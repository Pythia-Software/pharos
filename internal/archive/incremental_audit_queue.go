package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	automaticAuditAttempts = 3
	auditRetry             = 15 * time.Minute
	heavyAuditBytes        = int64(128 << 20)
	heavyAuditWall         = 30 * time.Second
	heavyAuditBatch        = 4
)

// auditBackoffMax bounds how long an input that never fits a pass waits.
var auditBackoffMax = 24 * time.Hour

// auditBackoff is the wait before a queued input's next attempt, after the
// given number of attempts at the same input: auditRetry after the first
// queued one, doubling from there up to auditBackoffMax.
func auditBackoff(attempts int) time.Duration {
	wait := auditRetry
	for range attempts - 2 {
		if wait *= 2; wait >= auditBackoffMax {
			return auditBackoffMax
		}
	}
	return wait
}

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
		if signature != previousSignature {
			// Changed input is a new check: earlier failures say nothing of it.
			attempts = 0
		}
		attempts++
		var next any = formatTime(at.Add(auditRetry))
		if heavy && (pending == 0 || signature != previousSignature) {
			next = stamp
		} else if heavy && (result.Reason == "evidence_size" || result.Reason == "time_budget") {
			// The same input ran out of room again: retry it ever less often,
			// so it stops heading the queue every pass, until Verify all or a
			// change to the input.
			next = formatTime(at.Add(auditBackoff(attempts)))
		}
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
	// Inputs tried least first, so one that keeps running out of time (and
	// backs off) does not starve the rest.
	rows, err := queryMapsContext(ctx, s.Catalog.reader(), `SELECT source_name,unit FROM sync_verification_progress
		WHERE host_id=? AND heavy=1 AND next_at<=? ORDER BY attempts,next_at,attempted_at,source_name,unit LIMIT ?`, currentHost().ID, now(), heavyAuditBatch)
	if err != nil {
		return err
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(parent, heavyAuditWall)
	defer cancel()
	// Acquisition and auditing report their own work into op, so the step is
	// restored around them rather than advanced in place: Advance would add
	// to whatever step they left.
	verified := int64(0)
	step := func() { op.progress("Verify queued inputs", "", "input", float64(verified), float64(len(rows)), true) }
	step()
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
			if ctx.Err() != nil {
				break
			}
			failure = errors.Join(failure, err)
			continue
		}
		defer input.Close()
		adapter = input.Adapter
		// A queued Conductor check already names its sessions. Listing every
		// session's message statistics can consume the entire batch budget on
		// a large live database before the first queued input is reached.
		if conductor, ok := adapter.(*conductorAdapter); ok {
			conductor.selected = map[string]bool{}
			for unit := range selected {
				if separator := strings.LastIndex(unit, ":sessions:"); separator >= 0 {
					conductor.selected[unit[separator+len(":sessions:"):]] = true
				}
			}
		}
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
			if ctx.Err() != nil {
				break
			}
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
			verified++
			step()
			if result.Outcome == "read_error" {
				failure = errors.Join(failure, fmt.Errorf("%s: %s", source.Name, result.Detail))
			}
		}
	}
	// Unfinished inputs remain pending with backoff, including when discovery
	// exhausted this pass's budget. Use the parent so the local deadline cannot
	// prevent saving the retry or leave the same inputs blocking every batch.
	// A service stop still cancels the pass and leaves its durable queue alone.
	step()
	reason := "source_unavailable"
	if ctx.Err() == context.DeadlineExceeded && parent.Err() == nil {
		reason = "time_budget"
	}
	for source, units := range remaining {
		for unit := range units {
			if parent.Err() != nil {
				break
			}
			_, err := s.Catalog.DB.ExecContext(parent, "UPDATE sync_verification_progress SET next_at=?,reason=? WHERE host_id=? AND source_name=? AND unit=? AND heavy=1", formatTime(time.Now().Add(auditRetry)), reason, currentHost().ID, source, unit)
			if err != nil {
				return err
			}
		}
	}
	// Reaching our own batch limit is a deferral, just as in auditUnit. Real
	// read failures and cancellation of the parent remain operation failures.
	return errors.Join(failure, parent.Err())
}
