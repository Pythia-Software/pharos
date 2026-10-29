package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const authorshipStateVersion = "authorship-state-v1/" + authorshipVersion

var authorshipSuffixLimit = 2000

const authorshipStateDDL = `
CREATE TABLE IF NOT EXISTS authorship_dirty(id INTEGER PRIMARY KEY CHECK(id=1),revision TEXT NOT NULL,earliest TEXT);
INSERT OR IGNORE INTO authorship_dirty VALUES(1,'',NULL);
CREATE TABLE IF NOT EXISTS authorship_user_text(message_id TEXT PRIMARY KEY,conversation_id TEXT NOT NULL,sent_at TEXT NOT NULL,hashes_json TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS authorship_candidates(candidate TEXT PRIMARY KEY,message_id TEXT NOT NULL,offset INTEGER NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS authorship_lines(key TEXT PRIMARY KEY,conversation_id TEXT NOT NULL,message_id TEXT NOT NULL,conversations_json TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS authorship_templates(key TEXT PRIMARY KEY,sessions_json TEXT NOT NULL,days_json TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS authorship_last_user(conversation_id TEXT PRIMARY KEY,sent_at TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS authorship_questions(conversation_id TEXT PRIMARY KEY,questions_json TEXT NOT NULL) WITHOUT ROWID;
`

func (c *Catalog) ensureAuthorshipState() error {
	if _, err := c.DB.Exec(authorshipStateDDL); err != nil {
		return err
	}
	for _, spec := range []struct{ table, stamp, columns, relevant string }{
		{"messages", "created_at", "id,conversation_id,role,kind,text,created_at,sender", "%s.role IN ('user','assistant') AND %s.kind IN ('message','tool_call')"},
		{"tool_calls", "started_at", "id,workspace_id,conversation_id,call_message_id,tool_category,program,subcommand,command,started_at,ended_at,backgrounded", ""},
		{"tool_commands", "", "tool_call_id,position,program,subcommand,command", ""},
		{"conversations", "", "id,workspace_id,parent_id,harness,origin_host_id,provider,account,native_id,started_at", ""},
		{"conversation_identity_links", "", "", ""},
		{"workspaces", "", "id,source_kind,location", ""},
	} {
		columns := strings.Split(spec.columns, ",")
		if spec.columns == "" {
			rows, err := queryMaps(c.DB, "PRAGMA table_info("+spec.table+")")
			if err != nil {
				return err
			}
			columns = nil
			for _, row := range rows {
				columns = append(columns, asString(row["name"]))
			}
		}
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			refs := []string{"NEW"}
			if event == "DELETE" {
				refs = []string{"OLD"}
			} else if event == "UPDATE" {
				refs = []string{"OLD", "NEW"}
			}
			guards := []string{}
			if event == "UPDATE" {
				changes := []string{}
				for _, col := range columns {
					changes = append(changes, "OLD."+col+" IS NOT NEW."+col)
				}
				guards = append(guards, "("+strings.Join(changes, " OR ")+")")
			}
			if spec.relevant != "" {
				relevant := []string{}
				for _, ref := range refs {
					relevant = append(relevant, fmt.Sprintf(spec.relevant, ref, ref))
				}
				guards = append(guards, "("+strings.Join(relevant, " OR ")+")")
			}
			body := ""
			for _, ref := range refs {
				stamp := "''"
				if spec.stamp != "" {
					stamp = "COALESCE(" + ref + "." + spec.stamp + ",'')"
				}
				if spec.table == "tool_commands" {
					stamp = "COALESCE((SELECT started_at FROM tool_calls WHERE id=" + ref + ".tool_call_id),'')"
				}
				body += `UPDATE authorship_dirty SET revision=hex(randomblob(16)),earliest=MIN(COALESCE(earliest,` + stamp + `),` + stamp + `) WHERE id=1;`
			}
			generationKey := "tool_ledger_generation"
			if spec.table == "messages" {
				generationKey = "authorship_inputs_generation"
			}
			body += `INSERT INTO meta(key,value) VALUES('` + generationKey + `','1') ON CONFLICT(key) DO UPDATE SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT);`
			when := ""
			if len(guards) > 0 {
				when = " WHEN " + strings.Join(guards, " AND ")
			}
			if _, err := c.DB.Exec("CREATE TRIGGER IF NOT EXISTS authorship_input_" + spec.table + "_" + event + " AFTER " + event + " ON " + spec.table + when + " BEGIN " + body + " END"); err != nil {
				return err
			}
		}
	}
	// Activity chooses the representative only inside a mirror group. Ordinary
	// activity appends must not turn every message into a graph-wide rebuild.
	_, err := c.DB.Exec(`CREATE TRIGGER IF NOT EXISTS authorship_mirror_activity AFTER UPDATE OF activity_at ON workspaces
 WHEN OLD.activity_at IS NOT NEW.activity_at AND EXISTS(SELECT 1 FROM conversations c JOIN conversation_identity_links l ON c.id=l.left_id OR c.id=l.right_id WHERE c.workspace_id=NEW.id)
 BEGIN UPDATE authorship_dirty SET revision=hex(randomblob(16)),earliest='' WHERE id=1;
 INSERT INTO meta(key,value) VALUES('tool_ledger_generation','1') ON CONFLICT(key) DO UPDATE SET value=CAST(CAST(value AS INTEGER)+1 AS TEXT); END`)
	return err
}

type authorshipCheckpoint struct {
	version, generation, watermark, graph, revision string
	earliest                                        sql.NullString
}

// Hash only classification and launch metadata. Source titles and display
// names do not affect classification. Mirror priority includes activity through
// the representative map, so changing the selected copy cannot reuse state.
func (c *Catalog) authorshipGraph(ctx context.Context) (string, time.Time, error) {
	workspaces, err := queryMapsContext(ctx, c.DB, `SELECT id,source_kind,location,activity_at FROM workspaces ORDER BY id`)
	if err != nil {
		return "", time.Time{}, err
	}
	links, err := queryMapsContext(ctx, c.DB, identityLinkWorkspacesSQL)
	if err != nil {
		return "", time.Time{}, err
	}
	representative := mirrorRepresentatives(workspaces, links)
	identities, err := queryMapsContext(ctx, c.DB, `SELECT left_id,right_id,relationship FROM conversation_identity_links ORDER BY left_id,right_id,relationship`)
	if err != nil {
		return "", time.Time{}, err
	}
	conversations, err := queryMapsContext(ctx, c.DB, `SELECT id,workspace_id,parent_id,harness,origin_host_id,provider,account,native_id,started_at FROM conversations ORDER BY id`)
	if err != nil {
		return "", time.Time{}, err
	}
	claimed := map[string]bool{}
	for _, link := range identities {
		claimed[asString(link["left_id"])] = true
		claimed[asString(link["right_id"])] = true
	}
	var lastRun time.Time
	for _, row := range conversations {
		if row["parent_id"] == nil && !claimed[asString(row["id"])] && headlessRun(asString(row["provider"]), asString(row["harness"])) != "" {
			if at, ok := parseTime(asString(row["started_at"])); ok && latestLaunch(at).After(lastRun) {
				lastRun = latestLaunch(at)
			}
		}
	}
	for _, work := range workspaces {
		delete(work, "activity_at")
	}
	zone, offset := time.Now().In(time.Local).Zone()
	return hashBytes([]byte(jsonText([]any{workspaces, representative, identities, conversations, time.Local.String(), zone, offset}))), lastRun, nil
}

func (c *Catalog) authorshipCheckpoint(ctx context.Context) (authorshipCheckpoint, error) {
	var state authorshipCheckpoint
	rows, err := queryMapsContext(ctx, c.DB, `SELECT key,value FROM meta WHERE key LIKE 'authorship_state_%'`)
	if err != nil {
		return state, err
	}
	for _, row := range rows {
		switch asString(row["key"]) {
		case "authorship_state_version":
			state.version = asString(row["value"])
		case "authorship_state_generation":
			state.generation = asString(row["value"])
		case "authorship_state_watermark":
			state.watermark = asString(row["value"])
		case "authorship_state_graph":
			state.graph = asString(row["value"])
		}
	}
	err = c.DB.QueryRowContext(ctx, `SELECT revision,earliest FROM authorship_dirty WHERE id=1`).Scan(&state.revision, &state.earliest)
	return state, err
}

type authorshipIncremental struct {
	checkpoint    authorshipCheckpoint
	graph, reason string
	inputs        []authorInput
	launches      []conversationLaunch
	classifier    *classifier
	since         time.Time
}

func (c *Catalog) incrementalAuthorship(ctx context.Context) (*authorshipIncremental, error) {
	checkpoint, err := c.authorshipCheckpoint(ctx)
	if err != nil {
		return nil, err
	}
	run := &authorshipIncremental{checkpoint: checkpoint, reason: "checkpoint unavailable"}
	graph, lastRun, err := c.authorshipGraph(ctx)
	if err != nil {
		return nil, err
	}
	run.graph = graph
	_, built, err := c.authorshipGenerations()
	if err != nil {
		return nil, err
	}
	if checkpoint.version != authorshipStateVersion || checkpoint.generation != built {
		return run, nil
	}
	if graph != checkpoint.graph {
		run.reason = "identity or launch graph changed"
		return run, nil
	}
	watermark, ok := parseTime(checkpoint.watermark)
	if !ok {
		return run, nil
	}
	since, ok := parseTime(checkpoint.earliest.String)
	if !checkpoint.earliest.Valid {
		run.reason = "unchanged"
		return run, nil
	}
	if !ok || !since.After(watermark) {
		run.reason = "backfill or changed historical input"
		return run, nil
	}
	// A command later than every existing headless start cannot launch one of
	// those runs. Overlap uses the full graph resolver until stream-local replay
	// can prove the same ownership and folding decisions.
	if !since.After(lastRun) {
		run.reason = "launch stream overlap"
		return run, nil
	}
	workspaces, err := queryMapsContext(ctx, c.DB, `SELECT id,source_kind,activity_at FROM workspaces`)
	if err != nil {
		return nil, err
	}
	links, err := queryMapsContext(ctx, c.DB, identityLinkWorkspacesSQL)
	if err != nil {
		return nil, err
	}
	claimed, err := c.appClaims(ctx, c.DB)
	if err != nil {
		return nil, err
	}
	rows, err := queryMapsContext(ctx, c.DB, `SELECT l.child_id,l.parent_id,l.tool_call_id,l.program,COALESCE(t.call_message_id,'') message FROM conversation_launches l LEFT JOIN tool_calls t ON t.id=l.tool_call_id`)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		run.launches = append(run.launches, conversationLaunch{child: asString(row["child_id"]), parent: asString(row["parent_id"]), toolCall: asString(row["tool_call_id"]), program: asString(row["program"]), message: asString(row["message"])})
	}
	run.inputs, err = c.authorInputsSince(ctx, c.DB, mirrorRepresentatives(workspaces, links), claimed, run.launches, &since)
	if err != nil {
		return nil, err
	}
	if len(run.inputs) > authorshipSuffixLimit {
		run.reason = "suffix limit"
		return run, nil
	}
	// No user turn can consume these replies yet. Keep the dirty watermark so
	// a later user turn still sees old question forms and late agent output.
	if len(run.inputs) == 0 {
		run.reason = "no user suffix"
		return run, nil
	}
	classifier, err := c.loadAuthorshipClassifier(ctx, run.inputs, watermark)
	if err != nil {
		return nil, err
	}
	if classifier == nil {
		run.reason = "historical template counts changed"
		return run, nil
	}
	run.classifier, run.since, run.reason = classifier, since, "suffix"
	return run, nil
}

type storedAuthorshipQuestion struct {
	Title                 string
	Options               []string
	Conversation, Message string
	At                    time.Time
}

func (c *Catalog) loadAuthorshipClassifier(ctx context.Context, inputs []authorInput, watermark time.Time) (*classifier, error) {
	classifier := newClassifier(inputs)
	keys := make([]string, 0, len(classifier.templates))
	for key := range classifier.templates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rows, err := queryMapsContext(ctx, c.DB, `SELECT key,sessions_json,days_json FROM authorship_templates WHERE key IN (SELECT value FROM json_each(?))`, jsonText(keys))
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		key := asString(row["key"])
		old := &templateUse{}
		if err := json.Unmarshal([]byte(asString(row["sessions_json"])), &old.sessions); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(asString(row["days_json"])), &old.days); err != nil {
			return nil, err
		}
		use := classifier.templates[key]
		sessions, days := len(old.sessions), len(old.days)
		for session := range old.sessions {
			use.sessions[session] = true
		}
		for day := range old.days {
			use.days[day] = true
		}
		if use.template() && (len(use.sessions) != sessions || len(use.days) != days) {
			return nil, nil
		}
	}
	conversations := map[string]bool{}
	for _, input := range inputs {
		conversations[input.ConversationID] = true
	}
	ids := sortedNameIDs(conversations)
	rows, err = queryMapsContext(ctx, c.DB, `SELECT conversation_id,sent_at FROM authorship_last_user WHERE conversation_id IN (SELECT value FROM json_each(?))`, jsonText(ids))
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		at, ok := parseTime(asString(row["sent_at"]))
		if !ok {
			return nil, fmt.Errorf("invalid persisted authorship time")
		}
		classifier.lastUserSent[asString(row["conversation_id"])] = at
	}
	rows, err = queryMapsContext(ctx, c.DB, `SELECT conversation_id,questions_json FROM authorship_questions WHERE conversation_id IN (SELECT value FROM json_each(?))`, jsonText(ids))
	if err != nil {
		return nil, err
	}
	start := inputs[0].SentAt.Add(-authorshipWindow)
	if watermark.Before(start) {
		start = watermark
	}
	classifier.replyStart = start
	for _, row := range rows {
		var questions []storedAuthorshipQuestion
		if err := json.Unmarshal([]byte(asString(row["questions_json"])), &questions); err != nil {
			return nil, err
		}
		for _, question := range questions {
			if question.At.Before(start) {
				classifier.questions[question.Conversation] = append(classifier.questions[question.Conversation], authorshipQuestion{title: question.Title, options: question.Options, origin: textOrigin{question.Conversation, question.Message}, at: question.At})
			}
		}
	}
	loaded := map[uint64]bool{}
	batches := map[string]*shingleBatch{}
	classifier.userText.load = func(keys []uint64) error {
		wanted := []string{}
		for _, key := range keys {
			if _, ok := classifier.userText.seen[key]; !ok && !loaded[key] {
				wanted = append(wanted, authorshipKey(key))
				loaded[key] = true
			}
		}
		if len(wanted) == 0 {
			return nil
		}
		rows, err := queryMapsContext(ctx, c.DB, `SELECT candidate,offset,message_id FROM authorship_candidates WHERE candidate IN (SELECT value FROM json_each(?))`, jsonText(wanted))
		if err != nil {
			return err
		}
		needed := map[string]bool{}
		for _, row := range rows {
			id := asString(row["message_id"])
			if batches[id] == nil {
				needed[id] = true
			}
		}
		if len(needed) > 0 {
			sources, err := queryMapsContext(ctx, c.DB, `SELECT message_id,conversation_id,sent_at,hashes_json FROM authorship_user_text WHERE message_id IN (SELECT value FROM json_each(?))`, jsonText(sortedNameIDs(needed)))
			if err != nil {
				return err
			}
			for _, row := range sources {
				id := asString(row["message_id"])
				at, ok := parseTime(asString(row["sent_at"]))
				if !ok {
					return fmt.Errorf("invalid persisted shingle time")
				}
				batch := &shingleBatch{sentAt: at, at: at.Unix(), origin: textOrigin{asString(row["conversation_id"]), id}}
				if err := json.Unmarshal([]byte(asString(row["hashes_json"])), &batch.hashes); err != nil {
					return err
				}
				batches[id] = batch
			}
		}
		for _, row := range rows {
			batch := batches[asString(row["message_id"])]
			if batch == nil {
				return fmt.Errorf("persisted authorship source missing")
			}
			key, err := strconv.ParseUint(asString(row["candidate"]), 16, 64)
			if err != nil {
				return err
			}
			offset := int(integer(row["offset"]))
			if offset < 0 || offset+copyShingles > len(batch.hashes) {
				return fmt.Errorf("invalid persisted shingle offset")
			}
			classifier.userText.seen[key] = shingleRef{batch: batch, offset: offset}
		}
		return nil
	}
	classifier.loadLines = func(keys []uint64) error {
		wanted := []string{}
		for _, key := range keys {
			if key != 0 && classifier.lines[key] == nil {
				wanted = append(wanted, authorshipKey(key))
			}
		}
		if len(wanted) == 0 {
			return nil
		}
		rows, err := queryMapsContext(ctx, c.DB, `SELECT * FROM authorship_lines WHERE key IN (SELECT value FROM json_each(?))`, jsonText(wanted))
		if err != nil {
			return err
		}
		for _, row := range rows {
			key, err := strconv.ParseUint(asString(row["key"]), 16, 64)
			if err != nil {
				return err
			}
			use := &lineUse{origin: textOrigin{asString(row["conversation_id"]), asString(row["message_id"])}}
			if err := json.Unmarshal([]byte(asString(row["conversations_json"])), &use.conversations); err != nil {
				return err
			}
			classifier.lines[key] = use
		}
		return nil
	}
	return classifier, nil
}

func authorshipKey(key uint64) string { return fmt.Sprintf("%016x", key) }

// Save auxiliary state in the same bounded writer turns as output rows. The
// checkpoint is invalidated before any chunk, and restored only with the final
// generation marker. A crash during either output or state publication forces
// the next run through the real full classifier and diffing store.
func (c *Catalog) storeAuthorshipState(ctx context.Context, run *authorshipIncremental, results []authorResult, generation string, full bool) error {
	if err := c.writeTransaction(ctx, "invalidate authorship checkpoint", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM meta WHERE key='authorship_state_version'`)
		return err
	}); err != nil {
		return err
	}
	batch := []authorshipMutation{}
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := c.writeTransaction(ctx, "save authorship state chunk", func(tx *sql.Tx) error {
			return executeAuthorshipMutations(ctx, tx, batch, nil)
		})
		batch = nil
		return err
	}
	put := func(table string, columns []string, values []any) error {
		assignments, guards := []string{}, []string{}
		for _, col := range columns[1:] {
			assignments = append(assignments, col+"=excluded."+col)
			guards = append(guards, table+"."+col+" IS NOT excluded."+col)
		}
		batch = append(batch, authorshipMutation{query: "INSERT INTO " + table + "(" + strings.Join(columns, ",") + ") VALUES(" + placeholders(len(columns)) + ") ON CONFLICT(" + columns[0] + ") DO UPDATE SET " + strings.Join(assignments, ",") + " WHERE " + strings.Join(guards, " OR "), args: values})
		if len(batch) >= max(1, authorshipWriteBatch) {
			return flush()
		}
		return nil
	}
	if full {
		for _, table := range []struct{ name, key string }{{"authorship_candidates", "candidate"}, {"authorship_user_text", "message_id"}, {"authorship_lines", "key"}, {"authorship_templates", "key"}, {"authorship_last_user", "conversation_id"}, {"authorship_questions", "conversation_id"}} {
			for {
				var n int64
				if err := c.writeTransaction(ctx, "reset authorship state chunk", func(tx *sql.Tx) error {
					result, err := tx.ExecContext(ctx, "DELETE FROM "+table.name+" WHERE "+table.key+" IN (SELECT "+table.key+" FROM "+table.name+" LIMIT ?)", max(1, authorshipWriteBatch))
					if err != nil {
						return err
					}
					n, err = result.RowsAffected()
					return err
				}); err != nil {
					return err
				}
				if n == 0 {
					break
				}
			}
		}
	}
	classifier := run.classifier
	for _, text := range classifier.userText.queue {
		if err := put("authorship_user_text", []string{"message_id", "conversation_id", "sent_at", "hashes_json"}, []any{text.origin.message, text.origin.conversation, formatTime(text.sentAt), jsonText(text.hashes)}); err != nil {
			return err
		}
	}
	candidateKeys := make([]uint64, 0, len(classifier.userText.seen))
	for key := range classifier.userText.seen {
		candidateKeys = append(candidateKeys, key)
	}
	slices.Sort(candidateKeys)
	for _, key := range candidateKeys {
		ref := classifier.userText.seen[key]
		if err := put("authorship_candidates", []string{"candidate", "message_id", "offset"}, []any{authorshipKey(key), ref.batch.origin.message, ref.offset}); err != nil {
			return err
		}
	}
	lineKeys := make([]uint64, 0, len(classifier.lines))
	for key := range classifier.lines {
		lineKeys = append(lineKeys, key)
	}
	slices.Sort(lineKeys)
	for _, key := range lineKeys {
		use := classifier.lines[key]
		if err := put("authorship_lines", []string{"key", "conversation_id", "message_id", "conversations_json"}, []any{authorshipKey(key), use.origin.conversation, use.origin.message, jsonText(use.conversations)}); err != nil {
			return err
		}
	}
	for key, use := range classifier.templates {
		if err := put("authorship_templates", []string{"key", "sessions_json", "days_json"}, []any{key, jsonText(use.sessions), jsonText(use.days)}); err != nil {
			return err
		}
	}
	for id, at := range classifier.lastUserSent {
		if err := put("authorship_last_user", []string{"conversation_id", "sent_at"}, []any{id, formatTime(at)}); err != nil {
			return err
		}
	}
	targets := map[string]bool{}
	for _, result := range results {
		targets[result.Input.ConversationID] = true
	}
	for id, questions := range classifier.questions {
		if !full && !targets[id] {
			continue
		}
		stored := []storedAuthorshipQuestion{}
		for _, question := range questions {
			stored = append(stored, storedAuthorshipQuestion{Title: question.title, Options: question.options, Conversation: question.origin.conversation, Message: question.origin.message, At: question.at})
		}
		if err := put("authorship_questions", []string{"conversation_id", "questions_json"}, []any{id, jsonText(stored)}); err != nil {
			return err
		}
	}
	if err := flush(); err != nil {
		return err
	}
	// If source rows moved while the unpinned reads or chunks ran, do not bless
	// this auxiliary state. The requested generation remains stale and its next
	// refresh takes the full fallback, with all dirty evidence retained.
	return c.writeTransaction(ctx, "complete authorship checkpoint", func(tx *sql.Tx) error {
		var revision string
		if err := tx.QueryRowContext(ctx, "SELECT revision FROM authorship_dirty WHERE id=1").Scan(&revision); err != nil {
			return err
		}
		metadata := map[string]string{"authorship_generation": generation, "authorship_built_at": now()}
		if revision == run.checkpoint.revision {
			watermark := run.checkpoint.watermark
			if len(results) > 0 {
				watermark = formatTime(results[len(results)-1].Input.SentAt)
			}
			for key, value := range map[string]string{"version": authorshipStateVersion, "generation": generation, "watermark": watermark, "graph": run.graph} {
				metadata["authorship_state_"+key] = value
			}
			if _, err := tx.ExecContext(ctx, "UPDATE authorship_dirty SET earliest=NULL WHERE id=1 AND revision=?", revision); err != nil {
				return err
			}
		}
		for key, value := range metadata {
			if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
				return err
			}
		}
		return nil
	})
}
