package bcadmintest_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"slices"
	"strings"
	"testing"

	"github.com/axiansinfoma/go-bcadmin"
	"github.com/axiansinfoma/go-bcadmin/bcadmintest"
)

const (
	baseID      = "00000000-0000-0000-0000-000000000201"
	connectorID = "00000000-0000-0000-0000-000000000202"
	pteID       = "00000000-0000-0000-0000-000000000203"
)

// newAppsFake returns a fake with Contoso Base 1.0.0.0 and 1.5.0.0 and
// Contoso Connector 2.0.0.0 (needs Base 1.0) and 2.2.0.0 (needs Base 1.5).
func newAppsFake(t *testing.T) (*bcadmintest.Apps, *bcadmin.AppsService) {
	t.Helper()
	fake, apps, _ := newAppsServer(t)
	return fake, apps
}

// newAppsServer is newAppsFake that also returns the server.
func newAppsServer(t *testing.T) (*bcadmintest.Apps, *bcadmin.AppsService, *bcadmintest.Server) {
	t.Helper()
	srv := bcadmintest.NewServer(t)
	fake := srv.EnableApps()
	fake.Publish(bcadmintest.App{AppID: baseID, Name: "Contoso Base", Publisher: "Contoso", Version: "1.0.0.0"})
	fake.Publish(bcadmintest.App{AppID: baseID, Name: "Contoso Base", Publisher: "Contoso", Version: "1.5.0.0"})
	fake.Publish(bcadmintest.App{
		AppID: connectorID, Name: "Contoso Connector", Publisher: "Contoso", Version: "2.0.0.0",
		Dependencies: []bcadmintest.Dependency{{AppID: baseID, MinVersion: "1.0.0.0"}},
	})
	fake.Publish(bcadmintest.App{
		AppID: connectorID, Name: "Contoso Connector", Publisher: "Contoso", Version: "2.2.0.0",
		Dependencies: []bcadmintest.Dependency{{AppID: baseID, MinVersion: "1.5.0.0"}},
	})
	return fake, srv.NewClient(t).Apps(bcadmin.FamilyBusinessCentral, "sb"), srv
}

func installed(t *testing.T, fake *bcadmintest.Apps) map[string]bcadmin.InstalledApp {
	t.Helper()
	out := map[string]bcadmin.InstalledApp{}
	for _, a := range fake.Installed(bcadmin.FamilyBusinessCentral, "sb") {
		out[a.AppID] = a
	}
	return out
}

func TestAppsFakeInstallAndDependencies(t *testing.T) {
	t.Parallel()
	fake, apps := newAppsFake(t)
	ctx := context.Background()

	if _, err := apps.Install(ctx, connectorID, bcadmin.InstallAppOptions{}); bcadmin.ErrorCode(err) != "eulaNotAccepted" {
		t.Errorf("install without EULA: %v", err)
	}
	if _, err := apps.Install(ctx, "00000000-0000-0000-0000-000000000999", bcadmin.InstallAppOptions{AcceptISVEULA: true}); !bcadmin.IsNotFound(err) {
		t.Errorf("install unknown app: %v", err)
	}
	_, err := apps.Install(ctx, connectorID, bcadmin.InstallAppOptions{AcceptISVEULA: true, TargetVersion: "2.0.0.0"})
	var re *bcadmin.ResponseError
	if !errors.As(err, &re) || re.Code != "EntityValidationFailed" || re.Data == nil || len(re.Data.Requirements) != 1 ||
		re.Data.Requirements[0].AppID != baseID || re.Data.Requirements[0].Version != "1.5.0.0" || re.Data.Requirements[0].Type != "Install" {
		t.Fatalf("install with missing dependency: %v", err)
	}
	if n := len(installed(t, fake)); n != 0 {
		t.Fatalf("installed after refused install: %d apps", n)
	}

	p, err := apps.Install(ctx, connectorID, bcadmin.InstallAppOptions{
		AcceptISVEULA: true, TargetVersion: "2.0.0.0", InstallOrUpdateNeededDependencies: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apps.Install(ctx, connectorID, bcadmin.InstallAppOptions{AcceptISVEULA: true}); !bcadmin.IsConflict(err) || bcadmin.ErrorCode(err) != "appOperationInProgress" {
		t.Errorf("second install while the first runs: %v", err)
	}
	op, err := p.Wait(ctx, nil)
	if err != nil || !op.Status.Is(bcadmin.OperationStatusSucceeded) || op.TargetAppVersion != "2.0.0.0" {
		t.Fatalf("Wait() = %+v, %v", op, err)
	}
	got := installed(t, fake)
	if got[connectorID].Version != "2.0.0.0" || got[baseID].Version != "1.5.0.0" || got[connectorID].LastOperationID != op.ID ||
		!got[connectorID].AppType.Is(bcadmin.AppTypeGlobal) || !got[connectorID].State.Is(bcadmin.AppStateInstalled) {
		t.Errorf("installed = %+v", got)
	}
	if _, err := apps.Install(ctx, connectorID, bcadmin.InstallAppOptions{AcceptISVEULA: true}); !bcadmin.IsConflict(err) || bcadmin.ErrorCode(err) != "appAlreadyInstalled" {
		t.Errorf("install of an installed app: %v", err)
	}

	list, err := apps.List(ctx)
	if err != nil || len(list) != 2 {
		t.Errorf("List() = %+v, %v", list, err)
	}
	updates, err := apps.AvailableUpdates(ctx)
	if err != nil || len(updates) != 1 || updates[0].AppID != connectorID || updates[0].Version != "2.2.0.0" || len(updates[0].Requirements) != 0 {
		t.Errorf("AvailableUpdates() = %+v, %v", updates, err)
	}
	ops, err := apps.Operations(ctx, connectorID)
	if err != nil || len(ops) != 1 || ops[0].ID != op.ID {
		t.Errorf("Operations() = %+v, %v", ops, err)
	}
}

func TestAppsFakeUpdate(t *testing.T) {
	t.Parallel()
	fake, apps := newAppsFake(t)
	fake.Install(bcadmin.FamilyBusinessCentral, "sb", bcadmin.InstalledApp{AppID: baseID, Name: "Contoso Base", Publisher: "Contoso", Version: "1.0.0.0", CanBeUninstalled: true})
	fake.Install(bcadmin.FamilyBusinessCentral, "sb", bcadmin.InstalledApp{AppID: connectorID, Name: "Contoso Connector", Publisher: "Contoso", Version: "2.0.0.0", CanBeUninstalled: true})
	ctx := context.Background()

	updates, err := apps.AvailableUpdates(ctx)
	if err != nil || len(updates) != 2 {
		t.Fatalf("AvailableUpdates() = %+v, %v", updates, err)
	}
	for _, u := range updates {
		if u.AppID == connectorID && (len(u.Requirements) != 1 || u.Requirements[0].Type != "Update") {
			t.Errorf("connector update requirements = %+v", u.Requirements)
		}
	}
	if _, err := apps.Update(ctx, connectorID, bcadmin.UpdateAppOptions{TargetVersion: "2.0.0.0"}); bcadmin.ErrorCode(err) != "invalidTargetVersion" {
		t.Errorf("update to the installed version: %v", err)
	}
	if _, err := apps.Update(ctx, connectorID, bcadmin.UpdateAppOptions{TargetVersion: "9.0.0.0"}); bcadmin.ErrorCode(err) != "versionNotFound" {
		t.Errorf("update to an unknown version: %v", err)
	}

	// Scheduled for the update window, then canceled.
	p, err := apps.Update(ctx, connectorID, bcadmin.UpdateAppOptions{
		TargetVersion: "2.2.0.0", UseEnvironmentUpdateWindow: true, InstallOrUpdateNeededDependencies: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(ctx, &bcadmin.WaitOptions{TreatScheduledAsDone: true}); !errors.Is(err, bcadmin.ErrOperationScheduled) {
		t.Errorf("Wait(TreatScheduledAsDone) = %v", err)
	}
	if s := installed(t, fake)[connectorID].State; !s.Is(bcadmin.AppStateUpdatePending) {
		t.Errorf("state while scheduled = %q", s)
	}
	canceled, err := apps.CancelUpdate(ctx, connectorID, p.ID())
	if err != nil || !canceled.Status.Is(bcadmin.OperationStatusCanceled) {
		t.Fatalf("CancelUpdate() = %+v, %v", canceled, err)
	}
	if _, err := apps.CancelUpdate(ctx, connectorID, p.ID()); bcadmin.ErrorCode(err) != "operationNotScheduled" {
		t.Errorf("second CancelUpdate: %v", err)
	}
	if a := installed(t, fake)[connectorID]; a.Version != "2.0.0.0" || !a.State.Is(bcadmin.AppStateInstalled) || a.LastUpdateAttemptResult != "Canceled" {
		t.Errorf("after cancel = %+v", a)
	}

	// Immediate, with polling.
	fake.SetPollsUntilDone(2)
	p, err = apps.Update(ctx, connectorID, bcadmin.UpdateAppOptions{TargetVersion: "2.2.0.0", InstallOrUpdateNeededDependencies: true})
	if err != nil {
		t.Fatal(err)
	}
	if op, err := p.Poll(ctx); err != nil || !op.Status.Is(bcadmin.OperationStatusRunning) {
		t.Errorf("first Poll() = %+v, %v", op, err)
	}
	if s := installed(t, fake)[connectorID].State; !s.Is(bcadmin.AppStateUpdating) {
		t.Errorf("state while running = %q", s)
	}
	if _, err := p.Wait(ctx, nil); err != nil {
		t.Fatal(err)
	}
	got := installed(t, fake)
	if got[connectorID].Version != "2.2.0.0" || got[baseID].Version != "1.5.0.0" || !got[connectorID].State.Is(bcadmin.AppStateInstalled) {
		t.Errorf("after update = %+v", got)
	}

	// A failing update changes nothing but the result.
	fake.Publish(bcadmintest.App{AppID: connectorID, Name: "Contoso Connector", Publisher: "Contoso", Version: "3.0.0.0"})
	fake.FailNextOperation("The update failed.")
	p, err = apps.Update(ctx, connectorID, bcadmin.UpdateAppOptions{TargetVersion: "3.0.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	var oe *bcadmin.OperationError
	if _, err := p.Wait(ctx, nil); !errors.As(err, &oe) || oe.Operation.ErrorMessage != "The update failed." {
		t.Errorf("failing update: %v", err)
	}
	if a := installed(t, fake)[connectorID]; a.Version != "2.2.0.0" || a.LastUpdateAttemptResult != "Failed" {
		t.Errorf("after failed update = %+v", a)
	}
}

func TestAppsFakeUninstall(t *testing.T) {
	t.Parallel()
	fake, apps := newAppsFake(t)
	fake.Install(bcadmin.FamilyBusinessCentral, "sb", bcadmin.InstalledApp{AppID: baseID, Name: "Contoso Base", Publisher: "Contoso", Version: "1.5.0.0", CanBeUninstalled: true})
	fake.Install(bcadmin.FamilyBusinessCentral, "sb", bcadmin.InstalledApp{AppID: connectorID, Name: "Contoso Connector", Publisher: "Contoso", Version: "2.2.0.0", CanBeUninstalled: true})
	fake.Install(bcadmin.FamilyBusinessCentral, "sb", bcadmin.InstalledApp{AppID: "00000000-0000-0000-0000-000000000204", Name: "System", Version: "27.0.0.0"})
	ctx := context.Background()

	reqs, err := apps.UninstallRequirements(ctx, baseID)
	if err != nil || len(reqs) != 1 || reqs[0].AppID != connectorID || reqs[0].OperationRequirementType != "Uninstall" || reqs[0].Type != "Global" {
		t.Errorf("UninstallRequirements() = %+v, %v", reqs, err)
	}
	_, err = apps.Uninstall(ctx, baseID, bcadmin.UninstallAppOptions{})
	var re *bcadmin.ResponseError
	if !errors.As(err, &re) || re.Code != "EntityValidationFailed" || re.Data == nil || len(re.Data.Requirements) != 1 || re.Data.Requirements[0].AppID != connectorID {
		t.Errorf("uninstall with dependents: %v", err)
	}
	if _, err := apps.Uninstall(ctx, "00000000-0000-0000-0000-000000000204", bcadmin.UninstallAppOptions{}); bcadmin.ErrorCode(err) != "appCannotBeUninstalled" {
		t.Errorf("uninstall of a protected app: %v", err)
	}
	p, err := apps.Uninstall(ctx, baseID, bcadmin.UninstallAppOptions{UninstallDependents: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got := installed(t, fake); len(got) != 1 {
		t.Errorf("after uninstall = %+v", got)
	}
	if _, err := apps.Uninstall(ctx, baseID, bcadmin.UninstallAppOptions{}); !bcadmin.IsNotFound(err) {
		t.Errorf("uninstall of an uninstalled app: %v", err)
	}
}

func TestAppsFakePTE(t *testing.T) {
	t.Parallel()
	fake, apps := newAppsFake(t)
	fake.AddPTEFile("Contoso_Customizations_1.0.0.0.app", bcadmintest.App{AppID: pteID, Name: "Contoso Customizations", Publisher: "Contoso", Version: "1.0.0.0"})
	fake.AddPTEFile("Contoso_Customizations_2.0.0.0.app", bcadmintest.App{
		AppID: pteID, Name: "Contoso Customizations", Publisher: "Contoso", Version: "2.0.0.0",
		Dependencies: []bcadmintest.Dependency{{AppID: baseID, MinVersion: "1.0.0.0"}},
	})
	ctx := context.Background()
	file := func() *bytes.Reader { return bytes.NewReader([]byte("app")) }

	if _, err := apps.InstallPTE(ctx, file(), bcadmin.InstallPTEOptions{FileName: "Unknown.app", AcceptISVEULA: true}); bcadmin.ErrorCode(err) != "invalidInput" {
		t.Errorf("unknown file: %v", err)
	}
	if _, err := apps.InstallPTE(ctx, file(), bcadmin.InstallPTEOptions{
		FileName: "Contoso_Customizations_1.0.0.0.app", AcceptISVEULA: true, DeploymentSchedule: bcadmin.DeploymentNextMajorUpdate,
	}); bcadmin.ErrorCode(err) != "invalidInput" {
		t.Errorf("new PTE for the next major update: %v", err)
	}
	p, err := apps.InstallPTE(ctx, file(), bcadmin.InstallPTEOptions{FileName: "Contoso_Customizations_1.0.0.0.app", AcceptISVEULA: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if a := installed(t, fake)[pteID]; a.Version != "1.0.0.0" || !a.AppType.Is(bcadmin.AppTypeTenant) {
		t.Errorf("after PTE install = %+v", a)
	}

	// The next version needs Contoso Base.
	if _, err := apps.InstallPTE(ctx, file(), bcadmin.InstallPTEOptions{FileName: "Contoso_Customizations_2.0.0.0.app", AcceptISVEULA: true}); bcadmin.ErrorCode(err) != "EntityValidationFailed" {
		t.Errorf("PTE with missing dependency: %v", err)
	}
	opts := bcadmin.InstallPTEOptions{
		FileName: "Contoso_Customizations_2.0.0.0.app", AcceptISVEULA: true, InstallOrUpdateNeededDependencies: true,
		DeploymentSchedule: bcadmin.DeploymentNextMajorUpdate,
	}
	if _, err := apps.InstallPTE(ctx, file(), opts); err != nil {
		t.Fatal(err)
	}
	scheduled, err := apps.ScheduledPTEOperations(ctx)
	if err != nil || len(scheduled) != 1 || scheduled[0].ScheduleKind != "NextMajorUpdate" || scheduled[0].SourceAppVersion != "" ||
		scheduled[0].Parameters["name"] != "Contoso Customizations" || scheduled[0].Parameters["syncMode"] != "Add" {
		t.Fatalf("ScheduledPTEOperations() = %+v, %v", scheduled, err)
	}
	removed, err := apps.RemoveScheduledPTEVersion(ctx, pteID, bcadmin.RemoveScheduledPTEVersionOptions{TargetVersion: "2.0.0.0", ScheduleKind: bcadmin.DeploymentNextMajorUpdate})
	if err != nil || !removed.Status.Is(bcadmin.OperationStatusCanceled) {
		t.Errorf("RemoveScheduledPTEVersion() = %+v, %v", removed, err)
	}
	if scheduled, _ := apps.ScheduledPTEOperations(ctx); len(scheduled) != 0 {
		t.Errorf("scheduled after remove = %+v", scheduled)
	}

	// Scheduled for the update window, then run.
	opts.DeploymentSchedule = bcadmin.DeploymentUpdateWindow
	p, err = apps.InstallPTE(ctx, file(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(ctx, &bcadmin.WaitOptions{TreatScheduledAsDone: true}); !errors.Is(err, bcadmin.ErrOperationScheduled) {
		t.Errorf("Wait(TreatScheduledAsDone) = %v", err)
	}
	fake.RunScheduled(bcadmin.FamilyBusinessCentral, "sb")
	if _, err := p.Wait(ctx, nil); err != nil {
		t.Fatal(err)
	}
	got := installed(t, fake)
	if got[pteID].Version != "2.0.0.0" || got[baseID].Version != "1.5.0.0" {
		t.Errorf("after scheduled PTE update = %+v", got)
	}
	if ops := fake.Operations(bcadmin.FamilyBusinessCentral, "sb"); len(ops) != 3 {
		t.Errorf("Operations() = %d, want 3", len(ops))
	}
}

// TestAppsFakeDependencyResolution checks a diamond, where a second path
// requires a newer version of an installed app, and a cycle.
func TestAppsFakeDependencyResolution(t *testing.T) {
	t.Parallel()
	const (
		xID = "00000000-0000-0000-0000-000000000211"
		aID = "00000000-0000-0000-0000-000000000212"
		bID = "00000000-0000-0000-0000-000000000213"
	)
	setup := func(t *testing.T, latestA string) (*bcadmintest.Apps, *bcadmin.AppsService) {
		t.Helper()
		fake, apps := newAppsFake(t)
		fake.Publish(bcadmintest.App{AppID: aID, Name: "A", Version: "1.0.0.0"})
		fake.Publish(bcadmintest.App{AppID: aID, Name: "A", Version: latestA})
		fake.Publish(bcadmintest.App{AppID: bID, Name: "B", Version: "1.0.0.0", Dependencies: []bcadmintest.Dependency{{AppID: aID, MinVersion: "1.5.0.0"}}})
		fake.Publish(bcadmintest.App{AppID: xID, Name: "X", Version: "1.0.0.0", Dependencies: []bcadmintest.Dependency{
			{AppID: aID, MinVersion: "1.0.0.0"}, {AppID: bID, MinVersion: "1.0.0.0"},
		}})
		fake.Install(bcadmin.FamilyBusinessCentral, "sb", bcadmin.InstalledApp{AppID: aID, Name: "A", Version: "1.0.0.0"})
		return fake, apps
	}
	ctx := context.Background()

	t.Run("diamond", func(t *testing.T) {
		t.Parallel()
		fake, apps := setup(t, "1.5.0.0")
		_, err := apps.Install(ctx, xID, bcadmin.InstallAppOptions{AcceptISVEULA: true})
		var re *bcadmin.ResponseError
		if !errors.As(err, &re) || re.Data == nil {
			t.Fatalf("install without dependencies: %v", err)
		}
		got := map[string]string{}
		for _, r := range re.Data.Requirements {
			got[r.AppID] = r.Type + " " + r.Version
		}
		if len(got) != 2 || got[bID] != "Install 1.0.0.0" || got[aID] != "Update 1.5.0.0" {
			t.Errorf("requirements = %v", got)
		}
		p, err := apps.Install(ctx, xID, bcadmin.InstallAppOptions{AcceptISVEULA: true, InstallOrUpdateNeededDependencies: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Wait(ctx, nil); err != nil {
			t.Fatal(err)
		}
		inst := installed(t, fake)
		if inst[aID].Version != "1.5.0.0" || inst[bID].Version != "1.0.0.0" || inst[xID].Version != "1.0.0.0" {
			t.Errorf("installed = %+v", inst)
		}
	})
	t.Run("unsatisfiable", func(t *testing.T) {
		t.Parallel()
		_, apps := setup(t, "1.2.0.0")
		_, err := apps.Install(ctx, xID, bcadmin.InstallAppOptions{AcceptISVEULA: true, InstallOrUpdateNeededDependencies: true})
		var re *bcadmin.ResponseError
		if !errors.As(err, &re) || re.Data == nil || !slices.ContainsFunc(re.Data.Requirements, func(r bcadmin.AppRequirement) bool {
			return r.AppID == aID && r.Version == "1.5.0.0"
		}) {
			t.Errorf("install with unsatisfiable dependency: %v", err)
		}
	})
	t.Run("cycle", func(t *testing.T) {
		t.Parallel()
		fake, apps := newAppsFake(t)
		fake.Publish(bcadmintest.App{AppID: aID, Name: "A", Version: "1.0.0.0", Dependencies: []bcadmintest.Dependency{{AppID: bID, MinVersion: "1.0.0.0"}}})
		fake.Publish(bcadmintest.App{AppID: bID, Name: "B", Version: "1.0.0.0", Dependencies: []bcadmintest.Dependency{{AppID: aID, MinVersion: "1.0.0.0"}}})
		p, err := apps.Install(ctx, aID, bcadmin.InstallAppOptions{AcceptISVEULA: true, InstallOrUpdateNeededDependencies: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Wait(ctx, nil); err != nil {
			t.Fatal(err)
		}
		if n := len(installed(t, fake)); n != 2 {
			t.Errorf("installed %d apps, want 2", n)
		}
	})
}

// TestAppsFakeScheduledNoDowngrade checks that a scheduled operation that
// runs later does not downgrade a dependency updated meanwhile.
func TestAppsFakeScheduledNoDowngrade(t *testing.T) {
	t.Parallel()
	fake, apps := newAppsFake(t)
	fake.Install(bcadmin.FamilyBusinessCentral, "sb", bcadmin.InstalledApp{AppID: baseID, Name: "Contoso Base", Version: "1.0.0.0"})
	fake.Install(bcadmin.FamilyBusinessCentral, "sb", bcadmin.InstalledApp{AppID: connectorID, Name: "Contoso Connector", Version: "2.0.0.0"})
	ctx := context.Background()
	scheduled, err := apps.Update(ctx, connectorID, bcadmin.UpdateAppOptions{TargetVersion: "2.2.0.0", UseEnvironmentUpdateWindow: true, InstallOrUpdateNeededDependencies: true})
	if err != nil {
		t.Fatal(err)
	}
	fake.Publish(bcadmintest.App{AppID: baseID, Name: "Contoso Base", Version: "3.0.0.0"})
	p, err := apps.Update(ctx, baseID, bcadmin.UpdateAppOptions{TargetVersion: "3.0.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(ctx, nil); err != nil {
		t.Fatal(err)
	}
	fake.RunScheduled(bcadmin.FamilyBusinessCentral, "sb")
	if _, err := scheduled.Wait(ctx, nil); err != nil {
		t.Fatal(err)
	}
	inst := installed(t, fake)
	if inst[baseID].Version != "3.0.0.0" || inst[connectorID].Version != "2.2.0.0" {
		t.Errorf("installed = %+v", inst)
	}
}

// TestAppsFakeImmediateIsRunning checks that immediate operations start
// Running, so TreatScheduledAsDone waits for them.
func TestAppsFakeImmediateIsRunning(t *testing.T) {
	t.Parallel()
	fake, apps := newAppsFake(t)
	fake.AddPTEFile("p.app", bcadmintest.App{AppID: pteID, Name: "P", Version: "1.0.0.0"})
	ctx := context.Background()
	wait := &bcadmin.WaitOptions{TreatScheduledAsDone: true}
	p, err := apps.InstallPTE(ctx, bytes.NewReader([]byte("app")), bcadmin.InstallPTEOptions{FileName: "p.app", AcceptISVEULA: true})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Operation().Status.Is(bcadmin.OperationStatusRunning) || p.Operation().StartedOn == nil {
		t.Errorf("immediate PTE operation = %+v", p.Operation())
	}
	if _, err := p.Wait(ctx, wait); err != nil {
		t.Errorf("Wait(TreatScheduledAsDone) on an immediate PTE install = %v", err)
	}
	p, err = apps.Install(ctx, baseID, bcadmin.InstallAppOptions{AcceptISVEULA: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(ctx, wait); err != nil {
		t.Errorf("Wait(TreatScheduledAsDone) on an immediate install = %v", err)
	}
	if n := len(installed(t, fake)); n != 2 {
		t.Errorf("installed %d apps, want 2", n)
	}
}

// TestAppsFakeScheduledPTEVersions checks that several scheduled versions
// of a PTE can wait, that uninstalling the PTE keeps them, and the
// validation of PTE uploads.
func TestAppsFakeScheduledPTEVersions(t *testing.T) {
	t.Parallel()
	fake, apps, srv := newAppsServer(t)
	for _, v := range []string{"1.0.0.0", "1.1.0.0", "2.0.0.0"} {
		fake.AddPTEFile("p_"+v+".app", bcadmintest.App{AppID: pteID, Name: "P", Version: v})
	}
	fake.Install(bcadmin.FamilyBusinessCentral, "sb", bcadmin.InstalledApp{AppID: pteID, Name: "P", Version: "1.0.0.0", AppType: bcadmin.AppTypeTenant, CanBeUninstalled: true})
	ctx := context.Background()
	upload := func(version string, schedule bcadmin.DeploymentSchedule) error {
		_, err := apps.InstallPTE(ctx, bytes.NewReader([]byte("app")), bcadmin.InstallPTEOptions{
			FileName: "p_" + version + ".app", AcceptISVEULA: true, DeploymentSchedule: schedule,
		})
		return err
	}
	if err := upload("2.0.0.0", bcadmin.DeploymentNextMajorUpdate); err != nil {
		t.Fatal(err)
	}
	if err := upload("1.1.0.0", bcadmin.DeploymentNextMinorUpdate); err != nil {
		t.Errorf("second scheduled version: %v", err)
	}
	if err := upload("2.0.0.0", bcadmin.DeploymentNextMajorUpdate); bcadmin.ErrorCode(err) != "appOperationInProgress" {
		t.Errorf("duplicate scheduled version: %v", err)
	}
	if err := upload("2.0.0.0", "Tomorrow"); bcadmin.ErrorCode(err) != "invalidInput" {
		t.Errorf("unknown deployment schedule: %v", err)
	}
	p, err := apps.Uninstall(ctx, pteID, bcadmin.UninstallAppOptions{})
	if err != nil {
		t.Fatalf("uninstall with scheduled versions: %v", err)
	}
	if _, err := p.Wait(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if scheduled, err := apps.ScheduledPTEOperations(ctx); err != nil || len(scheduled) != 2 {
		t.Errorf("scheduled after uninstall = %+v, %v", scheduled, err)
	}
	_, err = apps.RemoveScheduledPTEVersion(ctx, pteID, bcadmin.RemoveScheduledPTEVersionOptions{TargetVersion: "9.0.0.0", ScheduleKind: bcadmin.DeploymentUpdateWindow})
	if !bcadmin.IsNotFound(err) || bcadmin.ErrorCode(err) != "ResourceDoesNotExist" {
		t.Errorf("remove unknown scheduled version: %v", err)
	}

	// A failing PTE install leaves the apps unchanged.
	fake.FailNextOperation("The extension failed to install.")
	p, err = apps.InstallPTE(ctx, bytes.NewReader([]byte("app")), bcadmin.InstallPTEOptions{FileName: "p_1.0.0.0.app", AcceptISVEULA: true})
	if err != nil {
		t.Fatal(err)
	}
	var oe *bcadmin.OperationError
	if _, err := p.Wait(ctx, nil); !errors.As(err, &oe) {
		t.Errorf("failing PTE install: %v", err)
	}
	if n := len(installed(t, fake)); n != 0 {
		t.Errorf("installed %d apps after failed install, want 0", n)
	}

	// An extensionFile part without a file name.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("acceptIsvEula", "true")
	part, _ := mw.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="extensionFile"`}})
	_, _ = part.Write([]byte("app"))
	_ = mw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL()+"/admin/"+srv.APIVersion()+"/applications/BusinessCentral/environments/sb/apps/pteInstall", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+bcadmintest.Token)
	resp, err := srv.ClientOptions().Transport.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(msg), "extensionFile is required") {
		t.Errorf("upload without file name: %d %s", resp.StatusCode, msg)
	}
}

// TestAppsFakePTEDowngrade checks that the fake never installs an older PTE
// version over a newer one.
func TestAppsFakePTEDowngrade(t *testing.T) {
	t.Parallel()
	fake, apps := newAppsFake(t)
	for _, v := range []string{"1.0.0.0", "1.1.0.0", "2.0.0.0"} {
		fake.AddPTEFile("p_"+v+".app", bcadmintest.App{AppID: pteID, Name: "P", Version: v})
	}
	ctx := context.Background()
	upload := func(version string, schedule bcadmin.DeploymentSchedule) (*bcadmin.Poller[*bcadmin.Operation], error) {
		return apps.InstallPTE(ctx, bytes.NewReader([]byte("app")), bcadmin.InstallPTEOptions{
			FileName: "p_" + version + ".app", AcceptISVEULA: true, DeploymentSchedule: schedule,
		})
	}
	p, err := upload("1.0.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(ctx, nil); err != nil {
		t.Fatal(err)
	}
	scheduled, err := upload("1.1.0.0", bcadmin.DeploymentUpdateWindow)
	if err != nil {
		t.Fatal(err)
	}
	p, err = upload("2.0.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := upload("1.0.0.0", ""); bcadmin.ErrorCode(err) != "invalidTargetVersion" {
		t.Errorf("upload of an older version: %v", err)
	}
	fake.RunScheduled(bcadmin.FamilyBusinessCentral, "sb")
	op, err := scheduled.Wait(ctx, nil)
	if err != nil || !op.Status.Is(bcadmin.OperationStatusSkipped) {
		t.Errorf("scheduled older version = %+v, %v", op, err)
	}
	if v := installed(t, fake)[pteID].Version; v != "2.0.0.0" {
		t.Errorf("installed version = %s, want 2.0.0.0", v)
	}
}

// TestAppsFakeScheduledRecheck checks that scheduled operations check the
// dependencies again when they run.
func TestAppsFakeScheduledRecheck(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := bcadmin.InstalledApp{AppID: baseID, Name: "Contoso Base", Version: "1.5.0.0", CanBeUninstalled: true}

	t.Run("dependency uninstalled meanwhile", func(t *testing.T) {
		t.Parallel()
		for _, handle := range []bool{false, true} {
			fake, apps := newAppsFake(t)
			fake.Install(bcadmin.FamilyBusinessCentral, "sb", base)
			p, err := apps.Install(ctx, connectorID, bcadmin.InstallAppOptions{AcceptISVEULA: true, UseEnvironmentUpdateWindow: true, InstallOrUpdateNeededDependencies: handle})
			if err != nil {
				t.Fatal(err)
			}
			u, err := apps.Uninstall(ctx, baseID, bcadmin.UninstallAppOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := u.Wait(ctx, nil); err != nil {
				t.Fatal(err)
			}
			fake.RunScheduled(bcadmin.FamilyBusinessCentral, "sb")
			_, err = p.Wait(ctx, nil)
			got := installed(t, fake)
			if handle {
				if err != nil || got[connectorID].Version != "2.2.0.0" || got[baseID].Version != "1.5.0.0" {
					t.Errorf("with dependencies: %v, installed = %+v", err, got)
				}
				continue
			}
			var oe *bcadmin.OperationError
			if !errors.As(err, &oe) || !strings.Contains(oe.Operation.ErrorMessage, baseID) || len(got) != 0 {
				t.Errorf("without dependencies: %v, installed = %+v", err, got)
			}
		}
	})
	t.Run("dependent installed meanwhile", func(t *testing.T) {
		t.Parallel()
		fake, apps := newAppsFake(t)
		fake.Install(bcadmin.FamilyBusinessCentral, "sb", base)
		u, err := apps.Uninstall(ctx, baseID, bcadmin.UninstallAppOptions{UseEnvironmentUpdateWindow: true})
		if err != nil {
			t.Fatal(err)
		}
		p, err := apps.Install(ctx, connectorID, bcadmin.InstallAppOptions{AcceptISVEULA: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Wait(ctx, nil); err != nil {
			t.Fatal(err)
		}
		fake.RunScheduled(bcadmin.FamilyBusinessCentral, "sb")
		var oe *bcadmin.OperationError
		if _, err := u.Wait(ctx, nil); !errors.As(err, &oe) || !strings.Contains(oe.Operation.ErrorMessage, connectorID) {
			t.Errorf("scheduled uninstall with a new dependent: %v", err)
		}
		if got := installed(t, fake); len(got) != 2 {
			t.Errorf("installed = %+v", got)
		}
	})
}

// TestAppsFakeVersionMatching checks that versions match numerically and
// that an app depending on itself installs.
func TestAppsFakeVersionMatching(t *testing.T) {
	t.Parallel()
	fake, apps := newAppsFake(t)
	fake.AddPTEFile("p.app", bcadmintest.App{AppID: pteID, Name: "P", Version: "2.0.0.0"})
	fake.Install(bcadmin.FamilyBusinessCentral, "sb", bcadmin.InstalledApp{AppID: pteID, Name: "P", Version: "1.0", AppType: bcadmin.AppTypeTenant})
	ctx := context.Background()
	if _, err := apps.InstallPTE(ctx, bytes.NewReader([]byte("app")), bcadmin.InstallPTEOptions{
		FileName: "p.app", AcceptISVEULA: true, DeploymentSchedule: bcadmin.DeploymentNextMajorUpdate,
	}); err != nil {
		t.Fatal(err)
	}
	op, err := apps.RemoveScheduledPTEVersion(ctx, pteID, bcadmin.RemoveScheduledPTEVersionOptions{TargetVersion: "2.0", ScheduleKind: bcadmin.DeploymentNextMajorUpdate})
	if err != nil || !op.Status.Is(bcadmin.OperationStatusCanceled) || op.ErrorMessage == "" {
		t.Errorf("RemoveScheduledPTEVersion(2.0) = %+v, %v", op, err)
	}

	const selfID = "00000000-0000-0000-0000-000000000221"
	fake.Publish(bcadmintest.App{AppID: selfID, Name: "Self", Version: "1.0.0.0", Dependencies: []bcadmintest.Dependency{{AppID: selfID, MinVersion: "2.0.0.0"}}})
	p, err := apps.Install(ctx, selfID, bcadmin.InstallAppOptions{AcceptISVEULA: true})
	if err != nil {
		t.Fatalf("install of an app that depends on itself: %v", err)
	}
	if _, err := p.Wait(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := installed(t, fake)[selfID]; !ok {
		t.Error("an app that depends on itself was not installed")
	}
}

// TestAppsFakeUninstallInteractions checks uninstalls that interact with
// other operations and protected apps.
func TestAppsFakeUninstallInteractions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	seed := func(t *testing.T) (*bcadmintest.Apps, *bcadmin.AppsService) {
		t.Helper()
		fake, apps := newAppsFake(t)
		fake.Install(bcadmin.FamilyBusinessCentral, "sb", bcadmin.InstalledApp{AppID: baseID, Name: "Contoso Base", Version: "1.0.0.0", CanBeUninstalled: true})
		fake.Install(bcadmin.FamilyBusinessCentral, "sb", bcadmin.InstalledApp{AppID: connectorID, Name: "Contoso Connector", Version: "2.0.0.0", CanBeUninstalled: true})
		return fake, apps
	}

	t.Run("dependent with a pending update", func(t *testing.T) {
		t.Parallel()
		fake, apps := seed(t)
		if _, err := apps.Update(ctx, connectorID, bcadmin.UpdateAppOptions{TargetVersion: "2.2.0.0", UseEnvironmentUpdateWindow: true, InstallOrUpdateNeededDependencies: true}); err != nil {
			t.Fatal(err)
		}
		// Removing the connector as a dependent would leave its update
		// without an app; the fake refuses, as for the app itself.
		if _, err := apps.Uninstall(ctx, baseID, bcadmin.UninstallAppOptions{UninstallDependents: true}); bcadmin.ErrorCode(err) != "appOperationInProgress" {
			t.Errorf("uninstall with a busy dependent: %v", err)
		}
		if n := len(installed(t, fake)); n != 2 {
			t.Errorf("installed %d apps, want 2", n)
		}
	})
	t.Run("scheduled uninstall whose dependent became busy", func(t *testing.T) {
		t.Parallel()
		fake, apps := seed(t)
		u, err := apps.Uninstall(ctx, baseID, bcadmin.UninstallAppOptions{UninstallDependents: true, UseEnvironmentUpdateWindow: true})
		if err != nil {
			t.Fatal(err)
		}
		p, err := apps.Update(ctx, connectorID, bcadmin.UpdateAppOptions{TargetVersion: "2.2.0.0", UseEnvironmentUpdateWindow: true, InstallOrUpdateNeededDependencies: true})
		if err != nil {
			t.Fatal(err)
		}
		fake.RunScheduled(bcadmin.FamilyBusinessCentral, "sb")
		// The uninstall finishes first and fails: its dependent, the
		// connector, has its own operation in progress.
		var oe *bcadmin.OperationError
		if _, err := u.Wait(ctx, nil); !errors.As(err, &oe) || !strings.Contains(oe.Operation.ErrorMessage, "in progress") {
			t.Errorf("scheduled uninstall with a busy dependent: %v", err)
		}
		if _, err := p.Wait(ctx, nil); err != nil {
			t.Fatal(err)
		}
		got := installed(t, fake)
		if got[connectorID].Version != "2.2.0.0" || got[baseID].Version != "1.5.0.0" {
			t.Errorf("installed = %+v", got)
		}
	})
	t.Run("protected dependent", func(t *testing.T) {
		t.Parallel()
		fake, apps := newAppsFake(t)
		fake.Install(bcadmin.FamilyBusinessCentral, "sb", bcadmin.InstalledApp{AppID: baseID, Name: "Contoso Base", Version: "1.0.0.0", CanBeUninstalled: true})
		fake.Install(bcadmin.FamilyBusinessCentral, "sb", bcadmin.InstalledApp{AppID: connectorID, Name: "Contoso Connector", Version: "2.0.0.0"})
		if _, err := apps.Uninstall(ctx, baseID, bcadmin.UninstallAppOptions{UninstallDependents: true}); bcadmin.ErrorCode(err) != "appCannotBeUninstalled" {
			t.Errorf("uninstall with a protected dependent: %v", err)
		}
		if n := len(installed(t, fake)); n != 2 {
			t.Errorf("installed %d apps, want 2", n)
		}
	})
}

// TestAppsFakeResults checks the result fields of skipped operations and of
// apps changed as dependencies.
func TestAppsFakeResults(t *testing.T) {
	t.Parallel()
	fake, apps := newAppsFake(t)
	fake.Install(bcadmin.FamilyBusinessCentral, "sb", bcadmin.InstalledApp{AppID: baseID, Name: "Contoso Base", Version: "1.0.0.0"})
	fake.Install(bcadmin.FamilyBusinessCentral, "sb", bcadmin.InstalledApp{AppID: connectorID, Name: "Contoso Connector", Version: "2.0.0.0"})
	ctx := context.Background()
	baseUpdate, err := apps.Update(ctx, baseID, bcadmin.UpdateAppOptions{TargetVersion: "1.5.0.0", UseEnvironmentUpdateWindow: true})
	if err != nil {
		t.Fatal(err)
	}
	p, err := apps.Update(ctx, connectorID, bcadmin.UpdateAppOptions{TargetVersion: "2.2.0.0", InstallOrUpdateNeededDependencies: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(ctx, nil); err != nil {
		t.Fatal(err)
	}
	base := installed(t, fake)[baseID]
	if base.Version != "1.5.0.0" || !base.State.Is(bcadmin.AppStateUpdatePending) || base.LastOperationID == p.ID() {
		t.Errorf("base after a dependency update = %+v", base)
	}
	fake.RunScheduled(bcadmin.FamilyBusinessCentral, "sb")
	op, err := baseUpdate.Wait(ctx, nil)
	if err != nil || !op.Status.Is(bcadmin.OperationStatusSkipped) {
		t.Fatalf("scheduled base update = %+v, %v", op, err)
	}
	base = installed(t, fake)[baseID]
	if base.LastOperationID != op.ID || base.LastUpdateAttemptResult != "Skipped" || !base.State.Is(bcadmin.AppStateInstalled) {
		t.Errorf("base after skipped update = %+v", base)
	}
}

// TestAppsFakeRemoveScheduledKeepsResult checks that removing a stale
// scheduled PTE version does not change the installed app's result, and the
// type of dev dependents.
func TestAppsFakeRemoveScheduledKeepsResult(t *testing.T) {
	t.Parallel()
	fake, apps := newAppsFake(t)
	for _, v := range []string{"1.0.0.0", "2.0.0.0", "3.0.0.0"} {
		fake.AddPTEFile("p_"+v+".app", bcadmintest.App{AppID: pteID, Name: "P", Version: v})
	}
	ctx := context.Background()
	upload := func(version string, schedule bcadmin.DeploymentSchedule) *bcadmin.Poller[*bcadmin.Operation] {
		t.Helper()
		p, err := apps.InstallPTE(ctx, bytes.NewReader([]byte("app")), bcadmin.InstallPTEOptions{
			FileName: "p_" + version + ".app", AcceptISVEULA: true, DeploymentSchedule: schedule,
		})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := upload("1.0.0.0", "").Wait(ctx, nil); err != nil {
		t.Fatal(err)
	}
	upload("2.0.0.0", bcadmin.DeploymentNextMajorUpdate)
	last := upload("3.0.0.0", "")
	if _, err := last.Wait(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := apps.RemoveScheduledPTEVersion(ctx, pteID, bcadmin.RemoveScheduledPTEVersionOptions{TargetVersion: "2.0.0.0", ScheduleKind: bcadmin.DeploymentNextMajorUpdate}); err != nil {
		t.Fatal(err)
	}
	a := installed(t, fake)[pteID]
	if a.Version != "3.0.0.0" || a.LastOperationID != last.ID() || a.LastUpdateAttemptResult != "Succeeded" {
		t.Errorf("after removing a stale scheduled version = %+v", a)
	}

	const devID = "00000000-0000-0000-0000-000000000231"
	fake.Install(bcadmin.FamilyBusinessCentral, "sb", bcadmin.InstalledApp{AppID: baseID, Name: "Contoso Base", Version: "1.0.0.0", CanBeUninstalled: true})
	fake.AddPTEFile("dev.app", bcadmintest.App{AppID: devID, Name: "Dev", Version: "1.0.0.0", Dependencies: []bcadmintest.Dependency{{AppID: baseID, MinVersion: "1.0.0.0"}}})
	fake.Install(bcadmin.FamilyBusinessCentral, "sb", bcadmin.InstalledApp{AppID: devID, Name: "Dev", Version: "1.0.0.0", AppType: bcadmin.AppTypeDev})
	reqs, err := apps.UninstallRequirements(ctx, baseID)
	if err != nil || len(reqs) != 1 || reqs[0].Type != "DEV" {
		t.Errorf("UninstallRequirements() = %+v, %v", reqs, err)
	}
}
