package archive

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

type tokenCounts map[string]float64

type tokenReport struct {
	counts, checkpoint, reported tokenCounts
	usage                        map[string]any
	models                       map[string]tokenCounts
	scope, stream, model         string
	start                        int
	requestOnly                  bool
}

func tokenNumber(values ...any) (float64, bool) {
	for _, v := range values {
		if n, ok := number(v); ok && n >= 0 && !math.IsNaN(n) && !math.IsInf(n, 0) {
			return n, true
		}
	}
	return 0, false
}

// Canonical input includes cache reads/writes. Claude's base input excludes
// both; OpenAI input already includes them. Detail fields are subsets.
func normalizeTokenCounts(u map[string]any) tokenCounts {
	if u == nil {
		return nil
	}
	out := tokenCounts{}
	put := func(key string, values ...any) {
		if n, ok := tokenNumber(values...); ok {
			out[key] = n
		}
	}
	inputDetails := mapValueDefault(firstNonNil(u["input_tokens_details"], u["prompt_tokens_details"]))
	outputDetails := mapValueDefault(firstNonNil(u["output_tokens_details"], u["completion_tokens_details"]))
	put("input_tokens", u["input_tokens"], u["prompt_tokens"], u["inputTokens"])
	put("output_tokens", u["output_tokens"], u["completion_tokens"], u["outputTokens"])
	put("cache_read_input_tokens", u["cache_read_input_tokens"], u["cacheReadInputTokens"], u["cached_input_tokens"], inputDetails["cached_tokens"])
	put("cache_creation_input_tokens", u["cache_creation_input_tokens"], u["cacheCreationInputTokens"], u["cache_write_input_tokens"], inputDetails["cache_write_tokens"])
	if cache := mapValue(u["cache_creation"]); cache != nil {
		put("cache_creation_5m_input_tokens", cache["ephemeral_5m_input_tokens"], cache["5m_input_tokens"])
		put("cache_creation_1h_input_tokens", cache["ephemeral_1h_input_tokens"], cache["1h_input_tokens"])
		if _, ok := out["cache_creation_input_tokens"]; !ok {
			for _, v := range cache {
				if n, valid := tokenNumber(v); valid {
					out["cache_creation_input_tokens"] += n
				}
			}
		}
	}
	put("reasoning_output_tokens", u["reasoning_output_tokens"], u["thinking_tokens"], outputDetails["reasoning_tokens"], outputDetails["thinking_tokens"])
	separate := u["cache_read_input_tokens"] != nil || u["cache_creation_input_tokens"] != nil || u["cacheReadInputTokens"] != nil || u["cacheCreationInputTokens"] != nil || u["cache_creation"] != nil
	if separate {
		if n, ok := out["input_tokens"]; ok {
			out["uncached_input_tokens"] = n
		}
		out["input_tokens"] += out["cache_read_input_tokens"] + out["cache_creation_input_tokens"]
	} else if input, ok := out["input_tokens"]; ok {
		if _, hasRead := out["cache_read_input_tokens"]; hasRead {
			out["uncached_input_tokens"] = math.Max(0, input-out["cache_read_input_tokens"]-out["cache_creation_input_tokens"])
		} else if _, hasWrite := out["cache_creation_input_tokens"]; hasWrite {
			out["uncached_input_tokens"] = math.Max(0, input-out["cache_creation_input_tokens"])
		}
	}
	put("total_tokens", u["total_tokens"], u["totalTokens"])
	if _, ok := out["total_tokens"]; !ok {
		_, input := out["input_tokens"]
		_, output := out["output_tokens"]
		if input || output {
			out["total_tokens"] = out["input_tokens"] + out["output_tokens"]
		}
	}
	if _, ok := out["total_tokens"]; !ok {
		return nil
	}
	return out
}
func tokenObject(m MessageRecord) (map[string]any, map[string]any) {
	if m.accounting != nil && m.accounting.input == defaultString(m.RawText, m.Text) {
		return m.accounting.raw, m.accounting.value
	}
	var raw map[string]any
	decoder := json.NewDecoder(strings.NewReader(defaultString(m.RawText, m.Text)))
	decoder.UseNumber()
	if decoder.Decode(&raw) != nil {
		return nil, nil
	}
	value := raw
	for depth := 0; depth < 5; depth++ {
		var next map[string]any
		for _, key := range []string{"payload", "event", "msg", "data"} {
			if next = mapValue(value[key]); next != nil {
				break
			}
		}
		if next == nil {
			break
		}
		value = next
	}
	return raw, value
}

type messageAccounting struct {
	input      string
	raw, value map[string]any
}

func prepareRecordAccounting(record *WorkspaceRecord) {
	cache := map[string]*messageAccounting{}
	var bytes int64
	for conversationIndex := range record.Conversations {
		for messageIndex := range record.Conversations[conversationIndex].Messages {
			message := &record.Conversations[conversationIndex].Messages[messageIndex]
			input := defaultString(message.RawText, message.Text)
			if cached, ok := cache[input]; ok {
				message.accounting = cached
				continue
			}
			if bytes+int64(len(input))*8 > 64<<20 {
				message.accounting = nil
				continue
			}
			raw, value := tokenObject(*message)
			message.accounting = &messageAccounting{input: input, raw: raw, value: value}
			cache[input] = message.accounting
			bytes += int64(len(input)) * 8
		}
	}
}
func addTokenCounts(to, from tokenCounts) {
	for k, v := range from {
		to[k] += v
	}
}

// continuesHistory reports whether a Codex session_meta payload says the
// rollout continues another thread's history rather than starting its own.
func continuesHistory(meta map[string]any) bool {
	ordinal, _ := number(meta["subagent_history_start_ordinal"])
	return firstString(meta["forked_from_id"]) != "" || mapValue(meta["history_base"]) != nil || ordinal > 0
}

func tokenRemainder(total, attributed tokenCounts) tokenCounts {
	out := tokenCounts{}
	for k, v := range total {
		out[k] = math.Max(0, v-attributed[k])
	}
	return out
}

// The same request can appear as stream deltas, a final message, an SDK run
// result and a thread checkpoint. Resolve identities first, then use aggregates
// only to fill gaps. Keep all originals in raw_text for independent inspection.
func tokenStreamAliases(messages []MessageRecord) map[string]string {
	aliases := map[string]string{"main": "main"}
	for _, message := range messages {
		if message.Kind != "delegation" {
			continue
		}
		child := defaultString(nilIfEmpty(message.CallID), message.NativeID)
		if child != "" {
			aliases[child] = child
			if message.NativeID != "" {
				aliases[message.NativeID] = child
			}
		}
	}
	return aliases
}

func tokenStreamParents(messages []MessageRecord) map[string]string {
	parents, aliases := map[string]string{}, tokenStreamAliases(messages)
	for _, message := range messages {
		if message.Kind != "delegation" {
			continue
		}
		child := defaultString(nilIfEmpty(message.CallID), message.NativeID)
		parent := aliases[message.ParentNativeID]
		if parent == "" {
			parent = defaultString(nilIfEmpty(message.ParentNativeID), "main")
		}
		if child != "" && child != parent {
			parents[child] = parent
		}
	}
	return parents
}

func streamDescendsFrom(stream, ancestor string, parents map[string]string) bool {
	if stream == ancestor {
		return true
	}
	seen := map[string]bool{}
	for stream != "" && stream != "main" && !seen[stream] {
		seen[stream] = true
		stream = parents[stream]
		if stream == ancestor {
			return true
		}
	}
	return false
}

func streamDepth(stream string, parents map[string]string) int {
	depth, seen := 0, map[string]bool{}
	for stream != "" && stream != "main" && !seen[stream] {
		seen[stream] = true
		stream = parents[stream]
		depth++
	}
	return depth
}

// Match the same unique delegation proof used by the persisted child link.
// A separately retained child's own accounting (including a proved usage copy)
// supersedes the parent's fallback result. Missing child accounting does not.
// Clone derived flags so inspecting a group never alters its source records.
func conversationsWithChildUsage(conversations []ConversationRecord) []ConversationRecord {
	return conversationsWithChildUsageState(conversations, nil)
}

func conversationsWithChildUsageState(conversations []ConversationRecord, recorded map[int]bool) []ConversationRecord {
	candidate := false
	recoverable := map[int]map[string]bool{}
	for index, c := range conversations {
		for _, m := range c.Messages {
			candidate = candidate || m.usageInChild
			if m.Kind != "delegation_result" || !strings.Contains(m.Text, "totalToolUseCount") {
				continue
			}
			details := mapValueDefault(mapValueDefault(parseJSONValue(m.Text))["details"])
			if singleRequestAgentResult(details) == nil {
				continue
			}
			candidate = true
			if recoverable[index] == nil {
				recoverable[index] = map[string]bool{}
			}
			recoverable[index][m.CallID] = true
		}
	}
	if !candidate {
		return conversations
	}
	result := slices.Clone(conversations)
	calls := map[int]map[string]bool{}
	for child, edge := range claudeLineageEdges(conversations) {
		if !recoverable[edge.parent][edge.callID] {
			continue
		}
		parent := conversations[edge.parent]
		c := conversations[child]
		if parent.Provider != "claude" || c.Provider != "claude" || parent.Account != c.Account {
			continue
		}
		accounted := recorded[child]
		for _, s := range agentSessionSummaries(withUsageCopies(c.Messages)) {
			if s.nativeID == "main" && len(s.counts) > 0 {
				accounted = true
				break
			}
		}
		if !accounted {
			continue
		}
		if calls[edge.parent] == nil {
			calls[edge.parent] = map[string]bool{}
		}
		calls[edge.parent][edge.callID] = true
	}
	for index, c := range result {
		c.Messages = slices.Clone(c.Messages)
		for m := range c.Messages {
			c.Messages[m].usageInChild = c.Messages[m].Kind == "delegation_result" && calls[index][c.Messages[m].CallID]
		}
		result[index] = c
	}
	return result
}

// A configured alias can match only one concrete family in this aggregate.
func tokenModelFamily(model string, totals map[string]tokenCounts) string {
	family := modelFamily(model)
	for name := range totals {
		if modelFamily(name) == family {
			return family
		}
	}
	if !matchesFold(family, "opus", "sonnet", "haiku", "fable") {
		return family
	}
	matched := ""
	for name := range totals {
		candidate := modelFamily(name)
		if strings.HasPrefix(candidate, family+"-") {
			if matched != "" && matched != candidate {
				return family
			}
			matched = candidate
		}
	}
	return defaultString(matched, family)
}

func singleRequestAgentResult(details map[string]any) tokenCounts {
	tools, hasTools := tokenNumber(details["totalToolUseCount"])
	if firstString(details["status"]) != "completed" || !hasTools || tools != 0 {
		return nil
	}
	counts := normalizeTokenCounts(mapValue(details["usage"]))
	total, hasTotal := tokenNumber(details["totalTokens"])
	if counts["total_tokens"] <= 0 || !hasTotal || total != counts["total_tokens"] || total != counts["input_tokens"]+counts["output_tokens"] {
		return nil
	}
	return counts
}

func conversationTokenReports(messages []MessageRecord) []tokenReport {
	return conversationTokenReportsWithCostState(messages, false)
}

func conversationTokenReportsWithCostState(messages []MessageRecord, suppressCostState bool) []tokenReport {
	reports := make([]tokenReport, len(messages))
	streamAliases := tokenStreamAliases(messages)
	seen := map[string]int{}
	active := map[string]string{}
	runs := map[string]int{}
	cumulative := map[string]tokenCounts{}
	modelCounters := map[string]tokenCounts{}
	resultIDs := map[string]bool{}
	recordStreams := map[string]bool{}
	// A transcript that continues another thread's history (a Codex fork,
	// forked subagent or revert page) starts its counters at that thread's
	// usage, which the other thread's transcript counts. inherited holds that
	// starting point per stream, from its first counter or record.
	inherits := false
	inherited := map[string]tokenCounts{}
	// Each message is decoded once for both passes.
	raws, values := make([]map[string]any, len(messages)), make([]map[string]any, len(messages))
	for i, m := range messages {
		raws[i], values[i] = tokenObject(m)
		raw, v := raws[i], values[i]
		if m.Kind == "metadata" && m.Role == "system" && firstString(raw["type"]) == "session_meta" && continuesHistory(v) {
			inherits = true
		}
		if firstString(raw["type"], v["type"]) == "token_usage_record" {
			stream := defaultString(firstNonNil(v["agent_id"], raw["parent_tool_use_id"], nilIfEmpty(m.ParentNativeID)), "main")
			stream = defaultString(streamAliases[stream], stream)
			recordStreams[stream] = true
		}
	}
	for i, m := range messages {
		raw, v := raws[i], values[i]
		if v == nil {
			continue
		}
		typ := firstString(v["type"], raw["type"])
		stream := defaultString(firstNonNil(v["agent_id"], raw["parent_tool_use_id"], nilIfEmpty(m.ParentNativeID)), "main")
		stream = defaultString(streamAliases[stream], stream)
		hasRecords := recordStreams[stream]
		msg := mapValueDefault(v["message"])
		response := mapValueDefault(v["response"])
		metadata := mapValueDefault(v["metadata"])
		u := mapValue(firstNonNil(msg["usage"], response["usage"], v["usage"], metadata["usage"]))
		counts := normalizeTokenCounts(u)
		id := firstString(msg["id"], response["id"], v["response_id"], v["usage_message_id"])
		if id == "" && (matchesFold(typ, "message", "response") || firstString(v["object"]) == "response") {
			id = firstString(v["id"])
		}
		if typ == "message_start" {
			active[stream] = id
		}
		if typ == "message_delta" && id == "" {
			id = active[stream]
			if id == "" {
				id = fmt.Sprintf("unidentified-stream-%d", i)
			}
			active[stream] = id
		}
		if typ == "message_stop" {
			delete(active, stream)
		}
		r := tokenReport{counts: counts, usage: u, scope: "request", stream: stream}
		// Native session/model totals contribute spend without inventing an
		// individual model request or a per-request timestamp.
		if typ == "token_usage" && m.Role == "system" && m.Kind == "metadata" && firstString(v["usage_scope"]) == "aggregate" {
			r.scope = "aggregate"
		}
		if matchesFold(typ, "result", "turn.completed", "cost-state") {
			if resultID := firstString(v["uuid"], v["id"], raw["uuid"]); resultID != "" {
				key := stream + ":" + typ + ":" + resultID
				if resultIDs[key] {
					continue
				}
				resultIDs[key] = true
			}
		}
		info := mapValueDefault(v["info"])
		if snapshot := mapValue(v["latest_token_usage_record"]); snapshot != nil && !hasRecords {
			info = map[string]any{"total_token_usage": snapshot["thread_token_usage"], "last_token_usage": snapshot["usage"]}
		}
		if running := normalizeTokenCounts(mapValue(info["total_token_usage"])); running != nil {
			r.scope = "thread"
			r.counts = nil
			r.reported = running
			last := normalizeTokenCounts(mapValue(info["last_token_usage"]))
			if _, ok := inherited[stream]; !ok {
				inherited[stream] = tokenCounts{}
				if inherits && last != nil {
					inherited[stream] = tokenRemainder(running, last)
				}
			}
			if !hasRecords {
				previous := cumulative[stream]
				if previous == nil {
					previous = inherited[stream]
				}
				if running["total_tokens"] < previous["total_tokens"] {
					r.counts = last
				} else {
					r.counts = tokenRemainder(running, previous)
				}
				cumulative[stream] = running
			}
		} else if typ == "token_count" {
			if !hasRecords {
				r.counts = normalizeTokenCounts(mapValue(info["last_token_usage"]))
			}
			r.scope = "thread"
		} else if matchesFold(firstString(v["subtype"]), "task_progress", "task_notification") {
			r.scope = "task"
			r.stream = defaultString(firstNonNil(v["tool_use_id"], v["task_id"], raw["parent_tool_use_id"]), stream)
			r.stream = defaultString(streamAliases[r.stream], r.stream)
			id = "task:" + r.stream
		} else if matchesFold(typ, "result", "turn.completed", "cost-state") {
			r.scope = "run"
			r.start = runs[stream]
			runs[stream] = i + 1
			if typ == "cost-state" {
				r.scope = "session"
			}
			models := tokenCounts{}
			r.models = map[string]tokenCounts{}
			modelUsage := mapValueDefault(v["modelUsage"])
			if len(modelUsage) == 1 {
				for model := range modelUsage {
					r.model = model
				}
			} else if len(modelUsage) > 1 {
				// A result may list only model context windows. Its aggregate
				// usage cannot inherit the parser's last execution model. Exact
				// counters below still replace this fallback when they reconcile.
				r.model = "Unknown model"
				if placeholderModel(m.Model) {
					r.model = m.Model
				}
			}
			for model, usage := range modelUsage {
				key := stream + ":" + firstString(raw["session_id"], v["session_id"]) + ":" + model
				current := normalizeTokenCounts(mapValue(usage))
				if current == nil {
					continue
				}
				previous := modelCounters[key]
				if current["total_tokens"] < previous["total_tokens"] {
					addTokenCounts(models, current)
					r.models[model] = current
				} else {
					delta := tokenRemainder(current, previous)
					addTokenCounts(models, delta)
					r.models[model] = delta
				}
				modelCounters[key] = current
			}
			if models["total_tokens"] > r.counts["total_tokens"] {
				r.counts = models
			}
		} else {
			r.checkpoint = normalizeTokenCounts(mapValue(v["thread_token_usage"]))
		}
		if _, started := inherited[stream]; inherits && !started && typ == "token_usage_record" && r.checkpoint != nil {
			// Codex writes each response's record before its counter, so a
			// stream's first record reports where its thread usage started.
			// After a counter, a record cannot tell inherited usage from earlier
			// requests no record covers: the counter's start stands and
			// checkpoints fill the rest.
			inherited[stream] = tokenRemainder(r.checkpoint, r.counts)
		}
		if id != "" && (r.scope == "request" || r.scope == "task") {
			key := stream + ":" + id
			if previous, ok := seen[key]; ok {
				merged := tokenCounts{}
				for k, n := range reports[previous].counts {
					merged[k] = n
				}
				for k, n := range r.counts {
					merged[k] = n
				}
				if r.scope == "request" {
					usage := map[string]any{}
					for k, v := range reports[previous].usage {
						usage[k] = v
					}
					for k, v := range r.usage {
						usage[k] = v
					}
					r.usage = usage
					merged = normalizeTokenCounts(usage)
				}
				r.counts = merged
				reports[previous].counts = nil
				reports[previous].checkpoint = nil
			}
			seen[key] = i
		}
		reports[i] = r
	}
	// Conductor stores one usage-bearing assistant envelope per content block.
	// With no message ID, adjacent blocks of one response share the same prompt
	// size. Keep the largest output until a user turn or tool result intervenes.
	lastAnonymous := map[string]int{}
	streams := make([]string, len(reports))
	for i := range reports {
		streams[i] = reports[i].stream
		r := reports[i]
		if r.scope != "request" || r.counts == nil {
			continue
		}
		raw, value := raws[i], values[i]
		msg := mapValueDefault(value["message"])
		response := mapValueDefault(value["response"])
		if msg["usage"] == nil || firstString(msg["id"], response["id"], value["response_id"], value["usage_message_id"]) != "" || !hasUsageEvidence(raw) {
			delete(lastAnonymous, r.stream)
			continue
		}
		if previous, ok := lastAnonymous[r.stream]; ok && reports[previous].counts["input_tokens"] == r.counts["input_tokens"] &&
			!turnBetween(messages, streams, r.stream, previous, i) {
			if reports[previous].counts["output_tokens"] > r.counts["output_tokens"] {
				reports[i].counts = reports[previous].counts
			}
			reports[previous].counts = nil
		}
		lastAnonymous[r.stream] = i
	}
	hasRequests := false
	for _, r := range reports {
		if r.scope == "request" && r.counts != nil && r.counts["total_tokens"] > 0 {
			hasRequests = true
			break
		}
	}
	if hasRequests || suppressCostState {
		for i := range messages {
			raw, value := raws[i], values[i]
			if firstString(value["type"], raw["type"]) == "cost-state" {
				reports[i].counts = nil
			}
		}
	}
	requests := map[string]tokenCounts{}
	for i, r := range reports {
		if r.scope != "request" || r.counts == nil {
			continue
		}
		if requests[r.stream] == nil {
			requests[r.stream] = tokenCounts{}
			addTokenCounts(requests[r.stream], inherited[r.stream])
		}
		addTokenCounts(requests[r.stream], r.counts)
		gap := tokenRemainder(r.checkpoint, requests[r.stream])
		addTokenCounts(r.counts, gap)
		addTokenCounts(requests[r.stream], gap)
		reports[i] = r
	}
	if len(recordStreams) > 0 {
		latest := map[string]int{}
		for i, r := range reports {
			if r.scope == "thread" && r.reported != nil && recordStreams[r.stream] {
				latest[r.stream] = i
			}
		}
		for stream, i := range latest {
			reports[i].counts = tokenRemainder(reports[i].reported, requests[stream])
		}
	}
	parents := tokenStreamParents(messages)
	accountedStreams := map[string]bool{}
	markAccounted := func(stream string) {
		for stream != "" && !accountedStreams[stream] {
			accountedStreams[stream] = true
			stream = parents[stream]
		}
	}
	for _, report := range reports {
		if report.counts["total_tokens"] > 0 {
			markAccounted(report.stream)
		}
	}
	launches := map[string]MessageRecord{}
	for _, message := range messages {
		if message.Kind == "delegation" {
			launches[defaultString(nilIfEmpty(message.CallID), message.NativeID)] = message
		}
	}
	launchModel := func(launch MessageRecord) string {
		payload := mapValueDefault(parseJSONValue(launch.Text))
		input := mapValueDefault(payload["input"])
		if model := firstString(input["model"]); model != "" {
			return model
		}
		// Custom agent types can choose their own default. General-purpose
		// agents inherit the launching request's model.
		if kind := firstString(input["subagent_type"]); kind != "" && kind != "general-purpose" {
			return "Unknown model"
		}
		if launch.Model == "" || launch.Model == "<synthetic>" {
			return "Unknown model"
		}
		return launch.Model
	}
	resultDetails, agentModels := map[int]map[string]any{}, map[string]string{}
	for i, message := range messages {
		if message.Kind != "delegation_result" {
			continue
		}
		var result map[string]any
		decoder := json.NewDecoder(strings.NewReader(message.Text))
		decoder.UseNumber()
		if decoder.Decode(&result) != nil {
			continue
		}
		details := mapValueDefault(result["details"])
		resultDetails[i] = details
		launch, known := launches[message.CallID]
		input := mapValueDefault(mapValueDefault(parseJSONValue(launch.Text))["input"])
		if agent := firstString(details["agentId"]); agent != "" && known && firstString(input["resume"]) == "" {
			model := firstString(details["model"], launchModel(launch))
			if prior := agentModels[agent]; prior != "" && prior != model {
				model = "Unknown model"
			}
			agentModels[agent] = model
		}
	}
	// Some Conductor captures retain the Agent result's structured usage but
	// none of the child's request envelopes. It is evidence for that child,
	// not the parent request that launched it. These counters can describe only
	// the final request. Only recover a completed run with no tool calls and
	// matching counters, when the child stream has no positive accounting.
	// Never add them on top of requests or task totals.
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Kind != "delegation_result" || message.CallID == "" || streamAliases[message.CallID] == "" {
			continue
		}
		details := resultDetails[i]
		counts := singleRequestAgentResult(details)
		if counts == nil {
			continue
		}
		stream := streamAliases[message.CallID]
		if message.usageInChild {
			// An explicit zero contribution also tells stored-message repair
			// this was reconciled, rather than evidence simply being absent.
			reports[i] = tokenReport{counts: tokenCounts{"total_tokens": 0}, scope: "task", stream: stream, requestOnly: true}
			continue
		}
		if accountedStreams[stream] {
			continue
		}
		model := firstString(details["model"])
		if model == "" {
			launch := launches[stream]
			payload := mapValueDefault(parseJSONValue(launch.Text))
			input := mapValueDefault(payload["input"])
			if resume := firstString(input["resume"]); resume != "" && firstString(input["model"]) == "" {
				model = defaultString(nilIfEmpty(agentModels[resume]), "Unknown model")
			} else {
				model = launchModel(launch)
			}
		}
		reports[i] = tokenReport{counts: counts, scope: "task", stream: stream, model: model, requestOnly: true}
		markAccounted(stream)
	}
	tasks := []int{}
	for i, r := range reports {
		if r.scope == "task" && r.counts != nil {
			tasks = append(tasks, i)
		}
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		return streamDepth(reports[tasks[i]].stream, parents) > streamDepth(reports[tasks[j]].stream, parents)
	})
	for _, index := range tasks {
		r := reports[index]
		attributed := tokenCounts{}
		for childIndex, child := range reports {
			if childIndex == index || child.counts == nil || (child.scope != "request" && child.scope != "task") {
				continue
			}
			if streamDescendsFrom(child.stream, r.stream, parents) {
				addTokenCounts(attributed, child.counts)
			}
		}
		reports[index].counts = tokenRemainder(r.counts, attributed)
	}
	for i, r := range reports {
		if r.counts == nil {
			continue
		}
		if r.scope == "run" || r.scope == "session" {
			attributed := tokenCounts{}
			byModel := map[string]tokenCounts{}
			addModel := func(model string, counts tokenCounts) {
				family := tokenModelFamily(model, r.models)
				if byModel[family] == nil {
					byModel[family] = tokenCounts{}
				}
				addTokenCounts(byModel[family], counts)
			}
			for offset, prior := range reports[r.start:i] {
				if prior.counts == nil {
					continue
				}
				if r.stream == "main" || prior.stream == r.stream {
					addTokenCounts(attributed, prior.counts)
					if prior.models != nil {
						for model, counts := range prior.models {
							addModel(model, counts)
						}
					} else {
						addModel(firstString(nilIfEmpty(prior.model), messages[r.start+offset].Model), prior.counts)
					}
				}
			}
			r.counts = tokenRemainder(r.counts, attributed)
			allocated := tokenCounts{}
			for model, counts := range r.models {
				r.models[model] = tokenRemainder(counts, byModel[modelFamily(model)])
				addTokenCounts(allocated, r.models[model])
			}
			// Keep the model split only when every dimension reconciles. A
			// missing request model or cache TTL split must not invent prices.
			for _, field := range usageTokenFields {
				if allocated[field] != r.counts[field] {
					r.models = nil
					break
				}
			}
		}
		reports[i] = r
	}
	return reports
}

func conversationTokenCounts(messages []MessageRecord) tokenCounts {
	return conversationTokenCountsWithCostState(messages, false)
}

func conversationTokenCountsWithCostState(messages []MessageRecord, suppressCostState bool) tokenCounts {
	totals := tokenCounts{}
	for _, report := range conversationTokenReportsWithCostState(messages, suppressCostState) {
		addTokenCounts(totals, report.counts)
	}
	return totals
}

type agentSessionSummary struct {
	nativeID, parentNativeID string
	delegationMessageIndex   int
	depth                    int
	messageIndexes           []int
	counts                   tokenCounts
	usage                    map[usageBucket]tokenCounts
	startedAt, endedAt       string
}

// usageBucket places reconciled usage on the timeline at the UTC hour of the
// accounting event that reported it. Events without a timestamp are placed at
// the session start and marked, so trends never silently invent precision.
type usageBucket struct {
	hour, model, effort, attribution string
}

func usageHour(timestamp string) string {
	parsed, ok := parseTime(timestamp)
	if !ok {
		return ""
	}
	return parsed.UTC().Truncate(time.Hour).Format(time.RFC3339)
}

func (s *agentSessionSummary) addUsage(message MessageRecord, counts tokenCounts, attribution string) {
	bucket := usageBucket{hour: usageHour(message.CreatedAt), model: strings.TrimSpace(message.Model), effort: message.Effort, attribution: attribution}
	if bucket.hour == "" && attribution == "request" {
		bucket.attribution = "session-start"
	}
	if s.usage[bucket] == nil {
		s.usage[bucket] = tokenCounts{}
	}
	addTokenCounts(s.usage[bucket], counts)
}

func agentSessionSummaries(messages []MessageRecord) []agentSessionSummary {
	return agentSessionSummariesWithCostState(messages, false)
}

func agentSessionSummariesWithCostState(messages []MessageRecord, suppressCostState bool) []agentSessionSummary {
	root := &agentSessionSummary{nativeID: "main", delegationMessageIndex: -1, counts: tokenCounts{}, usage: map[usageBucket]tokenCounts{}}
	sessions := map[string]*agentSessionSummary{"main": root}
	aliases := tokenStreamAliases(messages)
	order := []string{"main"}
	messageSessions := make([]string, len(messages))
	// Materialize every delegation before assigning messages so nested children
	// remain queryable even when a provider emits their records out of order.
	for index, message := range messages {
		if message.Kind != "delegation" {
			continue
		}
		key := defaultString(nilIfEmpty(message.CallID), message.NativeID)
		if key == "" || sessions[key] != nil {
			continue
		}
		sessions[key] = &agentSessionSummary{nativeID: key, delegationMessageIndex: index, counts: tokenCounts{}, usage: map[usageBucket]tokenCounts{}}
		order = append(order, key)
	}
	for index, message := range messages {
		key := "main"
		if canonical := aliases[message.ParentNativeID]; sessions[canonical] != nil {
			key = canonical
		}
		messageSessions[index] = key
		session := sessions[key]
		session.messageIndexes = append(session.messageIndexes, index)
		if message.CreatedAt != "" {
			if session.startedAt == "" || message.CreatedAt < session.startedAt {
				session.startedAt = message.CreatedAt
			}
			if session.endedAt == "" || message.CreatedAt > session.endedAt {
				session.endedAt = message.CreatedAt
			}
		}
		if message.Kind == "delegation" {
			child := defaultString(nilIfEmpty(message.CallID), message.NativeID)
			if sessions[child] != nil {
				sessions[child].parentNativeID = key
			}
		}
	}
	sessionEfforts := map[string]string{}
	for key, session := range sessions {
		sessionEfforts[key] = sessionMessageEffort(session.messageIndexes, messages)
	}
	for index, report := range conversationTokenReportsWithCostState(messages, suppressCostState) {
		if report.counts == nil {
			continue
		}
		key := report.stream
		key = defaultString(aliases[key], key)
		if key == "" || key == "main" {
			key = messageSessions[index]
		}
		if sessions[key] == nil {
			parent := messageSessions[index]
			if parent == key || parent == "" {
				parent = "main"
			}
			sessions[key] = &agentSessionSummary{nativeID: key, parentNativeID: parent, delegationMessageIndex: -1, counts: tokenCounts{}, usage: map[usageBucket]tokenCounts{}}
			order = append(order, key)
		}
		if prior := messageSessions[index]; key != prior {
			kept := sessions[prior].messageIndexes[:0]
			for _, messageIndex := range sessions[prior].messageIndexes {
				if messageIndex != index {
					kept = append(kept, messageIndex)
				}
			}
			sessions[prior].messageIndexes = kept
			sessions[key].messageIndexes = append(sessions[key].messageIndexes, index)
			messageSessions[index] = key
			if timestamp := messages[index].CreatedAt; timestamp != "" {
				if sessions[key].startedAt == "" || timestamp < sessions[key].startedAt {
					sessions[key].startedAt = timestamp
				}
				if sessions[key].endedAt == "" || timestamp > sessions[key].endedAt {
					sessions[key].endedAt = timestamp
				}
			}
		}
		addTokenCounts(sessions[key].counts, report.counts)
		message := messages[index]
		if report.model != "" {
			message.Model = report.model
		}
		if report.scope != "request" && report.scope != "thread" {
			// A run/session claim can span effort changes. Only label it when
			// the entire session has one recorded configuration.
			message.Effort = sessionEfforts[key]
		}
		attribution := "request"
		if !report.requestOnly && matchesFold(report.scope, "run", "session", "task") {
			attribution = report.scope + "-remainder"
		}
		if report.models != nil {
			for model, counts := range report.models {
				if counts["total_tokens"] <= 0 {
					continue
				}
				message.Model = model
				sessions[key].addUsage(message, counts, attribution)
			}
		} else {
			sessions[key].addUsage(message, report.counts, attribution)
		}
	}
	for _, session := range sessions {
		session.startedAt, session.endedAt = "", ""
		for _, index := range session.messageIndexes {
			timestamp := messages[index].CreatedAt
			if timestamp != "" && (session.startedAt == "" || timestamp < session.startedAt) {
				session.startedAt = timestamp
			}
			if timestamp != "" && (session.endedAt == "" || timestamp > session.endedAt) {
				session.endedAt = timestamp
			}
		}
		start := usageHour(defaultString(session.startedAt, session.endedAt))
		for bucket, counts := range session.usage {
			if bucket.hour != "" || start == "" {
				continue
			}
			delete(session.usage, bucket)
			bucket.hour = start
			if session.usage[bucket] == nil {
				session.usage[bucket] = tokenCounts{}
			}
			addTokenCounts(session.usage[bucket], counts)
		}
	}
	depth := func(key string) int {
		value, seen := 0, map[string]bool{}
		for key != "" && key != "main" && !seen[key] {
			seen[key] = true
			key = sessions[key].parentNativeID
			value++
		}
		return value
	}
	result := make([]agentSessionSummary, 0, len(order))
	for _, key := range order {
		sessions[key].depth = depth(key)
		result = append(result, *sessions[key])
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].depth < result[j].depth })
	return result
}

func reconciledTokenMetrics(record WorkspaceRecord) []map[string]any {
	record.Conversations = conversationsWithChildUsage(record.Conversations)
	totals := tokenCounts{}
	for _, c := range record.Conversations {
		remainders := claudeGroupRemainder(record.Conversations, c)
		addTokenCounts(totals, conversationTokenCountsWithCostState(c.Messages, remainders != nil))
		for _, remainder := range remainders {
			addTokenCounts(totals, remainder)
		}
	}
	if _, ok := totals["total_tokens"]; ok {
		if unclassified := math.Max(0, totals["total_tokens"]-totals["input_tokens"]-totals["output_tokens"]); unclassified > 0 {
			totals["unclassified_tokens"] = unclassified
		}
	}
	return tokenMetricRows(totals)
}

func tokenMetricRows(totals tokenCounts) []map[string]any {
	rows := []map[string]any{}
	for name, value := range totals {
		rows = append(rows, map[string]any{"name": name, "value": value, "unit": "tokens", "status": "reported-reconciled", "extractor_version": "tokens-v2", "definition": "Retained request usage plus unallocated aggregate remainder. Input includes cached input; cache and reasoning are subsets. Raw accounting evidence retained on messages."})
	}
	sort.Slice(rows, func(i, j int) bool { return firstString(rows[i]["name"]) < firstString(rows[j]["name"]) })
	return rows
}

func accountingRaw(event map[string]any) string {
	if raw := firstString(event["raw_text"]); raw != "" {
		return raw
	}
	if hasUsageEvidence(event) {
		return jsonText(event)
	}
	return ""
}

// Only inspect protocol envelopes, never tool arguments, output, or prose.
// Retain the entire original accounting object so new dimensions remain
// inspectable even when they do not contribute to the token total.
func hasUsageEvidence(event map[string]any) bool {
	for _, key := range []string{"usage", "modelUsage", "total_token_usage", "last_token_usage", "thread_token_usage", "turn_token_usage", "latest_token_usage_record", "estimated_tokens", "estimated_tokens_delta"} {
		if event[key] != nil {
			return true
		}
	}
	for _, key := range []string{"payload", "info", "event", "msg", "message", "response", "metadata"} {
		if value := mapValue(event[key]); value != nil && hasUsageEvidence(value) {
			return true
		}
	}
	return false
}

func sessionMessageEffort(indexes []int, messages []MessageRecord) string {
	if len(indexes) == 0 {
		return ""
	}
	effort := messages[indexes[0]].Effort
	for _, index := range indexes[1:] {
		if messages[index].Effort != effort {
			return ""
		}
	}
	return effort
}
