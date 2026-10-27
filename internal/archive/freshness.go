package archive

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// The freshness ledger (docs/background-work.md §5): one row per data
// domain, saying whether what the pages show is current, how far behind it
// is when that can be measured, why, and which operation will catch it up.
// It is one function over today's tables and generations; storage does not
// change. It runs on every status snapshot, so it reads only meta keys,
// small state tables and capped counts, never probes a lock, and takes the
// file-derived inputs (backups.json, capture manifests) from a cache the
// server refreshes at most once a minute.
//
// "unknown" is an honest answer: a source's conversations can only be known
// current as of the last check, and a count the tables do not hold is left
// out rather than guessed.

const (
	freshCurrent = "current"
	freshBehind  = "behind"
	freshUnknown = "unknown"
	// freshScheduled is waiting for its next planned run, within its
	// designed cadence: not stale, and not alarming (§5).
	freshScheduled = "scheduled"
)

// freshnessRow is one domain in the snapshot's shape.
type freshnessRow struct {
	Domain string `json:"domain"`
	// Group is the summary line's group: conversations, search, analyses,
	// findings, upgrade or backups.
	Group string `json:"group"`
	Label string `json:"label"`
	// AsOf is when the domain was last brought current, when that is
	// recorded.
	AsOf       *string          `json:"as_of"`
	Behind     freshnessBehind  `json:"behind"`
	StateLabel string           `json:"state_label"`
	Reason     *freshnessReason `json:"reason"`
	// CatchingUp is the queued or running operation that will bring it
	// current, through its kind's affects.
	CatchingUp      *string `json:"catching_up"`
	CatchingUpLabel *string `json:"catching_up_label"`
	// Next says what catches it up when nothing is running.
	Next   *string `json:"next"`
	NextAt *string `json:"next_at,omitempty"`
}

type freshnessBehind struct {
	State string  `json:"state"`
	Count *int64  `json:"count"`
	Unit  *string `json:"unit"`
}

type freshnessReason struct {
	Code string `json:"code"`
	Text string `json:"text"`
	// Detail is a short qualifier from the data, such as why captures need
	// indexing.
	Detail string `json:"detail,omitempty"`
}

// freshnessReasons are the reason codes' words (§5). Running codes come
// first in every domain's list: the work in progress explains the state
// better than an earlier attempt does.
var freshnessReasons = map[string]string{
	"indexing":             "Indexing now",
	"capture-running":      "Capturing now",
	"refreshing":           "Updating now",
	"building":             "Building now",
	"running":              "Updating now",
	"never-indexed":        "Not indexed yet",
	"source-error":         "The last index of this source failed",
	"capture-busy":         "A capture was using it at the last index",
	"needs-capture":        "Newer captured inputs are waiting to be indexed",
	"schedule-off":         "Scheduled indexing is off for it",
	"interrupted":          "The last index of this source was interrupted",
	"overdue":              "The scheduled index has not checked it recently",
	"not-indexed":          "Captured data is not indexed yet",
	"stopped":              "Stopped by you",
	"version":              "This version of Pharos rebuilds it",
	"deferred-by-schedule": "Scheduled indexing skips analyses",
	"failed":               "The last update failed",
	"never-built":          "Not built yet",
	"changed":              "Changed since it was last built",
	"daily-pass-due":       "Today's full pass has not run yet",
	"upgrade-available":    "An upgrade is available",
	"never":                "No backup yet",
	"retrying":             "The scheduled backup failed; it will try again",
	"unverified":           "The newest cloud backup is not verified yet",
	"paused":               "Its schedule is paused",
	"no-record":            "Not checked since this version of Pharos; updates with the next manual index",
	"blocked":              "Waits for an earlier step that failed",
	"no-schedule":          "Has no schedule, so Pharos cannot say it is current",
	"not-counted":          "Not counted since the library last changed",
	"unreadable":           "The capture folder cannot be read",
}

// freshnessReasonOverrides word a code for one kind of domain (the part of
// its ID before ":") or one group, where the shared words would mislead.
var freshnessReasonOverrides = map[string]string{
	"backups running":    "Backing up now",
	"backups failed":     "The last backup failed",
	"backups overdue":    "Older than its schedule",
	"backup.cloud never": "No verified cloud backup yet",
	"backup.local never": "No local backup yet",
	"findings changed":   "New data since today's full pass; the next one runs tomorrow",
}

// reasonText is a code's words for domain, in group.
func reasonText(domain, group, code string) string {
	kind, _, _ := strings.Cut(domain, ":")
	for _, key := range []string{kind + " " + code, group + " " + code} {
		if text := freshnessReasonOverrides[key]; text != "" {
			return text
		}
	}
	return freshnessReasons[code]
}

var freshnessStateLabels = map[string]string{freshCurrent: "Current", freshScheduled: "Scheduled", freshBehind: "Behind", freshUnknown: "Unknown"}

// scheduledShort is the Data row's muted words for a scheduled domain:
// "updates <when>".
var scheduledShort = map[string]string{"findings": "updates tomorrow"}

// pageGroups are the groups that feed pages, which the Data row covers.
// Backups are in the ledger, Show all and MCP, and reach the panel only
// through attention.
var pageGroups = map[string]bool{"conversations": true, "search": true, "analyses": true, "findings": true, "upgrade": true}

// What catches a domain up when nothing runs (§5 "next").
const (
	nextScheduledIndex = "updates with the next scheduled index"
	nextManualIndex    = "updates with the next manual index"
	nextOnItsOwn       = "carries on by itself"
	nextResume         = "carries on when you resume it or Pharos next opens the library"
	nextReadOrIndex    = "updates with the next manual index or Tools page"
	nextUpgrade        = "updates when you run the library upgrade"
	nextBackup         = "updates with your next backup"
	nextCloudSchedule  = "updates with the next scheduled cloud backup"
	nextCloudBackup    = "updates with your next cloud backup"
	nextFindingsPass   = "updates with Refresh findings now, or the first index tomorrow"
	nextCapturesRead   = "updates when the capture folder can be read"
)

// freshnessOp is a queued or running operation the ledger can name as
// catching a domain up.
type freshnessOp struct {
	ID, Kind, Subject, Label, State string
	Affects                         []string
	Started                         time.Time
}

// freshnessInput is what the ledger reads besides the catalog. The service
// fills all of it; the MCP process, which has no configuration, registry or
// backup history, leaves the optional parts nil and gets "unknown" there.
type freshnessInput struct {
	host string
	now  time.Time
	// sources are this Mac's enabled live sources; nil takes every source
	// the state tables know for this host.
	sources []string
	// ops are this Mac's queued and running operations.
	ops []freshnessOp
	// deferredToCapture are sources the latest index left to a running
	// capture (errCaptureBusy).
	deferredToCapture map[string]bool
	// viewStopped is the Search view's refresh, stopped by the user.
	viewStopped bool
	// files are the file-derived inputs; nil leaves captures out and the
	// local backup unknown.
	files *freshnessFiles
	// backupFailed is the latest local backup run of this process, when it
	// failed: when, and why.
	backupFailedAt string
}

// freshnessFiles are the inputs read from the library's files rather than
// the catalog: backups.json and the capture manifests.
type freshnessFiles struct {
	at time.Time
	// backupAt is the newest complete (not partial) local backup;
	// backupKnown is false where backups.json cannot be found (the MCP
	// process has no configuration).
	backupAt    string
	backupKnown bool
	// captures are capturedHosts' rows: one per host and captured source.
	// capturesUnreadable says the capture root could not be read: captures
	// are then the last rows read, if any, and every one is unknown.
	captures           []freshnessCapture
	capturesUnreadable bool
}

type freshnessCapture struct {
	host, hostLabel, source, indexedAt, reason string
	needsIndex                                 bool
}

// freshnessMetaKeys are the meta rows the ledger reads, in one query.
var freshnessMetaKeys = []string{"message_trigram_version", "text_search_documents_version", "text_search_documents_cursor", "tool_command_fts_version",
	"human_query_text_fts_ready", "tool_query_text_fts_ready", "tool_query_facts_ready", "workspace_library_version",
	"tool_ledger_generation", "tool_rollup_generation", "tool_rollup_day", "tool_rollup_timezone", "tool_rollup_full_revision", "tool_rollup_built_full_revision",
	"authorship_inputs_generation", "authorship_generation", "authorship_built_at", "findings_generation", "findings_built_at",
	"findings_inputs_generation", "analysis_inputs:identities", "analysis_inputs:git"}

// libraryVersionOnce is libraryVersion, which hashes the projection's
// definition, computed once.
var libraryVersionOnce = sync.OnceValue(libraryVersion)

// freshnessCap bounds a dirty-table count, so a large backlog costs no more
// than this many index entries.
const freshnessCap = 100_000

// freshness computes the ledger for host (this Mac). Each query is a meta
// lookup, a small state table, or a capped count; a query that fails makes
// its domains unknown rather than failing the snapshot.
func (c *Catalog) freshness(ctx context.Context, in freshnessInput) []freshnessRow {
	if in.now.IsZero() {
		in.now = time.Now()
	}
	if in.host == "" {
		in.host = currentHost().ID
	}
	l := ledger{in: in}
	meta, metaErr := c.freshnessMeta(ctx)
	deferred, deferredErr := c.freshnessDeferred(ctx)
	ends := c.latestAnalysisEnds(ctx, in.host)

	l.conversations(ctx, c)
	l.captures()
	l.view(ctx, c, meta, metaErr)
	l.search(ctx, c, meta, metaErr)
	l.analyses(ctx, c, meta, metaErr, deferred, deferredErr, ends)
	l.findings(c, meta, metaErr, deferred, deferredErr, ends)
	l.upgrade(c, meta, metaErr)
	l.backups(ctx, c)
	return l.rows
}

type ledger struct {
	in   freshnessInput
	rows []freshnessRow
}

// domainEntry is one row before the running operation is applied.
type domainEntry struct {
	domain, group, label string
	asOf                 string
	state                string
	count                int64
	countKnown           bool
	unit                 string
	reason, detail       string
	next, nextAt         string
	// running is the code used when an operation is catching it up.
	running string
}

// add finishes a row: an operation that affects the domain catches it up,
// and while one does, its running code is the reason (lists put running work
// first, §5).
func (l *ledger) add(entry domainEntry) {
	row := freshnessRow{Domain: entry.domain, Group: entry.group, Label: entry.label, AsOf: optional(entry.asOf),
		Behind: freshnessBehind{State: entry.state}}
	if entry.countKnown && entry.state != freshCurrent {
		count := entry.count
		row.Behind.Count = &count
		row.Behind.Unit = optional(entry.unit)
	}
	reason := entry.reason
	if entry.state != freshCurrent {
		if op := l.catchingUp(entry.domain); op != nil {
			row.CatchingUp, row.CatchingUpLabel = optional(op.ID), optional(op.Label)
			code := defaultString(entry.running, "running")
			if strings.HasPrefix(op.Kind, "capture.") {
				code = "capture-running"
			}
			reason = code
		}
	} else {
		reason = ""
	}
	if reason != "" {
		row.Reason = &freshnessReason{Code: reason, Text: reasonText(entry.domain, entry.group, reason), Detail: entry.detail}
	}
	if entry.state != freshCurrent && row.CatchingUp == nil {
		row.Next, row.NextAt = optional(entry.next), optional(entry.nextAt)
	}
	row.StateLabel = freshnessStateLabels[entry.state]
	l.rows = append(l.rows, row)
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// catchingUp is the most recently started queued or running operation whose
// kind affects domain: a chain stage before the index that started it.
func (l *ledger) catchingUp(domain string) *freshnessOp {
	var found *freshnessOp
	for index := range l.in.ops {
		op := &l.in.ops[index]
		if !slices.ContainsFunc(op.Affects, func(pattern string) bool { return affectsDomain(pattern, op.Subject, l.in.host, domain) }) {
			continue
		}
		if found == nil || op.Started.After(found.Started) {
			found = op
		}
	}
	return found
}

// affectsDomain matches one of a kind's affects (§3) against a domain:
// "conv:*" and "an.*" are prefixes, "this" is host, a "d" parameter is the
// operation's subject (any destination when it has none), "all" is every
// domain, and "all derived domains" every one but conversations, captures
// and backups. "(target) …" names another library's domains.
func affectsDomain(pattern, subject, host, domain string) bool {
	switch {
	case pattern == "all":
		return true
	case pattern == "all derived domains":
		return !strings.HasPrefix(domain, "conv:") && !strings.HasPrefix(domain, "capt:") && !strings.HasPrefix(domain, "backup.")
	case strings.HasPrefix(pattern, "("):
		return false
	}
	pattern = strings.Replace(pattern, "capt:this/", "capt:"+host+"/", 1)
	if strings.HasPrefix(pattern, "capt:h/") {
		pattern = "capt:*"
	}
	if name, ok := strings.CutSuffix(pattern, ":d"); ok {
		if subject == "" {
			return strings.HasPrefix(domain, name+":")
		}
		return domain == name+":"+subject
	}
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(domain, prefix)
	}
	return pattern == domain
}

func (c *Catalog) freshnessMeta(ctx context.Context) (map[string]string, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(freshnessMetaKeys)), ",")
	args := make([]any, len(freshnessMetaKeys))
	for index, key := range freshnessMetaKeys {
		args[index] = key
	}
	rows, err := c.reader().QueryContext(ctx, "SELECT key,value FROM meta WHERE key IN ("+placeholders+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		values[key] = value
	}
	return values, rows.Err()
}

type deferredRow struct {
	generation, completed  int64
	changedAt, completedAt string
}

func (c *Catalog) freshnessDeferred(ctx context.Context) (map[string]deferredRow, error) {
	rows, err := c.reader().QueryContext(ctx, "SELECT name,generation,completed_generation,changed_at,COALESCE(completed_at,'') FROM sync_deferred")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := map[string]deferredRow{}
	for rows.Next() {
		var name string
		var row deferredRow
		if err := rows.Scan(&name, &row.generation, &row.completed, &row.changedAt, &row.completedAt); err != nil {
			return nil, err
		}
		values[name] = row
	}
	return values, rows.Err()
}

// journalEnd is what the journal says of an analysis kind on this Mac: its
// latest ended row's state and end, and when it last succeeded.
type journalEnd struct{ state, ended, succeeded string }

// latestAnalysisEnds reads this host's newest analysis rows through the
// journal's (host_id, started_at) index.
func (c *Catalog) latestAnalysisEnds(ctx context.Context, host string) map[string]journalEnd {
	ends := map[string]journalEnd{}
	rows, err := c.reader().QueryContext(ctx, `SELECT kind,state,COALESCE(ended_at,'') FROM operation_journal WHERE host_id=? AND kind LIKE 'analysis.%' AND ended_at IS NOT NULL
		ORDER BY started_at DESC LIMIT 200`, host)
	if err != nil {
		return ends
	}
	defer rows.Close()
	for rows.Next() {
		var kind, state, ended string
		if rows.Scan(&kind, &state, &ended) != nil {
			continue
		}
		end, seen := ends[kind]
		if !seen {
			end.state, end.ended = state, ended
		}
		if state == opSucceeded && end.succeeded == "" {
			end.succeeded = ended
		}
		ends[kind] = end
	}
	return ends
}

// conversations is conv:<source> for this Mac's live sources.
func (l *ledger) conversations(ctx context.Context, c *Catalog) {
	type sourceState struct {
		success, automaticSuccess, automaticIndexed string
		attemptAt, attemptError                     string
	}
	states := map[string]*sourceState{}
	rows, err := c.reader().QueryContext(ctx, `WITH keys AS (SELECT source_name FROM source_states WHERE host_id=?1 UNION SELECT source_name FROM automatic_source_states WHERE host_id=?1)
		SELECT k.source_name,COALESCE(s.last_success_at,''),COALESCE(a.succeeded_at,''),COALESCE(a.indexed_at,''),
		COALESCE(s.last_attempt_at,''),COALESCE(s.error,''),COALESCE(a.attempted_at,''),COALESCE(a.error,'')
		FROM keys k LEFT JOIN source_states s ON s.host_id=?1 AND s.source_name=k.source_name
		LEFT JOIN automatic_source_states a ON a.host_id=?1 AND a.source_name=k.source_name`, l.in.host)
	known := err == nil
	if err == nil {
		for rows.Next() {
			var name, manualAttempt, manualError, automaticAttempt, automaticError string
			state := &sourceState{}
			if rows.Scan(&name, &state.success, &state.automaticSuccess, &state.automaticIndexed, &manualAttempt, &manualError, &automaticAttempt, &automaticError) != nil {
				known = false
				break
			}
			// The latest attempt wins, as attention rule 1 reads it.
			state.attemptAt, state.attemptError = manualAttempt, manualError
			if canonicalTime(automaticAttempt) > canonicalTime(manualAttempt) {
				state.attemptAt, state.attemptError = automaticAttempt, automaticError
			}
			states[name] = state
		}
		rows.Close()
	}
	pending := map[string]bool{}
	if rows, err := c.reader().QueryContext(ctx, "SELECT source_name FROM source_input_states WHERE host_id=? AND pending<>0", l.in.host); err == nil {
		for rows.Next() {
			var name string
			if rows.Scan(&name) == nil {
				pending[name] = true
			}
		}
		rows.Close()
	}
	settings, settingsErr := c.syncSettings(ctx)
	sources := l.in.sources
	if sources == nil {
		for name := range states {
			sources = append(sources, name)
		}
	}
	sort.Strings(sources)
	interval := time.Duration(max(settings.Interval, 60)) * time.Second
	for _, name := range sources {
		entry := domainEntry{domain: "conv:" + name, group: "conversations", label: "Conversations (" + name + ")", running: "indexing"}
		scheduled := settingsErr == nil && settings.Enabled && (len(settings.Sources) == 0 || slices.Contains(settings.Sources, name))
		entry.next = nextManualIndex
		if scheduled {
			entry.next = nextScheduledIndex
		}
		state := states[name]
		if state != nil {
			entry.asOf = latestTime(state.success, state.automaticSuccess, state.automaticIndexed)
		}
		asOf, indexed := parseTime(entry.asOf)
		switch {
		case !known || settingsErr != nil:
			entry.state = freshUnknown
		case !indexed:
			entry.state, entry.reason = freshBehind, "never-indexed"
		case strings.HasPrefix(state.attemptError, "interrupted:"):
			entry.state, entry.reason = freshUnknown, "interrupted"
		case state.attemptError != "":
			entry.state, entry.reason = freshUnknown, "source-error"
		case l.in.deferredToCapture[name]:
			entry.state, entry.reason = freshUnknown, "capture-busy"
		case pending[name]:
			entry.state, entry.reason = freshBehind, "needs-capture"
		case !scheduled:
			// Only the last index's answer is known; whether the source has
			// changed since is not.
			entry.state, entry.reason = freshUnknown, "schedule-off"
		case l.in.now.Sub(asOf) > max(2*interval, interval+15*time.Minute):
			entry.state, entry.reason = freshUnknown, "overdue"
		default:
			entry.state = freshCurrent
		}
		l.add(entry)
	}
}

// latestTime is the latest of several stored times, or "".
func latestTime(values ...string) string {
	latest := ""
	for _, value := range values {
		if value != "" && canonicalTime(value) > canonicalTime(latest) {
			latest = value
		}
	}
	return latest
}

// captures is capt:<host>/<source>: captured data waiting to be indexed,
// from the cached capturedHosts.
func (l *ledger) captures() {
	if l.in.files == nil {
		return
	}
	if l.in.files.capturesUnreadable && len(l.in.files.captures) == 0 {
		l.add(domainEntry{domain: "capt:*", group: "conversations", label: "Captures", state: freshUnknown, reason: "unreadable", next: nextCapturesRead})
		return
	}
	for _, capture := range l.in.files.captures {
		entry := domainEntry{domain: "capt:" + capture.host + "/" + capture.source, group: "conversations", asOf: capture.indexedAt,
			label: "Captures (" + capture.source + " on " + defaultString(capture.hostLabel, capture.host) + ")", running: "indexing", next: nextManualIndex, state: freshCurrent}
		switch {
		case l.in.files.capturesUnreadable:
			// The last rows read, which cannot be checked now.
			entry.state, entry.reason, entry.next = freshUnknown, "unreadable", nextCapturesRead
		case capture.needsIndex:
			entry.state, entry.reason, entry.detail = freshBehind, "not-indexed", capture.reason
		}
		l.add(entry)
	}
}

// view is the Search view: workspaces waiting in workspace_library_dirty.
func (l *ledger) view(ctx context.Context, c *Catalog, meta map[string]string, metaErr error) {
	entry := domainEntry{domain: "view", group: "search", label: "Search view", running: "refreshing", unit: "workspace", next: nextOnItsOwn}
	var dirty int64
	if err := c.reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM (SELECT 1 FROM workspace_library_dirty LIMIT ?)", freshnessCap).Scan(&dirty); err != nil {
		entry.state = freshUnknown
		l.add(entry)
		return
	}
	entry.count, entry.countKnown = dirty, true
	switch {
	case metaErr == nil && meta["workspace_library_version"] != libraryVersionOnce():
		// Every workspace is rebuilt; the dirty set does not count them.
		entry.state, entry.reason, entry.countKnown = freshBehind, "version", false
	case dirty == 0:
		entry.state = freshCurrent
	case l.in.viewStopped:
		entry.state, entry.reason, entry.next = freshBehind, "stopped", nextResume
	default:
		entry.state, entry.reason = freshBehind, "refreshing"
	}
	l.add(entry)
}

// search is the four index backfills, each with a done flag in meta.
func (l *ledger) search(ctx context.Context, c *Catalog, meta map[string]string, metaErr error) {
	add := func(domain, label string, ready bool, count int64, countKnown bool, unit string) {
		entry := domainEntry{domain: domain, group: "search", label: label, running: "building", next: nextOnItsOwn, unit: unit, count: count, countKnown: countKnown}
		switch {
		case metaErr != nil:
			entry.state = freshUnknown
		case ready:
			entry.state = freshCurrent
		default:
			entry.state, entry.reason = freshBehind, "version"
		}
		l.add(entry)
	}
	substringReady := meta["message_trigram_version"] != ""
	var remaining int64
	known := false
	if metaErr == nil && !substringReady {
		// Only while it builds: two counts over conversations' rowids.
		if progress, err := c.substringIndexProgress(ctx); err == nil {
			remaining, known = max(0, int64(progress.Total-progress.Done)), true
		}
	}
	add("search.substring", "Substring search", substringReady, remaining, known, "conversation")
	add("search.text", "Text search", meta["text_search_documents_version"] == "1", 0, false, "")
	add("search.tools", "Tool search", meta["tool_command_fts_version"] == "1", 0, false, "")
	add("search.query", "Table search", meta["human_query_text_fts_ready"] == humanQueryText.version() && meta["tool_query_text_fts_ready"] == toolQueryText.version() &&
		meta["tool_query_facts_ready"] == toolQueryFacts.version(), 0, false, "")
}

// libraryInputs is the catalog's input generation: the tool ledger's
// generation and the record writer's authorship inputs, which every write of
// conversations moves. A derived domain is current only when its recorded
// completion was for this value (or its own generation check says so).
func libraryInputs(meta map[string]string) string {
	return meta["tool_ledger_generation"] + ":" + meta["authorship_inputs_generation"]
}

// chainStages are the analysis chain's stages in the order it runs them; it
// stops at the first that fails.
var chainStages = []struct{ kind, domain, label string }{
	{"analysis.github", "", "GitHub check"}, {"analysis.identities", "an.identities", "Identity links"}, {"analysis.git", "an.git", "Merges from Git"},
	{"analysis.tools", "an.tools", "Tools rollup"}, {"analysis.authorship", "an.authorship", "Writing classification"}, {"analysis.findings", "findings", "Findings"}}

// chainFailure applies failure precedence (§5) to a domain that is not
// current: its own stage failed after its last completion (and after the
// deferral, when scheduled indexing deferred it) makes it failed; an earlier
// stage's failure makes it blocked, since the chain stopped there.
func chainFailure(entry *domainEntry, ends map[string]journalEnd, deferral string) bool {
	relevant := func(ended string) bool {
		return ended != "" && canonicalTime(ended) > canonicalTime(entry.asOf) && (deferral == "" || canonicalTime(ended) > canonicalTime(deferral))
	}
	for _, stage := range chainStages {
		end := ends[stage.kind]
		if end.state == opFailed && relevant(end.ended) {
			if stage.domain == entry.domain {
				entry.state, entry.reason, entry.next = freshBehind, "failed", nextManualIndex
			} else {
				entry.state, entry.reason, entry.detail, entry.next = freshBehind, "blocked", stage.label, nextManualIndex
			}
			return true
		}
		if stage.domain == entry.domain {
			return false
		}
	}
	return false
}

// analyses is the library-wide analyses the chain after a manual index
// brings current, and scheduled indexing leaves behind (sync_deferred).
func (l *ledger) analyses(ctx context.Context, c *Catalog, meta map[string]string, metaErr error, deferred map[string]deferredRow, deferredErr error, ends map[string]journalEnd) {
	inputs := libraryInputs(meta)
	// Scheduled indexing deferred it: behind since its last completion. The
	// generations count written records, not runs, so no count is given.
	pendingDeferral := func(name string) (string, bool) {
		row, ok := deferred[name]
		return row.changedAt, ok && row.generation > row.completed
	}
	deferral := func(entry *domainEntry) {
		entry.state, entry.reason, entry.next = freshBehind, "deferred-by-schedule", nextManualIndex
	}
	for _, stage := range chainStages[1:3] {
		name := strings.TrimPrefix(stage.kind, "analysis.")
		entry := domainEntry{domain: stage.domain, group: "analyses", label: stage.label, state: freshCurrent,
			asOf: latestTime(deferred[name].completedAt, ends[stage.kind].succeeded)}
		changedAt, isDeferred := pendingDeferral(name)
		recorded := meta["analysis_inputs:"+name]
		switch {
		case deferredErr != nil || metaErr != nil:
			entry.state = freshUnknown
		case !isDeferred && recorded == inputs:
		case chainFailure(&entry, ends, changedAt):
		case isDeferred:
			deferral(&entry)
		case recorded == "":
			entry.state, entry.reason, entry.next = freshUnknown, "no-record", nextManualIndex
		default:
			entry.state, entry.reason, entry.next = freshBehind, "changed", nextManualIndex
		}
		l.add(entry)
	}

	// The Tools rollup: built for the ledger's generation, this layout,
	// today and this timezone, with no dirty workspaces.
	tools := domainEntry{domain: "an.tools", group: "analyses", label: "Tools rollup", state: freshCurrent,
		asOf: latestTime(deferred["tools"].completedAt, ends["analysis.tools"].succeeded)}
	var dirty int64
	dirtyErr := c.reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM (SELECT 1 FROM tool_rollup_dirty LIMIT ?)", freshnessCap).Scan(&dirty)
	built := meta["tool_rollup_generation"]
	changedAt, isDeferred := pendingDeferral("tools")
	layout := strings.HasSuffix(built, "/"+toolRollupVersion)
	stale := built != meta["tool_ledger_generation"]+"/"+toolRollupVersion || dirty > 0 || meta["tool_rollup_day"] != c.clock().Format("2006-01-02") ||
		meta["tool_rollup_timezone"] != toolRollupTimezone() || meta["tool_rollup_full_revision"] != meta["tool_rollup_built_full_revision"]
	switch {
	case metaErr != nil || deferredErr != nil || dirtyErr != nil:
		tools.state = freshUnknown
	case !stale && !isDeferred:
	case chainFailure(&tools, ends, changedAt):
	case isDeferred:
		deferral(&tools)
	case built == "":
		tools.state, tools.reason, tools.next = freshBehind, "never-built", nextReadOrIndex
	case !layout:
		tools.state, tools.reason, tools.next = freshBehind, "version", nextReadOrIndex
	default:
		tools.state, tools.reason, tools.next = freshBehind, "changed", nextReadOrIndex
		if dirty > 0 {
			tools.count, tools.countKnown, tools.unit = dirty, true, "workspace"
		}
	}
	l.add(tools)

	// Writing classification: built for the ledger and record generations.
	authorship := domainEntry{domain: "an.authorship", group: "analyses", label: "Writing classification", asOf: meta["authorship_built_at"], state: freshCurrent}
	want := inputs + "/" + authorshipVersion
	changedAt, isDeferred = pendingDeferral("authorship")
	switch {
	case metaErr != nil || deferredErr != nil:
		authorship.state = freshUnknown
	case meta["authorship_generation"] == want && !isDeferred:
	case chainFailure(&authorship, ends, changedAt):
	case isDeferred:
		deferral(&authorship)
	case meta["authorship_generation"] == "":
		authorship.state, authorship.reason, authorship.next = freshBehind, "never-built", nextManualIndex
	default:
		authorship.state, authorship.reason, authorship.next = freshBehind, "changed", nextManualIndex
	}
	l.add(authorship)

	// The tool ledger: conversations whose ledger is from an older version.
	// Counting them reads every conversation, so the ledger takes the count
	// from the last time anything asked for the upgrade's steps, and only
	// while the library has not changed since.
	ledgerEntry := domainEntry{domain: "an.ledger", group: "analyses", label: "Tools ledger", unit: "conversation", next: nextUpgrade}
	if steps, ok := l.countedSteps(c, meta, metaErr, &ledgerEntry); ok {
		pending := int64(0)
		for _, step := range steps {
			if step.ID == "tools" {
				pending = step.Pending
			}
		}
		ledgerEntry.state, ledgerEntry.count, ledgerEntry.countKnown = freshCurrent, pending, true
		if pending > 0 {
			ledgerEntry.state, ledgerEntry.reason = freshBehind, "version"
		}
	}
	l.add(ledgerEntry)
}

// countedSteps is the upgrade's last count when it was taken for the
// library's current inputs; otherwise it makes entry unknown.
func (l *ledger) countedSteps(c *Catalog, meta map[string]string, metaErr error, entry *domainEntry) ([]upgradeStep, bool) {
	steps, at, counted, ok := c.upgrade().cachedSteps()
	entry.asOf = ""
	if ok {
		entry.asOf = formatTime(at)
	}
	switch {
	case metaErr != nil:
		entry.state = freshUnknown
	case !ok:
		entry.state, entry.reason = freshUnknown, "not-counted"
	case counted != libraryInputs(meta):
		entry.state, entry.reason = freshUnknown, "not-counted"
	default:
		return steps, true
	}
	return nil, false
}

// findings is the findings' daily full pass, for the library's inputs.
func (l *ledger) findings(c *Catalog, meta map[string]string, metaErr error, deferred map[string]deferredRow, deferredErr error, ends map[string]journalEnd) {
	entry := domainEntry{domain: "findings", group: "findings", label: "Findings", asOf: meta["findings_built_at"], state: freshCurrent, next: nextManualIndex}
	row := deferred["findings"]
	isDeferred := row.generation > row.completed
	today := meta["findings_generation"] == c.findingsGeneration()
	switch {
	case metaErr != nil || deferredErr != nil:
		entry.state = freshUnknown
	case today && !isDeferred && meta["findings_inputs_generation"] == libraryInputs(meta):
	case meta["findings_generation"] == "":
		entry.state, entry.reason = freshBehind, "never-built"
	case chainFailure(&entry, ends, conditionalText(isDeferred, row.changedAt)):
	case isDeferred:
		entry.state, entry.reason = freshBehind, "deferred-by-schedule"
	case !today:
		entry.state, entry.reason = freshBehind, "daily-pass-due"
	case meta["findings_inputs_generation"] == "":
		entry.state, entry.reason = freshUnknown, "no-record"
	default:
		// The full pass runs once a day: later data waits for tomorrow's
		// (or Refresh findings now). Within today that is its designed
		// cadence; tomorrow, without a pass, it is daily-pass-due.
		entry.state, entry.reason, entry.next = freshScheduled, "changed", nextFindingsPass
	}
	l.add(entry)
}

func conditionalText(condition bool, value string) string {
	if condition {
		return value
	}
	return ""
}

// upgrade is the library upgrade's pending steps, from the last count taken
// for the current inputs (counting them scans the catalog).
func (l *ledger) upgrade(c *Catalog, meta map[string]string, metaErr error) {
	entry := domainEntry{domain: "upgrade", group: "upgrade", label: "Library upgrade", unit: "step", next: nextUpgrade}
	steps, ok := l.countedSteps(c, meta, metaErr, &entry)
	if !ok {
		l.add(entry)
		return
	}
	pending := int64(0)
	for _, step := range steps {
		if step.Pending > 0 {
			pending++
		}
	}
	entry.state, entry.count, entry.countKnown = freshCurrent, pending, true
	if pending > 0 {
		entry.state, entry.reason = freshBehind, "upgrade-available"
	}
	l.add(entry)
}

// backups is the local backup and each cloud destination. Only a schedule
// says what current means: without one, the newest backup is reported with
// its age as unknown.
func (l *ledger) backups(ctx context.Context, c *Catalog) {
	local := domainEntry{domain: "backup.local", group: "backups", label: "Local backup", next: nextBackup}
	switch {
	case l.in.files == nil || !l.in.files.backupKnown:
		local.state = freshUnknown
	case l.in.backupFailedAt != "" && canonicalTime(l.in.backupFailedAt) > canonicalTime(l.in.files.backupAt):
		local.asOf, local.state, local.reason = l.in.files.backupAt, freshBehind, "failed"
	case l.in.files.backupAt == "":
		local.state, local.reason = freshUnknown, "never"
	default:
		// Local backups have no schedule.
		local.asOf, local.state, local.reason = l.in.files.backupAt, freshUnknown, "no-schedule"
	}
	l.add(local)

	destinations, err := c.freshnessCloudDestinations(ctx)
	if err != nil || len(destinations) == 0 {
		return
	}
	schedules, schedulesErr := c.cloudSchedules(ctx)
	for _, destination := range destinations {
		entry := domainEntry{domain: "backup.cloud:" + destination.id, group: "backups", label: "Cloud backup (" + destination.name + ")", next: nextCloudBackup}
		latest, verified, runsErr := c.freshnessCloudRuns(ctx, destination.id)
		if runsErr != nil || schedulesErr != nil {
			entry.state = freshUnknown
			l.add(entry)
			continue
		}
		var schedule *cloudSchedule
		for index := range schedules {
			if schedules[index].DestinationID == destination.id {
				schedule = &schedules[index]
			}
		}
		if verified != nil {
			entry.asOf = defaultString(verified.UpdatedAt, verified.StartedAt)
		}
		enabled := schedule != nil && schedule.Enabled
		if enabled {
			entry.next, entry.nextAt = nextCloudSchedule, schedule.NextRunAt
		}
		asOf, ok := parseTime(entry.asOf)
		overdue := enabled && ok && l.in.now.Sub(asOf) > time.Duration(max(schedule.Cadence, 1))*24*time.Hour+12*time.Hour
		switch {
		case enabled && schedule.Failures > 0:
			entry.state, entry.reason = freshBehind, "retrying"
		case latest != nil && latest.State == "failed" && (verified == nil || canonicalTime(latest.StartedAt) > canonicalTime(verified.StartedAt)):
			entry.state, entry.reason = freshBehind, "failed"
		case verified == nil && latest != nil && latest.State == "complete":
			entry.state, entry.reason = freshBehind, "unverified"
		case verified == nil:
			entry.state, entry.reason = freshBehind, "never"
		case overdue:
			entry.state, entry.reason = freshBehind, "overdue"
		case latest != nil && latest.ID != verified.ID && latest.State == "complete" && !latest.Verified:
			entry.state, entry.reason = freshBehind, "unverified"
		case !enabled:
			entry.state, entry.reason = freshUnknown, "no-schedule"
		default:
			entry.state = freshCurrent
		}
		if entry.state != freshCurrent && schedule != nil && !schedule.Enabled && entry.reason != "failed" {
			entry.reason = "paused"
		}
		l.add(entry)
	}
}

// freshnessCloudRuns is one destination's latest finished full run and its
// latest complete, verified one, through cloud_runs_destination.
func (c *Catalog) freshnessCloudRuns(ctx context.Context, destination string) (latest, verified *cloudRun, err error) {
	scan := func(query string) ([]cloudRun, error) {
		rows, err := c.reader().QueryContext(ctx, query, destination)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []cloudRun{}
		for rows.Next() {
			var raw string
			var run cloudRun
			if err := rows.Scan(&raw); err != nil {
				return nil, err
			}
			if json.Unmarshal([]byte(raw), &run) == nil {
				out = append(out, run)
			}
		}
		return out, rows.Err()
	}
	recent, err := scan(`SELECT status_json FROM cloud_backup_runs WHERE destination_id=? AND json_extract(status_json,'$.kind')<>'sample'
		AND json_extract(status_json,'$.state') NOT IN ('running','pending') ORDER BY created_at DESC LIMIT 1`)
	if err != nil {
		return nil, nil, err
	}
	good, err := scan(`SELECT status_json FROM cloud_backup_runs WHERE destination_id=? AND json_extract(status_json,'$.kind')<>'sample'
		AND json_extract(status_json,'$.state')='complete' AND json_extract(status_json,'$.verified')=1 ORDER BY created_at DESC LIMIT 1`)
	if err != nil {
		return nil, nil, err
	}
	if len(recent) > 0 {
		latest = &recent[0]
	}
	if len(good) > 0 {
		verified = &good[0]
	}
	return latest, verified, nil
}

type freshnessDestination struct{ id, name string }

// freshnessCloudDestinations lists the cloud destinations by id and a name
// from their configuration.
func (c *Catalog) freshnessCloudDestinations(ctx context.Context) ([]freshnessDestination, error) {
	rows, err := c.reader().QueryContext(ctx, "SELECT id,config_json FROM cloud_destinations ORDER BY imported_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []freshnessDestination{}
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var config cloudConfig
		_ = json.Unmarshal([]byte(raw), &config)
		out = append(out, freshnessDestination{id: id, name: firstString(config.Name, config.Bucket, config.Container, config.Provider, id)})
	}
	return out, rows.Err()
}

// freshnessGroups are the summary's groups, in order, with their words.
var freshnessGroups = []struct{ id, label string }{
	{"conversations", "Conversations"}, {"analyses", "Analyses"}, {"findings", "Findings"}, {"search", "Search"}, {"upgrade", "Upgrade"}, {"backups", "Backups"}}

// Short states (§5): the Data row's words for a group that is not current.
const (
	shortUpdating = "updating"
	shortBehind   = "behind"
	shortManual   = "waits for manual index"
	shortUnknown  = "unknown"
	shortFailed   = "failed"
)

// freshnessGroup is one group of the summary (§5, the panel's Data row):
// the worst of its rows, with server words.
type freshnessGroup struct {
	Group string `json:"group"`
	Label string `json:"label"`
	State string `json:"state"`
	// Short is the Data row's word for it: updating, behind (with its age
	// when ShortAge), waits for manual index, unknown or failed.
	Short    string `json:"short"`
	ShortAge bool   `json:"short_age"`
	// Text is the state with what catches it up, for tooltips: "behind,
	// updates with the next manual index", "catching up".
	Text string `json:"text"`
	// AsOf is the least recent as_of among the rows in the group's state;
	// AsOfLabel is what a surface says before its age.
	AsOf      *string `json:"as_of"`
	AsOfLabel string  `json:"as_of_label"`
	// Tooltip lists the rows that are not current and why.
	Tooltip    string  `json:"tooltip,omitempty"`
	CatchingUp *string `json:"catching_up"`
	// Shown is whether the Data row lists it: page groups that are behind,
	// unknown or failed. Muted is a page group that is only scheduled, which
	// the row may show muted after "All current".
	Shown bool `json:"shown"`
	Muted bool `json:"muted"`
}

// freshnessSummary is the Data row: the groups, and the words for when
// every group is current.
type freshnessSummary struct {
	Groups     []freshnessGroup `json:"groups"`
	AllCurrent bool             `json:"all_current"`
	// Text ("All current") and CheckedLabel ("checked") with CheckedAt, the
	// least recent check of this Mac's conversations, when all are current.
	Text         string  `json:"text,omitempty"`
	CheckedLabel string  `json:"checked_label,omitempty"`
	CheckedAt    *string `json:"checked_at"`
}

// summarizeFreshness groups rows for the Data row.
func summarizeFreshness(rows []freshnessRow) freshnessSummary {
	rank := map[string]int{freshCurrent: 0, freshScheduled: 1, freshUnknown: 2, freshBehind: 3}
	summary := freshnessSummary{Groups: []freshnessGroup{}, AllCurrent: true}
	for _, definition := range freshnessGroups {
		members := slices.DeleteFunc(slices.Clone(rows), func(row freshnessRow) bool { return row.Group != definition.id })
		if len(members) == 0 {
			continue
		}
		group := freshnessGroup{Group: definition.id, Label: definition.label, State: freshCurrent}
		for _, row := range members {
			if rank[row.Behind.State] > rank[group.State] {
				group.State = row.Behind.State
			}
		}
		tooltip := []string{}
		failed, allCaught, manualOnly := false, true, true
		next := ""
		for _, row := range members {
			if row.Behind.State == freshCurrent {
				if definition.id == "conversations" && row.AsOf != nil && (summary.CheckedAt == nil || canonicalTime(*row.AsOf) < canonicalTime(*summary.CheckedAt)) {
					summary.CheckedAt = row.AsOf
				}
				continue
			}
			if row.Behind.State == freshScheduled && group.State != freshScheduled {
				// Planned, not stale: only a tooltip line.
				tooltip = append(tooltip, row.Label+": "+lowerFirst(row.StateLabel))
				continue
			}
			line := row.Label + ": " + lowerFirst(row.StateLabel)
			if row.Reason != nil {
				line += ", " + lowerFirst(row.Reason.Text)
				if row.Reason.Detail != "" {
					line += " (" + row.Reason.Detail + ")"
				}
				failed = failed || slices.Contains([]string{"failed", "blocked", "retrying"}, row.Reason.Code)
			}
			if row.Next != nil {
				line += "; " + *row.Next
			}
			tooltip = append(tooltip, line)
			if row.CatchingUp == nil {
				allCaught = false
			}
			if row.Behind.State != group.State {
				continue
			}
			if row.AsOf != nil && (group.AsOf == nil || canonicalTime(*row.AsOf) < canonicalTime(*group.AsOf)) {
				group.AsOf = row.AsOf
			}
			if row.CatchingUp != nil && group.CatchingUp == nil {
				group.CatchingUp = row.CatchingUp
			}
			if row.CatchingUp == nil {
				if next == "" && row.Next != nil {
					next = *row.Next
				}
				if row.Next == nil || *row.Next != nextManualIndex {
					manualOnly = false
				}
			}
		}
		group.Tooltip = strings.Join(tooltip, "\n")
		switch {
		case group.State == freshCurrent:
			group.Text = "current"
		case allCaught:
			group.Text = "catching up"
		case next != "":
			group.Text = group.State + ", " + next
		default:
			group.Text = group.State
		}
		switch {
		case group.State == freshCurrent:
		case group.State == freshScheduled:
			group.Short = defaultString(scheduledShort[members[0].Domain], "updates later")
			for _, row := range members {
				if short := scheduledShort[row.Domain]; short != "" && row.Behind.State == freshScheduled {
					group.Short = short
				}
			}
		case failed:
			group.Short = shortFailed
		case allCaught:
			group.Short = shortUpdating
		case group.State == freshBehind && manualOnly:
			group.Short = shortManual
		case group.State == freshBehind:
			group.Short, group.ShortAge = shortBehind, group.AsOf != nil
		default:
			group.Short = shortUnknown
		}
		if group.AsOf != nil && group.State != freshCurrent {
			group.AsOfLabel = "last current"
		}
		// The Data row: page groups that are stale; a scheduled one is
		// muted and never keeps the row from "All current".
		group.Shown = pageGroups[definition.id] && group.State != freshCurrent && group.State != freshScheduled
		group.Muted = pageGroups[definition.id] && group.State == freshScheduled
		summary.AllCurrent = summary.AllCurrent && !group.Shown
		summary.Groups = append(summary.Groups, group)
	}
	if summary.AllCurrent {
		summary.Text, summary.CheckedLabel = "All current", "checked"
	} else {
		summary.CheckedAt = nil
	}
	return summary
}

// cachedSteps is UpgradeSteps' latest answer for the ledger: the steps,
// when they were counted, and the library's inputs then.
func (state *upgradeState) cachedSteps() ([]upgradeStep, time.Time, string, bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	return slices.Clone(state.lastSteps), state.lastStepsAt, state.lastStepsInputs, !state.lastStepsAt.IsZero()
}
