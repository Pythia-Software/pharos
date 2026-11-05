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

// planCapture lists exactly what the source's adapter would read.
func planCapture(source SourceConfig) (capturePlan, error) {
	plan := capturePlan{complete: true}
	if strings.EqualFold(source.Kind, "codex") {
		source = resolveRolloutSource(source)
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
	jsonl := func(file string) bool { return strings.HasSuffix(strings.ToLower(file), ".jsonl") }
	switch strings.ToLower(source.Kind) {
	case "claude":
		plan.files = plan.walk(source.Path, info, "", func(file string) bool {
			return jsonl(file) || filepath.Base(file) == "sessions-index.json"
		})
		if !info.IsDir() {
			index := filepath.Join(filepath.Dir(source.Path), "sessions-index.json")
			if stat, e := os.Stat(index); e == nil && stat.Mode().IsRegular() {
				plan.files = append(plan.files, captureItem{index, "sessions-index.json"})
				plan.pathMap = append(plan.pathMap, capturePathMapping{Original: filepath.Dir(source.Path), Captured: captureFilesDir})
			} else if e != nil && !os.IsNotExist(e) {
				return plan, e
			}
		}
	case "codex":
		adapter := jsonlAdapter{baseAdapter: baseAdapter{config: source}, provider: "codex"}
		plan.files = plan.walk(source.Path, info, "", func(file string) bool {
			return adapter.transcriptName(file) && adapter.accept(file) || info.IsDir() && file == filepath.Join(source.Path, "session_index.jsonl")
		})
		plan.files = preferRollouts(plan.files)
		if info.IsDir() {
			databases, err := codexNamingDatabases(context.Background(), source.Path)
			if err != nil {
				return plan, fmt.Errorf("Codex names are unavailable: %w", err)
			}
			for _, database := range databases {
				plan.databases = append(plan.databases, captureItem{database, filepath.Base(database)})
			}
		}
	case "canonical", "tl1-export":
		plan.files = plan.walk(source.Path, info, "", func(file string) bool {
			return !info.IsDir() || strings.HasSuffix(strings.ToLower(file), ".json")
		})
	case "chatgpt", "chatgpt-export":
		// The conversation files, or the ZIP whole; the export's account
		// files beside them are not read.
		files, err := (&chatGPTAdapter{baseAdapter{config: source}}).exportFiles()
		if err != nil {
			return plan, fmt.Errorf("ChatGPT export is unavailable: %w", err)
		}
		for _, file := range files {
			if _, err := os.Stat(file); err != nil {
				return plan, fmt.Errorf("ChatGPT export is unavailable: %w", err)
			}
			rel, _ := filepath.Rel(source.Path, file)
			if !info.IsDir() {
				rel = filepath.Base(file)
			}
			plan.files = append(plan.files, captureItem{file, filepath.ToSlash(rel)})
		}
	case "claude-ai":
		file := (&claudeAIAdapter{baseAdapter{config: source}}).exportFile()
		if _, err := os.Stat(file); err != nil {
			return plan, fmt.Errorf("Claude.ai export is unavailable: %w", err)
		}
		rel, _ := filepath.Rel(source.Path, file)
		if !info.IsDir() {
			rel = filepath.Base(file)
		}
		plan.files = []captureItem{{file, filepath.ToSlash(rel)}}
	case "conductor":
		// The Conductor adapter's candidates: every SQLite file, of which it
		// reads those holding sessions and messages (not, say, cache.db).
		candidates := plan.walk(source.Path, info, "", func(file string) bool {
			if !info.IsDir() {
				return true
			}
			name := strings.ToLower(filepath.Base(file))
			return (strings.HasSuffix(name, ".db") || strings.Contains(name, ".sqlite")) &&
				!strings.HasSuffix(name, "-wal") && !strings.HasSuffix(name, "-shm") && !strings.HasSuffix(name, "-journal")
		})
		for _, candidate := range candidates {
			if ok, err := conductorConversationDatabase(candidate.path); err != nil {
				plan.warnings = append(plan.warnings, captureError{Path: candidate.path, Error: err.Error()})
				plan.skipped = append(plan.skipped, candidate.rel)
			} else if ok {
				plan.databases = append(plan.databases, candidate)
			} else {
				plan.skipped = append(plan.skipped, candidate.rel)
			}
		}
	case "tl1":
		return plan, plan.tl1(source)
	case "copilot":
		files, err := (&copilotAdapter{baseAdapter{config: source}}).captureFiles()
		if err != nil {
			return plan, fmt.Errorf("Copilot sessions are unavailable: %w", err)
		}
		for _, file := range files {
			rel, _ := filepath.Rel(source.Path, file)
			plan.files = append(plan.files, captureItem{file, filepath.ToSlash(rel)})
		}
	case "cline":
		// The sessions' manifests and messages: not the provider settings,
		// which hold credentials, nor Cline's index or checkpoints.
		files, err := (&clineAdapter{baseAdapter{config: source}}).captureFiles()
		if err != nil {
			return plan, fmt.Errorf("Cline sessions are unavailable: %w", err)
		}
		for _, file := range files {
			rel, _ := filepath.Rel(source.Path, file)
			plan.files = append(plan.files, captureItem{file, filepath.ToSlash(rel)})
		}
	case "vscode-copilot":
		// The chat sessions and the workspace.json naming each one's folder:
		// not the rest of the user folder, which holds settings, extension
		// state and other extensions' secrets.
		files, err := (&vscodeCopilotAdapter{baseAdapter{config: source}}).captureFiles()
		if err != nil {
			return plan, fmt.Errorf("VS Code chat sessions are unavailable: %w", err)
		}
		for _, file := range files {
			rel, _ := filepath.Rel(source.Path, file)
			plan.files = append(plan.files, captureItem{file, filepath.ToSlash(rel)})
		}
	case "cursor":
		// The global database and each window's, without their other rows
		// (captureTables), and the workspace.json naming a window's folder:
		// not the rest of the user folder, which holds settings and other
		// extensions' state.
		databases, files, err := (&cursorAdapter{baseAdapter: baseAdapter{config: source}}).captureInputs()
		if err != nil {
			return plan, fmt.Errorf("Cursor chats are unavailable: %w", err)
		}
		for _, database := range databases {
			rel, _ := filepath.Rel(source.Path, database)
			if !info.IsDir() {
				rel = filepath.Base(source.Path)
			}
			plan.databases = append(plan.databases, captureItem{database, filepath.ToSlash(rel)})
		}
		for _, file := range files {
			rel, _ := filepath.Rel(source.Path, file)
			plan.files = append(plan.files, captureItem{file, filepath.ToSlash(rel)})
		}
	case "cursor-cli":
		// Each chat's database, without its encryption key (captureTables),
		// and its meta.json: not the rest of ~/.cursor, which holds the
		// CLI's sign-in and settings.
		databases, files, err := (&cursorCLIAdapter{baseAdapter: baseAdapter{config: source}}).captureInputs()
		if err != nil && databases == nil {
			return plan, fmt.Errorf("Cursor CLI chats are unavailable: %w", err)
		} else if err != nil {
			// A folder of chats that cannot be listed leaves the others.
			plan.complete = false
			plan.warnings = append(plan.warnings, captureError{Path: filepath.Join(source.Path, "chats"), Error: err.Error()})
		}
		for _, database := range databases {
			rel, _ := filepath.Rel(source.Path, database)
			plan.databases = append(plan.databases, captureItem{database, filepath.ToSlash(rel)})
		}
		for _, file := range files {
			rel, _ := filepath.Rel(source.Path, file)
			plan.files = append(plan.files, captureItem{file, filepath.ToSlash(rel)})
		}
	case "opencode":
		// Each database, without the tables that hold credentials and
		// settings (captureTables).
		databases, err := (&opencodeAdapter{baseAdapter: baseAdapter{config: source}}).databases()
		if err != nil {
			return plan, fmt.Errorf("OpenCode sessions are unavailable: %w", err)
		}
		for _, database := range databases {
			rel := filepath.Base(database)
			if !info.IsDir() {
				rel = filepath.Base(source.Path)
			}
			plan.databases = append(plan.databases, captureItem{database, rel})
		}
	case "zed":
		// The threads database, without tables a later release adds
		// (captureTables): not the rest of Zed's data folder, which holds
		// its settings, extensions and other databases.
		database, err := (&zedAdapter{baseAdapter: baseAdapter{config: source}}).database()
		if err != nil {
			return plan, fmt.Errorf("Zed threads are unavailable: %w", err)
		}
		if database != "" {
			rel := zedDatabase
			if !info.IsDir() {
				rel = filepath.Base(source.Path)
			}
			plan.databases = append(plan.databases, captureItem{database, rel})
		}
	case "antigravity":
		adapter := antigravityAdapter{baseAdapter: baseAdapter{config: source}}
		files, err := adapter.captureFiles()
		if err != nil {
			return plan, fmt.Errorf("Antigravity conversations are unavailable: %w", err)
		}
		for _, file := range files {
			rel, _ := filepath.Rel(source.Path, file)
			plan.files = append(plan.files, captureItem{file, filepath.ToSlash(rel)})
		}
		databases, err := adapter.databases()
		if err != nil {
			return plan, fmt.Errorf("Antigravity conversations are unavailable: %w", err)
		}
		for _, database := range databases {
			rel, _ := filepath.Rel(source.Path, database)
			plan.databases = append(plan.databases, captureItem{database, filepath.ToSlash(rel)})
		}
	default:
		return plan, fmt.Errorf("unsupported source kind: %s", source.Kind)
	}
	return plan, nil
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

func conductorConversationDatabase(file string) (bool, error) {
	db, err := openSourceSQLite(context.Background(), file)
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
