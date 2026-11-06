package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const authorshipRecentWindow = 24 * time.Hour

// Keep outcomes even when a message intentionally has no counting row.
const (
	authorshipCounted    = "counted"
	authorshipSuppressed = "suppressed"
	authorshipIneligible = "ineligible"
	authorshipDeferred   = "deferred"
)

func (c *Catalog) authorshipRepresentatives(ctx context.Context) (map[string]string, error) {
	workspaces, err := queryMapsContext(ctx, c.reader(), `SELECT id,source_kind,activity_at FROM workspaces`)
	if err != nil {
		return nil, err
	}
	links, err := queryMapsContext(ctx, c.reader(), identityLinkWorkspacesSQL)
	if err != nil {
		return nil, err
	}
	return mirrorRepresentatives(workspaces, links), nil
}

// Suppression depends on workspace identity, not text. This metadata-only
// upsert also covers pre-estimates libraries whose excluded copies never had
// a counting row. Existing counting rows must go through the normal diff.
func recordAuthorshipSuppressed(ctx context.Context, tx *sql.Tx, representatives map[string]string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO authorship_message_state(message_id,source_hash,version,provisional,disposition,representative_id)
 SELECT m.id,'',?,0,'suppressed',j.value FROM json_each(?) j
 JOIN conversations c ON c.workspace_id=j.key JOIN messages m ON m.conversation_id=c.id
 WHERE m.role='user' AND m.kind='message' AND NOT EXISTS(SELECT 1 FROM message_authorship a WHERE a.message_id=m.id)
 ON CONFLICT(message_id) DO UPDATE SET disposition=excluded.disposition,representative_id=excluded.representative_id,version=excluded.version,provisional=0
 WHERE authorship_message_state.disposition IS NOT excluded.disposition OR authorship_message_state.representative_id IS NOT excluded.representative_id OR authorship_message_state.version IS NOT excluded.version`, authorshipVersion, jsonText(representatives))
	return err
}

// A previously suppressed copy may become the representative, and a counted
// copy may stop being one. Persist the map and queue both sides atomically.
func (c *Catalog) refreshAuthorshipMirrors(ctx context.Context, representatives map[string]string) error {
	stored, err := c.metaValue(ctx, "authorship_mirrors")
	if err != nil {
		return err
	}
	current := jsonText(representatives)
	if stored == current {
		return nil
	}
	previous := map[string]string{}
	if stored != "" {
		if err := json.Unmarshal([]byte(stored), &previous); err != nil {
			return err
		}
	}
	changed := map[string]bool{}
	for id, rep := range representatives {
		if previous[id] != rep {
			changed[id] = true
		}
	}
	for id, rep := range previous {
		if representatives[id] != rep {
			changed[id] = true
		}
	}
	ids := sortedNameIDs(changed)
	return c.writeTransaction(ctx, "reconsider writing mirrors", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO authorship_pending(message_id,revision,sent_at,queued_at)
 SELECT m.id,hex(randomblob(16)),m.created_at,? FROM conversations c JOIN messages m ON m.conversation_id=c.id
 WHERE c.workspace_id IN (SELECT value FROM json_each(?)) AND m.role='user' AND m.kind='message'
 ON CONFLICT(message_id) DO UPDATE SET revision=excluded.revision,sent_at=excluded.sent_at`, now(), jsonText(ids)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('authorship_mirrors',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, current)
		return err
	})
}

func (c *Catalog) ensureQuickAuthorshipState() error {
	if _, err := c.DB.Exec(authorshipQuickDDL); err != nil {
		return err
	}
	for _, column := range []struct{ table, name, ddl string }{
		{"authorship_pending", "sent_at", "TEXT"},
		{"authorship_pending", "queued_at", "TEXT NOT NULL DEFAULT ''"},
		{"authorship_message_state", "disposition", "TEXT NOT NULL DEFAULT 'counted'"},
		{"authorship_message_state", "representative_id", "TEXT NOT NULL DEFAULT ''"},
	} {
		has, err := c.hasColumn(column.table, column.name)
		if err != nil {
			return err
		}
		if !has {
			if _, err := c.DB.Exec("ALTER TABLE " + column.table + " ADD COLUMN " + column.name + " " + column.ddl); err != nil {
				return err
			}
		}
	}
	if _, err := c.DB.Exec(`CREATE INDEX IF NOT EXISTS authorship_pending_time ON authorship_pending(sent_at DESC,message_id);
 CREATE INDEX IF NOT EXISTS authorship_pending_age ON authorship_pending(queued_at);
 CREATE INDEX IF NOT EXISTS authorship_disposition ON authorship_message_state(disposition);`); err != nil {
		return err
	}
	// Named versions make the upgrade idempotent without recreating triggers on
	// every open. Source changes retain the first unacknowledged arrival time.
	for _, event := range []string{"insert", "update", "delete"} {
		if _, err := c.DB.Exec("DROP TRIGGER IF EXISTS authorship_quick_" + event); err != nil {
			return err
		}
	}
	if _, err := c.DB.Exec(authorshipQueueTriggers); err != nil {
		return err
	}
	ready, err := c.metaValue(context.Background(), "authorship_quick_initialized")
	if err != nil || ready == "2" {
		return err
	}
	representatives, err := c.authorshipRepresentatives(context.Background())
	if err != nil {
		return err
	}
	return c.writeTransaction(context.Background(), "initialize writing estimates", func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE authorship_pending SET sent_at=(SELECT created_at FROM messages m WHERE m.id=message_id),queued_at=? WHERE queued_at=''`, now()); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE authorship_message_state SET disposition='deferred' WHERE version=?`, authorshipVersion+"/deferred"); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO authorship_message_state(message_id,source_hash,version,provisional,disposition,representative_id) SELECT message_id,'',?,0,'counted','' FROM message_authorship`, authorshipVersion); err != nil {
			return err
		}
		if err := recordAuthorshipSuppressed(context.Background(), tx, representatives); err != nil {
			return err
		}
		// Re-evaluate exclusions whose relationship changed before an interrupted
		// bootstrap resumed, and counting rows now belonging to a mirror.
		if _, err := tx.Exec(`INSERT INTO authorship_pending(message_id,revision,sent_at,queued_at)
 SELECT m.id,hex(randomblob(16)),m.created_at,? FROM messages m JOIN conversations c ON c.id=m.conversation_id
 LEFT JOIN json_each(?) j ON j.key=c.workspace_id LEFT JOIN authorship_message_state s ON s.message_id=m.id
 WHERE m.kind='message' AND m.role='user' AND
 ((s.disposition='suppressed' AND s.representative_id IS NOT COALESCE(j.value,'')) OR
  (j.value IS NOT NULL AND EXISTS(SELECT 1 FROM message_authorship a WHERE a.message_id=m.id)))
 ON CONFLICT(message_id) DO UPDATE SET revision=excluded.revision,sent_at=excluded.sent_at`, now(), jsonText(representatives)); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM authorship_pending WHERE message_id IN (
 SELECT s.message_id FROM authorship_message_state s JOIN messages m ON m.id=s.message_id
 JOIN conversations c ON c.id=m.conversation_id JOIN json_each(?) j ON j.key=c.workspace_id
 WHERE s.disposition='suppressed' AND s.representative_id=j.value)
 AND NOT EXISTS(SELECT 1 FROM message_authorship a WHERE a.message_id=authorship_pending.message_id)`, jsonText(representatives)); err != nil {
			return err
		}

		if _, err := tx.Exec(`INSERT OR IGNORE INTO authorship_pending(message_id,revision,sent_at,queued_at)
 SELECT m.id,hex(randomblob(16)),m.created_at,? FROM messages m WHERE m.kind='message' AND m.role='user'
 AND NOT EXISTS(SELECT 1 FROM message_authorship a WHERE a.message_id=m.id)
 AND NOT EXISTS(SELECT 1 FROM authorship_message_state s WHERE s.message_id=m.id AND s.disposition IN ('suppressed','ineligible','deferred') AND s.version IN (?,?))`, now(), authorshipVersion, authorshipQuickVersion); err != nil {
			return err
		}
		for key, value := range map[string]string{"authorship_quick_initialized": "2", "authorship_mirrors": jsonText(representatives)} {
			if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO meta(key,value) SELECT 'authorship_complete_at',value FROM meta WHERE key='authorship_built_at'`); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT OR IGNORE INTO meta(key,value) SELECT 'authorship_refined_at',value FROM meta WHERE key='authorship_built_at'`)
		return err
	})
}

const authorshipQueueTriggers = `
CREATE TRIGGER IF NOT EXISTS authorship_quick_insert_v2 AFTER INSERT ON messages WHEN NEW.role='user' AND NEW.kind='message'
BEGIN INSERT INTO authorship_pending(message_id,revision,sent_at,queued_at) VALUES(NEW.id,hex(randomblob(16)),NEW.created_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) ON CONFLICT(message_id) DO UPDATE SET revision=excluded.revision,sent_at=excluded.sent_at; END;
CREATE TRIGGER IF NOT EXISTS authorship_quick_update_v2 AFTER UPDATE ON messages
WHEN ((OLD.role='user' AND OLD.kind='message') OR (NEW.role='user' AND NEW.kind='message'))
AND (OLD.id IS NOT NEW.id OR OLD.conversation_id IS NOT NEW.conversation_id OR OLD.role IS NOT NEW.role OR OLD.kind IS NOT NEW.kind OR OLD.text IS NOT NEW.text OR OLD.created_at IS NOT NEW.created_at OR OLD.sender IS NOT NEW.sender)
BEGIN
INSERT INTO authorship_pending(message_id,revision,sent_at,queued_at) VALUES(OLD.id,hex(randomblob(16)),OLD.created_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) ON CONFLICT(message_id) DO UPDATE SET revision=excluded.revision,sent_at=excluded.sent_at;
INSERT INTO authorship_pending(message_id,revision,sent_at,queued_at) VALUES(NEW.id,hex(randomblob(16)),NEW.created_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) ON CONFLICT(message_id) DO UPDATE SET revision=excluded.revision,sent_at=excluded.sent_at;
END;
CREATE TRIGGER IF NOT EXISTS authorship_quick_delete_v2 AFTER DELETE ON messages WHEN OLD.role='user' AND OLD.kind='message'
BEGIN INSERT INTO authorship_pending(message_id,revision,sent_at,queued_at) VALUES(OLD.id,hex(randomblob(16)),OLD.created_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')) ON CONFLICT(message_id) DO UPDATE SET revision=excluded.revision,sent_at=excluded.sent_at; END;
`

// Each lane gets rows and time of its own. Unused rows can be borrowed, but
// the recent lane always runs before any historical work.
func (c *Catalog) runAuthorshipLanes(ctx context.Context, representatives map[string]string) error {
	limit := max(1, authorshipQuickLimit)
	recentLimit := limit - limit/5
	cutoff := formatTime(c.clock().Add(-authorshipRecentWindow))
	deadline, _ := ctx.Deadline()
	recentCtx, stop := context.WithTimeout(ctx, time.Until(deadline)*3/4)
	recent, err := c.runAuthorshipLane(recentCtx, representatives, recentLimit, cutoff, true)
	expired := recentCtx.Err() == context.DeadlineExceeded
	stop()
	if err != nil && !expired {
		return err
	}
	historical, err := c.runAuthorshipLane(ctx, representatives, limit-recent, cutoff, false)
	if err != nil {
		return err
	}
	if remaining := limit - recent - historical; remaining > 0 {
		_, err = c.runAuthorshipLane(ctx, representatives, remaining, cutoff, true)
	}
	return err
}

func (c *Catalog) runAuthorshipLane(ctx context.Context, representatives map[string]string, limit int, cutoff string, recent bool) (int, error) {
	done := 0
	for done < limit {
		n, err := c.quickAuthorshipBatch(ctx, representatives, min(25, limit-done), cutoff, recent)
		if err != nil {
			return done, err
		}
		done += n
		if n == 0 {
			break
		}
	}
	return done, nil
}

type authorshipBacklog struct {
	Pending      int    `json:"pending_messages"`
	Recent       int    `json:"pending_recent_messages"`
	Historical   int    `json:"pending_historical_messages"`
	OldestSent   string `json:"oldest_pending_sent_at"`
	OldestQueued string `json:"oldest_pending_queued_at"`
}

func (c *Catalog) authorshipBacklog(ctx context.Context) (authorshipBacklog, error) {
	var b authorshipBacklog
	cutoff := formatTime(c.clock().Add(-authorshipRecentWindow))
	err := c.reader().QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(sent_at>=?),0),COALESCE(MIN(sent_at),''),COALESCE(MIN(NULLIF(queued_at,'')),'') FROM authorship_pending`, cutoff).Scan(&b.Pending, &b.Recent, &b.OldestSent, &b.OldestQueued)
	b.Historical = b.Pending - b.Recent
	return b, err
}

func (b authorshipBacklog) note(at time.Time) string {
	if b.Pending == 0 {
		return ""
	}
	note := fmt.Sprintf("%d messages await writing estimates (%d recent, %d historical).", b.Pending, b.Recent, b.Historical)
	if queued, ok := parseTime(b.OldestQueued); ok {
		note += " Oldest waiting work: " + durationWords(max(0, at.Sub(queued).Seconds())) + "."
	}
	return note
}
