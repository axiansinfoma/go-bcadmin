package bcadmin

import (
	"net/http"
	"strings"
	"testing"
)

func FuzzResponseErrorDecode(f *testing.F) {
	for _, s := range []string{
		`{"code":"x","message":"y"}`,
		`{"error":{"code":"x","message":"y","innererror":{}}}`,
		`{"code":1,"clientError":[{"code":"a"}],"data":{"requirements":[{"appId":"1"}]}}`,
		`<html></html>`,
		``,
		`{`,
		`[]`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body string) {
		resp := &http.Response{StatusCode: http.StatusBadRequest, Status: "400 Bad Request", Header: http.Header{}}
		e := newResponseError(resp, []byte(body))
		msg := e.Error()
		if !strings.HasPrefix(msg, "bcadmin: 400 Bad Request") {
			t.Fatalf("Error() = %q", msg)
		}
		if e.Message != "" && !strings.Contains(msg, e.Message) {
			t.Fatalf("Error() = %q does not contain message %q", msg, e.Message)
		}
	})
}
