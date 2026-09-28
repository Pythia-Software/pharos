package archive

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// Run explicitly with PHAROS_LIVE_CATALOG. This opens the existing library
// read-only and derives a single workspace in memory; it never initializes it.
func TestLiveClaudeUsageAttributionSample(t *testing.T) {
	path := os.Getenv("PHAROS_LIVE_CATALOG")
	if path == "" {
		t.Skip("set PHAROS_LIVE_CATALOG to sample a read-only catalog")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	var workspaceID string
	if err := db.QueryRowContext(ctx, `SELECT w.id FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
		WHERE c.started_at>=date('now','-30 day') AND c.agent_depth=0
		AND w.title LIKE 'El Paso v10 multi-agent orchestration%' LIMIT 1`).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	rows, err := queryMapsContext(ctx, db, `SELECT id,native_id,provider,model,agent_depth,started_at FROM conversations WHERE workspace_id=? ORDER BY agent_depth,native_id`, workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	conversations := make([]ConversationRecord, 0, len(rows))
	for _, row := range rows {
		messages, err := storedMessages(ctx, db, row["id"])
		if err != nil {
			t.Fatal(err)
		}
		nativeID := firstString(row["native_id"])
		parent := ""
		if before, _, ok := strings.Cut(nativeID, ":subagent:"); ok {
			parent = before
		}
		conversations = append(conversations, ConversationRecord{NativeID: nativeID, ParentNativeID: parent, Provider: firstString(row["provider"]), Model: firstString(row["model"]), AgentDepth: int(integer(row["agent_depth"])), StartedAt: firstString(row["started_at"]), Messages: messages})
	}
	var before int64
	if err := db.QueryRowContext(ctx, "SELECT COALESCE(SUM(total_tokens),0) FROM agent_sessions WHERE workspace_id=?", workspaceID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	after := reconciledTokenMetrics(WorkspaceRecord{Conversations: conversations})
	for _, metric := range after {
		if metric["name"] == "total_tokens" {
			t.Logf("El Paso workspace: conversations=%d, before=%d, after=%d", len(conversations), before, integer(metric["value"]))
		}
	}
}
