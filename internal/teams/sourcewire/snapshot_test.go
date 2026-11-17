package sourcewire

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestSyntheticSnapshotVector(t *testing.T) {
	raw, err := os.ReadFile("../../../testdata/teams/source-snapshot-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		PageJSON        string `json:"page_json"`
		PageSHA256      string `json:"page_sha256"`
		InventorySHA256 string `json:"inventory_sha256"`
		SealJSON        string `json:"seal_json"`
	}
	if json.Unmarshal(raw, &v) != nil {
		t.Fatal("invalid fixture")
	}
	page, err := DecodePage([]byte(v.PageJSON))
	if err != nil || len(page.Items) != 2 || SHA256([]byte(v.PageJSON)) != v.PageSHA256 {
		t.Fatalf("invalid synthetic page: %v", err)
	}
	seal, err := DecodeSeal([]byte(v.SealJSON))
	if err != nil || seal.FinalPageSHA256 != v.PageSHA256 || seal.ItemCount != len(page.Items) {
		t.Fatalf("invalid synthetic seal: %v", err)
	}
	h, err := NewInventoryHasher(seal.InventoryComplete, seal.Coverage)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if err = h.Add(item); err != nil {
			t.Fatal(err)
		}
	}
	digest, count, err := h.Finish()
	if err != nil || count != len(page.Items) || digest != v.InventorySHA256 || page.InventorySHA256 != digest || seal.InventorySHA256 != digest {
		t.Fatal("inventory digest differs from shared vector")
	}
}
func TestSnapshotRejectsSemanticAliasesAndInvalidBounds(t *testing.T) {
	raw, err := os.ReadFile("../../../testdata/teams/source-snapshot-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		PageJSON string `json:"page_json"`
		SealJSON string `json:"seal_json"`
	}
	if json.Unmarshal(raw, &v) != nil {
		t.Fatal("invalid fixture")
	}
	for _, bad := range []string{
		strings.Replace(v.PageJSON, `"local_revision":7`, `"Local_Revision":7`, 1),
		strings.Replace(v.PageJSON, `"local_revision":7`, `"local_revision":7,"local_revision":7`, 1),
		strings.Replace(v.PageJSON, `"index":0`, `"index":1`, 1),
		strings.Replace(v.PageJSON, `"version":1`, `"version":2`, 1),
		strings.Replace(v.PageJSON, `"dependencies":[]`, `"dependencies":null`, 1),
		" " + v.PageJSON,
	} {
		if _, err := DecodePage([]byte(bad)); err == nil {
			t.Fatal("invalid snapshot page accepted")
		}
	}
	badSeal := strings.Replace(v.SealJSON, `"page_count":1`, `"page_count":101`, 1)
	if _, err := DecodeSeal([]byte(badSeal)); err == nil {
		t.Fatal("oversize snapshot seal accepted")
	}
}
