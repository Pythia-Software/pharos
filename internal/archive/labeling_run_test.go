package archive

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
		case "missing":
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
		if scenario == "tool" {
			emit(map[string]any{"type": "item.completed", "item": map[string]any{"id": "item_1", "type": "command_execution", "command": "ls", "status": "completed"}})
		}
		emit(map[string]any{"type": "item.completed", "item": map[string]any{"id": "item_2", "type": "agent_message", "text": string(answer)}})
		os.WriteFile(flag("--output-last-message"), answer, 0o600)
		emit(map[string]any{"type": "turn.completed", "usage": map[string]any{"input_tokens": 17000, "cached_input_tokens": 1000, "output_tokens": 900, "reasoning_output_tokens": 400}})
		return 0
	}
	model := flag("--model")
	usage := map[string]any{"input_tokens": 2, "cache_creation_input_tokens": 15000, "cache_read_input_tokens": 0, "output_tokens": 1000,
		"cache_creation": map[string]any{"ephemeral_1h_input_tokens": 15000, "ephemeral_5m_input_tokens": 0}}
	modelUsage := map[string]any{model: map[string]any{"inputTokens": 2, "outputTokens": 1000, "cacheReadInputTokens": 0, "cacheCreationInputTokens": 15000, "costUSD": 0.01}}
	if scenario == "unpriced" {
		modelUsage["claude-mystery-9"] = map[string]any{"inputTokens": 300, "outputTokens": 20, "cacheReadInputTokens": 0, "cacheCreationInputTokens": 0}
	}
	emit(map[string]any{"type": "system", "subtype": "init", "session_id": session, "model": model, "tools": []any{"StructuredOutput"}, "cwd": "/private/secret"})
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
	case "failed":
		result["subtype"], result["is_error"] = "error_during_execution", true
		delete(result, "structured_output")
	default:
		content = append(content, map[string]any{"type": "tool_use", "id": "tool-2", "name": "StructuredOutput", "input": output})
	}
	emit(map[string]any{"type": "assistant", "session_id": session, "message": map[string]any{"model": model, "content": content, "usage": usage}})
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
	excerpt                                          bool
	segments                                         []fixtureSegment
	chunks                                           []int
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
			unitID, "m_"+unit.key, "c_1", "w_1", "resolved", unit.cohort, "agent", unit.sender, "claude", "message", unit.channel,
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
		integer(attempt["duration_ms"]) <= 0 || compactJSON(attempt["cleanup"]) != "[]" {
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
		{"missing,ok", "invalid,completed,completed", 1, 3, func(t *testing.T, db *sql.DB) {
			if got := itemOutcomes(t, db, "pilot:000001/1")["i_d0"]["outcome"]; got != itemMissing {
				t.Fatalf("i_d0 = %v", got)
			}
			if rows := labelingRows(t, db, `SELECT r.retry_of,i.item_id FROM requests r JOIN request_items i ON i.request_id=r.id WHERE r.position=3`); len(rows) != 1 || rows[0]["item_id"] != "i_d0" {
				t.Fatalf("retry = %v", rows)
			}
		}},
		// Extra and duplicate ids break the request; the duplicated item is invalid.
		{"extra", "invalid,invalid", 0, 2, func(t *testing.T, db *sql.DB) {
			attempt, _ := labelingAttemptRecord(db, "pilot:000001/1")
			problems := compactJSON(attempt["problems"])
			if !strings.Contains(problems, "extra_id:ghost") || !strings.Contains(problems, "duplicate_id:i_a") {
				t.Fatalf("problems = %s", problems)
			}
			results := itemOutcomes(t, db, "pilot:000001/1")
			if results["i_a"]["outcome"] != itemInvalid || results["i_b"]["outcome"] != itemUsable {
				t.Fatalf("results = %v", results)
			}
		}},
		// Quotes from a marker or the context are not evidence.
		{"context-quote", "completed,completed", 1, 2, func(t *testing.T, db *sql.DB) {
			results := itemOutcomes(t, db, "pilot:000001/1")
			for id, want := range map[string]string{"i_a": "in_marker", "i_b": "in_context"} {
				evidence := results[id]["evidence"].([]labelingEvidence)
				if results[id]["outcome"] != itemEvidenceIssues || evidence[0].Status != want || !strings.Contains(firstString(results[id]["issues"]), "label_without_valid_evidence") {
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
		// Refusals are kept and never retried.
		{"refusal", "refused,refused", 1, 2, func(t *testing.T, db *sql.DB) {
			if got := itemOutcomes(t, db, "pilot:000001/1")["i_a"]["outcome"]; got != itemRefused {
				t.Fatalf("i_a = %v", got)
			}
		}},
		// Any tool use invalidates the attempt; its items are retried once.
		{"tool", "invalid,invalid,invalid,invalid", 1, 4, func(t *testing.T, db *sql.DB) {
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
	// A hang ends unknown at the timeout, as does a crash before any output;
	// neither is re-sent until reconcile re-queues it explicitly.
	for _, scenario := range []string{"hang", "crash-before"} {
		dir := writeLabelingFixture(t, labelingFixtureUnits())
		cli, calls, _ := fakeLabelingCLI(t, scenario+",ok")
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
		if got := requestStates(t, db); got != requestUnknown || fakeCalls(t, calls) != 1 {
			t.Fatalf("%s: states %s after %d calls", scenario, got, fakeCalls(t, calls))
		}
		status, err := labelingJobStatus(db, "pilot", labelingTestDay)
		if err != nil {
			t.Fatal(err)
		}
		if spend := status["spend"].(labelingSpend); spend.UnknownAttempts != 1 || spend.UnknownExposure <= 0 {
			t.Fatalf("%s: spend = %+v", scenario, spend)
		}
		var out strings.Builder
		if err := runLabelingCLI("", []string{"reconcile", "--dir", dir, "--job", "pilot"}, &out, io.Discard); err != nil || !strings.Contains(out.String(), "pilot:000001 unknown") {
			t.Fatalf("reconcile = %v: %s", err, out.String())
		}
		if err := runLabelingCLI("", []string{"reconcile", "--dir", dir, "--job", "pilot", "--retry-unknown"}, &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		opts.Timeout = 20 * time.Second
		if err := runLabelingJob(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
		status, _ = labelingJobStatus(db, "pilot", labelingTestDay)
		if got := requestStates(t, db); got != requestCompleted || fakeCalls(t, calls) != 2 || status["spend"].(labelingSpend).UnknownAttempts != 1 {
			t.Fatalf("%s after retry: states %s, %d calls, spend %+v", scenario, got, fakeCalls(t, calls), status["spend"])
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
	runLabelingJob(context.Background(), opts)
}

func TestLabelingRunResumesFromTheSavedStream(t *testing.T) {
	quickLabelingTimers(t)
	dir := writeLabelingFixture(t, labelingFixtureUnits())
	cli, calls, _ := fakeLabelingCLI(t, "crash-after")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runner := exec.Command(binary, "-test.run=^TestLabelingHelperRunner$")
	runner.Env = append(os.Environ(), testProcessEnv+"=1", "PHAROS_LABELING_HELPER_DIR="+dir, "PHAROS_LABELING_HELPER_CLI="+cli)
	if err := runner.Start(); err != nil {
		t.Fatal(err)
	}
	// Once the result is in the saved stream, the runner dies.
	events := filepath.Join(dir, "attempts", "pilot", "000001", "1", "events.jsonl")
	deadline := time.Now().Add(20 * time.Second)
	for {
		data, _ := os.ReadFile(events)
		if strings.Contains(string(data), `"type":"result"`) {
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
	opts := labelingTestOptions(dir, cli, io.Discard)
	opts.Create = false
	if err := runLabelingJob(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	attempt, _ = labelingAttemptRecord(db, "pilot:000001/1")
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
	if requestStates(t, db) != "completed,completed" || spend.UnpricedAttempts != 2 || spend.PartialTokens["claude-mystery-9 (unpriced)"]["output_tokens"] != 40 {
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
