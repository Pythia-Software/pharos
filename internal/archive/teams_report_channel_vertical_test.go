package archive

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

func TestChannelReviewRequiresPilotExactCapabilityAndCollectionLabels(t *testing.T) {
	raw, err := os.ReadFile("testdata/teams-report-series/manual_series_config.json")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]json.RawMessage
	if err = json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	channel, err := os.ReadFile("testdata/channel_report.json")
	if err != nil {
		t.Fatal(err)
	}
	config["publication_v2"] = channel
	raw, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	w := teams.Work{Kind: "deterministic", Config: raw, ConfigSHA256: teamsPayloadHash(raw), PopulationKey: "current", Period: json.RawMessage(`{"key":"once"}`), ExpiresAt: time.Now().Add(time.Hour)}
	m := teamsCapabilityManifest{SchemaVersion: 2, AppVersion: "0.10.0", Features: []string{"deterministic_query_v1", "reader_labels_v1", teamsChannelReportFeature, "source_snapshot_v2"}}
	if _, err = teamsPlanFromWork(w); err == nil || teamsNumericWorkEligible(w, m) {
		t.Fatal("disabled channel pilot admitted work")
	}
	override(t, &teamsChannelReportPilotEnabled, true)
	if _, err = teamsPlanFromWork(w); err != nil {
		t.Fatal(err)
	}
	if !teamsNumericWorkEligible(w, m) || !validTeamsSeriesReview(raw, w.ConfigSHA256, w.ConfigSHA256) || validTeamsSeriesReview(raw, w.ConfigSHA256, "") {
		t.Fatal("exact review or capability failed")
	}
	m.Features = []string{"deterministic_query_v1", "reader_labels_v1", "report_deterministic_v2", "source_snapshot_v2"}
	if teamsNumericWorkEligible(w, m) {
		t.Fatal("legacy series capability admitted channel")
	}
	labels := &teams.ReaderLabels{Version: 1, ConfigSHA256: w.ConfigSHA256, Entries: []teams.ReaderLabel{{PersonID: "0c000000-0000-4000-8000-000000000001", Status: "active", DisplayName: "Synthetic"}}}
	if !labelsForReview(w, labels, time.Now()) {
		t.Fatal("valid collection reader labels rejected")
	}
	labels.Entries = append(labels.Entries, teams.ReaderLabel{PersonID: "CTEST", Status: "active", DisplayName: "channel"})
	if validReaderLabels(w, labels, time.Now(), false) {
		t.Fatal("channel was treated as a person")
	}
}
