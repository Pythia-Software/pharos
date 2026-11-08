package archive

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
)

// Audit is deliberately routed before the ordinary CLI catalog open: even an
// older library is inspected without initialization or backfill writes.
func runConversationNamesCLI(config Config, args []string, stdout, stderr io.Writer) error {
	const usage = "usage: pharos [--config PATH] conversation-names audit [--limit N] [--conversation ID]"
	if len(args) == 0 || args[0] != "audit" {
		return fmt.Errorf("%s", usage)
	}
	flags := flag.NewFlagSet("conversation-names audit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	limit := flags.Int("limit", 100, "inspect at most N conversations (0: all)")
	conversation := flags.String("conversation", "", "inspect one retained conversation by its catalog ID")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || *limit < 0 {
		return fmt.Errorf("%s", usage)
	}
	c, err := openCatalogReadOnly(config.CatalogPath)
	if err != nil {
		return err
	}
	defer c.Close()
	audit, err := c.auditConversationNames(context.Background(), *limit, *conversation)
	if err != nil {
		return err
	}
	return printJSONTo(stdout, audit)
}

func (c *Catalog) auditConversationNames(ctx context.Context, limit int, conversation string) (map[string]any, error) {
	tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	query, args := "SELECT id FROM conversations", []any{}
	if conversation != "" {
		query += " WHERE id=?"
		args = append(args, conversation)
	}
	query += " ORDER BY id"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	selected, err := queryMapsContext(ctx, tx, query, args...)
	if err != nil {
		return nil, err
	}
	rows, err := fetchExecutionSourceRows(ctx, tx, nil, nil)
	if err != nil {
		return nil, err
	}
	evidence, err := rows.evidence(ctx, tx, 0)
	if err != nil {
		return nil, err
	}
	before := resolveExecutions(evidence)
	for _, row := range selected {
		id := asString(row["id"])
		candidates, err := conversationNameCandidates(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		cv := rows.conversations[id]
		cv.humanName, cv.requestName = "", ""
		for _, n := range candidates {
			if n.Kind == "typed" {
				cv.humanName = n.Title
			} else {
				cv.requestName = n.Title
			}
		}
	}
	evidence, err = rows.evidence(ctx, tx, 0)
	if err != nil {
		return nil, err
	}
	after := resolveExecutions(evidence)
	items := []map[string]any{}
	changed, unnamed := 0, 0
	for _, row := range selected {
		id := asString(row["id"])
		old := before.Executions[before.BindConversation(id).ExecutionID]
		next := after.Executions[after.BindConversation(id).ExecutionID]
		if old == nil || next == nil {
			continue
		}
		if old.Label != next.Label {
			changed++
		}
		if next.Label.Source == "unavailable" {
			unnamed++
		}
		items = append(items, map[string]any{"conversation_id": id, "agent_session": string(next.ID), "current_name": old.Label.Text,
			"proposed_name": next.Label.Text, "name_source": next.Label.Source, "changed": old.Label != next.Label})
	}
	return map[string]any{"dry_run": true, "examined": len(items), "changed": changed, "preserved": len(items) - changed,
		"without_retained_request": unnamed, "policy_version": conversationNamesVersion, "conversations": items}, nil
}
