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
}

// freshnessReasonOverrides word a code for one kind of domain (the part of
// its ID before ":") or one group, where the shared words would mislead.
var freshnessReasonOverrides = map[string]string{
	"backups running":    "Backing up now",
	"backups failed":     "The last backup failed",
	"backups overdue":    "Older than its schedule",
	"backup.cloud never": "No verified cloud backup yet",
	"backup.local never": "No local backup yet",
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

var freshnessStateLabels = map[string]string{freshCurrent: "Current", freshBehind: "Behind", freshUnknown: "Unknown"}

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
	captures []freshnessCapture
}

type freshnessCapture struct {
	host, hostLabel, source, indexedAt, reason string
	needsIndex                                 bool
}

// freshnessMetaKeys are the meta rows the ledger reads, in one query.
var freshnessMetaKeys = []string{"message_trigram_version", "text_search_documents_version", "text_search_documents_cursor", "tool_command_fts_version",
	"human_query_text_fts_ready", "tool_query_text_fts_ready", "tool_query_facts_ready", "workspace_library_version",
	"tool_ledger_generation", "tool_rollup_generation", "tool_rollup_day", "tool_rollup_timezone", "tool_rollup_full_revision", "tool_rollup_built_full_revision",
	"authorship_inputs_generation", "authorship_generation", "authorship_built_at", "findings_generation", "findings_built_at"}

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
	failures := c.latestAnalysisFailures(ctx, in.host)

	l.conversations(ctx, c)
	l.captures()
	l.view(ctx, c, meta, metaErr)
	l.search(ctx, c, meta, metaErr)
	l.analyses(ctx, c, meta, metaErr, deferred, deferredErr, failures)
	l.findings(c, meta, metaErr, deferred, deferredErr)
	l.upgrade(c)
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
	generation, completed int64
	completedAt           string
}

func (c *Catalog) freshnessDeferred(ctx context.Context) (map[string]deferredRow, error) {
	rows, err := c.reader().QueryContext(ctx, "SELECT name,generation,completed_generation,COALESCE(completed_at,'') FROM sync_deferred")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := map[string]deferredRow{}
	for rows.Next() {
		var name string
		var row deferredRow
		if err := rows.Scan(&name, &row.generation, &row.completed, &row.completedAt); err != nil {
			return nil, err
		}
		values[name] = row
	}
	return values, rows.Err()
}

// latestAnalysisFailures are the analysis kinds whose latest ended journal
// row on this Mac failed, with when it ended. It reads this host's newest
// rows through the journal's (host_id, started_at) index.
func (c *Catalog) latestAnalysisFailures(ctx context.Context, host string) map[string]string {
	failed := map[string]string{}
	rows, err := c.reader().QueryContext(ctx, `SELECT kind,state,COALESCE(ended_at,'') FROM operation_journal WHERE host_id=? AND kind LIKE 'analysis.%' AND ended_at IS NOT NULL
		ORDER BY started_at DESC LIMIT 200`, host)
	if err != nil {
		return failed
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var kind, state, ended string
		if rows.Scan(&kind, &state, &ended) != nil || seen[kind] {
			continue
		}
		seen[kind] = true
		if state == opFailed {
			failed[kind] = ended
		}
	}
	return failed
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
	for _, capture := range l.in.files.captures {
		entry := domainEntry{domain: "capt:" + capture.host + "/" + capture.source, group: "conversations", asOf: capture.indexedAt,
			label: "Captures (" + capture.source + " on " + defaultString(capture.hostLabel, capture.host) + ")", running: "indexing", next: nextManualIndex, state: freshCurrent}
		if capture.needsIndex {
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

// analyses is the library-wide analyses the chain after a manual index
// brings current, and scheduled indexing leaves behind (sync_deferred).
func (l *ledger) analyses(ctx context.Context, c *Catalog, meta map[string]string, metaErr error, deferred map[string]deferredRow, deferredErr error, failures map[string]string) {
	// A deferred analysis: its generation is ahead of what the chain last
	// completed, by the number of scheduled indexes since.
	behindBySchedule := func(entry *domainEntry, name string) bool {
		row, ok := deferred[name]
		if !ok || row.generation <= row.completed {
			return false
		}
		entry.state, entry.reason, entry.next = freshBehind, "deferred-by-schedule", nextManualIndex
		entry.count, entry.countKnown, entry.unit = row.generation-row.completed, true, "scheduled index"
		return true
	}
	failedSince := func(entry *domainEntry, kind string) bool {
		ended, ok := failures[kind]
		if !ok || canonicalTime(ended) <= canonicalTime(entry.asOf) {
			return false
		}
		entry.state, entry.reason, entry.next = freshBehind, "failed", nextManualIndex
		return true
	}
	for _, stage := range []struct{ name, domain, label string }{{"identities", "an.identities", "Identity links"}, {"git", "an.git", "Merges from Git"}} {
		entry := domainEntry{domain: stage.domain, group: "analyses", label: stage.label, asOf: deferred[stage.name].completedAt, state: freshCurrent}
		switch {
		case deferredErr != nil:
			entry.state = freshUnknown
		case behindBySchedule(&entry, stage.name):
		case failedSince(&entry, "analysis."+stage.name):
		}
		l.add(entry)
	}

	// The Tools rollup: built for the ledger's generation, this layout,
	// today and this timezone, with no dirty workspaces.
	tools := domainEntry{domain: "an.tools", group: "analyses", label: "Tools rollup", asOf: deferred["tools"].completedAt, state: freshCurrent}
	var dirty int64
	dirtyErr := c.reader().QueryRowContext(ctx, "SELECT COUNT(*) FROM (SELECT 1 FROM tool_rollup_dirty LIMIT ?)", freshnessCap).Scan(&dirty)
	built := meta["tool_rollup_generation"]
	switch {
	case metaErr != nil || deferredErr != nil:
		tools.state = freshUnknown
	case behindBySchedule(&tools, "tools"):
	case !strings.HasSuffix(built, "/"+toolRollupVersion) || meta["tool_ledger_generation"]+"/"+toolRollupVersion != built:
		tools.state, tools.reason, tools.next = freshBehind, "version", nextReadOrIndex
		if built == "" {
			tools.reason = "never-built"
		} else if strings.HasSuffix(built, "/"+toolRollupVersion) {
			tools.reason = "changed"
		}
	case dirtyErr != nil:
		tools.state = freshUnknown
	case dirty > 0 || meta["tool_rollup_day"] != c.clock().Format("2006-01-02") || meta["tool_rollup_timezone"] != toolRollupTimezone() ||
		meta["tool_rollup_full_revision"] != meta["tool_rollup_built_full_revision"]:
		tools.state, tools.reason, tools.next = freshBehind, "changed", nextReadOrIndex
		if dirty > 0 {
			tools.count, tools.countKnown, tools.unit = dirty, true, "workspace"
		}
	case failedSince(&tools, "analysis.tools"):
	}
	l.add(tools)

	// Writing classification: built for the ledger and record generations.
	authorship := domainEntry{domain: "an.authorship", group: "analyses", label: "Writing classification", asOf: meta["authorship_built_at"], state: freshCurrent}
	want := meta["tool_ledger_generation"] + ":" + meta["authorship_inputs_generation"] + "/" + authorshipVersion
	switch {
	case metaErr != nil || deferredErr != nil:
		authorship.state = freshUnknown
	case behindBySchedule(&authorship, "authorship"):
	case meta["authorship_generation"] == "":
		authorship.state, authorship.reason, authorship.next = freshBehind, "never-built", nextManualIndex
	case meta["authorship_generation"] != want:
		authorship.state, authorship.reason, authorship.next = freshBehind, "changed", nextManualIndex
	case failedSince(&authorship, "analysis.authorship"):
	}
	l.add(authorship)

	// The tool ledger: conversations whose ledger is from an older version.
	// Counting them reads every conversation, so the ledger takes the count
	// from the last time anything asked for the upgrade's steps.
	ledgerEntry := domainEntry{domain: "an.ledger", group: "analyses", label: "Tools ledger", unit: "conversation", next: nextUpgrade}
	if steps, _, ok := c.upgrade().cachedSteps(); ok {
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
	} else {
		ledgerEntry.state = freshUnknown
	}
	l.add(ledgerEntry)
}

// findings is the findings' daily full pass.
func (l *ledger) findings(c *Catalog, meta map[string]string, metaErr error, deferred map[string]deferredRow, deferredErr error) {
	entry := domainEntry{domain: "findings", group: "findings", label: "Findings", asOf: meta["findings_built_at"], state: freshCurrent, next: nextManualIndex}
	row := deferred["findings"]
	switch {
	case metaErr != nil || deferredErr != nil:
		entry.state = freshUnknown
	case meta["findings_generation"] == "":
		entry.state, entry.reason = freshBehind, "never-built"
	case row.generation > row.completed:
		entry.state, entry.reason = freshBehind, "deferred-by-schedule"
		entry.count, entry.countKnown, entry.unit = row.generation-row.completed, true, "scheduled index"
	case meta["findings_generation"] != c.findingsGeneration():
		entry.state, entry.reason = freshBehind, "daily-pass-due"
	}
	l.add(entry)
}

// upgrade is the library upgrade's pending steps, from the last time
// anything asked for them (counting them scans the catalog).
func (l *ledger) upgrade(c *Catalog) {
	entry := domainEntry{domain: "upgrade", group: "upgrade", label: "Library upgrade", unit: "step", next: nextUpgrade}
	steps, _, ok := c.upgrade().cachedSteps()
	if !ok {
		entry.state = freshUnknown
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

// backups is the local backup (age only: it has no schedule) and each cloud
// destination (against its schedule).
func (l *ledger) backups(ctx context.Context, c *Catalog) {
	local := domainEntry{domain: "backup.local", group: "backups", label: "Local backup", next: nextBackup}
	switch {
	case l.in.files == nil || !l.in.files.backupKnown:
		local.state = freshUnknown
	case l.in.backupFailedAt != "" && canonicalTime(l.in.backupFailedAt) > canonicalTime(l.in.files.backupAt):
		local.asOf, local.state, local.reason = l.in.files.backupAt, freshBehind, "failed"
	case l.in.files.backupAt == "":
		// No backup at all: Pharos cannot tell whether one is wanted.
		local.state, local.reason = freshUnknown, "never"
	default:
		local.asOf, local.state = l.in.files.backupAt, freshCurrent
	}
	l.add(local)

	destinations, err := c.freshnessCloudDestinations(ctx)
	if err != nil || len(destinations) == 0 {
		return
	}
	runs, runsErr := c.cloudRuns(ctx)
	schedules, schedulesErr := c.cloudSchedules(ctx)
	for _, destination := range destinations {
		entry := domainEntry{domain: "backup.cloud:" + destination.id, group: "backups", label: "Cloud backup (" + destination.name + ")", next: nextCloudBackup}
		if runsErr != nil || schedulesErr != nil {
			entry.state = freshUnknown
			l.add(entry)
			continue
		}
		var verified, latest *cloudRun
		for index := range runs { // newest first
			run := &runs[index]
			if run.DestinationID != destination.id || run.Kind == "sample" {
				continue
			}
			if latest == nil && run.State != "running" && run.State != "pending" {
				latest = run
			}
			if verified == nil && run.State == "complete" && run.Verified {
				verified = run
			}
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
		case latest != nil && latest != verified && latest.State == "complete" && !latest.Verified:
			entry.state, entry.reason = freshBehind, "unverified"
		default:
			entry.state = freshCurrent
		}
		if entry.state != freshCurrent && schedule != nil && !schedule.Enabled && entry.reason != "failed" {
			entry.reason = "paused"
		}
		l.add(entry)
	}
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

// freshnessGroups are the summary line's groups, in order, with their words.
var freshnessGroups = []struct{ id, label string }{
	{"conversations", "Conversations"}, {"analyses", "Analyses"}, {"findings", "Findings"}, {"search", "Search"}, {"upgrade", "Upgrade"}, {"backups", "Backups"}}

// freshnessGroup is one group of the summary line (§5, the panel's Data
// row): the worst of its rows, with server words.
type freshnessGroup struct {
	Group string `json:"group"`
	Label string `json:"label"`
	State string `json:"state"`
	// Text is the state in a few words: "current", "behind, updates with
	// the next manual index", "catching up".
	Text string `json:"text"`
	// AsOf is the least recent as_of among the rows in the group's state;
	// AsOfLabel is what the surface says before its age.
	AsOf      *string `json:"as_of"`
	AsOfLabel string  `json:"as_of_label"`
	// Tooltip lists the rows that are not current and why.
	Tooltip    string  `json:"tooltip,omitempty"`
	CatchingUp *string `json:"catching_up"`
	// Shown is whether the one-line summary includes it: conversations and
	// analyses always, the rest when not current.
	Shown bool `json:"shown"`
}

// summarizeFreshness groups rows for the one-line summary.
func summarizeFreshness(rows []freshnessRow) []freshnessGroup {
	rank := map[string]int{freshCurrent: 0, freshUnknown: 1, freshBehind: 2}
	groups := []freshnessGroup{}
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
		next, tooltip := "", []string{}
		for _, row := range members {
			if row.Behind.State != freshCurrent {
				line := row.Label + ": " + lowerFirst(row.StateLabel)
				if row.Reason != nil {
					line += ", " + lowerFirst(row.Reason.Text)
				}
				tooltip = append(tooltip, line)
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
			if next == "" && row.Next != nil {
				next = *row.Next
			}
		}
		group.Tooltip = strings.Join(tooltip, "\n")
		switch {
		case group.State == freshCurrent:
			group.Text = "current"
		case group.CatchingUp != nil:
			group.Text = "catching up"
		case next != "":
			group.Text = group.State + ", " + next
		default:
			group.Text = group.State
		}
		if group.AsOf != nil && group.State != freshCurrent {
			group.AsOfLabel = "last current"
		}
		group.Shown = definition.id == "conversations" || definition.id == "analyses" || group.State != freshCurrent
		groups = append(groups, group)
	}
	return groups
}

// upgradeStepsCache remembers UpgradeSteps' latest answer for the ledger.
func (state *upgradeState) cachedSteps() ([]upgradeStep, time.Time, bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	return slices.Clone(state.lastSteps), state.lastStepsAt, !state.lastStepsAt.IsZero()
}
