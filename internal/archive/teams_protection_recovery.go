package archive

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
)

// prepareTeamsRecoveredCopy operates only on the isolated, unpublished
// recovery staging copy. It never changes the source catalog or host object.
// A clone cannot prove local-only history from its own checkpoint, so it
// retains positives and adds a library-wide unproved recovery assertion.
func prepareTeamsRecoveredCopy(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", catalogDSNWithPragmas(path, durableCatalog)+"&_txlock=immediate")
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var metaExists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='meta')`).Scan(&metaExists); err != nil {
		return err
	}
	if !metaExists {
		return nil
	}
	var version string
	if err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&version); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	numeric, e := strconv.Atoi(version)
	if e != nil {
		return errTeamsProtectionContinuity
	}
	if numeric <= catalogSchemaVersion {
		return nil
	}
	if version != strconv.Itoa(protectedCatalogSchemaVersion) {
		return errTeamsProtectionContinuity
	}
	if err = teamsRecoveryStructureSupported(ctx, tx); err != nil {
		return err
	}
	for _, fence := range teamsProtectionFenceStatements() {
		if _, err = tx.ExecContext(ctx, "DROP TRIGGER IF EXISTS "+quotedIdentifier(fence.name)); err != nil {
			return err
		}
	}
	for _, statement := range []string{
		`DELETE FROM teams_protection_observations`,
		`DELETE FROM teams_protection_policy_ack`,
		`DELETE FROM teams_protection_enrollment`,
		`DELETE FROM teams_protection_checkpoint`,
		`DELETE FROM teams_protection_open_contract`,
		`UPDATE teams_protection_baseline SET state='held',generation=generation+1 WHERE id=1`,
		`INSERT INTO teams_protection_assertions(conversation_id,provider,account,native_id,kind,evidence,active,created_revision) SELECT '','','','','legacy_hold','unproved_recovery',1,revision FROM teams_privacy_revision WHERE id=1 AND NOT EXISTS(SELECT 1 FROM teams_protection_assertions WHERE conversation_id='' AND provider='' AND account='' AND native_id='' AND kind='legacy_hold' AND evidence='unproved_recovery' AND active=1)`,
		`INSERT OR REPLACE INTO meta(key,value) VALUES('teams_protection_recovered_unproved','1')`,
	} {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE meta SET value=? WHERE key='schema_version' AND value=?`, strconv.Itoa(catalogSchemaVersion), strconv.Itoa(protectedCatalogSchemaVersion))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return errors.New("recovery schema marker changed")
	}
	return tx.Commit()
}

func (c *Catalog) teamsMarkRecoveryUnproved(ctx context.Context) error {
	// This hold remains until a dedicated recovery release rule exists. It
	// holds future protection proof; existing numeric policy is independent.
	return c.writeTransaction(ctx, "teams-recovered-history-hold", func(tx *sql.Tx) error {
		for _, q := range []string{
			`UPDATE teams_protection_baseline SET state='held',generation=generation+1 WHERE id=1 AND state<>'held'`,
			`INSERT INTO teams_protection_assertions(conversation_id,provider,account,native_id,kind,evidence,active,created_revision) SELECT '','','','','legacy_hold','unproved_recovery',1,revision FROM teams_privacy_revision WHERE id=1 AND NOT EXISTS(SELECT 1 FROM teams_protection_assertions WHERE conversation_id='' AND provider='' AND account='' AND native_id='' AND kind='legacy_hold' AND evidence='unproved_recovery' AND active=1)`,
			`INSERT OR REPLACE INTO meta(key,value) VALUES('teams_protection_recovered_unproved','1')`,
		} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
		return nil
	})
}
