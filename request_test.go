package bcadmin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"

	"github.com/axiansinfoma/go-bcadmin"
	"github.com/axiansinfoma/go-bcadmin/bcadmintest"
)

func TestDo(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.Handle(http.MethodPost, "custom/{name}/action", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			bcadmintest.WriteError(w, http.StatusBadRequest, "BadJSON", err.Error())
			return
		}
		bcadmintest.WriteJSON(w, http.StatusOK, map[string]any{"name": r.PathValue("name"), "echo": in["value"], "q": r.URL.Query().Get("x")})
	})
	c := srv.NewClient(t)
	ctx := context.Background()

	var out struct {
		Name string `json:"name"`
		Echo string `json:"echo"`
		Q    string `json:"q"`
	}
	if err := c.Do(ctx, http.MethodPost, "custom/my%20env/action?x=1", map[string]string{"value": "hello"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Name != "my env" || out.Echo != "hello" || out.Q != "1" {
		t.Errorf("out = %+v", out)
	}
	req, _ := srv.LastRequest()
	if req.Path != "/admin/v2.29/custom/my%20env/action" || req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("request = %s %s", req.Path, req.Header.Get("Content-Type"))
	}

	for _, p := range []string{"", "/admin/v2.29/x", "https://evil.example.com/x", "../x", "a/../../x"} {
		if err := c.Do(ctx, http.MethodGet, p, nil, nil); err == nil {
			t.Errorf("Do(%q) did not fail", p)
		}
	}

	// PUT through Do is not retried on 500: Do cannot know whether it creates
	// something.
	srv.Fail(bcadmintest.Fault{Method: http.MethodPut, Path: "put/x", Status: http.StatusInternalServerError, Times: 1})
	srv.HandleJSON(http.MethodPut, "put/x", http.StatusOK, map[string]any{})
	before := len(srv.Requests())
	if err := c.Do(ctx, http.MethodPut, "put/x", map[string]string{}, nil); err == nil {
		t.Error("PUT through Do was retried after 500")
	}
	if n := len(srv.Requests()) - before; n != 1 {
		t.Errorf("PUT attempts = %d, want 1", n)
	}

	// Unregistered path: the server's 404 body is passed through.
	err := c.Do(ctx, http.MethodGet, "nothing/here", nil, nil)
	if !bcadmin.IsNotFound(err) || !strings.Contains(err.Error(), "no handler for GET") {
		t.Errorf("Do() error = %v", err)
	}
}

func TestDecodeErrorIsReported(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.Handle(http.MethodGet, "environments/quotas", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"environmentsCount": "many"}`))
	})
	_, err := srv.NewClient(t).Tenant().Quotas(context.Background())
	if err == nil || !strings.Contains(err.Error(), "decoding response of GET") {
		t.Errorf("error = %v", err)
	}
}

func TestRetries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		method    string
		status    int
		header    http.Header
		opt       func(*bcadmin.ClientOptions)
		wantCalls int
		wantOK    bool
	}{
		{name: "GET 500 retried", method: http.MethodGet, status: http.StatusInternalServerError, wantCalls: 2, wantOK: true},
		{name: "GET 429 retried", method: http.MethodGet, status: http.StatusTooManyRequests, header: http.Header{"Retry-After": {"0"}}, wantCalls: 2, wantOK: true},
		{name: "GET 400 not retried", method: http.MethodGet, status: http.StatusBadRequest, wantCalls: 1},
		{name: "POST 429 retried", method: http.MethodPost, status: http.StatusTooManyRequests, wantCalls: 2, wantOK: true},
		{name: "POST 503 with Retry-After retried", method: http.MethodPost, status: http.StatusServiceUnavailable, header: http.Header{"Retry-After": {"0"}}, wantCalls: 2, wantOK: true},
		{name: "POST 503 with retry-after-ms retried", method: http.MethodPost, status: http.StatusServiceUnavailable, header: http.Header{"Retry-After-Ms": {"0"}}, wantCalls: 2, wantOK: true},
		{name: "POST 503 without Retry-After not retried", method: http.MethodPost, status: http.StatusServiceUnavailable, wantCalls: 1},
		{name: "POST 500 not retried", method: http.MethodPost, status: http.StatusInternalServerError, wantCalls: 1},
		{name: "PATCH 502 not retried", method: http.MethodPatch, status: http.StatusBadGateway, wantCalls: 1},
		{
			name: "POST 500 retried when allowed", method: http.MethodPost, status: http.StatusInternalServerError, wantCalls: 2, wantOK: true,
			opt: func(o *bcadmin.ClientOptions) { o.RetryNonIdempotent = true },
		},
		{
			name: "POST 429 not retried when excluded from StatusCodes", method: http.MethodPost, status: http.StatusTooManyRequests, wantCalls: 1,
			opt: func(o *bcadmin.ClientOptions) { o.Retry.StatusCodes = []int{http.StatusInternalServerError} },
		},
		{
			name: "user ShouldRetry consulted for POST", method: http.MethodPost, status: http.StatusTooManyRequests, wantCalls: 1,
			opt: func(o *bcadmin.ClientOptions) {
				o.Retry.ShouldRetry = func(*http.Response, error) bool { return false }
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := bcadmintest.NewServer(t)
			var calls atomic.Int32
			srv.Handle(tt.method, "x", func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				bcadmintest.WriteJSON(w, http.StatusOK, map[string]any{})
			})
			srv.Fail(bcadmintest.Fault{Method: tt.method, Path: "x", Status: tt.status, Header: tt.header, Body: `{"code":"Transient","message":"try again"}`, Times: 1})
			opts := srv.ClientOptions()
			if tt.opt != nil {
				tt.opt(opts)
			}
			c, err := bcadmin.NewClient(bcadmintest.Credential(), opts)
			if err != nil {
				t.Fatal(err)
			}
			err = c.Do(context.Background(), tt.method, "x", nil, nil)
			if (err == nil) != tt.wantOK {
				t.Errorf("Do() error = %v, want success %v", err, tt.wantOK)
			}
			if got := len(srv.Requests()); got != tt.wantCalls {
				t.Errorf("requests = %d, want %d", got, tt.wantCalls)
			}
			if !tt.wantOK && !strings.Contains(err.Error(), "Transient: try again") {
				t.Errorf("error %q does not carry the API message", err)
			}
		})
	}
}

// countingTransport fails every request with err and counts the attempts.
type countingTransport struct {
	err   error
	calls atomic.Int32
}

func (c *countingTransport) Do(*http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return nil, c.err
}

func TestRetryTransportErrors(t *testing.T) {
	t.Parallel()
	dial := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	read := &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")}
	tests := []struct {
		name      string
		method    string
		err       error
		wantCalls int32
	}{
		{name: "GET read error retried", method: http.MethodGet, err: read, wantCalls: 3},
		{name: "POST dial error retried", method: http.MethodPost, err: dial, wantCalls: 3},
		{name: "POST read error not retried", method: http.MethodPost, err: read, wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tr := &countingTransport{err: tt.err}
			c, err := bcadmin.NewClient(bcadmintest.Credential(), &bcadmin.ClientOptions{ClientOptions: azcore.ClientOptions{
				Transport: tr,
				Retry:     policy.RetryOptions{MaxRetries: 2, RetryDelay: time.Millisecond, MaxRetryDelay: 10 * time.Millisecond},
				Cloud: cloud.Configuration{Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
					bcadmin.ServiceName: {Endpoint: "https://bc.example.com", Audience: "https://bc.example.com"},
				}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Do(context.Background(), tt.method, "x", nil, nil); err == nil {
				t.Fatal("Do() did not fail")
			}
			if got := tr.calls.Load(); got != tt.wantCalls {
				t.Errorf("attempts = %d, want %d", got, tt.wantCalls)
			}
		})
	}
}

func TestPagination(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.Handle(http.MethodGet, "environments/operations", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "":
			bcadmintest.WriteJSON(w, http.StatusOK, map[string]any{
				"value":           []map[string]string{{"id": "1"}, {"id": "2"}},
				"@odata.nextLink": srv.URL() + "/admin/v2.29/environments/operations?page=2",
			})
		case "2":
			bcadmintest.WriteJSON(w, http.StatusOK, map[string]any{
				"value":           []map[string]string{{"id": "3"}},
				"@odata.nextLink": "environments/operations?page=3",
			})
		default:
			bcadmintest.WriteJSON(w, http.StatusOK, map[string]any{"value": []map[string]string{{"id": "4"}}})
		}
	})
	c := srv.NewClient(t)
	ops, err := c.Tenant().Operations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, op := range ops {
		ids = append(ids, op.ID)
	}
	if strings.Join(ids, ",") != "1,2,3,4" {
		t.Errorf("ids = %v", ids)
	}

	// Stopping the iterator early does not request further pages.
	before := len(srv.Requests())
	for op, err := range c.Tenant().AllOperations(context.Background()) {
		if err != nil {
			t.Fatal(err)
		}
		if op.ID == "1" {
			break
		}
	}
	if got := len(srv.Requests()) - before; got != 1 {
		t.Errorf("requests after early break = %d, want 1", got)
	}
}

func TestPaginationRefusesForeignHost(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.HandleJSON(http.MethodGet, "environments/operations", http.StatusOK, map[string]any{
		"value":           []map[string]string{{"id": "1"}},
		"@odata.nextLink": "https://evil.example.com/steal",
	})
	_, err := srv.NewClient(t).Tenant().Operations(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refusing @odata.nextLink") {
		t.Errorf("error = %v", err)
	}
	if n := len(srv.Requests()); n != 1 {
		t.Errorf("requests = %d", n)
	}
}

func TestPaginationErrorPathIsEscaped(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.HandleJSON(http.MethodGet, "environments/operations", http.StatusOK, map[string]any{
		"value":           []map[string]string{{"id": "1"}},
		"@odata.nextLink": "/admin/v2.29/x%1B%5B2J%0Afake%E2%80%AE",
	})
	srv.Handle(http.MethodGet, "{rest...}", func(w http.ResponseWriter, _ *http.Request) {
		bcadmintest.WriteJSON(w, http.StatusOK, map[string]any{"value": "not a list"})
	})
	_, err := srv.NewClient(t).Tenant().Operations(context.Background())
	if err == nil || !strings.Contains(err.Error(), "x%1B%5B2J%0Afake%E2%80%AE") {
		t.Fatalf("error = %v, want the escaped path", err)
	}
	for _, r := range err.Error() {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			t.Errorf("error %q contains %U", err.Error(), r)
		}
	}
}

func TestAuthChallengeTextIsCleaned(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.Fail(bcadmintest.Fault{Path: "environments/quotas", Status: http.StatusUnauthorized, Header: http.Header{
		"Www-Authenticate": {"Bearer error=\"insufficient_claims\", claims=\"not\u202ebase64\u009b\""},
	}})
	_, err := srv.NewClient(t).Tenant().Quotas(context.Background())
	if err == nil || !strings.Contains(err.Error(), "invalid claims") {
		t.Fatalf("error = %v, want azcore's invalid claims error", err)
	}
	for _, r := range err.Error() {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			t.Errorf("error %q contains %U", err.Error(), r)
		}
	}
}

func TestPaginationStopTextIsCleaned(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.HandleJSON(http.MethodGet, "environments/operations", http.StatusOK, map[string]any{
		"value":           []map[string]string{{"id": "1"}},
		"@odata.nextLink": "/admin/v2.29/environments/operations?a=\u202e",
	})
	_, err := srv.NewClient(t).Tenant().Operations(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already visited") {
		t.Fatalf("error = %v", err)
	}
	for _, r := range err.Error() {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			t.Errorf("error %q contains %U", err.Error(), r)
		}
	}
}

func TestPaginationLoop(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.HandleJSON(http.MethodGet, "environments/operations", http.StatusOK, map[string]any{
		"value":           []map[string]string{{"id": "1"}},
		"@odata.nextLink": "/admin/v2.29/environments/operations",
	})
	_, err := srv.NewClient(t).Tenant().Operations(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already visited") {
		t.Errorf("error = %v", err)
	}
	if n := len(srv.Requests()); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
}

func TestBareArrayList(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.HandleJSON(http.MethodGet, "applications/{$}", http.StatusOK, []map[string]any{{"applicationFamily": "BusinessCentral"}})
	fams, err := srv.NewClient(t).Applications().Families(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fams) != 1 || fams[0].ApplicationFamily != bcadmin.FamilyBusinessCentral {
		t.Errorf("families = %+v", fams)
	}
}

func TestContextCanceled(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.HandleJSON(http.MethodGet, "environments/quotas", http.StatusOK, map[string]any{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := srv.NewClient(t).Tenant().Quotas(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}
