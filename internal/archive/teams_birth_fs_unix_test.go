//go:build darwin || linux

package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTeamsBirthStagingExclusive(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "library")
	image := []byte("synthetic image")
	d := LibraryDescriptor{Version: libraryFormatVersion, ID: "fresh", Instance: "instance"}
	stage, err := teamsStageBirth(target, "Library", image, d, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	defer stage.close()
	sum := sha256.Sum256(image)
	r := teamsBirthReservation{ImageSHA256: hex.EncodeToString(sum[:]), DirectoryDevice: uint64(stage.dirStat.Dev), DirectoryInode: stage.dirStat.Ino, CatalogDevice: uint64(stage.catalogStat.Dev), CatalogInode: stage.catalogStat.Ino}
	if err = stage.publish(); err != nil {
		t.Fatal(err)
	}
	if err = teamsVerifyBirthImage(target, r); err != nil {
		t.Fatal(err)
	}
	if _, err = teamsStageBirth(target, "Library", image, d, func(string) string { return "" }); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing destination: %v", err)
	}
	other := filepath.Join(parent, "other")
	competing, err := teamsStageBirth(other, "Other", image, d, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	defer competing.close()
	if err = os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	if err = competing.publish(); !errors.Is(err, os.ErrExist) {
		t.Fatalf("publish replaced destination: %v", err)
	}
	if err = os.WriteFile(filepath.Join(target, "catalog", "catalog.sqlite3-wal"), []byte("unexpected"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = teamsVerifyBirthImage(target, r); err == nil {
		t.Fatal("unexpected sidecar accepted")
	}
}

func TestTeamsCreateManagedLibrary(t *testing.T) {
	useHost(t, "synthetic-managed-birth")
	t.Setenv("PHAROS_SUPPORT_DIR", t.TempDir())
	override(t, &teamsBirthHardwareUUID, func() (string, error) { return "synthetic-hardware", nil })
	parent := t.TempDir()
	target := filepath.Join(parent, "fresh")
	cfg, err := teamsCreateManagedLibrary(context.Background(), filepath.Join(parent, "Synthetic.app"), target, "Fresh")
	if err != nil {
		t.Fatal(err)
	}
	d, err := readLibraryDescriptor(cfg)
	if err != nil || d.ID == "" || d.Instance == "" {
		t.Fatalf("descriptor: %v", err)
	}
	store, err := teamsCredentialStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	appID := installationID(cfg.Application)
	_, reservation, err := teamsReadBirthReservation(store, teamsBirthReservationName(appID, filepath.Dir(cfg.Path)))
	if err != nil || reservation.Phase != "committed" {
		t.Fatalf("reservation: %v", err)
	}
	_, host, err := teamsCheckpointLoad(store, reservation.Scope)
	if err != nil || host.Phase != "current" || *host.Current != *reservation.Tuple {
		t.Fatalf("host checkpoint: %v", err)
	}
	// Model a crash after host finalization and before reservation CAS. The
	// exact installed catalog can finish without minting another identity.
	reservationKey := teamsBirthReservationName(appID, filepath.Dir(cfg.Path))
	committedBytes, _, err := teamsReadBirthReservation(store, reservationKey)
	if err != nil {
		t.Fatal(err)
	}
	interrupted := reservation
	interrupted.Phase = "image_bound"
	interruptedBytes, err := teamsEncodeBirthReservation(interrupted)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Replace(reservationKey, committedBytes, interruptedBytes); err != nil {
		t.Fatal(err)
	}
	if _, err = teamsCreateManagedLibrary(context.Background(), cfg.Application, target, "Fresh"); err != nil {
		t.Fatalf("finalization retry: %v", err)
	}
	catalog, found, err := teamsOpenManagedCatalog(context.Background(), cfg)
	if err != nil || !found {
		t.Fatalf("managed restart: %v", err)
	}
	if catalog.teamsCheckpoint == nil {
		t.Fatal("missing protected serving writer")
	}
	// Replacing the main path while the writer is open must stop a protected
	// write before it changes either the host checkpoint or the replacement.
	oldPath := cfg.CatalogPath + ".moved"
	if err = os.Rename(cfg.CatalogPath, oldPath); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cfg.CatalogPath, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = catalog.writeTransaction(context.Background(), "replacement check", func(*sql.Tx) error { t.Error("replacement callback ran"); return nil }); err == nil {
		t.Fatal("replacement accepted")
	}
	if err = os.Remove(cfg.CatalogPath); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(oldPath, cfg.CatalogPath); err != nil {
		t.Fatal(err)
	}
	if err = catalog.writeTransaction(context.Background(), "synthetic private change", func(tx *sql.Tx) error {
		_, e := tx.Exec(`UPDATE teams_protection_baseline SET generation=generation+1 WHERE id=1`)
		return e
	}); err != nil {
		t.Fatal("protected write", err)
	}
	if err = catalog.Close(); err != nil {
		t.Fatal(err)
	}
	catalog, found, err = teamsOpenManagedCatalog(context.Background(), cfg)
	if err != nil || !found {
		t.Fatalf("restart after protected write: %v", err)
	}
	if err = catalog.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = teamsCreateManagedLibrary(context.Background(), cfg.Application, target, "Fresh"); err != nil {
		t.Fatalf("registration retry: %v", err)
	}
	if err = os.WriteFile(filepath.Join(target, "catalog", "catalog.sqlite3-wal"), []byte("unsafe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(target, "catalog", "catalog.sqlite3-wal")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("other", filepath.Join(target, "catalog", "catalog.sqlite3-wal")); err != nil {
		t.Fatal(err)
	}
	if _, _, err = teamsOpenManagedCatalog(context.Background(), cfg); err == nil {
		t.Fatal("unsafe sidecar accepted")
	}
}
