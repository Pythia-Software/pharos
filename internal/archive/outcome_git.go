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
	"maps"
	"os"
	"os/exec"
	"path/filepath"
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
// A library drive moves between Macs, each with its own clones. Each link
// records the Mac whose scan made it, and a scan replaces only the links it
// can decide: those to commits it read, and its own Mac's earlier ones.
// Another Mac's link to a commit this one cannot see stays, and a repository
// is checked when any Mac checked it.
//
// Only facts are stored: hashes, times, counts, the files a link shares and
// how a commit was reverted. No commit message, diff or file content: a
// subject is compared as a fingerprint, and squash messages in memory. For
// file history, every commit that changed a file some session edited or
// committed keeps that file's path and line counts (git_commit_files).

// outcomeGitVersion scans every repository again when the rules change.
const outcomeGitVersion = "4"

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
  host_id TEXT NOT NULL DEFAULT '',
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
  checked_at TEXT NOT NULL,
  lines_added INTEGER NOT NULL DEFAULT 0,
  lines_landed INTEGER NOT NULL DEFAULT 0) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS git_commit_files(
  repository_id TEXT NOT NULL,
  hash TEXT NOT NULL,
  path TEXT NOT NULL,
  lines_added INTEGER NOT NULL,
  lines_removed INTEGER NOT NULL,
  PRIMARY KEY(repository_id,path,hash)) WITHOUT ROWID;
-- File history finds a file by its path (findFileHistoryKeys).
CREATE INDEX IF NOT EXISTS session_files_path_idx ON session_files(path);
CREATE TABLE IF NOT EXISTS git_repository_scans(
  repository_id TEXT NOT NULL,
  host_id TEXT NOT NULL,
  scan_key TEXT NOT NULL,
  status TEXT NOT NULL,
  reason TEXT,
  scanned_at TEXT NOT NULL,
  history_since TEXT,
  PRIMARY KEY(repository_id,host_id)) WITHOUT ROWID;
`

func (c *Catalog) ensureOutcomeGit() error {
	if _, err := c.DB.Exec(outcomeGitSQL); err != nil {
		return err
	}
	for _, column := range []string{"lines_added", "lines_landed"} {
		if has, err := c.hasColumn("session_git", column); err != nil {
			return err
		} else if !has {
			if _, err := c.DB.Exec("ALTER TABLE session_git ADD COLUMN " + column + " INTEGER NOT NULL DEFAULT 0"); err != nil {
				return err
			}
		}
	}
	var hosts int
	if err := c.DB.QueryRow("SELECT COUNT(*) FROM pragma_table_info('session_commit_links') WHERE name='host_id'").Scan(&hosts); err != nil || hosts > 0 {
		return err
	}
	_, err := c.DB.Exec("ALTER TABLE session_commit_links ADD COLUMN host_id TEXT NOT NULL DEFAULT ''")
	return err
}

// editSpan is when a session first and last edited a file.
type editSpan struct{ First, Last time.Time }

// gitSession is a session as linking reads it: its edit window, the files
// it edited in each repository and the lines it kept in them, the hashes its
// own git commit printed, the git commands it ran (outcome_git_ops.go), its
// own repository and its checkouts on this Mac.
type gitSession struct {
	ID          string
	First, Last time.Time
	Timed       bool
	Files       map[string]map[string]editSpan
	Kept        map[string]map[string]lineSet
	Printed     []outcomeCommit
	Ops         []gitOp
	Repository  string
	Locations   []string
	key         []string
}

// gitCommit is one commit of a scan. Subject is a fingerprint of its
// subject (subjectFingerprint), compared in memory and never stored.
type gitCommit struct {
	Hash          string
	Parents       int
	ParentHashes  []string
	Tree          string
	AuthorEmail   string
	Subject       string
	SubjectLength int
	// Replaced is a commit only a reflog holds.
	Replaced         bool
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
	// Edited are the repository's files some session edited or committed:
	// the commits that changed them are kept for file history.
	Edited map[string]bool
	// Since is how far back the scan read: commits before it are not
	// stored, so file history says it covers only the time since.
	Since time.Time
}

var errGitTimeout = errors.New("git timed out")

// gitRawDiff reads a diff as stored, running no program the repository names.
var gitRawDiff = []string{"--no-textconv", "--no-ext-diff"}

// outcomeGitCommand runs git read-only in dir: no lazy fetch from a promisor
// remote, no optional locks, no prompts, at low priority when nice is there.
// Every diff it reads passes gitRawDiff, since a repository's attributes can
// name a text converter or diff program, which would run (and a converter
// with a cache writes notes refs). Nor does a log verify signatures: with
// log.showSignature set, it would run the repository's gpg.program (or ssh
// signing program) for every signed commit.
var outcomeGitCommand = func(ctx context.Context, dir string, timeout time.Duration, stdin []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	name, full := "git", append([]string{"-C", dir, "-c", "log.showSignature=false"}, args...)
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
	identities, err := c.gitIdentities(ctx)
	if err != nil {
		return err
	}
	remotes, err := queryMapsContext(ctx, c.DB, "SELECT id,normalized_remote FROM repositories WHERE COALESCE(normalized_remote,'')<>''")
	if err != nil {
		return err
	}
	forges := map[string]string{}
	for _, row := range remotes {
		forges[firstString(row["id"])] = firstString(row["normalized_remote"])
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
		var clones []*gitClone
		reason := ""
		if gitErr == nil {
			clones, reason = gitCheckouts(ctx, locations[repository], identities[repository])
		}
		// A scan that could not start is keyed by its reason; one that
		// started and failed, by what it read and the day, so that a timeout
		// is tried again when the repository changes or the next day.
		key, failedKey := "", ""
		switch {
		case gitErr != nil:
			scan.Reason = "Git is not installed on this Mac."
		case len(clones) == 0:
			scan.Reason = reason
		default:
			key, err = gitScanKey(ctx, clones, sessions, members, repository, forges[repository])
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
			scan = scanRepository(ctx, clones, sessions, members, repository, forges[repository])
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
// edited files in a repository, printed a commit or ran a git command that
// makes one, outside mirrors.
func (c *Catalog) readGitSessions(ctx context.Context) (map[string]*gitSession, map[string][]string, error) {
	rows, err := queryMapsContext(ctx, c.DB, `SELECT so.workspace_id,so.first_edit_at,so.last_edit_at,so.commits_json,so.git_ops_json,COALESCE(w.repository_id,w.folder_repository_id) repository_id
		FROM session_outcomes so JOIN workspaces w ON w.id=so.workspace_id
		WHERE so.workspace_id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces) AND (so.files_changed>0 OR so.commits_json<>'[]' OR so.git_ops_json<>'[]')`)
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
		_ = json.Unmarshal([]byte(firstString(row["git_ops_json"])), &session.Ops)
		session.Repository = firstString(row["repository_id"])
		session.key = []string{firstString(row["first_edit_at"]), firstString(row["last_edit_at"]), firstString(row["commits_json"]), firstString(row["git_ops_json"])}
		sessions[session.ID] = session
		if len(session.Printed) > 0 {
			add(session.Repository, session.ID)
		}
		// A git command counts in the repository it named, else the session's.
		for _, op := range session.Ops {
			add(defaultString(op.Repository, session.Repository), session.ID)
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

// gitIdentity is what names a repository: its first root commit and its
// normalized remotes.
type gitIdentity struct {
	Root    string
	Remotes []string
}

// gitIdentities reads each repository's identity.
func (c *Catalog) gitIdentities(ctx context.Context) (map[string]gitIdentity, error) {
	rows, err := queryMapsContext(ctx, c.DB, "SELECT id,root_commit,normalized_remote,aliases_json FROM repositories")
	if err != nil {
		return nil, err
	}
	identities := map[string]gitIdentity{}
	for _, row := range rows {
		identity := gitIdentity{Root: strings.ToLower(firstString(row["root_commit"]))}
		var aliases []string
		_ = json.Unmarshal([]byte(firstString(row["aliases_json"])), &aliases)
		for _, remote := range append([]string{firstString(row["normalized_remote"])}, aliases...) {
			if remote = normalizeRepositoryRemote(remote); remote != "" && !slices.Contains(identity.Remotes, remote) {
				identity.Remotes = append(identity.Remotes, remote)
			}
		}
		identities[firstString(row["id"])] = identity
	}
	return identities, nil
}

// holds reports whether the checkout at location is of the repository: it
// has the repository's root commit, or its origin is one of its remotes (a
// shallow clone can lack the root). With neither known, the location must be
// the checkout itself: finding .git in a folder above it, such as a home
// folder kept in Git, finds some other repository.
func (identity gitIdentity) holds(ctx context.Context, location string) bool {
	if identity.Root != "" && gitCommitID.MatchString(identity.Root) {
		if _, err := outcomeGitCommand(ctx, location, outcomeGitShortTimeout, nil, "cat-file", "-e", identity.Root+"^{commit}"); err == nil {
			return true
		}
	}
	if len(identity.Remotes) > 0 {
		origin, err := outcomeGitCommand(ctx, location, outcomeGitShortTimeout, nil, "remote", "get-url", "origin")
		return err == nil && slices.Contains(identity.Remotes, normalizeRepositoryRemote(strings.TrimSpace(string(origin))))
	}
	if identity.Root != "" {
		return false
	}
	_, err := os.Stat(filepath.Join(location, ".git"))
	return err == nil
}

// gitClone is one clone of a repository a scan reads: a recorded checkout
// of it, whether it is shallow, its worktrees' detached HEADs and the
// commits read from it. Clones of one repository (a checkout in a home
// folder and a Conductor clone) share its identity but not their branches.
type gitClone struct {
	Dir     string
	Common  string
	Shallow bool
	Heads   []string
	Has     map[string]bool
}

// gitCheckouts is one recorded checkout of the repository that still exists
// for each of its clones on this Mac (each Git common directory), most
// recently seen first, or why there is none. A deleted worktree's directory
// can keep a .git that holds no repository (only info/exclude, say): it is
// gone too.
func gitCheckouts(ctx context.Context, locations []string, identity gitIdentity) ([]*gitClone, string) {
	if len(locations) == 0 {
		return nil, "No checkout of this repository was recorded on this Mac."
	}
	clones, seen, other := []*gitClone{}, map[string]bool{}, false
	for _, location := range locations {
		common, err := integrationCommonDir(location)
		if err != nil || seen[common] {
			continue
		}
		if _, err := os.Stat(filepath.Join(common, "HEAD")); err != nil {
			continue
		}
		if !identity.holds(ctx, location) {
			other = true
			continue
		}
		seen[common] = true
		clones = append(clones, &gitClone{Dir: location, Common: common, Shallow: integrationShallow(common)})
	}
	switch {
	case len(clones) > 0:
		return clones, ""
	case other:
		return nil, "The checkouts recorded on this Mac hold another repository."
	}
	return nil, "The checkouts recorded on this Mac are gone."
}

func gitFailure(err error) string {
	if errors.Is(err, errGitTimeout) {
		return "Git took too long to read this repository."
	}
	return "Git could not read this repository."
}

// gitScanKey identifies what a scan would read: each clone's refs and its
// worktrees' detached HEADs, the repository's remote, and the sessions
// linked against it. An unchanged key needs no scan. It also records each
// clone's detached HEADs, which no ref holds.
func gitScanKey(ctx context.Context, clones []*gitClone, sessions map[string]*gitSession, members []string, repository, forge string) (string, error) {
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%s", outcomeGitVersion, outcomeGitGrace, repository, forge)
	for _, clone := range clones {
		refs, err := outcomeGitCommand(ctx, clone.Dir, outcomeGitShortTimeout, nil, "for-each-ref", "--format=%(objectname) %(refname)", "refs/heads", "refs/remotes", "refs/tags", "refs/notes/ai")
		if err != nil {
			return "", err
		}
		worktrees, err := outcomeGitCommand(ctx, clone.Dir, outcomeGitShortTimeout, nil, "worktree", "list", "--porcelain")
		if err != nil {
			return "", err
		}
		clone.Heads = detachedHeads(string(worktrees))
		fmt.Fprintf(hash, "\x00%s\x00", clone.Common)
		hash.Write(refs)
		fmt.Fprintf(hash, "\x00%s", strings.Join(clone.Heads, " "))
	}
	for _, id := range members {
		fmt.Fprintf(hash, "\x00%s\x00%s", id, strings.Join(sessions[id].key, "\x01"))
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
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

// scanRepository reads a repository's commits since its sessions began, in
// each of its clones, and links them. A commit is the same in every clone
// that has it; each clone answers for the commits it has.
func scanRepository(ctx context.Context, clones []*gitClone, sessions map[string]*gitSession, members []string, repository, forge string) gitScan {
	// From the first edit, git command or printed commit of its sessions.
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
		for _, op := range session.opsIn(repository) {
			if from, _, ok := op.window(); ok {
				earliest(from)
			}
		}
		for _, printed := range session.Printed {
			if at, ok := parseTime(printed.At); ok {
				earliest(at)
			}
		}
	}
	if since.IsZero() {
		return gitScan{Status: "not_checked", Reason: "Its sessions have no edit or git command times to link commits by."}
	}
	// A day of slack for clocks and for commits printed before the first edit.
	after := "--since=" + strconv.FormatInt(since.Add(-24*time.Hour).Unix(), 10)
	commits := map[string]*gitCommit{}
	for _, clone := range clones {
		read, err := readGitCommits(ctx, clone.Dir, after, clone.Heads...)
		if err != nil {
			return gitScan{Status: "not_checked", Reason: gitFailure(err)}
		}
		clone.Has = map[string]bool{}
		for hash, commit := range read {
			clone.Has[hash] = true
			if commits[hash] == nil {
				commits[hash] = commit
			} else if !commit.Replaced {
				commits[hash].Replaced = false
			}
		}
	}
	scan := gitScan{Status: "checked", Commits: commits, Edited: map[string]bool{}, Since: since.Add(-24 * time.Hour)}
	for _, id := range members {
		for path := range sessions[id].Files[repository] {
			scan.Edited[path] = true
		}
	}
	scan.Links, _ = inferLinks(ctx, clones, sessions, members, repository, commits)
	linked := map[string]*gitCommit{}
	for _, link := range scan.Links {
		linked[link.Hash] = commits[link.Hash]
	}
	if len(linked) > 0 {
		pulls := pullRequests(scan.Links, sessions, forge)
		for _, clone := range clones {
			if err := markMainline(ctx, clone.Dir, after, clone.holding(commits), clone.holding(linked), pulls, clone.Shallow); err != nil {
				return gitScan{Status: "not_checked", Reason: gitFailure(err)}
			}
		}
		scan.Links = linkOnce(scan.Links, commits)
		linked = map[string]*gitCommit{}
		for _, link := range scan.Links {
			linked[link.Hash] = commits[link.Hash]
			// What a session committed has a file history, whatever tool wrote it.
			for path := range commits[link.Hash].Files {
				scan.Edited[path] = true
			}
		}
	}
	// Every commit stored is asked whether it was reverted: the linked ones
	// and, for file history, those that changed an edited file, so a stored
	// commit without reverted_by was checked and not found reverted. Only
	// linked commits are asked about mainline. A replaced commit no session
	// is linked to is a copy of another, and not stored.
	stored := maps.Clone(linked)
	for hash, commit := range commits {
		if commit.Replaced {
			continue
		}
		for path := range commit.Files {
			if scan.Edited[path] {
				stored[hash] = commit
				break
			}
		}
	}
	if len(stored) == 0 {
		return scan
	}
	for _, clone := range clones {
		if err := markReverts(ctx, clone.Dir, after, clone.holding(commits), clone.holding(stored)); err != nil {
			return gitScan{Status: "not_checked", Reason: gitFailure(err)}
		}
		if len(linked) == 0 {
			continue
		}
		if notes, err := outcomeGitCommand(ctx, clone.Dir, outcomeGitShortTimeout, nil, "notes", "--ref=ai", "list"); err == nil {
			for _, line := range strings.Split(string(notes), "\n") {
				if fields := strings.Fields(line); len(fields) == 2 && linked[fields[1]] != nil && clone.Has[fields[1]] {
					linked[fields[1]].AINote = true
				}
			}
		}
	}
	return scan
}

// holding is those of commits the clone has.
func (clone *gitClone) holding(commits map[string]*gitCommit) map[string]*gitCommit {
	held := map[string]*gitCommit{}
	for hash, commit := range commits {
		if clone.Has[hash] {
			held[hash] = commit
		}
	}
	return held
}

// stashSubject is the subject git stash gives the commits it makes, which
// are not work a session committed: the stash itself, a merge, and the
// commits of its index (one parent) and untracked files (none).
var stashSubject = regexp.MustCompile(`^(?:(WIP on|On) [^:\n]+: |(index|untracked files) on [^:\n]+: [0-9a-f]{7,} )`)

func stashCommit(subject string, parents int) bool {
	match := stashSubject.FindStringSubmatch(subject)
	switch {
	case match == nil:
		return false
	case match[2] == "index":
		return parents == 1
	case match[2] == "untracked files":
		return parents == 0
	default:
		return parents >= 2
	}
}

// readGitCommits reads every commit on a branch, remote-tracking branch or
// tag, or behind a detached HEAD, since a time, with the lines each changed
// per file; then the commits only the reflogs still hold, as replaced: those
// a rebase, amend or reset replaced, which is where a hash a session printed
// may now be. Git stash's commits are left out. Reflogs that cannot be read
// in time leave out only theirs.
func readGitCommits(ctx context.Context, dir, after string, heads ...string) (map[string]*gitCommit, error) {
	commits := map[string]*gitCommit{}
	for _, replaced := range []bool{false, true} {
		args := append([]string{"-c", "core.quotePath=false", "log", "--branches", "--remotes", "--tags"}, heads...)
		if replaced {
			args = append([]string{"-c", "core.quotePath=false", "log", "--reflog", "--not", "--branches", "--remotes", "--tags"}, heads...)
		}
		output, err := outcomeGitCommand(ctx, dir, outcomeGitLogTimeout, nil, append(append(args, after, "--no-renames", "--numstat",
			"--format=%x1e%H%x1f%P%x1f%at%x1f%ct%x1f%T%x1f%ae%x1f%s"), gitRawDiff...)...)
		if err != nil && (!replaced || ctx.Err() != nil) {
			return nil, err
		} else if err != nil {
			output = nil
		}
		for hash, commit := range parseGitCommits(string(output)) {
			if commits[hash] == nil {
				commit.Replaced = replaced
				commits[hash] = commit
			}
		}
	}
	return commits, nil
}

// parseGitCommits reads readGitCommits's log.
func parseGitCommits(output string) map[string]*gitCommit {
	commits := map[string]*gitCommit{}
	for _, record := range strings.Split(output, "\x1e") {
		header, body, _ := strings.Cut(record, "\n")
		fields := strings.SplitN(header, "\x1f", 7)
		if len(fields) != 7 || !gitCommitID.MatchString(fields[0]) {
			continue
		}
		parents := strings.Fields(fields[1])
		if stashCommit(fields[6], len(parents)) {
			continue
		}
		author, _ := strconv.ParseInt(fields[2], 10, 64)
		committed, _ := strconv.ParseInt(fields[3], 10, 64)
		commit := &gitCommit{Hash: fields[0], Parents: len(parents), ParentHashes: parents, Tree: fields[4], AuthorEmail: strings.ToLower(fields[5]),
			Author: time.Unix(author, 0).UTC(), Commit: time.Unix(committed, 0).UTC(), Files: map[string][2]int64{}}
		if subject := messageSubject(fields[6]); subject != "" {
			commit.Subject, commit.SubjectLength = subjectFingerprint(subject), len(subject)
		}
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
	return commits
}

// inferLinks links a repository's commits, reading signals for the
// contested ones; it returns the links and the signals it read.
func inferLinks(ctx context.Context, clones []*gitClone, sessions map[string]*gitSession, members []string, repository string, commits map[string]*gitCommit) ([]gitLink, *gitSignals) {
	links, contested := linkCommits(sessions, members, repository, commits, nil)
	if len(contested) == 0 {
		return links, nil
	}
	signals := readGitSignals(ctx, clones, sessions, contested)
	links, _ = linkCommits(sessions, members, repository, commits, signals)
	return links, signals
}

// linkCommits links a repository's commits to its sessions, outside merges,
// by the first of these that applies:
//   - a hash the session's own git commit printed (1.0);
//   - a git command of the session's that ran when the commit was made
//     (commandLinks): 0.95 when the command wrote its subject, 0.9 when it is
//     the only command that could have made it;
//   - a rewrite (rebase, amend, cherry-pick) of a commit linked so: the same
//     author, author time and subject, at the original's confidence;
//   - for a commit no git command explains, as one made by hand: the session
//     whose edit window (plus outcomeGitGrace) holds its author time and that
//     edited one of its files, 0.7 when it is the only such session. Time
//     alone links nothing. So does a printed commit when no session that
//     printed it edited one of its files (one that only committed or opened
//     the pull request): the work it holds is the editing session's, which is
//     linked too.
//
// A commit several sessions could have made is contested. With signals
// (outcome_git_signals.go), it goes to the one candidate the checkout that
// made it or the lines it adds point to, in that order, at that signal's
// confidence; else, between commands, to the one that ran the shortest, and
// between edit windows, to the candidate editing its files nearest the
// commit, then with the tightest window, at 0.5. linkCommits returns the
// contested commits and their candidates, for reading signals.
func linkCommits(sessions map[string]*gitSession, members []string, repository string, commits map[string]*gitCommit, signals *gitSignals) ([]gitLink, map[string][]string) {
	links := []gitLink{}
	contested := map[string][]string{}
	direct, printers := map[string]bool{}, map[string][]string{}
	for _, id := range members {
		link := func(printed string, evidence map[string]any) {
			hash := resolveAbbreviated(printed, commits)
			if hash == "" || direct[id+"\x00"+hash] {
				return
			}
			direct[id+"\x00"+hash], printers[hash] = true, append(printers[hash], id)
			links = append(links, gitLink{Session: id, Hash: hash, Method: "transcript", Confidence: 1, Evidence: evidence})
		}
		for _, printed := range sessions[id].Printed {
			evidence := map[string]any{"printed": printed.Hash}
			if printed.Branch != "" {
				evidence["branch"] = printed.Branch
			}
			link(printed.Hash, evidence)
		}
		// Its git commands' printed hashes, in the repository each ran in: a
		// cherry-pick or revert prints one too, and so does a call a later
		// step failed.
		for _, op := range sessions[id].opsIn(repository) {
			for _, printed := range op.Hashes {
				link(printed, map[string]any{"printed": printed, "command": op.Kind})
			}
		}
	}
	hashes := make([]string, 0, len(commits))
	for hash := range commits {
		hashes = append(hashes, hash)
	}
	slices.Sort(hashes)
	windows := readOpWindows(sessions, members, repository)
	for _, hash := range hashes {
		if commit := commits[hash]; len(printers[hash]) == 0 && commit.Parents <= 1 {
			if link, ids := commandLink(windows, sessions, repository, commit, signals); link != nil {
				links = append(links, *link)
			} else if len(ids) > 0 {
				// Should the commands not explain it, edit inference reads
				// the same signals.
				for _, candidate := range commitCandidates(sessions, members, repository, commit) {
					if !slices.Contains(ids, candidate.id) {
						ids = append(ids, candidate.id)
					}
				}
				contested[hash] = ids
			}
		}
	}
	links = append(links, rewriteLinks(links, commits)...)
	// A commit a command or rewrite explains is not inferred; a printed one
	// is, when no session that printed it edited one of its files.
	for _, link := range links {
		if link.Method != "transcript" {
			direct[link.Hash] = true
		}
	}
	for _, hash := range hashes {
		commit := commits[hash]
		if direct[hash] || contested[hash] != nil || commit.Parents > 1 ||
			slices.ContainsFunc(printers[hash], func(id string) bool { return editedAny(sessions[id], repository, commit) }) {
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

// editedAny reports whether a session edited any of a commit's files.
func editedAny(session *gitSession, repository string, commit *gitCommit) bool {
	for path := range commit.Files {
		if _, ok := session.Files[repository][path]; ok {
			return true
		}
	}
	return false
}

// gitOpSlack widens a git command's window for whole-second commit times
// and for a harness that records a call's end when its output arrives.
var gitOpSlack = 2 * time.Second

// window is when a git command could have made a commit: while its call ran,
// give or take gitOpSlack. A harness that records no end gives the start.
func (op gitOp) window() (from, until time.Time, ok bool) {
	from, ok = parseTime(op.At)
	if !ok {
		return
	}
	until = from
	if ended, endedOK := parseTime(op.Until); endedOK && ended.After(from) {
		until = ended
	}
	return from.Add(-gitOpSlack), until.Add(gitOpSlack), true
}

// opsIn are a session's git commands in a repository: those that named it,
// and those that named none, in the session's own.
func (session *gitSession) opsIn(repository string) []gitOp {
	ops := []gitOp{}
	for _, op := range session.Ops {
		if defaultString(op.Repository, session.Repository) == repository {
			ops = append(ops, op)
		}
	}
	return ops
}

// gitOpWindow is a git command that makes new commits, a commit or a
// revert, and when it ran. The others keep the commits' author times
// (rebase, cherry-pick, am) or make merges, and link nothing by time.
type gitOpWindow struct {
	session     string
	op          gitOp
	from, until time.Time
}

// gitOpTrusted is the longest window in which time alone says a command
// made a commit, when the command named its repository. A longer call (tests
// before the commit, say) or one counted in the session's own repository
// because it named none must be confirmed: see commandLink.
var gitOpTrusted = 10 * time.Second

func (window gitOpWindow) trusted() bool {
	return window.op.Repository != "" && window.until.Sub(window.from) <= gitOpTrusted
}

// gitOpWindows are a repository's, by start, with the longest's span.
type gitOpWindows struct {
	windows []gitOpWindow
	longest time.Duration
}

func readOpWindows(sessions map[string]*gitSession, members []string, repository string) gitOpWindows {
	index := gitOpWindows{}
	for _, id := range members {
		for _, op := range sessions[id].opsIn(repository) {
			if from, until, ok := op.window(); ok && (op.Kind == "commit" || op.Kind == "revert") {
				index.windows = append(index.windows, gitOpWindow{id, op, from, until})
				index.longest = max(index.longest, until.Sub(from))
			}
		}
	}
	slices.SortFunc(index.windows, func(a, b gitOpWindow) int { return cmp.Or(a.from.Compare(b.from), cmp.Compare(a.session, b.session)) })
	return index
}

// holding are the commands running at a time.
func (index gitOpWindows) holding(at time.Time) []gitOpWindow {
	first, _ := slices.BinarySearchFunc(index.windows, at.Add(-index.longest), func(window gitOpWindow, target time.Time) int { return window.from.Compare(target) })
	held := []gitOpWindow{}
	for _, window := range index.windows[first:] {
		if window.from.After(at) {
			break
		}
		if !window.until.Before(at) {
			held = append(held, window)
		}
	}
	return held
}

// commandLink links a commit to the session whose git command made it: one
// that ran when it was committed and wrote its subject, else one that ran
// when it was both written and committed (an amend keeps the author time)
// and wrote no other subject. Time alone is confirmed when the window is not
// trusted: the commit changes a file the session had edited, or the
// session's checkout's reflog made it; else the command does not explain it.
// Between several sessions, the signals decide, else the command that ran
// the shortest, at 0.5. It returns no link and the candidates when they need
// signals.
func commandLink(windows gitOpWindows, sessions map[string]*gitSession, repository string, commit *gitCommit, signals *gitSignals) (*gitLink, []string) {
	type candidate struct {
		gitOpWindow
		message   bool
		confirmed string
	}
	best := map[string]candidate{}
	unconfirmed := []string{}
	for _, window := range windows.holding(commit.Commit) {
		message := commit.Subject != "" && slices.Contains(window.op.Subjects, commit.Subject)
		if !message && (len(window.op.Subjects) > 0 || commit.Author.Before(window.from) || commit.Author.After(window.until)) {
			continue
		}
		confirmed := ""
		if !message && !window.trusted() {
			session := sessions[window.session]
			switch {
			case editedBefore(session, repository, commit):
				confirmed = "edited"
			case signals != nil && slices.ContainsFunc(session.Locations, func(location string) bool { return slices.Contains(signals.MadeIn[commit.Hash], location) }):
				confirmed = "checkout"
			default:
				if signals == nil && !slices.Contains(unconfirmed, window.session) {
					unconfirmed = append(unconfirmed, window.session)
				}
				continue
			}
		}
		current, seen := best[window.session]
		if span := window.until.Sub(window.from); !seen || message && !current.message || message == current.message && span < current.until.Sub(current.from) {
			best[window.session] = candidate{window, message, confirmed}
		}
	}
	candidates := slices.SortedFunc(maps.Values(best), func(a, b candidate) int { return cmp.Compare(a.session, b.session) })
	if slices.ContainsFunc(candidates, func(c candidate) bool { return c.message }) {
		candidates = slices.DeleteFunc(candidates, func(c candidate) bool { return !c.message })
	} else if len(unconfirmed) > 0 {
		// The checkouts' reflogs may confirm them.
		ids := slices.Clone(unconfirmed)
		for _, candidate := range candidates {
			if !slices.Contains(ids, candidate.session) {
				ids = append(ids, candidate.session)
			}
		}
		slices.Sort(ids)
		return nil, ids
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	chosen, method, confidence, by := candidates[0], "window", 0.9, ""
	if chosen.message {
		method, confidence = "message", 0.95
	}
	if len(candidates) > 1 {
		ids := make([]string, len(candidates))
		for index, candidate := range candidates {
			ids[index] = candidate.session
		}
		if signals == nil {
			return nil, ids
		}
		checkout, lines := signals.picks(commit.Hash, repository, sessions, ids)
		for index, pick := range []string{checkout, lines} {
			if pick != "" {
				chosen = candidates[slices.Index(ids, pick)]
				confidence, by = min(confidence, linkTieBreaks[index].confidence), linkTieBreaks[index].name
				break
			}
		}
		if by == "" {
			span := func(c candidate) time.Duration { return c.until.Sub(c.from) }
			slices.SortStableFunc(candidates, func(a, b candidate) int { return cmp.Compare(span(a), span(b)) })
			if span(candidates[0]) == span(candidates[1]) {
				return nil, ids
			}
			chosen, confidence, by = candidates[0], 0.5, "shortest"
		}
	}
	evidence := map[string]any{"command": chosen.op.Kind, "at": chosen.op.At, "committed_at": commit.Commit.Format(time.RFC3339), "candidates": len(candidates)}
	if by != "" {
		evidence["chosen_by"] = by
	}
	if chosen.confirmed != "" {
		evidence["confirmed_by"] = chosen.confirmed
	}
	return &gitLink{Session: chosen.session, Hash: commit.Hash, Method: method, Confidence: confidence, Evidence: evidence}, nil
}

// editedBefore says whether a session had edited one of a commit's files by
// the time it was committed.
func editedBefore(session *gitSession, repository string, commit *gitCommit) bool {
	for path := range commit.Files {
		if span, ok := session.Files[repository][path]; ok && !commit.Commit.Before(span.First) {
			return true
		}
	}
	return false
}

// commitIdentity is what a rebase, amend or cherry-pick keeps of a commit:
// its author, author time and subject. Empty without a subject.
func commitIdentity(commit *gitCommit) string {
	if commit.Subject == "" {
		return ""
	}
	return commit.AuthorEmail + "\x00" + strconv.FormatInt(commit.Author.Unix(), 10) + "\x00" + commit.Subject
}

// rewriteLinks links each rewrite of a commit a session is linked to, at
// the original link's confidence, to the same session.
func rewriteLinks(links []gitLink, commits map[string]*gitCommit) []gitLink {
	byIdentity := map[string][]string{}
	for hash, commit := range commits {
		if identity := commitIdentity(commit); identity != "" && commit.Parents <= 1 {
			byIdentity[identity] = append(byIdentity[identity], hash)
		}
	}
	linked := map[string]bool{}
	for _, link := range links {
		linked[link.Session+"\x00"+link.Hash] = true
	}
	rewrites := []gitLink{}
	for _, link := range links {
		copies := byIdentity[commitIdentity(commits[link.Hash])]
		slices.Sort(copies)
		for _, hash := range copies {
			if !linked[link.Session+"\x00"+hash] {
				linked[link.Session+"\x00"+hash] = true
				rewrites = append(rewrites, gitLink{Session: link.Session, Hash: hash, Method: "rewrite", Confidence: link.Confidence,
					Evidence: map[string]any{"of": link.Hash, "by": link.Method}})
			}
		}
	}
	return rewrites
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

// pullRequests gives each linked commit the pull request its session opened
// in this repository (forge is its normalized remote), when the session
// opened exactly one there and every session linked to the commit agrees.
func pullRequests(links []gitLink, sessions map[string]*gitSession, forge string) map[string]int {
	pulls := map[string]int{}
	if forge == "" {
		return pulls
	}
	for _, link := range links {
		if link.Confidence < outcomeGitCounted {
			continue
		}
		opened := 0
		for _, op := range sessions[link.Session].Ops {
			if op.Kind == "pull_request" && "github.com/"+op.Forge == forge && op.PullRequest != opened {
				if opened != 0 {
					opened = -1
					break
				}
				opened = op.PullRequest
			}
		}
		if previous, seen := pulls[link.Hash]; seen && previous != opened {
			opened = -1
		}
		pulls[link.Hash] = opened
	}
	return pulls
}

// squashSubjectMinimum is the shortest subject a squash's list of commits is
// matched by: "Fix tests" names too many commits to name one.
const squashSubjectMinimum = 16

var squashPullRequest = regexp.MustCompile(`\(#(\d+)\)\s*$`)

// markMainline records whether each linked commit reached mainline, by the
// first of these:
//   - it is in mainline's history;
//   - a commit there has the same change (a rebase or cherry-pick, by
//     patch-id);
//   - a squash on mainline took it in, committed after it: the squash has
//     the tree of a branch commit descended from it; or, like GitHub's,
//     lists its subject as "* subject" (a subject of at least
//     squashSubjectMinimum that no other commit has); or names the one pull
//     request its session opened, as "Title (#N)", and changed every file
//     the commit did.
//
// Without a mainline, in a shallow clone that lacks it, or when its change
// cannot be read (an empty commit or a merge has no patch-id), it stays
// unknown. Each clone answers for the commits it has: one whose mainline has
// a commit makes it landed, whichever clone said otherwise.
func markMainline(ctx context.Context, dir, after string, commits, linked map[string]*gitCommit, pulls map[string]int, shallow bool) error {
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
			commit.OnMainline, commit.MainlineEvidence, commit.SameAs = new(true), "reachable from "+strings.TrimPrefix(ref, "refs/"), ""
		} else if commit.OnMainline == nil || !*commit.OnMainline {
			missing = append(missing, hash)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	slices.Sort(missing)
	// Comparing changes is a further check: when it cannot run, the rest
	// stay unknown rather than off mainline.
	mine, err := patchIDs(ctx, dir, false, missing)
	if err != nil {
		return nil
	}
	theirs, err := patchIDs(ctx, dir, false, nil, "--no-merges", after, tip)
	if err != nil {
		return nil
	}
	byPatch := map[string]string{}
	for hash, id := range theirs {
		byPatch[id] = hash
	}
	squashed := []string{}
	for _, hash := range missing {
		commit := linked[hash]
		switch match := byPatch[mine[hash]]; {
		case mine[hash] != "" && match != "":
			commit.OnMainline, commit.MainlineEvidence, commit.SameAs = new(true), "same change as "+match[:12]+" on "+strings.TrimPrefix(ref, "refs/"), match
		default:
			squashed = append(squashed, hash)
		}
	}
	if len(squashed) == 0 {
		return nil
	}
	squashes, err := readSquashes(ctx, dir, after, tip, commits, on)
	if err != nil {
		return nil
	}
	for _, hash := range squashed {
		commit := linked[hash]
		if squash, how := squashes.find(commit, pulls[hash]); squash != "" {
			commit.OnMainline, commit.MainlineEvidence = new(true), how+" "+squash[:12]+" on "+strings.TrimPrefix(ref, "refs/")
		} else if mine[hash] != "" && !shallow && commit.OnMainline == nil {
			commit.OnMainline = new(false)
		}
	}
	return nil
}

// gitSquashes is what mainline's squashes say about the commits they took
// in, each keyed to the earliest squash that says it.
type gitSquashes struct {
	commits map[string]*gitCommit
	// byTree are the commits a squash has the tree of a descendant of.
	byTree map[string]string
	// listed are the subjects squashes list; byPull, the pull requests they
	// name.
	listed map[string]string
	byPull map[int]string
	// subjects counts the changes (not copies) with each subject.
	subjects map[string]int
}

// readSquashes reads mainline's non-merge commits since after, with their
// messages, compared in memory and never stored.
func readSquashes(ctx context.Context, dir, after, tip string, commits map[string]*gitCommit, on map[string]bool) (*gitSquashes, error) {
	squashes := &gitSquashes{commits: commits, byTree: map[string]string{}, listed: map[string]string{}, byPull: map[int]string{}, subjects: map[string]int{}}
	earlier := func(hash, current string) bool {
		return current == "" || commits[hash].Commit.Before(commits[current].Commit)
	}
	trees := map[string][]string{}
	identities := map[string]bool{}
	for hash, commit := range commits {
		if on[hash] && commit.Parents <= 1 {
			trees[commit.Tree] = append(trees[commit.Tree], hash)
		}
		if identity := commitIdentity(commit); identity != "" && !identities[identity] {
			identities[identity] = true
			squashes.subjects[commit.Subject]++
		}
	}
	// A branch commit with a squash's tree, and every commit it descends
	// from off mainline, went in with that squash.
	tips := []string{}
	for hash, commit := range commits {
		if !on[hash] && len(trees[commit.Tree]) > 0 {
			tips = append(tips, hash)
		}
	}
	slices.SortFunc(tips, func(a, b string) int { return cmp.Or(commits[a].Commit.Compare(commits[b].Commit), cmp.Compare(a, b)) })
	for _, tip := range tips {
		squash := ""
		for _, hash := range trees[commits[tip].Tree] {
			if !commits[hash].Commit.Before(commits[tip].Commit) && earlier(hash, squash) {
				squash = hash
			}
		}
		for stack := []string{tip}; squash != "" && len(stack) > 0; {
			hash := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if commit := commits[hash]; commit != nil && !on[hash] && squashes.byTree[hash] == "" {
				squashes.byTree[hash] = squash
				stack = append(stack, commit.ParentHashes...)
			}
		}
	}
	output, err := outcomeGitCommand(ctx, dir, outcomeGitLogTimeout, nil, "log", "--no-merges", after, "--format=%x1e%H%x1f%B", tip)
	if err != nil {
		return nil, err
	}
	for _, record := range strings.Split(string(output), "\x1e") {
		hash, body, ok := strings.Cut(record, "\x1f")
		if !ok || commits[hash] == nil {
			continue
		}
		lines := strings.Split(body, "\n")
		if match := squashPullRequest.FindStringSubmatch(lines[0]); match != nil {
			if number, _ := strconv.Atoi(match[1]); earlier(hash, squashes.byPull[number]) {
				squashes.byPull[number] = hash
			}
		}
		for _, line := range lines[1:] {
			if item, ok := strings.CutPrefix(strings.TrimSpace(line), "* "); ok {
				if subject := messageSubject(item); len(subject) >= squashSubjectMinimum {
					if fingerprint := subjectFingerprint(subject); earlier(hash, squashes.listed[fingerprint]) {
						squashes.listed[fingerprint] = hash
					}
				}
			}
		}
	}
	return squashes, nil
}

// find is the squash that took a commit in, and how it says so, given the
// one pull request its session opened (0 or less for none).
func (squashes *gitSquashes) find(commit *gitCommit, pull int) (string, string) {
	after := func(squash string) bool {
		return squash != "" && !squashes.commits[squash].Commit.Before(commit.Commit)
	}
	if squash := squashes.byTree[commit.Hash]; after(squash) {
		return squash, "squashed into"
	}
	if squash := squashes.listed[commit.Subject]; commit.Subject != "" && squashes.subjects[commit.Subject] == 1 && after(squash) {
		return squash, "listed in"
	}
	if squash := squashes.byPull[pull]; pull > 0 && after(squash) && squashes.changedAll(squash, commit) {
		return squash, "pull request #" + strconv.Itoa(pull) + ","
	}
	return "", ""
}

// changedAll says whether a squash changed every file a commit changed. A
// session's pull request is not tied to each of its commits: one on another
// branch, changing files the squash did not, was not taken in by it.
func (squashes *gitSquashes) changedAll(squash string, commit *gitCommit) bool {
	if len(commit.Files) == 0 {
		return false
	}
	for path := range commit.Files {
		if _, ok := squashes.commits[squash].Files[path]; !ok {
			return false
		}
	}
	return true
}

// linkOnce links each session once to each change: of the copies a rebase,
// amend or cherry-pick made (the same author, author time and subject, or
// the same change on mainline), to the one on mainline, then one a ref
// still holds, then the newest. A replaced commit the session's own later
// work took the place of is left out (dropSuperseded).
func linkOnce(links []gitLink, commits map[string]*gitCommit) []gitLink {
	change := func(hash string) string {
		commit := commits[hash]
		if commit.SameAs != "" && commits[commit.SameAs] != nil {
			commit = commits[commit.SameAs]
		}
		return cmp.Or(commitIdentity(commit), commit.Hash)
	}
	// On mainline itself, then by a copy there, then unknown, then off it.
	rank := func(hash string) int {
		switch commit := commits[hash]; {
		case commit.OnMainline == nil:
			return 1
		case !*commit.OnMainline:
			return 0
		case commit.SameAs == "":
			return 3
		}
		return 2
	}
	held := func(hash string) int {
		if commits[hash].Replaced {
			return 0
		}
		return 1
	}
	better := func(a, b string) bool {
		return cmp.Or(cmp.Compare(rank(a), rank(b)), cmp.Compare(held(a), held(b)), commits[a].Commit.Compare(commits[b].Commit), cmp.Compare(a, b)) > 0
	}
	best := map[string]int{}
	for index, link := range links {
		key := link.Session + "\x00" + change(link.Hash)
		if current, ok := best[key]; !ok || better(link.Hash, links[current].Hash) {
			best[key] = index
		}
	}
	// The link kept carries the strongest evidence of the change's links, so
	// a printed hash still counts when only inference linked the copy kept.
	for index, link := range links {
		if keep := best[link.Session+"\x00"+change(link.Hash)]; keep != index && link.Confidence > links[keep].Confidence {
			links[keep].Method, links[keep].Confidence, links[keep].Evidence = link.Method, link.Confidence, link.Evidence
		}
	}
	kept := []gitLink{}
	for index, link := range links {
		if best[link.Session+"\x00"+change(link.Hash)] == index {
			kept = append(kept, link)
		}
	}
	return dropSuperseded(kept, commits)
}

// dropSuperseded leaves out a session's links to replaced commits that a
// commit of its own, one a ref holds and counted, took the place of: one
// with the tree of a commit in the replaced commit's line (the replaced
// commits joined to it by parents), as a squash by reset or a fixup an
// interactive rebase folded in has; or one made on the parent that line
// started from, as an amend that changed the subject or a redo is. Its
// change is the held commit's, or was given up for it: counted too, its
// lines would count twice.
func dropSuperseded(links []gitLink, commits map[string]*gitCommit) []gitLink {
	trees, parents := map[string]bool{}, map[string]bool{}
	for _, link := range links {
		if commit := commits[link.Hash]; !commit.Replaced && link.Confidence >= outcomeGitCounted {
			trees[link.Session+"\x00"+commit.Tree] = true
			for _, parent := range commit.ParentHashes {
				parents[link.Session+"\x00"+parent] = true
			}
		}
	}
	if len(trees) == 0 {
		return links
	}
	children := map[string][]string{}
	for hash, commit := range commits {
		if commit.Replaced {
			for _, parent := range commit.ParentHashes {
				children[parent] = append(children[parent], hash)
			}
		}
	}
	superseded := func(session, hash string) bool {
		seen, stack := map[string]bool{hash: true}, []string{hash}
		for len(stack) > 0 {
			commit := commits[stack[len(stack)-1]]
			stack = stack[:len(stack)-1]
			if trees[session+"\x00"+commit.Tree] {
				return true
			}
			for _, other := range slices.Concat(commit.ParentHashes, children[commit.Hash]) {
				if parents[session+"\x00"+other] && slices.Contains(commit.ParentHashes, other) {
					return true
				}
				if next := commits[other]; next != nil && next.Replaced && !seen[other] {
					seen[other] = true
					stack = append(stack, other)
				}
			}
		}
		return false
	}
	return slices.DeleteFunc(links, func(link gitLink) bool {
		return commits[link.Hash].Replaced && superseded(link.Session, link.Hash)
	})
}

// patchIDs gives each commit's patch-id, of its change or of its inverse:
// of the commits listed, read on stdin since there can be more than a
// command line holds, or else of the revisions given. A commit with no
// change to compare has none.
func patchIDs(ctx context.Context, dir string, reverse bool, listed []string, revisions ...string) (map[string]string, error) {
	// Without a/ and b/ prefixes, which an inverse diff swaps, and without
	// colour, which a repository can force on and patch-id cannot read.
	args := append([]string{"log", "-p", "--no-prefix", "--no-color", "--format=commit %H"}, gitRawDiff...)
	if reverse {
		args = append(args, "-R")
	}
	var stdin []byte
	if listed != nil {
		stdin = []byte(strings.Join(listed, "\n") + "\n")
		args = append(args, "--no-walk=unsorted", "--stdin")
	}
	patches, err := outcomeGitCommand(ctx, dir, outcomeGitLogTimeout, stdin, append(args, revisions...)...)
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

// markReverts records which of the given commits (scanRepository passes
// every commit it stores) a later commit reverted: by the message git revert
// writes, or by a diff that is the exact inverse of the commit's (same
// files, lines added and removed swapped, and the inverse patch-id equal to
// the commit's).
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
			if other == hash || later.Parents > 1 || later.Replaced || later.Commit.Before(commit.Commit) || len(later.Files) != len(commit.Files) {
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
	forward, err := patchIDs(ctx, dir, false, targets)
	if err != nil {
		return nil
	}
	backward, err := patchIDs(ctx, dir, true, reverters)
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
// to look is not evidence. A scan replaces the links of commits it read and
// its own Mac's earlier links; another Mac's link to a commit it did not read
// (on a branch this Mac's clones lack) stays. Only rows that changed are
// written.
func (c *Catalog) writeGitScan(ctx context.Context, repository, host, key string, scan gitScan) error {
	return c.writeTransaction(ctx, "session-git", func(tx *sql.Tx) error {
		if scan.Status == "checked" {
			if err := writeGitLinks(ctx, tx, repository, host, scan); err != nil {
				return err
			}
		}
		var since any
		if !scan.Since.IsZero() {
			since = scan.Since.UTC().Format(time.RFC3339)
		}
		// A failed scan keeps the commits an earlier one stored, so it keeps
		// how far back they were read too.
		_, err := tx.ExecContext(ctx, `INSERT INTO git_repository_scans(repository_id,host_id,scan_key,status,reason,scanned_at,history_since) VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(repository_id,host_id) DO UPDATE SET scan_key=excluded.scan_key,status=excluded.status,reason=excluded.reason,scanned_at=excluded.scanned_at,
			history_since=CASE WHEN excluded.status='checked' THEN excluded.history_since ELSE git_repository_scans.history_since END`,
			repository, host, key, scan.Status, nilIfEmpty(scan.Reason), now(), since)
		return err
	})
}

// gitCommitColumns are the columns of git_commits a scan writes after the
// key, in the order gitCommitValues gives them.
var gitCommitColumns = []string{"author_at", "committed_at", "parents", "files", "lines_added", "lines_removed", "on_mainline", "mainline_evidence", "reverted_by", "revert_evidence", "ai_note"}

func gitCommitValues(commit *gitCommit) []any {
	var mainline any
	if commit.OnMainline != nil {
		mainline = boolInt(*commit.OnMainline)
	}
	return []any{commit.Author.Format(time.RFC3339), commit.Commit.Format(time.RFC3339), commit.Parents, len(commit.Files),
		commit.Added, commit.Removed, mainline, nilIfEmpty(commit.MainlineEvidence), nilIfEmpty(commit.RevertedBy), nilIfEmpty(commit.RevertEvidence), boolInt(commit.AINote)}
}

// sameStored reports whether a stored row holds values in columns.
func sameStored(row map[string]any, columns []string, values []any) bool {
	for index, column := range columns {
		if fmt.Sprint(row[column]) != fmt.Sprint(values[index]) {
			return false
		}
	}
	return true
}

func writeGitLinks(ctx context.Context, tx *sql.Tx, repository, host string, scan gitScan) error {
	want := map[[2]string]gitLink{}
	for _, link := range scan.Links {
		want[[2]string{link.Session, link.Hash}] = link
	}
	linkColumns := []string{"method", "confidence", "evidence_json", "host_id"}
	stored, err := queryMapsContext(ctx, tx, "SELECT workspace_id,hash,method,confidence,evidence_json,host_id FROM session_commit_links WHERE repository_id=?", repository)
	if err != nil {
		return err
	}
	for _, row := range stored {
		id := [2]string{firstString(row["workspace_id"]), firstString(row["hash"])}
		if link, ok := want[id]; ok {
			if sameStored(row, linkColumns, []any{link.Method, link.Confidence, jsonText(link.Evidence), host}) {
				delete(want, id)
			}
			continue
		}
		if firstString(row["host_id"]) != host && scan.Commits[id[1]] == nil {
			continue
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM session_commit_links WHERE workspace_id=? AND repository_id=? AND hash=?", id[0], repository, id[1]); err != nil {
			return err
		}
	}
	for _, link := range scan.Links {
		if _, ok := want[[2]string{link.Session, link.Hash}]; !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO session_commit_links(workspace_id,repository_id,hash,method,confidence,evidence_json,host_id) VALUES(?,?,?,?,?,?,?)`,
			link.Session, repository, link.Hash, link.Method, link.Confidence, jsonText(link.Evidence), host); err != nil {
			return err
		}
	}
	// The commits of this scan's links, as it read them; another Mac's
	// commits that a kept link still names stay as that Mac read them.
	// Commits no session is linked to are kept too where they changed a file
	// some session edited or committed, for file history, with each such
	// file's lines and their revert state but without a mainline answer. A
	// replaced one no session is linked to is a copy of another, and not kept.
	linked := map[string]bool{}
	for _, link := range scan.Links {
		linked[link.Hash] = true
	}
	files := map[[2]string][2]int64{}
	for hash, commit := range scan.Commits {
		if commit.Replaced && !linked[hash] {
			continue
		}
		for path, lines := range commit.Files {
			if scan.Edited[path] {
				files[[2]string{hash, path}] = lines
				linked[hash] = true
			}
		}
	}
	if err := writeGitCommitFiles(ctx, tx, repository, files); err != nil {
		return err
	}
	kept, err := queryMapsContext(ctx, tx, "SELECT DISTINCT hash FROM session_commit_links WHERE repository_id=?", repository)
	if err != nil {
		return err
	}
	named := map[string]bool{}
	for _, row := range kept {
		named[firstString(row["hash"])] = true
	}
	commits, err := queryMapsContext(ctx, tx, "SELECT hash,"+strings.Join(gitCommitColumns, ",")+" FROM git_commits WHERE repository_id=?", repository)
	if err != nil {
		return err
	}
	for _, row := range commits {
		hash := firstString(row["hash"])
		switch {
		case linked[hash]:
			if sameStored(row, gitCommitColumns, gitCommitValues(scan.Commits[hash])) {
				delete(linked, hash)
			}
		case !named[hash]:
			if _, err := tx.ExecContext(ctx, "DELETE FROM git_commits WHERE repository_id=? AND hash=?", repository, hash); err != nil {
				return err
			}
		}
	}
	hashes := make([]string, 0, len(linked))
	for hash := range linked {
		hashes = append(hashes, hash)
	}
	slices.Sort(hashes)
	for _, hash := range hashes {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO git_commits(repository_id,hash,`+strings.Join(gitCommitColumns, ",")+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			append([]any{repository, hash}, gitCommitValues(scan.Commits[hash])...)...); err != nil {
			return err
		}
	}
	return nil
}

// writeGitCommitFiles makes a repository's git_commit_files hold files,
// writing only the rows that changed.
func writeGitCommitFiles(ctx context.Context, tx *sql.Tx, repository string, files map[[2]string][2]int64) error {
	stored, err := queryMapsContext(ctx, tx, "SELECT hash,path,lines_added,lines_removed FROM git_commit_files WHERE repository_id=?", repository)
	if err != nil {
		return err
	}
	want := maps.Clone(files)
	for _, row := range stored {
		id := [2]string{firstString(row["hash"]), firstString(row["path"])}
		if lines, ok := want[id]; ok && integer(row["lines_added"]) == lines[0] && integer(row["lines_removed"]) == lines[1] {
			delete(want, id)
			continue
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM git_commit_files WHERE repository_id=? AND hash=? AND path=?", repository, id[0], id[1]); err != nil {
			return err
		}
	}
	for id, lines := range want {
		if _, err := tx.ExecContext(ctx, `INSERT INTO git_commit_files(repository_id,hash,path,lines_added,lines_removed) VALUES(?,?,?,?,?)`,
			repository, id[0], id[1], lines[0], lines[1]); err != nil {
			return err
		}
	}
	return nil
}

// sessionGitTotals are a session's counts over its counted links, in
// session_git's column order.
const sessionGitTotals = `COUNT(*) linked,COALESCE(SUM(g.on_mainline=1),0) landed,COALESCE(SUM(g.reverted_by IS NOT NULL),0) reverted,
	COALESCE(SUM(g.on_mainline IS NULL),0) unknown,COALESCE(SUM(g.lines_added),0) added,COALESCE(SUM(CASE WHEN g.on_mainline=1 THEN g.lines_added END),0) added_landed`

// writeSessionGit records each session's Tier 2 state from its
// repositories' scans: checked when every one was, else not checked with the
// first reason, and not applicable for a session that changed no file in a
// repository; and its linked, landed and reverted commits and the lines they
// add, counting only links of at least outcomeGitCounted. A repository is
// checked when any Mac checked it, since its links stay; else this Mac's
// reason, or another's. Only the sessions whose state changed are written, so
// a pass that found nothing new leaves the cached views alone.
func (c *Catalog) writeSessionGit(ctx context.Context, sessions map[string]*gitSession, byRepository map[string][]string) error {
	return c.writeTransaction(ctx, "session-git-state", func(tx *sql.Tx) error {
		scans, err := queryMapsContext(ctx, tx, "SELECT repository_id,host_id,status,reason FROM git_repository_scans ORDER BY host_id=?,host_id", currentHost().ID)
		if err != nil {
			return err
		}
		status := map[string][2]string{}
		for _, row := range scans {
			if repository := firstString(row["repository_id"]); status[repository][0] != "checked" {
				status[repository] = [2]string{firstString(row["status"]), firstString(row["reason"])} // this Mac's last
			}
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
		counts, err := queryMapsContext(ctx, tx, `SELECT l.workspace_id,`+sessionGitTotals+`
			FROM session_commit_links l JOIN git_commits g ON g.repository_id=l.repository_id AND g.hash=l.hash WHERE l.confidence>=? GROUP BY l.workspace_id`, outcomeGitCounted)
		if err != nil {
			return err
		}
		tally := map[string]string{}
		for _, row := range counts {
			tally[firstString(row["workspace_id"])] = fmt.Sprint(row["linked"], "|", row["landed"], "|", row["reverted"], "|", row["unknown"], "|", row["added"], "|", row["added_landed"])
		}
		stored, err := queryMapsContext(ctx, tx, "SELECT workspace_id,status,COALESCE(reason,'') reason,linked_commits,landed_commits,reverted_commits,mainline_unknown,lines_added,lines_landed FROM session_git")
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
				counted = "0|0|0|0|0|0"
			}
			if want == [2]string{firstString(row["status"]), firstString(row["reason"])} &&
				counted == fmt.Sprint(row["linked_commits"], "|", row["landed_commits"], "|", row["reverted_commits"], "|", row["mainline_unknown"], "|", row["lines_added"], "|", row["lines_landed"]) {
				delete(state, id)
			}
		}
		ids := make([]string, 0, len(state))
		for id := range state {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO session_git(workspace_id,status,reason,linked_commits,landed_commits,reverted_commits,mainline_unknown,lines_added,lines_landed,checked_at)
				SELECT ?,?,?,`+sessionGitTotals+`,?
				FROM session_commit_links l JOIN git_commits g ON g.repository_id=l.repository_id AND g.hash=l.hash WHERE l.workspace_id=? AND l.confidence>=?`,
				id, state[id][0], nilIfEmpty(state[id][1]), now(), id, outcomeGitCounted); err != nil {
				return err
			}
		}
		return nil
	})
}
