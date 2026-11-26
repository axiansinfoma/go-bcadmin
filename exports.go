package bcadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ExportsService exports environment databases to Azure storage. Use it
// through [Client.Exports]. The caller needs the D365 BACKUP/RESTORE
// permission set in the environment.
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
	// read, write, create and delete permissions. It is a secret:
	//   - Validation errors built by this package never include it. Error
	//     responses from the API are passed through unchanged and could echo
	//     it; take care when logging them.
	//   - fmt masks it (with any verb) when it formats the value, a pointer
	//     to it, or an exported field or element containing it. fmt cannot
	//     mask it in an unexported field of another struct, and JSON
	//     encoding (for example by a JSON logger) does not mask it.
	//   - It is sent in the request body, so it appears in the logs when
	//     ClientOptions.Logging.IncludeBody is set.
	StorageAccountSASURI string `json:"storageAccountSasUri"`
	// Container is the name of the container the export creates.
	Container string `json:"container"`
	// Blob is the name of the blob in the container; exports are .bacpac
	// files, so a name ending in ".bacpac" is typical.
	Blob string `json:"blob"`
}

// Start exports the database of the environment family/environment to Azure
// storage. Each export uses one of the monthly exports (see
// [ExportsService.Metrics]). The error code exportFailed is documented as
// typically meaning no paid subscription, no permission or no exports
// left. The documentation does not describe the response; if the
// service returns an operation, it is returned, otherwise the result is nil.
// Track the export with the environment's operations
// ([EnvironmentsService.Operations]).
//
// The request is not retried on server errors (unless
// ClientOptions.RetryNonIdempotent is set), so a failure never starts a
// second export.
//
// POST /exports/applications/{applicationFamily}/environments/{environmentName}.
func (s *ExportsService) Start(ctx context.Context, family, environment string, opts ExportOptions) (*Operation, error) {
	if blank(opts.StorageAccountSASURI) || blank(opts.Container) || blank(opts.Blob) {
		return nil, errors.New("bcadmin: ExportOptions.StorageAccountSASURI, Container and Blob are required")
	}
	u, err := url.Parse(opts.StorageAccountSASURI)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery == "" {
		// Do not include the URI or the parse error: both can contain the
		// SAS token.
		return nil, errors.New("bcadmin: ExportOptions.StorageAccountSASURI must be an https URL with a SAS token")
	}
	var raw rawPayload
	if err := s.c.call(ctx, http.MethodPost, "exports/applications/{}/environments/{}",
		[]string{family, environment}, opts, &raw); err != nil {
		return nil, err
	}
	// The export was accepted (2xx). The response is not documented, so any
	// body that is not an operation is ignored rather than turned into an
	// error that would invite a second export.
	return operationIn(raw), nil
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
	list, err := decodeList[ExportHistoryEntry](raw)
	if err != nil {
		return nil, fmt.Errorf("bcadmin: POST exports/history: %w", cleanErr(err))
	}
	return list, nil
}

// decodeList decodes a JSON array, an object {"value": [...]}, a single
// object (as a list of one) or an empty body (as an empty list). The value
// of "value" must be an array or null; it is not unwrapped again. An exact
// "value" key wins over other casings.
func decodeList[T any](raw json.RawMessage) ([]T, error) {
	return decodeListDepth[T](raw, true)
}

func decodeListDepth[T any](raw json.RawMessage, unwrap bool) ([]T, error) {
	raw = bytes.TrimSpace(raw)
	out := []T{}
	switch {
	case len(raw) == 0 || bytes.Equal(raw, []byte("null")):
		return out, nil
	case raw[0] == '[':
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("decoding response: %w", err)
		}
		return out, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	if !unwrap {
		return nil, errors.New("decoding response: \"value\" is not a list")
	}
	if v, ok := fields["value"]; ok {
		return decodeListDepth[T](v, false)
	}
	for k, v := range fields {
		if strings.EqualFold(k, "value") {
			return decodeListDepth[T](v, false)
		}
	}
	var one T
	if err := json.Unmarshal(raw, &one); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return append(out, one), nil
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

// String returns the options with the SAS token of the URI masked.
func (o ExportOptions) String() string {
	return fmt.Sprintf("{StorageAccountSASURI:%s Container:%s Blob:%s}", maskQuery(o.StorageAccountSASURI), o.Container, o.Blob)
}

// GoString returns the options as Go syntax with the SAS token masked.
func (o ExportOptions) GoString() string {
	return fmt.Sprintf("bcadmin.ExportOptions{StorageAccountSASURI:%q, Container:%q, Blob:%q}", maskQuery(o.StorageAccountSASURI), o.Container, o.Blob)
}

// Format implements [fmt.Formatter] so that every verb prints the masked
// form: %#v prints GoString, %q the quoted String, all other verbs String.
func (o ExportOptions) Format(f fmt.State, verb rune) {
	s := o.String()
	switch {
	case verb == 'v' && f.Flag('#'):
		s = o.GoString()
	case verb == 'q':
		s = strconv.Quote(s)
	}
	_, _ = io.WriteString(f, s)
}

// maskQuery returns u with its query replaced by "REDACTED", or "REDACTED"
// if u cannot be parsed.
func maskQuery(u string) string {
	if u == "" {
		return ""
	}
	p, err := url.Parse(u)
	if err != nil {
		return "REDACTED"
	}
	if p.RawQuery != "" {
		p.RawQuery = "REDACTED"
	}
	p.User = nil
	p.Fragment = ""
	return p.String()
}

// operationIn returns the operation in body, or nil if body is not a JSON
// object with an operation ID.
func operationIn(body []byte) *Operation {
	body = bytes.TrimSpace(body)
	var op Operation
	if len(body) == 0 || body[0] != '{' {
		return nil
	}
	if err := json.Unmarshal(body, &op); err != nil || op.ID == "" {
		return nil
	}
	return &op
}
