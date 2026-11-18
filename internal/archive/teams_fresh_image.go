package archive

import (
	"context"
	"errors"
	"fmt"
)

const teamsFreshImageLimit = 64 << 20

// teamsBuildFreshImage creates an empty protected catalog on a private,
// single-connection in-memory driver. It does not publish or authorize a
// library; callers must bind and durably install the image before use.
func teamsBuildFreshImage(ctx context.Context, libraryID, instanceID string) ([]byte, *teamsCheckpointTuple, error) {
	if libraryID == "" || instanceID == "" {
		return nil, nil, errTeamsProtectionContinuity
	}
	w, err := teamsOpenProtectedWriterDSN(":memory:")
	if err != nil {
		return nil, nil, err
	}
	defer w.db.Close()
	w.authorized.Store(true)
	defer w.authorized.Store(false)
	c := &Catalog{DB: w.db, Path: ":memory:"}
	if err = c.Initialize(); err != nil {
		return nil, nil, err
	}
	cn, err := w.db.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer cn.Close()
	tx, err := cn.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, teamsProtectedFormat35DDL); err != nil {
		return nil, nil, err
	}
	for _, fence := range teamsProtectionFenceStatements35() {
		if _, err = tx.ExecContext(ctx, fence.sql); err != nil {
			return nil, nil, err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE meta SET value='35' WHERE key='schema_version'"); err != nil {
		return nil, nil, err
	}
	actual, err := teamsActualSchemaDigest35(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO teams_protection_open_contract(id,initializer_epoch,compiled_schema_sha256,actual_schema_sha256,catalog_version,library_version) VALUES(1,?,?,?,?,?)", teamsProtectionInitializerEpoch, teamsCompiledSchemaDigest35(), actual, teamsProtectedFormat35, libraryVersion()); err != nil {
		return nil, nil, err
	}
	if err = teamsVerifyProtectionFence(ctx, tx); err != nil {
		return nil, nil, err
	}
	valid, err := teamsOpenContractMatches(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	if !valid {
		return nil, nil, errTeamsProtectionContinuity
	}
	digest, err := teamsProtectionProjection(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	revision, err := teamsCheckpointFenceRevision(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	challenge, err := teamsCheckpointRandom()
	if err != nil {
		return nil, nil, err
	}
	tuple := &teamsCheckpointTuple{Generation: 1, Instance: teamsCheckpointInstance(libraryID, instanceID), Revision: revision, CommitID: challenge, Digest: digest}
	if err = teamsCheckpointPutTuple(ctx, tx, tuple); err != nil {
		return nil, nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, nil, err
	}
	var image []byte
	err = cn.Raw(func(raw any) error {
		serializer, ok := raw.(interface{ Serialize() ([]byte, error) })
		if !ok {
			return errors.New("SQLite serialization unavailable")
		}
		var e error
		image, e = serializer.Serialize()
		return e
	})
	if err != nil {
		return nil, nil, err
	}
	if len(image) == 0 || len(image) > teamsFreshImageLimit {
		return nil, nil, fmt.Errorf("fresh catalog image exceeds size bound")
	}
	return image, tuple, nil
}
