package teams

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestContentBindingSharedVectorAndLocalClosure(t *testing.T) {
	raw, e := os.ReadFile("../../testdata/teams/content-binding-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Binding ContentBinding `json:"binding"`
		SHA256  string         `json:"sha256"`
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	expected := append([]ContentEvidence(nil), fixture.Binding.Components...)
	if e = VerifyContentBinding(fixture.Binding, expected, fixture.SHA256); e != nil {
		t.Fatal(e)
	}
	fixture.Binding.Components[0].EffectiveProtected = true
	if e = VerifyContentBinding(fixture.Binding, expected, fixture.SHA256); e == nil {
		t.Fatal("protected evidence accepted")
	}
	fixture.Binding.Components[0] = expected[0]
	expected[0].EvidenceRevision = "changed"
	if e = VerifyContentBinding(fixture.Binding, expected, fixture.SHA256); e == nil {
		t.Fatal("different local closure accepted")
	}
}

func TestContentBindingRejectsAlteredClosurePage(t *testing.T) {
	raw, e := os.ReadFile("../../testdata/teams/content-binding-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Binding ContentBinding `json:"binding"`
		SHA256  string         `json:"sha256"`
	}
	if e = json.Unmarshal(raw, &fixture); e != nil {
		t.Fatal(e)
	}
	b := fixture.Binding
	b.DecisionID = "00000000-0000-4000-8000-000000000004"
	b.SHA256 = ""
	raw, e = json.Marshal(b)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(append([]byte("pharos-content-binding-v1\x00"), raw...))
	digest := hex.EncodeToString(sum[:])
	altered := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			json.NewEncoder(w).Encode(contentBindReceipt{BindingID: "00000000-0000-4000-8000-000000000005", BindingSHA256: digest, ComponentCount: len(b.Components)})
			return
		}
		items := append([]ContentEvidence(nil), b.Components...)
		if altered {
			items[0].EffectiveProtected = true
		}
		json.NewEncoder(w).Encode(map[string]any{"binding_id": "00000000-0000-4000-8000-000000000005", "binding_sha256": digest, "index": 0, "page_count": 1, "items": items})
	}))
	defer server.Close()
	_, key, e := ed25519.GenerateKey(nil)
	if e != nil {
		t.Fatal(e)
	}
	client, e := NewDeviceClient(server.URL, b.InstallationID, key, true)
	if e != nil {
		t.Fatal(e)
	}
	defer client.Destroy()
	request := ContentBindRequest{BindingID: "00000000-0000-4000-8000-000000000005", SnapshotID: b.SnapshotID, InventoryRevision: b.InventoryRevision, InventorySHA256: b.InventorySHA256, LocalRevision: b.LocalRevision, CatalogRevision: b.CatalogRevision, EvaluationRevision: b.EvaluationRevision, ConfigSHA256: b.ConfigSHA256, PrivacyRevision: b.PrivacyRevision, OwnershipRevision: b.OwnershipRevision, DecisionID: b.DecisionID, Processing: b.Processing, Components: []string{"component"}}
	if _, e = client.BindContent(context.Background(), b.WorkID, b.LibraryID, request, b.Components); e != nil {
		t.Fatal(e)
	}
	altered = true
	if _, e = client.BindContent(context.Background(), b.WorkID, b.LibraryID, request, b.Components); e == nil {
		t.Fatal("altered server page accepted")
	}
}
