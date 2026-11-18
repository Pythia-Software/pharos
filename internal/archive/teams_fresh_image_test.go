package archive

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestTeamsFreshImage(t *testing.T) {
	useHost(t, "synthetic-fresh-image")
	t.Setenv("PHAROS_SUPPORT_DIR", t.TempDir())
	ctx := context.Background()
	image, tuple, err := teamsBuildFreshImage(ctx, "fresh-library", "fresh-instance")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "catalog.sqlite")
	if err = os.WriteFile(path, image, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = teamsVerifyProtectionFence(ctx, tx); err != nil {
		t.Fatal(err)
	}
	ok, err := teamsOpenContractMatches(ctx, tx)
	if err != nil || !ok {
		t.Fatalf("contract: %v", err)
	}
	actual, err := teamsCheckpointTupleFromSQL(ctx, tx)
	if err != nil || *actual != *tuple {
		t.Fatalf("checkpoint: %v", err)
	}
	digest, err := teamsProtectionProjection(ctx, tx)
	if err != nil || digest != tuple.Digest {
		t.Fatalf("projection: %v", err)
	}
	var state string
	if err = tx.QueryRowContext(ctx, "SELECT state FROM teams_protection_baseline WHERE id=1").Scan(&state); err != nil || state != "held" {
		t.Fatalf("baseline %q: %v", state, err)
	}
}

func TestTeamsBirthReservationTransitions(t *testing.T) {
	store := &teamsCheckpointTestStore{}
	r := teamsBirthReservation{Version: 1, Phase: "reserved", Application: "synthetic-app", Path: filepath.Join(t.TempDir(), "library"), Host: "synthetic-host", UID: 1, LibraryID: "library", InstanceID: "instance"}
	r.Scope = teamsCheckpointScope(r.Application, r.LibraryID)
	name := teamsBirthReservationName(r.Application, r.Path)
	raw, err := teamsEncodeBirthReservation(r)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(name, raw); err != nil {
		t.Fatal(err)
	}
	_, got, err := teamsReadBirthReservation(store, name)
	if err != nil || got.Phase != "reserved" {
		t.Fatal(err)
	}
	next := r
	next.Phase = "image_bound"
	next.ImageSHA256 = string(make([]byte, 64))
	next.Tuple = &teamsCheckpointTuple{Generation: 1, Instance: teamsCheckpointInstance(r.LibraryID, r.InstanceID), Revision: 0, CommitID: "00000000000000000000000000000000", Digest: "0000000000000000000000000000000000000000000000000000000000000000"}
	next.ImageSHA256 = next.Tuple.Digest
	next.DirectoryInode = 1
	next.CatalogInode = 2
	bound, err := teamsAdvanceBirthReservation(store, name, raw, r, next)
	if err != nil {
		t.Fatal(err)
	}
	altered := next
	altered.Phase = "committed"
	altered.DirectoryInode = 1
	altered.CatalogInode = 2
	altered.Path = filepath.Join(t.TempDir(), "other")
	if _, err = teamsAdvanceBirthReservation(store, name, bound, next, altered); err == nil {
		t.Fatal("changed path accepted")
	}
	committed := next
	committed.Phase = "committed"
	committed.DirectoryInode = 1
	committed.CatalogInode = 2
	if _, err = teamsAdvanceBirthReservation(store, name, raw, next, committed); err == nil {
		t.Fatal("stale predecessor accepted")
	}
	if _, err = teamsAdvanceBirthReservation(store, name, bound, next, committed); err != nil {
		t.Fatal(err)
	}
}
