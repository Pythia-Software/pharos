//go:build probe

package archive

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Derives peer links from a catalog copy without writing it, timing each
// signal. With a Conductor database it also checks the links offline against
// Conductor's workspace grouping: links Pharos never stores or uses, only a
// rough measure of how often probable hits share a Conductor workspace.
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
	container := map[string]string{}
	titles := map[string]string{}
	rows, err = db.QueryContext(ctx, `SELECT id,source_kind,source_id,COALESCE(title,'') FROM workspaces`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, kind, source, title string
		if err := rows.Scan(&id, &kind, &source, &title); err != nil {
			t.Fatal(err)
		}
		rep := id
		if r := representatives[id]; r != "" {
			rep = r
		}
		if kind == "conductor" && sessions[source] != "" {
			container[rep] = sessions[source]
		}
		if titles[rep] == "" || rep == id {
			titles[rep] = title
		}
	}
	rows.Close()
	type tally struct{ same, different, unknown int }
	counts := map[string]*tally{}
	different := []string{}
	for _, l := range all {
		key := l.kind + "/" + l.confidence
		if counts[key] == nil {
			counts[key] = &tally{}
		}
		a, b := container[l.left], container[l.right]
		switch {
		case a == "" || b == "":
			counts[key].unknown++
		case a == b:
			counts[key].same++
		default:
			counts[key].different++
			if len(different) < 40 {
				different = append(different, key+": "+titles[l.left]+"  →  "+titles[l.right]+"  "+jsonText(l.evidence))
			}
		}
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		t.Logf("%-24s same=%d different=%d outside_conductor=%d", key, counts[key].same, counts[key].different, counts[key].unknown)
	}
	for _, line := range different {
		t.Log("different: " + line)
	}
	// Every link with its offline check, for analysis outside the test.
	if out := os.Getenv("PHAROS_PEER_PROBE_OUT"); out != "" {
		rows := []map[string]any{}
		for _, l := range all {
			a, b := container[l.left], container[l.right]
			check := "outside"
			if a != "" && b != "" {
				check = map[bool]string{true: "same", false: "different"}[a == b]
			}
			rows = append(rows, map[string]any{"left": l.left, "right": l.right, "kind": l.kind, "confidence": l.confidence, "at": l.at,
				"left_message": l.leftMessage, "right_message": l.rightMessage, "evidence": l.evidence, "check": check, "left_title": titles[l.left], "right_title": titles[l.right]})
		}
		if err := os.WriteFile(out, []byte(jsonText(rows)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
