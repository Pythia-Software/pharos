package archive

import (
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
	"testing"
)

func TestTeamsContentOutputRejectsStableSourceIdentity(t *testing.T) {
	inventory := teamsContentInventory{teamsV2Inventory: teamsV2Inventory{Items: []sourcewire.Item{{Token: "stableSyntheticToken", Witness: "stableSyntheticWitness"}}}}
	for _, raw := range []string{`{"answer":"contains stableSyntheticToken"}`, `{"answer":"stableSynthetic\u0054oken"}`, `{"stableSyntheticWitness":"x"}`} {
		if !teamsAnswerContainsSourceIdentity([]byte(raw), inventory) {
			t.Fatal("stable identity accepted")
		}
	}
	if teamsAnswerContainsSourceIdentity([]byte(`{"answer":"Source 1 contains synthetic work"}`), inventory) {
		t.Fatal("local label rejected")
	}
}
