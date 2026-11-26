# Development tasks for go-bcadmin.
#
# Usage: make [target]
#   make help   lists the targets.

# renovate: datasource=go depName=golang.org/x/vuln
GOVULNCHECK_VERSION ?= v1.10.0
FUZZTIME ?= 15s

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help.
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: test
test: ## Run the tests with the race detector.
	go test -race -shuffle=on ./...

.PHONY: cover
cover: ## Run the tests and write coverage.html.
	go test -coverpkg=./... -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	go tool cover -func=coverage.out | tail -n 1

.PHONY: fuzz
fuzz: ## Run the fuzz targets for FUZZTIME each (default 15s).
	go test -run='^$$' -fuzz=FuzzResponseErrorDecode -fuzztime=$(FUZZTIME) .
	go test -run='^$$' -fuzz=FuzzBuild -fuzztime=$(FUZZTIME) ./internal/pathutil

.PHONY: integration
integration: ## Run the read-only live tests (needs BCADMIN_TENANT_ID, see README).
	go test -tags integration -count=1 -v ./integration/...

.PHONY: lint
lint: ## Run golangci-lint.
	golangci-lint run ./...

.PHONY: vuln
vuln: ## Check for known vulnerabilities with govulncheck.
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

.PHONY: tidy
tidy: ## Tidy and verify go.mod and go.sum.
	go mod tidy
	go mod verify

.PHONY: check
check: tidy lint test vuln ## Run everything CI runs, except fuzzing.
