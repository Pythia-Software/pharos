package archive

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Hermes' native schema is owned by hermes_state_common.py. Session counters
// describe the main loop; session_model_usage also includes auxiliary calls.
// Compaction continuations are the same conversation, not delegated helpers.
func (a *personalAgentAdapter) readHermes(input personalAgentInput) ([]WorkspaceRecord, error) {
	db, tx, tables, err := a.openPersonalDatabase(input.path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	defer tx.Rollback()
	if !tables["sessions"] || !tables["messages"] {
		return nil, fmt.Errorf("unsupported Hermes SQLite schema: expected sessions and messages")
	}
	sessions, err := queryMapsContext(a.context(), tx, "SELECT * FROM sessions ORDER BY started_at,id")
	if err != nil {
		return nil, err
	}
	byID := map[string]map[string]any{}
	for _, session := range sessions {
		byID[firstString(session["id"])] = session
	}
	root := func(id string) string {
		seen := map[string]bool{}
		for !seen[id] {
			seen[id] = true
			row := byID[id]
			parent := firstString(row["parent_session_id"])
			config := personalJSON(row["model_config"])
			boundary := false
			for _, marker := range []string{"_branched_from", "_delegate_from", "_reset_from"} {
				boundary = boundary || parent != "" && firstString(config[marker]) == parent
			}
			if parent == "" || byID[parent] == nil || firstString(byID[parent]["end_reason"]) != "compression" || firstString(row["source"]) == "tool" || boundary {
				return id
			}
			id = parent
		}
		return "" // Corrupt compression lineage; do not publish a partial cycle.
	}
	groups := map[string][]map[string]any{}
	var roots []string
	for _, session := range sessions {
		id := firstString(session["id"])
		if id == "" {
			return nil, fmt.Errorf("Hermes session has no ID")
		}
		top := root(id)
		if top == "" {
			return nil, fmt.Errorf("cyclic Hermes compression lineage at %s", id)
		}
		if groups[top] == nil {
			roots = append(roots, top)
		}
		groups[top] = append(groups[top], session)
	}
	records := []WorkspaceRecord{}
	for _, top := range roots {
		meta := maps.Clone(byID[top])
		meta["branch"] = meta["git_branch"]
		config := personalJSON(meta["model_config"])
		cv := ConversationRecord{NativeID: personalNativeID(input.namespace, top), Provider: "hermes", Account: a.config.Account, Origin: a.original(input.path), Coverage: "complete", Harness: "hermes", Extractor: hermesExtractor,
			StartedAt: iso(meta["started_at"]), Model: firstString(meta["model"]), Effort: recordedEffort(config), NativeContext: map[string]any{"source": firstString(meta["created_source"], meta["source"]), "session_key": meta["session_key"]}}
		// parent_session_id also records resets and user forks. Only an
		// explicit delegation marker or a tool session establishes a helper.
		parent := firstString(config["_delegate_from"])
		if parent == "" && firstString(meta["source"]) == "tool" {
			parent = firstString(meta["parent_session_id"])
		}
		if parent != "" && parent != top {
			cv.ParentNativeID, cv.AgentDepth = personalNativeID(input.namespace, root(parent)), 1
		}
		if fork := firstString(config["_branched_from"]); fork != "" {
			meta["forked_from"] = personalNativeID(input.namespace, fork)
		}
		var rows []map[string]any
		for _, session := range groups[top] {
			part, err := queryMapsContext(a.context(), tx, "SELECT messages.*,messages.rowid AS _pharos_rowid FROM messages WHERE session_id=? ORDER BY messages.rowid", session["id"])
			if err != nil {
				return nil, err
			}
			rows = append(rows, part...)
		}
		generations := hermesMessageGenerations(rows)
		pendingCalls, usedCalls := map[string][]string{}, map[string]bool{}
		var nativeIDs []string
		var reportedCost float64
		cwd := firstString(meta["cwd"], meta["git_repo_root"])
		for _, generation := range generations {
			row := generation.row
			id := firstString(row["session_id"])
			session := byID[id]
			messageID := asString(row["id"])
			if messageID == "" {
				return nil, fmt.Errorf("Hermes message in %s has no native ID", id)
			}
			content := hermesContent(row["content"])
			base := MessageRecord{NativeID: id + ":" + messageID, Role: firstString(row["role"]), Kind: "message", Text: firstString(row["content"]), CreatedAt: iso(row["timestamp"]), Model: firstString(session["model"]), Effort: recordedEffort(personalJSON(session["model_config"])), Selected: true,
				EvidenceLocator: fmt.Sprintf("%s#messages:%s", a.original(input.path), messageID),
				RawText:         jsonText(map[string]any{"type": row["role"], "id": row["id"]})}
			if text, ok := content.(string); ok {
				base.Text = text
			} else if content != nil {
				base.Text = jsonText(content)
			}
			base.Selected = generation.selected
			if uid := firstString(row["message_uid"]); uid != "" {
				base.NativeID = "message:" + uid
			}
			if hermesModelContext(row) {
				base.Role, base.Kind = "system", "metadata"
				base.Text = jsonText(map[string]any{"type": "hidden_context", "content": base.Text})
				cv.Messages = append(cv.Messages, base)
				continue
			}
			if integer(row["_compressed_summary"]) != 0 {
				base.Role, base.Kind = "system", "metadata"
				base.Text = jsonText(map[string]any{"type": "compaction", "summary": base.Text})
			} else if base.Role == "tool" || base.Role == "function" {
				providerID := firstString(row["tool_call_id"])
				base.Role, base.Kind, base.CallID = "tool", "tool_result", hermesResultCallID(pendingCalls, providerID, firstString(row["tool_call_uid"]))
				envelope := map[string]any{"tool": row["tool_name"], "content": content}
				if base.CallID != providerID {
					envelope["provider_call_id"] = providerID
				}
				base.Text = jsonText(envelope)
			} else if base.Role == "user" {
				clear(pendingCalls) // Results cannot pair across a new human turn.
				if cv.ParentNativeID != "" {
					base.Role, base.Sender = agentRole, "agent:"+cv.ParentNativeID
				} else if firstString(session["source"]) == "cron" {
					base.Role, base.Sender = agentRole, "automation:cron"
				}
			}
			if reasoning := firstString(row["reasoning_content"], row["reasoning"]); reasoning != "" {
				thinking := base
				thinking.NativeID, thinking.Role, thinking.Kind, thinking.Text, thinking.ReasoningFormat = base.NativeID+":reasoning", "assistant", "reasoning", reasoning, "text"
				cv.Messages = append(cv.Messages, thinking)
			}
			if blocks, ok := content.([]any); ok && base.Kind == "message" {
				cv.Messages = append(cv.Messages, personalContent(base, blocks)...)
			} else {
				cv.Messages = append(cv.Messages, base)
			}
			var calls []any
			if text := firstString(row["tool_calls"]); text != "" {
				if err := decodePersonalJSON(text, &calls); err != nil {
					return nil, fmt.Errorf("Hermes message %s tool calls: %w", base.NativeID, err)
				}
			}
			occurrences := map[string]int{}
			callUIDs := personalJSON(row["tool_call_uids"])
			for index, raw := range calls {
				call := mapValueDefault(raw)
				function := mapValueDefault(call["function"])
				var args any = function["arguments"]
				if text, ok := args.(string); ok {
					if err := decodePersonalJSON(text, &args); err != nil {
						args = text // Preserve incomplete or non-JSON native arguments.
					}
				}
				tool := base
				providerID := firstString(call["id"], call["call_id"], call["tool_call_id"])
				tool.NativeID, tool.Role, tool.Kind = base.NativeID+":tool:"+strconv.Itoa(index), "assistant", "tool_call"
				uid := hermesToolUID(callUIDs[providerID], occurrences[providerID])
				occurrences[providerID]++
				tool.CallID = defaultString(uid, providerID)
				if uid == "" && (tool.CallID == "" || usedCalls[tool.CallID]) {
					tool.CallID = "hermes-call:" + hashBytes([]byte(tool.NativeID))
				}
				usedCalls[tool.CallID] = true
				pendingCalls[providerID] = append(pendingCalls[providerID], tool.CallID)
				envelope := map[string]any{"tool": firstString(function["name"], call["name"]), "input": args}
				if tool.CallID != providerID {
					envelope["provider_call_id"] = providerID
				}
				tool.Text = jsonText(envelope)
				cv.Messages = append(cv.Messages, tool)
			}
		}
		// Aggregate usage keeps its reporting timestamps without changing the
		// durable insertion order of transcript messages across continuations.
		for _, session := range groups[top] {
			id := firstString(session["id"])
			nativeIDs = append(nativeIDs, id)
			var usageRows []map[string]any
			if tables["session_model_usage"] {
				columns, columnErr := tableColumns(tx, "session_model_usage")
				if columnErr != nil {
					return nil, columnErr
				}
				order := []string{"model"}
				for _, key := range []string{"billing_provider", "billing_base_url", "billing_mode", "task"} {
					if columns[key] {
						order = append(order, key)
					}
				}
				usageRows, err = queryMapsContext(a.context(), tx, "SELECT * FROM session_model_usage WHERE session_id=? ORDER BY "+strings.Join(order, ","), id)
				if err != nil {
					return nil, err
				}
			}
			// Per-model rows replace, rather than add to, the main-loop
			// counters. Auxiliary tasks have independent usage. Keep any
			// main-loop residual without guessing which model spent it.
			mainSums := map[string]float64{}
			for _, row := range usageRows {
				if firstString(row["task"]) == "" || firstString(row["task"]) == "voice_chat" {
					for _, key := range hermesUsageKeys {
						n, _ := number(row[key])
						mainSums[key] += n
					}
				}
				base := MessageRecord{Model: firstString(row["model"]), CreatedAt: iso(firstNonNil(row["last_seen"], session["ended_at"], session["started_at"])), Selected: true, EvidenceLocator: a.original(input.path) + "#session_model_usage:" + id}
				route := []any{row["model"], row["billing_provider"], row["billing_base_url"], row["billing_mode"], row["task"]}
				cv.Messages = append(cv.Messages, hermesAggregateUsage(base, id+":model:"+hashBytes([]byte(jsonText(route))), row))
			}
			residual := maps.Clone(session)
			positive := false
			for _, key := range hermesUsageKeys {
				n, ok := number(session[key])
				if !ok {
					delete(residual, key)
					continue
				}
				residual[key] = max(0, n-mainSums[key])
				positive = positive || n > mainSums[key]
			}
			if positive {
				model := firstString(session["model"])
				if len(mainSums) > 0 {
					model = "unknown"
				}
				base := MessageRecord{Model: model, CreatedAt: iso(firstNonNil(session["ended_at"], session["last_activity_at"], session["started_at"])), Selected: true, EvidenceLocator: a.original(input.path) + "#sessions:" + id}
				cv.Messages = append(cv.Messages, hermesAggregateUsage(base, id+":session", residual))
			}
			if cost, ok := number(session["actual_cost_usd"]); ok && cost > 0 {
				reportedCost += cost
			}
			cv.EndedAt = iso(session["ended_at"])
			cv.Model = firstString(session["model"], cv.Model)
		}
		meta["actual_cost_usd"] = reportedCost
		record := a.personalRecord(input, cv, meta, cwd)
		record.Metadata["native_session_ids"] = nativeIDs
		observe(&record, a.databaseVersion(input.path))
		records = append(records, record)
	}
	return records, nil
}

type hermesMessageGeneration struct {
	row      map[string]any
	selected bool
}

func hermesModelContext(row map[string]any) bool {
	return firstString(row["display_kind"]) == "hidden" || defaultBool(personalJSON(row["display_metadata"])["model_only"], false)
}

func hermesToolUID(value any, occurrence int) string {
	switch value := value.(type) {
	case string:
		return value
	case []any:
		if occurrence < len(value) {
			uid, _ := value[occurrence].(string)
			return uid
		}
	}
	return ""
}

// A native UID identifies an execution even when results arrive out of order.
// Legacy or partially backfilled results consume the oldest pending occurrence
// of their provider ID, which may be reused within the same assistant response.
func hermesResultCallID(pending map[string][]string, providerID, uid string) string {
	queue := pending[providerID]
	index := 0
	if uid != "" {
		index = slices.Index(queue, uid)
	} else if len(queue) > 0 {
		uid = queue[0]
	}
	if index >= 0 && index < len(queue) {
		pending[providerID] = slices.Delete(queue, index, index+1)
	}
	return defaultString(uid, providerID)
}

// Hermes' legacy display key includes the original timestamp and tool fields;
// repeated prose at another time is a separate message. Tool-calling assistant
// copies use their ordered provider IDs so pruning their arguments does not
// duplicate an execution (hermes_state_identity._stable_tool_key).
func hermesLegacyDisplayKey(row map[string]any) string {
	content, calls := row["content"], row["tool_calls"]
	var nativeToolIDs []string
	if firstString(row["role"]) == "assistant" {
		var parsed []map[string]any
		if decodePersonalJSON(firstString(calls), &parsed) == nil && len(parsed) > 0 {
			ids := []string{}
			occurrences := map[string]int{}
			uids := personalJSON(row["tool_call_uids"])
			complete := true
			for _, call := range parsed {
				id := firstString(call["id"], call["call_id"], call["tool_call_id"])
				if id == "" {
					ids = nil
					complete = false
					break
				}
				ids = append(ids, id)
				uid := hermesToolUID(uids[id], occurrences[id])
				occurrences[id]++
				complete = complete && uid != ""
				nativeToolIDs = append(nativeToolIDs, uid)
			}
			if !complete {
				nativeToolIDs = nil
			}
			if len(ids) > 0 {
				content, calls = nil, ids
			}
		}
	}
	// A recorded occurrence UID outranks a coincident provider ID and
	// timestamp: separate executions can have otherwise identical fields.
	return hashBytes([]byte(jsonText([]any{row["role"], content, row["timestamp"], row["tool_call_id"], calls, row["tool_name"], nativeToolIDs, row["tool_call_uid"]})))
}

// Native row order sorts generations independently of copied timestamps and
// legacy textual IDs. Keep the first durable content/identity, but let the
// newest copy decide selection:
// an in-place compaction removes an original before inserting its live copy,
// and a later rewind can remove that copy without reviving the original.
func hermesMessageGenerations(rows []map[string]any) []hermesMessageGeneration {
	sort.SliceStable(rows, func(i, j int) bool { return integer(rows[i]["_pharos_rowid"]) < integer(rows[j]["_pharos_rowid"]) })
	parents := make([]int, len(rows))
	var root func(int) int
	root = func(i int) int {
		if parents[i] != i {
			parents[i] = root(parents[i])
		}
		return parents[i]
	}
	identities := map[string]int{}
	for i, row := range rows {
		parents[i] = i
		keys := []string{"legacy:" + hermesLegacyDisplayKey(row)}
		if uid := firstString(row["message_uid"]); uid != "" {
			keys = append(keys, "uid:"+uid)
		}
		if display := personalBytes(row["display_identity"]); len(display) > 0 {
			keys = append(keys, "display:"+string(display))
		}
		for _, key := range keys {
			// Synthetic model context must not replace a visible turn even
			// when a compactor carries its identity onto the wrapper.
			if hermesModelContext(row) {
				key = "context:" + key
			}
			if prior, ok := identities[key]; ok {
				a, b := root(i), root(prior)
				if a != b {
					parents[max(a, b)] = min(a, b)
				}
			} else {
				identities[key] = i
			}
		}
	}
	latest := map[int]map[string]any{}
	for i, row := range rows {
		latest[root(i)] = row
	}
	var result []hermesMessageGeneration
	for i, row := range rows {
		if root(i) != i {
			continue
		}
		last := latest[i]
		selected := last["active"] == nil || integer(last["active"]) != 0 || integer(last["compacted"]) != 0
		result = append(result, hermesMessageGeneration{row, selected})
	}
	return result
}

// Structured Hermes content carries a reserved sentinel. JSON-looking prose
// without that marker remains prose; a wrapped literal is decoded only once.
func hermesContent(value any) any {
	text, ok := value.(string)
	if ok && strings.HasPrefix(text, "\x00json:") {
		var content any
		if decodePersonalJSON(strings.TrimPrefix(text, "\x00json:"), &content) == nil {
			return content
		}
	}
	return value
}

var hermesUsageKeys = []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "reasoning_tokens"}

func hermesAggregateUsage(base MessageRecord, id string, row map[string]any) MessageRecord {
	base = personalUsage(base, id, hermesUsage(row))
	base.Text = jsonText(map[string]any{"type": "token_usage", "usage": hermesUsage(row), "usage_scope": "aggregate", "task": row["task"]})
	return base
}

func hermesUsage(row map[string]any) map[string]any {
	usage := map[string]any{}
	for _, key := range []string{"input_tokens", "output_tokens"} {
		if value := row[key]; value != nil {
			usage[key] = value
		}
	}
	// Hermes normalizes provider usage to CanonicalUsage before saving:
	// input_tokens excludes both cache buckets (agent/usage_pricing.py).
	if value := row["cache_read_tokens"]; value != nil {
		usage["cache_read_input_tokens"] = value
	}
	if value := row["cache_write_tokens"]; value != nil {
		usage["cache_creation_input_tokens"] = value
	}
	if value := row["reasoning_tokens"]; value != nil {
		usage["reasoning_output_tokens"] = value
	}
	return usage
}

func decodePersonalJSON(text string, result any) error {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	if err := decoder.Decode(result); err != nil {
		return err
	}
	return ensureEOF(decoder)
}
