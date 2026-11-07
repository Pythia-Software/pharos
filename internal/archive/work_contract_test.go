package archive

import (
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
		Floor int `json:"catalog_compatibility_floor"`
		Tasks []struct {
			Kind     string `json:"kind"`
			Class    string `json:"execution_class"`
			Scope    string `json:"scope"`
			Boundary string `json:"boundary"`
			Inputs   string `json:"inputs"`
			Quality  string `json:"accepted_quality"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(data, &inventory); err != nil {
		t.Fatal(err)
	}
	if inventory.Floor != 23 {
		t.Fatal("the execution refactor must preserve the retained version-23 binary's catalog compatibility")
	}
	classes := map[string]bool{"coordinator": true, "population": true, "partition": true, "global": true, "source": true, "maintenance": true, "essential": true, "specialized": true}
	seen := map[string]bool{}
	for _, row := range inventory.Tasks {
		if seen[row.Kind] {
			t.Errorf("duplicate classification: %s", row.Kind)
		}
		seen[row.Kind] = true
		if _, ok := lookupOperationKind(row.Kind); !ok {
			t.Errorf("unregistered task %s", row.Kind)
		}
		if !classes[row.Class] || row.Scope == "" || row.Boundary == "" || row.Inputs == "" || row.Quality == "" {
			t.Errorf("incomplete execution contract for %s", row.Kind)
		}
	}
	for _, kind := range operationKinds {
		if !seen[kind.Kind] {
			t.Errorf("%s needs an explicit scope, quality and execution classification", kind.Kind)
		}
	}
}
