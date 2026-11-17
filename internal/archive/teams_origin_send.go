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

type teamsOriginTransport interface {
	ObserveSourceProtection(context.Context, string, []byte) (sourcewire.ProtectionReceipt, error)
}

type teamsOriginSendState struct {
	body  []byte
	state string
}

func teamsOriginSendStateIn(ctx context.Context, tx *sql.Tx, d LibraryDescriptor, credential *teams.EnrollmentCredential, work teams.Work, id string) (teamsOriginSendState, error) {
	var out teamsOriginSendState
	if credential == nil || credential.LibraryID != d.ID || work.LibraryID != d.ID || work.VerifyConfig() != nil || !work.ExpiresAt.After(time.Now().UTC()) {
		return out, errTeamsConsentStale
	}
	key := credential.SigningKey()
	if len(key) != ed25519.PrivateKeySize {
		clear(key)
		return out, errTeamsNotEnrolled
	}
	fingerprint := teamsPayloadHash(key.Public().(ed25519.PublicKey))
	clear(key)
	settings, err := readTeamsSettings(ctx, tx)
	if err != nil {
		return out, err
	}
	ack, err := readTeamsProtectionAcknowledgement(ctx, tx, d, credential)
	if err != nil {
		return out, err
	}
	if !ack || !settings.Enabled || settings.MetricsOptOut || settings.LibraryID != d.ID || settings.InstanceID != d.Instance || settings.OrgID != credential.OrgID || settings.InstallationID != credential.InstallationID || settings.KeyFingerprint != fingerprint {
		return out, errTeamsConsentStale
	}
	var snapshot, inventory, hash, token string
	var settingsRev, sourceRev, privacyRev, generation, expires, expected int64
	var receipt []byte
	err = tx.QueryRowContext(ctx, `SELECT snapshot_id,inventory_sha256,settings_revision,source_revision,privacy_revision,cache_generation,expires_at,token,expected_revision,observation_json,observation_sha256,state,receipt_json FROM teams_origin_outbox WHERE observation_id=? AND origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND work_id=?`, id, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, work.ID).Scan(&snapshot, &inventory, &settingsRev, &sourceRev, &privacyRev, &generation, &expires, &token, &expected, &out.body, &hash, &out.state, &receipt)
	if err != nil || settingsRev != settings.SettingsRevision || expires <= time.Now().UTC().Unix() || hash != sourcewire.ProtectionDigest(out.body) {
		return teamsOriginSendState{}, errTeamsConsentStale
	}
	if out.state == "held" {
		return teamsOriginSendState{}, errTeamsRecoveryFreshProfile
	}
	if out.state != "prepared" && out.state != "accepted" {
		return teamsOriginSendState{}, errTeamsConsentStale
	}
	observation, err := sourcewire.DecodeProtectionObservation(out.body)
	if err != nil {
		return teamsOriginSendState{}, errTeamsConsentStale
	}
	expectedID, err := sourcewire.ProtectionObservationID(observation, credential.InstallationID, d.ID)
	if err != nil || expectedID != id || observation.ObservationID != id || observation.Token != token || observation.ExpectedRevision != expected || (observation.Protection != "explicit_private" && observation.Protection != "unknown") {
		return teamsOriginSendState{}, errTeamsConsentStale
	}
	if _, err = sourcewire.CheckRecoveryIssuance(observation.IssuedAt, time.Now().UTC()); err != nil {
		return teamsOriginSendState{}, errTeamsRecoveryFreshProfile
	}
	var currentSource, currentPrivacy int64
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&currentSource); err != nil {
		return teamsOriginSendState{}, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&currentPrivacy); err != nil {
		return teamsOriginSendState{}, err
	}
	currentGeneration, err := readCacheGenerations(ctx, tx)
	if err != nil {
		return teamsOriginSendState{}, err
	}
	anchor, err := readTeamsV2Upload(ctx, tx, work.ID)
	if err != nil || currentSource != sourceRev || currentPrivacy != privacyRev || currentGeneration.all != generation || (anchor.State != "sealed" && anchor.State != "bound") || len(anchor.SealedJSON) == 0 || anchor.LibraryID != d.ID || anchor.InstanceID != d.Instance || anchor.OrgID != credential.OrgID || anchor.InstallationID != credential.InstallationID || anchor.SnapshotID != snapshot || anchor.InventoryHash != inventory || anchor.ConfigHash != work.ConfigSHA256 || anchor.VersionID != work.VersionID || anchor.EvaluationRevision != work.EvaluationRevision || anchor.SettingsRevision != settingsRev || anchor.SourceRevision != sourceRev || anchor.PrivacyRevision != privacyRev || anchor.Generation != generation {
		return teamsOriginSendState{}, errTeamsSnapshotChanged
	}
	found := false
	for _, raw := range anchor.Pages {
		page, e := sourcewire.DecodePage(raw)
		if e != nil || page.SnapshotID != snapshot || page.InventorySHA256 != inventory {
			return teamsOriginSendState{}, errTeamsConsentStale
		}
		for _, item := range page.Items {
			if item.Token != token {
				continue
			}
			if found || item.Kind != "codex_thread" || item.Witness != observation.Witness || item.EvidenceRevision != observation.EvidenceRevision || len(item.Dependencies) != len(observation.Dependencies) || (observation.Protection == "explicit_private" && item.Protection != "private") || (observation.Protection == "unknown" && item.Protection != "unknown") {
				return teamsOriginSendState{}, errTeamsConsentStale
			}
			for i := range item.Dependencies {
				if item.Dependencies[i] != observation.Dependencies[i] {
					return teamsOriginSendState{}, errTeamsConsentStale
				}
			}
			found = true
		}
	}
	if !found {
		return teamsOriginSendState{}, errTeamsConsentStale
	}
	if out.state == "accepted" {
		var r sourcewire.ProtectionReceipt
		if json.Unmarshal(receipt, &r) != nil || !teamsOriginReceiptMatches(observation, out.body, r) || r.Status != "accepted" {
			return teamsOriginSendState{}, errTeamsConsentStale
		}
	} else if len(receipt) != 0 {
		return teamsOriginSendState{}, errTeamsConsentStale
	}
	return out, nil
}
func teamsOriginReceiptMatches(v sourcewire.ProtectionObservation, raw []byte, r sourcewire.ProtectionReceipt) bool {
	if r.ObservationID != v.ObservationID || r.ObservationSHA256 != sourcewire.ProtectionDigest(raw) {
		return false
	}
	switch r.Status {
	case "accepted":
		return r.Revision == v.Revision && r.PrivacyRevision > 0 && r.OriginProtection == v.Protection
	case "held":
		return r.Revision == 0 && r.PrivacyRevision == 0 && r.OriginProtection == "" && r.SafetyPending
	default:
		return false
	}
}

// publishTeamsOrigin remains closed until the complete producer and continuity
// writer inventory are integrated; no typed authority follows from a receipt.
func (c *Catalog) publishTeamsOrigin(ctx context.Context, client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential, work teams.Work, id string) error {
	if err := teamsRequireTypedUsageEligibility(); err != nil {
		return err
	}
	return c.publishTeamsOriginUnderGate(ctx, client, d, credential, work, id)
}
func (c *Catalog) publishTeamsOriginUnderGate(ctx context.Context, client teamsOriginTransport, d LibraryDescriptor, credential *teams.EnrollmentCredential, work teams.Work, id string) error {
	if client == nil {
		return errTeamsConsentStale
	}
	lease, err := c.teamsSharingLease()
	if err != nil {
		return err
	}
	defer lease.Close()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := c.reader().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	state, err := teamsOriginSendStateIn(ctx, tx, d, credential, work, id)
	_ = tx.Rollback()
	if err != nil {
		return err
	}
	if state.state == "accepted" {
		return nil
	}
	receipt, err := client.ObserveSourceProtection(ctx, d.ID, state.body)
	if err != nil {
		var response *teams.HTTPError
		if !errors.As(err, &response) || (response.Code != "source_resync_required" && response.Code != "source_issuance_expired") {
			return err
		}
		e := c.writeTransaction(ctx, "teams-origin-resync-held", func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, `UPDATE teams_origin_outbox SET state='held' WHERE observation_id=? AND origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND work_id=? AND state='prepared'`, id, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, work.ID)
			return e
		})
		if e != nil {
			return e
		}
		return errTeamsRecoveryFreshProfile
	}
	observation, err := sourcewire.DecodeProtectionObservation(state.body)
	if err != nil || !teamsOriginReceiptMatches(observation, state.body, receipt) {
		return errTeamsConsentStale
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	err = c.writeTransaction(ctx, "teams-origin-receipt", func(tx *sql.Tx) error {
		current, e := teamsOriginSendStateIn(ctx, tx, d, credential, work, id)
		if e != nil {
			return e
		}
		if current.state != "prepared" || !bytes.Equal(current.body, state.body) {
			return errTeamsConsentStale
		}
		_, e = tx.ExecContext(ctx, `UPDATE teams_origin_outbox SET state=?,receipt_json=? WHERE observation_id=? AND state='prepared'`, receipt.Status, body, id)
		return e
	})
	if err != nil {
		return err
	}
	if receipt.Status == "held" {
		return errTeamsRecoveryFreshProfile
	}
	return nil
}
