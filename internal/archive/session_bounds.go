package archive

import "encoding/json"

// SessionTimeBounds describes an enclosing attempt, never active execution
// time or the timestamps of individual requests. A missing end stays unknown.
type SessionTimeBounds struct {
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at,omitempty"`
	Source    string `json:"source"`
}

func decodeSessionBounds(value any) *SessionTimeBounds {
	var bounds *SessionTimeBounds
	if err := json.Unmarshal([]byte(firstString(value)), &bounds); err != nil {
		return nil
	}
	return bounds
}

func sessionBoundsJSON(bounds *SessionTimeBounds) any {
	if bounds == nil {
		return nil
	}
	return jsonText(bounds)
}

func attemptSessionBounds(start, end string) *SessionTimeBounds {
	started, ok := parseTime(start)
	if !ok {
		return nil
	}
	bounds := &SessionTimeBounds{StartedAt: iso(start), Source: "attempt-envelope"}
	if ended, ok := parseTime(end); ok && !ended.Before(started) {
		bounds.EndedAt = iso(end)
	}
	return bounds
}

// A single complete root transcript may use the enclosing attempt when it has
// no timed messages. Multiple files can represent different processes/parts of
// an attempt: none is assigned the whole attempt. Partial transcripts and
// embedded or separately recorded child agents cannot inherit this envelope.
func rootSessionEnvelope(c ConversationRecord) *SessionTimeBounds {
	if c.SessionBounds == nil || c.SessionBounds.Source != "attempt-envelope" ||
		c.AgentDepth != 0 || c.ParentNativeID != "" || c.Copy || defaultString(c.Coverage, "complete") != "complete" {
		return nil
	}
	return attemptSessionBounds(c.SessionBounds.StartedAt, c.SessionBounds.EndedAt)
}

func applySessionEnvelope(summary *agentSessionSummary, c ConversationRecord) {
	if summary.startedAt != "" || summary.endedAt != "" || summary.nativeID != "main" {
		return
	}
	if bounds := rootSessionEnvelope(c); bounds != nil {
		summary.startedAt, summary.endedAt, summary.timeBoundsSource = bounds.StartedAt, bounds.EndedAt, bounds.Source
	}
}
