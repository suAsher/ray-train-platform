package k8s

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	defaultLocalCacheTotal = "200Gi"
	maximumLocalCacheTotal = "5Ti"
	bytesPerGiB            = int64(1024 * 1024 * 1024)
)

// splitLocalCacheCapacity converts the user-visible total cache request into
// the equal request placed on each independent NVMe PVC. The platform accepts
// only even whole-GiB totals so it never rounds a storage request silently.
func splitLocalCacheCapacity(total string) (string, error) {
	value := strings.TrimSpace(total)
	if value == "" {
		value = defaultLocalCacheTotal
	}
	quantity, err := positiveCacheQuantity(value)
	if err != nil {
		return "", fmt.Errorf("cache total %q must be a positive Kubernetes storage quantity", total)
	}
	bytes := quantity.Value()
	if bytes%bytesPerGiB != 0 {
		return "", fmt.Errorf("cache total %q must be a whole-GiB quantity", value)
	}
	wholeGiB := bytes / bytesPerGiB
	if wholeGiB%2 != 0 {
		return "", fmt.Errorf("cache total %q must be an even whole-GiB quantity", value)
	}
	maximum := resource.MustParse(maximumLocalCacheTotal)
	if quantity.Cmp(maximum) > 0 {
		return "", fmt.Errorf("cache total %q exceeds maximum %s", value, maximumLocalCacheTotal)
	}
	perDisk := resource.NewQuantity(bytes/2, resource.BinarySI)
	return perDisk.String(), nil
}
