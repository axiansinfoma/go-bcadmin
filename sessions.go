package bcadmin

import (
	"context"
	"errors"
	"net/http"
	"strconv"
)

// SessionsService manages the active sessions of one environment. Get it
// with [Client.Sessions] or [Environment.Sessions].
type SessionsService struct {
	c           *Client
	family, env string
	err         error // set for services of unbound environments
}

// Sessions returns the session endpoints of the environment
// family/environment.
func (c *Client) Sessions(family, environment string) *SessionsService {
	return &SessionsService{c: c, family: family, env: environment}
}

// Sessions returns the session endpoints of the environment. For an
// environment that is not bound to a client (see [Environment]), every
// method returns an error.
func (e *Environment) Sessions() *SessionsService {
	s, err := e.service()
	if err != nil {
		return &SessionsService{err: err}
	}
	return s.c.Sessions(e.ApplicationFamily, e.Name)
}

// Session is an active session of an environment.
type Session struct {
	EnvironmentName   string `json:"environmentName"`
	ApplicationFamily string `json:"applicationFamily"`
	SessionID         int    `json:"sessionId"`
	// UserID is the user the session belongs to.
	UserID string `json:"userId"`
	// ClientType is the client of the session, for example "WebClient".
	ClientType string `json:"clientType"`
	// LogOnDate is the time the session started, as the service returns it.
	// The documentation types it as a string, not a timestamp, so it is not
	// parsed.
	LogOnDate            string `json:"logOnDate"`
	EntryPointOperation  string `json:"entryPointOperation"`
	EntryPointObjectName string `json:"entryPointObjectName"`
	EntryPointObjectID   string `json:"entryPointObjectId"`
	EntryPointObjectType string `json:"entryPointObjectType"`
	CurrentObjectName    string `json:"currentObjectName"`
	CurrentObjectID      int    `json:"currentObjectId"`
	CurrentObjectType    string `json:"currentObjectType"`
	// CurrentOperationDuration is the duration of the current operation, as
	// returned by the service (the documentation gives no unit).
	CurrentOperationDuration int64 `json:"currentOperationDuration"`
}

// List returns the active sessions of the environment.
//
// GET /applications/{applicationFamily}/environments/{environmentName}/sessions.
func (s *SessionsService) List(ctx context.Context) ([]Session, error) {
	if s.err != nil {
		return nil, s.err
	}
	return listAll[Session](ctx, s.c, "applications/{}/environments/{}/sessions", s.family, s.env)
}

// Get returns one active session.
//
// GET /applications/{applicationFamily}/environments/{environmentName}/sessions/{sessionId}.
func (s *SessionsService) Get(ctx context.Context, sessionID int) (*Session, error) {
	if s.err != nil {
		return nil, s.err
	}
	var out Session
	if err := s.c.call(ctx, http.MethodGet, "applications/{}/environments/{}/sessions/{}",
		[]string{s.family, s.env, strconv.Itoa(sessionID)}, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Terminate stops and deletes an active session. Users of the session lose
// unsaved work.
//
// DELETE /applications/{applicationFamily}/environments/{environmentName}/sessions/{sessionId}.
func (s *SessionsService) Terminate(ctx context.Context, sessionID int) error {
	if s.err != nil {
		return s.err
	}
	if sessionID < 0 {
		return errors.New("bcadmin: session ID must not be negative")
	}
	return s.c.call(ctx, http.MethodDelete, "applications/{}/environments/{}/sessions/{}",
		[]string{s.family, s.env, strconv.Itoa(sessionID)}, nil, nil)
}
