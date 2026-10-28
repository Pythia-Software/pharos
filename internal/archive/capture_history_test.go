package archive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func historyFixture(t *testing.T) (*Catalog, Config, string) {
	t.Helper()
	useHost(t, "host-a")
	c, config := testCatalog(t)
	writeSourceFile(t, config.Path, "# fixture library\n")
	if e := os.MkdirAll(config.CaptureRoot, 0700); e != nil {
		t.Fatal(e)
	}
	dest := filepath.Join(t.TempDir(), "rollback")
	stubBackupDrives(t, canonical(filepath.Dir(dest)), nil)
	return c, config, dest
}
func prepareHistoryFixture(t *testing.T, config Config, dest string) historyJob {
	t.Helper()
	p, e := planHistory(t.Context(), config, historyScope{})
	if e != nil {
		t.Fatal(e)
	}
	j, e := prepareHistory(t.Context(), config, p, dest, 128<<20, 128<<20)
	if e != nil {
		t.Fatal(e)
	}
	return j
}
func TestHistoryCatalogRecoveryOffline(t *testing.T) {
	c, config, dest := historyFixture(t)
	record := sessionCopy("/deleted/old/session.jsonl", "first", "second")
	record.Conversations[0].Messages[0].Text = "retained historical body"
	record.Conversations[0].Messages[1].RawText = `{"usage":{"input_tokens":72,"output_tokens":9}}`
	record.Conversations[0].Messages[1].Selected = false
	// A TL1 task's work-item identity differs from its containing workspace.
	// Multiple conversations can have distinct or absent work-item links.
	record.SourceID = "installation:task"
	record.WorkItems = []map[string]any{{"source_id": "task", "title": "Task"}, {"source_id": "followup", "title": "Follow-up"}}
	for _, name := range []string{"other-task", "unassigned"} {
		conversation := record.Conversations[0]
		conversation.NativeID = name
		conversation.Messages = []MessageRecord{{NativeID: name, Role: "user", Kind: "message", Text: name, Selected: true}}
		record.Conversations = append(record.Conversations, conversation)
	}
	lateCheckout := filepath.Join(t.TempDir(), "post-cutoff-checkout")
	record.Repository = map[string]any{
		"id": "retained-repo", "canonical_remote": "https://github.com/fixture/current.git", "display_name": "history",
		"aliases":         []string{"https://github.com/fixture/previous.git", "original-name"},
		"local_locations": []string{lateCheckout, "/other-host/checkout-two"},
	}
	fixture := newCopyFixture(t, record)
	if r := c.Ingest(fixture, nil); r.Error != nil {
		t.Fatal(r)
	}
	// Divergent host copies retain overlapping positions; these are source
	// observations rather than a single newly numbered transcript.
	divergent := sessionCopy("/other-host/session.jsonl", "first", "third")
	divergent.SourceID = record.SourceID
	divergent.WorkItems = record.WorkItems
	divergent.Conversations[0].Messages[0].Text = record.Conversations[0].Messages[0].Text
	ingestAs(t, c, "host-b", newCopyFixture(t, divergent))
	tx := seedTx(t, c.DB)
	if _, err := tx.Exec("UPDATE conversations SET work_item_id=? WHERE native_id='session'", stableID("work", "claude", "local", "task")); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("UPDATE conversations SET work_item_id=NULL WHERE native_id='unassigned'"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE messages SET source_order=CASE native_id WHEN 'other-task' THEN 37 ELSE NULL END
		WHERE native_id IN ('other-task','unassigned')`); err != nil {
		t.Fatal(err)
	}
	// Retained PRs keep their repository after the workspace moves elsewhere.
	// Two older PRs share a dependency; an unlinked PR must stay out of scope.
	if _, err := tx.Exec(`INSERT INTO repositories(id,display_name,aliases_json,local_locations_json,created_at,updated_at) VALUES
		('older-pr-repo','Original repository','["original-alias"]','["/deleted/original-checkout"]','2026-01-01','2026-01-01'),
		('unrelated-repo','Unrelated repository','[]','[]','2026-01-01','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	workspaceID := stableID("workspace", record.SourceKind, record.Account, record.SourceID)
	for _, dependency := range []struct {
		repository string
		prs        []map[string]any
	}{
		{"older-pr-repo", []map[string]any{{"number": 7, "title": "Original PR"}, {"number": 8, "title": "Another original PR"}}},
		{"retained-repo", []map[string]any{{"number": 7, "title": "Current PR"}}},
		{"", []map[string]any{{"number": 7, "title": "Unassociated repository"}}},
	} {
		if err := upsertPRs(tx, workspaceID, dependency.repository, dependency.prs); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO pull_requests(id,host,repository_id,number) VALUES('unlinked-pr','github.com','unrelated-repo',9)`); err != nil {
		t.Fatal(err)
	}
	commitSeed(t, tx)
	expectedRepositories, err := queryMaps(c.DB, "SELECT * FROM repositories WHERE id<>'unrelated-repo' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	expectedPRs, err := queryMaps(c.DB, "SELECT * FROM pull_requests WHERE id<>'unlinked-pr' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	expectedPRLinks, err := queryMaps(c.DB, "SELECT * FROM work_pr_links ORDER BY pr_id,relationship")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("no-current-repository", func(t *testing.T) {
		tx := seedTx(t, c.DB)
		if _, err := tx.Exec("UPDATE workspaces SET repository_id=NULL WHERE id=?", workspaceID); err != nil {
			t.Fatal(err)
		}
		envelope, err := exportHistoryRecovery(t.Context(), tx, historyUnit{Kind: "catalog-workspace", Original: workspaceID}, "")
		tx.Rollback()
		if err != nil {
			t.Fatal(err)
		}
		if envelope.Record.Repository != nil {
			t.Fatal("PR dependency invented a current workspace repository", envelope.Record.Repository)
		}
		fresh, _ := testCatalog(t)
		if outcome, err := fresh.applyHistoryRecovery(t.Context(), envelope); err != nil || outcome != "catalog-derived" {
			t.Fatal(outcome, err)
		}
		var repository any
		if err := fresh.DB.QueryRow("SELECT repository_id FROM workspaces WHERE id=?", workspaceID).Scan(&repository); err != nil || repository != nil {
			t.Fatal("recovery invented a current workspace repository", repository, err)
		}
		prs, err := queryMaps(fresh.DB, "SELECT * FROM pull_requests ORDER BY id")
		if err != nil || jsonText(prs) != jsonText(expectedPRs) {
			t.Fatal("PR repository dependencies were not restored", prs, err)
		}
	})
	expectedPositions, err := queryMaps(c.DB, "SELECT id,source_order FROM messages ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	expectedWork, err := queryMaps(c.DB, "SELECT * FROM work_items ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	expectedWorkLinks, err := queryMaps(c.DB, "SELECT id,work_item_id FROM conversations ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	var expectedAliases, expectedLocations string
	if err := c.DB.QueryRow("SELECT aliases_json,local_locations_json FROM repositories WHERE id='retained-repo'").Scan(&expectedAliases, &expectedLocations); err != nil {
		t.Fatal(err)
	}
	// Application secrets and derived caches must not enter recovery envelopes.
	execTx(t, c.DB, `INSERT INTO ui_preferences(key,value) VALUES('private-test','"credential-must-stay-out"')`)
	j := prepareHistoryFixture(t, config, dest)
	j, e := runHistory(t.Context(), config, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	if j.State != "exported" {
		t.Fatal(j.State)
	}
	receipts, v, e := j.inventoryReceipts(t.Context())
	if e != nil || v.CatalogDerived != 1 {
		t.Fatalf("%+v %v", v, e)
	}
	for _, r := range receipts {
		if r.Recovery != "" {
			bytes, e := os.ReadFile(historyRecoveryPath(config.CaptureRoot, r.Recovery))
			if e != nil || strings.Contains(string(bytes), "credential-must-stay-out") {
				t.Fatal("recovery leaked application state", e)
			}
		}
	}
	// Close and remove the live catalog before rebuilding. Only the independent
	// rollback snapshot is opened later as a comparison oracle.
	c.Close()
	if e = os.Remove(config.CatalogPath); e != nil {
		t.Fatal(e)
	}
	// A checkout created after publication must not fill the unknown root commit
	// while restoring its recorded repository locations.
	if e = os.MkdirAll(lateCheckout, 0700); e != nil {
		t.Fatal(e)
	}
	gitBin, gitCalls := t.TempDir(), filepath.Join(t.TempDir(), "git-calls")
	if e = os.WriteFile(filepath.Join(gitBin, "git"), []byte(`#!/bin/sh
printf '%s\n' called >> "$HISTORY_GIT_CALLS"
printf '%040d\n' 1
`), 0700); e != nil {
		t.Fatal(e)
	}

	t.Setenv("HISTORY_GIT_CALLS", gitCalls)
	t.Setenv("PATH", gitBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	scratch := filepath.Join(t.TempDir(), "scratch.sqlite3")
	report, e := verifyHistory(t.Context(), config, j.ID, scratch)
	if e != nil {
		t.Fatal(e)
	}
	if report.CatalogOnlyApplied != 1 {
		t.Fatalf("%+v", report)
	}
	rebuilt := reopenCatalog(t, scratch)
	for _, observation := range []struct {
		table    string
		order    string
		expected []map[string]any
	}{{"repositories", "id", expectedRepositories}, {"pull_requests", "id", expectedPRs}, {"work_pr_links", "pr_id,relationship", expectedPRLinks}} {
		actual, err := queryMaps(rebuilt.DB, "SELECT * FROM "+observation.table+" ORDER BY "+observation.order)
		if err != nil || jsonText(actual) != jsonText(observation.expected) {
			t.Fatalf("recovery changed %s: got %s want %s: %v", observation.table, jsonText(actual), jsonText(observation.expected), err)
		}
	}
	for _, receipt := range receipts {
		if receipt.Recovery == "" {
			continue
		}
		var envelope historyRecovery
		if err := historyReadJSON(historyRecoveryPath(config.CaptureRoot, receipt.Recovery), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Kind == "catalog-workspace" && firstString(envelope.Record.Repository["id"]) != "retained-repo" {
			t.Fatal("historical PR repository replaced the core workspace repository", envelope.Record.Repository)
		}
	}
	actualPositions, err := queryMaps(rebuilt.DB, "SELECT id,source_order FROM messages ORDER BY id")
	if err != nil || jsonText(actualPositions) != jsonText(expectedPositions) {
		t.Fatalf("recovery renumbered recorded positions: got %s want %s: %v", jsonText(actualPositions), jsonText(expectedPositions), err)
	}
	if strings.Contains(jsonText(report.Differences), "messages differs") {
		t.Fatalf("message observations changed during offline rebuild: %+v", report)
	}
	if _, err := os.Stat(gitCalls); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("offline repository restoration queried local Git", err)
	}
	actualWork, err := queryMaps(rebuilt.DB, "SELECT * FROM work_items ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	actualWorkLinks, err := queryMaps(rebuilt.DB, "SELECT id,work_item_id FROM conversations ORDER BY id")
	if err != nil || jsonText(actualWork) != jsonText(expectedWork) || jsonText(actualWorkLinks) != jsonText(expectedWorkLinks) {
		t.Fatalf("work items/associations changed: work=%s links=%s want work=%s links=%s: %v", jsonText(actualWork), jsonText(actualWorkLinks), jsonText(expectedWork), jsonText(expectedWorkLinks), err)
	}
	var aliases, locations string
	if err := rebuilt.DB.QueryRow("SELECT aliases_json,local_locations_json FROM repositories WHERE id='retained-repo'").Scan(&aliases, &locations); err != nil || aliases != expectedAliases || locations != expectedLocations {
		t.Fatalf("repository evidence lost: aliases=%s locations=%s: %v", aliases, locations, err)
	}
	// The comparison must detect a later loss even if workspace/message counts
	// still match. Rebuilding from scratch above must keep the original values.
	execTx(t, rebuilt.DB, "UPDATE repositories SET aliases_json='[]',local_locations_json='[]' WHERE id='retained-repo'")
	oracle, err := openReadOnlySQLite(j.Oracle)
	if err != nil {
		t.Fatal(err)
	}
	defer oracle.Close()
	var comparison historyVerification
	for _, u := range j.Plan.Units {
		if u.Kind == "catalog-workspace" {
			if err = compareHistoryWorkspace(t.Context(), oracle, rebuilt.DB, u, &comparison); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !strings.Contains(jsonText(comparison.Differences), "repositories differs") {
		t.Fatalf("comparison certified missing repository metadata: %+v", comparison)
	}
	execTx(t, rebuilt.DB, "UPDATE conversations SET work_item_id=NULL WHERE native_id='session'")
	comparison = historyVerification{}
	for _, u := range j.Plan.Units {
		if u.Kind == "catalog-workspace" {
			if err = compareHistoryWorkspace(t.Context(), oracle, rebuilt.DB, u, &comparison); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !strings.Contains(jsonText(comparison.Differences), "conversations differs") {
		t.Fatalf("comparison certified a lost work-item association: %+v", comparison)
	}
	var body string
	var selected int
	if e = rebuilt.DB.QueryRow("SELECT text,selected FROM messages WHERE native_id='first'").Scan(&body, &selected); e != nil || body != "retained historical body" {
		t.Fatal(body, e)
	}
	result, e := rebuilt.Search(SearchOptions{Query: "historical", Limit: 10})
	if e != nil || len(result["items"].([]map[string]any)) == 0 {
		t.Fatal("public search failed after rescue", result, e)
	}
	var conversationID string
	if e = rebuilt.DB.QueryRow("SELECT id FROM conversations WHERE native_id='session'").Scan(&conversationID); e != nil {
		t.Fatal(e)
	}
	messages, e := rebuilt.conversationMessages(map[string]any{"conversation_id": conversationID, "limit": 12})
	if e != nil || !strings.Contains(jsonText(messages), "retained historical body") {
		t.Fatal("public conversation reader lost rescued text", messages, e)
	}
	var coverage string
	if e = rebuilt.DB.QueryRow("SELECT preservation_completeness FROM workspaces").Scan(&coverage); e != nil || coverage != "catalog-derived" {
		t.Fatal(coverage, e)
	}
}
func TestHistoryPlanPaginationAndStaleInputs(t *testing.T) {
	c, config, dest := historyFixture(t)
	for _, id := range []string{"one", "two", "three"} {
		r := sessionCopy("/deleted/"+id+".jsonl", id)
		r.SourceID = id
		r.Conversations[0].NativeID = id
		if result := c.Ingest(newCopyFixture(t, r), nil); result.Error != nil {
			t.Fatal(result)
		}
	}
	p, e := planHistory(t.Context(), config, historyScope{})
	if e != nil {
		t.Fatal(e)
	}
	page, e := historyPage(p, "", 1)
	if e != nil || len(page.Units) != 1 || page.Next == "" {
		t.Fatal(page, e)
	}
	next, e := historyPage(p, page.Next, 1)
	if e != nil || len(next.Units) != 1 || next.Units[0].ID == page.Units[0].ID {
		t.Fatal(next, e)
	}
	execTx(t, c.DB, `UPDATE messages SET text='changed' WHERE native_id='one'`)
	if _, e = prepareHistory(t.Context(), config, p, dest, 128<<20, 128<<20); e == nil {
		t.Fatal("stale catalog accepted")
	}
	if _, e = os.Stat(filepath.Join(config.CaptureRoot, historyDir, "jobs", p.ID)); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("stale dry run wrote job state", e)
	}
}
func TestHistoryPublicationResumesAndKeepsPins(t *testing.T) {
	for _, point := range []string{"evidence", "publication", "receipt", "cursor"} {
		t.Run(point, func(t *testing.T) {
			c, config, dest := historyFixture(t)
			if r := c.Ingest(newCopyFixture(t, sessionCopy("/deleted/a.jsonl", "m")), nil); r.Error != nil {
				t.Fatal(r)
			}
			j := prepareHistoryFixture(t, config, dest)
			old := captureFault
			captureFault = func(phase, path string) error {
				if phase == "history-"+point {
					return errors.New("injected interruption")
				}
				return nil
			}
			t.Cleanup(func() { captureFault = old })
			_, e := runHistory(t.Context(), config, j.ID)
			if e == nil {
				t.Fatal("fault missed")
			}
			captureFault = old
			j, e = runHistory(t.Context(), config, j.ID)
			if e != nil {
				t.Fatal(e)
			}
			entries, e := os.ReadDir(filepath.Join(config.CaptureRoot, historyDir, "objects"))
			payloads := 0
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".json") {
					payloads++
				}
			}
			if e != nil || payloads != 1 {
				t.Fatal("duplicate payload publication", payloads, e)
			}
			if _, _, e = j.inventoryReceipts(t.Context()); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestHistoryNativeSnapshotsDeletedSessionsAndPruning(t *testing.T) {
	c, config, dest := historyFixture(t)
	dir := filepath.Join(t.TempDir(), "conductor")
	exec := conductorFixture(t, dir)
	exec(addConductorSession("deleted", 2)...)
	exec(addConductorSession("survivor", 2)...)
	config.Sources = []SourceConfig{{Name: "conductor", Kind: "conductor", Path: dir, Account: "local", Enabled: true}}
	if run := runCapture(t, config); !run.OK {
		t.Fatal(run)
	}
	exec(`DELETE FROM session_messages WHERE session_id='deleted'`, `DELETE FROM sessions WHERE id='deleted'`, `DELETE FROM session_messages WHERE session_id='survivor' AND id='survivor-m1'`)
	if run := runCapture(t, config); !run.OK {
		t.Fatal(run)
	}
	// The deleted session survives only in a generation stored compressed:
	// planning, pins, the rollback backup's check and the export all read its
	// original bytes.
	if done := compressGenerations(t, config); len(done) != 1 {
		t.Fatal("generation was not compressed", done)
	}
	encoded := readCaptureManifest(t, config, "conductor").Snapshots[0].Generations[0]
	j := prepareHistoryFixture(t, config, dest)
	pinned := false
	for _, pin := range j.Protected {
		if pin.Path == encoded.Captured {
			pinned = pin.Encoding == captureEncodingZstd && pin.SHA256 == encoded.SHA256 && pin.Size == encoded.Size && pin.ClosureBytes == encoded.StoredSize
		}
	}
	if !pinned {
		t.Fatal("compressed generation was not pinned by its original bytes", j.Protected)
	}
	if done := compressGenerations(t, config); len(done) != 0 {
		t.Fatal("compression ran during a backfill's native pause", done)
	}
	// The native-only gate survives a restart and applies to manual capture.
	c.setCaptureRoot(config.CaptureRoot)
	if status := c.inputPreservationStatus(t.Context(), "host-a", "conductor"); status["pending_newer_inputs"] != true {
		t.Fatal("history pause absent from status", status)
	}
	loaded, e := loadHistoryJob(config.CaptureRoot, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	if run := runCapture(t, config); run.OK || captureResult(t, run, "conductor").State != "pending" {
		t.Fatal("manual capture bypassed native pause", run)
	}
	oldFault := captureFault
	captureFault = func(phase, path string) error {
		if phase == "history-publication" {
			return errors.New("native publication interrupted")
		}
		return nil
	}
	t.Cleanup(func() { captureFault = oldFault })
	if _, e = runHistory(t.Context(), config, loaded.ID); e == nil {
		t.Fatal("native interruption missed")
	}
	captureFault = oldFault
	if e = os.RemoveAll(dir); e != nil {
		t.Fatal(e)
	}
	j, e = runHistory(t.Context(), config, loaded.ID)
	if e != nil {
		t.Fatal(e)
	}
	scratch := filepath.Join(t.TempDir(), "scratch.sqlite3")
	report, e := verifyHistory(t.Context(), config, j.ID, scratch)
	if e != nil {
		t.Fatal(e)
	}
	rebuilt := reopenCatalog(t, scratch)
	if n := conversationMessages(t, rebuilt, "conductor:deleted"); n != 2 {
		t.Fatal("deleted session lost", n, report)
	}
	if n := conversationMessages(t, rebuilt, "conductor:survivor"); n != 1 {
		t.Fatal("historical messages reappeared", n, report)
	}
	// Live catalog is an untouched empty oracle; source replay can add archived
	// sessions that were never indexed, and reports do not equate this to loss.
	var n int
	_ = c.DB.QueryRow("SELECT COUNT(*) FROM workspaces").Scan(&n)
	if n != 0 {
		t.Fatal("export rewrote live catalog")
	}
	if status := c.inputPreservationStatus(t.Context(), "host-a", "conductor"); status["pending_newer_inputs"] != false {
		t.Fatal("completed history job left source pending", status)
	}
}
func TestHistoryDegradedCannotReplaceNative(t *testing.T) {
	c, config, dest := historyFixture(t)
	record := sessionCopy("/deleted/a.jsonl", "m")
	record.Conversations[0].Messages[0].Text = "fallback"
	if r := c.Ingest(newCopyFixture(t, record), nil); r.Error != nil {
		t.Fatal(r)
	}
	j := prepareHistoryFixture(t, config, dest)
	j, e := runHistory(t.Context(), config, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	receipts, _, e := j.inventoryReceipts(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	native, _ := testCatalog(t)
	record.Conversations[0].Messages[0].Text = "richer native"
	if r := native.Ingest(newCopyFixture(t, record), nil); r.Error != nil {
		t.Fatal(r)
	}
	for _, r := range receipts {
		if r.Recovery != "" {
			var envelope historyRecovery
			if e = historyReadJSON(historyRecoveryPath(config.CaptureRoot, r.Recovery), &envelope); e != nil {
				t.Fatal(e)
			}
			outcome, e := native.applyHistoryRecovery(t.Context(), envelope)
			if e != nil || outcome != "native-or-existing-kept" {
				t.Fatal(outcome, e)
			}
		}
	}
	var text string
	_ = native.DB.QueryRow("SELECT text FROM messages WHERE native_id='m'").Scan(&text)
	if text != "richer native" {
		t.Fatal(text)
	}
}
func TestHistoryCancellationAndBudgets(t *testing.T) {
	c, config, dest := historyFixture(t)
	if r := c.Ingest(newCopyFixture(t, sessionCopy("/deleted/a.jsonl", "m")), nil); r.Error != nil {
		t.Fatal(r)
	}
	p, e := planHistory(t.Context(), config, historyScope{})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = prepareHistory(t.Context(), config, p, dest, 0, 1<<20); e == nil {
		t.Fatal("unknown output budget accepted")
	}
	j := prepareHistoryFixture(t, config, dest)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e = runHistory(ctx, config, j.ID); e == nil {
		t.Fatal("cancelled job advanced")
	}
	j, e = runHistory(t.Context(), config, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	// An independently altered rollback file cannot silently become an oracle.
	if e = os.WriteFile(j.Oracle, []byte("damaged"), 0600); e != nil {
		t.Fatal(e)
	}
	_, e = verifyHistory(t.Context(), config, j.ID, filepath.Join(t.TempDir(), "scratch.sqlite3"))
	if e == nil || !strings.Contains(fmt.Sprint(e), "oracle") {
		t.Fatal(e)
	}
}

func TestHistoryTL1StructuredOnlyAndOfflineRebuild(t *testing.T) {
	c, config, dest := historyFixture(t)
	ingestTL1Fixture(t, c, 0)
	var tasks int
	if e := c.DB.QueryRow("SELECT COUNT(*) FROM tl1_tasks").Scan(&tasks); e != nil || tasks == 0 {
		t.Fatal(tasks, e)
	}
	j := prepareHistoryFixture(t, config, dest)
	j, e := runHistory(t.Context(), config, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	c.Close()
	if e = os.RemoveAll(dest); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(config.CatalogPath); e != nil {
		t.Fatal(e)
	}
	_, rebuilt, e := j.rebuild(t.Context(), config, filepath.Join(t.TempDir(), "offline.sqlite3"))
	if e != nil {
		t.Fatal(e)
	}
	defer rebuilt.Close()
	var got int
	if e = rebuilt.DB.QueryRow("SELECT COUNT(*) FROM tl1_tasks").Scan(&got); e != nil || got != tasks {
		t.Fatal("structured catalog-only tasks lost", got, tasks, e)
	}
	var events int
	if e = rebuilt.DB.QueryRow("SELECT COUNT(*) FROM tl1_events").Scan(&events); e != nil || events == 0 {
		t.Fatal("TL1 events lost", events, e)
	}
}

func TestHistoryBackfilledEvidenceTravelsInCloudArchive(t *testing.T) {
	slowTest(t)
	c, config, dest := historyFixture(t)
	if r := c.Ingest(newCopyFixture(t, sessionCopy("/gone/session.jsonl", "m")), nil); r.Error != nil {
		t.Fatal(r)
	}
	j := prepareHistoryFixture(t, config, dest)
	j, e := runHistory(t.Context(), config, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	_, _, destination, run, _ := cloudFixture(t, false)
	if e = os.MkdirAll(config.StagingRoot, 0700); e != nil {
		t.Fatal(e)
	}
	stage, e := cloudWorkDir(config, "cloud-backfill-test-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(stage)
	run.Kind = "full"
	archive, _, e := buildCloudArchive(t.Context(), destination, &run, config, stage, func(string) {})
	if e != nil {
		t.Fatal(e)
	}
	f, e := os.Open(archive)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	payload, e := cloudPayload(f, nil, "")
	if e != nil {
		t.Fatal(e)
	}
	restored, e := extractCloudArchive(t.Context(), config, payload, nil)
	if e != nil {
		t.Fatal(e)
	}
	recovered := config
	recovered.CaptureRoot = filepath.Join(restored, "captures")
	moved, e := loadHistoryJob(recovered.CaptureRoot, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	// No use of the downloaded catalog or original backup/source paths.
	_, rebuilt, e := moved.rebuild(t.Context(), recovered, filepath.Join(t.TempDir(), "recovered.sqlite3"))
	if e != nil {
		t.Fatal(e)
	}
	defer rebuilt.Close()
	var messages int
	if e = rebuilt.DB.QueryRow("SELECT COUNT(*) FROM messages").Scan(&messages); e != nil || messages == 0 {
		t.Fatal("archive omitted committed recovery envelopes", messages, e)
	}
}

func TestHistoryKeepsHostAccountAndLineage(t *testing.T) {
	c, config, dest := historyFixture(t)
	for _, host := range []string{"host-a", "host-b"} {
		useHost(t, host)
		record := sessionCopy("/same/original/path.jsonl", "m")
		record.Account = host + "-account"
		record.Conversations[0].Account = record.Account
		child := record.Conversations[0]
		child.NativeID = "child"
		child.ParentNativeID = record.Conversations[0].NativeID
		child.Messages = []MessageRecord{{NativeID: "child-message", Role: "assistant", Text: "child history", Selected: true}}
		record.Conversations = append(record.Conversations, child)
		if r := c.Ingest(newCopyFixture(t, record), nil); r.Error != nil {
			t.Fatal(r)
		}
		// A separate workspace can belong to a parent conversation in another
		// workspace, with colliding native IDs under the other host's account.
		external := sessionCopy("/same/original/external.jsonl", "external-message")
		external.SourceID, external.Account = "external", record.Account
		external.Conversations[0].NativeID = "external-child"
		external.Conversations[0].Account = record.Account
		external.Conversations[0].ParentNativeID = record.Conversations[0].NativeID
		external.Conversations[0].AgentDepth = 1
		if r := c.Ingest(newCopyFixture(t, external), nil); r.Error != nil {
			t.Fatal(r)
		}
		// A delegation belongs to the parent workspace but references the
		// separately archived child. The parent's envelope may be replayed first.
		tx := seedTx(t, c.DB)
		if _, err := tx.Exec(`INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,parent_id,delegation_message_id,child_conversation_id,kind,provider)
			SELECT ?,p.workspace_id,p.id,'external-delegation',a.id,m.id,child.id,'subagent',p.provider
			FROM conversations p JOIN agent_sessions a ON a.conversation_id=p.id AND a.native_id='main'
			JOIN messages m ON m.conversation_id=p.id AND m.native_id='m'
			JOIN conversations child ON child.account=p.account AND child.native_id='external-child'
			WHERE p.native_id='session' AND p.account=?`, "delegation-"+host, record.Account); err != nil {
			t.Fatal(err)
		}
		commitSeed(t, tx)
	}
	j := prepareHistoryFixture(t, config, dest)
	j, e := runHistory(t.Context(), config, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	useHost(t, "rescue-machine")
	_, rebuilt, e := j.rebuild(t.Context(), config, filepath.Join(t.TempDir(), "two-hosts.sqlite3"))
	if e != nil {
		t.Fatal(e)
	}
	defer rebuilt.Close()
	var hosts, parents, accounts, rescue int
	for query, value := range map[string]*int{"SELECT COUNT(DISTINCT host_id) FROM workspace_sightings": &hosts, "SELECT COUNT(*) FROM conversations WHERE parent_id IS NOT NULL": &parents, "SELECT COUNT(DISTINCT account) FROM conversations": &accounts, "SELECT COUNT(*) FROM workspace_sightings WHERE host_id='rescue-machine'": &rescue} {
		if e = rebuilt.DB.QueryRow(query).Scan(value); e != nil {
			t.Fatal(e)
		}
	}
	if hosts != 2 || parents != 4 || accounts != 2 || rescue != 0 {
		t.Fatal("source identity/lineage flattened", hosts, parents, accounts, rescue)
	}
	var crossedAccounts int
	if e = rebuilt.DB.QueryRow("SELECT COUNT(*) FROM conversations c JOIN conversations p ON p.id=c.parent_id WHERE c.account<>p.account").Scan(&crossedAccounts); e != nil || crossedAccounts != 0 {
		t.Fatal("parent resolved against a different original account", crossedAccounts, e)
	}
	receipts, _, e := j.inventoryReceipts(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	envelopes := map[string]historyRecovery{}
	for _, receipt := range receipts {
		if receipt.Recovery == "" {
			continue
		}
		var envelope historyRecovery
		if e = historyReadJSON(historyRecoveryPath(config.CaptureRoot, receipt.Recovery), &envelope); e != nil {
			t.Fatal(e)
		}
		if envelope.Record.Account == "host-a-account" {
			envelopes[envelope.Record.SourceID] = envelope
		}
	}
	if len(envelopes) != 2 {
		t.Fatal("fixture lacks independent parent/child workspaces", len(envelopes))
	}
	t.Run("missing-delegation-target", func(t *testing.T) {
		fresh, _ := testCatalog(t)
		if outcome, err := fresh.applyHistoryRecovery(t.Context(), envelopes["session"]); err != nil || outcome != "catalog-derived" {
			t.Fatal(outcome, err)
		}
		var comparison historyVerification
		unit := historyUnit{Kind: "catalog-workspace", Original: envelopes["session"].OriginalID}
		if err := compareHistoryWorkspace(t.Context(), c.DB, fresh.DB, unit, &comparison); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(jsonText(comparison.Differences), "agent_sessions differs") {
			t.Fatalf("verification certified a missing delegation target: %+v", comparison)
		}
		var links int
		if err := fresh.DB.QueryRow(`SELECT COUNT(*) FROM agent_sessions WHERE native_id='external-delegation'
			AND parent_id IS NOT NULL AND delegation_message_id IS NOT NULL AND child_conversation_id IS NULL`).Scan(&links); err != nil || links != 1 {
			t.Fatal("missing child discarded the otherwise recoverable delegation", links, err)
		}
	})
	for _, parentFirst := range []bool{false, true} {
		t.Run(fmt.Sprint("parent-first=", parentFirst), func(t *testing.T) {
			fresh, _ := testCatalog(t)
			order := []string{"external", "session"}
			if parentFirst {
				order[0], order[1] = order[1], order[0]
			}
			for _, name := range order {
				envelope := envelopes[name]
				// The typed contract does not promise a topological row order.
				if parentFirst {
					envelope.Observations = append([]historyObservation(nil), envelope.Observations...)
					for i, observation := range envelope.Observations {
						if observation.Kind == "agent_sessions" {
							rows := append([][]sqliteValue(nil), observation.Rows...)
							for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
								rows[left], rows[right] = rows[right], rows[left]
							}
							envelope.Observations[i].Rows = rows
						}
					}
				}
				if outcome, err := fresh.applyHistoryRecovery(t.Context(), envelope); err != nil || outcome != "catalog-derived" {
					t.Fatal(outcome, err)
				}
			}
			var links int
			if err := fresh.DB.QueryRow("SELECT COUNT(*) FROM conversations WHERE parent_id IS NOT NULL").Scan(&links); err != nil || links != 2 {
				t.Fatal("recovery depended on workspace discovery order", links, err)
			}
			if err := fresh.DB.QueryRow(`SELECT COUNT(*) FROM agent_sessions a JOIN conversations child ON child.id=a.child_conversation_id
				WHERE a.native_id='external-delegation' AND child.native_id='external-child'`).Scan(&links); err != nil || links != 1 {
				t.Fatal("recovery lost a cross-workspace delegation", links, err)
			}
			var comparison historyVerification
			unit := historyUnit{Kind: "catalog-workspace", Original: envelopes["session"].OriginalID}
			if err := compareHistoryWorkspace(t.Context(), c.DB, fresh.DB, unit, &comparison); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(jsonText(comparison.Differences), "agent_sessions differs") {
				t.Fatalf("recovered delegation observations changed: %+v", comparison)
			}
		})
	}
	for _, mode := range []string{"recovery", "native-capture", "accepted-link", "native-owner"} {
		t.Run("delegation-child-later/"+mode, func(t *testing.T) {
			fresh, _ := testCatalog(t)
			if outcome, err := fresh.applyHistoryRecovery(t.Context(), envelopes["session"]); err != nil || outcome != "catalog-derived" {
				t.Fatal(outcome, err)
			}
			fresh.Close()
			fresh = reopenCatalog(t, fresh.Path)
			defer fresh.Close()
			switch mode {
			case "native-owner":
				ingestAs(t, fresh, "host-a", newCopyFixture(t, envelopes["session"].Record))
			case "accepted-link":
				execTx(t, fresh.DB, `UPDATE agent_sessions SET child_conversation_id=(SELECT id FROM conversations WHERE native_id='child')
					WHERE native_id='external-delegation'`)
			}
			if mode == "native-capture" {
				useHost(t, "host-a")
				root := t.TempDir()
				writeSourceFile(t, filepath.Join(root, "project", "external-child.jsonl"), claudeLine("external-child", "native-message", "native child", "/project", 1))
				source := SourceConfig{Name: "claude", Kind: "claude", Path: root, Account: "host-a-account", Enabled: true}
				capture := captureTestConfig(t, source)
				if run := runCapture(t, capture); !run.OK {
					t.Fatal(run)
				}
				target := captureTarget{Host: currentHost(), Source: source.Name, Dir: filepath.Join(capture.CaptureRoot, "host-a", source.Name)}
				if result := fresh.IndexCapture(t.Context(), target, nil); result.Error != nil {
					t.Fatal(result)
				}
			} else if outcome, err := fresh.applyHistoryRecovery(t.Context(), envelopes["external"]); err != nil || outcome != "catalog-derived" {
				t.Fatal(outcome, err)
			}
			if _, err := fresh.ReconcileIdentitiesContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			var linked int
			if err := fresh.DB.QueryRow(`SELECT COUNT(*) FROM agent_sessions a JOIN conversations child ON child.id=a.child_conversation_id
				WHERE a.native_id='external-delegation' AND child.native_id='external-child'`).Scan(&linked); err != nil {
				t.Fatal(err)
			}
			want := 1
			if mode == "native-owner" || mode == "accepted-link" {
				want = 0
			}
			if linked != want {
				t.Fatalf("late endpoint resolution changed accepted authority: links=%d want %d", linked, want)
			}
			var pending int
			if err := fresh.DB.QueryRow("SELECT COUNT(*) FROM meta WHERE key>=? AND key<'history_agent;'", historyAgentPrefix).Scan(&pending); err != nil || pending != 0 {
				t.Fatal("resolved or superseded delegation stayed pending", pending, err)
			}
		})
	}
	for _, mode := range []string{"existing-envelope", "native-capture"} {
		t.Run("native-parent-later/"+mode, func(t *testing.T) {
			fresh, _ := testCatalog(t)
			if outcome, err := fresh.applyHistoryRecovery(t.Context(), envelopes["external"]); err != nil || outcome != "catalog-derived" {
				t.Fatal(outcome, err)
			}
			// The unresolved relationship must survive a process restart too.
			fresh.Close()
			fresh = reopenCatalog(t, fresh.Path)
			useHost(t, "host-a")
			if mode == "existing-envelope" {
				ingestAs(t, fresh, "host-a", newCopyFixture(t, envelopes["session"].Record))
				if outcome, err := fresh.applyHistoryRecovery(t.Context(), envelopes["session"]); err != nil || outcome != "native-or-existing-kept" {
					t.Fatal(outcome, err)
				}
				if _, err := fresh.ReconcileIdentitiesContext(t.Context()); err != nil {
					t.Fatal(err)
				}
			} else {
				root := t.TempDir()
				writeSourceFile(t, filepath.Join(root, "project", "session.jsonl"), claudeLine("session", "native-message", "new native parent", "/project", 1))
				source := SourceConfig{Name: "claude", Kind: "claude", Path: root, Account: "host-a-account", Enabled: true}
				capture := captureTestConfig(t, source)
				if run := runCapture(t, capture); !run.OK {
					t.Fatal(run)
				}
				// This capture root has no history jobs: repair uses the committed
				// relationship in the catalog, without rereading recovery payloads.
				target := captureTarget{Host: currentHost(), Source: source.Name, Dir: filepath.Join(capture.CaptureRoot, "host-a", source.Name)}
				if result := fresh.IndexCapture(t.Context(), target, nil); result.Error != nil {
					t.Fatal(result)
				}
			}
			var links int
			if err := fresh.DB.QueryRow(`SELECT COUNT(*) FROM conversations c JOIN conversations p ON p.id=c.parent_id
				WHERE c.native_id='external-child' AND p.native_id='session' AND c.account=p.account`).Scan(&links); err != nil || links != 1 {
				t.Fatal("recovered child remained parentless after native indexing", links, err)
			}
		})
	}
	t.Run("selected-root-kept", func(t *testing.T) {
		original, _ := testCatalog(t)
		record := envelopes["external"].Record
		record.Conversations[0].ParentNativeID = ""
		ingestAs(t, original, "host-a", newCopyFixture(t, record))
		tx, err := original.DB.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		root, err := exportHistoryRecovery(t.Context(), tx, historyUnit{Kind: "catalog-workspace", Original: envelopes["external"].OriginalID}, "")
		tx.Rollback()
		if err != nil {
			t.Fatal(err)
		}
		fresh, _ := testCatalog(t)
		for _, envelope := range []historyRecovery{root, envelopes["session"]} {
			if outcome, err := fresh.applyHistoryRecovery(t.Context(), envelope); err != nil || outcome != "catalog-derived" {
				t.Fatal(outcome, err)
			}
		}
		if _, err := fresh.ReconcileIdentitiesContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		var linked int
		if err := fresh.DB.QueryRow("SELECT COUNT(*) FROM conversations WHERE native_id='external-child' AND parent_id IS NOT NULL").Scan(&linked); err != nil || linked != 0 {
			t.Fatal("incoming observation invented a parent for the selected root", linked, err)
		}
	})
	t.Run("native-child-kept", func(t *testing.T) {
		native, _ := testCatalog(t)
		record := envelopes["external"].Record
		record.Conversations[0].ParentNativeID = ""
		ingestAs(t, native, "host-a", newCopyFixture(t, record))
		if outcome, err := native.applyHistoryRecovery(t.Context(), envelopes["session"]); err != nil || outcome != "catalog-derived" {
			t.Fatal(outcome, err)
		}
		var linked int
		if err := native.DB.QueryRow("SELECT COUNT(*) FROM conversations WHERE native_id='external-child' AND parent_id IS NOT NULL").Scan(&linked); err != nil || linked != 0 {
			t.Fatal("degraded incoming edge modified a native child", linked, err)
		}
	})
}
func TestHistoryExistingPrivateSessionExclusion(t *testing.T) {
	c, config, dest := historyFixture(t)
	dir := filepath.Join(t.TempDir(), "conductor")
	exec := conductorFixture(t, dir)
	exec(addConductorSession("private", 1)...)
	source := SourceConfig{Name: "conductor", Kind: "conductor", Path: dir, Account: "local", Enabled: true}
	config.Sources = []SourceConfig{source}
	if run := runCapture(t, config); !run.OK {
		t.Fatal(run)
	}
	// Determine this provider's public identity through the ordinary parser.
	adapter, e := MakeAdapter(source)
	if e != nil {
		t.Fatal(e)
	}
	var identity ConversationRecord
	if e = adapter.Discover(func(r WorkspaceRecord) error { identity = r.Conversations[0]; return nil }); e != nil {
		t.Fatal(e)
	}
	if _, e = c.DB.Exec("INSERT INTO ask_native_sessions(provider,native_id,host_id) VALUES(?,?,?)", identity.Provider, identity.NativeID, "host-a"); e != nil {
		t.Fatal(e)
	}
	j := prepareHistoryFixture(t, config, dest)
	j, e = runHistory(t.Context(), config, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	_, rebuilt, e := j.rebuild(t.Context(), config, filepath.Join(t.TempDir(), "excluded.sqlite3"))
	if e != nil {
		t.Fatal(e)
	}
	defer rebuilt.Close()
	var count int
	if e = rebuilt.DB.QueryRow("SELECT COUNT(*) FROM conversations").Scan(&count); e != nil || count != 0 {
		t.Fatal("private native protocol was resurrected", count, e)
	}
}

func TestHistoryDriveRemovalDoesNotRecreateMetadata(t *testing.T) {
	c, config, dest := historyFixture(t)
	if r := c.Ingest(newCopyFixture(t, sessionCopy("/gone/a.jsonl", "m")), nil); r.Error != nil {
		t.Fatal(r)
	}
	j := prepareHistoryFixture(t, config, dest)
	offline := config.CaptureRoot + "-offline"
	if e := os.Rename(config.CaptureRoot, offline); e != nil {
		t.Fatal(e)
	}
	if e := j.save(); e == nil {
		t.Fatal("missing drive accepted job metadata")
	}
	if _, e := os.Stat(config.CaptureRoot); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("job recreated missing capture drive", e)
	}
	if _, e := planHistory(t.Context(), config, historyScope{}); e == nil {
		t.Fatal("missing capture drive became empty history")
	}
	if e := os.Rename(offline, config.CaptureRoot); e != nil {
		t.Fatal(e)
	}
	if _, e := runHistory(t.Context(), config, j.ID); e != nil {
		t.Fatal(e)
	}
}

func TestHistoryOfflineEnrichmentUsesOnlyCommittedEvidence(t *testing.T) {
	for _, newerCapture := range []bool{false, true} {
		t.Run(fmt.Sprint("newer-capture=", newerCapture), func(t *testing.T) {
			c, config, dest := historyFixture(t)
			origin := filepath.Join(t.TempDir(), "rollout-late.jsonl")
			r := sessionCopy(origin, "m")
			r.SourceKind, r.Conversations[0].Provider = "codex", "codex"
			fixture := newCopyFixture(t, r)
			fixture.config.Name, fixture.config.Kind = "codex", "codex"
			if result := c.Ingest(fixture, nil); result.Error != nil {
				t.Fatal(result)
			}
			j := prepareHistoryFixture(t, config, dest)
			j, err := runHistory(t.Context(), config, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			// Neither a live file nor an independently captured newer file is
			// evidence for this job, even when its original host is this Mac.
			writeSourceFile(t, origin, `{"type":"session_meta","payload":{"cli_version":"999.0","originator":"post-cutoff"}}`+"\n")
			if newerCapture {
				config.Sources = []SourceConfig{{Name: "codex", Kind: "codex", Path: filepath.Dir(origin), Account: "local", Enabled: true}}
				if run := runCapture(t, config); !run.OK {
					t.Fatal(run)
				}
				if err = os.Remove(origin); err != nil {
					t.Fatal(err)
				}
			}
			captureContents := func() string {
				t.Helper()
				files := map[string]string{}
				err := filepath.WalkDir(config.CaptureRoot, func(path string, entry os.DirEntry, err error) error {
					if err != nil || entry.IsDir() {
						return err
					}
					hash, _, err := fileSHA256(path)
					files[path] = hash
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				return jsonText(files)
			}
			before := captureContents()
			_, rebuilt, err := j.rebuild(t.Context(), config, filepath.Join(t.TempDir(), "offline.sqlite3"))
			if err != nil {
				t.Fatal(err)
			}
			defer rebuilt.Close()
			// Exercise both enrichment consumers on the scratch catalog.
			if _, err = rebuilt.BackfillInstructions(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			var version string
			if err = rebuilt.DB.QueryRow("SELECT COALESCE(harness_version_last,'') FROM conversations LIMIT 1").Scan(&version); err != nil || version != "" {
				t.Fatalf("imported post-cutoff version %q: %v", version, err)
			}
			if after := captureContents(); after != before {
				t.Fatal("offline enrichment wrote capture evidence")
			}
		})
	}
}

func TestHistoryOversizedRecoveryDoesNotBlockOtherUnits(t *testing.T) {
	for _, mode := range []string{"cell", "aggregate"} {
		t.Run(mode, func(t *testing.T) {
			c, config, dest := historyFixture(t)
			override(t, &historyEntityBytes, int64(32<<10))
			large := sessionCopy("/deleted/large.jsonl", "large")
			large.SourceID, large.Conversations[0].NativeID = "large", "large"
			if mode == "cell" {
				large.Conversations[0].Messages[0].Text = strings.Repeat("x", 64<<10)
			} else {
				ids := make([]string, 40)
				for i := range ids {
					ids[i] = fmt.Sprint("m", i)
				}
				large = sessionCopy("/deleted/large.jsonl", ids...)
				large.SourceID, large.Conversations[0].NativeID = "large", "large"
				for i := range large.Conversations[0].Messages {
					large.Conversations[0].Messages[i].Text = strings.Repeat("x", 1024)
				}
			}
			small := sessionCopy("/deleted/small.jsonl", "small")
			small.SourceID, small.Conversations[0].NativeID = "small", "small"
			if result := c.Ingest(newCopyFixture(t, large, small), nil); result.Error != nil {
				t.Fatal(result)
			}
			j := prepareHistoryFixture(t, config, dest)
			var largeID string
			if err := c.DB.QueryRow("SELECT id FROM workspaces WHERE source_id='large'").Scan(&largeID); err != nil {
				t.Fatal(err)
			}
			// Ensure the oversized unit precedes its healthy neighbor.
			for i, u := range j.Plan.Units {
				if u.Original == largeID {
					j.Plan.Units[0], j.Plan.Units[i] = u, j.Plan.Units[0]
				}
			}
			if err := j.save(); err != nil {
				t.Fatal(err)
			}
			j, err := runHistory(t.Context(), config, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			receipts, report, err := j.inventoryReceipts(t.Context())
			if err != nil || report.Unresolved != 1 || report.CatalogDerived != 1 || report.Pending != 0 {
				t.Fatalf("%+v: %v", report, err)
			}
			for _, r := range receipts {
				if r.Tier == "unresolved" && (r.Closed || r.OracleHash != j.OracleHash || !strings.Contains(r.Gap, "per-entity memory limit")) {
					t.Fatalf("invalid oversized receipt: %+v", r)
				}
			}
			if _, err = runHistory(t.Context(), config, j.ID); err != nil {
				t.Fatal("resume retried an unresolved unit", err)
			}
			report, err = verifyHistory(t.Context(), config, j.ID, filepath.Join(t.TempDir(), "verify.sqlite3"))
			if err != nil || report.State != "verified-with-gaps" || report.Unresolved != 1 || report.Compared != 1 || report.CatalogOnlyApplied != 1 {
				t.Fatalf("verification stalled or certified oversized recovery: %+v %v", report, err)
			}
		})
	}
}

func TestHistoryRecoveryConflictsRespectSourceScope(t *testing.T) {
	for _, damage := range []string{"corrupt", "oversized"} {
		t.Run(damage+"-job", func(t *testing.T) {
			useHost(t, "host-a")
			catalog, _ := testCatalog(t)
			dir := t.TempDir()
			writeSourceFile(t, filepath.Join(dir, "s.jsonl"), claudeLine("s", "u1", "healthy captured input", "/p", 1))
			config := captureTestConfig(t, SourceConfig{Name: "claude", Kind: "claude", Path: dir, Account: "local", Enabled: true})
			runCapture(t, config)
			id := strings.Repeat("a", 64)
			body := "{"
			if damage == "oversized" {
				override(t, &historyMetadataLimit, int64(128))
				body = strings.Repeat(" ", 129)
			}
			job := filepath.Join(config.CaptureRoot, historyDir, "jobs", id, "job.json")
			writeSourceFile(t, job, body)
			target := captureTarget{Host: currentHost(), Source: "claude", Dir: filepath.Join(config.CaptureRoot, "host-a", "claude")}
			for range 2 {
				result := catalog.IndexCapture(t.Context(), target, nil)
				if result.Error != nil || len(result.HistoryWarnings) != 1 || conversationMessages(t, catalog, "s") != 1 {
					t.Fatalf("optional job metadata blocked native indexing: %+v", result)
				}
			}
			if _, err := loadHistoryJob(config.CaptureRoot, id); err == nil {
				t.Fatal("explicit job load ignored damaged metadata")
			}
			if readText(t, job) != body {
				t.Fatal("ordinary indexing changed the damaged job")
			}
		})
	}
	for _, scope := range []string{"source", "host"} {
		t.Run(scope, func(t *testing.T) {
			c, config, dest := historyFixture(t)
			a := sessionCopy("/deleted/a.jsonl", "a")
			a.SourceID, a.Conversations[0].NativeID = "a", "a"
			fa := newCopyFixture(t, a)
			fa.config.Name = "source-a"
			ingestAs(t, c, "host-a", fa)
			b := sessionCopy("/deleted/b.jsonl", "b")
			b.SourceID, b.Conversations[0].NativeID = "b", "b"
			fb := newCopyFixture(t, b)
			fb.config.Name = "source-b"
			hostB := "host-a"
			if scope == "host" {
				hostB, fb.config.Name = "host-b", "source-a"
			}
			ingestAs(t, c, hostB, fb)
			useHost(t, "host-a")
			first := prepareHistoryFixture(t, config, dest)
			if _, err := runHistory(t.Context(), config, first.ID); err != nil {
				t.Fatal(err)
			}
			execTx(t, c.DB, `UPDATE messages SET text='a conflicting observation' WHERE native_id='a'`)
			second := prepareHistoryFixture(t, config, filepath.Join(filepath.Dir(dest), "rollback-second"))
			if _, err := runHistory(t.Context(), config, second.ID); err != nil {
				t.Fatal(err)
			}
			rebuilt, _ := testCatalog(t)
			target := captureTarget{Host: Host{ID: hostB}, Source: fb.config.Name, Dir: filepath.Join(config.CaptureRoot, hostB, fb.config.Name)}
			applied, err := rebuilt.indexHistoricalRecovery(t.Context(), target, false)
			if err != nil || applied != 1 || conversationMessages(t, rebuilt, "b") == 0 {
				t.Fatalf("unrelated conflict blocked recovery: %d %v", applied, err)
			}
			target = captureTarget{Host: Host{ID: "host-a"}, Source: fa.config.Name, Dir: filepath.Join(config.CaptureRoot, "host-a", fa.config.Name)}
			if _, err = rebuilt.indexHistoricalRecovery(t.Context(), target, false); err == nil || !strings.Contains(err.Error(), "conflicting catalog-derived") {
				t.Fatalf("matching conflict was given implicit authority: %v", err)
			}
		})
	}
}

// History backfill reads compressed latest files and earlier versions by
// their original bytes: planning, pins, the rollback backup's check and the
// raw export all decode them.
func TestHistoryBackfillsCompressedFiles(t *testing.T) {
	c, config, dest := historyFixture(t)
	override(t, &compressMinBytes, 1)
	claude := filepath.Join(t.TempDir(), "claude")
	lines := ""
	for line := range 30 {
		lines += claudeLine("kept", fmt.Sprintf("kept-%d", line), strings.Repeat("retained text ", 20), "/proj", line)
	}
	writeSourceFile(t, filepath.Join(claude, "p", "kept.jsonl"), lines)
	writeSourceFile(t, filepath.Join(claude, "p", "rewritten.jsonl"), lines)
	config.Sources = []SourceConfig{{Name: "claude", Kind: "claude", Path: claude, Account: "local", Enabled: true}}
	runCapture(t, config)
	writeSourceFile(t, filepath.Join(claude, "p", "rewritten.jsonl"), claudeLine("rewritten", "r-1", "new", "/proj", 1))
	runCapture(t, config)
	indexCaptures(t, c, config.CaptureRoot, "host-a")
	runCapture(t, config)
	compressed := 0
	if err := compressCaptures(t.Context(), config, c, func(compressedGeneration) { compressed++ }); err != nil || compressed != 2 {
		t.Fatal("fixture: expected the cold latest file and the superseded version compressed", compressed, err)
	}
	p, err := planHistory(t.Context(), config, historyScope{Host: "host-a", Source: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	encoded := 0
	for _, u := range p.Units {
		if u.Kind == "file" && u.Gap != "" {
			t.Fatal("compressed file planned as a gap", u)
		}
		if u.Version.Encoding != "" {
			encoded++
		}
	}
	if encoded != 2 {
		t.Fatal("plan did not see both compressed files", encoded)
	}
	j, err := prepareHistory(t.Context(), config, p, dest, 128<<20, 128<<20)
	if err != nil {
		t.Fatal(err)
	}
	pinned := 0
	for _, pin := range j.Protected {
		if pin.Encoding == captureEncodingZstd {
			pinned++
		}
	}
	if pinned != 2 {
		t.Fatal("pins do not record the compressed inputs' encoding", j.Protected)
	}
	if j, err = runHistory(t.Context(), config, j.ID); err != nil || j.State != "exported" {
		t.Fatal("backfill over compressed files", j.State, err)
	}
}
