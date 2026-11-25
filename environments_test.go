package bcadmin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/axiansinfoma/go-bcadmin"
	"github.com/axiansinfoma/go-bcadmin/bcadmintest"
)

const bc = bcadmin.FamilyBusinessCentral

// lastBody returns the JSON body of the last request as a map.
func lastBody(t *testing.T, srv *bcadmintest.Server) map[string]any {
	t.Helper()
	req, ok := srv.LastRequest()
	if !ok {
		t.Fatal("no request")
	}
	var m map[string]any
	if err := json.Unmarshal(req.Body, &m); err != nil {
		t.Fatalf("request body %q: %v", req.Body, err)
	}
	return m
}

// findRequest returns the first request with method and escaped path.
func findRequest(t *testing.T, srv *bcadmintest.Server, method, path string) bcadmintest.Request {
	t.Helper()
	for _, r := range srv.Requests() {
		if r.Method == method && r.Path == path {
			return r
		}
	}
	t.Fatalf("no request %s %s", method, path)
	return bcadmintest.Request{}
}

func TestEnvironmentsListAndGet(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	serveFixture(t, srv, http.MethodGet, "applications/environments", "testdata/environments/list.json")
	serveFixture(t, srv, http.MethodGet, "applications/{family}/environments", "testdata/environments/list.json")
	c := srv.NewClient(t)
	ctx := context.Background()

	all, err := c.Environments().List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("List() returned %d environments", len(all))
	}
	prod, sb := all[0], all[1]
	if prod.Name != "Production" || !prod.Type.Is(bcadmin.EnvironmentTypeProduction) || !prod.Status.Is(bcadmin.EnvironmentStatusActive) ||
		prod.VersionDetails == nil || prod.VersionDetails.Version != "27.1" ||
		prod.VersionDetails.EnforcedUpdatePeriodStartDate == nil || prod.VersionDetails.EnforcedUpdatePeriodStartDate.Month() != time.December ||
		prod.AppSourceAppsUpdateCadence != "Default" || prod.SoftDeletedOn != nil {
		t.Errorf("Production = %+v", prod)
	}
	if !sb.Status.Is(bcadmin.EnvironmentStatusSoftDeleted) || sb.SoftDeletedOn == nil || sb.HardDeletePendingOn == nil || sb.DeleteReason != "requestedByAdmin" {
		t.Errorf("mysandbox = %+v", sb)
	}
	if _, err := c.Environments().List(ctx, bc); err != nil {
		t.Fatal(err)
	}
	if req, _ := srv.LastRequest(); req.Path != "/admin/v2.29/applications/BusinessCentral/environments" {
		t.Errorf("path = %s", req.Path)
	}
}

func TestEnvironmentGetAcceptsBothShapes(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.HandleJSON(http.MethodGet, "applications/{family}/environments/plain", http.StatusOK, map[string]any{"name": "plain", "applicationFamily": bc, "status": "Active"})
	srv.HandleJSON(http.MethodGet, "applications/{family}/environments/wrapped", http.StatusOK, map[string]any{"value": []map[string]any{{"name": "wrapped", "applicationFamily": bc}}})
	c := srv.NewClient(t)
	for _, name := range []string{"plain", "wrapped"} {
		env, err := c.Environments().Get(context.Background(), bc, name)
		if err != nil || env.Name != name {
			t.Errorf("Get(%s) = %+v, %v", name, env, err)
		}
	}
}

func TestEnvironmentGetNotFound(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.EnableEnvironments()
	_, err := srv.NewClient(t).Environments().Get(context.Background(), bc, "missing")
	if !bcadmin.IsNotFound(err) || bcadmin.ErrorCode(err) != "environmentNotFound" {
		t.Errorf("error = %v", err)
	}
}

func TestEnvironmentLifecycle(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	fake := srv.EnableEnvironments()
	fake.SetPollsUntilDone(3)
	c := srv.NewClient(t)
	ctx := context.Background()

	// Create.
	p, err := c.Environments().Create(ctx, bc, "uat", bcadmin.CreateEnvironmentOptions{Type: bcadmin.EnvironmentTypeSandbox, CountryCode: "DE"})
	if err != nil {
		t.Fatal(err)
	}
	if body := lastBody(t, srv); len(body) != 2 || body["environmentType"] != "Sandbox" || body["countryCode"] != "DE" {
		t.Errorf("create body = %v (optional fields must be omitted)", body)
	}
	if req, _ := srv.LastRequest(); req.Method != http.MethodPut || req.Path != "/admin/v2.29/applications/BusinessCentral/environments/uat" {
		t.Errorf("create request = %s %s", req.Method, req.Path)
	}
	if p.ID() == "" || !p.Operation().Type.Is(bcadmin.OperationTypeCreate) {
		t.Errorf("operation = %+v", p.Operation())
	}
	env, err := p.Wait(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if env.Name != "uat" || !env.Status.Is(bcadmin.EnvironmentStatusActive) {
		t.Errorf("created = %+v", env)
	}
	// Operations are polled through the tenant-wide endpoint (v2.28+).
	findRequest(t, srv, http.MethodGet, "/admin/v2.29/environments/operations/"+p.ID())

	// Copy through the bound environment.
	cp, err := env.Copy(ctx, "uat-copy", bcadmin.EnvironmentTypeSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if body := lastBody(t, srv); body["environmentName"] != "uat-copy" || body["type"] != "Sandbox" {
		t.Errorf("copy body = %v", body)
	}
	copied, err := cp.Wait(ctx, nil)
	if err != nil || copied.Name != "uat-copy" {
		t.Fatalf("copy = %+v, %v", copied, err)
	}

	// Rename: the result is the environment under its new name.
	rp, err := copied.Rename(ctx, "uat-renamed")
	if err != nil {
		t.Fatal(err)
	}
	if body := lastBody(t, srv); len(body) != 1 || body["NewEnvironmentName"] != "uat-renamed" {
		t.Errorf("rename body = %v", body)
	}
	renamed, err := rp.Wait(ctx, nil)
	if err != nil || renamed.Name != "uat-renamed" {
		t.Fatalf("rename = %+v, %v", renamed, err)
	}
	if _, ok := fake.Get(bc, "uat-copy"); ok {
		t.Error("old name still exists after rename")
	}

	// Delete, then recover.
	dp, err := renamed.Delete(ctx)
	if err != nil {
		t.Fatal(err)
	}
	op, err := dp.Wait(ctx, nil)
	if err != nil || !op.Status.Is(bcadmin.OperationStatusSucceeded) || !op.Type.Is(bcadmin.OperationTypeSoftDelete) {
		t.Fatalf("delete = %+v, %v", op, err)
	}
	deleted, err := renamed.Refresh(ctx)
	if err != nil || !deleted.Status.Is(bcadmin.EnvironmentStatusSoftDeleted) {
		t.Fatalf("after delete = %+v, %v", deleted, err)
	}
	recp, err := deleted.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := recp.Wait(ctx, nil)
	if err != nil || !recovered.Status.Is(bcadmin.EnvironmentStatusActive) {
		t.Fatalf("recover = %+v, %v", recovered, err)
	}

	// Restore.
	pit := time.Date(2026, 11, 24, 21, 0, 0, 0, time.FixedZone("CET", 3600))
	resp, err := env.Restore(ctx, bcadmin.RestoreOptions{EnvironmentName: "uat-restored", EnvironmentType: bcadmin.EnvironmentTypeSandbox, PointInTime: pit, SkipInstallingPTEs: true})
	if err != nil {
		t.Fatal(err)
	}
	body := lastBody(t, srv)
	if body["EnvironmentName"] != "uat-restored" || body["EnvironmentType"] != "Sandbox" || body["PointInTime"] != "2026-11-24T20:00:00Z" ||
		body["SkipInstallingPTEs"] != true || len(body) != 4 {
		t.Errorf("restore body = %v", body)
	}
	if restored, err := resp.Wait(ctx, nil); err != nil || restored.Name != "uat-restored" {
		t.Fatalf("restore = %+v, %v", restored, err)
	}

	// Operations of an environment.
	ops, err := env.Operations(ctx)
	if err != nil || len(ops) == 0 {
		t.Fatalf("Operations() = %v, %v", ops, err)
	}
}

func TestEnvironmentOperationFails(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	fake := srv.EnableEnvironments()
	fake.Add(bcadmin.Environment{Name: "Production", Type: bcadmin.EnvironmentTypeProduction})
	fake.FailNextOperation("The target environment name is already in use.")
	c := srv.NewClient(t)
	p, err := c.Environments().Copy(context.Background(), bc, "Production", "sb", bcadmin.EnvironmentTypeSandbox)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Wait(context.Background(), nil)
	oe, ok := errors.AsType[*bcadmin.OperationError](err)
	if !ok || !strings.Contains(err.Error(), "The target environment name is already in use.") || oe.Operation.ID != p.ID() {
		t.Errorf("Wait() error = %v", err)
	}
	if _, ok := fake.Get(bc, "sb"); ok {
		t.Error("failed copy created the environment")
	}
}

func TestEnvironmentCreateConflictAndValidation(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	fake := srv.EnableEnvironments()
	fake.Add(bcadmin.Environment{Name: "uat", Type: bcadmin.EnvironmentTypeSandbox})
	c := srv.NewClient(t)
	ctx := context.Background()
	_, err := c.Environments().Create(ctx, bc, "uat", bcadmin.CreateEnvironmentOptions{Type: bcadmin.EnvironmentTypeSandbox, CountryCode: "DE"})
	if !bcadmin.IsConflict(err) || bcadmin.ErrorCode(err) != "resourceExists" {
		t.Errorf("create existing: %v", err)
	}
	if _, err := c.Environments().Create(ctx, bc, "x", bcadmin.CreateEnvironmentOptions{}); err == nil {
		t.Error("create without type and country did not fail")
	}
	if _, err := c.Environments().Create(ctx, bc, "a/b", bcadmin.CreateEnvironmentOptions{Type: "Sandbox", CountryCode: "DE"}); err == nil {
		t.Error("invalid environment name was sent")
	}
	if _, err := c.Environments().Restore(ctx, bc, "uat", bcadmin.RestoreOptions{EnvironmentName: "x"}); err == nil {
		t.Error("restore without type and time did not fail")
	}
	if _, err := c.Environments().Rename(ctx, bc, "uat", ""); err == nil {
		t.Error("rename without name did not fail")
	}
}

func TestEnvironmentCreateIsNotRetriedOnServerError(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.EnableEnvironments()
	srv.Fail(bcadmintest.Fault{Method: http.MethodPut, Status: http.StatusBadGateway, Body: `{"code":"BadGateway","message":"upstream"}`, Times: 1})
	srv.Fail(bcadmintest.Fault{Method: http.MethodDelete, Status: http.StatusInternalServerError, Times: 1})
	c := srv.NewClient(t)
	_, err := c.Environments().Create(context.Background(), bc, "uat", bcadmin.CreateEnvironmentOptions{Type: "Sandbox", CountryCode: "DE"})
	if err == nil || !strings.Contains(err.Error(), "upstream") {
		t.Errorf("error = %v", err)
	}
	_, err = c.Environments().Delete(context.Background(), bc, "uat")
	if err == nil {
		t.Error("delete did not fail")
	}
	if n := len(srv.Requests()); n != 2 {
		t.Errorf("requests = %d, want 2 (no retries)", n)
	}
}

func TestWaitUntilEnvironmentActive(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.HandleJSON(http.MethodPut, "applications/{family}/environments/{env}", http.StatusAccepted, map[string]any{"id": "op-1", "status": "scheduled", "type": "create"})
	srv.HandleJSON(http.MethodGet, "environments/operations/{id}", http.StatusOK, map[string]any{"id": "op-1", "status": "succeeded", "type": "create"})
	var gets atomic.Int32
	srv.Handle(http.MethodGet, "applications/{family}/environments/{env}", func(w http.ResponseWriter, _ *http.Request) {
		status := "Preparing"
		if gets.Add(1) >= 3 {
			status = "Active"
		}
		bcadmintest.WriteJSON(w, http.StatusOK, map[string]any{"name": "uat", "applicationFamily": bc, "status": status})
	})
	c := srv.NewClient(t)
	p, err := c.Environments().Create(context.Background(), bc, "uat", bcadmin.CreateEnvironmentOptions{Type: "Sandbox", CountryCode: "DE"})
	if err != nil {
		t.Fatal(err)
	}
	env, err := p.Wait(context.Background(), &bcadmin.WaitOptions{UntilEnvironmentActive: true})
	if err != nil || !env.Status.Is(bcadmin.EnvironmentStatusActive) || gets.Load() != 3 {
		t.Errorf("Wait() = %+v, %v after %d GETs", env, err, gets.Load())
	}
}

func TestWaitUntilEnvironmentActiveFails(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.HandleJSON(http.MethodPost, "applications/{family}/environments/{env}/recover", http.StatusAccepted, map[string]any{"id": "op-2", "status": "succeeded"})
	srv.HandleJSON(http.MethodGet, "applications/{family}/environments/{env}", http.StatusOK, map[string]any{"name": "uat", "status": "RecoveringFailed"})
	p, err := srv.NewClient(t).Environments().Recover(context.Background(), bc, "uat")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Wait(context.Background(), &bcadmin.WaitOptions{UntilEnvironmentActive: true})
	if err == nil || !strings.Contains(err.Error(), "RecoveringFailed") {
		t.Errorf("Wait() error = %v", err)
	}
}

func TestEnvironmentOperationsBeforeV228(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t, bcadmintest.WithAPIVersion("v2.27"))
	fake := srv.EnableEnvironments()
	fake.Add(bcadmin.Environment{Name: "Production", Type: bcadmin.EnvironmentTypeProduction})
	c := srv.NewClient(t)
	p, err := c.Environments().Copy(context.Background(), bc, "Production", "sb", bcadmin.EnvironmentTypeSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	for _, r := range srv.Requests() {
		if strings.HasPrefix(r.Path, "/admin/v2.27/environments/operations/") {
			t.Errorf("v2.27 client used the v2.28 endpoint %s", r.Path)
		}
	}
	findRequest(t, srv, http.MethodGet, "/admin/v2.27/applications/BusinessCentral/environments/Production/operations")
}

func TestEnvironmentResumeOperation(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	fake := srv.EnableEnvironments()
	fake.Add(bcadmin.Environment{Name: "sb", Type: bcadmin.EnvironmentTypeSandbox})
	c := srv.NewClient(t)
	started, err := c.Environments().MatchProductionConfiguration(context.Background(), bc, "sb")
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := c.Environments().ResumeOperation(bc, "sb", started.ID())
	if err != nil {
		t.Fatal(err)
	}
	op, err := resumed.Wait(context.Background(), nil)
	if err != nil || !op.Type.Is(bcadmin.OperationTypeMoveToProductionDBConfig) {
		t.Errorf("Wait() = %+v, %v", op, err)
	}
	got, err := c.Environments().GetOperation(context.Background(), bc, "sb", started.ID())
	if err != nil || got.ID != started.ID() {
		t.Errorf("GetOperation() = %+v, %v", got, err)
	}
	if _, err := c.Environments().ResumeOperation(bc, "sb", ""); err == nil {
		t.Error("ResumeOperation with empty ID did not fail")
	}
}

func TestEnvironmentReadEndpoints(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	serveFixture(t, srv, http.MethodGet, "applications/{family}/environments/{env}/availableRestorePeriods", "testdata/environments/restoreperiods.json")
	srv.HandleJSON(http.MethodGet, "applications/{family}/environments/{env}/usedstorage", http.StatusOK,
		map[string]any{"environmentType": "Sandbox", "environmentName": "sb", "applicationFamily": bc, "databaseStorageInKilobytes": 5368709120})
	srv.HandleJSON(http.MethodGet, "applications/{family}/environments/{env}/matchProductionConfiguration/metrics", http.StatusOK,
		map[string]any{
			"requestsPerMonth": 3, "requestsUsedThisMonth": 1, "requestsRemainingThisMonth": 2, "resetsAtUtc": "2026-12-01T00:00:00Z",
			"canMatchProductionConfiguration": false, "blockedReason": "AlreadyActive", "durationInHours": 72,
		})
	c := srv.NewClient(t)
	ctx := context.Background()

	periods, err := c.Environments().RestorePeriods(ctx, bc, "sb")
	if err != nil || len(periods) != 2 || periods[1].CorrespondingApplicationPackageVersion != "21.5.0.0" || periods[0].From == nil {
		t.Errorf("RestorePeriods() = %+v, %v", periods, err)
	}
	u, err := c.Environments().UsedStorage(ctx, bc, "sb")
	if err != nil || u.DatabaseStorageInKilobytes != 5368709120 {
		t.Errorf("UsedStorage() = %+v, %v", u, err)
	}
	m, err := c.Environments().MatchProductionConfigurationMetrics(ctx, bc, "sb")
	if err != nil || m.CanMatchProductionConfiguration || m.BlockedReason != "AlreadyActive" || m.RequestsRemainingThisMonth != 2 || m.ResetsAtUTC == nil {
		t.Errorf("Metrics() = %+v, %v", m, err)
	}
}

func TestPowerPlatformLink(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	fake := srv.EnableEnvironments()
	fake.Add(bcadmin.Environment{Name: "Production"})
	c := srv.NewClient(t)
	ctx := context.Background()
	if err := c.Environments().LinkPowerPlatform(ctx, bc, "Production", "pp env&1"); err != nil {
		t.Fatal(err)
	}
	req, _ := srv.LastRequest()
	if req.Method != http.MethodPost || req.Path != "/admin/v2.29/bap/applications/BusinessCentral/environments/Production/linkEnvironment" ||
		req.Query != "powerPlatformEnvironmentId=pp+env%261" {
		t.Errorf("request = %s %s?%s", req.Method, req.Path, req.Query)
	}
	if err := c.Environments().UnlinkPowerPlatform(ctx, bc, "Production", "pp"); err != nil {
		t.Fatal(err)
	}
	if err := c.Environments().LinkPowerPlatform(ctx, bc, "Production", ""); err == nil {
		t.Error("empty ID did not fail")
	}
}

func TestUnboundEnvironment(t *testing.T) {
	t.Parallel()
	env := &bcadmin.Environment{Name: "x", ApplicationFamily: bc}
	if _, err := env.Refresh(context.Background()); err == nil {
		t.Error("unbound Refresh did not fail")
	}
	if _, err := env.Delete(context.Background()); err == nil {
		t.Error("unbound Delete did not fail")
	}
}
