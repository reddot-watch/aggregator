package models

import "time"

// FeedItem represents a row in the feed_items table
type FeedItem struct {
	ID           int64     `db:"id"`
	URL          string    `db:"url"`
	FeedID       int64     `db:"feed_id"`       // ID from the 'feeds' table
	FeedMetadata []byte    `db:"feed_metadata"` // JSON marshaled metadata (e.g., {"feed_url": "...", "language": "en"})
	Headline     string    `db:"headline"`
	Content      string    `db:"content"`
	PublishedAt  time.Time `db:"published_at"`
	CreatedAt    time.Time `db:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"`
}

// NewFeedItem creates a new FeedItem with default values
func NewFeedItem() *FeedItem {
	now := time.Now()
	return &FeedItem{
		CreatedAt: now,
		UpdatedAt: now,
	}
}
