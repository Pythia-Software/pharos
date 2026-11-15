package archive

// Safeguard checks: an agent saying it got around an instruction, test,
// harness restriction or product rule (agent events of the family
// circumventing_constraint). They are Risky actions' own: rare, rarely worded
// alike, and worth a second look one at a time, so a single event raises a
// notice, sorted into a check by its words. Fixes (fixes.go) leaves them out
// and links here.

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"regexp"
)

// safetySafeguardChecks in order: an event takes the first that matches.
var safetySafeguardChecks = []struct {
	Rule    string
	Pattern *regexp.Regexp
}{
	{"safeguard-test", regexp.MustCompile(`(?i)(remov|relax|loosen|skip|disabl|drop|delet)\w*\b.{0,60}\b(tests?|checks?|assert\w*|cases?|gates?|verification)\b|\b(tests?|checks?|assert\w*)\b.{0,40}\b(remov|relax|loosen|skip|disabl)`)},
	{"safeguard-restriction", regexp.MustCompile(`(?i)sandbox|write tool|\bblock|forbidden|denied|restriction|heredoc|permission|\bstash\b|not allowed`)},
	{"safeguard-gate", regexp.MustCompile(`(?i)bypass|override|sidestep|sneak|disable\w* (the )?\w* ?(polic|gate|throttl|freshness|marker|rule)|pinning the clock`)},
	{"safeguard-other", regexp.MustCompile(`(?s).`)},
}

func init() {
	safetyRules["safeguard-test"] = safetyRule{"Agent weakened, skipped or removed a test or check", "An agent said it relaxed, skipped or deleted a test, assertion or check to get past it.",
		[]string{"Did the test or check fail because the work was wrong, or because the test was?", "Did the agent say so in its reply, and did the person agree?", "Did the weakened check reach a merged change?"}, safetyMedium,
		"A rule in the agent instructions that a failing test is reported, not relaxed or removed, unless the person agrees; or a review step that flags deleted assertions."}
	safetyRules["safeguard-restriction"] = safetyRule{"Agent got around a harness or tool restriction", "An agent said it worked around a sandbox, permission, blocked tool or similar restriction.",
		[]string{"Which restriction was it, and was the agent's need legitimate (an explicit request, a required output)?", "Did the workaround achieve what the restriction was meant to prevent?"}, safetyMedium,
		"Either make the restriction workable (allow the legitimate case explicitly) or state the prohibition so the workaround is ruled out too."}
	safetyRules["safeguard-gate"] = safetyRule{"Agent bypassed a product rule, gate or policy", "An agent said it bypassed, overrode or disabled a rule, gate or policy of the product it was working on.",
		[]string{"Was the bypass confined to a test or measurement, or could it ship?", "Is there a supported way to get the same result?"}, safetyMedium,
		"Document the supported way (a test hook, a flag) so agents don't invent one, or forbid the bypass in the agent instructions."}
	safetyRules["safeguard-other"] = safetyRule{"Agent got around an instruction another way", "An agent said it got around an instruction or safeguard in a way the other checks don't name.",
		[]string{"What instruction or safeguard was involved, and did the person know?"}, safetyLow,
		"Decide whether the instruction should hold; if so, say so in the agent instructions."}
}

func safetySafeguardRule(text string) string {
	for _, check := range safetySafeguardChecks {
		if check.Pattern.MatchString(text) {
			return check.Rule
		}
	}
	return "safeguard-other"
}

// safetySafeguardCandidates are the notices agent events raise in the window.
func safetySafeguardCandidates(ctx context.Context, read *sql.Tx, since string) ([]safetyCandidate, int, error) {
	rows, err := read.QueryContext(ctx, fmt.Sprintf(`SELECT e.id,e.message_id,e.conversation_id,c.workspace_id,COALESCE(w.repository_id,''),COALESCE(r.display_name,''),COALESCE(c.origin_host_id,''),COALESCE(h.label,''),
 e.source,e.sentence,e.subject,COALESCE(e.occurred_at,''),e.sentence_start
 FROM agent_events e JOIN conversations c ON c.id=e.conversation_id LEFT JOIN workspaces w ON w.id=c.workspace_id
 LEFT JOIN repositories r ON r.id=w.repository_id LEFT JOIN hosts h ON h.id=c.origin_host_id
 WHERE e.family='circumventing_constraint' AND e.probability>=%g AND e.occurred_at>=? ORDER BY e.occurred_at,e.id`, agentEventThreshold), since)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []safetyCandidate{}
	count := 0
	for rows.Next() {
		var id int64
		var start int
		var message, conversation, workspace, repository, repositoryLabel, host, hostLabel, source, sentence, subject, at string
		if err := rows.Scan(&id, &message, &conversation, &workspace, &repository, &repositoryLabel, &host, &hostLabel, &source, &sentence, &subject, &at, &start); err != nil {
			return nil, 0, err
		}
		count++
		text := sentence
		if subject != "" && subject != sentence {
			text += " " + subject
		}
		rule := safetySafeguardRule(text)
		target, label := "repo:"+repository, repositoryLabel
		if repository == "" {
			target, label = "machine:"+host, hostLabel
		}
		if repository == "" && host == "" {
			target, label = "workspace:"+workspace, "Work with unknown machine"
		}
		where := "In a reply"
		if source == "thinking" {
			where = "In the agent's thinking"
		}
		quote := clipText(safetyRedact(sentence), 1000)
		candidate := safetyCandidate{ID: stableID("safety", rule, target, "agent-event", message, start), Rule: rule, Target: target, Label: defaultString(nilIfEmpty(label), "Repository"),
			Subject: clipText(fixesTitle(safetyRedact(sentence)), 160), Repository: repository,
			Evidence: []safetyEvidence{{ID: fmt.Sprintf("e%d", id), Workspace: workspace, Conversation: conversation, Message: message, At: at, Command: quote,
				CWDBasis: "The agent's own words, found by the agent events pass; no command is implied", Status: source, Outcome: where, Quote: true,
				Path: "/work/" + workspace + "?conversation=" + url.QueryEscape(conversation) + "&message=" + url.QueryEscape(message)}}}
		out = append(out, candidate)
	}
	return out, count, rows.Err()
}
