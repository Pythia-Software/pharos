package archive

import (
	"context"
	"time"
)

var labelerThreadCheck = 2 * time.Minute

// labelerThreadTarget samples the service's current core target at batch
// boundaries. Only thread settings change; the run keeps its model, worker,
// batching and other options. A nil target keeps CLI runs fixed.
type labelerThreadTarget struct {
	read func() LabelerSettings
	next time.Time
}

func (target *labelerThreadTarget) settings(current LabelerSettings) LabelerSettings {
	if target.read == nil || time.Now().Before(target.next) {
		return current
	}
	target.next = time.Now().Add(labelerThreadCheck)
	chosen := target.read()
	current.Threads = max(1, chosen.Threads)
	current.PeerThreads = chosen.PeerThreads
	current.ThreadBudget = chosen.ThreadBudget
	return current
}

// labelerThreadSettings reads Settings again, including each model's peer
// and the battery cap. Both inline and backlog runs use it.
func (s *Server) labelerThreadSettings(ctx context.Context, events bool) func() LabelerSettings {
	return func() LabelerSettings {
		config := s.Config()
		settings := config.AgentEvents.withPeer(config.Labels)
		if events {
			settings = config.AgentEvents.labeler(config.Labels)
		}
		settings, _ = labelsForPower(settings, s.cachedPower(ctx))
		return settings
	}
}
