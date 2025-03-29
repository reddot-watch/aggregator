package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"reddot-watch/aggregator/internal/config"
	"reddot-watch/aggregator/internal/database"
	importfeeds "reddot-watch/aggregator/internal/import"
	"reddot-watch/aggregator/internal/process"
	"reddot-watch/aggregator/internal/server"
)

func init() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "2006-01-02 15:04:05"})
	zerolog.SetGlobalLevel(zerolog.DebugLevel)
}

func main() {
	cfg := config.DefaultConfig()

	importCmd := flag.NewFlagSet("import", flag.ExitOnError)
	importCmd.StringVar(&cfg.FeedsCSVPath, "csv", config.GetEnvString("AGGREGATOR_CSV_PATH", config.DefaultFeedsCSVPath),
		"Path to the feeds CSV file (env: AGGREGATOR_CSV_PATH)")
	importCmd.StringVar(&cfg.DBPath, "db", config.GetEnvString("AGGREGATOR_DB_PATH", config.DefaultDBPath),
		"Path to the SQLite database file (env: AGGREGATOR_DB_PATH)")

	var logLevelStr string
	importCmd.StringVar(&logLevelStr, "log-level", config.GetEnvString("AGGREGATOR_LOG_LEVEL", config.DefaultLogLevel),
		"Log level: debug, info, warn, error (env: AGGREGATOR_LOG_LEVEL)")

	startCmd := flag.NewFlagSet("start", flag.ExitOnError)
	startCmd.StringVar(&cfg.DBPath, "db", config.GetEnvString("AGGREGATOR_DB_PATH", config.DefaultDBPath),
		"Path to the SQLite database file (env: AGGREGATOR_DB_PATH)")

	var startLogLevelStr string
	startCmd.StringVar(&startLogLevelStr, "log-level", config.GetEnvString("AGGREGATOR_LOG_LEVEL", config.DefaultLogLevel),
		"Log level: debug, info, warn, error (env: AGGREGATOR_LOG_LEVEL)")

	var intervalMinutes int
	startCmd.IntVar(&intervalMinutes, "interval", config.GetEnvInt("AGGREGATOR_INTERVAL", config.DefaultInterval),
		"Interval in minutes between processing runs, 0 for one-shot mode (env: AGGREGATOR_INTERVAL)")

	startCmd.IntVar(&cfg.WorkerCount, "workers", config.GetEnvInt("AGGREGATOR_WORKER_COUNT", config.DefaultWorkerCount),
		"Number of worker goroutines for processing, 0 for CPU count (env: AGGREGATOR_WORKER_COUNT)")

	startCmd.IntVar(&cfg.RetentionDays, "retention", config.GetEnvInt("AGGREGATOR_RETENTION_DAYS", config.DefaultRetentionDays),
		"Number of days to retain feed items (env: AGGREGATOR_RETENTION_DAYS)")

	serverCmd := flag.NewFlagSet("server", flag.ExitOnError)
	serverCmd.StringVar(&cfg.DBPath, "db", config.GetEnvString("AGGREGATOR_DB_PATH", config.DefaultDBPath),
		"Path to the SQLite database file (env: AGGREGATOR_DB_PATH)")

	serverCmd.StringVar(&cfg.ServerHost, "host", config.GetEnvString("AGGREGATOR_HOST", config.DefaultServerHost),
		"Host to bind the server to (env: AGGREGATOR_HOST)")

	serverCmd.IntVar(&cfg.ServerPort, "port", config.GetEnvInt("AGGREGATOR_PORT", config.DefaultServerPort),
		"Port to listen on (env: AGGREGATOR_PORT)")

	var serverLogLevelStr string
	serverCmd.StringVar(&serverLogLevelStr, "log-level", config.GetEnvString("AGGREGATOR_LOG_LEVEL", config.DefaultLogLevel),
		"Log level: debug, info, warn, error (env: AGGREGATOR_LOG_LEVEL)")

	if len(os.Args) < 2 {
		fmt.Println("Usage: aggregator [command] [options]")
		fmt.Println("Commands: import, start, server")
		fmt.Println("\nFor command-specific options, use: aggregator [command] -h")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "import":
		importCmd.Parse(os.Args[2:])

		// Handle log level parsing separately since it needs conversion
		if level, err := zerolog.ParseLevel(logLevelStr); err == nil {
			cfg.LogLevel = level
		}

		zerolog.SetGlobalLevel(cfg.LogLevel)

		err := runImport(cfg)
		if err != nil {
			log.Error().Err(err).Msg("Import failed")
			os.Exit(1)
		}

	case "start":
		startCmd.Parse(os.Args[2:])

		// Handle log level parsing separately
		if level, err := zerolog.ParseLevel(startLogLevelStr); err == nil {
			cfg.LogLevel = level
		}

		// Convert interval minutes to duration
		cfg.Interval = time.Duration(intervalMinutes) * time.Minute

		zerolog.SetGlobalLevel(cfg.LogLevel)

		err := runStart(cfg)
		if err != nil {
			log.Error().Err(err).Msg("Processing failed")
			os.Exit(1)
		}

	case "server":
		serverCmd.Parse(os.Args[2:])

		// Handle log level parsing separately
		if level, err := zerolog.ParseLevel(serverLogLevelStr); err == nil {
			cfg.LogLevel = level
		}

		zerolog.SetGlobalLevel(cfg.LogLevel)

		err := runServer(cfg)
		if err != nil {
			log.Error().Err(err).Msg("Server failed")
			os.Exit(1)
		}

	case "-h", "--help", "help":
		fmt.Println("Usage: aggregator [command] [options]")
		fmt.Println("Commands: import, start, server")
		fmt.Println("\nFor command-specific options, use: aggregator [command] -h")
		os.Exit(0)

	default:
		log.Error().Str("command", os.Args[1]).Msg("Unknown command")
		fmt.Println("Available commands: import, start, server")
		fmt.Println("\nFor command-specific options, use: aggregator [command] -h")
		os.Exit(1)
	}
}

// runImport imports feeds from a CSV file into a fresh database.
// It will prompt for confirmation before deleting an existing database.
func runImport(cfg *config.Config) error {
	if _, err := os.Stat(cfg.DBPath); err == nil {
		fmt.Printf("Database %s already exists. All data will be lost as updates are not currently supported.\n", cfg.DBPath)
		fmt.Print("Delete and recreate? (y/N): ")

		var answer string
		fmt.Scanln(&answer)

		if strings.ToLower(answer) != "y" {
			log.Info().Msg("Operation canceled by user")
			return fmt.Errorf("operation canceled by user")
		}

		err := database.DeleteDB(cfg.DBPath)
		if err != nil {
			log.Error().Err(err).Str("path", cfg.DBPath).Msg("Failed to delete existing database")
			return fmt.Errorf("failed to delete existing database: %w", err)
		}
		log.Info().Str("path", cfg.DBPath).Msg("Deleted existing database")
	}

	dbCfg := database.NewConfig(cfg.DBPath)
	db, err := database.NewDB(dbCfg)
	if err != nil {
		log.Error().Err(err).Str("path", cfg.DBPath).Msg("Failed to initialize database")
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer db.Close()

	importer := importfeeds.NewImporter(db)
	return importer.ImportFeeds(cfg.FeedsCSVPath)
}

// runStart executes the feed processor either once or periodically based on configuration.
func runStart(cfg *config.Config) error {
	if cfg.Interval <= 0 {
		log.Info().Msg("Running in one-shot mode")
	} else {
		log.Info().Int64("interval_minutes", int64(cfg.Interval.Minutes())).Msg("Running in periodic mode")
	}

	dbCfg := database.NewConfig(cfg.DBPath)
	db, err := database.NewDB(dbCfg)
	if err != nil {
		log.Error().Err(err).Str("path", cfg.DBPath).Msg("Failed to initialize database")
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-shutdown
		log.Info().Str("signal", sig.String()).Msg("Shutdown signal received")
		cancel() // Cancel the context to stop processing
	}()

	if err := runProcessingCycle(ctx, db, cfg); err != nil {
		if errors.Is(err, context.Canceled) {
			log.Info().Msg("Processing cycle canceled by shutdown signal")
			return nil
		}
		return err
	}

	if cfg.Interval == 0 {
		log.Info().Msg("One-shot processing completed, exiting")
		return nil
	}

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	log.Info().
		Dur("interval", cfg.Interval).
		Time("next_run", time.Now().Add(cfg.Interval)).
		Msg("Waiting for next processing cycle")

	for {
		select {
		case <-ticker.C:
			log.Info().Msg("Starting scheduled processing cycle")

			if err := runProcessingCycle(ctx, db, cfg); err != nil {
				if errors.Is(err, context.Canceled) {
					log.Info().Msg("Processing cycle canceled by shutdown signal")
					return nil
				}
				log.Error().Err(err).Msg("Processing cycle failed")
				// Continue to the next cycle rather than exiting
			}

			log.Info().
				Time("next_run", time.Now().Add(cfg.Interval)).
				Msg("Waiting for next processing cycle")

		case <-ctx.Done():
			log.Info().Msg("Shutting down periodic processing")
			return nil
		}
	}
}

// runProcessingCycle executes a single feed processing cycle.
func runProcessingCycle(ctx context.Context, db *database.DB, cfg *config.Config) error {
	processor, err := process.NewFeedProcessor(db, cfg.WorkerCount)
	if err != nil {
		return fmt.Errorf("failed to initialize feeds processor: %w", err)
	}

	processCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	log.Info().
		Int("worker_count", processor.WorkerCount).
		Msg("Starting processing cycle")

	startTime := time.Now()
	err = processor.ProcessFeeds(processCtx)
	endTime := time.Now()

	log.Info().
		Dur("duration", endTime.Sub(startTime)).
		Msg("Processing cycle finished")

	if err != nil {
		if ctxErr := processCtx.Err(); ctxErr != nil && (errors.Is(ctxErr, err) || err.Error() == ctxErr.Error()) {
			return ctx.Err() // Propagate cancellation
		}
		return fmt.Errorf("processing error: %w", err)
	}

	processed, duplicates := processor.Stats()
	log.Info().
		Int64("processed", processed).
		Int64("duplicates", duplicates).
		Msg("Processing stats")

	// Run purging as part of the processing cycle
	purgeCtx, purgeCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer purgeCancel()

	purgedCount, purgeErr := processor.PurgeOldItems(purgeCtx, cfg.RetentionDays)
	if purgeErr != nil {
		log.Error().Err(purgeErr).Msg("Failed to purge old items")
	} else if purgedCount > 0 {
		log.Info().Int64("purged_count", purgedCount).Msg("Successfully purged old feed items")
	} else {
		log.Info().Msg("No old feed items needed purging")
	}

	return nil
}

// runServer starts the HTTP API server with the provided configuration.
func runServer(cfg *config.Config) error {
	log.Debug().Msg("Starting server with debug logging enabled")

	dbCfg := database.NewConfig(cfg.DBPath)
	dbCfg.ReadOnly = true

	db, err := database.NewDB(dbCfg)
	if err != nil {
		log.Error().Err(err).Str("path", cfg.DBPath).Msg("Failed to initialize database")
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer db.Close()

	return server.RunServer(db, cfg.ListenAddr(), log.Logger)
}
