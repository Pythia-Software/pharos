package archive

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Exercise the parser and assignment verifier while the platform
// loader sees conflicting, valid zone data for the same exact zone name.
func TestScheduledContractHostileTimezone(t *testing.T) {
	if os.Getenv("PHAROS_HOSTILE_CONTRACT_CHILD") == "1" {
		data, e := os.ReadFile("testdata/teams-report-series/scheduled_timing_vectors.json")
		if e != nil {
			t.Fatal(e)
		}
		var vectors []struct {
			Name            string                    `json:"name"`
			Config          json.RawMessage           `json:"config"`
			Valid           bool                      `json:"valid"`
			Assignment      *teamsScheduledAssignment `json:"assignment"`
			AssignmentValid *bool                     `json:"assignment_valid"`
		}
		if e = json.Unmarshal(data, &vectors); e != nil {
			t.Fatal(e)
		}
		for _, v := range vectors {
			c, e := parseTeamsScheduledConfig(v.Config)
			if (e == nil) != v.Valid {
				t.Fatalf("%s parser: %v", v.Name, e)
			}
			if e == nil && v.Assignment != nil {
				want := true
				if v.AssignmentValid != nil {
					want = *v.AssignmentValid
				}
				if got := matchesPersistedTeamsScheduledAssignment(c, *v.Assignment, *v.Assignment); got != want {
					t.Fatalf("%s assignment %v", v.Name, got)
				}
			}
		}
		return
	}
	archive, err := os.ReadFile("zoneinfo_2026c.zip")
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	var utc []byte
	for _, f := range z.File {
		if f.Name == "UTC" {
			r, e := f.Open()
			if e != nil {
				t.Fatal(e)
			}
			utc, e = io.ReadAll(r)
			_ = r.Close()
			if e != nil {
				t.Fatal(e)
			}
		}
	}
	if len(utc) == 0 {
		t.Fatal("UTC missing")
	}
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, err := w.CreateHeader(&zip.FileHeader{Name: "America/New_York", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(utc); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "zones.zip")
	if err = os.WriteFile(path, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestScheduledContractHostileTimezone$")
	cmd.Env = append(os.Environ(), "PHAROS_HOSTILE_CONTRACT_CHILD=1", "ZONEINFO="+path, "TZDIR="+t.TempDir())
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("hostile contract: %v: %s", e, out)
	}
}
