package archive

import (
	"context"
	"sort"
	"time"
)

// Calendar columns are local calendar intervals, not fixed 24-hour multiples.
// Weeks begin on Monday, matching the analysis tables.
func usageSummaryBounds(key string, span time.Duration, now time.Time) (time.Time, time.Time) {
	day := localMidnight(now, time.Local)
	switch key {
	case "today":
		return day, now
	case "yesterday":
		return day.AddDate(0, 0, -1), day
	case "week":
		return day.AddDate(0, 0, -(int(day.Weekday())+6)%7), now
	case "month":
		return time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, day.Location()), now
	}
	if span > 0 {
		return now.Add(-span), now
	}
	return time.Time{}, now
}

type quickStatsCounts struct {
	Conversations int64    `json:"active_conversations"`
	Messages      int64    `json:"message_count"`
	UserMessages  int64    `json:"user_messages"`
	Replies       int64    `json:"assistant_messages"`
	ToolCalls     int64    `json:"tool_calls"`
	FailedCalls   int64    `json:"failed_tool_calls"`
	FailureRate   *float64 `json:"tool_failure_rate"`
	ToolTime      *int64   `json:"tool_time_ms"`
	TimedCalls    int64    `json:"timed_tool_calls"`
	DelegatedRuns int64    `json:"delegated_runs"`
	Repositories  int64    `json:"active_repositories"`
	// Retained observations, including copies of history a transcript
	// inherited; the counts above include each physical occurrence once.
	MessageObservations  int64 `json:"message_observations"`
	ToolCallObservations int64 `json:"tool_call_observations"`
	// Observations whose equivalence with another the evidence cannot
	// decide. They are counted separately above; a nonzero value makes the
	// canonical counts partial.
	UnresolvedOccurrences int64 `json:"unresolved_occurrences"`
	// Transcripts counted here whose copied history the evidence cannot
	// account for (occurrenceIndex.Partial): their messages and calls are
	// counted as their own, so the canonical counts are partial.
	PartialConversations int64  `json:"occurrence_partial_conversations"`
	OccurrenceCoverage   string `json:"occurrence_coverage"`
	// Ancestry coverage of the conversation and run counts above, from the
	// shared resolution (executionAncestry): transcripts active here whose
	// top-level conversation is unknown (not in active_conversations),
	// transcripts counted under a top-level conversation reached only through
	// an inferred or unverified link, and transcripts whose execution identity
	// is in an unresolved conflict, so the distinct counts that include them
	// are not exact. Complete when all are zero, qualified when only the
	// second is not, otherwise partial.
	UnrootedConversations  int64  `json:"unrooted_conversations"`
	QualifiedConversations int64  `json:"ancestry_qualified_conversations"`
	UncertainConversations int64  `json:"identity_uncertain_conversations"`
	AncestryCoverage       string `json:"ancestry_coverage"`
}

// The default header and native menu retain their inexpensive usage-only read.
// Detailed counts are requested by an expanded editor or an opted-in count row.
// No transcript text is read, and mirrors/continuations/linked child runs use
// the same canonical execution identities as the analysis tables.
func (c *Catalog) addQuickStatsCounts(ctx context.Context, summary map[string]any) error {
	workspaces, err := queryMapsContext(ctx, c.reader(), `SELECT id,source_kind,activity_at,repository_id FROM workspaces`)
	if err != nil {
		return err
	}
	kept, repos := map[string]bool{}, map[string]string{}
	var ledger []map[string]any
	for _, w := range workspaces {
		repos[firstString(w["id"])] = firstString(w["repository_id"])
	}
	for _, w := range c.suppressMirrors(workspaces) {
		id := firstString(w["id"])
		kept[id] = true
		ledger = append(ledger, map[string]any{"workspace_id": id})
	}
	// Occurrence ownership: a transcript's copy of history it inherited is
	// not its own activity. The original is counted where it happened.
	lineage, occurrences, err := c.usageConversationRootsAndOccurrences(ctx, ledger)
	if err != nil {
		return err
	}
	copied, candidates := map[string]bool{}, map[string]bool{}
	for _, ref := range occurrences.Classified() {
		if ref.Kind != sourceMessage {
			continue
		}
		switch occurrences.Lookup(ref, sourceRef{}).Role {
		case occurrenceInherited, occurrenceDuplicate:
			copied[ref.ID] = true
		case occurrenceUnresolved:
			candidates[ref.ID] = true
		}
	}
	copiedIDs := make([]string, 0, len(copied))
	for id := range copied {
		copiedIDs = append(copiedIDs, id)
	}
	copiedJSON := jsonText(copiedIDs)
	owners, err := queryMapsContext(ctx, c.reader(), `SELECT id,workspace_id FROM conversations`)
	if err != nil {
		return err
	}
	retained := map[string]bool{}
	ownerWorkspaces := map[string]string{}
	for _, owner := range owners {
		id := firstString(owner["id"])
		ownerWorkspaces[id] = firstString(owner["workspace_id"])
		retained[id] = kept[firstString(owner["workspace_id"])] && lineage.canonical[id] == id
	}
	type bucket struct {
		from, until                                                 time.Time
		roots, runs, repos, partial, unrooted, qualified, uncertain map[string]bool
	}
	windows := summary["windows"].([]*usageSummaryWindow)
	buckets := make([]bucket, len(windows))
	for i, w := range windows {
		w.quickStatsCounts = &quickStatsCounts{}
		from, _ := parseTime(w.From)
		until, _ := parseTime(w.Until)
		buckets[i] = bucket{from, until, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}}
	}
	// A run whose session's identity is in conflict leaves every count of
	// distinct runs that includes it uncertain, wherever its activity lands.
	uncertainRuns := map[string]bool{}
	for id := range lineage.uncertain {
		if run := lineage.delegatedRuns[id]; run != "" {
			uncertainRuns[run] = true
		}
		if run := lineage.runs[id]; run != "" {
			uncertainRuns[run] = true
		}
	}
	// visit applies a timestamp's counts to the windows it falls in. Only an
	// own observation (active) registers the conversation, run and repository
	// as active then: a copy of history is counted as an observation, but its
	// timestamp is when the original happened, not activity of the transcript
	// that inherited it. Unresolved observations count as own.
	visit := func(cv, at, run string, active bool, apply func(*quickStatsCounts)) {
		if !retained[cv] {
			return
		}
		when, valid := parseTime(at)
		for i, w := range windows {
			b := &buckets[i]
			if valid && !when.Before(b.until) || !b.from.IsZero() && (!valid || when.Before(b.from)) {
				continue
			}
			apply(w.quickStatsCounts)
			if occurrences.Partial(cv) != "" {
				b.partial[cv] = true
			}
			if !active {
				continue
			}
			workspace := ownerWorkspaces[cv]
			if root := lineage.roots[cv]; root != nil {
				b.roots[firstString(root["id"])] = true
				workspace = firstString(root["workspace_id"])
				if lineage.rootQuality[cv] != ancestryObserved {
					b.qualified[cv] = true
				}
			} else {
				b.unrooted[cv] = true
			}
			if lineage.uncertain[cv] || uncertainRuns[run] {
				b.uncertain[cv] = true
			}
			if repo := repos[workspace]; repo != "" {
				b.repos[repo] = true
			}
			if run != "" {
				b.runs[run] = true
			}
		}
	}
	// Group simultaneous events before crossing the Go boundary. Tools and
	// system events establish activity, but do not inflate message totals.
	messages, err := c.reader().QueryContext(ctx, `SELECT conversation_id,created_at,
 SUM(CASE WHEN kind='message' AND role='user' THEN 1 ELSE 0 END),
 SUM(CASE WHEN kind='message' AND role='assistant' THEN 1 ELSE 0 END),
 SUM(CASE WHEN kind='message' AND role='user' AND NOT EXISTS(
 SELECT 1 FROM agent_session_messages am JOIN agent_sessions a ON a.id=am.agent_session_id
 WHERE am.message_id=messages.id AND (a.kind='subagent' OR a.depth>0)) THEN 1 ELSE 0 END),
 SUM(CASE WHEN kind='message' AND role IN ('user','assistant') AND id IN (SELECT value FROM json_each(?1)) THEN 1 ELSE 0 END),
 SUM(CASE WHEN kind='message' AND role='user' AND id IN (SELECT value FROM json_each(?1)) THEN 1 ELSE 0 END),
 SUM(CASE WHEN kind='message' AND role='assistant' AND id IN (SELECT value FROM json_each(?1)) THEN 1 ELSE 0 END),
 SUM(CASE WHEN kind='message' AND role IN ('user','assistant') AND id IN (SELECT value FROM json_each(?2)) THEN 1 ELSE 0 END),
 SUM(CASE WHEN id IN (SELECT value FROM json_each(?1)) THEN 0 ELSE 1 END)
 FROM messages WHERE selected=1 GROUP BY conversation_id,created_at`, copiedJSON, jsonText(quickStatsKeys(candidates)))
	if err != nil {
		return err
	}
	// Each transcript's first own observation, and which were observed at all,
	// anchor its sessions below.
	firstOwn, observed := map[string]time.Time{}, map[string]bool{}
	observe := func(cv, at string, own bool) {
		observed[cv] = true
		if when, valid := parseTime(at); own && valid && (firstOwn[cv].IsZero() || when.Before(firstOwn[cv])) {
			firstOwn[cv] = when
		}
	}
	for messages.Next() {
		var cv string
		var at *string
		var users, replies, topUsers, copies, copiedUsers, copiedReplies, unresolved, own int64
		if err := messages.Scan(&cv, &at, &users, &replies, &topUsers, &copies, &copiedUsers, &copiedReplies, &unresolved, &own); err != nil {
			messages.Close()
			return err
		}
		timestamp := ""
		if at != nil {
			timestamp = *at
		}
		observe(cv, timestamp, own > 0)
		visit(cv, timestamp, lineage.runs[cv], own > 0, func(w *quickStatsCounts) {
			w.MessageObservations += users + replies
			w.Messages += users + replies - copies
			w.Replies += replies - copiedReplies
			w.UnresolvedOccurrences += unresolved
			if lineage.runs[cv] == "" && lineage.roots[cv] != nil {
				// Copied user turns in a root's own continuation are its history.
				w.UserMessages += max(0, topUsers-copiedUsers)
			}
		})
	}
	err = messages.Err()
	messages.Close()
	if err != nil {
		return err
	}
	calls, err := c.reader().QueryContext(ctx, `SELECT conversation_id,COALESCE(started_at,''),COALESCE(agent_session_id,''),status,duration_ms,COALESCE(call_message_id,'') FROM tool_calls`)
	if err != nil {
		return err
	}
	for calls.Next() {
		var cv, at, session, status, message string
		var duration *int64
		if err := calls.Scan(&cv, &at, &session, &status, &duration, &message); err != nil {
			calls.Close()
			return err
		}
		run := lineage.delegatedRuns[session]
		if run == "" {
			run = lineage.runs[cv]
		}
		observe(cv, at, !copied[message])
		if copied[message] {
			// Recorded by a copy of history: another transcript owns the call.
			visit(cv, at, run, false, func(w *quickStatsCounts) { w.ToolCallObservations++ })
			continue
		}
		visit(cv, at, run, true, func(w *quickStatsCounts) {
			w.ToolCallObservations++
			if candidates[message] {
				w.UnresolvedOccurrences++
			}
			w.ToolCalls++
			if status == "error" {
				w.FailedCalls++
			}
			if duration != nil && *duration >= 0 {
				if w.ToolTime == nil {
					w.ToolTime = new(int64)
				}
				*w.ToolTime += *duration
				w.TimedCalls++
			}
		})
	}
	err = calls.Err()
	calls.Close()
	if err != nil {
		return err
	}
	// Executions without activity of their own, tokenless or a bare handle,
	// stay visible through their sessions, anchored on owned evidence: a
	// session's envelope is derived from its first message, which may be a
	// copy. A launch registers when its launcher issued it; a stream no earlier
	// than its transcript's first own observation, and only in unbounded
	// windows when it observed only copies. Only a stream with no observations
	// keeps its recorded start.
	sessions, err := queryMapsContext(ctx, c.reader(), `SELECT a.id,a.conversation_id,a.started_at,COALESCE(a.delegation_message_id,'') launch,m.created_at launch_at
 FROM agent_sessions a LEFT JOIN messages m ON m.id=a.delegation_message_id`)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		id, cv := firstString(session["id"]), firstString(session["conversation_id"])
		run := lineage.delegatedRuns[id]
		if _, known := lineage.delegatedDepth[id]; run == "" || !known || lineage.resolution.BindSession(id).State != bindingResolved {
			// Attempts are not executions.
			continue
		}
		at := firstString(session["started_at"])
		switch launch := firstString(session["launch"]); {
		case launch != "" && copied[launch]:
			continue // a copied launch counts at its original
		case launch != "":
			at = firstString(session["launch_at"])
		case !firstOwn[cv].IsZero():
			if when, valid := parseTime(at); !valid || when.Before(firstOwn[cv]) {
				at = firstOwn[cv].UTC().Format(time.RFC3339Nano)
			}
		case observed[cv]:
			at = ""
		}
		visit(cv, at, run, true, func(*quickStatsCounts) {})
	}
	for i, w := range windows {
		b := buckets[i]
		w.Conversations = int64(len(b.roots))
		w.DelegatedRuns = int64(len(b.runs))
		w.Repositories = int64(len(b.repos))
		w.PartialConversations = int64(len(b.partial))
		w.UnrootedConversations, w.QualifiedConversations, w.UncertainConversations = int64(len(b.unrooted)), int64(len(b.qualified)), int64(len(b.uncertain))
		switch {
		case w.UnrootedConversations > 0 || w.UncertainConversations > 0:
			w.AncestryCoverage = "partial"
		case w.QualifiedConversations > 0:
			w.AncestryCoverage = "qualified"
		default:
			w.AncestryCoverage = "complete"
		}
		w.OccurrenceCoverage = "complete"
		if w.UnresolvedOccurrences > 0 || w.PartialConversations > 0 {
			w.OccurrenceCoverage = "partial"
		}
		if w.ToolCalls == 0 {
			w.ToolTime = new(int64)
		}
		if w.ToolCalls > 0 {
			rate := 100 * float64(w.FailedCalls) / float64(w.ToolCalls)
			w.FailureRate = &rate
		}
	}
	return ctx.Err()
}

func quickStatsKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
