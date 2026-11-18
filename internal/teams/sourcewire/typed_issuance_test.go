package sourcewire

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestTypedBindingIDVector(t *testing.T) {
	var f struct {
		Domain    string `json:"domain"`
		Canonical string `json:"canonical_identity_json"`
		Digest    string `json:"sha256"`
		Scope     struct {
			Org          string `json:"org_id"`
			Person       string `json:"person_id"`
			Installation string `json:"installation_id"`
			Library      string `json:"library_id"`
			Work         string `json:"work_id"`
		} `json:"scope"`
		Request TypedBindingRequest `json:"request_without_binding_id"`
		ID      string              `json:"binding_id"`
	}
	raw, err := os.ReadFile("../testdata/typed-source-binding-id-v2.json")
	if err != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal("fixture", err)
	}
	sum := sha256.Sum256([]byte(f.Domain + f.Canonical))
	if f.Domain != "pharos-typed-source-binding-id-v2\n" || hex.EncodeToString(sum[:]) != f.Digest {
		t.Fatal("canonical identity preimage does not produce the fixture digest")
	}
	id, err := TypedBindingID(f.Scope.Org, f.Scope.Person, f.Scope.Installation, f.Scope.Library, f.Scope.Work, f.Request)
	if err != nil || id != f.ID {
		t.Fatal("identity vector", id, err)
	}
	other, err := TypedBindingID(f.Scope.Org, f.Scope.Person, f.Scope.Installation, f.Scope.Library, f.Scope.Work, f.Request)
	if err != nil || other != id {
		t.Fatal("unstable identity")
	}
	f.Request.IssuedAt++
	other, err = TypedBindingID(f.Scope.Org, f.Scope.Person, f.Scope.Installation, f.Scope.Library, f.Scope.Work, f.Request)
	if err != nil || other == id {
		t.Fatal("issuance identity")
	}
}
