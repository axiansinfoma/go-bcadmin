package bcadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ExportsService exports environment databases to Azure storage. Use it
// through [Client.Exports]. The caller needs the D365 BACKUP/RESTORE
// permission set in the environment, and the tenant a paid subscription.
type ExportsService struct {
	c *Client
}

// Exports returns the database export endpoints.
func (c *Client) Exports() *ExportsService { return &ExportsService{c: c} }

// ExportMetrics is the monthly database export allowance of an environment.
type ExportMetrics struct {
	// ExportsPerMonth is the number of exports allowed every month.
	ExportsPerMonth int `json:"exportsPerMonth"`
	// ExportsRemainingThisMonth is the number of exports left this month.
	ExportsRemainingThisMonth int `json:"exportsRemainingThisMonth"`
}

// Metrics returns the export allowance of the environment
// family/environment.
//
// GET /exports/applications/{applicationFamily}/environments/{environmentName}/metrics.
func (s *ExportsService) Metrics(ctx context.Context, family, environment string) (*ExportMetrics, error) {
	var m ExportMetrics
	if err := s.c.call(ctx, http.MethodGet, "exports/applications/{}/environments/{}/metrics",
		[]string{family, environment}, nil, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// ExportOptions selects where an environment database is exported to.
type ExportOptions struct {
	// StorageAccountSASURI is a SAS URI of the Azure storage account with
	// read, write, create and delete permissions. It is a secret: it is sent
	// in the request body and never included in errors from this package.
	StorageAccountSASURI string `json:"storageAccountSasUri"`
	// Container is the name of the container the export creates.
	Container string `json:"container"`
	// Blob is the name of the blob in the container; exports are .bacpac
	// files, so a name ending in ".bacpac" is typical.
	Blob string `json:"blob"`
}

// Start exports the database of the environment family/environment to Azure
// storage. Each export uses one of the monthly exports (see
// [ExportsService.Metrics]); the service refuses an export without a paid
// subscription, without permission or without exports left (code
// exportFailed). The documentation does not describe the response; if the
// service returns an operation, it is returned, otherwise the result is nil.
// Track the export with the environment's operations
// ([EnvironmentsService.Operations]).
//
// The request is not retried on server errors, so a failure never starts a
// second export.
//
// POST /exports/applications/{applicationFamily}/environments/{environmentName}.
func (s *ExportsService) Start(ctx context.Context, family, environment string, opts ExportOptions) (*Operation, error) {
	if opts.StorageAccountSASURI == "" || opts.Container == "" || opts.Blob == "" {
		return nil, errors.New("bcadmin: ExportOptions.StorageAccountSASURI, Container and Blob are required")
	}
	u, err := url.Parse(opts.StorageAccountSASURI)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery == "" {
		// Do not include the URI or the parse error: both can contain the
		// SAS token.
		return nil, errors.New("bcadmin: ExportOptions.StorageAccountSASURI must be an https URL with a SAS token")
	}
	var op *Operation
	if err := s.c.call(ctx, http.MethodPost, "exports/applications/{}/environments/{}",
		[]string{family, environment}, opts, &op); err != nil {
		return nil, err
	}
	if op != nil && op.ID == "" {
		return nil, nil
	}
	return op, nil
}

// ExportHistoryEntry is a database export of the tenant.
type ExportHistoryEntry struct {
	// ApplicationType is the family of the environment, for example
	// "BusinessCentral".
	ApplicationType    string `json:"applicationType"`
	ApplicationVersion string `json:"applicationVersion"`
	Blob               string `json:"blob"`
	Container          string `json:"container"`
	// Country is the country code of the environment.
	Country         string `json:"country"`
	EnvironmentName string `json:"environmentName"`
	StorageAccount  string `json:"storageAccount"`
	Time            *Time  `json:"time,omitempty"`
	// User is the user who started the export.
	User string `json:"user"`
}

// History returns the database exports of the tenant in the time window
// start to end.
//
// Deprecated: the endpoint is supported up to API v2.29 and deprecated from
// v2.30; use the environment operations instead.
//
// POST /exports/history?start={startTime}&end={endTime}.
func (s *ExportsService) History(ctx context.Context, start, end time.Time) ([]ExportHistoryEntry, error) {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return nil, errors.New("bcadmin: export history needs a start before its end")
	}
	if s.c.apiVersionAtLeast(2, 30) {
		return nil, fmt.Errorf("bcadmin: export history is deprecated from API v2.30, client uses %s; use the environment operations", s.c.apiVersion)
	}
	query := url.Values{
		"start": {start.UTC().Format(time.RFC3339)},
		"end":   {end.UTC().Format(time.RFC3339)},
	}
	var raw json.RawMessage
	if err := s.c.callQuery(ctx, http.MethodPost, "exports/history", nil, query, nil, &raw); err != nil {
		return nil, err
	}
	return decodeList[ExportHistoryEntry](raw)
}

// decodeList decodes a JSON array, an object {"value": [...]} or an empty
// body.
func decodeList[T any](raw json.RawMessage) ([]T, error) {
	raw = bytes.TrimSpace(raw)
	var out []T
	switch {
	case len(raw) == 0 || bytes.Equal(raw, []byte("null")):
		return out, nil
	case raw[0] == '[':
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("bcadmin: decoding response: %w", err)
		}
	default:
		var wrapped struct {
			Value []T `json:"value"`
		}
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			return nil, fmt.Errorf("bcadmin: decoding response: %w", err)
		}
		out = wrapped.Value
	}
	return out, nil
}

// ExportDatabase exports the environment's database; see
// [ExportsService.Start].
func (e *Environment) ExportDatabase(ctx context.Context, opts ExportOptions) (*Operation, error) {
	s, err := e.service()
	if err != nil {
		return nil, err
	}
	return s.c.Exports().Start(ctx, e.ApplicationFamily, e.Name, opts)
}

// ExportMetrics returns the environment's export allowance; see
// [ExportsService.Metrics].
func (e *Environment) ExportMetrics(ctx context.Context) (*ExportMetrics, error) {
	s, err := e.service()
	if err != nil {
		return nil, err
	}
	return s.c.Exports().Metrics(ctx, e.ApplicationFamily, e.Name)
}

// trimmed reports whether s is empty after trimming spaces.
func trimmed(s string) bool { return strings.TrimSpace(s) == "" }
