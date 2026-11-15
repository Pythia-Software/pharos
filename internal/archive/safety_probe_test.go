//go:build probe

package archive

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Runs risky action discovery over a backup's retained shell calls, without
// writing anything, and reports how many notices each check would raise with
// examples. Use it to measure a detector change against real activity. Only an
// explicitly named backup is accepted, opened immutable and read-only.
//
//	PHAROS_SAFETY_BACKUP='/path/to/backup/catalog.sqlite3' \
//	PHAROS_SAFETY_REPORT=.context/safety-probe.json \
//	  go test -tags probe -run '^TestSafetyBackupProbe$' -v -timeout 30m ./internal/archive
func TestSafetyBackupProbe(t *testing.T) {
	path := os.Getenv("PHAROS_SAFETY_BACKUP")
	if path == "" {
		t.Skip("set PHAROS_SAFETY_BACKUP to an immutable backup")
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(path), "backup") || strings.Contains(path, "/Pharos/catalog/") {
		t.Fatal("probe requires a backup, never the live catalog")
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+"?immutable=1&mode=ro&_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var latest string
	if err := db.QueryRow("SELECT MAX(started_at) FROM tool_calls").Scan(&latest); err != nil {
		t.Fatal(err)
	}
	end, err := time.Parse(time.RFC3339Nano, latest)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT t.id,t.workspace_id,t.conversation_id,COALESCE(t.call_message_id,''),COALESCE(t.result_message_id,''),COALESCE(t.started_at,''),t.status,t.exit_code,
 COALESCE(w.repository_id,''),COALESCE(r.display_name,''),COALESCE(v.origin_host_id,''),COALESCE(h.label,''),COALESCE(w.location,''),COALESCE(t.command,''),COALESCE(m.text,'')
 FROM tool_calls t JOIN workspaces w ON w.id=t.workspace_id JOIN conversations v ON v.id=t.conversation_id
 LEFT JOIN repositories r ON r.id=w.repository_id LEFT JOIN hosts h ON h.id=v.origin_host_id LEFT JOIN messages m ON m.id=t.call_message_id
 WHERE t.started_at>=? AND COALESCE(t.command,'')<>'' AND t.workspace_id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces)`, formatTime(end.AddDate(0, 0, -safetyWindowDays)))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type example struct {
		Subject string `json:"subject"`
		Command string `json:"command"`
		CWD     string `json:"cwd"`
	}
	seen := map[string]bool{}
	// Every call that selected something, so a prototype catalog can be
	// seeded with just those calls and rediscover them.
	selecting := []string{}
	byRule := map[string][]example{}
	calls := 0
	for rows.Next() {
		var call safetyCall
		if err := rows.Scan(&call.ID, &call.Workspace, &call.Conversation, &call.Message, &call.ResultMessage, &call.At, &call.Status, &call.Exit, &call.Repository, &call.RepositoryLabel, &call.Host, &call.HostLabel, &call.Root, &call.Command, &call.Input); err != nil {
			t.Fatal(err)
		}
		calls++
		candidates := selectSafetyCall(call)
		if len(candidates) > 0 {
			selecting = append(selecting, call.ID)
		}
		for _, candidate := range candidates {
			if seen[candidate.ID] {
				continue
			}
			seen[candidate.ID] = true
			evidence := candidate.Evidence[0]
			byRule[candidate.Rule] = append(byRule[candidate.Rule], example{candidate.Subject, clipText(evidence.Command, 200), evidence.CWD})
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rules := make([]string, 0, len(byRule))
	for rule := range byRule {
		rules = append(rules, rule)
	}
	sort.Slice(rules, func(i, j int) bool { return len(byRule[rules[i]]) > len(byRule[rules[j]]) })
	t.Logf("%d shell calls through %s; %d notices", calls, latest, len(seen))
	report := map[string]any{"calls": calls, "through": latest, "notices": len(seen), "rules": map[string]any{}, "call_ids": selecting}
	random := rand.New(rand.NewSource(1))
	for _, rule := range rules {
		examples := byRule[rule]
		t.Logf("%-16s %5d", rule, len(examples))
		random.Shuffle(len(examples), func(i, j int) { examples[i], examples[j] = examples[j], examples[i] })
		report["rules"].(map[string]any)[rule] = map[string]any{"count": len(examples), "examples": examples[:min(len(examples), 60)]}
	}
	if out := os.Getenv("PHAROS_SAFETY_REPORT"); out != "" {
		data, _ := json.MarshalIndent(report, "", " ")
		if err := os.WriteFile(out, data, 0o600); err != nil {
			t.Fatal(err)
		}
		fmt.Println("report:", out)
	}
}
