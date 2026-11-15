package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	labelrt "github.com/gbdubs/pharos/internal/labeler"
)

// A labels session labels a selected population with the classic pipeline
// (labelsRun): its model, item context and discovered inventory are kept
// between steps, so a small batch neither reloads the model nor rescans the
// library. Its safety rests on the ledger's own state, never on the session:
//
//   - Discover is RunLabels' discovery over the whole library. Before any
//     selected label is stored, every conversation discovery wants for a
//     change (its followers, and those wanted for a model or contract change
//     or a structure change) has a dirty mark of its own, and deleted
//     conversations lose their labels. One wanted only for missing labels
//     needs none: the ledger shows it unbuilt until it is, and a mark would
//     make classic runs treat it as changed. A historical dependent outside
//     the selection stays discoverable by any later run, this binary's or an
//     older one's.
//   - A step adopts a dirty token only after marking the followers of the
//     change it stands for. A conversation whose token or structure differs
//     from what discovery captured has its followers marked first, in their
//     own committed transaction. Followers are discovery's graph plus the
//     forks and re-sends of every conversation a step refreshed since; a
//     labels run by anyone else in between (which may have built a new
//     re-sender this graph lacks) sends the session back to discovery.
//   - A step stores with the token it adopted, so a mark written while it
//     computes survives (Superseded), reports Done only what the ledger shows
//     current, and never writes labels_generation.
//
// The locks RunLabels takes are taken per call, so a classic run (or pharos
// labels run) may run between steps.
type labelsSession struct {
	c        *Catalog
	settings LabelerSettings
	alloc    workAllocation

	// mu serializes Discover and Run. inventory guards jobs and queue for
	// PendingFor and PendingPage, which a selector calls while a step runs;
	// only Discover and Run change them, under both locks.
	mu, inventory sync.Mutex
	discovered    atomic.Bool
	// Set by the first Discover; fixed for the session's life.
	model              *labelrt.Model
	identity, manifest string
	prep               int
	serial             bool
	// Discovered state, replaced by each Discover.
	builder   *labelsItemBuilder
	all       bool
	followers map[string][]string
	jobs      map[string]*labelsJob
	queue     workDomainQueue
	// watermark is the ledger's latest built_at after this session's last
	// call: another value at the next one means another run stored labels.
	watermark string
	// Counts for tests and diagnostics: item contexts built, discoveries,
	// and the last step's run statistics.
	contexts, discoveries int
	last                  *LabelsRunStats
	// beforeRun, in tests, runs after a step's refresh and before it labels.
	beforeRun func(context.Context)
}

var errLabelsSessionStale = errors.New("the labels model changed; start a new labels session")

// labelsSplit divides a fixed CPU allocation between preparation workers and
// the model worker's native threads, for the labels and agent events
// sessions. The worker is charged the intra-op threads it is started with,
// within all but one CPU, and preparation gets the rest (at most four). That
// holds for a worker that may use the GPU too: on the auto backend it can
// fall back to the CPU after its handshake, at those threads, and a session
// never retunes. Only gpuOnly (the mlx backend, which has no CPU fallback)
// starts the worker at one thread, charged one CPU for its driver. One CPU
// runs preparation and inference in turn (serial), never at once.
//
// The split is cooperative, not enforced by the OS: it counts preparation
// workers and the worker's configured intra-op threads. The pipeline's
// coordinator and writer, the runtime's other threads and the GPU driver's
// are not counted.
func labelsSplit(cpus, threads int, gpuOnly bool) (prep, inference int, serial bool) {
	cpus, threads = max(1, cpus), max(1, threads)
	switch {
	case cpus == 1:
		return 1, 1, true
	case gpuOnly:
		return min(4, cpus-1), 1, false
	}
	inference = min(threads, cpus-1)
	return min(4, cpus-inference), inference, false
}

// labelsGPUOnly is whether a model's worker can only run on the GPU.
func labelsGPUOnly(model *labelrt.Model, settings LabelerSettings) bool {
	return model.MayUseGPU(settings.Backend) && settings.Backend == "mlx"
}

// workDomainQueue is a session's library queue: discovery's order (newest
// first), fixed until the next discovery, over the conversations still
// enrolled; conversations a step enrolled that discovery did not see follow
// it. Memory grows only by those. Callers hold the session's inventory lock.
type workDomainQueue struct {
	order    []string
	position map[string]int
	// head: no conversation before it is enrolled.
	head int
}

func newWorkDomainQueue(order []string) workDomainQueue {
	position := make(map[string]int, len(order))
	for i, id := range order {
		position[id] = i
	}
	return workDomainQueue{order: order, position: position}
}

// enrolled notes that id joined the inventory: again, in its place, or
// after discovery's order when discovery did not see it.
func (q *workDomainQueue) enrolled(id string) {
	i, ok := q.position[id]
	if !ok {
		if q.position == nil {
			return // closed
		}
		i = len(q.order)
		q.order, q.position[id] = append(q.order, id), i
	}
	q.head = min(q.head, i)
}

// page is at most limit enrolled, not excluded conversations after the one
// named after ("" or one not in the order: from the head), and whether none
// is left anywhere in the order.
func (q *workDomainQueue) page(after string, limit int, enrolled, excluded func(string) bool) (ids []string, exhausted bool) {
	candidate := func(id string) bool { return enrolled(id) && (excluded == nil || !excluded(id)) }
	start := q.head
	if i, ok := q.position[after]; ok && i+1 > start {
		start = i + 1
	}
	for i := start; i < len(q.order) && len(ids) < max(1, limit); i++ {
		id := q.order[i]
		if i == q.head && !enrolled(id) {
			q.head++
			continue
		}
		if candidate(id) {
			ids = append(ids, id)
		}
	}
	if len(ids) > 0 {
		return ids, false
	}
	for i := q.head; i < start && i < len(q.order); i++ {
		if candidate(q.order[i]) {
			return nil, false
		}
	}
	return nil, true
}

// workDomainPage is a bounded page of a session's library queue.
type workDomainPage struct {
	// Units are the next candidates in the session's order, each at the
	// token the session holds for it (a step's refresh may adopt a newer one),
	// then conversations marked after discovery at their marks' tokens.
	Units []workMember
	// Rediscover: the session has no inventory yet. It is never a readiness
	// claim.
	Rediscover bool
}

// IDs are the page's conversations.
func (page workDomainPage) IDs() []string {
	ids := make([]string, len(page.Units))
	for i, unit := range page.Units {
		ids[i] = unit.ID
	}
	return ids
}

// pendingPage is PendingPage for a session's queue; token is an enrolled
// conversation's token (ok false when it is not enrolled). Once the queue
// holds no candidate it reads the dirty table in primary-key order for
// conversations marked after discovery: a step's refresh enrolls them as it
// does any change (marking their followers first), so an ingest does not
// send a draining session back to discovery. Enrolled rows are skipped;
// excluded ones are bounded by the caller.
func pendingPage(ctx context.Context, q queryer, dirty string, inventory *sync.Mutex, queue *workDomainQueue, token func(string) (string, bool), after string, limit int, excluded func(workMember) bool) (workDomainPage, error) {
	enrolled := func(id string) bool { _, ok := token(id); return ok }
	member := func(id string) workMember { revision, _ := token(id); return workMember{ID: id, Revision: revision} }
	inventory.Lock()
	ids, exhausted := queue.page(after, limit, enrolled, func(id string) bool { return excluded != nil && excluded(member(id)) })
	page := workDomainPage{}
	for _, id := range ids {
		page.Units = append(page.Units, member(id))
	}
	inventory.Unlock()
	if !exhausted {
		return page, nil
	}
	for cursor := ""; len(page.Units) < max(1, limit); {
		rows, err := queryMapsContext(ctx, q, `SELECT conversation_id id,token FROM `+dirty+` WHERE conversation_id>? ORDER BY conversation_id LIMIT 256`, cursor)
		if err != nil {
			return workDomainPage{}, err
		}
		inventory.Lock()
		for _, row := range rows {
			cursor = firstString(row["id"])
			late := workMember{ID: cursor, Revision: firstString(row["token"])}
			if !enrolled(late.ID) && (excluded == nil || !excluded(late)) && len(page.Units) < max(1, limit) {
				page.Units = append(page.Units, late)
			}
		}
		inventory.Unlock()
		if len(rows) < 256 {
			break
		}
	}
	return page, nil
}

// newLabelsSession checks that labeling can run; it reads nothing from the
// catalog. The allocation is the session's fixed envelope: a different one
// needs a new session.
func (c *Catalog) newLabelsSession(settings LabelerSettings, alloc workAllocation) (*labelsSession, error) {
	settings = settings.withAssets()
	if settings.ModelDir == "" {
		return nil, fmt.Errorf("%w: the model is %s; run pharos labels fetch, or set labels_model_dir (PHAROS_LABELS_MODEL_DIR)", labelrt.ErrModelUnavailable, labelsAssetStatus(labelsAssets.Model))
	}
	if settings.Worker == "" {
		return nil, fmt.Errorf("%w: set labels_worker (or PHAROS_LABELS_WORKER)", labelrt.ErrModelUnavailable)
	}
	alloc.CPUs = max(1, alloc.CPUs)
	return &labelsSession{c: c, settings: settings, alloc: alloc}, nil
}

// Allocation is the CPUs every step of this session uses.
func (s *labelsSession) Allocation() workAllocation { return s.alloc }

// Identity is the model identity labels are stored under ("" before Discover).
func (s *labelsSession) Identity() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.identity
}

// Discovered reports whether steps can run; false asks for Discover.
func (s *labelsSession) Discovered() bool { return s.discovered.Load() }

// setJob enrolls (or with nil, drops) a conversation's job.
func (s *labelsSession) setJob(id string, job *labelsJob) {
	s.inventory.Lock()
	defer s.inventory.Unlock()
	if job == nil {
		delete(s.jobs, id)
	} else {
		s.jobs[id] = job
		s.queue.enrolled(id)
	}
}

// Close drops the session's context. The model's workers stay warm in the
// process's labeler manager for their idle period, as after RunLabels.
func (s *labelsSession) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.discovered.Store(false)
	s.inventory.Lock()
	defer s.inventory.Unlock()
	s.builder, s.jobs, s.followers, s.queue = nil, nil, nil, workDomainQueue{}
}

func labelsManifestDigest(dir string) string {
	raw, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:12])
}

// Discover is the session's broad phase, to be admitted like any step: it
// loads the model (once), reads the item context, and runs RunLabels'
// discovery over the whole library, which durably marks followers and removes
// deleted conversations' labels.
func (s *labelsSession) Discover(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.c
	release, err := c.labelsInvocation()
	if err != nil {
		return err
	}
	defer release()
	if err := s.checkModel(); err != nil {
		return err
	}
	if err := c.ensureLabelsSchema(ctx); err != nil {
		return err
	}
	if err := c.recordLabelsModel(ctx, s.model, s.identity); err != nil {
		return err
	}
	// Execution evidence another writer changed reaches this ledger's dirty
	// marks before discovery reads them, as in RunLabels.
	if err := c.propagateExecutionChanges(ctx); err != nil {
		return err
	}
	s.discovered.Store(false)
	builder, err := newLabelsItemBuilder(ctx, c.reader())
	if err != nil {
		return err
	}
	s.contexts++
	found, err := c.labelsDiscover(ctx, builder, s.identity, LabelsRunOptions{}, &LabelsRunStats{}, true)
	if err != nil {
		return err
	}
	s.discoveries++
	jobs := make(map[string]*labelsJob, len(found.jobs))
	order := make([]string, len(found.jobs))
	for i, job := range found.jobs {
		jobs[job.conversation], order[i] = job, job.conversation
	}
	if s.watermark, err = labelsWatermark(ctx, c.reader()); err != nil {
		return err
	}
	s.inventory.Lock()
	s.builder, s.all, s.followers, s.jobs, s.queue = builder, found.all, found.followers, jobs, newWorkDomainQueue(order)
	s.inventory.Unlock()
	s.discovered.Store(true)
	return nil
}

// labelingInputsGeneration is the native generation of the inputs a model
// session's discovery reads that no dirty mark carries: the record writer's
// (conversations, their structure and lineage, and the mirror
// representatives they decide), the tool ledger's (identity links, Git
// merges) and the execution evidence's (execution_source_generation: claims,
// launches and observations a writer may change without marking dependents
// until the next propagation). Each moves in its writer's own transaction.
// Message, writing, session and reasoning changes mark the dirty tables
// themselves (itemDirtyTriggers), so a background writing or reasoning pass
// does not move it, and neither do a propagation's marks nor a derivative's
// publication. One read: a library selection compares it with the
// generation its session's discovery read before it ran, so a page is never
// empty for an inventory older than the library's inputs.
func (c *Catalog) labelingInputsGeneration(ctx context.Context) (string, error) {
	var generation string
	err := c.reader().QueryRowContext(ctx, `SELECT COALESCE((SELECT value FROM meta WHERE key='authorship_inputs_generation'),'')||':'||
 COALESCE((SELECT value FROM meta WHERE key='tool_ledger_generation'),'')||':'||
 COALESCE((SELECT value FROM execution_source_generation WHERE id=1),'')`).Scan(&generation)
	return generation, err
}

// labelsWatermark is the latest time any run stored a conversation's labels.
// built_at has a fixed width, so the text order is the time order.
func labelsWatermark(ctx context.Context, q queryer) (string, error) {
	rows, err := queryMapsContext(ctx, q, `SELECT COALESCE(MAX(built_at),'') at FROM message_labels_conversations`)
	if err != nil || len(rows) == 0 {
		return "", err
	}
	return firstString(rows[0]["at"]), nil
}

// checkModel loads the model on first use and afterwards refuses a session
// whose model files changed on disk.
func (s *labelsSession) checkModel() error {
	if s.model != nil {
		if labelsManifestDigest(s.settings.ModelDir) != s.manifest {
			return errLabelsSessionStale
		}
		return nil
	}
	manifest := labelsManifestDigest(s.settings.ModelDir)
	model, err := labelrt.LoadModel(s.settings.ModelDir, s.settings.Variant)
	if err != nil {
		return err
	}
	s.model, s.identity, s.manifest = model, labelsModelIdentity(model), manifest
	var threads int
	s.prep, threads, s.serial = labelsSplit(s.alloc.CPUs, s.settings.Threads, labelsGPUOnly(model, s.settings))
	s.settings.Threads, s.settings.Workers = threads, 1
	return nil
}

// PendingFor is which selected conversations may still need labels: in the
// discovered inventory, dirty, or never built. Indexed lookups only; before
// Discover every selected conversation is pending.
func (s *labelsSession) PendingFor(ctx context.Context, ids []string) ([]string, error) {
	ids = workDomainIDs(ids)
	if !s.discovered.Load() {
		return ids, nil
	}
	pending, err := workDomainPending(ctx, s.c.reader(), `SELECT j.value id FROM json_each(?) j
 WHERE EXISTS(SELECT 1 FROM message_labels_dirty d WHERE d.conversation_id=j.value)
 OR (EXISTS(SELECT 1 FROM conversations c WHERE c.id=j.value) AND NOT EXISTS(SELECT 1 FROM message_labels_conversations l WHERE l.conversation_id=j.value))`, ids)
	s.inventory.Lock()
	for _, id := range ids {
		if s.jobs[id] != nil {
			pending[id] = true
		}
	}
	s.inventory.Unlock()
	return sortedKeys(pending), err
}

// PendingPage is the next page of the library's pending conversations, in
// discovery's order, for library-wide demand: in memory, with one bounded
// read of the dirty table once the queue holds no candidate. It claims
// nothing; before Discover it asks for one.
func (s *labelsSession) PendingPage(ctx context.Context, after string, limit int, excluded func(workMember) bool) (workDomainPage, error) {
	if !s.discovered.Load() {
		return workDomainPage{Rediscover: true}, nil
	}
	return pendingPage(ctx, s.c.reader(), "message_labels_dirty", &s.inventory, &s.queue, func(id string) (string, bool) {
		job := s.jobs[id]
		if job == nil {
			return "", false
		}
		return job.token, true
	}, after, limit, excluded)
}

// labelsReadyFor is which selected conversations have current labels by the
// ledger alone: built, not dirty, every unit labeled, all by identity under
// the current contract. Mirror and structure changes that carry no dirty mark
// are only seen by discovery.
func (c *Catalog) labelsReadyFor(ctx context.Context, ids []string, identity string) ([]string, error) {
	ids = workDomainIDs(ids)
	if len(ids) == 0 || !c.hasLabelsLedger() {
		return nil, nil
	}
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT l.conversation_id id FROM message_labels_conversations l
 WHERE l.conversation_id IN (SELECT value FROM json_each(?))
 AND NOT EXISTS(SELECT 1 FROM message_labels_dirty d WHERE d.conversation_id=l.conversation_id)
 AND (SELECT COUNT(*) FROM message_labels m WHERE m.conversation_id=l.conversation_id)=l.units
 AND NOT EXISTS(SELECT 1 FROM message_labels m WHERE m.conversation_id=l.conversation_id AND (m.model<>? OR m.contract<>?))`, jsonText(ids), identity, labelsContract)
	ready := []string{}
	for _, row := range rows {
		ready = append(ready, firstString(row["id"]))
	}
	sort.Strings(ready)
	return ready, err
}

// labelsCurrentFor is which of ids need no labels by the ledger alone:
// labelsReadyFor's, and conversations that do not exist and have neither
// labels nor a mark.
func (c *Catalog) labelsCurrentFor(ctx context.Context, ids []string, identity string) (map[string]bool, error) {
	ready, err := c.labelsReadyFor(ctx, ids, identity)
	if err != nil {
		return nil, err
	}
	current, err := workDomainPending(ctx, c.reader(), `SELECT j.value id FROM json_each(?) j
 WHERE NOT EXISTS(SELECT 1 FROM conversations c WHERE c.id=j.value)
 AND NOT EXISTS(SELECT 1 FROM message_labels_conversations l WHERE l.conversation_id=j.value)
 AND NOT EXISTS(SELECT 1 FROM message_labels m WHERE m.conversation_id=j.value)
 AND NOT EXISTS(SELECT 1 FROM message_labels_dirty d WHERE d.conversation_id=j.value)`, ids)
	for _, id := range ready {
		current[id] = true
	}
	return current, err
}

// workDomainOrder is ids deduplicated in their given order.
func workDomainOrder(ids []string) []string {
	order := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			order = append(order, id)
		}
	}
	return order
}

// Run labels the selected conversations, in the given order, through the
// classic pipeline with the session's fixed allocation and warm model
// workers. Callers bound the selection; limit, when set, also bounds the
// messages labeled (a cut conversation stays pending). It takes RunLabels'
// locks for the call (errLabelsRunning while another run holds them), and
// joins its preparation, inference and writer before returning.
func (s *labelsSession) Run(ctx context.Context, ids []string, limit int) (workDomainStep, error) {
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
	release, err := c.labelsInvocation()
	if err != nil {
		return step, err
	}
	defer release()
	if err := s.checkModel(); err != nil {
		s.discovered.Store(false)
		return step, err
	}
	// Another run stored labels since this session's last call: it may have
	// built a conversation that re-sends from (or forks) one this session
	// will change, an edge discovery's graph lacks.
	watermark, err := labelsWatermark(ctx, c.reader())
	if err != nil {
		return step, err
	}
	rediscover := watermark != s.watermark
	if !rediscover {
		if rediscover, err = s.refresh(ctx, ids); err != nil {
			// refresh may have adopted tokens and structures whose followers
			// it did not get to mark: only discovery restores that guarantee.
			s.discovered.Store(false)
			return step, err
		}
	}
	if rediscover {
		s.discovered.Store(false)
		step.Remaining, step.Rediscover = ids, true
		return step, nil
	}
	// This step's own writes move the watermark; it holds the locks until it
	// has read it again.
	defer func() {
		if at, werr := labelsWatermark(context.WithoutCancel(ctx), c.reader()); werr == nil {
			s.watermark = at
		} else {
			s.discovered.Store(false)
		}
	}()
	jobs := []*labelsJob{}
	for _, id := range order {
		if job := s.jobs[id]; job != nil {
			jobs = append(jobs, job)
		}
	}
	stored, failed := map[string]bool{}, map[string]bool{}
	var storedMu sync.Mutex
	stats := &LabelsRunStats{}
	run := &labelsRun{c: c, settings: admittedLabeler(ctx, s.settings), opts: LabelsRunOptions{Limit: limit, Conversations: ids}, model: s.model, identity: s.identity,
		stats: stats, progress: labelingProgress(nil, time.Now()), prep: s.prep,
		stored: func(job *labelsJob, complete bool) {
			storedMu.Lock()
			defer storedMu.Unlock()
			stored[job.conversation] = complete
		},
		failed: func(job *labelsJob) {
			storedMu.Lock()
			defer storedMu.Unlock()
			failed[job.conversation] = true
		}}
	if s.serial {
		run.serial = make(chan struct{}, 1)
	}
	s.last = stats
	if s.beforeRun != nil {
		s.beforeRun(ctx)
	}
	if len(jobs) > 0 {
		reportWork(ctx, "labeling messages", "conversation", 0, len(jobs))
		err = run.execute(ctx, s.builder, jobs)
	}
	storedMu.Lock()
	defer storedMu.Unlock()
	read := context.WithoutCancel(ctx)
	tokens, terr := labelsDirtyTokens(read, c.reader(), ids)
	current, cerr := c.labelsCurrentFor(read, ids, s.identity)
	if err == nil {
		err = errors.Join(terr, cerr)
	}
	attempted := map[string]bool{}
	for _, job := range jobs {
		attempted[job.conversation] = true
	}
	for _, id := range ids {
		complete, wrote := stored[id]
		switch {
		case wrote && complete && tokens[id] != "":
			// Stored, and changed again meanwhile: it stays enrolled for the
			// next refresh to adopt the new token.
			step.Superseded = append(step.Superseded, id)
		case wrote && complete && current[id]:
			s.setJob(id, nil)
			step.Done = append(step.Done, id)
		case wrote && complete, failed[id] && ctx.Err() == nil:
			// Stored, yet the ledger does not show it current (a label or
			// its conversation went missing): never a false Done. Or the run
			// gave up on it (unbuildable, or a message the worker could not
			// label), whether or not a limit applied.
			step.Failed = append(step.Failed, id)
		case !attempted[id]:
			// Current at refresh: nothing was wanted.
			if tokens[id] == "" && current[id] {
				step.Done = append(step.Done, id)
			} else {
				step.Remaining = append(step.Remaining, id)
			}
		case err == nil && limit == 0:
			// An unlimited pass that ended normally stored it with a failed
			// message, or could not build it: the domain keeps it pending.
			step.Failed = append(step.Failed, id)
		default:
			// Cut by the limit (or the run ended early): pending, and progress
			// when part of it was stored.
			step.Remaining = append(step.Remaining, id)
			step.Progress = step.Progress || wrote
		}
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return step, err
}

// labelsDirtyTokens reads the current dirty tokens of ids.
func labelsDirtyTokens(ctx context.Context, q queryer, ids []string) (map[string]string, error) {
	tokens := map[string]string{}
	rows, err := queryMapsContext(ctx, q, `SELECT conversation_id,token FROM message_labels_dirty WHERE conversation_id IN (SELECT value FROM json_each(?))`, jsonText(ids))
	for _, row := range rows {
		tokens[firstString(row["conversation_id"])] = firstString(row["token"])
	}
	return tokens, err
}

// refreshLabelingContext reads ids' conversation rows, agent sessions and
// reasoning state into a session's item context, in place, with the queries
// loadLabelingPass runs for the whole library: no preparation worker shares
// it between steps. It returns their conversation rows for the caller to
// compare with the context's before replacing them. stale reports a mirror
// representative change, after which any structure may differ, or a
// selected conversation whose execution dependency digest is not the one
// discovery resolved (new, gone, or its evidence changed): its lineage,
// copied history and occurrences are only resolved for the whole context.
// Either way the context must be rebuilt.
func (c *Catalog) refreshLabelingContext(ctx context.Context, builder *labelsItemBuilder, ids []string) (live map[string]*labelingConversation, stale bool, err error) {
	p := builder.p
	representative, err := c.authorshipRepresentatives(ctx)
	if err != nil || !maps.Equal(representative, p.representative) {
		return nil, err == nil, err
	}
	digests, err := executionDependencyDigestsFor(ctx, c.reader(), ids)
	if err != nil {
		return nil, false, err
	}
	for _, id := range ids {
		if digests[id] != p.executionDigests[id] {
			return nil, true, nil
		}
	}
	selected := ` WHERE id IN (SELECT value FROM json_each(?))`
	encoded := jsonText(ids)
	live = map[string]*labelingConversation{}
	rows, err := c.reader().QueryContext(ctx, labelingConversationsSQL+selected, encoded)
	if err != nil {
		return nil, false, err
	}
	for rows.Next() {
		info, err := scanLabelingConversation(rows)
		if err != nil {
			rows.Close()
			return nil, false, err
		}
		live[info.id] = info
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	sessions, err := queryMapsContext(ctx, c.reader(), labelingSessionsSQL+` WHERE conversation_id IN (SELECT value FROM json_each(?))`, encoded)
	if err != nil {
		return nil, false, err
	}
	current := map[string]bool{}
	reasoning, err := queryMapsContext(ctx, c.reader(), labelingReasoningSQL+` AND conversation_id IN (SELECT value FROM json_each(?))`, reasoningExtractionVersion, encoded)
	if err != nil {
		return nil, false, err
	}
	for _, row := range reasoning {
		current[firstString(row["conversation_id"])] = true
	}
	bySession := map[string][]string{}
	for id := range live {
		delete(p.mainSession, id)
	}
	for _, row := range sessions {
		conversation := firstString(row["conversation_id"])
		bySession[conversation] = append(bySession[conversation], labelsSessionEntry(row))
		p.addSession(row)
	}
	for id := range live {
		list := bySession[id]
		sort.Strings(list)
		builder.sessions[id] = list
		p.current[id] = current[id]
	}
	return live, false, nil
}

// follow records that follower follows source, once.
func (s *labelsSession) follow(source, follower string) {
	if !slices.Contains(s.followers[source], follower) {
		s.followers[source] = append(s.followers[source], follower)
	}
}

// refresh reads the selected conversations' live state into the session's
// context and inventory, as labelsDiscover would decide them. Their own
// parents and the conversations they re-send from gain them as followers.
// A change seen since discovery (a new token, another structure, a new
// conversation) has its followers durably marked before its token is
// adopted. It reports whether the context must be rebuilt instead: a mirror
// representative changed (every structure may change), a selected
// conversation's execution dependency digest moved (refreshLabelingContext)
// or a selected conversation was deleted (discovery removes its labels).
func (s *labelsSession) refresh(ctx context.Context, ids []string) (bool, error) {
	c, p := s.c, s.builder.p
	live, stale, err := c.refreshLabelingContext(ctx, s.builder, ids)
	if err != nil || stale {
		return stale, err
	}
	encoded := jsonText(ids)
	// A re-send recorded after discovery: the re-sender's own authorship
	// write marked it, and once a step builds it, it must follow its source.
	// Forward edges only, by message: indexed.
	reuses, err := queryMapsContext(ctx, c.reader(), `SELECT DISTINCT src.conversation_id source,m.conversation_id user
 FROM messages m JOIN message_authorship_spans s ON s.message_id=m.id JOIN messages src ON src.id=s.source_message_id
 WHERE m.conversation_id IN (SELECT value FROM json_each(?)) AND s.category=? AND src.conversation_id<>m.conversation_id`, encoded, spanResent)
	if err != nil {
		return false, err
	}
	ledger, err := queryMapsContext(ctx, c.reader(), `SELECT j.value id,COALESCE(l.structure,'') structure,l.conversation_id IS NOT NULL built,COALESCE(l.units,0) units,
 (SELECT COUNT(*) FROM message_labels m WHERE m.conversation_id=j.value) n,
 COALESCE((SELECT token FROM message_labels_dirty d WHERE d.conversation_id=j.value),'') token,
 EXISTS(SELECT 1 FROM message_labels m WHERE m.conversation_id=j.value AND (m.model<>? OR m.contract<>?)) other
 FROM json_each(?) j LEFT JOIN message_labels_conversations l ON l.conversation_id=j.value`, s.identity, labelsContract, encoded)
	if err != nil {
		return false, err
	}
	changes := []string{}
	for _, row := range ledger {
		id := firstString(row["id"])
		info := live[id]
		if info == nil {
			if p.conversations[id] != nil || integer(row["built"]) != 0 || integer(row["n"]) != 0 || firstString(row["token"]) != "" {
				return true, nil
			}
			continue // never existed: nothing to label
		}
		if info.parent != "" {
			s.follow(info.parent, id)
		}
		p.conversations[id] = info
		structure := labelsStructure(p, s.builder.sessions, id)
		token := firstString(row["token"])
		// Discovery's rule, with this conversation's own labels by another
		// model or contract standing for discovery's library-wide one.
		changed, want := labelsWant(labelsLedger{built: integer(row["built"]) != 0, structure: firstString(row["structure"]), token: token,
			labels: integer(row["n"]), units: integer(row["units"])}, structure, integer(row["other"]) != 0)
		job := s.jobs[id]
		if changed && (job == nil || job.token != token || job.structure != structure) {
			changes = append(changes, id)
		}
		// Every conversation discovery wanted for a model or contract change
		// stays wanted until stored; a later step needs its own reason.
		if want || s.all && job != nil {
			s.setJob(id, &labelsJob{conversation: id, token: token, structure: structure, dependency: p.executionDigests[id]})
		} else {
			s.setJob(id, nil)
		}
	}
	for _, row := range reuses {
		s.follow(firstString(row["source"]), firstString(row["user"]))
	}
	return false, s.markFollowers(ctx, changes)
}

// markFollowers gives every conversation that follows sources, transitively,
// a dirty mark of its own (keeping one it has), as labelsDiscover does, and
// enrolls it in the inventory. A follower already enrolled keeps the token
// discovery captured.
func (s *labelsSession) markFollowers(ctx context.Context, sources []string) error {
	p := s.builder.p
	visited := map[string]bool{}
	for _, id := range sources {
		visited[id] = true
	}
	followers := []string{}
	for queue := sources; len(queue) > 0; queue = queue[1:] {
		for _, follower := range s.followers[queue[0]] {
			if !visited[follower] && p.conversations[follower] != nil {
				visited[follower] = true
				followers = append(followers, follower)
				queue = append(queue, follower)
			}
		}
	}
	if len(followers) == 0 {
		return nil
	}
	tokens := map[string]string{}
	err := s.c.writeTransaction(ctx, "mark conversations that follow a changed one", func(tx *sql.Tx) error {
		for _, id := range followers {
			var token string
			if err := tx.QueryRowContext(ctx, `INSERT INTO message_labels_dirty(conversation_id,token) VALUES(?,hex(randomblob(8)))
				ON CONFLICT(conversation_id) DO UPDATE SET token=message_labels_dirty.token RETURNING token`, id).Scan(&token); err != nil {
				return err
			}
			tokens[id] = token
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, id := range followers {
		if s.jobs[id] == nil {
			s.setJob(id, &labelsJob{conversation: id, token: tokens[id], structure: labelsStructure(p, s.builder.sessions, id), dependency: p.executionDigests[id]})
		}
	}
	return nil
}
