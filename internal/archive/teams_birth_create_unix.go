//go:build darwin || linux

package archive

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"time"
)

var teamsBirthHardwareUUID = func() (string, error) { return platformUUID([]time.Duration{2 * time.Second}) }

// teamsVerifyFreshSQL checks the exact image on the same private connection
// that will transition it into WAL and reconcile its independent checkpoint.
func teamsVerifyFreshSQL(ctx context.Context, w *teamsProtectedWriter, expected *teamsCheckpointTuple) error {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = teamsVerifyProtectionFence(ctx, tx); err != nil {
		return err
	}
	ok, err := teamsOpenContractMatches(ctx, tx)
	if err != nil || !ok {
		return errTeamsProtectionContinuity
	}
	tuple, err := teamsCheckpointTupleFromSQL(ctx, tx)
	if err != nil || *tuple != *expected {
		return errTeamsProtectionContinuity
	}
	digest, err := teamsProtectionProjection(ctx, tx)
	if err != nil || digest != expected.Digest {
		return errTeamsProtectionContinuity
	}
	rev, err := teamsCheckpointFenceRevision(ctx, tx)
	if err != nil || rev != expected.Revision {
		return errTeamsProtectionContinuity
	}
	var baseline string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM teams_protection_baseline WHERE id=1`).Scan(&baseline); err != nil || baseline != "held" {
		return errTeamsProtectionContinuity
	}
	return nil
}

func teamsCreateManagedLibrary(ctx context.Context, application, path, name string) (Config, error) {
	if application == "" {
		return Config{}, errTeamsProtectionContinuity
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Config{}, err
	}
	// Resolve the existing parent before reserving: the leaf itself must not exist.
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return Config{}, err
	}
	absolute = filepath.Join(parent, filepath.Base(absolute))
	appID := installationID(application)
	hardware, err := teamsBirthHardwareUUID()
	if err != nil || hardware == "" {
		return Config{}, errTeamsProtectionContinuity
	}
	cfg := Config{Application: application, Library: true, Path: filepath.Join(absolute, libraryConfigName), CatalogPath: filepath.Join(absolute, "catalog", "catalog.sqlite3"), ArchiveRoot: filepath.Join(absolute, "preserved"), StagingRoot: filepath.Join(absolute, "staging")}
	store, err := teamsCredentialStore(cfg)
	if err != nil {
		return Config{}, err
	}
	reservationName := teamsBirthReservationName(appID, absolute)
	raw, record, err := teamsReadBirthReservation(store, reservationName)
	if errors.Is(err, os.ErrNotExist) {
		lib, e := teamsCheckpointRandom()
		if e != nil {
			return Config{}, e
		}
		instance, e := teamsCheckpointRandom()
		if e != nil {
			return Config{}, e
		}
		record = teamsBirthReservation{Version: 1, Phase: "reserved", Scope: teamsCheckpointScope(appID, lib), Application: appID, Path: absolute, Host: hardware, UID: os.Geteuid(), Volume: libraryVolumeIdentity(parent), LibraryID: lib, InstanceID: instance}
		raw, e = teamsEncodeBirthReservation(record)
		if e != nil {
			return Config{}, e
		}
		if e = store.Save(reservationName, raw); e != nil {
			return Config{}, e
		}
		image, tuple, e := teamsBuildFreshImage(ctx, lib, instance)
		if e != nil {
			return Config{}, e
		}
		d := LibraryDescriptor{Version: libraryFormatVersion, ID: lib, Instance: instance}
		stage, e := teamsStageBirth(absolute, name, image, d, libraryVolumeIdentity)
		if e != nil {
			return Config{}, e
		}
		defer stage.close()
		sum := sha256.Sum256(image)
		next := record
		next.Phase = "image_bound"
		next.ImageSHA256 = hex.EncodeToString(sum[:])
		next.Tuple = tuple
		next.DirectoryDevice = uint64(stage.dirStat.Dev)
		next.DirectoryInode = stage.dirStat.Ino
		next.CatalogDevice = uint64(stage.catalogStat.Dev)
		next.CatalogInode = stage.catalogStat.Ino
		raw, e = teamsAdvanceBirthReservation(store, reservationName, raw, record, next)
		if e != nil {
			return Config{}, e
		}
		record = next
		key := make([]byte, 32)
		if _, e = rand.Read(key); e != nil {
			return Config{}, e
		}
		defer clear(key)
		if e = store.Save(teamsCheckpointKeyName(record.Scope), key); e != nil {
			return Config{}, e
		}
		keyID, e := teamsCheckpointKeyID(key)
		if e != nil {
			return Config{}, e
		}
		challenge, e := teamsCheckpointRandom()
		if e != nil {
			return Config{}, e
		}
		predecessor := sha256.Sum256(raw)
		pending := teamsCheckpointRecord{Version: 1, Scope: record.Scope, KeyID: keyID, Sequence: 1, Phase: "pending", PreviousUnactivated: hex.EncodeToString(predecessor[:]), Next: record.Tuple, Operation: challenge, Transition: "no_tightening", Cover: []teamsCheckpointGuardCover{}}
		encoded, e := teamsEncodeCheckpoint(pending)
		if e != nil {
			return Config{}, e
		}
		if e = store.Save(record.Scope, encoded); e != nil {
			return Config{}, e
		}
		if e = stage.publish(); e != nil {
			return Config{}, e
		}
	} else if err != nil {
		return Config{}, err
	}
	if record.Phase == "committed" {
		config, e := LoadConfig(cfg.Path)
		if e != nil {
			return Config{}, e
		}
		config.Application = application
		catalog, found, e := teamsOpenManagedCatalog(ctx, config)
		if e != nil || !found {
			if e != nil {
				return Config{}, e
			}
			return Config{}, errTeamsProtectionContinuity
		}
		if e = catalog.Close(); e != nil {
			return Config{}, e
		}
		return config, nil
	}
	if record.Application != appID || record.Path != absolute || record.Host != hardware || record.UID != os.Geteuid() || record.Volume != libraryVolumeIdentity(parent) || record.Phase != "image_bound" {
		return Config{}, errTeamsProtectionContinuity
	}
	// Only the exact pending successor or its already-finalized host record can
	// recover a published birth. Never take the general predecessor recovery.
	_, hostRecord, err := teamsCheckpointLoad(store, record.Scope)
	if err != nil {
		return Config{}, err
	}
	predecessor := sha256.Sum256(raw)
	if hostRecord.Scope != record.Scope || (hostRecord.Phase == "pending" && (hostRecord.Previous != nil || hostRecord.PreviousUnactivated != hex.EncodeToString(predecessor[:]) || hostRecord.Next == nil || *hostRecord.Next != *record.Tuple)) || (hostRecord.Phase == "current" && (hostRecord.Current == nil || *hostRecord.Current != *record.Tuple)) || (hostRecord.Phase != "pending" && hostRecord.Phase != "current") {
		return Config{}, errTeamsProtectionContinuity
	}
	if err = teamsVerifyBirthImage(absolute, record); err != nil {
		// A crash after enabling WAL can leave a valid SQLite sidecar. The
		// independently bound file identities and SQL tuple must still match.
		if e := teamsVerifyBirthLocation(absolute, record); e != nil {
			return Config{}, err
		}
		if hostRecord.Phase == "pending" {
			if _, e := os.Lstat(cfg.CatalogPath + "-wal"); e != nil {
				return Config{}, err
			}
		}
	}
	key, err := store.Load(teamsCheckpointKeyName(record.Scope))
	if err != nil || len(key) != 32 {
		return Config{}, errTeamsProtectionContinuity
	}
	defer clear(key)
	w, err := teamsOpenProtectedWriter(cfg.CatalogPath)
	if err != nil {
		return Config{}, err
	}
	defer w.db.Close()
	if err = teamsVerifyFreshSQL(ctx, w, record.Tuple); err != nil {
		return Config{}, err
	}
	var mode string
	if err = w.db.QueryRowContext(ctx, `PRAGMA journal_mode=WAL`).Scan(&mode); err != nil || mode != "wal" {
		return Config{}, errTeamsProtectionContinuity
	}
	w.requireFence = true
	w.expectedInstance = teamsCheckpointInstance(record.LibraryID, record.InstanceID)
	if err = teamsCheckpointReconcile(ctx, w, store, record.Scope, record.Scope, key); err != nil {
		return Config{}, err
	}
	next := record
	next.Phase = "committed"
	if _, err = teamsAdvanceBirthReservation(store, reservationName, raw, record, next); err != nil {
		return Config{}, err
	}
	config, err := LoadConfig(cfg.Path)
	if err != nil {
		return Config{}, err
	}
	config.Application = application
	return config, nil
}
