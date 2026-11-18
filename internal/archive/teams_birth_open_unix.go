//go:build darwin || linux

package archive

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
)

// teamsOpenManagedCatalog verifies host binding before any catalog initializer
// or background task. found=false leaves legacy Open behavior untouched.
func teamsOpenManagedCatalog(ctx context.Context, cfg Config) (*Catalog, bool, error) {
	if !cfg.Library {
		return nil, false, nil
	}
	descriptor, descriptorErr := readLibraryDescriptor(cfg)
	managedMarker := descriptorErr == nil && descriptor.ManagedBirth
	if managedMarker {
		if cfg.Application == "" {
			return nil, true, errTeamsProtectionContinuity
		}
		parent, e := filepath.EvalSymlinks(filepath.Dir(filepath.Dir(cfg.Path)))
		if e != nil {
			return nil, true, e
		}
		path := filepath.Join(parent, filepath.Base(filepath.Dir(cfg.Path)))
		store, e := teamsCredentialStore(cfg)
		if e != nil {
			return nil, true, e
		}
		_, r, e := teamsReadBirthReservation(store, teamsBirthReservationName(installationID(cfg.Application), path))
		if e != nil || r.Phase != "committed" {
			return nil, true, errTeamsProtectionContinuity
		}
		actual, e := filepath.EvalSymlinks(cfg.CatalogPath)
		if e != nil || actual != filepath.Join(path, "catalog", "catalog.sqlite3") {
			return nil, true, errTeamsProtectionContinuity
		}
		if e = teamsVerifyBirthLocation(path, r); e != nil {
			return nil, true, e
		}
	}
	// Legacy catalogs keep their ordinary startup even if host credential
	// storage is unavailable. This probe is read-only and does not Initialize.
	db, e := sql.Open("sqlite", readerDSN(cfg.CatalogPath))
	if e != nil {
		return nil, false, nil
	}
	var version string
	e = db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&version)
	_ = db.Close()
	if e != nil {
		// A damaged bound library must not fall back to the legacy opener.
		if managedMarker || teamsHasRetainedBirth(cfg) {
			return nil, true, errTeamsProtectionContinuity
		}
		return nil, false, nil
	}
	parent, e := filepath.EvalSymlinks(filepath.Dir(filepath.Dir(cfg.Path)))
	if e != nil {
		if managedMarker {
			return nil, true, e
		}
		return nil, false, nil
	}
	path := filepath.Join(parent, filepath.Base(filepath.Dir(cfg.Path)))
	if cfg.Application == "" {
		if managedMarker {
			return nil, true, errTeamsProtectionContinuity
		}
		return nil, false, nil
	}
	store, e := teamsCredentialStore(cfg)
	if e != nil {
		if managedMarker {
			return nil, true, e
		}
		return nil, false, nil
	}
	appID := installationID(cfg.Application)
	bound, r, e := teamsReadBirthReservation(store, teamsBirthReservationName(appID, path))
	if version != "35" {
		// A retained birth record prevents a restored older image from being
		// silently opened as ordinary data. A reserved unrelated path is inert.
		if managedMarker {
			return nil, true, errTeamsProtectionContinuity
		}
		if e == nil && (r.Phase == "committed" || r.Phase == "image_bound" || r.Phase == "verified") && teamsVerifyBirthLocation(path, r) == nil {
			return nil, true, errTeamsProtectionContinuity
		}
		return nil, false, nil
	}
	if errors.Is(e, os.ErrNotExist) || (e == nil && (r.Phase == "reserved" || r.Phase == "abandoned")) {
		if managedMarker {
			return nil, true, errTeamsProtectionContinuity
		}
		// Existing upgrades remain on their old startup path. A fresh birth
		// has a second, scope-indexed witness even if its path changed.
		if descriptorErr == nil {
			scope := teamsCheckpointScope(appID, descriptor.ID)
			if _, witnessErr := store.Load(teamsBirthIdentityName(scope)); witnessErr == nil {
				return nil, true, errTeamsProtectionContinuity
			} else if !errors.Is(witnessErr, os.ErrNotExist) {
				return nil, false, nil
			}
		}
		return nil, false, nil
	}

	if e != nil {
		if !managedMarker {
			return nil, false, nil
		}
		return nil, true, e
	}
	witness, e := store.Load(teamsBirthIdentityName(r.Scope))
	if e != nil || string(witness) != teamsBirthReservationName(appID, path) {
		return nil, true, errTeamsProtectionContinuity
	}
	actual, e := filepath.EvalSymlinks(cfg.CatalogPath)
	if e != nil || actual != filepath.Join(path, "catalog", "catalog.sqlite3") {
		return nil, true, errTeamsProtectionContinuity
	}
	host, err := teamsBirthHardwareUUID()
	if err != nil || host == "" || r.Phase != "committed" || r.Path != path || r.Application != appID || r.Host != host || r.UID != os.Geteuid() || r.Volume != libraryVolumeIdentity(parent) {
		return nil, true, errTeamsProtectionContinuity
	}
	if err = teamsVerifyBirthLocation(path, r); err != nil {
		return nil, true, err
	}
	d, err := readLibraryDescriptor(cfg)
	if err != nil || !d.ManagedBirth || d.ID != r.LibraryID || d.Instance != r.InstanceID {
		return nil, true, errTeamsProtectionContinuity
	}
	// Only birth checkpoint writes use the power-loss durable host lane.
	store, err = teamsBirthCredentialStore(cfg)
	if err != nil {
		return nil, true, err
	}
	// mode=rw prevents accidentally replacing a missing catalog with an empty DB.
	dsn := catalogDSN(cfg.CatalogPath) + "&mode=rw"
	catalog, err := openCatalogWithDSN(cfg.CatalogPath, dsn, func(c *Catalog) error {
		// Held writes do not advance the retained checkpoint today. Park a
		// managed birth until a held-state writer can prevent rollback.
		held, e := c.configureTeamsCheckpointWithStore(ctx, cfg, d, store)
		if e != nil {
			return e
		}
		if held {
			_ = c.teamsCheckpoint.writer.db.Close()
			clear(c.teamsCheckpoint.guardKey)
			c.teamsCheckpoint = nil
			return errTeamsProtectionContinuity
		}
		c.teamsCheckpoint.writer.validateBinding = func() error {
			current, _, e := teamsReadBirthReservation(store, teamsBirthReservationName(appID, path))
			if e != nil || string(current) != string(bound) || r.Volume != libraryVolumeIdentity(parent) {
				return errTeamsProtectionContinuity
			}
			currentDescriptor, e := readLibraryDescriptor(cfg)
			if e != nil || !currentDescriptor.ManagedBirth || currentDescriptor.ID != r.LibraryID || currentDescriptor.Instance != r.InstanceID {
				return errTeamsProtectionContinuity
			}
			return teamsVerifyBirthLocation(path, r)
		}
		return nil
	})
	return catalog, true, err
}

// Ignore unavailable birth storage for legacy catalogs, but recognize an
// existing bound birth even when SQLite cannot read its schema.
func teamsHasRetainedBirth(cfg Config) bool {
	if cfg.Application == "" {
		return false
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(filepath.Dir(cfg.Path)))
	if err != nil {
		return false
	}
	path := filepath.Join(parent, filepath.Base(filepath.Dir(cfg.Path)))
	store, err := teamsCredentialStore(cfg)
	if err != nil {
		return false
	}
	_, r, err := teamsReadBirthReservation(store, teamsBirthReservationName(installationID(cfg.Application), path))
	return err == nil && (r.Phase == "committed" || r.Phase == "image_bound" || r.Phase == "verified")
}
