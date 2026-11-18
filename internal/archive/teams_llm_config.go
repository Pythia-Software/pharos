package archive

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

type teamsLLMConfig struct {
	Question       string `json:"question"`
	Prompt         string `json:"prompt"`
	ApprovalPolicy string `json:"approval_policy"`
	Requester      struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"requester"`
	Audiences struct {
		ResponseReaders []struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		} `json:"response_readers"`
		Publications []json.RawMessage `json:"publications"`
		Exports      []json.RawMessage `json:"exports"`
	} `json:"audiences"`
	ResultSchema struct {
		JSONSchema json.RawMessage `json:"json_schema"`
	} `json:"result_schema"`
	MinAppVersion   string `json:"min_app_version"`
	RetentionPolicy struct {
		Mode          string `json:"mode"`
		DurationDays  *int   `json:"duration_days"`
		PolicyVersion int64  `json:"policy_version"`
	} `json:"retention_policy"`
}

// This is the closed initial artifact contract. Unknown routes, tools,
// destinations or execution modes fail closed instead of being ignored.
func teamsLLMFromWork(w teams.Work, appVersion string) (teamsLLMConfig, error) {
	return teamsLLMFromWorkAt(w, appVersion, time.Now())
}

// Call only with the server time from a current authenticated poll for w.
func teamsLLMFromWorkAt(w teams.Work, appVersion string, now time.Time) (teamsLLMConfig, error) {
	var out teamsLLMConfig
	bad := errTeamsQueryUnsupported
	if w.VerifyConfig() != nil || w.Kind != "llm" || w.PopulationKey != "current" || w.ExpiresAt.IsZero() || !now.Before(w.ExpiresAt) || (len(w.RunDecision) != 0 && !bytes.Equal(bytes.TrimSpace(w.RunDecision), []byte("null"))) {
		return out, bad
	}
	period, e := teamsStrictObject(w.Period, map[string]bool{"key": true, "anchor_at": true})
	if e != nil {
		return out, bad
	}
	var key string
	if json.Unmarshal(period["key"], &key) != nil || key != "once" {
		return out, bad
	}
	if anchor, ok := period["anchor_at"]; ok && string(anchor) != "null" {
		var t time.Time
		if json.Unmarshal(anchor, &t) != nil || t.IsZero() {
			return out, bad
		}
	}
	top, e := teamsStrictObject(w.Config, map[string]bool{"question": true, "prompt": true, "approval_policy": true, "requester": true, "audiences": true, "result_schema": true, "min_app_version": true, "retention_policy": true})
	if e != nil || len(top) != 8 {
		return out, bad
	}
	check := func(raw []byte, keys ...string) bool {
		allowed := map[string]bool{}
		for _, k := range keys {
			allowed[k] = true
		}
		v, e := teamsStrictObject(raw, allowed)
		return e == nil && len(v) == len(keys)
	}
	if !check(top["requester"], "id", "display_name") || !check(top["audiences"], "response_readers", "publications", "exports") || !check(top["result_schema"], "json_schema") || !check(top["retention_policy"], "mode", "duration_days", "policy_version") {
		return out, bad
	}
	dec := json.NewDecoder(bytes.NewReader(w.Config))
	dec.DisallowUnknownFields()
	if dec.Decode(&out) != nil || strings.TrimSpace(out.Question) == "" || len(out.Question) > 2000 || strings.TrimSpace(out.Prompt) == "" || len(out.Prompt) > 32000 || out.ApprovalPolicy != "review" || out.Requester.ID == "" || len(out.Requester.DisplayName) > 256 || !teamsVersionAtLeast(appVersion, out.MinAppVersion) || out.Audiences.ResponseReaders == nil || out.Audiences.Publications == nil || out.Audiences.Exports == nil || len(out.Audiences.Publications) != 0 || len(out.Audiences.Exports) != 0 || len(out.Audiences.ResponseReaders) == 0 || len(out.Audiences.ResponseReaders) > 1000 || out.RetentionPolicy.PolicyVersion < 1 {
		return teamsLLMConfig{}, bad
	}
	var aud map[string]json.RawMessage
	if json.Unmarshal(top["audiences"], &aud) != nil {
		return teamsLLMConfig{}, bad
	}
	var readers []json.RawMessage
	if json.Unmarshal(aud["response_readers"], &readers) != nil {
		return teamsLLMConfig{}, bad
	}
	seen := map[string]bool{}
	for i, r := range out.Audiences.ResponseReaders {
		if !check(readers[i], "kind", "id") || r.Kind != "person" || r.ID == "" || seen[r.ID] {
			return teamsLLMConfig{}, bad
		}
		seen[r.ID] = true
	}
	if _, e = teamsCompileArtifactSchema(out.ResultSchema.JSONSchema); e != nil {
		return teamsLLMConfig{}, bad
	}
	return out, nil
}
