package archive

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/gbdubs/pharos/internal/teams"
)

type teamsEnrollmentState struct {
	mu                          sync.Mutex
	attempt                     *teams.EnrollmentAttempt
	state, fingerprint, problem string
	generation                  uint64
}

// BeginEnrollment validates the approval origin and its opaque capability;
// the native browser opener must never expose it to the page or logs.
var teamsOpenApproval = func(link string) error {
	if !browserOpenAvailable() || !strings.HasPrefix(link, "https://") || strings.ContainsAny(link, "\r\n") {
		return errors.New("Pharos could not open the Teams sign-in")
	}
	return browserOpenNative(link)
}

func (s *Server) teamsEnrollmentStatus() map[string]any {
	cfg := s.Config()
	result := map[string]any{"state": "disconnected", "available": false, "library": "", "fingerprint": "", "problem": ""}
	d, err := readLibraryDescriptor(cfg)
	if err != nil {
		result["problem"] = "Open a valid library before connecting Teams."
		return result
	}
	result["library"] = d.Name
	if cfg.Application == "" {
		result["problem"] = "Teams needs a registered Pharos installation."
		return result
	}
	if os.Getenv("PHAROS_TEAMS_URL") == "" {
		result["problem"] = "Teams service is not configured on this installation."
	} else {
		result["available"] = true
	}
	settings, err := readTeamsSettings(context.Background(), s.Catalog.reader())
	if err != nil {
		result["problem"] = "Teams settings could not be read."
		result["available"] = false
		return result
	}
	if settings.LibraryID != "" {
		if settings.LibraryID != d.ID || settings.InstanceID != d.Instance {
			result["state"] = "reconcile"
			result["problem"] = "This library was restored or moved. Reconcile its Teams connection before continuing."
			result["available"] = false
		} else {
			credential, e := s.Catalog.loadTeamsCredential(cfg, d)
			if e != nil {
				result["state"] = "reconcile"
				result["problem"] = "The Teams credential is unavailable or needs reconciliation."
				result["available"] = false
			} else {
				credential.Destroy()
				result["state"] = "connected_paused"
				result["optedOut"] = settings.MetricsOptOut
			}
		}
	}
	s.teams.mu.Lock()
	defer s.teams.mu.Unlock()
	if s.teams.state != "" && !(s.teams.state == "connected_paused" && result["state"] != "connected_paused") {
		result["state"] = s.teams.state
		result["fingerprint"] = s.teams.fingerprint
		result["problem"] = s.teams.problem
	}
	return result
}

func (s *Server) teamsEnrollmentHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch {
	case r.URL.Path == "/api/teams/enrollment" && r.Method == http.MethodGet:
		writeJSON(w, s.teamsEnrollmentStatus(), http.StatusOK)
	case r.URL.Path == "/api/teams/enrollment" && r.Method == http.MethodPost:
		cfg := s.Config()
		d, err := readLibraryDescriptor(cfg)
		if err != nil || cfg.Application == "" {
			writeError(w, errors.New("Open a registered library before connecting Teams"), 400)
			return
		}
		origin := os.Getenv("PHAROS_TEAMS_URL")
		if origin == "" {
			writeError(w, errors.New("Teams service is not configured"), 503)
			return
		}
		settings, err := readTeamsSettings(r.Context(), s.Catalog.reader())
		if err != nil {
			writeError(w, errors.New("Teams settings are unavailable"), 500)
			return
		}
		if settings.LibraryID != "" {
			writeError(w, errors.New("This library already has a Teams connection to reconcile"), 409)
			return
		}
		s.teams.mu.Lock()
		if s.teams.state == "starting" || s.teams.attempt != nil {
			s.teams.mu.Unlock()
			writeError(w, errors.New("Teams enrollment is already in progress"), 409)
			return
		}
		s.teams.state = "starting"
		s.teams.problem = ""
		s.teams.generation++
		generation := s.teams.generation
		s.teams.mu.Unlock()
		name := d.Name
		if name == "" {
			name = "Library"
		}
		installationKey, keyErr := teamsInstallationKey(cfg, origin, true)
		if keyErr != nil {
			s.teams.mu.Lock()
			if s.teams.generation == generation {
				s.teams.state = "error"
				s.teams.problem = "Protected Teams credential storage is unavailable."
			}
			s.teams.mu.Unlock()
			writeError(w, errors.New("Protected Teams credential storage is unavailable"), 503)
			return
		}
		attempt, err := teams.BeginEnrollmentWithKey(r.Context(), origin, d.ID, name, "1", installationKey, false)
		clear(installationKey)
		if err != nil {
			s.teams.mu.Lock()
			if s.teams.generation == generation {
				s.teams.state = "error"
				s.teams.problem = "Could not start Teams enrollment."
			}
			s.teams.mu.Unlock()
			writeError(w, errors.New("Could not start Teams enrollment"), 502)
			return
		}
		s.teams.mu.Lock()
		if s.teams.generation != generation || s.teams.state != "starting" {
			s.teams.mu.Unlock()
			attempt.Close()
			writeError(w, errors.New("Teams enrollment was cancelled"), 409)
			return
		}
		if err = teamsOpenApproval(attempt.ApprovalURL); err != nil {
			attempt.Close()
			s.teams.state = "error"
			s.teams.problem = "Could not open the Teams sign-in."
			s.teams.mu.Unlock()
			writeError(w, errors.New("Could not open the Teams sign-in"), 503)
			return
		}
		s.teams.attempt = attempt
		s.teams.state = "waiting"
		s.teams.fingerprint = attempt.Fingerprint
		s.teams.mu.Unlock()
		if !s.start(OperationSpec{Kind: "teams.enrollment", Trigger: triggerUser}, func(ctx context.Context, op *Op) error {
			op.Step("Waiting for browser confirmation", "", -1)
			return s.finishTeamsEnrollment(ctx, cfg, d, attempt)
		}) {
			attempt.Close()
			s.teams.mu.Lock()
			s.teams.attempt = nil
			s.teams.state = "error"
			s.teams.problem = "Pharos is stopping."
			s.teams.mu.Unlock()
			writeError(w, errors.New("Pharos is stopping"), 503)
			return
		}
		writeJSON(w, s.teamsEnrollmentStatus(), http.StatusAccepted)
	case r.URL.Path == "/api/teams/enrollment/cancel" && r.Method == http.MethodPost:
		s.teams.mu.Lock()
		s.teams.generation++
		attempt := s.teams.attempt
		s.teams.attempt = nil
		s.teams.state = ""
		s.teams.fingerprint = ""
		s.teams.problem = ""
		s.teams.mu.Unlock()
		if attempt != nil {
			_ = attempt.Close()
		}
		writeJSON(w, map[string]bool{"ok": true}, 200)
	default:
		writeError(w, errors.New("not found"), 404)
	}
}
func (s *Server) finishTeamsEnrollment(ctx context.Context, cfg Config, d LibraryDescriptor, attempt *teams.EnrollmentAttempt) error {
	credential, err := attempt.Redeem(ctx)
	// A lost response may have completed redemption. Retry once with the same
	// code and original key; transport generates a fresh signature and nonce.
	if err != nil && ctx.Err() == nil {
		credential, err = attempt.Redeem(ctx)
	}
	s.teams.mu.Lock()
	defer s.teams.mu.Unlock()
	if s.teams.attempt != attempt {
		if credential != nil {
			credential.Destroy()
		}
		return context.Canceled
	}
	s.teams.attempt = nil
	s.teams.fingerprint = ""
	if err != nil {
		s.teams.state = "error"
		s.teams.problem = "Teams enrollment could not be completed. Start again."
		return errors.New("Teams enrollment could not be completed")
	}
	defer credential.Destroy()
	// Verify the active descriptor again: restored/switched libraries cannot
	// inherit this installation's connection.
	current, e := readLibraryDescriptor(s.Config())
	if e != nil || current.ID != d.ID || current.Instance != d.Instance {
		s.teams.state = "reconcile"
		s.teams.problem = "The active library changed during enrollment."
		return errors.New("active library changed during Teams enrollment")
	}
	if e = s.Catalog.saveTeamsCredential(ctx, cfg, d, credential); e != nil {
		s.teams.state = "reconcile"
		s.teams.problem = "Teams connection needs local reconciliation."
		return errors.New("Teams connection needs local reconciliation")
	}
	s.teams.state = "connected_paused"
	s.teams.problem = "Connection saved. Teams requests remain paused until setup and privacy checks are ready."
	return nil
}
