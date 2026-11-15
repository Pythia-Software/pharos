package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// pushBucket is a destination that accepts HTTPS PUTs. It fails the test if
// a key is ever written with different bytes, the hello apart.
type pushBucket struct {
	t       *testing.T
	mu      sync.Mutex
	objects map[string][]byte
	puts    int
	// before runs ahead of each write and may refuse it with a status.
	before func(key string) int
}

const pushBucketHost = "bucket.test"

func usePushBucket(t *testing.T) *pushBucket {
	t.Helper()
	bucket := &pushBucket{t: t, objects: map[string][]byte{}}
	override(t, &pushRetryDelay, 0)
	override(t, &pushHTTP, &http.Client{Transport: cloudRoundTripper(func(r *http.Request) (*http.Response, error) {
		answer := func(status int) (*http.Response, error) {
			return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}, Request: r}, nil
		}
		if r.URL.Host != pushBucketHost || r.Method != http.MethodPut {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			return answer(http.StatusBadRequest)
		}
		key := strings.TrimPrefix(r.URL.Path, "/")
		body, _ := io.ReadAll(r.Body)
		if bucket.before != nil {
			if status := bucket.before(key); status != 0 {
				return answer(status)
			}
		}
		bucket.mu.Lock()
		defer bucket.mu.Unlock()
		if existing, ok := bucket.objects[key]; ok && !bytes.Equal(existing, body) && !strings.Contains(key, "hello/") {
			t.Errorf("%s was overwritten with different bytes", key)
		}
		bucket.objects[key] = body
		bucket.puts++
		return answer(http.StatusCreated)
	})})
	return bucket
}

func (b *pushBucket) keys(part string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var keys []string
	for key := range b.objects {
		if strings.Contains(key, part) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func (b *pushBucket) get(_ context.Context, key string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.objects[key]
	if !ok {
		return nil, fmt.Errorf("%s is not in the bucket", key)
	}
	return data, nil
}

func (b *pushBucket) export(key string) pushExportDoc {
	b.t.Helper()
	data, err := b.get(context.Background(), key)
	if err != nil {
		b.t.Fatal(err)
	}
	var doc pushExportDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		b.t.Fatal(err)
	}
	return doc
}

// pushFixture is a machine with a home, one Claude transcript folder and a
// push configuration for the fake bucket.
type pushFixture struct {
	t          *testing.T
	home       string
	options    pushOptions
	bucket     *pushBucket
	transcript string
}

func newPushFixture(t *testing.T, config string) *pushFixture {
	t.Helper()
	if !captureExportSupported {
		t.Skip("capture export is not supported here")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME", "PHAROS_HOST_ID", "PHAROS_PUSH_OWNER", "PHAROS_PUSH_AUTOMATION", "PHAROS_PUSH_CONFIG", "PHAROS_PUSH_STATE",
		"NITESHIFT_TASK_ID", "NITESHIFT_SANDBOX_ID", "NITESHIFT_TASK_URL", "NITESHIFT_USER_ID"} {
		t.Setenv(name, "")
	}
	t.Setenv("PHAROS_HOST_LABEL", "sandbox")
	override(t, &pushMinInterval, 0)
	override(t, &currentHost, currentHost)
	machine := filepath.Join(home, "machine-id")
	if err := os.WriteFile(machine, []byte("machine-one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	override(t, &pushMachineIDFile, machine)
	f := &pushFixture{t: t, home: home, bucket: usePushBucket(t), transcript: filepath.Join(home, ".claude", "projects", "-work-api", "session.jsonl"),
		options: pushOptions{ConfigPath: filepath.Join(home, ".pharos", "pharos-push.json"), StateDir: filepath.Join(home, ".pharos", "push")}}
	if config == "" {
		config = `{"contract":"pharos-push-config/1","access":"write","prefix":"team","destination":{"type":"http","url_template":"https://bucket.test/{key}"}}`
	}
	writeSourceFile(t, f.options.ConfigPath, config)
	writeSourceFile(t, f.transcript, pushLines(0, 9000))
	return f
}

// pushLines is transcript text that does not repeat, about 70 bytes a line.
func pushLines(from, to int) string {
	var text strings.Builder
	for i := from; i < to; i++ {
		fmt.Fprintf(&text, `{"type":"assistant","n":%d,"text":"%s"}`+"\n", i, stableID("line", i, "pharos"))
	}
	return text.String()
}

func (f *pushFixture) appendTranscript(text string) {
	f.t.Helper()
	data, err := os.ReadFile(f.transcript)
	if err != nil {
		f.t.Fatal(err)
	}
	writeSourceFile(f.t, f.transcript, string(data)+text)
}

func (f *pushFixture) push() pushPassRecord {
	f.t.Helper()
	record, err := drainPush(context.Background(), f.options, true)
	if err != nil {
		f.t.Fatal(err)
	}
	return record
}

// rebuilt assembles the export at key from the bucket, verifies it and
// returns the transcript it holds.
func (f *pushFixture) rebuilt(key string) string {
	f.t.Helper()
	doc := f.bucket.export(key)
	dir := f.t.TempDir()
	prefix, _, _ := strings.Cut(key, "/exports/")
	envelope, err := assemblePushExport(context.Background(), f.bucket.get, prefix, doc, dir)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, file := range envelope.Files {
		if strings.HasSuffix(file.Path, "/session.jsonl") {
			data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file.Path)))
			if err != nil {
				f.t.Fatal(err)
			}
			return string(data)
		}
	}
	f.t.Fatalf("%s holds no transcript", key)
	return ""
}

func TestGearChunkerCutsWhereTheExportStoreDoes(t *testing.T) {
	data := []byte(pushLines(0, 20000))
	want := claudeSourceChunks(data)
	if len(want) < 4 {
		t.Fatalf("fixture makes only %d chunks", len(want))
	}
	for _, step := range []int{1 << 20, 4097, 333} {
		var got [][]byte
		chunker := newGearChunker(func(chunk pushChunk, piece []byte) error {
			if chunk.SHA256 != exportSum(piece) || chunk.Size != int64(len(piece)) {
				t.Fatalf("chunk named %s/%d holds other bytes", chunk.SHA256, chunk.Size)
			}
			got = append(got, bytes.Clone(piece))
			return nil
		})
		for rest := data; len(rest) > 0; {
			n := min(step, len(rest))
			if _, err := chunker.Write(rest[:n]); err != nil {
				t.Fatal(err)
			}
			rest = rest[n:]
		}
		if err := chunker.Close(); err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("writes of %d: %d chunks, want %d", step, len(got), len(want))
		}
		for i := range want {
			if !bytes.Equal(got[i], want[i]) {
				t.Fatalf("writes of %d: chunk %d differs", step, i)
			}
		}
		if chunker.sum != exportSum(data) || chunker.size != int64(len(data)) {
			t.Fatalf("writes of %d: whole digest or size is wrong", step)
		}
	}
}

func TestCapturePushUploadsWhatATurnAdded(t *testing.T) {
	f := newPushFixture(t, "")
	first := f.push()
	if !first.OK || len(first.Sources) != 1 || first.Sources[0].Export == "" {
		t.Fatalf("first pass: %+v", first)
	}
	source := first.Sources[0]
	if source.Kind != "claude" || source.Files != 1 || source.ChunksUploaded < 5 {
		t.Fatalf("first source: %+v", source)
	}
	if hello := f.bucket.keys("team/hello/"); len(hello) != 1 || hello[0] != "team/hello/"+first.Pusher+".json" {
		t.Fatalf("hello: %v", hello)
	}
	original, _ := os.ReadFile(f.transcript)
	if got := f.rebuilt(source.Export); got != string(original) {
		t.Fatal("the first export does not rebuild the transcript")
	}
	doc := f.bucket.export(source.Export)
	if doc.Previous != nil || doc.Pusher != first.Pusher || !strings.HasPrefix(source.Export, "team/exports/"+first.Pusher+"/claude/") {
		t.Fatalf("first export: previous %v, key %s", doc.Previous, source.Export)
	}

	// A turn appends: the pass uploads the end of the file, not the file.
	f.appendTranscript(pushLines(9000, 9050))
	second := f.push().Sources[0]
	if second.Export == "" || second.Export == source.Export {
		t.Fatalf("second pass made no new export: %+v", second)
	}
	if second.ChunksUploaded > 4 || second.BytesUploaded > source.BytesUploaded/2 {
		t.Fatalf("second pass uploaded %d chunks, %d bytes after %d", second.ChunksUploaded, second.BytesUploaded, source.BytesUploaded)
	}
	appended, _ := os.ReadFile(f.transcript)
	if got := f.rebuilt(second.Export); got != string(appended) {
		t.Fatal("the second export does not rebuild the transcript")
	}
	if previous := f.bucket.export(second.Export).Previous; previous == nil || !strings.HasSuffix(source.Export, "/"+*previous) {
		t.Fatalf("second export follows %v, not %s", previous, source.Export)
	}
	if got := f.rebuilt(source.Export); got != string(original) {
		t.Fatal("the first export no longer rebuilds what it held")
	}

	// Nothing new: nothing is written at all.
	puts := f.bucket.puts
	third := f.push().Sources[0]
	if !third.Unchanged || third.Export != "" || f.bucket.puts != puts {
		t.Fatalf("an unchanged pass wrote %d objects: %+v", f.bucket.puts-puts, third)
	}
	if exports := f.bucket.keys("/exports/"); len(exports) != 2 {
		t.Fatalf("exports: %v", exports)
	}
}

func TestCapturePushUploadsChunksAgainBeforeTheBucketDropsThem(t *testing.T) {
	f := newPushFixture(t, `{"contract":"pharos-push-config/1","access":"write","retention_days":90,"destination":{"type":"http","url_template":"https://bucket.test/{key}"}}`)
	first := f.push().Sources[0]
	if doc := f.bucket.export(first.Export); doc.RetentionDays != 90 {
		t.Fatalf("retention in the export: %d", doc.RetentionDays)
	}
	// A month on, an unchanged transcript costs nothing.
	override(t, &pushNow, func() time.Time { return time.Now().Add(30 * 24 * time.Hour) })
	f.appendTranscript(pushLines(9000, 9010))
	if early := f.push().Sources[0]; early.ChunksUploaded > 4 {
		t.Fatalf("a month on, %d chunks were uploaded", early.ChunksUploaded)
	}
	// Past half the lifecycle its chunks are sent again, so a conversation
	// that is still growing never loses its beginning.
	override(t, &pushNow, func() time.Time { return time.Now().Add(50 * 24 * time.Hour) })
	f.appendTranscript(pushLines(9010, 9020))
	if late := f.push().Sources[0]; late.ChunksUploaded < first.ChunksUploaded-2 {
		t.Fatalf("past half the lifecycle, %d of %d chunks were uploaded again", late.ChunksUploaded, first.ChunksUploaded)
	}
}

func TestCapturePushNeverPublishesAnExportBeforeItsChunks(t *testing.T) {
	f := newPushFixture(t, "")
	chunks := 0
	f.bucket.before = func(key string) int {
		if strings.Contains(key, "/chunks/") {
			if chunks++; chunks > 3 {
				return http.StatusServiceUnavailable
			}
		}
		return 0
	}
	if record, err := drainPush(context.Background(), f.options, true); err == nil || record.OK || record.Refused {
		t.Fatalf("a push whose uploads failed reported %+v, %v", record, err)
	}
	if exports := f.bucket.keys("/exports/"); len(exports) != 0 {
		t.Fatalf("an interrupted push published %v", exports)
	}
	if kept := f.bucket.keys("/chunks/"); len(kept) != 3 {
		t.Fatalf("chunks kept: %d", len(kept))
	}

	// The next pass sends only what the first did not.
	f.bucket.before = nil
	record := f.push()
	original, _ := os.ReadFile(f.transcript)
	if got := f.rebuilt(record.Sources[0].Export); got != string(original) {
		t.Fatal("the export after an interrupted push does not rebuild the transcript")
	}
	total := len(f.bucket.export(record.Sources[0].Export).Members[0].Chunks)
	if uploaded := record.Sources[0].ChunksUploaded; uploaded >= total+2 {
		t.Fatalf("the retry uploaded %d chunks of a %d-chunk transcript again", uploaded, total)
	}
}

func TestCapturePushServesARequestMadeDuringAPass(t *testing.T) {
	f := newPushFixture(t, "")
	asked := false
	f.bucket.before = func(key string) int {
		if strings.Contains(key, "/exports/") && !asked {
			// Another session ends its turn while this pass is finishing.
			asked = true
			f.appendTranscript(pushLines(9000, 9010))
			if err := requestPush(f.options); err != nil {
				t.Error(err)
			}
		}
		return 0
	}
	if _, err := drainPush(context.Background(), f.options, false); err != nil {
		t.Fatal(err)
	}
	exports := f.bucket.keys("/exports/")
	if len(exports) != 2 {
		t.Fatalf("exports: %v", exports)
	}
	final, _ := os.ReadFile(f.transcript)
	// Keys order a pusher's exports only to the second; the chain says which
	// came last.
	newest := exports[0]
	if f.bucket.export(newest).Previous == nil {
		newest = exports[1]
	}
	if got := f.rebuilt(newest); got != string(final) {
		t.Fatal("the last pass does not hold the last turn")
	}
	if pushPending(f.options) {
		t.Fatal("a request was left unserved")
	}

	// While a pass holds the lock a background push leaves, having asked.
	lock, err := lockPush(f.options, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := drainPush(context.Background(), f.options, false); !errors.Is(err, errPushRunning) {
		t.Fatalf("a second push while one runs: %v", err)
	}
}

func TestCapturePushFilesWorkByOwner(t *testing.T) {
	config := `{"contract":"pharos-push-config/1","access":"write","prefix":"team/{owner}","destination":{"type":"http","url_template":"https://bucket.test/{key}"}}`
	f := newPushFixture(t, config)

	// Nobody says whose work this is: refused, and nothing is written.
	_, err := drainPush(context.Background(), f.options, true)
	if err == nil || !strings.Contains(err.Error(), "PHAROS_PUSH_OWNER") || f.bucket.puts != 0 {
		t.Fatalf("an ownerless push to an owner prefix: %v, %d writes", err, f.bucket.puts)
	}

	// On Niteshift the task's user is the owner, and the export says where
	// it ran.
	t.Setenv("NITESHIFT_TASK_ID", "task_abc")
	t.Setenv("NITESHIFT_SANDBOX_ID", "sbx_1")
	t.Setenv("NITESHIFT_TASK_URL", "https://niteshift.dev/repo/acme/api/task_abc")
	if _, err := drainPush(context.Background(), f.options, true); err == nil || !strings.Contains(err.Error(), "NITESHIFT_USER_ID") {
		t.Fatalf("a Niteshift task without a user: %v", err)
	}
	t.Setenv("NITESHIFT_USER_ID", "user_42")
	record := f.push()
	if record.Prefix != "team/user_42" || record.Platform != "niteshift" || !strings.HasPrefix(record.Sources[0].Export, "team/user_42/exports/") {
		t.Fatalf("Niteshift pass: %+v", record)
	}
	doc := f.bucket.export(record.Sources[0].Export)
	if doc.Owner != "user_42" || doc.Platform == nil || doc.Platform.Environment != "api" || doc.Platform.Task != "task_abc" ||
		doc.Platform.TaskURL != "https://niteshift.dev/repo/acme/api/task_abc" || doc.Pool != "Niteshift · api" || doc.Initiator == nil || doc.Initiator.Kind != "person" {
		t.Fatalf("Niteshift export: %+v %+v %+v", doc.Platform, doc.Initiator, doc)
	}

	// A machine declared to run an automation files under shared, by name.
	t.Setenv("NITESHIFT_USER_ID", "")
	t.Setenv("PHAROS_PUSH_AUTOMATION", "Sentry autofix")
	record = f.push()
	doc = f.bucket.export(record.Sources[0].Export)
	if record.Prefix != "team/shared" || doc.Owner != "shared" || doc.Initiator == nil || doc.Initiator.Kind != "automation" || doc.Initiator.Name != "Sentry autofix" {
		t.Fatalf("automation pass: %+v %+v", record, doc.Initiator)
	}
	// Another prefix is another place: its chunks were uploaded there too.
	if got := f.rebuilt(record.Sources[0].Export); got == "" {
		t.Fatal("the shared export does not rebuild")
	}
}

func TestCapturePushTakesANewPusherOnAnotherMachine(t *testing.T) {
	f := newPushFixture(t, "")
	first := f.push()
	chunks := len(f.bucket.keys("/chunks/"))

	// The state folder is found on a machine it was not made on.
	if err := os.WriteFile(pushMachineIDFile, []byte("machine-two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := f.push()
	if second.Pusher == first.Pusher || second.Sources[0].Export == "" {
		t.Fatalf("a copied state kept pusher %s: %+v", first.Pusher, second)
	}
	doc := f.bucket.export(second.Sources[0].Export)
	if doc.Previous != nil || doc.Envelope.HostID == f.bucket.export(first.Sources[0].Export).Envelope.HostID {
		t.Fatalf("the new pusher continued the old chain or host: %+v", doc)
	}
	if hello := f.bucket.keys("/hello/"); len(hello) != 2 {
		t.Fatalf("hello: %v", hello)
	}
	// The transcript's chunks are already there; only the new host's records
	// are added.
	if added := len(f.bucket.keys("/chunks/")) - chunks; added > 2 {
		t.Fatalf("the new pusher uploaded %d chunks again", added)
	}
	if got, _ := os.ReadFile(f.transcript); f.rebuilt(second.Sources[0].Export) != string(got) {
		t.Fatal("the new pusher's export does not rebuild the transcript")
	}
}

func TestCapturePushBacksOffAfterRepeatedRefusals(t *testing.T) {
	f := newPushFixture(t, "")
	override(t, &pushRefusalLimit, 2)
	refuse := true
	f.bucket.before = func(string) int {
		if refuse {
			return http.StatusForbidden
		}
		return 0
	}
	attempts := func() int {
		data, _ := os.ReadFile(filepath.Join(f.options.StateDir, "push.log"))
		return strings.Count(string(data), "\n")
	}
	for i := 0; i < 2; i++ {
		if record, err := drainPush(context.Background(), f.options, false); err == nil || !record.Refused {
			t.Fatalf("refused pass %d: %+v, %v", i, record, err)
		}
	}
	// Background pushes now leave the destination alone, and say why.
	if _, err := drainPush(context.Background(), f.options, false); err != nil || attempts() != 2 {
		t.Fatalf("a push during the back-off: %v after %d attempts", err, attempts())
	}
	if status := readPushStatus(f.options); status.Paused == "" || status.Last == nil || !status.Last.Refused {
		t.Fatalf("status during the back-off: %+v", status)
	}
	// A day later one is tried; a person's own push and check always are.
	override(t, &pushNow, func() time.Time { return time.Now().Add(25 * time.Hour) })
	if _, err := drainPush(context.Background(), f.options, false); err == nil || attempts() != 3 {
		t.Fatalf("a push after the back-off: %v after %d attempts", err, attempts())
	}
	override(t, &pushNow, time.Now)
	if _, err := drainPush(context.Background(), f.options, true); err == nil || attempts() != 4 {
		t.Fatalf("a foreground push during the back-off: %v after %d attempts", err, attempts())
	}
	refuse = false
	if result, err := checkPush(context.Background(), f.options); err != nil || !result.OK {
		t.Fatalf("check: %+v, %v", result, err)
	}
	if record, err := drainPush(context.Background(), f.options, false); err != nil || !record.OK || readPushStatus(f.options).Paused != "" {
		t.Fatalf("a push after access was fixed: %+v, %v", record, err)
	}
}

func TestCapturePushWatchesTranscripts(t *testing.T) {
	f := newPushFixture(t, "")
	override(t, &pushWatchPoll, 5*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- watchPush(ctx, f.options) }()
	waitFor := func(exports int) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for len(f.bucket.keys("/exports/")) < exports {
			if time.Now().After(deadline) {
				cancel()
				t.Fatalf("the watcher made %d exports, want %d", len(f.bucket.keys("/exports/")), exports)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitFor(1)
	f.appendTranscript(pushLines(9000, 9010))
	waitFor(2)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCapturePushHookNeverFailsATurn(t *testing.T) {
	f := newPushFixture(t, "")
	var spawned []pushOptions
	override(t, &pushSpawn, func(options pushOptions) error { spawned = append(spawned, options); return nil })
	args := []string{"--config", f.options.ConfigPath, "--state", f.options.StateDir}
	if err := runCapturePushCLI(args); err != nil || len(spawned) != 1 || !pushPending(f.options) || f.bucket.puts != 0 {
		t.Fatalf("hook: %v, %d workers, %d writes", err, len(spawned), f.bucket.puts)
	}
	if spawned[0] != f.options {
		t.Fatalf("the worker was given %+v", spawned[0])
	}
	// The worker serves the request.
	if err := runCapturePushCLI(append([]string{"--background"}, args...)); err != nil || pushPending(f.options) || len(f.bucket.keys("/exports/")) != 1 {
		t.Fatalf("worker: %v, exports %v", err, f.bucket.keys("/exports/"))
	}
	// Where no worker can be started the hook pushes itself, and a broken
	// configuration still does not fail the turn.
	override(t, &pushSpawn, func(pushOptions) error { return errProcessIsolationUnsupported })
	f.appendTranscript(pushLines(9000, 9010))
	if err := runCapturePushCLI(args); err != nil || len(f.bucket.keys("/exports/")) != 2 {
		t.Fatalf("hook without a worker: %v, exports %v", err, f.bucket.keys("/exports/"))
	}
	writeSourceFile(t, f.options.ConfigPath, `{"contract":"other"}`)
	if err := runCapturePushCLI(args); err != nil {
		t.Fatalf("hook with a broken configuration: %v", err)
	}
	if status := readPushStatus(f.options); status.Problem == "" {
		t.Fatalf("status with a broken configuration: %+v", status)
	}
	if err := runCapturePushCLI(append([]string{"--wait", "--watch"}, args...)); err == nil {
		t.Fatal("two modes were accepted")
	}
}

// Each cloud is reached with plain HTTPS requests of the shape it documents.
func TestCapturePushDestinations(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("signed.identity.token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PUSH_TEST_SAS", "https://acct.blob.core.windows.net/box?sv=1&sig=s")
	type seen struct {
		method, url, body string
		header            http.Header
	}
	cases := []struct {
		name, destination string
		answers           map[string]string // host -> body
		check             func(t *testing.T, requests []seen)
	}{
		{"s3 keyless", `{"type":"s3","bucket":"box","region":"eu-west-1","role_arn":"arn:aws:iam::1:role/push","identity_token":{"file":"token"}}`,
			map[string]string{"sts.amazonaws.com": `<AssumeRoleWithWebIdentityResponse><AssumeRoleWithWebIdentityResult><Credentials><AccessKeyId>AKID</AccessKeyId><SecretAccessKey>secret</SecretAccessKey><SessionToken>session</SessionToken><Expiration>2099-01-01T00:00:00Z</Expiration></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`},
			func(t *testing.T, requests []seen) {
				if len(requests) != 2 || !strings.Contains(requests[0].body, "WebIdentityToken=signed.identity.token") || !strings.Contains(requests[0].body, "Action=AssumeRoleWithWebIdentity") {
					t.Fatalf("exchange: %+v", requests)
				}
				put := requests[1]
				if put.method != http.MethodPut || !strings.HasPrefix(put.url, "https://box.s3.eu-west-1.amazonaws.com/p/chunks/abc?") || put.header.Get("X-Amz-Security-Token") != "session" ||
					!strings.Contains(put.header.Get("Authorization"), "Credential=AKID/") || put.body != "bytes" {
					t.Fatalf("put: %+v", put)
				}
			}},
		{"s3 compatible", `{"type":"s3","bucket":"box","endpoint":"https://store.example","access_key_id":"AKID","secret_access_key":"secret"}`, nil,
			func(t *testing.T, requests []seen) {
				if len(requests) != 1 || !strings.HasPrefix(requests[0].url, "https://store.example/box/p/chunks/abc?") || requests[0].body != "bytes" {
					t.Fatalf("put: %+v", requests)
				}
			}},
		{"gcs keyless", `{"type":"gcs","bucket":"box","workload_identity_provider":"projects/1/locations/global/workloadIdentityPools/p/providers/n","service_account":"push@example.iam.gserviceaccount.com","identity_token":{"file":"token"}}`,
			map[string]string{"sts.googleapis.com": `{"access_token":"federated","expires_in":3600}`, "iamcredentials.googleapis.com": `{"accessToken":"acting","expireTime":"2099-01-01T00:00:00Z"}`},
			func(t *testing.T, requests []seen) {
				if len(requests) != 3 || !strings.Contains(requests[0].body, `"subjectToken":"signed.identity.token"`) ||
					!strings.Contains(requests[0].body, `"audience":"//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/p/providers/n"`) ||
					requests[1].header.Get("Authorization") != "Bearer federated" || !strings.Contains(requests[1].url, "push@example.iam.gserviceaccount.com:generateAccessToken") {
					t.Fatalf("exchange: %+v", requests)
				}
				put := requests[2]
				if put.method != http.MethodPost || put.url != "https://storage.googleapis.com/upload/storage/v1/b/box/o?uploadType=media&name=p%2Fchunks%2Fabc" || put.header.Get("Authorization") != "Bearer acting" || put.body != "bytes" {
					t.Fatalf("put: %+v", put)
				}
			}},
		{"azure keyless", `{"type":"azure","account":"acct","container":"box","tenant_id":"tenant","client_id":"client","identity_token":{"file":"token"}}`,
			map[string]string{"login.microsoftonline.com": `{"access_token":"azure","expires_in":3600}`},
			func(t *testing.T, requests []seen) {
				if len(requests) != 2 || requests[0].url != "https://login.microsoftonline.com/tenant/oauth2/v2.0/token" || !strings.Contains(requests[0].body, "client_assertion=signed.identity.token") {
					t.Fatalf("exchange: %+v", requests)
				}
				put := requests[1]
				if put.method != http.MethodPut || put.url != "https://acct.blob.core.windows.net/box/p/chunks/abc" || put.header.Get("Authorization") != "Bearer azure" || put.header.Get("x-ms-blob-type") != "BlockBlob" {
					t.Fatalf("put: %+v", put)
				}
			}},
		{"azure sas", `{"type":"azure","sas_url":{"env":"PUSH_TEST_SAS"}}`, nil,
			func(t *testing.T, requests []seen) {
				if len(requests) != 1 || requests[0].url != "https://acct.blob.core.windows.net/box/p/chunks/abc?sv=1&sig=s" || requests[0].header.Get("Authorization") != "" {
					t.Fatalf("put: %+v", requests)
				}
			}},
		{"http", `{"type":"http","url_template":"https://files.example/in/{key}?t=1","header":"Authorization: Bearer plain"}`, nil,
			func(t *testing.T, requests []seen) {
				if len(requests) != 1 || requests[0].url != "https://files.example/in/p/chunks/abc?t=1" || requests[0].header.Get("Authorization") != "Bearer plain" {
					t.Fatalf("put: %+v", requests)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var requests []seen
			override(t, &pushRetryDelay, 0)
			override(t, &pushHTTP, &http.Client{Transport: cloudRoundTripper(func(r *http.Request) (*http.Response, error) {
				body := []byte{}
				if r.Body != nil {
					body, _ = io.ReadAll(r.Body)
				}
				requests = append(requests, seen{r.Method, r.URL.String(), string(body), r.Header.Clone()})
				status, answer := http.StatusCreated, ""
				if text, ok := c.answers[r.URL.Host]; ok {
					status, answer = http.StatusOK, text
				} else if r.Method == http.MethodPost || strings.Contains(r.URL.Host, "amazonaws") || r.URL.Host == "store.example" {
					status = http.StatusOK
				}
				return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(answer)), Header: http.Header{}, Request: r}, nil
			})})
			path := filepath.Join(dir, strings.ReplaceAll(c.name, " ", "-")+".json")
			writeSourceFile(t, path, `{"contract":"pharos-push-config/1","access":"write","prefix":"p","destination":`+c.destination+`}`)
			config, err := readPushConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			store, err := newPushStore(config, dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.put(context.Background(), "p/chunks/abc", []byte("bytes")); err != nil {
				t.Fatal(err)
			}
			c.check(t, requests)
			if printed, _ := json.Marshal(config); strings.Contains(string(printed), `"secret"`) || strings.Contains(string(printed), "sig=s") {
				t.Fatalf("the configuration prints its secret: %s", printed)
			}
		})
	}
}

func TestPushConfigurationsAreChecked(t *testing.T) {
	examples, err := filepath.Glob(filepath.Join("..", "..", "contracts", "plugins", "examples", "push.config.*.json"))
	if err != nil || len(examples) < 3 {
		t.Fatalf("examples: %v, %v", examples, err)
	}
	for _, example := range examples {
		if _, err := readPushConfig(example); err != nil {
			t.Errorf("%s: %v", example, err)
		}
	}
	for name, config := range map[string]string{
		"unknown field":     `{"contract":"pharos-push-config/1","access":"write","surprise":1,"destination":{"type":"http","url_template":"https://x/{key}"}}`,
		"plain http":        `{"contract":"pharos-push-config/1","access":"write","destination":{"type":"http","url_template":"http://x/{key}"}}`,
		"two credentials":   `{"contract":"pharos-push-config/1","access":"write","destination":{"type":"s3","bucket":"b","access_key_id":"a","secret_access_key":"s","role_arn":"arn:aws:iam::1:role/r","identity_token":{"env":"T"}}}`,
		"no credential":     `{"contract":"pharos-push-config/1","access":"write","destination":{"type":"gcs","bucket":"b"}}`,
		"slash in prefix":   `{"contract":"pharos-push-config/1","access":"write","prefix":"a/","destination":{"type":"http","url_template":"https://x/{key}"}}`,
		"ambiguous secret":  `{"contract":"pharos-push-config/1","access":"write","destination":{"type":"azure","sas_url":{"env":"A","file":"b"}}}`,
		"another contract":  `{"contract":"pharos-push-config/2","access":"write","destination":{"type":"http","url_template":"https://x/{key}"}}`,
		"unpushable source": `{"contract":"pharos-push-config/1","access":"write","sources":[{"name":"x","kind":"chatgpt","path":"/x"}],"destination":{"type":"http","url_template":"https://x/{key}"}}`,
		"read through http": `{"contract":"pharos-push-config/1","access":"read","destination":{"type":"http","url_template":"https://x/{key}"}}`,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		writeSourceFile(t, path, config)
		if _, err := readPushConfig(path); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// The published platform recipes are the ones capture-push recognizes. Run
// with PHAROS_UPDATE_GOLDEN=1 after changing one.
func TestPushPlatformsContractMatchesTheRecipes(t *testing.T) {
	want, err := json.MarshalIndent(pushPlatformsContract(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	path := filepath.Join("..", "..", "contracts", "plugins", "platforms.json")
	if os.Getenv("PHAROS_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from the platform recipes; run with PHAROS_UPDATE_GOLDEN=1 and review the change", path)
	}
}

func TestCapturePushFindsTheAgentsFolders(t *testing.T) {
	f := newPushFixture(t, "")
	moved := filepath.Join(f.home, "elsewhere")
	writeSourceFile(t, filepath.Join(moved, "projects", "-w", "s.jsonl"), "{}\n")
	writeSourceFile(t, filepath.Join(moved, "codex", "sessions", "2026", "11", "15", "rollout-1.jsonl"), "{}\n")
	t.Setenv("CLAUDE_CONFIG_DIR", moved)
	t.Setenv("CODEX_HOME", filepath.Join(moved, "codex"))
	config, err := readPushConfig(f.options.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, source := range pushSources(config) {
		relative, _ := filepath.Rel(mustEvalSymlinks(t, f.home), source.Path)
		found = append(found, source.Name+"="+source.Kind+":"+filepath.ToSlash(relative))
	}
	// The app's own variable comes first; the usual folder is still taken.
	want := []string{"claude=claude:elsewhere/projects", "claude-2=claude:.claude/projects", "codex=codex:elsewhere/codex"}
	if strings.Join(found, " ") != strings.Join(want, " ") {
		t.Fatalf("sources: %v, want %v", found, want)
	}
	// Both agents' transcripts travel in one pass.
	record := f.push()
	if len(record.Sources) != 3 || record.Sources[2].Kind != "codex" || record.Sources[2].Export == "" || record.Sources[2].Files != 1 {
		t.Fatalf("pass: %+v", record.Sources)
	}
}

func mustEvalSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
