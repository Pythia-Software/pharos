package archive

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Opt-in, isolated-process profiling on a consistent scratch replica. Never
// point this at an installed catalog: it publishes derived analysis tables.
func TestAnalysisReplica(t *testing.T) {
	path := os.Getenv("PHAROS_ANALYSIS_REPLICA")
	if path == "" {
		t.Skip("set PHAROS_ANALYSIS_REPLICA to a scratch SQLite backup")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		t.Fatal(err)
	}
	if absolute == "/Volumes/euclid/Pharos/catalog/catalog.sqlite3" {
		t.Fatal("the analysis harness only writes scratch replicas")
	}
	db, err := sql.Open("sqlite", catalogDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	c := &Catalog{Path: path, DB: db, now: func() time.Time { return time.Date(2026, 10, 8, 21, 0, 0, 0, time.UTC) }}
	for _, query := range []string{"SELECT COUNT(*) FROM workspaces", "SELECT COUNT(*) FROM conversations", "SELECT COUNT(*) FROM tool_calls"} {
		var n int
		if err := db.QueryRow(query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s = %d", query, n)
	}
	incremental := os.Getenv("PHAROS_ANALYSIS_INCREMENTAL") == "1"
	if incremental {
		if err := c.ensureAnalysisPartitions(); err != nil {
			t.Fatal(err)
		}
	}
	workloads := []string{"full", "warm-full"}
	if incremental {
		workloads = []string{"first", "noop", "corrected", "append", "active-eight", "noop-after"}
	}
	if incremental && os.Getenv("PHAROS_ANALYSIS_WARM") == "1" {
		if err := c.ensureToolRollup(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := c.RefreshFindings(t.Context(), true); err != nil {
			t.Fatal(err)
		}
		workloads = []string{"noop", "corrected", "append", "active-eight", "noop-after"}
	}
	for pass, workload := range workloads {
		if incremental && (workload == "corrected" || workload == "append" || workload == "active-eight") {
			rows, err := queryMapsContext(t.Context(), db, `SELECT id,conversation_id FROM tool_calls WHERE started_at>='2026-10-07' AND started_at<='2026-10-08T21:00:00.000Z' GROUP BY conversation_id ORDER BY MAX(started_at) DESC LIMIT 8`)
			if err != nil || len(rows) == 0 {
				t.Fatalf("active calls: %v %d", err, len(rows))
			}
			if workload != "active-eight" {
				rows = rows[:1]
			}
			for _, row := range rows {
				if workload == "append" {
					columns, err := queryMaps(db, "PRAGMA table_info(tool_calls)")
					if err != nil {
						t.Fatal(err)
					}
					names, values := []string{}, []string{}
					for _, col := range columns {
						name := firstString(col["name"])
						names = append(names, name)
						value := name
						if name == "id" {
							value = fmt.Sprintf("'analysis-append-call-%d'", os.Getpid())
						}
						if name == "sequence" {
							value = "sequence+100000"
						}
						values = append(values, value)
					}
					_, err = db.Exec("INSERT INTO tool_calls("+strings.Join(names, ",")+") SELECT "+strings.Join(values, ",")+" FROM tool_calls WHERE id=?", row["id"])
					if err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := db.Exec("UPDATE tool_calls SET result_tokens=result_tokens+1,carried_tokens=carried_tokens+10 WHERE id=?", row["id"]); err != nil {
						t.Fatal(err)
					}
				}
			}
		}

		for _, stage := range []string{"tools", "findings"} {
			priorPublication, _ := c.metaValue(t.Context(), "tool_rollup_publication")
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			resource := processResourceUsage()
			start := time.Now()
			if stage == "tools" {
				generation, _, day, _, e := c.toolRollupStale(t.Context())
				if e != nil {
					t.Fatal(e)
				}
				if incremental {
					err = c.ensureToolRollup(t.Context())
				} else {
					err = c.rebuildToolRollup(t.Context(), generation, day)
				}
			} else {
				err = c.RefreshFindings(t.Context(), true)
			}
			runtime.ReadMemStats(&after)
			end := processResourceUsage()
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("workload=%s pass=%d stage=%s wall=%.6f cpu=%.6f allocated=%d mallocs=%d rss=%d read_blocks=%d written_blocks=%d", workload, pass, stage, time.Since(start).Seconds(), end.cpu-resource.cpu, after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs, processRSS(), end.read-resource.read, end.written-resource.written)
			var writer string
			if stage == "tools" {
				writer, _ = c.metaValue(t.Context(), "tool_rollup_writer_ms")
				if publication, _ := c.metaValue(t.Context(), "tool_rollup_publication"); publication == priorPublication {
					writer = "not-published"
				}
				t.Logf("workload=%s writer_ms=%s", workload, writer)
			}
		}
		if incremental && workload == "first" {
			// Save derived tables for comparison with the unchanged baseline before
			// applying controlled updates. Native transcripts are never exported.
			for _, table := range []string{"tool_call_cube", "tool_usage_daily", "tool_mirror_workspaces", "findings", "finding_daily", "finding_aliases"} {
				if _, err := db.Exec("CREATE TABLE IF NOT EXISTS analysis_initial_" + table + " AS SELECT * FROM " + table); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if incremental && os.Getenv("PHAROS_ANALYSIS_PARITY") == "1" {
		tools, findings := canonicalToolOutput(t, c), canonicalFindings(t, c)
		generation, _, day, _, err := c.toolRollupStale(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err := c.rebuildToolRollup(t.Context(), generation, day); err != nil {
			t.Fatal(err)
		}
		if err := c.runFindingsPassReference(t.Context(), true); err != nil {
			t.Fatal(err)
		}
		if !same(tools, canonicalToolOutput(t, c)) {
			t.Fatal("large replica tool parity failed")
		}
		reference := canonicalFindings(t, c)
		if !same(findings, reference) {
			for table, before := range findings {
				after := reference[table]
				if len(before) != len(after) {
					t.Logf("%s rows: %d vs %d", table, len(before), len(after))
				}
				for i := 0; i < min(len(before), len(after)); i++ {
					for column, value := range before[i] {
						if !same(value, after[i][column]) {
							t.Logf("%s row=%d id=%v finding_id=%v column=%s before=%s after=%s", table, i, before[i]["id"], before[i]["finding_id"], column, jsonText(value), jsonText(after[i][column]))
						}
					}
				}
			}
			t.Fatal("large replica findings parity failed")
		}
		t.Log("large replica full-reference parity passed")
	}
	fmt.Fprintln(os.Stderr, "analysis replica completed")
}
