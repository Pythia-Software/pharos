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

func BenchmarkIncrementalScheduled(b *testing.B) {
	for _, workload := range []string{"unchanged", "append", "metadata", "active-groups", "large-group", "first-run", "audit-only"} {
		b.Run(workload, func(b *testing.B) {
			directory := incrementalBenchmarkDir(b)
			catalog, err := OpenCatalog(filepath.Join(directory, "catalog.sqlite3"))
			if err != nil {
				b.Fatal(err)
			}
			defer catalog.Close()
			root := filepath.Join(directory, "transcripts")
			if err := os.MkdirAll(root, 0o700); err != nil {
				b.Fatal(err)
			}
			groups, messages := 1, 200
			if workload == "active-groups" {
				groups = 8
			}
			if workload == "large-group" {
				messages = 4000
			}
			paths := []string{}
			for index := range groups {
				path := filepath.Join(root, fmt.Sprintf("rollout-%d.jsonl", index))
				if err := os.WriteFile(path, []byte(codexLines(fmt.Sprintf("benchmark-%d", index), messages)), 0o600); err != nil {
					b.Fatal(err)
				}
				stamp := time.Now().Add(-time.Minute)
				if err := os.Chtimes(path, stamp, stamp); err != nil {
					b.Fatal(err)
				}
				paths = append(paths, path)
			}
			source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}
			config := Config{Sources: []SourceConfig{source}, CaptureRoot: filepath.Join(directory, "captures")}
			server := NewServer(config, catalog)
			defer server.stop(5 * time.Second)
			adapter, _ := MakeAdapter(source)
			if workload != "first-run" {
				if result := catalog.IngestContext(b.Context(), adapter, nil); result.Error != nil {
					b.Fatal(result.Error)
				}
			}
			var unit syncUnit
			if err := adapter.(partialAdapter).discoverParts(func(parts []sourcePart) bool { unit = unitOf(source, parts, false); return true }, func(*WorkspaceRecord, []sourcePart) error { return nil }); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			cpu := processResourceUsage().cpu
			for iteration := range b.N {
				if workload == "audit-only" {
					if result := catalog.auditUnit(b.Context(), unit, "benchmark", automaticAuditBytes); result.Outcome != "passed" {
						b.Fatalf("reference audit: %+v", result)
					}
					continue
				}
				if workload == "first-run" && iteration > 0 {
					b.StopTimer()
					for _, path := range paths {
						if err := os.WriteFile(path, []byte(codexLines(fmt.Sprintf("benchmark-first-%d", iteration), messages)), 0o600); err != nil {
							b.Fatal(err)
						}
						stamp := time.Now().Add(-time.Minute)
						if err := os.Chtimes(path, stamp, stamp); err != nil {
							b.Fatal(err)
						}
					}
					b.StartTimer()
				}
				if workload != "unchanged" && workload != "first-run" {
					b.StopTimer()
					for _, path := range paths {
						line := jsonText(map[string]any{"type": "event_msg", "timestamp": "2026-09-01T01:00:00Z", "payload": map[string]any{"type": "user_message", "message": fmt.Sprintf("incremental workload %d", iteration)}}) + "\n"
						if workload == "metadata" {
							line = jsonText(map[string]any{"type": "turn_context", "payload": map[string]any{"model": fmt.Sprintf("model-%d", iteration)}}) + "\n"
						}
						file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
						if err != nil {
							b.Fatal(err)
						}
						_, err = file.WriteString(line)
						file.Close()
						if err != nil {
							b.Fatal(err)
						}
						stamp := time.Now().Add(-time.Minute)
						if err := os.Chtimes(path, stamp, stamp); err != nil {
							b.Fatal(err)
						}
					}
					b.StartTimer()
				}
				if !server.runAutomatic(b.Context(), syncSettings{Interval: 300}) {
					b.Fatal("automatic run not admitted")
				}
				if run := server.runs[0]; run.State != "complete" {
					b.Fatalf("scheduled run: %s: %v", run.State, run.Error)
				}
			}
			b.ReportMetric((processResourceUsage().cpu-cpu)/float64(b.N), "cpu-s/op")
		})
	}
}
