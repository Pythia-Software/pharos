//go:build probe

// Links a real library's sessions to the commits of this Mac's checkouts, to
// time the scans, see how links fall, and compare the links a printed hash
// makes with what inference alone would have made. It reads the outcomes
// extract (outcomes_probe_test.go) and a second extract of this Mac's
// sightings, from the same backup opened immutable and read-only:
//
//	sqlite3 /tmp/git.db "ATTACH 'file:BACKUP?immutable=1&mode=ro' AS src;
//	  CREATE TABLE workspace_sightings AS SELECT workspace_id,host_id,source_name,location,repository_locations_json,first_seen_at,last_seen_at
//	    FROM src.workspace_sightings WHERE host_id='THIS_MACS_HOST_ID';
//	  CREATE TABLE repositories AS SELECT id,canonical_remote,display_name,owner,aliases_json,local_locations_json,created_at,updated_at,
//	    normalized_remote,root_commit,forge_id FROM src.repositories;
//	  CREATE TABLE workspace_repositories AS SELECT id,repository_id FROM src.workspaces WHERE repository_id IS NOT NULL;"
//	PHAROS_OUTCOMES_SOURCE=/tmp/outcomes.db PHAROS_GIT_SOURCE=/tmp/git.db PHAROS_TEST_PROCESSES=1 \
//	  go test -tags probe -run TestSessionGitProbe -v -timeout 60m ./internal/archive
//
// Git runs only read-only commands, in the checkouts the sightings name.

package archive

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestSessionGitProbe(t *testing.T) {
	gitSource := os.Getenv("PHAROS_GIT_SOURCE")
	if gitSource == "" {
		t.Skip("set PHAROS_GIT_SOURCE to an extract of this Mac's sightings")
	}
	catalog := outcomesProbeCatalog(t)
	ctx := context.Background()
	if err := catalog.refreshSessionOutcomes(ctx); err != nil {
		t.Fatal(err)
	}
	execTx(t, catalog.DB, `ATTACH DATABASE '`+gitSource+`' AS extract`,
		`INSERT OR IGNORE INTO repositories SELECT * FROM extract.repositories`,
		`UPDATE workspaces SET repository_id=(SELECT repository_id FROM extract.workspace_repositories r WHERE r.id=workspaces.id)`,
		`INSERT OR IGNORE INTO workspace_sightings(workspace_id,host_id,source_name,location,repository_locations_json,first_seen_at,last_seen_at)
			SELECT workspace_id,'`+currentHost().ID+`',source_name,location,repository_locations_json,first_seen_at,last_seen_at FROM extract.workspace_sightings
			WHERE workspace_id IN (SELECT id FROM workspaces)`)
	execTx(t, catalog.DB, `DETACH DATABASE extract`)
	report := func(query string) {
		t.Helper()
		rows, err := queryMaps(catalog.DB, query)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			t.Logf("%v", row)
		}
	}

	// Each repository's scan, timed by its log.
	logs := map[string]time.Duration{}
	run := outcomeGitCommand
	override(t, &outcomeGitCommand, func(ctx context.Context, dir string, timeout time.Duration, stdin []byte, args ...string) ([]byte, error) {
		started := time.Now()
		output, err := run(ctx, dir, timeout, stdin, args...)
		logs[dir] += time.Since(started)
		return output, err
	})
	started := time.Now()
	if err := catalog.refreshSessionGit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("linked every repository in %s", time.Since(started).Round(time.Millisecond))
	for _, dir := range slices.SortedFunc(maps.Keys(logs), func(a, b string) int { return int(logs[b] - logs[a]) })[:min(len(logs), 5)] {
		t.Logf("git in %s: %s", dir, logs[dir].Round(time.Millisecond))
	}
	started = time.Now()
	if err := catalog.refreshSessionGit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("linked again, nothing changed, in %s", time.Since(started).Round(time.Millisecond))
	report(`SELECT r.display_name,s.status,s.reason,COUNT(DISTINCT l.hash) links FROM git_repository_scans s JOIN repositories r ON r.id=s.repository_id
		LEFT JOIN session_commit_links l ON l.repository_id=s.repository_id GROUP BY s.repository_id ORDER BY links DESC`)
	report(`SELECT status,reason,COUNT(*) sessions FROM session_git GROUP BY 1,2 ORDER BY 3 DESC`)
	report(`SELECT method,confidence,COUNT(*) links,SUM(g.on_mainline=1) landed,SUM(g.on_mainline IS NULL) unknown,SUM(g.reverted_by IS NOT NULL) reverted
		FROM session_commit_links l JOIN git_commits g USING(repository_id,hash) GROUP BY 1,2`)
	report(`SELECT mainline_evidence,COUNT(*) FROM git_commits GROUP BY 1 ORDER BY 2 DESC LIMIT 8`)
	report(`SELECT ` + outcomeLabelSQL("w") + ` label,COUNT(*) sessions FROM workspaces w WHERE w.id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces) GROUP BY 1 ORDER BY 2 DESC`)

	// How far after its session's last edit a printed commit was authored:
	// the grace an inferred link allows.
	gaps, err := queryMaps(catalog.DB, `SELECT (julianday(g.author_at)-julianday(so.last_edit_at))*1440 minutes,(julianday(g.author_at)-julianday(so.first_edit_at))*1440 after_first
		FROM session_commit_links l JOIN git_commits g USING(repository_id,hash) JOIN session_outcomes so ON so.workspace_id=l.workspace_id
		WHERE l.method='transcript' AND so.last_edit_at IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	buckets := map[string]int{}
	for _, row := range gaps {
		minutes, first := floatOr(row["minutes"]), floatOr(row["after_first"])
		switch {
		case first < 0:
			buckets["before first edit"]++
		case minutes <= 0:
			buckets["within edits"]++
		case minutes <= 10:
			buckets["0-10 min after"]++
		case minutes <= 60:
			buckets["10-60 min after"]++
		case minutes <= 240:
			buckets["1-4 h after"]++
		default:
			buckets["over 4 h after"]++
		}
	}
	t.Logf("printed commits by author time against their session's edits: %v", buckets)

	// What inference alone, without printed hashes, makes of the commits a
	// session printed.
	sessions, byRepository, err := catalog.readGitSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	locations, err := catalog.gitLocations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	outcome := map[string]int{}
	// Why inference disagrees: where the commit falls against the printing
	// session's window, whether it shares a file, and how the session it
	// picked relates (same title, so a parent and its subagent or a resumed
	// session).
	titles := map[string]string{}
	titleRows, err := queryMaps(catalog.DB, "SELECT id,COALESCE(title,'') title,source_kind FROM workspaces")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range titleRows {
		titles[firstString(row["id"])] = firstString(row["source_kind"]) + ":" + firstString(row["title"])
	}
	samples := map[string]int{}
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
		}
		if since.IsZero() {
			continue
		}
		commits, err := readGitCommits(ctx, dir, "--since="+since.Add(-24*time.Hour).Format(time.RFC3339))
		if err != nil {
			continue
		}
		withPrinted := linkCommits(sessions, members, repository, commits)
		blind := map[string]*gitSession{}
		for id, session := range sessions {
			copied := *session
			copied.Printed = nil
			blind[id] = &copied
		}
		inferred := map[string][]string{}
		confidence := map[string]float64{}
		for _, link := range linkCommits(blind, members, repository, commits) {
			inferred[link.Hash] = append(inferred[link.Hash], link.Session)
			confidence[link.Hash] = link.Confidence
		}
		for _, link := range withPrinted {
			if link.Method != "transcript" {
				continue
			}
			session, commit := sessions[link.Session], commits[link.Hash]
			why := ""
			switch {
			case !session.Timed:
				why = "no edit times"
			case commit.Author.Before(session.First):
				why = "authored before the first edit"
			case commit.Author.After(session.Last.Add(outcomeGitGrace)):
				why = "authored over the grace after the last edit"
			default:
				why = "no shared file"
				for path := range commit.Files {
					if _, ok := session.Files[repository][path]; ok {
						why = "in window with a shared file"
					}
				}
			}
			if commit.Parents > 1 {
				why = "a merge"
			}
			switch got := inferred[link.Hash]; {
			case len(got) == 0:
				outcome["inference finds no session: "+why]++
			case slices.Contains(got, link.Session):
				outcome[fmt.Sprintf("inference agrees at %.1f, printer %s", confidence[link.Hash], why)]++
			default:
				other := sessions[got[0]]
				relation := "another title"
				if titles[got[0]] == titles[link.Session] {
					relation = "same title"
				}
				if strings.HasPrefix(titles[got[0]], "conductor:") != strings.HasPrefix(titles[link.Session], "conductor:") {
					relation = "other source " + strings.SplitN(titles[got[0]], ":", 2)[0]
				}
				key := fmt.Sprintf("inference picks another session at %.1f: printer %s, picked has %s", confidence[link.Hash], why, relation)
				outcome[key]++
				if samples[key] < 2 && why == "in window with a shared file" {
					samples[key]++
					t.Logf("sample %s | printer %s %s [%s..%s] | picked %s %s [%s..%s] | author %s | %s", key, link.Session, titles[link.Session],
						session.First.Format(time.RFC3339), session.Last.Format(time.RFC3339), got[0], titles[got[0]],
						other.First.Format(time.RFC3339), other.Last.Format(time.RFC3339), commit.Author.Format(time.RFC3339), repository)
				}
			}
		}
	}
	keys := slices.Collect(maps.Keys(outcome))
	sort.Strings(keys)
	for _, key := range keys {
		t.Logf("printed commits, %s: %d", key, outcome[key])
	}
}
