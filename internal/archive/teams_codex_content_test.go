package archive

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTeamsCodexContentBindsCompleteFileAndIndexedMessages(t *testing.T) {
	useHost(t, "synthetic-host")
	c, _ := testCatalog(t)
	root := filepath.Join(t.TempDir(), "codex")
	path := filepath.Join(root, "sessions", "rollout.jsonl")
	data := codexLines("synthetic-thread", 1)
	writeSourceFile(t, path, data)
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}
	ingestSource(t, c, source)
	messages, digest, err := teamsCodexContentFile(source, path, "synthetic-thread")
	if err != nil || digest == ([32]byte{}) {
		t.Fatalf("source: %v", err)
	}
	var id string
	if err = c.DB.QueryRow(`SELECT id FROM conversations WHERE native_id='synthetic-thread'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	match, err := teamsCodexIndexedContentMatches(context.Background(), c.reader(), id, messages)
	if err != nil || !match {
		t.Fatalf("indexed mismatch: %v", err)
	}
	if _, err = c.DB.Exec(`UPDATE messages SET text='modified after indexing' WHERE conversation_id=?`, id); err != nil {
		t.Fatal(err)
	}
	match, err = teamsCodexIndexedContentMatches(context.Background(), c.reader(), id, messages)
	if err != nil || match {
		t.Fatal("changed indexed text accepted", err)
	}
	writeSourceFile(t, path, data+`{"unfinished":`)
	if _, _, err = teamsCodexContentFile(source, path, "synthetic-thread"); err == nil {
		t.Fatal("torn input accepted")
	}
	writeSourceFile(t, path, strings.Replace(data, "synthetic-thread", "different-thread", 1))
	if _, _, err = teamsCodexContentFile(source, path, "synthetic-thread"); err == nil {
		t.Fatal("changed native identity accepted")
	}
	link := filepath.Join(root, "sessions", "link.jsonl")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err = teamsCodexContentFile(source, link, "synthetic-thread"); err == nil {
		t.Fatal("symlink accepted")
	}
}
