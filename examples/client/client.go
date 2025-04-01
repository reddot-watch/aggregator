package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type FeedItem struct {
	ID           int64     `db:"id"`
	URL          string    `db:"url"`
	FeedID       int64     `db:"feed_id"`
	FeedMetadata []byte    `db:"feed_metadata"`
	Headline     string    `db:"headline"`
	Content      string    `db:"content"`
	PublishedAt  time.Time `db:"published_at"`
	CreatedAt    time.Time `db:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"`
}

// APIResponse structure expected from the server
// Match this with the server's api.APIResponse
type APIResponse struct {
	Items      []FeedItem `json:"items"`
	NextCursor *string    `json:"next_cursor"` // Pointer handles potential null
}

// --- Configuration ---
const (
	apiBaseURL      = "http://localhost:8080" // API server address
	endpoint        = "/v1/feed-items"
	pollingInterval = 5 * time.Minute // How often to check for new items
	requestTimeout  = 30 * time.Second
	limitPerPage    = 100             // How many items to fetch per API call
	overlapDelta    = 2 * time.Minute // Fetch items since (last_processed - delta)
	retentionHours  = 72              // Used for initial 'since' if no state
	serverApiKey    = ""              // API key for server authentication (empty means no auth)

	// Retry configuration
	maxRetries     = 3                      // Maximum number of retry attempts
	initialBackoff = 500 * time.Millisecond // Initial backoff duration
	maxBackoff     = 5 * time.Second        // Maximum backoff duration
	backoffFactor  = 2.0                    // Exponential factor for backoff

	// State database configuration
	stateDBPath       = "./client-state.db" // Path to SQLite database for client state
	stateKeyNextSince = "next_since"        // Key for storing next since timestamp
)

// Global variable for state (in a real app, persist this properly!)
var nextSince time.Time

// StateDB manages client state persistence
type StateDB struct {
	db     *sql.DB
	logger *log.Logger
}

// NewStateDB creates and initializes the state database
func NewStateDB(dbPath string, logger *log.Logger) (*StateDB, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory for state database: %w", err)
		}
	}

	// Open database connection
	db, err := sql.Open("sqlite3", dbPath+"?_journal=WAL&_synchronous=NORMAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("failed to open state database: %w", err)
	}

	// Create state table if it doesn't exist
	_, err = db.Exec(`
	CREATE TABLE IF NOT EXISTS client_state (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create state table: %w", err)
	}

	logger.Printf("State database initialized at %s", dbPath)
	return &StateDB{db: db, logger: logger}, nil
}

// Close closes the database connection
func (s *StateDB) Close() error {
	return s.db.Close()
}

// LoadSinceTime loads the next since timestamp from the database
func (s *StateDB) LoadSinceTime() (time.Time, error) {
	var zeroTime time.Time
	var timeStr string

	err := s.db.QueryRow("SELECT value FROM client_state WHERE key = ?", stateKeyNextSince).Scan(&timeStr)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// No state found, this is normal for first run
			s.logger.Println("No previous state found, starting fresh")
			return zeroTime, nil
		}
		return zeroTime, fmt.Errorf("failed to query state: %w", err)
	}

	// Parse the time string
	t, err := time.Parse(time.RFC3339, timeStr)
	if err != nil {
		return zeroTime, fmt.Errorf("failed to parse stored timestamp: %w", err)
	}

	s.logger.Printf("Loaded previous state: nextSince=%s", t.Format(time.RFC3339))
	return t, nil
}

// SaveSinceTime saves the next since timestamp to the database
func (s *StateDB) SaveSinceTime(t time.Time) error {
	if t.IsZero() {
		s.logger.Println("Not saving zero time")
		return nil
	}

	// Use UPSERT syntax to either insert or update the value
	_, err := s.db.Exec(`
		INSERT INTO client_state (key, value, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			updated_at = CURRENT_TIMESTAMP`,
		stateKeyNextSince, t.UTC().Format(time.RFC3339))

	if err != nil {
		return fmt.Errorf("failed to save state: %w", err)
	}

	s.logger.Printf("Saved state: nextSince=%s", t.UTC().Format(time.RFC3339))
	return nil
}

func main() {
	// Initialize random seed for backoff jitter
	rand.Seed(time.Now().UnixNano())

	logger := log.New(os.Stdout, "FEED-CLIENT | ", log.LstdFlags|log.Lmicroseconds)
	logger.Println("Starting Feed API Client...")

	// Initialize state database
	stateDB, err := NewStateDB(stateDBPath, logger)
	if err != nil {
		logger.Fatalf("Failed to initialize state database: %v", err)
	}
	defer stateDB.Close()

	// Load saved state
	savedTime, err := stateDB.LoadSinceTime()
	if err != nil {
		logger.Printf("Warning: Failed to load state, starting fresh: %v", err)
	}

	// --- Initialize State ---
	if !savedTime.IsZero() {
		nextSince = savedTime
		logger.Printf("Resuming with saved 'nextSince' timestamp: %s", nextSince.Format(time.RFC3339))
	} else if nextSince.IsZero() {
		nextSince = time.Now().Add(-retentionHours * time.Hour).UTC()
		logger.Printf("Initial 'nextSince' timestamp set to: %s", nextSince.Format(time.RFC3339))
	} else {
		logger.Printf("Using existing 'nextSince' timestamp: %s", nextSince.Format(time.RFC3339))
	}

	// --- Polling Loop ---
	ticker := time.NewTicker(pollingInterval)
	defer ticker.Stop()

	httpClient := &http.Client{
		Timeout: requestTimeout,
	}

	// Run once immediately, then wait for ticker
	if err := pollAndProcess(httpClient, logger, stateDB); err != nil {
		logger.Printf("ERROR during initial poll: %v", err)
	}

	// Set up signal handler for graceful shutdown
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case <-ticker.C:
			if err := pollAndProcess(httpClient, logger, stateDB); err != nil {
				logger.Printf("ERROR during poll: %v", err)
				// Errors are already logged with context
			}
		case sig := <-shutdown:
			logger.Printf("Received %s signal, shutting down...", sig)
			// Final state save before exit
			if err := stateDB.SaveSinceTime(nextSince); err != nil {
				logger.Printf("WARNING: Failed to save final state: %v", err)
			}
			logger.Println("Client shutdown complete")
			return
		}
	}
}

// pollAndProcess performs one full polling cycle: fetches all pages since the last run.
func pollAndProcess(client *http.Client, logger *log.Logger, stateDB *StateDB) error {
	ctx := context.Background() // Use a background context for now

	// Calculate the 'since' timestamp for this polling cycle, applying overlap
	currentSince := nextSince.Add(-overlapDelta)
	logger.Printf("Polling for items since %s (includes overlap)", currentSince.Format(time.RFC3339))

	var currentCursor *string // Use pointer to handle nil easily
	itemsProcessedInCycle := 0
	maxTsThisCycle := currentSince // Track max timestamp seen in this *cycle* for next 'since'

	// --- Pagination Loop ---
	for {
		// Build Request URL
		reqURL, err := buildRequestURL(currentSince, limitPerPage, currentCursor)
		if err != nil {
			return fmt.Errorf("failed to build request URL: %w", err)
		}

		// Make request with retry for transient errors
		var apiResp APIResponse
		err = retryWithBackoff(func() error {
			var fetchErr error
			apiResp, fetchErr = fetchItems(ctx, client, reqURL, logger)
			return fetchErr
		}, logger)

		if err != nil {
			return fmt.Errorf("failed to fetch items after %d retries: %w", maxRetries, err)
		}

		// --- Process Items (Placeholder) ---
		if len(apiResp.Items) == 0 {
			logger.Println("No more items found in this polling cycle.")
			break // Exit pagination loop
		}

		logger.Printf("Received %d items.", len(apiResp.Items))
		for _, item := range apiResp.Items {
			// !!! IMPORTANT: The downstream consumer MUST be idempotent !!!
			// This client only simulates publishing.

			// Simulate publishing to an event bus
			// logger.Printf("  Publishing item ID %d (URL: %s, Created: %s)", item.ID, item.URL, item.CreatedAt.Format(time.RFC3339))
			publishToEventBus(item) // Placeholder

			// Track the latest timestamp processed for the *next* cycle's 'since'
			if item.CreatedAt.After(maxTsThisCycle) {
				maxTsThisCycle = item.CreatedAt
			}
			itemsProcessedInCycle++
		}
		// --- End Processing ---

		// Prepare for next page or exit
		if apiResp.NextCursor == nil || *apiResp.NextCursor == "" {
			logger.Println("Reached end of pages for this cycle.")
			break // Exit pagination loop
		}

		// Use the cursor for the next iteration
		currentCursor = apiResp.NextCursor
		currentSince = time.Time{} // Ensure 'since' isn't used when cursor is present
	}
	// --- End Pagination Loop ---

	// Update state for the next polling interval
	// Only update if we actually processed items to avoid rewinding time on empty polls
	if itemsProcessedInCycle > 0 {
		if maxTsThisCycle.After(nextSince) {
			logger.Printf("Updating 'nextSince' timestamp to: %s (was %s)", maxTsThisCycle.UTC().Format(time.RFC3339), nextSince.UTC().Format(time.RFC3339))
			nextSince = maxTsThisCycle.UTC()

			// Persist nextSince to database
			if err := stateDB.SaveSinceTime(nextSince); err != nil {
				logger.Printf("WARNING: Failed to save state: %v", err)
				// Continue processing despite save failure
			}
		} else {
			// This might happen if overlap fetched items but no new ones arrived
			logger.Printf("No items processed with timestamp later than current 'nextSince' (%s), state not updated.", nextSince.UTC().Format(time.RFC3339))
		}
	} else {
		logger.Println("No items processed in this cycle, 'nextSince' remains unchanged.")
	}

	return nil
}

// buildRequestURL constructs the URL with appropriate query parameters.
func buildRequestURL(since time.Time, limit int, cursor *string) (string, error) {
	baseURL, err := url.Parse(apiBaseURL)
	if err != nil {
		return "", fmt.Errorf("invalid base API URL: %w", err)
	}

	endpointURL, err := baseURL.Parse(endpoint) // Use Parse to handle relative paths correctly
	if err != nil {
		return "", fmt.Errorf("invalid endpoint path: %w", err)
	}

	query := url.Values{}
	query.Set("limit", strconv.Itoa(limit))

	if cursor != nil && *cursor != "" {
		query.Set("cursor", *cursor)
	} else if !since.IsZero() {
		// Format timestamp consistently in UTC using RFC3339
		query.Set("since", since.UTC().Format(time.RFC3339))
	} else {
		// This case shouldn't be reached if logic is correct, but prevents empty query
		return "", fmt.Errorf("internal error: neither cursor nor valid since timestamp provided")
	}

	endpointURL.RawQuery = query.Encode()
	return endpointURL.String(), nil
}

// publishToEventBus is a placeholder for the actual event bus publishing logic.
func publishToEventBus(item FeedItem) {
	// In a real application, this would:
	// 1. Marshal the item (or just its ID) into the event bus message format (e.g., JSON, Protobuf).
	// 2. Get an event bus client (e.g., Kafka producer, Pub/Sub client).
	// 3. Publish the message to the appropriate topic/queue.
	// 4. Handle publishing errors (e.g., retries, logging).
	// log.Printf("      -> (Placeholder) Published item %d to Event Bus.", item.ID)
}

// fetchItems makes a single HTTP request and returns the parsed API response
func fetchItems(ctx context.Context, client *http.Client, reqURL string, logger *log.Logger) (APIResponse, error) {
	var apiResp APIResponse

	// Create and Execute Request
	reqCtx, cancelReq := context.WithTimeout(ctx, requestTimeout)
	defer cancelReq() // Ensure context is always canceled when function returns

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, reqURL, nil)
	if err != nil {
		return apiResp, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	// Add API key header if configured
	if serverApiKey != "" {
		req.Header.Set("X-API-Key", serverApiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return apiResp, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close() // Always close body

	// Read response body
	bodyBytes, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return apiResp, fmt.Errorf("failed to read response body (status %d): %w", resp.StatusCode, readErr)
	}

	if resp.StatusCode != http.StatusOK {
		return apiResp, fmt.Errorf("API returned non-200 status: %d - Body: %s", resp.StatusCode, string(bodyBytes))
	}

	// Decode JSON Response
	if err := json.Unmarshal(bodyBytes, &apiResp); err != nil {
		return apiResp, fmt.Errorf("failed to decode JSON response: %w - Body: %s", err, string(bodyBytes))
	}

	return apiResp, nil
}

// retryWithBackoff executes the given function with exponential backoff on failure
func retryWithBackoff(fn func() error, logger *log.Logger) error {
	var err error
	backoff := initialBackoff

	for attempt := 0; attempt <= maxRetries; attempt++ {
		err = fn()
		if err == nil {
			return nil // Success, no need to retry
		}

		if attempt == maxRetries {
			break
		}

		if !isRetriableError(err) {
			logger.Printf("Non-retriable error: %v", err)
			return err
		}

		// Wait before retrying with exponential backoff
		retryDelay := time.Duration(float64(backoff) * (1.0 + 0.2*rand.Float64())) // Add jitter
		logger.Printf("Transient error: %v. Retrying in %v (attempt %d/%d)",
			err, retryDelay.Round(time.Millisecond), attempt+1, maxRetries)
		time.Sleep(retryDelay)

		// Increase backoff for next attempt, capped at maxBackoff
		backoff = time.Duration(float64(backoff) * backoffFactor)
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}

	return err // Return last error encountered
}

// isRetriableError determines if an error should be retried
func isRetriableError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Temporary() || netErr.Timeout()
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	if errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.EPIPE) {
		return true
	}

	// Retry on HTTP 5xx server errors, HTTP 429 Too Many Requests
	errMsg := err.Error()
	return strings.Contains(errMsg, "API returned non-200 status: 5") || // 5xx errors
		strings.Contains(errMsg, "API returned non-200 status: 429") // Rate limiting
}
