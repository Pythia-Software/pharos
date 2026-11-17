package archive

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// conductorEventTable is Conductor's raw session event log, kept beside
// session_messages since its schema migration 151 (October 2026). Each row is
// one event as the agent or the app emitted it: a userMessage the user sent,
// an agent event wrapping the harness's own payload (rawPayload), a git
// checkpoint, and so on. Conductor's client projects the events it shows into
// session_messages: a userMessage becomes the user row with the message's ID,
// and an agent event becomes rows with the deterministic IDs
// cloud-feed:<session>:<event>:<index>. Both tables therefore describe one
// conversation, and reading both naively would count every message twice.
//
// Stored rows stay the parser's input, unchanged, for every event they
// project, so message IDs and text are the same with or without the event
// log, and databases from before it parse exactly as they did. An event adds
// a parser input only when Conductor would project it and no row does; it
// then takes the ID that row would have, so a row that appears later is the
// same message. Reverted events (reverted_at) were removed from the
// conversation and add nothing. Events Conductor never projects (rate limits,
// tool progress, command lifecycle, checkpoints, in-progress Codex items) are
// left as retained evidence, not messages.
//
// Codex events are not rebuilt: Conductor translates Codex app-server items
// into these rows with a client-side mapping of its own, which Pharos does not
// reproduce, and the Codex source reads the same threads from Codex's rollout
// files.
const conductorEventTable = "session_events"

// conductorParserInput is one row the session parser reads: a stored message
// row, or a row rebuilt from an unprojected event. table and ordinal name the
// retained row it came from, for its evidence locator.
type conductorParserInput struct {
	row     map[string]any
	table   string
	ordinal int
}

// conductorEventColumns returns the event log's columns, or nil when the
// database has no usable event log (every database before migration 151).
func conductorEventColumns(q queryer, tables map[string]bool) (map[string]bool, error) {
	if !tables[conductorEventTable] {
		return nil, nil
	}
	columns, err := tableColumns(q, conductorEventTable)
	if err != nil {
		return nil, err
	}
	if !columns["session_id"] || !columns["event_id"] || !columns["content"] {
		return nil, nil
	}
	return columns, nil
}

// conductorEventOrder is the event log's own sequence for one session.
func conductorEventOrder(columns map[string]bool) string {
	if columns["session_index"] {
		return " ORDER BY session_index,rowid"
	}
	return " ORDER BY rowid"
}

// conductorClaudeFamily reports agent types whose agent events carry a
// Claude-shaped payload that Conductor stores as is (cursor, muse and ACP
// agents share that shape in Conductor's client).
func conductorClaudeFamily(agentType string) bool {
	switch strings.ToLower(strings.TrimSpace(agentType)) {
	case "claude", "cursor", "muse", "acp":
		return true
	}
	return false
}

// unprojectedConductorEvents returns parser inputs for the session's events
// that no stored row projects. Live reads list the session's event IDs and
// types from the start of each row first, and read whole events only for the
// candidates, so the content of projected events is never read twice.
func (a *conductorAdapter) unprojectedConductorEvents(tx *sql.Tx, sid, agentType string, rows []map[string]any, tables map[string]bool) ([]conductorParserInput, error) {
	if a.view != nil && a.view.recordRevision != nil {
		// A retained observation without an event input predates event
		// retention, and parses as it did when it was taken.
		events, err := a.view.recordStore.conductorEventRows(*a.view.recordRevision, sid)
		if err != nil || events == nil {
			return nil, err
		}
		return conductorUnprojectedEvents(sid, agentType, rows, events), nil
	}
	columns, err := conductorEventColumns(tx, tables)
	if err != nil || columns == nil {
		return nil, err
	}
	order := conductorEventOrder(columns)
	typeColumn := ""
	if columns["event_type"] {
		typeColumn = ",event_type"
	}
	listed, err := queryMapsContext(a.context(), tx, "SELECT event_id"+typeColumn+" FROM "+conductorEventTable+" WHERE session_id=?"+order, sid)
	if err != nil || len(listed) == 0 {
		return nil, err
	}
	projected := conductorProjectedEvents(sid, rows)
	family := conductorClaudeFamily(agentType)
	ordinals := map[string]int{}
	candidates := []any{}
	for ordinal, event := range listed {
		id := firstString(event["event_id"])
		if id == "" || projected[id] {
			continue
		}
		kind := firstString(event["event_type"])
		if kind == "userMessage" || kind == "" || (kind == "agent" && family) {
			if _, seen := ordinals[id]; !seen {
				ordinals[id] = ordinal
				candidates = append(candidates, id)
			}
		}
	}
	events := make([]map[string]any, 0, len(candidates))
	for len(candidates) > 0 {
		batch := candidates[:min(len(candidates), 500)]
		found, err := queryMapsContext(a.context(), tx, "SELECT * FROM "+conductorEventTable+" WHERE session_id=? AND event_id IN ("+placeholders(len(batch))+")"+order, append([]any{sid}, batch...)...)
		if err != nil {
			return nil, err
		}
		events = append(events, found...)
		candidates = candidates[len(batch):]
	}
	inputs := conductorUnprojectedEvents(sid, agentType, rows, events)
	for i := range inputs {
		// The locator ordinal is the event's place in the session's whole
		// sequence, which retained event inputs keep in the same order.
		inputs[i].ordinal = ordinals[firstString(inputs[i].eventID())]
	}
	return inputs, nil
}

// eventID is the source event of an input rebuilt from the event log.
func (input conductorParserInput) eventID() any { return input.row[conductorEventIDKey] }

// conductorEventIDKey carries a rebuilt row's source event ID beside its
// columns. The parser reads only known column names, so it never sees it.
const conductorEventIDKey = "\x00event_id"

// conductorProjectedEvents is the set of event IDs stored rows project.
func conductorProjectedEvents(sid string, rows []map[string]any) map[string]bool {
	prefix := "cloud-feed:" + sid + ":"
	projected := map[string]bool{}
	for _, row := range rows {
		if rest, ok := strings.CutPrefix(firstString(row["id"]), prefix); ok {
			if event, _, ok := strings.Cut(rest, ":"); ok && event != "" {
				projected[event] = true
			}
		}
	}
	return projected
}

// conductorUnprojectedEvents rebuilds, as session_messages rows, the events
// among events (one session's, in its sequence) that Conductor projects but
// no row in rows does. ordinal is the event's position in events.
func conductorUnprojectedEvents(sid, agentType string, rows, events []map[string]any) []conductorParserInput {
	stored := map[string]bool{}
	for _, row := range rows {
		stored[firstString(row["id"])] = true
	}
	projected := conductorProjectedEvents(sid, rows)
	family := conductorClaudeFamily(agentType)
	var uuids map[string]bool
	seen := map[string]bool{}
	inputs := []conductorParserInput{}
	for ordinal, event := range events {
		id := firstString(event["event_id"])
		if id == "" || projected[id] || seen[id] || firstString(event["reverted_at"]) != "" {
			continue
		}
		// Conductor's unique (session_id, event_id) index makes a repeat
		// impossible there; a copy without it must not emit one twice.
		seen[id] = true
		var envelope struct {
			Type       string          `json:"type"`
			ID         string          `json:"id"`
			Message    json.RawMessage `json:"message"`
			State      string          `json:"state"`
			TurnID     string          `json:"turnId"`
			RawPayload json.RawMessage `json:"rawPayload"`
			// Who sent a user message, as its stored row records it.
			SenderSessionID  string          `json:"senderSessionId"`
			SenderAPIKeyName string          `json:"senderApiKeyName"`
			SenderSource     json.RawMessage `json:"senderSource"`
			Config           struct {
				Model string `json:"model"`
			} `json:"config"`
		}
		if json.Unmarshal([]byte(messageText(event["content"])), &envelope) != nil {
			continue
		}
		at := firstNonNil(event["received_at"])
		row := map[string]any{"created_at": at, "sent_at": at, conductorEventIDKey: id}
		if envelope.TurnID != "" {
			row["turn_id"] = envelope.TurnID
		}
		switch firstString(event["event_type"], envelope.Type) {
		case "userMessage":
			// Conductor stores the sent message under its own ID; a queued
			// one has not reached the agent yet.
			if envelope.ID == "" || stored[envelope.ID] || (envelope.State != "" && envelope.State != "sent") {
				continue
			}
			var text string
			if json.Unmarshal(envelope.Message, &text) != nil || text == "" {
				continue
			}
			// The row stores the text after the harness's leading
			// instruction blocks; the evidence locator names the event,
			// which keeps them.
			row["id"], row["role"], row["content"] = envelope.ID, "user", conductorUserText(text)
			if envelope.Config.Model != "" {
				row["model"] = envelope.Config.Model
			}
			// The sending agent session and API key pass to the row as is.
			// senderId stays unknown: the event names the sending client
			// ("local"), where the row records the account.
			if envelope.SenderSessionID != "" {
				row["sender_session_id"] = envelope.SenderSessionID
			}
			if envelope.SenderAPIKeyName != "" {
				row["sender_api_key_name"] = envelope.SenderAPIKeyName
			}
			if source := bytes.TrimSpace(envelope.SenderSource); len(source) > 0 && !bytes.Equal(source, []byte("null")) {
				row["sender_source"] = string(source)
			}
		case "agent":
			if !family || len(envelope.RawPayload) == 0 {
				continue
			}
			decoder := json.NewDecoder(bytes.NewReader(envelope.RawPayload))
			decoder.UseNumber()
			var payload map[string]any
			if decoder.Decode(&payload) != nil || payload == nil {
				continue
			}
			projection, ok := conductorProjection(payload)
			if !ok {
				continue
			}
			// The deterministic ID already rules out a projected copy; the
			// payload's own UUID also rules out a row stored under another ID.
			if uuid := firstString(payload["uuid"]); uuid != "" {
				if uuids == nil {
					uuids = conductorRowUUIDs(rows)
				}
				if uuids[uuid] {
					continue
				}
			}
			content := string(envelope.RawPayload)
			if projection != nil {
				content = jsonText(projection)
			}
			// Conductor stores one row per projected payload, at index 0 for
			// these agents, and every agent row under the assistant role.
			row["id"], row["role"], row["content"] = "cloud-feed:"+sid+":"+id+":0", "assistant", content
		default:
			continue
		}
		inputs = append(inputs, conductorParserInput{row: row, table: conductorEventTable, ordinal: ordinal})
	}
	return inputs
}

// conductorUserText is the text Conductor stores for a sent user message:
// the message after any leading <system_instruction> blocks, left untouched
// when a block is unterminated.
func conductorUserText(message string) string {
	rest, stripped := message, false
	for {
		trimmed := strings.TrimLeftFunc(rest, unicode.IsSpace)
		if !strings.HasPrefix(trimmed, "<system_instruction>") {
			if stripped {
				return trimmed
			}
			return message
		}
		end := strings.Index(trimmed, "</system_instruction>")
		if end < 0 {
			return message
		}
		rest, stripped = trimmed[end+len("</system_instruction>"):], true
	}
}

var conductorLocalCommandStdout = regexp.MustCompile(`<local-command-stdout>([\s\S]*?)</local-command-stdout>`)

// conductorProjection applies the filter Conductor's client applies before it
// stores a Claude-family payload: it keeps assistant, result, system (but not
// task_progress) and error payloads, user payloads carrying tool results, and
// local command output, which it stores as assistant text. ok reports whether
// the payload is stored; a non-nil projection replaces it.
func conductorProjection(payload map[string]any) (projection map[string]any, ok bool) {
	switch firstString(payload["type"]) {
	case "assistant", "result", "error":
		return nil, true
	case "system":
		return nil, firstString(payload["subtype"]) != "task_progress"
	case "user":
		message := mapValue(payload["message"])
		if text, isText := message["content"].(string); isText {
			if !strings.Contains(text, "<local-command-stdout>") {
				return nil, false
			}
			if match := conductorLocalCommandStdout.FindStringSubmatch(text); match != nil {
				text = strings.TrimSpace(match[1])
			}
			projection = map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}}}
			if session, isString := payload["session_id"].(string); isString {
				projection["session_id"] = session
			}
			return projection, true
		}
		blocks, _ := message["content"].([]any)
		for _, block := range blocks {
			if firstString(mapValue(block)["type"]) == "tool_result" {
				return nil, true
			}
		}
	}
	return nil, false
}

// conductorRowUUIDs is the set of payload UUIDs stored rows carry.
func conductorRowUUIDs(rows []map[string]any) map[string]bool {
	uuids := map[string]bool{}
	for _, row := range rows {
		for _, column := range []string{"content", "full_message"} {
			var payload struct {
				UUID string `json:"uuid"`
			}
			if json.Unmarshal([]byte(messageText(row[column])), &payload) == nil && payload.UUID != "" {
				uuids[payload.UUID] = true
			}
		}
	}
	return uuids
}

// mergeConductorInputs places rebuilt event rows among stored rows in the
// parser's (created_at, id) order. Stored rows keep their relative order, so
// a session with no rebuilt rows parses exactly as before; without that
// order the rebuilt rows follow the stored ones. A rebuilt row carries state
// to the rows after it (the reported model, a delegation its tool call
// opened) just as its stored row would have.
func mergeConductorInputs(rows []map[string]any, table string, events []conductorParserInput, ordered bool) []conductorParserInput {
	inputs := make([]conductorParserInput, 0, len(rows)+len(events))
	less := func(left, right map[string]any) bool {
		leftAt, rightAt := asString(left["created_at"]), asString(right["created_at"])
		if leftAt != rightAt {
			return leftAt < rightAt
		}
		return asString(left["id"]) < asString(right["id"])
	}
	if ordered {
		events = slices.Clone(events)
		sort.SliceStable(events, func(i, j int) bool { return less(events[i].row, events[j].row) })
	}
	before := func(event conductorParserInput, row map[string]any) bool {
		return ordered && less(event.row, row)
	}
	next := 0
	for i, row := range rows {
		for next < len(events) && before(events[next], row) {
			inputs = append(inputs, events[next])
			next++
		}
		inputs = append(inputs, conductorParserInput{row: row, table: table, ordinal: i})
	}
	return append(inputs, events[next:]...)
}
