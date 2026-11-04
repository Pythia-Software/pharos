package archive

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
)

// labelsSettings is always visible in Settings, including before labels
// have been enabled or their ledger has been built.
func (s *Server) labelsSettings() map[string]any {
	config := s.Config()
	settings := config.Labels.withAssets()
	status := "Off. Turn on to label messages in this library."
	if settings.Enabled {
		status = "On. Message labels follow the background update schedule."
		waiting := settings.labelsWaiting()
		if problem := settings.labelerProblem(); problem != "" {
			// No download fixes this, so it leads.
			status = "On, but labeling can't run: " + problem + "."
			if waiting != "" {
				status += " It is also " + waiting + "."
			}
		} else if waiting != "" {
			status = "On, " + waiting + "."
		}
	}
	return map[string]any{"enabled": settings.Enabled, "can_change": config.Path != "", "status": status,
		"threads": max(1, settings.Threads), "thread_choices": labelerThreadChoices()}
}

// labelerThreadChoices are the threads per worker Settings offers each
// model: powers of two up to this Mac's cores, and the cores themselves.
func labelerThreadChoices() []int {
	choices := []int{}
	for threads := 1; threads < runtime.NumCPU(); threads *= 2 {
		choices = append(choices, threads)
	}
	return append(choices, runtime.NumCPU())
}

// agentEventsSettings sits beside labelsSettings in Background Processing.
// Its switch is the agentEventsFlag preference, which the page saves; a
// library whose configuration sets agent_events_enabled is on regardless.
func (s *Server) agentEventsSettings(ctx context.Context) map[string]any {
	config := s.Config()
	configured := config.AgentEvents.Enabled
	enabled := configured || s.Catalog.featureFlag(ctx, agentEventsFlag)
	status := "Off. Turn on to find agent events in this library."
	if enabled {
		status = "On. Agent events follow the background update schedule."
		if configured {
			status = "On in this library's configuration (agent_events_enabled)."
		}
		waiting := config.AgentEvents.waiting(config.Labels)
		if problem := config.Labels.withAssets().labelerProblem(); problem != "" {
			status = "On, but agent events can't be found: " + problem + "."
			if waiting != "" {
				status += " It is also " + waiting + "."
			}
		} else if waiting != "" {
			status = "On, " + waiting + "."
		}
	}
	return map[string]any{"enabled": enabled, "can_change": !configured, "status": status,
		"threads": max(1, config.AgentEvents.threads(config.Labels)), "threads_can_change": config.Path != "", "thread_choices": labelerThreadChoices()}
}

// labelerProblem is why no labeling can run on this Mac, however long it
// waits for downloads ("" when it can): no worker, or a configured worker or
// runtime that is missing. Agent events share the worker.
func (settings LabelerSettings) labelerProblem() string {
	switch {
	case settings.Worker == "":
		return "this Pharos has no labeling worker; install a Pharos build that includes it, or configure a local worker"
	case !fileExists(settings.Worker):
		return "the configured labeling worker " + settings.Worker + " is missing"
	case settings.Runtime != "" && !fileExists(settings.Runtime):
		return "the configured labeling runtime " + settings.Runtime + " is missing"
	}
	return ""
}

// postLabelsSettings persists what Settings chose for the labeler (enabled,
// threads, agent_events_threads; any of them) alongside the library's other
// settings, and applies it to the running service without a restart. Threads
// are a target that active runs check every two minutes.
func (s *Server) postLabelsSettings(w http.ResponseWriter, r *http.Request, body map[string]any) {
	threads := map[string]int{}
	for _, key := range []string{"threads", "agent_events_threads"} {
		raw, ok := body[key]
		if !ok {
			continue
		}
		value, valid := raw.(float64)
		if !valid || value != float64(int(value)) || value < 1 || int(value) > runtime.NumCPU() {
			writeError(w, fmt.Errorf("%s must be a whole number from 1 to %d", key, runtime.NumCPU()), http.StatusBadRequest)
			return
		}
		threads[key] = int(value)
	}
	enabled, valid := body["enabled"].(bool)
	if _, ok := body["enabled"]; (ok && !valid) || (!ok && len(threads) == 0) {
		writeError(w, fmt.Errorf("enabled must be true or false"), http.StatusBadRequest)
		return
	}
	if s.Config().Path == "" {
		writeError(w, fmt.Errorf("the running service has no writable configuration path"), http.StatusBadRequest)
		return
	}
	if len(threads) > 0 {
		if err := s.setLabelerThreads(threads); err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
	}
	if valid {
		if err := s.setLabelsEnabled(r.Context(), enabled); err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
	}
	s.wakeBackground()
	s.getBackground(w, r)
}

// setLabelerThreads saves labels_threads and agent_events_threads, and
// recounts each model's peer for the default budget.
func (s *Server) setLabelerThreads(threads map[string]int) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if _, chosen := threads["agent_events_threads"]; !chosen && s.config.AgentEvents.Threads == 0 {
		// Agent events followed labels_threads; Settings shows two separate
		// choices, so changing one keeps the other where it was.
		threads["agent_events_threads"] = max(1, s.config.Labels.Threads)
	}
	hostFileMu.Lock()
	defer hostFileMu.Unlock()
	for _, key := range []string{"threads", "agent_events_threads"} {
		value, ok := threads[key]
		if !ok {
			continue
		}
		name := "labels_threads"
		if key == "agent_events_threads" {
			name = key
		}
		if err := setRootValue(s.config.Path, name, strconv.Itoa(value)); err != nil {
			return err
		}
		if key == "threads" {
			s.config.Labels.Threads = value
		} else {
			s.config.AgentEvents.Threads = value
		}
	}
	s.config.Labels = s.config.AgentEvents.withPeer(s.config.Labels)
	return nil
}

func (s *Server) setLabelsEnabled(ctx context.Context, enabled bool) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	// Enabling an already indexed library must schedule its earlier messages
	// too, even if the disabled stage had previously been marked complete.
	if enabled && !s.config.Labels.Enabled {
		if err := s.Catalog.writeTransaction(ctx, "enable message labels", func(tx *sql.Tx) error {
			return markSyncDeferredStage(tx, "labels")
		}); err != nil {
			return err
		}
	}
	hostFileMu.Lock()
	err := setRootValue(s.config.Path, "labels_enabled", strconv.FormatBool(enabled))
	hostFileMu.Unlock()
	if err != nil {
		return err
	}
	s.config.Labels.Enabled = enabled
	return nil
}
