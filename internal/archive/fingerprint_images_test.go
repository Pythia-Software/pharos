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
func renderFingerprintTestEntity(t *testing.T, c *Catalog, entity, id string) {
	t.Helper()
	execTx(t, c.DB, `UPDATE fingerprint_entities SET dirty=0`)
	if _, err := c.DB.Exec(`UPDATE fingerprint_entities SET dirty=1,next_attempt=0,priority=100 WHERE entity=? AND id=?`, entity, id); err != nil {
		t.Fatal(err)
	}
	more, err := c.refreshFingerprintImages(t.Context())
	if err != nil || !more {
		t.Fatalf("render %s/%s: more=%v %v", entity, id, more, err)
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
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := c.refreshFingerprintImages(ctx); done <- err }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
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
}

func fingerprintTestInput() map[string]any {
	messages := []map[string]any{}
	for i := range 12 {
		messages = append(messages, map[string]any{"id": fmt.Sprintf("m%d", i), "role": "assistant", "kind": "message", "at": time.Unix(1700000000+int64(i)*10, 0).UTC().Format(time.RFC3339), "labels": []any{[]any{"C.report.product", 1}}, "chunks": 1})
	}
	return map[string]any{"conversation": map[string]any{"id": "c", "work_id": "w", "messages": messages}, "events": []any{}, "taxonomy": []any{}}
}

func TestFingerprintRenderer(t *testing.T) {
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
