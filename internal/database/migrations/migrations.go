package migrations

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"
)

// Migration represents a database migration
type Migration struct {
	Version int
	Up      string
	Down    string
}

// LoadMigrations loads all migration files from the migrations directory
func LoadMigrations(migrationsDir string) ([]Migration, error) {
	var migrations []Migration
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read migrations directory: %w", err)
	}

	// Group files by version
	versionFiles := make(map[int]struct {
		up   string
		down string
	})

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}

		var version int
		var direction string
		_, err := fmt.Sscanf(name, "%d_%s", &version, &direction)
		if err != nil {
			log.Warn().Err(err).Str("file", name).Msg("Skipping invalid migration file")
			continue
		}

		content, err := os.ReadFile(filepath.Join(migrationsDir, name))
		if err != nil {
			return nil, fmt.Errorf("failed to read migration file %s: %w", name, err)
		}

		v := versionFiles[version]
		if strings.HasSuffix(direction, ".up.sql") {
			v.up = string(content)
		} else if strings.HasSuffix(direction, ".down.sql") {
			v.down = string(content)
		}
		versionFiles[version] = v
	}

	// Convert to slice and sort by version
	for version, files := range versionFiles {
		migrations = append(migrations, Migration{
			Version: version,
			Up:      files.up,
			Down:    files.down,
		})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	log.Debug().
		Int("count", len(migrations)).
		Msg("Loaded migrations")

	return migrations, nil
}

// RunMigrations executes all pending migrations
func RunMigrations(db *sql.DB, migrations []Migration) error {
	// Create migrations table if it doesn't exist
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS migrations (
			version INTEGER PRIMARY KEY,
			applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create migrations table: %w", err)
	}

	// Get applied migrations
	rows, err := db.Query("SELECT version FROM migrations ORDER BY version")
	if err != nil {
		return fmt.Errorf("failed to query applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return fmt.Errorf("failed to scan migration version: %w", err)
		}
		applied[version] = true
	}

	// Run pending migrations
	for _, migration := range migrations {
		if applied[migration.Version] {
			log.Debug().
				Int("version", migration.Version).
				Msg("Migration already applied, skipping")
			continue
		}

		log.Info().
			Int("version", migration.Version).
			Msg("Running migration")

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("failed to begin transaction: %w", err)
		}

		// Execute migration
		if _, err := tx.Exec(migration.Up); err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to execute migration %d: %w", migration.Version, err)
		}

		// Record migration
		if _, err := tx.Exec("INSERT INTO migrations (version) VALUES (?)", migration.Version); err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to record migration %d: %w", migration.Version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit migration %d: %w", migration.Version, err)
		}

		log.Info().
			Int("version", migration.Version).
			Msg("Migration completed successfully")
	}

	return nil
}

// RollbackMigrations rolls back the last N migrations
func RollbackMigrations(db *sql.DB, migrations []Migration, n int) error {
	// Get applied migrations in reverse order
	rows, err := db.Query("SELECT version FROM migrations ORDER BY version DESC LIMIT ?", n)
	if err != nil {
		return fmt.Errorf("failed to query applied migrations: %w", err)
	}
	defer rows.Close()

	var versions []int
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return fmt.Errorf("failed to scan migration version: %w", err)
		}
		versions = append(versions, version)
	}

	// Rollback migrations in reverse order
	for _, version := range versions {
		var migration Migration
		for _, m := range migrations {
			if m.Version == version {
				migration = m
				break
			}
		}

		if migration.Down == "" {
			log.Warn().
				Int("version", version).
				Msg("No down migration found, skipping")
			continue
		}

		log.Info().
			Int("version", version).
			Msg("Rolling back migration")

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("failed to begin transaction: %w", err)
		}

		// Execute rollback
		if _, err := tx.Exec(migration.Down); err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to execute rollback for migration %d: %w", version, err)
		}

		// Remove migration record
		if _, err := tx.Exec("DELETE FROM migrations WHERE version = ?", version); err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to remove migration record %d: %w", version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit rollback for migration %d: %w", version, err)
		}

		log.Info().
			Int("version", version).
			Msg("Rollback completed successfully")
	}

	return nil
}
