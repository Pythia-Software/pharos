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
	"path/filepath"
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

// calibrationSessions hides evidence without changing the source sessions:
// Git operations can carry printed hashes as well as session.Printed.
func calibrationSessions(sessions map[string]*gitSession, printed, ops bool) map[string]*gitSession {
	hidden := map[string]*gitSession{}
	for id, session := range sessions {
		copied := *session
		if ops {
			copied.Ops = nil
		} else if printed {
			copied.Ops = slices.Clone(session.Ops)
			for index := range copied.Ops {
				copied.Ops[index].Hashes = nil
			}
		}
		if printed {
			copied.Printed = nil
		}
		hidden[id] = &copied
	}
	return hidden
}

// readCalibrationCommits reads the same clone histories as a production scan,
// keeping each clone's ownership for inferLinks' diff signals. GitHub committers
// are collected from every clone, including detached heads, for coverage counts.
// All Git processes are owned and waited for by outcomeGitCommand.
func readCalibrationCommits(ctx context.Context, clones []*gitClone, after string) (map[string]*gitCommit, map[string]bool, error) {
	commits, byGitHub := map[string]*gitCommit{}, map[string]bool{}
	for _, clone := range clones {
		worktrees, err := outcomeGitCommand(ctx, clone.Dir, outcomeGitShortTimeout, nil, "worktree", "list", "--porcelain")
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", clone.Dir, err)
		}
		clone.Heads = detachedHeads(string(worktrees))
		read, err := readGitCommits(ctx, clone.Dir, after, clone.Heads...)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", clone.Dir, err)
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
		args := append([]string{"-c", "log.showSignature=false", "log", "--branches", "--remotes", "--tags"}, clone.Heads...)
		committers, err := outcomeGitCommand(ctx, clone.Dir, outcomeGitLogTimeout, nil, append(args, after, "--format=%H %ce")...)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", clone.Dir, err)
		}
		for _, line := range strings.Split(string(committers), "\n") {
			if hash, email, ok := strings.Cut(line, " "); ok && email == "noreply@github.com" {
				byGitHub[hash] = true
			}
		}
	}
	return commits, byGitHub, nil
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
	identities, err := catalog.gitIdentities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	blind, editsOnly := calibrationSessions(sessions, true, false), calibrationSessions(sessions, true, true)
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
		clones, reason := gitCheckouts(ctx, locations[repository], identities[repository])
		if len(clones) == 0 {
			t.Logf("%s: %s", repository, reason)
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
		commits, byGitHub, err := readCalibrationCommits(ctx, clones, after)
		if err != nil {
			t.Logf("%s: %v", repository, err)
			continue
		}
		truth := map[string]string{}
		all, _ := inferLinks(ctx, clones, sessions, members, repository, commits)
		for _, link := range all {
			if link.Method == "transcript" {
				if previous, ok := truth[link.Hash]; ok && previous != link.Session {
					truth[link.Hash] = "?"
				} else {
					truth[link.Hash] = link.Session
				}
			}
		}
		hidden, _ := inferLinks(ctx, clones, blind, members, repository, commits)
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
		old, _ := inferLinks(ctx, clones, editsOnly, members, repository, commits)
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
		t.Logf("%s (%d clones): %d commits by sessions' time, %d lines; edits alone link %d (%d lines); everything %d (%d lines); %s",
			repository, len(clones), held, heldLines, oldCommits, oldLines, newCommits, newLines, time.Since(started).Round(time.Second))
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

// These fixture checks need no stage catalog or recorded checkouts.
func TestCommitLinkCalibrationInputs(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	hash := strings.Repeat("a", 40)
	subject := subjectFingerprint("A commit made during the session")
	sessions := map[string]*gitSession{"session": {
		ID: "session", Repository: "repo", Timed: true, First: at.Add(-time.Minute), Last: at,
		Files:   map[string]map[string]editSpan{"repo": {"a.go": {First: at.Add(-time.Minute), Last: at}}},
		Printed: []outcomeCommit{{Hash: hash}},
		Ops: []gitOp{{Kind: "commit", At: at.Add(-time.Second).Format(time.RFC3339), Until: at.Add(time.Second).Format(time.RFC3339),
			Hashes: []string{hash}, Subjects: []string{subject}}},
	}}
	commits := map[string]*gitCommit{hash: {Hash: hash, Author: at, Commit: at, Subject: subject, Files: map[string][2]int64{"a.go": {1, 0}}}}
	for _, check := range []struct {
		name         string
		printed, ops bool
		method       string
		confidence   float64
	}{
		{"everything", false, false, "transcript", 1},
		{"printed hashes hidden", true, false, "message", 0.95},
		{"edits alone", true, true, "inferred", 0.7},
	} {
		t.Run(check.name, func(t *testing.T) {
			hidden := calibrationSessions(sessions, check.printed, check.ops)
			links, _ := inferLinks(t.Context(), nil, hidden, []string{"session"}, "repo", commits)
			if len(links) != 1 || links[0].Method != check.method || links[0].Confidence != check.confidence {
				t.Fatalf("links = %+v, want %s at %g", links, check.method, check.confidence)
			}
			if len(sessions["session"].Printed) != 1 || len(sessions["session"].Ops) != 1 || !slices.Equal(sessions["session"].Ops[0].Hashes, []string{hash}) {
				t.Fatal("hiding evidence changed the source sessions")
			}
		})
	}
}

func TestCommitLinkCalibrationClones(t *testing.T) {
	repo := newGitRepository(t)
	at := time.Now().Add(-4 * time.Hour).Truncate(time.Second)
	root := repo.git(at, "rev-parse", "HEAD")
	shared := repo.commitAt(at, "Shared commit", map[string]string{"shared.go": "package shared\n"})
	other := &gitRepository{t, filepath.Join(t.TempDir(), "clone")}
	repo.git(at, "clone", "-q", "--no-local", repo.dir, other.dir)
	// A commit held only by one clone's reflog is still live in another.
	repo.git(at, "reset", "-q", "--hard", root)
	first := repo.commitAt(at.Add(time.Hour), "First clone only", map[string]string{"first.go": "package first\n"})
	second := other.commitAt(at.Add(2*time.Hour), "Second clone only", map[string]string{"second.go": "package second\n"})
	detached := &gitRepository{t, filepath.Join(t.TempDir(), "detached")}
	other.git(at, "worktree", "add", "-q", "--detach", detached.dir, "HEAD")
	detached.commitAt(at.Add(3*time.Hour), "Detached GitHub commit", map[string]string{"detached.go": "package detached\n"})
	detached.git(at.Add(3*time.Hour), "-c", "user.email=noreply@github.com", "commit", "-q", "--amend", "--no-edit")
	github := detached.git(at, "rev-parse", "HEAD")
	clones, reason := gitCheckouts(t.Context(), []string{repo.dir, other.dir, detached.dir}, gitIdentity{Root: root})
	if len(clones) != 2 || reason != "" {
		t.Fatalf("checkouts = %v, reason = %q; want two clones", clones, reason)
	}
	commits, byGitHub, err := readCalibrationCommits(t.Context(), clones, "--since="+strconv.FormatInt(at.Add(-time.Hour).Unix(), 10))
	if err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{shared, first, second, github} {
		if commits[hash] == nil || commits[hash].Replaced {
			t.Fatalf("live commit %s missing or marked replaced: %+v", hash, commits[hash])
		}
	}
	if !clones[0].Has[shared] || !clones[1].Has[shared] || !clones[0].Has[first] || clones[1].Has[first] || clones[0].Has[second] || !clones[1].Has[second] || !clones[1].Has[github] {
		t.Fatalf("incorrect per-clone ownership: %v, %v", clones[0].Has, clones[1].Has)
	}
	if !slices.Contains(clones[1].Heads, github) || !byGitHub[github] || byGitHub[second] {
		t.Fatalf("detached head or GitHub committer missing: heads %v, GitHub %v", clones[1].Heads, byGitHub)
	}
	// No partial population should be scored if a clone fails to read.
	run := outcomeGitCommand
	override(t, &outcomeGitCommand, func(ctx context.Context, dir string, timeout time.Duration, stdin []byte, args ...string) ([]byte, error) {
		if dir == other.dir {
			return nil, errGitTimeout
		}
		return run(ctx, dir, timeout, stdin, args...)
	})
	if commits, byGitHub, err := readCalibrationCommits(t.Context(), clones, "--since=0"); err == nil || commits != nil || byGitHub != nil {
		t.Fatalf("failed read returned commits %v, GitHub %v, error %v", commits, byGitHub, err)
	}
}
