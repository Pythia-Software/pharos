package querytable

import (
	"context"
	"encoding/json"
)

// ServerQueryV2 mirrors the core computed-row request. HTTP authorization and
// plan-token validation remain host responsibilities, distinct from revisions.
type ServerQueryV2 struct {
	WireQuery
	Version           int               `json:"version"`
	Profile           string            `json:"profile"`
	ExpectedRevisions map[string]string `json:"expectedRevisions"`
	PlanToken         string            `json:"planToken,omitempty"`
	Snapshot          string            `json:"snapshot,omitempty"`
	Diagnostics       []any             `json:"diagnostics,omitempty"`
}

func (q *ServerQueryV2) UnmarshalJSON(data []byte) error {
	var base WireQuery
	if err := json.Unmarshal(data, &base); err != nil {
		return err
	}
	var meta struct {
		Version           int               `json:"version"`
		Profile           string            `json:"profile"`
		ExpectedRevisions map[string]string `json:"expectedRevisions"`
		PlanToken         string            `json:"planToken"`
		Snapshot          string            `json:"snapshot"`
		Diagnostics       []any             `json:"diagnostics"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return err
	}
	*q = ServerQueryV2{base, meta.Version, meta.Profile, meta.ExpectedRevisions, meta.PlanToken, meta.Snapshot, meta.Diagnostics}
	return nil
}
func requestOptions(ctx context.Context, profile, token, snapshot string, revisions map[string]string, o PlanOptions) (PlanOptions, error) {
	return requestOptionsDialect(ctx, false, profile, token, snapshot, revisions, o)
}
func requestOptionsDialect(ctx context.Context, sqlite bool, profile, token, snapshot string, revisions map[string]string, o PlanOptions) (PlanOptions, error) {
	expected := ServerExpressionProfile
	if sqlite {
		expected = SQLiteExpressionProfile
	}
	if profile != "" && profile != expected {
		return o, diagnostic("profile_mismatch", "requested execution profile is unavailable")
	}
	if snapshot != "" {
		if len(snapshot) > 10000 || o.ValidateSnapshot == nil {
			return o, diagnostic("snapshot_validation_required", "host must validate and bind the requested data snapshot")
		}
		if err := o.ValidateSnapshot(ctx, snapshot); err != nil {
			return o, err
		}
	}
	if token != "" {
		if o.ValidatePlanToken == nil {
			return o, diagnostic("plan_token_validation_required", "host must validate actor/dataset/profile/expiry-bound plan token")
		}
		if err := o.ValidatePlanToken(ctx, token); err != nil {
			return o, err
		}
	}
	if revisions != nil {
		o.ExpectedRevisions = revisions
	}
	return o, nil
}
func CompileRowsV2(ctx context.Context, q ServerQueryV2, s Schema, o PlanOptions) (SQLPlan, error) {
	if q.Version != 2 {
		return SQLPlan{}, diagnostic("version", "row protocol version must be 2")
	}
	if len(q.Diagnostics) > 0 {
		return SQLPlan{}, diagnostic("residual_query", "row query contains unresolved diagnostics")
	}
	if q.Profile == "" {
		return SQLPlan{}, diagnostic("profile_required", "computed row request must specify profile")
	}
	options, err := requestOptions(ctx, q.Profile, q.PlanToken, q.Snapshot, q.ExpectedRevisions, o)
	if err != nil {
		return SQLPlan{}, err
	}
	return CompileComputedRows(ctx, q.WireQuery, s, options)
}
