package bcadmin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

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
