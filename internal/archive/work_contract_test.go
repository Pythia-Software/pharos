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
	// This is metadata about a retained artifact, not a restriction on future
	// catalog migrations. The experiment harness enforces compatibility when
	// admitting a corpus; ordinary tests only validate the metadata's shape.
	switch inventory.Window {
	case "open", "closed":
	default:
		t.Fatalf("comparison_window is open or closed, not %q", inventory.Window)
	}
	if inventory.Floor < 1 {
		t.Fatal("catalog_compatibility_floor must identify the retained artifact's schema")
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
