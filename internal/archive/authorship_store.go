package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Bound the number of row mutations per writer turn, including individual
// spans. Generation publication happens only after every turn succeeds.
var authorshipWriteBatch = 250

type authorshipMutation struct {
	table, query string
	args         []any
	workspace    string
}

type authorshipTable struct {
	name    string
	columns []string
	keys    int
	rows    [][]any
}

func (table authorshipTable) plan(ctx context.Context, q queryer) ([]authorshipMutation, error) {
	stored, err := queryMapsContext(ctx, q, "SELECT "+strings.Join(table.columns, ",")+" FROM "+table.name)
	if err != nil {
		return nil, err
	}
	previous := map[string][]any{}
	for _, row := range stored {
		values := make([]any, len(table.columns))
		for i, col := range table.columns {
			values[i] = row[col]
		}
		previous[jsonText(values[:table.keys])] = values
	}
	assignments, guards, predicates := []string{}, []string{}, []string{}
	for _, col := range table.columns[table.keys:] {
		assignments = append(assignments, col+"=excluded."+col)
		guards = append(guards, table.name+"."+col+" IS NOT excluded."+col)
	}
	for _, col := range table.columns[:table.keys] {
		predicates = append(predicates, col+"=?")
	}
	insert := "INSERT INTO " + table.name + "(" + strings.Join(table.columns, ",") + ") VALUES(" + placeholders(len(table.columns)) + ") ON CONFLICT(" + strings.Join(table.columns[:table.keys], ",") + ") DO UPDATE SET " + strings.Join(assignments, ",") + " WHERE " + strings.Join(guards, " OR ")
	// Names require source/display seeds only on insertion. Existing source names
	// and overrides belong to ingest, never to the classification pass.
	if table.name == "workspace_names" {
		insert = "INSERT INTO workspace_names(" + strings.Join(table.columns, ",") + ",source_title,display_title) SELECT " + placeholders(len(table.columns)) + ",title,title FROM workspaces WHERE id=? ON CONFLICT(workspace_id) DO UPDATE SET " + strings.Join(assignments, ",") + " WHERE " + strings.Join(guards, " OR ")
	}
	changes := []authorshipMutation{}
	for _, values := range table.rows {
		key := jsonText(values[:table.keys])
		prior, exists := previous[key]
		delete(previous, key)
		if exists && jsonText(prior) == jsonText(values) {
			continue
		}
		args := append([]any{}, values...)
		change := authorshipMutation{table: table.name, query: insert, args: args}
		if table.name == "workspace_names" {
			change.workspace = asString(values[0])
			change.args = append(change.args, values[0])
		}
		changes = append(changes, change)
	}
	// Sorting makes deletion plans repeatable without depending on map order.
	keys := make([]string, 0, len(previous))
	for key := range previous {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		changes = append(changes, authorshipMutation{table: table.name, query: "DELETE FROM " + table.name + " WHERE " + strings.Join(predicates, " AND "), args: previous[key][:table.keys]})
	}
	return changes, nil
}

// Compute folding against the proposed launches, so dry-run probes and the
// store share the same plan without publishing launches just to read parents.
func authorshipParents(ctx context.Context, q queryer, launches []conversationLaunch) (map[string]string, error) {
	rows, err := queryMapsContext(ctx, q, `SELECT id,workspace_id,parent_id FROM conversations`)
	if err != nil {
		return nil, err
	}
	works, totals, roots := map[string]string{}, map[string]int{}, map[string]int{}
	for _, row := range rows {
		id, work := asString(row["id"]), asString(row["workspace_id"])
		works[id] = work
		totals[work]++
		if row["parent_id"] == nil {
			roots[work]++
		}
	}
	type group struct {
		count  int
		parent string
	}
	sub, run := map[string]group{}, map[string]group{}
	add := func(groups map[string]group, child, parent string) {
		if parent == "" || child == parent {
			return
		}
		g := groups[child]
		g.count++
		if g.parent == "" || parent < g.parent {
			g.parent = parent
		}
		groups[child] = g
	}
	for _, row := range rows {
		add(sub, asString(row["workspace_id"]), works[asString(row["parent_id"])])
	}
	for _, launch := range launches {
		add(run, works[launch.child], works[launch.parent])
	}
	parents := map[string]string{}
	for work, g := range sub {
		if g.count == totals[work] {
			parents[work] = g.parent
		}
	}
	for work, g := range run {
		if _, ok := parents[work]; !ok && g.count == roots[work] {
			parents[work] = g.parent
		}
	}
	return parents, nil
}

func (c *Catalog) planAuthorship(ctx context.Context, results []authorResult, launches []conversationLaunch) ([]authorshipMutation, error) {
	parents, err := authorshipParents(ctx, c.DB, launches)
	if err != nil {
		return nil, err
	}
	columns := append([]string{"message_id", "conversation_id", "workspace_id", "sent_at", "day", "total_chars"}, authorshipColumns()...)
	columns = append(columns, "spans_json", "work_id", "words", "main_category", "categories", "rules")
	messages := authorshipTable{name: "message_authorship", columns: columns, keys: 1}
	spans := authorshipTable{name: "message_authorship_spans", keys: 2, columns: []string{"message_id", "position", "category", "rule", "reason", "start_byte", "end_byte", "chars", "words", "source_conversation_id", "source_message_id"}}
	launchTable := authorshipTable{name: "conversation_launches", keys: 1, columns: []string{"child_id", "parent_id", "tool_call_id", "program"}}
	names := authorshipTable{name: "workspace_names", keys: 1, columns: []string{"workspace_id", "authored_title", "authored_message_id", "authored_hash", "authored_at", "authored_version"}}
	named := map[string][]any{}
	for _, launch := range launches {
		launchTable.rows = append(launchTable.rows, []any{launch.child, launch.parent, launch.toolCall, launch.program})
	}
	for _, result := range results {
		input := result.Input
		counts := countSpans(input.Text, result.Spans)
		encoded, _ := json.Marshal(result.Spans)
		values := []any{input.ID, input.ConversationID, input.WorkspaceID, formatTime(input.SentAt), input.SentAt.Local().Format("2006-01-02"), utf8.RuneCountInString(strings.TrimSpace(input.Text))}
		words := 0
		for _, category := range authorshipCategories {
			values = append(values, counts.chars[category], counts.words[category])
			words += counts.words[category]
		}
		summary := summarizeSpans(input.Text, result.Spans)
		values = append(values, string(encoded), rootWorkspace(input.WorkspaceID, parents), words, nilIfEmpty(summary.main), summary.categories, summary.rules)
		messages.rows = append(messages.rows, values)
		wordCounts := authorshipWordCounts(input.Text, result.Spans)
		for position, span := range result.Spans {
			spans.rows = append(spans.rows, []any{input.ID, position, span.Category, spanRule(span), nilIfEmpty(span.Reason), span.Start, span.End, utf8.RuneCountInString(strings.TrimSpace(input.Text[span.Start:span.End])), wordCounts[position], nilIfEmpty(span.Source), nilIfEmpty(span.Message)})
		}
		if _, seen := named[input.WorkspaceID]; !seen {
			if title := authoredWorkspaceTitle(input.Text, result.Spans); title != "" {
				named[input.WorkspaceID] = []any{input.WorkspaceID, title, input.ID, hashBytes([]byte(input.Text)), formatTime(input.SentAt), workspaceNamesVersion}
			}
		}
	}
	workspaces, err := queryMapsContext(ctx, c.DB, "SELECT id FROM workspaces ORDER BY id")
	if err != nil {
		return nil, err
	}
	for _, work := range workspaces {
		id := asString(work["id"])
		values := named[id]
		if values == nil {
			values = []any{id, nil, nil, nil, nil, workspaceNamesVersion}
		}
		names.rows = append(names.rows, values)
	}
	plan := []authorshipMutation{}
	for _, table := range []authorshipTable{launchTable, messages, spans, names} {
		changes, err := table.plan(ctx, c.DB)
		if err != nil {
			return nil, err
		}
		plan = append(plan, changes...)
	}
	return plan, nil
}

func (c *Catalog) storeAuthorship(ctx context.Context, results []authorResult, launches []conversationLaunch, generation string) error {
	plan, err := c.planAuthorship(ctx, results, launches)
	if err != nil {
		return err
	}
	written := map[string]int{}
	reportWork(ctx, "saving authorship", "row", 0, len(plan))
	for start := 0; start < len(plan); start += max(1, authorshipWriteBatch) {
		stop := min(start+max(1, authorshipWriteBatch), len(plan))
		if err := c.writeTransaction(ctx, "save authorship chunk", func(tx *sql.Tx) error {
			names := []string{}
			for _, change := range plan[start:stop] {
				result, err := tx.ExecContext(ctx, change.query, change.args...)
				if err != nil {
					return err
				}
				count, err := result.RowsAffected()
				if err != nil {
					return err
				}
				written[change.table] += int(count)
				if change.workspace != "" {
					names = append(names, change.workspace)
				}
			}
			if len(names) > 0 {
				return refreshWorkspaceNames(tx, names)
			}
			return nil
		}); err != nil {
			return err
		}
		reportWork(ctx, "saving authorship", "row", stop, len(plan))
	}
	// The marker is deliberately separate from all chunks: interruption leaves
	// the previous generation, and retrying diffs against the rows already saved.
	if err := c.writeTransaction(ctx, "complete authorship", func(tx *sql.Tx) error {
		for key, value := range map[string]string{"authorship_generation": generation, "authorship_built_at": now()} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	reportWork(ctx, fmt.Sprintf("saved authorship: messages=%d spans=%d launches=%d names=%d", written["message_authorship"], written["message_authorship_spans"], written["conversation_launches"], written["workspace_names"]), "row", len(plan), len(plan))
	return nil
}
