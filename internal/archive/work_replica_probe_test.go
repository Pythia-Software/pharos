//go:build probe

package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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
	if err := json.Unmarshal(raw, &marker); err != nil || marker.Version != 1 || marker.Role != "arm" {
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
		} `json:"product"`
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
	config, err := loadConfig(filepath.Join(root, "archive.toml"), false)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(config.CatalogPath) != filepath.Join(root, "catalog/catalog.sqlite3") || len(config.Sources) > 0 {
		t.Fatal("domain probe requires a private catalog and no external source declarations")
	}
	started := time.Now()
	before := processResourceUsage()
	c, err := OpenCatalog(config.CatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.evidenceOnly = true
	c.now = func() time.Time { return clock }
	server := testServer(t, config, c)
	type observation struct {
		Seconds   float64 `json:"seconds"`
		LatencyMS float64 `json:"latency_ms"`
		Status    int     `json:"status"`
		Digest    string  `json:"sha256"`
	}
	var mu sync.Mutex
	observations := []observation{}
	observe := func() {
		at := time.Now()
		response := apiCall(t, server, config.APIToken, http.MethodPost, descriptor.Product.Route, string(descriptor.Product.Query))
		// The oracle is the UI page's actual rows, not the projection queue and
		// not duration/diagnostic metadata. No private values leave this process.
		var body struct {
			Rows []map[string]any `json:"rows"`
		}
		_ = json.Unmarshal(response.Body.Bytes(), &body)
		data, _ := json.Marshal(body.Rows)
		hash := sha256.Sum256(data)
		mu.Lock()
		observations = append(observations, observation{time.Since(started).Seconds(), float64(time.Since(at).Microseconds()) / 1000, response.Code, hex.EncodeToString(hash[:])})
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
	defer func() { stopPoll(); <-pollDone }()
	stages := []map[string]any{}
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
			t.Fatalf("unclassified experiment stage %q", stage)
		}
		if err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		after := processResourceUsage()
		stages = append(stages, map[string]any{"stage": stage, "wall_seconds": time.Since(at).Seconds(), "cpu_seconds": after.cpu - usage.cpu, "read_blocks": after.read - usage.read, "written_blocks": after.written - usage.written})
	}
	settled := time.Since(started).Seconds()
	after := processResourceUsage()
	stopPoll()
	<-pollDone
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
		valid := o.Status == http.StatusOK && descriptor.Product.Expected != "" && o.Digest == descriptor.Product.Expected
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
	report := map[string]any{"version": 1, "name": descriptor.Name, "engine": os.Getenv("PHAROS_WORK_ENGINE"), "stages": stages,
		"t_correct": correct, "t_usable": usable, "t_settled": settled, "cpu_seconds": after.cpu - before.cpu,
		"read_blocks": after.read - before.read, "written_blocks": after.written - before.written, "resource_basis": syncMeasurementLimits,
		"observations": snapshots, "final_product_sha256": snapshots[len(snapshots)-1].Digest, "clock": descriptor.Clock}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "probe-report.json"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("work replica %s: settled=%.3fs cpu=%.3fs observations=%d; private report saved\n", descriptor.Name, settled, after.cpu-before.cpu, len(snapshots))
}
