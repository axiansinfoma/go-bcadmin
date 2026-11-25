package multipartbody

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"testing"
)

func TestBody(t *testing.T) {
	t.Parallel()
	content := bytes.Repeat([]byte("0123456789"), 10000)
	file := bytes.NewReader(content)
	b, err := New([]Field{{"syncMode", "Add"}, {"acceptIsvEula", "true"}}, "extensionFile", "My App_1.0.0.0.app", file)
	if err != nil {
		t.Fatal(err)
	}
	// Read twice to check that seeking back resends the same bytes.
	for range 2 {
		if _, err := b.Seek(0, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(io.LimitReader(b, b.Size()+10))
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(raw)) != b.Size() {
			t.Fatalf("read %d bytes, Size() = %d", len(raw), b.Size())
		}
		_, params, err := mime.ParseMediaType(b.ContentType)
		if err != nil {
			t.Fatal(err)
		}
		r := multipart.NewReader(bytes.NewReader(raw), params["boundary"])
		got := map[string]string{}
		for {
			p, err := r.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(p)
			if p.FormName() == "extensionFile" {
				if p.FileName() != "My App_1.0.0.0.app" || !bytes.Equal(data, content) {
					t.Errorf("file part: name %q, %d bytes", p.FileName(), len(data))
				}
				continue
			}
			got[p.FormName()] = string(data)
		}
		if got["syncMode"] != "Add" || got["acceptIsvEula"] != "true" {
			t.Errorf("fields = %v", got)
		}
	}
	if n, _ := b.Seek(0, io.SeekEnd); n != b.Size() {
		t.Errorf("Seek(0, End) = %d, want %d", n, b.Size())
	}
}

func TestBodyRejectsBadFileName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "a\r\nX-Injected: 1.app", `a"b.app`, `a\b.app`} {
		if _, err := New(nil, "extensionFile", name, strings.NewReader("x")); err == nil {
			t.Errorf("New with file name %q did not fail", name)
		}
	}
}

func TestBodyShortFile(t *testing.T) {
	t.Parallel()
	f := &shrinking{Reader: bytes.NewReader([]byte("abcdef"))}
	b, err := New(nil, "f", "x.app", f)
	if err != nil {
		t.Fatal(err)
	}
	f.n = 3 // the file shrinks after the body was built
	if _, err := io.ReadAll(b); err == nil {
		t.Error("reading a body whose file shrank did not fail")
	}
}

// shrinking returns EOF after n bytes once n is set.
type shrinking struct {
	*bytes.Reader
	n int64
}

func (s *shrinking) Read(p []byte) (int, error) {
	if s.n > 0 {
		pos, _ := s.Seek(0, io.SeekCurrent)
		if pos >= s.n {
			return 0, io.EOF
		}
		if rem := s.n - pos; int64(len(p)) > rem {
			p = p[:rem]
		}
	}
	return s.Reader.Read(p)
}
