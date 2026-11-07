//go:build probe

// Read-only fingerprint inputs for the standalone compact-rendering prototype.
// No Initialize, migrations, inference, background work or catalog writes.
// PHAROS_FINGERPRINT_CATALOG=/path/catalog.sqlite3 \
// PHAROS_FINGERPRINT_MANIFEST=/path/manifest.json \
// PHAROS_FINGERPRINT_OUTPUT=/path/output \
// go test -tags probe -run '^TestCompactFingerprintExport$' -v ./internal/archive
package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestCompactFingerprintExport(t *testing.T) {
	path, manifest, output := os.Getenv("PHAROS_FINGERPRINT_CATALOG"), os.Getenv("PHAROS_FINGERPRINT_MANIFEST"), os.Getenv("PHAROS_FINGERPRINT_OUTPUT")
	if path == "" || manifest == "" || output == "" {
		t.Skip("set the three PHAROS_FINGERPRINT_* paths to export prototype inputs")
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var samples []struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Population bool   `json:"population"`
	}
	if err := json.Unmarshal(data, &samples); err != nil {
		t.Fatal(err)
	}
	u := &url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", u.String()+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	catalog := &Catalog{DB: db, Path: path, readOnly: true, queryOnly: true}
	server := &Server{Catalog: catalog}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	// The study needs full histories. The product endpoint retains its limit.
	override(t, &messageLabelFingerprintLimit, 1000000)
	for i, sample := range samples {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		request := httptest.NewRequest("GET", "/api/labels/conversations/"+url.PathEscape(sample.ID), nil).WithContext(ctx)
		response := httptest.NewRecorder()
		server.getMessageLabelFingerprint(response, request)
		if response.Code != 200 {
			cancel()
			t.Fatalf("sample %s: %d %s", sample.ID, response.Code, response.Body.String())
		}
		var value map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
			cancel()
			t.Fatal(err)
		}
		tools, err := queryMapsContext(ctx, db, `SELECT id,COALESCE(agent_session_id,?) session_id,call_message_id message_id,tool_name name,tool_category category,command_category,status,started_at start,ended_at end,duration_ms FROM tool_calls WHERE conversation_id=? ORDER BY sequence,id`, sample.ID, sample.ID)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		conversation := value["conversation"].(map[string]any)
		conversation["activity"] = tools
		value["study"] = map[string]any{"name": sample.Name, "population": sample.Population, "synthetic": false, "full_history": value["truncated"] != true, "read_ms": time.Since(start).Milliseconds()}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(output, sample.ID+".json"), encoded, 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("%d/%d %s: %d messages, %d events, %d tools, %s", i+1, len(samples), sample.Name, len(conversation["messages"].([]any)), len(value["agent_events"].([]any)), len(tools), time.Since(start))
	}
}

// Profile 100 distinct real conversations. The source catalog is opened strictly
// read-only, without Initialize. PNG publication uses the production transaction
// against a disposable test catalog, never the source library.
// PHAROS_FINGERPRINT_CATALOG=/absolute/catalog.sqlite3 \
// PHAROS_FINGERPRINT_BATCH=/absolute/manifest.json \
// PHAROS_FINGERPRINT_BATCH_OUTPUT=/absolute/output \
// go test -tags probe -run '^TestCompactFingerprintBatch$' -v ./internal/archive
// The manifest is [{"id":"..."},...] with exactly 100 distinct IDs. First run
// saves frozen inputs; repeats reuse them. Add PHAROS_FINGERPRINT_PROFILE=1 for
// cpu.pprof. The renderer is entirely native Go.
// PHAROS_FINGERPRINT_WORKERS=1..4 measures independent renderer concurrency.
// PHAROS_FINGERPRINT_DURABLE=1 uses production drive flushes. Optional
// PHAROS_FINGERPRINT_CACHE_ROOT creates and removes an owned temporary cache
// directory on that volume; it never opens that volume's real library writable.
func TestCompactFingerprintBatch(t *testing.T) {
	manifest, output := os.Getenv("PHAROS_FINGERPRINT_BATCH"), os.Getenv("PHAROS_FINGERPRINT_BATCH_OUTPUT")
	if manifest == "" || output == "" {
		t.Skip("set PHAROS_FINGERPRINT_BATCH and PHAROS_FINGERPRINT_BATCH_OUTPUT")
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var samples []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &samples); err != nil {
		t.Fatal(err)
	}
	if len(samples) != 100 {
		t.Fatal("batch must contain 100 conversations")
	}
	seen := map[string]bool{}
	for _, sample := range samples {
		if sample.ID == "" || seen[sample.ID] {
			t.Fatal("duplicate or empty ID")
		}
		seen[sample.ID] = true
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	inputs := filepath.Join(filepath.Dir(manifest), "inputs")
	if err := os.MkdirAll(inputs, 0700); err != nil {
		t.Fatal(err)
	}
	var source *Catalog
	if path := os.Getenv("PHAROS_FINGERPRINT_CATALOG"); path != "" {
		u := &url.URL{Scheme: "file", Path: path}
		db, err := sql.Open("sqlite", u.String()+"?mode=ro&_pragma=query_only(1)")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		db.SetMaxOpenConns(1)
		source = &Catalog{DB: db, Path: path, readOnly: true, queryOnly: true}
	}
	if os.Getenv("PHAROS_FINGERPRINT_DURABLE") == "1" {
		useDurableCatalog(t)
	}
	var cache *Catalog
	if root := os.Getenv("PHAROS_FINGERPRINT_CACHE_ROOT"); root != "" {
		dir, err := os.MkdirTemp(root, "pharos-fingerprint-profile-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.RemoveAll(dir); err != nil {
				t.Error(err)
			}
		})
		cache = openTestCatalog(t, filepath.Join(dir, "catalog.sqlite3"))
	} else {
		cache, _ = testCatalog(t)
	}
	seed := seedTx(t, cache.DB)
	for _, sample := range samples {
		if _, err := seed.Exec(`INSERT INTO fingerprint_entities(entity,id,revision) VALUES('conversation',?,'probe') ON CONFLICT(entity,id) DO UPDATE SET revision='probe',dirty=1`, sample.ID); err != nil {
			t.Fatal(err)
		}
	}
	commitSeed(t, seed)
	workers := 1
	if value := os.Getenv("PHAROS_FINGERPRINT_WORKERS"); value != "" {
		workers, err = strconv.Atoi(value)
		if err != nil || workers < 1 || workers > 4 {
			t.Fatal("workers must be 1-4")
		}
	}
	renderers := make([]*fingerprintRenderer, workers)
	for i := range renderers {
		renderers[i], err = newFingerprintRenderer()
		if err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv("PHAROS_FINGERPRINT_PROFILE") == "1" {
		cpu, err := os.Create(filepath.Join(output, "cpu.pprof"))
		if err != nil {
			t.Fatal(err)
		}
		if err := pprof.StartCPUProfile(cpu); err != nil {
			t.Fatal(err)
		}
		defer func() { pprof.StopCPUProfile(); cpu.Close() }()
	}
	type timing struct {
		ID                             string  `json:"id"`
		LoadMS                         float64 `json:"load_ms"`
		RenderMS                       float64 `json:"render_ms"`
		PublishMS                      float64 `json:"publish_ms"`
		Messages, Events, Tools, Bytes int
		Hashes                         map[string]string `json:"hashes"`
	}
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	timings := make([]timing, len(samples))
	started := time.Now()
	run := func(i int, renderer *fingerprintRenderer) timing {
		sample := samples[i]
		var err error
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		at := time.Now()
		path := filepath.Join(inputs, sample.ID+".json")
		var input map[string]any
		if data, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(data, &input); err != nil {
				t.Fatal(err)
			}
		} else {
			if source == nil {
				t.Fatal("no frozen input or source catalog", sample.ID)
			}
			input, err = source.messageLabelFingerprintInput(ctx, sample.ID, false, 0, true)
			if err != nil {
				t.Fatal(sample.ID, err)
			}
			input["events"] = input["agent_events"]
			data, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		loaded := time.Since(at)
		at = time.Now()
		result, err := renderer.Render(ctx, input, 1)
		if err != nil {
			t.Fatal(sample.ID, err)
		}
		rendered := time.Since(at)
		at = time.Now()
		if err := cache.publishFingerprintImages(ctx, "conversation", sample.ID, "probe", result); err != nil {
			t.Fatal(err)
		}
		published := time.Since(at)
		cancel()
		row := timing{ID: sample.ID, LoadMS: ms(loaded), RenderMS: ms(rendered), PublishMS: ms(published), Messages: result.Messages, Events: result.Events, Tools: result.Tools, Hashes: map[string]string{}}
		for _, image := range result.Images {
			row.Hashes[image.Profile+"-"+image.Theme] = image.Hash
			row.Bytes += len(image.PNG)
		}
		t.Logf("%d/100: messages=%d events=%d tools=%d read=%.1fms render=%.1fms publish=%.1fms", i+1, row.Messages, row.Events, row.Tools, row.LoadMS, row.RenderMS, row.PublishMS)
		return row
	}
	jobs := make(chan int, len(samples))
	for i := range samples {
		jobs <- i
	}
	close(jobs)
	var group sync.WaitGroup
	for _, renderer := range renderers {
		group.Add(1)
		go func() {
			defer group.Done()
			for i := range jobs {
				timings[i] = run(i, renderer)
			}
		}()
	}
	group.Wait()
	if t.Failed() {
		t.FailNow()
	}
	total := time.Since(started)
	var stored, pending int
	if err := cache.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM fingerprint_images WHERE revision='probe'),COALESCE(SUM(dirty),0) FROM fingerprint_entities WHERE revision='probe'`).Scan(&stored, &pending); err != nil || stored != 400 || pending != 0 {
		t.Fatalf("batch publication incomplete: images=%d pending=%d %v", stored, pending, err)
	}
	var load, render, publish time.Duration
	for _, row := range timings {
		load += time.Duration(row.LoadMS * float64(time.Millisecond))
		render += time.Duration(row.RenderMS * float64(time.Millisecond))
		publish += time.Duration(row.PublishMS * float64(time.Millisecond))
	}
	sorted := append([]timing(nil), timings...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].RenderMS < sorted[j].RenderMS })
	report := map[string]any{"count": 100, "workers": workers, "durable": os.Getenv("PHAROS_FINGERPRINT_DURABLE") == "1", "cache_root": os.Getenv("PHAROS_FINGERPRINT_CACHE_ROOT"), "elapsed_ms": ms(total), "load_ms": ms(load), "render_ms": ms(render), "publish_ms": ms(publish), "median_render_ms": sorted[49].RenderMS, "p95_render_ms": sorted[94].RenderMS, "items": timings}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, "results.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("100 conversations, 400 PNGs: elapsed=%s read=%s render=%s publish=%s p50=%.1fms p95=%.1fms", total, load, render, publish, sorted[49].RenderMS, sorted[94].RenderMS)
}

// PHAROS_FINGERPRINT_INPUT points to a previously exported input, never a live
// catalog. Optional PHAROS_FINGERPRINT_NATIVE_OUTPUT saves the encoded PNGs.
func TestCompactFingerprintNativeRender(t *testing.T) {
	input := os.Getenv("PHAROS_FINGERPRINT_INPUT")
	if input == "" {
		t.Skip("set PHAROS_FINGERPRINT_INPUT")
	}
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	value["events"] = value["agent_events"]
	renderer, err := newFingerprintRenderer()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := renderer.Render(ctx, value, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d messages, %d events: %d PNGs in %s", result.Messages, result.Events, len(result.Images), time.Since(start))
	if output := os.Getenv("PHAROS_FINGERPRINT_NATIVE_OUTPUT"); output != "" {
		if err := os.MkdirAll(output, 0700); err != nil {
			t.Fatal(err)
		}
		for _, image := range result.Images {
			if err := os.WriteFile(filepath.Join(output, image.Profile+"-"+image.Theme+".png"), image.PNG, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
