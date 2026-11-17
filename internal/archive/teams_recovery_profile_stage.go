package archive

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"time"

	"fmt"
	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

// Build an immutable recovery stage chain from a retained original 032 chain.
// The caller must journal the returned bytes before signed transmission and
// independently establish the current run/anchor. This does not release holds.
func teamsBuildRecoveryProfileStages(installation, library, runID string, issuedAt, priorRevision int64, priorSHA string, pages [][]byte, sealRaw []byte) ([][]byte, error) {
	seal, err := teamsValidateOriginalProfileChain(installation, library, pages, sealRaw)
	if err != nil {
		return nil, err
	}
	if priorRevision < 0 || seal.ExpectedPriorSHA256 != priorSHA || (priorRevision == 0) != (priorSHA == "") || seal.ProfileRevision <= priorRevision {
		return nil, errTeamsSourceUnavailable
	}
	var result [][]byte
	var previous *string
	for pageIndex, raw := range pages {
		original, e := sourcewire.DecodeProfilePage(raw)
		if e != nil {
			return nil, e
		}
		originalHash := sourcewire.ProfilePageDigest(raw)
		for start, chunk := 0, 0; start < len(original.Items) || chunk == 0; chunk++ {
			if len(result) >= 1024 {
				return nil, errTeamsSourceUnavailable
			}
			var commandBytes [16]byte
			if _, e = rand.Read(commandBytes[:]); e != nil {
				return nil, e
			}
			commandBytes[6] = (commandBytes[6] & 0x0f) | 0x40
			commandBytes[8] = (commandBytes[8] & 0x3f) | 0x80
			command := fmt.Sprintf("%x-%x-%x-%x-%x", commandBytes[0:4], commandBytes[4:6], commandBytes[6:8], commandBytes[8:10], commandBytes[10:16])
			end := min(start+500, len(original.Items))
			build := func(limit int) ([]byte, string, error) {
				items := make([]sourcewire.RecoveryStageItem, 0, limit-start)
				for _, item := range original.Items[start:limit] {
					items = append(items, sourcewire.RecoveryStageItem{Token: item.Token, Witness: item.Witness, EvidenceRevision: item.EvidenceRevision, Dependencies: append([]string{}, item.Dependencies...), Protection: "unknown", Classification: sourcewire.Classification{State: "unclassified"}})
				}
				finalChunk := limit == len(original.Items)
				final := pageIndex == len(pages)-1 && finalChunk
				stage := sourcewire.RecoveryStagePage{Version: 1, Contract: sourcewire.SourceRecoveryContract, IssuedAt: issuedAt, CommandID: command, RunID: runID, Purpose: "profiles", SnapshotID: seal.SnapshotID, InventorySHA256: seal.InventorySHA256, ProfileID: seal.ProfileID, ProfileVersion: seal.ProfileVersion, ProfileRevision: priorRevision, ProfileSHA256: priorSHA, Index: len(result), PreviousPageSHA256: previous, Final: final, ProfileChunk: &sourcewire.RecoveryProfileChunk{Contract: original.Contract, IssuedAt: original.IssuedAt, UploadID: original.UploadID, SnapshotID: original.SnapshotID, InventorySHA256: original.InventorySHA256, ProfileID: original.ProfileID, ProfileVersion: original.ProfileVersion, ProfileRevision: original.ProfileRevision, LocalRevision: original.LocalRevision, ExpectedPriorSHA256: original.ExpectedPriorSHA256, PageIndex: original.Index, PreviousPageSHA256: original.PreviousPageSHA256, PageSHA256: originalHash, ChunkIndex: chunk, FinalChunk: finalChunk}, Items: items}
				if final {
					stage.ProfileSeal = &seal
				}
				return stage.Bytes()
			}
			var body []byte
			var hash string
			if start == end {
				body, hash, err = build(end)
			} else {
				low, high := start+1, end
				for low <= high {
					mid := (low + high) / 2
					b, h, e := build(mid)
					if e == nil {
						body, hash = b, h
						low = mid + 1
					} else {
						high = mid - 1
					}
				}
			}
			if err != nil || body == nil {
				return nil, errTeamsSourceUnavailable
			}
			stage, e := sourcewire.DecodeRecoveryStagePage(body)
			if e != nil {
				return nil, e
			}
			start += len(stage.Items)
			result = append(result, body)
			previous = &hash
		}
	}
	return result, nil
}

var errTeamsRecoveryFreshProfile = errors.New("recovery needs fresh independently signed profile evidence")

// journalTeamsRecoveryProfileStages retains exact contributor bytes before
// transport. It requires a previously accepted local original 032 chain; no
// producer or network dispatch is enabled by this journal.
func (c *Catalog) journalTeamsRecoveryProfileStages(ctx context.Context, d LibraryDescriptor, credential *teams.EnrollmentCredential, work teams.Work, run sourcewire.RecoveryStartReceipt, uploadID string, priorRevision int64, priorSHA string) ([][]byte, error) {
	if credential == nil || credential.LibraryID != d.ID || work.LibraryID != d.ID || work.VerifyConfig() != nil || work.ExpiresAt.IsZero() || !work.ExpiresAt.After(time.Now().UTC()) || (run.Status != "proof_required" && run.Status != "held") || !run.ExternalProofRequired || run.Generation < 1 || run.ServerTime <= 0 || run.ExpiresAt <= run.ServerTime || run.UnacceptedFloor < 0 {
		return nil, errTeamsConsentStale
	}
	if run.UnacceptedFloor >= math.MaxInt64-1 {
		return nil, errTeamsSourceUnavailable
	}
	issued := max(time.Now().UTC().Unix(), run.ServerTime, run.UnacceptedFloor+1)
	key := credential.SigningKey()
	if len(key) != ed25519.PrivateKeySize {
		clear(key)
		return nil, errTeamsNotEnrolled
	}
	fingerprint := teamsPayloadHash(key.Public().(ed25519.PublicKey))
	clear(key)
	lease, err := c.teamsSharingLease()
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	var out [][]byte
	err = c.writeTransaction(ctx, "teams-recovery-stage-journal", func(tx *sql.Tx) error {
		settings, e := readTeamsSettings(ctx, tx)
		if e != nil {
			return e
		}
		acknowledged, e := readTeamsProtectionAcknowledgement(ctx, tx, d, credential)
		if e != nil {
			return e
		}
		if !acknowledged || !settings.Enabled || settings.MetricsOptOut || settings.LibraryID != d.ID || settings.InstanceID != d.Instance || settings.OrgID != credential.OrgID || settings.InstallationID != credential.InstallationID || settings.KeyFingerprint != fingerprint {
			return errTeamsConsentStale
		}
		var sourceRevision, privacyRevision int64
		if e = tx.QueryRowContext(ctx, `SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&sourceRevision); e != nil {
			return e
		}
		if e = tx.QueryRowContext(ctx, `SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&privacyRevision); e != nil {
			return e
		}
		var rawSeal, sealReceiptRaw []byte
		var origin, org, person, installation, library, instance, workID, snapshot, inventory, state, sealHash string
		var originalExpiry, settingsRev, sourceRev, privacyRev int64
		e = tx.QueryRowContext(ctx, `SELECT origin,org_id,person_id,installation_id,library_id,instance_id,work_id,snapshot_id,inventory_sha256,state,seal_json,seal_sha256,seal_receipt_json,expires_at,settings_revision,source_revision,privacy_revision FROM teams_profile_outbox WHERE upload_id=?`, uploadID).Scan(&origin, &org, &person, &installation, &library, &instance, &workID, &snapshot, &inventory, &state, &rawSeal, &sealHash, &sealReceiptRaw, &originalExpiry, &settingsRev, &sourceRev, &privacyRev)
		if e != nil || state != "accepted" || origin != credential.Origin || org != credential.OrgID || person != credential.PersonID || installation != credential.InstallationID || library != d.ID || instance != d.Instance || workID != work.ID || settingsRev != settings.SettingsRevision || sourceRev != sourceRevision || privacyRev != privacyRevision || sourcewire.ProfileSealDigest(rawSeal) != sealHash {
			return errTeamsConsentStale
		}
		// Expiry is not permission to forge or renew original bytes. A separate
		// fresh 032 upload must reproduce all captured held evidence.
		var sealReceipt sourcewire.ProfileSealReceipt
		if !teams.ValidUniqueJSON(sealReceiptRaw) || json.Unmarshal(sealReceiptRaw, &sealReceipt) != nil || sealReceipt.Status != "accepted" || sealReceipt.SnapshotID != snapshot || sealReceipt.SealSHA256 != sealHash || !sealReceipt.Complete {
			return errTeamsConsentStale
		}
		if originalExpiry <= run.ServerTime {
			return errTeamsRecoveryFreshProfile
		}
		anchor, e := readTeamsV2Upload(ctx, tx, work.ID)
		if e != nil || (anchor.State != "sealed" && anchor.State != "bound") || len(anchor.SealedJSON) == 0 || anchor.SnapshotID != snapshot || anchor.InventoryHash != inventory || anchor.LibraryID != d.ID || anchor.InstanceID != d.Instance || anchor.OrgID != credential.OrgID || anchor.InstallationID != credential.InstallationID || anchor.VersionID != work.VersionID || anchor.ConfigHash != work.ConfigSHA256 || anchor.EvaluationRevision != work.EvaluationRevision || anchor.SettingsRevision != settings.SettingsRevision || anchor.SourceRevision != sourceRevision || anchor.PrivacyRevision != privacyRevision {
			return errTeamsSnapshotChanged
		}
		cache, e := readCacheGenerations(ctx, tx)
		if e != nil {
			return e
		}
		if anchor.Generation != cache.all {
			return errTeamsSnapshotChanged
		}
		rows, e := tx.QueryContext(ctx, `SELECT page_index,page_json,page_sha256,receipt_json FROM teams_profile_outbox_pages WHERE upload_id=? ORDER BY page_index`, uploadID)
		if e != nil {
			return e
		}
		var originals [][]byte
		for rows.Next() {
			var index int
			var body, receiptRaw []byte
			var hash string
			if e = rows.Scan(&index, &body, &hash, &receiptRaw); e != nil {
				rows.Close()
				return e
			}
			if index != len(originals) || sourcewire.ProfilePageDigest(body) != hash {
				rows.Close()
				return errTeamsConsentStale
			}
			var receipt sourcewire.ProfilePageReceipt
			parsedPage, parseErr := sourcewire.DecodeProfilePage(body)
			if parseErr != nil || !teams.ValidUniqueJSON(receiptRaw) || json.Unmarshal(receiptRaw, &receipt) != nil || receipt.Status != "accepted" || receipt.SnapshotID != snapshot || receipt.ProfileID != parsedPage.ProfileID || receipt.ProfileVersion != parsedPage.ProfileVersion || receipt.ProfileRevision != parsedPage.ProfileRevision || receipt.Index != index || receipt.PageSHA256 != hash {
				rows.Close()
				return errTeamsConsentStale
			}
			originals = append(originals, bytes.Clone(body))
		}
		if e = rows.Err(); e != nil {
			rows.Close()
			return e
		}
		if e = rows.Close(); e != nil {
			return e
		}
		validatedSeal, e := teamsValidateOriginalProfileChain(credential.InstallationID, d.ID, originals, rawSeal)
		if e != nil {
			return e
		}
		if validatedSeal.LocalRevision != anchor.LocalRevision {
			return errTeamsSnapshotChanged
		}
		if sealReceipt.ProfileID != validatedSeal.ProfileID || sealReceipt.ProfileVersion != validatedSeal.ProfileVersion || sealReceipt.ProfileRevision != validatedSeal.ProfileRevision || sealReceipt.ItemCount != validatedSeal.ItemCount {
			return errTeamsConsentStale
		}
		var generation, oldSettings, oldSource, oldPrivacy, oldPrior, batchExpiry, batchIssued int64
		var oldSHA, oldState, batchOrg, batchPerson, batchLibrary, batchInstance string
		e = tx.QueryRowContext(ctx, `SELECT generation,settings_revision,source_revision,privacy_revision,prior_profile_revision,prior_profile_sha256,state,expires_at,issued_at,org_id,person_id,library_id,instance_id FROM teams_recovery_stage_batches WHERE run_id=? AND installation_id=? AND upload_id=?`, run.RunID, credential.InstallationID, uploadID).Scan(&generation, &oldSettings, &oldSource, &oldPrivacy, &oldPrior, &oldSHA, &oldState, &batchExpiry, &batchIssued, &batchOrg, &batchPerson, &batchLibrary, &batchInstance)
		if e == nil {
			if generation != run.Generation || oldSettings != settings.SettingsRevision || oldSource != sourceRevision || oldPrivacy != privacyRevision || oldPrior != priorRevision || oldSHA != priorSHA || oldState == "held" || batchExpiry <= run.ServerTime || batchIssued <= 0 || batchIssued > run.ServerTime+int64(sourcewire.RecoveryFutureSkew/time.Second) || batchOrg != credential.OrgID || batchPerson != credential.PersonID || batchLibrary != d.ID || batchInstance != d.Instance {
				return errTeamsConsentStale
			}
			rows, e = tx.QueryContext(ctx, `SELECT page_index,page_json,page_sha256 FROM teams_recovery_stage_pages WHERE run_id=? AND installation_id=? AND upload_id=? ORDER BY page_index`, run.RunID, credential.InstallationID, uploadID)
			if e != nil {
				return e
			}
			defer rows.Close()
			prev := ""
			for rows.Next() {
				var index int
				var body []byte
				var hash string
				if e = rows.Scan(&index, &body, &hash); e != nil {
					return e
				}
				page, e := sourcewire.DecodeRecoveryStagePage(body)
				if e != nil || index != len(out) || page.RunID != run.RunID || page.Index != index || sourcewire.RecoveryStagePageDigest(body) != hash || (index > 0 && (page.PreviousPageSHA256 == nil || *page.PreviousPageSHA256 != prev)) {
					return errTeamsConsentStale
				}
				prev = hash
				out = append(out, bytes.Clone(body))
			}
			if e = rows.Err(); e != nil {
				return e
			}
			if len(out) == 0 {
				return errTeamsConsentStale
			}
			if e = teamsValidateRecoveryProfileStages(run.RunID, priorRevision, priorSHA, originals, rawSeal, out); e != nil {
				return e
			}
			last, e := sourcewire.DecodeRecoveryStagePage(out[len(out)-1])
			if e != nil || !last.Final {
				return errTeamsConsentStale
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if issued > run.ServerTime+int64(sourcewire.RecoveryFutureSkew/time.Second) || issued >= run.ExpiresAt {
			return errTeamsV2Pending
		}
		stages, e := teamsBuildRecoveryProfileStages(credential.InstallationID, d.ID, run.RunID, issued, priorRevision, priorSHA, originals, rawSeal)
		if e != nil {
			return e
		}
		if e = teamsValidateRecoveryProfileStages(run.RunID, priorRevision, priorSHA, originals, rawSeal, stages); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO teams_recovery_stage_batches(run_id,installation_id,upload_id,org_id,person_id,library_id,instance_id,generation,issued_at,expires_at,settings_revision,source_revision,privacy_revision,prior_profile_revision,prior_profile_sha256,state) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'prepared')`, run.RunID, credential.InstallationID, uploadID, credential.OrgID, credential.PersonID, d.ID, d.Instance, run.Generation, issued, min(run.ExpiresAt, originalExpiry), settings.SettingsRevision, sourceRevision, privacyRevision, priorRevision, priorSHA)
		if e != nil {
			return e
		}
		for i, body := range stages {
			page, e := sourcewire.DecodeRecoveryStagePage(body)
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO teams_recovery_stage_pages(run_id,installation_id,upload_id,page_index,command_id,page_json,page_sha256) VALUES(?,?,?,?,?,?,?)`, run.RunID, credential.InstallationID, uploadID, i, page.CommandID, body, sourcewire.RecoveryStagePageDigest(body)); e != nil {
				return e
			}
			out = append(out, bytes.Clone(body))
		}
		return nil
	})
	return out, err
}

// Revalidate journaled stages against the retained 032 chain, not merely
// against their own hashes. A changed token or page header can never be signed
// just because a local page hash was changed alongside it.
func teamsValidateRecoveryProfileStages(runID string, priorRevision int64, priorSHA string, originals [][]byte, sealRaw []byte, stages [][]byte) error {
	seal, err := sourcewire.DecodeProfileSeal(sealRaw)
	if err != nil || len(stages) == 0 {
		return errTeamsConsentStale
	}
	pageIndex, chunkIndex, consumed := 0, 0, 0
	previous := ""
	for i, raw := range stages {
		stage, e := sourcewire.DecodeRecoveryStagePage(raw)
		if e != nil || stage.RunID != runID || stage.Index != i || stage.Purpose != "profiles" || stage.ProfileRevision != priorRevision || stage.ProfileSHA256 != priorSHA || pageIndex >= len(originals) || stage.ProfileChunk == nil {
			return errTeamsConsentStale
		}
		if (i == 0 && stage.PreviousPageSHA256 != nil) || (i > 0 && (stage.PreviousPageSHA256 == nil || *stage.PreviousPageSHA256 != previous)) {
			return errTeamsConsentStale
		}
		original, e := sourcewire.DecodeProfilePage(originals[pageIndex])
		if e != nil {
			return e
		}
		chunk := stage.ProfileChunk
		if chunk.PageIndex != pageIndex || chunk.ChunkIndex != chunkIndex || chunk.PageSHA256 != sourcewire.ProfilePageDigest(originals[pageIndex]) || chunk.Contract != original.Contract || chunk.IssuedAt != original.IssuedAt || chunk.UploadID != original.UploadID || chunk.SnapshotID != original.SnapshotID || chunk.InventorySHA256 != original.InventorySHA256 || chunk.ProfileID != original.ProfileID || chunk.ProfileVersion != original.ProfileVersion || chunk.ProfileRevision != original.ProfileRevision || chunk.LocalRevision != original.LocalRevision || chunk.ExpectedPriorSHA256 != original.ExpectedPriorSHA256 || !equalOptionalString(chunk.PreviousPageSHA256, original.PreviousPageSHA256) {
			return errTeamsConsentStale
		}
		if consumed+len(stage.Items) > len(original.Items) {
			return errTeamsConsentStale
		}
		for j, item := range stage.Items {
			want := original.Items[consumed+j]
			if item.Token != want.Token || item.Witness != want.Witness || item.EvidenceRevision != want.EvidenceRevision || !slices.Equal(item.Dependencies, want.Dependencies) {
				return errTeamsConsentStale
			}
		}
		consumed += len(stage.Items)
		if chunk.FinalChunk {
			if consumed != len(original.Items) {
				return errTeamsConsentStale
			}
			pageIndex++
			chunkIndex = 0
			consumed = 0
		} else {
			chunkIndex++
		}
		if stage.Final != (i == len(stages)-1) {
			return errTeamsConsentStale
		}
		if stage.Final {
			if pageIndex != len(originals) || stage.ProfileSeal == nil {
				return errTeamsConsentStale
			}
			encoded, _, e := stage.ProfileSeal.Bytes()
			if e != nil || !bytes.Equal(encoded, sealRaw) || stage.ProfileSeal.UploadID != seal.UploadID {
				return errTeamsConsentStale
			}
		}
		previous = sourcewire.RecoveryStagePageDigest(raw)
	}
	if pageIndex != len(originals) {
		return errTeamsConsentStale
	}
	return nil
}
func equalOptionalString(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
