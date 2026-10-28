//go:build probe

// Measures what leaving tool output out of the word index costs, on a scratch
// copy of a real catalog. Never point these at a live catalog: both open it
// through OpenCatalog, which upgrades and writes it. Probes build only with
// the probe tag, so they stay out of go test ./...
//
// Replay real MCP search calls (exported from a catalog's mcp_calls as a JSON
// array of {id, tool_name, arguments_json}) and record each call's result
// identities, total, and the kinds of the matched messages:
//
//	PHAROS_REPLAY_CATALOG=/scratch/catalog.sqlite3 PHAROS_REPLAY_CALLS=calls.json \
//	  PHAROS_REPLAY_OUT=before.json go test -tags probe -run TestToolOutputSearchReplay -v ./internal/archive
//
// Run it before and after the migration and compare the two outputs. With
// PHAROS_REPLAY_BY_KIND set, it also counts the tool_result and metadata
// messages in each default-kinds search_messages total (slow: two more
// counts of every match).
//
// Time the migration (the tool-result deletes and the merge loop separately):
//
//	PHAROS_MIGRATE_CATALOG=/scratch/catalog.sqlite3 \
//	  go test -tags probe -run TestToolOutputIndexMigration -v -timeout 600m ./internal/archive

package archive

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func probeScratchCatalog(t *testing.T, variable string) *Catalog {
	path := os.Getenv(variable)
	if path == "" {
		t.Skip("set " + variable + " to a scratch copy of a catalog")
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(filepath.Dir(path)) == "/Volumes/euclid/Pharos" {
		t.Fatal("use a scratch copy, not the live catalog")
	}
	started := time.Now()
	catalog, err := OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { catalog.Close() })
	t.Logf("opened (and upgraded) in %s", time.Since(started).Round(time.Millisecond))
	return catalog
}

func TestToolOutputSearchReplay(t *testing.T) {
	catalog := probeScratchCatalog(t, "PHAROS_REPLAY_CATALOG")
	raw, err := os.ReadFile(os.Getenv("PHAROS_REPLAY_CALLS"))
	if err != nil {
		t.Fatal(err)
	}
	var calls []struct {
		ID        int64  `json:"id"`
		Tool      string `json:"tool_name"`
		Arguments string `json:"arguments_json"`
	}
	if err := json.Unmarshal(raw, &calls); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		ID      int64            `json:"id"`
		Tool    string           `json:"tool"`
		Error   string           `json:"error,omitempty"`
		Total   any              `json:"total,omitempty"`
		Items   []string         `json:"items"`
		Kinds   map[string]int   `json:"kinds"`
		ByKind  map[string]int64 `json:"by_kind,omitempty"`
		Elapsed int64            `json:"elapsed_ms"`
	}
	outcomes := []outcome{}
	for _, call := range calls {
		var args map[string]any
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		value, err := callMCPContext(context.Background(), catalog, call.Tool, args)
		result := outcome{ID: call.ID, Tool: call.Tool, Items: []string{}, Kinds: map[string]int{}, Elapsed: time.Since(started).Milliseconds()}
		if err != nil {
			result.Error = err.Error()
			outcomes = append(outcomes, result)
			continue
		}
		var generic map[string]any
		_ = json.Unmarshal([]byte(jsonText(value)), &generic)
		result.Total = generic["total"]
		if result.Total == nil {
			result.Total = generic["count"]
		}
		items, _ := generic["items"].([]any)
		for _, entry := range items {
			item, _ := entry.(map[string]any)
			id := firstString(item["message_id"])
			if id != "" {
				var kind string
				_ = catalog.reader().QueryRow("SELECT kind FROM messages WHERE id=?", id).Scan(&kind)
				result.Kinds[kind]++
			}
			for _, key := range []string{"conversation_id", "workspace_id"} {
				if id == "" {
					id = firstString(item[key])
				}
			}
			result.Items = append(result.Items, id)
		}
		// What a default-kinds search_messages total holds of each kind.
		if call.Tool == "search_messages" && args["kinds"] == nil && os.Getenv("PHAROS_REPLAY_BY_KIND") != "" {
			result.ByKind = map[string]int64{}
			for _, kind := range []string{"tool_result", "metadata"} {
				scoped := map[string]any{}
				for key, value := range args {
					scoped[key] = value
				}
				scoped["kinds"] = []any{kind}
				if value, err := catalog.searchMessages(scoped); err == nil {
					result.ByKind[kind] = integer(value["total"])
				}
			}
		}
		outcomes = append(outcomes, result)
		t.Logf("%d %s: %v items=%d %dms %s", call.ID, call.Tool, result.Total, len(result.Items), result.Elapsed, result.Error)
	}
	if err := os.WriteFile(os.Getenv("PHAROS_REPLAY_OUT"), []byte(jsonText(outcomes)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// wordIndexTables are the tables whose sizes the migration changes.
var wordIndexTables = []string{"messages_fts_data", "messages_fts_idx", "messages_fts_content", "messages_fts_docsize", "messages_fts_config",
	"message_fts_rows", "sqlite_autoindex_message_fts_rows_1", "sqlite_autoindex_message_fts_rows_2",
	"messages_trigram_data", "messages_trigram_idx", "messages_trigram_docsize", "messages_trigram_config"}

func probeTableSizes(t *testing.T, catalog *Catalog) map[string]int64 {
	sizes := map[string]int64{}
	for _, name := range wordIndexTables {
		started := time.Now()
		var size int64
		if err := catalog.DB.QueryRow("SELECT COALESCE(SUM(pgsize),0) FROM dbstat WHERE name=?", name).Scan(&size); err != nil {
			t.Fatal(err)
		}
		sizes[name] = size
		t.Logf("dbstat %s: %.2f GB (%s)", name, float64(size)/1e9, time.Since(started).Round(time.Millisecond))
	}
	var pages, free, pageSize int64
	catalog.DB.QueryRow("PRAGMA page_count").Scan(&pages)
	catalog.DB.QueryRow("PRAGMA freelist_count").Scan(&free)
	catalog.DB.QueryRow("PRAGMA page_size").Scan(&pageSize)
	sizes["file"], sizes["freelist"] = pages*pageSize, free*pageSize
	t.Logf("file %.2f GB, free pages %.2f GB", float64(pages*pageSize)/1e9, float64(free*pageSize)/1e9)
	return sizes
}

func TestToolOutputIndexMigration(t *testing.T) {
	catalog := probeScratchCatalog(t, "PHAROS_MIGRATE_CATALOG")
	ctx := context.Background()
	before := probeTableSizes(t, catalog)
	var mapped, toolResults int64
	if err := catalog.DB.QueryRow(`SELECT COUNT(*),SUM(m.kind='tool_result') FROM message_fts_rows r JOIN messages m ON m.id=r.message_id`).Scan(&mapped, &toolResults); err != nil {
		t.Fatal(err)
	}
	t.Logf("word index rows %d, of them tool results %d", mapped, toolResults)
	execTx(t, catalog.DB, "DELETE FROM meta WHERE key IN ('message_fts_tool_output_removed','message_fts_tool_output_merged','message_fts_tool_output_cursor')")
	timed := func(step func(context.Context) (bool, error)) (time.Duration, int, time.Duration) {
		started, steps, slowest := time.Now(), 0, time.Duration(0)
		for {
			at := time.Now()
			more, err := step(ctx)
			if err != nil {
				t.Fatal(err)
			}
			slowest = max(slowest, time.Since(at))
			if !more {
				return time.Since(started), steps, slowest
			}
			steps++
			if steps%500 == 0 {
				t.Logf("  %d steps, %s", steps, time.Since(started).Round(time.Second))
			}
		}
	}
	removal, batches, slowestBatch := timed(catalog.removeToolOutputBatch)
	t.Logf("removal: %s in %d batches of %d rows, slowest %s", removal.Round(time.Second), batches, toolOutputScan, slowestBatch.Round(time.Millisecond))
	_ = catalog.Checkpoint()
	middle := probeTableSizes(t, catalog)
	merge, steps, slowestStep := timed(catalog.mergeWordIndexStep)
	t.Logf("merge: %s in %d steps of %d pages, slowest %s", merge.Round(time.Second), steps, toolOutputMergePages, slowestStep.Round(time.Millisecond))
	_ = catalog.Checkpoint()
	after := probeTableSizes(t, catalog)
	for _, name := range append(wordIndexTables, "file", "freelist") {
		t.Logf("| %s | %.2f | %.2f | %.2f |", name, float64(before[name])/1e9, float64(middle[name])/1e9, float64(after[name])/1e9)
	}
}
