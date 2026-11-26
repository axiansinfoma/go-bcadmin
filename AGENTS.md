# AGENTS.md

Guidance for coding agents working on go-bcadmin, a public Go client for the
Business Central Administration Center API. Read README.md for the user view.

## Structure

| Path | Content |
| --- | --- |
| `doc.go` | Package documentation. |
| `client.go` | `Client`, `NewClient`, `ClientOptions`, `ForTenant`. Builds the azcore pipeline (bearer token, retry, telemetry). |
| `transport.go` | Default HTTP client (TLS 1.2, same-origin redirects) and the response size limit. |
| `request.go` | `Do`, path building, query strings (`callQuery`), request sending, error mapping, retry rules for POST/PATCH, API version checks (`needVersion`), `@odata.nextLink` pagination (`list`, `listAll`). |
| `errors.go` | `ResponseError` (API error body pass-through), sentinels, `OperationError`. |
| `operation.go`, `poller.go` | `Operation`, statuses and types; generic `Poller[T]`. |
| `tenant.go`, `applications.go`, `environments.go`, ... | One file per API area with its types and service. |
| `apps.go`, `pte.go`, `updates.go`, `settings.go`, `sessions.go` | Environment-scoped services (`Client.Apps`, `Client.Updates`, `Client.Settings`, `Client.Sessions` and the same methods on `Environment`); unbound environments give services whose methods return an error. |
| `telemetry.go` | `EnvironmentsService.Telemetry` and `Environment.Telemetry`. |
| `transfers.go`, `authorizedapps.go` | `Client.Transfers` (environment transfers between Entra tenants) and `Client.AuthorizedApps`. |
| `exports.go`, `notifications.go`, `support.go` | Tenant-level services (`Client.Exports`, `Client.Notifications`, `Client.Support`) with family/environment arguments where needed, and `Environment` shortcuts. |
| `internal/multipartbody/` | Seekable multipart body that streams the .app file of a PTE upload. |
| `environment.go` | The `Environment` object; its methods delegate to `EnvironmentsService`. |
| `time.go` | `Time`, a tolerant timestamp type; use `*Time` for all timestamps in responses. |
| `version.go` | `DefaultAPIVersion`; module version for the User-Agent from build info. |
| `bcadmintest/` | Public fake API server for tests (ours and users'); `environments.go` and `apps.go` are stateful fakes of the environment and app endpoints. |
| `internal/pathutil/` | Path segment validation and escaping. |
| `testdata/` | JSON fixtures, based on the samples in the Microsoft Learn docs. |

The source of truth for endpoints is the Microsoft Learn documentation of the
Admin Center API, linked in README.md. The current baseline is **v2.29**
(`DefaultAPIVersion`).

## Commands

```sh
make test     # go test -race -shuffle=on ./...
make lint     # golangci-lint v2 (config: .golangci.yml)
make vuln     # govulncheck
make fuzz     # fuzz targets, FUZZTIME=15s each
make tidy     # go mod tidy && go mod verify
make check    # tidy, lint, test, vuln
```

## Conventions

- **One public package** `bcadmin`, plus `bcadmintest`. No per-area packages.
  Helpers that must not become API go to `internal/`.
- **Services** are returned by methods on `Client` (`client.Tenant()`); they
  hold only the client and are cheap to create.
- **Every I/O method** takes `ctx context.Context` first and returns typed
  results and an `error`. Long-running operations return `*Poller[T]`.
- **Paths** are built with `path(template, segments...)` / `c.call(...)`; the
  template uses `{}` for caller-supplied segments, which are validated and
  escaped. Never build paths with `fmt.Sprintf`. Query strings go through
  `c.callQuery(..., url.Values, ...)`, which encodes the values and leaves
  empty ones out; never append `"?"` to a template.
- **Lists** use `listAll[T]` (slice) or `list[T]` (`iter.Seq2`); both follow
  `@odata.nextLink` and accept bare JSON arrays.
- **JSON**: request fields use the exact casing of the docs (some bodies are
  PascalCase, for example `NewEnvironmentName`). Responses decode
  case-insensitively. Use `omitzero` or pointers so unset fields are not sent and
  an explicit `false` can be sent. Enums are string types with constants and an
  `Is` method for case-insensitive comparison; unknown values must decode.
- **Errors**: never replace or drop the API's error body. Return
  `*ResponseError` as produced by `send`. Wrap other errors with `%w` and the
  prefix `bcadmin: `.
- **Docs**: every exported identifier has a doc comment ending with a period.
  Methods name the HTTP method and path, and the API version they were
  introduced in when it is later than v2.0. Use `// Deprecated:` for deprecated
  endpoints.
- **No global mutable state**, no `init()`, no logging to stdout/stderr.
- **Tests** are black-box (`package bcadmin_test`) against `bcadmintest` where
  possible, table-driven, `t.Parallel()`. Assert method, path, request body and
  decoded response. White-box tests (`package bcadmin`) cannot import
  `bcadmintest` (import cycle).

## Adding an endpoint

1. Read the endpoint's page on Microsoft Learn; note method, path, body, response
   and the "INTRODUCED IN" version.
2. Add types and the method to the area's file.
3. Add a fixture under `testdata/<area>/` and a test.
4. If it starts an operation, return a `Poller` and test success, failure and
   cancellation.
5. Update the coverage table in README.md.

## Pitfalls

- POST/PATCH are not retried on 5xx because the operation may have started;
  keep it that way (see `nonIdempotentRetryOptions`). A PUT that creates a
  resource (for example an environment) must be sent with
  `withNonIdempotent(ctx)` so it gets the same rules.
- For non-idempotent requests the client's retry options replace per-call
  options from `policy.WithRetryOptions`; azcore offers no way to read them.
- Without a custom Transport, all clients share `sharedHTTPClient` (one
  pooled transport, `sync.OnceValue`, never modified after creation) to
  avoid connection churn and SNAT port exhaustion. Never create a transport
  per client or per request. It refuses cross-origin redirects; 3xx
  responses that reach `send` become `*ResponseError`.
- Response bodies are capped by `limitTransport` (`MaxResponseBytes`); the
  error is non-retriable for azcore. azcore reads bodies inside the pipeline,
  and its bearer token policy returns a nil response when a later policy
  fails, so `tooLargeError` and `bodyReadError` (any other body read failure)
  carry the response; `send` uses it to return a `*ResponseError` with status
  and headers. Error messages show request paths with `EscapedPath()` because
  paths can come from server-provided nextLinks.
- `ErrNotFound` matches 404 and the code `EnvironmentNotFound` only; do not
  widen it to every code ending in "NotFound" (some mean "service not found").
- `@odata.nextLink` must point to the configured endpoint; others are refused
  so the token is never sent elsewhere.
- `Retry-After` larger than `Retry.MaxRetryDelay` stops retrying (azcore
  behavior).
- azcore treats `RetryDelay: -1` as zero delay and its overflow check then uses
  the maximum delay; tests should use small positive delays.
- Environment operations are polled with `GET environments/operations/{id}`
  (v2.28+), because rename and restore responses lack the environment and the
  name changes. Older API versions search the operation lists of the
  environment in the request path and of the result environment, then the
  operations of all environments (v2.7). Operation-starting environment
  methods refuse API versions in which the operation cannot be tracked
  (`minorTrackable*` constants in environments.go).
- `Get environment` is documented as wrapped in `{"value": [...]}`, but the
  service returns the object; `EnvironmentsService.Get` accepts both.
- Methods whose endpoint has a documented "INTRODUCED IN" version later than
  v2.0 (apps, PTE, settings) check it with `Client.needVersion` before
  sending, so an old client gets a clear error instead of a 404 that looks
  like "not found". Endpoints without a documented version are not checked.
- Settings bodies use the documented casing, which is inconsistent: the
  security group body is `{"Value": ...}` (capital V), the AppSource cadence
  body `{"value": ...}`. A missing security group is a 204 (or an empty
  object); `SecurityGroup` returns nil. `UpdateWindow` returns nil for a
  `null` body.
- Telemetry query parameters are `startDateUtc`, `endDateUtc` and
  `logCategory` (the names in the documented route; the parameter table on
  the same page uses other names). Rows keep numbers as `json.Number`.
- Database exports: the SAS URI is a secret; validation errors never include
  it (nor `url.Parse` errors, which quote it), and `ExportOptions` masks it
  when formatted (`Format` covers every verb, but fmt cannot reach it in
  unexported fields of other structs). The start response is not documented: it is read as
  `rawPayload` (send does not decode it) and an operation is returned if the
  body is one; any other 2xx body, and a 2xx body that cannot be read
  (`acceptedRaw` in send), gives nil, never an error that would invite a
  second export from the monthly quota. `decodeList` also accepts a single
  object (the documented history shape).
  Export history is a POST with `start`/`end` query parameters, deprecated
  from v2.30 (refused there); it accepts a bare array or `{"value": [...]}`
  (`decodeList`, exact `value` key first, unwrapped once).
- Creating a notification recipient is a PUT that creates; it is sent with
  `withNonIdempotent` so it is not retried on 5xx. Outage report results
  document `creationStatus` value `Error`; callers check it as well as the
  error.
- Environment transfers (v2.20+, internal administrators only) have
  PascalCase request bodies (`DestinationEntraTenantId`, `RunAt`,
  `SourceEntraTenantId`, ...) and return operations; tracking them is not
  documented, so they return `*Operation`, not a `Poller`. Request and
  accept are POSTs (not retried on 5xx). `RunAt` is sent as UTC.
- `GET authorizedAadApps` returns a bare array (`listAll` handles it);
  `manageableTenants` is a wrapped list. App client IDs equal to
  `manageableTenants` are refused so PUT/DELETE cannot address that path.
- Reported outages use PascalCase keys; outage question `parentId` is
  documented as a string but accepted as a number too.
- `Session.LogOnDate` stays a string: the docs type it as a string, and a
  format `Time` cannot parse would fail the whole list.
- A single app operation (`apps/{appId}/operations/{id}`) is documented as a
  plain object; `AppsService.GetOperation` also accepts the wrapped list form
  and checks the operation ID in both.
- The PTE upload body must be seekable (azcore sets Content-Length and
  rewinds it on retry); never read the file into memory. azcore reuses one
  body object for all attempts while net/http may still be writing an
  abandoned attempt (early 429), so `freshBodyPolicy` (after the bearer
  policy) gives each attempt its own reader from `multipartbody.Body.NewReader`,
  which reads the file with `ReadAt`. `TestInstallPTERetry` (TLS, early 429)
  catches regressions under `-race`. `Body.Close` (deferred in `InstallPTE`)
  waits for reads in progress and fails later ones, so the caller's file is
  never read after `InstallPTE` returns. A caller's `PerRetryPolicies` run
  after `freshBodyPolicy`; one that calls `RewindBody` brings back the shared
  body.
- `GORELEASE_VERSION` in ci.yml is a pseudo-version (golang.org/x/exp has no
  tags); check that Renovate proposes updates for it, otherwise bump it by
  hand.
- `bcadmintest` patterns use `http.ServeMux` syntax; `applications/` needs
  `applications/{$}` to match only that path.
- The stateful fakes in `bcadmintest` follow the documented behavior and
  error codes; where the docs name none, they use their own codes, listed in
  the type's doc comment. Keep that list current when adding checks. The
  apps fake identifies PTE uploads by file name (`AddPTEFile`) because it
  cannot read .app files.
- This repository is public. Never add tenant IDs, environment or customer
  names, internal URLs, email addresses or secrets to code, tests, fixtures,
  commit messages or PR descriptions. Test data uses placeholder GUIDs
  (`00000000-0000-0000-0000-00000000000x`), `contoso.com` and `example.com`.

## Releases

Maintainers create releases by tagging `vX.Y.Z` on `main`. Agents never create
tags or releases; they may suggest the next version (Conventional Commits:
`feat` minor, `fix` patch, `!` breaking; before v1.0.0 breaking changes bump the
minor version).
