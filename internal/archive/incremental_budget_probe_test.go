//go:build probe

// Resource budgets for scheduled incremental syncs. They need an otherwise
// idle Mac, so they are a probe, built only with the probe tag:
//
//	PHAROS_INCREMENTAL_RESOURCE_GATES=1 go test -tags probe -run TestIncrementalScheduledResourceBudgets -v ./internal/archive
//
// Read-only Codex verification canary against an existing development catalog:
//
//	PHAROS_CODEX_AUDIT_INPUTS=/absolute/sample.json go test -tags probe -run TestCodexScopedAuditSample -v ./internal/archive
//
// sample.json contains root, catalog, account, host, and paths. Source files and
// the selected catalog are read-only; only t.TempDir reference catalogs change.
// BenchmarkIncrementalScheduled (incremental_bench_test.go) measures the same
// workloads without budgets.

package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCodexScopedAuditSample(t *testing.T) {
	input := os.Getenv("PHAROS_CODEX_AUDIT_INPUTS")
	if input == "" {
		t.Skip("set PHAROS_CODEX_AUDIT_INPUTS to an explicit read-only sample descriptor")
	}
	var sample struct {
		Root, Catalog, Account, Host string
		Paths                        []string
	}
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &sample); err != nil {
		t.Fatal(err)
	}
	if sample.Root == "" || sample.Catalog == "" || len(sample.Paths) == 0 {
		t.Fatal("sample needs root, catalog and paths")
	}
	if os.Getenv("PHAROS_AUDIT_QUERY_TIMINGS") == "1" {
		previous := auditQueryTiming
		t.Cleanup(func() { auditQueryTiming = previous })
		auditQueryTiming = func(statement string, elapsed time.Duration, rows int) {
			if elapsed > 100*time.Millisecond {
				t.Logf("scope-query seconds=%.3f rows=%d SQL=%s", elapsed.Seconds(), rows, statement)
			}
		}
	}
	catalog, err := openCatalogReadOnly(sample.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	source := SourceConfig{Name: "codex", Kind: "codex", Path: sample.Root, Account: sample.Account, Enabled: true}
	adapter, err := MakeAdapter(source)
	if err != nil {
		t.Fatal(err)
	}
	selected := map[string]bool{}
	for _, path := range sample.Paths {
		selected[path] = true
	}
	units := map[string]syncUnit{}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	setAdapterContext(adapter, ctx)
	err = adapter.(partialAdapter).discoverParts(func(parts []sourcePart) bool {
		unit := unitOf(source, parts, false)
		if selected[unit.ID] {
			unit.Host = sample.Host
			units[unit.ID] = unit
		}
		return true
	}, func(*WorkspaceRecord, []sourcePart) error { return fmt.Errorf("canary discovery attempted to parse") })
	if err != nil {
		t.Fatal(err)
	}
	for index, path := range sample.Paths {
		unit, ok := units[path]
		if !ok {
			t.Fatalf("sample %d was not discovered", index)
		}
		t.Run(fmt.Sprintf("sample-%d", index), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), automaticAuditWall)
			defer cancel()
			started := time.Now()
			record, parts, evidence, err := freezeCodexAuditRecord(ctx, unit, filepath.Join(t.TempDir(), "evidence"), automaticAuditBytes)
			if err != nil {
				t.Fatalf("sample %d evidence: %v", index, err)
			}
			if record == nil || partsSignature(parts) != partsSignature(unit.Parts) {
				t.Fatalf("sample %d changed while acquired", index)
			}
			t.Logf("evidence_bytes=%d snapshot_seconds=%.3f", evidence, time.Since(started).Seconds())
			comparison := filepath.Join(t.TempDir(), "comparison")
			differences, err := catalog.compareRetainedAuditRecord(ctx, unit, *record, comparison, automaticAuditBytes)
			if err == nil && len(differences) > 0 {
				reference, openErr := openCatalogReadOnly(filepath.Join(comparison, "reference.sqlite3"))
				if openErr == nil {
					defer reference.Close()
					actual, _ := snapshotAuditScope(ctx, catalog, *record, automaticAuditBytes)
					expected, _ := snapshotAuditScope(ctx, reference, *record, automaticAuditBytes)
					if len(actual["conversations"]) == len(expected["conversations"]) {
						for i, row := range actual["conversations"] {
							for key, value := range row {
								if normalizeAuditRows("conversations", []map[string]any{{key: value}}) != normalizeAuditRows("conversations", []map[string]any{{key: expected["conversations"][i][key]}}) {
									t.Logf("conversation field differs: %s", key)
								}
							}
						}
					}
				}
			}
			if err != nil || len(differences) > 0 {
				t.Fatalf("sample %d comparison: %v, categories=%v", index, err, differences)
			}
			t.Logf("sample=%d outcome=passed evidence_bytes=%d seconds=%.3f", index, evidence, time.Since(started).Seconds())
		})
	}
}

func TestIncrementalScheduledResourceBudgets(t *testing.T) {
	if os.Getenv("PHAROS_INCREMENTAL_RESOURCE_GATES") != "1" {
		t.Skip("set PHAROS_INCREMENTAL_RESOURCE_GATES=1 on an otherwise idle benchmark host")
	}
	for _, workload := range []string{"unchanged", "append", "metadata", "active-groups", "large-group", "first-run"} {
		t.Run(workload, func(t *testing.T) {
			useHost(t, "resource-host")
			root := t.TempDir()
			groups, messages := 1, 200
			wallLimit, cpuLimit, allocationLimit, memoryLimit := float64(3), float64(3), uint64(64<<20), int64(128<<20)
			if workload == "active-groups" {
				groups, allocationLimit = 8, 128<<20
			}
			if workload == "large-group" {
				messages, wallLimit, cpuLimit, allocationLimit, memoryLimit = 4000, 10, 10, 600<<20, 512<<20
			}
			paths := []string{}
			for index := range groups {
				path := filepath.Join(root, fmt.Sprintf("rollout-%d.jsonl", index))
				writeSourceFile(t, path, codexLines(fmt.Sprintf("resource-%d", index), messages))
				paths = append(paths, path)
			}
			source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}
			server := automaticTestServer(t, source)
			if workload != "first-run" {
				ingestSource(t, server.Catalog, source)
			}
			for _, path := range paths {
				if workload != "unchanged" && workload != "first-run" {
					line := `{"type":"event_msg","timestamp":"2026-09-01T01:00:00Z","payload":{"type":"user_message","message":"resource gate append"}}`
					if workload == "metadata" {
						line = `{"type":"turn_context","payload":{"model":"resource-gate-model"}}`
					}
					appendSourceFile(t, path, line+"\n")
				}
				stamp := time.Now().Add(-time.Minute)
				if err := os.Chtimes(path, stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}
			if !server.runAutomatic(t.Context(), syncSettings{Interval: 300}) {
				t.Fatal("resource workload not admitted")
			}
			var text string
			if err := server.Catalog.DB.QueryRow("SELECT sample_json FROM sync_history ORDER BY id DESC LIMIT 1").Scan(&text); err != nil {
				t.Fatal(err)
			}
			var sample syncMeasurement
			if err := json.Unmarshal([]byte(text), &sample); err != nil {
				t.Fatal(err)
			}
			if sample.State != "complete" || sample.Audit.Outcome != "passed" {
				t.Fatalf("resource budget cannot substitute for correctness: %+v", sample)
			}
			if sample.Seconds > wallLimit || sample.CPUSeconds > cpuLimit || sample.Allocated > allocationLimit || sample.RSSPeak-sample.RSSBaseline > memoryLimit {
				t.Fatalf("%s exceeded budgets (wall %.1fs, CPU %.1fs, allocations %d, RSS growth %d): %+v", workload, wallLimit, cpuLimit, allocationLimit, memoryLimit, sample)
			}
			t.Logf("wall=%.3fs CPU=%.3fs allocated=%d observed-RSS-growth=%d audit=%s", sample.Seconds, sample.CPUSeconds, sample.Allocated, sample.RSSPeak-sample.RSSBaseline, sample.Audit.Outcome)
		})
	}
}
