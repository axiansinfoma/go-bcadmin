package bcadmin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Sentinel errors for use with [errors.Is]. A [*ResponseError] matches them
// based on its HTTP status and error code.
var (
	// ErrNotFound matches responses with HTTP status 404 and responses whose
	// error code ends in "NotFound" (for example "EnvironmentNotFound"), which
	// the API also returns with other status codes.
	ErrNotFound = errors.New("bcadmin: not found")

	// ErrConflict matches responses with HTTP status 409.
	ErrConflict = errors.New("bcadmin: conflict")

	// ErrThrottled matches responses with HTTP status 429, returned when the
	// retries configured in ClientOptions.Retry are used up.
	ErrThrottled = errors.New("bcadmin: throttled")

	// ErrOperationScheduled is returned by [Poller.Wait] when
	// WaitOptions.TreatScheduledAsDone is set and the operation has been
	// scheduled to run later (for example in the environment's update window).
	ErrOperationScheduled = errors.New("bcadmin: operation scheduled")
)

// maxErrorBody is the largest number of bytes of an error response body kept
// in ResponseError.RawBody.
const maxErrorBody = 1 << 20

// maxErrorBodyInMessage is the largest number of bytes of a raw (non-JSON)
// error body included in the text of ResponseError.Error.
const maxErrorBodyInMessage = 2048

// ErrorDetail is one entry of the nested error lists of an error response
// ("clientError", "details").
type ErrorDetail struct {
	Code    string        `json:"code,omitempty"`
	Message string        `json:"message,omitempty"`
	Target  string        `json:"target,omitempty"`
	Details []ErrorDetail `json:"details,omitempty"`
}

// ErrorData holds the "data" object of an error response. App management
// endpoints use it to list the apps an operation depends on.
type ErrorData struct {
	Requirements []AppRequirement `json:"requirements,omitempty"`
}

// AppRequirement describes an app that another app depends on, or that
// depends on it, as returned in app management error responses and in
// available updates.
type AppRequirement struct {
	AppID     string `json:"appId,omitempty"`
	Name      string `json:"name,omitempty"`
	Publisher string `json:"publisher,omitempty"`
	Version   string `json:"version,omitempty"`
	Type      string `json:"type,omitempty"`
}

// ResponseError is returned for every HTTP response with a status code that
// indicates failure. It carries the error details from the response body as
// the API sent them.
//
// The Admin Center API returns errors either as a flat object
// ({"code": ..., "message": ...}) or wrapped in an "error" object
// ({"error": {"code": ..., "message": ...}}); both are decoded into the same
// fields. When the body is not JSON, Code and Message are empty and the body is
// available in RawBody.
type ResponseError struct {
	// StatusCode is the HTTP status code, for example 400.
	StatusCode int
	// Status is the HTTP status line, for example "400 Bad Request".
	Status string
	// Method is the HTTP method of the request.
	Method string
	// Path is the URL path of the request, without query parameters.
	Path string
	// RequestID is the value of the first of the response headers
	// "request-id", "x-ms-request-id", "ms-correlation-x-ms-id" and
	// "x-ms-correlation-id" that is present. Include it when contacting
	// Microsoft support.
	RequestID string
	// Header contains the response headers.
	Header http.Header

	// Code is the error code from the response body, for example
	// "EnvironmentNotFound".
	Code string
	// Message is the error message from the response body.
	Message string
	// Target is the request element the error refers to, if any.
	Target string
	// ClientErrors lists the nested errors of the "clientError" field.
	ClientErrors []ErrorDetail
	// Details lists the nested errors of the "details" field.
	Details []ErrorDetail
	// ExtensionData holds the "extensionData" field.
	ExtensionData map[string]any
	// InnerError holds the "innererror" field.
	InnerError map[string]any
	// Data holds the "data" field, for example the requirements of an app
	// operation that failed because of missing dependencies.
	Data *ErrorData

	// RawBody is the response body, up to 1 MiB.
	RawBody []byte
}

// errorBody is the wire form of an error response body.
type errorBody struct {
	Code          string         `json:"code"`
	Message       string         `json:"message"`
	Target        string         `json:"target"`
	ClientError   []ErrorDetail  `json:"clientError"`
	Details       []ErrorDetail  `json:"details"`
	ExtensionData map[string]any `json:"extensionData"`
	InnerError    map[string]any `json:"innererror"`
	Data          *ErrorData     `json:"data"`
}

func (b *errorBody) empty() bool {
	return b.Code == "" && b.Message == "" && b.Target == "" && len(b.ClientError) == 0 && len(b.Details) == 0
}

// newResponseError builds a ResponseError from resp and its body, keeping at
// most maxErrorBody bytes of the body.
func newResponseError(resp *http.Response, body []byte) *ResponseError {
	e := &ResponseError{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Header:     resp.Header,
	}
	if resp.Request != nil {
		e.Method = resp.Request.Method
		if resp.Request.URL != nil {
			e.Path = resp.Request.URL.Path
		}
	}
	for _, h := range []string{"request-id", "x-ms-request-id", "ms-correlation-x-ms-id", "x-ms-correlation-id"} {
		if v := resp.Header.Get(h); v != "" {
			e.RequestID = v
			break
		}
	}
	if len(body) > maxErrorBody {
		body = body[:maxErrorBody]
	}
	e.RawBody = body
	e.decodeBody()
	return e
}

// decodeBody fills the error fields from RawBody, accepting the flat and the
// wrapped ("error") shape.
func (e *ResponseError) decodeBody() {
	trimmed := bytes.TrimSpace(e.RawBody)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return
	}
	var wrapped struct {
		Error *errorBody `json:"error"`
	}
	var body errorBody
	if err := json.Unmarshal(trimmed, &wrapped); err == nil && wrapped.Error != nil && !wrapped.Error.empty() {
		body = *wrapped.Error
	} else if err := json.Unmarshal(trimmed, &body); err != nil {
		// Some fields may have unexpected types; fall back to code and
		// message only so the API's message still reaches the caller.
		var minimal struct {
			Code    any `json:"code"`
			Message any `json:"message"`
		}
		if json.Unmarshal(trimmed, &minimal) == nil {
			body = errorBody{Code: stringify(minimal.Code), Message: stringify(minimal.Message)}
		}
	}
	e.Code = body.Code
	e.Message = body.Message
	e.Target = body.Target
	e.ClientErrors = body.ClientError
	e.Details = body.Details
	e.ExtensionData = body.ExtensionData
	e.InnerError = body.InnerError
	e.Data = body.Data
}

func stringify(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// Error returns the HTTP status and the error details of the response body,
// for example
//
//	bcadmin: PUT /admin/v2.29/applications/BusinessCentral/environments/x: 400 Bad Request: InvalidInput: Environment name is invalid (target: name)
func (e *ResponseError) Error() string {
	var b strings.Builder
	b.WriteString("bcadmin: ")
	if e.Method != "" || e.Path != "" {
		fmt.Fprintf(&b, "%s: ", strings.TrimSpace(e.Method+" "+e.Path))
	}
	if e.Status != "" {
		b.WriteString(e.Status)
	} else {
		fmt.Fprintf(&b, "%d %s", e.StatusCode, http.StatusText(e.StatusCode))
	}
	switch {
	case e.Code != "" || e.Message != "":
		b.WriteString(": ")
		switch {
		case e.Code != "" && e.Message != "":
			fmt.Fprintf(&b, "%s: %s", e.Code, e.Message)
		case e.Code != "":
			b.WriteString(e.Code)
		default:
			b.WriteString(e.Message)
		}
		if e.Target != "" {
			fmt.Fprintf(&b, " (target: %s)", e.Target)
		}
		writeDetails(&b, "client errors", e.ClientErrors)
		writeDetails(&b, "details", e.Details)
		if e.Data != nil && len(e.Data.Requirements) > 0 {
			b.WriteString("; requirements: ")
			for i, r := range e.Data.Requirements {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%s %s (%s)", r.Name, r.Version, r.AppID)
			}
		}
	case len(bytes.TrimSpace(e.RawBody)) > 0:
		raw := string(bytes.TrimSpace(e.RawBody))
		if len(raw) > maxErrorBodyInMessage {
			raw = raw[:maxErrorBodyInMessage] + "... (truncated, see RawBody)"
		}
		b.WriteString(": ")
		b.WriteString(raw)
	}
	if e.RequestID != "" {
		fmt.Fprintf(&b, " [request-id: %s]", e.RequestID)
	}
	return b.String()
}

func writeDetails(b *strings.Builder, label string, details []ErrorDetail) {
	if len(details) == 0 {
		return
	}
	fmt.Fprintf(b, "; %s: ", label)
	for i, d := range details {
		if i > 0 {
			b.WriteString(", ")
		}
		switch {
		case d.Code != "" && d.Message != "":
			fmt.Fprintf(b, "%s: %s", d.Code, d.Message)
		case d.Code != "":
			b.WriteString(d.Code)
		default:
			b.WriteString(d.Message)
		}
		if d.Target != "" {
			fmt.Fprintf(b, " (target: %s)", d.Target)
		}
	}
}

// Is reports whether the error matches one of the sentinels [ErrNotFound],
// [ErrConflict] or [ErrThrottled].
func (e *ResponseError) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.StatusCode == http.StatusNotFound || strings.HasSuffix(strings.ToLower(e.Code), "notfound")
	case ErrConflict:
		return e.StatusCode == http.StatusConflict
	case ErrThrottled:
		return e.StatusCode == http.StatusTooManyRequests
	}
	return false
}

// ErrorCode returns the API error code of err if it is or wraps a
// [*ResponseError], and "" otherwise.
func ErrorCode(err error) string {
	if re, ok := errors.AsType[*ResponseError](err); ok {
		return re.Code
	}
	return ""
}

// IsNotFound reports whether err matches [ErrNotFound].
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// IsConflict reports whether err matches [ErrConflict].
func IsConflict(err error) bool { return errors.Is(err, ErrConflict) }

// IsThrottled reports whether err matches [ErrThrottled].
func IsThrottled(err error) bool { return errors.Is(err, ErrThrottled) }

// OperationError is returned when a long-running operation ends with status
// Failed or Canceled. Operation holds the operation as last returned by the
// API, including its ErrorMessage.
type OperationError struct {
	Operation *Operation
}

// Error returns the operation's ID, type, status and error message.
func (e *OperationError) Error() string {
	op := e.Operation
	if op == nil {
		return "bcadmin: operation failed"
	}
	msg := fmt.Sprintf("bcadmin: operation %s", op.ID)
	if op.Type != "" {
		msg += " (" + string(op.Type) + ")"
	}
	msg += " ended with status " + string(op.Status)
	if op.ErrorMessage != "" {
		msg += ": " + op.ErrorMessage
	}
	return msg
}

// Canceled reports whether the operation was canceled rather than failed.
func (e *OperationError) Canceled() bool {
	return e.Operation != nil && e.Operation.Status.Is(OperationStatusCanceled)
}
