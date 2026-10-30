package archive

import (
	"context"
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
	lineage, err := c.usageConversationRoots(ledger)
	if err != nil {
		return err
	}
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
		from, until        time.Time
		roots, runs, repos map[string]bool
	}
	windows := summary["windows"].([]*usageSummaryWindow)
	buckets := make([]bucket, len(windows))
	for i, w := range windows {
		w.quickStatsCounts = &quickStatsCounts{}
		from, _ := parseTime(w.From)
		until, _ := parseTime(w.Until)
		buckets[i] = bucket{from, until, map[string]bool{}, map[string]bool{}, map[string]bool{}}
	}
	visit := func(cv, at, run string, apply func(*quickStatsCounts)) {
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
			workspace := ownerWorkspaces[cv]
			if root := lineage.roots[cv]; root != nil {
				b.roots[firstString(root["id"])] = true
				workspace = firstString(root["workspace_id"])
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
 WHERE am.message_id=messages.id AND (a.kind='subagent' OR a.depth>0)) THEN 1 ELSE 0 END)
 FROM messages WHERE selected=1 GROUP BY conversation_id,created_at`)
	if err != nil {
		return err
	}
	for messages.Next() {
		var cv string
		var at *string
		var users, replies, topUsers int64
		if err := messages.Scan(&cv, &at, &users, &replies, &topUsers); err != nil {
			messages.Close()
			return err
		}
		timestamp := ""
		if at != nil {
			timestamp = *at
		}
		visit(cv, timestamp, lineage.runs[cv], func(w *quickStatsCounts) {
			w.Messages += users + replies
			w.Replies += replies
			if lineage.runs[cv] == "" && lineage.roots[cv] != nil {
				w.UserMessages += topUsers
			}
		})
	}
	err = messages.Err()
	messages.Close()
	if err != nil {
		return err
	}
	calls, err := c.reader().QueryContext(ctx, `SELECT conversation_id,COALESCE(started_at,''),COALESCE(agent_session_id,''),status,duration_ms FROM tool_calls`)
	if err != nil {
		return err
	}
	for calls.Next() {
		var cv, at, session, status string
		var duration *int64
		if err := calls.Scan(&cv, &at, &session, &status, &duration); err != nil {
			calls.Close()
			return err
		}
		run := lineage.delegatedRuns[session]
		if run == "" {
			run = lineage.runs[cv]
		}
		visit(cv, at, run, func(w *quickStatsCounts) {
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
	sessions, err := queryMapsContext(ctx, c.reader(), `SELECT id,conversation_id,started_at FROM agent_sessions`)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		id := firstString(session["id"])
		run := lineage.delegatedRuns[id]
		if _, known := lineage.delegatedDepth[id]; run == "" || !known {
			continue
		}
		visit(firstString(session["conversation_id"]), firstString(session["started_at"]), run, func(*quickStatsCounts) {})
	}
	for i, w := range windows {
		b := buckets[i]
		w.Conversations = int64(len(b.roots))
		w.DelegatedRuns = int64(len(b.runs))
		w.Repositories = int64(len(b.repos))
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
