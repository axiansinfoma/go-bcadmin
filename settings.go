package bcadmin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// SettingsService manages the settings of one environment: update window,
// Application Insights, security group, partner access, access with
// Microsoft 365 licenses and the AppSource app update cadence. Get it with
// [Client.Settings] or [Environment.Settings].
type SettingsService struct {
	c           *Client
	family, env string
	err         error // set for services of unbound environments
}

// Settings returns the settings endpoints of the environment
// family/environment.
func (c *Client) Settings(family, environment string) *SettingsService {
	return &SettingsService{c: c, family: family, env: environment}
}

// Settings returns the settings endpoints of the environment. For an
// environment that is not bound to a client (see [Environment]), every
// method returns an error.
func (e *Environment) Settings() *SettingsService {
	s, err := e.service()
	if err != nil {
		return &SettingsService{err: err}
	}
	return s.c.Settings(e.ApplicationFamily, e.Name)
}

// settingsPath is the path template of the environment's settings below
// which name is appended.
const settingsPath = "applications/{}/environments/{}/settings/"

func (s *SettingsService) segments() []string { return []string{s.family, s.env} }

// UpdateWindow is the window in which an environment is updated.
type UpdateWindow struct {
	// PreferredStartTime is the start of the window in 24-hour format
	// (HH:mm) in TimeZoneID. Returned since API v2.13.
	PreferredStartTime string `json:"preferredStartTime,omitempty"`
	// PreferredEndTime is the end of the window in 24-hour format (HH:mm)
	// in TimeZoneID. Returned since API v2.13.
	PreferredEndTime string `json:"preferredEndTime,omitempty"`
	// TimeZoneID is the Windows time zone identifier of the window, for
	// example "Romance Standard Time". Returned since API v2.13.
	TimeZoneID string `json:"timeZoneId,omitempty"`
	// PreferredStartTimeUTC is the start of the current or next occurrence
	// of the window.
	PreferredStartTimeUTC *Time `json:"preferredStartTimeUtc,omitempty"`
	// PreferredEndTimeUTC is the end of the current or next occurrence of
	// the window.
	PreferredEndTimeUTC *Time `json:"preferredEndTimeUtc,omitempty"`
}

// UpdateWindow returns the update window of the environment, or nil if none
// is set.
//
// GET /applications/{applicationFamily}/environments/{environmentName}/settings/upgrade.
func (s *SettingsService) UpdateWindow(ctx context.Context) (*UpdateWindow, error) {
	if s.err != nil {
		return nil, s.err
	}
	var w *UpdateWindow
	if err := s.c.call(ctx, http.MethodGet, settingsPath+"upgrade", s.segments(), nil, &w); err != nil {
		return nil, err
	}
	return w, nil
}

// SetUpdateWindowOptions sets the update window. Set either Start, End and
// TimeZoneID (recommended, API v2.13 and later) or StartUTC and EndUTC, not
// both.
type SetUpdateWindowOptions struct {
	// Start and End are the window in 24-hour format, for example "22:00"
	// and "06:00", in the Windows time zone TimeZoneID (see
	// [ApplicationsService.TimeZones]).
	Start, End, TimeZoneID string
	// StartUTC and EndUTC are the window as UTC times; only their time of
	// day is used. For countries with several time zones, the service may
	// reset the time zone shown in the admin center to the country default.
	StartUTC, EndUTC time.Time
}

type updateWindowBody struct {
	PreferredStartTime    string `json:"preferredStartTime,omitempty"`
	PreferredEndTime      string `json:"preferredEndTime,omitempty"`
	TimeZoneID            string `json:"timeZoneId,omitempty"`
	PreferredStartTimeUTC string `json:"preferredStartTimeUtc,omitempty"`
	PreferredEndTimeUTC   string `json:"preferredEndTimeUtc,omitempty"`
}

// SetUpdateWindow sets the update window of the environment and returns the
// window the service stored. The service refuses windows that are too short
// (invalidRange) or that conflict with a scheduled update
// (ScheduledUpgradeConstraintViolation).
//
// PUT /applications/{applicationFamily}/environments/{environmentName}/settings/upgrade.
func (s *SettingsService) SetUpdateWindow(ctx context.Context, opts SetUpdateWindowOptions) (*UpdateWindow, error) {
	if s.err != nil {
		return nil, s.err
	}
	wall := opts.Start != "" || opts.End != "" || opts.TimeZoneID != ""
	utc := !opts.StartUTC.IsZero() || !opts.EndUTC.IsZero()
	var body updateWindowBody
	switch {
	case wall && utc:
		return nil, errors.New("bcadmin: SetUpdateWindowOptions sets both the local and the UTC window; set one")
	case wall:
		if opts.Start == "" || opts.End == "" || opts.TimeZoneID == "" {
			return nil, errors.New("bcadmin: SetUpdateWindowOptions.Start, End and TimeZoneID are required together")
		}
		if err := s.c.needVersion("an update window with a time zone", 13); err != nil {
			return nil, err
		}
		body = updateWindowBody{PreferredStartTime: opts.Start, PreferredEndTime: opts.End, TimeZoneID: opts.TimeZoneID}
	case utc:
		if opts.StartUTC.IsZero() || opts.EndUTC.IsZero() {
			return nil, errors.New("bcadmin: SetUpdateWindowOptions.StartUTC and EndUTC are required together")
		}
		body = updateWindowBody{
			PreferredStartTimeUTC: opts.StartUTC.UTC().Format(time.RFC3339),
			PreferredEndTimeUTC:   opts.EndUTC.UTC().Format(time.RFC3339),
		}
	default:
		return nil, errors.New("bcadmin: SetUpdateWindowOptions sets no window")
	}
	var w UpdateWindow
	if err := s.c.call(ctx, http.MethodPut, settingsPath+"upgrade", s.segments(), body, &w); err != nil {
		return nil, err
	}
	return &w, nil
}

// SetAppInsightsKey sets the Application Insights connection string or
// instrumentation key of the environment. The environment must be Active;
// the service restarts it to apply the key, so run this outside working
// hours.
//
// POST /applications/{applicationFamily}/environments/{environmentName}/settings/appinsightskey.
func (s *SettingsService) SetAppInsightsKey(ctx context.Context, key string) error {
	if s.err != nil {
		return s.err
	}
	if strings.TrimSpace(key) == "" {
		return errors.New("bcadmin: Application Insights key is required")
	}
	return s.c.call(ctx, http.MethodPost, settingsPath+"appinsightskey", s.segments(), map[string]string{"key": key}, nil)
}

// SecurityGroup is the Microsoft Entra security group that controls access
// to an environment.
type SecurityGroup struct {
	// ID is the object ID of the group.
	ID string `json:"id"`
	// DisplayName is the group's name; empty when the group no longer
	// exists in Microsoft Entra ID.
	DisplayName string `json:"displayName"`
}

// SecurityGroup returns the security group of the environment, or nil if
// none is set. Available since API v2.8.
//
// GET /applications/{applicationFamily}/environments/{environmentName}/settings/securitygroupaccess.
func (s *SettingsService) SecurityGroup(ctx context.Context) (*SecurityGroup, error) {
	if s.err != nil {
		return nil, s.err
	}
	if err := s.c.needVersion("securitygroupaccess", 8); err != nil {
		return nil, err
	}
	var g *SecurityGroup
	if err := s.c.call(ctx, http.MethodGet, settingsPath+"securitygroupaccess", s.segments(), nil, &g); err != nil {
		return nil, err
	}
	if g != nil && g.ID == "" {
		return nil, nil // 204 No Content or an empty object: no group
	}
	return g, nil
}

// SetSecurityGroup sets the Microsoft Entra security group, by object ID,
// that controls access to the environment. A group that does not exist
// gives a 404 error. Available since API v2.8.
//
// POST /applications/{applicationFamily}/environments/{environmentName}/settings/securitygroupaccess.
func (s *SettingsService) SetSecurityGroup(ctx context.Context, groupID string) error {
	if s.err != nil {
		return s.err
	}
	if err := s.c.needVersion("securitygroupaccess", 8); err != nil {
		return err
	}
	if groupID == "" {
		return errors.New("bcadmin: group ID is required")
	}
	body := struct {
		Value string `json:"Value"` // capitalized, as documented
	}{groupID}
	return s.c.call(ctx, http.MethodPost, settingsPath+"securitygroupaccess", s.segments(), body, nil)
}

// ClearSecurityGroup removes the security group of the environment.
// Available since API v2.8.
//
// DELETE /applications/{applicationFamily}/environments/{environmentName}/settings/securitygroupaccess.
func (s *SettingsService) ClearSecurityGroup(ctx context.Context) error {
	if s.err != nil {
		return s.err
	}
	if err := s.c.needVersion("securitygroupaccess", 8); err != nil {
		return err
	}
	return s.c.call(ctx, http.MethodDelete, settingsPath+"securitygroupaccess", s.segments(), nil, nil)
}

// PartnerAccessStatus tells which partner tenants may access an
// environment. Compare with [PartnerAccessStatus.Is].
type PartnerAccessStatus string

// Partner access statuses.
const (
	PartnerAccessDisabled                    PartnerAccessStatus = "Disabled"
	PartnerAccessAllowAllPartnerTenants      PartnerAccessStatus = "AllowAllPartnerTenants"
	PartnerAccessAllowSelectedPartnerTenants PartnerAccessStatus = "AllowSelectedPartnerTenants"
)

// Is reports whether s and other are the same status, ignoring case.
func (s PartnerAccessStatus) Is(other PartnerAccessStatus) bool {
	return strings.EqualFold(string(s), string(other))
}

// PartnerAccess controls whether delegated administrators of partner
// tenants can access an environment.
type PartnerAccess struct {
	Status PartnerAccessStatus `json:"status"`
	// AllowedPartnerTenantIDs are the Microsoft Entra tenant IDs of the
	// partners allowed with status AllowSelectedPartnerTenants.
	AllowedPartnerTenantIDs []string `json:"allowedPartnerTenantIds,omitempty"`
}

// PartnerAccess returns the partner access settings of the environment.
// Only internal global administrators can call it: the service refuses
// delegated administrators and app-only (service-to-service)
// authentication. Available since API v2.22.
//
// GET /applications/{applicationFamily}/environments/{environmentName}/settings/partneraccess.
func (s *SettingsService) PartnerAccess(ctx context.Context) (*PartnerAccess, error) {
	if s.err != nil {
		return nil, s.err
	}
	if err := s.c.needVersion("partneraccess", 22); err != nil {
		return nil, err
	}
	var p PartnerAccess
	if err := s.c.call(ctx, http.MethodGet, settingsPath+"partneraccess", s.segments(), nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// SetPartnerAccess sets the partner access settings of the environment.
// AllowedPartnerTenantIDs may only be set with status
// AllowSelectedPartnerTenants. Only internal global administrators can call
// it, as for [SettingsService.PartnerAccess]. Available since API v2.22.
//
// PUT /applications/{applicationFamily}/environments/{environmentName}/settings/partneraccess.
func (s *SettingsService) SetPartnerAccess(ctx context.Context, access PartnerAccess) error {
	if s.err != nil {
		return s.err
	}
	if err := s.c.needVersion("partneraccess", 22); err != nil {
		return err
	}
	if access.Status == "" {
		return errors.New("bcadmin: PartnerAccess.Status is required")
	}
	if len(access.AllowedPartnerTenantIDs) > 0 && !access.Status.Is(PartnerAccessAllowSelectedPartnerTenants) {
		return errors.New("bcadmin: PartnerAccess.AllowedPartnerTenantIDs requires status AllowSelectedPartnerTenants")
	}
	return s.c.call(ctx, http.MethodPut, settingsPath+"partneraccess", s.segments(), access, nil)
}

// AccessWithM365Licenses reports whether users with Microsoft 365 licenses
// can access the environment. Supported for environments of version 21.1
// and later. Available since API v2.12.
//
// GET /applications/{applicationFamily}/environments/{environmentName}/settings/accesswithm365licenses.
func (s *SettingsService) AccessWithM365Licenses(ctx context.Context) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	if err := s.c.needVersion("accesswithm365licenses", 12); err != nil {
		return false, err
	}
	var v struct {
		Enabled bool `json:"enabled"`
	}
	if err := s.c.call(ctx, http.MethodGet, settingsPath+"accesswithm365licenses", s.segments(), nil, &v); err != nil {
		return false, err
	}
	return v.Enabled, nil
}

// SetAccessWithM365Licenses allows or denies access to the environment for
// users with Microsoft 365 licenses. Available since API v2.12.
//
// POST /applications/{applicationFamily}/environments/{environmentName}/settings/accesswithm365licenses.
func (s *SettingsService) SetAccessWithM365Licenses(ctx context.Context, enabled bool) error {
	if s.err != nil {
		return s.err
	}
	if err := s.c.needVersion("accesswithm365licenses", 12); err != nil {
		return err
	}
	body := struct {
		Enabled bool `json:"enabled"`
	}{enabled}
	return s.c.call(ctx, http.MethodPost, settingsPath+"accesswithm365licenses", s.segments(), body, nil)
}

// AppSourceUpdateCadence tells when AppSource (Marketplace) apps of an
// environment are updated.
type AppSourceUpdateCadence string

// AppSource app update cadences.
const (
	// AppSourceUpdateDefault leaves the cadence to the service.
	AppSourceUpdateDefault AppSourceUpdateCadence = "Default"
	// AppSourceUpdateMajor updates apps with every major update.
	AppSourceUpdateMajor AppSourceUpdateCadence = "DuringMajorUpgrade"
	// AppSourceUpdateMajorMinor updates apps with every major and minor
	// update.
	AppSourceUpdateMajorMinor AppSourceUpdateCadence = "DuringMajorMinorUpgrade"
)

// SetAppSourceUpdateCadence sets when AppSource apps of the environment are
// updated.
//
// PUT /applications/{applicationFamily}/environments/{environmentName}/settings/appSourceAppsUpdateCadence.
func (s *SettingsService) SetAppSourceUpdateCadence(ctx context.Context, cadence AppSourceUpdateCadence) error {
	if s.err != nil {
		return s.err
	}
	if cadence == "" {
		return errors.New("bcadmin: update cadence is required")
	}
	body := struct {
		Value AppSourceUpdateCadence `json:"value"`
	}{cadence}
	return s.c.call(ctx, http.MethodPut, settingsPath+"appSourceAppsUpdateCadence", s.segments(), body, nil)
}

// TimeZone is a Windows time zone that update windows can use.
type TimeZone struct {
	// ID is the identifier, for example "Romance Standard Time".
	ID string `json:"id"`
	// DisplayName is the display name, for example "(UTC+01:00) Brussels,
	// Copenhagen, Madrid, Paris".
	DisplayName string `json:"displayName"`
	// CurrentUTCOffset is the current offset from UTC, for example "+01:00".
	CurrentUTCOffset string `json:"currentUtcOffset"`
	// SupportsDaylightSavingTime tells whether the zone has daylight saving
	// time rules.
	SupportsDaylightSavingTime bool `json:"supportsDaylightSavingTime"`
	// IsCurrentlyDaylightSavingTime tells whether daylight saving time is in
	// effect at the time of the request.
	IsCurrentlyDaylightSavingTime bool `json:"isCurrentlyDaylightSavingTime"`
}

// TimeZones returns the time zones that update windows can use (see
// [SettingsService.SetUpdateWindow]). Available since API v2.13.
//
// GET /applications/settings/timezones.
func (s *ApplicationsService) TimeZones(ctx context.Context) ([]TimeZone, error) {
	if err := s.c.needVersion("timezones", 13); err != nil {
		return nil, err
	}
	return listAll[TimeZone](ctx, s.c, "applications/settings/timezones")
}
