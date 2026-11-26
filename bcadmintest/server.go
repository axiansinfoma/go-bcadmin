// Package bcadmintest provides a fake Business Central Admin Center API for
// testing code that uses the bcadmin package, without network access or
// credentials.
//
// A [Server] is an HTTPS test server. Register handlers for the endpoints a
// test needs with [Server.Handle], create a client with [Server.NewClient],
// and inspect the requests the client sent with [Server.Requests]. Faults
// such as throttling or error bodies can be injected with [Server.Fail].
//
//	srv := bcadmintest.NewServer(t)
//	srv.Handle(http.MethodGet, "environments/quotas", func(w http.ResponseWriter, r *http.Request) {
//		bcadmintest.WriteJSON(w, http.StatusOK, map[string]any{"environmentsCount": map[string]int{"production": 1}})
//	})
//	client := srv.NewClient(t)
package bcadmintest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"

	"github.com/axiansinfoma/go-bcadmin"
)

// Token is the access token the fake credential returns and the server
// expects in the Authorization header.
const Token = "bcadmintest-token"

// Request is a request received by a [Server].
type Request struct {
	Method string
	// Path is the escaped URL path, for example
	// "/admin/v2.29/applications/BusinessCentral/environments/my%20sandbox".
	Path   string
	Query  string
	Header http.Header
	// Body is the request body, cut off after the server's recording limit
	// (see [WithMaxRecordedBody]); handlers always receive the whole body.
	Body []byte
	// BodyTruncated tells whether Body was cut off.
	BodyTruncated bool
}

// DefaultMaxRecordedBody is the number of body bytes a [Server] records per
// request unless [WithMaxRecordedBody] sets another limit.
const DefaultMaxRecordedBody = 1 << 20

// Fault makes a [Server] answer matching requests with a fixed response
// instead of calling the registered handler.
type Fault struct {
	// Method and Path select the requests. Path is relative to the API root
	// and escaped, for example "environments/quotas". An empty Method or Path
	// matches every method or path.
	Method string
	Path   string
	// Status is the HTTP status code of the response. An invalid or zero
	// status is replaced with 500.
	Status int
	// Body is the response body, sent as is.
	Body string
	// Header contains additional response headers, for example
	// "Retry-After-Ms": {"10"} for a fast retry. The client gives up instead
	// of retrying when the requested delay exceeds its Retry.MaxRetryDelay
	// (5 s with ClientOptions).
	Header http.Header
	// Times is how many requests the fault answers. Zero means every request.
	Times int
}

// Server is a fake Admin Center API. It is safe for concurrent use.
type Server struct {
	srv        *httptest.Server
	apiVersion string
	prefix     string
	maxBody    int

	mu       sync.Mutex
	mux      *http.ServeMux
	requests []Request
	faults   []*Fault
}

// Option configures a Server.
type Option func(*Server)

// WithAPIVersion makes the server serve the API under version v instead of
// bcadmin.DefaultAPIVersion.
func WithAPIVersion(v string) Option {
	return func(s *Server) { s.apiVersion = v }
}

// WithMaxRecordedBody sets how many bytes of each request body the server
// keeps in [Server.Requests] (default [DefaultMaxRecordedBody]), so tests
// that upload large files do not keep them in memory. Zero records no body;
// a negative limit records whole bodies.
func WithMaxRecordedBody(n int) Option {
	return func(s *Server) { s.maxBody = n }
}

// NewServer starts a Server and stops it when the test ends.
func NewServer(t testing.TB, opts ...Option) *Server {
	t.Helper()
	s := &Server{apiVersion: bcadmin.DefaultAPIVersion, mux: http.NewServeMux(), maxBody: DefaultMaxRecordedBody}
	for _, o := range opts {
		o(s)
	}
	s.prefix = "/admin/" + s.apiVersion + "/"
	s.srv = httptest.NewTLSServer(http.HandlerFunc(s.serveHTTP))
	t.Cleanup(s.srv.Close)
	return s
}

// URL returns the base URL of the server, to be used as the endpoint of the
// bcadmin client.
func (s *Server) URL() string { return s.srv.URL }

// APIVersion returns the API version the server serves.
func (s *Server) APIVersion() string { return s.apiVersion }

// ClientOptions returns client options that point a bcadmin client at the
// server, trust its certificate and use short retry and poll intervals.
// They are meant for tests only: the transport trusts the test server's
// certificate and follows redirects without the default client's
// same-origin restriction.
func (s *Server) ClientOptions() *bcadmin.ClientOptions {
	return &bcadmin.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			APIVersion: s.apiVersion,
			Cloud: cloud.Configuration{Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
				bcadmin.ServiceName: {Endpoint: s.srv.URL, Audience: "https://bcadmintest.example.com"},
			}},
			Transport: s.srv.Client(),
			Retry: policy.RetryOptions{
				RetryDelay:    time.Millisecond,
				MaxRetryDelay: 5 * time.Second,
			},
		},
		PollFrequency: time.Millisecond,
	}
}

// NewClient returns a bcadmin client for the server that authenticates with
// [Credential]. It fails the test if the client cannot be created.
func (s *Server) NewClient(t testing.TB) *bcadmin.Client {
	t.Helper()
	c, err := bcadmin.NewClient(Credential(), s.ClientOptions())
	if err != nil {
		t.Fatalf("bcadmintest: creating client: %v", err)
	}
	return c
}

// Handle registers handler for requests with method to path. path is
// relative to the API root and uses the pattern syntax of [http.ServeMux],
// for example "applications/{family}/environments/{env}". Use "{$}" to match
// a trailing slash exactly, for example "applications/{$}". Registering the
// same method and path twice panics, as with http.ServeMux.
func (s *Server) Handle(method, path string, handler http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mux.HandleFunc(method+" "+s.prefix+strings.TrimPrefix(path, "/"), handler)
}

// HandleJSON registers a handler that answers requests with method to path
// with status and v encoded as JSON.
func (s *Server) HandleJSON(method, path string, status int, v any) {
	s.Handle(method, path, func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, status, v)
	})
}

// Fail adds a fault. Faults are checked in the order they were added, before
// the registered handlers.
func (s *Server) Fail(f Fault) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f.Path = strings.TrimPrefix(f.Path, "/")
	if f.Status < 100 || f.Status > 999 {
		f.Status = http.StatusInternalServerError
	}
	s.faults = append(s.faults, &f)
}

// Requests returns the requests the server received, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.requests))
	copy(out, s.requests)
	return out
}

// LastRequest returns the last request the server received, or false if it
// received none.
func (s *Server) LastRequest() (Request, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		return Request{}, false
	}
	return s.requests[len(s.requests)-1], true
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BadRequest", "bcadmintest: reading body: "+err.Error())
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	recorded, truncated := body, false
	if s.maxBody >= 0 && len(body) > s.maxBody {
		// Copy, so the recorded prefix does not keep the whole body alive.
		recorded, truncated = bytes.Clone(body[:s.maxBody]), true
	}

	s.mu.Lock()
	s.requests = append(s.requests, Request{
		Method:        r.Method,
		Path:          r.URL.EscapedPath(),
		Query:         r.URL.RawQuery,
		Header:        r.Header.Clone(),
		Body:          recorded,
		BodyTruncated: truncated,
	})
	if r.Header.Get("Authorization") != "Bearer "+Token {
		s.mu.Unlock()
		WriteError(w, http.StatusUnauthorized, "Unauthorized", "bcadmintest: missing or wrong bearer token")
		return
	}
	// Faults apply only to authenticated requests, so a rejected request does
	// not use them up.
	fault := s.matchFault(r)
	mux := s.mux
	s.mu.Unlock()
	if fault != nil {
		for k, v := range fault.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(fault.Status)
		_, _ = io.WriteString(w, fault.Body)
		return
	}
	if !strings.HasPrefix(r.URL.Path, s.prefix) {
		WriteError(w, http.StatusNotFound, "NotFound", fmt.Sprintf("bcadmintest: path %s is not below %s", r.URL.Path, s.prefix))
		return
	}
	if _, pattern := mux.Handler(r); pattern == "" {
		WriteError(w, http.StatusNotFound, "NotFound", fmt.Sprintf("bcadmintest: no handler for %s %s", r.Method, r.URL.EscapedPath()))
		return
	}
	mux.ServeHTTP(w, r)
}

// matchFault returns the first fault matching r and consumes one use of it.
// s.mu must be held.
func (s *Server) matchFault(r *http.Request) *Fault {
	rel := strings.TrimPrefix(r.URL.EscapedPath(), s.prefix)
	for i, f := range s.faults {
		if f.Method != "" && !strings.EqualFold(f.Method, r.Method) {
			continue
		}
		if f.Path != "" && f.Path != rel {
			continue
		}
		matched := *f
		if f.Times > 0 {
			f.Times--
			if f.Times == 0 {
				s.faults = append(s.faults[:i], s.faults[i+1:]...)
			}
		}
		return &matched
	}
	return nil
}

// WriteJSON writes v as a JSON response with status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError writes an error response in the format of the Admin Center
// API: {"code": code, "message": message}.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, map[string]string{"code": code, "message": message})
}

// Credential returns an azcore.TokenCredential that always returns [Token].
func Credential() azcore.TokenCredential { return staticCredential{} }

type staticCredential struct{}

// GetToken returns Token, valid for one hour.
func (staticCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: Token, ExpiresOn: time.Now().Add(time.Hour)}, nil
}
