package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"strings"
)

// Work revisions identify a conversation's canonical framing inputs without
// rereading its transcript. Ingest already stores the digest of the delivered
// normalized record in conversation_derivation_inputs, in the same
// transaction as the messages and sessions it derives. That digest is only an
// input identity while every later canonical edit removes it, so these
// triggers delete the conversation's row when a column the revision covers
// changes. Supported ingest writes it again at the end of its transaction;
// repairs that already delete it are unaffected. Derived writers (authorship,
// labels, usage, speed, raw_text, session depth) touch none of these columns,
// so publishing a result cannot supersede its own request.
//
// The triggers are installed lazily under admission, never by Initialize: no
// schema floor changes, and older binaries keep them (their Initialize drops
// only the trigger families it owns) and fire them on their own writes. A
// snapshot trusts a stored digest only when all of them are visible in that
// same snapshot; otherwise the revision falls back to hashing the rows, which
// is exact but reads every message row, overflow pages included.
//
// Agent sessions are read directly (a few small rows per conversation), so
// lineage relinking does not discard the digest, and the conversation row is
// hashed directly, so metadata edits need no trigger.
const workRevisionVersion = "pharos-work-revision-v2"

const workRevisionMessageColumns = "id,conversation_id,native_id,role,kind,created_at,text,content_hash,selected,sender,source_order,reasoning_format,call_id"

// The trigger names are versioned; a changed definition takes a new name.
var workRevisionTriggers = []struct{ name, sql string }{
	{"work_revision_message_insert_v1", `CREATE TRIGGER IF NOT EXISTS work_revision_message_insert_v1 AFTER INSERT ON messages BEGIN
 DELETE FROM conversation_derivation_inputs WHERE conversation_id=NEW.conversation_id; END`},
	{"work_revision_message_delete_v1", `CREATE TRIGGER IF NOT EXISTS work_revision_message_delete_v1 AFTER DELETE ON messages BEGIN
 DELETE FROM conversation_derivation_inputs WHERE conversation_id=OLD.conversation_id; END`},
	// Text and hash change together; text is compared only for old rows without
	// a hash, matching what the fallback hashes.
	{"work_revision_message_update_v1", `CREATE TRIGGER IF NOT EXISTS work_revision_message_update_v1 AFTER UPDATE OF ` + workRevisionMessageColumns + ` ON messages
 WHEN OLD.id IS NOT NEW.id OR OLD.conversation_id IS NOT NEW.conversation_id OR OLD.native_id IS NOT NEW.native_id OR OLD.role IS NOT NEW.role
 OR OLD.kind IS NOT NEW.kind OR OLD.created_at IS NOT NEW.created_at OR OLD.content_hash IS NOT NEW.content_hash OR OLD.selected IS NOT NEW.selected
 OR OLD.sender IS NOT NEW.sender OR OLD.source_order IS NOT NEW.source_order OR OLD.reasoning_format IS NOT NEW.reasoning_format OR OLD.call_id IS NOT NEW.call_id
 OR (NEW.content_hash='' AND OLD.text IS NOT NEW.text)
 BEGIN DELETE FROM conversation_derivation_inputs WHERE conversation_id IN (OLD.conversation_id,NEW.conversation_id); END`},
	{"work_revision_session_message_insert_v1", `CREATE TRIGGER IF NOT EXISTS work_revision_session_message_insert_v1 AFTER INSERT ON agent_session_messages BEGIN
 DELETE FROM conversation_derivation_inputs WHERE conversation_id=(SELECT conversation_id FROM agent_sessions WHERE id=NEW.agent_session_id); END`},
	{"work_revision_session_message_delete_v1", `CREATE TRIGGER IF NOT EXISTS work_revision_session_message_delete_v1 AFTER DELETE ON agent_session_messages BEGIN
 DELETE FROM conversation_derivation_inputs WHERE conversation_id=(SELECT conversation_id FROM agent_sessions WHERE id=OLD.agent_session_id); END`},
	{"work_revision_session_message_update_v1", `CREATE TRIGGER IF NOT EXISTS work_revision_session_message_update_v1 AFTER UPDATE OF agent_session_id,message_id ON agent_session_messages
 WHEN OLD.agent_session_id IS NOT NEW.agent_session_id OR OLD.message_id IS NOT NEW.message_id
 BEGIN DELETE FROM conversation_derivation_inputs WHERE conversation_id IN (SELECT conversation_id FROM agent_sessions WHERE id IN (OLD.agent_session_id,NEW.agent_session_id)); END`},
}

func workRevisionTriggersPresent(ctx context.Context, q queryer) (bool, error) {
	names := make([]any, len(workRevisionTriggers))
	for i, trigger := range workRevisionTriggers {
		names[i] = trigger.name
	}
	rows, err := q.QueryContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name IN ("+placeholders(len(names))+")", names...)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	var n int
	if rows.Next() {
		if err := rows.Scan(&n); err != nil {
			return false, err
		}
	}
	return n == len(workRevisionTriggers), rows.Err()
}

// ensureWorkRevisionInputs installs the invalidation triggers once per
// catalog. Call it from an admitted phase before the snapshot that should
// trust stored digests. A read-only catalog keeps the exact fallback.
// Digests stored before installation may describe older rows than the current
// ones; that is harmless, because a revision only has to change when its
// inputs change after it was observed, and from here on every such change
// removes the digest.
func (c *Catalog) ensureWorkRevisionInputs(ctx context.Context) error {
	present, err := workRevisionTriggersPresent(ctx, c.reader())
	if err != nil || present || c.readOnly || c.queryOnly {
		return err
	}
	return c.writeTransaction(ctx, "work revision inputs", func(tx *sql.Tx) error {
		for _, trigger := range workRevisionTriggers {
			if _, err := tx.ExecContext(ctx, trigger.sql); err != nil {
				return err
			}
		}
		return nil
	})
}

// workRevisionReader reads revisions inside one snapshot. trusted records
// whether that snapshot had the triggers; Fallbacks counts live conversations
// whose revision needed the row hash.
type workRevisionReader struct {
	ctx       context.Context
	q         queryer
	trusted   bool
	Fallbacks int
}

func newWorkRevisionReader(ctx context.Context, q queryer) (*workRevisionReader, error) {
	trusted, err := workRevisionTriggersPresent(ctx, q)
	if err != nil {
		return nil, err
	}
	return &workRevisionReader{ctx: ctx, q: q, trusted: trusted}, nil
}

const workRevisionConversationColumns = "c.id,c.workspace_id,c.provider,c.account,c.native_id,c.parent_id,c.agent_depth,c.started_at,c.ended_at,c.harness,c.model"

// workRevisionConversation is the conversation row and its stored digest, as
// read by the membership scan or a point lookup.
type workRevisionConversation struct {
	ID          string
	Row         []any // nil for a deleted conversation
	Fingerprint sql.NullString
}

func (r *workRevisionReader) lookup(id string) (workRevisionConversation, error) {
	out := workRevisionConversation{ID: id}
	rows, err := r.q.QueryContext(r.ctx, `SELECT `+workRevisionConversationColumns+`,d.fingerprint FROM conversations c
 LEFT JOIN conversation_derivation_inputs d ON d.conversation_id=c.id WHERE c.id=?`, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	if rows.Next() {
		if out, err = scanWorkRevisionConversation(rows); err != nil {
			return out, err
		}
	}
	return out, rows.Err()
}

func scanWorkRevisionConversation(rows *sql.Rows) (workRevisionConversation, error) {
	values := make([]any, strings.Count(workRevisionConversationColumns, ",")+1)
	var fingerprint sql.NullString
	pointers := make([]any, len(values)+1)
	for i := range values {
		pointers[i] = &values[i]
	}
	pointers[len(values)] = &fingerprint
	if err := rows.Scan(pointers...); err != nil {
		return workRevisionConversation{}, err
	}
	id, _ := values[0].(string)
	return workRevisionConversation{ID: id, Row: values, Fingerprint: fingerprint}, nil
}

func (r *workRevisionReader) revision(id string) (string, error) {
	conversation, err := r.lookup(id)
	if err != nil {
		return "", err
	}
	return r.revisionOf(conversation)
}

// revisionOf hashes the conversation row, its agent sessions, and either the
// trusted stored digest or every message and session-message row. A deleted
// conversation hashes empty inputs, so it stays a stable named member.
func (r *workRevisionReader) revisionOf(conversation workRevisionConversation) (string, error) {
	h := sha256.New()
	_, _ = h.Write([]byte(workRevisionVersion + "\x00"))
	if conversation.Row != nil {
		if err := writeWorkRevisionRow(r.ctx, h, conversation.Row); err != nil {
			return "", err
		}
	}
	_, _ = h.Write([]byte{0})
	if err := r.feed(h, `SELECT id,native_id,kind,child_conversation_id,delegation_message_id FROM agent_sessions WHERE conversation_id=? ORDER BY id`, conversation.ID); err != nil {
		return "", err
	}
	_, _ = h.Write([]byte{0})
	if r.trusted && conversation.Fingerprint.Valid {
		fmt.Fprintf(h, "digest:%s", conversation.Fingerprint.String)
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	if conversation.Row != nil {
		r.Fallbacks++
	}
	_, _ = h.Write([]byte("rows:"))
	for _, query := range []string{
		`SELECT id,native_id,role,kind,created_at,CASE WHEN content_hash<>'' THEN content_hash ELSE text END,selected,sender,source_order,reasoning_format,call_id FROM messages WHERE conversation_id=? ORDER BY id`,
		`SELECT am.agent_session_id,am.message_id FROM agent_session_messages am JOIN agent_sessions a ON a.id=am.agent_session_id WHERE a.conversation_id=? ORDER BY am.agent_session_id,am.message_id`,
	} {
		if err := r.feed(h, query, conversation.ID); err != nil {
			return "", err
		}
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeWorkRevisionRow(ctx context.Context, h hash.Hash, values []any) error {
	data, err := json.Marshal(values)
	if err != nil {
		return err
	}
	taskResourcesFrom(ctx).addRead(int64(len(data)))
	fmt.Fprintf(h, "%d:", len(data))
	_, _ = h.Write(data)
	return nil
}

func (r *workRevisionReader) feed(h hash.Hash, query string, args ...any) error {
	rows, err := r.q.QueryContext(r.ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	values := make([]any, len(cols))
	pointers := make([]any, len(cols))
	for i := range values {
		pointers[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(pointers...); err != nil {
			return err
		}
		if err := writeWorkRevisionRow(r.ctx, h, values); err != nil {
			return err
		}
	}
	return rows.Err()
}

// workConversationRevisions reads several revisions with one trigger check;
// q should be a read transaction so before/after pairs are each consistent.
func workConversationRevisions(ctx context.Context, q queryer, ids []string) (map[string]string, error) {
	reader, err := newWorkRevisionReader(ctx, q)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(ids))
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		revision, err := reader.revision(id)
		if err != nil {
			return nil, err
		}
		result[id] = revision
	}
	return result, nil
}

// readWorkRevisions is the batch helper for runConversationWork: it makes sure
// the triggers exist (an admitted batch is the place for that one-time write),
// then reads every revision in one short read transaction.
func (c *Catalog) readWorkRevisions(ctx context.Context, ids []string) (map[string]string, error) {
	if err := c.ensureWorkRevisionInputs(ctx); err != nil {
		return nil, err
	}
	tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return workConversationRevisions(ctx, tx, ids)
}
