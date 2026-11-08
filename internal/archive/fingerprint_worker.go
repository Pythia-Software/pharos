package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"runtime"
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

func (c *Catalog) refreshFingerprintImages(ctx context.Context) (bool, error) {
	if c.readOnly || c.queryOnly {
		return false, nil
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
	query := `SELECT entity,id,revision,events_mask,attempts FROM fingerprint_entities WHERE dirty=1 AND next_attempt<=unixepoch()`
	args := []any{}
	for _, active := range worker.active {
		if active != nil {
			query += ` AND NOT (entity=? AND id=?)`
			args = append(args, active.entity, active.id)
		}
	}
	query += ` ORDER BY priority DESC,next_attempt,entity,id LIMIT 1`
	err := c.reader().QueryRowContext(ctx, query, args...).Scan(&entity, &id, &revision, &mask, &attempts)
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
	// While the appearance rests on superseded execution evidence
	// (fingerprintExecutionPending) it waits, backing off: drawn so, it would
	// read current if the change were undone before any mark. A mark, and the
	// run that clears it, requeue it (fingerprintSourceTriggers). The input is
	// classified at one execution source generation: the pending sets move
	// only with it or with writes that move the revision, so a read that
	// straddles a change is not drawn. It is conservative: any execution
	// source write counts, related or not, so the retry takes the same
	// backoff, bounding repeated reads while an ingest writes.
	generation, tracked, err := executionSourceGeneration(ctx, c.reader())
	if err != nil {
		return false, err
	}
	superseded, err := fingerprintExecutionPending(ctx, c.reader(), entity, id, needed == 1)
	if err != nil {
		return false, err
	}
	if superseded {
		delay := min(300, 5*(1<<min(attempts, 6)))
		return true, c.writeTransaction(ctx, "defer superseded fingerprint", func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE fingerprint_entities SET attempts=attempts+1,next_attempt=unixepoch()+? WHERE entity=? AND id=? AND revision=?`, delay, entity, id, revision)
			return err
		})
	}
	renderCtx, cancel := context.WithTimeout(ctx, fingerprintRenderTimeout)
	defer cancel()
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
	if err == nil && renderer == nil {
		renderer, err = newFingerprintRenderer()
	}
	var result fingerprintRendered
	if err == nil {
		input["events"] = input["agent_events"]
		result, err = renderer.Render(renderCtx, input, needed)
	}
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		delay := min(300, 5*(1<<min(attempts, 6)))
		failure := err.Error()
		if len(failure) > 1000 {
			failure = failure[:1000]
		}
		stored := c.writeTransaction(ctx, "defer failed fingerprint", func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, `UPDATE fingerprint_entities SET attempts=attempts+1,next_attempt=unixepoch()+?,last_error=? WHERE entity=? AND id=? AND revision=?`, delay, failure, entity, id, revision)
			return e
		})
		if stored != nil {
			return false, stored
		}
		fmt.Fprintf(os.Stderr, "Fingerprint %s %s: %v\n", entity, id, err)
		return true, nil // other entities progress while this one backs off
	}
	// Publication and acknowledgement are atomic. A source change or deletion
	// while loading/rendering leaves the new generation queued, never stale PNGs
	// stamped current. A concurrently requested event-hidden variant stays queued.
	err = c.publishFingerprintImages(ctx, entity, id, revision, result)
	return true, err
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
			if _, err := tx.ExecContext(ctx, `INSERT INTO fingerprint_blobs(hash,png) VALUES(?,?) ON CONFLICT(hash) DO NOTHING`, image.Hash, image.PNG); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO fingerprint_images(entity,entity_id,profile,theme,events,revision,renderer,hash) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(entity,entity_id,profile,theme,events) DO UPDATE SET revision=excluded.revision,renderer=excluded.renderer,hash=excluded.hash`, entity, id, image.Profile, image.Theme, image.Events, revision, fingerprintRendererVersion(), image.Hash); err != nil {
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
		if pass == nil {
			pass = shared.join(ctx, c)
		}
		if !secondary && time.Since(pruneAt) > time.Minute {
			prune := c.beginPass(ctx, "maintenance.fingerprints", triggerSchedule)
			prune.runIO(ctx, func(ctx context.Context) { _, err = c.pruneFingerprintBlobs(ctx) })
			prune.end(err)
			pruneAt = time.Now()
		}
		var more bool
		pass.runIO(ctx, func(ctx context.Context) { more, err = c.refreshFingerprintImages(ctx) })
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
			pending, err := catalogExists(ctx, c, "SELECT 1 FROM fingerprint_entities WHERE dirty=1 AND next_attempt<=unixepoch()")
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
			rendered, more, err := c.renderFingerprintBatch(ctx, allocation, slower, maintenanceSlice)
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
