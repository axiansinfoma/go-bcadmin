package bcadmin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/axiansinfoma/go-bcadmin"
	"github.com/axiansinfoma/go-bcadmin/bcadmintest"
)

func TestTelemetry(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	serveFixture(t, srv, http.MethodGet, "applications/{family}/environments/{env}/telemetry", "testdata/telemetry/query.json")
	c := srv.NewClient(t)
	ctx := context.Background()

	cet := time.FixedZone("CET", 3600)
	tel, err := c.Environments().Telemetry(ctx, bc, "sb", bcadmin.TelemetryQuery{
		Start: time.Date(2026, 11, 26, 9, 0, 0, 0, cet), End: time.Date(2026, 11, 26, 10, 0, 0, 0, cet), Category: bcadmin.LogCategoryAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := srv.LastRequest()
	q, err := url.ParseQuery(req.Query)
	if err != nil || q.Get("startDateUtc") != "2026-11-26T08:00:00Z" || q.Get("endDateUtc") != "2026-11-26T09:00:00Z" || q.Get("logCategory") != "All" || len(q) != 3 {
		t.Errorf("query = %q", req.Query)
	}
	if len(tel.Columns) != 3 || !tel.Columns[0].DataType.Is(bcadmin.TelemetryDateTime) || !tel.Columns[1].DataType.Is(bcadmin.TelemetryString) ||
		!tel.Columns[1].Expectations.Is(bcadmin.TelemetryExpectationsWide) || tel.Columns[2].DataType != bcadmin.TelemetryNumeric {
		t.Errorf("columns = %+v", tel.Columns)
	}
	i := tel.Column("executiontime")
	if i != 2 || len(tel.Rows) != 2 || tel.Rows[0][i] != json.Number("12345678901234567") || tel.Rows[1][1] != nil {
		t.Errorf("rows = %v (column %d)", tel.Rows, i)
	}
	if tel.Column("missing") != -1 {
		t.Error("Column(missing) != -1")
	}

	if _, err := c.Environments().Telemetry(ctx, bc, "sb", bcadmin.TelemetryQuery{}); err != nil {
		t.Fatal(err)
	}
	if req, _ := srv.LastRequest(); req.Query != "" {
		t.Errorf("empty query sent %q", req.Query)
	}
	if _, err := c.Environments().Telemetry(ctx, bc, "sb", bcadmin.TelemetryQuery{Start: time.Now(), End: time.Now().Add(-time.Hour)}); err == nil {
		t.Error("end before start: no error")
	}
}

// TestTelemetryColumnOutOfRange checks that Column never returns an index a
// server sent outside the column list.
func TestTelemetryColumnOutOfRange(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`{"queryColumns":[{"name":"message","ordinal":7}],"queryResults":[["x"]]}`,
		`{"queryColumns":[{"name":"message","ordinal":-3}],"queryResults":[["x"]]}`,
	} {
		var tel bcadmin.Telemetry
		if err := json.Unmarshal([]byte(body), &tel); err != nil {
			t.Fatal(err)
		}
		if i := tel.Column("message"); i != -1 {
			t.Errorf("Column() = %d for %s, want -1", i, body)
		}
	}
}

// TestTelemetryUnknownEnumNumbers checks that unexpected numbers in column
// metadata are kept instead of failing the decode.
func TestTelemetryUnknownEnumNumbers(t *testing.T) {
	t.Parallel()
	var tel bcadmin.Telemetry
	body := `{"queryColumns":[{"name":"a","ordinal":0,"dataType":7,"expectations":1e30},{"name":"b","ordinal":1,"dataType":1.5,"expectations":null}],"queryResults":[]}`
	if err := json.Unmarshal([]byte(body), &tel); err != nil {
		t.Fatal(err)
	}
	if tel.Columns[0].DataType != "7" || tel.Columns[0].Expectations != "1e30" || tel.Columns[1].DataType != "1.5" || tel.Columns[1].Expectations != "" {
		t.Errorf("columns = %+v", tel.Columns)
	}
	if err := json.Unmarshal([]byte(`{"queryColumns":[{"name":"a","dataType":{}}]}`), &tel); err == nil {
		t.Error("object as data type: no error")
	}
}
