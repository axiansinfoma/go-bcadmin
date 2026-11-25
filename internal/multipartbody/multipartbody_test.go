package multipartbody

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
	for _, name := range []string{"", "a\r\nX-Injected: 1.app", `a"b.app`, `a\b.app`, "My\u00a0App.app", "a\u200bb.app", "a\u202eb.app", "a\xffb.app"} {
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

func (s *shrinking) ReadAt(p []byte, off int64) (int, error) {
	if s.n > 0 {
		if off >= s.n {
			return 0, io.EOF
		}
		if rem := s.n - off; int64(len(p)) > rem {
			n, _ := s.Reader.ReadAt(p[:rem], off)
			return n, io.EOF
		}
	}
	return s.Reader.ReadAt(p, off)
}

func TestBodyFileNameVerbatim(t *testing.T) {
	t.Parallel()
	b, err := New(nil, "extensionFile", "Café Müller_1.0.0.0.app", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(b)
	if err != nil {
		t.Fatal(err)
	}
	want := `Content-Disposition: form-data; name="extensionFile"; filename="Café Müller_1.0.0.0.app"`
	if !bytes.Contains(raw, []byte(want)) {
		t.Errorf("body does not contain %q:\n%s", want, raw)
	}
}

// TestBodyReaders checks that readers from NewReader are independent of each
// other, of Body's own offset and of the file position, for files with and
// without ReadAt. Run with -race.
func TestBodyReaders(t *testing.T) {
	t.Parallel()
	content := bytes.Repeat([]byte("0123456789"), 100000)
	for name, file := range map[string]io.ReadSeeker{
		"ReaderAt":       bytes.NewReader(content),
		"ReadSeekerOnly": struct{ io.ReadSeeker }{bytes.NewReader(content)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			b, err := New(nil, "f", "x.app", file)
			if err != nil {
				t.Fatal(err)
			}
			want, err := io.ReadAll(b.NewReader())
			if err != nil || int64(len(want)) != b.Size() {
				t.Fatalf("ReadAll = %d bytes, %v; Size() = %d", len(want), err, b.Size())
			}
			var wg sync.WaitGroup
			for range 4 {
				wg.Go(func() {
					r := b.NewReader()
					var got []byte
					buf := make([]byte, 777)
					for {
						n, err := r.Read(buf)
						got = append(got, buf[:n]...)
						if err == io.EOF {
							break
						}
						if err != nil {
							t.Error(err)
							return
						}
					}
					if !bytes.Equal(got, want) {
						t.Errorf("reader got %d bytes, different from the first read", len(got))
					}
				})
			}
			wg.Wait()
		})
	}
}

// TestBodyClose checks that Close waits for a read in progress and that
// reads after Close fail without touching the file.
func TestBodyClose(t *testing.T) {
	t.Parallel()
	f := &blockingFile{Reader: bytes.NewReader([]byte("abcdef")), entered: make(chan struct{}), release: make(chan struct{})}
	b, err := New(nil, "f", "x.app", f)
	if err != nil {
		t.Fatal(err)
	}
	r := b.NewReader()
	// Read the multipart head, then block inside the file read.
	head := make([]byte, b.sizes[0])
	if _, err := io.ReadFull(r, head); err != nil {
		t.Fatal(err)
	}
	readDone := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(r)
		readDone <- err
	}()
	<-f.entered
	closed := make(chan struct{})
	go func() {
		_ = b.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned while a file read was in progress")
	case <-time.After(50 * time.Millisecond):
	}
	close(f.release)
	<-closed
	if err := <-readDone; !errors.Is(err, ErrClosed) {
		t.Errorf("read after Close: %v, want ErrClosed", err)
	}
	before := f.calls.Load()
	if _, err := b.NewReader().Read(make([]byte, 10)); !errors.Is(err, ErrClosed) {
		t.Errorf("new reader after Close: %v, want ErrClosed", err)
	}
	if f.calls.Load() != before {
		t.Error("the file was read after Close")
	}
}

// blockingFile blocks its first ReadAt until release is closed.
type blockingFile struct {
	*bytes.Reader
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (f *blockingFile) ReadAt(p []byte, off int64) (int, error) {
	if f.calls.Add(1) == 1 {
		close(f.entered)
		<-f.release
	}
	return f.Reader.ReadAt(p, off)
}
