//go:build darwin || linux

package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// teamsOpenManagedCatalog verifies host binding before any catalog initializer
// or background task. found=false leaves legacy Open behavior untouched.
func teamsOpenManagedCatalog(ctx context.Context, cfg Config) (*Catalog, bool, error) {
	if !cfg.Library || cfg.Application == "" {
		return nil, false, nil
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(filepath.Dir(cfg.Path)))
	if err != nil {
		return nil, false, err
	}
	path := filepath.Join(parent, filepath.Base(filepath.Dir(cfg.Path)))
	store, err := teamsCredentialStore(cfg)
	if err != nil {
		return nil, false, err
	}
	appID := installationID(cfg.Application)
	bound, r, err := teamsReadBirthReservation(store, teamsBirthReservationName(appID, path))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	host, err := teamsBirthHardwareUUID()
	if err != nil || host == "" || r.Phase != "committed" || r.Path != path || r.Application != appID || r.Host != host || r.UID != os.Geteuid() || r.Volume != libraryVolumeIdentity(parent) {
		return nil, true, errTeamsProtectionContinuity
	}
	if err = teamsVerifyBirthLocation(path, r); err != nil {
		return nil, true, err
	}
	d, err := readLibraryDescriptor(cfg)
	if err != nil || d.ID != r.LibraryID || d.Instance != r.InstanceID {
		return nil, true, errTeamsProtectionContinuity
	}
	// mode=rw prevents accidentally replacing a missing catalog with an empty DB.
	dsn := catalogDSN(cfg.CatalogPath) + "&mode=rw"
	catalog, err := openCatalogWithDSN(cfg.CatalogPath, dsn, func(c *Catalog) error {
		held, e := c.configureTeamsCheckpoint(ctx, cfg, d)
		if e != nil {
			return e
		}
		if held {
			return errTeamsProtectionContinuity
		}
		c.teamsCheckpoint.writer.validateBinding = func() error {
			current, _, e := teamsReadBirthReservation(store, teamsBirthReservationName(appID, path))
			if e != nil || string(current) != string(bound) || r.Volume != libraryVolumeIdentity(parent) {
				return errTeamsProtectionContinuity
			}
			currentDescriptor, e := readLibraryDescriptor(cfg)
			if e != nil || currentDescriptor.ID != r.LibraryID || currentDescriptor.Instance != r.InstanceID {
				return errTeamsProtectionContinuity
			}
			return teamsVerifyBirthLocation(path, r)
		}
		return nil
	})
	return catalog, true, err
}
