package bcadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"

	"github.com/axiansinfoma/go-bcadmin/internal/pathutil"
)

// Do sends a request to the Admin Center API and is meant for endpoints this
// package does not wrap yet.
//
// path is relative to the versioned API root (see [Client.Endpoint]), for
// example "environments/quotas", and may contain a query string. Escape
// caller-supplied segments with [url.PathEscape]. body, if not nil, is
// encoded with encoding/json and sent as JSON; pass a json.RawMessage to send
// JSON you encoded yourself (a []byte would be encoded as a base64 string).
// out, if not nil, receives the JSON response body. A response with a
// status code of 300 or above is returned as a [*ResponseError].
//
// Because Do cannot know whether a request starts an operation, only GET,
// HEAD and OPTIONS requests are retried on server errors; all other methods
// follow the rules for non-idempotent requests (see
// ClientOptions.RetryNonIdempotent).
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	if err := pathutil.ValidateRelative(path); err != nil {
		return fmt.Errorf("bcadmin: %w", err)
	}
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
	default:
		ctx = withNonIdempotent(ctx)
	}
	u, err := c.resolve(path)
	if err != nil {
		return err
	}
	return c.do(ctx, method, u, body, out)
}

// path builds a path below the API root from template, replacing each "{}"
// with the next escaped segment.
func path(template string, segments ...string) (string, error) {
	p, err := pathutil.Build(template, segments...)
	if err != nil {
		return "", fmt.Errorf("bcadmin: %w", err)
	}
	return p, nil
}

// resolve returns the absolute URL of p, an escaped path relative to the API
// root that may contain a query string.
func (c *Client) resolve(p string) (string, error) {
	u := strings.TrimRight(c.baseURL.String(), "/") + "/" + p
	if _, err := url.Parse(u); err != nil {
		return "", fmt.Errorf("bcadmin: invalid path %q: %w", p, err)
	}
	return u, nil
}

// call sends a request to template/segments and decodes the response into out.
func (c *Client) call(ctx context.Context, method, template string, segments []string, body, out any) error {
	p, err := path(template, segments...)
	if err != nil {
		return err
	}
	u, err := c.resolve(p)
	if err != nil {
		return err
	}
	return c.do(ctx, method, u, body, out)
}

// do sends a request to the absolute URL u. body is encoded as JSON unless it
// is a *rawBody. A successful response is decoded into out when out is not nil
// and the body is not empty.
func (c *Client) do(ctx context.Context, method, u string, body, out any) error {
	req, err := c.newRequest(ctx, method, u, body)
	if err != nil {
		return err
	}
	return c.send(req, out)
}

// rawBody is a request body that is sent as is.
type rawBody struct {
	r           io.ReadSeekCloser
	contentType string
}

// nonIdempotentKey marks a context whose request must be retried like a POST
// even though its method is idempotent by definition, for example a PUT that
// creates an environment.
type nonIdempotentKey struct{}

// withNonIdempotent returns a context that makes newRequest apply the retry
// rules of non-idempotent requests.
func withNonIdempotent(ctx context.Context) context.Context {
	return context.WithValue(ctx, nonIdempotentKey{}, true)
}

// newRequest creates a request. Requests that are not idempotent (POST,
// PATCH, or any request whose ctx was marked with withNonIdempotent) get retry
// options that only retry when the request cannot have been processed. These
// options replace retry options a caller set on ctx with
// policy.WithRetryOptions.
func (c *Client) newRequest(ctx context.Context, method, u string, body any) (*policy.Request, error) {
	marked, _ := ctx.Value(nonIdempotentKey{}).(bool)
	if (marked || !idempotent(method)) && !c.retryNonIdempotent {
		ctx = policy.WithRetryOptions(ctx, c.nonIdempotentRetryOptions())
	}
	req, err := runtime.NewRequest(ctx, method, u)
	if err != nil {
		return nil, fmt.Errorf("bcadmin: %w", err)
	}
	req.Raw().Header.Set("Accept", "application/json")
	switch b := body.(type) {
	case nil:
	case *rawBody:
		if err := req.SetBody(b.r, b.contentType); err != nil {
			return nil, fmt.Errorf("bcadmin: %w", err)
		}
	default:
		if err := runtime.MarshalAsJSON(req, body); err != nil {
			return nil, fmt.Errorf("bcadmin: encoding request body: %w", err)
		}
	}
	return req, nil
}

// send sends req through the pipeline and decodes a successful response
// into out. A response with status >= 300 is returned as *ResponseError. The
// pipeline reads and closes the response body.
func (c *Client) send(req *policy.Request, out any) error {
	resp, err := c.pl.Do(req)
	if err != nil {
		// The pipeline reads the body, so a body that is too large or cannot be
		// read fails here; keep the status and headers of the response.
		if re, ok := errors.AsType[responseError](err); ok {
			if r := re.response(); r != nil {
				_ = r.Body.Close()
				if acceptedRaw(r, out) {
					return nil
				}
				return responseReadError(req, r, err)
			}
		}
		return cleanErr(fmt.Errorf("bcadmin: %s %s: %w", req.Raw().Method, req.Raw().URL.EscapedPath(), err))
	}
	payload, err := runtime.Payload(resp)
	_ = resp.Body.Close()
	if err != nil && acceptedRaw(resp, out) {
		return nil
	}
	if err != nil {
		return cleanErr(fmt.Errorf("bcadmin: reading response of %s %s (status %s): %w", req.Raw().Method, req.Raw().URL.EscapedPath(), clean(resp.Status), err))
	}
	// 3xx responses only reach this point when a redirect was refused.
	if resp.StatusCode >= http.StatusMultipleChoices {
		if resp.Request == nil {
			resp.Request = req.Raw()
		}
		return newResponseError(resp, payload)
	}
	if r, ok := out.(*rawPayload); ok {
		*r = append((*r)[:0], payload...)
		return nil
	}
	if out != nil && len(bytes.TrimSpace(payload)) > 0 {
		if err := json.Unmarshal(payload, out); err != nil {
			return cleanErr(fmt.Errorf("bcadmin: decoding response of %s %s: %w", req.Raw().Method, req.Raw().URL.EscapedPath(), err))
		}
	}
	return nil
}

// responseReadError builds the error for a response whose body could not be
// read: a *ResponseError (wrapping err) for error statuses, otherwise an
// error naming the status.
func responseReadError(req *policy.Request, resp *http.Response, err error) error {
	if resp.StatusCode >= http.StatusMultipleChoices {
		if resp.Request == nil {
			resp.Request = req.Raw()
		}
		re := newResponseError(resp, nil)
		re.cause = err
		return re
	}
	return cleanErr(fmt.Errorf("bcadmin: %s %s (status %s): %w", req.Raw().Method, req.Raw().URL.EscapedPath(), clean(resp.Status), err))
}

func idempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions:
		return true
	}
	return false
}

// nonIdempotentRetryOptions returns the client's retry options restricted to
// failures where the service did not process the request.
func (c *Client) nonIdempotentRetryOptions() policy.RetryOptions {
	opts := c.retry
	user := opts.ShouldRetry
	opts.ShouldRetry = func(resp *http.Response, err error) bool {
		if err != nil {
			if !isConnectError(err) {
				return false
			}
		} else {
			switch {
			case resp.StatusCode == http.StatusTooManyRequests:
			case resp.StatusCode == http.StatusServiceUnavailable && hasRetryAfter(resp.Header):
			default:
				return false
			}
			if opts.StatusCodes != nil && !runtime.HasStatusCode(resp, opts.StatusCodes...) {
				return false
			}
		}
		if user != nil {
			return user(resp, err)
		}
		return true
	}
	return opts
}

// hasRetryAfter reports whether h contains one of the retry headers azcore
// honors.
func hasRetryAfter(h http.Header) bool {
	return h.Get("Retry-After") != "" || h.Get("retry-after-ms") != "" || h.Get("x-ms-retry-after-ms") != ""
}

// isConnectError reports whether err happened while establishing the
// connection, so the request cannot have reached the service.
func isConnectError(err error) bool {
	// Walk the chain: a failed proxy connection is an OpError "proxyconnect"
	// wrapping the "dial" OpError.
	for e := err; e != nil; e = errors.Unwrap(e) {
		if opErr, ok := e.(*net.OpError); ok && (opErr.Op == "dial" || opErr.Op == "proxyconnect") { //nolint:errorlint // the loop unwraps the chain itself
			return true
		}
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

// page is one page of a list response.
type page[T any] struct {
	Value    []T    `json:"value"`
	NextLink string `json:"@odata.nextLink"`
}

// UnmarshalJSON accepts both {"value": [...]} and a bare array, which some
// endpoints return.
func (p *page[T]) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '[' {
		return json.Unmarshal(data, &p.Value)
	}
	type plain page[T]
	return json.Unmarshal(data, (*plain)(p))
}

// maxPages limits the number of pages list follows, as a guard against a
// server that keeps returning further links.
const maxPages = 10000

// list returns an iterator over all items of a list endpoint, following
// @odata.nextLink. Iteration stops after the first error. A link that was
// already visited ends the iteration with an error.
func list[T any](ctx context.Context, c *Client, template string, segments ...string) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		p, err := path(template, segments...)
		if err != nil {
			yield(zero, err)
			return
		}
		next, err := c.resolve(p)
		if err != nil {
			yield(zero, err)
			return
		}
		visited := map[string]bool{}
		for next != "" {
			if visited[next] || len(visited) >= maxPages {
				yield(zero, cleanErr(fmt.Errorf("bcadmin: stopping pagination at %s: link was already visited or more than %d pages", next, maxPages)))
				return
			}
			visited[next] = true
			var pg page[T]
			if err := c.do(ctx, http.MethodGet, next, nil, &pg); err != nil {
				yield(zero, err)
				return
			}
			for _, v := range pg.Value {
				if !yield(v, nil) {
					return
				}
			}
			next, err = c.nextLink(pg.NextLink)
			if err != nil {
				yield(zero, err)
				return
			}
		}
	}
}

// listAll collects all items of a list endpoint.
func listAll[T any](ctx context.Context, c *Client, template string, segments ...string) ([]T, error) {
	items := []T{}
	for v, err := range list[T](ctx, c, template, segments...) {
		if err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, nil
}

// asciiEqualFold compares a and b case-insensitively for ASCII letters only;
// strings with other non-ASCII characters never match, so Unicode case
// folding cannot make two different hosts compare equal.
func asciiEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= utf8.RuneSelf || cb >= utf8.RuneSelf {
			return false
		}
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// nextLink validates an @odata.nextLink value. It must point to the same
// scheme and host as the client's endpoint so the bearer token is never sent
// elsewhere.
func (c *Client) nextLink(link string) (string, error) {
	if link == "" {
		return "", nil
	}
	// Resolve relative links below the versioned API root, not next to it.
	root := *c.baseURL
	root.Path = strings.TrimRight(root.Path, "/") + "/"
	root.RawPath = ""
	u, err := root.Parse(link)
	if err != nil {
		return "", fmt.Errorf("bcadmin: invalid @odata.nextLink %q: %w", link, err)
	}
	if !asciiEqualFold(u.Scheme, c.baseURL.Scheme) || !asciiEqualFold(u.Host, c.baseURL.Host) {
		return "", fmt.Errorf("bcadmin: refusing @odata.nextLink to %q, which is not the API endpoint", clean(u.Scheme+"://"+u.Host))
	}
	return u.String(), nil
}

// bodyReaderSource is a request body that can hand out independent readers
// of itself, such as the multipart body of a PTE upload.
type bodyReaderSource interface {
	NewReader() io.ReadCloser
}

// freshBodyPolicy gives every attempt of a request whose body is a
// bodyReaderSource its own reader. azcore rewinds and reuses one body for
// all attempts, but net/http may still be writing the body of an abandoned
// attempt (for example after an early 429) when the retry starts; separate
// readers keep the attempts from interfering.
type freshBodyPolicy struct{}

// Do sets a fresh body reader on the request and sends it.
func (freshBodyPolicy) Do(req *policy.Request) (*http.Response, error) {
	if src, ok := req.Body().(bodyReaderSource); ok {
		raw := req.Raw()
		raw.Body = src.NewReader()
		raw.GetBody = func() (io.ReadCloser, error) { return src.NewReader(), nil }
	}
	return req.Next()
}

// needVersion returns an error if the client's API version is older than
// v2.minMinor, the version in which the endpoint called name was introduced.
func (c *Client) needVersion(name string, minMinor int) error {
	if !c.apiVersionAtLeast(2, minMinor) {
		return fmt.Errorf("bcadmin: %s needs API version v2.%d or later, client uses %s", name, minMinor, c.apiVersion)
	}
	return nil
}

// callQuery is call with a query string; empty query values are left out.
func (c *Client) callQuery(ctx context.Context, method, template string, segments []string, query url.Values, body, out any) error {
	p, err := path(template, segments...)
	if err != nil {
		return err
	}
	q := url.Values{}
	for k, v := range query {
		if len(v) > 1 || (len(v) == 1 && v[0] != "") {
			q[k] = v
		}
	}
	if q := q.Encode(); q != "" {
		p += "?" + q
	}
	u, err := c.resolve(p)
	if err != nil {
		return err
	}
	return c.do(ctx, method, u, body, out)
}

// blank reports whether s is empty or only white space.
func blank(s string) bool { return strings.TrimSpace(s) == "" }

// rawPayload is an out value for send that receives the successful response
// body as is, without decoding, for endpoints whose response is undocumented
// or whose accepted (2xx) response must never become an error.
// A 2xx response whose body cannot be read (too large, connection lost)
// counts as success with an empty payload: the request was accepted, and an
// error would invite repeating it.
type rawPayload []byte

// acceptedRaw reports whether resp is a 2xx response for a rawPayload out
// value, and empties the payload.
func acceptedRaw(resp *http.Response, out any) bool {
	p, ok := out.(*rawPayload)
	if !ok || resp == nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	*p = (*p)[:0]
	return true
}
