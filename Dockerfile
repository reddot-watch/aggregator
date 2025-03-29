# Builder stage
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Install necessary build dependencies
RUN apk add --no-cache git gcc musl-dev sqlite-dev

# Copy only dependency files first to leverage Docker caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build with optimizations, CGO enabled for SQLite
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o aggregator .

# Runtime stage
FROM alpine:3.19

# Add non-root user for security
RUN addgroup -S appgroup && adduser -S appuser -G appgroup

# Install runtime dependencies only
RUN apk add --no-cache ca-certificates sqlite-libs tzdata

# Set working directory
WORKDIR /app

# Copy the binary from builder stage
COPY --from=builder /app/aggregator /app/

# Create data directory with proper permissions
RUN mkdir -p /app/data && chown -R appuser:appgroup /app/data

# Use volume for database persistence
VOLUME /app/data

# Use non-root user
USER appuser

# Set environment variables with defaults
ENV AGGREGATOR_DB_PATH=/app/data/feeds.db \
    AGGREGATOR_CSV_PATH=/app/data/feeds.csv \
    AGGREGATOR_SERVER_PORT=8080 \
    AGGREGATOR_SERVER_HOST="" \
    AGGREGATOR_WORKER_COUNT=0 \
    AGGREGATOR_INTERVAL=15 \
    AGGREGATOR_RETENTION_DAYS=3 \
    AGGREGATOR_LOG_LEVEL=debug

# Expose port for the API server
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD wget -qO- http://localhost:8080/health || exit 1

ENTRYPOINT ["/app/aggregator"]
