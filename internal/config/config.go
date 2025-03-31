package config

import (
	"fmt"
	"time"

	"github.com/rs/zerolog"
)

// Config holds all configuration for the application
type Config struct {
	// File paths
	FeedsCSVPath string
	DBPath       string

	// Server settings
	ServerHost string
	ServerPort int
	APIKey     string

	// Processing settings
	WorkerCount   int
	Interval      time.Duration
	RetentionDays int

	// Log settings
	LogLevel zerolog.Level
}

// DefaultConfig returns an initial configuration with hardcoded defaults.
func DefaultConfig() *Config {
	logLevel, _ := zerolog.ParseLevel(DefaultLogLevel)

	return &Config{
		FeedsCSVPath:  DefaultFeedsCSVPath,
		DBPath:        DefaultDBPath,
		ServerHost:    DefaultServerHost,
		ServerPort:    DefaultServerPort,
		APIKey:        GetEnvString("AGGREGATOR_API_KEY", ""),
		WorkerCount:   DefaultWorkerCount,
		Interval:      time.Duration(DefaultInterval) * time.Minute,
		RetentionDays: DefaultRetentionDays,
		LogLevel:      logLevel,
	}
}

// ListenAddr returns the formatted listen address for the HTTP server.
func (c *Config) ListenAddr() string {
	return fmt.Sprintf("%s:%d", c.ServerHost, c.ServerPort)
}
