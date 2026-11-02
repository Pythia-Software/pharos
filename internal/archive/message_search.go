package archive

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"strconv"
	"strings"
	"unicode"

	"modernc.org/sqlite"
)

const messageSearchVersion = "1"

func init() {
	sqlite.MustRegisterDeterministicScalarFunction("pharos_lexical_text", 2, func(_ *sqlite.FunctionContext, values []driver.Value) (driver.Value, error) {
		return messageLexicalText(sqliteText(values[0]), sqliteText(values[1])), nil
	})
}

// JSON fields are separate positional units. unicode61 treats private-use
// characters as tokens; this internal token prevents phrases from crossing
// strings, without adding a searchable word to the vocabulary. Query validation
// reserves it, and snippets remove it before returning evidence.
const lexicalBoundary = "\x1f\ue000\x1f"

type messageLexicalProjection struct {
	text       string
	insertedAt []int
}

func (p messageLexicalProjection) sourceOffset(offset int) int {
	removed := 0
	for _, at := range p.insertedAt {
		if offset <= at {
			break
		}
		removed += min(offset-at, len(lexicalBoundary))
	}
	return offset - removed
}

func messageLexicalText(kind, text string) string {
	return projectMessageLexicalText(kind, text).text
}

// Envelopes keep their canonical bytes for readers and accounting. Only the
// lexical index/verifier sees decoded whitespace separators and field barriers.
// Whitespace replacements keep their byte length; inserted barriers carry an
// offset map so search ranges still address the original stored evidence.
func projectMessageLexicalText(kind, text string) messageLexicalProjection {
	result := messageLexicalProjection{text: text}
	switch kind {
	case "tool_call", "tool_result", "delegation", "delegation_result", "context":
	default:
		return result
	}
	if !json.Valid([]byte(text)) {
		return result
	}
	var projected strings.Builder
	quoted, copied := false, 0
	for i := 0; i < len(text); i++ {
		if text[i] == '"' {
			quoted = !quoted
			if !quoted {
				projected.WriteString(text[copied : i+1])
				result.insertedAt = append(result.insertedAt, projected.Len())
				projected.WriteString(lexicalBoundary)
				copied = i + 1
			}
			continue
		}
		if !quoted || text[i] != '\\' || i+1 >= len(text) {
			continue
		}
		n, space := 2, false
		switch text[i+1] {
		case 'n', 'r', 't', 'b', 'f':
			space = true
		case 'u':
			if i+6 <= len(text) {
				n = 6
				value, err := strconv.ParseUint(text[i+2:i+6], 16, 16)
				space = err == nil && unicode.IsSpace(rune(value))
			}
		}
		if space {
			projected.WriteString(text[copied:i])
			projected.WriteString(strings.Repeat(" ", n))
			copied = i + n
		}
		// Escaped quotes/backslashes must not open another JSON string/escape.
		i += n - 1
	}
	if projected.Len() == 0 {
		return result
	}
	projected.WriteString(text[copied:])
	result.text = projected.String()
	return result
}

func lexicalSnippet(text string) string {
	return strings.NewReplacer("\x1f", "", "\ue000", "").Replace(text)
}

func (c *Catalog) pendingMessageSearch(ctx context.Context) (int64, error) {
	var count int64
	err := c.reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations c LEFT JOIN message_search_state s ON s.conversation_id=c.id
	 WHERE s.conversation_id IS NULL OR s.version<>?`, messageSearchVersion).Scan(&count)
	return count, err
}

// repairMessageSearch refreshes only envelope postings whose lexical form has
// changed. It never modifies canonical text, raw evidence or message identity,
// and never indexes tool output (see wordIndexedKind).
func repairMessageSearch(ctx context.Context, tx *sql.Tx, conversation string) error {
	rows, err := queryMapsContext(ctx, tx, `SELECT m.id FROM messages m
	 LEFT JOIN message_fts_rows r ON r.message_id=m.id LEFT JOIN messages_fts f ON f.rowid=r.fts_rowid
	 WHERE m.conversation_id=? AND m.kind IN ('tool_call','tool_result','delegation','delegation_result','context') AND `+wordIndexedKind("m.")+`
	 AND m.text<>'' AND pharos_lexical_text(m.kind,m.text) IS NOT f.text`, conversation)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := replaceMessageFTS(tx, firstString(row["id"])); err != nil {
			return err
		}
	}
	return recordMessageSearchState(ctx, tx, conversation)
}

func recordMessageSearchState(ctx context.Context, tx *sql.Tx, conversation string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO message_search_state(conversation_id,version) VALUES(?,?)
	 ON CONFLICT(conversation_id) DO UPDATE SET version=excluded.version WHERE version<>excluded.version`, conversation, messageSearchVersion)
	return err
}

// BackfillMessageSearch is an explicit, resumable Library upgrade. Each
// conversation and its completion marker commit together; startup and search
// do not scan or rebuild historical tool bodies.
func (c *Catalog) BackfillMessageSearch(ctx context.Context, progress func(int, int)) (int, error) {
	total, err := c.pendingMessageSearch(ctx)
	if err != nil {
		return 0, err
	}
	done := 0
	for {
		if err := ctx.Err(); err != nil {
			return done, err
		}
		var conversation, workspace string
		err := c.reader().QueryRowContext(ctx, `SELECT c.id,c.workspace_id FROM conversations c
		 LEFT JOIN message_search_state s ON s.conversation_id=c.id WHERE s.conversation_id IS NULL OR s.version<>? ORDER BY c.rowid LIMIT 1`, messageSearchVersion).Scan(&conversation, &workspace)
		if err == sql.ErrNoRows {
			return done, nil
		}
		if err != nil {
			return done, err
		}
		err = c.writeTransaction(ctx, "message-search-upgrade", func(tx *sql.Tx) error {
			var native string
			if err := tx.QueryRowContext(ctx, `SELECT c.native_id FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
			 WHERE c.id=? AND w.source_kind='claude' AND c.provider='claude'`, conversation).Scan(&native); err == nil {
				// A merge can invalidate only a descendant's marker. Its native
				// prefix still identifies the whole group that must be repaired.
				root, _, _ := strings.Cut(native, ":subagent:")
				if err := repairStoredClaudeLineage(ctx, tx, workspace, root); err != nil {
					return err
				}
			} else if err != sql.ErrNoRows {
				return err
			}
			if err := backfillHandbackMessages(ctx, tx, workspace, conversation); err != nil {
				return err
			}
			return repairMessageSearch(ctx, tx, conversation)
		})
		if err != nil {
			return done, err
		}
		done++
		if progress != nil {
			progress(done, int(total))
		}
	}
}

func backfillHandbackMessages(ctx context.Context, tx *sql.Tx, workspace, conversation string) error {
	var candidate bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages WHERE conversation_id=? AND kind='tool_call' AND instr(text,'SubagentHandback')>0)`, conversation).Scan(&candidate); err != nil || !candidate {
		return err
	}
	messages, err := storedMessages(ctx, tx, conversation)
	if err != nil {
		return err
	}
	normalized := normalizeHandbackMessages(messages)
	if len(normalized) == len(messages) {
		return nil
	}
	original := map[string]MessageRecord{}
	for _, message := range messages {
		original[message.NativeID] = message
	}
	for i, message := range normalized {
		message.SourceOrder = i
		normalized[i] = message
		if old, exists := original[message.NativeID]; exists && old == message {
			continue
		}
		id, changed, err := upsertMessage(tx, conversation, message)
		if err != nil {
			return err
		}
		if changed {
			if err := replaceMessageFTS(tx, id); err != nil {
				return err
			}
		}
	}
	var record ConversationRecord
	if err := tx.QueryRowContext(ctx, `SELECT provider,COALESCE(model,''),COALESCE(effort,''),COALESCE(started_at,''),agent_depth FROM conversations WHERE id=?`, conversation).
		Scan(&record.Provider, &record.Model, &record.Effort, &record.StartedAt, &record.AgentDepth); err != nil {
		return err
	}
	record.Messages = normalized
	if err := refreshAgentSessionMembership(tx, workspace, conversation, record); err != nil {
		return err
	}
	if err := upsertConversationDocument(tx, conversation); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM conversation_derivation_inputs WHERE conversation_id=?", conversation); err != nil {
		return err
	}
	return recordReasoningState(tx, conversation, normalized)
}
