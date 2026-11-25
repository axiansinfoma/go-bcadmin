package bcadmin_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/axiansinfoma/go-bcadmin"
)

func TestTimeUnmarshal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    time.Time
		wantErr bool
	}{
		{in: `"2021-01-25T14:57:04.967Z"`, want: time.Date(2021, 1, 25, 14, 57, 4, 967000000, time.UTC)},
		{in: `"2021-01-27T14:33:15.0007416Z"`, want: time.Date(2021, 1, 27, 14, 33, 15, 741600, time.UTC)},
		{in: `"2026-11-24T21:00:00+01:00"`, want: time.Date(2026, 11, 24, 20, 0, 0, 0, time.UTC)},
		{in: `"2026-12-01T00:00:00"`, want: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)},
		{in: `"2026-12-01T00:00:00.5"`, want: time.Date(2026, 12, 1, 0, 0, 0, 500000000, time.UTC)},
		{in: `"2026-12-01"`, want: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)},
		{in: `""`},
		{in: `null`},
		{in: `"timestamp"`, wantErr: true},
		{in: `42`, wantErr: true},
	}
	for _, tt := range tests {
		var got bcadmin.Time
		err := json.Unmarshal([]byte(tt.in), &got)
		if (err != nil) != tt.wantErr {
			t.Errorf("Unmarshal(%s) error = %v", tt.in, err)
			continue
		}
		if !got.Equal(tt.want) {
			t.Errorf("Unmarshal(%s) = %v, want %v", tt.in, got.Time, tt.want)
		}
	}
}

func TestTimeMarshal(t *testing.T) {
	t.Parallel()
	b, err := json.Marshal(struct {
		A bcadmin.Time
		B bcadmin.Time
	}{A: bcadmin.Time{Time: time.Date(2026, 11, 24, 21, 0, 0, 0, time.FixedZone("CET", 3600))}})
	if err != nil || string(b) != `{"A":"2026-11-24T20:00:00Z","B":null}` {
		t.Errorf("Marshal = %s, %v", b, err)
	}
}
