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

type teamsProfileTransport interface {
	StageSourceProfilePage(context.Context, string, []byte) (teams.ProfilePageReceipt, error)
	SealSourceProfile(context.Context, string, []byte) (teams.ProfileSealReceipt, error)
}

type teamsProfileSendState struct {
	pages    [][]byte
	receipts [][]byte
	seal     []byte
	state    string
}

// teamsProfileSendStateIn rechecks the scope and the complete immutable chain
// under the same short transaction used to record each response. A held upload
// is retained as evidence and requires a newly issued replacement, not replay.
func teamsProfileSendStateIn(ctx context.Context, tx *sql.Tx, d LibraryDescriptor, credential *teams.EnrollmentCredential, work teams.Work, uploadID string) (teamsProfileSendState, error) {
	var out teamsProfileSendState
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
	var origin, org, person, installation, library, instance, workID, snapshot, inventory, sealHash string
	var settingsRev, sourceRev, privacyRev, expires int64
	var sealReceipt []byte
	err = tx.QueryRowContext(ctx, `SELECT origin,org_id,person_id,installation_id,library_id,instance_id,work_id,snapshot_id,inventory_sha256,settings_revision,source_revision,privacy_revision,expires_at,seal_json,seal_sha256,state,seal_receipt_json FROM teams_profile_outbox WHERE upload_id=?`, uploadID).Scan(&origin, &org, &person, &installation, &library, &instance, &workID, &snapshot, &inventory, &settingsRev, &sourceRev, &privacyRev, &expires, &out.seal, &sealHash, &out.state, &sealReceipt)
	if err != nil || origin != credential.Origin || org != credential.OrgID || person != credential.PersonID || installation != credential.InstallationID || library != d.ID || instance != d.Instance || workID != work.ID || settingsRev != settings.SettingsRevision || expires <= time.Now().UTC().Unix() || sealHash != sourcewire.ProfileSealDigest(out.seal) {
		return teamsProfileSendState{}, errTeamsConsentStale
	}
	if out.state == "held" {
		return teamsProfileSendState{}, errTeamsRecoveryFreshProfile
	}
	if out.state != "prepared" && out.state != "accepted" {
		return teamsProfileSendState{}, errTeamsConsentStale
	}
	var currentSource, currentPrivacy int64
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&currentSource); err != nil {
		return teamsProfileSendState{}, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&currentPrivacy); err != nil {
		return teamsProfileSendState{}, err
	}
	anchor, err := readTeamsV2Upload(ctx, tx, work.ID)
	if err != nil {
		return teamsProfileSendState{}, errTeamsSnapshotChanged
	}
	cache, err := readCacheGenerations(ctx, tx)
	if err != nil {
		return teamsProfileSendState{}, err
	}
	if currentSource != sourceRev || currentPrivacy != privacyRev || (anchor.State != "sealed" && anchor.State != "bound") || len(anchor.SealedJSON) == 0 || anchor.LibraryID != d.ID || anchor.InstanceID != d.Instance || anchor.OrgID != credential.OrgID || anchor.InstallationID != credential.InstallationID || anchor.SnapshotID != snapshot || anchor.InventoryHash != inventory || anchor.ConfigHash != work.ConfigSHA256 || anchor.VersionID != work.VersionID || anchor.EvaluationRevision != work.EvaluationRevision || anchor.SettingsRevision != settingsRev || anchor.SourceRevision != sourceRev || anchor.PrivacyRevision != privacyRev || anchor.Generation != cache.all {
		return teamsProfileSendState{}, errTeamsSnapshotChanged
	}
	rows, err := tx.QueryContext(ctx, `SELECT page_index,page_json,page_sha256,receipt_json FROM teams_profile_outbox_pages WHERE upload_id=? ORDER BY page_index`, uploadID)
	if err != nil {
		return teamsProfileSendState{}, err
	}
	for rows.Next() {
		var index int
		var raw, receipt []byte
		var hash string
		if err = rows.Scan(&index, &raw, &hash, &receipt); err != nil || index != len(out.pages) || hash != sourcewire.ProfilePageDigest(raw) {
			rows.Close()
			return teamsProfileSendState{}, errTeamsConsentStale
		}
		out.pages = append(out.pages, raw)
		out.receipts = append(out.receipts, receipt)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return teamsProfileSendState{}, err
	}
	rows.Close()
	seal, err := teamsValidateOriginalProfileChain(installation, library, out.pages, out.seal)
	if err != nil || seal.UploadID != uploadID || seal.SnapshotID != snapshot || seal.InventorySHA256 != inventory || seal.LocalRevision != anchor.LocalRevision {
		return teamsProfileSendState{}, errTeamsConsentStale
	}
	for i, body := range out.receipts {
		if len(body) == 0 {
			if out.state == "accepted" {
				return teamsProfileSendState{}, errTeamsConsentStale
			}
			continue
		}
		page, e := sourcewire.DecodeProfilePage(out.pages[i])
		var receipt sourcewire.ProfilePageReceipt
		if e != nil || json.Unmarshal(body, &receipt) != nil || receipt.Status != "accepted" || receipt.SnapshotID != page.SnapshotID || receipt.ProfileID != page.ProfileID || receipt.ProfileVersion != page.ProfileVersion || receipt.ProfileRevision != page.ProfileRevision || receipt.Index != page.Index || receipt.PageSHA256 != sourcewire.ProfilePageDigest(out.pages[i]) {
			return teamsProfileSendState{}, errTeamsConsentStale
		}
	}
	if out.state == "accepted" {
		var receipt sourcewire.ProfileSealReceipt
		if json.Unmarshal(sealReceipt, &receipt) != nil || receipt.Status != "accepted" || receipt.SnapshotID != seal.SnapshotID || receipt.ProfileID != seal.ProfileID || receipt.ProfileVersion != seal.ProfileVersion || receipt.ProfileRevision != seal.ProfileRevision || receipt.SealSHA256 != sourcewire.ProfileSealDigest(out.seal) || receipt.Complete != seal.Complete || receipt.ItemCount != seal.ItemCount {
			return teamsProfileSendState{}, errTeamsConsentStale
		}
	} else if len(sealReceipt) != 0 {
		return teamsProfileSendState{}, errTeamsConsentStale
	}
	return out, nil
}

// publishTeamsProfile is not wired into production until protection continuity
// and the full source writer inventory can establish the effect's eligibility.
func (c *Catalog) publishTeamsProfile(ctx context.Context, client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential, work teams.Work, uploadID string) error {
	if err := teamsRequireTypedUsageEligibility(); err != nil {
		return err
	}
	return c.publishTeamsProfileUnderGate(ctx, client, d, credential, work, uploadID)
}

// publishTeamsProfileUnderGate sends only journaled bytes. The sharing lease
// orders settings/privacy changes, and each accepted or held result is retained
// before a subsequent operation. Held results never authorize a profile.
func (c *Catalog) publishTeamsProfileUnderGate(ctx context.Context, client teamsProfileTransport, d LibraryDescriptor, credential *teams.EnrollmentCredential, work teams.Work, uploadID string) error {
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
	read := func() (teamsProfileSendState, error) {
		tx, e := c.reader().BeginTx(ctx, nil)
		if e != nil {
			return teamsProfileSendState{}, e
		}
		defer tx.Rollback()
		return teamsProfileSendStateIn(ctx, tx, d, credential, work, uploadID)
	}
	transportError := func(sendErr error) error {
		var response *teams.HTTPError
		if !errors.As(sendErr, &response) || (response.Code != "source_resync_required" && response.Code != "source_issuance_expired") {
			return sendErr
		}
		if err := c.writeTransaction(ctx, "teams-profile-resync-held", func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, `UPDATE teams_profile_outbox SET state='held' WHERE upload_id=? AND origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND work_id=? AND state='prepared'`, uploadID, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, work.ID)
			return e
		}); err != nil {
			return err
		}
		return errTeamsRecoveryFreshProfile
	}
	current, err := read()
	if err != nil {
		return err
	}
	if current.state == "accepted" {
		return nil
	}
	for i, raw := range current.pages {
		if len(current.receipts[i]) != 0 {
			continue
		}
		if _, err = read(); err != nil {
			return err
		}
		receipt, e := client.StageSourceProfilePage(ctx, work.ID, raw)
		if e != nil {
			return transportError(e)
		}
		page, e := sourcewire.DecodeProfilePage(raw)
		if e != nil || receipt.SnapshotID != page.SnapshotID || receipt.ProfileID != page.ProfileID || receipt.ProfileVersion != page.ProfileVersion || receipt.ProfileRevision != page.ProfileRevision || receipt.Index != page.Index || receipt.PageSHA256 != sourcewire.ProfilePageDigest(raw) || (receipt.Status != "accepted" && receipt.Status != "held") {
			return errTeamsConsentStale
		}
		body, e := json.Marshal(receipt)
		if e != nil {
			return e
		}
		e = c.writeTransaction(ctx, "teams-profile-page-receipt", func(tx *sql.Tx) error {
			state, e := teamsProfileSendStateIn(ctx, tx, d, credential, work, uploadID)
			if e != nil {
				return e
			}
			if state.state != "prepared" || i >= len(state.pages) || !bytes.Equal(state.pages[i], raw) || len(state.receipts[i]) != 0 {
				return errTeamsConsentStale
			}
			if _, e = tx.ExecContext(ctx, `UPDATE teams_profile_outbox_pages SET receipt_json=? WHERE upload_id=? AND page_index=?`, body, uploadID, i); e != nil {
				return e
			}
			if receipt.Status == "held" {
				_, e = tx.ExecContext(ctx, `UPDATE teams_profile_outbox SET state='held' WHERE upload_id=? AND state='prepared'`, uploadID)
			}
			return e
		})
		if e != nil {
			return e
		}
		if receipt.Status == "held" {
			return errTeamsRecoveryFreshProfile
		}
	}
	current, err = read()
	if err != nil {
		return err
	}
	for _, receipt := range current.receipts {
		if len(receipt) == 0 {
			return errTeamsConsentStale
		}
	}
	receipt, err := client.SealSourceProfile(ctx, work.ID, current.seal)
	if err != nil {
		return transportError(err)
	}
	seal, err := sourcewire.DecodeProfileSeal(current.seal)
	if err != nil || receipt.SnapshotID != seal.SnapshotID || receipt.ProfileID != seal.ProfileID || receipt.ProfileVersion != seal.ProfileVersion || receipt.ProfileRevision != seal.ProfileRevision || (receipt.Status == "accepted" && (receipt.SealSHA256 != sourcewire.ProfileSealDigest(current.seal) || receipt.Complete != seal.Complete || receipt.ItemCount != seal.ItemCount)) || (receipt.Status == "held" && (receipt.SealSHA256 != "" || receipt.Complete || receipt.ItemCount != 0)) || (receipt.Status != "accepted" && receipt.Status != "held") {
		return errTeamsConsentStale
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	err = c.writeTransaction(ctx, "teams-profile-seal-receipt", func(tx *sql.Tx) error {
		state, e := teamsProfileSendStateIn(ctx, tx, d, credential, work, uploadID)
		if e != nil {
			return e
		}
		if state.state != "prepared" || !bytes.Equal(state.seal, current.seal) {
			return errTeamsConsentStale
		}
		for _, r := range state.receipts {
			if len(r) == 0 {
				return errTeamsConsentStale
			}
		}
		_, e = tx.ExecContext(ctx, `UPDATE teams_profile_outbox SET state=?,seal_receipt_json=? WHERE upload_id=? AND state='prepared'`, receipt.Status, body, uploadID)
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
