package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"mime"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

// shareRepositoryURL is where a shared export points readers who want the
// tool that produced it.
const shareRepositoryURL = "https://github.com/Pythia-Software/pharos"

// shareWorkLimit bounds one export, which embeds every selected transcript and
// the tool calls, tokens, and costs behind them.
const shareWorkLimit = 200

// shareToolTextLimit clips the input and result text kept for each tool call.
// The transcript holds the full text; the call's dialog holds an excerpt.
const shareToolTextLimit = 3000

var (
	errNothingToShare = errors.New("none of these works has a retained conversation to share")
	errUnknownWork    = errors.New("work not found")
)

// sharedPageAssets matches the tags that load the app's own scripts and
// styles from the service, none of which a standalone file can reach.
var sharedPageAssets = regexp.MustCompile(`<script src="/assets/[^"]*"[^>]*></script>|<link rel="stylesheet" href="/assets/[^"]*">`)

// A shared file makes no network requests: it cannot phone home or pull in
// anything its recipient did not receive. Links only open on click. Blob
// workers evaluate the tables' computed columns.
const sharedContentSecurityPolicy = "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src data:; worker-src blob:; child-src blob:"

// SharedHTML renders the works as a single HTML file. It shows the same
// pages as the app (each conversation, and tables over the works' Library
// rows, tool use, and token usage), with their data embedded in place of the
// service. It returns the page and a file name suggested for saving it.
func (c *Catalog) SharedHTML(ctx context.Context, ids []string) ([]byte, string, error) {
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	if len(ids) == 0 {
		return nil, "", errNothingToShare
	}
	if len(ids) > shareWorkLimit {
		return nil, "", fmt.Errorf("an export holds at most %d works; %d were selected", shareWorkLimit, len(ids))
	}
	works := make([]map[string]any, 0, len(ids))
	kept := make([]string, 0, len(ids))
	for _, id := range ids {
		work, err := c.WorkDetail(id)
		if err != nil {
			return nil, "", err
		}
		if work == nil {
			return nil, "", errUnknownWork
		}
		if shared := sharedWork(work); len(shared["conversations"].([]map[string]any)) > 0 {
			works = append(works, shared)
			kept = append(kept, id)
		}
	}
	if len(works) == 0 {
		return nil, "", errNothingToShare
	}
	datasets, toolCalls, err := c.sharedDatasets(ctx, kept)
	if err != nil {
		return nil, "", err
	}
	payload, err := json.Marshal(map[string]any{"repository": shareRepositoryURL, "works": works, "datasets": datasets, "tool_calls": toolCalls})
	if err != nil {
		return nil, "", err
	}
	title := "Pharos conversations"
	if len(works) == 1 {
		title = firstString(works[0]["title"])
	}
	page, err := sharedPage(appHTML(), payload, title)
	return page, sharedFilename(title, len(works)), err
}

// sharedWork keeps only what the conversation page reads. Everything else in
// a work's detail (source accounts, host and receipt bookkeeping, attempts,
// links to other workspaces) describes the archive rather than the
// conversation, so a new field stays private until it is listed here.
func sharedWork(work map[string]any) map[string]any {
	shared := pick(work, "id", "title", "repository_name", "branch", "activity_at", "location", "repository_locations_json",
		"main_merge_commit", "main_merge_title", "main_merge_url", "main_merge_method")
	shared["prs"] = pickAll(work["prs"], "number", "url", "title", "relationship")
	shared["metrics"] = pickAll(work["metrics"], "name", "value", "unit", "status")
	shared["changes"] = pickAll(work["changes"], "path", "classification", "status", "complete")
	conversations, _ := work["conversations"].([]map[string]any)
	kept := make([]map[string]any, 0, len(conversations))
	for _, conversation := range conversations {
		item := pick(conversation, "id", "provider", "model", "native_id", "coverage", "started_at", "ended_at",
			"harness", "harness_version_first", "harness_version_last", "harness_version_source", "token_usage")
		messages, _ := conversation["messages"].([]map[string]any)
		item["messages"] = sharedMessages(messages)
		kept = append(kept, item)
	}
	shared["conversations"] = kept
	return shared
}

func sharedMessages(messages []map[string]any) []map[string]any {
	kept := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		// Not evidence_locator, which names a file on the sharer's disk, or
		// the ownership and ordering bookkeeping the reader never shows.
		item := pick(message, "id", "native_id", "role", "kind", "model", "text", "raw_text", "created_at", "parent_native_id", "call_id")
		if authorship, ok := message["authorship"].(map[string]any); ok {
			// A span's source names another of the sharer's conversations.
			spans, _ := authorship["spans"].([]map[string]any)
			cleaned := make([]map[string]any, 0, len(spans))
			for _, span := range spans {
				cleaned = append(cleaned, pick(span, "category", "reason", "words", "excerpt"))
			}
			item["authorship"] = map[string]any{"typed_words": authorship["typed_words"], "pasted_words": authorship["pasted_words"], "spans": cleaned}
		}
		kept = append(kept, item)
	}
	return kept
}

func pick(row map[string]any, keys ...string) map[string]any {
	picked := make(map[string]any, len(keys))
	for _, key := range keys {
		if value, ok := row[key]; ok {
			picked[key] = value
		}
	}
	return picked
}

func pickAll(rows any, keys ...string) []map[string]any {
	list, _ := rows.([]map[string]any)
	picked := make([]map[string]any, 0, len(list))
	for _, row := range list {
		picked = append(picked, pick(row, keys...))
	}
	return picked
}

// sharedDatasets returns the rows of the tables a shared file carries, for
// only the given works, and the detail of each tool call.
func (c *Catalog) sharedDatasets(ctx context.Context, ids []string) (map[string][]map[string]any, map[string]map[string]any, error) {
	if err := c.currentToolRollup(ctx); err != nil {
		return nil, nil, err
	}
	scope := placeholders(len(ids))
	args := make([]any, len(ids))
	for index, id := range ids {
		args[index] = id
	}
	library := make([]map[string]any, len(ids))
	for index, id := range ids {
		library[index] = map[string]any{"id": id}
	}
	library, err := c.libraryPage(ctx, library)
	if err != nil {
		return nil, nil, err
	}
	usage, err := c.localUsageRows(ctx)
	if err != nil {
		return nil, nil, err
	}
	usage = slices.DeleteFunc(slices.Clone(usage), func(row map[string]any) bool { return !slices.Contains(ids, firstString(row["workspace_id"])) })
	toolCalls, err := toolCallDataset.scopedRows(ctx, c.DB, "t.workspace_id IN ("+scope+")", args, "id")
	if err != nil {
		return nil, nil, err
	}
	if err := c.priceToolCalls(toolCalls); err != nil {
		return nil, nil, err
	}
	tools, err := c.sharedToolRollup(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	details, err := c.sharedToolCallDetails(ctx, scope, args)
	if err != nil {
		return nil, nil, err
	}
	rows := map[string][]map[string]any{"library": library, "usage": usage, "tools": tools, "tool_calls": toolCalls}
	for dataset, list := range rows {
		names, err := sharedFieldNames(dataset)
		if err != nil {
			return nil, nil, err
		}
		for index, row := range list {
			list[index] = pickFields(row, names)
		}
	}
	return rows, details, nil
}

// sharedRowExtras are Library fields the tables' cells read beyond the schema's
// columns: pull requests, merge links, and the previews of the card view.
var sharedRowExtras = map[string][]string{
	"library": {"pr_details", "canonical_remote", "main_merge_commit", "main_merge_url", "main_merge_method", "first_input", "last_response"},
}

// sharedRowExcluded are schema fields whose values stay out of shared files.
var sharedRowExcluded = map[string]bool{"location": true, "owner": true}

// sharedFieldNames are the fields a shared file keeps for a dataset's rows:
// its schema's, since the tables show nothing else.
func sharedFieldNames(dataset string) (map[string]bool, error) {
	document, err := querySchemaDocument(dataset)
	if err != nil {
		return nil, err
	}
	var schema struct {
		Fields []struct {
			Name string `json:"name"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(document, &schema); err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, field := range schema.Fields {
		names[field.Name] = !sharedRowExcluded[field.Name]
	}
	for _, name := range sharedRowExtras[dataset] {
		names[name] = true
	}
	return names, nil
}

func pickFields(row map[string]any, names map[string]bool) map[string]any {
	picked := make(map[string]any, len(names))
	for name, keep := range names {
		if value, ok := row[name]; ok && keep {
			picked[name] = value
		}
	}
	return picked
}

// sharedToolRollup returns the Tools summary rows for the works, grouped as
// the app's rollup is. The rollup's own SQL excludes mirrored work by a table
// of workspaces to leave out; here that table also holds every other work.
func (c *Catalog) sharedToolRollup(ctx context.Context, ids []string) ([]map[string]any, error) {
	book, err := c.loadPriceBook()
	if err != nil {
		return nil, err
	}
	conn, err := c.DB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), "DROP TABLE IF EXISTS temp.share_cube")
	defer conn.ExecContext(context.Background(), "DROP TABLE IF EXISTS temp.share_excluded")
	for _, statement := range []string{"DROP TABLE IF EXISTS temp.share_cube", "DROP TABLE IF EXISTS temp.share_excluded",
		"CREATE TEMP TABLE share_excluded(workspace_id TEXT PRIMARY KEY)",
		"INSERT OR IGNORE INTO temp.share_excluded SELECT DISTINCT workspace_id FROM tool_calls"} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return nil, err
		}
	}
	args := make([]any, len(ids))
	for index, id := range ids {
		args[index] = id
	}
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{"DELETE FROM temp.share_excluded WHERE workspace_id IN (" + placeholders(len(ids)) + ")", args},
		{"INSERT OR IGNORE INTO temp.share_excluded SELECT workspace_id FROM tool_mirror_workspaces", nil},
		{"CREATE TEMP TABLE share_cube AS " + toolCubeSelect("temp.share_excluded"), nil},
	} {
		if _, err := conn.ExecContext(ctx, statement.sql, statement.args...); err != nil {
			return nil, err
		}
	}
	rows, err := queryMapsContext(ctx, conn, strings.Replace(toolRollupFromCube, "FROM tool_call_cube", "FROM temp.share_cube", 1))
	if err != nil {
		return nil, err
	}
	records := make([]map[string]any, len(rows))
	for index, row := range rows {
		records[index] = toolUsageRecord(book, row)
	}
	return records, nil
}

// sharedToolCallDetails returns, by call ID, what a call's dialog shows beyond
// its table row: clipped input and result text, its shell commands, and the
// URLs it reached.
func (c *Catalog) sharedToolCallDetails(ctx context.Context, scope string, args []any) (map[string]map[string]any, error) {
	calls, err := queryMapsContext(ctx, c.DB, `SELECT t.id,cm.text call_text,rm.text result_text FROM tool_calls t
		LEFT JOIN messages cm ON cm.id=t.call_message_id LEFT JOIN messages rm ON rm.id=t.result_message_id
		WHERE t.workspace_id IN (`+scope+`)`, args...)
	if err != nil {
		return nil, err
	}
	details := make(map[string]map[string]any, len(calls))
	for _, call := range calls {
		detail := map[string]any{}
		toolCallText(detail, firstString(call["call_text"]), firstString(call["result_text"]), shareToolTextLimit)
		details[firstString(call["id"])] = detail
	}
	for key, query := range map[string]string{
		"commands": "SELECT c.tool_call_id,c.position,c.operator,c.command,c.program,c.subcommand,c.category,c.exit_code,c.duration_ms FROM tool_commands c JOIN tool_calls t ON t.id=c.tool_call_id",
		"urls":     "SELECT u.tool_call_id,u.position,u.url,u.host,u.source FROM tool_urls u JOIN tool_calls t ON t.id=u.tool_call_id",
	} {
		orderColumn := map[string]string{"commands": "c", "urls": "u"}[key]
		rows, err := queryMapsContext(ctx, c.DB, query+" WHERE t.workspace_id IN ("+scope+") ORDER BY "+orderColumn+".tool_call_id,"+orderColumn+".position", args...)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			id := firstString(row["tool_call_id"])
			delete(row, "tool_call_id")
			if details[id] == nil {
				details[id] = map[string]any{}
			}
			list, _ := details[id][key].([]map[string]any)
			details[id][key] = append(list, row)
		}
	}
	return details, nil
}

// sharedPage turns the app page into a standalone one. The app's script runs
// unchanged: the embedded payload (which json.Marshal keeps from closing its
// tag) tells it to answer from that data instead of the service.
func sharedPage(app string, payload []byte, title string) ([]byte, error) {
	style, err := assets.ReadFile("assets/share.css")
	if err != nil {
		return nil, err
	}
	tableScript, err := assets.ReadFile("assets/query-tables.js")
	if err != nil {
		return nil, err
	}
	tableStyle, err := assets.ReadFile("assets/query-tables.css")
	if err != nil {
		return nil, err
	}
	head := fmt.Sprintf(`<head>
  <meta http-equiv="Content-Security-Policy" content="%s">
  <script>document.documentElement.classList.add('shared')</script>
  <script type="application/json" id="pharosShareData">%s</script>`, sharedContentSecurityPolicy, payload)
	footer := fmt.Sprintf(`<footer class="share-footer">Shared from a <a href="%s" target="_blank" rel="noopener noreferrer">Pharos</a> conversation · <a href="%s" target="_blank" rel="noopener noreferrer">View on GitHub</a></footer>`, shareRepositoryURL, shareRepositoryURL)
	page := app
	for _, edit := range []struct{ old, new string }{
		// The tables' bundle and styles travel inside the file.
		{`<script src="/assets/query-tables.js"></script>`, "<script>" + strings.ReplaceAll(string(tableScript), "</script", `<\/script`) + "</script>"},
		{`<link rel="stylesheet" href="/assets/query-tables.css">`, "<style>" + string(tableStyle) + "</style>"},
	} {
		if !strings.Contains(page, edit.old) {
			return nil, fmt.Errorf("the app page no longer contains %q, so it cannot be shared", edit.old)
		}
		page = strings.Replace(page, edit.old, edit.new, 1)
	}
	page = sharedPageAssets.ReplaceAllString(page, "")
	for _, edit := range []struct{ old, new string }{
		{"<head>", head},
		{"<title>Pharos</title>", "<title>" + html.EscapeString(sharedTitle(title)) + "</title>"},
		{"</head>", "<style>" + string(style) + "</style>\n</head>"},
		{"</main>", "</main>\n" + footer},
	} {
		if !strings.Contains(page, edit.old) {
			return nil, fmt.Errorf("the app page no longer contains %q, so it cannot be shared", edit.old)
		}
		page = strings.Replace(page, edit.old, edit.new, 1)
	}
	return []byte(page), nil
}

func sharedTitle(title string) string {
	if title = strings.TrimSpace(title); title != "" {
		return title + " · Pharos"
	}
	return "Pharos"
}

var filenameRuns = regexp.MustCompile(`[^a-z0-9]+`)

func sharedFilename(title string, works int) string {
	if works > 1 {
		return fmt.Sprintf("pharos-%d-conversations.html", works)
	}
	slug := strings.Trim(filenameRuns.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(slug) > 60 {
		slug = strings.Trim(slug[:60], "-")
	}
	if slug == "" {
		slug = "conversation"
	}
	return "pharos-" + slug + ".html"
}

// getShared serves GET /api/share?id=...&id=..., and /api/share/work/<id> for
// one work, as a file to save; with check=1 it only reports that it could.
func (s *Server) getShared(w http.ResponseWriter, r *http.Request, ids []string) {
	page, filename, err := s.Catalog.SharedHTML(r.Context(), ids)
	switch {
	case errors.Is(err, errUnknownWork), errors.Is(err, errNothingToShare):
		writeError(w, err, http.StatusNotFound)
	case err != nil:
		writeError(w, err, http.StatusInternalServerError)
	case r.URL.Query().Get("check") == "1":
		// The page asks first, so that a failure reaches the person instead of
		// being saved as the file.
		writeJSON(w, map[string]any{"filename": filename, "bytes": len(page)}, http.StatusOK)
	default:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(page)
	}
}
