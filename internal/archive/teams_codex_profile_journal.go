package archive

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"time"

	"errors"
	"github.com/gbdubs/pharos/internal/teams"

	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

// Validate the exact original 032 chain before retaining or staging it. A
// structurally valid chain is not an accepted receipt or current authority.
func teamsValidateOriginalProfileChain(installation, library string, pages [][]byte, sealRaw []byte) (sourcewire.ProfileSeal, error) {
	seal, err := sourcewire.DecodeProfileSeal(sealRaw)
	if err != nil || seal.ProfileID != "codex_request_usage" || seal.ProfileVersion != 1 || len(pages) != seal.PageCount || !seal.Complete || seal.Coverage != (sourcewire.Coverage{}) {
		return sourcewire.ProfileSeal{}, errTeamsSourceUnavailable
	}
	upload, err := sourcewire.ProfileUploadID(installation, library, seal.SnapshotID, seal.InventorySHA256, seal.ProfileID, seal.ProfileVersion, seal.ProfileRevision, seal.LocalRevision, seal.IssuedAt, seal.ExpectedPriorSHA256)
	if err != nil || upload != seal.UploadID {
		return sourcewire.ProfileSeal{}, errTeamsSourceUnavailable
	}
	previous := ""
	lastToken := ""
	count := 0
	for i, raw := range pages {
		page, e := sourcewire.DecodeProfilePage(raw)
		if e != nil {
			return sourcewire.ProfileSeal{}, errTeamsSourceUnavailable
		}
		if page.Index != i || page.Contract != seal.Contract || page.IssuedAt != seal.IssuedAt || page.UploadID != seal.UploadID || page.SnapshotID != seal.SnapshotID || page.InventorySHA256 != seal.InventorySHA256 || page.ProfileID != seal.ProfileID || page.ProfileVersion != seal.ProfileVersion || page.ProfileRevision != seal.ProfileRevision || page.LocalRevision != seal.LocalRevision || page.ExpectedPriorSHA256 != seal.ExpectedPriorSHA256 {
			return sourcewire.ProfileSeal{}, errTeamsSourceUnavailable
		}
		if (i == 0 && page.PreviousPageSHA256 != nil) || (i > 0 && (page.PreviousPageSHA256 == nil || *page.PreviousPageSHA256 != previous)) {
			return sourcewire.ProfileSeal{}, errTeamsSourceUnavailable
		}
		for _, item := range page.Items {
			if item.Token <= lastToken {
				return sourcewire.ProfileSeal{}, errTeamsSourceUnavailable
			}
			lastToken = item.Token
			count++
		}
		canonical, hash, e := page.Bytes()
		if e != nil || !bytes.Equal(canonical, raw) {
			return sourcewire.ProfileSeal{}, errTeamsSourceUnavailable
		}
		previous = hash
	}
	if count != seal.ItemCount || previous != seal.FinalPageSHA256 {
		return sourcewire.ProfileSeal{}, errTeamsSourceUnavailable
	}
	canonical, _, err := seal.Bytes()
	if err != nil || !bytes.Equal(canonical, sealRaw) {
		return sourcewire.ProfileSeal{}, errors.New("invalid original profile seal")
	}
	return seal, nil
}

// journalTeamsProfile preserves the exact original page/seal bytes before
// transport, tied to the current accepted local snapshot and device scope. The
// prepared row alone neither certifies server acceptance nor authorizes proof.
func (c *Catalog) journalTeamsProfile(ctx context.Context, d LibraryDescriptor, credential *teams.EnrollmentCredential, work teams.Work, pages [][]byte, sealRaw []byte) error {
	if credential == nil || credential.LibraryID != d.ID || work.LibraryID != d.ID || work.VerifyConfig() != nil || work.ExpiresAt.IsZero() || !work.ExpiresAt.After(time.Now().UTC()) {
		return errTeamsConsentStale
	}
	seal, err := teamsValidateOriginalProfileChain(credential.InstallationID, d.ID, pages, sealRaw)
	if err != nil {
		return err
	}
	expiry, err := sourcewire.CheckRecoveryIssuance(seal.IssuedAt, time.Now().UTC())
	if err != nil {
		return errTeamsSourceUnavailable
	}
	total := len(sealRaw)
	for _, p := range pages {
		total += len(p)
		if total > 16<<20 {
			return errTeamsSourceUnavailable
		}
	}
	key := credential.SigningKey()
	if len(key) != ed25519.PrivateKeySize {
		clear(key)
		return errTeamsNotEnrolled
	}
	fingerprint := teamsPayloadHash(key.Public().(ed25519.PublicKey))
	clear(key)
	lease, err := c.teamsSharingLease()
	if err != nil {
		return err
	}
	defer lease.Close()
	return c.writeTransaction(ctx, "teams-profile-journal", func(tx *sql.Tx) error {
		settings, e := readTeamsSettings(ctx, tx)
		if e != nil {
			return e
		}
		if !settings.Enabled || settings.MetricsOptOut || settings.LibraryID != d.ID || settings.InstanceID != d.Instance || settings.OrgID != credential.OrgID || settings.InstallationID != credential.InstallationID || settings.KeyFingerprint != fingerprint {
			return errTeamsConsentStale
		}
		acknowledged, e := readTeamsProtectionAcknowledgement(ctx, tx, d, credential)
		if e != nil {
			return e
		}
		if !acknowledged {
			return errTeamsConsentStale
		}
		var sourceRevision, privacyRevision int64
		if e = tx.QueryRowContext(ctx, `SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&sourceRevision); e != nil {
			return e
		}
		if e = tx.QueryRowContext(ctx, `SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&privacyRevision); e != nil {
			return e
		}
		anchor, e := readTeamsV2Upload(ctx, tx, work.ID)
		if e != nil {
			return errTeamsConsentStale
		}
		cache, e := readCacheGenerations(ctx, tx)
		if e != nil {
			return e
		}
		if (anchor.State != "sealed" && anchor.State != "bound") || len(anchor.SealedJSON) == 0 || anchor.LibraryID != d.ID || anchor.InstanceID != d.Instance || anchor.OrgID != credential.OrgID || anchor.InstallationID != credential.InstallationID || anchor.SnapshotID != seal.SnapshotID || anchor.InventoryHash != seal.InventorySHA256 || anchor.ConfigHash != work.ConfigSHA256 || anchor.VersionID != work.VersionID || anchor.EvaluationRevision != work.EvaluationRevision || anchor.LocalRevision != seal.LocalRevision || anchor.SettingsRevision != settings.SettingsRevision || anchor.SourceRevision != sourceRevision || anchor.PrivacyRevision != privacyRevision || anchor.Generation != cache.all {
			return errTeamsSnapshotChanged
		}
		sealHash := sourcewire.ProfileSealDigest(sealRaw)
		result, e := tx.ExecContext(ctx, `INSERT INTO teams_profile_outbox(upload_id,origin,org_id,person_id,installation_id,library_id,instance_id,work_id,settings_revision,source_revision,privacy_revision,snapshot_id,inventory_sha256,profile_revision,issued_at,expires_at,seal_json,seal_sha256,state) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'prepared') ON CONFLICT(upload_id) DO NOTHING`, seal.UploadID, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, work.ID, settings.SettingsRevision, sourceRevision, privacyRevision, seal.SnapshotID, seal.InventorySHA256, seal.ProfileRevision, seal.IssuedAt, expiry.Unix(), sealRaw, sealHash)
		if e != nil {
			return e
		}
		inserted, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if inserted == 0 {
			var oldSeal []byte
			var oldHash, origin, org, person, installation, library, instance, workID string
			var settingsRev, sourceRev, privacyRev int64
			e = tx.QueryRowContext(ctx, `SELECT seal_json,seal_sha256,origin,org_id,person_id,installation_id,library_id,instance_id,work_id,settings_revision,source_revision,privacy_revision FROM teams_profile_outbox WHERE upload_id=?`, seal.UploadID).Scan(&oldSeal, &oldHash, &origin, &org, &person, &installation, &library, &instance, &workID, &settingsRev, &sourceRev, &privacyRev)
			if e != nil || !bytes.Equal(oldSeal, sealRaw) || oldHash != sealHash || origin != credential.Origin || org != credential.OrgID || person != credential.PersonID || installation != credential.InstallationID || library != d.ID || instance != d.Instance || workID != work.ID || settingsRev != settings.SettingsRevision || sourceRev != sourceRevision || privacyRev != privacyRevision {
				return errTeamsConsentStale
			}
		}
		for i, page := range pages {
			hash := sourcewire.ProfilePageDigest(page)
			res, e := tx.ExecContext(ctx, `INSERT INTO teams_profile_outbox_pages(upload_id,page_index,page_json,page_sha256) VALUES(?,?,?,?) ON CONFLICT(upload_id,page_index) DO NOTHING`, seal.UploadID, i, page, hash)
			if e != nil {
				return e
			}
			n, e := res.RowsAffected()
			if e != nil {
				return e
			}
			if n == 0 {
				var old []byte
				var oldHash string
				e = tx.QueryRowContext(ctx, `SELECT page_json,page_sha256 FROM teams_profile_outbox_pages WHERE upload_id=? AND page_index=?`, seal.UploadID, i).Scan(&old, &oldHash)
				if e != nil || !bytes.Equal(old, page) || oldHash != hash {
					return errTeamsConsentStale
				}
			}
		}
		var pageCount int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM teams_profile_outbox_pages WHERE upload_id=?`, seal.UploadID).Scan(&pageCount); e != nil {
			return e
		}
		if pageCount != len(pages) {
			return errTeamsConsentStale
		}
		return nil
	})
}
