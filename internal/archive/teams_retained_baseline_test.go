package archive

import (
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
	"testing"
	"time"
)

func TestTeamsBaselineServerFloorStaysHeld(t *testing.T) {
	now := time.Unix(100, 0)
	clear := teamsRecoveryOriginScan{start: sourcewire.RecoveryStartReceipt{ExpiresAt: 101}, byToken: map[string]teamsRecoveryOrigin{}, byWitness: map[string]teamsRecoveryOrigin{}}
	if !teamsBaselineServerClear(&clear, now) {
		t.Fatal("empty current scan denied")
	}
	cases := []teamsRecoveryOriginScan{clear, clear, clear, clear, clear}
	cases[0].held = true
	cases[1].start.UnacceptedFloor = 1
	cases[2].last.UnacceptedFloor = 1
	cases[3].last.NextCursor = "remaining"
	cases[4].byToken = map[string]teamsRecoveryOrigin{"retained": {Protection: "flagged"}}
	for i := range cases {
		if teamsBaselineServerClear(&cases[i], now) {
			t.Fatalf("unsafe scan %d accepted", i)
		}
	}
	if teamsBaselineServerClear(&clear, time.Unix(101, 0)) {
		t.Fatal("expired scan accepted")
	}
}
