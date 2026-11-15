# Pull request reviews

Pharos can read what a forge records about the pull requests your workspaces
produced: each review and its verdict, the comments on lines of the change, and
the pull request's conversation. It is Tier 3 of the
[outcomes evidence](projects-and-outcomes.md#tier-3-from-the-forge), and it is
off until you turn it on.

The code is `internal/archive/pull_request_reviews.go` (storage, links, the
pass), `forge_github.go` (GitHub) and `pull_request_reviews_settings.go` (the
switch).

## Turning it on

Settings → Background Processing → Pull request reviews. The switch saves
`pull_request_reviews_enabled` in the library's configuration and starts a
pass. Turning it off stops reading and keeps what was read.

Reading uses the `gh` command-line tool and its login on this Mac, as
repository identity does. Pharos keeps no token. Without `gh`, or signed out,
the switch says so and nothing is read. Whether `gh` is signed in is read from
its stored login (`gh auth token`), without the network, so being offline is
not reported as signed out. Settings reuses the answer for five minutes; a
pass checks afresh.

What is sent: the owner, name and number of each pull request to read. What is
stored: the forge's answer in full, including the text of reviews and comments
and their authors' logins. It is part of the library: backups copy it, and a
library transfer carries it. A forge's answer is taken or left whole: a pull
request the receiving library already holds details of keeps its own rows,
even if its latest read failed, and any other takes the incoming answer with
its fetch record. The receiving library works out which workspaces each pull
request belongs to itself (below), so transferred reviews show in Outcomes
there whether or not its switch is on.

This is the one place Pharos makes a network call for Git-related data.
Local Git still stores no commit messages, diffs or file contents; see
[What Pharos never does with git](projects-and-outcomes.md#what-pharos-never-does-with-git).

## The model

The model is the one GitHub, GitLab and Gitea share, and nothing outside
`forge_github.go` names a forge:

- A **pull request** has a state (open, merged, closed), an author, and
  commits.
- A **review** is one submission by a reviewer: a verdict (approved,
  changes requested, commented, dismissed) and an optional summary.
- A **comment** is either *inline*, on a line of a file and in a thread that
  can be resolved, usually as part of a review, or in the pull request's
  *conversation*.
- Authors are a *user* or a *bot*.

A forge is a Go value that answers `PullRequest(host, slug, number)`
(`forge` in `pull_request_reviews.go`), registered by host in `forges`. Only
`github.com` is registered. A pull request on a host with no forge is never
looked up.

| Table | Holds |
|---|---|
| `pull_request_details` | One row per pull request read: its facts and the review summary below |
| `pull_request_commits` | Its commits, in order |
| `pull_request_reviews` | Its reviews, with their text |
| `pull_request_comments` | Its comments, with their text, file, line and thread |
| `pull_request_fetches` | When each pull request was last read and how that went |
| `pull_request_workspaces` | Which pull requests are a workspace's own (derived) |

These tables have no foreign key to `pull_requests`. Indexing a workspace
again replaces its pull request rows under the same IDs, and the forge's
answer must outlive that. A repository merge, which changes the IDs, moves the
rows (`movePullRequestReviews`).

## Which pull requests are a workspace's own

`work_pr_links` records every pull request address that appears in a
workspace's messages. That is a mention, whatever relationship the row claims,
so it decides nothing here. `pull_request_workspaces` is derived from stronger
evidence:

| Role | Evidence | Confidence |
|---|---|---|
| Opened | A `gh pr create` the workspace ran printed the address | 1.0 |
| Committed | The forge lists commits that local Git links to the workspace | The best of those links |

A commit link below the counted threshold (0.7) does not make a pull request a
workspace's. A workspace has one role per pull request, the stronger. A
`gh pr create` address gets a `pull_requests` row if indexing recorded none,
which is how Claude Code and Codex workspaces get pull requests at all.

This derivation reads only the library, never a forge. The analysis chain's
tools stage runs it after every update, with the switch on or off
(`refreshPullRequestWorkspaces`). That is what makes a pull request a session
just opened due to be read, and a reading pass runs it again after the forge
lists commits.

A pull request with several workspaces gives each of them its whole review.
Comments are not divided among them.

## What is read, and when

A pull request is read when a workspace is linked to it (its own, or a
mention), its address is in a repository of this library, and its host has a
forge. An address pasted from someone else's repository is never looked up.

| State when last read | Read again |
|---|---|
| Never read | Next pass; a workspace's own first, newest first |
| Open | Hourly |
| Merged or closed | Daily, until it has been closed 30 days; then never |
| Not found (deleted, or not visible to this login) | After 7 days |
| Failed | After 6 hours |

A pass reads at most 150 pull requests, with a pause between requests, so a
first pass over a large library takes several runs. One request answers a
typical pull request; a long list takes further requests, up to 50. A thread
with over 100 comments is cut there and the pull request is marked truncated.
A forge limiting requests ends the pass. No write is open while a forge is
asked, and an answer identical to the last one only moves the fetch time, so
unchanged pull requests do not invalidate cached views.

The pass is the background item Pull request reviews (`forge.reviews`):
hourly, after the analysis chain, so the commit links it reads are current.

## The review summary

Each pull request's reviews are summarized when stored
(`summarizePullRequest`). Only other people's reviews and comments count: the
author's replies and self-review are not a review of the change. A bot's
review counts, and "reviewed by" says whether people, bots or both took part.

- **Review status** is the strongest verdict any reviewer gave: changes
  requested, else approved, else commented, else not reviewed. A pull request
  that drew a request for changes and was later approved reads as changes
  requested: the question is whether the change needed rework.
- **Review comments** counts inline and conversation comments by others.
- **Commits after first review** counts commits made after the first review
  or comment from someone else.

Comments an agent posts through the author's own login are the author's, and
are not counted.

## Where it shows

- **Analyze → Outcomes**: the Review, Reviewed by and Pull request breakdowns,
  and the measures Pull requests read, Reviews, Review comments, Review
  comments per pull request, Changes requested share and Commits after first
  review. A workspace with no pull request read is "No pull request read",
  never "not reviewed". The breakdowns are disabled, with the reason, until a
  pull request has been read.
- **MCP** `get_work_detail`: sections `pull_request_reviews` (one row per own
  pull request) and `review_comments` (reviews and comments in order, with
  their text).

Review facts never change an outcome label. Landing is still decided by local
Git.

## Not built yet

- Classifying comments by kind (correctness, design, style, tests and so on).
- A Work page view of a pull request's reviews.
- Reviews that are not on a forge: Conductor's diff comments, TL1's review
  findings.
- Attributing a comment to the workspace that wrote the line it is on.
- CI check results.
- The Library's pull request columns still count mentions (`work_pr_links`).
