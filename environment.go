package bcadmin

import (
	"context"
	"strings"
)

// EnvironmentStatus is the status of an environment. Compare with
// [EnvironmentStatus.Is]; the API is not consistent about casing.
type EnvironmentStatus string

// Environment statuses.
const (
	EnvironmentStatusNotReady           EnvironmentStatus = "NotReady"
	EnvironmentStatusRemoving           EnvironmentStatus = "Removing"
	EnvironmentStatusPreparing          EnvironmentStatus = "Preparing"
	EnvironmentStatusActive             EnvironmentStatus = "Active"
	EnvironmentStatusUpgrading          EnvironmentStatus = "Upgrading"
	EnvironmentStatusCreatingFailed     EnvironmentStatus = "CreatingFailed"
	EnvironmentStatusRemovingFailed     EnvironmentStatus = "RemovingFailed"
	EnvironmentStatusSoftDeleting       EnvironmentStatus = "SoftDeleting"
	EnvironmentStatusSoftDeletingFailed EnvironmentStatus = "SoftDeletingFailed"
	EnvironmentStatusSoftDeleted        EnvironmentStatus = "SoftDeleted"
	EnvironmentStatusRecovering         EnvironmentStatus = "Recovering"
	EnvironmentStatusRecoveringFailed   EnvironmentStatus = "RecoveringFailed"
)

// Is reports whether s and other are the same status, ignoring case.
func (s EnvironmentStatus) Is(other EnvironmentStatus) bool {
	return strings.EqualFold(string(s), string(other))
}

// Failed reports whether s is one of the failed statuses (CreatingFailed,
// RemovingFailed, SoftDeletingFailed, RecoveringFailed).
func (s EnvironmentStatus) Failed() bool {
	return strings.HasSuffix(strings.ToLower(string(s)), "failed")
}

// Environment is a Business Central environment.
//
// Environments returned by [EnvironmentsService.Get] and
// [EnvironmentsService.List] are bound to the client that returned them, so
// their methods (Refresh, Copy, Delete, ...) can be called directly. An
// Environment built by hand is not bound; use the methods of
// [EnvironmentsService] for it. The binding is an unexported field, so compare
// environments field by field (or with cmpopts.IgnoreUnexported) rather than
// with == or reflect.DeepEqual.
type Environment struct {
	// FriendlyName is the display name.
	FriendlyName string `json:"friendlyName,omitempty"`
	// Type is Production or Sandbox.
	Type EnvironmentType `json:"type,omitempty"`
	// Name is the environment name, unique within an application family.
	Name string `json:"name,omitempty"`
	// CountryCode is the country/region the environment is deployed in.
	CountryCode string `json:"countryCode,omitempty"`
	// ApplicationFamily is the family of the environment, for example
	// "BusinessCentral".
	ApplicationFamily string `json:"applicationFamily,omitempty"`
	// AadTenantID is the Microsoft Entra tenant that owns the environment.
	AadTenantID string `json:"aadTenantId,omitempty"`
	// ApplicationVersion is the application version (API v2.8 and earlier;
	// later versions report it in VersionDetails).
	ApplicationVersion string `json:"applicationVersion,omitempty"`
	// PlatformVersion is the platform version (API v2.8 and earlier).
	PlatformVersion string `json:"platformVersion,omitempty"`
	// Status is the current status.
	Status EnvironmentStatus `json:"status,omitempty"`
	// WebClientLoginURL is the URL to sign in to the environment.
	WebClientLoginURL string `json:"webClientLoginUrl,omitempty"`
	// WebServiceURL is the URL of the environment's service API.
	WebServiceURL string `json:"webServiceUrl,omitempty"`
	// LocationName is the Azure region the database is stored in.
	LocationName string `json:"locationName,omitempty"`
	// GeoName is the Azure geography the database is stored in.
	GeoName string `json:"geoName,omitempty"`
	// RingName is the release ring, for example "Prod" or "Preview".
	RingName string `json:"ringName,omitempty"`
	// AppInsightsKey is the Application Insights key or connection string.
	// It can grant write access to the telemetry resource; avoid logging
	// whole Environment values (for example with %+v).
	AppInsightsKey string `json:"appInsightsKey,omitempty"`
	// SoftDeletedOn is when the environment was soft deleted.
	SoftDeletedOn *Time `json:"softDeletedOn,omitempty"`
	// HardDeletePendingOn is when a soft-deleted environment is deleted
	// permanently.
	HardDeletePendingOn *Time `json:"hardDeletePendingOn,omitempty"`
	// DeleteReason is why the environment was deleted.
	DeleteReason string `json:"deleteReason,omitempty"`
	// AppSourceAppsUpdateCadence is when Marketplace apps are updated, for
	// example "Default", "DuringMajorUpgrade" or "DuringMajorMinorUpgrade".
	AppSourceAppsUpdateCadence string `json:"appSourceAppsUpdateCadence,omitempty"`
	// VersionDetails holds the current version and its support dates.
	VersionDetails *VersionDetails `json:"versionDetails,omitempty"`

	svc *EnvironmentsService
}

// VersionDetails describes the version of an environment.
type VersionDetails struct {
	// Version is the current version, for example "26.0".
	Version string `json:"version,omitempty"`
	// GracePeriodStartDate is when the grace period of the current major
	// version starts.
	GracePeriodStartDate *Time `json:"gracePeriodStartDate,omitempty"`
	// EnforcedUpdatePeriodStartDate is when the enforced update period of
	// the current major version starts.
	EnforcedUpdatePeriodStartDate *Time `json:"enforcedUpdatePeriodStartDate,omitempty"`
}

// service returns the service the environment is bound to.
func (e *Environment) service() (*EnvironmentsService, error) {
	if e == nil || e.svc == nil {
		return nil, errUnbound
	}
	return e.svc, nil
}

// Refresh returns the current state of the environment.
func (e *Environment) Refresh(ctx context.Context) (*Environment, error) {
	s, err := e.service()
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, e.ApplicationFamily, e.Name)
}

// Copy copies the environment to a new environment. See
// [EnvironmentsService.Copy].
func (e *Environment) Copy(ctx context.Context, targetName string, targetType EnvironmentType) (*Poller[*Environment], error) {
	s, err := e.service()
	if err != nil {
		return nil, err
	}
	return s.Copy(ctx, e.ApplicationFamily, e.Name, targetName, targetType)
}

// Delete deletes the environment. See [EnvironmentsService.Delete].
func (e *Environment) Delete(ctx context.Context) (*Poller[*Operation], error) {
	s, err := e.service()
	if err != nil {
		return nil, err
	}
	return s.Delete(ctx, e.ApplicationFamily, e.Name)
}

// Recover recovers the soft-deleted environment. See
// [EnvironmentsService.Recover].
func (e *Environment) Recover(ctx context.Context) (*Poller[*Environment], error) {
	s, err := e.service()
	if err != nil {
		return nil, err
	}
	return s.Recover(ctx, e.ApplicationFamily, e.Name)
}

// Rename renames the environment. See [EnvironmentsService.Rename]. e keeps
// the old name; use the environment returned by the poller afterwards.
func (e *Environment) Rename(ctx context.Context, newName string) (*Poller[*Environment], error) {
	s, err := e.service()
	if err != nil {
		return nil, err
	}
	return s.Rename(ctx, e.ApplicationFamily, e.Name, newName)
}

// Restore restores the environment to a point in time into a new
// environment. See [EnvironmentsService.Restore].
func (e *Environment) Restore(ctx context.Context, opts RestoreOptions) (*Poller[*Environment], error) {
	s, err := e.service()
	if err != nil {
		return nil, err
	}
	return s.Restore(ctx, e.ApplicationFamily, e.Name, opts)
}

// RestorePeriods returns the periods the environment can be restored to.
// See [EnvironmentsService.RestorePeriods].
func (e *Environment) RestorePeriods(ctx context.Context) ([]RestorePeriod, error) {
	s, err := e.service()
	if err != nil {
		return nil, err
	}
	return s.RestorePeriods(ctx, e.ApplicationFamily, e.Name)
}

// UsedStorage returns the database storage used by the environment.
func (e *Environment) UsedStorage(ctx context.Context) (*UsedStorage, error) {
	s, err := e.service()
	if err != nil {
		return nil, err
	}
	return s.UsedStorage(ctx, e.ApplicationFamily, e.Name)
}

// Operations returns the operations of the environment.
func (e *Environment) Operations(ctx context.Context) ([]Operation, error) {
	s, err := e.service()
	if err != nil {
		return nil, err
	}
	return s.Operations(ctx, e.ApplicationFamily, e.Name)
}

// MatchProductionConfiguration temporarily applies the database configuration
// of a production environment to the sandbox. See
// [EnvironmentsService.MatchProductionConfiguration].
func (e *Environment) MatchProductionConfiguration(ctx context.Context) (*Poller[*Operation], error) {
	s, err := e.service()
	if err != nil {
		return nil, err
	}
	return s.MatchProductionConfiguration(ctx, e.ApplicationFamily, e.Name)
}
