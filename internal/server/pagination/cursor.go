package pagination

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const cursorSeparator = ","
const timeFormat = time.RFC3339Nano // Use nano for precision

// EncodeCursor creates an opaque cursor string from timestamp and ID.
func EncodeCursor(ts time.Time, id int64) string {
	key := fmt.Sprintf("%s%s%d", ts.UTC().Format(timeFormat), cursorSeparator, id)
	return base64.URLEncoding.EncodeToString([]byte(key))
}

// DecodeCursor parses the opaque cursor string back into timestamp and ID.
func DecodeCursor(encodedCursor string) (time.Time, int64, error) {
	decodedBytes, err := base64.URLEncoding.DecodeString(encodedCursor)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("invalid cursor encoding: %w", err)
	}

	key := string(decodedBytes)
	parts := strings.SplitN(key, cursorSeparator, 2)
	if len(parts) != 2 {
		return time.Time{}, 0, fmt.Errorf("invalid cursor format")
	}

	ts, err := time.Parse(timeFormat, parts[0])
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("invalid timestamp in cursor: %w", err)
	}

	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("invalid id in cursor: %w", err)
	}

	return ts.UTC(), id, nil
}
