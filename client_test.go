package bcadmin_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"

	"github.com/axiansinfoma/go-bcadmin"
	"github.com/axiansinfoma/go-bcadmin/bcadmintest"
)

func TestNewClientValidation(t *testing.T) {
	t.Parallel()
	cred := bcadmintest.Credential()
	withEndpoint := func(endpoint string, insecure bool) *bcadmin.ClientOptions {
		return &bcadmin.ClientOptions{ClientOptions: azcore.ClientOptions{
			InsecureAllowCredentialWithHTTP: insecure,
			Cloud: cloud.Configuration{Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
				bcadmin.ServiceName: {Endpoint: endpoint},
			}},
		}}
	}
	tests := []struct {
		name    string
		cred    azcore.TokenCredential
		opts    *bcadmin.ClientOptions
		wantErr string
		wantURL string
	}{
		{name: "defaults", cred: cred, wantURL: "https://api.businesscentral.dynamics.com/admin/v2.29"},
		{name: "nil credential", wantErr: "credential must not be nil"},
		{name: "api version", cred: cred, opts: &bcadmin.ClientOptions{ClientOptions: azcore.ClientOptions{APIVersion: "v2.28"}}, wantURL: "https://api.businesscentral.dynamics.com/admin/v2.28"},
		{name: "bad api version", cred: cred, opts: &bcadmin.ClientOptions{ClientOptions: azcore.ClientOptions{APIVersion: "2.28"}}, wantErr: "invalid API version"},
		{name: "custom endpoint", cred: cred, opts: withEndpoint("https://bc.example.com/", false), wantURL: "https://bc.example.com/admin/v2.29"},
		{name: "http refused", cred: cred, opts: withEndpoint("http://localhost:8080", false), wantErr: "uses http"},
		{name: "http allowed", cred: cred, opts: withEndpoint("http://localhost:8080", true), wantURL: "http://localhost:8080/admin/v2.29"},
		{name: "relative endpoint", cred: cred, opts: withEndpoint("bc.example.com", false), wantErr: "want an absolute https URL"},
		{name: "ftp endpoint", cred: cred, opts: withEndpoint("ftp://bc.example.com", false), wantErr: "want an absolute https URL"},
		{name: "query in endpoint", cred: cred, opts: withEndpoint("https://bc.example.com/?x=1", false), wantErr: "must not contain a query"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, err := bcadmin.NewClient(tt.cred, tt.opts)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("NewClient() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.Endpoint() != tt.wantURL {
				t.Errorf("Endpoint() = %q, want %q", c.Endpoint(), tt.wantURL)
			}
			if !strings.HasSuffix(tt.wantURL, c.APIVersion()) {
				t.Errorf("APIVersion() = %q", c.APIVersion())
			}
		})
	}
}

// recordingCredential records the token requests it receives.
type recordingCredential struct {
	mu   sync.Mutex
	reqs []policy.TokenRequestOptions
}

func (r *recordingCredential) GetToken(ctx context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, opts)
	r.mu.Unlock()
	return bcadmintest.Credential().GetToken(ctx, opts)
}

func (r *recordingCredential) last() policy.TokenRequestOptions {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reqs[len(r.reqs)-1]
}

func TestTokenScopeAndTenant(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.HandleJSON(http.MethodGet, "environments/quotas", http.StatusOK, map[string]any{})
	cred := &recordingCredential{}
	c, err := bcadmin.NewClient(cred, srv.ClientOptions())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := c.Tenant().Quotas(ctx); err != nil {
		t.Fatal(err)
	}
	got := cred.last()
	if len(got.Scopes) != 1 || got.Scopes[0] != "https://bcadmintest.example.com/.default" {
		t.Errorf("Scopes = %v", got.Scopes)
	}
	if got.TenantID != "" {
		t.Errorf("TenantID = %q, want empty", got.TenantID)
	}

	tc, err := c.ForTenant("00000000-0000-0000-0000-00000000000a")
	if err != nil {
		t.Fatal(err)
	}
	tc2, err := tc.ForTenant("00000000-0000-0000-0000-00000000000b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tc2.Tenant().Quotas(ctx); err != nil {
		t.Fatal(err)
	}
	if got := cred.last().TenantID; got != "00000000-0000-0000-0000-00000000000b" {
		t.Errorf("TenantID = %q", got)
	}
	if _, err := c.ForTenant(""); err == nil {
		t.Error("ForTenant(\"\") did not fail")
	}
}

func TestUserAgentAndHeaders(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	srv.HandleJSON(http.MethodGet, "environments/quotas", http.StatusOK, map[string]any{})
	opts := srv.ClientOptions()
	opts.Telemetry.ApplicationID = "my-tool/1.0"
	c, err := bcadmin.NewClient(bcadmintest.Credential(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Tenant().Quotas(context.Background()); err != nil {
		t.Fatal(err)
	}
	req, _ := srv.LastRequest()
	ua := req.Header.Get("User-Agent")
	if !strings.HasPrefix(ua, "my-tool/1.0 ") || !strings.Contains(ua, "go-bcadmin/") {
		t.Errorf("User-Agent = %q", ua)
	}
	if req.Header.Get("Accept") != "application/json" {
		t.Errorf("Accept = %q", req.Header.Get("Accept"))
	}
	if req.Query != "" {
		t.Errorf("unexpected query %q; the API version belongs in the path", req.Query)
	}
}
