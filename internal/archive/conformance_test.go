package archive

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// The conformance harness reads every provider the way production does: it
// captures a synthetic source, takes the source away, and reads the records
// back from the capture alone. Each provider's records are kept as a golden
// file, testdata/ingestion/<kind>.json, so a parser change shows as a diff
// beside the version that tells stored records to be read again. What the
// records hold is checked against what the provider declares it records.
//
// PHAROS_UPDATE_GOLDEN=1 rewrites the golden files. It refuses to change a
// provider's records without a new index version, since the libraries that
// already hold them would keep the old ones; PHAROS_UPDATE_GOLDEN=same-version
// writes them anyway, for a change stored records need not pick up.

// conformanceCase is one provider's synthetic source.
type conformanceCase struct {
	// build writes the source and returns its configuration.
	build func(t *testing.T) SourceConfig
	// secrets are the credentials the source holds, which no capture may.
	secrets []string
}

var conformanceCases = map[string]conformanceCase{
	"claude":         {build: conformanceClaude},
	"codex":          {build: conformanceCodex},
	"copilot":        {build: func(t *testing.T) SourceConfig { return conformanceSource("copilot", copilotFixture(t)) }, secrets: []string{copilotSecret}},
	"opencode":       {build: conformanceOpenCode, secrets: []string{opencodeSecret}},
	"cline":          {build: func(t *testing.T) SourceConfig { return conformanceSource("cline", clineFixture(t)) }, secrets: []string{clineSecret}},
	"vscode-copilot": {build: func(t *testing.T) SourceConfig { return conformanceSource("vscode-copilot", vscodeFixture(t)) }, secrets: []string{vscodeSecret}},
	"cursor":         {build: func(t *testing.T) SourceConfig { return conformanceSource("cursor", cursorFixture(t)) }, secrets: []string{cursorSecret}},
	"cursor-cli":     {build: conformanceCursorCLI, secrets: []string{cursorCLISecret}},
	"zed":            {build: conformanceZed, secrets: []string{zedSecret}},
	"antigravity":    {build: func(t *testing.T) SourceConfig { return conformanceSource("antigravity", antigravityFixture(t)) }},
	"conductor":      {build: conformanceConductor},
	"tl1":            {build: conformanceTL1},
	"canonical":      {build: func(t *testing.T) SourceConfig { return conformanceExport(t, "canonical") }},
	"tl1-export":     {build: func(t *testing.T) SourceConfig { return conformanceExport(t, "tl1-export") }},
	"chatgpt": {build: func(t *testing.T) SourceConfig {
		root := t.TempDir()
		conversations := chatGPTConversations()
		// A reply that records the effort it was asked for.
		answer := mapValue(mapValue(mapValue(conversations[0])["mapping"])["answer"])
		mapValue(mapValue(answer["message"])["metadata"])["reasoning_effort"] = "high"
		mapValue(mapValue(answer["message"])["metadata"])["service_tier"] = "priority"
		data, _ := json.Marshal(conversations)
		writeSourceFile(t, filepath.Join(root, "conversations.json"), string(data))
		return conformanceSource("chatgpt", root)
	}},
	"claude-ai": {build: func(t *testing.T) SourceConfig {
		root := t.TempDir()
		writeSourceFile(t, filepath.Join(root, "conversations.json"), claudeAIExport)
		return conformanceSource("claude-ai", root)
	}},
}

// conformanceGaps are capabilities a provider declares that its fixture does
// not show yet, each with the reason. An entry the fixture comes to show
// fails, so the list only shrinks.
var conformanceGaps = map[string]map[string]string{
	"antigravity": {"harness_version": "Read from the installed app, which tests do not install."},
	"conductor":   {"harness_version": "From the Claude Code or Codex session it ran, when that is a source of its own."},
}

func conformanceSource(kind, path string) SourceConfig {
	return SourceConfig{Name: kind, Kind: kind, Path: path, Account: "local", Enabled: true}
}

func TestEveryProviderHasAConformanceCase(t *testing.T) {
	for index := range providerRegistry {
		if _, ok := conformanceCases[providerRegistry[index].Kind]; !ok {
			t.Errorf("%s has no conformance case", providerRegistry[index].Kind)
		}
	}
	for kind := range conformanceCases {
		if _, ok := providerFor(kind); !ok {
			t.Errorf("conformance case %s names no provider", kind)
		}
	}
	for kind, gaps := range conformanceGaps {
		spec, ok := providerFor(kind)
		for key, reason := range gaps {
			if !ok || reason == "" || !slices.Contains([]contentSupport{supported, varies}, spec.Content.support(key)) {
				t.Errorf("conformanceGaps %s %s: a gap must be a capability the provider declares, with a reason", kind, key)
			}
		}
	}
}

func TestProviderConformance(t *testing.T) {
	kinds := []string{}
	for index := range providerRegistry {
		kinds = append(kinds, providerRegistry[index].Kind)
	}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) { conformance(t, kind) })
	}
}

// conformance reads kind's fixture twice, each time in a new folder. A hash
// that differs between the two reads is of the folder's path (a change set's
// ID hashes its evidence's file), so it is left out of the golden records;
// anything else that differs is a read that is not deterministic.
func conformance(t *testing.T, kind string) {
	reads, version := [2]string{}, ""
	for index, name := range []string{"read", "again"} {
		t.Run(name, func(t *testing.T) { version, reads[index] = conformanceRead(t, kind, index == 0) })
	}
	if t.Failed() {
		return
	}
	records, problem := conformanceStable(reads[0], reads[1], conformancePathHashes[kind])
	if problem != "" {
		t.Fatalf("two reads of the same fixture, in two folders, differ: %s", problem)
	}
	conformanceGolden(t, kind, version, records)
}

// conformancePathHashes are the fields, by the start of their golden line,
// whose hashes include a fixture's path, with the reason. Only these may
// differ between reads in two folders.
var conformancePathHashes = map[string]map[string]string{
	"conductor": {`"id": "changes_`: "A change Conductor's tools recorded is named by its files' evidence locators, which name the database's path."},
}

// conformanceRead captures kind's fixture, removes the source and reads the
// capture as production indexes it, returning its index version and its
// records, normalized. The first read also indexes them and checks what they
// hold.
func conformanceRead(t *testing.T, kind string, check bool) (string, string) {
	useHost(t, "conformance")
	override(t, &captureClock, time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	scratch := filepath.Dir(t.TempDir())
	started := time.Now().Add(-time.Second)
	source := conformanceCases[kind].build(t)
	fixtures, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatal(err)
	}
	// A file the fixture wrote without writeSourceFile has this Mac's time.
	settled := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	_ = filepath.WalkDir(scratch, func(path string, entry fs.DirEntry, err error) error {
		if info, statErr := os.Lstat(path); err == nil && statErr == nil && info.Mode().IsRegular() && info.ModTime().After(started) {
			_ = os.Chtimes(path, settled, settled)
		}
		return nil
	})
	for _, secret := range conformanceCases[kind].secrets {
		if len(conformanceHolding(t, scratch, secret)) == 0 {
			t.Fatalf("the fixture never writes the secret %q, so its capture is not checked for it", secret)
		}
	}
	live := conformanceRecords(t, discoverAll(t, source))

	config := captureTestConfig(t, source)
	if summary := runCapture(t, config); !summary.OK {
		t.Fatalf("capture: %+v", summary)
	}
	for _, secret := range conformanceCases[kind].secrets {
		for _, path := range conformanceHolding(t, config.CaptureRoot, secret) {
			t.Errorf("the capture holds a credential, in %s", path)
		}
	}
	// The capture alone must be enough: the source is gone, as on another
	// Mac reading this one's captures.
	for _, entry := range fixtures {
		if err := os.RemoveAll(filepath.Join(scratch, entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	target := captureTarget{Host: currentHost(), Source: source.Name, Dir: filepath.Join(config.CaptureRoot, currentHost().ID, source.Name)}
	input, err := leaseCaptureForIndex(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if input.Adapter == nil {
		t.Fatal("the capture leased no reader")
	}
	discovered := map[string]WorkspaceRecord{}
	if err := input.Adapter.Discover(func(record WorkspaceRecord) error { discovered[record.SourceID] = record; return nil }); err != nil {
		t.Fatal(err)
	}
	if len(discovered) == 0 {
		t.Fatal("the capture holds no records")
	}
	records := conformanceRecords(t, discovered)
	normalized := conformanceNormalize(records, scratch, captureViewOf(input.Adapter))
	// Another read of the same capture gives the same records, hashes and
	// all; only the folders differ between this read and the other one.
	again, err := leaseCaptureForIndex(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	rediscovered := map[string]WorkspaceRecord{}
	if err := again.Adapter.Discover(func(record WorkspaceRecord) error { rediscovered[record.SourceID] = record; return nil }); err != nil {
		t.Fatal(err)
	}
	if reread := conformanceNormalize(conformanceRecords(t, rediscovered), scratch, captureViewOf(again.Adapter)); reread != normalized {
		t.Errorf("two reads of one capture differ:\n%s", firstDifference(normalized, reread))
	}
	if want := conformanceNormalize(live, scratch, nil); normalized != want {
		t.Errorf("the capture reads differently from the source:\n%s", firstDifference(want, normalized))
	}
	version := sourceIndexVersion(input.Adapter)
	if !check {
		return version, normalized
	}
	catalog, _ := testCatalog(t)
	if result := catalog.IndexCapture(t.Context(), target, nil); result.Error != nil {
		t.Fatalf("index the capture: %v", result.Error)
	}
	for _, record := range records {
		if countRows(t, catalog, "SELECT COUNT(*) FROM workspaces WHERE source_id=?", record.SourceID) == 0 {
			t.Errorf("workspace %s was read but not indexed", record.SourceID)
		}
		for _, conversation := range record.Conversations {
			if countRows(t, catalog, "SELECT COUNT(*) FROM conversations c JOIN workspaces w ON w.id=c.workspace_id WHERE w.source_id=? AND c.native_id=?", record.SourceID, conversation.NativeID) != 1 {
				t.Errorf("conversation %s of %s was read but not indexed once", conversation.NativeID, record.SourceID)
			}
			ids := map[string]bool{}
			for _, message := range conversation.Messages {
				ids[message.NativeID] = true
			}
			indexed := countRows(t, catalog, `SELECT COUNT(*) FROM messages m JOIN conversations c ON c.id=m.conversation_id JOIN workspaces w ON w.id=c.workspace_id
				WHERE w.source_id=? AND c.native_id=?`, record.SourceID, conversation.NativeID)
			if indexed != len(ids) {
				t.Errorf("conversation %s of %s was read with %d messages but indexed with %d", conversation.NativeID, record.SourceID, len(ids), indexed)
			}
		}
	}
	conformanceCapabilities(t, kind, records, catalog)
	return version, normalized
}

// The credential scan finds a secret however a capture may hold it.
func TestConformanceFindsEncodedSecrets(t *testing.T) {
	const secret = "conformance-secret-do-not-capture"
	dir := t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wrapped := jsonText(map[string]any{"value": base64.StdEncoding.EncodeToString([]byte(jsonText(map[string]any{"meta": hex.EncodeToString([]byte(`{"key":"` + secret + `"}`))})))})
	var gzipped bytes.Buffer
	writer := gzip.NewWriter(&gzipped)
	_, _ = writer.Write([]byte(wrapped))
	_ = writer.Close()
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	// Bytes zstd cannot shrink, so the compressed cell overflows its page.
	noise := []byte{}
	for block := 0; len(noise) < 20000; block++ {
		noise = append(noise, hashBytes([]byte(strconv.Itoa(block)))...)
	}
	write("plain.txt", []byte("token="+secret))
	write("hex.json", []byte(`{"meta":"`+hex.EncodeToString([]byte(secret))+`"}`))
	write("wrapped.json", []byte(wrapped))
	write("record.gz", gzipped.Bytes())
	write("stream.bin", append([]byte("header"), encoder.EncodeAll([]byte(secret), nil)...))
	db := openWritableSQLite(t, filepath.Join(dir, "cells.db"))
	execSQL(t, db, "CREATE TABLE cells (value BLOB)")
	if _, err := db.Exec("INSERT INTO cells VALUES (?)", encoder.EncodeAll(append(noise, secret...), nil)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	write("clean.json", []byte(`{"meta":"`+hex.EncodeToString([]byte("nothing to see"))+`"}`))
	got := []string{}
	for _, path := range conformanceHolding(t, dir, secret) {
		got = append(got, filepath.Base(path))
	}
	slices.Sort(got)
	if want := []string{"cells.db", "hex.json", "plain.txt", "record.gz", "stream.bin", "wrapped.json"}; !slices.Equal(got, want) {
		t.Fatalf("files holding the secret = %v, want %v", got, want)
	}
	// Read cell by cell, a value is whole even where its pages split it.
	if found, err := conformanceDatabaseHolds(filepath.Join(dir, "cells.db"), []byte(secret)); err != nil || !found {
		t.Fatalf("the cell's secret was not found: %v", err)
	}
	if found, err := conformanceDatabaseHolds(filepath.Join(dir, "cells.db"), []byte("absent-secret-value")); err != nil || found {
		t.Fatalf("a secret the database lacks was found: %v", err)
	}
}

// conformanceHolding are the files below root holding secret, wherever it
// is (conformanceHolds).
func conformanceHolding(t *testing.T, root, secret string) []string {
	t.Helper()
	holding := []string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !entry.Type().IsRegular() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		found := conformanceHolds(data, []byte(secret), 3)
		if !found && bytes.HasPrefix(data, []byte("SQLite format 3\x00")) {
			if found, err = conformanceDatabaseHolds(path, []byte(secret)); err != nil {
				return err
			}
		}
		if found {
			holding = append(holding, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return holding
}

var (
	conformanceHex    = regexp.MustCompile(`[0-9a-fA-F]{16,}`)
	conformanceBase64 = regexp.MustCompile(`[A-Za-z0-9+/_-]{16,}={0,2}`)
	conformanceGzip   = []byte{0x1f, 0x8b, 0x08}
)

// conformanceHolds says whether data holds secret: as written, or within
// hex or base64 text, or a zstd or gzip stream starting anywhere in it,
// decoded up to depth times (base64 of a gzipped record of JSON with hex).
func conformanceHolds(data, secret []byte, depth int) bool {
	if bytes.Contains(data, secret) {
		return true
	}
	if depth == 0 {
		return false
	}
	decoded := [][]byte{}
	for _, run := range conformanceHex.FindAll(data, -1) {
		if value, err := hex.DecodeString(string(run[:len(run)/2*2])); err == nil {
			decoded = append(decoded, value)
		}
	}
	for _, run := range conformanceBase64.FindAll(data, -1) {
		text := strings.TrimRight(string(run), "=")
		for _, encoding := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
			if value, err := encoding.DecodeString(text); err == nil {
				decoded = append(decoded, value)
			}
		}
	}
	for _, magic := range [][]byte{zstdMagic, conformanceGzip} {
		for offset := bytes.Index(data, magic); offset >= 0; {
			var reader io.Reader
			if magic[0] == zstdMagic[0] {
				if decoder, err := zstd.NewReader(bytes.NewReader(data[offset:]), zstd.WithDecoderConcurrency(1)); err == nil {
					defer decoder.Close()
					reader = decoder
				}
			} else if decoder, err := gzip.NewReader(bytes.NewReader(data[offset:])); err == nil {
				reader = decoder
			}
			if reader != nil {
				// What decodes before a stream breaks off still counts.
				value, _ := io.ReadAll(io.LimitReader(reader, 64<<20))
				decoded = append(decoded, value)
			}
			next := bytes.Index(data[offset+1:], magic)
			if next < 0 {
				break
			}
			offset += 1 + next
		}
	}
	return slices.ContainsFunc(decoded, func(value []byte) bool { return len(value) > 0 && conformanceHolds(value, secret, depth-1) })
}

// conformanceDatabaseHolds looks in every value of every table of the SQLite
// database at path. A value longer than a page is split across overflow
// pages, which the scan of the file's bytes reads in pieces; read as a value,
// it is whole.
func conformanceDatabaseHolds(path string, secret []byte) (bool, error) {
	db, err := openReadOnlySQLite(path)
	if err != nil {
		return false, err
	}
	defer db.Close()
	tables, err := queryMaps(db, "SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		return false, err
	}
	for _, table := range tables {
		rows, err := db.Query(`SELECT * FROM "` + strings.ReplaceAll(firstString(table["name"]), `"`, `""`) + `"`)
		if err != nil {
			return false, err
		}
		columns, _ := rows.Columns()
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for index := range values {
				pointers[index] = &values[index]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				return false, err
			}
			for _, value := range values {
				var data []byte
				switch value := value.(type) {
				case []byte:
					data = value
				case string:
					data = []byte(value)
				}
				if conformanceHolds(data, secret, 3) {
					rows.Close()
					return true, nil
				}
			}
		}
		if err := rows.Close(); err != nil {
			return false, err
		}
	}
	return false, nil
}

var conformanceHash = regexp.MustCompile(`[0-9a-f]{16,}`)

// conformanceStable is first with each hash that differs in second as
// $PATH_HASH, on lines starting (after indentation) with one of fields. It
// returns a problem if anything else differs, or if a field never does.
func conformanceStable(first, second string, fields map[string]string) (string, string) {
	a, b := strings.Split(first, "\n"), strings.Split(second, "\n")
	if len(a) != len(b) {
		return first, firstDifference(first, second)
	}
	masked := map[string]bool{}
	for index := range a {
		if a[index] == b[index] {
			continue
		}
		line := strings.TrimSpace(a[index])
		field := ""
		for prefix := range fields {
			if strings.HasPrefix(line, prefix) {
				field = prefix
			}
		}
		hashed := conformanceHash.ReplaceAllString(a[index], "$$PATH_HASH")
		if field == "" || hashed != conformanceHash.ReplaceAllString(b[index], "$$PATH_HASH") {
			return first, "line " + strconv.Itoa(index+1) + ":\n  " + a[index] + "\n  " + b[index] + "\n(a hash of the fixture's path is declared in conformancePathHashes)"
		}
		masked[field] = true
		a[index] = hashed
	}
	for field := range fields {
		if !masked[field] {
			return first, "conformancePathHashes declares " + field + ", which no longer depends on the fixture's path"
		}
	}
	return strings.Join(a, "\n"), ""
}

// conformanceRecords orders records by source ID.
func conformanceRecords(t *testing.T, records map[string]WorkspaceRecord) []WorkspaceRecord {
	t.Helper()
	ids := slices.Sorted(func(yield func(string) bool) {
		for id := range records {
			if !yield(id) {
				return
			}
		}
	})
	result := []WorkspaceRecord{}
	for _, id := range ids {
		result = append(result, records[id])
	}
	return result
}

// conformanceNormalize is records as JSON, with the scratch folder, wherever
// it appears, as $ROOT, and a capture's folder as $CAPTURE.
func conformanceNormalize(records []WorkspaceRecord, scratch string, view *captureView) string {
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err.Error()
	}
	text := string(data)
	roots := []string{}
	if view != nil {
		roots = append(roots, view.dir)
	}
	roots = append(roots, scratch)
	for _, root := range roots {
		forms := []string{root}
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			forms = append(forms, resolved)
		}
		forms = append(forms, strings.TrimPrefix(root, "/private"))
		name := "$ROOT"
		if view != nil && root == view.dir {
			name = "$CAPTURE"
		}
		for _, form := range forms {
			if form != "" && form != "/" {
				text = strings.ReplaceAll(text, form, name)
			}
		}
	}
	return text + "\n"
}

func firstDifference(want, got string) string {
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")
	for index := range max(len(wantLines), len(gotLines)) {
		var a, b string
		if index < len(wantLines) {
			a = wantLines[index]
		}
		if index < len(gotLines) {
			b = gotLines[index]
		}
		if a != b {
			return "line " + strconv.Itoa(index+1) + ":\n  want " + a + "\n  got  " + b
		}
	}
	return ""
}

// conformanceGolden compares records with testdata/ingestion/<kind>.json.
func conformanceGolden(t *testing.T, kind, version, records string) {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "ingestion", kind+".json")
	type golden struct {
		IndexVersion string          `json:"index_version"`
		Records      json.RawMessage `json:"records"`
	}
	current, err := json.MarshalIndent(golden{IndexVersion: version, Records: json.RawMessage(records)}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	current = append(current, '\n')
	stored, readErr := os.ReadFile(path)
	if bytes.Equal(stored, current) {
		return
	}
	var previous golden
	_ = json.Unmarshal(stored, &previous)
	update := os.Getenv("PHAROS_UPDATE_GOLDEN")
	if update == "" {
		if readErr != nil {
			t.Fatalf("no golden records for %s; write them with PHAROS_UPDATE_GOLDEN=1", kind)
		}
		var stale bytes.Buffer
		_ = json.Indent(&stale, previous.Records, "", "  ")
		t.Fatalf("%s's records differ from %s (PHAROS_UPDATE_GOLDEN=1 rewrites it):\n%s", kind, path, firstDifference(stale.String()+"\n", records))
	}
	if readErr == nil && previous.IndexVersion == version && update != "same-version" {
		t.Fatalf("%s's records changed but its index version, %q, did not, so libraries would keep the records they hold. Change the version, or write with PHAROS_UPDATE_GOLDEN=same-version if stored records need not be read again.", kind, version)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, current, 0o644); err != nil {
		t.Fatal(err)
	}
}

// conformanceObserved are the capabilities records show, as people would see
// them: read from the records, or from the catalog indexed from them.
func conformanceObserved(records []WorkspaceRecord, catalog *Catalog) map[string]bool {
	seen := map[string]bool{}
	for _, record := range records {
		seen["cwd"] = seen["cwd"] || record.Location != ""
		seen["git_branch"] = seen["git_branch"] || firstString(record.Metadata["branch"]) != ""
		// A title the tool gave, not the fallback of the provider's name
		// and the session's short ID, or the first prompt.
		if title := record.Title; title != "" && title != record.SourceID && !strings.HasSuffix(title, " "+short(record.SourceID)) && title != record.Purpose {
			seen["title"] = true
		}
		if _, ok := record.Metadata["reported_cost_usd"]; ok {
			seen["cost"] = true
		}
		for _, metric := range record.Metrics {
			seen["cost"] = seen["cost"] || firstString(metric["name"]) == "reported_cost"
		}
		for _, conversation := range record.Conversations {
			times := map[string]bool{}
			seen["model"] = seen["model"] || conversation.Model != ""
			seen["effort"] = seen["effort"] || conversation.Effort != ""
			seen["subagents"] = seen["subagents"] || conversation.ParentNativeID != ""
			seen["harness_version"] = seen["harness_version"] || conversation.HarnessVersionFirst != ""
			for _, message := range conversation.Messages {
				seen["model"] = seen["model"] || message.Model != ""
				seen["effort"] = seen["effort"] || message.Effort != ""
				if message.CreatedAt != "" && message.Kind != "metadata" {
					times[message.CreatedAt] = true
				}
				switch message.Kind {
				case "tool_call", "delegation":
					seen["tool_calls"] = true
				case "tool_result", "delegation_result":
					seen["tool_results"] = true
				case "reasoning":
					seen["reasoning"] = true
				}
			}
			// Two messages sent at different times: a time copied to every
			// message, or a conversation of one, shows none.
			seen["timestamps"] = seen["timestamps"] || len(times) > 1
		}
	}
	// Each request's own usage, not a session's total.
	var tokens int
	_ = catalog.DB.QueryRow(`SELECT COUNT(*) FROM model_requests WHERE COALESCE(input_tokens,0)+COALESCE(output_tokens,0)>0`).Scan(&tokens)
	seen["message_tokens"] = tokens > 0
	// A speed the source recorded, not the standard it is taken to be.
	var speeds int
	_ = catalog.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE speed_source<>'assumed_standard'`).Scan(&speeds)
	seen["speed"] = speeds > 0
	return seen
}

// conformanceCapabilities holds what the records show to what the provider
// declares: it records nothing it declares it never records, and its fixture
// shows everything it declares, or the gap is listed.
func conformanceCapabilities(t *testing.T, kind string, records []WorkspaceRecord, catalog *Catalog) {
	t.Helper()
	spec, _ := providerFor(kind)
	seen := conformanceObserved(records, catalog)
	for _, capability := range contentCapabilityList {
		key, support := capability.Key, spec.Content.support(capability.Key)
		gap, listed := conformanceGaps[kind][key]
		switch {
		case seen[key] && (support == unsupported || support == notApplicable):
			t.Errorf("%s records %s, which it declares %s", kind, key, support)
		case seen[key] && listed:
			t.Errorf("%s's fixture now shows %s: remove it from conformanceGaps (%q)", kind, key, gap)
		case !seen[key] && !listed && (support == supported || support == varies):
			t.Errorf("%s declares %s %s, but its fixture shows none: extend the fixture, or list the gap in conformanceGaps", kind, key, support)
		}
	}
}

// conformanceClaude is a Claude Code session on a branch that thinks, runs a
// command, hands work to a subagent, reports its usage and effort, and is
// named.
func conformanceClaude(t *testing.T) SourceConfig {
	t.Helper()
	main, agent := conformanceClaudeLines()
	return claudeSessionFiles(t, map[string][]string{"claude-session.jsonl": main, "claude-session/subagents/agent-a7.jsonl": agent})
}

// conformanceClaudeLines are a Claude Code session's lines and its subagent's.
func conformanceClaudeLines() (main, agent []string) {
	line := func(fields map[string]any) string {
		event := map[string]any{"sessionId": "claude-session", "cwd": "/work/app", "gitBranch": "main", "version": "2.1.3", "entrypoint": "cli"}
		for key, value := range fields {
			event[key] = value
		}
		return jsonText(event)
	}
	usage := map[string]any{"input_tokens": 120, "output_tokens": 30, "cache_read_input_tokens": 2000, "service_tier": "standard"}
	assistant := func(uuid, parent, at, id string, content ...any) string {
		return line(map[string]any{"type": "assistant", "uuid": uuid, "parentUuid": parent, "timestamp": at,
			"message": map[string]any{"id": id, "role": "assistant", "model": "claude-opus-4-5", "output_config": map[string]any{"effort": "high"}, "content": content, "usage": usage}})
	}
	user := func(uuid, parent, at string, content any) string {
		return line(map[string]any{"type": "user", "uuid": uuid, "parentUuid": parent, "timestamp": at, "message": map[string]any{"role": "user", "content": content}})
	}
	main = []string{
		user("u1", "", "2026-09-01T10:00:00.000Z", "Count the words in README.md"),
		assistant("a1", "u1", "2026-09-01T10:00:05.000Z", "msg_1",
			map[string]any{"type": "thinking", "thinking": "Read the file first.", "signature": "c2ln"},
			map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Bash", "input": map[string]any{"command": "wc -w README.md"}}),
		user("u2", "a1", "2026-09-01T10:00:06.000Z", []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "74 README.md"}}),
		assistant("a2", "u2", "2026-09-01T10:00:08.000Z", "msg_2",
			map[string]any{"type": "tool_use", "id": "toolu_2", "name": "Task", "input": map[string]any{"description": "Check the count", "prompt": "Count the words again", "subagent_type": "general-purpose"}}),
		user("u3", "a2", "2026-09-01T10:00:20.000Z", []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_2", "content": []any{map[string]any{"type": "text", "text": "74 words."}}}}),
		assistant("a3", "u3", "2026-09-01T10:00:22.000Z", "msg_3", map[string]any{"type": "text", "text": "README.md has 74 words."}),
		jsonText(map[string]any{"type": "ai-title", "sessionId": "claude-session", "aiTitle": "Count README words"}),
	}
	agent = []string{
		line(map[string]any{"type": "user", "uuid": "s1", "isSidechain": true, "agentId": "a7", "timestamp": "2026-09-01T10:00:09.000Z", "message": map[string]any{"role": "user", "content": "Count the words again"}}),
		line(map[string]any{"type": "assistant", "uuid": "s2", "parentUuid": "s1", "isSidechain": true, "agentId": "a7", "timestamp": "2026-09-01T10:00:19.000Z",
			"message": map[string]any{"id": "msg_s", "role": "assistant", "model": "claude-haiku-4-5", "content": []any{map[string]any{"type": "text", "text": "74 words."}}, "usage": usage}}),
	}
	return main, agent
}

// conformanceCodex is a named Codex session on a branch that reasons, runs a
// command and reports its usage, and a subagent it started.
func conformanceCodex(t *testing.T) SourceConfig {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".codex")
	main, agent := conformanceCodexLines()
	day := filepath.Join(root, "sessions", "2026", "09", "01")
	writeSourceFile(t, filepath.Join(day, "rollout-2026-09-01T10-00-00-"+conformanceCodexParent+".jsonl"), strings.Join(main, "\n")+"\n")
	writeSourceFile(t, filepath.Join(day, "rollout-2026-09-01T10-00-08-"+conformanceCodexChild+".jsonl"), strings.Join(agent, "\n")+"\n")
	writeSourceFile(t, filepath.Join(root, "session_index.jsonl"), jsonText(map[string]any{"id": conformanceCodexParent, "thread_name": "Count README words", "updated_at": "2026-09-01T10:10:00Z"})+"\n")
	return conformanceSource("codex", root)
}

const conformanceCodexParent, conformanceCodexChild = "0199a000-0000-7000-8000-000000000001", "0199a000-0000-7000-8000-000000000002"

// conformanceCodexLines are a Codex rollout's lines and its subagent's.
func conformanceCodexLines() (main, agent []string) {
	const parent, child = conformanceCodexParent, conformanceCodexChild
	event := func(at, kind string, payload map[string]any) string {
		return jsonText(map[string]any{"timestamp": at, "type": kind, "payload": payload})
	}
	meta := func(id string, extra map[string]any) map[string]any {
		payload := map[string]any{"id": id, "timestamp": "2026-09-01T10:00:00.000Z", "cwd": "/work/app", "originator": "codex_cli_rs", "cli_version": "0.50.0",
			"git": map[string]any{"branch": "main", "commit_hash": "abc123", "repository_url": "https://github.com/acme/app.git"}}
		for key, value := range extra {
			payload[key] = value
		}
		return payload
	}
	tokens := map[string]any{"input_tokens": 2000, "cached_input_tokens": 1500, "output_tokens": 40, "reasoning_output_tokens": 10, "total_tokens": 2040}
	main = []string{
		event("2026-09-01T10:00:00.000Z", "session_meta", meta(parent, nil)),
		event("2026-09-01T10:00:01.000Z", "turn_context", map[string]any{"cwd": "/work/app", "model": "gpt-5-codex", "effort": "high", "service_tier": "priority"}),
		event("2026-09-01T10:00:02.000Z", "response_item", map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Count the words in README.md"}}}),
		event("2026-09-01T10:00:03.000Z", "response_item", map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{map[string]any{"type": "summary_text", "text": "Read the file first."}}, "encrypted_content": "gAAAA"}),
		event("2026-09-01T10:00:04.000Z", "response_item", map[string]any{"type": "function_call", "name": "shell", "arguments": `{"command":["wc","-w","README.md"]}`, "call_id": "call_1"}),
		event("2026-09-01T10:00:05.000Z", "response_item", map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "74 README.md"}),
		event("2026-09-01T10:00:06.000Z", "event_msg", map[string]any{"type": "token_count", "info": map[string]any{"total_token_usage": tokens, "last_token_usage": tokens}}),
		event("2026-09-01T10:00:07.000Z", "response_item", map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "README.md has 74 words."}}}),
	}
	agent = []string{
		event("2026-09-01T10:00:08.000Z", "session_meta", meta(child, map[string]any{"source": map[string]any{"subagent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": parent, "depth": 1}}}})),
		event("2026-09-01T10:00:09.000Z", "turn_context", map[string]any{"cwd": "/work/app", "model": "gpt-5-codex", "effort": "low"}),
		event("2026-09-01T10:00:10.000Z", "response_item", map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "74 words."}}}),
	}
	return main, agent
}

// conformanceExport is a canonical export holding everything the format can:
// a session in a folder on a branch, with a model, effort, thinking, a tool
// call and its result, each request's usage, a subagent, a tool version and
// a cost.
func conformanceExport(t *testing.T, kind string) SourceConfig {
	t.Helper()
	message := func(id, role, kind, text, at string, extra map[string]any) map[string]any {
		row := map[string]any{"id": id, "role": role, "kind": kind, "text": text, "created_at": at}
		for key, value := range extra {
			row[key] = value
		}
		return row
	}
	conversation := func(id, parent string, messages ...any) map[string]any {
		row := map[string]any{"id": id, "provider": "claude", "model": "claude-opus-4-5", "effort": "high", "speed": "fast", "started_at": "2026-09-01T10:00:00Z", "ended_at": "2026-09-01T10:01:00Z",
			"harness": "claude-code/cli", "harness_version_first": "2.1.3", "harness_version_last": "2.1.3", "messages": messages}
		if parent != "" {
			row["parent_id"] = parent
		}
		return row
	}
	// Usage counts as a request's when it names the request.
	usage := func(id string) map[string]any {
		return map[string]any{"message": map[string]any{"id": id, "usage": map[string]any{"input_tokens": 100, "output_tokens": 20}}}
	}
	document := map[string]any{"workspaces": []any{map[string]any{
		"id": "export-work", "title": "Count README words", "purpose": "Count the words", "outcome": "74 words", "activity_at": "2026-09-01T10:01:00Z",
		"location": "/work/app", "metadata": map[string]any{"branch": "main"}, "metrics": []any{map[string]any{"name": "reported_cost", "value": 0.12, "unit": "USD"}},
		"conversations": []any{
			conversation("export-main", "",
				message("m1", "user", "message", "Count the words", "2026-09-01T10:00:00Z", nil),
				message("m2", "assistant", "reasoning", "Read the file first.", "2026-09-01T10:00:01Z", nil),
				message("m3", "assistant", "tool_call", `{"tool":"Bash","input":{"command":"wc -w README.md"}}`, "2026-09-01T10:00:02Z", map[string]any{"call_id": "c1"}),
				message("m4", "tool", "tool_result", "74 README.md", "2026-09-01T10:00:03Z", map[string]any{"call_id": "c1"}),
				message("m5", "assistant", "message", "README.md has 74 words.", "2026-09-01T10:00:04Z", usage("req-1"))),
			conversation("export-agent", "export-main", message("a1", "assistant", "message", "74 words.", "2026-09-01T10:00:05Z", usage("req-2"))),
		},
	}}}
	data, _ := json.Marshal(document)
	file := filepath.Join(t.TempDir(), "export", "export.json")
	writeSourceFile(t, file, string(data))
	return conformanceSource(kind, filepath.Dir(file))
}

// conformanceOpenCode is OpenCode's fixture with a session that reports its
// cost.
func conformanceOpenCode(t *testing.T) SourceConfig {
	t.Helper()
	root := opencodeFixture(t)
	db := openWritableSQLite(t, filepath.Join(root, "opencode.db"))
	execSQL(t, db, "UPDATE session_v2 SET cost=0.05 WHERE id='ses_parts'")
	db.Close()
	return conformanceSource("opencode", root)
}

// conformanceCursorCLI is the Cursor CLI's fixture with a chat it named,
// reasoning it kept and a second prompt.
func conformanceCursorCLI(t *testing.T) SourceConfig {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".cursor")
	chat := cursorCLIConversation()
	chat.meta["name"], chat.metaJSON["title"] = "Repair the build", "Repair the build"
	thought := mapValue(mapValue(chat.messages[3])["content"].([]any)[0])
	thought["text"] = "Check the makefile."
	// A second prompt, so its messages have a time of their own.
	chat.messages = append(chat.messages,
		map[string]any{"role": "user", "id": "u2", "content": []any{map[string]any{"type": "text", "text": "<timestamp>Wednesday, Nov 4, 2026, 11:25 AM (UTC-5)</timestamp>\n<user_query>\nRun the tests\n</user_query>"}}},
		map[string]any{"role": "assistant", "id": "a4", "content": []any{map[string]any{"type": "text", "text": "They pass."}}})
	last := len(chat.roots) - 1
	chat.roots[last] = append(chat.roots[last], len(chat.messages)-2, len(chat.messages)-1)
	cursorCLIWriteChat(t, root, chat)
	writeSourceFile(t, filepath.Join(root, "cli-config.json"), `{"authInfo":{"token":"`+cursorCLISecret+`"}}`)
	return conformanceSource("cursor-cli", root)
}

// conformanceZed is Zed's fixture with a thread that records its effort.
func conformanceZed(t *testing.T) SourceConfig {
	t.Helper()
	root := zedFixture(t)
	rows := zedRows()
	rows[1].data["thinking_effort"] = "high"
	rows[1].data["speed"] = "fast"
	zedWrite(t, root, func(db *sql.DB) error { zedPut(t, db, rows[1]); return nil })
	return conformanceSource("zed", root)
}

// conformanceConductor is Conductor's fixture with the session's effort and
// a reply that thought and reports its request's usage.
func conformanceConductor(t *testing.T) SourceConfig {
	t.Helper()
	root := t.TempDir()
	conductorSemanticFixture(t, root)
	db := openWritableSQLite(t, filepath.Join(root, "conductor.db"))
	thought := jsonText(map[string]any{"type": "assistant", "message": map[string]any{"id": "msg-think", "role": "assistant", "content": []any{
		map[string]any{"type": "thinking", "thinking": "Check the formatter first.", "signature": "c2ln"}, map[string]any{"type": "text", "text": "Checking."}},
		"usage": map[string]any{"input_tokens": 300, "output_tokens": 40, "service_tier": "standard"}}})
	execSQL(t, db, "ALTER TABLE sessions ADD COLUMN claude_effort_level TEXT", "UPDATE sessions SET claude_effort_level='high'")
	if _, err := db.Exec("INSERT INTO session_messages VALUES ('m6','session-1','assistant',?,'2026-09-20T10:00:50Z',NULL,NULL,NULL)", thought); err != nil {
		t.Fatal(err)
	}
	db.Close()
	return conformanceSource("conductor", root)
}

// conformanceTL1 is TL1's fixture with a Claude Code run, whose cost the run
// summary reports, and a Codex rollout that names the session that started
// it.
func conformanceTL1(t *testing.T) SourceConfig {
	t.Helper()
	registry := tl1Fixture(t, 0)
	root := filepath.Dir(registry)
	db := openWritableSQLite(t, filepath.Join(root, "tl1.db"))
	execSQL(t, db,
		"INSERT INTO tasks VALUES ('task-claude','f-impl','Count the words','done','ready',NULL,NULL,'ready',NULL,'cand-1','task-impl','v2','{}','2026-09-20 10:30:00','2026-09-20 10:30:05','2026-09-20 10:40:00','2026-09-20 10:40:00')",
		"INSERT INTO task_attempts VALUES ('att-claude','task-claude',1,'wt-1','claude','claude-opus-4-5','high','claude-opus','claude','succeeded',NULL,'2026-09-20 10:30:05','2026-09-20 10:40:00')",
		"INSERT INTO transcript_summaries VALUES ('att-claude','1-claude.jsonl',0.12,'claude-opus-4-5'),('att-claude','1-codex.jsonl',NULL,'gpt-5-codex')")
	db.Close()
	main, _ := conformanceClaudeLines()
	_, agent := conformanceCodexLines()
	writeSourceFile(t, filepath.Join(root, "transcripts", "task-claude", "1-claude.jsonl"), strings.Join(main, "\n")+"\n")
	writeSourceFile(t, filepath.Join(root, "transcripts", "task-claude", "1-codex.jsonl"), strings.Join(agent, "\n")+"\n")
	return conformanceSource("tl1", registry)
}
