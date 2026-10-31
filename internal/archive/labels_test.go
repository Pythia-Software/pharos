package archive

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	labelrt "github.com/gbdubs/pharos/internal/labeler"
	"github.com/gbdubs/pharos/internal/labeler/labelertest"
)

// labelsTestSettings is a model directory with four labels (P > P.request,
// C > C.report), the committed tokenizer and a stand-in model file, served
// by the fake worker in mode.
func labelsTestSettings(t *testing.T, mode string, manifestFields ...map[string]any) LabelerSettings {
	t.Helper()
	dir := t.TempDir()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "labeler", "kestrel-68m-tokenizer.json.gz"))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	tokenizer, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	model := []byte("stand-in model")
	sum := func(data []byte) string { s := sha256.Sum256(data); return hex.EncodeToString(s[:]) }
	fields := map[string]any{
		"name": "fake", "framing_contract": labelrt.FramingContract,
		"files":  map[string]string{"onnx_int8": "model.int8.onnx", "tokenizer": "tokenizer.json"},
		"sha256": map[string]string{"model.int8.onnx": sum(model)}, "tokenizer_sha256": sum(tokenizer),
		"labels": map[string]any{"order": []string{"P", "P.request", "C", "C.report"},
			"ancestors": map[string][]string{"P": {}, "P.request": {"P"}, "C": {}, "C.report": {"C"}}},
		"thresholds": map[string][]float64{"default": {0.5, 0.5, 0.5, 0.5}},
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
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(labelertest.FakeWorkerEnv, mode)
	t.Setenv(labelertest.FakeCountEnv, filepath.Join(t.TempDir(), "count"))
	// The fake worker loads no ONNX Runtime, so none is downloaded for it.
	override(t, &labelsRuntimePinned, false)
	return LabelerSettings{Enabled: true, ModelDir: dir, Variant: "int8", Worker: exe, Threads: 1, Workers: 1, BatchTokens: 4096, MaxBatch: 16, IdleSeconds: 60}
}

// labeledCatalog is the labeling fixture with its authorship ledger built.
func labeledCatalog(t *testing.T) *Catalog {
	t.Helper()
	catalog, _ := testCatalog(t)
	seedLabelingCatalog(t, catalog)
	if err := catalog.RebuildAuthorship(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	return catalog
}

func labelCount(t *testing.T, catalog *Catalog, where string, args ...any) int {
	t.Helper()
	var n int
	if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM message_labels `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLabelsLedgerLabelsEveryUnitOnceAndThenOnlyChanges(t *testing.T) {
	catalog := labeledCatalog(t)
	settings := labelsTestSettings(t, "ok")
	live := liveItems(t, catalog)
	messages := map[string]bool{}
	for _, item := range live {
		messages[item.MessageID] = true
	}
	stats, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Complete || stats.MessagesLabeled != len(messages) || stats.ItemsLabeled != len(live) || labelCount(t, catalog, "") != len(messages) {
		t.Fatalf("first run: %+v; %d messages, %d items", stats, len(messages), len(live))
	}
	// Batched: far fewer batches than items, and no batch over the budget.
	if stats.Batches >= stats.ItemsLabeled || stats.PaddedTokens > stats.Batches*settings.BatchTokens {
		t.Fatalf("batches %d for %d items, %d padded tokens", stats.Batches, stats.ItemsLabeled, stats.PaddedTokens)
	}
	if generation, _ := catalog.metaValue(t.Context(), "labels_generation"); generation == "" {
		t.Fatal("a complete run recorded no generation")
	}
	// A stored label is the closed set the model's probabilities give.
	var mask int64
	var probs []byte
	if err := catalog.DB.QueryRow(`SELECT labels,probs FROM message_labels WHERE message_id='m-a1'`).Scan(&mask, &probs); err != nil {
		t.Fatal(err)
	}
	decoded := decodeLabelProbs(probs)
	if len(decoded) != 4 || (decoded[1] >= 0.5) != (mask&2 != 0) || mask&2 != 0 && mask&1 == 0 {
		t.Fatalf("m-a1: mask %b probs %v", mask, decoded)
	}

	// Nothing changed: nothing is rebuilt or labeled.
	again, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
	if err != nil || again.ConversationsBuilt != 0 || again.MessagesLabeled != 0 || !again.Complete {
		t.Fatalf("second run: %+v %v", again, err)
	}

	// A new message in one conversation: that conversation is rebuilt, with
	// c-late, which re-sends c-other's messages, and only the new message is
	// labeled.
	execTx(t, catalog.DB, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('m-o8','c-other','m-o8','assistant','message','All checks pass now.','hash-m-o8','2026-09-03T13:00:00.000Z',999)`)
	small, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
	if err != nil || small.ConversationsBuilt != 2 || small.MessagesLabeled != 1 || small.MessagesUnchanged == 0 {
		t.Fatalf("incremental run: %+v %v", small, err)
	}
	// The first run's workers stayed warm: the same processes label it.
	if !slices.Equal(small.WorkerPIDs, stats.WorkerPIDs) {
		t.Fatalf("workers %v, then %v: not reused", stats.WorkerPIDs, small.WorkerPIDs)
	}

	// Rewriting a classification without changing it marks nothing.
	execTx(t, catalog.DB, `UPDATE message_authorship SET spans_json=spans_json,work_id=work_id`)
	var marks int
	if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM message_labels_dirty`).Scan(&marks); err != nil || marks != 0 {
		t.Fatalf("an unchanged rewrite marked %d conversations dirty (%v)", marks, err)
	}

	// A deleted message loses its label; labels deleted behind the ledger's
	// back are restored.
	execTx(t, catalog.DB, `DELETE FROM messages WHERE id='m-o8'`, `DELETE FROM message_labels WHERE message_id IN ('m-a1','m-h3')`)
	healed, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
	// c-root (lost labels) and c-other (a deleted message), with c-late,
	// which re-sends c-other's messages: c-root's forks copy its history, but
	// restoring labels changed none of its inputs.
	if err != nil || healed.MessagesRemoved != 1 || healed.MessagesLabeled != 2 || healed.ConversationsBuilt != 3 || labelCount(t, catalog, "") != len(messages) {
		t.Fatalf("healing run: %+v %v", healed, err)
	}
	if labelCount(t, catalog, "WHERE message_id='m-o8'") != 0 {
		t.Fatal("a deleted message kept its label")
	}
	// A new message in a parent: its forks (six) are rebuilt with it, and so
	// are c-other, which re-sends its request, and c-late, which re-sends
	// c-other's messages; only the new message is labeled.
	execTx(t, catalog.DB, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('m-a9','c-root','m-a9','assistant','message','Wrapped up.','hash-m-a9','2026-09-01T13:00:00.000Z',998)`)
	parent, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
	if err != nil || parent.ConversationsBuilt != 9 || parent.MessagesLabeled != 1 {
		t.Fatalf("a parent's change: %+v %v", parent, err)
	}
	var dirty int
	if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM message_labels_dirty`).Scan(&dirty); err != nil || dirty != 0 {
		t.Fatalf("dirty marks left: %d %v", dirty, err)
	}

	// A message re-sent a month later is labeled from its source's
	// classification. When the source becomes quoted agent text (an earlier
	// reply is imported), the copy's own spans stay the same, yet its items
	// change: the copy's conversation follows its source's.
	reused := "Please add retry logic with exponential backoff to the upload client and cover the timeout path with a regression test"
	execTx(t, catalog.DB,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('ws-reuse','claude','local','ws-reuse','ws-reuse','2026-10-01')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,agent_depth,started_at) VALUES('c-src','ws-reuse','claude','local','c-src',0,'2026-09-10T12:00:00.000Z'),('c-copy','ws-reuse','claude','local','c-copy',0,'2026-10-12T12:00:00.000Z'),('c-earlier','ws-reuse','claude','local','c-earlier',0,'2026-09-09T12:00:00.000Z')`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('m-src','c-src','m-src','user','message','`+reused+`','hash-m-src','2026-09-10T12:00:00.000Z',1001),('m-copy','c-copy','m-copy','user','message','`+reused+`','hash-m-copy','2026-10-12T12:00:00.000Z',1002)`)
	if err := catalog.RebuildAuthorship(t.Context(), "test-reuse"); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{}); err != nil || labelCount(t, catalog, "WHERE message_id IN ('m-src','m-copy')") != 2 {
		t.Fatalf("labeling the source and its copy: %v", err)
	}
	copySpans := func() string {
		t.Helper()
		var spans string
		if err := catalog.DB.QueryRow(`SELECT group_concat(category||':'||COALESCE(source_message_id,''),',') FROM message_authorship_spans WHERE message_id='m-copy'`).Scan(&spans); err != nil {
			t.Fatal(err)
		}
		return spans
	}
	before := copySpans()
	if !strings.Contains(before, "resent:m-src") {
		t.Fatalf("the copy is not re-sent from its source: %q", before)
	}
	execTx(t, catalog.DB, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('m-earlier','c-earlier','m-earlier','assistant','message','`+reused+`','hash-m-earlier','2026-09-09T12:00:00.000Z',1003)`)
	if err := catalog.RebuildAuthorship(t.Context(), "test-reuse-2"); err != nil {
		t.Fatal(err)
	}
	if after := copySpans(); after != before {
		t.Fatalf("the copy's own spans changed (%q to %q); the case needs them unchanged", before, after)
	}
	if _, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{}); err != nil {
		t.Fatal(err)
	}
	// The incremental run reached what a full rebuild reaches.
	full, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{Full: true})
	if err != nil || full.MessagesLabeled != 0 || full.MessagesRemoved != 0 {
		t.Fatalf("a full run still changed labels after the source's reclassification: %+v %v", full, err)
	}
}

func TestLabelsRunsInLimitedStepsAndOneAtATime(t *testing.T) {
	catalog := labeledCatalog(t)
	settings := labelsTestSettings(t, "ok")
	first, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{Limit: 3})
	if err != nil || first.MessagesLabeled != 3 || first.Complete {
		t.Fatalf("limited run: %+v %v", first, err)
	}
	rest, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{OneAtATime: true})
	if err != nil || !rest.Complete || rest.MessagesLabeled == 0 || rest.Batches != rest.ItemsLabeled {
		t.Fatalf("one at a time: %+v %v", rest, err)
	}
}

func TestLabelsSurviveAWorkerCrash(t *testing.T) {
	override(t, &labelsWorkerBackoff, func(int) time.Duration { return time.Millisecond })
	catalog := labeledCatalog(t)
	settings := labelsTestSettings(t, "crash-after:4")
	settings.MaxBatch = 2
	stats, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
	if err != nil || !stats.Complete || stats.Restarts == 0 {
		t.Fatalf("run with crashes: %+v %v", stats, err)
	}
	if n := labelCount(t, catalog, ""); n != stats.MessagesLabeled {
		t.Fatalf("%d labels for %d labeled", n, stats.MessagesLabeled)
	}
	// A worker that always crashes ends the run, which the operation records
	// as interrupted; what was stored stays, and nothing is marked complete.
	execTx(t, catalog.DB, `DELETE FROM message_labels`)
	settings = labelsTestSettings(t, "crash-after:0")
	stats, err = catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
	if !errors.Is(err, labelrt.ErrWorkersUnavailable) || stats.Complete {
		t.Fatalf("always crashing: %+v %v", stats, err)
	}
}

// The builder stays a bounded distance ahead of a slow worker: a backfill
// holds a few windows of items, not the library.
func TestLabelsBuilderWaitsForTheWorkers(t *testing.T) {
	slowTest(t)
	catalog := labeledCatalog(t)
	settings := labelsTestSettings(t, "slow")
	settings.MaxBatch = 1
	stats, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
	if err != nil || !stats.Complete {
		t.Fatalf("run: %+v %v", stats, err)
	}
	// A window of 4 × 1 × 2 items, a few packed batches, one conversation.
	if stats.PeakPending > 16 || stats.PeakPending >= stats.ItemsLabeled {
		t.Fatalf("%d items were pending at once, of %d", stats.PeakPending, stats.ItemsLabeled)
	}
}

func TestLabelsStopAndResume(t *testing.T) {
	catalog := labeledCatalog(t)
	settings := labelsTestSettings(t, "slow")
	settings.MaxBatch = 1
	ctx, cancel := context.WithCancel(t.Context())
	override(t, &labelsFlushEvery, 20*time.Millisecond)
	// Stop as soon as the first label is stored.
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				var n int
				if catalog.DB.QueryRow(`SELECT COUNT(*) FROM message_labels`).Scan(&n) == nil && n > 0 {
					cancel()
					return
				}
			}
		}
	}()
	stats, err := catalog.RunLabels(ctx, settings, LabelsRunOptions{})
	cancel()
	if !errors.Is(err, context.Canceled) || stats.Complete {
		t.Fatalf("stopped run: %+v %v", stats, err)
	}
	kept := labelCount(t, catalog, "")
	settings = labelsTestSettings(t, "ok")
	resumed, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
	// What the stopped run stored is never labeled again, and every message
	// ends up labeled.
	messages := map[string]bool{}
	for _, item := range liveItems(t, catalog) {
		messages[item.MessageID] = true
	}
	total := len(messages)
	if err != nil || !resumed.Complete || resumed.MessagesLabeled > total-kept || labelCount(t, catalog, "") != total {
		t.Fatalf("resumed: %+v %v (kept %d of %d)", resumed, err, kept, total)
	}
}

func TestLabelsRunInTheAnalysisChainOnlyWhenEnabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		catalog, config := testCatalog(t)
		seedLabelingCatalog(t, catalog)
		config.Labels = labelsTestSettings(t, "ok")
		config.Labels.Enabled = enabled
		server := testServer(t, config, catalog)
		if err := server.finishDeferredSync(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !enabled {
			if catalog.hasLabelsLedger() {
				t.Fatal("the chain created the labels ledger with labeling off")
			}
			continue
		}
		// Authorship ran first in the chain, so every unit is labeled.
		if n := labelCount(t, catalog, ""); n < 20 {
			t.Fatalf("the chain labeled %d messages", n)
		}
		if generation, _ := catalog.metaValue(t.Context(), "labels_generation"); generation == "" {
			t.Fatal("the chain's labels did not complete")
		}
	}
}

func TestLabelsStatusAndExplain(t *testing.T) {
	catalog := labeledCatalog(t)
	settings := labelsTestSettings(t, "ok")
	status, err := catalog.LabelsStatus(t.Context(), settings)
	if err != nil || status["state"] != "never built" || catalog.hasLabelsLedger() {
		t.Fatalf("status before any run: %v %v (ledger created: %v)", status, err, catalog.hasLabelsLedger())
	}
	if _, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{}); err != nil {
		t.Fatal(err)
	}
	if status, err = catalog.LabelsStatus(t.Context(), settings); err != nil || status["state"] != "current" {
		t.Fatalf("status after a run: %v %v", status, err)
	}
	explained, err := catalog.LabelsExplain(t.Context(), settings, "m-h1", true)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := explained["items"].([]map[string]any)
	stored, _ := explained["stored"].(map[string]any)
	if len(items) != 1 || items[0]["sender_role"] != "user" || items[0]["tokens"].(int) < 10 || stored == nil || stored["current"] != true || explained["inferred"] == nil {
		t.Fatalf("explain m-h1: %v", explained)
	}
	explained, err = catalog.LabelsExplain(t.Context(), settings, "m-branch", false)
	if err != nil || explained["excluded"].(map[string]any)["reason"] != "unselected_branch" {
		t.Fatalf("explain m-branch: %v %v", explained, err)
	}
}

// A model whose input contract adds a pooling mask over the target text
// runs through the same protocol: one more named input, built from the
// framing's text span. The fake worker pools what the mask marks.
func TestLabelsCarryAPoolingInput(t *testing.T) {
	catalog := labeledCatalog(t)
	settings := labelsTestSettings(t, "ok", map[string]any{"input_contract": labelrt.InputContractIDsAttentionPool})
	if _, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{Conversations: []string{"c-root"}}); err != nil {
		t.Fatal(err)
	}
	model, err := labelrt.LoadModel(settings.ModelDir, "int8")
	if err != nil || !model.Pools() {
		t.Fatal(err)
	}
	var item labelingItem
	for _, candidate := range liveItems(t, catalog) {
		if candidate.MessageID == "m-a1" {
			item = candidate
		}
	}
	ids, spans := model.Tokenizer.FrameWithSpans(labelrt.FrameInput{Segment: item.Segment, Cohort: item.Cohort, Sender: item.SenderRole, Context: item.Context, Text: item.Text})
	var raw []byte
	if err := catalog.DB.QueryRow(`SELECT probs FROM message_labels WHERE message_id='m-a1'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if got, want := decodeLabelProbs(raw)[0], labelertest.FakeProb(ids[spans.Text.Start:spans.Text.End], 0); got != want {
		t.Fatalf("pooled prob %v, want %v (whole sequence %v)", got, want, labelertest.FakeProb(ids, 0))
	}
}

// An item that crashes the worker every time fails only its own message:
// the batch is split, the rest are labeled, and the next run retries it.
func TestLabelsIsolateAnItemThatCrashesTheWorker(t *testing.T) {
	override(t, &labelsWorkerBackoff, func(int) time.Duration { return time.Millisecond })
	catalog, config := testCatalog(t)
	seedLabelingCatalog(t, catalog)
	if err := catalog.RebuildAuthorship(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	settings := labelsTestSettings(t, "ok")
	model, err := labelrt.LoadModel(settings.ModelDir, "int8")
	if err != nil {
		t.Fatal(err)
	}
	// The thinking tag's first token appears in the one thinking item only.
	poison := model.Tokenizer.Encode("thinking assistant :")[0]
	settings = labelsTestSettings(t, fmt.Sprintf("crash-on-id:%d", poison))
	stats, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
	if err != nil || stats.MessagesFailed != 1 || stats.Complete || labelCount(t, catalog, "WHERE message_id='m-think'") != 0 || labelCount(t, catalog, "") < 20 {
		t.Fatalf("run with a poison item: %+v %v", stats, err)
	}
	// The background stage keeps a failed message pending, so the schedule
	// retries it rather than completing the stage around it.
	config.Labels = settings
	server := testServer(t, config, catalog)
	stage := func() deferredRow {
		t.Helper()
		if err := catalog.writeTransaction(t.Context(), "test", markSyncDeferred); err != nil {
			t.Fatal(err)
		}
		if err := server.finishDeferredStages(t.Context(), findingsAuto, map[string]bool{"labels": true}, nil); err != nil {
			t.Fatal(err)
		}
		deferred, _ := catalog.freshnessDeferred(t.Context())
		return deferred["labels"]
	}
	if row := stage(); row.generation <= row.completed {
		t.Fatalf("the labels stage completed with a failed message: %+v", row)
	}
	healed, err := catalog.RunLabels(t.Context(), labelsTestSettings(t, "ok"), LabelsRunOptions{})
	if err != nil || !healed.Complete || healed.MessagesLabeled != 1 {
		t.Fatalf("next run: %+v %v", healed, err)
	}
	if row := stage(); row.generation == 0 || row.generation > row.completed {
		t.Fatalf("the labels stage did not complete once nothing failed: %+v", row)
	}
}

// A writer that cannot store anything stops the run at once with its own
// error, rather than labeling the rest of the library for nothing.
func TestLabelsStopWhenTheyCannotBeStored(t *testing.T) {
	override(t, &labelsFlushEvery, 10*time.Millisecond)
	catalog := labeledCatalog(t)
	if err := catalog.ensureLabelsSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	execTx(t, catalog.DB, `CREATE TRIGGER labels_disk_full BEFORE INSERT ON message_labels BEGIN SELECT RAISE(ABORT,'disk full'); END`)
	settings := labelsTestSettings(t, "slow")
	settings.MaxBatch = 1
	stats, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
	if err == nil || !strings.Contains(err.Error(), "disk full") || stats.ItemsLabeled > 8 {
		t.Fatalf("run that cannot store: %+v %v", stats, err)
	}
}

// A stored classification that no longer fits its message (the text changed
// and authorship has not caught up) counts as none: no panic, no label.
func TestLabelsIgnoreAStaleClassification(t *testing.T) {
	catalog := labeledCatalog(t)
	execTx(t, catalog.DB, `UPDATE message_authorship SET spans_json='[{"start":0,"end":99999,"category":"typed"}]' WHERE message_id='m-h1'`,
		`UPDATE message_authorship SET total_chars=total_chars+1 WHERE message_id='m-o1'`)
	stats, err := catalog.RunLabels(t.Context(), labelsTestSettings(t, "ok"), LabelsRunOptions{})
	if err != nil || labelCount(t, catalog, "WHERE message_id IN ('m-h1','m-o1')") != 0 || stats.MessagesLabeled < 20 {
		t.Fatalf("stale classifications: %+v %v", stats, err)
	}
}

// A pooling model never gets an item whose text has no tokens; a model that
// pools every token does.
func TestLabelsFrameRefusesAnEmptyTargetForAPoolingModel(t *testing.T) {
	pooled, err := labelrt.LoadModel(labelsTestSettings(t, "ok", map[string]any{"input_contract": labelrt.InputContractIDsAttentionPool}).ModelDir, "int8")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := labelrt.LoadModel(labelsTestSettings(t, "ok").ModelDir, "int8")
	if err != nil {
		t.Fatal(err)
	}
	empty := labelingItem{Segment: segmentAR, Cohort: "agent_reply", SenderRole: "assistant", Context: "Previous user request:\nfix it"}
	full := empty
	full.Text = "Done."
	if _, spans, ok := labelsFrame(pooled, empty); ok || spans.Text.Len() != 0 {
		t.Fatalf("an empty target was framed for a pooling model: %+v", spans)
	}
	for _, c := range []struct {
		model *labelrt.Model
		item  labelingItem
	}{{pooled, full}, {plain, empty}, {plain, full}} {
		if _, _, ok := labelsFrame(c.model, c.item); !ok {
			t.Fatalf("%s refused %q", c.model.InputContract, c.item.Text)
		}
	}
}

// Every label records its model, input contract included: switching models
// either way relabels every message, and a run after that has nothing to do.
func TestLabelsRelabelOnAModelSwitch(t *testing.T) {
	catalog := labeledCatalog(t)
	plain := labelsTestSettings(t, "ok")
	pooled := labelsTestSettings(t, "ok", map[string]any{"name": "fake-pool", "input_contract": labelrt.InputContractIDsAttentionPool})
	first, err := catalog.RunLabels(t.Context(), plain, LabelsRunOptions{})
	if err != nil || !first.Complete {
		t.Fatalf("first model: %+v %v", first, err)
	}
	for _, settings := range []LabelerSettings{pooled, plain} {
		switched, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
		if err != nil || !switched.Complete || switched.MessagesLabeled != first.MessagesLabeled || switched.MessagesUnchanged != 0 {
			t.Fatalf("switch: %+v %v", switched, err)
		}
		model, _ := labelrt.LoadModel(settings.ModelDir, "int8")
		if n := labelCount(t, catalog, "WHERE model=?", labelsModelIdentity(model)); n != first.MessagesLabeled || !strings.Contains(labelsModelIdentity(model), model.InputContract) {
			t.Fatalf("%d rows carry %s", n, labelsModelIdentity(model))
		}
		again, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
		if err != nil || again.MessagesLabeled != 0 || again.ConversationsBuilt != 0 {
			t.Fatalf("after the switch: %+v %v", again, err)
		}
	}
}

// Labels are their own background item, the chain's last stage: an
// update of your writing alone leaves them waiting, and the labels item
// labels what changed.
func TestLabelsAreTheirOwnBackgroundItem(t *testing.T) {
	catalog, config := testCatalog(t)
	seedLabelingCatalog(t, catalog)
	config.Labels = labelsTestSettings(t, "ok")
	server := testServer(t, config, catalog)
	if err := catalog.writeTransaction(t.Context(), "test", markSyncDeferred); err != nil {
		t.Fatal(err)
	}
	if err := server.finishDeferredStages(context.Background(), findingsAuto, map[string]bool{"authorship": true}, nil); err != nil {
		t.Fatal(err)
	}
	if catalog.hasLabelsLedger() {
		t.Fatal("a writing update labeled messages")
	}
	if err := server.finishDeferredStages(context.Background(), findingsAuto, map[string]bool{"labels": true}, nil); err != nil {
		t.Fatal(err)
	}
	if n := labelCount(t, catalog, ""); n < 20 {
		t.Fatalf("the labels item labeled %d messages", n)
	}
	deferred, _ := catalog.freshnessDeferred(t.Context())
	if row := deferred["labels"]; row.generation == 0 || row.generation > row.completed {
		t.Fatalf("the labels stage did not complete: %+v", row)
	}
	// The item exists only where labels are on.
	settings, _ := catalog.backgroundSettings(t.Context(), time.Now())
	has := func(in backgroundInputs) bool {
		return slices.ContainsFunc(backgroundPlan(settings, in, time.Now()), func(plan itemPlan) bool { return plan.ID == "labels" })
	}
	if in := server.backgroundInputsFor(t.Context(), nil); !has(in) {
		t.Fatal("no labels item with labels on")
	}
	if has(backgroundInputs{}) {
		t.Fatal("a labels item with labels off")
	}
}

// On battery a run gets one small worker and small batches, unless the
// configuration sets a thread budget, and a backlog waits; plugged in
// nothing changes.
func TestLabelsForPower(t *testing.T) {
	settings := LabelerSettings{Threads: 4, Workers: 3, BatchTokens: 16384, MemoryBudgetMB: 8192}
	if got, bulk := labelsForPower(settings, powerState{Battery: true}); got != settings || !bulk {
		t.Fatalf("plugged in: %+v %v", got, bulk)
	}
	got, bulk := labelsForPower(settings, powerState{Battery: true, OnBattery: true, Percent: 80})
	if bulk || got.Workers != 1 || got.Threads != labelsBatteryThreads || got.BatchTokens != labelsBatteryBatchTokens || labelsBudget(got).Threads != labelsBatteryThreads {
		t.Fatalf("on battery: %+v %v (budget %+v)", got, bulk, labelsBudget(got))
	}
	settings.ThreadBudget = 6
	if got, bulk := labelsForPower(settings, powerState{Battery: true, OnBattery: true}); got != settings || bulk {
		t.Fatalf("on battery with labels_thread_budget: %+v %v", got, bulk)
	}
}

// A backlog continues as its own run only when plugged in; on battery the
// chain labels its share and the stage stays pending.
func TestLabelsBacklogWaitsForPower(t *testing.T) {
	for _, battery := range []bool{true, false} {
		catalog, config := testCatalog(t)
		seedLabelingCatalog(t, catalog)
		config.Labels = labelsTestSettings(t, "ok")
		override(t, &labelsChainLimit, 3)
		srv := testServer(t, config, catalog)
		srv.background.power, srv.background.powerAt = powerState{Battery: true, OnBattery: battery, Percent: 90}, time.Now()
		if err := catalog.writeTransaction(t.Context(), "test", markSyncDeferred); err != nil {
			t.Fatal(err)
		}
		if err := srv.finishDeferredStages(t.Context(), findingsAuto, map[string]bool{"authorship": true, "labels": true}, nil); err != nil {
			t.Fatal(err)
		}
		deferred, _ := catalog.freshnessDeferred(t.Context())
		if row := deferred["labels"]; row.generation <= row.completed {
			t.Fatalf("battery %v: a stage that left a backlog completed", battery)
		}
		if battery {
			if n := labelCount(t, catalog, ""); n > labelsChainLimit {
				t.Fatalf("on battery %d messages were labeled, past the chain's share", n)
			}
			continue
		}
		if !waitFor(20*time.Second, func() {
			for labelCount(t, catalog, "") < 20 {
				time.Sleep(20 * time.Millisecond)
			}
		}) {
			t.Fatalf("plugged in, the backlog run labeled %d messages", labelCount(t, catalog, ""))
		}
	}
}
