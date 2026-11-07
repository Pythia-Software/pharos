package archive

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// Adding a kind requires deciding its correctness boundary. Unknown work must
// not become an unbudgeted background path through a permissive default.
func TestWorkTaskInventoryCoversRegisteredKinds(t *testing.T) {
	data, err := os.ReadFile("../../docs/work-task-inventory.json")
	if err != nil {
		t.Fatal(err)
	}
	var inventory struct {
		Floor   int `json:"catalog_compatibility_floor"`
		Classes map[string]struct {
			Scope    string `json:"scope"`
			Boundary string `json:"boundary"`
			Inputs   string `json:"inputs"`
			Quality  string `json:"accepted_quality"`
		} `json:"classes"`
		Kinds json.RawMessage `json:"kinds"`
	}
	if err := json.Unmarshal(data, &inventory); err != nil {
		t.Fatal(err)
	}
	if inventory.Floor != catalogSchemaVersion {
		t.Fatalf("inventory floor %d, catalog schema %d: the execution refactor must keep the retained binary's catalog compatibility", inventory.Floor, catalogSchemaVersion)
	}
	// Each class owns one contract; a kind names its class explicitly.
	known := map[string]bool{"coordinator": true, "population": true, "partition": true, "global": true, "source": true, "maintenance": true, "essential": true, "specialized": true}
	for class, contract := range inventory.Classes {
		if !known[class] {
			t.Errorf("unknown execution class %s", class)
		}
		if contract.Scope == "" || contract.Boundary == "" || contract.Inputs == "" || contract.Quality == "" {
			t.Errorf("incomplete execution contract for class %s", class)
		}
	}
	// Decoded as tokens: a map would silently keep the last of two entries.
	decoder := json.NewDecoder(bytes.NewReader(inventory.Kinds))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		t.Fatalf("kinds must map each kind to its class: %v", err)
	}
	seen := map[string]bool{}
	for decoder.More() {
		var kind, class string
		if token, err := decoder.Token(); err != nil {
			t.Fatal(err)
		} else {
			kind, _ = token.(string)
		}
		if err := decoder.Decode(&class); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if seen[kind] {
			t.Errorf("duplicate classification: %s", kind)
		}
		seen[kind] = true
		if _, ok := lookupOperationKind(kind); !ok {
			t.Errorf("unregistered task %s", kind)
		}
		if _, ok := inventory.Classes[class]; !ok {
			t.Errorf("%s names unknown execution class %q", kind, class)
		}
	}
	for _, kind := range operationKinds {
		if !seen[kind.Kind] {
			t.Errorf("%s needs an explicit scope, quality and execution classification", kind.Kind)
		}
	}
}
