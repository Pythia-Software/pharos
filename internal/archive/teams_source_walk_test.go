package archive

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestTeamsSourceWalkMetadataOnlyAndRejectsLinks(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("version 1 supports local APFS on macOS")
	}
	root := t.TempDir()
	_, fs, err := volumeMount(root)
	if err != nil || fs != "apfs" {
		t.Skip("requires APFS")
	}
	file := filepath.Join(root, "sessions", "rollout.jsonl")
	writeSourceFile(t, file, codexLines("synthetic-thread", 1))
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}
	before, err := teamsSourceWalk(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("unexpected roster size: %d", len(before))
	}
	if err = os.Chmod(file, 0); err != nil {
		t.Fatal(err)
	}
	after, err := teamsSourceWalk(context.Background(), source)
	if err != nil || len(after) != 1 {
		t.Fatalf("metadata-only walk: %v", err)
	}
	if before[file] == after[file] {
		t.Fatal("metadata change was not reflected in acquisition stamp")
	}
	if err = os.Symlink(file, filepath.Join(root, "sessions", "alias.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err = teamsSourceWalk(context.Background(), source); err == nil {
		t.Fatal("symlink admitted")
	}
}
