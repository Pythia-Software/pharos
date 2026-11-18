package archive

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

// teamsRetainedBaselineDraft reads an independently captured complete roster
// and every current local protection under the caller's capture lease. It does
// not release a hold or grant any transfer authority.
func teamsRetainedBaselineDraft(ctx context.Context, tx *sql.Tx, cfg Config, d LibraryDescriptor, credential *teams.EnrollmentCredential) (teamsV2Inventory, teamsOriginBatchScope, error) {
	var empty teamsV2Inventory
	var scope teamsOriginBatchScope
	if !d.ManagedBirth || credential == nil || credential.LibraryID != d.ID {
		return empty, scope, errTeamsProtectionHeld
	}
	settings, err := readTeamsSettings(ctx, tx)
	if err != nil {
		return empty, scope, err
	}
	local, err := readTeamsProtectionAcknowledgement(ctx, tx, d, credential)
	if err != nil {
		return empty, scope, err
	}
	transfer, err := readTeamsSourceTransferAcknowledgement(ctx, tx, d, credential)
	if err != nil {
		return empty, scope, err
	}
	key := credential.SigningKey()
	if len(key) != ed25519.PrivateKeySize {
		clear(key)
		return empty, scope, errTeamsNotEnrolled
	}
	fingerprint := teamsPayloadHash(key.Public().(ed25519.PublicKey))
	clear(key)
	if !local || !transfer || !settings.Enabled || settings.LibraryID != d.ID || settings.InstanceID != d.Instance || settings.OrgID != credential.OrgID || settings.InstallationID != credential.InstallationID || settings.KeyFingerprint != fingerprint {
		return empty, scope, errTeamsConsentStale
	}
	var unsafe bool
	// Retained history outside the live roster cannot be inferred away. This
	// narrow first lane never clears recovery guards or any earlier 031 outcome.
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM teams_protection_recovery_guards) OR EXISTS(SELECT 1 FROM teams_origin_outbox) OR EXISTS(SELECT 1 FROM teams_protection_assertions a WHERE a.active=1 AND NOT EXISTS(SELECT 1 FROM conversations c WHERE a.provider<>'' AND a.account<>'' AND a.native_id<>'' AND c.provider=a.provider AND c.account=a.account AND c.native_id=a.native_id))`).Scan(&unsafe)
	if err != nil {
		return empty, scope, err
	}
	if unsafe {
		return empty, scope, errTeamsProtectionHeld
	}
	snapshot, err := teamsIndependentRosterProof(ctx, tx, cfg, d, credential)
	if err != nil {
		return empty, scope, err
	}
	if !snapshot.Complete || snapshot.Coverage != (sourcewire.Coverage{}) || snapshot.Items == nil {
		return empty, scope, errTeamsSourceUnavailable
	}
	h, err := sourcewire.NewInventoryHasher(snapshot.Complete, snapshot.Coverage)
	if err != nil {
		return empty, scope, err
	}
	threads := 0
	for _, item := range snapshot.Items {
		if err = h.Add(item); err != nil {
			return empty, scope, err
		}
		if item.Kind == "codex_thread" {
			threads++
		}
	}
	if threads < 1 || threads > 500 {
		return empty, scope, errTeamsSourceUnavailable
	}
	hash, _, err := h.Finish()
	if err != nil {
		return empty, scope, err
	}
	scope = teamsOriginBatchScope{inventoryHash: hash, settingsRevision: settings.SettingsRevision, sourceRevision: snapshot.SourceRevision, privacyRevision: snapshot.PrivacyRevision, generation: snapshot.Generation}
	return snapshot, scope, nil
}

// Only an authenticated empty origin scan with a valid, retained floor can
// authorize the first baseline. The managed birth and empty local outbox prove
// that no earlier origin attempt exists to be hidden below that floor.
func teamsBaselineServerClear(scan *teamsRecoveryOriginScan, now time.Time) bool {
	return scan != nil && !scan.held && len(scan.byToken) == 0 && len(scan.byWitness) == 0 && scan.start.UnacceptedFloor >= 0 && scan.last.UnacceptedFloor >= scan.start.UnacceptedFloor && scan.last.UnacceptedFloor <= scan.last.ServerTime && scan.last.NextCursor == "" && now.UTC().Unix() < scan.start.ExpiresAt
}

// teamsProduceRetainedBaseline bootstraps a new managed birth only after the
// authenticated bounded server scan proves there are no retained origins.
// Nonempty or uncertain server history remains held for explicit recovery.
func (c *Catalog) teamsProduceRetainedBaseline(ctx context.Context, cfg Config, client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential) error {
	if !d.ManagedBirth || c.teamsCheckpoint == nil || c.teamsCheckpoint.writer.validateBinding == nil || client == nil || !client.MatchesCredential(credential) {
		return errTeamsProtectionHeld
	}
	if err := c.teamsPilotCheckpointCurrent(ctx, d); err != nil {
		return err
	}
	lease, err := c.teamsSharingLease()
	if err != nil {
		return err
	}
	censusCtx, release, err := teamsCensusCaptureLease(ctx, cfg)
	if err != nil {
		lease.Close()
		return err
	}
	tx, err := c.reader().BeginTx(censusCtx, nil)
	if err != nil {
		release()
		lease.Close()
		return err
	}
	snapshot, scope, err := teamsRetainedBaselineDraft(censusCtx, tx, cfg, d, credential)
	_ = tx.Rollback()
	release()
	lease.Close()
	if err != nil {
		return err
	}
	check := func(ctx context.Context, tx *sql.Tx) error {
		current, fresh, e := teamsRetainedBaselineDraft(ctx, tx, cfg, d, credential)
		if e != nil {
			return e
		}
		if fresh != scope || !teamsSamePilotSnapshot(current, snapshot) {
			return errTeamsSnapshotChanged
		}
		return nil
	}
	scan, err := c.teamsRecoveryPageForScope(ctx, cfg, client, d, credential, "", snapshot, scope, "baseline", check)
	if err != nil {
		return err
	}
	if !teamsBaselineServerClear(scan, time.Now()) {
		return errTeamsProtectionHeld
	}
	lease, err = c.teamsSharingLease()
	if err != nil {
		return err
	}
	defer lease.Close()
	censusCtx, release, err = teamsCensusCaptureLease(ctx, cfg)
	if err != nil {
		return err
	}
	defer release()
	if err = c.teamsPilotCheckpointCurrent(censusCtx, d); err != nil {
		return err
	}
	return c.writeTransaction(censusCtx, "teams-retained-baseline", func(tx *sql.Tx) error {
		if e := check(censusCtx, tx); e != nil {
			return e
		}
		var run string
		e := tx.QueryRowContext(censusCtx, `SELECT run_key FROM teams_origin_runs WHERE origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND work_id='' AND snapshot_id='' AND inventory_sha256=? AND settings_revision=? AND source_revision=? AND privacy_revision=? AND cache_generation=? AND token='baseline' AND state='ready' AND json_extract(start_receipt_json,'$.run_id')=? AND expires_at>?`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, scope.inventoryHash, scope.settingsRevision, scope.sourceRevision, scope.privacyRevision, scope.generation, scan.start.RunID, time.Now().UTC().Unix()).Scan(&run)
		if errors.Is(e, sql.ErrNoRows) {
			return errTeamsProtectionHeld
		}
		if e != nil {
			return e
		}
		var generation int64
		if e = tx.QueryRowContext(censusCtx, `SELECT generation FROM teams_protection_baseline WHERE id=1`).Scan(&generation); e != nil {
			return e
		}
		if generation >= math.MaxInt64-1 {
			return errTeamsProtectionHeld
		}
		var mapping int64
		if e = tx.QueryRowContext(censusCtx, `SELECT COALESCE(MAX(revision),0) FROM teams_source_mapping_revisions`).Scan(&mapping); e != nil {
			return e
		}
		_, e = tx.ExecContext(censusCtx, `UPDATE teams_protection_baseline SET state='current',generation=generation+1,library_id=?,instance_id=?,policy_sha256=?,inventory_sha256=?,proposal_sha256=?,source_revision=?,privacy_revision=?,mapping_revision=?,acknowledged_policy=1 WHERE id=1`, d.ID, d.Instance, teamsLocalProtectionPolicyDigest(), scope.inventoryHash, run, scope.sourceRevision, scope.privacyRevision, mapping)
		return e
	})
}
