package archive

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Tier 2 outcome evidence (docs/projects-and-outcomes.md): the commits in
// local Git linked to a session, whether they reached mainline and whether a
// later commit reverted them. Git runs only in background stages, after the
// tools stage derives the Tier 1 facts it starts from, and only in checkouts
// this Mac recorded (workspace_sightings). Every command is read-only, has a
// timeout, runs at low priority and waits while pages read; requests read
// the stored facts. A repository whose refs and sessions did not change since
// its last scan is not asked again.
//
// Only facts are stored: hashes, times, counts, the files a link shares and
// how a commit was reverted. No commit message, diff or file content. For
// file history, every commit that changed a file some session edited keeps
// that file's path and line counts (git_commit_files).

// outcomeGitVersion scans every repository again when the rules change.
const outcomeGitVersion = "2"

// outcomeGitGrace is how long after a session's last edit a commit can still
// be inferred to be its own: an agent or person commits soon after the work.
// Calibrated against transcript-linked commits; see the design document.
var outcomeGitGrace = time.Hour

// outcomeGitCounted is the least confidence of a link that counts toward a
// session's commits and label. Measured against printed hashes, a commit
// inferred for the only session that could have made it was right every
// time, one of several told apart by its checkout or lines 92% of the time
// or better, and one given to the nearest of several about half the time:
// those links are kept, with their evidence, as candidates only.
const outcomeGitCounted = 0.7

// Timeouts for one repository's commands. A slow repository is "not
// checked" rather than holding the stage.
var (
	outcomeGitLogTimeout   = 60 * time.Second
	outcomeGitShortTimeout = 10 * time.Second
)

const outcomeGitSQL = `
CREATE TABLE IF NOT EXISTS git_commits(
  repository_id TEXT NOT NULL,
  hash TEXT NOT NULL,
  author_at TEXT NOT NULL,
  committed_at TEXT NOT NULL,
  parents INTEGER NOT NULL,
  files INTEGER NOT NULL,
  lines_added INTEGER,
  lines_removed INTEGER,
  on_mainline INTEGER,
  mainline_evidence TEXT,
  reverted_by TEXT,
  revert_evidence TEXT,
  ai_note INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(repository_id,hash)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS session_commit_links(
  workspace_id TEXT NOT NULL,
  repository_id TEXT NOT NULL,
  hash TEXT NOT NULL,
  method TEXT NOT NULL,
  confidence REAL NOT NULL,
  evidence_json TEXT NOT NULL,
  PRIMARY KEY(workspace_id,repository_id,hash)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS session_commit_links_commit_idx ON session_commit_links(repository_id,hash);
CREATE TABLE IF NOT EXISTS session_git(
  workspace_id TEXT PRIMARY KEY,
  status TEXT NOT NULL,
  reason TEXT,
  linked_commits INTEGER NOT NULL DEFAULT 0,
  landed_commits INTEGER NOT NULL DEFAULT 0,
  reverted_commits INTEGER NOT NULL DEFAULT 0,
  mainline_unknown INTEGER NOT NULL DEFAULT 0,
  checked_at TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS git_commit_files(
  repository_id TEXT NOT NULL,
  hash TEXT NOT NULL,
  path TEXT NOT NULL,
  lines_added INTEGER NOT NULL,
  lines_removed INTEGER NOT NULL,
  PRIMARY KEY(repository_id,path,hash)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS git_repository_scans(
  repository_id TEXT NOT NULL,
  host_id TEXT NOT NULL,
  scan_key TEXT NOT NULL,
  status TEXT NOT NULL,
  reason TEXT,
  scanned_at TEXT NOT NULL,
  PRIMARY KEY(repository_id,host_id)) WITHOUT ROWID;
`

func (c *Catalog) ensureOutcomeGit() error {
	_, err := c.DB.Exec(outcomeGitSQL)
	return err
}

// editSpan is when a session first and last edited a file.
type editSpan struct{ First, Last time.Time }

// gitSession is a session as linking reads it: its edit window, the files
// it edited in each repository and the lines it kept in them, the hashes its
// own git commit printed, and its checkouts on this Mac.
type gitSession struct {
	ID          string
	First, Last time.Time
	Timed       bool
	Files       map[string]map[string]editSpan
	Kept        map[string]map[string]lineSet
	Printed     []outcomeCommit
	Locations   []string
	key         []string
}

// gitCommit is one commit of a scan.
type gitCommit struct {
	Hash             string
	Parents          int
	Author, Commit   time.Time
	Files            map[string][2]int64
	Added, Removed   int64
	OnMainline       *bool
	MainlineEvidence string
	SameAs           string
	RevertedBy       string
	RevertEvidence   string
	AINote           bool
}

// gitLink links a session to a commit, with the evidence that decided it.
type gitLink struct {
	Session, Hash string
	Method        string
	Confidence    float64
	Evidence      map[string]any
}

// gitScan is what one repository's scan found, or why it could not run.
type gitScan struct {
	Status, Reason string
	Commits        map[string]*gitCommit
	Links          []gitLink
	// Edited are the repository's files some session edited: the commits
	// that changed them are kept for file history.
	Edited map[string]bool
}

var errGitTimeout = errors.New("git timed out")

// gitRawDiff reads a diff as stored, running no program the repository names.
var gitRawDiff = []string{"--no-textconv", "--no-ext-diff"}

// outcomeGitCommand runs git read-only in dir: no lazy fetch from a promisor
// remote, no optional locks, no prompts, at low priority when nice is there.
// Every diff it reads passes gitRawDiff, since a repository's attributes can
// name a text converter or diff program, which would run (and a converter
// with a cache writes notes refs).
var outcomeGitCommand = func(ctx context.Context, dir string, timeout time.Duration, stdin []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	name, full := "git", append([]string{"-C", dir}, args...)
	if nice, err := exec.LookPath("nice"); err == nil {
		name, full = nice, append([]string{"-n", "10", "git"}, full...)
	}
	command := exec.CommandContext(ctx, name, full...)
	command.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	output, err := command.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, errGitTimeout
	}
	return output, err
}

// refreshSessionGit links sessions to local commits, repository by
// repository, and records each session's Tier 2 state.
func (c *Catalog) refreshSessionGit(ctx context.Context) error {
	sessions, byRepository, err := c.readGitSessions(ctx)
	if err != nil {
		return err
	}
	locations, err := c.gitLocations(ctx)
	if err != nil {
		return err
	}
	host := currentHost().ID
	stored, err := queryMapsContext(ctx, c.DB, "SELECT repository_id,scan_key,status,reason FROM git_repository_scans WHERE host_id=?", host)
	if err != nil {
		return err
	}
	previous := map[string]map[string]any{}
	for _, row := range stored {
		previous[firstString(row["repository_id"])] = row
	}
	repositories := make([]string, 0, len(byRepository))
	for repository := range byRepository {
		repositories = append(repositories, repository)
	}
	slices.Sort(repositories)
	_, gitErr := exec.LookPath("git")
	reportWork(ctx, "linking commits", "repository", 0, len(repositories))
	for index, repository := range repositories {
		if err := c.queries.wait(ctx); err != nil {
			return err
		}
		members := byRepository[repository]
		scan := gitScan{Status: "not_checked"}
		dir, reason := gitCheckout(locations[repository])
		// A scan that could not start is keyed by its reason; one that
		// started and failed, by what it read and the day, so that a timeout
		// is tried again when the repository changes or the next day.
		key, failedKey, heads := "", "", []string(nil)
		switch {
		case gitErr != nil:
			scan.Reason = "Git is not installed on this Mac."
		case dir == "":
			scan.Reason = reason
		default:
			key, heads, err = gitScanKey(ctx, dir, sessions, members, repository)
			if err != nil {
				scan.Reason = gitFailure(err)
			} else {
				failedKey = "unchecked:" + key + ":" + time.Now().Format(time.DateOnly)
			}
		}
		if failedKey == "" {
			failedKey = "unchecked:" + scan.Reason
		}
		if stored := firstString(previous[repository]["scan_key"]); stored != "" && (stored == key || stored == failedKey) {
			reportWork(ctx, "linking commits", "repository", index+1, len(repositories))
			continue
		}
		if scan.Reason == "" {
			scan = scanRepository(ctx, dir, sessions, members, repository, heads)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if scan.Status != "checked" {
			key = failedKey
		}
		if err := c.writeGitScan(ctx, repository, host, key, scan); err != nil {
			return err
		}
		reportWork(ctx, "linking commits", "repository", index+1, len(repositories))
	}
	return c.writeSessionGit(ctx, sessions, byRepository)
}

// readGitSessions reads the sessions that could have commits: those that
// edited files in a repository or printed a commit, outside mirrors.
func (c *Catalog) readGitSessions(ctx context.Context) (map[string]*gitSession, map[string][]string, error) {
	rows, err := queryMapsContext(ctx, c.DB, `SELECT so.workspace_id,so.first_edit_at,so.last_edit_at,so.commits_json,COALESCE(w.repository_id,w.folder_repository_id) repository_id
		FROM session_outcomes so JOIN workspaces w ON w.id=so.workspace_id
		WHERE so.workspace_id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces) AND (so.files_changed>0 OR so.commits_json<>'[]')`)
	if err != nil {
		return nil, nil, err
	}
	sessions := map[string]*gitSession{}
	byRepository := map[string][]string{}
	add := func(repository, session string) {
		if repository != "" && !slices.Contains(byRepository[repository], session) {
			byRepository[repository] = append(byRepository[repository], session)
		}
	}
	for _, row := range rows {
		session := &gitSession{ID: firstString(row["workspace_id"]), Files: map[string]map[string]editSpan{}, Kept: map[string]map[string]lineSet{}}
		first, firstOK := parseTime(firstString(row["first_edit_at"]))
		last, lastOK := parseTime(firstString(row["last_edit_at"]))
		session.First, session.Last, session.Timed = first, last, firstOK && lastOK
		_ = json.Unmarshal([]byte(firstString(row["commits_json"])), &session.Printed)
		session.key = []string{firstString(row["first_edit_at"]), firstString(row["last_edit_at"]), firstString(row["commits_json"])}
		sessions[session.ID] = session
		if len(session.Printed) > 0 {
			add(firstString(row["repository_id"]), session.ID)
		}
	}
	files, err := queryMapsContext(ctx, c.DB, "SELECT workspace_id,repository_id,path,first_edit_at,last_edit_at,kept_lines FROM session_files WHERE repository_id IS NOT NULL ORDER BY workspace_id,repository_id,path")
	if err != nil {
		return nil, nil, err
	}
	for _, row := range files {
		session := sessions[firstString(row["workspace_id"])]
		if session == nil {
			continue
		}
		repository := firstString(row["repository_id"])
		if session.Files[repository] == nil {
			session.Files[repository] = map[string]editSpan{}
		}
		span := editSpan{session.First, session.Last}
		if first, ok := parseTime(firstString(row["first_edit_at"])); ok {
			span.First = first
		}
		if last, ok := parseTime(firstString(row["last_edit_at"])); ok {
			span.Last = last
		}
		session.Files[repository][firstString(row["path"])] = span
		// Fingerprints are bytes, any of which can read as a space.
		kept := bytesValue(row["kept_lines"])
		if len(kept) > 0 {
			if session.Kept[repository] == nil {
				session.Kept[repository] = map[string]lineSet{}
			}
			session.Kept[repository][firstString(row["path"])] = unpackLines(kept)
		}
		digest := sha256.Sum256(kept)
		session.key = append(session.key, repository+":"+firstString(row["path"])+":"+firstString(row["first_edit_at"])+":"+firstString(row["last_edit_at"])+":"+hex.EncodeToString(digest[:8]))
		add(repository, session.ID)
	}
	for _, members := range byRepository {
		slices.Sort(members)
	}
	sightings, err := queryMapsContext(ctx, c.DB, "SELECT workspace_id,location FROM workspace_sightings WHERE host_id=? AND COALESCE(location,'')<>'' ORDER BY workspace_id,location", currentHost().ID)
	if err != nil {
		return nil, nil, err
	}
	for _, row := range sightings {
		if session := sessions[firstString(row["workspace_id"])]; session != nil && !slices.Contains(session.Locations, firstString(row["location"])) {
			session.Locations = append(session.Locations, firstString(row["location"]))
			session.key = append(session.key, "at:"+firstString(row["location"]))
		}
	}
	return sessions, byRepository, nil
}

// gitLocations lists, for each repository, the checkouts this Mac recorded:
// the locations of its workspaces' sightings on this host. A path another
// Mac recorded may be an unrelated checkout here.
func (c *Catalog) gitLocations(ctx context.Context) (map[string][]string, error) {
	rows, err := queryMapsContext(ctx, c.DB, `SELECT DISTINCT COALESCE(w.repository_id,w.folder_repository_id) repository_id,s.location
		FROM workspace_sightings s JOIN workspaces w ON w.id=s.workspace_id
		WHERE s.host_id=? AND COALESCE(s.location,'')<>'' AND COALESCE(w.repository_id,w.folder_repository_id) IS NOT NULL
		ORDER BY s.last_seen_at DESC`, currentHost().ID)
	if err != nil {
		return nil, err
	}
	locations := map[string][]string{}
	for _, row := range rows {
		repository := firstString(row["repository_id"])
		locations[repository] = append(locations[repository], firstString(row["location"]))
	}
	return locations, nil
}

// gitCheckout is the first recorded checkout that still exists, or why none
// can be used.
func gitCheckout(locations []string) (string, string) {
	if len(locations) == 0 {
		return "", "No checkout of this repository was recorded on this Mac."
	}
	for _, location := range locations {
		if _, err := integrationCommonDir(location); err == nil {
			return location, ""
		}
	}
	return "", "The checkouts recorded on this Mac are gone."
}

func gitFailure(err error) string {
	if errors.Is(err, errGitTimeout) {
		return "Git took too long to read this repository."
	}
	return "Git could not read this repository."
}

// gitScanKey identifies what a scan would read: the repository's refs, its
// worktrees' detached HEADs and the sessions linked against it. An unchanged
// key needs no scan. It also gives the detached HEADs, which no ref holds.
func gitScanKey(ctx context.Context, dir string, sessions map[string]*gitSession, members []string, repository string) (string, []string, error) {
	refs, err := outcomeGitCommand(ctx, dir, outcomeGitShortTimeout, nil, "for-each-ref", "--format=%(objectname) %(refname)", "refs/heads", "refs/remotes", "refs/tags", "refs/notes/ai")
	if err != nil {
		return "", nil, err
	}
	worktrees, err := outcomeGitCommand(ctx, dir, outcomeGitShortTimeout, nil, "worktree", "list", "--porcelain")
	if err != nil {
		return "", nil, err
	}
	heads := detachedHeads(string(worktrees))
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\x00%s\x00%s\x00", outcomeGitVersion, outcomeGitGrace, repository)
	hash.Write(refs)
	fmt.Fprintf(hash, "\x00%s", strings.Join(heads, " "))
	for _, id := range members {
		fmt.Fprintf(hash, "\x00%s\x00%s", id, strings.Join(sessions[id].key, "\x01"))
	}
	return hex.EncodeToString(hash.Sum(nil)), heads, nil
}

// detachedHeads reads the commits worktrees have checked out without a
// branch, from git worktree list --porcelain.
func detachedHeads(worktrees string) []string {
	heads := []string{}
	for _, block := range strings.Split(worktrees, "\n\n") {
		head, detached := "", false
		for _, line := range strings.Split(block, "\n") {
			if value, ok := strings.CutPrefix(line, "HEAD "); ok && gitCommitID.MatchString(value) {
				head = value
			}
			detached = detached || line == "detached"
		}
		if detached && head != "" && !slices.Contains(heads, head) {
			heads = append(heads, head)
		}
	}
	slices.Sort(heads)
	return heads
}

// scanRepository reads a repository's commits since its sessions began and
// links them.
func scanRepository(ctx context.Context, dir string, sessions map[string]*gitSession, members []string, repository string, heads []string) gitScan {
	// From the first edit or printed commit of its sessions.
	since := time.Time{}
	earliest := func(at time.Time) {
		if since.IsZero() || at.Before(since) {
			since = at
		}
	}
	for _, id := range members {
		session := sessions[id]
		if session.Timed {
			earliest(session.First)
		}
		for _, printed := range session.Printed {
			if at, ok := parseTime(printed.At); ok {
				earliest(at)
			}
		}
	}
	if since.IsZero() {
		return gitScan{Status: "not_checked", Reason: "Its sessions have no edit or commit times to link commits by."}
	}
	// A day of slack for clocks and for commits printed before the first edit.
	after := "--since=" + strconv.FormatInt(since.Add(-24*time.Hour).Unix(), 10)
	commits, err := readGitCommits(ctx, dir, after, heads...)
	if err != nil {
		return gitScan{Status: "not_checked", Reason: gitFailure(err)}
	}
	scan := gitScan{Status: "checked", Commits: commits, Edited: map[string]bool{}}
	for _, id := range members {
		for path := range sessions[id].Files[repository] {
			scan.Edited[path] = true
		}
	}
	scan.Links, _ = inferLinks(ctx, dir, after, sessions, members, repository, commits)
	linked := map[string]*gitCommit{}
	for _, link := range scan.Links {
		linked[link.Hash] = commits[link.Hash]
	}
	if len(linked) == 0 {
		return scan
	}
	common, _ := integrationCommonDir(dir)
	shallow := integrationShallow(common)
	if err := markMainline(ctx, dir, after, linked, shallow); err != nil {
		return gitScan{Status: "not_checked", Reason: gitFailure(err)}
	}
	scan.Links = linkCopiesOnce(scan.Links, linked)
	linked = map[string]*gitCommit{}
	for _, link := range scan.Links {
		linked[link.Hash] = commits[link.Hash]
	}
	if err := markReverts(ctx, dir, after, commits, linked); err != nil {
		return gitScan{Status: "not_checked", Reason: gitFailure(err)}
	}
	if notes, err := outcomeGitCommand(ctx, dir, outcomeGitShortTimeout, nil, "notes", "--ref=ai", "list"); err == nil {
		for _, line := range strings.Split(string(notes), "\n") {
			if fields := strings.Fields(line); len(fields) == 2 && linked[fields[1]] != nil {
				linked[fields[1]].AINote = true
			}
		}
	}
	return scan
}

// linkCopiesOnce links each session once to a change it also linked as its
// copy on mainline: a cherry-pick or rebase keeps the author time, so the
// copy links to the session too. The link kept is the copy's, with the
// stronger evidence of the two, so a printed hash still counts when only
// inference linked the copy. Another session's link to the original stays.
func linkCopiesOnce(links []gitLink, linked map[string]*gitCommit) []gitLink {
	index := map[[2]string]int{}
	for at, link := range links {
		index[[2]string{link.Session, link.Hash}] = at
	}
	copied := map[int]bool{}
	for at, link := range links {
		copy, ok := index[[2]string{link.Session, linked[link.Hash].SameAs}]
		if !ok || copy == at {
			continue
		}
		copied[at] = true
		if link.Confidence > links[copy].Confidence {
			links[copy].Method, links[copy].Confidence, links[copy].Evidence = link.Method, link.Confidence, link.Evidence
		}
	}
	kept := []gitLink{}
	for at, link := range links {
		if !copied[at] {
			kept = append(kept, link)
		}
	}
	return kept
}

// readGitCommits reads every commit on a branch, remote-tracking branch or
// tag, or behind a detached HEAD, since a time, with the lines each changed
// per file.
func readGitCommits(ctx context.Context, dir, after string, heads ...string) (map[string]*gitCommit, error) {
	args := append(append([]string{"-c", "core.quotePath=false", "log", "--branches", "--remotes", "--tags"}, heads...),
		after, "--no-renames", "--numstat", "--format=%x1e%H%x1f%P%x1f%at%x1f%ct")
	output, err := outcomeGitCommand(ctx, dir, outcomeGitLogTimeout, nil, append(args, gitRawDiff...)...)
	if err != nil {
		return nil, err
	}
	commits := map[string]*gitCommit{}
	for _, record := range strings.Split(string(output), "\x1e") {
		header, body, _ := strings.Cut(record, "\n")
		fields := strings.Split(header, "\x1f")
		if len(fields) != 4 || !gitCommitID.MatchString(fields[0]) {
			continue
		}
		author, _ := strconv.ParseInt(fields[2], 10, 64)
		committed, _ := strconv.ParseInt(fields[3], 10, 64)
		commit := &gitCommit{Hash: fields[0], Parents: len(strings.Fields(fields[1])), Author: time.Unix(author, 0).UTC(), Commit: time.Unix(committed, 0).UTC(), Files: map[string][2]int64{}}
		for _, line := range strings.Split(body, "\n") {
			parts := strings.SplitN(line, "\t", 3)
			if len(parts) != 3 {
				continue
			}
			// Binary files count "-" lines.
			added, _ := strconv.ParseInt(parts[0], 10, 64)
			removed, _ := strconv.ParseInt(parts[1], 10, 64)
			path := parts[2]
			if unquoted, err := strconv.Unquote(path); err == nil && strings.HasPrefix(path, `"`) {
				path = unquoted
			}
			commit.Files[path] = [2]int64{added, removed}
			commit.Added += added
			commit.Removed += removed
		}
		commits[commit.Hash] = commit
	}
	return commits, nil
}

// inferLinks links a repository's commits, reading signals for the
// contested ones; it returns the links and the signals it read.
func inferLinks(ctx context.Context, dir, after string, sessions map[string]*gitSession, members []string, repository string, commits map[string]*gitCommit) ([]gitLink, *gitSignals) {
	links, contested := linkCommits(sessions, members, repository, commits, nil)
	if len(contested) == 0 {
		return links, nil
	}
	signals := readGitSignals(ctx, dir, sessions, contested)
	links, _ = linkCommits(sessions, members, repository, commits, signals)
	return links, signals
}

// linkCommits links a repository's commits to its sessions:
//   - a hash the session's own git commit printed links directly (1.0);
//   - any other commit, outside merges, links to a session whose edit window
//     (plus outcomeGitGrace) holds its author time and that edited one of its
//     files: 0.7 when it is the only such session. Time alone links nothing.
//
// A commit several sessions could have made is contested. With signals
// (outcome_git_signals.go), it goes to the one candidate the checkout that
// made it or the lines it adds point to, in that order, at that signal's
// confidence; else to the candidate editing its files nearest the commit,
// then with the tightest window, at 0.5. linkCommits
// returns the contested commits and their candidates, for reading signals.
func linkCommits(sessions map[string]*gitSession, members []string, repository string, commits map[string]*gitCommit, signals *gitSignals) ([]gitLink, map[string][]string) {
	links := []gitLink{}
	contested := map[string][]string{}
	direct := map[string]bool{}
	for _, id := range members {
		for _, printed := range sessions[id].Printed {
			hash := resolveAbbreviated(printed.Hash, commits)
			if hash == "" || direct[id+"\x00"+hash] {
				continue
			}
			direct[id+"\x00"+hash], direct[hash] = true, true
			evidence := map[string]any{"printed": printed.Hash}
			if printed.Branch != "" {
				evidence["branch"] = printed.Branch
			}
			links = append(links, gitLink{Session: id, Hash: hash, Method: "transcript", Confidence: 1, Evidence: evidence})
		}
	}
	hashes := make([]string, 0, len(commits))
	for hash := range commits {
		hashes = append(hashes, hash)
	}
	slices.Sort(hashes)
	for _, hash := range hashes {
		commit := commits[hash]
		if direct[hash] || commit.Parents > 1 {
			continue
		}
		candidates := commitCandidates(sessions, members, repository, commit)
		if len(candidates) == 0 {
			continue
		}
		chosen, confidence, by := candidates[0], 0.7, ""
		if len(candidates) > 1 {
			ids := make([]string, len(candidates))
			for index, candidate := range candidates {
				ids[index] = candidate.id
			}
			contested[hash] = ids
			checkout, lines := signals.picks(hash, repository, sessions, ids)
			for index, pick := range []string{checkout, lines} {
				if pick != "" {
					chosen = candidates[slices.Index(ids, pick)]
					confidence, by = linkTieBreaks[index].confidence, linkTieBreaks[index].name
					break
				}
			}
			if by == "" {
				if linkOrder(candidates[0], candidates[1]) == 0 {
					continue
				}
				confidence, by = 0.5, "nearest"
			}
		}
		evidence := map[string]any{"author_at": commit.Author.Format(time.RFC3339), "shared_files": len(chosen.shared), "files": chosen.shared[:min(len(chosen.shared), 5)],
			"window": []string{sessions[chosen.id].First.Format(time.RFC3339), sessions[chosen.id].Last.Format(time.RFC3339)}, "candidates": len(candidates)}
		if by != "" {
			evidence["chosen_by"] = by
		}
		links = append(links, gitLink{Session: chosen.id, Hash: hash, Method: "inferred", Confidence: confidence, Evidence: evidence})
	}
	return links, contested
}

// linkCandidate is a session that could have made a commit: how long since
// it last edited one of the commit's files (zero while it still was), its
// edit window's span, and the files they share.
type linkCandidate struct {
	id        string
	gap, span time.Duration
	shared    []string
}

// linkOrder puts first the session still editing the commit's files when it
// was made, or that stopped nearest before it, then the tightest window: a
// session that edited once an hour before has the tightest window but did
// not make the commit.
func linkOrder(a, b linkCandidate) int {
	if a.gap != b.gap {
		return cmp.Compare(a.gap, b.gap)
	}
	return cmp.Compare(a.span, b.span)
}

// commitCandidates lists the sessions that could have made a commit, in
// linkOrder.
func commitCandidates(sessions map[string]*gitSession, members []string, repository string, commit *gitCommit) []linkCandidate {
	candidates := []linkCandidate{}
	for _, id := range members {
		session := sessions[id]
		if !session.Timed || commit.Author.Before(session.First) || commit.Author.After(session.Last.Add(outcomeGitGrace)) {
			continue
		}
		// A shared file is one the session had edited by the commit.
		shared, gap := []string{}, time.Duration(-1)
		for path := range commit.Files {
			if span, ok := session.Files[repository][path]; ok && !commit.Author.Before(span.First) {
				shared = append(shared, path)
				if since := max(commit.Author.Sub(span.Last), 0); gap < 0 || since < gap {
					gap = since
				}
			}
		}
		if len(shared) > 0 {
			slices.Sort(shared)
			candidates = append(candidates, linkCandidate{id, gap, session.Last.Sub(session.First), shared})
		}
	}
	slices.SortStableFunc(candidates, linkOrder)
	return candidates
}

// resolveAbbreviated finds the one scanned commit an abbreviated hash names.
func resolveAbbreviated(abbreviated string, commits map[string]*gitCommit) string {
	abbreviated = strings.ToLower(abbreviated)
	if commits[abbreviated] != nil {
		return abbreviated
	}
	found := ""
	for hash := range commits {
		if strings.HasPrefix(hash, abbreviated) {
			if found != "" {
				return ""
			}
			found = hash
		}
	}
	return found
}

// mainlineTip is the commit mainline points at: the remote-tracking default
// branch, as git_integration.go reads it, else a local main or master for a
// repository without a remote. Nothing is fetched.
func mainlineTip(ctx context.Context, dir string) (tip, ref string, err error) {
	refs, err := outcomeGitCommand(ctx, dir, outcomeGitShortTimeout, nil, "for-each-ref", "--format=%(refname) %(objectname)",
		"refs/remotes/origin/HEAD", "refs/remotes/origin/main", "refs/remotes/origin/master", "refs/heads/main", "refs/heads/master")
	if err != nil {
		return "", "", err
	}
	tips := map[string]string{}
	for _, line := range strings.Split(string(refs), "\n") {
		if fields := strings.Fields(line); len(fields) == 2 {
			tips[fields[0]] = fields[1]
		}
	}
	for _, ref := range []string{"refs/remotes/origin/HEAD", "refs/remotes/origin/main", "refs/remotes/origin/master", "refs/heads/main", "refs/heads/master"} {
		if tip := tips[ref]; gitCommitID.MatchString(tip) {
			return tip, ref, nil
		}
	}
	return "", "", nil
}

// markMainline records whether each linked commit reached mainline: it is
// in mainline's history, or a commit there has the same change (a rebase or
// cherry-pick, by patch-id). A squash of several commits is not recognized.
// Without a mainline, or in a shallow clone that lacks it, it stays unknown.
func markMainline(ctx context.Context, dir, after string, linked map[string]*gitCommit, shallow bool) error {
	tip, ref, err := mainlineTip(ctx, dir)
	if err != nil || tip == "" {
		return err
	}
	history, err := outcomeGitCommand(ctx, dir, outcomeGitLogTimeout, nil, "rev-list", after, tip)
	if err != nil {
		return err
	}
	on := map[string]bool{}
	for _, hash := range strings.Fields(string(history)) {
		on[hash] = true
	}
	missing := []string{}
	for hash, commit := range linked {
		if on[hash] {
			commit.OnMainline, commit.MainlineEvidence = new(true), "reachable from "+strings.TrimPrefix(ref, "refs/")
		} else {
			missing = append(missing, hash)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	slices.Sort(missing)
	// Comparing changes is a further check: when it cannot run, the rest
	// stay unknown rather than off mainline.
	mine, err := patchIDs(ctx, dir, false, append([]string{"--no-walk=unsorted"}, missing...)...)
	if err != nil {
		return nil
	}
	theirs, err := patchIDs(ctx, dir, false, "--no-merges", after, tip)
	if err != nil {
		return nil
	}
	byPatch := map[string]string{}
	for hash, id := range theirs {
		byPatch[id] = hash
	}
	for _, hash := range missing {
		commit := linked[hash]
		if match := byPatch[mine[hash]]; mine[hash] != "" && match != "" {
			commit.OnMainline, commit.MainlineEvidence, commit.SameAs = new(true), "same change as "+match[:12]+" on "+strings.TrimPrefix(ref, "refs/"), match
		} else if !shallow {
			commit.OnMainline = new(false)
		}
	}
	return nil
}

// patchIDs gives each commit's patch-id, of its change or of its inverse.
func patchIDs(ctx context.Context, dir string, reverse bool, revisions ...string) (map[string]string, error) {
	// Without a/ and b/ prefixes, which an inverse diff swaps.
	args := append([]string{"log", "-p", "--no-prefix", "--format=commit %H"}, gitRawDiff...)
	if reverse {
		args = append(args, "-R")
	}
	patches, err := outcomeGitCommand(ctx, dir, outcomeGitLogTimeout, nil, append(args, revisions...)...)
	if err != nil {
		return nil, err
	}
	output, err := outcomeGitCommand(ctx, dir, outcomeGitLogTimeout, patches, "patch-id", "--stable")
	if err != nil {
		return nil, err
	}
	ids := map[string]string{}
	for _, line := range strings.Split(string(output), "\n") {
		if fields := strings.Fields(line); len(fields) == 2 {
			ids[fields[1]] = fields[0]
		}
	}
	return ids, nil
}

var revertLine = regexp.MustCompile(`(?m)^This reverts commit ([0-9a-f]{7,64})`)

// markReverts records which linked commits a later commit reverted: by the
// message git revert writes, or by a diff that is the exact inverse of the
// commit's (same files, lines added and removed swapped, and the inverse
// patch-id equal to the commit's).
func markReverts(ctx context.Context, dir, after string, commits, linked map[string]*gitCommit) error {
	messages, err := outcomeGitCommand(ctx, dir, outcomeGitLogTimeout, nil, "log", "--branches", "--remotes", "--tags", after,
		"--grep=This reverts commit", "--format=%x1e%H%x1f%B")
	if err != nil {
		return err
	}
	for _, record := range strings.Split(string(messages), "\x1e") {
		hash, body, ok := strings.Cut(record, "\x1f")
		if !ok {
			continue
		}
		for _, match := range revertLine.FindAllStringSubmatch(body, -1) {
			if target := resolveAbbreviated(match[1], linked); target != "" && linked[target].RevertedBy == "" {
				linked[target].RevertedBy, linked[target].RevertEvidence = strings.TrimSpace(hash), "message"
			}
		}
	}
	candidates := map[string][]string{}
	reverters := []string{}
	for hash, commit := range linked {
		if commit.RevertedBy != "" || len(commit.Files) == 0 {
			continue
		}
		for other, later := range commits {
			if other == hash || later.Parents > 1 || later.Commit.Before(commit.Commit) || len(later.Files) != len(commit.Files) {
				continue
			}
			inverse := true
			for path, lines := range commit.Files {
				if counts, ok := later.Files[path]; !ok || counts != [2]int64{lines[1], lines[0]} {
					inverse = false
					break
				}
			}
			if inverse {
				candidates[hash] = append(candidates[hash], other)
				if !slices.Contains(reverters, other) {
					reverters = append(reverters, other)
				}
			}
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	targets := make([]string, 0, len(candidates))
	for hash := range candidates {
		targets = append(targets, hash)
	}
	slices.Sort(targets)
	slices.Sort(reverters)
	// As for mainline, a comparison that cannot run finds nothing.
	forward, err := patchIDs(ctx, dir, false, append([]string{"--no-walk=unsorted"}, targets...)...)
	if err != nil {
		return nil
	}
	backward, err := patchIDs(ctx, dir, true, append([]string{"--no-walk=unsorted"}, reverters...)...)
	if err != nil {
		return nil
	}
	for _, hash := range targets {
		for _, other := range candidates[hash] {
			if forward[hash] != "" && forward[hash] == backward[other] {
				linked[hash].RevertedBy, linked[hash].RevertEvidence = other, "inverse_diff"
				break
			}
		}
	}
	return nil
}

// writeGitScan stores one repository's scan: its links and their commits,
// or, when it could not run, why. A failed scan keeps earlier links: failing
// to look is not evidence.
func (c *Catalog) writeGitScan(ctx context.Context, repository, host, key string, scan gitScan) error {
	return c.writeTransaction(ctx, "session-git", func(tx *sql.Tx) error {
		if scan.Status == "checked" {
			if _, err := tx.ExecContext(ctx, "DELETE FROM session_commit_links WHERE repository_id=?", repository); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM git_commits WHERE repository_id=?", repository); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM git_commit_files WHERE repository_id=?", repository); err != nil {
				return err
			}
			written := map[string]bool{}
			writeCommit := func(commit *gitCommit) error {
				var mainline any
				if commit.OnMainline != nil {
					mainline = boolInt(*commit.OnMainline)
				}
				_, err := tx.ExecContext(ctx, `INSERT INTO git_commits(repository_id,hash,author_at,committed_at,parents,files,lines_added,lines_removed,on_mainline,mainline_evidence,reverted_by,revert_evidence,ai_note)
					VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, repository, commit.Hash, commit.Author.Format(time.RFC3339), commit.Commit.Format(time.RFC3339), commit.Parents, len(commit.Files),
					commit.Added, commit.Removed, mainline, nilIfEmpty(commit.MainlineEvidence), nilIfEmpty(commit.RevertedBy), nilIfEmpty(commit.RevertEvidence), boolInt(commit.AINote))
				return err
			}
			for _, link := range scan.Links {
				if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO session_commit_links(workspace_id,repository_id,hash,method,confidence,evidence_json) VALUES(?,?,?,?,?,?)`,
					link.Session, repository, link.Hash, link.Method, link.Confidence, jsonText(link.Evidence)); err != nil {
					return err
				}
				if written[link.Hash] {
					continue
				}
				written[link.Hash] = true
				if err := writeCommit(scan.Commits[link.Hash]); err != nil {
					return err
				}
			}
			// Commits no session is linked to are kept too where they changed
			// an edited file, without a mainline answer: only linked commits
			// are asked about.
			for hash, commit := range scan.Commits {
				touched := false
				for path, lines := range commit.Files {
					if !scan.Edited[path] {
						continue
					}
					touched = true
					if _, err := tx.ExecContext(ctx, `INSERT INTO git_commit_files(repository_id,hash,path,lines_added,lines_removed) VALUES(?,?,?,?,?)`,
						repository, hash, path, lines[0], lines[1]); err != nil {
						return err
					}
				}
				if touched && !written[hash] {
					written[hash] = true
					if err := writeCommit(commit); err != nil {
						return err
					}
				}
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO git_repository_scans(repository_id,host_id,scan_key,status,reason,scanned_at) VALUES(?,?,?,?,?,?)
			ON CONFLICT(repository_id,host_id) DO UPDATE SET scan_key=excluded.scan_key,status=excluded.status,reason=excluded.reason,scanned_at=excluded.scanned_at`,
			repository, host, key, scan.Status, nilIfEmpty(scan.Reason), now())
		return err
	})
}

// writeSessionGit records each session's Tier 2 state from its
// repositories' scans: checked when every one was, else not checked with the
// first reason, and not applicable for a session that changed no file in a
// repository; and its linked, landed and reverted commits, counting only
// links of at least outcomeGitCounted. Only the sessions
// whose state changed are written, so a pass that found nothing new leaves
// the cached views alone.
func (c *Catalog) writeSessionGit(ctx context.Context, sessions map[string]*gitSession, byRepository map[string][]string) error {
	return c.writeTransaction(ctx, "session-git-state", func(tx *sql.Tx) error {
		scans, err := queryMapsContext(ctx, tx, "SELECT repository_id,status,reason FROM git_repository_scans WHERE host_id=?", currentHost().ID)
		if err != nil {
			return err
		}
		status := map[string][2]string{}
		for _, row := range scans {
			status[firstString(row["repository_id"])] = [2]string{firstString(row["status"]), firstString(row["reason"])}
		}
		repositories := make([]string, 0, len(byRepository))
		for repository := range byRepository {
			repositories = append(repositories, repository)
		}
		slices.Sort(repositories)
		state := map[string][2]string{}
		for id := range sessions {
			state[id] = [2]string{"not_applicable", ""}
		}
		for _, repository := range repositories {
			for _, id := range byRepository[repository] {
				scan := status[repository]
				if scan[0] == "" {
					scan = [2]string{"not_checked", "Not checked yet."}
				}
				if current := state[id]; current[0] == "not_applicable" || current[0] == "checked" && scan[0] != "checked" {
					state[id] = scan
				}
			}
		}
		// Links of sessions that no longer edit in a repository go first.
		linkedSessions, err := queryMapsContext(ctx, tx, "SELECT DISTINCT workspace_id FROM session_commit_links")
		if err != nil {
			return err
		}
		for _, row := range linkedSessions {
			if id := firstString(row["workspace_id"]); state[id][0] == "" || state[id][0] == "not_applicable" {
				if _, err := tx.ExecContext(ctx, "DELETE FROM session_commit_links WHERE workspace_id=?", id); err != nil {
					return err
				}
			}
		}
		counts, err := queryMapsContext(ctx, tx, `SELECT l.workspace_id,COUNT(*) linked,COALESCE(SUM(g.on_mainline=1),0) landed,
			COALESCE(SUM(g.reverted_by IS NOT NULL),0) reverted,COALESCE(SUM(g.on_mainline IS NULL),0) unknown
			FROM session_commit_links l JOIN git_commits g ON g.repository_id=l.repository_id AND g.hash=l.hash WHERE l.confidence>=? GROUP BY l.workspace_id`, outcomeGitCounted)
		if err != nil {
			return err
		}
		tally := map[string]string{}
		for _, row := range counts {
			tally[firstString(row["workspace_id"])] = fmt.Sprint(row["linked"], "|", row["landed"], "|", row["reverted"], "|", row["unknown"])
		}
		stored, err := queryMapsContext(ctx, tx, "SELECT workspace_id,status,COALESCE(reason,'') reason,linked_commits,landed_commits,reverted_commits,mainline_unknown FROM session_git")
		if err != nil {
			return err
		}
		for _, row := range stored {
			id := firstString(row["workspace_id"])
			want, ok := state[id]
			if !ok {
				if _, err := tx.ExecContext(ctx, "DELETE FROM session_git WHERE workspace_id=?", id); err != nil {
					return err
				}
				continue
			}
			counted := tally[id]
			if counted == "" {
				counted = "0|0|0|0"
			}
			if want == [2]string{firstString(row["status"]), firstString(row["reason"])} &&
				counted == fmt.Sprint(row["linked_commits"], "|", row["landed_commits"], "|", row["reverted_commits"], "|", row["mainline_unknown"]) {
				delete(state, id)
			}
		}
		ids := make([]string, 0, len(state))
		for id := range state {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO session_git(workspace_id,status,reason,linked_commits,landed_commits,reverted_commits,mainline_unknown,checked_at)
				SELECT ?,?,?,COUNT(*),COALESCE(SUM(g.on_mainline=1),0),COALESCE(SUM(g.reverted_by IS NOT NULL),0),COALESCE(SUM(g.on_mainline IS NULL),0),?
				FROM session_commit_links l JOIN git_commits g ON g.repository_id=l.repository_id AND g.hash=l.hash WHERE l.workspace_id=? AND l.confidence>=?`,
				id, state[id][0], nilIfEmpty(state[id][1]), now(), id, outcomeGitCounted); err != nil {
				return err
			}
		}
		return nil
	})
}
