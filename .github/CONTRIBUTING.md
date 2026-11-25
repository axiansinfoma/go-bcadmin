# Contributing

Thanks for helping with go-bcadmin. Bug reports, fixes and new endpoints are
welcome.

## Before you start

- For larger changes, open an issue first so we can agree on the API shape.
- Security problems go through [private vulnerability
  reporting](https://github.com/axiansinfoma/go-bcadmin/security/advisories/new),
  not public issues.

## Development

Requirements: the Go version in `go.mod` or newer and
[golangci-lint](https://golangci-lint.run/) v2.

```sh
make test    # go test -race -shuffle=on ./...
make lint    # golangci-lint run
make vuln    # govulncheck ./...
make tidy    # go mod tidy
make help    # all targets
```

Tests run against the fake server in `bcadmintest`; they need no network access
or credentials.

## Adding or changing an endpoint

1. Check the endpoint in the [Admin Center API
   documentation](https://learn.microsoft.com/en-us/dynamics365/business-central/dev-itpro/administration/administration-center-api)
   and note the API version it was introduced in.
2. Add the request and response types and the method to the file of its area.
   Follow the conventions in [AGENTS.md](../AGENTS.md): `ctx` first, typed
   results, a `Poller` for long-running operations, the documented JSON casing.
3. Add a JSON fixture under `testdata/` based on the documented sample and a test
   that checks the method, path, request body and decoded response.
4. Add the endpoint to the coverage table in [README.md](../README.md).

## Pull requests

- Use [Conventional Commits](https://www.conventionalcommits.org/) for the PR
  title (`feat: ...`, `fix: ...`, `docs: ...`). PRs are squash-merged, so the
  title becomes the commit message.
- Keep test data free of real tenant IDs, environment names, email addresses and
  secrets. Use placeholder GUIDs and `contoso.com` / `example.com`.
- CI must pass: lint, tests on the two latest Go releases, govulncheck, CodeQL
  and secret scanning.

## Releases

Maintainers create releases by tagging `vX.Y.Z` on `main` following Semantic
Versioning. Contributors don't need to bump versions.
