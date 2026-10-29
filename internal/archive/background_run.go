package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
)

// runBackgroundItem runs one item that is not a stage of the analysis
// chain, as a child of the pass's operation.
func (s *Server) runBackgroundItem(ctx context.Context, op *Op, id string) error {
	switch id {
	case "other-sources":
		names := s.backgroundInputsFor(ctx, nil).otherSources
		return s.backgroundCapture(ctx, op, "sources", names)
	case "preserve":
		return s.backgroundCapture(ctx, op, "preserve", nil)
	case "upgrade":
		ctx, child := op.Child(OperationSpec{Kind: "library.upgrade", Trigger: triggerChained})
		err := s.Catalog.RunUpgrade(ctx, nil)
		child.Finish(err)
		if err == nil {
			s.Catalog.storeUpgradeCount(ctx)
		}
		return err
	case "backup":
		return s.backgroundBackup(ctx, op)
	}
	return fmt.Errorf("unknown background item %q", id)
}

// backgroundCapture captures this Mac's sources named (all enabled ones
// when names is nil), then indexes what every Mac captured of them: what
// Capture & index does, without the analysis chain, whose stages then run
// on their own cadences.
func (s *Server) backgroundCapture(ctx context.Context, op *Op, variant string, names []string) error {
	ctx, child := op.Child(OperationSpec{Kind: "background.capture", Variant: variant, Trigger: triggerChained})
	err := s.captureAndIndex(ctx, child, names)
	child.Finish(err)
	return err
}

func (s *Server) captureAndIndex(ctx context.Context, op *Op, names []string) error {
	config := s.Config()
	sources := []SourceConfig{}
	for _, source := range config.Sources {
		if source.Enabled && (names == nil || slices.Contains(names, source.Name)) {
			sources = append(sources, source)
		}
	}
	if len(sources) == 0 {
		return nil
	}
	op.Plan(StepPlan{Name: "Capture this Mac", Total: -1}, StepPlan{Name: "Index captures", Unit: "source", Total: -1})
	op.Step("Capture this Mac", "", -1)
	var failure error
	summary, err := Capture(ctx, config, sources, func(result CaptureSourceResult) {
		work := captureWork(result)
		op.progress(work.Phase, result.Name, work.Unit, work.Done, work.Total, work.TotalKnown)
	})
	if err != nil {
		return err
	}
	if !summary.OK {
		failure = errors.New("some inputs could not be captured; what was captured is indexed")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	targets, err := captureTargets(config.CaptureRoot, nil, true, names)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	op.Step("Index captures", "source", int64(len(targets)))
	wrote := false
	for _, target := range targets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		result := s.Catalog.IndexCapture(withIngestMode(ctx, ingestMode{deferGit: true}), target, nil)
		op.Advance(1)
		wrote = wrote || result.Workspaces > 0
		if result.Error != nil && failure == nil {
			failure = fmt.Errorf("%s: %v", target.label(), result.Error)
		}
	}
	if wrote {
		// The analyses run on their own cadences, as after a scheduled index.
		if err := s.Catalog.writeTransaction(ctx, "background deferred", func(tx *sql.Tx) error { return markSyncDeferred(tx) }); err != nil && failure == nil {
			failure = err
		}
	}
	return failure
}

// backgroundBackup backs up to the last backup's destination, when its
// drive is connected; otherwise it waits for the next day.
func (s *Server) backgroundBackup(ctx context.Context, op *Op) error {
	config := s.Config()
	history := loadBackupHistory(config)
	if len(history.Backups) == 0 {
		return nil
	}
	job, err := prepareBackup(config, history.Backups[0].Destination, false)
	if err != nil {
		return err
	}
	started := job.run.clone()
	backupCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := s.backups.begin(started, cancel); err != nil {
		return err
	}
	_, child := op.Child(OperationSpec{Kind: "backup.local", Subject: started.Destination, Trigger: triggerChained, Legacy: started.ID})
	run := started
	defer func() { s.backups.update(run, true) }()
	run = job.Run(backupCtx, func(progress BackupRun) {
		s.backups.update(progress, false)
		mirrorBackup(child, progress)
	})
	err = backupOutcome(backupCtx, run)
	child.Finish(err)
	return err
}
