package bcadmin

import (
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestErrorTruncatesAtRuneBoundary(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("a", maxErrorBodyInMessage-1) + "ä" + strings.Repeat("b", 10)
	e := newResponseError(&http.Response{StatusCode: http.StatusBadGateway, Status: "502 Bad Gateway", Header: http.Header{}}, []byte(body))
	if !utf8.ValidString(e.Error()) || !strings.Contains(e.Error(), "(truncated, see RawBody)") {
		t.Errorf("Error() is not valid UTF-8 or not truncated")
	}
}

func FuzzResponseErrorDecode(f *testing.F) {
	for _, s := range []string{
		`{"code":"x","message":"y"}`,
		`{"error":{"code":"x","message":"y","innererror":{}}}`,
		`{"code":1,"clientError":[{"code":"a"}],"data":{"requirements":[{"appId":"1"}]}}`,
		`<html></html>`,
		``,
		`{`,
		`[]`,
		`{"error":{"code":"X","details":{"x":1},"innererror":"text"}}`,
		`{"error":"flat string"}`,
		`{"code":"a\u001b[2Jb","message":null,"target":["t"]}`,
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
		if e.Message != "" && !strings.Contains(msg, clean(e.Message)) {
			t.Fatalf("Error() = %q does not contain message %q", msg, e.Message)
		}
		if !utf8.ValidString(body) {
			return
		}
		for _, r := range msg {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("Error() = %q contains control character %q", msg, r)
			}
		}
	})
}
