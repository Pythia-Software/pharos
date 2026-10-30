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
	return LabelerSettings{Enabled: true, ModelDir: dir, Variant: "int8", Worker: exe, Threads: 1, Workers: 1, BatchTokens: 4096, MaxBatch: 16}
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

	// A new message in one conversation: that conversation is rebuilt, and
	// only the new message is labeled.
	execTx(t, catalog.DB, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('m-o8','c-other','m-o8','assistant','message','All checks pass now.','hash-m-o8','2026-09-03T13:00:00.000Z',999)`)
	small, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
	if err != nil || small.ConversationsBuilt != 1 || small.MessagesLabeled != 1 || small.MessagesUnchanged == 0 {
		t.Fatalf("incremental run: %+v %v", small, err)
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
	// c-root (lost labels) and c-other (a deleted message): c-root's forks
	// copy its history, but restoring labels changed none of its inputs.
	if err != nil || healed.MessagesRemoved != 1 || healed.MessagesLabeled != 2 || healed.ConversationsBuilt != 2 || labelCount(t, catalog, "") != len(messages) {
		t.Fatalf("healing run: %+v %v", healed, err)
	}
	if labelCount(t, catalog, "WHERE message_id='m-o8'") != 0 {
		t.Fatal("a deleted message kept its label")
	}
	// A new message in a parent: its forks (six) are rebuilt with it, and
	// only the new message is labeled.
	execTx(t, catalog.DB, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('m-a9','c-root','m-a9','assistant','message','Wrapped up.','hash-m-a9','2026-09-01T13:00:00.000Z',998)`)
	parent, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
	if err != nil || parent.ConversationsBuilt != 7 || parent.MessagesLabeled != 1 {
		t.Fatalf("a parent's change: %+v %v", parent, err)
	}
	var dirty int
	if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM message_labels_dirty`).Scan(&dirty); err != nil || dirty != 0 {
		t.Fatalf("dirty marks left: %d %v", dirty, err)
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
	catalog := labeledCatalog(t)
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
	healed, err := catalog.RunLabels(t.Context(), labelsTestSettings(t, "ok"), LabelsRunOptions{})
	if err != nil || !healed.Complete || healed.MessagesLabeled != 1 {
		t.Fatalf("next run: %+v %v", healed, err)
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
