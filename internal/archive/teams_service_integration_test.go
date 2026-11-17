package archive

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

// Opt in only against the dedicated loopback synthetic Postgres harness.
func TestTeamsServiceSyntheticPostgres(t *testing.T) {
	origin := os.Getenv("PHAROS_TEAMS_SYNTHETIC_ORIGIN")
	if origin == "" {
		t.Skip("no synthetic integration harness")
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		t.Fatal("synthetic harness must be literal loopback HTTP origin")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	control := func(path string, in, out any) {
		t.Helper()
		b, _ := json.Marshal(in)
		req, e := http.NewRequestWithContext(ctx, "POST", origin+path, bytes.NewReader(b))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Content-Type", "application/json")
		r, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Fatalf("synthetic control %s: status %d", path, r.StatusCode)
		}
		if e = json.NewDecoder(r.Body).Decode(out); e != nil {
			t.Fatal(e)
		}
	}
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	ids := []string{"synthetic-library-a", "synthetic-library-b"}
	creds := make([]*teams.EnrollmentCredential, 2)
	clients := make([]*teams.DeviceClient, 2)
	catalogs := make([]*Catalog, 2)
	descriptors := make([]LibraryDescriptor, 2)
	for i, id := range ids {
		attempt, e := teams.BeginEnrollmentWithKey(ctx, origin, id, "Synthetic Library", "1", key, true)
		if e != nil {
			t.Fatalf("begin enrollment: %v", e)
		}
		defer attempt.Close()
		approval, e := url.Parse(attempt.ApprovalURL)
		if e != nil {
			t.Fatal(e)
		}
		frag, e := url.ParseQuery(approval.Fragment)
		if e != nil {
			t.Fatal(e)
		}
		var authorized struct {
			CallbackURL string `json:"callback_url"`
		}
		control("/_test/authorize", map[string]string{"enrollment_id": strings.TrimPrefix(approval.Path, "/enroll/"), "enrollment_confirmation": frag.Get("confirmation"), "fingerprint": attempt.Fingerprint}, &authorized)
		callback, e := url.Parse(authorized.CallbackURL)
		if e != nil || callback.Scheme != "http" || callback.Hostname() != "127.0.0.1" || callback.Path != "/teams/enrollment" {
			t.Fatal("invalid synthetic callback")
		}
		req, e := http.NewRequestWithContext(ctx, "GET", callback.String(), nil)
		if e != nil {
			t.Fatal(e)
		}
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("callback status %d", resp.StatusCode)
		}
		creds[i], e = attempt.Redeem(ctx)
		if e != nil {
			t.Fatalf("redeem enrollment: %v", e)
		}
		defer creds[i].Destroy()
		clients[i], e = teams.NewDeviceClient(origin, creds[i].InstallationID, key, true)
		if e != nil {
			t.Fatal(e)
		}
		defer clients[i].Destroy()
		c, _ := testCatalog(t)
		catalogs[i] = c
		descriptors[i] = LibraryDescriptor{ID: id, Instance: fmt.Sprintf("synthetic-instance-%d", i)}
		execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('w','codex','synthetic-account','w','Synthetic','2026-01-01')`, `INSERT INTO conversations(id,workspace_id,provider,account,native_id) VALUES('c','w','codex','synthetic-account','synthetic-source')`, `INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider) VALUES('s','w','c','main','root','codex')`, `INSERT INTO agent_session_usage(agent_session_id,usage_hour,model,effort,attribution,total_tokens) VALUES('s','2026-01-01T00:00:00Z','m','','request',123)`)
		if i == 1 {
			if _, e = c.setTeamsPrivate(ctx, "c", true); e != nil {
				t.Fatal(e)
			}
		}
		if _, e = c.setTeamsLocalSettings(ctx, id, descriptors[i].Instance, creds[i].OrgID, creds[i].InstallationID, teamsPayloadHash(key.Public().(ed25519.PublicKey)), true, false, creds[i].MetricsDefault.PolicyVersion); e != nil {
			t.Fatal(e)
		}
	}
	if creds[0].InstallationID != creds[1].InstallationID || !bytes.Equal(creds[0].SourceKey(), creds[1].SourceKey()) {
		t.Fatal("installation or source key not shared")
	}
	var issued map[string]string
	control("/_test/send", map[string]string{"library_id": ids[0]}, &issued)
	poll, err := clients[0].Poll(ctx, teams.PollRequest{AppVersion: "1.0.0", LibraryIDs: ids})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	byLibrary := map[string]teams.Work{}
	for _, w := range poll.Work {
		if w.VersionID == issued["version_id"] {
			byLibrary[w.LibraryID] = w
		}
	}
	if len(byLibrary) != 2 {
		t.Fatalf("expected two synthetic work items, got %d", len(byLibrary))
	}
	sourceKey := creds[0].SourceKey()
	defer clear(sourceKey)
	prepared := make([]teamsPreparedManifest, 2)
	snapshots := make([]teamsSourceSnapshot, 2)
	sources := make([]teams.SourcesResponse, 2)
	for i, id := range ids[:1] {
		snapshots[i], err = catalogs[i].teamsSourceSnapshot(ctx, sourceKey, teamsSyntheticResolver)
		if err != nil {
			t.Fatal(err)
		}
		prepared[i], sources[i], err = catalogs[i].registerTeamsManifest(ctx, clients[i], descriptors[i], snapshots[i], byLibrary[id], "1.0.0")
		if err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
	}
	if len(sources[0].OwnedSourceTokens) != 1 {
		t.Fatalf("synthetic source was not assigned")
	}
	shared, err := clients[0].Privacy(ctx, byLibrary[ids[0]].ID)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := newTeamsRegistration(prepared[0].Manifest, sources[0], shared)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := teamsPlanFromWork(byLibrary[ids[0]])
	if err != nil {
		t.Fatal(err)
	}
	result, err := catalogs[0].teamsExecuteNumeric(ctx, plan, sourceKey, teamsSyntheticResolver, snapshots[0], registration)
	if err != nil {
		t.Fatal(err)
	}
	if result.Values["total_tokens"] != 123 {
		t.Fatal("unexpected synthetic total")
	}
	payload, _ := json.Marshal(teams.Payload{Outcome: "completed", Form: "snapshot", ObservedAt: time.Now().UTC(), ComputedAt: time.Now().UTC(), Coverage: teams.Coverage{SourceState: "complete", Measurement: "measured", Caveats: []string{}}, Rows: []teams.Row{{Key: []json.RawMessage{}, Values: map[string]json.RawMessage{"total_tokens": json.RawMessage(`123`)}}}, DeleteKeys: [][]json.RawMessage{}})
	work := byLibrary[ids[0]]
	staged, err := catalogs[0].stageTeamsNumeric(ctx, work, descriptors[0], snapshots[0], result, prepared[0], "8134b012-9ab5-4dd4-b3ec-1d5f9e474de7", payload)
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	if staged.State != "approved" {
		t.Fatal("synthetic automatic policy not approved")
	}
	if err = catalogs[0].sendTeamsApproved(ctx, clients[0], descriptors[0], creds[0].OrgID, creds[0].InstallationID, "1.0.0", work.ID); err != nil {
		t.Fatalf("send: %v", err)
	}
	var logged int
	if err = catalogs[0].DB.QueryRow(`SELECT COUNT(*) FROM teams_send_log WHERE work_id=?`, work.ID).Scan(&logged); err != nil || logged != 1 {
		t.Fatal("synthetic receipt not logged")
	}
}
