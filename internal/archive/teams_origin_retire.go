package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

// Retire only complete, non-held scans with independently retained exact
// accepted observations. Inspect at most limit candidates, rotating the scan
// cursor so an older blocked run cannot starve newer safe candidates. One
// transaction inspects at most eight 2 MiB chains and 8×2,001 outbox rows.
func teamsRetireRedundantOriginRunsIn(ctx context.Context, tx *sql.Tx, d LibraryDescriptor, credential *teams.EnrollmentCredential, limit int) (int, error) {
	if credential == nil || limit < 1 || limit > 8 {
		return 0, errTeamsConsentStale
	}
	identity, _ := json.Marshal([]string{credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance})
	cursorKey := "teams_origin_retire_cursor:" + teamsPayloadHash(identity)
	var cursor int64
	e := tx.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM meta WHERE key=?`, cursorKey).Scan(&cursor)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return 0, e
	}
	type candidate struct {
		id    int64
		key   string
		start []byte
	}
	var list []candidate
	query := func(after int64, max int) error {
		rows, err := tx.QueryContext(ctx, `SELECT rowid,run_key,start_receipt_json FROM teams_origin_runs WHERE origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND token<>'baseline' AND state='completed' AND rowid>? ORDER BY rowid LIMIT ?`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, after, max)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v candidate
			if err = rows.Scan(&v.id, &v.key, &v.start); err != nil {
				return err
			}
			list = append(list, v)
		}
		return rows.Err()
	}
	if e = query(cursor, limit); e != nil {
		return 0, e
	}
	if len(list) == 0 && cursor != 0 {
		if e = query(0, limit); e != nil {
			return 0, e
		}
	}
	retired := 0
	for _, v := range list {
		cursor = v.id
		var start sourcewire.RecoveryStartReceipt
		if !teams.ValidUniqueJSON(v.start) || json.Unmarshal(v.start, &start) != nil {
			continue
		}
		rows, err := tx.QueryContext(ctx, `SELECT page_index,request_json,receipt_json FROM teams_origin_run_pages WHERE run_key=? ORDER BY page_index`, v.key)
		if err != nil {
			return retired, err
		}
		var requests []sourcewire.RecoveryPageRequest
		var pages []sourcewire.RecoveryPageReceipt
		valid := true
		for rows.Next() {
			var idx int
			var raw, receipt []byte
			if err = rows.Scan(&idx, &raw, &receipt); err != nil {
				rows.Close()
				return retired, err
			}
			req, x := sourcewire.DecodeRecoveryPage(raw)
			var page sourcewire.RecoveryPageReceipt
			if idx != len(requests) || x != nil || !teams.ValidUniqueJSON(receipt) || json.Unmarshal(receipt, &page) != nil {
				valid = false
			}
			requests = append(requests, req)
			pages = append(pages, page)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return retired, err
		}
		if !valid {
			continue
		}
		scan, err := teamsBuildRecoveryOriginScan(start, requests, pages)
		if err != nil || scan.held {
			continue
		}
		// A single bounded read builds the lookup for every origin in this scan.
		observed, err := tx.QueryContext(ctx, `SELECT run_id,observation_json,receipt_json,state FROM teams_origin_outbox WHERE origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? LIMIT 2001`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance)
		if err != nil {
			return retired, err
		}
		matched := map[string]bool{}
		own, ownValid, total := 0, true, 0
		for observed.Next() {
			var run, state string
			var body, receipt []byte
			if err = observed.Scan(&run, &body, &receipt, &state); err != nil {
				observed.Close()
				return retired, err
			}
			total++
			if total > 2000 {
				ownValid = false
				break
			}
			observation, x := sourcewire.DecodeProtectionObservation(body)
			var result sourcewire.ProtectionReceipt
			expectedID, idErr := sourcewire.ProtectionObservationID(observation, credential.InstallationID, d.ID)
			good := x == nil && idErr == nil && expectedID == observation.ObservationID && teams.ValidUniqueJSON(receipt) && json.Unmarshal(receipt, &result) == nil && teamsOriginReceiptMatches(observation, body, result) && state == "accepted" && result.Status == "accepted" && !result.SafetyPending
			if run == start.RunID {
				own++
				if !good {
					ownValid = false
				}
			}
			if good {
				if prior, ok := scan.byToken[observation.Token]; ok && prior.Witness == observation.Witness && prior.Revision == observation.Revision && prior.Protection == observation.Protection && prior.EvidenceRevision == observation.EvidenceRevision {
					matched[observation.Token] = true
				}
			}
		}
		err = observed.Err()
		observed.Close()
		if err != nil {
			return retired, err
		}
		if !ownValid || own == 0 || len(matched) != len(scan.byToken) {
			continue
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM teams_origin_runs WHERE run_key=? AND state='completed'`, v.key)
		if err != nil {
			return retired, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return retired, err
		}
		retired += int(n)
	}
	if len(list) > 0 {
		_, e = tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, cursorKey, cursor)
		if e != nil {
			return retired, e
		}
	}
	return retired, nil
}
