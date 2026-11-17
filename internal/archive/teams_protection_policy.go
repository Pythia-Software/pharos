package archive

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"errors"
	"math"
	"time"

	"encoding/hex"
	"github.com/gbdubs/pharos/internal/teams"
)

// This contract is independent of the numeric metrics policy and grants no
// model, content-processing, or send permission. Its exact text is hashed
// so a semantic change requires a fresh acknowledgement.
const teamsLocalProtectionPolicyID = "teams_local_protection_baseline_v1"
const teamsLocalProtectionDisclosure = "Pharos can establish current non-private source observations when it verifies independent source and capture evidence. Retained private, flagged, inherited, and unknown protections remain in effect. Classification progress is separate. This does not authorize model processing, content sharing, or sending a response."

func teamsLocalProtectionPolicyDigest() string {
	sum := sha256.Sum256([]byte(teamsLocalProtectionPolicyID + "\n" + teamsLocalProtectionDisclosure))
	return hex.EncodeToString(sum[:])
}

// This describes an acknowledgement only; its presence does not establish a
// baseline or prove that local assertion history survived a restore.
type teamsProtectionPolicyAcknowledgement struct {
	PolicyID, PolicyDigest                                         string
	Origin, OrgID, PersonID, InstallationID, LibraryID, InstanceID string
	EnrollmentGeneration                                           string
}

func (a teamsProtectionPolicyAcknowledgement) applies(scope teamsProtectionPolicyAcknowledgement) bool {
	return a.PolicyID == teamsLocalProtectionPolicyID && a.PolicyDigest == teamsLocalProtectionPolicyDigest() &&
		a.Origin != "" && a.Origin == scope.Origin && a.OrgID != "" && a.OrgID == scope.OrgID &&
		a.PersonID != "" && a.PersonID == scope.PersonID && a.InstallationID != "" && a.InstallationID == scope.InstallationID &&
		a.LibraryID != "" && a.LibraryID == scope.LibraryID && a.InstanceID != "" && a.InstanceID == scope.InstanceID &&
		teamsCheckpointHex(a.EnrollmentGeneration, 32) && a.EnrollmentGeneration == scope.EnrollmentGeneration
}

func teamsProtectionPolicyScope(d LibraryDescriptor, credential *teams.EnrollmentCredential, generation string) teamsProtectionPolicyAcknowledgement {
	if credential == nil {
		return teamsProtectionPolicyAcknowledgement{}
	}
	return teamsProtectionPolicyAcknowledgement{Origin: credential.Origin, OrgID: credential.OrgID, PersonID: credential.PersonID, InstallationID: credential.InstallationID, LibraryID: d.ID, InstanceID: d.Instance, EnrollmentGeneration: generation}
}
func readTeamsProtectionAcknowledgement(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, d LibraryDescriptor, credential *teams.EnrollmentCredential) (bool, error) {
	var a teamsProtectionPolicyAcknowledgement
	var generation string
	err := q.QueryRowContext(ctx, `SELECT a.policy_id,a.policy_sha256,a.origin,a.org_id,a.person_id,a.installation_id,a.library_id,a.instance_id,a.enrollment_generation,e.generation FROM teams_protection_policy_ack a JOIN teams_protection_enrollment e ON e.id=1 WHERE a.id=1`).Scan(&a.PolicyID, &a.PolicyDigest, &a.Origin, &a.OrgID, &a.PersonID, &a.InstallationID, &a.LibraryID, &a.InstanceID, &a.EnrollmentGeneration, &generation)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return a.applies(teamsProtectionPolicyScope(d, credential, generation)), nil
}

// The acknowledgement is a library-scoped local settings action, not a proof
// or a send permission. Existing numeric policy values are left unchanged.
func (c *Catalog) acknowledgeTeamsProtectionPolicy(ctx context.Context, d LibraryDescriptor, credential *teams.EnrollmentCredential, expected int64) error {
	lease, err := c.teamsSharingLease()
	if err != nil {
		return err
	}
	defer lease.Close()
	return c.writeTransaction(ctx, "teams-protection-policy", func(tx *sql.Tx) error {
		settings, e := readTeamsSettings(ctx, tx)
		if e != nil {
			return e
		}
		if credential == nil || credential.LibraryID != d.ID || credential.Origin == "" || credential.PersonID == "" || settings.LibraryID != d.ID || settings.InstanceID != d.Instance || settings.OrgID != credential.OrgID || settings.InstallationID != credential.InstallationID || settings.SettingsRevision != expected {
			return errTeamsConsentStale
		}
		key := credential.SigningKey()
		defer clear(key)
		if len(key) != ed25519.PrivateKeySize || teamsPayloadHash(key.Public().(ed25519.PublicKey)) != settings.KeyFingerprint {
			return errTeamsConsentStale
		}
		applicable, e := readTeamsProtectionAcknowledgement(ctx, tx, d, credential)
		if e != nil {
			return e
		}
		if applicable {
			return nil
		}
		var generation string
		e = tx.QueryRowContext(ctx, `SELECT generation FROM teams_protection_enrollment WHERE id=1`).Scan(&generation)
		if errors.Is(e, sql.ErrNoRows) {
			generation, e = teamsCheckpointRandom()
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO teams_protection_enrollment(id,generation) VALUES(1,?)`, generation); e != nil {
				return e
			}
		} else if e != nil {
			return e
		}
		if !teamsCheckpointHex(generation, 32) || settings.SettingsRevision >= math.MaxInt64-1 || settings.LocalRevision >= math.MaxInt64-1 {
			return errTeamsConsentStale
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO teams_protection_policy_ack(id,policy_id,policy_sha256,origin,org_id,person_id,installation_id,library_id,instance_id,enrollment_generation,settings_revision,acknowledged_at) VALUES(1,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET policy_id=excluded.policy_id,policy_sha256=excluded.policy_sha256,origin=excluded.origin,org_id=excluded.org_id,person_id=excluded.person_id,installation_id=excluded.installation_id,library_id=excluded.library_id,instance_id=excluded.instance_id,enrollment_generation=excluded.enrollment_generation,settings_revision=excluded.settings_revision,acknowledged_at=excluded.acknowledged_at`, teamsLocalProtectionPolicyID, teamsLocalProtectionPolicyDigest(), credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, d.ID, d.Instance, generation, settings.SettingsRevision+1, time.Now().UTC().Format(time.RFC3339Nano))
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE teams_local_settings SET settings_revision=settings_revision+1,local_revision=local_revision+1 WHERE id=1`)
		return e
	})
}
