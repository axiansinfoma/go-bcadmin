package bcadmin

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"time"
)

// errUnbound is returned by methods of an Environment that was not returned
// by a client.
var errUnbound = errors.New("bcadmin: environment is not bound to a client; use the EnvironmentsService methods")

// EnvironmentsService manages environments. Use it through
// [Client.Environments].
type EnvironmentsService struct {
	c *Client
}

// Environments returns the endpoints for environments.
func (c *Client) Environments() *EnvironmentsService { return &EnvironmentsService{c: c} }

// CreateEnvironmentOptions describes a new environment.
type CreateEnvironmentOptions struct {
	// Type is Production or Sandbox. Required.
	Type EnvironmentType `json:"environmentType"`
	// CountryCode is the country/region to create the environment in, for
	// example "DE". Required. See [ApplicationsService.Families].
	CountryCode string `json:"countryCode"`
	// RingName is the release ring. Optional; the production ring is used
	// when empty. Only sandboxes can be created in a preview ring.
	RingName string `json:"ringName,omitempty"`
	// ApplicationVersion is the version to create the environment on.
	// Optional; the latest version is used when empty. See
	// [ApplicationsService.Versions].
	ApplicationVersion string `json:"applicationVersion,omitempty"`
}

// RestoreOptions describes a point-in-time restore. The restore creates a
// new environment.
type RestoreOptions struct {
	// EnvironmentName is the name of the new environment. Required.
	EnvironmentName string `json:"EnvironmentName"`
	// EnvironmentType is the type of the new environment. Required.
	EnvironmentType EnvironmentType `json:"EnvironmentType"`
	// PointInTime is the time to restore to. Required. It is sent in UTC.
	PointInTime time.Time `json:"-"`
	// SkipInstallingPTEs uninstalls per-tenant extensions in the new
	// environment.
	SkipInstallingPTEs bool `json:"SkipInstallingPTEs,omitempty"`
	// SkipInstallingThirdPartyGlobalApps uninstalls third-party Marketplace
	// apps in the new environment.
	SkipInstallingThirdPartyGlobalApps bool `json:"SkipInstallingThirdPartyGlobalApps,omitempty"`
	// SkipEnvironmentCleanup skips the codeunits that clear tables and
	// disable setups to avoid side effects on external integrations.
	SkipEnvironmentCleanup bool `json:"SkipEnvironmentCleanup,omitempty"`
}

// RestorePeriod is a period an environment can be restored to.
type RestorePeriod struct {
	From *Time `json:"from,omitempty"`
	To   *Time `json:"to,omitempty"`
	// CorrespondingApplicationPackageVersion is the application version the
	// environment is restored to.
	CorrespondingApplicationPackageVersion string `json:"correspondingApplicationPackageVersion,omitempty"`
}

// MatchProductionConfigurationMetrics tells whether a sandbox can get the
// database configuration of a production environment, and the monthly quota.
type MatchProductionConfigurationMetrics struct {
	RequestsPerMonth                int    `json:"requestsPerMonth"`
	RequestsUsedThisMonth           int    `json:"requestsUsedThisMonth"`
	RequestsRemainingThisMonth      int    `json:"requestsRemainingThisMonth"`
	ResetsAtUTC                     *Time  `json:"resetsAtUtc,omitempty"`
	CanMatchProductionConfiguration bool   `json:"canMatchProductionConfiguration"`
	BlockedReason                   string `json:"blockedReason,omitempty"`
	DurationInHours                 int    `json:"durationInHours"`
}

// bind binds env to the service and returns it.
func (s *EnvironmentsService) bind(env *Environment) *Environment {
	env.svc = s
	return env
}

// List returns the environments of family, or of all families when family is
// empty.
//
// GET /applications/environments or /applications/{applicationFamily}/environments.
func (s *EnvironmentsService) List(ctx context.Context, family string) ([]*Environment, error) {
	var out []*Environment
	for env, err := range s.All(ctx, family) {
		if err != nil {
			return nil, err
		}
		out = append(out, env)
	}
	if out == nil {
		out = []*Environment{}
	}
	return out, nil
}

// All iterates over the environments of family, or of all families when
// family is empty, requesting further pages as needed.
func (s *EnvironmentsService) All(ctx context.Context, family string) iter.Seq2[*Environment, error] {
	return func(yield func(*Environment, error) bool) {
		seq := list[Environment](ctx, s.c, "applications/environments")
		if family != "" {
			seq = list[Environment](ctx, s.c, "applications/{}/environments", family)
		}
		for env, err := range seq {
			if err != nil {
				yield(nil, err)
				return
			}
			// Bound methods use the family of the entry; with a family
			// requested, it must be that one.
			if family != "" {
				if env.ApplicationFamily == "" {
					env.ApplicationFamily = family
				} else if !asciiEqualFold(env.ApplicationFamily, family) {
					yield(nil, fmt.Errorf("bcadmin: list of family %s contains environment %s of family %s",
						clean(family), clean(env.Name), clean(env.ApplicationFamily)))
					return
				}
			}
			if !yield(s.bind(&env), nil) {
				return
			}
		}
	}
}

// Get returns one environment.
//
// GET /applications/{applicationFamily}/environments/{environmentName}.
func (s *EnvironmentsService) Get(ctx context.Context, family, name string) (*Environment, error) {
	// The documentation shows the environment wrapped in {"value": [...]},
	// the service returns it unwrapped; accept both.
	var raw struct {
		Environment
		Value []Environment `json:"value"`
	}
	if err := s.c.call(ctx, http.MethodGet, "applications/{}/environments/{}", []string{family, name}, nil, &raw); err != nil {
		return nil, err
	}
	env := raw.Environment
	if len(raw.Value) > 0 {
		env = raw.Value[0]
		for _, v := range raw.Value {
			if asciiEqualFold(v.Name, name) {
				env = v
				break
			}
		}
	}
	// Bound methods (Delete, ...) use the returned name and family, so they
	// must be the requested ones.
	if !asciiEqualFold(env.Name, name) || (env.ApplicationFamily != "" && !asciiEqualFold(env.ApplicationFamily, family)) {
		return nil, fmt.Errorf("bcadmin: response for environment %s/%s describes environment %s/%s",
			clean(family), clean(name), clean(env.ApplicationFamily), clean(env.Name))
	}
	if env.ApplicationFamily == "" {
		env.ApplicationFamily = family
	}
	env.svc = nil
	return s.bind(&env), nil
}

// Create creates an environment and returns a poller for the creation.
// The poller's result is the new environment.
//
// The request is not retried on server errors, because the environment may
// already be being created; check [EnvironmentsService.Get] or the tenant's
// operations before trying again.
//
// PUT /applications/{applicationFamily}/environments/{environmentName}.
func (s *EnvironmentsService) Create(ctx context.Context, family, name string, opts CreateEnvironmentOptions) (*Poller[*Environment], error) {
	if opts.Type == "" || opts.CountryCode == "" {
		return nil, errors.New("bcadmin: CreateEnvironmentOptions.Type and CountryCode are required")
	}
	return s.startEnvironmentOperation(withNonIdempotent(ctx), minorTrackableCreate, http.MethodPut,
		"applications/{}/environments/{}", []string{family, name}, opts, family, name)
}

// Copy copies the environment source to a new environment target of type
// targetType and returns a poller whose result is the new environment. Needs
// API v2.9 or later, which returns the operation.
//
// POST /applications/{applicationFamily}/environments/{sourceEnvironmentName}/copy.
func (s *EnvironmentsService) Copy(ctx context.Context, family, source, target string, targetType EnvironmentType) (*Poller[*Environment], error) {
	if target == "" || targetType == "" {
		return nil, errors.New("bcadmin: target name and type are required")
	}
	body := struct {
		EnvironmentName string          `json:"environmentName"`
		Type            EnvironmentType `json:"type"`
	}{target, targetType}
	return s.startEnvironmentOperation(ctx, minorTrackableCreate, http.MethodPost,
		"applications/{}/environments/{}/copy", []string{family, source}, body, family, target)
}

// Delete deletes an environment and returns a poller for the deletion. Needs
// API v2.9 or later, which returns the operation. Since
// API v2.18 the environment is soft deleted and can be recovered for 14 days,
// unless its status is CreatingFailed or RemovingFailed.
//
// DELETE /applications/{applicationFamily}/environments/{environmentName}.
func (s *EnvironmentsService) Delete(ctx context.Context, family, name string) (*Poller[*Operation], error) {
	op, err := s.startOperation(withNonIdempotent(ctx), minorTrackableCreate, http.MethodDelete,
		"applications/{}/environments/{}", []string{family, name}, nil)
	if err != nil {
		return nil, err
	}
	return newPoller(s.c, op, s.operationFetcher(family, name, op.ID), operationResult), nil
}

// Recover recovers a soft-deleted environment and returns a poller whose
// result is the recovered environment. Available since API v2.18.
//
// POST /applications/{applicationFamily}/environments/{environmentName}/recover.
func (s *EnvironmentsService) Recover(ctx context.Context, family, name string) (*Poller[*Environment], error) {
	return s.startEnvironmentOperation(ctx, minorRecover, http.MethodPost,
		"applications/{}/environments/{}/recover", []string{family, name}, nil, family, name)
}

// Rename renames an environment and returns a poller whose result is the
// renamed environment. The endpoint exists since API v2.3; this client needs
// v2.7 or later to track the operation.
//
// POST /applications/{applicationFamily}/environments/{environmentName}/rename.
func (s *EnvironmentsService) Rename(ctx context.Context, family, name, newName string) (*Poller[*Environment], error) {
	if newName == "" {
		return nil, errors.New("bcadmin: new environment name is required")
	}
	body := struct {
		NewEnvironmentName string `json:"NewEnvironmentName"`
	}{newName}
	return s.startEnvironmentOperation(ctx, minorTrackableRename, http.MethodPost,
		"applications/{}/environments/{}/rename", []string{family, name}, body, family, newName)
}

// Restore restores the environment name to opts.PointInTime into the new
// environment opts.EnvironmentName and returns a poller whose result is the
// new environment. The endpoint exists since API v2.4; this client needs v2.6
// or later to track the operation.
//
// POST /applications/{applicationFamily}/environments/{environmentName}/restore.
func (s *EnvironmentsService) Restore(ctx context.Context, family, name string, opts RestoreOptions) (*Poller[*Environment], error) {
	if opts.EnvironmentName == "" || opts.EnvironmentType == "" || opts.PointInTime.IsZero() {
		return nil, errors.New("bcadmin: RestoreOptions.EnvironmentName, EnvironmentType and PointInTime are required")
	}
	type wire RestoreOptions
	body := struct {
		wire
		PointInTime string `json:"PointInTime"`
	}{wire(opts), opts.PointInTime.UTC().Format(time.RFC3339)}
	return s.startEnvironmentOperation(ctx, minorTrackableOperations, http.MethodPost,
		"applications/{}/environments/{}/restore", []string{family, name}, body, family, opts.EnvironmentName)
}

// RestorePeriods returns the periods the environment can be restored to, in
// ascending order. Available since API v2.4.
//
// GET /applications/{applicationFamily}/environments/{environmentName}/availableRestorePeriods.
func (s *EnvironmentsService) RestorePeriods(ctx context.Context, family, name string) ([]RestorePeriod, error) {
	return listAll[RestorePeriod](ctx, s.c, "applications/{}/environments/{}/availableRestorePeriods", family, name)
}

// UsedStorage returns the database storage used by the environment. Before
// API v2.26 the value is -1 for databases larger than 2 TB.
//
// GET /applications/{applicationFamily}/environments/{environmentName}/usedstorage.
func (s *EnvironmentsService) UsedStorage(ctx context.Context, family, name string) (*UsedStorage, error) {
	var u UsedStorage
	if err := s.c.call(ctx, http.MethodGet, "applications/{}/environments/{}/usedstorage", []string{family, name}, nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// Operations returns the operations of the environment. Available since API
// v2.6.
//
// GET /applications/{applicationFamily}/environments/{environmentName}/operations.
func (s *EnvironmentsService) Operations(ctx context.Context, family, name string) ([]Operation, error) {
	return listAll[Operation](ctx, s.c, "applications/{}/environments/{}/operations", family, name)
}

// GetOperation returns one operation of the environment. Available since API
// v2.28.
//
// GET /applications/{applicationFamily}/environments/{environmentName}/operations/{operationId}.
func (s *EnvironmentsService) GetOperation(ctx context.Context, family, name, operationID string) (*Operation, error) {
	var op Operation
	if err := s.c.call(ctx, http.MethodGet, "applications/{}/environments/{}/operations/{}", []string{family, name, operationID}, nil, &op); err != nil {
		return nil, err
	}
	return &op, nil
}

// ResumeOperation returns a poller for an operation of the environment that
// was started earlier, for example by another process. Its result is the
// final operation. The poller only finds operations of this environment; use
// [TenantService.ResumeOperation] for an operation whose environment has
// been renamed or deleted since.
func (s *EnvironmentsService) ResumeOperation(family, name, operationID string) (*Poller[*Operation], error) {
	if _, err := path("applications/{}/environments/{}/operations/{}", family, name, operationID); err != nil {
		return nil, err
	}
	return newPoller(s.c, &Operation{ID: operationID}, s.scopedOperationFetcher(family, name, operationID), operationResult), nil
}

// scopedOperationFetcher returns a function that requests an operation only
// if it belongs to the environment family/name: with API v2.28 and later
// through the environment-scoped GET, before through the environment's
// operation list.
func (s *EnvironmentsService) scopedOperationFetcher(family, name, id string) func(context.Context) (*Operation, error) {
	return func(ctx context.Context) (*Operation, error) {
		if s.c.apiVersionAtLeast(2, 28) {
			return s.GetOperation(ctx, family, name, id)
		}
		ops, err := s.Operations(ctx, family, name)
		if err != nil {
			return nil, err
		}
		for i := range ops {
			if asciiEqualFold(ops[i].ID, id) {
				return &ops[i], nil
			}
		}
		return nil, fmt.Errorf("bcadmin: operation %s not found in the operations of environment %s: %w", clean(id), clean(name), ErrNotFound)
	}
}

// MatchProductionConfigurationMetrics tells whether the sandbox can get the
// database configuration of a production environment now, and returns the
// monthly quota. It does not use up quota.
//
// GET /applications/{applicationFamily}/environments/{environmentName}/matchProductionConfiguration/metrics.
func (s *EnvironmentsService) MatchProductionConfigurationMetrics(ctx context.Context, family, name string) (*MatchProductionConfigurationMetrics, error) {
	var m MatchProductionConfigurationMetrics
	if err := s.c.call(ctx, http.MethodGet, "applications/{}/environments/{}/matchProductionConfiguration/metrics", []string{family, name}, nil, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// MatchProductionConfiguration applies the database configuration of a
// typical production environment to the sandbox for a limited time (72
// hours) and returns a poller for the operation. When the period ends, the
// service returns the sandbox to its configuration automatically.
//
// POST /applications/{applicationFamily}/environments/{environmentName}/matchProductionConfiguration.
func (s *EnvironmentsService) MatchProductionConfiguration(ctx context.Context, family, name string) (*Poller[*Operation], error) {
	op, err := s.startOperation(ctx, minorTrackableCreate, http.MethodPost,
		"applications/{}/environments/{}/matchProductionConfiguration", []string{family, name}, nil)
	if err != nil {
		return nil, err
	}
	return newPoller(s.c, op, s.operationFetcher(family, name, op.ID), operationResult), nil
}

// LinkPowerPlatform links the environment to the Power Platform environment
// powerPlatformEnvironmentID of the same type. Available since API v2.21.
// This endpoint does not accept app-only (service-to-service) tokens.
//
// POST /bap/applications/{applicationFamily}/environments/{environmentName}/linkEnvironment.
func (s *EnvironmentsService) LinkPowerPlatform(ctx context.Context, family, name, powerPlatformEnvironmentID string) error {
	return s.powerPlatform(ctx, "linkEnvironment", family, name, powerPlatformEnvironmentID)
}

// UnlinkPowerPlatform unlinks the environment from the Power Platform
// environment powerPlatformEnvironmentID. Available since API v2.21. This
// endpoint does not accept app-only (service-to-service) tokens.
//
// POST /bap/applications/{applicationFamily}/environments/{environmentName}/unlinkEnvironment.
func (s *EnvironmentsService) UnlinkPowerPlatform(ctx context.Context, family, name, powerPlatformEnvironmentID string) error {
	return s.powerPlatform(ctx, "unlinkEnvironment", family, name, powerPlatformEnvironmentID)
}

func (s *EnvironmentsService) powerPlatform(ctx context.Context, action, family, name, id string) error {
	if id == "" {
		return errors.New("bcadmin: Power Platform environment ID is required")
	}
	p, err := path("bap/applications/{}/environments/{}/"+action, family, name)
	if err != nil {
		return err
	}
	u, err := s.c.resolve(p + "?powerPlatformEnvironmentId=" + url.QueryEscape(id))
	if err != nil {
		return err
	}
	return s.c.do(ctx, http.MethodPost, u, nil, nil)
}

// Minimum API versions (v2.x) of the operation-starting endpoints with which
// the client can track the operation.
const (
	// Create, copy and delete return an operation since v2.9; before, they
	// answer without one.
	minorTrackableCreate = 9
	// Restore (v2.4) returns an operation, but operations can only be polled
	// since v2.6.
	minorTrackableOperations = 6
	// Rename (v2.3) needs the operations of all environments (v2.7), because
	// the environment's name changes while the operation runs.
	minorTrackableRename = 7
	// Recover exists since v2.18.
	minorRecover = 18
)

// startOperation sends a request that starts an environment operation and
// returns the operation from the response. It refuses to send the request
// when the client's API version is older than v2.minMinor, because the
// request would be accepted but could not be tracked, and a retry by the
// caller would start a second operation.
func (s *EnvironmentsService) startOperation(ctx context.Context, minMinor int, method, template string, segments []string, body any) (*Operation, error) {
	if !s.c.apiVersionAtLeast(2, minMinor) {
		return nil, fmt.Errorf("bcadmin: %s %s needs API version v2.%d or later to track the operation, client uses %s", method, template, minMinor, s.c.apiVersion)
	}
	var op Operation
	if err := s.c.call(ctx, method, template, segments, body, &op); err != nil {
		return nil, err
	}
	if op.ID == "" {
		return nil, fmt.Errorf("bcadmin: %s %s was accepted but the response contains no operation to track; check the environment's operations before trying again", method, template)
	}
	return &op, nil
}

// startEnvironmentOperation starts an operation whose result is the
// environment family/resultName.
func (s *EnvironmentsService) startEnvironmentOperation(ctx context.Context, minMinor int, method, template string, segments []string, body any, family, resultName string) (*Poller[*Environment], error) {
	op, err := s.startOperation(ctx, minMinor, method, template, segments, body)
	if err != nil {
		return nil, err
	}
	// segments[1] is the environment of the request path, which lists the
	// operation; the result environment may have another name (copy, rename,
	// restore).
	return newPoller(s.c, op, s.operationFetcher(family, segments[1], op.ID, resultName), s.environmentResult(family, resultName)), nil
}

// operationFetcher returns a function that requests the current state of an
// operation. With API v2.28 and later it uses GET environments/operations/{id},
// which works across renames. Before, it searches the operation lists of the
// environments names, in order, skipping environments that no longer exist,
// and then the operations of all environments (v2.7), which also covers
// environments that were deleted. Environment operation lists exist since
// API v2.6.
func (s *EnvironmentsService) operationFetcher(family, name, id string, more ...string) func(context.Context) (*Operation, error) {
	names := append([]string{name}, more...)
	return func(ctx context.Context) (*Operation, error) {
		if s.c.apiVersionAtLeast(2, 28) {
			return s.c.Tenant().GetOperation(ctx, id)
		}
		for _, n := range names {
			ops, err := s.Operations(ctx, family, n)
			if IsNotFound(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			for i := range ops {
				if asciiEqualFold(ops[i].ID, id) {
					return &ops[i], nil
				}
			}
		}
		if s.c.apiVersionAtLeast(2, 7) {
			ops, err := s.c.Tenant().Operations(ctx)
			if err != nil {
				return nil, err
			}
			for i := range ops {
				if asciiEqualFold(ops[i].ID, id) {
					return &ops[i], nil
				}
			}
		}
		return nil, fmt.Errorf("bcadmin: operation %s not found in the operations of environment %s: %w", clean(id), clean(name), ErrNotFound)
	}
}

// environmentResult returns the result function of pollers whose result is
// the environment family/name.
func (s *EnvironmentsService) environmentResult(family, name string) func(context.Context, *Operation, *WaitOptions) (*Environment, error) {
	return func(ctx context.Context, _ *Operation, opts *WaitOptions) (*Environment, error) {
		waiting := "environment " + name
		env, err := s.Get(ctx, family, name)
		if err != nil && ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return nil, waitError(ctx, waiting)
		}
		if err != nil || opts == nil || !opts.UntilEnvironmentActive {
			return env, err
		}
		freq := s.c.pollFrequency
		if opts.Frequency > 0 {
			freq = opts.Frequency
		}
		timer := time.NewTimer(freq)
		defer timer.Stop()
		for !env.Status.Is(EnvironmentStatusActive) {
			if env.Status.Failed() {
				return env, fmt.Errorf("bcadmin: environment %s has status %s", clean(env.Name), clean(string(env.Status)))
			}
			timer.Reset(freq)
			select {
			case <-ctx.Done():
				return env, waitError(ctx, "environment "+name+" to become active")
			case <-timer.C:
			}
			if env, err = s.Get(ctx, family, name); err != nil {
				if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
					return nil, waitError(ctx, "environment "+name+" to become active")
				}
				return nil, err
			}
		}
		return env, nil
	}
}
