package archive

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

type teamsLocalOriginState struct {
	body          []byte
	state         string
	safetyPending bool
	attempted     bool
}

func teamsLocalOriginStateIn(ctx context.Context, tx *sql.Tx, cfg Config, d LibraryDescriptor, credential *teams.EnrollmentCredential, snapshot teamsV2Inventory, scope teamsOriginBatchScope, id string, replay bool) (teamsLocalOriginState, error) {
	return teamsLocalOriginStateCheckedIn(ctx, tx, cfg, d, credential, snapshot, scope, id, replay, false, nil, false)
}

// A caller that already checked the census in this transaction can provide its
// unique indexed item, avoiding a complete source proof for every receipt.
func teamsLocalOriginStateCheckedIn(ctx context.Context, tx *sql.Tx, cfg Config, d LibraryDescriptor, credential *teams.EnrollmentCredential, snapshot teamsV2Inventory, scope teamsOriginBatchScope, id string, replay, checked bool, candidate *sourcewire.Item, lookupOnly bool) (teamsLocalOriginState, error) {
	var out teamsLocalOriginState
	var err error
	if replay {
		local, e := readTeamsProtectionAcknowledgement(ctx, tx, d, credential)
		if e != nil || !local {
			return out, errTeamsConsentStale
		}
		transfer, e := readTeamsSourceTransferAcknowledgement(ctx, tx, d, credential)
		if e != nil || !transfer {
			return out, errTeamsConsentStale
		}
		key := credential.SigningKey()
		if len(key) != ed25519.PrivateKeySize {
			clear(key)
			return out, errTeamsConsentStale
		}
		fingerprint := teamsPayloadHash(key.Public().(ed25519.PublicKey))
		clear(key)
		settings, e := readTeamsSettings(ctx, tx)
		if e != nil || !settings.Enabled || settings.LibraryID != d.ID || settings.InstanceID != d.Instance || settings.OrgID != credential.OrgID || settings.InstallationID != credential.InstallationID || settings.KeyFingerprint != fingerprint {
			return out, errTeamsConsentStale
		}
	} else if !checked {
		var current teamsV2Inventory
		var fresh teamsOriginBatchScope
		var e error
		if scope.tightening {
			current, fresh, e = teamsRetainedBaselineDraftAllowAttempt(ctx, tx, cfg, d, credential, id)
			fresh.tightening = true
		} else {
			current, fresh, e = teamsLocalSourceCensusIn(ctx, tx, cfg, d, credential)
		}
		if e != nil {
			return out, e
		}
		if fresh != scope || !teamsSamePilotSnapshot(current, snapshot) {
			return out, errTeamsSnapshotChanged
		}
	}
	var hash, token string
	var expected, expires int64
	var receipt []byte
	err = tx.QueryRowContext(ctx, `SELECT o.observation_json,o.observation_sha256,o.state,o.receipt_json,o.token,o.expected_revision,o.expires_at,EXISTS(SELECT 1 FROM teams_origin_attempts a WHERE a.observation_id=o.observation_id AND a.first_attempt_at>0) FROM teams_origin_outbox o WHERE o.observation_id=? AND o.origin=? AND o.org_id=? AND o.person_id=? AND o.installation_id=? AND o.library_id=? AND o.instance_id=? AND o.work_id='' AND o.snapshot_id='' AND (? OR (o.inventory_sha256=? AND o.settings_revision=? AND o.source_revision=? AND o.privacy_revision=? AND o.cache_generation=?))`, id, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, replay, scope.inventoryHash, scope.settingsRevision, scope.sourceRevision, scope.privacyRevision, scope.generation).Scan(&out.body, &hash, &out.state, &receipt, &token, &expected, &expires, &out.attempted)
	if err != nil || hash != sourcewire.ProtectionDigest(out.body) {
		return teamsLocalOriginState{}, errTeamsConsentStale
	}
	if out.state == "held" || (out.state == "prepared" && !lookupOnly && expires <= time.Now().UTC().Unix()) {
		return teamsLocalOriginState{}, errTeamsPilotPending
	}
	if out.state != "prepared" && out.state != "accepted" {
		return teamsLocalOriginState{}, errTeamsConsentStale
	}
	v, err := sourcewire.DecodeProtectionObservation(out.body)
	if err != nil {
		return teamsLocalOriginState{}, errTeamsConsentStale
	}
	if scope.tightening && v.Protection != "explicit_private" && v.Protection != "unknown" {
		return teamsLocalOriginState{}, errTeamsProtectionHeld
	}
	if replay && !lookupOnly && v.Protection == "non_private" {
		current, _, e := teamsLocalSourceCensusIn(ctx, tx, cfg, d, credential)
		if e != nil {
			return teamsLocalOriginState{}, errTeamsProtectionHeld
		}
		matched := false
		for _, item := range current.Items {
			if item.Token == v.Token && item.Witness == v.Witness && item.EvidenceRevision == v.EvidenceRevision && item.Protection == "public" && len(item.Dependencies) == len(v.Dependencies) {
				same := true
				for i := range item.Dependencies {
					if item.Dependencies[i] != v.Dependencies[i] {
						same = false
					}
				}
				matched = matched || same
			}
		}
		if !matched {
			return teamsLocalOriginState{}, errTeamsProtectionHeld
		}
	}
	want, err := sourcewire.ProtectionObservationID(v, credential.InstallationID, d.ID)
	if err != nil || want != id || v.ObservationID != id || v.Token != token || v.ExpectedRevision != expected || (v.Protection != "explicit_private" && v.Protection != "unknown" && v.Protection != "non_private") {
		return teamsLocalOriginState{}, errTeamsConsentStale
	}
	if _, err = sourcewire.CheckRecoveryIssuance(v.IssuedAt, time.Now().UTC()); err != nil && out.state == "prepared" && !lookupOnly {
		return teamsLocalOriginState{}, errTeamsPilotPending
	}
	found := replay && out.attempted
	items := snapshot.Items
	if candidate != nil {
		items = []sourcewire.Item{*candidate}
	}
	for _, item := range items {
		if replay {
			break
		}
		if item.Token != v.Token {
			continue
		}
		if found || item.Kind != "codex_thread" || item.Witness != v.Witness || item.EvidenceRevision != v.EvidenceRevision || len(item.Dependencies) != len(v.Dependencies) || (v.Protection == "explicit_private" && item.Protection != "private") || (v.Protection == "unknown" && item.Protection != "unknown" && item.Protection != "public") || (v.Protection == "non_private" && item.Protection != "public") {
			return teamsLocalOriginState{}, errTeamsConsentStale
		}
		for i := range item.Dependencies {
			if item.Dependencies[i] != v.Dependencies[i] {
				return teamsLocalOriginState{}, errTeamsConsentStale
			}
		}
		found = true
	}
	if !found {
		return teamsLocalOriginState{}, errTeamsConsentStale
	}
	if out.state == "accepted" {
		var r sourcewire.ProtectionReceipt
		if json.Unmarshal(receipt, &r) != nil || !teamsOriginReceiptMatches(v, out.body, r) || r.Status != "accepted" {
			return teamsLocalOriginState{}, errTeamsConsentStale
		}
		out.safetyPending = r.SafetyPending
	} else if len(receipt) != 0 {
		return teamsLocalOriginState{}, errTeamsConsentStale
	}
	return out, nil
}

// sendTeamsLocalOrigin holds the same lease as withdrawal, revalidates the
// host checkpoint and census immediately before each signed 031 effect, and
// retains accepted or held receipts before another effect can begin.
func (c *Catalog) sendTeamsLocalOrigin(ctx context.Context, cfg Config, client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential, snapshot teamsV2Inventory, scope teamsOriginBatchScope, id string, replay bool) error {
	if !client.MatchesCredential(credential) {
		return errTeamsConsentStale
	}
	lease, err := c.teamsSharingLease()
	if err != nil {
		return err
	}
	defer lease.Close()
	ctx, release, err := teamsCensusCaptureLease(ctx, cfg)
	if err != nil {
		return err
	}
	defer release()
	if err = c.teamsPilotCheckpointCurrent(ctx, d); err != nil {
		return err
	}
	tx, err := c.reader().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	state, err := teamsLocalOriginStateIn(ctx, tx, cfg, d, credential, snapshot, scope, id, replay)
	_ = tx.Rollback()
	if err != nil {
		return err
	}
	if state.state == "accepted" {
		if state.safetyPending {
			return errTeamsPilotPending
		}
		return nil
	}
	var enteredSend bool
	if !state.attempted {
		attemptAt := time.Now().UTC().Unix()
		if attemptAt <= 0 {
			return errTeamsPilotPending
		}
		// A failed post-marker check proves no PUT was initiated by this
		// process. Revert only the exact marker/body; a crash or failed
		// revert remains ambiguous and must never mint a replacement ID.
		defer func() {
			if enteredSend {
				return
			}
			revertCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = c.writeTransaction(revertCtx, "teams-local-origin-unsent", func(tx *sql.Tx) error {
				_, e := tx.ExecContext(revertCtx, `UPDATE teams_origin_attempts SET first_attempt_at=0 WHERE observation_id=? AND first_attempt_at=? AND terminal_code='' AND EXISTS(SELECT 1 FROM teams_origin_outbox o WHERE o.observation_id=? AND o.origin=? AND o.org_id=? AND o.person_id=? AND o.installation_id=? AND o.library_id=? AND o.instance_id=? AND o.state='prepared' AND o.receipt_json IS NULL AND o.observation_json=?)`, id, attemptAt, id, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, state.body)
				return e
			})
		}()
		err = c.writeTransaction(ctx, "teams-local-origin-attempt", func(tx *sql.Tx) error {
			current, e := teamsLocalOriginStateIn(ctx, tx, cfg, d, credential, snapshot, scope, id, replay)
			if e != nil {
				return e
			}
			if current.state != "prepared" || !bytes.Equal(current.body, state.body) {
				return errTeamsConsentStale
			}
			result, e := tx.ExecContext(ctx, `INSERT INTO teams_origin_attempts(observation_id,first_attempt_at) VALUES(?,?) ON CONFLICT(observation_id) DO UPDATE SET first_attempt_at=excluded.first_attempt_at WHERE first_attempt_at=0 AND terminal_code='' `, id, attemptAt)
			if e != nil {
				return e
			}
			n, e := result.RowsAffected()
			if e != nil {
				return e
			}
			if n != 1 {
				return errTeamsConsentStale
			}
			return nil
		})
		if err != nil {
			return err
		}
		// The attempt marker itself may commit while host finalization is still
		// pending. No signed effect may leave until that is reconciled.
		if err = c.teamsPilotCheckpointCurrent(ctx, d); err != nil {
			return err
		}
	}
	// The source may change outside Pharos even while its capture lock is
	// held. Recheck the live roster after the attempt commit and before PUT.
	tx, err = c.reader().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	current, checkErr := teamsLocalOriginStateIn(ctx, tx, cfg, d, credential, snapshot, scope, id, replay)
	_ = tx.Rollback()
	if checkErr != nil {
		return checkErr
	}
	if current.state != "prepared" || !bytes.Equal(current.body, state.body) {
		return errTeamsConsentStale
	}
	if err = c.teamsPilotCheckpointCurrent(ctx, d); err != nil {
		return err
	}
	enteredSend = true
	receipt, err := client.ObserveSourceProtection(ctx, d.ID, state.body)
	if err != nil {
		var response *teams.HTTPError
		if !errors.As(err, &response) || response.Status != 409 || (response.Code != "source_issuance_expired" && response.Code != "source_resync_required" && response.Code != "stale_protection_revision") {
			return err
		}
		// A prior held response may have changed remote state without leaving a
		// receipt. A later rejection does not prove the first effect absent.
		disposition := "unknown_" + response.Code
		if !state.attempted {
			disposition = "first_rejected_" + response.Code
		}
		e := c.writeTransaction(ctx, "teams-local-origin-unknown", func(tx *sql.Tx) error {
			result, e := tx.ExecContext(ctx, `UPDATE teams_origin_attempts SET terminal_code=? WHERE observation_id=? AND terminal_code='' AND EXISTS(SELECT 1 FROM teams_origin_outbox o WHERE o.observation_id=? AND o.observation_json=? AND o.state='prepared' AND o.receipt_json IS NULL)`, disposition, id, id, state.body)
			if e != nil {
				return e
			}
			n, e := result.RowsAffected()
			if e != nil {
				return e
			}
			if n != 1 {
				return errTeamsConsentStale
			}
			if !state.attempted {
				_, e = tx.ExecContext(ctx, `UPDATE teams_origin_runs SET state='completed' WHERE state='ready' AND origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND json_extract(start_receipt_json,'$.run_id')=(SELECT run_id FROM teams_origin_outbox WHERE observation_id=?)`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, id)
			}
			return e
		})
		if e != nil {
			return e
		}
		return errTeamsPilotPending
	}
	observation, err := sourcewire.DecodeProtectionObservation(state.body)
	if err != nil || !teamsOriginReceiptMatches(observation, state.body, receipt) {
		return errTeamsConsentStale
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	err = c.writeTransaction(ctx, "teams-local-origin-receipt", func(tx *sql.Tx) error {
		result, e := tx.ExecContext(ctx, `UPDATE teams_origin_outbox SET state=?,receipt_json=? WHERE observation_id=? AND origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND state='prepared' AND receipt_json IS NULL AND observation_json=? AND EXISTS(SELECT 1 FROM teams_origin_attempts a WHERE a.observation_id=teams_origin_outbox.observation_id AND a.terminal_code='')`, receipt.Status, body, id, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, state.body)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return errTeamsConsentStale
		}
		// A baseline recovery scan used for a restrictive observation is stale
		// once accepted. Complete it even after exact replay so the next scan
		// obtains the advanced authenticated generation.
		if receipt.Status == "accepted" {
			_, e = tx.ExecContext(ctx, `UPDATE teams_origin_runs SET state='completed' WHERE state='ready' AND token='baseline' AND origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND json_extract(start_receipt_json,'$.run_id')=(SELECT run_id FROM teams_origin_outbox WHERE observation_id=?)`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, id)
			if e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if receipt.Status == "held" || receipt.SafetyPending {
		return errTeamsPilotPending
	}
	return nil
}
