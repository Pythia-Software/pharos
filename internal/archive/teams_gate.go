package archive

import (
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
