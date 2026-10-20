//go:build probe

// Resource budgets for scheduled incremental syncs. They need an otherwise
// idle Mac, so they are a probe, built only with the probe tag:
//
//	PHAROS_INCREMENTAL_RESOURCE_GATES=1 go test -tags probe -run TestIncrementalScheduledResourceBudgets -v ./internal/archive
//
// BenchmarkIncrementalScheduled (incremental_bench_test.go) measures the same
// workloads without budgets.

package archive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
