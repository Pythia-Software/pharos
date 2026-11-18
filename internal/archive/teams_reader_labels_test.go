package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

func TestReaderLabelsReview(t *testing.T) {
	w := teams.Work{ConfigSHA256: "digest", Config: json.RawMessage(`{"requester":{"id":"a","display_name":"Requester"},"audiences":{"response_readers":[{"id":"a"},{"id":"b"}]},"publication_v2":{"descriptors":[{"readers":[{"id":"b"},{"id":"c"}]}]}}`)}
	labels := &teams.ReaderLabels{Version: 1, ConfigSHA256: "digest", Entries: []teams.ReaderLabel{{PersonID: "a", Status: "unknown"}, {PersonID: "b", Status: "active", DisplayName: "Alex Kim"}, {PersonID: "c", Status: "active", DisplayName: "Alex Kim"}}}
	now := time.Now()
	if !labelsForReview(w, labels, now) {
		t.Fatal("valid labels denied")
	}
	labels.Entries[2].PersonID = "b"
	if validReaderLabels(w, labels, now, false) {
		t.Fatal("duplicate accepted")
	}
	labels.Entries[2].PersonID = "c"
	labels.Entries[2].DisplayName = ""
	if labelsForReview(w, labels, now) {
		t.Fatal("missing name accepted")
	}
	if !validReaderLabels(w, labels, now, false) {
		t.Fatal("valid unavailable label denied")
	}
	labels.Entries[2].Status = "inactive"
	if !labelsForReview(w, labels, now) {
		t.Fatal("inactive reader should be visible")
	}
	labels.Entries[2].Status = "active"
	labels.Entries[2].DisplayName = "نام\u200cخانواده"
	if !labelsForReview(w, labels, now) {
		t.Fatal("legitimate joiner name rejected")
	}
	labels.Entries[2].DisplayName = "\u200c\u200d"
	if validReaderLabels(w, labels, now, false) {
		t.Fatal("invisible name accepted")
	}
	labels.Entries[2].DisplayName = "Alex Kim"
	bad := w
	bad.Config = json.RawMessage(strings.Replace(string(w.Config), "Requester", "\\u202eRequester", 1))
	if labelsForReview(bad, labels, now) {
		t.Fatal("unsafe frozen requester fallback accepted")
	}
	item := teamsWorkerItem{Work: w, State: "awaiting_review", Labels: labels, LabelsAt: now, LabelsScope: "scope"}
	token := teamsReaderLabelToken(item, "scope", 1)
	if token == "" || token == teamsReaderLabelToken(item, "scope", 2) {
		t.Fatal("display token not bound to generation")
	}
	item.Labels.Entries[1].DisplayName = "Different"
	if token == teamsReaderLabelToken(item, "scope", 1) {
		t.Fatal("display token did not change with name")
	}
	labels.ConfigSHA256 = "other"
	if validReaderLabels(w, labels, now, false) {
		t.Fatal("wrong hash accepted")
	}
}

func TestReaderLabelsClearPreservesWork(t *testing.T) {
	w := teamsWorkerState{generation: 5, scope: "old", items: map[string]teamsWorkerItem{"work": {State: "awaiting_review", Labels: &teams.ReaderLabels{Version: 1}, LabelsAt: time.Now(), LabelsScope: "old"}}}
	w.clearReaderLabelsLocked()
	item := w.items["work"]
	if w.generation != 6 || w.scope != "" || item.Labels != nil || item.State != "awaiting_review" {
		t.Fatal("failed check retained reader labels or changed ordinary work")
	}
}

func TestReaderLabelsClearedWhenCredentialCheckFails(t *testing.T) {
	c, cfg := testCatalog(t)
	if _, err := ensureLibraryDescriptor(cfg, "Synthetic"); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, cfg, c)
	server.teamsWorker.items = map[string]teamsWorkerItem{"work": {State: "awaiting_review", Labels: &teams.ReaderLabels{Version: 1}, LabelsAt: time.Now(), LabelsScope: "scope"}}
	server.teamsWorker.scope = "scope"
	server.teamsWorker.generation = 4
	override(t, &teamsLoadCredential, func(*Catalog, Config, LibraryDescriptor) (*teams.EnrollmentCredential, error) {
		return nil, errors.New("synthetic auth failure")
	})
	if _, err := server.checkTeamsWork(context.Background()); err == nil {
		t.Fatal("credential failure was not returned")
	}
	if len(server.teamsWorker.items) != 0 || server.teamsWorker.scope != "" || server.teamsWorker.generation != 5 {
		t.Fatal("credential failure left cached work actionable")
	}
}

func TestReaderLabelsMaximumAudience(t *testing.T) {
	type reader struct {
		ID string `json:"id"`
	}
	collection := make([]reader, 1000)
	report := make([]reader, 100)
	labels := &teams.ReaderLabels{Version: 1, ConfigSHA256: "hash"}
	for i := 0; i < 1100; i++ {
		id := fmt.Sprintf("%08x-0000-4000-8000-000000000001", i+1)
		if i < 1000 {
			collection[i] = reader{ID: id}
		} else {
			report[i-1000] = reader{ID: id}
		}
		labels.Entries = append(labels.Entries, teams.ReaderLabel{PersonID: id, Status: "active", DisplayName: strings.Repeat("<", 64)})
	}
	config, _ := json.Marshal(map[string]any{"requester": map[string]string{"id": collection[0].ID, "display_name": "Requester"}, "audiences": map[string]any{"response_readers": collection}, "publication_v2": map[string]any{"descriptors": []any{map[string]any{"readers": report}}}})
	w := teams.Work{ConfigSHA256: "hash", Config: config}
	if !labelsForReview(w, labels, time.Now()) {
		t.Fatal("largest supported audience rejected")
	}
	encoded, _ := json.Marshal(labels)
	if len(encoded) > 2<<20 {
		t.Fatalf("bounded labels exceed response ceiling: %d", len(encoded))
	}
	labels.Entries = labels.Entries[:1099]
	if validReaderLabels(w, labels, time.Now(), false) {
		t.Fatal("incomplete audience accepted")
	}
}

func TestTeamsWorkerSweepRetainsWaitingAndInvalidatesChangedWork(t *testing.T) {
	old := map[string]teamsWorkerItem{"a": {State: "awaiting_review", Payload: []byte("old-a")}, "b": {State: "awaiting_source_consent"}, "c": {State: "awaiting_review", Payload: []byte("old-c")}}
	oldKeys := map[string]string{"a": "a1", "b": "b1", "c": "c1", "gone": "g1"}
	keys := map[string]string{"a": "a2", "b": "b1", "c": "c1", "new": "n1"}
	ordered := []string{"a", "b", "c", "new"}
	items := map[string]teamsWorkerItem{}
	for _, id := range ordered {
		items[id] = teamsWorkerItem{State: "pending"}
	}
	sweep := teamsMergeWorkSweep([]string{"a", "c", "gone", "b"}, oldKeys, keys, oldKeys, keys, ordered, old, items)
	if got := strings.Join(sweep, ","); got != "c,b,a,new" {
		t.Fatalf("queue order %q", got)
	}
	if items["a"].State != "pending" || len(items["a"].Payload) != 0 {
		t.Fatal("changed binding retained review")
	}
	if items["b"].State != "awaiting_source_consent" || items["c"].State != "awaiting_review" {
		t.Fatal("unchanged review discarded")
	}
}

func TestTeamsWorkerSweepAfterFailedPoll(t *testing.T) {
	keys := map[string]string{"a": "1", "b": "2", "c": "3"}
	items := map[string]teamsWorkerItem{"a": {State: "pending"}, "b": {State: "pending"}, "c": {State: "pending"}}
	got := teamsMergeWorkSweep([]string{"c"}, keys, keys, keys, keys, []string{"a", "b", "c"}, nil, items)
	if strings.Join(got, ",") != "c,a,b" {
		t.Fatalf("lost unvisited turn after outage: %v", got)
	}
}

func TestTeamsWorkerSweepRefillsAfterWithdrawnSuffix(t *testing.T) {
	keys := map[string]string{"a": "a", "b": "b"}
	old := map[string]teamsWorkerItem{"a": {State: "awaiting_review"}, "b": {State: "awaiting_model_permission"}}
	items := map[string]teamsWorkerItem{"a": {State: "pending"}, "b": {State: "pending"}}
	got := teamsMergeWorkSweep([]string{"withdrawn"}, keys, keys, keys, keys, []string{"a", "b"}, old, items)
	if strings.Join(got, ",") != "a,b" {
		t.Fatalf("withdrawn suffix suppressed refill: %v", got)
	}
}

func TestTeamsWorkerSweepRotatesRunnableBehindWaiting(t *testing.T) {
	items := map[string]teamsWorkerItem{"run": {State: "source_safety_waiting"}, "done": {State: "awaiting_review"}}
	got := teamsAppendRunnable([]string{"waiting"}, []string{"run", "done"}, items, nil)
	if strings.Join(got, ",") != "waiting,run" {
		t.Fatalf("runnable turn lost: %v", got)
	}
}

func TestTeamsWorkerAuthorityAllowsOnlyCatalogCounterChanges(t *testing.T) {
	base := teamsLocalSettings{LibraryID: "library", InstanceID: "instance", OrgID: "org", InstallationID: "install", KeyFingerprint: "key", Enabled: true, SettingsRevision: 4, LocalRevision: 2, ObservedGeneration: 2, ObservedPrivacyRevision: 3}
	changed := base
	changed.LocalRevision++
	changed.ObservedGeneration++
	changed.ObservedPrivacyRevision++
	if !teamsSameWorkerAuthority(base, changed) {
		t.Fatal("ordinary catalog counters invalidated the visit")
	}
	for _, mutate := range []func(*teamsLocalSettings){func(s *teamsLocalSettings) { s.Enabled = false }, func(s *teamsLocalSettings) { s.SettingsRevision++ }, func(s *teamsLocalSettings) { s.InstanceID = "other" }, func(s *teamsLocalSettings) { s.OrgID = "other" }, func(s *teamsLocalSettings) { s.KeyFingerprint = "other" }} {
		changed = base
		mutate(&changed)
		if teamsSameWorkerAuthority(base, changed) {
			t.Fatal("changed consent or enrollment retained authority")
		}
	}
}
