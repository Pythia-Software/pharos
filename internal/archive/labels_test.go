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
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
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

func labelerHelloThreads(t *testing.T, path string) []int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var threads []int
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var hello labelrt.Hello
		if err := decoder.Decode(&hello); err == io.EOF {
			return threads
		} else if err != nil {
			t.Fatal(err)
		}
		threads = append(threads, hello.Threads)
	}
}

func TestLabelsLiveThreadTarget(t *testing.T) {
	catalog := labeledCatalog(t)
	settings := labelsTestSettings(t, "ok")
	settings.MaxBatch = 1 // Enough batches to change targets during one run.
	hellos := filepath.Join(t.TempDir(), "hellos.jsonl")
	t.Setenv(labelertest.FakeHelloEnv, hellos)
	override(t, &labelerThreadCheck, time.Duration(0))
	var observed []int
	stats, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{ThreadTarget: func() LabelerSettings {
		if catalog.labelers != nil {
			threads, _, _ := catalog.labelers.Usage()
			if len(observed) == 0 || observed[len(observed)-1] != threads {
				observed = append(observed, threads)
			}
		}
		target := settings
		target.Threads = 1
		if slices.Contains(observed, 1) {
			target.Threads = 4
		}
		if slices.Contains(observed, 4) {
			target.Threads = 2
		}
		return target
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Complete || stats.MessagesFailed != 0 || stats.ItemsLabeled != len(liveItems(t, catalog)) || labelCount(t, catalog, "") != stats.MessagesLabeled {
		t.Fatalf("changing cores lost work: %+v", stats)
	}
	if threads := labelerHelloThreads(t, hellos); !slices.Equal(threads, []int{1, 4, 2}) {
		t.Fatalf("worker thread targets: %v", threads)
	}
	if stats.Restarts != 0 {
		t.Fatalf("deliberate core changes counted as crashes: %+v", stats)
	}
}

func TestLabelerThreadTargetPolling(t *testing.T) {
	settings := LabelerSettings{Threads: 2, Workers: 3, ModelDir: "original", BatchTokens: 4096}
	reads := 0
	target := labelerThreadTarget{read: func() LabelerSettings {
		reads++
		return LabelerSettings{Threads: 4, PeerThreads: 2, ThreadBudget: 8, ModelDir: "other"}
	}}
	got := target.settings(settings)
	if got.Threads != 4 || got.PeerThreads != 2 || got.ThreadBudget != 8 || got.Workers != settings.Workers || got.ModelDir != settings.ModelDir || got.BatchTokens != settings.BatchTokens {
		t.Fatalf("thread settings: %+v", got)
	}
	_ = target.settings(got)
	if reads != 1 {
		t.Fatalf("polled before the interval elapsed: %d", reads)
	}
	target.next = time.Now().Add(-time.Second)
	_ = target.settings(got)
	if reads != 2 {
		t.Fatalf("did not poll after the interval: %d", reads)
	}
	if fixed := (&labelerThreadTarget{}).settings(settings); fixed != settings {
		t.Fatalf("a CLI run changed settings: %+v", fixed)
	}
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
	// The reader gets each message's stored labels, named in the order of
	// the model that wrote them, and the taxonomy rows naming them.
	model, err := labelrt.LoadModel(settings.ModelDir, "int8")
	if err != nil {
		t.Fatal(err)
	}
	readerLabels := func(conversation, workspace, message string) (map[string]any, map[string]any) {
		t.Helper()
		page, err := catalog.WorkConversation(workspace, conversation)
		if err != nil {
			t.Fatal(err)
		}
		summary, _ := page["labels"].(map[string]any)
		for _, row := range page["messages"].([]map[string]any) {
			if row["id"] == message {
				labels, _ := row["labels"].(map[string]any)
				return labels, summary
			}
		}
		return nil, summary
	}
	shown, summary := readerLabels("c-root", "ws-main", "m-a1")
	named := map[string]bool{}
	for _, row := range summary["taxonomy"].([]map[string]any) {
		named[row["id"].(string)] = row["name"] != ""
	}
	if shown == nil || !slices.Equal(shown["ids"].([]string), model.LabelNames(uint64(mask))) || summary["pending"] != false {
		t.Fatalf("reader labels %v, summary %v", shown, summary)
	}
	for j, id := range shown["ids"].([]string) {
		if p, ok := shown["probs"].(map[string]float64)[id]; !named[id] || !ok || math.Abs(p-float64(decoded[slices.Index(model.Manifest.Labels.Order, id)])) > 0.001 {
			t.Fatalf("label %d %s: probability %v, taxonomy %v", j, id, shown["probs"], summary)
		}
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
	if _, summary := readerLabels("c-other", "ws-other", "m-o8"); summary["pending"] != true {
		t.Fatalf("a changed conversation reads as labeled: %v", summary)
	}
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
	// The newest conversation comes first, so recent labels appear soonest.
	var newest int
	if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM message_labels WHERE conversation_id='c-late'`).Scan(&newest); err != nil || newest == 0 {
		t.Fatalf("the limited run skipped the newest conversation (%v)", err)
	}
	rest, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{OneAtATime: true})
	if err != nil || !rest.Complete || rest.MessagesLabeled == 0 || rest.Batches != rest.ItemsLabeled {
		t.Fatalf("one at a time: %+v %v", rest, err)
	}
	// A fork that sorts after its parent (started with it, so by id) follows
	// the parent's change. A run cut short once the parent is stored must not
	// lose the fork: the next run still rebuilds it.
	execTx(t, catalog.DB,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth,started_at) VALUES('c-zz-fork','ws-main','codex','local','c-zz-fork','c-root',0,'2026-09-01T12:00:00.000Z')`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('m-zz1','c-zz-fork','m-zz1','user','message','Now make the toolbar sticky on tablets as well','hash-m-zz1','2026-09-02T13:00:00.000Z',1100)`)
	if _, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{}); err != nil {
		t.Fatal(err)
	}
	execTx(t, catalog.DB, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('m-a10','c-root','m-a10','assistant','message','Pushed the fix.','hash-m-a10','2026-09-01T14:00:00.000Z',1101)`)
	cut, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{Limit: 1})
	if err != nil || cut.MessagesLabeled != 1 || cut.Complete {
		t.Fatalf("a run cut after the parent: %+v %v", cut, err)
	}
	after, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
	if err != nil || !after.Complete || after.ConversationsBuilt == 0 {
		t.Fatalf("the next run after a cut: %+v %v (the fork was lost)", after, err)
	}
	var dirty int
	if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM message_labels_dirty`).Scan(&dirty); err != nil || dirty != 0 {
		t.Fatalf("dirty marks left: %d %v", dirty, err)
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
	// A window of 4 × 1 × 2 items, the packed batches that wait or run
	// (2 × 2 + 1 of one item), and the largest conversation, which is
	// taken whole while the window has room.
	var largest int
	if err := catalog.DB.QueryRow(`SELECT MAX(n) FROM (SELECT SUM(chunks) n FROM message_labels GROUP BY conversation_id)`).Scan(&largest); err != nil {
		t.Fatal(err)
	}
	if stats.PeakPending > 8+5+largest || stats.PeakPending >= stats.ItemsLabeled {
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
	// Both thinking items have the same tag. Pick a token from m-think's
	// text that no other framed item contains, so only that message crashes.
	items := liveItems(t, catalog)
	otherTokens := map[int32]bool{}
	for _, item := range items {
		if item.MessageID == "m-think" {
			continue
		}
		ids, _, _ := labelsFrame(model, item)
		for _, id := range ids {
			otherTokens[id] = true
		}
	}
	var poison int32
	found := false
	for _, item := range items {
		if item.MessageID != "m-think" {
			continue
		}
		ids, spans, _ := labelsFrame(model, item)
		for _, id := range ids[spans.Text.Start:spans.Text.End] {
			if !otherTokens[id] {
				poison, found = id, true
				break
			}
		}
	}
	if !found {
		t.Fatal("m-think has no token unique among the framed items")
	}
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

func TestLabelsRetrySingleWorkerCrashInIsolation(t *testing.T) {
	message := &labelsMessage{seqs: [][]int32{{1}}}
	job := &labelsJob{remaining: 1}
	message.job = job
	batch := labelsBatch{seqs: []labelsSeq{{message: message}}}
	run := &labelsRun{stats: &LabelsRunStats{}, progress: func(string, ...any) {}}
	crash := labelsBatchResult{batch: batch, err: &labelrt.WorkerExitError{Status: "exit status 4"}}
	ready := []labelsBatch{{seqs: []labelsSeq{{message: &labelsMessage{}}}}}
	finished := false
	finish := func(*labelsJob) { finished = true }
	if err := run.apply(crash, &ready, finish); err != nil {
		t.Fatal(err)
	}
	if len(ready) != 2 || !ready[0].isolated || ready[0].seqs[0].message != message || message.failed || message.done != 0 || finished {
		t.Fatalf("single crash was not retried first and alone: ready=%+v message=%+v", ready, message)
	}
	crash.batch = ready[0]
	ready = nil
	if err := run.apply(crash, &ready, finish); err != nil {
		t.Fatal(err)
	}
	if len(ready) != 0 || !message.failed || message.done != 1 || !finished {
		t.Fatalf("isolated crash did not fail only its message: ready=%+v message=%+v", ready, message)
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

// The default budget fits both models at the threads Settings chose for
// each: agent events run with their own threads beside labels'.
func TestLabelerBudgetFitsBothModels(t *testing.T) {
	labels := AgentEventSettings{Threads: runtime.NumCPU()}.withPeer(LabelerSettings{Threads: 4, Workers: 1})
	if got, want := labelsBudget(labels).Threads, 4+runtime.NumCPU(); got != want {
		t.Fatalf("labels' budget %d, want %d", got, want)
	}
	events := AgentEventSettings{Threads: 2}.labeler(LabelerSettings{Threads: 4, Workers: 1})
	if events.Threads != 2 || events.PeerThreads != 4 {
		t.Fatalf("agent events' settings: %+v", events)
	}
	if inherited := (AgentEventSettings{}).labeler(LabelerSettings{Threads: 3, Workers: 1}); inherited.Threads != 3 {
		t.Fatalf("without agent_events_threads, agent events use labels_threads: %+v", inherited)
	}
	if got := labelsBudget(events).Threads; got != max(6, runtime.NumCPU()/2) {
		t.Fatalf("agent events' budget %d", got)
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

func TestMessageLabelQueryProjection(t *testing.T) {
	c, config := testCatalog(t)
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,location,title,indexed_at) VALUES('label-work','test','test','label-work','/test','Label work','2026-01-01');
 INSERT INTO conversations(id,workspace_id,provider,account,native_id,coverage,started_at,ended_at) VALUES('label-conv','label-work','claude','test','label-conv','complete','2026-01-01','2026-01-02');
 INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES('label-a','label-conv','a','assistant','message','A report','a','2026-01-01T12:00:00Z'),('label-b','label-conv','b','user','message','Request','b','2026-01-01T13:00:00Z')`)
	read := func() []map[string]any {
		table, _ := c.sqlDatasetFor("message_labels")
		table, _ = table.project([]string{"id", "message_id", "label_status", "label", "family"}).preferCoveringScan(nil)
		rows, err := queryMapsContext(context.Background(), c.reader(), table.v2Source(PharosMessageLabelsSchema()))
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	srv := testServer(t, config, c)
	ribbon := func() map[string]any {
		t.Helper()
		w := apiCall(t, srv, config.APIToken, http.MethodGet, "/api/labels/conversations/label-conv", nil)
		var body map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("fingerprint: %d %s", w.Code, w.Body.String())
		}
		return body
	}
	override(t, &messageLabelFingerprintLimit, 1)
	if body := ribbon(); body["truncated"] != true {
		t.Fatalf("fingerprint sample was not bounded: %v", body)
	}
	rows := read()
	if signal, err := askLabelCoverage(t.Context(), &askSignalRun{c: c}); err != nil || signal.Score != 0 {
		t.Fatalf("absent label ledger suggested: %+v %v", signal, err)
	}
	if len(rows) != 2 || rows[0]["label_status"] != "unlabeled" {
		t.Fatalf("missing ledger must remain unlabelled: %v", rows)
	}
	var ledger int
	if err := c.reader().QueryRow("SELECT count(*) FROM sqlite_master WHERE name='message_labels'").Scan(&ledger); err != nil || ledger != 0 {
		t.Fatal("query initialized labeling", err)
	}
	if err := c.ensureLabelsSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB, `INSERT INTO message_labels_models(model,labels_json) VALUES('test','["P","P.request","C","C.report"]');
 INSERT INTO message_labels(message_id,conversation_id,segment,labels,probs,chunks,tokens,input_hash,model,contract,labeled_at) VALUES('label-a','label-conv','AR',15,x'00000000',1,1,'a','test','test','2026-01-01');
 DELETE FROM message_labels_dirty WHERE conversation_id='label-conv'`)
	for _, row := range read() {
		if row["label_status"] != "unlabeled" {
			t.Fatalf("unfinished labels visible: %v", row)
		}
	}
	execTx(t, c.DB, `INSERT INTO message_labels_conversations(conversation_id,structure,units,built_at) VALUES('label-conv','test',2,'2026-01-01')`)
	rows = read()
	if signal, err := askLabelCoverage(t.Context(), &askSignalRun{c: c}); err != nil || signal.Score <= 0 || signal.Support != 1 {
		t.Fatalf("completed label signal: %+v %v", signal, err)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if row["message_id"] == "label-a" {
			seen[firstString(row["label"])] = true
		}
	}
	if len(rows) != 3 || !seen["P.request"] || !seen["C.report"] || len(seen) != 2 {
		t.Fatalf("ancestor/overlap projection: %v", rows)
	}
	// A pre-upgrade read-only catalog remains queryable without the index;
	// installing the additive index must leave the observation grain unchanged.
	execTx(t, c.DB, `DROP INDEX messages_label_modes_idx`)
	fallback := read()
	if len(fallback) != len(rows) {
		t.Fatalf("indexed/fallback observation count differs: %d / %d", len(rows), len(fallback))
	}
	byID := map[string]map[string]any{}
	for _, row := range rows {
		byID[firstString(row["id"])] = row
	}
	for _, row := range fallback {
		if !maps.Equal(byID[firstString(row["id"])], row) {
			t.Fatalf("indexed/fallback observation differs: %v", row)
		}
	}
	execTx(t, c.DB, messageLabelMessageIndexSQL)
	// The preview's message cap must also bound label decoding and taxonomy.
	// A label on the next message must remain visible to the full reader only.
	execTx(t, c.DB, `INSERT INTO message_labels_models(model,labels_json) VALUES('outside-preview','["C","C.difficulty","C.difficulty.blocker"]');
 INSERT INTO message_labels(message_id,conversation_id,segment,labels,probs,chunks,tokens,input_hash,model,contract,labeled_at) VALUES('label-b','label-conv','H-typed',7,x'00000000',1,1,'b','outside-preview','test','2026-01-01');
 DELETE FROM message_labels_dirty WHERE conversation_id='label-conv'`)
	attach, summary, err := c.messageLabelsAttacherContext(t.Context(), "label-conv", []string{"label-a"})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := summary()
	if err != nil || meta["labeled"] != 1 {
		t.Fatalf("sample label count: %v %v", meta, err)
	}
	outside := map[string]any{"id": "label-b"}
	attach(outside)
	if outside["labels"] != nil {
		t.Fatalf("sample decoded an excluded message: %v", outside)
	}
	body := ribbon()
	conversation := body["conversation"].(map[string]any)
	preview := conversation["messages"].([]any)
	if len(preview) != 1 || preview[0].(map[string]any)["id"] != "label-a" || len(preview[0].(map[string]any)["labels"].([]any)) != 4 {
		t.Fatalf("sample lost selected labels: %v", preview)
	}
	for _, entry := range body["taxonomy"].([]any) {
		if strings.HasPrefix(firstString(entry.(map[string]any)["id"]), "C.difficulty") {
			t.Fatalf("fingerprint decoded excluded taxonomy: %v", entry)
		}
	}
	full, _, err := c.messageLabelsAttacher("label-conv")
	if err != nil {
		t.Fatal(err)
	}
	full(outside)
	if outside["labels"] == nil {
		t.Fatal("full reader lost excluded preview labels")
	}
	_, empty, err := c.messageLabelsAttacherContext(t.Context(), "label-conv", []string{})
	if err != nil {
		t.Fatal(err)
	}
	if meta, err := empty(); err != nil || meta != nil {
		t.Fatalf("empty sample read conversation labels: %v %v", meta, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	_, canceledSummary, err := c.messageLabelsAttacherContext(ctx, "label-conv", []string{"label-a"})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	if _, err := canceledSummary(); !errors.Is(err, context.Canceled) {
		t.Fatalf("label summary ignored cancellation: %v", err)
	}
	if _, _, err := c.messageLabelsAttacherContext(ctx, "label-conv", []string{"label-a"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("label read ignored cancellation: %v", err)
	}
	execTx(t, c.DB, `INSERT INTO message_labels_dirty(conversation_id,token) VALUES('label-conv','dirty')`)
	if signal, err := askLabelCoverage(t.Context(), &askSignalRun{c: c}); err != nil || signal.Score != 0 {
		t.Fatalf("stale label ledger suggested: %+v %v", signal, err)
	}
	if body := ribbon(); body["pending"] != true {
		t.Fatalf("dirty fingerprint not pending: %v", body)
	} else {
		conversation := body["conversation"].(map[string]any)
		message := conversation["messages"].([]any)[0].(map[string]any)
		if len(message["labels"].([]any)) != 0 || message["text"] != "" {
			t.Fatalf("dirty fingerprint leaked labels or text: %v", message)
		}
	}

	for _, row := range read() {
		if row["label_status"] != "unlabeled" {
			t.Fatalf("stale labels visible: %v", row)
		}
	}
}

func TestMessageLabelFacts(t *testing.T) {
	c, config := testCatalog(t)
	ctx := t.Context()
	override(t, &messageLabelFactsBatch, 1)
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,location,title,indexed_at) VALUES('facts-work','test','test','facts-work','/test','Label work','2026-01-01');
 INSERT INTO conversations(id,workspace_id,provider,account,native_id,coverage,model,started_at) VALUES('facts-a','facts-work','claude','test','facts-a','complete','generation-a','2026-01-01'),('facts-b','facts-work','codex','test','facts-b','complete','generation-b','2026-01-01');
 INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES
 ('facts-a1','facts-a','a1','assistant','message','A report','a1','2026-01-01T12:00:00Z'),
 ('facts-a2','facts-a','a2','user','message','Request','a2','2026-01-01T13:00:00Z'),
 ('facts-b1','facts-b','b1','assistant','thinking','Thinking','b1','2026-01-01T14:00:00Z')`)
	read := func(table sqlDataset) map[string]map[string]any {
		t.Helper()
		rows, err := queryMapsContext(ctx, c.reader(), table.v2Source(PharosMessageLabelsSchema()))
		if err != nil {
			t.Fatal(err)
		}
		result := map[string]map[string]any{}
		for _, row := range rows {
			result[firstString(row["id"])] = row
		}
		return result
	}
	parity := func() {
		t.Helper()
		live := messageLabelDataset
		if !c.hasLabelsLedger() {
			live = messageLabelWithoutLedger(live)
		}
		stored, _ := c.sqlDatasetFor("message_labels")
		actual, expected := read(stored), read(live)
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("stored/live differ\nstored: %v\nlive: %v", actual, expected)
		}
	}
	flush := func() {
		t.Helper()
		for n := 0; n < 100; n++ {
			count, err := c.refreshMessageLabelFacts(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if count == 0 {
				return
			}
		}
		t.Fatal("facts did not finish")
	}
	scalar := func(q string) int64 {
		t.Helper()
		var n int64
		if err := c.DB.QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	flush()
	parity()
	if c.hasLabelsLedger() {
		t.Fatal("query maintenance created inference ledger")
	}
	if err := c.ensureLabelsSchema(ctx); err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB, `INSERT INTO message_labels_models(model,labels_json) VALUES('classifier','["P","P.request","P.request.a","P.request.b","C","C.report"]');
 INSERT INTO message_labels(message_id,conversation_id,segment,labels,probs,chunks,tokens,input_hash,model,contract,labeled_at) VALUES
 ('facts-a1','facts-a','AR',63,x'00000000',1,1,'a','classifier','test','2026-01-01'),
 ('facts-a2','facts-a','H-typed',0,x'00000000',1,1,'b','classifier','test','2026-01-01'),
 ('facts-b1','facts-b','AT',48,x'00000000',1,1,'c','classifier','test','2026-01-01');
 INSERT INTO message_labels_conversations(conversation_id,structure,units,built_at) VALUES('facts-a','test',2,'2026-01-01'),('facts-b','test',1,'2026-01-01');
 DELETE FROM message_labels_dirty`)
	parity()
	flush()
	parity()
	// The dashboard and table consume different field subsets. Pruning the
	// compound source must preserve metadata, calendar, coverage and text.
	projectedParity := func() {
		t.Helper()
		stored, _ := c.sqlDatasetFor("message_labels")
		for _, selected := range [][]string{{"text"}, {"model", "provider", "source"}, {"repository_name", "title", "fingerprint"}, {"day", "week", "month"}, {"label", "family", "label_status"}, {"segment", "label_model", "labeled_at"}} {
			fields := append([]string{"id", "message_id"}, selected...)
			if actual, expected := read(stored.project(fields)), read(messageLabelDataset.project(fields)); !reflect.DeepEqual(actual, expected) {
				t.Fatalf("projected fields %v differ: %v / %v", selected, actual, expected)
			}
		}
	}
	projectedParity()
	// Parent + descendant masks emit only leaves. Overlapping children count
	// once as messages in both the parent filter and family grouping.
	srv := testServer(t, config, c)
	query := qt.MetricQuery{Version: 2, Limit: 20, Where: []qt.WhereTerm{{Any: []qt.WhereClause{{Field: "label", Op: "=", Value: "P.request"}, {Field: "label", Op: "starts_with", Value: "P.request."}}}}, Metrics: []qt.AggSpec{{ID: "messages", Op: "count_distinct", Field: "message_id", GroupBy: []string{"family"}}}}
	out, err := srv.executeQueryV2(ctx, "message_labels", nil, PharosMessageLabelsSchema(), nil, &query)
	if err != nil {
		t.Fatal(err)
	}
	metric := out.Metrics.Metrics[0]
	if metric.Error != "" || len(metric.Buckets) != 1 || metric.Buckets[0].Value != float64(1) {
		t.Fatalf("parent message counts: %+v", metric)
	}
	if scalar("SELECT count(*) FROM message_label_facts") != 3 || scalar("SELECT count(*) FROM message_label_memberships") != 5 {
		t.Fatal("fact/membership grain differs")
	}
	// One metadata change touches only one conversation; pending data still
	// agrees with the live projection before the maintenance batch runs.
	before := scalar("SELECT value FROM query_cache_generation WHERE id=1")
	execTx(t, c.DB, `UPDATE conversations SET model='generation-updated' WHERE id='facts-a'`)
	if scalar("SELECT count(*) FROM message_label_facts_dirty") != 1 {
		t.Fatal("unrelated conversations invalidated")
	}
	parity()
	flush()
	parity()
	if scalar("SELECT value FROM query_cache_generation WHERE id=1") <= before {
		t.Fatal("facts publication did not invalidate query cache")
	}
	// A dirty inference ledger must hide old labels immediately, even while
	// other conversations keep reading their stored memberships.
	execTx(t, c.DB, `UPDATE messages SET text='Changed content' WHERE id='facts-a1'`)
	parity()
	projectedParity()
	flush()
	parity()
	execTx(t, c.DB, `UPDATE message_labels SET labels=3 WHERE message_id='facts-a1'; DELETE FROM message_labels_dirty WHERE conversation_id='facts-a'`)
	parity()
	flush()
	parity()
	// Typed-word accounting changes independently of label inference.
	execTx(t, c.DB, `INSERT INTO message_authorship(message_id,conversation_id,workspace_id,sent_at,day,total_chars,typed_words,spans_json) VALUES('facts-a2','facts-a','facts-work','2026-01-01','2026-01-01',7,2,'[]')`)
	parity()
	flush()
	parity()
	execTx(t, c.DB, `UPDATE message_authorship SET typed_words=0 WHERE message_id='facts-a2'`)
	parity()
	flush()
	parity()
	// Model taxonomy replacement invalidates every conversation using it.
	execTx(t, c.DB, `UPDATE message_labels_models SET labels_json='["P","P.request","P.request.renamed","P.request.b","C","C.report"]' WHERE model='classifier'`)
	// Repeated invalidations of an already queued model must remain harmless,
	// including when an outer upsert supplies its own conflict policy.
	execTx(t, c.DB, `UPDATE message_labels_models SET labels_json='["P","P.request","P.request.a","P.request.b","C","C.report"]' WHERE model='classifier'`)
	parity()
	flush()
	parity()
	execTx(t, c.DB, `UPDATE messages SET selected=0 WHERE id='facts-b1'`)
	parity()
	flush()
	parity()
	// Source rowids can be reused before a queued deletion is processed.
	// Fact identity must remain independent of the transcript's physical rowid.
	rowid := scalar("SELECT rowid FROM messages WHERE id='facts-b1'")
	execTx(t, c.DB, `DELETE FROM messages WHERE id='facts-b1';
 INSERT INTO conversations(id,workspace_id,provider,account,native_id,coverage,model) VALUES('facts-0','facts-work','claude','test','facts-0','complete','generation-new')`,
		fmt.Sprintf(`INSERT INTO messages(rowid,id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES(%d,'facts-new','facts-0','new','assistant','message','New','new','2026-01-01T01:00:00Z')`, rowid))
	parity()
	flush()
	parity()
	execTx(t, c.DB, `UPDATE messages SET conversation_id='facts-0' WHERE id='facts-a2'`)
	parity()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.refreshMessageLabelFacts(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled refresh: %v", err)
	}
	flush()
	parity()
	execTx(t, c.DB, `DELETE FROM messages WHERE id='facts-a1'`)
	parity()
	flush()
	parity()
	execTx(t, c.DB, `DELETE FROM conversations WHERE id='facts-a'`)
	parity()
	flush()
	parity()
	// Rebuild the query algorithm independently; preserve inference results,
	// keep reads valid during backfill and resume the queue across restarts.
	ledgerRows := scalar("SELECT count(*) FROM message_labels")
	execTx(t, c.DB, `UPDATE meta SET value='old' WHERE key='message_label_facts_version'`)
	if err := c.ensureMessageLabelFacts(ctx); err != nil {
		t.Fatal(err)
	}
	if scalar("SELECT count(*) FROM meta WHERE key='message_label_facts_ready'") != 0 {
		t.Fatal("old algorithm still accepted")
	}
	parity()
	_, err = c.refreshMessageLabelFacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pending := scalar("SELECT count(*) FROM message_label_facts_dirty")
	if err := c.ensureMessageLabelFacts(ctx); err != nil {
		t.Fatal(err)
	}
	if scalar("SELECT count(*) FROM message_label_facts_dirty") != pending {
		t.Fatal("restart restarted backfill")
	}
	flush()
	parity()
	if scalar("SELECT count(*) FROM message_labels") != ledgerRows {
		t.Fatal("query rebuild altered inference")
	}
	// A local-calendar change invalidates only the query projection too.
	override(t, &time.Local, time.FixedZone("facts-test", -8*60*60))
	if err := c.ensureMessageLabelFacts(ctx); err != nil {
		t.Fatal(err)
	}
	parity()
	flush()
	parity()
	// Read-only callers consume the same stored data without initializing,
	// refreshing or acquiring the encoder.
	readonly, err := openCatalogReadOnly(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	stored, _ := readonly.sqlDatasetFor("message_labels")
	rows, err := queryMapsContext(ctx, readonly.reader(), stored.v2Source(PharosMessageLabelsSchema()))
	if err != nil || len(rows) != 2 {
		t.Fatalf("read-only stored labels: %v %v", rows, err)
	}
}
