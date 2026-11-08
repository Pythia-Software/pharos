package archive

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"slices"
	"strconv"
	"time"
)

func (s *Server) workSettings() map[string]any {
	config := s.Config()
	locked := ""
	if _, ok := backgroundCPUsFromEnv(); ok {
		locked = "PHAROS_BACKGROUND_CPUS sets this allowance at process start."
	} else if config.Path == "" {
		locked = "This service has no writable configuration path."
	}
	return map[string]any{"available": s.work != nil, "engine": defaultString(config.WorkEngine, "classic"), "cpu_slots": max(1, config.BackgroundCPUs), "max_cpu_slots": runtime.NumCPU(),
		"scope": "this process", "can_change": s.work != nil && locked == "", "locked_reason": locked,
		"description": "Shared background worker allowance for this library service. Other Pharos processes have separate allowances; interactive queries remain available.",
		"enforcement": "Cooperative Go pools and native worker threads; an allowance is not measured CPU use or an OS affinity limit."}
}

type workInspectionDocument struct {
	workInspection
	Labels map[string]string `json:"labels"`
}

func (w *workService) inspection() workInspectionDocument {
	labels := make(map[string]string, len(w.runner.kinds))
	for _, task := range w.runner.kinds {
		if kind, ok := lookupOperationKind(task.def.Kind); ok {
			labels[task.def.Kind] = kind.label("")
		}
	}
	return workInspectionDocument{workInspection: w.runner.Inspect(), Labels: labels}
}

func (s *Server) postWorkSettings(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if s.work == nil {
		writeError(w, errWorkUnavailable, http.StatusConflict)
		return
	}
	if _, ok := backgroundCPUsFromEnv(); ok {
		writeError(w, fmt.Errorf("the process-start PHAROS_BACKGROUND_CPUS setting owns this allowance"), http.StatusConflict)
		return
	}
	value, valid := number(body["cpu_slots"])
	if !valid || value != float64(int(value)) || value < 1 || value > float64(runtime.NumCPU()) {
		writeError(w, fmt.Errorf("cpu_slots must be a whole number from 1 to %d", runtime.NumCPU()), http.StatusBadRequest)
		return
	}
	s.configMu.Lock()
	err := setRootValue(s.config.Path, "background_cpu_slots", strconv.Itoa(int(value)))
	if err == nil {
		s.config.BackgroundCPUs = int(value)
	}
	s.configMu.Unlock()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	if s.work.authority.cap() != int(value) {
		s.work.authority.SetCPUs(int(value))
		s.Catalog.labelerManager(s.Config().Labels).Retire()
	}
	s.work.runner.Wake()
	s.getBackground(w, r)
}

func (s *Server) workKinds(body map[string]any) ([]string, error) {
	if raw, ok := body["kinds"]; ok {
		list, valid := raw.([]any)
		if !valid || len(list) == 0 || len(list) > len(s.work.runner.kinds) {
			return nil, fmt.Errorf("kinds must name one or more supported task kinds")
		}
		kinds := []string{}
		for _, value := range list {
			kind, ok := value.(string)
			if !ok || s.work.runner.byKind[kind] == nil {
				return nil, fmt.Errorf("unsupported work kind %q", value)
			}
			kinds = append(kinds, kind)
		}
		slices.Sort(kinds)
		return slices.Compact(kinds), nil
	}
	return s.currentPopulationKinds(), nil
}

func (s *Server) postWork(w http.ResponseWriter, r *http.Request, body map[string]any) {
	if s.work == nil {
		writeError(w, errWorkUnavailable, http.StatusConflict)
		return
	}
	action := firstString(body["action"])
	switch action {
	case "recent", "backfill":
		fromText, untilText := firstString(body["from"]), firstString(body["until"])
		_, hasFrom := body["from"]
		_, hasUntil := body["until"]
		var from, until time.Time
		if hasFrom || hasUntil {
			var fromErr, untilErr error
			from, fromErr = time.Parse(time.RFC3339, fromText)
			until, untilErr = time.Parse(time.RFC3339, untilText)
			if action != "backfill" || fromErr != nil || untilErr != nil || !from.Before(until) {
				writeError(w, fmt.Errorf("a backfill window requires RFC3339 from and until, with from before until"), http.StatusBadRequest)
				return
			}
		}
		kinds, err := s.workKinds(body)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		var id uint64
		var record *workOperation
		if action == "recent" {
			until = s.Catalog.clock()
			id, record, err = s.work.requestRecent(r.Context(), kinds, until.Add(-48*time.Hour), until, true)
		} else {
			id, record, err = s.work.submit(r.Context(), workRequestSpec{Key: "explicit-backfill:" + fmt.Sprint(kinds) + ":" + fromText + ":" + untilText, Scope: "backfill", Kinds: kinds, Priority: workPriorityBackfill, Explicit: true, Trigger: triggerUser,
				Build: func(ctx context.Context, b *workInventoryBuilder) error {
					return s.Catalog.freezeWorkPopulation(ctx, b, from, until, nil)
				}})
		}
		if err != nil {
			writeError(w, err, http.StatusConflict)
			return
		}
		response := map[string]any{"request_id": id, "operation_id": record.op.id, "scope": action, "state": "queued"}
		if hasFrom {
			response["from"], response["until"] = fromText, untilText
		}
		writeJSON(w, response, http.StatusAccepted)
	case "cancel":
		value, valid := number(body["request_id"])
		if !valid || value < 1 || value > float64(1<<53-1) || value != float64(uint64(value)) {
			writeError(w, fmt.Errorf("request_id must be a positive integer"), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"cancelled": s.work.runner.Cancel(uint64(value)), "effect": "Only this request's demand is detached; shared work may continue."}, http.StatusOK)
	case "stop", "resume", "retry":
		kind := firstString(body["kind"])
		if s.work.runner.byKind[kind] == nil {
			writeError(w, fmt.Errorf("unsupported task kind %q", kind), http.StatusBadRequest)
			return
		}
		changed := false
		switch action {
		case "stop":
			changed = s.work.runner.Stop(kind)
		case "resume":
			changed = s.work.runner.Resume(kind)
			switch kind {
			case "maintenance.origins", "maintenance.library-view", "maintenance.previews", "maintenance.parallelism", "maintenance.session-shape", kindNames:
				// These tasks also honor the existing Search-view Stop.
				changed = s.Catalog.resumeLibraryRefresh() || changed
			}
		case "retry":
			changed = s.work.runner.Retry(kind)
		}
		writeJSON(w, map[string]any{"changed": changed, "kind": kind, "action": action, "hold_lifetime": "until Resume or service restart; saved background pause is separate"}, http.StatusOK)
	default:
		writeError(w, fmt.Errorf("action must be recent, backfill, cancel, stop, resume or retry"), http.StatusBadRequest)
	}
}
