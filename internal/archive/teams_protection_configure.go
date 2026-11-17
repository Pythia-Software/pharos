package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

func teamsCheckpointKeyName(scope string) string {
	sum := sha256.Sum256([]byte("pharos-teams-checkpoint-key-v1\x00" + scope))
	return hex.EncodeToString(sum[:])
}

// configureTeamsCheckpoint is a closed integration seam. It requires explicit
// application and library identity, not an inferred catalog path. The caller
// must invoke it before publishing the Catalog to any goroutine. No production
// startup path invokes it until schema activation and writer coverage are ready.
// A returned held state permits local protected writes but never proof effects.
func (c *Catalog) configureTeamsCheckpoint(ctx context.Context, cfg Config, d LibraryDescriptor) (bool, error) {
	if c.teamsCheckpoint != nil || cfg.Application == "" || d.ID == "" || d.Instance == "" || len(d.ID) > 256 || len(d.Instance) > 256 || cfg.CatalogPath != c.Path {
		return false, errTeamsProtectionContinuity
	}
	scope := teamsCheckpointScope(installationID(cfg.Application), d.ID)
	store, err := teamsCredentialStore(cfg)
	if err != nil {
		return false, err
	}
	key, err := store.Load(teamsCheckpointKeyName(scope))
	if err != nil || len(key) != 32 {
		clear(key)
		return false, errTeamsProtectionContinuity
	}
	w, err := teamsOpenProtectedWriter(c.Path)
	if err != nil {
		clear(key)
		return false, err
	}
	w.requireFence = true
	err = teamsCheckpointReconcile(ctx, w, store, scope, scope, key)
	held := errors.Is(err, errTeamsProtectionHeld)
	if err == nil {
		_, host, e := teamsCheckpointLoad(store, scope)
		if e != nil || host.Phase != "current" || host.Current.Instance != teamsCheckpointInstance(d.ID, d.Instance) {
			err = errTeamsProtectionContinuity
		}
	}
	if err != nil && !held {
		w.db.Close()
		clear(key)
		return false, err
	}
	c.teamsCheckpoint = &teamsCheckpointRuntime{writer: w, store: store, name: scope, scope: scope, guardKey: key}
	return held, nil
}

func (c *Catalog) configureTeamsCheckpointIfActive(ctx context.Context, cfg Config) error {
	active, err := c.hasProtectionCheckpoint()
	if err != nil || !active {
		return err
	}
	d, err := readLibraryDescriptor(cfg)
	if err != nil {
		return err
	}
	_, err = c.configureTeamsCheckpoint(ctx, cfg, d)
	return err
}
