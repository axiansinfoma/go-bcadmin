package bcadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// LogCategory selects the telemetry of [EnvironmentsService.Telemetry].
type LogCategory string

// Log categories. The documentation names only All.
const (
	LogCategoryAll LogCategory = "All"
)

// TelemetryQuery selects the telemetry of an environment. The service
// recommends a time window to keep the result small.
type TelemetryQuery struct {
	// Start and End are the time window; zero values are not sent. They
	// are sent in UTC.
	Start, End time.Time
	// Category is the log category; empty is not sent.
	Category LogCategory
}

// TelemetryDataType is the type of a telemetry column. The service sends it
// as a name or a number; numbers are decoded to the names below. Compare
// with [TelemetryDataType.Is].
type TelemetryDataType string

// Telemetry data types.
const (
	TelemetryString   TelemetryDataType = "String"
	TelemetryNumeric  TelemetryDataType = "Numeric"
	TelemetryDateTime TelemetryDataType = "DateTime"
)

// Is reports whether t and other are the same type, ignoring case.
func (t TelemetryDataType) Is(other TelemetryDataType) bool {
	return strings.EqualFold(string(t), string(other))
}

// UnmarshalJSON decodes a name or a number (0 String, 1 Numeric, 2
// DateTime; other numbers keep their digits).
func (t *TelemetryDataType) UnmarshalJSON(data []byte) error {
	s, err := enumName(data, []string{string(TelemetryString), string(TelemetryNumeric), string(TelemetryDateTime)})
	*t = TelemetryDataType(s)
	return err
}

// TelemetryExpectations are flags of a telemetry column. The service sends
// them as a name or a number; numbers are decoded to the names below.
type TelemetryExpectations string

// Telemetry column expectations.
const (
	TelemetryExpectationsNone TelemetryExpectations = "None"
	TelemetryExpectationsWide TelemetryExpectations = "Wide"
)

// UnmarshalJSON decodes a name or a number (0 None, 1 Wide; other numbers
// keep their digits).
func (e *TelemetryExpectations) UnmarshalJSON(data []byte) error {
	s, err := enumName(data, []string{string(TelemetryExpectationsNone), string(TelemetryExpectationsWide)})
	*e = TelemetryExpectations(s)
	return err
}

// enumName decodes a JSON string, null or integer; an integer is replaced by
// names[n] when it is in range.
func enumName(data []byte, names []string) (string, error) {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		return "", nil
	}
	if len(data) > 0 && data[0] == '"' {
		var s string
		err := json.Unmarshal(data, &s)
		return s, err
	}
	n, err := strconv.Atoi(string(data))
	if err != nil {
		return "", errors.New("bcadmin: enum value is neither a string nor an integer")
	}
	if n >= 0 && n < len(names) {
		return names[n], nil
	}
	return strconv.Itoa(n), nil
}

// TelemetryColumn describes one column of a telemetry result.
type TelemetryColumn struct {
	Name string `json:"name"`
	// Ordinal is the index of the column in each row.
	Ordinal      int                   `json:"ordinal"`
	DataType     TelemetryDataType     `json:"dataType"`
	Expectations TelemetryExpectations `json:"expectations"`
}

// Telemetry is the result of a telemetry query: column headers and rows of
// raw values. Numbers are decoded as json.Number, so large values keep
// their precision; strings and nulls as string and nil.
type Telemetry struct {
	Columns []TelemetryColumn `json:"queryColumns"`
	Rows    [][]any           `json:"queryResults"`
}

// UnmarshalJSON decodes the result with json.Number for numbers.
func (t *Telemetry) UnmarshalJSON(data []byte) error {
	type plain Telemetry
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var p plain
	if err := dec.Decode(&p); err != nil {
		return err
	}
	*t = Telemetry(p)
	return nil
}

// Column returns the index of the column named name in each row, ignoring
// case, or -1.
func (t *Telemetry) Column(name string) int {
	for _, c := range t.Columns {
		if strings.EqualFold(c.Name, name) {
			return c.Ordinal
		}
	}
	return -1
}

// Telemetry returns telemetry of the environment family/environment: top
// level AL events and errors logged by the service.
//
// GET /applications/{applicationFamily}/environments/{environmentName}/telemetry.
func (s *EnvironmentsService) Telemetry(ctx context.Context, family, environment string, q TelemetryQuery) (*Telemetry, error) {
	if !q.Start.IsZero() && !q.End.IsZero() && q.End.Before(q.Start) {
		return nil, errors.New("bcadmin: TelemetryQuery.End is before Start")
	}
	query := url.Values{"logCategory": {string(q.Category)}}
	if !q.Start.IsZero() {
		query.Set("startDateUtc", q.Start.UTC().Format(time.RFC3339))
	}
	if !q.End.IsZero() {
		query.Set("endDateUtc", q.End.UTC().Format(time.RFC3339))
	}
	var t Telemetry
	if err := s.c.callQuery(ctx, http.MethodGet, "applications/{}/environments/{}/telemetry",
		[]string{family, environment}, query, nil, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// Telemetry returns telemetry of the environment; see
// [EnvironmentsService.Telemetry].
func (e *Environment) Telemetry(ctx context.Context, q TelemetryQuery) (*Telemetry, error) {
	s, err := e.service()
	if err != nil {
		return nil, err
	}
	return s.Telemetry(ctx, e.ApplicationFamily, e.Name, q)
}
