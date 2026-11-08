package archive

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
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
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	qt "github.com/Pythia-Software/query-table/backends/go"
	labelrt "github.com/gbdubs/pharos/internal/labeler"
	"github.com/gbdubs/pharos/internal/labeler/labelertest"
	"github.com/gbdubs/pharos/internal/platform/filelock"
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
	for _, device := range []string{"", "cpu", "gpu"} {
		t.Run("device="+device, func(t *testing.T) {
			t.Setenv(labelertest.FakeDeviceEnv, device)
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
			want := []int{1, 4, 2}
			if device == "gpu" {
				want = []int{1}
			}
			if threads := labelerHelloThreads(t, hellos); !slices.Equal(threads, want) {
				t.Fatalf("worker thread targets: %v", threads)
			}
			if stats.Restarts != 0 {
				t.Fatalf("deliberate core changes counted as crashes: %+v", stats)
			}
		})
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

// The run's lease: only another holder is "already running", which the
// analysis chain waits out; any other lock failure keeps its cause. Neither
// labels anything, and both leave the in-process mutex free. A run in this
// process is refused before any lease is tried.
func TestLabelsLockFailures(t *testing.T) {
	catalog := labeledCatalog(t)
	settings := labelsTestSettings(t, "ok")
	labeled := func() (n int) {
		_ = catalog.DB.QueryRow(`SELECT COUNT(*) FROM message_labels`).Scan(&n)
		return n
	}
	for _, tc := range leaseLockFailures() {
		t.Run(tc.name, func(t *testing.T) {
			refused := failFileLease(t, ".labels.lock", tc.err)
			catalog.labels.Lock()
			_, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
			catalog.labels.Unlock()
			if err != errLabelsRunning || len(*refused) != 0 {
				t.Fatalf("beside a run in this process: %v after %d lease attempts", err, len(*refused))
			}
			_, err = catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
			if errors.Is(err, errLabelsRunning) != errors.Is(tc.err, filelock.ErrContended) || !errors.Is(err, tc.err) {
				t.Fatalf("lease failure: %v", err)
			}
			requireLeasesClosed(t, *refused, 1)
			if !catalog.labels.TryLock() {
				t.Fatal("a failed lease kept the run's mutex")
			}
			catalog.labels.Unlock()
			if n := labeled(); n != 0 {
				t.Fatalf("labeled %d messages without the lease", n)
			}
		})
	}
	if stats, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{}); err != nil || stats.MessagesLabeled == 0 {
		t.Fatalf("a run after the failures: %+v %v", stats, err)
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

func TestLabelsWriteContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, stop := context.WithCancel(context.Background())
		defer stop()
		ctx, cancel := labelsWriteContext(parent)
		defer cancel()
		// Virtual time: a live writer may wait longer than the stop grace.
		timer := time.NewTimer(2 * labelsWriteGrace)
		<-timer.C
		if ctx.Err() != nil {
			t.Fatalf("a running writer expired while waiting: %v", ctx.Err())
		}
		stoppedAt := time.Now()
		stop()
		synctest.Wait()
		if ctx.Err() != nil {
			t.Fatalf("stopping left no time to save finished work: %v", ctx.Err())
		}
		<-ctx.Done()
		if elapsed := time.Since(stoppedAt); elapsed != labelsWriteGrace {
			t.Fatalf("stop drain lasted %s, want %s", elapsed, labelsWriteGrace)
		}
		cancel() // Cleanup is safe even after the grace expires.
	})
}

func TestLabelsWriterDrainsWhileStopping(t *testing.T) {
	for _, releaseWriter := range []bool{true, false} {
		t.Run(fmt.Sprintf("catalog_available=%v", releaseWriter), func(t *testing.T) {
			if !releaseWriter {
				override(t, &labelsWriteGrace, 100*time.Millisecond)
			}
			catalog := labeledCatalog(t)
			if err := catalog.ensureLabelsSchema(t.Context()); err != nil {
				t.Fatal(err)
			}
			execTx(t, catalog.DB, `INSERT INTO message_labels_dirty(conversation_id,token) VALUES('c-root','pending')
				ON CONFLICT(conversation_id) DO UPDATE SET token=excluded.token`)
			release, err := catalog.acquireWriteGate(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if release != nil {
					release()
				}
			}()
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			run := &labelsRun{c: catalog, identity: "test", stats: &LabelsRunStats{}, cancel: stop}
			jobs := make(chan *labelsJob, 1)
			jobs <- &labelsJob{conversation: "c-root", token: "pending", structure: "test", units: 1, complete: true,
				messages: []*labelsMessage{{id: "m-h1", conversation: "c-root", segment: "H-typed", hash: "test", seqs: [][]int32{{1}}, probs: []float32{1}, mask: 1}}}
			close(jobs)
			done := make(chan error, 1)
			go func() { done <- run.write(ctx, jobs) }()
			waitUntil(t, 5*time.Second, "the label writer to queue", catalog.writersWaiting)
			stop()
			if releaseWriter {
				release()
				release = nil
			}
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("stopping waited indefinitely for the catalog")
			}
			var built, dirty int
			if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM message_labels_conversations WHERE conversation_id='c-root'`).Scan(&built); err != nil {
				t.Fatal(err)
			}
			if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM message_labels_dirty WHERE conversation_id='c-root'`).Scan(&dirty); err != nil {
				t.Fatal(err)
			}
			if releaseWriter {
				if err != nil || labelCount(t, catalog, "") != 1 || run.stats.MessagesLabeled != 1 || built != 1 || dirty != 0 {
					t.Fatalf("finished labels were not saved during stop: %+v built=%d dirty=%d err=%v", run.stats, built, dirty, err)
				}
			} else if !errors.Is(err, context.Canceled) || labelCount(t, catalog, "") != 0 || run.stats.MessagesLabeled != 0 || built != 0 || dirty != 1 {
				t.Fatalf("blocked stop lost its pending work: %+v built=%d dirty=%d err=%v", run.stats, built, dirty, err)
			}
		})
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
	// Lower the backend budget so the existing long-message fixture needs
	// multiple requests, even though its chunks fit the row limit.
	t.Setenv(labelertest.FakeMaxTokensEnv, "512")
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
	t.Run("long message respects backend token budget", func(t *testing.T) {
		model, err := labelrt.LoadModel(settings.ModelDir, settings.Variant)
		if err != nil {
			t.Fatal(err)
		}
		explained, err := catalog.LabelsExplain(t.Context(), settings, "m-n2", true)
		if err != nil {
			t.Fatal(err)
		}
		items := explained["items"].([]map[string]any)
		inferred := explained["inferred"].(map[string]any)
		chunks := inferred["chunks"].([]map[string]any)
		if len(items) < 2 || len(chunks) != len(items) {
			t.Fatalf("long explanation lost chunks: %d items, %d inferred", len(items), len(chunks))
		}
		longest := 0
		var union uint64
		for row, item := range items {
			ids, _ := model.Tokenizer.FrameWithSpans(labelrt.FrameInput{
				Segment: item["segment"].(string), Cohort: item["cohort"].(string), Sender: item["sender_role"].(string),
				Context: item["context"].(string), Text: item["text"].(string),
			})
			longest = max(longest, len(ids))
			probs := make([]float32, model.Labels())
			for label := range probs {
				probs[label] = labelertest.FakeProb(ids, label)
			}
			if !reflect.DeepEqual(chunks[row]["probs"], labelProbMap(model, probs)) {
				t.Fatalf("chunk %d probabilities lost their input order", row)
			}
			union |= model.Decide(probs, item["segment"].(string))
		}
		if len(items)*labelrt.PadLength(longest, model.PadMultiple) <= 512 {
			t.Fatal("fixture does not exceed the backend token budget")
		}
		if !reflect.DeepEqual(inferred["labels"], model.LabelNames(union)) {
			t.Fatal("chunk labels were not merged")
		}
	})
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
	// A session step under a message limit reports the poisoned conversation
	// failed, not cut by the limit, so the scheduler backs off instead of
	// reselecting it forever.
	var poisoned string
	if err := catalog.DB.QueryRow(`SELECT conversation_id FROM messages WHERE id='m-think'`).Scan(&poisoned); err != nil {
		t.Fatal(err)
	}
	session, err := catalog.newLabelsSession(settings, workAllocation{CPUs: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	step, err := session.Run(t.Context(), []string{poisoned}, labelsChainLimit)
	session.Close()
	// Its crashes count against the warm pool's restarts; start the
	// stage below from fresh workers.
	catalog.labelerManager(settings).Retire()
	if err != nil || !slices.Equal(step.Failed, []string{poisoned}) || len(step.Remaining) != 0 {
		t.Fatalf("a limited step over a poison item: %+v %v", step, err)
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

// Under a work grant the CPU authority is the one thread budget (M1): on
// battery both models' runners start side by side within their grants, where
// the manager's own battery budget would refuse the second. Memory (the model
// cache) is still bounded, and a caller without a grant keeps its budget.
func TestAdmittedLabelerLeavesThreadsToTheAuthority(t *testing.T) {
	c, _ := testCatalog(t)
	plugged := labelsTestSettings(t, "ok")
	plugged.Threads = 4
	settings, _ := labelsForPower(plugged, powerState{Battery: true, OnBattery: true})
	if got := labelsBudget(admittedLabeler(t.Context(), settings)); got != labelsBudget(settings) || got.Threads != labelsBatteryThreads {
		t.Fatalf("a caller without a grant: %+v", got)
	}
	admitted := withWorkAdmission(t.Context(), newWorkAuthority(8), workAllocation{CPUs: 4})
	if got := labelsBudget(admittedLabeler(admitted, settings)); got.Threads != 0 || got.MemoryMB != settings.MemoryBudgetMB {
		t.Fatalf("an admitted caller's manager budget: %+v", got)
	}
	model, err := labelrt.LoadModel(settings.ModelDir, settings.Variant)
	if err != nil {
		t.Fatal(err)
	}
	labels := labelrt.RunnerConfig{Worker: settings.Worker, Workers: 1, Threads: settings.Threads}
	events := labels
	events.InFlight = 1 // another runner key: the other model's resident workers
	_, release, err := c.labelerRunner(admitted, admittedLabeler(admitted, settings), model, labels)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, releaseEvents, err := c.labelerRunner(admitted, admittedLabeler(admitted, settings), model, events)
	if err != nil {
		t.Fatalf("a granted second model was refused by the manager's thread budget: %v", err)
	}
	defer releaseEvents()
	classic := labels
	classic.MaxBatch = 1
	if _, _, err := c.labelerRunner(t.Context(), settings, model, classic); !errors.Is(err, labelrt.ErrBudget) {
		t.Fatalf("a classic caller lost its own budget: %v", err)
	}
	// Admission is no saved value: a labels_thread_budget of -1 is the
	// classic default, as any value below one is.
	saved, unset := plugged, plugged
	saved.ThreadBudget, unset.ThreadBudget = -1, 0
	if got := labelsBudget(saved); got != labelsBudget(unset) || got.Threads <= 0 {
		t.Fatalf("a saved budget of -1: %+v", got)
	}

	// RunLabels under an authority admits discovery at one slot, outside the
	// labeling grant, and labeling at the worker's threads beside
	// preparation. Its worker answers to the authority's budget, not the
	// saved one that would refuse it.
	catalog := labeledCatalog(t)
	run := labelsTestSettings(t, "ok")
	run.Threads, run.ThreadBudget = 2, 1
	hellos := filepath.Join(t.TempDir(), "hellos.jsonl")
	t.Setenv(labelertest.FakeHelloEnv, hellos)
	authority := newWorkAuthority(4)
	discovered, discoverySlots := -1, -1
	lock := lockFileLease
	override(t, &lockFileLease, func(file *os.File, mode filelock.Mode, wait filelock.WaitMode) error {
		if strings.HasSuffix(file.Name(), ".labels.lock") {
			discoverySlots = authority.facts().CPUAllocated
		}
		return lock(file, mode, wait)
	})
	progress := labelsProgressFunc(func(line string) {
		if strings.Contains(line, "to rebuild") {
			discovered = authority.facts().CPUAllocated
		}
	})
	stats, err := catalog.RunLabels(withWorkAuthority(t.Context(), authority), run, LabelsRunOptions{Progress: progress})
	if err != nil || !stats.Complete || stats.MessagesLabeled == 0 {
		t.Fatalf("an admitted run: %+v %v", stats, err)
	}
	if discoverySlots != 1 || discovered != 0 || authority.facts().CPUAllocated != 0 {
		t.Fatalf("slots during discovery %d, after discovery %d, after run %d", discoverySlots, discovered, authority.facts().CPUAllocated)
	}
	if threads := labelerHelloThreads(t, hellos); !slices.Equal(threads, []int{2}) {
		t.Fatalf("an admitted run's workers: %v threads", threads)
	}
	// GPU-only inference needs one CPU beside at most four preparation
	// workers. Do not reserve unused CPUs for a long pass.
	gpuAuthority := newWorkAuthority(8)
	gpu, err := acquireLabelerCPU(withWorkAuthority(t.Context(), gpuAuthority), nil, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gpu.Release)
	if gpu.Allocation.CPUs != 5 {
		t.Fatalf("GPU-only pipeline reserved %d CPUs for five workers", gpu.Allocation.CPUs)
	}
	gpu.Release()

	// A cap lowered while a larger pipeline waits retries at the new cap.
	a := newWorkAuthority(8)
	ctx := withWorkAuthority(t.Context(), a)
	occupied, err := acquireWorkCPU(ctx, nil, 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Release()
	done := make(chan *workCPUGrant, 1)
	failure := make(chan error, 1)
	go func() {
		grant, err := acquireLabelerCPU(ctx, nil, 4, false)
		done <- grant
		failure <- err
	}()
	waitUntil(t, 5*time.Second, "labeling to wait for its grant", func() bool { return a.facts().CPUWaiters == 1 })
	a.SetCPUs(2)
	occupied.Release()
	grant := <-done
	if err := <-failure; err != nil {
		t.Fatal(err)
	}
	defer grant.Release()
	if grant.Allocation.CPUs != 2 {
		t.Fatalf("pipeline did not adopt the lowered allowance: %+v", grant.Allocation)
	}
}

// labelsProgressFunc receives each progress line.
type labelsProgressFunc func(string)

func (f labelsProgressFunc) Write(p []byte) (int, error) {
	f(string(p))
	return len(p), nil
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

func TestWorkspaceLabelFingerprint(t *testing.T) {
	c, config := testCatalog(t)
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('ribbon-work','test','test','ribbon-work','Ribbon work','2026-01-01'),('elsewhere','test','test','elsewhere','Other work','2026-01-01');
 INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('first','ribbon-work','claude','test','first'),('second','ribbon-work','claude','test','second'),('outside','elsewhere','claude','test','outside');
 INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES('first-a','first','a','assistant','message','Private report','a','2026-01-01T12:00:00Z'),('second-a','second','a','user','message','Private request','b','2026-01-01T13:00:00Z'),('outside-a','outside','a','user','message','Other workspace','c','2026-01-01T11:00:00Z');
 INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,depth,provider,parent_id) VALUES('first-main','ribbon-work','first','main','root',0,'claude',NULL),('first-middle','ribbon-work','first','middle','subagent',1,'claude','first-main'),('first-child','ribbon-work','first','child','subagent',2,'claude','first-middle'),('first-unused','ribbon-work','first','unused','subagent',1,'claude','first-main');
 INSERT INTO agent_session_messages(agent_session_id,message_id,source_order) VALUES('first-child','first-a',0)`)
	srv := testServer(t, config, c)
	read := func(id string, status int) map[string]any {
		t.Helper()
		w := apiCall(t, srv, config.APIToken, http.MethodGet, "/api/labels/workspaces/"+id, nil)
		var body map[string]any
		if w.Code != status || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("workspace fingerprint: %d %s", w.Code, w.Body.String())
		}
		return body
	}
	read("missing", 404)
	check := func(body map[string]any, count int) []any {
		t.Helper()
		conversation := body["conversation"].(map[string]any)
		messages := conversation["messages"].([]any)
		if conversation["work_id"] != "ribbon-work" || len(messages) != count {
			t.Fatalf("wrong workspace evidence: %v", conversation)
		}
		for _, value := range messages {
			m := value.(map[string]any)
			if m["conversation_id"] == "outside" || m["text"] != "" || m["work_id"] != "ribbon-work" || firstString(m["session_id"]) == "" {
				t.Fatalf("wrong or private ribbon evidence: %v", m)
			}
		}
		return messages
	}
	body := read("ribbon-work", 200)
	messages := check(body, 2)
	if messages[0].(map[string]any)["id"] != "first-a" || messages[1].(map[string]any)["session_id"] != "second" {
		t.Fatalf("lost message order or fallback session: %v", messages)
	}
	sessions := body["conversation"].(map[string]any)["sessions"].([]any)
	if len(sessions) != 4 || messages[0].(map[string]any)["session_id"] != "first-child" {
		t.Fatalf("lost recorded ancestors or included unused branches: %v", sessions)
	}
	var ledger int
	if err := c.reader().QueryRow("SELECT count(*) FROM sqlite_master WHERE name='message_labels'").Scan(&ledger); err != nil || ledger != 0 {
		t.Fatal("fingerprint initialized labeling", err)
	}
	if err := c.ensureLabelsSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB, `INSERT INTO message_labels_models(model,labels_json) VALUES('ribbon-test','["P","C"]');
 INSERT INTO message_labels(message_id,conversation_id,segment,labels,probs,chunks,tokens,input_hash,model,contract,labeled_at) VALUES('first-a','first','AR',2,x'00000000',1,1,'a','ribbon-test','test','2026-01-01'),('second-a','second','HU',1,x'00000000',1,1,'b','ribbon-test','test','2026-01-01');
 INSERT INTO message_labels_conversations(conversation_id,structure,units,built_at) VALUES('first','test',1,'2026-01-01'),('second','test',1,'2026-01-01');
 DELETE FROM message_labels_dirty;
 INSERT INTO message_labels_dirty(conversation_id,token) VALUES('second','dirty')`)
	body = read("ribbon-work", 200)
	messages = check(body, 2)
	if body["pending"] != true || len(messages[0].(map[string]any)["labels"].([]any)) != 1 || len(messages[1].(map[string]any)["labels"].([]any)) != 0 || messages[1].(map[string]any)["chunks"] != float64(0) {
		t.Fatalf("mixed freshness hid valid labels or exposed stale labels: %v", body)
	}
	// An accepted empty classification carries processed chunks; stale or
	// missing classifications must stay distinguishable from that no-match.
	execTx(t, c.DB, `UPDATE message_labels SET labels=0 WHERE message_id='first-a'`)
	messages = check(read("ribbon-work", 200), 2)
	if len(messages[0].(map[string]any)["labels"].([]any)) != 0 || messages[0].(map[string]any)["chunks"] != float64(1) || messages[1].(map[string]any)["chunks"] != float64(0) {
		t.Fatalf("no-match and pending classifications are indistinguishable: %v", messages)
	}
	execTx(t, c.DB, `DELETE FROM message_labels WHERE message_id='second-a'; DELETE FROM message_labels_dirty WHERE conversation_id='second'`)
	messages = check(read("ribbon-work", 200), 2)
	if messages[1].(map[string]any)["chunks"] != nil || len(messages[1].(map[string]any)["labels"].([]any)) != 0 {
		t.Fatalf("missing classification looks processed: %v", messages[1])
	}
	override(t, &messageLabelFingerprintLimit, 1)
	body = read("ribbon-work", 200)
	check(body, 1)
	if body["truncated"] != true {
		t.Fatalf("workspace sample was not bounded: %v", body)
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
		rows, err := queryMapsContext(context.Background(), c.reader(), table.querySourceSQL(PharosMessageLabelsSchema()))
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
	c.ops = newOpRegistry("service")
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
		rows, err := queryMapsContext(ctx, c.reader(), table.querySourceSQL(PharosMessageLabelsSchema()))
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
		pass := c.beginPass(ctx, "maintenance.labels.query", triggerChained)
		defer pass.end(nil)
		var progress activityWork
		runCtx := withWorkProgress(ctx, func(work activityWork) { progress = work })
		for n := 0; n < 100; n++ {
			var count int
			var err error
			pass.runIO(runCtx, func(ctx context.Context) { count, err = c.refreshMessageLabelFacts(ctx) })
			if err != nil {
				t.Fatal(err)
			}
			if count == 0 {
				return
			}
			if progress.Unit != "conversation" || progress.Done <= 0 || progress.Total < progress.Done {
				t.Fatalf("message-label query indexing did not report its committed conversations: %+v", progress)
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
	// A selected batch publishes only its conversations and claims no
	// readiness; an empty selection publishes nothing.
	execTx(t, c.DB, `DELETE FROM meta WHERE key='message_label_facts_ready'`)
	if step, err := c.refreshMessageLabelFactsFor(ctx, nil, 0); err != nil || len(step.Done)+len(step.Remaining) != 0 {
		t.Fatalf("an empty selection: %+v %v", step, err)
	}
	if pending, err := c.messageLabelFactsPendingFor(ctx, []string{"facts-a", "facts-b"}); err != nil || !slices.Equal(pending, []string{"facts-a", "facts-b"}) {
		t.Fatalf("pending facts: %v %v", pending, err)
	}
	step, err := c.refreshMessageLabelFactsFor(ctx, []string{"facts-b"}, 0)
	if err != nil || !slices.Equal(step.Done, []string{"facts-b"}) || scalar(`SELECT COUNT(*) FROM message_label_facts_dirty WHERE conversation_id='facts-a'`) != 1 ||
		scalar(`SELECT COUNT(*) FROM meta WHERE key='message_label_facts_ready'`) != 0 {
		t.Fatalf("a selected batch: %+v %v", step, err)
	}
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
	query := queryFixtureMetrics{Version: 2, Limit: 20, Where: []qt.WhereTerm{{Any: []qt.WhereClause{{Field: "label", Op: "=", Value: "P.request"}, {Field: "label", Op: "starts_with", Value: "P.request."}}}}, Metrics: []qt.AggSpec{{ID: "messages", Op: "count_distinct", Field: "message_id", GroupBy: []string{"family"}}}}
	out, err := srv.executeQueryFixture(ctx, "message_labels", nil, PharosMessageLabelsSchema(), nil, &query)
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
	identities := func() map[string]int64 {
		t.Helper()
		rows, err := queryMapsContext(ctx, c.DB, "SELECT message_id,rowid FROM message_label_facts")
		if err != nil {
			t.Fatal(err)
		}
		ids := map[string]int64{}
		for _, row := range rows {
			ids[firstString(row["message_id"])] = integer(row["rowid"])
		}
		return ids
	}
	originalIDs := identities()
	// An unchanged refresh does not rewrite either persistent projection, even
	// when several invalidations coalesced into the same queue entry.
	execTx(t, c.DB, `INSERT OR IGNORE INTO message_label_facts_dirty VALUES('facts-a');
 INSERT OR IGNORE INTO message_label_facts_dirty VALUES('facts-a')`)
	if count, timing, err := c.refreshMessageLabelFactsBudget(ctx, messageLabelFactsMessages); err != nil || count != 1 || timing.changedFacts != 0 || timing.changedMemberships != 0 {
		t.Fatalf("unchanged refresh: count=%d timing=%+v error=%v", count, timing, err)
	}
	if !reflect.DeepEqual(identities(), originalIDs) {
		t.Fatal("unchanged refresh replaced fact identities")
	}
	parity()
	// Source writes can finish while preparation holds a read snapshot. A
	// stale preparation must not publish its old inputs or consume the queue.
	execTx(t, c.DB, `INSERT INTO message_label_facts_dirty VALUES('facts-a')`)
	func() {
		conn, err := c.DB.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		read, err := conn.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer read.Rollback()
		build, err := prepareMessageLabelFacts(ctx, read, messageLabelFactsMessages, nil)
		if err != nil {
			t.Fatal(err)
		}
		writeCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if err := c.writeTransaction(writeCtx, "labels during query preparation", func(tx *sql.Tx) error {
			_, err := tx.ExecContext(writeCtx, "UPDATE message_labels SET labeled_at='2026-01-02' WHERE message_id='facts-a1'")
			return err
		}); err != nil {
			t.Fatalf("preparing query facts blocked a source writer: %v", err)
		}
		if err := read.Commit(); err != nil {
			t.Fatal(err)
		}
		tx, finish, err := c.beginTrackedWriteUsing(ctx, "stale label facts", conn)
		if err != nil {
			t.Fatal(err)
		}
		defer finish()
		defer tx.Rollback()
		if err := publishMessageLabelFacts(ctx, tx, build, &messageLabelFactsTiming{}); !errors.Is(err, errMessageLabelFactsStale) {
			t.Fatalf("stale preparation: %v", err)
		}
	}()
	if scalar("SELECT count(*) FROM message_label_facts_dirty") != 1 {
		t.Fatal("stale preparation consumed its invalidation")
	}
	flush()
	parity()
	if !reflect.DeepEqual(identities(), originalIDs) {
		t.Fatal("label metadata update replaced fact identities")
	}
	// One metadata change touches only one conversation; pending data still
	// agrees with the live projection before the maintenance batch runs.
	before := scalar("SELECT value FROM query_cache_generation WHERE id=1")
	execTx(t, c.DB, `UPDATE conversations SET model='generation-updated' WHERE id='facts-a'`)
	if scalar("SELECT count(*) FROM message_label_facts_dirty") != 1 {
		t.Fatal("unrelated conversations invalidated")
	}
	parity()
	if count, timing, err := c.refreshMessageLabelFactsBudget(ctx, messageLabelFactsMessages); err != nil || count != 1 || timing.changedFacts != 1 || timing.changedMemberships != 0 {
		t.Fatalf("metadata refresh rewrote unrelated rows: count=%d timing=%+v error=%v", count, timing, err)
	}
	flush()
	parity()
	if !reflect.DeepEqual(identities(), originalIDs) {
		t.Fatal("conversation metadata update replaced fact identities")
	}
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
	// A failure after updating facts rolls the entire publication back,
	// including its queue removal and any membership changes.
	var beforeFailure sql.NullString
	if err := c.DB.QueryRow("SELECT labeled_at FROM message_label_facts WHERE message_id='facts-a1'").Scan(&beforeFailure); err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB, `UPDATE message_labels SET labels=48,labeled_at='2026-01-03' WHERE message_id='facts-a1';
 DELETE FROM message_labels_dirty WHERE conversation_id='facts-a';
 CREATE TRIGGER fail_label_membership BEFORE INSERT ON message_label_memberships BEGIN SELECT RAISE(ABORT,'test membership failure'); END`)
	if _, err := c.refreshMessageLabelFacts(ctx); err == nil || !strings.Contains(err.Error(), "test membership failure") {
		t.Fatalf("publication failure: %v", err)
	}
	if scalar("SELECT count(*) FROM message_label_facts_dirty") != 1 || !reflect.DeepEqual(identities(), originalIDs) {
		t.Fatal("failed publication changed identities or consumed its queue")
	}
	execTx(t, c.DB, "DROP TRIGGER fail_label_membership")
	// The old physical projection is still intact; the live fallback already
	// exposes the changed source while maintenance retries.
	var labeledAt sql.NullString
	if err := c.DB.QueryRow("SELECT labeled_at FROM message_label_facts WHERE message_id='facts-a1'").Scan(&labeledAt); err != nil || labeledAt != beforeFailure {
		t.Fatalf("failed publication changed stored facts: %v %v (prior value %v)", labeledAt, err, beforeFailure)
	}
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
	if identities()["facts-a2"] != originalIDs["facts-a2"] {
		t.Fatal("moving a message replaced its fact identity")
	}
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
	rows, err := queryMapsContext(ctx, readonly.reader(), stored.querySourceSQL(PharosMessageLabelsSchema()))
	if err != nil || len(rows) != 2 {
		t.Fatalf("read-only stored labels: %v %v", rows, err)
	}
}

func TestMessageLabelFactsBudget(t *testing.T) {
	c, _ := testCatalog(t)
	ctx := t.Context()
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,location,title,indexed_at) VALUES('budget-work','test','test','budget-work','/test','Budget','2026-01-01');
 INSERT INTO conversations(id,workspace_id,provider,account,native_id,coverage,model) VALUES
 ('budget-a','budget-work','codex','test','a','complete','old'),('budget-b','budget-work','codex','test','b','complete','old'),
 ('budget-c','budget-work','codex','test','c','complete','old'),('budget-d','budget-work','codex','test','d','complete','old');
 INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash) VALUES
 ('budget-a1','budget-a','a1','assistant','message','a','a'),('budget-a2','budget-a','a2','assistant','message','b','b'),
 ('budget-a3','budget-a','a3','assistant','message','c','c'),('budget-b1','budget-b','b1','assistant','message','d','d'),
 ('budget-c1','budget-c','c1','tool','tool_result','tool output','tool'),('budget-d1','budget-d','d1','assistant','message','e','e')`)
	// Hold the gate until the read preparation has finished, then simulate a
	// source commit from another process. Refresh must retry with the new data.
	release, err := c.acquireWriteGate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	type result struct {
		count int
		err   error
	}
	done := make(chan result, 1)
	go func() {
		count, _, err := c.refreshMessageLabelFactsBudget(ctx, 2)
		done <- result{count, err}
	}()
	waitUntil(t, 5*time.Second, "prepared label facts to wait for publication", c.writersWaiting)
	execTx(t, c.DB, "UPDATE conversations SET model='new' WHERE id='budget-a'")
	release()
	released = true
	select {
	case got := <-done:
		if got.err != nil || got.count != 1 {
			t.Fatalf("oversized conversation did not finish atomically: %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not retry its stale preparation")
	}
	var rows, current int
	if err := c.DB.QueryRow("SELECT count(*),sum(model='new') FROM message_label_facts").Scan(&rows, &current); err != nil || rows != 3 || current != 3 {
		t.Fatalf("partial or stale conversation facts: rows=%d current=%d error=%v", rows, current, err)
	}
	// Tool-only conversations cost no eligible-message budget. The remaining
	// two prose messages and the empty projection fit in the next transaction.
	if count, _, err := c.refreshMessageLabelFactsBudget(ctx, 2); err != nil || count != 3 {
		t.Fatalf("bounded continuation: count=%d error=%v", count, err)
	}
	if count, err := c.refreshMessageLabelFacts(ctx); err != nil || count != 0 {
		t.Fatalf("budgeted refresh did not drain: count=%d error=%v", count, err)
	}
	if next := messageLabelFactsNextBudget(100, messageLabelFactsTarget*2); next < 1 || next >= 100 {
		t.Fatalf("slow publication did not reduce its budget: %d", next)
	}
	if next := messageLabelFactsNextBudget(100, messageLabelFactsTarget/2); next <= 100 || next > messageLabelFactsMessages*4 {
		t.Fatalf("fast publication did not grow its bounded budget: %d", next)
	}
}

func TestLabelerBackendSettings(t *testing.T) {
	base := t.TempDir()
	t.Setenv("PHAROS_ORT_LIBRARY", "")
	t.Setenv("PHAROS_MLX_LIBRARY", "/environment/libpharos_mlx.dylib")
	defaults := labelerSettingsFrom(nil, base)
	if defaults.Backend != "auto" || defaults.MLXRuntime != "/environment/libpharos_mlx.dylib" {
		t.Fatalf("defaults: %+v", defaults)
	}
	configured := labelerSettingsFrom(map[string]any{"labels_backend": "mlx", "labels_mlx_runtime": "native/engine.dylib", "labels_runtime": "native/ort.dylib"}, base)
	if configured.Backend != "mlx" || configured.MLXRuntime != filepath.Join(base, "native/engine.dylib") {
		t.Fatalf("configured: %+v", configured)
	}
	want := []string{"--runtime", filepath.Join(base, "native/ort.dylib"), "--backend", "mlx", "--mlx-runtime", filepath.Join(base, "native/engine.dylib")}
	if got := runtimeArgs(configured); !slices.Equal(got, want) {
		t.Fatalf("worker arguments: %v", got)
	}
}

// Preparation overlaps across conversations, but neither an out-of-order
// result nor a blocked consumer may grow the preparation window.
func TestLabelsPreparationScheduling(t *testing.T) {
	for _, mode := range []string{"complete", "stop", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				started := make(chan int, 3)
				releaseFirst, consuming, releaseConsumer := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var active, total atomic.Int32
				var order []int
				done := make(chan error, 1)
				go func() {
					done <- prepareConversations(ctx, 12, 3, func(ctx context.Context, worker, index int) int {
						active.Add(1)
						defer active.Add(-1)
						total.Add(1)
						if index < 3 {
							started <- index
						}
						if index == 0 {
							select {
							case <-releaseFirst:
							case <-ctx.Done():
							}
						}
						return index
					}, func(index, value int) bool {
						order = append(order, value)
						if index == 0 {
							close(consuming)
							<-releaseConsumer
						}
						return mode != "stop"
					})
				}()
				for range 3 {
					<-started // All three start while the first is still blocked.
				}
				synctest.Wait()
				if total.Load() != 3 {
					t.Fatal("preparation ran beyond its reorder window")
				}
				if mode == "cancel" {
					cancel()
				} else {
					close(releaseFirst)
					<-consuming
					synctest.Wait()
					if total.Load() != 3 {
						t.Fatal("a blocked consumer did not stop preparation")
					}
					close(releaseConsumer)
				}
				err := <-done
				if mode == "cancel" && !errors.Is(err, context.Canceled) || mode != "cancel" && err != nil {
					t.Fatalf("%s: %v", mode, err)
				}
				want := 12
				if mode == "stop" {
					want = 1
				} else if mode == "cancel" {
					want = 0
				}
				if len(order) != want || active.Load() != 0 {
					t.Fatalf("delivered %v; active workers %d", order, active.Load())
				}
				for index, value := range order {
					if value != index {
						t.Fatalf("delivery order %v", order)
					}
				}
			})
		})
	}
}

func TestLabelsParallelPreparationMatchesSerial(t *testing.T) {
	settings := labelsTestSettings(t, "ok")
	var want []map[string]any
	for _, threads := range []int{1, 4} {
		catalog := labeledCatalog(t)
		settings.Threads = threads
		stats, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{})
		if err != nil || !stats.Complete || stats.MessagesFailed != 0 || stats.ConversationsFailed != 0 {
			t.Fatalf("threads=%d: %+v %v", threads, stats, err)
		}
		rows, err := queryMaps(catalog.DB, `SELECT message_id,conversation_id,segment,labels,hex(probs) probs,chunks,tokens,input_hash FROM message_labels ORDER BY message_id`)
		if err != nil {
			t.Fatal(err)
		}
		if threads == 1 {
			want = rows
		} else if len(rows) == 0 || !reflect.DeepEqual(rows, want) {
			t.Fatalf("parallel preparation changed stored labels: %v, want %v", rows, want)
		}
	}
}

// labelsDirtyToken is a conversation's labels dirty token ("" when clean).
func labelsDirtyToken(t *testing.T, catalog *Catalog, id string) string {
	t.Helper()
	var token string
	if err := catalog.DB.QueryRow(`SELECT COALESCE((SELECT token FROM message_labels_dirty WHERE conversation_id=?),'')`, id).Scan(&token); err != nil {
		t.Fatal(err)
	}
	return token
}

// A recent source with historical forks and a re-sender outside the
// selection: the selected step stores only the source, every dependent keeps
// a durable mark through each interruption, and classic discovery alone (a
// new process, no session) reaches what a full run reaches.
func TestLabelsSessionKeepsDependentsOutsideTheSelection(t *testing.T) {
	catalog := labeledCatalog(t)
	settings := labelsTestSettings(t, "ok")
	if stats, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{}); err != nil || !stats.Complete {
		t.Fatalf("reference run: %+v %v", stats, err)
	}
	built, _ := catalog.metaValue(t.Context(), "labels_built_at")
	dependents := []string{"c-other", "c-late"}
	rows, err := queryMapsContext(t.Context(), catalog.DB, `SELECT id FROM conversations WHERE parent_id='c-root'`)
	if err != nil || len(rows) == 0 {
		t.Fatalf("the fixture has no forks: %v", err)
	}
	for _, row := range rows {
		dependents = append(dependents, firstString(row["id"]))
	}
	execTx(t, catalog.DB, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('m-s1','c-root','m-s1','assistant','message','Shipped the recent change.','hash-m-s1','2026-09-01T15:00:00.000Z',1200)`)
	session, err := catalog.newLabelsSession(settings, workAllocation{CPUs: 2})
	if err != nil {
		t.Fatal(err)
	}
	// Interrupted before discovery commits: nothing durable changed.
	stopped, stop := context.WithCancel(t.Context())
	stop()
	if err := session.Discover(stopped); err == nil || session.Discovered() {
		t.Fatalf("a stopped discovery succeeded: %v", err)
	}
	if err := session.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, id := range dependents {
		if labelsDirtyToken(t, catalog, id) == "" {
			t.Fatalf("discovery stored no mark for %s before any selected label", id)
		}
	}
	// Interrupted after the dependents' marks, before the source's labels.
	ctx, cancel := context.WithCancel(t.Context())
	session.beforeRun = func(context.Context) { cancel() }
	step, err := session.Run(ctx, []string{"c-root"}, 0)
	if !errors.Is(err, context.Canceled) || !slices.Equal(step.Remaining, []string{"c-root"}) || labelsDirtyToken(t, catalog, "c-root") == "" || labelCount(t, catalog, "WHERE message_id='m-s1'") != 0 {
		t.Fatalf("an interrupted step: %+v %v", step, err)
	}
	session.beforeRun = nil
	step, err = session.Run(t.Context(), []string{"c-root"}, 0)
	if err != nil || !slices.Equal(step.Done, []string{"c-root"}) || labelCount(t, catalog, "WHERE message_id='m-s1'") != 1 || labelsDirtyToken(t, catalog, "c-root") != "" {
		t.Fatalf("the selected source: %+v %v", step, err)
	}
	// Its publication is no claim about the library, and its dependents stay
	// pending for whoever runs next.
	if after, _ := catalog.metaValue(t.Context(), "labels_built_at"); after != built {
		t.Fatalf("a selected step completed the ledger: %q to %q", built, after)
	}
	for _, id := range dependents {
		if labelsDirtyToken(t, catalog, id) == "" {
			t.Fatalf("the source's step cleared %s", id)
		}
	}
	if pending, err := session.PendingFor(t.Context(), dependents); err != nil || len(pending) != len(dependents) {
		t.Fatalf("pending dependents: %v %v", pending, err)
	}
	// Interrupted after the source's output: the session is gone, and a new
	// process recovers the dependents from the ledger alone.
	session.Close()
	reopened, err := OpenCatalog(catalog.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recovered, err := reopened.RunLabels(t.Context(), settings, LabelsRunOptions{})
	if err != nil || !recovered.Complete || recovered.ConversationsBuilt < len(dependents) {
		t.Fatalf("classic recovery: %+v %v", recovered, err)
	}
	full, err := reopened.RunLabels(t.Context(), settings, LabelsRunOptions{Full: true})
	if err != nil || full.MessagesLabeled != 0 || full.MessagesRemoved != 0 {
		t.Fatalf("recovery differs from a full run: %+v %v", full, err)
	}
	// Labels lost behind the ledger's back: discovery wants the conversation
	// again but marks neither it nor its dependents, so a classic run
	// restores it alone, as it would without the session.
	execTx(t, reopened.DB, `DELETE FROM message_labels WHERE message_id='m-s1'`)
	lost, err := reopened.newLabelsSession(settings, workAllocation{CPUs: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer lost.Close()
	if err := lost.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, id := range append([]string{"c-root"}, dependents...) {
		if labelsDirtyToken(t, reopened, id) != "" {
			t.Fatalf("lost labels marked %s as changed", id)
		}
	}
	if pending, err := lost.PendingFor(t.Context(), []string{"c-root"}); err != nil || len(pending) != 1 {
		t.Fatalf("the conversation that lost labels: pending %v %v", pending, err)
	}
	healed, err := reopened.RunLabels(t.Context(), settings, LabelsRunOptions{})
	if err != nil || !healed.Complete || healed.ConversationsBuilt != 1 || healed.MessagesLabeled != 1 {
		t.Fatalf("restoring lost labels after a session's discovery: %+v %v", healed, err)
	}
}

// One session labels in steps with one context, one discovery and one warm
// worker. Changes after discovery are adopted only with their followers
// marked, a new conversation joins the context, and a revision arriving
// during a step survives it.
func TestLabelsSessionFollowsLaterChangesWithItsWarmContext(t *testing.T) {
	catalog := labeledCatalog(t)
	settings := labelsTestSettings(t, "ok")
	hellos := filepath.Join(t.TempDir(), "hellos.jsonl")
	t.Setenv(labelertest.FakeHelloEnv, hellos)
	session, err := catalog.newLabelsSession(settings, workAllocation{CPUs: 3})
	if err != nil {
		t.Fatal(err)
	}
	if step, err := session.Run(t.Context(), []string{"c-root"}, 0); err != nil || !step.Rediscover {
		t.Fatalf("a step before discovery: %+v %v", step, err)
	}
	if err := session.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err := queryMapsContext(t.Context(), catalog.DB, `SELECT id FROM conversations ORDER BY started_at DESC,id`)
	if err != nil {
		t.Fatal(err)
	}
	all := []string{}
	for _, row := range rows {
		all = append(all, firstString(row["id"]))
	}
	if pending, err := session.PendingFor(t.Context(), all); err != nil || len(pending) != len(all) {
		t.Fatalf("an unlabeled library: %v pending of %d (%v)", pending, len(all), err)
	}
	if step, err := session.Run(t.Context(), nil, 0); err != nil || len(step.Done)+len(step.Remaining) != 0 {
		t.Fatalf("an empty selection did work: %+v %v", step, err)
	}
	first, err := session.Run(t.Context(), all[:3], 0)
	if err != nil || len(first.Done) != 3 {
		t.Fatalf("the newest three: %+v %v", first, err)
	}
	pids := session.last.WorkerPIDs
	rest, err := session.Run(t.Context(), all[3:], 0)
	if err != nil || len(rest.Done) != len(all)-3 || !slices.Equal(session.last.WorkerPIDs, pids) {
		t.Fatalf("the rest: %+v %v (workers %v, then %v)", rest, err, pids, session.last.WorkerPIDs)
	}
	if ready, err := catalog.labelsReadyFor(t.Context(), all, session.Identity()); err != nil || len(ready) != len(all) {
		t.Fatalf("ready after both steps: %v %v", ready, err)
	}

	// A change after discovery: the followers are marked before the source's
	// new token is taken. When marking them fails, the session forgets what
	// it adopted and discovers again, so the change is never taken unmarked.
	execTx(t, catalog.DB, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('m-s2','c-root','m-s2','assistant','message','Merged it.','hash-m-s2','2026-09-01T15:00:00.000Z',1200)`,
		`CREATE TRIGGER fail_follower_mark BEFORE INSERT ON message_labels_dirty WHEN NEW.conversation_id='c-fork' BEGIN SELECT RAISE(ABORT,'test mark failure'); END`)
	if _, err := session.Run(t.Context(), []string{"c-root"}, 0); err == nil || session.Discovered() {
		t.Fatalf("a failed follower mark kept the session's adopted change: %v", err)
	}
	execTx(t, catalog.DB, `DROP TRIGGER fail_follower_mark`)
	if err := session.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if step, err := session.Run(t.Context(), []string{"c-root"}, 0); err != nil || !slices.Equal(step.Done, []string{"c-root"}) {
		t.Fatalf("a later change: %+v %v", step, err)
	}
	for _, id := range []string{"c-fork", "c-other", "c-late"} {
		if labelsDirtyToken(t, catalog, id) == "" {
			t.Fatalf("%s lost its reason to be relabeled", id)
		}
	}
	// A conversation created after discovery is built, not reported current.
	execTx(t, catalog.DB,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,agent_depth,started_at) VALUES('c-session-new','ws-main','codex','local','c-session-new',0,'2026-09-05T12:00:00.000Z')`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('m-sn1','c-session-new','m-sn1','assistant','message','Started a new session.','hash-m-sn1','2026-09-05T12:00:00.000Z',1300)`)
	if step, err := session.Run(t.Context(), []string{"c-session-new"}, 0); err != nil || !slices.Equal(step.Done, []string{"c-session-new"}) || labelCount(t, catalog, "WHERE message_id='m-sn1'") != 1 {
		t.Fatalf("a new conversation: %+v %v", step, err)
	}
	// A revision during the step: its labels are stored, its newer mark stays.
	execTx(t, catalog.DB, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('m-sn2','c-session-new','m-sn2','assistant','message','Added a test.','hash-m-sn2','2026-09-05T12:10:00.000Z',1301)`)
	session.beforeRun = func(context.Context) {
		execTx(t, catalog.DB, `UPDATE messages SET text='Added two tests.',content_hash='hash-m-sn2b' WHERE id='m-sn2'`)
	}
	if step, err := session.Run(t.Context(), []string{"c-session-new"}, 0); err != nil || !slices.Equal(step.Superseded, []string{"c-session-new"}) || labelsDirtyToken(t, catalog, "c-session-new") == "" {
		t.Fatalf("a superseded step: %+v %v", step, err)
	}
	session.beforeRun = nil
	if step, err := session.Run(t.Context(), []string{"c-session-new"}, 0); err != nil || !slices.Equal(step.Done, []string{"c-session-new"}) {
		t.Fatalf("after the newer revision: %+v %v", step, err)
	}
	if session.contexts != 2 || session.discoveries != 2 {
		t.Fatalf("small steps rebuilt the context %d times, discovered %d times", session.contexts, session.discoveries)
	}
	if threads := labelerHelloThreads(t, hellos); len(threads) != 1 {
		t.Fatalf("workers started %d times across steps: %v", len(threads), threads)
	}
	// The classic run finishes the followers; a full run then changes nothing.
	if stats, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{}); err != nil || !stats.Complete {
		t.Fatalf("classic run: %+v %v", stats, err)
	}
	if full, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{Full: true}); err != nil || full.MessagesLabeled != 0 || full.MessagesRemoved != 0 {
		t.Fatalf("session steps differ from a full run: %+v %v", full, err)
	}
}

func TestLabelsSessionAllocations(t *testing.T) {
	// A worker that may fall back to the CPU (auto) is charged its threads
	// like a CPU worker; only one that cannot (mlx) runs at one thread.
	for _, c := range []struct {
		cpus, threads   int
		gpuOnly         bool
		prep, inference int
		serial          bool
	}{
		{1, 4, false, 1, 1, true}, {2, 4, false, 1, 1, false}, {4, 4, false, 1, 3, false}, {8, 4, false, 4, 4, false},
		{3, 1, false, 2, 1, false}, {1, 4, true, 1, 1, true}, {2, 4, true, 1, 1, false}, {6, 4, true, 4, 1, false},
	} {
		prep, inference, serial := labelsSplit(c.cpus, c.threads, c.gpuOnly)
		if prep != c.prep || inference != c.inference || serial != c.serial {
			t.Fatalf("%+v: prep %d, inference %d, serial %v", c, prep, inference, serial)
		}
	}
	for cpus := 1; cpus <= 16; cpus++ {
		for threads := 1; threads <= 8; threads++ {
			for _, gpuOnly := range []bool{false, true} {
				prep, inference, serial := labelsSplit(cpus, threads, gpuOnly)
				if !serial && prep+inference > cpus || prep < 1 || inference < 1 || serial != (cpus == 1) {
					t.Fatalf("%d CPUs, %d threads (GPU only %v): prep %d + inference %d, serial %v", cpus, threads, gpuOnly, prep, inference, serial)
				}
			}
		}
	}
	// One CPU still labels everything, alternating preparation and inference
	// with a one-thread worker; a message limit cuts a step without losing work.
	catalog := labeledCatalog(t)
	settings := labelsTestSettings(t, "ok")
	settings.Threads = 4
	hellos := filepath.Join(t.TempDir(), "hellos.jsonl")
	t.Setenv(labelertest.FakeHelloEnv, hellos)
	session, err := catalog.newLabelsSession(settings, workAllocation{CPUs: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, err := queryMapsContext(t.Context(), catalog.DB, `SELECT id FROM conversations ORDER BY started_at DESC,id`)
	if err != nil {
		t.Fatal(err)
	}
	all := []string{}
	for _, row := range rows {
		all = append(all, firstString(row["id"]))
	}
	cut, err := session.Run(t.Context(), all, 2)
	if err != nil || session.last.MessagesLabeled != 2 || len(cut.Remaining) == 0 || len(cut.Failed) != 0 || !cut.Progress {
		t.Fatalf("a limited step: %+v %v (%+v)", cut, err, session.last)
	}
	done, err := session.Run(t.Context(), all, 0)
	if err != nil || len(done.Done) != len(all) {
		t.Fatalf("the rest at one CPU: %+v %v", done, err)
	}
	if pending, err := session.PendingFor(t.Context(), all); err != nil || len(pending) != 0 {
		t.Fatalf("still pending: %v %v", pending, err)
	}
	if !session.serial || session.settings.Threads != 1 || !slices.Equal(labelerHelloThreads(t, hellos), []int{1}) {
		t.Fatalf("one CPU used workers with threads %v (serial %v)", labelerHelloThreads(t, hellos), session.serial)
	}
	if generation, _ := catalog.metaValue(t.Context(), "labels_generation"); generation != "" {
		t.Fatalf("selected steps recorded a ledger generation: %q", generation)
	}
}

// labelsSessionConversations is every conversation, newest first.
func labelsSessionConversations(t *testing.T, catalog *Catalog) []string {
	t.Helper()
	rows, err := queryMapsContext(t.Context(), catalog.DB, `SELECT id FROM conversations ORDER BY started_at DESC,id`)
	if err != nil {
		t.Fatal(err)
	}
	all := []string{}
	for _, row := range rows {
		all = append(all, firstString(row["id"]))
	}
	return all
}

// labelsStepDone runs a session step and requires every selected
// conversation done, each current by the ledger alone.
func labelsStepDone(t *testing.T, catalog *Catalog, session *labelsSession, ids ...string) {
	t.Helper()
	step, err := session.Run(t.Context(), ids, 0)
	if err != nil || len(step.Done) != len(workDomainIDs(ids)) {
		t.Fatalf("step over %v: %+v %v", ids, step, err)
	}
	current, err := catalog.labelsCurrentFor(t.Context(), step.Done, session.Identity())
	if err != nil || len(current) != len(step.Done) {
		t.Fatalf("done but not current: %v of %v (%v)", current, step.Done, err)
	}
}

// A model switch wants every conversation (another model's labels remain),
// and only that view of the ledger wants a dependent the new model already
// labeled: the last old-model source's step turns it off. Discovery gives
// every wanted conversation its own mark first, so the dependents of the
// source's change survive each interruption and a new process with the
// classic run alone finishes what a full run would.
func TestLabelsSessionModelSwitchKeepsDependentsOfTheLastOldSource(t *testing.T) {
	catalog := labeledCatalog(t)
	plain := labelsTestSettings(t, "ok")
	pooled := labelsTestSettings(t, "ok", map[string]any{"name": "fake-pool", "input_contract": labelrt.InputContractIDsAttentionPool})
	if stats, err := catalog.RunLabels(t.Context(), plain, LabelsRunOptions{}); err != nil || !stats.Complete {
		t.Fatalf("first model: %+v %v", stats, err)
	}
	all := labelsSessionConversations(t, catalog)
	rest := slices.DeleteFunc(slices.Clone(all), func(id string) bool { return id == "c-root" })
	if _, err := catalog.RunLabels(t.Context(), pooled, LabelsRunOptions{Conversations: rest}); err != nil {
		t.Fatal(err)
	}
	model, _ := labelrt.LoadModel(pooled.ModelDir, "int8")
	identity := labelsModelIdentity(model)
	if old := labelCount(t, catalog, "WHERE model<>?", identity); old == 0 || old != labelCount(t, catalog, "WHERE conversation_id='c-root'") {
		t.Fatalf("the mixed ledger: %d old-model labels", old)
	}
	execTx(t, catalog.DB, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('m-s1','c-root','m-s1','assistant','message','Shipped the recent change.','hash-m-s1','2026-09-01T15:00:00.000Z',1200)`)
	dependents := []string{"c-other", "c-late"}
	rows, err := queryMapsContext(t.Context(), catalog.DB, `SELECT id FROM conversations WHERE parent_id='c-root'`)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		dependents = append(dependents, firstString(row["id"]))
	}
	session, err := catalog.newLabelsSession(pooled, workAllocation{CPUs: 2})
	if err != nil {
		t.Fatal(err)
	}
	stopped, stop := context.WithCancel(t.Context())
	stop()
	if err := session.Discover(stopped); err == nil {
		t.Fatal("a stopped discovery succeeded")
	}
	if err := session.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, id := range all {
		if session.builder.p.conversations[id] == nil {
			continue // not labeled at all
		}
		if labelsDirtyToken(t, catalog, id) == "" {
			t.Fatalf("discovery wanted %s for the model switch and left it no mark", id)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	session.beforeRun = func(context.Context) { cancel() }
	if step, err := session.Run(ctx, []string{"c-root"}, 0); !errors.Is(err, context.Canceled) || len(step.Done) != 0 {
		t.Fatalf("an interrupted step: %+v %v", step, err)
	}
	session.beforeRun = nil
	labelsStepDone(t, catalog, session, "c-root")
	if old := labelCount(t, catalog, "WHERE model<>?", identity); old != 0 {
		t.Fatalf("%d old-model labels after the last old source", old)
	}
	for _, id := range dependents {
		if labelsDirtyToken(t, catalog, id) == "" {
			t.Fatalf("the last old source's step cleared %s's only reason", id)
		}
	}
	if ready, err := catalog.labelsReadyFor(t.Context(), dependents, identity); err != nil || len(ready) != 0 {
		t.Fatalf("dependents reported ready: %v %v", ready, err)
	}
	session.Close()
	reopened, err := OpenCatalog(catalog.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recovered, err := reopened.RunLabels(t.Context(), pooled, LabelsRunOptions{})
	if err != nil || !recovered.Complete || recovered.ConversationsBuilt < len(dependents) {
		t.Fatalf("classic recovery: %+v %v", recovered, err)
	}
	if full, err := reopened.RunLabels(t.Context(), pooled, LabelsRunOptions{Full: true}); err != nil || full.MessagesLabeled != 0 || full.MessagesRemoved != 0 {
		t.Fatalf("recovery differs from a full run: %+v %v", full, err)
	}
}

// Links made after discovery cannot lose a historical consumer: a re-sender
// or fork a step built follows its source, a re-sender another run built
// sends the session back to discovery, and a deleted conversation is
// removed by discovery before it is reported done.
func TestLabelsSessionFollowsLinksMadeAfterDiscovery(t *testing.T) {
	catalog := labeledCatalog(t)
	settings := labelsTestSettings(t, "ok")
	reused := "Please add retry logic with exponential backoff to the upload client and cover the timeout path with a regression test"
	execTx(t, catalog.DB,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('ws-reuse','claude','local','ws-reuse','ws-reuse','2026-10-01')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,agent_depth,started_at) VALUES('c-src','ws-reuse','claude','local','c-src',0,'2026-09-10T12:00:00.000Z')`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('m-src','c-src','m-src','user','message','`+reused+`','hash-m-src','2026-09-10T12:00:00.000Z',1001)`)
	if err := catalog.RebuildAuthorship(t.Context(), "test-src"); err != nil {
		t.Fatal(err)
	}
	session, err := catalog.newLabelsSession(settings, workAllocation{CPUs: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	all := labelsSessionConversations(t, catalog)
	labelsStepDone(t, catalog, session, all...)
	resend := func(id string) {
		t.Helper()
		execTx(t, catalog.DB,
			`INSERT INTO conversations(id,workspace_id,provider,account,native_id,agent_depth,started_at) VALUES('`+id+`','ws-reuse','claude','local','`+id+`',0,'2026-10-12T12:00:00.000Z')`,
			`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('m-`+id+`','`+id+`','m-`+id+`','user','message','`+reused+`','hash-m-`+id+`','2026-10-12T12:00:00.000Z',1400)`)
		if err := catalog.RebuildAuthorship(t.Context(), "test-"+id); err != nil {
			t.Fatal(err)
		}
		reusers, err := catalog.labelsReusers(t.Context())
		source := "c-src"
		if id == "c-reuse2" {
			source = "c-reuse" // copied text follows its immediate source transitively
		}
		if err != nil || !slices.Contains(reusers[source], id) {
			t.Fatalf("the fixture's %s re-sends nothing of c-src: %v %v", id, reusers, err)
		}
	}
	change := func(id, message string) {
		t.Helper()
		if _, err := catalog.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES(?,?,?,'assistant','message','One more thing.',?,'2026-09-10T15:00:00.000Z',1500)`, message, id, message, "hash-"+message); err != nil {
			t.Fatal(err)
		}
	}
	clean := func() {
		t.Helper()
		rows, err := queryMapsContext(t.Context(), catalog.DB, `SELECT conversation_id FROM message_labels_dirty`)
		if err != nil {
			t.Fatal(err)
		}
		pending := []string{}
		for _, row := range rows {
			pending = append(pending, firstString(row["conversation_id"]))
		}
		if len(pending) > 0 {
			labelsStepDone(t, catalog, session, pending...)
		}
	}

	// A re-sender recorded after discovery and built by a step.
	resend("c-reuse")
	labelsStepDone(t, catalog, session, "c-reuse")
	clean()
	change("c-src", "m-src-more")
	labelsStepDone(t, catalog, session, "c-src")
	if labelsDirtyToken(t, catalog, "c-reuse") == "" {
		t.Fatal("a change of c-src left its late re-sender without a mark")
	}
	clean()
	// A fork recorded after discovery and built by a step.
	execTx(t, catalog.DB,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth,started_at) VALUES('c-fork-late','ws-main','codex','local','c-fork-late','c-root',0,'2026-09-02T13:00:00.000Z')`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('m-fork-late','c-fork-late','m-fork-late','user','message','continue later','hash-m-fork-late','2026-09-02T13:00:00.000Z',1600)`)
	labelsStepDone(t, catalog, session, "c-fork-late")
	clean()
	if _, err := catalog.DB.Exec(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('m-root-more','c-root','m-root-more','assistant','message','One more thing.','hash-m-root-more','2026-09-01T15:00:00.000Z',1501)`); err != nil {
		t.Fatal(err)
	}
	labelsStepDone(t, catalog, session, "c-root")
	if labelsDirtyToken(t, catalog, "c-fork-late") == "" {
		t.Fatal("a change of c-root left its late fork without a mark")
	}
	clean()
	if session.discoveries != 1 {
		t.Fatalf("%d discoveries before another run wrote", session.discoveries)
	}

	// A re-sender another run built: the session's graph lacks it.
	resend("c-reuse2")
	if stats, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{}); err != nil || !stats.Complete {
		t.Fatalf("a classic run between steps: %+v %v", stats, err)
	}
	change("c-src", "m-src-more2")
	if step, err := session.Run(t.Context(), []string{"c-src"}, 0); err != nil || !step.Rediscover || session.Discovered() || len(step.Done) != 0 {
		t.Fatalf("a step after another run: %+v %v", step, err)
	}
	if err := session.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	labelsStepDone(t, catalog, session, "c-src")
	if labelsDirtyToken(t, catalog, "c-reuse2") == "" {
		t.Fatal("another run's re-sender lost its reason to follow c-src")
	}
	clean()

	// A deleted conversation: discovery removes its labels first.
	execTx(t, catalog.DB, `DELETE FROM messages WHERE conversation_id='c-daily2'`, `DELETE FROM conversations WHERE id='c-daily2'`)
	if step, err := session.Run(t.Context(), []string{"c-daily2"}, 0); err != nil || !step.Rediscover || len(step.Done) != 0 {
		t.Fatalf("a deleted conversation: %+v %v", step, err)
	}
	if err := session.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	labelsStepDone(t, catalog, session, "c-daily2")
	if n := labelCount(t, catalog, "WHERE conversation_id='c-daily2'"); n != 0 {
		t.Fatalf("a deleted conversation kept %d labels", n)
	}
	if stats, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{}); err != nil || !stats.Complete {
		t.Fatalf("classic run: %+v %v", stats, err)
	}
	if full, err := catalog.RunLabels(t.Context(), settings, LabelsRunOptions{Full: true}); err != nil || full.MessagesLabeled != 0 || full.MessagesRemoved != 0 {
		t.Fatalf("session steps differ from a full run: %+v %v", full, err)
	}
}

// The library queue pages discovery's order without the session's locks:
// excluded and finished conversations drop out, a re-enrolled one returns to
// its place, and a change the queue does not hold asks for discovery.
func TestLabelsSessionPendingPage(t *testing.T) {
	catalog := labeledCatalog(t)
	settings := labelsTestSettings(t, "ok")
	session, err := catalog.newLabelsSession(settings, workAllocation{CPUs: 2})
	if err != nil {
		t.Fatal(err)
	}
	page := func(after string, limit int, excluded ...string) workDomainPage {
		t.Helper()
		got, err := session.PendingPage(t.Context(), after, limit, func(m workMember) bool { return slices.Contains(excluded, m.ID) })
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := page("", 3); !got.Rediscover || len(got.IDs()) != 0 {
		t.Fatalf("before discovery: %+v", got)
	}
	if err := session.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	all := slices.DeleteFunc(labelsSessionConversations(t, catalog), func(id string) bool { return session.builder.p.conversations[id] == nil })
	if got := page("", 3); !slices.Equal(got.IDs(), all[:3]) || got.Rediscover {
		t.Fatalf("the first page: %+v, want %v", got, all[:3])
	}
	if got := page(all[2], 3, all[3]); !slices.Equal(got.IDs(), all[4:7]) {
		t.Fatalf("the next page without an excluded one: %+v", got)
	}
	if got := page("", 3); !slices.Equal(got.IDs(), all[:3]) {
		t.Fatalf("paging claimed work: %+v", got)
	}
	labelsStepDone(t, catalog, session, all[:3]...)
	if got := page("", 3); !slices.Equal(got.IDs(), all[3:6]) {
		t.Fatalf("after a step: %+v", got)
	}
	labelsStepDone(t, catalog, session, all...)
	if got := page("", 3); len(got.IDs()) != 0 || got.Rediscover {
		t.Fatalf("a current library: %+v", got)
	}
	// A change after discovery: the queue does not hold it, so the page
	// offers its mark for a step's refresh to enroll, without rediscovery.
	execTx(t, catalog.DB, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('m-page','c-new','m-page','assistant','message','Done.','hash-m-page','2026-10-20T14:00:00.000Z',1700)`)
	if got := page("", 3); !slices.Equal(got.IDs(), []string{"c-new"}) || got.Units[0].Revision != labelsDirtyToken(t, catalog, "c-new") || got.Rediscover {
		t.Fatalf("a change the queue lacks: %+v", got)
	}
	if got := page("", 3, "c-new"); len(got.IDs()) != 0 || got.Rediscover {
		t.Fatalf("an excluded late change: %+v", got)
	}
	// A step's refresh enrolls it again, in its place, though interrupted.
	ctx, cancel := context.WithCancel(t.Context())
	session.beforeRun = func(context.Context) { cancel() }
	if _, err := session.Run(ctx, []string{"c-new"}, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("an interrupted step: %v", err)
	}
	session.beforeRun = nil
	if got := page("", 3); !slices.Equal(got.IDs(), []string{"c-new"}) {
		t.Fatalf("a re-enrolled conversation: %+v", got)
	}
	if got := page("", 3, "c-new"); len(got.IDs()) != 0 || got.Rediscover {
		t.Fatalf("only an excluded candidate left: %+v", got)
	}
	labelsStepDone(t, catalog, session, "c-new")
	if got := page("", 3); len(got.IDs()) != 0 || got.Rediscover {
		t.Fatalf("after the change: %+v", got)
	}
}
