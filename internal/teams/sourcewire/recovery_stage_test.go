package sourcewire

import (
	"encoding/json"
	"os"
	"testing"
)

func TestRecoveryStageProfileGolden(t *testing.T) {
	data, err := os.ReadFile("../testdata/source-recovery-stage-profile-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Chunk0JSON   string `json:"chunk0_json"`
		Chunk0SHA256 string `json:"chunk0_sha256"`
		Chunk1JSON   string `json:"chunk1_json"`
		Chunk1SHA256 string `json:"chunk1_sha256"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct{ raw, digest string }{{fixture.Chunk0JSON, fixture.Chunk0SHA256}, {fixture.Chunk1JSON, fixture.Chunk1SHA256}} {
		stage, err := DecodeRecoveryStagePage([]byte(pair.raw))
		if err != nil {
			t.Fatal(err)
		}
		raw, digest, err := stage.Bytes()
		if err != nil || string(raw) != pair.raw || digest != pair.digest {
			t.Fatalf("canonical stage mismatch: %s %v", digest, err)
		}
	}
}
