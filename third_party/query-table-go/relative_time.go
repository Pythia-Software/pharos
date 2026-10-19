package querytable

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// MaxRelativeTimeMilliseconds matches the portable datetime range used by
// local executors. Multiplication and summation are checked before arithmetic.
const MaxRelativeTimeMilliseconds int64 = 8_640_000_000_000_000

var relativeDurationPattern = regexp.MustCompile(`^[+-](?:[0-9]+(?:ms|s|m|h|d|w))+$`)
var durationComponentPattern = regexp.MustCompile(`([0-9]+)(ms|s|m|h|d|w)`)

func parseRelativeDuration(value string) (int64, error) {
	if !relativeDurationPattern.MatchString(value) {
		return 0, fmt.Errorf("not a signed duration: %q", value)
	}
	var total int64
	for _, part := range durationComponentPattern.FindAllStringSubmatch(value[1:], -1) {
		unit := map[string]int64{"ms": 1, "s": 1000, "m": 60000, "h": 3600000, "d": 86400000, "w": 604800000}[part[2]]
		// ParseUint tolerates leading zeros but still rejects integer overflow.
		amount, err := strconv.ParseUint(part[1], 10, 64)
		if err != nil || amount > uint64((MaxRelativeTimeMilliseconds-total)/unit) {
			return 0, fmt.Errorf("signed duration exceeds datetime limits: %q", value)
		}
		total += int64(amount) * unit
	}
	if value[0] == '-' {
		total = -total
	}
	return total, nil
}

func relativeTimestamp(now time.Time, offset int64) (time.Time, error) {
	ms := now.UnixMilli()
	if ms < -MaxRelativeTimeMilliseconds || ms > MaxRelativeTimeMilliseconds || ms+offset < -MaxRelativeTimeMilliseconds || ms+offset > MaxRelativeTimeMilliseconds {
		return time.Time{}, fmt.Errorf("relative time exceeds datetime limits")
	}
	// Avoid time.Duration's ~292-year nanosecond overflow, and retain the
	// captured server clock's precision (including for +0s).
	return time.Unix(now.Unix()+offset/1000, int64(now.Nanosecond())+(offset%1000)*1_000_000).UTC(), nil
}

// ResolveRelativeDatetime resolves a signed duration using one host clock.
// Absolute operands are returned unchanged. Hosts with map-backed legacy paths
// can share the compiler's duration grammar and bounds without duplicating it.
func ResolveRelativeDatetime(value string, now time.Time) (string, error) {
	if len(value) == 0 || value[0] != '+' && value[0] != '-' {
		return value, nil
	}
	offset, err := parseRelativeDuration(value)
	if err != nil {
		return "", err
	}
	instant, err := relativeTimestamp(now, offset)
	if err != nil {
		return "", err
	}
	return instant.Format(time.RFC3339Nano), nil
}
