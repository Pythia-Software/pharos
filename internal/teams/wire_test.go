package teams

import (
	"encoding/json"
	"os"
	"testing"
)

func TestManifestBytesAndValidation(t *testing.T) {
	vector, err := os.ReadFile("testdata/work-manifest-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Manifest  Manifest `json:"manifest"`
		Canonical string   `json:"canonical_json"`
		SHA256    string   `json:"manifest_sha256"`
	}
	if json.Unmarshal(vector, &fixture) != nil {
		t.Fatal("invalid manifest fixture")
	}
	data, digest, err := fixture.Manifest.Bytes()
	if err != nil || string(data) != fixture.Canonical || digest != fixture.SHA256 {
		t.Fatalf("manifest fixture: %v", err)
	}
	m := Manifest{LocalRevision: 1, Complete: true, SourceManifest: []Source{{Token: "knhABoFGkiIWS5lojTjS5weYgzs1b4lahJBqOXOWSag", Protection: "private"}}}
	b, digest, err := m.Bytes()
	want := `{"local_revision":1,"complete":true,"source_manifest":[{"token":"knhABoFGkiIWS5lojTjS5weYgzs1b4lahJBqOXOWSag","evidence_revision":"","protection":"private"}]}`
	if err != nil || string(b) != want || digest != hashBytes([]byte(want)) {
		t.Fatalf("manifest %s %s %v", b, digest, err)
	}
	m.SourceManifest = append(m.SourceManifest, m.SourceManifest[0])
	if _, _, err = m.Bytes(); err == nil {
		t.Fatal("duplicate source accepted")
	}
}
func TestWorkConfigHashAndRoutes(t *testing.T) {
	raw := json.RawMessage(`{"question":"synthetic","query_url":"/analyze"}`)
	w := Work{Config: raw, ConfigSHA256: hashBytes(raw)}
	if err := w.VerifyConfig(); err != nil {
		t.Fatal(err)
	}
	w.Config = json.RawMessage(`{"question": "synthetic","query_url":"/analyze"}`)
	if w.VerifyConfig() == nil {
		t.Fatal("changed bytes accepted")
	}
	w.Config = json.RawMessage(`{"question":"one","question":"two"}`)
	w.ConfigSHA256 = hashBytes(w.Config)
	if w.VerifyConfig() == nil {
		t.Fatal("ambiguous config accepted")
	}
	if _, err := WorkPath("a4c09e42-d0a6-45f0-89ef-2a03b7cff630", "result"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../other", "..", ".", "x/y", "", "x?secret"} {
		if _, err := LibraryPath(id, "privacy"); err == nil {
			t.Fatalf("unsafe library ID %q", id)
		}
	}
	if _, err := WorkPath("a4c09e42-d0a6-45f0-89ef-2a03b7cff630", "../receipt"); err == nil {
		t.Fatal("unsafe operation")
	}
}

func TestSubmissionPreservesApprovedPayloadBytes(t *testing.T) {
	payload := json.RawMessage(`{ "text": "<synthetic>", "n": 1 }`)
	s := Submission{Payload: payload, PayloadSHA256: hashBytes(payload)}
	body, err := SubmissionBytes(s)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip Submission
	if json.Unmarshal(body, &roundTrip) != nil || string(roundTrip.Payload) != string(payload) {
		t.Fatalf("payload changed: %s", body)
	}
}
