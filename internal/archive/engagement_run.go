package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	labelrt "github.com/gbdubs/pharos/internal/labeler"
	"github.com/gbdubs/pharos/internal/platform/filelock"
)

// The engagement pass (docs/engagement-labels.md) brings engagement_spans up
// to date, newest message first: it builds each typed message's target text
// and context (engagement.go), runs the token-label model on the local
// labeler runtime, and stores the message's spans with its state row. A
// message is scored again only when its input hash or the model identity
// differs from its state row's. Messages to look at come from the state
// table (never scored, or scored by another model) and from a dirty table
// fed by the labels ledger's triggers.

// engagementCandidatesSQL selects the messages the model reads: what a
// person typed to an agent in a top-level conversation, as the training data
// was sampled.
const engagementCandidatesSQL = `SELECT a.message_id, a.conversation_id, a.sent_at, m.source_order
	FROM message_authorship a JOIN conversations c ON c.id=a.conversation_id JOIN messages m ON m.id=a.message_id
	WHERE a.typed_words>0 AND c.agent_depth=0 AND m.role='user' AND m.kind='message'`

// errEngagementRunning reports an engagement run already in progress.
var errEngagementRunning = errors.New("the engagement pass is already running")

// engagementChunkMessages is how many messages one chunk reads, scores and
// stores in one transaction. A chunk's windows are packed by length, so a
// larger chunk wastes less on padding; a stop loses at most one chunk.
var engagementChunkMessages = 1024

// EngagementRunOptions bound one run.
type EngagementRunOptions struct {
	// Limit stops before a chunk once this many messages were scored (0: no
	// limit); the rest stay pending.
	Limit int
	// Full looks at every typed message; only messages whose inputs or model
	// changed are scored again either way.
	Full bool
	// Conversations restricts the run (tests and diagnostics).
	Conversations []string
	// DryRun builds and frames the inputs and counts them, but starts no
	// worker and stores nothing.
	DryRun   bool
	Progress io.Writer
}

// EngagementRunStats describe a run.
type EngagementRunStats struct {
	// Candidates are the typed messages there are; Messages the ones this
	// run looked at.
	Candidates, Messages int
	// MessagesScored went through the model; MessagesSkipped got a state row
	// without it (nothing typed left, or no letter); MessagesUnchanged kept
	// their rows; MessagesRemoved lost them (no longer typed messages).
	MessagesScored, MessagesSkipped, MessagesUnchanged, MessagesRemoved int
	Windows, Tokens, Spans, MessagesWithSpans                           int
	Cut, Complete                                                       bool
	// Threads per worker and worker processes used, and the device.
	Threads, Workers int
	Device           string
	WorkerPIDs       []int
	// Where the time went. Plan chooses the messages; Read is SQL for their
	// text, spans and context; Frame builds targets, tokenizes and windows;
	// Infer is the worker; Decode makes spans; Write stores them.
	Plan, Read, Frame, Infer, Decode, Write, Total time.Duration
	MessagesPerSecond, TokensPerSecond             float64
}

// engagementJob is one message to look at.
type engagementJob struct {
	id, conversation string
	order            sql.NullInt64
	// storedHash is its state row's hash when this model made it.
	storedHash string
}

// engagementItem is a message read and framed.
type engagementItem struct {
	job     engagementJob
	text    string
	target  engagementTarget
	hash    string
	outcome string
	offsets []labelrt.ByteRange
	windows []labelrt.TokenWindow
	first   int // its first window among the chunk's rows
	spans   []engagementSpan
}

// RunEngagement brings engagement spans up to date. Cancellation keeps the
// chunks already stored; the next run resumes with the rest.
func (c *Catalog) RunEngagement(ctx context.Context, labels LabelerSettings, settings EngagementSettings, opts EngagementRunOptions) (*EngagementRunStats, error) {
	started := time.Now()
	stats := &EngagementRunStats{}
	defer func() {
		stats.Total = time.Since(started)
		if seconds := (stats.Total - stats.Plan).Seconds(); seconds > 0 {
			stats.MessagesPerSecond = float64(stats.MessagesScored+stats.MessagesSkipped) / seconds
			stats.TokensPerSecond = float64(stats.Tokens) / seconds
		}
	}()
	var progressMu sync.Mutex
	log := labelingProgress(opts.Progress, started)
	progress := func(format string, args ...any) {
		progressMu.Lock()
		defer progressMu.Unlock()
		log(format, args...)
	}
	labels = settings.labeler(labels.withAssets())
	if labels.Worker == "" && !opts.DryRun {
		return stats, fmt.Errorf("%w: set labels_worker (or PHAROS_LABELS_WORKER)", labelrt.ErrModelUnavailable)
	}
	unlock, err := c.engagementInvocation()
	if err != nil {
		return stats, err
	}
	defer unlock()
	model, err := loadEngagementModel(settings)
	if err != nil {
		return stats, err
	}
	if !opts.DryRun {
		if err := c.ensureEngagementSchema(ctx); err != nil {
			return stats, err
		}
	}
	planned := time.Now()
	plan, err := c.engagementWork(ctx, model.identity, opts, stats)
	stats.Plan = time.Since(planned)
	if err != nil {
		return stats, err
	}
	jobs := plan.jobs
	stats.Messages = len(jobs)
	progress("engagement: %d messages to look at of %d typed messages", len(jobs), stats.Candidates)

	grant, err := acquireLabelerCPU(ctx, c, labels.Threads, false)
	if err != nil {
		return stats, err
	}
	defer grant.Release() // the runner's cleanup below happens before this release
	ctx = grant.Ctx
	prep := min(4, max(1, labels.Threads))
	if _, admitted := workAllocationFrom(ctx); admitted {
		labels = admittedLabeler(ctx, labels)
		prep, labels.Threads, _ = labelsSplit(grant.Allocation.CPUs, labels.Threads, false)
		labels.Workers = 1
	}
	stats.Threads, stats.Workers = labels.Threads, max(1, labels.Workers)

	var runner *labelrt.Runner
	release := func() {}
	defer func() {
		if runner != nil && ctx.Err() != nil {
			// A stop: batches already queued at the workers must not run on.
			c.labelers.Kill(runner)
		}
		release()
	}()
	infer := func(rows []labelrt.Framed) ([][]labelrt.TokenLabel, error) {
		if runner == nil {
			cfg := labelrt.RunnerConfig{Worker: labels.Worker, Args: runtimeArgs(labels), Workers: max(1, labels.Workers), Threads: labels.Threads,
				MaxBatch: max(1, labels.MaxBatch), Backoff: labelsWorkerBackoff,
				Options:   map[string]any{"sparse": labelrt.SparseOption{Output: labelrt.TokenLabelOutput, Activation: "sigmoid", Floor: model.floor}},
				OnRestart: func(slot int, cause error) { progress("engagement: restarting worker %d after: %v", slot, cause) }}
			var err error
			if runner, release, err = c.labelerRunner(ctx, labels, model.Model, cfg); err != nil {
				runner, release = nil, func() {}
				return nil, err
			}
			ready := runner.Pool().Ready()
			stats.Device, stats.WorkerPIDs, stats.Workers = ready.Worker.Device, runner.Pool().PIDs(), len(runner.Pool().PIDs())
			progress("engagement: %s on %s, %d × %d threads", model.identity, ready.Worker.Runtime, stats.Workers, labels.Threads)
		}
		return runner.RunSparse(ctx, rows, labelrt.TokenLabelOutput, labels.BatchTokens, nil)
	}

	remaining := map[string]int{}
	for _, job := range jobs {
		remaining[job.conversation]++
	}
	// Reading and framing run ahead of inference, at most two chunks, so the
	// catalog's I/O (the context lookup, cold) overlaps the model's time.
	type prepared struct {
		start int
		chunk []engagementJob
		items []*engagementItem
		err   error
	}
	reading, cancel := context.WithCancel(ctx)
	defer cancel()
	ahead := make(chan prepared, 2)
	var scored atomic.Int64 // messages framed for scoring so far, for Limit
	var prepareStats EngagementRunStats
	var producer sync.WaitGroup
	producer.Add(1)
	go func() {
		defer producer.Done()
		defer close(ahead)
		previous := newEngagementContexts(c)
		for start := 0; start < len(jobs); start += engagementChunkMessages {
			if reading.Err() != nil || opts.Limit > 0 && scored.Load() >= int64(opts.Limit) {
				return
			}
			next := prepared{start: start, chunk: jobs[start:min(len(jobs), start+engagementChunkMessages)]}
			opFrom(ctx).RunIO(func() {
				next.items, next.err = c.engagementItems(reading, model, next.chunk, previous, prep, &prepareStats)
			})
			for _, item := range next.items {
				if item.outcome == "scored" {
					scored.Add(1)
				}
			}
			select {
			case ahead <- next:
			case <-reading.Done():
				return
			}
			if next.err != nil {
				return
			}
		}
	}()
	// settle stops the reader and takes its times (which overlap inference's).
	settle := func() {
		cancel()
		producer.Wait()
		stats.Read, stats.Frame = prepareStats.Read, prepareStats.Frame
	}
	defer settle()
	processed := 0
	for next := range ahead {
		if err := ctx.Err(); err != nil {
			return stats, context.Cause(ctx)
		}
		if next.err != nil {
			return stats, next.err
		}
		start, chunk, items := next.start, next.chunk, next.items
		var err error
		processed = start + len(chunk)
		rows := []labelrt.Framed{}
		for _, item := range items {
			item.first = len(rows)
			for _, window := range item.windows {
				rows = append(rows, labelrt.Framed{IDs: window.IDs})
				stats.Tokens += len(window.IDs)
			}
		}
		stats.Windows += len(rows)
		if len(rows) > 0 && !opts.DryRun {
			inferStarted := time.Now()
			entries, err := infer(rows)
			stats.Infer += time.Since(inferStarted)
			if err != nil {
				return stats, err
			}
			decodeStarted := time.Now()
			for _, item := range items {
				if len(item.windows) == 0 {
					continue
				}
				merged := labelrt.MergeWindows(item.windows, entries[item.first:item.first+len(item.windows)])
				item.spans = engagementSpans(model, item.text, item.target, labelrt.DecodeSpans(item.target.text, item.offsets, merged, model.Thresholds))
			}
			stats.Decode += time.Since(decodeStarted)
		}
		finished := []string{}
		for _, job := range chunk {
			if remaining[job.conversation]--; remaining[job.conversation] == 0 {
				finished = append(finished, job.conversation)
			}
		}
		if !opts.DryRun {
			writeStarted := time.Now()
			opFrom(ctx).RunIO(func() { err = c.storeEngagement(ctx, model.identity, items, finished, plan.tokens, stats) })
			stats.Write += time.Since(writeStarted)
			if err != nil {
				return stats, err
			}
		}
		for _, item := range items {
			if item.outcome == "" {
				stats.MessagesUnchanged++
			} else if item.outcome == "scored" {
				stats.MessagesScored++
			} else {
				stats.MessagesSkipped++
			}
		}
		done := min(len(jobs), start+engagementChunkMessages)
		progress("engagement: %d of %d messages; %d scored, %d spans; inference %s", done, len(jobs), stats.MessagesScored, stats.Spans, stats.Infer.Round(time.Millisecond))
		publishEngagementProgress(ctx, done, len(jobs))
	}
	settle()
	if err := ctx.Err(); err != nil {
		return stats, context.Cause(ctx)
	}
	stats.Cut = processed < len(jobs)
	if !stats.Cut && len(opts.Conversations) == 0 && !opts.DryRun {
		// Everything typed has a row from this model: later runs look only at
		// changed conversations.
		if err := c.writeTransaction(ctx, "mark engagement complete", func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('engagement_complete',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, model.identity)
			return err
		}); err != nil {
			return stats, err
		}
		stats.Complete = true
	}
	progress("engagement: %d messages scored (%d unchanged, %d not scored), %d windows, %d tokens, %d spans; reading %s, framing %s, inference %s, decoding %s, saving %s",
		stats.MessagesScored, stats.MessagesUnchanged, stats.MessagesSkipped, stats.Windows, stats.Tokens, stats.Spans, stats.Read.Round(time.Millisecond),
		stats.Frame.Round(time.Millisecond), stats.Infer.Round(time.Millisecond), stats.Decode.Round(time.Millisecond), stats.Write.Round(time.Millisecond))
	return stats, nil
}

// publishEngagementProgress reports the run's position to its operation.
func publishEngagementProgress(ctx context.Context, done, total int) {
	if op := opFrom(ctx); op != nil {
		op.progress("Messages", "", "message", float64(done), float64(total), total > 0)
	}
}

// engagementInvocation takes the pass's locks: one run at a time in this
// process, and across processes through a lease the OS releases if the
// holder exits. Either held elsewhere is errEngagementRunning.
func (c *Catalog) engagementInvocation() (release func(), err error) {
	if !c.engagementRun.TryLock() {
		return nil, errEngagementRunning
	}
	if c.Path == "" {
		return c.engagementRun.Unlock, nil
	}
	lease, err := fileLease(canonical(c.Path)+".engagement.lock", true)
	if err != nil {
		c.engagementRun.Unlock()
		if errors.Is(err, filelock.ErrContended) {
			return nil, fmt.Errorf("%w: %w", errEngagementRunning, err)
		}
		return nil, fmt.Errorf("lock the engagement pass: %w", err)
	}
	return func() { lease.Close(); c.engagementRun.Unlock() }, nil
}

// engagementPlan is a run's work: the messages to look at, newest first, and
// the dirty marks read (a mark is cleared only if it is still the one read).
type engagementPlan struct {
	jobs   []engagementJob
	tokens map[string]string
}

// engagementWork chooses the messages to look at, newest first: typed
// messages with no state row from this model, and every typed message of a
// changed conversation; every one for Full. Once a run has completed for
// this model, only changed conversations are read. Rows of messages that
// are no longer typed messages of a changed conversation are removed, and
// marks of conversations with nothing to look at are cleared.
func (c *Catalog) engagementWork(ctx context.Context, identity string, opts EngagementRunOptions, stats *EngagementRunStats) (*engagementPlan, error) {
	plan := &engagementPlan{tokens: map[string]string{}}
	present := engagementPresent(ctx, c.DB)
	if err := c.DB.QueryRowContext(ctx, `SELECT count(*) FROM (`+engagementCandidatesSQL+`)`).Scan(&stats.Candidates); err != nil {
		return nil, err
	}
	stored := map[string][2]string{} // message → conversation, hash ('' when another model's)
	complete := ""
	if present {
		rows, err := queryMapsContext(ctx, c.DB, `SELECT conversation_id,token FROM engagement_dirty`)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			plan.tokens[firstString(row["conversation_id"])] = firstString(row["token"])
		}
		complete, _ = c.metaValue(ctx, "engagement_complete")
	}
	only := map[string]bool{}
	for _, id := range opts.Conversations {
		only[id] = true
	}
	// After a complete run, new and changed messages arrive only through
	// the dirty table.
	dirtyOnly := present && complete == identity && !opts.Full && len(only) == 0
	query, args := engagementCandidatesSQL, []any{}
	switch {
	case len(only) > 0:
		query += ` AND a.conversation_id IN (` + placeholders(len(opts.Conversations)) + `)`
		for _, id := range opts.Conversations {
			args = append(args, id)
		}
	case dirtyOnly:
		query += ` AND a.conversation_id IN (SELECT conversation_id FROM engagement_dirty)`
	}
	if present {
		filter := ``
		if dirtyOnly {
			filter = ` WHERE conversation_id IN (SELECT conversation_id FROM engagement_dirty)`
		}
		rows, err := c.DB.QueryContext(ctx, `SELECT message_id,conversation_id,input_hash,model FROM engagement_messages`+filter)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, conversation, hash, made string
			if err := rows.Scan(&id, &conversation, &hash, &made); err != nil {
				rows.Close()
				return nil, err
			}
			if made != identity {
				hash = ""
			}
			stored[id] = [2]string{conversation, hash}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	rows, err := c.DB.QueryContext(ctx, query+` ORDER BY a.sent_at DESC, a.message_id`, args...)
	if err != nil {
		return nil, err
	}
	candidates, withWork := map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var job engagementJob
		var sent sql.NullString
		if err := rows.Scan(&job.id, &job.conversation, &sent, &job.order); err != nil {
			rows.Close()
			return nil, err
		}
		candidates[job.id] = true
		row, built := stored[job.id]
		job.storedHash = row[1]
		if opts.Full || len(only) > 0 || !built || row[1] == "" || plan.tokens[job.conversation] != "" {
			plan.jobs = append(plan.jobs, job)
			withWork[job.conversation] = true
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if opts.DryRun || !present {
		return plan, nil
	}
	// Changed conversations: rows of messages that are no longer candidates
	// go, and a mark with no message to look at is done.
	gone, idle := []string{}, []string{}
	for id, row := range stored {
		if conversation := row[0]; !candidates[id] && plan.tokens[conversation] != "" && (len(only) == 0 || only[conversation]) {
			gone = append(gone, id)
		}
	}
	for conversation := range plan.tokens {
		if !withWork[conversation] && (len(only) == 0 || only[conversation]) {
			idle = append(idle, conversation)
		}
	}
	if len(gone)+len(idle) == 0 {
		return plan, nil
	}
	sort.Strings(gone)
	sort.Strings(idle)
	stats.MessagesRemoved = len(gone)
	return plan, c.writeTransaction(ctx, "clear engagement rows of changed conversations", func(tx *sql.Tx) error {
		for _, id := range gone {
			for _, statement := range []string{`DELETE FROM engagement_spans WHERE message_id=?`, `DELETE FROM engagement_messages WHERE message_id=?`} {
				if _, err := tx.ExecContext(ctx, statement, id); err != nil {
					return err
				}
			}
		}
		for _, conversation := range idle {
			if _, err := tx.ExecContext(ctx, `DELETE FROM engagement_dirty WHERE conversation_id=? AND token=?`, conversation, plan.tokens[conversation]); err != nil {
				return err
			}
		}
		return nil
	})
}

// engagementContexts finds the agent message before a typed message, keeping
// each conversation's agent messages for the run.
type engagementContexts struct {
	c *Catalog
	// replies are a conversation's agent messages, by source order.
	replies map[string][]engagementReply
}

type engagementReply struct {
	order int64
	id    string
}

func newEngagementContexts(c *Catalog) *engagementContexts {
	return &engagementContexts{c: c, replies: map[string][]engagementReply{}}
}

// before is the id of the last agent message before order in conversation
// ("" when there is none).
func (p *engagementContexts) before(ctx context.Context, conversation string, order sql.NullInt64) (string, error) {
	if !order.Valid {
		return "", nil
	}
	replies, ok := p.replies[conversation]
	if !ok {
		rows, err := p.c.reader().QueryContext(ctx, `SELECT id,source_order FROM messages WHERE conversation_id=? AND role='assistant' AND kind='message' AND source_order IS NOT NULL`, conversation)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var reply engagementReply
			if err := rows.Scan(&reply.id, &reply.order); err != nil {
				rows.Close()
				return "", err
			}
			replies = append(replies, reply)
		}
		if err := rows.Close(); err != nil {
			return "", err
		}
		sort.Slice(replies, func(a, b int) bool { return replies[a].order < replies[b].order })
		p.replies[conversation] = replies
	}
	at := sort.Search(len(replies), func(i int) bool { return replies[i].order >= order.Int64 })
	if at == 0 {
		return "", nil
	}
	return replies[at-1].id, nil
}

// engagementItems reads a chunk's messages and frames those whose inputs
// changed: target text and context, token ids with offsets, and windows.
// Tokenizing runs on workers goroutines.
func (c *Catalog) engagementItems(ctx context.Context, model *engagementModel, chunk []engagementJob, previous *engagementContexts, workers int, stats *EngagementRunStats) ([]*engagementItem, error) {
	readStarted := time.Now()
	ids := make([]any, len(chunk))
	for i, job := range chunk {
		ids[i] = job.id
	}
	texts, spans := map[string]string{}, map[string]string{}
	rows, err := c.reader().QueryContext(ctx, `SELECT m.id,COALESCE(m.text,''),a.spans_json FROM messages m JOIN message_authorship a ON a.message_id=m.id WHERE m.id IN (`+placeholders(len(ids))+`)`, ids...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, text, raw string
		if err := rows.Scan(&id, &text, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		texts[id], spans[id] = text, raw
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	before := make([]string, len(chunk))
	wanted := []any{}
	for i, job := range chunk {
		if before[i], err = previous.before(ctx, job.conversation, job.order); err != nil {
			return nil, err
		}
		if before[i] != "" {
			wanted = append(wanted, before[i])
		}
	}
	type tail struct {
		text  string
		runes int
	}
	tails := map[string]tail{}
	if len(wanted) > 0 {
		// substr and length count characters, as the reference's slice does.
		rows, err := c.reader().QueryContext(ctx, fmt.Sprintf(`SELECT id,COALESCE(substr(text,-%d),''),COALESCE(length(text),0) FROM messages WHERE id IN (`, engagementContextRunes)+placeholders(len(wanted))+`)`, wanted...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			var value tail
			if err := rows.Scan(&id, &value.text, &value.runes); err != nil {
				rows.Close()
				return nil, err
			}
			tails[id] = value
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	stats.Read += time.Since(readStarted)

	frameStarted := time.Now()
	items := make([]*engagementItem, len(chunk))
	var wg sync.WaitGroup
	next := make(chan int)
	for range max(1, min(workers, len(chunk))) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				job := chunk[i]
				item := &engagementItem{job: job, text: texts[job.id]}
				var authored []authorSpan
				// A message whose spans cannot be read has nothing typed.
				_ = json.Unmarshal([]byte(spans[job.id]), &authored)
				item.target = engagementTargetText(item.text, authored)
				context := ""
				if reply, ok := tails[before[i]]; ok {
					context = engagementContext(reply.text, reply.runes)
				}
				item.hash = engagementInputHash(item.target, context)
				items[i] = item
				if job.storedHash == item.hash {
					continue // outcome "": its rows stand
				}
				if item.outcome = item.target.scorable(); item.outcome != "" {
					continue
				}
				item.outcome = "scored"
				var ids []int32
				ids, item.offsets = model.Tokenizer.EncodeOffsets(item.target.text)
				item.windows = model.FrameWindows(model.Tokenizer.Encode(context), ids)
			}
		}()
	}
	for i := range chunk {
		next <- i
	}
	close(next)
	wg.Wait()
	stats.Frame += time.Since(frameStarted)
	return items, nil
}

// storeEngagement writes a chunk in one transaction: each changed message's
// spans and state row, and the dirty marks (the ones this run read) of
// conversations whose last message the chunk holds.
func (c *Catalog) storeEngagement(ctx context.Context, identity string, items []*engagementItem, finished []string, tokens map[string]string, stats *EngagementRunStats) error {
	at := now()
	var spans, withSpans int
	err := c.writeTransaction(ctx, "store engagement spans", func(tx *sql.Tx) error {
		spans, withSpans = 0, 0
		clear, err := tx.PrepareContext(ctx, `DELETE FROM engagement_spans WHERE message_id=?`)
		if err != nil {
			return err
		}
		defer clear.Close()
		insert, err := tx.PrepareContext(ctx, `INSERT INTO engagement_spans(message_id,position,label,start_byte,end_byte,score) VALUES(?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer insert.Close()
		state, err := tx.PrepareContext(ctx, `INSERT INTO engagement_messages(message_id,conversation_id,input_hash,model,outcome,tokens,spans,processed_at) VALUES(?,?,?,?,?,?,?,?)
			ON CONFLICT(message_id) DO UPDATE SET conversation_id=excluded.conversation_id, input_hash=excluded.input_hash, model=excluded.model,
			outcome=excluded.outcome, tokens=excluded.tokens, spans=excluded.spans, processed_at=excluded.processed_at`)
		if err != nil {
			return err
		}
		defer state.Close()
		for _, item := range items {
			if item.outcome == "" {
				continue
			}
			if _, err := clear.ExecContext(ctx, item.job.id); err != nil {
				return err
			}
			for position, span := range item.spans {
				if _, err := insert.ExecContext(ctx, item.job.id, position, span.label, span.start, span.end, span.score); err != nil {
					return err
				}
			}
			if _, err := state.ExecContext(ctx, item.job.id, item.job.conversation, item.hash, identity, item.outcome, len(item.offsets), len(item.spans), at); err != nil {
				return err
			}
			spans += len(item.spans)
			if len(item.spans) > 0 {
				withSpans++
			}
		}
		for _, conversation := range finished {
			if token := tokens[conversation]; token != "" {
				// Only the mark this run read: one written since stays.
				if _, err := tx.ExecContext(ctx, `DELETE FROM engagement_dirty WHERE conversation_id=? AND token=?`, conversation, token); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err == nil {
		stats.Spans += spans
		stats.MessagesWithSpans += withSpans
	}
	return err
}

// runEngagementCLI: pharos engagement run [flags] | status.
func runEngagementCLI(ctx context.Context, config Config, catalog *Catalog, op *Op, args []string) error {
	if len(args) >= 1 && args[0] == "run" {
		settings, labels := config.Engagement, config.Labels
		flags := flag.NewFlagSet("engagement run", flag.ContinueOnError)
		limit := flags.Int("limit", 0, "score at most about N messages")
		full := flags.Bool("full", false, "look at every typed message")
		dryRun := flags.Bool("dry-run", false, "build and frame the inputs, score and store nothing")
		flags.IntVar(&settings.Threads, "threads", settings.Threads, "threads per worker (0: labels_threads)")
		flags.IntVar(&labels.Workers, "workers", labels.Workers, "worker processes")
		flags.IntVar(&settings.BatchTokens, "batch-tokens", settings.BatchTokens, "padded tokens per batch (0: labels_batch_tokens)")
		flags.IntVar(&labels.MaxBatch, "max-batch", labels.MaxBatch, "rows per batch")
		flags.StringVar(&settings.Variant, "variant", settings.Variant, "model variant in model.json (default fp32)")
		flags.StringVar(&settings.ModelDir, "model-dir", settings.ModelDir, "model directory (default engagement_model_dir)")
		var conversations stringList
		flags.Var(&conversations, "conversation", "only this conversation (repeatable)")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
			return fmt.Errorf("usage: pharos engagement run [--limit N] [--full] [--dry-run] [--conversation ID]... [--threads N] [--workers N] [--batch-tokens N] [--max-batch N] [--variant NAME] [--model-dir DIR]")
		}
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		ctx = withWorkProgress(ctx, op.mirrorWork)
		stats, err := catalog.RunEngagement(ctx, labels, settings, EngagementRunOptions{Limit: *limit, Full: *full, DryRun: *dryRun, Conversations: conversations, Progress: os.Stderr})
		if stats != nil {
			_ = printJSON(stats)
		}
		return err
	}
	if len(args) == 1 && args[0] == "status" {
		status, err := catalog.engagementStatus(ctx, config)
		if err != nil {
			return err
		}
		return printJSON(status)
	}
	return fmt.Errorf("usage: pharos engagement {run [flags] | status}")
}

// engagementChainLimit is the chain stage's share: about this many messages
// scored per pass; the rest stays pending for the next pass.
var engagementChainLimit = 1000

// chainEngagement is the analysis chain's last stage, where
// engagement_enabled is on: the same power rules as the other model stages
// (labelsForPower). It downloads nothing: a library without a worker or a
// model directory leaves the stage pending.
func (s *Server) chainEngagement(ctx context.Context, labels LabelerSettings, settings EngagementSettings) error {
	if labels.withAssets().Worker == "" || settings.ModelDir == "" {
		return errStageContinues
	}
	// On battery labelsForPower narrows the threads and batches; the pass
	// then takes them as given.
	labels, _ = labelsForPower(settings.labeler(labels.withAssets()), s.cachedPower(ctx))
	settings.Threads, settings.BatchTokens = 0, 0
	stats, err := s.Catalog.RunEngagement(ctx, labels, settings, EngagementRunOptions{Limit: engagementChainLimit, Progress: os.Stderr})
	switch {
	case errors.Is(err, errEngagementRunning):
		return errStageContinues
	case err != nil:
		return err
	case stats.Cut || !stats.Complete:
		return errStageContinues
	}
	return nil
}
