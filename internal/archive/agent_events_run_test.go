package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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

	// An edited reply is scored again, alone; a deleted one loses its rows.
	if _, err := catalog.DB.Exec(`UPDATE messages SET text=? WHERE id='m-ev-reply'`, "That workaround didn't help either."); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.DB.Exec(`DELETE FROM messages WHERE id='m-ev-think'`); err != nil {
		t.Fatal(err)
	}
	edited, err := catalog.RunAgentEvents(t.Context(), labels, settings, AgentEventsRunOptions{})
	if err != nil || edited.ConversationsBuilt != 1 || edited.MessagesScored != 1 || edited.MessagesRemoved != 1 {
		t.Fatalf("after an edit: %+v %v", edited, err)
	}
	if got := agentEventsStored(t, catalog, "m-ev-think"); len(got) != 0 {
		t.Fatalf("a deleted message kept events %v", got)
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
	switched, err := catalog.RunAgentEvents(t.Context(), labels, other, AgentEventsRunOptions{})
	if err != nil || switched.MessagesScored != read-1 || switched.MessagesUnchanged != 0 {
		t.Fatalf("after a model switch: %+v %v (%d messages)", switched, err, read-1)
	}
}

func TestAgentEventsRunsInLimitedSteps(t *testing.T) {
	catalog := agentEventsCatalog(t)
	labels, settings := agentEventsTestSettings(t)
	first, err := catalog.RunAgentEvents(t.Context(), labels, settings, AgentEventsRunOptions{Limit: 1})
	if err != nil || !first.Cut || first.Complete {
		t.Fatalf("a limited run: %+v %v", first, err)
	}
	// The newest conversation comes first.
	var newest int
	if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM agent_event_messages WHERE conversation_id='c-events'`).Scan(&newest); err != nil || newest == 0 {
		t.Fatalf("the limited run skipped the newest conversation (%v)", err)
	}
	rest, err := catalog.RunAgentEvents(t.Context(), labels, settings, AgentEventsRunOptions{})
	if err != nil || !rest.Complete || rest.MessagesScored == 0 {
		t.Fatalf("the rest: %+v %v", rest, err)
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
	for _, enabled := range []bool{false, true} {
		catalog, config := testCatalog(t)
		seedLabelingCatalog(t, catalog)
		config.Labels, config.AgentEvents = agentEventsTestSettings(t)
		config.Labels.Enabled, config.AgentEvents.Enabled = false, enabled
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
				t.Fatalf("with agent events off: %d messages read, item planned %v", read, planned)
			}
			continue
		}
		if read == 0 || !planned {
			t.Fatalf("with agent events on: %d messages read, item planned %v", read, planned)
		}
		deferred, _ := catalog.freshnessDeferred(t.Context())
		if row := deferred["agent_events"]; row.generation == 0 || row.generation > row.completed {
			t.Fatalf("the agent events stage did not complete: %+v", row)
		}
	}
}
