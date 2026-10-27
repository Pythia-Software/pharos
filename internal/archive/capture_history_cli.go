package archive

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

func runHistoryCLI(config Config, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: pharos history-backfill plan|prepare|resume|status|rebuild|verify|budget|release [options]")
	}
	flags := flag.NewFlagSet("history-backfill "+args[0], flag.ContinueOnError)
	host := flags.String("host", "", "original captured host")
	source := flags.String("source", "", "source name")
	limit := flags.Int("limit", 100, "plan page size (1..256)")
	cursor := flags.String("cursor", "", "stable plan continuation")
	expected := flags.String("plan", "", "reviewed dry-run plan id")
	rollback := flags.String("rollback", "", "rollback backup on another drive")
	output := flags.Int64("output-budget", 0, "maximum uncompressed export bytes")
	scratchBudget := flags.Int64("scratch-budget", 0, "scratch catalog and WAL byte budget")
	scratch := flags.String("scratch", "", "new isolated scratch catalog path")
	release := flags.Bool("accept-unresolved", false, "explicitly abandon unresolved input protection; retained evidence is kept")
	if e := flags.Parse(args[1:]); e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "plan", "prepare":
		if flags.NArg() != 0 {
			return errors.New("use --host and --source to scope a dry run")
		}
		plan, e := planHistory(ctx, config, historyScope{Host: *host, Source: *source})
		if e != nil {
			return e
		}
		if args[0] == "plan" {
			page, e := historyPage(plan, *cursor, *limit)
			if e != nil {
				return e
			}
			return printJSON(page)
		}
		if *expected == "" || *expected != plan.ID {
			return errors.New("prepare requires the unchanged reviewed --plan ID; run plan first")
		}
		job, e := prepareHistory(ctx, config, plan, *rollback, *output, *scratchBudget)
		if p := job.ID; p != "" {
			_ = printJSON(historyJobSummary(job))
		}
		return e
	case "resume", "status", "rebuild", "verify", "budget", "release":
		if flags.NArg() != 1 {
			return errors.New("expected one exact history job id")
		}
		id := flags.Arg(0)
		if args[0] == "resume" {
			job, e := runHistory(ctx, config, id)
			_ = printJSON(historyJobSummary(job))
			return e
		}
		if args[0] == "verify" {
			if *scratch == "" {
				return errors.New("verify requires --scratch /absolute/new/catalog.sqlite3")
			}
			report, e := verifyHistory(ctx, config, id, *scratch)
			_ = printJSON(report)
			return e
		}
		job, e := loadHistoryJob(config.CaptureRoot, id)
		if e != nil {
			return e
		}
		if args[0] == "rebuild" {
			if *scratch == "" {
				return errors.New("rebuild requires --scratch /absolute/new/catalog.sqlite3")
			}
			v, c, e := job.rebuild(ctx, config, *scratch)
			if c != nil {
				c.Close()
			}
			if e != nil {
				v.State = "rebuild-failed"
			} else {
				v.State = "rebuilt-pending-comparison"
			}
			_ = printJSON(v)
			return e
		}
		if args[0] == "budget" {
			lock, e := historyJobLock(job.dir())
			if e != nil {
				return e
			}
			defer lock.Close()
			if *output < job.OutputBudget || *scratchBudget < job.ScratchBudget || *output > 1<<50 || *scratchBudget > 1<<50 {
				return errors.New("reviewed budgets must be explicit, positive and at least the recorded budgets")
			}
			if e = ensureFree(config.CaptureRoot, *output-job.OutputBudget); e != nil {
				return e
			}
			job.OutputBudget = *output
			job.ScratchBudget = *scratchBudget
			if e = job.save(); e != nil {
				return e
			}
			return printJSON(historyJobSummary(job))
		}
		if args[0] == "release" {
			if !*release {
				return errors.New("release requires --accept-unresolved after reviewing the unresolved pins and verified retained output")
			}
			lock, e := historyJobLock(job.dir())
			if e != nil {
				return e
			}
			defer lock.Close()
			if e = job.finishPauses(ctx, true); e != nil {
				return e
			}
			job.State = "protection-released"
			job.Error = "operator released unresolved protection; this is not coverage verification"
			return job.save()
		}
		_, summary, e := job.summarizeReceipts(ctx, false)
		if e != nil {
			return e
		}
		pins := map[string]any{}
		for _, s := range job.Plan.Sources {
			p, e := loadPreservationPins(historySourceDir(config.CaptureRoot, s))
			if e != nil {
				return e
			}
			owners := map[string]int{}
			var bytes int64
			for _, pin := range p.Pins {
				if pin.ReleasedAt == "" {
					owners[pin.Owner]++
					bytes += max(pin.Size, pin.ClosureBytes)
				}
			}
			pins[s.Host.ID+"/"+s.Source.Name] = map[string]any{"active_owners": owners, "pinned_bytes": bytes, "native_snapshot_pauses": p.NativePauses, "acquisition_allowances": p.Gates}
		}
		var verification historyVerification
		_ = historyReadJSON(filepath.Join(job.dir(), "verification.json"), &verification)
		return printJSON(map[string]any{"job": historyJobSummary(job), "coverage": summary, "pins_and_native_pauses": pins, "verification": verification, "note": fmt.Sprintf("Fixed cutoff %s; status reads receipt metadata; verify rehashes evidence. Index checkpoints are not historical coverage receipts", job.Plan.ID)})
	default:
		return errors.New("unknown history-backfill command")
	}
}

func historyJobSummary(job historyJob) map[string]any {
	plan := job.Plan
	count := len(plan.Units)
	plan.Units = nil
	return map[string]any{"id": job.ID, "state": job.State, "phase": job.Phase, "cursor": job.Cursor, "units": count, "protected_inputs": len(job.Protected), "rollback_destination": job.Backup, "oracle": job.Oracle, "output_bytes": job.OutputBytes, "uncertain_output_bytes": job.UncertainOutputBytes, "attempt_reserved_bytes": job.AttemptBytes, "output_budget_bytes": job.OutputBudget, "scratch_budget_bytes": job.ScratchBudget, "error": job.Error, "updated_at": job.UpdatedAt, "plan": plan}
}
