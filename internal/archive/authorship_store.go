package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
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
	filter  string
	args    []any
}

func (table authorshipTable) plan(ctx context.Context, q queryer) ([]authorshipMutation, error) {
	stored, err := queryMapsContext(ctx, q, "SELECT "+strings.Join(table.columns, ",")+" FROM "+table.name+table.filter, table.args...)
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
	rows := make([]map[string]string, 0, len(launches))
	for _, launch := range launches {
		rows = append(rows, map[string]string{"child": launch.child, "parent": launch.parent})
	}
	parents, _, err := foldedParentsUsing(ctx, q, `(SELECT json_extract(value,'$.child') child_id,json_extract(value,'$.parent') parent_id FROM json_each(?))`, jsonText(rows))
	return parents, err
}

func (c *Catalog) planAuthorship(ctx context.Context, results []authorResult, launches []conversationLaunch) ([]authorshipMutation, error) {
	return c.planAuthorshipScope(ctx, results, launches, nil)
}

func (c *Catalog) planAuthorshipScope(ctx context.Context, results []authorResult, launches []conversationLaunch, since *time.Time) ([]authorshipMutation, error) {
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
	query, args := "SELECT id FROM workspaces ORDER BY id", []any{}
	if since != nil {
		ids, works := []string{}, map[string]bool{}
		for _, result := range results {
			ids = append(ids, result.Input.ID)
			works[result.Input.WorkspaceID] = true
		}
		messages.filter = " WHERE message_id IN (SELECT value FROM json_each(?))"
		messages.args = []any{jsonText(ids)}
		spans.filter, spans.args = messages.filter, messages.args
		query = "SELECT id FROM workspaces WHERE id IN (SELECT value FROM json_each(?)) ORDER BY id"
		args = []any{jsonText(sortedNameIDs(works))}
	}
	workspaces, err := queryMapsContext(ctx, c.DB, query, args...)
	if err != nil {
		return nil, err
	}
	for _, work := range workspaces {
		id := asString(work["id"])
		if since != nil {
			rows, err := queryMapsContext(ctx, c.DB, "SELECT authored_at FROM workspace_names WHERE workspace_id=?", id)
			if err != nil {
				return nil, err
			}
			if len(rows) > 0 {
				if at, ok := parseTime(asString(rows[0]["authored_at"])); ok && at.Before(*since) {
					continue
				}
			}
		}
		values := named[id]
		if values == nil {
			values = []any{id, nil, nil, nil, nil, workspaceNamesVersion}
		}
		names.rows = append(names.rows, values)
	}
	tables := []authorshipTable{launchTable, messages, spans, names}
	if since != nil {
		namesIDs := []string{}
		for _, row := range names.rows {
			namesIDs = append(namesIDs, asString(row[0]))
		}
		names.filter = " WHERE workspace_id IN (SELECT value FROM json_each(?))"
		names.args = []any{jsonText(namesIDs)}
		tables = []authorshipTable{messages, spans, names}
	}
	plan := []authorshipMutation{}
	for _, table := range tables {
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
	if err := c.storeAuthorshipRows(ctx, plan, map[string]int{}); err != nil {
		return err
	}
	return c.publishAuthorship(ctx, generation)
}

func (c *Catalog) storeAuthorshipRows(ctx context.Context, plan []authorshipMutation, written map[string]int) error {
	reportWork(ctx, "saving authorship", "row", 0, len(plan))
	for start := 0; start < len(plan); start += max(1, authorshipWriteBatch) {
		stop := min(start+max(1, authorshipWriteBatch), len(plan))
		if err := c.writeTransaction(ctx, "save authorship chunk", func(tx *sql.Tx) error {
			names := []string{}
			if err := executeAuthorshipMutations(ctx, tx, plan[start:stop], func(change authorshipMutation, count int64) {
				written[change.table] += int(count)
				if change.workspace != "" {
					names = append(names, change.workspace)
				}
			}); err != nil {
				return err
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
	reportWork(ctx, fmt.Sprintf("saved authorship: messages=%d spans=%d launches=%d names=%d", written["message_authorship"], written["message_authorship_spans"], written["conversation_launches"], written["workspace_names"]), "row", len(plan), len(plan))
	return nil
}

func (c *Catalog) publishAuthorship(ctx context.Context, generation string) error {
	return c.writeTransaction(ctx, "complete authorship", func(tx *sql.Tx) error {
		for key, value := range map[string]string{"authorship_generation": generation, "authorship_built_at": now()} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
				return err
			}
		}
		return nil
	})
}

// Prepare each mutation shape once per writer turn. State indexes can contain
// many candidates; parsing the same INSERT for every row would dominate their
// initial build even though commits are bounded.
func executeAuthorshipMutations(ctx context.Context, tx *sql.Tx, changes []authorshipMutation, visit func(authorshipMutation, int64)) error {
	statements := map[string]*sql.Stmt{}
	defer func() {
		for _, statement := range statements {
			_ = statement.Close()
		}
	}()
	for _, change := range changes {
		statement := statements[change.query]
		if statement == nil {
			var err error
			statement, err = tx.PrepareContext(ctx, change.query)
			if err != nil {
				return err
			}
			statements[change.query] = statement
		}
		result, err := statement.ExecContext(ctx, change.args...)
		if err != nil {
			return err
		}
		if visit != nil {
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			visit(change, count)
		}
	}
	return nil
}
