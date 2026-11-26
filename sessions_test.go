package bcadmin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/axiansinfoma/go-bcadmin"
	"github.com/axiansinfoma/go-bcadmin/bcadmintest"
)

func TestSessions(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	serveFixture(t, srv, http.MethodGet, "applications/{family}/environments/{env}/sessions", "testdata/sessions/list.json")
	raw, err := os.ReadFile("testdata/sessions/list.json")
	if err != nil {
		t.Fatal(err)
	}
	var list struct{ Value []json.RawMessage }
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	srv.Handle(http.MethodGet, "applications/{family}/environments/{env}/sessions/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(list.Value[0])
	})
	srv.Handle(http.MethodDelete, "applications/{family}/environments/{env}/sessions/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	s := srv.NewClient(t).Sessions(bc, "sb")
	ctx := context.Background()

	sessions, err := s.List(ctx)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("List() = %+v, %v", sessions, err)
	}
	got := sessions[0]
	if got.SessionID != 1042 || got.ClientType != "WebClient" || got.CurrentObjectID != 21 || got.EntryPointObjectID != "22" ||
		got.CurrentOperationDuration != 1500 || got.LogOnDate != "2026-11-26T08:15:00Z" {
		t.Errorf("session = %+v", got)
	}
	one, err := s.Get(ctx, 1042)
	if err != nil || *one != got {
		t.Errorf("Get() = %+v, %v", one, err)
	}
	findRequest(t, srv, http.MethodGet, "/admin/v2.29/applications/BusinessCentral/environments/sb/sessions/1042")
	if err := s.Terminate(ctx, 1042); err != nil {
		t.Fatal(err)
	}
	findRequest(t, srv, http.MethodDelete, "/admin/v2.29/applications/BusinessCentral/environments/sb/sessions/1042")
	if err := s.Terminate(ctx, -1); err == nil {
		t.Error("negative session ID: no error")
	}
	if _, err := s.Get(ctx, -1); err == nil {
		t.Error("Get with a negative session ID: no error")
	}
}

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
