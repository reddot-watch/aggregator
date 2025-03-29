package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/rs/zerolog/hlog"

	"reddot-watch/aggregator/internal/models"
	"reddot-watch/aggregator/internal/server/pagination"
	"reddot-watch/aggregator/internal/server/storage"
)

const defaultLimit = 100
const maxLimit = 1000
const iso8601Format = time.RFC3339

// Response structure for the feed items endpoint
type Response struct {
	Items      []models.FeedItem `json:"items"`
	NextCursor *string           `json:"next_cursor,omitempty"`
}

// FeedItemsHandler holds dependencies for the API handler.
// No longer needs to store logger, retrieves from request context.
type FeedItemsHandler struct {
	repo storage.FeedItemRepository
}

// NewFeedItemsHandler creates a new handler instance.
func NewFeedItemsHandler(repo storage.FeedItemRepository) *FeedItemsHandler {
	return &FeedItemsHandler{
		repo: repo,
	}
}

// GetFeedItems handles requests to fetch feed items.
func (h *FeedItemsHandler) GetFeedItems(w http.ResponseWriter, r *http.Request) {
	log := hlog.FromRequest(r)
	log.Debug().Msg("Processing feed items request")

	if r.Method != http.MethodGet {
		log.Warn().Str("method", r.Method).Msg("Method not allowed")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	query := r.URL.Query()
	limitStr := query.Get("limit")
	sinceStr := query.Get("since")
	cursorStr := query.Get("cursor")

	limit := defaultLimit
	if limitStr != "" {
		parsedLimit, err := strconv.Atoi(limitStr)
		if err != nil || parsedLimit <= 0 || parsedLimit > maxLimit {
			log.Warn().Err(err).Str("limit", limitStr).Msg("Invalid 'limit' parameter value")
			http.Error(w, fmt.Sprintf("Invalid 'limit' parameter: must be between 1 and %d", maxLimit), http.StatusBadRequest)
			return
		}
		limit = parsedLimit
	}

	var since *time.Time
	var cursorTimestamp *time.Time
	var cursorID *int64

	if cursorStr != "" {
		ts, id, err := pagination.DecodeCursor(cursorStr)
		if err != nil {
			log.Warn().Err(err).Str("cursor", cursorStr).Msg("Invalid 'cursor' parameter")
			http.Error(w, "Invalid 'cursor' parameter", http.StatusBadRequest)
			return
		}
		cursorTimestamp = &ts
		cursorID = &id
	} else if sinceStr != "" {
		parsedSince, err := time.Parse(iso8601Format, sinceStr)
		if err != nil {
			log.Warn().Err(err).Str("since", sinceStr).Msg("Invalid 'since' parameter format")
			http.Error(w, "Invalid 'since' parameter: use RFC3339 format (e.g., 2025-03-28T15:00:00Z)", http.StatusBadRequest)
			return
		}
		utcSince := parsedSince.UTC()
		since = &utcSince
	} else {
		log.Warn().Msg("Missing required parameter: 'since' or 'cursor'")
		http.Error(w, "Missing required parameter: 'since' or 'cursor'", http.StatusBadRequest)
		return
	}

	items, err := h.repo.FetchFeedItems(ctx, limit+1, since, cursorTimestamp, cursorID) // Fetch one extra
	if err != nil {

		errLogEvent := log.Error().Err(err)
		if since != nil {
			errLogEvent = errLogEvent.Time("since", *since)
		}
		errLogEvent.Str("cursor", cursorStr).Msg("Error fetching feed items from repository")

		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var nextCursorStr *string
	hasNextPage := len(items) > limit
	actualItems := items
	if hasNextPage {
		actualItems = items[:limit]
		if len(actualItems) > 0 {
			lastItem := actualItems[len(actualItems)-1]
			cursor := pagination.EncodeCursor(lastItem.CreatedAt.UTC(), lastItem.ID)
			nextCursorStr = &cursor
		}
	}

	response := Response{
		Items:      actualItems,
		NextCursor: nextCursorStr,
	}

	jsonBytes, err := json.Marshal(response)
	if err != nil {
		log.Error().Err(err).Msg("Error marshaling JSON response")
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK) // Now it's safe to send 200 OK
	_, writeErr := w.Write(jsonBytes)
	if writeErr != nil {
		log.Error().Err(writeErr).Msg("Error writing JSON response body to client")
		// Cannot reliably send a different status code here.
	}
	log.Debug().Int("bytes_written", len(jsonBytes)).Msg("Response completed")
}
