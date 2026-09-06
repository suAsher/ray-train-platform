package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

var cliReleasePattern = regexp.MustCompile(`^release-([0-9]{8})-([0-9]{1,9})$`)

// CompareCLIReleaseVersions compares release dates and numeric build counters.
// Development and unknown version formats are not comparable.
func CompareCLIReleaseVersions(left, right string) (int, error) {
	parse := func(value string) (string, uint64, error) {
		parts := cliReleasePattern.FindStringSubmatch(value)
		if parts == nil {
			return "", 0, fmt.Errorf("unknown CLI release version")
		}
		if _, err := time.Parse("20060102", parts[1]); err != nil {
			return "", 0, fmt.Errorf("invalid CLI release date")
		}
		counter, err := strconv.ParseUint(parts[2], 10, 64)
		if counter == 0 {
			return "", 0, fmt.Errorf("invalid CLI release counter")
		}
		return parts[1], counter, err
	}
	ld, ln, err := parse(left)
	if err != nil {
		return 0, err
	}
	rd, rn, err := parse(right)
	if err != nil {
		return 0, err
	}
	if ld < rd || ld == rd && ln < rn {
		return -1, nil
	}
	if ld > rd || ld == rd && ln > rn {
		return 1, nil
	}
	return 0, nil
}
