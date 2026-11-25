package bcadmin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// Time is a timestamp returned by the API. It decodes RFC 3339 timestamps
// and, because the API is not consistent, also timestamps without a time zone
// (interpreted as UTC). Empty strings and null decode to the zero time.
type Time struct {
	time.Time
}

// timeLayouts are tried in order when decoding a Time.
var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02",
}

// UnmarshalJSON decodes a JSON string or null.
func (t *Time) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		t.Time = time.Time{}
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("bcadmin: invalid timestamp %s: %w", data, err)
	}
	if s == "" {
		t.Time = time.Time{}
		return nil
	}
	for _, layout := range timeLayouts {
		if v, err := time.Parse(layout, s); err == nil {
			t.Time = v
			return nil
		}
	}
	return fmt.Errorf("bcadmin: invalid timestamp %q", s)
}

// MarshalJSON encodes the time in RFC 3339 format in UTC, or null for the zero
// time.
func (t Time) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(t.UTC().Format(time.RFC3339Nano))
}
