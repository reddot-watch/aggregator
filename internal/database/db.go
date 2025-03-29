package database

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/mattn/go-sqlite3"
	"github.com/rs/zerolog/log"

	"reddot-watch/aggregator/internal/database/migrations"
	"reddot-watch/aggregator/internal/models"
)

// DB represents the database connection
type DB struct {
	*sqlx.DB
}

// NewDB creates a new database connection with optimized settings
func NewDB(cfg *Config) (*DB, error) {
	dir := filepath.Dir(cfg.DBPath)
	if dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory for database: %w", err)
		}
	}

	if cfg.WorkerCount <= 0 {
		cfg.WorkerCount = defaultWorkerCount
	}
	if cfg.MaxIdleConns <= 0 {
		cfg.MaxIdleConns = defaultMaxIdleConns
	}
	if cfg.MaxOpenConns <= 0 {
		cfg.MaxOpenConns = defaultMaxOpenConns
	}

	// Connect to database with optimized settings for concurrency
	// WAL mode allows concurrent reads while writing
	dsn := fmt.Sprintf("%s?_journal=WAL&_synchronous=NORMAL&_busy_timeout=%d",
		cfg.DBPath, cfg.BusyTimeoutMS)

	if cfg.ReadOnly {
		dsn += "&mode=ro"
		log.Info().Str("path", cfg.DBPath).Msg("Opening database in Read-Only mode (from config)")
	} else {
		log.Info().Str("path", cfg.DBPath).Msg("Opening database in Read-Write mode (from config)")
	}

	db, err := sqlx.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	// Apply optimal SQLite PRAGMAs
	// Journal/Sync/Timeout set via DSN
	// Use slightly different pragmas based on mode
	var pragmas []string
	if cfg.ReadOnly {
		pragmas = []string{
			fmt.Sprintf("PRAGMA cache_size = %d;", cfg.CacheSizeKB), // Cache good for reads
			"PRAGMA temp_store = MEMORY;",
			"PRAGMA query_only = ON;",    // Extra safety for read-only
			"PRAGMA foreign_keys = OFF;", // Not strictly needed for reads
		}
	} else {
		pragmas = []string{
			fmt.Sprintf("PRAGMA cache_size = %d;", cfg.CacheSizeKB),
			"PRAGMA temp_store = MEMORY;",
			"PRAGMA foreign_keys = ON;",
		}
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			log.Warn().Err(err).Str("pragma", pragma).Str("mode", modeStr(cfg.ReadOnly)).Msg("Failed to set PRAGMA")
		}
	}

	if !cfg.ReadOnly {
		log.Info().Msg("Running database migrations...")
		migrationsDir := filepath.Join("internal", "database", "migrations")
		migrationFiles, err := migrations.LoadMigrations(migrationsDir)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to load migrations: %w", err)
		}

		if err := migrations.RunMigrations(db.DB, migrationFiles); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to run migrations: %w", err)
		}
		log.Info().Msg("Database migrations completed successfully")
	} else {
		log.Info().Msg("Skipping migrations for read-only connection (from config).")
	}

	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping db (%s): %w", modeStr(cfg.ReadOnly), err)
	}

	log.Info().Str("mode", modeStr(cfg.ReadOnly)).Msg("Database connection successful")
	return &DB{db}, nil
}

// Helper for logging
func modeStr(readOnly bool) string {
	if readOnly {
		return "read-only"
	}
	return "read-write"
}

// InsertFeed inserts a new feed into the database
func (db *DB) InsertFeed(feed *models.Feed) error {
	stmt, err := db.DB.Prepare(`
		INSERT INTO feeds (url, comments, language, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(
		feed.URL,
		feed.Comments,
		feed.Language,
		feed.Status,
		feed.CreatedAt.Format("2006-01-02 15:04:05"),
		feed.UpdatedAt.Format("2006-01-02 15:04:05"),
	)
	return err
}

// DeleteDB removes the database file if it exists
func DeleteDB(dbPath string) error {
	if _, err := os.Stat(dbPath); err == nil {
		return os.Remove(dbPath)
	}
	return nil
}
