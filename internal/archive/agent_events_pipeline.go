package archive

import (
	"context"
	"fmt"
	"time"
)

// agentEventsPipeline keeps preparation, inference and persistence independent.
// Only the producer uses the mutable item builder; only the coordinator uses
// the runner. The writer owns its counts until all three stages have joined.
// Stage functions also let the scheduling contract be tested without a model
// process or timing-dependent database locks.
type agentEventsPipeline struct {
	opts     AgentEventsRunOptions
	stats    *AgentEventsRunStats
	progress func(string, ...any)
	status   *agentEventsProgress
	build    func(*agentEventsConversation) (*agentEventsConversation, error)
	infer    func([]*agentEventsConversation) error
	store    func(*agentEventsConversation, *AgentEventsRunStats) error
}

type agentEventsPrepared struct {
	conversation *agentEventsConversation
	index, slots int
	err          error
}

// weight bounds retained candidates and message bookkeeping, including
// conversations with no candidates. Rows have the model's bounded sequence
// length. The preparation budget ends after inference, so a slow save cannot
// prevent the next oversized conversation from reaching the GPU.
func (conversation *agentEventsConversation) weight() int {
	weight := 1 + len(conversation.messages) + len(conversation.stale) + len(conversation.retimed)
	for _, message := range conversation.messages {
		weight += len(message.rows)
	}
	return weight
}

// A persistence panic must reach the coordinator as an error now that saves
// have their own goroutine. The transaction's deferred rollback runs first.
func (p *agentEventsPipeline) save(conversation *agentEventsConversation, stats *AgentEventsRunStats) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = fmt.Errorf("storing agent events for %s panicked: %v", conversation.id, value)
		}
	}()
	return p.store(conversation, stats)
}

func (p *agentEventsPipeline) run(ctx context.Context, cancel context.CancelFunc, jobs []*agentEventsConversation) error {
	if p.status == nil {
		p.status = newAgentEventsProgress(ctx, p.opts.progressBase, time.Now(), len(jobs), p.opts.DryRun)
	}
	defer func() { p.stats.progress = p.status.snapshot() }()
	window := max(1, agentEventsChunkRows)
	slots := make(chan struct{}, 3*window)
	release := func(job agentEventsPrepared) {
		for range job.slots {
			<-slots
		}
	}
	built := make(chan agentEventsPrepared, window)
	type production struct {
		cut  bool
		took time.Duration
	}
	produced := make(chan production, 1)
	go func() {
		var result production
		defer func() { close(built); produced <- result }()
		messages := 0
		for index, job := range jobs {
			if ctx.Err() != nil {
				return
			}
			if p.opts.Limit > 0 && messages >= p.opts.Limit {
				result.cut = true
				return
			}
			started := time.Now()
			// Keep the catalog-wide work list as metadata only. The builder
			// fills its job in place; mutating the listed job would retain
			// every completed conversation until the entire backlog ended.
			work := *job
			p.status.stage("preparing", true)
			conversation, err := p.build(&work)
			p.status.stage("preparing", false)
			result.took += time.Since(started)
			if ctx.Err() != nil {
				return
			}
			ready := agentEventsPrepared{conversation: conversation, index: index + 1, err: err}
			if err == nil {
				messages += len(conversation.messages)
				// The budget follows a job until inference releases its token arrays.
				// Partial acquisition is safe: there is only one producer, and
				// inference flushes available work without waiting for a full window.
				for range min(conversation.weight(), cap(slots)) {
					select {
					case slots <- struct{}{}:
						ready.slots++
					case <-ctx.Done():
						release(ready)
						return
					}
				}
			}
			select {
			case built <- ready:
			case <-ctx.Done():
				release(ready)
				return
			}
		}
	}()

	writes := make(chan []agentEventsPrepared, 1)
	type persistence struct {
		stats AgentEventsRunStats
		err   error
	}
	written := make(chan persistence, 1)
	go func() {
		var result persistence
		for chunk := range writes {
			for i, job := range chunk {
				if result.err == nil && ctx.Err() == nil {
					started := time.Now()
					p.status.stage("saving", true)
					result.err = p.save(job.conversation, &result.stats)
					result.stats.Write += time.Since(started)
					if result.err == nil {
						p.status.saved(job.conversation)
					}
					p.status.stage("saving", false)
					if result.err != nil {
						cancel()
					}
				}
				chunk[i] = agentEventsPrepared{}
			}
		}
		written <- result
	}()

	coordinate := func() error {
		for {
			var first agentEventsPrepared
			select {
			case <-ctx.Done():
				return ctx.Err()
			case job, ok := <-built:
				if !ok {
					return nil
				}
				first = job
			}
			chunk := []agentEventsPrepared{}
			weight := 0
			accept := func(job agentEventsPrepared) {
				if job.err != nil {
					p.stats.ConversationsFailed++
					p.progress("agent events: conversation %s: %v", jobs[job.index-1].id, job.err)
					return
				}
				conversation := job.conversation
				p.stats.ConversationsBuilt++
				p.stats.MessagesUnchanged += conversation.unchanged
				p.stats.MessagesBefore += conversation.before
				p.stats.MessagesScored += len(conversation.messages)
				for _, message := range conversation.messages {
					p.stats.Candidates += len(message.rows)
				}
				chunk = append(chunk, job)
				weight += conversation.weight()
			}
			accept(first)
			// Flush as soon as the producer pauses, so slow input reads cannot
			// leave a partial window waiting while the worker has nothing to do.
		gather:
			for weight < window {
				select {
				case job, ok := <-built:
					if !ok {
						break gather
					}
					accept(job)
				default:
					break gather
				}
			}
			conversations := make([]*agentEventsConversation, len(chunk))
			for i, job := range chunk {
				conversations[i] = job.conversation
			}
			if !p.opts.DryRun && len(chunk) > 0 {
				if err := p.infer(conversations); err != nil {
					for _, job := range chunk {
						release(job)
					}
					return err
				}
				// Persistence needs sentence spans and outputs, never token IDs.
				// Release inference credits before waiting for the bounded writer.
				for i, job := range chunk {
					for _, message := range job.conversation.messages {
						for k := range message.rows {
							message.rows[k].IDs = nil
						}
					}
					release(job)
					chunk[i].slots = 0
				}
				select {
				case writes <- chunk:
					continue
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			for _, job := range chunk {
				if p.opts.DryRun {
					p.status.saved(job.conversation)
				}
				release(job)
			}
		}
	}
	err := coordinate()
	if err != nil {
		cancel()
	}
	// Every handoff has an owner even on failure. Join the producer before
	// returning or releasing its builder; drain anything it already handed off.
	for job := range built {
		release(job)
	}
	productionResult := <-produced
	close(writes)
	writeResult := <-written
	p.stats.Prepare += productionResult.took
	p.stats.Cut = productionResult.cut
	p.stats.Write += writeResult.stats.Write
	p.stats.Stored += writeResult.stats.Stored
	p.stats.Events += writeResult.stats.Events
	p.stats.MessagesRemoved += writeResult.stats.MessagesRemoved
	if writeResult.err != nil {
		return writeResult.err
	}
	if err != nil {
		return err
	}
	return ctx.Err()
}
