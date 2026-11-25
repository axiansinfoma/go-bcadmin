package pathutil

import (
	"errors"
	"testing"
)

func TestBuild(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		template string
		segments []string
		want     string
		wantErr  bool
	}{
		{name: "no segments", template: "environments/quotas", want: "environments/quotas"},
		{name: "two segments", template: "applications/{}/environments/{}", segments: []string{"BusinessCentral", "my sandbox"}, want: "applications/BusinessCentral/environments/my%20sandbox"},
		{name: "query chars escaped", template: "a/{}", segments: []string{"x?y#z"}, want: "a/x%3Fy%23z"},
		{name: "percent escaped", template: "a/{}", segments: []string{"50%"}, want: "a/50%25"},
		{name: "slash rejected", template: "a/{}", segments: []string{"x/y"}, wantErr: true},
		{name: "backslash rejected", template: "a/{}", segments: []string{`x\y`}, wantErr: true},
		{name: "dotdot rejected", template: "a/{}", segments: []string{".."}, wantErr: true},
		{name: "dot rejected", template: "a/{}", segments: []string{"."}, wantErr: true},
		{name: "empty rejected", template: "a/{}", segments: []string{""}, wantErr: true},
		{name: "control rejected", template: "a/{}", segments: []string{"a\nb"}, wantErr: true},
		{name: "too few segments", template: "a/{}/{}", segments: []string{"x"}, wantErr: true},
		{name: "too many segments", template: "a", segments: []string{"x"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Build(tt.template, tt.segments...)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Build() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Build() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateRelative(t *testing.T) {
	t.Parallel()
	valid := []string{"environments/quotas", "applications/BusinessCentral/environments?x=1", "a/b%2Fc"}
	invalid := []string{"", "/admin/v2.29/x", "https://example.com/x", "//example.com/x", "a/../b", "./a", `a\b`, `\a`}
	for _, p := range valid {
		if err := ValidateRelative(p); err != nil {
			t.Errorf("ValidateRelative(%q) = %v, want nil", p, err)
		}
	}
	for _, p := range invalid {
		if err := ValidateRelative(p); !errors.Is(err, ErrInvalidSegment) {
			t.Errorf("ValidateRelative(%q) = %v, want ErrInvalidSegment", p, err)
		}
	}
}

func FuzzBuild(f *testing.F) {
	for _, s := range []string{"BusinessCentral", "my sandbox", "..", "a/b", "x?y", "%2F"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, seg string) {
		got, err := Build("a/{}/b", seg)
		if err != nil {
			return
		}
		// A valid segment must stay exactly one segment.
		if err := ValidateRelative(got); err != nil {
			t.Fatalf("Build produced invalid relative path %q: %v", got, err)
		}
		if n := countSlashes(got); n != 2 {
			t.Fatalf("Build(%q) = %q has %d slashes, want 2", seg, got, n)
		}
	})
}

func countSlashes(s string) int {
	n := 0
	for _, r := range s {
		if r == '/' {
			n++
		}
	}
	return n
}
