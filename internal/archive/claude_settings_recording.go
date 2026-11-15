package archive

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

const claudeRecordingDuration = 30 * time.Minute

var claudeRecordingInterval = 5 * time.Second

type claudeSettingsRecording struct {
	mu                 sync.Mutex
	source             string
	expires            time.Time
	ctx                context.Context
	cancel             context.CancelFunc
	generation         uint64
	baseline           int64
	checkedAt, problem string
}

func (c *Catalog) claudeSettingsCount(ctx context.Context, source string) (int64, error) {
	var total int64
	err := c.reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT DISTINCT account,organization_uuid,conversation_uuid FROM claude_chat_settings_observations WHERE host_id=? AND source_name=?)`, currentHost().ID, source).Scan(&total)
	return total, err
}
func (s *Server) claudeRecordingDocument(ctx context.Context, source string) (map[string]any, error) {
	total, err := s.Catalog.claudeSettingsCount(ctx, source)
	if err != nil {
		return nil, err
	}
	recording := &s.claudeSettingsRecording
	recording.mu.Lock()
	defer recording.mu.Unlock()
	active := recording.source == source && recording.cancel != nil && recording.ctx.Err() == nil && time.Now().Before(recording.expires)
	doc := map[string]any{"source": source, "active": active, "known_conversations": total, "new_conversations": int64(0), "interval_seconds": int(claudeRecordingInterval / time.Second)}
	if recording.source == source {
		doc["new_conversations"] = max(int64(0), total-recording.baseline)
		doc["expires_at"] = recording.expires.UTC().Format(time.RFC3339Nano)
		doc["checked_at"], doc["error"] = recording.checkedAt, recording.problem
	}
	return doc, nil
}
func (s *Server) getClaudeSettingsRecording(w http.ResponseWriter, r *http.Request) {
	source, err := s.configuredSource(r.URL.Query().Get("source"))
	if err != nil || source.Kind != claudeSettingsKind {
		writeError(w, errors.New("choose a configured Claude Chat settings source"), http.StatusBadRequest)
		return
	}
	doc, err := s.claudeRecordingDocument(r.Context(), source.Name)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	writeJSON(w, doc, 200)
}
func (s *Server) postClaudeSettingsRecording(w http.ResponseWriter, body map[string]any) {
	source, err := s.configuredSource(firstString(body["source"]))
	active, ok := body["active"].(bool)
	if err != nil || source.Kind != claudeSettingsKind || !ok {
		writeError(w, errors.New("source and active boolean are required for a Claude Chat settings source"), 400)
		return
	}
	if active && !source.Enabled {
		writeError(w, errors.New("enable this source before recording"), 400)
		return
	}
	baseline, err := s.Catalog.claudeSettingsCount(s.life.ctx, source.Name)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	recording := &s.claudeSettingsRecording
	recording.mu.Lock()
	if active && recording.cancel != nil && recording.source != source.Name && time.Now().Before(recording.expires) {
		recording.mu.Unlock()
		writeError(w, errors.New("another Claude settings source is recording"), 409)
		return
	}
	if (!active && recording.source == source.Name) || (active && (recording.cancel == nil || recording.source != source.Name || !time.Now().Before(recording.expires))) {
		if recording.cancel != nil {
			recording.cancel()
			recording.cancel = nil
		}
		recording.generation++
		if active {
			recording.source, recording.baseline = source.Name, baseline
			recording.expires = time.Now().Add(claudeRecordingDuration)
			recording.ctx, recording.cancel = context.WithDeadline(s.life.ctx, recording.expires)
			recording.checkedAt, recording.problem = "", ""
		}
	}
	recording.mu.Unlock()
	select {
	case s.claudeSettingsWake <- struct{}{}:
	default:
	}
	doc, err := s.claudeRecordingDocument(s.life.ctx, source.Name)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	writeJSON(w, doc, 200)
}

// Frequent checks exist only for a bounded, explicitly started session. Normal
// periodic acquisition uses the existing automatic source scheduler.
func (s *Server) claudeSettingsRecordingLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.claudeSettingsWake:
		case <-time.After(claudeRecordingInterval):
		}
		s.checkClaudeSettingsRecording()
	}
}
func (s *Server) checkClaudeSettingsRecording() {
	recording := &s.claudeSettingsRecording
	recording.mu.Lock()
	if recording.cancel == nil {
		recording.mu.Unlock()
		return
	}
	if !time.Now().Before(recording.expires) {
		recording.cancel()
		recording.cancel = nil
		recording.mu.Unlock()
		return
	}
	sourceName, ctx, generation := recording.source, recording.ctx, recording.generation
	recording.mu.Unlock()
	source, err := s.configuredSource(sourceName)
	if err != nil || !source.Enabled || source.Kind != claudeSettingsKind {
		recording.mu.Lock()
		if generation == recording.generation && recording.cancel != nil {
			recording.cancel()
			recording.cancel = nil
			recording.problem = "Recording stopped because the source was paused or removed."
		}
		recording.mu.Unlock()
		return
	}
	// A manual index or another background acquisition gets to finish. The next
	// tick retries, and stop/expiry cancels any acquisition we own.
	if !s.ingestMu.TryLock() {
		return
	}
	defer s.ingestMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	ctx, op := s.begin(ctx, OperationSpec{Kind: "capture.settings", Variant: claudeSettingsKind, Trigger: triggerSchedule})
	op.setStopper(func() { s.stopClaudeSettingsRecording(sourceName, generation, "Recording stopped from Activity.") })
	err = s.backgroundCapture(ctx, op, claudeSettingsKind, []string{sourceName})
	op.Finish(err)
	recording.mu.Lock()
	defer recording.mu.Unlock()
	if generation != recording.generation {
		return
	}
	recording.checkedAt = time.Now().UTC().Format(time.RFC3339Nano)
	recording.problem = ""
	if err != nil && !errors.Is(err, context.Canceled) {
		recording.problem = err.Error()
	}
}

// generation zero is a source-setting action; a running operation may stop
// only the exact recording session it belongs to.
func (s *Server) stopClaudeSettingsRecording(source string, generation uint64, reason string) {
	recording := &s.claudeSettingsRecording
	recording.mu.Lock()
	defer recording.mu.Unlock()
	if recording.source != source || recording.cancel == nil || (generation != 0 && generation != recording.generation) {
		return
	}
	recording.cancel()
	recording.cancel = nil
	recording.generation++
	recording.problem = reason
}

// Settings are joined at read time, so either source may be ingested first.
// Account aliases must match. Keep organizations separate: neither a matching
// alias nor a latest cache selection proves a historical request's model.
func (c *Catalog) claudeConversationSettings(ctx context.Context, account, conversation string) ([]map[string]any, error) {
	return claudeConversationSettingsFrom(ctx, c.reader(), account, conversation)
}

func claudeConversationSettingsFrom(ctx context.Context, q queryer, account, conversation string) ([]map[string]any, error) {
	return queryMapsContext(ctx, q, `SELECT organization_uuid,observed_at,time_basis,model,effort_level,thinking_mode,host_id,source_name,cache_entry,snapshot_id
 FROM claude_chat_settings_observations WHERE account=? AND conversation_uuid=?
 ORDER BY julianday(observed_at) DESC,organization_uuid,snapshot_id LIMIT 501`, account, conversation)
}
