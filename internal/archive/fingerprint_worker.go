package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type fingerprintImageRenderer interface {
	Render(context.Context, any, int) (fingerprintRendered, error)
}
type fingerprintEntity struct{ entity, id string }

// Two bounded slots each own native layout/raster scratch. In-memory reservations
// exclude an entity until its render ends, including newer generations of it.
// The service holds the exclusive library lock; a crash loses reservations,
// while all unfinished work stays dirty in SQLite and resumes immediately.
type fingerprintWorker struct {
	mu        sync.Mutex
	renderer  fingerprintImageRenderer
	secondary fingerprintImageRenderer
	active    [2]*fingerprintEntity
	pass      fingerprintPass
}

// fingerprintPass is the one "Render fingerprints" operation both workers
// report into, so Background Processing shows one row with their combined
// count. It ends, with the first error either saw, when the last one leaves.
type fingerprintPass struct {
	mu    sync.Mutex
	pass  *daemonPass
	users int
	err   error
}

func (p *fingerprintPass) join(ctx context.Context, c *Catalog) *daemonPass {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.users == 0 {
		p.pass, p.err = c.beginPass(ctx, "maintenance.fingerprints", triggerSchedule), nil
	}
	p.users++
	return p.pass
}

func (p *fingerprintPass) worked(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pass.worked(n, "fingerprint")
}

func (p *fingerprintPass) leave(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err == nil {
		p.err = err
	}
	if p.users--; p.users == 0 {
		p.pass.end(p.err)
		p.pass = nil
	}
}

var fingerprintRenderTimeout = 2 * time.Minute

// fingerprintInputReadHook, when set (tests), runs after a unit reads its input.
var fingerprintInputReadHook func()

type fingerprintAnalysisPolicy struct{ labels, events bool }
type fingerprintAnalysisPolicyKey struct{}

func (s *Server) fingerprintContext(ctx context.Context) context.Context {
	config := s.Config()
	// An absent worker or explicitly configured runtime cannot be fixed by a
	// model download. Keep their stages pending, but do not hold fingerprints
	// indefinitely while inference is unavailable on this Mac.
	available := config.Labels.withAssets().labelerProblem() == ""
	return context.WithValue(ctx, fingerprintAnalysisPolicyKey{}, fingerprintAnalysisPolicy{config.Labels.Enabled && available, s.agentEventsEnabled(ctx) && available})
}

func fingerprintPolicy(ctx context.Context) fingerprintAnalysisPolicy {
	if policy, ok := ctx.Value(fingerprintAnalysisPolicyKey{}).(fingerprintAnalysisPolicy); ok {
		return policy
	}
	// Catalog-only callers have no service configuration: existing analysis
	// ledgers remain dependencies until their pending work is complete.
	return fingerprintAnalysisPolicy{true, true}
}

// Fingerprints consume the analysis chain's results. Wait between passes,
// including after an interrupted run, rather than drawing intermediate labels
// and events and invalidating the same images again as analysis progresses.
func (c *Catalog) fingerprintAnalysisReady(ctx context.Context) (bool, error) {
	for _, op := range c.ops.views() {
		if !liveState(op.State) {
			continue
		}
		switch op.Spec.Kind {
		case "analysis.chain":
			// Population requests also wrap maintenance, including these
			// fingerprints. Their actual analysis stages are checked below.
			if op.Spec.Variant != "population-request" {
				return false, nil
			}
		case "analysis.labels", "analysis.agent_events", "analysis.authorship", "analysis.authorship.refine", "analysis.peers", "analysis.tools":
			return false, nil
		}
	}
	stages := []string{"tools", "authorship", "peers"}
	policy := fingerprintPolicy(ctx)
	if policy.labels {
		stages = append(stages, "labels")
	}
	if policy.events {
		stages = append(stages, "agent_events")
	}
	pending, err := catalogExists(ctx, c, `SELECT 1 FROM sync_deferred WHERE name IN (SELECT value FROM json_each(?)) AND generation>completed_generation`, jsonText(stages))
	return !pending, err
}

// Select only entities whose analysis is ready. Dirty marks also cover work
// outside the chain, and unmarked execution changes cover older writers. A
// workspace waits for every conversation it draws. Keeping waiting entities
// out of selection avoids retry writes, inflated progress and starvation of
// ready entities behind a high-priority waiting one.
func (c *Catalog) fingerprintSelectionSQL(ctx context.Context, columns string) (string, error) {
	tables, err := queryMapsContext(ctx, c.reader(), `SELECT name FROM sqlite_master WHERE type='table' AND name IN ('message_labels_dirty','agent_events_dirty','message_labels_conversations','agent_events_conversations')`)
	if err != nil {
		return "", err
	}
	pending := []string{}
	policy := fingerprintPolicy(ctx)
	for _, table := range tables {
		switch name := firstString(table["name"]); name {
		case "message_labels_dirty":
			if policy.labels {
				pending = append(pending, `SELECT conversation_id id FROM `+name)
			}
		case "agent_events_dirty":
			if policy.events {
				pending = append(pending, `SELECT conversation_id id FROM `+name)
			}
		case "message_labels_conversations":
			pending = append(pending, executionUnmarkedSQL(ctx, c.reader(), name))
		case "agent_events_conversations":
			pending = append(pending, `SELECT conversation_id id FROM (`+agentEventsWithheldSQLFor(ctx, c.reader())+`)`)
		}
	}
	query := `SELECT ` + columns + ` FROM fingerprint_entities WHERE dirty=1 AND next_attempt<=unixepoch()`
	if len(pending) > 0 {
		// One CASE, not an OR of the two entities: SQLite answers the OR from
		// the primary key and then sorts the whole queue to take its first
		// row, once per image; a CASE leaves it reading fingerprint_pending
		// in order and stopping at the first ready entity.
		query = `WITH pending_analysis(id) AS MATERIALIZED (` + strings.Join(pending, ` UNION `) + `),
pending_workspaces(id) AS MATERIALIZED (SELECT DISTINCT workspace_id FROM conversations WHERE id IN (SELECT id FROM pending_analysis) AND workspace_id IS NOT NULL)
` + query + ` AND CASE entity WHEN 'conversation' THEN id NOT IN (SELECT id FROM pending_analysis) ELSE id NOT IN (SELECT id FROM pending_workspaces) END`
	}
	return query, nil
}

func (c *Catalog) fingerprintImagesPending(ctx context.Context) (bool, error) {
	ready, err := c.fingerprintAnalysisReady(ctx)
	if err != nil || !ready {
		return false, err
	}
	query, err := c.fingerprintSelectionSQL(ctx, "1")
	if err != nil {
		return false, err
	}
	return catalogExists(ctx, c, query+` LIMIT 1`)
}

// Waiting is a held pass, not a success or a rendering failure. The background
// loop retains its accepted demand until analysis (or a render retry) is ready.
var errFingerprintWaiting = fmt.Errorf("%w: fingerprints are waiting for analysis or retry", errWorkHeld)
var errFingerprintPreempted = fmt.Errorf("%w: fingerprints yielded to manual indexing or verification", errWorkHeld)

func (s *Server) retainFingerprintPass(cadence string) {
	s.background.mu.Lock()
	defer s.background.mu.Unlock()
	if s.background.fingerprintRetry != "requested" {
		s.background.fingerprintRetry = cadence
	}
}

func (s *Server) fingerprintPassReady(ctx context.Context) (bool, error) {
	pending, err := catalogExists(ctx, s.Catalog, `SELECT 1 FROM fingerprint_entities WHERE dirty=1`)
	if err != nil || !pending {
		return !pending, err
	}
	return s.Catalog.fingerprintImagesPending(ctx)
}

func (s *Server) runFingerprintPass(ctx context.Context) error {
	ctx = s.fingerprintContext(ctx)
	c := s.Catalog
	ready, err := s.fingerprintPassReady(ctx)
	if err != nil {
		return err
	}
	if !ready {
		// Collection remains independent of analysis readiness.
		if err := c.runSearchBatches(ctx, []searchBatch{{"maintenance.fingerprints", c.pruneFingerprintBlobs}}); err != nil {
			return err
		}
		return errFingerprintWaiting
	}
	if s.work.declares(kindFingerprints) {
		err = s.work.runLibraryMode(ctx, []string{kindFingerprints}, backgroundExplicit(ctx), triggerChained, workModePass)
	} else {
		checked := 0
		err = c.runSearchBatches(ctx, []searchBatch{
			{"maintenance.fingerprints", c.pruneFingerprintBlobs},
			{"maintenance.fingerprints", func(ctx context.Context) (bool, error) {
				did, err := c.refreshFingerprintImages(ctx)
				if did && err == nil {
					checked++
					reportWork(ctx, "Checking fingerprints", "fingerprint", checked, -1)
				}
				return did, err
			}},
		})
	}
	if err != nil {
		return err
	}
	// Selection can become blocked during a pass. Do not consume the request
	// just because no image is eligible in this snapshot.
	if pending, checkErr := catalogExists(ctx, c, `SELECT 1 FROM fingerprint_entities WHERE dirty=1`); checkErr != nil {
		return checkErr
	} else if pending {
		return errFingerprintWaiting
	}
	return nil
}

func (c *Catalog) refreshFingerprintImages(ctx context.Context) (more bool, err error) {
	if c.readOnly || c.queryOnly {
		return false, nil
	}
	ready, err := c.fingerprintAnalysisReady(ctx)
	if err != nil || !ready {
		return false, err
	}
	query, err := c.fingerprintSelectionSQL(ctx, "entity,id,revision,events_mask,attempts")
	if err != nil {
		return false, err
	}
	worker := &c.fingerprints
	worker.mu.Lock()
	slot := -1
	for i, active := range worker.active {
		if active == nil {
			slot = i
			break
		}
	}
	if slot < 0 {
		worker.mu.Unlock()
		return false, nil
	}
	var entity, id, revision string
	var mask, attempts int
	args := []any{}
	for _, active := range worker.active {
		if active != nil {
			query += ` AND NOT (entity=? AND id=?)`
			args = append(args, active.entity, active.id)
		}
	}
	query += ` ORDER BY priority DESC,next_attempt,entity,id LIMIT 1`
	err = c.reader().QueryRowContext(ctx, query, args...).Scan(&entity, &id, &revision, &mask, &attempts)
	var renderer fingerprintImageRenderer
	if err == nil {
		worker.active[slot] = &fingerprintEntity{entity, id}
		renderer = worker.renderer
		if slot == 1 {
			renderer = worker.secondary
		}
	}
	worker.mu.Unlock()
	if err == nil {
		defer func() {
			worker.mu.Lock()
			defer worker.mu.Unlock()
			if slot == 0 {
				worker.renderer = renderer
			} else {
				worker.secondary = renderer
			}
			worker.active[slot] = nil
		}()
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	stage, started := "check cached images", time.Now()
	failure := func(err error) error {
		if cause := context.Cause(ctx); ctx.Err() != nil && cause != ctx.Err() {
			err = fmt.Errorf("%w (cause: %v)", err, cause)
		}
		return fmt.Errorf("fingerprint %s %s: %s after %s (appearance=%d attempt=%d): %w", entity, id, stage, time.Since(started).Round(time.Millisecond), mask, attempts+1, err)
	}
	defer func() {
		if err != nil {
			err = failure(err)
		}
	}()
	needed := mask
	available, err := queryMapsContext(ctx, c.reader(), `SELECT events,COUNT(*) count FROM fingerprint_images WHERE entity=? AND entity_id=? AND revision=? AND renderer=? GROUP BY events`, entity, id, revision, fingerprintRendererVersion())
	if err != nil {
		return false, err
	}
	for _, row := range available {
		if integer(row["count"]) == fingerprintImagesPerAppearance() {
			if integer(row["events"]) != 0 {
				needed &^= 1
			} else {
				needed &^= 2
			}
		}
	}
	// Each unit publishes one appearance. A newly requested event-hidden
	// image does not have to wait behind expensive event selection.
	if needed&2 != 0 {
		needed = 2
	} else if needed&1 != 0 {
		needed = 1
	}
	// Recheck execution evidence after selection. Leave superseded work queued
	// without counting a render or writing a retry. The input is classified at
	// one execution source generation: the pending sets move only with it or
	// with writes that move the revision, so a read that
	// straddles a change is not drawn. It is conservative: any execution
	// source write counts, related or not, so the retry takes the same
	// backoff, bounding repeated reads while an ingest writes.
	stage = "check execution evidence"
	generation, tracked, err := executionSourceGeneration(ctx, c.reader())
	if err != nil {
		return false, err
	}
	superseded, err := fingerprintExecutionPending(ctx, c.reader(), entity, id, needed == 1)
	if err != nil {
		return false, err
	}
	if superseded {
		return false, nil
	}
	if needed == 0 {
		return true, c.publishFingerprintImages(ctx, entity, id, revision, fingerprintRendered{})
	}
	renderCtx, cancel := context.WithTimeout(ctx, fingerprintRenderTimeout)
	defer cancel()
	stage = "load input"
	input, err := c.messageLabelFingerprintInput(renderCtx, id, entity == "workspace", 0, true)
	if errors.Is(err, sql.ErrNoRows) {
		return true, c.writeTransaction(ctx, "discard deleted fingerprint", func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `DELETE FROM fingerprint_entities WHERE entity=? AND id=? AND revision=?`, entity, id, revision)
			return err
		})
	}
	if err == nil && fingerprintInputReadHook != nil {
		fingerprintInputReadHook()
	}
	if err == nil && tracked {
		var after int64
		if after, _, err = executionSourceGeneration(ctx, c.reader()); err == nil && after != generation {
			delay := min(300, 5*(1<<min(attempts, 6)))
			return true, c.writeTransaction(ctx, "retry fingerprint after an execution change", func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `UPDATE fingerprint_entities SET attempts=attempts+1,next_attempt=unixepoch()+? WHERE entity=? AND id=? AND revision=?`, delay, entity, id, revision)
				return err
			})
		}
	}
	var result fingerprintRendered
	if err == nil {
		input["events"] = input["agent_events"]
		stage = "check input changes"
		var inputHash string
		inputHash, err = fingerprintInputHash(input, needed)
		if err == nil {
			result, err = c.reuseFingerprintImages(renderCtx, entity, id, inputHash, needed)
		}
		if err == nil && len(result.Images) == 0 {
			if renderer == nil {
				renderer, err = newFingerprintRenderer()
			}
			if err == nil {
				stage = "render images"
				result, err = renderer.Render(renderCtx, input, needed)
			}
		}
		result.InputHash = inputHash
	}
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		delay := min(300, 5*(1<<min(attempts, 6)))
		problem := failure(err).Error()
		if len(problem) > 1000 {
			problem = problem[:1000]
		}
		stored := c.writeTransaction(ctx, "defer failed fingerprint", func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, `UPDATE fingerprint_entities SET attempts=attempts+1,next_attempt=unixepoch()+?,last_error=? WHERE entity=? AND id=? AND revision=?`, delay, problem, entity, id, revision)
			return e
		})
		if stored != nil {
			return false, stored
		}
		fmt.Fprintf(os.Stderr, "Fingerprint retry in %ds: %s\n", delay, problem)
		return true, nil // other entities progress while this one backs off
	}
	// Publication and acknowledgement are atomic. A source change or deletion
	// while loading/rendering leaves the new generation queued, never stale PNGs
	// stamped current. A concurrently requested event-hidden variant stays queued.
	stage = "publish images"
	err = c.publishFingerprintImages(ctx, entity, id, revision, result)
	return true, err
}

// Reuse a complete appearance across source revisions, including after restart.
// Publication still checks the selected revision before acknowledging it.
// Older writers leave the input stamp alone: require its revision to match the
// image's so their replacement PNGs cannot inherit an unrelated input hash.
func (c *Catalog) reuseFingerprintImages(ctx context.Context, entity, id, inputHash string, mask int) (fingerprintRendered, error) {
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT profile,theme,events,hash FROM fingerprint_images WHERE entity=? AND entity_id=? AND renderer=? AND input_hash=? AND input_revision=revision AND events=?`, entity, id, fingerprintRendererVersion(), inputHash, mask == 1)
	result := fingerprintRendered{}
	if err != nil || int64(len(rows)) != fingerprintImagesPerAppearance() {
		return result, err
	}
	for _, row := range rows {
		result.Images = append(result.Images, fingerprintRenderedImage{Profile: firstString(row["profile"]), Theme: firstString(row["theme"]), Events: integer(row["events"]) != 0, Hash: firstString(row["hash"])})
	}
	return result, nil
}

func (c *Catalog) publishFingerprintImages(ctx context.Context, entity, id, revision string, result fingerprintRendered) error {
	return c.writeTransaction(ctx, "publish fingerprint images", func(tx *sql.Tx) error {
		var current string
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM fingerprint_entities WHERE entity=? AND id=?`, entity, id).Scan(&current); errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		if current != revision {
			return nil
		}
		for _, image := range result.Images {
			if image.PNG != nil {
				if _, err := tx.ExecContext(ctx, `INSERT INTO fingerprint_blobs(hash,png) VALUES(?,?) ON CONFLICT(hash) DO NOTHING`, image.Hash, image.PNG); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO fingerprint_images(entity,entity_id,profile,theme,events,revision,renderer,input_hash,input_revision,hash) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(entity,entity_id,profile,theme,events) DO UPDATE SET revision=excluded.revision,renderer=excluded.renderer,input_hash=excluded.input_hash,input_revision=excluded.input_revision,hash=excluded.hash`, entity, id, image.Profile, image.Theme, image.Events, revision, fingerprintRendererVersion(), result.InputHash, revision, image.Hash); err != nil {
				return err
			}
		}
		ready, err := queryMapsContext(ctx, tx, `SELECT events,COUNT(*) count FROM fingerprint_images WHERE entity=? AND entity_id=? AND revision=? AND renderer=? GROUP BY events`, entity, id, revision, fingerprintRendererVersion())
		if err != nil {
			return err
		}
		readyMask := 0
		for _, row := range ready {
			if integer(row["count"]) == fingerprintImagesPerAppearance() {
				if integer(row["events"]) == 0 {
					readyMask |= 2
				} else {
					readyMask |= 1
				}
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE fingerprint_entities SET dirty=CASE WHEN events_mask&?=events_mask THEN 0 ELSE 1 END,priority=CASE WHEN events_mask&?=events_mask THEN 0 ELSE priority END,attempts=0,next_attempt=0,last_error='' WHERE entity=? AND id=? AND revision=?`, readyMask, readyMask, entity, id, revision)
		return err
	})
}

func (s *Server) maintainFingerprintImages(ctx context.Context) {
	s.maintainFingerprintImagesLoop(ctx, false)
}
func (s *Server) maintainFingerprintImagesSecondary(ctx context.Context) {
	s.maintainFingerprintImagesLoop(ctx, true)
}

func (s *Server) maintainFingerprintImagesLoop(ctx context.Context, secondary bool) {
	c := s.Catalog
	shared := &c.fingerprints.pass
	var pass *daemonPass
	defer func() {
		if pass != nil {
			shared.leave(ctx.Err())
		}
	}()
	var pruneAt time.Time
	for ctx.Err() == nil {
		settings, err := c.backgroundSettings(ctx, time.Now())
		item, _ := backgroundItemByID("fingerprints")
		battery := settings.Battery.onPower(cadenceFrequent, s.cachedPower(ctx))
		if err != nil || settings.Mode != "on" || settings.cadence(item) != cadenceFrequent || c.backgroundPaused(ctx, time.Now()) || battery == batteryWait || secondary && battery == batterySlower {
			if pass != nil {
				shared.leave(nil)
				pass = nil
			}
			if !waitFingerprintWorker(ctx, time.Second) {
				return
			}
			continue
		}
		if err := c.queries.wait(ctx); err != nil {
			return
		}
		if !secondary && time.Since(pruneAt) > time.Minute {
			prune := c.beginPass(ctx, "maintenance.fingerprints", triggerSchedule)
			prune.runIO(ctx, func(ctx context.Context) { _, err = c.pruneFingerprintBlobs(ctx) })
			prune.end(err)
			pruneAt = time.Now()
		}
		if pass == nil {
			if pending, err := c.fingerprintImagesPending(s.fingerprintContext(ctx)); err != nil || !pending {
				if !waitFingerprintWorker(ctx, time.Second) {
					return
				}
				continue
			}
			pass = shared.join(ctx, c)
		}
		var more bool
		pass.runIO(ctx, func(ctx context.Context) { more, err = c.refreshFingerprintImages(s.fingerprintContext(ctx)) })
		if more {
			shared.worked(1)
		}
		if !more || err != nil {
			shared.leave(err)
			pass = nil
			if err != nil && ctx.Err() == nil {
				fmt.Fprintf(os.Stderr, "Fingerprint cache: %v\n", err)
			}

			if !waitFingerprintWorker(ctx, time.Second) {
				return
			}
		} else if !waitFingerprintWorker(ctx, func() time.Duration {
			if battery == batterySlower {
				return 2 * time.Second
			}
			return 0
		}()) {
			return
		}
	}
}
func waitFingerprintWorker(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		// Query activity already gates each unit. Yield without a fixed sleep so
		// small fingerprints can backfill at the renderer's actual throughput.
		runtime.Gosched()
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Fingerprint rendering under population execution. The two daemons above
// become one declared coarse task: the maintenance coordinator submits its
// demand while the item is on the continuous Frequently schedule, and Run
// now asks the same task for a pass. Each admitted batch runs up to one
// worker per granted CPU, at most one per renderer slot, so both renderer
// slots stay busy at a cap of two or more and one runs at a cap of one. The
// domain keeps its dirty rows, retry backoff, revision-checked publication
// and renderer scratch reuse.

// fingerprintPower is the daemons' gate as of now: whether continuous
// rendering may run at all (the Frequently cadence, power allowing) and
// whether it runs slower on battery (one worker, spaced images).
func (s *Server) fingerprintPower(ctx context.Context) (allowed, slower bool) {
	settings, err := s.Catalog.backgroundSettings(ctx, time.Now())
	if err != nil {
		return false, false
	}
	item, _ := backgroundItemByID("fingerprints")
	battery := settings.Battery.onPower(cadenceFrequent, s.cachedPower(ctx))
	return settings.cadence(item) == cadenceFrequent && battery != batteryWait, battery == batterySlower
}

// fingerprintMaintenanceTask is maintenance.fingerprints as a coarse task.
// Demand the pending check started follows the daemons' cadence and battery
// gate, so it ends rather than waits when either turns it off; a requested
// pass (Run now, a scheduled item) runs regardless, as runSearchBatches did.
func (s *Server) fingerprintMaintenanceTask() *maintenanceTask {
	c := s.Catalog
	var pruneAt atomic.Int64 // unix nanoseconds of the last scheduled prune
	pruneDue := func() bool { return time.Since(time.Unix(0, pruneAt.Load())) > time.Minute }
	return &maintenanceTask{kind: kindFingerprints, phase: "render", cpus: 1,
		maxCPUs: func(ctx context.Context) int {
			if _, slower := s.fingerprintPower(ctx); slower {
				return 1
			}
			return len(c.fingerprints.active)
		},
		probe: func(ctx context.Context) (bool, error) {
			if c.readOnly || c.queryOnly {
				return false, nil
			}
			if allowed, _ := s.fingerprintPower(ctx); !allowed {
				return false, nil
			}
			pending, err := c.fingerprintImagesPending(s.fingerprintContext(ctx))
			if err != nil || pending || !pruneDue() {
				return pending, err
			}
			return catalogExists(ctx, c, "SELECT 1 FROM fingerprint_orphans WHERE not_before<=unixepoch()")
		},
		step: func(ctx context.Context, allocation workAllocation) (bool, int64, error) {
			if c.readOnly || c.queryOnly {
				return false, 0, nil
			}
			requested := maintenanceModeFrom(ctx) != ""
			var work int64
			pruned := false
			// Scheduled rendering prunes at most once a minute, as the primary
			// daemon did; a requested pass drains the orphans first.
			if requested || pruneDue() {
				pruneAt.Store(time.Now().UnixNano())
				var err error
				if pruned, err = c.pruneFingerprintBlobs(ctx); err != nil {
					return false, 0, err
				}
				if pruned {
					work++
				}
			}
			_, slower := s.fingerprintPower(ctx)
			rendered, more, err := c.renderFingerprintBatch(s.fingerprintContext(ctx), allocation, slower, maintenanceSlice)
			if err == nil && requested && !more {
				pending, checkErr := catalogExists(ctx, c, `SELECT 1 FROM fingerprint_entities WHERE dirty=1`)
				if checkErr != nil {
					return false, work + rendered, checkErr
				}
				if pending {
					return false, work + rendered, workBlocked(workBlocker{Reason: workBlockedDependency, Detail: "fingerprints are waiting for analysis or retry"})
				}
			}
			return more || requested && pruned, work + rendered, err
		}}
}

// renderFingerprintBatch renders dirty images with one worker per granted
// CPU (at most one per renderer slot; one when slower) until nothing is
// eligible or slice has passed. Each worker lets interactive queries go
// first before every image and starts the next image without a gap, except
// on battery's slower policy. more reports that a worker stopped at the
// slice with images still to render.
func (c *Catalog) renderFingerprintBatch(ctx context.Context, allocation workAllocation, slower bool, slice time.Duration) (int64, bool, error) {
	workers := min(max(allocation.CPUs, 1), len(c.fingerprints.active))
	if slower {
		workers = 1
	}
	deadline := time.Now().Add(slice)
	var rendered atomic.Int64
	var more atomic.Bool
	failures := make([]error, workers)
	var wait sync.WaitGroup
	for index := range workers {
		wait.Go(func() {
			for {
				if err := c.queries.wait(ctx); err != nil {
					failures[index] = err
					return
				}
				did, err := c.refreshFingerprintImages(ctx)
				if err != nil || !did {
					failures[index] = err
					return
				}
				rendered.Add(1)
				if !time.Now().Before(deadline) {
					more.Store(true)
					return
				}
				if slower && !waitFingerprintWorker(ctx, 2*time.Second) {
					failures[index] = ctx.Err()
					return
				}
			}
		})
	}
	wait.Wait()
	return rendered.Load(), more.Load(), errors.Join(failures...)
}
