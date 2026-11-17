package teams

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestCodexContentIdentitySharedVector(t *testing.T) {
	raw, e := os.ReadFile("../../testdata/teams/codex-content-identity-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	var v struct {
		KeyHex           string `json:"key_hex"`
		NativeID         string `json:"native_id"`
		RawUTF8          string `json:"raw_utf8"`
		WorkNamespace    string `json:"work_namespace"`
		ThreadToken      string `json:"thread_token"`
		ThreadWitness    string `json:"thread_witness"`
		ContentToken     string `json:"content_token"`
		ContentWitness   string `json:"content_witness"`
		ComponentToken   string `json:"component_token"`
		ComponentWitness string `json:"component_witness"`
		Intrinsic        string `json:"intrinsic_sha256"`
		Revision         string `json:"evidence_revision"`
	}
	if e = json.Unmarshal(raw, &v); e != nil {
		t.Fatal(e)
	}
	key, e := hex.DecodeString(v.KeyHex)
	if e != nil {
		t.Fatal(e)
	}
	account, e := SourceWorkNamespace(key, "codex")
	if e != nil || account != v.WorkNamespace {
		t.Fatal("namespace mismatch")
	}
	for _, c := range []struct{ provider, want string }{{"codex", v.ThreadToken}, {"codex-content", v.ContentToken}, {"codex-component", v.ComponentToken}} {
		got, e := SourceToken(key, c.provider, account, v.NativeID)
		if e != nil || got != c.want {
			t.Fatal("token mismatch")
		}
	}
	for _, c := range []struct{ unit, want string }{{"codex_thread", v.ThreadWitness}, {"codex_transcript", v.ContentWitness}, {"content_component", v.ComponentWitness}} {
		got, e := SourceWitness(key, "codex", c.unit, v.NativeID)
		if e != nil || got != c.want {
			t.Fatal("witness mismatch")
		}
	}
	h := sha256.New()
	h.Write([]byte("pharos-codex-transcript-v1\x00"))
	h.Write([]byte(v.RawUTF8))
	intrinsic := h.Sum(nil)
	if hex.EncodeToString(intrinsic) != v.Intrinsic {
		t.Fatal("digest mismatch")
	}
	revision, e := SourceEvidenceRevision(key, v.ContentToken, "codex_transcript", 1, intrinsic)
	if e != nil || revision != v.Revision {
		t.Fatal("revision mismatch")
	}
}
