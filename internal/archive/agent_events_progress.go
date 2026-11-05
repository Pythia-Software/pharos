package archive

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// These counts describe work completed in this pass and its bounded predecessor,
// not an estimate of the library's as-yet-unprepared candidate population.
type agentEventsCounts struct {
	conversations, messages, candidates int
	elapsed                             time.Duration
}

// Preparation, batch completion and commits report independently. Publish a
// coherent snapshot under one lock, and limit batch-only updates to once a second.
type agentEventsProgress struct {
	mu                         sync.Mutex
	ctx                        context.Context
	counts                     agentEventsCounts
	total                      int
	started, published         time.Time
	preparing, scoring, saving bool
	device                     string
	dryRun                     bool
}

func newAgentEventsProgress(ctx context.Context, base agentEventsCounts, started time.Time, total int, dryRun bool) *agentEventsProgress {
	p := &agentEventsProgress{ctx: ctx, counts: base, started: started.Add(-base.elapsed), total: base.conversations + total, dryRun: dryRun}
	p.publish(true)
	return p
}

func (p *agentEventsProgress) stage(stage string, active bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch stage {
	case "preparing":
		p.preparing = active
	case "scoring":
		p.scoring = active
	case "saving":
		p.saving = active
	}
	p.publish(true)
}

func (p *agentEventsProgress) setDevice(device string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.device = device
	p.publish(true)
}

func (p *agentEventsProgress) scored(n int, device string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.counts.candidates += n
	p.device = device
	p.publish(false)
}

func (p *agentEventsProgress) saved(conversation *agentEventsConversation) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.counts.conversations++
	p.counts.messages += len(conversation.messages)
	p.publish(true)
}

func (p *agentEventsProgress) snapshot() agentEventsCounts {
	p.mu.Lock()
	defer p.mu.Unlock()
	counts := p.counts
	counts.elapsed = time.Since(p.started)
	return counts
}

func agentEventsCount(n int) string {
	s := strconv.Itoa(n)
	for at := len(s) - 3; at > 0; at -= 3 {
		s = s[:at] + "," + s[at:]
	}
	return s
}

// publish is called with the lock held (or before the tracker is shared).
func (p *agentEventsProgress) publish(force bool) {
	at := time.Now()
	if !force && at.Sub(p.published) < time.Second {
		return
	}
	p.published = at
	phase := "Finishing"
	if p.counts.conversations < p.total {
		phase = "Waiting for prepared conversations"
	}
	switch {
	case p.scoring:
		phase = "Scoring candidates"
		if p.device == "gpu" {
			phase = "Scoring on GPU"
		} else if p.device == "cpu" {
			phase = "Scoring on CPU"
		}
	case p.preparing:
		phase = "Preparing conversations"
	case p.saving:
		phase = "Saving results"
	}
	detail := []string{phase, agentEventsCount(p.counts.messages) + " messages saved", agentEventsCount(p.counts.candidates) + " candidates scored"}
	if seconds := at.Sub(p.started).Seconds(); seconds >= 1 && p.counts.candidates > 0 {
		detail = append(detail, fmt.Sprintf("%.0f candidates/s overall", float64(p.counts.candidates)/seconds))
	}
	if p.device != "" && !p.scoring {
		detail = append(detail, strings.ToUpper(p.device)+" worker")
	}
	name := "finding agent events"
	if p.dryRun {
		name = "preparing agent events (dry run)"
		detail = []string{"Dry run; no results saved"}
	}
	reportWork(p.ctx, name, "conversation", p.counts.conversations, p.total, strings.Join(detail, " · "))
}
