//go:build probe

package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestWorkReplica measures real domain work on an explicitly prepared arm. It
// opens no installed library, serves no network port and starts no service
// schedules. Compile once with `go test -c -tags probe ./internal/archive`.
//
//	PHAROS_WORK_REPLICA=/.../arm PHAROS_WORK_DESCRIPTOR=/.../workload.json \
//	  archive.test -test.run '^TestWorkReplica$' -test.timeout 24h -test.v
//
// Preparation: python3 tools/work-experiment.py prepare --completed-backup ... --master ...
func TestWorkReplica(t *testing.T) {
	root := os.Getenv("PHAROS_WORK_REPLICA")
	if root == "" {
		t.Skip("set PHAROS_WORK_REPLICA to a freshly prepared experiment arm")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || root != resolved || root == "/Volumes/euclid/Pharos" {
		t.Fatalf("unsafe replica directory: %v", err)
	}
	var marker struct {
		Version int    `json:"version"`
		Role    string `json:"role"`
	}
	raw, err := os.ReadFile(filepath.Join(root, ".pharos-work-experiment.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &marker); err != nil || marker.Version != 2 || marker.Role != "arm" {
		t.Fatal("not a prepared experiment arm")
	}
	var descriptor struct {
		Name    string   `json:"name"`
		Clock   string   `json:"clock"`
		Stages  []string `json:"stages"`
		Product struct {
			Route       string          `json:"route"`
			Query       json.RawMessage `json:"query"`
			LatencyMS   float64         `json:"latency_ms"`
			Consecutive int             `json:"consecutive"`
			Expected    string          `json:"expected_sha256"`
			MinRows     *int            `json:"min_expected_rows"`
		} `json:"product"`
		// Read-only queries per stage whose result must be empty once that
		// stage's required work has settled.
		Pending map[string][]string `json:"pending_checks"`
	}
	raw, err = os.ReadFile(os.Getenv("PHAROS_WORK_DESCRIPTOR"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &descriptor); err != nil {
		t.Fatal(err)
	}
	clock, err := time.Parse(time.RFC3339, descriptor.Clock)
	if err != nil {
		t.Fatal("workload must pin its calendar clock")
	}
	if len(descriptor.Stages) == 0 || descriptor.Product.LatencyMS <= 0 || descriptor.Product.Consecutive < 1 {
		t.Fatal("pin stages, product latency and observation window before running")
	}
	if descriptor.Product.Route != "/api/query/message_labels/query-v3" {
		t.Fatal("first product gate is pinned to the real message-label query route")
	}
	// An empty table can be a correct answer, so the descriptor says how many
	// rows the pinned surface has; the busy recent 48 hours pins at least one.
	if descriptor.Product.MinRows == nil || *descriptor.Product.MinRows < 0 {
		t.Fatal("pin product.min_expected_rows")
	}
	for _, stage := range descriptor.Stages {
		if len(descriptor.Pending[stage]) == 0 {
			t.Fatalf("stage %q needs pending_checks: returning is not settlement", stage)
		}
		for _, query := range descriptor.Pending[stage] {
			if !readOnlyCheck(query) {
				t.Fatalf("stage %q: a pending check is one SELECT or WITH statement", stage)
			}
		}
	}
	config, err := loadConfig(filepath.Join(root, "archive.toml"), false)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(config.CatalogPath) != filepath.Join(root, "catalog/catalog.sqlite3") || len(config.Sources) > 0 {
		t.Fatal("domain probe requires a private catalog and no external source declarations")
	}
	// Opening initializes/migrates the catalog: startup is measured apart
	// from the work it precedes, so a fixed cost cannot dilute a stage.
	opening, openUsage := time.Now(), processResourceUsage()
	c, err := OpenCatalog(config.CatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.evidenceOnly = true
	c.now = func() time.Time { return clock }
	server := testServer(t, config, c)
	checks, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: config.CatalogPath}).String()+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer checks.Close()
	opened := processResourceUsage()
	startup := map[string]any{"wall_seconds": time.Since(opening).Seconds(), "cpu_seconds": opened.cpu - openUsage.cpu}
	started, before := time.Now(), opened

	type observation struct {
		Seconds   float64 `json:"seconds"`
		LatencyMS float64 `json:"latency_ms"`
		Status    int     `json:"status"`
		Rows      int     `json:"rows"`
		Digest    string  `json:"sha256,omitempty"`
		Invalid   string  `json:"invalid,omitempty"`
	}
	var mu sync.Mutex
	observations := []observation{}
	// Runs on the polling goroutine too, so it reports errors, never t.Fatal.
	observe := func() {
		at := time.Now()
		o := observation{}
		status, rows, err := probeProductRows(server, config.APIToken, descriptor.Product.Route, descriptor.Product.Query)
		o.Seconds, o.LatencyMS, o.Status = time.Since(started).Seconds(), float64(time.Since(at).Microseconds())/1000, status
		if err == nil {
			// The oracle is the UI page's actual rows, not the projection queue
			// and not duration/diagnostic metadata. No values leave this process.
			var data []byte
			data, err = json.Marshal(rows)
			hash := sha256.Sum256(data)
			o.Rows, o.Digest = len(rows), hex.EncodeToString(hash[:])
		}
		if err != nil {
			o.Invalid = err.Error()
		}
		mu.Lock()
		observations = append(observations, o)
		mu.Unlock()
	}
	observe()
	pollCtx, stopPoll := context.WithCancel(t.Context())
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				observe()
			}
		}
	}()
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			stopPoll()
			<-pollDone
		}
	}
	defer stop()

	// This probe calls the classic domain methods directly.
	engine := map[string]any{"value": "classic", "basis": "probe invokes classic domain methods"}
	report := map[string]any{"version": 2, "name": descriptor.Name, "clock": descriptor.Clock, "engine_actual": engine,
		"startup": startup, "settled": false, "resource_basis": syncMeasurementLimits}
	save := func() {
		mu.Lock()
		report["observations"] = append([]observation(nil), observations...)
		mu.Unlock()
		data, err := json.MarshalIndent(report, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(root, "probe-report.json"), append(data, '\n'), 0o600)
		}
		if err != nil {
			t.Error(err)
		}
	}
	// A failed stage still leaves its private report, unsettled.
	fail := func(format string, args ...any) {
		stop()
		report["failure"] = fmt.Sprintf(format, args...)
		save()
		t.Fatalf(format, args...)
	}
	stages := []map[string]any{}
	report["stages"] = &stages
	for _, stage := range descriptor.Stages {
		at, usage := time.Now(), processResourceUsage()
		var err error
		ctx := t.Context()
		switch stage {
		case "timing":
			for {
				var n int
				n, err = c.refreshParallelism(ctx, parallelismBatch, true)
				if err != nil || n == 0 {
					break
				}
			}
		case "tools":
			err = c.ensureToolRollup(ctx)
		case "identities":
			_, err = c.ReconcileIdentitiesContext(ctx)
		case "authorship-estimates":
			for {
				err = c.UpdateAuthorship(ctx, formatTime(clock))
				if err != nil || !c.authorshipQuickPending() {
					break
				}
			}
		case "authorship-exact":
			err = c.refineAuthorship(ctx, false)
		case "labels":
			var stats *LabelsRunStats
			stats, err = c.RunLabels(ctx, config.Labels, LabelsRunOptions{})
			if err == nil && !stats.Complete {
				err = fmt.Errorf("labels returned before full settlement")
			}
		case "agent-events":
			_, err = c.RunAgentEvents(ctx, config.Labels, config.AgentEvents, AgentEventsRunOptions{})
		case "label-facts":
			for {
				var n int
				n, err = c.refreshMessageLabelFacts(ctx)
				if err != nil || n == 0 {
					break
				}
			}
		case "findings":
			err = c.RefreshFindings(ctx, true)
		default:
			fail("unclassified experiment stage %q", stage)
		}
		if err != nil {
			fail("%s: %v", stage, err)
		}
		// Independent of what the method returned: its required work is done.
		for index, query := range descriptor.Pending[stage] {
			if pending, err := checkPending(ctx, checks, query); err != nil || pending {
				fail("%s: pending check %d has not settled (%v)", stage, index, err)
			}
		}
		after := processResourceUsage()
		stages = append(stages, map[string]any{"stage": stage, "wall_seconds": time.Since(at).Seconds(), "cpu_seconds": after.cpu - usage.cpu, "read_blocks": after.read - usage.read, "written_blocks": after.written - usage.written})
	}
	settled := time.Since(started).Seconds()
	after := processResourceUsage()
	stop()
	// Ensure a short run still has the pinned number of product observations.
	// These post-settlement queries are outside execution resource/wall totals.
	for range descriptor.Product.Consecutive {
		observe()
	}
	mu.Lock()
	snapshots := append([]observation(nil), observations...)
	mu.Unlock()
	var correct, usable *float64
	consecutive := 0
	for _, o := range snapshots {
		valid := o.Status == http.StatusOK && o.Invalid == "" && o.Rows >= *descriptor.Product.MinRows &&
			descriptor.Product.Expected != "" && o.Digest == descriptor.Product.Expected
		if valid && correct == nil {
			value := o.Seconds
			correct = &value
		}
		if valid && o.LatencyMS <= descriptor.Product.LatencyMS {
			consecutive++
		} else {
			consecutive = 0
		}
		if usable == nil && consecutive >= descriptor.Product.Consecutive {
			value := o.Seconds
			usable = &value
		}
	}
	last := snapshots[len(snapshots)-1]
	if last.Invalid != "" || last.Rows < *descriptor.Product.MinRows {
		fail("the settled product query is invalid or has fewer than the pinned rows: %s", last.Invalid)
	}
	results := map[string]any{"t_correct": correct, "t_usable": usable, "t_settled": settled, "settled": true,
		"cpu_seconds": after.cpu - before.cpu, "read_blocks": after.read - before.read, "written_blocks": after.written - before.written,
		"final_product_sha256": last.Digest, "final_product_rows": last.Rows}
	for key, value := range results {
		report[key] = value
	}
	save()
	fmt.Printf("work replica %s: settled=%.3fs cpu=%.3fs observations=%d; private report saved\n", descriptor.Name, settled, after.cpu-before.cpu, len(snapshots))
}

// probeProductRows posts one product query and decodes its rows. Anything
// but a 200 version-3 result with a rows array of objects is an error.
func probeProductRows(server *Server, token, route string, query json.RawMessage) (int, []map[string]any, error) {
	request := httptest.NewRequest(http.MethodPost, route, bytes.NewReader(query))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		return response.Code, nil, fmt.Errorf("status %d", response.Code)
	}
	var body struct {
		Version int               `json:"version"`
		Rows    *[]map[string]any `json:"rows"`
	}
	decoder := json.NewDecoder(response.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil {
		return response.Code, nil, fmt.Errorf("undecodable result: %w", err)
	}
	if body.Version != 3 || body.Rows == nil {
		return response.Code, nil, errors.New("not a version-3 result with rows")
	}
	for _, row := range *body.Rows {
		if row == nil {
			return response.Code, nil, errors.New("a null row")
		}
	}
	return response.Code, *body.Rows, nil
}

// readOnlyCheck admits one SELECT or WITH statement. The connection is also
// read-only; refusing a second statement keeps ATTACH out of the probe.
func readOnlyCheck(query string) bool {
	query = strings.TrimSuffix(strings.TrimSpace(query), ";")
	words := strings.Fields(query)
	return len(words) > 0 && (strings.EqualFold(words[0], "SELECT") || strings.EqualFold(words[0], "WITH")) && !strings.Contains(query, ";")
}

func checkPending(ctx context.Context, db *sql.DB, query string) (bool, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	pending := rows.Next()
	return pending, rows.Err()
}
