# Reddot Watch Aggregator

A high-performance feed aggregation service that collects, processes, and serves content from multiple RSS/Atom feeds through a clean REST API.

## Overview

The Reddot Watch Aggregator is designed to solve the problem of aggregating content from hundreds or thousands of feeds into a centralized, queryable system. It handles:

- Feed import from CSV files
- Parallel feed fetching with error handling and retries
- Efficient storage with duplicate detection
- Automatic pruning of old items
- HTTP API for accessing aggregated content

This aggregator is built on [Reddot Watch FeedFetcher](https://github.com/reddot-watch/feedfetcher), a Go wrapper around the gofeed library that adds useful features for handling RSS/Atom feeds.

## Quick Start

### Installation

```bash
# Clone the repository
git clone https://github.com/reddot-watch/aggregator.git
cd aggregator

# Build the binary
go build -o aggregator ./cmd/aggregator
```

### Basic Usage

```bash
# Import feeds from a CSV file
./aggregator import -csv feeds.csv

# Start the processing service (runs periodically)
./aggregator start

# Start the API server
./aggregator server
```

## Commands

### Import Feeds

```bash
./aggregator import -csv feeds.csv -db feeds.db
```

Imports feeds from a CSV file into the database. The CSV should have columns for:
- URL (required)
- Comments (optional)
- Language (optional)
- Status (optional, defaults to "active")

If no CSV file is specified, the aggregator will default to using the global news feed list from [Reddot Watch Curated World News](https://github.com/reddot-watch/curated-world-news).

### Start Processing

```bash
# Run periodically (default: every 15 minutes)
./aggregator start -interval 15

# Run once and exit
./aggregator start -interval 0
```

This command fetches all active feeds, applies basic content validation, and stores items in the database. The validation includes:
- Filtering out items older than a configurable threshold (default 48 hours)
- Validating publication dates (removing items with future dates beyond a tolerance)
- Handling duplicate content through URL-based deduplication

You can:
- Set the processing interval in minutes (0 for one-shot execution)
- Configure the database path with `-db`
- Set logging level with `-log-level`

### Run API Server

```bash
./aggregator server
```

Starts the HTTP API server that provides access to the aggregated feed items.

Optional parameters:
- `-api-key`: API key for authentication (env: AGGREGATOR_API_KEY)
  - If set, requires the `X-API-Key` header in all requests
  - If not set, no authentication is required

Example with API key:
```bash
./aggregator server -api-key your-secret-key
```

## API Reference

The aggregator provides a REST API that serves feed items with pagination support.

### Endpoints

#### GET /v1/feed-items

Retrieves feed items with cursor-based pagination.

**Parameters:**

| Parameter | Type   | Required | Description |
|-----------|--------|----------|-------------|
| since     | string | Yes*     | ISO8601 timestamp to fetch items created after this time (e.g., `2025-03-28T15:00:00Z`) |
| cursor    | string | Yes*     | Pagination cursor from a previous response |
| limit     | int    | No       | Maximum number of items to return (default: 100, max: 1000) |

\* Either `since` or `cursor` must be provided. If both are provided, `cursor` takes precedence.

**Response:**

```json
{
  "items": [
    {
      "id": 123,
      "url": "https://example.com/article",
      "feed_id": 45,
      "feed_metadata": {"feed_url": "https://example.com/feed", "language": "en"},
      "headline": "Example Article Title",
      "content": "Article content...",
      "published_at": "2025-03-28T12:34:56Z",
      "created_at": "2025-03-28T13:00:00Z",
      "updated_at": "2025-03-28T13:00:00Z"
    },
    ...
  ],
  "next_cursor": "eyJ0aW1lc3RhbXAiOiIyMDI1LTAzLTI4VDEzOjAwOjAwWiIsImlkIjoxMjN9"
}
```

The `next_cursor` value should be used for subsequent requests to get the next page of results. If not present, you've reached the end of the available items.

#### GET /health

Health check endpoint. Returns a simple 200 OK response with "OK" text when the service is running.

### Docker Deployment

The aggregator is available as a Docker image. You can build and run it using different commands and configurations.

#### Building the Image

```bash
# Build the image
docker build -t reddot-watch-aggregator .
```

#### Running Different Commands

##### Import Feeds
```bash
# Import feeds from a CSV file
docker run -v /path/to/data:/app/data \
  reddot-watch-aggregator import

# Import with custom CSV file
docker run -v /path/to/data:/app/data \
  -e AGGREGATOR_CSV_PATH=/app/data/custom_feeds.csv \
  reddot-watch-aggregator import
```

##### Start Processing Service
```bash
# Run with default settings (15-minute interval)
docker run -d \
  -v /path/to/data:/app/data \
  --name aggregator \
  reddot-watch-aggregator start

# Run with custom interval (30 minutes)
docker run -d \
  -v /path/to/data:/app/data \
  -e AGGREGATOR_INTERVAL=30 \
  --name aggregator \
  reddot-watch-aggregator start

# Run once and exit
docker run -v /path/to/data:/app/data \
  reddot-watch-aggregator start --interval 0
```

##### Run API Server
```bash
# Run API server with default settings
docker run -d \
  -p 8080:8080 \
  -v /path/to/data:/app/data \
  --name aggregator-api \
  reddot-watch-aggregator server
```

#### Environment Variables

The following environment variables can be customized when running the container:

| Variable | Description | Default |
|----------|-------------|---------|
| `AGGREGATOR_DB_PATH` | Path to SQLite database | `/app/data/feeds.db` |
| `AGGREGATOR_CSV_PATH` | Path to feeds CSV file | `/app/data/feeds.csv` |
| `AGGREGATOR_SERVER_PORT` | API server port | `8080` |
| `AGGREGATOR_SERVER_HOST` | API server host | `""` (all interfaces) |
| `AGGREGATOR_WORKER_COUNT` | Number of workers (0 = use CPU count) | `0` |
| `AGGREGATOR_INTERVAL` | Processing interval in minutes | `15` |
| `AGGREGATOR_RETENTION_DAYS` | Days to keep items | `3` |
| `AGGREGATOR_LOG_LEVEL` | Logging level | `info` |
| `AGGREGATOR_API_KEY` | API key for authentication | `""` (disabled) |

Example with multiple custom settings:
```bash
docker run -d \
  -p 8080:8080 \
  -v /path/to/data:/app/data \
  -e AGGREGATOR_WORKER_COUNT=4 \
  -e AGGREGATOR_INTERVAL=30 \
  -e AGGREGATOR_RETENTION_DAYS=7 \
  -e AGGREGATOR_LOG_LEVEL=debug \
  --name aggregator \
  reddot-watch-aggregator start
```

#### Data Persistence

The container uses a volume mount for data persistence:
- Database file: `/app/data/feeds.db`
- Feeds CSV file: `/app/data/feeds.csv`

Make sure to:
1. Create the data directory on your host
2. Set appropriate permissions
3. Back up the data directory regularly

#### Health Check

The container includes a health check that monitors the API server:
```bash
# Check container health
docker inspect --format='{{.State.Health.Status}}' aggregator
```

#### Logging

View container logs:
```bash
# Follow logs
docker logs -f aggregator

# Last 100 lines
docker logs --tail 100 aggregator
```

## Architecture

The aggregator consists of several components:

1. **Feed Importer**: Processes CSV files to populate the feed database
2. **Feed Processor**: Concurrently fetches feeds, applies basic validation, and stores items in the database
3. **API Server**: Provides HTTP endpoints for accessing aggregated items
4. **Client SDK**: Reference implementation for integrating with the API

### Processing Logic

The feed processor:
- Uses worker pools for parallel feed fetching
- Implements exponential backoff for failed feeds
- Applies simple validation rules to filter items:
  - Excludes items older than a configured maximum age
  - Rejects items with publication dates too far in the future
- Deduplicates items by URL
- Tracks feed health and error states
- Periodically purges old items

## Performance Considerations

- The aggregator is designed to handle thousands of feeds efficiently
- SQLite with WAL mode provides excellent read/write concurrency
- Pagination with cursor-based design ensures efficient querying
- Consider the `interval` parameter based on feed update frequency and server resources

## Advanced Configuration

The aggregator supports configuration through both environment variables and command-line flags. When both are provided, command-line flags take precedence over environment variables.

### Environment Variables

| Variable | Description | Default               |
|----------|-------------|-----------------------|
| `AGGREGATOR_CSV_PATH` | Path to the feeds CSV file | `./feeds.csv`         |
| `AGGREGATOR_DB_PATH` | Path to the SQLite database file | `./feeds.db`          |
| `AGGREGATOR_HOST` | Host to bind the server to | `""` (all interfaces) |
| `AGGREGATOR_PORT` | Port to listen on | `8080`                |
| `AGGREGATOR_API_KEY` | API key for authentication | `""` (disabled)       |
| `AGGREGATOR_WORKER_COUNT` | Number of worker goroutines for processing (0 = CPU count) | `0`                   |
| `AGGREGATOR_INTERVAL` | Interval in minutes between processing runs | `15`                  |
| `AGGREGATOR_RETENTION_DAYS` | Number of days to retain feed items | `3`                   |
| `AGGREGATOR_DB_READONLY` | Open database in read-only mode | `false`               |
| `AGGREGATOR_LOG_LEVEL` | Log level (debug, info, warn, error) | `info`                |

### Command-Line Flags

Each subcommand supports specific flags:

#### Import Command

```bash
./aggregator import [options]
```

| Flag | Description | Environment Variable |
|------|-------------|----------------------|
| `-csv` | Path to the feeds CSV file | `AGGREGATOR_CSV_PATH` |
| `-db` | Path to the SQLite database file | `AGGREGATOR_DB_PATH` |
| `-log-level` | Log level | `AGGREGATOR_LOG_LEVEL` |

#### Start Command

```bash
./aggregator start [options]
```

| Flag | Description | Environment Variable |
|------|-------------|----------------------|
| `-db` | Path to the SQLite database file | `AGGREGATOR_DB_PATH` |
| `-interval` | Minutes between processing runs (0 for one-shot) | `AGGREGATOR_INTERVAL` |
| `-workers` | Number of worker goroutines (0 for CPU count) | `AGGREGATOR_WORKER_COUNT` |
| `-retention` | Days to retain feed items | `AGGREGATOR_RETENTION_DAYS` |
| `-log-level` | Log level | `AGGREGATOR_LOG_LEVEL` |

#### Server Command

```bash
./aggregator server [options]
```

| Flag | Description | Environment Variable |
|------|-------------|----------------------|
| `-db` | Path to the SQLite database file | `AGGREGATOR_DB_PATH` |
| `-host` | Host to bind to | `AGGREGATOR_HOST` |
| `-port` | Port to listen on | `AGGREGATOR_PORT` |
| `-api-key` | API key for authentication | `AGGREGATOR_API_KEY` |
| `-log-level` | Log level | `AGGREGATOR_LOG_LEVEL` |


## Client Integration

The `examples/client` directory contains a client implementation that demonstrates how to integrate with the aggregator API. The client:

1. Polls the API at regular intervals
2. Uses cursor-based pagination to fetch all new items
3. Maintains persistent state between runs
4. Implements backoff retry logic for transient errors
5. Provides a framework for processing fetched items

### Sample Client Usage

```go
package main

import (
    "log"
    "time"
    // your imports
)

func main() {
    // Initialize client with your endpoint
    client := NewAggregatorClient("http://localhost:8080")
    
    // Start polling for items since 3 days ago
    since := time.Now().Add(-72 * time.Hour)
    
    // Poll at regular intervals (e.g., every 5 minutes)
    for {
        items, err := client.FetchItems(since, 100)
        if err != nil {
            log.Printf("Error: %v", err)
            time.Sleep(5 * time.Minute)
            continue
        }
        
        // Process the items
        for _, item := range items {
            processItem(item)
        }
        
        // Update since timestamp for next poll if items were returned
        if len(items) > 0 {
            since = items[len(items)-1].CreatedAt
        }
        
        time.Sleep(5 * time.Minute)
    }
}
```

## License

This project is licensed under Apache 2.0 - See [LICENSE](LICENSE)