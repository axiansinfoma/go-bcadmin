# AGENTS.md

Guidance for coding agents working on go-bcadmin, a public Go client for the
Business Central Administration Center API. Read README.md for the user view.

## Structure

| Path | Content |
| --- | --- |
| `doc.go` | Package documentation. |
| `client.go` | `Client`, `NewClient`, `ClientOptions`, `ForTenant`. Builds the azcore pipeline (bearer token, retry, telemetry). |
| `transport.go` | Default HTTP client (TLS 1.2, same-origin redirects) and the response size limit. |
| `request.go` | `Do`, path building, request sending, error mapping, retry rules for POST/PATCH, `@odata.nextLink` pagination (`list`, `listAll`). |
| `errors.go` | `ResponseError` (API error body pass-through), sentinels, `OperationError`. |
| `operation.go`, `poller.go` | `Operation`, statuses and types; generic `Poller[T]`. |
| `tenant.go`, `applications.go`, ... | One file per API area with its types and service. |
| `version.go` | `DefaultAPIVersion`; module version for the User-Agent from build info. |
| `bcadmintest/` | Public fake API server for tests (ours and users'). |
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
  escaped. Never build paths with `fmt.Sprintf`.
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
- Without a custom Transport, `newDefaultHTTPClient` refuses cross-origin
  redirects; 3xx responses that reach `send` become `*ResponseError`.
- Response bodies are capped by `limitTransport` (`MaxResponseBytes`); the
  error is non-retriable for azcore. azcore reads bodies inside the pipeline,
  and its bearer token policy returns a nil response when a later policy
  fails, so `tooLargeError` carries the response; `send` uses it to return a
  `*ResponseError` with status and headers.
- `ErrNotFound` matches 404 and the code `EnvironmentNotFound` only; do not
  widen it to every code ending in "NotFound" (some mean "service not found").
- `@odata.nextLink` must point to the configured endpoint; others are refused
  so the token is never sent elsewhere.
- `Retry-After` larger than `Retry.MaxRetryDelay` stops retrying (azcore
  behavior).
- azcore treats `RetryDelay: -1` as zero delay and its overflow check then uses
  the maximum delay; tests should use small positive delays.
- `GORELEASE_VERSION` in ci.yml is a pseudo-version (golang.org/x/exp has no
  tags); check that Renovate proposes updates for it, otherwise bump it by
  hand.
- `bcadmintest` patterns use `http.ServeMux` syntax; `applications/` needs
  `applications/{$}` to match only that path.
- This repository is public. Never add tenant IDs, environment or customer
  names, internal URLs, email addresses or secrets to code, tests, fixtures,
  commit messages or PR descriptions. Test data uses placeholder GUIDs
  (`00000000-0000-0000-0000-00000000000x`), `contoso.com` and `example.com`.

## Releases

Maintainers create releases by tagging `vX.Y.Z` on `main`. Agents never create
tags or releases; they may suggest the next version (Conventional Commits:
`feat` minor, `fix` patch, `!` breaking; before v1.0.0 breaking changes bump the
minor version).
