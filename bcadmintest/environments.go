package bcadmintest

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/axiansinfoma/go-bcadmin"
)

// Environments is an in-memory fake of the environment endpoints, created
// with [Server.EnableEnvironments]. Operations started through the API
// (create, copy, delete, recover, rename, restore, match production
// configuration) finish after SetPollsUntilDone status requests and then
// change the stored environments. Environments that an operation creates
// (create, copy, restore) appear right away with status Preparing, and get
// status Active when the operation succeeds or CreatingFailed when it fails.
// Like the service, the fake refuses operations on environments that are
// busy with another operation or not Active; where the documentation names
// no error code for that, it uses "environmentOperationInProgress" (409) and
// "invalidEnvironmentStatus" (400). Deleting a CreatingFailed or
// RemovingFailed environment removes it permanently (operation type
// "delete"). It is safe for concurrent use.
type Environments struct {
	mu      sync.Mutex
	envs    map[string]*bcadmin.Environment // key: lower(family)/lower(name)
	ops     []*fakeOperation
	polls   int
	failMsg string
	nextID  int
	// matchUsed counts match production configuration requests this month;
	// matchActive holds the environments that have the configuration.
	matchUsed   int
	matchActive map[string]bool
}

// matchPerMonth is the fake's monthly allowance of match production
// configuration requests.
const matchPerMonth = 5

type fakeOperation struct {
	op       bcadmin.Operation
	family   string
	name     string
	reserves string // environment key reserved until the operation ends
	creates  string // key of the environment the operation creates
	alias    string // other environment name the operation is listed under (rename target)
	fail     string // error message the operation fails with
	polls    int
	apply    func(e *Environments)
}

// EnableEnvironments registers handlers for the environment, environment
// operation and tenant operation endpoints and returns the store behind
// them. Do not register handlers for these paths yourself afterwards.
func (s *Server) EnableEnvironments() *Environments {
	e := &Environments{envs: map[string]*bcadmin.Environment{}, polls: 1, matchActive: map[string]bool{}}
	const env = "applications/{family}/environments/{env}"
	s.Handle(http.MethodGet, "applications/environments", e.handleList)
	s.Handle(http.MethodGet, "applications/{family}/environments", e.handleList)
	s.Handle(http.MethodGet, env, e.handleGet)
	s.Handle(http.MethodPut, env, e.handleCreate)
	s.Handle(http.MethodDelete, env, e.handleDelete)
	s.Handle(http.MethodPost, env+"/copy", e.handleCopy)
	s.Handle(http.MethodPost, env+"/recover", e.handleRecover)
	s.Handle(http.MethodPost, env+"/rename", e.handleRename)
	s.Handle(http.MethodPost, env+"/restore", e.handleRestore)
	s.Handle(http.MethodGet, env+"/availableRestorePeriods", e.handleRestorePeriods)
	s.Handle(http.MethodGet, env+"/usedstorage", e.handleUsedStorage)
	s.Handle(http.MethodGet, env+"/operations", e.handleEnvOperations)
	s.Handle(http.MethodGet, env+"/operations/{id}", e.handleGetOperation)
	s.Handle(http.MethodGet, env+"/matchProductionConfiguration/metrics", e.handleMatchMetrics)
	s.Handle(http.MethodPost, env+"/matchProductionConfiguration", e.handleMatch)
	s.Handle(http.MethodPost, "bap/"+env+"/linkEnvironment", e.handleLink)
	s.Handle(http.MethodPost, "bap/"+env+"/unlinkEnvironment", e.handleLink)
	s.Handle(http.MethodGet, "environments/operations", e.handleAllOperations)
	s.Handle(http.MethodGet, "environments/operations/{id}", e.handleGetOperation)
	return e
}

// SetPollsUntilDone sets how many status requests an operation needs to
// finish. The default is 1: the first status request already reports the
// final status. Values below 1 are treated as 1.
func (e *Environments) SetPollsUntilDone(n int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.polls = max(n, 1)
}

// FailNextOperation makes the next operation that is started end with status
// Failed and the given error message, without changing any environment.
func (e *Environments) FailNextOperation(message string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.failMsg = message
}

// Add stores env, replacing an environment with the same family and name.
// Empty ApplicationFamily defaults to BusinessCentral and empty Status to
// Active.
func (e *Environments) Add(env bcadmin.Environment) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.put(env)
}

// cloneEnvironment returns a copy of env that shares no pointers with it.
func cloneEnvironment(env bcadmin.Environment) bcadmin.Environment {
	cloneTime := func(t *bcadmin.Time) *bcadmin.Time {
		if t == nil {
			return nil
		}
		c := *t
		return &c
	}
	env.SoftDeletedOn = cloneTime(env.SoftDeletedOn)
	env.HardDeletePendingOn = cloneTime(env.HardDeletePendingOn)
	if env.VersionDetails != nil {
		vd := *env.VersionDetails
		vd.GracePeriodStartDate = cloneTime(vd.GracePeriodStartDate)
		vd.EnforcedUpdatePeriodStartDate = cloneTime(vd.EnforcedUpdatePeriodStartDate)
		env.VersionDetails = &vd
	}
	return env
}

func (e *Environments) put(env bcadmin.Environment) {
	env = cloneEnvironment(env)
	if env.ApplicationFamily == "" {
		env.ApplicationFamily = bcadmin.FamilyBusinessCentral
	}
	if env.Status == "" {
		env.Status = bcadmin.EnvironmentStatusActive
	}
	if env.FriendlyName == "" {
		env.FriendlyName = env.Name
	}
	e.envs[envKey(env.ApplicationFamily, env.Name)] = &env
}

// Get returns a copy of the stored environment.
func (e *Environments) Get(family, name string) (bcadmin.Environment, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	env, ok := e.envs[envKey(family, name)]
	if !ok {
		return bcadmin.Environment{}, false
	}
	return cloneEnvironment(*env), true
}

// Operations returns copies of all operations started through the API.
func (e *Environments) Operations() []bcadmin.Operation {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]bcadmin.Operation, len(e.ops))
	for i, o := range e.ops {
		out[i] = cloneOperation(o.op)
	}
	return out
}

// cloneOperation returns a copy of op that shares no pointers or maps with it.
func cloneOperation(op bcadmin.Operation) bcadmin.Operation {
	op.Parameters = maps.Clone(op.Parameters)
	for _, t := range []**bcadmin.Time{&op.CreatedOn, &op.StartedOn, &op.CompletedOn} {
		if *t != nil {
			c := **t
			*t = &c
		}
	}
	return op
}

func envKey(family, name string) string {
	return strings.ToLower(family) + "/" + strings.ToLower(name)
}

// lookup returns the environment of the request's path or writes a 404.
// e.mu must be held.
func (e *Environments) lookup(w http.ResponseWriter, r *http.Request) (*bcadmin.Environment, bool) {
	env, ok := e.envs[envKey(r.PathValue("family"), r.PathValue("env"))]
	if !ok {
		WriteError(w, http.StatusNotFound, "environmentNotFound",
			fmt.Sprintf("Environment '%s/%s' was not found.", r.PathValue("family"), r.PathValue("env")))
	}
	return env, ok
}

// taken reports whether an environment named name exists or is reserved by
// a pending operation. e.mu must be held.
func (e *Environments) taken(family, name string) bool {
	key := envKey(family, name)
	if _, ok := e.envs[key]; ok {
		return true
	}
	for _, o := range e.ops {
		if o.reserves == key && !o.op.Status.Terminal() {
			return true
		}
	}
	return false
}

// belongsTo reports whether the operation is listed under the environment
// family/name: its environment, or the new name of a rename.
func (o *fakeOperation) belongsTo(family, name string) bool {
	return strings.EqualFold(o.family, family) &&
		(strings.EqualFold(o.name, name) || (o.alias != "" && strings.EqualFold(o.alias, name)))
}

// pending returns the running operation of the environment family/name, or
// nil. e.mu must be held.
func (e *Environments) pending(family, name string) *fakeOperation {
	key := envKey(family, name)
	for _, o := range e.ops {
		if !o.op.Status.Terminal() && (o.belongsTo(family, name) || o.creates == key) {
			return o
		}
	}
	return nil
}

// ready checks that env can start an operation: no other operation is
// running on it and it has status Active. It writes the error otherwise.
// e.mu must be held.
func (e *Environments) ready(w http.ResponseWriter, env *bcadmin.Environment) bool {
	if e.pending(env.ApplicationFamily, env.Name) != nil {
		WriteError(w, http.StatusConflict, "environmentOperationInProgress", fmt.Sprintf("Another operation is running on environment '%s'.", env.Name))
		return false
	}
	if !env.Status.Is(bcadmin.EnvironmentStatusActive) {
		WriteError(w, http.StatusBadRequest, "invalidEnvironmentStatus", fmt.Sprintf("Environment '%s' has status %s.", env.Name, env.Status))
		return false
	}
	return true
}

// conflict writes the error for a name that is taken. e.mu must be held.
func conflict(w http.ResponseWriter, name string) {
	WriteError(w, http.StatusConflict, "resourceExists", fmt.Sprintf("An environment named '%s' already exists.", name))
}

// start records a new operation and writes it with status 202. reserve is a
// name the operation reserves until it ends (rename). creates is the
// environment the operation creates (create, copy, restore); like the
// service, the fake shows it with status Preparing while the operation runs,
// Active when it succeeded and CreatingFailed when it failed. e.mu must be
// held.
func (e *Environments) start(w http.ResponseWriter, typ bcadmin.OperationType, family, name string, envType bcadmin.EnvironmentType, reserve string, creates *bcadmin.Environment, params map[string]any, apply func(*Environments)) {
	e.nextID++
	op := bcadmin.Operation{
		ID:              fmt.Sprintf("00000000-0000-0000-0001-%012d", e.nextID),
		Type:            typ,
		Status:          bcadmin.OperationStatusScheduled,
		CreatedOn:       &bcadmin.Time{Time: time.Now().UTC()},
		Parameters:      params,
		EnvironmentName: name,
		EnvironmentType: string(envType),
		ProductFamily:   family,
	}
	fo := &fakeOperation{op: op, family: family, name: name, apply: apply}
	if reserve != "" {
		fo.reserves = envKey(family, reserve)
		fo.alias = reserve
	}
	if creates != nil {
		pre := *creates
		pre.Status = bcadmin.EnvironmentStatusPreparing
		e.put(pre)
		fo.creates = envKey(pre.ApplicationFamily, pre.Name)
	}
	if e.failMsg != "" {
		fo.fail, e.failMsg = e.failMsg, ""
		fo.apply = nil
	}
	e.ops = append(e.ops, fo)
	WriteJSON(w, http.StatusAccepted, fo.op)
}

// advance counts a status request of o and finishes it when due. e.mu must
// be held.
func (e *Environments) advance(o *fakeOperation) {
	if o.op.Status.Terminal() {
		return
	}
	o.polls++
	if o.polls < e.polls {
		o.op.Status = bcadmin.OperationStatusRunning
		return
	}
	now := &bcadmin.Time{Time: time.Now().UTC()}
	o.op.CompletedOn = now
	if o.fail != "" {
		o.op.Status = bcadmin.OperationStatusFailed
		o.op.ErrorMessage = o.fail
		if env, ok := e.envs[o.creates]; ok {
			env.Status = bcadmin.EnvironmentStatusCreatingFailed
		}
		return
	}
	o.op.Status = bcadmin.OperationStatusSucceeded
	if o.apply != nil {
		o.apply(e)
	}
	if env, ok := e.envs[o.creates]; ok {
		env.Status = bcadmin.EnvironmentStatusActive
	}
}

func (e *Environments) handleList(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	family := r.PathValue("family")
	out := []bcadmin.Environment{}
	for _, env := range e.envs {
		if family == "" || strings.EqualFold(env.ApplicationFamily, family) {
			out = append(out, *env)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func (e *Environments) handleGet(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if env, ok := e.lookup(w, r); ok {
		WriteJSON(w, http.StatusOK, env)
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		WriteError(w, http.StatusBadRequest, "requestBodyRequired", "bcadmintest: invalid request body: "+err.Error())
		return false
	}
	return true
}

func (e *Environments) handleCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EnvironmentType    bcadmin.EnvironmentType `json:"environmentType"`
		CountryCode        string                  `json:"countryCode"`
		RingName           string                  `json:"ringName"`
		ApplicationVersion string                  `json:"applicationVersion"`
	}
	if !decode(w, r, &body) {
		return
	}
	family, name := r.PathValue("family"), r.PathValue("env")
	if body.EnvironmentType == "" || body.CountryCode == "" {
		WriteError(w, http.StatusBadRequest, "invalidInput", "environmentType and countryCode are required")
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.taken(family, name) {
		conflict(w, name)
		return
	}
	newEnv := bcadmin.Environment{
		Name: name, ApplicationFamily: family, Type: body.EnvironmentType, CountryCode: body.CountryCode,
		RingName: body.RingName, VersionDetails: &bcadmin.VersionDetails{Version: body.ApplicationVersion},
	}
	e.start(w, bcadmin.OperationTypeCreate, family, name, body.EnvironmentType, "", &newEnv,
		map[string]any{"destinationEnvironmentName": name, "destinationEnvironmentType": string(body.EnvironmentType), "countryCode": body.CountryCode},
		func(e *Environments) { e.put(newEnv) })
}

func (e *Environments) handleDelete(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	env, ok := e.lookup(w, r)
	if !ok {
		return
	}
	family, name, typ := env.ApplicationFamily, env.Name, env.Type
	if o := e.pending(family, name); o != nil {
		if o.op.Type.Is(bcadmin.OperationTypeSoftDelete) || o.op.Type.Is(bcadmin.OperationTypeDelete) {
			WriteError(w, http.StatusConflict, "tenantDeletionInProgress", "The environment is already being deleted.")
		} else {
			WriteError(w, http.StatusConflict, "environmentOperationInProgress", fmt.Sprintf("Another operation is running on environment '%s'.", name))
		}
		return
	}
	// Environments whose creation or removal failed are deleted permanently.
	if env.Status.Is(bcadmin.EnvironmentStatusCreatingFailed) || env.Status.Is(bcadmin.EnvironmentStatusRemovingFailed) {
		e.start(w, bcadmin.OperationTypeDelete, family, name, typ, "", nil, nil,
			func(e *Environments) { delete(e.envs, envKey(family, name)) })
		return
	}
	if !env.Status.Is(bcadmin.EnvironmentStatusActive) {
		WriteError(w, http.StatusBadRequest, "invalidStatusCannotDeleteTenant", fmt.Sprintf("Environment '%s' has status %s.", name, env.Status))
		return
	}
	e.start(w, bcadmin.OperationTypeSoftDelete, family, name, typ, "", nil, map[string]any{"deleteReason": "requestedByAdmin"},
		func(e *Environments) {
			if stored, ok := e.envs[envKey(family, name)]; ok {
				stored.Status = bcadmin.EnvironmentStatusSoftDeleted
				stored.SoftDeletedOn = &bcadmin.Time{Time: time.Now().UTC()}
				stored.HardDeletePendingOn = &bcadmin.Time{Time: time.Now().UTC().Add(14 * 24 * time.Hour)}
				stored.DeleteReason = "requestedByAdmin"
			}
		})
}

func (e *Environments) handleCopy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EnvironmentName string                  `json:"environmentName"`
		Type            bcadmin.EnvironmentType `json:"type"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.EnvironmentName == "" || body.Type == "" {
		WriteError(w, http.StatusBadRequest, "invalidInput", "environmentName and type are required")
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	src, ok := e.lookup(w, r)
	if !ok || !e.ready(w, src) {
		return
	}
	if e.taken(src.ApplicationFamily, body.EnvironmentName) {
		conflict(w, body.EnvironmentName)
		return
	}
	copied := *src
	copied.Name, copied.FriendlyName, copied.Type = body.EnvironmentName, body.EnvironmentName, body.Type
	// Like the service, report the operation for the destination.
	e.start(w, bcadmin.OperationTypeCopy, src.ApplicationFamily, body.EnvironmentName, body.Type, "", &copied,
		map[string]any{"sourceEnvironmentName": src.Name, "destinationEnvironmentName": body.EnvironmentName, "destinationEnvironmentType": string(body.Type)},
		func(e *Environments) { e.put(copied) })
}

func (e *Environments) handleRecover(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	env, ok := e.lookup(w, r)
	if !ok {
		return
	}
	if e.pending(env.ApplicationFamily, env.Name) != nil {
		WriteError(w, http.StatusConflict, "deletedEnvironmentRecoveryInProgress", "An operation is running on the environment.")
		return
	}
	if !env.Status.Is(bcadmin.EnvironmentStatusSoftDeleted) {
		WriteError(w, http.StatusBadRequest, "invalidStatusCannotRecoverDeletedEnvironment", "The environment is not soft deleted.")
		return
	}
	family, name := env.ApplicationFamily, env.Name
	e.start(w, bcadmin.OperationTypeRecover, family, name, env.Type, "", nil, map[string]any{"recoveryReason": "requestedByAdmin"},
		func(e *Environments) {
			if stored, ok := e.envs[envKey(family, name)]; ok {
				stored.Status = bcadmin.EnvironmentStatusActive
				stored.SoftDeletedOn, stored.HardDeletePendingOn, stored.DeleteReason = nil, nil, ""
			}
		})
}

func (e *Environments) handleRename(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NewEnvironmentName string `json:"NewEnvironmentName"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.NewEnvironmentName == "" {
		WriteError(w, http.StatusBadRequest, "invalidInput", "NewEnvironmentName is required")
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	env, ok := e.lookup(w, r)
	if !ok || !e.ready(w, env) {
		return
	}
	family, oldName, newName := env.ApplicationFamily, env.Name, body.NewEnvironmentName
	if e.taken(family, newName) {
		conflict(w, newName)
		return
	}
	e.start(w, bcadmin.OperationTypeEnvironmentRename, family, oldName, env.Type, newName, nil,
		map[string]any{"oldEnvironmentName": oldName, "newEnvironmentName": newName},
		func(e *Environments) {
			if stored, ok := e.envs[envKey(family, oldName)]; ok {
				delete(e.envs, envKey(family, oldName))
				renamed := *stored
				renamed.Name, renamed.FriendlyName = newName, newName
				e.put(renamed)
			}
		})
}

func (e *Environments) handleRestore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EnvironmentName string                  `json:"EnvironmentName"`
		EnvironmentType bcadmin.EnvironmentType `json:"EnvironmentType"`
		PointInTime     string                  `json:"PointInTime"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.EnvironmentName == "" || body.EnvironmentType == "" || body.PointInTime == "" {
		WriteError(w, http.StatusBadRequest, "invalidInput", "EnvironmentName, EnvironmentType and PointInTime are required")
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	src, ok := e.lookup(w, r)
	if !ok || !e.ready(w, src) {
		return
	}
	if e.taken(src.ApplicationFamily, body.EnvironmentName) {
		conflict(w, body.EnvironmentName)
		return
	}
	restored := *src
	restored.Name, restored.FriendlyName, restored.Type = body.EnvironmentName, body.EnvironmentName, body.EnvironmentType
	e.start(w, bcadmin.OperationTypePitRestore, src.ApplicationFamily, body.EnvironmentName, body.EnvironmentType, "", &restored,
		map[string]any{"environmentName": body.EnvironmentName, "environmentType": string(body.EnvironmentType), "restorePointInTime": body.PointInTime},
		func(e *Environments) { e.put(restored) })
}

func (e *Environments) handleRestorePeriods(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.lookup(w, r); !ok {
		return
	}
	now := time.Now().UTC()
	WriteJSON(w, http.StatusOK, map[string]any{"value": []bcadmin.RestorePeriod{{
		From: &bcadmin.Time{Time: now.Add(-30 * 24 * time.Hour)}, To: &bcadmin.Time{Time: now},
	}}})
}

func (e *Environments) handleUsedStorage(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if env, ok := e.lookup(w, r); ok {
		WriteJSON(w, http.StatusOK, bcadmin.UsedStorage{
			EnvironmentType: env.Type, EnvironmentName: env.Name, ApplicationFamily: env.ApplicationFamily,
		})
	}
}

func (e *Environments) handleEnvOperations(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.lookup(w, r); !ok {
		return
	}
	family, name := r.PathValue("family"), r.PathValue("env")
	out := []bcadmin.Operation{}
	for _, o := range e.ops {
		if o.belongsTo(family, name) {
			e.advance(o)
			out = append(out, o.op)
		}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func (e *Environments) handleAllOperations(w http.ResponseWriter, _ *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]bcadmin.Operation, len(e.ops))
	for i, o := range e.ops {
		e.advance(o)
		out[i] = o.op
	}
	WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func (e *Environments) handleGetOperation(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	id, family, name := r.PathValue("id"), r.PathValue("family"), r.PathValue("env")
	for _, o := range e.ops {
		if !strings.EqualFold(o.op.ID, id) {
			continue
		}
		if family != "" && !o.belongsTo(family, name) {
			break
		}
		e.advance(o)
		WriteJSON(w, http.StatusOK, o.op)
		return
	}
	WriteError(w, http.StatusNotFound, "operationNotFound", fmt.Sprintf("Operation '%s' was not found.", id))
}

func (e *Environments) handleMatchMetrics(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if env, ok := e.lookup(w, r); ok {
		m := bcadmin.MatchProductionConfigurationMetrics{
			RequestsPerMonth: matchPerMonth, RequestsUsedThisMonth: e.matchUsed,
			RequestsRemainingThisMonth: matchPerMonth - e.matchUsed, DurationInHours: 72,
		}
		switch {
		case !env.Type.Is(bcadmin.EnvironmentTypeSandbox) || !env.Status.Is(bcadmin.EnvironmentStatusActive):
			m.BlockedReason = "NotEligible"
		case e.matchActive[envKey(env.ApplicationFamily, env.Name)]:
			m.BlockedReason = "AlreadyActive"
		case e.matchUsed >= matchPerMonth:
			m.BlockedReason = "MonthlyLimitReached"
		default:
			m.CanMatchProductionConfiguration = true
		}
		WriteJSON(w, http.StatusOK, m)
	}
}

func (e *Environments) handleMatch(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	env, ok := e.lookup(w, r)
	if !ok || !e.ready(w, env) {
		return
	}
	if !env.Type.Is(bcadmin.EnvironmentTypeSandbox) {
		WriteError(w, http.StatusBadRequest, "productionConfigurationNotEligible", "The environment isn't an active sandbox.")
		return
	}
	key := envKey(env.ApplicationFamily, env.Name)
	if e.matchActive[key] {
		WriteError(w, http.StatusConflict, "productionConfigurationAlreadyActive", "The production configuration is already applied to the environment.")
		return
	}
	if e.matchUsed >= matchPerMonth {
		WriteError(w, http.StatusConflict, "productionConfigurationMonthlyLimitReached", "The monthly request limit has been reached.")
		return
	}
	e.matchUsed++
	e.start(w, bcadmin.OperationTypeMoveToProductionDBConfig, env.ApplicationFamily, env.Name, env.Type, "", nil, nil,
		func(e *Environments) { e.matchActive[key] = true })
}

func (e *Environments) handleLink(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.lookup(w, r); !ok {
		return
	}
	if r.URL.Query().Get("powerPlatformEnvironmentId") == "" {
		WriteError(w, http.StatusBadRequest, "BadArgument", "powerPlatformEnvironmentId is required")
		return
	}
	w.WriteHeader(http.StatusOK)
}
