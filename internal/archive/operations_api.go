package archive

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

// GET /api/operations is the shadow snapshot of this library's operations
// (docs/background-work.md §7.1): what this service runs, what other Pharos
// processes on this Mac run against the library (from their leases and
// journal rows), the eject verdict, and the journal's recent rows for this
// Mac. Nothing reads it yet; it runs beside libraryActivities, which the UI
// still uses. A GET never writes or deletes anything.

// operationsRecent is how long an ended top-level operation stays listed.
const operationsRecent = 10 * time.Minute

// operationState is a state's word (§8.2).
var operationStateWords = map[string]string{opQueued: "Waiting to start", opBlocked: "Waiting", opStopping: "Stopping…", opSucceeded: "Done",
	opFailed: "Failed", opCancelled: "Stopped", opInterrupted: "Interrupted", opSkipped: "Skipped"}

// operationReasons are the reason codes' sentences (§8.3).
var operationReasons = map[string]string{
	"capture-running":     "Waiting for a capture to finish",
	"ingest-busy":         "Waiting for another index to finish",
	"catalog-busy":        "Waiting for the catalog",
	"destination-busy":    "Waiting for the destination",
	"waiting-for-drive":   "Waiting for the drive",
	"waiting-for-network": "Waiting for the network",
	"already-running":     "Already running",
	"nothing-to-do":       "Nothing to do",
	"disabled":            "Turned off",
	"capture-busy":        "A capture was using it",
	"stopping":            "Pharos was stopping",
	"owner-exited":        "The process running it exited",
	"panic":               "It stopped on an internal error",
}

func reasonSentence(reason string) string {
	if sentence := operationReasons[reason]; sentence != "" {
		return sentence
	}
	if kind, ok := lookupOperationKind(strings.TrimPrefix(reason, "deferred-to-")); ok && strings.HasPrefix(reason, "deferred-to-") {
		return "Left for " + lowerFirst(kind.Label)
	}
	return reason
}

// stateLabel is the operation's state word, the kind's verb while running.
func stateLabel(view opView) string {
	if view.State == opRunning {
		return view.Kind.Verb
	}
	return operationStateWords[view.State]
}

// stateDetail is the state with its detail (§8.2).
func stateDetail(view opView) string {
	switch view.State {
	case opBlocked:
		if view.Detail != "" {
			return "Waiting for " + view.Detail
		}
		return reasonSentence(view.Reason)
	case opFailed:
		return "Failed: " + defaultString(view.Error, reasonSentence(view.Reason))
	case opCancelled:
		return "Stopped by you. " + view.Kind.OnEject
	case opInterrupted:
		if view.Reason != "" {
			return "Interrupted: " + lowerFirst(reasonSentence(view.Reason)) + ". " + view.Kind.OnEject
		}
		return "Interrupted. " + view.Kind.OnEject
	case opSkipped:
		return "Skipped: " + lowerFirst(reasonSentence(defaultString(view.Reason, "nothing-to-do")))
	}
	return ""
}

// rangeLabel says a time range in minutes or seconds: "about 3–6 min".
func rangeLabel(low, high float64) string {
	if high < 60 {
		return fmt.Sprintf("about %d–%d s", int(low), int(max(high, low+1)))
	}
	lowMinutes, highMinutes := int(low/60+0.5), int(high/60+0.5)
	if highMinutes <= lowMinutes {
		return fmt.Sprintf("about %d min", max(1, highMinutes))
	}
	return fmt.Sprintf("about %d–%d min", max(1, lowMinutes), highMinutes)
}

// operationTime is the forecast in the snapshot's shape: a range only while
// estimating, or provisional and marked as early (§7.1).
func operationTime(view opView, at time.Time) map[string]any {
	if view.Forecast == nil || opTerminal(view.State) {
		return nil
	}
	estimate := view.Forecast.estimate(at, view.State == opStopping)
	if estimate == nil {
		return nil
	}
	value := map[string]any{"state": estimate.State, "label": nil}
	if estimate.State == "estimating" || estimate.State == "provisional" {
		value["low_seconds"], value["high_seconds"] = estimate.LowSeconds, estimate.HighSeconds
		label := rangeLabel(estimate.LowSeconds, estimate.HighSeconds)
		if estimate.State == "provisional" {
			label += ", early estimate"
		}
		value["label"] = label
	}
	return value
}

// operationStep is the step in the snapshot's shape.
func operationStep(view opView) map[string]any {
	index, count := stepPosition(view)
	if index == 0 && view.Step.Name == "" {
		return nil
	}
	step := map[string]any{"index": index, "count": nil, "name": view.Step.Name, "unit": nilIfEmpty(view.Step.Unit), "done": view.Step.Done, "total": nil}
	if count > 0 {
		step["count"] = count
	}
	if view.Step.Source != "" {
		step["source"] = view.Step.Source
	}
	if view.Step.Total >= 0 {
		step["total"] = view.Step.Total
		if view.Step.Unit != "" && view.Step.Total > 0 {
			step["fraction"] = min(max(view.Step.Done/view.Step.Total, 0), 1)
		}
	}
	return step
}

// operationEntry is one operation in the snapshot's shape.
func operationEntry(view opView, children []opView, at time.Time) map[string]any {
	kind := view.Kind
	entry := map[string]any{"id": view.ID, "kind": view.Spec.Kind, "variant": nilIfEmpty(view.Spec.Variant), "subject": nilIfEmpty(view.Spec.Subject),
		"label": kind.label(view.Spec.Variant), "verb": kind.Verb, "state": view.State, "state_label": stateLabel(view), "detail": nilIfEmpty(stateDetail(view)),
		"reason": nilIfEmpty(view.Reason), "trigger": view.Spec.Trigger, "process": view.Process, "priority": view.Spec.Priority,
		"started_at": formatTime(view.Started), "ended_at": nil, "step": operationStep(view), "time": operationTime(view, at),
		"stoppable": false, "holds": view.Spec.Holds, "affects": view.Spec.Affects, "on_eject": kind.OnEject, "latency": nilIfEmpty(kind.Latency),
		"severity": "none", "legacy": nil, "children": []map[string]any{}, "children_count": len(view.Children)}
	if !view.Ended.IsZero() {
		entry["ended_at"] = formatTime(view.Ended)
	}
	if view.State == opFailed {
		entry["severity"] = "error"
	} else if view.State == opInterrupted {
		entry["severity"] = "warning"
	}
	if view.Spec.Legacy != "" {
		entry["legacy"] = map[string]any{"run_id": view.Spec.Legacy}
	}
	if view.Foreign {
		entry["detail"] = defaultString(firstString(entry["detail"]), "In another process ("+strings.Replace(view.Process, "cli:", "pharos ", 1)+")")
	} else if kind.Stop != "" && !opTerminal(view.State) && view.State != opStopping && view.Parent == "" {
		entry["stoppable"] = true
		entry["stop"] = map[string]any{"method": "POST", "path": "/api/operations/stop", "body": map[string]any{"id": view.ID}}
	}
	running := []map[string]any{}
	for _, child := range children {
		if !opTerminal(child.State) {
			running = append(running, operationEntry(child, nil, at))
		}
	}
	entry["children"] = running
	return entry
}

// foreignOperations lists the operations of live leases as views, with the
// steps their journal rows report.
func (s *Server) foreignOperations(ctx context.Context, leases []leaseProbe) []opView {
	views := []opView{}
	var rows []journalRow
	for _, probe := range leases {
		if !probe.Live || probe.Info == nil || probe.Info.HostID != currentHost().ID {
			continue
		}
		if rows == nil && len(probe.Info.Operations) > 0 {
			rows, _ = s.Catalog.openJournal(ctx, s.ops.instance)
		}
		started, _ := parseTime(probe.Info.StartedAt)
		listed := false
		for _, row := range rows {
			if row.Instance != probe.Info.Instance || row.ParentID != "" {
				continue
			}
			views = append(views, foreignView(*probe.Info, row))
			listed = true
		}
		if listed {
			continue
		}
		kind := firstOf(probe.Info.Kinds)
		row, ok := lookupOperationKind(kind)
		if !ok {
			row = operationKindIndex[unknownOperationKind]
		}
		views = append(views, opView{ID: OpID("lease:" + probe.Info.Instance), Root: OpID("lease:" + probe.Info.Instance), Kind: row,
			Spec:    OperationSpec{Kind: row.Kind, Trigger: triggerCLI, Holds: probe.Info.Holds, Priority: defaultString(row.Priority, priorityForeground)},
			Process: probe.Info.Process, Instance: probe.Info.Instance, State: opRunning, Started: started, Foreign: true})
	}
	return views
}

func foreignView(info leaseInfo, row journalRow) opView {
	kind, ok := lookupOperationKind(row.Kind)
	if !ok {
		kind = operationKindIndex[unknownOperationKind]
	}
	started, _ := parseTime(row.StartedAt)
	total := -1.0
	if row.StepTotal != nil {
		total = *row.StepTotal
	}
	var detail struct {
		Holds    []Hold   `json:"holds"`
		Affects  []string `json:"affects"`
		Priority string   `json:"priority"`
	}
	_ = json.Unmarshal(row.Detail, &detail)
	if detail.Holds == nil {
		detail.Holds = info.Holds
	}
	return opView{ID: OpID(row.ID), Root: OpID(row.RootID), Kind: kind, Process: row.Process, Instance: row.Instance, State: row.State, Reason: row.Reason,
		Error: row.Error, Started: started, Foreign: true, Step: opStep{Index: row.StepIndex, Name: row.StepName, Unit: row.StepUnit, Done: row.StepDone, Total: total},
		Spec: OperationSpec{Kind: row.Kind, Variant: row.Variant, Subject: row.Subject, Trigger: row.Trigger, Holds: detail.Holds, Affects: detail.Affects,
			Priority: defaultString(detail.Priority, kind.Priority), Legacy: row.Legacy}}
}

// ejectInputs gathers what the verdict reads. fresh probes leases afresh
// (the release path); otherwise a probe up to a second old is shared.
func (s *Server) ejectInputs(ctx context.Context, fresh bool) (verdictInput, []opView) {
	leases := s.leases.probe(fresh)
	own := s.ops.views()
	foreign := s.foreignOperations(ctx, leases)
	in := verdictInput{ops: own, leases: leases}
	// Every capture and index lock is probed only on the release and quit
	// path, where stopping work is the intent: writers take them with
	// LOCK_NB, so a probe from a polled read could make a capture or index
	// fail. A polled verdict reuses only the probe of this Mac's capture
	// lock that libraryActivities (and /api/library/status) runs anyway,
	// captureActive: its "capture by another process" goes through the
	// self-check below, unsafe unless a lease or operation took that lock.
	in.fresh = fresh
	if fresh {
		held, failures := heldCaptureLocks(s.Config().CaptureRoot)
		in.locks, in.scanErrors = unnamedLocks(held, own, leases), failures
	}
	in.unregistered = unregisteredActivities(s.libraryActivities(), own, leases)
	// A lease listed through its journal rows is one holder, not two.
	in.leases = slices.DeleteFunc(slices.Clone(leases), func(probe leaseProbe) bool {
		return probe.Info != nil && slices.ContainsFunc(foreign, func(view opView) bool {
			return view.Instance == probe.Info.Instance && !strings.HasPrefix(string(view.ID), "lease:")
		})
	})
	in.ops = append(in.ops, slices.DeleteFunc(slices.Clone(foreign), func(view opView) bool { return strings.HasPrefix(string(view.ID), "lease:") })...)
	return in, append(own, foreign...)
}

// ejectVerdictNow is the verdict at this moment.
func (s *Server) ejectVerdictNow(ctx context.Context, fresh bool) ejectVerdict {
	in, _ := s.ejectInputs(ctx, fresh)
	return computeEjectVerdict(in)
}

// operationsDocument builds the polled snapshot.
func (s *Server) operationsDocument(ctx context.Context) map[string]any {
	return s.operationsDocumentChecked(ctx, false)
}

// operationsDocumentChecked builds the snapshot; fresh (?fresh=1, the
// release path) runs the full lock check.
func (s *Server) operationsDocumentChecked(ctx context.Context, fresh bool) map[string]any {
	at := time.Now()
	revision := s.ops.currentRevision()
	in, views := s.ejectInputs(ctx, fresh)
	verdict := computeEjectVerdict(in)
	byID := map[OpID]opView{}
	for _, view := range views {
		byID[view.ID] = view
	}
	operations := []map[string]any{}
	for _, view := range views {
		if view.Parent != "" || view.Kind == nil || view.Kind.Hidden || view.Tentative {
			continue
		}
		if opTerminal(view.State) && at.Sub(view.Ended) > operationsRecent {
			continue
		}
		children := []opView{}
		for _, id := range view.Children {
			if child, ok := byID[id]; ok {
				children = append(children, child)
			}
		}
		operations = append(operations, operationEntry(view, children, at))
	}
	config := s.Config()
	drive := libraryDriveOf(config)
	library := map[string]any{"id": nil, "name": nil, "drive": map[string]any{"name": drive.Name, "ejectable": drive.Ejectable}}
	if descriptor, err := readLibraryDescriptor(config); err == nil {
		library["id"], library["name"] = descriptor.ID, nilIfEmpty(descriptor.Name)
	}
	recent, err := s.Catalog.recentJournal(ctx, 50)
	if err != nil {
		recent = []journalRow{}
	}
	host := currentHost()
	return map[string]any{"version": 1, "revision": revision, "generated_at": formatTime(at), "host": map[string]any{"id": host.ID, "label": host.Label},
		"library": library, "eject": verdict, "operations": operations, "attention": []any{}, "freshness": []any{}, "resources": nil,
		"journal": map[string]any{"dropped": s.journal.droppedRounds(), "recent": recent}}
}

// operationsETag changes whenever the document may: the registry's revision
// and what the lease probes found.
func operationsETag(document map[string]any) string {
	data, _ := json.Marshal(map[string]any{"eject": document["eject"], "operations": document["operations"], "recent": document["journal"]})
	sum := sha256.Sum256(data)
	return fmt.Sprintf("\"%v-%x\"", document["revision"], sum[:8])
}

func (s *Server) serveOperations(w http.ResponseWriter, r *http.Request) {
	document := s.operationsDocumentChecked(r.Context(), r.URL.Query().Get("fresh") == "1")
	tag := operationsETag(document)
	w.Header().Set("ETag", tag)
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, document, http.StatusOK)
}

// stopOperation answers POST /api/operations/stop {"id": …}: it calls the
// kind's existing stopper and marks the operation stopping (§2.3).
func (s *Server) stopOperation(w http.ResponseWriter, body map[string]any) {
	op := s.ops.find(OpID(firstString(body["id"])))
	if op == nil {
		writeError(w, errors.New("no such operation in this service; work in another process cannot be stopped from Pharos"), http.StatusNotFound)
		return
	}
	view := op.view()
	if opTerminal(view.State) {
		writeJSON(w, map[string]any{"stopping": false, "state": view.State}, http.StatusOK)
		return
	}
	stopped := false
	switch view.Kind.Stop {
	case "index":
		s.ingestStop.mu.Lock()
		current := s.ingestStop.runID
		s.ingestStop.mu.Unlock()
		if current != "" && current == view.Spec.Legacy {
			stopped = s.stopIngest()
		}
	case "view":
		stopped = s.Catalog.stopLibraryRefresh()
	case "backup":
		stopped = s.backups.stop()
	case "cloud":
		id := view.Spec.Legacy
		if view.Spec.Kind == "cloud.connect" {
			id = "connect"
		}
		stopped = s.cloud.stop(id)
	default:
		writeError(w, fmt.Errorf("%s cannot be stopped: %s", view.Kind.Label, view.Kind.OnEject), http.StatusConflict)
		return
	}
	if !stopped {
		writeError(w, errors.New("it is not at a point where it can be stopped; try again in a moment"), http.StatusConflict)
		return
	}
	op.markStopping()
	writeJSON(w, map[string]any{"stopping": true}, http.StatusOK)
}

// unregisteredActivities is the registry's self-check against
// libraryActivities: each writer it lists must have a running operation, or
// a live lease, that accounts for it. It returns the labels of those that do
// not; the verdict makes them unsafe, and the shadow parity test fails on
// them.
func unregisteredActivities(activities []libraryActivity, ops []opView, leases []leaseProbe) []string {
	running := func(match func(opView) bool) bool {
		return slices.ContainsFunc(ops, func(view opView) bool { return !opTerminal(view.State) && match(view) })
	}
	kinds := func(names ...string) func(opView) bool {
		return func(view opView) bool { return slices.Contains(names, view.Spec.Kind) }
	}
	legacy := func(id string) func(opView) bool {
		return func(view opView) bool { return id != "" && view.Spec.Legacy == id }
	}
	missing := []string{}
	for _, activity := range activities {
		if !activity.Writes {
			continue
		}
		var ok bool
		switch {
		case activity.Kind == "sync" || activity.Kind == "index" || activity.Kind == "capture" || activity.Kind == "backup" && !strings.HasPrefix(activity.ID, "cloud:"):
			ok = running(legacy(activity.ID))
		case activity.Kind == "backup":
			parts := strings.SplitN(activity.ID, ":", 3)
			if len(parts) == 3 && parts[1] == "connect" {
				ok = running(kinds("cloud.connect"))
			} else if len(parts) == 3 {
				ok = running(legacy(parts[2]))
			}
		case activity.Kind == "capture-other":
			// This Mac's capture lock, held by an operation that took it
			// (Capture & index, which libraryActivities lists as another
			// process's capture) or by a lease that declares it.
			ok = lockNamed("capture:"+currentHost().ID, ops, leases)
		case activity.Kind == "git":
			ok = running(kinds("analysis.git", "analysis.refresh"))
		case strings.HasPrefix(activity.ID, "upgrade:"):
			ok = running(kinds("library.upgrade"))
		case activity.ID == "tools":
			ok = running(kinds("tools.ledger.build", "library.upgrade"))
		case activity.ID == "substring":
			ok = running(kinds("maintenance.search.substring"))
		case activity.Label == "Updating findings":
			ok = running(kinds("analysis.findings"))
		case activity.Label == "Classifying your writing":
			ok = running(kinds("analysis.authorship"))
		case activity.Label == "Indexing the Library view":
			ok = running(kinds("maintenance.library-view"))
		}
		if !ok {
			missing = append(missing, activity.Label)
		}
	}
	return missing
}
