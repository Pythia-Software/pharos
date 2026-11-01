package archive

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
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
		if waiting := settings.labelsWaiting(); waiting != "" {
			status = "On, " + waiting + "."
		}
		if settings.Worker == "" || !fileExists(settings.Worker) {
			status += " The labeling worker is missing; install a Pharos build that includes it, or configure a local worker."
		} else if settings.Runtime != "" && !fileExists(settings.Runtime) {
			status += " The configured labeling runtime is missing."
		}
	}
	return map[string]any{"enabled": settings.Enabled, "can_change": config.Path != "", "status": status}
}

// postLabelsSettings persists the opt-in alongside labels_enabled's other
// settings, and applies it to the running service without a restart.
func (s *Server) postLabelsSettings(w http.ResponseWriter, r *http.Request, body map[string]any) {
	enabled, valid := body["enabled"].(bool)
	if !valid {
		writeError(w, fmt.Errorf("enabled must be true or false"), http.StatusBadRequest)
		return
	}
	if s.Config().Path == "" {
		writeError(w, fmt.Errorf("the running service has no writable configuration path"), http.StatusBadRequest)
		return
	}
	if err := s.setLabelsEnabled(r.Context(), enabled); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	s.wakeBackground()
	s.getBackground(w, r)
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
