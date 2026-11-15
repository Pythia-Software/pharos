package archive

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// Native schemas are documented at docs.openclaw.ai/reference/database-schemas/layout.
// In particular, navigation_json is not a transcript body: a NULL body must
// be recovered from its verified cold archive, never silently indexed as text.
type openClawEvent struct {
	seq  int64
	data map[string]any
}

type openClawLegacyIndexKey struct{}
type openClawLegacyIndex struct {
	bySession map[string]map[string]any
	err       error
}

func (a *personalAgentAdapter) openClawLegacyMetadata(input personalAgentInput, id string) (map[string]any, error) {
	dir := filepath.Dir(input.path)
	cache, _ := a.context().Value(openClawLegacyIndexKey{}).(map[string]openClawLegacyIndex)
	index, ok := cache[dir]
	if !ok {
		index.bySession = map[string]map[string]any{}
		file, err := a.files().Open(filepath.Join(dir, "sessions.json"))
		if err == nil {
			var entries map[string]map[string]any
			index.err = json.NewDecoder(file).Decode(&entries)
			file.Close()
			if index.err == nil {
				for _, key := range slices.Sorted(maps.Keys(entries)) {
					entry := entries[key]
					sessionID := firstString(entry["sessionId"])
					if sessionID == "" || index.bySession[sessionID] != nil {
						continue
					}
					meta := maps.Clone(entry)
					meta["session_key"], meta["current_session_id"] = key, sessionID
					if parent := entries[firstString(entry["spawnedBy"])]; parent != nil {
						if parentID := firstString(parent["sessionId"]); parentID != "" && parentID != sessionID {
							meta["parent_id"] = personalNativeID(input.namespace, parentID)
						}
					}
					index.bySession[sessionID] = meta
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			index.err = err
		}
		if cache != nil {
			cache[dir] = index
		}
	}
	if index.err != nil {
		return nil, fmt.Errorf("legacy session index: %w", index.err)
	}
	return mapValueDefault(maps.Clone(index.bySession[id])), nil
}

func personalBytes(value any) []byte {
	switch value := value.(type) {
	case []byte:
		return value
	case string:
		return []byte(value) // queryMaps represents SQLite blobs as strings.
	case nil:
		return nil
	}
	return []byte(asString(value))
}

func readOpenClawLines(reader io.Reader, visit func(map[string]any) error) error {
	return readOpenClawRecords(reader, true, visit)
}

func readOpenClawRecords(reader io.Reader, allowIncompleteTail bool, visit func(map[string]any) error) error {
	buffer := bufio.NewReader(reader)
	for line := 1; ; line++ {
		data, readErr := buffer.ReadBytes('\n')
		if len(bytes.TrimSpace(data)) > 0 {
			var value map[string]any
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.UseNumber()
			if err := decoder.Decode(&value); err != nil {
				if allowIncompleteTail && errors.Is(readErr, io.EOF) && !bytes.HasSuffix(data, []byte{'\n'}) && (errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "unexpected end")) {
					return nil
				}
				return fmt.Errorf("transcript line %d: %w", line, err)
			}
			if value == nil {
				return fmt.Errorf("transcript line %d is not an object", line)
			}
			if err := ensureEOF(decoder); err != nil {
				return fmt.Errorf("transcript line %d: %w", line, err)
			}
			if err := visit(value); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func (a *personalAgentAdapter) readOpenClawFile(input personalAgentInput) ([]WorkspaceRecord, error) {
	file, err := a.files().Open(input.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var reader io.Reader = file
	if strings.HasSuffix(input.path, ".zst") {
		decoder, err := zstd.NewReader(reader, zstd.WithDecoderMaxMemory(256<<20))
		if err != nil {
			return nil, err
		}
		defer decoder.Close()
		reader = decoder
	}
	var events []openClawEvent
	if err := readOpenClawLines(reader, func(event map[string]any) error {
		events = append(events, openClawEvent{int64(len(events)), event})
		return a.context().Err()
	}); err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}
	header := events[0].data
	if firstString(header["type"]) != "session" || firstString(header["id"]) == "" {
		return nil, fmt.Errorf("OpenClaw transcript has no session header")
	}
	id := firstString(header["id"])
	generation, reason := openClawFileGeneration(input.path)
	meta := map[string]any{"archive_generation": generation}
	if generation == "" {
		meta, err = a.openClawLegacyMetadata(input, id)
		if err != nil {
			return nil, err
		}
	}
	record, err := a.openClawRecord(input, id, meta, events, "")
	if err != nil {
		return nil, err
	}
	if generation != "" {
		cv := &record.Conversations[0]
		cv.NativeContext["archive_name"], cv.NativeContext["archive_generation"], cv.NativeContext["session_id"] = filepath.Base(input.path), generation, id
		record.Metadata["archive_generation"], record.Metadata["archive_reason"], record.Metadata["native_session_id"] = generation, reason, id
	}
	info, err := a.files().Stat(input.path)
	if err != nil {
		return nil, err
	}
	observe(&record, info.ModTime().UnixNano())
	return []WorkspaceRecord{record}, nil
}

func (a *personalAgentAdapter) readOpenClaw(input personalAgentInput) ([]WorkspaceRecord, error) {
	db, tx, tables, err := a.openPersonalDatabase(input.path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	defer tx.Rollback()
	live := tables["session_windows"] && tables["transcript_events"]
	if !live && !tables["session_transcript_archives"] {
		return nil, fmt.Errorf("unsupported OpenClaw SQLite schema: expected session_windows and transcript_events, or session_transcript_archives")
	}
	var windows []map[string]any
	if live {
		windows, err = queryMapsContext(a.context(), tx, "SELECT * FROM session_windows ORDER BY session_id")
		if err != nil {
			return nil, err
		}
	}
	nodes := map[string]map[string]any{}
	keys := map[string]string{}
	if tables["session_nodes"] {
		rows, err := queryMapsContext(a.context(), tx, "SELECT * FROM session_nodes ORDER BY session_key")
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			meta := personalJSON(row["entry_json"])
			for key, value := range row {
				if value != nil && key != "entry_json" {
					meta[key] = value
				}
			}
			key := firstString(row["session_key"])
			nodes[key] = meta
			keys[key] = firstString(row["current_session_id"])
		}
	}
	version := ""
	if tables["schema_meta"] {
		rows, err := queryMapsContext(a.context(), tx, "SELECT agent_id,app_version FROM schema_meta WHERE role='agent'")
		if err != nil {
			return nil, err
		}
		if len(rows) > 0 {
			version = firstString(rows[0]["app_version"])
			input.namespace = defaultString(rows[0]["agent_id"], input.namespace)
		}
	}
	records := []WorkspaceRecord{}
	var failures []error
	for _, window := range windows {
		if err := a.context().Err(); err != nil {
			return records, err
		}
		id, key := firstString(window["session_id"]), firstString(window["session_key"])
		if id == "" {
			failures = append(failures, fmt.Errorf("OpenClaw window has no session ID"))
			continue
		}
		meta := map[string]any{}
		// A node describes its current lifecycle only. Never apply its
		// new title/model/parent to a previous retained window.
		if keys[key] == id {
			maps.Copy(meta, nodes[key])
		}
		for k, value := range window {
			if value != nil {
				meta[k] = value
			}
		}
		parentKey := firstString(meta["spawned_by"], meta["spawnedBy"], meta["parent_session_key"], meta["parentSessionKey"])
		if parent := keys[parentKey]; parent != "" && parent != id && firstString(meta["reason"]) != "fork" {
			meta["parent_id"] = personalNativeID(input.namespace, parent)
		}
		meta["harness_version"] = version
		events, leaf, err := a.openClawEvents(tx, tables, input.path, id)
		if err != nil {
			failures = append(failures, fmt.Errorf("session %s: %w", id, err))
			continue
		}
		record, err := a.openClawRecord(input, id, meta, events, leaf)
		if err != nil {
			failures = append(failures, fmt.Errorf("session %s: %w", id, err))
			continue
		}
		observe(&record, a.databaseVersion(input.path))
		records = append(records, record)
	}
	if tables["session_transcript_archives"] {
		archives, err := queryMapsContext(a.context(), tx, "SELECT * FROM session_transcript_archives ORDER BY session_id,generation")
		if err != nil {
			failures = append(failures, err)
		}
		for _, archive := range archives {
			record, err := a.openClawReclaimedRecord(input, archive)
			if err != nil {
				failures = append(failures, fmt.Errorf("reclaimed transcript %s/%s: %w", archive["session_id"], archive["generation"], err))
				continue
			}
			observe(&record, a.databaseVersion(input.path))
			records = append(records, record)
		}
	}
	return records, errors.Join(failures...)
}

func openClawArchivedID(id, generation string) string { return id + ":archive:" + generation }

// Registered archives append their SQLite generation after the timestamp.
// Older archives have only a filename-owned suffix; retain its reason as well
// so reset and deletion archives created at the same instant stay distinct.
var openClawRegisteredArchiveSuffix = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}(?:\.\d{3})?Z\.([0-9a-f]{32})$`)

func openClawFileGeneration(file string) (generation, reason string) {
	name := strings.TrimSuffix(filepath.Base(file), ".zst")
	for _, reason := range []string{"reset", "deleted"} {
		marker := ".jsonl." + reason + "."
		if index := strings.LastIndex(name, marker); index > 0 {
			suffix := name[index+len(marker):]
			if suffix == "" {
				continue
			}
			if match := openClawRegisteredArchiveSuffix.FindStringSubmatch(suffix); match != nil {
				return match[1], reason
			}
			return reason + "." + suffix, reason
		}
	}
	return "", ""
}

// Reclaimed generations are canonical in SQLite, independently of whether
// their derived .reset/.deleted file was ever published. Repeated physical
// session IDs own distinct generations and must not overwrite one another.
func (a *personalAgentAdapter) openClawReclaimedRecord(input personalAgentInput, archive map[string]any) (WorkspaceRecord, error) {
	id, generation := firstString(archive["session_id"]), firstString(archive["generation"])
	if id == "" || generation == "" {
		return WorkspaceRecord{}, fmt.Errorf("missing archive identity")
	}
	data := personalBytes(archive["archive_blob"])
	if hashBytes(data) != firstString(archive["archive_sha256"]) {
		return WorkspaceRecord{}, fmt.Errorf("archived transcript failed hash verification")
	}
	var reader io.Reader = bytes.NewReader(data)
	switch firstString(archive["encoding"]) {
	case "identity":
	case "zstd":
		decoder, err := zstd.NewReader(reader, zstd.WithDecoderMaxMemory(256<<20))
		if err != nil {
			return WorkspaceRecord{}, err
		}
		defer decoder.Close()
		reader = decoder
	default:
		return WorkspaceRecord{}, fmt.Errorf("unsupported archive encoding %q", archive["encoding"])
	}
	var events []openClawEvent
	err := readOpenClawRecords(reader, false, func(event map[string]any) error {
		if len(events) == 0 {
			if firstString(event["type"]) != "session" || firstString(event["id"]) != id {
				return fmt.Errorf("archived transcript header does not match its session")
			}
		} else if firstString(event["type"]) == "session" {
			return fmt.Errorf("duplicate archived transcript header")
		}
		events = append(events, openClawEvent{int64(len(events)), event})
		return a.context().Err()
	})
	if err != nil {
		return WorkspaceRecord{}, err
	}
	if len(events) == 0 {
		return WorkspaceRecord{}, fmt.Errorf("archived transcript has no session header")
	}
	meta := map[string]any{"session_key": archive["session_key"], "archive_generation": generation}
	record, err := a.openClawRecord(input, id, meta, events, "")
	if err != nil {
		return WorkspaceRecord{}, err
	}
	cv := &record.Conversations[0]
	cv.NativeContext["archive_name"], cv.NativeContext["archive_generation"], cv.NativeContext["session_id"] = archive["archive_name"], generation, id
	record.Metadata["archive_generation"], record.Metadata["archive_reason"], record.Metadata["native_session_id"] = generation, archive["reason"], id
	return record, nil
}

func (a *personalAgentAdapter) openClawEvents(tx *sql.Tx, tables map[string]bool, database, id string) ([]openClawEvent, string, error) {
	bySeq := map[int64]map[string]any{}
	leaf := ""
	archives, err := openClawArchiveRows(tx, tables, id)
	if err != nil {
		return nil, "", err
	}
	for _, archive := range archives {
		events, archiveLeaf, err := a.openClawColdEvents(database, archive)
		if err != nil {
			return nil, "", err
		}
		for _, event := range events {
			bySeq[event.seq] = event.data
		}
		leaf = defaultString(archiveLeaf, leaf)
	}
	columns, err := tableColumns(tx, "transcript_events")
	if err != nil {
		return nil, "", err
	}
	compressed := "NULL"
	if columns["event_zstd"] {
		compressed = "event_zstd"
	}
	rows, err := queryMapsContext(a.context(), tx, "SELECT seq,event_json,"+compressed+" AS compressed FROM transcript_events WHERE session_id=? ORDER BY seq", id)
	if err != nil {
		return nil, "", err
	}
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(256<<20))
	if err != nil {
		return nil, "", err
	}
	defer decoder.Close()
	for _, row := range rows {
		seq := integer(row["seq"])
		data := personalBytes(row["event_json"])
		if len(data) == 0 && row["compressed"] != nil {
			data, err = decoder.DecodeAll(personalBytes(row["compressed"]), nil)
			if err != nil {
				return nil, "", fmt.Errorf("compressed event %d: %w", seq, err)
			}
		}
		if len(data) == 0 {
			if bySeq[seq] == nil {
				return nil, "", fmt.Errorf("event %d has no payload or retained cold archive", seq)
			}
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(data, &event); err != nil || event == nil {
			return nil, "", fmt.Errorf("invalid transcript event %d", seq)
		}
		bySeq[seq] = event
	}
	if tables["session_transcript_index_state"] {
		rows, err := queryMapsContext(a.context(), tx, "SELECT leaf_event_id,needs_rebuild FROM session_transcript_index_state WHERE session_id=?", id)
		if err != nil {
			return nil, "", err
		}
		if len(rows) > 0 && integer(rows[0]["needs_rebuild"]) == 0 {
			leaf = firstString(rows[0]["leaf_event_id"])
		}
	}
	events := make([]openClawEvent, 0, len(bySeq))
	for seq, event := range bySeq {
		events = append(events, openClawEvent{seq, event})
	}
	sort.Slice(events, func(i, j int) bool { return events[i].seq < events[j].seq })
	return events, leaf, nil
}

var openClawArchiveName = regexp.MustCompile(`^[a-f0-9]{64}\.jsonl\.zst$`)

func openClawColdPath(database, name string) (string, error) {
	if !openClawArchiveName.MatchString(name) {
		return "", fmt.Errorf("invalid OpenClaw cold archive name %q", name)
	}
	dir := filepath.Dir(database)
	if filepath.Base(dir) == "agent" {
		dir = filepath.Join(filepath.Dir(dir), "sessions")
	}
	return filepath.Join(dir, "cold", name), nil
}

// A chunk inherits its parent's session, generation and storage kind.
func openClawArchiveRows(tx *sql.Tx, tables map[string]bool, id string) ([]map[string]any, error) {
	if !tables["session_transcript_cold_archives"] {
		return nil, nil
	}
	query, args := "SELECT * FROM session_transcript_cold_archives", []any{}
	if id != "" {
		query, args = query+" WHERE session_id=?", []any{id}
	}
	parents, err := queryMaps(tx, query+" ORDER BY session_id", args...)
	if err != nil {
		return nil, err
	}
	var archives []map[string]any
	for _, parent := range parents {
		var chunks []map[string]any
		if tables["session_transcript_cold_archive_chunks"] {
			chunks, err = queryMaps(tx, "SELECT * FROM session_transcript_cold_archive_chunks WHERE session_id=? ORDER BY chunk_index", parent["session_id"])
			if err != nil {
				return nil, err
			}
		}
		if len(chunks) == 0 {
			archives = append(archives, parent)
			continue
		}
		manifest := []any{}
		var count, rawBytes, archiveBytes int64
		for index, chunk := range chunks {
			if integer(chunk["chunk_index"]) != int64(index) || index > 0 && integer(chunk["first_seq"]) <= integer(chunks[index-1]["last_seq"]) {
				return nil, fmt.Errorf("invalid OpenClaw cold chunk ordering")
			}
			manifest = append(manifest, []any{chunk["chunk_index"], chunk["archive_name"], chunk["archive_sha256"], chunk["first_seq"], chunk["last_seq"], chunk["event_count"], chunk["raw_bytes"], chunk["archive_bytes"]})
			count += integer(chunk["event_count"])
			rawBytes += integer(chunk["raw_bytes"])
			archiveBytes += integer(chunk["archive_bytes"])
			chunk["generation"], chunk["storage"] = parent["generation"], parent["storage"]
		}
		hash := hashBytes([]byte(jsonText(manifest)))
		if count != integer(parent["event_count"]) || rawBytes+int64(len(chunks)-1) != integer(parent["raw_bytes"]) || archiveBytes != integer(parent["archive_bytes"]) || integer(chunks[len(chunks)-1]["last_seq"]) != integer(parent["last_seq"]) || hash != firstString(parent["archive_sha256"]) || hash+".jsonl.zst" != firstString(parent["archive_name"]) {
			return nil, fmt.Errorf("OpenClaw cold chunk manifest does not match its parent")
		}
		archives = append(archives, chunks...)
	}
	return archives, nil
}

func openClawColdPaths(tx *sql.Tx, tables map[string]bool, database string) ([]string, error) {
	if !tables["session_transcript_cold_archives"] {
		return nil, nil
	}
	// Inventory never selects payload columns or verifies manifests. The
	// session reader owns verification; one broken manifest cannot block
	// preflight for healthy sessions or other agent databases.
	query := "SELECT archive_name FROM session_transcript_cold_archives WHERE storage='file'"
	if tables["session_transcript_cold_archive_chunks"] {
		query = "SELECT a.archive_name FROM session_transcript_cold_archives a WHERE a.storage='file' AND NOT EXISTS(SELECT 1 FROM session_transcript_cold_archive_chunks c WHERE c.session_id=a.session_id) UNION ALL SELECT c.archive_name FROM session_transcript_cold_archive_chunks c JOIN session_transcript_cold_archives a ON a.session_id=c.session_id WHERE a.storage='file'"
	}
	archives, err := queryMaps(tx, query)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, archive := range archives {
		path, err := openClawColdPath(database, firstString(archive["archive_name"]))
		if err != nil {
			// Invalid names are reported with their session.
			continue
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func (a *personalAgentAdapter) openClawColdEvents(database string, archive map[string]any) ([]openClawEvent, string, error) {
	data := personalBytes(archive["archive_blob"])
	if firstString(archive["storage"]) == "file" {
		path, err := openClawColdPath(database, firstString(archive["archive_name"]))
		if err != nil {
			return nil, "", err
		}
		file, err := a.files().Open(path)
		if err != nil {
			return nil, "", fmt.Errorf("missing cold transcript: %w", err)
		}
		data, err = io.ReadAll(io.LimitReader(file, integer(archive["archive_bytes"])+1))
		file.Close()
		if err != nil {
			return nil, "", err
		}
	}
	if int64(len(data)) != integer(archive["archive_bytes"]) || hashBytes(data) != firstString(archive["archive_sha256"]) {
		return nil, "", fmt.Errorf("cold transcript failed size/hash verification")
	}
	decoder, err := zstd.NewReader(bytes.NewReader(data), zstd.WithDecoderMaxMemory(128<<20))
	if err != nil {
		return nil, "", err
	}
	defer decoder.Close()
	// Upstream bounds each decoded archive/chunk at 64 MiB.
	decoded, err := io.ReadAll(io.LimitReader(decoder, (64<<20)+1))
	if err != nil || len(decoded) > 64<<20 {
		return nil, "", fmt.Errorf("cold transcript cannot be decoded within its size limit: %v", err)
	}
	var events []openClawEvent
	headerSeen, leaf := false, ""
	rawBytes := int64(-1)
	err = readOpenClawLines(bytes.NewReader(decoded), func(record map[string]any) error {
		kind := firstString(record["kind"])
		if !headerSeen {
			if kind != "header" || integer(record["version"]) != 1 || firstString(record["sessionId"]) != firstString(archive["session_id"]) || firstString(record["generation"]) != firstString(archive["generation"]) {
				return fmt.Errorf("cold transcript header does not match its database")
			}
			headerSeen = true
			return nil
		}
		row := mapValueDefault(record["row"])
		switch kind {
		case "header":
			return fmt.Errorf("duplicate cold transcript header")
		case "event":
			seq := integer(row["seq"])
			if len(events) > 0 && seq <= events[len(events)-1].seq {
				return fmt.Errorf("cold transcript events are out of order")
			}
			text := firstString(row["event_json"])
			var event map[string]any
			if err := json.Unmarshal([]byte(text), &event); err != nil || event == nil {
				return fmt.Errorf("invalid cold transcript event %d", seq)
			}
			events = append(events, openClawEvent{seq, event})
			rawBytes += int64(len(text) + 1)
		case "index":
			leaf = firstString(row["leaf_event_id"])
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	if !headerSeen || int64(len(events)) != integer(archive["event_count"]) || rawBytes != integer(archive["raw_bytes"]) || len(events) == 0 || events[len(events)-1].seq != integer(archive["last_seq"]) || archive["first_seq"] != nil && events[0].seq != integer(archive["first_seq"]) {
		return nil, "", fmt.Errorf("cold transcript counts do not match its database")
	}
	return events, leaf, nil
}

func (a *personalAgentAdapter) openClawRecord(input personalAgentInput, id string, meta map[string]any, events []openClawEvent, leaf string) (WorkspaceRecord, error) {
	identity := id
	if generation := firstString(meta["archive_generation"]); generation != "" {
		identity = openClawArchivedID(id, generation)
	}
	cv := ConversationRecord{NativeID: personalNativeID(input.namespace, identity), Provider: "openclaw", Account: a.config.Account, Origin: a.original(input.path), Coverage: "complete", Harness: "openclaw", Extractor: openclawExtractor,
		Model: firstString(meta["model"], meta["modelOverride"]), Effort: recordedEffort(map[string]any{"effort": meta["thinkingLevel"]}),
		ParentNativeID: firstString(meta["parent_id"]), StartedAt: iso(firstNonNil(meta["started_at"], meta["created_at"])), EndedAt: iso(meta["ended_at"]),
		HarnessVersionFirst: firstString(meta["harness_version"]), HarnessVersionLast: firstString(meta["harness_version"]),
		NativeContext: map[string]any{"session_key": meta["session_key"], "channel": meta["channel"], "current_session_id": meta["current_session_id"]}}
	if firstString(meta["reason"]) != "fork" {
		cv.NativeContext["spawned_by"] = firstString(meta["spawned_by"], meta["spawnedBy"], meta["parent_session_key"], meta["parentSessionKey"])
	}
	byID := map[string]map[string]any{}
	for _, event := range events {
		if eventID := firstString(event.data["id"]); eventID != "" {
			byID[eventID] = event.data
		}
	}
	if leaf == "" {
		for i := len(events) - 1; i >= 0; i-- {
			if firstString(events[i].data["type"]) != "session" && firstString(events[i].data["id"]) != "" {
				leaf = firstString(events[i].data["id"])
				break
			}
		}
	}
	selected := map[string]bool{}
	for at := leaf; at != ""; {
		if selected[at] {
			return WorkspaceRecord{}, fmt.Errorf("cyclic OpenClaw branch at %s", at)
		}
		event := byID[at]
		if event == nil {
			return WorkspaceRecord{}, fmt.Errorf("OpenClaw branch references missing event %s", at)
		}
		selected[at] = true
		if firstString(event["type"]) == "reset" {
			// A preserve-tail reset explicitly retains part of its old
			// branch. Only those user/assistant messages remain selected.
			if kept := firstString(event["firstKeptEntryId"]); kept != "" {
				found := false
				visited := map[string]bool{}
				for tail := firstString(event["parentId"]); tail != ""; {
					prior := byID[tail]
					if prior == nil || visited[tail] {
						break
					}
					visited[tail] = true
					role := firstString(mapValueDefault(prior["message"])["role"])
					if firstString(prior["type"]) == "message" && (role == "user" || role == "assistant") {
						selected[tail] = true
					}
					if tail == kept {
						found = true
						break
					}
					tail = firstString(prior["parentId"])
				}
				if !found {
					return WorkspaceRecord{}, fmt.Errorf("OpenClaw reset references missing retained tail %s", kept)
				}
			}
			break
		}
		at = firstString(event["parentId"])
	}
	// Rewrites copy request usage too. Choose one event in each explicit
	// supersession chain; never deduplicate unrelated equal message text.
	superseded := map[string]bool{}
	for _, event := range events {
		if previous := firstString(event.data["supersedesEntryId"]); previous != "" {
			superseded[previous] = true
		}
	}
	cwd := firstString(meta["cwd"])
	type configuration struct{ model, effort string }
	configurations := map[string]configuration{}
	for _, stored := range events {
		event := stored.data
		typ, eventID := firstString(event["type"]), defaultString(event["id"], "event:"+strconv.FormatInt(stored.seq, 10))
		at := iso(event["timestamp"])
		if typ == "session" {
			cwd, cv.StartedAt = firstString(event["cwd"]), defaultString(at, cv.StartedAt)
			continue
		}
		// Follow each event's branch. A model change on a discarded branch
		// must not label a later reply on a different branch.
		config := configurations[firstString(event["parentId"])]
		if typ == "model_change" {
			config.model = firstString(event["modelId"], event["model"])
		}
		if typ == "thinking_level_change" {
			config.effort = recordedEffort(map[string]any{"effort": event["thinkingLevel"]})
		}
		if message := mapValue(event["message"]); message != nil {
			config.model = firstString(message["model"], config.model)
		}
		configurations[eventID] = config
		model, effort := config.model, config.effort
		base := MessageRecord{NativeID: eventID, CreatedAt: at, Model: model, Effort: effort, Selected: selected[eventID] && !superseded[eventID], PreviousNativeID: firstString(event["parentId"]), EvidenceLocator: fmt.Sprintf("%s#%s:%d", a.original(input.path), identity, stored.seq),
			RawText: jsonText(map[string]any{"type": typ, "id": eventID})}
		message := mapValue(event["message"])
		if typ != "message" || message == nil {
			base.Role, base.Kind, base.Text = "system", "metadata", jsonText(event)
			if typ == "compaction" || typ == "branch_summary" {
				base.Text = jsonText(map[string]any{"type": typ, "summary": event["summary"], "firstKeptEntryId": event["firstKeptEntryId"], "tokensBefore": event["tokensBefore"]})
			}
			cv.Messages = append(cv.Messages, base)
			continue
		}
		base.Role = firstString(message["role"])
		base.Model = firstString(message["model"], model)
		base.CreatedAt = defaultString(at, iso(message["timestamp"]))
		if base.Role == "toolResult" {
			base.Role, base.Kind, base.CallID = "tool", "tool_result", firstString(message["toolCallId"])
			base.Text = jsonText(map[string]any{"tool": message["toolName"], "content": message["content"], "is_error": message["isError"], "details": message["details"]})
			cv.Messages = append(cv.Messages, base)
		} else {
			if base.Role == "user" {
				if cv.ParentNativeID != "" {
					base.Role, base.Sender = agentRole, "agent:"+cv.ParentNativeID
				} else if via := firstString(meta["created_via"], meta["createdVia"]); via == "cron" || via == "internal" || firstString(meta["created_actor_type"]) == "system" {
					base.Role, base.Sender = agentRole, "automation:"+defaultString(via, "system")
				}
			}
			cv.Messages = append(cv.Messages, personalContent(base, message["content"])...)
		}
		if usage := mapValue(message["usage"]); usage != nil && !superseded[eventID] {
			counts := map[string]any{}
			for from, to := range map[string]string{"input": "input_tokens", "output": "output_tokens", "cacheRead": "cache_read_input_tokens", "cacheWrite": "cache_creation_input_tokens"} {
				if value, ok := usage[from]; ok {
					counts[to] = value
				}
			}
			if len(counts) > 0 {
				cv.Messages = append(cv.Messages, personalUsage(base, eventID, counts))
			}
		}
		if base.Selected {
			cv.Model, cv.Effort = firstString(base.Model, cv.Model), firstString(base.Effort, cv.Effort)
		}
	}
	if cv.ParentNativeID != "" {
		cv.AgentDepth = 1
	}
	return a.personalRecord(input, cv, meta, cwd), nil
}

// personalContent normalizes Pi/Anthropic blocks and plain text without
// confusing thinking summaries, tool arguments, or tool results with prose.
func personalContent(base MessageRecord, content any) []MessageRecord {
	if text, ok := content.(string); ok {
		base.Kind, base.Text = "message", text
		return []MessageRecord{base}
	}
	blocks, _ := content.([]any)
	var messages []MessageRecord
	for index, raw := range blocks {
		block := mapValueDefault(raw)
		message := base
		if index > 0 {
			message.NativeID += ":" + strconv.Itoa(index)
		}
		switch firstString(block["type"]) {
		case "text":
			message.Kind, message.Text = "message", firstString(block["text"])
		case "thinking", "reasoning":
			message.Kind, message.Text = "reasoning", firstString(block["thinking"], block["text"])
			message.ReasoningFormat = "text"
		case "toolCall", "tool_use":
			message.Kind, message.CallID = "tool_call", firstString(block["id"])
			message.Text = jsonText(map[string]any{"tool": block["name"], "input": firstNonNil(block["arguments"], block["input"])})
		default:
			message.Kind, message.Text = "metadata", jsonText(block)
		}
		messages = append(messages, message)
	}
	return messages
}
