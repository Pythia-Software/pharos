package archive

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/gbdubs/pharos/internal/teams"
)

func TestTeamsContentInventoryRequiresCompleteProvedCodexSource(t *testing.T) {
	useHost(t, "synthetic-host")
	c, cfg := testCatalog(t)
	ctx := context.Background()
	d := LibraryDescriptor{ID: "synthetic-library", Instance: "synthetic-instance"}
	app := "synthetic-app"
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	raw, _ := json.Marshal(map[string]any{"version": 1, "origin": "https://synthetic.invalid", "org_id": "00000000-0000-4000-8000-000000000001", "person_id": "00000000-0000-4000-8000-000000000002", "installation_id": "00000000-0000-4000-8000-000000000003", "library_id": d.ID, "library_instance": d.Instance, "application_id": app, "private_key": base64.RawURLEncoding.EncodeToString(key), "source_token_key": base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "metrics_default": map[string]any{"value": false, "policy_version": 1}})
	credential, err := teams.ParseCredential(raw, d.ID, d.Instance, app, false)
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	settings, err := c.setTeamsLocalSettings(ctx, d.ID, d.Instance, credential.OrgID, credential.InstallationID, teamsPayloadHash(key.Public().(ed25519.PublicKey)), true, true, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.requestTeamsSourceProof(ctx, d, settings.SettingsRevision); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "codex")
	file := filepath.Join(root, "sessions", "rollout.jsonl")
	data := codexLines("synthetic-thread", 1) + `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":123},"last_token_usage":{"total_tokens":123}}}}` + "\n"
	writeSourceFile(t, file, data)
	source := SourceConfig{Name: "codex", Kind: "codex", Path: root, Account: "local", Enabled: true}
	cfg.Sources = []SourceConfig{source}
	ingestSource(t, c, source)
	var binding, id string
	var rev int64
	if err = c.DB.QueryRow(`SELECT binding_id FROM teams_source_bindings WHERE superseded_at=''`).Scan(&binding); err != nil {
		t.Fatal(err)
	}
	if err = c.DB.QueryRow(`SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	if err = c.reviewTeamsSourceMapping(ctx, d, credential, binding, "work", rev, true); err != nil {
		t.Fatal(err)
	}
	if err = c.DB.QueryRow(`SELECT id FROM conversations WHERE native_id='synthetic-thread'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	files, err := teamsCodexRollouts(source)
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	base, err := c.teamsObserveV2Codex(ctx, cfg, d, credential)
	if err != nil || len(base.Items) != 1 || base.Coverage.OverlapUnresolved != 0 || base.Coverage.IdentityAmbiguous != 0 {
		t.Fatalf("base=%+v err=%v", base, err)
	}
	messages, _, err := teamsCodexContentFile(source, file, "synthetic-thread")
	if err != nil {
		t.Fatalf("content file: %v", err)
	}
	matched, err := teamsCodexIndexedContentMatches(ctx, c.reader(), id, messages)
	if err != nil || !matched {
		var n int
		_ = c.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE conversation_id=?`, id).Scan(&n)
		t.Fatalf("content mismatch: parsed=%d indexed=%d error=%v", len(messages), n, err)
	}
	inventory, err := c.teamsObserveV2Content(ctx, cfg, d, credential)
	if err != nil || !inventory.Complete || len(inventory.Items) != 3 {
		t.Fatalf("complete inventory: %+v %v", inventory, err)
	}
	for _, item := range inventory.Items {
		if item.Protection != "unknown" {
			t.Fatal("unreviewed content became public")
		}
	}
	input, err := teamsManualReviewInput(ctx, c.reader(), id)
	if err != nil {
		t.Fatal(err)
	}
	privacy, err := c.teamsPrivacy(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.manuallyClearTeamsInput(ctx, id, input, privacy.Revision); err != nil {
		t.Fatal(err)
	}
	inventory, err = c.teamsObserveV2Content(ctx, cfg, d, credential)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range inventory.Items {
		if item.Protection != "public" {
			t.Fatal("manual review did not resolve current input")
		}
	}
	closure, err := teamsContentClosure(inventory, []string{inventory.Candidates[0].ComponentToken})
	if err != nil || len(closure) != 3 {
		t.Fatalf("public closure: %v", err)
	}
	if _, err = c.setTeamsPrivate(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	inventory, err = c.teamsObserveV2Content(ctx, cfg, d, credential)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range inventory.Items {
		if item.Protection != "private" {
			t.Fatal("explicit private mark lost")
		}
	}
	if _, err = teamsContentClosure(inventory, []string{inventory.Candidates[0].ComponentToken}); err == nil {
		t.Fatal("private component selected")
	}
	writeSourceFile(t, filepath.Join(root, "sessions", "rollout-new.jsonl"), codexLines("unindexed", 1))
	if _, err = c.teamsObserveV2Content(ctx, cfg, d, credential); err == nil {
		t.Fatal("unindexed file admitted as complete")
	}
}
