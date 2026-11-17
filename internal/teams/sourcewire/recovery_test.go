package sourcewire

import (
	"errors"
	"testing"
	"time"
)

func TestRecoveryIssuanceBoundaries(t *testing.T) {
	issued := time.Unix(1794931200, 0).UTC()
	for _, tc := range []struct {
		name string
		now  time.Time
		want error
	}{
		{"earliest accepted future", issued.Add(-RecoveryFutureSkew), nil},
		{"too far future", issued.Add(-RecoveryFutureSkew - time.Nanosecond), ErrInvalid},
		{"last instant", issued.Add(RecoveryHorizon - time.Nanosecond), nil},
		{"exact expiry", issued.Add(RecoveryHorizon), ErrIssuanceExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expires, err := CheckRecoveryIssuance(issued.Unix(), tc.now)
			if !errors.Is(err, tc.want) || (tc.want != ErrInvalid && !expires.Equal(issued.Add(RecoveryHorizon))) {
				t.Fatalf("expiry %v: %v", expires, err)
			}
		})
	}
}
