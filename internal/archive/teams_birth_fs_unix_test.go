//go:build darwin || linux

package archive

import (
	"crypto/sha256"
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
