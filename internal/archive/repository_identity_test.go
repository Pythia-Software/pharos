package archive

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeRepositoryRemote(t *testing.T) {
	for _, raw := range []string{"git@GitHub.com:Acme/Widget.git", "https://github.com/acme/widget/", "ssh://git@github.com/ACME/WIDGET.git", "github.com/acme/widget"} {
		if got := normalizeRepositoryRemote(raw); got != "github.com/acme/widget" {
			t.Errorf("%q: %q", raw, got)
		}
	}
}

func TestRepositoryConfigAliases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.toml")
	data := `[repositories.aliases]
"github.com/acme/old" = "github.com/acme/new"
[repositories.separate]
"repo_keep" = true
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.RepositoryAliases["github.com/acme/old"] != "github.com/acme/new" || len(config.RepositorySeparate) != 1 || config.RepositorySeparate[0] != "repo_keep" {
		t.Fatalf("config: %#v", config)
	}
}

func TestRepositoryIdentitySignals(t *testing.T) {
	remote := func(id, slug string) repositoryIdentity {
		return repositoryIdentity{ID: id, Remote: "https://" + slug + ".git", Normalized: slug, Name: filepath.Base(slug)}
	}
	a, b := remote("a", "github.com/one/origin"), remote("b", "github.com/two/origin")
	a.Root, b.Root = "same", "same"
	if groups := planRepositoryMerges([]repositoryIdentity{a, b}, nil); len(groups) != 0 {
		t.Fatalf("different owners merged: %#v", groups)
	}
	if groups := planRepositoryMerges([]repositoryIdentity{{ID: "origin-one", Name: "origin"}, {ID: "origin-two", Name: "origin"}}, nil); len(groups) != 0 {
		t.Fatalf("same names merged: %#v", groups)
	}
	a.Forge, b.Forge = "123", "123"
	if groups := planRepositoryMerges([]repositoryIdentity{a, b}, nil); len(groups) != 1 {
		t.Fatalf("forge ids did not merge: %#v", groups)
	}
	a.Forge, b.Forge = "", ""
	aliases := map[string]string{"github.com/one/origin": "github.com/two/origin"}
	if groups := planRepositoryMerges([]repositoryIdentity{a, b}, aliases); len(groups) != 1 {
		t.Fatalf("config aliases did not merge: %#v", groups)
	}
	byName := repositoryIdentity{ID: "named", Name: "old-name"}
	if groups := planRepositoryMerges([]repositoryIdentity{byName, b}, map[string]string{"old-name": "github.com/two/origin"}); len(groups) != 1 {
		t.Fatalf("display-name alias did not merge: %#v", groups)
	}
	missing := repositoryIdentity{ID: "missing", Name: "origin", Locations: []string{"/old/conductor/workspaces/origin/one"}}
	a.Locations = []string{"/old/conductor/workspaces/origin/two"}
	if groups := planRepositoryMerges([]repositoryIdentity{a, missing}, nil); len(groups) != 1 {
		t.Fatalf("missing remote did not attach: %#v", groups)
	}
	b.Locations = []string{"/other/conductor/workspaces/origin/three"}
	if groups := planRepositoryMerges([]repositoryIdentity{a, b, missing}, nil); len(groups) != 0 {
		t.Fatalf("ambiguous directory merged: %#v", groups)
	}
}

func TestPharosRenamePlan(t *testing.T) {
	rows := []repositoryIdentity{
		{ID: "old", Name: "alexandria", Remote: "https://github.com/gbdubs/alexandria.git", Normalized: "github.com/gbdubs/alexandria", Forge: "1377317940", ForgeCanonical: "github.com/pythia-software/pharos", Workspaces: 227, Locations: []string{"/Users/test/conductor/workspaces/alexandria/one"}},
		{ID: "missing", Name: "alexandria", Workspaces: 57, Locations: []string{"/Users/test/conductor/workspaces/alexandria/two"}},
		{ID: "middle", Name: "pharos", Remote: "https://github.com/gbdubs/pharos.git", Normalized: "github.com/gbdubs/pharos", Forge: "1377317940", ForgeCanonical: "github.com/pythia-software/pharos", Workspaces: 1},
		{ID: "current", Name: "pharos", Remote: "https://github.com/Pythia-Software/pharos.git", Normalized: "github.com/pythia-software/pharos", Forge: "1377317940", ForgeCanonical: "github.com/pythia-software/pharos", Workspaces: 4},
	}
	groups := planRepositoryMerges(rows, nil)
	if len(groups) != 1 || len(groups[0].Losers) != 3 || groups[0].Survivor.ID != "current" || groups[0].Name != "pharos" {
		t.Fatalf("plan: %#v", groups)
	}
}

func TestRepositoryUpsertUnionsRemotes(t *testing.T) {
	catalog, err := OpenCatalog(filepath.Join(t.TempDir(), "catalog.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	tx, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	a, err := upsertRepository(tx, map[string]any{"canonical_remote": "git@github.com:Acme/Widget.git", "display_name": "Widget", "local_locations": []string{"/one"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := upsertRepository(tx, map[string]any{"canonical_remote": "https://github.com/acme/widget/", "display_name": "widget", "local_locations": []string{"/two"}})
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("ids differ: %s %s", a, b)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	items, err := loadRepositoryIdentities(catalog.DB)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || len(items[0].Aliases) != 2 || len(items[0].Locations) != 2 {
		t.Fatalf("repository = %#v", items)
	}
}

func TestRepositoryUpsertConfigAlias(t *testing.T) {
	catalog, err := OpenCatalog(filepath.Join(t.TempDir(), "catalog.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	tx, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	options := repositoryOptions{aliases: map[string]string{"github.com/old/project": "github.com/new/project"}}
	a, err := upsertRepository(tx, map[string]any{"canonical_remote": "https://github.com/old/project.git", "display_name": "project"}, options)
	if err != nil {
		t.Fatal(err)
	}
	b, err := upsertRepository(tx, map[string]any{"canonical_remote": "https://github.com/new/project.git", "display_name": "project"}, options)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("alias IDs differ: %s %s", a, b)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryMergeKeepsPRLinks(t *testing.T) {
	catalog, err := OpenCatalog(filepath.Join(t.TempDir(), "catalog.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	for _, query := range []string{
		`INSERT INTO repositories(id,canonical_remote,display_name,created_at,updated_at) VALUES('a','https://github.com/acme/old.git','old','t','t'),('b','https://github.com/acme/new.git','new','t','t')`,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at,repository_id) VALUES('w','codex','local','w','work','t','a')`,
		`INSERT INTO pull_requests(id,host,repository_id,number,title,commit_refs_json) VALUES('p1','github.com','a',1,'rich','["abc"]'),('p2','github.com','b',1,NULL,'[]')`,
		`INSERT INTO work_pr_links(workspace_id,pr_id,relationship,confidence,evidence_json) VALUES('w','p1','mentions',1,'{}')`,
		`INSERT INTO protections(scope_type,scope_id,mode,reason,created_at) VALUES('repository','a','protect','keep','t')`,
	} {
		if _, err := catalog.DB.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	items, err := loadRepositoryIdentities(catalog.DB)
	if err != nil {
		t.Fatal(err)
	}
	groups := planRepositoryMerges(items, map[string]string{"github.com/acme/old": "github.com/acme/new"})
	if len(groups) != 1 {
		t.Fatalf("groups: %#v", groups)
	}
	if err := mergeRepositoryGroup(catalog.DB, groups[0]); err != nil {
		t.Fatal(err)
	}
	var repo, title, link string
	if err := catalog.DB.QueryRow(`SELECT repository_id FROM workspaces WHERE id='w'`).Scan(&repo); err != nil {
		t.Fatal(err)
	}
	if err := catalog.DB.QueryRow(`SELECT title FROM pull_requests WHERE repository_id=?`, repo).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if err := catalog.DB.QueryRow(`SELECT pr_id FROM work_pr_links WHERE workspace_id='w'`).Scan(&link); err != nil {
		t.Fatal(err)
	}
	if repo != groups[0].Survivor.ID || title != "rich" || link == "" {
		t.Fatalf("repo=%s title=%s link=%s", repo, title, link)
	}
	var protected int
	if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM protections WHERE scope_type='repository' AND scope_id=?`, repo).Scan(&protected); err != nil || protected != 1 {
		t.Fatalf("protection=%d: %v", protected, err)
	}
	items, err = loadRepositoryIdentities(catalog.DB)
	if err != nil {
		t.Fatal(err)
	}
	if len(planRepositoryMerges(items, nil)) != 0 {
		t.Fatal("merge not idempotent")
	}
}
