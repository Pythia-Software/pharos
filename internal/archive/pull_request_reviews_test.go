package archive

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"
)

// fakeForge answers from a map of slug#number, and records what it was asked.
type fakeForge struct {
	unavailable string
	checks      int
	answers     map[string]*forgePullRequest
	failures    map[string]error
	asked       []string
}

func (f *fakeForge) Unavailable(context.Context, string) string {
	f.checks++
	return f.unavailable
}

func (f *fakeForge) PullRequest(_ context.Context, host, slug string, number int) (*forgePullRequest, error) {
	key := slug + "#" + string(rune('0'+number))
	f.asked = append(f.asked, key)
	if err := f.failures[key]; err != nil {
		return nil, err
	}
	if answer := f.answers[key]; answer != nil {
		return answer, nil
	}
	return nil, errForgeNotFound
}

func reviewedPullRequest() *forgePullRequest {
	return &forgePullRequest{State: "merged", Title: "Parse dates", Author: "ada", AuthorKind: "user",
		CreatedAt: "2026-11-01T10:00:00.000Z", MergedAt: "2026-11-02T10:00:00.000Z", ClosedAt: "2026-11-02T10:00:00.000Z",
		Commits: []forgeCommit{{Hash: strings.Repeat("a", 40), CommittedAt: "2026-11-01T09:00:00.000Z"}, {Hash: strings.Repeat("b", 40), CommittedAt: "2026-11-01T15:00:00.000Z"}},
		Reviews: []forgeReview{
			{ID: "r1", Author: "grace", AuthorKind: "user", State: "changes_requested", Body: "The parser drops time zones.", SubmittedAt: "2026-11-01T12:00:00.000Z"},
			{ID: "r2", Author: "grace", AuthorKind: "user", State: "approved", SubmittedAt: "2026-11-01T16:00:00.000Z"},
			{ID: "r3", Author: "ada", AuthorKind: "user", State: "commented", SubmittedAt: "2026-11-01T13:00:00.000Z"},
		},
		Comments: []forgeComment{
			{ID: "c1", Kind: "inline", ReviewID: "r1", ThreadID: "t1", Author: "grace", AuthorKind: "user", Body: "Keep the offset here.", Path: "dates.go", Line: 12, Resolved: true, CreatedAt: "2026-11-01T12:00:00.000Z"},
			{ID: "c2", Kind: "inline", ThreadID: "t1", ReplyTo: "c1", Author: "ada", AuthorKind: "user", Body: "Done.", Path: "dates.go", Line: 12, Resolved: true, CreatedAt: "2026-11-01T15:05:00.000Z"},
			{ID: "c3", Kind: "inline", ThreadID: "t2", Author: "lint-bot", AuthorKind: "bot", Body: "Unused import.", Path: "dates.go", Line: 3, CreatedAt: "2026-11-01T11:00:00.000Z"},
			{ID: "c4", Kind: "conversation", Author: "ada", AuthorKind: "user", Body: "Ready for another look.", CreatedAt: "2026-11-01T15:10:00.000Z"},
		}}
}

func TestSummarizePullRequestCountsOnlyOtherPeople(t *testing.T) {
	got := summarizePullRequest(reviewedPullRequest())
	want := pullRequestSummary{Status: "changes_requested", ReviewerKind: "both", Reviews: 2, ChangesRequested: 1, Approvals: 1, Comments: 2, Threads: 2, Unresolved: 1,
		FirstReviewAt: "2026-11-01T11:00:00.000Z", CommitsAfter: 1}
	if got != want {
		t.Fatalf("summary:\n got %+v\nwant %+v", got, want)
	}
	// The author's own replies and self-review are not a review.
	alone := reviewedPullRequest()
	alone.Reviews, alone.Comments = alone.Reviews[2:], alone.Comments[3:]
	if got := summarizePullRequest(alone); got.Status != "none" || got.ReviewerKind != "none" || got.Reviews != 0 || got.Comments != 0 || got.FirstReviewAt != "" {
		t.Fatalf("a pull request only its author wrote on reads as reviewed: %+v", got)
	}
	approved := reviewedPullRequest()
	approved.Reviews, approved.Comments = approved.Reviews[1:2], nil
	if got := summarizePullRequest(approved); got.Status != "approved" || got.ReviewerKind != "human" {
		t.Fatalf("approved: %+v", got)
	}
}

func TestGitHubAnswerReadsPagesAndProblems(t *testing.T) {
	first := `{"data":{"repository":{"pullRequest":{"state":"OPEN","isDraft":true,"title":"T","createdAt":"2026-11-01T10:00:00Z","baseRefName":"main","headRefName":"fix","additions":3,"deletions":1,"changedFiles":2,
		"reviewDecision":"CHANGES_REQUESTED","author":{"__typename":"User","login":"ada"},
		"commits":{"pageInfo":{"hasNextPage":false,"endCursor":"c"},"nodes":[{"commit":{"oid":"ABCDEF","committedDate":"2026-11-01T09:00:00Z"}}]},
		"reviews":{"pageInfo":{"hasNextPage":false,"endCursor":"r"},"nodes":[{"id":"R1","state":"CHANGES_REQUESTED","body":"No.","submittedAt":"2026-11-01T12:00:00Z","author":{"__typename":"Bot","login":"reviewer"},"commit":{"oid":"ABCDEF"}}]},
		"reviewThreads":{"pageInfo":{"hasNextPage":true,"endCursor":"t1"},"nodes":[{"id":"T1","isResolved":true,"isOutdated":false,"path":"a.go","line":null,"originalLine":7,
			"comments":{"pageInfo":{"hasNextPage":true},"nodes":[{"id":"C1","body":"Why?","createdAt":"2026-11-01T12:00:00Z","author":null,"replyTo":null,"pullRequestReview":{"id":"R1"}}]}}]},
		"comments":{"pageInfo":{"hasNextPage":false,"endCursor":"i"},"nodes":[{"id":"I1","body":"Thanks","createdAt":"2026-11-01T13:00:00Z","author":{"__typename":"User","login":"ada"}}]}}}}}`
	page, err := readGitHubAnswer([]byte(first), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	pr := &forgePullRequest{}
	next := addGitHubPage(pr, page, true)
	if !next.more() || !next.wantThreads || next.threads != "t1" || next.wantCommits || next.wantReviews || next.wantComments {
		t.Fatalf("only the list with more is asked for again: %+v", next)
	}
	if pr.State != "open" || !pr.Draft || pr.Author != "ada" || pr.Decision != "changes_requested" || pr.Commits[0].Hash != "abcdef" || !pr.Truncated {
		t.Fatalf("pull request: %+v", pr)
	}
	if review := pr.Reviews[0]; review.State != "changes_requested" || review.AuthorKind != "bot" || review.SubmittedAt != "2026-11-01T12:00:00.000Z" {
		t.Fatalf("review: %+v", review)
	}
	if len(pr.Comments) != 2 || pr.Comments[0].Kind != "inline" || pr.Comments[0].Line != 7 || pr.Comments[0].ReviewID != "R1" || !pr.Comments[0].Resolved || pr.Comments[0].Author != "" || pr.Comments[1].Kind != "conversation" {
		t.Fatalf("comments: %+v", pr.Comments)
	}
	// A later page adds to its list and leaves the pull request's facts.
	second, err := readGitHubAnswer([]byte(`{"data":{"repository":{"pullRequest":{"state":"MERGED","reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[{"id":"T2","path":"b.go","line":2,
		"comments":{"pageInfo":{"hasNextPage":false},"nodes":[{"id":"C2","body":"Nit","author":{"__typename":"User","login":"grace"}}]}}]}}}}}`), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if next := addGitHubPage(pr, second, false); next.more() || pr.State != "open" || len(pr.Comments) != 3 {
		t.Fatalf("second page: more=%v state=%s comments=%d", next.more(), pr.State, len(pr.Comments))
	}
	for name, test := range map[string]struct {
		output, stderr string
		failed         error
		want           error
	}{
		"missing":      {`{"data":{"repository":{"pullRequest":null}},"errors":[{"type":"NOT_FOUND","message":"Could not resolve to a PullRequest"}]}`, "", errors.New("exit 1"), errForgeNotFound},
		"no access":    {`{"data":{"repository":null}}`, "", nil, errForgeNotFound},
		"rate limited": {`{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`, "", errors.New("exit 1"), errForgeBusy},
		"secondary":    {``, "You have exceeded a secondary rate limit", errors.New("exit 1"), errForgeBusy},
	} {
		if _, err := readGitHubAnswer([]byte(test.output), test.stderr, test.failed); !errors.Is(err, test.want) {
			t.Errorf("%s: %v, want %v", name, err, test.want)
		}
	}
	if _, err := readGitHubAnswer(nil, "gh: Bad credentials", errors.New("exit 1")); err == nil || !strings.Contains(err.Error(), "Bad credentials") {
		t.Fatalf("a failure keeps gh's words: %v", err)
	}
}

// reviewFixture is a library with one repository on GitHub, a workspace that
// opened pull request 1 there, one that committed to pull request 2, and one
// that only mentioned pull request 3 and one in a repository not in the
// library.
func reviewFixture(t *testing.T) (*Catalog, *fakeForge, func(number int) string) {
	t.Helper()
	catalog, _ := testCatalog(t)
	id := func(number int) string { return stableID("pr", "github.com", "repo", number) }
	execTx(t, catalog.DB,
		`INSERT INTO repositories(id,canonical_remote,normalized_remote,display_name,created_at,updated_at) VALUES('repo','https://github.com/Acme/App.git','github.com/acme/app','app','t','t')`,
		`INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at,repository_id) VALUES
			('opener','claude','local','opener','Parse dates','t','repo'),('committer','codex','local','committer','Fix lint','t','repo'),('reader','conductor','local','reader','Review','t','repo')`,
		`INSERT INTO session_outcomes(workspace_id,tool_calls_recorded,files_changed,rework_days,pull_requests_json) VALUES
			('opener',4,1,14,'["https://github.com/Acme/App/pull/1","https://github.com/elsewhere/tool/pull/9"]'),('committer',4,1,14,'[]')`,
		`INSERT INTO pull_requests(id,host,repository_id,number,url,observed_at) VALUES
			('`+id(2)+`','github.com','repo',2,'https://github.com/acme/app/pull/2','2026-11-02T00:00:00.000Z'),
			('`+id(3)+`','github.com','repo',3,'https://github.com/acme/app/pull/3','2026-11-03T00:00:00.000Z'),
			('`+id(7)+`','github.com','repo',7,'https://github.com/elsewhere/tool/pull/7','2026-11-04T00:00:00.000Z')`,
		`INSERT INTO work_pr_links(workspace_id,pr_id,relationship,confidence,evidence_json) VALUES
			('reader','`+id(2)+`','created',1,'{}'),('reader','`+id(3)+`','created',1,'{}'),('reader','`+id(7)+`','created',1,'{}')`,
		`INSERT INTO session_commit_links(workspace_id,repository_id,hash,method,confidence,evidence_json) VALUES
			('committer','repo','`+strings.Repeat("c", 40)+`','message',0.95,'{}'),('reader','repo','`+strings.Repeat("c", 40)+`','nearest',0.5,'{}')`)
	second := &forgePullRequest{State: "open", Author: "ada", AuthorKind: "user", CreatedAt: "2026-11-02T10:00:00.000Z",
		Commits: []forgeCommit{{Hash: strings.Repeat("C", 40)}}, Reviews: []forgeReview{}, Comments: []forgeComment{}}
	source := &fakeForge{answers: map[string]*forgePullRequest{"Acme/App#1": reviewedPullRequest(), "acme/app#2": second}}
	override(t, &forges, map[string]forge{"github.com": source})
	override(t, &githubPause, 0)
	return catalog, source, id
}

func TestRefreshPullRequestReviewsReadsAWorkspacesOwnPullRequests(t *testing.T) {
	catalog, source, id := reviewFixture(t)
	at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	catalog.now = func() time.Time { return at }
	ctx := context.Background()
	stats, err := catalog.RefreshPullRequestReviews(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Every linked pull request in a repository of the library is read, the
	// workspace's own first; the address in another repository never is.
	if strings.Join(source.asked, " ") != "Acme/App#1 acme/app#3 acme/app#2" || stats.Read != 2 || stats.NotFound != 1 || stats.Changed != 2 || stats.Remaining != 0 {
		t.Fatalf("asked %v, stats %+v", source.asked, stats)
	}
	links, err := queryMaps(catalog.DB, "SELECT workspace_id,pr_id,role,confidence FROM pull_request_workspaces ORDER BY workspace_id")
	if err != nil {
		t.Fatal(err)
	}
	// gh pr create makes a pull request the opener's; the forge's commits
	// make one the committer's. A mention, or a commit link too weak to
	// count, makes it nobody's.
	if len(links) != 2 || links[0]["workspace_id"] != "committer" || links[0]["pr_id"] != id(2) || links[0]["role"] != "committed" || links[0]["confidence"] != 0.95 ||
		links[1]["workspace_id"] != "opener" || links[1]["pr_id"] != id(1) || links[1]["role"] != "opened" {
		t.Fatalf("links: %#v", links)
	}
	var state, status, body, verdict string
	var comments int
	if err := catalog.DB.QueryRow(`SELECT d.state,d.review_status,d.comments,(SELECT body FROM pull_request_comments WHERE pr_id=d.pr_id AND comment_id='c1'),
		(SELECT state FROM pull_request_reviews WHERE pr_id=d.pr_id AND review_id='r1') FROM pull_request_details d WHERE d.pr_id=?`, id(1)).Scan(&state, &status, &comments, &body, &verdict); err != nil {
		t.Fatal(err)
	}
	if state != "merged" || status != "changes_requested" || comments != 2 || body != "Keep the offset here." || verdict != "changes_requested" {
		t.Fatalf("stored: %s %s %d %q %s", state, status, comments, body, verdict)
	}

	// The Outcomes dataset carries the review beside the label; a workspace
	// with no pull request read has no review fields, not zeros.
	rows, err := catalog.outcomeRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, row := range rows {
		byID[firstString(row["id"])] = row
	}
	if row := byID["opener"]; integer(row["pull_requests"]) != 1 || row["pull_request_state"] != "merged" || row["review_status"] != "changes_requested" || row["reviewer_kind"] != "both" ||
		integer(row["review_comments"]) != 2 || integer(row["reviews"]) != 2 || integer(row["changes_requested"]) != 1 || integer(row["reviewed"]) != 1 || integer(row["commits_after_review"]) != 1 {
		t.Fatalf("opener's row: %#v", row)
	}
	if wait, _ := byID["opener"]["review_wait_hours"].(float64); wait < 0.99 || wait > 1.01 {
		t.Fatalf("hours to first review: %v", byID["opener"]["review_wait_hours"])
	}
	if row := byID["committer"]; row["review_status"] != "none" || integer(row["reviewed"]) != 0 || row["pull_request_state"] != "open" || row["review_wait_hours"] != nil {
		t.Fatalf("committer's row: %#v", row)
	}
	if row := byID["reader"]; row["pull_requests"] != nil || row["review_status"] != nil {
		t.Fatalf("a workspace that only mentioned pull requests has review fields: %#v", row)
	}
	detail, err := catalog.boundedWorkDetail(map[string]any{"workspace_id": "opener", "section": "review_comments"})
	if err != nil || integer(detail["total"]) != 7 {
		t.Fatalf("review_comments: %#v, %v", detail, err)
	}

	// Nothing is due again at once. An hour on, the open pull request is read
	// again, and an answer that says the same writes no view's table.
	if due, _ := catalog.pullRequestsDue(ctx, at.Add(time.Minute)); len(due) != 0 {
		t.Fatalf("due a minute later: %+v", due)
	}
	execTx(t, catalog.DB, `CREATE TABLE detail_writes(n)`, `CREATE TRIGGER detail_written AFTER INSERT ON pull_request_details BEGIN INSERT INTO detail_writes VALUES(1); END`)
	at = at.Add(2 * time.Hour)
	source.asked = nil
	stats, err = catalog.RefreshPullRequestReviews(ctx)
	if err != nil || strings.Join(source.asked, " ") != "acme/app#2" || stats.Changed != 0 {
		t.Fatalf("an hour later asked %v, stats %+v, %v", source.asked, stats, err)
	}
	var writes int
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM detail_writes").Scan(&writes); err != nil || writes != 0 {
		t.Fatalf("an unchanged answer rewrote details %d times: %v", writes, err)
	}
	// A merged pull request is read daily until it settles, then never; one
	// the forge did not have is tried again after a week.
	for _, test := range []struct {
		after time.Duration
		want  string
	}{{25 * time.Hour, "Acme/App#1 acme/app#2"}, {8 * 24 * time.Hour, "Acme/App#1 acme/app#2 acme/app#3"}} {
		due, err := catalog.pullRequestsDue(ctx, time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).Add(test.after))
		if err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, pr := range due {
			names = append(names, pr.Slug+"#"+string(rune('0'+pr.Number)))
		}
		sort.Strings(names)
		if strings.Join(names, " ") != test.want {
			t.Errorf("due after %s: %v, want %s", test.after, names, test.want)
		}
	}
	at = time.Date(2026, 12, 20, 12, 0, 0, 0, time.UTC)
	source.asked = nil
	if _, err := catalog.RefreshPullRequestReviews(ctx); err != nil {
		t.Fatal(err)
	}
	at = at.Add(48 * time.Hour)
	if due, _ := catalog.pullRequestsDue(ctx, at); len(due) != 1 || due[0].Number != 2 {
		t.Fatalf("a pull request merged over a month before its last read is settled: %+v", due)
	}
}

func TestRefreshPullRequestReviewsWaitsForItsForge(t *testing.T) {
	catalog, source, id := reviewFixture(t)
	ctx := context.Background()
	// The pull request a gh pr create printed has no row until workspaces'
	// pull requests are derived, which the analysis chain does without a
	// forge: only then can the schedule find it due and start a pass.
	if due := catalog.pullRequestReviewsDue(ctx); due != 2 {
		t.Fatalf("before the chain derived pull requests, %d are due", due)
	}
	if err := catalog.refreshPullRequestWorkspaces(ctx); err != nil {
		t.Fatal(err)
	}
	if due := catalog.pullRequestReviewsDue(ctx); due != 3 || len(source.asked) != 0 {
		t.Fatalf("after the chain derived pull requests, %d are due and the forge was asked %v", due, source.asked)
	}
	// A forge that cannot be asked is not asked, and nothing is recorded
	// against the pull requests. Settings, which reads the reason on every
	// poll, does not start the forge's tool each time; a pass checks afresh.
	source.unavailable = "gh is not signed in"
	for range 3 {
		if reason := catalog.forgeUnavailable(ctx, "github.com", false); reason != "gh is not signed in" {
			t.Fatalf("reason: %q", reason)
		}
	}
	if source.checks != 1 {
		t.Fatalf("three reads of the reason checked the forge %d times", source.checks)
	}
	stats, err := catalog.RefreshPullRequestReviews(ctx)
	if source.checks != 2 {
		t.Fatalf("a pass did not check the forge afresh: %d checks", source.checks)
	}
	if err != nil || len(source.asked) != 0 || stats.Waiting != "gh is not signed in" || stats.Remaining != 3 {
		t.Fatalf("unavailable: asked %v, stats %+v, %v", source.asked, stats, err)
	}
	// One pull request failing is recorded and the rest are read; a forge
	// limiting requests ends the pass with the rest still due.
	source.unavailable = ""
	source.failures = map[string]error{"Acme/App#1": errors.New("HTTP 502"), "acme/app#2": errForgeBusy}
	stats, err = catalog.RefreshPullRequestReviews(ctx)
	if err != nil || stats.Failed != 1 || stats.NotFound != 1 || stats.Read != 0 || stats.Remaining != 1 || !strings.Contains(stats.Waiting, "limiting") {
		t.Fatalf("failures: asked %v, stats %+v, %v", source.asked, stats, err)
	}
	var status, reason string
	if err := catalog.DB.QueryRow("SELECT status,reason FROM pull_request_fetches WHERE pr_id=?", id(1)).Scan(&status, &reason); err != nil || status != "failed" || reason != "HTTP 502" {
		t.Fatalf("failure record: %s %q %v", status, reason, err)
	}
	if due := catalog.pullRequestReviewsDue(ctx); due != 1 {
		t.Fatalf("after a failure %d are due, want only the one not yet asked", due)
	}
}
