package archive

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
)

// Execution resolution is cached per source generation, not per query cache
// generation: label and agent-event publication, usage reconciliation and
// other derived writes must not make every consumer re-resolve the catalog.
//
// Triggers on the stored rows resolution and occurrence ownership read advance
// execution_source_generation in the writing transaction and record which
// conversation owns the changed row (execution_source_changes). A cached
// snapshot at generation G is brought to a reader's generation by reloading
// only the conversations changed since G, read through the reader's own
// transaction, so a caller never combines an old graph with newer rows.

// executionSourceTable is one stored table resolution reads: the columns
// whose change can change a decision, and the conversations a row belongs to
// (SQL over %[1]s, the NEW or OLD row).
type executionSourceTable struct {
	table   string
	tag     string   // optional second dependency on a narrower set of columns
	owners  []string // SQL over the row, or a SELECT of owner rows
	columns []string // nil: every column
	// presence lists columns that matter only as zero or positive.
	presence []string
}

var executionSourceTables = []executionSourceTable{
	// Source titles have conversation scope only while there is one root.
	// A second root, a move or a corrected depth invalidates the other roots'
	// names too. Nickname/usage changes keep their existing narrower owner.
	{table: "conversations", tag: "name_scope", owners: []string{`SELECT id owner FROM conversations WHERE workspace_id=%[1]s.workspace_id
 AND agent_depth=0 AND instr(native_id,':subagent:')=0
 AND %[1]s.agent_depth=0 AND instr(%[1]s.native_id,':subagent:')=0`,
		`SELECT c.id owner FROM conversations c JOIN workspace_names n ON n.workspace_id=c.workspace_id
 WHERE n.display_workspace_id=%[1]s.workspace_id AND c.agent_depth=0 AND instr(c.native_id,':subagent:')=0
 AND %[1]s.agent_depth=0 AND instr(%[1]s.native_id,':subagent:')=0`},
		columns: []string{"id", "workspace_id", "native_id", "agent_depth", "parent_id", "provider"}},
	{table: "conversations", owners: []string{"%[1]s.id"},
		columns: []string{"id", "workspace_id", "provider", "account", "native_id", "parent_id", "agent_depth", "agent_path", "agent_nickname", "started_at", "aliases_json"}},
	{table: "agent_sessions", owners: []string{"%[1]s.conversation_id"},
		columns:  []string{"id", "conversation_id", "workspace_id", "provider", "native_id", "parent_id", "child_conversation_id", "delegation_message_id", "kind", "started_at"},
		presence: []string{"total_tokens"}},
	// Session membership: whether a launch was observed, and an original's
	// owning stream.
	{table: "agent_session_messages", owners: []string{"(SELECT conversation_id FROM agent_sessions WHERE id=%[1]s.agent_session_id)"},
		columns: []string{"agent_session_id", "message_id"}},
	// Launch calls and results, copied history and assignments.
	{table: "messages", owners: []string{"%[1]s.conversation_id"},
		// evidence_locator classifies stored IDs (messageIDKind).
		columns: []string{"id", "conversation_id", "native_id", "role", "kind", "text", "call_id", "content_hash", "created_at", "source_order", "evidence_locator"}},
	{table: "tool_calls", owners: []string{"%[1]s.conversation_id"}, columns: []string{"id", "conversation_id", "agent_session_id", "call_message_id"}},
	// A request's occurrence follows the messages that carried its usage.
	{table: "model_requests", owners: []string{"%[1]s.conversation_id"}, columns: []string{"id", "conversation_id", "agent_session_id", "source_message_id", "final_message_id"}},
	{table: "conversation_identity_links", owners: []string{"%[1]s.left_id", "%[1]s.right_id"}},
	{table: "conversation_launches", owners: []string{"%[1]s.child_id", "%[1]s.parent_id"}},
	{table: "conversation_name_candidates", owners: []string{"%[1]s.conversation_id"}},
	{table: "authorship_message_state", owners: []string{"SELECT conversation_id owner FROM messages WHERE id=%[1]s.message_id"}},
	{table: "workspace_names", owners: []string{"SELECT id owner FROM conversations WHERE workspace_id=%[1]s.workspace_id",
		"SELECT c.id owner FROM conversations c JOIN workspace_names n ON n.workspace_id=c.workspace_id WHERE n.display_workspace_id=%[1]s.workspace_id"},
		columns: []string{"workspace_id", "display_source", "source_scope", "display_workspace_id", "override_title"}},
	{table: "tl1_attempts", owners: []string{"SELECT c.id owner FROM conversations c JOIN workspaces w ON w.id=c.workspace_id WHERE c.native_id=%[1]s.conversation_native_id AND w.source_kind='tl1' AND w.source_id=%[1]s.installation_id||':'||%[1]s.task_id"},
		columns: []string{"installation_id", "task_id", "conversation_native_id", "attempt_number"}},
	{table: "origin_observations", owners: []string{"%[1]s.conversation_id"},
		// evidence_key says which adapter recorded a parent (its namespace).
		columns: []string{"conversation_id", "evidence_key", "parent_native_id", "parent_provider", "parent_account"}},
	{table: "execution_claims", owners: []string{"%[1]s.conversation_id"}},
	// The source kind says which adapter recorded a retained parent with no
	// recorded namespace (addRetainedNativeParents).
	{table: "workspaces", owners: []string{"SELECT id owner FROM conversations WHERE workspace_id=%[1]s.id",
		"SELECT c.id owner FROM conversations c JOIN workspace_names n ON n.workspace_id=c.workspace_id WHERE n.display_workspace_id=%[1]s.id"}, columns: []string{"id", "source_id", "source_kind", "title"}},
	{table: "execution_evidence_state", owners: []string{"%[1]s.conversation_id"}},
}

// executionSourceIndexes serve the reads a changed conversation needs: legacy
// launch results by (conversation, call ID) without scanning the transcript.
var executionSourceIndexes = []string{
	`CREATE INDEX IF NOT EXISTS messages_call_idx ON messages(conversation_id, call_id) WHERE call_id<>''`,
	`CREATE INDEX IF NOT EXISTS messages_workflow_call_idx ON messages(conversation_id,call_id) WHERE ` + claudeWorkflowCallSQL(""),
}

// ensureExecutionSourceGeneration installs the source generation, its change
// log and triggers (recreated so new columns participate) and lookup indexes.
// It is idempotent; triggers run in every writer's transaction, including
// another process's.
func (c *Catalog) ensureExecutionSourceGeneration() error {
	tx, finish, err := c.beginTrackedWrite(context.Background(), "execution-source-schema")
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(); finish() }()
	if err := installExecutionSourceGeneration(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func installExecutionSourceGeneration(tx *sql.Tx) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS execution_source_generation (id INTEGER PRIMARY KEY CHECK(id=1), value INTEGER NOT NULL);
 INSERT INTO execution_source_generation(id,value) SELECT 1,1 WHERE NOT EXISTS(SELECT 1 FROM execution_source_generation WHERE id=1);
 CREATE TABLE IF NOT EXISTS execution_source_changes (conversation_id TEXT PRIMARY KEY, generation INTEGER NOT NULL) WITHOUT ROWID;
 CREATE INDEX IF NOT EXISTS execution_source_changes_generation_idx ON execution_source_changes(generation)`); err != nil {
		return err
	}
	for _, statement := range executionSourceIndexes {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	old, err := queryMaps(tx, `SELECT name FROM sqlite_schema WHERE type='trigger' AND name GLOB 'execution_source_*'`)
	if err != nil {
		return err
	}
	for _, trigger := range old {
		if _, err := tx.Exec("DROP TRIGGER " + sqlIdentifier(firstString(trigger["name"]))); err != nil {
			return err
		}
	}
	for _, source := range executionSourceTables {
		statements, err := executionSourceTriggers(tx, source)
		if err != nil {
			return err
		}
		for _, statement := range statements {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("%s: %w", source.table, err)
			}
		}
	}
	statements, err := retainedEvidenceTriggers(tx)
	if err != nil {
		return err
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("retained evidence: %w", err)
		}
	}
	return nil
}

// executionEvidenceStale prefixes the version of retained typed evidence
// whose stored inputs changed after backfill derived it (retainedEvidenceTriggers).
const executionEvidenceStale = "stale/"

// claudeGroupRootSQL is claudeRootNative of conversation c's native ID.
const claudeGroupRootSQL = `CASE WHEN instr(c.native_id,':subagent:')>0 THEN substr(c.native_id,1,instr(c.native_id,':subagent:')-1) ELSE c.native_id END`

// retainedEvidenceTriggers keep evidence re-derived from stored rows (input
// 'retained') from reading as current once those rows change: a write to a
// row backfillExecutionEvidence or storedClaudeGroup reads marks the owning
// conversation's state stale, and for a Claude conversation every retained
// member of its root group, whose lineage claims read each other's rows.
// Readers then use the stored rows (the legacy reading) until backfill
// re-derives them. Evidence from a provider read (input 'ingest') is the
// provider's record and stays: a later pruning or correction of the
// transcript does not withdraw what the provider asserted. Ingestion writes
// its messages before writeExecutionEvidence records the state, so its own
// writes leave the state current. conversation_parent_refs holds only the
// provider's field, which only ingestion writes.
func retainedEvidenceTriggers(tx *sql.Tx) ([]string, error) {
	tables, err := queryMaps(tx, `SELECT name FROM sqlite_schema WHERE type='table' AND name IN ('execution_evidence_state','origin_observations')`)
	if err != nil || len(tables) < 2 {
		return nil, err // optional tables may not exist yet
	}
	// stale marks the retained evidence of conversation id, a SQL expression.
	stale := func(id string) string {
		root := claudeGroupRootSQL
		return `UPDATE execution_evidence_state SET version='` + executionEvidenceStale + `'||version
 WHERE input='retained' AND substr(version,1,` + fmt.Sprint(len(executionEvidenceStale)) + `)<>'` + executionEvidenceStale + `' AND (conversation_id=` + id + ` OR conversation_id IN (
 SELECT g.id FROM conversations c JOIN conversations g ON g.account=c.account AND g.workspace_id=c.workspace_id AND g.provider='claude'
 AND (g.native_id=` + root + ` OR g.native_id>` + root + `||':subagent:' AND g.native_id<` + root + `||':subagent;')
 WHERE c.id=` + id + ` AND c.provider='claude'));`
	}
	// The messages backfill and Claude group lineage read: launches, results,
	// Workflow calls, Codex session notes and a conversation's first assignment.
	read := func(row string) string {
		return `(` + row + `.kind IN ('delegation','delegation_result') OR (` + claudeWorkflowCallSQL(row) + `) OR ` + row + `.kind='tool_result' AND ` + row + `.call_id<>''
 OR ` + row + `.kind='metadata' AND ` + row + `.text LIKE '{"payload":%"type":"session_meta"%' OR ` + row + `.kind='message' AND ` + row + `.role IN ('user','agent'))`
	}
	changed := func(columns ...string) string {
		parts := make([]string, len(columns))
		for i, column := range columns {
			parts[i] = "OLD." + column + " IS NOT NEW." + column
		}
		return "(" + strings.Join(parts, " OR ") + ")"
	}
	return []string{
		`CREATE TRIGGER execution_source_retained_messages_insert AFTER INSERT ON messages WHEN ` + read("NEW") + ` BEGIN ` + stale("NEW.conversation_id") + ` END`,
		`CREATE TRIGGER execution_source_retained_messages_delete AFTER DELETE ON messages WHEN ` + read("OLD") + ` BEGIN ` + stale("OLD.conversation_id") + ` END`,
		`CREATE TRIGGER execution_source_retained_messages_update AFTER UPDATE ON messages WHEN (` + read("OLD") + ` OR ` + read("NEW") + `)
 AND ` + changed("conversation_id", "native_id", "role", "kind", "call_id", "text", "created_at", "source_order") + ` BEGIN ` + stale("OLD.conversation_id") + stale("NEW.conversation_id") + ` END`,
		`CREATE TRIGGER execution_source_retained_origins_insert AFTER INSERT ON origin_observations BEGIN ` + stale("NEW.conversation_id") + ` END`,
		`CREATE TRIGGER execution_source_retained_origins_delete AFTER DELETE ON origin_observations BEGIN ` + stale("OLD.conversation_id") + ` END`,
		`CREATE TRIGGER execution_source_retained_origins_update AFTER UPDATE ON origin_observations WHEN ` + changed("conversation_id", "evidence_key", "parent_native_id", "parent_provider", "parent_account") + `
 BEGIN ` + stale("OLD.conversation_id") + stale("NEW.conversation_id") + ` END`,
		`CREATE TRIGGER execution_source_retained_workspaces_update AFTER UPDATE ON workspaces WHEN ` + changed("source_id", "source_kind") + `
 BEGIN UPDATE execution_evidence_state SET version='` + executionEvidenceStale + `'||version WHERE input='retained'
 AND substr(version,1,` + fmt.Sprint(len(executionEvidenceStale)) + `)<>'` + executionEvidenceStale + `' AND conversation_id IN (SELECT id FROM conversations WHERE workspace_id=NEW.id); END`,
	}, nil
}

func executionSourceTriggers(tx *sql.Tx, source executionSourceTable) ([]string, error) {
	columns, err := queryMaps(tx, "PRAGMA table_info("+sqlIdentifier(source.table)+")")
	if err != nil || len(columns) == 0 {
		return nil, err // optional tables may not exist yet
	}
	present := map[string]bool{}
	for _, column := range columns {
		present[firstString(column["name"])] = true
	}
	var changed []string
	for _, column := range columns {
		name := firstString(column["name"])
		if source.columns == nil || slices.Contains(source.columns, name) {
			changed = append(changed, "OLD."+sqlIdentifier(name)+" IS NOT NEW."+sqlIdentifier(name))
		}
	}
	for _, name := range source.presence {
		if present[name] {
			changed = append(changed, "(OLD."+sqlIdentifier(name)+">0) IS NOT (NEW."+sqlIdentifier(name)+">0)")
		}
	}
	owners := func(rows ...string) string {
		var parts []string
		for _, row := range rows {
			for _, owner := range source.owners {
				if expr := fmt.Sprintf(owner, row); strings.HasPrefix(expr, "SELECT ") {
					parts = append(parts, expr) // a query naming several owners
				} else {
					parts = append(parts, "SELECT "+expr+" owner")
				}
			}
		}
		return strings.Join(parts, " UNION ")
	}
	body := func(rows ...string) string {
		return `BEGIN UPDATE execution_source_generation SET value=value+1 WHERE id=1;
 INSERT INTO execution_source_changes(conversation_id,generation)
 SELECT owner,(SELECT value FROM execution_source_generation WHERE id=1) FROM (` + owners(rows...) + `) WHERE owner IS NOT NULL
 ON CONFLICT(conversation_id) DO UPDATE SET generation=excluded.generation; END`
	}
	prefix := "execution_source_" + source.table
	if source.tag != "" {
		prefix += "_" + source.tag
	}
	name := func(event string) string { return sqlIdentifier(prefix + "_" + event) }
	table := sqlIdentifier(source.table)
	return []string{
		fmt.Sprintf("CREATE TRIGGER %s AFTER INSERT ON %s %s", name("insert"), table, body("NEW")),
		fmt.Sprintf("CREATE TRIGGER %s AFTER DELETE ON %s %s", name("delete"), table, body("OLD")),
		fmt.Sprintf("CREATE TRIGGER %s AFTER UPDATE ON %s WHEN %s %s", name("update"), table, strings.Join(changed, " OR "), body("NEW", "OLD")),
	}, nil
}

// executionSourceGeneration is the source generation in q's snapshot. tracked
// is false for a catalog or snapshot without it (offline labeling snapshots,
// older catalogs), which then resolve without the cache.
func executionSourceGeneration(ctx context.Context, q queryer) (generation int64, tracked bool, err error) {
	rows, err := queryMapsContext(ctx, q, "SELECT value FROM execution_source_generation WHERE id=1")
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return 0, false, nil
		}
		return 0, false, err
	}
	if len(rows) == 0 {
		return 0, false, nil
	}
	return integer(rows[0]["value"]), true, nil
}

// executionChangedSince lists the conversations whose source rows changed
// after generation, as q sees them.
func executionChangedSince(ctx context.Context, q queryer, generation int64) ([]string, error) {
	rows, err := queryMapsContext(ctx, q, `SELECT conversation_id FROM execution_source_changes WHERE generation>? ORDER BY conversation_id`, generation)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, firstString(row["conversation_id"]))
	}
	return out, nil
}

// executionSnapshot is the resolution of one source generation with the rows
// it was assembled from, so the next generation reloads only what changed.
// The catalog-wide occurrence index is built when a global consumer first
// asks, from the newest earlier snapshot's claims plus the conversations
// changed since (base); only one claim map is kept per snapshot.
type executionSnapshot struct {
	generation int64
	rows       *executionSourceRows
	resolution *executionResolution

	// occurrenceBuild serializes this snapshot's catalog occurrence build;
	// occurrenceMu guards the fields below and is held only to read or set
	// them, so a successor snapshot takes its base without waiting for a
	// build in flight.
	occurrenceBuild  sync.Mutex
	occurrenceMu     sync.Mutex
	occurrenceClaims map[string][]executionClaim // by copy conversation, once built
	occurrences      *occurrenceIndex
	base             *occurrenceBase
}

// occurrenceBase is the newest earlier snapshot's built occurrence claims
// and the conversations changed since it.
type occurrenceBase struct {
	claims  map[string][]executionClaim
	changed []string
	rows    *executionSourceRows // rows at the base, for moved parents and native keys
}

// executionOccurrenceBuildHook, when set by a test, runs inside each catalog
// occurrence build.
var executionOccurrenceBuildHook func()

// executionCache holds the newest snapshot and serializes its rebuilds: many
// requests after one commit share one incremental reload. It also keeps a few
// earlier snapshots, so a transaction that began before the newest commits
// (common during ingestion) is brought forward from one of them instead of
// resolving the whole catalog again.
type executionCache struct {
	mu      sync.Mutex
	current *executionSnapshot
	earlier []*executionSnapshot // ascending generation, below current
	build   chan struct{}        // closed when the running rebuild finishes
	// older holds builds for transactions behind current, by generation:
	// readers at one older generation share one build.
	older map[int64]*executionOlderBuild
	stats executionCacheStats
}

// executionOlderBuild is one build for an older generation; its result goes
// to every waiter even when the snapshot is too old to keep.
type executionOlderBuild struct {
	done     chan struct{}
	snapshot *executionSnapshot // nil when the build failed
}

// executionSnapshotBuildHook, when set by a test, runs before each snapshot
// build with the generation being built.
var executionSnapshotBuildHook func(generation int64)

// executionCacheEarlier bounds the earlier snapshots kept. Each shares
// unchanged rows with its successors.
var executionCacheEarlier = 4

// before returns the newest cached snapshot at or below generation, or nil.
func (cache *executionCache) before(generation int64) *executionSnapshot {
	for i := len(cache.earlier) - 1; i >= 0; i-- {
		if cache.earlier[i].generation <= generation {
			return cache.earlier[i]
		}
	}
	return nil
}

// keep records s, newest as current. Called with mu held.
func (cache *executionCache) keep(s *executionSnapshot) {
	all := cache.earlier
	if cache.current != nil {
		all = append(slices.Clone(all), cache.current)
	}
	if slices.ContainsFunc(all, func(x *executionSnapshot) bool { return x.generation == s.generation }) {
		return
	}
	all = append(all, s)
	slices.SortFunc(all, func(a, b *executionSnapshot) int { return int(a.generation - b.generation) })
	cache.current = all[len(all)-1]
	all = all[:len(all)-1]
	cache.earlier = all[max(0, len(all)-executionCacheEarlier):]
}

// executionResolution is the shared resolution of the catalog's current
// source generation.
func (c *Catalog) executionResolution(ctx context.Context) (*executionResolution, error) {
	tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return c.executionResolutionAt(ctx, tx)
}

// executionResolutionAt returns the resolution of exactly the source
// generation q reads: the cached snapshot when it matches, else the snapshot
// brought forward by reloading changed conversations through q.
//
// q must be a read-only transaction (or another committed snapshot): the
// snapshot built through it is kept in the catalog's cache under q's
// generation, and a write transaction's rows may roll back while a later
// commit reaches the same generation with other contents. Every caller opens
// c.reader().BeginTx(ReadOnly) for it. A writer that needs the resolution of
// its own uncommitted rows uses resolveExecutionsFrom(ctx, tx), which caches
// nothing.
func (c *Catalog) executionResolutionAt(ctx context.Context, q queryer) (*executionResolution, error) {
	s, err := c.executionSnapshotAt(ctx, q)
	if err != nil {
		return nil, err
	}
	return s.resolution, nil
}

// executionSnapshotAt is executionResolutionAt's snapshot, with the same
// precondition: q is a read-only (committed) snapshot, never a write
// transaction, because the result is admitted to the shared cache.
func (c *Catalog) executionSnapshotAt(ctx context.Context, q queryer) (*executionSnapshot, error) {
	generation, tracked, err := executionSourceGeneration(ctx, q)
	if err != nil {
		return nil, err
	}
	if !tracked {
		return newExecutionSnapshot(ctx, q, 0, nil, nil)
	}
	cache := &c.execution
	for {
		cache.mu.Lock()
		current := cache.current
		if current != nil && current.generation == generation {
			cache.mu.Unlock()
			return current, nil
		}
		older := current != nil && current.generation > generation
		var base *executionSnapshot
		var running chan struct{}
		var shared *executionOlderBuild
		if older {
			// An older transaction: bring the newest snapshot below it forward
			// through its own rows, or resolve them whole when none is kept.
			if base = cache.before(generation); base != nil && base.generation == generation {
				cache.mu.Unlock()
				return base, nil
			}
			if shared = cache.older[generation]; shared != nil {
				running = shared.done
			}
		} else {
			base, running = current, cache.build
		}
		if running != nil {
			// Wait for the running build, then look again: it may be ours. A
			// build reads through its leader's transaction, so a cancelled
			// leader ends it; the next reader leads from the same base.
			cache.stats.waits++
			cache.mu.Unlock()
			select {
			case <-running:
				if shared != nil && shared.snapshot != nil {
					return shared.snapshot, nil
				}
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		build := make(chan struct{})
		if older {
			if cache.older == nil {
				cache.older = map[int64]*executionOlderBuild{}
			}
			shared = &executionOlderBuild{done: build}
			cache.older[generation] = shared
		} else {
			cache.build = build
		}
		cache.mu.Unlock()
		if executionSnapshotBuildHook != nil {
			executionSnapshotBuildHook(generation)
		}
		var stats executionCacheStats
		s, err := newExecutionSnapshot(ctx, q, generation, base, &stats)
		cache.mu.Lock()
		cache.stats.add(stats)
		if err == nil {
			cache.keep(s)
		}
		if older {
			if err == nil {
				shared.snapshot = s
			}
			delete(cache.older, generation)
		} else {
			cache.build = nil
		}
		close(build)
		cache.mu.Unlock()
		return s, err
	}
}

type executionCacheStats struct {
	fullLoads, incrementalLoads, reloadedConversations int
	reused                                             int // incremental loads whose evidence was unchanged
	waits                                              int // readers that waited for another's build
}

func (s *executionCacheStats) add(other executionCacheStats) {
	s.waits += other.waits
	s.reused += other.reused
	s.fullLoads += other.fullLoads
	s.incrementalLoads += other.incrementalLoads
	s.reloadedConversations += other.reloadedConversations
}

// newExecutionSnapshot resolves generation through q, reusing previous's rows
// for conversations that did not change since it.
func newExecutionSnapshot(ctx context.Context, q queryer, generation int64, previous *executionSnapshot, stats *executionCacheStats) (*executionSnapshot, error) {
	s := &executionSnapshot{generation: generation}
	if previous == nil {
		rows, err := fetchExecutionSourceRows(ctx, q, nil, nil)
		if err != nil {
			return nil, err
		}
		s.rows = rows
		if stats != nil {
			stats.fullLoads++
		}
	} else {
		changed, err := executionChangedSince(ctx, q, previous.generation)
		if err != nil {
			return nil, err
		}
		fresh, err := fetchExecutionSourceRows(ctx, q, changed, []string{})
		if err != nil {
			return nil, err
		}
		// Copied launches depend on ancestors' launch calls and ancestry: a
		// changed conversation reloads its own, and its descendants' only
		// when those inputs changed.
		var moved []string
		for _, id := range changed {
			if previous.rows.copyInputs(id) != fresh.copyInputs(id) {
				moved = append(moved, id)
			}
		}
		below := slices.Compact(slices.Sorted(slices.Values(append(executionDescendants(moved, previous.rows, fresh), changed...))))
		copies, err := fetchExecutionSourceRows(ctx, q, []string{}, below)
		if err != nil {
			return nil, err
		}
		fresh.copies = copies.copies
		s.rows = previous.rows.merge(fresh, changed, below)
		if previous.rows.sameEvidence(fresh, changed, below) {
			// Appended messages, results already read, accounting: the
			// decisions stand, and so does their revision.
			s.resolution = previous.resolution
		}
		// The previous snapshot's built claims, or its own base, carried
		// forward; never waits for a build in flight.
		previous.occurrenceMu.Lock()
		switch {
		case previous.occurrenceClaims != nil:
			s.base = &occurrenceBase{claims: previous.occurrenceClaims, changed: changed, rows: previous.rows}
		case previous.base != nil:
			s.base = &occurrenceBase{claims: previous.base.claims, rows: previous.base.rows,
				changed: slices.Compact(slices.Sorted(slices.Values(append(slices.Clone(previous.base.changed), changed...))))}
		}
		previous.occurrenceMu.Unlock()
		if stats != nil {
			stats.incrementalLoads++
			stats.reloadedConversations += len(below)
		}
	}
	if s.resolution != nil {
		if stats != nil {
			stats.reused++
		}
		return s, nil
	}
	e, err := s.rows.evidence(ctx, q, generation)
	if err != nil {
		return nil, err
	}
	s.resolution = resolveExecutions(e)
	return s, nil
}

// resolveExecutionsFrom resolves the evidence visible to q without the
// shared cache: offline labeling snapshots and other read transactions that
// must agree with their own rows.
func resolveExecutionsFrom(ctx context.Context, q queryer) (*executionResolution, error) {
	e, err := loadExecutionEvidence(ctx, q)
	if err != nil {
		return nil, err
	}
	return resolveExecutions(e), nil
}

// catalogOccurrences builds (once) the catalog-wide occurrence index of s. q
// must read s's generation. Claims of conversations that did not change since
// the base are reused; a changed conversation reloads its own claims, its
// descendants' (copied history follows the recorded ancestry) and those of
// copies that declared it as their source or matched an original in it.
func (s *executionSnapshot) catalogOccurrences(ctx context.Context, q queryer) (*occurrenceIndex, error) {
	s.occurrenceBuild.Lock()
	defer s.occurrenceBuild.Unlock()
	s.occurrenceMu.Lock()
	built, base := s.occurrences, s.base
	s.occurrenceMu.Unlock()
	if built != nil {
		return built, nil
	}
	if executionOccurrenceBuildHook != nil {
		executionOccurrenceBuildHook()
	}
	claims := map[string][]executionClaim{}
	scope := []string(nil) // nil: every conversation
	if base != nil {
		var err error
		if scope, err = s.occurrenceScope(ctx, q, base); err != nil {
			return nil, err
		}
		for cid, list := range base.claims {
			claims[cid] = list
		}
		for _, cid := range scope {
			delete(claims, cid)
		}
	}
	fresh, err := loadOccurrenceClaims(ctx, q, scope)
	if err != nil {
		return nil, err
	}
	for _, claim := range fresh {
		cid := occurrenceClaimConversation(claim)
		claims[cid] = append(claims[cid], claim)
	}
	var all []executionClaim
	for _, list := range claims {
		all = append(all, list...)
	}
	// Copied launch calls follow this generation's resolution, so they are
	// added to the cached per-conversation claims here, never cached with them.
	if all, err = withCopiedLaunchCalls(ctx, q, s.resolution, nil, all); err != nil {
		return nil, err
	}
	index := newOccurrenceIndex(s.resolution, nil, all)
	s.occurrenceMu.Lock()
	s.occurrenceClaims, s.occurrences, s.base = claims, index, nil
	s.occurrenceMu.Unlock()
	return index, nil
}

// occurrenceScope widens the conversations changed since the base to every
// conversation whose occurrence claims can depend on them.
func (s *executionSnapshot) occurrenceScope(ctx context.Context, q queryer, base *occurrenceBase) ([]string, error) {
	changed := map[string]bool{}
	for _, cid := range base.changed {
		changed[cid] = true
	}
	scope := map[string]bool{}
	for _, cid := range executionDescendants(base.changed, base.rows, s.rows) {
		scope[cid] = true
	}
	// Sessions whose transcript changed (main streams included), before or
	// after the change.
	changedSessions := map[string]bool{}
	for _, rows := range []*executionSourceRows{base.rows, s.rows} {
		if rows == nil {
			continue
		}
		for cid := range changed {
			if cv := rows.conversations[cid]; cv != nil && cv.mainSession != "" {
				changedSessions[cv.mainSession] = true
			}
			for _, row := range rows.sessions[cid] {
				changedSessions[row.id] = true
			}
		}
	}
	// Copies matched to an original in a changed conversation.
	for cid, list := range base.claims {
		for _, claim := range list {
			if claim.Context.Kind == sourceSession && changedSessions[claim.Context.ID] || claim.Context.Kind != sourceSession && changed[claim.Context.ID] {
				scope[cid] = true
				break
			}
		}
	}
	// Copies that declared a changed conversation as their source, which may
	// only now be retained.
	keys := map[string]bool{}
	for cid := range changed {
		for _, rows := range []*executionSourceRows{s.rows, base.rows} {
			if rows == nil {
				continue
			}
			if cv := rows.conversations[cid]; cv != nil {
				keys[executionNativeKey(cv.provider, cv.account, cv.native)] = true
			}
		}
	}
	if present, err := executionEvidenceTablesPresent(ctx, q); err != nil {
		return nil, err
	} else if present && len(keys) > 0 {
		rows, err := queryMapsContext(ctx, q, `SELECT DISTINCT conversation_id,object FROM execution_claims WHERE kind=?`, evidenceHistoryCopy)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if keys[firstString(row["object"])] {
				scope[firstString(row["conversation_id"])] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(scope)), nil
}
