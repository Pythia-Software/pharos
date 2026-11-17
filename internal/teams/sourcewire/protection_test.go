package sourcewire

import (
	"encoding/json"
	"os"
	"testing"
)

func TestProtectionObservationVector(t *testing.T) {
	raw, err := os.ReadFile("../testdata/source-protection-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Observation  ProtectionObservation `json:"observation"`
		Canonical    string                `json:"canonical_json"`
		Digest       string                `json:"sha256"`
		Accepted     ProtectionReceipt     `json:"accepted_receipt"`
		Held         ProtectionReceipt     `json:"held_receipt"`
		AcceptedJSON string                `json:"accepted_receipt_json"`
		HeldJSON     string                `json:"held_receipt_json"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	b, digest, err := fixture.Observation.Bytes()
	if err != nil || string(b) != fixture.Canonical || digest != fixture.Digest {
		t.Fatalf("canonical protection vector: %v", err)
	}
	if _, err = DecodeProtectionObservation(b); err != nil {
		t.Fatal(err)
	}
	for _, vector := range []struct {
		receipt  ProtectionReceipt
		expected string
	}{{fixture.Accepted, fixture.AcceptedJSON}, {fixture.Held, fixture.HeldJSON}} {
		raw, e := json.Marshal(vector.receipt)
		if e != nil || string(raw) != vector.expected {
			t.Fatal("protection receipt vector differs")
		}
	}
	stale := fixture.Observation
	stale.Classification = Classification{State: "stale", Required: true, EvidenceRevision: fixture.Observation.Token, PolicySHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if stale.Validate() != nil {
		t.Fatal("valid stale provenance rejected")
	}
	stale.Classification.State = "clear"
	if stale.Validate() == nil {
		t.Fatal("stale clearance accepted as current")
	}
	for _, bad := range []ProtectionObservation{
		func() ProtectionObservation { x := fixture.Observation; x.Revision = 2; return x }(),
		func() ProtectionObservation { x := fixture.Observation; x.Classification.State = "clear"; return x }(),
		func() ProtectionObservation { x := fixture.Observation; x.Dependencies = []string{x.Token}; return x }(),
		func() ProtectionObservation { x := fixture.Observation; x.ReviewedFlagRevision = 1; return x }(),
	} {
		if bad.Validate() == nil {
			t.Fatal("invalid observation accepted")
		}
	}
}
