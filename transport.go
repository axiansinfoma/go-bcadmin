package bcadmin

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

// DefaultMaxResponseBytes is the largest response body the client reads when
// ClientOptions.MaxResponseBytes is zero.
const DefaultMaxResponseBytes = 64 << 20

// ErrResponseTooLarge is returned when a response body exceeds
// ClientOptions.MaxResponseBytes.
var ErrResponseTooLarge = errors.New("bcadmin: response body too large")

// newDefaultHTTPClient returns the HTTP client used when ClientOptions.Transport
// is nil. It requires TLS 1.2 and follows redirects only to the same scheme and
// host, so the bearer token is never forwarded to another origin.
func newDefaultHTTPClient() *http.Client {
	var tr *http.Transport
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		tr = dt.Clone()
	} else {
		// The application replaced http.DefaultTransport; start from the
		// standard settings instead of using its transport.
		tr = &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		}
	}
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
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
	if req.URL.Scheme != first.Scheme || !strings.EqualFold(req.URL.Host, first.Host) {
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
	resp.Body = &limitedBody{rc: resp.Body, r: io.LimitReader(resp.Body, t.max+1), max: t.max, resp: resp}
	return resp, nil
}

// limitedBody returns ErrResponseTooLarge once more than max bytes were read.
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
	return n, err
}

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
