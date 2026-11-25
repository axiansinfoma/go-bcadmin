package bcadmintest_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/axiansinfoma/go-bcadmin"
	"github.com/axiansinfoma/go-bcadmin/bcadmintest"
)

func TestServer(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t, bcadmintest.WithAPIVersion("v2.28"))
	if srv.APIVersion() != "v2.28" {
		t.Fatalf("APIVersion() = %q", srv.APIVersion())
	}
	if _, ok := srv.LastRequest(); ok {
		t.Fatal("LastRequest() reported a request before any was sent")
	}
	srv.HandleJSON(http.MethodGet, "environments/quotas", http.StatusOK, map[string]any{"environmentsCount": map[string]int{"production": 2}})
	srv.Fail(bcadmintest.Fault{Method: http.MethodGet, Path: "/environments/quotas", Status: http.StatusTooManyRequests, Times: 2})
	c := srv.NewClient(t)
	if c.APIVersion() != "v2.28" {
		t.Fatalf("client API version = %q", c.APIVersion())
	}
	q, err := c.Tenant().Quotas(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if q.EnvironmentsCount.Production != 2 {
		t.Errorf("quotas = %+v", q)
	}
	reqs := srv.Requests()
	if len(reqs) != 3 {
		t.Fatalf("requests = %d, want 3 (2 faults + 1 success)", len(reqs))
	}
	if reqs[2].Path != "/admin/v2.28/environments/quotas" || reqs[2].Header.Get("Authorization") != "Bearer "+bcadmintest.Token {
		t.Errorf("request = %+v", reqs[2])
	}
}

func TestServerRejectsMissingToken(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL()+"/admin/v2.29/environments/quotas", nil)
	if err != nil {
		t.Fatal(err)
	}
	opts := srv.ClientOptions()
	resp, err := opts.Transport.(*http.Client).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestServerUnknownPath(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	err := srv.NewClient(t).Do(context.Background(), http.MethodGet, "unknown", nil, nil)
	if !bcadmin.IsNotFound(err) {
		t.Errorf("error = %v, want not found", err)
	}
}
