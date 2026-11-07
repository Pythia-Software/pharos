package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	labelrt "github.com/gbdubs/pharos/internal/labeler"
)

// An agent events session scores a selected population with the classic
// pipeline (agentEventsPipeline): its verifier, item context and discovered
// inventory are kept between steps, and its workers stay warm in the
// process's labeler manager. Its safety rests on the ledger's own state:
//
//   - Discover is RunAgentEvents' discovery (agentEventsWork) over the whole
//     library: forks of changed conversations get dirty marks of their own,
//     and deleted conversations lose their events. Every other reason a
//     conversation is wanted (never built, another structure, another
//     verifier's rows, a wider horizon) is the ledger's own and outlives the
//     session.
//   - A step adopts a changed token or structure only after marking every
//     live fork of that conversation, transitively, read from the catalog
//     (forks created after discovery included). Only forks follow a change:
//     a re-sent message does not change an agent's text.
//   - A step stores with the token it adopted, so a mark written while it
//     computes survives (Superseded), and reports Done only what the ledger
//     shows current.
//
// Events read the labeling item context (agent replies and thinking, mirror
// representatives, sessions, reasoning extraction) and take stored writing
// estimates as the builder reads them; they never read labels.
type agentEventsSession struct {
	c        *Catalog
	labels   LabelerSettings
	settings AgentEventSettings
	alloc    workAllocation

	// mu serializes Discover and Run; inventory guards jobs and queue for
	// PendingFor and PendingPage, as in labelsSession.
	mu, inventory sync.Mutex
	discovered    atomic.Bool
	// Set by the first Discover; fixed for the session's life.
	verifier *agentEventVerifier
	manifest string
	prep     int
	serial   bool
	// Discovered state, replaced by each Discover: the horizon it read from.
	since   time.Time
	builder *labelsItemBuilder
	jobs    map[string]*agentEventsConversation
	queue   workDomainQueue
	// Counts for tests and diagnostics.
	contexts, discoveries int
	last                  *AgentEventsRunStats
	beforeRun             func(context.Context)
}

var errAgentEventsSessionStale = errors.New("the agent events verifier changed; start a new agent events session")

// newAgentEventsSession checks that the pass can run; it reads nothing from
// the catalog. labels are the pass's labeler settings as the caller derived
// them (AgentEventSettings.labeler, then labelsForPower): they are not
// derived again, which would undo a battery limit. The allocation is the
// session's fixed envelope.
func (c *Catalog) newAgentEventsSession(labels LabelerSettings, settings AgentEventSettings, alloc workAllocation) (*agentEventsSession, error) {
	labels, settings = labels.withAssets(), settings.withAssets()
	if settings.ModelDir == "" {
		return nil, fmt.Errorf("%w: the verifier is %s; run pharos agent-events fetch, or set agent_events_model_dir (PHAROS_AGENT_EVENTS_MODEL_DIR)",
			labelrt.ErrModelUnavailable, labelsAssetStatus(labelsAssets.AgentEventsModel))
	}
	if labels.Worker == "" {
		return nil, fmt.Errorf("%w: set labels_worker (or PHAROS_LABELS_WORKER)", labelrt.ErrModelUnavailable)
	}
	alloc.CPUs = max(1, alloc.CPUs)
	return &agentEventsSession{c: c, labels: labels, settings: settings, alloc: alloc}, nil
}

// Allocation is the CPUs every step of this session uses.
func (s *agentEventsSession) Allocation() workAllocation { return s.alloc }

// Discovered reports whether steps can run; false asks for Discover.
func (s *agentEventsSession) Discovered() bool { return s.discovered.Load() }

// Identity is what scored rows are stored under ("" before Discover).
func (s *agentEventsSession) Identity() string {
	s.inventory.Lock()
	defer s.inventory.Unlock()
	if s.verifier == nil {
		return ""
	}
	return s.verifier.identity
}

// Since is the horizon the last Discover read from (zero: everything).
func (s *agentEventsSession) Since() time.Time {
	s.inventory.Lock()
	defer s.inventory.Unlock()
	return s.since
}

// Close drops the session's context; its workers idle in the manager.
func (s *agentEventsSession) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.discovered.Store(false)
	s.inventory.Lock()
	defer s.inventory.Unlock()
	s.builder, s.jobs, s.queue = nil, nil, workDomainQueue{}
}

func (s *agentEventsSession) setJob(id string, job *agentEventsConversation) {
	s.inventory.Lock()
	defer s.inventory.Unlock()
	if job == nil {
		delete(s.jobs, id)
	} else {
		s.jobs[id] = job
		s.queue.enrolled(id)
	}
}

// checkModel loads the verifier on first use and afterwards refuses a
// session whose verifier files changed. The split is fixed with it: the
// worker's threads within the allocation (labelsSplit), one worker.
func (s *agentEventsSession) checkModel() error {
	if s.verifier != nil {
		if labelsManifestDigest(s.settings.ModelDir) != s.manifest {
			return errAgentEventsSessionStale
		}
		return nil
	}
	manifest := labelsManifestDigest(s.settings.ModelDir)
	verifier, err := loadAgentEventVerifier(s.settings.ModelDir, s.settings.Variant)
	if err != nil {
		return err
	}
	var threads int
	s.prep, threads, s.serial = labelsSplit(s.alloc.CPUs, s.labels.Threads, labelsGPUOnly(verifier.model, s.labels))
	s.labels.Threads, s.labels.Workers = threads, 1
	s.inventory.Lock()
	s.verifier, s.manifest = verifier, manifest
	s.inventory.Unlock()
	return nil
}

// Discover is the session's broad phase, admitted like any step (it is
// serial: one CPU): it loads the verifier (once), reads the item context and
// runs RunAgentEvents' discovery over the library at the current horizon.
func (s *agentEventsSession) Discover(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.c
	release, err := c.agentEventsInvocation()
	if err != nil {
		return err
	}
	defer release()
	if err := s.checkModel(); err != nil {
		return err
	}
	if err := c.ensureAgentEventsLedger(ctx); err != nil {
		return err
	}
	s.discovered.Store(false)
	builder, err := newLabelsItemBuilder(ctx, c.reader())
	if err != nil {
		return err
	}
	s.contexts++
	since := s.settings.since()
	found, err := c.agentEventsWork(ctx, builder, s.verifier.identity, AgentEventsRunOptions{Since: since}, &AgentEventsRunStats{})
	if err != nil {
		return err
	}
	s.discoveries++
	jobs := make(map[string]*agentEventsConversation, len(found))
	order := make([]string, len(found))
	for i, job := range found {
		jobs[job.id], order[i] = job, job.id
	}
	s.inventory.Lock()
	s.since, s.builder, s.jobs, s.queue = since, builder, jobs, newWorkDomainQueue(order)
	s.inventory.Unlock()
	s.discovered.Store(true)
	return nil
}

// agentEventsHorizonDay is the day since falls on ("" for everything).
func agentEventsHorizonDay(since time.Time) string {
	if since.IsZero() {
		return ""
	}
	return since.UTC().Format(time.DateOnly)
}

// horizon is since as the ledger stores it (” for everything).
func agentEventsHorizon(since time.Time) string {
	if since.IsZero() {
		return ""
	}
	return formatTime(since)
}

// PendingFor is which selected conversations may still need scoring: in the
// inventory, dirty, or never built and not over before the horizon. Indexed
// lookups only; before Discover every selected conversation is pending.
func (s *agentEventsSession) PendingFor(ctx context.Context, ids []string) ([]string, error) {
	ids = workDomainIDs(ids)
	if !s.discovered.Load() {
		return ids, nil
	}
	since := agentEventsHorizon(s.Since())
	pending, err := workDomainSet(ctx, s.c.reader(), `SELECT j.value id FROM json_each(?1) j
 WHERE EXISTS(SELECT 1 FROM agent_events_dirty d WHERE d.conversation_id=j.value)
 OR EXISTS(SELECT 1 FROM conversations c WHERE c.id=j.value AND (?2='' OR COALESCE(c.ended_at,'')='' OR c.ended_at>=?2)
  AND NOT EXISTS(SELECT 1 FROM agent_events_conversations l WHERE l.conversation_id=j.value))`, jsonText(ids), since)
	s.inventory.Lock()
	for _, id := range ids {
		if s.jobs[id] != nil {
			pending[id] = true
		}
	}
	s.inventory.Unlock()
	return sortedKeys(pending), err
}

// PendingPage is labelsSession.PendingPage over the events queue.
func (s *agentEventsSession) PendingPage(ctx context.Context, after string, limit int, excluded func(workMember) bool) (workDomainPage, error) {
	if !s.discovered.Load() {
		return workDomainPage{Rediscover: true}, nil
	}
	return pendingPage(ctx, s.c.reader(), "agent_events_dirty", &s.inventory, &s.queue, func(id string) (string, bool) {
		job := s.jobs[id]
		if job == nil {
			return "", false
		}
		return job.token, true
	}, after, limit, excluded)
}

// agentEventsReadyFor is which selected conversations have current events by
// the ledger alone: built at a horizon covering since's, not dirty, and
// every row within the horizon scored by identity from a known input. A
// structure change that carries no dirty mark is only seen by discovery.
func (c *Catalog) agentEventsReadyFor(ctx context.Context, ids []string, identity string, since time.Time) ([]string, error) {
	current, err := c.agentEventsCurrentFor(ctx, ids, identity, agentEventsHorizon(since), false)
	return sortedKeys(current), err
}

// agentEventsCurrentFor is agentEventsReadyFor's set, and with unscoped the
// conversations that need nothing either: gone without rows or a mark, or
// over before the horizon with nothing changed (discovery skips those).
func (c *Catalog) agentEventsCurrentFor(ctx context.Context, ids []string, identity, since string, unscoped bool) (map[string]bool, error) {
	ids = workDomainIDs(ids)
	if len(ids) == 0 {
		return map[string]bool{}, nil
	}
	if version, err := c.metaValue(ctx, "agent_events_ledger_version"); err != nil || version != agentEventsLedgerVersion {
		return map[string]bool{}, nil
	}
	current, err := workDomainSet(ctx, c.reader(), `SELECT l.conversation_id id FROM agent_events_conversations l
 WHERE l.conversation_id IN (SELECT value FROM json_each(?1))
 AND NOT EXISTS(SELECT 1 FROM agent_events_dirty d WHERE d.conversation_id=l.conversation_id)
 AND (l.since=?3 OR NOT (l.since='~' OR ?3='' OR l.since<>'' AND ?3<l.since))
 AND NOT EXISTS(SELECT 1 FROM agent_event_messages m WHERE m.conversation_id=l.conversation_id AND (m.model<>?2 OR m.input_hash IS NULL)
  AND (?3='' OR m.occurred_at IS NULL OR m.occurred_at>=?3))`, jsonText(ids), identity, since)
	if err != nil || !unscoped {
		return current, err
	}
	other, err := workDomainSet(ctx, c.reader(), `SELECT j.value id FROM json_each(?1) j
 WHERE NOT EXISTS(SELECT 1 FROM agent_events_dirty d WHERE d.conversation_id=j.value) AND (
  NOT EXISTS(SELECT 1 FROM conversations c WHERE c.id=j.value)
   AND NOT EXISTS(SELECT 1 FROM agent_events_conversations l WHERE l.conversation_id=j.value)
   AND NOT EXISTS(SELECT 1 FROM agent_event_messages m WHERE m.conversation_id=j.value)
  OR ?2<>'' AND EXISTS(SELECT 1 FROM conversations c WHERE c.id=j.value AND COALESCE(c.ended_at,'')<>'' AND c.ended_at<?2))`, jsonText(ids), since)
	for id := range other {
		current[id] = true
	}
	return current, err
}

// Run scores the selected conversations, in the given order, through the
// classic pipeline with the session's fixed split and warm workers. limit,
// when set, bounds the messages scored (whole conversations; the rest stay
// pending). It takes the pass's locks for the call (errAgentEventsRunning
// while another run holds them). Preparation, inference and the writer have
// all joined when it returns; a stopped step kills its workers first.
func (s *agentEventsSession) Run(ctx context.Context, ids []string, limit int) (workDomainStep, error) {
	var step workDomainStep
	order := workDomainOrder(ids)
	ids = workDomainIDs(ids)
	if len(ids) == 0 {
		return step, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.discovered.Load() {
		step.Remaining, step.Rediscover = ids, true
		return step, nil
	}
	c := s.c
	unlock, err := c.agentEventsInvocation()
	if err != nil {
		return step, err
	}
	defer unlock()
	if err := s.checkModel(); err != nil {
		s.discovered.Store(false)
		return step, err
	}
	rediscover, err := s.refresh(ctx, ids)
	if err != nil {
		return step, err
	}
	if rediscover {
		s.discovered.Store(false)
		step.Remaining, step.Rediscover = ids, true
		return step, nil
	}
	jobs := []*agentEventsConversation{}
	for _, id := range order {
		if job := s.jobs[id]; job != nil {
			jobs = append(jobs, job)
		}
	}
	stats := &AgentEventsRunStats{Conversations: len(jobs)}
	s.last = stats
	if s.beforeRun != nil {
		s.beforeRun(ctx)
	}
	stored := map[string]bool{}
	if len(jobs) > 0 {
		err = s.execute(ctx, jobs, limit, stats, stored)
	}
	read := context.WithoutCancel(ctx)
	tokens, terr := agentEventsDirtyTokens(read, c.reader(), ids)
	current, cerr := c.agentEventsCurrentFor(read, ids, s.verifier.identity, agentEventsHorizon(s.since), true)
	if err == nil {
		err = errors.Join(terr, cerr)
	}
	attempted := map[string]bool{}
	for _, job := range jobs {
		attempted[job.id] = true
	}
	for _, id := range ids {
		switch {
		case stored[id] && tokens[id] != "":
			// Stored, and changed again meanwhile: it stays enrolled for the
			// next refresh to adopt the new token.
			step.Superseded = append(step.Superseded, id)
		case stored[id] && current[id]:
			s.setJob(id, nil)
			step.Done = append(step.Done, id)
		case stored[id]:
			step.Failed = append(step.Failed, id)
		case !attempted[id]:
			if tokens[id] == "" && current[id] {
				step.Done = append(step.Done, id)
			} else {
				step.Remaining = append(step.Remaining, id)
			}
		case err == nil && !stats.Cut:
			// Its units could not be built: it keeps its mark.
			step.Failed = append(step.Failed, id)
		default:
			step.Remaining = append(step.Remaining, id)
		}
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return step, err
}

// execute is RunAgentEvents' pipeline over jobs with the session's verifier,
// context and split. At one CPU, preparation and inference take turns.
func (s *agentEventsSession) execute(parent context.Context, jobs []*agentEventsConversation, limit int, stats *AgentEventsRunStats, stored map[string]bool) error {
	c, verifier := s.c, s.verifier
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	started := time.Now()
	progress := labelingProgress(nil, started)
	opts := AgentEventsRunOptions{Limit: limit, Since: s.since}
	status := newAgentEventsProgress(ctx, agentEventsCounts{}, started, len(jobs), false)
	var gate chan struct{}
	if s.serial {
		gate = make(chan struct{}, 1)
	}
	hold := func(ctx context.Context) (func(), error) {
		if gate == nil {
			return func() {}, nil
		}
		select {
		case gate <- struct{}{}:
			return func() { <-gate }, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	var runner *labelrt.Runner
	release := func() {}
	stopPool := func() {
		if runner != nil {
			c.labelers.Kill(runner)
			release()
			runner, release = nil, func() {}
		}
	}
	defer func() {
		if parent.Err() != nil {
			// A stop: nothing queued at the workers may run past the grant.
			stopPool()
		}
		release()
	}()
	infer := func(pending []*agentEventsConversation) error {
		framed := agentEventFramed(pending)
		if len(framed) == 0 {
			return nil
		}
		done, err := hold(ctx)
		if err != nil {
			return err
		}
		defer done()
		status.stage("scoring", true)
		defer status.stage("scoring", false)
		if runner == nil {
			if runner, release, err = c.labelerRunner(ctx, admittedLabeler(ctx, s.labels), verifier.model, agentEventsRunnerConfig(verifier, s.labels, progress)); err != nil {
				runner, release = nil, func() {}
				return err
			}
		}
		status.setDevice(runner.Pool().Ready().Worker.Device)
		inferStarted := time.Now()
		outputs, err := runner.RunWithProgress(ctx, framed, agentEventRunOutputs, s.labels.BatchTokens, func(n int) {
			status.scored(n, runner.Pool().Ready().Worker.Device)
		})
		stats.Infer += time.Since(inferStarted)
		if err != nil {
			stopPool()
			return err
		}
		assignAgentEventOutputs(pending, outputs)
		return nil
	}
	builders := make([]*labelsItemBuilder, s.prep)
	var storedMu sync.Mutex
	pipeline := agentEventsPipeline{
		workers: s.prep,
		opts:    opts, stats: stats, progress: progress, status: status,
		build: func(ctx context.Context, worker int, job *agentEventsConversation) (conversation *agentEventsConversation, err error) {
			done, err := hold(ctx)
			if err != nil {
				return nil, err
			}
			defer done()
			if builders[worker] == nil {
				builders[worker] = s.builder.fork(ctx)
			}
			opFrom(ctx).RunIO(func() {
				conversation, err = c.agentEventsConversation(ctx, builders[worker], verifier, job, s.since)
			})
			return conversation, err
		},
		infer: infer,
		store: func(conversation *agentEventsConversation, written *AgentEventsRunStats) (err error) {
			opFrom(ctx).RunIO(func() {
				err = c.storeAgentEvents(ctx, verifier.identity, []*agentEventsConversation{conversation}, written)
			})
			if err == nil {
				storedMu.Lock()
				stored[conversation.id] = true
				storedMu.Unlock()
			}
			return err
		},
	}
	if err := pipeline.run(ctx, cancel, jobs); err != nil {
		stopPool()
		return err
	}
	return nil
}

// workDomainSet is the id column of query's rows.
func workDomainSet(ctx context.Context, q queryer, query string, args ...any) (map[string]bool, error) {
	set := map[string]bool{}
	rows, err := queryMapsContext(ctx, q, query, args...)
	for _, row := range rows {
		set[firstString(row["id"])] = true
	}
	return set, err
}

// agentEventsDirtyTokens reads the current dirty tokens of ids.
func agentEventsDirtyTokens(ctx context.Context, q queryer, ids []string) (map[string]string, error) {
	tokens := map[string]string{}
	rows, err := queryMapsContext(ctx, q, `SELECT conversation_id,token FROM agent_events_dirty WHERE conversation_id IN (SELECT value FROM json_each(?))`, jsonText(ids))
	for _, row := range rows {
		tokens[firstString(row["conversation_id"])] = firstString(row["token"])
	}
	return tokens, err
}

// refresh reads the selected conversations' live state into the context and
// inventory, as agentEventsWork would decide them. A change since discovery
// has every live fork marked before its token is adopted. It reports whether
// the context must be rebuilt instead (a mirror representative changed, or
// a selected conversation was deleted).
func (s *agentEventsSession) refresh(ctx context.Context, ids []string) (bool, error) {
	c, p := s.c, s.builder.p
	live, stale, err := c.refreshLabelingContext(ctx, s.builder, ids)
	if err != nil || stale {
		return stale, err
	}
	since := agentEventsHorizon(s.since)
	ledger, err := queryMapsContext(ctx, c.reader(), `SELECT j.value id,COALESCE(l.structure,'') structure,l.conversation_id IS NOT NULL built,COALESCE(l.since,'') horizon,
 COALESCE((SELECT token FROM agent_events_dirty d WHERE d.conversation_id=j.value),'') token,
 EXISTS(SELECT 1 FROM agent_event_messages m WHERE m.conversation_id=j.value) rows,
 EXISTS(SELECT 1 FROM agent_event_messages m WHERE m.conversation_id=j.value AND (m.model<>?2 OR m.input_hash IS NULL) AND (?3='' OR m.occurred_at IS NULL OR m.occurred_at>=?3)) other
 FROM json_each(?1) j LEFT JOIN agent_events_conversations l ON l.conversation_id=j.value`, jsonText(ids), s.verifier.identity, since)
	if err != nil {
		return false, err
	}
	changes := []string{}
	for _, row := range ledger {
		id := firstString(row["id"])
		info := live.conversations[id]
		token, built, horizon := firstString(row["token"]), integer(row["built"]) != 0, firstString(row["horizon"])
		if info == nil {
			if p.conversations[id] != nil || built || integer(row["rows"]) != 0 || token != "" {
				return true, nil
			}
			continue
		}
		p.conversations[id] = info
		structure := labelsStructure(p, s.builder.sessions, id)
		job := s.jobs[id]
		changed := token != "" || built && firstString(row["structure"]) != structure
		if changed && (job == nil || job.token != token || job.structure != structure) {
			changes = append(changes, id)
		}
		ended := live.ended[id]
		skip := token == "" && since != "" && ended != "" && ended < since
		widened := built && horizon != since && (horizon == "~" || since == "" || horizon != "" && since < horizon)
		if !skip && (!built || changed || integer(row["other"]) != 0 || widened) {
			s.setJob(id, &agentEventsConversation{id: id, token: token, structure: structure, since: since})
		} else {
			s.setJob(id, nil)
		}
	}
	return false, s.markForks(ctx, changes)
}

// markForks gives every live fork of sources, transitively, a dirty mark of
// its own (keeping one it has), as agentEventsWork does, and enrolls those in
// the context. A fork created after discovery keeps its mark for a later
// step or discovery.
func (s *agentEventsSession) markForks(ctx context.Context, sources []string) error {
	if len(sources) == 0 {
		return nil
	}
	rows, err := queryMapsContext(ctx, s.c.reader(), `WITH RECURSIVE f(id) AS (
 SELECT c.id FROM conversations c WHERE c.parent_id IN (SELECT value FROM json_each(?))
 UNION SELECT c.id FROM conversations c JOIN f ON c.parent_id=f.id)
 SELECT id FROM f`, jsonText(sources))
	if err != nil || len(rows) == 0 {
		return err
	}
	forks := make([]string, len(rows))
	for i, row := range rows {
		forks[i] = firstString(row["id"])
	}
	tokens := map[string]string{}
	if err := s.c.writeTransaction(ctx, "mark forks of changed agent event conversations", func(tx *sql.Tx) error {
		for _, id := range forks {
			var token string
			if err := tx.QueryRowContext(ctx, `INSERT INTO agent_events_dirty(conversation_id,token) VALUES(?,hex(randomblob(8)))
				ON CONFLICT(conversation_id) DO UPDATE SET token=agent_events_dirty.token RETURNING token`, id).Scan(&token); err != nil {
				return err
			}
			tokens[id] = token
		}
		return nil
	}); err != nil {
		return err
	}
	p := s.builder.p
	since := agentEventsHorizon(s.since)
	for _, id := range forks {
		if p.conversations[id] != nil && s.jobs[id] == nil {
			s.setJob(id, &agentEventsConversation{id: id, token: tokens[id], structure: labelsStructure(p, s.builder.sessions, id), since: since})
		}
	}
	return nil
}
