package archive

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"math"
	"reflect"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

// journalTeamsOrigin prepares a conservative 031 observation for one actual
// 030 inventory item after a current signed origins page. It can add explicit
// private or unknown evidence but cannot clear an existing positive origin.
// Neither the row nor a recovery page grants disclosure or query authority.
// journalTeamsOrigin preserves the single-item call contract for existing tests.
func (c *Catalog) journalTeamsOrigin(ctx context.Context, d LibraryDescriptor, credential *teams.EnrollmentCredential, work teams.Work, snapshot teamsV2Inventory, token string, start sourcewire.RecoveryStartReceipt, request sourcewire.RecoveryPageRequest, page sourcewire.RecoveryPageReceipt) ([]byte, error) {
	rows, err := c.journalTeamsOriginBatch(ctx, d, credential, work, snapshot, []string{token}, start, request, page)
	if err != nil {
		return nil, err
	}
	return rows[0], nil
}

type teamsOriginBatchScope struct {
	snapshotID, inventoryHash                                     string
	tightening                                                    bool
	settingsRevision, sourceRevision, privacyRevision, generation int64
}

// All candidate observations are validated and inserted in one protected
// transaction. A partial batch is never eligible for its first network send.
func (c *Catalog) journalTeamsOriginBatch(ctx context.Context, d LibraryDescriptor, credential *teams.EnrollmentCredential, work teams.Work, snapshot teamsV2Inventory, tokens []string, start sourcewire.RecoveryStartReceipt, request sourcewire.RecoveryPageRequest, page sourcewire.RecoveryPageReceipt) ([][]byte, error) {
	if len(tokens) == 0 || len(tokens) > 500 {
		return nil, errTeamsSourceUnavailable
	}
	scan, err := teamsBuildRecoveryOriginScan(start, []sourcewire.RecoveryPageRequest{request}, []sourcewire.RecoveryPageReceipt{page})
	if err != nil {
		return nil, err
	}
	lease, err := c.teamsSharingLease()
	if err != nil {
		return nil, err
	}
	defer lease.Close()
	var out [][]byte
	err = c.writeTransaction(ctx, "teams-origin-batch-journal", func(tx *sql.Tx) error {
		scope, e := teamsValidateOriginBatchIn(ctx, tx, d, credential, work, snapshot)
		if e != nil {
			return e
		}
		seen := map[string]bool{}
		out = nil
		for _, token := range tokens {
			if seen[token] {
				return errTeamsConsentStale
			}
			seen[token] = true
			raw, e := teamsJournalOriginIn(ctx, tx, d, credential, work, snapshot, token, scan, scope)
			if e != nil {
				return e
			}
			out = append(out, raw)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func teamsValidateOriginBatchIn(ctx context.Context, tx *sql.Tx, d LibraryDescriptor, credential *teams.EnrollmentCredential, work teams.Work, snapshot teamsV2Inventory) (teamsOriginBatchScope, error) {
	var out teamsOriginBatchScope
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
	settings, e := readTeamsSettings(ctx, tx)
	if e != nil {
		return out, e
	}
	ack, e := readTeamsProtectionAcknowledgement(ctx, tx, d, credential)
	if e != nil {
		return out, e
	}
	if !ack || !settings.Enabled || settings.MetricsOptOut || settings.LibraryID != d.ID || settings.InstanceID != d.Instance || settings.OrgID != credential.OrgID || settings.InstallationID != credential.InstallationID || settings.KeyFingerprint != fingerprint {
		return out, errTeamsConsentStale
	}
	if e = tx.QueryRowContext(ctx, `SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&out.sourceRevision); e != nil {
		return out, e
	}
	if e = tx.QueryRowContext(ctx, `SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&out.privacyRevision); e != nil {
		return out, e
	}
	generation, e := readCacheGenerations(ctx, tx)
	if e != nil {
		return out, e
	}
	out.generation = generation.all
	out.settingsRevision = settings.SettingsRevision
	anchor, e := readTeamsV2Upload(ctx, tx, work.ID)
	if e != nil || (anchor.State != "sealed" && anchor.State != "bound") || len(anchor.SealedJSON) == 0 || anchor.LibraryID != d.ID || anchor.InstanceID != d.Instance || anchor.OrgID != credential.OrgID || anchor.InstallationID != credential.InstallationID || anchor.ConfigHash != work.ConfigSHA256 || anchor.VersionID != work.VersionID || anchor.EvaluationRevision != work.EvaluationRevision || anchor.SettingsRevision != settings.SettingsRevision || anchor.SourceRevision != out.sourceRevision || anchor.PrivacyRevision != out.privacyRevision || anchor.Generation != out.generation || snapshot.SourceRevision != out.sourceRevision || snapshot.PrivacyRevision != out.privacyRevision || snapshot.Generation != out.generation {
		return out, errTeamsSnapshotChanged
	}
	sealed, e := sourcewire.DecodeSeal(anchor.Seal)
	if e != nil {
		return out, errTeamsConsentStale
	}
	rebuilt, e := teamsBuildV2Pages(snapshot, anchor.SnapshotID, anchor.LocalRevision, anchor.InventoryRevision, anchor.EvaluationRevision, sealed.ExpectedPriorSHA256)
	if e != nil || rebuilt.InventorySHA256 != anchor.InventoryHash || !bytes.Equal(rebuilt.Seal, anchor.Seal) || len(rebuilt.Pages) != len(anchor.Pages) {
		return out, errTeamsSnapshotChanged
	}
	for i := range rebuilt.Pages {
		if !bytes.Equal(rebuilt.Pages[i], anchor.Pages[i]) {
			return out, errTeamsSnapshotChanged
		}
	}
	out.snapshotID, out.inventoryHash = anchor.SnapshotID, anchor.InventoryHash
	return out, nil
}

// The reviewed non-private producer remains dormant until cross-service
// proof, replay and floor tests are complete.
var teamsNonPrivateSourceProofEnabled = false

func teamsJournalOriginIn(ctx context.Context, tx *sql.Tx, d LibraryDescriptor, credential *teams.EnrollmentCredential, work teams.Work, snapshot teamsV2Inventory, token string, scan *teamsRecoveryOriginScan, scope teamsOriginBatchScope) ([]byte, error) {
	if scan == nil || scan.held {
		return nil, errTeamsPilotPending
	}
	start, page := scan.start, scan.last
	if credential == nil || credential.LibraryID != d.ID {
		return nil, errTeamsConsentStale
	}
	var item *sourcewire.Item
	for i := range snapshot.Items {
		if snapshot.Items[i].Token == token {
			if item != nil {
				return nil, errTeamsSourceUnavailable
			}
			item = &snapshot.Items[i]
		}
	}
	if item == nil || item.Kind != "codex_thread" || (item.Protection != "private" && item.Protection != "unknown" && item.Protection != "public") || item.EvidenceRevision == "" {
		return nil, errTeamsSourceUnavailable
	}
	previous, protection, err := scan.lookup(item.Token, item.Witness)
	if err != nil || previous == math.MaxInt64 || page.UnacceptedFloor == math.MaxInt64 {
		return nil, errTeamsConsentStale
	}
	nextProtection := "unknown"
	if item.Protection == "private" {
		nextProtection = "explicit_private"
	} else if teamsNonPrivateSourceProofEnabled && !scope.tightening && item.Protection == "public" && work.ID == "" && scope.snapshotID == "" {
		// Only the independent local census calls this lane with a current
		// checkpointed baseline and separately acknowledged transfer. Its
		// public state requires the exact reviewed source evidence.
		// An existing unknown or restrictive server floor is never cleared.
		prior, exists := scan.byToken[item.Token]
		if !exists || (prior.Protection == "non_private" && prior.FlagFloor != "unknown" && (!prior.Classification.Required || prior.Classification.State == "clear")) {
			nextProtection = "non_private"
		}
	}
	if scope.tightening {
		prior, exists := scan.byToken[item.Token]
		sameRestrictionNewEvidence := exists && prior.EvidenceRevision != item.EvidenceRevision && ((protection == "explicit_private" && nextProtection == "explicit_private") || (protection == "unknown" && nextProtection == "unknown"))
		if !((protection == "non_private" && (nextProtection == "unknown" || nextProtection == "explicit_private")) || (protection == "unknown" && nextProtection == "explicit_private") || sameRestrictionNewEvidence) {
			return nil, errTeamsProtectionHeld
		}
	} else if protection != "" && protection != "unknown" && protection != nextProtection {
		return nil, errTeamsSourceUnavailable
	}
	now := time.Now().UTC()
	issued := max(now.Unix(), page.ServerTime, page.UnacceptedFloor+1)
	if issued >= start.ExpiresAt || issued > now.Add(sourcewire.RecoveryFutureSkew).Unix() {
		return nil, errTeamsConsentStale
	}
	observation := sourcewire.ProtectionObservation{Version: 1, Contract: sourcewire.SourceRecoveryContract, IssuedAt: issued, ExpectedRevision: previous, Revision: previous + 1, Token: item.Token, Witness: item.Witness, Dependencies: append([]string{}, item.Dependencies...), EvidenceRevision: item.EvidenceRevision, Protection: nextProtection, Classification: sourcewire.Classification{State: "unclassified"}}
	observation.ObservationID, err = sourcewire.ProtectionObservationID(observation, credential.InstallationID, d.ID)
	if err != nil {
		return nil, errTeamsConsentStale
	}
	raw, hash, err := observation.Bytes()
	if err != nil {
		return nil, errTeamsConsentStale
	}
	err = func() error {
		var e error
		var existing []byte
		var existingHash, existingState string
		e = tx.QueryRowContext(ctx, `SELECT observation_json,observation_sha256,state FROM teams_origin_outbox WHERE origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND work_id=? AND snapshot_id=? AND inventory_sha256=? AND settings_revision=? AND source_revision=? AND privacy_revision=? AND cache_generation=? AND run_id=? AND run_generation=? AND expires_at=? AND token=? AND expected_revision=?`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, work.ID, scope.snapshotID, scope.inventoryHash, scope.settingsRevision, scope.sourceRevision, scope.privacyRevision, scope.generation, start.RunID, start.Generation, start.ExpiresAt, observation.Token, observation.ExpectedRevision).Scan(&existing, &existingHash, &existingState)
		if e == nil {
			prior, decodeErr := sourcewire.DecodeProtectionObservation(existing)
			oldID, idErr := sourcewire.ProtectionObservationID(prior, credential.InstallationID, d.ID)
			candidate := observation
			candidate.ObservationID, candidate.IssuedAt = prior.ObservationID, prior.IssuedAt
			if decodeErr != nil || idErr != nil || prior.ObservationID != oldID || existingHash != sourcewire.ProtectionDigest(existing) || !reflect.DeepEqual(prior, candidate) {
				return errTeamsConsentStale
			}
			if existingState == "held" {
				return errTeamsRecoveryFreshProfile
			}
			if existingState != "prepared" && existingState != "accepted" {
				return errTeamsConsentStale
			}
			raw = existing
			return nil
		}
		if e != sql.ErrNoRows {
			return e
		}
		var high int64
		if e = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(CAST(json_extract(observation_json,'$.issued_at') AS INTEGER)),0) FROM teams_origin_outbox WHERE org_id=? AND installation_id=? AND library_id=? AND instance_id=? AND token=?`, credential.OrgID, credential.InstallationID, d.ID, d.Instance, observation.Token).Scan(&high); e != nil {
			return e
		}
		if high == math.MaxInt64 {
			return errTeamsConsentStale
		}
		observation.IssuedAt = max(observation.IssuedAt, high+1)
		if observation.IssuedAt >= start.ExpiresAt || observation.IssuedAt > time.Now().UTC().Add(sourcewire.RecoveryFutureSkew).Unix() {
			return errTeamsConsentStale
		}
		observation.ObservationID, e = sourcewire.ProtectionObservationID(observation, credential.InstallationID, d.ID)
		if e != nil {
			return errTeamsConsentStale
		}
		raw, hash, e = observation.Bytes()
		if e != nil {
			return errTeamsConsentStale
		}
		res, e := tx.ExecContext(ctx, `INSERT INTO teams_origin_outbox(observation_id,origin,org_id,person_id,installation_id,library_id,instance_id,work_id,snapshot_id,inventory_sha256,settings_revision,source_revision,privacy_revision,cache_generation,run_id,run_generation,expires_at,token,expected_revision,observation_json,observation_sha256,state) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'prepared') ON CONFLICT DO NOTHING`, observation.ObservationID, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, work.ID, scope.snapshotID, scope.inventoryHash, scope.settingsRevision, scope.sourceRevision, scope.privacyRevision, scope.generation, start.RunID, start.Generation, start.ExpiresAt, observation.Token, observation.ExpectedRevision, raw, hash)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return e
		}
		if n == 0 {
			var old []byte
			var oldHash, state string
			e = tx.QueryRowContext(ctx, `SELECT observation_json,observation_sha256,state FROM teams_origin_outbox WHERE origin=? AND org_id=? AND person_id=? AND installation_id=? AND library_id=? AND instance_id=? AND work_id=? AND snapshot_id=? AND inventory_sha256=? AND settings_revision=? AND source_revision=? AND privacy_revision=? AND cache_generation=? AND run_id=? AND run_generation=? AND expires_at=? AND token=? AND expected_revision=?`, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, work.ID, scope.snapshotID, scope.inventoryHash, scope.settingsRevision, scope.sourceRevision, scope.privacyRevision, scope.generation, start.RunID, start.Generation, start.ExpiresAt, observation.Token, observation.ExpectedRevision).Scan(&old, &oldHash, &state)
			prior, decodeErr := sourcewire.DecodeProtectionObservation(old)
			oldID, idErr := sourcewire.ProtectionObservationID(prior, credential.InstallationID, d.ID)
			candidate := observation
			candidate.ObservationID, candidate.IssuedAt = prior.ObservationID, prior.IssuedAt
			if e != nil || decodeErr != nil || idErr != nil || prior.ObservationID != oldID || oldHash != sourcewire.ProtectionDigest(old) || !reflect.DeepEqual(prior, candidate) {
				return errTeamsConsentStale
			}
			if state == "held" {
				return errTeamsRecoveryFreshProfile
			}
			if state != "prepared" && state != "accepted" {
				return errTeamsConsentStale
			}
			raw = old
		}
		return nil
	}()
	if err != nil {
		return nil, err
	}
	return raw, nil
}
