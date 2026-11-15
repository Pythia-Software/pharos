package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
)

const executionEvidenceUsage = `usage: pharos execution-evidence status
       pharos execution-evidence run [--batch N] [--limit N]

status reads the catalog in one read-only snapshot and reports the
conversations whose typed execution evidence is absent or from another
extractor version (the pending scope), what a retained-row run cannot
recover, the provider reads that would, and source changes not yet marked on
labels and agent events. It writes nothing.

run re-derives typed evidence from the rows the catalog retains, in committed
batches of at most --batch conversations (default 500) and about one second of
writer time each, stopping after --limit (0: all pending). Each batch marks
the labels and agent events whose evidence it changed as pending. It is
resumable and idempotent: run it again to continue. It never reads a
provider and never changes transcripts, parents or usage.
See docs/execution-evidence-backfill.md.`

// runExecutionEvidenceCLI: pharos execution-evidence status|run.
func runExecutionEvidenceCLI(ctx context.Context, catalog *Catalog, op *Op, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(executionEvidenceUsage)
	}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return errors.New(executionEvidenceUsage)
		}
		status, err := executionEvidenceSnapshot(ctx, catalog.reader())
		if err != nil {
			return err
		}
		return printJSONTo(stdout, status)
	case "run":
		flags := flag.NewFlagSet("execution-evidence run", flag.ContinueOnError)
		flags.SetOutput(stderr)
		batch := flags.Int("batch", 500, "conversations per committed batch")
		limit := flags.Int("limit", 0, "derive at most N conversations (0: every pending one)")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || *batch <= 0 || *limit < 0 {
			return errors.New(executionEvidenceUsage)
		}
		before, err := executionEvidenceSnapshot(ctx, catalog.reader())
		if err != nil {
			return err
		}
		total := integer(before["pending"])
		if *limit > 0 {
			total = min(total, int64(*limit))
		}
		done, err := catalog.backfillExecutionEvidenceBounded(ctx, *batch, *limit, func(done int) {
			if op != nil {
				op.progress("Derive execution evidence", "", "conversation", float64(done), float64(total), total > 0)
			}
			fmt.Fprintf(stderr, "derived execution evidence for %d/%d conversations\n", done, total)
		})
		if err != nil {
			return fmt.Errorf("execution evidence after %d conversations: %w", done, err)
		}
		after, err := executionEvidenceSnapshot(ctx, catalog.reader())
		if err != nil {
			return err
		}
		return printJSONTo(stdout, map[string]any{"derived": done, "status": after})
	}
	return errors.New(executionEvidenceUsage)
}

// executionEvidenceSnapshot reads executionEvidenceStatus in one read-only
// transaction, so a concurrent ingest cannot make its counts disagree.
func executionEvidenceSnapshot(ctx context.Context, db *sql.DB) (map[string]any, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return executionEvidenceStatus(ctx, committedReads{tx})
}

// providerReadEvidence is what only a provider read recovers, by provider:
// the extractor retains it before it filters, and stored rows never held it.
var providerReadEvidence = []struct {
	provider, extractor string
	recovers            []string
	how                 string
}{
	{"cursor", cursorExtractor, []string{"history_copy", "suppressed_record", "copy_reconstruction"},
		"the next index of each Cursor source reads it again (the extractor version changed); composers Cursor has deleted stay unavailable"},
	{"copilot", copilotExtractor, []string{"parent_fallback"},
		"the next index of each Copilot CLI source reads it again; until then a session-root parent is legacy (copilot-retained-session-parent/v1)"},
	{"vscode-copilot", vscodeCopilotExtractor, []string{"parent_fallback"},
		"the next index of each VS Code Copilot source reads it again; until then a session-root parent is legacy (copilot-retained-session-parent/v1)"},
	// The session_meta note backfill types is stored only by the Codex
	// extractor from message-model-v19; a rollout indexed earlier has none.
	{"codex", jsonlExtractor, []string{"history_copy"},
		"the next index of each Codex source reads rollouts indexed before " + jsonlExtractor + " again; until then a forked or spawned child reports retained-history-undeclared, and rollouts Codex has deleted stay so"},
}

// executionEvidenceStatus is the pending scope of the retained-row backfill
// and what stays unavailable after it. It only reads q.
func executionEvidenceStatus(ctx context.Context, q queryer) (map[string]any, error) {
	status := map[string]any{"version": executionEvidenceVersion, "resolver": executionResolverVersion}
	present, err := executionEvidenceTablesPresent(ctx, q)
	if err != nil {
		return nil, err
	}
	if !present {
		// A catalog this build has not opened yet; opening creates the tables.
		status["tables"] = "absent"
		return status, nil
	}
	rows, err := queryMapsContext(ctx, q, `SELECT c.provider,
	 CASE WHEN s.conversation_id IS NULL THEN 'absent' WHEN s.version IS NOT ? THEN 'other-version' ELSE s.input END state,COUNT(*) n
	 FROM conversations c LEFT JOIN execution_evidence_state s ON s.conversation_id=c.id GROUP BY 1,2 ORDER BY 1,2`, executionEvidenceVersion)
	if err != nil {
		return nil, err
	}
	byProvider := map[string]map[string]int64{}
	var total, pending, retained int64
	unread := map[string]int64{}
	for _, row := range rows {
		provider, state, n := firstString(row["provider"]), firstString(row["state"]), integer(row["n"])
		if byProvider[provider] == nil {
			byProvider[provider] = map[string]int64{}
		}
		byProvider[provider][state] += n
		total += n
		switch state {
		case "absent", "other-version":
			pending += n
		case "retained":
			retained += n
		}
		if state != "ingest" {
			unread[provider] += n
		}
	}
	status["conversations"], status["pending"], status["retained"] = total, pending, retained
	status["by_provider"] = byProvider
	reads := []map[string]any{}
	for _, read := range providerReadEvidence {
		reads = append(reads, map[string]any{"provider": read.provider, "extractor": read.extractor,
			"conversations": unread[read.provider], "recovers": read.recovers, "how": read.how})
	}
	status["provider_read"] = reads
	unavailable := map[string]any{}
	counts, err := queryMapsContext(ctx, q, `SELECT
	 (SELECT COUNT(*) FROM execution_claims WHERE kind='parent' AND json_extract(detail_json,'$.parent_namespace')='unrecorded') unrecorded,
	 (SELECT COUNT(*) FROM execution_claims WHERE kind='launch_outcome' AND json_extract(detail_json,'$.outcome')='unknown') unknown_outcomes,
	 (SELECT COUNT(*) FROM execution_claims WHERE kind='history_copy' AND json_extract(detail_json,'$.mapping') IN ('unmapped','unverified')) unmapped_copies`)
	if err != nil {
		return nil, err
	}
	if len(counts) > 0 {
		// Kept as they are: none of these is reconstructed from the old graph.
		unavailable["parent_namespace_unrecorded"] = integer(counts[0]["unrecorded"])
		unavailable["launch_outcome_unknown"] = integer(counts[0]["unknown_outcomes"])
		unavailable["history_copy_unmapped"] = integer(counts[0]["unmapped_copies"])
	}
	// Parents no record names, by the rule that chose them: a fallback
	// (synthesized) or a retained parent of unknown provenance (legacy).
	fallbacks, err := queryMapsContext(ctx, q, `SELECT rule,COUNT(*) n FROM execution_claims
	 WHERE kind='parent' AND (authority IN ('fallback','unverified') OR origin IN ('synthesized','legacy')) GROUP BY rule ORDER BY rule`)
	if err != nil {
		return nil, err
	}
	byRule := map[string]int64{}
	for _, row := range fallbacks {
		byRule[firstString(row["rule"])] = integer(row["n"])
	}
	unavailable["parent_fallbacks"] = byRule
	status["unavailable"] = unavailable
	behind, err := executionDependentsBehind(ctx, q)
	if err != nil {
		return nil, err
	}
	// Source changes the next ingest, label or agent-event run marks on the
	// derivatives they reach.
	status["dependents_unmarked_generations"] = behind
	if pending > 0 {
		status["next"] = "pharos execution-evidence run --limit N (resumable; reads only the catalog)"
	}
	return status, nil
}

func printJSONTo(w io.Writer, value any) error {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(payload))
	return err
}
