package archive

import (
	"slices"
	"testing"
)

// One Claude Code session that leaves each kind of harness wait: background
// commands that complete, are stopped, or outlive the session; a Monitor
// watch with events that expires; and wakeups that fire or are replaced.
var harnessWaitFixture = []string{
	`{"type":"user","uuid":"u1","sessionId":"waits","timestamp":"2026-11-13T18:00:00.000Z","message":{"role":"user","content":"run the panel"}}`,
	// A background command, launched at 18:01, completing at 18:44.
	`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"waits","timestamp":"2026-11-13T18:01:00.000Z","message":{"id":"msg_1","role":"assistant","model":"claude-opus-5","content":[{"type":"tool_use","id":"toolu_wait","name":"Bash","input":{"command":"until ! pgrep panel; do sleep 30; done","description":"Wait for the panel","run_in_background":true}}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
	`{"type":"user","uuid":"r1","parentUuid":"a1","sessionId":"waits","timestamp":"2026-11-13T18:01:03.000Z","message":{"role":"user","content":[{"tool_use_id":"toolu_wait","type":"tool_result","content":"Command running in background with ID: btask1. Output is being written to: /tmp/btask1.output","is_error":false}]},"toolUseResult":{"stdout":"","stderr":"","interrupted":false,"backgroundTaskId":"btask1"}}`,
	// A second background command the agent stops at 18:10.
	`{"type":"assistant","uuid":"a2","parentUuid":"r1","sessionId":"waits","timestamp":"2026-11-13T18:02:00.000Z","message":{"id":"msg_2","role":"assistant","model":"claude-opus-5","content":[{"type":"tool_use","id":"toolu_server","name":"Bash","input":{"command":"npm run dev","run_in_background":true}}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
	`{"type":"user","uuid":"r2","parentUuid":"a2","sessionId":"waits","timestamp":"2026-11-13T18:02:01.000Z","message":{"role":"user","content":[{"tool_use_id":"toolu_server","type":"tool_result","content":"Command running in background with ID: btask2.","is_error":false}]},"toolUseResult":{"backgroundTaskId":"btask2"}}`,
	// A Monitor with a 30-minute timeout and one event, then its expiry notice.
	`{"type":"assistant","uuid":"a3","parentUuid":"r2","sessionId":"waits","timestamp":"2026-11-13T18:03:00.000Z","message":{"id":"msg_3","role":"assistant","model":"claude-opus-5","content":[{"type":"tool_use","id":"toolu_monitor","name":"Monitor","input":{"command":"tail -f log","description":"log watch","timeout_ms":1800000}}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
	`{"type":"user","uuid":"r3","parentUuid":"a3","sessionId":"waits","timestamp":"2026-11-13T18:03:01.000Z","message":{"role":"user","content":[{"tool_use_id":"toolu_monitor","type":"tool_result","content":"Monitor started (task bmon1, expires in 30m unless the source ends first)","is_error":false}]},"toolUseResult":{"taskId":"bmon1","timeoutMs":1800000,"persistent":false}}`,
	`{"type":"assistant","uuid":"a4","parentUuid":"r3","sessionId":"waits","timestamp":"2026-11-13T18:10:00.000Z","message":{"id":"msg_4","role":"assistant","model":"claude-opus-5","content":[{"type":"tool_use","id":"toolu_stop","name":"TaskStop","input":{"task_id":"btask2"}}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
	`{"type":"user","uuid":"r4","parentUuid":"a4","sessionId":"waits","timestamp":"2026-11-13T18:10:01.000Z","message":{"role":"user","content":[{"tool_use_id":"toolu_stop","type":"tool_result","content":"Successfully stopped task: btask2","is_error":false}]}}`,
	// An event delivered while the agent is mid-turn is a queued command.
	`{"type":"attachment","uuid":"n1","parentUuid":"r4","sessionId":"waits","timestamp":"2026-11-13T18:12:00.000Z","attachment":{"type":"queued_command","prompt":"<task-notification>\n<task-id>bmon1</task-id>\n<summary>Monitor event: \"log watch\"</summary>\n<event>build done</event>\n</task-notification>","commandMode":"task-notification"}}`,
	`{"type":"user","uuid":"n2","parentUuid":"n1","sessionId":"waits","timestamp":"2026-11-13T18:33:05.000Z","origin":{"kind":"task-notification"},"message":{"role":"user","content":"<task-notification>\n<task-id>bmon1</task-id>\n<summary>Monitor event: \"log watch\"</summary>\n<event>[Monitor expired after 30m with no events delivered.]</event>\n</task-notification>"}}`,
	// The first command completes.
	`{"type":"user","uuid":"n3","parentUuid":"n2","sessionId":"waits","timestamp":"2026-11-13T18:44:00.000Z","origin":{"kind":"task-notification"},"message":{"role":"user","content":"<task-notification>\n<task-id>btask1</task-id>\n<tool-use-id>toolu_wait</tool-use-id>\n<status>completed</status>\n<summary>Background command \"Wait for the panel\" completed (exit code 0)</summary>\n</task-notification>"}}`,
	// A wakeup replaced by another before it fires; the second fires at 19:20.
	`{"type":"assistant","uuid":"a5","parentUuid":"n3","sessionId":"waits","timestamp":"2026-11-13T18:45:00.000Z","message":{"id":"msg_5","role":"assistant","model":"claude-opus-5","content":[{"type":"tool_use","id":"toolu_wake1","name":"ScheduleWakeup","input":{"delaySeconds":1800,"prompt":"check"}}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
	`{"type":"user","uuid":"r5","parentUuid":"a5","sessionId":"waits","timestamp":"2026-11-13T18:45:01.000Z","message":{"role":"user","content":[{"tool_use_id":"toolu_wake1","type":"tool_result","content":"Next wakeup scheduled","is_error":false}]},"toolUseResult":{"scheduledFor":1794597300000,"clampedDelaySeconds":1800,"wasClamped":false}}`,
	`{"type":"assistant","uuid":"a6","parentUuid":"r5","sessionId":"waits","timestamp":"2026-11-13T18:50:00.000Z","message":{"id":"msg_6","role":"assistant","model":"claude-opus-5","content":[{"type":"tool_use","id":"toolu_wake2","name":"ScheduleWakeup","input":{"delaySeconds":1800,"prompt":"check"}}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
	`{"type":"user","uuid":"r6","parentUuid":"a6","sessionId":"waits","timestamp":"2026-11-13T18:50:01.000Z","message":{"role":"user","content":[{"tool_use_id":"toolu_wake2","type":"tool_result","content":"Next wakeup scheduled","is_error":false}]},"toolUseResult":{"scheduledFor":1794597600000,"clampedDelaySeconds":1800,"wasClamped":false}}`,
	`{"type":"user","uuid":"u2","parentUuid":"r6","sessionId":"waits","timestamp":"2026-11-13T19:20:00.500Z","turnOrigin":"scheduled","message":{"role":"user","content":"check"}}`,
	// A command still running when the session ends.
	`{"type":"assistant","uuid":"a7","parentUuid":"u2","sessionId":"waits","timestamp":"2026-11-13T19:21:00.000Z","message":{"id":"msg_7","role":"assistant","model":"claude-opus-5","content":[{"type":"tool_use","id":"toolu_late","name":"Bash","input":{"command":"sleep 9999","run_in_background":true}}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
	`{"type":"user","uuid":"r7","parentUuid":"a7","sessionId":"waits","timestamp":"2026-11-13T19:21:01.000Z","message":{"role":"user","content":[{"tool_use_id":"toolu_late","type":"tool_result","content":"Command running in background with ID: btask3.","is_error":false}]},"toolUseResult":{"backgroundTaskId":"btask3"}}`,
}

func TestHarnessWaitsRunToTheNoticeThatEndsThem(t *testing.T) {
	catalog, _ := testCatalog(t)
	root := t.TempDir()
	writeFixture(t, root, "project/waits.jsonl", harnessWaitFixture)
	ingestFixture(t, catalog, "claude", root)
	read := func() map[string]map[string]any {
		rows, err := queryMaps(catalog.DB, `SELECT call_id,tool_name,started_at,ended_at,duration_ms,wait_kind,wait_ended_at,wait_status,wait_end_source,wait_events FROM tool_calls`)
		if err != nil {
			t.Fatal(err)
		}
		byCall := map[string]map[string]any{}
		for _, row := range rows {
			byCall[firstString(row["call_id"])] = row
		}
		return byCall
	}
	calls := read()
	check := func(id, kind, end, status, source string, events int64) {
		t.Helper()
		row := calls[id]
		if row == nil {
			t.Fatalf("no call %s in %v", id, calls)
		}
		if firstString(row["wait_kind"]) != kind || firstString(row["wait_ended_at"]) != end || firstString(row["wait_status"]) != status || firstString(row["wait_end_source"]) != source || integer(row["wait_events"]) != events {
			t.Fatalf("%s: %v", id, row)
		}
	}
	check("toolu_wait", waitBackgroundCommand, "2026-11-13T18:44:00.000Z", "completed", "notification", 0)
	// The launch keeps its own end and duration: the acknowledgement.
	if row := calls["toolu_wait"]; firstString(row["ended_at"]) != "2026-11-13T18:01:03.000Z" || integer(row["duration_ms"]) != 3000 {
		t.Fatalf("the launch's own timing changed: %v", row)
	}
	check("toolu_server", waitBackgroundCommand, "2026-11-13T18:10:00.000Z", "killed", "stop", 0)
	// Expired at its timeout, not at the later notice; one event on the way.
	check("toolu_monitor", waitMonitor, "2026-11-13T18:33:00.000Z", "expired", "expiry", 1)
	check("toolu_wake1", waitWakeup, "2026-11-13T18:50:00.000Z", "replaced", "superseded", 0)
	check("toolu_wake2", waitWakeup, "2026-11-13T19:20:00.000Z", "fired", "scheduled", 0)
	check("toolu_late", waitBackgroundCommand, "", "", "", 0)
	check("toolu_stop", "", "", "", "", 0)

	// The conversation reader gets each call's wait on its message.
	conversations, err := queryMaps(catalog.DB, `SELECT id,workspace_id FROM conversations`)
	if err != nil || len(conversations) != 1 {
		t.Fatal(conversations, err)
	}
	detail, err := catalog.WorkConversationContext(t.Context(), firstString(conversations[0]["workspace_id"]), firstString(conversations[0]["id"]))
	if err != nil {
		t.Fatal(err)
	}
	waits := map[string]map[string]any{}
	for _, message := range detail["messages"].([]map[string]any) {
		if wait, ok := message["harness_wait"].(map[string]any); ok {
			waits[firstString(message["call_id"])] = wait
		}
	}
	if wait := waits["toolu_monitor"]; len(waits) != 6 || wait["kind"] != waitMonitor || wait["ended_at"] != "2026-11-13T18:33:00.000Z" || wait["status"] != "expired" || wait["events"] != int64(1) {
		t.Fatalf("reader waits: %v", waits)
	}

	// The session goes on in a later read: the old session's command is
	// reported unfinished, so its end stays unknown, and a new command's
	// completion is attached to its launch.
	writeFixture(t, root, "project/waits.jsonl", append(slices.Clone(harnessWaitFixture),
		`{"type":"user","uuid":"n4","parentUuid":"r7","sessionId":"waits","timestamp":"2026-11-13T20:00:00.000Z","origin":{"kind":"task-notification"},"message":{"role":"user","content":"<task-notification>\n<task-id>btask3</task-id>\n<task-id>__orphan_summary__:shell</task-id>\n<status>stopped</status>\n<summary>Background shell command didn't finish before the previous session ended</summary>\n</task-notification>"}}`,
	))
	ingestFixture(t, catalog, "claude", root)
	calls = read()
	check("toolu_late", waitBackgroundCommand, "", "stopped", "session_end", 0)
	check("toolu_wait", waitBackgroundCommand, "2026-11-13T18:44:00.000Z", "completed", "notification", 0)
}
