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
	"testing"

	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

func TestSignedProtectionObservation(t *testing.T) {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/source-protection-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Observation sourcewire.ProtectionObservation `json:"observation"`
		Canonical   string                           `json:"canonical_json"`
		Digest      string                           `json:"sha256"`
	}
	if err = json.Unmarshal(fixture, &f); err != nil {
		t.Fatal(err)
	}
	id := "00000000-0000-4000-8000-000000000003"
	held := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		message, e := signedMessage(id, r.Method, r.URL.EscapedPath(), r.Header.Get("X-Pharos-Timestamp"), r.Header.Get("X-Pharos-Nonce"), raw)
		signature, e2 := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Pharos-Signature"))
		if e != nil || e2 != nil || !ed25519.Verify(key.Public().(ed25519.PublicKey), []byte(message), signature) {
			t.Error("invalid signed observation")
		}
		if string(raw) != f.Canonical || r.URL.Path != "/v1/device/libraries/synthetic-library/source-protection/"+f.Observation.ObservationID {
			t.Error("unexpected observation")
		}
		if held {
			json.NewEncoder(w).Encode(sourcewire.ProtectionReceipt{Status: "held", ObservationID: f.Observation.ObservationID, ObservationSHA256: f.Digest, SafetyPending: true})
			return
		}
		json.NewEncoder(w).Encode(sourcewire.ProtectionReceipt{Status: "accepted", ObservationID: f.Observation.ObservationID, Revision: 1, ObservationSHA256: f.Digest, PrivacyRevision: 1, OriginProtection: "flagged", SafetyPending: true})
	}))
	defer server.Close()
	client, err := NewDeviceClient(server.URL, id, key, true)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := client.ObserveSourceProtection(context.Background(), "synthetic-library", []byte(f.Canonical))
	if err != nil || receipt.OriginProtection != "flagged" {
		t.Fatalf("observation receipt: %+v %v", receipt, err)
	}
	held = true
	receipt, err = client.ObserveSourceProtection(context.Background(), "synthetic-library", []byte(f.Canonical))
	if err != nil || receipt.Status != "held" || receipt.Revision != 0 {
		t.Fatalf("held receipt: %+v %v", receipt, err)
	}
}
