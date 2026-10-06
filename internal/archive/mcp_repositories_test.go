package archive

import (
	"strings"
	"testing"
)

func TestMCPListRepositories(t *testing.T) {
	catalog, _ := testCatalog(t)
	for _, statement := range []string{
		`INSERT INTO repositories(id,canonical_remote,display_name,local_locations_json,created_at,updated_at) VALUES
			('old','git@github.com:acme/parser.git','parser','["/src/parser"]','t','t'),
			('new','https://github.com/acme/web.git','web','["/src/web","/work/berlin"]','t','t'),
			('empty',NULL,'scratch','[]','t','t')`,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,repository_id,title,indexed_at) VALUES
			('w-old','claude','local','w-old','old','Parser','2026-09-01T00:00:00Z'),
			('w-new','conductor','local','w-new','new','Web','2026-09-30T00:00:00Z'),
			('w-none','codex','local','w-none',NULL,'Loose','2026-09-30T00:00:00Z')`,
		`INSERT INTO conversations(id,workspace_id,provider,account,native_id,started_at,ended_at) VALUES
			('c-old','w-old','claude','local','c-old','2026-08-01T00:00:00Z','2026-08-02T00:00:00Z'),
			('c-new','w-new','claude','local','c-new','2026-09-29T00:00:00Z','2026-09-29T12:00:00Z'),
			('c-new-2','w-new','codex','local','c-new-2','2026-09-20T00:00:00Z',NULL),
			('c-none','w-none','codex','local','c-none','2026-09-30T00:00:00Z',NULL)`,
		`INSERT INTO hosts(id,label,first_seen_at,last_seen_at) VALUES('h1','Studio','t','t')`,
		`INSERT INTO automatic_source_states(host_id,source_name,attempted_at,succeeded_at,error) VALUES
			('h1','claude','2026-10-01T00:00:00Z','2026-10-01T00:00:00Z',NULL),
			('h1','codex','2026-10-01T01:00:00Z','2026-09-30T00:00:00Z','codex home missing')`,
	} {
		if _, err := catalog.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	list := func(args map[string]any) map[string]any {
		t.Helper()
		value, err := callMCP(catalog, "list_repositories", args)
		if err != nil {
			t.Fatal(err)
		}
		return value.(map[string]any)
	}
	all := list(map[string]any{})
	items := all["items"].([]map[string]any)
	if len(items) != 3 || items[0]["repository"] != "web" || items[1]["repository"] != "parser" || items[2]["repository"] != "scratch" {
		t.Fatalf("repositories are not newest first: %#v", items)
	}
	if integer(items[0]["conversations"]) != 2 || items[0]["last_message_at"] != "2026-09-29T12:00:00Z" || items[0]["last_indexed_at"] != "2026-09-30T00:00:00Z" {
		t.Fatalf("web summary: %#v", items[0])
	}
	if integer(all["conversations_without_repository"]) != 1 {
		t.Fatalf("unassigned conversations: %#v", all["conversations_without_repository"])
	}
	sync := all["last_sync"].([]map[string]any)
	if len(sync) != 2 || sync[0]["host"] != "Studio" || sync[0]["error"] != nil || sync[1]["error"] != "codex home missing" {
		t.Fatalf("sync state: %#v", sync)
	}
	if items := list(map[string]any{"query": "ACME/PARSER"})["items"].([]map[string]any); len(items) != 1 || items[0]["repository"] != "parser" {
		t.Fatalf("query by remote: %#v", items)
	}
	if items := list(map[string]any{"path": "/work/berlin/internal"})["items"].([]map[string]any); len(items) != 1 || items[0]["repository"] != "web" {
		t.Fatalf("path inside a checkout: %#v", items)
	}
	if items := list(map[string]any{"path": "/src/parser-old"})["items"].([]map[string]any); len(items) != 0 {
		t.Fatalf("path only shares a prefix: %#v", items)
	}
	if limited := list(map[string]any{"limit": 1}); len(limited["items"].([]map[string]any)) != 1 || limited["truncated"] != true || integer(limited["total"]) != 3 {
		t.Fatalf("limit: %#v", limited)
	}
}

func TestMCPRepositoryFilterMustNameARepository(t *testing.T) {
	catalog, _ := testCatalog(t)
	if _, err := catalog.DB.Exec(`INSERT INTO repositories(id,canonical_remote,display_name,local_locations_json,created_at,updated_at) VALUES
		('a','https://github.com/acme/web.git','web','["/work/acme/berlin-v2","/src/web"]','t','t'),
		('b','git@github.com:acme/api.git','api','["/work/acme/oslo"]','t','t')`); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"search_conversations", "search_messages", "search_work", "list_findings", "trace_worktree"} {
		args := map[string]any{"query": "parser"}
		for _, repository := range []string{"web", "WEB", "acme/api"} {
			args["repository"] = repository
			if _, err := callMCP(catalog, tool, args); err != nil {
				t.Errorf("%s accepts %q: %v", tool, repository, err)
			}
		}
		for repository, want := range map[string]string{
			"berlin-v2":              `no repository named "berlin-v2": it is a checkout of "web"; pass that repository name instead`,
			"/work/acme/oslo/server": `it is a checkout of "api"`,
			"work":                   `it is a checkout of "api" or "web"`,
			"payments":               "list_repositories lists the names Pharos knows",
		} {
			_, err := callMCP(catalog, tool, map[string]any{"repository": repository})
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s with %q: got %v, want %q", tool, repository, err, want)
			}
		}
	}
}
