package archive

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the production reference-catalog creation path; the ordinary test
// harness substitutes a preinitialized catalog and would hide that cost.
func BenchmarkVerificationLedgerUpgrade(b *testing.B) {
	override(b, &newCatalog, OpenCatalog)
	root := b.TempDir()
	var lines strings.Builder
	for i := range 32 {
		lines.WriteString(claudeLine("upgrade", fmt.Sprintf("user-%d", i), "question", "", i*2+1))
		lines.WriteString(jsonText(map[string]any{"type": "assistant", "uuid": fmt.Sprintf("answer-%d", i), "sessionId": "upgrade", "timestamp": "2026-09-01T00:02:00Z", "message": map[string]any{
			"id": fmt.Sprintf("request-%d", i), "role": "assistant", "model": "claude-sonnet-4-5", "content": []any{map[string]any{"type": "text", "text": strings.Repeat("retained output ", 1024)}}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 2}}}) + "\n")
	}
	if err := os.WriteFile(filepath.Join(root, "session.jsonl"), []byte(lines.String()), 0o600); err != nil {
		b.Fatal(err)
	}
	source := SourceConfig{Name: "claude", Kind: "claude", Path: root, Account: "local", Enabled: true}
	catalog, _ := testCatalog(b)
	adapter, err := MakeAdapter(source)
	if err != nil {
		b.Fatal(err)
	}
	if result := catalog.IngestContext(b.Context(), adapter, nil); result.Error != nil {
		b.Fatal(result.Error)
	}
	var unit syncUnit
	if err := adapter.(partialAdapter).discoverParts(func(parts []sourcePart) bool { unit = unitOf(source, parts, false); return true }, func(*WorkspaceRecord, []sourcePart) error { return nil }); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		if _, err := catalog.DB.Exec("UPDATE model_requests SET source_message_id=NULL,final_message_id=NULL; UPDATE tool_ledger_state SET version='tools-v12-inference-speed-policy'"); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if result := catalog.auditUnitMode(b.Context(), unit, "benchmark", 0, true); result.Outcome != "passed" {
			b.Fatalf("upgrade verification: %+v", result)
		}
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
			before := processResourceUsage()
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
			// CPU is reported only where this platform reads it.
			if cpu := cpuDelta(before, processResourceUsage()); cpu != nil {
				b.ReportMetric(*cpu/float64(b.N), "cpu-s/op")
			}
		})
	}
}
