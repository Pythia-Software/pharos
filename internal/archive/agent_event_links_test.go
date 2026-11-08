package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func linkTarget(t *testing.T, messages []eventLinkMessage, e eventLinkEvent) *agentEventTarget {
	t.Helper()
	if e.ID == "" {
		e.ID = "e1"
	}
	return findAgentEventTargets(messages, []eventLinkEvent{e})[e.ID]
}

// A quote prefers the person's own words over an agent's earlier copy of
// them, even when the copy is nearer; offsets are into the target's text.
func TestAgentEventTargetsPreferThePersonsWords(t *testing.T) {
	prompt := "Please build one large stacked PR for the importer."
	messages := []eventLinkMessage{
		{ID: "p", Role: "user", Kind: "message", Text: prompt},
		{ID: "r", Role: "assistant", Kind: "message", Text: `You asked for "one large stacked PR"; starting now.`},
		{ID: "a", Role: "assistant", Kind: "thinking", Text: "x"},
	}
	got := linkTarget(t, messages, eventLinkEvent{MessageID: "a", Family: "ambiguous_request", Sentence: `The phrase "One large stacked PR" could mean two things.`})
	if got == nil || got.Method != "quote" || got.MessageID != "p" || !got.Strong || prompt[got.Start:got.End] != "one large stacked PR" {
		t.Fatalf("target = %+v", got)
	}
	// Without the prompt's words, the nearest earlier message wins; one
	// quoted word in agent text is weak.
	messages[0].Text = "Please build it."
	messages = append(messages[:2], eventLinkMessage{ID: "r2", Role: "assistant", Kind: "message", Text: "the importer is slow"}, messages[2])
	got = linkTarget(t, messages, eventLinkEvent{MessageID: "a", Family: "surprise", Sentence: `I expected "importer" to be fast.`})
	if got == nil || got.MessageID != "r2" || got.Strong {
		t.Fatalf("nearest = %+v", got)
	}
	// Later messages are never targets.
	got = linkTarget(t, messages, eventLinkEvent{MessageID: "p", Family: "surprise", Sentence: `I expected "importer" to be fast.`})
	if got != nil {
		t.Fatalf("a target after the event: %+v", got)
	}
}

// A code quote is an identifier: it only matches the person's prompts, not
// an agent's earlier text that used it.
func TestAgentEventTargetsCodeQuotesMatchOnlyPrompts(t *testing.T) {
	messages := []eventLinkMessage{
		{ID: "p", Role: "user", Kind: "message", Text: "Fix the sync."},
		{ID: "r", Role: "assistant", Kind: "message", Text: "I changed runSyncLoop to retry."},
		{ID: "a", Role: "assistant", Kind: "message", Text: "x"},
	}
	e := eventLinkEvent{MessageID: "a", Family: "own_mistake", Sentence: "I broke `runSyncLoop` earlier."}
	if got := linkTarget(t, messages, e); got != nil {
		t.Fatalf("code quote matched agent text: %+v", got)
	}
	messages[0].Text = "Fix the sync in runSyncLoop please."
	if got := linkTarget(t, messages, e); got == nil || got.MessageID != "p" || got.Method != "quote" {
		t.Fatalf("code quote of the prompt = %+v", got)
	}
}

// Without a quote, an event links to the sentence of its turn's prompt
// that shares enough specific words; too little overlap is no target.
// Harness wrappers are not the person's words.
func TestAgentEventTargetsOverlapThreshold(t *testing.T) {
	prompt := "<system-reminder>Mobile users gesture with two fingers on the canvas.</system-reminder>\nKeep the toolbar small. " +
		"Mobile users can zoom and rotate the canvas with two fingers, so hide those buttons on phones. Ship it today."
	messages := []eventLinkMessage{
		{ID: "old", Role: "user", Kind: "message", Text: "Earlier prompt about the database schema migration. Add an index. Keep the old column.\nRun the backfill overnight. Report the timings. Thanks!"},
		{ID: "p", Role: "user", Kind: "message", Text: prompt},
		{ID: "a", Role: "assistant", Kind: "thinking", Text: "x"},
	}
	e := eventLinkEvent{MessageID: "a", Family: "assumption", Sentence: "I'm assuming the user wants rotate and zoom gestures with fingers hidden on phones."}
	got := linkTarget(t, messages, e)
	want := "Mobile users can zoom and rotate the canvas with two fingers, so hide those buttons on phones."
	if got == nil || got.Method != "overlap" || got.MessageID != "p" || got.Text != want || prompt[got.Start:got.End] != want || len(got.Shared) < 2 || got.Score < agentEventOverlapScore {
		t.Fatalf("overlap = %+v", got)
	}
	// An assumption about the code, not the request, does not link by overlap.
	e.Sentence = "Assuming rotate and zoom gestures with fingers hidden on phones."
	if got := linkTarget(t, messages, e); got != nil {
		t.Fatalf("assumption about the code linked: %+v", got)
	}
	// One shared word is not enough.
	e = eventLinkEvent{MessageID: "a", Family: "confusion", Sentence: "The toolbar layout confuses me."}
	if got := linkTarget(t, messages, e); got != nil {
		t.Fatalf("one shared word linked: %+v", got)
	}
}

// A stale-guidance event that names a file has a file target; other events
// without a quote or overlap have none.
func TestAgentEventTargetsFilesAndNone(t *testing.T) {
	messages := []eventLinkMessage{{ID: "p", Role: "user", Kind: "message", Text: "Go."}, {ID: "a", Role: "assistant", Kind: "message", Text: "x"}}
	got := linkTarget(t, messages, eventLinkEvent{MessageID: "a", Family: "stale_guidance", Sentence: "The docs/releases.md and CLAUDE.md steps are out of date."})
	if got == nil || got.Method != "file" || !reflect.DeepEqual(got.Files, []string{"CLAUDE.md", "releases.md"}) {
		t.Fatalf("file = %+v", got)
	}
	if got := linkTarget(t, messages, eventLinkEvent{MessageID: "a", Family: "obstacle", Sentence: `The "Go." command failed.`}); got != nil {
		t.Fatalf("one-sided family linked: %+v", got)
	}
	if got := linkTarget(t, messages, eventLinkEvent{MessageID: "a", Family: "discovery", Sentence: "Nothing here is quoted."}); got != nil {
		t.Fatalf("no target = %+v", got)
	}
}

func TestAgentEventQuotesSkipApostrophesAndShortWords(t *testing.T) {
	got := []string{}
	for _, q := range agentEventQuotes(`I don't think it's right: 'stale index' and "ok" and “the drawer only”.`) {
		got = append(got, q.text)
	}
	if !reflect.DeepEqual(got, []string{"stale index", "the drawer only"}) {
		t.Fatalf("quotes = %q", got)
	}
}

// The reader and the fingerprint carry each event's target: the reader in
// the message's agent_events (UTF-16 offsets), the fingerprint as a list.
func TestAgentEventTargetsServedToViews(t *testing.T) {
	catalog, config := testCatalog(t)
	prompt := "Make the “drawer only” removal flow work on phones."
	reply := "I’ll read the phrase \"drawer only\" as the bottom sheet."
	execTx(t, catalog.DB,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','codex','local','w','Work','2026-10-21T00:00:00.000Z')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','claude','local','c')`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash,source_order) VALUES('m1','c','m1','user','message','`+prompt+`','2026-10-21T15:00:00.000Z','h1',1)`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash,source_order) VALUES('m2','c','m2','assistant','message','`+strings.ReplaceAll(reply, "'", "''")+`','2026-10-21T15:01:00.000Z','h2',2)`,
		`INSERT INTO agent_event_messages(message_id,conversation_id,source,candidates,events,model,lexicon,imported_at) VALUES('m2','c','reply',1,1,'t','t','2026-10-21T15:02:00.000Z')`,
		`INSERT INTO agent_events(id,message_id,conversation_id,source,family,probability,sentence_start,sentence_end,sentence,subject,response,subject_ranges,response_ranges,model)
		 VALUES(7,'m2','c','reply','assumption',0.9,0,`+strconv.Itoa(len(reply))+`,'`+strings.ReplaceAll(reply, "'", "''")+`','','','[]','[]','t')`,
	)
	conversation, err := catalog.WorkConversation("w", "c")
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	for _, m := range conversation["messages"].([]map[string]any) {
		if events, ok := m["agent_events"].([]map[string]any); ok {
			event = events[0]
		}
	}
	target, _ := event["target"].(map[string]any)
	if event["id"] != "e7" || target == nil || target["message_id"] != "m1" || target["method"] != "quote" || target["start"] != 10 || target["end"] != 21 {
		t.Fatalf("reader event = %#v", event)
	}
	srv := testServer(t, config, catalog)
	w := apiCall(t, srv, config.APIToken, http.MethodGet, "/api/labels/conversations/c", nil)
	var body map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatalf("fingerprint: %d %s", w.Code, w.Body.String())
	}
	events, _ := body["agent_events"].([]any)
	if len(events) != 1 || events[0].(map[string]any)["target"].(map[string]any)["message_id"] != "m1" {
		t.Fatalf("fingerprint events = %#v", body["agent_events"])
	}
	// Positions are byte shares of each message, so the timeline can place
	// an event, and its arc's target, inside the message's span.
	served := events[0].(map[string]any)
	quote := float64(strings.Index(prompt, "drawer only")) / float64(len(prompt))
	if served["position"] != 0.0 || math.Abs(served["target"].(map[string]any)["position"].(float64)-quote) > 1e-4 {
		t.Fatalf("positions = %v, target %v; want 0 and %.4f", served["position"], served["target"].(map[string]any)["position"], quote)
	}
	// The sentence's byte range, by which views merge near-identical marks.
	if span, _ := served["span"].([]any); len(span) != 2 || span[0] != 0.0 || span[1] != float64(len(reply)) {
		t.Fatalf("span = %#v, want [0 %d]", served["span"], len(reply))
	}
	// Workspace ribbons resolve each conversation separately and only return
	// events on selected messages in the retained fingerprint sample.
	execTx(t, catalog.DB,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('sibling','w','claude','local','sibling')`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash,source_order,selected) VALUES
		 ('m3','sibling','m3','user','message','Keep the drawer only option.','2026-10-21T16:00:00.000Z','h3',1,1),
		 ('m4','sibling','m4','assistant','message','I read "drawer only" as the bottom sheet.','2026-10-21T16:01:00.000Z','h4',2,1),
		 ('m5','sibling','m5','assistant','message','Excluded event.','2026-10-21T16:02:00.000Z','h5',3,0)`,
		`INSERT INTO agent_events(id,message_id,conversation_id,source,family,probability,sentence_start,sentence_end,sentence,subject,response,subject_ranges,response_ranges,model) VALUES
		 (8,'m4','sibling','reply','assumption',0.9,0,40,'I read "drawer only" as the bottom sheet.','','','[]','[]','t'),
		 (9,'m5','sibling','reply','discovery',0.9,0,15,'Excluded event.','','','[]','[]','t')`,
	)
	readFingerprint := func(path string) map[string]any {
		t.Helper()
		w := apiCall(t, srv, config.APIToken, http.MethodGet, path, nil)
		var body map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("fingerprint: %d %s", w.Code, w.Body.String())
		}
		return body
	}
	workspace := readFingerprint("/api/labels/workspaces/w")
	events = workspace["agent_events"].([]any)
	if len(events) != 2 || events[0].(map[string]any)["id"] != "e7" || events[1].(map[string]any)["id"] != "e8" || events[1].(map[string]any)["target"].(map[string]any)["message_id"] != "m3" {
		t.Fatalf("workspace events lost conversation ownership or exposed excluded evidence: %v", events)
	}
	// Restricting events must not restrict the earlier text used for targets.
	sample, err := catalog.agentEventLinksForMessages(t.Context(), "c", []string{"m2"})
	if err != nil || len(sample) != 1 || sample[0]["target"].(map[string]any)["message_id"] != "m1" {
		t.Fatalf("sample lost its earlier target: %v %v", sample, err)
	}
	if sample, err := catalog.agentEventLinksForMessages(t.Context(), "c", []string{}); err != nil || len(sample) != 0 {
		t.Fatalf("empty sample returned events: %v %v", sample, err)
	}
	override(t, &messageLabelFingerprintLimit, 2)
	workspace = readFingerprint("/api/labels/workspaces/w")
	if events := workspace["agent_events"].([]any); workspace["truncated"] != true || len(events) != 1 || events[0].(map[string]any)["id"] != "e7" {
		t.Fatalf("workspace events exceed the message sample: %v", workspace)
	}
	override(t, &messageLabelFingerprintLimit, 1)
	if conversation := readFingerprint("/api/labels/conversations/c"); len(conversation["agent_events"].([]any)) != 0 {
		t.Fatalf("conversation events exceed the message sample: %v", conversation)
	}
}

// A ribbon never draws events resting on superseded execution evidence as
// current: they are left out, exactly as the Agent Experience datasets leave
// them out, and events_pending says so apart from label freshness, until the
// ordinary events run republishes them.
func TestRibbonEventsFollowAgentEventsFreshness(t *testing.T) {
	ctx := t.Context()
	c := agentEventsCatalog(t)
	labels, settings := agentEventsTestSettings(t)
	config := testConfig(t)
	config.CatalogPath = c.Path
	srv := testServer(t, config, c)
	run := func() {
		t.Helper()
		if stats, err := c.RunLabels(ctx, labels, LabelsRunOptions{}); err != nil || !stats.Complete {
			t.Fatalf("labels: %+v %v", stats, err)
		}
		if stats, err := c.RunAgentEvents(ctx, labels, settings, AgentEventsRunOptions{}); err != nil || !stats.Complete {
			t.Fatalf("agent events: %+v %v", stats, err)
		}
	}
	read := func(path string) (events int, eventsPending float64, labelsPending any) {
		t.Helper()
		w := apiCall(t, srv, config.APIToken, http.MethodGet, path, nil)
		var body map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("ribbon %s: %d %s", path, w.Code, w.Body.String())
		}
		for _, event := range body["agent_events"].([]any) {
			if strings.HasPrefix(firstString(event.(map[string]any)["message_id"]), "m-ev-") {
				events++
			}
		}
		pending, _ := body["events_pending"].(float64)
		return events, pending, body["pending"]
	}
	pendingIDs := func(path string) (ids []string) {
		t.Helper()
		var body struct {
			Pending []string `json:"events_pending_conversations"`
		}
		if w := apiCall(t, srv, config.APIToken, http.MethodGet, path, nil); json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatal(w.Body.String())
		}
		return body.Pending
	}
	// The events ledger is created by the first run, after the catalog opened.
	if countRows(t, c, `SELECT COUNT(*) FROM sqlite_master WHERE name='agent_events_conversations'`) != 0 {
		t.Fatal("precondition: the events ledger exists before the first run")
	}
	run()
	for _, path := range []string{"/api/labels/conversations/c-events", "/api/labels/workspaces/ws-main"} {
		if events, pending, _ := read(path); events == 0 || pending != 0 {
			t.Fatalf("%s current: events=%d events_pending=%v", path, events, pending)
		}
	}
	built, _, _ := read("/api/labels/conversations/c-events")
	// The cached ribbon image follows the same freshness, for both
	// appearances: with events (mask 1) and event-hidden (mask 2), whose
	// labels follow it too. drawn records what each drawing was given.
	renderer, err := newFingerprintRenderer()
	if err != nil {
		t.Fatal(err)
	}
	type drawing struct{ mask, events, labeled int }
	var drawn []drawing
	c.fingerprints.renderer = fingerprintRenderFunc(func(ctx context.Context, input any, mask int) (fingerprintRendered, error) {
		d := drawing{mask: mask}
		for _, event := range input.(map[string]any)["events"].([]map[string]any) {
			if strings.HasPrefix(firstString(event["message_id"]), "m-ev-") {
				d.events++
			}
		}
		for _, message := range input.(map[string]any)["conversation"].(map[string]any)["messages"].([]map[string]any) {
			// Pending labels are drawn as no labelled chunks.
			if integer(message["chunks"]) > 0 {
				d.labeled++
			}
		}
		drawn = append(drawn, d)
		return renderer.Render(ctx, input, mask)
	})
	image := func(label string, events bool) fingerprintMetadata {
		t.Helper()
		m, err := c.fingerprintMetadata(ctx, fingerprintRequest{"conversation", "c-events", "wide"}, events)
		if err != nil {
			t.Fatal(label, err)
		}
		return m
	}
	ready := func(label string, hidden, shown bool) {
		t.Helper()
		if h, e := image(label, false), image(label, true); (h.State == "ready") != hidden || (e.State == "ready") != shown || h.State != "ready" && !h.Stale || e.State != "ready" && !e.Stale {
			t.Fatalf("%s: event-hidden %+v, with events %+v; want ready %v and %v, otherwise stale", label, h, e, hidden, shown)
		}
	}
	queued := func() int {
		return countRows(t, c, `SELECT COUNT(*) FROM fingerprint_entities WHERE entity='conversation' AND id='c-events' AND dirty=1`)
	}
	// Each unit draws one appearance, event-hidden first.
	execTx(t, c.DB, `UPDATE fingerprint_entities SET events_mask=3 WHERE entity='conversation' AND id='c-events'`)
	renderFingerprintTestEntity(t, c, "conversation", "c-events")
	renderFingerprintTestEntity(t, c, "conversation", "c-events")
	labeled := drawn[0].labeled
	ready("current", true, true)
	if want := []drawing{{2, built, labeled}, {1, built, labeled}}; labeled == 0 || !slices.Equal(drawn, want) || queued() != 0 {
		t.Fatalf("current drawings %v, want %v with labels; queued %d", drawn, want, queued())
	}
	// An execution change made and undone while a unit reads its input,
	// before any mark, is not drawn: the unit is retried and then draws the
	// unchanged labels and events.
	var parent sql.NullString
	var depth int
	if err := c.DB.QueryRow(`SELECT parent_id,agent_depth FROM conversations WHERE id='c-child'`).Scan(&parent, &depth); err != nil {
		t.Fatal(err)
	}
	override(t, &fingerprintInputReadHook, func() {
		execTx(t, c.DB, `UPDATE conversations SET parent_id='c-events',agent_depth=1 WHERE id='c-child'`)
		if _, err := c.DB.Exec(`UPDATE conversations SET parent_id=?,agent_depth=? WHERE id='c-child'`, parent, depth); err != nil {
			t.Fatal(err)
		}
	})
	execTx(t, c.DB, `UPDATE fingerprint_entities SET revision='requeued' WHERE entity='conversation' AND id='c-events'`)
	renderFingerprintTestEntity(t, c, "conversation", "c-events")
	if len(drawn) != 2 || queued() != 1 {
		t.Fatalf("a read that straddled an execution change was drawn: %v queued %d", drawn, queued())
	}
	// The retry backs off like any deferral, so reads are bounded while
	// unrelated execution writes go on.
	if backoff := countRows(t, c, `SELECT COUNT(*) FROM fingerprint_entities WHERE entity='conversation' AND id='c-events' AND attempts=1 AND next_attempt>unixepoch()`); backoff != 1 {
		t.Fatal("a straddled read was retried without backoff")
	}
	ready("straddled read", false, false)
	fingerprintInputReadHook = nil
	renderFingerprintTestEntity(t, c, "conversation", "c-events")
	renderFingerprintTestEntity(t, c, "conversation", "c-events")
	ready("after the undone change", true, true)
	if want := []drawing{{2, built, labeled}, {1, built, labeled}}; !slices.Equal(drawn[2:], want) {
		t.Fatalf("after the undone change: %v, want %v", drawn[2:], want)
	}
	drawn = nil
	// A writer that marks nothing moves c-child under c-events.
	if err := c.writeTransaction(ctx, "repair", func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE conversations SET parent_id='c-events',agent_depth=1 WHERE id='c-child'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/labels/conversations/c-events", "/api/labels/workspaces/ws-main"} {
		if events, pending, _ := read(path); events != 0 || pending == 0 {
			t.Fatalf("%s stale attribution drawn as current: events=%d events_pending=%v", path, events, pending)
		}
		if ids := pendingIDs(path); !slices.Contains(ids, "c-events") {
			t.Fatalf("%s pending conversations = %v", path, ids)
		}
	}
	// Before any mark both appearances keep their revision but are not
	// current (the labels too rest on the superseded evidence), and neither
	// is drawn.
	ready("before the mark", false, false)
	execTx(t, c.DB, `UPDATE fingerprint_entities SET revision='requeued' WHERE entity='conversation' AND id='c-events'`)
	renderFingerprintTestEntity(t, c, "conversation", "c-events")
	if len(drawn) != 0 || queued() != 1 {
		t.Fatalf("drawn on superseded evidence before the mark: %v queued %d", drawn, queued())
	}
	// Once marked, the labels are ordinary labeling backlog: the event-hidden
	// appearance is drawn without them and is current. The events appearance
	// is queued but not drawn without its events.
	if err := c.propagateExecutionChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if countRows(t, c, `SELECT COUNT(*) FROM agent_events_execution_stale WHERE conversation_id='c-events'`) != 1 {
		t.Fatal("propagation did not mark c-events")
	}
	renderFingerprintTestEntity(t, c, "conversation", "c-events")
	renderFingerprintTestEntity(t, c, "conversation", "c-events")
	ready("after the mark", true, false)
	if want := []drawing{{2, 0, 0}}; !slices.Equal(drawn, want) || queued() != 1 {
		t.Fatalf("after the mark: %v, want %v; queued %d", drawn, want, queued())
	}
	run()
	for _, path := range []string{"/api/labels/conversations/c-events", "/api/labels/workspaces/ws-main"} {
		if events, pending, labelsPending := read(path); events != built || pending != 0 || labelsPending == true {
			t.Fatalf("%s republished: events=%d (want %d) events_pending=%v labels pending=%v", path, events, built, pending, labelsPending)
		}
	}
	// The run that clears the mark requeues the image, drawn with them again.
	if dirty := countRows(t, c, `SELECT COUNT(*) FROM fingerprint_entities WHERE entity='conversation' AND id='c-events' AND dirty=1 AND attempts=0`); dirty != 1 {
		t.Fatal("clearing the mark did not requeue the image")
	}
	renderFingerprintTestEntity(t, c, "conversation", "c-events")
	renderFingerprintTestEntity(t, c, "conversation", "c-events")
	ready("republished", true, true)
	if want := []drawing{{2, 0, 0}, {2, built, labeled}, {1, built, labeled}}; !slices.Equal(drawn, want) || queued() != 0 {
		t.Fatalf("republished: %v, want %v; queued %d", drawn, want, queued())
	}
	// Ordinary labeling backlog never holds an image back: drawn without the
	// pending labels, current, and requeued again as labeling moves.
	execTx(t, c.DB, `INSERT INTO message_labels_dirty(conversation_id,token) VALUES('c-events','backlog')`)
	renderFingerprintTestEntity(t, c, "conversation", "c-events")
	renderFingerprintTestEntity(t, c, "conversation", "c-events")
	ready("labeling backlog", true, true)
	if want := []drawing{{2, built, 0}, {1, built, 0}}; !slices.Equal(drawn[3:], want) || queued() != 0 {
		t.Fatalf("labeling backlog: %v, want %v; queued %d", drawn[3:], want, queued())
	}
	// The ledger created during this session already requeues images: a
	// write to its build stamp alone (an executable that records no digest)
	// withholds the events and queues the image, without a reopen.
	execTx(t, c.DB, `UPDATE agent_events_conversations SET execution_digest='' WHERE conversation_id='c-events'`)
	if queued() != 1 {
		t.Fatal("a ledger-stamp write did not queue the image")
	}
	ready("ledger stamp", false, false)
}
