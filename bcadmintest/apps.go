package bcadmintest

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/axiansinfoma/go-bcadmin"
)

// Apps is an in-memory fake of the app management and per-tenant extension
// endpoints, created with [Server.EnableApps]. It keeps a catalog of
// published global app versions ([Apps.Publish]) and the installed apps of
// every environment ([Apps.Install]); environments need not exist in an
// [Environments] fake.
//
// Install, update and uninstall operations started through the API finish
// after SetPollsUntilDone requests of the single operation
// (GET apps/{appId}/operations/{id}) and then change the installed apps.
// Operations started with useEnvironmentUpdateWindow, and PTE uploads with a
// deployment schedule other than Immediate, stay Scheduled until
// [Apps.RunScheduled]; they can be canceled meanwhile (update/cancel,
// removeScheduledPteVersion).
//
// Operations carry both version pairs (sourceAppVersion/targetAppVersion and
// sourceVersion/targetVersion), as the different app endpoints return one or
// the other. Whether the service lists the operation of a PTE that is
// scheduled but not yet installed under apps/{appId}/operations is not
// documented; the fake does.
//
// Like the service, the fake requires acceptIsvEula for installs, refuses
// missing dependencies (and dependents on uninstall) with code
// EntityValidationFailed and the apps in data.requirements unless the
// request asks to handle them, and allows one running operation per app.
// Where the documentation names no error code, it uses appNotFound (404),
// appNotInstalled (404), appAlreadyInstalled (409), appOperationInProgress
// (409), versionNotFound (400), invalidTargetVersion (400), eulaNotAccepted
// (400), appCannotBeUninstalled (400), operationNotScheduled (400) and
// invalidInput (400); invalid JSON bodies get requestBodyRequired (400). It is safe for concurrent use.
type Apps struct {
	mu        sync.Mutex
	catalog   map[string][]App                            // key: lower(appId); versions ascending
	installed map[string]map[string]*bcadmin.InstalledApp // key: envKey, lower(appId)
	ptes      map[string]App                              // key: lower(file name)
	ops       []*appOperation
	polls     int
	failMsg   string
	nextID    int
}

// App is a version of an app the fake knows: a global app in the catalog
// ([Apps.Publish]) or the content of a per-tenant extension file
// ([Apps.AddPTEFile]).
type App struct {
	AppID     string
	Name      string
	Publisher string
	Version   string
	// Dependencies are the apps this version needs, with their minimum
	// versions.
	Dependencies []Dependency
}

// Dependency is an app another app needs, at least in MinVersion.
type Dependency struct {
	AppID      string
	MinVersion string
}

type appOperation struct {
	op     bcadmin.Operation
	env    string // envKey
	app    App    // the app version the operation installs, updates to or uninstalls
	kind   string // install, update, uninstall
	pte    bool
	held   bool // scheduled until RunScheduled
	fail   string
	polls  int
	extras []App // dependencies installed or updated, or dependents uninstalled, with the app
}

// EnableApps registers handlers for the app management endpoints
// (applications/{family}/environments/{env}/apps/...) and returns the store
// behind them. Call it once per server and do not register handlers for
// these paths yourself.
func (s *Server) EnableApps() *Apps {
	a := &Apps{
		catalog:   map[string][]App{},
		installed: map[string]map[string]*bcadmin.InstalledApp{},
		ptes:      map[string]App{},
		polls:     1,
	}
	const apps = "applications/{family}/environments/{env}/apps"
	s.Handle(http.MethodGet, apps, a.handleList)
	s.Handle(http.MethodGet, apps+"/availableUpdates", a.handleAvailableUpdates)
	s.Handle(http.MethodPost, apps+"/pteInstall", a.handlePTEInstall)
	s.Handle(http.MethodGet, apps+"/scheduledPteOperations", a.handleScheduledPTE)
	s.Handle(http.MethodPost, apps+"/{app}/install", a.handleInstall)
	s.Handle(http.MethodPost, apps+"/{app}/update", a.handleUpdate)
	s.Handle(http.MethodPost, apps+"/{app}/update/cancel", a.handleCancelUpdate)
	s.Handle(http.MethodPost, apps+"/{app}/uninstall", a.handleUninstall)
	s.Handle(http.MethodGet, apps+"/{app}/uninstallRequirements", a.handleUninstallRequirements)
	s.Handle(http.MethodGet, apps+"/{app}/operations", a.handleOperations)
	s.Handle(http.MethodGet, apps+"/{app}/operations/{id}", a.handleGetOperation)
	s.Handle(http.MethodPost, apps+"/{app}/removeScheduledPteVersion", a.handleRemoveScheduledPTE)
	return a
}

// SetPollsUntilDone sets how many requests of the single operation an
// operation needs to finish once it is not held as scheduled. The default
// is 1. Values below 1 are treated as 1.
func (a *Apps) SetPollsUntilDone(n int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.polls = max(n, 1)
}

// FailNextOperation makes the next operation that is started end with status
// Failed and the given error message, without changing the installed apps.
func (a *Apps) FailNextOperation(message string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failMsg = message
}

// Publish adds a version of a global app to the catalog, replacing the same
// version if it was published before. Installs and updates can only use
// published versions.
func (a *Apps) Publish(app App) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := strings.ToLower(app.AppID)
	versions := slices.DeleteFunc(a.catalog[key], func(v App) bool { return v.Version == app.Version })
	versions = append(versions, cloneApp(app))
	slices.SortFunc(versions, func(x, y App) int { return compareVersions(x.Version, y.Version) })
	a.catalog[key] = versions
}

// AddPTEFile makes a pteInstall upload of a file with this name (the base
// name, compared case-insensitively) install app as a per-tenant extension.
// The fake does not read the file content. Uploads of unknown file names
// fail with invalidInput.
func (a *Apps) AddPTEFile(fileName string, app App) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ptes[strings.ToLower(fileName)] = cloneApp(app)
}

// Install stores app as installed in the environment family/environment,
// replacing an app with the same ID. Empty State defaults to Installed and
// empty AppType to global.
func (a *Apps) Install(family, environment string, app bcadmin.InstalledApp) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if app.State == "" {
		app.State = bcadmin.AppStateInstalled
	}
	if app.AppType == "" {
		app.AppType = bcadmin.AppTypeGlobal
	}
	a.envApps(envKey(family, environment))[strings.ToLower(app.AppID)] = &app
}

// Installed returns the apps installed in the environment family/environment,
// sorted by name.
func (a *Apps) Installed(family, environment string) []bcadmin.InstalledApp {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.list(envKey(family, environment))
}

// Operations returns copies of the operations started through the API in
// the environment family/environment, oldest first.
func (a *Apps) Operations(family, environment string) []bcadmin.Operation {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := envKey(family, environment)
	var out []bcadmin.Operation
	for _, o := range a.ops {
		if o.env == key {
			out = append(out, cloneOperation(o.op))
		}
	}
	return out
}

// RunScheduled releases the scheduled operations of the environment
// family/environment, as the update window or a platform update would: they
// then run and finish like other operations.
func (a *Apps) RunScheduled(family, environment string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := envKey(family, environment)
	for _, o := range a.ops {
		if o.env == key && o.held && o.op.Status.Is(bcadmin.OperationStatusScheduled) {
			o.held = false
		}
	}
}

func cloneApp(app App) App {
	app.Dependencies = slices.Clone(app.Dependencies)
	return app
}

// envApps returns the installed apps of the environment key, creating the
// map. a.mu must be held.
func (a *Apps) envApps(key string) map[string]*bcadmin.InstalledApp {
	m := a.installed[key]
	if m == nil {
		m = map[string]*bcadmin.InstalledApp{}
		a.installed[key] = m
	}
	return m
}

// list returns copies of the installed apps of the environment key. a.mu
// must be held.
func (a *Apps) list(key string) []bcadmin.InstalledApp {
	out := []bcadmin.InstalledApp{}
	for _, app := range a.installed[key] {
		out = append(out, *app)
	}
	slices.SortFunc(out, func(x, y bcadmin.InstalledApp) int { return strings.Compare(x.Name, y.Name) })
	return out
}

// published returns the catalog version of appID, the latest when version
// is empty. a.mu must be held.
func (a *Apps) published(appID, version string) (App, bool) {
	versions := a.catalog[strings.ToLower(appID)]
	if len(versions) == 0 {
		return App{}, false
	}
	if version == "" {
		return versions[len(versions)-1], true
	}
	for _, v := range versions {
		if v.Version == version {
			return v, true
		}
	}
	return App{}, false
}

// busy reports whether an operation on appID in the environment key is
// scheduled or running. a.mu must be held.
func (a *Apps) busy(key, appID string) bool {
	for _, o := range a.ops {
		if o.env == key && strings.EqualFold(o.app.AppID, appID) && !o.op.Status.Terminal() {
			return true
		}
	}
	return false
}

// missing returns the dependencies of app that are not installed in at
// least their minimum version, as requirements, and the catalog versions
// that would satisfy them. Dependencies of dependencies are included. It
// returns ok false with the requirement of a dependency that is not in the
// catalog. a.mu must be held.
func (a *Apps) missing(key string, app App) (reqs []bcadmin.AppRequirement, fix []App, ok bool) {
	seen := map[string]bool{}
	var walk func(App) bool
	walk = func(app App) bool {
		for _, d := range app.Dependencies {
			id := strings.ToLower(d.AppID)
			if seen[id] {
				continue
			}
			seen[id] = true
			inst := a.installed[key][id]
			if inst != nil && compareVersions(inst.Version, d.MinVersion) >= 0 {
				continue
			}
			latest, found := a.published(d.AppID, "")
			if !found || compareVersions(latest.Version, d.MinVersion) < 0 {
				reqs = append(reqs, bcadmin.AppRequirement{AppID: d.AppID, Version: d.MinVersion, Type: "Install"})
				return false
			}
			typ := "Install"
			if inst != nil {
				typ = "Update"
			}
			reqs = append(reqs, bcadmin.AppRequirement{AppID: latest.AppID, Name: latest.Name, Publisher: latest.Publisher, Version: latest.Version, Type: typ})
			fix = append(fix, latest)
			if !walk(latest) {
				return false
			}
		}
		return true
	}
	ok = walk(app)
	return reqs, fix, ok
}

// dependents returns the installed apps of the environment key that depend
// on appID, directly or indirectly. a.mu must be held.
func (a *Apps) dependents(key, appID string) []bcadmin.InstalledApp {
	var out []bcadmin.InstalledApp
	seen := map[string]bool{strings.ToLower(appID): true}
	queue := []string{appID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, inst := range a.list(key) {
			if seen[strings.ToLower(inst.AppID)] {
				continue
			}
			v, ok := a.published(inst.AppID, inst.Version)
			if !ok {
				v, ok = a.installedPTE(inst)
			}
			if !ok {
				continue
			}
			for _, d := range v.Dependencies {
				if strings.EqualFold(d.AppID, id) {
					seen[strings.ToLower(inst.AppID)] = true
					out = append(out, inst)
					queue = append(queue, inst.AppID)
					break
				}
			}
		}
	}
	return out
}

// installedPTE returns the PTE file content matching an installed app.
// a.mu must be held.
func (a *Apps) installedPTE(inst bcadmin.InstalledApp) (App, bool) {
	for _, p := range a.ptes {
		if strings.EqualFold(p.AppID, inst.AppID) && p.Version == inst.Version {
			return p, true
		}
	}
	return App{}, false
}

// requirementsError writes the EntityValidationFailed error with the apps
// in data.requirements.
func requirementsError(w http.ResponseWriter, message string, reqs []bcadmin.AppRequirement) {
	WriteJSON(w, http.StatusBadRequest, map[string]any{
		"code":    "EntityValidationFailed",
		"message": message,
		"data":    map[string]any{"requirements": reqs},
	})
}

// start records a new operation of the given kind (install, update,
// uninstall) and writes it. It is Scheduled and held when scheduleKind is
// not Immediate. a.mu must be held.
func (a *Apps) start(w http.ResponseWriter, r *http.Request, kind string, app App, source string, scheduleKind string, pte bool, extras []App) {
	a.nextID++
	now := time.Now().UTC()
	op := bcadmin.Operation{
		ID:               fmt.Sprintf("00000000-0000-0000-0002-%012d", a.nextID),
		Type:             bcadmin.OperationType(kind),
		Status:           bcadmin.OperationStatusScheduled,
		CreatedOn:        &bcadmin.Time{Time: now},
		AppID:            app.AppID,
		SourceAppVersion: source,
		TargetAppVersion: app.Version,
		SourceVersion:    source,
		TargetVersion:    app.Version,
		ScheduleKind:     scheduleKind,
		EnvironmentName:  r.PathValue("env"),
		ProductFamily:    r.PathValue("family"),
	}
	if kind == "uninstall" {
		op.TargetAppVersion, op.TargetVersion = "", ""
	}
	o := &appOperation{
		op: op, env: envKey(r.PathValue("family"), r.PathValue("env")), app: app, kind: kind,
		pte: pte, held: scheduleKind != "Immediate", extras: extras,
	}
	if a.failMsg != "" {
		o.fail, a.failMsg = a.failMsg, ""
	}
	if kind == "update" && !pte {
		if inst := a.installed[o.env][strings.ToLower(app.AppID)]; inst != nil {
			inst.State = bcadmin.AppStateUpdatePending
		}
	}
	a.ops = append(a.ops, o)
	WriteJSON(w, http.StatusOK, o.op)
}

// advance counts a request of the single operation o and finishes it when
// due. a.mu must be held.
func (a *Apps) advance(o *appOperation) {
	if o.op.Status.Terminal() || o.held {
		return
	}
	now := &bcadmin.Time{Time: time.Now().UTC()}
	if o.op.StartedOn == nil {
		o.op.StartedOn = now
	}
	o.polls++
	if o.polls < a.polls {
		o.op.Status = bcadmin.OperationStatusRunning
		if inst := a.installed[o.env][strings.ToLower(o.app.AppID)]; inst != nil && o.kind == "update" {
			inst.State = bcadmin.AppStateUpdating
		}
		return
	}
	o.op.CompletedOn = now
	apps := a.envApps(o.env)
	key := strings.ToLower(o.app.AppID)
	if o.fail != "" {
		o.op.Status = bcadmin.OperationStatusFailed
		o.op.ErrorMessage = o.fail
		if inst := apps[key]; inst != nil {
			inst.State = bcadmin.AppStateInstalled
			setResult(inst, o, "Failed")
		}
		return
	}
	o.op.Status = bcadmin.OperationStatusSucceeded
	switch o.kind {
	case "uninstall":
		for _, d := range o.extras {
			delete(apps, strings.ToLower(d.AppID))
		}
		delete(apps, key)
	default:
		for _, d := range o.extras {
			a.put(apps, d, false, o)
		}
		a.put(apps, o.app, o.pte, o)
	}
}

// put installs app in apps or updates it. a.mu must be held.
func (a *Apps) put(apps map[string]*bcadmin.InstalledApp, app App, pte bool, o *appOperation) {
	key := strings.ToLower(app.AppID)
	inst := apps[key]
	if inst == nil {
		typ := bcadmin.AppTypeGlobal
		if pte {
			typ = bcadmin.AppTypeTenant
		}
		inst = &bcadmin.InstalledApp{AppID: app.AppID, AppType: typ, CanBeUninstalled: true}
		apps[key] = inst
	}
	inst.Name, inst.Publisher, inst.Version = app.Name, app.Publisher, app.Version
	inst.State = bcadmin.AppStateInstalled
	setResult(inst, o, "Succeeded")
}

func setResult(inst *bcadmin.InstalledApp, o *appOperation, result string) {
	if o.kind == "uninstall" {
		inst.LastUninstallOperationID, inst.LastUninstallAttemptResult = o.op.ID, result
		return
	}
	inst.LastOperationID, inst.LastUpdateAttemptResult = o.op.ID, result
}

// findOperation returns the operation id of appID in the environment of the
// request. a.mu must be held.
func (a *Apps) findOperation(r *http.Request, id string) *appOperation {
	key := envKey(r.PathValue("family"), r.PathValue("env"))
	for _, o := range a.ops {
		if o.env == key && strings.EqualFold(o.app.AppID, r.PathValue("app")) && strings.EqualFold(o.op.ID, id) {
			return o
		}
	}
	return nil
}

func (a *Apps) handleList(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	WriteJSON(w, http.StatusOK, map[string]any{"value": a.list(envKey(r.PathValue("family"), r.PathValue("env")))})
}

func (a *Apps) handleAvailableUpdates(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := envKey(r.PathValue("family"), r.PathValue("env"))
	out := []bcadmin.AvailableAppUpdate{}
	for _, inst := range a.list(key) {
		if !inst.AppType.Is(bcadmin.AppTypeGlobal) {
			continue
		}
		latest, ok := a.published(inst.AppID, "")
		if !ok || compareVersions(latest.Version, inst.Version) <= 0 {
			continue
		}
		reqs, _, _ := a.missing(key, latest)
		out = append(out, bcadmin.AvailableAppUpdate{
			AppID: latest.AppID, Name: latest.Name, Publisher: latest.Publisher, Version: latest.Version, Requirements: reqs,
		})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func (a *Apps) handleInstall(w http.ResponseWriter, r *http.Request) {
	var body bcadmin.InstallAppOptions
	if !decode(w, r, &body) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := envKey(r.PathValue("family"), r.PathValue("env"))
	appID := r.PathValue("app")
	if !body.AcceptISVEULA {
		WriteError(w, http.StatusBadRequest, "eulaNotAccepted", "acceptIsvEula must be true to install an app")
		return
	}
	if _, ok := a.catalog[strings.ToLower(appID)]; !ok {
		WriteError(w, http.StatusNotFound, "appNotFound", "app "+appID+" was not found")
		return
	}
	if a.installed[key][strings.ToLower(appID)] != nil {
		WriteError(w, http.StatusConflict, "appAlreadyInstalled", "app "+appID+" is already installed")
		return
	}
	if a.busy(key, appID) {
		WriteError(w, http.StatusConflict, "appOperationInProgress", "another operation on app "+appID+" is in progress")
		return
	}
	app, ok := a.published(appID, body.TargetVersion)
	if !ok {
		WriteError(w, http.StatusBadRequest, "versionNotFound", "version "+body.TargetVersion+" of app "+appID+" was not found")
		return
	}
	reqs, fix, resolvable := a.missing(key, app)
	if len(reqs) > 0 && (!body.InstallOrUpdateNeededDependencies || !resolvable) {
		requirementsError(w, "The app cannot be installed because of missing dependencies.", reqs)
		return
	}
	a.start(w, r, "install", app, "", schedule(body.UseEnvironmentUpdateWindow), false, fix)
}

func (a *Apps) handleUpdate(w http.ResponseWriter, r *http.Request) {
	var body bcadmin.UpdateAppOptions
	if !decode(w, r, &body) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := envKey(r.PathValue("family"), r.PathValue("env"))
	appID := r.PathValue("app")
	inst := a.installed[key][strings.ToLower(appID)]
	if inst == nil {
		WriteError(w, http.StatusNotFound, "appNotInstalled", "app "+appID+" is not installed")
		return
	}
	if a.busy(key, appID) {
		WriteError(w, http.StatusConflict, "appOperationInProgress", "another operation on app "+appID+" is in progress")
		return
	}
	if body.TargetVersion == "" {
		WriteError(w, http.StatusBadRequest, "invalidInput", "targetVersion is required")
		return
	}
	app, ok := a.published(appID, body.TargetVersion)
	if !ok {
		WriteError(w, http.StatusBadRequest, "versionNotFound", "version "+body.TargetVersion+" of app "+appID+" was not found")
		return
	}
	if compareVersions(app.Version, inst.Version) <= 0 {
		WriteError(w, http.StatusBadRequest, "invalidTargetVersion", "version "+app.Version+" is not newer than the installed version "+inst.Version)
		return
	}
	reqs, fix, resolvable := a.missing(key, app)
	if len(reqs) > 0 && (!body.InstallOrUpdateNeededDependencies || !resolvable) {
		requirementsError(w, "The app cannot be updated because of missing dependencies.", reqs)
		return
	}
	a.start(w, r, "update", app, inst.Version, schedule(body.UseEnvironmentUpdateWindow), false, fix)
}

func (a *Apps) handleCancelUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ScheduledOperationID string `json:"ScheduledOperationId"`
	}
	if !decode(w, r, &body) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	o := a.findOperation(r, body.ScheduledOperationID)
	if o == nil || o.kind != "update" || o.pte {
		WriteError(w, http.StatusNotFound, "operationNotFound", "scheduled update "+body.ScheduledOperationID+" was not found")
		return
	}
	a.cancel(w, o)
}

// cancel cancels the scheduled operation o and writes it. a.mu must be held.
func (a *Apps) cancel(w http.ResponseWriter, o *appOperation) {
	if !o.held || !o.op.Status.Is(bcadmin.OperationStatusScheduled) {
		WriteError(w, http.StatusBadRequest, "operationNotScheduled", "operation "+o.op.ID+" is not scheduled")
		return
	}
	o.op.Status = bcadmin.OperationStatusCanceled
	o.op.CanceledBy = "bcadmintest"
	o.op.CompletedOn = &bcadmin.Time{Time: time.Now().UTC()}
	if inst := a.installed[o.env][strings.ToLower(o.app.AppID)]; inst != nil {
		inst.State = bcadmin.AppStateInstalled
	}
	WriteJSON(w, http.StatusOK, o.op)
}

func (a *Apps) handleUninstall(w http.ResponseWriter, r *http.Request) {
	var body bcadmin.UninstallAppOptions
	if !decode(w, r, &body) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := envKey(r.PathValue("family"), r.PathValue("env"))
	appID := r.PathValue("app")
	inst := a.installed[key][strings.ToLower(appID)]
	if inst == nil {
		WriteError(w, http.StatusNotFound, "appNotInstalled", "app "+appID+" is not installed")
		return
	}
	if !inst.CanBeUninstalled {
		WriteError(w, http.StatusBadRequest, "appCannotBeUninstalled", "app "+appID+" cannot be uninstalled")
		return
	}
	if a.busy(key, appID) {
		WriteError(w, http.StatusConflict, "appOperationInProgress", "another operation on app "+appID+" is in progress")
		return
	}
	deps := a.dependents(key, appID)
	if len(deps) > 0 && !body.UninstallDependents {
		reqs := make([]bcadmin.AppRequirement, 0, len(deps))
		for _, d := range deps {
			reqs = append(reqs, bcadmin.AppRequirement{AppID: d.AppID, Name: d.Name, Publisher: d.Publisher, Version: d.Version, Type: "Uninstall"})
		}
		requirementsError(w, "The app cannot be uninstalled because other apps depend on it.", reqs)
		return
	}
	extras := make([]App, 0, len(deps))
	for _, d := range deps {
		extras = append(extras, App{AppID: d.AppID, Name: d.Name, Publisher: d.Publisher, Version: d.Version})
	}
	app := App{AppID: inst.AppID, Name: inst.Name, Publisher: inst.Publisher, Version: inst.Version}
	a.start(w, r, "uninstall", app, inst.Version, schedule(body.UseEnvironmentUpdateWindow), inst.AppType.Is(bcadmin.AppTypeTenant), extras)
}

func (a *Apps) handleUninstallRequirements(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := envKey(r.PathValue("family"), r.PathValue("env"))
	appID := r.PathValue("app")
	if a.installed[key][strings.ToLower(appID)] == nil {
		WriteError(w, http.StatusNotFound, "appNotInstalled", "app "+appID+" is not installed")
		return
	}
	out := []bcadmin.UninstallRequirement{}
	for _, d := range a.dependents(key, appID) {
		typ := "Global"
		if d.AppType.Is(bcadmin.AppTypeTenant) {
			typ = "PTE"
		}
		out = append(out, bcadmin.UninstallRequirement{
			AppID: d.AppID, Name: d.Name, Publisher: d.Publisher, Version: d.Version, Type: typ, OperationRequirementType: "Uninstall",
		})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func (a *Apps) handleOperations(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := envKey(r.PathValue("family"), r.PathValue("env"))
	out := []bcadmin.Operation{}
	for _, o := range a.ops {
		if o.env == key && strings.EqualFold(o.app.AppID, r.PathValue("app")) {
			out = append(out, o.op)
		}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func (a *Apps) handleGetOperation(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	o := a.findOperation(r, r.PathValue("id"))
	if o == nil {
		WriteError(w, http.StatusNotFound, "operationNotFound", "operation "+r.PathValue("id")+" was not found")
		return
	}
	a.advance(o)
	WriteJSON(w, http.StatusOK, o.op)
}

func (a *Apps) handlePTEInstall(w http.ResponseWriter, r *http.Request) {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		WriteError(w, http.StatusBadRequest, "invalidInput", "the request must be multipart/form-data")
		return
	}
	fields := map[string]string{}
	var fileName string
	mr := multipart.NewReader(r.Body, params["boundary"])
	for {
		p, err := mr.NextPart()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				WriteError(w, http.StatusBadRequest, "invalidInput", "invalid multipart body: "+err.Error())
				return
			}
			break
		}
		if p.FormName() == "extensionFile" {
			fileName = filepath.Base(p.FileName())
			continue
		}
		v, err := io.ReadAll(io.LimitReader(p, 1<<16))
		if err != nil {
			WriteError(w, http.StatusBadRequest, "invalidInput", "invalid multipart body: "+err.Error())
			return
		}
		fields[p.FormName()] = string(v)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if fileName == "" {
		WriteError(w, http.StatusBadRequest, "invalidInput", "extensionFile is required")
		return
	}
	if accept, _ := strconv.ParseBool(fields["acceptIsvEula"]); !accept {
		WriteError(w, http.StatusBadRequest, "eulaNotAccepted", "acceptIsvEula must be true to install an app")
		return
	}
	app, ok := a.ptes[strings.ToLower(fileName)]
	if !ok {
		WriteError(w, http.StatusBadRequest, "invalidInput", "the fake knows no app file "+fileName+"; register it with AddPTEFile")
		return
	}
	key := envKey(r.PathValue("family"), r.PathValue("env"))
	if a.busy(key, app.AppID) {
		WriteError(w, http.StatusConflict, "appOperationInProgress", "another operation on app "+app.AppID+" is in progress")
		return
	}
	scheduleKind := fields["deploymentSchedule"]
	if scheduleKind == "" {
		scheduleKind = "Immediate"
	}
	source := ""
	if inst := a.installed[key][strings.ToLower(app.AppID)]; inst != nil {
		source = inst.Version
	} else if scheduleKind != "Immediate" && scheduleKind != "UpdateWindow" {
		WriteError(w, http.StatusBadRequest, "invalidInput", "a new per-tenant extension can only use Immediate or UpdateWindow")
		return
	}
	needDeps, _ := strconv.ParseBool(fields["installOrUpdateNeededDependencies"])
	reqs, fix, resolvable := a.missing(key, app)
	if len(reqs) > 0 && (!needDeps || !resolvable) {
		requirementsError(w, "The app cannot be installed because of missing dependencies.", reqs)
		return
	}
	a.start(w, r, "install", app, source, scheduleKind, true, fix)
}

func (a *Apps) handleScheduledPTE(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := envKey(r.PathValue("family"), r.PathValue("env"))
	out := []bcadmin.Operation{}
	for _, o := range a.ops {
		if o.env == key && o.pte && o.kind == "install" && o.held && o.op.Status.Is(bcadmin.OperationStatusScheduled) {
			out = append(out, o.op)
		}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func (a *Apps) handleRemoveScheduledPTE(w http.ResponseWriter, r *http.Request) {
	var body bcadmin.RemoveScheduledPTEVersionOptions
	if !decode(w, r, &body) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := envKey(r.PathValue("family"), r.PathValue("env"))
	for _, o := range a.ops {
		if o.env == key && o.pte && o.kind == "install" && strings.EqualFold(o.app.AppID, r.PathValue("app")) &&
			o.app.Version == body.TargetVersion && strings.EqualFold(o.op.ScheduleKind, string(body.ScheduleKind)) &&
			o.held && o.op.Status.Is(bcadmin.OperationStatusScheduled) {
			a.cancel(w, o)
			return
		}
	}
	WriteError(w, http.StatusNotFound, "operationNotFound",
		"no scheduled installation of version "+body.TargetVersion+" ("+string(body.ScheduleKind)+") of app "+r.PathValue("app"))
}

// schedule returns the schedule kind of an install, update or uninstall
// request.
func schedule(useUpdateWindow bool) string {
	if useUpdateWindow {
		return "UpdateWindow"
	}
	return "Immediate"
}

// compareVersions compares dotted numeric versions such as "1.2.0.0";
// missing parts count as 0 and non-numeric parts compare as strings.
func compareVersions(x, y string) int {
	xs, ys := strings.Split(x, "."), strings.Split(y, ".")
	for i := range max(len(xs), len(ys)) {
		var a, b string
		if i < len(xs) {
			a = xs[i]
		}
		if i < len(ys) {
			b = ys[i]
		}
		ai, aerr := strconv.Atoi(cmpZero(a))
		bi, berr := strconv.Atoi(cmpZero(b))
		if aerr == nil && berr == nil {
			if ai != bi {
				return ai - bi
			}
			continue
		}
		if c := strings.Compare(a, b); c != 0 {
			return c
		}
	}
	return 0
}

func cmpZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}
