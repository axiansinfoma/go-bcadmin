package bcadmin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// AppsService manages the apps of one environment. Get it with
// [Client.Apps] or [Environment.Apps].
type AppsService struct {
	c           *Client
	family, env string
	err         error // set for services of unbound environments
}

// Apps returns the app endpoints of the environment family/environment.
func (c *Client) Apps(family, environment string) *AppsService {
	return &AppsService{c: c, family: family, env: environment}
}

// Apps returns the app endpoints of the environment. For an environment that
// is not bound to a client (see [Environment]), every method returns an
// error.
func (e *Environment) Apps() *AppsService {
	s, err := e.service()
	if err != nil {
		return &AppsService{err: err}
	}
	return s.c.Apps(e.ApplicationFamily, e.Name)
}

// AppType is the type of an installed app. Compare with [AppType.Is].
type AppType string

// App types.
const (
	AppTypeGlobal AppType = "global"
	AppTypeTenant AppType = "tenant"
	AppTypeDev    AppType = "dev"
)

// Is reports whether t and other are the same type, ignoring case.
func (t AppType) Is(other AppType) bool { return strings.EqualFold(string(t), string(other)) }

// AppState is the state of an installed app. Compare with [AppState.Is].
type AppState string

// App states.
const (
	AppStateInstalled     AppState = "Installed"
	AppStateUpdatePending AppState = "UpdatePending"
	AppStateUpdating      AppState = "Updating"
)

// Is reports whether s and other are the same state, ignoring case.
func (s AppState) Is(other AppState) bool { return strings.EqualFold(string(s), string(other)) }

// InstalledApp is an app installed in an environment.
type InstalledApp struct {
	AppID                      string   `json:"appId"`
	Name                       string   `json:"name"`
	Publisher                  string   `json:"publisher"`
	Version                    string   `json:"version"`
	State                      AppState `json:"state,omitempty"`
	LastOperationID            string   `json:"lastOperationId,omitempty"`
	LastUpdateAttemptResult    string   `json:"lastUpdateAttemptResult,omitempty"`
	LastUninstallOperationID   string   `json:"lastUninstallOperationId,omitempty"`
	LastUninstallAttemptResult string   `json:"lastUninstallAttemptResult,omitempty"`
	AppType                    AppType  `json:"appType,omitempty"`
	CanBeUninstalled           bool     `json:"canBeUninstalled"`
}

// AvailableAppUpdate is a newer version of an installed global app.
type AvailableAppUpdate struct {
	AppID     string `json:"appId"`
	Name      string `json:"name"`
	Publisher string `json:"publisher"`
	// Version is the new version.
	Version string `json:"version"`
	// Requirements lists apps that must be installed or updated first.
	Requirements []AppRequirement `json:"requirements,omitempty"`
}

// UninstallRequirement is an app that depends on the app to uninstall.
type UninstallRequirement struct {
	AppID     string `json:"appId"`
	Name      string `json:"name"`
	Publisher string `json:"publisher"`
	Version   string `json:"version"`
	// Type is the app type: Global, PTE or DEV.
	Type string `json:"type,omitempty"`
	// OperationRequirementType is the operation the dependency applies to.
	OperationRequirementType string `json:"EnvironmentAppOperationRequirementType,omitempty"`
}

// InstallAppOptions configures [AppsService.Install].
type InstallAppOptions struct {
	// TargetVersion is the version to install. Optional; the latest version
	// is installed when empty. Required when AllowPreviewVersion is set.
	TargetVersion string `json:"targetVersion,omitempty"`
	// UseEnvironmentUpdateWindow runs the installation in the environment's
	// update window; until then the operation is Scheduled.
	UseEnvironmentUpdateWindow bool `json:"useEnvironmentUpdateWindow"`
	// AllowPreviewVersion allows preview versions (Embed ISV apps only).
	AllowPreviewVersion bool `json:"allowPreviewVersion"`
	// InstallOrUpdateNeededDependencies installs or updates dependencies;
	// otherwise missing dependencies are returned in the error's
	// Data.Requirements.
	InstallOrUpdateNeededDependencies bool `json:"installOrUpdateNeededDependencies"`
	// AcceptISVEULA must be true for the installation to proceed. Setting it
	// accepts the app provider's license terms and the terms described in
	// the Admin Center API documentation.
	AcceptISVEULA bool `json:"acceptIsvEula"`
	// LanguageID is the language for the installation, for example "en-US".
	LanguageID string `json:"languageId,omitempty"`
}

// UpdateAppOptions configures [AppsService.Update].
type UpdateAppOptions struct {
	// TargetVersion is the version to update to. Required; see
	// [AppsService.AvailableUpdates].
	TargetVersion string `json:"targetVersion"`
	// UseEnvironmentUpdateWindow runs the update in the environment's update
	// window; until then the operation is Scheduled.
	UseEnvironmentUpdateWindow bool `json:"useEnvironmentUpdateWindow"`
	// AllowPreviewVersion allows preview versions.
	AllowPreviewVersion bool `json:"allowPreviewVersion"`
	// InstallOrUpdateNeededDependencies installs or updates dependencies;
	// otherwise missing dependencies are returned in the error's
	// Data.Requirements.
	InstallOrUpdateNeededDependencies bool `json:"installOrUpdateNeededDependencies"`
}

// UninstallAppOptions configures [AppsService.Uninstall].
type UninstallAppOptions struct {
	// UseEnvironmentUpdateWindow runs the uninstallation in the environment's
	// update window; until then the operation is Scheduled.
	UseEnvironmentUpdateWindow bool `json:"useEnvironmentUpdateWindow"`
	// UninstallDependents also uninstalls apps that depend on the app;
	// otherwise they are returned in the error's Data.Requirements.
	UninstallDependents bool `json:"uninstallDependents"`
	// DeleteData deletes the app's data (sync mode clean), as in the
	// Extension Management page.
	DeleteData bool `json:"deleteData"`
}

// List returns the apps installed in the environment.
//
// GET /applications/{applicationFamily}/environments/{environmentName}/apps.
func (s *AppsService) List(ctx context.Context) ([]InstalledApp, error) {
	if s.err != nil {
		return nil, s.err
	}
	return listAll[InstalledApp](ctx, s.c, "applications/{}/environments/{}/apps", s.family, s.env)
}

// AvailableUpdates returns the updates available for installed global apps.
//
// GET /applications/{applicationFamily}/environments/{environmentName}/apps/availableUpdates.
func (s *AppsService) AvailableUpdates(ctx context.Context) ([]AvailableAppUpdate, error) {
	if s.err != nil {
		return nil, s.err
	}
	return listAll[AvailableAppUpdate](ctx, s.c, "applications/{}/environments/{}/apps/availableUpdates", s.family, s.env)
}

// Install installs a global (AppSource) app and returns a poller for the
// operation. Use [AppsService.InstallPTE] for per-tenant extensions.
// Available since API v2.6.
//
// POST /applications/{applicationFamily}/environments/{environmentName}/apps/{appId}/install.
func (s *AppsService) Install(ctx context.Context, appID string, opts InstallAppOptions) (*Poller[*Operation], error) {
	if s.err != nil {
		return nil, s.err
	}
	if opts.AllowPreviewVersion && opts.TargetVersion == "" {
		return nil, errors.New("bcadmin: InstallAppOptions.TargetVersion is required with AllowPreviewVersion")
	}
	return s.startAppOperation(ctx, "install", 6, appID, "applications/{}/environments/{}/apps/{}/install", opts)
}

// Update updates a global app to opts.TargetVersion and returns a poller for
// the operation. Per-tenant extensions are updated by uploading a new
// version with [AppsService.InstallPTE].
//
// POST /applications/{applicationFamily}/environments/{environmentName}/apps/{appId}/update.
func (s *AppsService) Update(ctx context.Context, appID string, opts UpdateAppOptions) (*Poller[*Operation], error) {
	if s.err != nil {
		return nil, s.err
	}
	if opts.TargetVersion == "" {
		return nil, errors.New("bcadmin: UpdateAppOptions.TargetVersion is required")
	}
	return s.startAppOperation(ctx, "update", 0, appID, "applications/{}/environments/{}/apps/{}/update", opts)
}

// CancelUpdate cancels a scheduled update of a global app and returns the
// canceled operation. It does not apply to per-tenant extensions.
//
// POST /applications/{applicationFamily}/environments/{environmentName}/apps/{appId}/update/cancel.
func (s *AppsService) CancelUpdate(ctx context.Context, appID, scheduledOperationID string) (*Operation, error) {
	if s.err != nil {
		return nil, s.err
	}
	if scheduledOperationID == "" {
		return nil, errors.New("bcadmin: scheduled operation ID is required")
	}
	body := struct {
		ScheduledOperationID string `json:"ScheduledOperationId"`
	}{scheduledOperationID}
	var op Operation
	if err := s.c.call(ctx, http.MethodPost, "applications/{}/environments/{}/apps/{}/update/cancel",
		[]string{s.family, s.env, appID}, body, &op); err != nil {
		return nil, err
	}
	return &op, nil
}

// Uninstall uninstalls an app and returns a poller for the operation.
// Available since API v2.6.
//
// POST /applications/{applicationFamily}/environments/{environmentName}/apps/{appId}/uninstall.
func (s *AppsService) Uninstall(ctx context.Context, appID string, opts UninstallAppOptions) (*Poller[*Operation], error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.startAppOperation(ctx, "uninstall", 6, appID, "applications/{}/environments/{}/apps/{}/uninstall", opts)
}

// UninstallRequirements returns the apps that depend on the app and would
// have to be uninstalled with it. Available since API v2.25.
//
// GET /applications/{applicationFamily}/environments/{environmentName}/apps/{appId}/uninstallRequirements.
func (s *AppsService) UninstallRequirements(ctx context.Context, appID string) ([]UninstallRequirement, error) {
	if s.err != nil {
		return nil, s.err
	}
	if err := s.needVersion("uninstallRequirements", 25); err != nil {
		return nil, err
	}
	return listAll[UninstallRequirement](ctx, s.c, "applications/{}/environments/{}/apps/{}/uninstallRequirements", s.family, s.env, appID)
}

// Operations returns the install, update and uninstall operations of an app.
//
// GET /applications/{applicationFamily}/environments/{environmentName}/apps/{appId}/operations.
func (s *AppsService) Operations(ctx context.Context, appID string) ([]Operation, error) {
	if s.err != nil {
		return nil, s.err
	}
	return listAll[Operation](ctx, s.c, "applications/{}/environments/{}/apps/{}/operations", s.family, s.env, appID)
}

// GetOperation returns one operation of an app.
//
// GET /applications/{applicationFamily}/environments/{environmentName}/apps/{appId}/operations/{operationId}.
func (s *AppsService) GetOperation(ctx context.Context, appID, operationID string) (*Operation, error) {
	if s.err != nil {
		return nil, s.err
	}
	// The documentation describes a plain object; the wrapped form
	// {"value": [...]} of the list endpoint is accepted defensively.
	var raw struct {
		Operation
		Value []Operation `json:"value"`
	}
	if err := s.c.call(ctx, http.MethodGet, "applications/{}/environments/{}/apps/{}/operations/{}",
		[]string{s.family, s.env, appID, operationID}, nil, &raw); err != nil {
		return nil, err
	}
	if raw.Value == nil {
		if raw.ID != "" && !asciiEqualFold(raw.ID, operationID) {
			return nil, fmt.Errorf("bcadmin: operation %s of app %s: response is for operation %s: %w",
				clean(operationID), clean(appID), clean(raw.ID), ErrNotFound)
		}
		return &raw.Operation, nil
	}
	for i := range raw.Value {
		if asciiEqualFold(raw.Value[i].ID, operationID) {
			return &raw.Value[i], nil
		}
	}
	return nil, fmt.Errorf("bcadmin: operation %s of app %s: %w", clean(operationID), clean(appID), ErrNotFound)
}

// ResumeOperation returns a poller for an operation of an app that was
// started earlier. Its result is the final operation.
func (s *AppsService) ResumeOperation(appID, operationID string) (*Poller[*Operation], error) {
	if s.err != nil {
		return nil, s.err
	}
	if _, err := path("applications/{}/environments/{}/apps/{}/operations/{}", s.family, s.env, appID, operationID); err != nil {
		return nil, err
	}
	return newPoller(s.c, &Operation{ID: operationID}, s.fetcher(appID, operationID), operationResult), nil
}

func (s *AppsService) fetcher(appID, operationID string) func(context.Context) (*Operation, error) {
	return func(ctx context.Context) (*Operation, error) { return s.GetOperation(ctx, appID, operationID) }
}

// needVersion returns an error if the client's API version is older than
// v2.minMinor, the version in which the endpoint called name was introduced.
func (s *AppsService) needVersion(name string, minMinor int) error {
	return s.c.needVersion(name, minMinor)
}

// startAppOperation sends a POST that starts the operation name (install,
// update, uninstall) on appID and returns a poller for it. minMinor is the
// documented minimum minor API version of the endpoint (v2.minMinor); 0
// means none is documented.
func (s *AppsService) startAppOperation(ctx context.Context, name string, minMinor int, appID, template string, body any) (*Poller[*Operation], error) {
	if err := s.needVersion("app "+name, minMinor); err != nil {
		return nil, err
	}
	var op Operation
	if err := s.c.call(ctx, http.MethodPost, template, []string{s.family, s.env, appID}, body, &op); err != nil {
		return nil, err
	}
	if op.ID == "" {
		return nil, fmt.Errorf("bcadmin: app %s of %s was accepted but the response contains no operation to track; check the app's operations before trying again", name, clean(appID))
	}
	return newPoller(s.c, &op, s.fetcher(appID, op.ID), operationResult), nil
}
