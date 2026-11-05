package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// Spend by branch (docs/projects-and-outcomes.md). Claude Code records the
// checkout's branch (gitBranch) on each event, so a session that switches
// branches spends on each in turn. The usage ledger is hourly per session
// and model; agent_session_branch_shares records, for each of those, the
// tokens each branch took, turn by turn, and the branch_usage dataset splits
// the priced ledger by them. Only facts the transcript already holds are
// stored: branch names and token counts.
//
// A turn is on the last branch the transcript recorded at or before it. One
// before the session's first recorded branch, or on a detached HEAD, has no
// branch (stored as ""). A session that never recorded a branch has no
// shares; the dataset then uses the branch its source records once per
// session (Codex), or says it has no branch data.

// branchUsageVersion changes when the derivation does, so every
// conversation is read again.
const branchUsageVersion = "1"

// branchUsageBatch is how many conversations one write commits.
const branchUsageBatch = 100

const branchUsageSQL = `
CREATE TABLE IF NOT EXISTS agent_session_branch_shares(
  agent_session_id TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
  usage_hour TEXT NOT NULL,
  model TEXT NOT NULL,
  branch TEXT NOT NULL,
  tokens INTEGER NOT NULL,
  PRIMARY KEY(agent_session_id,usage_hour,model,branch)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS branch_usage_scans(
  conversation_id TEXT PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE,
  scan_key TEXT NOT NULL) WITHOUT ROWID;
`

func (c *Catalog) ensureBranchUsage() error {
	_, err := c.DB.Exec(branchUsageSQL)
	return err
}

// scheduleToolDerivations marks the tools stage pending when the rules of a
// derivation it runs changed (outcomes, commit links, spend by branch), on a
// new library or an upgraded one. Each derives everything again when its
// version changes, but only when the stage runs: a library that indexes
// nothing new would otherwise wait for the next index or a manual run.
func (c *Catalog) scheduleToolDerivations() error {
	want := outcomesVersion + ":" + outcomeGitVersion + ":" + branchUsageVersion
	stored, err := c.metaValue(context.Background(), "tool_derivations_version")
	if err != nil || stored == want {
		return err
	}
	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A library with nothing indexed derives as it indexes.
	var indexed bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM workspaces)").Scan(&indexed); err != nil {
		return err
	}
	if indexed {
		if err := markSyncDeferredStage(tx, "tools"); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('tool_derivations_version',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, want); err != nil {
		return err
	}
	return tx.Commit()
}

// branchEvent is one stored event of a conversation that kept its source
// record: its session, hour, model and the record itself.
type branchEvent struct {
	Session, CreatedAt, Model, Raw string
}

// branchShareKey is one ledger row's place: session, hour and model.
type branchShareKey struct{ Session, Hour, Model string }

// branchShares reads a conversation's events, in order, into the tokens each
// branch took per session, hour and model. It returns nil for a
// conversation that recorded no branch.
func branchShares(events []branchEvent) map[branchShareKey]map[string]int64 {
	type turn struct {
		key    branchShareKey
		branch string
		tokens int64
	}
	turns, byMessage := []turn{}, map[string]int{}
	current, seen := "", false
	for _, event := range events {
		var record struct {
			GitBranch *string `json:"gitBranch"`
			Message   struct {
				ID    string                     `json:"id"`
				Usage map[string]json.RawMessage `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(event.Raw), &record) != nil {
			continue
		}
		if record.GitBranch != nil {
			switch branch := strings.TrimSpace(*record.GitBranch); branch {
			case "":
			case "HEAD":
				// A detached checkout is on no branch.
				current, seen = "", true
			default:
				current, seen = branch, true
			}
		}
		tokens := int64(0)
		for _, field := range []string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
			var value int64
			if json.Unmarshal(record.Message.Usage[field], &value) == nil {
				tokens += value
			}
		}
		if tokens <= 0 {
			continue
		}
		hour := usageHour(event.CreatedAt)
		if hour == "" {
			continue
		}
		next := turn{key: branchShareKey{event.Session, hour, strings.TrimSpace(event.Model)}, branch: current, tokens: tokens}
		// Claude Code repeats a response's usage on each of its content
		// blocks; the response counts once.
		if id := record.Message.ID; id != "" {
			if index, ok := byMessage[id]; ok {
				turns[index] = next
				continue
			}
			byMessage[id] = len(turns)
		}
		turns = append(turns, next)
	}
	if !seen {
		return nil
	}
	shares := map[branchShareKey]map[string]int64{}
	for _, turn := range turns {
		if shares[turn.key] == nil {
			shares[turn.key] = map[string]int64{}
		}
		shares[turn.key][turn.branch] += turn.tokens
	}
	return shares
}

// refreshBranchUsage derives the shares of Claude conversations in
// repositories whose usage changed since they were last read. It runs in
// the background tools stage, never on a request.
func (c *Catalog) refreshBranchUsage(ctx context.Context) error {
	rows, err := queryMapsContext(ctx, c.DB, `SELECT c.id,
		'`+branchUsageVersion+`:'||COALESCE(c.ended_at,'')||':'||COUNT(a.id)||':'||COALESCE(SUM(a.total_tokens),0) scan_key,
		(SELECT s.scan_key FROM branch_usage_scans s WHERE s.conversation_id=c.id) stored
		FROM conversations c JOIN workspaces w ON w.id=c.workspace_id`+projectJoin("w")+`
		LEFT JOIN agent_sessions a ON a.conversation_id=c.id
		WHERE c.provider='claude' AND pj.id IS NOT NULL
		GROUP BY c.id`)
	if err != nil {
		return err
	}
	pending := rows[:0]
	for _, row := range rows {
		if firstString(row["scan_key"]) != firstString(row["stored"]) {
			pending = append(pending, row)
		}
	}
	reportWork(ctx, "attributing spend to branches", "conversation", 0, len(pending))
	type derived struct {
		id, key string
		shares  map[branchShareKey]map[string]int64
	}
	batch := []derived{}
	write := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := c.writeTransaction(ctx, fmt.Sprintf("branch usage conversations=%d", len(batch)), func(tx *sql.Tx) error {
			for _, item := range batch {
				if _, err := tx.Exec(`DELETE FROM agent_session_branch_shares WHERE agent_session_id IN (SELECT id FROM agent_sessions WHERE conversation_id=?)`, item.id); err != nil {
					return err
				}
				for key, branches := range item.shares {
					for branch, tokens := range branches {
						if _, err := tx.Exec(`INSERT INTO agent_session_branch_shares(agent_session_id,usage_hour,model,branch,tokens) VALUES(?,?,?,?,?)`,
							key.Session, key.Hour, key.Model, branch, tokens); err != nil {
							return fmt.Errorf("branch share: %w", err)
						}
					}
				}
				if _, err := tx.Exec(`INSERT INTO branch_usage_scans(conversation_id,scan_key) VALUES(?,?)
					ON CONFLICT(conversation_id) DO UPDATE SET scan_key=excluded.scan_key`, item.id, item.key); err != nil {
					return err
				}
			}
			return nil
		})
		batch = batch[:0]
		return err
	}
	for index, row := range pending {
		if err := c.queries.wait(ctx); err != nil {
			return err
		}
		id := firstString(row["id"])
		// Only events that kept their source record are read; the Claude
		// adapter keeps it for every event with usage. Claude Code records
		// gitBranch on those too, so each turn's own record places it, and a
		// branch recorded on a prompt is recorded again on the response.
		events, err := queryMapsContext(ctx, c.reader(), `SELECT am.agent_session_id,m.created_at,m.model,m.raw_text FROM messages m
			JOIN agent_session_messages am ON am.message_id=m.id
			WHERE m.conversation_id=? AND m.raw_text IS NOT NULL
			ORDER BY m.source_order IS NULL,m.source_order,m.created_at,m.id`, id)
		if err != nil {
			return err
		}
		read := make([]branchEvent, 0, len(events))
		for _, event := range events {
			read = append(read, branchEvent{Session: firstString(event["agent_session_id"]), CreatedAt: firstString(event["created_at"]),
				Model: firstString(event["model"]), Raw: firstString(event["raw_text"])})
		}
		batch = append(batch, derived{id, firstString(row["scan_key"]), branchShares(read)})
		// Conversations commit in batches: a first pass reads thousands.
		if len(batch) == branchUsageBatch {
			if err := write(); err != nil {
				return err
			}
		}
		reportWork(ctx, "attributing spend to branches", "conversation", index+1, len(pending))
	}
	if err := write(); err != nil {
		return err
	}
	return nil
}
