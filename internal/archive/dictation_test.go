package archive

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/querytable"
)

func TestTypedMarksNameWhatDictationNeverWrites(t *testing.T) {
	for _, test := range []struct {
		text string
		mark string
	}{
		{"Please check teh parser.", "typo"},
		{"please check the parser.", "lowercase_start"},
		{"It failed. then we stopped.", "lowercase_start"},
		{"Then i ran it again.", "lowercase_i"},
		{"Run `go test` again.", "code"},
		{"Look at writing_table.go again.", "code"},
		{"Call parseRows again.", "code"},
		{"Keep the reader/writer split.", "slash"},
		{"Add tests, e.g. for the parser.", "abbreviation"},
		{"Hmm, that failed again.", "interjection"},
		{"This is VERY wrong.", "caps"},
		{"It dont work at all.", "no_apostrophe"},
		{"Map the input -> output.", "symbol"},
		{"Do two things: 1) fix the parser 2) add a test.", "inline_list"},
		{"Please check the parser", "no_end_punctuation"},
	} {
		marks := typedMarks(test.text, nil)
		if !slices.Contains(marks, test.mark) {
			t.Errorf("%q: marks %v, want %s", test.text, marks, test.mark)
		}
	}
	for _, text := range []string{
		"Please check the parser, and tell me what happened.",
		"I think it's fine. Can you check the ZXQ output?",
		"Ask Tabel about the release!",
		"Read the notes (the second part) first.",
	} {
		if marks := typedMarks(text, nil); len(marks) > 0 {
			t.Errorf("%q: unexpected marks %v", text, marks)
		}
	}
}

func TestTyposSpareTheLibrarysOwnWords(t *testing.T) {
	counts := func(words map[string]int64) *spellingCounts { return &spellingCounts{cache: words} }
	for _, test := range []struct {
		text     string
		spelling *spellingCounts
		want     []string
	}{
		{"Fix the tabel now.", nil, []string{"tabel"}},
		{"Fix the tabel now.", counts(map[string]int64{"tabel": 1, "table": 40}), []string{"tabel"}},
		// A word the library uses nearly as often as its English neighbour is
		// a name or term of its own.
		{"Fix the tabel now.", counts(map[string]int64{"tabel": 30, "table": 40}), nil},
		{"Put it onthe shelf.", nil, []string{"onthe"}},
		{"Put it onthe shelf.", counts(map[string]int64{"onthe": 60}), nil},
		// As in the measured rules, a word ending a sentence is not checked.
		{"Fix the tabel.", nil, nil},
		// Capitalised words are names, unless they open a sentence.
		{"Ask Tabel.", nil, nil},
		{"Teh parser broke.\nTeh fix works.", nil, []string{"teh"}},
		{"The tables, parsed and checked, worked.", nil, nil},
	} {
		if got := typos(test.text, test.spelling); !slices.Equal(got, test.want) && len(got)+len(test.want) > 0 {
			t.Errorf("%q: typos %v, want %v", test.text, got, test.want)
		}
	}
}

func TestEstimateDictationWeighsMarksByLength(t *testing.T) {
	clean := func(words int) string {
		return strings.TrimSpace(strings.Repeat("Please check the parser now. ", words/5))
	}
	whole := func(text string) []authorSpan { return []authorSpan{{Category: spanTyped, Start: 0, End: len(text)}} }
	estimate := func(text string, voice bool) dictationEstimate {
		return estimateDictation(text, whole(text), len(strings.Fields(text)), voice, nil)
	}
	if got := estimate("Fix it now.", false); got.probability != nil || got.method != inputUnknown {
		t.Fatalf("short text: %+v", got)
	}
	if got := estimate("Fix it now.", true); got.probability == nil || *got.probability != 1 || got.method != inputDictation || got.evidence != "source" {
		t.Fatalf("a recorded voice transcript decides: %+v", got)
	}
	// Clean text is more likely dictated the longer it runs.
	for _, test := range []struct {
		words  int
		p      float64
		method string
	}{{10, 0.339, inputUncertain}, {30, 0.398, inputUncertain}, {60, 0.490, inputUncertain}, {120, 0.744, inputDictation}} {
		got := estimate(clean(test.words), false)
		if got.probability == nil || *got.probability != test.p || got.method != test.method || len(got.marks) > 0 {
			t.Errorf("%d clean words: %+v, want p=%v %s", test.words, got, test.p, test.method)
		}
	}
	marked := estimate(clean(120)+" Then i stopped.", false)
	if marked.probability == nil || *marked.probability > 0.05 || marked.method != inputKeyboard || !slices.Equal(marked.marks, []string{"lowercase_i"}) {
		t.Fatalf("a typed mark: %+v", marked)
	}
	// Only typed spans are read: pasted code is not the person's typing.
	text := "Please look at this output.\n`panic: index out of range`\nPlease check the parser now and tell me why it fails."
	pasted, after := strings.Index(text, "`"), strings.LastIndex(text, "\n")+1
	spans := []authorSpan{{Category: spanTyped, Start: 0, End: pasted}, {Category: "quoted", Start: pasted, End: after}, {Category: spanTyped, Start: after, End: len(text)}}
	if got := dictationTypedText(text, spans); strings.Contains(got, "panic") || !strings.HasPrefix(got, "Please look") || !strings.HasSuffix(got, "fails.") {
		t.Fatalf("typed text %q", got)
	}
	if got := estimateDictation(text, spans, 16, false, nil); len(got.marks) > 0 {
		t.Fatalf("copied text gave marks %v", got.marks)
	}
}

func TestDictationEstimatesFollowAuthorship(t *testing.T) {
	catalog, _ := testCatalog(t)
	if err := catalog.Initialize(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	long := strings.TrimSpace(strings.Repeat("Please check the parser and tell me what happened there. ", 12))
	for _, record := range []WorkspaceRecord{
		{SourceID: "parser", SourceKind: "claude", Account: "local", Title: "Parser", Conversations: []ConversationRecord{{
			NativeID: "parser", Provider: "claude", Account: "local", Coverage: "complete", Messages: []MessageRecord{
				{NativeID: "typed", Role: "user", Kind: "message", Text: "please fix the parser so nested tables work again and add a test", CreatedAt: "2026-09-01T12:00:00.000Z", Selected: true},
				{NativeID: "clean", Role: "user", Kind: "message", Text: long, CreatedAt: "2026-09-01T12:10:00.000Z", Selected: true},
				{NativeID: "short", Role: "user", Kind: "message", Text: "Fix it now.", CreatedAt: "2026-09-01T12:20:00.000Z", Selected: true},
			}}}},
		{SourceID: "chat", SourceKind: "chatgpt", Account: "local", Title: "Chat", Conversations: []ConversationRecord{{
			NativeID: "chat", Provider: "chatgpt", Account: "local", Coverage: "complete", Harness: "chatgpt-export", Messages: []MessageRecord{
				{NativeID: "voice", Role: "user", Kind: "message", Text: "can you tell me how the parser works", CreatedAt: "2026-09-02T12:00:00.000Z", Selected: true,
					RawText: `{"content":{"parts":[{"content_type":"audio_transcription","text":"can you tell me how the parser works"}]}}`},
			}}}},
	} {
		tx := seedTx(t, catalog.DB)
		if _, _, err := ingestWorkspace(tx, record, false); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.RebuildAuthorship(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	estimates := func() map[string]string {
		t.Helper()
		rows, err := queryMapsContext(ctx, catalog.DB, `SELECT m.native_id,d.input_method,d.evidence,d.version FROM message_dictation d JOIN messages m ON m.id=d.message_id`)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, row := range rows {
			if asString(row["version"]) != dictationVersion {
				t.Fatalf("stale estimate %v", row)
			}
			out[asString(row["native_id"])] = asString(row["input_method"]) + "/" + asString(row["evidence"])
		}
		return out
	}
	want := map[string]string{"typed": "keyboard/text", "clean": "dictation/text", "short": "unknown/text", "voice": "dictation/source"}
	if got := estimates(); !maps.Equal(got, want) {
		t.Fatalf("estimates %v, want %v", got, want)
	}
	// The word index answers how many messages use a word.
	if n := newSpellingCounts(ctx, catalog.DB).messages("parser"); n != 3 {
		t.Fatalf("messages using parser: %d", n)
	}

	// The message view and the per-work table split typed words by input method.
	document, err := querySchemaDocument("writing_messages")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := querytable.LoadSchema(document)
	if err != nil {
		t.Fatal(err)
	}
	result, err := writingMessageDataset.Rows(ctx, catalog.DB, querytable.Query{Limit: 10, OrderBy: []querytable.OrderBy{{Field: "sent_at", Dir: "asc"}},
		Where: []querytable.WhereTerm{{Field: "input_method", Op: "=", Value: inputDictation}}}, schema)
	if err != nil {
		t.Fatal(err)
	}
	dictated := 0
	for _, row := range result.Rows {
		dictated += int(integer(row["dictated_words"]))
		if integer(row["dictated_percent"]) < 50 {
			t.Fatalf("dictated row with %v%%", row["dictated_percent"])
		}
	}
	if len(result.Rows) != 2 || dictated == 0 {
		t.Fatalf("dictated rows %v", result.Rows)
	}
	publishOrigins(t, catalog)
	rows, _, err := catalog.writingData(ctx)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, row := range rows {
		total += int(integer(row["dictated_words"]))
	}
	if total != dictated {
		t.Fatalf("writing table dictated words %d, message view %d", total, dictated)
	}

	// A library classified before the estimate, or by older rules, catches up
	// once; a finished refresh is not repeated.
	execTx(t, catalog.DB,
		`DELETE FROM message_dictation WHERE message_id=(SELECT id FROM messages WHERE native_id='clean')`,
		`UPDATE message_dictation SET version='dictation-old',input_method='keyboard' WHERE message_id=(SELECT id FROM messages WHERE native_id='typed')`,
		`INSERT INTO message_dictation(message_id,conversation_id,typed_words,input_method,evidence,version) VALUES('gone','c',9,'keyboard','text','dictation-old')`,
		`UPDATE meta SET value='dictation-old' WHERE key='dictation_version'`)
	if err := catalog.refreshDictation(ctx); err != nil {
		t.Fatal(err)
	}
	if got := estimates(); !maps.Equal(got, want) {
		t.Fatalf("after refresh %v, want %v", got, want)
	}
	execTx(t, catalog.DB, `DELETE FROM message_dictation WHERE message_id=(SELECT id FROM messages WHERE native_id='clean')`)
	if err := catalog.refreshDictation(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := estimates()["clean"]; ok {
		t.Fatal("a finished refresh ran again")
	}

	// Authorship removes the estimate with the message's typed words.
	execTx(t, catalog.DB, `UPDATE messages SET text='',content_hash='empty' WHERE native_id='typed'`)
	if err := catalog.RebuildAuthorship(ctx, "test-2"); err != nil {
		t.Fatal(err)
	}
	if _, ok := estimates()["typed"]; ok {
		t.Fatal("estimate kept for a message without typed words")
	}
	if _, ok := estimates()["clean"]; !ok {
		t.Fatal("authorship did not write the missing estimate")
	}
}

// A rule upgrade has no new source rows or authorship queue entries. Both
// engines must admit it, keep readiness pending through interruption, and
// complete only after the independent dictation backfill has drained.
func TestDictationBackfillAdmissionAndResume(t *testing.T) {
	for _, entry := range []string{"page", "index", "scheduled"} {
		t.Run(entry, func(t *testing.T) {
			c, config := testCatalog(t)
			ctx := t.Context()
			tx := seedTx(t, c.DB)
			_, _, err := ingestWorkspace(tx, WorkspaceRecord{SourceID: "dictation", SourceKind: "claude", Account: "local", Title: "Dictation", Conversations: []ConversationRecord{{
				NativeID: "dictation", Provider: "claude", Account: "local", Coverage: "complete", Messages: []MessageRecord{
					{NativeID: "first", Role: "user", Kind: "message", Text: "Please check the parser and explain why the output changed.", CreatedAt: "2026-09-01T12:00:00.000Z", Selected: true},
					{NativeID: "second", Role: "user", Kind: "message", Text: "please fix the nested tables so they render correctly again", CreatedAt: "2026-09-01T12:10:00.000Z", Selected: true},
				}}}}, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			want, _, err := c.authorshipGenerations()
			if err != nil {
				t.Fatal(err)
			}
			if err := c.RebuildAuthorship(ctx, want); err != nil {
				t.Fatal(err)
			}
			if c.readBackgroundInputs(ctx).writingPending {
				t.Fatal("the initial authorship and dictation build is not current")
			}
			// Simulate an existing library opened with a new dictation version.
			// Include an orphan so completion also exercises cleanup.
			execTx(t, c.DB, `DELETE FROM message_dictation`,
				`UPDATE meta SET value='dictation-old' WHERE key='dictation_version'`,
				`INSERT INTO message_dictation(message_id,conversation_id,typed_words,input_method,evidence,version) VALUES('orphan','gone',9,'keyboard','text','dictation-old')`,
				`UPDATE sync_deferred SET completed_generation=generation WHERE name IN ('authorship','labels','findings')`)
			if c.authorshipQuickPending() || !c.readBackgroundInputs(ctx).writingPending {
				t.Fatal("a version-only upgrade was not admitted independently of the authorship queue")
			}
			override(t, &dictationRefreshBatch, 1)
			// Stop after the first committed batch, without sleeps or a race
			// against the clock. The next ordinary entry must resume it.
			interrupted, cancel := context.WithCancel(ctx)
			defer cancel()
			interrupted = withWorkProgress(interrupted, func(work activityWork) {
				if strings.HasPrefix(work.Phase, "estimating dictation:") {
					cancel()
				}
			})
			if err := c.UpdateAuthorship(interrupted, want); !errors.Is(err, context.Canceled) {
				t.Fatalf("interrupted backfill: %v", err)
			}
			if n := countRows(t, c, `SELECT COUNT(*) FROM message_dictation WHERE version='`+dictationVersion+`'`); n != 1 {
				t.Fatalf("interruption saved %d rows, want one committed batch", n)
			}
			if !c.dictationRefreshPending() || !c.readBackgroundInputs(ctx).writingPending {
				t.Fatal("an interrupted backfill claimed readiness")
			}
			if wantNow, built, err := c.authorshipEstimateGenerations(); err != nil || wantNow != built {
				t.Fatalf("the test must resume with current authorship: %q %q %v", wantNow, built, err)
			}

			if entry == "scheduled" {
				config.WorkEngine = "scheduled"
				s := testServer(t, config, c)
				if s.workError != nil {
					t.Fatal(s.workError)
				}
				s.work.once.Do(func() {}) // Pump the real adapter deterministically.
				// Exercise admission through the scheduled writing stage with
				// no dirty source generation, rather than submitting library
				// work directly. Page selections remain bounded.
				if generation, err := c.deferredPendingGeneration(ctx, "authorship"); err != nil || generation != 0 {
					t.Fatalf("expected no source work: %d %v", generation, err)
				}
				done := make(chan error, 1)
				go func() { done <- s.routePopulationStage(ctx, "authorship", kindAuthorship, false) }()
				routed := false
				waitUntil(t, 10*time.Second, "the scheduled dictation backfill", func() bool {
					s.work.runner.Pump()
					if !routed {
						select {
						case err := <-done:
							if err != nil {
								t.Fatal(err)
							}
							routed = true
						default:
						}
					}
					return routed && !c.dictationRefreshPending()
				})
			} else {
				var started []func(context.Context, *Op) error
				c.background = func(_ OperationSpec, work func(context.Context, *Op) error) bool {
					started = append(started, work)
					return true
				}
				if entry == "page" {
					stats, err := c.AuthorshipStats(ctx)
					if err != nil || stats["stale"] != true {
						t.Fatalf("page readiness during backfill: %v %v", stats, err)
					}
				} else if !c.refreshAuthorshipInContext(ctx) {
					t.Fatal("an idle index bypassed the dictation backfill")
				}
				if len(started) != 1 {
					t.Fatalf("started %d passes, want one", len(started))
				}
				if err := started[0](ctx, nil); err != nil {
					t.Fatal(err)
				}
			}
			if c.dictationRefreshPending() || c.readBackgroundInputs(ctx).writingPending || c.ensureAuthorship() {
				t.Fatal("the resumed backfill did not publish readiness")
			}
			if n := countRows(t, c, `SELECT COUNT(*) FROM message_dictation`); n != 2 {
				t.Fatalf("after resume: %d estimates, want both messages and no orphan", n)
			}
			if n := countRows(t, c, `SELECT COUNT(*) FROM sync_deferred WHERE name IN ('labels','findings') AND generation>completed_generation`); n != 0 {
				t.Fatal("a dictation-only backfill invalidated labels or findings")
			}
		})
	}
}
