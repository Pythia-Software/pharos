package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

func TestTeamsManualReportSeriesContract(t *testing.T) {
	raw, e := os.ReadFile("testdata/teams-report-series/manual_series_config.json")
	if e != nil {
		t.Fatal(e)
	}
	makeWork := func(b []byte) teams.Work {
		sum := sha256.Sum256(b)
		return teams.Work{Kind: "deterministic", Config: json.RawMessage(b), ConfigSHA256: hex.EncodeToString(sum[:]), PopulationKey: "current", Period: json.RawMessage(`{"key":"once"}`), ExpiresAt: time.Now().Add(time.Hour)}
	}
	w := makeWork(raw)
	if w.ConfigSHA256 != "01779e79d6812c11354ba411b435f5f98d7161d8a8c45ca83504f11e8d8cb3be" {
		t.Fatal("shared config digest mismatch")
	}
	if _, e = teamsPlanFromWork(w); e != nil {
		t.Fatal(e)
	}
	alias := []byte(strings.Replace(string(raw), `"publication_v2"`, `"Publication_V2"`, 1))
	duplicate := []byte(strings.Replace(string(raw), `"publication_v2":`, `"publication_v2":null,"publication_v2":`, 1))
	if validTeamsSeriesReview(alias, teamsPayloadHash(alias), "") || validTeamsSeriesReview(duplicate, teamsPayloadHash(duplicate), "") {
		t.Fatal("ambiguous publication bypassed review binding")
	}
	if !validTeamsSeriesReview(raw, w.ConfigSHA256, w.ConfigSHA256) || validTeamsSeriesReview(raw, w.ConfigSHA256, "") || validTeamsSeriesReview(raw, w.ConfigSHA256, strings.Repeat("0", 64)) || validTeamsSeriesReview(append(append([]byte(nil), raw...), ' '), w.ConfigSHA256, w.ConfigSHA256) {
		t.Fatal("series review did not bind exact displayed config")
	}

	legacyConfig := []byte(`{"query_url":"legacy"}`)
	if !validTeamsSeriesReview(legacyConfig, teamsPayloadHash(legacyConfig), "") || validTeamsSeriesReview(legacyConfig, teamsPayloadHash(legacyConfig), w.ConfigSHA256) {
		t.Fatal("legacy review did not reject a foreign config hash")
	}

	legacy := teamsCapabilityManifest{SchemaVersion: 1, AppVersion: "0.10.0", Capabilities: []string{"deterministic_query_v1", "report_deterministic_v1", "source_snapshot_v2"}}
	if teamsNumericWorkEligible(w, legacy) {
		t.Fatal("v1 capability authorized v2 consent")
	}
	modern := teamsCapabilityManifest{SchemaVersion: 2, AppVersion: "0.10.0", Features: []string{"deterministic_query_v1", "report_deterministic_v2", "source_snapshot_v2"}, Profiles: []teams.CapabilityProfile{}}
	if !teamsNumericWorkEligible(w, modern) {
		t.Fatal("matching v2 capability rejected")
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"coverage":["complete"]`, `"coverage":["complete","freshness"]`, 1),
		strings.Replace(string(raw), `"withdrawal_does_not_remove_published_snapshots"`, `"unrecognized"`, 1),
		strings.Replace(string(raw), `"approval_policy":"review"`, `"approval_policy":"metrics_or_review"`, 1),
		strings.Replace(string(raw), `"unit":"tokens"`, `"unit":"usd"`, 1),
		strings.Replace(string(raw), `"schema_version":2`, `"schema_version":1`, 1),
		strings.Replace(string(raw), `"publications":[]`, `"publications":[{}]`, 1),
	} {
		if _, e = teamsPlanFromWork(makeWork([]byte(bad))); e == nil {
			t.Fatalf("unsupported v2 config accepted: %s", bad)
		}
	}
	w.ConfigSHA256 = strings.Repeat("0", 64)
	if _, e = teamsPlanFromWork(w); e == nil {
		t.Fatal("changed config digest accepted")
	}
}
