package archive

import "time"

// Timing uses recorded intervals only. In particular, an active attempt is
// unknown rather than a zero-length run, even if TL1 supplied duration_ms.
func tl1Seconds(start, end string) any {
	a, aok := parseTime(start)
	b, bok := parseTime(end)
	if !aok || !bok || b.Before(a) {
		return nil
	}
	return b.Sub(a).Seconds()
}

func tl1AttemptSeconds(attempt *tl1Attempt) any {
	return tl1Seconds(attempt.StartedAt, attempt.CompletedAt)
}

// Normalize the first claim once when loading, so large histories do not scan
// all events for every task/attempt row. The stored source row stays untouched.
func tl1ApplyFirstClaims(data *tl1Data) {
	for _, event := range data.Events {
		if !matchesFold(firstString(event["event_type"]), "claimed", "task_claimed") {
			continue
		}
		task := data.TaskByID[firstString(event["task_id"])]
		if task == nil {
			continue
		}
		at := firstString(event["created_at"])
		created, createdOK := parseTime(task.CreatedAt)
		if t, ok := parseTime(at); ok && (!createdOK || !t.Before(created)) {
			if prior, ok := parseTime(task.ClaimedAt); !ok || t.Before(prior) {
				task.ClaimedAt = at
			}
		}
	}
}

func tl1TaskTiming(task *tl1Task) map[string]any {
	total, known, missing := 0.0, 0, 0
	for _, attempt := range task.Attempts {
		if seconds, ok := tl1AttemptSeconds(attempt).(float64); ok {
			total += seconds
			known++
		} else {
			missing++
		}
	}
	return map[string]any{
		"queue_wait_seconds":    tl1Seconds(task.CreatedAt, task.ClaimedAt),
		"attempt_seconds":       tl1Optional(total, known > 0 && missing == 0),
		"known_attempt_seconds": tl1Optional(total, known > 0), "attempts_missing_endpoints": missing,
		"wall_seconds":         tl1Seconds(task.CreatedAt, task.CompletedAt),
		"attempt_time_measure": "LLM time for LLM tasks; sum of recorded attempt end minus start, including retries and tools",
	}
}

// A set's makespan runs from first enqueue to last task completion. Missing
// task or attempt endpoints invalidate the corresponding total, with measured
// subtotals exposed separately. Capacity is deliberately not inferred from
// observed concurrency or a flavor's max_parallelism.
func tl1TimingSummary(tasks []*tl1Task) map[string]any {
	var first, last, firstClaim time.Time
	var firstText, lastText, claimText string
	wallKnown, claimsKnown, queueKnown := len(tasks) > 0, len(tasks) > 0, len(tasks) > 0
	queue, attempts, knownAttempts, missingAttempts := 0.0, 0.0, 0, 0
	llmAttempts, llmSeconds, missingLLM := 0, 0.0, 0
	for _, task := range tasks {
		start, sok := parseTime(task.CreatedAt)
		end, eok := parseTime(task.CompletedAt)
		if !sok || !eok || end.Before(start) {
			wallKnown = false
		}
		if sok && (first.IsZero() || start.Before(first)) {
			first, firstText = start, task.CreatedAt
		}
		if eok && (last.IsZero() || end.After(last)) {
			last, lastText = end, task.CompletedAt
		}
		claim := task.ClaimedAt
		if at, ok := parseTime(claim); ok && sok && !at.Before(start) {
			if firstClaim.IsZero() || at.Before(firstClaim) {
				firstClaim, claimText = at, claim
			}
		} else {
			claimsKnown = false
		}
		if seconds, ok := tl1Seconds(task.CreatedAt, claim).(float64); ok {
			queue += seconds
		} else {
			queueKnown = false
		}
		for _, attempt := range task.Attempts {
			seconds, ok := tl1AttemptSeconds(attempt).(float64)
			if ok {
				attempts += seconds
				knownAttempts++
			} else {
				missingAttempts++
			}
			if task.ExecutionClass == "llm" {
				llmAttempts++
				if ok {
					llmSeconds += seconds
				} else {
					missingLLM++
				}
			}
		}
	}
	makespan := tl1Seconds(firstText, lastText)
	if !wallKnown {
		makespan = nil
	}
	var concurrency any
	if seconds, ok := makespan.(float64); ok && seconds > 0 && llmAttempts > 0 && missingLLM == 0 {
		concurrency = llmSeconds / seconds
	}
	var queueWait any
	if claimsKnown {
		queueWait = tl1Seconds(firstText, claimText)
	}
	return map[string]any{
		"tasks": len(tasks), "started_at": nilIfEmpty(firstText), "completed_at": nilIfEmpty(lastText),
		"queue_wait_seconds": queueWait, "total_task_queue_wait_seconds": tl1Optional(queue, queueKnown),
		"attempt_seconds":       tl1Optional(attempts, knownAttempts > 0 && missingAttempts == 0),
		"known_attempt_seconds": tl1Optional(attempts, knownAttempts > 0), "attempts_missing_endpoints": missingAttempts,
		"llm_attempt_seconds":  tl1Optional(llmSeconds, llmAttempts > 0 && missingLLM == 0),
		"attempt_time_measure": "LLM time is the sum of LLM attempt end minus start, including retries and tools; it excludes queue wait",
		"wall_seconds":         makespan, "makespan_seconds": makespan, "average_llm_concurrency": concurrency,
		"used_slot_hours":     tl1Optional(llmSeconds/3600, llmAttempts > 0 && missingLLM == 0),
		"capacity_slot_hours": nil, "idle_slot_hours": nil, "slot_utilisation": nil,
		"ramp_idle_slot_hours": nil, "tail_idle_slot_hours": nil,
		"capacity_status": "unavailable", "capacity_note": "TL1 capacity over time is not ingested. Slot utilisation and idle/ramp/tail slot-hours require capacity history from TL1; observed concurrency is not capacity.",
	}
}

// Enqueues are the existing groups of at least five root tasks separated by
// at most ten minutes. Descendants follow their root even across later enqueues.
// Only tasks in the selected window/scope contribute to each returned batch.
func tl1TimingBatches(data *tl1Data) []map[string]any {
	rows := []map[string]any{}
	for _, spike := range data.Enqueues {
		tasks := []*tl1Task{}
		for _, task := range data.Tasks {
			root := task
			seen := map[string]bool{}
			for root.Parent != nil && !seen[root.ID] {
				seen[root.ID] = true
				root = root.Parent
			}
			if root.ParentID == "" && root.CreatedBy == "" && root.CreatedAt >= spike.StartedAt && root.CreatedAt <= spike.EndedAt {
				tasks = append(tasks, task)
			}
		}
		if len(tasks) == 0 {
			continue
		}
		row := tl1TimingSummary(tasks)
		row["enqueue_started_at"], row["enqueue_ended_at"] = spike.StartedAt, spike.EndedAt
		row["enqueued_root_tasks"] = spike.RootTasks
		row["scope"] = "selected tasks and descendants belonging to this enqueue"
		rows = append(rows, row)
		if len(rows) >= tl1EnqueueListLimit {
			break
		}
	}
	return rows
}
