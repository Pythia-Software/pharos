package archive

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeSized(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMeasureLibrarySplitsCapturesAndCatalogParts(t *testing.T) {
	root := t.TempDir()
	config := defaultConfig(filepath.Join(root, "library.toml"))
	config.Library = true
	config.CatalogPath = filepath.Join(root, "catalog", "catalog.sqlite3")
	config.CaptureRoot = filepath.Join(root, "captures")
	config.ArchiveRoot = filepath.Join(root, "preserved")
	config.StagingRoot = filepath.Join(root, "staging")
	writeSized(t, config.Path, 10)
	writeSized(t, filepath.Join(root, "Pharos.app", "Contents", "pharos"), 200)
	writeSized(t, filepath.Join(config.StagingRoot, "package"), 30)
	writeSized(t, config.CatalogPath, 4000)
	writeSized(t, config.CatalogPath+"-wal", 600)
	writeSized(t, config.CatalogPath+"-shm", 32)
	writeSized(t, config.CatalogPath+".text-explorer.sqlite3", 900)
	writeSized(t, filepath.Join(config.ArchiveRoot, "receipt"), 5)
	captures := func(parts ...string) string { return filepath.Join(append([]string{config.CaptureRoot}, parts...)...) }
	writeSized(t, captures("host-a", "host.json"), 1)
	writeSized(t, captures("host-a", "claude", "manifest.json"), 2)
	writeSized(t, captures("host-a", "claude", "files", "projects", "one.jsonl"), 100)
	writeSized(t, captures("host-a", "claude", "files", "projects", "two.jsonl"), 50)
	writeSized(t, captures("host-a", "claude", "history", "20260101T000000.000000000Z", "projects", "one.jsonl"), 70)
	writeSized(t, captures("host-b", "codex", "files", "rollout.jsonl"), 300)
	// A reader's temporary copy hard-links the capture it reads, and sorts
	// before files/; only what it adds counts, as Other.
	reader := captures("host-a", "claude", ".reader-1.pharos-tmp")
	if err := os.MkdirAll(filepath.Join(reader, "files", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(captures("host-a", "claude", "files", "projects", "one.jsonl"), filepath.Join(reader, "files", "projects", "one.jsonl")); err != nil {
		t.Fatal(err)
	}
	writeSized(t, filepath.Join(reader, ".reader.lock"), 3)

	device, ok := deviceOf(root)
	if !ok {
		t.Fatal("no device")
	}
	sizes := measureLibrary(t.Context(), config, device)
	want := []captureSize{
		{Host: "host-a", Other: 1},
		{Host: "host-a", Source: "claude", Latest: 150, History: 70, Other: 5},
		{Host: "host-b", Source: "codex", Latest: 300},
	}
	if len(sizes.Captures) != len(want) {
		t.Fatalf("captures %+v", sizes.Captures)
	}
	for index := range want {
		if sizes.Captures[index] != want[index] {
			t.Fatalf("capture %d: %+v, want %+v", index, sizes.Captures[index], want[index])
		}
	}
	if sizes.CapturesBytes != 526 || sizes.CatalogBytes != 4632 || sizes.CatalogLogBytes != 632 || sizes.CatalogSidecarBytes != 900 ||
		sizes.PreservedBytes != 5 || sizes.OtherBytes != 240 {
		t.Fatalf("sizes %+v", sizes)
	}
	if sizes.LibraryBytes != 526+4632+5 {
		t.Fatalf("library bytes %d", sizes.LibraryBytes)
	}
}

func TestStorageBreakdownDividesOriginalsAndCatalog(t *testing.T) {
	sizes := librarySizes{
		CatalogBytes: 1000, CatalogLogBytes: 100, CatalogSidecarBytes: 200, OtherBytes: 7,
		Captures: []captureSize{
			{Host: "host-a", Source: "claude", Latest: 600, History: 300, Other: 100},
			{Host: "host-a", Other: 10},
			{Host: "host-b", Source: "conductor", Latest: 90},
		},
	}
	shares := storageShares{
		// host-a saw 3 messages of pharos and 1 of no repository in Claude;
		// host-b's Conductor capture has no conversations to divide by.
		Originals: []storageShare{{"host-a", "claude", "repo-pharos", 3}, {"host-a", "claude", "", 1}},
		Catalog:   []storageShare{{"host-a", "claude", "repo-pharos", 2}, {"host-b", "conductor", "repo-site", 1}},
		Hosts:     map[string]string{"host-a": "Studio", "host-b": "Air"},
		Repositories: map[string]string{
			"repo-pharos": "pharos", "repo-site": "site",
		},
	}
	result := storageBreakdown(sizes, shares, "host-b")
	if total := result["total_bytes"].(int64); total != 1000+10+90+1000+200+7 {
		t.Fatalf("total %d", total)
	}
	parts := map[string]int64{}
	for _, part := range result["parts"].([]storagePart) {
		parts[part.Key] = part.Bytes
	}
	if parts["originals_latest"] != 690 || parts["originals_history"] != 300 || parts["originals_other"] != 110 ||
		parts["catalog_database"] != 900 || parts["catalog_log"] != 100 || parts["catalog_sidecar"] != 200 || parts["other"] != 7 {
		t.Fatalf("parts %v", parts)
	}
	rows := func(key string) map[string]storageRow {
		found := map[string]storageRow{}
		for _, row := range result[key].([]storageRow) {
			found[row.Label] = row
		}
		return found
	}
	// Originals by Mac and source are measured; the catalog (1200 bytes with
	// its caches) is divided by messages, two to one.
	machines := rows("machines")
	if machines["Studio"] != (storageRow{"host-a", "Studio", 1010, 800}) || machines["Air (this Mac)"] != (storageRow{"host-b", "Air (this Mac)", 90, 400}) {
		t.Fatalf("machines %+v", machines)
	}
	sources := rows("sources")
	if sources["claude"].Originals != 1000 || sources["claude"].Catalog != 800 || sources["conductor"].Originals != 90 || sources["conductor"].Catalog != 400 {
		t.Fatalf("sources %+v", sources)
	}
	// host-a's Claude capture is split three to one; its host files and the
	// undivided Conductor capture have no repository.
	repositories := rows("repositories")
	if repositories["pharos"] != (storageRow{"repo-pharos", "pharos", 750, 800}) || repositories["site"] != (storageRow{"repo-site", "site", 0, 400}) ||
		repositories["No repository"] != (storageRow{"", "No repository", 250 + 10 + 90, 0}) {
		t.Fatalf("repositories %+v", repositories)
	}

	override(t, &storageRepositoryLimit, 1)
	listed := storageBreakdown(sizes, shares, "")["repositories"].([]storageRow)
	if len(listed) != 2 || listed[0].Label != "pharos" || listed[1] != (storageRow{"other", "Other repositories", 350, 400}) {
		t.Fatalf("limited repositories %+v", listed)
	}
}

func TestDivideSharesAddsUpExactly(t *testing.T) {
	shares := []storageShare{{Messages: 1}, {Messages: 1}, {Messages: 1}}
	parts := divideShares(100, shares)
	if parts[0]+parts[1]+parts[2] != 100 || parts[0] < 33 || parts[2] < 33 {
		t.Fatalf("parts %v", parts)
	}
	if parts := divideShares(100, nil); len(parts) != 0 {
		t.Fatalf("no shares %v", parts)
	}
}

func TestStorageBreakdownEndpoint(t *testing.T) {
	catalog, config := testCatalog(t)
	execTx(t, catalog.DB,
		`INSERT INTO repositories(id,display_name,created_at,updated_at) VALUES('repo','pharos','x','x')`,
		`INSERT INTO hosts(id,label,first_seen_at,last_seen_at) VALUES('host-a','Studio','x','x'),('host-b','Air','x','x')`,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,repository_id,title,indexed_at) VALUES('w1','claude','local','w1','repo','One','x'),('w2','codex','local','w2',NULL,'Two','x')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,origin,origin_host_id) VALUES('c1','w1','claude','local','c1','/a/c1.jsonl','host-a'),
			('c2','w2','codex','local','c2','/b/c2.jsonl','host-b'),('c3','w1','claude','local','c3','/a/c3.jsonl','merged'),('c4','w2','codex','local','c4','/a/c4.jsonl',NULL)`,
		`INSERT OR REPLACE INTO meta(key,value) VALUES('`+legacyHostKey+`','host-a')`,
		// c1 was captured on both Macs; the catalog holds host-a's copy. c3's
		// copies diverged and were merged; c4 predates origin_host_id and was
		// written from the legacy host.
		`INSERT INTO conversation_sightings(conversation_id,host_id,origin,source_name,messages,first_seen_at,last_seen_at) VALUES
			('c1','host-a','/a/c1.jsonl','claude',30,'x','x'),('c1','host-b','/b/c1.jsonl','claude',30,'x','x'),('c2','host-b','/b/c2.jsonl','codex',10,'x','x'),
			('c3','host-a','/a/c3.jsonl','claude',40,'x','x'),('c3','host-b','/b/c3.jsonl','claude',20,'x','x'),('c4','host-a','/a/c4.jsonl','codex',20,'x','x')`)
	shares, err := catalog.storageShares(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(shares.Originals) != 4 || shares.Hosts["host-b"] != "Air" || shares.Repositories["repo"] != "pharos" {
		t.Fatalf("shares %+v", shares)
	}
	// The merged copy's 40 messages, its largest sighting, are split between
	// its Macs; the legacy copy keeps its sighting's count and source.
	catalogShares := map[storageShare]bool{}
	for _, share := range shares.Catalog {
		catalogShares[share] = true
	}
	for _, want := range []storageShare{{"host-a", "claude", "repo", 50}, {"host-b", "claude", "repo", 20}, {"host-b", "codex", "", 10}, {"host-a", "codex", "", 20}} {
		if !catalogShares[want] {
			t.Fatalf("catalog shares %+v, want %+v among them", shares.Catalog, want)
		}
	}
	if len(shares.Catalog) != 4 {
		t.Fatalf("catalog shares %+v", shares.Catalog)
	}

	quietDriveCache(t)
	t.Cleanup(func() { quietDriveCache(t) })
	server := testServer(t, config, catalog)
	get := func() map[string]any {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/api/health/storage/breakdown", nil)
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status %d: %s", response.Code, response.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	drives.mu.Lock()
	drives.key = driveKey(config)
	drives.state = &driveState{checked: time.Now(), shares: shares, sizes: librarySizes{CatalogBytes: 400,
		Captures: []captureSize{{Host: "host-b", Source: "claude", Latest: 60}, {Host: "host-b", Source: "codex", Latest: 20}}}}
	drives.mu.Unlock()
	body := get()
	if body["status"] != "measured" || body["total_bytes"] != float64(480) {
		t.Fatalf("breakdown %v", body)
	}
	repositories := map[string]float64{}
	for _, row := range body["repositories"].([]any) {
		row := row.(map[string]any)
		repositories[row["label"].(string)+" originals"] = row["originals_bytes"].(float64)
		repositories[row["label"].(string)+" catalog"] = row["catalog_bytes"].(float64)
	}
	if repositories["pharos originals"] != 60 || repositories["No repository originals"] != 20 || repositories["pharos catalog"] != 280 || repositories["No repository catalog"] != 120 {
		t.Fatalf("repositories %v", repositories)
	}
	for _, row := range body["machines"].([]any) {
		if id := row.(map[string]any)["id"]; id != "host-a" && id != "host-b" {
			t.Fatalf("machine %v", row)
		}
	}
}
