package archive

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
