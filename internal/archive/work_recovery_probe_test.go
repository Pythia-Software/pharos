//go:build probe

package archive

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
	if err != nil || stats.Complete {
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
	root := t.TempDir()
	config := filepath.Join(root, "archive.toml")
	text := fmt.Sprintf("catalog_path = %q\narchive_root = %q\ncapture_root = %q\nstaging_root = %q\nlabels_enabled = true\nlabels_model_dir = %q\nlabels_worker = %q\nlabels_variant = %q\nlabels_threads = 1\nlabels_workers = 1\n", path, filepath.Join(root, "preserved"), filepath.Join(root, "captures"), filepath.Join(root, "staging"), settings.ModelDir, settings.Worker, settings.Variant)
	if err := os.WriteFile(config, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	output, err := exec.CommandContext(ctx, baseline, "--config", config, "labels", "run").CombinedOutput()
	if err != nil {
		t.Fatalf("retained binary could not recover current catalog: %v\n%s", err, output)
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
	execTx(t, reopened.DB, `UPDATE messages SET text='An edit made after the old binary ran',content_hash='rollback-edited' WHERE id='rollback-new'`)
	final, err := reopened.RunLabels(t.Context(), settings, LabelsRunOptions{})
	if err != nil || !final.Complete {
		t.Fatalf("new process did not reconstruct old-writer work: %+v %v", final, err)
	}
	if len(output) == 0 {
		t.Fatal("retained binary produced no run report")
	}
}
