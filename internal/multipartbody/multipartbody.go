// Package multipartbody builds a multipart/form-data request body that streams
// a file from an io.ReadSeeker without reading it into memory, and that can
// seek, so it can be sent with a known Content-Length and resent on retry.
package multipartbody

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/textproto"
	"strings"
	"unicode"
)

// Field is a form field sent before the file.
type Field struct {
	Name  string
	Value string
}

// Body is a seekable multipart body. Close does not close the file.
type Body struct {
	parts  []io.ReadSeeker
	sizes  []int64
	size   int64
	offset int64
	// ContentType is the Content-Type header value, including the boundary.
	ContentType string
}

// New returns a body with the fields, followed by file as the form file
// fileField with the given file name. The file is read from its current start
// (offset 0) to its end.
func New(fields []Field, fileField, fileName string, file io.ReadSeeker) (*Body, error) {
	if err := validHeaderValue(fileName); err != nil {
		return nil, fmt.Errorf("file name: %w", err)
	}
	fileSize, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("determining file size: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewinding file: %w", err)
	}

	var head bytes.Buffer
	w := multipart.NewWriter(&head)
	for _, f := range fields {
		if err := w.WriteField(f.Name, f.Value); err != nil {
			return nil, err
		}
	}
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, fileField, fileName))
	h.Set("Content-Type", "application/octet-stream")
	if _, err := w.CreatePart(h); err != nil {
		return nil, err
	}
	tail := "\r\n--" + w.Boundary() + "--\r\n"

	b := &Body{ContentType: w.FormDataContentType()}
	b.add(bytes.NewReader(head.Bytes()), int64(head.Len()))
	b.add(file, fileSize)
	b.add(strings.NewReader(tail), int64(len(tail)))
	return b, nil
}

// validHeaderValue rejects values that cannot be placed in a quoted MIME
// header parameter without changing the header.
func validHeaderValue(s string) error {
	if s == "" {
		return errors.New("empty")
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == '"' || r == '\\' {
			return fmt.Errorf("%q contains %q", s, r)
		}
	}
	return nil
}

func (b *Body) add(r io.ReadSeeker, n int64) {
	b.parts = append(b.parts, r)
	b.sizes = append(b.sizes, n)
	b.size += n
}

// Size returns the total length of the body in bytes.
func (b *Body) Size() int64 { return b.size }

// Read reads from the current offset.
func (b *Body) Read(p []byte) (int, error) {
	if b.offset >= b.size {
		return 0, io.EOF
	}
	start := int64(0)
	for i, part := range b.parts {
		end := start + b.sizes[i]
		if b.offset < end {
			if _, err := part.Seek(b.offset-start, io.SeekStart); err != nil {
				return 0, err
			}
			max := end - b.offset
			if int64(len(p)) > max {
				p = p[:max]
			}
			n, err := part.Read(p)
			b.offset += int64(n)
			if errors.Is(err, io.EOF) {
				if n == 0 {
					return 0, io.ErrUnexpectedEOF // the file is shorter than when the body was built
				}
				err = nil
			}
			return n, err
		}
		start = end
	}
	return 0, io.EOF
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

// Close does nothing; the caller owns the file.
func (b *Body) Close() error { return nil }
