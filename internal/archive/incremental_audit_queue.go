package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	automaticAuditAttempts = 3
	auditRetry             = 15 * time.Minute
	heavyAuditBytes        = int64(128 << 20)
	heavyAuditWall         = 30 * time.Second
	heavyAuditBatch        = 4
)

type auditProgress struct {
	attempted, next, signature string
	heavy                      bool
}

// Keep last completed verification independent of attempts and backoff. The
// queue commits with the result, and survives cancellation, restarts and ejects.
func (c *Catalog) recordAuditProgress(unit syncUnit, result auditResult) error {
	return c.writeTransaction(context.Background(), "verification result", func(tx *sql.Tx) error {
		at := time.Now()
		stamp, signature := formatTime(at), partsSignature(unit.Parts)
		var verified, previousSignature string
		var pending, attempts int
		err := tx.QueryRow("SELECT COALESCE(verified_at,''),signature,heavy,attempts FROM sync_verification_progress WHERE host_id=? AND source_name=? AND unit=?", unit.hostID(), unit.Source.Name, unit.ID).Scan(&verified, &previousSignature, &pending, &attempts)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRow("SELECT checked_at FROM sync_verifications WHERE host_id=? AND source_name=? AND unit=? AND outcome IN ('passed','mismatch')", unit.hostID(), unit.Source.Name, unit.ID).Scan(&verified)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		complete := result.Outcome == "passed" || result.Outcome == "mismatch"
		heavy := result.Reason == "evidence_size" || result.Reason == "time_budget" || pending != 0 && previousSignature == signature
		var next any = formatTime(at.Add(auditRetry))
		if heavy && (pending == 0 || signature != previousSignature) {
			next = stamp
		}
		attempts++
		if complete {
			verified, next, heavy, attempts = stamp, nil, false, 0
		}
		_, err = tx.Exec(`INSERT INTO sync_verification_progress(host_id,source_name,unit,signature,attempted_at,verified_at,next_at,heavy,attempts,reason,evidence_bytes)
			VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(host_id,source_name,unit) DO UPDATE SET
			signature=excluded.signature,attempted_at=excluded.attempted_at,verified_at=excluded.verified_at,next_at=excluded.next_at,
			heavy=excluded.heavy,attempts=excluded.attempts,reason=excluded.reason,evidence_bytes=excluded.evidence_bytes`,
			unit.hostID(), unit.Source.Name, unit.ID, signature, stamp, nilIfEmpty(verified), next, heavy, attempts, nilIfEmpty(result.Reason), result.EvidenceBytes)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO sync_verifications(host_id,source_name,unit,checked_at,outcome,detail,signature)
			VALUES(?,?,?,?,?,?,?) ON CONFLICT(host_id,source_name,unit) DO UPDATE SET checked_at=excluded.checked_at,
			outcome=excluded.outcome,detail=excluded.detail,signature=excluded.signature`, unit.hostID(), unit.Source.Name, unit.ID, stamp, result.Outcome, nilIfEmpty(result.Detail), signature)
		return err
	})
}

func (c *Catalog) queuedVerificationNext(ctx context.Context) time.Time {
	var value sql.NullString
	if c.reader().QueryRowContext(ctx, "SELECT MIN(next_at) FROM sync_verification_progress WHERE host_id=? AND heavy=1", currentHost().ID).Scan(&value) != nil {
		return time.Time{}
	}
	at, _ := parseTime(value.String)
	return at
}

func (c *Catalog) verificationStatus(ctx context.Context) map[string]any {
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT source_name,reason,COUNT(*) units,MIN(next_at) next_at,MAX(attempted_at) attempted_at
		FROM sync_verification_progress WHERE host_id=? AND heavy=1 GROUP BY source_name,reason ORDER BY source_name,reason`, currentHost().ID)
	var verified sql.NullString
	verifiedErr := c.reader().QueryRowContext(ctx, `SELECT MAX(COALESCE(p.verified_at,CASE WHEN v.outcome IN ('passed','mismatch') THEN v.checked_at END))
        FROM sync_verifications v LEFT JOIN sync_verification_progress p USING(host_id,source_name,unit) WHERE v.host_id=?`, currentHost().ID).Scan(&verified)
	return map[string]any{"available": err == nil && verifiedErr == nil, "pending": rows, "last_verified_at": nilIfEmpty(verified.String)}
}

// A larger pass is one bounded batch under the background pass's existing
// ingest coordinator. It rediscovers current source units, never deserializes
// an old adapter/configuration or certifies a stored result as fresh evidence.
func (s *Server) verifyQueued(ctx context.Context, op *Op) error {
	rows, err := queryMapsContext(ctx, s.Catalog.reader(), `SELECT source_name,unit FROM sync_verification_progress
		WHERE host_id=? AND heavy=1 AND next_at<=? ORDER BY next_at,attempted_at,source_name,unit LIMIT ?`, currentHost().ID, now(), heavyAuditBatch)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, heavyAuditWall)
	defer cancel()
	op.Step("Verify queued inputs", "input", int64(len(rows)))
	remaining := map[string]map[string]bool{}
	for _, row := range rows {
		source := firstString(row["source_name"])
		if remaining[source] == nil {
			remaining[source] = map[string]bool{}
		}
		remaining[source][firstString(row["unit"])] = true
	}
	var failure error
	for _, source := range s.Config().Sources {
		selected := remaining[source.Name]
		if len(selected) == 0 || !source.Enabled || ctx.Err() != nil {
			continue
		}
		adapter, err := MakeAdapter(source)
		if err != nil {
			failure = errors.Join(failure, err)
			continue
		}
		// Indexing may replace the live adapter with a retained SQLite-group
		// reader. Rediscover through the same acquisition boundary so queued
		// database identities match, and pin that reader through verification.
		input, err := s.Catalog.acquireRetainedInput(ctx, adapter, nil)
		if err != nil {
			failure = errors.Join(failure, err)
			continue
		}
		defer input.Close()
		adapter = input.Adapter
		partial, ok := adapter.(partialAdapter)
		if !ok {
			continue
		}
		setAdapterContext(adapter, ctx)
		units := []syncUnit{}
		err = partial.discoverParts(func(parts []sourcePart) bool {
			unit := unitOf(adapter.Config(), parts, false)
			if selected[unit.ID] {
				unit.Reader = adapter
				units = append(units, unit)
			}
			return true
		}, func(*WorkspaceRecord, []sourcePart) error {
			return errors.New("verification discovery attempted to parse")
		})
		if err != nil {
			failure = errors.Join(failure, err)
			continue
		}
		for _, unit := range units {
			if ctx.Err() != nil {
				break
			}
			result := s.Catalog.auditUnit(ctx, unit, string(op.ID()), heavyAuditBytes)
			if err := s.Catalog.recordVerification(unit, result); err != nil {
				return err
			}
			delete(selected, unit.ID)
			op.Advance(1)
			if result.Outcome == "read_error" {
				failure = errors.Join(failure, fmt.Errorf("%s: %s", source.Name, result.Detail))
			}
		}
	}
	// Missing or disabled sources remain pending with backoff. They do not
	// manufacture coverage, disappear from the queue, or spin every tick.
	for source, units := range remaining {
		for unit := range units {
			if ctx.Err() != nil {
				break
			}
			_, err := s.Catalog.DB.ExecContext(ctx, "UPDATE sync_verification_progress SET next_at=?,reason='source_unavailable' WHERE host_id=? AND source_name=? AND unit=? AND heavy=1", formatTime(time.Now().Add(auditRetry)), currentHost().ID, source, unit)
			if err != nil {
				return err
			}
		}
	}
	return errors.Join(failure, ctx.Err())
}
