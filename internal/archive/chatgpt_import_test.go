package archive

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func chatGPTMediaExport(t *testing.T, file string) {
	t.Helper()
	conversation := map[string]any{"id": "media-thread", "title": "Voice and files", "create_time": 1793722000, "update_time": 1793722010, "current_node": "reply", "voice": "saved-voice", "memory_scope": "global_enabled", "is_starred": true, "mapping": map[string]any{
		"ask":   chatGPTNodeOf("ask", "", []string{"reply"}, "user", 1793722001, map[string]any{"content_type": "multimodal_text", "parts": []any{map[string]any{"content_type": "audio_transcription", "text": "A spoken question", "direction": "in"}, map[string]any{"content_type": "real_time_user_audio_video_asset_pointer", "audio_asset_pointer": map[string]any{"asset_pointer": "sediment://file_voice"}}}}, nil),
		"reply": chatGPTNodeOf("reply", "ask", nil, "assistant", 1793722002, chatGPTText("A cited answer"), map[string]any{"metadata": map[string]any{"model_slug": "bidi", "search_result_groups": []any{map[string]any{"entries": []any{map[string]any{"title": "Source", "url": "https://example.com", "snippet": "An exported snippet"}}}}}}),
	}}
	members := map[string]string{
		"conversations.json":                 jsonText([]any{conversation}),
		"file_voice.dat":                     "RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00\x40\x1f\x00\x00\x80\x3e\x00\x00\x02\x00\x10\x00data\x00\x00\x00\x00",
		"conversation_asset_file_names.json": `{"file_voice.dat":"question.wav"}`,
		"library_files.json":                 `[{"file_id":"file_voice","file_name":"question.wav","mime_type":"audio/wav","origination_thread_id":"media-thread"},{"file_id":"file_missing","file_name":"missing.gif","mime_type":"image/gif","origination_thread_id":"media-thread","state":"created"}]`,
		"shared_conversations.json":          `[{"conversation_id":"media-thread","id":"share-1","is_anonymous":true}]`,
		"conversation_summaries.json":        `[{"memory":"Account memory, without a conversation association"}]`,
		"user.json":                          `{"email":"excluded@example.com"}`, "user_settings.json": `{"setting":true}`, "ads.json": `{}`,
	}
	entries := []any{}
	for name, data := range members {
		entries = append(entries, map[string]any{"path": name, "size_bytes": len(data)})
	}
	members["export_manifest.json"] = jsonText(map[string]any{"export_files": entries})
	chatGPTZip(t, file, members)
}

func TestChatGPTImportPreservesMediaAndOfflineReader(t *testing.T) {
	config := claudeImportConfig(t)
	file := filepath.Join(t.TempDir(), "export.zip")
	chatGPTMediaExport(t, file)
	before, _ := os.ReadFile(file)
	snapshot, excluded, err := collectChatGPTExport(context.Background(), config, file)
	if err != nil || len(excluded) != 3 {
		t.Fatal(snapshot, excluded, err)
	}
	if after, _ := os.ReadFile(file); string(after) != string(before) {
		t.Fatal("modified original export")
	}
	z, err := zip.OpenReader(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range z.File {
		if chatGPTExcluded(f.Name) {
			t.Fatal("retained account record", f.Name)
		}
	}
	z.Close()
	config, err = LoadConfig(config.Path)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := MakeAdapter(config.Sources[0])
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := adapter.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	again, _, err := collectChatGPTExport(context.Background(), config, file)
	if err != nil || again != snapshot {
		t.Fatal(again, err)
	}
	if next, err := adapter.Fingerprint(); err != nil || next != fingerprint {
		t.Fatal("duplicate changed source", next, err)
	}
	// The same ZIP downloaded later stays at its original observation time.
	originalInfo, _ := os.Stat(file)
	if err := os.Chtimes(file, originalInfo.ModTime().Add(time.Hour), originalInfo.ModTime().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := collectChatGPTExport(context.Background(), config, file); err != nil {
		t.Fatal(err)
	}
	if after, err := adapter.Fingerprint(); err != nil || after != fingerprint {
		t.Fatal("re-download changed retained chronology", err)
	}
	// A retained ZIP's checksum is checked before records are emitted, and
	// a retry from the unchanged original repairs the corrupt retained copy.
	retained, _ := os.ReadFile(snapshot)
	damaged := append([]byte(nil), retained...)
	damaged[len(damaged)-1] ^= 1
	if err := os.WriteFile(snapshot, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Discover(func(WorkspaceRecord) error { t.Fatal("emitted corrupt snapshot"); return nil }); err == nil {
		t.Fatal("accepted altered retained bytes")
	}
	if _, _, err := collectChatGPTExport(context.Background(), config, file); err != nil {
		t.Fatal(err)
	}
	if result := runCapture(t, config); !result.OK {
		t.Fatal(result)
	}
	os.RemoveAll(filepath.Dir(file))
	os.RemoveAll(chatGPTImportRoot(config))
	useHost(t, "host-b")
	c, _ := testCatalog(t)
	indexCaptures(t, c, config.CaptureRoot, "host-a")
	if n := countRows(t, c, `SELECT COUNT(*) FROM conversations WHERE provider='chatgpt'`); n != 1 {
		t.Fatal(n)
	}
	rows, err := queryMaps(c.reader(), `SELECT id,text FROM messages WHERE kind='attachment'`)
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	assertMedia := func(t *testing.T, config Config) {
		t.Helper()
		server := testServer(t, config, c)
		for _, row := range rows {
			var asset map[string]any
			json.Unmarshal([]byte(firstString(row["text"])), &asset)
			request := httptest.NewRequest("GET", "/api/chatgpt-assets/"+firstString(row["id"]), nil)
			request.Header.Set("Range", "bytes=0-3")
			response := httptest.NewRecorder()
			server.chatGPTAsset(response, request)
			if asset["available"] == true {
				if response.Code != 206 || response.Body.String() != "RIFF" || !strings.HasPrefix(response.Header().Get("Content-Type"), "audio/") {
					t.Fatal(response.Code, response.Header(), response.Body.String())
				}
			} else if response.Code != 404 {
				t.Fatal("missing media was served", response.Code)
			}
		}
	}
	assertMedia(t, config)
	t.Run("completed transfer with encoded evidence", func(t *testing.T) {
		transferred := config
		transferred.CaptureRoot = filepath.Join(t.TempDir(), "captures")
		transfer := filepath.Join(transferred.CaptureRoot, ".imports", "media-transfer")
		if err := os.MkdirAll(transfer, 0700); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(transfer, "captures")
		if err := os.Rename(config.CaptureRoot, root); err != nil {
			t.Fatal(err)
		}
		// An incomplete transfer cannot supply evidence to readers.
		receipt := libraryTransferReceipt{Version: 1, ID: "media-transfer", State: "copying"}
		writeSourceFile(t, filepath.Join(transfer, "transfer.json"), jsonText(receipt))
		if _, err := capturedPathFor(transferred.CaptureRoot, "host-a", chatGPTImportSource, snapshot); err == nil {
			t.Fatal("unpublished transfer supplied evidence")
		}
		receipt.State = "complete"
		writeSourceFile(t, filepath.Join(transfer, "transfer.json"), jsonText(receipt))
		captured, err := capturedPathFor(transferred.CaptureRoot, "host-a", chatGPTImportSource, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(captured)
		if err != nil {
			t.Fatal(err)
		}
		encoded := zstdBytes(t, data)
		if err := os.WriteFile(captured, encoded, 0600); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(root, "host-a", chatGPTImportSource)
		manifest, err := loadCaptureManifest(dir)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, entry := range manifest.Files {
			if entry.Path == snapshot {
				entry.Encoding, entry.StoredSize = captureEncodingZstd, int64(len(encoded))
				found = true
			}
		}
		if !found {
			t.Fatal("fixture lacks the snapshot entry")
		}
		writeSourceFile(t, filepath.Join(dir, captureManifestName), jsonText(manifest))
		assertMedia(t, transferred)
	})

	var contextText, raw string
	if err := c.DB.QueryRow(`SELECT text FROM messages WHERE native_id='media-thread:export-context'`).Scan(&contextText); err != nil || !strings.Contains(contextText, "saved-voice") || !strings.Contains(contextText, "share-1") {
		t.Fatal(contextText, err)
	}
	if err := c.DB.QueryRow(`SELECT raw_text FROM messages WHERE native_id='reply'`).Scan(&raw); err != nil || !strings.Contains(raw, "exported snippet") {
		t.Fatal(raw, err)
	}
}

func TestChatGPTImportRejectsUnsafeIncompleteAndDamagedExports(t *testing.T) {
	config := claudeImportConfig(t)
	for name, members := range map[string]map[string]string{
		"traversal":       {"../conversations.json": "[]"},
		"bad later shard": {"conversations-000.json": jsonText(chatGPTConversations()), "conversations-001.json": "[{"},
		"missing member":  {"conversations.json": "[]", "export_manifest.json": `{"export_files":[{"path":"file_missing.dat","size_bytes":1}]}`},
	} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "export.zip")
			chatGPTZip(t, file, members)
			if _, _, err := collectChatGPTExport(context.Background(), config, file); err == nil {
				t.Fatal("accepted invalid export")
			}
		})
	}
	t.Run("duplicate empty members", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "duplicate.zip")
		out, _ := os.Create(file)
		z := zip.NewWriter(out)
		for _, name := range []string{"empty", "empty", "conversations.json"} {
			w, _ := z.Create(name)
			if name == "conversations.json" {
				w.Write([]byte("[]"))
			}
		}
		z.Close()
		out.Close()
		if _, _, err := collectChatGPTExport(context.Background(), config, file); err == nil {
			t.Fatal("accepted duplicate")
		}
	})
	t.Run("CRC", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "crc.zip")
		out, _ := os.Create(file)
		z := zip.NewWriter(out)
		w, _ := z.CreateHeader(&zip.FileHeader{Name: "conversations.json", Method: zip.Store})
		w.Write([]byte(`[{"id":"check-crc","mapping":{}}]`))
		z.Close()
		out.Close()
		data, _ := os.ReadFile(file)
		data = []byte(strings.Replace(string(data), "check-crc", "check-bad", 1))
		os.WriteFile(file, data, 0600)
		if _, _, err := collectChatGPTExport(context.Background(), config, file); err == nil {
			t.Fatal("accepted corrupt CRC")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(t.TempDir(), "original.zip")
		chatGPTMediaExport(t, file)
		link := filepath.Join(dir, "export.zip")
		os.Symlink(file, link)
		if _, _, err := collectChatGPTExport(context.Background(), config, link); err == nil {
			t.Fatal("followed an external symlink")
		}
	})
	t.Run("cancel", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "export.zip")
		chatGPTMediaExport(t, file)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, _, err := collectChatGPTExport(ctx, config, file); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
	if _, err := os.Stat(filepath.Join(chatGPTImportRoot(config), chatGPTInventoryName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("published invalid export", err)
	}
}

func TestChatGPTDownloadsConsentRetryAndStop(t *testing.T) {
	config := claudeImportConfig(t)
	dir := t.TempDir()
	file := filepath.Join(dir, strings.Repeat("a", 64)+"-2026-11-11-21-59-32-"+strings.Repeat("b", 32)+".zip")
	chatGPTMediaExport(t, file)
	chatGPTMediaExport(t, filepath.Join(dir, "unrelated.zip"))
	d := &chatGPTImports{dir: dir, config: func() Config { return config }, index: func(context.Context, string, ProgressFunc) error { return errors.New("index unavailable") }}
	if more, err := d.Check(context.Background()); err != nil || more || len(d.load().Jobs) != 0 {
		t.Fatal(more, err, d.load())
	}
	if err := d.act(map[string]any{"enabled": true}); err != nil {
		t.Fatal(err)
	}
	d.collect = func(context.Context, Config, string) (string, []string, error) { return "", nil, errClaudeImportBusy }
	if more, err := d.Check(context.Background()); !more || !errors.Is(err, errClaudeImportBusy) {
		t.Fatal(more, err)
	}
	if jobs := d.load().Jobs; len(jobs) != 1 || jobs[0].Phase != "queued" {
		t.Fatal(jobs)
	}
	d.collect = nil
	if _, err := d.Check(context.Background()); err == nil {
		t.Fatal("expected paused index")
	}
	job := d.load().Jobs[0]
	if job.State != "paused" || job.Snapshot == "" {
		t.Fatal(job)
	}
	os.Remove(file)
	if err := d.act(map[string]any{"enabled": false, "id": job.ID, "action": "retry"}); err != nil {
		t.Fatal(err)
	}
	d.index = func(context.Context, string, ProgressFunc) error { return nil }
	if _, err := d.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if job = d.load().Jobs[0]; job.State != "imported" {
		t.Fatal(job)
	}
	// A stop decision during collection wins over its late cancelled result.
	chatGPTMediaExport(t, file)
	if err := d.act(map[string]any{"path": file}); err != nil {
		t.Fatal(err)
	}
	d.collect = func(ctx context.Context, _ Config, _ string) (string, []string, error) {
		id := d.working
		if err := d.act(map[string]any{"id": id, "action": "stop"}); err != nil {
			t.Fatal(err)
		}
		return "", nil, ctx.Err()
	}
	d.Check(context.Background())
	if jobs := d.load().Jobs; jobs[0].State != "stopped" {
		t.Fatal(jobs)
	}
	for _, damage := range []string{"missing", "checksum"} {
		t.Run("retry repairs "+damage, func(t *testing.T) {
			cfg := claudeImportConfig(t)
			original := filepath.Join(t.TempDir(), "export.zip")
			chatGPTMediaExport(t, original)
			originalBytes, err := os.ReadFile(original)
			if err != nil {
				t.Fatal(err)
			}
			imports := &chatGPTImports{config: func() Config { return cfg }, index: func(context.Context, string, ProgressFunc) error { return errors.New("index unavailable") }}
			if err := imports.act(map[string]any{"path": original}); err != nil {
				t.Fatal(err)
			}
			if _, err := imports.Check(t.Context()); err == nil {
				t.Fatal("expected initial index pause")
			}
			job := imports.load().Jobs[0]
			if job.Snapshot == "" || job.State != "paused" {
				t.Fatal(job)
			}
			if damage == "missing" {
				err = os.Remove(job.Snapshot)
			} else {
				err = os.WriteFile(job.Snapshot, []byte("damaged retained ZIP"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			collections := 0
			imports.collect = func(ctx context.Context, cfg Config, file string) (string, []string, error) {
				collections++
				return collectChatGPTExport(ctx, cfg, file)
			}
			imports.index = func(ctx context.Context, _ string, _ ProgressFunc) error {
				return verifyChatGPTSnapshot(ctx, cfg, job.Snapshot)
			}
			if err := imports.act(map[string]any{"id": job.ID, "action": "retry"}); err != nil {
				t.Fatal(err)
			}
			if _, err := imports.Check(t.Context()); err != nil {
				t.Fatal(err)
			}
			repaired := imports.load().Jobs[0]
			if repaired.State != "imported" || repaired.Snapshot != job.Snapshot || collections != 1 {
				t.Fatal("retry did not repair and index", repaired, collections)
			}
			if data, err := os.ReadFile(original); err != nil || string(data) != string(originalBytes) {
				t.Fatal("retry changed original", err)
			}
			// Healthy snapshots still support index-only retries without Downloads.
			if err := os.Remove(original); err != nil {
				t.Fatal(err)
			}
			if err := imports.act(map[string]any{"id": job.ID, "action": "retry"}); err != nil {
				t.Fatal(err)
			}
			if _, err := imports.Check(t.Context()); err != nil {
				t.Fatal(err)
			}
			if collections != 1 || imports.load().Jobs[0].State != "imported" {
				t.Fatal("healthy retry recollected", collections, imports.load())
			}
		})
	}

}

func TestChatGPTFolderPreservesSidecarsAndImageOnlyMetadata(t *testing.T) {
	dir := t.TempDir()
	conversation := map[string]any{"id": "image-only", "title": "Image", "mapping": map[string]any{"image": chatGPTNodeOf("image", "", nil, "user", 1793722001, map[string]any{"content_type": "multimodal_text", "parts": []any{map[string]any{"content_type": "image_asset_pointer", "asset_pointer": "file-service://file_picture"}}}, map[string]any{"metadata": map[string]any{"status": "source metadata"}})}}
	writeSourceFile(t, filepath.Join(dir, "conversations.json"), jsonText([]any{conversation}))
	writeSourceFile(t, filepath.Join(dir, "file_picture.dat"), "image bytes")
	writeSourceFile(t, filepath.Join(dir, "conversation_asset_file_names.json"), `{"file_picture.dat":"picture.png"}`)
	writeSourceFile(t, filepath.Join(dir, "user.json"), `{"email":"excluded"}`)
	writeSourceFile(t, filepath.Join(dir, "unrelated.txt"), "not export evidence")
	records := chatGPTRecords(t, dir)["image-only"].Conversations[0].Messages
	found, attachment := false, false
	for _, record := range records {
		if record.NativeID == "image" {
			found = record.Kind == "metadata" && strings.Contains(record.RawText, "source metadata")
		}
		if record.Kind == "attachment" {
			attachment = strings.Contains(record.Text, "picture.png") && strings.Contains(record.Text, `"available":true`)
		}
	}
	if !found || !attachment {
		t.Fatal("image-only message lost evidence", records)
	}
	plan, err := planCapture(context.Background(), chatGPTSource(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.files) != 3 {
		t.Fatal("folder evidence selection", plan.files)
	}
	adapter, _ := MakeAdapter(chatGPTSource(dir))
	before, _ := adapter.Fingerprint()
	writeSourceFile(t, filepath.Join(dir, "conversation_asset_file_names.json"), `{"file_picture.dat":"renamed.png"}`)
	if after, err := adapter.Fingerprint(); err != nil || before == after {
		t.Fatal("sidecar edit did not change fingerprint", err)
	}
}
