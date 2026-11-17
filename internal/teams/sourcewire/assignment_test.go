package sourcewire

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestSyntheticAssignmentVector(t *testing.T) {
	raw, err := os.ReadFile("../../../testdata/teams/source-assignments-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Canonical string `json:"canonical_utf8"`
		SHA       string `json:"sha256"`
	}
	if json.Unmarshal(raw, &v) != nil {
		t.Fatal("fixture")
	}
	const prefix = "pharos-source-assignments-v2\n"
	if !strings.HasPrefix(v.Canonical, prefix) || SHA256([]byte(v.Canonical)) != v.SHA {
		t.Fatal("fixture digest")
	}
	var data struct {
		AssignmentHeader
		Items []Assignment `json:"items"`
	}
	if json.Unmarshal([]byte(strings.TrimPrefix(v.Canonical, prefix)), &data) != nil {
		t.Fatal("assignment fixture")
	}
	h, err := NewAssignmentHasher(data.AssignmentHeader)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range data.Items {
		if err = h.Add(item); err != nil {
			t.Fatal(err)
		}
	}
	digest, count, err := h.Finish()
	if err != nil || digest != v.SHA || count != len(data.Items) {
		t.Fatal("assignment differs from shared fixture")
	}
}
