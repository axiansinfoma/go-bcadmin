package bcadmin

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

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
			ServiceName: {Endpoint: srv.URL, Audience: "https://test.example.com"},
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

func TestOversizedErrorResponse(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Request-Id", "rid-1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, "<html>"+strings.Repeat("x", 4096)+"</html>")
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv, func(o *ClientOptions) { o.MaxResponseBytes = 1024; o.Retry.MaxRetries = -1 })
	err := c.Do(context.Background(), http.MethodGet, "x", nil, nil)
	re, ok := errors.AsType[*ResponseError](err)
	if !ok || re.StatusCode != http.StatusTooManyRequests || re.RequestID != "rid-1" {
		t.Fatalf("error = %v, want *ResponseError with status 429", err)
	}
	if !errors.Is(err, ErrResponseTooLarge) || !IsThrottled(err) || !strings.Contains(err.Error(), "body not read") {
		t.Errorf("error = %v", err)
	}
}

func TestLimitedBodyAfterError(t *testing.T) {
	t.Parallel()
	b := &limitedBody{rc: io.NopCloser(strings.NewReader("0123456789")), max: 4}
	b.r = io.LimitReader(b.rc, b.max+1)
	buf := make([]byte, 10)
	if n, err := b.Read(buf); n != 4 || !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("first Read = %d, %v", n, err)
	}
	if n, err := b.Read(buf); n != 0 || !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("second Read = %d, %v", n, err)
	}
}

func TestServerTextIsCleanedInErrors(t *testing.T) {
	t.Parallel()
	// A status line with an escape sequence, followed by a body over the limit.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 200 \x1b[31mOK\u202e\r\nContent-Length: 100\r\nConnection: close\r\n\r\n" + strings.Repeat("x", 100))
		_ = buf.Flush()
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv, func(o *ClientOptions) {
		o.MaxResponseBytes = 10
		// HTTP/1.1 only, so the connection can be hijacked.
		tr := srv.Client().Transport.(*http.Transport).Clone()
		tr.ForceAttemptHTTP2 = false
		tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
		o.Transport = &http.Client{Transport: tr}
	})
	err := c.Do(context.Background(), http.MethodGet, "x", nil, nil)
	if err == nil || !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error = %v, want ErrResponseTooLarge", err)
	}
	if !strings.Contains(err.Error(), "(status 200  [31mOK ") {
		t.Errorf("error %q does not name the cleaned status", err)
	}
	assertClean(t, err.Error())

	// A nextLink host with a bidi override.
	_, err = c.nextLink("https://a\u202eb.example.com/x")
	if err == nil {
		t.Fatal("foreign nextLink was accepted")
	}
	assertClean(t, err.Error())
}

func assertClean(t *testing.T, s string) {
	t.Helper()
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			t.Errorf("error %q contains %U", s, r)
		}
	}
}

func TestSharedPooledTransport(t *testing.T) {
	t.Parallel()
	c1, err := NewClient(fakeCred{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := NewClient(fakeCred{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c3, err := c1.ForTenant("00000000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	shared := sharedHTTPClient()
	for i, c := range []*Client{c1, c2, c3} {
		if c.options.Transport != shared {
			t.Errorf("client %d does not use the shared HTTP client", i+1)
		}
	}
	tr := shared.Transport.(*http.Transport)
	if tr.MaxIdleConnsPerHost != defaultMaxIdleConns || tr.MaxIdleConns != defaultMaxIdleConns || !tr.ForceAttemptHTTP2 {
		t.Errorf("pool settings: MaxIdleConns %d, MaxIdleConnsPerHost %d, HTTP/2 %v", tr.MaxIdleConns, tr.MaxIdleConnsPerHost, tr.ForceAttemptHTTP2)
	}
	if tr.TLSClientConfig.MinVersion != tls.VersionTLS12 || shared.CheckRedirect == nil {
		t.Error("shared client lost TLS or redirect settings")
	}
}

func TestNextLinkHostComparison(t *testing.T) {
	t.Parallel()
	c, err := NewClient(fakeCred{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.nextLink("HTTPS://API.BusinessCentral.Dynamics.com/admin/v2.29/x"); err != nil {
		t.Errorf("same host in other case refused: %v", err)
	}
	// U+212A KELVIN SIGN folds to "k" under Unicode rules; it must not match.
	if _, err := c.nextLink("https://api.businesscentral.dynamics.\u212aom/x"); err == nil {
		t.Error("non-ASCII host accepted")
	}
}

func TestUnlimitedResponseSize(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv, func(o *ClientOptions) { o.MaxResponseBytes = math.MaxInt64 })
	var out struct{ OK bool }
	if err := c.Do(context.Background(), http.MethodGet, "x", nil, &out); err != nil || !out.OK {
		t.Errorf("Do() = %v, %+v", err, out)
	}
}

func TestTruncatedErrorBodyKeepsStatus(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 409 Conflict\r\nContent-Length: 500\r\nRequest-Id: rid-2\r\n\r\n{\"code\":\"EnvironmentExists\"")
		_ = buf.Flush()
	}))
	t.Cleanup(srv.Close)
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.ForceAttemptHTTP2 = false
	tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		c := newTestClient(t, srv, func(o *ClientOptions) { o.Transport = &http.Client{Transport: tr}; o.Retry.MaxRetries = -1 })
		err := c.Do(context.Background(), method, "x", nil, nil)
		re, ok := errors.AsType[*ResponseError](err)
		if !ok || re.StatusCode != http.StatusConflict || re.RequestID != "rid-2" || !IsConflict(err) {
			t.Errorf("%s: error = %v, want *ResponseError with 409", method, err)
		}
	}
}

func TestIsConnectErrorProxy(t *testing.T) {
	t.Parallel()
	dial := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	if !isConnectError(&net.OpError{Op: "proxyconnect", Net: "tcp", Err: dial}) {
		t.Error("proxyconnect wrapping dial is not a connect error")
	}
	if isConnectError(&net.OpError{Op: "read", Net: "tcp", Err: errors.New("reset")}) {
		t.Error("read error is a connect error")
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

func TestCallQueryKeepsCallerValues(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "a=1" {
			http.Error(w, "query "+r.URL.RawQuery, http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv, nil)
	q := url.Values{"a": {"1"}, "empty": {""}}
	if err := c.callQuery(context.Background(), http.MethodGet, "x", nil, q, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := q["empty"]; !ok {
		t.Error("callQuery changed the caller's values")
	}
}
