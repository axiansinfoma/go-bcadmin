package bcadmintest_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/axiansinfoma/go-bcadmin"
	"github.com/axiansinfoma/go-bcadmin/bcadmintest"
)

func TestEnvironmentsFake(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	fake := srv.EnableEnvironments()
	fake.Add(bcadmin.Environment{Name: "Production", Type: bcadmin.EnvironmentTypeProduction, CountryCode: "DE"})
	fake.Add(bcadmin.Environment{Name: "sb", Type: bcadmin.EnvironmentTypeSandbox, ApplicationFamily: "Other"})
	c := srv.NewClient(t)
	ctx := context.Background()

	all, err := c.Environments().List(ctx, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("List(all) = %v, %v", all, err)
	}
	bc, err := c.Environments().List(ctx, bcadmin.FamilyBusinessCentral)
	if err != nil || len(bc) != 1 || bc[0].Name != "Production" || !bc[0].Status.Is(bcadmin.EnvironmentStatusActive) {
		t.Fatalf("List(BusinessCentral) = %v, %v", bc, err)
	}
	prod := bc[0]
	if periods, err := prod.RestorePeriods(ctx); err != nil || len(periods) != 1 {
		t.Errorf("RestorePeriods() = %v, %v", periods, err)
	}
	if u, err := prod.UsedStorage(ctx); err != nil || u.EnvironmentName != "Production" {
		t.Errorf("UsedStorage() = %+v, %v", u, err)
	}
	m, err := c.Environments().MatchProductionConfigurationMetrics(ctx, bcadmin.FamilyBusinessCentral, "Production")
	if err != nil || m.CanMatchProductionConfiguration || m.BlockedReason != "NotEligible" {
		t.Errorf("metrics = %+v, %v", m, err)
	}
	if _, err := prod.MatchProductionConfiguration(ctx); bcadmin.ErrorCode(err) != "productionConfigurationNotEligible" {
		t.Errorf("match on production: %v", err)
	}
	if _, err := prod.Recover(ctx); bcadmin.ErrorCode(err) != "invalidStatusCannotRecoverDeletedEnvironment" {
		t.Errorf("recover active: %v", err)
	}
	if err := c.Do(ctx, http.MethodPut, "applications/BusinessCentral/environments/x", map[string]string{}, nil); bcadmin.ErrorCode(err) != "invalidInput" {
		t.Errorf("create without body fields: %v", err)
	}

	fake.SetPollsUntilDone(2)
	p, err := prod.Copy(ctx, "copy", bcadmin.EnvironmentTypeSandbox)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Poll(ctx); err != nil || !p.Operation().Status.Is(bcadmin.OperationStatusRunning) {
		t.Errorf("first poll = %+v, %v", p.Operation(), err)
	}
	ops, err := c.Tenant().Operations(ctx)
	if err != nil || len(ops) != 1 || !ops[0].Status.Terminal() {
		t.Errorf("tenant operations = %+v, %v", ops, err)
	}
	if got := fake.Operations(); len(got) != 1 || got[0].ID != p.ID() {
		t.Errorf("fake.Operations() = %+v", got)
	}
	if _, ok := fake.Get(bcadmin.FamilyBusinessCentral, "copy"); !ok {
		t.Error("copy not stored")
	}
	if _, err := c.Environments().GetOperation(ctx, bcadmin.FamilyBusinessCentral, "sb", p.ID()); !bcadmin.IsNotFound(err) {
		t.Errorf("operation of another environment: %v", err)
	}
}
