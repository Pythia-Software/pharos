package archive

import (
	"context"
	"slices"
	"strings"
)

// When several sessions could have made a commit, two facts git keeps
// locally can tell them apart. Each is read only for those commits:
//   - the checkout that made it: each worktree's HEAD reflog records the
//     commits made there, and a session's sighting names its checkout;
//   - the lines it adds: a session's kept lines (as fingerprints, from
//     session_files) that the commit's diff adds, read and compared in
//     memory and never stored.
//
// A third, the session's recorded branch holding the commit, decided none
// of 577 contested commits on a real library and is not read: see the
// design document's commit-linking calibration.

// gitSignals is what those facts say about a repository's contested commits.
type gitSignals struct {
	MadeIn map[string][]string           // commit → checkouts whose reflog made it
	Added  map[string]map[string]lineSet // commit → file → lines it adds
}

// reflogMadeCommit reports whether a reflog entry is of a commit made in the
// checkout, as opposed to one checked out, reset to or pulled: a commit,
// cherry-pick or revert, or a commit a rebase wrote. A rebase's start, finish
// or abort names a commit it did not write, such as its upstream.
func reflogMadeCommit(action string) bool {
	head, message, _ := strings.Cut(action, ": ")
	if strings.HasPrefix(head, "rebase") {
		return !strings.Contains(head, "(start)") && !strings.Contains(head, "(finish)") && !strings.Contains(head, "(abort)") &&
			!strings.HasPrefix(head, "rebase finished") && !strings.HasPrefix(message, "checkout ") && !strings.HasPrefix(message, "fast-forward")
	}
	return slices.ContainsFunc([]string{"commit", "cherry-pick", "revert"}, func(prefix string) bool { return strings.HasPrefix(head, prefix) })
}

// readGitSignals reads the signals for contested commits, each with the
// sessions that could have made it. A signal that cannot be read is left
// out, and the commit falls back to the edit windows.
func readGitSignals(ctx context.Context, dir string, sessions map[string]*gitSession, contested map[string][]string) *gitSignals {
	signals := &gitSignals{MadeIn: map[string][]string{}, Added: map[string]map[string]lineSet{}}
	if len(contested) == 0 {
		return signals
	}
	hashes := make([]string, 0, len(contested))
	locations := []string{}
	for hash, candidates := range contested {
		hashes = append(hashes, hash)
		for _, id := range candidates {
			for _, location := range sessions[id].Locations {
				if !slices.Contains(locations, location) {
					locations = append(locations, location)
				}
			}
		}
	}
	slices.Sort(hashes)
	slices.Sort(locations)
	wanted := map[string]bool{}
	for _, hash := range hashes {
		wanted[hash] = true
	}

	for _, location := range locations {
		if _, err := integrationCommonDir(location); err != nil {
			continue
		}
		output, err := outcomeGitCommand(ctx, location, outcomeGitShortTimeout, nil, "reflog", "show", "--format=%H%x1f%gs", "HEAD")
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(output), "\n") {
			hash, action, ok := strings.Cut(line, "\x1f")
			if !ok || !wanted[hash] || slices.Contains(signals.MadeIn[hash], location) {
				continue
			}
			if reflogMadeCommit(action) {
				signals.MadeIn[hash] = append(signals.MadeIn[hash], location)
			}
		}
	}

	if output, err := outcomeGitCommand(ctx, dir, outcomeGitLogTimeout, []byte(strings.Join(hashes, "\n")+"\n"),
		append([]string{"-c", "core.quotePath=false", "log", "--no-walk=unsorted", "--stdin", "-p", "--no-prefix", "--no-renames", "--no-color", "--format=%x1e%H"}, gitRawDiff...)...); err == nil {
		for hash, files := range readAddedLines(string(output)) {
			if wanted[hash] {
				signals.Added[hash] = files
			}
		}
	}

	return signals
}

// readAddedLines reads `git log -p --no-prefix --format=%x1e%H` into each
// commit's added lines per file, as fingerprints.
func readAddedLines(output string) map[string]map[string]lineSet {
	added := map[string]map[string]lineSet{}
	for _, record := range strings.Split(output, "\x1e") {
		hash, body, _ := strings.Cut(record, "\n")
		if !gitCommitID.MatchString(hash) {
			continue
		}
		files := map[string]lineSet{}
		path, header := "", false
		for _, line := range strings.Split(body, "\n") {
			switch {
			case strings.HasPrefix(line, "diff --git "):
				path, header = "", true
			case header && strings.HasPrefix(line, "+++ "):
				path = strings.TrimPrefix(line, "+++ ")
				if path == "/dev/null" {
					path = ""
				}
			case header && strings.HasPrefix(line, "@@"):
				header = false
			case !header && path != "" && strings.HasPrefix(line, "+"):
				if files[path] == nil {
					files[path] = lineSet{}
				}
				fingerprintLines(line[1:], files[path])
			}
		}
		added[hash] = files
	}
	return added
}

// lineMargin is how many times the runner-up's matching lines the session
// with the most must have for the lines to decide a contested commit. On a
// real library, any lead was right 92.3% of the time, deciding 96% of
// contested commits; twice the runner-up 93.6%, deciding 92%; five times
// 95.8%, deciding 83%.
var lineMargin = 2.0

// picks is the one candidate each signal points to, or "" when it points to
// none or to several: by checkout, and by the most of its lines, by
// lineMargin over the runner-up.
func (signals *gitSignals) picks(hash, repository string, sessions map[string]*gitSession, candidates []string) (checkout, lines string) {
	if signals == nil {
		return "", ""
	}
	made := []string{}
	for _, id := range candidates {
		if slices.ContainsFunc(sessions[id].Locations, func(location string) bool { return slices.Contains(signals.MadeIn[hash], location) }) {
			made = append(made, id)
		}
	}
	if len(made) == 1 {
		checkout = made[0]
	}
	best, bestLines, second := "", 0, 0
	for _, id := range candidates {
		switch matched := signals.matchedLines(hash, repository, sessions[id]); {
		case matched > bestLines:
			best, bestLines, second = id, matched, bestLines
		case matched > second:
			second = matched
		}
	}
	if bestLines > 0 && bestLines > second && float64(bestLines) >= lineMargin*float64(second) {
		lines = best
	}
	return checkout, lines
}

// matchedLines counts the lines a commit adds that a session kept.
func (signals *gitSignals) matchedLines(hash, repository string, session *gitSession) int {
	matched := 0
	for path, lines := range signals.Added[hash] {
		kept := session.Kept[repository][path]
		for line := range lines {
			if hasLine(kept, line) {
				matched++
			}
		}
	}
	return matched
}

// linkTieBreaks are how a contested commit's session is chosen, most
// reliable first, and the confidence each gives: on a real library the
// checkout was right 13 times of 13 it decided, the lines (by lineMargin)
// 498 of 532.
var linkTieBreaks = []struct {
	name       string
	confidence float64
}{{"checkout", 0.9}, {"lines", 0.8}}
