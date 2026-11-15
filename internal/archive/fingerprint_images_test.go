package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fingerprintRenderFunc func(context.Context, any, int) (fingerprintRendered, error)

func (f fingerprintRenderFunc) Render(ctx context.Context, input any, mask int) (fingerprintRendered, error) {
	return f(ctx, input, mask)
}

func seedFingerprintLibrary(t *testing.T) (*Catalog, Config) {
	t.Helper()
	c, config := testCatalog(t)
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('fp-w','test','test','fp-w','Fingerprints','2026-01-01'),('fp-other','test','test','fp-other','Other','2026-01-01');
INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('fp-a','fp-w','test','test','a'),('fp-b','fp-w','test','test','b');
INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES('fp-m1','fp-a','m1','user','message','private user text','h1','2026-01-01T12:00:00Z'),('fp-m2','fp-a','m2','assistant','message','private reply','h2','2026-01-01T12:01:00Z');
UPDATE fingerprint_entities SET dirty=0,priority=0,next_attempt=0;`)
	return c, config
}
func renderFingerprintTestEntity(t *testing.T, c *Catalog, entity, id string, wantWork ...bool) {
	t.Helper()
	execTx(t, c.DB, `UPDATE fingerprint_entities SET dirty=0`)
	if _, err := c.DB.Exec(`UPDATE fingerprint_entities SET dirty=1,next_attempt=0,priority=100 WHERE entity=? AND id=?`, entity, id); err != nil {
		t.Fatal(err)
	}
	more, err := c.refreshFingerprintImages(t.Context())
	want := true
	if len(wantWork) > 0 {
		want = wantWork[0]
	}
	if err != nil || more != want {
		t.Fatalf("render %s/%s: more=%v %v", entity, id, more, err)
	}
}

func TestFingerprintWaitsForAnalysis(t *testing.T) {
	c, config := seedFingerprintLibrary(t)
	if err := c.SetUIPreferences(t.Context(), map[string]any{"set": map[string]any{agentEventsFlag: false}}); err != nil {
		t.Fatal(err)
	}
	srv := testServer(t, config, c)
	if err := c.ensureLabelsSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := c.ensureAgentEventsSchema(); err != nil {
		t.Fatal(err)
	}
	if err := c.ensureAgentEventsLedger(t.Context()); err != nil {
		t.Fatal(err)
	}
	renderFingerprintTestEntity(t, c, "conversation", "fp-a")
	var calls int
	renderer := c.fingerprints.renderer
	c.fingerprints.renderer = fingerprintRenderFunc(func(ctx context.Context, input any, mask int) (fingerprintRendered, error) {
		calls++
		return renderer.Render(ctx, input, mask)
	})
	queue := func(t *testing.T) {
		t.Helper()
		execTx(t, c.DB, `UPDATE fingerprint_entities SET dirty=1,priority=0,next_attempt=0,attempts=0;
UPDATE fingerprint_entities SET priority=100 WHERE entity='conversation' AND id='fp-a'`)
	}
	waiting := func(t *testing.T) {
		t.Helper()
		before := calls
		queueState := func() string {
			t.Helper()
			rows, err := queryMapsContext(t.Context(), c.reader(), `SELECT entity,id,revision,dirty,attempts,next_attempt FROM fingerprint_entities ORDER BY entity,id`)
			if err != nil {
				t.Fatal(err)
			}
			return jsonText(rows)
		}
		unchanged := queueState()
		if pending, err := c.fingerprintImagesPending(t.Context()); err != nil || pending {
			t.Fatalf("waiting work submitted: %v %v", pending, err)
		}
		if did, more, err := c.renderFingerprintBatch(t.Context(), workAllocation{CPUs: 1}, false, time.Minute); err != nil || more || did != 0 || calls != before {
			t.Fatalf("waiting work rendered: count=%d more=%v calls=%d %v", did, more, calls-before, err)
		}
		if queueState() != unchanged {
			t.Fatal("waiting analysis changed its queue state")
		}
	}
	for _, stage := range []string{"tools", "authorship", "peers", "labels", "agent_events"} {
		t.Run("deferred "+stage, func(t *testing.T) {
			queue(t)
			execTx(t, c.DB, fmt.Sprintf(`INSERT INTO sync_deferred(name,generation,completed_generation,changed_at) VALUES('%s',1,0,'2026-01-01') ON CONFLICT(name) DO UPDATE SET generation=generation+1`, stage))
			waiting(t)
			if stage == "labels" || stage == "agent_events" {
				if pending, err := c.fingerprintImagesPending(srv.fingerprintContext(t.Context())); err != nil || !pending {
					t.Fatalf("disabled analysis held images: %v %v", pending, err)
				}
			}
			execTx(t, c.DB, fmt.Sprintf(`UPDATE sync_deferred SET completed_generation=generation WHERE name='%s'`, stage))
			if pending, err := c.fingerprintImagesPending(t.Context()); err != nil || !pending {
				t.Fatalf("completed analysis did not resume images: %v %v", pending, err)
			}
		})
	}
	queue(t)
	_, op := srv.begin(t.Context(), OperationSpec{Kind: "analysis.labels", Trigger: triggerChained})
	waiting(t)
	op.Finish(nil)
	// A pending conversation and its workspace stay queued; unrelated ready
	// entities still progress, despite the waiting conversation's priority.
	for _, table := range []string{"message_labels_dirty", "agent_events_dirty"} {
		t.Run(table, func(t *testing.T) {
			queue(t)
			execTx(t, c.DB, `INSERT INTO `+table+`(conversation_id,token) VALUES('fp-a','waiting'); UPDATE fingerprint_entities SET next_attempt=0`)
			if did, more, err := c.renderFingerprintBatch(t.Context(), workAllocation{CPUs: 1}, false, time.Minute); err != nil || more || did != 2 {
				t.Fatalf("ready entities: count=%d more=%v %v", did, more, err)
			}
			waiting(t)
			if pending, err := c.fingerprintImagesPending(srv.fingerprintContext(t.Context())); err != nil || !pending {
				t.Fatalf("disabled analysis marks held images: %v %v", pending, err)
			}
			if dirty := countRows(t, c, `SELECT COUNT(*) FROM fingerprint_entities WHERE dirty=1`); dirty != 2 {
				t.Fatalf("waiting conversation and workspace lost: %d", dirty)
			}
			execTx(t, c.DB, `DELETE FROM `+table+` WHERE conversation_id='fp-a'; UPDATE fingerprint_entities SET next_attempt=0`)
			if did, more, err := c.renderFingerprintBatch(t.Context(), workAllocation{CPUs: 1}, false, time.Minute); err != nil || more || did != 2 {
				t.Fatalf("completed analysis: count=%d more=%v %v", did, more, err)
			}
		})
	}
	t.Run("unavailable labeler", func(t *testing.T) {
		useSupportDir(t)
		c, config := seedFingerprintLibrary(t)
		config.Labels.Enabled = true
		config.Labels.Worker, config.Labels.Runtime = "", ""
		if err := c.ensureLabelsSchema(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := c.ensureAgentEventsLedger(t.Context()); err != nil {
			t.Fatal(err)
		}
		s := testServer(t, config, c)
		if err := c.scheduleDefaultModelBackfill(t.Context()); err != nil {
			t.Fatal(err)
		}
		execTx(t, c.DB, `INSERT INTO message_labels_dirty(conversation_id,token) VALUES('fp-a','pending');
INSERT INTO agent_events_dirty(conversation_id,token) VALUES('fp-a','pending');
UPDATE fingerprint_entities SET dirty=0;
UPDATE fingerprint_entities SET dirty=1,next_attempt=0 WHERE (entity='conversation' AND id='fp-a') OR (entity='workspace' AND id='fp-w')`)
		ctx := s.fingerprintContext(t.Context())
		if pending, err := c.fingerprintImagesPending(ctx); err != nil || !pending {
			t.Fatalf("unavailable models held fingerprints: %v %v", pending, err)
		}
		if did, more, err := c.renderFingerprintBatch(ctx, workAllocation{CPUs: 1}, false, time.Minute); err != nil || more || did != 2 {
			t.Fatalf("render while unavailable: count=%d more=%v %v", did, more, err)
		}
		for _, entity := range []fingerprintRequest{{"conversation", "fp-a", "wide"}, {"workspace", "fp-w", "wide"}} {
			if image, err := c.fingerprintMetadata(t.Context(), entity, true); err != nil || image.State != "ready" {
				t.Fatalf("%s fingerprint: %+v %v", entity.Entity, image, err)
			}
		}
		deferred, err := c.freshnessDeferred(t.Context())
		if err != nil || deferred["labels"].generation <= deferred["labels"].completed || deferred["agent_events"].generation <= deferred["agent_events"].completed {
			t.Fatalf("rendering acknowledged model backlog: %v %v", deferred, err)
		}
		if !s.Config().Labels.Enabled || !s.agentEventsEnabled(t.Context()) {
			t.Fatal("unavailable worker changed the model settings")
		}
		// A configured but missing worker or runtime has the same fallback.
		worker := filepath.Join(t.TempDir(), "worker")
		for _, settings := range []LabelerSettings{{Enabled: true, Worker: worker}, {Enabled: true, Worker: worker, Runtime: filepath.Join(t.TempDir(), "missing-runtime")}} {
			if settings.Runtime != "" {
				if err := os.WriteFile(worker, []byte("worker"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			s.configMu.Lock()
			s.config.Labels = settings
			s.configMu.Unlock()
			if policy := fingerprintPolicy(s.fingerprintContext(t.Context())); policy.labels || policy.events {
				t.Fatalf("missing worker or runtime still holds fingerprints: %+v", policy)
			}
		}
		// Once the worker is available, model backlog is a dependency again;
		// missing downloadable assets do not make it permanently unavailable.
		s.configMu.Lock()
		s.config.Labels = LabelerSettings{Enabled: true, Worker: worker}
		s.configMu.Unlock()
		execTx(t, c.DB, `UPDATE fingerprint_entities SET dirty=1,next_attempt=0 WHERE entity='conversation' AND id='fp-a'`)
		if pending, err := c.fingerprintImagesPending(s.fingerprintContext(t.Context())); err != nil || pending {
			t.Fatalf("available worker did not restore the dependency: %v %v", pending, err)
		}
		// A completed model update invalidates the provisional images.
		execTx(t, c.DB, `UPDATE fingerprint_entities SET dirty=0 WHERE entity='conversation' AND id='fp-a';
DELETE FROM message_labels_dirty WHERE conversation_id='fp-a'`)
		if dirty := countRows(t, c, `SELECT COUNT(*) FROM fingerprint_entities WHERE dirty=1 AND ((entity='conversation' AND id='fp-a') OR (entity='workspace' AND id='fp-w'))`); dirty != 2 {
			t.Fatalf("model update did not requeue provisional fingerprints: %d", dirty)
		}
	})
	t.Run("requested passes", testFingerprintRequestedPasses)
	t.Run("blocked maintenance demand", func(t *testing.T) {
		c, config := seedFingerprintLibrary(t)
		s := testServer(t, config, c)
		execTx(t, c.DB, `UPDATE fingerprint_entities SET dirty=1,next_attempt=0;
INSERT INTO sync_deferred(name,generation,changed_at) VALUES('tools',1,'2026-01-01')`)
		task := s.fingerprintMaintenanceTask()
		h := newWorkHarness(t, 2, workDefinition{Kind: kindFingerprints, Coarse: true, Task: task})
		request := h.submit(workRequestSpec{Kinds: []string{kindFingerprints}, Mode: workModePass, Priority: workPriorityLive})
		h.runner.Pump()
		h.complete(kindFingerprints)
		if facts := h.request(request); opTerminal(facts.State) || countRows(t, c, `SELECT COUNT(*) FROM fingerprint_images`) != 0 {
			t.Fatalf("blocked pass consumed its demand: %+v", facts)
		}
		execTx(t, c.DB, `UPDATE sync_deferred SET completed_generation=generation`)
		h.clock.Advance(workReconcileInterval)
		h.runner.Pump()
		h.complete(kindFingerprints)
		if facts := h.request(request); facts.State != opSucceeded || countRows(t, c, `SELECT COUNT(*) FROM fingerprint_entities WHERE dirty=1`) != 0 {
			t.Fatalf("ready pass did not finish its demand: %+v", facts)
		}
	})
}

func testFingerprintRequestedPasses(t *testing.T) {
	for _, engine := range []string{"classic", "scheduled"} {
		for _, cadence := range []string{cadenceHourly, cadenceDaily, cadenceManual} {
			t.Run(engine+"/"+cadence, func(t *testing.T) {
				c, config := seedFingerprintLibrary(t)
				config.WorkEngine, config.BackgroundCPUs = engine, 2
				s := testServer(t, config, c)
				renderer, err := newFingerprintRenderer()
				if err != nil {
					t.Fatal(err)
				}
				var calls atomic.Int64
				// Use one CPU so both slots never share native scratch.
				engine := fingerprintRenderFunc(func(ctx context.Context, input any, mask int) (fingerprintRendered, error) {
					if pending := countRows(t, c, `SELECT COUNT(*) FROM sync_deferred WHERE name='tools' AND generation>completed_generation`); pending != 0 {
						t.Error("fingerprint rendered before tools finished")
					}
					calls.Add(1)
					return renderer.Render(ctx, input, mask)
				})
				c.fingerprints.renderer = engine
				if s.work != nil {
					defs, err := s.maintenanceWorkDefinitions(nil)
					if err != nil {
						t.Fatal(err)
					}
					for _, def := range defs {
						if def.Kind == kindFingerprints {
							def.Task.(*maintenanceTask).maxCPUs = func(context.Context) int { return 1 }
						}
					}
					useWorkDefinitions(t, s, defs...)
				}
				passCadence := cadence
				if cadence == cadenceManual {
					passCadence = "requested"
				}
				set := apiCall(t, s, config.APIToken, http.MethodPost, "/api/background/settings", map[string]any{
					"mode": "on", "hold_when_busy": false, "temporary_cadences": map[string]any{"fingerprints": cadence, "tools": cadence}})
				if set.Code != http.StatusOK {
					t.Fatal(set.Body.String())
				}
				execTx(t, c.DB, `UPDATE fingerprint_entities SET dirty=1,next_attempt=0;
INSERT INTO sync_deferred(name,generation,changed_at) VALUES('tools',1,'2026-01-01')`)
				plans := []itemPlan{{ID: "tools", Cadence: cadence}, {ID: "fingerprints", Cadence: cadence}}
				run := func(ids ...string) {
					t.Helper()
					ctx, op := s.begin(t.Context(), OperationSpec{Kind: "background.update", Trigger: triggerSchedule})
					_, err := s.runBackgroundItems(ctx, op, "", passCadence, ids, plans)
					op.Finish(err)
					if err != nil {
						t.Fatal(err)
					}
				}
				// Deliberately list fingerprints first: the pass must run tools
				// before it records a successful fingerprint pass.
				run("fingerprints", "tools")
				if calls.Load() == 0 || countRows(t, c, `SELECT COUNT(*) FROM fingerprint_entities WHERE dirty=1`) != 0 {
					deferred, _ := c.freshnessDeferred(t.Context())
					t.Fatalf("pass succeeded without rendering after analysis: calls=%d dirty=%d deferred=%v runs=%v", calls.Load(), countRows(t, c, `SELECT COUNT(*) FROM fingerprint_entities WHERE dirty=1`), deferred, c.backgroundRuns(t.Context()))
				}
				if config.WorkEngine == "classic" {
					measured := false
					for _, op := range c.ops.views() {
						if op.Spec.Kind == kindFingerprints && op.Step.Done > 0 {
							measured = op.Step.Unit == "fingerprint"
						}
					}
					if !measured {
						t.Fatal("fingerprint checks reported generic batches")
					}
				}
				last := c.backgroundRuns(t.Context())["fingerprints"].succeeded
				execTx(t, c.DB, `UPDATE sync_deferred SET generation=generation+1 WHERE name='tools';
UPDATE fingerprint_entities SET dirty=1,next_attempt=0`)
				run("fingerprints")
				if row := c.backgroundRuns(t.Context())["fingerprints"]; row.succeeded != last || row.err != "" || s.background.fingerprintRetry != passCadence {
					t.Fatalf("waiting pass was consumed: %+v retry=%q", row, s.background.fingerprintRetry)
				}
				// Keep unrelated work off this retry's schedule.
				tx := seedTx(t, c.DB)
				for _, item := range backgroundItems {
					if item.ID != "fingerprints" {
						if _, err := tx.Exec(`INSERT INTO background_runs(host_id,item,succeeded_at) VALUES(?,?,?) ON CONFLICT(host_id,item) DO UPDATE SET succeeded_at=excluded.succeeded_at`, currentHost().ID, item.ID, now()); err != nil {
							t.Fatal(err)
						}
					}
				}
				if _, err := tx.Exec(`UPDATE sync_deferred SET completed_generation=generation WHERE name='tools'`); err != nil {
					t.Fatal(err)
				}
				commitSeed(t, tx)
				if !s.backgroundPass(t.Context(), time.Now()) || countRows(t, c, `SELECT COUNT(*) FROM fingerprint_entities WHERE dirty=1`) != 0 || s.background.fingerprintRetry != "" {
					t.Fatal("accepted pass did not resume after analysis")
				}
				// The public Run now queue must also retain its request while
				// letting other background stages proceed.
				execTx(t, c.DB, `UPDATE sync_deferred SET generation=generation+1 WHERE name='tools'; UPDATE fingerprint_entities SET dirty=1,next_attempt=0`)
				s.requestBackground("fingerprints")
				before := calls.Load()
				s.backgroundPass(t.Context(), time.Now())
				if !s.peekRequested()["fingerprints"] || calls.Load() != before {
					t.Fatal("Run now consumed a request waiting for analysis")
				}
				execTx(t, c.DB, `UPDATE sync_deferred SET completed_generation=generation WHERE name='tools'`)
				if !s.backgroundPass(t.Context(), time.Now()) || s.peekRequested()["fingerprints"] || countRows(t, c, `SELECT COUNT(*) FROM fingerprint_entities WHERE dirty=1`) != 0 {
					t.Fatal("Run now did not resume its retained request")
				}
			})
		}
	}
}
func fingerprintTestRevision(t *testing.T, c *Catalog, entity, id string) string {
	t.Helper()
	var revision string
	if err := c.reader().QueryRow(`SELECT revision FROM fingerprint_entities WHERE entity=? AND id=?`, entity, id).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	return revision
}

func TestFingerprintImagesAPI(t *testing.T) {
	c, config := seedFingerprintLibrary(t)
	srv := testServer(t, config, c)
	request := func(events bool) fingerprintMetadata {
		t.Helper()
		w := apiCall(t, srv, config.APIToken, http.MethodPost, "/api/fingerprints", map[string]any{"items": []any{map[string]any{"entity": "conversation", "id": "fp-a", "profile": "wide"}}, "events": events})
		var body struct {
			Items []fingerprintMetadata `json:"items"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Items) != 1 {
			t.Fatalf("metadata: %d %s", w.Code, w.Body.String())
		}
		return body.Items[0]
	}
	if m := request(true); m.State != "pending" || len(m.Images) != 0 {
		t.Fatal(m)
	}
	var images int
	c.DB.QueryRow(`SELECT COUNT(*) FROM fingerprint_images`).Scan(&images)
	if images != 0 {
		t.Fatal("request rendered synchronously")
	}
	// The legacy evidence API stays bounded while the background loader is full.
	override(t, &messageLabelFingerprintLimit, 1)
	input, err := c.messageLabelFingerprintInput(t.Context(), "fp-a", false, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(input["conversation"].(map[string]any)["messages"].([]map[string]any)) != 2 {
		t.Fatal("background read truncated history")
	}
	renderFingerprintTestEntity(t, c, "conversation", "fp-a")
	m := request(true)
	if m.State != "ready" || len(m.Images) != 2 || m.Href != "/work/fp-w/conversation/fp-a" {
		t.Fatal(m)
	}
	w := apiCall(t, srv, config.APIToken, http.MethodGet, m.Images["light"], nil)
	decoded, err := png.Decode(bytes.NewReader(w.Body.Bytes()))
	if err != nil || w.Code != 200 || decoded.Bounds().Dx() != 1024 || decoded.Bounds().Dy() != 128 {
		t.Fatalf("PNG: %d %v", w.Code, err)
	}
	if w.Header().Get("ETag") == "" || w.Header().Get("Cache-Control") != "private, max-age=31536000, immutable" {
		t.Fatal(w.Header())
	}
	if apiCall(t, srv, "wrong", http.MethodGet, m.Images["light"], nil).Code != 401 {
		t.Fatal("unauthenticated image")
	}
	if bytes.Contains(w.Body.Bytes(), []byte("private user text")) {
		t.Fatal("PNG retained conversation text")
	}
	if apiCall(t, srv, config.APIToken, http.MethodGet, "/api/fingerprints/images/../bad.png", nil).Code != 404 {
		t.Fatal("unvalidated image path")
	}
	execTx(t, c.DB, `UPDATE messages SET text='new private text' WHERE id='fp-m1'`)
	if stale := request(true); stale.State != "pending" || !stale.Stale || len(stale.Images) != 2 {
		t.Fatal("lost last good image", stale)
	}
	renderFingerprintTestEntity(t, c, "conversation", "fp-a")
	if request(true).State != "ready" {
		t.Fatal("never refreshed")
	}
	if request(false).State != "pending" {
		t.Fatal("hidden-events variant was not queued")
	}
	renderFingerprintTestEntity(t, c, "conversation", "fp-a")
	if request(false).State != "ready" {
		t.Fatal("hidden-events variant not published")
	}
	var blobs int
	c.DB.QueryRow(`SELECT COUNT(*) FROM fingerprint_images`).Scan(&images)
	c.DB.QueryRow(`SELECT COUNT(*) FROM fingerprint_blobs`).Scan(&blobs)
	if images != 8 || blobs != 4 {
		t.Fatalf("identical variants not deduplicated: images=%d blobs=%d", images, blobs)
	}
	// Withheld events: the events appearance counts the entity's
	// conversations whose events are left out, and is stale; the event-hidden
	// one, once drawn, is neither. A workspace counts its conversations.
	if err := c.ensureAgentEventsSchema(); err != nil {
		t.Fatal(err)
	}
	if err := c.ensureAgentEventsLedger(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m := request(true); m.EventsPending != 0 {
		t.Fatal("events pending without withheld events", m)
	}
	execTx(t, c.DB, `INSERT INTO agent_events_execution_stale(conversation_id) VALUES('fp-a')`)
	renderFingerprintTestEntity(t, c, "conversation", "fp-a", false) // both appearances wait for analysis
	if hidden := request(false); hidden.State == "ready" || !hidden.Stale || hidden.EventsPending != 0 {
		t.Fatal("the event-hidden image reported withheld events", hidden)
	}
	if shown := request(true); shown.State == "ready" || !shown.Stale || len(shown.Images) != 2 || shown.EventsPending != 1 {
		t.Fatal("withheld events were not reported on the events image", shown)
	}
	workspace := func(events bool) fingerprintMetadata {
		t.Helper()
		w := apiCall(t, srv, config.APIToken, http.MethodPost, "/api/fingerprints", map[string]any{"items": []any{map[string]any{"entity": "workspace", "id": "fp-w", "profile": "wide"}}, "events": events})
		var body struct {
			Items []fingerprintMetadata `json:"items"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Items) != 1 {
			t.Fatalf("workspace metadata: %d %s", w.Code, w.Body.String())
		}
		return body.Items[0]
	}
	if shown, hidden := workspace(true), workspace(false); shown.EventsPending != 1 || hidden.EventsPending != 0 {
		t.Fatal("workspace withheld events", shown, hidden)
	}
	items := make([]any, 251)
	for i := range items {
		items[i] = map[string]any{"entity": "conversation", "id": "fp-a", "profile": "wide"}
	}
	for _, body := range []map[string]any{
		{"items": items},
		{"items": []any{items[0]}, "events": "false"},
		{"items": []any{map[string]any{"entity": "conversation", "id": "fp-a", "profile": "unknown"}}},
	} {
		if w := apiCall(t, srv, config.APIToken, http.MethodPost, "/api/fingerprints", body); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid batch accepted: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestFingerprintToolOnlyInput(t *testing.T) {
	c, _ := seedFingerprintLibrary(t)
	execTx(t, c.DB, `INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,depth,provider,parent_id) VALUES
('fp-main','fp-w','fp-b','main','root',0,'test',NULL),('fp-child','fp-w','fp-b','child','subagent',1,'test','fp-main');
INSERT INTO tool_calls(id,workspace_id,conversation_id,agent_session_id,sequence,provider,kind,tool_name,tool_category,command_category,status,duration_ms) VALUES
('fp-git','fp-w','fp-b','fp-child',1,'test','call','Bash','command','vcs','error',12000),
('fp-read','fp-w','fp-b',NULL,2,'test','call','Read','read','','ok',100)`)
	input, err := c.messageLabelFingerprintInput(t.Context(), "fp-b", false, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	conversation := input["conversation"].(map[string]any)
	tools := conversation["activity"].([]map[string]any)
	if len(tools) != 2 || tools[0]["category"] != "git" || tools[0]["session_id"] != "fp-child" || tools[1]["session_id"] != "fp-main" {
		t.Fatal("tool category or recorded session lost", tools)
	}
	if len(conversation["sessions"].([]map[string]any)) != 2 || len(conversation["messages"].([]map[string]any)) != 0 {
		t.Fatal("tool-only session tree lost", conversation)
	}
	renderFingerprintTestEntity(t, c, "conversation", "fp-b")
	if m, err := c.fingerprintMetadata(t.Context(), fingerprintRequest{"conversation", "fp-b", "wide"}, true); err != nil || m.State != "ready" {
		t.Fatal("tool-only fingerprint not generated", m, err)
	}
}

func TestFingerprintPublicationRaces(t *testing.T) {
	c, _ := seedFingerprintLibrary(t)
	renderer, err := newFingerprintRenderer()
	if err != nil {
		t.Fatal(err)
	}
	result, err := renderer.Render(t.Context(), fingerprintTestInput(), 1)
	if err != nil {
		t.Fatal(err)
	}
	// A hidden-events request arriving during the default render must not be
	// acknowledged by that render. Only its own four variants complete it.
	c.fingerprints.renderer = fingerprintRenderFunc(func(context.Context, any, int) (fingerprintRendered, error) {
		execTx(t, c.DB, `UPDATE fingerprint_entities SET events_mask=3 WHERE entity='conversation' AND id='fp-a'`)
		return result, nil
	})
	renderFingerprintTestEntity(t, c, "conversation", "fp-a")
	var dirty int
	if err := c.DB.QueryRow(`SELECT dirty FROM fingerprint_entities WHERE entity='conversation' AND id='fp-a'`).Scan(&dirty); err != nil || dirty != 1 {
		t.Fatalf("concurrent variant request lost: dirty=%d %v", dirty, err)
	}
	c.fingerprints.renderer = renderer
	more, err := c.refreshFingerprintImages(t.Context())
	if err != nil || !more {
		t.Fatal(more, err)
	}
	if m, err := c.fingerprintMetadata(t.Context(), fingerprintRequest{"conversation", "fp-a", "card"}, false); err != nil || m.State != "ready" {
		t.Fatal(m, err)
	}
	// Deleting and recreating the same identity must not let the old render
	// overwrite the new entity, even when all the source fields are identical.
	c.fingerprints.renderer = fingerprintRenderFunc(func(context.Context, any, int) (fingerprintRendered, error) {
		execTx(t, c.DB, `DELETE FROM conversations WHERE id='fp-b'; INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('fp-b','fp-w','test','test','b')`)
		return result, nil
	})
	renderFingerprintTestEntity(t, c, "conversation", "fp-b")
	if m, err := c.fingerprintMetadata(t.Context(), fingerprintRequest{"conversation", "fp-b", "wide"}, true); err != nil || m.State != "pending" || len(m.Images) != 0 {
		t.Fatal("deleted generation published onto replacement", m, err)
	}
}

func TestFingerprintConcurrentWorkers(t *testing.T) {
	c, _ := seedFingerprintLibrary(t)
	execTx(t, c.DB, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('fp-c','fp-w','test','test','c');
UPDATE fingerprint_entities SET dirty=0;
UPDATE fingerprint_entities SET dirty=1,priority=100,next_attempt=0 WHERE entity='conversation'`)
	renderer, err := newFingerprintRenderer()
	if err != nil {
		t.Fatal(err)
	}
	result, err := renderer.Render(t.Context(), fingerprintTestInput(), 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan string, 3)
	release := make(chan struct{})
	done := make(chan error, 2)
	engine := fingerprintRenderFunc(func(ctx context.Context, input any, _ int) (fingerprintRendered, error) {
		started <- firstString(input.(map[string]any)["conversation"].(map[string]any)["id"])
		select {
		case <-release:
			return result, nil
		case <-ctx.Done():
			return fingerprintRendered{}, ctx.Err()
		}
	})
	c.fingerprints.renderer, c.fingerprints.secondary = engine, engine
	for range 2 {
		go func() { _, err := c.refreshFingerprintImages(ctx); done <- err }()
	}
	remaining := 2
	defer func() {
		cancel()
		for remaining > 0 {
			<-done
			remaining--
		}
	}()
	ids := map[string]bool{}
	for range 2 {
		select {
		case id := <-started:
			ids[id] = true
		case <-time.After(5 * time.Second):
			t.Fatal("workers did not start independently")
		}
	}
	if len(ids) != 2 || !ids["fp-a"] || !ids["fp-b"] {
		t.Fatal("workers claimed the same entity", ids)
	}
	// A third runnable conversation must wait; a new generation of an active
	// conversation must wait too, rather than being rendered in another renderer.
	execTx(t, c.DB, `UPDATE messages SET text='changed during concurrent render' WHERE id='fp-m1'; UPDATE fingerprint_entities SET next_attempt=0`)
	third, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	more, err := c.refreshFingerprintImages(third)
	stop()
	if more || err != nil {
		t.Fatal("worker bound was exceeded", more, err)
	}
	close(release)
	for remaining > 0 {
		err := <-done
		remaining--
		if err != nil {
			t.Fatal(err)
		}
	}
	a, err := c.fingerprintMetadata(t.Context(), fingerprintRequest{"conversation", "fp-a", "wide"}, true)
	if err != nil || a.State != "pending" || len(a.Images) != 0 {
		t.Fatal("stale concurrent result published", a, err)
	}
	b, err := c.fingerprintMetadata(t.Context(), fingerprintRequest{"conversation", "fp-b", "wide"}, true)
	if err != nil || b.State != "ready" {
		t.Fatal("independent render lost", b, err)
	}
	c.fingerprints.mu.Lock()
	free := c.fingerprints.active[0] == nil && c.fingerprints.active[1] == nil
	c.fingerprints.mu.Unlock()
	if !free {
		t.Fatal("completed reservations leaked")
	}
	if more, err := c.refreshFingerprintImages(t.Context()); err != nil || !more {
		t.Fatal("released slot could not resume", more, err)
	}
}

func TestFingerprintAtomicPublication(t *testing.T) {
	c, _ := seedFingerprintLibrary(t)
	// Fail after at least one variant would have been written. No subset of
	// the set, or its otherwise-unreferenced blobs, may escape the transaction.
	execTx(t, c.DB, `CREATE TRIGGER fingerprint_test_failure BEFORE INSERT ON fingerprint_images WHEN NEW.profile='card' BEGIN SELECT RAISE(ABORT,'simulated disk write failure'); END;
UPDATE fingerprint_entities SET dirty=1,next_attempt=0,priority=100 WHERE entity='conversation' AND id='fp-a'`)
	if _, err := c.refreshFingerprintImages(t.Context()); err == nil {
		t.Fatal("expected publication failure")
	}
	var images, blobs, dirty int
	if err := c.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM fingerprint_images),(SELECT COUNT(*) FROM fingerprint_blobs),dirty FROM fingerprint_entities WHERE entity='conversation' AND id='fp-a'`).Scan(&images, &blobs, &dirty); err != nil || images != 0 || blobs != 0 || dirty != 1 {
		t.Fatalf("partial publication: images=%d blobs=%d dirty=%d %v", images, blobs, dirty, err)
	}
	execTx(t, c.DB, `DROP TRIGGER fingerprint_test_failure`)
	if more, err := c.refreshFingerprintImages(t.Context()); err != nil || !more {
		t.Fatal("failed publication could not resume", more, err)
	}
}

func TestFingerprintSharedBlobCleanup(t *testing.T) {
	c, _ := seedFingerprintLibrary(t)
	renderFingerprintTestEntity(t, c, "conversation", "fp-a")
	// fp-a and its workspace have exactly the same source ribbon; storage
	// should share those PNGs, and deleting either owner must retain them.
	renderFingerprintTestEntity(t, c, "workspace", "fp-w")
	var blobs int
	if err := c.DB.QueryRow(`SELECT COUNT(*) FROM fingerprint_blobs`).Scan(&blobs); err != nil || blobs != 4 {
		t.Fatalf("workspace PNGs not deduplicated: %d %v", blobs, err)
	}
	execTx(t, c.DB, `DELETE FROM conversations WHERE id='fp-a'`)
	if _, err := c.pruneFingerprintBlobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Cleanup grants a grace period to already-issued immutable URLs.
	var retained int
	if err := c.DB.QueryRow(`SELECT COUNT(*) FROM fingerprint_blobs`).Scan(&retained); err != nil || retained != blobs {
		t.Fatal("grace period discarded an issued image", retained, err)
	}
	execTx(t, c.DB, `UPDATE fingerprint_orphans SET not_before=0`)
	if _, err := c.pruneFingerprintBlobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := c.DB.QueryRow(`SELECT COUNT(*) FROM fingerprint_blobs`).Scan(&retained); err != nil || retained != blobs {
		t.Fatal("cleanup deleted a shared workspace PNG", retained, err)
	}
	execTx(t, c.DB, `DELETE FROM workspaces WHERE id='fp-w'; UPDATE fingerprint_orphans SET not_before=0`)
	if _, err := c.pruneFingerprintBlobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := c.DB.QueryRow(`SELECT COUNT(*) FROM fingerprint_blobs`).Scan(&retained); err != nil || retained != 0 {
		t.Fatal("unreferenced PNGs remain", retained, err)
	}
	for _, cadence := range []string{cadenceHourly, cadenceDaily, cadenceManual} {
		t.Run(cadence, func(t *testing.T) {
			c, config := seedFingerprintLibrary(t)
			srv := testServer(t, config, c)
			override(t, &fingerprintPruneBatchSize, 2)
			response := apiCall(t, srv, config.APIToken, http.MethodPost, "/api/background/settings",
				map[string]any{"temporary_cadences": map[string]any{"fingerprints": cadence}})
			if response.Code != http.StatusOK {
				t.Fatal(response.Body.String())
			}
			renderFingerprintTestEntity(t, c, "conversation", "fp-a")
			old, err := queryMapsContext(t.Context(), c.reader(), `SELECT hash FROM fingerprint_images WHERE entity='conversation' AND entity_id='fp-a'`)
			if err != nil {
				t.Fatal(err)
			}
			renderFingerprintTestEntity(t, c, "conversation", "fp-b")
			execTx(t, c.DB, `DELETE FROM conversations WHERE id='fp-a';
UPDATE fingerprint_entities SET dirty=0;
UPDATE conversations SET provider='other' WHERE id='fp-b';
UPDATE fingerprint_entities SET dirty=0 WHERE entity='workspace';
UPDATE fingerprint_entities SET next_attempt=0;
UPDATE fingerprint_orphans SET not_before=0;
INSERT INTO fingerprint_orphans(hash,not_before) SELECT hash,0 FROM fingerprint_images WHERE entity='conversation' AND entity_id='fp-b' ON CONFLICT(hash) DO UPDATE SET not_before=0;
INSERT INTO fingerprint_blobs(hash,png) VALUES('young',X'00');
INSERT INTO fingerprint_orphans(hash,not_before) VALUES('young',unixepoch()+3600)`)
			// The production scheduled/manual entry point must drain more than
			// one cleanup batch, including markers whose PNGs are still live.
			if err := srv.runBackgroundItem(t.Context(), nil, "fingerprints"); err != nil {
				t.Fatal(err)
			}
			for _, image := range old {
				var found int
				if err := c.DB.QueryRow(`SELECT COUNT(*) FROM fingerprint_blobs WHERE hash=?`, image["hash"]).Scan(&found); err != nil || found != 0 {
					t.Fatal("pass retained an expired PNG", found, err)
				}
			}
			var expired, missing, young int
			if err := c.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM fingerprint_orphans WHERE not_before<=unixepoch()),
(SELECT COUNT(*) FROM fingerprint_images i WHERE NOT EXISTS(SELECT 1 FROM fingerprint_blobs b WHERE b.hash=i.hash)),
(SELECT COUNT(*) FROM fingerprint_blobs WHERE hash='young')`).Scan(&expired, &missing, &young); err != nil || expired != 0 || missing != 0 || young != 1 {
				t.Fatalf("cleanup: expired=%d missing=%d young=%d %v", expired, missing, young, err)
			}
			if image, err := c.fingerprintMetadata(t.Context(), fingerprintRequest{"conversation", "fp-b", "wide"}, true); err != nil || image.State != "ready" {
				t.Fatal("cleanup prevented image refresh", image, err)
			}
			// Cleanup must also run when no conversation needs rendering.
			execTx(t, c.DB, `INSERT INTO fingerprint_blobs(hash,png) VALUES('expired-idle',X'01');
INSERT INTO fingerprint_orphans(hash,not_before) VALUES('expired-idle',0)`)
			if err := srv.runBackgroundItem(t.Context(), nil, "fingerprints"); err != nil {
				t.Fatal(err)
			}
			if err := c.DB.QueryRow(`SELECT COUNT(*) FROM fingerprint_blobs WHERE hash='expired-idle'`).Scan(&retained); err != nil || retained != 0 {
				t.Fatal("idle pass skipped cleanup", retained, err)
			}
		})
	}
}

func TestFingerprintInvalidationAndRecovery(t *testing.T) {
	t.Run("unchanged input", func(t *testing.T) {
		c, _ := seedFingerprintLibrary(t)
		renderer, err := newFingerprintRenderer()
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		counted := fingerprintRenderFunc(func(ctx context.Context, input any, mask int) (fingerprintRendered, error) {
			calls++
			return renderer.Render(ctx, input, mask)
		})
		c.fingerprints.renderer = counted
		for _, entity := range []string{"conversation", "workspace"} {
			id := "fp-a"
			if entity == "workspace" {
				id = "fp-w"
			}
			renderFingerprintTestEntity(t, c, entity, id)
			// A redundant dirty mark needs only acknowledgement.
			renderFingerprintTestEntity(t, c, entity, id)
		}
		if calls != 2 {
			t.Fatalf("redundant dirty marks rendered again: %d calls", calls)
		}
		images, err := queryMapsContext(t.Context(), c.reader(), `SELECT entity,entity_id,profile,theme,hash FROM fingerprint_images ORDER BY entity,entity_id,profile,theme`)
		if err != nil {
			t.Fatal(err)
		}
		// Text changes without labels or events change the source revision but
		// not the renderer's evidence. Reuse must survive process restarts.
		execTx(t, c.DB, `UPDATE messages SET text='new text without a drawing change' WHERE id='fp-m1'`)
		reopened, err := OpenCatalog(c.Path)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		reopened.fingerprints.renderer = counted
		for _, item := range []fingerprintRequest{{"conversation", "fp-a", "wide"}, {"workspace", "fp-w", "wide"}} {
			renderFingerprintTestEntity(t, reopened, item.Entity, item.ID)
			if m, err := reopened.fingerprintMetadata(t.Context(), item, true); err != nil || m.State != "ready" {
				t.Fatalf("unchanged input not acknowledged: %+v %v", m, err)
			}
		}
		if calls != 2 {
			t.Fatalf("unchanged input rendered after restart: %d calls", calls)
		}
		// Reuse must obey the same publication guard as a fresh render.
		execTx(t, reopened.DB, `UPDATE conversations SET agent_nickname='before reuse' WHERE id='fp-a'`)
		func() {
			override(t, &fingerprintInputReadHook, func() {
				execTx(t, reopened.DB, `UPDATE conversations SET agent_nickname='during reuse' WHERE id='fp-a'`)
			})
			defer func() { fingerprintInputReadHook = nil }()
			renderFingerprintTestEntity(t, reopened, "conversation", "fp-a")
		}()
		if pending := countRows(t, reopened, `SELECT COUNT(*) FROM fingerprint_entities WHERE entity='conversation' AND id='fp-a' AND dirty=1`); pending != 1 {
			t.Fatal("reuse acknowledged a concurrently changed source")
		}
		renderFingerprintTestEntity(t, reopened, "conversation", "fp-a")
		if calls != 2 {
			t.Fatalf("reuse race unnecessarily rendered unchanged inputs: %d calls", calls)
		}
		after, err := queryMapsContext(t.Context(), reopened.reader(), `SELECT entity,entity_id,profile,theme,hash FROM fingerprint_images ORDER BY entity,entity_id,profile,theme`)
		if err != nil || jsonText(after) != jsonText(images) || countRows(t, reopened, `SELECT COUNT(*) FROM fingerprint_orphans`) != 0 {
			t.Fatalf("reuse replaced images or orphaned their PNGs: %v", err)
		}
		// A newly requested appearance and a genuine input change each render.
		execTx(t, reopened.DB, `UPDATE fingerprint_entities SET events_mask=3 WHERE entity='conversation' AND id='fp-a'`)
		renderFingerprintTestEntity(t, reopened, "conversation", "fp-a")
		if calls != 3 {
			t.Fatalf("missing appearance not rendered: %d calls", calls)
		}
		execTx(t, reopened.DB, `INSERT INTO messages(id,conversation_id,native_id,role,kind,text,content_hash,created_at) VALUES('fp-new','fp-a','new','assistant','message','another message','new','2026-01-01T12:02:00Z')`)
		renderFingerprintTestEntity(t, reopened, "conversation", "fp-a")
		renderFingerprintTestEntity(t, reopened, "conversation", "fp-a")
		if calls != 5 {
			t.Fatalf("changed inputs did not render both appearances: %d calls", calls)
		}
		// An older executable does not know to replace input stamps when it
		// publishes a different revision. Never trust its inherited hash.
		execTx(t, reopened.DB, `UPDATE fingerprint_images SET revision='older-writer' WHERE entity='conversation' AND entity_id='fp-a';
UPDATE fingerprint_entities SET revision='after-older-writer' WHERE entity='conversation' AND id='fp-a'`)
		renderFingerprintTestEntity(t, reopened, "conversation", "fp-a")
		if calls != 6 {
			t.Fatalf("older writer's unrelated input stamp was reused: %d calls", calls)
		}
		// Older images acquire hashes only when next dirtied. Opening/upgrading
		// a catalog must not queue a full backfill just to populate the column.
		execTx(t, reopened.DB, `UPDATE fingerprint_entities SET dirty=0;
ALTER TABLE fingerprint_images DROP COLUMN input_hash;
ALTER TABLE fingerprint_images DROP COLUMN input_revision`)
		if err := reopened.ensureFingerprintImages(t.Context()); err != nil {
			t.Fatal(err)
		}
		if pending := countRows(t, reopened, `SELECT COUNT(*) FROM fingerprint_entities WHERE dirty=1`); pending != 0 {
			t.Fatalf("cache migration queued %d unchanged entities", pending)
		}
		execTx(t, reopened.DB, `UPDATE fingerprint_entities SET revision='legacy' WHERE entity='conversation' AND id='fp-a'`)
		renderFingerprintTestEntity(t, reopened, "conversation", "fp-a")
		if calls != 7 {
			t.Fatalf("legacy image trusted without an input hash: %d calls", calls)
		}
	})
	t.Run("analysis timestamps", func(t *testing.T) {
		c, _ := seedFingerprintLibrary(t)
		if err := c.ensureLabelsSchema(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := c.ensureAgentEventsSchema(); err != nil {
			t.Fatal(err)
		}
		if err := c.ensureAgentEventsLedger(t.Context()); err != nil {
			t.Fatal(err)
		}
		execTx(t, c.DB, `INSERT INTO message_labels(message_id,conversation_id,segment,labels,probs,chunks,tokens,input_hash,model,contract,labeled_at) VALUES('fp-m1','fp-a','P',0,X'00',1,1,'hash','model','contract','before');
INSERT INTO message_labels_conversations(conversation_id,structure,units,built_at) VALUES('fp-a','structure',1,'before');
INSERT INTO agent_events_conversations(conversation_id,structure,built_at) VALUES('fp-a','structure','before');
INSERT INTO agent_event_messages(message_id,conversation_id,source,candidates,events,model,lexicon,imported_at) VALUES('fp-m1','fp-a','reply',0,0,'model','lexicon','before');
UPDATE fingerprint_entities SET dirty=0`)
		conversation := fingerprintTestRevision(t, c, "conversation", "fp-a")
		workspace := fingerprintTestRevision(t, c, "workspace", "fp-w")
		execTx(t, c.DB, `UPDATE message_labels SET labeled_at='after';
UPDATE message_labels_conversations SET built_at='after';
UPDATE agent_events_conversations SET built_at='after';
UPDATE agent_event_messages SET imported_at='after'`)
		if fingerprintTestRevision(t, c, "conversation", "fp-a") != conversation || fingerprintTestRevision(t, c, "workspace", "fp-w") != workspace || countRows(t, c, `SELECT COUNT(*) FROM fingerprint_entities WHERE dirty=1`) != 0 {
			t.Fatal("analysis bookkeeping queued unchanged images")
		}
		execTx(t, c.DB, `UPDATE message_labels SET chunks=2 WHERE message_id='fp-m1'`)
		if fingerprintTestRevision(t, c, "conversation", "fp-a") == conversation || fingerprintTestRevision(t, c, "workspace", "fp-w") == workspace {
			t.Fatal("changed label evidence did not invalidate both images")
		}
	})
	c, _ := seedFingerprintLibrary(t)
	before := fingerprintTestRevision(t, c, "conversation", "fp-a")
	tx, err := c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	tx.Exec(`UPDATE messages SET text='rolled back' WHERE id='fp-m1'`)
	tx.Rollback()
	if fingerprintTestRevision(t, c, "conversation", "fp-a") != before {
		t.Fatal("rollback invalidated images")
	}
	oldWork := fingerprintTestRevision(t, c, "workspace", "fp-w")
	newWork := fingerprintTestRevision(t, c, "workspace", "fp-other")
	execTx(t, c.DB, `UPDATE conversations SET workspace_id='fp-other' WHERE id='fp-a'`)
	if fingerprintTestRevision(t, c, "workspace", "fp-w") == oldWork || fingerprintTestRevision(t, c, "workspace", "fp-other") == newWork {
		t.Fatal("move failed to invalidate both workspaces")
	}
	before = fingerprintTestRevision(t, c, "conversation", "fp-a")
	if err := c.ensureLabelsSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	execTx(t, c.DB, `INSERT INTO message_labels_dirty(conversation_id,token) VALUES('fp-a','new') ON CONFLICT(conversation_id) DO UPDATE SET token=excluded.token`)
	if fingerprintTestRevision(t, c, "conversation", "fp-a") == before {
		t.Fatal("late-created labels ledger lacks invalidation")
	}
	before = fingerprintTestRevision(t, c, "conversation", "fp-a")
	execTx(t, c.DB, `INSERT INTO tool_calls(id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,status) VALUES('fp-tool','fp-other','fp-a',1,'test','call','Bash','command','error')`)
	if fingerprintTestRevision(t, c, "conversation", "fp-a") == before {
		t.Fatal("tool call did not invalidate image")
	}
	execTx(t, c.DB, `DELETE FROM message_labels_dirty`)
	renderer, err := newFingerprintRenderer()
	if err != nil {
		t.Fatal(err)
	}
	result, err := renderer.Render(t.Context(), fingerprintTestInput(), 1)
	if err != nil {
		t.Fatal(err)
	}
	c.fingerprints.renderer = fingerprintRenderFunc(func(ctx context.Context, input any, mask int) (fingerprintRendered, error) {
		execTx(t, c.DB, `UPDATE messages SET text='changed during render' WHERE id='fp-m1'`)
		return result, nil
	})
	renderFingerprintTestEntity(t, c, "conversation", "fp-a")
	var count int
	c.DB.QueryRow(`SELECT COUNT(*) FROM fingerprint_images WHERE entity_id='fp-a'`).Scan(&count)
	if count != 0 {
		t.Fatal("stale render was published")
	}
	c.fingerprints.renderer = fingerprintRenderFunc(func(context.Context, any, int) (fingerprintRendered, error) {
		return fingerprintRendered{}, errors.New("temporary raster failure")
	})
	execTx(t, c.DB, `DELETE FROM message_labels_dirty`)
	renderFingerprintTestEntity(t, c, "conversation", "fp-a")
	var attempts, dirty, next int
	if err := c.DB.QueryRow(`SELECT attempts,dirty,next_attempt FROM fingerprint_entities WHERE entity='conversation' AND id='fp-a'`).Scan(&attempts, &dirty, &next); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || dirty != 1 || next <= int(time.Now().Unix()) {
		t.Fatalf("retry not durable: %d %d %d", attempts, dirty, next)
	}
	reopened, err := OpenCatalog(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var retained int
	reopened.DB.QueryRow(`SELECT attempts FROM fingerprint_entities WHERE entity='conversation' AND id='fp-a'`).Scan(&retained)
	if retained != attempts {
		t.Fatal("restart lost retry state")
	}
	c.fingerprints.renderer = renderer
	renderFingerprintTestEntity(t, c, "conversation", "fp-a")
	// An earlier renderer that drew the same images hands them over, even after
	// a version (0.9.0's) requeued everything; an entity without them still renders.
	override(t, &fingerprintSameImages, map[string][]string{fingerprintVersion: {"same-renderer"}})
	execTx(t, c.DB, `UPDATE fingerprint_images SET renderer='same-renderer';UPDATE fingerprint_entities SET dirty=1;UPDATE meta SET value='same-renderer' WHERE key='fingerprint_renderer'`)
	if err := c.ensureFingerprintImages(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m, err := c.fingerprintMetadata(t.Context(), fingerprintRequest{"conversation", "fp-a", "wide"}, true); err != nil || m.State != "ready" {
		t.Fatal("identical renderer's images were not adopted", m, err)
	}
	var adopted, queued int
	c.DB.QueryRow(`SELECT dirty FROM fingerprint_entities WHERE entity='conversation' AND id='fp-a'`).Scan(&adopted)
	c.DB.QueryRow(`SELECT dirty FROM fingerprint_entities WHERE entity='workspace' AND id='fp-other'`).Scan(&queued)
	if adopted != 0 || queued != 1 {
		t.Fatalf("adoption settled the wrong entities: fp-a dirty=%d, fp-other dirty=%d", adopted, queued)
	}
	var stamped string
	c.DB.QueryRow(`SELECT value FROM meta WHERE key='fingerprint_renderer'`).Scan(&stamped)
	if stamped != fingerprintVersion {
		t.Fatalf("adoption left the renderer version %q, so every open adopts again", stamped)
	}
	execTx(t, c.DB, `UPDATE fingerprint_images SET renderer='older-renderer';UPDATE meta SET value='older-renderer' WHERE key='fingerprint_renderer'`)
	if err := c.ensureFingerprintImages(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m, err := c.fingerprintMetadata(t.Context(), fingerprintRequest{"conversation", "fp-a", "wide"}, true); err != nil || m.State != "pending" || !m.Stale {
		t.Fatal("renderer change failed to requeue", m, err)
	}
	execTx(t, c.DB, `DELETE FROM conversations WHERE id='fp-a';UPDATE fingerprint_orphans SET not_before=0`)
	if _, err := c.pruneFingerprintBlobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	c.DB.QueryRow(`SELECT COUNT(*) FROM fingerprint_images WHERE entity_id='fp-a'`).Scan(&count)
	if count != 0 {
		t.Fatal("deleted entity retained images")
	}
	c.DB.QueryRow(`SELECT COUNT(*) FROM fingerprint_blobs`).Scan(&count)
	if count != 0 {
		t.Fatal("orphan PNGs not pruned")
	}
}

func TestFingerprintCancellation(t *testing.T) {
	c, _ := seedFingerprintLibrary(t)
	started := make(chan struct{})
	c.fingerprints.renderer = fingerprintRenderFunc(func(ctx context.Context, _ any, _ int) (fingerprintRendered, error) {
		close(started)
		<-ctx.Done()
		return fingerprintRendered{}, ctx.Err()
	})
	execTx(t, c.DB, `UPDATE fingerprint_entities SET dirty=0;UPDATE fingerprint_entities SET dirty=1,next_attempt=0 WHERE entity='conversation' AND id='fp-a'`)
	ctx, cancel := context.WithCancelCause(t.Context())
	done := make(chan error, 1)
	go func() { _, err := c.refreshFingerprintImages(ctx); done <- err }()
	<-started
	cancel(errAutomaticPreempted)
	if err := <-done; !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "render images") || !strings.Contains(err.Error(), errAutomaticPreempted.Error()) {
		t.Fatal(err)
	}
	var dirty, attempts int
	c.DB.QueryRow(`SELECT dirty,attempts FROM fingerprint_entities WHERE entity='conversation' AND id='fp-a'`).Scan(&dirty, &attempts)
	if dirty != 1 || attempts != 0 {
		t.Fatal("interruption did not retain pending work")
	}
	// Event target matching is part of loading the full history, before the
	// embedded renderer. It must also release promptly on shutdown.
	if _, err := findAgentEventTargetsContext(ctx, []eventLinkMessage{{ID: "m", Text: "a retained prompt"}}, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("source target matching ignored cancellation", err)
	}
	t.Run("preempted passes resume", testFingerprintPreemptedPasses)
	t.Run("preemption preserves other worker failures", func(t *testing.T) {
		cancelled := fmt.Errorf("render images: %w", context.Canceled)
		if !cancellationOnly(errors.Join(cancelled, context.Canceled)) || cancellationOnly(errors.Join(cancelled, errors.New("disk write failed"))) || cancellationOnly(context.DeadlineExceeded) {
			t.Fatal("preemption hid a non-cancellation failure")
		}
	})
	t.Run("preempted before rendering", func(t *testing.T) {
		c, config := seedFingerprintLibrary(t)
		s := testServer(t, config, c)
		ctx, cancel := context.WithCancelCause(t.Context())
		cancel(errAutomaticPreempted)
		_, _ = s.runBackgroundItems(ctx, nil, "", "requested", []string{"fingerprints"}, nil)
		if s.background.fingerprintRetry != "requested" {
			t.Fatal("preemption before the fingerprint stage lost its request")
		}
	})
	t.Run("timeouts remain retryable", func(t *testing.T) {
		c, _ := seedFingerprintLibrary(t)
		for _, phase := range []string{"load input", "render images"} {
			t.Run(phase, func(t *testing.T) {
				if phase == "load input" {
					override(t, &fingerprintRenderTimeout, time.Duration(0))
				} else {
					c.fingerprints.renderer = fingerprintRenderFunc(func(context.Context, any, int) (fingerprintRendered, error) {
						return fingerprintRendered{}, context.DeadlineExceeded
					})
				}
				execTx(t, c.DB, `UPDATE fingerprint_entities SET attempts=0,last_error=''`)
				renderFingerprintTestEntity(t, c, "conversation", "fp-a")
				var dirty, attempts, next int
				var problem string
				if err := c.DB.QueryRow(`SELECT dirty,attempts,next_attempt,last_error FROM fingerprint_entities WHERE entity='conversation' AND id='fp-a'`).Scan(&dirty, &attempts, &next, &problem); err != nil {
					t.Fatal(err)
				}
				if dirty != 1 || attempts != 1 || next <= int(time.Now().Unix()) || !strings.Contains(problem, phase) || !strings.Contains(problem, context.DeadlineExceeded.Error()) || !strings.Contains(problem, "fp-a") {
					t.Fatalf("timeout lost diagnostics or retry: dirty=%d attempts=%d next=%d %s", dirty, attempts, next, problem)
				}
			})
		}
		c.fingerprints.renderer = nil
		renderFingerprintTestEntity(t, c, "conversation", "fp-a")
		if metadata, err := c.fingerprintMetadata(t.Context(), fingerprintRequest{"conversation", "fp-a", "wide"}, true); err != nil || metadata.State != "ready" {
			t.Fatal("timed out fingerprint did not recover", metadata, err)
		}
	})
}

func testFingerprintPreemptedPasses(t *testing.T) {
	for _, engine := range []string{"classic", "scheduled"} {
		for _, mode := range []struct {
			cadence    string
			stop       bool
			stopBefore bool
		}{{"requested", false, false}, {"requested", true, false}, {cadenceHourly, false, false}, {cadenceHourly, true, false}, {"requested", true, true}} {
			cadence, stop, stopBefore := mode.cadence, mode.stop, mode.stopBefore
			t.Run(fmt.Sprintf("%s/%s/stop=%t/before=%t", engine, cadence, stop, stopBefore), func(t *testing.T) {
				c, config := seedFingerprintLibrary(t)
				config.WorkEngine, config.BackgroundCPUs = engine, 1
				s := testServer(t, config, c)
				if s.work != nil {
					defs := []workDefinition{{Kind: kindFingerprints, Coarse: true, Task: s.fingerprintMaintenanceTask()}}
					if stopBefore {
						all, err := s.maintenanceWorkDefinitions(nil)
						if err != nil {
							t.Fatal(err)
						}
						defs = all
					}
					useWorkDefinitions(t, s, defs...)
				}
				placement := cadence
				if cadence == "requested" {
					placement = cadenceManual
				}
				temporary := map[string]any{}
				for _, item := range backgroundItems {
					temporary[item.ID] = cadenceManual
				}
				temporary["fingerprints"] = placement
				set := apiCall(t, s, config.APIToken, http.MethodPost, "/api/background/settings", map[string]any{
					"mode": "on", "hold_when_busy": false, "temporary_cadences": temporary})
				if set.Code != http.StatusOK {
					t.Fatal(set.Body.String())
				}
				// A retained scheduled pass must resume even when its next
				// ordinary cadence is not due. Keep its last success intact.
				c.recordBackgroundRun(t.Context(), "fingerprints", time.Now(), true, nil)
				lastRun := c.backgroundRuns(t.Context())["fingerprints"]
				lastSuccess := lastRun.succeeded
				started := make(chan struct{})
				native, err := newFingerprintRenderer()
				if err != nil {
					t.Fatal(err)
				}
				var first atomic.Bool
				c.fingerprints.renderer = fingerprintRenderFunc(func(ctx context.Context, input any, mask int) (fingerprintRendered, error) {
					if first.CompareAndSwap(false, true) {
						close(started)
						<-ctx.Done()
						return fingerprintRendered{}, ctx.Err()
					}
					return native.Render(ctx, input, mask)
				})
				execTx(t, c.DB, `UPDATE fingerprint_entities SET dirty=1,next_attempt=0 WHERE entity='conversation' AND id='fp-a'`)
				if stop {
					// This is the accepted demand retained by a previous
					// preemption, still set while its resumed pass runs.
					s.retainFingerprintPass(cadence)
				}
				ids := []string{"fingerprints"}
				releaseQuery := func() {}
				if stopBefore {
					ids = []string{"search", "fingerprints"}
					c.queries.setEnabled(true)
					releaseQuery = sync.OnceFunc(c.queries.begin())
					t.Cleanup(releaseQuery)
				}
				done := make(chan bool, 1)
				go func() { done <- s.runBackgroundPass(t.Context(), cadence, ids, nil) }()
				if stopBefore {
					waitUntil(t, 5*time.Second, "search before the fingerprint stage", func() bool {
						c.queries.mu.Lock()
						defer c.queries.mu.Unlock()
						return c.queries.waiting > 0
					})
					if first.Load() {
						t.Fatal("fingerprint stage ran before Stop")
					}
					if c.backgroundRuns(t.Context())["fingerprints"].started != lastRun.started {
						t.Fatal("Stop did not precede the fingerprint stage")
					}
				} else {
					<-started
				}
				if stop {
					if !s.stopIngest() {
						t.Fatal("could not stop pass")
					}
				} else {
					if !s.acquireManualIngest() {
						t.Fatal("manual verification could not preempt pass")
					}
					s.ingestMu.Unlock()
				}
				if !<-done {
					t.Fatal("pass was not admitted")
				}
				if countRows(t, c, `SELECT COUNT(*) FROM fingerprint_entities WHERE id='fp-a' AND dirty=1 AND attempts=0`) != 1 {
					t.Fatal("interruption lost pending work or charged a render failure")
				}
				if stop {
					if s.background.fingerprintRetry != "" {
						t.Fatal("deliberate Stop retained the resumed request")
					}
					releaseQuery()
					if s.backgroundPass(t.Context(), time.Now()) || countRows(t, c, `SELECT COUNT(*) FROM fingerprint_entities WHERE id='fp-a' AND dirty=1 AND attempts=0`) != 1 {
						t.Fatal("background tick restarted the stopped pass")
					}
					return
				}
				if row := c.backgroundRuns(t.Context())["fingerprints"]; row.err != "" || row.succeeded != lastSuccess || s.background.fingerprintRetry != cadence {
					t.Fatalf("preemption became a failure or consumed request: %+v retry=%q", row, s.background.fingerprintRetry)
				}
				for _, view := range s.ops.views() {
					if view.Spec.Kind == "background.update" && view.State != opInterrupted {
						t.Fatalf("preemption misclassified parent operation: %+v", view)
					}
				}
				if !s.backgroundPass(t.Context(), time.Now()) || s.background.fingerprintRetry != "" || countRows(t, c, `SELECT COUNT(*) FROM fingerprint_entities WHERE dirty=1`) != 0 {
					t.Fatalf("preempted %s request did not resume: retry=%q pending=%d", cadence, s.background.fingerprintRetry, countRows(t, c, `SELECT COUNT(*) FROM fingerprint_entities WHERE dirty=1`))
				}
			})
		}
	}
}

func fingerprintTestInput() map[string]any {
	messages := []map[string]any{}
	for i := range 12 {
		messages = append(messages, map[string]any{"id": fmt.Sprintf("m%d", i), "role": "assistant", "kind": "message", "at": time.Unix(1700000000+int64(i)*10, 0).UTC().Format(time.RFC3339), "labels": []any{[]any{"C.report.product", 1}}, "chunks": 1})
	}
	return map[string]any{"conversation": map[string]any{"id": "c", "work_id": "w", "messages": messages}, "events": []any{}, "taxonomy": []any{}}
}

func TestFingerprintRenderer(t *testing.T) {
	t.Run("input hash", func(t *testing.T) {
		input := fingerprintTestInput()
		hash := func(mask int) string {
			t.Helper()
			value, err := fingerprintInputHash(input, mask)
			if err != nil {
				t.Fatal(err)
			}
			return value
		}
		shown, hidden := hash(1), hash(2)
		input["pending"], input["events_pending"] = true, 5
		input["conversation"].(map[string]any)["title"] = "new title"
		if hash(1) != shown || hash(2) != hidden {
			t.Fatal("non-renderer metadata changed the input hash")
		}
		input["events"] = []any{map[string]any{"id": "event", "message_id": "m1", "kind": "obstacle", "group": "Friction", "probability": .9}}
		if hash(1) == shown || hash(2) != hidden {
			t.Fatal("event change did not affect only the event appearance")
		}
		shown = hash(1)
		input["conversation"].(map[string]any)["messages"].([]map[string]any)[0]["chunks"] = 0
		if hash(1) == shown || hash(2) == hidden {
			t.Fatal("changed label evidence kept the old input hash")
		}
	})
	renderer, err := newFingerprintRenderer()
	if err != nil {
		t.Fatal(err)
	}
	result, err := renderer.Render(context.Background(), fingerprintTestInput(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Images) != 8 || result.Messages != 12 {
		t.Fatalf("unexpected variants/summary: %d, %d", len(result.Images), result.Messages)
	}
	for _, image := range result.Images {
		decoded, err := png.Decode(bytes.NewReader(image.PNG))
		if err != nil {
			t.Fatal(err)
		}
		height := 128
		if image.Profile == "card" {
			height = 256
		}
		if decoded.Bounds().Dx() != 1024 || decoded.Bounds().Dy() != height {
			t.Fatal(decoded.Bounds())
		}
		r, g, b, a := decoded.At(20, 20).RGBA()
		if r != 0xefef || g != 0x9595 || b != 0x3535 || a != 0xffff {
			t.Fatalf("message palette changed: %x %x %x %x", r, g, b, a)
		}
		if len(image.Hash) != 64 {
			t.Fatal("missing image hash")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := renderer.Render(ctx, fingerprintTestInput(), 1); err == nil {
		t.Fatal("cancelled render succeeded")
	}
	if _, err := renderer.Render(context.Background(), fingerprintTestInput(), 1); err != nil {
		t.Fatalf("cancelled renderer cannot be reused: %v", err)
	}
	// Exercise cancellation inside native preparation, then reuse its scratch.
	during := &fingerprintStepContext{Context: t.Context(), remaining: 8}
	if _, err := renderer.Render(during, fingerprintTestInput(), 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("native preparation ignored cancellation: %v", err)
	}
	if _, err := renderer.Render(t.Context(), fingerprintTestInput(), 1); err != nil {
		t.Fatal("cancelled renderer did not recover", err)
	}

	var dense fingerprintInput
	raw, _ := json.Marshal(fingerprintTestInput())
	if err := json.Unmarshal(raw, &dense); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		dense.Events = append(dense.Events, fingerprintEvent{ID: fmt.Sprintf("cancel-e%d", i), Message: fmt.Sprintf("m%d", i+1), Kind: "obstacle", Group: "Friction", Probability: .9})
	}
	prepared, err := prepareFingerprint(t.Context(), &dense)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := layoutFingerprint(&fingerprintStepContext{Context: t.Context(), remaining: 3}, prepared, "wide", true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("native selection ignored cancellation: %v", err)
	}

}

func TestFingerprintRasterReuse(t *testing.T) {
	renderer, err := newFingerprintRenderer()
	if err != nil {
		t.Fatal(err)
	}
	override(t, &fingerprintPNGCacheLimit, 2)
	source := func(title, color string, height int) string {
		return fmt.Sprintf(`<svg width="1024" height="%d" viewBox="0 0 320 %d"><title>%s</title><rect width="320" height="%d" fill="%s"/></svg>`, height, height*320/1024, title, height*320/1024, color)
	}
	first, err := renderer.cachedRasterize(t.Context(), source("one message", "#399bed", 128))
	if err != nil {
		t.Fatal(err)
	}
	retained := bytes.Clone(first)
	same, err := renderer.cachedRasterize(t.Context(), source("100 messages", "#399bed", 128))
	if err != nil || !bytes.Equal(first, same) || renderer.pngLRU.Len() != 1 {
		t.Fatal("nonvisual counts defeated reuse", err)
	}
	// Different profiles and colors must clear the prior pixels/clip/path
	// state. Eviction and subsequent scratch reuse must not mutate issued PNGs.
	for _, height := range []int{256, 128} {
		other, err := renderer.cachedRasterize(t.Context(), source("other", "#ef9535", height))
		if err != nil || bytes.Equal(first, other) {
			t.Fatal("different drawing reused", err)
		}
		decoded, err := png.Decode(bytes.NewReader(other))
		if err != nil {
			t.Fatal(err)
		}
		r, g, b, a := decoded.At(10, 10).RGBA()
		if r != 0xefef || g != 0x9595 || b != 0x3535 || a != 0xffff {
			t.Fatal("scratch leaked pixels")
		}
	}
	if !bytes.Equal(first, retained) || renderer.pngLRU.Len() != 2 {
		t.Fatal("issued PNG changed or cache grew without bound")
	}
	again, err := renderer.cachedRasterize(t.Context(), source("one message", "#399bed", 128))
	if err != nil || !bytes.Equal(again, retained) {
		t.Fatal("eviction changed the drawing", err)
	}
}

// A deterministic deadline while visiting input, without timing/scheduler races.
type fingerprintStepContext struct {
	context.Context
	remaining int
}

func (c *fingerprintStepContext) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.DeadlineExceeded
	}
	return c.Context.Err()
}

// The expected SVG/PNG hashes were captured from 0.8.9's Goja renderer on
// synthetic, nonprivate inputs. The TS unit test independently checks the SVG
// hashes against the current frontend model; neither side can silently drift.
type fingerprintRenderCase struct {
	Name   string          `json:"name"`
	Input  json.RawMessage `json:"input"`
	Images []struct {
		Profile string `json:"profile"`
		Theme   string `json:"theme"`
		Events  bool   `json:"events"`
		SVGHash string `json:"svg_sha256"`
		PNGHash string `json:"png_sha256"`
		SVG     string `json:"svg,omitempty"`
	} `json:"images"`
}

func fingerprintRenderCases(tb testing.TB) []fingerprintRenderCase {
	tb.Helper()
	path := filepath.Join("..", "..", "testdata", "fingerprint-rendering.json")
	if reference := os.Getenv("PHAROS_FP_REFERENCE"); reference != "" {
		path = reference
	}
	data, err := os.ReadFile(path)
	if err != nil {
		tb.Fatal(err)
	}
	var cases []fingerprintRenderCase
	if err = json.Unmarshal(data, &cases); err != nil {
		tb.Fatal(err)
	}
	return cases
}

// The renderer version names the corpus's images, so a refactor that draws the
// same pixels keeps stored PNGs and one that changes them requeues the cache.
func TestFingerprintRendererVersion(t *testing.T) {
	digest := sha256.New()
	for _, sample := range fingerprintRenderCases(t) {
		for _, image := range sample.Images {
			fmt.Fprintf(digest, "%s %s %s %t %s\n", sample.Name, image.Profile, image.Theme, image.Events, image.PNGHash)
		}
	}
	if version := hex.EncodeToString(digest.Sum(nil)); version != fingerprintVersion {
		t.Fatalf("the parity corpus's images changed: set fingerprintVersion to %s", version)
	}
}

func TestFingerprintWorkersShareOnePass(t *testing.T) {
	c := &Catalog{ops: newOpRegistry("service")}
	shared := &c.fingerprints.pass
	first, second := shared.join(t.Context(), c), shared.join(t.Context(), c)
	if first != second || len(c.ops.running()) != 1 {
		t.Fatal("workers opened separate operations")
	}
	shared.worked(2)
	shared.worked(3)
	shared.leave(nil)
	if len(c.ops.running()) != 1 {
		t.Fatal("the first worker to finish ended the other's pass")
	}
	shared.leave(nil)
	if views := c.ops.views(); len(c.ops.running()) != 0 || len(views) != 1 || views[0].Step.Done != 5 {
		t.Fatalf("one pass with both workers' count: %+v", views)
	}
}

func TestFingerprintRendererParity(t *testing.T) {
	slowTest(t)
	renderer, err := newFingerprintRenderer()
	if err != nil {
		t.Fatal(err)
	}
	cases := fingerprintRenderCases(t)
	update := os.Getenv("PHAROS_UPDATE_FINGERPRINTS") == "1"
	if update && os.Getenv("PHAROS_FP_REFERENCE") != "" {
		t.Fatal("cannot update the external reference corpus")
	}
	for sampleIndex, sample := range cases {
		t.Run(sample.Name, func(t *testing.T) {
			var input any
			if err := json.Unmarshal(sample.Input, &input); err != nil {
				t.Fatal(err)
			}
			result, err := renderFingerprintSVG(t.Context(), input, 3)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Images) != len(sample.Images) {
				t.Fatalf("variants: got %d want %d", len(result.Images), len(sample.Images))
			}
			for i, image := range result.Images {
				expected := sample.Images[i]
				sum := sha256.Sum256([]byte(image.SVG))
				hash := hex.EncodeToString(sum[:])
				svgHash := hash
				if image.Profile != expected.Profile || image.Theme != expected.Theme || image.Events != expected.Events {
					t.Fatalf("variant order changed: %+v", image)
				}
				if hash != expected.SVGHash {
					if expected.SVG != "" {
						at := 0
						for at < min(len(expected.SVG), len(image.SVG)) && expected.SVG[at] == image.SVG[at] {
							at++
						}
						t.Errorf("%s/%s/events=%t SVG difference at %d: got %q want %q", image.Profile, image.Theme, image.Events, at, image.SVG[max(0, at-80):min(len(image.SVG), at+100)], expected.SVG[max(0, at-80):min(len(expected.SVG), at+100)])
					} else {
						t.Errorf("%s/%s/events=%t SVG hash got %s want %s", image.Profile, image.Theme, image.Events, hash, expected.SVGHash)
					}
				}
				png, err := renderer.cachedRasterize(t.Context(), image.SVG)
				if err != nil {
					t.Fatal(err)
				}
				sum = sha256.Sum256(png)
				if hash = hex.EncodeToString(sum[:]); hash != expected.PNGHash {
					if update && svgHash == expected.SVGHash {
						cases[sampleIndex].Images[i].PNGHash = hash
					} else {
						t.Errorf("%s/%s/events=%t PNG hash got %s want %s", image.Profile, image.Theme, image.Events, hash, expected.PNGHash)
					}
				}
				if output := os.Getenv("PHAROS_FINGERPRINT_PARITY_OUTPUT"); output != "" && sample.Name == "mixed" {
					suffix := "-plain"
					if image.Events {
						suffix = "-events"
					}
					if err = os.WriteFile(filepath.Join(output, "native-"+image.Profile+"-"+image.Theme+suffix+".png"), png, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
	if update && !t.Failed() {
		data, err := json.Marshal(cases)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join("..", "..", "testdata", "fingerprint-rendering.json"), append(data, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}

}
func BenchmarkFingerprintRender(b *testing.B) {
	for _, sample := range fingerprintRenderCases(b) {
		if sample.Name != "small" && sample.Name != "mixed" && sample.Name != "dense" {
			continue
		}
		var input any
		if err := json.Unmarshal(sample.Input, &input); err != nil {
			b.Fatal(err)
		}
		for _, cache := range []bool{false, true} {
			name := "uncached"
			if cache {
				name = "warm"
			}
			b.Run(sample.Name+"/"+name, func(b *testing.B) {
				old := fingerprintPNGCacheLimit
				defer func() { fingerprintPNGCacheLimit = old }()
				fingerprintPNGCacheLimit = 0
				if cache {
					fingerprintPNGCacheLimit = 64
				}
				r, err := newFingerprintRenderer()
				if err != nil {
					b.Fatal(err)
				}
				if _, err = r.Render(context.Background(), input, 1); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err = r.Render(context.Background(), input, 1); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// Under population execution one admitted batch keeps both renderer slots
// busy when its grant has two CPUs, renders one image at a time with one,
// and drains every eligible entity without a gap between images.
func TestFingerprintBatchUsesItsAllocation(t *testing.T) {
	renderer, err := newFingerprintRenderer()
	if err != nil {
		t.Fatal(err)
	}
	result, err := renderer.Render(t.Context(), fingerprintTestInput(), 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, cpus := range []int{2, 1} {
		c, _ := seedFingerprintLibrary(t)
		execTx(t, c.DB, `UPDATE fingerprint_entities SET dirty=0;
UPDATE fingerprint_entities SET dirty=1,priority=100,next_attempt=0 WHERE entity='conversation'`)
		var mu sync.Mutex
		active, peak := 0, 0
		release := make(chan struct{})
		started := make(chan struct{}, 16)
		engine := fingerprintRenderFunc(func(ctx context.Context, _ any, _ int) (fingerprintRendered, error) {
			mu.Lock()
			active++
			peak = max(peak, active)
			mu.Unlock()
			defer func() { mu.Lock(); active--; mu.Unlock() }()
			started <- struct{}{}
			select {
			case <-release:
				return result, nil
			case <-ctx.Done():
				return fingerprintRendered{}, ctx.Err()
			}
		})
		c.fingerprints.renderer, c.fingerprints.secondary = engine, engine
		type outcome struct {
			rendered int64
			more     bool
			err      error
		}
		done := make(chan outcome, 1)
		go func() {
			rendered, more, err := c.renderFingerprintBatch(t.Context(), workAllocation{CPUs: cpus}, false, time.Minute)
			done <- outcome{rendered, more, err}
		}()
		for range cpus {
			<-started
		}
		close(release)
		got := <-done
		if got.err != nil || got.more || got.rendered < 2 || peak != cpus {
			t.Fatalf("%d CPUs: %+v, peak %d", cpus, got, peak)
		}
		if pending := countRows(t, c, `SELECT COUNT(*) FROM fingerprint_entities WHERE dirty=1 AND next_attempt<=unixepoch()`); pending != 0 {
			t.Fatalf("%d CPUs left %d entities", cpus, pending)
		}
	}
}
