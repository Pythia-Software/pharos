package teams

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestSourceIdentityV2SyntheticVector(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/teams/source-identity-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Key          string `json:"key_hex"`
		Provider     string `json:"provider"`
		Native       string `json:"native_id"`
		Other        string `json:"other_native_id"`
		Namespace    string `json:"account_namespace"`
		Token        string `json:"source_token"`
		Witness      string `json:"witness"`
		OtherWitness string `json:"other_witness"`
		Profile      string `json:"profile"`
		Version      int    `json:"profile_version"`
		Digest       string `json:"intrinsic_digest_hex"`
		Revision     string `json:"evidence_revision"`
	}
	if err = json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	key, _ := hex.DecodeString(v.Key)
	digest, _ := hex.DecodeString(v.Digest)
	ns, err := SourceWorkNamespace(key, v.Provider)
	if err != nil || ns != v.Namespace {
		t.Fatalf("namespace %q %v", ns, err)
	}
	token, err := SourceToken(key, v.Provider, ns, v.Native)
	if err != nil || token != v.Token {
		t.Fatalf("token %q %v", token, err)
	}
	witness, err := SourceWitness(key, v.Provider, "codex_thread", v.Native)
	if err != nil || witness != v.Witness {
		t.Fatalf("witness %q %v", witness, err)
	}
	other, err := SourceWitness(key, v.Provider, "codex_thread", v.Other)
	if err != nil || other != v.OtherWitness || other == witness {
		t.Fatalf("other witness %q %v", other, err)
	}
	otherNamespace, err := sourceIdentityMAC(key, []byte("pharos-account-namespace-v1"), []byte("codex"), []byte("synthetic-distinct-group"), []byte("other"))
	if err != nil {
		t.Fatal(err)
	}
	otherToken, err := SourceToken(key, v.Provider, otherNamespace, v.Native)
	if err != nil || otherToken == token {
		t.Fatal("different account assignments must produce different tokens")
	}
	revision, err := SourceEvidenceRevision(key, token, v.Profile, v.Version, digest)
	if err != nil || revision != v.Revision {
		t.Fatalf("revision %q %v", revision, err)
	}
	if _, err = SourceEvidenceRevision(key, token, v.Profile, 2, digest); err == nil {
		t.Fatal("unknown profile version accepted")
	}
}

func TestSourceProfileIdentityIsBoundToAnchor(t *testing.T) {
	key := make([]byte, 32)
	anchor, err := SourceToken(key, "codex", "synthetic-account", "thread")
	if err != nil {
		t.Fatal(err)
	}
	witness, err := SourceWitness(key, "codex", "codex_thread", "thread")
	if err != nil {
		t.Fatal(err)
	}
	token, childWitness, err := SourceProfileIdentity(key, anchor, witness, "codex_request_usage", 1)
	if err != nil {
		t.Fatal(err)
	}
	again, againWitness, err := SourceProfileIdentity(key, anchor, witness, "codex_request_usage", 1)
	if err != nil || token != again || childWitness != againWitness || token == anchor || childWitness == witness {
		t.Fatal("profile identity is not stable and distinct")
	}
	if token != "QmDktxSMM5dbYzwdex6tGjLH37TMch_GqNNwwiwbWJY" || childWitness != "6N7-W7P9VhSHUAd8IxhfUjobDwbQEHxseULpphLAilg" {
		t.Fatal("profile identity vector changed")
	}
	other, _ := SourceToken(key, "codex", "synthetic-account", "other")
	changed, _, err := SourceProfileIdentity(key, other, witness, "codex_request_usage", 1)
	if err != nil || changed == token {
		t.Fatal("profile token not anchored")
	}
	if _, _, err = SourceProfileIdentity(key, anchor, witness, "codex_request_usage", 2); err == nil {
		t.Fatal("unsupported profile version accepted")
	}
	if _, err = SourceEvidenceRevision(key, token, "codex_request_usage", 1, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
}
