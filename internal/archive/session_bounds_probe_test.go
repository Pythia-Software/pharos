//go:build probe

// Validate the timing-only repair on a deliberately projected disposable copy.
// See docs/session-time-bounds.md. Never opens or initializes the source library.
//
//	PHAROS_SESSION_BOUNDS_COPY=/path/cohort.sqlite3 go test -tags probe \
//	  -run '^TestSessionBoundsDisposableCopy$' -v ./internal/archive
package archive

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestSessionBoundsDisposableCopy(t *testing.T) {
	path := os.Getenv("PHAROS_SESSION_BOUNDS_COPY")
	if path == "" {
		t.Skip("set PHAROS_SESSION_BOUNDS_COPY to a fresh projected disposable copy")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=rw"}).String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var marker string
	if err := db.QueryRow(`SELECT purpose FROM session_bounds_disposable_copy`).Scan(&marker); err != nil || marker != "session-bounds-repair-validation" {
		t.Fatal("refusing to repair a database without the disposable-copy marker")
	}
	c := &Catalog{DB: db, Path: absolute}
	facts := func() string {
		t.Helper()
		h := sha256.New()
		for _, table := range []string{"agent_sessions", "conversations", "messages", "model_requests", "tool_calls", "agent_session_usage", "usage_attribution_state", "tl1_attempts"} {
			order := "id"
			switch table {
			case "agent_session_usage":
				order = "agent_session_id,usage_hour,model,effort,attribution"
			case "usage_attribution_state":
				order = "workspace_id"
			case "tl1_attempts":
				order = "installation_id,attempt_id"
			}
			rows, err := queryMaps(db, "SELECT * FROM "+table+" ORDER BY "+order)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if table == "agent_sessions" {
					delete(row, "started_at")
					delete(row, "ended_at")
					delete(row, "time_bounds_source")
				}
				if table == "conversations" {
					delete(row, "session_bounds_json")
				}
				fmt.Fprintln(h, table, jsonText(row))
			}
		}
		return fmt.Sprintf("%x", h.Sum(nil))
	}
	missing := func() int {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM agent_sessions WHERE COALESCE(started_at,'')=''`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	beforeMissing, beforeFacts := missing(), facts()
	preview, err := c.RepairSessionBounds(t.Context(), false)
	if err != nil || preview.Eligible == 0 || missing() != beforeMissing {
		t.Fatalf("preview: %+v, %v", preview, err)
	}
	first, err := c.RepairSessionBounds(t.Context(), true)
	if err != nil || first.Updated != preview.Eligible {
		t.Fatalf("first pass: %+v, %v", first, err)
	}
	second, err := c.RepairSessionBounds(t.Context(), true)
	if err != nil || second.Updated != 0 {
		t.Fatalf("second pass: %+v, %v", second, err)
	}
	if after := facts(); after != beforeFacts {
		t.Fatalf("non-timing facts changed: %s -> %s", beforeFacts, after)
	}
	t.Logf("%s", jsonText(map[string]any{"missing_before": beforeMissing, "missing_after": missing(), "preview": preview, "first_pass": first, "second_pass": second, "non_timing_sha256": beforeFacts}))
}
