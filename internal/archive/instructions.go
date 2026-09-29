package archive

// The loaded-instructions record (G7): which instruction files and skills each
// Claude Code or Codex conversation loaded, as the harness recorded it in the
// transcript. It is what instruction drift (D9) and the context an
// instruction change adds are measured from.
//
// Sources, per harness (kind is lowercased and normalized):
//
//   - Claude Code writes an `attachment` event of type `instructions` whose
//     `files` list each loaded file with its `path`, `type`, and full
//     `content`. The type becomes the kind: Project -> project, User -> user,
//     Local -> local, AutoMem -> automem, Managed -> managed, and any other
//     lowercased. A `nested_memory` attachment (a CLAUDE.md in a subdirectory,
//     loaded when the agent first touches it) is kind nested. An
//     `invoked_skills` attachment is kind skill, its path as recorded
//     (`bundled:<name>` or an absolute SKILL.md path). A `skill_listing`
//     attachment is one skill_listing row per name, the path being the skill
//     name because Claude records no path; bytes are the listing entry's.
//   - Codex writes the AGENTS.md chain it loaded as a message beginning
//     "# AGENTS.md instructions for <dir>" with the text in <INSTRUCTIONS>
//     tags, and names the directory in `world_state.state.agents_md`. Both are
//     kind agents_md with path <dir>/AGENTS.md (the directory's chain, which
//     may include AGENTS.override.md or the global file); the message gives
//     bytes and hash. A `<skills_instructions>` developer message lists the
//     skills Codex offered, one skill_listing row per entry, the path being the
//     SKILL.md locator with the `rN` skill roots expanded; bytes are the
//     entry's line, which is all a listing puts in context.
//
// bytes and hash are of the content last loaded in the conversation (the
// content Claude records is the file without its trailing newline), NULL when
// the transcript names a file without its content. first_seen_at and
// last_seen_at are the first and last events that recorded it.
//
// Ingest replaces a conversation's rows whenever it writes the conversation;
// BackfillInstructions fills conversations indexed before the record existed
// by re-reading their source or captured transcripts, never changing them.
// conversation_instructions_state marks each conversation read, so a
// conversation that loaded nothing is not read again.
//
// Two reads serve findings:
//
//   - instructionCoverage: per provider in a repository, how many top-level
//     conversations have a record, how many loaded a project-level file, and
//     the repository files loaded with their latest size.
//   - instructionLoad: for one repository file over a time range, the
//     conversations that loaded it, their model requests, the file's size at
//     the start and end of the range, and bytes x requests, the context the
//     file added. Comparing the windows before and after a change gives the
//     added context net of the change.

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// instructionsVersion names the parsing that built a conversation's rows;
// bumping it makes the library upgrade read Claude and Codex transcripts again.
const instructionsVersion = "instructions-v1"

// projectInstructionKinds are repository instruction files a harness loads at
// the start of a session.
var projectInstructionKinds = []string{"project", "local", "agents_md"}

// InstructionRecord is one instruction file or skill a conversation loaded.
type InstructionRecord struct {
	Harness, Path, Kind string
	Bytes               *int64 `json:",omitempty"`
	Hash                string `json:",omitempty"`
	FirstSeenAt         string `json:",omitempty"`
	LastSeenAt          string `json:",omitempty"`
}

// instructionSet merges repeated loads of one file (Claude records them again
// after a compaction) into one record, keeping the last content.
type instructionSet struct {
	harness string
	order   []string
	byKey   map[string]*InstructionRecord
}

func newInstructionSet(harness string) *instructionSet {
	return &instructionSet{harness: harness, byKey: map[string]*InstructionRecord{}}
}

func (s *instructionSet) add(path, kind, content string, hasContent bool, at string) {
	path, kind = strings.TrimSpace(path), strings.TrimSpace(kind)
	if path == "" || kind == "" {
		return
	}
	key := kind + "\x00" + path
	record := s.byKey[key]
	if record == nil {
		record = &InstructionRecord{Harness: s.harness, Path: path, Kind: kind}
		s.byKey[key] = record
		s.order = append(s.order, key)
	}
	if at != "" {
		if record.FirstSeenAt == "" || at < record.FirstSeenAt {
			record.FirstSeenAt = at
		}
		if at > record.LastSeenAt {
			record.LastSeenAt = at
		}
	}
	if hasContent {
		size := int64(len(content))
		record.Bytes, record.Hash = &size, hashBytes([]byte(content))
	}
}

func (s *instructionSet) records() []InstructionRecord {
	if len(s.order) == 0 {
		return nil
	}
	output := make([]InstructionRecord, 0, len(s.order))
	for _, key := range s.order {
		output = append(output, *s.byKey[key])
	}
	return output
}

// claudeInstructionKind lowercases Claude's memory type (Project, User, Local,
// AutoMem, Managed, ...).
func claudeInstructionKind(value any) string {
	kind := strings.ToLower(strings.TrimSpace(firstString(value)))
	if kind == "" {
		return "unknown"
	}
	return strings.NewReplacer(" ", "_", "-", "_").Replace(kind)
}

// claudeInstructions reads the instruction attachments of one Claude session
// file's events.
func claudeInstructions(events []map[string]any) []InstructionRecord {
	set := newInstructionSet("claude-code")
	for _, event := range events {
		if firstString(event["type"]) != "attachment" {
			continue
		}
		attachment := mapValue(event["attachment"])
		at := iso(event["timestamp"])
		switch firstString(attachment["type"]) {
		case "instructions":
			for _, file := range mapSlice(attachment["files"]) {
				content, ok := file["content"].(string)
				set.add(firstString(file["path"]), claudeInstructionKind(file["type"]), content, ok, at)
			}
		case "nested_memory":
			inner := mapValue(attachment["content"])
			content, ok := inner["content"].(string)
			set.add(firstString(attachment["path"], inner["path"]), "nested", content, ok, at)
		case "invoked_skills":
			for _, skill := range mapSlice(attachment["skills"]) {
				content, ok := skill["content"].(string)
				set.add(firstString(skill["path"], skill["name"]), "skill", content, ok, at)
			}
		case "skill_listing":
			entries := skillListingEntries(firstString(attachment["content"]))
			names := stringSlice(attachment["names"])
			if len(names) == 0 {
				for name := range entries {
					names = append(names, name)
				}
				sort.Strings(names)
			}
			for _, name := range names {
				entry, ok := entries[name]
				set.add(name, "skill_listing", entry, ok, at)
			}
		}
	}
	return set.records()
}

// skillListingEntries splits Claude's "- name: description" listing by name.
func skillListingEntries(content string) map[string]string {
	entries := map[string]string{}
	var name string
	var entry strings.Builder
	flush := func() {
		if name != "" {
			entries[name] = strings.TrimRight(entry.String(), "\n")
		}
		name = ""
		entry.Reset()
	}
	for _, line := range strings.SplitAfter(content, "\n") {
		if strings.HasPrefix(line, "- ") {
			flush()
			head := strings.TrimPrefix(line, "- ")
			// Names have no spaces but may have colons (plugin skills such
			// as conductor:conductor), so the name ends at ": ".
			if at := strings.Index(head, ": "); at > 0 {
				name = strings.TrimSpace(head[:at])
			} else if strings.HasSuffix(strings.TrimSpace(head), ":") {
				name = strings.TrimSuffix(strings.TrimSpace(head), ":")
			}
		}
		if name != "" {
			entry.WriteString(line)
		}
	}
	flush()
	return entries
}

const codexAgentsHeader = "# AGENTS.md instructions for "

var (
	codexSkillRoot    = regexp.MustCompile("^- `(r\\d+)` = `([^`]+)`\\s*$")
	codexSkillLocator = regexp.MustCompile(`\((\w[\w ]*): ([^()]+)\)\s*$`)
)

// codexInstructions reads the AGENTS.md and skill listing a Codex rollout
// recorded.
func codexInstructions(events []map[string]any) []InstructionRecord {
	set := newInstructionSet("codex")
	for _, event := range events {
		payload := mapValue(event["payload"])
		at := iso(event["timestamp"])
		switch etype := firstString(event["type"]); {
		case etype == "world_state":
			for _, dir := range codexAgentsDirectories(mapValue(payload["state"])["agents_md"]) {
				set.add(codexAgentsPath(dir), "agents_md", "", false, at)
			}
		case etype == "response_item" && firstString(payload["type"]) == "message":
			items, _ := payload["content"].([]any)
			for _, raw := range items {
				text := firstString(mapValue(raw)["text"])
				switch {
				case strings.HasPrefix(text, codexAgentsHeader):
					dir, body, _ := strings.Cut(strings.TrimPrefix(text, codexAgentsHeader), "\n")
					if start := strings.Index(body, "<INSTRUCTIONS>"); start >= 0 {
						body = body[start+len("<INSTRUCTIONS>"):]
						if end := strings.LastIndex(body, "</INSTRUCTIONS>"); end >= 0 {
							body = body[:end]
						}
						set.add(codexAgentsPath(dir), "agents_md", strings.Trim(body, "\n"), true, at)
					} else {
						set.add(codexAgentsPath(dir), "agents_md", "", false, at)
					}
				case strings.HasPrefix(text, "<skills_instructions>"):
					codexSkillListing(set, text, at)
				}
			}
		}
	}
	return set.records()
}

func codexAgentsPath(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	return strings.TrimRight(dir, "/") + "/AGENTS.md"
}

// codexAgentsDirectories accepts world_state.agents_md as a snapshot
// ({"directory": ...}), a map of such snapshots by environment or directory,
// or a list of them. It is empty ({}) when no AGENTS.md was found.
func codexAgentsDirectories(value any) []string {
	directory := func(snapshot map[string]any) string {
		return firstString(snapshot["directory"], snapshot["path"], snapshot["cwd"])
	}
	dirs := []string{}
	switch item := value.(type) {
	case map[string]any:
		if dir := directory(item); dir != "" {
			return []string{dir}
		}
		keys := make([]string, 0, len(item))
		for key := range item {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if dir := directory(mapValue(item[key])); dir != "" {
				dirs = append(dirs, dir)
			} else if filepath.IsAbs(key) && item[key] != nil {
				dirs = append(dirs, key)
			}
		}
	case []any:
		for _, element := range item {
			if dir := directory(mapValue(element)); dir != "" {
				dirs = append(dirs, dir)
			}
		}
	}
	return dirs
}

// codexSkillListing records each entry of an "### Available skills" list.
// Entries end in a locator such as (file: /abs/SKILL.md) or (file:
// r0/name/SKILL.md), where rN is a root from the "### Skill roots" table.
func codexSkillListing(set *instructionSet, text, at string) {
	roots := map[string]string{}
	section := ""
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "### ") {
			section = strings.TrimSpace(strings.TrimPrefix(line, "### "))
			continue
		}
		if match := codexSkillRoot.FindStringSubmatch(line); match != nil {
			roots[match[1]] = match[2]
			continue
		}
		if section != "Available skills" || !strings.HasPrefix(line, "- ") {
			continue
		}
		path := ""
		if match := codexSkillLocator.FindStringSubmatch(line); match != nil {
			path = strings.TrimSpace(match[2])
			if head, rest, ok := strings.Cut(path, "/"); ok && roots[head] != "" {
				path = strings.TrimRight(roots[head], "/") + "/" + rest
			}
		} else if name, _, ok := strings.Cut(strings.TrimPrefix(line, "- "), ": "); ok {
			path = strings.TrimSpace(name)
		}
		set.add(path, "skill_listing", line, true, at)
	}
}

// instructionRepoPath is path relative to its repository checkout, or nil
// outside one (agent homes, bundled skills, bare skill names). Memory and user
// files live in the agent's home even when a session ran from a checkout that
// contains it.
func instructionRepoPath(path, kind string, roots []repoRoot) any {
	if !filepath.IsAbs(path) || kind == "automem" || kind == "user" || kind == "managed" {
		return nil
	}
	rel, _, scope := resolveRepoPath(path, "", roots)
	if scope != "repo" || rel == "" || rel == "." {
		return nil
	}
	return rel
}

// replaceConversationInstructions stores the record a conversation's
// transcript holds, in the transaction that writes the conversation. locations
// may be nil; they are read when a record needs them.
func replaceConversationInstructions(tx *sql.Tx, workspaceID, conversationID string, records []InstructionRecord, locations []repoRoot, status string) error {
	if _, err := tx.Exec("DELETE FROM conversation_instructions WHERE conversation_id=?", conversationID); err != nil {
		return err
	}
	if err := upsertConversationInstructions(tx, workspaceID, conversationID, records, locations); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO conversation_instructions_state(conversation_id,version,status,updated_at) VALUES(?,?,?,?)
		ON CONFLICT(conversation_id) DO UPDATE SET version=excluded.version,status=excluded.status,updated_at=excluded.updated_at`, conversationID, instructionsVersion, status, now())
	return err
}

// upsertConversationInstructions adds records to what is stored, widening the
// seen span; the later load's size and hash win. A non-writer copy of a
// conversation merges this way.
func upsertConversationInstructions(tx *sql.Tx, workspaceID, conversationID string, records []InstructionRecord, locations []repoRoot) error {
	if len(records) == 0 {
		return nil
	}
	if locations == nil {
		var err error
		if locations, err = toolRepositoryLocations(tx); err != nil {
			return err
		}
	}
	roots, err := toolRepoRoots(tx, workspaceID, locations)
	if err != nil {
		return err
	}
	statement, err := tx.Prepare(`INSERT INTO conversation_instructions(conversation_id,harness,path,kind,bytes,hash,repo_path,first_seen_at,last_seen_at)
		VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(conversation_id,path,kind) DO UPDATE SET
		bytes=CASE WHEN excluded.bytes IS NOT NULL AND (conversation_instructions.bytes IS NULL OR COALESCE(excluded.last_seen_at,'')>=COALESCE(conversation_instructions.last_seen_at,'')) THEN excluded.bytes ELSE conversation_instructions.bytes END,
		hash=CASE WHEN excluded.hash IS NOT NULL AND (conversation_instructions.hash IS NULL OR COALESCE(excluded.last_seen_at,'')>=COALESCE(conversation_instructions.last_seen_at,'')) THEN excluded.hash ELSE conversation_instructions.hash END,
		first_seen_at=COALESCE(MIN(conversation_instructions.first_seen_at,excluded.first_seen_at),conversation_instructions.first_seen_at,excluded.first_seen_at),
		last_seen_at=COALESCE(MAX(conversation_instructions.last_seen_at,excluded.last_seen_at),conversation_instructions.last_seen_at,excluded.last_seen_at),
		repo_path=COALESCE(excluded.repo_path,conversation_instructions.repo_path)`)
	if err != nil {
		return err
	}
	defer statement.Close()
	for _, record := range records {
		var size any
		if record.Bytes != nil {
			size = *record.Bytes
		}
		if _, err := statement.Exec(conversationID, record.Harness, record.Path, record.Kind, size, nilIfEmpty(record.Hash), instructionRepoPath(record.Path, record.Kind, roots),
			nilIfEmpty(iso(record.FirstSeenAt)), nilIfEmpty(iso(record.LastSeenAt))); err != nil {
			return err
		}
	}
	return nil
}

// instructionMarkers select the transcript lines that can hold a record, so a
// backfill decodes a few lines of each file rather than all of them.
var instructionMarkers = map[string][][]byte{
	"claude": {[]byte(`"attachment"`)},
	// Without angle brackets: an encoder may escape them.
	"codex": {[]byte(`"world_state"`), []byte(codexAgentsHeader), []byte(`skills_instructions`)},
}

// readInstructionsFile parses the record from a Claude session file or Codex
// rollout, reading only the bytes present when it was opened.
func readInstructionsFile(path, provider string) ([]InstructionRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	markers := instructionMarkers[provider]
	reader := bufio.NewReaderSize(io.LimitReader(file, info.Size()), 256*1024)
	events := []map[string]any{}
	for {
		line, readErr := reader.ReadBytes('\n')
		for _, marker := range markers {
			if bytes.Contains(line, marker) {
				var event map[string]any
				if json.Unmarshal(line, &event) == nil && event != nil {
					events = append(events, event)
				}
				break
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	if provider == "codex" {
		return codexInstructions(events), nil
	}
	return claudeInstructions(events), nil
}

// pendingInstructionsQuery selects Claude and Codex conversations whose record
// was never read, or was read by an older parser. Conductor wrappers carry
// none (their native copy does); TL1 runs get theirs when next ingested.
const pendingInstructionsQuery = `FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
	LEFT JOIN conversation_instructions_state s ON s.conversation_id=c.id
	WHERE w.source_kind IN ('claude','codex') AND (s.conversation_id IS NULL OR s.version<>?)`

func (c *Catalog) pendingInstructions(ctx context.Context) (int64, error) {
	var count int64
	err := c.DB.QueryRowContext(ctx, "SELECT COUNT(*) "+pendingInstructionsQuery, instructionsVersion).Scan(&count)
	return count, err
}

// BackfillInstructions fills the record for conversations indexed before it
// existed, from each conversation's source transcript on this Mac or its
// captured copy. Files are read before a batch takes the write lock, and each
// batch commits on its own, so an interrupted run resumes where it stopped. A
// conversation whose transcript is gone is marked unavailable rather than
// read again; indexing a capture of it later writes its record.
func (c *Catalog) BackfillInstructions(ctx context.Context, progress func(done, total int)) (int, error) {
	pending, err := queryMapsContext(ctx, c.DB, `SELECT c.id,c.workspace_id,w.source_kind,c.origin,c.origin_host_id `+pendingInstructionsQuery+` ORDER BY c.rowid`, instructionsVersion)
	if err != nil {
		return 0, err
	}
	locations, err := toolRepositoryLocations(c.DB)
	if err != nil {
		return 0, err
	}
	captured := map[string]map[string]string{}
	var hosts []string
	const batch = 100
	type prepared struct {
		id, workspaceID, status string
		records                 []InstructionRecord
	}
	for start := 0; start < len(pending); start += batch {
		if err := ctx.Err(); err != nil {
			return start, err
		}
		rows := pending[start:min(start+batch, len(pending))]
		values := make([]prepared, 0, len(rows))
		for _, row := range rows {
			value := prepared{id: firstString(row["id"]), workspaceID: firstString(row["workspace_id"]), status: "unavailable"}
			origin, host := firstString(row["origin"]), firstString(row["origin_host_id"])
			path := c.harnessSourcePath(origin, host, captured)
			if path == "" && origin != "" && !strings.HasPrefix(origin, "sqlite:") {
				// A conversation merged from several Macs' copies names no one
				// host; this Mac's file or any capture of it will do.
				if hosts == nil {
					hosts = append([]string{currentHost().ID}, c.captureHosts()...)
				}
				for _, other := range hosts {
					if other != host {
						if path = c.harnessSourcePath(origin, other, captured); path != "" {
							break
						}
					}
				}
			}
			if path != "" {
				records, err := readInstructionsFile(path, firstString(row["source_kind"]))
				if err == nil {
					value.status, value.records = "transcript", records
				}
			}
			values = append(values, value)
		}
		if err := c.writeTransaction(ctx, fmt.Sprintf("instructions-backfill batch=%d", start/batch), func(tx *sql.Tx) error {
			for _, value := range values {
				// A conversation indexed meanwhile holds a newer read.
				var version string
				if err := tx.QueryRow(`SELECT version FROM conversation_instructions_state WHERE conversation_id=?`, value.id).Scan(&version); err != nil && err != sql.ErrNoRows {
					return err
				}
				if version == instructionsVersion {
					continue
				}
				if err := replaceConversationInstructions(tx, value.workspaceID, value.id, value.records, locations, value.status); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return start, err
		}
		if progress != nil {
			progress(start+len(rows), len(pending))
		}
	}
	return len(pending), nil
}

// captureHosts lists the hosts with captures in this library.
func (c *Catalog) captureHosts() []string {
	hosts := []string{}
	entries, err := os.ReadDir(c.captureRootPath())
	if err != nil {
		return hosts
	}
	for _, entry := range entries {
		if entry.IsDir() {
			hosts = append(hosts, entry.Name())
		}
	}
	return hosts
}

// instructionUse is instructionCoverage's row for one provider.
type instructionUse struct {
	Provider string `json:"provider"`
	// Conversations are the provider's top-level conversations in range;
	// Recorded those with at least one row, and ProjectLoaded those that
	// loaded a project-level file (projectInstructionKinds).
	Conversations int64 `json:"conversations"`
	Recorded      int64 `json:"recorded"`
	ProjectLoaded int64 `json:"project_loaded"`
	// Unavailable conversations had no transcript left to read when the
	// record was backfilled, so their absence of rows says nothing.
	Unavailable int64                `json:"unavailable"`
	Files       []instructionFileUse `json:"files"`
}

// instructionFileUse is one repository file a provider loaded.
type instructionFileUse struct {
	RepoPath string `json:"repo_path"`
	Kind     string `json:"kind"`
	// Conversations counts every conversation that loaded it, sub-agents
	// included.
	Conversations int64  `json:"conversations"`
	Bytes         *int64 `json:"bytes"`
	Hash          string `json:"hash,omitempty"`
	LastSeenAt    string `json:"last_seen_at,omitempty"`
}

// instructionCoverage reports, per provider (the native transcript's source
// kind: claude, codex, antigravity), which conversations in repositoryID
// (every repository when empty) that started at or after since (timeLayout;
// everything when empty) carry a loaded-instructions record: top-level
// conversation counts, how many have a record and how many loaded a
// project-level file, and each repository file loaded (repo_path and kind)
// with its size and hash at the latest load. Conductor wrappers and TL1 runs
// are left out: the wrapped native conversation carries the record. It reads
// through the repository and conversation indexes and the table's primary key.
func (c *Catalog) instructionCoverage(ctx context.Context, q queryer, repositoryID, since string) ([]instructionUse, error) {
	if q == nil {
		q = c.DB
	}
	filter := `w.source_kind IN ('claude','codex','antigravity') AND (?='' OR w.repository_id=?) AND (?='' OR COALESCE(c.started_at,w.activity_at)>=?)`
	args := []any{repositoryID, repositoryID, since, since}
	kinds := "'" + strings.Join(projectInstructionKinds, "','") + "'"
	rows, err := queryMapsContext(ctx, q, `SELECT w.source_kind provider,COUNT(*) conversations,
		SUM(EXISTS(SELECT 1 FROM conversation_instructions i WHERE i.conversation_id=c.id)) recorded,
		SUM(EXISTS(SELECT 1 FROM conversation_instructions i WHERE i.conversation_id=c.id AND i.kind IN (`+kinds+`))) project_loaded,
		SUM(EXISTS(SELECT 1 FROM conversation_instructions_state s WHERE s.conversation_id=c.id AND s.status='unavailable')) unavailable
		FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
		WHERE c.parent_id IS NULL AND `+filter+` GROUP BY w.source_kind ORDER BY w.source_kind`, args...)
	if err != nil {
		return nil, err
	}
	output := make([]instructionUse, 0, len(rows))
	index := map[string]int{}
	for _, row := range rows {
		provider := firstString(row["provider"])
		index[provider] = len(output)
		output = append(output, instructionUse{Provider: provider, Conversations: integer(row["conversations"]), Recorded: integer(row["recorded"]),
			ProjectLoaded: integer(row["project_loaded"]), Unavailable: integer(row["unavailable"]), Files: []instructionFileUse{}})
	}
	// SQLite takes bytes, hash and last_seen_at from the row holding the
	// latest last_seen_at of each group.
	files, err := queryMapsContext(ctx, q, `SELECT w.source_kind provider,i.repo_path,i.kind,COUNT(DISTINCT i.conversation_id) conversations,
		i.bytes,i.hash,MAX(COALESCE(i.last_seen_at,'')) last_seen_at
		FROM conversation_instructions i JOIN conversations c ON c.id=i.conversation_id JOIN workspaces w ON w.id=c.workspace_id
		WHERE i.repo_path IS NOT NULL AND `+filter+` GROUP BY w.source_kind,i.repo_path,i.kind ORDER BY w.source_kind,i.repo_path,i.kind`, args...)
	if err != nil {
		return nil, err
	}
	for _, row := range files {
		at, ok := index[firstString(row["provider"])]
		if !ok {
			continue
		}
		file := instructionFileUse{RepoPath: firstString(row["repo_path"]), Kind: firstString(row["kind"]), Conversations: integer(row["conversations"]),
			Hash: firstString(row["hash"]), LastSeenAt: firstString(row["last_seen_at"])}
		if row["bytes"] != nil {
			size := integer(row["bytes"])
			file.Bytes = &size
		}
		output[at].Files = append(output[at].Files, file)
	}
	return output, nil
}

// instructionLoadStats measures one instruction file's context over a range.
type instructionLoadStats struct {
	// Sessions are the conversations (sub-agents included, since each loads
	// its own instructions) that first loaded the file in the range; Requests
	// are all their model requests.
	Sessions int64 `json:"sessions"`
	Requests int64 `json:"requests"`
	// BytesFirst and BytesLast are the size loaded by the earliest and the
	// latest of those sessions that recorded one; zero when none did.
	BytesFirst int64 `json:"bytes_first"`
	BytesLast  int64 `json:"bytes_last"`
	// ByteRequests is the sum over sessions of bytes loaded times requests:
	// the bytes the file put in front of the model in the range.
	ByteRequests int64 `json:"byte_requests"`
}

// instructionLoad measures the context repoPath (a path relative to the
// repository root, such as CLAUDE.md or AGENTS.md) put in front of the model
// in repositoryID's sessions that first loaded it between from (inclusive) and
// to (exclusive), both in timeLayout; an empty bound is open. For a change
// copied at time T, call it for a window before and one after T: the change
// added (after.BytesFirst - before.BytesLast) bytes to each of after.Requests
// requests, and after.ByteRequests counts the file's whole cost after it. It
// reads through the repo_path index.
func (c *Catalog) instructionLoad(ctx context.Context, q queryer, repositoryID, repoPath, from, to string) (instructionLoadStats, error) {
	if q == nil {
		q = c.DB
	}
	var stats instructionLoadStats
	rows, err := queryMapsContext(ctx, q, `SELECT i.conversation_id,MAX(i.bytes) bytes,MIN(i.first_seen_at) first_seen,
		(SELECT COUNT(*) FROM model_requests r WHERE r.conversation_id=i.conversation_id) requests
		FROM conversation_instructions i JOIN conversations c ON c.id=i.conversation_id JOIN workspaces w ON w.id=c.workspace_id
		WHERE i.repo_path=? AND w.repository_id=? AND (?='' OR i.first_seen_at>=?) AND (?='' OR i.first_seen_at<?)
		GROUP BY i.conversation_id ORDER BY first_seen,i.conversation_id`, repoPath, repositoryID, from, from, to, to)
	if err != nil {
		return stats, err
	}
	sized := false
	for _, row := range rows {
		requests := integer(row["requests"])
		stats.Sessions++
		stats.Requests += requests
		if row["bytes"] == nil {
			continue
		}
		size := integer(row["bytes"])
		if !sized {
			stats.BytesFirst, sized = size, true
		}
		stats.BytesLast = size
		stats.ByteRequests += size * requests
	}
	return stats, nil
}
