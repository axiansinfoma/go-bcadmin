package bcadmin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
)

// Sentinel errors for use with [errors.Is]. A [*ResponseError] matches them
// based on its HTTP status and error code.
var (
	// ErrNotFound matches responses with HTTP status 404 and responses with
	// the error code "EnvironmentNotFound", which the API also returns with
	// other status codes. Use [ErrorCode] to check for other error codes.
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

// decodeBody fills the error fields from RawBody. It accepts the flat and the
// wrapped ("error") shape and decodes each field on its own, so a field with an
// unexpected type does not prevent the others from being decoded.
func (e *ResponseError) decodeBody() {
	fields := jsonObject(e.RawBody)
	if fields == nil {
		return
	}
	if inner := jsonObject(fields["error"]); len(inner) > 0 {
		fields = inner
	}
	e.Code = jsonString(fields["code"])
	e.Message = jsonString(fields["message"])
	e.Target = jsonString(fields["target"])
	e.ClientErrors = jsonDetails(fields["clienterror"])
	e.Details = jsonDetails(fields["details"])
	e.ExtensionData = jsonMap(fields["extensiondata"])
	e.InnerError = jsonMap(fields["innererror"])
	if raw, ok := fields["data"]; ok {
		var d ErrorData
		if json.Unmarshal(raw, &d) == nil && len(d.Requirements) > 0 {
			e.Data = &d
		}
	}
}

// jsonObject decodes raw as a JSON object and returns its members with
// lower-cased keys, or nil if raw is not an object.
func jsonObject(raw []byte) map[string]json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		out[strings.ToLower(k)] = v
	}
	return out
}

// jsonString returns a JSON string as is and any other JSON value in its JSON
// form, so no value the API sent is lost.
func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return stringify(v)
}

// jsonDetails decodes a list of error details, or a single detail object.
func jsonDetails(raw json.RawMessage) []ErrorDetail {
	if len(raw) == 0 {
		return nil
	}
	var list []ErrorDetail
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var one ErrorDetail
	if json.Unmarshal(raw, &one) == nil && (one.Code != "" || one.Message != "") {
		return []ErrorDetail{one}
	}
	return nil
}

// jsonMap decodes an object; any other non-null value is returned as
// {"value": value}.
func jsonMap(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil || v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{"value": v}
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
		fmt.Fprintf(&b, "%s: ", clean(strings.TrimSpace(e.Method+" "+e.Path)))
	}
	if e.Status != "" {
		b.WriteString(clean(e.Status))
	} else {
		fmt.Fprintf(&b, "%d %s", e.StatusCode, http.StatusText(e.StatusCode))
	}
	switch {
	case e.Code != "" || e.Message != "":
		b.WriteString(": ")
		switch {
		case e.Code != "" && e.Message != "":
			fmt.Fprintf(&b, "%s: %s", clean(e.Code), clean(e.Message))
		case e.Code != "":
			b.WriteString(clean(e.Code))
		default:
			b.WriteString(clean(e.Message))
		}
		if e.Target != "" {
			fmt.Fprintf(&b, " (target: %s)", clean(e.Target))
		}
		writeDetails(&b, "client errors", e.ClientErrors)
		writeDetails(&b, "details", e.Details)
		if e.Data != nil && len(e.Data.Requirements) > 0 {
			b.WriteString("; requirements: ")
			for i, r := range e.Data.Requirements {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%s %s (%s)", clean(r.Name), clean(r.Version), clean(r.AppID))
			}
		}
	case len(bytes.TrimSpace(e.RawBody)) > 0:
		raw := clean(string(bytes.TrimSpace(e.RawBody)))
		if len(raw) > maxErrorBodyInMessage {
			raw = raw[:maxErrorBodyInMessage] + "... (truncated, see RawBody)"
		}
		b.WriteString(": ")
		b.WriteString(raw)
	}
	if e.RequestID != "" {
		fmt.Fprintf(&b, " [request-id: %s]", clean(e.RequestID))
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
			fmt.Fprintf(b, "%s: %s", clean(d.Code), clean(d.Message))
		case d.Code != "":
			b.WriteString(clean(d.Code))
		default:
			b.WriteString(clean(d.Message))
		}
		if d.Target != "" {
			fmt.Fprintf(b, " (target: %s)", clean(d.Target))
		}
	}
}

// clean replaces control characters (including newlines and terminal escape
// sequences) with spaces, so server-provided text cannot garble log output.
// The fields of ResponseError keep the original text.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// Is reports whether the error matches one of the sentinels [ErrNotFound],
// [ErrConflict] or [ErrThrottled].
func (e *ResponseError) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.StatusCode == http.StatusNotFound || strings.EqualFold(e.Code, "EnvironmentNotFound")
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
