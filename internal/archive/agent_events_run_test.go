package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	labelrt "github.com/gbdubs/pharos/internal/labeler"
	"github.com/gbdubs/pharos/internal/labeler/labelertest"
)

// agentEventsTestSettings are the fake worker (labelsTestSettings) and a
// verifier directory for it: the committed tokenizer, a stand-in model file
// and a manifest like the export's. The fake's verdict for a candidate is
// labelertest.FakeProb of its anchor's tokens.
func agentEventsTestSettings(t *testing.T, manifestFields ...map[string]any) (LabelerSettings, AgentEventSettings) {
	t.Helper()
	labels := labelsTestSettings(t, "ok")
	dir := t.TempDir()
	tokenizer, err := os.ReadFile(filepath.Join(labels.ModelDir, "tokenizer.json"))
	if err != nil {
		t.Fatal(err)
	}
	model := []byte("stand-in verifier")
	sum := func(data []byte) string { s := sha256.Sum256(data); return hex.EncodeToString(s[:]) }
	lexicon, _ := agentEventLexicon()
	definitions := map[string]string{}
	for _, family := range agentEventFamilies {
		definitions[family.Key] = family.Definition
	}
	fields := map[string]any{
		"name": "fake-verifier", "framing_contract": agentEventFraming.Contract, "input_contract": labelrt.InputContractIDsAttentionPoolSegments,
		"max_seq": 768, "files": map[string]string{"onnx_int8": "model.int8.onnx", "tokenizer": "tokenizer.json"},
		"sha256":  map[string]string{"model.int8.onnx": sum(model), "tokenizer.json": sum(tokenizer)},
		"onnx":    map[string]any{"outputs": []string{"verdict", "families", "sentences"}},
		"lexicon": lexicon, "definitions": definitions, "decision": map[string]any{"threshold": 0.5},
	}
	for _, extra := range manifestFields {
		maps.Copy(fields, extra)
	}
	manifest, _ := json.Marshal(fields)
	for name, data := range map[string][]byte{"tokenizer.json": tokenizer, "model.int8.onnx": model, "manifest.json": manifest} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return labels, AgentEventSettings{Enabled: true, ModelDir: dir, Variant: "int8"}
}

// The labeling fixture plus a conversation whose agent writes like the study's:
// a workaround and a stale doc in a reply, confusion in thinking, and a fenced
// log that is never read.
const agentEventsTestReply = "The endpoint doesn't support pagination. I'll work around that by fetching one day at a time.\n" +
	"```\nERROR: still fails, not sure why\n```\nThe README says to run make setup, but that target no longer exists."

func agentEventsCatalog(t *testing.T) *Catalog {
	t.Helper()
	catalog := labeledCatalog(t)
	tx := seedTx(t, catalog.DB)
	for _, statement := range []string{
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,started_at) VALUES('c-events','ws-main','claude','local','c-events','2026-10-25T12:00:00.000Z')`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,selected,source_order) VALUES
			('m-ev-user','c-events','m-ev-user','user','message','Fetch the report for October.','h1','2026-10-25T12:00:00.000Z',1,1001),
			('m-ev-think','c-events','m-ev-think','assistant','thinking','I''m not sure why the second page is empty. Maybe the cursor expired.','h2','2026-10-25T12:01:00.000Z',1,1002),
			('m-ev-reply','c-events','m-ev-reply','assistant','message',?,'h3','2026-10-25T12:02:00.000Z',1,1003)`,
		// Its thinking is extracted, so the pass reads it.
		`INSERT INTO reasoning_extraction_state(conversation_id,version,retained_count,retained_bytes,summary_count,unavailable_count) VALUES('c-events',?,1,70,0,0)`,
	} {
		args := []any{}
		if strings.Contains(statement, "reasoning_extraction_state") {
			args = append(args, reasoningExtractionVersion)
		}
		if strings.Contains(statement, "m-ev-reply") {
			args = append(args, agentEventsTestReply)
		}
		if _, err := tx.Exec(statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	// A fork without a start time copies its parent's history in order: its
	// copy of the root's first reply is inherited, not its own.
	if _, err := tx.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,selected,source_order)
		SELECT 'm-f3-a','c-fork3','m-f3-a','assistant','message','I will look into the export button.','h-f3a','2026-09-01T12:01:00.000Z',1,source_order
		FROM messages WHERE id='m-f3-1'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return catalog
}

// agentEventsExpected is what the fake verifier makes of a message: each
// candidate's probability, by "kind@sentence start".
func agentEventsExpected(t *testing.T, labels LabelerSettings, settings AgentEventSettings, source, text string) map[string]float64 {
	t.Helper()
	verifier, err := loadAgentEventVerifier(settings.ModelDir, settings.Variant)
	if err != nil {
		t.Fatal(err)
	}
	ranges := agentEventSentences(text)
	texts := make([]string, len(ranges))
	for i, r := range ranges {
		texts[i] = text[r[0]:r[1]]
	}
	out := map[string]float64{}
	for _, candidate := range agentEventCandidates(texts) {
		row := agentEventFrame(verifier.model.Tokenizer, verifier.definitions, candidate.Kind, source, texts, candidate.Sentence, verifier.model.MaxSeq)
		key := candidate.Kind + "@" + strconv.Itoa(ranges[candidate.Sentence][0])
		out[key] = float64(labelertest.FakeProb(row.IDs[row.Spans.Text.Start:row.Spans.Text.End], 0))
	}
	return out
}

func agentEventsStored(t *testing.T, catalog *Catalog, message string) map[string]float64 {
	t.Helper()
	rows, err := queryMaps(catalog.DB, `SELECT family,sentence_start,probability FROM agent_events WHERE message_id=?`, message)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, row := range rows {
		out[firstString(row["family"])+"@"+strconv.Itoa(int(integer(row["sentence_start"])))] = row["probability"].(float64)
	}
	return out
}

func TestAgentEventsPassScoresChangedMessagesOnly(t *testing.T) {
	catalog := agentEventsCatalog(t)
	labels, settings := agentEventsTestSettings(t)
	stats, err := catalog.RunAgentEvents(t.Context(), labels, settings, AgentEventsRunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Complete || stats.MessagesScored == 0 || stats.Candidates == 0 {
		t.Fatalf("first run: %+v", stats)
	}
	// Every candidate at or above the floor is stored with the verifier's
	// probability; the fenced log's sentence is never a candidate.
	for id, source := range map[string]string{"m-ev-reply": "reply", "m-ev-think": "thinking"} {
		var text string
		if err := catalog.DB.QueryRow(`SELECT text FROM messages WHERE id=?`, id).Scan(&text); err != nil {
			t.Fatal(err)
		}
		want := map[string]float64{}
		for key, p := range agentEventsExpected(t, labels, settings, source, text) {
			if p >= agentEventStoreFloor {
				want[key] = float64(float32(p))
			}
		}
		got := agentEventsStored(t, catalog, id)
		if !maps.EqualFunc(got, want, func(a, b float64) bool { return float32(a) == float32(b) }) {
			t.Errorf("%s: stored %v, want %v", id, got, want)
		}
		for key := range agentEventsExpected(t, labels, settings, source, text) {
			if start := key[strings.Index(key, "@")+1:]; id == "m-ev-reply" && start == strconv.Itoa(strings.Index(agentEventsTestReply, "ERROR")) {
				t.Errorf("the fenced log was a candidate: %s", key)
			}
		}
	}
	// Every agent reply and thinking unit is read, with or without candidates;
	// people's messages and delegations are not.
	var read, people int
	if err := catalog.DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(message_id IN ('m-h1','m-ev-user','m-del')),0) FROM agent_event_messages`).Scan(&read, &people); err != nil {
		t.Fatal(err)
	}
	if read != stats.MessagesScored || people != 0 {
		t.Fatalf("%d messages read (%d people's or delegations) for %d scored", read, people, stats.MessagesScored)
	}
	var source string
	if err := catalog.DB.QueryRow(`SELECT source FROM agent_event_messages WHERE message_id='m-ev-think'`).Scan(&source); err != nil || source != "thinking" {
		t.Fatalf("the thinking block was read as %q (%v)", source, err)
	}
	var events int
	if err := catalog.DB.QueryRow(`SELECT COALESCE(SUM(events),0) FROM agent_event_messages`).Scan(&events); err != nil || events != stats.Events {
		t.Fatalf("message rows count %d events, the run %d (%v)", events, stats.Events, err)
	}

	// Nothing changed: nothing is looked at again.
	again, err := catalog.RunAgentEvents(t.Context(), labels, settings, AgentEventsRunOptions{})
	if err != nil || again.ConversationsBuilt != 0 || again.MessagesScored != 0 || !again.Complete {
		t.Fatalf("second run: %+v %v", again, err)
	}

	// A corrected time moves the reply's rows to its day without scoring it.
	execTx(t, catalog.DB, `UPDATE messages SET created_at='2026-10-26T23:30:00.000Z' WHERE id='m-ev-reply'`)
	retimed, err := catalog.RunAgentEvents(t.Context(), labels, settings, AgentEventsRunOptions{})
	if err != nil || retimed.ConversationsBuilt != 1 || retimed.MessagesScored != 0 || !retimed.Complete {
		t.Fatalf("after a time correction: %+v %v", retimed, err)
	}
	want := agentEventDay("2026-10-26T23:30:00.000Z")
	var stale int
	if err := catalog.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM agent_event_messages WHERE message_id='m-ev-reply' AND day IS NOT ?)+
		(SELECT COUNT(*) FROM agent_events WHERE message_id='m-ev-reply' AND (day IS NOT ? OR occurred_at<>'2026-10-26T23:30:00.000Z'))`, want, want).Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("%d rows kept the old day (%v)", stale, err)
	}

	// An edited reply is scored again, alone; a deleted one loses its rows.
	if _, err := catalog.DB.Exec(`UPDATE messages SET text=? WHERE id='m-ev-reply'`, "That workaround didn't help either."); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec(`DELETE FROM messages WHERE id='m-ev-think'`); err != nil {
		t.Fatal(err)
	}
	edited, err := catalog.RunAgentEvents(t.Context(), labels, settings, AgentEventsRunOptions{})
	// The deleted one's rows went with it (agent_events_message_deleted).
	if err != nil || edited.ConversationsBuilt != 1 || edited.MessagesScored != 1 {
		t.Fatalf("after an edit: %+v %v", edited, err)
	}
	if got := agentEventsStored(t, catalog, "m-ev-think"); len(got) != 0 {
		t.Fatalf("a deleted message kept events %v", got)
	}

	// A parent's corrected reply no longer matches its fork's copy, which
	// becomes the fork's own reply: the fork is looked at again, even when a
	// limited run stops before it (its own dirty mark keeps it pending).
	readMessage := func(id string) bool {
		var n int
		_ = catalog.DB.QueryRow(`SELECT COUNT(*) FROM agent_event_messages WHERE message_id=?`, id).Scan(&n)
		return n > 0
	}
	if readMessage("m-f3-a") {
		t.Fatal("the fork's copy of its parent's reply was read as its own")
	}
	execTx(t, catalog.DB, `UPDATE messages SET text='I will look into the export button first.' WHERE id='m-a1'`)
	if _, err := catalog.RunAgentEvents(t.Context(), labels, settings, AgentEventsRunOptions{Limit: 1}); err != nil {
		t.Fatal(err)
	}
	var pendingFork int
	_ = catalog.DB.QueryRow(`SELECT COUNT(*) FROM agent_events_dirty WHERE conversation_id='c-fork3'`).Scan(&pendingFork)
	if !readMessage("m-f3-a") && pendingFork == 0 {
		t.Fatal("a limited run dropped the fork of a changed conversation")
	}
	forked, err := catalog.RunAgentEvents(t.Context(), labels, settings, AgentEventsRunOptions{})
	if err != nil || !forked.Complete || !readMessage("m-f3-a") {
		t.Fatalf("after the parent's edit: %+v %v; the fork's reply read: %v", forked, err, readMessage("m-f3-a"))
	}

	// Rows another verifier made (an import's have no input hash) are
	// scored again; so is everything when the verifier changes.
	if _, err := catalog.DB.Exec(`UPDATE agent_event_messages SET input_hash=NULL WHERE message_id='m-ev-reply'`); err != nil {
		t.Fatal(err)
	}
	imported, err := catalog.RunAgentEvents(t.Context(), labels, settings, AgentEventsRunOptions{})
	if err != nil || imported.ConversationsBuilt != 1 || imported.MessagesScored != 1 {
		t.Fatalf("after an import: %+v %v", imported, err)
	}
	_, other := agentEventsTestSettings(t, map[string]any{"name": "another-verifier"})
	var current int
	_ = catalog.DB.QueryRow(`SELECT COUNT(*) FROM agent_event_messages`).Scan(&current)
	switched, err := catalog.RunAgentEvents(t.Context(), labels, other, AgentEventsRunOptions{})
	if err != nil || switched.MessagesScored != current || switched.MessagesUnchanged != 0 {
		t.Fatalf("after a model switch: %+v %v (%d messages)", switched, err, current)
	}
}

func TestAgentEventsLiveThreadTarget(t *testing.T) {
	for _, device := range []string{"", "cpu", "gpu"} {
		t.Run("device="+device, func(t *testing.T) {
			t.Setenv(labelertest.FakeDeviceEnv, device)
			catalog := agentEventsCatalog(t)
			labels, settings := agentEventsTestSettings(t)
			override(t, &agentEventsChunkRows, 1)
			override(t, &labelerThreadCheck, time.Duration(0))
			hellos := filepath.Join(t.TempDir(), "hellos.jsonl")
			t.Setenv(labelertest.FakeHelloEnv, hellos)
			var observed []int
			var reportedDevice atomic.Bool
			ctx := withWorkProgress(t.Context(), func(work activityWork) {
				if device != "" && strings.Contains(work.Detail, strings.ToUpper(device)) {
					reportedDevice.Store(true)
				}
			})
			stats, err := catalog.RunAgentEvents(ctx, labels, settings, AgentEventsRunOptions{ThreadTarget: func() LabelerSettings {
				if catalog.labelers != nil {
					threads, _, _ := catalog.labelers.Usage()
					if len(observed) == 0 || observed[len(observed)-1] != threads {
						observed = append(observed, threads)
					}
				}
				target := labels
				target.Threads = 1
				if slices.Contains(observed, 1) {
					target.Threads = 4
				}
				if slices.Contains(observed, 4) {
					target.Threads = 2
				}
				return target
			}})
			if err != nil || !stats.Complete || stats.ConversationsFailed != 0 || stats.Candidates == 0 {
				t.Fatalf("changing cores: %+v %v", stats, err)
			}
			want := []int{1, 4, 2}
			if device == "gpu" {
				want = []int{1}
			}
			if threads := labelerHelloThreads(t, hellos); !slices.Equal(threads, want) {
				t.Fatalf("worker thread targets: %v", threads)
			}
			if device != "" && !reportedDevice.Load() {
				t.Fatalf("progress never reported the confirmed %s device", device)
			}
			again, err := catalog.RunAgentEvents(t.Context(), labels, settings, AgentEventsRunOptions{})
			if err != nil || !again.Complete || again.MessagesScored != 0 {
				t.Fatalf("changing cores left work pending: %+v %v", again, err)
			}
		})
	}
}

func TestAgentEventsRunsInLimitedSteps(t *testing.T) {
	catalog := agentEventsCatalog(t)
	labels, settings := agentEventsTestSettings(t)
	var current atomic.Pointer[activityWork]
	ctx := withWorkProgress(t.Context(), func(work activityWork) { current.Store(&work) })
	first, err := catalog.RunAgentEvents(ctx, labels, settings, AgentEventsRunOptions{Limit: 1})
	if err != nil || !first.Cut || first.Complete {
		t.Fatalf("a limited run: %+v %v", first, err)
	}
	// The newest conversation comes first.
	var newest int
	if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM agent_event_messages WHERE conversation_id='c-events'`).Scan(&newest); err != nil || newest == 0 {
		t.Fatalf("the limited run skipped the newest conversation (%v)", err)
	}
	initial := *current.Load()
	var resumed atomic.Pointer[activityWork]
	ctx = withWorkProgress(t.Context(), func(work activityWork) {
		resumed.CompareAndSwap(nil, &work)
		current.Store(&work)
	})
	rest, err := catalog.RunAgentEvents(ctx, labels, settings, AgentEventsRunOptions{progressBase: first.progress})
	if err != nil || !rest.Complete || rest.MessagesScored == 0 {
		t.Fatalf("the rest: %+v %v", rest, err)
	}
	if start, end := resumed.Load(), current.Load(); start.Done != initial.Done || start.Total != initial.Total || end.Done != end.Total || end.Total != initial.Total {
		t.Fatalf("handoff reset progress: initial=%+v resumed=%+v end=%+v", initial, start, end)
	}
	if rest.progress.messages != first.MessagesScored+rest.MessagesScored || rest.progress.candidates != first.Candidates+rest.Candidates {
		t.Fatalf("handoff lost message or candidate counts: %+v", rest.progress)
	}
	dry, err := catalog.RunAgentEvents(t.Context(), labels, settings, AgentEventsRunOptions{Full: true, DryRun: true})
	if err != nil || dry.MessagesScored != 0 || dry.MessagesUnchanged != first.MessagesScored+rest.MessagesScored {
		t.Fatalf("a dry run of a current ledger: %+v %v", dry, err)
	}
}

func TestAgentEventVerifierMustMatchThisBuild(t *testing.T) {
	for name, fields := range map[string]map[string]any{
		"another lexicon":       {"lexicon": "jayhawk-lexicon-1"},
		"another threshold":     {"decision": map[string]any{"threshold": 0.6}},
		"a missing definition":  {"definitions": map[string]string{"workaround": "Getting around it."}},
		"the labeler's framing": {"framing_contract": labelrt.FramingContract},
		"no sentence outputs":   {"onnx": map[string]any{"outputs": []string{"verdict"}}},
	} {
		_, settings := agentEventsTestSettings(t, fields)
		if _, err := loadAgentEventVerifier(settings.ModelDir, "int8"); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

// The chain finds agent events only where they are turned on, as their own
// background item after labels, and completes its stage.
func TestAgentEventsAreAChainStageOnlyWhenEnabled(t *testing.T) {
	for _, mode := range []string{"off", "configuration", "settings switch"} {
		enabled := mode != "off"
		catalog, config := testCatalog(t)
		seedLabelingCatalog(t, catalog)
		config.Labels, config.AgentEvents = agentEventsTestSettings(t)
		config.Labels.Enabled, config.AgentEvents.Enabled = false, mode == "configuration"
		if mode == "settings switch" {
			execTx(t, catalog.DB, `INSERT INTO ui_preferences(key,value) VALUES('`+agentEventsFlag+`','true')`)
			catalog.flags.invalidate()
		}
		server := testServer(t, config, catalog)
		if err := catalog.writeTransaction(t.Context(), "test", markSyncDeferred); err != nil {
			t.Fatal(err)
		}
		if err := server.finishDeferredSync(context.Background()); err != nil {
			t.Fatal(err)
		}
		var read int
		_ = catalog.DB.QueryRow(`SELECT COUNT(*) FROM agent_event_messages`).Scan(&read)
		settings, _ := catalog.backgroundSettings(t.Context(), time.Now())
		planned := slices.ContainsFunc(backgroundPlan(settings, server.backgroundInputsFor(t.Context(), nil), time.Now()), func(plan itemPlan) bool { return plan.ID == "agent_events" })
		if !enabled {
			if read != 0 || planned {
				t.Fatalf("%s: %d messages read, item planned %v", mode, read, planned)
			}
			continue
		}
		if read == 0 || !planned {
			t.Fatalf("on by the %s: %d messages read, item planned %v", mode, read, planned)
		}
		if status, err := catalog.agentEventsStatus(t.Context(), config); err != nil || status["enabled"] != true {
			t.Fatalf("on by the %s, status says enabled %v (%v)", mode, status["enabled"], err)
		}
		deferred, _ := catalog.freshnessDeferred(t.Context())
		if row := deferred["agent_events"]; row.generation == 0 || row.generation > row.completed {
			t.Fatalf("the agent events stage did not complete: %+v", row)
		}
	}
}

// A horizon leaves older messages unread and older conversations unbuilt,
// and keeps rows already stored for them.
func TestAgentEventsReadOnlyWithinTheHorizon(t *testing.T) {
	catalog := agentEventsCatalog(t)
	labels, settings := agentEventsTestSettings(t)
	// A September reply in October's conversation; an October reply in a
	// conversation that started in September and has no recorded end; and
	// September's root conversation, which ended in September.
	execTx(t, catalog.DB,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,selected,source_order) VALUES
			('m-ev-old','c-events','m-ev-old','assistant','message','That workaround didn''t help.','h4','2026-09-20T12:00:00.000Z',1,1000),
			('m-o-late','c-other','m-o-late','assistant','message','The build still fails on the runner.','h5','2026-10-26T12:00:00.000Z',1,1050)`,
		`UPDATE conversations SET ended_at='2026-09-01T13:00:00.000Z' WHERE id='c-root'`)
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	stats, err := catalog.RunAgentEvents(t.Context(), labels, settings, AgentEventsRunOptions{Since: since})
	if err != nil || !stats.Complete || stats.MessagesBefore == 0 {
		t.Fatalf("a run since October: %+v %v", stats, err)
	}
	read := func(id string) bool {
		var n int
		_ = catalog.DB.QueryRow(`SELECT COUNT(*) FROM agent_event_messages WHERE message_id=?`, id).Scan(&n)
		return n > 0
	}
	var outside, root int
	if err := catalog.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM agent_event_messages WHERE occurred_at<?),
		(SELECT COUNT(*) FROM agent_events_conversations WHERE conversation_id='c-root')`, formatTime(since)).Scan(&outside, &root); err != nil || outside != 0 || root != 0 {
		t.Fatalf("%d messages read before the horizon; the conversation that ended in September built: %v (%v)", outside, root != 0, err)
	}
	if !read("m-o-late") {
		t.Fatal("a recent reply in a conversation without a recorded end was skipped")
	}
	// A wider horizon reads what the narrower one left, in conversations
	// already built too; the narrower one again removes nothing.
	if _, err := catalog.RunAgentEvents(t.Context(), labels, settings, AgentEventsRunOptions{}); err != nil {
		t.Fatal(err)
	}
	if !read("m-ev-old") {
		t.Fatal("widening the horizon left a built conversation's older reply unread")
	}
	var all int
	_ = catalog.DB.QueryRow(`SELECT COUNT(*) FROM agent_event_messages`).Scan(&all)
	again, err := catalog.RunAgentEvents(t.Context(), labels, settings, AgentEventsRunOptions{Since: since, Full: true})
	var after int
	_ = catalog.DB.QueryRow(`SELECT COUNT(*) FROM agent_event_messages`).Scan(&after)
	if err != nil || again.MessagesScored != 0 || again.MessagesBefore == 0 || after != all {
		t.Fatalf("a full run with the horizon: %+v %v; %d messages, then %d", again, err, all, after)
	}
}

// With no verifier configured the item says it waits for the download; the
// stage downloads it under the per-Mac lock (the Settings switch is in the
// catalog, which the broker does not read), then reads. The broker fetches
// it for a library whose configuration turns agent events on, labels or not.
func TestAgentEventsDownloadTheVerifier(t *testing.T) {
	useSupportDir(t)
	catalog, config := testCatalog(t)
	seedLabelingCatalog(t, catalog)
	labels, settings := agentEventsTestSettings(t)
	entries := []tarEntry{}
	for _, name := range []string{"manifest.json", "tokenizer.json", "model.int8.onnx"} {
		data, err := os.ReadFile(filepath.Join(settings.ModelDir, name))
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, tarEntry{name: name, body: string(data)})
	}
	archive := testArchive(t, entries...)
	server := newAssetServer(t, archive)
	override(t, &labelsAssets, labelsAssetSet{AgentEventsModel: server.asset(archive, "")})
	config.Labels, config.AgentEvents = labels, settings
	config.Labels.Enabled, config.AgentEvents.ModelDir = false, ""
	srv := testServer(t, config, catalog)
	if sub := itemSub(backgroundItem{ID: "agent_events"}, srv.backgroundInputsFor(t.Context(), nil)); !strings.Contains(sub, "the agent events model: not yet downloaded") {
		t.Fatalf("the item says %q", sub)
	}
	if status, err := catalog.agentEventsStatus(t.Context(), config); err != nil || !strings.Contains(fmt.Sprint(status["waiting"]), "not yet downloaded") {
		t.Fatalf("status: %v %v", status, err)
	}
	if err := catalog.writeTransaction(t.Context(), "test", markSyncDeferred); err != nil {
		t.Fatal(err)
	}
	if err := srv.finishDeferredStages(t.Context(), findingsAuto, map[string]bool{"agent_events": true}, nil); err != nil {
		t.Fatal(err)
	}
	var read int
	_ = catalog.DB.QueryRow(`SELECT COUNT(*) FROM agent_event_messages`).Scan(&read)
	if !labelsAssets.AgentEventsModel.present() || read == 0 {
		t.Fatalf("the stage downloaded the verifier: %v; read %d messages", labelsAssets.AgentEventsModel.present(), read)
	}

	// The broker, for a library configured on, on a Mac without the verifier.
	if err := os.RemoveAll(labelsAssets.AgentEventsModel.dir()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	m := &applicationManager{ctx: ctx, client: &http.Client{Timeout: 5 * time.Second}, operations: newBrokerOperations(filepath.Join(t.TempDir(), "operations.json"))}
	m.startLabelsFetch(config.Labels, AgentEventSettings{})
	if len(m.operations.reg.views()) != 0 {
		t.Fatal("a download started for a library with neither labels nor agent events")
	}
	m.startLabelsFetch(config.Labels, config.AgentEvents)
	if !waitFor(10*time.Second, func() {
		for !labelsAssets.AgentEventsModel.present() {
			time.Sleep(10 * time.Millisecond)
		}
	}) {
		t.Fatal("the broker never fetched the verifier")
	}
}

// A stop kills the verifier's workers instead of keeping them warm, so
// batches queued at them do not run on; the next run resumes.
func TestAgentEventsStopKillsTheWorkers(t *testing.T) {
	catalog := agentEventsCatalog(t)
	labels, settings := agentEventsTestSettings(t)
	t.Setenv(labelertest.FakeWorkerEnv, "slow")
	override(t, &agentEventsChunkRows, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := catalog.RunAgentEvents(ctx, labels, settings, AgentEventsRunOptions{})
		done <- err
	}()
	var kept int
	waitUntil(t, 5*time.Second, "agent events to save a conversation", func() bool {
		return catalog.reader().QueryRow(`SELECT COUNT(*) FROM agent_event_messages`).Scan(&kept) == nil && kept > 0
	})
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped run: %v", err)
	}
	if _, _, runners := catalog.labelerManager(labels).Usage(); runners != 0 {
		t.Fatalf("%d runners kept after a stop", runners)
	}
	if err := catalog.reader().QueryRow(`SELECT COUNT(*) FROM agent_event_messages`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	t.Setenv(labelertest.FakeWorkerEnv, "ok")
	rest, err := catalog.RunAgentEvents(t.Context(), labels, settings, AgentEventsRunOptions{})
	if err != nil || !rest.Complete {
		t.Fatalf("the run after a stop: %+v %v", rest, err)
	}
	var total int
	if err := catalog.reader().QueryRow(`SELECT COUNT(*) FROM agent_event_messages`).Scan(&total); err != nil || rest.MessagesScored+kept != total {
		t.Fatalf("resumed run rescored saved messages: %d kept + %d scored, %d total (%v)", kept, rest.MessagesScored, total, err)
	}
	if _, _, runners := catalog.labelerManager(labels).Usage(); runners != 1 {
		t.Fatalf("a finished run kept %d runners warm, want 1", runners)
	}
}

// Exercise scheduling with handshakes rather than model speeds: the second
// conversation must be prepared during the first inference, and scored while
// the first save is still blocked. The ledger/model tests above cover outputs.
func TestAgentEventsPipelineOverlapsStages(t *testing.T) {
	for _, rows := range []int{1, 20} {
		t.Run(fmt.Sprintf("rows=%d", rows), func(t *testing.T) {
			override(t, &agentEventsChunkRows, 3)
			var committed atomic.Int64
			ctx, cancel := context.WithTimeout(withWorkProgress(t.Context(), func(w activityWork) { committed.Store(int64(w.Done)) }), 5*time.Second)
			defer cancel()
			inferring, prepared := make(chan struct{}), make(chan struct{})
			writing, scoredDuringWrite := make(chan struct{}), make(chan struct{})
			releaseWrite := make(chan struct{})
			jobs := []*agentEventsConversation{{id: "first"}, {id: "second"}, {id: "third"}}
			stats := &AgentEventsRunStats{}
			pipeline := agentEventsPipeline{
				stats: stats, progress: func(string, ...any) {},
				build: func(_ context.Context, _ int, job *agentEventsConversation) (*agentEventsConversation, error) {
					if job.id == "second" {
						select {
						case <-inferring:
						case <-ctx.Done():
							return nil, ctx.Err()
						}
						close(prepared)
					}
					job.messages = []*agentEventsMessage{{rows: make([]labelrt.Framed, rows)}}
					for i := range job.messages[0].rows {
						job.messages[0].rows[i] = labelrt.Framed{IDs: []int32{1, 2}, Extra: map[string][]labelrt.Span{labelrt.SegmentSpans: {{Start: 0, End: 2}}}}
					}
					return job, nil
				},
				infer: func(conversations []*agentEventsConversation) error {
					switch conversations[0].id {
					case "first":
						close(inferring)
						select {
						case <-prepared:
						case <-ctx.Done():
							return ctx.Err()
						}
					case "second":
						select {
						case <-writing:
						case <-ctx.Done():
							return ctx.Err()
						}
						close(scoredDuringWrite)
					}
					for _, conversation := range conversations {
						conversation.messages[0].hash = "scored"
					}
					return nil
				},
				store: func(conversation *agentEventsConversation, written *AgentEventsRunStats) error {
					for _, row := range conversation.messages[0].rows {
						if row.IDs != nil || len(row.Extra[labelrt.SegmentSpans]) == 0 {
							return errors.New("save retained token IDs or lost sentence spans")
						}
					}
					if conversation.messages[0].hash != "scored" {
						return errors.New("saved an unscored conversation")
					}
					if conversation.id == "first" {
						close(writing)
						select {
						case <-releaseWrite:
						case <-ctx.Done():
							return ctx.Err()
						}
					}
					written.Stored++
					return nil
				},
			}
			done := make(chan error, 1)
			go func() { done <- pipeline.run(ctx, cancel, jobs) }()
			select {
			case <-scoredDuringWrite:
				if committed.Load() != 0 {
					t.Error("progress advanced before the first save committed")
				}
				close(releaseWrite)
			case err := <-done:
				t.Fatalf("pipeline ended before overlapping stages: %v", err)
			case <-ctx.Done():
				t.Fatal("preparation, inference and persistence did not overlap")
			}
			if err := <-done; err != nil || stats.ConversationsBuilt != 3 || stats.Candidates != 3*rows || stats.Stored != 3 {
				t.Fatalf("overlapping stages: %+v %v", stats, err)
			}
			if committed.Load() != 3 {
				t.Fatalf("committed progress = %d, want 3", committed.Load())
			}
		})
	}
}

func TestAgentEventsPipelineBoundsPreparedWork(t *testing.T) {
	override(t, &agentEventsChunkRows, 4)
	for _, rows := range []int{2, 20} {
		t.Run(fmt.Sprintf("rows=%d", rows), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var retained, peak atomic.Int32
			jobs := make([]*agentEventsConversation, 24)
			for i := range jobs {
				jobs[i] = &agentEventsConversation{id: strconv.Itoa(i)}
			}
			stats := &AgentEventsRunStats{}
			pipeline := agentEventsPipeline{
				stats: stats, workers: 3, progress: func(string, ...any) {},
				build: func(_ context.Context, _ int, job *agentEventsConversation) (*agentEventsConversation, error) {
					n := retained.Add(1)
					for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
					}
					job.messages = []*agentEventsMessage{{rows: make([]labelrt.Framed, rows)}}
					return job, nil
				},
				infer: func([]*agentEventsConversation) error { return nil },
				store: func(_ *agentEventsConversation, written *AgentEventsRunStats) error {
					retained.Add(-1)
					written.Stored++
					return nil
				},
			}
			if err := pipeline.run(ctx, cancel, jobs); err != nil || stats.Stored != len(jobs) {
				t.Fatalf("bounded pipeline: %+v %v", stats, err)
			}
			for _, job := range jobs {
				if len(job.messages) != 0 {
					t.Fatal("the backlog work list retained prepared message rows")
				}
			}
			// Three unscored windows, three conversations being built, and
			// three scored windows at the bounded writer. Each scored
			// window can retain one oversized conversation, without tokens.
			perWindow := max(1, (4+1+rows)/(2+rows))
			limit := 12/min(2+rows, 12) + 3 + 3*perWindow
			if got := peak.Load(); got > int32(limit) || retained.Load() != 0 {
				t.Fatalf("retained %d conversations (peak %d), limit %d", retained.Load(), got, limit)
			}
		})
	}
}

func TestAgentEventsPipelineFailureCancelsOtherStages(t *testing.T) {
	override(t, &agentEventsChunkRows, 3)
	for _, stage := range []string{"inference", "inference while saving", "writer", "writer panic", "stop", "stop rollback"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			failure := errors.New("injected " + stage + " failure")
			saving := make(chan struct{})
			jobs := make([]*agentEventsConversation, 12)
			for i := range jobs {
				jobs[i] = &agentEventsConversation{id: strconv.Itoa(i)}
			}
			pipeline := agentEventsPipeline{
				stats: &AgentEventsRunStats{}, workers: 3, progress: func(string, ...any) {},
				build: func(_ context.Context, _ int, job *agentEventsConversation) (*agentEventsConversation, error) {
					job.messages = []*agentEventsMessage{{rows: make([]labelrt.Framed, 1)}}
					return job, nil
				},
				infer: func(conversations []*agentEventsConversation) error {
					if stage == "inference" {
						return failure
					}
					if stage == "inference while saving" && conversations[0].id != "0" {
						select {
						case <-saving:
							return failure
						case <-ctx.Done():
							return ctx.Err()
						}
					}
					return ctx.Err()
				},
				store: func(*agentEventsConversation, *AgentEventsRunStats) error {
					if stage == "inference while saving" {
						close(saving)
						<-ctx.Done()
						return sql.ErrTxDone
					}
					if stage == "writer panic" {
						panic(failure)
					}
					if stage == "stop" || stage == "stop rollback" {
						cancel()
						if stage == "stop rollback" {
							return sql.ErrTxDone
						}
						return ctx.Err()
					}
					return failure
				},
			}
			want := failure
			if stage == "stop" || stage == "stop rollback" {
				want = context.Canceled
			}
			err := pipeline.run(ctx, cancel, jobs)
			if stage == "writer panic" && err != nil && strings.Contains(err.Error(), failure.Error()) {
				return
			}
			if !errors.Is(err, want) {
				t.Fatalf("%s returned %v, want %v", stage, err, want)
			}
		})
	}
}

func TestAgentEventsFailedSaveRollsBackAndResumes(t *testing.T) {
	catalog := agentEventsCatalog(t)
	labels, settings := agentEventsTestSettings(t)
	if err := catalog.ensureAgentEventsLedger(t.Context()); err != nil {
		t.Fatal(err)
	}
	execTx(t, catalog.DB, `CREATE TRIGGER reject_agent_events BEFORE INSERT ON agent_events_conversations BEGIN
		SELECT RAISE(ABORT,'injected save failure'); END`)
	opts := AgentEventsRunOptions{Conversations: []string{"c-events"}}
	stats, err := catalog.RunAgentEvents(t.Context(), labels, settings, opts)
	if err == nil || !strings.Contains(err.Error(), "injected save failure") || stats.Stored != 0 || stats.Events != 0 || stats.progress.conversations != 0 || stats.progress.messages != 0 {
		t.Fatalf("failed save: %+v %v", stats, err)
	}
	for _, table := range []string{"agent_events", "agent_event_messages", "agent_events_conversations"} {
		var n int
		if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE conversation_id='c-events'`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("failed save left %d rows in %s (%v)", n, table, err)
		}
	}
	execTx(t, catalog.DB, `DROP TRIGGER reject_agent_events`)
	stats, err = catalog.RunAgentEvents(t.Context(), labels, settings, opts)
	if err != nil || stats.MessagesScored == 0 || stats.Stored == 0 {
		t.Fatalf("resuming after failed save: %+v %v", stats, err)
	}
}

func TestAgentEventsPipelineUsesOperationIOPriority(t *testing.T) {
	catalog := agentEventsCatalog(t)
	labels, settings := agentEventsTestSettings(t)
	server := testServer(t, Config{}, catalog)
	var wrapped atomic.Int32
	override(t, &utilityIO, func(_ *Op, work func()) { wrapped.Add(1); work() })
	for _, priority := range []string{priorityBackground, priorityForeground} {
		wrapped.Store(0)
		ctx, op := server.begin(t.Context(), OperationSpec{Kind: "analysis.agent_events", Priority: priority, Trigger: triggerUser})
		stats, err := catalog.RunAgentEvents(ctx, labels, settings, AgentEventsRunOptions{Full: true})
		op.Finish(err)
		if err != nil || !stats.Complete {
			t.Fatalf("%s run: %+v %v", priority, stats, err)
		}
		if got := wrapped.Load() > 0; got != (priority == priorityBackground) {
			t.Fatalf("%s pipeline ran at utility IO: %v", priority, got)
		}
	}
}

func TestAgentEventsParallelPreparationMatchesSerial(t *testing.T) {
	labels, settings := agentEventsTestSettings(t)
	var wantMessages, wantEvents []map[string]any
	for _, threads := range []int{1, 4} {
		catalog := agentEventsCatalog(t)
		labels.Threads = threads
		stats, err := catalog.RunAgentEvents(t.Context(), labels, settings, AgentEventsRunOptions{})
		if err != nil || !stats.Complete || stats.ConversationsFailed != 0 {
			t.Fatalf("threads=%d: %+v %v", threads, stats, err)
		}
		messages, err := queryMaps(catalog.DB, `SELECT message_id,conversation_id,source,occurred_at,day,candidates,events,input_hash FROM agent_event_messages ORDER BY message_id`)
		if err != nil {
			t.Fatal(err)
		}
		events, err := queryMaps(catalog.DB, `SELECT message_id,family,probability,sentence_start,sentence_end,sentence,subject,response,subject_ranges,response_ranges FROM agent_events ORDER BY message_id,family,sentence_start`)
		if err != nil {
			t.Fatal(err)
		}
		if threads == 1 {
			wantMessages, wantEvents = messages, events
		} else if len(messages) == 0 || !reflect.DeepEqual(messages, wantMessages) || !reflect.DeepEqual(events, wantEvents) {
			t.Fatalf("parallel preparation changed messages or events:\n%v\n%v", messages, events)
		}
	}
}
