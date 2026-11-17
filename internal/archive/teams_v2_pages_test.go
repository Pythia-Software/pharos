package archive

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

func TestTeamsV2PagesMatchSharedVector(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/teams/source-snapshot-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Page string `json:"page_json"`
		Seal string `json:"seal_json"`
	}
	if json.Unmarshal(raw, &v) != nil {
		t.Fatal("fixture")
	}
	page, err := sourcewire.DecodePage([]byte(v.Page))
	if err != nil {
		t.Fatal(err)
	}
	seal, err := sourcewire.DecodeSeal([]byte(v.Seal))
	if err != nil {
		t.Fatal(err)
	}
	got, err := teamsBuildV2Pages(teamsV2Inventory{Items: page.Items, Coverage: seal.Coverage, Complete: seal.InventoryComplete}, page.SnapshotID, page.LocalRevision, page.InventoryRevision, page.EvaluationRevision, page.ExpectedPriorSHA256)
	if err != nil || len(got.Pages) != 1 || string(got.Pages[0]) != v.Page || string(got.Seal) != v.Seal {
		t.Fatalf("shared vector mismatch: %v", err)
	}
}
