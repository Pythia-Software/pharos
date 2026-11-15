package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Pull request reviews (docs/pull-request-reviews.md): what a forge records
// about the pull requests workspaces opened or committed to. A pull request
// has reviews, each with a verdict and an optional summary, and comments,
// either on a line of the change (in a thread, usually part of a review) or
// in the pull request's conversation. That model is the one GitHub, GitLab
// and Gitea share; a forge is anything that can answer forge.PullRequest, and
// nothing below names one.
//
// Reading a forge is off unless pull_request_reviews_enabled is on. What it
// answers is stored in full, the text of reviews and comments included, and
// travels with the library: unlike local Git's facts it cannot be derived
// again from the transcripts, and a forge may stop serving it.

// forgePullRequest is one pull request as its forge describes it.
type forgePullRequest struct {
	State        string `json:"state"` // open, merged or closed
	Draft        bool   `json:"draft,omitempty"`
	Title        string `json:"title,omitempty"`
	Author       string `json:"author,omitempty"`
	AuthorKind   string `json:"author_kind,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	MergedAt     string `json:"merged_at,omitempty"`
	ClosedAt     string `json:"closed_at,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
	BaseRef      string `json:"base_ref,omitempty"`
	HeadRef      string `json:"head_ref,omitempty"`
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
	ChangedFiles int    `json:"changed_files"`
	// Decision is the forge's own standing verdict, where it keeps one:
	// approved, changes_requested or review_required.
	Decision string         `json:"decision,omitempty"`
	Commits  []forgeCommit  `json:"commits"`
	Reviews  []forgeReview  `json:"reviews"`
	Comments []forgeComment `json:"comments"`
	// Truncated is set when the forge held more than was read (a thread with
	// over a hundred comments), so counts are a lower bound.
	Truncated bool `json:"truncated,omitempty"`
}

type forgeCommit struct {
	Hash        string `json:"hash"`
	CommittedAt string `json:"committed_at,omitempty"`
}

// forgeReview is one submitted review: its verdict (approved,
// changes_requested, commented or dismissed) and its summary, if it has one.
type forgeReview struct {
	ID          string `json:"id"`
	Author      string `json:"author,omitempty"`
	AuthorKind  string `json:"author_kind,omitempty"`
	State       string `json:"state"`
	Body        string `json:"body,omitempty"`
	SubmittedAt string `json:"submitted_at,omitempty"`
	Commit      string `json:"commit,omitempty"`
	URL         string `json:"url,omitempty"`
}

// forgeComment is one comment: inline, on a line of the change and in a
// thread, or in the pull request's conversation.
type forgeComment struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"` // inline or conversation
	ReviewID   string `json:"review_id,omitempty"`
	ThreadID   string `json:"thread_id,omitempty"`
	ReplyTo    string `json:"reply_to,omitempty"`
	Author     string `json:"author,omitempty"`
	AuthorKind string `json:"author_kind,omitempty"`
	Body       string `json:"body,omitempty"`
	Path       string `json:"path,omitempty"`
	Line       int    `json:"line,omitempty"`
	Resolved   bool   `json:"resolved,omitempty"`
	Outdated   bool   `json:"outdated,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	URL        string `json:"url,omitempty"`
}

// A forge answers for the pull requests of one host.
type forge interface {
	// Unavailable is why the forge cannot be asked on this Mac ("" when it
	// can): its tool is missing or signed out. It must not use the network:
	// being offline is not being signed out.
	Unavailable(ctx context.Context, host string) string
	// PullRequest reads one pull request of the repository at slug
	// (owner/name). errForgeNotFound means the forge has no such pull
	// request, or does not show it to this login.
	PullRequest(ctx context.Context, host, slug string, number int) (*forgePullRequest, error)
}

var (
	errForgeNotFound = errors.New("the forge has no such pull request")
	// errForgeBusy stops a pass: the forge asked for fewer requests.
	errForgeBusy = errors.New("the forge is limiting requests")
)

// forges are the forges Pharos can read, by host. Tests replace it.
var forges = map[string]forge{"github.com": githubForge{}}

// How often a pull request is read again, and how many one pass reads. An
// open pull request changes while it is reviewed; one that closed can still
// gather comments for a while, and then it is settled and never read again.
var (
	forgeOpenRefresh    = time.Hour
	forgeClosedRefresh  = 24 * time.Hour
	forgeSettled        = 30 * 24 * time.Hour
	forgeFailedRetry    = 6 * time.Hour
	forgeNotFoundRetry  = 7 * 24 * time.Hour
	forgeRunLimit       = 150
	forgeRequestTimeout = 30 * time.Second
)

const pullRequestReviewsSQL = `
CREATE TABLE IF NOT EXISTS pull_request_fetches(
  pr_id TEXT PRIMARY KEY,
  status TEXT NOT NULL,
  reason TEXT,
  fetched_at TEXT NOT NULL,
  content_hash TEXT NOT NULL DEFAULT '') WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS pull_request_details(
  pr_id TEXT PRIMARY KEY,
  state TEXT NOT NULL,
  draft INTEGER NOT NULL DEFAULT 0,
  title TEXT,
  author TEXT,
  author_kind TEXT,
  created_at TEXT,
  merged_at TEXT,
  closed_at TEXT,
  updated_at TEXT,
  base_ref TEXT,
  head_ref TEXT,
  additions INTEGER NOT NULL DEFAULT 0,
  deletions INTEGER NOT NULL DEFAULT 0,
  changed_files INTEGER NOT NULL DEFAULT 0,
  decision TEXT,
  truncated INTEGER NOT NULL DEFAULT 0,
  review_status TEXT NOT NULL DEFAULT 'none',
  reviewer_kind TEXT NOT NULL DEFAULT 'none',
  reviews INTEGER NOT NULL DEFAULT 0,
  changes_requested INTEGER NOT NULL DEFAULT 0,
  approvals INTEGER NOT NULL DEFAULT 0,
  comments INTEGER NOT NULL DEFAULT 0,
  threads INTEGER NOT NULL DEFAULT 0,
  unresolved_threads INTEGER NOT NULL DEFAULT 0,
  first_review_at TEXT,
  commits_after_review INTEGER NOT NULL DEFAULT 0) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS pull_request_commits(
  pr_id TEXT NOT NULL,
  hash TEXT NOT NULL,
  position INTEGER NOT NULL,
  committed_at TEXT,
  PRIMARY KEY(pr_id,hash)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS pull_request_commits_hash_idx ON pull_request_commits(hash);
CREATE TABLE IF NOT EXISTS pull_request_reviews(
  pr_id TEXT NOT NULL,
  review_id TEXT NOT NULL,
  author TEXT,
  author_kind TEXT,
  state TEXT NOT NULL,
  body TEXT,
  submitted_at TEXT,
  commit_hash TEXT,
  url TEXT,
  PRIMARY KEY(pr_id,review_id)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS pull_request_comments(
  pr_id TEXT NOT NULL,
  comment_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  review_id TEXT,
  thread_id TEXT,
  reply_to TEXT,
  author TEXT,
  author_kind TEXT,
  body TEXT,
  path TEXT,
  line INTEGER,
  resolved INTEGER NOT NULL DEFAULT 0,
  outdated INTEGER NOT NULL DEFAULT 0,
  created_at TEXT,
  url TEXT,
  PRIMARY KEY(pr_id,comment_id)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS pull_request_workspaces(
  workspace_id TEXT NOT NULL,
  pr_id TEXT NOT NULL,
  role TEXT NOT NULL,
  confidence REAL NOT NULL,
  evidence_json TEXT NOT NULL,
  PRIMARY KEY(workspace_id,pr_id)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS pull_request_workspaces_pr_idx ON pull_request_workspaces(pr_id);
`

// pullRequestReviewTables are the tables keyed by a pull request's ID. They
// hold no foreign key to pull_requests: indexing a workspace again replaces
// its pull request rows under the same IDs, and what the forge said must
// outlive that. A repository merge, which does change the IDs, moves them
// (movePullRequestReviews).
var pullRequestReviewTables = []string{"pull_request_fetches", "pull_request_details", "pull_request_commits", "pull_request_reviews", "pull_request_comments", "pull_request_workspaces"}

func (c *Catalog) ensurePullRequestReviews() error {
	_, err := c.DB.Exec(pullRequestReviewsSQL)
	return err
}

// movePullRequestReviews gives what the forge said of oldID to newID. A
// forge's answer is one pull request's details, commits, reviews and comments
// together, and its summary and content hash describe those rows, so it is
// taken or left whole, as a library transfer does (mergeHistory): when newID
// already holds details, its own answer stands and oldID's is dropped. Which
// workspaces the pull request belongs to is not part of an answer, and is
// always carried over.
func movePullRequestReviews(tx *sql.Tx, oldID, newID string) error {
	var held int
	if err := tx.QueryRow("SELECT COUNT(*) FROM pull_request_details WHERE pr_id=?", newID).Scan(&held); err != nil {
		return err
	}
	if held == 0 {
		// newID may hold a failed read's record, or nothing: either way
		// oldID's answer replaces it whole.
		for _, table := range pullRequestReviewTables {
			if table == "pull_request_workspaces" {
				continue
			}
			if _, err := tx.Exec("DELETE FROM "+table+" WHERE pr_id=?", newID); err != nil {
				return err
			}
		}
	}
	for _, table := range pullRequestReviewTables {
		if held == 0 || table == "pull_request_workspaces" {
			if _, err := tx.Exec("UPDATE OR IGNORE "+table+" SET pr_id=? WHERE pr_id=?", newID, oldID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec("DELETE FROM "+table+" WHERE pr_id=?", oldID); err != nil {
			return err
		}
	}
	return nil
}

// pullRequestSummary is what a pull request's reviews come to. Only other
// people's count: the author's own replies and self-review are not a review
// of the change. A bot's review counts, and reviewer_kind says which it was.
type pullRequestSummary struct {
	// Status is the strongest verdict any reviewer gave: changes_requested,
	// else approved, else commented (a review or comment without a verdict),
	// else none.
	Status           string
	ReviewerKind     string // human, bot, both or none
	Reviews          int
	ChangesRequested int
	Approvals        int
	Comments         int
	Threads          int
	Unresolved       int
	FirstReviewAt    string
	// CommitsAfter is the commits made after the first review or comment.
	CommitsAfter int
}

func summarizePullRequest(pr *forgePullRequest) pullRequestSummary {
	summary := pullRequestSummary{Status: "none", ReviewerKind: "none"}
	other := func(author string) bool { return author == "" || !strings.EqualFold(author, pr.Author) }
	kinds := map[string]bool{}
	first := time.Time{}
	seen := func(kind, at string) {
		if kind == "bot" {
			kinds["bot"] = true
		} else {
			kinds["human"] = true
		}
		if value, ok := parseTime(at); ok && (first.IsZero() || value.Before(first)) {
			first = value
		}
	}
	for _, review := range pr.Reviews {
		if !other(review.Author) || review.State == "pending" {
			continue
		}
		summary.Reviews++
		switch review.State {
		case "changes_requested":
			summary.ChangesRequested++
		case "approved":
			summary.Approvals++
		}
		seen(review.AuthorKind, review.SubmittedAt)
	}
	threads := map[string]bool{}
	for _, comment := range pr.Comments {
		if comment.ThreadID != "" && !threads[comment.ThreadID] {
			threads[comment.ThreadID] = true
			summary.Threads++
			if !comment.Resolved {
				summary.Unresolved++
			}
		}
		if !other(comment.Author) {
			continue
		}
		summary.Comments++
		seen(comment.AuthorKind, comment.CreatedAt)
	}
	switch {
	case summary.ChangesRequested > 0:
		summary.Status = "changes_requested"
	case summary.Approvals > 0:
		summary.Status = "approved"
	case summary.Reviews > 0 || summary.Comments > 0:
		summary.Status = "commented"
	}
	switch {
	case kinds["human"] && kinds["bot"]:
		summary.ReviewerKind = "both"
	case kinds["human"]:
		summary.ReviewerKind = "human"
	case kinds["bot"]:
		summary.ReviewerKind = "bot"
	}
	if !first.IsZero() {
		summary.FirstReviewAt = formatTime(first)
		for _, commit := range pr.Commits {
			if at, ok := parseTime(commit.CommittedAt); ok && at.After(first) {
				summary.CommitsAfter++
			}
		}
	}
	return summary
}

// storePullRequest writes one answer. A pull request that reads as it did
// last time only has its fetch time moved, so views are not invalidated by a
// forge that had nothing new to say.
func storePullRequest(tx *sql.Tx, id string, pr *forgePullRequest, at string) (changed bool, err error) {
	digest := sha256.Sum256([]byte(jsonText(pr)))
	hash := hex.EncodeToString(digest[:])
	var previous sql.NullString
	if err := tx.QueryRow("SELECT content_hash FROM pull_request_fetches WHERE pr_id=? AND status='fetched'", id).Scan(&previous); err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if _, err := tx.Exec(`INSERT INTO pull_request_fetches(pr_id,status,reason,fetched_at,content_hash) VALUES(?,'fetched',NULL,?,?)
		ON CONFLICT(pr_id) DO UPDATE SET status='fetched',reason=NULL,fetched_at=excluded.fetched_at,content_hash=excluded.content_hash`, id, at, hash); err != nil {
		return false, err
	}
	if previous.String == hash {
		return false, nil
	}
	for _, table := range []string{"pull_request_details", "pull_request_commits", "pull_request_reviews", "pull_request_comments"} {
		if _, err := tx.Exec("DELETE FROM "+table+" WHERE pr_id=?", id); err != nil {
			return false, err
		}
	}
	summary := summarizePullRequest(pr)
	if _, err := tx.Exec(`INSERT INTO pull_request_details(pr_id,state,draft,title,author,author_kind,created_at,merged_at,closed_at,updated_at,base_ref,head_ref,
		additions,deletions,changed_files,decision,truncated,review_status,reviewer_kind,reviews,changes_requested,approvals,comments,threads,unresolved_threads,first_review_at,commits_after_review)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, pr.State, boolInt(pr.Draft), nilIfEmpty(pr.Title), nilIfEmpty(pr.Author), nilIfEmpty(pr.AuthorKind), nilIfEmpty(pr.CreatedAt), nilIfEmpty(pr.MergedAt), nilIfEmpty(pr.ClosedAt), nilIfEmpty(pr.UpdatedAt),
		nilIfEmpty(pr.BaseRef), nilIfEmpty(pr.HeadRef), pr.Additions, pr.Deletions, pr.ChangedFiles, nilIfEmpty(pr.Decision), boolInt(pr.Truncated),
		summary.Status, summary.ReviewerKind, summary.Reviews, summary.ChangesRequested, summary.Approvals, summary.Comments, summary.Threads, summary.Unresolved, nilIfEmpty(summary.FirstReviewAt), summary.CommitsAfter); err != nil {
		return false, err
	}
	for position, commit := range pr.Commits {
		if _, err := tx.Exec("INSERT OR IGNORE INTO pull_request_commits(pr_id,hash,position,committed_at) VALUES(?,?,?,?)", id, strings.ToLower(commit.Hash), position, nilIfEmpty(commit.CommittedAt)); err != nil {
			return false, err
		}
	}
	for _, review := range pr.Reviews {
		if _, err := tx.Exec("INSERT OR IGNORE INTO pull_request_reviews(pr_id,review_id,author,author_kind,state,body,submitted_at,commit_hash,url) VALUES(?,?,?,?,?,?,?,?,?)",
			id, review.ID, nilIfEmpty(review.Author), nilIfEmpty(review.AuthorKind), review.State, nilIfEmpty(review.Body), nilIfEmpty(review.SubmittedAt), nilIfEmpty(review.Commit), nilIfEmpty(review.URL)); err != nil {
			return false, err
		}
	}
	for _, comment := range pr.Comments {
		var line any
		if comment.Line > 0 {
			line = comment.Line
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO pull_request_comments(pr_id,comment_id,kind,review_id,thread_id,reply_to,author,author_kind,body,path,line,resolved,outdated,created_at,url)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, comment.ID, comment.Kind, nilIfEmpty(comment.ReviewID), nilIfEmpty(comment.ThreadID), nilIfEmpty(comment.ReplyTo),
			nilIfEmpty(comment.Author), nilIfEmpty(comment.AuthorKind), nilIfEmpty(comment.Body), nilIfEmpty(comment.Path), line, boolInt(comment.Resolved), boolInt(comment.Outdated), nilIfEmpty(comment.CreatedAt), nilIfEmpty(comment.URL)); err != nil {
			return false, err
		}
	}
	return true, nil
}

// repositoryRemotes maps every remote a repository is known by
// (host/owner/name, lower case) to its ID. A remote two repositories claim
// names neither.
func (c *Catalog) repositoryRemotes(ctx context.Context) (map[string]string, error) {
	identities, err := c.gitIdentities(ctx)
	if err != nil {
		return nil, err
	}
	remotes := map[string]string{}
	for id, identity := range identities {
		for _, remote := range identity.Remotes {
			if other, taken := remotes[remote]; taken && other != id {
				remotes[remote] = ""
				continue
			}
			remotes[remote] = id
		}
	}
	return remotes, nil
}

// pullRequestPlace reads a pull request's address: its host, its
// repository's remote (host/owner/name, lower case), the slug the forge
// knows it by, and its number.
func pullRequestPlace(address string) (host, remote, slug string, number int, ok bool) {
	match := prURLPattern.FindStringSubmatch(address)
	if match == nil {
		return "", "", "", 0, false
	}
	number, _ = strconv.Atoi(match[4])
	host = strings.ToLower(match[1])
	slug = match[2] + "/" + match[3]
	return host, host + "/" + strings.ToLower(slug), slug, number, number > 0
}

// pullRequestWorkspace is why a pull request is a workspace's own work.
type pullRequestWorkspace struct {
	Role       string
	Confidence float64
	Evidence   string
}

// refreshPullRequestWorkspaces derives which pull requests are a workspace's
// own, from evidence stronger than its address appearing in a message:
//
//   - opened (1.0): a gh pr create the workspace ran printed its address;
//   - committed: the forge lists commits local Git links to the workspace, at
//     the best of those links' confidence. Links below the counted threshold
//     (outcomeGitCounted) do not make a pull request a workspace's.
//
// A workspace has one role per pull request, the stronger. Rows are written
// only where they changed. Pull requests a gh pr create printed get a row in
// pull_requests if indexing recorded none (it does only for some sources).
//
// It reads only the library, never a forge, so the analysis chain's tools
// stage runs it after every update, whether or not reading reviews is on:
// that is how a newly opened pull request becomes due, and how reviews a
// transfer brought reach the workspaces of the library that received them.
func (c *Catalog) refreshPullRequestWorkspaces(ctx context.Context) error {
	defer c.forgetPullRequestReviewsDue()
	remotes, err := c.repositoryRemotes(ctx)
	if err != nil {
		return err
	}
	type key struct{ workspace, pr string }
	want := map[key]pullRequestWorkspace{}
	type newPullRequest struct {
		id, host, repository, url string
		number                    int
	}
	created := map[string]newPullRequest{}
	opened, err := queryMapsContext(ctx, c.DB, "SELECT workspace_id,pull_requests_json FROM session_outcomes WHERE pull_requests_json NOT IN ('[]','null','')")
	if err != nil {
		return err
	}
	for _, row := range opened {
		var addresses []string
		_ = json.Unmarshal([]byte(firstString(row["pull_requests_json"])), &addresses)
		for _, address := range addresses {
			host, remote, _, number, ok := pullRequestPlace(address)
			repository := remotes[remote]
			if !ok || repository == "" {
				continue
			}
			id := stableID("pr", host, repository, number)
			created[id] = newPullRequest{id, host, repository, address, number}
			want[key{firstString(row["workspace_id"]), id}] = pullRequestWorkspace{"opened", 1, jsonText(map[string]any{"source": "gh pr create", "url": address})}
		}
	}
	committed, err := queryMapsContext(ctx, c.DB, `SELECT pc.pr_id,l.workspace_id,MAX(l.confidence) confidence,COUNT(DISTINCT pc.hash) commits,
		(SELECT COUNT(*) FROM pull_request_commits x WHERE x.pr_id=pc.pr_id) of
		FROM pull_request_commits pc JOIN pull_requests p ON p.id=pc.pr_id
		JOIN session_commit_links l ON l.repository_id=p.repository_id AND l.hash=pc.hash AND l.confidence>=?
		GROUP BY pc.pr_id,l.workspace_id`, outcomeGitCounted)
	if err != nil {
		return err
	}
	for _, row := range committed {
		k := key{firstString(row["workspace_id"]), firstString(row["pr_id"])}
		if _, has := want[k]; has {
			continue
		}
		confidence, _ := row["confidence"].(float64)
		want[k] = pullRequestWorkspace{"committed", confidence, jsonText(map[string]any{"source": "forge commits linked by local Git", "commits": integer(row["commits"]), "of": integer(row["of"])})}
	}
	stored, err := queryMapsContext(ctx, c.DB, "SELECT workspace_id,pr_id,role,confidence,evidence_json FROM pull_request_workspaces")
	if err != nil {
		return err
	}
	remove := []key{}
	for _, row := range stored {
		k := key{firstString(row["workspace_id"]), firstString(row["pr_id"])}
		confidence, _ := row["confidence"].(float64)
		if link, ok := want[k]; ok && link == (pullRequestWorkspace{firstString(row["role"]), confidence, firstString(row["evidence_json"])}) {
			delete(want, k)
		} else if !ok {
			remove = append(remove, k)
		}
	}
	existing, err := queryMapsContext(ctx, c.DB, "SELECT id FROM pull_requests")
	if err != nil {
		return err
	}
	for _, row := range existing {
		delete(created, firstString(row["id"]))
	}
	if len(want) == 0 && len(remove) == 0 && len(created) == 0 {
		return nil
	}
	return c.writeTransaction(ctx, "pull request workspaces", func(tx *sql.Tx) error {
		for _, pr := range created {
			if _, err := tx.Exec("INSERT OR IGNORE INTO pull_requests(id,host,repository_id,number,url,observed_at) VALUES(?,?,?,?,?,?)", pr.id, pr.host, pr.repository, pr.number, pr.url, now()); err != nil {
				return err
			}
		}
		for _, k := range remove {
			if _, err := tx.Exec("DELETE FROM pull_request_workspaces WHERE workspace_id=? AND pr_id=?", k.workspace, k.pr); err != nil {
				return err
			}
		}
		for k, link := range want {
			if _, err := tx.Exec(`INSERT INTO pull_request_workspaces(workspace_id,pr_id,role,confidence,evidence_json) VALUES(?,?,?,?,?)
				ON CONFLICT(workspace_id,pr_id) DO UPDATE SET role=excluded.role,confidence=excluded.confidence,evidence_json=excluded.evidence_json`, k.workspace, k.pr, link.Role, link.Confidence, link.Evidence); err != nil {
				return err
			}
		}
		return nil
	})
}

// duePullRequest is a pull request to read now.
type duePullRequest struct {
	ID, Host, Slug string
	Number         int
	fetched        time.Time
	own            bool
	observed       string
}

// pullRequestsDue lists the pull requests whose forge should be asked now,
// those never read first (a workspace's own before ones it only mentions,
// newest first), then the longest unread. A pull request qualifies when a
// workspace is linked to it, its address is in a repository of this library
// (so an address pasted from elsewhere is never looked up), and its host has
// a forge.
func (c *Catalog) pullRequestsDue(ctx context.Context, at time.Time) ([]duePullRequest, error) {
	remotes, err := c.repositoryRemotes(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := queryMapsContext(ctx, c.DB, `SELECT p.id,p.url,p.repository_id,COALESCE(p.observed_at,'') observed_at,COALESCE(f.status,'') status,COALESCE(f.fetched_at,'') fetched_at,
		COALESCE(d.state,'') state,COALESCE(d.merged_at,d.closed_at,'') closed_at,
		EXISTS(SELECT 1 FROM pull_request_workspaces pw WHERE pw.pr_id=p.id) own
		FROM pull_requests p LEFT JOIN pull_request_fetches f ON f.pr_id=p.id LEFT JOIN pull_request_details d ON d.pr_id=p.id
		WHERE p.url IS NOT NULL AND p.repository_id IS NOT NULL
		AND p.id IN (SELECT pr_id FROM pull_request_workspaces UNION SELECT pr_id FROM work_pr_links)`)
	if err != nil {
		return nil, err
	}
	due := []duePullRequest{}
	for _, row := range rows {
		host, remote, slug, number, ok := pullRequestPlace(firstString(row["url"]))
		if !ok || forges[host] == nil || remotes[remote] != firstString(row["repository_id"]) {
			continue
		}
		fetched, read := parseTime(firstString(row["fetched_at"]))
		if read {
			wait := forgeOpenRefresh
			switch status := firstString(row["status"]); {
			case status == "not_found":
				wait = forgeNotFoundRetry
			case status != "fetched":
				wait = forgeFailedRetry
			case firstString(row["state"]) != "open":
				closed, known := parseTime(firstString(row["closed_at"]))
				if known && fetched.Sub(closed) > forgeSettled {
					continue
				}
				wait = forgeClosedRefresh
			}
			if at.Sub(fetched) < wait {
				continue
			}
		}
		due = append(due, duePullRequest{ID: firstString(row["id"]), Host: host, Slug: slug, Number: number, fetched: fetched, own: integer(row["own"]) == 1, observed: firstString(row["observed_at"])})
	}
	sort.SliceStable(due, func(i, j int) bool {
		a, b := due[i], due[j]
		switch {
		case a.fetched.IsZero() != b.fetched.IsZero():
			return a.fetched.IsZero()
		case !a.fetched.Equal(b.fetched):
			return a.fetched.Before(b.fetched)
		case !a.fetched.IsZero():
			return a.ID < b.ID
		case a.own != b.own:
			return a.own
		case a.observed != b.observed:
			return a.observed > b.observed
		}
		return a.ID < b.ID
	})
	return due, nil
}

// PullRequestReviewStats is what one pass did.
type PullRequestReviewStats struct {
	Due      int `json:"due"`
	Read     int `json:"read"`
	Changed  int `json:"changed"`
	NotFound int `json:"not_found"`
	Failed   int `json:"failed"`
	// Waiting is why some forge could not be asked, such as gh signed out.
	Waiting string `json:"waiting,omitempty"`
	// Remaining is the pull requests left for later passes.
	Remaining int `json:"remaining"`
}

// RefreshPullRequestReviews derives workspaces' pull requests, asks their
// forges about those that are due, at most forgeRunLimit of them, and stores
// the answers. No write is open while a forge is asked. One pull request
// failing is recorded and retried later; a forge limiting requests ends the
// pass, as does a forge that cannot be asked.
func (c *Catalog) RefreshPullRequestReviews(ctx context.Context) (PullRequestReviewStats, error) {
	stats := PullRequestReviewStats{}
	defer c.forgetPullRequestReviewsDue()
	if err := c.refreshPullRequestWorkspaces(ctx); err != nil {
		return stats, err
	}
	due, err := c.pullRequestsDue(ctx, c.clock())
	if err != nil {
		return stats, err
	}
	stats.Due = len(due)
	batch := due
	if len(batch) > forgeRunLimit {
		batch = batch[:forgeRunLimit]
	}
	reportWork(ctx, "Reading pull request reviews", "pull request", 0, len(batch))
	done := 0
	for index, pr := range batch {
		if ctx.Err() != nil {
			break
		}
		source := forges[pr.Host]
		if reason := c.forgeUnavailable(ctx, pr.Host, index == 0); reason != "" {
			stats.Waiting = reason
			continue
		}
		request, cancel := context.WithTimeout(ctx, forgeRequestTimeout)
		answer, err := source.PullRequest(request, pr.Host, pr.Slug, pr.Number)
		cancel()
		if ctx.Err() != nil {
			break
		}
		if errors.Is(err, errForgeBusy) {
			stats.Waiting = err.Error()
			break
		}
		at := formatTime(c.clock())
		writeErr := c.writeTransaction(ctx, "pull request reviews", func(tx *sql.Tx) error {
			switch {
			case errors.Is(err, errForgeNotFound):
				_, e := tx.Exec(`INSERT INTO pull_request_fetches(pr_id,status,reason,fetched_at) VALUES(?,'not_found',NULL,?)
					ON CONFLICT(pr_id) DO UPDATE SET status='not_found',reason=NULL,fetched_at=excluded.fetched_at`, pr.ID, at)
				return e
			case err != nil:
				_, e := tx.Exec(`INSERT INTO pull_request_fetches(pr_id,status,reason,fetched_at) VALUES(?,'failed',?,?)
					ON CONFLICT(pr_id) DO UPDATE SET status='failed',reason=excluded.reason,fetched_at=excluded.fetched_at`, pr.ID, truncateText(err.Error(), 300), at)
				return e
			}
			changed, e := storePullRequest(tx, pr.ID, answer, at)
			if changed {
				stats.Changed++
			}
			return e
		})
		if writeErr != nil {
			return stats, writeErr
		}
		switch {
		case errors.Is(err, errForgeNotFound):
			stats.NotFound++
		case err != nil:
			stats.Failed++
		default:
			stats.Read++
		}
		done = index + 1
		reportWork(ctx, "Reading pull request reviews", "pull request", done, len(batch))
		if index+1 < len(batch) {
			select {
			case <-ctx.Done():
			case <-time.After(githubPause):
			}
		}
	}
	stats.Remaining = len(due) - stats.Read - stats.NotFound - stats.Failed
	if stats.Changed > 0 {
		// The commits a forge listed can make a pull request a workspace's.
		if err := c.refreshPullRequestWorkspaces(ctx); err != nil {
			return stats, err
		}
	}
	return stats, ctx.Err()
}

// forgeAvailabilityAge is how long an answer to "can this forge be asked"
// stands. Settings reads it on every poll of an open page, and each check
// starts the forge's tool.
var forgeAvailabilityAge = 5 * time.Minute

// forgeUnavailable is why host's forge cannot be asked ("" when it can),
// checked at most once in forgeAvailabilityAge unless fresh is set, as a
// pass sets it for its first pull request.
func (c *Catalog) forgeUnavailable(ctx context.Context, host string, fresh bool) string {
	source := forges[host]
	if source == nil {
		return "Pharos cannot read " + host
	}
	c.forgeAvailability.mu.Lock()
	defer c.forgeAvailability.mu.Unlock()
	at := time.Now()
	if known, ok := c.forgeAvailability.hosts[host]; ok && !fresh && at.Sub(known.at) < forgeAvailabilityAge {
		return known.reason
	}
	reason := source.Unavailable(ctx, host)
	if ctx.Err() != nil {
		return reason
	}
	if c.forgeAvailability.hosts == nil {
		c.forgeAvailability.hosts = map[string]forgeAvailable{}
	}
	c.forgeAvailability.hosts[host] = forgeAvailable{at, reason}
	return reason
}

type forgeAvailable struct {
	at     time.Time
	reason string
}

// pullRequestReviewsDue is how many pull requests a pass would read now. The
// schedule asks on every look at it, so the answer is kept for a minute, and
// until the next pass ends.
func (c *Catalog) pullRequestReviewsDue(ctx context.Context) int {
	c.reviewsDue.mu.Lock()
	defer c.reviewsDue.mu.Unlock()
	if at := c.clock(); c.reviewsDue.at.IsZero() || at.Sub(c.reviewsDue.at) > time.Minute || at.Before(c.reviewsDue.at) {
		due, err := c.pullRequestsDue(ctx, at)
		if err != nil {
			return 0
		}
		c.reviewsDue.at, c.reviewsDue.count = at, len(due)
	}
	return c.reviewsDue.count
}

// forgetPullRequestReviewsDue makes the next look count again.
func (c *Catalog) forgetPullRequestReviewsDue() {
	c.reviewsDue.mu.Lock()
	c.reviewsDue.at = time.Time{}
	c.reviewsDue.mu.Unlock()
}

// workspaceReviewsSQL is each workspace's own pull requests as their forge
// described them, folded to one row: a workspace almost always has one. The
// status is the strongest any of them drew, and the state the furthest any
// got (merged, else open, else closed).
var workspaceReviewsSQL = `SELECT pw.workspace_id,COUNT(*) pull_requests,
	CASE MAX(CASE d.state WHEN 'merged' THEN 3 WHEN 'open' THEN 2 ELSE 1 END) WHEN 3 THEN 'merged' WHEN 2 THEN 'open' ELSE 'closed' END pull_request_state,
	CASE MAX(CASE d.review_status WHEN 'changes_requested' THEN 3 WHEN 'approved' THEN 2 WHEN 'commented' THEN 1 ELSE 0 END)
		WHEN 3 THEN 'changes_requested' WHEN 2 THEN 'approved' WHEN 1 THEN 'commented' ELSE 'none' END review_status,
	CASE WHEN SUM(d.reviewer_kind IN ('human','both'))>0 AND SUM(d.reviewer_kind IN ('bot','both'))>0 THEN 'both'
		WHEN SUM(d.reviewer_kind IN ('human','both'))>0 THEN 'human' WHEN SUM(d.reviewer_kind IN ('bot','both'))>0 THEN 'bot' ELSE 'none' END reviewer_kind,
	SUM(d.reviews) reviews,SUM(d.comments) review_comments,SUM(d.unresolved_threads) unresolved_threads,SUM(d.commits_after_review) commits_after_review,
	MAX(d.review_status<>'none') reviewed,MAX(d.changes_requested>0) changes_requested,
	SUM((julianday(d.first_review_at)-julianday(d.created_at))*24.0) review_wait_hours
	FROM pull_request_workspaces pw JOIN pull_request_details d ON d.pr_id=pw.pr_id WHERE pw.confidence>=` + strconv.FormatFloat(outcomeGitCounted, 'f', -1, 64) + ` GROUP BY pw.workspace_id`

// pullRequestReviewsCoverage is what Settings says of the library's pull
// requests: how many are workspaces' own, how many of those were read, and
// what the last attempts ran into.
func (c *Catalog) pullRequestReviewsCoverage(ctx context.Context) (map[string]any, error) {
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT
		(SELECT COUNT(DISTINCT pr_id) FROM pull_request_workspaces) own,
		(SELECT COUNT(*) FROM pull_request_fetches WHERE status='fetched') fetched,
		(SELECT COUNT(*) FROM pull_request_fetches WHERE status='failed') failed,
		(SELECT COUNT(*) FROM pull_request_fetches WHERE status='not_found') not_found,
		(SELECT COUNT(*) FROM pull_request_reviews) reviews,
		(SELECT COUNT(*) FROM pull_request_comments) comments,
		(SELECT MAX(fetched_at) FROM pull_request_fetches) last_read_at`)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}
