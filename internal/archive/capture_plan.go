package archive

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type captureItem struct {
	path string // original, absolute
	rel  string // slash-separated, relative to files/
}

type capturePlan struct {
	files, databases []captureItem
	projections      []captureProjection
	retainAbsent     bool
	// skipped exist but are not captured (databases without conversations),
	// so they are not missing either.
	skipped []string
	// complete is false when part of the source could not be listed; nothing
	// may then be marked missing.
	complete      bool
	warnings      []captureError
	installations []capturedInstallation
	pathMap       []capturePathMapping
}

// preferRollouts drops the form of a rollout listed both plain and
// compressed that the adapter does not read (preferRollout).
func preferRollouts(items []captureItem) []captureItem {
	listed := map[string]bool{}
	for _, item := range items {
		listed[item.path] = true
	}
	kept := items[:0:0]
	for _, item := range items {
		plain := plainRollout(item.path)
		if listed[plain] && listed[plain+".zst"] && rolloutFile(plain) != item.path {
			continue
		}
		kept = append(kept, item)
	}
	return kept
}

// planCapture lists exactly what the source's adapter would read
// (providerSpec.Plan).
func planCapture(ctx context.Context, source SourceConfig) (capturePlan, error) {
	plan := capturePlan{complete: true}
	spec, known := providerFor(source.Kind)
	if known && spec.Resolve != nil {
		source = spec.Resolve(source)
	}
	info, err := os.Stat(source.Path)
	if err != nil {
		return plan, fmt.Errorf("source path is unavailable: %w", err)
	}
	rootRel := captureFilesDir
	if !info.IsDir() {
		rootRel = path.Join(captureFilesDir, filepath.Base(source.Path))
	}
	plan.pathMap = []capturePathMapping{{Original: source.Path, Captured: rootRel}}
	if !known {
		return plan, fmt.Errorf("unsupported source kind: %s", source.Kind)
	}
	err = spec.Plan(&plan, ctx, source, info)
	return plan, err
}

// claude plans a Claude Code source's capture.
func (p *capturePlan) claude(ctx context.Context, source SourceConfig, info fs.FileInfo) error {
	jsonl := func(file string) bool { return strings.HasSuffix(strings.ToLower(file), ".jsonl") }
	p.files = p.walk(source.Path, info, "", func(file string) bool {
		return jsonl(file) || filepath.Base(file) == "sessions-index.json"
	})
	if !info.IsDir() {
		index := filepath.Join(filepath.Dir(source.Path), "sessions-index.json")
		if stat, e := os.Stat(index); e == nil && stat.Mode().IsRegular() {
			p.files = append(p.files, captureItem{index, "sessions-index.json"})
			p.pathMap = append(p.pathMap, capturePathMapping{Original: filepath.Dir(source.Path), Captured: captureFilesDir})
		} else if e != nil && !os.IsNotExist(e) {
			return e
		}
	}
	return nil
}

// codex plans a Codex source's capture.
func (p *capturePlan) codex(ctx context.Context, source SourceConfig, info fs.FileInfo) error {
	adapter := jsonlAdapter{baseAdapter: baseAdapter{config: source}, provider: "codex"}
	p.files = p.walk(source.Path, info, "", func(file string) bool {
		return adapter.transcriptName(file) && adapter.accept(file) || info.IsDir() && file == filepath.Join(source.Path, "session_index.jsonl")
	})
	p.files = preferRollouts(p.files)
	if info.IsDir() {
		databases, err := codexNamingDatabases(sourceFiles{ctx: ctx}, source.Path)
		if err != nil {
			return fmt.Errorf("Codex names are unavailable: %w", err)
		}
		for _, database := range databases {
			p.databases = append(p.databases, captureItem{database, filepath.Base(database)})
		}
	}
	return nil
}

// canonical plans a canonical export source's capture.
func (p *capturePlan) canonical(ctx context.Context, source SourceConfig, info fs.FileInfo) error {
	p.files = p.walk(source.Path, info, "", func(file string) bool {
		return !info.IsDir() || strings.HasSuffix(strings.ToLower(file), ".json")
	})
	return nil
}

// chatGPT plans a ChatGPT export source's capture.
func (p *capturePlan) chatGPT(ctx context.Context, source SourceConfig, info fs.FileInfo) error {
	// The conversation files, or the ZIP whole; the export's account
	// files beside them are not read.
	files, err := (&chatGPTAdapter{baseAdapter{config: source}}).exportFiles()
	if err != nil {
		return fmt.Errorf("ChatGPT export is unavailable: %w", err)
	}
	for _, file := range files {
		if _, err := os.Stat(file); err != nil {
			return fmt.Errorf("ChatGPT export is unavailable: %w", err)
		}
		rel, _ := filepath.Rel(source.Path, file)
		if !info.IsDir() {
			rel = filepath.Base(file)
		}
		p.files = append(p.files, captureItem{file, filepath.ToSlash(rel)})
	}
	return nil
}

// claudeAI plans a Claude.ai export source's capture.
func (p *capturePlan) claudeAI(ctx context.Context, source SourceConfig, info fs.FileInfo) error {
	inputs, err := claudeExportInputs(sourceFiles{ctx: ctx}, source.Path)
	if err != nil {
		return fmt.Errorf("Claude.ai export is unavailable: %w", err)
	}
	for _, input := range inputs {
		rel, _ := filepath.Rel(source.Path, input.path)
		if !info.IsDir() {
			rel = filepath.Base(input.path)
		}
		p.files = append(p.files, captureItem{input.path, filepath.ToSlash(rel)})
	}
	return nil
}

// conductor plans a Conductor source's capture.
func (p *capturePlan) conductor(ctx context.Context, source SourceConfig, info fs.FileInfo) error {
	// The Conductor adapter's candidates: every SQLite file, of which it
	// reads those holding sessions and messages (not, say, cache.db).
	candidates := p.walk(source.Path, info, "", func(file string) bool {
		if !info.IsDir() {
			return true
		}
		name := strings.ToLower(filepath.Base(file))
		return (strings.HasSuffix(name, ".db") || strings.Contains(name, ".sqlite")) &&
			!strings.HasSuffix(name, "-wal") && !strings.HasSuffix(name, "-shm") && !strings.HasSuffix(name, "-journal")
	})
	for _, candidate := range candidates {
		if ok, err := conductorConversationDatabase(ctx, candidate.path); err != nil {
			p.warnings = append(p.warnings, captureError{Path: candidate.path, Error: err.Error()})
			p.skipped = append(p.skipped, candidate.rel)
		} else if ok {
			p.databases = append(p.databases, candidate)
		} else {
			p.skipped = append(p.skipped, candidate.rel)
		}
	}
	return nil
}

// copilot plans a Copilot CLI source's capture.
func (p *capturePlan) copilot(ctx context.Context, source SourceConfig, info fs.FileInfo) error {
	files, err := (&copilotAdapter{baseAdapter{config: source}}).captureFiles()
	if err != nil {
		return fmt.Errorf("Copilot sessions are unavailable: %w", err)
	}
	for _, file := range files {
		rel, _ := filepath.Rel(source.Path, file)
		p.files = append(p.files, captureItem{file, filepath.ToSlash(rel)})
	}
	return nil
}

// cline plans a Cline source's capture.
func (p *capturePlan) cline(ctx context.Context, source SourceConfig, info fs.FileInfo) error {
	// The sessions' manifests and messages: not the provider settings,
	// which hold credentials, nor Cline's index or checkpoints.
	files, err := (&clineAdapter{baseAdapter{config: source}}).captureFiles()
	if err != nil {
		return fmt.Errorf("Cline sessions are unavailable: %w", err)
	}
	for _, file := range files {
		rel, _ := filepath.Rel(source.Path, file)
		p.files = append(p.files, captureItem{file, filepath.ToSlash(rel)})
	}
	return nil
}

// vscodeCopilot plans a VS Code Copilot source's capture.
func (p *capturePlan) vscodeCopilot(ctx context.Context, source SourceConfig, info fs.FileInfo) error {
	// The chat sessions and the workspace.json naming each one's folder:
	// not the rest of the user folder, which holds settings, extension
	// state and other extensions' secrets.
	files, err := (&vscodeCopilotAdapter{baseAdapter{config: source}}).captureFiles()
	if err != nil {
		return fmt.Errorf("VS Code chat sessions are unavailable: %w", err)
	}
	for _, file := range files {
		rel, _ := filepath.Rel(source.Path, file)
		p.files = append(p.files, captureItem{file, filepath.ToSlash(rel)})
	}
	return nil
}

// cursor plans a Cursor source's capture.
func (p *capturePlan) cursor(ctx context.Context, source SourceConfig, info fs.FileInfo) error {
	// The global database and each window's, without their other rows
	// (captureTables), and the workspace.json naming a window's folder:
	// not the rest of the user folder, which holds settings and other
	// extensions' state.
	databases, files, err := (&cursorAdapter{baseAdapter: baseAdapter{config: source}}).captureInputs()
	if err != nil {
		return fmt.Errorf("Cursor chats are unavailable: %w", err)
	}
	for _, database := range databases {
		rel, _ := filepath.Rel(source.Path, database)
		if !info.IsDir() {
			rel = filepath.Base(source.Path)
		}
		p.databases = append(p.databases, captureItem{database, filepath.ToSlash(rel)})
	}
	for _, file := range files {
		rel, _ := filepath.Rel(source.Path, file)
		p.files = append(p.files, captureItem{file, filepath.ToSlash(rel)})
	}
	return nil
}

// cursorCLI plans a Cursor CLI source's capture.
func (p *capturePlan) cursorCLI(ctx context.Context, source SourceConfig, info fs.FileInfo) error {
	// Each chat's database, without its encryption key (captureTables),
	// and its meta.json: not the rest of ~/.cursor, which holds the
	// CLI's sign-in and settings.
	databases, files, err := (&cursorCLIAdapter{baseAdapter: baseAdapter{config: source}}).captureInputs()
	if err != nil && databases == nil {
		return fmt.Errorf("Cursor CLI chats are unavailable: %w", err)
	} else if err != nil {
		// A folder of chats that cannot be listed leaves the others.
		p.complete = false
		p.warnings = append(p.warnings, captureError{Path: filepath.Join(source.Path, "chats"), Error: err.Error()})
	}
	for _, database := range databases {
		rel, _ := filepath.Rel(source.Path, database)
		p.databases = append(p.databases, captureItem{database, filepath.ToSlash(rel)})
	}
	for _, file := range files {
		rel, _ := filepath.Rel(source.Path, file)
		p.files = append(p.files, captureItem{file, filepath.ToSlash(rel)})
	}
	return nil
}

// opencode plans an OpenCode source's capture.
func (p *capturePlan) opencode(ctx context.Context, source SourceConfig, info fs.FileInfo) error {
	// Each database, without the tables that hold credentials and
	// settings (captureTables).
	databases, err := (&opencodeAdapter{baseAdapter: baseAdapter{config: source}}).databases()
	if err != nil {
		return fmt.Errorf("OpenCode sessions are unavailable: %w", err)
	}
	for _, database := range databases {
		rel := filepath.Base(database)
		if !info.IsDir() {
			rel = filepath.Base(source.Path)
		}
		p.databases = append(p.databases, captureItem{database, rel})
	}
	return nil
}

// zed plans a Zed source's capture.
func (p *capturePlan) zed(ctx context.Context, source SourceConfig, info fs.FileInfo) error {
	// The threads database, without tables a later release adds
	// (captureTables): not the rest of Zed's data folder, which holds
	// its settings, extensions and other databases.
	database, err := (&zedAdapter{baseAdapter: baseAdapter{config: source}}).database()
	if err != nil {
		return fmt.Errorf("Zed threads are unavailable: %w", err)
	}
	if database != "" {
		rel := zedDatabase
		if !info.IsDir() {
			rel = filepath.Base(source.Path)
		}
		p.databases = append(p.databases, captureItem{database, rel})
	}
	return nil
}

// antigravity plans an Antigravity source's capture.
func (p *capturePlan) antigravity(ctx context.Context, source SourceConfig, info fs.FileInfo) error {
	adapter := antigravityAdapter{baseAdapter: baseAdapter{config: source}}
	files, err := adapter.captureFiles()
	if err != nil {
		return fmt.Errorf("Antigravity conversations are unavailable: %w", err)
	}
	for _, file := range files {
		rel, _ := filepath.Rel(source.Path, file)
		p.files = append(p.files, captureItem{file, filepath.ToSlash(rel)})
	}
	databases, err := adapter.databases()
	if err != nil {
		return fmt.Errorf("Antigravity conversations are unavailable: %w", err)
	}
	for _, database := range databases {
		rel, _ := filepath.Rel(source.Path, database)
		p.databases = append(p.databases, captureItem{database, filepath.ToSlash(rel)})
	}
	return nil
}

func (p *capturePlan) walk(root string, info fs.FileInfo, prefix string, include func(string) bool) []captureItem {
	if !info.IsDir() {
		if include(root) {
			return []captureItem{{root, path.Join(prefix, filepath.Base(root))}}
		}
		return nil
	}
	items := []captureItem{}
	_ = filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			p.complete = false
			p.warnings = append(p.warnings, captureError{Path: file, Error: err.Error()})
			return nil
		}
		if entry.IsDir() || !include(file) {
			return nil
		}
		rel, _ := filepath.Rel(root, file)
		items = append(items, captureItem{file, path.Join(prefix, filepath.ToSlash(rel))})
		return nil
	})
	return items
}

func conductorConversationDatabase(ctx context.Context, file string) (bool, error) {
	db, err := openSourceSQLite(ctx, file)
	if err != nil {
		return false, err
	}
	defer db.Close()
	tables, err := tableNames(db)
	if err != nil {
		return false, err
	}
	return firstPresent(tables, "sessions", "workspaces", "threads") != "" && firstPresent(tables, "messages", "session_messages") != "", nil
}

// tl1 captures the registry, and for each installation the TL1 adapter would
// read, a snapshot of its database and its transcripts and script logs. The
// registry keeps absolute paths, so the manifest maps each to its capture.
func (p *capturePlan) tl1(source SourceConfig) error {
	registry := filepath.Base(source.Path)
	p.files = []captureItem{{source.Path, registry}}
	installations, err := (&tl1Adapter{baseAdapter: baseAdapter{config: source}}).installations()
	if err != nil {
		return err
	}
	databases, transcripts, keys := map[string]string{}, map[string]string{}, map[string]bool{}
	for _, installation := range installations {
		key := installation.ID
		if !validCaptureName(key) {
			key = "installation-" + hashBytes([]byte(installation.ID))[:16]
		}
		if keys[key] {
			key += "-" + hashBytes([]byte(installation.Database))[:8]
		}
		keys[key] = true
		record := capturedInstallation{ID: installation.ID, Project: installation.Project, DBPath: installation.Database, CodeRepo: installation.Repository, TranscriptsDir: installation.Transcripts, ConfigPath: installation.ConfigPath}
		rel, seen := databases[installation.Database]
		if !seen {
			rel = path.Join("installations", key, filepath.Base(installation.Database))
			databases[installation.Database] = rel
			p.databases = append(p.databases, captureItem{installation.Database, rel})
			p.pathMap = append(p.pathMap, capturePathMapping{Original: installation.Database, Captured: path.Join(captureFilesDir, rel)})
		}
		record.CapturedDB = path.Join(captureFilesDir, rel)
		if installation.Transcripts != "" {
			rel, seen := transcripts[installation.Transcripts]
			if !seen {
				rel = path.Join("installations", key, "transcripts")
				transcripts[installation.Transcripts] = rel
				if info, err := os.Stat(installation.Transcripts); err == nil && info.IsDir() {
					p.files = append(p.files, p.walk(installation.Transcripts, info, rel, func(file string) bool {
						return tl1CapturedFile(filepath.Base(file))
					})...)
				}
				p.pathMap = append(p.pathMap, capturePathMapping{Original: installation.Transcripts, Captured: path.Join(captureFilesDir, rel)})
			}
			record.CapturedTranscripts = path.Join(captureFilesDir, rel)
		}
		p.installations = append(p.installations, record)
	}
	return nil
}

// A previously captured conversation DB stays in scope after a schema change
// removes its session/message tables. Unrelated cache/auth databases remain
// outside the established Conductor capture boundary.
func includeKnownCaptureDatabases(plan *capturePlan, manifest *captureManifest, source SourceConfig) {
	for _, rel := range plan.skipped {
		for _, snapshot := range manifest.Snapshots {
			if snapshot.Captured != path.Join(captureFilesDir, rel) {
				continue
			}
			expected := filepath.Join(source.Path, filepath.FromSlash(rel))
			if info, e := os.Stat(source.Path); e == nil && !info.IsDir() {
				expected = source.Path
			}
			if snapshot.Path == expected {
				plan.databases = append(plan.databases, captureItem{snapshot.Path, rel})
			}
		}
	}
}
