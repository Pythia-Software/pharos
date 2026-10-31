package archive

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// Agent events (experimental) are sentences in agents' visible replies and
// thinking where something happened worth a reader's attention: an obstacle,
// a surprise, a discovery, a workaround, an unchecked claim, getting around a
// safeguard, and so on. A keyword pass finds candidate sentences and a small
// encoder decides whether each really expresses its family, and which
// sentences say what it was about and what the agent did. The JAYHAWK study
// built the families and the model; see docs/agent-events.md.
//
// Until the model runs inside Pharos, events are imported from the study's
// predictions with `pharos agent-events import FILE`. Both tables are derived:
// deleting them loses nothing that a new import or run cannot rebuild.
//
//   - agent_event_messages: every agent message the pass read, with or without
//     events, the denominator for rates.
//   - agent_events: one row per (message, sentence, family) the model scored,
//     with its probability. The datasets show events at or above
//     agentEventThreshold, fixed per model so counts stay comparable.

const agentEventsSchemaSQL = `
CREATE TABLE IF NOT EXISTS agent_event_messages (
  message_id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL,
  source TEXT NOT NULL,
  occurred_at TEXT,
  day TEXT,
  candidates INTEGER NOT NULL,
  events INTEGER NOT NULL,
  model TEXT NOT NULL,
  lexicon TEXT NOT NULL,
  imported_at TEXT NOT NULL,
  input_hash TEXT
);
CREATE INDEX IF NOT EXISTS agent_event_messages_day_idx ON agent_event_messages(day);
CREATE INDEX IF NOT EXISTS agent_event_messages_conversation_idx ON agent_event_messages(conversation_id);
CREATE TABLE IF NOT EXISTS agent_events (
  id INTEGER PRIMARY KEY,
  message_id TEXT NOT NULL,
  conversation_id TEXT NOT NULL,
  source TEXT NOT NULL,
  occurred_at TEXT,
  day TEXT,
  family TEXT NOT NULL,
  probability REAL NOT NULL,
  sentence_start INTEGER NOT NULL,
  sentence_end INTEGER NOT NULL,
  sentence TEXT NOT NULL,
  subject TEXT NOT NULL,
  response TEXT NOT NULL,
  subject_ranges TEXT NOT NULL,
  response_ranges TEXT NOT NULL,
  model TEXT NOT NULL,
  UNIQUE(message_id, sentence_start, family)
);
CREATE INDEX IF NOT EXISTS agent_events_message_idx ON agent_events(message_id);
CREATE INDEX IF NOT EXISTS agent_events_day_idx ON agent_events(day, family);
CREATE INDEX IF NOT EXISTS agent_events_conversation_idx ON agent_events(conversation_id);
`

// agentEventThreshold is the probability at or above which a scored
// candidate counts as an event. It belongs to the model: a new model may need
// its own, and counts across a model change are not comparable.
const agentEventThreshold = 0.5

// agentEventFamily is one family the product reports: its label, the group
// the dashboard shows it under, and its definition.
type agentEventFamily struct{ Key, Label, Group, Definition string }

// agentEventFamilies are the families the product reports (JAYHAWK lexicon 2).
// Imported rows for other families are skipped.
var agentEventFamilies = []agentEventFamily{
	{"obstacle", "Obstacle or roadblock", "Friction", "A specific problem prevented or derailed an intended action."},
	{"env_friction", "Environment friction", "Friction", "An obstacle from the environment: sandbox, permissions, network, installs, versions, CI, locks, limits."},
	{"stale_guidance", "Out-of-date docs or instructions", "Friction", "Documentation, instructions, plans or memory did not match reality."},
	{"preexisting_failure", "Pre-existing or flaky failure", "Friction", "Failures the agent said were already there, unrelated to its change, or flaky."},
	{"missing_capability", "Missing tool or capability", "Friction", "The agent lacked a tool, access, permission or ability it needed."},
	{"ambiguous_request", "Unclear or conflicting instructions", "Friction", "The request or requirements were unclear, underspecified or conflicting."},
	{"stuck", "Stuck", "Friction", "Repeated attempts were not making progress."},
	{"gave_up", "Giving up or handing back", "Friction", "The agent stopped trying or handed the task back."},
	{"discovery", "Discovery", "Reactions and learning", "The agent learned a non-obvious fact about the system, code or data."},
	{"confusion", "Confusion", "Reactions and learning", "The agent did not understand something or was unsure why it happened."},
	{"surprise", "Surprise", "Reactions and learning", "Something contradicted what the agent expected."},
	{"own_mistake", "Own mistake or correction", "Reactions and learning", "The agent caught or corrected its own error."},
	{"unblocking_insight", "Lesson that unblocked progress", "Reactions and learning", "A realization or change resolved a problem and let work proceed."},
	{"circumventing_constraint", "Getting around an instruction or safeguard", "Worth a second look", "The agent bypassed, disabled or weakened a check, hook, test, sandbox, permission or instruction to get past it."},
	{"risk_flagged", "Risk raised", "Choices you may not see", "The agent warned about a risk, side effect or caveat."},
	{"deferred", "Deferred or skipped work", "Choices you may not see", "The agent left part of the work undone, stubbed or for later."},
	{"workaround", "Workaround", "Choices you may not see", "The agent took an alternative path because the direct one was blocked or unsuitable."},
	{"unverified", "Unchecked claim", "Choices you may not see", "The agent said something was not tested, run or verified."},
	{"assumption", "Assumption", "Choices you may not see", "The agent proceeded on an assumption it did not check."},
	{"tech_debt", "Technical debt noticed", "Choices you may not see", "The agent noticed code-quality problems in existing code."},
	{"pushback", "Disagreeing with the user", "Choices you may not see", "The agent disagreed with or recommended against what was asked."},
}

func agentEventFamilyByKey(key string) (agentEventFamily, bool) {
	for _, family := range agentEventFamilies {
		if family.Key == key {
			return family, true
		}
	}
	return agentEventFamily{}, false
}

// agentEventFamilySQL maps the family key column to one of its attributes.
func agentEventFamilySQL(column string, attribute func(agentEventFamily) string) string {
	var b strings.Builder
	b.WriteString("CASE " + column)
	for _, family := range agentEventFamilies {
		fmt.Fprintf(&b, " WHEN '%s' THEN '%s'", family.Key, strings.ReplaceAll(attribute(family), "'", "''"))
	}
	b.WriteString(" END")
	return b.String()
}

func (c *Catalog) ensureAgentEventsSchema() error {
	if _, err := c.DB.Exec(agentEventsSchemaSQL); err != nil {
		return err
	}
	// input_hash came with the events pass (agent_events_run.go); an import
	// leaves it empty, so the pass scores those messages again.
	if ok, err := c.hasColumn("agent_event_messages", "input_hash"); err != nil || ok {
		return err
	}
	_, err := c.DB.Exec(`ALTER TABLE agent_event_messages ADD COLUMN input_hash TEXT`)
	return err
}

// agentEventDataset serves one row per event: a scored candidate at or above
// the threshold, with its sentence, what it was about and what the agent did.
var agentEventDataset = func() sqlDataset {
	columns := map[string]string{
		"id": "('e'||e.id)", "message_id": "e.message_id", "conversation_id": "e.conversation_id",
		"workspace_id": "c.workspace_id", "title": "w.title", "repository_name": "r.display_name",
		"provider": "c.provider", "model": "COALESCE(m.model,c.model)", "subagent": "(c.parent_id IS NOT NULL)",
		"source":       "e.source",
		"in_thinking":  "(e.source='thinking')",
		"family":       "e.family",
		"family_label": agentEventFamilySQL("e.family", func(f agentEventFamily) string { return f.Label }),
		"family_group": agentEventFamilySQL("e.family", func(f agentEventFamily) string { return f.Group }),
		"probability":  "e.probability",
		"sentence":     "e.sentence", "subject": "e.subject", "response": "e.response",
		"has_subject": "(e.subject<>'' AND e.subject<>e.sentence)", "has_response": "(e.response<>'')",
		"occurred_at": "e.occurred_at", "day": "e.day", "week": "date(e.day,'-6 days','weekday 1')", "month": "substr(e.day,1,7)",
		"event_count": "1",
		"event_model": "e.model",
	}
	return sqlDataset{
		from: `FROM agent_events e JOIN conversations c ON c.id=e.conversation_id LEFT JOIN messages m ON m.id=e.message_id
			LEFT JOIN workspaces w ON w.id=c.workspace_id LEFT JOIN repositories r ON r.id=w.repository_id`,
		base:      fmt.Sprintf("e.probability>=%g", agentEventThreshold),
		countFrom: "FROM agent_events e", countAlias: "e",
		lookups:    map[string]bool{"message_id": true, "conversation_id": true, "day": true},
		columns:    columns,
		timestamps: map[string]string{"occurred_at": "e.occurred_at"},
		optionalJoins: map[string]string{
			"c": " JOIN conversations c ON c.id=e.conversation_id",
			"m": " LEFT JOIN messages m ON m.id=e.message_id",
			"w": " LEFT JOIN workspaces w ON w.id=c.workspace_id",
			"r": " LEFT JOIN repositories r ON r.id=w.repository_id",
		},
		pageFrom: "FROM agent_events e",
	}
}()

// agentEventMessageDataset serves one row per agent message the pass read,
// so rates can divide events by the messages they could have come from.
var agentEventMessageDataset = func() sqlDataset {
	columns := map[string]string{
		"id": "a.message_id", "message_id": "a.message_id", "conversation_id": "a.conversation_id",
		"workspace_id": "c.workspace_id", "title": "w.title", "repository_name": "r.display_name",
		"provider": "c.provider", "subagent": "(c.parent_id IS NOT NULL)",
		"source": "a.source", "candidates": "a.candidates",
		"events":        fmt.Sprintf("(SELECT count(*) FROM agent_events e WHERE e.message_id=a.message_id AND e.probability>=%g)", agentEventThreshold),
		"has_event":     fmt.Sprintf("EXISTS(SELECT 1 FROM agent_events e WHERE e.message_id=a.message_id AND e.probability>=%g)", agentEventThreshold),
		"message_count": "1",
		"occurred_at":   "a.occurred_at", "day": "a.day", "week": "date(a.day,'-6 days','weekday 1')", "month": "substr(a.day,1,7)",
		"event_model": "a.model",
	}
	return sqlDataset{
		from: `FROM agent_event_messages a JOIN conversations c ON c.id=a.conversation_id
			LEFT JOIN workspaces w ON w.id=c.workspace_id LEFT JOIN repositories r ON r.id=w.repository_id`,
		countFrom: "FROM agent_event_messages a", countAlias: "a",
		lookups:    map[string]bool{"id": true, "message_id": true, "conversation_id": true, "day": true},
		columns:    columns,
		timestamps: map[string]string{"occurred_at": "a.occurred_at"},
		optionalJoins: map[string]string{
			"c": " JOIN conversations c ON c.id=a.conversation_id",
			"w": " LEFT JOIN workspaces w ON w.id=c.workspace_id",
			"r": " LEFT JOIN repositories r ON r.id=w.repository_id",
		},
		pageFrom: "FROM agent_event_messages a",
	}
}()

// agentEventImportLine is one line of an import file (format
// pharos-agent-events-v1): a header, a scanned message, or a scored event.
// Ranges are zero-based UTF-8 byte offsets into messages.text, end exclusive.
type agentEventImportLine struct {
	Kind          string   `json:"kind"`
	Format        string   `json:"format"`
	Model         string   `json:"model"`
	Lexicon       string   `json:"lexicon"`
	MessageID     string   `json:"message_id"`
	MessageSHA256 string   `json:"message_sha256"`
	Source        string   `json:"source"`
	Candidates    int      `json:"candidates"`
	Family        string   `json:"family"`
	Probability   float64  `json:"probability"`
	Sentence      [2]int   `json:"sentence"`
	Subject       [][2]int `json:"subject"`
	Response      [][2]int `json:"response"`
}

// AgentEventImport reports what an import wrote and what it skipped.
type AgentEventImport struct {
	Model            string         `json:"model"`
	Lexicon          string         `json:"lexicon"`
	Messages         int            `json:"messages"`
	Events           int            `json:"events_scored"`
	EventsAtCutoff   int            `json:"events_at_threshold"`
	SkippedMissing   int            `json:"skipped_missing_message"`
	SkippedChanged   int            `json:"skipped_changed_text"`
	SkippedFamily    int            `json:"skipped_family"`
	SkippedRange     int            `json:"skipped_bad_range"`
	SkippedUnscanned int            `json:"skipped_event_without_message"`
	ByFamily         map[string]int `json:"events_at_threshold_by_family"`
}

type agentEventMessage struct {
	conversationID, text, occurredAt, day string
}

// ImportAgentEvents replaces the events of every message in the file with the
// file's. A message must exist with the same text (by SHA-256) it was scored
// on; ranges must fall on UTF-8 boundaries of that text. Everything is written
// in one transaction.
func (c *Catalog) ImportAgentEvents(ctx context.Context, path string) (AgentEventImport, error) {
	result := AgentEventImport{ByFamily: map[string]int{}}
	file, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 1<<20)
	readLine := func() (agentEventImportLine, bool, error) {
		raw, err := reader.ReadBytes('\n')
		if len(strings.TrimSpace(string(raw))) == 0 {
			if err == io.EOF {
				return agentEventImportLine{}, false, nil
			}
			if err != nil {
				return agentEventImportLine{}, false, err
			}
		}
		var line agentEventImportLine
		if jerr := json.Unmarshal(raw, &line); jerr != nil {
			return line, false, fmt.Errorf("import line: %w", jerr)
		}
		return line, true, nil
	}
	header, ok, err := readLine()
	if err != nil {
		return result, err
	}
	if !ok || header.Kind != "header" || header.Format != "pharos-agent-events-v1" || header.Model == "" {
		return result, fmt.Errorf("%s: not a pharos-agent-events-v1 file", path)
	}
	result.Model, result.Lexicon = header.Model, header.Lexicon

	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	lookup, err := tx.PrepareContext(ctx, `SELECT conversation_id, text, COALESCE(created_at,'') FROM messages WHERE id=?`)
	if err != nil {
		return result, err
	}
	defer lookup.Close()
	messages := map[string]*agentEventMessage{}
	resolve := func(line agentEventImportLine) (*agentEventMessage, string) {
		if cached, ok := messages[line.MessageID]; ok {
			return cached, ""
		}
		var m agentEventMessage
		if err := lookup.QueryRowContext(ctx, line.MessageID).Scan(&m.conversationID, &m.text, &m.occurredAt); err != nil {
			if err == sql.ErrNoRows {
				return nil, "missing"
			}
			return nil, err.Error()
		}
		sum := sha256.Sum256([]byte(m.text))
		if hex.EncodeToString(sum[:]) != line.MessageSHA256 {
			return nil, "changed"
		}
		if at, ok := parseTime(m.occurredAt); ok {
			m.day = at.Local().Format("2006-01-02")
		}
		messages[line.MessageID] = &m
		return &m, ""
	}
	type pendingEvent struct {
		line    agentEventImportLine
		message *agentEventMessage
	}
	var events []pendingEvent
	scanned := map[string]agentEventImportLine{}
	for {
		line, ok, err := readLine()
		if err != nil {
			return result, err
		}
		if !ok {
			break
		}
		switch line.Kind {
		case "message":
			if _, problem := resolve(line); problem != "" {
				if problem == "missing" {
					result.SkippedMissing++
				} else if problem == "changed" {
					result.SkippedChanged++
				} else {
					return result, fmt.Errorf("message %s: %s", line.MessageID, problem)
				}
				continue
			}
			scanned[line.MessageID] = line
		case "event":
			if _, ok := agentEventFamilyByKey(line.Family); !ok {
				result.SkippedFamily++
				continue
			}
			m, problem := resolve(line)
			if problem != "" {
				if problem == "missing" {
					result.SkippedMissing++
				} else if problem == "changed" {
					result.SkippedChanged++
				} else {
					return result, fmt.Errorf("message %s: %s", line.MessageID, problem)
				}
				continue
			}
			events = append(events, pendingEvent{line, m})
		}
	}
	now := formatTime(time.Now())
	clear, err := tx.PrepareContext(ctx, `DELETE FROM agent_events WHERE message_id=?`)
	if err != nil {
		return result, err
	}
	defer clear.Close()
	insertMessage, err := tx.PrepareContext(ctx, `INSERT INTO agent_event_messages(message_id,conversation_id,source,occurred_at,day,candidates,events,model,lexicon,imported_at)
		VALUES(?,?,?,?,?,?,0,?,?,?) ON CONFLICT(message_id) DO UPDATE SET conversation_id=excluded.conversation_id, source=excluded.source,
		occurred_at=excluded.occurred_at, day=excluded.day, candidates=excluded.candidates, events=0, model=excluded.model, lexicon=excluded.lexicon, imported_at=excluded.imported_at`)
	if err != nil {
		return result, err
	}
	defer insertMessage.Close()
	for id, line := range scanned {
		m := messages[id]
		if _, err := clear.ExecContext(ctx, id); err != nil {
			return result, err
		}
		if _, err := insertMessage.ExecContext(ctx, id, m.conversationID, line.Source, nilIfEmpty(m.occurredAt), nilIfEmpty(m.day), line.Candidates, header.Model, header.Lexicon, now); err != nil {
			return result, err
		}
		result.Messages++
	}
	insertEvent, err := tx.PrepareContext(ctx, `INSERT INTO agent_events(message_id,conversation_id,source,occurred_at,day,family,probability,
		sentence_start,sentence_end,sentence,subject,response,subject_ranges,response_ranges,model) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(message_id,sentence_start,family) DO UPDATE SET probability=excluded.probability, sentence_end=excluded.sentence_end,
		sentence=excluded.sentence, subject=excluded.subject, response=excluded.response, subject_ranges=excluded.subject_ranges,
		response_ranges=excluded.response_ranges, model=excluded.model`)
	if err != nil {
		return result, err
	}
	defer insertEvent.Close()
	counted := map[string]int{}
	for _, event := range events {
		line, m := event.line, event.message
		scan, ok := scanned[line.MessageID]
		if !ok {
			result.SkippedUnscanned++
			continue
		}
		sentence, ok := agentEventText(m.text, [][2]int{line.Sentence})
		if !ok {
			result.SkippedRange++
			continue
		}
		subject, ok1 := agentEventText(m.text, line.Subject)
		response, ok2 := agentEventText(m.text, line.Response)
		if !ok1 || !ok2 {
			result.SkippedRange++
			continue
		}
		subjectRanges, _ := json.Marshal(nonNilRanges(line.Subject))
		responseRanges, _ := json.Marshal(nonNilRanges(line.Response))
		if _, err := insertEvent.ExecContext(ctx, line.MessageID, m.conversationID, scan.Source, nilIfEmpty(m.occurredAt), nilIfEmpty(m.day), line.Family, line.Probability,
			line.Sentence[0], line.Sentence[1], sentence, subject, response, string(subjectRanges), string(responseRanges), header.Model); err != nil {
			return result, err
		}
		result.Events++
		if line.Probability >= agentEventThreshold {
			result.EventsAtCutoff++
			result.ByFamily[line.Family]++
			counted[line.MessageID]++
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE agent_event_messages SET events=(SELECT count(*) FROM agent_events e
		WHERE e.message_id=agent_event_messages.message_id AND e.probability>=%g) WHERE imported_at=?`, agentEventThreshold), now); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

// agentEventText joins the text of byte ranges in order, with a space
// between ranges. ok is false when a range is out of bounds or splits a
// UTF-8 character.
func agentEventText(text string, ranges [][2]int) (string, bool) {
	parts := make([]string, 0, len(ranges))
	for _, r := range ranges {
		start, end := r[0], r[1]
		if start < 0 || end > len(text) || start >= end {
			return "", false
		}
		if (start < len(text) && !utf8.RuneStart(text[start])) || (end < len(text) && !utf8.RuneStart(text[end])) {
			return "", false
		}
		parts = append(parts, strings.TrimSpace(text[start:end]))
	}
	return strings.Join(parts, " "), true
}

func nonNilRanges(ranges [][2]int) [][2]int {
	if ranges == nil {
		return [][2]int{}
	}
	return ranges
}
