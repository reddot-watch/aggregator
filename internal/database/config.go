package database

import "time"

const (
	defaultWorkerCount     = 10
	defaultMaxIdleConns    = 12
	defaultMaxOpenConns    = 12
	defaultConnMaxLifetime = time.Hour
)

// Config holds database configuration settings
type Config struct {
	// Required settings
	DBPath string

	// Optional settings (will use defaults if not set)
	WorkerCount     int
	MaxIdleConns    int
	MaxOpenConns    int
	ConnMaxLifetime time.Duration
	CacheSizeKB     int
	BusyTimeoutMS   int
	ReadOnly        bool
}

// NewConfig creates a new database configuration with default values
func NewConfig(dbPath string) *Config {
	return &Config{
		DBPath:          dbPath,
		WorkerCount:     0, // Will be set to default if not specified
		MaxIdleConns:    0, // Will be set to default if not specified
		MaxOpenConns:    0, // Will be set to default if not specified
		ConnMaxLifetime: defaultConnMaxLifetime,
		CacheSizeKB:     -64000, // 64MB
		BusyTimeoutMS:   5000,
	}
}
