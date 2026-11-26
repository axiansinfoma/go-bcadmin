package bcadmin_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/axiansinfoma/go-bcadmin/bcadmintest"
)

const appClientID = "00000000-0000-0000-0000-000000000b01"

func TestAuthorizedApps(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	serveFixture(t, srv, http.MethodGet, "authorizedAadApps", "testdata/authorizedapps/list.json")
	serveFixture(t, srv, http.MethodGet, "authorizedAadApps/manageableTenants", "testdata/authorizedapps/manageabletenants.json")
	srv.HandleJSON(http.MethodPut, "authorizedAadApps/{id}", http.StatusOK, map[string]any{"appId": appClientID, "isAdminConsentGranted": false})
	srv.Handle(http.MethodDelete, "authorizedAadApps/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	apps := srv.NewClient(t).AuthorizedApps()
	ctx := context.Background()

	list, err := apps.List(ctx)
	if err != nil || len(list) != 2 || list[0].AppID != appClientID || list[0].IsAdminConsentGranted || !list[1].IsAdminConsentGranted {
		t.Errorf("List() = %+v, %v", list, err)
	}
	app, err := apps.Authorize(ctx, appClientID)
	if err != nil || app.AppID != appClientID {
		t.Errorf("Authorize() = %+v, %v", app, err)
	}
	if r := findRequest(t, srv, http.MethodPut, "/admin/v2.29/authorizedAadApps/"+appClientID); len(r.Body) != 0 {
		t.Errorf("authorize body = %q, want none", r.Body)
	}
	if err := apps.Remove(ctx, appClientID); err != nil {
		t.Error(err)
	}
	findRequest(t, srv, http.MethodDelete, "/admin/v2.29/authorizedAadApps/"+appClientID)

	tenants, err := apps.ManageableTenants(ctx)
	if err != nil || len(tenants) != 2 || tenants[1].EntraTenantID != "00000000-0000-0000-0000-000000000002" {
		t.Errorf("ManageableTenants() = %+v, %v", tenants, err)
	}

	n := len(srv.Requests())
	for _, id := range []string{"", " ", "00000000-0000-0000-0000-000000000000", "manageableTenants", "ManageableTenants", "a/b", ".."} {
		if _, err := apps.Authorize(ctx, id); err == nil {
			t.Errorf("Authorize(%q): no error", id)
		}
		if err := apps.Remove(ctx, id); err == nil {
			t.Errorf("Remove(%q): no error", id)
		}
	}
	if got := len(srv.Requests()); got != n {
		t.Errorf("invalid IDs sent %d requests", got-n)
	}

	old := bcadmintest.NewServer(t, bcadmintest.WithAPIVersion("v2.23"))
	if _, err := old.NewClient(t).AuthorizedApps().ManageableTenants(ctx); err == nil || !strings.Contains(err.Error(), "v2.24") {
		t.Errorf("ManageableTenants on v2.23: %v", err)
	}
	if n := len(old.Requests()); n != 0 {
		t.Errorf("v2.23 sent %d requests", n)
	}
}
