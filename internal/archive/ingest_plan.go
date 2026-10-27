package archive

import (
	"context"
	"errors"
	"strings"
	"time"
)

type partEmitter func(*WorkspaceRecord, []sourcePart) error

type plannedPartGroup struct {
	parts []sourcePart
	read  func(partEmitter) error
}

// A plan retains discovery metadata and executable readers, never parsed
// transcripts. Its membership is bounded even while live sources keep growing.
type sourceWorkPlan struct {
	reader            Adapter
	sqliteAcquisition *sqliteAcquisitionBoundary
	groups            []plannedPartGroup
	tracker           *partTracker
	needed            int
	size              *activitySize
	features          workFeatures
	discoveryError    error
	continueOnError   bool
	discoverySeconds  float64
	captureSignature  string
}

type partialPlanner interface {
	planParts() (*sourceWorkPlan, error)
}

func (p *sourceWorkPlan) prepare(tracker *partTracker) {
	p.tracker, p.needed, p.size, p.features = tracker, 0, nil, workFeatures{}
	size, known := &activitySize{}, true
	for _, group := range p.groups {
		if tracker.unchanged(group.parts) {
			continue
		}
		p.needed++
		amount, unit := partWorkSize(group.parts)
		if unit == "" || size.Unit != "" && size.Unit != unit {
			known = false
		}
		size.Unit, size.Total = unit, size.Total+amount
		for _, part := range group.parts {
			if strings.HasPrefix(part.item, "group:") || strings.HasPrefix(part.item, "sqlite:") && part.sizeUnit != "message" {
				continue
			}
			if part.sizeUnit == "message" {
				p.features.Messages += float64(part.size)
			} else {
				p.features.Bytes += float64(part.size)
				p.features.Files++
			}
		}
	}
	p.features.Groups = float64(p.needed)
	if p.features.Bytes > 0 {
		p.features.BytesBasis = "transcript"
	}
	if known && size.Unit != "" {
		p.size = size
	}
}

func (p *sourceWorkPlan) observe(record *WorkspaceRecord) {
	p.features.Conversations += float64(len(record.Conversations))
	database := p.size != nil && p.size.Unit == "message"
	for _, conversation := range record.Conversations {
		if !database {
			p.features.Messages += float64(len(conversation.Messages))
			continue
		}
		p.features.BytesBasis = "message_text"
		for _, message := range conversation.Messages {
			text := message.RawText
			if text == "" {
				text = message.Text
			}
			p.features.Bytes += float64(len(text))
		}
	}
}

func (p *sourceWorkPlan) discover(ctx context.Context, unchanged func([]sourcePart) bool, emit partEmitter) error {
	failures := []error{p.discoveryError}
	for _, group := range p.groups {
		if err := ctx.Err(); err != nil {
			return err
		}
		if unchanged != nil && unchanged(group.parts) {
			continue
		}
		var writeErr error
		err := group.read(func(record *WorkspaceRecord, parts []sourcePart) error {
			if p.size != nil {
				oldAmount, oldUnit := partWorkSize(group.parts)
				amount, unit := partWorkSize(parts)
				if oldUnit == p.size.Unit && unit == p.size.Unit {
					p.size.Total += amount - oldAmount
					if unit == "byte" {
						p.features.Bytes += amount - oldAmount
					} else if unit == "message" {
						p.features.Messages += amount - oldAmount
					}
				}
			}
			writeErr = emit(record, parts)
			return writeErr
		})
		if err != nil {
			if !p.continueOnError || writeErr != nil {
				return err
			}
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (p *sourceWorkPlan) fingerprint() string {
	parts := []sourcePart{}
	for _, group := range p.groups {
		parts = append(parts, group.parts...)
	}
	return "plan-v1:" + partsSignature(parts)
}

func (p *sourceWorkPlan) work(phase, source, model string, done int) activityWork {
	w := activityWork{Phase: phase, Source: source, Unit: "conversation group", Done: float64(done), Total: float64(p.needed), TotalKnown: true, Model: model, Features: p.features}
	if p.size != nil {
		copied := *p.size
		w.Size = &copied
	}
	return w
}

func cloneWork(work activityWork) activityWork {
	if work.Size != nil {
		copied := *work.Size
		work.Size = &copied
	}
	return work
}

func plannedIndexModel(adapter Adapter, force bool) string {
	mode := "changes"
	if force {
		mode = "rebuild"
	}
	return adapter.Config().Kind + ":" + sourceIndexVersion(adapter) + ":" + mode
}

// A measured plan is shared by counting and execution for capture indexes.
func (c *Catalog) prepareSourcePlan(ctx context.Context, adapter Adapter) (*sourceWorkPlan, error) {
	started := time.Now()
	planner, ok := adapter.(partialPlanner)
	if !ok {
		return nil, nil
	}
	ctx = c.withSQLiteAcquisition(ctx, adapter)
	setAdapterContext(adapter, ctx)
	// Planned readers retain this adapter, which may differ from the one
	// subsequently opened to index a capture.
	if a, ok := adapter.(*antigravityAdapter); ok && a.installedVersions == nil {
		if err := a.prepareInstalledVersions(c); err != nil {
			return nil, err
		}
	}
	plan, err := planner.planParts()
	if err != nil {
		return nil, err
	}
	plan.reader = adapter
	plan.sqliteAcquisition, _ = ctx.Value(sqliteAcquisitionKey{}).(*sqliteAcquisitionBoundary)
	partial := adapter.(partialAdapter)
	tracker, err := c.newPartTracker(sourceHost(adapter).ID, adapter.Config().Name, partial, captureViewOf(adapter))
	if err != nil {
		return nil, err
	}
	if modeOf(ctx).force {
		tracker.states = nil
	}
	plan.prepare(tracker)
	if view := captureViewOf(adapter); view != nil {
		plan.captureSignature = view.planSignature()
	}
	plan.features.LoadOneMinute = machineLoad()
	var previous string
	_ = c.reader().QueryRowContext(ctx, "SELECT COALESCE(last_success_at,'') FROM source_states WHERE host_id=? AND source_name=?", sourceHost(adapter).ID, adapter.Config().Name).Scan(&previous)
	if indexed, ok := parseTime(previous); ok {
		plan.features.SinceLastIndexSeconds = max(0, time.Since(indexed).Seconds())
	}
	plan.discoverySeconds = time.Since(started).Seconds()
	return plan, nil
}
