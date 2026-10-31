package archive

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	labelrt "github.com/gbdubs/pharos/internal/labeler"
)

const labelsUsage = `usage:
  pharos [--config PATH] labels run [--limit N] [--batch-tokens N] [--max-batch N] [--workers N] [--threads N] [--full] [--one-at-a-time] [--dry-run] [--json] [--export-items FILE [--export-text]]
  pharos [--config PATH] labels status [--json]
  pharos [--config PATH] labels fetch
  pharos [--config PATH] labels explain MESSAGE_ID [--infer]`

// labelsCLIOperation is the operation a labels command runs as: run writes
// the ledger (analysis.labels); status and explain only read.
func labelsCLIOperation(args []string) OperationSpec {
	if len(args) > 0 && args[0] == "run" {
		return OperationSpec{Kind: "analysis.labels"}
	}
	return OperationSpec{}
}

// runLabelsCLI: pharos labels run | status | explain.
func runLabelsCLI(ctx context.Context, config Config, catalog *Catalog, op *Op, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(labelsUsage)
	}
	settings := config.Labels.withAssets()
	switch args[0] {
	case "fetch":
		if len(args) != 1 {
			return errors.New(labelsUsage)
		}
		return fetchLabelsCLI(ctx, settings, stdout)
	case "run":
		flags := flag.NewFlagSet("labels run", flag.ContinueOnError)
		flags.SetOutput(stderr)
		limit := flags.Int("limit", 0, "label at most N messages")
		flags.IntVar(&settings.BatchTokens, "batch-tokens", settings.BatchTokens, "padded tokens per batch")
		flags.IntVar(&settings.MaxBatch, "max-batch", settings.MaxBatch, "items per batch")
		flags.IntVar(&settings.Workers, "workers", settings.Workers, "worker processes")
		flags.IntVar(&settings.Threads, "threads", settings.Threads, "threads per worker")
		full := flags.Bool("full", false, "rebuild every conversation's items")
		single := flags.Bool("one-at-a-time", false, "one item per batch, one batch at a time (a baseline)")
		asJSON := flags.Bool("json", false, "print the run's statistics as JSON")
		dryRun := flags.Bool("dry-run", false, "build and frame items, label and store nothing")
		exportPath := flags.String("export-items", "", "diagnostic: write one JSON line per item to FILE (created 0600; holds conversation text with --export-text)")
		exportText := flags.Bool("export-text", false, "with --export-items: include context, text and token ids")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
			return errors.New(labelsUsage)
		}
		if !settings.Enabled {
			fmt.Fprintln(stderr, "labels_enabled is off: indexing will not label messages; this run labels them once.")
		}
		// Interrupt stops between batches, keeping what was stored.
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		ctx = withWorkProgress(ctx, op.mirrorWork)
		if err := catalog.ensureAuthorshipForLabels(ctx, stderr); err != nil {
			return err
		}
		options := LabelsRunOptions{Limit: *limit, Full: *full, OneAtATime: *single, DryRun: *dryRun, ExportText: *exportText, Progress: stderr}
		if *exportPath != "" {
			file, err := os.OpenFile(*exportPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			defer file.Close()
			buffered := bufio.NewWriterSize(file, 1<<20)
			defer buffered.Flush()
			options.Export = buffered
		}
		stats, err := catalog.RunLabels(ctx, settings, options)
		if errors.Is(err, labelrt.ErrWorkersUnavailable) {
			// The ledger keeps what was stored; the next run resumes.
			op.legacyOutcome("interrupted", nil)
			fmt.Fprintln(stderr, "labels: interrupted:", err)
			err = nil
		}
		if stats != nil {
			if *asJSON {
				data, _ := json.MarshalIndent(stats, "", "  ")
				fmt.Fprintln(stdout, string(data))
			} else {
				fmt.Fprintf(stdout, "labeled %d messages (%d items, %d batches) in %s; %d unchanged, %d removed, %d failed; complete: %v\n",
					stats.MessagesLabeled, stats.ItemsLabeled, stats.Batches, stats.Total.Round(time.Millisecond), stats.MessagesUnchanged,
					stats.MessagesRemoved, stats.MessagesFailed, stats.Complete)
			}
		}
		return err
	case "status":
		flags := flag.NewFlagSet("labels status", flag.ContinueOnError)
		asJSON := flags.Bool("json", false, "print JSON")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
			return errors.New(labelsUsage)
		}
		status, err := catalog.LabelsStatus(ctx, settings)
		if err != nil {
			return err
		}
		if *asJSON {
			data, _ := json.MarshalIndent(status, "", "  ")
			fmt.Fprintln(stdout, string(data))
			return nil
		}
		if waiting, ok := status["waiting"].(string); ok {
			fmt.Fprintf(stdout, "labeling is %s\n", waiting)
		}
		fmt.Fprintf(stdout, "enabled: %v\nmodel: %s\nledger: %s\nlabeled messages: %d of %d units in %d built conversations (of %d)\ndirty conversations: %d\nbuilt at: %s\n",
			status["enabled"], status["model"], status["state"], status["labeled_messages"], status["units"], status["built_conversations"],
			status["conversations"], status["dirty_conversations"], status["built_at"])
		if segments, ok := status["segments"].(map[string]int64); ok && len(segments) > 0 {
			fmt.Fprintf(stdout, "segments: %v\n", segments)
		}
		return nil
	case "explain":
		if len(args) < 2 {
			return errors.New(labelsUsage)
		}
		flags := flag.NewFlagSet("labels explain", flag.ContinueOnError)
		infer := flags.Bool("infer", false, "also run the model on the message now")
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 {
			return errors.New(labelsUsage)
		}
		explanation, err := catalog.LabelsExplain(ctx, settings, args[1], *infer)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(explanation, "", "  ")
		fmt.Fprintln(stdout, string(data))
		return nil
	}
	return errors.New(labelsUsage)
}

// ensureAuthorshipForLabels brings the authorship ledger current first: the
// labeler reads human pieces from it, as the chain runs labels after it.
func (c *Catalog) ensureAuthorshipForLabels(ctx context.Context, stderr io.Writer) error {
	want, built, err := c.authorshipGenerations()
	if err != nil || want == built {
		return err
	}
	fmt.Fprintln(stderr, "labels: classifying your writing first (the authorship ledger is behind)")
	started := time.Now()
	if err := c.RebuildAuthorship(ctx, want); err != nil {
		return fmt.Errorf("authorship: %w", err)
	}
	fmt.Fprintf(stderr, "labels: authorship took %s\n", time.Since(started).Round(time.Second))
	return nil
}

// LabelsStatus describes the ledger without writing anything: a catalog
// that never ran labels has no tables, which reads as never built.
func (c *Catalog) LabelsStatus(ctx context.Context, settings LabelerSettings) (map[string]any, error) {
	status := map[string]any{"enabled": settings.Enabled, "model_dir": settings.ModelDir, "worker": settings.Worker, "runtime": settings.Runtime,
		"contract": labelsContract}
	if waiting := settings.labelsWaiting(); waiting != "" {
		status["waiting"] = waiting
	}
	identity := ""
	if settings.ModelDir == "" {
		status["model"] = "not present: " + labelsAssetStatus(labelsAssets.Model) + " (or set labels_model_dir or PHAROS_LABELS_MODEL_DIR)"
	} else if model, err := labelrt.LoadModel(settings.ModelDir, settings.Variant); err != nil {
		status["model"] = err.Error()
	} else {
		identity = labelsModelIdentity(model)
		status["model"] = identity + " (verified)"
	}
	var conversations int64
	if err := c.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations`).Scan(&conversations); err != nil {
		return nil, err
	}
	status["conversations"] = conversations
	if !c.hasLabelsLedger() {
		status["state"], status["labeled_messages"], status["units"], status["built_conversations"], status["dirty_conversations"], status["built_at"] =
			"never built", 0, 0, 0, 0, ""
		return status, nil
	}
	counts := map[string]int64{}
	for key, query := range map[string]string{
		"labeled_messages":    `SELECT COUNT(*) FROM message_labels`,
		"units":               `SELECT COALESCE(SUM(units),0) FROM message_labels_conversations`,
		"built_conversations": `SELECT COUNT(*) FROM message_labels_conversations`,
		"dirty_conversations": `SELECT COUNT(*) FROM message_labels_dirty`,
		"other_model":         `SELECT COUNT(*) FROM message_labels WHERE model<>? OR contract<>?`,
	} {
		var value int64
		args := []any{}
		if key == "other_model" {
			args = []any{identity, labelsContract}
		}
		if err := c.DB.QueryRowContext(ctx, query, args...).Scan(&value); err != nil {
			return nil, err
		}
		counts[key] = value
		status[key] = value
	}
	segments := map[string]int64{}
	rows, err := queryMapsContext(ctx, c.DB, `SELECT segment,COUNT(*) n FROM message_labels GROUP BY segment`)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		segments[firstString(row["segment"])] = integer(row["n"])
	}
	status["segments"] = segments
	generation, _ := c.metaValue(ctx, "labels_generation")
	builtAt, _ := c.metaValue(ctx, "labels_built_at")
	_, authorship, _ := c.authorshipGenerations()
	status["generation"], status["built_at"] = generation, builtAt
	switch {
	case generation == "":
		status["state"] = "incomplete: never finished a full run"
	case identity != "" && generation != labelsGeneration(authorship, identity):
		status["state"] = "behind: built for another authorship generation, model or contract"
	case counts["dirty_conversations"] > 0 || counts["built_conversations"] < conversations:
		status["state"] = "behind: conversations changed since the last run"
	default:
		status["state"] = "current"
	}
	return status, nil
}

// LabelsExplain shows how one message was labeled: its items as the model
// sees them (segment, framing tag, context and text, token counts), its
// stored labels with each label's probability and threshold, and whether the
// stored labels match its current inputs. With infer it runs the model now.
func (c *Catalog) LabelsExplain(ctx context.Context, settings LabelerSettings, messageID string, infer bool) (map[string]any, error) {
	var conversation, role, kind string
	if err := c.DB.QueryRowContext(ctx, `SELECT conversation_id,role,kind FROM messages WHERE id=?`, messageID).Scan(&conversation, &role, &kind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("no message %s", messageID)
		}
		return nil, err
	}
	out := map[string]any{"message_id": messageID, "conversation_id": conversation, "role": role, "kind": kind, "contract": labelsContract}
	model, err := labelrt.LoadModel(settings.ModelDir, settings.Variant)
	if err != nil {
		return nil, err
	}
	out["model"] = labelsModelIdentity(model)
	builder, err := newLabelsItemBuilder(ctx, c.DB)
	if err != nil {
		return nil, err
	}
	builder.p.excluded = func(id, reason, detail string) {
		if id == messageID {
			out["excluded"] = map[string]any{"reason": reason, "detail": detail}
		}
	}
	units, err := builder.conversation(ctx, conversation)
	if err != nil {
		return nil, err
	}
	var items []labelingItem
	for _, unit := range units {
		if unit.messageID == messageID {
			items = unit.items()
			out["unit"] = map[string]any{"id": unit.id, "cohort": unit.cohort, "pieces": len(unit.pieces), "visible_chars": len([]rune(unit.visible)), "chunks": len(unit.chunks)}
		}
	}
	if items == nil {
		if out["excluded"] == nil {
			out["excluded"] = map[string]any{"reason": "not a unit", "detail": "the conversation's work is a mirror, or the message is not one the labeler reads"}
		}
		return out, nil
	}
	shown := []map[string]any{}
	rows := []labelrt.Framed{}
	for _, item := range items {
		seq, spans := model.Tokenizer.FrameWithSpans(labelrt.FrameInput{Segment: item.Segment, Cohort: item.Cohort, Sender: item.SenderRole, Context: item.Context, Text: item.Text})
		rows = append(rows, labelrt.Framed{IDs: seq, Spans: spans})
		shown = append(shown, map[string]any{"item_id": item.ID, "chunk": item.ChunkIndex, "segment": item.Segment, "cohort": item.Cohort, "sender_role": item.SenderRole,
			"context": item.Context, "text": item.Text, "chars": item.Chars(), "tokens": len(seq), "text_tokens": len(model.Tokenizer.Encode(item.Text)),
			"spans": spans, "thresholds": thresholdRow(model, item.Segment)})
	}
	out["items"] = shown
	hash := labelsInputHash(items)
	out["input_hash"] = hash
	if c.hasLabelsLedger() {
		var mask int64
		var probs []byte
		var storedHash, storedModel, storedContract, labeledAt string
		err := c.DB.QueryRowContext(ctx, `SELECT labels,probs,input_hash,model,contract,labeled_at FROM message_labels WHERE message_id=?`, messageID).
			Scan(&mask, &probs, &storedHash, &storedModel, &storedContract, &labeledAt)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			out["stored"] = nil
		case err != nil:
			return nil, err
		default:
			out["stored"] = map[string]any{"labels": model.LabelNames(uint64(mask)), "probs": labelProbMap(model, decodeLabelProbs(probs)), "labeled_at": labeledAt,
				"model": storedModel, "current": storedHash == hash && storedModel == out["model"] && storedContract == labelsContract}
		}
	}
	if infer {
		runner, release, err := c.labelerManager(settings).Runner(ctx, model, labelrt.RunnerConfig{Worker: settings.Worker, Args: runtimeArgs(settings),
			Workers: 1, Threads: settings.Threads, MaxBatch: max(1, settings.MaxBatch)})
		if err != nil {
			return nil, err
		}
		defer release()
		result, err := runner.RunBatch(ctx, rows, []string{"probs"})
		if err != nil {
			return nil, err
		}
		probs := result.Tensor("probs").Float32
		var mask uint64
		chunks := []map[string]any{}
		for row, item := range items {
			p := probs[row*model.Labels() : (row+1)*model.Labels()]
			chunk := model.Decide(p, item.Segment)
			mask |= chunk
			chunks = append(chunks, map[string]any{"labels": model.LabelNames(chunk), "probs": labelProbMap(model, p)})
		}
		out["inferred"] = map[string]any{"labels": model.LabelNames(mask), "chunks": chunks, "worker": runner.Pool().Ready().Worker}
	}
	return out, nil
}

func runtimeArgs(settings LabelerSettings) []string {
	if settings.Runtime == "" {
		return nil
	}
	return []string{"--runtime", settings.Runtime}
}

func thresholdRow(model *labelrt.Model, segment string) map[string]float32 {
	row := map[string]float32{}
	for j, value := range model.Thresholds(segment) {
		row[model.Manifest.Labels.Order[j]] = value
	}
	return row
}

// labelProbMap names probabilities by label, keeping those over 0.05.
func labelProbMap(model *labelrt.Model, probs []float32) map[string]float32 {
	named := map[string]float32{}
	for j, p := range probs {
		if p >= 0.05 && j < model.Labels() {
			named[model.Manifest.Labels.Order[j]] = float32(int(p*10000+0.5)) / 10000
		}
	}
	return named
}

// fetchLabelsCLI downloads what settings still need into this Mac's support
// directory, printing progress; with nothing missing it says where each
// asset is.
func fetchLabelsCLI(ctx context.Context, settings LabelerSettings, stdout io.Writer) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	missing := settings.missingAssets()
	if len(missing) == 0 {
		fmt.Fprintf(stdout, "model: %s\nruntime: %s\n", settings.ModelDir, defaultString(settings.Runtime, "bundled with the worker"))
		return nil
	}
	last := time.Time{}
	err := fetchLabelsAssets(ctx, missing, nil, func(asset labelsAsset, done, total int64) {
		if time.Since(last) > time.Second || done == total {
			last = time.Now()
			fmt.Fprintf(stdout, "%s: %d of %d MB\n", asset.Name, done>>20, total>>20)
		}
	})
	if err != nil {
		return err
	}
	for _, asset := range missing {
		fmt.Fprintf(stdout, "%s: %s\n", asset.Name, labelsAssetStatus(asset))
	}
	return nil
}
