package bcadmintest

import "testing"

func TestCompareVersions(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		x, y string
		want int // sign
	}{
		{"1.0.0.0", "1.0.0.0", 0},
		{"1.0", "1.0.0.0", 0},
		{"1.10.0.0", "1.9.0.0", 1},
		{"2.0.0.0", "10.0.0.0", -1},
		{"1.0.0.0-beta", "1.0.0.0-alpha", 1},
		{"", "0.0", 0},
	} {
		got := compareVersions(tt.x, tt.y)
		if (got > 0) != (tt.want > 0) || (got < 0) != (tt.want < 0) {
			t.Errorf("compareVersions(%q, %q) = %d, want sign %d", tt.x, tt.y, got, tt.want)
		}
	}
}
