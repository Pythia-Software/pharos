package archive

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// Agent event facts: what the Agent events dashboard divides and breaks down.
// One row per agent message the events pass read (agent_event_messages, the
// denominator), with the dimensions a rate can be split by joined in when the
// projection is built, never when a page asks:
//
//   - the turn it belongs to: a conversation's messages split at each prompt
//     (a user message, or the delegating agent's message that starts a
//     subagent), as Fixes splits them;
//   - who wrote that prompt: a person (typed words), an automation (a user
//     message with no typed words), a delegating agent, or no prompt at all;
//   - the typed length of a person's prompt;
//   - the message's model and effort, provider, harness and whether it is a
//     subagent's.
//
// agent_event_fact_kinds holds each message's events by kind, at the
// datasets' threshold. The agent_event_rates dataset crosses every fact with
// every reported kind, so a kind filter or a kind breakdown never removes a
// message from the denominator.
//
// The projection is derived and refreshed in the background after the events
// pass (refreshAgentEventFactsSoon): a conversation is rebuilt when its
// messages read, their events, its prompts' authorship or its own attributes
// change (its stamp).

const agentEventFactsVersion = "1"

const agentEventFactsSchemaSQL = `
CREATE TABLE IF NOT EXISTS agent_event_facts (
  message_id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL,
  source TEXT NOT NULL,
  occurred_at TEXT,
  time_source TEXT NOT NULL,
  turn_id TEXT NOT NULL,
  prompted_by TEXT NOT NULL,
  prompt_words INTEGER,
  model TEXT NOT NULL,
  effort TEXT NOT NULL,
  provider TEXT NOT NULL,
  harness TEXT NOT NULL,
  subagent INTEGER NOT NULL,
  events INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS agent_event_facts_conversation_idx ON agent_event_facts(conversation_id);
CREATE INDEX IF NOT EXISTS agent_event_facts_time_idx ON agent_event_facts(occurred_at);
CREATE TABLE IF NOT EXISTS agent_event_fact_kinds (
  message_id TEXT NOT NULL,
  family TEXT NOT NULL,
  events INTEGER NOT NULL,
  PRIMARY KEY(message_id, family)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS agent_event_fact_conversations (
  conversation_id TEXT PRIMARY KEY,
  stamp TEXT NOT NULL
) WITHOUT ROWID;
-- A message the pass no longer holds leaves the facts at once; the next
-- refresh rebuilds its conversation from what the pass wrote instead.
CREATE TRIGGER IF NOT EXISTS agent_event_facts_message_gone AFTER DELETE ON agent_event_messages BEGIN
  DELETE FROM agent_event_facts WHERE message_id=OLD.message_id;
  DELETE FROM agent_event_fact_kinds WHERE message_id=OLD.message_id;
END;
`

// Who wrote the prompt that started a message's turn.
const (
	agentEventPromptPerson    = "person"
	agentEventPromptAutomated = "automated"
	agentEventPromptAgent     = "agent"
	agentEventPromptNone      = "none"
	agentEventPromptUnknown   = "unclassified"
)

// agentEventPromptLengths are the typed-length buckets of a person's prompt,
// in words, as the dashboard orders them.
var agentEventPromptLengths = []struct {
	Key      string
	Min, Max int
}{{"1-10", 1, 10}, {"11-30", 11, 30}, {"31-100", 31, 100}, {"101-300", 101, 300}, {"300+", 301, 1 << 30}}

func agentEventPromptLengthSQL(column string) string {
	var b strings.Builder
	b.WriteString("CASE")
	for _, bucket := range agentEventPromptLengths {
		fmt.Fprintf(&b, " WHEN %s BETWEEN %d AND %d THEN '%s'", column, bucket.Min, bucket.Max, bucket.Key)
	}
	b.WriteString(" END")
	return b.String()
}

// agentEventFactsBatch bounds the conversations one refresh transaction
// rebuilds, so the writer is held briefly; a test lowers it.
var agentEventFactsBatch = 200

func (c *Catalog) ensureAgentEventFactsSchema() error {
	_, err := c.DB.Exec(agentEventFactsSchemaSQL)
	return err
}

// agentEventFactsChainInterval spaces the rebuilds the events pass asks for,
// as Fixes does; a refresh with nothing changed is cheap regardless.
var agentEventFactsChainInterval = 10 * time.Minute

func (c *Catalog) refreshAgentEventFactsSoon(ctx context.Context) error {
	if built, _ := c.metaValue(ctx, "agent_event_facts_built_at"); built != "" {
		if at, ok := parseTime(built); ok && c.clock().Sub(at) < agentEventFactsChainInterval {
			return nil
		}
	}
	_, err := c.refreshAgentEventFacts(ctx)
	return err
}

// agentEventFactsStamps are each conversation's current inputs, in one
// string: what the pass read and found there, its prompts' authorship, its
// own attributes and the projection's version.
func agentEventFactsStamps(ctx context.Context, q queryer) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT a.conversation_id, a.n||':'||a.events||':'||a.imported||':'||COALESCE(p.n,0)||':'||COALESCE(p.words,0)||':'||
  COALESCE(c.model,'')||':'||COALESCE(c.effort,'')||':'||COALESCE(c.harness,'')||':'||COALESCE(c.provider,'')||':'||(c.parent_id IS NOT NULL)||':'||COALESCE(c.started_at,'')
 FROM (SELECT conversation_id, COUNT(*) n, SUM(events) events, MAX(imported_at) imported FROM agent_event_messages GROUP BY conversation_id) a
 JOIN conversations c ON c.id=a.conversation_id
 LEFT JOIN (SELECT conversation_id, COUNT(*) n, SUM(typed_words) words FROM message_authorship
   WHERE conversation_id IN (SELECT DISTINCT conversation_id FROM agent_event_messages) GROUP BY conversation_id) p ON p.conversation_id=a.conversation_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, stamp string
		if err := rows.Scan(&id, &stamp); err != nil {
			return nil, err
		}
		out[id] = agentEventFactsVersion + "/" + stamp
	}
	return out, rows.Err()
}

type agentEventFact struct {
	MessageID, ConversationID, Source string
	OccurredAt                        sql.NullString
	TimeSource                        string
	TurnID, PromptedBy                string
	PromptWords                       sql.NullInt64
	Model, Effort, Provider, Harness  string
	Subagent                          bool
	Events                            int
	order                             float64
}

type agentEventPrompt struct {
	ID         string
	Order      float64
	Role       string
	Typed      int
	Classified bool
}

// agentEventPromptedBy says who wrote a turn's prompt. A subagent's first
// prompt is its delegating agent's even when the harness records it as a
// user message.
func agentEventPromptedBy(prompt *agentEventPrompt, subagent bool) string {
	switch {
	case prompt == nil:
		return agentEventPromptNone
	case prompt.Role == "agent":
		return agentEventPromptAgent
	case prompt.Typed > 0:
		return agentEventPromptPerson
	case subagent:
		return agentEventPromptAgent
	case !prompt.Classified:
		return agentEventPromptUnknown
	}
	return agentEventPromptAutomated
}

// agentEventTurns places each fact in the turn of the last prompt at or
// before it (prompts sorted by order). Facts before any prompt share one turn
// of their own.
func agentEventTurns(conversation string, subagent bool, prompts []agentEventPrompt, facts []*agentEventFact) {
	for _, fact := range facts {
		at := sort.Search(len(prompts), func(i int) bool { return prompts[i].Order > fact.order }) - 1
		if at < 0 {
			fact.TurnID, fact.PromptedBy = conversation+":start", agentEventPromptNone
			continue
		}
		prompt := &prompts[at]
		fact.TurnID, fact.PromptedBy = prompt.ID, agentEventPromptedBy(prompt, subagent)
		if fact.PromptedBy == agentEventPromptPerson {
			fact.PromptWords = sql.NullInt64{Int64: int64(prompt.Typed), Valid: true}
		}
	}
}

// refreshAgentEventFacts rebuilds the conversations whose stamps changed and
// drops those the pass no longer holds. It reads outside the writer, then
// writes each batch in one transaction. It returns the conversations rebuilt.
func (c *Catalog) refreshAgentEventFacts(ctx context.Context) (int, error) {
	current, err := agentEventFactsStamps(ctx, c.reader())
	if err != nil {
		return 0, err
	}
	stored := map[string]string{}
	rows, err := c.reader().QueryContext(ctx, `SELECT conversation_id, stamp FROM agent_event_fact_conversations`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var id, stamp string
		if err := rows.Scan(&id, &stamp); err != nil {
			rows.Close()
			return 0, err
		}
		stored[id] = stamp
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var changed, gone []string
	for id, stamp := range current {
		if stored[id] != stamp {
			changed = append(changed, id)
		}
	}
	for id := range stored {
		if _, ok := current[id]; !ok {
			gone = append(gone, id)
		}
	}
	sort.Strings(changed)
	sort.Strings(gone)
	rebuilt := 0
	for start := 0; start < len(changed) || start == 0 && len(gone) > 0; start += agentEventFactsBatch {
		if err := ctx.Err(); err != nil {
			return rebuilt, err
		}
		batch := changed[start:min(start+agentEventFactsBatch, len(changed))]
		facts, kinds, err := c.agentEventFactsFor(ctx, batch)
		if err != nil {
			return rebuilt, err
		}
		drop := batch
		if start == 0 {
			drop = append(append([]string(nil), gone...), batch...)
		}
		if err := c.writeTransaction(ctx, "agent event facts", func(tx *sql.Tx) error {
			for _, id := range drop {
				for _, statement := range []string{
					`DELETE FROM agent_event_fact_kinds WHERE message_id IN (SELECT message_id FROM agent_event_facts WHERE conversation_id=?)`,
					`DELETE FROM agent_event_facts WHERE conversation_id=?`,
					`DELETE FROM agent_event_fact_conversations WHERE conversation_id=?`,
				} {
					if _, err := tx.ExecContext(ctx, statement, id); err != nil {
						return err
					}
				}
			}
			insert, err := tx.PrepareContext(ctx, `INSERT INTO agent_event_facts(message_id,conversation_id,source,occurred_at,time_source,turn_id,prompted_by,prompt_words,model,effort,provider,harness,subagent,events)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
			if err != nil {
				return err
			}
			defer insert.Close()
			for _, f := range facts {
				if _, err := insert.ExecContext(ctx, f.MessageID, f.ConversationID, f.Source, f.OccurredAt, f.TimeSource, f.TurnID, f.PromptedBy, f.PromptWords,
					f.Model, f.Effort, f.Provider, f.Harness, f.Subagent, f.Events); err != nil {
					return err
				}
			}
			kind, err := tx.PrepareContext(ctx, `INSERT INTO agent_event_fact_kinds(message_id,family,events) VALUES(?,?,?)`)
			if err != nil {
				return err
			}
			defer kind.Close()
			for key, n := range kinds {
				if _, err := kind.ExecContext(ctx, key[0], key[1], n); err != nil {
					return err
				}
			}
			for _, id := range batch {
				if _, err := tx.ExecContext(ctx, `INSERT INTO agent_event_fact_conversations(conversation_id,stamp) VALUES(?,?)
 ON CONFLICT(conversation_id) DO UPDATE SET stamp=excluded.stamp`, id, current[id]); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return rebuilt, err
		}
		rebuilt += len(batch)
	}
	if err := c.writeTransaction(ctx, "agent event facts built", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('agent_event_facts_built_at',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, formatTime(c.clock()))
		return err
	}); err != nil {
		return rebuilt, err
	}
	return rebuilt, nil
}

// agentEventFactsFor reads the facts of these conversations: their messages
// read with the turn each belongs to, and their events by kind at the
// threshold, for kinds the product reports.
func (c *Catalog) agentEventFactsFor(ctx context.Context, conversations []string) ([]*agentEventFact, map[[2]string]int, error) {
	kinds := map[[2]string]int{}
	if len(conversations) == 0 {
		return nil, kinds, nil
	}
	read, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	defer read.Rollback()
	args := make([]any, len(conversations))
	for i, id := range conversations {
		args[i] = id
	}
	in := placeholders(len(conversations))
	rows, err := read.QueryContext(ctx, `SELECT a.message_id, a.conversation_id, a.source, a.occurred_at, c.started_at, a.events, COALESCE(m.source_order,0),
  COALESCE(NULLIF(m.model,''),c.model,''), COALESCE(NULLIF(m.effort,''),c.effort,''), COALESCE(c.provider,''), COALESCE(c.harness,''), c.parent_id IS NOT NULL
 FROM agent_event_messages a JOIN conversations c ON c.id=a.conversation_id LEFT JOIN messages m ON m.id=a.message_id
 WHERE a.conversation_id IN (`+in+`)`, args...)
	if err != nil {
		return nil, nil, err
	}
	byConversation := map[string][]*agentEventFact{}
	subagents := map[string]bool{}
	var facts []*agentEventFact
	for rows.Next() {
		f := &agentEventFact{}
		var started sql.NullString
		if err := rows.Scan(&f.MessageID, &f.ConversationID, &f.Source, &f.OccurredAt, &started, &f.Events, &f.order, &f.Model, &f.Effort, &f.Provider, &f.Harness, &f.Subagent); err != nil {
			rows.Close()
			return nil, nil, err
		}
		// Some imports (TL1's Codex transcripts) record no message times;
		// their conversation's start stands in, as Fixes does.
		switch {
		case f.OccurredAt.Valid && f.OccurredAt.String != "":
			f.TimeSource = "message"
		case started.Valid && started.String != "":
			f.OccurredAt, f.TimeSource = started, "conversation"
		default:
			f.OccurredAt, f.TimeSource = sql.NullString{}, ""
		}
		facts = append(facts, f)
		byConversation[f.ConversationID] = append(byConversation[f.ConversationID], f)
		subagents[f.ConversationID] = f.Subagent
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows, err = read.QueryContext(ctx, `SELECT m.id, m.conversation_id, COALESCE(m.source_order,0), m.role, COALESCE(a.typed_words,0), a.message_id IS NOT NULL
 FROM messages m LEFT JOIN message_authorship a ON a.message_id=m.id
 WHERE m.conversation_id IN (`+in+`) AND m.kind='message' AND m.role IN ('user','agent') AND m.selected=1`, args...)
	if err != nil {
		return nil, nil, err
	}
	prompts := map[string][]agentEventPrompt{}
	for rows.Next() {
		var p agentEventPrompt
		var conversation string
		if err := rows.Scan(&p.ID, &conversation, &p.Order, &p.Role, &p.Typed, &p.Classified); err != nil {
			rows.Close()
			return nil, nil, err
		}
		prompts[conversation] = append(prompts[conversation], p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	for conversation, list := range byConversation {
		found := prompts[conversation]
		sort.Slice(found, func(i, j int) bool {
			if found[i].Order != found[j].Order {
				return found[i].Order < found[j].Order
			}
			return found[i].ID < found[j].ID
		})
		agentEventTurns(conversation, subagents[conversation], found, list)
	}
	reported := map[string]bool{}
	for _, family := range agentEventFamilies {
		reported[family.Key] = true
	}
	rows, err = read.QueryContext(ctx, fmt.Sprintf(`SELECT message_id, family, COUNT(*) FROM agent_events
 WHERE conversation_id IN (`+in+`) AND probability>=%g AND message_id IN (SELECT message_id FROM agent_event_messages WHERE conversation_id IN (`+in+`))
 GROUP BY message_id, family`, agentEventThreshold), append(args, args...)...)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var message, family string
		var n int
		if err := rows.Scan(&message, &family, &n); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if reported[family] {
			kinds[[2]string{message, family}] = n
		}
	}
	rows.Close()
	return facts, kinds, rows.Err()
}

// refreshAgentEventFactsLogged refreshes the projection after an events pass,
// reporting a failure without failing the pass.
func (c *Catalog) refreshAgentEventFactsLogged(ctx context.Context, soon bool) {
	var err error
	if soon {
		err = c.refreshAgentEventFactsSoon(ctx)
	} else {
		_, err = c.refreshAgentEventFacts(ctx)
	}
	if err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "Agent event facts: %v\n", err)
	}
}

// agentEventKindsSQL is the reported kinds as a table: key, group and their
// place in the taxonomy.
func agentEventKindsSQL() string {
	parts := make([]string, len(agentEventFamilies))
	for i, family := range agentEventFamilies {
		parts[i] = fmt.Sprintf("SELECT '%s' AS family,'%s' AS family_group,%d AS family_order", family.Key, strings.ReplaceAll(family.Group, "'", "''"), i)
	}
	return "(" + strings.Join(parts, " UNION ALL ") + ")"
}

// agentEventRateDataset serves one row per (message read, reported kind),
// with that kind's events in the message (often none). Every message read is
// in every kind, so a rate's denominator, the distinct messages, turns or
// conversations under the filters, is the same whichever kinds are chosen or
// broken down; the numerator counts only the chosen kinds' events.
var agentEventRateDataset = func() sqlDataset {
	columns := map[string]string{
		"id": "(f.message_id||':'||k.family)", "message_id": "f.message_id", "conversation_id": "f.conversation_id",
		"workspace_id": "c.workspace_id", "title": "COALESCE(NULLIF(c.agent_nickname,''),w.title,c.id)", "repository_name": "r.display_name",
		"provider": "f.provider", "model": "NULLIF(f.model,'')", "effort": "NULLIF(f.effort,'')",
		"model_effort": "(COALESCE(NULLIF(f.model,''),'Unknown model')||' · '||COALESCE(NULLIF(f.effort,''),'effort not recorded'))",
		"harness":      "NULLIF(f.harness,'')", "subagent": "(f.subagent=1)", "agent_role": "CASE WHEN f.subagent=1 THEN 'subagent' ELSE 'main' END",
		"source": "f.source", "turn_id": "f.turn_id", "prompted_by": "f.prompted_by", "prompt_words": "f.prompt_words",
		"prompt_length": agentEventPromptLengthSQL("f.prompt_words"),
		"family":        "k.family", "family_group": "k.family_group",
		"events":    "COALESCE(x.events,0)",
		"has_event": "(COALESCE(x.events,0)>0)",
		// Days in this Mac's time zone when read, as the other datasets.
		"occurred_at": "f.occurred_at", "time_source": "NULLIF(f.time_source,'')",
		"day": "date(f.occurred_at,'localtime')", "week": "date(f.occurred_at,'localtime','-6 days','weekday 1')", "month": "strftime('%Y-%m',f.occurred_at,'localtime')",
	}
	return sqlDataset{
		from: `FROM agent_event_facts f CROSS JOIN ` + agentEventKindsSQL() + ` k LEFT JOIN agent_event_fact_kinds x ON x.message_id=f.message_id AND x.family=k.family` +
			` LEFT JOIN conversations c ON c.id=f.conversation_id LEFT JOIN workspaces w ON w.id=c.workspace_id LEFT JOIN repositories r ON r.id=w.repository_id`,
		lookups:    map[string]bool{"message_id": true, "conversation_id": true},
		columns:    columns,
		timestamps: map[string]string{"occurred_at": "f.occurred_at"},
		optionalJoins: map[string]string{
			"x": " LEFT JOIN agent_event_fact_kinds x ON x.message_id=f.message_id AND x.family=k.family",
			"c": " LEFT JOIN conversations c ON c.id=f.conversation_id",
			"w": " LEFT JOIN workspaces w ON w.id=c.workspace_id",
			"r": " LEFT JOIN repositories r ON r.id=w.repository_id",
		},
		pageFrom: "FROM agent_event_facts f",
	}
}()
