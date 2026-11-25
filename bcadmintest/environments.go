package bcadmintest

import (
	"encoding/json"
	"fmt"
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
// configuration) finish after PollsUntilDone status requests and then change
// the stored environments. It is safe for concurrent use.
type Environments struct {
	mu      sync.Mutex
	envs    map[string]*bcadmin.Environment // key: lower(family)/lower(name)
	ops     []*fakeOperation
	polls   int
	failMsg string
	nextID  int
}

type fakeOperation struct {
	op     bcadmin.Operation
	family string
	name   string
	polls  int
	apply  func(e *Environments)
}

// EnableEnvironments registers handlers for the environment, environment
// operation and tenant operation endpoints and returns the store behind
// them. Do not register handlers for these paths yourself afterwards.
func (s *Server) EnableEnvironments() *Environments {
	e := &Environments{envs: map[string]*bcadmin.Environment{}, polls: 1}
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

func (e *Environments) put(env bcadmin.Environment) {
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
	return *env, true
}

// Operations returns copies of all operations started through the API.
func (e *Environments) Operations() []bcadmin.Operation {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]bcadmin.Operation, len(e.ops))
	for i, o := range e.ops {
		out[i] = o.op
	}
	return out
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

// start records a new operation and writes it with status 202. e.mu must be
// held.
func (e *Environments) start(w http.ResponseWriter, typ bcadmin.OperationType, family, name string, envType bcadmin.EnvironmentType, params map[string]any, apply func(*Environments)) {
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
	if e.failMsg != "" {
		fo.op.ErrorMessage, e.failMsg = e.failMsg, ""
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
	if o.op.ErrorMessage != "" {
		o.op.Status = bcadmin.OperationStatusFailed
		return
	}
	o.op.Status = bcadmin.OperationStatusSucceeded
	if o.apply != nil {
		o.apply(e)
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
	if _, exists := e.envs[envKey(family, name)]; exists {
		WriteError(w, http.StatusConflict, "resourceExists", fmt.Sprintf("An environment named '%s' already exists.", name))
		return
	}
	newEnv := bcadmin.Environment{
		Name: name, ApplicationFamily: family, Type: body.EnvironmentType, CountryCode: body.CountryCode,
		RingName: body.RingName, VersionDetails: &bcadmin.VersionDetails{Version: body.ApplicationVersion},
	}
	e.start(w, bcadmin.OperationTypeCreate, family, name, body.EnvironmentType,
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
	e.start(w, bcadmin.OperationTypeSoftDelete, family, name, typ, map[string]any{"deleteReason": "requestedByAdmin"},
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
	e.mu.Lock()
	defer e.mu.Unlock()
	src, ok := e.lookup(w, r)
	if !ok {
		return
	}
	if _, exists := e.envs[envKey(src.ApplicationFamily, body.EnvironmentName)]; exists {
		WriteError(w, http.StatusConflict, "resourceExists", fmt.Sprintf("An environment named '%s' already exists.", body.EnvironmentName))
		return
	}
	copied := *src
	copied.Name, copied.FriendlyName, copied.Type = body.EnvironmentName, body.EnvironmentName, body.Type
	e.start(w, bcadmin.OperationTypeCopy, src.ApplicationFamily, src.Name, src.Type,
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
	if !env.Status.Is(bcadmin.EnvironmentStatusSoftDeleted) {
		WriteError(w, http.StatusBadRequest, "invalidStatusCannotRecoverDeletedEnvironment", "The environment is not soft deleted.")
		return
	}
	family, name := env.ApplicationFamily, env.Name
	e.start(w, bcadmin.OperationTypeRecover, family, name, env.Type, map[string]any{"recoveryReason": "requestedByAdmin"},
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
	e.mu.Lock()
	defer e.mu.Unlock()
	env, ok := e.lookup(w, r)
	if !ok {
		return
	}
	family, oldName, newName := env.ApplicationFamily, env.Name, body.NewEnvironmentName
	e.start(w, bcadmin.OperationTypeEnvironmentRename, family, oldName, env.Type,
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
	e.mu.Lock()
	defer e.mu.Unlock()
	src, ok := e.lookup(w, r)
	if !ok {
		return
	}
	restored := *src
	restored.Name, restored.FriendlyName, restored.Type = body.EnvironmentName, body.EnvironmentName, body.EnvironmentType
	e.start(w, bcadmin.OperationTypePitRestore, src.ApplicationFamily, src.Name, src.Type,
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
	family, name := r.PathValue("family"), r.PathValue("env")
	out := []bcadmin.Operation{}
	for _, o := range e.ops {
		if strings.EqualFold(o.family, family) && strings.EqualFold(o.name, name) {
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
		if family != "" && (!strings.EqualFold(o.family, family) || !strings.EqualFold(o.name, name)) {
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
		can := env.Type.Is(bcadmin.EnvironmentTypeSandbox)
		m := bcadmin.MatchProductionConfigurationMetrics{
			RequestsPerMonth: 5, RequestsRemainingThisMonth: 5, CanMatchProductionConfiguration: can, DurationInHours: 72,
		}
		if !can {
			m.BlockedReason = "NotEligible"
		}
		WriteJSON(w, http.StatusOK, m)
	}
}

func (e *Environments) handleMatch(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	env, ok := e.lookup(w, r)
	if !ok {
		return
	}
	if !env.Type.Is(bcadmin.EnvironmentTypeSandbox) {
		WriteError(w, http.StatusBadRequest, "productionConfigurationNotEligible", "The environment isn't an active sandbox.")
		return
	}
	e.start(w, bcadmin.OperationTypeMoveToProductionDBConfig, env.ApplicationFamily, env.Name, env.Type, nil, nil)
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
