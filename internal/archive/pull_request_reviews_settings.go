package archive

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
)

// pullRequestReviewsSettings is the Settings → Background Processing switch
// for reading pull request reviews: whether it is on, whether this Mac can
// ask each forge, and what the library holds so far.
func (s *Server) pullRequestReviewsSettings(ctx context.Context) map[string]any {
	config := s.Config()
	status := "Off. Turn on to read reviews and comments on your workspaces' pull requests from GitHub."
	notice := ""
	if config.PullRequestReviews {
		status = "On. Pull request reviews follow the background update schedule."
		for host := range forges {
			if reason := s.Catalog.forgeUnavailable(ctx, host, false); reason != "" {
				status = fmt.Sprintf("On, but %s can't be asked: %s.", host, reason)
				notice = status
			}
		}
	}
	document := map[string]any{"enabled": config.PullRequestReviews, "can_change": config.Path != "", "status": status, "notice": notice}
	if coverage, err := s.Catalog.pullRequestReviewsCoverage(ctx); err == nil && coverage != nil {
		document["coverage"] = coverage
		// The switch's line beside its title, where a model shows its device.
		read := "No pull request read yet"
		if fetched := integer(coverage["fetched"]); fetched == 1 {
			read = "1 pull request read"
		} else if fetched > 1 {
			read = fmt.Sprintf("%d pull requests read", fetched)
		}
		document["execution"] = map[string]any{"device": "forge", "status": read}
	}
	return document
}

// postPullRequestReviewsSettings saves the switch with the library's other
// settings and applies it without a restart. Turning it on starts a pass;
// turning it off keeps what was read.
func (s *Server) postPullRequestReviewsSettings(w http.ResponseWriter, r *http.Request, body map[string]any) {
	enabled, valid := body["enabled"].(bool)
	if !valid {
		writeError(w, fmt.Errorf("enabled must be true or false"), http.StatusBadRequest)
		return
	}
	if s.Config().Path == "" {
		writeError(w, fmt.Errorf("the running service has no writable configuration path"), http.StatusBadRequest)
		return
	}
	if err := s.setPullRequestReviewsEnabled(enabled); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	if enabled {
		s.requestBackground("pull-request-reviews")
	}
	s.wakeBackground()
	s.getBackground(w, r)
}

func (s *Server) setPullRequestReviewsEnabled(enabled bool) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	hostFileMu.Lock()
	err := setRootValue(s.config.Path, "pull_request_reviews_enabled", strconv.FormatBool(enabled))
	hostFileMu.Unlock()
	if err != nil {
		return err
	}
	s.config.PullRequestReviews = enabled
	return nil
}
