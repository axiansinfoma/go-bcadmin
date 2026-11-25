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
they need a delegated (user) token. Their documentation says so.

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
`RawBody` and shown in the error text.

```go
var re *bcadmin.ResponseError
if errors.As(err, &re) {
	log.Printf("API error %s: %s (request %s)", re.Code, re.Message, re.RequestID)
}
if bcadmin.IsNotFound(err) { /* 404, or an error code ending in NotFound */ }
```

## Retries

Requests are retried with exponential backoff and honor `Retry-After`.
Configure it with `ClientOptions.Retry` (the
[azcore retry options](https://pkg.go.dev/github.com/Azure/azure-sdk-for-go/sdk/azcore/policy#RetryOptions)).

- GET, PUT and DELETE are retried on 408, 429, 500, 502, 503 and 504 and on
  network errors.
- POST and PATCH start operations and are therefore only retried when the
  request cannot have been processed: on 429, on 503 with `Retry-After`, and when
  the connection could not be established. Set
  `ClientOptions.RetryNonIdempotent` to retry them like GET.

## Configuration

```go
client, err := bcadmin.NewClient(cred, &bcadmin.ClientOptions{
	ClientOptions: azcore.ClientOptions{
		APIVersion: "v2.28",                                     // default: bcadmin.DefaultAPIVersion
		Telemetry:  policy.TelemetryOptions{ApplicationID: "my-tool/1.2.3"}, // User-Agent prefix
		Cloud: cloud.Configuration{Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
			bcadmin.ServiceName: {Endpoint: "https://...", Audience: "https://..."}, // other clouds
		}},
	},
	PollFrequency: 30 * time.Second,
})

// Manage another tenant with a multi-tenant app registration.
customer, err := client.ForTenant("<tenant-id>")
```

`client.Do` calls endpoints this module does not wrap yet.

## Testing your code

The [`bcadmintest`](https://pkg.go.dev/github.com/axiansinfoma/go-bcadmin/bcadmintest)
package provides a fake Admin Center API server:

```go
srv := bcadmintest.NewServer(t)
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
| Environments | list, get, create, copy, delete, recover, rename, restore, restore periods, used storage, operations, match production configuration, Power Platform link/unlink | | | 🚧 |
| Apps | list, available updates, install, update, cancel update, uninstall, uninstall requirements, operations, per-tenant extension upload, scheduled per-tenant extension operations | | | 🚧 |
| Updates | list, select/schedule, cancel | | | 🚧 |
| Settings | update window, time zones, Application Insights, security group, partner access, Microsoft 365 licenses, AppSource update cadence | | | 🚧 |
| Sessions | list, get, terminate | | | 🚧 |
| Database export | metrics, start, history | | | 🚧 |
| Notifications | settings, recipients | | | 🚧 |
| Support | contact, outage types and questions, reported outages, report outage | | | 🚧 |
| Telemetry | query | | | 🚧 |
| Environment transfers | outgoing, incoming | | | 🚧 |
| Authorized Entra apps | list, authorize, remove, manageable tenants | | | 🚧 |

## Versioning

go-bcadmin follows [Semantic Versioning](https://semver.org/). Releases are Git
tags `vX.Y.Z`. Before v1.0.0, minor versions may contain breaking changes; they
are called out in the release notes.

## Contributing

See [CONTRIBUTING.md](.github/CONTRIBUTING.md). Please report security problems
privately as described in [SECURITY.md](.github/SECURITY.md).

## License

[MIT](LICENSE)
