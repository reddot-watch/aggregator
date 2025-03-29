package models

import (
	"database/sql"
	"time"
)

// Feed represents a row in the 'feeds' table
type Feed struct {
	ID              int64          `db:"id"`
	URL             string         `db:"url"`
	Comments        sql.NullString `db:"comments"`
	Language        sql.NullString `db:"language"`
	Status          string         `db:"status"`
	FailuresCount   int            `db:"failures_count"`
	LastError       sql.NullString `db:"last_error"`
	LastRetrievedAt sql.NullTime   `db:"last_retrieved_at"`
	CreatedAt       time.Time      `db:"created_at"`
	UpdatedAt       time.Time      `db:"updated_at"`
	DeletedAt       sql.NullTime   `db:"deleted_at"`
}

// NewFeed creates a new Feed with default values
func NewFeed() *Feed {
	now := time.Now()
	return &Feed{
		Status:    "active",
		CreatedAt: now,
		UpdatedAt: now,
	}
}
