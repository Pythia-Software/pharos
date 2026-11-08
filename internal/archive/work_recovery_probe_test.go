//go:build probe

package archive

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gbdubs/pharos/internal/labeler/labelertest"
)

// Run against an actual retained binary, never an installed library:
//
//	PHAROS_WORK_BASELINE=/absolute/path/pharos go test -tags probe \
//	  -run '^TestWorkPreviousBinaryRecovery$' ./internal/archive
//
// The fake model is for a deterministic storage/recovery oracle. It makes no
// claim about throughput or production model accuracy.
func TestWorkPreviousBinaryRecovery(t *testing.T) {
	baseline := os.Getenv("PHAROS_WORK_BASELINE")
	if baseline == "" {
		t.Skip("set PHAROS_WORK_BASELINE to the retained pre-refactor binary")
	}
	baseline, err := filepath.Abs(baseline)
	if err != nil {
		t.Fatal(err)
	}
	c := labeledCatalog(t)
	settings := labelsTestSettings(t, "ok")
	if _, err := c.RunLabels(t.Context(), settings, LabelsRunOptions{}); err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,parent_id,agent_depth,started_at) VALUES('rollback-fork','ws-main','codex','local','rollback-fork','c-root',0,'2026-09-01T12:00:00.000Z')`,
		`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('rollback-message','rollback-fork','rollback-message','user','message','Make this work on tablets too','rollback-hash','2026-09-02T13:00:00.000Z',1100)`)
	if _, err := c.RunLabels(t.Context(), settings, LabelsRunOptions{}); err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at,source_order) VALUES('rollback-new','c-root','rollback-new','assistant','message','Updated after the fork was labeled.','rollback-new-hash','2026-09-01T14:00:00.000Z',1101)`)
	// Classic discovery durably enrolls dependent work before producer output.
	// The selected adapter must meet this same oracle in its own tests.
	stats, err := c.RunLabels(t.Context(), settings, LabelsRunOptions{Limit: 1})
	if err != nil || stats.Complete || stats.MessagesLabeled != 1 {
		t.Fatalf("interrupted producer: %+v %v", stats, err)
	}
	var dirty int
	if err := c.DB.QueryRow(`SELECT count(*) FROM message_labels_dirty WHERE conversation_id='rollback-fork'`).Scan(&dirty); err != nil || dirty != 1 {
		t.Fatalf("lost dependent before downgrade: %d %v", dirty, err)
	}
	path := c.Path
	var before int
	if err := c.DB.QueryRow(`SELECT count(*) FROM messages`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	old := runRetainedLabels(t, baseline, path, settings)
	// The old binary must itself rebuild the enrolled fork and finish.
	if !old.Complete || old.Cut || old.ConversationsBuilt < 1 || old.ConversationsFailed != 0 || old.MessagesFailed != 0 {
		t.Fatalf("retained binary did not recover the enrolled dependent: %+v", old)
	}
	reopened, err := OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.DB.QueryRow(`SELECT count(*) FROM message_labels_dirty`).Scan(&dirty); err != nil || dirty != 0 {
		t.Fatalf("old binary left pending dependent: %d %v", dirty, err)
	}
	var after int
	if err := reopened.DB.QueryRow(`SELECT count(*) FROM messages`).Scan(&after); err != nil || after != before {
		t.Fatalf("downgrade changed canonical count: before=%d after=%d err=%v", before, after, err)
	}
	recovered := storedLabels(t, reopened.DB)
	if recovered["rollback-message"] == "" || recovered["rollback-new"] == "" {
		t.Fatal("the old writer's state is missing a label for the fork or the parent's new message")
	}
	// A forced full classic rebuild relabels any message whose framed inputs
	// differ from what is stored. Nothing to relabel means the old binary's
	// output is the current classic result. (input_hash is the framing hash,
	// not the message's content_hash, so the two are never compared.)
	full, err := reopened.RunLabels(t.Context(), settings, LabelsRunOptions{Full: true})
	if err != nil || !full.Complete || full.MessagesLabeled != 0 || full.MessagesRemoved != 0 || full.MessagesFailed != 0 {
		t.Fatalf("old-writer labels differ from the forced full classic result: %+v %v", full, err)
	}
	if labels := storedLabels(t, reopened.DB); !maps.Equal(labels, recovered) {
		t.Fatal("a forced full classic rebuild changed the old writer's labels")
	}

	execTx(t, reopened.DB, `UPDATE messages SET text='An edit made after the old binary ran',content_hash='rollback-edited' WHERE id='rollback-new'`)
	final, err := reopened.RunLabels(t.Context(), settings, LabelsRunOptions{})
	if err != nil || !final.Complete || final.MessagesLabeled < 1 {
		t.Fatalf("new process did not process the subsequent edit: %+v %v", final, err)
	}
	if edited := storedLabels(t, reopened.DB)["rollback-new"]; edited == "" || edited == recovered["rollback-new"] {
		t.Fatal("the edited message was not relabeled from its new inputs")
	}
}

// runRetainedLabels runs the retained binary's `labels run --json` on the
// closed catalog with a private config, HOME and temporary directory. Only
// the fake worker's settings pass through from this test's environment.
// retainedLabelsReport is the part of the retained binary's `labels run
// --json` report the oracle reads. It is decoded by field name rather than
// into LabelsRunStats, so a later change to that struct cannot fail a proof
// about a binary that is fixed; a missing field still fails it.
type retainedLabelsReport struct {
	Complete, Cut                                           bool
	ConversationsBuilt, ConversationsFailed, MessagesFailed int
}

func runRetainedLabels(t *testing.T, binary, catalog string, settings LabelerSettings) retainedLabelsReport {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"home", "tmp", "preserved", "captures", "staging"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The fake worker loads no runtime; naming a stand-in keeps the old
	// binary from looking for a downloaded one under a real HOME.
	runtime := filepath.Join(root, "fake-runtime.dylib")
	if err := os.WriteFile(runtime, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "archive.toml")
	text := fmt.Sprintf("library = false\ncatalog_path = %q\narchive_root = %q\ncapture_root = %q\nstaging_root = %q\n"+
		"enable_reclamation = false\ngithub_token = \"\"\ntl1_token = \"\"\n"+
		"labels_enabled = true\nlabels_model_dir = %q\nlabels_worker = %q\nlabels_variant = %q\nlabels_runtime = %q\nlabels_threads = 1\nlabels_workers = 1\n",
		catalog, filepath.Join(root, "preserved"), filepath.Join(root, "captures"), filepath.Join(root, "staging"),
		settings.ModelDir, settings.Worker, settings.Variant, runtime)
	if err := os.WriteFile(config, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	// An allowlist, not the test's environment: no user HOME, tokens,
	// PHAROS_* settings or git configuration reach the old binary.
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + filepath.Join(root, "home"), "TMPDIR=" + filepath.Join(root, "tmp"),
		"TZ=UTC", "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "PHAROS_LABELER_FAKE_") {
			env = append(env, entry)
		}
	}
	if os.Getenv(labelertest.FakeWorkerEnv) == "" {
		t.Fatal("the fake worker's mode is not set")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	command := exec.CommandContext(ctx, binary, "--config", config, "labels", "run", "--json")
	command.Env, command.Dir = env, root
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("retained binary could not recover current catalog: %v\n%s", err, stderr.Bytes())
	}
	var fields map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	if err := decoder.Decode(&fields); err != nil || decoder.More() {
		t.Fatalf("retained binary's run report is not one JSON object: %v", err)
	}
	for _, name := range []string{"Complete", "Cut", "ConversationsBuilt", "ConversationsFailed", "MessagesFailed"} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("retained binary's run report has no %s", name)
		}
	}
	var report retainedLabelsReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("retained binary's run report: %v", err)
	}
	return report
}

// storedLabels is each stored label's content, without its timestamp.
func storedLabels(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT message_id, segment || '|' || labels || '|' || hex(probs) || '|' || input_hash || '|' || model || '|' || contract FROM message_labels`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := map[string]string{}
	for rows.Next() {
		var id, value string
		if err := rows.Scan(&id, &value); err != nil {
			t.Fatal(err)
		}
		result[id] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
