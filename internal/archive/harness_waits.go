package archive

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Harness waits are the stretches a harness feature keeps work going after
// its tool call has returned: a background command, a Monitor watch, or a
// scheduled wakeup. The call's own result is only the launch
// acknowledgement, so its ended_at and duration_ms stay the launch. The wait
// is recorded beside them (wait_kind, wait_ended_at, wait_status,
// wait_end_source, wait_events), from the evidence the harness leaves:
//
//   - Claude Code background commands (Bash run_in_background): the launch
//     result names a task ID; a <task-notification> naming the call's
//     tool-use ID or the task ID reports its status when it ends. TaskStop
//     (or KillShell) ends it early. A notice that the command "didn't finish
//     before the previous session ended" means its end is unknown.
//   - Monitor: the launch result names a task ID and a timeout. Each event is
//     a notification without a status; a completion notice, an expiry event,
//     TaskStop or the timeout ends the watch.
//   - ScheduleWakeup: the result names the time it fires. A later
//     ScheduleWakeup in the same stream replaces it and stop cancels it.
//
// An end the transcript does not show (the session ended first) is left
// unknown rather than guessed.
const (
	waitBackgroundCommand = "background_command"
	waitMonitor           = "monitor"
	waitWakeup            = "wakeup"
)

var (
	backgroundLaunchPattern = regexp.MustCompile(`(?i)running in background with ID: ?([A-Za-z0-9_-]+)`)
	monitorLaunchPattern    = regexp.MustCompile(`(?i)Monitor started \(task ([A-Za-z0-9_-]+)`)
	notificationTaskPattern = regexp.MustCompile(`<task-id>([^<]+)</task-id>`)
	notificationToolPattern = regexp.MustCompile(`<tool-use-id>([^<]+)</tool-use-id>`)
	notificationStatus      = regexp.MustCompile(`<status>([^<]+)</status>`)
	notificationSummary     = regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)
	notificationEvent       = regexp.MustCompile(`(?s)<event>(.*?)</event>`)
)

// harnessWait is what a call's launch says about the wait it starts.
type harnessWait struct {
	task       string
	timeout    int64 // Monitor: ms after the launch it expires; 0 when persistent or unknown
	planned    string
	stopTask   string // TaskStop / KillShell: the task it stops
	stopWakeup bool   // ScheduleWakeup {stop: true}
}

// detectHarnessWait reads the wait a call starts (or stops) from its input
// and launch result. It sets WaitKind; the end is resolved later from the
// rest of the conversation (resolveHarnessWaits).
func detectHarnessWait(call *toolCall, input any, details map[string]any, content string) {
	name := strings.ToLower(call.ToolName)
	fields := mapValueDefault(input)
	failed := call.Status == "error"
	switch {
	case name == "bash" && fields["run_in_background"] == true && !failed:
		task := firstString(details["backgroundTaskId"])
		if task == "" {
			if match := backgroundLaunchPattern.FindStringSubmatch(content); match != nil {
				task = match[1]
			}
		}
		if task != "" {
			call.WaitKind, call.wait = waitBackgroundCommand, harnessWait{task: task}
		}
	case name == "monitor" && !failed:
		task := firstString(details["taskId"])
		if task == "" {
			if match := monitorLaunchPattern.FindStringSubmatch(content); match != nil {
				task = match[1]
			}
		}
		if task == "" {
			return
		}
		wait := harnessWait{task: task}
		if details["persistent"] != true && fields["persistent"] != true {
			if timeout, ok := number(details["timeoutMs"]); ok && timeout > 0 {
				wait.timeout = int64(timeout)
			} else if timeout, ok := number(fields["timeout_ms"]); ok && timeout > 0 {
				wait.timeout = int64(timeout)
			}
		}
		call.WaitKind, call.wait = waitMonitor, wait
	case name == "schedulewakeup" && !failed:
		if fields["stop"] == true || details["stopped"] == true {
			call.wait.stopWakeup = true
			return
		}
		if at, ok := number(details["scheduledFor"]); ok && at > 0 {
			call.WaitKind, call.wait = waitWakeup, harnessWait{planned: canonicalTime(at)}
		}
	case (name == "taskstop" || name == "killshell" || name == "killbash") && !failed:
		call.wait.stopTask = firstString(fields["task_id"], fields["shell_id"], fields["bash_id"])
	}
}

// taskNotification is one harness notice about background tasks.
type taskNotification struct {
	tasks         []string
	toolUse       string
	status, event string
	summary       string
	orphaned      bool
	expired       bool
	at            time.Time
}

func parseTaskNotification(message MessageRecord) (taskNotification, bool) {
	if message.Kind != contextKind {
		return taskNotification{}, false
	}
	var envelope struct {
		Source string `json:"source"`
		Text   string `json:"text"`
	}
	if json.Unmarshal([]byte(message.Text), &envelope) != nil || envelope.Source != "task_notification" {
		return taskNotification{}, false
	}
	at, ok := parseTime(message.CreatedAt)
	if !ok {
		return taskNotification{}, false
	}
	notice := taskNotification{at: at}
	for _, match := range notificationTaskPattern.FindAllStringSubmatch(envelope.Text, -1) {
		if task := strings.TrimSpace(match[1]); task != "" && !strings.HasPrefix(task, "__") {
			notice.tasks = append(notice.tasks, task)
		}
	}
	if match := notificationToolPattern.FindStringSubmatch(envelope.Text); match != nil {
		notice.toolUse = strings.TrimSpace(match[1])
	}
	if match := notificationStatus.FindStringSubmatch(envelope.Text); match != nil {
		notice.status = strings.ToLower(strings.TrimSpace(match[1]))
	}
	if match := notificationSummary.FindStringSubmatch(envelope.Text); match != nil {
		notice.summary = strings.TrimSpace(match[1])
	}
	if match := notificationEvent.FindStringSubmatch(envelope.Text); match != nil {
		notice.event = strings.TrimSpace(match[1])
	}
	notice.orphaned = strings.Contains(notice.summary, "didn't finish before the previous session ended") || strings.Contains(envelope.Text, "No completion record was found")
	notice.expired = notice.status == "" && strings.HasPrefix(notice.event, "[Monitor expired")
	return notice, len(notice.tasks) > 0 || notice.toolUse != ""
}

// resolveHarnessWaits ends each wait from the notifications, stops and
// replacements that follow its launch.
func resolveHarnessWaits(messages []MessageRecord, calls []toolCall) {
	byTask, byToolUse := map[string]int{}, map[string]int{}
	found := false
	for i := range calls {
		if calls[i].WaitKind == "" {
			continue
		}
		found = true
		if calls[i].wait.task != "" {
			byTask[calls[i].wait.task] = i
		}
		if calls[i].CallID != "" {
			byToolUse[calls[i].CallID] = i
		}
	}
	if !found {
		return
	}
	last := time.Time{}
	for _, message := range messages {
		if at, ok := parseTime(message.CreatedAt); ok && at.After(last) {
			last = at
		}
	}
	started := func(call *toolCall) (time.Time, bool) { return parseTime(call.StartedAt) }
	end := func(call *toolCall, at time.Time, status, source string) {
		if call.WaitEndSource != "" {
			return
		}
		if start, ok := started(call); ok && at.Before(start) {
			at = start
		}
		call.WaitEndedAt, call.WaitStatus, call.WaitEndSource = formatTime(at), status, source
	}
	unknown := func(call *toolCall, status, source string) {
		if call.WaitEndSource == "" {
			call.WaitStatus, call.WaitEndSource = status, source
		}
	}
	// Notifications and stops in transcript order.
	type step struct {
		index  int
		notice *taskNotification
		stop   *toolCall
	}
	steps := []step{}
	for index, message := range messages {
		if notice, ok := parseTaskNotification(message); ok {
			steps = append(steps, step{index: index, notice: &notice})
		}
	}
	for i := range calls {
		if calls[i].wait.stopTask != "" {
			steps = append(steps, step{index: calls[i].messageIndex, stop: &calls[i]})
		}
	}
	sort.SliceStable(steps, func(a, b int) bool { return steps[a].index < steps[b].index })
	for _, s := range steps {
		if s.stop != nil {
			target, ok := byTask[s.stop.wait.stopTask]
			if !ok || calls[target].messageIndex > s.index {
				continue
			}
			if at, ok := parseTime(s.stop.StartedAt); ok {
				end(&calls[target], at, "killed", "stop")
			}
			continue
		}
		notice := s.notice
		targets := []int{}
		if target, ok := byToolUse[notice.toolUse]; ok && notice.toolUse != "" {
			targets = append(targets, target)
		} else {
			for _, task := range notice.tasks {
				if target, ok := byTask[task]; ok {
					targets = append(targets, target)
				}
			}
		}
		for _, target := range targets {
			call := &calls[target]
			if calls[target].messageIndex > s.index || call.WaitEndSource != "" {
				continue
			}
			switch {
			case notice.orphaned:
				unknown(call, "stopped", "session_end")
			case notice.status != "":
				end(call, notice.at, notice.status, "notification")
			case notice.expired:
				at := notice.at
				if start, ok := started(call); ok && call.wait.timeout > 0 {
					if planned := start.Add(time.Duration(call.wait.timeout) * time.Millisecond); planned.Before(at) {
						at = planned
					}
				}
				end(call, at, "expired", "expiry")
			case notice.event != "":
				call.WaitEvents++
			}
		}
	}
	// A wakeup fires at its planned time unless the stream replaced or
	// stopped it first; a watch that saw no end expires at its timeout.
	for i := range calls {
		call := &calls[i]
		switch call.WaitKind {
		case waitWakeup:
			planned, ok := parseTime(call.wait.planned)
			if !ok {
				continue
			}
			for j := i + 1; j < len(calls); j++ {
				next := &calls[j]
				if next.Stream != call.Stream || (next.WaitKind != waitWakeup && !next.wait.stopWakeup) {
					continue
				}
				if at, ok := parseTime(next.StartedAt); ok && at.Before(planned) {
					status := "replaced"
					if next.wait.stopWakeup {
						status = "cancelled"
					}
					end(call, at, status, "superseded")
				}
				break
			}
			if !planned.After(last) {
				end(call, planned, "fired", "scheduled")
			} else {
				unknown(call, "pending", "")
			}
		case waitMonitor:
			start, ok := started(call)
			if ok && call.wait.timeout > 0 {
				if planned := start.Add(time.Duration(call.wait.timeout) * time.Millisecond); !planned.After(last) {
					end(call, planned, "expired", "timeout")
				}
			}
		}
	}
}

// harnessWaitAttacher adds each tool call message's recorded wait, as
// harness_wait {kind, ended_at, status, events}, so the conversation reader's
// timeline draws a wait to the same end as the Timeline does.
func (c *Catalog) harnessWaitAttacher(ctx context.Context, conversationID string) (func(map[string]any), error) {
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT call_message_id,call_id,wait_kind,wait_ended_at,wait_status,wait_events FROM tool_calls
		WHERE conversation_id=? AND wait_kind IS NOT NULL AND call_message_id IS NOT NULL`, conversationID)
	if err != nil || len(rows) == 0 {
		return func(map[string]any) {}, err
	}
	waits := map[string]map[string]any{}
	for _, row := range rows {
		wait := map[string]any{"kind": row["wait_kind"], "ended_at": row["wait_ended_at"], "status": row["wait_status"], "events": integer(row["wait_events"])}
		waits[firstString(row["call_message_id"])+"\x1f"+firstString(row["call_id"])] = wait
	}
	return func(message map[string]any) {
		if firstString(message["kind"]) != "tool_call" {
			return
		}
		if wait := waits[firstString(message["id"])+"\x1f"+firstString(message["call_id"])]; wait != nil {
			message["harness_wait"] = wait
		}
	}, nil
}
