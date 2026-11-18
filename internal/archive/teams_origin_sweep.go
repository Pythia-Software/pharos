package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

// Sweep only stale transport-only prefixes. Any retained origin observation,
// held response, unknown/private evidence or malformed page remains held.
func teamsSweepOriginPartialsIn(ctx context.Context, tx *sql.Tx, d LibraryDescriptor, credential *teams.EnrollmentCredential, scope teamsOriginBatchScope) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT run_key,state,start_receipt_json,expires_at,inventory_sha256,settings_revision,source_revision,privacy_revision,cache_generation FROM teams_origin_runs WHERE origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND token<>'baseline' AND state IN ('prepared','started','ready') ORDER BY rowid LIMIT 33`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance)
	if err != nil {
		return false, err
	}
	type candidate struct {
		key, hash, state                               string
		start                                          []byte
		expires, settings, source, privacy, generation int64
	}
	var list []candidate
	for rows.Next() {
		var v candidate
		if err = rows.Scan(&v.key, &v.state, &v.start, &v.expires, &v.hash, &v.settings, &v.source, &v.privacy, &v.generation); err != nil {
			rows.Close()
			return false, err
		}
		if v.expires <= time.Now().UTC().Unix() || v.hash != scope.inventoryHash || v.settings != scope.settingsRevision || v.source != scope.sourceRevision || v.privacy != scope.privacyRevision || v.generation != scope.generation {
			list = append(list, v)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	changed := false
	for _, v := range list {
		// An existing held state may represent malformed or unrecorded
		// evidence; without a trustworthy disposition it is not transport-only.
		safe := v.state != "held"
		retained, cleanComplete := false, false
		var start sourcewire.RecoveryStartReceipt
		if len(v.start) > 0 && (!teams.ValidUniqueJSON(v.start) || json.Unmarshal(v.start, &start) != nil || start.Held || start.Status != "proof_required" || !start.ExternalProofRequired || start.RunID == "" || start.Generation < 1 || start.ServerTime <= 0 || start.ExpiresAt <= start.ServerTime || start.UnacceptedFloor < 0) {
			safe = false
		}
		if start.RunID != "" {
			var has bool
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM teams_origin_outbox WHERE run_id=? AND origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=?)`, start.RunID, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance).Scan(&has); err != nil {
				return false, err
			}
			if has {
				retained = true
				safe = false
				if v.expires <= time.Now().UTC().Unix() {
					// Only a format-35 never-sent marker proves this row has not
					// left the host. Legacy rows without one stay ambiguous.
					if _, err = tx.ExecContext(ctx, `UPDATE teams_origin_attempts SET terminal_code='first_rejected_source_issuance_expired' WHERE first_attempt_at=0 AND terminal_code='' AND EXISTS(SELECT 1 FROM teams_origin_outbox o WHERE o.observation_id=teams_origin_attempts.observation_id AND o.run_id=? AND o.origin=? AND o.org_id=? AND o.person_id=? AND o.installation_id=? AND o.library_id=? AND o.instance_id=? AND o.state='prepared')`, start.RunID, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance); err != nil {
						return false, err
					}
					if _, err = tx.ExecContext(ctx, `UPDATE teams_origin_outbox SET state='held' WHERE run_id=? AND origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND state='prepared' AND EXISTS(SELECT 1 FROM teams_origin_attempts a WHERE a.observation_id=teams_origin_outbox.observation_id AND a.first_attempt_at=0 AND a.terminal_code='first_rejected_source_issuance_expired')`, start.RunID, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance); err != nil {
						return false, err
					}
				}
			}
		}
		pages, e := tx.QueryContext(ctx, `SELECT page_index,request_json,receipt_json FROM teams_origin_run_pages WHERE run_key=? ORDER BY page_index`, v.key)
		if e != nil {
			return false, e
		}
		var requests []sourcewire.RecoveryPageRequest
		var receipts []sourcewire.RecoveryPageReceipt
		pending := false
		for pages.Next() {
			var idx int
			var requestRaw, receiptRaw []byte
			if e = pages.Scan(&idx, &requestRaw, &receiptRaw); e != nil {
				pages.Close()
				return false, e
			}
			request, x := sourcewire.DecodeRecoveryPage(requestRaw)
			if idx != len(requests) || x != nil || pending || start.RunID == "" || request.RunID != start.RunID || request.Family != "origins" {
				safe = false
			}
			requests = append(requests, request)
			if len(receiptRaw) == 0 {
				pending = true
				continue
			}
			var page sourcewire.RecoveryPageReceipt
			if !teams.ValidUniqueJSON(receiptRaw) || json.Unmarshal(receiptRaw, &page) != nil {
				safe = false
			}
			receipts = append(receipts, page)
		}
		e = pages.Err()
		pages.Close()
		if e != nil {
			return false, e
		}
		if len(requests) > 0 {
			expected := ""
			if len(receipts) > 0 {
				expected = receipts[len(receipts)-1].NextCursor
			}
			if pending && (requests[len(requests)-1].Cursor != expected || (len(receipts) > 0 && expected == "")) {
				safe = false
			}
			if len(receipts) > 0 {
				actualNext := receipts[len(receipts)-1].NextCursor
				if actualNext != "" && len(receipts[len(receipts)-1].Items) == 0 {
					safe = false
				}
				for _, request := range requests[:len(receipts)] {
					if actualNext != "" && actualNext == request.Cursor {
						safe = false
					}
				}
				prefix := append([]sourcewire.RecoveryPageReceipt(nil), receipts...)
				prefix[len(prefix)-1].NextCursor = ""
				scan, x := teamsBuildRecoveryOriginScan(start, requests[:len(receipts)], prefix)
				if x != nil || scan.held {
					safe = false
				} else {
					cleanComplete = !pending && len(receipts) == len(requests) && actualNext == ""
					for _, origin := range scan.byToken {
						if origin.Protection != "non_private" || origin.FlagFloor != "" || origin.Classification.State != "clear" {
							safe = false
						}
					}
				}
			} else if len(requests) != 1 || requests[0].Cursor != "" {
				safe = false
			}
		}

		var result sql.Result
		if safe {
			result, err = tx.ExecContext(ctx, `DELETE FROM teams_origin_runs WHERE run_key=? AND state IN ('prepared','started','ready')`, v.key)
		} else if retained && cleanComplete {
			result, err = tx.ExecContext(ctx, `UPDATE teams_origin_runs SET state='completed' WHERE run_key=? AND state IN ('prepared','started','ready')`, v.key)
		} else {
			result, err = tx.ExecContext(ctx, `UPDATE teams_origin_runs SET state='held' WHERE run_key=? AND state IN ('prepared','started','ready')`, v.key)
		}
		if err != nil {
			return false, err
		}
		n, e := result.RowsAffected()
		if e != nil {
			return false, e
		}
		changed = changed || n > 0
	}
	return changed, nil
}

// Any attempted nonterminal observation is ambiguous independent of census.
func teamsOriginAmbiguousIn(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, d LibraryDescriptor, credential *teams.EnrollmentCredential) (bool, error) {
	var ambiguous bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM teams_origin_outbox o LEFT JOIN teams_origin_attempts a ON a.observation_id=o.observation_id WHERE o.origin=? AND o.org_id=? AND o.person_id=? AND o.installation_id=? AND o.library_id=? AND o.instance_id=? AND o.state IN ('prepared','held') AND o.receipt_json IS NULL AND (a.observation_id IS NULL OR (a.first_attempt_at>0 AND a.terminal_code NOT IN ('first_rejected_source_issuance_expired','first_rejected_source_resync_required','first_rejected_stale_protection_revision'))) )`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance).Scan(&ambiguous)
	return ambiguous, err
}
