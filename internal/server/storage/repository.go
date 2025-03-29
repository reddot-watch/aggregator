package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"reddot-watch/aggregator/internal/database"
	"reddot-watch/aggregator/internal/models"
)

// FeedItemRepository defines operations for accessing feed items.
type FeedItemRepository interface {
	FetchFeedItems(ctx context.Context, limit int, since *time.Time, cursorTimestamp *time.Time, cursorID *int64) ([]models.FeedItem, error)
}

// sqlxRepository implements FeedItemRepository using sqlx.
type sqlxRepository struct {
	db *database.DB
}

// NewRepository creates a new repository instance.
func NewRepository(db *database.DB) FeedItemRepository {
	return &sqlxRepository{db: db}
}

// FetchFeedItems retrieves feed items based on time or cursor.
func (r *sqlxRepository) FetchFeedItems(ctx context.Context, limit int, since *time.Time, cursorTimestamp *time.Time, cursorID *int64) ([]models.FeedItem, error) {
	var items []models.FeedItem
	var query string
	var args []any

	// We must order consistently for cursor pagination to work.
	// Fetching items CREATED strictly AFTER a certain point.
	const baseQuery = `SELECT * FROM feed_items `
	const orderBy = ` ORDER BY created_at ASC, id ASC LIMIT ?`

	if cursorTimestamp != nil && cursorID != nil {
		// Paginate using cursor (timestamp and ID of the last item from previous page)
		query = baseQuery + `WHERE (created_at > ?) OR (created_at = ? AND id > ?)` + orderBy
		args = append(args, cursorTimestamp.UTC(), cursorTimestamp.UTC(), *cursorID, limit)
		// log.Printf("DEBUG: Fetching with cursor: T=%v, ID=%d, Limit=%d", cursorTimestamp.UTC().Format(time.RFC3339Nano), *cursorID, limit)
	} else if since != nil {
		// First page request using 'since' timestamp
		query = baseQuery + `WHERE created_at > ?` + orderBy
		args = append(args, since.UTC(), limit)
		// log.Printf("DEBUG: Fetching with since: T=%v, Limit=%d", since.UTC().Format(time.RFC3339Nano), limit)
	} else {
		// Should not happen if handler validates properly, but return error just in case.
		return nil, fmt.Errorf("either 'since' or cursor parameters must be provided")
	}

	err := r.db.SelectContext(ctx, &items, query, args...)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []models.FeedItem{}, nil // Return empty slice, not error
		}
		// Consider logging the error here
		return nil, fmt.Errorf("database query failed: %w", err)
	}

	return items, nil
}
