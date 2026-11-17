package archive

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

var errTeamsSourceRemapRequired = errors.New("source identity was exposed; reconciliation is required before changing it")

// reviewTeamsSourceMapping records the member's attestation for the exact
// enrolled identity. It is not provider verification or permission to send.
// The caller must obtain credential via loadTeamsCredential for this library.
func (c *Catalog) reviewTeamsSourceMapping(ctx context.Context, d LibraryDescriptor, credential *teams.EnrollmentCredential, bindingID, mode string, expectedRevision int64, attested bool) error {
	if credential == nil || credential.LibraryID != d.ID || credential.Origin == "" || credential.PersonID == "" || (mode != "work" && mode != "unavailable") || (mode == "work" && !attested) {
		return errTeamsConsentStale
	}
	lease, err := c.teamsSharingLease()
	if err != nil {
		return err
	}
	defer lease.Close()
	return c.writeTransaction(ctx, "teams-source-mapping", func(tx *sql.Tx) error {
		s, e := readTeamsSettings(ctx, tx)
		if e != nil {
			return e
		}
		if s.LibraryID != d.ID || s.InstanceID != d.Instance || s.OrgID != credential.OrgID || s.InstallationID != credential.InstallationID {
			return errTeamsNotEnrolled
		}
		var revision int64
		if e = tx.QueryRowContext(ctx, `SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&revision); e != nil {
			return e
		}
		if revision != expectedRevision {
			return errTeamsConsentStale
		}
		var provider string
		if e = tx.QueryRowContext(ctx, `SELECT provider FROM teams_source_bindings WHERE binding_id=? AND superseded_at=''`, bindingID).Scan(&provider); e != nil {
			return errTeamsConsentStale
		}
		if provider != "codex" && provider != "claude" {
			return errTeamsConsentStale
		}
		var exposure int
		if e = tx.QueryRowContext(ctx, `WITH RECURSIVE lineage(binding_id) AS (
 SELECT ? UNION SELECT prior.binding_id FROM lineage l JOIN teams_source_bindings current ON current.binding_id=l.binding_id JOIN teams_source_bindings prior ON prior.host_id=current.host_id AND lower(prior.source_kind)=lower(current.source_kind) AND (prior.source_name=current.source_name OR prior.source_root=current.source_root)
) SELECT COUNT(*) FROM teams_source_mapping_exposure WHERE binding_id IN (SELECT binding_id FROM lineage)`, bindingID).Scan(&exposure); e != nil {
			return e
		}
		if exposure != 0 && mode != "unavailable" {
			return errTeamsSourceRemapRequired
		}
		res, e := tx.ExecContext(ctx, `INSERT INTO teams_source_mapping_revisions(binding_id,library_id,instance_id,origin,org_id,person_id,installation_id,provider,mode,reviewed_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, bindingID, d.ID, d.Instance, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, provider, mode, time.Now().UTC().Format(time.RFC3339Nano))
		if e != nil {
			return e
		}
		id, e := res.LastInsertId()
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO teams_source_mapping_current(binding_id,revision) VALUES(?,?) ON CONFLICT(binding_id) DO UPDATE SET revision=excluded.revision`, bindingID, id); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE teams_source_revision SET revision=revision+1 WHERE id=1`); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE teams_local_settings SET settings_revision=settings_revision+1,local_revision=local_revision+1 WHERE id=1`)
		return e
	})
}

// reserveTeamsSourceExposure is called under the sharing lease, in a durable
// write transaction, before the first manifest, protection update, or result
// can be emitted. A lost response is already an exposure. No caller currently
// enables v2 sending; integration must supply and recheck the sealed mapping.
func reserveTeamsSourceExposure(ctx context.Context, tx *sql.Tx, d LibraryDescriptor, credential *teams.EnrollmentCredential, bindingID string, revision int64) error {
	if credential == nil || credential.LibraryID != d.ID {
		return errTeamsConsentStale
	}
	settings, err := readTeamsSettings(ctx, tx)
	if err != nil {
		return err
	}
	if settings.LibraryID != d.ID || settings.InstanceID != d.Instance || settings.OrgID != credential.OrgID || settings.InstallationID != credential.InstallationID {
		return errTeamsNotEnrolled
	}
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM teams_source_mapping_current WHERE binding_id=?`, bindingID).Scan(&current); err != nil || current != revision {
		return errTeamsConsentStale
	}
	var mode string
	if err := tx.QueryRowContext(ctx, `SELECT mode FROM teams_source_mapping_revisions WHERE revision=? AND binding_id=? AND library_id=? AND instance_id=? AND origin=? AND org_id=? AND person_id=? AND installation_id=?`, revision, bindingID, d.ID, d.Instance, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID).Scan(&mode); err != nil || mode != "work" {
		return errTeamsConsentStale
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO teams_source_mapping_exposure(binding_id,mapping_revision,exposed_at) VALUES(?,?,?) ON CONFLICT(binding_id) DO NOTHING`, bindingID, revision, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	var exposed int64
	if err := tx.QueryRowContext(ctx, `SELECT mapping_revision FROM teams_source_mapping_exposure WHERE binding_id=?`, bindingID).Scan(&exposed); err != nil {
		return err
	}
	if exposed != revision {
		return errTeamsSourceRemapRequired
	}
	return nil
}
