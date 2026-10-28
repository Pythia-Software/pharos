//go:build probe

// Measures what leaving tool output out of the word index costs, on a scratch
// copy of a real catalog. Both open it through OpenCatalog, which upgrades
// and writes it, so they refuse a catalog outside PHAROS_PROBE_SCRATCH, a
// scratch directory the caller names; that directory may be neither a backup
// nor a library, and on the euclid drive (any path under /Volumes/euclid,
// compared ignoring case) nothing else is accepted. Probes build only with the
// probe tag, so they stay out of go test ./...
//
// Replay real MCP search calls (exported from a catalog's mcp_calls as a JSON
// array of {id, tool_name, arguments_json}) and record each call's result
// identities, total, and the kinds of the matched messages, a JSON line each
// (PHAROS_REPLAY_SHARD=i/n splits the calls across processes):
//
//	PHAROS_PROBE_SCRATCH=/scratch PHAROS_REPLAY_CATALOG=/scratch/catalog.sqlite3 PHAROS_REPLAY_CALLS=calls.json \
//	  PHAROS_REPLAY_OUT=before.json go test -tags probe -run TestToolOutputSearchReplay -v ./internal/archive
//
// Run it before and after the migration and compare the two outputs. With
// PHAROS_REPLAY_BY_KIND set, it also counts the tool_result and metadata
// messages in each default-kinds search_messages total (slow: two more
// counts of every match).
//
// Time the migration (the tool-result deletes and the merge loop separately):
//
//	PHAROS_PROBE_SCRATCH=/scratch PHAROS_MIGRATE_CATALOG=/scratch/catalog.sqlite3 \
//	  go test -tags probe -run TestToolOutputIndexMigration -v -timeout 600m ./internal/archive

package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func probeScratchCatalog(t *testing.T, variable string) *Catalog {
	path := os.Getenv(variable)
	if path == "" {
		t.Skip("set " + variable + " to a scratch copy of a catalog")
	}
	if err := probeScratchPath(os.Getenv("PHAROS_PROBE_SCRATCH"), path); err != nil {
		t.Fatal(err)
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

// probeScratchPath refuses a catalog that is not inside scratch, and a
// scratch directory that is a backup or a library.
func probeScratchPath(scratch, path string) error {
	if scratch == "" {
		return fmt.Errorf("set PHAROS_PROBE_SCRATCH to the scratch directory holding the copy")
	}
	resolve := func(value string) (string, error) {
		absolute, err := filepath.Abs(value)
		if err != nil {
			return "", err
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		return strings.ToLower(filepath.Clean(resolved)), err
	}
	scratch, err := resolve(scratch)
	if err != nil {
		return err
	}
	path, err = resolve(path)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(path, scratch+string(filepath.Separator)) {
		return fmt.Errorf("%s is not inside the scratch directory %s", path, scratch)
	}
	if parts := strings.Split(scratch, string(filepath.Separator)); len(parts) <= 3 && strings.HasPrefix(scratch, "/volumes/") {
		return fmt.Errorf("the scratch directory %s is a whole drive", scratch)
	}
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if strings.Contains(part, "backup") || part == "pharos" {
			return fmt.Errorf("%s looks like a backup or a library; copy the catalog to a scratch directory", path)
		}
	}
	if _, err := os.Stat(filepath.Join(scratch, libraryConfigName)); err == nil {
		return fmt.Errorf("the scratch directory %s holds a library", scratch)
	}
	return nil
}

func TestProbeScratchPathGuard(t *testing.T) {
	scratch := t.TempDir()
	if err := os.WriteFile(filepath.Join(scratch, "catalog.sqlite3"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := probeScratchPath(scratch, filepath.Join(scratch, "catalog.sqlite3")); err != nil {
		t.Fatal(err)
	}
	for _, refused := range [][2]string{{"", filepath.Join(scratch, "catalog.sqlite3")}, {scratch, "/Volumes/euclid/Pharos/catalog/catalog.sqlite3"},
		{scratch, "/Volumes/Euclid/pharos/catalog/catalog.sqlite3"}, {"/Volumes/euclid", "/Volumes/euclid/Pharos Backup Tuesday October 27th 7AM/catalog/catalog.sqlite3"}} {
		if probeScratchPath(refused[0], refused[1]) == nil {
			t.Errorf("accepted %q in %q", refused[1], refused[0])
		}
	}
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
	// Each outcome is a JSON line, so a stopped replay resumes; a shard i/n
	// replays the calls whose id is i modulo n, into its own file.
	out := os.Getenv("PHAROS_REPLAY_OUT")
	shard, shards := int64(0), int64(1)
	if value := os.Getenv("PHAROS_REPLAY_SHARD"); value != "" {
		fmt.Sscanf(value, "%d/%d", &shard, &shards)
		out += fmt.Sprintf(".%d", shard)
	}
	done := map[int64]bool{}
	if previous, err := os.ReadFile(out); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(previous)), "\n") {
			var recorded outcome
			if json.Unmarshal([]byte(line), &recorded) == nil {
				done[recorded.ID] = true
			}
		}
	}
	file, err := os.OpenFile(out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, call := range calls {
		if done[call.ID] || call.ID%shards != shard {
			continue
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		value, err := callMCPContext(context.Background(), catalog, call.Tool, args)
		result := outcome{ID: call.ID, Tool: call.Tool, Items: []string{}, Kinds: map[string]int{}, Elapsed: time.Since(started).Milliseconds()}
		if err != nil {
			result.Error = err.Error()
			fmt.Fprintln(file, jsonText(result))
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
		fmt.Fprintln(file, jsonText(result))
		t.Logf("%d %s: %v items=%d %dms %s", call.ID, call.Tool, result.Total, len(result.Items), result.Elapsed, result.Error)
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
	// PHAROS_MIGRATE_SKIP_BEFORE skips the first measurements, for a rerun.
	before := map[string]int64{}
	if os.Getenv("PHAROS_MIGRATE_SKIP_BEFORE") == "" {
		before = probeTableSizes(t, catalog)
		var mapped, toolResults int64
		if err := catalog.DB.QueryRow(`SELECT COUNT(*),SUM(m.kind='tool_result') FROM message_fts_rows r JOIN messages m ON m.id=r.message_id`).Scan(&mapped, &toolResults); err != nil {
			t.Fatal(err)
		}
		t.Logf("word index rows %d, of them tool results %d", mapped, toolResults)
	}
	execTx(t, catalog.DB, "DELETE FROM meta WHERE key IN ('message_fts_tool_output_removed','message_fts_tool_output_merged','message_fts_tool_output_cursor')")
	migration := newToolOutputMigration()
	var slowestRemoval, slowestTidy, slowestMerge time.Duration
	started, batches := time.Now(), 0
	for more := true; more; batches++ {
		var held time.Duration
		var err error
		if more, held, err = catalog.removeToolOutputBatch(ctx, migration.removal.next()); err != nil {
			t.Fatal(err)
		}
		migration.removal.held(held)
		slowestRemoval = max(slowestRemoval, held)
		if !more {
			break
		}
		if _, held, err = catalog.mergeWordIndexStep(ctx, migration.merge.next(), false); err != nil {
			t.Fatal(err)
		}
		migration.merge.held(held)
		slowestTidy = max(slowestTidy, held)
		if batches%500 == 0 {
			t.Logf("  %d batches, %s, %d rows, %d pages", batches, time.Since(started).Round(time.Second), migration.removal.next(), migration.merge.next())
		}
	}
	removal := time.Since(started)
	t.Logf("removal: %s in %d batches, each with a merge step; slowest removal held the writer %s, slowest merge step %s", removal.Round(time.Second), batches, slowestRemoval.Round(time.Millisecond), slowestTidy.Round(time.Millisecond))
	_ = catalog.Checkpoint()
	middle := probeTableSizes(t, catalog)
	started, steps := time.Now(), 0
	for more := true; more; steps++ {
		var held time.Duration
		var err error
		if more, held, err = catalog.mergeWordIndexStep(ctx, migration.merge.next(), true); err != nil {
			t.Fatal(err)
		}
		migration.merge.held(held)
		slowestMerge = max(slowestMerge, held)
		if steps%500 == 0 {
			t.Logf("  %d merge steps, %s, %d pages", steps, time.Since(started).Round(time.Second), migration.merge.next())
		}
	}
	t.Logf("merge: %s in %d steps, slowest held the writer %s", time.Since(started).Round(time.Second), steps, slowestMerge.Round(time.Millisecond))
	_ = catalog.Checkpoint()
	after := probeTableSizes(t, catalog)
	for _, name := range append(wordIndexTables, "file", "freelist") {
		t.Logf("| %s | %.2f | %.2f | %.2f |", name, float64(before[name])/1e9, float64(middle[name])/1e9, float64(after[name])/1e9)
	}
}
