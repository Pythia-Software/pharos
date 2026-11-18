package archive

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"maps"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

func teamsSamePilotSnapshot(a, b teamsV2Inventory) bool {
	if a.SourceRevision != b.SourceRevision || a.PrivacyRevision != b.PrivacyRevision || a.Generation != b.Generation || a.Complete != b.Complete || a.Coverage != b.Coverage || len(a.Items) != len(b.Items) || !maps.Equal(a.values, b.values) || !maps.Equal(a.exposures, b.exposures) {
		return false
	}
	for i := range a.Items {
		x, _ := json.Marshal(a.Items[i])
		y, _ := json.Marshal(b.Items[i])
		if !bytes.Equal(x, y) {
			return false
		}
	}
	return true
}

type teamsCensusCaptureKey struct{}

// The capture lock precedes any SQLite transaction. A caller already holding
// the sharing lease takes this next, never the other way around.
var teamsCensusCaptureLease = func(ctx context.Context, cfg Config) (context.Context, func(), error) {
	session, err := beginCapture(cfg)
	if err != nil {
		return ctx, nil, err
	}
	return context.WithValue(ctx, teamsCensusCaptureKey{}, true), func() { _ = session.Close() }, nil
}

var teamsIndependentRosterProof = func(ctx context.Context, tx *sql.Tx, cfg Config, d LibraryDescriptor, credential *teams.EnrollmentCredential) (teamsV2Inventory, error) {
	if held, _ := ctx.Value(teamsCensusCaptureKey{}).(bool); !held {
		return teamsV2Inventory{}, errTeamsSourceUnavailable
	}
	roster, err := teamsCaptureRoster(ctx, cfg)
	if err != nil {
		return teamsV2Inventory{}, err
	}
	return teamsMetadataInventoryIn(ctx, tx, cfg, d, credential, roster)
}

// teamsLocalSourceCensusIn binds independent library-scoped transfer authority
// to the current checkpointed source state and the separately disclosed choice.
func teamsLocalSourceCensusIn(ctx context.Context, tx *sql.Tx, cfg Config, d LibraryDescriptor, credential *teams.EnrollmentCredential) (teamsV2Inventory, teamsOriginBatchScope, error) {
	var snapshot teamsV2Inventory
	var scope teamsOriginBatchScope
	if credential == nil || credential.LibraryID != d.ID {
		return snapshot, scope, errTeamsConsentStale
	}
	key := credential.SigningKey()
	if len(key) != ed25519.PrivateKeySize {
		clear(key)
		return snapshot, scope, errTeamsNotEnrolled
	}
	fingerprint := teamsPayloadHash(key.Public().(ed25519.PublicKey))
	clear(key)
	settings, err := readTeamsSettings(ctx, tx)
	if err != nil {
		return snapshot, scope, err
	}
	local, err := readTeamsProtectionAcknowledgement(ctx, tx, d, credential)
	if err != nil {
		return snapshot, scope, err
	}
	transfer, err := readTeamsSourceTransferAcknowledgement(ctx, tx, d, credential)
	if err != nil {
		return snapshot, scope, err
	}
	if !local || !transfer || !settings.Enabled || settings.LibraryID != d.ID || settings.InstanceID != d.Instance || settings.OrgID != credential.OrgID || settings.InstallationID != credential.InstallationID || settings.KeyFingerprint != fingerprint {
		return snapshot, scope, errTeamsConsentStale
	}
	var state, library, instance, baselineHash string
	var sourceRev, privacyRev int64
	if err = tx.QueryRowContext(ctx, `SELECT state,library_id,instance_id,inventory_sha256,source_revision,privacy_revision FROM teams_protection_baseline WHERE id=1`).Scan(&state, &library, &instance, &baselineHash, &sourceRev, &privacyRev); err != nil {
		return snapshot, scope, err
	}
	if state != "current" || library != d.ID || instance != d.Instance {
		return snapshot, scope, errTeamsProtectionHeld
	}
	var currentSource, currentPrivacy int64
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&currentSource); err != nil {
		return snapshot, scope, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&currentPrivacy); err != nil {
		return snapshot, scope, err
	}
	generation, err := readCacheGenerations(ctx, tx)
	if err != nil {
		return snapshot, scope, err
	}
	if currentSource != sourceRev || currentPrivacy != privacyRev {
		return snapshot, scope, errTeamsSnapshotChanged
	}
	snapshot, err = teamsIndependentRosterProof(ctx, tx, cfg, d, credential)
	if err != nil {
		return teamsV2Inventory{}, scope, err
	}
	if !snapshot.Complete || snapshot.Coverage != (sourcewire.Coverage{}) || snapshot.Items == nil || snapshot.SourceRevision != sourceRev || snapshot.PrivacyRevision != privacyRev || snapshot.Generation != generation.all {
		return teamsV2Inventory{}, scope, errTeamsSourceUnavailable
	}
	h, err := sourcewire.NewInventoryHasher(snapshot.Complete, snapshot.Coverage)
	if err != nil {
		return teamsV2Inventory{}, scope, errTeamsSourceUnavailable
	}
	threads := 0
	for _, item := range snapshot.Items {
		if err = h.Add(item); err != nil {
			return teamsV2Inventory{}, scope, errTeamsSourceUnavailable
		}
		if item.Kind == "codex_thread" {
			threads++
		}
	}
	hash, _, err := h.Finish()
	if err != nil || threads < 1 || threads > 500 || hash != baselineHash {
		return teamsV2Inventory{}, scope, errTeamsSourceUnavailable
	}
	scope = teamsOriginBatchScope{inventoryHash: hash, settingsRevision: settings.SettingsRevision, sourceRevision: sourceRev, privacyRevision: privacyRev, generation: generation.all}
	return snapshot, scope, nil
}

func (c *Catalog) journalTeamsLocalOriginBatch(ctx context.Context, cfg Config, d LibraryDescriptor, credential *teams.EnrollmentCredential, snapshot teamsV2Inventory, tokens []string, scan *teamsRecoveryOriginScan) ([][]byte, error) {
	if len(tokens) == 0 || len(tokens) > 500 {
		return nil, errTeamsSourceUnavailable
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
	var out [][]byte
	err = c.writeTransaction(ctx, "teams-local-origin-batch", func(tx *sql.Tx) error {
		current, scope, e := teamsLocalSourceCensusIn(ctx, tx, cfg, d, credential)
		if e != nil {
			return e
		}
		if !teamsSamePilotSnapshot(current, snapshot) {
			return errTeamsSnapshotChanged
		}
		for binding, revision := range current.exposures {
			if e = reserveTeamsSourceExposure(ctx, tx, d, credential, binding, revision); e != nil {
				return e
			}
		}
		seen := map[string]bool{}
		out = nil
		for _, token := range tokens {
			if seen[token] {
				return errTeamsConsentStale
			}
			seen[token] = true
			body, e := teamsJournalOriginIn(ctx, tx, d, credential, teams.Work{}, snapshot, token, scan, scope)
			if e != nil {
				return e
			}
			v, e := sourcewire.DecodeProtectionObservation(body)
			if e != nil {
				return e
			}
			// Zero is a durable never-sent marker. It is promoted before PUT;
			// old v34 rows without any marker remain ambiguous.
			if _, e = tx.ExecContext(ctx, `INSERT OR IGNORE INTO teams_origin_attempts(observation_id,first_attempt_at) VALUES(?,0)`, v.ObservationID); e != nil {
				return e
			}
			out = append(out, body)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
