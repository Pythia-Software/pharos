package archive

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

const teamsSourceTransferPolicyID = "teams_source_protection_transfer_v2"
const teamsSourceTransferText = "Pharos can send opaque source identifiers, evidence and dependency commitments, and private or unknown protection observations to the Teams service for this organization. Withdrawing this choice stops future source-protection transfers; observations already sent may remain under the organization's retention and recovery rules. This choice does not authorize model processing, content sharing, or sending a response."

func teamsSourceTransferPolicyDigest() string {
	sum := sha256.Sum256([]byte(teamsSourceTransferPolicyID + "\n" + teamsSourceTransferText))
	return hex.EncodeToString(sum[:])
}
func teamsSourceTransferDisclosure(credential *teams.EnrollmentCredential) string {
	if credential == nil {
		return ""
	}
	return fmt.Sprintf("Destination: %s. Organization: %s. %s", credential.Origin, credential.OrgID, teamsSourceTransferText)
}
func readTeamsSourceTransferAcknowledgement(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, d LibraryDescriptor, credential *teams.EnrollmentCredential) (bool, error) {
	if credential == nil {
		return false, nil
	}
	var format string
	if err := q.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&format); err != nil {
		return false, err
	}
	if format != "35" {
		return false, nil
	}
	var policy, digest, origin, org, person, installation, library, instance, generation, currentGeneration string
	err := q.QueryRowContext(ctx, `SELECT a.policy_id,a.policy_sha256,a.origin,a.org_id,a.person_id,a.installation_id,a.library_id,a.instance_id,a.enrollment_generation,e.generation FROM teams_source_transfer_ack a JOIN teams_protection_enrollment e ON e.id=1 WHERE a.id=1`).Scan(&policy, &digest, &origin, &org, &person, &installation, &library, &instance, &generation, &currentGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return policy == teamsSourceTransferPolicyID && digest == teamsSourceTransferPolicyDigest() && origin == credential.Origin && org == credential.OrgID && person == credential.PersonID && installation == credential.InstallationID && library == d.ID && instance == d.Instance && teamsCheckpointHex(generation, 32) && generation == currentGeneration, nil
}

// setTeamsSourceTransferChoice is a separate choice from the local-only policy
// and numeric sharing. It is ordered with source sends by the sharing lease.
func (c *Catalog) setTeamsSourceTransferChoice(ctx context.Context, d LibraryDescriptor, credential *teams.EnrollmentCredential, expected int64, accept bool) error {
	if credential == nil || credential.LibraryID != d.ID {
		return errTeamsConsentStale
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
	return c.writeTransaction(ctx, "teams-source-transfer-choice", func(tx *sql.Tx) error {
		var format string
		if e := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&format); e != nil {
			return e
		}
		if format != "35" {
			return errTeamsProtectionHeld
		}
		settings, e := readTeamsSettings(ctx, tx)
		if e != nil {
			return e
		}
		if settings.LibraryID != d.ID || settings.InstanceID != d.Instance || settings.OrgID != credential.OrgID || settings.InstallationID != credential.InstallationID || settings.KeyFingerprint != fingerprint || settings.SettingsRevision != expected {
			return errTeamsConsentStale
		}
		if !accept {
			if _, e = tx.ExecContext(ctx, `DELETE FROM teams_source_transfer_ack`); e != nil {
				return e
			}
			return nil
		}
		local, e := readTeamsProtectionAcknowledgement(ctx, tx, d, credential)
		if e != nil {
			return e
		}
		if !local {
			return errTeamsConsentStale
		}
		acknowledged, e := readTeamsSourceTransferAcknowledgement(ctx, tx, d, credential)
		if e != nil || acknowledged {
			return e
		}
		var generation string
		if e = tx.QueryRowContext(ctx, `SELECT generation FROM teams_protection_enrollment WHERE id=1`).Scan(&generation); e != nil || !teamsCheckpointHex(generation, 32) {
			return errTeamsConsentStale
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO teams_source_transfer_ack(id,policy_id,policy_sha256,origin,org_id,person_id,installation_id,library_id,instance_id,enrollment_generation,acknowledged_at) VALUES(1,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET policy_id=excluded.policy_id,policy_sha256=excluded.policy_sha256,origin=excluded.origin,org_id=excluded.org_id,person_id=excluded.person_id,installation_id=excluded.installation_id,library_id=excluded.library_id,instance_id=excluded.instance_id,enrollment_generation=excluded.enrollment_generation,acknowledged_at=excluded.acknowledged_at`, teamsSourceTransferPolicyID, teamsSourceTransferPolicyDigest(), credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, generation, time.Now().UTC().Format(time.RFC3339Nano))
		return e
	})
}
