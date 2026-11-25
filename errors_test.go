package bcadmin_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/axiansinfoma/go-bcadmin"
	"github.com/axiansinfoma/go-bcadmin/bcadmintest"
)

func TestResponseErrorPassThrough(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		fixture     string
		body        string
		status      int
		header      http.Header
		wantCode    string
		wantMessage string
		wantInError []string
		check       func(t *testing.T, re *bcadmin.ResponseError)
	}{
		{
			name:        "flat",
			fixture:     "testdata/errors/flat.json",
			status:      http.StatusBadRequest,
			wantCode:    "EnvironmentNameInvalid",
			wantMessage: "The environment name 'x y' is invalid. Names can contain only letters, digits and dashes.",
			wantInError: []string{"400 Bad Request", "EnvironmentNameInvalid", "The environment name 'x y' is invalid", "(target: environmentName)"},
			check: func(t *testing.T, re *bcadmin.ResponseError) {
				if re.Target != "environmentName" {
					t.Errorf("Target = %q", re.Target)
				}
			},
		},
		{
			name:        "odata wrapped",
			fixture:     "testdata/errors/odata.json",
			status:      http.StatusForbidden,
			wantCode:    "Forbidden",
			wantMessage: "The application is not authorized to access this tenant.",
			wantInError: []string{"403 Forbidden", "Forbidden: The application is not authorized"},
			check: func(t *testing.T, re *bcadmin.ResponseError) {
				if re.InnerError["code"] != "AppNotAuthorized" {
					t.Errorf("InnerError = %v", re.InnerError)
				}
			},
		},
		{
			name:        "client errors",
			fixture:     "testdata/errors/clienterror.json",
			status:      http.StatusBadRequest,
			wantCode:    "InvalidInput",
			wantMessage: "One or more values are invalid.",
			wantInError: []string{"client errors: InvalidCountryCode: Country code 'XX' is not supported (target: countryCode)", "InvalidRing: Ring 'FOO' does not exist"},
			check: func(t *testing.T, re *bcadmin.ResponseError) {
				if len(re.ClientErrors) != 2 || re.ClientErrors[0].Code != "InvalidCountryCode" {
					t.Errorf("ClientErrors = %+v", re.ClientErrors)
				}
				if re.ExtensionData["environmentName"] != "mysandbox" {
					t.Errorf("ExtensionData = %v", re.ExtensionData)
				}
			},
		},
		{
			name:        "requirements",
			fixture:     "testdata/errors/requirements.json",
			status:      http.StatusBadRequest,
			wantCode:    "EntityValidationFailed",
			wantMessage: "The app cannot be installed because of missing dependencies.",
			wantInError: []string{"requirements: Contoso Base 1.2.3.4 (00000000-0000-0000-0000-000000000002)"},
			check: func(t *testing.T, re *bcadmin.ResponseError) {
				if re.Data == nil || len(re.Data.Requirements) != 1 || re.Data.Requirements[0].Publisher != "Contoso" {
					t.Errorf("Data = %+v", re.Data)
				}
			},
		},
		{
			name:        "html 502",
			body:        "<html><body><h1>502 Bad Gateway</h1></body></html>",
			status:      http.StatusBadGateway,
			wantInError: []string{"502 Bad Gateway", "<h1>502 Bad Gateway</h1>"},
		},
		{
			name:        "empty body",
			status:      http.StatusConflict,
			wantInError: []string{"409 Conflict"},
		},
		{
			name:        "unexpected field types",
			body:        `{"code": 42, "message": "numeric code", "clientError": "not a list"}`,
			status:      http.StatusBadRequest,
			wantCode:    "42",
			wantMessage: "numeric code",
			wantInError: []string{"42: numeric code"},
		},
		{
			name:        "wrapped with unexpected field types",
			body:        `{"error":{"code":"EnvironmentNotFound","message":"gone","details":{"code":"d1","message":"single detail"},"innererror":"text"}}`,
			status:      http.StatusBadRequest,
			wantCode:    "EnvironmentNotFound",
			wantMessage: "gone",
			wantInError: []string{"EnvironmentNotFound: gone", "details: d1: single detail"},
			check: func(t *testing.T, re *bcadmin.ResponseError) {
				if re.InnerError["value"] != "text" {
					t.Errorf("InnerError = %v", re.InnerError)
				}
				if !bcadmin.IsNotFound(re) {
					t.Error("IsNotFound = false")
				}
			},
		},
		{
			name:        "flat keeps target when another field has an unexpected type",
			body:        `{"code":"X","message":"Y","target":"t","clientError":{"code":"a","message":"b"},"data":"oops"}`,
			status:      http.StatusBadRequest,
			wantCode:    "X",
			wantMessage: "Y",
			wantInError: []string{"X: Y (target: t)", "client errors: a: b"},
		},
		{
			name:        "nested client errors and a bad entry",
			body:        `{"code":"invalidInput","message":"Invalid input.","clientError":[{"code":1,"message":"numeric code","extensionData":{"field":"name"}},{"code":"outer","message":"outer msg","clientError":[{"code":"inner","message":"inner msg"}]}]}`,
			status:      http.StatusBadRequest,
			wantCode:    "invalidInput",
			wantMessage: "Invalid input.",
			wantInError: []string{"client errors: 1: numeric code, outer: outer msg [client errors: inner: inner msg]"},
			check: func(t *testing.T, re *bcadmin.ResponseError) {
				if len(re.ClientErrors) != 2 || re.ClientErrors[0].ExtensionData["field"] != "name" ||
					len(re.ClientErrors[1].ClientErrors) != 1 || re.ClientErrors[1].ClientErrors[0].Code != "inner" {
					t.Errorf("ClientErrors = %+v", re.ClientErrors)
				}
			},
		},
		{
			name:        "top-level fields kept next to a wrapped error",
			body:        `{"code":"Top","message":"top msg","error":{"target":"x"}}`,
			status:      http.StatusBadRequest,
			wantCode:    "Top",
			wantMessage: "top msg",
			wantInError: []string{"Top: top msg (target: x)"},
		},
		{
			name:        "requirement with an unexpected field type",
			body:        `{"code":"C","message":"M","data":{"requirements":[{"appId":"a","name":"n","version":1}]}}`,
			status:      http.StatusBadRequest,
			wantCode:    "C",
			wantMessage: "M",
			wantInError: []string{"requirements: n 1 (a)"},
		},
		{
			name:        "wrapped with only innererror",
			body:        `{"error":{"innererror":{"code":"Deep"}}}`,
			status:      http.StatusBadRequest,
			wantInError: []string{`{"error":{"innererror":{"code":"Deep"}}}`},
			check: func(t *testing.T, re *bcadmin.ResponseError) {
				if re.InnerError["code"] != "Deep" {
					t.Errorf("InnerError = %v", re.InnerError)
				}
			},
		},
		{
			name:        "control characters are replaced in the error text",
			body:        `{"code":"Bad","message":"line1\nline2\u001b[31m"}`,
			status:      http.StatusBadRequest,
			wantCode:    "Bad",
			wantMessage: "line1\nline2\u001b[31m",
			wantInError: []string{"Bad: line1 line2 [31m"},
		},
		{
			name:        "request id",
			body:        `{"code":"Unknown","message":"Something went wrong."}`,
			status:      http.StatusBadRequest,
			header:      http.Header{"Request-Id": {"11111111-2222-3333-4444-555555555555"}},
			wantCode:    "Unknown",
			wantMessage: "Something went wrong.",
			wantInError: []string{"[request-id: 11111111-2222-3333-4444-555555555555]"},
			check: func(t *testing.T, re *bcadmin.ResponseError) {
				if re.RequestID != "11111111-2222-3333-4444-555555555555" {
					t.Errorf("RequestID = %q", re.RequestID)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := tt.body
			if tt.fixture != "" {
				b, err := os.ReadFile(tt.fixture)
				if err != nil {
					t.Fatal(err)
				}
				body = string(b)
			}
			srv := bcadmintest.NewServer(t)
			srv.Fail(bcadmintest.Fault{Path: "environments/quotas", Status: tt.status, Body: body, Header: tt.header})
			_, err := srv.NewClient(t).Tenant().Quotas(context.Background())
			re, ok := errors.AsType[*bcadmin.ResponseError](err)
			if !ok {
				t.Fatalf("error %v (%T) is not a *ResponseError", err, err)
			}
			if re.StatusCode != tt.status {
				t.Errorf("StatusCode = %d, want %d", re.StatusCode, tt.status)
			}
			if re.Method != http.MethodGet || re.Path != "/admin/v2.29/environments/quotas" {
				t.Errorf("Method, Path = %q, %q", re.Method, re.Path)
			}
			if re.Code != tt.wantCode || re.Message != tt.wantMessage {
				t.Errorf("Code, Message = %q, %q; want %q, %q", re.Code, re.Message, tt.wantCode, tt.wantMessage)
			}
			if string(re.RawBody) != body {
				t.Errorf("RawBody = %q, want %q", re.RawBody, body)
			}
			for _, s := range tt.wantInError {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("Error() = %q, want it to contain %q", err.Error(), s)
				}
			}
			if tt.check != nil {
				tt.check(t, re)
			}
		})
	}
}

func TestResponseErrorLargeJSONBodyIsDecoded(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	body := `{"code":"invalidInput","message":"too many problems","extensionData":{"pad":"` + strings.Repeat("x", 2<<20) + `"}}`
	srv.Fail(bcadmintest.Fault{Path: "environments/quotas", Status: http.StatusBadRequest, Body: body})
	_, err := srv.NewClient(t).Tenant().Quotas(context.Background())
	re, ok := errors.AsType[*bcadmin.ResponseError](err)
	if !ok || re.Code != "invalidInput" || re.Message != "too many problems" || len(re.RawBody) != 1<<20 {
		t.Errorf("error = %v", err)
	}
}

func TestResponseErrorLargeBody(t *testing.T) {
	t.Parallel()
	srv := bcadmintest.NewServer(t)
	big := strings.Repeat("x", 2<<20)
	srv.Fail(bcadmintest.Fault{Path: "environments/quotas", Status: http.StatusInternalServerError, Body: big})
	c, err := bcadmin.NewClient(bcadmintest.Credential(), noRetry(srv))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Tenant().Quotas(context.Background())
	re, ok := errors.AsType[*bcadmin.ResponseError](err)
	if !ok {
		t.Fatalf("error %v is not a *ResponseError", err)
	}
	if len(re.RawBody) != 1<<20 {
		t.Errorf("len(RawBody) = %d, want %d", len(re.RawBody), 1<<20)
	}
	if !strings.Contains(err.Error(), "(truncated, see RawBody)") || len(err.Error()) > 4096 {
		t.Errorf("Error() has length %d and does not mark truncation", len(err.Error()))
	}
}

func TestErrorSentinels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		status    int
		body      string
		notFound  bool
		conflict  bool
		throttled bool
		code      string
	}{
		{status: http.StatusNotFound, notFound: true},
		{status: http.StatusBadRequest, body: `{"code":"EnvironmentNotFound","message":"Environment 'x' was not found."}`, notFound: true, code: "EnvironmentNotFound"},
		{status: http.StatusConflict, conflict: true},
		{status: http.StatusTooManyRequests, throttled: true},
		{status: http.StatusBadRequest, body: `{"code":"InvalidInput"}`, code: "InvalidInput"},
		{status: http.StatusBadRequest, body: `{"code":"TargetServiceNotFound"}`, code: "TargetServiceNotFound"},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.status)+tt.code, func(t *testing.T) {
			t.Parallel()
			srv := bcadmintest.NewServer(t)
			srv.Fail(bcadmintest.Fault{Status: tt.status, Body: tt.body})
			c, err := bcadmin.NewClient(bcadmintest.Credential(), noRetry(srv))
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Tenant().Quotas(context.Background())
			if got := bcadmin.IsNotFound(err); got != tt.notFound {
				t.Errorf("IsNotFound = %v", got)
			}
			if got := bcadmin.IsConflict(err); got != tt.conflict {
				t.Errorf("IsConflict = %v", got)
			}
			if got := bcadmin.IsThrottled(err); got != tt.throttled {
				t.Errorf("IsThrottled = %v", got)
			}
			if got := bcadmin.ErrorCode(err); got != tt.code {
				t.Errorf("ErrorCode = %q, want %q", got, tt.code)
			}
		})
	}
	if bcadmin.ErrorCode(errors.New("plain")) != "" {
		t.Error("ErrorCode of a plain error is not empty")
	}
}

func TestOperationError(t *testing.T) {
	t.Parallel()
	e := &bcadmin.OperationError{Operation: &bcadmin.Operation{
		ID: "op1", Type: bcadmin.OperationTypeCopy, Status: "failed", ErrorMessage: "The copy failed: database is too large.",
	}}
	want := "bcadmin: operation op1 (Copy) ended with status failed: The copy failed: database is too large."
	if e.Error() != want {
		t.Errorf("Error() = %q, want %q", e.Error(), want)
	}
	if e.Canceled() {
		t.Error("Canceled() = true for a failed operation")
	}
	injected := &bcadmin.OperationError{Operation: &bcadmin.Operation{ID: "op\n3", Status: "Failed", ErrorMessage: "bad\n\x1b[31mforged\u202e"}}
	if got := injected.Error(); strings.ContainsAny(got, "\n\x1b\u202e") {
		t.Errorf("Error() = %q contains control or format characters", got)
	}
	c := &bcadmin.OperationError{Operation: &bcadmin.Operation{ID: "op2", Status: "Cancelled"}}
	if !c.Canceled() {
		t.Error("Canceled() = false for status Cancelled")
	}
	if (&bcadmin.OperationError{}).Error() == "" {
		t.Error("Error() of an empty OperationError is empty")
	}
}

// noRetry returns the server's client options with retries disabled.
func noRetry(srv *bcadmintest.Server) *bcadmin.ClientOptions {
	o := srv.ClientOptions()
	o.Retry.MaxRetries = -1
	return o
}
