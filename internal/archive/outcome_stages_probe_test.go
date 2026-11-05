//go:build probe

// Runs the background outcome, commit-linking and spend-by-branch stages on
// a dev copy of a library (never production), the way a sync's tools stage
// does, and times them; then measures each way of linking a commit against
// the hashes sessions printed:
//
//	PHAROS_STAGE_HOST=host_… PHAROS_STAGE_CATALOG=/Volumes/euclid/pharos-dev-outcomes/catalog/catalog.sqlite3 PHAROS_TEST_PROCESSES=1 \
//	  go test -tags probe -run 'TestOutcomeStagesProbe|TestCommitLinkCalibrationProbe' -v -timeout 120m ./internal/archive
//
// Git runs only read-only commands, in the checkouts this Mac recorded.

package archive

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// stageCatalog opens the dev copy as this Mac.
func stageCatalog(t *testing.T) *Catalog {
	path := os.Getenv("PHAROS_STAGE_CATALOG")
	if path == "" || strings.Contains(path, "/Volumes/euclid/Pharos/") {
		t.Skip("set PHAROS_STAGE_CATALOG to a dev copy, never the live catalog")
	}
	// TestMain stands in a template host and support directory; the stages
	// must run as this Mac, which recorded the checkouts: PHAROS_STAGE_HOST.
	host := Host{ID: os.Getenv("PHAROS_STAGE_HOST")}
	if host.ID == "" {
		t.Skip("set PHAROS_STAGE_HOST to this Mac's host ID")
	}
	override(t, &currentHost, func() Host { return host })
	t.Logf("host %s", host.ID)
	catalog, err := OpenCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { catalog.Close() })
	return catalog
}

func TestOutcomeStagesProbe(t *testing.T) {
	catalog := stageCatalog(t)
	ctx := context.Background()
	if _, err := catalog.DB.Exec(`DELETE FROM git_repository_scans WHERE host_id IN ('host_template','host_pharos_test')`); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []struct {
		name string
		run  func(context.Context) error
	}{{"outcomes", catalog.refreshSessionOutcomes}, {"commit links", catalog.refreshSessionGit}, {"spend by branch", catalog.refreshBranchUsage}} {
		started := time.Now()
		if err := stage.run(ctx); err != nil {
			t.Fatalf("%s: %v", stage.name, err)
		}
		t.Logf("%s: %s", stage.name, time.Since(started).Round(time.Second))
	}
}

// TestCommitLinkCalibrationProbe links each repository's commits with the
// printed hashes hidden and scores every link against them, by method; and
// counts the commits each pipeline links: edits alone (the sessions' git
// commands hidden too), and everything.
func TestCommitLinkCalibrationProbe(t *testing.T) {
	catalog := stageCatalog(t)
	ctx := context.Background()
	sessions, byRepository, err := catalog.readGitSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	locations, err := catalog.gitLocations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hide := func(printed, ops bool) map[string]*gitSession {
		hidden := map[string]*gitSession{}
		for id, session := range sessions {
			copied := *session
			if printed {
				copied.Printed = nil
			}
			if ops {
				copied.Ops = nil
			}
			hidden[id] = &copied
		}
		return hidden
	}
	blind, editsOnly := hide(true, false), hide(false, true)
	// Commits after the snapshot's last activity, and those GitHub committed
	// (squash merges), are no session's to link.
	var last string
	if err := catalog.DB.QueryRow("SELECT MAX(activity_at) FROM workspaces").Scan(&last); err != nil {
		t.Fatal(err)
	}
	until, _ := parseTime(last)
	t.Logf("snapshot ends %s", until.Format(time.RFC3339))
	scores := map[string]*[3]int{} // method/confidence → right, wrong, of printed
	score := func(key string, index int) {
		if scores[key] == nil {
			scores[key] = &[3]int{}
		}
		scores[key][index]++
	}
	totals := map[string]int{}
	for _, repository := range slices.Sorted(maps.Keys(byRepository)) {
		dir, _ := gitCheckout(locations[repository])
		if dir == "" {
			continue
		}
		members := byRepository[repository]
		since := time.Time{}
		for _, id := range members {
			if session := sessions[id]; session.Timed && (since.IsZero() || session.First.Before(since)) {
				since = session.First
			}
			for _, op := range sessions[id].opsIn(repository) {
				if from, _, ok := op.window(); ok && (since.IsZero() || from.Before(since)) {
					since = from
				}
			}
		}
		if since.IsZero() {
			continue
		}
		after := "--since=" + strconv.FormatInt(since.Add(-24*time.Hour).Unix(), 10)
		started := time.Now()
		commits, err := readGitCommits(ctx, dir, after)
		if err != nil {
			t.Logf("%s: %v", repository, err)
			continue
		}
		truth := map[string]string{}
		all, _ := inferLinks(ctx, dir, after, sessions, members, repository, commits)
		for _, link := range all {
			if link.Method == "transcript" {
				if previous, ok := truth[link.Hash]; ok && previous != link.Session {
					truth[link.Hash] = "?"
				} else {
					truth[link.Hash] = link.Session
				}
			}
		}
		hidden, _ := inferLinks(ctx, dir, after, blind, members, repository, commits)
		found := map[string]bool{}
		for _, link := range hidden {
			session, ok := truth[link.Hash]
			if !ok || session == "?" {
				continue
			}
			found[link.Hash] = true
			key := fmt.Sprintf("%s %.2f", link.Method, link.Confidence)
			if by, _ := link.Evidence["chosen_by"].(string); by != "" {
				key += " " + by
			}
			score(key, map[bool]int{true: 0, false: 1}[link.Session == session])
		}
		for hash, session := range truth {
			if session != "?" && !found[hash] {
				score("(none)", 2)
			}
		}
		// Why commands missed a printed commit: what the printing session's
		// commands in this repository say about it.
		commanded := map[string]bool{}
		for _, link := range hidden {
			if link.Method == "message" || link.Method == "window" || link.Method == "rewrite" {
				commanded[link.Hash] = true
			}
		}
		windows := readOpWindows(blind, members, repository)
		for hash, session := range truth {
			if session == "?" || commanded[hash] {
				continue
			}
			commit := commits[hash]
			why := "no commit command in this repository"
			nearest := time.Duration(-1)
			for _, op := range sessions[session].opsIn(repository) {
				if op.Kind != "commit" && op.Kind != "revert" {
					continue
				}
				from, until, ok := op.window()
				if !ok {
					continue
				}
				distance := max(from.Sub(commit.Commit), commit.Commit.Sub(until), 0)
				if nearest < 0 || distance < nearest {
					nearest = distance
				}
				if distance == 0 {
					switch {
					case len(op.Subjects) > 0 && !slices.Contains(op.Subjects, commit.Subject):
						why = "its command wrote another subject"
					case commit.Author.Before(from) || commit.Author.After(until):
						why = "authored before its command (amend or rebase)"
					default:
						others := map[string]bool{}
						for _, window := range windows.holding(commit.Commit) {
							others[window.session] = true
						}
						why = fmt.Sprintf("contested by %d sessions", len(others))
					}
					break
				}
				why = "outside its commands' windows"
				if op.Until == "" {
					why = "outside its commands' windows (no end recorded)"
				}
			}
			if strings.HasPrefix(why, "outside") {
				switch {
				case nearest < time.Minute:
					why += ", within a minute"
				case nearest < time.Hour:
					why += ", within an hour"
				default:
					why += ", further"
				}
			}
			if commit.Parents > 1 {
				why = "a merge"
			}
			score("missed: "+why, 2)
		}
		old, _ := inferLinks(ctx, dir, after, editsOnly, members, repository, commits)
		committers, err := outcomeGitCommand(ctx, dir, outcomeGitLogTimeout, nil, "log", "--branches", "--remotes", "--tags", after, "--format=%H %ce")
		if err != nil {
			t.Logf("%s: %v", repository, err)
			continue
		}
		byGitHub := map[string]bool{}
		for _, line := range strings.Split(string(committers), "\n") {
			if hash, email, ok := strings.Cut(line, " "); ok && email == "noreply@github.com" {
				byGitHub[hash] = true
			}
		}
		counts := func(commit *gitCommit) bool {
			return commit.Parents <= 1 && !commit.Replaced && !byGitHub[commit.Hash] && !commit.Author.Before(since) && !commit.Author.After(until)
		}
		held, heldLines := 0, int64(0)
		for _, commit := range commits {
			if counts(commit) {
				held++
				heldLines += commit.Added
			}
		}
		linked := func(links []gitLink) (int, int64) {
			seen := map[string]bool{}
			lines := int64(0)
			for _, link := range links {
				if commit := commits[link.Hash]; link.Confidence >= outcomeGitCounted && !seen[link.Hash] && counts(commit) {
					seen[link.Hash] = true
					lines += commit.Added
				}
			}
			return len(seen), lines
		}
		oldCommits, oldLines := linked(old)
		newCommits, newLines := linked(all)
		t.Logf("%s (%s): %d commits by sessions' time, %d lines; edits alone link %d (%d lines); everything %d (%d lines); %s",
			repository, dir, held, heldLines, oldCommits, oldLines, newCommits, newLines, time.Since(started).Round(time.Second))
		for key, value := range map[string]int{"held": held, "old": oldCommits, "new": newCommits} {
			totals[key] += value
		}
		totals["held lines"] += int(heldLines)
		totals["old lines"] += int(oldLines)
		totals["new lines"] += int(newLines)
	}
	t.Logf("totals %v", totals)
	for _, key := range slices.Sorted(maps.Keys(scores)) {
		value := scores[key]
		t.Logf("%-32s right %5d  wrong %4d  missed %5d", key, value[0], value[1], value[2])
	}
}
