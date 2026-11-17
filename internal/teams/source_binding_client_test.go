package teams

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

func TestSourceBindingPinsAssignmentPages(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/teams/source-snapshot-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Page string `json:"page_json"`
		Seal string `json:"seal_json"`
	}
	if json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("fixture")
	}
	page, err := sourcewire.DecodePage([]byte(fixture.Page))
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	id := "a4c09e42-d0a6-45f0-89ef-2a03b7cff630"
	req := SourceBindRequest{BindingID: id, SnapshotID: page.SnapshotID, InventoryRevision: page.InventoryRevision, InventorySHA256: page.InventorySHA256, LocalRevision: page.LocalRevision, CatalogRevision: page.LocalRevision + 1, EvaluationRevision: page.EvaluationRevision, ConfigSHA256: strings.Repeat("a", 64), OwnershipRevision: 3, PrivacyRevision: 4}
	items := []sourcewire.Assignment{{Token: page.Items[0].Token, Decision: "owned"}, {Token: page.Items[1].Token, Decision: "unavailable"}}
	h, _ := sourcewire.NewAssignmentHasher(sourcewire.AssignmentHeader{BindingID: req.BindingID, SnapshotID: req.SnapshotID, InventorySHA256: req.InventorySHA256, OwnershipRevision: 3, PrivacyRevision: 4})
	for _, item := range items {
		if err = h.Add(item); err != nil {
			t.Fatal(err)
		}
	}
	digest, _, _ := h.Finish()
	binding := SourceBinding{SourceBindRequest: req, AssignmentSHA256: digest, AssignmentCount: 2, AssignmentPageCount: 1, BindingState: "ready", ProjectionState: "staged"}
	badPage := false
	badWatermark := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		message, e := signedMessage(id, r.Method, r.URL.EscapedPath(), r.Header.Get("X-Pharos-Timestamp"), r.Header.Get("X-Pharos-Nonce"), body)
		signature, e2 := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Pharos-Signature"))
		if e != nil || e2 != nil || !ed25519.Verify(key.Public().(ed25519.PublicKey), []byte(message), signature) {
			t.Error("bad signature")
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/source-watermark"):
			wmark := SourceWatermark{LocalRevision: 7, ReservedInventoryRevision: 3, ReservedInventorySHA256: req.InventorySHA256, AcceptedInventoryRevision: 3, AcceptedInventorySHA256: req.InventorySHA256}
			if badWatermark {
				wmark.AcceptedInventorySHA256 = strings.Repeat("b", 64)
			}
			json.NewEncoder(w).Encode(wmark)
		case strings.HasSuffix(r.URL.Path, "/seal"):
			if string(body) != fixture.Seal {
				t.Error("seal bytes changed")
			}
			json.NewEncoder(w).Encode(SourceSealReceipt{SnapshotID: req.SnapshotID, InventoryRevision: req.InventoryRevision, InventorySHA256: req.InventorySHA256, LocalRevision: req.LocalRevision, OwnershipRevision: 3, PrivacyRevision: 4})
		case strings.HasSuffix(r.URL.Path, "/source-bindings"):
			var got SourceBindRequest
			if json.Unmarshal(body, &got) != nil || got != req {
				t.Error("binding request changed")
			}
			json.NewEncoder(w).Encode(binding)
		case strings.HasSuffix(r.URL.Path, "/assignments/0"):
			p := sourceAssignmentPage{BindingID: req.BindingID, SnapshotID: req.SnapshotID, InventorySHA256: req.InventorySHA256, OwnershipRevision: 3, PrivacyRevision: 4, AssignmentSHA256: digest, Index: 0, PageCount: 1, Items: items}
			if badPage {
				p.PrivacyRevision++
			}
			json.NewEncoder(w).Encode(p)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c, err := NewDeviceClient(server.URL, id, key, true)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Destroy()
	if _, err = c.SourceWatermark(context.Background(), "synthetic-library"); err != nil {
		t.Fatal(err)
	}
	badWatermark = true
	if _, err = c.SourceWatermark(context.Background(), "synthetic-library"); err == nil {
		t.Fatal("conflicting watermark accepted")
	}
	badWatermark = false
	if _, err = c.SealSourceSnapshot(context.Background(), id, []byte(fixture.Seal)); err != nil {
		t.Fatal(err)
	}
	got, err := c.BindSourceInventory(context.Background(), id, req)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectionState != "staged" {
		t.Fatal("staged projection lost")
	}
	if assignments, err := c.SourceAssignments(context.Background(), id, got, []string{items[0].Token, items[1].Token}); err != nil || len(assignments) != 2 {
		t.Fatalf("assignments %v %v", assignments, err)
	}
	badPage = true
	if _, err = c.SourceAssignments(context.Background(), id, got, []string{items[0].Token, items[1].Token}); err == nil {
		t.Fatal("stale privacy epoch accepted")
	}
	got.BindingState = "awaiting_barrier"
	if _, err = c.SourceAssignments(context.Background(), id, got, []string{}); err == nil {
		t.Fatal("pending binding used")
	}
}
