package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

// A fallback to manifest v1 never authorizes source-recovery effects.
// The pilot has no production activation or advertised capabilities. Tests
// may enable this hook only with synthetic census and checkpoint evidence.
var teamsSourcePilotEnabled = false

var teamsIndependentSourceCarrierReady = func(m teams.CapabilityManifest, negotiatedV2 bool) bool {
	return negotiatedV2 && slices.Contains(m.Capabilities, "source_snapshot_v2") && slices.Contains(m.V2OnlyFeatures, "source_protection_v1") && slices.Contains(m.V2OnlyFeatures, "source_recovery_v1")
}
var errTeamsPilotMore = errors.New("source protection is progressing")
var errTeamsPilotRetry = errors.New("source protection connection needs retry")

// teamsRunIndependentSourcePilot is library-scoped: it neither requires nor
// changes numeric work, and uses only the separate source-transfer choice.
func (c *Catalog) teamsRunIndependentSourcePilot(ctx context.Context, cfg Config, client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential, manifest teams.CapabilityManifest, negotiatedV2 bool) error {
	if !teamsSourcePilotEnabled || !client.MatchesCredential(credential) || !teamsIndependentSourceCarrierReady(manifest, negotiatedV2) {
		return errTeamsSourceUnavailable
	}
	if err := c.teamsPilotCheckpointCurrent(ctx, d); err != nil {
		return err
	}
	var uncertain string
	err := c.reader().QueryRowContext(ctx, `SELECT o.observation_id FROM teams_origin_outbox o JOIN teams_origin_attempts a ON a.observation_id=o.observation_id WHERE o.origin=? AND o.org_id=? AND o.person_id=? AND o.installation_id=? AND o.library_id=? AND o.instance_id=? AND o.work_id='' AND o.snapshot_id='' AND o.state='prepared' AND a.first_attempt_at>0 AND a.terminal_code='' AND o.receipt_json IS NULL ORDER BY a.first_attempt_at,o.observation_id LIMIT 1`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance).Scan(&uncertain)
	if err == nil {
		replayCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		err = c.sendTeamsLocalOrigin(replayCtx, cfg, client, d, credential, teamsV2Inventory{}, teamsOriginBatchScope{}, uncertain, true)
		cancel()
		if err != nil {
			return err
		}
		return errTeamsPilotMore
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	censusCtx, release, err := teamsCensusCaptureLease(ctx, cfg)
	if err != nil {
		return err
	}
	tx, err := c.reader().BeginTx(censusCtx, nil)
	if err != nil {
		release()
		return err
	}
	snapshot, scope, err := teamsLocalSourceCensusIn(censusCtx, tx, cfg, d, credential)
	_ = tx.Rollback()
	release()
	if errors.Is(err, errTeamsProtectionHeld) || errors.Is(err, errTeamsSnapshotChanged) {
		return c.teamsProduceRetainedBaseline(ctx, cfg, client, d, credential)
	}
	if err != nil {
		return err
	}
	// A previously attempted request blocks a new scan until its exact
	// outcome is known. An expired attempt remains parked for read-only recovery.

	// An older census or a held spelling cannot hide an attempted 031 whose
	// terminal receipt is still unknown. Never start a new scan around it.
	ambiguous, err := teamsOriginAmbiguousIn(ctx, c.reader(), d, credential)
	if err != nil {
		return err
	}
	if ambiguous {
		return errTeamsPilotPending
	}
	censusCtx, release, err = teamsCensusCaptureLease(ctx, cfg)
	if err != nil {
		return err
	}
	tx, err = c.reader().BeginTx(censusCtx, nil)
	if err != nil {
		release()
		return err
	}
	current, fresh, err := teamsLocalSourceCensusIn(censusCtx, tx, cfg, d, credential)
	if err != nil || fresh != scope || !teamsSamePilotSnapshot(current, snapshot) {
		_ = tx.Rollback()
		release()
		if err != nil {
			return err
		}
		return errTeamsSnapshotChanged
	}
	var pending []sourcewire.Item
	type acceptedOrigin struct {
		id, run string
		state   teamsLocalOriginState
	}
	var accepted []acceptedOrigin
	needsSafetyRecheck := false
	for _, item := range snapshot.Items {
		if item.Kind != "codex_thread" {
			continue
		}
		var id, run string
		e := tx.QueryRowContext(ctx, `SELECT observation_id,run_id FROM teams_origin_outbox WHERE origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND work_id='' AND snapshot_id='' AND inventory_sha256=? AND settings_revision=? AND source_revision=? AND privacy_revision=? AND cache_generation=? AND token=? AND state='accepted' ORDER BY rowid DESC LIMIT 1`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, scope.inventoryHash, scope.settingsRevision, scope.sourceRevision, scope.privacyRevision, scope.generation, item.Token).Scan(&id, &run)
		if errors.Is(e, sql.ErrNoRows) {
			pending = append(pending, item)
			continue
		}
		if e != nil {
			_ = tx.Rollback()
			release()
			return e
		}
		state, e := teamsLocalOriginStateCheckedIn(censusCtx, tx, cfg, d, credential, snapshot, scope, id, false, true, &item, false)
		if e != nil {
			_ = tx.Rollback()
			release()
			return e
		}
		if state.safetyPending {
			needsSafetyRecheck = true
		}
		accepted = append(accepted, acceptedOrigin{id, run, state})
	}
	safetyAlreadyProved := false
	if len(pending) == 0 && needsSafetyRecheck {
		prior, e := teamsCompletedSafetyScanIn(censusCtx, tx, d, credential, scope)
		if e != nil {
			_ = tx.Rollback()
			release()
			return e
		}
		if prior != nil && !prior.held {
			safetyAlreadyProved = true
			for _, a := range accepted {
				observation, e := sourcewire.DecodeProtectionObservation(a.state.body)
				if e != nil {
					safetyAlreadyProved = false
					break
				}
				current, ok := prior.byToken[observation.Token]
				if !ok || current.Witness != observation.Witness || current.Revision != observation.Revision || current.Protection != observation.Protection || current.EvidenceRevision != observation.EvidenceRevision {
					safetyAlreadyProved = false
					break
				}
			}
		}
	}
	_ = tx.Rollback()
	release()
	if safetyAlreadyProved {
		return c.completeTeamsLocalOriginRun(ctx, cfg, d, credential, snapshot, scope, true)
	}
	if len(pending) == 0 && !needsSafetyRecheck {
		return c.completeTeamsLocalOriginRun(ctx, cfg, d, credential, snapshot, scope, false)
	}
	check := func(ctx context.Context, tx *sql.Tx) error {
		current, fresh, e := teamsLocalSourceCensusIn(ctx, tx, cfg, d, credential)
		if e != nil {
			return e
		}
		if fresh != scope || !teamsSamePilotSnapshot(current, snapshot) {
			return errTeamsSnapshotChanged
		}
		return nil
	}
	stageToken := "batch"
	if needsSafetyRecheck {
		// The accepted receipt is immutable and records the earlier pending
		// state. Close the pre-send scan, then obtain a new signed 038 view.
		lease, e := c.teamsSharingLease()
		if e != nil {
			return e
		}
		lockedCtx, unlock, e := teamsCensusCaptureLease(ctx, cfg)
		if e != nil {
			lease.Close()
			return e
		}
		e = c.writeTransaction(lockedCtx, "teams-origin-safety-recheck", func(tx *sql.Tx) error {
			if e := check(lockedCtx, tx); e != nil {
				return e
			}
			_, e := tx.ExecContext(lockedCtx, `UPDATE teams_origin_runs SET state='completed' WHERE origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND token IN ('batch','batch-safety') AND state='ready'`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance)
			return e
		})
		unlock()
		lease.Close()
		if e != nil {
			return e
		}
		stageToken = "batch-safety"
	}
	scan, err := c.teamsRecoveryPageForScope(ctx, cfg, client, d, credential, "", snapshot, scope, stageToken, check)
	if err != nil {
		return err
	}
	for _, prior := range accepted {
		observation, e := sourcewire.DecodeProtectionObservation(prior.state.body)
		if e != nil {
			return errTeamsConsentStale
		}
		revision, protection, e := scan.lookup(observation.Token, observation.Witness)
		if prior.run == scan.start.RunID {
			if e != nil || revision != observation.ExpectedRevision {
				return errTeamsPilotPending
			}
			continue
		}
		if e != nil || revision != observation.Revision || protection != observation.Protection {
			return errTeamsPilotPending
		}
		origin, found := scan.byToken[observation.Token]
		if !found || origin.Witness != observation.Witness || origin.EvidenceRevision != observation.EvidenceRevision {
			return errTeamsPilotPending
		}
	}
	if needsSafetyRecheck {
		lease, e := c.teamsSharingLease()
		if e != nil {
			return e
		}
		lockedCtx, unlock, e := teamsCensusCaptureLease(ctx, cfg)
		if e != nil {
			lease.Close()
			return e
		}
		e = c.writeTransaction(lockedCtx, "teams-origin-safety-retire", func(tx *sql.Tx) error {
			if e := check(lockedCtx, tx); e != nil {
				return e
			}
			var run string
			if e := tx.QueryRowContext(lockedCtx, `SELECT run_key FROM teams_origin_runs WHERE origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND token='batch-safety' AND state='ready' AND json_extract(start_receipt_json,'$.run_id')=?`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, scan.start.RunID).Scan(&run); e != nil {
				return e
			}
			return teamsPruneSupersededBaselineRuns(lockedCtx, tx, d, credential, run, "batch-safety", scan, true)
		})
		unlock()
		lease.Close()
		if e != nil {
			return e
		}
	}
	if len(pending) == 0 {
		return c.completeTeamsLocalOriginRun(ctx, cfg, d, credential, snapshot, scope, needsSafetyRecheck)
	}
	// One new observation at a time fits the format-35 durable response
	// reserve. Exact journal bytes survive restart and ambiguous responses.
	bodies, err := c.journalTeamsLocalOriginBatch(ctx, cfg, d, credential, snapshot, []string{pending[0].Token}, scan)
	if err != nil {
		return err
	}
	batchCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	v, err := sourcewire.DecodeProtectionObservation(bodies[0])
	if err != nil {
		return errTeamsConsentStale
	}
	if err = c.sendTeamsLocalOrigin(batchCtx, cfg, client, d, credential, snapshot, scope, v.ObservationID, false); err != nil {
		return err
	}
	return errTeamsPilotMore
}

// Reuse a retained, fully verified non-held scan only to finish the same
// already-accepted local batch. It never authorizes a new observation.
func teamsCompletedSafetyScanIn(ctx context.Context, tx *sql.Tx, d LibraryDescriptor, credential *teams.EnrollmentCredential, scope teamsOriginBatchScope) (*teamsRecoveryOriginScan, error) {
	var key string
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT run_key,start_receipt_json FROM teams_origin_runs WHERE origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND work_id='' AND snapshot_id='' AND inventory_sha256=? AND settings_revision=? AND source_revision=? AND privacy_revision=? AND cache_generation=? AND token='batch-safety' AND state='completed' ORDER BY rowid DESC LIMIT 1`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, scope.inventoryHash, scope.settingsRevision, scope.sourceRevision, scope.privacyRevision, scope.generation).Scan(&key, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var start sourcewire.RecoveryStartReceipt
	if !teams.ValidUniqueJSON(raw) || json.Unmarshal(raw, &start) != nil {
		return nil, errTeamsProtectionHeld
	}
	rows, err := tx.QueryContext(ctx, `SELECT page_index,request_json,receipt_json FROM teams_origin_run_pages WHERE run_key=? ORDER BY page_index`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var requests []sourcewire.RecoveryPageRequest
	var receipts []sourcewire.RecoveryPageReceipt
	for rows.Next() {
		var i int
		var request, receipt []byte
		if err = rows.Scan(&i, &request, &receipt); err != nil {
			return nil, err
		}
		req, e := sourcewire.DecodeRecoveryPage(request)
		var res sourcewire.RecoveryPageReceipt
		if i != len(requests) || e != nil || !teams.ValidUniqueJSON(receipt) || json.Unmarshal(receipt, &res) != nil {
			return nil, errTeamsProtectionHeld
		}
		requests = append(requests, req)
		receipts = append(receipts, res)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return teamsBuildRecoveryOriginScan(start, requests, receipts)
}

// Completion is a local journal transition. Revalidate every accepted local
// observation under the same consent and capture gates before retiring a run.
func (c *Catalog) completeTeamsLocalOriginRun(ctx context.Context, cfg Config, d LibraryDescriptor, credential *teams.EnrollmentCredential, snapshot teamsV2Inventory, scope teamsOriginBatchScope, safetyRechecked bool) error {
	lease, e := c.teamsSharingLease()
	if e != nil {
		return e
	}
	defer lease.Close()
	ctx, release, e := teamsCensusCaptureLease(ctx, cfg)
	if e != nil {
		return e
	}
	defer release()
	if e = c.teamsPilotCheckpointCurrent(ctx, d); e != nil {
		return e
	}
	return c.writeTransaction(ctx, "teams-local-origin-complete", func(tx *sql.Tx) error {
		current, fresh, e := teamsLocalSourceCensusIn(ctx, tx, cfg, d, credential)
		if e != nil {
			return e
		}
		if fresh != scope || !teamsSamePilotSnapshot(current, snapshot) {
			return errTeamsSnapshotChanged
		}
		for _, item := range snapshot.Items {
			if item.Kind != "codex_thread" {
				continue
			}
			var id string
			e = tx.QueryRowContext(ctx, `SELECT observation_id FROM teams_origin_outbox WHERE origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND work_id='' AND snapshot_id='' AND inventory_sha256=? AND settings_revision=? AND source_revision=? AND privacy_revision=? AND cache_generation=? AND token=? AND state='accepted' ORDER BY rowid DESC LIMIT 1`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, scope.inventoryHash, scope.settingsRevision, scope.sourceRevision, scope.privacyRevision, scope.generation, item.Token).Scan(&id)
			if e != nil {
				return e
			}
			state, e := teamsLocalOriginStateCheckedIn(ctx, tx, cfg, d, credential, snapshot, scope, id, false, true, &item, false)
			if e != nil {
				return e
			}
			if state.safetyPending && !safetyRechecked {
				return errTeamsPilotPending
			}
		}
		_, e = tx.ExecContext(ctx, `UPDATE teams_origin_runs SET state='completed' WHERE state='ready' AND origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND work_id='' AND snapshot_id='' AND inventory_sha256=? AND settings_revision=? AND source_revision=? AND privacy_revision=? AND cache_generation=? AND token IN ('batch','batch-safety')`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, scope.inventoryHash, scope.settingsRevision, scope.sourceRevision, scope.privacyRevision, scope.generation)
		return e
	})
}
