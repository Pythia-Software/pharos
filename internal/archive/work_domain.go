package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
)

// Domain steps over a selected population (docs/work-execution-plan.md,
// sections 3.1 and 5.4). Each runs one bounded step of an existing domain
// algorithm over the given conversations and publishes through that
// domain's own revision guard. None of them publishes a library-wide
// readiness claim from a selection. An empty selection (nil or empty) does
// nothing; whole-library work stays with the classic functions.

// workDomainStep is what one step did to its selection, as sorted
// conversation IDs.
type workDomainStep struct {
	// Done is current after this step: its captured revision was committed
	// and none newer is pending (or it had nothing pending).
	Done []string
	// Superseded was committed, but a newer revision arrived meanwhile and
	// stays pending.
	Superseded []string
	// Remaining is still pending: the step's bound did not reach it.
	Remaining []string
	// Failed stays pending after the domain gave up on it in this step (a
	// conversation whose items could not be built, a message the worker
	// could not label).
	Failed []string
	// Rediscover asks for the labels session's discovery before more steps.
	Rediscover bool
	// Progress: the step durably advanced a Remaining unit (stored part of a
	// conversation its bound cut). A step that leaves units Remaining and
	// publishes nothing without it made no progress, and its units are
	// charged as failed rather than retried at once.
	Progress bool
}

// errWorkDomainBusy is a step whose domain lock another run holds. The
// caller releases its allocation and tries again later.
var errWorkDomainBusy = errors.New("another run is doing this work")

// workDomainIDs sorts and deduplicates a selection.
func workDomainIDs(ids []string) []string {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	return slices.Compact(ids)
}

// workDomainPending reads which of ids query (one parameter, a JSON array of
// IDs) reports.
func workDomainPending(ctx context.Context, q queryer, query string, ids []string) (map[string]bool, error) {
	pending := map[string]bool{}
	if len(ids) == 0 {
		return pending, nil
	}
	rows, err := queryMapsContext(ctx, q, query, jsonText(ids))
	for _, row := range rows {
		pending[firstString(row["id"])] = true
	}
	return pending, err
}

// classify sorts ids into a step from what it attempted and what is still
// pending afterwards.
func (step *workDomainStep) classify(ids []string, attempted, pending map[string]bool) {
	for _, id := range ids {
		switch {
		case !pending[id]:
			step.Done = append(step.Done, id)
		case attempted[id]:
			step.Superseded = append(step.Superseded, id)
		default:
			step.Remaining = append(step.Remaining, id)
		}
	}
}

// Stored activity.

const parallelismPendingSQL = `SELECT conversation_id id FROM parallelism_dirty WHERE conversation_id IN (SELECT value FROM json_each(?))`

// Stored activity an execution evidence change made stale is marked dirty
// only by the sweep (sweepParallelism), which no trigger runs.
const parallelismStoredSQL = `SELECT conversation_id id FROM parallelism_occurrences WHERE conversation_id IN (SELECT value FROM json_each(?))`

// parallelismPendingFor is which selected conversations' stored activity is
// pending: indexed lookups, for a selector. Until the sweep has reached the
// execution source generation, a selected conversation with stored activity
// is pending too, since the change may have made it stale unmarked.
func (c *Catalog) parallelismPendingFor(ctx context.Context, ids []string) ([]string, error) {
	pending, err := c.parallelismPending(ctx, workDomainIDs(ids))
	return sortedKeys(pending), err
}

func (c *Catalog) parallelismPending(ctx context.Context, ids []string) (map[string]bool, error) {
	if len(ids) == 0 {
		return map[string]bool{}, nil
	}
	tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	pending, err := workDomainPending(ctx, tx, parallelismPendingSQL, ids)
	if err != nil {
		return nil, err
	}
	if due, err := parallelismSweepDue(ctx, tx); err != nil || !due {
		return pending, err
	}
	stored, err := workDomainPending(ctx, tx, parallelismStoredSQL, ids)
	for id := range stored {
		pending[id] = true
	}
	return pending, err
}

// parallelismSweepDue reports whether the execution source generation has
// moved past the one stored activity was last swept against (as
// parallelismStale decides it); untracked, never.
func parallelismSweepDue(ctx context.Context, q queryer) (bool, error) {
	generation, tracked, err := executionSourceGeneration(ctx, q)
	if err != nil || !tracked {
		return false, err
	}
	meta, err := queryMapsContext(ctx, q, "SELECT CAST(value AS INTEGER) swept FROM meta WHERE key=?", parallelismExecutionKey)
	return err == nil && (len(meta) == 0 || integer(meta[0]["swept"]) < generation), err
}

// parallelismSweepVersion is the execution source generation the timing
// task's admitted preparation sweeps to (empty when untracked).
func (c *Catalog) parallelismSweepVersion(ctx context.Context) (string, error) {
	generation, tracked, err := executionSourceGeneration(ctx, c.reader())
	if err != nil || !tracked {
		return "", err
	}
	return fmt.Sprint(generation), nil
}

// prepareParallelism is the timing task's admitted preparation: the sweep
// marks the stored activity execution evidence changes made stale, so the
// queue a selection reads is complete.
func (c *Catalog) prepareParallelism(ctx context.Context, _ string) error {
	if !c.parallelismMu.TryLock() {
		return errWorkDomainBusy
	}
	defer c.parallelismMu.Unlock()
	return c.sweepParallelism(ctx)
}

// refreshParallelismFor stores the activity of at most parallelismBatch
// selected dirty conversations: refreshParallelism's sweep, its derivation in
// one read snapshot and its revision-guarded publication. It is serial (one
// CPU).
func (c *Catalog) refreshParallelismFor(ctx context.Context, ids []string) (workDomainStep, error) {
	var step workDomainStep
	ids = workDomainIDs(ids)
	if len(ids) == 0 {
		return step, nil
	}
	if !c.parallelismMu.TryLock() {
		return step, errWorkDomainBusy
	}
	defer c.parallelismMu.Unlock()
	// Selected stored activity an execution evidence change made stale is
	// dirty before the derivation reads the queue.
	if err := c.sweepParallelism(ctx); err != nil {
		return step, err
	}
	_, _, cutoff := parallelismPeriod(c.clock())
	tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return step, err
	}
	batch, err := deriveParallelismBatch(ctx, tx, c.parallelismIndexAt(ctx, tx), cutoff, parallelismBatch, ids, true)
	_ = tx.Rollback()
	if err != nil {
		return step, err
	}
	attempted := map[string]bool{}
	for _, derived := range batch {
		attempted[derived.id] = true
	}
	if len(batch) > 0 {
		if err := c.publishParallelism(ctx, batch); err != nil {
			return step, err
		}
	}
	pending, err := c.parallelismPending(ctx, ids)
	step.classify(ids, attempted, pending)
	return step, err
}

// Writing estimates.

// A pending row whose message is gone names no conversation: it is the
// library-wide lanes' work, never a selection's.
const authorshipPendingSQL = `SELECT DISTINCT m.conversation_id id FROM messages m JOIN authorship_pending p ON p.message_id=m.id
 WHERE m.conversation_id IN (SELECT value FROM json_each(?))`

// Mirror reconsideration is admitted preparation even when the message queue
// was empty. A changed representative must enqueue both sides before a
// selector can truthfully report no pending writing.
func (c *Catalog) prepareAuthorshipEstimates(ctx context.Context, generation string) error {
	entered := false
	err := c.withAuthorshipLease(func() error {
		entered = true
		representative, err := c.authorshipRepresentatives(ctx)
		if err != nil {
			return err
		}
		if err := c.refreshAuthorshipMirrors(ctx, representative); err != nil {
			return err
		}
		c.authorship.mu.Lock()
		c.authorship.workRepresentatives = representative
		c.authorship.workPreparedGeneration = generation
		c.authorship.mu.Unlock()
		return c.publishAuthorshipEstimates(ctx, generation)
	})
	if err != nil && !entered {
		return fmt.Errorf("%w: %v", errWorkDomainBusy, err)
	}
	return err
}

// Deleted messages no longer belong to a conversation selection, but their
// native queue entries still need the classifier's guarded cleanup.
func (c *Catalog) refreshAuthorshipOrphans(ctx context.Context) (int, error) {
	entered, count := false, 0
	err := c.withAuthorshipLease(func() error {
		entered = true
		var err error
		count, _, err = c.quickAuthorshipRows(ctx, nil, `SELECT p.message_id,p.revision FROM authorship_pending p
 WHERE NOT EXISTS(SELECT 1 FROM messages m WHERE m.id=p.message_id) LIMIT ?`, 100)
		if err != nil {
			return err
		}
		want, _, err := c.authorshipGenerationsContext(ctx)
		if err != nil {
			return err
		}
		return c.publishAuthorshipEstimates(ctx, want)
	})
	if err != nil && !entered {
		return 0, fmt.Errorf("%w: %v", errWorkDomainBusy, err)
	}
	return count, err
}

func (c *Catalog) authorshipOrphansPending(ctx context.Context) (bool, error) {
	var pending bool
	err := c.reader().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM authorship_pending p WHERE NOT EXISTS(SELECT 1 FROM messages m WHERE m.id=p.message_id))`).Scan(&pending)
	return pending, err
}

// authorshipPendingFor is which selected conversations have a message
// waiting for a writing estimate.
func (c *Catalog) authorshipPendingFor(ctx context.Context, ids []string) ([]string, error) {
	pending, err := workDomainPending(ctx, c.reader(), authorshipPendingSQL, workDomainIDs(ids))
	return sortedKeys(pending), err
}

// estimateAuthorshipFor estimates up to limit pending messages of the
// selected conversations (authorshipQuickLimit when limit is 0), newest
// first, in batches of 25 under the classifier lease, after reconsidering
// mirrors as UpdateAuthorship does. Each batch keeps the quick estimates'
// source hashes, dispositions and all-or-nothing revision check. It never
// tries the exact suffix (a library-wide read). It publishes the estimate
// generation only as UpdateAuthorship does: when the whole queue is empty.
func (c *Catalog) estimateAuthorshipFor(ctx context.Context, ids []string, generation string, limit int) (workDomainStep, error) {
	var step workDomainStep
	ids = workDomainIDs(ids)
	if len(ids) == 0 {
		return step, nil
	}
	if limit <= 0 {
		limit = authorshipQuickLimit
	}
	attempted := map[string]bool{}
	entered := false
	err := c.withAuthorshipLease(func() error {
		entered = true
		// Workspace identity is library context, not per-batch preparation.
		// Keep it for the input generation; changed inputs prepare it again
		// under the same lease before processing another selected batch.
		c.authorship.mu.Lock()
		representative, prepared := c.authorship.workRepresentatives, c.authorship.workPreparedGeneration == generation
		c.authorship.mu.Unlock()
		if !prepared {
			var err error
			representative, err = c.authorshipRepresentatives(ctx)
			if err != nil {
				return err
			}
			if err := c.refreshAuthorshipMirrors(ctx, representative); err != nil {
				return err
			}
			c.authorship.mu.Lock()
			c.authorship.workRepresentatives, c.authorship.workPreparedGeneration = representative, generation
			c.authorship.mu.Unlock()
		}
		for done := 0; done < limit; {
			n, read, err := c.quickAuthorshipSelected(ctx, representative, ids, min(25, limit-done))
			if err != nil {
				return err
			}
			for id := range read {
				attempted[id] = true
			}
			done += n
			if n == 0 {
				break
			}
		}
		return c.publishAuthorshipEstimates(ctx, generation)
	})
	if err != nil && !entered {
		return step, fmt.Errorf("%w: %v", errWorkDomainBusy, err)
	}
	if err != nil {
		return step, err
	}
	pending, err := workDomainPending(ctx, c.reader(), authorshipPendingSQL, ids)
	step.classify(ids, attempted, pending)
	return step, err
}

// Label query facts.

const messageLabelFactsPendingSQL = `SELECT conversation_id id FROM message_label_facts_dirty WHERE conversation_id IN (SELECT value FROM json_each(?))`

// messageLabelFactsPendingFor is which selected conversations' query facts
// are pending.
func (c *Catalog) messageLabelFactsPendingFor(ctx context.Context, ids []string) ([]string, error) {
	pending, err := workDomainPending(ctx, c.reader(), messageLabelFactsPendingSQL, workDomainIDs(ids))
	return sortedKeys(pending), err
}

// refreshMessageLabelFactsFor publishes one bounded batch of the selected
// dirty conversations' query facts within a message budget
// (messageLabelFactsMessages when 0), with the same query-generation guard
// as the daemon, and returns the next budget from this batch's writer hold.
// It never marks the facts ready.
func (c *Catalog) refreshMessageLabelFactsFor(ctx context.Context, ids []string, messages int) (workDomainStep, int, error) {
	var step workDomainStep
	if messages <= 0 {
		messages = messageLabelFactsMessages
	}
	ids = workDomainIDs(ids)
	if len(ids) == 0 {
		return step, messages, nil
	}
	before, err := workDomainPending(ctx, c.reader(), messageLabelFactsPendingSQL, ids)
	if err != nil {
		return step, messages, err
	}
	next := messages
	if len(before) > 0 {
		count, timing, err := c.refreshMessageLabelFactsOf(ctx, messages, sortedKeys(before))
		if err != nil {
			return step, messages, err
		}
		if count > 0 {
			next = messageLabelFactsNextBudget(messages, timing.hold)
		}
	}
	// A conversation the batch published and labels dirtied again since is
	// superseded; the budget may leave others for the next step.
	pending, err := workDomainPending(ctx, c.reader(), messageLabelFactsPendingSQL, ids)
	step.classify(ids, nil, pending)
	return step, next, err
}
