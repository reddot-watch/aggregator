package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"reddot-watch/aggregator/internal/database"
	"reddot-watch/aggregator/internal/server/api"
	"reddot-watch/aggregator/internal/server/storage"

	_ "github.com/mattn/go-sqlite3"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
)

// RunServer starts the HTTP server with graceful shutdown support.
// It sets up routes, middleware, and handles OS signals for clean termination.
func RunServer(db *database.DB, listenAddr string, logger zerolog.Logger) error {
	// Add service identifier to the logger
	logger = logger.With().Str("service", "feed-api-readonly").Logger()

	feedItemRepo := storage.NewRepository(db)
	feedItemsHandler := api.NewFeedItemsHandler(feedItemRepo)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/feed-items", feedItemsHandler.GetFeedItems)
	mux.HandleFunc("GET /health", healthCheckHandler)

	// Set up middleware chain for logging and request tracking
	h := hlog.NewHandler(logger)(mux)
	h = hlog.MethodHandler("method")(h)
	h = hlog.URLHandler("url")(h)
	h = hlog.RemoteAddrHandler("remote_addr")(h)
	h = hlog.UserAgentHandler("user_agent")(h)
	h = hlog.RequestIDHandler("req_id", "Request-Id")(h)
	h = hlog.AccessHandler(func(r *http.Request, status, size int, duration time.Duration) {
		idReq, _ := hlog.IDFromRequest(r)

		hlog.FromRequest(r).Info().
			Str("method", r.Method).
			Stringer("url", r.URL).
			Int("status", status).
			Int("size", size).
			Dur("duration", duration).
			Str("req_id", idReq.String()).
			Msg("HTTP Request")
	})(h)

	httpServer := &http.Server{
		Addr:              listenAddr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info().Str("address", listenAddr).Msg("API Server starting")
		err := httpServer.ListenAndServe()
		if !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		logger.Fatal().Err(err).Msg("Server failed to start")

	case sig := <-shutdown:
		logger.Info().Str("signal", sig.String()).Msg("Shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Error().Err(err).Msg("HTTP server shutdown error")
			if err := httpServer.Close(); err != nil {
				logger.Error().Err(err).Msg("HTTP server force close error")
			}
		} else {
			logger.Info().Msg("HTTP server shutdown complete.")
		}
		if err := <-serverErr; err != nil {
			logger.Error().Err(err).Msg("ListenAndServe error during shutdown")
		}
	}

	logger.Info().Msg("Server exiting.")
	return nil
}

// healthCheckHandler responds to health check requests with a simple 200 OK.
// This endpoint is used by monitoring systems to verify the service is operational.
func healthCheckHandler(w http.ResponseWriter, r *http.Request) {
	log := hlog.FromRequest(r)
	log.Debug().Msg("Health check request received")

	if r.Method != http.MethodGet {
		log.Warn().Str("method", r.Method).Msg("Health check method not allowed")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	// TODO: if err := db.PingContext(r.Context()); err != nil { /* return 503 */ }

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	n, err := w.Write([]byte("OK"))
	if err != nil {
		log.Error().Err(err).Msg("Error writing health check response")
	} else {
		log.Debug().Int("bytes_written", n).Msg("Health check response sent")
	}
}
