package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
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
	return r.commitAt(time.Now().Add(-time.Duration(days*float64(24*time.Hour))), message, files)
}

// commitAt writes files and commits them at a time, returning the hash.
func (r *gitRepository) commitAt(at time.Time, message string, files map[string]string) string {
	r.t.Helper()
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

// command records a shell call of the session's that ran a command from at,
// for took, and printed result.
func (f *gitFixture) command(session, command, result string, at time.Time, took time.Duration) {
	f.t.Helper()
	f.calls++
	id := fmt.Sprintf("command-%d", f.calls)
	text, _ := json.Marshal(map[string]any{"content": result, "is_error": false})
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	execTx(f.t, f.catalog.DB,
		fmt.Sprintf(`INSERT INTO messages(id,conversation_id,native_id,role,kind,text,created_at,content_hash) VALUES('%s','%s-c','%s','tool','tool_result',%s,'x','h')`, id, session, id, quote(string(text))),
		fmt.Sprintf(`INSERT INTO tool_calls(id,workspace_id,conversation_id,sequence,provider,kind,tool_name,tool_category,command,command_category,status,started_at,ended_at,result_message_id)
			VALUES('%s','%s','%s-c',%d,'claude','tool','Bash','command',%s,'vcs','ok','%s','%s','%s')`, id, session, session, f.calls, quote(command),
			at.UTC().Format(time.RFC3339Nano), at.Add(took).UTC().Format(time.RFC3339Nano), id))
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
		"early", "printer", "printer-too", "detached", "editor", "publisher", "empty", "cloned"} {
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
	// Two sessions printed one commit, cherry-picked to mainline: each is
	// linked once, to the copy, as a rewrite of the commit it printed.
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
	// A session that only committed another's work (opening the pull
	// request) keeps the printed hash, and the session that edited its files
	// is linked too.
	f.edit("editor", "pr.go", 2.6)
	pr := repo.commit(2.59, "Pull request", map[string]string{"pr.go": "package pr\n"})
	f.printed("publisher", pr, 2.59)
	repo.git(time.Now(), "update-ref", "refs/remotes/origin/main", "HEAD")
	// Another clone of the repository, with a branch only it has.
	clone := &gitRepository{t, filepath.Join(t.TempDir(), "clone")}
	repo.git(time.Now(), "clone", "-q", repo.dir, clone.dir)
	clone.git(time.Now(), "checkout", "-q", "-b", "unpushed")
	clone.git(time.Now(), "config", "color.ui", "always")
	execTx(t, f.catalog.DB, `UPDATE workspace_sightings SET location='`+clone.dir+`' WHERE workspace_id='cloned'`)
	f.edit("cloned", "cloned.go", 2.5)
	cloned := clone.commit(2.49, "Cloned", map[string]string{"cloned.go": "package cloned\n"})
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
		"early": early[:7] + " transcript 1", "printer": printedCopy[:7] + " rewrite 1", "printer-too": printedCopy[:7] + " rewrite 1", "detached": detached[:7] + " transcript 1",
		"editor": pr[:7] + " inferred 0.7", "publisher": pr[:7] + " transcript 1", "empty": empty[:7] + " transcript 1", "cloned": cloned[:7] + " inferred 0.7",
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
		"editor": "landed", "publisher": "landed", "empty": "committed", "cloned": "committed",
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
	// Either clone can say so: the other's origin is this checkout's main.
	for _, want := range []string{squashed + " same change as " + squash[:12] + " ", reverted + " reachable from remotes/origin/(main|HEAD) message", undone + " reachable from remotes/origin/(main|HEAD) inverse_diff"} {
		if !regexp.MustCompile(want).MatchString(evidence) {
			t.Errorf("commit evidence %q lacks %q", evidence, want)
		}
	}
}

// Failing to look is never evidence: a repository local Git cannot read is
// "not checked", with the reason, and keeps nothing it did not find.
func TestSessionGitNotChecked(t *testing.T) {
	// The Library's Git counts read their project's kind, and local Git's
	// counts its check too, whichever columns are shown.
	for selected, want := range map[string]string{"pr_count": "project_kind", "branch_count": "project_kind", "linked_commits": "git_check_reason"} {
		if fields := queryRowActionFields("library", []string{"title", selected}); !slices.Contains(fields, want) {
			t.Errorf("library row fields for %s: %v", selected, fields)
		}
	}
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
	// So is one whose directory keeps a .git that holds no repository.
	husk := filepath.Join(t.TempDir(), "husk")
	if err := os.MkdirAll(filepath.Join(husk, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	execTx(t, f.catalog.DB, `UPDATE workspace_sightings SET location='`+husk+`' WHERE workspace_id='gone'`)
	f.refresh()
	if got := state("gone"); got != "not_checked|The checkouts recorded on this Mac are gone.|<nil>" {
		t.Fatalf("a deleted worktree's leftover directory: %s", got)
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
	// A scan that fails afterwards keeps the commits it did not look at, and
	// how far back they were read.
	since := func() string {
		t.Helper()
		rows, err := queryMaps(f.catalog.DB, `SELECT history_since FROM git_repository_scans WHERE repository_id='repo-app' AND host_id=?`, currentHost().ID)
		if err != nil || len(rows) != 1 {
			t.Fatal(rows, err)
		}
		return firstString(rows[0]["history_since"])
	}
	checkedSince := since()
	if err := f.catalog.writeGitScan(t.Context(), "repo-app", currentHost().ID, "failed", gitScan{Status: "not_checked", Reason: "Git took too long to read this repository."}); err != nil {
		t.Fatal(err)
	}
	if got := since(); checkedSince == "" || got != checkedSince || countRows(t, f.catalog, `SELECT COUNT(*) FROM git_commit_files WHERE repository_id='repo-app'`) == 0 {
		t.Fatalf("a failed scan's history_since %q, want %q", got, checkedSince)
	}
	// The reflogs are extra: when they cannot be read in time, the scan goes
	// on without them.
	run := outcomeGitCommand
	override(t, &outcomeGitCommand, func(ctx context.Context, dir string, timeout time.Duration, stdin []byte, args ...string) ([]byte, error) {
		if slices.Contains(args, "--reflog") {
			return nil, errGitTimeout
		}
		return run(ctx, dir, timeout, stdin, args...)
	})
	f.repo.commit(4.4, "Later still", map[string]string{"later.go": "package later // still\n"})
	f.refresh()
	if got := state("gone"); !strings.HasPrefix(got, "checked|<nil>|") {
		t.Fatalf("without the reflogs: %s", got)
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
	// A recorded folder inside some other repository (a home folder kept in
	// Git, once the checkout in it is gone) is not a checkout of this one:
	// it needs the repository's root commit or remote, or, with neither
	// known, to be the checkout itself.
	home := filepath.Join(t.TempDir(), "home")
	gitInit(t, home)
	folder := filepath.Join(home, "app")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	root := f.repo.git(time.Now(), "rev-list", "--max-parents=0", "HEAD")
	f.repo.git(time.Now(), "remote", "add", "origin", "git@github.com:Owner/App.git")
	ctx := context.Background()
	for _, check := range []struct {
		identity  gitIdentity
		locations []string
		want      string
	}{
		{gitIdentity{Root: root}, []string{folder}, "The checkouts recorded on this Mac hold another repository."},
		{gitIdentity{Remotes: []string{"github.com/owner/app"}}, []string{folder}, "The checkouts recorded on this Mac hold another repository."},
		{gitIdentity{}, []string{folder}, "The checkouts recorded on this Mac hold another repository."},
		{gitIdentity{Root: root}, []string{folder, f.repo.dir}, f.repo.dir},
		{gitIdentity{Root: strings.Repeat("0", 40), Remotes: []string{"github.com/owner/app"}}, []string{f.repo.dir}, f.repo.dir},
		{gitIdentity{}, []string{folder, f.repo.dir}, f.repo.dir},
	} {
		clones, reason := gitCheckouts(ctx, check.locations, check.identity)
		if len(clones) == 1 {
			reason = clones[0].Dir
		}
		if reason != check.want || len(clones) > 1 {
			t.Errorf("%+v in %v: %s, want %s", check.identity, check.locations, reason, check.want)
		}
	}
}

// A library drive moves between Macs, each with its own clones. A Mac with
// no checkout, or whose clone lacks another's branch, leaves that Mac's links
// and checked state alone; a Mac drops its own link once the commit is gone
// from its clones.
func TestSessionGitAcrossMacs(t *testing.T) {
	f := newGitFixture(t)
	home := currentHost().ID
	f.session("work", "")
	f.edit("work", "main.go", 5)
	f.repo.git(time.Now(), "checkout", "-q", "-b", "local")
	commit := f.repo.commit(4.99, "Work", map[string]string{"main.go": "package main\n"})
	f.repo.git(time.Now(), "checkout", "-q", "main")
	state := func() string {
		t.Helper()
		rows, err := queryMaps(f.catalog.DB, `SELECT status,COALESCE(reason,'') reason,linked_commits FROM session_git WHERE workspace_id='work'`)
		if err != nil || len(rows) != 1 {
			t.Fatal(rows, err)
		}
		return fmt.Sprintf("%v|%v|%v|%s|%s", rows[0]["status"], rows[0]["reason"], rows[0]["linked_commits"], strings.Join(f.links()["work"], ","), f.labels()["work"][0])
	}
	f.refresh()
	want := "checked||1|" + commit[:7] + " inferred 0.7|committed"
	if got := state(); got != want {
		t.Fatalf("first Mac: %s", got)
	}
	useHost(t, "host_other_mac")
	f.refresh()
	if got := state(); got != want {
		t.Fatalf("a Mac without a checkout: %s", got)
	}
	other := filepath.Join(t.TempDir(), "other")
	f.repo.git(time.Now(), "clone", "-q", "--single-branch", "--branch", "main", f.repo.dir, other)
	execTx(t, f.catalog.DB, `INSERT INTO workspace_sightings(workspace_id,host_id,source_name,location,first_seen_at,last_seen_at) VALUES('work','host_other_mac','claude','`+other+`','x','x')`)
	f.refresh()
	if got := state(); got != want || countRows(t, f.catalog, `SELECT COUNT(*) FROM git_repository_scans WHERE host_id='host_other_mac' AND status='checked'`) != 1 {
		t.Fatalf("a Mac whose clone lacks the branch: %s", got)
	}
	useHost(t, home)
	f.refresh()
	if got := state(); got != want {
		t.Fatalf("back on the first Mac: %s", got)
	}
	// Gone means no ref or reflog holds it: a reflog's commit is read as
	// replaced, and still linked.
	f.repo.git(time.Now(), "branch", "-q", "-D", "local")
	f.repo.git(time.Now(), "reflog", "expire", "--expire=now", "--all")
	f.refresh()
	if got := state(); got != "checked||0||changed_recent" {
		t.Fatalf("after the branch was deleted: %s", got)
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
	// A scan rewrites only the links and commits that changed: a commit
	// that links to no session writes nothing, and the views stay.
	execTx(t, f.catalog.DB, `CREATE TABLE link_writes(n INTEGER)`,
		`CREATE TRIGGER links_inserted AFTER INSERT ON session_commit_links BEGIN INSERT INTO link_writes VALUES(1); END`,
		`CREATE TRIGGER links_deleted AFTER DELETE ON session_commit_links BEGIN INSERT INTO link_writes VALUES(1); END`,
		`CREATE TRIGGER commits_inserted AFTER INSERT ON git_commits BEGIN INSERT INTO link_writes VALUES(1); END`,
		`CREATE TRIGGER commits_deleted AFTER DELETE ON git_commits BEGIN INSERT INTO link_writes VALUES(1); END`)
	f.repo.commit(4.5, "Unrelated", map[string]string{"other.go": "package other\n"})
	f.refresh()
	if writes := countRows(t, f.catalog, `SELECT COUNT(*) FROM link_writes`); logs == 0 || writes != 0 || generation() != before {
		t.Fatalf("a scan that linked nothing new: %d logs, %d writes, generation %d to %d", logs, writes, before, generation())
	}
	// New work writes its link and commit, and the first link again, since
	// its evidence holds the session's window, now wider.
	f.edit("work", "util.go", 4)
	f.repo.commit(3.99, "More", map[string]string{"util.go": "package util\n"})
	f.refresh()
	if writes := countRows(t, f.catalog, `SELECT COUNT(*) FROM link_writes`); logs == 0 || len(f.links()["work"]) != 2 || writes != 3 {
		t.Fatalf("after new work: %d logs, %d writes, %v", logs, writes, f.links())
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

// A session linked to a change and its copy on mainline is linked once, to
// the copy, with the stronger evidence: a squash that kept the patch but not
// the subject is no rewrite, so only inference linked the copy itself.
func TestLinkOnceKeepsTheStrongerEvidence(t *testing.T) {
	landed := true
	commits := map[string]*gitCommit{
		"original": {Hash: "original", Subject: "Work", OnMainline: &landed, SameAs: "squash"},
		"squash":   {Hash: "squash", Subject: "Work (#7)", OnMainline: &landed},
	}
	links := linkOnce([]gitLink{
		{Session: "s", Hash: "original", Method: "transcript", Confidence: 1},
		{Session: "s", Hash: "squash", Method: "inferred", Confidence: 0.7},
		{Session: "other", Hash: "squash", Method: "inferred", Confidence: 0.5},
	}, commits)
	if got := fmt.Sprint(links); got != fmt.Sprint([]gitLink{
		{Session: "s", Hash: "squash", Method: "transcript", Confidence: 1},
		{Session: "other", Hash: "squash", Method: "inferred", Confidence: 0.5},
	}) {
		t.Fatalf("links: %s", got)
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

// A session's own git commands link the commits they made, whatever tool
// wrote the files and whether or not the commit printed its hash. Edit
// windows link only what no command explains, as commits made by hand.
func TestSessionGitCommandLinks(t *testing.T) {
	f := newGitFixture(t)
	repo := f.repo
	// The checkout is the repository's by its remote (gitIdentity.holds).
	execTx(t, f.catalog.DB, `UPDATE repositories SET normalized_remote='github.com/acme/app' WHERE id='repo-app'`)
	repo.git(time.Now(), "remote", "add", "origin", "git@github.com:acme/app.git")
	for _, id := range []string{"quiet", "heredoc", "scripted", "wrong", "hand", "nested", "rebased", "long", "short", "treed", "listed", "twin", "pulled", "picker"} {
		f.session(id, "")
	}
	base := time.Now().Add(-20 * 24 * time.Hour).Truncate(time.Second)
	at := func(hours float64, seconds int) time.Time {
		return base.Add(time.Duration(hours*float64(time.Hour)) + time.Duration(seconds)*time.Second)
	}
	// A quiet commit prints no hash: the message its command wrote names it,
	// and so does one from a here document, its subject over two lines.
	f.command("quiet", `git add -A && git commit -q -m "Quiet commit with a message"`, "", at(0, 0), 3*time.Second)
	quiet := repo.commitAt(at(0, 1), "Quiet commit with a message", map[string]string{"quiet.go": "package quiet\n"})
	f.command("heredoc", "git commit -q -F - <<'EOF'\nWrite the subject\nacross two lines\n\nThe body.\nEOF", "", at(2, 0), 3*time.Second)
	heredoc := repo.commitAt(at(2, 1), "Write the subject\nacross two lines\n\nThe body.", map[string]string{"heredoc.go": "package heredoc\n"})
	// A message from a file is unknown: the one command running links it. The
	// stash it made first is not the session's commit.
	f.command("scripted", "git stash -q && git stash apply -q && git commit -q -a -F .git/MESSAGE", "", at(4, 0), 4*time.Second)
	if err := os.WriteFile(filepath.Join(repo.dir, "quiet.go"), []byte("package quiet // changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo.git(at(4, 1), "stash", "-q")
	repo.git(at(4, 1), "stash", "apply", "-q")
	scripted := repo.commitAt(at(4, 2), "From a file", nil)
	// A command that wrote another subject did not make the commit: it was
	// made by hand, and the session editing its file is linked.
	f.edit("hand", "hand.go", time.Since(at(6, -600)).Hours()/24)
	f.command("wrong", `git commit -m "A subject nobody used"`, "", at(6, 0), 2*time.Second)
	hand := repo.commitAt(at(6, 1), "Hand made at that moment", map[string]string{"hand.go": "package hand\n"})
	// A command run in another repository's checkout links there, and not
	// the commit made in its own at that moment.
	lib := newGitRepository(t)
	execTx(t, f.catalog.DB, `INSERT INTO repositories(id,display_name,local_locations_json,created_at,updated_at) VALUES('repo-lib','lib','["`+lib.dir+`"]','x','x')`)
	f.outcomeFixture.session("lib-home", "claude")
	execTx(t, f.catalog.DB, `UPDATE workspaces SET repository_id='repo-lib' WHERE id='lib-home'`,
		fmt.Sprintf(`INSERT INTO workspace_sightings(workspace_id,host_id,source_name,location,first_seen_at,last_seen_at) VALUES('lib-home','%s','claude','%s','x','x')`, currentHost().ID, lib.dir))
	f.command("nested", `cd "`+lib.dir+`" && git commit -qm "Library change from afar"`, "", at(8, 0), 3*time.Second)
	nested := lib.commitAt(at(8, 1), "Library change from afar", map[string]string{"lib.go": "package lib\n"})
	repo.commitAt(at(8, 1), "Library change from afar", map[string]string{"app.go": "package app\n"})
	// A rebase keeps the author, author time and subject: its copy is the
	// commit the session printed, linked once.
	repo.git(at(10, 0), "checkout", "-q", "-b", "topic")
	original := repo.commitAt(at(10, 1), "Rebased work", map[string]string{"topic.go": "package topic\n"})
	f.command("rebased", `git commit -qam "$MESSAGE"`, "[topic "+original[:7]+"] Rebased work\n 1 file changed\n", at(10, 0), 3*time.Second)
	repo.git(at(10, 0), "checkout", "-q", "main")
	repo.commitAt(at(10.5, 0), "Main moves on", map[string]string{"main-1.go": "package main1\n"})
	repo.git(at(11, 0), "rebase", "-q", "main", "topic")
	rebased := repo.git(at(11, 0), "rev-parse", "topic")
	repo.git(at(11, 0), "checkout", "-q", "main")
	// Two sessions' commands were running: the shorter, as a candidate.
	f.command("long", "git commit -q -F .git/MESSAGE", "", at(12, 0), time.Minute)
	f.command("short", "git commit -q -F .git/MESSAGE", "", at(12, 20), 5*time.Second)
	concurrent := repo.commitAt(at(12, 22), "Concurrent work", map[string]string{"concurrent.go": "package concurrent\n"})
	// Squashed onto mainline, more than one commit at a time (patch-id finds
	// one): recognized by the branch's tree, by a subject the squash lists
	// that no other commit has, or by the one pull request the session opened.
	squash := func(branch string, hours float64, message string) string {
		repo.git(at(hours, 0), "merge", "-q", "--squash", branch)
		repo.git(at(hours, 0), "commit", "-q", "-m", message)
		return repo.git(at(hours, 0), "rev-parse", "HEAD")
	}
	repo.git(at(14, 0), "checkout", "-q", "-b", "tree-branch")
	f.command("treed", `git commit -qam "Tree branch work"`, "", at(14, 0), 3*time.Second)
	treed := repo.commitAt(at(14, 1), "Tree branch work", map[string]string{"treed.go": "package treed\n"})
	f.command("treed", `git commit -qam "Tree branch more work"`, "", at(14, 10), 3*time.Second)
	treedMore := repo.commitAt(at(14, 11), "Tree branch more work", map[string]string{"treed.go": "package treed\n\nvar more = 1\n"})
	repo.git(at(14, 0), "checkout", "-q", "main")
	treeSquash := squash("tree-branch", 15, "Tree squash (#11)")
	repo.git(at(16, 0), "checkout", "-q", "-b", "list-branch")
	f.command("listed", `git commit -qm "Listed subject that is long enough"`, "", at(16, 0), 3*time.Second)
	listed := repo.commitAt(at(16, 1), "Listed subject that is long enough", map[string]string{"listed.go": "package listed\n"})
	f.command("listed", `git commit -qm "Common subject seen on two branches"`, "", at(16, 10), 3*time.Second)
	common := repo.commitAt(at(16, 11), "Common subject seen on two branches", map[string]string{"common.go": "package common\n"})
	repo.git(at(17, 0), "checkout", "-q", "-b", "twin-branch", "main")
	f.command("twin", `git commit -qm "Common subject seen on two branches"`, "", at(17, 0), 3*time.Second)
	twin := repo.commitAt(at(17, 1), "Common subject seen on two branches", map[string]string{"twin.go": "package twin\n"})
	repo.git(at(17, 0), "checkout", "-q", "main")
	repo.commitAt(at(18, 0), "Main moves again", map[string]string{"main-2.go": "package main2\n"})
	listSquash := squash("list-branch", 19, "List squash (#12)\n\n* Listed subject that is long enough\n\n* Common subject seen on two branches")
	repo.git(at(20, 0), "checkout", "-q", "-b", "pr-branch")
	f.command("pulled", `git commit -qm "Short one"`, "", at(20, 0), 3*time.Second)
	pulled := repo.commitAt(at(20, 1), "Short one", map[string]string{"pulled.go": "package pulled\n"})
	f.command("pulled", `git commit -qm "Short two"`, "", at(20, 10), 3*time.Second)
	pulledMore := repo.commitAt(at(20, 11), "Short two", map[string]string{"pulled-2.go": "package pulled\n"})
	f.command("pulled", "gh pr create --fill", "https://github.com/acme/app/pull/13\n", at(20, 30), 5*time.Second)
	// A commit on another branch after opening the pull request is not one
	// of its commits: the squash changed none of its files.
	repo.git(at(20, 40), "checkout", "-q", "-b", "side-branch", "main")
	f.command("pulled", `git commit -qm "Side work"`, "", at(20, 40), 3*time.Second)
	side := repo.commitAt(at(20, 41), "Side work", map[string]string{"side.go": "package side\n"})
	repo.git(at(20, 0), "checkout", "-q", "main")
	repo.commitAt(at(21, 0), "Main moves once more", map[string]string{"main-3.go": "package main3\n"})
	prSquash := squash("pr-branch", 22, "PR squash (#13)")
	// A cherry-pick prints the hash it made: linked directly, without an
	// edit or a commit message.
	repo.git(at(23, 0), "checkout", "-q", "-b", "pick-source")
	source := repo.commitAt(at(23, 1), "Picked by a command", map[string]string{"pick.go": "package pick\n"})
	repo.git(at(23, 0), "checkout", "-q", "main")
	repo.git(at(23, 10), "cherry-pick", source)
	cherry := repo.git(at(23, 10), "rev-parse", "HEAD")
	f.command("picker", "git cherry-pick "+source[:7], "[main "+cherry[:7]+"] Picked by a command\n", at(23, 10), 3*time.Second)
	repo.git(time.Now(), "update-ref", "refs/remotes/origin/main", "HEAD")
	f.refresh()

	links := f.links()
	messages := func(hashes ...string) string {
		links := []string{}
		for _, hash := range hashes {
			links = append(links, hash[:7]+" message 0.95")
		}
		slices.Sort(links)
		return strings.Join(links, ", ")
	}
	for id, want := range map[string]string{
		"quiet": messages(quiet), "heredoc": messages(heredoc), "scripted": scripted[:7] + " window 0.9",
		"hand": hand[:7] + " inferred 0.7", "nested": messages(nested), "rebased": rebased[:7] + " rewrite 1",
		"short": concurrent[:7] + " window 0.5", "treed": messages(treed, treedMore), "listed": messages(listed, common),
		"twin": messages(twin), "pulled": messages(pulled, pulledMore, side), "picker": cherry[:7] + " transcript 1",
	} {
		if strings.Join(links[id], ", ") != want {
			t.Errorf("%s links: %v, want %s", id, links[id], want)
		}
	}
	for _, id := range []string{"wrong", "long", "lib-home"} {
		if len(links[id]) != 0 {
			t.Errorf("%s links: %v", id, links[id])
		}
	}
	rows, err := queryMaps(f.catalog.DB, `SELECT hash,COALESCE(on_mainline,-1) on_mainline,COALESCE(mainline_evidence,'') evidence FROM git_commits WHERE hash IN (?,?,?,?,?,?,?,?,?)`,
		treed, treedMore, listed, common, twin, pulled, pulledMore, rebased, side)
	if err != nil {
		t.Fatal(err)
	}
	mainline := map[string]string{}
	for _, row := range rows {
		mainline[firstString(row["hash"])] = fmt.Sprint(row["on_mainline"], " ", row["evidence"])
	}
	for hash, want := range map[string]string{
		treed: "1 squashed into " + treeSquash[:12] + " on remotes/origin/main", treedMore: "1 squashed into " + treeSquash[:12] + " on remotes/origin/main",
		listed: "1 listed in " + listSquash[:12] + " on remotes/origin/main", common: "0 ", twin: "0 ", rebased: "0 ",
		pulled: "1 pull request #13, " + prSquash[:12] + " on remotes/origin/main", pulledMore: "1 pull request #13, " + prSquash[:12] + " on remotes/origin/main",
		side: "0 ",
	} {
		if mainline[hash] != want {
			t.Errorf("%s on mainline: %q, want %q", hash[:7], mainline[hash], want)
		}
	}
	labels := f.labels()
	for id, want := range map[string]string{"quiet": "landed", "treed": "landed", "listed": "landed", "pulled": "landed", "twin": "committed", "rebased": "committed", "wrong": "no_changes"} {
		if labels[id][0] != want {
			t.Errorf("%s: %s, want %s (%s)", id, labels[id][0], want, labels[id][1])
		}
	}
	for id, want := range map[string]string{"listed": "2 2 1", "twin": "1 1 0", "quiet": "1 1 1"} {
		var got string
		if err := f.catalog.DB.QueryRow(`SELECT linked_commits||' '||lines_added||' '||lines_landed FROM session_git WHERE workspace_id=?`, id).Scan(&got); err != nil || got != want {
			t.Errorf("%s commits and lines: %q, want %q (%v)", id, got, want, err)
		}
	}
	// What a session committed has a file history, though it edited nothing.
	if count := countRows(t, f.catalog, `SELECT COUNT(*) FROM git_commit_files WHERE path='quiet.go'`); count != 2 {
		t.Errorf("quiet.go has %d commits in its history", count)
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
