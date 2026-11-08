package archive

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const fingerprintCacheDDL = `
CREATE TABLE IF NOT EXISTS fingerprint_entities (
 entity TEXT NOT NULL CHECK(entity IN ('conversation','workspace')), id TEXT NOT NULL,
 revision TEXT NOT NULL, dirty INTEGER NOT NULL DEFAULT 1,
 events_mask INTEGER NOT NULL DEFAULT 1, priority INTEGER NOT NULL DEFAULT 0,
 attempts INTEGER NOT NULL DEFAULT 0, next_attempt INTEGER NOT NULL DEFAULT 0,
 last_error TEXT NOT NULL DEFAULT '', PRIMARY KEY(entity,id));
CREATE INDEX IF NOT EXISTS fingerprint_pending ON fingerprint_entities(priority DESC,next_attempt,entity,id) WHERE dirty=1;
CREATE TABLE IF NOT EXISTS fingerprint_blobs (hash TEXT PRIMARY KEY, png BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS fingerprint_images (
 entity TEXT NOT NULL, entity_id TEXT NOT NULL, profile TEXT NOT NULL,
 theme TEXT NOT NULL, events INTEGER NOT NULL, revision TEXT NOT NULL,
 renderer TEXT NOT NULL, hash TEXT NOT NULL REFERENCES fingerprint_blobs(hash),
 PRIMARY KEY(entity,entity_id,profile,theme,events),
 FOREIGN KEY(entity,entity_id) REFERENCES fingerprint_entities(entity,id) ON DELETE CASCADE);
CREATE INDEX IF NOT EXISTS fingerprint_image_hash ON fingerprint_images(hash);
CREATE TABLE IF NOT EXISTS fingerprint_orphans (hash TEXT PRIMARY KEY, not_before INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS fingerprint_orphan_age ON fingerprint_orphans(not_before);
CREATE TRIGGER IF NOT EXISTS fingerprint_image_removed AFTER DELETE ON fingerprint_images BEGIN
 INSERT INTO fingerprint_orphans(hash,not_before) VALUES(OLD.hash,unixepoch()+3600) ON CONFLICT(hash) DO UPDATE SET not_before=excluded.not_before;
END;
CREATE TRIGGER IF NOT EXISTS fingerprint_image_replaced AFTER UPDATE OF hash ON fingerprint_images WHEN OLD.hash<>NEW.hash BEGIN
 INSERT INTO fingerprint_orphans(hash,not_before) VALUES(OLD.hash,unixepoch()+3600) ON CONFLICT(hash) DO UPDATE SET not_before=excluded.not_before;
END;`

func (c *Catalog) ensureFingerprintImages(ctx context.Context) error {
	return c.writeTransaction(ctx, "initialize fingerprint images", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, fingerprintCacheDDL); err != nil {
			return err
		}
		if err := fingerprintSourceTriggers(ctx, tx); err != nil {
			return err
		}
		var previous string
		_ = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='fingerprint_renderer'`).Scan(&previous)
		version := fingerprintRendererVersion()
		if previous == version {
			return nil
		}
		// Bootstrap metadata only. Never render or discard the last good PNGs
		// during library open; the background queue resumes the backfill.
		if _, err := tx.ExecContext(ctx, `INSERT INTO fingerprint_entities(entity,id,revision) SELECT 'conversation',id,lower(hex(randomblob(16))) FROM conversations WHERE 1 ON CONFLICT(entity,id) DO NOTHING;
INSERT INTO fingerprint_entities(entity,id,revision) SELECT 'workspace',id,lower(hex(randomblob(16))) FROM workspaces WHERE 1 ON CONFLICT(entity,id) DO NOTHING;`); err != nil {
			return err
		}
		if same := fingerprintSameImages[version]; slices.Contains(same, previous) {
			if err := adoptFingerprintImages(ctx, tx, version, same); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE fingerprint_entities SET dirty=1,attempts=0,next_attempt=0,last_error=''`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('fingerprint_renderer',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, version)
		return err
	})
}

// adoptFingerprintImages stamps images an earlier, identical renderer drew as
// the current renderer's, and settles each queued entity whose images are
// then complete at its revision. Only a version change queues an entity with
// complete current images, so this undoes that requeue and nothing else: an
// entity whose sources changed has images of an older revision and stays queued.
func adoptFingerprintImages(ctx context.Context, tx *sql.Tx, version string, same []string) error {
	marks := strings.TrimSuffix(strings.Repeat("?,", len(same)), ",")
	args := []any{version}
	for _, earlier := range same {
		args = append(args, earlier)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE fingerprint_images SET renderer=? WHERE renderer IN (`+marks+`)`, args...); err != nil {
		return err
	}
	complete := func(events int) string {
		return `(SELECT COUNT(*) FROM fingerprint_images i WHERE i.entity=fingerprint_entities.entity AND i.entity_id=fingerprint_entities.id AND i.events=` + strconv.Itoa(events) + ` AND i.revision=fingerprint_entities.revision AND i.renderer=?1)=?2`
	}
	// events_mask bit 1 asks for the event appearance (events=1), bit 2 for the
	// event-hidden one (events=0), as publishFingerprintImages counts them.
	_, err := tx.ExecContext(ctx, `UPDATE fingerprint_entities SET dirty=0,priority=0,attempts=0,next_attempt=0,last_error=''
WHERE dirty=1 AND (events_mask&1=0 OR `+complete(1)+`) AND (events_mask&2=0 OR `+complete(0)+`)`, version, fingerprintImagesPerAppearance())
	return err
}

// Invalidation is in the source transaction, including writes by another
// process. Random generation tokens also protect deletion/recreation of an ID.
// Optional labeling tables get these triggers when their ledger is created.
func fingerprintSourceTriggers(ctx context.Context, tx *sql.Tx) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name='fingerprint_entities'`).Scan(&exists); err != nil || exists == 0 {
		return err
	}
	upsert := ` ON CONFLICT(entity,id) DO UPDATE SET revision=excluded.revision,dirty=1,attempts=0,next_attempt=unixepoch()+2,last_error='';`
	workspace := func(id string) string {
		return `INSERT INTO fingerprint_entities(entity,id,revision) SELECT 'workspace',id,lower(hex(randomblob(16))) FROM workspaces WHERE id=` + id + upsert
	}
	conversation := func(id string) string {
		return `INSERT INTO fingerprint_entities(entity,id,revision) SELECT 'conversation',id,lower(hex(randomblob(16))) FROM conversations WHERE id=` + id + upsert +
			workspace(`(SELECT workspace_id FROM conversations WHERE id=`+id+`)`)
	}
	type source struct {
		table, key string
		fields     []string
	}
	sources := []source{
		{"conversations", "id", []string{"id", "workspace_id", "provider", "started_at", "agent_depth", "agent_nickname", "parent_id"}},
		{"workspaces", "id", []string{"id", "title"}},
		{"messages", "conversation_id", []string{"id", "conversation_id", "selected", "kind", "role", "text", "content_hash", "created_at", "source_order"}},
		{"tool_calls", "conversation_id", []string{"id", "conversation_id", "agent_session_id", "call_message_id", "tool_name", "tool_category", "command_category", "status", "started_at", "ended_at", "duration_ms", "sequence"}},
		{"agent_sessions", "conversation_id", nil},
		{"agent_session_messages", "(SELECT conversation_id FROM messages WHERE id=%s.message_id)", nil},
		{"agent_events", "conversation_id", nil},
		{"agent_event_messages", "conversation_id", nil},
		// Marking a conversation's events stale, the run that clears the
		// mark, and its events build stamp (whose execution digest decides
		// whether they are withheld) change what its ribbon may draw
		// (fingerprintExecutionPending); message_labels_conversations is the
		// labels' stamp.
		{"agent_events_execution_stale", "conversation_id", nil},
		{"agent_events_conversations", "conversation_id", nil},
		{"message_labels", "conversation_id", nil},
		{"message_labels_conversations", "conversation_id", nil},
		{"message_labels_dirty", "conversation_id", nil},
		{"message_labels_models", "*", nil},
	}
	for _, source := range sources {
		columns, err := queryMapsContext(ctx, tx, `PRAGMA table_info(`+sqlIdentifier(source.table)+`)`)
		if err != nil {
			return err
		}
		if len(columns) == 0 {
			continue
		}
		fields := source.fields
		if fields == nil {
			for _, column := range columns {
				fields = append(fields, firstString(column["name"]))
			}
		}
		known := map[string]bool{}
		for _, column := range columns {
			known[firstString(column["name"])] = true
		}
		changed := []string{}
		for _, field := range fields {
			if known[field] {
				name := sqlIdentifier(field)
				changed = append(changed, "OLD."+name+" IS NOT NEW."+name)
			}
		}
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			name := sqlIdentifier("fingerprint_source_" + source.table + "_" + event)
			body := ""
			refs := []string{"NEW"}
			if event == "DELETE" {
				refs = []string{"OLD"}
			}
			if event == "UPDATE" {
				refs = []string{"OLD", "NEW"}
			}
			if event == "DELETE" && (source.table == "conversations" || source.table == "workspaces") {
				entity := "conversation"
				if source.table == "workspaces" {
					entity = "workspace"
				}
				body = `DELETE FROM fingerprint_entities WHERE entity='` + entity + `' AND id=OLD.id;`
				if entity == "conversation" {
					body += workspace("OLD.workspace_id")
				}
			} else {
				for _, ref := range refs {
					key := ref + "." + source.key
					if strings.Contains(source.key, "%s") {
						key = fmt.Sprintf(source.key, ref)
					}
					switch source.table {
					case "workspaces":
						body += workspace(key)
					case "message_labels_models":
						body += `UPDATE fingerprint_entities SET revision=lower(hex(randomblob(16))),dirty=1,attempts=0,next_attempt=0,last_error='';`
					default:
						body += conversation(key)
					}
				}
				if source.table == "conversations" && event == "UPDATE" {
					body += workspace("OLD.workspace_id")
				}
			}
			when := ""
			if event == "UPDATE" {
				when = " WHEN " + strings.Join(changed, " OR ")
			}
			if _, err := tx.ExecContext(ctx, "DROP TRIGGER IF EXISTS "+name+"; CREATE TRIGGER "+name+" AFTER "+event+" ON "+sqlIdentifier(source.table)+when+" BEGIN "+body+" END"); err != nil {
				return fmt.Errorf("fingerprint %s: %w", source.table, err)
			}
		}
	}
	return nil
}

var fingerprintPruneBatchSize = 100

// Report processed orphan markers, including those still referenced, so a
// scheduled/manual pass can drain the backlog in bounded transactions.
func (c *Catalog) pruneFingerprintBlobs(ctx context.Context) (bool, error) {
	var removed int64
	err := c.writeTransaction(ctx, "prune fingerprint cache", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM fingerprint_blobs WHERE hash IN (SELECT hash FROM fingerprint_orphans WHERE not_before<=unixepoch() LIMIT ?) AND NOT EXISTS(SELECT 1 FROM fingerprint_images i WHERE i.hash=fingerprint_blobs.hash)`, fingerprintPruneBatchSize); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM fingerprint_orphans WHERE hash IN (SELECT hash FROM fingerprint_orphans WHERE not_before<=unixepoch() LIMIT ?)`, fingerprintPruneBatchSize)
		if err != nil {
			return err
		}
		removed, err = result.RowsAffected()
		return err
	})
	return removed > 0 && err == nil, err
}
