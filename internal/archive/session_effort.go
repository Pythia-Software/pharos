package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
)

// Independent of usageAttributionVersion: this projection never rewrites usage
// or reparses settings. A current usage repair must not skip this backfill.
const sessionEffortSummaryVersion = "1"

var sessionEffortRepairBatchSize = 500

// sessionEffortSummary describes retained model_requests, not every request the
// agent ever made. Unknown includes missing and explicitly reset request effort.
// Startup messages and aggregate usage claims do not establish request effort.
type sessionEffortSummary struct {
	Levels          []string `json:"levels"`
	KnownRequests   int64    `json:"known_requests"`
	UnknownRequests int64    `json:"unknown_requests"`
	State           string   `json:"state"`
	Coverage        string   `json:"coverage"`
}

func newSessionEffortSummary() sessionEffortSummary {
	return sessionEffortSummary{Levels: []string{}, State: "no-requests", Coverage: "recorded-requests"}
}

func (s *sessionEffortSummary) add(effort string, count int64) {
	effort = normalizeEffort(effort)
	if effort == "" {
		s.UnknownRequests += count
	} else {
		s.KnownRequests += count
		if !slices.Contains(s.Levels, effort) {
			s.Levels = append(s.Levels, effort)
			slices.Sort(s.Levels)
		}
	}
	switch len(s.Levels) {
	case 0:
		s.State = "unknown"
	case 1:
		s.State = "single"
	default:
		s.State = "multiple"
	}
}

const sessionEffortSummaryJoin = ` LEFT JOIN agent_session_effort_summaries es ON es.agent_session_id=a.id AND es.version='` + sessionEffortSummaryVersion + `' `

func readSessionEffortSummary(value any) *sessionEffortSummary {
	var s sessionEffortSummary
	if text := firstString(value); text != "" && json.Unmarshal([]byte(text), &s) == nil {
		return &s
	}
	return nil
}

func (c *Catalog) ensureSessionEffortSummaries() error {
	_, err := c.DB.Exec(`
CREATE INDEX IF NOT EXISTS model_requests_session_effort_idx ON model_requests(agent_session_id,effort);
CREATE TRIGGER IF NOT EXISTS session_effort_requests_insert AFTER INSERT ON model_requests BEGIN
 DELETE FROM agent_session_effort_summaries WHERE agent_session_id=new.agent_session_id;
END;
CREATE TRIGGER IF NOT EXISTS session_effort_requests_delete AFTER DELETE ON model_requests BEGIN
 DELETE FROM agent_session_effort_summaries WHERE agent_session_id=old.agent_session_id;
END;
CREATE TRIGGER IF NOT EXISTS session_effort_requests_update AFTER UPDATE OF effort,agent_session_id ON model_requests
WHEN old.effort IS NOT new.effort OR old.agent_session_id IS NOT new.agent_session_id BEGIN
 DELETE FROM agent_session_effort_summaries WHERE agent_session_id IN (old.agent_session_id,new.agent_session_id);
END;`)
	return err
}

// refreshSessionEffortSummaries reads only request identities and efforts. It
// works for no-request sessions too, and writes the version in the same commit.
func refreshSessionEffortSummaries(ctx context.Context, tx *sql.Tx, ids []string) error {
	summaries := make(map[string]*sessionEffortSummary, len(ids))
	for _, id := range ids {
		s := newSessionEffortSummary()
		summaries[id] = &s
	}
	rows, err := tx.QueryContext(ctx, `SELECT agent_session_id,effort,COUNT(*) FROM model_requests
 WHERE is_estimate=0 AND agent_session_id IN (SELECT value FROM json_each(?)) GROUP BY agent_session_id,effort`, jsonText(ids))
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var effort sql.NullString
		var count int64
		if err := rows.Scan(&id, &effort, &count); err != nil {
			rows.Close()
			return err
		}
		summaries[id].add(effort.String, count)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_session_effort_summaries(agent_session_id,version,summary_json) VALUES(?,?,?)
 ON CONFLICT(agent_session_id) DO UPDATE SET version=excluded.version,summary_json=excluded.summary_json`, id, sessionEffortSummaryVersion, jsonText(summaries[id])); err != nil {
			return err
		}
	}
	return nil
}

// RepairSessionEffortSummaries repairs only the request-effort projection, in
// bounded transactions. Committed batches survive cancellation/restart; request
// mutations invalidate both old and new owners, including moved/deleted rows.
func (c *Catalog) RepairSessionEffortSummaries(ctx context.Context, progress func(int, int)) (int, error) {
	var total int
	if err := c.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_sessions a`+sessionEffortSummaryJoin+`WHERE es.agent_session_id IS NULL`).Scan(&total); err != nil {
		return 0, err
	}
	done := 0
	for {
		count := 0
		err := c.writeTransaction(ctx, "session-effort-repair", func(tx *sql.Tx) error {
			rows, err := queryMapsContext(ctx, tx, `SELECT a.id FROM agent_sessions a`+sessionEffortSummaryJoin+`WHERE es.agent_session_id IS NULL ORDER BY a.id LIMIT ?`, sessionEffortRepairBatchSize)
			if err != nil {
				return err
			}
			ids := make([]string, 0, len(rows))
			for _, row := range rows {
				ids = append(ids, firstString(row["id"]))
			}
			count = len(ids)
			if count == 0 {
				return nil
			}
			return refreshSessionEffortSummaries(ctx, tx, ids)
		})
		if err != nil {
			return done, err
		}
		if count == 0 {
			return done, nil
		}
		done += count
		c.boundWAL(ctx, walSizeLimit)
		if progress != nil {
			progress(done, max(total, done))
		}
	}
}
