package bcadmin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

type fakeCred struct{}

func (fakeCred) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "t", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

func newTestClient(t *testing.T, srv *httptest.Server, mut func(*ClientOptions)) *Client {
	t.Helper()
	opts := &ClientOptions{ClientOptions: azcore.ClientOptions{
		Transport: srv.Client(),
		Retry:     policy.RetryOptions{RetryDelay: time.Millisecond, MaxRetryDelay: 10 * time.Millisecond},
		Cloud: cloud.Configuration{Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
			ServiceName: {Endpoint: srv.URL},
		}},
	}}
	if mut != nil {
		mut(opts)
	}
	c, err := NewClient(fakeCred{}, opts)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNonIdempotentMarker(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv, nil)

	// A marked PUT is not retried on 500.
	if err := c.call(withNonIdempotent(context.Background()), http.MethodPut, "x", nil, nil, nil); err == nil {
		t.Fatal("call did not fail")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("marked PUT attempts = %d, want 1", got)
	}
	// An unmarked PUT is retried.
	calls.Store(0)
	_ = c.call(context.Background(), http.MethodPut, "x", nil, nil, nil)
	if got := calls.Load(); got != 4 {
		t.Errorf("unmarked PUT attempts = %d, want 4", got)
	}
}

func TestResponseSizeLimit(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"value":"`+strings.Repeat("x", 2048)+`"}`)
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv, func(o *ClientOptions) { o.MaxResponseBytes = 1024 })
	err := c.Do(context.Background(), http.MethodGet, "x", nil, nil)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("error = %v, want ErrResponseTooLarge", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1 (not retried)", got)
	}
	c = newTestClient(t, srv, func(o *ClientOptions) { o.MaxResponseBytes = 4096 })
	if err := c.Do(context.Background(), http.MethodGet, "x", nil, nil); err != nil {
		t.Errorf("error = %v under the limit", err)
	}
}

func TestRedirects(t *testing.T) {
	t.Parallel()
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("token was sent to another origin")
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(other.Close)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/v2.29/same":
			http.Redirect(w, r, "/admin/v2.29/target", http.StatusFound)
		case "/admin/v2.29/foreign":
			http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
		case "/admin/v2.29/target":
			_, _ = io.WriteString(w, `{"ok":true}`)
		}
	}))
	t.Cleanup(srv.Close)

	// The default HTTP client, trusting the test server's certificate.
	client := newDefaultHTTPClient()
	client.Transport.(*http.Transport).TLSClientConfig.RootCAs = srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	c := newTestClient(t, srv, func(o *ClientOptions) { o.Transport = client })

	var out struct{ OK bool }
	if err := c.Do(context.Background(), http.MethodGet, "same", nil, &out); err != nil || !out.OK {
		t.Errorf("same-origin redirect: %v, %+v", err, out)
	}
	err := c.Do(context.Background(), http.MethodGet, "foreign", nil, nil)
	re, ok := errors.AsType[*ResponseError](err)
	if !ok || re.StatusCode != http.StatusFound {
		t.Errorf("foreign redirect error = %v, want *ResponseError with 302", err)
	}
}

func TestSameOriginRedirectLimit(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodGet, "https://a.example.com/x", nil)
	via := make([]*http.Request, 10)
	for i := range via {
		via[i] = req
	}
	if err := sameOriginRedirect(req, via); err == nil {
		t.Error("11th redirect was allowed")
	}
}
