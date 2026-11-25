package bcadmin_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/axiansinfoma/go-bcadmin"
	"github.com/axiansinfoma/go-bcadmin/bcadmintest"
)

const (
	appID    = "00000000-0000-0000-0000-000000000101"
	appsPath = "/admin/v2.29/applications/BusinessCentral/environments/sb/apps"
)

func TestAppsReadEndpoints(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	serveFixture(t, srv, http.MethodGet, "applications/{family}/environments/{env}/apps", "testdata/apps/installed.json")
	serveFixture(t, srv, http.MethodGet, "applications/{family}/environments/{env}/apps/availableUpdates", "testdata/apps/availableupdates.json")
	serveFixture(t, srv, http.MethodGet, "applications/{family}/environments/{env}/apps/{app}/uninstallRequirements", "testdata/apps/uninstallrequirements.json")
	serveFixture(t, srv, http.MethodGet, "applications/{family}/environments/{env}/apps/{app}/operations", "testdata/apps/operations.json")
	serveFixture(t, srv, http.MethodGet, "applications/{family}/environments/{env}/apps/scheduledPteOperations", "testdata/apps/scheduledpte.json")
	apps := srv.NewClient(t).Apps(bc, "sb")
	ctx := context.Background()

	installed, err := apps.List(ctx)
	if err != nil || len(installed) != 2 {
		t.Fatalf("List() = %v, %v", installed, err)
	}
	if a := installed[0]; a.AppID != appID || !a.AppType.Is(bcadmin.AppTypeGlobal) || !a.State.Is(bcadmin.AppStateInstalled) || !a.CanBeUninstalled || a.LastUpdateAttemptResult != "Succeeded" {
		t.Errorf("app = %+v", a)
	}
	if a := installed[1]; !a.AppType.Is(bcadmin.AppTypeTenant) || !a.State.Is(bcadmin.AppStateUpdatePending) || a.CanBeUninstalled {
		t.Errorf("PTE = %+v", a)
	}

	updates, err := apps.AvailableUpdates(ctx)
	if err != nil || len(updates) != 1 || updates[0].Version != "2.2.0.0" || len(updates[0].Requirements) != 1 || updates[0].Requirements[0].Type != "Update" {
		t.Errorf("AvailableUpdates() = %+v, %v", updates, err)
	}
	reqs, err := apps.UninstallRequirements(ctx, appID)
	if err != nil || len(reqs) != 1 || reqs[0].Type != "Global" || reqs[0].OperationRequirementType != "Uninstall" {
		t.Errorf("UninstallRequirements() = %+v, %v", reqs, err)
	}
	ops, err := apps.Operations(ctx, appID)
	if err != nil || len(ops) != 1 || ops[0].TargetVersion != "2.1.0.0" || !ops[0].Status.Is(bcadmin.OperationStatusSucceeded) {
		t.Errorf("Operations() = %+v, %v", ops, err)
	}
	scheduled, err := apps.ScheduledPTEOperations(ctx)
	if err != nil || len(scheduled) != 1 || scheduled[0].ScheduleKind != "NextMajorUpdate" || scheduled[0].Parameters["targetRelease"] != "29.0.0.0" || scheduled[0].StartedOn != nil {
		t.Errorf("ScheduledPTEOperations() = %+v, %v", scheduled, err)
	}
	findRequest(t, srv, http.MethodGet, appsPath+"/availableUpdates")
	findRequest(t, srv, http.MethodGet, appsPath+"/"+appID+"/uninstallRequirements")
}

// serveAppOperation answers GET .../apps/{app}/operations/{id} with the
// statuses in order, as a plain object or wrapped in {"value": [...]}.
func serveAppOperation(srv *bcadmintest.Server, wrapped bool, statuses ...string) *atomic.Int32 {
	var calls atomic.Int32
	srv.Handle(http.MethodGet, "applications/{family}/environments/{env}/apps/{app}/operations/{id}", func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1)) - 1
		op := map[string]any{"id": r.PathValue("id"), "status": statuses[min(n, len(statuses)-1)], "type": "install"}
		if wrapped {
			bcadmintest.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{op}})
			return
		}
		bcadmintest.WriteJSON(w, http.StatusOK, op)
	})
	return &calls
}

func TestAppInstallUpdateUninstall(t *testing.T) {
	t.Parallel()
	for _, wrapped := range []bool{false, true} {
		srv := bcadmintest.NewServer(t)
		serveFixture(t, srv, http.MethodPost, "applications/{family}/environments/{env}/apps/{app}/install", "testdata/apps/install-operation.json")
		serveFixture(t, srv, http.MethodPost, "applications/{family}/environments/{env}/apps/{app}/update", "testdata/apps/install-operation.json")
		serveFixture(t, srv, http.MethodPost, "applications/{family}/environments/{env}/apps/{app}/uninstall", "testdata/apps/install-operation.json")
		serveAppOperation(srv, wrapped, "running", "succeeded")
		apps := srv.NewClient(t).Apps(bc, "sb")
		ctx := context.Background()

		p, err := apps.Install(ctx, appID, bcadmin.InstallAppOptions{AcceptISVEULA: true, InstallOrUpdateNeededDependencies: true, LanguageID: "de-DE"})
		if err != nil {
			t.Fatal(err)
		}
		body := lastBody(t, srv)
		if body["acceptIsvEula"] != true || body["installOrUpdateNeededDependencies"] != true || body["useEnvironmentUpdateWindow"] != false ||
			body["languageId"] != "de-DE" || body["allowPreviewVersion"] != false || len(body) != 5 {
			t.Errorf("install body = %v (targetVersion must be omitted)", body)
		}
		if p.Operation().TargetAppVersion != "2.1.0.0" || !p.Scheduled() {
			t.Errorf("install operation = %+v", p.Operation())
		}
		op, err := p.Wait(ctx, nil)
		if err != nil || !op.Status.Is(bcadmin.OperationStatusSucceeded) {
			t.Fatalf("install Wait() = %+v, %v (wrapped %v)", op, err, wrapped)
		}
		findRequest(t, srv, http.MethodGet, appsPath+"/"+appID+"/operations/00000000-0000-0000-0000-000000000301")

		if _, err := apps.Update(ctx, appID, bcadmin.UpdateAppOptions{TargetVersion: "2.2.0.0", UseEnvironmentUpdateWindow: true}); err != nil {
			t.Fatal(err)
		}
		if body := lastBody(t, srv); body["targetVersion"] != "2.2.0.0" || body["useEnvironmentUpdateWindow"] != true || len(body) != 4 {
			t.Errorf("update body = %v", body)
		}
		if _, err := apps.Uninstall(ctx, appID, bcadmin.UninstallAppOptions{UninstallDependents: true}); err != nil {
			t.Fatal(err)
		}
		if body := lastBody(t, srv); body["uninstallDependents"] != true || body["deleteData"] != false || len(body) != 3 {
			t.Errorf("uninstall body = %v", body)
		}
	}
}

func TestAppInstallMissingDependencies(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	b, err := os.ReadFile("testdata/errors/requirements.json")
	if err != nil {
		t.Fatal(err)
	}
	srv.Fail(bcadmintest.Fault{Method: http.MethodPost, Status: http.StatusBadRequest, Body: string(b)})
	_, err = srv.NewClient(t).Apps(bc, "sb").Install(context.Background(), appID, bcadmin.InstallAppOptions{AcceptISVEULA: true})
	re, ok := errors.AsType[*bcadmin.ResponseError](err)
	if !ok || re.Data == nil || len(re.Data.Requirements) != 1 || re.Data.Requirements[0].Name != "Contoso Base" {
		t.Errorf("error = %v", err)
	}
	if n := len(srv.Requests()); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
}

func TestAppValidation(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	apps := srv.NewClient(t).Apps(bc, "sb")
	ctx := context.Background()
	if _, err := apps.Install(ctx, appID, bcadmin.InstallAppOptions{AllowPreviewVersion: true}); err == nil {
		t.Error("preview install without target version did not fail")
	}
	if _, err := apps.Update(ctx, appID, bcadmin.UpdateAppOptions{}); err == nil {
		t.Error("update without target version did not fail")
	}
	if _, err := apps.CancelUpdate(ctx, appID, ""); err == nil {
		t.Error("cancel without operation ID did not fail")
	}
	if _, err := apps.RemoveScheduledPTEVersion(ctx, appID, bcadmin.RemoveScheduledPTEVersionOptions{}); err == nil {
		t.Error("remove without version did not fail")
	}
	if _, err := apps.Install(ctx, "a/b", bcadmin.InstallAppOptions{}); err == nil {
		t.Error("invalid app ID was sent")
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("requests = %d, want 0", n)
	}
	old := bcadmintest.NewServer(t, bcadmintest.WithAPIVersion("v2.5"))
	if _, err := old.NewClient(t).Apps(bc, "sb").Install(ctx, appID, bcadmin.InstallAppOptions{}); err == nil || !strings.Contains(err.Error(), "v2.6") {
		t.Errorf("install on v2.5: %v", err)
	}
}

func TestAppCancelUpdateAndRemoveScheduledPTE(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.HandleJSON(http.MethodPost, "applications/{family}/environments/{env}/apps/{app}/update/cancel", http.StatusOK,
		map[string]any{"id": "op-9", "type": "update", "status": "canceled", "canceledBy": "admin@contoso.com"})
	srv.HandleJSON(http.MethodPost, "applications/{family}/environments/{env}/apps/{app}/removeScheduledPteVersion", http.StatusOK,
		map[string]any{"id": "op-10", "type": "install", "status": "canceled", "scheduleKind": "NextMajorUpdate"})
	apps := srv.NewClient(t).Apps(bc, "sb")
	ctx := context.Background()
	op, err := apps.CancelUpdate(ctx, appID, "op-9")
	if err != nil || !op.Status.Is(bcadmin.OperationStatusCanceled) || op.CanceledBy == "" {
		t.Errorf("CancelUpdate() = %+v, %v", op, err)
	}
	if body := lastBody(t, srv); len(body) != 1 || body["ScheduledOperationId"] != "op-9" {
		t.Errorf("cancel body = %v", body)
	}
	op, err = apps.RemoveScheduledPTEVersion(ctx, appID, bcadmin.RemoveScheduledPTEVersionOptions{TargetVersion: "2.0.0.0", ScheduleKind: bcadmin.DeploymentNextMajorUpdate})
	if err != nil || op.ScheduleKind != "NextMajorUpdate" {
		t.Errorf("RemoveScheduledPTEVersion() = %+v, %v", op, err)
	}
	if body := lastBody(t, srv); len(body) != 2 || body["targetVersion"] != "2.0.0.0" || body["scheduleKind"] != "NextMajorUpdate" {
		t.Errorf("remove body = %v", body)
	}
}

func TestAppGetOperationWrappedNotFound(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.HandleJSON(http.MethodGet, "applications/{family}/environments/{env}/apps/{app}/operations/{id}", http.StatusOK,
		map[string]any{"value": []map[string]any{{"id": "other"}}})
	_, err := srv.NewClient(t).Apps(bc, "sb").GetOperation(context.Background(), appID, "op-1")
	if !bcadmin.IsNotFound(err) {
		t.Errorf("error = %v", err)
	}
}

func TestInstallPTE(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	content := bytes.Repeat([]byte("PK\x03\x04app-bytes"), 4096)
	var gotFields map[string]string
	var gotFile []byte
	var gotName string
	srv.Handle(http.MethodPost, "applications/{family}/environments/{env}/apps/pteInstall", func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			bcadmintest.WriteError(w, http.StatusBadRequest, "BadContentType", err.Error())
			return
		}
		if r.ContentLength <= int64(len(content)) {
			bcadmintest.WriteError(w, http.StatusBadRequest, "NoLength", "Content-Length missing")
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		gotFields = map[string]string{}
		for {
			p, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				bcadmintest.WriteError(w, http.StatusBadRequest, "BadMultipart", err.Error())
				return
			}
			data, _ := io.ReadAll(p)
			if p.FormName() == "extensionFile" {
				gotFile, gotName = data, p.FileName()
				continue
			}
			gotFields[p.FormName()] = string(data)
		}
		b, _ := os.ReadFile("testdata/apps/pte-install-operation.json")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	})
	serveAppOperation(srv, false, "succeeded")
	apps := srv.NewClient(t).Apps(bc, "sb")
	ctx := context.Background()

	p, err := apps.InstallPTE(ctx, bytes.NewReader(content), bcadmin.InstallPTEOptions{
		FileName: `C:\build\Contoso_Customizations_1.0.0.6.app`, AcceptISVEULA: true,
		DeploymentSchedule: bcadmin.DeploymentImmediate, SyncMode: bcadmin.SyncModeForceSync,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotName != "Contoso_Customizations_1.0.0.6.app" || !bytes.Equal(gotFile, content) {
		t.Errorf("file part: name %q, %d bytes", gotName, len(gotFile))
	}
	want := map[string]string{"acceptIsvEula": "true", "installOrUpdateNeededDependencies": "false", "deploymentSchedule": "Immediate", "syncMode": "ForceSync"}
	if len(gotFields) != len(want) {
		t.Errorf("fields = %v, want %v", gotFields, want)
	}
	for k, v := range want {
		if gotFields[k] != v {
			t.Errorf("field %s = %q, want %q", k, gotFields[k], v)
		}
	}
	if p.Operation().AppID != "00000000-0000-0000-0000-000000000102" || p.Operation().ScheduleKind != "Immediate" {
		t.Errorf("operation = %+v", p.Operation())
	}
	if _, err := p.Wait(ctx, nil); err != nil {
		t.Fatal(err)
	}
	findRequest(t, srv, http.MethodGet, appsPath+"/00000000-0000-0000-0000-000000000102/operations/00000000-0000-0000-0000-000000000401")
}

func TestInstallPTEValidation(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	apps := srv.NewClient(t).Apps(bc, "sb")
	ctx := context.Background()
	small := bytes.NewReader([]byte("x"))
	for name, opts := range map[string]bcadmin.InstallPTEOptions{
		"no name":          {},
		"not .app":         {FileName: "x.zip"},
		"header injection": {FileName: "x\r\nX-Injected: 1.app"},
		"quote":            {FileName: `x".app`},
		"bad language":     {FileName: "x.app", LanguageID: "en\r\nX: y"},
	} {
		if _, err := apps.InstallPTE(ctx, small, opts); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := apps.InstallPTE(ctx, nil, bcadmin.InstallPTEOptions{FileName: "x.app"}); err == nil {
		t.Error("nil file: no error")
	}
	if _, err := apps.InstallPTE(ctx, &sizedReader{size: bcadmin.MaxPTESize + 1}, bcadmin.InstallPTEOptions{FileName: "x.app"}); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("too large: %v", err)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("requests = %d, want 0", n)
	}
	old := bcadmintest.NewServer(t, bcadmintest.WithAPIVersion("v2.28"))
	if _, err := old.NewClient(t).Apps(bc, "sb").InstallPTE(ctx, small, bcadmin.InstallPTEOptions{FileName: "x.app"}); err == nil || !strings.Contains(err.Error(), "v2.29") {
		t.Errorf("v2.28: %v", err)
	}
}

// sizedReader reports size bytes without holding them.
type sizedReader struct {
	size, pos int64
}

func (s *sizedReader) Read([]byte) (int, error) { return 0, io.EOF }

func (s *sizedReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekEnd:
		s.pos = s.size + offset
	case io.SeekStart:
		s.pos = offset
	}
	return s.pos, nil
}

func TestUnboundEnvironmentServices(t *testing.T) {
	t.Parallel()
	env := &bcadmin.Environment{Name: "x", ApplicationFamily: bc}
	if _, err := env.Apps().List(context.Background()); err == nil {
		t.Error("unbound Apps().List did not fail")
	}
	if err := env.Updates().Select(context.Background(), "26.1", nil); err == nil {
		t.Error("unbound Updates().Select did not fail")
	}
}

func TestUpdates(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	serveFixture(t, srv, http.MethodGet, "applications/{family}/environments/{env}/updates", "testdata/updates/list.json")
	srv.Handle(http.MethodPatch, "applications/{family}/environments/{env}/updates/{version}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv.Handle(http.MethodPost, "applications/{family}/environments/{env}/operations/{id}/cancel", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	updates := srv.NewClient(t).Updates(bc, "sb")
	ctx := context.Background()

	list, err := updates.List(ctx)
	if err != nil || len(list) != 3 {
		t.Fatalf("List() = %v, %v", list, err)
	}
	if u := list[0]; !u.Selected || !u.Available || u.ScheduleDetails == nil || u.ScheduleDetails.UpdateStatus != "scheduled" ||
		u.ScheduleDetails.SelectedDateTime == nil || u.ScheduleDetails.SelectedDateTime.Day() != 5 || !u.TargetVersionType.Is(bcadmin.TargetVersionGA) {
		t.Errorf("26.1 = %+v", u)
	}
	if u := list[1]; u.ScheduleDetails == nil || u.ScheduleDetails.SelectedDateTime != nil {
		t.Errorf("26.2 = %+v", u)
	}
	if u := list[2]; u.Available || u.ExpectedAvailability == nil || u.ExpectedAvailability.Month != 2 || u.ExpectedAvailability.Year != 2027 {
		t.Errorf("26.3 = %+v", u)
	}

	if err := updates.Select(ctx, "26.3", nil); err != nil {
		t.Fatal(err)
	}
	req, _ := srv.LastRequest()
	if req.Method != http.MethodPatch || req.Path != "/admin/v2.29/applications/BusinessCentral/environments/sb/updates/26.3" || string(req.Body) != `{"selected":true}` {
		t.Errorf("select request = %s %s %s", req.Method, req.Path, req.Body)
	}

	ignore := true
	when := time.Date(2026, 12, 6, 23, 0, 0, 0, time.FixedZone("CET", 3600))
	if err := updates.Select(ctx, "26.2", &bcadmin.ScheduleUpdateOptions{SelectedDateTime: when, IgnoreUpdateWindow: &ignore}); err != nil {
		t.Fatal(err)
	}
	body := lastBody(t, srv)
	details, _ := body["scheduleDetails"].(map[string]any)
	if body["selected"] != true || details["selectedDateTime"] != "2026-12-06T22:00:00Z" || details["ignoreUpdateWindow"] != true || len(body) != 2 {
		t.Errorf("select body = %v", body)
	}

	notIgnore := false
	if err := updates.Reschedule(ctx, "26.1", bcadmin.ScheduleUpdateOptions{IgnoreUpdateWindow: &notIgnore}); err != nil {
		t.Fatal(err)
	}
	body = lastBody(t, srv)
	details, _ = body["scheduleDetails"].(map[string]any)
	if _, ok := body["selected"]; ok || details["ignoreUpdateWindow"] != false || len(details) != 1 {
		t.Errorf("reschedule body = %v (selected must be omitted)", body)
	}
	if err := updates.Reschedule(ctx, "26.1", bcadmin.ScheduleUpdateOptions{}); err == nil {
		t.Error("empty reschedule did not fail")
	}

	if err := updates.CancelRunning(ctx, "op-11"); err != nil {
		t.Fatal(err)
	}
	if req, _ := srv.LastRequest(); req.Method != http.MethodPost || req.Path != "/admin/v2.29/applications/BusinessCentral/environments/sb/operations/op-11/cancel" {
		t.Errorf("cancel request = %s %s", req.Method, req.Path)
	}
}

func TestUpdatesSelectIsNotRetried(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.Fail(bcadmintest.Fault{Method: http.MethodPatch, Status: http.StatusInternalServerError, Body: `{"code":"Unknown","message":"boom"}`})
	err := srv.NewClient(t).Updates(bc, "sb").Select(context.Background(), "26.1", nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %v", err)
	}
	if n := len(srv.Requests()); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
}

func TestAppsThroughBoundEnvironmentAndResume(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.HandleJSON(http.MethodGet, "applications/{family}/environments/{env}", http.StatusOK, map[string]any{"name": "sb", "applicationFamily": bc})
	serveFixture(t, srv, http.MethodGet, "applications/{family}/environments/{env}/apps", "testdata/apps/installed.json")
	serveFixture(t, srv, http.MethodGet, "applications/{family}/environments/{env}/updates", "testdata/updates/list.json")
	serveAppOperation(srv, false, "running", "failed")
	c := srv.NewClient(t)
	ctx := context.Background()
	env, err := c.Environments().Get(ctx, bc, "sb")
	if err != nil {
		t.Fatal(err)
	}
	if apps, err := env.Apps().List(ctx); err != nil || len(apps) != 2 {
		t.Errorf("env.Apps().List() = %v, %v", apps, err)
	}
	if updates, err := env.Updates().List(ctx); err != nil || len(updates) != 3 {
		t.Errorf("env.Updates().List() = %v, %v", updates, err)
	}
	p, err := env.Apps().ResumeOperation(appID, "op-12")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(ctx, nil); err == nil {
		t.Error("failed app operation did not return an error")
	} else if _, ok := errors.AsType[*bcadmin.OperationError](err); !ok {
		t.Errorf("error = %v, want *OperationError", err)
	}
	if _, err := env.Apps().ResumeOperation(appID, ""); err == nil {
		t.Error("ResumeOperation with empty ID did not fail")
	}
}
