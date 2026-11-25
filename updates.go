package bcadmin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// UpdatesService manages the version updates of one environment. Get it
// with [Client.Updates] or [Environment.Updates].
type UpdatesService struct {
	c           *Client
	family, env string
	err         error // set for services of unbound environments
}

// Updates returns the update endpoints of the environment
// family/environment.
func (c *Client) Updates(family, environment string) *UpdatesService {
	return &UpdatesService{c: c, family: family, env: environment}
}

// Updates returns the update endpoints of the environment. For an
// environment that is not bound to a client (see [Environment]), every
// method returns an error.
func (e *Environment) Updates() *UpdatesService {
	s, err := e.service()
	if err != nil {
		return &UpdatesService{err: err}
	}
	return s.c.Updates(e.ApplicationFamily, e.Name)
}

// TargetVersionType is the kind of a target version. Compare with
// [TargetVersionType.Is].
type TargetVersionType string

// Target version types. Preview and EarlyAccessPreview versions can only be
// selected for sandboxes; EarlyAccessPreview needs a partner sandbox license.
const (
	TargetVersionGA                 TargetVersionType = "GA"
	TargetVersionPreview            TargetVersionType = "Preview"
	TargetVersionEarlyAccessPreview TargetVersionType = "EarlyAccessPreview"
)

// Is reports whether t and other are the same type, ignoring case.
func (t TargetVersionType) Is(other TargetVersionType) bool {
	return strings.EqualFold(string(t), string(other))
}

// EnvironmentUpdate is a version an environment can be updated to.
type EnvironmentUpdate struct {
	// TargetVersion is the version, for example "26.1".
	TargetVersion string `json:"targetVersion"`
	// Available tells whether the version has been released.
	Available bool `json:"available"`
	// Selected tells whether this version is selected as the next update.
	Selected bool `json:"selected"`
	// ScheduleDetails is set for released versions.
	ScheduleDetails *UpdateScheduleDetails `json:"scheduleDetails,omitempty"`
	// ExpectedAvailability is set for versions not released yet.
	ExpectedAvailability *ExpectedAvailability `json:"expectedAvailability,omitempty"`
	// TargetVersionType is GA, Preview or EarlyAccessPreview.
	TargetVersionType TargetVersionType `json:"targetVersionType,omitempty"`
}

// UpdateScheduleDetails describes when an update runs.
type UpdateScheduleDetails struct {
	// UpdateStatus is the status of the selected update, for example
	// "scheduled".
	UpdateStatus string `json:"updateStatus,omitempty"`
	// LatestSelectableDateTime is the latest date the update can be
	// scheduled for.
	LatestSelectableDateTime *Time `json:"latestSelectableDateTime,omitempty"`
	// SelectedDateTime is when the update is scheduled to start.
	SelectedDateTime *Time `json:"selectedDateTime,omitempty"`
	// IgnoreUpdateWindow tells whether the update may run outside the
	// environment's update window.
	IgnoreUpdateWindow bool `json:"ignoreUpdateWindow"`
	// RolloutStatus is the rollout status of the version, for example
	// "Active", "UnderMaintenance" or "Postponed".
	RolloutStatus string `json:"rolloutStatus,omitempty"`
}

// ExpectedAvailability is the month a version is expected to be released.
type ExpectedAvailability struct {
	Month int `json:"month"`
	Year  int `json:"year"`
}

// ScheduleUpdateOptions sets when an update runs. Unset fields are not sent.
type ScheduleUpdateOptions struct {
	// TargetVersionType is the type of the target version; the service
	// default is GA.
	TargetVersionType TargetVersionType
	// SelectedDateTime is when the update should start. Outside the update
	// window, the update starts in the next update window unless
	// IgnoreUpdateWindow is true. Sent in UTC.
	SelectedDateTime time.Time
	// IgnoreUpdateWindow allows the update to run outside the update
	// window. Nil leaves the setting unchanged.
	IgnoreUpdateWindow *bool
}

// List returns the versions the environment can be updated to.
//
// GET /applications/{applicationFamily}/environments/{environmentName}/updates.
func (s *UpdatesService) List(ctx context.Context) ([]EnvironmentUpdate, error) {
	if s.err != nil {
		return nil, s.err
	}
	return listAll[EnvironmentUpdate](ctx, s.c, "applications/{}/environments/{}/updates", s.family, s.env)
}

// Select selects targetVersion as the next update of the environment,
// replacing a previous selection, and sets its schedule. opts may be nil.
// For versions that are not released yet only the selection is allowed.
//
// PATCH /applications/{applicationFamily}/environments/{environmentName}/updates/{targetVersion}.
func (s *UpdatesService) Select(ctx context.Context, targetVersion string, opts *ScheduleUpdateOptions) error {
	if s.err != nil {
		return s.err
	}
	return s.patch(ctx, targetVersion, true, opts)
}

// Reschedule changes the schedule of targetVersion without changing which
// version is selected.
//
// PATCH /applications/{applicationFamily}/environments/{environmentName}/updates/{targetVersion}.
func (s *UpdatesService) Reschedule(ctx context.Context, targetVersion string, opts ScheduleUpdateOptions) error {
	if s.err != nil {
		return s.err
	}
	if opts.SelectedDateTime.IsZero() && opts.IgnoreUpdateWindow == nil && opts.TargetVersionType == "" {
		return errors.New("bcadmin: ScheduleUpdateOptions sets nothing to change")
	}
	return s.patch(ctx, targetVersion, false, &opts)
}

// updateBody is the wire form of the PATCH body.
type updateBody struct {
	Selected          *bool                `json:"selected,omitempty"`
	TargetVersionType TargetVersionType    `json:"targetVersionType,omitempty"`
	ScheduleDetails   *updateScheduleInput `json:"scheduleDetails,omitempty"`
}

type updateScheduleInput struct {
	SelectedDateTime   string `json:"selectedDateTime,omitempty"`
	IgnoreUpdateWindow *bool  `json:"ignoreUpdateWindow,omitempty"`
}

func (s *UpdatesService) patch(ctx context.Context, targetVersion string, selected bool, opts *ScheduleUpdateOptions) error {
	var body updateBody
	if selected {
		body.Selected = &selected
	}
	if opts != nil {
		body.TargetVersionType = opts.TargetVersionType
		if !opts.SelectedDateTime.IsZero() || opts.IgnoreUpdateWindow != nil {
			body.ScheduleDetails = &updateScheduleInput{IgnoreUpdateWindow: opts.IgnoreUpdateWindow}
			if !opts.SelectedDateTime.IsZero() {
				body.ScheduleDetails.SelectedDateTime = opts.SelectedDateTime.UTC().Format(time.RFC3339)
			}
		}
	}
	return s.c.call(ctx, http.MethodPatch, "applications/{}/environments/{}/updates/{}",
		[]string{s.family, s.env, targetVersion}, body, nil)
}

// CancelRunning cancels a running update (or data upgrade) operation of the
// environment and restores the environment to its state before the update.
// The operation must have status Running, and the environment must not be in
// its enforced update period. Available since API v2.28.
//
// POST /applications/{applicationFamily}/environments/{environmentName}/operations/{operationId}/cancel.
func (s *UpdatesService) CancelRunning(ctx context.Context, operationID string) error {
	if s.err != nil {
		return s.err
	}
	return s.c.call(ctx, http.MethodPost, "applications/{}/environments/{}/operations/{}/cancel",
		[]string{s.family, s.env, operationID}, nil, nil)
}
