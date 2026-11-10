package archive

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFolderKey(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for location, want := range map[string]string{
		"":                               "",
		"relative/dir":                   "",
		home:                             "~",
		filepath.Join(home, "notes"):     "~/notes",
		"/Users/someone/Documents/notes": "~/Documents/notes",
		"/Users/someone":                 "~",
		"/home/someone/data/":            "~/data",
		"/Users/Shared/team":             "/Users/Shared/team",
		"~/notes/../drafts":              "~/drafts",
		"/srv/data//sets":                "/srv/data/sets",
		"/tmp/scratch-1":                 "/tmp/scratch-1",
	} {
		if got := folderKey(location); got != want {
			t.Errorf("folderKey(%q) = %q, want %q", location, got, want)
		}
	}
	for location, want := range map[string]string{
		"/tmp/scratch-1":                    "/tmp",
		"/private/var/folders/x/y/T/run-12": "/tmp",
		"/srv/tmpdata":                      "/srv/tmpdata",
	} {
		if got := projectFolder(location); got != want {
			t.Errorf("projectFolder(%q) = %q, want %q", location, got, want)
		}
	}
}

func TestFolderRepository(t *testing.T) {
	roots := []projectRoot{{"~/src/app", "app"}, {"~/src/app/vendor/lib", "lib"}, {"~", "dotfiles"}, {"/", "root"}, {"~/src/twin", "a"}, {"~/src/twin", "b"}, {"~/src/again", "c"}, {"~/src/again", "c"}}
	for folder, want := range map[string]string{
		"~/src/app":              "app",
		"~/src/app/docs":         "app",
		"~/src/app/vendor/lib/x": "lib",
		"~/src/application":      "",
		"~/notes":                "",
		"~":                      "dotfiles",
		"/srv":                   "",
		"~/src/twin/docs":        "",
		"~/src/again/docs":       "c",
	} {
		if got := folderRepository(folder, roots); got != want {
			t.Errorf("folderRepository(%q) = %q, want %q", folder, got, want)
		}
	}
}

// Every session has a project: its folder without Git, none without a
// working directory.
func TestProjectsWithoutGit(t *testing.T) {
	library := noGitLibrary(t)
	projects := library.projects(t)
	if len(projects) != 5 {
		t.Fatalf("workspaces: %#v", projects)
	}
	for _, session := range []string{"s-notes", "s-research", "s-later"} {
		project := projects[session]
		folder := folderKey(library.folders[session[2:]])
		if project["project_kind"] != "folder" || project["project_name"] != folder || project["project_id"] != "folder:"+folder || project["repository_name"] != nil {
			t.Errorf("%s project: %#v", session, project)
		}
	}
	for _, session := range []string{"s-nowhere", "chat-1"} {
		if project := projects[session]; project["project_kind"] != "none" || project["project_name"] != nil || project["project_id"] != nil {
			t.Errorf("%s project: %#v", session, project)
		}
	}
}

// A folder that later becomes a repository takes its earlier sessions along,
// and a session recorded in a subfolder of a checkout belongs to it too.
func TestProjectsFolderBecomesRepository(t *testing.T) {
	library := mixedLibrary(t)
	projects := library.projects(t)
	app := projects["s-app"]
	if app["project_kind"] != "repository" || app["project_name"] != "app" || app["repository_name"] != "app" {
		t.Fatalf("repository project: %#v", app)
	}
	if docs := projects["s-app-docs"]; docs["project_id"] != app["project_id"] {
		t.Fatalf("subfolder session: %#v", docs)
	}
	if gone := projects["s-gone"]; gone["project_kind"] != "repository" || gone["project_name"] != "gone" {
		t.Fatalf("deleted checkout keeps its repository: %#v", gone)
	}
	if notes := projects["s-notes"]; notes["project_kind"] != "folder" {
		t.Fatalf("plain folder: %#v", notes)
	}

	// The new checkout resolves again only the folders inside it: a stale
	// row elsewhere is left for the repair pass, not read under the writer.
	execTx(t, library.catalog.DB, `UPDATE workspaces SET folder_repository_id='stale' WHERE source_id='s-notes'`)
	later := library.folders["later"]
	gitInit(t, later)
	writeClaudeSession(t, library.claude, "s-later-git", claudeSession("s-later-git", later, "main", "Keep drafting", 20))
	library.ingestClaude(t)
	var stale string
	if err := library.catalog.DB.QueryRow(`SELECT COALESCE(folder_repository_id,'') FROM workspaces WHERE source_id='s-notes'`).Scan(&stale); err != nil || stale != "stale" {
		t.Fatalf("a folder outside the new checkout was resolved again: %q %v", stale, err)
	}
	execTx(t, library.catalog.DB, `UPDATE workspaces SET folder_repository_id=NULL WHERE source_id='s-notes'`)
	projects = library.projects(t)
	repository := projects["s-later-git"]
	if repository["project_kind"] != "repository" || repository["project_name"] != "later" {
		t.Fatalf("new repository: %#v", repository)
	}
	if before := projects["s-later"]; before["project_id"] != repository["project_id"] || before["repository_name"] != nil {
		t.Fatalf("the earlier session joins the repository's project, keeping no repository of its own: %#v", before)
	}
}

// An older catalog fills the project columns in once, when it is opened.
func TestProjectsBackfillOnOpen(t *testing.T) {
	library := noGitLibrary(t)
	execTx(t, library.catalog.DB, `UPDATE workspaces SET project_folder=NULL,folder_repository_id=NULL`,
		`DELETE FROM meta WHERE key='`+initializedBuildKey+`'`)
	reopened := reopenCatalog(t, library.catalog.Path)
	library.catalog = reopened
	if project := library.projects(t)["s-notes"]; project["project_kind"] != "folder" {
		t.Fatalf("backfill: %#v", project)
	}
}

// A build from before projects can still write to the catalog: it inserts
// workspaces without project columns and moves locations without updating
// them. Opening with this build again, and the identities pass while this
// build stays open, derive them again.
func TestProjectsRepairOlderWriters(t *testing.T) {
	library := noGitLibrary(t)
	moved := filepath.Join(filepath.Dir(library.folders["notes"]), "notebook")
	execTx(t, library.catalog.DB,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,location,indexed_at) VALUES('w-older','claude','local','s-older','Older','`+library.folders["notes"]+`','x')`,
		`UPDATE workspaces SET location='`+moved+`' WHERE source_id='s-research'`)
	check := func(when string) {
		t.Helper()
		projects := library.projects(t)
		if projects["s-older"]["project_id"] != projects["s-notes"]["project_id"] || projects["s-research"]["project_name"] != folderKey(moved) {
			t.Fatalf("%s: %#v %#v", when, projects["s-older"], projects["s-research"])
		}
	}
	if _, err := library.catalog.ReconcileIdentities(); err != nil {
		t.Fatal(err)
	}
	check("identities pass")

	execTx(t, library.catalog.DB, `UPDATE workspaces SET project_folder=NULL,folder_repository_id=NULL`,
		`DELETE FROM meta WHERE key='`+initializedBuildKey+`'`)
	library.catalog = reopenCatalog(t, library.catalog.Path)
	check("reopened")
}

// Recovery restores repositories directly, not through the writer that
// reports new checkouts; a folder session recovered before the session that
// brought its repository still joins it.
func TestProjectsRecoveredInAnyOrder(t *testing.T) {
	library := noGitLibrary(t)
	later := library.folders["later"]
	gitInit(t, later)
	writeClaudeSession(t, library.claude, "s-later-git", claudeSession("s-later-git", later, "main", "Keep drafting", 20))
	library.ingestClaude(t)
	want := library.projects(t)
	if want["s-later"]["project_id"] != want["s-later-git"]["project_id"] {
		t.Fatalf("original catalog: %#v", want)
	}
	fresh, _ := testCatalog(t)
	for _, session := range []string{"s-later", "s-later-git"} {
		var id string
		if err := library.catalog.DB.QueryRow("SELECT id FROM workspaces WHERE source_id=?", want[session]["source_id"]).Scan(&id); err != nil {
			t.Fatal(session, err)
		}
		tx := seedTx(t, library.catalog.DB)
		envelope, err := exportHistoryRecovery(t.Context(), tx, historyUnit{Kind: "catalog-workspace", Original: id}, "")
		tx.Rollback()
		if err != nil {
			t.Fatal(err)
		}
		if outcome, err := fresh.applyHistoryRecovery(t.Context(), envelope); err != nil || outcome != "catalog-derived" {
			t.Fatal(session, outcome, err)
		}
	}
	got := projectLibrary{catalog: fresh}.projects(t)
	if got["s-later"]["project_id"] != want["s-later-git"]["project_id"] || got["s-later"]["project_kind"] != "repository" {
		t.Fatalf("recovered folder session: %#v", got["s-later"])
	}
}

// A new checkout resolves again the folders at or under it, including the
// temporary directories that are all one /tmp project, and no others.
func TestResolveFolderProjectsBounds(t *testing.T) {
	catalog, _ := testCatalog(t)
	execTx(t, catalog.DB,
		`INSERT INTO repositories(id,display_name,local_locations_json,created_at,updated_at) VALUES
			('r-tmp','build','["/tmp/build/app"]','x','x'),('r-app','app','["/Users/someone/app"]','x','x')`,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,location,project_folder,folder_repository_id,indexed_at) VALUES
			('w-tmp','claude','local','s-tmp','T','/tmp/build/app/sub','/tmp',NULL,'x'),
			('w-app','claude','local','s-app','A','/Users/other/app/docs','~/app/docs',NULL,'x'),
			('w-near','claude','local','s-near','N','/Users/other/application','~/application','stale','x'),
			('w-else','claude','local','s-else','E','/srv/else','/srv/else','stale','x')`)
	tx := seedTx(t, catalog.DB)
	defer tx.Rollback()
	if err := resolveFolderProjects(t.Context(), tx, changedRoots(nil, []projectRoot{{"/tmp/build/app", "r-tmp"}, {"~/app", "r-app"}})); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"w-tmp": "r-tmp", "w-app": "r-app", "w-near": "stale", "w-else": "stale"} {
		var got string
		if err := tx.QueryRow(`SELECT COALESCE(folder_repository_id,'') FROM workspaces WHERE id=?`, id).Scan(&got); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", id, got, err, want)
		}
	}
}
