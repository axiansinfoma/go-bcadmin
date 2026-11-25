package bcadmin

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
)

// ServiceName identifies the Business Central Admin Center API in a
// [cloud.Configuration]. Set ClientOptions.Cloud.Services[ServiceName] to use
// another endpoint or token audience, for example for a sovereign cloud.
const ServiceName cloud.ServiceName = "bcadmin"

const (
	// DefaultEndpoint is the base URL of the Business Central API in the
	// Azure public cloud.
	DefaultEndpoint = "https://api.businesscentral.dynamics.com"

	// DefaultAudience is the token audience of the Business Central API in
	// the Azure public cloud. Tokens are requested for the scope
	// DefaultAudience + "/.default".
	DefaultAudience = "https://api.businesscentral.dynamics.com"

	// DefaultPollFrequency is the interval between two status requests of a
	// Poller when neither ClientOptions.PollFrequency nor
	// WaitOptions.Frequency is set.
	DefaultPollFrequency = 10 * time.Second
)

var apiVersionPattern = regexp.MustCompile(`^v\d+\.\d+$`)

// ClientOptions configures a [Client]. A nil *ClientOptions is valid and
// selects the defaults.
//
// The embedded [azcore.ClientOptions] configure the HTTP pipeline:
//   - APIVersion selects the Admin Center API version (default
//     [DefaultAPIVersion]), for example "v2.28".
//   - Cloud.Services[ServiceName] overrides the endpoint and token audience.
//   - Retry configures retries of throttled and failed requests. See
//     RetryNonIdempotent for POST and PATCH requests.
//   - Telemetry.ApplicationID is added to the User-Agent header.
//   - Transport, Logging, PerCallPolicies and PerRetryPolicies work as in
//     the Azure SDK for Go. The default transport requires TLS 1.2 and
//     follows redirects only to the same scheme and host; a custom Transport
//     is responsible for its own redirect policy.
//   - InsecureAllowCredentialWithHTTP allows an http:// endpoint, which sends
//     the bearer token in clear text. Use it only for tests.
type ClientOptions struct {
	azcore.ClientOptions

	// RetryNonIdempotent allows retrying POST and PATCH requests (and PUT
	// requests that create resources) on every status code in
	// Retry.StatusCodes and on every transport error. By default they are only
	// retried on 429, on 503 with a Retry-After header, and when the connection
	// could not be established, because the service may already have started
	// the requested operation. For these requests the client's Retry options
	// apply even when a caller sets per-call options with
	// policy.WithRetryOptions.
	RetryNonIdempotent bool

	// PollFrequency is the default interval between two status requests of a
	// Poller. The default is DefaultPollFrequency.
	PollFrequency time.Duration

	// MaxResponseBytes is the largest response body the client reads. A
	// larger response fails with ErrResponseTooLarge. The default is
	// DefaultMaxResponseBytes.
	MaxResponseBytes int64
}

// Client is a client for the Business Central Admin Center API. Create it with
// [NewClient]. A Client is safe for concurrent use.
type Client struct {
	cred               azcore.TokenCredential
	options            ClientOptions
	pl                 runtime.Pipeline
	baseURL            *url.URL // endpoint including /admin/{version}
	apiVersion         string
	retry              policy.RetryOptions
	retryNonIdempotent bool
	pollFrequency      time.Duration
}

// NewClient returns a client that authenticates with cred. options may be nil.
func NewClient(cred azcore.TokenCredential, options *ClientOptions) (*Client, error) {
	if cred == nil {
		return nil, errors.New("bcadmin: credential must not be nil")
	}
	var opts ClientOptions
	if options != nil {
		opts = *options
	}

	apiVersion := opts.APIVersion
	if apiVersion == "" {
		apiVersion = DefaultAPIVersion
	}
	if !apiVersionPattern.MatchString(apiVersion) {
		return nil, fmt.Errorf("bcadmin: invalid API version %q, want a value like %q", apiVersion, DefaultAPIVersion)
	}

	endpoint, audience := DefaultEndpoint, DefaultAudience
	if svc, ok := opts.Cloud.Services[ServiceName]; ok {
		if svc.Endpoint != "" {
			endpoint = svc.Endpoint
		}
		if svc.Audience != "" {
			audience = svc.Audience
		}
	}
	base, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil {
		return nil, fmt.Errorf("bcadmin: invalid endpoint %q: %w", endpoint, err)
	}
	if base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") {
		return nil, fmt.Errorf("bcadmin: invalid endpoint %q: want an absolute https URL", endpoint)
	}
	if base.Scheme == "http" && !opts.InsecureAllowCredentialWithHTTP {
		return nil, fmt.Errorf("bcadmin: endpoint %q uses http; set InsecureAllowCredentialWithHTTP to allow sending the token in clear text", endpoint)
	}
	if base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("bcadmin: invalid endpoint %q: must not contain a query or fragment", endpoint)
	}
	base = base.JoinPath("admin", apiVersion)

	scope := strings.TrimRight(audience, "/") + "/.default"
	if opts.Transport == nil {
		// Keep the default client in opts so ForTenant shares its connections.
		opts.Transport = newDefaultHTTPClient()
	}
	maxResponse := opts.MaxResponseBytes
	if maxResponse <= 0 {
		maxResponse = DefaultMaxResponseBytes
	}
	pipelineOpts := opts.ClientOptions
	pipelineOpts.Transport = &limitTransport{next: opts.Transport, max: maxResponse}
	// The API version is part of the path, not a query parameter.
	pipelineOpts.APIVersion = ""
	pl := runtime.NewPipeline(moduleName, moduleVersion(), runtime.PipelineOptions{
		AllowedHeaders: []string{"request-id", "x-ms-request-id", "ms-correlation-x-ms-id", "x-ms-correlation-id", "Retry-After", "Location"},
		PerRetry: []policy.Policy{
			runtime.NewBearerTokenPolicy(cred, []string{scope}, &policy.BearerTokenOptions{
				InsecureAllowCredentialWithHTTP: opts.InsecureAllowCredentialWithHTTP,
			}),
		},
	}, &pipelineOpts)

	pollFrequency := opts.PollFrequency
	if pollFrequency <= 0 {
		pollFrequency = DefaultPollFrequency
	}

	return &Client{
		cred:               cred,
		options:            opts,
		pl:                 pl,
		baseURL:            base,
		apiVersion:         apiVersion,
		retry:              opts.Retry,
		retryNonIdempotent: opts.RetryNonIdempotent,
		pollFrequency:      pollFrequency,
	}, nil
}

// APIVersion returns the Admin Center API version the client uses, for
// example "v2.29".
func (c *Client) APIVersion() string { return c.apiVersion }

// Endpoint returns the versioned API root the client sends requests to, for
// example "https://api.businesscentral.dynamics.com/admin/v2.29".
func (c *Client) Endpoint() string { return c.baseURL.String() }

// ForTenant returns a client that requests its tokens for the Microsoft Entra
// tenant tenantID instead of the credential's default tenant. Use it to manage
// the environments of several customer tenants with one multi-tenant app
// registration. The credential must allow the tenant, for example through
// AdditionallyAllowedTenants in azidentity.
func (c *Client) ForTenant(tenantID string) (*Client, error) {
	if tenantID == "" {
		return nil, errors.New("bcadmin: tenant ID must not be empty")
	}
	cred := c.cred
	if tc, ok := cred.(*tenantCredential); ok {
		cred = tc.cred
	}
	return NewClient(&tenantCredential{cred: cred, tenantID: tenantID}, &c.options)
}

// tenantCredential requests tokens for a fixed tenant.
type tenantCredential struct {
	cred     azcore.TokenCredential
	tenantID string
}

// GetToken requests a token for the fixed tenant.
func (t *tenantCredential) GetToken(ctx context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	opts.TenantID = t.tenantID
	return t.cred.GetToken(ctx, opts)
}
