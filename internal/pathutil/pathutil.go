// Package pathutil builds URL paths from a template and caller-supplied
// segments, escaping each segment and rejecting values that would change the
// structure of the path.
package pathutil

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// ErrInvalidSegment is returned when a path segment is empty, is "." or "..",
// or contains a slash, backslash or control character.
var ErrInvalidSegment = errors.New("invalid path segment")

// Placeholder marks the position of a segment in a template.
const Placeholder = "{}"

// Build replaces each Placeholder in template with the next value of segments,
// validated with [ValidateSegment] and escaped with [url.PathEscape].
// It returns an error when a segment is invalid or the number of segments does
// not match the number of placeholders.
func Build(template string, segments ...string) (string, error) {
	if n := strings.Count(template, Placeholder); n != len(segments) {
		return "", fmt.Errorf("path template %q expects %d segments, got %d", template, n, len(segments))
	}
	var b strings.Builder
	rest := template
	for _, seg := range segments {
		if err := ValidateSegment(seg); err != nil {
			return "", err
		}
		i := strings.Index(rest, Placeholder)
		b.WriteString(rest[:i])
		b.WriteString(url.PathEscape(seg))
		rest = rest[i+len(Placeholder):]
	}
	b.WriteString(rest)
	return b.String(), nil
}

// ValidateSegment reports whether s can be used as a single path segment.
func ValidateSegment(s string) error {
	if s == "" {
		return fmt.Errorf("%w: empty", ErrInvalidSegment)
	}
	if s == "." || s == ".." {
		return fmt.Errorf("%w: %q", ErrInvalidSegment, s)
	}
	for _, r := range s {
		if r == '/' || r == '\\' || unicode.IsControl(r) {
			return fmt.Errorf("%w: %q contains %q", ErrInvalidSegment, s, r)
		}
	}
	return nil
}

// ValidateRelative reports whether p is a relative path below the API root:
// not empty, not absolute, without a scheme or host, and without "." or ".."
// segments. A query string is allowed.
func ValidateRelative(p string) error {
	if p == "" {
		return fmt.Errorf("%w: empty path", ErrInvalidSegment)
	}
	u, err := url.Parse(p)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSegment, err)
	}
	if u.Scheme != "" || u.Host != "" || u.User != nil || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") {
		return fmt.Errorf("%w: %q must be relative to the API root", ErrInvalidSegment, p)
	}
	for seg := range strings.SplitSeq(u.Path, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("%w: %q contains %q", ErrInvalidSegment, p, seg)
		}
		if strings.ContainsRune(seg, '\\') {
			return fmt.Errorf("%w: %q contains a backslash", ErrInvalidSegment, p)
		}
	}
	return nil
}
