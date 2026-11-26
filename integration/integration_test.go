//go:build integration

// Package integration_test runs read-only tests against the live Admin
// Center API. They are excluded from normal builds by the "integration"
// build tag and never run in CI. See README.md, "Integration tests".
//
// Environment variables:
//
//	BCADMIN_TENANT_ID    Microsoft Entra tenant to test against (required;
//	                     the tests are skipped without it).
//	BCADMIN_ENVIRONMENT  Name of an existing environment for the
//	                     environment-scoped tests (optional).
//	BCADMIN_API_VERSION  API version, for example "v2.28" (optional).
//
// Authentication uses azidentity.DefaultAzureCredential, for example the
// AZURE_CLIENT_ID, AZURE_CLIENT_SECRET and AZURE_TENANT_ID variables or an
// Azure CLI login. The tests only read, and they log counts, not tenant
// data.
package integration_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"

	"github.com/axiansinfoma/go-bcadmin"
)

// newClient returns a client for BCADMIN_TENANT_ID or skips the test.
func newClient(t *testing.T) *bcadmin.Client {
	t.Helper()
	tenant := os.Getenv("BCADMIN_TENANT_ID")
	if tenant == "" {
		t.Skip("BCADMIN_TENANT_ID is not set")
	}
	cred, err := azidentity.NewDefaultAzureCredential(&azidentity.DefaultAzureCredentialOptions{TenantID: tenant})
	if err != nil {
		t.Fatal(err)
	}
	c, err := bcadmin.NewClient(cred, &bcadmin.ClientOptions{
		ClientOptions: azcore.ClientOptions{APIVersion: os.Getenv("BCADMIN_API_VERSION")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// testContext returns a context that ends after two minutes.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func TestTenant(t *testing.T) {
	c := newClient(t)
	ctx := testContext(t)
	if _, err := c.Tenant().Quotas(ctx); err != nil {
		t.Errorf("Quotas: %v", err)
	}
	if s, err := c.Tenant().UsedStorage(ctx); err != nil {
		t.Errorf("UsedStorage: %v", err)
	} else {
		t.Logf("used storage entries: %d", len(s))
	}
	if ops, err := c.Tenant().Operations(ctx); err != nil {
		t.Errorf("Operations: %v", err)
	} else {
		t.Logf("operations: %d", len(ops))
	}
	if fams, err := c.Applications().Families(ctx); err != nil {
		t.Errorf("Families: %v", err)
	} else {
		t.Logf("application families: %d", len(fams))
	}
	if envs, err := c.Environments().List(ctx, ""); err != nil {
		t.Errorf("Environments: %v", err)
	} else {
		t.Logf("environments: %d", len(envs))
	}
	if _, err := c.Notifications().Settings(ctx); err != nil {
		t.Errorf("Notifications: %v", err)
	}
	if types, err := c.Support().OutageTypes(ctx); err != nil {
		t.Errorf("OutageTypes: %v", err)
	} else {
		t.Logf("outage types: %d", len(types))
	}
}

func TestEnvironment(t *testing.T) {
	c := newClient(t)
	name := os.Getenv("BCADMIN_ENVIRONMENT")
	if name == "" {
		t.Skip("BCADMIN_ENVIRONMENT is not set")
	}
	ctx := testContext(t)
	env, err := c.Environments().Get(ctx, bcadmin.FamilyBusinessCentral, name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if apps, err := env.Apps().List(ctx); err != nil {
		t.Errorf("Apps: %v", err)
	} else {
		t.Logf("installed apps: %d", len(apps))
	}
	if updates, err := env.Updates().List(ctx); err != nil {
		t.Errorf("Updates: %v", err)
	} else {
		t.Logf("updates: %d", len(updates))
	}
	if _, err := env.Settings().UpdateWindow(ctx); err != nil {
		t.Errorf("UpdateWindow: %v", err)
	}
	if sessions, err := env.Sessions().List(ctx); err != nil {
		t.Errorf("Sessions: %v", err)
	} else {
		t.Logf("sessions: %d", len(sessions))
	}
	if _, err := env.UsedStorage(ctx); err != nil {
		t.Errorf("UsedStorage: %v", err)
	}
	if ops, err := env.Operations(ctx); err != nil {
		t.Errorf("Operations: %v", err)
	} else {
		t.Logf("environment operations: %d", len(ops))
	}
	if _, err := env.ExportMetrics(ctx); err != nil {
		t.Errorf("ExportMetrics: %v", err)
	}
}
