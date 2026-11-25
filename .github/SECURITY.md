# Security Policy

go-bcadmin is a client for the Business Central Administration Center API. The
applications that use it typically hold the `AdminCenter.ReadWrite.All`
permission and can create, copy and permanently delete Business Central
environments. We take reports about it seriously and would rather hear about a
suspected problem than not.

## Reporting a Vulnerability

**Please do not open a public issue, discussion or pull request for a security
problem.**

Report it privately through GitHub's private vulnerability reporting:

**[Report a vulnerability](https://github.com/axiansinfoma/go-bcadmin/security/advisories/new)**

(Repository → *Security* → *Advisories* → *Report a vulnerability*.) The report
is visible only to you and the maintainers, and the advisory is where we
coordinate the fix and the disclosure with you.

Please include:

- The module version (or commit).
- The affected function, type or option.
- A minimal program that reproduces the problem.
- What you observed and why you believe it is a security problem.

Please **do not** include live secrets, access tokens or tenant identifiers in a
report. A redacted excerpt is enough; we will ask if we need more.

### What to expect

| Stage | Target |
| --- | --- |
| Acknowledgement of your report | 5 working days |
| Initial assessment | 10 working days |
| Fix released, or a public status update if it takes longer | 90 days |

This is a small project maintained alongside other work; these are targets, not
a contractual commitment. We will credit you in the advisory unless you ask us
not to, and agree a disclosure date with you before publishing. We do not
operate a bug bounty.

## Scope

**In scope:**

- Anything that could send an access token to a host other than the configured
  API endpoint, or over plain HTTP without the explicit opt-in.
- Request construction that lets caller-supplied values (environment names, app
  IDs, ...) change the request path or target another resource.
- Retries that could repeat a non-idempotent operation.
- The `bcadmintest` package, where it could weaken the security of code that
  does not use it in tests.

**Out of scope** (report to their owners instead):

- The Business Central Admin Center API itself →
  [Microsoft](https://msrc.microsoft.com/create-report).
- The Azure SDK for Go (`azcore`, `azidentity`) →
  [Azure SDK for Go](https://github.com/Azure/azure-sdk-for-go/security/policy).
- Vulnerabilities in dependencies that go-bcadmin does not reach.

## Supported Versions

Only the latest release is supported. Fixes are released from `main`; there are
no backports.

## Security Properties You Can Rely On

- **Tokens go only to the configured endpoint.** Requests use HTTPS (TLS 1.2 or
  later with the default transport). A custom endpoint must come with its own
  token audience. An `http://` endpoint is rejected unless
  `InsecureAllowCredentialWithHTTP` is set. `@odata.nextLink` URLs and redirects
  that point to another scheme or host are not followed. A custom
  `ClientOptions.Transport` is responsible for its own redirect policy.
- **Bounded responses.** Response bodies larger than `MaxResponseBytes`
  (default 64 MiB) are rejected.
- **Caller-supplied path segments cannot change the path.** Each segment is
  validated (it must not be empty, equal to `.` or `..`, or contain `/`, `\` or
  control characters) and escaped. `Client.Do` takes a caller-built relative
  path and query; it rejects absolute URLs and `.`/`..` segments, but escaping
  the values in it is the caller's responsibility (values must be escaped
  exactly once).
- **Non-idempotent requests are not repeated.** POST, PATCH, creating PUT
  requests and requests sent with `Client.Do` other than GET, HEAD and OPTIONS
  are only retried when the service signals that it did not process them (429,
  or 503 with `Retry-After`) or the connection could not be established,
  unless `RetryNonIdempotent` is set.
- **No secrets in errors.** Error messages contain the request method, path and
  the API's error body, never request headers or tokens. Control and format
  characters in server text are replaced in the messages of `ResponseError`,
  `OperationError` and poller errors; request paths in error messages are
  shown percent-encoded. `ResponseError.Header` holds all response headers;
  avoid logging it as a whole.
