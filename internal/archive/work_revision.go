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
// transaction as the messages and sessions it derives, and the repairs that
// rewrite canonical rows (search repair, reasoning order, capture-history
// recovery) delete it. The scheduler never writes that table: a revision is
// the conversation row, its agent sessions, an index-only summary of its
// message and session-membership rows, and the stored digest when there is
// one. The summary catches row inserts and deletes by writers that keep a
// digest (a library transfer, an older writer); an in-place edit by such a
// writer is not seen until the domain's own dirty state or the next ingest
// moves it. That is acceptable because a revision only keys scheduler memory
// (supersession, failure backoff); domain dirty marks decide what is pending
// and what may publish. Without a digest the revision hashes every message
// row, which is exact but reads overflow pages.
//
// Derived writers (authorship, labels, usage, speed, raw_text, session depth)
// touch none of these inputs, so publishing a result cannot supersede its own
// request.
const workRevisionVersion = "pharos-work-revision-v3"

// retiredWorkRevisionTriggers were installed lazily by development builds of
// the scheduled engine and deleted ingest's digests on every message write.
// Initialize removes them so classic and older binaries stop firing them.
var retiredWorkRevisionTriggers = []string{
	"work_revision_message_insert_v1", "work_revision_message_delete_v1", "work_revision_message_update_v1",
	"work_revision_session_message_insert_v1", "work_revision_session_message_delete_v1", "work_revision_session_message_update_v1",
}

func (c *Catalog) dropRetiredWorkRevisionTriggers() error {
	if c.readOnly || c.queryOnly {
		return nil
	}
	names := make([]any, len(retiredWorkRevisionTriggers))
	for i, name := range retiredWorkRevisionTriggers {
		names[i] = name
	}
	var n int
	if err := c.DB.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name IN ("+placeholders(len(names))+")", names...).Scan(&n); err != nil || n == 0 {
		return err
	}
	for _, name := range retiredWorkRevisionTriggers {
		if _, err := c.DB.Exec("DROP TRIGGER IF EXISTS " + name); err != nil {
			return err
		}
	}
	return nil
}

// workRevisionReader reads revisions inside one snapshot. Fallbacks counts
// live conversations whose revision needed the row hash.
type workRevisionReader struct {
	ctx       context.Context
	q         queryer
	Fallbacks int
}

func newWorkRevisionReader(ctx context.Context, q queryer) *workRevisionReader {
	return &workRevisionReader{ctx: ctx, q: q}
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

// revisionOf hashes the conversation row, its agent sessions, the row
// summary, and either the stored digest or every message and session-message
// row. A deleted conversation hashes empty inputs, so it stays a stable named
// member.
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
	// Both counts read only the conversation's index entries.
	if err := r.feed(h, `SELECT (SELECT count(*) FROM messages WHERE conversation_id=?1),(SELECT max(rowid) FROM messages WHERE conversation_id=?1),
 (SELECT count(*) FROM agent_session_messages am JOIN agent_sessions a ON a.id=am.agent_session_id WHERE a.conversation_id=?1)`, conversation.ID); err != nil {
		return "", err
	}
	_, _ = h.Write([]byte{0})
	if conversation.Fingerprint.Valid {
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

// workConversationRevisions reads several revisions; q should be a read
// transaction so before/after pairs are each consistent.
func workConversationRevisions(ctx context.Context, q queryer, ids []string) (map[string]string, error) {
	reader := newWorkRevisionReader(ctx, q)
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

// readWorkRevisions is the batch helper for runConversationWork: it reads
// every revision in one short read transaction.
func (c *Catalog) readWorkRevisions(ctx context.Context, ids []string) (map[string]string, error) {
	tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return workConversationRevisions(ctx, tx, ids)
}
