package querytable

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSnapshotBindingRequired(t *testing.T) {
	ctx := context.Background()
	options := metricOptions()
	var rows ServerQueryV2
	if err := json.Unmarshal([]byte(`{"version":2,"profile":"qt-postgres-v1","snapshot":"snapshot-2","where":[],"orderBy":[],"select":["id"],"limit":2,"offset":0}`), &rows); err != nil {
		t.Fatal(err)
	}
	if rows.Snapshot != "snapshot-2" {
		t.Fatal(rows)
	}
	if _, err := CompileRowsV2(ctx, rows, metricSchema(), options); err == nil {
		t.Fatal("missing snapshot validation accepted")
	}
	metrics := MetricQuery{Version: 2, Snapshot: "snapshot-2", Metrics: []AggSpec{{ID: "count", Op: "count", GroupBy: []string{}}}}
	if _, err := CompileMetrics(ctx, metrics, metricSchema(), options); err == nil {
		t.Fatal("metric snapshot ignored")
	}
	expected := errors.New("snapshot expired")
	options.ValidateSnapshot = func(_ context.Context, snapshot string) error {
		if snapshot != "snapshot-2" {
			t.Fatal(snapshot)
		}
		return expected
	}
	if _, err := CompileRowsV2(ctx, rows, metricSchema(), options); !errors.Is(err, expected) {
		t.Fatal(err)
	}
	if _, err := CompileMetrics(ctx, metrics, metricSchema(), options); !errors.Is(err, expected) {
		t.Fatal(err)
	}
	options.ValidateSnapshot = func(_ context.Context, snapshot string) error { return nil }
	if _, err := CompileRowsV2(ctx, rows, metricSchema(), options); err != nil {
		t.Fatal(err)
	}
	if _, err := CompileMetrics(ctx, metrics, metricSchema(), options); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotEnvelopeAndCallback(t *testing.T) {
	// Reusing a decoded request must clear old snapshot metadata when omitted.
	var rows ServerQueryV2
	var metrics MetricQuery
	rowJSON := `{"version":2,"profile":"qt-postgres-v1","snapshot":"snapshot-2","select":["id"],"limit":2}`
	metricJSON := `{"version":2,"snapshot":"snapshot-2","metrics":[{"id":"n","op":"count","groupBy":[]}]}`
	if err := json.Unmarshal([]byte(rowJSON), &rows); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(metricJSON), &metrics); err != nil {
		t.Fatal(err)
	}
	if metrics.Snapshot != "snapshot-2" {
		t.Fatal(metrics)
	}
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "request")
	calls := 0
	o := metricOptions()
	o.ValidateSnapshot = func(got context.Context, snapshot string) error {
		calls++
		if got != ctx || got.Value(contextKey{}) != "request" || snapshot != "snapshot-2" {
			t.Fatal("lost callback context/token", snapshot)
		}
		return nil
	}
	p, err := CompileRowsV2(ctx, rows, metricSchema(), o)
	if err != nil || !p.RequiresSnapshot {
		t.Fatal(p, err)
	}
	batch, err := CompileMetrics(ctx, metrics, metricSchema(), o)
	if err != nil || !batch.RequiresSharedSnapshot || !batch.Metrics[0].RequiresSnapshot {
		t.Fatal(batch, err)
	}
	if calls != 2 {
		t.Fatal("expected one validation per request", calls)
	}
	rows.Snapshot, metrics.Snapshot = strings.Repeat("s", 10001), strings.Repeat("s", 10001)
	for _, compile := range []func() error{
		func() error { _, err := CompileRowsV2(ctx, rows, metricSchema(), o); return err },
		func() error { _, err := CompileMetrics(ctx, metrics, metricSchema(), o); return err },
	} {
		var diagnostic *PlanDiagnostic
		if err := compile(); !errors.As(err, &diagnostic) || diagnostic.Code != "snapshot_validation_required" {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal("oversized snapshot reached host", calls)
	}
	if err := json.Unmarshal([]byte(`{"version":2,"profile":"qt-postgres-v1","select":["id"],"limit":2}`), &rows); err != nil {
		t.Fatal(err)
	}
	if rows.Snapshot != "" {
		t.Fatal("stale row snapshot", rows.Snapshot)
	}
	metrics.Snapshot = ""
	if _, err := CompileRowsV2(ctx, rows, metricSchema(), o); err != nil {
		t.Fatal(err)
	}
	if _, err := CompileMetrics(ctx, metrics, metricSchema(), o); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("empty snapshot reached host", calls)
	}
}
