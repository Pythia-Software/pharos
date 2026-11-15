//go:build probe

package archive

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// Derives peer links from a catalog copy without writing it, timing each
// signal. With a Conductor database it also scores the links offline: a link
// is right when both sides share a Conductor workspace (its id, or its
// checkout folder) or a TL1 work item, and wrong otherwise. Pharos never
// stores or uses that grouping.
//
//	PHAROS_PEER_PROBE_CATALOG='/path/to/copy/catalog.sqlite3' \
//	PHAROS_PEER_PROBE_CONDUCTOR="$HOME/Library/Application Support/com.conductor.app/conductor.db" \
//	  go test -tags probe -run '^TestPeerLinksProbe$' -v -timeout 60m ./internal/archive
func TestPeerLinksProbe(t *testing.T) {
	path := os.Getenv("PHAROS_PEER_PROBE_CATALOG")
	if path == "" {
		t.Skip("set PHAROS_PEER_PROBE_CATALOG to a catalog copy")
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(path, "/Pharos/catalog/") {
		t.Fatal("probe requires a copy, never the live catalog")
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := &Catalog{DB: db}
	ctx := t.Context()
	started := time.Now()
	families, err := c.loadPeerFamilies(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("families roots=%d seconds=%.1f", len(families.top), time.Since(started).Seconds())
	all := []peerLink{}
	for _, step := range []struct {
		name string
		run  func() ([]peerLink, error)
	}{
		{"copies", func() ([]peerLink, error) { return c.derivePeerCopies(ctx, families) }},
		{"tl1", func() ([]peerLink, error) { return c.derivePeerTL1(ctx, families) }},
		{"reads", func() ([]peerLink, error) { links, _, err := c.derivePeerReads(ctx, families, ""); return links, err }},
	} {
		started = time.Now()
		links, err := step.run()
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		t.Logf("%s links=%d seconds=%.1f", step.name, len(links), time.Since(started).Seconds())
		all = append(all, links...)
	}
	// What a later run costs: readers that read in the last day, plus the
	// recheck window before it.
	var latest string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(started_at),'') FROM tool_calls`).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	if at, ok := parseTime(latest); ok {
		started = time.Now()
		links, readers, err := c.derivePeerReads(ctx, families, formatTime(at.Add(-24*time.Hour-peerRecheck)))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("reads for a day of new calls readers=%d links=%d seconds=%.1f", len(readers), len(links), time.Since(started).Seconds())
	}
	conductor := os.Getenv("PHAROS_PEER_PROBE_CONDUCTOR")
	if conductor == "" {
		return
	}
	cdb, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: conductor}).String()+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer cdb.Close()
	sessions := map[string]string{}
	rows, err := cdb.QueryContext(ctx, `SELECT id,workspace_id FROM sessions WHERE workspace_id IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, workspace string
		if err := rows.Scan(&id, &workspace); err != nil {
			t.Fatal(err)
		}
		sessions[id] = workspace
	}
	rows.Close()
	representatives, err := c.authorshipRepresentatives(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Correct related work shares a Conductor workspace (by id, or by its
	// checkout folder in either layout) or a TL1 work item; anything else
	// counts as wrong.
	container, folder, titles := map[string]string{}, map[string]string{}, map[string]string{}
	checkout := regexp.MustCompile(`/conductor/workspaces/[^/]+/[^/]+|/\.conductor/[^/]+`)
	rows, err = db.QueryContext(ctx, `SELECT id,source_kind,source_id,COALESCE(title,''),COALESCE(location,'') FROM workspaces`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, kind, source, title, location string
		if err := rows.Scan(&id, &kind, &source, &title, &location); err != nil {
			t.Fatal(err)
		}
		rep := id
		if r := representatives[id]; r != "" {
			rep = r
		}
		if kind == "conductor" && sessions[source] != "" {
			container[rep] = sessions[source]
		}
		if dir := checkout.FindString(location); dir != "" && folder[rep] == "" {
			folder[rep] = dir
		}
		if titles[rep] == "" || rep == id {
			titles[rep] = title
		}
	}
	rows.Close()
	type tl1Item struct{ candidate, root string }
	items := map[string]tl1Item{}
	rows, err = db.QueryContext(ctx, `SELECT workspace_id,COALESCE(candidate_id,''),COALESCE(NULLIF(root_task_id,''),task_id) FROM tl1_tasks WHERE workspace_id IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var workspace string
		var item tl1Item
		if err := rows.Scan(&workspace, &item.candidate, &item.root); err != nil {
			t.Fatal(err)
		}
		items[workspace] = item
	}
	rows.Close()
	check := func(left, right string) string {
		if a, b := container[left], container[right]; a != "" && b != "" {
			return map[bool]string{true: "same", false: "different"}[a == b]
		}
		if a, okA := items[left]; okA {
			if b, okB := items[right]; okB {
				return map[bool]string{true: "same", false: "different"}[a.candidate != "" && a.candidate == b.candidate || a.root == b.root]
			}
		}
		if a, b := folder[left], folder[right]; a != "" && b != "" {
			return map[bool]string{true: "same", false: "different"}[a == b]
		}
		return "unknown"
	}
	type tally struct{ same, different, unknown int }
	add := func(t *tally, verdict string) {
		switch verdict {
		case "same":
			t.same++
		case "different":
			t.different++
		default:
			t.unknown++
		}
	}
	counts := map[string]*tally{}
	different := []string{}
	type pair struct {
		kinds map[string]bool
		high  bool
	}
	pairs := map[[2]string]*pair{}
	for _, l := range all {
		key := l.kind + "/" + l.confidence
		if counts[key] == nil {
			counts[key] = &tally{}
		}
		verdict := check(l.left, l.right)
		add(counts[key], verdict)
		if verdict == "different" && len(different) < 40 {
			different = append(different, key+": "+titles[l.left]+"  →  "+titles[l.right]+"  "+jsonText(l.evidence))
		}
		if l.kind == "tl1_candidate" {
			continue
		}
		k := [2]string{min(l.left, l.right), max(l.left, l.right)}
		if pairs[k] == nil {
			pairs[k] = &pair{kinds: map[string]bool{}}
		}
		pairs[k].kinds[l.kind] = true
		pairs[k].high = pairs[k].high || l.confidence == peerHigh
	}
	// What a page shows: a pair is certain with one high link or two kinds.
	for k, p := range pairs {
		tier := "pairs (copy and read)/low"
		if p.high || len(p.kinds) > 1 {
			tier = "pairs (copy and read)/high"
		}
		if counts[tier] == nil {
			counts[tier] = &tally{}
		}
		add(counts[tier], check(k[0], k[1]))
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		c := counts[key]
		t.Logf("%-28s same=%d different=%d unknown=%d precision=%.2f", key, c.same, c.different, c.unknown, float64(c.same)/float64(max(1, c.same+c.different)))
	}
	for _, line := range different {
		t.Log("different: " + line)
	}
	// Every link with its offline check, for analysis outside the test.
	if out := os.Getenv("PHAROS_PEER_PROBE_OUT"); out != "" {
		rows := []map[string]any{}
		for _, l := range all {
			rows = append(rows, map[string]any{"left": l.left, "right": l.right, "kind": l.kind, "confidence": l.confidence, "at": l.at,
				"left_message": l.leftMessage, "right_message": l.rightMessage, "evidence": l.evidence, "check": check(l.left, l.right), "left_title": titles[l.left], "right_title": titles[l.right]})
		}
		if err := os.WriteFile(out, []byte(jsonText(rows)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
