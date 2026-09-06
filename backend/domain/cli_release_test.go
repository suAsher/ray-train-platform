package domain

import "testing"

func TestCompareCLIReleaseVersions(t *testing.T) {
	for _, tt := range []struct {
		a, b    string
		want    int
		invalid bool
	}{
		{"release-20260906-02", "release-20260906-10", -1, false},
		{"release-20260907-01", "release-20260906-99", 1, false},
		{"release-20260906-01", "release-20260906-01", 0, false},
		{"dev", "release-20260906-01", 0, true},
		{"release-20260906-2", "release-20260906-10", -1, false},
		{"release-20260906-00", "release-20260906-01", 0, true},
		{"release-20260230-01", "release-20260906-01", 0, true},
	} {
		got, err := CompareCLIReleaseVersions(tt.a, tt.b)
		if (err != nil) != tt.invalid || got != tt.want {
			t.Errorf("%+v: %d %v", tt, got, err)
		}
	}
}
