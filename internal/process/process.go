package process

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/rs/zerolog/log"

	"github.com/reddot-watch/feedfetcher"
	"reddot-watch/aggregator/internal/database"
	dbmodels "reddot-watch/aggregator/internal/models"
)

// FeedProcessor handles parallel processing of RSS feeds
type FeedProcessor struct {
	db           *database.DB
	fetcher      *feedfetcher.FeedFetcher
	WorkerCount  int
	feedQueue    chan dbmodels.Feed
	itemQueue    chan dbmodels.FeedItem
	dbWriteQueue chan dbmodels.FeedItem
	errorQueue   chan error

	workerWg    sync.WaitGroup
	processorWg sync.WaitGroup
	processed   atomic.Int64
	duplicates  atomic.Int64

	// Counters for current processing state
	activeWorkers    atomic.Int32
	activeProcessors atomic.Int32
	activeWriters    atomic.Int32
	currentBatchSize atomic.Int32

	// Configuration for feed_items DB batching
	batchSize    int
	batchTimeout time.Duration
}

const (
	defaultBatchSize    = 100
	defaultBatchTimeout = 2 * time.Second
	maxFeedFailures     = 10
)

// NewFeedProcessor creates a new feed processor using an existing database connection
func NewFeedProcessor(db *database.DB, workerCount int) (*FeedProcessor, error) {
	if workerCount <= 0 {
		workerCount = runtime.NumCPU()
	}
	if db == nil {
		return nil, fmt.Errorf("database connection cannot be nil")
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("database connection is not valid: %w", err)
	}

	fetcher := feedfetcher.NewFeedFetcher(feedfetcher.Config{
		UserAgent:            "MyFeedReader/1.0",
		RequestTimeout:       15 * time.Second,
		MaxItems:             100,
		MaxHeadingLength:     200,
		MaxAge:               48 * time.Hour,
		FutureDriftTolerance: 12 * time.Hour,
	})

	itemQueueSize := workerCount * 5
	dbWriteQueueSize := itemQueueSize * 2

	return &FeedProcessor{
		db:           db,
		fetcher:      fetcher,
		WorkerCount:  workerCount,
		feedQueue:    make(chan dbmodels.Feed, workerCount*2),
		itemQueue:    make(chan dbmodels.FeedItem, itemQueueSize),
		dbWriteQueue: make(chan dbmodels.FeedItem, dbWriteQueueSize),
		errorQueue:   make(chan error, workerCount),
		batchSize:    defaultBatchSize,
		batchTimeout: defaultBatchTimeout,
	}, nil
}

// ProcessFeeds fetches active feeds from the DB and processes them in parallel.
func (p *FeedProcessor) ProcessFeeds(ctx context.Context) error {
	progressTicker := time.NewTicker(5 * time.Minute)
	defer progressTicker.Stop()

	// goroutine to log progress
	go func() {
		for {
			select {
			case <-progressTicker.C:
				processed, duplicates := p.Stats()
				log.Info().
					Int64("processed", processed).
					Int64("duplicates", duplicates).
					Int32("active_workers", p.activeWorkers.Load()).
					Int32("active_processors", p.activeProcessors.Load()).
					Int32("active_writers", p.activeWriters.Load()).
					Int32("current_batch_size", p.currentBatchSize.Load()).
					Int("feed_queue_size", len(p.feedQueue)).
					Int("item_queue_size", len(p.itemQueue)).
					Int("db_write_queue_size", len(p.dbWriteQueue)).
					Msg("Processing progress")
			case <-ctx.Done():
				return
			}
		}
	}()

	var processWg sync.WaitGroup // WaitGroup for the main pipeline stages

	errChan := make(chan error, 1) // Channel to collect the first error
	go func() {
		var firstErr error
		for err := range p.errorQueue {
			if err != nil {
				log.Error().
					Err(err).
					Msg("Error occurred")
				// Only store critical errors (database connection, etc.)
				if firstErr == nil && strings.Contains(err.Error(), "database") {
					firstErr = err
				}
			}
		}
		errChan <- firstErr
		close(errChan)
	}()

	// Start pipeline stages (workers, processors, writer)
	processWg.Add(1)
	go func() {
		defer processWg.Done()
		for i := 0; i < p.WorkerCount; i++ {
			p.workerWg.Add(1)
			go p.feedWorker(ctx)
		}
		p.workerWg.Wait()
		close(p.itemQueue)
		log.Info().Msg("All feed workers finished.")
	}()

	processWg.Add(1)
	go func() {
		defer processWg.Done()
		for i := 0; i < p.WorkerCount; i++ {
			p.processorWg.Add(1)
			go p.itemProcessor(ctx)
		}
		p.processorWg.Wait()
		close(p.dbWriteQueue)
		log.Info().Msg("All item processors finished.")
	}()

	processWg.Add(1)
	go func() {
		defer processWg.Done()
		p.databaseWriter(ctx)
		log.Info().Msg("Database writer finished.")
	}()

	// Load active feeds from the database
	var feedsToProcess []dbmodels.Feed
	query := "SELECT * FROM feeds WHERE status = 'active' AND deleted_at IS NULL ORDER BY last_retrieved_at ASC, created_at ASC"
	err := p.db.SelectContext(ctx, &feedsToProcess, query)
	if err != nil {
		// Critical error: cannot load feeds, initiate shutdown
		log.Error().
			Err(err).
			Msg("Critical error loading feeds from database")
		close(p.feedQueue) // Signal workers there's no work (they will exit)
		// Need to wait for pipeline stages to attempt graceful shutdown
		processWg.Wait()
		close(p.errorQueue) // Close error queue after stages finish

		// Wait for error collector and combine errors if needed
		if collectedErr := <-errChan; collectedErr != nil {
			return fmt.Errorf("failed to load feeds: %w (additional error: %v)", err, collectedErr)
		}
		return fmt.Errorf("failed to load feeds: %w", err)
	}
	log.Info().
		Int("loaded_feeds", len(feedsToProcess)).
		Msg("Loaded active feeds to process.")

	// Queue the loaded feeds
feedLoop:
	for _, feed := range feedsToProcess {
		feedToSend := feed // Make a copy for the channel
		select {
		case p.feedQueue <- feedToSend:
		case <-ctx.Done():
			log.Info().
				Err(ctx.Err()).
				Msg("Context cancelled during feed queuing")
			break feedLoop
		}
	}
	// Signal that no more feeds will be added.
	close(p.feedQueue)
	log.Info().Msg("Finished queueing feeds.")

	// Wait for pipeline stage launchers/writer to complete their shutdown logic
	processWg.Wait()
	log.Info().Msg("All processing stages complete.")

	// Close error queue *after* all goroutines that might write to it are done
	close(p.errorQueue)

	// Wait for the error collector and return result
	finalErr := <-errChan
	log.Info().Msg("Error collector finished.")
	return finalErr
}

// feedWorker receives Feed structs, fetches items, updates feed status, and queues items.
func (p *FeedProcessor) feedWorker(ctx context.Context) {
	defer p.workerWg.Done() // *** Use workerWg ***
	p.activeWorkers.Add(1)
	defer p.activeWorkers.Add(-1)
	log.Debug().Msg("Feed worker started")

	for {
		select {
		case feed, ok := <-p.feedQueue:
			if !ok {
				log.Debug().Msg("Feed worker exiting (feedQueue closed)")
				return
			}

			feedCtx, cancelFeedCtx := context.WithTimeout(ctx, time.Minute*2)

			log.Info().
				Int64("feed_id", feed.ID).
				Str("url", feed.URL).
				Msg("Processing feed")

			items, fetchErr := p.fetcher.FetchAndProcess(feedCtx, feed.URL)

			updateCtx, cancelUpdateCtx := context.WithTimeout(ctx, 15*time.Second) // Short timeout for DB update
			now := time.Now()
			var updateErr error

			if fetchErr != nil {
				// Handle specific errors if necessary (e.g., rate limiting)
				if strings.Contains(fetchErr.Error(), "429") {
					log.Warn().
						Int64("feed_id", feed.ID).
						Str("url", feed.URL).
						Msg("Rate limited by feed, marking for later retry")
					feed.Status = "rate_limited" // Custom status?
					feed.LastError = sql.NullString{String: "Rate limited by feed", Valid: true}
					// TODO: Don't increment failures for rate limits?
				} else {
					feed.FailuresCount++
					feed.LastError = sql.NullString{String: fetchErr.Error(), Valid: true}
					if feed.FailuresCount > maxFeedFailures {
						feed.Status = "failed"
					}
				}
				_, updateErr = p.db.ExecContext(updateCtx, `
					UPDATE feeds
					SET status = ?, failures_count = ?, last_error = ?, last_retrieved_at = ?, updated_at = ?
					WHERE id = ?`,
					feed.Status, feed.FailuresCount, feed.LastError, now, now, feed.ID)
				p.sendError(fmt.Errorf("error fetching feed %d (%s): %w", feed.ID, feed.URL, fetchErr))
			} else {
				// Reset failures on success
				_, updateErr = p.db.ExecContext(updateCtx, `
					UPDATE feeds
					SET status = 'active', failures_count = 0, last_error = NULL, last_retrieved_at = ?, updated_at = ?
					WHERE id = ?`,
					now, now, feed.ID)
			}
			cancelUpdateCtx() // Release update context resources

			if updateErr != nil {
				p.sendError(fmt.Errorf("failed to update feed status for feed %d (%s): %w", feed.ID, feed.URL, updateErr))
				// TODO: Decide if this error prevents further processing for this feed
			}

			// If fetching failed, don't process items for this feed
			if fetchErr != nil {
				cancelFeedCtx() // Release feed processing context early
				continue        // Move to the next feed
			}

			if len(items) > 0 {
				log.Info().
					Int64("feed_id", feed.ID).
					Str("url", feed.URL).
					Int("items", len(items)).
					Msg("Feed processed successfully, queuing items")
			}

			metadataMap := make(map[string]interface{})
			metadataMap["feed_url"] = feed.URL
			if feed.Language.Valid {
				metadataMap["language"] = feed.Language.String
			}
			if feed.Comments.Valid {
				metadataMap["comments"] = feed.Comments.String
			}
			feedMetadataJSON, jsonErr := json.Marshal(metadataMap)
			if jsonErr != nil {
				p.sendError(fmt.Errorf("failed to marshal feed metadata for feed %d (%s): %w", feed.ID, feed.URL, jsonErr))
				cancelFeedCtx()
				continue // Skip items if metadata fails
			}

			for _, item := range items {
				if item.URL == "" {
					p.sendError(fmt.Errorf("item from feed %d (%s) has empty URL", feed.ID, feed.URL))
					continue
				}
				feedItem := dbmodels.FeedItem{
					URL:          item.URL,
					FeedID:       feed.ID,
					FeedMetadata: feedMetadataJSON,
					Headline:     item.Headline,
					Content:      item.Content,
					PublishedAt:  item.PublishedAt,
					CreatedAt:    time.Now(),
					UpdatedAt:    time.Now(),
				}

				select {
				case p.itemQueue <- feedItem:
				case <-feedCtx.Done(): // Check per-feed timeout
					log.Warn().
						Int64("feed_id", feed.ID).
						Err(feedCtx.Err()).
						Msg("Feed worker cancelling item queueing due to feed context")
					goto EndFeedProcessing // Use goto to break out and cancel context
				case <-ctx.Done(): // Check outer context
					log.Info().
						Int64("feed_id", feed.ID).
						Err(ctx.Err()).
						Msg("Feed worker cancelling item queueing due to outer context")
					goto EndFeedProcessing
				}
			}

		EndFeedProcessing:
			cancelFeedCtx() // Release feed processing context resources

		case <-ctx.Done():
			log.Info().
				Err(ctx.Err()).
				Msg("Feed worker cancelling")
			return
		}
	}
}

// itemProcessor reads FeedItems from itemQueue and passes them to dbWriteQueue.
func (p *FeedProcessor) itemProcessor(ctx context.Context) {
	defer p.processorWg.Done()
	p.activeProcessors.Add(1)
	defer p.activeProcessors.Add(-1)
	log.Debug().Msg("Item processor started")

	for {
		select {
		case item, ok := <-p.itemQueue:
			if !ok {
				log.Debug().Msg("Item processor exiting (itemQueue closed)")
				return // Exit when itemQueue is closed and drained
			}
			// Pass the item to the database writer queue
			select {
			case p.dbWriteQueue <- item:
				// Item successfully queued for writing
			case <-ctx.Done():
				log.Info().
					Err(ctx.Err()).
					Str("item_url", item.URL).
					Msg("Item processor cancelling during DB queueing")
				return // Exit if context is cancelled
			}
		case <-ctx.Done():
			log.Info().
				Err(ctx.Err()).
				Msg("Item processor cancelling")
			return // Exit if context is cancelled
		}
	}
}

// databaseWriter handles batch inserts into the 'feed_items' table.
func (p *FeedProcessor) databaseWriter(ctx context.Context) {
	log.Info().Msg("Database writer for feed_items started")
	p.activeWriters.Add(1)
	defer p.activeWriters.Add(-1)

	batch := make([]dbmodels.FeedItem, 0, p.batchSize)
	ticker := time.NewTicker(p.batchTimeout)
	defer ticker.Stop()

	for {
		select {
		case item, ok := <-p.dbWriteQueue:
			if !ok {
				log.Info().Msg("DB writer: dbWriteQueue closed, processing final batch")
				if len(batch) > 0 {
					p.processBatch(ctx, batch)
				}
				log.Info().Msg("DB writer for feed_items exiting")
				return
			}

			batch = append(batch, item)
			p.currentBatchSize.Store(int32(len(batch)))

			if len(batch) >= p.batchSize {
				p.processBatch(ctx, batch)
				batch = make([]dbmodels.FeedItem, 0, p.batchSize)
				p.currentBatchSize.Store(0)
				ticker.Reset(p.batchTimeout)
			}

		case <-ticker.C:
			if len(batch) > 0 {
				p.processBatch(ctx, batch)
				batch = make([]dbmodels.FeedItem, 0, p.batchSize)
				p.currentBatchSize.Store(0)
			}

		case <-ctx.Done():
			log.Info().
				Err(ctx.Err()).
				Msg("DB writer: Context cancelled, processing final batch")
			if len(batch) > 0 {
				p.processBatch(ctx, batch)
			}
			log.Info().Msg("DB writer for feed_items exiting due to context cancellation")
			return
		}
	}
}

func (p *FeedProcessor) processBatch(ctx context.Context, batch []dbmodels.FeedItem) {
	if len(batch) == 0 {
		return
	}

	tx, err := p.db.BeginTxx(ctx, nil)
	if err != nil {
		p.sendError(fmt.Errorf("feed_items writer: failed to begin transaction: %w", err))
		return
	}
	defer tx.Rollback()

	stmt, err := tx.PreparexContext(ctx, `
		INSERT INTO feed_items (url, feed_id, feed_metadata, headline, content, published_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(url) DO NOTHING;`)
	if err != nil {
		p.sendError(fmt.Errorf("feed_items writer: failed to prepare batch insert: %w", err))
		return
	}
	defer stmt.Close()

	processedInBatch := 0
	duplicatesInBatch := 0

	for _, item := range batch {
		res, err := stmt.ExecContext(ctx,
			item.URL, item.FeedID, item.FeedMetadata,
			item.Headline, item.Content, item.PublishedAt,
		)
		if err != nil {
			p.sendError(fmt.Errorf("feed_items writer: failed to insert item %s: %w", item.URL, err))
			continue
		}
		rowsAffected, err := res.RowsAffected()
		if err != nil {
			p.sendError(fmt.Errorf("feed_items writer: failed get rows affected for %s: %w", item.URL, err))
			duplicatesInBatch++
			continue
		}
		if rowsAffected > 0 {
			processedInBatch++
		} else {
			duplicatesInBatch++
			log.Debug().
				Str("url", item.URL).
				Int64("feed_id", item.FeedID).
				Time("published_at", item.PublishedAt).
				Msg("Duplicate URL detected")
		}
	}

	if err := tx.Commit(); err != nil {
		p.sendError(fmt.Errorf("feed_items writer: failed to commit transaction: %w", err))
		return
	}

	p.processed.Add(int64(processedInBatch))
	p.duplicates.Add(int64(duplicatesInBatch))

	log.Info().
		Int("processed", processedInBatch).
		Int("duplicates", duplicatesInBatch).
		Msg("Batch processed")
}

// sendError sends an error to the error queue without blocking.
func (p *FeedProcessor) sendError(err error) {
	if err == nil {
		return
	}
	// Non-blocking send to error queue
	select {
	case p.errorQueue <- err:
	default:
		// Log if the error queue is full to avoid blocking the sender
		log.Error().
			Err(err).
			Msg("Error queue full, logging error instead of queuing")
	}
}

// Close releases resources (but DB connection managed externally).
func (p *FeedProcessor) Close() {
	// Placeholder: If other resources needed closing, do it here.
	log.Info().Msg("FeedProcessor resources released (DB connection managed externally).")
}

// Stats returns processing statistics for feed_items.
func (p *FeedProcessor) Stats() (processed, duplicates int64) {
	processed = p.processed.Load()
	duplicates = p.duplicates.Load()
	return
}

// PurgeOldItems removes items from feed_items older than the specified retention days.
func (p *FeedProcessor) PurgeOldItems(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, fmt.Errorf("retentionDays must be positive")
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
	// Use format compatible with SQLite query
	cutoffStr := cutoff.Format("2006-01-02 15:04:05")

	log.Info().
		Str("cutoff_date", cutoffStr).
		Int("retention_days", retentionDays).
		Msg("Purging old items from feed_items")

	result, err := p.db.ExecContext(ctx,
		"DELETE FROM feed_items WHERE created_at < ?", cutoffStr)
	if err != nil {
		return 0, fmt.Errorf("failed to execute purge command on feed_items: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		// Log warning but don't necessarily return error - purge might have partially worked?
		log.Warn().
			Err(err).
			Msg("Warning: Could not get RowsAffected after purging feed_items")
		return 0, nil // Or return the error: fmt.Errorf("failed to get rows affected after purge: %w", err)
	}

	log.Info().
		Int64("rows_affected", rowsAffected).
		Msg("Purged old items from feed_items.")
	return rowsAffected, nil
}
