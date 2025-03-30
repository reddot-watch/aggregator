package importfeeds

import (
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/rs/zerolog/log"

	"reddot-watch/aggregator/internal/config"
	"reddot-watch/aggregator/internal/database"
	"reddot-watch/aggregator/internal/models"
)

// Importer handles the feed import process
type Importer struct {
	db *database.DB
}

// NewImporter creates a new feed importer
func NewImporter(db *database.DB) *Importer {
	return &Importer{db: db}
}

// ImportFeeds imports feeds from a CSV file
func (i *Importer) ImportFeeds(csvPath string) error {
	log.Info().Str("csv", csvPath).Msg("Starting feed import")

	csvData, err := i.getCSVData(csvPath)
	if err != nil {
		return fmt.Errorf("failed to get CSV data: %w", err)
	}

	err = i.parseAndImportFeeds(csvData)
	if err != nil {
		return fmt.Errorf("failed to import feeds: %w", err)
	}

	log.Info().Msg("Import completed successfully")
	return nil
}

func (i *Importer) getCSVData(csvPath string) (io.Reader, error) {
	if _, err := os.Stat(csvPath); err == nil {
		log.Info().Str("path", csvPath).Msg("Using local CSV file")
		return os.Open(csvPath)
	}

	// If the file doesn't exist and we have a remote URL, download it
	if config.RemoteFeedsURL != "" {
		log.Info().Str("url", config.RemoteFeedsURL).Str("path", csvPath).Msg("Local CSV file not found. Downloading from remote source")

		// Download the CSV file
		reader, err := i.downloadCSV(config.RemoteFeedsURL, csvPath)
		if err != nil {
			return nil, fmt.Errorf("failed to download CSV file: %w", err)
		}

		return reader, nil
	}

	return nil, fmt.Errorf("CSV file not found: %s", csvPath)
}

func (i *Importer) downloadCSV(url string, savePath string) (io.Reader, error) {
	log.Debug().Str("url", url).Str("savePath", savePath).Msg("Downloading CSV file")

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("failed to download file: HTTP status %d", resp.StatusCode)
	}

	// If a save path is provided, save the file there
	if savePath != "" {
		// Create the file
		outFile, err := os.Create(savePath)
		if err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("failed to create file %s: %w", savePath, err)
		}

		// Copy the response body to both the file and a buffer
		bodyData, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			outFile.Close()
			return nil, err
		}

		bytesWritten, err := outFile.Write(bodyData)
		outFile.Close()
		if err != nil {
			return nil, err
		}

		log.Debug().
			Int("bytes", bytesWritten).
			Str("path", savePath).
			Msg("Downloaded and saved CSV file")

		// Return a reader for the downloaded data
		return strings.NewReader(string(bodyData)), nil
	}

	// If no save path is provided, return the response body directly
	// Create a temporary file to store the downloaded content
	tempFile, err := os.CreateTemp("", "feeds-*.csv")
	if err != nil {
		resp.Body.Close()
		return nil, err
	}

	bytesWritten, err := io.Copy(tempFile, resp.Body)
	resp.Body.Close()
	if err != nil {
		tempFile.Close()
		os.Remove(tempFile.Name())
		return nil, err
	}

	log.Debug().
		Int64("bytes", bytesWritten).
		Str("path", tempFile.Name()).
		Msg("Downloaded CSV file to temporary location")

	// Seek to the beginning of the file so it can be read
	_, err = tempFile.Seek(0, 0)
	if err != nil {
		tempFile.Close()
		os.Remove(tempFile.Name())
		return nil, err
	}

	return tempFile, nil
}

func (i *Importer) parseAndImportFeeds(csvData io.Reader) error {
	log.Debug().Msg("Starting to parse and import feeds")

	reader := csv.NewReader(csvData)
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true

	header, err := reader.Read()
	if err != nil {
		return err
	}

	log.Debug().Strs("header", header).Msg("CSV header read")

	expectedColumns := map[string]bool{
		"url": false, "comments": false, "language": false, "status": false,
	}

	for _, column := range header {
		column = strings.ToLower(column)
		if _, exists := expectedColumns[column]; exists {
			expectedColumns[column] = true
		}
	}

	for column, found := range expectedColumns {
		if !found {
			return fmt.Errorf("required column '%s' not found in CSV header", column)
		}
	}

	urlIdx := findColumnIndex(header, "url")
	commentsIdx := findColumnIndex(header, "comments")
	languageIdx := findColumnIndex(header, "language")
	statusIdx := findColumnIndex(header, "status")

	tx, err := i.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
			log.Debug().Msg("Transaction rolled back")
			return
		}
		err = tx.Commit()
		if err != nil {
			log.Error().Err(err).Msg("Failed to commit transaction")
		} else {
			log.Debug().Msg("Transaction committed successfully")
		}
	}()

	lineCount := 1 // Header was already read
	successCount := 0
	var errors []string

	for {
		lineCount++
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Warn().Err(err).Int("line", lineCount).Msg("Error reading CSV line")
			errors = append(errors, fmt.Sprintf("line %d: %v", lineCount, err))
			continue
		}

		if len(record) == 0 || (len(record) == 1 && record[0] == "") {
			log.Debug().Int("line", lineCount).Msg("Skipping empty row")
			continue
		}

		feed := models.NewFeed()
		feed.URL = safeGetValue(record, urlIdx).String
		feed.Comments = safeGetValue(record, commentsIdx)
		feed.Language = safeGetValue(record, languageIdx)
		if status := safeGetValue(record, statusIdx); status.Valid {
			feed.Status = status.String
		}

		if feed.URL == "" {
			log.Warn().Int("line", lineCount).Msg("Skipping row with empty URL")
			errors = append(errors, fmt.Sprintf("line %d: empty URL", lineCount))
			continue
		}

		logger := log.With().
			Int("line", lineCount).
			Str("url", feed.URL).
			Str("language", feed.Language.String).
			Logger()

		logger.Debug().Msg("Processing feed")

		err = i.db.InsertFeed(feed)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				logger.Warn().Msg("Duplicate URL")
				errors = append(errors, fmt.Sprintf("line %d: duplicate URL: %s", lineCount, feed.URL))
			} else {
				logger.Error().Err(err).Msg("Failed to insert feed")
				errors = append(errors, fmt.Sprintf("line %d: %v", lineCount, err))
			}
			continue
		}

		successCount++
		logger.Debug().Msg("Feed inserted successfully")
	}

	log.Info().
		Int("total", lineCount-1).
		Int("success", successCount).
		Int("errors", len(errors)).
		Msg("Import summary")

	fmt.Printf("Imported %d feeds successfully\n", successCount)
	if len(errors) > 0 {
		fmt.Printf("Encountered %d errors:\n", len(errors))
		for _, err := range errors {
			fmt.Printf("  - %s\n", err)
		}
	}

	return nil
}

func findColumnIndex(header []string, columnName string) int {
	for i, col := range header {
		if strings.EqualFold(col, columnName) {
			return i
		}
	}
	return -1
}

// safeGetValue returns a sql.NullString from a record at the specified index.
// If the index is out of bounds or the value is empty, it returns an invalid NullString.
func safeGetValue(record []string, index int) sql.NullString {
	if index >= 0 && index < len(record) && record[index] != "" {
		return sql.NullString{
			String: record[index],
			Valid:  true,
		}
	}
	return sql.NullString{Valid: false}
}
