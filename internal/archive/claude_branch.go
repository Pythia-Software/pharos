package archive

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// claudeActiveBranch says which of a Claude Code transcript's entries are on
// the conversation it holds now, by uuid. Claude Code keeps every entry it
// wrote: a rewind or an edited prompt leaves the abandoned turns in the file,
// under the parent they branched from. The conversation is the path from the
// last prompt or reply back to the start, as a resume shows it, with a
// compaction joined to what it summarized (logicalParentUuid).
//
// Claude Code also writes one reply over several lines (one per content
// block, a parallel tool call on its own line) and each tool result under the
// line of its call, so a reply's other lines and its calls' results branch
// off that path without leaving the conversation. They are on it with the
// entry they belong to, as are the attachments and notes under an entry on
// it. An entry with no uuid, or none of whose kind has a parent, is never
// off it.
func claudeActiveBranch(events []map[string]any) map[string]bool {
	type entry struct {
		uuid, parent, reply, kind string
		calls, results            []string
	}
	entries := []entry{}
	index := map[string]int{}
	for _, event := range events {
		id := firstString(event["uuid"])
		if id == "" {
			continue
		}
		if _, seen := index[id]; seen {
			continue
		}
		message := mapValue(event["message"])
		e := entry{uuid: id, parent: firstString(event["parentUuid"], event["logicalParentUuid"]), kind: firstString(event["type"])}
		if e.kind == "assistant" {
			e.reply = firstString(message["id"])
		}
		if blocks, ok := firstNonNil(message["content"], event["content"]).([]any); ok {
			for _, raw := range blocks {
				block := mapValue(raw)
				switch firstString(block["type"]) {
				case "tool_use":
					e.calls = append(e.calls, firstString(block["id"]))
				case "tool_result":
					e.results = append(e.results, firstString(block["tool_use_id"]))
				}
			}
		}
		index[id] = len(entries)
		entries = append(entries, e)
	}
	turn := func(e entry) bool { return e.kind == "user" || e.kind == "assistant" }
	leaf := -1
	for i := len(entries) - 1; i >= 0; i-- {
		if turn(entries[i]) {
			leaf = i
			break
		}
	}
	on := make(map[string]bool, len(entries))
	if leaf < 0 {
		for _, e := range entries {
			on[e.uuid] = true
		}
		return on
	}
	replies, calls, results, notes := map[string][]int{}, map[string][]int{}, map[string][]int{}, map[string][]int{}
	for i, e := range entries {
		if e.reply != "" {
			replies[e.reply] = append(replies[e.reply], i)
		}
		for _, id := range e.calls {
			calls[id] = append(calls[id], i)
		}
		for _, id := range e.results {
			results[id] = append(results[id], i)
		}
		if !turn(e) && e.parent != "" {
			notes[e.parent] = append(notes[e.parent], i)
		}
	}
	queue := []int{}
	add := func(i int) {
		if !on[entries[i].uuid] {
			on[entries[i].uuid] = true
			queue = append(queue, i)
		}
	}
	for i := leaf; i >= 0; {
		if on[entries[i].uuid] {
			break
		}
		add(i)
		next, ok := index[entries[i].parent]
		if !ok {
			break
		}
		i = next
	}
	for len(queue) > 0 {
		e := entries[queue[0]]
		queue = queue[1:]
		if e.reply != "" {
			for _, i := range replies[e.reply] {
				add(i)
			}
		}
		for _, id := range e.calls {
			for _, i := range results[id] {
				add(i)
			}
		}
		for _, id := range e.results {
			for _, i := range calls[id] {
				add(i)
			}
		}
		for _, i := range notes[e.uuid] {
			add(i)
		}
	}
	for _, e := range entries {
		if !turn(e) {
			if _, ok := index[e.parent]; !ok {
				on[e.uuid] = true
			}
		}
	}
	return on
}

// claudeAdvisorUsage is one usage record for each advisor iteration of a
// reply's usage (iterations[type=advisor_message]). The reply's own usage is
// its main model's alone, so an advisor's tokens are counted beside it, under
// the advisor's model; other iteration types repeat the reply's own usage and
// are not counted again.
func claudeAdvisorUsage(base MessageRecord, event map[string]any, usage map[string]any) []MessageRecord {
	records := []MessageRecord{}
	iterations, _ := usage["iterations"].([]any)
	message := mapValue(event["message"])
	for index, raw := range iterations {
		iteration := mapValue(raw)
		if firstString(iteration["type"]) != "advisor_message" {
			continue
		}
		counts := mapValue(iteration["usage"])
		if counts == nil {
			counts = iteration
		}
		model := defaultString(iteration["model"], base.Model)
		id := firstString(message["id"]) + ":advisor:" + strconv.Itoa(index)
		envelope := map[string]any{"type": "assistant", "advisor_of": firstString(message["id"]), "requestId": event["requestId"],
			"message": map[string]any{"id": id, "model": model, "usage": counts}}
		record := base
		record.NativeID = base.NativeID + ":advisor:" + strconv.Itoa(index)
		record.Model = model
		record.RawText = jsonText(envelope)
		record.Text = jsonText(map[string]any{"type": "token_usage", "usage": counts, "usage_message_id": id, "advisor_of": firstString(message["id"])})
		records = append(records, record)
	}
	return records
}

// A copy of a reply's usage record keeps the record it copies as evidence,
// under a key no reader of usage looks in, so it counts nothing. copy_of
// names the conversation whose record counts.
const claudeUsageCopyType = "usage_copy"

func wrapUsageCopy(raw, owner string) string {
	var original any
	if json.Unmarshal([]byte(raw), &original) != nil {
		original = raw
	}
	return jsonText(map[string]any{"type": claudeUsageCopyType, "copy_of": owner, "copied": original})
}

// unwrapUsageCopy returns the record a copy keeps, and whether raw was one.
func unwrapUsageCopy(raw string) (string, bool) {
	if !strings.Contains(raw, `"`+claudeUsageCopyType+`"`) {
		return raw, false
	}
	value := map[string]any{}
	if json.Unmarshal([]byte(raw), &value) != nil || firstString(value["type"]) != claudeUsageCopyType {
		return raw, false
	}
	if text, ok := value["copied"].(string); ok {
		return text, true
	}
	return jsonText(value["copied"]), true
}

// claudeUsageRecord says whether m is a reply's usage, or one of its advisor
// iterations' (claudeAdvisorUsage).
func claudeUsageRecord(m MessageRecord) bool {
	return m.Kind == "metadata" && (strings.HasSuffix(m.NativeID, ":usage") || strings.Contains(m.NativeID, ":usage:advisor:"))
}

// claudeUsageKeys is a usage record's identities: its reply's message ID
// with the request ID, and for an Anthropic message ID (msg_), which names
// one reply wherever it appears, the message ID alone. A sidechain (/btw)
// replays its parent's replies under their message IDs but new request IDs.
func claudeUsageKeys(m MessageRecord) []string {
	if !claudeUsageRecord(m) {
		return nil
	}
	raw, _ := unwrapUsageCopy(m.RawText)
	event := map[string]any{}
	if json.Unmarshal([]byte(raw), &event) != nil {
		return nil
	}
	id := firstString(mapValue(event["message"])["id"])
	if id == "" {
		return nil
	}
	keys := []string{}
	if request := firstString(event["requestId"]); request != "" {
		keys = append(keys, id+"\x00"+request)
	}
	if strings.HasPrefix(id, "msg_") {
		keys = append(keys, id)
	}
	return keys
}

// dedupeClaudeGroupUsage counts once a reply that several of a session's
// transcripts hold: a forked subagent starts with a copy of its parent's
// replies, and a sidechain replays them. The root's copy counts, else the
// first subagent's by native ID; the others become copies of it. Within one
// transcript a reply's lines are one request already (conversationTokenReports).
func dedupeClaudeGroupUsage(record *WorkspaceRecord) {
	order := make([]int, len(record.Conversations))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := record.Conversations[order[i]], record.Conversations[order[j]]
		if a.AgentDepth != b.AgentDepth {
			return a.AgentDepth < b.AgentDepth
		}
		return a.NativeID < b.NativeID
	})
	owners := map[string]string{}
	for _, index := range order {
		conversation := &record.Conversations[index]
		claimed := map[string]bool{}
		for m := range conversation.Messages {
			message := &conversation.Messages[m]
			keys := claudeUsageKeys(*message)
			owner := ""
			for _, key := range keys {
				if found, ok := owners[key]; ok && found != conversation.NativeID {
					owner = found
					break
				}
			}
			if owner != "" {
				raw, _ := unwrapUsageCopy(message.RawText)
				message.RawText = wrapUsageCopy(raw, owner)
				continue
			}
			for _, key := range keys {
				claimed[key] = true
			}
		}
		for key := range claimed {
			if _, ok := owners[key]; !ok {
				owners[key] = conversation.NativeID
			}
		}
	}
}
