package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// GetEnvString retrieves a string from environment variables or returns the default value.
func GetEnvString(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

// GetEnvInt retrieves an integer from environment variables or returns the default value.
func GetEnvInt(key string, defaultValue int) int {
	valStr := os.Getenv(key)
	if valStr == "" {
		return defaultValue
	}

	val, err := strconv.Atoi(valStr)
	if err != nil {
		return defaultValue
	}
	return val
}

// GetEnvBool retrieves a boolean from environment variables or returns the default value.
func GetEnvBool(key string, defaultValue bool) bool {
	valStr := os.Getenv(key)
	if valStr == "" {
		return defaultValue
	}

	val, err := strconv.ParseBool(valStr)
	if err != nil {
		return defaultValue
	}
	return val
}

// GetEnvDuration retrieves a duration from environment variables or returns the default value.
// If the string contains time units (m, h, s), they'll be parsed accordingly.
// Otherwise, the value is interpreted as minutes.
func GetEnvDuration(key string, defaultValue time.Duration) time.Duration {
	valStr := os.Getenv(key)
	if valStr == "" {
		return defaultValue
	}

	if strings.Contains(valStr, "m") || strings.Contains(valStr, "h") || strings.Contains(valStr, "s") {
		val, err := time.ParseDuration(valStr)
		if err != nil {
			return defaultValue
		}
		return val
	}

	val, err := strconv.Atoi(valStr)
	if err != nil {
		return defaultValue
	}
	return time.Duration(val) * time.Minute
}

// GetEnvLogLevel retrieves a log level from environment variables or returns the default value.
func GetEnvLogLevel(key string, defaultValue zerolog.Level) zerolog.Level {
	valStr := os.Getenv(key)
	if valStr == "" {
		return defaultValue
	}

	level, err := zerolog.ParseLevel(valStr)
	if err != nil {
		return defaultValue
	}
	return level
}
