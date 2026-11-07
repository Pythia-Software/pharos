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
		Floor   int    `json:"catalog_compatibility_floor"`
		Window  string `json:"comparison_window"`
		Classes map[workClass]struct {
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
	// The floor is the retained baseline's schema, which the experiment harness
	// also reads. It must match only while classic and scheduled execution are
	// compared against that binary; the plan closes the window when it removes
	// the comparison path.
	switch inventory.Window {
	case "open":
		if inventory.Floor != catalogSchemaVersion {
			t.Fatalf("catalog schema %d, retained baseline schema %d: while the comparison window is open the retained binary must open this catalog. Rebuild the baseline at the new schema and update baseline_commit and catalog_compatibility_floor, or set comparison_window to closed once the comparison path is removed", catalogSchemaVersion, inventory.Floor)
		}
	case "closed":
	default:
		t.Fatalf("comparison_window is open or closed, not %q", inventory.Window)
	}
	// Each class owns one contract; operationKinds is the one registry of
	// which class each kind belongs to.
	for _, class := range workClasses {
		contract, ok := inventory.Classes[class]
		if !ok {
			t.Errorf("execution class %s has no contract", class)
		} else if contract.Scope == "" || contract.Boundary == "" || contract.Inputs == "" || contract.Quality == "" {
			t.Errorf("incomplete execution contract for class %s", class)
		}
	}
	if len(inventory.Classes) != len(workClasses) {
		t.Errorf("inventory documents %d classes, operation_kinds.go declares %d", len(inventory.Classes), len(workClasses))
	}
	if inventory.Kinds != nil {
		t.Error("the inventory must not classify kinds; each operationKind's Class does")
	}
	for _, kind := range operationKinds {
		if _, ok := inventory.Classes[kind.Class]; !ok {
			t.Errorf("%s needs an explicit scope, quality and execution class, not %q", kind.Kind, kind.Class)
		}
	}
}
