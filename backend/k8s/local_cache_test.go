package k8s

import (
	"strings"
	"testing"
)

func TestSplitLocalCacheCapacity(t *testing.T) {
	tests := []struct {
		name  string
		total string
		want  string
	}{
		{name: "empty uses default", total: "", want: "100Gi"},
		{name: "200Gi", total: "200Gi", want: "100Gi"},
		{name: "500Gi", total: "500Gi", want: "250Gi"},
		{name: "1Ti", total: "1Ti", want: "512Gi"},
		{name: "2Ti", total: "2Ti", want: "1Ti"},
		{name: "4Ti", total: "4Ti", want: "2Ti"},
		{name: "5Ti", total: "5Ti", want: "2560Gi"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := splitLocalCacheCapacity(test.total)
			if err != nil {
				t.Fatalf("split %q: %v", test.total, err)
			}
			if got != test.want {
				t.Fatalf("split %q: got %q, want %q", test.total, got, test.want)
			}
		})
	}
}

func TestSplitLocalCacheCapacityRejectsUnsafeValues(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{value: "garbage", want: "positive Kubernetes storage quantity"},
		{value: "0", want: "positive Kubernetes storage quantity"},
		{value: "201Gi", want: "even whole-GiB"},
		{value: "200500Mi", want: "whole-GiB"},
		{value: "6Ti", want: "5Ti"},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			if _, err := splitLocalCacheCapacity(test.value); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("split %q: got %v, want error containing %q", test.value, err, test.want)
			}
		})
	}
}
