package archive

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
)

// The attention list (docs/background-work.md §7.3): what needs the user,
// computed once on the server for the status snapshot, the native menu and
// the web chip. Each item says what is wrong and where to act; the first
// item sets the menu's single line, and the snapshot lists them all. An
// item is listed only when there is something to do about it.

type attentionAction struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

type attentionItem struct {
	ID       string          `json:"id"`
	Rule     int             `json:"rule"`
	Severity string          `json:"severity"` // error | warning
	Title    string          `json:"title"`
	Detail   string          `json:"detail,omitempty"`
	Action   attentionAction `json:"action"`
	// Library is the registered library's ID in the broker's union.
	Library string `json:"library,omitempty"`
}

var (
	openCaptureAndIndex = attentionAction{Label: "Open Capture & index", Path: "/settings/sync"}
	reviewIntegrity     = attentionAction{Label: "Review…", Path: "/settings/sync"}
	openBackups         = attentionAction{Label: "Open Backups", Path: "/settings/backups"}
)

// attentionRunning reports an index or capture run in progress: rules 3
// and 4 wait for it, as the web rule does (a failed index stops counting
// once another one runs).
func (s *Server) attentionRunning() bool {
	s.runsMu.RLock()
	defer s.runsMu.RUnlock()
	for _, run := range s.runs {
		if run.State == "running" {
			return true
		}
	}
	for _, run := range s.captures.list() {
		if run.State == "running" {
			return true
		}
	}
	return false
}

// attention evaluates rules 1–7 for this library on this Mac. Rule 8 (the
// library unavailable, disconnected or not set up) is the broker's and the
// native app's. A query that fails leaves its rule out: the list errs toward
// saying nothing it cannot back up, and the verdict, not this list, decides
// whether ejecting is safe.
func (s *Server) attention(ctx context.Context) []attentionItem {
	items := []attentionItem{}
	hostID := currentHost().ID
	enabled := map[string]bool{}
	for _, source := range s.Config().Sources {
		if source.Enabled {
			enabled[source.Name] = true
		}
	}
	enabledSource := func(name string) bool {
		if enabled[name] {
			return true
		}
		// Capture-index runs use host/source labels.
		_, source, qualified := strings.Cut(name, "/")
		return qualified && enabled[source]
	}
	relevantSources := func(names []string) bool {
		if len(names) == 0 {
			return true // a run-wide failure before choosing sources
		}
		for _, name := range names {
			if enabledSource(name) {
				return true
			}
		}
		return false
	}

	// 1. The latest attempt for an enabled source has an error other than a
	// stop's.
	if issues, err := queryMapsContext(ctx, s.Catalog.reader(), `WITH attempts AS (
		SELECT source_name,last_attempt_at at,error FROM source_states WHERE host_id=?
		UNION ALL SELECT source_name,attempted_at at,error FROM automatic_source_states WHERE host_id=?
	), latest AS (SELECT *,ROW_NUMBER() OVER (PARTITION BY source_name ORDER BY at DESC) n FROM attempts)
	SELECT source_name,error FROM latest WHERE n=1 AND COALESCE(error,'')<>'' AND error NOT LIKE 'interrupted:%' ORDER BY source_name`, hostID, hostID); err == nil {
		failed := []string{}
		for _, issue := range issues {
			if name := firstString(issue["source_name"]); enabled[name] {
				failed = append(failed, name+": "+firstString(issue["error"]))
			}
		}
		if len(failed) > 0 {
			title := "Indexing a source failed"
			if len(failed) > 1 {
				title = "Indexing some sources failed"
			}
			items = append(items, attentionItem{ID: "source-error", Rule: 1, Severity: "error", Title: title, Detail: strings.Join(failed, " · "), Action: openCaptureAndIndex})
		}
	}

	// 2. Index verification has open mismatches.
	var open int
	if s.Catalog.reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM sync_integrity_issues WHERE state='open'").Scan(&open) == nil && open > 0 {
		title := "Index verification found a mismatch"
		if open > 1 {
			title = "Index verification found " + formatCount(open) + " mismatches"
		}
		items = append(items, attentionItem{ID: "integrity", Rule: 2, Severity: "error", Title: title, Detail: "Some indexed conversations differ from their retained inputs.", Action: reviewIntegrity})
	}

	// 3 and 4. The latest finished index failed, or was interrupted, without
	// a user's stop; neither counts while an index or capture runs.
	if !s.attentionRunning() {
		s.runsMu.RLock()
		var latest *SyncRun
		for _, run := range s.runs {
			if run.State != "running" {
				latest = run
				break
			}
		}
		var item *attentionItem
		if latest != nil && !latest.StopRequested {
			switch latest.State {
			case "failed":
				detail, relevant := "", false
				for _, result := range latest.Results {
					if result.Error != nil && enabledSource(result.Source) {
						relevant = true
						detail = joinDetail(detail, result.Source+": "+defaultString(result.Error, "Indexing failed."))
					}
				}
				if !relevant && relevantSources(latest.Sources) {
					relevant, detail = true, defaultString(latest.Error, "")
				}
				if relevant {
					item = &attentionItem{ID: "index-failed", Rule: 3, Severity: "error", Title: "The last index failed", Detail: detail, Action: openCaptureAndIndex}
				}
			case "interrupted":
				item = &attentionItem{ID: "index-interrupted", Rule: 4, Severity: "warning", Title: "The last index was interrupted",
					Detail: "What it wrote is kept; the next index resumes it.", Action: openCaptureAndIndex}
			}
		}
		s.runsMu.RUnlock()
		// The journal outlives the process: after a restart or a crash,
		// s.runs is empty, and the journal's latest finished index (made
		// interrupted by the reconcile pass if its process died) still
		// says what happened. It wins when it is newer than this
		// process's latest run, and is not that same run.
		if row, ok := s.Catalog.latestIndexRow(ctx); ok && (latest == nil || row.Legacy != latest.ID && endedAfter(row.EndedAt, firstString(latest.CompletedAt))) {
			item = nil
			var detail struct {
				Sources []string `json:"sources"`
			}
			_ = json.Unmarshal(row.Detail, &detail)
			switch {
			case row.State == opFailed && relevantSources(detail.Sources):
				item = &attentionItem{ID: "index-failed", Rule: 3, Severity: "error", Title: "The last index failed", Detail: row.Error, Action: openCaptureAndIndex}
			// A release (eject, quit, switch) is the user's doing; a crash or
			// a dead process (owner-exited) is not.
			case row.State == opInterrupted && row.Reason != "released":
				item = &attentionItem{ID: "index-interrupted", Rule: 4, Severity: "warning", Title: "The last index was interrupted",
					Detail: "What it wrote is kept; the next index resumes it.", Action: openCaptureAndIndex}
			}
		}
		if item != nil {
			items = append(items, *item)
		}
	}

	// 5. The latest capture failed for an enabled source, or as a whole.
	if runs := s.captures.list(); len(runs) > 0 && runs[0].State == "failed" {
		run := runs[0]
		sourceErrors, sourceFailed, relevantFailure := map[string]bool{}, false, false
		for _, result := range run.Results {
			if result.State == "failed" || len(result.Errors) > 0 {
				sourceFailed = true
				relevantFailure = relevantFailure || enabledSource(result.Name)
			}
			for _, failure := range result.Errors {
				sourceErrors[failure.Error] = true
			}
		}
		generalFailure := !sourceFailed
		detail := ""
		for _, failure := range run.Errors {
			if !sourceErrors[failure.Error] {
				generalFailure = true
				detail = joinDetail(detail, failure.Error)
			}
		}
		if relevantFailure || generalFailure && relevantSources(run.Sources) {
			items = append(items, attentionItem{ID: "capture-failed", Rule: 5, Severity: "error", Title: "The last capture failed", Detail: detail, Action: openCaptureAndIndex})
		}
	}

	// 6. The latest local backup failed.
	if _, backups := s.backups.status(); len(backups) > 0 && backups[0].State == "failed" {
		detail := ""
		for _, failure := range backups[0].Errors {
			detail = joinDetail(detail, failure.Error)
			break
		}
		items = append(items, attentionItem{ID: "backup-failed", Rule: 6, Severity: "error", Title: "The last backup failed", Detail: detail, Action: openBackups})
	}

	// 7. A scheduled cloud backup failed, or the latest verification did.
	if schedules, err := s.Catalog.cloudSchedules(ctx); err == nil {
		for _, schedule := range schedules {
			if schedule.Enabled && schedule.Failures > 0 {
				items = append(items, attentionItem{ID: "cloud-schedule-failed:" + schedule.DestinationID, Rule: 7, Severity: "error",
					Title: "A scheduled cloud backup failed", Detail: schedule.LastError, Action: openBackups})
			}
		}
	}
	if runs, err := s.Catalog.cloudRuns(ctx); err == nil {
		seen := map[string]bool{}
		for _, run := range runs { // newest first
			if seen[run.DestinationID] || run.VerificationState != "complete" && run.VerificationState != "failed" {
				continue
			}
			seen[run.DestinationID] = true
			if run.VerificationState == "failed" {
				items = append(items, attentionItem{ID: "cloud-verify-failed:" + run.DestinationID, Rule: 7, Severity: "error",
					Title: "The last cloud backup verification failed", Detail: run.VerificationError, Action: openBackups})
			}
		}
	}
	return items
}

func joinDetail(detail, more string) string {
	if more == "" {
		return detail
	}
	if detail == "" {
		return more
	}
	return detail + " · " + more
}

// formatCount writes n with thousands separators.
func formatCount(n int) string {
	digits := strconv.Itoa(n)
	var text strings.Builder
	for index, digit := range digits {
		if index > 0 && (len(digits)-index)%3 == 0 {
			text.WriteByte(',')
		}
		text.WriteRune(digit)
	}
	return text.String()
}

// attentionIndexKinds are the runs rules 3 and 4 look at: the kinds of
// today's SyncRuns.
var attentionIndexKinds = []string{"index.changes.automatic", "index.changes.manual", "index.captures", "library.update", "library.rebuild", "index.verify", "index.sources.legacy"}

// latestIndexRow is this Mac's most recently ended top-level index in the
// journal.
func (c *Catalog) latestIndexRow(ctx context.Context) (journalRow, bool) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(attentionIndexKinds)), ",")
	args := []any{currentHost().ID}
	for _, kind := range attentionIndexKinds {
		args = append(args, kind)
	}
	rows, err := c.reader().QueryContext(ctx, "SELECT "+journalColumns+" FROM operation_journal WHERE host_id=? AND parent_id IS NULL AND ended_at IS NOT NULL AND kind IN ("+placeholders+") ORDER BY ended_at DESC LIMIT 1", args...)
	if err != nil {
		return journalRow{}, false
	}
	found, err := scanJournalRows(rows)
	if err != nil || len(found) == 0 {
		return journalRow{}, false
	}
	return found[0], true
}

// endedAfter reports whether time a is after time b; an unreadable b loses.
func endedAfter(a, b string) bool {
	left, ok := parseTime(a)
	right, known := parseTime(b)
	return ok && (!known || left.After(right))
}
