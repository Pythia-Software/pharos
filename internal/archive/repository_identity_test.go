package archive

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
"https://GitHub.com/Acme/Old.git" = "git@github.com:Acme/New.git"
"Alexandria" = "https://github.com/Acme/New.git"
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
	if config.RepositoryAliases["github.com/acme/old"] != "github.com/acme/new" || config.RepositoryAliases["alexandria"] != "github.com/acme/new" || len(config.RepositorySeparate) != 1 || config.RepositorySeparate[0] != "repo_keep" {
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
		// Two remote rows claim Conductor directory alexandria, but both land
		// in the same group, so the remoteless row still attaches.
		{ID: "middle", Name: "pharos", Remote: "https://github.com/gbdubs/pharos.git", Normalized: "github.com/gbdubs/pharos", Forge: "1377317940", ForgeCanonical: "github.com/pythia-software/pharos", Workspaces: 1, Locations: []string{"/Users/test/conductor/workspaces/alexandria/stuttgart"}},
		{ID: "current", Name: "pharos", Remote: "https://github.com/Pythia-Software/pharos.git", Normalized: "github.com/pythia-software/pharos", Forge: "1377317940", ForgeCanonical: "github.com/pythia-software/pharos", Workspaces: 4},
	}
	groups := planRepositoryMerges(rows, nil)
	if len(groups) != 1 || len(groups[0].Losers) != 3 || groups[0].Survivor.ID != "current" || groups[0].Name != "pharos" {
		t.Fatalf("plan: %#v", groups)
	}
	// Without the forge redirect the two owners form separate groups, and the
	// shared directory is ambiguous again.
	for i := range rows {
		rows[i].Forge, rows[i].ForgeCanonical = "", ""
	}
	for _, group := range planRepositoryMerges(rows, nil) {
		for _, item := range append([]repositoryIdentity{group.Survivor}, group.Losers...) {
			if item.ID == "missing" {
				t.Fatalf("attached across groups: %#v", group)
			}
		}
	}
}

func repositoryGroupIDs(group repositoryMergeGroup) map[string]bool {
	ids := map[string]bool{group.Survivor.ID: true}
	for _, loser := range group.Losers {
		ids[loser.ID] = true
	}
	return ids
}

func TestRepositoryRemotelessCheckoutAttach(t *testing.T) {
	remote := func(id, slug string, workspaces int, locations ...string) repositoryIdentity {
		return repositoryIdentity{ID: id, Remote: "git@" + strings.Replace(slug, "/", ":", 1) + ".git", Normalized: slug, Name: filepath.Base(slug), Workspaces: workspaces, Locations: locations}
	}
	local := func(id, name string, workspaces int, locations ...string) repositoryIdentity {
		return repositoryIdentity{ID: id, Name: name, Workspaces: workspaces, Locations: locations}
	}

	// A remoteless row at the clone path joins the clone's group.
	rows := []repositoryIdentity{
		remote("pythia", "github.com/pythia-software/excel-corpus", 327, "/Users/t/gbdubs/excel-corpus", "/Users/t/gbdubs/excel-corpus/.task-worktrees/e60-attempt-1"),
		remote("gbdubs", "github.com/pythia-software/excel-corpus", 292, "/Users/t/gbdubs/excel-corpus"),
		local("tl1", "excel-corpus", 10323, "/Users/t/gbdubs/excel-corpus"),
	}
	groups := planRepositoryMerges(rows, nil)
	if len(groups) != 1 || !repositoryGroupIDs(groups[0])["tl1"] || groups[0].Survivor.ID != "pythia" || groups[0].Name != "excel-corpus" {
		t.Fatalf("clone path: %#v", groups)
	}

	// Worktrees in known in-clone layouts map to the clone, in either direction.
	for _, worktree := range []string{"/Users/t/gbdubs/grady.dev/.conductor/puebla-v5", "/Users/t/gbdubs/grady.dev/.task-worktrees/a1", "/Users/t/gbdubs/grady.dev/.claude/worktrees/agent-1"} {
		rows = []repositoryIdentity{remote("site", "github.com/gbdubs/grady.dev", 38, "/Users/t/gbdubs/grady.dev"), local("wt", "grady.dev", 4, worktree)}
		if groups := planRepositoryMerges(rows, nil); len(groups) != 1 || !repositoryGroupIDs(groups[0])["wt"] || groups[0].Signals[len(groups[0].Signals)-1] != "local checkout" {
			t.Fatalf("%s: %#v", worktree, groups)
		}
		rows = []repositoryIdentity{remote("site", "github.com/gbdubs/grady.dev", 38, worktree), local("clone", "grady.dev", 4, "/Users/t/gbdubs/grady.dev")}
		if groups := planRepositoryMerges(rows, nil); len(groups) != 1 {
			t.Fatalf("clone of %s: %#v", worktree, groups)
		}
	}
	// A deeper path is not truncated to an ancestor clone.
	rows = []repositoryIdentity{remote("site", "github.com/gbdubs/grady.dev", 38, "/Users/t/gbdubs/grady.dev"), local("nested", "grady.dev", 4, "/Users/t/gbdubs/grady.dev/.conductor/puebla-v5/.context/scratch")}
	if groups := planRepositoryMerges(rows, nil); len(groups) != 0 {
		t.Fatalf("nested path attached: %#v", groups)
	}

	// A path shared by two different groups is ambiguous. Without a shared root
	// commit nothing says the two remotes are one repository.
	rows = []repositoryIdentity{
		remote("mine", "github.com/one/widget", 5, "/Users/t/widget"),
		remote("theirs", "github.com/two/widget", 5, "/Users/t/widget"),
		local("orphan", "widget", 9, "/Users/t/widget"),
	}
	if groups := planRepositoryMerges(rows, nil); len(groups) != 0 {
		t.Fatalf("ambiguous path merged: %#v", groups)
	}

	// explo-candidate-v2 is a TL1 project name for the explo clone. The
	// remoteless rows join explo, and the group keeps explo's name even though
	// the remoteless rows have more workspaces than the remote rows.
	rows = []repositoryIdentity{
		remote("ssh", "github.com/pythia-software/explo", 464, "/Users/t/gbdubs/explo", "/Users/t/gbdubs/explo/.conductor/kampala-v6"),
		remote("https", "github.com/pythia-software/explo", 300, "/Users/t/gbdubs/explo/.conductor/moscow-v1"),
		local("candidate", "explo-candidate-v2", 2631, "/Users/t/gbdubs/explo"),
		local("bare", "explo", 2447, "/Users/t/gbdubs/explo", "/Users/t/gbdubs/explo/.conductor/los-angeles-v2"),
	}
	groups = planRepositoryMerges(rows, nil)
	if len(groups) != 1 || len(groups[0].Losers) != 3 || groups[0].Survivor.ID != "ssh" || groups[0].Name != "explo" {
		t.Fatalf("explo: %#v", groups)
	}

	// A TL1 scratch origin.git nested in a Conductor workspace neither merges
	// nor makes the workspace directory ambiguous.
	origin := repositoryIdentity{ID: "origin", Name: "origin", Remote: "/Users/t/conductor/workspaces/tl1/edinburgh-v2/.context/run1/origin.git", Workspaces: 10, Locations: []string{"/Users/t/conductor/workspaces/tl1/edinburgh-v2/.context/run1/repo/.candidate-worktrees/4c11"}}
	rows = []repositoryIdentity{
		remote("tl1", "github.com/gbdubs/tl1", 261, "/Users/t/gbdubs/tl1", "/Users/t/conductor/workspaces/tl1/yellowknife-v1"),
		origin,
		local("missing", "tl1", 19, "/Users/t/conductor/workspaces/tl1/west-monroe-v8"),
	}
	groups = planRepositoryMerges(rows, nil)
	if len(groups) != 1 || !repositoryGroupIDs(groups[0])["missing"] || repositoryGroupIDs(groups[0])["origin"] {
		t.Fatalf("tl1: %#v", groups)
	}
}

func TestRepositoryRemotelessAttachChecksCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	clone := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "git@github.com:other/widget.git"}} {
		if output, err := exec.Command("git", append([]string{"-C", clone}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	rows := []repositoryIdentity{
		{ID: "acme", Remote: "https://github.com/acme/widget.git", Normalized: "github.com/acme/widget", Name: "widget", Locations: []string{clone}},
		{ID: "local", Name: "widget", Locations: []string{clone}},
	}
	if groups := planRepositoryMerges(rows, nil); len(groups) != 0 {
		t.Fatalf("checkout origin disagrees but merged: %#v", groups)
	}
	if groups := planRepositoryMerges(rows, map[string]string{"github.com/other/widget": "github.com/acme/widget"}); len(groups) != 1 {
		t.Fatalf("aliased checkout origin did not attach: %#v", groups)
	}
}

func TestRepositoryUpsertRemotelessJoinsGroup(t *testing.T) {
	catalog, err := OpenCatalog(filepath.Join(t.TempDir(), "catalog.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	for _, query := range []string{
		`INSERT INTO repositories(id,canonical_remote,normalized_remote,display_name,forge_id,local_locations_json,created_at,updated_at) VALUES
			('alexandria','https://github.com/gbdubs/alexandria.git','github.com/gbdubs/alexandria','alexandria','42','["/Users/t/conductor/workspaces/alexandria/one"]','t','t'),
			('pharos','https://github.com/gbdubs/pharos.git','github.com/gbdubs/pharos','pharos','42','["/Users/t/conductor/workspaces/alexandria/stuttgart"]','t','t'),
			('explo','git@github.com:acme/explo.git','github.com/acme/explo','explo',NULL,'["/Users/t/gbdubs/explo"]','t','t'),
			('mine','https://github.com/one/widget.git','github.com/one/widget','widget',NULL,'["/Users/t/widget"]','t','t'),
			('theirs','https://github.com/two/widget.git','github.com/two/widget','widget',NULL,'["/Users/t/widget"]','t','t')`,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at,repository_id) VALUES('w','codex','local','w','work','t','alexandria')`,
	} {
		if _, err := catalog.DB.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	id, err := upsertRepository(tx, map[string]any{"display_name": "alexandria", "local_locations": []string{"/Users/t/conductor/workspaces/alexandria/abu-dhabi-v11"}})
	if err != nil {
		t.Fatal(err)
	}
	if id != "alexandria" {
		t.Fatalf("conductor directory in one group: %s", id)
	}
	id, err = upsertRepository(tx, map[string]any{"display_name": "explo-candidate-v2", "local_locations": []string{"/Users/t/gbdubs/explo/.conductor/kyoto"}})
	if err != nil {
		t.Fatal(err)
	}
	var name string
	if err := tx.QueryRow(`SELECT display_name FROM repositories WHERE id=?`, id).Scan(&name); err != nil || id != "explo" || name != "explo" {
		t.Fatalf("clone worktree: id=%s name=%s err=%v", id, name, err)
	}
	id, err = upsertRepository(tx, map[string]any{"display_name": "widget", "local_locations": []string{"/Users/t/widget"}})
	if err != nil {
		t.Fatal(err)
	}
	if id == "mine" || id == "theirs" {
		t.Fatalf("ambiguous clone joined %s", id)
	}
	id, err = upsertRepository(tx, map[string]any{"display_name": "origin", "canonical_remote": "/Users/t/gbdubs/explo/.conductor/kyoto/.context/run/origin.git", "local_locations": []string{"/Users/t/gbdubs/explo"}})
	if err != nil {
		t.Fatal(err)
	}
	if id == "explo" {
		t.Fatal("local origin.git joined explo")
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

func TestRepositoryUpsertLocalWorktreesAndSeparateRow(t *testing.T) {
	catalog, err := OpenCatalog(filepath.Join(t.TempDir(), "catalog.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	legacyID := stableID("repo", "local", nil)
	if _, err := catalog.DB.Exec(`INSERT INTO repositories(id,display_name,local_locations_json,created_at,updated_at) VALUES(?,?,?,?,?)`, legacyID, "local", jsonText([]string{"/old/conductor/workspaces/local/one"}), now(), now()); err != nil {
		t.Fatal(err)
	}
	tx, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/old/conductor/workspaces/local/two", "/old/conductor/workspaces/local/three"} {
		id, err := upsertRepository(tx, map[string]any{"display_name": "local", "local_locations": []string{path}})
		if err != nil {
			t.Fatal(err)
		}
		if id != legacyID {
			t.Fatalf("%s created %s, wanted %s", path, id, legacyID)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	items, err := loadRepositoryIdentities(catalog.DB)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || len(items[0].Locations) != 3 {
		t.Fatalf("local repositories: %#v", items)
	}

	oldRemote := "https://github.com/acme/one.git"
	separateID := stableID("repo", oldRemote, nil)
	if _, err := catalog.DB.Exec(`INSERT INTO repositories(id,canonical_remote,display_name,created_at,updated_at) VALUES(?,?,?,?,?)`, separateID, oldRemote, "one", now(), now()); err != nil {
		t.Fatal(err)
	}
	options := repositoryOptions{aliases: map[string]string{"github.com/acme/one": "github.com/acme/two"}, separate: []string{separateID}}
	tx, err = catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	id, err := upsertRepository(tx, map[string]any{"canonical_remote": "git@github.com:ACME/ONE.git", "display_name": "one"}, options)
	if err != nil {
		t.Fatal(err)
	}
	if id != separateID {
		t.Fatalf("separate row split: %s != %s", id, separateID)
	}
	other, err := upsertRepository(tx, map[string]any{"canonical_remote": "https://github.com/acme/two.git", "display_name": "two"}, options)
	if err != nil {
		t.Fatal(err)
	}
	if other == separateID {
		t.Fatal("separate row merged with alias target")
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
		`INSERT INTO pull_requests(id,host,repository_id,number,title,commit_refs_json) VALUES('p1','github.com','a',1,'rich','["abc"]'),('p2','github.com','b',1,NULL,'[]'),('p3','github.com','a',2,'second','[]')`,
		`INSERT INTO work_pr_links(workspace_id,pr_id,relationship,confidence,evidence_json) VALUES('w','p1','mentions',1,'{}'),('w','p3','mentions',1,'{}')`,
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
	if err := catalog.mergeRepositoryGroup(context.Background(), groups[0]); err != nil {
		t.Fatal(err)
	}
	var repo, title string
	if err := catalog.DB.QueryRow(`SELECT repository_id FROM workspaces WHERE id='w'`).Scan(&repo); err != nil {
		t.Fatal(err)
	}
	if err := catalog.DB.QueryRow(`SELECT title FROM pull_requests WHERE repository_id=? AND number=1`, repo).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if repo != groups[0].Survivor.ID || title != "rich" {
		t.Fatalf("repo=%s title=%s", repo, title)
	}
	for _, number := range []int{1, 2} {
		var id string
		if err := catalog.DB.QueryRow(`SELECT id FROM pull_requests WHERE repository_id=? AND number=?`, repo, number).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if want := stableID("pr", "github.com", repo, number); id != want {
			t.Fatalf("PR %d id=%s, want %s", number, id, want)
		}
	}
	var links int
	if err := catalog.DB.QueryRow(`SELECT COUNT(*) FROM work_pr_links WHERE workspace_id='w'`).Scan(&links); err != nil || links != 2 {
		t.Fatalf("links=%d: %v", links, err)
	}
	tx, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := upsertPRs(tx, "w", repo, []map[string]any{{"host": "github.com", "number": 1, "title": "rich"}, {"host": "github.com", "number": 2, "title": "second"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := upsertRepository(tx, map[string]any{"canonical_remote": "https://github.com/acme/old.git", "display_name": "old"}, repositoryOptions{aliases: map[string]string{"github.com/acme/old": "github.com/acme/new"}}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var remote, name string
	if err := catalog.DB.QueryRow(`SELECT canonical_remote,display_name FROM repositories WHERE id=?`, repo).Scan(&remote, &name); err != nil {
		t.Fatal(err)
	}
	if remote != "https://github.com/acme/new.git" || name != "new" {
		t.Fatalf("merge identity overwritten: remote=%s name=%s", remote, name)
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

func TestRepositorySharedCheckoutMergesMovedRemote(t *testing.T) {
	row := func(id, slug, root string, locations ...string) repositoryIdentity {
		return repositoryIdentity{ID: id, Remote: "git@" + strings.Replace(slug, "/", ":", 1) + ".git", Normalized: slug, Name: filepath.Base(slug), Root: root, Locations: locations}
	}
	// One clone that had both remotes over time is one repository, without GitHub.
	moved := []repositoryIdentity{
		row("old", "github.com/gbdubs/excel-corpus", "r1", "/Users/t/gbdubs/excel-corpus"),
		row("new", "github.com/pythia-software/excel-corpus", "r1", "/Users/t/gbdubs/excel-corpus/.task-worktrees/e1"),
	}
	groups := planRepositoryMerges(moved, nil)
	if len(groups) != 1 || len(groups[0].Losers) != 1 || !containsString(groups[0].Signals, "shared checkout") {
		t.Fatalf("moved remote: %#v", groups)
	}
	// The same root commit in different checkouts is a fork, or another clone.
	fork := []repositoryIdentity{
		row("mine", "github.com/one/widget", "r1", "/Users/t/one/widget"),
		row("theirs", "github.com/two/widget", "r1", "/Users/t/two/widget"),
	}
	if groups := planRepositoryMerges(fork, nil); len(groups) != 0 {
		t.Fatalf("fork merged: %#v", groups)
	}
	// A checkout alone, with different root commits, proves nothing either.
	unrelated := []repositoryIdentity{
		row("a", "github.com/one/widget", "r1", "/Users/t/widget"),
		row("b", "github.com/two/widget", "r2", "/Users/t/widget"),
	}
	if groups := planRepositoryMerges(unrelated, nil); len(groups) != 0 {
		t.Fatalf("different roots merged: %#v", groups)
	}
	// Ingest recognizes it too: the new remote joins the old row.
	if !repositorySameIdentity(moved[0], moved[1], nil, nil) {
		t.Fatal("ingest did not recognize the moved remote")
	}
}

func TestRepositoryTL1OriginsStaySeparate(t *testing.T) {
	// Ten stress-test bare repositories share a root commit and the checkout
	// they were cloned into, but each is its own repository.
	var rows []repositoryIdentity
	for i := 0; i < 10; i++ {
		remote := fmt.Sprintf("/Users/t/conductor/workspaces/tl1/edinburgh-v2/.context/run%d/origin.git", i)
		rows = append(rows, repositoryIdentity{ID: fmt.Sprintf("origin-%d", i), Name: "origin", Remote: remote, Root: "r1", Workspaces: 3,
			Locations: []string{"/Users/t/conductor/workspaces/tl1/edinburgh-v2/.context/repo/.candidate-worktrees/c" + fmt.Sprint(i)}})
	}
	rows = append(rows, repositoryIdentity{ID: "tl1", Name: "tl1", Remote: "git@github.com:gbdubs/tl1.git", Normalized: "github.com/gbdubs/tl1", Root: "r1", Locations: []string{"/Users/t/conductor/workspaces/tl1/edinburgh-v2/.context/repo"}})
	if groups := planRepositoryMerges(rows, nil); len(groups) != 0 {
		t.Fatalf("origin repositories merged: %#v", groups)
	}
	if id := repositoryIdentityKey(rows[0]); id != rows[0].Remote {
		t.Fatalf("identity of a local remote = %q", id)
	}
}

func TestRepositoryRemotelessDuplicatesMerge(t *testing.T) {
	local := func(id, name string, workspaces int, locations ...string) repositoryIdentity {
		return repositoryIdentity{ID: id, Name: name, Workspaces: workspaces, Locations: locations}
	}
	// Remoteless rows of one repository, with no row that has a remote, merge
	// by name and checkout, or by Conductor repository directory.
	rows := []repositoryIdentity{
		local("a", "xlsxl-agy", 1, "/Users/t/conductor/workspaces/xlsxl-agy/one"),
		local("b", "xlsxl-agy", 2, "/Users/t/conductor/workspaces/xlsxl-agy/two"),
		local("c", "xlsxl-agy", 5, "/Users/t/gbdubs/xlsxl-agy/.task-worktrees/t1"),
		local("d", "xlsxl-agy", 1, "/Users/t/gbdubs/xlsxl-agy"),
		local("other", "elsewhere", 1, "/Users/t/conductor/workspaces/xlsxl-agy/three"),
	}
	groups := planRepositoryMerges(rows, nil)
	if len(groups) != 2 {
		t.Fatalf("groups: %#v", groups)
	}
	for _, group := range groups {
		ids := repositoryGroupIDs(group)
		if ids["other"] || ids["a"] != ids["b"] || ids["c"] != ids["d"] || ids["a"] == ids["c"] {
			t.Fatalf("group %v", ids)
		}
	}

	// They also join the group of a sibling that attached to a repository with
	// a remote, but never bridge two such groups.
	keyed := func(id, slug string, locations ...string) repositoryIdentity {
		return repositoryIdentity{ID: id, Remote: "git@" + strings.Replace(slug, "/", ":", 1) + ".git", Normalized: slug, Name: filepath.Base(slug), Workspaces: 10, Locations: locations}
	}
	rows = []repositoryIdentity{
		keyed("remote", "github.com/acme/excel-corpus", "/Users/t/gbdubs/excel-corpus"),
		local("clone", "excel-corpus", 6122, "/Users/t/gbdubs/excel-corpus"),
		local("old", "excel-corpus", 4, "/Users/t/gbdubs/excel-corpus", "/Users/t/conductor/workspaces/excel-corpus/one"),
		local("worktree", "excel-corpus", 2, "/Users/t/conductor/workspaces/excel-corpus/two"),
	}
	groups = planRepositoryMerges(rows, nil)
	if len(groups) != 1 || len(groups[0].Losers) != 3 || groups[0].Survivor.ID != "remote" {
		t.Fatalf("attached siblings: %#v", groups)
	}
	rows = []repositoryIdentity{
		keyed("one", "github.com/acme/widget", "/Users/t/one/widget"),
		keyed("two", "github.com/other/widget", "/Users/t/two/widget"),
		local("a", "widget", 1, "/Users/t/one/widget", "/Users/t/conductor/workspaces/widget/w1"),
		local("b", "widget", 1, "/Users/t/two/widget", "/Users/t/conductor/workspaces/widget/w2"),
		local("both", "widget", 1, "/Users/t/conductor/workspaces/widget/w3"),
	}
	for _, group := range planRepositoryMerges(rows, nil) {
		if ids := repositoryGroupIDs(group); ids["one"] && ids["two"] || ids["both"] {
			t.Fatalf("bridged two repositories: %#v", group)
		}
	}
}

func TestRepositoryRemotelessAttachUsesCheckoutOrigin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	clone := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "git@github.com:other/widget.git"}} {
		if output, err := exec.Command("git", append([]string{"-C", clone}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	// Two repositories claim the clone. Its current origin says which one the
	// remoteless row belongs to.
	rows := []repositoryIdentity{
		{ID: "acme", Remote: "https://github.com/acme/widget.git", Normalized: "github.com/acme/widget", Name: "widget", Root: "r1", Workspaces: 9, Locations: []string{clone}},
		{ID: "other", Remote: "https://github.com/other/widget.git", Normalized: "github.com/other/widget", Name: "widget", Root: "r2", Workspaces: 1, Locations: []string{clone}},
		{ID: "local", Name: "widget", Workspaces: 3, Locations: []string{clone}},
	}
	groups := planRepositoryMerges(rows, nil)
	if len(groups) != 1 || groups[0].Survivor.ID != "other" || !repositoryGroupIDs(groups[0])["local"] || repositoryGroupIDs(groups[0])["acme"] || !containsString(groups[0].Signals, "checkout origin") {
		t.Fatalf("origin tie-break: %#v", groups)
	}
	// A checkout whose origin names neither stays ambiguous.
	if output, err := exec.Command("git", "-C", clone, "remote", "set-url", "origin", "git@github.com:third/widget.git").CombinedOutput(); err != nil {
		t.Fatalf("git: %v %s", err, output)
	}
	if groups := planRepositoryMerges(rows, nil); len(groups) != 0 {
		t.Fatalf("unrelated origin merged: %#v", groups)
	}
}

func repositoryCount(t *testing.T, db repositoryQuerier) int {
	t.Helper()
	items, err := loadRepositoryIdentities(db)
	if err != nil {
		t.Fatal(err)
	}
	return len(items)
}

func TestRepositoryUpsertRemotelessWorktreesOneRow(t *testing.T) {
	catalog, err := OpenCatalog(filepath.Join(t.TempDir(), "catalog.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	tx, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// Ten workspaces of one remoteless repository, in ten Conductor worktrees.
	first := ""
	for i := 0; i < 10; i++ {
		id, err := upsertRepository(tx, map[string]any{"display_name": "widget", "local_locations": []string{fmt.Sprintf("/Users/t/conductor/workspaces/widget/w%d", i)}})
		if err != nil {
			t.Fatal(err)
		}
		if first == "" {
			first = id
		} else if id != first {
			t.Fatalf("workspace %d created %s, want %s", i, id, first)
		}
	}
	if n := repositoryCount(t, tx); n != 1 {
		t.Fatalf("%d rows for one repository", n)
	}
	// So are worktrees of a plain clone, in every layout, and the clone itself.
	for _, path := range []string{"/Users/t/src/tool/.task-worktrees/a", "/Users/t/src/tool/.conductor/b", "/Users/t/src/tool/.claude/worktrees/c", "/Users/t/src/tool/.candidate-worktrees/d", "/Users/t/src/tool"} {
		if _, err := upsertRepository(tx, map[string]any{"display_name": "tool", "local_locations": []string{path}}); err != nil {
			t.Fatal(err)
		}
	}
	if n := repositoryCount(t, tx); n != 2 {
		t.Fatalf("%d rows for two repositories", n)
	}
	// A different name in the same place is a different repository.
	if _, err := upsertRepository(tx, map[string]any{"display_name": "Tool-Fork", "local_locations": []string{"/Users/t/src/tool/.task-worktrees/e"}}); err != nil {
		t.Fatal(err)
	}
	if n := repositoryCount(t, tx); n != 3 {
		t.Fatalf("%d rows", n)
	}
}

func TestRepositoryUpsertRemotelessReusesLegacyRow(t *testing.T) {
	catalog, err := OpenCatalog(filepath.Join(t.TempDir(), "catalog.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	// Earlier versions named a remoteless row by its name alone.
	legacyID := stableID("repo", "excel-corpus", nil)
	if _, err := catalog.DB.Exec(`INSERT INTO repositories(id,display_name,local_locations_json,created_at,updated_at) VALUES(?,?,?,?,?)`, legacyID, "excel-corpus", jsonText([]string{"/Users/x/src/excel-corpus"}), now(), now()); err != nil {
		t.Fatal(err)
	}
	tx, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, path := range []string{"/Users/x/src/excel-corpus", "/Users/x/src/excel-corpus/.task-worktrees/e60-attempt-1", "/Users/x/src/excel-corpus/.candidate-worktrees/c1", "/Users/x/src/excel-corpus/.claude/worktrees/agent-1", "/Users/x/src/excel-corpus/.conductor/lima"} {
		id, err := upsertRepository(tx, map[string]any{"display_name": "Excel-Corpus", "local_locations": []string{path}})
		if err != nil {
			t.Fatal(err)
		}
		if id != legacyID {
			t.Fatalf("%s created %s, want the existing row", path, id)
		}
	}
	if n := repositoryCount(t, tx); n != 1 {
		t.Fatalf("%d rows", n)
	}
	// Of several remoteless rows for the name, the one with the most
	// workspaces takes the record.
	if _, err := tx.Exec(`INSERT INTO repositories(id,display_name,local_locations_json,created_at,updated_at) VALUES('busy','excel-corpus','["/Users/x/src/excel-corpus"]','t','t')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at,repository_id) VALUES('w','codex','local','w','work','t','busy')`); err != nil {
		t.Fatal(err)
	}
	id, err := upsertRepository(tx, map[string]any{"display_name": "excel-corpus", "local_locations": []string{"/Users/x/src/excel-corpus/.task-worktrees/next"}})
	if err != nil || id != "busy" {
		t.Fatalf("id=%s err=%v", id, err)
	}
}

func TestRepositoryUpsertJoinsMovedRemoteOfSharedCheckout(t *testing.T) {
	catalog, err := OpenCatalog(filepath.Join(t.TempDir(), "catalog.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	tx, err := catalog.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	old, err := upsertRepository(tx, map[string]any{"canonical_remote": "git@github.com:gbdubs/repo.git", "display_name": "repo", "root_commit": "r1", "local_locations": []string{"/Users/t/gbdubs/repo"}})
	if err != nil {
		t.Fatal(err)
	}
	moved, err := upsertRepository(tx, map[string]any{"canonical_remote": "git@github.com:Pythia-Software/repo.git", "display_name": "repo", "root_commit": "r1", "local_locations": []string{"/Users/t/gbdubs/repo/.conductor/lima"}})
	if err != nil {
		t.Fatal(err)
	}
	fork, err := upsertRepository(tx, map[string]any{"canonical_remote": "git@github.com:someone/repo.git", "display_name": "repo", "root_commit": "r1", "local_locations": []string{"/Users/t/elsewhere/repo"}})
	if err != nil {
		t.Fatal(err)
	}
	if moved != old || fork == old {
		t.Fatalf("old=%s moved=%s fork=%s", old, moved, fork)
	}
}

func TestRepositoryRemotelessNestedConductorLocation(t *testing.T) {
	keyed := repositoryIdentity{ID: "site", Remote: "git@github.com:acme/xlsx-exec.git", Normalized: "github.com/acme/xlsx-exec", Name: "xlsx-exec", Workspaces: 9,
		Locations: []string{"/Users/t/conductor/workspaces/xlsx-exec/bilbao-v6"}}
	// A folder in a workspace whose repository Git can no longer read is named
	// after the Conductor directory, and belongs to that repository.
	named := repositoryIdentity{ID: "folder", Name: "xlsx-exec", Locations: []string{"/Users/t/conductor/workspaces/xlsx-exec/bilbao-v6/.context/poison-workbooks"}}
	if groups := planRepositoryMerges([]repositoryIdentity{keyed, named}, nil); len(groups) != 1 || !repositoryGroupIDs(groups[0])["folder"] {
		t.Fatalf("named after the directory: %#v", groups)
	}
	// A scratch repository with a name of its own does not.
	scratch := repositoryIdentity{ID: "scratch", Name: "repo", Locations: []string{"/Users/t/conductor/workspaces/xlsx-exec/bilbao-v6/.context/run1/repo"}}
	if groups := planRepositoryMerges([]repositoryIdentity{keyed, scratch}, nil); len(groups) != 0 {
		t.Fatalf("scratch repository attached: %#v", groups)
	}
}
