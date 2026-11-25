package bcadmin

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

// DefaultMaxResponseBytes is the largest response body the client reads when
// ClientOptions.MaxResponseBytes is zero.
const DefaultMaxResponseBytes = 64 << 20

// ErrResponseTooLarge is returned when a response body exceeds
// ClientOptions.MaxResponseBytes.
var ErrResponseTooLarge = errors.New("bcadmin: response body too large")

// Connection pool settings of the default transport. All requests go to one
// host, so the per-host idle limit is as large as the total limit; Go's
// default of 2 idle connections per host would close and reopen connections
// under concurrent load, which can exhaust SNAT ports in Azure.
const (
	defaultMaxIdleConns    = 100
	defaultIdleConnTimeout = 90 * time.Second
)

// sharedHTTPClient is the HTTP client of all clients created without
// ClientOptions.Transport. Sharing one connection pool across clients (and
// across ForTenant clients) keeps the number of outbound connections low. It
// is created on first use and never modified afterwards.
var sharedHTTPClient = sync.OnceValue(newDefaultHTTPClient)

// newDefaultHTTPClient returns an HTTP client with a pooled transport. It
// requires TLS 1.2 and follows redirects only to the same scheme and host, so
// the bearer token is never forwarded to another origin.
func newDefaultHTTPClient() *http.Client {
	var tr *http.Transport
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		tr = dt.Clone()
	} else {
		// The application replaced http.DefaultTransport; start from the
		// standard settings instead of using its transport.
		tr = &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		}
	}
	tr.ForceAttemptHTTP2 = true // one multiplexed connection per host where possible
	tr.MaxIdleConns = defaultMaxIdleConns
	tr.MaxIdleConnsPerHost = defaultMaxIdleConns
	tr.IdleConnTimeout = defaultIdleConnTimeout
	// Keep TLS settings the application put on http.DefaultTransport (root
	// CAs, client certificates); only raise the minimum version.
	if tr.TLSClientConfig == nil {
		tr.TLSClientConfig = &tls.Config{}
	} else {
		tr.TLSClientConfig = tr.TLSClientConfig.Clone()
	}
	if tr.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		tr.TLSClientConfig.MinVersion = tls.VersionTLS12
	}
	return &http.Client{Transport: tr, CheckRedirect: sameOriginRedirect}
}

// sameOriginRedirect allows at most 10 redirects to the scheme and host of the
// original request. For other redirects the 3xx response is returned, which
// the client reports as a *ResponseError.
func sameOriginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("bcadmin: stopped after 10 redirects")
	}
	first := via[0].URL
	if !asciiEqualFold(req.URL.Scheme, first.Scheme) || !asciiEqualFold(req.URL.Host, first.Host) {
		return http.ErrUseLastResponse
	}
	return nil
}

// limitTransport limits the size of response bodies.
type limitTransport struct {
	next policy.Transporter
	max  int64
}

// Do sends req and wraps the response body in a size limit.
func (t *limitTransport) Do(req *http.Request) (*http.Response, error) {
	resp, err := t.next.Do(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	if t.max >= math.MaxInt64-1 {
		return resp, nil // effectively unlimited; t.max+1 would overflow
	}
	resp.Body = &limitedBody{rc: resp.Body, r: io.LimitReader(resp.Body, t.max+1), max: t.max, resp: resp}
	return resp, nil
}

// limitedBody returns ErrResponseTooLarge once more than max bytes were read.
// Other read errors are wrapped in a *bodyReadError that keeps the response.
type limitedBody struct {
	rc   io.ReadCloser
	r    io.Reader
	max  int64
	read int64
	err  error
	resp *http.Response
}

// Read reads from the body and fails once the limit is exceeded.
func (b *limitedBody) Read(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	n, err := b.r.Read(p)
	b.read += int64(n)
	if b.read > b.max {
		b.err = &tooLargeError{max: b.max, resp: b.resp}
		return n - int(b.read-b.max), b.err
	}
	if err != nil && !errors.Is(err, io.EOF) {
		b.err = &bodyReadError{err: err, resp: b.resp}
		return n, b.err
	}
	return n, err
}

// responseError is implemented by errors that keep the response whose body
// could not be read, because azcore's bearer token policy drops the response
// when a later policy fails.
type responseError interface {
	error
	response() *http.Response
}

// bodyReadError is a failure while reading a response body, for example a
// connection reset. It is retried by azcore like the underlying error.
type bodyReadError struct {
	err  error
	resp *http.Response
}

// Error returns the underlying error's message.
func (e *bodyReadError) Error() string { return "reading response body: " + e.err.Error() }

// Unwrap returns the underlying error.
func (e *bodyReadError) Unwrap() error { return e.err }

func (e *bodyReadError) response() *http.Response { return e.resp }

// Close closes the underlying body.
func (b *limitedBody) Close() error { return b.rc.Close() }

// tooLargeError matches ErrResponseTooLarge and is not retried by azcore. It
// keeps the response, because azcore's bearer token policy drops the response
// when a later policy fails.
type tooLargeError struct {
	max  int64
	resp *http.Response
}

// Error describes the exceeded limit.
func (e *tooLargeError) Error() string {
	return fmt.Sprintf("%s: more than %d bytes", ErrResponseTooLarge, e.max)
}

// Is reports whether target is ErrResponseTooLarge.
func (e *tooLargeError) Is(target error) bool { return target == ErrResponseTooLarge }

// NonRetriable marks the error as permanent for the azcore retry policy.
func (*tooLargeError) NonRetriable() {}

func (e *tooLargeError) response() *http.Response { return e.resp }
