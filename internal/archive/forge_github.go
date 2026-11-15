package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// githubForge reads pull requests from GitHub through the gh tool and its
// login, as repository identity does (repository_identity.go): Pharos keeps
// no token of its own. One GraphQL request answers a typical pull request;
// further requests follow only the lists that had more.
type githubForge struct{}

// Unavailable asks gh for its stored login, which it answers without the
// network (gh auth status validates the token with GitHub, so it fails
// offline and takes as long as the request does).
func (githubForge) Unavailable(ctx context.Context, host string) string {
	binary, ok := githubBinary()
	if !ok {
		return "the GitHub command-line tool (gh) is not installed on this Mac"
	}
	request, cancel := context.WithTimeout(ctx, githubTimeout)
	defer cancel()
	if output, err := exec.CommandContext(request, binary, "auth", "token", "--hostname", host).Output(); err != nil || strings.TrimSpace(string(output)) == "" {
		return "the GitHub command-line tool (gh) is not signed in to " + host + " on this Mac"
	}
	return ""
}

const githubPullRequestQuery = `query($owner:String!,$name:String!,$number:Int!,$commitsAfter:String,$reviewsAfter:String,$threadsAfter:String,$commentsAfter:String,$wantCommits:Boolean!,$wantReviews:Boolean!,$wantThreads:Boolean!,$wantComments:Boolean!){
repository(owner:$owner,name:$name){pullRequest(number:$number){
state isDraft title createdAt mergedAt closedAt updatedAt baseRefName headRefName additions deletions changedFiles reviewDecision
author{__typename login}
commits(first:100,after:$commitsAfter) @include(if:$wantCommits){pageInfo{hasNextPage endCursor} nodes{commit{oid committedDate}}}
reviews(first:100,after:$reviewsAfter) @include(if:$wantReviews){pageInfo{hasNextPage endCursor} nodes{id state body submittedAt url author{__typename login} commit{oid}}}
reviewThreads(first:50,after:$threadsAfter) @include(if:$wantThreads){pageInfo{hasNextPage endCursor} nodes{id isResolved isOutdated path line originalLine
comments(first:100){pageInfo{hasNextPage} nodes{id body createdAt url author{__typename login} replyTo{id} pullRequestReview{id}}}}}
comments(first:100,after:$commentsAfter) @include(if:$wantComments){pageInfo{hasNextPage endCursor} nodes{id body createdAt url author{__typename login}}}
}}}`

type githubActor struct {
	Type  string `json:"__typename"`
	Login string `json:"login"`
}

// kind is user or bot. A deleted account has no author.
func (actor *githubActor) read() (login, kind string) {
	if actor == nil {
		return "", ""
	}
	if actor.Type == "Bot" {
		return actor.Login, "bot"
	}
	return actor.Login, "user"
}

type githubPage struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type githubPullRequest struct {
	State          string       `json:"state"`
	IsDraft        bool         `json:"isDraft"`
	Title          string       `json:"title"`
	CreatedAt      string       `json:"createdAt"`
	MergedAt       string       `json:"mergedAt"`
	ClosedAt       string       `json:"closedAt"`
	UpdatedAt      string       `json:"updatedAt"`
	BaseRefName    string       `json:"baseRefName"`
	HeadRefName    string       `json:"headRefName"`
	Additions      int          `json:"additions"`
	Deletions      int          `json:"deletions"`
	ChangedFiles   int          `json:"changedFiles"`
	ReviewDecision string       `json:"reviewDecision"`
	Author         *githubActor `json:"author"`
	Commits        *struct {
		PageInfo githubPage `json:"pageInfo"`
		Nodes    []struct {
			Commit struct {
				OID           string `json:"oid"`
				CommittedDate string `json:"committedDate"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	Reviews *struct {
		PageInfo githubPage `json:"pageInfo"`
		Nodes    []struct {
			ID          string       `json:"id"`
			State       string       `json:"state"`
			Body        string       `json:"body"`
			SubmittedAt string       `json:"submittedAt"`
			URL         string       `json:"url"`
			Author      *githubActor `json:"author"`
			Commit      *struct {
				OID string `json:"oid"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"reviews"`
	ReviewThreads *struct {
		PageInfo githubPage `json:"pageInfo"`
		Nodes    []struct {
			ID           string `json:"id"`
			IsResolved   bool   `json:"isResolved"`
			IsOutdated   bool   `json:"isOutdated"`
			Path         string `json:"path"`
			Line         int    `json:"line"`
			OriginalLine int    `json:"originalLine"`
			Comments     struct {
				PageInfo githubPage `json:"pageInfo"`
				Nodes    []struct {
					ID        string       `json:"id"`
					Body      string       `json:"body"`
					CreatedAt string       `json:"createdAt"`
					URL       string       `json:"url"`
					Author    *githubActor `json:"author"`
					ReplyTo   *struct {
						ID string `json:"id"`
					} `json:"replyTo"`
					PullRequestReview *struct {
						ID string `json:"id"`
					} `json:"pullRequestReview"`
				} `json:"nodes"`
			} `json:"comments"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
	Comments *struct {
		PageInfo githubPage `json:"pageInfo"`
		Nodes    []struct {
			ID        string       `json:"id"`
			Body      string       `json:"body"`
			CreatedAt string       `json:"createdAt"`
			URL       string       `json:"url"`
			Author    *githubActor `json:"author"`
		} `json:"nodes"`
	} `json:"comments"`
}

type githubAnswer struct {
	Data struct {
		Repository *struct {
			PullRequest *githubPullRequest `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"errors"`
}

// githubCursors is where each list of a pull request continues; a list that
// is finished is not asked for again.
type githubCursors struct {
	commits, reviews, threads, comments string
	wantCommits, wantReviews            bool
	wantThreads, wantComments           bool
}

func (cursors githubCursors) more() bool {
	return cursors.wantCommits || cursors.wantReviews || cursors.wantThreads || cursors.wantComments
}

// githubTime is a GitHub timestamp as Pharos stores times.
func githubTime(value string) string {
	if at, ok := parseTime(value); ok {
		return formatTime(at)
	}
	return ""
}

// addGitHubPage adds one answer's lists to pr and returns where to continue.
// The first page also gives the pull request's own facts.
func addGitHubPage(pr *forgePullRequest, page *githubPullRequest, first bool) githubCursors {
	next := githubCursors{}
	if first {
		pr.State, pr.Draft, pr.Title = strings.ToLower(page.State), page.IsDraft, page.Title
		pr.Author, pr.AuthorKind = page.Author.read()
		pr.CreatedAt, pr.MergedAt, pr.ClosedAt, pr.UpdatedAt = githubTime(page.CreatedAt), githubTime(page.MergedAt), githubTime(page.ClosedAt), githubTime(page.UpdatedAt)
		pr.BaseRef, pr.HeadRef = page.BaseRefName, page.HeadRefName
		pr.Additions, pr.Deletions, pr.ChangedFiles = page.Additions, page.Deletions, page.ChangedFiles
		pr.Decision = strings.ToLower(page.ReviewDecision)
	}
	if list := page.Commits; list != nil {
		for _, node := range list.Nodes {
			pr.Commits = append(pr.Commits, forgeCommit{Hash: strings.ToLower(node.Commit.OID), CommittedAt: githubTime(node.Commit.CommittedDate)})
		}
		next.wantCommits, next.commits = list.PageInfo.HasNextPage, list.PageInfo.EndCursor
	}
	if list := page.Reviews; list != nil {
		for _, node := range list.Nodes {
			review := forgeReview{ID: node.ID, State: strings.ToLower(node.State), Body: node.Body, SubmittedAt: githubTime(node.SubmittedAt), URL: node.URL}
			review.Author, review.AuthorKind = node.Author.read()
			if node.Commit != nil {
				review.Commit = strings.ToLower(node.Commit.OID)
			}
			pr.Reviews = append(pr.Reviews, review)
		}
		next.wantReviews, next.reviews = list.PageInfo.HasNextPage, list.PageInfo.EndCursor
	}
	if list := page.ReviewThreads; list != nil {
		for _, thread := range list.Nodes {
			line := thread.Line
			if line == 0 {
				line = thread.OriginalLine
			}
			pr.Truncated = pr.Truncated || thread.Comments.PageInfo.HasNextPage
			for _, node := range thread.Comments.Nodes {
				comment := forgeComment{ID: node.ID, Kind: "inline", ThreadID: thread.ID, Body: node.Body, Path: thread.Path, Line: line,
					Resolved: thread.IsResolved, Outdated: thread.IsOutdated, CreatedAt: githubTime(node.CreatedAt), URL: node.URL}
				comment.Author, comment.AuthorKind = node.Author.read()
				if node.ReplyTo != nil {
					comment.ReplyTo = node.ReplyTo.ID
				}
				if node.PullRequestReview != nil {
					comment.ReviewID = node.PullRequestReview.ID
				}
				pr.Comments = append(pr.Comments, comment)
			}
		}
		next.wantThreads, next.threads = list.PageInfo.HasNextPage, list.PageInfo.EndCursor
	}
	if list := page.Comments; list != nil {
		for _, node := range list.Nodes {
			comment := forgeComment{ID: node.ID, Kind: "conversation", Body: node.Body, CreatedAt: githubTime(node.CreatedAt), URL: node.URL}
			comment.Author, comment.AuthorKind = node.Author.read()
			pr.Comments = append(pr.Comments, comment)
		}
		next.wantComments, next.comments = list.PageInfo.HasNextPage, list.PageInfo.EndCursor
	}
	return next
}

// githubPages bounds the requests one pull request takes: 50 pages is five
// thousand of its longest list.
const githubPages = 50

func (githubForge) PullRequest(ctx context.Context, host, slug string, number int) (*forgePullRequest, error) {
	binary, ok := githubBinary()
	if !ok {
		return nil, errors.New("gh is not installed")
	}
	owner, name, ok := strings.Cut(slug, "/")
	if !ok {
		return nil, fmt.Errorf("%q is not owner/name", slug)
	}
	pr := &forgePullRequest{Commits: []forgeCommit{}, Reviews: []forgeReview{}, Comments: []forgeComment{}}
	cursors := githubCursors{wantCommits: true, wantReviews: true, wantThreads: true, wantComments: true}
	for page := 0; cursors.more(); page++ {
		if page == githubPages {
			pr.Truncated = true
			break
		}
		args := []string{"api", "graphql", "--hostname", host, "-f", "query=" + githubPullRequestQuery, "-f", "owner=" + owner, "-f", "name=" + name, "-F", fmt.Sprintf("number=%d", number),
			"-F", fmt.Sprintf("wantCommits=%t", cursors.wantCommits), "-F", fmt.Sprintf("wantReviews=%t", cursors.wantReviews),
			"-F", fmt.Sprintf("wantThreads=%t", cursors.wantThreads), "-F", fmt.Sprintf("wantComments=%t", cursors.wantComments)}
		for key, cursor := range map[string]string{"commitsAfter": cursors.commits, "reviewsAfter": cursors.reviews, "threadsAfter": cursors.threads, "commentsAfter": cursors.comments} {
			if cursor != "" {
				args = append(args, "-f", key+"="+cursor)
			}
		}
		command := exec.CommandContext(ctx, binary, args...)
		var stderr bytes.Buffer
		command.Stderr = &stderr
		// gh prints GraphQL's answer and exits non-zero when it holds errors.
		output, runErr := command.Output()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		answer, err := readGitHubAnswer(output, stderr.String(), runErr)
		if err != nil {
			return nil, err
		}
		cursors = addGitHubPage(pr, answer, page == 0)
	}
	return pr, nil
}

// readGitHubAnswer reads one gh api graphql result: the pull request, or why
// there is none.
func readGitHubAnswer(output []byte, stderr string, runErr error) (*githubPullRequest, error) {
	var answer githubAnswer
	parseErr := json.Unmarshal(output, &answer)
	for _, problem := range answer.Errors {
		switch {
		case problem.Type == "NOT_FOUND":
			return nil, errForgeNotFound
		case problem.Type == "RATE_LIMITED" || strings.Contains(strings.ToLower(problem.Message), "rate limit"):
			return nil, errForgeBusy
		}
	}
	if lower := strings.ToLower(stderr); strings.Contains(lower, "rate limit") {
		return nil, errForgeBusy
	} else if strings.Contains(lower, "could not resolve to a") {
		return nil, errForgeNotFound
	}
	if len(answer.Errors) > 0 {
		return nil, errors.New(answer.Errors[0].Message)
	}
	if runErr != nil {
		if message := strings.TrimSpace(stderr); message != "" {
			return nil, errors.New(message)
		}
		return nil, runErr
	}
	if parseErr != nil {
		return nil, fmt.Errorf("reading GitHub's answer: %w", parseErr)
	}
	if answer.Data.Repository == nil || answer.Data.Repository.PullRequest == nil {
		return nil, errForgeNotFound
	}
	return answer.Data.Repository.PullRequest, nil
}
