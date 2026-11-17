package archive

import (
	"encoding/json"
	"os"
	"testing"
)

func TestTeamsArtifactSyntheticContractVectors(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/teams/artifact-structured-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name   string          `json:"name"`
		Schema json.RawMessage `json:"schema"`
		Answer json.RawMessage `json:"answer"`
		Valid  bool            `json:"valid"`
	}
	if json.Unmarshal(raw, &cases) != nil || len(cases) == 0 {
		t.Fatal("invalid fixture")
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			got := teamsValidateArtifact(tc.Schema, tc.Answer) == nil
			if got != tc.Valid {
				t.Fatalf("validation %v, expected %v", got, tc.Valid)
			}
		})
	}
}
