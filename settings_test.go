package bcadmin_test

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/axiansinfoma/go-bcadmin"
	"github.com/axiansinfoma/go-bcadmin/bcadmintest"
)

const settingsPrefix = "applications/{family}/environments/{env}/settings/"

func TestSettingsUpdateWindow(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	serveFixture(t, srv, http.MethodGet, settingsPrefix+"upgrade", "testdata/settings/upgrade.json")
	srv.HandleJSON(http.MethodPut, settingsPrefix+"upgrade", http.StatusOK, map[string]any{
		"preferredStartTimeUtc": "2026-11-26T21:00:00Z", "preferredEndTimeUtc": "2026-11-27T05:00:00Z",
	})
	s := srv.NewClient(t).Settings(bc, "sb")
	ctx := context.Background()

	w, err := s.UpdateWindow(ctx)
	if err != nil || w.PreferredStartTime != "22:00" || w.TimeZoneID != "Romance Standard Time" ||
		w.PreferredEndTimeUTC == nil || !w.PreferredEndTimeUTC.Equal(time.Date(2026, 11, 27, 5, 0, 0, 0, time.UTC)) {
		t.Fatalf("UpdateWindow() = %+v, %v", w, err)
	}
	findRequest(t, srv, http.MethodGet, "/admin/v2.29/applications/BusinessCentral/environments/sb/settings/upgrade")

	w, err = s.SetUpdateWindow(ctx, bcadmin.SetUpdateWindowOptions{Start: "22:00", End: "06:00", TimeZoneID: "Romance Standard Time"})
	if err != nil || w.PreferredStartTimeUTC == nil {
		t.Fatalf("SetUpdateWindow(local) = %+v, %v", w, err)
	}
	want := map[string]any{"preferredStartTime": "22:00", "preferredEndTime": "06:00", "timeZoneId": "Romance Standard Time"}
	if got := lastBody(t, srv); !reflect.DeepEqual(got, want) {
		t.Errorf("local body = %v, want %v", got, want)
	}

	berlin := time.FixedZone("CET", 3600)
	if _, err := s.SetUpdateWindow(ctx, bcadmin.SetUpdateWindowOptions{
		StartUTC: time.Date(2026, 11, 26, 22, 0, 0, 0, berlin), EndUTC: time.Date(2026, 11, 27, 6, 0, 0, 0, berlin),
	}); err != nil {
		t.Fatal(err)
	}
	want = map[string]any{"preferredStartTimeUtc": "2026-11-26T21:00:00Z", "preferredEndTimeUtc": "2026-11-27T05:00:00Z"}
	if got := lastBody(t, srv); !reflect.DeepEqual(got, want) {
		t.Errorf("UTC body = %v, want %v", got, want)
	}

	n := len(srv.Requests())
	for name, opts := range map[string]bcadmin.SetUpdateWindowOptions{
		"none":          {},
		"both":          {Start: "22:00", End: "06:00", TimeZoneID: "UTC", StartUTC: time.Now(), EndUTC: time.Now()},
		"no time zone":  {Start: "22:00", End: "06:00"},
		"only UTC end":  {EndUTC: time.Now()},
		"only start":    {Start: "22:00"},
		"zone, no time": {TimeZoneID: "UTC"},
	} {
		if _, err := s.SetUpdateWindow(ctx, opts); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if got := len(srv.Requests()); got != n {
		t.Errorf("invalid options sent %d requests", got-n)
	}

	old := bcadmintest.NewServer(t, bcadmintest.WithAPIVersion("v2.12"))
	if _, err := old.NewClient(t).Settings(bc, "sb").SetUpdateWindow(ctx, bcadmin.SetUpdateWindowOptions{Start: "22:00", End: "06:00", TimeZoneID: "UTC"}); err == nil || !strings.Contains(err.Error(), "v2.13") {
		t.Errorf("local window on v2.12: %v", err)
	}
}

func TestSettingsUpdateWindowNull(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.Handle(http.MethodGet, settingsPrefix+"upgrade", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("null"))
	})
	if w, err := srv.NewClient(t).Settings(bc, "sb").UpdateWindow(context.Background()); err != nil || w != nil {
		t.Errorf("UpdateWindow() = %+v, %v; want nil, nil", w, err)
	}
}

func TestSettingsSecurityGroup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := bcadmintest.NewServer(t)
	serveFixture(t, srv, http.MethodGet, settingsPrefix+"securitygroupaccess", "testdata/settings/securitygroup.json")
	srv.HandleJSON(http.MethodPost, settingsPrefix+"securitygroupaccess", http.StatusOK, nil)
	srv.Handle(http.MethodDelete, settingsPrefix+"securitygroupaccess", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	s := srv.NewClient(t).Settings(bc, "sb")

	g, err := s.SecurityGroup(ctx)
	if err != nil || g == nil || g.ID != "00000000-0000-0000-0000-000000000501" || g.DisplayName != "Security group 1" {
		t.Fatalf("SecurityGroup() = %+v, %v", g, err)
	}
	if err := s.SetSecurityGroup(ctx, "00000000-0000-0000-0000-000000000503"); err != nil {
		t.Fatal(err)
	}
	if got, want := lastBody(t, srv), map[string]any{"Value": "00000000-0000-0000-0000-000000000503"}; !reflect.DeepEqual(got, want) {
		t.Errorf("set body = %v, want %v", got, want)
	}
	if err := s.SetSecurityGroup(ctx, ""); err == nil {
		t.Error("empty group ID: no error")
	}
	if err := s.ClearSecurityGroup(ctx); err != nil {
		t.Fatal(err)
	}
	findRequest(t, srv, http.MethodDelete, "/admin/v2.29/applications/BusinessCentral/environments/sb/settings/securitygroupaccess")

	none := bcadmintest.NewServer(t)
	none.Handle(http.MethodGet, settingsPrefix+"securitygroupaccess", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if g, err := none.NewClient(t).Settings(bc, "sb").SecurityGroup(ctx); err != nil || g != nil {
		t.Errorf("SecurityGroup() with 204 = %+v, %v; want nil, nil", g, err)
	}

	old := bcadmintest.NewServer(t, bcadmintest.WithAPIVersion("v2.7"))
	if _, err := old.NewClient(t).Settings(bc, "sb").SecurityGroup(ctx); err == nil || !strings.Contains(err.Error(), "v2.8") {
		t.Errorf("SecurityGroup on v2.7: %v", err)
	}
}

func TestSettingsPartnerAccess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := bcadmintest.NewServer(t)
	serveFixture(t, srv, http.MethodGet, settingsPrefix+"partneraccess", "testdata/settings/partneraccess.json")
	srv.HandleJSON(http.MethodPut, settingsPrefix+"partneraccess", http.StatusOK, nil)
	s := srv.NewClient(t).Settings(bc, "sb")

	p, err := s.PartnerAccess(ctx)
	if err != nil || !p.Status.Is(bcadmin.PartnerAccessAllowSelectedPartnerTenants) || len(p.AllowedPartnerTenantIDs) != 1 {
		t.Fatalf("PartnerAccess() = %+v, %v", p, err)
	}
	if err := s.SetPartnerAccess(ctx, bcadmin.PartnerAccess{Status: bcadmin.PartnerAccessDisabled}); err != nil {
		t.Fatal(err)
	}
	if got, want := lastBody(t, srv), map[string]any{"status": "Disabled"}; !reflect.DeepEqual(got, want) {
		t.Errorf("set body = %v, want %v", got, want)
	}
	if err := s.SetPartnerAccess(ctx, bcadmin.PartnerAccess{Status: bcadmin.PartnerAccessAllowAllPartnerTenants, AllowedPartnerTenantIDs: []string{"x"}}); err == nil {
		t.Error("tenant IDs with AllowAllPartnerTenants: no error")
	}
	if err := s.SetPartnerAccess(ctx, bcadmin.PartnerAccess{}); err == nil {
		t.Error("empty status: no error")
	}
	old := bcadmintest.NewServer(t, bcadmintest.WithAPIVersion("v2.21"))
	if _, err := old.NewClient(t).Settings(bc, "sb").PartnerAccess(ctx); err == nil || !strings.Contains(err.Error(), "v2.22") {
		t.Errorf("PartnerAccess on v2.21: %v", err)
	}
}

func TestSettingsSimpleValues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := bcadmintest.NewServer(t)
	srv.HandleJSON(http.MethodGet, settingsPrefix+"accesswithm365licenses", http.StatusOK, map[string]any{"enabled": true})
	srv.HandleJSON(http.MethodPost, settingsPrefix+"accesswithm365licenses", http.StatusOK, nil)
	srv.HandleJSON(http.MethodPost, settingsPrefix+"appinsightskey", http.StatusOK, nil)
	srv.HandleJSON(http.MethodPut, settingsPrefix+"appSourceAppsUpdateCadence", http.StatusOK, nil)
	s := srv.NewClient(t).Settings(bc, "sb")

	if on, err := s.AccessWithM365Licenses(ctx); err != nil || !on {
		t.Errorf("AccessWithM365Licenses() = %v, %v", on, err)
	}
	if err := s.SetAccessWithM365Licenses(ctx, false); err != nil {
		t.Fatal(err)
	}
	if got, want := lastBody(t, srv), map[string]any{"enabled": false}; !reflect.DeepEqual(got, want) {
		t.Errorf("M365 body = %v, want %v", got, want)
	}
	if err := s.SetAppInsightsKey(ctx, "InstrumentationKey=00000000-0000-0000-0000-000000000504"); err != nil {
		t.Fatal(err)
	}
	if got, want := lastBody(t, srv), map[string]any{"key": "InstrumentationKey=00000000-0000-0000-0000-000000000504"}; !reflect.DeepEqual(got, want) {
		t.Errorf("App Insights body = %v, want %v", got, want)
	}
	if err := s.SetAppInsightsKey(ctx, " "); err == nil {
		t.Error("empty key: no error")
	}
	if err := s.SetAppSourceUpdateCadence(ctx, bcadmin.AppSourceUpdateMajorMinor); err != nil {
		t.Fatal(err)
	}
	if got, want := lastBody(t, srv), map[string]any{"value": "DuringMajorMinorUpgrade"}; !reflect.DeepEqual(got, want) {
		t.Errorf("cadence body = %v, want %v", got, want)
	}
	if err := s.SetAppSourceUpdateCadence(ctx, ""); err == nil {
		t.Error("empty cadence: no error")
	}
	old := bcadmintest.NewServer(t, bcadmintest.WithAPIVersion("v2.11"))
	if err := old.NewClient(t).Settings(bc, "sb").SetAccessWithM365Licenses(ctx, true); err == nil || !strings.Contains(err.Error(), "v2.12") {
		t.Errorf("SetAccessWithM365Licenses on v2.11: %v", err)
	}
}

func TestSettingsErrorPassThrough(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.Handle(http.MethodPost, settingsPrefix+"appinsightskey", func(w http.ResponseWriter, _ *http.Request) {
		bcadmintest.WriteError(w, http.StatusBadRequest, "cannotSetAppInsightsKey", "The environment status isn't Active.")
	})
	err := srv.NewClient(t).Settings(bc, "sb").SetAppInsightsKey(context.Background(), "k")
	if bcadmin.ErrorCode(err) != "cannotSetAppInsightsKey" || !strings.Contains(err.Error(), "isn't Active") {
		t.Errorf("error = %v", err)
	}
	if n := len(srv.Requests()); n != 1 {
		t.Errorf("POST was sent %d times, want 1", n)
	}
}

func TestTimeZones(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	serveFixture(t, srv, http.MethodGet, "applications/settings/timezones", "testdata/settings/timezones.json")
	zones, err := srv.NewClient(t).Applications().TimeZones(context.Background())
	if err != nil || len(zones) != 2 || zones[0].ID != "Romance Standard Time" || zones[1].CurrentUTCOffset != "-08:00" || !zones[0].SupportsDaylightSavingTime {
		t.Errorf("TimeZones() = %+v, %v", zones, err)
	}
	old := bcadmintest.NewServer(t, bcadmintest.WithAPIVersion("v2.12"))
	if _, err := old.NewClient(t).Applications().TimeZones(context.Background()); err == nil {
		t.Error("TimeZones on v2.12: no error")
	}
}

func TestUnboundEnvironmentSettingsSessionsTelemetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var env bcadmin.Environment
	if _, err := env.Settings().UpdateWindow(ctx); err == nil {
		t.Error("Settings of an unbound environment: no error")
	}
	if _, err := env.Sessions().List(ctx); err == nil {
		t.Error("Sessions of an unbound environment: no error")
	}
	if _, err := env.Telemetry(ctx, bcadmin.TelemetryQuery{}); err == nil {
		t.Error("Telemetry of an unbound environment: no error")
	}
}
