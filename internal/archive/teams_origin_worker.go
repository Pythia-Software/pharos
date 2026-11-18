package archive

import (
	"context"
	"errors"
)

// Caller holds the sharing lease when this check immediately precedes an
// external effect. Reconciliation includes the durable pending-commit barrier.
func (c *Catalog) teamsPilotCheckpointCurrent(ctx context.Context, d LibraryDescriptor) error {
	var format string
	if err := c.reader().QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&format); err != nil || format != "35" {
		return errTeamsProtectionHeld
	}
	checkpoint := c.teamsCheckpoint
	if checkpoint == nil {
		return errTeamsProtectionHeld
	}
	if err := teamsCheckpointReconcile(ctx, checkpoint.writer, checkpoint.store, checkpoint.name, checkpoint.scope, checkpoint.guardKey); err != nil {
		return err
	}
	_, host, err := teamsCheckpointLoad(checkpoint.store, checkpoint.name)
	if err != nil || host.Scope != checkpoint.scope || host.Phase != "current" || host.Current == nil || host.Current.Instance != teamsCheckpointInstance(d.ID, d.Instance) {
		return errTeamsProtectionHeld
	}
	return nil
}

var errTeamsPilotPending = errors.New("source protection needs reconciliation")
