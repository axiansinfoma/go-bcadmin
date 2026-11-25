package bcadmin_test

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/axiansinfoma/go-bcadmin"
	"github.com/axiansinfoma/go-bcadmin/bcadmintest"
)

// serveFixture registers a handler that answers method/path with the
// contents of a testdata file.
func serveFixture(t *testing.T, srv *bcadmintest.Server, method, path, file string) {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	srv.Handle(method, path, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	})
}

func TestTenantQuotas(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	serveFixture(t, srv, http.MethodGet, "environments/quotas", "testdata/tenant/quotas.json")
	q, err := srv.NewClient(t).Tenant().Quotas(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := bcadmin.EnvironmentQuotas{
		EnvironmentsCount:  bcadmin.EnvironmentsCountQuota{Production: 1, Sandbox: 3},
		StorageInKilobytes: bcadmin.StorageQuota{Default: 83886080, UserLicenses: 4194304, AdditionalCapacity: 0, Total: 88080384},
	}
	if *q != want {
		t.Errorf("Quotas() = %+v, want %+v", *q, want)
	}
}

func TestTenantUsedStorage(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	serveFixture(t, srv, http.MethodGet, "environments/usedstorage", "testdata/tenant/usedstorage.json")
	got, err := srv.NewClient(t).Tenant().UsedStorage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].EnvironmentName != "Production" || !got[0].EnvironmentType.Is(bcadmin.EnvironmentTypeProduction) ||
		got[1].DatabaseStorageInKilobytes != 3221225472 {
		t.Errorf("UsedStorage() = %+v", got)
	}
}

func TestTenantOperations(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	serveFixture(t, srv, http.MethodGet, "environments/operations", "testdata/tenant/operations.json")
	serveFixture(t, srv, http.MethodGet, "environments/operations/{id}", "testdata/tenant/operation.json")
	c := srv.NewClient(t)
	ops, err := c.Tenant().Operations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 {
		t.Fatalf("Operations() returned %d operations", len(ops))
	}
	op := ops[0]
	if op.ID != "00000000-0000-0000-0000-000000000010" || !op.Type.Is(bcadmin.OperationTypeCopy) ||
		!op.Status.Is(bcadmin.OperationStatusSucceeded) || op.CreatedOn == nil || op.CreatedOn.Year() != 2026 ||
		op.Parameters["destinationEnvironmentName"] != "mysandbox" || op.ProductFamily != bcadmin.FamilyBusinessCentral {
		t.Errorf("operation = %+v", op)
	}
	// Environment operation lists carry app details in parameters.
	if !ops[1].Type.Is(bcadmin.OperationTypeEnvironmentAppInstall) || ops[1].Parameters["appId"] != "00000000-0000-0000-0000-000000000002" ||
		ops[1].Parameters["targetAppVersion"] != "2.0.0.0" || ops[1].AppID != "" {
		t.Errorf("app operation = %+v", ops[1])
	}

	one, err := c.Tenant().GetOperation(context.Background(), "00000000-0000-0000-0000-000000000010")
	if err != nil {
		t.Fatal(err)
	}
	if one.ErrorMessage == "" || !one.Status.Is(bcadmin.OperationStatusFailed) {
		t.Errorf("GetOperation() = %+v", one)
	}
	req, _ := srv.LastRequest()
	if req.Path != "/admin/v2.29/environments/operations/00000000-0000-0000-0000-000000000010" {
		t.Errorf("path = %s", req.Path)
	}
}

func TestApplications(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	serveFixture(t, srv, http.MethodGet, "applications/{$}", "testdata/applications/families.json")
	serveFixture(t, srv, http.MethodGet, "applications/{family}/Countries/{country}/Rings/{ring}", "testdata/applications/versions.json")
	c := srv.NewClient(t)
	fams, err := c.Applications().Families(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fams) != 1 || len(fams[0].CountriesRingDetails) != 2 {
		t.Fatalf("Families() = %+v", fams)
	}
	de := fams[0].CountriesRingDetails[1]
	if de.CountryCode != "DE" || len(de.Rings) != 2 || !de.Rings[0].ProductionRing || de.Rings[1].Name != "PREVIEW" {
		t.Errorf("DE = %+v", de)
	}

	versions, err := c.Applications().Versions(context.Background(), bcadmin.FamilyBusinessCentral, "DE", "PROD")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[0] != "27.0.38460.41135" {
		t.Errorf("Versions() = %v", versions)
	}
	req, _ := srv.LastRequest()
	if req.Path != "/admin/v2.29/applications/BusinessCentral/Countries/DE/Rings/PROD" {
		t.Errorf("path = %s", req.Path)
	}
	if _, err := c.Applications().Versions(context.Background(), "", "DE", "PROD"); err == nil {
		t.Error("Versions with empty family did not fail")
	}
}
