// Package multipartbody builds a multipart/form-data request body that streams
// a file without reading it into memory, and that can seek, so it can be sent
// with a known Content-Length and resent on retry.
//
// The file is read with ReadAt at absolute offsets, never through its own
// position, and [Body.NewReader] returns independent readers. A transport that
// is still writing an abandoned attempt therefore does not disturb the next
// attempt, as long as every attempt uses its own reader.
package multipartbody

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/textproto"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Field is a form field sent before the file.
type Field struct {
	Name  string
	Value string
}

// ErrClosed is returned by reads after [Body.Close].
var ErrClosed = errors.New("multipartbody: body closed")

// Body is a seekable multipart body. Body itself has one offset and is not
// safe for concurrent use; readers from [Body.NewReader] are independent of
// it and of each other. [Body.Close] ends all reads of the file.
type Body struct {
	// mu is held for reading during every read of a part, and for writing
	// by Close.
	mu       sync.RWMutex
	closed   bool
	fileSize int64
	parts    []io.ReaderAt
	sizes    []int64
	size     int64
	// offset is the position of Read on Body itself.
	offset int64
	// ContentType is the Content-Type header value, including the boundary.
	ContentType string
}

// New returns a body with the fields, followed by file as the form file
// fileField with the given file name. The file is read from offset 0 to the
// end it has when New is called. If file implements io.ReaderAt (as *os.File
// and *bytes.Reader do), its position is not used after New; otherwise reads
// seek and read under a lock.
func New(fields []Field, fileField, fileName string, file io.ReadSeeker) (*Body, error) {
	if err := validHeaderValue(fileField); err != nil {
		return nil, fmt.Errorf("file field name: %w", err)
	}
	if err := validHeaderValue(fileName); err != nil {
		return nil, fmt.Errorf("file name: %w", err)
	}
	fileSize, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("determining file size: %w", err)
	}
	if fileSize < 0 {
		return nil, fmt.Errorf("file size %d is negative", fileSize)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewinding file: %w", err)
	}
	fileAt, ok := file.(io.ReaderAt)
	if !ok {
		fileAt = &lockedReaderAt{r: file}
	}

	var head bytes.Buffer
	w := multipart.NewWriter(&head)
	for _, f := range fields {
		if err := w.WriteField(f.Name, f.Value); err != nil {
			return nil, err
		}
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="`+fileField+`"; filename="`+fileName+`"`)
	h.Set("Content-Type", "application/octet-stream")
	if _, err := w.CreatePart(h); err != nil {
		return nil, err
	}
	tail := "\r\n--" + w.Boundary() + "--\r\n"

	b := &Body{ContentType: w.FormDataContentType(), fileSize: fileSize}
	b.add(bytes.NewReader(head.Bytes()), int64(head.Len()))
	b.add(fileAt, fileSize)
	b.add(strings.NewReader(tail), int64(len(tail)))
	return b, nil
}

// validHeaderValue rejects values that cannot be placed verbatim in a quoted
// MIME header parameter: empty values, invalid UTF-8, characters that are
// not printable (controls, format characters, separators other than the
// space) and quotes or backslashes.
func validHeaderValue(s string) error {
	if s == "" {
		return errors.New("empty")
	}
	if !utf8.ValidString(s) {
		return fmt.Errorf("%q is not valid UTF-8", s)
	}
	for _, r := range s {
		if !unicode.IsPrint(r) || r == '"' || r == '\\' {
			return fmt.Errorf("%q contains %q", s, r)
		}
	}
	return nil
}

func (b *Body) add(r io.ReaderAt, n int64) {
	b.parts = append(b.parts, r)
	b.sizes = append(b.sizes, n)
	b.size += n
}

// Size returns the total length of the body in bytes.
func (b *Body) Size() int64 { return b.size }

// readAt reads into p from the body offset off. It returns io.EOF at the end
// of the body and io.ErrUnexpectedEOF when the file is shorter than when the
// body was built.
func (b *Body) readAt(p []byte, off int64) (int, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return 0, ErrClosed
	}
	if off >= b.size {
		return 0, io.EOF
	}
	start := int64(0)
	for i, part := range b.parts {
		end := start + b.sizes[i]
		if off < end {
			if rem := end - off; int64(len(p)) > rem {
				p = p[:rem]
			}
			n, err := part.ReadAt(p, off-start)
			if errors.Is(err, io.EOF) {
				if n < len(p) {
					return n, io.ErrUnexpectedEOF
				}
				err = nil
			}
			return n, err
		}
		start = end
	}
	return 0, io.EOF
}

// Read reads from the current offset of b.
func (b *Body) Read(p []byte) (int, error) {
	n, err := b.readAt(p, b.offset)
	b.offset += int64(n)
	return n, err
}

// Seek sets the offset for the next Read.
func (b *Body) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = b.offset + offset
	case io.SeekEnd:
		abs = b.size + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("negative position")
	}
	b.offset = abs
	return abs, nil
}

// Close makes every later read of b and of its readers fail with ErrClosed,
// and waits until reads in progress have returned. After Close, the body
// does not touch the file any more. Close does not close the file; the
// caller owns it. Close can be called more than once.
func (b *Body) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	return nil
}

// FileSize returns the number of file bytes the body sends.
func (b *Body) FileSize() int64 { return b.fileSize }

// NewReader returns a reader of the whole body with its own offset. Readers
// can be used concurrently with each other and with b. Their Close does
// nothing; use [Body.Close] to end all reads.
func (b *Body) NewReader() io.ReadCloser { return &reader{b: b} }

type reader struct {
	b   *Body
	off int64
}

// Read reads from the reader's own offset.
func (r *reader) Read(p []byte) (int, error) {
	n, err := r.b.readAt(p, r.off)
	r.off += int64(n)
	return n, err
}

// Close does nothing; see [Body.Close].
func (r *reader) Close() error { return nil }

// lockedReaderAt implements io.ReaderAt for a file without ReadAt by seeking
// and reading under a lock.
type lockedReaderAt struct {
	mu sync.Mutex
	r  io.ReadSeeker
}

// ReadAt seeks to off and reads len(p) bytes, holding the lock.
func (l *lockedReaderAt) ReadAt(p []byte, off int64) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.r.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}
	return io.ReadFull(l.r, p)
}
