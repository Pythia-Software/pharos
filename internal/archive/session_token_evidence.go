package archive

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// A harness result counter has no token categories and may describe only the
// final request. Preserve it separately from the reconciled, priceable ledger.
// Its maximum is a lower bound, never an additional charge or a full run total.
type sessionTokenEvidence struct {
	Scope          string `json:"scope"`
	TotalTokens    int64  `json:"total_tokens"`
	MessageID      string `json:"message_id"`
	ConversationID string `json:"conversation_id"`
	WorkspaceID    string `json:"workspace_id"`
}

type streamTokenEvidence struct {
	stream string
	index  int
	total  int64
}

var agentUsageFooter = regexp.MustCompile(`^agentId: ([A-Za-z0-9_-]+) (.+)\n<usage>subagent_tokens: ([0-9]+)\ntool_uses: ([0-9]+)\nduration_ms: ([0-9]+)</usage>$`)

func conversationTokenEvidence(messages []MessageRecord) []streamTokenEvidence {
	launches := map[string]MessageRecord{}
	for _, m := range messages {
		if m.Kind == "delegation" && m.CallID != "" {
			call := mapValueDefault(parseJSONValue(m.Text))
			if matchesFold(firstString(call["tool"], call["name"]), "Agent", "Task") {
				launches[m.CallID] = m
			}
		}
	}
	var evidence []streamTokenEvidence
	for i, m := range messages {
		launch, known := launches[m.CallID]
		if m.Kind != "delegation_result" || !known {
			continue
		}
		if _, total, ok := agentUsageFooterOf(launch.Text, m.Text); ok {
			evidence = append(evidence, streamTokenEvidence{stream: m.CallID, index: i, total: total})
		}
	}
	return evidence
}

// agentUsageFooterTrailer begins every harness usage footer.
const agentUsageFooterTrailer = "<usage>subagent_tokens:"

// agentUsageFooterMarker is the part of the trailer a stored JSON result
// keeps verbatim (encoders may escape its angle brackets), for SQL that reads
// a result in full only when it may hold a footer.
const agentUsageFooterMarker = "subagent_tokens:"

// agentUsageFooterOf validates a harness usage footer on the result of an
// Agent or Task launch call (launch is the call's body) and returns the agent
// it names and the tokens it reports.
func agentUsageFooterOf(launch, result string) (agent string, total int64, ok bool) {
	call := mapValueDefault(parseJSONValue(launch))
	if !matchesFold(firstString(call["tool"], call["name"]), "Agent", "Task") {
		return "", 0, false
	}
	agent, total, ok = agentUsageFooterResult(result)
	if !ok {
		return "", 0, false
	}
	input := mapValueDefault(call["input"])
	if resume := firstString(input["resume"]); resume != "" && resume != agent {
		return "", 0, false
	}
	return agent, total, true
}

// agentUsageFooterResult is the result side of agentUsageFooterOf: the
// footer is the result's last text block, exactly once, under no error or
// contradicting typed identity.
func agentUsageFooterResult(text string) (agent string, total int64, ok bool) {
	var result struct {
		Content []struct{ Type, Text string }
		IsError bool `json:"is_error"`
	}
	if json.Unmarshal([]byte(text), &result) != nil || result.IsError || len(result.Content) == 0 {
		return "", 0, false
	}
	last := result.Content[len(result.Content)-1]
	if last.Type != "text" {
		return "", 0, false
	}
	match := agentUsageFooter.FindStringSubmatch(last.Text)
	if match == nil {
		return "", 0, false
	}
	agent = match[1]
	if match[2] != "(use SendMessage with to: '"+agent+"', summary: '<5-10 word recap>' to continue this agent)" {
		return "", 0, false
	}
	// A contradictory typed identity is not trustworthy footer evidence.
	details := mapValueDefault(mapValueDefault(parseJSONValue(text))["details"])
	if id := firstString(details["agentId"]); id != "" && id != agent {
		return "", 0, false
	}
	total, err := strconv.ParseInt(match[3], 10, 64)
	if err != nil || total <= 0 || total > 1<<53-1 {
		return "", 0, false
	}
	// Reject duplicate trailers, even if one is attached to report prose.
	trailers := 0
	for _, block := range result.Content {
		trailers += strings.Count(block.Text, agentUsageFooterTrailer)
	}
	if trailers != 1 {
		return "", 0, false
	}
	return agent, total, true
}

// The ledger remains the accounting authority. These fields distinguish where
// usage was consumed from where an aggregate was recorded. Observations may
// overlap either; they are never inserted into the priced ledger.
type sessionTokenAttribution struct {
	Accounted   float64                `json:"accounted_tokens"`
	Direct      float64                `json:"direct_tokens"`
	Unallocated float64                `json:"unallocated_tokens"`
	Observed    float64                `json:"observed_minimum_tokens"`
	Evidence    []sessionTokenEvidence `json:"evidence,omitempty"`
}

// Reconcile each included branch independently. Only an ancestor's aggregate
// remainder can cover an observation in its subtree. A sibling or unrelated
// root must never hide that observation. Known request totals may overlap a
// last-request observation on the same session, so use their maximum there.
func delegationTokenTotal(nodes []*delegationNode) delegationMetric {
	if len(nodes) == 1 {
		n := nodes[0]
		if a := n.TokenAttribution; a != nil && a.Accounted == 0 && a.Observed > 0 {
			return delegationNumber(a.Observed, "reported-minimum", 1, 1)
		}
	}
	included := map[string]*delegationNode{}
	children := map[string][]*delegationNode{}
	for _, n := range nodes {
		included[n.ID] = n
		children[n.ParentID] = append(children[n.ParentID], n)
	}
	known, partial, estimated := 0, false, false
	var walk func(*delegationNode) (float64, float64)
	walk = func(n *delegationNode) (accounted, uncovered float64) {
		m := n.Self["tokens"]
		if a := n.TokenAttribution; a != nil {
			// Self's partial quality can mean only that a recorded aggregate
			// has not been allocated to this layer. Its accounted total still
			// has the original source quality.
			if original, ok := n.accountedTokens["tokens"]; ok {
				m = original
			}
			if a.Observed > a.Direct {
				partial = true
			}
			accounted = a.Accounted
			uncovered = max(0, a.Observed-a.Direct)
			if accounted > 0 || a.Observed > 0 {
				known++
			} else {
				partial = true
			}
		} else if m.Value != nil {
			accounted = *m.Value
			known++
		} else {
			partial = true
		}
		estimated = estimated || m.IsEstimate
		if m.Quality == "partial" || m.Quality == "reported-minimum" {
			partial = true
		}
		for _, child := range children[n.ID] {
			c, extra := walk(child)
			accounted += c
			uncovered += extra
		}
		if a := n.TokenAttribution; a != nil {
			uncovered = max(0, uncovered-a.Unallocated)
		}
		return
	}
	total := 0.0
	for _, n := range nodes {
		if included[n.ParentID] == nil {
			accounted, uncovered := walk(n)
			total += accounted + uncovered
		}
	}
	if known == 0 {
		return delegationUnknown()
	}
	quality := "reported"
	if estimated {
		quality = "estimated"
	}
	if partial {
		quality = "partial"
	}
	result := delegationNumber(total, quality, known, len(nodes))
	result.IsEstimate = estimated
	return result
}
