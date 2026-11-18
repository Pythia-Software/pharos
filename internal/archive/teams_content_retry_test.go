package archive

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

func TestTeamsContentRetryBudget(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	server := now.Add(-time.Hour)
	r := teamsContentRetry{Version: 1, Snapshot: "synthetic", State: "waiting", First: now, Last: now, AuthorizationExpiry: server.Add(time.Hour), WorkExpiry: server.Add(time.Hour), RequestHash: strings.Repeat("a", 64)}
	c, _ := testCatalog(t)
	ctx := context.Background()
	if err := c.writeTransaction(ctx, "retry-test", func(tx *sql.Tx) error { return writeTeamsContentRetry(ctx, tx, "work", r) }); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		saved, err := readTeamsContentRetry(ctx, c.reader(), "work")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = nextTeamsContentRetry(saved, now.Add(time.Duration(i)*30*time.Second-time.Nanosecond), server); !errors.Is(err, errTeamsContentRetryWaiting) {
			t.Fatalf("early attempt: %v", err)
		}
		saved, err = nextTeamsContentRetry(saved, now.Add(time.Duration(i)*30*time.Second), server)
		if err != nil || saved.Count != i {
			t.Fatalf("attempt %d: %+v %v", i, saved, err)
		}
		// Reopen the record between attempts, as a restart or explicit check does.
		saved.State = "waiting"
		if err = c.writeTransaction(ctx, "retry-test", func(tx *sql.Tx) error { return writeTeamsContentRetry(ctx, tx, "work", saved) }); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := readTeamsContentRetry(ctx, c.reader(), "work")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = nextTeamsContentRetry(saved, now.Add(150*time.Second), server); !errors.Is(err, errTeamsContentRetryParked) {
		t.Fatalf("budget: %v", err)
	}
	if _, err = nextTeamsContentRetry(r, now.Add(-time.Second), server); !errors.Is(err, errTeamsContentRetryParked) {
		t.Fatalf("clock rollback: %v", err)
	}
	if _, err = nextTeamsContentRetry(r, now.Add(30*time.Second), r.AuthorizationExpiry); !errors.Is(err, errTeamsContentRetryParked) {
		t.Fatalf("server expiry: %v", err)
	}
	r.AuthorizationExpiry = time.Time{}
	if _, err = nextTeamsContentRetry(r, now.Add(30*time.Second), server); !errors.Is(err, errTeamsContentRetryParked) {
		t.Fatalf("legacy expiry: %v", err)
	}
}

func TestTeamsContentRetryStartup(t *testing.T) {
	c, cfg := testCatalog(t)
	s := testServer(t, cfg, c)
	ctx := t.Context()
	now := time.Now().UTC()
	d := LibraryDescriptor{ID: "library", Instance: "instance"}
	credential := &teams.EnrollmentCredential{OrgID: "org", InstallationID: "installation"}
	if _, ok := s.teamsRetryDelayForEnrollment(ctx, now, d, credential); ok {
		t.Fatal("unenrolled startup woke")
	}
	execTx(t, c.DB, `UPDATE teams_local_settings SET library_id='library',instance_id='instance',org_id='org',installation_id='installation',key_fingerprint='fingerprint',enabled=1`,
		`INSERT INTO teams_v2_uploads(work_id,version_id,library_id,instance_id,org_id,installation_id,config_sha256,snapshot_id,binding_id,inventory_revision,inventory_sha256,local_revision,evaluation_revision,settings_revision,source_revision,privacy_revision,generation,mode,policy_version,authorization_json,pages_json,seal_json,state) VALUES('work','version','library','instance','org','installation','hash','snapshot','binding',1,'hash',1,1,1,1,1,1,'reviewed',0,'{}','[]','{}','sealed')`,
		`INSERT INTO teams_request_grants(version_id,library_id,instance_id,org_id,installation_id,config_hash,settings_revision,granted_at) VALUES('version','library','instance','org','installation','hash',1,'now')`)
	if _, err := c.DB.Exec(`INSERT INTO teams_model_permission(id,library_id,instance_id,org_id,installation_id,host_id,provider,model,effort,session_digest,auth_digest,settings_revision,granted_at) VALUES(1,'library','instance','org','installation',?,'synthetic','synthetic','low','a','b',1,'now')`, currentHost().ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.teamsRetryDelayForEnrollment(ctx, now, d, credential); ok {
		t.Fatal("missing retry woke")
	}
	r := teamsContentRetry{Version: 1, Snapshot: "snapshot", State: "waiting", First: now, Last: now, AuthorizationExpiry: now.Add(time.Hour), WorkExpiry: now.Add(time.Hour), RequestHash: strings.Repeat("a", 64)}
	if err := c.writeTransaction(ctx, "retry-test", func(tx *sql.Tx) error { return writeTeamsContentRetry(ctx, tx, "work", r) }); err != nil {
		t.Fatal(err)
	}
	// A catalog with many retained retry rows cannot turn startup into an
	// unbounded scan. Fair rotation beyond this page is a separate concern.
	for i := 0; i < 100; i++ {
		if _, err := c.DB.Exec(`INSERT INTO meta(key,value) VALUES(?,?)`, fmt.Sprintf("teams_content_retry_v1:a%03d", i), "{}"); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := s.teamsRetryDelayForEnrollment(ctx, now, d, credential); ok {
		t.Fatal("startup exceeded its bounded candidate page")
	}
	if _, err := c.DB.Exec(`DELETE FROM meta WHERE key LIKE 'teams_content_retry_v1:a%'`); err != nil {
		t.Fatal(err)
	}
	if delay, ok := s.teamsRetryDelayForEnrollment(ctx, now, d, credential); !ok || delay != 30*time.Second {
		t.Fatalf("startup %v %v", delay, ok)
	}
	if _, ok := s.teamsRetryDelayForEnrollment(ctx, now.Add(-time.Second), d, credential); ok {
		t.Fatal("clock rollback woke")
	}
	if _, err := c.DB.Exec(`UPDATE teams_local_settings SET enabled=0`); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.teamsRetryDelayForEnrollment(ctx, now, d, credential); ok {
		t.Fatal("disabled enrollment woke")
	}
}

func TestTeamsContentRetryOnlyTransientCode(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&teams.HTTPError{Status: 409, Code: "source_safety_in_progress"}, true},
		{&teams.HTTPError{Status: 409, Code: "source_safety_pending"}, false},
		{&teams.HTTPError{Status: 409, Code: "source_legacy_privacy_unresolved"}, false},
		{&teams.HTTPError{Status: 409, Code: "source_safety_capacity_held"}, false},
		{&teams.HTTPError{Status: 409, Code: "privacy_withdrawal_pending"}, false},
		{&teams.HTTPError{Status: 503, Code: "source_safety_in_progress"}, false},
		{context.DeadlineExceeded, false},
	} {
		if got := teamsContentSafetyInProgress(tc.err); got != tc.want {
			t.Fatalf("%v: %v", tc.err, got)
		}
	}
}

func TestTeamsContentRetryNoImmediatePollLoop(t *testing.T) {
	// A due retry excluded by the five-item cap reports active; an absent work,
	// transport outage, rejected authorization or canceled check may report idle.
	for _, tc := range []struct {
		name   string
		active bool
	}{
		{"excluded by work cap", true}, {"removed from poll", false}, {"outage", false}, {"authorization rejected", false}, {"canceled", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if d := teamsDelayAfterCheck(tc.active); d < 30*time.Second {
				t.Fatalf("immediate loop: %s", d)
			}
		})
	}
}

func TestTeamsPreparedRetryTracking(t *testing.T) {
	dbErr := errors.New("database unavailable")
	for _, tc := range []struct {
		name    string
		prior   teamsContentRetry
		err     error
		tracked bool
		want    error
	}{
		{"absent", teamsContentRetry{}, sql.ErrNoRows, true, nil},
		{"same snapshot", teamsContentRetry{Snapshot: "same"}, nil, false, errTeamsContentRetryParked},
		{"different snapshot", teamsContentRetry{Snapshot: "other"}, nil, false, nil},
		{"malformed", teamsContentRetry{}, errTeamsContentRetryParked, false, nil},
		{"database failure", teamsContentRetry{}, dbErr, false, dbErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracked, err := teamsPreparedRetryTracking(tc.prior, tc.err, "same")
			if tracked != tc.tracked || !errors.Is(err, tc.want) {
				t.Fatalf("tracked=%v error=%v", tracked, err)
			}
		})
	}
}

func TestTeamsWaitingContentDeclineHandler(t *testing.T) {
	useHost(t, "synthetic-host")
	useSupportDir(t)
	c, cfg := testCatalog(t)
	cfg.Application = filepath.Join(t.TempDir(), "Synthetic.app")
	d, err := ensureLibraryDescriptor(cfg, "Synthetic")
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	appID := installationID(cfg.Application)
	raw, _ := json.Marshal(map[string]any{"version": 1, "origin": "https://synthetic.invalid", "org_id": "00000000-0000-4000-8000-000000000001", "person_id": "00000000-0000-4000-8000-000000000002", "installation_id": "00000000-0000-4000-8000-000000000003", "library_id": d.ID, "library_instance": d.Instance, "application_id": appID, "private_key": base64.RawURLEncoding.EncodeToString(key), "source_token_key": base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "metrics_default": map[string]any{"value": false, "policy_version": 1}})
	credential, err := teams.ParseCredential(raw, d.ID, d.Instance, appID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	if _, err = c.setTeamsLocalSettings(t.Context(), d.ID, d.Instance, credential.OrgID, credential.InstallationID, teamsPayloadHash(key.Public().(ed25519.PublicKey)), true, false, 1); err != nil {
		t.Fatal(err)
	}
	override(t, &teamsLoadCredential, func(_ *Catalog, _ Config, desc LibraryDescriptor) (*teams.EnrollmentCredential, error) {
		return teams.ParseCredential(raw, desc.ID, desc.Instance, appID, false)
	})
	var mu sync.Mutex
	var paths []string
	var failStatus bool
	var pollStatus int
	var advertised teams.Work
	var statusEntered, statusRelease chan struct{}
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		fail := failStatus
		status := pollStatus
		work := advertised
		entered, release := statusEntered, statusRelease
		mu.Unlock()
		if r.URL.Path == "/v1/device/capabilities" {
			_ = json.NewEncoder(w).Encode(map[string]any{"manifest_schema_versions": []int{1}})
			return
		}
		if r.URL.Path == "/v1/device/poll" {
			if status != 0 {
				w.WriteHeader(status)
				return
			}
			json.NewEncoder(w).Encode(teams.PollResponse{ServerTime: time.Now().UTC(), Work: []teams.Work{work}, Cancellations: []teams.Cancellation{}, NextPollSeconds: 900})
			return
		}
		if entered != nil {
			close(entered)
			<-release
		}
		if fail {
			http.Error(w, "synthetic unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer endpoint.Close()
	override(t, &teamsDeviceClient, func(_ string, installation string, k ed25519.PrivateKey, _ bool) (*teams.DeviceClient, error) {
		return teams.NewDeviceClient(endpoint.URL, installation, k, true)
	})
	override(t, &teamsWorkerCapabilities, func() teamsCapabilityManifest {
		return teams.CapabilityManifest{SchemaVersion: 1, AppVersion: "0.10.0", Capabilities: []string{"deterministic_query_v1", "source_snapshot_v2"}}
	})
	service := testServer(t, cfg, c)
	workID := "a4c09e42-d0a6-45f0-89ef-2a03b7cff630"
	service.teamsWorker.mu.Lock()
	service.teamsWorker.items = map[string]teamsWorkerItem{workID: {Work: teams.Work{ID: workID, ConfigSHA256: "synthetic"}, State: "source_safety_waiting"}}
	service.teamsWorker.scope = teamsCredentialScope(d, credential)
	service.teamsWorker.workBindings = map[string]string{workID: "first"}
	service.teamsWorker.mu.Unlock()
	// An offline non-series card is display-only and cannot grant or approve.
	service.teamsWorker.mu.Lock()
	service.teamsWorker.scope = ""
	service.teamsWorker.items[workID] = teamsWorkerItem{Work: teams.Work{ID: workID, ConfigSHA256: "synthetic"}, State: "awaiting_source_consent"}
	service.teamsWorker.mu.Unlock()
	grantBody, _ := json.Marshal(map[string]any{"workId": workID, "settingsRevision": 1})
	offlineGrant := httptest.NewRecorder()
	service.teamsWorkerHTTP(offlineGrant, httptest.NewRequest(http.MethodPost, "/api/teams/worker/allow-source", bytes.NewReader(grantBody)))
	if offlineGrant.Code == http.StatusOK {
		t.Fatal("offline grant accepted")
	}
	service.teamsWorker.mu.Lock()
	service.teamsWorker.items[workID] = teamsWorkerItem{Work: teams.Work{ID: workID, ConfigSHA256: "synthetic"}, State: "awaiting_review"}
	service.teamsWorker.mu.Unlock()
	approveBody, _ := json.Marshal(map[string]string{"workId": workID, "payloadHash": "synthetic"})
	offlineApproval := httptest.NewRecorder()
	service.teamsWorkerHTTP(offlineApproval, httptest.NewRequest(http.MethodPost, "/api/teams/worker/approve-result", bytes.NewReader(approveBody)))
	if offlineApproval.Code == http.StatusOK {
		t.Fatal("offline approval accepted")
	}
	service.teamsWorker.mu.Lock()
	service.teamsWorker.scope = teamsCredentialScope(d, credential)
	service.teamsWorker.items[workID] = teamsWorkerItem{Work: teams.Work{ID: workID, ConfigSHA256: "synthetic"}, State: "source_safety_waiting"}
	service.teamsWorker.mu.Unlock()
	body, _ := json.Marshal(map[string]string{"workId": workID})
	rec := httptest.NewRecorder()
	service.teamsWorkerHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/teams/worker/decline", bytes.NewReader(body)))
	mu.Lock()
	posted := len(paths) > 0
	mu.Unlock()
	if rec.Code != http.StatusOK || !posted {
		t.Fatalf("decline handler status=%d posted=%v", rec.Code, posted)
	}
	var state string
	if err = c.DB.QueryRow(`SELECT state FROM teams_v2_declines WHERE work_id=?`, workID).Scan(&state); err != nil || state != "sent" {
		t.Fatalf("decline journal: %s %v", state, err)
	}

	// A failed status response leaves a durable local decline even when the
	// authenticated poll still advertises work. No disclosure call may race it.
	failedID := "a4c09e42-d0a6-45f0-89ef-2a03b7cff631"
	config := json.RawMessage(`{"question":"synthetic"}`)
	work := teams.Work{ID: failedID, LibraryID: d.ID, Config: config, ConfigSHA256: teamsPayloadHash(config), ExpiresAt: time.Now().Add(time.Hour)}
	mu.Lock()
	failStatus = true
	advertised = work
	mu.Unlock()
	service.teamsWorker.mu.Lock()
	service.teamsWorker.items[failedID] = teamsWorkerItem{Work: work, State: "source_safety_waiting"}
	service.teamsWorker.workBindings[failedID] = "second"
	service.teamsWorker.mu.Unlock()
	body, _ = json.Marshal(map[string]string{"workId": failedID})
	rec = httptest.NewRecorder()
	service.teamsWorkerHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/teams/worker/decline", bytes.NewReader(body)))
	if rec.Code == http.StatusOK {
		t.Fatal("failed remote status acknowledged")
	}
	if err = c.DB.QueryRow(`SELECT state FROM teams_v2_declines WHERE work_id=?`, failedID).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("pending decline: %s %v", state, err)
	}
	client, err := teams.NewDeviceClient(endpoint.URL, credential.InstallationID, key, true)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Destroy()
	polled, err := client.Poll(t.Context(), teams.PollRequest{AppVersion: "0.10.0", LibraryIDs: []string{d.ID}})
	if err != nil || len(polled.Work) != 1 || polled.Work[0].ID != failedID {
		t.Fatalf("work no longer advertised: %v", err)
	}
	mu.Lock()
	before := len(paths)
	mu.Unlock()
	manifest := teams.CapabilityManifest{SchemaVersion: 1, AppVersion: "0.10.0", Capabilities: []string{"deterministic_query_v1", "scoped_artifact_v1", "source_snapshot_v2"}}
	_, err = c.registerTeamsContent(t.Context(), cfg, client, d, credential, work, teamsContentInventory{teamsV2Inventory: teamsV2Inventory{Complete: true}}, manifest)
	if !errors.Is(err, errTeamsConsentStale) {
		t.Fatalf("local decline did not win: %v", err)
	}
	mu.Lock()
	after := len(paths)
	mu.Unlock()
	if after != before {
		t.Fatal("registration contacted the service after local decline")
	}
	// A poll invalidation during the status POST must not reinsert stale work.
	raceID := "a4c09e42-d0a6-45f0-89ef-2a03b7cff632"
	mu.Lock()
	failStatus = false
	statusEntered = make(chan struct{})
	statusRelease = make(chan struct{})
	entered, release := statusEntered, statusRelease
	mu.Unlock()
	service.teamsWorker.mu.Lock()
	service.teamsWorker.items[raceID] = teamsWorkerItem{Work: teams.Work{ID: raceID, ConfigSHA256: "race"}, State: "awaiting_model_permission"}
	service.teamsWorker.workBindings[raceID] = "race"
	service.teamsWorker.mu.Unlock()
	raceBody, _ := json.Marshal(map[string]string{"workId": raceID})
	done := make(chan int, 1)
	go func() {
		rr := httptest.NewRecorder()
		service.teamsWorkerHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/teams/worker/decline", bytes.NewReader(raceBody)))
		done <- rr.Code
	}()
	<-entered
	service.teamsWorker.mu.Lock()
	service.teamsWorker.clearWorkLocked()
	service.teamsWorker.mu.Unlock()
	close(release)
	if code := <-done; code != http.StatusOK {
		t.Fatalf("race decline status %d", code)
	}
	service.teamsWorker.mu.Lock()
	_, exists := service.teamsWorker.items[raceID]
	service.teamsWorker.mu.Unlock()
	if exists {
		t.Fatal("decline reinserted invalidated work")
	}
	mu.Lock()
	statusEntered, statusRelease = nil, nil
	before = len(paths)
	mu.Unlock()
	// A transient poll outage retains the review, but makes it non-actionable.
	mu.Lock()
	pollStatus = http.StatusServiceUnavailable
	mu.Unlock()
	service.teamsWorker.mu.Lock()
	service.teamsWorker.items = map[string]teamsWorkerItem{workID: {Work: teams.Work{ID: workID, Config: json.RawMessage(`{}`), ConfigSHA256: "synthetic"}, State: "awaiting_review"}}
	service.teamsWorker.scope = teamsCredentialScope(d, credential)
	service.teamsWorker.viewScope = service.teamsWorker.scope
	service.teamsWorker.mu.Unlock()
	if _, e := service.checkTeamsWork(t.Context()); e == nil {
		t.Fatal("503 poll unexpectedly succeeded")
	}
	service.teamsWorker.mu.Lock()
	retained, ok := service.teamsWorker.items[workID]
	offline := service.teamsWorker.scope == ""
	service.teamsWorker.mu.Unlock()
	if !ok || retained.State != "awaiting_review" || !offline {
		t.Fatal("503 lost the offline review")
	}
	rr := httptest.NewRecorder()
	service.teamsWorkerHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/teams/worker/approve-result", bytes.NewReader(approveBody)))
	if rr.Code == http.StatusOK {
		t.Fatal("503 left approval actionable")
	}
	// Definitive device authorization failures remove retained card details.
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		mu.Lock()
		pollStatus = status
		mu.Unlock()
		service.teamsWorker.mu.Lock()
		service.teamsWorker.items = map[string]teamsWorkerItem{"old": {State: "awaiting_review"}}
		service.teamsWorker.scope = teamsCredentialScope(d, credential)
		service.teamsWorker.viewScope = service.teamsWorker.scope
		service.teamsWorker.mu.Unlock()
		if _, e := service.checkTeamsWork(t.Context()); e == nil {
			t.Fatalf("poll %d unexpectedly succeeded", status)
		}
		service.teamsWorker.mu.Lock()
		n := len(service.teamsWorker.items)
		service.teamsWorker.mu.Unlock()
		if n != 0 {
			t.Fatalf("poll %d retained cards", status)
		}
	}
	mu.Lock()
	pollStatus = 0
	before = len(paths)
	mu.Unlock()
	// A database failure in the decline gate must also fail before network.
	if _, err = c.DB.Exec(`DROP TABLE teams_v2_declines`); err != nil {
		t.Fatal(err)
	}
	_, err = c.registerTeamsContent(t.Context(), cfg, client, d, credential, work, teamsContentInventory{teamsV2Inventory: teamsV2Inventory{Complete: true}}, manifest)
	if err == nil || errors.Is(err, errTeamsConsentStale) {
		t.Fatalf("decline read failure hidden: %v", err)
	}
	mu.Lock()
	after = len(paths)
	mu.Unlock()
	if after != before {
		t.Fatal("registration contacted service after decline read failure")
	}
}
