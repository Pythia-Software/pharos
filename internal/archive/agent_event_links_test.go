package archive

import (
	"encoding/json"
	"math"
	"net/http"
	"reflect"
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
}
