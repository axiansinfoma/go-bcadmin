# go-bcadmin

[![CI](https://github.com/axiansinfoma/go-bcadmin/actions/workflows/ci.yml/badge.svg)](https://github.com/axiansinfoma/go-bcadmin/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/axiansinfoma/go-bcadmin.svg)](https://pkg.go.dev/github.com/axiansinfoma/go-bcadmin)
[![Go Report Card](https://goreportcard.com/badge/github.com/axiansinfoma/go-bcadmin)](https://goreportcard.com/report/github.com/axiansinfoma/go-bcadmin)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A Go client for the [Microsoft Dynamics 365 Business Central Administration Center
API](https://learn.microsoft.com/en-us/dynamics365/business-central/dev-itpro/administration/administration-center-api).

You work with environments, apps and operations as Go values. The client takes
care of URLs, authentication, retries, pagination and waiting for long-running
operations, and it passes the API's error messages through to you.

> **Status:** pre-1.0. The API of this module can still change between minor
> versions. See [Endpoint coverage](#endpoint-coverage) for what is implemented.

## Install

```sh
go get github.com/axiansinfoma/go-bcadmin
```

The package name is `bcadmin`. go-bcadmin supports the two latest Go releases.

## Prerequisites

go-bcadmin authenticates with any
[`azcore.TokenCredential`](https://pkg.go.dev/github.com/Azure/azure-sdk-for-go/sdk/azcore#TokenCredential),
for example one from
[azidentity](https://pkg.go.dev/github.com/Azure/azure-sdk-for-go/sdk/azidentity)
(client secret, certificate, workload identity, managed identity, Azure CLI).

For service-to-service authentication, the Microsoft Entra app needs:

1. The **AdminCenter.ReadWrite.All** application permission of *Dynamics 365
   Business Central*, with admin consent.
2. An entry on the **Microsoft Entra apps** page of the Business Central admin
   center of each tenant it manages.
3. For database exports, the **D365 BACKUP/RESTORE** permission set in the
   environment.

A few endpoints (Power Platform link/unlink, partner access, listing and
authorizing Entra apps, environment transfers) do not accept app-only tokens;
they need a user token, and environment transfers an internal administrator
of the tenant (not a delegated administrator). Listing the tenants an app may
manage works only with an app-only token. The method documentation says
which applies.

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"

	"github.com/axiansinfoma/go-bcadmin"
)

func main() {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		log.Fatal(err)
	}
	client, err := bcadmin.NewClient(cred, nil)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	quotas, err := client.Tenant().Quotas(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("production environments allowed: %d\n", quotas.EnvironmentsCount.Production)

	families, err := client.Applications().Families(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, f := range families {
		for _, c := range f.CountriesRingDetails {
			fmt.Println(f.ApplicationFamily, c.CountryCode, len(c.Rings), "rings")
		}
	}
}
```

## Environments

```go
envs := client.Environments()

// Create a sandbox and wait until it is active.
poller, err := envs.Create(ctx, bcadmin.FamilyBusinessCentral, "uat", bcadmin.CreateEnvironmentOptions{
	Type:        bcadmin.EnvironmentTypeSandbox,
	CountryCode: "DE",
})
if err != nil { /* *bcadmin.ResponseError, e.g. code "resourceExists" */ }
env, err := poller.Wait(ctx, &bcadmin.WaitOptions{UntilEnvironmentActive: true})

// Environments returned by the client have methods.
copyPoller, err := env.Copy(ctx, "uat-copy", bcadmin.EnvironmentTypeSandbox)
deletePoller, err := env.Delete(ctx)
```

`Create` and `Delete` are not retried on server errors, because the operation
may already have started; check the environment or its operations before trying
again. The methods that start environment operations refuse API versions in
which the operation cannot be tracked (before v2.9 for create, copy, delete and
match production configuration, v2.7 for rename, v2.6 for restore and v2.18
for recover), instead of starting an operation the client cannot follow.

## Apps and updates

```go
apps := env.Apps() // or client.Apps(bcadmin.FamilyBusinessCentral, "uat")

// Install an AppSource app with its dependencies.
p, err := apps.Install(ctx, appID, bcadmin.InstallAppOptions{
	AcceptISVEULA:                     true, // accepts the provider's terms, see the API docs
	InstallOrUpdateNeededDependencies: true,
})
var re *bcadmin.ResponseError
if errors.As(err, &re) && re.Data != nil {
	// Missing dependencies: re.Data.Requirements
}
if err != nil {
	return err
}
if _, err := p.Wait(ctx, nil); err != nil {
	return err
}

// Upload a per-tenant extension (streamed, up to 50 MB).
f, err := os.Open("Contoso_MyApp_1.0.0.0.app")
if err != nil {
	return err
}
defer f.Close()
pte, err := apps.InstallPTE(ctx, f, bcadmin.InstallPTEOptions{
	FileName:           "Contoso_MyApp_1.0.0.0.app",
	AcceptISVEULA:      true,
	DeploymentSchedule: bcadmin.DeploymentUpdateWindow,
})
if err != nil {
	return err
}
_, err = pte.Wait(ctx, &bcadmin.WaitOptions{TreatScheduledAsDone: true})
if err != nil && !errors.Is(err, bcadmin.ErrOperationScheduled) {
	return err
}

// Select the next update and its start time.
if err := env.Updates().Select(ctx, "27.1", &bcadmin.ScheduleUpdateOptions{SelectedDateTime: start}); err != nil {
	return err
}
```

## Settings, sessions and telemetry

```go
// Update window in a Windows time zone (see client.Applications().TimeZones).
_, err = env.Settings().SetUpdateWindow(ctx, bcadmin.SetUpdateWindowOptions{
	Start: "22:00", End: "06:00", TimeZoneID: "W. Europe Standard Time",
})
if err != nil {
	return err
}

// Restrict access to a Microsoft Entra security group.
if err := env.Settings().SetSecurityGroup(ctx, groupObjectID); err != nil {
	return err
}

// Active sessions.
sessions, err := env.Sessions().List(ctx)
if err != nil {
	return err
}
for _, s := range sessions {
	fmt.Println(s.SessionID, s.UserID, s.ClientType)
}

// Telemetry of the last hour.
tel, err := env.Telemetry(ctx, bcadmin.TelemetryQuery{Start: time.Now().Add(-time.Hour), End: time.Now()})
if err != nil {
	return err
}
msg := tel.Column("message")
for _, row := range tel.Rows {
	if msg >= 0 && msg < len(row) {
		fmt.Println(row[msg])
	}
}
```

## Exports, notifications and support

```go
// Export the database to Azure storage (needs D365 BACKUP/RESTORE in the
// environment; uses one of the monthly exports).
m, err := env.ExportMetrics(ctx)
if err != nil {
	return err
}
if m.ExportsRemainingThisMonth > 0 {
	if _, err := env.ExportDatabase(ctx, bcadmin.ExportOptions{
		StorageAccountSASURI: sasURI, Container: "exports", Blob: "uat.bacpac",
	}); err != nil {
		return err
	}
}

// Who gets update notifications.
if _, err := client.Notifications().AddRecipient(ctx, "ops@contoso.com", "Contoso Ops"); err != nil {
	return err
}

// Report that an environment is not accessible.
res, err := env.ReportOutage(ctx, bcadmin.OutageReport{OutageType: outageType, Answers: answers})
if err != nil {
	return err
}
if res.CreationStatus.Is(bcadmin.OutageReportError) {
	return errors.New("outage report failed")
}
```

## Environment transfers and authorized apps

```go
// Move an environment to another Microsoft Entra tenant: request it on the
// source tenant, accept it on the destination tenant (internal
// administrators only).
src, err := client.ForTenant(sourceTenantID)
if err != nil {
	return err
}
if _, err := src.Transfers().RequestOutgoing(ctx, bcadmin.FamilyBusinessCentral, "uat",
	bcadmin.OutgoingTransferOptions{DestinationEntraTenantID: destTenantID, RunAt: time.Now().Add(24 * time.Hour)}); err != nil {
	return err
}
dst, err := client.ForTenant(destTenantID)
if err != nil {
	return err
}
if _, err := dst.Transfers().AcceptIncoming(ctx, bcadmin.IncomingTransfer{
	SourceEntraTenantID: sourceTenantID, ApplicationFamily: bcadmin.FamilyBusinessCentral,
	SourceEnvironmentName: "uat", DestinationEnvironmentName: "uat",
}); err != nil {
	return err
}

// Authorize an Entra app for the Admin Center API (user token).
if _, err := client.AuthorizedApps().Authorize(ctx, appClientID); err != nil {
	return err
}
```

## Long-running operations

Calls that start an operation return a `*bcadmin.Poller[T]`:

```go
// Wait until the operation is done (polls every 10 s by default).
result, err := poller.Wait(ctx, nil)

// Or drive the polling yourself.
op, err := poller.Poll(ctx)
if poller.Done() { result, err = poller.Result(ctx) }
```

- `Wait` returns an `*bcadmin.OperationError` with the API's error message when
  the operation fails or is canceled.
- `WaitOptions.TreatScheduledAsDone` returns `bcadmin.ErrOperationScheduled` as
  soon as an operation is scheduled for later (for example in the update window).
- If `ctx` ends first, the operation keeps running in Business Central. Resume it
  later from its ID, for example with `client.Tenant().ResumeOperation(id)`.

## Errors

Every failed HTTP response is returned as `*bcadmin.ResponseError` with the
details the API sent: status, `Code`, `Message`, `Target`, `ClientErrors`,
`Details`, `Data.Requirements` (missing app dependencies), the request ID and
the raw body. Both the flat (`{"code","message"}`) and the wrapped
(`{"error":{...}}`) error formats are understood; non-JSON bodies are kept in
`RawBody` and shown in the error text. `Error()` replaces control characters so
the text is safe to log; the fields keep the original text.

```go
var re *bcadmin.ResponseError
if errors.As(err, &re) {
	log.Printf("API error %s: %s (request %s)", re.Code, re.Message, re.RequestID)
}
if bcadmin.IsNotFound(err) { /* 404, or the code EnvironmentNotFound */ }
```

## Retries

Requests are retried with exponential backoff and honor `Retry-After`.
Configure it with `ClientOptions.Retry` (the
[azcore retry options](https://pkg.go.dev/github.com/Azure/azure-sdk-for-go/sdk/azcore/policy#RetryOptions)).

- GET, PUT and DELETE are retried on 408, 429, 500, 502, 503 and 504 and on
  network errors.
- POST and PATCH (and PUT requests that create resources) start operations and
  are therefore only retried when the request cannot have been processed: on 429,
  on 503 with `Retry-After`, and when the connection could not be established.
  Set `ClientOptions.RetryNonIdempotent` to retry them like GET.
- Responses larger than `ClientOptions.MaxResponseBytes` (default 64 MiB) fail
  with `bcadmin.ErrResponseTooLarge` and are not retried.

The default transport requires TLS 1.2 and follows redirects only within the
endpoint's scheme and host, so the access token is never sent elsewhere.

All clients created without `ClientOptions.Transport` share one pooled HTTP
transport (up to 100 idle keep-alive connections, HTTP/2 where available), so
creating many clients, for example one per tenant, does not open a new
connection pool each time. This avoids SNAT port exhaustion in Azure. Prefer
`client.ForTenant` over new clients where possible; it shares everything but
the token tenant.

## Configuration

```go
client, err := bcadmin.NewClient(cred, &bcadmin.ClientOptions{
	ClientOptions: azcore.ClientOptions{
		APIVersion: "v2.28",                                     // default: bcadmin.DefaultAPIVersion
		Telemetry:  policy.TelemetryOptions{ApplicationID: "my-tool/1.2.3"}, // User-Agent prefix
		Cloud: cloud.Configuration{Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
			bcadmin.ServiceName: {Endpoint: "https://...", Audience: "https://..."}, // other clouds; set both
		}},
	},
	PollFrequency: 30 * time.Second,
})

// Manage another tenant with a multi-tenant app registration.
customer, err := client.ForTenant("<tenant-id>")
```

`client.Do` calls endpoints this module does not wrap yet. It retries only GET,
HEAD and OPTIONS requests on server errors; other methods get the rules for
POST.

## Testing your code

The [`bcadmintest`](https://pkg.go.dev/github.com/axiansinfoma/go-bcadmin/bcadmintest)
package provides a fake Admin Center API server:

```go
srv := bcadmintest.NewServer(t)

// A stateful fake of the environment endpoints: operations finish after a
// number of polls and change the stored environments.
envs := srv.EnableEnvironments()
envs.Add(bcadmin.Environment{Name: "Production", Type: bcadmin.EnvironmentTypeProduction})
envs.FailNextOperation("simulated failure")

// A stateful fake of the app endpoints: a catalog of published versions with
// dependencies, installed apps per environment, scheduled operations and
// PTE uploads (identified by file name).
apps := srv.EnableApps()
apps.Publish(bcadmintest.App{AppID: appID, Name: "Contoso Connector", Version: "2.0.0.0"})
apps.AddPTEFile("Contoso_MyApp_1.0.0.0.app", bcadmintest.App{AppID: pteID, Name: "My App", Version: "1.0.0.0"})
apps.RunScheduled(bcadmin.FamilyBusinessCentral, "Production") // release scheduled operations; they run when polled

// Or handlers for single endpoints.
srv.HandleJSON(http.MethodGet, "environments/quotas", http.StatusOK, map[string]any{
	"environmentsCount": map[string]int{"production": 1, "sandbox": 3},
})
srv.Fail(bcadmintest.Fault{Path: "environments/usedstorage", Status: http.StatusTooManyRequests, Times: 1})

client := srv.NewClient(t)
// ... exercise your code, then inspect srv.Requests().
```

## Endpoint coverage

API version **v2.29**. ✅ implemented, 🚧 planned.

| Area | Endpoints | Methods | Since | Status |
| --- | --- | --- | --- | --- |
| Tenant | `GET environments/quotas` | `Tenant().Quotas` | | ✅ |
| Tenant | `GET environments/usedstorage` | `Tenant().UsedStorage` | | ✅ |
| Tenant | `GET environments/operations` | `Tenant().Operations`, `Tenant().AllOperations` | v2.7 | ✅ |
| Tenant | `GET environments/operations/{id}` | `Tenant().GetOperation`, `Tenant().ResumeOperation` | v2.28 | ✅ |
| Available applications | `GET applications/` | `Applications().Families` | | ✅ |
| Available applications | `GET applications/{family}/Countries/{country}/Rings/{ring}` | `Applications().Versions` | | ✅ |
| Environments | `GET applications/environments`, `GET applications/{family}/environments` | `Environments().List`, `Environments().All` | | ✅ |
| Environments | `GET applications/{family}/environments/{name}` | `Environments().Get`, `Environment.Refresh` | | ✅ |
| Environments | `PUT applications/{family}/environments/{name}` | `Environments().Create` | v2.9 | ✅ |
| Environments | `POST .../environments/{name}/copy` | `Environments().Copy`, `Environment.Copy` | v2.9 | ✅ |
| Environments | `DELETE .../environments/{name}` | `Environments().Delete`, `Environment.Delete` | v2.9 | ✅ |
| Environments | `POST .../environments/{name}/recover` | `Environments().Recover`, `Environment.Recover` | v2.18 | ✅ |
| Environments | `POST .../environments/{name}/rename` | `Environments().Rename`, `Environment.Rename` | v2.7 (endpoint v2.3) | ✅ |
| Environments | `POST .../environments/{name}/restore` | `Environments().Restore`, `Environment.Restore` | v2.6 (endpoint v2.4) | ✅ |
| Environments | `GET .../environments/{name}/availableRestorePeriods` | `Environments().RestorePeriods` | v2.4 | ✅ |
| Environments | `GET .../environments/{name}/usedstorage` | `Environments().UsedStorage` | | ✅ |
| Environments | `GET .../environments/{name}/operations[/{id}]` | `Environments().Operations`, `GetOperation` (v2.28), `ResumeOperation` | v2.6 | ✅ |
| Environments | `POST .../environments/{name}/matchProductionConfiguration` | `Environments().MatchProductionConfiguration` | v2.9 | ✅ |
| Environments | `GET .../environments/{name}/matchProductionConfiguration/metrics` | `Environments().MatchProductionConfigurationMetrics` | | ✅ |
| Environments | `POST bap/.../environments/{name}/linkEnvironment`, `unlinkEnvironment` | `Environments().LinkPowerPlatform`, `UnlinkPowerPlatform` (delegated auth only) | v2.21 | ✅ |
| Apps | `GET .../environments/{name}/apps` | `Apps(...).List`, `Environment.Apps().List` | | ✅ |
| Apps | `GET .../apps/availableUpdates` | `Apps(...).AvailableUpdates` | | ✅ |
| Apps | `POST .../apps/{appId}/install` | `Apps(...).Install` | v2.6 | ✅ |
| Apps | `POST .../apps/{appId}/update` | `Apps(...).Update` |  | ✅ |
| Apps | `POST .../apps/{appId}/update/cancel` | `Apps(...).CancelUpdate` | | ✅ |
| Apps | `POST .../apps/{appId}/uninstall` | `Apps(...).Uninstall` | v2.6 | ✅ |
| Apps | `GET .../apps/{appId}/uninstallRequirements` | `Apps(...).UninstallRequirements` | v2.25 | ✅ |
| Apps | `GET .../apps/{appId}/operations[/{id}]` | `Apps(...).Operations`, `GetOperation`, `ResumeOperation` | | ✅ |
| Per-tenant extensions | `POST .../apps/pteInstall` (multipart upload) | `Apps(...).InstallPTE` | v2.29 | ✅ |
| Per-tenant extensions | `GET .../apps/scheduledPteOperations` | `Apps(...).ScheduledPTEOperations` | v2.29 | ✅ |
| Per-tenant extensions | `POST .../apps/{appId}/removeScheduledPteVersion` | `Apps(...).RemoveScheduledPTEVersion` | v2.29 | ✅ |
| Updates | `GET .../environments/{name}/updates` | `Updates(...).List`, `Environment.Updates().List` | | ✅ |
| Updates | `PATCH .../environments/{name}/updates/{version}` | `Updates(...).Select`, `Reschedule` | | ✅ |
| Updates | `POST .../environments/{name}/operations/{id}/cancel` | `Updates(...).CancelRunning` |  | ✅ |
| Settings | `GET/PUT .../settings/upgrade` | `Settings(...).UpdateWindow`, `SetUpdateWindow` (time zone form v2.13) | | ✅ |
| Settings | `GET applications/settings/timezones` | `Applications().TimeZones` | v2.13 | ✅ |
| Settings | `POST .../settings/appinsightskey` | `Settings(...).SetAppInsightsKey` | | ✅ |
| Settings | `GET/POST/DELETE .../settings/securitygroupaccess` | `Settings(...).SecurityGroup`, `SetSecurityGroup`, `ClearSecurityGroup` | v2.8 | ✅ |
| Settings | `GET/PUT .../settings/partneraccess` | `Settings(...).PartnerAccess`, `SetPartnerAccess` (internal global admins only) | v2.22 | ✅ |
| Settings | `GET/POST .../settings/accesswithm365licenses` | `Settings(...).AccessWithM365Licenses`, `SetAccessWithM365Licenses` | v2.12 | ✅ |
| Settings | `PUT .../settings/appSourceAppsUpdateCadence` | `Settings(...).SetAppSourceUpdateCadence` | | ✅ |
| Sessions | `GET .../environments/{name}/sessions[/{id}]` | `Sessions(...).List`, `Get` | | ✅ |
| Sessions | `DELETE .../environments/{name}/sessions/{id}` | `Sessions(...).Terminate` | | ✅ |
| Database export | `GET exports/.../environments/{name}/metrics` | `Exports().Metrics`, `Environment.ExportMetrics` | | ✅ |
| Database export | `POST exports/.../environments/{name}` | `Exports().Start`, `Environment.ExportDatabase` | | ✅ |
| Database export | `POST exports/history` | `Exports().History` (deprecated from v2.30) | | ✅ |
| Notifications | `GET settings/notification` | `Notifications().Settings` | | ✅ |
| Notifications | `GET/PUT settings/notification/recipients`, `DELETE .../recipients/{id}` | `Notifications().Recipients`, `AddRecipient`, `RemoveRecipient` | | ✅ |
| Support | `GET/PUT support/.../environments/{name}/supportcontact` | `Support().Contact`, `SetContact`, `Environment.SupportContact`, `SetSupportContact` | | ✅ |
| Support | `GET support/outageTypes[/{type}/outageQuestions]` | `Support().OutageTypes`, `OutageQuestions` | | ✅ |
| Support | `GET support/reportedoutages` | `Support().ReportedOutages` | | ✅ |
| Support | `POST support/.../environments/{name}/reportoutage` | `Support().ReportOutage`, `Environment.ReportOutage` | | ✅ |
| Telemetry | `GET .../environments/{name}/telemetry` | `Environments().Telemetry`, `Environment.Telemetry` | | ✅ |
| Environment transfers | `POST/DELETE transfers/outgoing/applications/{family}/environments/{name}` | `Transfers().RequestOutgoing`, `CancelOutgoing`, `Environment.RequestTransfer`, `CancelTransfer` | v2.20 | ✅ |
| Environment transfers | `GET transfers/outgoing` | `Transfers().Outgoing` | v2.20 | ✅ |
| Environment transfers | `POST transfers/incoming`, `GET transfers/incoming/{sourceTenantId}` | `Transfers().AcceptIncoming`, `Incoming` | v2.20 | ✅ |
| Authorized Entra apps | `GET authorizedAadApps`, `PUT/DELETE authorizedAadApps/{appClientId}` | `AuthorizedApps().List`, `Authorize` (user token only), `Remove` | | ✅ |
| Authorized Entra apps | `GET authorizedAadApps/manageableTenants` | `AuthorizedApps().ManageableTenants` (app-only token) | v2.24 | ✅ |

## Versioning

go-bcadmin follows [Semantic Versioning](https://semver.org/). Releases are Git
tags `vX.Y.Z`. Before v1.0.0, minor versions may contain breaking changes; they
are called out in the release notes.

## Contributing

See [CONTRIBUTING.md](.github/CONTRIBUTING.md). Please report security problems
privately as described in [SECURITY.md](.github/SECURITY.md).

## License

[MIT](LICENSE)
