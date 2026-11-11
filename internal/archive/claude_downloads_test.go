package archive

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const claudeTestManifestName = "manifest-3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13-1700000000-0a1b2c3d-2026-11-10-15-01-48.json"

func claudeDownloadsFixture(t *testing.T, created time.Time, browser func(profile, link string)) (*claudeDownloads, string, *[]string, *[]string) {
	t.Helper()
	config := claudeImportConfig(t)
	downloads := t.TempDir()
	manifest := `{"version":"1.0","created_at":"` + created.UTC().Format(time.RFC3339Nano) + `","total_files":3,"data_files":[` +
		`{"batch_index":2,"category":"light_metadata","part":0,"filename":"light_metadata-000.zip","export_url":"https://claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/account"},` +
		`{"batch_index":0,"category":"conversations","part":0,"filename":"conversations-000.zip","export_url":"https://claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/one"},` +
		`{"batch_index":1,"category":"projects","part":0,"filename":"projects-000.zip","export_url":"https://claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/two"}]}`
	probePut(t, filepath.Join(downloads, claudeTestManifestName), manifest)
	var opened, indexed []string
	d := &claudeDownloads{dir: downloads, name: "claude-ai-imports",
		config: func() Config { return config },
		open:   func(profile, link string) error { opened = append(opened, link); browser(profile, link); return nil },
		collect: func(ctx context.Context, request claudeImportRequest) (*claudeImportJob, error) {
			return collectClaudeExport(ctx, config, request)
		},
		index: func(_ context.Context, name string, _ ProgressFunc) error {
			indexed = append(indexed, name)
			return nil
		},
		now: time.Now,
	}
	return d, downloads, &opened, &indexed
}

func TestClaudeDownloadsOpensEachLinkOnceAndImportsWhatArrives(t *testing.T) {
	var downloads string
	d, dir, opened, indexed := claudeDownloadsFixture(t, time.Now().Add(-time.Hour), func(_, link string) {
		// The browser keeps an older export's file and saves "name (1).zip".
		name := map[string]string{"one": "conversations-000 (1).zip", "two": "projects-000.zip"}[link[strings.LastIndex(link, "/")+1:]]
		writeClaudeAIZip(t, filepath.Join(downloads, name))
	})
	downloads = dir
	index := d.index
	indexAttempts := 0
	d.index = func(ctx context.Context, name string, progress ProgressFunc) error {
		indexAttempts++
		fetch := d.Fetches()[0]
		if fetch.Phase != "indexing" || fetch.Collected != 2 || fetch.Expected != 2 || fetch.Waiting != "" || fetch.WaitingFile != "" {
			t.Fatalf("indexing progress was not published: %+v", fetch)
		}
		if indexAttempts == 1 {
			return errClaudeImportBusy
		}
		return index(ctx, name, progress)
	}
	stale := filepath.Join(dir, "conversations-000.zip")
	writeClaudeAIZip(t, stale)
	old := time.Now().Add(-14 * 24 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	// A file sharing only the name pattern, and an export too old to fetch.
	probePut(t, filepath.Join(dir, "manifest-00000000-0000-0000-0000-000000000000-x.json"), `{"created_at":"2026-01-01T00:00:00Z","data_files":[{"filename":"a.zip","category":"x","export_url":"https://example.com/a"}]}`)
	probePut(t, filepath.Join(dir, "manifest-00000000-0000-0000-0000-000000000002-z.json"), `{"created_at":"`+time.Now().UTC().Format(time.RFC3339Nano)+`","data_files":[{"filename":"Private/report.zip","category":"conversations","export_url":"https://claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/planted"}]}`)
	probePut(t, filepath.Join(dir, "manifest-00000000-0000-0000-0000-000000000001-y.json"), `{"created_at":"`+time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339Nano)+`","data_files":[{"filename":"b.zip","category":"x","export_url":"https://claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/c"}]}`)
	for i := 0; i < 6; i++ {
		if _, err := d.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
		if i == 2 {
			fetch := d.Fetches()[0]
			if fetch.Phase != "queued" || fetch.State != claudeFetchActive || fetch.Collected != 2 || !strings.Contains(fetch.Message, "Indexing starts") {
				t.Fatalf("indexing wait lost the completed collection: %+v", fetch)
			}
		}
	}
	if len(*opened) != 2 || !strings.HasSuffix((*opened)[0], "/one") || !strings.HasSuffix((*opened)[1], "/two") {
		t.Fatal("opened", *opened)
	}
	fetches := d.Fetches()
	if len(fetches) != 1 || fetches[0].State != claudeFetchImported || fetches[0].Collected != 2 || fetches[0].Message != "" || len(*indexed) != 1 {
		t.Fatalf("fetches %+v indexed %v", fetches, *indexed)
	}
	job, err := loadClaudeJob(d.config(), d.name, fetches[0].ID)
	if err != nil || job.State != "collected" {
		t.Fatal(job, err)
	}
	if more, err := d.Check(context.Background()); more || err != nil || len(*opened) != 2 {
		t.Fatal("an imported export was revisited", more, err, *opened)
	}
}

func TestClaudeDownloadsPausesWhenNothingArrivesAndRetriesOnlyOnRequest(t *testing.T) {
	d, _, opened, _ := claudeDownloadsFixture(t, time.Now().Add(-time.Hour), func(string, string) {})
	clock := time.Now()
	d.now = func() time.Time { return clock }
	for i := 0; i < 3; i++ {
		if _, err := d.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(*opened) != 1 || d.Fetches()[0].State != claudeFetchActive {
		t.Fatal("waiting", *opened, d.Fetches())
	}
	clock = clock.Add(claudeArrivalTimeout + time.Second)
	if _, err := d.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	fetch := d.Fetches()[0]
	if fetch.State != claudeFetchPaused || !strings.Contains(fetch.Message, "signed in") {
		t.Fatalf("%+v", fetch)
	}
	// Later checks must not reopen a possibly single-use link on their own.
	if _, err := d.Check(context.Background()); err != nil || len(*opened) != 1 {
		t.Fatal("reopened without a request", err, *opened)
	}
	if err := d.Act(fetch.ID, "retry", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Check(context.Background()); err != nil || len(*opened) != 2 || (*opened)[0] != (*opened)[1] {
		t.Fatal("retry", err, *opened)
	}
	if err := d.Act(fetch.ID, "stop", nil); err != nil || d.Fetches()[0].State != claudeFetchStopped {
		t.Fatal(err, d.Fetches())
	}
	if _, err := d.Check(context.Background()); err != nil || len(*opened) != 2 {
		t.Fatal("a stopped export advanced", err, *opened)
	}
	// Stop cleared the wait; retrying still reopens the uncollected link.
	if err := d.Act(fetch.ID, "retry", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Check(context.Background()); err != nil || len(*opened) != 3 {
		t.Fatal("retry after stop", err, *opened)
	}
}

// A long collection runs without the state lock, Stop cancels it, and the
// person's decision is not overwritten when the step returns.
func TestClaudeDownloadsStopInterruptsCollectionAndKeepsTheDecision(t *testing.T) {
	d, _, opened, _ := claudeDownloadsFixture(t, time.Now().Add(-time.Hour), func(string, string) {})
	started := make(chan struct{})
	d.collect = func(ctx context.Context, request claudeImportRequest) (*claudeImportJob, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	done := make(chan error, 1)
	go func() { _, err := d.Check(context.Background()); done <- err }()
	<-started
	listed := make(chan []claudeFetch, 1)
	go func() { listed <- d.Fetches() }()
	var fetch claudeFetch
	select {
	case list := <-listed:
		if len(list) != 1 {
			t.Fatal(list)
		}
		fetch = list[0]
		if fetch.Phase != "collecting" || fetch.Expected != 2 {
			t.Fatalf("collection progress was not published: %+v", fetch)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listing exports waited for a collection")
	}
	if err := d.Act(fetch.ID, "stop", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not interrupt the collection")
	}
	if list := d.Fetches(); list[0].State != claudeFetchStopped || len(*opened) != 0 {
		t.Fatalf("%+v %v", list, *opened)
	}
}

func TestClaudeDownloadsPausesOnCollectionFailureBeforeAJobExists(t *testing.T) {
	d, _, opened, _ := claudeDownloadsFixture(t, time.Now().Add(-time.Hour), func(string, string) {})
	d.collect = func(context.Context, claudeImportRequest) (*claudeImportJob, error) {
		return nil, errors.New("cannot save the import job")
	}
	if more, err := d.Check(context.Background()); more || err != nil {
		t.Fatal(more, err)
	}
	fetch := d.Fetches()[0]
	if fetch.State != claudeFetchPaused || fetch.Message != "cannot save the import job" || fetch.Expected != 2 || len(*opened) != 0 {
		t.Fatalf("collection failure was hidden: %+v", fetch)
	}
}

func TestClaudeDownloadsReportsIngestionAndRetriesFailure(t *testing.T) {
	var downloads string
	d, dir, opened, _ := claudeDownloadsFixture(t, time.Now().Add(-time.Hour), func(_, link string) {
		name := map[string]string{"one": "conversations-000.zip", "two": "projects-000.zip"}[link[strings.LastIndex(link, "/")+1:]]
		writeClaudeAIZip(t, filepath.Join(downloads, name))
	})
	downloads = dir
	attempt := 0
	d.index = func(_ context.Context, _ string, progress ProgressFunc) error {
		attempt++
		if attempt == 1 {
			progress("capturing", 0, 0, 0, 0, 0)
			progress("indexing", 1, 1, 4, 0, 1)
			fetch := d.Fetches()[0]
			if fetch.Ingestion == nil || fetch.Ingestion.Stage != "indexing" || fetch.Ingestion.Conversations != 1 || fetch.Ingestion.Messages != 4 || fetch.Ingestion.Processed != 1 {
				t.Fatalf("live ingestion progress: %+v", fetch)
			}
			return errors.New("catalog write failed")
		}
		progress("complete", 0, 0, 0, 2, 2)
		return nil
	}
	for i := 0; i < 3; i++ {
		if _, err := d.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	fetch := d.Fetches()[0]
	if fetch.State != claudeFetchPaused || !strings.Contains(fetch.Message, "catalog write failed") || fetch.Collected != 2 {
		t.Fatalf("ingestion failure was hidden: %+v", fetch)
	}
	if err := d.Act(fetch.ID, "retry", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	fetch = d.Fetches()[0]
	if fetch.State != claudeFetchImported || fetch.Ingestion.Stage != "complete" || fetch.Ingestion.SkippedCurrent != 2 || fetch.Ingestion.Processed != 2 || len(*opened) != 2 {
		t.Fatalf("ingestion retry redownloaded files or lost unchanged records: %+v, opened %v", fetch, *opened)
	}
}

func TestClaudeDownloadsStopWhileIndexingKeepsCompletedCollection(t *testing.T) {
	var downloads string
	d, dir, _, _ := claudeDownloadsFixture(t, time.Now().Add(-time.Hour), func(_, link string) {
		name := map[string]string{"one": "conversations-000.zip", "two": "projects-000.zip"}[link[strings.LastIndex(link, "/")+1:]]
		writeClaudeAIZip(t, filepath.Join(downloads, name))
	})
	downloads = dir
	indexed := false
	d.index = func(ctx context.Context, _ string, progress ProgressFunc) error {
		indexed = true
		progress("indexing", 1, 1, 4, 0, 1)
		fetch := d.Fetches()[0]
		if fetch.Phase != "indexing" || fetch.Collected != 2 || fetch.Expected != 2 {
			t.Fatalf("indexing progress: %+v", fetch)
		}
		if err := d.Act(fetch.ID, "stop", nil); err != nil {
			t.Fatal(err)
		}
		if ctx.Err() != context.Canceled {
			t.Fatal("Stop did not cancel indexing")
		}
		progress("complete", 2, 2, 8, 0, 2)
		return ctx.Err()
	}
	for i := 0; i < 3; i++ {
		if _, err := d.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	fetch := d.Fetches()[0]
	if !indexed || fetch.State != claudeFetchStopped || fetch.Collected != 2 || fetch.Expected != 2 || fetch.Ingestion.Workspaces != 1 {
		t.Fatalf("Stop lost the completed collection: %+v", fetch)
	}
}

func TestClaudeDownloadsSeesSafariDownloadsGrow(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "frames-000.zip.download")
	if err := os.Mkdir(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	probePut(t, filepath.Join(bundle, "frames-000.zip"), "partial")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(bundle, old, old); err != nil {
		t.Fatal(err)
	}
	if !claudeDownloadInProgress(dir, time.Now().Add(-time.Minute)) {
		t.Fatal("a growing Safari download was not seen")
	}
}

// The service's watch is off until turned on, opens links through the
// browser launcher, and finishes with the conversations indexed.
func TestClaudeDownloadsWatchSettingAndServiceImport(t *testing.T) {
	config := claudeImportConfig(t)
	server := probeServer(t, config.Path)
	downloads := t.TempDir()
	server.claudeWatch.dir = downloads
	var opened []string
	override(t, &openBrowserURL, func(link string) error {
		opened = append(opened, link)
		name := map[string]string{"one": "conversations-000.zip", "two": "projects-000.zip"}[link[strings.LastIndex(link, "/")+1:]]
		writeClaudeAIZip(t, filepath.Join(downloads, name))
		return nil
	})
	code, response := probeCall(t, server, http.MethodGet, "/api/claude-imports", "")
	if watch := mapValue(response["watch"]); code != 200 || watch["enabled"] != false || watch["available"] != browserOpenAvailable() {
		t.Fatal(code, response)
	}
	listed := 0
	override(t, &listBrowserProfiles, func() []browserProfile {
		listed++
		return []browserProfile{{Key: "chrome:Profile 2", Label: "Google Chrome — Work"}}
	})
	if code, response = probeCall(t, server, http.MethodGet, "/api/claude-imports", ""); code != 200 || listed != 0 {
		t.Fatal("browser profiles were read before the watch was turned on", code, listed)
	}
	code, response = probeCall(t, server, http.MethodPost, "/api/claude-imports/watch", `{"enabled":true}`)
	if code != 200 || response["enabled"] != true || !loadClaudeWatchSetting(server.Config()).Enabled || !strings.Contains(jsonText(response["browsers"]), "Profile 2") {
		t.Fatal(code, response)
	}
	if code, response = probeCall(t, server, http.MethodPost, "/api/claude-imports/watch", `{"browser":"chrome:Profile 9"}`); code != 400 {
		t.Fatal("an unlisted browser was accepted", code, response)
	}
	if code, response = probeCall(t, server, http.MethodPost, "/api/claude-imports/watch", `{"browser":""}`); code != 200 || response["browser"] != "" {
		t.Fatal(code, response)
	}
	created := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	probePut(t, filepath.Join(downloads, claudeTestManifestName), `{"version":"1.0","created_at":"`+created+`","total_files":2,"data_files":[`+
		`{"category":"conversations","part":0,"filename":"conversations-000.zip","export_url":"https://claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/one"},`+
		`{"category":"projects","part":0,"filename":"projects-000.zip","export_url":"https://claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/two"}]}`)
	stages := map[string]bool{}
	index := server.claudeWatch.index
	server.claudeWatch.index = func(ctx context.Context, name string, progress ProgressFunc) error {
		return index(ctx, name, func(stage string, workspaces, conversations, messages, skipped, processed int) {
			progress(stage, workspaces, conversations, messages, skipped, processed)
			stages[stage] = true
			if stage == "complete" {
				fetch := server.claudeWatch.Fetches()[0]
				if fetch.Ingestion == nil || fetch.Ingestion.Conversations != conversations {
					t.Fatalf("committed ingestion progress not visible: %+v", fetch)
				}
			}
		})
	}
	if !server.ingestMu.TryLock() {
		t.Fatal("could not reserve the library for another update")
	}
	more, err := server.claudeWatch.Check(context.Background())
	server.ingestMu.Unlock()
	fetch := server.claudeWatch.Fetches()[0]
	if err != nil || !more || fetch.Phase != "queued" || fetch.Expected != 2 || !strings.Contains(fetch.Message, "resumes automatically") || len(opened) != 0 {
		t.Fatalf("busy library: %+v more=%v err=%v opened=%v", fetch, more, err, opened)
	}
	for i := 0; i < 6; i++ {
		if _, err := server.claudeWatch.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	exports := server.claudeWatch.Fetches()
	if len(opened) != 2 || len(exports) != 1 || exports[0].State != claudeFetchImported || exports[0].Message != "" {
		t.Fatalf("opened %v exports %+v", opened, exports)
	}
	var conversations int
	if err := server.Catalog.DB.QueryRow(`SELECT count(*) FROM conversations`).Scan(&conversations); err != nil || conversations == 0 {
		t.Fatal("not indexed", conversations, err)
	}
	if !stages["capturing"] || !stages["indexing"] || !stages["complete"] || exports[0].Ingestion == nil || exports[0].Ingestion.Stage != "complete" {
		t.Fatal("capture/index stages were not reported", stages, exports)
	}
	var workspaces int
	if err := server.Catalog.DB.QueryRow("SELECT count(*) FROM workspaces").Scan(&workspaces); err != nil {
		t.Fatal(err)
	}
	tx := seedTx(t, server.Catalog.DB)
	for _, other := range []struct{ id, host, source string }{{"other-source", currentHost().ID, "other-imports"}, {"other-host", "another-host", server.claudeWatch.name}} {
		if _, err := tx.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES(?,'claude-ai','',?,?,?)`, other.id, other.id, other.id, now()); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO workspace_sightings(workspace_id,host_id,source_name,first_seen_at,last_seen_at) VALUES(?,?,?,?,?)`, other.id, other.host, other.source, now(), now()); err != nil {
			t.Fatal(err)
		}
	}
	commitSeed(t, tx)
	library := server.claudeImportLibrary()
	if library.Error != "" || library.Workspaces != workspaces || library.Workspaces == 0 || library.Conversations != conversations || len(library.Recent) == 0 {
		t.Fatalf("workspace results: %+v", library)
	}
	for _, workspace := range library.Recent {
		if workspace.ID == "other-source" || workspace.ID == "other-host" {
			t.Fatal("workspace results include another source or host", workspace)
		}
		var title string
		if err := server.Catalog.DB.QueryRow("SELECT title FROM workspaces WHERE id=?", workspace.ID).Scan(&title); err != nil || workspace.Title != title || workspace.URL != "/work/"+workspace.ID {
			t.Fatal("workspace result does not point to the imported workspace", workspace, title, err)
		}
	}
	if code, response = probeCall(t, server, http.MethodPost, "/api/claude-imports/watch", `{"id":"`+exports[0].ID+`","action":"retry"}`); code != 400 {
		t.Fatal("an imported export can be retried", code, response)
	}
	code, response = probeCall(t, server, http.MethodPost, "/api/claude-imports/watch", `{"enabled":false}`)
	if code != 200 || response["enabled"] != false || strings.Contains(jsonText(response), "claude.ai/export") {
		t.Fatal(code, response)
	}
}

func TestClaudeDownloadsOpensOnlyClaudeHTTPSLinks(t *testing.T) {
	profile := browserProfile{Key: "chrome:Default", app: "/Applications/Google Chrome.app", dir: "Default"}
	for _, link := range []string{"file:///System/Applications/Calculator.app", "http://claude.ai/export/a", "https://example.com/export", "-a Calculator", "",
		"https://claude.ai/public/artifacts/0e3c", "https://claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/x?next=/settings", "https://evil.claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/x",
		"https://anthropic.com/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/x", "https://claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/../../settings"} {
		if err := openBrowserURL(link); err == nil || errors.Is(err, errBrowserOpenUnsupported) {
			t.Fatalf("%q: %v", link, err)
		}
		if err := openBrowserProfile(profile, link); err == nil || errors.Is(err, errBrowserOpenUnsupported) {
			t.Fatalf("profile %q: %v", link, err)
		}
	}
	if err := openBrowserProfile(browserProfile{app: "/Applications/Google Chrome.app", dir: "--incognito"}, "https://claude.ai/export/a"); err == nil || errors.Is(err, errBrowserOpenUnsupported) {
		t.Fatal("a flag-like profile directory was accepted", err)
	}
}

// Two Chrome profiles: the default one is not signed in to this Claude
// account. Retrying in the other profile is remembered for the account.
func TestClaudeDownloadsRetriesInAnotherProfileAndRemembersIt(t *testing.T) {
	var downloads string
	var profiles []string
	d, dir, opened, _ := claudeDownloadsFixture(t, time.Now().Add(-time.Hour), func(profile, link string) {
		profiles = append(profiles, profile)
		if profile != "chrome:Profile 2" {
			return
		}
		name := map[string]string{"one": "conversations-000.zip", "two": "projects-000.zip", "three": "conversations-001.zip"}[link[strings.LastIndex(link, "/")+1:]]
		writeClaudeAIZip(t, filepath.Join(downloads, name))
	})
	downloads = dir
	config := d.config()
	if err := saveClaudeWatchSetting(config, claudeWatchSetting{Enabled: true, Browser: "chrome:Default"}); err != nil {
		t.Fatal(err)
	}
	override(t, &listBrowserProfiles, func() []browserProfile {
		return []browserProfile{{Key: "chrome:Default", Label: "Google Chrome — Personal"}, {Key: "chrome:Profile 2", Label: "Google Chrome — Work"}}
	})
	clock := time.Now()
	d.now = func() time.Time { return clock }
	check := func() {
		t.Helper()
		if _, err := d.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	check()
	clock = clock.Add(claudeArrivalTimeout + time.Second)
	check()
	fetch := d.Fetches()[0]
	if fetch.State != claudeFetchPaused || fetch.Account != "3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13" || !strings.Contains(fetch.Message, "Google Chrome — Personal") {
		t.Fatalf("%+v", fetch)
	}
	if _, ok := loadClaudeWatchSetting(config).Accounts[fetch.Account]; ok {
		t.Fatal("a profile that delivered nothing was remembered")
	}
	work := "chrome:Profile 2"
	if err := d.Act(fetch.ID, "retry", &work); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		check()
	}
	if fetch := d.Fetches()[0]; fetch.State != claudeFetchImported || strings.Join(profiles, ",") != "chrome:Default,chrome:Profile 2,chrome:Profile 2" {
		t.Fatalf("%+v %v", fetch, profiles)
	}
	if setting := loadClaudeWatchSetting(config); setting.Accounts[fetch.Account] != work || setting.Browser != "chrome:Default" {
		t.Fatalf("%+v", setting)
	}
	// The account's next export goes straight to the profile that worked.
	probePut(t, filepath.Join(dir, strings.Replace(claudeTestManifestName, "2026-11-10", "2026-11-11", 1)), `{"version":"1.0","created_at":"`+clock.UTC().Format(time.RFC3339Nano)+`","data_files":[`+
		`{"category":"conversations","part":1,"filename":"conversations-001.zip","export_url":"https://claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/three"}]}`)
	profiles = nil
	for i := 0; i < 3; i++ {
		check()
	}
	if strings.Join(profiles, ",") != work || len(*opened) != 4 {
		t.Fatal(profiles, *opened)
	}
}

func TestClaudeDownloadsBrowserProfilesAndAccounts(t *testing.T) {
	state := []byte(`{"profile":{"info_cache":{"Default":{"name":"Personal","user_name":"me@example.com"},"Profile 2":{"name":"Work"},"-flag":{"name":"x"},"../escape":{"name":"y"}}}}`)
	profiles := chromiumProfiles(chromiumBrowsers[0], "/Applications/Google Chrome.app", state)
	if len(profiles) != 2 || profiles[0].Key != "chrome:Default" || profiles[0].Label != "Google Chrome — Personal (me@example.com)" || profiles[1].dir != "Profile 2" {
		t.Fatalf("%+v", profiles)
	}
	manifest := claudeManifest{DataFiles: []claudeManifestPart{{URL: "https://claude.ai/export/3F1B6C0E-5a2d-4e8b-9c71-2d4a6b8e0f13/download/x"}}}
	if account := claudeExportAccount(manifest, "manifest.json"); account != "3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13" {
		t.Fatal(account)
	}
	if account := claudeExportAccount(claudeManifest{}, claudeTestManifestName); account != "3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13" {
		t.Fatal(account)
	}
	var launched []string
	override(t, &listBrowserProfiles, func() []browserProfile { return profiles })
	override(t, &openBrowserProfile, func(profile browserProfile, link string) error { launched = append(launched, profile.dir); return nil })
	if err := openClaudeLink("chrome:Profile 2", "https://claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/b"); err != nil || strings.Join(launched, ",") != "Profile 2" {
		t.Fatal(err, launched)
	}
	if err := openClaudeLink("chrome:Profile 9", "https://claude.ai/export/3f1b6c0e-5a2d-4e8b-9c71-2d4a6b8e0f13/download/b"); err == nil || len(launched) != 1 {
		t.Fatal("a profile that is not listed opened something", err, launched)
	}
}
