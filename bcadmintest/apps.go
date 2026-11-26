package bcadmintest

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
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
// every environment ([Apps.AddInstalled]); environments need not exist in an
// [Environments] fake.
//
// Install, update and uninstall operations started through the API start
// Running and finish after SetPollsUntilDone requests of the single
// operation (GET apps/{appId}/operations/{id}); then they change the
// installed apps. Operations started with useEnvironmentUpdateWindow, and
// PTE uploads with a deployment schedule other than Immediate, start
// Scheduled and wait for [Apps.RunScheduled]; they can be canceled meanwhile
// (update/cancel, removeScheduledPteVersion). Several scheduled versions of
// a PTE can wait at once, and uninstalling a PTE keeps them, as documented.
// When an operation finishes, the fake checks the current state again, as
// other operations may have changed it while it was scheduled: an install
// or update whose app is already installed in the same or a newer version
// ends Skipped; one whose dependencies are no longer met installs them if
// the request asked for that (never downgrading an app) and fails
// otherwise; an uninstall fails if other apps now depend on the app and the
// request did not ask to uninstall dependents, and is Skipped if the app is
// gone; an update of an app that was uninstalled meanwhile fails. A PTE
// upload older than the installed version is refused. Uninstalling with
// uninstallDependents is refused (or fails when it runs) if a dependent is
// protected (CanBeUninstalled false) or has its own operation in progress.
// Apps installed or updated as dependencies of another operation keep their
// own lastOperationId and result, and their UpdatePending state while their
// own update is pending.
//
// Operations carry both version pairs (sourceAppVersion/targetAppVersion and
// sourceVersion/targetVersion), as the different app endpoints return one or
// the other; sourceAppVersion is empty for PTE installs, as documented. PTE
// operations have parameters with the app's name, publisher, schedule and
// sync mode. Whether the service lists the operation of a PTE that is
// scheduled but not yet installed under apps/{appId}/operations is not
// documented; the fake does.
//
// Like the service, the fake requires acceptIsvEula for installs, refuses
// missing dependencies (and dependents on uninstall) with status 400 and the
// apps in data.requirements unless the request asks to handle them, accepts one request per app at a time
// (installing or updating an app as a dependency of another operation is not
// blocked by the app's own pending operation), and
// answers removeScheduledPteVersion for an unknown entry with
// ResourceDoesNotExist (404). Where the documentation names no error code,
// it uses EntityValidationFailed (400, the requirements errors above),
// appNotFound (404), appNotInstalled (404), operationNotFound (404),
// appAlreadyInstalled (409), appOperationInProgress (409), versionNotFound
// (400), invalidTargetVersion (400), eulaNotAccepted (400),
// appCannotBeUninstalled (400), operationNotScheduled (400) and invalidInput
// (400, also for unknown deploymentSchedule and syncMode values); invalid
// JSON bodies get requestBodyRequired (400).
//
// It is safe for concurrent use.
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
	op    bcadmin.Operation
	env   string // envKey
	app   App    // the app version the operation installs, updates to or uninstalls
	kind  string // install, update, uninstall
	pte   bool
	held  bool // scheduled until RunScheduled
	fail  string
	polls int
	// handleDeps is installOrUpdateNeededDependencies (install, update, PTE)
	// or uninstallDependents (uninstall) of the request.
	handleDeps bool
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
// version (compared numerically, so "1.0" equals "1.0.0.0") if it was
// published before. Installs and updates can only use
// published versions.
func (a *Apps) Publish(app App) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := strings.ToLower(app.AppID)
	versions := slices.DeleteFunc(a.catalog[key], func(v App) bool { return compareVersions(v.Version, app.Version) == 0 })
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

// AddInstalled stores app as installed in the environment family/environment,
// replacing an app with the same ID. Empty State defaults to Installed and
// empty AppType to global. CanBeUninstalled is stored as given: set it to
// true for apps a test uninstalls. The fake knows the dependencies of a
// stored app only when its version is published ([Apps.Publish]) or a
// registered PTE file ([Apps.AddPTEFile]).
func (a *Apps) AddInstalled(family, environment string, app bcadmin.InstalledApp) {
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
// sorted by name and app ID.
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

// RunScheduled releases all scheduled operations of the environment
// family/environment (any schedule kind), as the update window or a platform
// update would. Released operations keep status Scheduled until their
// single operation is requested; then they run and finish like other
// operations. Released PTE installs are no longer listed by
// scheduledPteOperations and can no longer be canceled.
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
	slices.SortFunc(out, func(x, y bcadmin.InstalledApp) int {
		return cmp.Or(strings.Compare(x.Name, y.Name), strings.Compare(strings.ToLower(x.AppID), strings.ToLower(y.AppID)))
	})
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
		if compareVersions(v.Version, version) == 0 {
			return v, true
		}
	}
	return App{}, false
}

// busy reports whether an operation on appID in the environment key is
// scheduled or running. With ignoreHeldPTE, PTE installs that wait for
// their schedule do not count: the service keeps several scheduled
// versions of a PTE and keeps them when the PTE is uninstalled. a.mu must
// be held.
func (a *Apps) busy(key, appID string, ignoreHeldPTE bool) bool {
	for _, o := range a.ops {
		if ignoreHeldPTE && o.heldPTEInstall() {
			continue
		}
		if o.env == key && strings.EqualFold(o.app.AppID, appID) && !o.op.Status.Terminal() {
			return true
		}
	}
	return false
}

// missing returns the dependencies of app, direct and transitive, that are
// not installed in at least the highest minimum version any path requires,
// as requirements, and the catalog versions (always the latest) that would
// satisfy them. ok is false when a dependency cannot be satisfied from the
// catalog; its requirement then names the minimum version. a.mu must be
// held.
func (a *Apps) missing(key string, app App) (reqs []bcadmin.AppRequirement, fix []App, ok bool) {
	need := map[string]string{} // lower(appId): highest required minimum version
	planned := map[string]App{} // lower(appId): version to install or update to
	var order []string          // planned or unresolvable app IDs, in discovery order
	unresolvable := map[string]bool{}
	ids := map[string]string{} // lower(appId): appId as given
	planned[strings.ToLower(app.AppID)] = app
	queue := []App{app}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, d := range cur.Dependencies {
			id := strings.ToLower(d.AppID)
			if id == strings.ToLower(app.AppID) {
				continue // the app itself, reached through a cycle
			}
			ids[id] = d.AppID
			if compareVersions(d.MinVersion, need[id]) > 0 {
				need[id] = d.MinVersion
			}
			have := ""
			if p, ok := planned[id]; ok {
				have = p.Version
			} else if inst := a.installed[key][id]; inst != nil {
				have = inst.Version
			}
			if have != "" && compareVersions(have, need[id]) >= 0 {
				continue
			}
			if !slices.Contains(order, id) {
				order = append(order, id)
			}
			latest, found := a.published(d.AppID, "")
			if !found || compareVersions(latest.Version, need[id]) < 0 {
				unresolvable[id] = true
				delete(planned, id)
				continue
			}
			if _, done := planned[id]; !done {
				planned[id] = latest
				queue = append(queue, latest)
			}
		}
	}
	ok = true
	for _, id := range order {
		typ := "Install"
		if a.installed[key][id] != nil {
			typ = "Update"
		}
		if unresolvable[id] {
			ok = false
			reqs = append(reqs, bcadmin.AppRequirement{AppID: ids[id], Version: need[id], Type: typ})
			continue
		}
		p := planned[id]
		reqs = append(reqs, bcadmin.AppRequirement{AppID: p.AppID, Name: p.Name, Publisher: p.Publisher, Version: p.Version, Type: typ})
		fix = append(fix, p)
	}
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
		if strings.EqualFold(p.AppID, inst.AppID) && compareVersions(p.Version, inst.Version) == 0 {
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
// uninstall) and writes it. It is Running when scheduleKind is Immediate,
// otherwise Scheduled and held until RunScheduled. a.mu must be held.
func (a *Apps) start(w http.ResponseWriter, r *http.Request, kind string, app App, source string, scheduleKind string, pte bool, handleDeps bool, params map[string]any) {
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
		Parameters:       params,
	}
	if scheduleKind == "Immediate" {
		op.Status = bcadmin.OperationStatusRunning
		op.StartedOn = &bcadmin.Time{Time: now}
	}
	if kind == "uninstall" {
		op.TargetAppVersion, op.TargetVersion = "", ""
	}
	o := &appOperation{
		op: op, env: envKey(r.PathValue("family"), r.PathValue("env")), app: app, kind: kind,
		pte: pte, held: scheduleKind != "Immediate", handleDeps: handleDeps,
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
	if o.fail != "" {
		a.failOperation(o, o.fail)
		return
	}
	// Check the current state again: other operations may have changed it
	// while this one was scheduled.
	apps := a.envApps(o.env)
	key := strings.ToLower(o.app.AppID)
	if o.kind == "uninstall" {
		if apps[key] == nil {
			o.op.Status = bcadmin.OperationStatusSkipped
			return
		}
		deps := a.dependents(o.env, o.app.AppID)
		if len(deps) > 0 && !o.handleDeps {
			a.failOperation(o, "The app cannot be uninstalled because other apps depend on it: "+appNames(deps))
			return
		}
		if _, msg := a.uninstallBlocked(o.env, deps); msg != "" {
			a.failOperation(o, msg)
			return
		}
		o.op.Status = bcadmin.OperationStatusSucceeded
		for _, d := range deps {
			delete(apps, strings.ToLower(d.AppID))
		}
		delete(apps, key)
		return
	}
	if o.kind == "update" && apps[key] == nil {
		a.failOperation(o, "The app is not installed.")
		return
	}
	if inst := apps[key]; inst != nil && compareVersions(inst.Version, o.app.Version) >= 0 {
		// The same or a newer version is installed already.
		o.op.Status = bcadmin.OperationStatusSkipped
		inst.State = bcadmin.AppStateInstalled
		setResult(inst, o, "Skipped")
		return
	}
	reqs, fix, ok := a.missing(o.env, o.app)
	if len(reqs) > 0 && (!o.handleDeps || !ok) {
		names := make([]bcadmin.InstalledApp, 0, len(reqs))
		for _, r := range reqs {
			names = append(names, bcadmin.InstalledApp{AppID: r.AppID, Name: r.Name, Version: r.Version})
		}
		a.failOperation(o, "Dependencies of the app are not met: "+appNames(names))
		return
	}
	o.op.Status = bcadmin.OperationStatusSucceeded
	for _, d := range fix {
		a.put(apps, d, false, true, o)
	}
	a.put(apps, o.app, o.pte, false, o)
}

// failOperation ends o with status Failed and message, without changing the
// installed apps. a.mu must be held.
func (a *Apps) failOperation(o *appOperation, message string) {
	o.op.Status = bcadmin.OperationStatusFailed
	o.op.ErrorMessage = message
	if inst := a.installed[o.env][strings.ToLower(o.app.AppID)]; inst != nil {
		inst.State = bcadmin.AppStateInstalled
		setResult(inst, o, "Failed")
	}
}

// appNames lists apps as "name version (id)".
func appNames(apps []bcadmin.InstalledApp) string {
	parts := make([]string, 0, len(apps))
	for _, a := range apps {
		parts = append(parts, strings.TrimSpace(a.Name+" "+a.Version)+" ("+a.AppID+")")
	}
	return strings.Join(parts, ", ")
}

// put installs app in apps or updates it as the result of o. For a
// dependency (dep), the app's own last operation fields are kept, and so
// is its state while it has its own pending operation. a.mu must be held.
func (a *Apps) put(apps map[string]*bcadmin.InstalledApp, app App, pte, dep bool, o *appOperation) {
	key := strings.ToLower(app.AppID)
	inst := apps[key]
	if inst == nil {
		typ := bcadmin.AppTypeGlobal
		if pte {
			typ = bcadmin.AppTypeTenant
		}
		inst = &bcadmin.InstalledApp{AppID: app.AppID, AppType: typ, CanBeUninstalled: true, State: bcadmin.AppStateInstalled}
		apps[key] = inst
	}
	inst.Name, inst.Publisher, inst.Version = app.Name, app.Publisher, app.Version
	if dep {
		if !a.busy(o.env, app.AppID, true) {
			inst.State = bcadmin.AppStateInstalled
		}
		return
	}
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
	if a.installed[key][strings.ToLower(appID)] != nil {
		WriteError(w, http.StatusConflict, "appAlreadyInstalled", "app "+appID+" is already installed")
		return
	}
	if _, ok := a.catalog[strings.ToLower(appID)]; !ok {
		WriteError(w, http.StatusNotFound, "appNotFound", "app "+appID+" was not found")
		return
	}
	if a.busy(key, appID, false) {
		WriteError(w, http.StatusConflict, "appOperationInProgress", "another operation on app "+appID+" is in progress")
		return
	}
	app, ok := a.published(appID, body.TargetVersion)
	if !ok {
		WriteError(w, http.StatusBadRequest, "versionNotFound", "version "+body.TargetVersion+" of app "+appID+" was not found")
		return
	}
	reqs, _, resolvable := a.missing(key, app)
	if len(reqs) > 0 && (!body.InstallOrUpdateNeededDependencies || !resolvable) {
		requirementsError(w, "The app cannot be installed because of missing dependencies.", reqs)
		return
	}
	a.start(w, r, "install", app, "", schedule(body.UseEnvironmentUpdateWindow), false, body.InstallOrUpdateNeededDependencies, nil)
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
	if a.busy(key, appID, false) {
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
	reqs, _, resolvable := a.missing(key, app)
	if len(reqs) > 0 && (!body.InstallOrUpdateNeededDependencies || !resolvable) {
		requirementsError(w, "The app cannot be updated because of missing dependencies.", reqs)
		return
	}
	a.start(w, r, "update", app, inst.Version, schedule(body.UseEnvironmentUpdateWindow), false, body.InstallOrUpdateNeededDependencies, nil)
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
	o.op.ErrorMessage = "The scheduled operation was canceled."
	o.op.CompletedOn = &bcadmin.Time{Time: time.Now().UTC()}
	// Only a canceled update of a global app is an update attempt of the
	// installed app; removing a scheduled PTE version does not change it.
	if inst := a.installed[o.env][strings.ToLower(o.app.AppID)]; inst != nil && o.kind == "update" && !o.pte {
		inst.State = bcadmin.AppStateInstalled
		setResult(inst, o, "Canceled")
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
	if a.busy(key, appID, true) {
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
	if status, msg := a.uninstallBlocked(key, deps); msg != "" {
		code := "appCannotBeUninstalled"
		if status == http.StatusConflict {
			code = "appOperationInProgress"
		}
		WriteError(w, status, code, msg)
		return
	}
	app := App{AppID: inst.AppID, Name: inst.Name, Publisher: inst.Publisher, Version: inst.Version}
	a.start(w, r, "uninstall", app, inst.Version, schedule(body.UseEnvironmentUpdateWindow), inst.AppType.Is(bcadmin.AppTypeTenant), body.UninstallDependents, nil)
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
		switch {
		case d.AppType.Is(bcadmin.AppTypeTenant):
			typ = "PTE"
		case d.AppType.Is(bcadmin.AppTypeDev):
			typ = "DEV"
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
			fileName = p.FileName() // already reduced to its base name
			continue
		}
		if !slices.Contains(pteFields, p.FormName()) {
			continue // NextPart discards the unread part
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
	if a.busy(key, app.AppID, true) {
		WriteError(w, http.StatusConflict, "appOperationInProgress", "another operation on app "+app.AppID+" is in progress")
		return
	}
	scheduleKind, ok := canonical(fields["deploymentSchedule"], "Immediate", "Immediate", "UpdateWindow", "NextMinorUpdate", "NextMajorUpdate")
	if !ok {
		WriteError(w, http.StatusBadRequest, "invalidInput", "invalid deploymentSchedule "+fields["deploymentSchedule"])
		return
	}
	syncMode, ok := canonical(fields["syncMode"], "Add", "Add", "ForceSync")
	if !ok {
		WriteError(w, http.StatusBadRequest, "invalidInput", "invalid syncMode "+fields["syncMode"])
		return
	}
	for _, o := range a.ops {
		if o.env == key && o.heldPTEInstall() && strings.EqualFold(o.app.AppID, app.AppID) &&
			compareVersions(o.app.Version, app.Version) == 0 && o.op.ScheduleKind == scheduleKind {
			WriteError(w, http.StatusConflict, "appOperationInProgress", "version "+app.Version+" of app "+app.AppID+" is already scheduled for "+scheduleKind)
			return
		}
	}
	if inst := a.installed[key][strings.ToLower(app.AppID)]; inst != nil && compareVersions(app.Version, inst.Version) < 0 {
		WriteError(w, http.StatusBadRequest, "invalidTargetVersion", "version "+app.Version+" is older than the installed version "+inst.Version)
		return
	}
	if inst := a.installed[key][strings.ToLower(app.AppID)]; inst == nil && scheduleKind != "Immediate" && scheduleKind != "UpdateWindow" {
		WriteError(w, http.StatusBadRequest, "invalidInput", "a new per-tenant extension can only use Immediate or UpdateWindow")
		return
	}
	needDeps, _ := strconv.ParseBool(fields["installOrUpdateNeededDependencies"])
	reqs, _, resolvable := a.missing(key, app)
	if len(reqs) > 0 && (!needDeps || !resolvable) {
		requirementsError(w, "The app cannot be installed because of missing dependencies.", reqs)
		return
	}
	opParams := map[string]any{
		"appId": app.AppID, "name": app.Name, "publisher": app.Publisher, "targetAppVersion": app.Version,
		"scheduleKind": scheduleKind, "syncMode": syncMode,
	}
	if lang := fields["languageId"]; lang != "" {
		opParams["languageId"] = lang
	}
	// The docs say sourceAppVersion is empty for PTE installs.
	a.start(w, r, "install", app, "", scheduleKind, true, needDeps, opParams)
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
			compareVersions(o.app.Version, body.TargetVersion) == 0 && strings.EqualFold(o.op.ScheduleKind, string(body.ScheduleKind)) &&
			o.held && o.op.Status.Is(bcadmin.OperationStatusScheduled) {
			a.cancel(w, o)
			return
		}
	}
	WriteError(w, http.StatusNotFound, "ResourceDoesNotExist",
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
			if c := cmp.Compare(ai, bi); c != 0 {
				return c
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

// heldPTEInstall reports whether o is a PTE install that waits for its
// schedule.
func (o *appOperation) heldPTEInstall() bool {
	return o.pte && o.kind == "install" && o.held && o.op.Status.Is(bcadmin.OperationStatusScheduled)
}

// pteFields are the form fields of a pteInstall request the fake reads;
// other parts are skipped.
var pteFields = []string{"acceptIsvEula", "installOrUpdateNeededDependencies", "deploymentSchedule", "syncMode", "languageId"}

// canonical returns the allowed value that equals v ignoring case, or def
// when v is empty. ok is false for other values.
func canonical(v, def string, allowed ...string) (string, bool) {
	if v == "" {
		return def, true
	}
	for _, a := range allowed {
		if strings.EqualFold(v, a) {
			return a, true
		}
	}
	return "", false
}

// uninstallBlocked reports why the dependents deps cannot be uninstalled
// with the app: one is protected (CanBeUninstalled false, 400) or has its
// own operation in progress (409). msg is empty when they can. a.mu must be
// held.
func (a *Apps) uninstallBlocked(key string, deps []bcadmin.InstalledApp) (status int, msg string) {
	for _, d := range deps {
		if !d.CanBeUninstalled {
			return http.StatusBadRequest, "the dependent app " + appNames([]bcadmin.InstalledApp{d}) + " cannot be uninstalled"
		}
		if a.busy(key, d.AppID, true) {
			return http.StatusConflict, "another operation on the dependent app " + appNames([]bcadmin.InstalledApp{d}) + " is in progress"
		}
	}
	return 0, ""
}
