package archive

import (
	"github.com/gbdubs/pharos/internal/teams"
	"testing"
)

func TestTeamsCapabilityVersionComparison(t *testing.T) {
	for _, tc := range []struct {
		actual, required string
		want             bool
	}{
		{"1.0.0", "1.0.0", true}, {"1.2.0", "1.1.99", true},
		{"1.0.9", "1.1.0", false}, {"01.0.0", "1.0.0", false},
		{"dev", "1.0.0", false}, {"1.0.0", "2.0.0", false},
		{"999999999999999999999999.0.0", "1.0.0", false},
	} {
		if got := teamsVersionAtLeast(tc.actual, tc.required); got != tc.want {
			t.Errorf("%s >= %s = %v, want %v", tc.actual, tc.required, got, tc.want)
		}
	}
}

func TestTeamsSendFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		err           error
		state, reason string
	}{
		{&teams.HTTPError{Status: 409, Code: "stale_ownership"}, "rejected", "stale_state"},
		{&teams.HTTPError{Status: 422, Code: "coverage_incomplete"}, "rejected", "invalid_result"},
		{&teams.HTTPError{Status: 404, Code: "not_found"}, "unconfirmed", "authorization"},
		{teams.ErrReceiptMismatch, "unconfirmed", "receipt_mismatch"},
		{&teams.HTTPError{Status: 503, Code: "unavailable"}, "needs_reconcile", "transport"},
	} {
		state, reason := teamsSubmitFailure(tc.err)
		if state != tc.state || reason != tc.reason {
			t.Errorf("%v: got %s/%s", tc.err, state, reason)
		}
	}
}
