package archive

import (
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
	"testing"
	"time"
)

func TestTeamsBaselineServerFloorStaysHeld(t *testing.T) {
	now := time.Unix(100, 0)
	clear := teamsRecoveryOriginScan{start: sourcewire.RecoveryStartReceipt{ExpiresAt: 101, UnacceptedFloor: 90}, last: sourcewire.RecoveryPageReceipt{ServerTime: 100, UnacceptedFloor: 90}, byToken: map[string]teamsRecoveryOrigin{}, byWitness: map[string]teamsRecoveryOrigin{}}
	if !teamsBaselineServerClear(&clear, now) {
		t.Fatal("empty current scan denied")
	}
	cases := []teamsRecoveryOriginScan{clear, clear, clear, clear, clear}
	cases[0].held = true
	cases[1].start.UnacceptedFloor = -1
	cases[2].last.UnacceptedFloor = 89
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

func TestTeamsBaselineReplacementPreservesServerFloors(t *testing.T) {
	old := &teamsRecoveryOriginScan{last: sourcewire.RecoveryPageReceipt{UnacceptedFloor: 20}, byToken: map[string]teamsRecoveryOrigin{"t": {Token: "t", Witness: "w", Revision: 3, EvidenceRevision: "e", Protection: "flagged", FlagFloor: "unknown"}}}
	next := &teamsRecoveryOriginScan{last: sourcewire.RecoveryPageReceipt{UnacceptedFloor: 21}, byToken: map[string]teamsRecoveryOrigin{"t": {Token: "t", Witness: "w", Revision: 4, EvidenceRevision: "e2", Protection: "flagged", FlagFloor: "unknown"}}}
	if !teamsBaselineScanDominates(next, old) {
		t.Fatal("monotone retained floor denied")
	}
	lower := *next
	lower.last.UnacceptedFloor = 19
	if teamsBaselineScanDominates(&lower, old) {
		t.Fatal("floor rollback accepted")
	}
	clear := *next
	clear.byToken = map[string]teamsRecoveryOrigin{"t": {Token: "t", Witness: "w", Revision: 4, EvidenceRevision: "e2", Protection: "non_private"}}
	if teamsBaselineScanDominates(&clear, old) {
		t.Fatal("prior flag lost")
	}
	missing := *next
	missing.byToken = map[string]teamsRecoveryOrigin{}
	if teamsBaselineScanDominates(&missing, old) {
		t.Fatal("missing prior origin accepted")
	}
}
