package archive

import (
	"context"
	"database/sql"
	"errors"
	"os"

	"github.com/gbdubs/pharos/internal/platform/filelock"
)

var errTeamsSharingBusy = errors.New("A Teams send is being reconciled. Try the change again shortly.")

// teamsSharingLease is a cross-process ordering barrier for the same library.
// The winner of this lease linearizes first: a privacy, classifier, settings,
// grant or draft mutation, or the final send attempt. SQLite transactions stay
// short; the send holds only this file lease over its bounded HTTP attempt.
// TransferLibrary already holds the exclusive library-use lease, while the
// running service holds it shared; recovery creates a new instance identity.
func (c *Catalog) teamsSharingLease() (*os.File, error) {
	lock, err := fileLease(canonical(c.Path)+".teams-sharing.lock", true)
	if errors.Is(err, filelock.ErrContended) {
		return nil, errTeamsSharingBusy
	}
	return lock, err
}

// Source settings can change which previously observed units are eligible.
// Bump before touching files so a failed or interrupted mutation invalidates
// an already prepared snapshot too. The caller holds the lease until the
// in-memory settings have been reloaded.
func (c *Catalog) beginTeamsSourceMutation(ctx context.Context) (*os.File, error) {
	lease, err := c.teamsSharingLease()
	if err != nil {
		return nil, err
	}
	err = c.writeTransaction(ctx, "teams-source-settings", func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `UPDATE teams_source_revision SET revision=revision+1 WHERE id=1`)
		return e
	})
	if err != nil {
		lease.Close()
		return nil, err
	}
	return lease, nil
}
