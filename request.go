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
		// The pipeline reads the body, so a body over MaxResponseBytes fails
		// here; keep the status and headers of the response.
		if tl, ok := errors.AsType[*tooLargeError](err); ok && tl.resp != nil {
			resp := tl.resp
			if resp.StatusCode >= http.StatusMultipleChoices {
				if resp.Request == nil {
					resp.Request = req.Raw()
				}
				re := newResponseError(resp, nil)
				re.cause = err
				return re
			}
			return fmt.Errorf("bcadmin: %s %s (status %s): %w", req.Raw().Method, req.Raw().URL.Path, clean(resp.Status), err)
		}
		return fmt.Errorf("bcadmin: %s %s: %w", req.Raw().Method, req.Raw().URL.Path, err)
	}
	payload, err := runtime.Payload(resp)
	_ = resp.Body.Close()
	if err != nil {
		return fmt.Errorf("bcadmin: reading response of %s %s (status %s): %w", req.Raw().Method, req.Raw().URL.Path, clean(resp.Status), err)
	}
	// 3xx responses only reach this point when a redirect was refused.
	if resp.StatusCode >= http.StatusMultipleChoices {
		if resp.Request == nil {
			resp.Request = req.Raw()
		}
		return newResponseError(resp, payload)
	}
	if out != nil && len(bytes.TrimSpace(payload)) > 0 {
		if err := json.Unmarshal(payload, out); err != nil {
			return fmt.Errorf("bcadmin: decoding response of %s %s: %w", req.Raw().Method, req.Raw().URL.Path, err)
		}
	}
	return nil
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
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
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
				yield(zero, fmt.Errorf("bcadmin: stopping pagination at %s: link was already visited or more than %d pages", next, maxPages))
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
	if !strings.EqualFold(u.Scheme, c.baseURL.Scheme) || !strings.EqualFold(u.Host, c.baseURL.Host) {
		return "", fmt.Errorf("bcadmin: refusing @odata.nextLink to %q, which is not the API endpoint", clean(u.Scheme+"://"+u.Host))
	}
	return u.String(), nil
}
