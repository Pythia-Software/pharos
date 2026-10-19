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

func repositoryGroupIDs(group repositoryMergeGroup) map[string]bool {
	ids := map[string]bool{group.Survivor.ID: true}
	for _, loser := range group.Losers {
		ids[loser.ID] = true
	}
	return ids
}

// TestPlanRepositoryMerges: which repository rows the planner merges, and
// which signals it trusts, without Git.
func TestPlanRepositoryMerges(t *testing.T) {
	// https and keyed are rows with a remote; local is a row without one.
	https := func(id, slug string) repositoryIdentity {
		return repositoryIdentity{ID: id, Remote: "https://" + slug + ".git", Normalized: slug, Name: filepath.Base(slug)}
	}
	keyed := func(id, slug string, workspaces int, locations ...string) repositoryIdentity {
		return repositoryIdentity{ID: id, Remote: "git@" + strings.Replace(slug, "/", ":", 1) + ".git", Normalized: slug, Name: filepath.Base(slug), Workspaces: workspaces, Locations: locations}
	}
	rooted := func(id, slug, root string, locations ...string) repositoryIdentity {
		row := keyed(id, slug, 0, locations...)
		row.Root = root
		return row
	}
	local := func(id, name string, workspaces int, locations ...string) repositoryIdentity {
		return repositoryIdentity{ID: id, Name: name, Workspaces: workspaces, Locations: locations}
	}
	with := func(row repositoryIdentity, change func(*repositoryIdentity)) repositoryIdentity {
		change(&row)
		return row
	}
	// Two owners' "origin" repositories, cloned from one root commit.
	one := with(https("a", "github.com/one/origin"), func(r *repositoryIdentity) { r.Root = "same" })
	two := with(https("b", "github.com/two/origin"), func(r *repositoryIdentity) { r.Root = "same" })
	forge := func(r *repositoryIdentity) { r.Forge = "123" }
	inConductor := func(path string) func(*repositoryIdentity) {
		return func(r *repositoryIdentity) { r.Locations = []string{path} }
	}
	missing := local("missing", "origin", 0, "/old/conductor/workspaces/origin/one")

	none := func(t *testing.T, groups []repositoryMergeGroup) {
		if len(groups) != 0 {
			t.Fatalf("merged: %#v", groups)
		}
	}
	groupsOf := func(n int) func(*testing.T, []repositoryMergeGroup) {
		return func(t *testing.T, groups []repositoryMergeGroup) {
			if len(groups) != n {
				t.Fatalf("%d groups, want %d: %#v", len(groups), n, groups)
			}
		}
	}
	// merged checks that one group holds members (and no others, when exact)
	// and, if set, its survivor and name.
	merged := func(survivor, name string, losers int, members ...string) func(*testing.T, []repositoryMergeGroup) {
		return func(t *testing.T, groups []repositoryMergeGroup) {
			if len(groups) != 1 {
				t.Fatalf("%d groups, want 1: %#v", len(groups), groups)
			}
			ids := repositoryGroupIDs(groups[0])
			for _, member := range members {
				if !ids[member] {
					t.Fatalf("%s did not join: %#v", member, groups[0])
				}
			}
			if survivor != "" && groups[0].Survivor.ID != survivor || name != "" && groups[0].Name != name || losers >= 0 && len(groups[0].Losers) != losers {
				t.Fatalf("survivor %s, name %q, %d losers: %#v", groups[0].Survivor.ID, groups[0].Name, len(groups[0].Losers), groups[0])
			}
		}
	}
	signal := func(want string, last bool) func(*testing.T, []repositoryMergeGroup) {
		return func(t *testing.T, groups []repositoryMergeGroup) {
			signals := groups[0].Signals
			if last && signals[len(signals)-1] != want || !last && !containsString(signals, want) {
				t.Fatalf("signals %q, want %q", signals, want)
			}
		}
	}
	never := func(ids ...string) func(*testing.T, []repositoryMergeGroup) {
		return func(t *testing.T, groups []repositoryMergeGroup) {
			for _, group := range groups {
				for _, id := range ids {
					if repositoryGroupIDs(group)[id] {
						t.Fatalf("%s joined: %#v", id, group)
					}
				}
			}
		}
	}

	pharos := []repositoryIdentity{
		{ID: "old", Name: "alexandria", Remote: "https://github.com/gbdubs/alexandria.git", Normalized: "github.com/gbdubs/alexandria", Forge: "1377317940", ForgeCanonical: "github.com/pythia-software/pharos", Workspaces: 227, Locations: []string{"/Users/test/conductor/workspaces/alexandria/one"}},
		{ID: "missing", Name: "alexandria", Workspaces: 57, Locations: []string{"/Users/test/conductor/workspaces/alexandria/two"}},
		// Two remote rows claim Conductor directory alexandria, but both land
		// in the same group, so the remoteless row still attaches.
		{ID: "middle", Name: "pharos", Remote: "https://github.com/gbdubs/pharos.git", Normalized: "github.com/gbdubs/pharos", Forge: "1377317940", ForgeCanonical: "github.com/pythia-software/pharos", Workspaces: 1, Locations: []string{"/Users/test/conductor/workspaces/alexandria/stuttgart"}},
		{ID: "current", Name: "pharos", Remote: "https://github.com/Pythia-Software/pharos.git", Normalized: "github.com/pythia-software/pharos", Forge: "1377317940", ForgeCanonical: "github.com/pythia-software/pharos", Workspaces: 4},
	}
	unforged := []repositoryIdentity{}
	for _, row := range pharos {
		row.Forge, row.ForgeCanonical = "", ""
		unforged = append(unforged, row)
	}
	origins := []repositoryIdentity{}
	// Ten stress-test bare repositories share a root commit and the checkout
	// they were cloned into, but each is its own repository.
	for i := 0; i < 10; i++ {
		origins = append(origins, repositoryIdentity{ID: fmt.Sprintf("origin-%d", i), Name: "origin", Remote: fmt.Sprintf("/Users/t/conductor/workspaces/tl1/edinburgh-v2/.context/run%d/origin.git", i), Root: "r1", Workspaces: 3,
			Locations: []string{"/Users/t/conductor/workspaces/tl1/edinburgh-v2/.context/repo/.candidate-worktrees/c" + fmt.Sprint(i)}})
	}
	origins = append(origins, repositoryIdentity{ID: "tl1", Name: "tl1", Remote: "git@github.com:gbdubs/tl1.git", Normalized: "github.com/gbdubs/tl1", Root: "r1", Locations: []string{"/Users/t/conductor/workspaces/tl1/edinburgh-v2/.context/repo"}})
	xlsxExec := keyed("site", "github.com/acme/xlsx-exec", 9, "/Users/t/conductor/workspaces/xlsx-exec/bilbao-v6")
	// One clone that had both remotes over time is one repository, without
	// GitHub.
	moved := []repositoryIdentity{
		rooted("old", "github.com/gbdubs/excel-corpus", "r1", "/Users/t/gbdubs/excel-corpus"),
		rooted("new", "github.com/pythia-software/excel-corpus", "r1", "/Users/t/gbdubs/excel-corpus/.task-worktrees/e1"),
	}

	type scenario struct {
		name    string
		rows    []repositoryIdentity
		aliases map[string]string
		checks  []func(*testing.T, []repositoryMergeGroup)
	}
	scenarios := []scenario{
		{"different owners with one root stay apart", []repositoryIdentity{one, two}, nil, nil},
		{"the same name alone proves nothing", []repositoryIdentity{local("origin-one", "origin", 0), local("origin-two", "origin", 0)}, nil, nil},
		{"a shared forge ID merges", []repositoryIdentity{with(one, forge), with(two, forge)}, nil, []func(*testing.T, []repositoryMergeGroup){groupsOf(1)}},
		{"a configured remote alias merges", []repositoryIdentity{one, two}, map[string]string{"github.com/one/origin": "github.com/two/origin"}, []func(*testing.T, []repositoryMergeGroup){groupsOf(1)}},
		{"a display-name alias merges", []repositoryIdentity{local("named", "old-name", 0), two}, map[string]string{"old-name": "github.com/two/origin"}, []func(*testing.T, []repositoryMergeGroup){groupsOf(1)}},
		{"a remoteless row attaches by Conductor directory", []repositoryIdentity{with(one, inConductor("/old/conductor/workspaces/origin/two")), missing}, nil, []func(*testing.T, []repositoryMergeGroup){groupsOf(1)}},
		{"a Conductor directory of two repositories is ambiguous", []repositoryIdentity{with(one, inConductor("/old/conductor/workspaces/origin/two")), with(two, inConductor("/other/conductor/workspaces/origin/three")), missing}, nil, nil},
		{"a forge redirect merges a renamed repository and its remoteless rows", pharos, nil, []func(*testing.T, []repositoryMergeGroup){merged("current", "pharos", 3)}},
		// Without the forge redirect the two owners form separate groups,
		// and the shared directory is ambiguous again.
		{"without the redirect the shared directory is ambiguous", unforged, nil, []func(*testing.T, []repositoryMergeGroup){never("missing")}},
		{"a remoteless row at the clone path joins the clone", []repositoryIdentity{
			keyed("pythia", "github.com/pythia-software/excel-corpus", 327, "/Users/t/gbdubs/excel-corpus", "/Users/t/gbdubs/excel-corpus/.task-worktrees/e60-attempt-1"),
			keyed("gbdubs", "github.com/pythia-software/excel-corpus", 292, "/Users/t/gbdubs/excel-corpus"),
			local("tl1", "excel-corpus", 10323, "/Users/t/gbdubs/excel-corpus"),
		}, nil, []func(*testing.T, []repositoryMergeGroup){merged("pythia", "excel-corpus", -1, "tl1")}},
		{"a deeper path is not truncated to an ancestor clone", []repositoryIdentity{
			keyed("site", "github.com/gbdubs/grady.dev", 38, "/Users/t/gbdubs/grady.dev"),
			local("nested", "grady.dev", 4, "/Users/t/gbdubs/grady.dev/.conductor/puebla-v5/.context/scratch"),
		}, nil, nil},
		// Without a shared root commit nothing says the two remotes are one
		// repository.
		{"a path of two different groups is ambiguous", []repositoryIdentity{
			keyed("mine", "github.com/one/widget", 5, "/Users/t/widget"),
			keyed("theirs", "github.com/two/widget", 5, "/Users/t/widget"),
			local("orphan", "widget", 9, "/Users/t/widget"),
		}, nil, nil},
		// explo-candidate-v2 is a TL1 project name for the explo clone. The
		// group keeps explo's name even though the remoteless rows have more
		// workspaces than the remote rows.
		{"a TL1 project name joins its clone and keeps the clone's name", []repositoryIdentity{
			keyed("ssh", "github.com/pythia-software/explo", 464, "/Users/t/gbdubs/explo", "/Users/t/gbdubs/explo/.conductor/kampala-v6"),
			keyed("https", "github.com/pythia-software/explo", 300, "/Users/t/gbdubs/explo/.conductor/moscow-v1"),
			local("candidate", "explo-candidate-v2", 2631, "/Users/t/gbdubs/explo"),
			local("bare", "explo", 2447, "/Users/t/gbdubs/explo", "/Users/t/gbdubs/explo/.conductor/los-angeles-v2"),
		}, nil, []func(*testing.T, []repositoryMergeGroup){merged("ssh", "explo", 3)}},
		{"a TL1 scratch origin neither merges nor makes the workspace ambiguous", []repositoryIdentity{
			keyed("tl1", "github.com/gbdubs/tl1", 261, "/Users/t/gbdubs/tl1", "/Users/t/conductor/workspaces/tl1/yellowknife-v1"),
			{ID: "origin", Name: "origin", Remote: "/Users/t/conductor/workspaces/tl1/edinburgh-v2/.context/run1/origin.git", Workspaces: 10, Locations: []string{"/Users/t/conductor/workspaces/tl1/edinburgh-v2/.context/run1/repo/.candidate-worktrees/4c11"}},
			local("missing", "tl1", 19, "/Users/t/conductor/workspaces/tl1/west-monroe-v8"),
		}, nil, []func(*testing.T, []repositoryMergeGroup){merged("", "", -1, "missing"), never("origin")}},
		{"a checkout shared by two remotes merges them", moved, nil, []func(*testing.T, []repositoryMergeGroup){merged("", "", 1), signal("shared checkout", false)}},
		{"the same root in different checkouts is a fork", []repositoryIdentity{
			rooted("mine", "github.com/one/widget", "r1", "/Users/t/one/widget"),
			rooted("theirs", "github.com/two/widget", "r1", "/Users/t/two/widget"),
		}, nil, nil},
		{"a shared checkout with different roots proves nothing", []repositoryIdentity{
			rooted("a", "github.com/one/widget", "r1", "/Users/t/widget"),
			rooted("b", "github.com/two/widget", "r2", "/Users/t/widget"),
		}, nil, nil},
		{"TL1 stress-test origins stay separate", origins, nil, nil},
		{"remoteless rows of one repository merge by name and checkout, or Conductor directory", []repositoryIdentity{
			local("a", "xlsxl-agy", 1, "/Users/t/conductor/workspaces/xlsxl-agy/one"),
			local("b", "xlsxl-agy", 2, "/Users/t/conductor/workspaces/xlsxl-agy/two"),
			local("c", "xlsxl-agy", 5, "/Users/t/gbdubs/xlsxl-agy/.task-worktrees/t1"),
			local("d", "xlsxl-agy", 1, "/Users/t/gbdubs/xlsxl-agy"),
			local("other", "elsewhere", 1, "/Users/t/conductor/workspaces/xlsxl-agy/three"),
		}, nil, []func(*testing.T, []repositoryMergeGroup){groupsOf(2), func(t *testing.T, groups []repositoryMergeGroup) {
			for _, group := range groups {
				if ids := repositoryGroupIDs(group); ids["other"] || ids["a"] != ids["b"] || ids["c"] != ids["d"] || ids["a"] == ids["c"] {
					t.Fatalf("group %v", ids)
				}
			}
		}}},
		{"remoteless rows join a sibling attached to a remote", []repositoryIdentity{
			keyed("remote", "github.com/acme/excel-corpus", 10, "/Users/t/gbdubs/excel-corpus"),
			local("clone", "excel-corpus", 6122, "/Users/t/gbdubs/excel-corpus"),
			local("old", "excel-corpus", 4, "/Users/t/gbdubs/excel-corpus", "/Users/t/conductor/workspaces/excel-corpus/one"),
			local("worktree", "excel-corpus", 2, "/Users/t/conductor/workspaces/excel-corpus/two"),
		}, nil, []func(*testing.T, []repositoryMergeGroup){merged("remote", "", 3)}},
		{"remoteless rows never bridge two repositories", []repositoryIdentity{
			keyed("one", "github.com/acme/widget", 10, "/Users/t/one/widget"),
			keyed("two", "github.com/other/widget", 10, "/Users/t/two/widget"),
			local("a", "widget", 1, "/Users/t/one/widget", "/Users/t/conductor/workspaces/widget/w1"),
			local("b", "widget", 1, "/Users/t/two/widget", "/Users/t/conductor/workspaces/widget/w2"),
			local("both", "widget", 1, "/Users/t/conductor/workspaces/widget/w3"),
		}, nil, []func(*testing.T, []repositoryMergeGroup){never("both"), func(t *testing.T, groups []repositoryMergeGroup) {
			for _, group := range groups {
				if ids := repositoryGroupIDs(group); ids["one"] && ids["two"] {
					t.Fatalf("bridged two repositories: %#v", group)
				}
			}
		}}},
		// A folder in a workspace whose repository Git can no longer read is
		// named after the Conductor directory, and belongs to that repository.
		{"a folder named after its Conductor directory joins it", []repositoryIdentity{xlsxExec,
			local("folder", "xlsx-exec", 0, "/Users/t/conductor/workspaces/xlsx-exec/bilbao-v6/.context/poison-workbooks")}, nil, []func(*testing.T, []repositoryMergeGroup){merged("", "", -1, "folder")}},
		{"a scratch repository with a name of its own does not", []repositoryIdentity{xlsxExec,
			local("scratch", "repo", 0, "/Users/t/conductor/workspaces/xlsx-exec/bilbao-v6/.context/run1/repo")}, nil, nil},
	}
	// Worktrees in known in-clone layouts map to the clone, in either
	// direction.
	for _, worktree := range []string{"/Users/t/gbdubs/grady.dev/.conductor/puebla-v5", "/Users/t/gbdubs/grady.dev/.task-worktrees/a1", "/Users/t/gbdubs/grady.dev/.claude/worktrees/agent-1"} {
		scenarios = append(scenarios,
			scenario{"a remoteless worktree joins its clone: " + worktree, []repositoryIdentity{keyed("site", "github.com/gbdubs/grady.dev", 38, "/Users/t/gbdubs/grady.dev"), local("wt", "grady.dev", 4, worktree)}, nil,
				[]func(*testing.T, []repositoryMergeGroup){merged("", "", -1, "wt"), signal("local checkout", true)}},
			scenario{"a remoteless clone joins its worktree: " + worktree, []repositoryIdentity{keyed("site", "github.com/gbdubs/grady.dev", 38, worktree), local("clone", "grady.dev", 4, "/Users/t/gbdubs/grady.dev")}, nil,
				[]func(*testing.T, []repositoryMergeGroup){groupsOf(1)}})
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			groups := planRepositoryMerges(scenario.rows, scenario.aliases)
			if scenario.checks == nil {
				none(t, groups)
			}
			for _, check := range scenario.checks {
				check(t, groups)
			}
		})
	}
	// Ingest recognizes a moved remote of a shared checkout too, and a local
	// remote is its own identity.
	if !repositorySameIdentity(moved[0], moved[1], nil, nil) {
		t.Fatal("ingest did not recognize the moved remote")
	}
	if id := repositoryIdentityKey(origins[0]); id != origins[0].Remote {
		t.Fatalf("identity of a local remote = %q", id)
	}
}

// TestRepositoryRemotelessAttachReadsCheckoutOrigin: when the clone a
// remoteless row sits in exists, its current origin decides where the row
// belongs.
func TestRepositoryRemotelessAttachReadsCheckoutOrigin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	clone := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "git@github.com:other/widget.git"}} {
		if output, err := exec.Command("git", append([]string{"-C", clone}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	acme := repositoryIdentity{ID: "acme", Remote: "https://github.com/acme/widget.git", Normalized: "github.com/acme/widget", Name: "widget", Root: "r1", Workspaces: 9, Locations: []string{clone}}
	other := repositoryIdentity{ID: "other", Remote: "https://github.com/other/widget.git", Normalized: "github.com/other/widget", Name: "widget", Root: "r2", Workspaces: 1, Locations: []string{clone}}
	remoteless := repositoryIdentity{ID: "local", Name: "widget", Workspaces: 3, Locations: []string{clone}}
	t.Run("an origin that disagrees blocks the attach", func(t *testing.T) {
		rows := []repositoryIdentity{{ID: acme.ID, Remote: acme.Remote, Normalized: acme.Normalized, Name: acme.Name, Locations: acme.Locations}, {ID: remoteless.ID, Name: remoteless.Name, Locations: remoteless.Locations}}
		if groups := planRepositoryMerges(rows, nil); len(groups) != 0 {
			t.Fatalf("checkout origin disagrees but merged: %#v", groups)
		}
		if groups := planRepositoryMerges(rows, map[string]string{"github.com/other/widget": "github.com/acme/widget"}); len(groups) != 1 {
			t.Fatalf("aliased checkout origin did not attach: %#v", groups)
		}
	})
	rows := []repositoryIdentity{acme, other, remoteless}
	t.Run("the origin breaks a tie between two repositories", func(t *testing.T) {
		groups := planRepositoryMerges(rows, nil)
		if len(groups) != 1 || groups[0].Survivor.ID != "other" || !repositoryGroupIDs(groups[0])["local"] || repositoryGroupIDs(groups[0])["acme"] || !containsString(groups[0].Signals, "checkout origin") {
			t.Fatalf("origin tie-break: %#v", groups)
		}
	})
	t.Run("an origin naming neither stays ambiguous", func(t *testing.T) {
		if output, err := exec.Command("git", "-C", clone, "remote", "set-url", "origin", "git@github.com:third/widget.git").CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, output)
		}
		if groups := planRepositoryMerges(rows, nil); len(groups) != 0 {
			t.Fatalf("unrelated origin merged: %#v", groups)
		}
	})
}

func TestRepositoryUpsertRemotelessJoinsGroup(t *testing.T) {
	catalog, _ := testCatalog(t)
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
	catalog, _ := testCatalog(t)
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
	catalog, _ := testCatalog(t)
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
	catalog, _ := testCatalog(t)
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
	catalog, _ := testCatalog(t)
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

func repositoryCount(t *testing.T, db repositoryQuerier) int {
	t.Helper()
	items, err := loadRepositoryIdentities(db)
	if err != nil {
		t.Fatal(err)
	}
	return len(items)
}

func TestRepositoryUpsertRemotelessWorktreesOneRow(t *testing.T) {
	catalog, _ := testCatalog(t)
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
	catalog, _ := testCatalog(t)
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
	catalog, _ := testCatalog(t)
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
