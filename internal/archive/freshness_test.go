package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// freshnessFixture is a catalog with one source on this Mac and the ledger's
// meta in the state a library has once everything has been built today.
type freshnessFixture struct {
	t       *testing.T
	catalog *Catalog
	config  Config
}

func newFreshnessFixture(t *testing.T) *freshnessFixture {
	t.Helper()
	catalog, config := testCatalog(t)
	config.Sources = []SourceConfig{{Name: "claude", Kind: "claude", Enabled: true}}
	f := &freshnessFixture{t: t, catalog: catalog, config: config}
	f.exec(f.builtToday()...)
	return f
}

func (f *freshnessFixture) exec(statements ...string) {
	f.t.Helper()
	execTx(f.t, f.catalog.DB, statements...)
}

func setMetaSQL(key, value string) string {
	return fmt.Sprintf("INSERT INTO meta(key,value) VALUES(%s,%s) ON CONFLICT(key) DO UPDATE SET value=excluded.value", sqlQuote(key), sqlQuote(value))
}

func sqlQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

// builtToday is the meta of a library whose search indexes, Tools rollup,
// writing classification, identity links, Git merges and findings were all
// built for today and for its current inputs.
func (f *freshnessFixture) builtToday() []string {
	c := f.catalog
	return []string{
		setMetaSQL("message_trigram_version", "1"), setMetaSQL("text_search_documents_version", "1"), setMetaSQL("tool_command_fts_version", "1"),
		setMetaSQL("human_query_text_fts_ready", humanQueryText.version()), setMetaSQL("tool_query_text_fts_ready", toolQueryText.version()),
		setMetaSQL("tool_query_facts_ready", toolQueryFacts.version()), setMetaSQL("workspace_library_version", libraryVersion()),
		setMetaSQL("tool_ledger_generation", "7"), setMetaSQL("tool_rollup_generation", "7/"+toolRollupVersion),
		setMetaSQL("tool_rollup_day", c.clock().Format("2006-01-02")), setMetaSQL("tool_rollup_timezone", toolRollupTimezone()),
		setMetaSQL("tool_rollup_full_revision", "r1"), setMetaSQL("tool_rollup_built_full_revision", "r1"),
		setMetaSQL("authorship_inputs_generation", "3"), setMetaSQL("authorship_generation", "7:3/"+authorshipVersion), setMetaSQL("authorship_built_at", "2026-10-27T08:00:00Z"),
		setMetaSQL("findings_generation", c.findingsGeneration()), setMetaSQL("findings_built_at", "2026-10-27T08:05:00Z"),
		// Each completion was for the library's inputs as they are (7:3).
		setMetaSQL("findings_inputs_generation", "7:3"), setMetaSQL("analysis_inputs:identities", "7:3"), setMetaSQL("analysis_inputs:git", "7:3"),
		"DELETE FROM workspace_library_dirty", "DELETE FROM tool_rollup_dirty",
	}
}

// scheduled turns on scheduled indexing every 5 min for every source.
func (f *freshnessFixture) scheduled(enabled bool) {
	f.exec(fmt.Sprintf("INSERT INTO sync_settings(host_id,enabled,interval_seconds,sources_json) VALUES(%s,%d,300,'[]') ON CONFLICT(host_id) DO UPDATE SET enabled=excluded.enabled",
		sqlQuote(currentHost().ID), map[bool]int{true: 1}[enabled]))
}

// ledger computes the rows with in's optional parts.
func (f *freshnessFixture) ledger(in freshnessInput) map[string]freshnessRow {
	f.t.Helper()
	if in.sources == nil {
		in.sources = []string{"claude"}
	}
	rows := map[string]freshnessRow{}
	for _, row := range f.catalog.freshness(context.Background(), in) {
		if _, dup := rows[row.Domain]; dup {
			f.t.Fatalf("domain %s listed twice", row.Domain)
		}
		rows[row.Domain] = row
	}
	return rows
}

// expectRow checks one row: state, reason code ("" for none), and whether
// op catches it up.
func expectRow(t *testing.T, rows map[string]freshnessRow, domain, state, reason, catchingUp string) freshnessRow {
	t.Helper()
	row, ok := rows[domain]
	if !ok {
		t.Fatalf("no %s row in %v", domain, keysOf(rows))
	}
	code := ""
	if row.Reason != nil {
		code = row.Reason.Code
		if row.Reason.Text == "" {
			t.Fatalf("%s: reason %s has no words", domain, code)
		}
	}
	catching := ""
	if row.CatchingUp != nil {
		catching = *row.CatchingUp
		if row.CatchingUpLabel == nil || *row.CatchingUpLabel == "" {
			t.Fatalf("%s: the catching-up operation has no label", domain)
		}
	}
	if row.Behind.State != state || code != reason || catching != catchingUp {
		data, _ := json.Marshal(row)
		t.Fatalf("%s: want %s/%q/%q, got %s", domain, state, reason, catchingUp, data)
	}
	if row.StateLabel == "" || row.Label == "" || row.Group == "" {
		t.Fatalf("%s: rows carry the server's words: %+v", domain, row)
	}
	if state != freshCurrent && catchingUp == "" && row.Next == nil {
		t.Fatalf("%s: a row that is not current says what catches it up: %+v", domain, row)
	}
	return row
}

func keysOf[V any](values map[string]V) []string {
	keys := []string{}
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

// counted is a setup that leaves steps as the upgrade's last count, taken
// for inputs.
func counted(inputs string, steps ...upgradeStep) func(*freshnessFixture) {
	return func(f *freshnessFixture) {
		state := f.catalog.upgrade()
		state.mu.Lock()
		state.lastSteps, state.lastStepsAt, state.lastStepsInputs = steps, time.Now(), inputs
		state.mu.Unlock()
	}
}

func runningOp(id, kind, subject string) freshnessOp {
	row, _ := lookupOperationKind(kind)
	return freshnessOp{ID: id, Kind: kind, Subject: subject, Label: row.Label, State: opRunning, Affects: parseAffects(row.Affects), Started: time.Now()}
}

// Conversations (conv:<source>) are current only as of the last check: the
// scheduled index's success within its interval. Off schedule, after an
// error or an interruption, their state is unknown, not guessed; an index
// running on the source catches it up.
func TestFreshnessConversations(t *testing.T) {
	host := sqlQuote(currentHost().ID)
	at := func(ago time.Duration) string { return sqlQuote(formatTime(time.Now().Add(-ago))) }
	for _, test := range []struct {
		name            string
		schedule        bool
		seed            []string
		in              freshnessInput
		state, reason   string
		catching, next  string
		asOfFromSeeding bool
	}{
		{name: "current after a scheduled check", schedule: true,
			seed:  []string{"INSERT INTO automatic_source_states(host_id,source_name,attempted_at,succeeded_at,indexed_at) VALUES(" + host + ",'claude'," + at(time.Minute) + "," + at(time.Minute) + "," + at(time.Hour) + ")"},
			state: freshCurrent, asOfFromSeeding: true},
		{name: "never indexed", schedule: true, state: freshBehind, reason: "never-indexed", next: nextScheduledIndex},
		{name: "catching up while an index runs", schedule: true, in: freshnessInput{ops: []freshnessOp{runningOp("op-index", "index.changes.manual", "")}},
			state: freshBehind, reason: "indexing", catching: "op-index"},
		{name: "schedule off", schedule: false,
			seed:  []string{"INSERT INTO source_states(host_id,source_name,kind,capability,coverage,last_attempt_at,last_success_at,updated_at) VALUES(" + host + ",'claude','claude','full','complete'," + at(time.Hour) + "," + at(time.Hour) + "," + at(time.Hour) + ")"},
			state: freshUnknown, reason: "schedule-off", next: nextManualIndex, asOfFromSeeding: true},
		{name: "the latest attempt failed", schedule: true,
			seed: []string{"INSERT INTO source_states(host_id,source_name,kind,capability,coverage,last_attempt_at,last_success_at,updated_at) VALUES(" + host + ",'claude','claude','full','complete'," + at(2*time.Hour) + "," + at(2*time.Hour) + "," + at(2*time.Hour) + ")",
				"INSERT INTO automatic_source_states(host_id,source_name,attempted_at,error) VALUES(" + host + ",'claude'," + at(time.Minute) + ",'unreadable transcript')"},
			state: freshUnknown, reason: "source-error", next: nextScheduledIndex},
		{name: "an index on the source beats its earlier error", schedule: true,
			seed: []string{"INSERT INTO automatic_source_states(host_id,source_name,attempted_at,succeeded_at,error) VALUES(" + host + ",'claude'," + at(time.Minute) + "," + at(time.Hour) + ",'unreadable transcript')"},
			in:   freshnessInput{ops: []freshnessOp{runningOp("op-auto", "index.changes.automatic", "")}}, state: freshUnknown, reason: "indexing", catching: "op-auto"},
		{name: "interrupted", schedule: true,
			seed:  []string{"INSERT INTO automatic_source_states(host_id,source_name,attempted_at,succeeded_at,error) VALUES(" + host + ",'claude'," + at(time.Minute) + "," + at(time.Hour) + ",'interrupted: released')"},
			state: freshUnknown, reason: "interrupted", next: nextScheduledIndex},
		{name: "left to a running capture", schedule: true, in: freshnessInput{deferredToCapture: map[string]bool{"claude": true}},
			seed:  []string{"INSERT INTO automatic_source_states(host_id,source_name,attempted_at,succeeded_at) VALUES(" + host + ",'claude'," + at(time.Minute) + "," + at(time.Minute) + ")"},
			state: freshUnknown, reason: "capture-busy", next: nextScheduledIndex},
		{name: "the schedule has not checked for days", schedule: true,
			seed:  []string{"INSERT INTO automatic_source_states(host_id,source_name,attempted_at,succeeded_at) VALUES(" + host + ",'claude'," + at(48*time.Hour) + "," + at(48*time.Hour) + ")"},
			state: freshUnknown, reason: "overdue", next: nextScheduledIndex},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFreshnessFixture(t)
			f.scheduled(test.schedule)
			f.exec(test.seed...)
			row := expectRow(t, f.ledger(test.in), "conv:claude", test.state, test.reason, test.catching)
			if test.next != "" && (row.Next == nil || *row.Next != test.next) {
				t.Fatalf("next: want %q, got %v", test.next, row.Next)
			}
			if test.asOfFromSeeding && row.AsOf == nil {
				t.Fatalf("as_of is the latest success: %+v", row)
			}
		})
	}
	// An index running on a current source does not make it behind.
	f := newFreshnessFixture(t)
	f.scheduled(true)
	f.exec("INSERT INTO automatic_source_states(host_id,source_name,attempted_at,succeeded_at) VALUES(" + host + ",'claude'," + at(time.Minute) + "," + at(time.Minute) + ")")
	expectRow(t, f.ledger(freshnessInput{ops: []freshnessOp{runningOp("op-auto", "index.changes.automatic", "")}}), "conv:claude", freshCurrent, "", "")
}

// Every domain the catalog alone decides: current, behind with its reason
// and count, unknown when the input is missing, and caught up by the
// operation whose kind affects it.
func TestFreshnessDomains(t *testing.T) {
	failedJournal := func(kind string) string {
		return `INSERT INTO operation_journal(id,root_id,kind,trigger,host_id,process,pid,instance,state,started_at,updated_at,ended_at)
			VALUES('j-` + kind + `','j-` + kind + `','` + kind + `','chained',` + sqlQuote(currentHost().ID) + `,'service',1,'old','failed','2026-10-27T09:00:00Z','2026-10-27T09:00:00Z','2026-10-27T09:01:00Z')`
	}
	for _, test := range []struct {
		name, domain   string
		seed           []string
		setup          func(*freshnessFixture)
		in             freshnessInput
		state, reason  string
		catching       string
		count          int64
		countKnown     bool
		unit, asOf     string
		absentOrUnseen bool
	}{
		{name: "view current", domain: "view", state: freshCurrent},
		{name: "view behind", domain: "view", seed: []string{"INSERT INTO workspace_library_dirty(workspace_id) VALUES('w1'),('w2')"}, state: freshBehind, reason: "refreshing", count: 2, countKnown: true, unit: "workspace"},
		{name: "view stopped", domain: "view", seed: []string{"INSERT INTO workspace_library_dirty(workspace_id) VALUES('w1')"}, in: freshnessInput{viewStopped: true}, state: freshBehind, reason: "stopped", count: 1, countKnown: true},
		{name: "view catching up", domain: "view", seed: []string{"INSERT INTO workspace_library_dirty(workspace_id) VALUES('w1')"},
			in: freshnessInput{ops: []freshnessOp{runningOp("op-view", "maintenance.library-view", "")}}, state: freshBehind, reason: "refreshing", catching: "op-view", count: 1, countKnown: true},
		{name: "view version", domain: "view", seed: []string{setMetaSQL("workspace_library_version", "old")}, state: freshBehind, reason: "version"},

		{name: "text search current", domain: "search.text", state: freshCurrent},
		{name: "text search building", domain: "search.text", seed: []string{"DELETE FROM meta WHERE key='text_search_documents_version'"},
			in: freshnessInput{ops: []freshnessOp{runningOp("op-text", "maintenance.search.text", "")}}, state: freshBehind, reason: "building", catching: "op-text"},
		{name: "substring search counts what is left", domain: "search.substring", seed: []string{"DELETE FROM meta WHERE key='message_trigram_version'"}, state: freshBehind, reason: "version", countKnown: true, unit: "conversation"},
		{name: "table search on an older version", domain: "search.query", seed: []string{setMetaSQL("tool_query_facts_ready", "old")}, state: freshBehind, reason: "version"},

		{name: "identities current", domain: "an.identities", state: freshCurrent},
		{name: "identities deferred", domain: "an.identities", seed: []string{"INSERT INTO sync_deferred(name,generation,completed_generation,changed_at,completed_at) VALUES('identities',5,3,'2026-10-27T10:00:00Z','2026-10-27T07:00:00Z')"},
			state: freshBehind, reason: "deferred-by-schedule", asOf: "2026-10-27T07:00:00Z"},
		{name: "identities with no record for the current data", domain: "an.identities", seed: []string{"DELETE FROM meta WHERE key='analysis_inputs:identities'"}, state: freshUnknown, reason: "no-record"},
		{name: "identities after a write outside the chain", domain: "an.identities", seed: []string{setMetaSQL("authorship_inputs_generation", "4"), setMetaSQL("authorship_generation", "7:4/"+authorshipVersion)},
			state: freshBehind, reason: "changed"},
		{name: "identities caught up by the chain", domain: "an.identities", seed: []string{"INSERT INTO sync_deferred(name,generation,completed_generation,changed_at) VALUES('identities',1,0,'2026-10-27T10:00:00Z')"},
			in: freshnessInput{ops: []freshnessOp{runningOp("op-index", "index.changes.manual", ""), runningOp("op-chain", "analysis.chain", "")}}, state: freshBehind, reason: "running", catching: "op-chain"},
		{name: "git failed since", domain: "an.git", seed: []string{"INSERT INTO sync_deferred(name,generation,completed_generation,changed_at,completed_at) VALUES('git',1,1,'2026-10-27T06:00:00Z','2026-10-27T06:00:00Z')",
			setMetaSQL("analysis_inputs:git", "6:3"), failedJournal("analysis.git")}, state: freshBehind, reason: "failed"},
		{name: "git current for its inputs, whatever failed earlier", domain: "an.git", seed: []string{failedJournal("analysis.git")}, state: freshCurrent},
		{name: "tools rollup current", domain: "an.tools", state: freshCurrent},
		{name: "tools rollup changed", domain: "an.tools", seed: []string{"INSERT INTO tool_rollup_dirty(workspace_id,revision) VALUES('w1','r'),('w2','r'),('w3','r')"}, state: freshBehind, reason: "changed", count: 3, countKnown: true, unit: "workspace"},
		{name: "tools rollup of an older layout", domain: "an.tools", seed: []string{setMetaSQL("tool_rollup_generation", "7/old")}, state: freshBehind, reason: "version"},
		{name: "tools rollup never built", domain: "an.tools", seed: []string{"DELETE FROM meta WHERE key='tool_rollup_generation'"}, state: freshBehind, reason: "never-built"},
		{name: "writing classification behind", domain: "an.authorship", seed: []string{setMetaSQL("authorship_inputs_generation", "4")}, state: freshBehind, reason: "changed", asOf: "2026-10-27T08:00:00Z"},
		{name: "writing classification caught up", domain: "an.authorship", seed: []string{setMetaSQL("authorship_inputs_generation", "4")},
			in: freshnessInput{ops: []freshnessOp{runningOp("op-author", "analysis.authorship", "")}}, state: freshBehind, reason: "running", catching: "op-author"},
		{name: "tools ledger unknown before anything counted it", domain: "an.ledger", state: freshUnknown, reason: "not-counted"},
		{name: "tools ledger of an older version", domain: "an.ledger", setup: counted("7:3", upgradeStep{ID: "tools", Pending: 12}, upgradeStep{ID: "usage", Pending: 1}),
			state: freshBehind, reason: "version", count: 12, countKnown: true, unit: "conversation"},
		{name: "tools ledger counted before the library changed", domain: "an.ledger", setup: counted("6:3", upgradeStep{ID: "tools"}), state: freshUnknown, reason: "not-counted"},

		{name: "findings current", domain: "findings", state: freshCurrent, asOf: "2026-10-27T08:05:00Z"},
		{name: "findings daily pass due", domain: "findings", seed: []string{setMetaSQL("findings_generation", findingsVersion+"/2026-10-26")}, state: freshBehind, reason: "daily-pass-due"},
		{name: "findings never built", domain: "findings", seed: []string{"DELETE FROM meta WHERE key='findings_generation'"}, state: freshBehind, reason: "never-built"},
		// Within its daily cadence: scheduled, not behind.
		{name: "findings after a Terminal ingest", domain: "findings", seed: []string{setMetaSQL("authorship_inputs_generation", "4")}, state: freshScheduled, reason: "changed"},
		{name: "findings with no record of their inputs", domain: "findings", seed: []string{"DELETE FROM meta WHERE key='findings_inputs_generation'"}, state: freshUnknown, reason: "no-record"},
		{name: "findings caught up", domain: "findings", seed: []string{"DELETE FROM meta WHERE key='findings_generation'"},
			in: freshnessInput{ops: []freshnessOp{runningOp("op-find", "analysis.findings", "")}}, state: freshBehind, reason: "running", catching: "op-find"},

		{name: "upgrade unknown before anything counted it", domain: "upgrade", state: freshUnknown, reason: "not-counted"},
		{name: "upgrade current once counted", domain: "upgrade", setup: func(f *freshnessFixture) {
			if _, err := f.catalog.UpgradeSteps(context.Background()); err != nil {
				f.t.Fatal(err)
			}
		}, state: freshCurrent},
		{name: "upgrade available and running", domain: "upgrade", setup: counted("7:3", upgradeStep{ID: "tools", Pending: 12}, upgradeStep{ID: "usage", Pending: 1}, upgradeStep{ID: "harness"}), in: freshnessInput{ops: []freshnessOp{runningOp("op-up", "library.upgrade", "")}}, state: freshBehind, reason: "running", catching: "op-up", count: 2, countKnown: true, unit: "step"},

		{name: "local backup unknown without the history", domain: "backup.local", state: freshUnknown},
		{name: "no local backup yet", domain: "backup.local", in: freshnessInput{files: &freshnessFiles{backupKnown: true}}, state: freshUnknown, reason: "never"},
		{name: "a local backup has no schedule to be current against", domain: "backup.local", in: freshnessInput{files: &freshnessFiles{backupKnown: true, backupAt: "2026-10-20T10:00:00Z"}},
			state: freshUnknown, reason: "no-schedule", asOf: "2026-10-20T10:00:00Z"},
		{name: "local backup failed since", domain: "backup.local", in: freshnessInput{files: &freshnessFiles{backupKnown: true, backupAt: "2026-10-20T10:00:00Z"}, backupFailedAt: "2026-10-27T10:00:00Z"},
			state: freshBehind, reason: "failed", asOf: "2026-10-20T10:00:00Z"},
		{name: "local backup caught up", domain: "backup.local", in: freshnessInput{files: &freshnessFiles{backupKnown: true}, backupFailedAt: "2026-10-27T10:00:00Z",
			ops: []freshnessOp{runningOp("op-backup", "backup.local", "/Volumes/Backup")}}, state: freshBehind, reason: "running", catching: "op-backup"},

		{name: "captures waiting to be indexed", domain: "capt:other-mac/codex", in: freshnessInput{files: &freshnessFiles{captures: []freshnessCapture{
			{host: "other-mac", hostLabel: "Studio", source: "codex", needsIndex: true, reason: "new capture data"}}}}, state: freshBehind, reason: "not-indexed"},
		{name: "captures caught up by Index captures", domain: "capt:other-mac/codex", in: freshnessInput{files: &freshnessFiles{captures: []freshnessCapture{
			{host: "other-mac", source: "codex", needsIndex: true}}}, ops: []freshnessOp{runningOp("op-ic", "index.captures", "")}}, state: freshBehind, reason: "indexing", catching: "op-ic"},
		{name: "this Mac's captures while it captures", domain: "capt:" + currentHost().ID + "/claude", in: freshnessInput{files: &freshnessFiles{captures: []freshnessCapture{
			{host: currentHost().ID, source: "claude", needsIndex: true}}}, ops: []freshnessOp{runningOp("op-cap", "capture.this-mac", "")}}, state: freshBehind, reason: "capture-running", catching: "op-cap"},
		{name: "an unreadable capture folder", domain: "capt:*", in: freshnessInput{files: &freshnessFiles{capturesUnreadable: true}}, state: freshUnknown, reason: "unreadable"},
		{name: "the last captures read, while the folder cannot be read", domain: "capt:other-mac/codex", in: freshnessInput{files: &freshnessFiles{capturesUnreadable: true, captures: []freshnessCapture{
			{host: "other-mac", source: "codex", needsIndex: true}}}}, state: freshUnknown, reason: "unreadable"},
		{name: "captures indexed", domain: "capt:other-mac/codex", in: freshnessInput{files: &freshnessFiles{captures: []freshnessCapture{
			{host: "other-mac", source: "codex", indexedAt: "2026-10-27T09:00:00Z"}}}}, state: freshCurrent, asOf: "2026-10-27T09:00:00Z"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFreshnessFixture(t)
			f.exec(test.seed...)
			if test.setup != nil {
				test.setup(f)
			}
			row := expectRow(t, f.ledger(test.in), test.domain, test.state, test.reason, test.catching)
			if test.countKnown != (row.Behind.Count != nil) || test.count != 0 && *row.Behind.Count != test.count {
				t.Fatalf("count: want %d (known %v), got %+v", test.count, test.countKnown, row.Behind)
			}
			if test.unit != "" && (row.Behind.Unit == nil || *row.Behind.Unit != test.unit) {
				t.Fatalf("unit: want %s, got %+v", test.unit, row.Behind)
			}
			if test.asOf != "" && (row.AsOf == nil || *row.AsOf != test.asOf) {
				t.Fatalf("as_of: want %s, got %v", test.asOf, row.AsOf)
			}
		})
	}
}

// A cloud destination is measured against its schedule: current when its
// newest verified backup is within the cadence, and otherwise behind with
// the first reason in the contract's order.
func TestFreshnessCloudBackups(t *testing.T) {
	recent := formatTime(time.Now().Add(-2 * time.Hour))
	old := formatTime(time.Now().Add(-9 * 24 * time.Hour))
	run := func(id, state string, verified bool, at string) string {
		status, _ := json.Marshal(cloudRun{ID: id, DestinationID: "d1", Kind: "full", State: state, Verified: verified, StartedAt: at, UpdatedAt: at})
		return fmt.Sprintf("INSERT INTO cloud_backup_runs(id,destination_id,status_json,created_at) VALUES(%s,'d1',%s,%s)", sqlQuote(id), sqlQuote(string(status)), sqlQuote(at))
	}
	schedule := func(enabled bool, cadence, failures int) string {
		state, _ := json.Marshal(cloudSchedule{cloudScheduleSpec: cloudScheduleSpec{Cadence: cadence}, DestinationID: "d1", Enabled: enabled, Failures: failures, NextRunAt: "2026-10-28T03:00:00Z"})
		return fmt.Sprintf("INSERT INTO cloud_backup_schedules(destination_id,state_json) VALUES('d1',%s)", sqlQuote(string(state)))
	}
	destination := `INSERT INTO cloud_destinations(id,config_json,credential_json,imported_at) VALUES('d1','{"name":"Archive bucket"}','','2026-10-01T00:00:00Z')`
	for _, test := range []struct {
		name          string
		seed          []string
		in            freshnessInput
		state, reason string
		catching      string
	}{
		{name: "never", seed: []string{destination}, state: freshBehind, reason: "never"},
		{name: "current within its schedule", seed: []string{destination, run("r1", "complete", true, recent), schedule(true, 1, 0)}, state: freshCurrent},
		{name: "no schedule to be current against", seed: []string{destination, run("r1", "complete", true, recent)}, state: freshUnknown, reason: "no-schedule"},
		{name: "older than its schedule", seed: []string{destination, run("r1", "complete", true, old), schedule(true, 7, 0)}, state: freshBehind, reason: "overdue"},
		{name: "a failed schedule retries", seed: []string{destination, run("r1", "complete", true, old), schedule(true, 7, 2)}, state: freshBehind, reason: "retrying"},
		{name: "the newest backup failed", seed: []string{destination, run("r1", "complete", true, old), run("r2", "failed", false, recent)}, state: freshBehind, reason: "failed"},
		{name: "the newest backup is not verified", seed: []string{destination, run("r1", "complete", true, old), run("r2", "complete", false, recent)}, state: freshBehind, reason: "unverified"},
		{name: "a paused schedule", seed: []string{destination, run("r1", "complete", false, old), schedule(false, 7, 0)}, state: freshBehind, reason: "paused"},
		{name: "caught up by an upload to it", seed: []string{destination}, in: freshnessInput{ops: []freshnessOp{runningOp("op-other", "cloud.upload", "d2"), runningOp("op-up", "cloud.upload", "d1")}},
			state: freshBehind, reason: "running", catching: "op-up"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFreshnessFixture(t)
			f.exec(test.seed...)
			row := expectRow(t, f.ledger(test.in), "backup.cloud:d1", test.state, test.reason, test.catching)
			if !strings.Contains(row.Label, "Archive bucket") {
				t.Fatalf("the destination is named: %s", row.Label)
			}
		})
	}
}

// After a scheduled (automatic-only) index, every library-wide analysis and
// the findings are behind: scheduled indexing marks them deferred, and only
// the chain after a manual index completes them. The summary says so in the
// server's words. A stage that then fails outranks the deferral, and the
// stages after it say they wait for it. The chain brings them current.
func TestFreshnessAfterAutomaticIndexLeavesAnalysesBehind(t *testing.T) {
	f := newFreshnessFixture(t)
	server := testServer(t, f.config, f.catalog)
	analyses := []string{"an.identities", "an.git", "an.tools", "an.authorship"}
	before := f.ledger(freshnessInput{})
	for _, domain := range append(analyses, "findings") {
		expectRow(t, before, domain, freshCurrent, "", "")
	}
	// What an automatic index's write transaction does for each record it
	// writes (ingest.go).
	for range 2 {
		if err := f.catalog.writeTransaction(context.Background(), "test-deferred", markSyncDeferred); err != nil {
			t.Fatal(err)
		}
	}
	ledger := func() ([]freshnessRow, map[string]freshnessRow) {
		rows := server.freshness(context.Background(), server.ops.views(), false)
		byDomain := map[string]freshnessRow{}
		for _, row := range rows {
			byDomain[row.Domain] = row
		}
		return rows, byDomain
	}
	rows, byDomain := ledger()
	for _, domain := range append(analyses, "findings") {
		row := expectRow(t, byDomain, domain, freshBehind, "deferred-by-schedule", "")
		// The generations count written records, not runs: no count.
		if row.Next == nil || *row.Next != nextManualIndex || row.Behind.Count != nil {
			t.Fatalf("%s: behind, with no count, until the next manual index: %+v", domain, row)
		}
	}
	group := summaryGroup(t, summarizeFreshness(rows), "analyses")
	if group.State != freshBehind || group.Short != shortManual || group.Text != "behind, "+nextManualIndex || !group.Shown || !strings.Contains(group.Tooltip, "scheduled indexing skips analyses") {
		t.Fatalf("the summary says analyses wait for the next manual index: %+v", group)
	}

	// A manual index then fails at Git: Git is failed, the stages after it
	// are blocked by it, and identities (before it) stay deferred.
	failedAt := sqlQuote(formatTime(time.Now().Add(time.Minute)))
	f.exec(`INSERT INTO operation_journal(id,root_id,kind,trigger,host_id,process,pid,instance,state,started_at,updated_at,ended_at)
		VALUES('j-git','j-git','analysis.git','chained',` + sqlQuote(currentHost().ID) + `,'service',1,'old','failed',` + failedAt + `,` + failedAt + `,` + failedAt + `)`)
	rows, byDomain = ledger()
	expectRow(t, byDomain, "an.identities", freshBehind, "deferred-by-schedule", "")
	expectRow(t, byDomain, "an.git", freshBehind, "failed", "")
	for _, domain := range []string{"an.tools", "an.authorship", "findings"} {
		if row := expectRow(t, byDomain, domain, freshBehind, "blocked", ""); row.Reason.Detail != "Merges from Git" {
			t.Fatalf("%s says which stage it waits for: %+v", domain, row.Reason)
		}
	}
	if group := summaryGroup(t, summarizeFreshness(rows), "analyses"); group.Short != shortFailed {
		t.Fatalf("a failed stage makes the group failed: %+v", group)
	}

	// The chain a manual index runs completes them, for the current inputs.
	slowTest(t)
	if err := server.finishDeferredSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, byDomain = ledger()
	for _, domain := range append(analyses, "findings") {
		expectRow(t, byDomain, domain, freshCurrent, "", "")
	}
}

func summaryGroup(t *testing.T, summary freshnessSummary, id string) freshnessGroup {
	t.Helper()
	for _, group := range summary.Groups {
		if group.Group == id {
			return group
		}
	}
	t.Fatalf("no %s group in %+v", id, summary)
	return freshnessGroup{}
}

// The Data row lists only groups that are not current, each with a short
// state, and says "All current" otherwise; a group catches up only when
// every row that is not current is being caught up.
func TestFreshnessSummary(t *testing.T) {
	text := func(value string) *string { return &value }
	row := func(domain, group, state string, changes func(*freshnessRow)) freshnessRow {
		r := freshnessRow{Domain: domain, Group: group, Label: domain, Behind: freshnessBehind{State: state}, StateLabel: freshnessStateLabels[state]}
		if changes != nil {
			changes(&r)
		}
		return r
	}
	current := []freshnessRow{row("conv:a", "conversations", freshCurrent, func(r *freshnessRow) { r.AsOf = text("2026-10-27T10:00:00Z") }),
		row("conv:b", "conversations", freshCurrent, func(r *freshnessRow) { r.AsOf = text("2026-10-27T09:00:00Z") }), row("findings", "findings", freshCurrent, nil)}
	all := summarizeFreshness(current)
	if !all.AllCurrent || all.Text != "All current" || all.CheckedAt == nil || *all.CheckedAt != "2026-10-27T09:00:00Z" || slices.ContainsFunc(all.Groups, func(g freshnessGroup) bool { return g.Shown }) {
		t.Fatalf("all current, checked as of the least recent check: %+v", all)
	}
	caught := func(r *freshnessRow) {
		r.CatchingUp, r.Reason = text("op-1"), &freshnessReason{Code: "indexing", Text: "Indexing now"}
	}
	mixed := summarizeFreshness([]freshnessRow{
		row("conv:a", "conversations", freshBehind, caught),
		row("conv:b", "conversations", freshBehind, func(r *freshnessRow) {
			r.Reason, r.Next, r.AsOf = &freshnessReason{Code: "never-indexed", Text: "Not indexed yet"}, text(nextScheduledIndex), text("2026-10-26T10:00:00Z")
		}),
		row("an.tools", "analyses", freshBehind, caught),
		row("backup.local", "backups", freshUnknown, func(r *freshnessRow) {
			r.Reason, r.Next = &freshnessReason{Code: "no-schedule", Text: "words"}, text(nextBackup)
		}),
	})
	if mixed.AllCurrent || mixed.Text != "" {
		t.Fatalf("not all current: %+v", mixed)
	}
	if g := summaryGroup(t, mixed, "conversations"); g.Short != shortBehind || !g.ShortAge || g.Text != "behind, "+nextScheduledIndex {
		t.Fatalf("one source caught up and one not is behind, with the uncaught one's next: %+v", g)
	}
	if g := summaryGroup(t, mixed, "analyses"); g.Short != shortUpdating || g.Text != "catching up" {
		t.Fatalf("every row caught up is updating: %+v", g)
	}
	if g := summaryGroup(t, mixed, "backups"); g.Short != shortUnknown || g.Shown {
		t.Fatalf("backups feed no page: never on the Data row: %+v", g)
	}

	// Unscheduled backups and findings waiting for tomorrow's pass leave the
	// row at "All current", the findings muted.
	quiet := summarizeFreshness(append(slices.Clone(current[:2]),
		row("findings", "findings", freshScheduled, func(r *freshnessRow) { r.Reason, r.Next = &freshnessReason{Code: "changed", Text: "words"}, text(nextFindingsPass) }),
		row("backup.local", "backups", freshUnknown, func(r *freshnessRow) { r.Reason = &freshnessReason{Code: "no-schedule", Text: "words"} })))
	if g := summaryGroup(t, quiet, "findings"); !quiet.AllCurrent || g.Shown || !g.Muted || g.Short != "updates tomorrow" {
		t.Fatalf("scheduled findings are muted and all is current: %+v %+v", quiet, g)
	}
}

// The snapshot carries the ledger, and the server fills what the catalog
// cannot know: this Mac's enabled sources, the operations it runs, the
// backup history (cached, per configuration), and the sources the latest
// index left to a capture. The release path's fresh check reads no file.
func TestStatusSnapshotCarriesTheLedger(t *testing.T) {
	override(t, &statusCoalesce, 10*time.Millisecond)
	f := newFreshnessFixture(t)
	server := testServer(t, f.config, f.catalog)
	views := func() []opView { return server.ops.views() }
	find := func(rows []freshnessRow, domain string) map[string]freshnessRow {
		return map[string]freshnessRow{domain: func() freshnessRow {
			for _, row := range rows {
				if row.Domain == domain {
					return row
				}
			}
			t.Fatalf("no %s", domain)
			return freshnessRow{}
		}()}
	}
	// Nothing cached yet: the fresh path leaves the files unknown.
	expectRow(t, find(server.freshness(context.Background(), views(), true), "backup.local"), "backup.local", freshUnknown, "", "")
	expectRow(t, find(server.freshness(context.Background(), views(), false), "backup.local"), "backup.local", freshUnknown, "never", "")
	if err := recordBackup(f.config, backupRecord{FinishedAt: "2026-10-26T10:00:00Z", Destination: "/Volumes/B"}); err != nil {
		t.Fatal(err)
	}
	// Cached: the history is read again only after a while, or when an
	// operation that changes it starts or ends.
	expectRow(t, find(server.freshness(context.Background(), views(), false), "backup.local"), "backup.local", freshUnknown, "never", "")
	override(t, &freshnessFilesMinAge, 0)
	_, backup := server.begin(context.Background(), OperationSpec{Kind: "backup.local", Subject: "/Volumes/B", Trigger: triggerUser})
	// While it runs, it catches the domain up; the fresh path still takes
	// the cached (old) history.
	expectRow(t, find(server.freshness(context.Background(), views(), true), "backup.local"), "backup.local", freshUnknown, "running", string(backup.id))
	if cached := server.freshnessFiles(server.Config(), views(), true); cached == nil || cached.backupAt != "" {
		t.Fatalf("the fresh path read the history again: %+v", cached)
	}
	backup.Finish(nil)
	// Once it ends, the polled ledger reads the history again.
	local := expectRow(t, find(server.freshness(context.Background(), views(), false), "backup.local"), "backup.local", freshUnknown, "no-schedule", "")
	if local.AsOf == nil || *local.AsOf != "2026-10-26T10:00:00Z" {
		t.Fatalf("the backup's age is reported: %+v", local)
	}
	// Another configuration's files are not the cached ones.
	server.configMu.Lock()
	server.config.CaptureRoot = t.TempDir()
	server.configMu.Unlock()
	if server.freshnessFiles(server.Config(), views(), true) != nil {
		t.Fatal("the cache served another capture root's files")
	}

	server.runsMu.Lock()
	server.runs = append([]*SyncRun{{ID: "run-1", State: "complete", Results: []IngestResult{{Source: "claude", Deferred: true}}}}, server.runs...)
	server.runsMu.Unlock()
	f.scheduled(true)
	f.exec("INSERT INTO automatic_source_states(host_id,source_name,attempted_at,succeeded_at) VALUES(" + sqlQuote(currentHost().ID) + ",'claude'," + sqlQuote(now()) + "," + sqlQuote(now()) + ")")
	expectRow(t, find(server.freshness(context.Background(), views(), false), "conv:claude"), "conv:claude", freshUnknown, "capture-busy", "")

	_, index := server.begin(context.Background(), OperationSpec{Kind: "index.changes.manual", Trigger: triggerUser})
	defer index.Finish(nil)
	f.exec("INSERT INTO sync_deferred(name,generation,completed_generation,changed_at) VALUES('findings',1,0," + sqlQuote(now()) + ")")
	status := getStatus(t, server, "/api/status")
	rows := []freshnessRow{}
	data, _ := json.Marshal(status.raw["freshness"])
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	expectRow(t, find(rows, "findings"), "findings", freshBehind, "running", string(index.id))
	summary := freshnessSummary{}
	data, _ = json.Marshal(status.raw["freshness_summary"])
	if err := json.Unmarshal(data, &summary); err != nil || len(summary.Groups) == 0 || summary.Groups[0].Group != "conversations" {
		t.Fatalf("the summary starts with conversations: %s", data)
	}
}

// An unreadable capture folder keeps the captures it last read, unknown;
// one that does not exist and never had captures has none.
func TestFreshnessFilesKeepCapturesThroughAnUnreadableRoot(t *testing.T) {
	catalog, _ := testCatalog(t)
	missing := filepath.Join(t.TempDir(), "absent")
	if files := readFreshnessFiles(catalog, missing, nil, nil); files.capturesUnreadable || len(files.captures) != 0 {
		t.Fatalf("no captures folder, no captures: %+v", files)
	}
	previous := []freshnessCapture{{host: "other-mac", source: "codex", needsIndex: true}}
	if files := readFreshnessFiles(catalog, missing, nil, previous); !files.capturesUnreadable || len(files.captures) != 1 {
		t.Fatalf("a folder that went away keeps the last captures, unreadable: %+v", files)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if files := readFreshnessFiles(catalog, file, nil, nil); !files.capturesUnreadable {
		t.Fatalf("a capture root that cannot be read is unreadable: %+v", files)
	}
}

// The ledger must stay cheap: it runs on every snapshot. A catalog with a
// backlog in each dirty table is measured in milliseconds, with no lock
// probe.
func TestFreshnessIsCheap(t *testing.T) {
	f := newFreshnessFixture(t)
	statements := []string{}
	for index := range 2000 {
		statements = append(statements, fmt.Sprintf("INSERT INTO workspace_library_dirty(workspace_id) VALUES('w%d')", index),
			fmt.Sprintf("INSERT INTO tool_rollup_dirty(workspace_id,revision) VALUES('w%d','r')", index))
	}
	f.exec(statements...)
	server := testServer(t, f.config, f.catalog)
	server.freshness(context.Background(), nil, false) // warm the file cache
	scans, probes := lockScans.Load(), lockProbes.Load()
	const runs = 20
	started := time.Now()
	for range runs {
		server.freshness(context.Background(), server.ops.views(), false)
	}
	each := time.Since(started) / runs
	t.Logf("freshness: %v per computation", each)
	if lockScans.Load() != scans || lockProbes.Load() != probes {
		t.Fatal("the ledger probed a lock")
	}
	if each > 250*time.Millisecond {
		t.Fatalf("the ledger took %v per computation", each)
	}
}

// A characterization of the three older freshness rules, which stay as they
// are (no retirement in this series): where each disagrees with the ledger's
// conv:<source> rule, so a later phase can retire them knowingly.
//
//   - Catalog.Freshness (Settings' health) reads only source_states, which
//     scheduled indexing never writes: a source indexed a minute ago by the
//     schedule is "stale" after a day.
//   - MCP archiveSourceStatus counts scheduled checks, but calls a source
//     with no manual index "incomplete_or_unknown_coverage", and ignores
//     whether scheduled indexing is on.
//   - The menu's indexed_at is the last time an index wrote, not the last
//     check: a source checked a minute ago with nothing new shows its last
//     write, days old.
//
// The ledger is current in the first two cases and unknown (schedule off)
// in the third, which both older rules call current.
func TestFreshnessDivergesFromTheOlderRules(t *testing.T) {
	host := sqlQuote(currentHost().ID)
	minute, threeDays := sqlQuote(formatTime(time.Now().Add(-time.Minute))), sqlQuote(formatTime(time.Now().Add(-72*time.Hour)))
	for _, test := range []struct {
		name     string
		schedule bool
		seed     []string
		// What each rule says about this Mac's source today.
		catalogStatus, mcpStatus string
		menuIndexedAt            string
		ledger                   string
	}{
		{name: "the schedule indexed it a minute ago, the last manual index was days ago", schedule: true,
			seed: []string{"INSERT INTO source_states(host_id,source_name,kind,capability,coverage,last_attempt_at,last_success_at,updated_at) VALUES(" + host + ",'claude','claude','full','complete'," + threeDays + "," + threeDays + "," + threeDays + ")",
				"INSERT INTO automatic_source_states(host_id,source_name,attempted_at,succeeded_at,indexed_at) VALUES(" + host + ",'claude'," + minute + "," + minute + "," + minute + ")"},
			catalogStatus: "stale", mcpStatus: "current", menuIndexedAt: minute, ledger: freshCurrent},
		{name: "the schedule checked a minute ago and found nothing new; it last wrote days ago", schedule: true,
			seed:          []string{"INSERT INTO automatic_source_states(host_id,source_name,attempted_at,succeeded_at,indexed_at) VALUES(" + host + ",'claude'," + minute + "," + minute + "," + threeDays + ")"},
			catalogStatus: "stale", mcpStatus: "stale", menuIndexedAt: threeDays, ledger: freshCurrent},
		{name: "scheduled indexing is off; a manual index ran a minute ago", schedule: false,
			seed:          []string{"INSERT INTO source_states(host_id,source_name,kind,capability,coverage,last_attempt_at,last_success_at,updated_at) VALUES(" + host + ",'claude','claude','full','complete'," + minute + "," + minute + "," + minute + ")"},
			catalogStatus: "current", mcpStatus: "current", menuIndexedAt: minute, ledger: freshUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFreshnessFixture(t)
			f.scheduled(test.schedule)
			f.exec(test.seed...)
			server := testServer(t, f.config, f.catalog)
			if got := f.catalog.Freshness()["status"]; got != test.catalogStatus {
				t.Errorf("Catalog.Freshness: want %s, got %v", test.catalogStatus, got)
			}
			mcp, err := f.catalog.archiveSourceStatus()
			if err != nil {
				t.Fatal(err)
			}
			if mcp["status"] != test.mcpStatus {
				t.Errorf("archiveSourceStatus: want %s, got %v (%v)", test.mcpStatus, mcp["status"], mcp["items"])
			}
			menu, err := server.menuBarStatus(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if want := strings.Trim(test.menuIndexedAt, "'"); menu["indexed_at"] != want {
				t.Errorf("menu indexed_at: want %s, got %v", want, menu["indexed_at"])
			}
			row := f.ledger(freshnessInput{})["conv:claude"]
			if row.Behind.State != test.ledger {
				t.Errorf("ledger: want %s, got %+v", test.ledger, row)
			}
		})
	}
}

// Every reason code the ledger uses is one the contract's §5 lists.
func TestFreshnessReasonsAreTheContracts(t *testing.T) {
	data, err := os.ReadFile("../../docs/background-work.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	section := text[strings.Index(text, "## 5. Freshness domains"):strings.Index(text, "## 6. Journal")]
	for code := range freshnessReasons {
		if !strings.Contains(section, "`"+code+"`") {
			t.Errorf("reason %s is not in the contract's §5", code)
		}
	}
}

// A count that started before an upgrade ended does not overwrite what the
// upgrade left, and a count is kept with the inputs it was taken for.
func TestUpgradeCountKeepsTheNewerAnswer(t *testing.T) {
	catalog, _ := testCatalog(t)
	state := catalog.upgrade()
	if _, err := catalog.UpgradeSteps(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, inputs, ok := state.cachedSteps()
	if !ok || inputs != catalog.inputsGeneration(context.Background()) {
		t.Fatalf("the count is kept with its inputs: %q %v", inputs, ok)
	}
	state.mu.Lock()
	state.lastSteps, state.lastFinishedAt = []upgradeStep{{ID: "tools"}}, time.Now().Add(time.Hour)
	state.mu.Unlock()
	if _, err := catalog.UpgradeSteps(context.Background()); err != nil {
		t.Fatal(err)
	}
	if steps, _, _, _ := state.cachedSteps(); len(steps) != 1 {
		t.Fatalf("a count older than the upgrade's end replaced it: %+v", steps)
	}
}
