package archive

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitRepository is a repository built in the test, committing at chosen
// times with no user configuration.
type gitRepository struct {
	t   *testing.T
	dir string
}

func newGitRepository(t *testing.T) *gitRepository {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "app")
	gitInit(t, dir)
	// The first commit is old, so it is outside every session's window.
	r := &gitRepository{t, dir}
	r.git(time.Now().Add(-90*24*time.Hour), "commit", "-q", "--amend", "--no-edit", "--reset-author")
	return r
}

func (r *gitRepository) git(at time.Time, args ...string) string {
	r.t.Helper()
	command := exec.Command("git", append([]string{"-C", r.dir, "-c", "user.name=Pharos Test", "-c", "user.email=test@example.com"}, args...)...)
	date := fmt.Sprintf("%d +0000", at.Unix())
	command.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	output, err := command.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

// commit writes files and commits them days ago, returning the hash.
func (r *gitRepository) commit(days float64, message string, files map[string]string) string {
	r.t.Helper()
	at := time.Now().Add(-time.Duration(days * float64(24*time.Hour)))
	for name, content := range files {
		path := filepath.Join(r.dir, name)
		if content == "" {
			if err := os.Remove(path); err != nil {
				r.t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	r.git(at, "add", "-A")
	r.git(at, "commit", "-q", "-m", message)
	return r.git(at, "rev-parse", "HEAD")
}

// signHead rewrites the commit HEAD names as signed, with a signature that
// only running gpg.program could check.
func (r *gitRepository) signHead() string {
	r.t.Helper()
	body := r.git(time.Now(), "cat-file", "commit", "HEAD") + "\n"
	at := strings.Index(body, "\n\n")
	command := exec.Command("git", "-C", r.dir, "hash-object", "-t", "commit", "-w", "--stdin")
	command.Stdin = strings.NewReader(body[:at] + "\ngpgsig -----BEGIN PGP SIGNATURE-----\n \n iQ==\n -----END PGP SIGNATURE-----" + body[at:])
	signed, err := command.Output()
	if err != nil {
		r.t.Fatal(err)
	}
	r.git(time.Now(), "update-ref", "HEAD", strings.TrimSpace(string(signed)))
	return strings.TrimSpace(string(signed))
}

// gitFixture is an outcome fixture whose sessions work in one repository
// this Mac recorded.
type gitFixture struct {
	*outcomeFixture
	repo *gitRepository
}

func newGitFixture(t *testing.T) *gitFixture {
	f := &gitFixture{newOutcomeFixture(t), newGitRepository(t)}
	execTx(t, f.catalog.DB, `INSERT INTO repositories(id,display_name,created_at,updated_at) VALUES('repo-app','app','x','x')`)
	return f
}

// session adds a session in the repository, sighted on this Mac unless host
// says otherwise.
func (f *gitFixture) session(id, host string) {
	f.t.Helper()
	f.outcomeFixture.session(id, "claude")
	if host == "" {
		host = currentHost().ID
	}
	execTx(f.t, f.catalog.DB, `UPDATE workspaces SET repository_id='repo-app' WHERE id='`+id+`'`,
		fmt.Sprintf(`INSERT INTO workspace_sightings(workspace_id,host_id,source_name,location,first_seen_at,last_seen_at) VALUES('%s','%s','claude','%s','x','x')`, id, host, f.repo.dir))
}

// edit records an edit of a repository file days ago.
func (f *gitFixture) edit(session, name string, days float64) {
	f.t.Helper()
	f.outcomeFixture.edit(session, filepath.Join(f.repo.dir, name), "repo-app", name, days, "", code(strings.ReplaceAll(name, ".", "_"), int(days*100), 3))
}

// printed records the session's own git commit printing a hash days ago.
func (f *gitFixture) printed(session, hash string, days float64) {
	f.t.Helper()
	f.calls++
	id := fmt.Sprintf("printed-%d", f.calls)
	execTx(f.t, f.catalog.DB,
		fmt.Sprintf(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES('%s','%s-c','%s','tool','tool_result','{"content":"[main %s] Work\n 1 file changed","is_error":false}','x','h')`, id, session, id, hash[:7]),
		fmt.Sprintf(`INSERT INTO tool_calls(id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,command,command_category,status,started_at,result_message_id)
			VALUES('%s','%s','%s-c',%d,'claude','tool','Bash','command','git commit -am Work','vcs','ok','%s','%s')`, id, session, session, f.calls, daysAgo(days), id))
}

func (f *gitFixture) refresh() {
	f.t.Helper()
	f.outcomeFixture.refresh()
	if err := f.catalog.refreshSessionGit(context.Background()); err != nil {
		f.t.Fatal(err)
	}
}

// links reads each session's links as "hash[:7] method confidence".
func (f *gitFixture) links() map[string][]string {
	f.t.Helper()
	rows, err := queryMaps(f.catalog.DB, "SELECT workspace_id,hash,method,confidence FROM session_commit_links ORDER BY workspace_id,hash")
	if err != nil {
		f.t.Fatal(err)
	}
	links := map[string][]string{}
	for _, row := range rows {
		id := firstString(row["workspace_id"])
		links[id] = append(links[id], fmt.Sprintf("%s %s %v", firstString(row["hash"])[:7], row["method"], row["confidence"]))
	}
	return links
}

func TestSessionGitLinksCommits(t *testing.T) {
	f := newGitFixture(t)
	repo := f.repo
	for _, id := range []string{"direct", "inferred", "stale", "wide", "narrow", "branch", "picked", "squashed", "reverted", "undone", "late",
		"early", "printer", "printer-too", "detached", "empty"} {
		f.session(id, "")
	}
	// A session that only committed, before every other session's edits.
	early := repo.commit(40, "Early", map[string]string{"early.go": "package early\n"})
	f.printed("early", early, 40)
	// A repository's text converter is never run: one with a cache would
	// write notes.
	repo.git(time.Now(), "config", "diff.custom.textconv", "cat")
	repo.git(time.Now(), "config", "diff.custom.cachetextconv", "true")
	if err := os.WriteFile(filepath.Join(repo.dir, ".git", "info", "attributes"), []byte("*.txt diff=custom\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Nor is its signature program, which a log would run for each signed
	// commit with log.showSignature set; and colour it forces on does not
	// hide a change from patch-id.
	ran := filepath.Join(t.TempDir(), "gpg-ran")
	program := filepath.Join(t.TempDir(), "gpg")
	if err := os.WriteFile(program, []byte("#!/bin/sh\ntouch '"+ran+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo.git(time.Now(), "config", "log.showSignature", "true")
	repo.git(time.Now(), "config", "gpg.program", program)
	repo.git(time.Now(), "config", "color.ui", "always")
	// Its own git commit printed the hash: linked directly, and on mainline.
	f.edit("direct", "main.go", 30)
	direct := repo.commit(29.99, "Direct", map[string]string{"main.go": "package main\n"})
	f.printed("direct", direct, 29.99)
	// A commit of its file in its window: inferred. Another commit in the
	// same window touches none of its files, so time alone links nothing.
	f.edit("inferred", "util.go", 25)
	inferred := repo.commit(24.99, "Inferred", map[string]string{"util.go": "package util\n"})
	repo.commit(24.98, "Elsewhere", map[string]string{"other.go": "package other\n"})
	repo.signHead()
	// Two sessions were editing a file a commit changed: the tightest
	// window gets the link, as a candidate that does not count. A session that edited it once, earlier, has a tighter
	// window still but stopped long before the commit, so it does not.
	f.edit("stale", "shared.go", 19.47)
	f.edit("wide", "shared.go", 20)
	f.edit("wide", "shared.go", 19)
	f.edit("narrow", "shared.go", 19.45)
	f.edit("narrow", "shared.go", 19.43)
	narrow := repo.commit(19.44, "Shared", map[string]string{"shared.go": "package shared\n"})
	// A revert by message, and one by an exactly inverse change.
	f.edit("reverted", "flag.go", 15)
	reverted := repo.commit(14.99, "Flag", map[string]string{"flag.go": "package flag\n"})
	repo.git(time.Now().Add(-14*24*time.Hour), "revert", "--no-edit", reverted)
	f.edit("undone", "undo.go", 12)
	undone := repo.commit(11.99, "Undo me", map[string]string{"undo.go": "package undo\n"})
	repo.commit(11, "Put it back", map[string]string{"undo.go": ""})
	// A commit long after the session's last edit is not its own.
	f.edit("late", "late.go", 10)
	repo.commit(9, "Late", map[string]string{"late.go": "package late\n"})
	repo.git(time.Now(), "update-ref", "refs/remotes/origin/main", "HEAD")
	// On a branch only: committed. Cherry-picked to mainline, keeping its
	// author time: the copy is the session's commit. Squashed, under a new
	// author time: the same change landed under another hash.
	repo.git(time.Now(), "checkout", "-q", "-b", "feature")
	f.edit("branch", "branch.go", 8)
	branch := repo.commit(7.99, "Branch", map[string]string{"branch.go": "package branch\n", "notes.txt": "converted\n"})
	// A commit with no change has no patch-id: whether it landed is unknown.
	repo.git(time.Now().Add(-7*24*time.Hour), "commit", "-q", "--allow-empty", "-m", "Empty")
	empty := repo.git(time.Now(), "rev-parse", "HEAD")
	f.printed("empty", empty, 7)
	// Two sessions printed one commit, which one of them also has as its
	// copy on mainline: each keeps its link, and the copy keeps the
	// printed hash's evidence.
	f.edit("printer", "printed.go", 6.5)
	printed := repo.commit(6.49, "Printed", map[string]string{"printed.go": "package printed\n"})
	f.printed("printer", printed, 6.49)
	f.printed("printer-too", printed, 6.49)
	f.edit("picked", "picked.go", 6)
	picked := repo.commit(5.99, "Picked", map[string]string{"picked.go": "package picked\n"})
	f.edit("squashed", "squashed.go", 5)
	squashed := repo.commit(4.99, "Squashed", map[string]string{"squashed.go": "package squashed\n"})
	repo.git(time.Now(), "checkout", "-q", "main")
	repo.git(time.Now().Add(-4*24*time.Hour), "cherry-pick", picked)
	copied := repo.git(time.Now(), "rev-parse", "HEAD")
	repo.git(time.Now().Add(-4*24*time.Hour), "cherry-pick", printed)
	printedCopy := repo.git(time.Now(), "rev-parse", "HEAD")
	repo.git(time.Now().Add(-3*24*time.Hour), "cherry-pick", squashed)
	repo.git(time.Now().Add(-3*24*time.Hour), "commit", "-q", "--amend", "--no-edit", "--reset-author")
	squash := repo.git(time.Now(), "rev-parse", "HEAD")
	repo.git(time.Now(), "update-ref", "refs/remotes/origin/main", "HEAD")
	// A commit in a worktree on a detached HEAD, which no ref holds.
	tree := filepath.Join(t.TempDir(), "tree")
	repo.git(time.Now(), "worktree", "add", "-q", "--detach", tree)
	f.edit("detached", "detached.go", 2)
	detached := (&gitRepository{t, tree}).commit(1.99, "Detached", map[string]string{"detached.go": "package detached\n"})
	f.printed("detached", detached, 1.99)
	f.refresh()

	links := f.links()
	for id, want := range map[string]string{
		"direct": direct[:7] + " transcript 1", "inferred": inferred[:7] + " inferred 0.7", "narrow": narrow[:7] + " inferred 0.5",
		"reverted": reverted[:7] + " inferred 0.7", "undone": undone[:7] + " inferred 0.7", "branch": branch[:7] + " inferred 0.7", "picked": copied[:7] + " inferred 0.7", "squashed": squashed[:7] + " inferred 0.7",
		"early": early[:7] + " transcript 1", "printer": printedCopy[:7] + " transcript 1", "printer-too": printed[:7] + " transcript 1", "detached": detached[:7] + " transcript 1",
		"empty": empty[:7] + " transcript 1",
	} {
		if strings.Join(links[id], ", ") != want {
			t.Errorf("%s links: %v, want %s", id, links[id], want)
		}
	}
	for _, id := range []string{"stale", "wide", "late"} {
		if len(links[id]) != 0 {
			t.Errorf("%s links: %v", id, links[id])
		}
	}
	if notes := repo.git(time.Now(), "for-each-ref", "refs/notes"); notes != "" {
		t.Fatalf("a scan wrote notes: %s", notes)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("a scan ran the repository's signature program")
	}
	// A candidate link is kept but counts toward nothing.
	if linked := countRows(t, f.catalog, `SELECT linked_commits FROM session_git WHERE workspace_id='narrow'`); linked != 0 {
		t.Fatalf("narrow counts %d linked commits", linked)
	}
	labels := f.labels()
	for id, want := range map[string]string{
		"direct": "landed", "inferred": "landed", "narrow": "changed_kept", "picked": "landed", "squashed": "landed", "branch": "committed",
		"reverted": "reworked", "undone": "reworked", "stale": "changed_kept", "wide": "changed_kept", "late": "changed_recent",
		"empty": "committed",
	} {
		if labels[id][0] != want {
			t.Errorf("%s: %s, want %s (%s)", id, labels[id][0], want, labels[id][1])
		}
	}
	for id, want := range map[string]string{
		"direct":   "1 of its 1 linked commit reached mainline (local Git).",
		"branch":   "1 linked commit, not on mainline (local Git).",
		"reverted": "A later commit reverted 1 of its 1 linked commit (local Git).",
		"empty":    "1 linked commit; mainline could not be checked (local Git).",
	} {
		if labels[id][1] != want {
			t.Errorf("%s evidence: %q, want %q", id, labels[id][1], want)
		}
	}
	var evidence string
	if err := f.catalog.DB.QueryRow(`SELECT group_concat(hash||' '||COALESCE(mainline_evidence,'-')||' '||COALESCE(revert_evidence,'-'),'; ')
		FROM (SELECT * FROM git_commits WHERE hash IN (?,?,?))`, squashed, reverted, undone).Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{squashed + " same change as " + squash[:12], reverted + " reachable from remotes/origin/main message", undone + " reachable from remotes/origin/main inverse_diff"} {
		if !strings.Contains(evidence, want) {
			t.Errorf("commit evidence %q lacks %q", evidence, want)
		}
	}
}

// Failing to look is never evidence: a repository local Git cannot read is
// "not checked", with the reason, and keeps nothing it did not find.
func TestSessionGitNotChecked(t *testing.T) {
	f := newGitFixture(t)
	f.session("elsewhere", "host_other_mac")
	f.edit("elsewhere", "main.go", 5)
	f.repo.commit(4.99, "Work", map[string]string{"main.go": "package main\n"})
	f.refresh()
	state := func(id string) string {
		t.Helper()
		rows, err := queryMaps(f.catalog.DB, `SELECT `+gitCheckSQL("w")+` git_check,`+gitCheckReasonSQL("w")+` reason,`+gitFactSQL("w", "sg.linked_commits")+` linked FROM workspaces w`+projectJoin("w")+` WHERE w.id=?`, id)
		if err != nil || len(rows) != 1 {
			t.Fatal(rows, err)
		}
		return fmt.Sprintf("%v|%v|%v", rows[0]["git_check"], rows[0]["reason"], rows[0]["linked"])
	}
	// A checkout another Mac recorded is not run, even when it exists here.
	if got := state("elsewhere"); got != "not_checked|No checkout of this repository was recorded on this Mac.|<nil>" {
		t.Fatalf("another Mac's checkout: %s", got)
	}
	if label := f.labels()["elsewhere"][0]; label != "changed_recent" {
		t.Fatalf("label without local Git: %s", label)
	}
	// A checkout this Mac recorded that is gone.
	f.session("gone", "")
	f.edit("gone", "main.go", 5)
	execTx(t, f.catalog.DB, `UPDATE workspace_sightings SET location='`+filepath.Join(t.TempDir(), "deleted")+`' WHERE workspace_id='gone'`)
	f.refresh()
	if got := state("gone"); got != "not_checked|The checkouts recorded on this Mac are gone.|<nil>" {
		t.Fatalf("a deleted checkout: %s", got)
	}
	// No git, and a repository too slow to read.
	execTx(t, f.catalog.DB, `UPDATE workspace_sightings SET location='`+f.repo.dir+`' WHERE workspace_id='gone'`)
	path := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	f.refresh()
	if got := state("gone"); got != "not_checked|Git is not installed on this Mac.|<nil>" {
		t.Fatalf("without git: %s", got)
	}
	t.Setenv("PATH", path)
	timeout := outcomeGitLogTimeout
	override(t, &outcomeGitLogTimeout, time.Nanosecond)
	f.refresh()
	if got := state("gone"); !strings.HasPrefix(got, "not_checked|Git took too long") {
		t.Fatalf("a timeout: %s", got)
	}
	// A timeout is tried again when the repository changes, not every pass.
	outcomeGitLogTimeout = timeout
	f.refresh()
	if got := state("gone"); !strings.HasPrefix(got, "not_checked|Git took too long") {
		t.Fatalf("a timeout tried again unchanged: %s", got)
	}
	f.repo.commit(4.5, "Later", map[string]string{"later.go": "package later\n"})
	f.refresh()
	if got := state("gone"); !strings.HasPrefix(got, "checked|<nil>|") {
		t.Fatalf("after the repository changed: %s", got)
	}
	// A repository session that changed nothing has no commits to look for.
	f.session("idle", "")
	f.refresh()
	if got := state("idle"); got != "not_applicable|<nil>|<nil>" {
		t.Fatalf("a session that changed nothing: %s", got)
	}
	// A folder session is not applicable, and none of this runs Git there.
	f.outcomeFixture.session("notes", "claude")
	execTx(t, f.catalog.DB, `UPDATE workspaces SET project_folder='~/notes' WHERE id='notes'`)
	if got := state("notes"); got != "not_applicable|<nil>|<nil>" {
		t.Fatalf("a folder: %s", got)
	}
}

// An unchanged repository is not scanned again, and a new commit or a
// changed session scans it again.
func TestSessionGitScansOnlyWhatChanged(t *testing.T) {
	f := newGitFixture(t)
	f.session("work", "")
	f.edit("work", "main.go", 5)
	f.repo.commit(4.99, "Work", map[string]string{"main.go": "package main\n"})
	logs := 0
	run := outcomeGitCommand
	override(t, &outcomeGitCommand, func(ctx context.Context, dir string, timeout time.Duration, stdin []byte, args ...string) ([]byte, error) {
		for _, arg := range args {
			if arg == "log" {
				logs++
				break
			}
		}
		return run(ctx, dir, timeout, stdin, args...)
	})
	f.refresh()
	if logs == 0 || len(f.links()["work"]) != 1 {
		t.Fatalf("first scan: %d logs, %v", logs, f.links())
	}
	// Nor does a pass that found nothing new write: the cached views stay.
	generation := func() (value int) {
		if err := f.catalog.DB.QueryRow("SELECT value FROM query_cache_generation WHERE id=1").Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	logs, before := 0, generation()
	f.refresh()
	if logs != 0 || generation() != before {
		t.Fatalf("an unchanged repository read its log %d times, generation %d to %d", logs, before, generation())
	}
	f.edit("work", "util.go", 4)
	f.repo.commit(3.99, "More", map[string]string{"util.go": "package util\n"})
	f.refresh()
	if logs == 0 || len(f.links()["work"]) != 2 {
		t.Fatalf("after new work: %d logs, %v", logs, f.links())
	}
	// Lines and edit times a session gained later scan again, and a
	// fingerprint is read as the bytes it is, spaces included.
	fingerprint := " \x00\x01\x02\x03\t"
	execTx(t, f.catalog.DB, `UPDATE session_files SET kept_lines=X'`+fmt.Sprintf("%x", fingerprint)+`' WHERE workspace_id='work' AND path='util.go'`)
	logs = 0
	f.refresh()
	if logs == 0 {
		t.Fatal("changed lines did not scan again")
	}
	sessions, _, err := f.catalog.readGitSessions(context.Background())
	if err != nil || !hasLine(sessions["work"].Kept["repo-app"]["util.go"], fingerprint) {
		t.Fatalf("fingerprints read: %v %v", sessions["work"].Kept, err)
	}
	execTx(t, f.catalog.DB, `UPDATE session_files SET first_edit_at='`+daysAgo(4.5)+`' WHERE workspace_id='work' AND path='util.go'`)
	logs = 0
	f.refresh()
	if logs == 0 {
		t.Fatal("an earlier first edit did not scan again")
	}
}

// A commit two sessions could have made goes to the session the checkout
// that made it or the lines it adds point to, before the session editing
// nearest the commit. In each case here the nearest is
// the wrong one.
func TestSessionGitContestedCommits(t *testing.T) {
	f := newGitFixture(t)
	repo := f.repo
	for _, id := range []string{"writer", "bystander", "worktree", "main", "most", "some", "neither", "near"} {
		f.session(id, "")
	}
	edit := func(session, name string, days float64, content string) {
		t.Helper()
		f.outcomeFixture.edit(session, filepath.Join(repo.dir, name), "repo-app", name, days, "", content)
	}
	// Lines: the commit adds the writer's lines.
	edit("writer", "lines.go", 10.02, code("written", 0, 3))
	edit("bystander", "lines.go", 10, code("other", 0, 3))
	byLines := repo.commit(9.99, "Lines", map[string]string{"lines.go": code("written", 0, 3) + "\n"})
	// Checkout: the commit was made in the worktree one session worked in.
	tree := filepath.Join(t.TempDir(), "tree")
	repo.git(time.Now(), "worktree", "add", "-q", "-b", "side", tree)
	execTx(t, f.catalog.DB, `UPDATE workspace_sightings SET location='`+tree+`' WHERE workspace_id='worktree'`)
	edit("worktree", "tree.go", 8.02, code("tree", 0, 2))
	edit("main", "tree.go", 8, code("main", 0, 2))
	byCheckout := (&gitRepository{t, tree}).commit(7.99, "Tree", map[string]string{"tree.go": "package tree\n"})
	// Lines that do not clearly favour one session (three to two) decide
	// nothing: the nearest, as a candidate.
	edit("most", "close.go", 5.02, code("most", 0, 3))
	edit("some", "close.go", 5, code("some", 0, 2))
	close := repo.commit(4.99, "Close", map[string]string{"close.go": code("most", 0, 3) + "\n" + code("some", 0, 2) + "\n"})
	// No signal: the nearest, as a candidate.
	edit("neither", "plain.go", 4.02, code("neither", 0, 2))
	edit("near", "plain.go", 4, code("near", 0, 2))
	nearest := repo.commit(3.99, "Plain", map[string]string{"plain.go": "package plain\n"})
	f.refresh()

	rows, err := queryMaps(f.catalog.DB, `SELECT workspace_id,hash,confidence,json_extract(evidence_json,'$.chosen_by') chosen_by FROM session_commit_links ORDER BY workspace_id`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, row := range rows {
		got[firstString(row["workspace_id"])] = fmt.Sprintf("%s %v %v", firstString(row["hash"])[:7], row["confidence"], row["chosen_by"])
	}
	want := map[string]string{
		"writer":   byLines[:7] + " 0.8 lines",
		"worktree": byCheckout[:7] + " 0.9 checkout",
		"near":     nearest[:7] + " 0.5 nearest",
		"some":     close[:7] + " 0.5 nearest",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("contested links:\n got %v\nwant %v", got, want)
	}
}

// A rebase's own commits were made in the checkout; the commit it started
// from, returned to or abandoned for was not.
func TestReflogMadeCommit(t *testing.T) {
	for action, want := range map[string]bool{
		"commit: Add login": true, "commit (amend): Add login": true, "cherry-pick: Fix": true, "revert: Revert \"Fix\"": true,
		"rebase (pick): Add login": true, "rebase -i (fixup): Add login": true, "rebase (continue): Add login": true,
		"rebase (start): checkout main": false, "rebase -i (start): checkout origin/main": false, "rebase (finish): returning to refs/heads/feature": false,
		"rebase (abort): returning to refs/heads/feature": false, "rebase: checkout main": false, "rebase finished: returning to refs/heads/x": false, "rebase: fast-forward": false,
		"checkout: moving from main to feature": false, "reset: moving to HEAD~1": false, "pull: Fast-forward": false,
	} {
		if reflogMadeCommit(action) != want {
			t.Errorf("%q: %v, want %v", action, !want, want)
		}
	}
}

func TestReadAddedLines(t *testing.T) {
	hash := strings.Repeat("a", 40)
	output := "\x1e" + hash + "\n\ndiff --git app/main.go app/main.go\nindex 1..2 100644\n--- app/main.go\n+++ app/main.go\n@@ -1 +1,2 @@\n-removed := old(1)\n" +
		"+added := value(1)\n++++ looks like a header but is content\n diff --git context line\ndiff --git gone.go gone.go\ndeleted file mode 100644\n--- gone.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-gone := value(2)\n"
	files := readAddedLines(output)[hash]
	want := lineSet{}
	fingerprintLines("added := value(1)\n+++ looks like a header but is content", want)
	if len(files) != 1 || fmt.Sprint(files["app/main.go"]) != fmt.Sprint(want) {
		t.Fatalf("added lines: %v", files)
	}
}
