package archive

import (
	"context"
	"time"
)

// searchScheduleGate leaves the normal catch-up daemons running continuously
// in Frequently. In other columns the background loop owns their catch-up.
// Cache the setting for a second so small index batches do not reread it.
type searchScheduleGate struct {
	checked    time.Time
	continuous bool
}

func (gate *searchScheduleGate) wait(ctx context.Context, c *Catalog, passes ...**daemonPass) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(gate.checked) >= time.Second {
			settings, err := c.backgroundSettings(ctx, time.Now())
			if err == nil {
				item, _ := backgroundItemByID("search")
				gate.checked, gate.continuous = time.Now(), settings.cadence(item) == cadenceFrequent
			}
		}
		if gate.continuous {
			return nil
		}
		// A paused daemon has no live operation holding up eject. A caller
		// starts a new pass if its continuous schedule later resumes.
		for _, pass := range passes {
			if *pass != nil {
				(*pass).end(nil)
				*pass = nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

type searchBatch struct {
	kind string
	run  func() (bool, error)
}

// backgroundSearch catches up the same projections and index backfills the
// continuous daemons maintain, for an Hourly/Daily pass or an explicit Run now.
func (c *Catalog) backgroundSearch(ctx context.Context) error {
	// Stop holds projection work and cancels a batch already in progress.
	// The independent search-index backfills still run, as their daemons do.
	if projection, done, running := c.libraryPass(ctx); running {
		err := c.backgroundSearchView(projection)
		stopped := projection.Err() != nil && ctx.Err() == nil
		done()
		if err != nil && !stopped {
			return err
		}
	}
	migration := newToolOutputMigration()
	batches := []searchBatch{
		{"maintenance.search.substring", func() (bool, error) { return c.indexSubstringBatch(ctx) }},
		{"maintenance.search.tool-output", func() (bool, error) { return migration.step(ctx, c) }},
		{"maintenance.search.text", func() (bool, error) { return c.indexTextSearchBatch(ctx) }},
		{"maintenance.search.tools", func() (bool, error) { return c.indexToolSearchBatch(ctx) }},
	}
	for _, index := range []queryTextIndex{humanQueryText, toolQueryText, toolQueryFacts} {
		batches = append(batches, searchBatch{"maintenance.search.query", func() (bool, error) { return c.indexQueryTextBatch(ctx, index) }})
	}
	return c.runSearchBatches(ctx, batches)
}

// backgroundSearchView uses the cancellable libraryPass context for every
// projection batch, including previews and parallelism derived from the view.
func (c *Catalog) backgroundSearchView(ctx context.Context) error {
	countBatch := func(fn func() (int, error)) func() (bool, error) {
		return func() (bool, error) { count, err := fn(); return count > 0, err }
	}
	return c.runSearchBatches(ctx, []searchBatch{
		{"maintenance.origins", func() (bool, error) { return c.publishOrigins(ctx) }},
		{"maintenance.library-view", countBatch(func() (int, error) {
			count, err := c.RefreshLibrary(ctx, 10)
			c.noteLibraryRefresh(count, err)
			return count, err
		})},
		{"maintenance.previews", countBatch(func() (int, error) { return c.refreshPreviews(ctx, 25) })},
		{"maintenance.parallelism", countBatch(func() (int, error) { return c.refreshParallelism(ctx, parallelismBatch, false) })},
	})
}

func (c *Catalog) runSearchBatches(ctx context.Context, batches []searchBatch) error {
	for _, batch := range batches {
		pass := c.beginPass(ctx, batch.kind, triggerChained)
		for {
			if err := c.queries.wait(ctx); err != nil {
				pass.end(err)
				return err
			}
			var more bool
			var err error
			pass.runIO(func() { more, err = batch.run() })
			if more {
				pass.worked(1)
			}
			if !more || err != nil {
				pass.end(err)
				if err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}
