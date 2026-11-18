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
		if version == "35" || managedMarker {
			return nil, true, e
		}
		return nil, false, nil
	}
	path := filepath.Join(parent, filepath.Base(filepath.Dir(cfg.Path)))
	if cfg.Application == "" {
		if version == "35" || managedMarker {
			return nil, true, errTeamsProtectionContinuity
		}
		return nil, false, nil
	}
	store, e := teamsCredentialStore(cfg)
	if e != nil {
		if version == "35" || managedMarker {
			return nil, true, e
		}
		return nil, false, nil
	}
	appID := installationID(cfg.Application)
	bound, r, e := teamsReadBirthReservation(store, teamsBirthReservationName(appID, path))
	if version != "35" {
		// A retained birth record prevents a restored older image from being
		// silently opened as ordinary data. A reserved unrelated path is inert.
		if managedMarker || (e == nil && (r.Phase == "committed" || r.Phase == "image_bound")) {
			return nil, true, errTeamsProtectionContinuity
		}
		return nil, false, nil
	}
	if errors.Is(e, os.ErrNotExist) || (e == nil && (r.Phase == "reserved" || r.Phase == "abandoned")) {
		if managedMarker {
			return nil, true, errTeamsProtectionContinuity
		}
		// Existing authenticated v35 upgrades have their own host checkpoint;
		// verify that continuity without inventing a birth record.
		d, e := readLibraryDescriptor(cfg)
		if e != nil {
			return nil, true, e
		}
		catalog, e := openCatalogWithDSN(cfg.CatalogPath, catalogDSN(cfg.CatalogPath)+"&mode=rw", func(c *Catalog) error { _, err := c.configureTeamsCheckpoint(ctx, cfg, d); return err })
		return catalog, true, e
	}
	if e != nil {
		return nil, true, e
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
	// mode=rw prevents accidentally replacing a missing catalog with an empty DB.
	dsn := catalogDSN(cfg.CatalogPath) + "&mode=rw"
	catalog, err := openCatalogWithDSN(cfg.CatalogPath, dsn, func(c *Catalog) error {
		// A verified held checkpoint still permits the guarded local lane;
		// proof effects remain held by the existing checkpoint policy.
		_, e := c.configureTeamsCheckpoint(ctx, cfg, d)
		if e != nil {
			return e
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
	return err == nil && (r.Phase == "committed" || r.Phase == "image_bound")
}
