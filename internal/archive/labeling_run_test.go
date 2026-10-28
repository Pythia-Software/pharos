package archive

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

// Fake model CLIs are this test binary started through a small script:
// TestMain runs labelingFakeCLI when labelingFakeEnv is set. The script sets
// the scenarios (one per call, comma-separated; the last repeats), a file
// counting the calls, and a file per call (PATH.N) recording its argv and
// stdin.
const (
	labelingFakeEnv   = "PHAROS_LABELING_FAKE"
	labelingFakeCalls = "PHAROS_LABELING_FAKE_CALLS"
	labelingFakeArgs  = "PHAROS_LABELING_FAKE_ARGS"
)

const fakeLabel = "P.request.work.investigate"

func labelingFakeCLI() int {
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("9.9.9 (fake)")
		return 0
	}
	route := labelingRouteClaude
	if len(args) > 0 && args[0] == "exec" {
		route = labelingRouteCodex
	}
	file, err := os.OpenFile(os.Getenv(labelingFakeCalls), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return 2
	}
	syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
	previous, _ := io.ReadAll(file)
	fmt.Fprintln(file, route)
	file.Close()
	call := strings.Count(string(previous), "\n")
	scenarios := strings.Split(os.Getenv(labelingFakeEnv), ",")
	scenario := scenarios[min(call, len(scenarios)-1)]
	prompt, _ := io.ReadAll(os.Stdin)
	recorded, _ := json.Marshal(map[string]any{"argv": args, "stdin": string(prompt)})
	os.WriteFile(fmt.Sprintf("%s.%d", os.Getenv(labelingFakeArgs), call+1), recorded, 0o600)
	flag := func(name string) string {
		for index, arg := range args {
			if arg == name && index+1 < len(args) {
				return args[index+1]
			}
		}
		return ""
	}
	start := strings.Index(string(prompt), "INDEPENDENT ITEMS:\n")
	end := strings.LastIndex(string(prompt), "\n\nReturn all requested IDs exactly once.")
	var items []map[string]any
	if start < 0 || end < start || json.Unmarshal(prompt[start+len("INDEPENDENT ITEMS:\n"):end], &items) != nil {
		fmt.Fprintln(os.Stderr, "fake: no items in the prompt")
		return 2
	}
	out := bufio.NewWriter(os.Stdout)
	emit := func(event map[string]any) {
		data, _ := json.Marshal(event)
		out.Write(append(data, '\n'))
		out.Flush()
	}
	switch scenario {
	case "crash-before":
		return 3
	case "hang":
		time.Sleep(30 * time.Second)
		return 0
	case "slow":
		time.Sleep(300 * time.Millisecond)
	case "slower":
		time.Sleep(1500 * time.Millisecond)
	}
	annotation := func(item map[string]any, quote string) map[string]any {
		return map[string]any{"id": item["id"], "labels": []any{fakeLabel}, "uncertain_labels": []any{},
			"evidence": []any{map[string]any{"label": fakeLabel, "quote": quote}}, "need_context": false}
	}
	annotations := []any{}
	for index, item := range items {
		text := firstString(item["CURRENT_TEXT"])
		quote := text
		if at := strings.Index(quote, "⟦"); at >= 0 {
			quote = quote[:at]
		}
		quote = strings.TrimSpace(firstRunes(strings.TrimSpace(quote), 12))
		switch scenario {
		case "missing", "other-missing":
			if index == len(items)-1 {
				continue
			}
		case "context-quote":
			if at := strings.Index(text, "⟦"); at >= 0 {
				quote = "the agent said"
			} else {
				quote = firstRunes(firstString(item["PREVIOUS_CONTEXT"]), 10)
			}
		case "repeated":
			if strings.Count(text, "fix it") > 1 {
				quote = "fix it"
			}
		}
		annotations = append(annotations, annotation(item, quote))
	}
	if scenario == "extra" {
		annotations = append(annotations, annotation(map[string]any{"id": "ghost"}, "x"), annotations[0])
	}
	output := map[string]any{"annotations": annotations}
	session := defaultString(flag("--session-id"), "fake-session")
	if route == labelingRouteCodex {
		answer, _ := json.Marshal(output)
		emit(map[string]any{"type": "thread.started", "thread_id": "thread-fake"})
		emit(map[string]any{"type": "turn.started"})
		emit(map[string]any{"type": "item.completed", "item": map[string]any{"id": "item_0", "type": "reasoning", "text": "PRIVATE THOUGHT"}})
		if scenario == "tool-answer-hang" {
			emit(map[string]any{"type": "item.completed", "item": map[string]any{"id": "item_1", "type": "command_execution", "command": "ls", "status": "completed"}})
		}
		if scenario == "answer-hang" || scenario == "tool-answer-hang" {
			os.WriteFile(flag("--output-last-message"), answer, 0o600)
			time.Sleep(30 * time.Second)
			return 0
		}
		if scenario == "failed" {
			emit(map[string]any{"type": "turn.failed", "error": map[string]any{"message": "stream disconnected before completion"}})
			return 1
		}
		if scenario == "policy" {
			emit(map[string]any{"type": "turn.failed", "error": map[string]any{"code": "invalid_prompt", "message": "Invalid prompt: your prompt was flagged as potentially violating our usage policy."}})
			return 1
		}
		if scenario == "conn-refused" {
			emit(map[string]any{"type": "turn.failed", "error": map[string]any{"message": "tcp connect error: Connection refused (os error 61)"}})
			return 1
		}
		if scenario == "tool" {
			emit(map[string]any{"type": "item.completed", "item": map[string]any{"id": "item_1", "type": "command_execution", "command": "ls", "status": "completed"}})
		}
		emit(map[string]any{"type": "item.completed", "item": map[string]any{"id": "item_2", "type": "agent_message", "text": string(answer)}})
		if scenario != "no-answer-file" {
			os.WriteFile(flag("--output-last-message"), answer, 0o600)
		}
		emit(map[string]any{"type": "turn.completed", "usage": map[string]any{"input_tokens": 17000, "cached_input_tokens": 1000, "output_tokens": 900, "reasoning_output_tokens": 400}})
		if scenario == "crash-after" {
			time.Sleep(30 * time.Second)
		}
		return 0
	}
	model := flag("--model")
	fmt.Fprintf(os.Stderr, "[claude-code:unrecognized_model] {\"model\":%q}\nplain stderr line\n", model)
	served := model
	if scenario == "other-model" || scenario == "other-missing" {
		served = model + "-20261001"
	}
	usage := map[string]any{"input_tokens": 2, "cache_creation_input_tokens": 15000, "cache_read_input_tokens": 0, "output_tokens": 1000,
		"cache_creation": map[string]any{"ephemeral_1h_input_tokens": 15000, "ephemeral_5m_input_tokens": 0}}
	modelUsage := map[string]any{served: map[string]any{"inputTokens": 2, "outputTokens": 1000, "cacheReadInputTokens": 0, "cacheCreationInputTokens": 15000, "costUSD": 0.01}}
	switch scenario {
	case "unpriced":
		modelUsage["claude-mystery-9"] = map[string]any{"inputTokens": 300, "outputTokens": 20, "cacheReadInputTokens": 0, "cacheCreationInputTokens": 0}
	case "unbounded":
		// No provider can be inferred: neither priced nor bounded.
		modelUsage["mystery-model-9"] = map[string]any{"inputTokens": 300, "outputTokens": 20, "cacheReadInputTokens": 0, "cacheCreationInputTokens": 0}
	case "mismatch-usage":
		// modelUsage sums more than the result's usage, so no model matches it.
		modelUsage[model] = map[string]any{"inputTokens": 2, "outputTokens": 1200, "cacheReadInputTokens": 0, "cacheCreationInputTokens": 20000}
		modelUsage["claude-haiku-4-5-20251001"] = map[string]any{"inputTokens": 500, "outputTokens": 30, "cacheReadInputTokens": 0, "cacheCreationInputTokens": 0}
	case "ambiguous-usage":
		// Two models wrote to the cache; the one-hour writes cannot be assigned.
		modelUsage[model] = map[string]any{"inputTokens": 2, "outputTokens": 1200, "cacheReadInputTokens": 0, "cacheCreationInputTokens": 20000}
		modelUsage["claude-haiku-4-5-20251001"] = map[string]any{"inputTokens": 500, "outputTokens": 30, "cacheReadInputTokens": 0, "cacheCreationInputTokens": 3000}
	}
	emit(map[string]any{"type": "system", "subtype": "init", "session_id": session, "model": served, "tools": []any{"StructuredOutput"}, "cwd": "/private/secret"})
	if scenario == "init-hang" {
		time.Sleep(30 * time.Second)
		return 0
	}
	if scenario == "refuse-hang" {
		// A refusal stop reason, then no result.
		emit(map[string]any{"type": "assistant", "session_id": session, "message": map[string]any{"model": served, "stop_reason": "refusal",
			"content": []any{map[string]any{"type": "text", "text": "No."}}}})
		time.Sleep(30 * time.Second)
		return 0
	}
	content := []any{map[string]any{"type": "thinking", "thinking": "PRIVATE THOUGHT", "signature": "sig"}}
	if scenario == "tool" {
		content = append(content, map[string]any{"type": "tool_use", "id": "tool-1", "name": "Bash", "input": map[string]any{"command": "ls"}})
	}
	result := map[string]any{"type": "result", "subtype": "success", "is_error": false, "num_turns": 2, "result": "", "session_id": session,
		"total_cost_usd": 0.01, "usage": usage, "modelUsage": modelUsage, "structured_output": output}
	switch scenario {
	case "refusal":
		delete(result, "structured_output")
		result["result"] = "I can't help with labeling this text."
	case "refusal-stop":
		delete(result, "structured_output")
		result["result"], result["stop_reason"] = "No.", "refusal"
	case "failed":
		result["subtype"], result["is_error"] = "error_during_execution", true
		delete(result, "structured_output")
	case "suspect":
		// Prose that cuts off a JSON answer quoting a difficulty.
		delete(result, "structured_output")
		result["result"] = `{"annotations":[{"id":"i_a","evidence":[{"quote":"I can't get the build to`
	case "policy":
		// Claude Code's own error text for a policy block.
		result["subtype"], result["is_error"] = "success", true
		result["result"] = "API Error: Claude Code is unable to respond to this request, which appears to violate our Usage Policy (https://www.anthropic.com/legal/aup)."
		delete(result, "structured_output")
	case "conn-refused":
		// A connection failure, with result text that merely mentions a refusal.
		result["subtype"], result["is_error"] = "error_during_execution", true
		result["errors"] = []any{"API Error: Connection error. ECONNREFUSED 127.0.0.1:443"}
		result["result"] = "the user refused the merge"
		delete(result, "structured_output")
		delete(result, "usage")
		delete(result, "modelUsage")
	case "unexplained":
		// modelUsage lists only an auxiliary model; the labeling call's usage is
		// in the result alone.
		result["modelUsage"] = map[string]any{"claude-haiku-4-5-20251001": map[string]any{"inputTokens": 5000, "outputTokens": 20, "cacheReadInputTokens": 0, "cacheCreationInputTokens": 0}}
	case "total-only":
		result["usage"] = map[string]any{"total_tokens": 16000}
		delete(result, "modelUsage")
	case "no-usage":
		delete(result, "usage")
		delete(result, "modelUsage")
	case "auth":
		result["subtype"], result["is_error"] = "success", true
		result["result"] = "Invalid API key · Please run /login"
		delete(result, "structured_output")
		delete(result, "modelUsage")
		delete(result, "usage")
	default:
		content = append(content, map[string]any{"type": "tool_use", "id": "tool-2", "name": "StructuredOutput", "input": output})
	}
	message := map[string]any{"model": served, "content": content, "usage": usage}
	if scenario == "refusal-stop" {
		message["stop_reason"] = "refusal"
	}
	emit(map[string]any{"type": "assistant", "session_id": session, "message": message})
	emit(result)
	switch scenario {
	case "linger":
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(30 * time.Second)
	case "crash-after":
		time.Sleep(30 * time.Second)
	}
	return 0
}

// fakeLabelingCLI writes a fake CLI running the scenarios and returns its
// path, the file counting its calls, and the prefix of the files recording
// each call.
func fakeLabelingCLI(t *testing.T, scenarios string) (path, calls, last string) {
	t.Helper()
	dir := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path, calls, last = filepath.Join(dir, "fake-cli"), filepath.Join(dir, "calls"), filepath.Join(dir, "call")
	script := fmt.Sprintf("#!/bin/sh\n%s='%s' %s='%s' %s='%s' exec '%s' \"$@\"\n", labelingFakeEnv, scenarios, labelingFakeCalls, calls, labelingFakeArgs, last, binary)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, calls, last
}

func fakeCalls(t *testing.T, calls string) int {
	data, err := os.ReadFile(calls)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "\n")
}

// A fixture unit is invented text in segments: target text, or other
// material shown as a marker (empty for dropped harness text). A transformed
// target shows decoded text for its encoded bytes.
type fixtureSegment struct {
	text, marker, visible string
	target                bool
}

type fixtureUnit struct {
	key, cohort, sender, channel, context, duplicate string
	// conversation defaults to one per unit.
	conversation string
	excerpt      bool
	segments     []fixtureSegment
	chunks       []int
}

func labelingFixtureUnits() []fixtureUnit {
	target := func(text string) fixtureSegment { return fixtureSegment{text: text, target: true} }
	return []fixtureUnit{
		{key: "a", cohort: "human", sender: "user", channel: "unspecified", context: "Previous assistant message:\nI updated the parser",
			segments: []fixtureSegment{target("Please fix it and fix it again. "),
				{text: "> the agent said: done\n", marker: `⟦quoted text, context only, 23 chars: "the agent said: done"⟧`}, target("Thanks")}},
		{key: "b", cohort: "agent_reply", sender: "assistant", channel: "unspecified", context: "Previous user request:\nwhy does it crash",
			segments: []fixtureSegment{target("I investigated the crash and found the cause.")}},
		{key: "c", cohort: "human", sender: "user", channel: "unspecified", context: "",
			segments: []fixtureSegment{{text: `"Looks \"wrong\" here"`, visible: `Looks "wrong" here`, target: true}}},
		{key: "d", cohort: "thinking", sender: "assistant", channel: "analysis", context: strings.Repeat("ctx ", 150), excerpt: true,
			segments: []fixtureSegment{target("First part of the plan. Second part with émoji 🚀 here.")}, chunks: []int{len("First part of the plan. ")}},
		{key: "e", cohort: "delegation", sender: "assistant", channel: "unspecified", duplicate: "m_f",
			segments: []fixtureSegment{target("Run the tests and report back.")}},
		{key: "f", cohort: "agent_in_user_role", sender: "user", channel: "unspecified", context: "Previous user request:\nship it",
			segments: []fixtureSegment{target("Run the tests and report back.")}},
	}
}

// writeLabelingFixture writes a prepared dataset of invented units.
func writeLabelingFixture(t *testing.T, units []fixtureUnit) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "dataset")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, labelingDatasetFile))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(labelingDatasetSQL); err != nil {
		t.Fatal(err)
	}
	tx := seedTx(t, db)
	for _, unit := range units {
		message, visible := "", ""
		markers := [][2]int{}
		type piece struct {
			role                     string
			start, end, vStart, vEnd int
			transform                string
		}
		pieces := []piece{}
		for _, segment := range unit.segments {
			start := len(message)
			message += segment.text
			if segment.target {
				shown := defaultString(segment.visible, segment.text)
				p := piece{role: pieceTarget, start: start, end: len(message), vStart: len(visible), vEnd: len(visible) + len(shown)}
				if segment.visible != "" {
					p.transform = feedbackJSONTransform
				}
				pieces = append(pieces, p)
				visible += shown
				continue
			}
			pieces = append(pieces, piece{role: pieceContext, start: start, end: len(message), vStart: -1})
			if segment.marker != "" {
				markers = append(markers, [2]int{len(visible), len(visible) + len(segment.marker)})
				visible += segment.marker
			}
		}
		unitID := "u_" + unit.key
		bounds := append(append([]int{0}, unit.chunks...), len(visible))
		if _, err := tx.Exec(`INSERT INTO units(id,message_id,conversation_id,workspace_id,depth_status,cohort,actor_kind,sender_role,provider,kind,channel,
			message_sha256,message_text,visible_text,visible_chars,marker_ranges,length_bucket,chunk_count,attached_marker_chunks,context_text,context_prompt,
			context_excerpt,context_refs,duplicate_of,split) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			unitID, "m_"+unit.key, defaultString(unit.conversation, "c_"+unit.key), "w_1", "resolved", unit.cohort, "agent", unit.sender, "claude", "message", unit.channel,
			hashBytes([]byte(message)), message, visible, utf8.RuneCountInString(visible), jsonText(markers), "short", len(bounds)-1, 0,
			unit.context, lastRunes(unit.context, 600), unit.excerpt, "[]", nilIfEmpty(unit.duplicate), "train"); err != nil {
			t.Fatal(err)
		}
		for position, p := range pieces {
			var vStart, vEnd any
			if p.vStart >= 0 {
				vStart, vEnd = p.vStart, p.vEnd
			}
			if _, err := tx.Exec(`INSERT INTO pieces(unit_id,position,role,status,start_byte,end_byte,visible_start,visible_end,transform) VALUES(?,?,?,?,?,?,?,?,?)`,
				unitID, position, p.role, "fixture", p.start, p.end, vStart, vEnd, nilIfEmpty(p.transform)); err != nil {
				t.Fatal(err)
			}
		}
		for index := 0; index+1 < len(bounds); index++ {
			id := "i_" + unit.key
			if len(bounds) > 2 {
				id += fmt.Sprint(index)
			}
			text := visible[bounds[index]:bounds[index+1]]
			if _, err := tx.Exec(`INSERT INTO items(id,unit_id,chunk_index,chunk_count,visible_start,visible_end,text,chars,source_ranges,target_hash) VALUES(?,?,?,?,?,?,?,?,?,?)`,
				id, unitID, index, len(bounds)-1, bounds[index], bounds[index+1], text, utf8.RuneCountInString(text), "[]", hashBytes([]byte(text))); err != nil {
				t.Fatal(err)
			}
		}
	}
	manifest := map[string]any{"contracts": map[string]any{"units": labelingUnitsVersion, "context": labelingContextVersion}, "population": "fixture"}
	if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('manifest',?),('state','complete')`, jsonText(manifest)); err != nil {
		t.Fatal(err)
	}
	commitSeed(t, tx)
	if err := durableJSON(filepath.Join(dir, labelingManifestFile), manifest); err != nil {
		t.Fatal(err)
	}
	return dir
}

var labelingTestDay = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

func labelingTestOptions(dir, cli string, out io.Writer) labelingRunOptions {
	return labelingRunOptions{Dir: dir, Job: "pilot", Create: true, MaxUSD: 5, Concurrency: 2, RetryInvalid: 1, CLIPath: cli, Out: out,
		Now: func() time.Time { return labelingTestDay }, Timeout: 20 * time.Second,
		Spec: labelingJobSpec{Route: labelingRouteClaude, Model: "claude-haiku-5-5", Effort: "high"}}
}

// quickLabelingTimers keeps the waits short; a CLI that exits on its own
// (slowly under -race) still gets seconds before it counts as lingering.
func quickLabelingTimers(t *testing.T) {
	override(t, &labelingLingerGrace, 5*time.Second)
	override(t, &labelingKillGrace, 100*time.Millisecond)
	override(t, &labelingPollInterval, 10*time.Millisecond)
	override(t, &labelingStopGrace, 5*time.Second)
}

func openFixtureLedger(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := openLabelingLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func requestStates(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows := labelingRows(t, db, `SELECT id,state FROM requests ORDER BY position`)
	states := []string{}
	for _, row := range rows {
		states = append(states, firstString(row["state"]))
	}
	return strings.Join(states, ",")
}

func itemOutcomes(t *testing.T, db *sql.DB, attempt string) map[string]map[string]any {
	t.Helper()
	results := map[string]map[string]any{}
	for _, row := range labelingRows(t, db, `SELECT * FROM item_results WHERE attempt_id LIKE ?`, attempt) {
		var evidence []labelingEvidence
		json.Unmarshal([]byte(firstString(row["evidence"])), &evidence)
		row["evidence"] = evidence
		results[firstString(row["item_id"])] = row
	}
	return results
}

func TestLabelingRunLabelsAndValidatesThroughClaude(t *testing.T) {
	quickLabelingTimers(t)
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, last := fakeLabelingCLI(t, "ok")
	var out strings.Builder
	if err := runLabelingJob(context.Background(), labelingTestOptions(dir, cli, &out)); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	db := openFixtureLedger(t, dir)
	if got := requestStates(t, db); got != "completed,completed" || fakeCalls(t, calls) != 2 {
		t.Fatalf("states %s after %d calls\n%s", got, fakeCalls(t, calls), out.String())
	}
	if !strings.Contains(out.String(), `"duplicate_items_skipped": 1`) || !strings.Contains(out.String(), "pilot:000002/1: completed, 2/2 items usable") {
		t.Fatalf("output:\n%s", out.String())
	}
	results := itemOutcomes(t, db, "pilot:000001/1")
	for _, id := range []string{"i_a", "i_b", "i_c", "i_d0"} {
		if results[id]["outcome"] != itemUsable {
			t.Fatalf("%s = %v", id, results[id])
		}
	}
	// Evidence maps to original bytes: literally, through a transform, and
	// from a later chunk.
	evidence := results["i_a"]["evidence"].([]labelingEvidence)[0]
	if evidence.Quote != "Please fix i" || evidence.Occurrences[0].Source[0]["start"] != float64(0) || evidence.Occurrences[0].Source[0]["end"] != float64(12) {
		t.Fatalf("i_a evidence = %+v", evidence)
	}
	if source := results["i_c"]["evidence"].([]labelingEvidence)[0].Occurrences[0].Source[0]; source["transform"] != feedbackJSONTransform || source["end"] != float64(22) {
		t.Fatalf("i_c source = %v", source)
	}
	later := itemOutcomes(t, db, "pilot:000002/1")["i_d1"]["evidence"].([]labelingEvidence)[0]
	if later.Quote != "Second part" || later.Occurrences[0].Source[0]["start"] != float64(len("First part of the plan. ")) {
		t.Fatalf("i_d1 evidence = %+v", later)
	}
	// The ledger: usage priced per model, the CLI's own cost apart, the
	// version once, private reasoning stripped from the saved stream.
	attempt, err := labelingAttemptRecord(db, "pilot:000001/1")
	if err != nil {
		t.Fatal(err)
	}
	if attempt["outcome"] != requestCompleted || attempt["terminal_event"] != "result" || attempt["cli_cost_usd"] != 0.01 || attempt["exit_code"] != int64(0) ||
		integer(attempt["duration_ms"]) <= 0 || compactJSON(attempt["cleanup"]) != "[]" || attempt["served_model"] != servedModelVerified ||
		attempt["cli_version"] != "9.9.9 (fake)" || attempt["process_start"] == nil {
		t.Fatalf("attempt = %v", attempt)
	}
	usage := labelingRows(t, db, `SELECT * FROM usage WHERE attempt_id='pilot:000001/1'`)
	if len(usage) != 1 || usage[0]["cost_status"] != "priced" || usage[0]["model_source"] != "result usage" || !strings.Contains(firstString(usage[0]["tokens"]), `"cache_creation_1h_input_tokens":15000`) {
		t.Fatalf("usage = %v", usage)
	}
	// 15,000 one-hour cache writes at $0.20/M, 2 input at $0.10/M, 1,000 output at $0.50/M.
	if cost := usage[0]["cost_usd"].(float64); cost < 0.0035 || cost > 0.00351 {
		t.Fatalf("cost = %v", cost)
	}
	if rows := labelingRows(t, db, `SELECT cli_version FROM jobs`); rows[0]["cli_version"] != "9.9.9 (fake)" {
		t.Fatalf("cli version = %v", rows)
	}
	events, err := os.ReadFile(filepath.Join(firstString(attempt["dir"]), "events.jsonl"))
	if err != nil || strings.Contains(string(events), "PRIVATE THOUGHT") || strings.Contains(string(events), "/private/secret") || !strings.Contains(string(events), `"usage"`) {
		t.Fatalf("events.jsonl keeps reasoning or loses usage: %s", events)
	}
	// The argv carries every isolation flag, the schema as measured, and
	// stdin the exact prompt; command.json records the argv without them.
	var called struct {
		Argv  []string
		Stdin string
	}
	data, _ := os.ReadFile(last + ".1")
	if err := json.Unmarshal(data, &called); err != nil {
		t.Fatalf("%v: %.300s", err, data)
	}
	schema, _ := labelingSchemaJSON()
	joined := strings.Join(called.Argv, " ")
	for _, want := range []string{"-p --model claude-haiku-5-5 --effort high --output-format stream-json --verbose --json-schema", "--tools  --safe-mode",
		"--strict-mcp-config", "--permission-mode dontAsk", "--no-session-persistence --session-id"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv lacks %q: %s", want, joined)
		}
	}
	if !strings.Contains(joined, schema) {
		t.Fatal("argv lacks the schema")
	}
	first, _ := os.ReadFile(filepath.Join(firstString(attempt["dir"]), "prompt.txt"))
	second, _ := os.ReadFile(filepath.Join(dir, "attempts", "pilot", "000002", "1", "prompt.txt"))
	if called.Stdin != string(first) && called.Stdin != string(second) || !strings.HasPrefix(called.Stdin, labelingPromptBase[:40]) || !strings.Contains(called.Stdin, labelingPiecesSentence) {
		t.Fatal("stdin is not the attempt's prompt")
	}
	command, _ := os.ReadFile(filepath.Join(firstString(attempt["dir"]), "command.json"))
	if strings.Contains(string(command), "annotations") || strings.Contains(string(command), "Please fix") {
		t.Fatalf("command.json holds the schema or prompt: %s", command)
	}
	if info, err := os.Stat(filepath.Join(firstString(attempt["dir"]), "events.jsonl")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("events.jsonl mode = %v, %v", info, err)
	}
	var status strings.Builder
	if err := runLabelingCLI("", []string{"status", "--dir", dir, "--job", "pilot", "--json"}, &status, io.Discard); err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(status.String()), &report); err != nil {
		t.Fatal(err)
	}
	if report["item_outcomes"].(map[string]any)["usable"] != float64(6) || report["spend"].(map[string]any)["known_usd"].(float64) < 0.007 {
		t.Fatalf("status = %s", status.String())
	}
}

func TestLabelingRunThroughCodex(t *testing.T) {
	quickLabelingTimers(t)
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, last := fakeLabelingCLI(t, "ok")
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Spec = labelingJobSpec{Route: labelingRouteCodex, Model: "gpt-6-luna", Effort: "high", Prompt: labelingPromptReproduction}
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	db := openFixtureLedger(t, dir)
	if got := requestStates(t, db); got != "completed,completed" || fakeCalls(t, calls) != 2 {
		t.Fatalf("states %s", got)
	}
	usage := labelingRows(t, db, `SELECT * FROM usage WHERE attempt_id='pilot:000001/1'`)
	// 16,000 uncached + 1,000 cached input, 900 output (reasoning included).
	if len(usage) != 1 || usage[0]["model"] != "gpt-6-luna" || usage[0]["cost_status"] != "priced" || !strings.Contains(firstString(usage[0]["tokens"]), `"uncached_input_tokens":16000`) {
		t.Fatalf("usage = %v", usage)
	}
	attempt, _ := labelingAttemptRecord(db, "pilot:000001/1")
	events, _ := os.ReadFile(filepath.Join(firstString(attempt["dir"]), "events.jsonl"))
	served := labelingRows(t, db, `SELECT (SELECT served_model FROM jobs) job,(SELECT COUNT(*) FROM item_results WHERE served_model=? AND outcome='usable') usable`, servedModelUnverified)
	if served[0]["job"] != servedModelUnverified || served[0]["usable"] != int64(6) || attempt["served_model"] != servedModelUnverified {
		t.Fatalf("served model = %v, attempt %v", served, attempt["served_model"])
	}
	if attempt["session_id"] != "thread-fake" || strings.Contains(string(events), "PRIVATE THOUGHT") {
		t.Fatalf("attempt = %v, events %s", attempt, events)
	}
	var called struct{ Argv []string }
	data, _ := os.ReadFile(last + ".1")
	json.Unmarshal(data, &called)
	joined := strings.Join(called.Argv, " ")
	for _, want := range []string{"exec --ephemeral --ignore-user-config --ignore-rules --skip-git-repo-check --sandbox read-only --model gpt-6-luna",
		`model_reasoning_effort="high"`, "--json --output-schema " + filepath.Join(dir, "contracts"), "--disable shell_tool", " -"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv lacks %q: %s", want, joined)
		}
	}
}

func TestLabelingRunValidatesEachItem(t *testing.T) {
	quickLabelingTimers(t)
	for _, check := range []struct {
		scenarios, states string
		retry             int
		calls             int
		verify            func(t *testing.T, db *sql.DB)
	}{
		// A missing item is re-sent once, alone; the rest stay usable.
		// A missing id makes the whole request untrusted: it is re-sent once.
		{"missing,ok", "invalid,completed,completed", 1, 3, func(t *testing.T, db *sql.DB) {
			results := itemOutcomes(t, db, "pilot:000001/1")
			if results["i_d0"]["outcome"] != itemMissing || results["i_a"]["outcome"] != itemInvalid || !strings.Contains(firstString(results["i_a"]["issues"]), "request_id_set_mismatch") {
				t.Fatalf("results = %v", results)
			}
			rows := labelingRows(t, db, `SELECT r.retry_of,r.chars,i.item_id FROM requests r JOIN request_items i ON i.request_id=r.id WHERE r.position=3`)
			if len(rows) != 4 || rows[0]["retry_of"] != "pilot:000001" {
				t.Fatalf("retry = %v", rows)
			}
			var original, retried int64
			db.QueryRow(`SELECT chars FROM requests WHERE position=1`).Scan(&original)
			db.QueryRow(`SELECT SUM(chars) FROM request_items WHERE request_id=?`, "pilot:000003").Scan(&retried)
			if integer(rows[0]["chars"]) != retried || retried != original {
				t.Fatalf("retry chars %v, items %d, original %d", rows[0]["chars"], retried, original)
			}
		}},
		// Extra and duplicate ids break the request: every item is invalid.
		{"extra", "invalid,invalid", 0, 2, func(t *testing.T, db *sql.DB) {
			attempt, _ := labelingAttemptRecord(db, "pilot:000001/1")
			problems := compactJSON(attempt["problems"])
			if !strings.Contains(problems, "extra_id:ghost") || !strings.Contains(problems, "duplicate_id:i_a") {
				t.Fatalf("problems = %s", problems)
			}
			results := itemOutcomes(t, db, "pilot:000001/1")
			if results["i_a"]["outcome"] != itemInvalid || results["i_b"]["outcome"] != itemInvalid || !strings.Contains(firstString(results["i_b"]["issues"]), "request_id_set_mismatch") {
				t.Fatalf("results = %v", results)
			}
		}},
		// Quotes from a marker or the context are not evidence.
		{"context-quote", "completed,completed", 1, 2, func(t *testing.T, db *sql.DB) {
			results := itemOutcomes(t, db, "pilot:000001/1")
			for id, want := range map[string]string{"i_a": "in_marker", "i_b": "in_context"} {
				evidence := results[id]["evidence"].([]labelingEvidence)
				if results[id]["outcome"] != itemEvidenceIssues || evidence[0].Status != want || !strings.Contains(firstString(results[id]["issues"]), "label_without_valid_evidence") ||
					firstString(results[id]["labels_evidence"]) != `{"`+fakeLabel+`":"`+want+`"}` {
					t.Fatalf("%s = %v", id, results[id])
				}
			}
		}},
		// A repeated quote records every occurrence.
		{"repeated", "completed,completed", 1, 2, func(t *testing.T, db *sql.DB) {
			evidence := itemOutcomes(t, db, "pilot:000001/1")["i_a"]["evidence"].([]labelingEvidence)[0]
			if !evidence.Ambiguous || len(evidence.Occurrences) != 2 || evidence.Occurrences[1].Source[0]["start"] != float64(len("Please fix it and ")) {
				t.Fatalf("evidence = %+v", evidence)
			}
		}},
		// Refusals are kept and never retried: refusal text, or a refusal stop reason.
		{"refusal", "refused,refused", 1, 2, func(t *testing.T, db *sql.DB) {
			if got := itemOutcomes(t, db, "pilot:000001/1")["i_a"]["outcome"]; got != itemRefused {
				t.Fatalf("i_a = %v", got)
			}
		}},
		{"refusal-stop", "refused,refused", 1, 2, func(t *testing.T, db *sql.DB) {
			if attempt, _ := labelingAttemptRecord(db, "pilot:000001/1"); attempt["refusal"] != "stop_reason refusal" {
				t.Fatalf("attempt = %v", attempt)
			}
		}},
		// Any tool use invalidates the attempt; its items are retried once.
		{"tool", "invalid,invalid", 1, 2, func(t *testing.T, db *sql.DB) {
			attempt, _ := labelingAttemptRecord(db, "pilot:000001/1")
			if !strings.Contains(compactJSON(attempt["tool_events"]), "Bash") {
				t.Fatalf("tool events = %v", attempt["tool_events"])
			}
		}},
		{"failed", "failed,failed", 1, 2, nil},
	} {
		t.Run(check.scenarios, func(t *testing.T) {
			dir := writeLabelingFixture(t, labelingFixtureUnits())
			cli, calls, _ := fakeLabelingCLI(t, check.scenarios)
			opts := labelingTestOptions(dir, cli, io.Discard)
			opts.RetryInvalid, opts.Concurrency = check.retry, 1
			if err := runLabelingJob(context.Background(), opts); err != nil {
				t.Fatal(err)
			}
			db := openFixtureLedger(t, dir)
			if got := requestStates(t, db); got != check.states || fakeCalls(t, calls) != check.calls {
				t.Fatalf("states %s after %d calls", got, fakeCalls(t, calls))
			}
			if check.verify != nil {
				check.verify(t, db)
			}
		})
	}
}

func TestLabelingRunCleansUpWithoutResending(t *testing.T) {
	quickLabelingTimers(t)
	// A CLI that lingers after its result, ignoring SIGTERM, is killed; the
	// result stands and is not re-sent.
	override(t, &labelingLingerGrace, 100*time.Millisecond)
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ := fakeLabelingCLI(t, "linger")
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Spec.LimitItems = 4
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	db := openFixtureLedger(t, dir)
	attempt, _ := labelingAttemptRecord(db, "pilot:000001/1")
	cleanup := compactJSON(attempt["cleanup"])
	if requestStates(t, db) != "completed" || fakeCalls(t, calls) != 1 || !strings.Contains(cleanup, "lingering after its terminal event") || !strings.Contains(cleanup, "killed") {
		t.Fatalf("states %s, calls %d, cleanup %s", requestStates(t, db), fakeCalls(t, calls), cleanup)
	}
	if pid := int(integer(attempt["pid"])); syscall.Kill(-pid, 0) == nil {
		t.Fatal("the CLI's process group survived")
	}
	// A hang ends unknown at the timeout, and so does a crash before any
	// output: both count as possibly charged and are not re-sent until
	// reconcile re-queues them explicitly.
	for _, check := range []struct {
		scenario, state, flag string
		exposed               int
	}{{"hang", requestUnknown, "--retry-unknown", 1}, {"crash-before", requestUnknown, "--retry-unknown", 1}} {
		dir := writeLabelingFixture(t, labelingFixtureUnits())
		cli, calls, _ := fakeLabelingCLI(t, check.scenario+",ok")
		opts := labelingTestOptions(dir, cli, io.Discard)
		opts.Spec.LimitItems, opts.Timeout = 4, 300*time.Millisecond
		if err := runLabelingJob(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
		opts.Create = false
		if err := runLabelingJob(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
		db := openFixtureLedger(t, dir)
		if got := requestStates(t, db); got != check.state || fakeCalls(t, calls) != 1 {
			t.Fatalf("%s: states %s after %d calls", check.scenario, got, fakeCalls(t, calls))
		}
		status, err := labelingJobStatus(db, "pilot", labelingTestDay)
		if err != nil {
			t.Fatal(err)
		}
		if spend := status["spend"].(labelingSpend); spend.UnknownAttempts != check.exposed || (spend.UnknownExposure > 0) != (check.exposed > 0) {
			t.Fatalf("%s: spend = %+v", check.scenario, spend)
		}
		var out strings.Builder
		if err := runLabelingCLI("", []string{"reconcile", "--dir", dir, "--job", "pilot"}, &out, io.Discard); err != nil || !strings.Contains(out.String(), "pilot:000001 "+check.state) {
			t.Fatalf("reconcile = %v: %s", err, out.String())
		}
		if err := runLabelingCLI("", []string{"reconcile", "--dir", dir, "--job", "pilot", check.flag}, &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		opts.Timeout = 20 * time.Second
		if err := runLabelingJob(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
		// Re-queued unknown attempts stay in the job's lifetime spend.
		status, _ = labelingJobStatus(db, "pilot", labelingTestDay)
		if got := requestStates(t, db); got != requestCompleted || fakeCalls(t, calls) != 2 || status["spend"].(labelingSpend).UnknownAttempts != check.exposed {
			t.Fatalf("%s after retry: states %s, %d calls, spend %+v", check.scenario, got, fakeCalls(t, calls), status["spend"])
		}
	}
}

// TestLabelingHelperRunner is the runner process the crash test kills.
func TestLabelingHelperRunner(t *testing.T) {
	dir := os.Getenv("PHAROS_LABELING_HELPER_DIR")
	if dir == "" {
		t.Skip("helper process for TestLabelingRunResumesFromTheSavedStream")
	}
	opts := labelingTestOptions(dir, os.Getenv("PHAROS_LABELING_HELPER_CLI"), io.Discard)
	opts.Spec.LimitItems = 4
	if os.Getenv("PHAROS_LABELING_HELPER_ROUTE") == labelingRouteCodex {
		opts.Spec.Route, opts.Spec.Model = labelingRouteCodex, "gpt-6-luna"
	}
	runLabelingJob(context.Background(), opts)
}

func TestLabelingRunResumesFromTheSavedStream(t *testing.T) {
	quickLabelingTimers(t)
	for _, route := range []string{labelingRouteClaude, labelingRouteCodex} {
		t.Run(route, func(t *testing.T) { testLabelingResume(t, route) })
	}
}

func testLabelingResume(t *testing.T, route string) {
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ := fakeLabelingCLI(t, "crash-after")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runner := exec.Command(binary, "-test.run=^TestLabelingHelperRunner$")
	runner.Env = append(os.Environ(), testProcessEnv+"=1", "PHAROS_LABELING_HELPER_DIR="+dir, "PHAROS_LABELING_HELPER_CLI="+cli, "PHAROS_LABELING_HELPER_ROUTE="+route)
	if err := runner.Start(); err != nil {
		t.Fatal(err)
	}
	// Once the result is in the saved stream, the runner dies.
	events := filepath.Join(dir, "attempts", "pilot", "000001", "1", "events.jsonl")
	deadline := time.Now().Add(20 * time.Second)
	for {
		data, _ := os.ReadFile(events)
		if strings.Contains(string(data), `"type":"result"`) || strings.Contains(string(data), `"type":"turn.completed"`) {
			break
		}
		if time.Now().After(deadline) {
			runner.Process.Kill()
			t.Fatal("the result never reached the saved stream")
		}
		time.Sleep(20 * time.Millisecond)
	}
	runner.Process.Kill()
	runner.Wait()
	db := openFixtureLedger(t, dir)
	if got := requestStates(t, db); got != requestSubmitted {
		t.Fatalf("after the crash: %s", got)
	}
	attempt, _ := labelingAttemptRecord(db, "pilot:000001/1")
	pid := int(integer(attempt["pid"]))
	// Through codex, the recorded start time is made stale: the group then
	// is not the recorded CLI, and recovery only notes it.
	foreign := route == labelingRouteCodex
	if foreign {
		if _, err := db.Exec(`UPDATE attempts SET process_start='Thu Jan  1 00:00:00 1970'`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { syscall.Kill(-pid, syscall.SIGKILL) })
	}
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Create = false
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	attempt, _ = labelingAttemptRecord(db, "pilot:000001/1")
	want := "orphaned by an earlier runner"
	if foreign {
		want = "possibly_orphaned"
	}
	if cleanup := compactJSON(attempt["cleanup"]); !strings.Contains(cleanup, want) {
		t.Fatalf("cleanup = %s", cleanup)
	}
	if foreign {
		if syscall.Kill(-pid, 0) != nil {
			t.Fatal("recovery signalled a process group it could not identify")
		}
		syscall.Kill(-pid, syscall.SIGKILL)
		for deadline := time.Now().Add(5 * time.Second); syscall.Kill(-pid, 0) == nil && time.Now().Before(deadline); {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if got := requestStates(t, db); got != requestCompleted || fakeCalls(t, calls) != 1 || attempt["recovered"] != int64(1) || attempt["outcome"] != requestCompleted {
		t.Fatalf("after resume: %s, %d calls, attempt %v", got, fakeCalls(t, calls), attempt)
	}
	if syscall.Kill(-pid, 0) == nil {
		t.Fatal("the orphaned CLI still runs")
	}
}

func TestLabelingRunSpendLimits(t *testing.T) {
	quickLabelingTimers(t)
	// Usage the price book cannot price halts the run unless allowed.
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ := fakeLabelingCLI(t, "unpriced")
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Concurrency = 1
	if err := runLabelingJob(context.Background(), opts); !errors.Is(err, errLabelingHalted) || !strings.Contains(err.Error(), "claude-mystery-9") {
		t.Fatalf("unpriced run = %v", err)
	}
	db := openFixtureLedger(t, dir)
	if got := requestStates(t, db); got != "completed,pending" || fakeCalls(t, calls) != 1 {
		t.Fatalf("states %s", got)
	}
	opts.Create, opts.AllowUnpriced = false, true
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	status, _ := labelingJobStatus(db, "pilot", labelingTestDay)
	spend := status["spend"].(labelingSpend)
	if requestStates(t, db) != "completed,completed" || spend.UnpricedAttempts != 2 || spend.PartialTokens["claude-mystery-9 (estimated_upper_bound)"]["output_tokens"] != 40 ||
		spend.EstimatedUsage <= 0 {
		t.Fatalf("spend = %+v", spend)
	}
	// A model without a price does not start without --allow-unpriced.
	opts = labelingTestOptions(writeLabelingFixture(t, labelingFixtureUnits()), cli, io.Discard)
	opts.Spec.Model = "claude-made-up"
	if err := runLabelingJob(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "no price") {
		t.Fatalf("unpriced model = %v", err)
	}
	// The ceiling counts in-flight estimates: the second request is not
	// leased while the first runs, and the first finishes.
	dir = writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ = fakeLabelingCLI(t, "slow")
	opts = labelingTestOptions(dir, cli, io.Discard)
	opts.MaxUSD = 0.004
	if err := runLabelingJob(context.Background(), opts); !errors.Is(err, errLabelingHalted) || !strings.Contains(err.Error(), "spend ceiling") {
		t.Fatalf("ceiling run = %v", err)
	}
	db = openFixtureLedger(t, dir)
	if got := requestStates(t, db); got != "completed,pending" || fakeCalls(t, calls) != 1 {
		t.Fatalf("states %s after %d calls", got, fakeCalls(t, calls))
	}
}

func TestLabelingStopAndOneRunnerPerJob(t *testing.T) {
	quickLabelingTimers(t)
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ := fakeLabelingCLI(t, "slower")
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Concurrency, opts.Spec.ItemsPerRequest = 1, 1
	done := make(chan error, 1)
	go func() { done <- runLabelingJob(context.Background(), opts) }()
	waitFor := func(condition func() bool) {
		deadline := time.Now().Add(20 * time.Second)
		for !condition() {
			if time.Now().After(deadline) {
				t.Fatal("timed out")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor(func() bool { return fakeCalls(t, calls) == 1 })
	second := opts
	second.Create = false
	if err := runLabelingJob(context.Background(), second); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second runner = %v", err)
	}
	if err := runLabelingCLI("", []string{"reconcile", "--dir", dir, "--job", "pilot"}, io.Discard, io.Discard); err == nil {
		t.Fatal("reconcile ran beside the runner")
	}
	if err := runLabelingCLI("", []string{"stop", "--dir", dir, "--job", "pilot"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	db := openFixtureLedger(t, dir)
	if got := requestStates(t, db); !strings.HasPrefix(got, "completed,pending") || fakeCalls(t, calls) != 1 {
		t.Fatalf("after stop: %s, %d calls", got, fakeCalls(t, calls))
	}
	// SIGINT stops the same way; the next run resumes where it stopped.
	signals := make(chan os.Signal, 1)
	second.Signals = signals
	go func() { done <- runLabelingJob(context.Background(), second) }()
	waitFor(func() bool { return fakeCalls(t, calls) == 2 })
	signals <- os.Interrupt
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := requestStates(t, db); !strings.HasPrefix(got, "completed,completed,pending") || fakeCalls(t, calls) != 2 {
		t.Fatalf("after SIGINT: %s, %d calls", got, fakeCalls(t, calls))
	}
}

func TestLabelingImportsUnchangedResults(t *testing.T) {
	quickLabelingTimers(t)
	old := writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ := fakeLabelingCLI(t, "ok")
	if err := runLabelingJob(context.Background(), labelingTestOptions(old, cli, io.Discard)); err != nil {
		t.Fatal(err)
	}
	// A later snapshot changed one item's text and another's context.
	units := labelingFixtureUnits()
	units[1].segments[0].text = "I investigated the crash and fixed it."
	units[5].context = "Previous user request:\nship it today"
	dir := writeLabelingFixture(t, units)
	var out strings.Builder
	if err := runLabelingCLI("", []string{"import-results", "--dir", dir, "--from", old, "--job", "pilot"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Imported 4 usable results") || !strings.Contains(out.String(), "2 stale") {
		t.Fatalf("import:\n%s", out.String())
	}
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Create = false
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	db := openFixtureLedger(t, dir)
	sent := labelingRows(t, db, `SELECT item_id FROM request_items ORDER BY item_id`)
	if len(sent) != 2 || sent[0]["item_id"] != "i_b" || sent[1]["item_id"] != "i_f" || fakeCalls(t, calls) != 3 {
		t.Fatalf("sent %v after %d calls", sent, fakeCalls(t, calls))
	}
	status, _ := labelingJobStatus(db, "pilot", labelingTestDay)
	if status["item_outcomes"].(map[string]int)["usable"] != 6 {
		t.Fatalf("outcomes = %v", status["item_outcomes"])
	}
}

func TestLabelingRunNeedsACeiling(t *testing.T) {
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	if err := runLabelingCLI("", []string{"run", "--dir", dir, "--job", "pilot", "--create", "--route", "claude", "--model", "claude-haiku-5-5", "--effort", "high"},
		io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "--max-usd") {
		t.Fatalf("run without a ceiling = %v", err)
	}
	var out strings.Builder
	if err := runLabelingCLI("", []string{"run", "--dir", dir, "--job", "pilot", "--create", "--create-only", "--route", "claude", "--model", "claude-haiku-5-5",
		"--effort", "high", "--cohort", "human"}, &out, io.Discard); err != nil || !strings.Contains(out.String(), `"selected_items": 2`) {
		t.Fatalf("create-only = %v: %s", err, out.String())
	}
	if err := runLabelingCLI("", []string{"run", "--dir", dir, "--job", "pilot", "--create", "--create-only", "--route", "claude", "--model", "m", "--effort", "high"},
		io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("re-create = %v", err)
	}
}

func TestLabelingRunCircuitBreaker(t *testing.T) {
	quickLabelingTimers(t)
	// A CLI that always exits at once, before any session, halts the run
	// after three calls instead of using up the job.
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ := fakeLabelingCLI(t, "crash-before")
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Concurrency, opts.Spec.ItemsPerRequest = 1, 1
	if err := runLabelingJob(context.Background(), opts); !errors.Is(err, errLabelingHalted) || !strings.Contains(err.Error(), "3 consecutive") || fakeCalls(t, calls) != 3 {
		t.Fatalf("run = %v after %d calls", err, fakeCalls(t, calls))
	}
	db := openFixtureLedger(t, dir)
	// Once started, a CLI may have reached the provider: unknown, exposed.
	if rows := labelingRows(t, db, `SELECT COUNT(*) n FROM attempts WHERE not_sent=0 AND usage_missing=1 AND outcome='unknown' AND problems LIKE '%no_stream_events%'`); rows[0]["n"] != int64(3) {
		t.Fatalf("unknown attempts = %v", rows)
	}
	// An authentication error halts at once; the next run resumes.
	dir = writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ = fakeLabelingCLI(t, "auth,ok")
	opts = labelingTestOptions(dir, cli, io.Discard)
	opts.Concurrency = 1
	if err := runLabelingJob(context.Background(), opts); !errors.Is(err, errLabelingHalted) || !strings.Contains(err.Error(), "authentication") || fakeCalls(t, calls) != 1 {
		t.Fatalf("auth run = %v after %d calls", err, fakeCalls(t, calls))
	}
	db = openFixtureLedger(t, dir)
	status, _ := labelingJobStatus(db, "pilot", labelingTestDay)
	if !strings.Contains(firstString(status["halted"]), "authentication") || status["spend"].(labelingSpend).UnknownAttempts != 1 {
		t.Fatalf("status = %v", status)
	}
	opts.Create = false
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := requestStates(t, db); got != "failed,completed" {
		t.Fatalf("states %s", got)
	}
}

func TestLabelingRunHoldsAnotherServedModel(t *testing.T) {
	quickLabelingTimers(t)
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ := fakeLabelingCLI(t, "other-model")
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Concurrency = 1
	if err := runLabelingJob(context.Background(), opts); !errors.Is(err, errLabelingHalted) || !strings.Contains(err.Error(), "claude-haiku-5-5-20261001") || fakeCalls(t, calls) != 1 {
		t.Fatalf("run = %v after %d calls", err, fakeCalls(t, calls))
	}
	db := openFixtureLedger(t, dir)
	results := itemOutcomes(t, db, "pilot:000001/1")
	if requestStates(t, db) != "held,pending" || results["i_a"]["outcome"] != itemHeld || results["i_a"]["served_model"] != servedModelDiffers ||
		!strings.Contains(firstString(results["i_a"]["issues"]), servedModelDiffers) || firstString(results["i_a"]["labels"]) == "[]" {
		t.Fatalf("states %s, i_a %v", requestStates(t, db), results["i_a"])
	}
	// Its usage is recorded under the served name; no family match lends it
	// the requested model's price: it is bounded at the provider's highest
	// rates.
	if usage := labelingRows(t, db, `SELECT model,cost_status,cost_usd,priced_model FROM usage`); len(usage) != 1 || usage[0]["model"] != "claude-haiku-5-5-20261001" ||
		usage[0]["cost_status"] != costUpperBound || usage[0]["priced_model"] != "upper bound: anthropic" || usage[0]["cost_usd"].(float64) <= 0 {
		t.Fatalf("usage = %v", usage)
	}
	// Estimated, it is exposure, not known spend, and never $0.
	if status, _ := labelingJobStatus(db, "pilot", labelingTestDay); status["spend"].(labelingSpend).Known != 0 || status["spend"].(labelingSpend).EstimatedUsage <= 0 ||
		status["spend"].(labelingSpend).UnknownExposure < status["spend"].(labelingSpend).EstimatedUsage {
		t.Fatalf("spend = %+v", status["spend"])
	}
	// The job does not run again until the operator decides.
	opts.Create = false
	if err := runLabelingJob(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "--accept-served-model") || fakeCalls(t, calls) != 1 {
		t.Fatalf("rerun = %v after %d calls", err, fakeCalls(t, calls))
	}
	// Accepting the model releases the held items (issue kept) and admits
	// later attempts it serves; the decision is in the ledger.
	opts.AcceptServedModel = "claude-haiku-5-5-20261001"
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	results = itemOutcomes(t, db, "pilot:000001/1")
	later := itemOutcomes(t, db, "pilot:000002/1")
	accepted := labelingRows(t, db, `SELECT model,accepted_at FROM accepted_models`)
	if requestStates(t, db) != "completed,completed" || results["i_a"]["outcome"] != itemUsable || results["i_a"]["served_model"] != servedModelAccepted ||
		!strings.Contains(firstString(results["i_a"]["issues"]), servedModelDiffers) || later["i_f"]["outcome"] != itemUsable || len(accepted) != 1 || accepted[0]["accepted_at"] == nil {
		t.Fatalf("states %s, i_a %v, i_f %v, accepted %v", requestStates(t, db), results["i_a"], later["i_f"], accepted)
	}
	// Re-queueing is the other decision.
	dir = writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ = fakeLabelingCLI(t, "other-model,ok")
	opts = labelingTestOptions(dir, cli, io.Discard)
	opts.Concurrency, opts.Spec.LimitItems = 1, 4
	runLabelingJob(context.Background(), opts)
	if err := runLabelingCLI("", []string{"reconcile", "--dir", dir, "--job", "pilot", "--requeue-held"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	opts.Create = false
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := requestStates(t, openFixtureLedger(t, dir)); got != requestCompleted || fakeCalls(t, calls) != 2 {
		t.Fatalf("after requeue: %s, %d calls", got, fakeCalls(t, calls))
	}
}

func TestLabelingRunLedgerErrorLetsInFlightFinish(t *testing.T) {
	quickLabelingTimers(t)
	leases := 0
	override(t, &labelingLeaseHook, func() error {
		leases++
		if leases == 3 {
			return errors.New("disk I/O error")
		}
		return nil
	})
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ := fakeLabelingCLI(t, "slow")
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Concurrency, opts.Spec.ItemsPerRequest = 3, 1
	if err := runLabelingJob(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "disk I/O error") {
		t.Fatalf("run = %v", err)
	}
	db := openFixtureLedger(t, dir)
	if got := requestStates(t, db); got != "completed,completed,pending,pending,pending,pending" || fakeCalls(t, calls) != 2 {
		t.Fatalf("states %s after %d calls", got, fakeCalls(t, calls))
	}
}

func TestLabelingCodexAnswerWithoutTerminal(t *testing.T) {
	quickLabelingTimers(t)
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ := fakeLabelingCLI(t, "answer-hang")
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Spec = labelingJobSpec{Route: labelingRouteCodex, Model: "gpt-6-luna", Effort: "high", LimitItems: 4}
	opts.Timeout = 500 * time.Millisecond
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	db := openFixtureLedger(t, dir)
	attempt, _ := labelingAttemptRecord(db, "pilot:000001/1")
	status, _ := labelingJobStatus(db, "pilot", labelingTestDay)
	if requestStates(t, db) != requestCompleted || fakeCalls(t, calls) != 1 || !strings.Contains(compactJSON(attempt["problems"]), "answer_without_terminal") ||
		attempt["usage_missing"] != int64(1) || status["spend"].(labelingSpend).UnknownAttempts != 1 {
		t.Fatalf("states %s, attempt %v, spend %+v", requestStates(t, db), attempt, status["spend"])
	}
	var out strings.Builder
	if err := runLabelingCLI("", []string{"reconcile", "--dir", dir, "--job", "pilot"}, &out, io.Discard); err != nil || !strings.Contains(out.String(), "pilot:000001/1 completed without usage") {
		t.Fatalf("reconcile = %v: %s", err, out.String())
	}
}

func TestLabelingRunWithoutUsageHalts(t *testing.T) {
	quickLabelingTimers(t)
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, _, _ := fakeLabelingCLI(t, "no-usage")
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Concurrency = 1
	if err := runLabelingJob(context.Background(), opts); !errors.Is(err, errLabelingHalted) || !strings.Contains(err.Error(), "without usage") {
		t.Fatalf("run = %v", err)
	}
	db := openFixtureLedger(t, dir)
	status, _ := labelingJobStatus(db, "pilot", labelingTestDay)
	if requestStates(t, db) != "completed,pending" || status["spend"].(labelingSpend).UnknownExposure <= 0 {
		t.Fatalf("states %s, spend %+v", requestStates(t, db), status["spend"])
	}
}

func TestLabelingRunnerDiesBeforeTheResult(t *testing.T) {
	quickLabelingTimers(t)
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ := fakeLabelingCLI(t, "init-hang")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runner := exec.Command(binary, "-test.run=^TestLabelingHelperRunner$")
	runner.Env = append(os.Environ(), testProcessEnv+"=1", "PHAROS_LABELING_HELPER_DIR="+dir, "PHAROS_LABELING_HELPER_CLI="+cli)
	if err := runner.Start(); err != nil {
		t.Fatal(err)
	}
	events := filepath.Join(dir, "attempts", "pilot", "000001", "1", "events.jsonl")
	for deadline := time.Now().Add(20 * time.Second); ; {
		if data, _ := os.ReadFile(events); strings.Contains(string(data), `"subtype":"init"`) {
			break
		}
		if time.Now().After(deadline) {
			runner.Process.Kill()
			t.Fatal("the session never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	runner.Process.Kill()
	runner.Wait()
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Create = false
	for run := 0; run < 2; run++ {
		if err := runLabelingJob(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
	}
	db := openFixtureLedger(t, dir)
	attempt, _ := labelingAttemptRecord(db, "pilot:000001/1")
	if requestStates(t, db) != requestUnknown || fakeCalls(t, calls) != 1 || attempt["recovered"] != int64(1) || attempt["usage_missing"] != int64(1) {
		t.Fatalf("states %s after %d calls, attempt %v", requestStates(t, db), fakeCalls(t, calls), attempt)
	}
}

func TestLabelingRefusalsAreNotResent(t *testing.T) {
	quickLabelingTimers(t)
	old := writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ := fakeLabelingCLI(t, "refusal,ok")
	opts := labelingTestOptions(old, cli, io.Discard)
	opts.Concurrency = 1
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	db := openFixtureLedger(t, old)
	status, _ := labelingJobStatus(db, "pilot", labelingTestDay)
	if status["refused_items_co_packed"] != 4 {
		t.Fatalf("co-packed refusals = %v", status["refused_items_co_packed"])
	}
	// A second job on the same route skips the refused items.
	second := labelingTestOptions(old, cli, io.Discard)
	second.Job, second.CreateOnly = "again", true
	var out strings.Builder
	second.Out = &out
	if err := runLabelingJob(context.Background(), second); err != nil || !strings.Contains(out.String(), `"refused_skipped": 4`) {
		t.Fatalf("second job = %v: %s", err, out.String())
	}
	// A new snapshot imports the refusals as refusals; nothing is re-sent.
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	out.Reset()
	if err := runLabelingCLI("", []string{"import-results", "--dir", dir, "--from", old, "--job", "pilot"}, &out, io.Discard); err != nil || !strings.Contains(out.String(), "0 items left") {
		t.Fatalf("import = %v: %s", err, out.String())
	}
	if rows := labelingRows(t, openFixtureLedger(t, dir), `SELECT COUNT(*) n FROM item_results WHERE outcome='refused'`); rows[0]["n"] != int64(4) || fakeCalls(t, calls) != 2 {
		t.Fatalf("imported refusals = %v after %d calls", rows, fakeCalls(t, calls))
	}
}

func TestLabelingResumeChecksTheContract(t *testing.T) {
	quickLabelingTimers(t)
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ := fakeLabelingCLI(t, "ok")
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.CreateOnly = true
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	db := openFixtureLedger(t, dir)
	opts.Create, opts.CreateOnly = false, false
	execTx(t, db, `UPDATE jobs SET prompt_template_sha256='0000'`)
	if err := runLabelingJob(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "different prompt") {
		t.Fatalf("changed template = %v", err)
	}
	execTx(t, db, `UPDATE jobs SET prompt_template_sha256='`+labelingPromptTemplateHash(labelingPromptPieces)+`'`, `UPDATE items SET text='Please fix it now.' WHERE id='i_b'`)
	if err := runLabelingJob(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "no longer match") || fakeCalls(t, calls) != 0 {
		t.Fatalf("changed item = %v", err)
	}
}

func TestLabelingStopBetweenLeaseAndSubmit(t *testing.T) {
	quickLabelingTimers(t)
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	open := labelingOpenReceipt
	// The stop arrives after the request was leased and its files written.
	override(t, &labelingOpenReceipt, func(path string) (labelingReceiptFile, error) {
		if err := requestLabelingStop(dir, "pilot"); err != nil {
			return nil, err
		}
		return open(path)
	})
	cli, calls, _ := fakeLabelingCLI(t, "ok")
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Concurrency = 1
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := requestStates(t, openFixtureLedger(t, dir)); got != "stopped,pending" || fakeCalls(t, calls) != 0 {
		t.Fatalf("states %s after %d calls", got, fakeCalls(t, calls))
	}
}

func TestLabelingRunUsageAndCodexOutcomes(t *testing.T) {
	quickLabelingTimers(t)
	// Claude modelUsage that does not equal the result's usage: every model
	// is priced, and the requested one keeps the one-hour cache-write share.
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, _, _ := fakeLabelingCLI(t, "mismatch-usage")
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Spec.LimitItems, opts.AllowUnpriced = 4, true
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	db := openFixtureLedger(t, dir)
	usage := map[string]map[string]any{}
	for _, row := range labelingRows(t, db, `SELECT * FROM usage`) {
		usage[firstString(row["model"])] = row
	}
	main, auxiliary := usage["claude-haiku-5-5"], usage["claude-haiku-4-5-20251001"]
	if main == nil || auxiliary == nil || !strings.Contains(firstString(main["tokens"]), `"cache_creation_1h_input_tokens":15000`) ||
		!strings.Contains(firstString(main["tokens"]), `"cache_creation_input_tokens":20000`) || !strings.Contains(firstString(main["model_source"]), "1h") ||
		main["role"] != usagePrimary || auxiliary["role"] != usageAuxiliary || auxiliary["cost_status"] != "priced" || auxiliary["priced_model"] != "claude-haiku-4-5" {
		t.Fatalf("usage = %v", usage)
	}
	// The auxiliary call is visible in status, and so is the CLI's warning,
	// once.
	status, _ := labelingJobStatus(db, "pilot", labelingTestDay)
	spend := status["spend"].(labelingSpend)
	if spend.Auxiliary <= 0 || spend.AuxiliaryModels["claude-haiku-4-5-20251001"] != spend.Auxiliary || !strings.Contains(labelingStatusLine(status), "auxiliary models: claude-haiku-4-5-20251001") {
		t.Fatalf("auxiliary spend = %+v", spend)
	}
	if warnings := status["cli_warnings"].([]string); len(warnings) != 1 || !strings.HasPrefix(warnings[0], "[claude-code:unrecognized_model]") {
		t.Fatalf("cli warnings = %v", warnings)
	}
	// A ledger priced under an earlier rule (the pilot's: auxiliary usage
	// unpriced, which halted it) is re-priced at the next run start, so it
	// runs without --allow-unpriced.
	execTx(t, db, `UPDATE usage SET cost_status='unpriced',cost_usd=NULL,priced_model=NULL,role=NULL WHERE model='claude-haiku-4-5-20251001'`, `UPDATE jobs SET pricing_rule=NULL`)
	opts.Create, opts.AllowUnpriced = false, false
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if rows := labelingRows(t, db, `SELECT u.cost_status,u.role,(SELECT pricing_rule FROM jobs) rule FROM usage u WHERE model='claude-haiku-4-5-20251001'`); rows[0]["cost_status"] != "priced" ||
		rows[0]["role"] != usageAuxiliary || rows[0]["rule"] != labelingPricingRule {
		t.Fatalf("re-priced = %v", rows)
	}
	// Two cache writers and one-hour writes that cannot be assigned: every
	// write is priced at the one-hour rate, marked assumed.
	dir = writeLabelingFixture(t, labelingFixtureUnits())
	cli, _, _ = fakeLabelingCLI(t, "ambiguous-usage")
	opts = labelingTestOptions(dir, cli, io.Discard)
	opts.Spec.LimitItems = 4
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	for _, row := range labelingRows(t, openFixtureLedger(t, dir), `SELECT * FROM usage`) {
		var tokens map[string]int64
		json.Unmarshal([]byte(firstString(row["tokens"])), &tokens)
		if row["cost_status"] != "assumed" || tokens["cache_creation_1h_input_tokens"] != tokens["cache_creation_input_tokens"] || tokens["cache_creation_input_tokens"] == 0 {
			t.Fatalf("ambiguous usage = %v", row)
		}
	}
	// Codex: a failed turn after the session began is failed and possibly
	// charged; a tool item invalidates; a missing last-message file falls
	// back to the last agent message.
	for _, check := range []struct{ scenario, state string }{{"failed", requestFailed}, {"tool", requestInvalid}, {"no-answer-file", requestCompleted}} {
		dir := writeLabelingFixture(t, labelingFixtureUnits())
		cli, _, _ := fakeLabelingCLI(t, check.scenario)
		opts := labelingTestOptions(dir, cli, io.Discard)
		opts.Spec = labelingJobSpec{Route: labelingRouteCodex, Model: "gpt-6-luna", Effort: "high", LimitItems: 4}
		opts.RetryInvalid = 0
		if err := runLabelingJob(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
		db := openFixtureLedger(t, dir)
		status, _ := labelingJobStatus(db, "pilot", labelingTestDay)
		if got := requestStates(t, db); got != check.state || (check.scenario == "failed") != (status["spend"].(labelingSpend).UnknownAttempts == 1) {
			t.Fatalf("%s: states %s, spend %+v", check.scenario, got, status["spend"])
		}
	}
}

func TestLabelingCeilingWithTwoInFlight(t *testing.T) {
	quickLabelingTimers(t)
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ := fakeLabelingCLI(t, "slow")
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Concurrency, opts.Spec.ItemsPerRequest = 3, 1
	// One-item requests are estimated at about $0.0019: two fit, three do not.
	opts.MaxUSD = 0.005
	err := runLabelingJob(context.Background(), opts)
	if !errors.Is(err, errLabelingHalted) || !strings.Contains(err.Error(), "lifetime spend ceiling") || !strings.Contains(err.Error(), "higher --max-usd") {
		t.Fatalf("run = %v", err)
	}
	db := openFixtureLedger(t, dir)
	if got := requestStates(t, db); got != "completed,completed,pending,pending,pending,pending" || fakeCalls(t, calls) != 2 {
		t.Fatalf("states %s after %d calls", got, fakeCalls(t, calls))
	}
}

// failingReceipt accepts the first write, then fails.
type failingReceipt struct{ writes int }

func (f *failingReceipt) Write(p []byte) (int, error) {
	f.writes++
	if f.writes > 1 {
		return 0, errors.New("no space left on device")
	}
	return len(p), nil
}
func (f *failingReceipt) Sync() error  { return nil }
func (f *failingReceipt) Close() error { return nil }

func TestLabelingRunStopsWhenTheReceiptFails(t *testing.T) {
	quickLabelingTimers(t)
	override(t, &labelingOpenReceipt, func(string) (labelingReceiptFile, error) { return &failingReceipt{}, nil })
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ := fakeLabelingCLI(t, "ok")
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Concurrency = 1
	if err := runLabelingJob(context.Background(), opts); !errors.Is(err, errLabelingHalted) || !strings.Contains(err.Error(), "not durable") || fakeCalls(t, calls) != 1 {
		t.Fatalf("run = %v after %d calls", err, fakeCalls(t, calls))
	}
	db := openFixtureLedger(t, dir)
	attempt, _ := labelingAttemptRecord(db, "pilot:000001/1")
	if requestStates(t, db) != "unknown,pending" || !strings.Contains(firstString(attempt["receipt_error"]), "no space") {
		t.Fatalf("states %s, attempt %v", requestStates(t, db), attempt)
	}
	if rows := labelingRows(t, db, `SELECT COUNT(*) n FROM item_results`); rows[0]["n"] != int64(0) {
		t.Fatalf("results without a receipt: %v", rows)
	}
}

func TestLabelingCommandGuards(t *testing.T) {
	quickLabelingTimers(t)
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	// stop and reconcile never create run tables in a dataset without jobs.
	for _, command := range []string{"stop", "reconcile"} {
		if err := runLabelingCLI("", []string{command, "--dir", dir, "--job", "pilot"}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "no labeling jobs") {
			t.Fatalf("%s = %v", command, err)
		}
	}
	if foreign, err := labelingForeignTables(filepath.Join(dir, labelingDatasetFile)); err != nil || len(foreign) > 0 {
		t.Fatalf("tables created: %v, %v", foreign, err)
	}
	for _, value := range []string{"NaN", "+Inf", "-1", "0"} {
		err := runLabelingCLI("", []string{"run", "--dir", dir, "--job", "pilot", "--max-usd", value}, io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "finite") {
			t.Fatalf("--max-usd %s = %v", value, err)
		}
	}
	// Only a CLI that cannot start is known not to have been sent.
	missing := labelingTestOptions(writeLabelingFixture(t, labelingFixtureUnits()), filepath.Join(t.TempDir(), "no-such-cli"), io.Discard)
	missing.Spec.LimitItems, missing.Concurrency = 4, 1
	runLabelingJob(context.Background(), missing)
	if rows := labelingRows(t, openFixtureLedger(t, missing.Dir), `SELECT outcome,not_sent,usage_missing FROM attempts`); len(rows) != 1 || rows[0]["outcome"] != requestFailed ||
		rows[0]["not_sent"] != int64(1) || rows[0]["usage_missing"] != int64(0) {
		t.Fatalf("missing CLI = %v", rows)
	}
	opts := labelingTestOptions(dir, "unused", io.Discard)
	opts.MaxUSD = math.NaN()
	if err := runLabelingJob(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "finite") {
		t.Fatalf("NaN ceiling = %v", err)
	}
	// Unpriced usage recorded by an earlier run blocks the next one.
	cli, calls, _ := fakeLabelingCLI(t, "unbounded,ok")
	opts = labelingTestOptions(dir, cli, io.Discard)
	opts.Concurrency = 1
	if err := runLabelingJob(context.Background(), opts); !errors.Is(err, errLabelingHalted) {
		t.Fatalf("unpriced = %v", err)
	}
	opts.Create = false
	if err := runLabelingJob(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "neither priced nor bounded") || fakeCalls(t, calls) != 1 {
		t.Fatalf("resume without --allow-unpriced = %v after %d calls", err, fakeCalls(t, calls))
	}
}

func TestLabelingImportIsAllOrNothing(t *testing.T) {
	quickLabelingTimers(t)
	old := writeLabelingFixture(t, labelingFixtureUnits())
	cli, _, _ := fakeLabelingCLI(t, "ok")
	if err := runLabelingJob(context.Background(), labelingTestOptions(old, cli, io.Discard)); err != nil {
		t.Fatal(err)
	}
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	override(t, &labelingImportHook, func() error { return errors.New("simulated crash") })
	if err := runLabelingCLI("", []string{"import-results", "--dir", dir, "--from", old, "--job", "pilot"}, io.Discard, io.Discard); err == nil {
		t.Fatal("the failing import succeeded")
	}
	db := openFixtureLedger(t, dir)
	if rows := labelingRows(t, db, `SELECT (SELECT COUNT(*) FROM jobs) jobs,(SELECT COUNT(*) FROM requests) requests,(SELECT COUNT(*) FROM item_results) results`); rows[0]["jobs"] != int64(0) ||
		rows[0]["requests"] != int64(0) || rows[0]["results"] != int64(0) {
		t.Fatalf("a failed import left %v", rows)
	}
	labelingImportHook = func() error { return nil }
	var out strings.Builder
	if err := runLabelingCLI("", []string{"import-results", "--dir", dir, "--from", old, "--job", "pilot"}, &out, io.Discard); err != nil || !strings.Contains(out.String(), "Imported 6 usable results") {
		t.Fatalf("import = %v: %s", err, out.String())
	}
}

func TestLabelingClaudeArgvNamesTheSession(t *testing.T) {
	args, err := labelingCommand(labelingRouteClaude, "claude-haiku-5-5", "high", "{}", "", "", "00000000-0000-4000-8000-000000000000")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\x00")
	// Without a session name Claude Code makes a paid title-generation call.
	for _, want := range []string{"--name\x00Pharos labeling", "--prompt-suggestions\x00false", "--no-session-persistence", "--tools\x00\x00"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("argv lacks %q: %q", want, args)
		}
	}
}

func TestLabelingPackingKeepsFamiliesApart(t *testing.T) {
	tokens := []float64{10, 10, 10, 10, 10, 10}
	chars := []int{1, 1, 1, 1, 1, 1}
	families := []string{"a", "a", "b", "a", "c", "d"}
	requests, oversize := packLabelingFamilies(tokens, chars, families, 4, 0, 1000, 0)
	if len(oversize) != 0 || fmt.Sprint(requests) != "[[0 2 4 5] [1] [3]]" {
		t.Fatalf("requests = %v", requests)
	}
	// Through a job: two units of one conversation never share a request.
	units := labelingFixtureUnits()
	units[0].conversation, units[1].conversation, units[2].conversation = "c_same", "c_same", "c_same"
	dir := writeLabelingFixture(t, units)
	opts := labelingTestOptions(dir, "unused", io.Discard)
	opts.CreateOnly = true
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	rows := labelingRows(t, openFixtureLedger(t, dir), `SELECT i.request_id,COUNT(*) n,COUNT(DISTINCT u.conversation_id) c FROM request_items i JOIN items t ON t.id=i.item_id
		JOIN units u ON u.id=t.unit_id GROUP BY i.request_id`)
	for _, row := range rows {
		if row["n"] != row["c"] {
			t.Fatalf("a request mixes one conversation's items: %v", rows)
		}
	}
	if len(rows) != 3 {
		t.Fatalf("requests = %v", rows)
	}
}

func TestLabelingRefusalSignals(t *testing.T) {
	quickLabelingTimers(t)
	// A cut-off JSON answer that quotes "I can't" is only suspected, and
	// re-sent only on request.
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ := fakeLabelingCLI(t, "suspect,ok")
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Concurrency, opts.Spec.LimitItems = 1, 4
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	db := openFixtureLedger(t, dir)
	if got := requestStates(t, db); got != requestRefusalSuspected || itemOutcomes(t, db, "pilot:000001/1")["i_a"]["outcome"] != itemRefusalSuspected {
		t.Fatalf("states %s", got)
	}
	if err := runLabelingCLI("", []string{"reconcile", "--dir", dir, "--job", "pilot", "--retry-suspected-refusals"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	opts.Create = false
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if got := requestStates(t, db); got != requestCompleted || fakeCalls(t, calls) != 2 {
		t.Fatalf("after retry: %s, %d calls", got, fakeCalls(t, calls))
	}
	// A refusal stop reason stands even when no result follows.
	dir = writeLabelingFixture(t, labelingFixtureUnits())
	cli, _, _ = fakeLabelingCLI(t, "refuse-hang")
	opts = labelingTestOptions(dir, cli, io.Discard)
	opts.Concurrency, opts.Spec.LimitItems, opts.Timeout = 1, 4, 300*time.Millisecond
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	db = openFixtureLedger(t, dir)
	results := itemOutcomes(t, db, "pilot:000001/1")
	if got := requestStates(t, db); got != requestRefused || !strings.Contains(firstString(results["i_a"]["issues"]), "refused_copacked_unresolved") {
		t.Fatalf("states %s, i_a %v", got, results["i_a"])
	}
	// Confirmed refusals are global and permanent: a codex job skips them,
	// and so does a job in a newly prepared dataset that names this one.
	var out strings.Builder
	other := labelingTestOptions(dir, cli, &out)
	other.Job, other.CreateOnly = "codex", true
	other.Spec = labelingJobSpec{Route: labelingRouteCodex, Model: "gpt-6-luna", Effort: "high", LimitItems: 4}
	if err := runLabelingJob(context.Background(), other); err != nil || !strings.Contains(out.String(), `"refused_skipped": 4`) {
		t.Fatalf("codex job = %v: %s", err, out.String())
	}
	fresh := writeLabelingFixture(t, labelingFixtureUnits())
	out.Reset()
	other = labelingTestOptions(fresh, cli, &out)
	other.CreateOnly, other.RefusalsFrom = true, []string{dir}
	other.Spec.LimitItems = 4
	if err := runLabelingJob(context.Background(), other); err != nil || !strings.Contains(out.String(), `"refused_skipped": 4`) {
		t.Fatalf("new dataset = %v: %s", err, out.String())
	}
}

func TestLabelingCodexToolUseWithAnAnswer(t *testing.T) {
	quickLabelingTimers(t)
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ := fakeLabelingCLI(t, "tool-answer-hang")
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Spec = labelingJobSpec{Route: labelingRouteCodex, Model: "gpt-6-luna", Effort: "high", LimitItems: 4}
	opts.Timeout = 500 * time.Millisecond
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	db := openFixtureLedger(t, dir)
	if got := requestStates(t, db); got != requestInvalid || fakeCalls(t, calls) != 1 {
		t.Fatalf("states %s after %d calls", got, fakeCalls(t, calls))
	}
	var out strings.Builder
	if err := runLabelingCLI("", []string{"reconcile", "--dir", dir, "--job", "pilot", "--retry-tool-use"}, &out, io.Discard); err != nil || !strings.Contains(out.String(), "Re-queued 1") {
		t.Fatalf("reconcile = %v: %s", err, out.String())
	}
}

func TestLabelingReleasedHoldRetriesItsGaps(t *testing.T) {
	quickLabelingTimers(t)
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ := fakeLabelingCLI(t, "other-missing,ok")
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Concurrency, opts.Spec.LimitItems = 1, 4
	if err := runLabelingJob(context.Background(), opts); !errors.Is(err, errLabelingHalted) {
		t.Fatalf("run = %v", err)
	}
	opts.Create, opts.AcceptServedModel = false, "claude-haiku-5-5-20261001"
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	db := openFixtureLedger(t, dir)
	retry := labelingRows(t, db, `SELECT state FROM requests WHERE retry_of='pilot:000001'`)
	if got := requestStates(t, db); got != "invalid,completed" || len(retry) != 1 || fakeCalls(t, calls) != 2 {
		t.Fatalf("states %s, retry %v, %d calls", got, retry, fakeCalls(t, calls))
	}
}

func TestLabelingIncompleteUsageHalts(t *testing.T) {
	quickLabelingTimers(t)
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, _, _ := fakeLabelingCLI(t, "total-only")
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Concurrency = 1
	if err := runLabelingJob(context.Background(), opts); !errors.Is(err, errLabelingHalted) {
		t.Fatalf("run = %v", err)
	}
	db := openFixtureLedger(t, dir)
	status, _ := labelingJobStatus(db, "pilot", labelingTestDay)
	usage := labelingRows(t, db, `SELECT cost_status,cost_usd FROM usage`)
	if len(usage) != 1 || usage[0]["cost_status"] != "partial" || usage[0]["cost_usd"] != nil || status["spend"].(labelingSpend).UnknownAttempts != 1 {
		t.Fatalf("usage %v, spend %+v", usage, status["spend"])
	}
	// Re-pricing keeps it partial; covered by exposure, it does not block
	// the next run.
	execTx(t, db, `UPDATE jobs SET pricing_rule=NULL`)
	opts.Create = false
	if err := runLabelingJob(context.Background(), opts); !errors.Is(err, errLabelingHalted) || strings.Contains(err.Error(), "neither priced") {
		t.Fatalf("rerun = %v", err)
	}
	if rows := labelingRows(t, db, `SELECT cost_status FROM usage WHERE attempt_id='pilot:000001/1'`); rows[0]["cost_status"] != "partial" {
		t.Fatalf("re-priced partial = %v", rows)
	}
}

func TestLabelingStopDuringStartupAndGrace(t *testing.T) {
	quickLabelingTimers(t)
	// A stop that arrives while the runner starts is honoured.
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ := fakeLabelingCLI(t, "ok")
	override(t, &labelingStartupHook, func() {
		if err := requestLabelingStop(dir, "pilot"); err != nil {
			t.Error(err)
		}
	})
	opts := labelingTestOptions(dir, cli, io.Discard)
	if err := runLabelingJob(context.Background(), opts); err != nil || fakeCalls(t, calls) != 0 {
		t.Fatalf("run = %v after %d calls", err, fakeCalls(t, calls))
	}
	labelingStartupHook = func() {}
	// After the stop grace, or at a second signal, attempts in flight are
	// terminated and unknown.
	for _, second := range []bool{false, true} {
		dir := writeLabelingFixture(t, labelingFixtureUnits())
		cli, calls, _ := fakeLabelingCLI(t, "hang")
		if !second {
			override(t, &labelingStopGrace, 200*time.Millisecond)
		} else {
			override(t, &labelingStopGrace, time.Minute)
		}
		signals := make(chan os.Signal, 2)
		opts := labelingTestOptions(dir, cli, io.Discard)
		opts.Concurrency, opts.Spec.LimitItems, opts.Signals = 1, 4, signals
		done := make(chan error, 1)
		go func() { done <- runLabelingJob(context.Background(), opts) }()
		for deadline := time.Now().Add(20 * time.Second); fakeCalls(t, calls) < 1; {
			if time.Now().After(deadline) {
				t.Fatal("never started")
			}
			time.Sleep(10 * time.Millisecond)
		}
		signals <- os.Interrupt
		if second {
			// The buffered channel delivers both, in order.
			signals <- os.Interrupt
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("the runner did not terminate its attempt")
		}
		attempt, _ := labelingAttemptRecord(openFixtureLedger(t, dir), "pilot:000001/1")
		if attempt["outcome"] != requestUnknown || !strings.Contains(firstString(attempt["error"]), "stop") {
			t.Fatalf("second %v: attempt %v", second, attempt)
		}
	}
}

func TestLabelingConnectionErrorsAreFailures(t *testing.T) {
	quickLabelingTimers(t)
	for _, route := range []string{labelingRouteClaude, labelingRouteCodex} {
		dir := writeLabelingFixture(t, labelingFixtureUnits())
		cli, calls, _ := fakeLabelingCLI(t, "conn-refused")
		opts := labelingTestOptions(dir, cli, io.Discard)
		opts.Concurrency = 1
		if route == labelingRouteCodex {
			opts.Spec = labelingJobSpec{Route: labelingRouteCodex, Model: "gpt-6-luna", Effort: "high"}
		}
		if err := runLabelingJob(context.Background(), opts); !errors.Is(err, errLabelingHalted) || !strings.Contains(err.Error(), "connection error") || fakeCalls(t, calls) != 1 {
			t.Fatalf("%s: run = %v after %d calls", route, err, fakeCalls(t, calls))
		}
		db := openFixtureLedger(t, dir)
		if got := requestStates(t, db); got != "failed,pending" {
			t.Fatalf("%s: states %s", route, got)
		}
		if rows := labelingRows(t, db, `SELECT COUNT(*) n FROM item_results WHERE outcome='refused'`); rows[0]["n"] != int64(0) {
			t.Fatalf("%s: a connection error became a refusal", route)
		}
	}
	// Refusals in a row halt too.
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ := fakeLabelingCLI(t, "refusal")
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Concurrency, opts.Spec.ItemsPerRequest = 1, 1
	if err := runLabelingJob(context.Background(), opts); !errors.Is(err, errLabelingHalted) || !strings.Contains(err.Error(), "3 consecutive requests were refused") || fakeCalls(t, calls) != 3 {
		t.Fatalf("refusals = %v after %d calls", err, fakeCalls(t, calls))
	}
}

func TestLabelingUnexplainedUsageHalts(t *testing.T) {
	quickLabelingTimers(t)
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, _, _ := fakeLabelingCLI(t, "unexplained")
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Concurrency = 1
	if err := runLabelingJob(context.Background(), opts); !errors.Is(err, errLabelingHalted) || !strings.Contains(err.Error(), "does not attribute") {
		t.Fatalf("run = %v", err)
	}
	db := openFixtureLedger(t, dir)
	attempt, _ := labelingAttemptRecord(db, "pilot:000001/1")
	status, _ := labelingJobStatus(db, "pilot", labelingTestDay)
	// The uncovered difference is bounded as exposure, under its own row.
	spend := status["spend"].(labelingSpend)
	unattributed := spend.PartialTokens["unattributed:claude-haiku-5-5 (estimated_upper_bound)"]
	if !strings.Contains(compactJSON(attempt["problems"]), "usage_unexplained") || spend.EstimatedUsage <= 0 || unattributed["output_tokens"] != 1000 {
		t.Fatalf("attempt %v, spend %+v", attempt, status["spend"])
	}
}

func TestLabelingJobRefusesOversizeItems(t *testing.T) {
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	opts := labelingTestOptions(dir, "unused", io.Discard)
	opts.CreateOnly, opts.Spec.MaxRequestChars = true, 40
	if err := runLabelingJob(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "do not fit one request") || !strings.Contains(err.Error(), "i_a") {
		t.Fatalf("oversize = %v", err)
	}
	if rows := labelingRows(t, openFixtureLedger(t, dir), `SELECT COUNT(*) n FROM jobs`); rows[0]["n"] != int64(0) {
		t.Fatal("a job was created with items it cannot send")
	}
	// --skip-oversize creates it without them, recorded and reported.
	opts.Spec.SkipOversize = true
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	db := openFixtureLedger(t, dir)
	status, _ := labelingJobStatus(db, "pilot", labelingTestDay)
	coverage := status["coverage"].(map[string]any)
	if coverage["blocked_oversize"].(int) == 0 || coverage["incomplete"] != true || !strings.Contains(labelingStatusLine(status), "job incomplete") {
		t.Fatalf("coverage = %v", coverage)
	}
}

func TestLabelingPolicyErrorsAreRefusals(t *testing.T) {
	quickLabelingTimers(t)
	for _, route := range []string{labelingRouteClaude, labelingRouteCodex} {
		dir := writeLabelingFixture(t, labelingFixtureUnits())
		cli, _, _ := fakeLabelingCLI(t, "policy")
		opts := labelingTestOptions(dir, cli, io.Discard)
		opts.Concurrency, opts.Spec.LimitItems = 1, 4
		if route == labelingRouteCodex {
			opts.Spec = labelingJobSpec{Route: labelingRouteCodex, Model: "gpt-6-luna", Effort: "high", LimitItems: 4}
		}
		if err := runLabelingJob(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
		db := openFixtureLedger(t, dir)
		status, _ := labelingJobStatus(db, "pilot", labelingTestDay)
		if got := requestStates(t, db); got != requestRefused || status["coverage"].(map[string]any)["refused_co_packed_blocked"] != 4 ||
			!strings.Contains(labelingStatusLine(status), "co-packed items permanently blocked") {
			t.Fatalf("%s: states %s, coverage %v", route, got, status["coverage"])
		}
	}
}

func TestLabelingRefusalsBlockOtherJobs(t *testing.T) {
	quickLabelingTimers(t)
	for _, scenario := range []string{"refusal", "suspect"} {
		// Two jobs created before either runs: a refusal (or a suspicion) in
		// one keeps the other from sending the same items.
		dir := writeLabelingFixture(t, labelingFixtureUnits())
		first, _, _ := fakeLabelingCLI(t, scenario+",ok")
		a := labelingTestOptions(dir, first, io.Discard)
		a.Job, a.Concurrency, a.Spec.LimitItems = "a", 1, 4
		b := labelingTestOptions(dir, "unused", io.Discard)
		b.Job, b.CreateOnly = "b", true
		b.Spec = labelingJobSpec{Route: labelingRouteCodex, Model: "gpt-6-luna", Effort: "high", LimitItems: 4}
		if err := runLabelingJob(context.Background(), b); err != nil {
			t.Fatal(err)
		}
		if err := runLabelingJob(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		second, calls, _ := fakeLabelingCLI(t, "ok")
		b.Create, b.CreateOnly, b.CLIPath = false, false, second
		if err := runLabelingJob(context.Background(), b); err != nil {
			t.Fatal(err)
		}
		db := openFixtureLedger(t, dir)
		states := labelingRows(t, db, `SELECT state FROM requests WHERE job='b'`)
		blocked := labelingRows(t, db, `SELECT COUNT(*) n FROM item_results WHERE job='b' AND outcome='refused_elsewhere'`)
		if len(states) != 1 || states[0]["state"] != requestRefusedElsewhere || blocked[0]["n"] != int64(4) || fakeCalls(t, calls) != 0 {
			t.Fatalf("%s: job b states %v, blocked %v, %d calls", scenario, states, blocked, fakeCalls(t, calls))
		}
		if scenario == "suspect" {
			// Only the suspecting job re-sends them, on request.
			if err := runLabelingCLI("", []string{"reconcile", "--dir", dir, "--job", "a", "--retry-suspected-refusals"}, io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			a.Create = false
			if err := runLabelingJob(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			if got := labelingRows(t, db, `SELECT state FROM requests WHERE job='a'`); got[0]["state"] != requestCompleted {
				t.Fatalf("job a after retry = %v", got)
			}
		}
	}
	// A re-queue by reconcile drops items refused elsewhere meanwhile.
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	hang, _, _ := fakeLabelingCLI(t, "hang")
	b := labelingTestOptions(dir, hang, io.Discard)
	b.Job, b.Concurrency, b.Timeout = "b", 1, 300*time.Millisecond
	b.Spec = labelingJobSpec{Route: labelingRouteCodex, Model: "gpt-6-luna", Effort: "high", LimitItems: 4}
	if err := runLabelingJob(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	refuse, _, _ := fakeLabelingCLI(t, "refusal")
	a := labelingTestOptions(dir, refuse, io.Discard)
	a.Job, a.Concurrency, a.Spec.LimitItems = "a", 1, 4
	if err := runLabelingJob(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := runLabelingCLI("", []string{"reconcile", "--dir", dir, "--job", "b", "--retry-unknown"}, &out, io.Discard); err != nil || !strings.Contains(out.String(), "4 of their items are blocked") {
		t.Fatalf("reconcile = %v: %s", err, out.String())
	}
	if got := labelingRows(t, openFixtureLedger(t, dir), `SELECT state FROM requests WHERE job='b'`); got[0]["state"] != requestRefusedElsewhere {
		t.Fatalf("job b = %v", got)
	}
}

func TestLabelingRefusalsFollowTheSplitsLineage(t *testing.T) {
	quickLabelingTimers(t)
	old := writeLabelingFixture(t, labelingFixtureUnits())
	cli, _, _ := fakeLabelingCLI(t, "refusal")
	opts := labelingTestOptions(old, cli, io.Discard)
	opts.Concurrency, opts.Spec.LimitItems = 1, 4
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	// A re-prepared dataset that kept the old splits (--splits-from).
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	manifest := map[string]any{"contracts": map[string]any{"units": labelingUnitsVersion}, "splits": map[string]any{"inherited_from": old}}
	if err := durableJSON(filepath.Join(dir, labelingManifestFile), manifest); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	next := labelingTestOptions(dir, cli, &out)
	next.CreateOnly = true
	next.Spec = labelingJobSpec{Route: labelingRouteCodex, Model: "gpt-6-luna", Effort: "high", LimitItems: 4}
	if err := runLabelingJob(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	hash, _ := labelingManifestHash(old)
	if !strings.Contains(out.String(), `"refused_skipped": 4`) || !strings.Contains(out.String(), old) || !strings.Contains(out.String(), hash) {
		t.Fatalf("lineage job: %s", out.String())
	}
}
