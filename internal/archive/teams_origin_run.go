package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

type teamsPilotRun struct {
	key, state          string
	start, startReceipt []byte
	expires             int64
}
type teamsPilotPage struct {
	index            int
	request, receipt []byte
}

var teamsPilotMaxRetainedRuns = 32

// One bounded 038 page is accepted per call. Commands are committed before
// sending, and the exact pending bytes are replayed after an uncertain reply.
func (c *Catalog) teamsRecoveryPageForScope(ctx context.Context, cfg Config, client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential, workID string, snapshot teamsV2Inventory, scope teamsOriginBatchScope, token string, check func(context.Context, *sql.Tx) error) (*teamsRecoveryOriginScan, error) {
	if !client.MatchesCredential(credential) {
		return nil, errTeamsConsentStale
	}
	lease, err := c.teamsSharingLease()
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	ctx, release, err := teamsCensusCaptureLease(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var row teamsPilotRun
	err = c.writeTransaction(ctx, "teams-origin-run-prepare", func(tx *sql.Tx) error {
		if e := check(ctx, tx); e != nil {
			return e
		}
		// Any attempted, nonterminal 031 in this enrollment has priority over a
		// new scan, including rows from an older census or a held spelling.
		ambiguous, e := teamsOriginAmbiguousIn(ctx, tx, d, credential)
		if e != nil {
			return e
		}
		if ambiguous {
			return errTeamsPilotPending
		}
		// A previously held scan is not retried merely because its lease
		// expired. A fresh census may create a separately scoped attempt.
		var heldCurrent bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM teams_origin_runs WHERE origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND work_id=? AND snapshot_id=? AND inventory_sha256=? AND settings_revision=? AND source_revision=? AND privacy_revision=? AND cache_generation=? AND token=? AND state='held')`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, workID, scope.snapshotID, scope.inventoryHash, scope.settingsRevision, snapshot.SourceRevision, snapshot.PrivacyRevision, snapshot.Generation, token).Scan(&heldCurrent); e != nil {
			return e
		}
		if heldCurrent {
			return errTeamsPilotPending
		}
		swept, e := teamsSweepOriginPartialsIn(ctx, tx, d, credential, scope)
		if e != nil {
			return e
		}
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM teams_origin_runs WHERE origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND work_id=? AND snapshot_id=? AND inventory_sha256=? AND settings_revision=? AND source_revision=? AND privacy_revision=? AND cache_generation=? AND token=? AND state='held')`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, workID, scope.snapshotID, scope.inventoryHash, scope.settingsRevision, snapshot.SourceRevision, snapshot.PrivacyRevision, snapshot.Generation, token).Scan(&heldCurrent); e != nil {
			return e
		}
		if heldCurrent {
			row.state = "held"
			return nil
		}
		e = tx.QueryRowContext(ctx, `SELECT run_key,state,start_json,start_receipt_json,expires_at FROM teams_origin_runs WHERE origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND work_id=? AND snapshot_id=? AND inventory_sha256=? AND settings_revision=? AND source_revision=? AND privacy_revision=? AND cache_generation=? AND token=? ORDER BY rowid DESC LIMIT 1`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, workID, scope.snapshotID, scope.inventoryHash, scope.settingsRevision, snapshot.SourceRevision, snapshot.PrivacyRevision, snapshot.Generation, token).Scan(&row.key, &row.state, &row.start, &row.startReceipt, &row.expires)
		restart := false
		if e == nil {
			if row.state == "held" && row.expires > time.Now().UTC().Unix() {
				return nil
			}
			if row.state != "completed" && row.expires > time.Now().UTC().Unix() {
				return nil
			}
			restart = true
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var otherActive bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM teams_origin_runs WHERE origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND state IN ('prepared','started','ready'))`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance).Scan(&otherActive); e != nil {
			return e
		}
		if otherActive {
			return errTeamsPilotPending
		}
		if _, e = teamsRetireRedundantOriginRunsIn(ctx, tx, d, credential, 8); e != nil {
			return e
		}
		var n int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM teams_origin_runs WHERE origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=?`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance).Scan(&n); e != nil {
			return e
		}
		if n >= teamsPilotMaxRetainedRuns {
			return errTeamsPilotPending
		}
		var priorAccepted bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM teams_origin_outbox WHERE origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND state='accepted')`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance).Scan(&priorAccepted); e != nil {
			return e
		}
		identity, _ := json.Marshal(struct {
			Origin, Org, Person, Installation, Library, Instance, Work, Snapshot, Inventory, Token string
			Settings, Source, Privacy, Generation                                                  int64
		}{credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, workID, scope.snapshotID, scope.inventoryHash, token, scope.settingsRevision, snapshot.SourceRevision, snapshot.PrivacyRevision, snapshot.Generation})
		command := sourcewire.RecoveryStart{Version: 1, Contract: sourcewire.SourceRecoveryContract, IssuedAt: time.Now().UTC().Unix(), StartID: randomUUID(), Purpose: "protection", Restart: restart || priorAccepted || swept}
		row.key = teamsPayloadHash(append(identity, []byte(command.StartID)...))
		row.start, e = json.Marshal(command)
		if e != nil {
			return e
		}
		if _, e = sourcewire.DecodeRecoveryStart(row.start); e != nil {
			return errTeamsConsentStale
		}
		row.expires = command.IssuedAt + int64(sourcewire.RecoveryHorizon/time.Second)
		row.state = "prepared"
		row.startReceipt = nil
		_, e = tx.ExecContext(ctx, `INSERT INTO teams_origin_runs(run_key,origin,org_id,person_id,installation_id,library_id,instance_id,work_id,snapshot_id,inventory_sha256,settings_revision,source_revision,privacy_revision,cache_generation,token,start_json,expires_at,state) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'prepared')`, row.key, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, workID, scope.snapshotID, scope.inventoryHash, scope.settingsRevision, snapshot.SourceRevision, snapshot.PrivacyRevision, snapshot.Generation, token, row.start, row.expires)
		return e
	})
	if err != nil {
		return nil, err
	}
	if row.state == "held" {
		return nil, errTeamsPilotPending
	}
	recheck := func() error {
		tx, e := c.reader().BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		return check(ctx, tx)
	}
	hold := func() error {
		return c.writeTransaction(ctx, "teams-origin-run-held", func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, `UPDATE teams_origin_runs SET state='held' WHERE run_key=? AND state IN ('prepared','started','ready')`, row.key)
			return e
		})
	}
	var start sourcewire.RecoveryStartReceipt
	if len(row.startReceipt) == 0 {
		if err = recheck(); err != nil {
			return nil, err
		}
		if err = c.teamsPilotCheckpointCurrent(ctx, d); err != nil {
			return nil, err
		}
		start, err = client.StartSourceRecovery(ctx, d.ID, row.start)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			if errors.Is(err, teams.ErrInvalidRecoveryStart) {
				if e := hold(); e != nil {
					return nil, e
				}
				return nil, errTeamsPilotPending
			}
			return nil, errTeamsPilotRetry
		}
		if start.Status == "busy" || start.Status == "cleanup_pending" || start.Status == "restart_exhausted" || start.Status == "capacity_blocked" {
			return nil, errTeamsPilotPending
		}
		if (start.Status != "proof_required" && start.Status != "held") || start.RunID == "" || start.ExpiresAt <= time.Now().UTC().Unix() {
			if e := hold(); e != nil {
				return nil, e
			}
			return nil, errTeamsPilotPending
		}
		row.startReceipt, err = json.Marshal(start)
		if err != nil {
			return nil, err
		}
		err = c.writeTransaction(ctx, "teams-origin-run-started", func(tx *sql.Tx) error {
			if e := check(ctx, tx); e != nil {
				return e
			}
			res, e := tx.ExecContext(ctx, `UPDATE teams_origin_runs SET state='started',start_receipt_json=?,expires_at=? WHERE run_key=? AND state='prepared' AND start_json=?`, row.startReceipt, start.ExpiresAt, row.key, row.start)
			if e != nil {
				return e
			}
			n, e := res.RowsAffected()
			if e != nil || n != 1 {
				return errTeamsConsentStale
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		row.state = "started"
		row.expires = start.ExpiresAt
	} else if !teams.ValidUniqueJSON(row.startReceipt) || json.Unmarshal(row.startReceipt, &start) != nil {
		return nil, errTeamsConsentStale
	}
	if (start.Status != "proof_required" && start.Status != "held") || !start.ExternalProofRequired || start.Generation < 1 || start.RunID == "" || start.ExpiresAt <= time.Now().UTC().Unix() || start.ExpiresAt != row.expires {
		return nil, errTeamsPilotPending
	}
	var pages []teamsPilotPage
	load := func() error {
		rows, e := c.reader().QueryContext(ctx, `SELECT page_index,request_json,receipt_json FROM teams_origin_run_pages WHERE run_key=? ORDER BY page_index`, row.key)
		if e != nil {
			return e
		}
		defer rows.Close()
		pages = nil
		for rows.Next() {
			var p teamsPilotPage
			if e = rows.Scan(&p.index, &p.request, &p.receipt); e != nil {
				return e
			}
			pages = append(pages, p)
		}
		return rows.Err()
	}
	if err = load(); err != nil {
		return nil, err
	}
	if len(pages) == 0 {
		request := sourcewire.RecoveryPageRequest{Version: 1, Contract: sourcewire.SourceRecoveryContract, IssuedAt: max(time.Now().UTC().Unix(), start.ServerTime), RunID: start.RunID, Family: "origins"}
		raw, e := json.Marshal(request)
		if e != nil {
			return nil, e
		}
		if _, e = sourcewire.DecodeRecoveryPage(raw); e != nil {
			return nil, errTeamsConsentStale
		}
		err = c.writeTransaction(ctx, "teams-origin-page-prepare", func(tx *sql.Tx) error {
			if e := check(ctx, tx); e != nil {
				return e
			}
			_, e := tx.ExecContext(ctx, `INSERT INTO teams_origin_run_pages(run_key,page_index,request_json) SELECT ?,0,? WHERE EXISTS(SELECT 1 FROM teams_origin_runs WHERE run_key=? AND state='started' AND start_receipt_json=?)`, row.key, raw, row.key, row.startReceipt)
			return e
		})
		if err != nil {
			return nil, err
		}
		if err = load(); err != nil {
			return nil, err
		}
	}
	if len(pages) == 0 || len(pages) > teamsRecoveryMaxOriginPages {
		if e := hold(); e != nil {
			return nil, e
		}
		return nil, errTeamsPilotPending
	}
	requests := make([]sourcewire.RecoveryPageRequest, 0, len(pages))
	receipts := make([]sourcewire.RecoveryPageReceipt, 0, len(pages))
	for i, p := range pages {
		if p.index != i {
			if e := hold(); e != nil {
				return nil, e
			}
			return nil, errTeamsPilotPending
		}
		req, e := sourcewire.DecodeRecoveryPage(p.request)
		if e != nil {
			if e = hold(); e != nil {
				return nil, e
			}
			return nil, errTeamsPilotPending
		}
		requests = append(requests, req)
		if len(p.receipt) == 0 {
			if i != len(pages)-1 || row.state != "started" {
				if e = hold(); e != nil {
					return nil, e
				}
				return nil, errTeamsPilotPending
			}
			break
		}
		var receipt sourcewire.RecoveryPageReceipt
		if !teams.ValidUniqueJSON(p.receipt) || json.Unmarshal(p.receipt, &receipt) != nil {
			if e = hold(); e != nil {
				return nil, e
			}
			return nil, errTeamsPilotPending
		}
		receipts = append(receipts, receipt)
	}
	if len(receipts) == len(requests) {
		scan, e := teamsBuildRecoveryOriginScan(start, requests, receipts)
		if e != nil || (row.state != "ready" && row.state != "completed") {
			if e = hold(); e != nil {
				return nil, e
			}
			return nil, errTeamsPilotPending
		}
		if scan.held {
			if token == "batch-safety" {
				// Retain this signed held scan, but permit a later independent
				// status scan. No origin or receipt is changed here.
				if e = c.writeTransaction(ctx, "teams-origin-safety-held", func(tx *sql.Tx) error {
					_, x := tx.ExecContext(ctx, `UPDATE teams_origin_runs SET state='completed' WHERE run_key=? AND token='batch-safety' AND state='ready'`, row.key)
					return x
				}); e != nil {
					return nil, e
				}
			}
			return nil, errTeamsPilotPending
		}
		return scan, nil
	}
	pending := pages[len(pages)-1]
	if err = recheck(); err != nil {
		return nil, err
	}
	if err = c.teamsPilotCheckpointCurrent(ctx, d); err != nil {
		return nil, err
	}
	page, err := client.PageSourceRecovery(ctx, d.ID, pending.request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if errors.Is(err, teams.ErrInvalidRecoveryPage) {
			if e := hold(); e != nil {
				return nil, e
			}
			return nil, errTeamsPilotPending
		}
		return nil, errTeamsPilotRetry
	}
	if page.Status == "capacity_blocked" {
		return nil, errTeamsPilotRetry
	}
	if err = c.teamsPilotCheckpointCurrent(ctx, d); err != nil {
		return nil, err
	}
	// Validate the accepted prefix as a complete scan only for checking; the
	// actual continuation cursor is retained and never treated as terminal.
	receipts = append(receipts, page)
	prefix := append([]sourcewire.RecoveryPageReceipt(nil), receipts...)
	prefix[len(prefix)-1].NextCursor = ""
	if _, e := teamsBuildRecoveryOriginScan(start, requests, prefix); e != nil || (page.NextCursor != "" && (len(page.Items) == 0 || len(pages) >= teamsRecoveryMaxOriginPages)) {
		if e = hold(); e != nil {
			return nil, e
		}
		return nil, errTeamsPilotPending
	}
	for _, req := range requests {
		if page.NextCursor != "" && page.NextCursor == req.Cursor {
			if e := hold(); e != nil {
				return nil, e
			}
			return nil, errTeamsPilotPending
		}
	}
	receiptRaw, err := json.Marshal(page)
	if err != nil {
		return nil, err
	}
	var next []byte
	if page.NextCursor != "" {
		next, err = json.Marshal(sourcewire.RecoveryPageRequest{Version: 1, Contract: sourcewire.SourceRecoveryContract, IssuedAt: max(time.Now().UTC().Unix(), page.ServerTime), RunID: start.RunID, Family: "origins", Cursor: page.NextCursor})
		if err != nil {
			return nil, err
		}
		if _, err = sourcewire.DecodeRecoveryPage(next); err != nil {
			if e := hold(); e != nil {
				return nil, e
			}
			return nil, errTeamsPilotPending
		}
	}
	err = c.writeTransaction(ctx, "teams-origin-page-receipt", func(tx *sql.Tx) error {
		if e := check(ctx, tx); e != nil {
			return e
		}
		res, e := tx.ExecContext(ctx, `UPDATE teams_origin_run_pages SET receipt_json=? WHERE run_key=? AND page_index=? AND request_json=? AND receipt_json IS NULL AND EXISTS(SELECT 1 FROM teams_origin_runs WHERE run_key=? AND state='started' AND start_receipt_json=?)`, receiptRaw, row.key, pending.index, pending.request, row.key, row.startReceipt)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil || n != 1 {
			return errTeamsConsentStale
		}
		if len(next) > 0 {
			_, e = tx.ExecContext(ctx, `INSERT INTO teams_origin_run_pages(run_key,page_index,request_json) VALUES(?,?,?)`, row.key, pending.index+1, next)
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE teams_origin_runs SET state='ready' WHERE run_key=? AND state='started'`, row.key)
		return e
	})
	if err != nil {
		return nil, err
	}
	if len(next) > 0 {
		return nil, errTeamsPilotMore
	}
	scan, err := teamsBuildRecoveryOriginScan(start, requests, receipts)
	if err != nil {
		return nil, err
	}
	if scan.held {
		return nil, errTeamsPilotPending
	}
	return scan, nil
}
