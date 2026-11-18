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

func TestSignedProtectionReceiptLookup(t *testing.T) {
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
	const installation = "00000000-0000-4000-8000-000000000003"
	mode := "accepted"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		message, e := signedMessage(installation, r.Method, r.URL.EscapedPath(), r.Header.Get("X-Pharos-Timestamp"), r.Header.Get("X-Pharos-Nonce"), raw)
		sig, e2 := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Pharos-Signature"))
		if e != nil || e2 != nil || !ed25519.Verify(key.Public().(ed25519.PublicKey), []byte(message), sig) || r.Method != http.MethodGet || len(raw) != 0 || r.URL.Path != "/v1/device/libraries/synthetic-library/source-protection/"+f.Observation.ObservationID+"/receipts/"+f.Digest {
			t.Error("unexpected signed lookup")
		}
		if mode == "missing" {
			http.Error(w, "unavailable", http.StatusNotFound)
			return
		}
		receipt := sourcewire.ProtectionReceipt{Status: "accepted", ObservationID: f.Observation.ObservationID, Revision: f.Observation.Revision, ObservationSHA256: f.Digest, PrivacyRevision: 1, OriginProtection: f.Observation.Protection, SafetyPending: true}
		if mode == "held" {
			receipt.Status = "held"
		}
		if mode == "mismatch" {
			receipt.ObservationSHA256 = strings.Repeat("0", 64)
		}
		_ = json.NewEncoder(w).Encode(receipt)
	}))
	defer server.Close()
	client, err := NewDeviceClient(server.URL, installation, key, true)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := client.LookupSourceProtectionReceipt(context.Background(), "synthetic-library", []byte(f.Canonical))
	if err != nil || receipt.Status != "accepted" {
		t.Fatalf("accepted lookup: %+v %v", receipt, err)
	}
	for _, mode = range []string{"missing", "held", "mismatch"} {
		if _, err = client.LookupSourceProtectionReceipt(context.Background(), "synthetic-library", []byte(f.Canonical)); err == nil {
			t.Fatalf("accepted %s lookup", mode)
		}
	}
}
