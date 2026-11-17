package archive

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

func TestTeamsRecurringWorkKeepsAllHistoryAndRequiresCapability(t *testing.T) {
	config, _ := json.Marshal(map[string]any{"query_url": teamsExampleQuery, "approval_policy": "metrics_or_review", "question": "Synthetic tokens", "requester": map[string]any{"id": "person", "display_name": "Synthetic"}, "audiences": map[string]any{"response_readers": []any{map[string]any{"kind": "person", "id": "person"}}, "publications": []any{}, "exports": []any{}}, "min_app_version": "0.10.0", "retention_policy": map[string]any{"mode": "organization_controlled", "duration_days": nil, "policy_version": 1}, "result_schema": map[string]any{"columns": []any{map[string]any{"id": "total_tokens", "type": "integer", "role": "metric", "merge": "sum", "metric_id": "captured_tokens", "unit": "tokens"}}}, "schedule": map[string]any{"cadence": "daily", "timezone": "America/Denver", "catch_up": "process", "response_window_seconds": 3600}})
	work := teams.Work{Kind: "deterministic", Config: config, ConfigSHA256: teamsPayloadHash(config), PopulationKey: "current", ExpiresAt: time.Now().Add(time.Hour), Period: json.RawMessage(`{"key":"period:12","start_at":"2026-03-08T07:00:00Z","end_at":"2026-03-09T06:00:00Z","anchor_at":"2026-03-08T07:00:00Z"}`)}
	plan, err := teamsPlanFromWork(work)
	if err != nil || len(plan.Metrics) != 1 || plan.Metrics[0].Field != "total_tokens" {
		t.Fatalf("recurring plan: %v", err)
	}
	manifest := teamsCapabilityManifest{SchemaVersion: 1, AppVersion: "0.10.0", Capabilities: []string{"deterministic_query_v1", "source_snapshot_v2"}}
	if teamsNumericWorkEligible(work, manifest) {
		t.Fatal("recurrence without capability accepted")
	}
	manifest.Capabilities = []string{"deterministic_query_v1", "recurring_deterministic_v1", "source_snapshot_v2"}
	if !teamsNumericWorkEligible(work, manifest) {
		t.Fatal("capable build rejected")
	}
	for _, bad := range []string{`{"key":"period:01","start_at":"2026-03-08T07:00:00Z","end_at":"2026-03-09T06:00:00Z","anchor_at":"2026-03-08T07:00:00Z"}`, `{"key":"period:1","start_at":"2026-03-08T00:00:00-07:00","end_at":"2026-03-09T06:00:00Z","anchor_at":"2026-03-08T07:00:00Z"}`, `{"key":"period:1","start_at":"2026-03-08T07:00:00Z","end_at":"2026-03-09T06:00:00Z","anchor_at":"2026-03-08T08:00:00Z"}`, `{"key":"period:1","start_at":"2026-03-08T07:00:00Z","end_at":"2026-03-09T06:00:00Z","anchor_at":"2026-03-08T07:00:00Z","extra":1}`} {
		candidate := work
		candidate.Period = json.RawMessage(bad)
		if _, err = teamsPlanFromWork(candidate); err == nil {
			t.Fatal("invalid period accepted")
		}
	}
}

func TestTeamsExportRequiresReviewedExactIntegerFields(t *testing.T) {
	plan := teamsQueryPlan{Metrics: []teamsMetric{{ID: "total_tokens", Field: "total_tokens"}}}
	export := teamsFeedExport{ID: "00000000-0000-4000-8000-000000000001", Kind: "feed", Destination: "bigquery:synthetic-project.synthetic_dataset.synthetic_table@US", Fields: []string{"total_tokens"}}
	if !validTeamsExports([]teamsFeedExport{export}, "review", plan) {
		t.Fatal("valid reviewed feed rejected")
	}
	if validTeamsExports([]teamsFeedExport{export}, "metrics_or_review", plan) {
		t.Fatal("automatic feed accepted")
	}
	for _, bad := range []teamsFeedExport{
		{ID: export.ID, Kind: "feed", Destination: "https://synthetic.invalid", Fields: export.Fields},
		{ID: export.ID, Kind: "feed", Destination: export.Destination, Fields: []string{"unknown"}},
		{ID: export.ID, Kind: "feed", Destination: export.Destination, Fields: []string{"total_tokens", "total_tokens"}},
	} {
		if validTeamsExports([]teamsFeedExport{bad}, "review", plan) {
			t.Fatal("invalid feed accepted")
		}
	}
}

func TestTeamsReportRequiresReviewedExactReadersAndFields(t *testing.T) {
	plan := teamsQueryPlan{Metrics: []teamsMetric{{ID: "total_tokens", Field: "total_tokens"}}}
	report := teamsReportPublication{ID: "00000000-0000-4000-8000-000000000001", Kind: "report", Readers: []teamsReportReader{{Kind: "person", ID: "00000000-0000-4000-8000-000000000002"}}, Fields: []string{"total_tokens"}}
	if !validTeamsReports([]teamsReportPublication{report}, "review", plan) {
		t.Fatal("valid reviewed report rejected")
	}
	if validTeamsReports([]teamsReportPublication{report}, "metrics_or_review", plan) {
		t.Fatal("automatic publication accepted")
	}
	report.Readers = append(report.Readers, report.Readers[0])
	if validTeamsReports([]teamsReportPublication{report}, "review", plan) {
		t.Fatal("duplicate audience accepted")
	}
}

func TestTeamsReviewedDestinationsAreBoundInSignedConfig(t *testing.T) {
	destination := map[string]any{"id": "00000000-0000-4000-8000-000000000001", "kind": "feed", "destination": "bigquery:synthetic-project.synthetic_dataset.synthetic_table@US", "fields": []string{"total_tokens"}}
	report := map[string]any{"id": "00000000-0000-4000-8000-000000000002", "kind": "report", "readers": []any{map[string]any{"kind": "person", "id": "00000000-0000-4000-8000-000000000003"}}, "fields": []string{"total_tokens"}}
	cfg := map[string]any{"query_url": teamsExampleQuery, "approval_policy": "review", "question": "Synthetic", "requester": map[string]any{"id": "person", "display_name": "Synthetic"}, "audiences": map[string]any{"response_readers": []any{map[string]any{"kind": "person", "id": "person"}}, "publications": []any{report}, "exports": []any{destination}}, "min_app_version": "0.10.0", "retention_policy": map[string]any{"mode": "organization_controlled", "duration_days": nil, "policy_version": 1}, "result_schema": map[string]any{"columns": []any{map[string]any{"id": "total_tokens", "type": "integer", "role": "metric", "merge": "sum", "metric_id": "captured_tokens", "unit": "tokens"}}}}
	encode := func() teams.Work {
		raw, _ := json.Marshal(cfg)
		return teams.Work{Kind: "deterministic", Config: raw, ConfigSHA256: teamsPayloadHash(raw), PopulationKey: "current", ExpiresAt: time.Now().Add(time.Hour), Period: json.RawMessage(`{"key":"once"}`)}
	}
	report["include_completeness"] = true
	if _, e := teamsPlanFromWork(encode()); e != nil {
		t.Fatal("explicit completeness rejected", e)
	}
	report["include_completeness"] = nil
	if _, e := teamsPlanFromWork(encode()); e == nil {
		t.Fatal("null completeness accepted")
	}
	delete(report, "include_completeness")
	work := encode()
	if _, e := teamsPlanFromWork(work); e != nil {
		t.Fatalf("reviewed destinations rejected: %v", e)
	}
	manifest := teamsCapabilityManifest{SchemaVersion: 1, AppVersion: "0.10.0", Capabilities: []string{"deterministic_query_v1", "source_snapshot_v2"}}
	if teamsNumericWorkEligible(work, manifest) {
		t.Fatal("destinations admitted without separate capabilities")
	}
	manifest.Capabilities = []string{"deterministic_query_v1", "export_deterministic_v1", "report_deterministic_v1", "source_snapshot_v2"}
	if !teamsNumericWorkEligible(work, manifest) {
		t.Fatal("reviewed capabilities rejected")
	}
	cfg["approval_policy"] = "metrics_or_review"
	if _, e := teamsPlanFromWork(encode()); e == nil {
		t.Fatal("automatic destinations accepted")
	}
	cfg["approval_policy"] = "review"
	destination["destination"] = "https://synthetic.invalid"
	if _, e := teamsPlanFromWork(encode()); e == nil {
		t.Fatal("unapproved destination accepted")
	}
}
