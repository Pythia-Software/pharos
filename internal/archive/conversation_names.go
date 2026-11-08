package archive

// Conversation names cache original message excerpts, independently of the
// workspace's broader title and independently of human-word accounting. Reads
// use compact candidates; only the bounded maintenance pass reads message text.
import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const conversationNamesVersion = "conversation-names-v1/" + authorshipVersion

const conversationNamesDDL = `
CREATE TABLE IF NOT EXISTS conversation_names_state (
 conversation_id TEXT PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE,
 version TEXT NOT NULL DEFAULT '', dirty INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS conversation_names_pending ON conversation_names_state(conversation_id) WHERE dirty=1;
CREATE TABLE IF NOT EXISTS conversation_name_candidates (
 conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
 kind TEXT NOT NULL,
 title TEXT NOT NULL,
 message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
 content_hash TEXT NOT NULL,
 source_hash TEXT NOT NULL,
 sender TEXT NOT NULL,
 sent_at TEXT NOT NULL,
 workspace_id TEXT NOT NULL,
 version TEXT NOT NULL,
 PRIMARY KEY(conversation_id,kind)
);
CREATE INDEX IF NOT EXISTS conversation_name_candidates_message_idx ON conversation_name_candidates(message_id);
`

// The cached excerpt must still name the same unchanged, refined source.
// Explicitly compare sender/time/location too: a source hash in the classifier
// state may predate an edit until its normal maintenance pass catches up.
const currentConversationNameCandidate = `n.version='` + conversationNamesVersion + `' AND n.workspace_id=c.workspace_id
 AND n.content_hash=m.content_hash AND m.conversation_id=c.id
 AND n.sender=COALESCE(m.sender,'') AND n.sent_at=COALESCE(m.created_at,'') AND m.kind='message'
 AND (n.kind='assignment' AND m.role='agent' AND n.source_hash=''
 OR m.role='user' AND a.provisional=0 AND a.disposition='counted' AND a.version='` + authorshipVersion + `' AND n.source_hash=a.source_hash)`

// Only a workspace with one non-helper conversation can reuse that
// conversation's excerpt as its own fallback. A task's attempts stay separate.
const workspaceRequestCandidateSQL = `(SELECT json_object('title',n.title,'kind',n.kind) FROM conversations c
 JOIN conversation_name_candidates n ON n.conversation_id=c.id JOIN messages m ON m.id=n.message_id
 LEFT JOIN authorship_message_state a ON a.message_id=m.id
 WHERE c.workspace_id=w.id AND c.agent_depth=0 AND c.parent_id IS NULL AND instr(c.native_id,':subagent:')=0 AND ` + currentConversationNameCandidate + `
 AND (SELECT COUNT(*) FROM conversations r WHERE r.workspace_id=w.id AND r.agent_depth=0 AND r.parent_id IS NULL AND instr(r.native_id,':subagent:')=0)=1
 ORDER BY CASE n.kind WHEN 'typed' THEN 0 ELSE 1 END LIMIT 1)`

func (c *Catalog) ensureConversationNames() error {
	return c.writeTransaction(context.Background(), "initialize conversation names", func(tx *sql.Tx) error {
		if _, err := tx.Exec(conversationNamesDDL); err != nil {
			return err
		}
		// Triggers belong to the input transaction, including other writers.
		dirty := func(id string) string {
			return `INSERT INTO conversation_names_state(conversation_id) SELECT id FROM conversations WHERE id=` + id + `
 ON CONFLICT(conversation_id) DO UPDATE SET dirty=1 WHERE dirty=0;`
		}
		statements := []string{
			`CREATE TRIGGER IF NOT EXISTS conversation_names_conversation_insert AFTER INSERT ON conversations BEGIN ` + dirty("NEW.id") + ` END`,
			`CREATE TRIGGER IF NOT EXISTS conversation_names_conversation_update AFTER UPDATE OF workspace_id ON conversations
 WHEN OLD.workspace_id IS NOT NEW.workspace_id BEGIN ` + dirty("NEW.id") + ` END`,
		}
		for _, event := range []string{"INSERT", "DELETE", "UPDATE"} {
			row := "NEW"
			if event == "DELETE" {
				row = "OLD"
			}
			for _, table := range []string{"messages", "message_authorship", "authorship_message_state"} {
				id := row + ".conversation_id"
				when := ""
				if table == "authorship_message_state" {
					id = "(SELECT conversation_id FROM messages WHERE id=" + row + ".message_id)"
				}
				if table == "messages" {
					when = ` WHEN ` + row + `.kind='message' AND ` + row + `.role IN ('user','agent')`
					if event == "UPDATE" {
						when = ` WHEN (OLD.kind='message' AND OLD.role IN ('user','agent') OR NEW.kind='message' AND NEW.role IN ('user','agent'))
 AND (OLD.text IS NOT NEW.text OR OLD.content_hash IS NOT NEW.content_hash OR OLD.sender IS NOT NEW.sender
 OR OLD.created_at IS NOT NEW.created_at OR OLD.source_order IS NOT NEW.source_order OR OLD.conversation_id IS NOT NEW.conversation_id
 OR OLD.role IS NOT NEW.role OR OLD.kind IS NOT NEW.kind)`
					}
				}
				body := dirty(id)
				if table == "messages" && event == "UPDATE" {
					// Withdraw an edited originating message immediately, even
					// when a repair writer did not update its content hash.
					body += `DELETE FROM conversation_name_candidates WHERE message_id=NEW.id;`
				}
				if event == "UPDATE" && table != "authorship_message_state" {
					body += dirty("OLD.conversation_id")
				}
				statements = append(statements, `CREATE TRIGGER IF NOT EXISTS conversation_names_`+table+`_`+strings.ToLower(event)+` AFTER `+event+` ON `+table+when+` BEGIN `+body+` END`)
			}
		}
		for _, statement := range statements {
			if _, err := tx.Exec(statement); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS tl1_attempts_conversation_name_idx ON tl1_attempts(conversation_native_id) WHERE conversation_native_id IS NOT NULL`); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO conversation_names_state(conversation_id) SELECT id FROM conversations WHERE true
 ON CONFLICT(conversation_id) DO UPDATE SET dirty=1 WHERE version<>?`, conversationNamesVersion)
		return err
	})
}

type conversationNameCandidate struct {
	Kind, Title, Message, Hash, SourceHash, Sender, At, Workspace string
}

// No independent human-authorship classifier: only current, refined saved
// spans may supply human words. An agent-role message is a recorded assignment.
func conversationNameCandidates(ctx context.Context, q queryer, id string) ([]conversationNameCandidate, error) {
	rows, err := q.QueryContext(ctx, `SELECT m.id,m.text,m.role,COALESCE(m.sender,''),COALESCE(m.created_at,''),m.content_hash,
 c.workspace_id,a.spans_json,s.source_hash,s.version,s.provisional,s.disposition
 FROM messages m JOIN conversations c ON c.id=m.conversation_id
 LEFT JOIN message_authorship a ON a.message_id=m.id
 LEFT JOIN authorship_message_state s ON s.message_id=m.id
 WHERE m.conversation_id=? AND m.kind='message' AND +m.role IN ('user','agent')
 AND (m.role='agent' OR a.typed_words>0 OR a.automated_words>0)
 ORDER BY m.source_order IS NULL,m.source_order,m.created_at,m.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := map[string]conversationNameCandidate{}
	for rows.Next() {
		var message, text, role, sender, at, hash, workspace string
		var spansJSON, sourceHash, version, disposition sql.NullString
		var provisional sql.NullInt64
		if err := rows.Scan(&message, &text, &role, &sender, &at, &hash, &workspace, &spansJSON, &sourceHash, &version, &provisional, &disposition); err != nil {
			return nil, err
		}
		add := func(kind, title, source string) {
			if _, exists := found[kind]; !exists && meaningfulWorkspaceTitle(title, "") {
				found[kind] = conversationNameCandidate{kind, title, message, hash, source, sender, at, workspace}
			}
		}
		if role == "agent" {
			add("assignment", assignmentNameExcerpt(text), "")
		} else if version.String == authorshipVersion && provisional.Valid && provisional.Int64 == 0 && disposition.String == authorshipCounted {
			stamp, _ := parseTime(at)
			input := authorInput{ID: message, ConversationID: id, WorkspaceID: workspace, Text: text, Sender: sender, SentAt: stamp}
			var spans []authorSpan
			if sourceHash.String != authorshipSourceHash(input) || json.Unmarshal([]byte(spansJSON.String), &spans) != nil {
				continue
			}
			add("typed", authoredWorkspaceTitle(text, spans), sourceHash.String)
			parts := []string{}
			for _, span := range spans {
				if span.Category == spanAutomated && span.Start >= 0 && span.End >= span.Start && span.End <= len(text) {
					parts = append(parts, text[span.Start:span.End])
				}
			}
			add("assignment", assignmentNameExcerpt(strings.Join(parts, " ")), sourceHash.String)
		}
		if len(found) == 2 {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []conversationNameCandidate{}
	for _, kind := range []string{"typed", "assignment"} {
		if candidate, ok := found[kind]; ok {
			out = append(out, candidate)
		}
	}
	return out, nil
}

// Automated instructions may contain harness blocks, paths and command
// output. Share the existing block/token exclusions without changing spans or
// running the corpus classifier. Keep the first eligible prose paragraph.
func assignmentNameExcerpt(text string) string {
	_, text = splitHarnessText(text)
	if strings.HasPrefix(strings.TrimSpace(text), "# AGENTS.md instructions") || strings.HasPrefix(strings.TrimSpace(text), "gAAAA") {
		return ""
	}
	inFence := false
	for _, paragraph := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		lines := []string{}
		for _, line := range strings.Split(paragraph, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "```") {
				inFence = !inFence
				continue
			}
			if inFence || line == "" || machineLine(line) || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ">") || attachmentPattern.MatchString(line) {
				continue
			}
			lines = append(lines, line)
		}
		if title := nameExcerpt(strings.Join(lines, " ")); meaningfulWorkspaceTitle(title, "") {
			return title
		}
	}
	return ""
}

// One write transaction is also the input snapshot: an edit cannot interleave
// between candidate validation and publication. Clean rows are durable progress;
// a restart or an authorship refresh queues only the remaining/changed inputs.
func (c *Catalog) refreshConversationNames(ctx context.Context, batch int) (int, error) {
	count := 0
	err := c.writeTransaction(ctx, "name conversations", func(tx *sql.Tx) error {
		count = 0
		started := time.Now()
		rows, err := queryMapsContext(ctx, tx, `SELECT s.conversation_id,c.workspace_id FROM conversation_names_state s
 JOIN conversations c ON c.id=s.conversation_id WHERE s.dirty=1 ORDER BY s.conversation_id LIMIT ?`, max(1, batch))
		if err != nil {
			return err
		}
		workspaces := []string{}
		for _, row := range rows {
			if count > 0 && time.Since(started) >= time.Second {
				break
			}
			id := asString(row["conversation_id"])
			candidates, err := conversationNameCandidates(ctx, tx, id)
			if err != nil {
				return err
			}
			keep := []string{}
			for _, n := range candidates {
				keep = append(keep, n.Kind)
				if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_name_candidates
 (conversation_id,kind,title,message_id,content_hash,source_hash,sender,sent_at,workspace_id,version) VALUES(?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(conversation_id,kind) DO UPDATE SET title=excluded.title,message_id=excluded.message_id,content_hash=excluded.content_hash,
 source_hash=excluded.source_hash,sender=excluded.sender,sent_at=excluded.sent_at,workspace_id=excluded.workspace_id,version=excluded.version
 WHERE title IS NOT excluded.title OR message_id IS NOT excluded.message_id OR content_hash IS NOT excluded.content_hash
 OR source_hash IS NOT excluded.source_hash OR sender IS NOT excluded.sender OR sent_at IS NOT excluded.sent_at
 OR workspace_id IS NOT excluded.workspace_id OR version IS NOT excluded.version`,
					id, n.Kind, n.Title, n.Message, n.Hash, n.SourceHash, n.Sender, n.At, n.Workspace, conversationNamesVersion); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM conversation_name_candidates WHERE conversation_id=? AND kind NOT IN (SELECT value FROM json_each(?))`, id, jsonText(keep)); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE conversation_names_state SET dirty=0,version=? WHERE conversation_id=?`, conversationNamesVersion, id); err != nil {
				return err
			}
			count++
			workspaces = append(workspaces, asString(row["workspace_id"]))
		}
		if len(workspaces) > 0 {
			return refreshWorkspaceNames(tx, workspaces)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("conversation names: %w", err)
	}
	return count, nil
}

// loadExecutionNames reads names, never message bodies. Optional cache tables
// keep earlier offline snapshots readable; they simply have fewer candidates.
func loadExecutionNames(ctx context.Context, q queryer, scope []string, out *executionSourceRows) error {
	// Schema-7 labeling snapshots can predate the workspace naming ledger.
	// Their missing name evidence must not make execution identity unreadable.
	namingTables, err := queryMapsContext(ctx, q, `SELECT name FROM sqlite_schema WHERE type='table' AND name='workspace_names'`)
	if err != nil || len(namingTables) == 0 {
		return err
	}
	where, args := executionScope("c.id", scope)
	rows, err := queryMapsContext(ctx, q, `SELECT c.id,w.title,w.source_id,w.source_kind,n.display_source,n.override_title,n.source_scope,
 f.source_kind from_kind,fn.source_scope from_scope,
 (SELECT COUNT(*) FROM conversations r WHERE r.workspace_id=w.id AND r.agent_depth=0 AND instr(r.native_id,':subagent:')=0
 AND (r.parent_id IS NULL OR r.provider='codex')) root_count
 FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
 LEFT JOIN workspace_names n ON n.workspace_id=w.id
 LEFT JOIN workspaces f ON f.id=n.display_workspace_id LEFT JOIN workspace_names fn ON fn.workspace_id=f.id
 WHERE c.agent_depth=0 AND instr(c.native_id,':subagent:')=0`+where, args...)
	if err != nil {
		return err
	}
	for _, row := range rows {
		cv := out.conversations[asString(row["id"])]
		if cv == nil || cv.depth > 0 || strings.Contains(cv.native, ":subagent:") {
			continue
		}
		title, sourceID := asString(row["title"]), asString(row["source_id"])
		if !meaningfulWorkspaceTitle(title, sourceID) {
			continue
		}
		kind, scope := asString(row["source_kind"]), asString(row["source_scope"])
		origin := asString(row["display_source"])
		broad := kind == "tl1" || kind == "tl1-export" || scope == "workspace" || scope == "task"
		// An excerpt displayed on a broad workspace is still an excerpt,
		// not independent task context. Its validated candidate below owns
		// freshness; otherwise an edited/deleted request could survive here.
		if broad && origin != "typed" && origin != "assignment" {
			cv.contextName = nameExcerpt(title)
		}
		if integer(row["root_count"]) != 1 {
			continue
		}
		if origin == "override" || meaningfulWorkspaceTitle(asString(row["override_title"]), sourceID) {
			cv.name, cv.nameSource = title, "override"
		} else if !broad && (origin == "source" || origin == "" || origin == "linked" && asString(row["from_kind"]) != "tl1" && asString(row["from_kind"]) != "tl1-export" && asString(row["from_scope"]) != "workspace" && asString(row["from_scope"]) != "task") {
			cv.name, cv.nameSource = title, "source"
			nameKind := kind
			if origin == "linked" {
				nameKind = asString(row["from_kind"])
			}
			if nameKind == "conductor" {
				cv.nameRank = 1
			}
		}
	}
	tables, err := queryMapsContext(ctx, q, `SELECT name FROM sqlite_schema WHERE type='table' AND name IN ('tl1_attempts','conversation_name_candidates')`)
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for _, table := range tables {
		present[asString(table["name"])] = true
	}
	if present["tl1_attempts"] {
		rows, err := queryMapsContext(ctx, q, `SELECT c.id,MIN(a.attempt_number) attempt_number
 FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
 JOIN tl1_attempts a ON a.conversation_native_id=c.native_id
 WHERE w.source_kind='tl1' AND w.source_id=a.installation_id||':'||a.task_id`+where+`
 GROUP BY c.id HAVING COUNT(*)=1`, args...)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if cv := out.conversations[asString(row["id"])]; cv != nil && cv.contextName != "" && row["attempt_number"] != nil {
				cv.contextName += fmt.Sprintf(" · attempt %d", integer(row["attempt_number"]))
			}
		}
	}
	if !present["conversation_name_candidates"] {
		return nil
	}
	rows, err = queryMapsContext(ctx, q, `SELECT n.conversation_id,n.kind,n.title
 FROM conversation_name_candidates n JOIN conversations c ON c.id=n.conversation_id
 JOIN messages m ON m.id=n.message_id AND m.conversation_id=c.id
 LEFT JOIN authorship_message_state a ON a.message_id=m.id
 WHERE `+currentConversationNameCandidate+where, args...)
	if err != nil {
		return err
	}
	for _, row := range rows {
		cv := out.conversations[asString(row["conversation_id"])]
		if cv == nil {
			continue
		}
		if title := asString(row["title"]); meaningfulWorkspaceTitle(title, cv.native) {
			if row["kind"] == "typed" {
				cv.humanName = title
			} else {
				cv.requestName = title
			}
		}
	}
	return nil
}
