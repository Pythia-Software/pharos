package archive

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestPinnedScheduledZoneData(t *testing.T) {
	sum := sha256.Sum256(teamsZoneinfo2026c)
	if hex.EncodeToString(sum[:]) != "b2d18a7c8fa8142097a48c99609fb3c92db5ee98bc740294e57eab8ae9f94779" {
		t.Fatal("timezone archive changed")
	}
	z, err := zip.NewReader(bytes.NewReader(teamsZoneinfo2026c), int64(len(teamsZoneinfo2026c)))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(z.File))
	var utc []byte
	for _, f := range z.File {
		names = append(names, f.Name)
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
	sort.Strings(names)
	inventory, err := os.ReadFile("iana_zone_names.txt")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, "\n")+"\n" != string(inventory) {
		t.Fatal("archive and approved names differ")
	}
	for _, name := range names {
		if _, err = loadTeamsScheduledLocation(name); err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
	}
	for _, name := range []string{"Local", "america/denver", "../UTC", ""} {
		if _, err = loadTeamsScheduledLocation(name); err == nil {
			t.Fatalf("accepted zone %q", name)
		}
	}
	if len(utc) == 0 {
		t.Fatal("missing UTC zone")
	}
	// A valid hostile archive is stronger than an invalid directory: legacy
	// time.LoadLocation sees Denver as UTC, while this contract must not.
	if os.Getenv("PHAROS_SCHEDULED_TZ_CHILD") == "1" {
		host, e := time.LoadLocation("America/Denver")
		if e != nil {
			t.Fatal(e)
		}
		_, hostOffset := time.Date(2030, 7, 1, 0, 0, 0, 0, time.UTC).In(host).Zone()
		pinned, e := loadTeamsScheduledLocation("America/Denver")
		if e != nil {
			t.Fatal(e)
		}
		_, pinnedOffset := time.Date(2030, 7, 1, 0, 0, 0, 0, time.UTC).In(pinned).Zone()
		if hostOffset != 0 || pinnedOffset != -6*3600 {
			t.Fatalf("host=%d pinned=%d", hostOffset, pinnedOffset)
		}
		data, e := os.ReadFile("testdata/teams-report-series/scheduled_calendar_vectors.json")
		if e != nil {
			t.Fatal(e)
		}
		var vectors []struct {
			Cadence  string    `json:"cadence"`
			Timezone string    `json:"timezone"`
			Anchor   time.Time `json:"anchor"`
			Index    int       `json:"index"`
			Boundary time.Time `json:"boundary"`
		}
		if e = json.Unmarshal(data, &vectors); e != nil {
			t.Fatal(e)
		}
		for _, v := range vectors {
			got, e := (teamsScheduledCalendar{Cadence: v.Cadence, Timezone: v.Timezone, AnchorAt: v.Anchor}).Boundary(v.Index)
			if e != nil || !got.Equal(v.Boundary) {
				t.Fatalf("hostile calendar %s %v %v", v.Timezone, got, e)
			}
		}
		return
	}
	path := filepath.Join(t.TempDir(), "hostile.zip")
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, e := w.CreateHeader(&zip.FileHeader{Name: "America/Denver", Method: zip.Store})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.Write(utc); e != nil {
		t.Fatal(e)
	}
	f, e = w.CreateHeader(&zip.FileHeader{Name: "America/New_York", Method: zip.Store})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.Write(utc); e != nil {
		t.Fatal(e)
	}
	if e = w.Close(); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, b.Bytes(), 0600); e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPinnedScheduledZoneData$")
	cmd.Env = append(os.Environ(), "PHAROS_SCHEDULED_TZ_CHILD=1", "ZONEINFO="+path, "TZDIR="+t.TempDir())
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("hostile timezone subprocess: %v: %s", e, out)
	}
}
