package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// list reads the fake bucket as a reader's credential would.
func (b *pushBucket) list(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for _, key := range b.keys("") {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

// libraryFor gives the fixture's bucket a library that reads it.
func (f *pushFixture) library(prefix string) (*Catalog, Config, string) {
	f.t.Helper()
	catalog, config := testCatalog(f.t)
	config.CaptureRoot = filepath.Join(f.t.TempDir(), "captures")
	if err := os.MkdirAll(config.CaptureRoot, 0o700); err != nil {
		f.t.Fatal(err)
	}
	useHost(f.t, "library-mac")
	override(f.t, &openPushReadStore, func(pushConfig, string) (pushReadStore, error) { return f.bucket, nil })
	read := filepath.Join(f.home, "pharos-read.json")
	writeSourceFile(f.t, read, `{"contract":"pharos-push-config/1","access":"read","prefix":"`+prefix+`","destination":{"type":"s3","bucket":"box","access_key_id":"id","secret_access_key":"secret"}}`)
	return catalog, config, read
}

func TestBucketImportCollectsPushedCapturesAsTheirHost(t *testing.T) {
	f := newPushFixture(t, "")
	t.Setenv("NITESHIFT_TASK_ID", "task_abc")
	t.Setenv("NITESHIFT_SANDBOX_ID", "sbx_1")
	t.Setenv("NITESHIFT_TASK_URL", "https://niteshift.dev/repo/acme/api/task_abc")
	writeSourceFile(t, f.transcript, claudeLine("session", "u1", "hello from the sandbox", "/root/workspace/api", 1))
	pushed := f.push().Sources[0]
	sandbox := f.bucket.export(pushed.Export).Envelope.HostID
	catalog, config, read := f.library("team")

	result, err := importBucket(context.Background(), config.CaptureRoot, read)
	if err != nil || len(result.Exports) != 1 || result.Exports[0].Host != sandbox || result.Exports[0].Written == 0 {
		t.Fatalf("first import: %+v, %v", result, err)
	}
	collected := filepath.Join(config.CaptureRoot, sandbox, "claude", captureFilesDir, "-work-api", "session.jsonl")
	if readText(t, collected) != readText(t, f.transcript) {
		t.Fatal("the collected transcript differs from the sandbox's")
	}

	// It indexes as a conversation of the sandbox, not of this Mac.
	index := func() IngestResult {
		t.Helper()
		targets, err := captureTargets(config.CaptureRoot, []string{sandbox}, false, nil)
		if err != nil || len(targets) != 1 {
			t.Fatalf("targets: %+v, %v", targets, err)
		}
		result := catalog.IndexCapture(context.Background(), targets[0], nil)
		if result.Error != nil || result.Host != sandbox {
			t.Fatalf("index: %+v", result)
		}
		return result
	}
	if indexed := index(); indexed.Workspaces != 1 {
		t.Fatalf("indexed %d workspaces", indexed.Workspaces)
	}
	messages := func() int {
		t.Helper()
		var count int
		if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	before := messages()

	// The sandbox's pool is its machine and its platform its channel.
	var channel, machine, kind string
	if err := catalog.DB.QueryRow(`SELECT o.channel, m.label, m.kind FROM origin_observations o JOIN host_machines h ON h.host_id=? JOIN machines m ON m.id=h.machine_id AND m.id=o.execution_machine_id`, sandbox).
		Scan(&channel, &machine, &kind); err != nil || channel != "niteshift" || machine != "Niteshift · api" || kind != "cloud" {
		t.Fatalf("origin: channel %q, machine %q (%s), %v", channel, machine, kind, err)
	}

	// Collecting again changes nothing and fetches nothing.
	puts := len(f.bucket.keys(""))
	if again, err := importBucket(context.Background(), config.CaptureRoot, read); err != nil || len(again.Exports) != 0 || len(f.bucket.keys("")) != puts {
		t.Fatalf("second import: %+v, %v", again, err)
	}

	// The next turn extends the same conversation.
	f.appendTranscript(claudeLine("session", "u2", "and a second turn", "/root/workspace/api", 2))
	f.push()
	grown, err := importBucket(context.Background(), config.CaptureRoot, read)
	if err != nil || len(grown.Exports) != 1 || grown.Exports[0].Written == 0 {
		t.Fatalf("third import: %+v, %v", grown, err)
	}
	if readText(t, collected) != readText(t, f.transcript) {
		t.Fatal("the collected transcript did not grow")
	}
	index()
	if after := messages(); after <= before {
		t.Fatalf("messages went from %d to %d", before, after)
	}
	var conversations int
	if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM conversations`).Scan(&conversations); err != nil || conversations != 1 {
		t.Fatalf("conversations: %d, %v", conversations, err)
	}
}

func TestBucketImportOnlyAdds(t *testing.T) {
	f := newPushFixture(t, "")
	first := f.push().Sources[0]
	doc := f.bucket.export(first.Export)
	sandbox := doc.Envelope.HostID
	_, config, read := f.library("team")

	// A chunk whose bytes are not the ones it is named for: nothing of the
	// export reaches the capture root.
	chunk := "team/chunks/" + strings.TrimPrefix(doc.Members[0].Chunks[0].SHA256, "sha256:")
	genuine := f.bucket.objects[chunk]
	f.bucket.objects[chunk] = []byte("tampered")
	if result, err := importBucket(context.Background(), config.CaptureRoot, read); err == nil || len(result.Errors) != 1 {
		t.Fatalf("a tampered chunk: %+v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(config.CaptureRoot, sandbox)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused export left %v in the capture root", err)
	}
	f.bucket.objects[chunk] = genuine
	if _, err := importBucket(context.Background(), config.CaptureRoot, read); err != nil {
		t.Fatal(err)
	}

	// A later export that lacks a file the library holds is refused whole.
	extra := filepath.Join(config.CaptureRoot, sandbox, "claude", captureFilesDir, "-work-api", "earlier.jsonl")
	writeSourceFile(t, extra, "{}\n")
	f.appendTranscript(pushLines(9000, 9010))
	f.push()
	collected := filepath.Join(config.CaptureRoot, sandbox, "claude", captureFilesDir, "-work-api", "session.jsonl")
	before := readText(t, collected)
	if result, err := importBucket(context.Background(), config.CaptureRoot, read); err == nil || !strings.Contains(strings.Join(result.Errors, " "), "earlier.jsonl") {
		t.Fatalf("a regressing export: %+v, %v", result, err)
	}
	if readText(t, collected) != before || readText(t, extra) != "{}\n" {
		t.Fatal("a refused export changed the capture")
	}

	// A host a Mac captured is never merged into, nor is this Mac.
	if err := os.Remove(filepath.Join(config.CaptureRoot, sandbox, bucketImportMarker)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(extra); err != nil {
		t.Fatal(err)
	}
	if result, err := importBucket(context.Background(), config.CaptureRoot, read); err == nil || !strings.Contains(strings.Join(result.Errors, " "), "not collected from a bucket") {
		t.Fatalf("a host without the marker: %+v, %v", result, err)
	}
	useHost(t, sandbox)
	if result, err := importBucket(context.Background(), config.CaptureRoot, read); err == nil || !strings.Contains(strings.Join(result.Errors, " "), "this Mac") {
		t.Fatalf("an export claiming this Mac: %+v, %v", result, err)
	}
}

func TestBucketImportRunsAsABackgroundItem(t *testing.T) {
	f := newPushFixture(t, "")
	writeSourceFile(t, f.transcript, claudeLine("session", "u1", "hello from the sandbox", "/root/workspace/api", 1))
	sandbox := f.bucket.export(f.push().Sources[0].Export).Envelope.HostID
	catalog, config, read := f.library("team")
	t.Setenv("PHAROS_SUPPORT_DIR", t.TempDir())
	server := testServer(t, config, catalog)

	// Nothing is connected: the item has nothing to do.
	if in := server.backgroundInputsFor(context.Background(), nil); in.bucketImports != 0 {
		t.Fatalf("buckets before setup: %d", in.bucketImports)
	}
	raw, err := os.ReadFile(read)
	if err != nil {
		t.Fatal(err)
	}
	// A push configuration, or one whose files would move, is refused.
	if _, err := saveBucketImport(config, []byte(strings.Replace(string(raw), `"read"`, `"write"`, 1))); err == nil {
		t.Fatal("a write configuration was saved")
	}
	if _, err := saveBucketImport(config, []byte(strings.Replace(string(raw), `"secret"`, `{"file":"key.txt"}`, 1))); err == nil {
		t.Fatal("a relative secret file was saved")
	}
	saved, err := saveBucketImport(config, raw)
	if err != nil {
		t.Fatal(err)
	}
	if listed := savedBucketImports(config); len(listed) != 1 || listed[0].ID != saved.ID || listed[0].Prefix != "team" {
		t.Fatalf("saved: %+v", listed)
	}
	if in := server.backgroundInputsFor(context.Background(), nil); in.bucketImports != 1 {
		t.Fatalf("buckets after setup: %d", in.bucketImports)
	}

	// One run collects and indexes.
	ctx, op := server.ops.begin(context.Background(), OperationSpec{Kind: "capture.import", Variant: "bucket", Trigger: triggerSchedule})
	err = server.runBackgroundItem(ctx, op, "bucket-import")
	op.Finish(err)
	if err != nil {
		t.Fatal(err)
	}
	var conversations int
	if err := server.Catalog.DB.QueryRow(`SELECT COUNT(*) FROM conversations WHERE origin_host_id=?`, sandbox).Scan(&conversations); err != nil || conversations != 1 {
		t.Fatalf("conversations from the sandbox: %d, %v", conversations, err)
	}
	if err := removeBucketImport(config, saved.ID); err != nil || len(savedBucketImports(config)) != 0 {
		t.Fatalf("remove: %v", err)
	}
}
