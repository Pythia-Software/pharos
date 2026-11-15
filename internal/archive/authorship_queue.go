package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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

// linkedWorkspacesSQL selects the workspaces an identity link joins. Only
// they can mirror one another, so they decide the representatives; reading
// every workspace instead was most of an idle estimates pass.
const linkedWorkspacesSQL = `SELECT id,source_kind,activity_at FROM workspaces WHERE id IN (
 SELECT a.workspace_id FROM conversation_identity_links l JOIN conversations a ON a.id=l.left_id
 UNION SELECT b.workspace_id FROM conversation_identity_links l JOIN conversations b ON b.id=l.right_id)`

func (c *Catalog) authorshipRepresentatives(ctx context.Context) (map[string]string, error) {
	workspaces, err := queryMapsContext(ctx, c.reader(), linkedWorkspacesSQL)
	if err != nil {
		return nil, err
	}
	links, err := queryMapsContext(ctx, c.reader(), identityLinkWorkspacesSQL)
	if err != nil {
		return nil, err
	}
	return mirrorRepresentatives(workspaces, links), nil
}

// authorshipMirrorSignature digests everything the representatives are chosen
// from (the linked workspaces' kinds and activity, and the links), streamed
// without building the map, so a pass can tell that the mirrors it stored
// still stand.
func (c *Catalog) authorshipMirrorSignature(ctx context.Context) (string, error) {
	digest := sha256.New()
	// One read of the links with both ends' workspaces: each lookup is a
	// seek into a wide row, and they are the whole cost of an idle pass.
	rows, err := c.reader().QueryContext(ctx, `SELECT l.left_workspace,l.right_workspace,
 COALESCE(a.source_kind,''),COALESCE(a.activity_at,''),COALESCE(b.source_kind,''),COALESCE(b.activity_at,'')
 FROM (`+identityLinkWorkspacesSQL+`) l LEFT JOIN workspaces a ON a.id=l.left_workspace LEFT JOIN workspaces b ON b.id=l.right_workspace ORDER BY 1,2`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	fields := make([]sql.RawBytes, 6)
	targets := make([]any, len(fields))
	for i := range fields {
		targets[i] = &fields[i]
	}
	for rows.Next() {
		if err := rows.Scan(targets...); err != nil {
			return "", err
		}
		for _, field := range fields {
			digest.Write(field)
			digest.Write([]byte{0})
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// authorshipIdleBudget bounds the wait for the writer when a pass has only a
// generation to publish: the next pass publishes it as well.
var authorshipIdleBudget = time.Second

// publishIdleAuthorship publishes generation as estimated when nothing is
// queued and the mirrors are the ones already reconsidered, and reports
// whether it did. The estimates' generation includes the tool ledger's, which
// moves with nearly every index, so most passes have only this to do; it
// costs two small reads and one metadata write instead of loading every
// workspace, and gives up rather than wait out the pass's budget behind
// another writer.
//
// idle reports that there was nothing else to do, published that the
// generation is now the estimates'.
func (c *Catalog) publishIdleAuthorship(parent context.Context, generation string) (idle, published bool, err error) {
	pending, err := c.authorshipPending(parent)
	if err != nil || pending {
		return false, false, err
	}
	signature, err := c.authorshipMirrorSignature(parent)
	if err != nil {
		return false, false, err
	}
	stored, err := c.metaValue(parent, "authorship_mirrors_signature")
	if err != nil || stored != signature {
		return false, false, err
	}
	ctx, cancel := context.WithTimeout(parent, authorshipIdleBudget)
	defer cancel()
	if err := c.publishAuthorshipEstimates(ctx, generation); err != nil {
		if parent.Err() == nil && ctx.Err() == context.DeadlineExceeded {
			return true, false, nil
		}
		return false, false, err
	}
	quick, err := c.metaValue(parent, "authorship_quick_generation")
	return true, quick == generation, err
}

// Suppression depends on workspace identity, not text. This metadata-only
// upsert also covers pre-estimates libraries whose excluded copies never had
// a counting row. Existing counting rows must go through the normal diff.
//
// The scan runs before the writer is taken and returns only the messages
// whose outcome is missing or out of date, usually none: as one statement
// inside the checkpoint's transaction it held the writer for 20 to 30 seconds
// on a large library. The map is materialized so each user message looks its
// workspace up; joined directly, json_each is scanned whole for every one.
func (c *Catalog) authorshipSuppressedChanges(ctx context.Context, representatives map[string]string) ([][2]string, error) {
	rows, err := c.reader().QueryContext(ctx, `WITH representatives AS MATERIALIZED (SELECT key,value FROM json_each(?))
 SELECT m.id,j.value FROM messages m JOIN conversations c ON c.id=m.conversation_id JOIN representatives j ON j.key=c.workspace_id
 LEFT JOIN authorship_message_state s ON s.message_id=m.id
 WHERE m.role='user' AND m.kind='message' AND NOT EXISTS(SELECT 1 FROM message_authorship a WHERE a.message_id=m.id)
 AND (s.message_id IS NULL OR s.disposition IS NOT 'suppressed' OR s.representative_id IS NOT j.value OR s.version IS NOT ?2)`, jsonText(representatives), authorshipVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	changes := [][2]string{}
	for rows.Next() {
		var change [2]string
		if err := rows.Scan(&change[0], &change[1]); err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}
	return changes, rows.Err()
}

// recordAuthorshipSuppressed stores the outcomes authorshipSuppressedChanges
// read. A message that gained a counting row meanwhile keeps its own outcome.
func recordAuthorshipSuppressed(ctx context.Context, tx *sql.Tx, changes [][2]string) error {
	if len(changes) == 0 {
		return nil
	}
	statement, err := tx.PrepareContext(ctx, `INSERT INTO authorship_message_state(message_id,source_hash,version,provisional,disposition,representative_id)
 SELECT ?1,'',?2,0,'suppressed',?3 WHERE NOT EXISTS(SELECT 1 FROM message_authorship a WHERE a.message_id=?1)
 ON CONFLICT(message_id) DO UPDATE SET disposition=excluded.disposition,representative_id=excluded.representative_id,version=excluded.version,provisional=0
 WHERE authorship_message_state.disposition IS NOT excluded.disposition OR authorship_message_state.representative_id IS NOT excluded.representative_id OR authorship_message_state.version IS NOT excluded.version`)
	if err != nil {
		return err
	}
	defer statement.Close()
	for _, change := range changes {
		if _, err := statement.ExecContext(ctx, change[0], authorshipVersion, change[1]); err != nil {
			return err
		}
	}
	return nil
}

// A previously suppressed copy may become the representative, and a counted
// copy may stop being one. Persist the map and queue both sides atomically.
//
// signature is authorshipMirrorSignature read before representatives; stored
// with them, it lets later passes skip this while the mirrors' inputs stand.
// A change between the two reads only makes the next pass look again.
func (c *Catalog) refreshAuthorshipMirrors(ctx context.Context, representatives map[string]string, signature string) error {
	stored, err := c.metaValue(ctx, "authorship_mirrors")
	if err != nil {
		return err
	}
	current := jsonText(representatives)
	if stored == current {
		if noted, err := c.metaValue(ctx, "authorship_mirrors_signature"); err != nil || signature == "" || noted == signature {
			return err
		}
		return c.writeTransaction(ctx, "note writing mirrors", func(tx *sql.Tx) error {
			return setMeta(tx, "authorship_mirrors_signature", signature)
		})
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
	// Find the affected messages before taking the writer, from the changed
	// workspaces' conversations: the unary + keeps SQLite off the prose index,
	// which walks every user message in the library (5 s of writer hold a
	// time on a large one). A message that arrives meanwhile queues itself.
	rows, err := c.reader().QueryContext(ctx, `SELECT m.id,m.created_at FROM conversations c JOIN messages m ON m.conversation_id=c.id
 WHERE c.workspace_id IN (SELECT value FROM json_each(?)) AND +m.role='user' AND +m.kind='message'`, jsonText(sortedNameIDs(changed)))
	if err != nil {
		return err
	}
	type queued struct {
		id   string
		sent sql.NullString
	}
	messages := []queued{}
	for rows.Next() {
		var message queued
		if err := rows.Scan(&message.id, &message.sent); err != nil {
			rows.Close()
			return err
		}
		messages = append(messages, message)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	return c.writeTransaction(ctx, "reconsider writing mirrors", func(tx *sql.Tx) error {
		at := now()
		for _, message := range messages {
			if _, err := tx.ExecContext(ctx, `INSERT INTO authorship_pending(message_id,revision,sent_at,queued_at) VALUES(?,hex(randomblob(16)),?,?)
 ON CONFLICT(message_id) DO UPDATE SET revision=excluded.revision,sent_at=excluded.sent_at`, message.id, message.sent, at); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('authorship_mirrors',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, current); err != nil {
			return err
		}
		return setMeta(tx, "authorship_mirrors_signature", signature)
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
	suppressed, err := c.authorshipSuppressedChanges(context.Background(), representatives)
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
		if err := recordAuthorshipSuppressed(context.Background(), tx, suppressed); err != nil {
			return err
		}
		// Re-evaluate exclusions whose relationship changed before an interrupted
		// bootstrap resumed, and counting rows now belonging to a mirror.
		// Materialize the map once so SQLite can index workspace lookups. A
		// direct LEFT JOIN json_each scans the entire map for every user message.
		if _, err := tx.Exec(`WITH representatives AS MATERIALIZED (SELECT key,value FROM json_each(?))
 INSERT INTO authorship_pending(message_id,revision,sent_at,queued_at)
 SELECT m.id,hex(randomblob(16)),m.created_at,? FROM messages m JOIN conversations c ON c.id=m.conversation_id
 LEFT JOIN representatives j ON j.key=c.workspace_id LEFT JOIN authorship_message_state s ON s.message_id=m.id
 WHERE m.kind='message' AND m.role='user' AND
 ((s.disposition='suppressed' AND s.representative_id IS NOT COALESCE(j.value,'')) OR
  (j.value IS NOT NULL AND EXISTS(SELECT 1 FROM message_authorship a WHERE a.message_id=m.id)))
 ON CONFLICT(message_id) DO UPDATE SET revision=excluded.revision,sent_at=excluded.sent_at`, jsonText(representatives), now()); err != nil {
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
	// Report the pass, not each 25-message batch: the run history showed
	// "N of 25" for a pass that handled up to the whole limit.
	progress := &authorshipQuickProgress{}
	if err := c.reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM authorship_pending LIMIT ?)`, limit).Scan(&progress.total); err != nil {
		return err
	}
	ctx = context.WithValue(ctx, authorshipQuickProgressKey{}, progress)
	reportWork(ctx, "estimating your writing", "message", 0, progress.total)
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
	if err == nil {
		// The queue may have held less than was counted: end on what was done.
		reportWork(ctx, "estimating your writing", "message", progress.done, progress.done)
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
	verb := "await"
	if b.Pending == 1 {
		verb = "awaits"
	}
	note := fmt.Sprintf("%s %s writing estimates (%d recent, %d historical).", plural(b.Pending, "message"), verb, b.Recent, b.Historical)
	if queued, ok := parseTime(b.OldestQueued); ok {
		note += " Oldest waiting work: " + durationWords(max(0, at.Sub(queued).Seconds())) + "."
	}
	return note
}
