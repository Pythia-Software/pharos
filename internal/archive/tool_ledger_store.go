package archive

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// toolRollupVersion names the tool_usage_daily and tool_call_cube layouts
// and their derivation.
const toolRollupVersion = "rollup-v4"

// toolLedgerState tracks the in-process ledger backfill and rollup rebuilds.
type toolLedgerState struct {
	backfill    sync.Mutex
	mu          sync.Mutex
	running     bool
	done, total int
	lastError   string
	rollup      sync.Mutex
	rolledAt    time.Time
	// refreshing is set while currentToolRollup rebuilds in the background.
	refreshing bool
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func nullableInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

// bumpToolLedgerGeneration marks the rollup stale. It runs in the writer's
// transaction, so another process's reads see the change with the data.
func bumpToolLedgerGeneration(tx execer) error {
	if err := bumpToolLedgerPartitionGeneration(tx); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('tool_rollup_full_revision',hex(randomblob(16))) ON CONFLICT(key) DO UPDATE SET value=excluded.value`)
	return err
}

// replaceToolLedger rebuilds one conversation's model requests and tool calls.
func replaceToolLedger(tx *sql.Tx, workspaceID, conversationID string, conversation ConversationRecord) error {
	var priorVersion string
	if err := tx.QueryRow("SELECT version FROM tool_ledger_state WHERE conversation_id=?", conversationID).Scan(&priorVersion); err != nil && err != sql.ErrNoRows {
		return err
	}
	if priorVersion != toolLedgerVersion {
		for _, table := range []string{"skill_usages", "tool_calls", "model_requests", "tool_ledger_inputs"} {
			if _, err := tx.Exec("DELETE FROM "+table+" WHERE conversation_id=?", conversationID); err != nil {
				return err
			}
		}
	}
	rows, err := queryMaps(tx, "SELECT row_kind,row_id,fingerprint FROM tool_ledger_inputs WHERE conversation_id=?", conversationID)
	if err != nil {
		return err
	}
	inputs := map[string]string{}
	for _, row := range rows {
		inputs[firstString(row["row_kind"])+":"+firstString(row["row_id"])] = firstString(row["fingerprint"])
	}
	retained := map[string]bool{}
	changed := false
	needsWrite := func(kind, id string, value any) (bool, error) {
		key := kind + ":" + id
		retained[key] = true
		fingerprint := hashBytes([]byte(toolLedgerVersion + ":" + workspaceID + ":" + conversation.Provider + ":" + jsonText(value)))
		if inputs[key] == fingerprint {
			return false, nil
		}
		if _, err := tx.Exec(`INSERT INTO tool_ledger_inputs(conversation_id,row_kind,row_id,fingerprint) VALUES(?,?,?,?)
			ON CONFLICT(conversation_id,row_kind,row_id) DO UPDATE SET fingerprint=excluded.fingerprint`, conversationID, kind, id, fingerprint); err != nil {
			return false, err
		}
		changed = true
		return true, nil
	}
	requests, calls := buildToolLedger(conversation.Messages, strings.TrimSpace(conversation.Model))
	locations, err := toolRepositoryLocations(tx)
	if err != nil {
		return err
	}
	roots, err := toolRepoRoots(tx, workspaceID, locations)
	if err != nil {
		return err
	}
	for index := range calls {
		if calls[index].CWD == "" && len(roots) > 0 {
			calls[index].CWD = roots[0].Location
		}
		if calls[index].FilePath == "" {
			continue
		}
		cwd := calls[index].CWD
		var repo repoRoot
		calls[index].PathAbsolute = absoluteToolPath(calls[index].FilePath, cwd)
		calls[index].RepoPath, repo, calls[index].PathScope = resolveRepoPath(calls[index].FilePath, cwd, roots)
		calls[index].PathRepository, calls[index].PathRepositoryID = repo.Repository, repo.ID
	}
	sessionID := func(stream string) any {
		if stream == "" {
			stream = "main"
		}
		return stableID("agent-session", conversationID, stream)
	}
	requestID := func(key string) any {
		if key == "" {
			return nil
		}
		return stableID("model-request", conversationID, key)
	}
	messageID := func(nativeID string) any {
		if nativeID == "" {
			return nil
		}
		return stableID("message", conversationID, nativeID)
	}
	if len(requests) > 0 {
		statement, err := tx.Prepare(derivedUpsertSQL(`INSERT INTO model_requests(id,conversation_id,agent_session_id,native_id,sequence,requested_at,model,
			input_tokens,uncached_input_tokens,cache_read_input_tokens,cache_creation_input_tokens,output_tokens,reasoning_output_tokens,total_tokens,
			context_growth_tokens,compacted_before,tool_call_count) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`))
		if err != nil {
			return err
		}
		for _, request := range requests {
			write, err := needsWrite("request", firstString(requestID(request.Key)), request)
			if err != nil {
				statement.Close()
				return err
			}
			if !write {
				continue
			}
			counts := request.Counts
			uncached := max(counts["input_tokens"]-counts["cache_read_input_tokens"]-counts["cache_creation_input_tokens"], 0)
			if _, err := statement.Exec(requestID(request.Key), conversationID, sessionID(request.Stream), nilIfEmpty(request.NativeID), request.Sequence,
				nilIfEmpty(request.RequestedAt), nilIfEmpty(request.Model), integer(counts["input_tokens"]), integer(uncached),
				integer(counts["cache_read_input_tokens"]), integer(counts["cache_creation_input_tokens"]), integer(counts["output_tokens"]),
				integer(counts["reasoning_output_tokens"]), integer(counts["total_tokens"]), nullableInt(request.ContextGrowth),
				boolInt(request.CompactedBefore), request.ToolCalls); err != nil {
				statement.Close()
				return err
			}
		}
		statement.Close()
	}
	if len(calls) > 0 {
		callStatement, err := tx.Prepare(derivedUpsertSQL(`INSERT INTO tool_calls(id,workspace_id,conversation_id,agent_session_id,call_id,call_message_id,result_message_id,
			sequence,provider,model,kind,tool_name,tool_category,mcp_server,command,program,subcommand,command_category,command_count,
			has_pipe,has_redirect,has_heredoc,backgrounded,file_path,started_at,ended_at,duration_ms,duration_source,status,error_type,exit_code,
			interrupted,truncated,input_bytes,result_bytes,result_tokens,result_tokens_source,request_id,next_request_id,parallel_count,
			output_tokens,carried_requests,carried_tokens,lines_added,lines_removed,url,host,hosts,url_count,search_query,error_signature,test_failure,repo_path,path_repository,path_repository_id,path_scope,path_absolute)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`))
		if err != nil {
			return err
		}
		defer callStatement.Close()
		commandStatement, err := tx.Prepare(`INSERT INTO tool_commands(tool_call_id,position,operator,command,program,subcommand,category,exit_code,duration_ms)
			VALUES(?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer commandStatement.Close()
		urlStatement, err := tx.Prepare(`INSERT INTO tool_urls(tool_call_id,position,url,host,source) VALUES(?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer urlStatement.Close()
		for _, call := range calls {
			id := stableID("tool-call", conversationID, call.Key)
			write, err := needsWrite("call", id, call)
			if err != nil {
				return err
			}
			if !write {
				continue
			}
			for _, table := range []string{"tool_commands", "tool_urls"} {
				if _, err := tx.Exec("DELETE FROM "+table+" WHERE tool_call_id=?", id); err != nil {
					return err
				}
			}
			if _, err := callStatement.Exec(id, workspaceID, conversationID, sessionID(call.Stream), nilIfEmpty(call.CallID),
				messageID(call.CallNativeID), messageID(call.ResultNativeID), call.Sequence, conversation.Provider, nilIfEmpty(call.Model),
				call.Kind, call.ToolName, call.Category, nilIfEmpty(call.MCPServer), nilIfEmpty(call.Command), nilIfEmpty(call.Program),
				nilIfEmpty(call.Subcommand), nilIfEmpty(call.CommandCategory), call.CommandCount, boolInt(call.HasPipe), boolInt(call.HasRedirect),
				boolInt(call.HasHeredoc), boolInt(call.Backgrounded), nilIfEmpty(call.FilePath), nilIfEmpty(call.StartedAt), nilIfEmpty(call.EndedAt),
				nullableInt(call.DurationMS), nilIfEmpty(call.DurationSource), call.Status, nilIfEmpty(call.ErrorType), nullableInt(call.ExitCode),
				boolInt(call.Interrupted), boolInt(call.Truncated), call.InputBytes, call.ResultBytes, call.ResultTokens,
				nilIfEmpty(call.ResultTokensSource), requestID(call.RequestKey), requestID(call.NextRequestKey), call.ParallelCount,
				call.OutputTokens, call.CarriedRequests, call.CarriedTokens, nullableInt(call.LinesAdded), nullableInt(call.LinesRemoved),
				nilIfEmpty(call.URL), nilIfEmpty(call.Host), nilIfEmpty(call.Hosts), call.URLCount, nilIfEmpty(call.SearchQuery), nilIfEmpty(call.ErrorSignature), boolInt(call.TestFailure), nilIfEmpty(call.RepoPath), nilIfEmpty(call.PathRepository), nilIfEmpty(call.PathRepositoryID), nilIfEmpty(call.PathScope), nilIfEmpty(call.PathAbsolute)); err != nil {
				return err
			}
			for _, command := range call.Commands {
				if _, err := commandStatement.Exec(id, command.Position, nilIfEmpty(command.Operator), command.Command, nilIfEmpty(command.Program),
					nilIfEmpty(command.Subcommand), nilIfEmpty(command.Category), nullableInt(command.ExitCode), nullableInt(command.DurationMS)); err != nil {
					return err
				}
			}
			for _, address := range call.URLs {
				if _, err := urlStatement.Exec(id, address.Position, address.URL, address.Host, address.Source); err != nil {
					return err
				}
			}
		}
	}
	usages := buildSkillUsages(conversation.Messages, calls)
	if len(usages) > 0 {
		statement, err := tx.Prepare(derivedUpsertSQL(`INSERT INTO skill_usages(id,workspace_id,conversation_id,tool_call_id,agent_session_id,
			evidence_message_id,body_message_id,skill_name,skill_path,evidence_type,status,created_at,content_bytes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`))
		if err != nil {
			return err
		}
		defer statement.Close()
		for _, usage := range usages {
			id := stableID("skill-usage", conversationID, usage.Key)
			write, err := needsWrite("skill", id, usage)
			if err != nil {
				return err
			}
			if !write {
				continue
			}
			var callID any
			if usage.CallKey != "" {
				callID = stableID("tool-call", conversationID, usage.CallKey)
			}
			if _, err := statement.Exec(id, workspaceID, conversationID, callID, sessionID(usage.Stream), messageID(usage.EvidenceNativeID),
				messageID(usage.BodyNativeID), usage.Name, nilIfEmpty(usage.Path), usage.Evidence, usage.Status, nilIfEmpty(usage.CreatedAt), nullableInt(usage.ContentBytes)); err != nil {
				return err
			}
		}
	}
	for _, kind := range []string{"skill", "call", "request"} {
		table := "tool_calls"
		if kind == "request" {
			table = "model_requests"
		} else if kind == "skill" {
			table = "skill_usages"
		}
		rows, err := queryMaps(tx, "SELECT id FROM "+table+" WHERE conversation_id=?", conversationID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			id := firstString(row["id"])
			if retained[kind+":"+id] {
				continue
			}
			if _, err := tx.Exec("DELETE FROM "+table+" WHERE id=?", id); err != nil {
				return err
			}
			if _, err := tx.Exec("DELETE FROM tool_ledger_inputs WHERE conversation_id=? AND row_kind=? AND row_id=?", conversationID, kind, id); err != nil {
				return err
			}
			changed = true
		}
	}
	if _, err := tx.Exec(`INSERT INTO tool_ledger_state(conversation_id,version,tool_calls,updated_at) VALUES(?,?,?,?)
		ON CONFLICT(conversation_id) DO UPDATE SET version=excluded.version,tool_calls=excluded.tool_calls,updated_at=excluded.updated_at
		WHERE tool_ledger_state.version<>excluded.version OR tool_ledger_state.tool_calls<>excluded.tool_calls`,
		conversationID, toolLedgerVersion, len(calls), now()); err != nil {
		return err
	}
	if changed {
		return bumpToolLedgerPartitionGeneration(tx)
	}
	return nil
}

func derivedUpsertSQL(statement string) string {
	start, end := strings.Index(statement, "("), strings.Index(statement, ")")
	columns := strings.Split(statement[start+1:end], ",")
	updates := []string{}
	for _, column := range columns {
		column = strings.TrimSpace(column)
		if column != "id" {
			updates = append(updates, column+"=excluded."+column)
		}
	}
	return statement + " ON CONFLICT(id) DO UPDATE SET " + strings.Join(updates, ",")
}

// storedMessages reads a conversation's retained messages in source order.
func storedMessages(ctx context.Context, q queryer, conversationID any) ([]MessageRecord, error) {
	messages, err := queryMapsContext(ctx, q, "SELECT * FROM messages WHERE conversation_id=? ORDER BY source_order IS NULL,source_order,created_at,id", conversationID)
	if err != nil {
		return nil, err
	}
	records := make([]MessageRecord, 0, len(messages))
	for _, message := range messages {
		records = append(records, MessageRecord{
			NativeID: firstString(message["native_id"]), Role: firstString(message["role"]), Kind: firstString(message["kind"]),
			Text: firstString(message["text"]), RawText: firstString(message["raw_text"]), CreatedAt: firstString(message["created_at"]),
			Model:          firstString(message["model"]),
			ParentNativeID: firstString(message["parent_native_id"]), CallID: firstString(message["call_id"]),
			PreviousNativeID: firstString(message["previous_native_id"]),
			EvidenceLocator:  firstString(message["evidence_locator"]), SourceOrder: int(integer(message["source_order"])), Selected: integer(message["selected"]) != 0,
			Sender: firstString(message["sender"]),
		})
	}
	return records, nil
}

// BackfillToolLedger builds the tool ledger from stored messages for every
// conversation whose ledger is missing or was built by an older derivation.
// It needs no source files, so reclaimed TL1 work is covered too. Each
// conversation commits separately; an interrupted run resumes where it left off.
func (c *Catalog) BackfillToolLedger(ctx context.Context, progress func(done, total int)) (int, error) {
	state := &c.tools
	if !state.backfill.TryLock() {
		return 0, fmt.Errorf("a tool ledger build is already running")
	}
	defer state.backfill.Unlock()
	pending, err := queryMapsContext(ctx, c.DB, `SELECT c.id,c.workspace_id,c.provider,c.model FROM conversations c
		LEFT JOIN tool_ledger_state s ON s.conversation_id=c.id
		WHERE s.conversation_id IS NULL OR s.version<>? ORDER BY c.started_at DESC`, toolLedgerVersion)
	if err != nil {
		return 0, err
	}
	state.mu.Lock()
	state.running, state.done, state.total, state.lastError = true, 0, len(pending), ""
	state.mu.Unlock()
	finish := func(done int, err error) (int, error) {
		state.mu.Lock()
		state.running, state.done = false, done
		if err != nil {
			state.lastError = err.Error()
		}
		state.mu.Unlock()
		return done, err
	}
	for index, row := range pending {
		if err := ctx.Err(); err != nil {
			return finish(index, err)
		}
		messages, err := storedMessages(ctx, c.DB, row["id"])
		if err != nil {
			return finish(index, err)
		}
		conversation := ConversationRecord{Provider: firstString(row["provider"]), Model: firstString(row["model"]), Messages: messages}
		if err := c.writeTransaction(ctx, "tool-ledger conversation="+firstString(row["id"]), func(tx *sql.Tx) error {
			// The known checkouts are read in each write transaction: an index
			// running alongside may add some, and re-resolves only the rows
			// already written (see reresolveToolPaths).
			return replaceToolLedger(tx, firstString(row["workspace_id"]), firstString(row["id"]), conversation)
		}); err != nil {
			return finish(index, err)
		}
		state.mu.Lock()
		state.done = index + 1
		state.mu.Unlock()
		if progress != nil {
			progress(index+1, len(pending))
		}
	}
	return finish(len(pending), nil)
}

type toolLedgerBuild struct {
	running     bool
	done, total int
}

// toolLedgerProgress reports an in-process backfill, for the library status.
func (c *Catalog) toolLedgerProgress() toolLedgerBuild {
	state := &c.tools
	state.mu.Lock()
	defer state.mu.Unlock()
	return toolLedgerBuild{state.running, state.done, state.total}
}

// ToolLedgerStatus reports ledger coverage and any in-process backfill.
func (c *Catalog) ToolLedgerStatus(ctx context.Context) (map[string]any, error) {
	var conversations, current, calls int64
	if err := c.DB.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM conversations),
		(SELECT COUNT(*) FROM tool_ledger_state WHERE version=?),
		(SELECT COALESCE(SUM(tool_calls),0) FROM tool_ledger_state WHERE version=?)`, toolLedgerVersion, toolLedgerVersion).Scan(&conversations, &current, &calls); err != nil {
		return nil, err
	}
	state := &c.tools
	state.mu.Lock()
	defer state.mu.Unlock()
	return map[string]any{
		"version": toolLedgerVersion, "conversations": conversations, "current_conversations": current,
		"pending_conversations": max(conversations-current, 0), "tool_calls": calls,
		"backfill": map[string]any{"running": state.running, "done": state.done, "total": state.total, "error": nilIfEmpty(state.lastError)},
	}, nil
}

func (c *Catalog) metaValue(ctx context.Context, key string) (string, error) {
	var value string
	err := c.DB.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// toolRollupStale reports the ledger generation and local day a rollup
// should be built for, the generation the current one was built for, and
// whether it needs a rebuild.
func (c *Catalog) toolRollupStale(ctx context.Context) (generation, built, day string, stale bool, err error) {
	if generation, err = c.metaValue(ctx, "tool_ledger_generation"); err != nil {
		return
	}
	// A new rollup layout rebuilds even when the ledger is unchanged.
	generation += "/" + toolRollupVersion
	if built, err = c.metaValue(ctx, "tool_rollup_generation"); err != nil {
		return
	}
	day = c.clock().Format("2006-01-02")
	builtDay, err := c.metaValue(ctx, "tool_rollup_day")
	if err != nil {
		return generation, built, day, false, err
	}
	var dirty bool
	err = c.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tool_rollup_dirty) OR
 COALESCE((SELECT value FROM meta WHERE key='tool_rollup_full_revision'),'')<>COALESCE((SELECT value FROM meta WHERE key='tool_rollup_built_full_revision'),'') OR
 COALESCE((SELECT value FROM meta WHERE key='tool_rollup_timezone'),'')<>?`, toolRollupTimezone()).Scan(&dirty)
	return generation, built, day, generation != built || day != builtDay || dirty, err
}

// currentToolRollup is ensureToolRollup for a page. A full rebuild reads every
// tool call, half a minute on a large catalog, so once this build's layout
// exists, a page reads it while the rebuild runs in the background: it lags
// only the last sync or a change of date.
func (c *Catalog) currentToolRollup(ctx context.Context) error {
	_, built, _, stale, err := c.toolRollupStale(ctx)
	if err != nil || !stale {
		return err
	}
	if !strings.HasSuffix(built, "/"+toolRollupVersion) {
		return c.ensureToolRollup(ctx)
	}
	var deferred bool
	if err := c.DB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sync_deferred WHERE name='tools' AND generation>completed_generation)").Scan(&deferred); err != nil {
		return err
	}
	if deferred {
		return nil
	}
	state := &c.tools
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.refreshing {
		return nil
	}
	refresh := func(ctx context.Context) {
		if err := c.ensureToolRollup(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "Tool rollup: %v\n", err)
		}
		state.mu.Lock()
		state.refreshing = false
		state.mu.Unlock()
	}
	state.refreshing = true
	if !c.runBackground(refresh) {
		state.refreshing = false // the service is stopping
	}
	return nil
}

// ensureToolRollup rebuilds tool_usage_daily and tool_call_cube when the
// ledger or identity links changed since the last build. While a backfill is
// writing, rebuilds are spaced out so each query does not redo the whole
// rollup.
func (c *Catalog) ensureToolRollup(ctx context.Context) error {
	state := &c.tools
	state.rollup.Lock()
	defer state.rollup.Unlock()
	generation, built, day, stale, err := c.toolRollupStale(ctx)
	if err != nil || !stale {
		return err
	}
	state.mu.Lock()
	running := state.running
	state.mu.Unlock()
	if running && built != "" && time.Since(state.rolledAt) < 30*time.Second {
		return nil
	}
	if err := c.updateToolRollup(ctx, generation, day, false); err != nil {
		return err
	}
	state.rolledAt = time.Now()
	return nil
}

// rebuildToolRollup groups tool calls into tool_call_cube, and sums that by
// local day and every summary dimension. Mirrors of the same work count once,
// as in Usage.
func (c *Catalog) rebuildToolRollup(ctx context.Context, generation, day string) error {
	return c.updateToolRollup(ctx, generation, day, true)
}

// toolUsageColumns are tool_usage_daily's columns.
var toolUsageColumns = []string{"id", "day", "week", "month", "repository_name", "source_kind", "provider", "model", "model_family", "session_kind",
	"tool_name", "tool_category", "mcp_server", "program", "subcommand", "command_name", "command_category", "call_count", "error_count", "error_rate",
	"no_result_count", "rejected_count", "interrupted_count", "timeout_count", "nonzero_exit_count", "hook_blocked_count", "truncated_count",
	"timed_count", "total_duration_ms", "avg_duration_ms", "max_duration_ms", "input_bytes", "result_bytes", "result_tokens", "measured_count",
	"avg_result_tokens", "carried_tokens", "output_tokens", "lines_added", "lines_removed", "work_count", "context_cost_usd", "output_cost_usd",
	"tool_cost_usd", "price_status"}

// toolUsageRecord turns one row of toolRollupFromCube into a tool_usage_daily
// row: its bucket fields, rates, and costs.
func toolUsageRecord(book priceBook, row map[string]any) map[string]any {
	dayValue := firstString(row["day"])
	var week, month any
	if parsed, err := time.Parse("2006-01-02", dayValue); err == nil {
		week = parsed.AddDate(0, 0, -((int(parsed.Weekday()) + 6) % 7)).Format("2006-01-02")
		month = parsed.Format("2006-01")
	}
	model := strings.TrimSpace(firstString(row["model"]))
	calls := integer(row["call_count"])
	var errorRate, avgDuration, avgResult any
	if calls > 0 {
		errorRate = float64(integer(row["error_count"])) / float64(calls) * 100
		avgResult = float64(integer(row["result_tokens"])) / float64(calls)
	}
	if timed := integer(row["timed_count"]); timed > 0 {
		avgDuration = float64(integer(row["total_duration_ms"])) / float64(timed)
	}
	// A result is first sent as new input (a cache write for Claude) and
	// then re-read from cache by every later request until compaction.
	context := map[string]int64{"cache_read_input_tokens": integer(row["carried_tokens"])}
	if firstString(row["provider"]) == "claude" {
		context["cache_creation_input_tokens"] = integer(row["result_tokens"])
	} else {
		context["uncached_input_tokens"] = integer(row["result_tokens"])
	}
	outputTokens, _ := number(row["output_tokens"])
	contextCost := book.cost(defaultString(nilIfEmpty(model), "Unknown model"), dayValue, context)
	outputCost := book.cost(defaultString(nilIfEmpty(model), "Unknown model"), dayValue, map[string]int64{"output_tokens": int64(outputTokens + 0.5)})
	total := addCost(addCost(nil, contextCost.cost), outputCost.cost)
	return map[string]any{
		"id": stableID("tool-usage", dayValue, row["repository_name"], row["source_kind"], row["provider"], model, row["session_kind"], row["tool_name"],
			row["tool_category"], row["mcp_server"], row["program"], row["subcommand"], row["command_category"]),
		"day": nilIfEmpty(dayValue), "week": week, "month": month, "repository_name": row["repository_name"], "source_kind": row["source_kind"],
		"provider": row["provider"], "model": nilIfEmpty(model), "model_family": nilIfEmpty(modelFamily(model)), "session_kind": row["session_kind"],
		"tool_name": row["tool_name"], "tool_category": row["tool_category"], "mcp_server": row["mcp_server"], "mcp_method": mcpMethod(asString(row["tool_name"]), row["mcp_server"]), "program": row["program"],
		"subcommand": row["subcommand"], "command_name": nilIfEmpty(strings.TrimSpace(firstString(row["program"]) + " " + firstString(row["subcommand"]))),
		"command_category": row["command_category"], "call_count": calls, "error_count": row["error_count"], "error_rate": errorRate,
		"no_result_count": row["no_result_count"], "rejected_count": row["rejected_count"], "interrupted_count": row["interrupted_count"],
		"timeout_count": row["timeout_count"], "nonzero_exit_count": row["nonzero_exit_count"], "hook_blocked_count": row["hook_blocked_count"],
		"truncated_count": row["truncated_count"], "timed_count": row["timed_count"], "total_duration_ms": row["total_duration_ms"],
		"avg_duration_ms": avgDuration, "max_duration_ms": row["max_duration_ms"], "input_bytes": row["input_bytes"], "result_bytes": row["result_bytes"],
		"result_tokens": row["result_tokens"], "measured_count": row["measured_count"], "avg_result_tokens": avgResult, "carried_tokens": row["carried_tokens"],
		"output_tokens": outputTokens, "lines_added": row["lines_added"], "lines_removed": row["lines_removed"], "work_count": row["work_count"],
		"context_cost_usd": costValue(contextCost.cost), "output_cost_usd": costValue(outputCost.cost), "tool_cost_usd": costValue(total),
		"price_status": worsePriceStatus(contextCost.status, outputCost.status),
	}
}

func costValue(value any) any {
	if cost, ok := value.(*float64); ok {
		if cost == nil {
			return nil
		}
		return *cost
	}
	return value
}
