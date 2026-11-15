//go:build probe

// Read-only audit of every agent session without a recorded token cost.
// No initialization, migrations, source acquisition, or repairs run:
//
// PHAROS_USAGE_AUDIT_CATALOG=/path/catalog.sqlite3 \
//
//	PHAROS_USAGE_AUDIT_OUTPUT=/path/audit.jsonl \
//	  go test -tags probe -run '^TestUsageCostAudit$' -v -timeout 60m ./internal/archive
package archive

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func auditCatalogURI(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(absolute), RawQuery: "mode=ro"}).String(), nil
}

// Match the writer's group accounting as well as the conversation-local
// accounting. Otherwise a root's group remainder can disappear on replay.
func auditSessionSummaries(ctx context.Context, tx *sql.Tx, id string, messages []MessageRecord) (map[string]agentSessionSummary, error) {
	var remainder map[string]tokenCounts
	var started string
	for _, message := range messages {
		raw, value := tokenObject(message)
		if firstString(value["type"], raw["type"]) != "cost-state" {
			continue
		}
		rows, err := queryMaps(tx, "SELECT workspace_id,provider,agent_depth FROM conversations WHERE id=?", id)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 || rows[0]["provider"] != "claude" || integer(rows[0]["agent_depth"]) != 0 {
			break
		}
		conversations, ids, err := storedWorkspaceConversations(ctx, tx, firstString(rows[0]["workspace_id"]))
		if err != nil {
			return nil, err
		}
		for i, conversation := range conversations {
			if ids[i] == id {
				remainder = claudeGroupRemainder(conversations, conversation)
				started = conversation.StartedAt
				break
			}
		}
		break
	}
	result := map[string]agentSessionSummary{}
	for _, summary := range agentSessionSummariesWithCostState(messages, remainder != nil) {
		if summary.nativeID == "main" {
			for model, counts := range remainder {
				addTokenCounts(summary.counts, counts)
				bucket := usageBucket{hour: usageHour(started), model: model, attribution: "session-remainder"}
				if summary.usage[bucket] == nil {
					summary.usage[bucket] = tokenCounts{}
				}
				addTokenCounts(summary.usage[bucket], counts)
			}
		}
		result[summary.nativeID] = summary
	}
	return result, nil
}

// Recheck the price gaps and structured result evidence from a completed
// inventory with the current accounting code, without repeating hierarchy
// resolution or writing the library. Set PHAROS_USAGE_AUDIT_INPUT to that
// inventory and PHAROS_USAGE_AUDIT_OUTPUT to a new JSONL file.
func TestUsageCostAuditReplay(t *testing.T) {
	path, input, output := os.Getenv("PHAROS_USAGE_AUDIT_CATALOG"), os.Getenv("PHAROS_USAGE_AUDIT_INPUT"), os.Getenv("PHAROS_USAGE_AUDIT_OUTPUT")
	if path == "" || input == "" || output == "" {
		t.Skip("set PHAROS_USAGE_AUDIT_CATALOG, PHAROS_USAGE_AUDIT_INPUT, and PHAROS_USAGE_AUDIT_OUTPUT")
	}
	uri, err := auditCatalogURI(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	c := &Catalog{DB: db, readOnly: true, now: time.Now}
	book, err := c.loadPriceBookFrom(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	encoder := json.NewEncoder(file)
	conversation := ""
	conversationStart, conversationModel := "", ""
	var summaries map[string]agentSessionSummary
	counts := map[string]int{}
	for scanner.Scan() {
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		var unpricedBefore int64
		for _, bucket := range sliceOf(row["unpriced_buckets"]) {
			unpricedBefore += integer(mapValueDefault(bucket)["tokens"])
		}
		if unpricedBefore == 0 && len(sliceOf(row["partial_price_buckets"])) == 0 {
			result := mapValueDefault(parseJSONValue(firstString(mapValueDefault(row["result"])["text"])))
			if normalizeTokenCounts(mapValue(mapValueDefault(result["details"])["usage"]))["total_tokens"] <= 0 {
				continue
			}
		}
		current := firstString(row["conversation_id"])
		if current != conversation {
			conversation = current
			messages, err := storedMessages(context.Background(), tx, current)
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := queryMaps(tx, "SELECT model,started_at FROM conversations WHERE id=?", current)
			if err != nil || len(metadata) != 1 {
				t.Fatalf("conversation metadata: %v", err)
			}
			conversationStart, conversationModel = firstString(metadata[0]["started_at"]), firstString(metadata[0]["model"])
			summaries, err = auditSessionSummaries(context.Background(), tx, current, messages)
			if err != nil {
				t.Fatal(err)
			}
		}
		summary := summaries[firstString(row["native_id"])]
		// Keep label-only changes separate from actual repricing. The input
		// inventory may select only one placeholder bucket of a mixed session.
		stored, err := queryMaps(tx, `SELECT * FROM agent_session_usage WHERE agent_session_id=?`, row["id"])
		if err != nil {
			t.Fatal(err)
		}
		beforeCost, afterCost := 0.0, 0.0
		var allUnpricedBefore int64
		for _, bucket := range stored {
			day := ""
			if at, ok := parseTime(firstString(bucket["usage_hour"])); ok {
				day = at.In(time.Local).Format("2006-01-02")
			}
			price := book.cost(defaultString(firstString(bucket["model"]), firstString(row["model"], conversationModel)), day, ledgerTokens(bucket))
			if price.cost == nil {
				allUnpricedBefore += integer(bucket["total_tokens"])
			} else {
				beforeCost += *price.cost
			}
		}
		var unpricedAfter, unclassifiedAfter int64
		buckets := []map[string]any{}
		for bucket, values := range summary.usage {
			if values["total_tokens"] <= 0 {
				continue
			}
			model := defaultString(nilIfEmpty(bucket.model), conversationModel)
			hour := defaultString(nilIfEmpty(bucket.hour), usageHour(conversationStart))
			day := ""
			if at, ok := parseTime(hour); ok {
				day = at.In(time.Local).Format("2006-01-02")
			}
			tokens := map[string]int64{}
			for _, field := range usageTokenFields {
				tokens[field] = integer(values[field])
			}
			// These fields are derived by writeAgentSessions, rather than
			// necessarily present on the source accounting object.
			tokens["uncached_input_tokens"] = max(tokens["input_tokens"]-tokens["cache_read_input_tokens"]-tokens["cache_creation_input_tokens"], 0)
			tokens["unclassified_tokens"] = max(tokens["total_tokens"]-tokens["input_tokens"]-tokens["output_tokens"], 0)
			unclassifiedAfter += tokens["unclassified_tokens"]
			price := book.cost(model, day, tokens)
			if price.cost == nil {
				unpricedAfter += tokens["total_tokens"]
			} else {
				afterCost += *price.cost
			}
			buckets = append(buckets, map[string]any{"hour": hour, "model": model, "tokens": tokens, "cost_usd": price.cost, "price_status": price.status})
		}
		classification := "unchanged-or-unresolved"
		if summary.counts["total_tokens"] > 0 && integer(row["total_tokens"]) == 0 {
			classification = "recovered-result-usage"
		} else if unpricedAfter < unpricedBefore {
			classification = "recovered-model-attribution"
		} else if unclassifiedAfter < integer(row["unclassified_tokens"]) && integer(summary.counts["total_tokens"]) == integer(row["total_tokens"]) {
			classification = "recovered-token-breakdown"
		}
		counts[classification]++
		if err := encoder.Encode(map[string]any{"id": row["id"], "conversation_id": current, "source_kind": row["source_kind"],
			"classification": classification, "before_tokens": row["total_tokens"], "after_tokens": summary.counts["total_tokens"],
			"before_cost_usd": beforeCost, "after_cost_usd": afterCost, "cost_delta_usd": afterCost - beforeCost,
			"before_all_unpriced_tokens": allUnpricedBefore,
			"before_unpriced_tokens":     unpricedBefore, "after_unpriced_tokens": unpricedAfter,
			"before_unclassified_tokens": row["unclassified_tokens"], "after_unclassified_tokens": unclassifiedAfter, "buckets": buckets}); err != nil {
			t.Fatal(err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Logf("replayed retained evidence: %v; output %s", counts, output)
}

func TestUsageCostAudit(t *testing.T) {
	path, output := os.Getenv("PHAROS_USAGE_AUDIT_CATALOG"), os.Getenv("PHAROS_USAGE_AUDIT_OUTPUT")
	if path == "" || output == "" {
		t.Skip("set PHAROS_USAGE_AUDIT_CATALOG and PHAROS_USAGE_AUDIT_OUTPUT")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(absolute); err != nil {
		t.Fatal(err)
	}
	uri, err := auditCatalogURI(absolute)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	c := &Catalog{DB: db, readOnly: true, now: time.Now}
	book, err := c.loadPriceBookFrom(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := queryMaps(tx, `SELECT s.*,c.native_id AS conversation_native_id,c.origin,c.harness,
		c.agent_depth,c.model AS conversation_model,w.source_kind,w.title AS workspace_title
		FROM agent_sessions s JOIN conversations c ON c.id=s.conversation_id
		JOIN workspaces w ON w.id=s.workspace_id ORDER BY s.conversation_id,s.id`)
	if err != nil {
		t.Fatal(err)
	}
	// A launch's child transcript, in or outside its workspace, and a refused
	// launch come from the shared execution resolution of tx's rows, resolved
	// once when the first unlinked launch needs it.
	var resolution *executionResolution
	ledger, err := queryMaps(tx, `SELECT u.*,u.model AS usage_model,s.model AS session_model,c.model AS conversation_model
		FROM agent_session_usage u JOIN agent_sessions s ON s.id=u.agent_session_id
		JOIN conversations c ON c.id=s.conversation_id`)
	if err != nil {
		t.Fatal(err)
	}
	priced := map[string]bool{}
	partial := map[string][]map[string]any{}
	ledgerCounts := map[string]int{}
	unpriced := map[string][]map[string]any{}
	for _, row := range ledger {
		id := firstString(row["agent_session_id"])
		ledgerCounts[id]++
		model, hour := ledgerModel(row), firstString(row["usage_hour"])
		day := ""
		if at, ok := parseTime(hour); ok {
			day = at.In(time.Local).Format("2006-01-02")
		}
		cost := book.cost(model, day, ledgerTokens(row))
		if cost.cost != nil {
			priced[id] = true
			if cost.status == "partial" {
				partial[id] = append(partial[id], map[string]any{"model": model, "day": day, "tokens": row["total_tokens"], "unclassified_tokens": row["unclassified_tokens"], "price_status": cost.status, "known_cost_usd": cost.cost})
			}
		} else {
			unpriced[id] = append(unpriced[id], map[string]any{"model": model, "day": day, "tokens": row["total_tokens"], "price_status": cost.status})
		}
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	summary := map[string]int{}
	var conversationID string
	var messages []MessageRecord
	var recomputed map[string]agentSessionSummary
	var members map[string][]MessageRecord
	started := time.Now()
	for index, session := range sessions {
		id := firstString(session["id"])
		if priced[id] && len(unpriced[id]) == 0 && len(partial[id]) == 0 && integer(session["total_tokens"]) > 0 {
			continue
		}
		session["ledger_rows"], session["unpriced_buckets"] = ledgerCounts[id], unpriced[id]
		session["partial_price_buckets"] = partial[id]
		child := firstString(session["child_conversation_id"])
		failed := false
		if integer(session["total_tokens"]) == 0 && child == "" && firstString(session["native_id"]) != "main" {
			if resolution == nil {
				if resolution, err = resolveExecutionsFrom(context.Background(), tx); err != nil {
					t.Fatal(err)
				}
			}
			// As usage attribution binds a launch: a resolved or inherited
			// execution whose transcript is not the launching conversation.
			if b := resolution.BindSession(id); b.ExecutionID != "" && (b.State == bindingResolved || b.State == bindingInherited) {
				if x := resolution.Executions[b.ExecutionID]; x != nil && x.Conversation != firstString(session["conversation_id"]) {
					child = x.Conversation
				}
			}
			if a := resolution.Attempts[sourceRef{sourceSession, id}]; a != nil {
				failed = a.Outcome == attemptRejected
			}
			if child != "" {
				session["resolved_child_conversation_id"] = child
			}
		}
		if priced[id] {
			session["classification"] = "partially-unpriced"
			if len(unpriced[id]) == 0 {
				session["classification"] = "partial-price"
			}
		} else if integer(session["total_tokens"]) > 0 {
			session["classification"] = "usage-without-price"
			if ledgerCounts[id] == 0 {
				session["classification"] = "usage-without-ledger"
			}
		} else if failed {
			session["classification"] = "launch-failed-without-execution"
		} else if child != "" {
			childRows, childErr := queryMaps(tx, "SELECT id,native_id,total_tokens,usage_status FROM agent_sessions WHERE conversation_id=?", child)
			if childErr != nil {
				t.Fatal(childErr)
			}
			session["linked_child_sessions"] = childRows
			session["classification"] = "linked-child-usage-unavailable"
			for _, childRow := range childRows {
				if integer(childRow["total_tokens"]) > 0 {
					session["classification"] = "usage-owned-by-linked-child"
					break
				}
			}
		} else {
			current := firstString(session["conversation_id"])
			if conversationID != current {
				conversationID = current
				messages, err = storedMessages(context.Background(), tx, current)
				if err != nil {
					t.Fatal(err)
				}
				recomputed, err = auditSessionSummaries(context.Background(), tx, current, messages)
				if err != nil {
					t.Fatal(err)
				}
				members = map[string][]MessageRecord{}
				membership, memberErr := queryMaps(tx, `SELECT s.native_id,m.native_id AS message_native_id FROM agent_session_messages sm
					JOIN agent_sessions s ON s.id=sm.agent_session_id JOIN messages m ON m.id=sm.message_id
					WHERE s.conversation_id=?`, current)
				if memberErr != nil {
					t.Fatal(memberErr)
				}
				byID := map[string]MessageRecord{}
				for _, message := range messages {
					byID[message.NativeID] = message
				}
				for _, row := range membership {
					key := firstString(row["native_id"])
					members[key] = append(members[key], byID[firstString(row["message_native_id"])])
				}
			}
			native := firstString(session["native_id"])
			own := members[native]
			session["retained_messages"] = len(own)
			session["recomputed_tokens"] = recomputed[native].counts
			session["classification"] = "no-recognized-usage-in-retained-messages"
			if len(own) == 0 {
				session["classification"] = "no-retained-session-messages"
			}
			if recomputed[native].counts["total_tokens"] > 0 {
				session["classification"] = "recoverable-from-retained-messages"
			}
			observations := []map[string]any{}
			zeroReports, copies := 0, map[string]bool{}
			for _, message := range own {
				raw, value := tokenObject(message)
				if owner := usageCopyOf(message); owner != "" {
					copies[owner] = true
				}
				msg := mapValueDefault(value["message"])
				response := mapValueDefault(value["response"])
				info := mapValueDefault(value["info"])
				for _, usage := range []any{msg["usage"], response["usage"], value["usage"], info["total_token_usage"]} {
					if counts := normalizeTokenCounts(mapValue(usage)); counts != nil && counts["total_tokens"] == 0 {
						zeroReports++
					}
				}
				if !hasUsageEvidence(raw) {
					continue
				}
				observations = append(observations, map[string]any{"native_id": message.NativeID, "kind": message.Kind,
					"role": message.Role, "type": firstString(value["type"], raw["type"]), "locator": message.EvidenceLocator,
					"usage_copy": usageCopyOf(message), "stream": firstString(value["agent_id"], raw["parent_tool_use_id"]),
					"raw_keys": sortedKeys(raw)})
			}
			session["explicit_zero_reports"], session["usage_copy_owners"] = zeroReports, sortedKeys(copies)
			if recomputed[native].counts["total_tokens"] <= 0 {
				if zeroReports > 0 {
					session["classification"] = "explicit-zero-usage"
				}
				if len(copies) > 0 {
					session["classification"] = "usage-copy-evidence"
				}
			}
			minimum := int64(0)
			for _, evidence := range conversationTokenEvidence(messages) {
				if evidence.stream == native {
					minimum = max(minimum, evidence.total)
				}
			}
			if minimum > 0 {
				session["observed_minimum_tokens"] = minimum
				if recomputed[native].counts["total_tokens"] <= 0 {
					session["classification"] = "reported-minimum-without-price"
				}
			}
			session["usage_evidence_messages"] = len(observations)
			if len(observations) > 5 {
				observations = observations[:5]
			}
			session["evidence_examples"] = observations
			for _, message := range messages {
				if message.Kind == "delegation" && defaultString(nilIfEmpty(message.CallID), message.NativeID) == native {
					session["delegation"] = map[string]any{"native_id": message.NativeID, "parent_native_id": message.ParentNativeID, "text": message.Text}
				}
				if message.Kind == "delegation_result" && message.CallID == native {
					session["result"] = map[string]any{"native_id": message.NativeID, "text": message.Text}
				}
			}
		}
		classification := firstString(session["classification"])
		summary[classification]++
		if err := encoder.Encode(session); err != nil {
			t.Fatal(err)
		}
		if index%100 == 0 {
			t.Logf("inspected %d/%d sessions in %s; findings %v", index, len(sessions), time.Since(started).Round(time.Second), summary)
		}
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	t.Logf("complete: %d sessions, %d usage buckets; findings %v; output %s", len(sessions), len(ledger), summary, output)
}

// Replay a previously exported conversation into a disposable catalog for UI
// inspection. This never opens the installed library or source databases.
func TestUsageLayerPreview(t *testing.T) {
	input, output := os.Getenv("PHAROS_TOKEN_LAYER_MESSAGES"), os.Getenv("PHAROS_TOKEN_LAYER_OUTPUT")
	if input == "" || output == "" {
		t.Skip("set PHAROS_TOKEN_LAYER_MESSAGES and PHAROS_TOKEN_LAYER_OUTPUT")
	}
	raw, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err = json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	messages := make([]MessageRecord, 0, len(rows))
	for _, m := range rows {
		messages = append(messages, MessageRecord{NativeID: firstString(m["native_id"]), Role: firstString(m["role"]), Kind: firstString(m["kind"]), Text: firstString(m["text"]), RawText: firstString(m["raw_text"]), Model: firstString(m["model"]), CreatedAt: firstString(m["created_at"]), ParentNativeID: firstString(m["parent_native_id"]), CallID: firstString(m["call_id"]), Selected: true})
	}
	c, config := testCatalog(t)
	record := WorkspaceRecord{SourceID: "layer-preview", SourceKind: "conductor", Account: "local", Title: "Token attribution preview", Conversations: []ConversationRecord{{NativeID: "layer-preview", Provider: "claude", Account: "local", Model: "opus-5-1m", Messages: messages}}}
	tx, err := c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = ingestWorkspace(tx, record, false); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	workspace := stableID("workspace", "conductor", "local", "layer-preview")
	overview, err := c.WorkOverview(workspace)
	if err != nil {
		t.Fatal(err)
	}
	var conversation string
	if err = c.DB.QueryRow("SELECT id FROM conversations WHERE workspace_id=?", workspace).Scan(&conversation); err != nil {
		t.Fatal(err)
	}
	body, err := c.WorkConversation(workspace, conversation)
	if err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, c)
	status, delegation := delegationGET(t, server, "/api/work/"+workspace+"/delegation?limit=200&conversation="+conversation)
	if status != 200 {
		t.Fatalf("%d %v", status, delegation)
	}
	value := map[string]any{"workspace": workspace, "conversation": conversation, "overview": overview, "body": body, "delegation": delegation}
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("previewed %d messages, %d scoped observations", len(messages), len(conversationTokenEvidence(messages)))
}
