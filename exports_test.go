package bcadmin_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/axiansinfoma/go-bcadmin"
	"github.com/axiansinfoma/go-bcadmin/bcadmintest"
)

const sasURI = "https://contosoexports.blob.core.windows.net/?sv=2024-01-01&sig=SECRET"

func TestExports(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.HandleJSON(http.MethodGet, "exports/applications/{family}/environments/{env}/metrics", http.StatusOK,
		map[string]any{"exportsPerMonth": 10, "exportsRemainingThisMonth": 7})
	srv.Handle(http.MethodPost, "exports/applications/{family}/environments/{env}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	serveFixture(t, srv, http.MethodPost, "exports/history", "testdata/exports/history.json")
	ex := srv.NewClient(t).Exports()
	ctx := context.Background()

	m, err := ex.Metrics(ctx, bc, "sb")
	if err != nil || m.ExportsPerMonth != 10 || m.ExportsRemainingThisMonth != 7 {
		t.Errorf("Metrics() = %+v, %v", m, err)
	}
	op, err := ex.Start(ctx, bc, "sb", bcadmin.ExportOptions{StorageAccountSASURI: sasURI, Container: "exports", Blob: "sb.bacpac"})
	if err != nil || op != nil {
		t.Errorf("Start() = %+v, %v; want nil, nil for an empty response", op, err)
	}
	want := map[string]any{"storageAccountSasUri": sasURI, "container": "exports", "blob": "sb.bacpac"}
	if got := lastBody(t, srv); !reflect.DeepEqual(got, want) {
		t.Errorf("start body = %v, want %v", got, want)
	}
	findRequest(t, srv, http.MethodPost, "/admin/v2.29/exports/applications/BusinessCentral/environments/sb")

	n := len(srv.Requests())
	for name, opts := range map[string]bcadmin.ExportOptions{
		"no blob":   {StorageAccountSASURI: sasURI, Container: "c"},
		"http":      {StorageAccountSASURI: "http://x.example.com/?sig=SECRET", Container: "c", Blob: "b"},
		"no token":  {StorageAccountSASURI: "https://x.example.com/", Container: "c", Blob: "b"},
		"not a url": {StorageAccountSASURI: "::SECRET", Container: "c", Blob: "b"},
	} {
		_, err := ex.Start(ctx, bc, "sb", opts)
		if err == nil {
			t.Errorf("%s: no error", name)
		} else if strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%s: error contains the SAS token: %v", name, err)
		}
	}
	if got := len(srv.Requests()); got != n {
		t.Errorf("invalid options sent %d requests", got-n)
	}

	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	history, err := ex.History(ctx, start, start.AddDate(0, 1, 0))
	if err != nil || len(history) != 1 || history[0].EnvironmentName != "sb" || history[0].Time == nil || history[0].User != "admin@contoso.com" {
		t.Fatalf("History() = %+v, %v", history, err)
	}
	req, _ := srv.LastRequest()
	q, _ := url.ParseQuery(req.Query)
	if req.Method != http.MethodPost || q.Get("start") != "2026-11-01T00:00:00Z" || q.Get("end") != "2026-12-01T00:00:00Z" {
		t.Errorf("history request = %s ?%s", req.Method, req.Query)
	}
	if _, err := ex.History(ctx, start, start.Add(-time.Hour)); err == nil {
		t.Error("end before start: no error")
	}
	newer := bcadmintest.NewServer(t, bcadmintest.WithAPIVersion("v2.30"))
	if _, err := newer.NewClient(t).Exports().History(ctx, start, start.Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "v2.30") {
		t.Errorf("History on v2.30: %v", err)
	}
}

func TestExportStartReturnsOperationAndIsNotRetried(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.HandleJSON(http.MethodPost, "exports/applications/{family}/environments/{env}", http.StatusAccepted,
		map[string]any{"id": "00000000-0000-0000-0000-000000000901", "type": "DatabaseExport", "status": "Queued"})
	op, err := srv.NewClient(t).Exports().Start(context.Background(), bc, "sb", bcadmin.ExportOptions{StorageAccountSASURI: sasURI, Container: "c", Blob: "b"})
	if err != nil || op == nil || op.ID != "00000000-0000-0000-0000-000000000901" {
		t.Errorf("Start() = %+v, %v", op, err)
	}

	failing := bcadmintest.NewServer(t)
	failing.Handle(http.MethodPost, "exports/applications/{family}/environments/{env}", func(w http.ResponseWriter, _ *http.Request) {
		bcadmintest.WriteError(w, http.StatusInternalServerError, "exportFailed", "The export failed.")
	})
	_, err = failing.NewClient(t).Exports().Start(context.Background(), bc, "sb", bcadmin.ExportOptions{StorageAccountSASURI: sasURI, Container: "c", Blob: "b"})
	if bcadmin.ErrorCode(err) != "exportFailed" || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error = %v", err)
	}
	if n := len(failing.Requests()); n != 1 {
		t.Errorf("export sent %d times, want 1", n)
	}
}

func TestHistoryAcceptsBareArray(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.HandleJSON(http.MethodPost, "exports/history", http.StatusOK, []map[string]any{{"environmentName": "a"}, {"environmentName": "b"}})
	h, err := srv.NewClient(t).Exports().History(context.Background(), time.Now().Add(-time.Hour), time.Now())
	if err != nil || len(h) != 2 || h[1].EnvironmentName != "b" {
		t.Errorf("History() = %+v, %v", h, err)
	}
}

// TestExportStartIgnoresUnexpectedBodies checks that an accepted export is
// not reported as an error when the undocumented response is no operation.
func TestExportStartIgnoresUnexpectedBodies(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`"Export queued"`, `true`, `[{"id":"x"}]`, `{"status":"Accepted"}`, `Accepted`} {
		srv := bcadmintest.NewServer(t)
		srv.Handle(http.MethodPost, "exports/applications/{family}/environments/{env}", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		})
		op, err := srv.NewClient(t).Exports().Start(context.Background(), bc, "sb", bcadmin.ExportOptions{StorageAccountSASURI: sasURI, Container: "c", Blob: "b"})
		if err != nil || op != nil {
			t.Errorf("body %s: Start() = %+v, %v; want nil, nil", body, op, err)
		}
	}
}

func TestExportOptionsMaskSASToken(t *testing.T) {
	t.Parallel()
	o := bcadmin.ExportOptions{StorageAccountSASURI: sasURI, Container: "c", Blob: "b"}
	for _, s := range []string{fmt.Sprint(o), fmt.Sprintf("%+v %#v %s", o, o, o), fmt.Sprintf("%v", &o)} {
		if strings.Contains(s, "SECRET") || !strings.Contains(s, "contosoexports.blob.core.windows.net") {
			t.Errorf("formatted options = %s", s)
		}
	}
	if _, err := bcadmintest.NewServer(t).NewClient(t).Exports().Start(context.Background(), bc, "sb", bcadmin.ExportOptions{StorageAccountSASURI: sasURI, Container: " ", Blob: "b"}); err == nil {
		t.Error("blank container: no error")
	}
}

// TestExportStartUnreadableBody checks that an accepted export whose 2xx
// body cannot be read (cut off, or larger than MaxResponseBytes) is not
// reported as an error.
func TestExportStartUnreadableBody(t *testing.T) {
	t.Parallel()
	opts := bcadmin.ExportOptions{StorageAccountSASURI: sasURI, Container: "c", Blob: "b"}
	t.Run("truncated", func(t *testing.T) {
		t.Parallel()
		srv := bcadmintest.NewServer(t)
		srv.Handle(http.MethodPost, "exports/applications/{family}/environments/{env}", func(w http.ResponseWriter, _ *http.Request) {
			conn, buf, err := http.NewResponseController(w).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"id\":")
			_ = buf.Flush()
			_ = conn.Close()
		})
		op, err := srv.NewClient(t).Exports().Start(context.Background(), bc, "sb", opts)
		if err != nil || op != nil {
			t.Errorf("Start() = %+v, %v; want nil, nil", op, err)
		}
		if n := len(srv.Requests()); n != 1 {
			t.Errorf("sent %d requests, want 1", n)
		}
	})
	t.Run("too large", func(t *testing.T) {
		t.Parallel()
		srv := bcadmintest.NewServer(t)
		srv.Handle(http.MethodPost, "exports/applications/{family}/environments/{env}", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
		})
		o := srv.ClientOptions()
		o.MaxResponseBytes = 1024
		c, err := bcadmin.NewClient(bcadmintest.Credential(), o)
		if err != nil {
			t.Fatal(err)
		}
		if op, err := c.Exports().Start(context.Background(), bc, "sb", opts); err != nil || op != nil {
			t.Errorf("Start() = %+v, %v; want nil, nil", op, err)
		}
	})
	t.Run("error status still fails", func(t *testing.T) {
		t.Parallel()
		srv := bcadmintest.NewServer(t)
		srv.Handle(http.MethodPost, "exports/applications/{family}/environments/{env}", func(w http.ResponseWriter, _ *http.Request) {
			bcadmintest.WriteError(w, http.StatusBadRequest, "exportFailed", "no exports left")
		})
		if _, err := srv.NewClient(t).Exports().Start(context.Background(), bc, "sb", opts); bcadmin.ErrorCode(err) != "exportFailed" {
			t.Errorf("error = %v", err)
		}
	})
}

func TestHistorySingleObject(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.HandleJSON(http.MethodPost, "exports/history", http.StatusOK, map[string]any{"environmentName": "a", "user": "u"})
	h, err := srv.NewClient(t).Exports().History(context.Background(), time.Now().Add(-time.Hour), time.Now())
	if err != nil || len(h) != 1 || h[0].EnvironmentName != "a" {
		t.Errorf("History() = %+v, %v", h, err)
	}
	empty := bcadmintest.NewServer(t)
	empty.Handle(http.MethodPost, "exports/history", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	if h, err := empty.NewClient(t).Exports().History(context.Background(), time.Now().Add(-time.Hour), time.Now()); err != nil || h == nil || len(h) != 0 {
		t.Errorf("History() on an empty body = %#v, %v; want an empty list", h, err)
	}
}

func TestHistoryNestedValueIsAnError(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.HandleJSON(http.MethodPost, "exports/history", http.StatusOK, map[string]any{"value": map[string]any{"value": []any{}}})
	if _, err := srv.NewClient(t).Exports().History(context.Background(), time.Now().Add(-time.Hour), time.Now()); err == nil {
		t.Error("nested value object: no error")
	}
}
