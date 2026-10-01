package archive

import (
	"encoding/json"
	"slices"
	"time"
)

func activityTimingKey(kind string) string {
	return "activity_durations:" + currentHost().ID + ":" + kind
}

func activityTimingKind(runKind string) string {
	switch runKind {
	case "source-sync":
		return "sync"
	case indexRunKind:
		return "index"
	default:
		return runKind
	}
}

func (s *Server) activityDurations(kind string) []float64 {
	var value string
	if s.Catalog == nil || s.Catalog.DB.QueryRow("SELECT value FROM meta WHERE key=?", activityTimingKey(kind)).Scan(&value) != nil {
		return nil
	}
	var durations []float64
	if json.Unmarshal([]byte(value), &durations) != nil {
		return nil
	}
	return slices.DeleteFunc(durations, func(duration float64) bool { return duration <= 0 })
}

func (s *Server) recordActivityDuration(kind, startedAt, completedAt string) {
	started, startErr := time.Parse(time.RFC3339Nano, startedAt)
	completed, endErr := time.Parse(time.RFC3339Nano, completedAt)
	if startErr != nil || endErr != nil || !completed.After(started) || s.Catalog == nil {
		return
	}
	s.timingMu.Lock()
	defer s.timingMu.Unlock()
	durations := append(s.activityDurations(kind), completed.Sub(started).Seconds())
	if len(durations) > 10 {
		durations = durations[len(durations)-10:]
	}
	value, err := json.Marshal(durations)
	if err == nil {
		_ = setMeta(s.Catalog.DB, activityTimingKey(kind), string(value))
	}
}

func (s *Server) activityDuration(kind string) time.Duration {
	durations := s.activityDurations(kind)
	if len(durations) == 0 {
		return 0
	}
	slices.Sort(durations)
	middle := len(durations) / 2
	seconds := durations[middle]
	if len(durations)%2 == 0 {
		seconds = (durations[middle-1] + seconds) / 2
	}
	return time.Duration(seconds * float64(time.Second))
}
