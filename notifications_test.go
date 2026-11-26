package bcadmin_test

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/axiansinfoma/go-bcadmin/bcadmintest"
)

func TestNotifications(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	serveFixture(t, srv, http.MethodGet, "settings/notification", "testdata/notifications/settings.json")
	srv.HandleJSON(http.MethodGet, "settings/notification/recipients", http.StatusOK, map[string]any{"value": []map[string]any{
		{"id": "00000000-0000-0000-0000-000000000701", "email": "admin@contoso.com", "name": "Contoso Admin"},
	}})
	srv.HandleJSON(http.MethodPut, "settings/notification/recipients", http.StatusOK,
		map[string]any{"id": "00000000-0000-0000-0000-000000000702", "email": "ops@contoso.com", "name": "Contoso Ops"})
	srv.Handle(http.MethodDelete, "settings/notification/recipients/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	n := srv.NewClient(t).Notifications()
	ctx := context.Background()

	s, err := n.Settings(ctx)
	if err != nil || s.AADTenantID != "00000000-0000-0000-0000-000000000001" || len(s.Recipients) != 1 || s.Recipients[0].Name != "Contoso Admin" {
		t.Errorf("Settings() = %+v, %v", s, err)
	}
	r, err := n.Recipients(ctx)
	if err != nil || len(r) != 1 || r[0].Email != "admin@contoso.com" {
		t.Errorf("Recipients() = %+v, %v", r, err)
	}
	added, err := n.AddRecipient(ctx, "ops@contoso.com", "Contoso Ops")
	if err != nil || added.ID != "00000000-0000-0000-0000-000000000702" {
		t.Fatalf("AddRecipient() = %+v, %v", added, err)
	}
	if got, want := lastBody(t, srv), map[string]any{"email": "ops@contoso.com", "name": "Contoso Ops"}; !reflect.DeepEqual(got, want) {
		t.Errorf("add body = %v, want %v", got, want)
	}
	if err := n.RemoveRecipient(ctx, added.ID); err != nil {
		t.Fatal(err)
	}
	findRequest(t, srv, http.MethodDelete, "/admin/v2.29/settings/notification/recipients/00000000-0000-0000-0000-000000000702")

	count := len(srv.Requests())
	if _, err := n.AddRecipient(ctx, " ", "x"); err == nil {
		t.Error("empty email: no error")
	}
	if err := n.RemoveRecipient(ctx, "00000000-0000-0000-0000-000000000000"); err == nil {
		t.Error("empty GUID: no error")
	}
	if got := len(srv.Requests()); got != count {
		t.Errorf("invalid input sent %d requests", got-count)
	}
}

func TestAddRecipientIsNotRetried(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.Handle(http.MethodPut, "settings/notification/recipients", func(w http.ResponseWriter, _ *http.Request) {
		bcadmintest.WriteError(w, http.StatusInternalServerError, "Unknown", "boom")
	})
	if _, err := srv.NewClient(t).Notifications().AddRecipient(context.Background(), "a@contoso.com", "A"); err == nil {
		t.Fatal("no error")
	}
	if n := len(srv.Requests()); n != 1 {
		t.Errorf("PUT sent %d times, want 1", n)
	}
}
