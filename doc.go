// Package bcadmin is a client for the Microsoft Dynamics 365 Business Central
// Administration Center API.
//
// The package wraps the REST API at
// https://api.businesscentral.dynamics.com/admin/{version} in typed services and
// objects, so callers work with environments, apps and operations instead of
// URLs, tokens and polling loops.
//
// # Authentication
//
// A [Client] takes any [azcore.TokenCredential], for example one from the
// azidentity module. Tokens are requested for the scope
// https://api.businesscentral.dynamics.com/.default, cached and refreshed by the
// client. For service-to-service authentication the Microsoft Entra app needs
// the AdminCenter.ReadWrite.All application permission and must be authorized
// in the Business Central admin center.
//
//	cred, err := azidentity.NewClientSecretCredential(tenantID, clientID, secret, nil)
//	if err != nil { ... }
//	client, err := bcadmin.NewClient(cred, nil)
//
// # Long-running operations
//
// Operations that run in Business Central are tracked with a [Poller]. Call
// [Poller.Wait] to block until the operation has finished, or [Poller.Poll] to
// drive the polling yourself. A poller can be recreated from an operation ID
// with [TenantService.ResumeOperation], so a process can pick up an operation
// another process started.
//
// # Errors
//
// Every non-successful HTTP response is returned as a [*ResponseError] that
// carries the HTTP status and the error details from the response body
// (code, message, target, client errors, ...) exactly as the API sent them.
// A failed or canceled operation is returned as an [*OperationError] with the
// operation's error message. Use [errors.As] to inspect them and [errors.Is]
// with [ErrNotFound], [ErrConflict] or [ErrThrottled] for common checks.
// Text from the API is kept unchanged in the fields; [ResponseError.Error]
// replaces control characters so it is safe to log.
//
// # Retries
//
// Requests are retried on throttling (429) and transient server errors with
// exponential backoff, honoring the Retry-After header. Requests that are not
// idempotent (POST, PATCH, and PUT requests that create resources) are only
// retried when the service rejected them before processing (429, or 503 with
// a Retry-After header) or the connection could not be established, so an
// operation is not started twice by a retry. See [ClientOptions].
//
// # Transport security
//
// The default transport requires TLS 1.2, refuses plain HTTP endpoints unless
// explicitly allowed, and follows redirects only within the endpoint's scheme
// and host; @odata.nextLink URLs to other hosts are refused. Response bodies
// are limited to ClientOptions.MaxResponseBytes.
//
// # Concurrency
//
// A [Client] and its services are safe for concurrent use by multiple
// goroutines. A [Poller] is not; use one poller per goroutine.
package bcadmin
