package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Session outcomes (docs/projects-and-outcomes.md): Tier 1 facts read from a
// session's own tool calls, which every harness that records tool calls
// gives, with or without Git, and the label they decide. A session is a
// workspace, a Library row: a Claude Code or Codex session, a Conductor
// workspace. Its delegated agents' calls are its own.
//
// The facts are derived with the tools stage, after the tool ledger, from
// the workspaces whose calls changed (session_outcome_dirty). Rework is
// judged across sessions through session_files, the files each session
// edited and fingerprints of the lines it added and removed
// (outcome_lines.go), so a new session re-judges only the earlier sessions
// that edited the same files within the window. Every marked session's facts
// come first and rework after (session_rework_dirty), so a session that many
// batches touch is judged once. The label is read from the stored facts,
// since a change stops being too recent to judge without any write.

// outcomesVersion derives every session again when its rules change.
const outcomesVersion = "4"

// defaultReworkDays is how long after a session's last edit a later session's
// edits to the same files count as rework (outcome_rework_days).
const defaultReworkDays = 14

// outcomeBatch is how many sessions one transaction derives.
var outcomeBatch = 100

// reworkShare is the share of the lines a session added and kept that later
// sessions must remove within the window for it to be reworked. Calibrated
// against a real library; see the design document.
const reworkShare = 0.5

// outcomeLabels are the labels in their order, most integrated first.
// Landed and Committed need Tier 2 evidence (local Git).
var outcomeLabels = []string{"landed", "committed", "changed_kept", "changed_recent", "reworked", "no_changes", "unknown"}

// toolRecordingSources are the source kinds whose sessions record tool calls,
// so a session of theirs without an edit changed no files. Any other source,
// such as a chat export, cannot tell, so its sessions are unknown.
var toolRecordingSources = []string{"claude", "codex", "conductor", "antigravity", "antigravity-cli", "tl1", "tl1-export"}

const outcomesSQL = `
CREATE TABLE IF NOT EXISTS session_files(
  workspace_id TEXT NOT NULL,
  file_key TEXT NOT NULL,
  path TEXT NOT NULL,
  repository_id TEXT,
  first_edit_at TEXT,
  last_edit_at TEXT,
  edits INTEGER NOT NULL,
  lines_added INTEGER,
  lines_removed INTEGER,
  lines_read INTEGER NOT NULL DEFAULT 0,
  kept_lines BLOB,
  removed_lines BLOB,
  rewrote INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(workspace_id,file_key)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS session_files_key_idx ON session_files(file_key,last_edit_at);
CREATE TABLE IF NOT EXISTS session_outcomes(
  workspace_id TEXT PRIMARY KEY,
  tool_calls_recorded INTEGER NOT NULL,
  files_changed INTEGER NOT NULL,
  lines_added INTEGER,
  lines_removed INTEGER,
  first_edit_at TEXT,
  last_edit_at TEXT,
  verification TEXT,
  verification_category TEXT,
  commits_json TEXT NOT NULL DEFAULT '[]',
  pushed INTEGER NOT NULL DEFAULT 0,
  pull_requests_json TEXT NOT NULL DEFAULT '[]',
  rework_days INTEGER NOT NULL,
  kept_lines INTEGER,
  reworked_files INTEGER NOT NULL DEFAULT 0,
  rework_removed_lines INTEGER,
  reworked_by_json TEXT NOT NULL DEFAULT '[]',
  heavily_reworked INTEGER NOT NULL DEFAULT 0) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS session_outcome_dirty(workspace_id TEXT PRIMARY KEY) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS session_rework_dirty(workspace_id TEXT PRIMARY KEY) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS session_outcome_mirrors(workspace_id TEXT PRIMARY KEY) WITHOUT ROWID;
`

// ensureOutcomes creates the outcome tables and the triggers that mark a
// session whose calls or workspace changed.
func (c *Catalog) ensureOutcomes() error {
	if _, err := c.DB.Exec(outcomesSQL); err != nil {
		return err
	}
	// An upsert, not INSERT OR IGNORE: the statement that fires a trigger
	// overrides an OR clause inside it, but not an ON CONFLICT clause.
	mark := func(id string) string {
		return "INSERT INTO session_outcome_dirty VALUES(" + id + ") ON CONFLICT DO NOTHING;"
	}
	for _, trigger := range []struct{ name, event, body string }{
		{"outcomes_tools_insert", "AFTER INSERT ON tool_calls", mark("NEW.workspace_id")},
		{"outcomes_tools_update", "AFTER UPDATE ON tool_calls", mark("OLD.workspace_id") + mark("NEW.workspace_id")},
		{"outcomes_tools_delete", "AFTER DELETE ON tool_calls", mark("OLD.workspace_id")},
		// A session without calls still has an outcome; a deleted one leaves
		// the sessions it reworked to be judged again.
		{"outcomes_workspaces_insert", "AFTER INSERT ON workspaces", mark("NEW.id")},
		{"outcomes_workspaces_delete", "AFTER DELETE ON workspaces", mark("OLD.id")},
	} {
		if _, err := c.DB.Exec("CREATE TRIGGER IF NOT EXISTS " + trigger.name + " " + trigger.event + " BEGIN " + trigger.body + " END"); err != nil {
			return err
		}
	}
	return nil
}

// reworkDays is the library's rework window.
func (c *Catalog) reworkDays() int {
	if c.ReworkDays > 0 {
		return c.ReworkDays
	}
	return defaultReworkDays
}

// outcomeCall is one tool call as outcomes read it.
type outcomeCall struct {
	Category, Status, CommandCategory, Command string
	StartedAt                                  string
	Path, RepoPath, RepositoryID, Scope        string
	LinesAdded, LinesRemoved                   *int64
	Input, Result                              string
}

type sessionFile struct {
	Key, Path, RepositoryID string
	First, Last             string
	Edits                   int
	Added, Removed          *int64
	Lines                   fileLines
}

type outcomeCommit struct {
	Hash   string `json:"hash"`
	Branch string `json:"branch,omitempty"`
	At     string `json:"at,omitempty"`
}

// sessionFacts are a session's Tier 1 facts.
type sessionFacts struct {
	Files                    []sessionFile
	Added, Removed           *int64
	FirstEdit, LastEdit      string
	Verification, VerifiedBy string
	Commits                  []outcomeCommit
	Pushed                   bool
	PullRequests             []string
}

var (
	// "[main 1a2b3c4] Subject", "[main (root-commit) 1a2b3c4]", "[detached HEAD 1a2b3c4]".
	commitLine = regexp.MustCompile(`(?m)^\[(.+?)(?: \(root-commit\))? ([0-9a-f]{7,40})\] `)
	// "   1a2b3c4..5d6e7f8  main -> main", " * [new branch]      feature -> feature".
	pushLine = regexp.MustCompile(`(?m)^\s*[+*\-=!]?\s*(?:[0-9a-f]{7,40}\.\.\.?[0-9a-f]{7,40}|\[new branch\]|\[new tag\]|\[new reference\])\s+\S+\s+->\s+\S+`)
	pullURL  = regexp.MustCompile(`https://github\.com/[\w.-]+/[\w.-]+/pull/\d+`)
)

// outcomeEdit reports whether a call changed a file of the session's work: a
// successful edit with a path, outside temporary and agent-home directories.
func outcomeEdit(call outcomeCall) bool {
	return call.Category == "edit" && call.Status == "ok" && call.Path != "" && call.Scope != "temp" && call.Scope != "agent_home"
}

// outcomeFileKey identifies a file across sessions: by repository and its path in
// the repository, so every checkout and worktree of a repository shares its
// files, else by absolute path.
func outcomeFileKey(call outcomeCall) (key, path string) {
	if call.RepositoryID != "" && call.RepoPath != "" {
		return "repository:" + call.RepositoryID + ":" + call.RepoPath, call.RepoPath
	}
	return "path:" + call.Path, defaultString(folderKey(call.Path), call.Path)
}

func addLines(total *int64, value *int64) *int64 {
	if value == nil {
		return total
	}
	sum := *value
	if total != nil {
		sum += *total
	}
	return &sum
}

// laterThan orders call times, which may be missing or differently formatted.
func laterThan(a, b string) bool {
	at, aok := parseTime(a)
	bt, bok := parseTime(b)
	if aok && bok {
		return at.After(bt)
	}
	return a > b
}

// fileResolver gives a path's identity as the tool ledger would: its path in
// its repository, the repository and its scope.
type fileResolver func(path string) (repoPath, repositoryID, scope string)

// deriveSessionFacts reads a session's facts from its calls, in call order.
// Each file an edit call changed counts on its own, so a patch's work files
// count when its first file is scratch, and a patch whose call has no file (a
// ledger before tools-v14 found none in a code-mode script) counts by the
// files it names. A
// patch's other files than the call's own are placed by resolve.
func deriveSessionFacts(calls []outcomeCall, resolve fileResolver) sessionFacts {
	facts := sessionFacts{}
	files := map[string]*sessionFile{}
	lastEdit := -1
	for index, call := range calls {
		if call.Category != "edit" || call.Status != "ok" {
			continue
		}
		edits := readEditChanges(call.Input, call.Path)
		if len(edits) == 0 && call.Path != "" {
			// Unreadable: the call's own file, without its lines.
			edits = []fileEdit{{Path: call.Path}}
		}
		for _, edit := range edits {
			target := call
			if call.Path == "" || filepath.Clean(edit.Path) != filepath.Clean(call.Path) {
				target.Path, target.RepoPath, target.RepositoryID, target.Scope = edit.Path, "", "", ""
				if resolve != nil {
					target.RepoPath, target.RepositoryID, target.Scope = resolve(edit.Path)
				}
			}
			if !outcomeEdit(target) {
				continue
			}
			lastEdit = index
			if edit.Added != nil {
				target.LinesAdded, target.LinesRemoved = edit.Added, edit.Removed
			}
			key, path := outcomeFileKey(target)
			file := files[key]
			if file == nil {
				file = &sessionFile{Key: key, Path: path, RepositoryID: target.RepositoryID, First: call.StartedAt}
				files[key] = file
			}
			file.Edits++
			if call.StartedAt != "" && (file.First == "" || laterThan(file.First, call.StartedAt)) {
				file.First = call.StartedAt
			}
			if call.StartedAt != "" && (file.Last == "" || laterThan(call.StartedAt, file.Last)) {
				file.Last = call.StartedAt
			}
			file.Added, file.Removed = addLines(file.Added, target.LinesAdded), addLines(file.Removed, target.LinesRemoved)
			if edit.Change.Added != nil {
				file.Lines.apply(edit.Change)
			}
		}
	}
	for _, file := range files {
		facts.Files = append(facts.Files, *file)
		facts.Added, facts.Removed = addLines(facts.Added, file.Added), addLines(facts.Removed, file.Removed)
		if file.First != "" && (facts.FirstEdit == "" || laterThan(facts.FirstEdit, file.First)) {
			facts.FirstEdit = file.First
		}
		if file.Last != "" && (facts.LastEdit == "" || laterThan(file.Last, facts.LastEdit)) {
			facts.LastEdit = file.Last
		}
	}
	slices.SortFunc(facts.Files, func(a, b sessionFile) int { return strings.Compare(a.Key, b.Key) })
	seen := map[string]bool{}
	for index, call := range calls {
		if lastEdit >= 0 && index > lastEdit && (call.CommandCategory == "test" || call.CommandCategory == "build") {
			facts.VerifiedBy = call.CommandCategory
			switch call.Status {
			case "ok":
				facts.Verification = "passed"
			case "error":
				facts.Verification = "failed"
			default:
				facts.Verification = "unknown"
			}
		}
		if call.Status != "ok" || call.Result == "" {
			continue
		}
		if strings.Contains(call.Command, "git") && strings.Contains(call.Command, "commit") {
			for _, match := range commitLine.FindAllStringSubmatch(call.Result, -1) {
				if !seen[match[2]] {
					seen[match[2]] = true
					branch := match[1]
					if branch == "detached HEAD" {
						branch = ""
					}
					facts.Commits = append(facts.Commits, outcomeCommit{Hash: match[2], Branch: branch, At: call.StartedAt})
				}
			}
		}
		if strings.Contains(call.Command, "git") && strings.Contains(call.Command, "push") && pushLine.MatchString(call.Result) {
			facts.Pushed = true
		}
		if strings.Contains(call.Command, "gh pr create") {
			for _, url := range pullURL.FindAllString(call.Result, -1) {
				if !slices.Contains(facts.PullRequests, url) {
					facts.PullRequests = append(facts.PullRequests, url)
				}
			}
		}
	}
	if len(facts.Files) > 0 && facts.Verification == "" {
		facts.Verification = "not_run"
	}
	return facts
}

// outcomeCallsSQL reads a session's calls in order: an edit's input, for its
// lines, and only the output of Git and gh commands, for their commit, push
// and pull request lines.
const outcomeCallsSQL = `SELECT t.tool_category,t.status,COALESCE(t.command_category,'') command_category,COALESCE(t.command,'') command,
	COALESCE(t.started_at,'') started_at,COALESCE(t.path_absolute,'') path_absolute,COALESCE(t.repo_path,'') repo_path,
	COALESCE(t.path_repository_id,'') path_repository_id,COALESCE(t.path_scope,'') path_scope,t.lines_added,t.lines_removed,
	CASE WHEN t.status='ok' AND (t.command LIKE '%git%commit%' OR t.command LIKE '%git%push%' OR t.command LIKE '%gh pr create%')
		THEN (SELECT r.text FROM messages r WHERE r.id=t.result_message_id) END result,
	CASE WHEN t.tool_category='edit' AND t.status='ok' THEN (SELECT m.text FROM messages m WHERE m.id=t.call_message_id) END input
	FROM tool_calls t WHERE t.workspace_id=? ORDER BY COALESCE(t.started_at,''),t.conversation_id,t.sequence`

func nullableInt64(value any) *int64 {
	if value == nil {
		return nil
	}
	number := integer(value)
	return &number
}

// toolResultContent is a stored result's text as the model read it: a
// result message keeps its content in a JSON envelope.
func toolResultContent(text string) string {
	var payload map[string]any
	if text == "" || json.Unmarshal([]byte(text), &payload) != nil || payload == nil {
		return text
	}
	content, _ := toolResultText(payload["content"])
	return content
}

func readOutcomeCalls(ctx context.Context, tx *sql.Tx, workspace string) ([]outcomeCall, error) {
	rows, err := queryMapsContext(ctx, tx, outcomeCallsSQL, workspace)
	if err != nil {
		return nil, err
	}
	calls := make([]outcomeCall, len(rows))
	for index, row := range rows {
		calls[index] = outcomeCall{Category: firstString(row["tool_category"]), Status: firstString(row["status"]), CommandCategory: firstString(row["command_category"]),
			Command: firstString(row["command"]), StartedAt: firstString(row["started_at"]), Path: firstString(row["path_absolute"]), RepoPath: firstString(row["repo_path"]),
			RepositoryID: firstString(row["path_repository_id"]), Scope: firstString(row["path_scope"]), LinesAdded: nullableInt64(row["lines_added"]),
			LinesRemoved: nullableInt64(row["lines_removed"]), Input: firstString(row["input"]), Result: toolResultContent(firstString(row["result"]))}
	}
	return calls, nil
}

// refreshSessionOutcomes derives the facts of the sessions marked since the
// last pass, and judges rework again for every session that edited one of
// their files. A new rule version or rework window derives every session.
func (c *Catalog) refreshSessionOutcomes(ctx context.Context) error {
	days := c.reworkDays()
	setting := outcomesVersion + ":" + strconv.Itoa(days)
	if err := c.writeTransaction(ctx, "session-outcomes-version", func(tx *sql.Tx) error {
		// The tool rollup publishes mirrors (tool_mirror_workspaces) whole,
		// so the workspaces whose membership changed since the last pass
		// are found against the mirrors it judged with. Deriving them again
		// judges the sessions sharing their files again.
		for _, statement := range []string{
			`INSERT OR IGNORE INTO session_outcome_dirty
				SELECT workspace_id FROM (SELECT workspace_id FROM tool_mirror_workspaces EXCEPT SELECT workspace_id FROM session_outcome_mirrors)
				UNION SELECT workspace_id FROM (SELECT workspace_id FROM session_outcome_mirrors EXCEPT SELECT workspace_id FROM tool_mirror_workspaces)`,
			"DELETE FROM session_outcome_mirrors WHERE workspace_id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces)",
			"INSERT OR IGNORE INTO session_outcome_mirrors SELECT workspace_id FROM tool_mirror_workspaces",
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		var stored string
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE((SELECT value FROM meta WHERE key='session_outcomes_version'),'')").Scan(&stored); err != nil || stored == setting {
			return err
		}
		for _, statement := range []string{"INSERT OR IGNORE INTO session_outcome_dirty SELECT id FROM workspaces",
			"INSERT OR IGNORE INTO session_outcome_dirty SELECT workspace_id FROM session_outcomes"} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO meta(key,value) VALUES('session_outcomes_version',?)", setting)
		return err
	}); err != nil {
		return err
	}
	for _, stage := range []struct {
		label, table string
		run          func(*sql.Tx, []string) error
	}{
		{"session-outcomes", "session_outcome_dirty", func(tx *sql.Tx, batch []string) error { return deriveOutcomeBatch(ctx, tx, batch, days) }},
		{"session-rework", "session_rework_dirty", func(tx *sql.Tx, batch []string) error {
			for _, workspace := range batch {
				if err := judgeRework(ctx, tx, workspace, days); err != nil {
					return err
				}
			}
			return nil
		}},
	} {
		for {
			done := false
			if err := c.writeTransaction(ctx, stage.label, func(tx *sql.Tx) error {
				rows, err := queryMapsContext(ctx, tx, "SELECT workspace_id FROM "+stage.table+" LIMIT ?", outcomeBatch)
				if err != nil {
					return err
				}
				if done = len(rows) == 0; done {
					return nil
				}
				batch := make([]string, len(rows))
				for index, row := range rows {
					batch[index] = firstString(row["workspace_id"])
				}
				if err := stage.run(tx, batch); err != nil {
					return err
				}
				_, err = tx.ExecContext(ctx, "DELETE FROM "+stage.table+" WHERE workspace_id IN (SELECT value FROM json_each(?))", jsonText(batch))
				return err
			}); err != nil {
				return err
			} else if done {
				break
			}
		}
	}
	return nil
}

// deriveOutcomeBatch derives the facts of a batch of marked sessions, then
// marks rework to judge again for them and for the earlier sessions sharing
// their files, before or after the change, whose window reaches it.
func deriveOutcomeBatch(ctx context.Context, tx *sql.Tx, batch []string, days int) error {
	ids := jsonText(batch)
	// The earliest edit of each file by the batch; a file without times
	// re-judges every session that edited it.
	earliest := map[string]sql.NullFloat64{}
	collect := func() error {
		rows, err := queryMapsContext(ctx, tx, `SELECT file_key,MIN(julianday(first_edit_at)) first,MAX(first_edit_at IS NULL OR julianday(first_edit_at) IS NULL) untimed
			FROM session_files WHERE workspace_id IN (SELECT value FROM json_each(?)) GROUP BY file_key`, ids)
		for _, row := range rows {
			key, first := firstString(row["file_key"]), sql.NullFloat64{}
			if value, ok := number(row["first"]); ok && integer(row["untimed"]) == 0 {
				first = sql.NullFloat64{Float64: value, Valid: true}
			}
			if seen, ok := earliest[key]; ok && (!seen.Valid || first.Valid && seen.Float64 < first.Float64) {
				first = seen
			}
			earliest[key] = first
		}
		return err
	}
	if err := collect(); err != nil {
		return err
	}
	for _, workspace := range batch {
		if err := deriveSessionOutcome(ctx, tx, workspace, days); err != nil {
			return err
		}
	}
	if err := collect(); err != nil {
		return err
	}
	affected := map[string]bool{}
	for _, workspace := range batch {
		affected[workspace] = true
	}
	for key, first := range earliest {
		rows, err := queryMapsContext(ctx, tx, `SELECT workspace_id FROM session_files WHERE file_key=?
			AND (? IS NULL OR last_edit_at IS NULL OR julianday(last_edit_at) IS NULL OR julianday(last_edit_at)+?>=?)`, key, first, days, first)
		if err != nil {
			return err
		}
		for _, row := range rows {
			affected[firstString(row["workspace_id"])] = true
		}
	}
	list := make([]string, 0, len(affected))
	for workspace := range affected {
		list = append(list, workspace)
	}
	_, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO session_rework_dirty SELECT value FROM json_each(?)", jsonText(list))
	return err
}

// deriveSessionOutcome stores one session's facts, without rework, or
// removes them when the session is gone.
func deriveSessionOutcome(ctx context.Context, tx *sql.Tx, workspace string, days int) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM session_files WHERE workspace_id=?", workspace); err != nil {
		return err
	}
	var source, activity string
	err := tx.QueryRowContext(ctx, "SELECT source_kind,COALESCE(activity_at,'') FROM workspaces WHERE id=?", workspace).Scan(&source, &activity)
	if err == sql.ErrNoRows {
		_, err = tx.ExecContext(ctx, "DELETE FROM session_outcomes WHERE workspace_id=?", workspace)
		return err
	} else if err != nil {
		return err
	}
	calls, err := readOutcomeCalls(ctx, tx, workspace)
	if err != nil {
		return err
	}
	var roots []repoRoot
	var rootsErr error
	resolved := false
	facts := deriveSessionFacts(calls, func(path string) (string, string, string) {
		if !resolved {
			resolved = true
			var locations []repoRoot
			if locations, rootsErr = toolRepositoryLocations(tx); rootsErr == nil {
				roots, rootsErr = toolRepoRoots(tx, workspace, locations)
			}
		}
		repoPath, repository, scope := resolveRepoPath(path, "", roots)
		return repoPath, repository.ID, scope
	})
	if rootsErr != nil {
		return rootsErr
	}
	var kept *int64
	for _, file := range facts.Files {
		if file.Lines.Read {
			kept = addLines(kept, new(int64(len(file.Lines.Kept))))
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_files(workspace_id,file_key,path,repository_id,first_edit_at,last_edit_at,edits,lines_added,lines_removed,
			lines_read,kept_lines,removed_lines,rewrote) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, workspace, file.Key, file.Path, nilIfEmpty(file.RepositoryID),
			nilIfEmpty(defaultString(file.First, activity)), nilIfEmpty(defaultString(file.Last, activity)), file.Edits, file.Added, file.Removed,
			boolInt(file.Lines.Read), packLines(file.Lines.Kept), packLines(file.Lines.Removed), boolInt(file.Lines.Rewrote)); err != nil {
			return err
		}
	}
	recorded := len(calls) > 0 || slices.Contains(toolRecordingSources, source)
	var first, last any
	if len(facts.Files) > 0 {
		// Without call times, the session's last activity stands in, so its
		// rework window still ends.
		first, last = nilIfEmpty(defaultString(facts.FirstEdit, activity)), nilIfEmpty(defaultString(facts.LastEdit, activity))
	}
	_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO session_outcomes(workspace_id,tool_calls_recorded,files_changed,lines_added,lines_removed,first_edit_at,last_edit_at,
		verification,verification_category,commits_json,pushed,pull_requests_json,rework_days,kept_lines) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		workspace, boolInt(recorded), len(facts.Files), facts.Added, facts.Removed, first, last, nilIfEmpty(facts.Verification), nilIfEmpty(facts.VerifiedBy),
		jsonText(defaultSlice(facts.Commits)), boolInt(facts.Pushed), jsonText(defaultSlice(facts.PullRequests)), days, kept)
	return err
}

// reworkedBy is how many sessions a session's outcome names as reworking it.
const reworkedByLimit = 20

// judgeRework judges which of the lines a session added and kept later
// sessions removed: a session that edited one of its files after it, starting
// within the window. It is reworked when they removed at least reworkShare of
// those lines. A mirror of another workspace (tool_mirror_workspaces) repeats
// that workspace's edits, so it is never rework.
func judgeRework(ctx context.Context, tx *sql.Tx, workspace string, days int) error {
	var kept sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT kept_lines FROM session_outcomes WHERE workspace_id=?", workspace).Scan(&kept); err == sql.ErrNoRows {
		return nil
	} else if err != nil {
		return err
	}
	rows, err := queryMapsContext(ctx, tx, `SELECT s.file_key,s.kept_lines,l.workspace_id,l.kept_lines later_kept,l.removed_lines later_removed,l.rewrote FROM session_files s
		JOIN session_files l ON l.file_key=s.file_key AND l.workspace_id<>s.workspace_id
		WHERE s.workspace_id=? AND s.kept_lines IS NOT NULL AND julianday(l.last_edit_at)>julianday(s.last_edit_at) AND julianday(l.first_edit_at)<=julianday(s.last_edit_at)+?
		AND l.workspace_id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces) ORDER BY s.file_key,l.workspace_id`, workspace, days)
	if err != nil {
		return err
	}
	files, gone, by := map[string]lineSet{}, map[string]lineSet{}, []string{}
	for _, row := range rows {
		key := firstString(row["file_key"])
		if files[key] == nil {
			files[key], gone[key] = unpackLines(bytesValue(row["kept_lines"])), lineSet{}
		}
		before := len(gone[key])
		removeLater(gone[key], files[key], bytesValue(row["later_kept"]), bytesValue(row["later_removed"]), integer(row["rewrote"]) == 1)
		if later := firstString(row["workspace_id"]); len(gone[key]) > before && !slices.Contains(by, later) {
			by = append(by, later)
		}
	}
	reworkedFiles, removed := 0, 0
	for _, lines := range gone {
		if len(lines) > 0 {
			reworkedFiles++
			removed += len(lines)
		}
	}
	heavy := kept.Valid && kept.Int64 > 0 && float64(removed) >= reworkShare*float64(kept.Int64)
	slices.Sort(by)
	if len(by) > reworkedByLimit {
		by = by[:reworkedByLimit]
	}
	var removedLines any
	if kept.Valid {
		removedLines = removed
	}
	_, err = tx.ExecContext(ctx, `UPDATE session_outcomes SET reworked_files=?,rework_removed_lines=?,reworked_by_json=?,heavily_reworked=? WHERE workspace_id=?`,
		reworkedFiles, removedLines, jsonText(by), boolInt(heavy), workspace)
	return err
}

func bytesValue(value any) []byte {
	switch typed := value.(type) {
	case []byte:
		return typed
	case string:
		return []byte(typed)
	}
	return nil
}

// outcomeLabelSQL is the label of workspace w's session, from its stored
// facts: a change younger than its rework window is too recent to judge.
func outcomeLabelSQL(w string) string {
	return `COALESCE((SELECT CASE WHEN so.tool_calls_recorded=0 THEN 'unknown' WHEN so.files_changed=0 THEN 'no_changes'
		WHEN so.heavily_reworked=1 THEN 'reworked' WHEN julianday(so.last_edit_at)>julianday('now')-so.rework_days THEN 'changed_recent'
		ELSE 'changed_kept' END FROM session_outcomes so WHERE so.workspace_id=` + w + `.id),'unknown')`
}

// outcomeWhySQL says what decided the label, and from which evidence tier.
func outcomeWhySQL(w string) string {
	return `COALESCE((SELECT CASE WHEN so.tool_calls_recorded=0 THEN 'This source records no tool calls, so its changes are unknown.'
		WHEN so.files_changed=0 THEN 'No files edited (transcript).'
		WHEN so.heavily_reworked=1 THEN printf('Later sessions removed %d of the %d lines it added within %d days (transcript).',so.rework_removed_lines,so.kept_lines,so.rework_days)
		WHEN julianday(so.last_edit_at)>julianday('now')-so.rework_days THEN printf('%d %s changed; rework can be judged after %s (transcript).',so.files_changed,
			CASE so.files_changed WHEN 1 THEN 'file' ELSE 'files' END,date(so.last_edit_at,printf('+%d days',so.rework_days)))
		WHEN so.kept_lines IS NULL THEN printf('%d %s changed; its edits could not be read line by line, so rework was not judged (transcript).',so.files_changed,
			CASE so.files_changed WHEN 1 THEN 'file' ELSE 'files' END)
		WHEN so.rework_removed_lines>0 THEN printf('%d %s changed; later sessions removed %d of the %d lines it added within %d days (transcript).',so.files_changed,
			CASE so.files_changed WHEN 1 THEN 'file' ELSE 'files' END,so.rework_removed_lines,so.kept_lines,so.rework_days)
		ELSE printf('%d %s changed; no later session removed its lines within %d days (transcript).',so.files_changed,CASE so.files_changed WHEN 1 THEN 'file' ELSE 'files' END,so.rework_days)
		END FROM session_outcomes so WHERE so.workspace_id=` + w + `.id),'Not derived yet.')`
}

// outcomeFactSQL reads one stored fact of workspace w's session.
func outcomeFactSQL(w, expr string) string {
	return "(SELECT " + expr + " FROM session_outcomes so WHERE so.workspace_id=" + w + ".id)"
}

// outcomesPending reports whether marked sessions wait for derivation.
func (c *Catalog) outcomesPending(ctx context.Context) (bool, error) {
	var pending bool
	err := c.reader().QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM session_outcome_dirty)").Scan(&pending)
	return pending, err
}
