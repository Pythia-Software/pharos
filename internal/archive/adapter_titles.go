package archive

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type transcriptName struct {
	id, title, evidence, rollout string
	custom                       bool
	observed                     int64
}

func (n transcriptName) signal() string {
	return hashBytes([]byte(jsonText([]any{n.id, n.title, n.custom})))[:16]
}

var codexSessionFilename = regexp.MustCompile(`([0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12})\.jsonl$`)

func codexStateName(name string) bool {
	if name == "state.sqlite" {
		return true
	}
	if !strings.HasPrefix(name, "state_") || !strings.HasSuffix(name, ".sqlite") {
		return false
	}
	version, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "state_"), ".sqlite"))
	return err == nil && version >= 0
}

func codexStateVersion(path string) int {
	name := filepath.Base(path)
	version, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "state_"), ".sqlite"))
	return version
}

// Read only known metadata stores under the configured source root. Optional
// older databases without threads.name do not turn prompt previews into names.
func codexNamingDatabases(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	files := []string{}
	for _, entry := range entries {
		if entry.IsDir() || !codexStateName(entry.Name()) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		db, err := openReadOnlySQLite(path)
		if err != nil {
			return nil, err
		}
		columns, err := tableColumns(db, "threads")
		db.Close()
		if err != nil {
			return nil, err
		}
		if columns["id"] && columns["name"] {
			files = append(files, path)
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if codexStateVersion(files[i]) != codexStateVersion(files[j]) {
			return codexStateVersion(files[i]) > codexStateVersion(files[j])
		}
		return files[i] < files[j]
	})
	return files, nil
}

func (a *jsonlAdapter) codexNames() (map[string]transcriptName, error) {
	names := map[string]transcriptName{}
	if info, err := os.Stat(a.config.Path); err != nil || !info.IsDir() {
		return names, err
	}
	index := filepath.Join(a.config.Path, "session_index.jsonl")
	if info, err := a.files().Stat(index); err == nil && info.Mode().IsRegular() {
		file, err := a.files().Open(index)
		if err != nil {
			return nil, err
		}
		rows, err := readJSONLines(file, info.Size())
		file.Close()
		if err != nil {
			return nil, fmt.Errorf("read Codex session names: %w", err)
		}
		updated := map[string]string{}
		for _, row := range rows {
			id := firstString(row["id"])
			stamp := iso(row["updated_at"])
			if id != "" && stamp >= updated[id] {
				updated[id] = stamp
				names[id] = transcriptName{id: id, title: firstString(row["thread_name"]), custom: true, observed: info.ModTime().UnixNano(), evidence: a.original(index)}
			}
		}
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	databases, err := codexNamingDatabases(a.config.Path)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, path := range databases {
		db, err := openReadOnlySQLite(path)
		if err != nil {
			return nil, err
		}
		tx, err := db.BeginTx(a.context(), nil)
		if err != nil {
			db.Close()
			return nil, err
		}
		version := a.databaseVersion(path)
		columns, err := tableColumns(tx, "threads")
		if err != nil {
			tx.Rollback()
			db.Close()
			return nil, err
		}
		query := "SELECT id,name FROM threads"
		if columns["rollout_path"] {
			query = "SELECT id,name,rollout_path FROM threads"
		}
		rows, err := queryMapsContext(a.context(), tx, query)
		tx.Rollback()
		db.Close()
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			id := firstString(row["id"])
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			// The newest schema's state is authoritative, including a cleared
			// name. An old append-only index must not resurrect a removed name.
			names[id] = transcriptName{id: id, title: firstString(row["name"]), rollout: firstString(row["rollout_path"]), custom: true, observed: version, evidence: a.original(path)}
		}
	}
	return names, nil
}

func (a *jsonlAdapter) transcriptNames(roots []string) (map[string]transcriptName, error) {
	result := map[string]transcriptName{}
	if a.provider == "codex" {
		names, err := a.codexNames()
		if err != nil {
			return nil, err
		}
		if len(names) == 0 {
			return result, nil
		}
		paths := map[string]transcriptName{}
		ambiguous := map[string]bool{}
		for _, name := range names {
			if name.rollout != "" {
				path := filepath.Clean(name.rollout)
				if previous, exists := paths[path]; exists && previous.id != name.id {
					ambiguous[path] = true
					delete(paths, path)
				}
				if !ambiguous[path] {
					paths[path] = name
				}
			}
		}
		// Prefer recorded rollout paths, so unchanged files need no header
		// reads. Only index candidates need a small session_meta read. Its ID
		// is verified again by the body parser before applying the title.
		for _, path := range roots {
			if name, exists := paths[filepath.Clean(a.original(path))]; exists {
				result[path] = name
				continue
			}
			if match := codexSessionFilename.FindStringSubmatch(filepath.Base(path)); len(match) == 2 {
				if _, exists := names[match[1]]; !exists {
					continue
				}
			}
			id, err := a.files().codexRolloutIdentity(path)
			if err != nil {
				return nil, err
			}
			if name, ok := names[id]; ok {
				result[path] = name
			}
		}
		return result, nil
	}
	byDir := map[string]map[string]transcriptName{}
	for _, path := range roots {
		dir := filepath.Dir(path)
		names, seen := byDir[dir]
		if !seen {
			names = map[string]transcriptName{}
			index := filepath.Join(dir, "sessions-index.json")
			raw, version, err := a.files().ReadJSONVersion(index)
			// The index is optional. A partial write or unreadable index must
			// not prevent this directory (or its siblings) using transcripts.
			// Its next successful read supplies a new incremental name signal.
			if err == nil {
				for _, row := range mapSlice(mapValue(raw)["entries"]) {
					custom := firstString(row["customTitle"])
					title := firstString(custom, row["summary"])
					if id := firstString(row["sessionId"]); id != "" && title != "" {
						names[id] = transcriptName{id: id, title: title, custom: custom != "", observed: version, evidence: a.original(index)}
					}
				}
			}
			byDir[dir] = names
		}
		if name, ok := names[strings.TrimSuffix(filepath.Base(path), ".jsonl")]; ok {
			result[path] = name
		}
	}
	return result, nil
}

func codexRolloutIdentity(path string) (string, error) {
	return sourceFiles{}.codexRolloutIdentity(path)
}

// codexRolloutIdentity reads only the rollout's header: for an encoded
// rollout, the decoder stops after its first block.
func (s sourceFiles) codexRolloutIdentity(path string) (string, error) {
	// Cached while the rollout is unchanged (see cachedHead).
	id, err := s.cachedHead("codex-rollout-identity", path, func() (any, error) { return s.readCodexRolloutIdentity(path) })
	if err != nil {
		return "", err
	}
	return id.(string), nil
}

func (s sourceFiles) readCodexRolloutIdentity(path string) (string, error) {
	fallback := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	file, err := s.Open(path)
	if os.IsNotExist(err) {
		return fallback, nil
	}
	if err != nil {
		return "", err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	// session_meta is the rollout header. Do not scan conversation bodies to
	// obtain a name dependency when deciding whether the body is unchanged.
	for range 4 {
		line, err := reader.ReadBytes('\n')
		var event struct {
			Type    string `json:"type"`
			Payload struct {
				ID string `json:"id"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &event) == nil && event.Type == "session_meta" {
			id := defaultString(event.Payload.ID, fallback)
			if match := codexForkFilename.FindStringSubmatch(filepath.Base(path)); len(match) == 3 && id == match[1] {
				id = match[2]
			}
			return id, nil
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return fallback, nil
}
