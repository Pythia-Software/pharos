package archive

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// personalAgentAdapter reads only native history, never agent configuration,
// credentials, memory indexes, or live APIs. The two formats have independent
// parsers; discovery and capture share the same bounded inventory.
type personalAgentAdapter struct{ baseAdapter }

const (
	openclawExtractor = "openclaw-v4"
	hermesExtractor   = "hermes-v4"
)

func (a *personalAgentAdapter) indexVersion() string {
	if a.config.Kind == "hermes" {
		return hermesExtractor
	}
	return openclawExtractor
}

type personalAgentInput struct {
	path, namespace string
	database        bool
}

func (a *personalAgentAdapter) inputs() ([]personalAgentInput, error) {
	info, err := a.files().Stat(a.config.Path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		name := filepath.Base(a.config.Path)
		return []personalAgentInput{{a.config.Path, a.personalNamespace(a.config.Path), !strings.Contains(name, ".jsonl")}}, nil
	}
	var inputs []personalAgentInput
	add := func(file string, database bool) error {
		info, err := a.files().Stat(file)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("history input %s is not a regular file", a.original(file))
		}
		inputs = append(inputs, personalAgentInput{file, a.personalNamespace(file), database})
		return nil
	}
	if a.config.Kind == "hermes" {
		if err := add(filepath.Join(a.config.Path, "state.db"), true); err != nil {
			return nil, err
		}
		profiles, err := a.personalDirectories(filepath.Join(a.config.Path, "profiles"))
		if err != nil {
			return nil, err
		}
		for _, profile := range profiles {
			if err := add(filepath.Join(profile, "state.db"), true); err != nil {
				return nil, err
			}
		}
		return inputs, nil
	}
	// A whole state directory, one agent directory, or a legacy sessions
	// directory. Do not recurse into workspaces, plugins, or auth files.
	roots := []string{a.config.Path}
	agents, err := a.personalDirectories(filepath.Join(a.config.Path, "agents"))
	if err != nil {
		return nil, err
	}
	roots = append(roots, agents...)
	for _, root := range roots {
		for _, file := range []string{filepath.Join(root, "openclaw-agent.sqlite"), filepath.Join(root, "agent", "openclaw-agent.sqlite")} {
			if err := add(file, true); err != nil {
				return nil, err
			}
		}
		for _, dir := range []string{root, filepath.Join(root, "sessions")} {
			entries, err := a.files().ReadDir(dir)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			for _, entry := range entries {
				name := entry.Name()
				if !entry.IsDir() && (strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".jsonl.zst") || strings.Contains(name, ".jsonl.reset.") || strings.Contains(name, ".jsonl.deleted.")) {
					if err := add(filepath.Join(dir, name), false); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	sort.Slice(inputs, func(i, j int) bool {
		if inputs[i].database != inputs[j].database {
			return inputs[i].database
		}
		return inputs[i].path < inputs[j].path
	})
	return a.openClawCanonicalInputs(slices.Compact(inputs))
}

// Published reclaimed files are derivatives of canonical database blobs. Keep
// the shared read/capture inventory on that owner so stale files cannot be
// counted twice or copied as inputs the adapter never reads.
func (a *personalAgentAdapter) openClawCanonicalInputs(inputs []personalAgentInput) ([]personalAgentInput, error) {
	derived := map[string]bool{}
	for _, input := range inputs {
		if !input.database {
			continue
		}
		db, tx, tables, err := a.openPersonalDatabase(input.path)
		if err != nil {
			if a.context().Err() != nil {
				return nil, a.context().Err()
			}
			continue // Discovery reports this database without hiding other inputs.
		}
		var rows []map[string]any
		if tables["session_transcript_archives"] {
			rows, err = queryMapsContext(a.context(), tx, "SELECT archive_name FROM session_transcript_archives")
		}
		tx.Rollback()
		db.Close()
		if err != nil {
			if a.context().Err() != nil {
				return nil, a.context().Err()
			}
			continue
		}
		for _, row := range rows {
			derived[input.namespace+"\x00"+firstString(row["archive_name"])] = true
		}
	}
	return slices.DeleteFunc(inputs, func(input personalAgentInput) bool {
		return !input.database && derived[input.namespace+"\x00"+filepath.Base(input.path)]
	}), nil
}

func (a *personalAgentAdapter) personalDirectories(dir string) ([]string, error) {
	entries, err := a.files().ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	dirs := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, filepath.Join(dir, entry.Name()))
		}
	}
	return dirs, nil
}

func (a *personalAgentAdapter) personalNamespace(file string) string {
	parts := strings.Split(filepath.ToSlash(a.original(file)), "/")
	marker, fallback := "agents", "main"
	if a.config.Kind == "hermes" {
		marker, fallback = "profiles", "default"
	}
	for i := len(parts) - 3; i >= 0; i-- {
		if parts[i] == marker {
			return parts[i+1]
		}
	}
	return fallback
}

func personalNativeID(namespace, id string) string { return namespace + ":" + id }

// A parent must be ingested before its children so the catalog can resolve
// native parent references on the first import, regardless of native row order.
func personalParentOrder(records []WorkspaceRecord) ([]WorkspaceRecord, error) {
	byID := map[string]int{}
	for index, record := range records {
		byID[record.SourceID] = index
	}
	state := make([]uint8, len(records))
	ordered := make([]WorkspaceRecord, 0, len(records))
	var visit func(int) error
	visit = func(index int) error {
		if state[index] == 2 {
			return nil
		}
		if state[index] == 1 {
			return fmt.Errorf("cyclic agent parent relation at %s", records[index].SourceID)
		}
		state[index] = 1
		cv := &records[index].Conversations[0]
		if parent, ok := byID[cv.ParentNativeID]; ok {
			if err := visit(parent); err != nil {
				return err
			}
			cv.AgentDepth = records[parent].Conversations[0].AgentDepth + 1
		}
		state[index] = 2
		ordered = append(ordered, records[index])
		return nil
	}
	for index := range records {
		if err := visit(index); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

func (a *personalAgentAdapter) openPersonalDatabase(file string) (*sql.DB, *sql.Tx, map[string]bool, error) {
	db, err := a.files().SQLite(file)
	if err != nil {
		return nil, nil, nil, err
	}
	tx, err := db.BeginTx(a.context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		db.Close()
		return nil, nil, nil, err
	}
	tables, err := tableNames(tx)
	if err != nil {
		tx.Rollback()
		db.Close()
		return nil, nil, nil, err
	}
	return db, tx, tables, nil
}

func (a *personalAgentAdapter) Fingerprint() (string, error) {
	inputs, err := a.inputs()
	if err != nil {
		return "", err
	}
	files, err := a.personalCompanions(inputs)
	if err != nil {
		return "", err
	}
	var signals []string
	for _, input := range inputs {
		version := a.databaseVersion(input.path)
		if !input.database {
			files = append(files, input.path)
			continue
		}
		signals = append(signals, fmt.Sprintf("%s:%d", a.original(input.path), version))
	}
	sort.Strings(files)
	for _, file := range slices.Compact(files) {
		if err := a.context().Err(); err != nil {
			return "", err
		}
		info, err := a.files().Stat(file)
		if err != nil {
			signals = append(signals, "unavailable:"+a.original(file)+":"+err.Error())
			continue // The reader reports the affected transcript, not preflight.
		}
		signals = append(signals, fmt.Sprintf("%s:%d:%d", a.original(file), info.Size(), info.ModTime().UnixNano()))
	}
	return a.indexVersion() + ":" + hashBytes([]byte(strings.Join(signals, "\n"))), nil
}

func (a *personalAgentAdapter) Discover(emit func(WorkspaceRecord) error) error {
	defer a.shareSQLiteCopies()()
	a.ctx = context.WithValue(a.context(), openClawLegacyIndexKey{}, map[string]openClawLegacyIndex{})
	inputs, err := a.inputs()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	archivedFiles := map[string]bool{}
	var discovered []WorkspaceRecord
	var failures []error
	for _, input := range inputs {
		if err := a.context().Err(); err != nil {
			return err
		}
		if !input.database && archivedFiles[input.namespace+"\x00"+filepath.Base(input.path)] {
			continue
		}
		var records []WorkspaceRecord
		if input.database {
			if a.config.Kind == "hermes" {
				records, err = a.readHermes(input)
			} else {
				records, err = a.readOpenClaw(input)
			}
		} else {
			records, err = a.readOpenClawFile(input)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", a.original(input.path), err))
		}
		for _, record := range records {
			if name := firstString(record.Conversations[0].NativeContext["archive_name"]); name != "" {
				archivedFiles[firstString(record.Metadata["agent_profile"])+"\x00"+name] = true
			}
			// SQLite is authoritative over legacy migration inputs with the
			// same native identity. Distinct archived lifecycles remain.
			if seen[record.SourceID] {
				continue
			}
			seen[record.SourceID] = true
			discovered = append(discovered, record)
		}
	}
	if a.config.Kind == "openclaw" {
		openClawCrossAgentParents(discovered)
	}
	ordered, err := personalParentOrder(discovered)
	if err != nil {
		return errors.Join(append(failures, err)...)
	}
	for _, record := range ordered {
		if err := emit(record); err != nil {
			return err
		}
	}
	return errors.Join(failures...)
}

// Routing keys name the parent across agent folders; only a current window
// is a valid target. Historical windows with the same key must not win by
// directory order. Unqualified keys remain local to their agent profile.
func openClawCrossAgentParents(records []WorkspaceRecord) {
	current := map[string]string{}
	keyFor := func(profile, key string) string {
		if strings.HasPrefix(key, "agent:") {
			return key
		}
		return profile + "\x00" + key
	}
	for _, record := range records {
		cv := record.Conversations[0]
		profile := firstString(record.Metadata["agent_profile"])
		key := firstString(cv.NativeContext["session_key"])
		id := firstString(cv.NativeContext["current_session_id"])
		if key != "" && personalNativeID(profile, id) == cv.NativeID {
			lookup := keyFor(profile, key)
			if prior, exists := current[lookup]; exists && prior != cv.NativeID {
				current[lookup] = ""
			} else {
				current[lookup] = cv.NativeID
			}
		}
	}
	for i := range records {
		cv := &records[i].Conversations[0]
		if cv.ParentNativeID != "" {
			continue
		}
		key := firstString(cv.NativeContext["spawned_by"])
		profile := firstString(records[i].Metadata["agent_profile"])
		if parent := current[keyFor(profile, key)]; key != "" && parent != "" && parent != cv.NativeID {
			cv.ParentNativeID, cv.AgentDepth = parent, 1
			for j := range cv.Messages {
				m := &cv.Messages[j]
				if m.Role == "user" {
					m.Role, m.Sender = agentRole, "agent:"+parent
				}
			}
		}
	}
}

func personalJSON(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}
	var result map[string]any
	decoder := json.NewDecoder(strings.NewReader(firstString(value)))
	decoder.UseNumber()
	_ = decoder.Decode(&result)
	return mapValueDefault(result)
}

func (a *personalAgentAdapter) personalRecord(input personalAgentInput, cv ConversationRecord, meta map[string]any, cwd string) WorkspaceRecord {
	var shown []MessageRecord
	for _, message := range cv.Messages {
		if message.Selected {
			shown = append(shown, message)
		}
	}
	purpose, outcome := agentPurposeOutcome(shown)
	title := firstString(meta["label"], meta["displayName"], meta["display_name"], meta["title"], meta["subject"])
	if title == "" {
		spec, _ := providerFor(a.config.Kind)
		title = defaultString(purpose, spec.DisplayName+" "+short(cv.NativeID))
	}
	metadata := map[string]any{"agent_profile": input.namespace}
	for _, key := range []string{"session_key", "channel", "source", "chat_type", "end_reason", "continuation_of", "forked_from", "branch"} {
		if value := meta[key]; value != nil && value != "" {
			metadata[key] = value
		}
	}
	if cost, ok := number(meta["actual_cost_usd"]); ok && cost > 0 {
		metadata["reported_cost_usd"] = cost
	}
	return WorkspaceRecord{SourceID: cv.NativeID, SourceKind: a.config.Kind, Title: title, Account: a.config.Account,
		Purpose: purpose, Outcome: outcome, ActivityAt: latestPersonalTime(cv), Location: cwd,
		Repository: a.repository(cwd, ""), Metadata: metadata, Conversations: []ConversationRecord{cv}}
}

func latestPersonalTime(cv ConversationRecord) string {
	at := cv.StartedAt
	for _, message := range cv.Messages {
		at = max(at, message.CreatedAt)
	}
	return max(at, cv.EndedAt)
}

func personalUsage(base MessageRecord, id string, usage map[string]any) MessageRecord {
	base.NativeID, base.Role, base.Kind, base.RawText = id+":usage", "system", "metadata", ""
	base.Text = jsonText(map[string]any{"type": "token_usage", "usage": usage, "usage_message_id": id})
	return base
}

// personalCompanions lists only the legacy metadata and cold archives the
// parser reads. Missing/unreadable companions are diagnosed by discovery at
// their session, so preflight cannot block unrelated history from updating.
func (a *personalAgentAdapter) personalCompanions(inputs []personalAgentInput) ([]string, error) {
	var files []string
	indexes := map[string]bool{}
	for _, input := range inputs {
		if err := a.context().Err(); err != nil {
			return nil, err
		}
		if a.config.Kind != "openclaw" {
			continue
		}
		if !input.database {
			index := filepath.Join(filepath.Dir(input.path), "sessions.json")
			if indexes[index] {
				continue
			}
			indexes[index] = true
			if _, err := a.files().Stat(index); err == nil {
				files = append(files, index)
			}
			continue
		}
		db, tx, tables, err := a.openPersonalDatabase(input.path)
		if err != nil {
			if a.context().Err() != nil {
				return nil, a.context().Err()
			}
			continue
		}
		paths, err := openClawColdPaths(tx, tables, input.path)
		tx.Rollback()
		db.Close()
		if err != nil {
			if a.context().Err() != nil {
				return nil, a.context().Err()
			}
			continue
		}
		for _, file := range paths {
			if _, err := a.files().Stat(file); err != nil {
				continue
			}
			files = append(files, file)
		}
	}
	sort.Strings(files)
	return slices.Compact(files), nil
}

func (p *capturePlan) personalAgents(ctx context.Context, source SourceConfig, info fs.FileInfo) error {
	if spec, ok := providerFor(source.Kind); ok {
		source.Kind = spec.Kind
	}
	a := &personalAgentAdapter{baseAdapter{config: source, ctx: ctx}}
	inputs, err := a.inputs()
	if err != nil {
		return err
	}
	files, err := a.personalCompanions(inputs)
	if err != nil {
		return err
	}
	root := source.Path
	if !info.IsDir() {
		root = filepath.Dir(root)
		if source.Kind == "openclaw" && filepath.Base(root) == "agent" {
			root = filepath.Dir(root)
		}
		p.pathMap = []capturePathMapping{{Original: root, Captured: captureFilesDir}}
	}
	for _, input := range inputs {
		rel, _ := filepath.Rel(root, input.path)
		item := captureItem{input.path, filepath.ToSlash(rel)}
		if input.database {
			p.databases = append(p.databases, item)
		} else {
			files = append(files, input.path)
		}
	}
	sort.Strings(files)
	for _, file := range slices.Compact(files) {
		rel, _ := filepath.Rel(root, file)
		if !filepath.IsLocal(rel) {
			return fmt.Errorf("history artifact lies outside the selected source: %s; choose its agent folder", file)
		}
		p.files = append(p.files, captureItem{file, filepath.ToSlash(rel)})
	}
	return nil
}

// Keep these tables only: both products share their databases with unrelated
// state, and OpenClaw also stores authentication material there.
func personalAgentKept(kind string) map[string]captureTable {
	names := []string{"sessions", "messages", "session_model_usage"}
	if kind == "openclaw" {
		names = []string{"schema_meta", "session_nodes", "session_windows", "transcript_events", "session_transcript_index_state", "session_transcript_active_events", "session_transcript_archives", "session_transcript_cold_archives", "session_transcript_cold_archive_chunks"}
	}
	keep := map[string]captureTable{}
	for _, name := range names {
		keep[name] = captureTable{}
	}
	return keep
}

// Probe uses sourceFiles too, before the source is configured. Its bounded
// inventory never invokes the agents or reads any authentication files.
func (r *probeRun) personalAgent(kind, dir, foundBy string) {
	candidate, resolved := r.locate(kind, dir, foundBy)
	if candidate == nil {
		return
	}
	candidate.Unit = "session"
	if candidate.Status == "found" {
		a := &personalAgentAdapter{baseAdapter{config: SourceConfig{Kind: kind, Path: resolved}}}
		inputs, err := a.inputs()
		stats := probeStats{}
		if err == nil {
			stats, err = a.personalProbeStats(inputs, r.deadline)
		}
		stats.apply(candidate, err)
	}
	r.keep(candidate)
}

func (a *personalAgentAdapter) personalProbeStats(inputs []personalAgentInput, deadline time.Time) (probeStats, error) {
	stats := probeStats{}
	seen := map[string]bool{}
	for _, input := range inputs {
		if time.Now().After(deadline) {
			stats.truncated = true
			break
		}
		info, err := a.files().Stat(input.path)
		if err != nil {
			return stats, err
		}
		stats.files++
		stats.bytes += info.Size()
		var sessions []map[string]any
		if input.database {
			db, tx, tables, err := a.openPersonalDatabase(input.path)
			if err != nil {
				return stats, err
			}
			if a.config.Kind == "hermes" && tables["sessions"] && tables["messages"] {
				sessions, err = queryMaps(tx, "SELECT id,started_at,ended_at FROM sessions ORDER BY id")
			} else if a.config.Kind == "openclaw" && tables["session_windows"] && tables["transcript_events"] {
				sessions, err = queryMaps(tx, "SELECT session_id AS id,created_at AS started_at,updated_at AS ended_at FROM session_windows ORDER BY session_id")
			} else if a.config.Kind == "openclaw" && tables["session_transcript_archives"] {
				// A database can retain reclaimed generations without live windows.
			} else {
				err = fmt.Errorf("unsupported %s history schema", a.config.Kind)
			}
			if err == nil && a.config.Kind == "openclaw" && tables["session_transcript_archives"] {
				var archives []map[string]any
				archives, err = queryMaps(tx, "SELECT session_id,generation,created_at FROM session_transcript_archives ORDER BY session_id,generation")
				for _, archive := range archives {
					sessions = append(sessions, map[string]any{"id": openClawArchivedID(firstString(archive["session_id"]), firstString(archive["generation"])), "ended_at": archive["created_at"]})
				}
			}
			tx.Rollback()
			db.Close()
			if err != nil {
				return stats, err
			}
		} else {
			file, err := a.files().Open(input.path)
			if err != nil {
				return stats, err
			}
			var reader io.Reader = file
			var decoder *zstd.Decoder
			if strings.HasSuffix(input.path, ".zst") {
				decoder, err = zstd.NewReader(file, zstd.WithDecoderMaxMemory(256<<20))
				if err != nil {
					file.Close()
					return stats, err
				}
				reader = decoder
			}
			line, readErr := bufio.NewReader(io.LimitReader(reader, probeSmallFileLimit)).ReadBytes('\n')
			file.Close()
			if decoder != nil {
				decoder.Close()
			}
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return stats, readErr
			}
			header := personalJSON(string(line))
			if firstString(header["type"]) == "session" && firstString(header["id"]) != "" {
				id := firstString(header["id"])
				if generation, _ := openClawFileGeneration(input.path); generation != "" {
					id = openClawArchivedID(id, generation)
				}
				sessions = []map[string]any{{"id": id, "started_at": header["timestamp"], "ended_at": info.ModTime().UnixMilli()}}
			}
		}
		for _, session := range sessions {
			id := personalNativeID(input.namespace, firstString(session["id"]))
			if seen[id] {
				continue
			}
			seen[id] = true
			stats.sessions++
			start, _ := parseTime(iso(session["started_at"]))
			end, _ := parseTime(defaultString(iso(session["ended_at"]), iso(session["started_at"])))
			if !start.IsZero() && (stats.oldest.IsZero() || start.Before(stats.oldest)) {
				stats.oldest = start
			}
			if end.After(stats.newest) {
				stats.newest = end
			}
			if len(stats.samples) < probeSampleSize {
				stats.samples = append(stats.samples, probeSession{id, end})
			}
		}
	}
	return stats, nil
}

func newPersonalAgentSources(home string, config Config, kind, folder, variable string) int {
	count := 0
	for _, dir := range probeFolderDirs(home, folder, []probeEnv{{Name: variable}}) {
		resolved, err := resolveUnprotected(home, dir)
		if err != nil {
			continue
		}
		a := &personalAgentAdapter{baseAdapter{config: SourceConfig{Kind: kind, Path: resolved}}}
		inputs, err := a.inputs()
		count += newProbeSource(home, config, kind, dir, err == nil && len(inputs) > 0)
	}
	return count
}
