package archive

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func claudeImportConfig(t *testing.T) Config {
	t.Helper()
	probeHome(t)
	t.Setenv("PHAROS_SUPPORT_DIR", t.TempDir())
	useHost(t, "host-a")
	config, err := LoadConfig(probeLibrary(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if err := config.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	openTestCatalog(t, config.CatalogPath).Close()
	return config
}

func claudeImportManifest(t *testing.T, dir string, entries ...claudeManifestPart) string {
	t.Helper()
	total := len(entries)
	file := filepath.Join(dir, "manifest.json")
	probePut(t, file, jsonText(claudeManifest{Version: "1.0", CreatedAt: "2026-08-01T00:00:00Z", TotalFiles: &total, DataFiles: entries}))
	return file
}

func TestClaudeImportCollectsMultipartAndIndexesCapturedFiles(t *testing.T) {
	config := claudeImportConfig(t)
	input := t.TempDir()
	var conversations []json.RawMessage
	if err := json.Unmarshal([]byte(claudeAIExport), &conversations); err != nil {
		t.Fatal(err)
	}
	chatGPTZip(t, filepath.Join(input, "conversations-000.zip"), map[string]string{"wrapped/conversations-000.json": jsonText(conversations[:1])})
	// Older single-ZIP exports put account records beside the conversations.
	chatGPTZip(t, filepath.Join(input, "conversations-001.zip"), map[string]string{"conversations.json": jsonText(conversations[1:]), "users.json": `[{"email_address":"a@example.com"}]`})
	chatGPTZip(t, filepath.Join(input, "projects-000.zip"), map[string]string{"projects/p.json": `{"uuid":"p","docs":[{"content":"original context"}]}`})
	chatGPTZip(t, filepath.Join(input, "light_metadata-000.zip"), map[string]string{"users.json": `[]`, "login_history.json": `{"login_events":[]}`})
	manifest := claudeImportManifest(t, input,
		claudeManifestPart{Filename: "light_metadata-000.zip", Category: "light_metadata", URL: "https://claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/secret-4"},
		claudeManifestPart{Filename: "conversations-001.zip", Category: "conversations", Part: 1, URL: "https://claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/secret-1"},
		claudeManifestPart{Filename: "projects-000.zip", Category: "projects", URL: "https://claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/secret-2"},
		claudeManifestPart{Filename: "conversations-000.zip", Category: "conversations", URL: "https://claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/secret-3"})
	job, err := collectClaudeExport(context.Background(), config, claudeImportRequest{Paths: []string{manifest}})
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "collected" || len(job.Parts) != 4 || job.Source == nil {
		t.Fatalf("job = %+v", job)
	}
	for _, part := range job.Parts {
		if part.Category == "light_metadata" {
			if part.State != claudePartExcluded || part.Storage != "" {
				t.Fatalf("account records kept: %+v", part)
			}
			continue
		}
		if part.State != "collected" || part.SHA256 == "" {
			t.Fatalf("part = %+v", part)
		}
		stored, err := zip.OpenReader(filepath.Join(claudeImportJobDir(config, job.Name, job.ID), filepath.FromSlash(part.Storage)))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range stored.File {
			if claudeAccountRecord(entry.Name) {
				t.Errorf("%s kept %s", part.Filename, entry.Name)
			}
		}
		stored.Close()
		if err := verifyClaudeStoredPart(context.Background(), claudeImportJobDir(config, job.Name, job.ID), part); err != nil {
			t.Fatal(part.Filename, err)
		}
	}
	// URL capabilities never enter a library file, API status or a capture.
	if err := filepath.WalkDir(config.CaptureRoot, func(file string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		content, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if bytes.Contains(content, []byte("secret-")) {
			t.Errorf("download capability persisted in library %s", file)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonText(loadClaudeLinks(config, job.ID)), "secret-") {
		t.Fatal("host lost the download links")
	}
	config, err = LoadConfig(config.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Sources) != 1 {
		t.Fatal(config.Sources)
	}
	reader, err := MakeAdapter(config.Sources[0])
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := reader.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	c, _ := testCatalog(t)
	ingestSource(t, c, config.Sources[0])
	// Retrying is idempotent and uses the same stable source.
	again, err := collectClaudeExport(context.Background(), config, claudeImportRequest{ID: job.ID})
	if err != nil || again.ID != job.ID {
		t.Fatal(again, err)
	}
	if next, err := reader.Fingerprint(); err != nil || next != fingerprint {
		t.Fatal("an unchanged verified retry changed its source fingerprint", err)
	}
	ingestSource(t, c, config.Sources[0])
	var count int
	if err := c.DB.QueryRow(`SELECT COUNT(*) FROM conversations WHERE provider='claude'`).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	if result := runCapture(t, config); !result.OK {
		t.Fatal(result)
	}
	if err := os.RemoveAll(input); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(claudeImportRoot(config, job.Name)); err != nil {
		t.Fatal(err)
	}
	useHost(t, "host-b")
	other, _ := testCatalog(t)
	indexCaptures(t, other, config.CaptureRoot, "host-a")
	if err := other.DB.QueryRow(`SELECT COUNT(*) FROM conversations WHERE provider='claude'`).Scan(&count); err != nil || count != 2 {
		t.Fatal("offline conversations", count, err)
	}
	if err := other.DB.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&count); err != nil || count != 15 {
		t.Fatal("offline messages", count, err)
	}
}

func TestClaudeImportRepairsMissingCorruptAndAmbiguousParts(t *testing.T) {
	config := claudeImportConfig(t)
	input := t.TempDir()
	manifest := claudeImportManifest(t, input, claudeManifestPart{Filename: "conversations-000.zip", Category: "conversations", URL: "https://claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/token"})
	job, err := collectClaudeExport(context.Background(), config, claudeImportRequest{Paths: []string{manifest}})
	if err != nil || job.State != "incomplete" || job.Source != nil {
		t.Fatal(job, err)
	}
	zipName := filepath.Join(input, "conversations-000.zip")
	probePut(t, zipName, "not a zip")
	job, err = collectClaudeExport(context.Background(), config, claudeImportRequest{ID: job.ID})
	if err != nil || job.Parts[0].State != "failed" {
		t.Fatal(job, err)
	}
	writeClaudeAIZip(t, zipName)
	probePut(t, zipName+".part", "still downloading")
	job, err = collectClaudeExport(context.Background(), config, claudeImportRequest{ID: job.ID})
	if err != nil || job.Parts[0].State != "missing" {
		t.Fatal(job, err)
	}
	if err := os.Remove(zipName + ".part"); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(input, "conversations-000 (1).zip")
	writeClaudeAIZip(t, second)
	job, err = collectClaudeExport(context.Background(), config, claudeImportRequest{ID: job.ID})
	if err != nil || job.Parts[0].State != "ambiguous" {
		t.Fatal(job, err)
	}
	job, err = collectClaudeExport(context.Background(), config, claudeImportRequest{ID: job.ID, Paths: []string{second}})
	if err != nil || job.State != "collected" || job.Parts[0].Conversations != 2 {
		t.Fatal(job, err)
	}
	// Receipt readback detects an edited local copy, including equal-length edits.
	stored := filepath.Join(claudeImportJobDir(config, job.Name, job.ID), filepath.FromSlash(job.Parts[0].Storage))
	content, err := os.ReadFile(stored)
	if err != nil {
		t.Fatal(err)
	}
	content[len(content)/2] ^= 1
	if err := os.WriteFile(stored, content, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(zipName); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(second); err != nil {
		t.Fatal(err)
	}
	job, err = collectClaudeExport(context.Background(), config, claudeImportRequest{ID: job.ID})
	if err != nil || job.Parts[0].State != "damaged" {
		t.Fatal(job, err)
	}
	// Recreating the same archive restores the original part without adding
	// its record counts to the previous attempt's counts.
	writeClaudeAIZip(t, zipName)
	job, err = collectClaudeExport(context.Background(), config, claudeImportRequest{ID: job.ID, Paths: []string{zipName}})
	if err != nil || job.State != "collected" || job.Parts[0].Conversations != 2 {
		t.Fatal(job, err)
	}
}

func TestClaudeImportKeepsSnapshotsSeparateAndRejectsStaleDownloads(t *testing.T) {
	config := claudeImportConfig(t)
	input := t.TempDir()
	zipName := filepath.Join(input, "conversations-000.zip")
	writeClaudeAIZip(t, zipName)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(zipName, old, old); err != nil {
		t.Fatal(err)
	}
	manifest := claudeImportManifest(t, input, claudeManifestPart{Filename: "conversations-000.zip", Category: "conversations"})
	job, err := collectClaudeExport(context.Background(), config, claudeImportRequest{Paths: []string{manifest}})
	if err != nil || job.Parts[0].State != "missing" {
		t.Fatal(job, err)
	}
	// An exact file explicitly selected by the person can be an older copy.
	job, err = collectClaudeExport(context.Background(), config, claudeImportRequest{ID: job.ID, Paths: []string{zipName}})
	if err != nil || job.State != "collected" {
		t.Fatal(job, err)
	}
	var output bytes.Buffer
	if err := runClaudeImportCLI(context.Background(), config, []string{"status", "--job", job.ID}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), job.ID) || strings.Contains(output.String(), "export_url") {
		t.Fatal(output.String())
	}
	// A later export reuses filenames; its browser "(1)" copy also postdates
	// this manifest, but only the copy downloaded while the links worked fits.
	repeated := t.TempDir()
	created := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	for name, at := range map[string]time.Time{"conversations-000.zip": created.Add(time.Hour), "conversations-000 (1).zip": created.Add(14 * 24 * time.Hour)} {
		writeClaudeAIZip(t, filepath.Join(repeated, name))
		if err := os.Chtimes(filepath.Join(repeated, name), at, at); err != nil {
			t.Fatal(err)
		}
	}
	first := filepath.Join(repeated, "manifest-first.json")
	probePut(t, first, `{"version":"1.0","created_at":"2026-08-03T00:00:00Z","data_files":[{"filename":"conversations-000.zip","category":"conversations"}]}`)
	job, err = collectClaudeExport(context.Background(), config, claudeImportRequest{Paths: []string{first}})
	if err != nil || job.State != "collected" {
		t.Fatal(job, err)
	}
	// Without a manifest, the downloaded file's time orders the snapshot,
	// not the time Pharos copied it. A leftover partial copy is removed.
	legacy := filepath.Join(t.TempDir(), "claude-export.zip")
	writeClaudeAIZip(t, legacy)
	downloaded := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(legacy, downloaded, downloaded); err != nil {
		t.Fatal(err)
	}
	loose, err := collectClaudeExport(context.Background(), config, claudeImportRequest{Paths: []string{legacy}})
	if err != nil || loose.State != "collected" {
		t.Fatal(loose, err)
	}
	snapshot := claudeImportJobDir(config, loose.Name, loose.ID)
	probePut(t, filepath.Join(snapshot, "parts", ".collect-orphan"), "unfiltered")
	if _, err := collectClaudeExport(context.Background(), config, claudeImportRequest{ID: loose.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(snapshot, "parts", ".collect-orphan")); !os.IsNotExist(err) {
		t.Fatal("a leftover partial copy was kept", err)
	}
	inputs, err := claudeExportInputs(sourceFiles{}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range inputs {
		if input.conversations && input.version != downloaded.UnixNano() {
			t.Fatalf("snapshot version %d, want the download time", input.version)
		}
	}
	// A person's folder contributes only its own top-level conversation
	// files: an unrelated or nested ZIP and a link elsewhere are not read.
	folder := t.TempDir()
	probePut(t, filepath.Join(folder, "conversations.json"), "[]")
	writeClaudeAIZip(t, filepath.Join(folder, "unrelated.zip"))
	if err := os.MkdirAll(filepath.Join(folder, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	writeClaudeAIZip(t, filepath.Join(folder, "nested", "conversations-000.zip"))
	if err := os.Symlink(t.TempDir(), filepath.Join(folder, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	if inputs, err := claudeExportInputs(sourceFiles{}, folder); err != nil || len(inputs) != 1 || filepath.Base(inputs[0].path) != "conversations.json" {
		t.Fatal(inputs, err)
	}
	// A collection is Pharos's own tree, so a linked part is refused.
	part := filepath.Join(snapshot, filepath.FromSlash(loose.Parts[0].Storage))
	target := filepath.Join(t.TempDir(), "elsewhere.zip")
	if err := os.Rename(part, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, part); err != nil {
		t.Fatal(err)
	}
	if _, err := claudeExportInputs(sourceFiles{}, snapshot); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatal("a linked part was accepted", err)
	}
	if err := os.Remove(part); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, part); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(input, "manifest-second.json")
	probePut(t, other, `{"version":"1.0","created_at":"2026-08-02T00:00:00Z","data_files":[{"filename":"conversations-000.zip","category":"conversations"}]}`)
	if _, err := collectClaudeExport(context.Background(), config, claudeImportRequest{Paths: []string{input}}); err == nil || !strings.Contains(err.Error(), "multiple manifests") {
		t.Fatal(err)
	}
}

func TestClaudeImportManifestsAndIntegrityLimits(t *testing.T) {
	config := claudeImportConfig(t)
	for name, content := range map[string]string{
		"traversal":     `{"data_files":[{"filename":"../outside.zip","category":"conversations"}]}`,
		"duplicate":     `{"data_files":[{"filename":"x.zip","category":"conversations"},{"filename":"x.zip","category":"projects"}]}`,
		"bad count":     `{"total_files":2,"data_files":[{"filename":"x.zip","category":"conversations"}]}`,
		"null list":     `{"data_files":null}`,
		"bad timestamp": `{"created_at":"yesterday","data_files":[{"filename":"x.zip","category":"conversations"}]}`,
		"bad checksum":  `{"data_files":[{"filename":"x.zip","category":"conversations","sha256":"x"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "manifest.json")
			probePut(t, file, content)
			if _, err := collectClaudeExport(context.Background(), config, claudeImportRequest{Paths: []string{file}}); err == nil {
				t.Fatal("accepted malformed manifest")
			}
		})
	}
	for name, members := range map[string]map[string]string{
		"traversal":           {"../conversations.json": claudeAIExport},
		"bad nested hash":     {"artifacts/a/versions/v.files.json": `{"files":[{"exported_as":"index.html","sha256":"bad","size_bytes":5}]}`, "artifacts/a/versions/v/index.html": "hello"},
		"missing nested file": {"artifacts/a/versions/v.files.json": `{"files":[{"exported_as":"index.html"}]}`},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "frames-000.zip")
			chatGPTZip(t, file, members)
			manifest := claudeImportManifest(t, dir, claudeManifestPart{Filename: "frames-000.zip", Category: "frames"})
			job, err := collectClaudeExport(context.Background(), config, claudeImportRequest{Paths: []string{manifest}})
			if err != nil || job.Parts[0].State != "failed" {
				t.Fatal(job, err)
			}
		})
	}
	t.Run("member budget", func(t *testing.T) {
		limit := claudeExportLimits
		limit.Member = 10
		override(t, &claudeExportLimits, limit)
		dir := t.TempDir()
		file := filepath.Join(dir, "conversations-000.zip")
		writeClaudeAIZip(t, file)
		job, err := collectClaudeExport(context.Background(), config, claudeImportRequest{Paths: []string{file}})
		if err != nil || job.Parts[0].State != "failed" {
			t.Fatal(job, err)
		}
	})
}

func TestClaudeImportNestedManifestsAndArtifactFiles(t *testing.T) {
	config := claudeImportConfig(t)
	input := t.TempDir()
	writeClaudeAIZip(t, filepath.Join(input, "conversations-000.zip"))
	content := "<script>this must remain inert</script>"
	chatGPTZip(t, filepath.Join(input, "frames-000.zip"), map[string]string{
		"artifacts/a/versions/v/index.html": content,
		"artifacts/a/versions/v.files.json": jsonText(map[string]any{"files": []any{map[string]any{"exported_as": "index.html", "sha256": hashBytes([]byte(content)), "size_bytes": len(content)}}}),
	})
	probePut(t, filepath.Join(input, "child.json"), `{"version":"1.0","data_files":[{"filename":"conversations-000.zip","category":"conversations","export_url":"https://claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/child-secret"},{"filename":"frames-000.zip","category":"frames"}]}`)
	manifest := claudeImportManifest(t, input, claudeManifestPart{Filename: "child.json", Category: "manifest", URL: "https://claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/parent-secret"})
	job, err := collectClaudeExport(context.Background(), config, claudeImportRequest{Paths: []string{input}})
	if err != nil || job.State != "collected" || len(job.Parts) != 3 {
		t.Fatal(job, err)
	}
	for _, part := range job.Parts {
		if part.State != "collected" {
			t.Fatal(part)
		}
	}
	parent := job.Parts[0]
	stored, err := os.ReadFile(filepath.Join(claudeImportJobDir(config, job.Name, job.ID), filepath.FromSlash(parent.Storage)))
	if err != nil || strings.Contains(string(stored), "child-secret") {
		t.Fatal(string(stored), err)
	}
	if _, err := os.Stat(filepath.Join(input, "index.html")); !os.IsNotExist(err) {
		t.Fatal("artifact extracted into input folder")
	}
	// Repairing the parent must reuse the same child records and their counts.
	storedPath := filepath.Join(claudeImportJobDir(config, job.Name, job.ID), filepath.FromSlash(parent.Storage))
	probePut(t, storedPath, "damaged manifest")
	job, err = collectClaudeExport(context.Background(), config, claudeImportRequest{ID: job.ID})
	if err != nil || job.State != "collected" || len(job.Parts) != 3 || job.Parts[1].Conversations != 2 {
		t.Fatal("nested manifest repair", job, err)
	}
	t.Run("failed manifest publication", func(t *testing.T) {
		raw, err := os.ReadFile(manifest)
		if err != nil {
			t.Fatal(err)
		}
		// Make the final rename fail after verification and expansion.
		request := claudeImportRequest{Name: "failed-manifest", Paths: []string{manifest}}
		target := filepath.Join(claudeImportJobDir(config, request.Name, hashBytes(raw)), "parts", hashBytes([]byte("child.json"))+".json")
		if err := os.MkdirAll(target, 0700); err != nil {
			t.Fatal(err)
		}
		job, err := collectClaudeExport(context.Background(), config, request)
		if err != nil || job.State != "incomplete" || len(job.Parts) != 1 || job.Parts[0].State != "failed" {
			t.Fatal("failed parent published children", job, err)
		}
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		job, err = collectClaudeExport(context.Background(), config, claudeImportRequest{Name: request.Name, ID: job.ID})
		if err != nil || job.State != "collected" || len(job.Parts) != 3 {
			t.Fatal("failed parent was not retryable", job, err)
		}
	})
	t.Run("cycle", func(t *testing.T) {
		dir := t.TempDir()
		root := claudeImportManifest(t, dir, claudeManifestPart{Filename: "manifest.json", Category: "manifest"})
		job, err := collectClaudeExport(context.Background(), config, claudeImportRequest{Paths: []string{root}})
		if err != nil || job.State != "incomplete" || !strings.Contains(job.Parts[0].Error, "cycle") {
			t.Fatal(job, err)
		}
	})
}

func TestClaudeImportCancellationAndBrowserBoundary(t *testing.T) {
	for _, url := range []string{"http://claude.ai/x", "https://localhost/x", "https://claude.ai.evil.test/x", "https://claude.ai:443/x", "https://user:password@claude.ai/x", "file:///tmp/export.zip", "https://127.0.0.1/x"} {
		if safeClaudeDownloadURL(url) != "" {
			t.Fatal("accepted", url)
		}
	}
	if safeClaudeDownloadURL("https://claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/token") == "" {
		t.Fatal("rejected Claude download")
	}
	config := claudeImportConfig(t)
	input := t.TempDir()
	manifest := claudeImportManifest(t, input, claudeManifestPart{Filename: "missing.zip", Category: "conversations"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	job, err := collectClaudeExport(ctx, config, claudeImportRequest{Paths: []string{manifest}})
	if !errors.Is(err, context.Canceled) || job.State != "interrupted" {
		t.Fatal(job, err)
	}
	lock, err := claudeImportLock(config, job.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := collectClaudeExport(context.Background(), config, claudeImportRequest{ID: job.ID}); err == nil {
		t.Fatal("concurrent job acquired the same collection")
	}
}

func TestClaudeImportRejectsDuplicateZIPPaths(t *testing.T) {
	config := claudeImportConfig(t)
	file := filepath.Join(t.TempDir(), "conversations-000.zip")
	out, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(out)
	for i := 0; i < 2; i++ {
		entry, err := archive.Create("conversations.json")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(claudeAIExport)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	job, err := collectClaudeExport(context.Background(), config, claudeImportRequest{Paths: []string{file}})
	if err != nil || job.Parts[0].State != "failed" {
		t.Fatal(job, err)
	}
}

func TestClaudeImportAPIResumesJobsAndScopesDownloadLinks(t *testing.T) {
	config := claudeImportConfig(t)
	server := probeServer(t, config.Path)
	input := t.TempDir()
	manifest := claudeImportManifest(t, input, claudeManifestPart{Filename: "conversations-000.zip", Category: "conversations", URL: "https://claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/private-token"},
		claudeManifestPart{Filename: "light_metadata-000.zip", Category: "light_metadata", URL: "https://claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/account-token"})
	// A snapshot interrupted before its first save does not hide the others.
	if err := os.MkdirAll(filepath.Join(claudeImportRoot(config, "claude-ai-imports"), "snapshots", strings.Repeat("e", 64)), 0700); err != nil {
		t.Fatal(err)
	}
	code, response := probeCall(t, server, http.MethodPost, "/api/claude-imports", jsonText(claudeImportRequest{Paths: []string{manifest}}))
	if code != 202 || response["started"] != true {
		t.Fatal(code, response)
	}
	waitUntil(t, 5*time.Second, "Claude import collection", func() bool {
		server.claudeImports.mu.Lock()
		defer server.claudeImports.mu.Unlock()
		return !server.claudeImports.Running
	})
	code, response = probeCall(t, server, http.MethodGet, "/api/claude-imports", "")
	if code != 200 || strings.Contains(jsonText(response), "private-token") {
		t.Fatal(code, response)
	}
	run := mapValue(response["run"])
	if firstString(run["error"]) != "" {
		t.Fatal(run)
	}
	id := firstString(run["id"])
	code, job := probeCall(t, server, http.MethodGet, "/api/claude-imports?id="+id, "")
	if code != 200 || job["state"] != "incomplete" {
		t.Fatal(code, job)
	}
	code, response = probeCall(t, server, http.MethodPost, "/api/claude-imports/links", jsonText(claudeImportRequest{ID: id}))
	if code != 200 || !strings.Contains(jsonText(response), "private-token") || strings.Contains(jsonText(response), "account-token") {
		t.Fatal(code, response)
	}
	if content, err := os.ReadFile(claudeImportSecrets(config, id)); err != nil || strings.Contains(string(content), "account-token") {
		t.Fatal("the account-records link was kept", err)
	}
	writeClaudeAIZip(t, filepath.Join(input, "conversations-000.zip"))
	code, response = probeCall(t, server, http.MethodPost, "/api/claude-imports", jsonText(claudeImportRequest{ID: id}))
	if code != 202 {
		t.Fatal(code, response)
	}
	waitUntil(t, 5*time.Second, "Claude import repair", func() bool {
		server.claudeImports.mu.Lock()
		defer server.claudeImports.mu.Unlock()
		return !server.claudeImports.Running
	})
	code, job = probeCall(t, server, http.MethodGet, "/api/claude-imports?id="+id, "")
	if code != 200 || job["state"] != "collected" || len(server.Config().Sources) != 1 {
		t.Fatal(code, job)
	}
	code, response = probeCall(t, server, http.MethodPost, "/api/claude-imports/links", jsonText(claudeImportRequest{ID: id}))
	if code != 200 || strings.Contains(jsonText(response), "private-token") {
		t.Fatal(code, response)
	}
	if code, _ := probeCall(t, server, http.MethodGet, "/api/claude-imports?id=../../secret", ""); code != 400 {
		t.Fatal(code)
	}
}

func TestClaudeImportConfinesManifestPathsAndLimitsRetainedBytes(t *testing.T) {
	config := claudeImportConfig(t)
	input, outside := t.TempDir(), t.TempDir()
	writeClaudeAIZip(t, filepath.Join(outside, "conversations-000.zip"))
	if err := os.Symlink(outside, filepath.Join(input, "redirect")); err != nil {
		t.Fatal(err)
	}
	manifest := claudeImportManifest(t, input, claudeManifestPart{Filename: "redirect/conversations-000.zip", Category: "conversations"})
	job, err := collectClaudeExport(context.Background(), config, claudeImportRequest{Paths: []string{manifest}})
	if err != nil || job.Parts[0].State != "failed" {
		t.Fatal(job, err)
	}
	t.Run("retained bytes", func(t *testing.T) {
		override(t, &claudeImportBudget, int64(1))
		file := filepath.Join(t.TempDir(), "conversations-000.zip")
		writeClaudeAIZip(t, file)
		job, err := collectClaudeExport(context.Background(), config, claudeImportRequest{Paths: []string{file}})
		if err != nil || !strings.Contains(job.Parts[0].Error, "budget") {
			t.Fatal(job, err)
		}
	})
}

type claudeWatchOutput struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (output *claudeWatchOutput) Write(p []byte) (int, error) {
	n, err := output.Buffer.Write(p)
	if bytes.Contains(p, []byte("\n")) {
		output.cancel()
	}
	return n, err
}

func TestClaudeImportWatcherCollectsOnlyRootManifestsAndStopsIdle(t *testing.T) {
	config := claudeImportConfig(t)
	input := t.TempDir()
	writeClaudeAIZip(t, filepath.Join(input, "conversations-000.zip"))
	probePut(t, filepath.Join(input, "child.json"), `{"data_files":[{"filename":"conversations-000.zip","category":"conversations"}]}`)
	claudeImportManifest(t, input, claudeManifestPart{Filename: "child.json", Category: "manifest"})
	// The inbox can be Downloads; unrelated JSON is not reported as an error.
	probePut(t, filepath.Join(input, "a-worker-log.json"), `{"line":1}{"line":2}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &claudeWatchOutput{cancel: cancel}
	err := runClaudeImportCLI(ctx, config, []string{"watch", input}, output)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	jobs, err := listClaudeImports(config)
	if err != nil || len(jobs) != 1 || jobs[0].State != "collected" {
		t.Fatal(jobs, err)
	}
	if leases := probeLeases(leaseDirs(config)); len(leases) != 0 {
		t.Fatal("watcher retained an idle write lease", leases)
	}
	if !strings.Contains(output.String(), `"state":"collected"`) {
		t.Fatal(output.String())
	}
}

func TestClaudeImportChecksCRCAndDoesNotRecreateAnUnavailableLibrary(t *testing.T) {
	config := claudeImportConfig(t)
	file := filepath.Join(t.TempDir(), "conversations-000.zip")
	buffer := &bytes.Buffer{}
	writer := zip.NewWriter(buffer)
	member, err := writer.CreateHeader(&zip.FileHeader{Name: "conversations.json", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := member.Write([]byte(claudeAIExport)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	content := buffer.Bytes()
	offset := bytes.Index(content, []byte("Trip"))
	if offset < 0 {
		t.Fatal("fixture title not found")
	}
	content[offset] = 'F'
	if err := os.WriteFile(file, content, 0600); err != nil {
		t.Fatal(err)
	}
	job, err := collectClaudeExport(context.Background(), config, claudeImportRequest{Paths: []string{file}})
	if err != nil || job.Parts[0].State != "failed" || !strings.Contains(job.Parts[0].Error, "checksum") {
		t.Fatal(job, err)
	}
	if err := os.Rename(config.CatalogPath, config.CatalogPath+".away"); err != nil {
		t.Fatal(err)
	}
	if _, err := collectClaudeExport(context.Background(), config, claudeImportRequest{Name: "another", Paths: []string{file}}); err == nil {
		t.Fatal("collected into an unavailable library")
	}
	if _, err := os.Stat(claudeImportRoot(config, "another")); !os.IsNotExist(err) {
		t.Fatal("created a new destination after losing the library", err)
	}
}

// A periodic export is a new snapshot of a subset of conversations, not a
// replacement of the account's earlier exports. Exercise collection, capture,
// indexing and a rebuild from the retained evidence in arrival order.
func TestClaudeImportFullThenOverlappingPartialExports(t *testing.T) {
	config := claudeImportConfig(t)
	c, _ := testCatalog(t)
	var full []map[string]any
	if err := json.Unmarshal([]byte(claudeAIExport), &full); err != nil {
		t.Fatal(err)
	}
	var changed []map[string]any
	if err := json.Unmarshal([]byte(claudeAIExport), &changed); err != nil {
		t.Fatal(err)
	}
	message := func(id, sender, text, parent, at string) map[string]any {
		return map[string]any{"uuid": id, "sender": sender, "text": text, "parent_message_uuid": parent, "created_at": at, "content": []any{map[string]any{"type": "text", "text": text}}}
	}
	changed[0]["chat_messages"] = append(changed[0]["chat_messages"].([]any),
		message("periodic-user", "human", "One more change", "m4", "2026-10-05T10:00:00Z"),
		message("periodic-reply", "assistant", "Latest periodic reply", "periodic-user", "2026-10-05T10:01:00Z"))
	changed[0]["updated_at"], changed[0]["current_leaf_message_uuid"] = "2026-10-05T10:01:00Z", "periodic-reply"
	newConversation := map[string]any{"uuid": "periodic-conversation", "name": "New since baseline", "created_at": "2026-10-05T12:00:00Z", "updated_at": "2026-10-05T12:01:00Z", "current_leaf_message_uuid": "periodic-new-reply", "chat_messages": []any{
		message("periodic-new-user", "human", "A new conversation", claudeAIRoot, "2026-10-05T12:00:00Z"),
		message("periodic-new-reply", "assistant", "A new reply", "periodic-new-user", "2026-10-05T12:01:00Z"),
	}}
	collect := func(at string, conversations []map[string]any) *claudeImportJob {
		t.Helper()
		dir := t.TempDir()
		zip := filepath.Join(dir, "conversations-000.zip")
		chatGPTZip(t, zip, map[string]string{"conversations.json": jsonText(conversations)})
		exported, err := time.Parse(time.RFC3339, at)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(zip, exported.Add(time.Minute), exported.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		manifest := filepath.Join(dir, "manifest.json")
		writeSourceFile(t, manifest, jsonText(claudeManifest{Version: "1.0", CreatedAt: at, DataFiles: []claudeManifestPart{{Filename: "conversations-000.zip", Category: "conversations"}}}))
		job, err := collectClaudeExport(t.Context(), config, claudeImportRequest{Paths: []string{manifest}})
		if err != nil || job.State != "collected" {
			t.Fatal(job, err)
		}
		config, err = LoadConfig(config.Path)
		if err != nil {
			t.Fatal(err)
		}
		if len(config.Sources) != 1 {
			t.Fatal("periodic exports created extra sources", config.Sources)
		}
		if result := runCapture(t, config); !result.OK {
			t.Fatal(result)
		}
		indexCaptures(t, c, config.CaptureRoot, "host-a")
		return job
	}
	check := func(c *Catalog, conversations, messages int) {
		t.Helper()
		if countRows(t, c, "SELECT COUNT(*) FROM conversations") != conversations || countRows(t, c, "SELECT COUNT(*) FROM messages") != messages {
			t.Fatalf("want %d conversations / %d messages, got %d / %d", conversations, messages, countRows(t, c, "SELECT COUNT(*) FROM conversations"), countRows(t, c, "SELECT COUNT(*) FROM messages"))
		}
	}
	collect("2026-10-03T00:00:00Z", full)
	check(c, 2, 15)
	// This export omits conv-2 completely, updates conv-1, and adds one chat.
	delta := []map[string]any{changed[0], newConversation}
	first := collect("2026-10-06T00:00:00Z", delta)
	check(c, 3, 19)
	// Overlap is deduplicated in the catalog; raw snapshots currently retain
	// even byte-identical exports under their separate manifest identities.
	repeated := collect("2026-10-08T00:00:00Z", delta)
	check(c, 3, 19)
	if first.ID == repeated.ID || first.Parts[0].SHA256 != repeated.Parts[0].SHA256 {
		t.Fatal("fixture should contain distinct snapshots of identical conversation bytes")
	}
	for _, job := range []*claudeImportJob{first, repeated} {
		if _, err := os.Stat(filepath.Join(claudeImportJobDir(config, job.Name, job.ID), filepath.FromSlash(job.Parts[0].Storage))); err != nil {
			t.Fatal("retained snapshot missing", err)
		}
	}
	// A delayed older partial snapshot cannot roll the latest reply back.
	collect("2026-10-04T00:00:00Z", full[:1])
	check(c, 3, 19)
	if countRows(t, c, "SELECT COUNT(*) FROM messages WHERE text='Latest periodic reply' AND selected=1") != 1 {
		t.Fatal("late export lost or duplicated the latest reply")
	}
	if err := os.RemoveAll(claudeImportRoot(config, "claude-ai-imports")); err != nil {
		t.Fatal(err)
	}
	useHost(t, "host-b")
	rebuilt, _ := testCatalog(t)
	indexCaptures(t, rebuilt, config.CaptureRoot, "host-a")
	check(rebuilt, 3, 19)
}
