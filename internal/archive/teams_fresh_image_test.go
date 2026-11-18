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
