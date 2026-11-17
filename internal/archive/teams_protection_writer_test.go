package archive

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestTeamsProtectedWriterIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.sqlite3")
	ordinary, err := sql.Open("sqlite", catalogDSNWithPragmas(path, durableCatalog))
	if err != nil {
		t.Fatal(err)
	}
	defer ordinary.Close()
	if _, err = ordinary.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE parent(id INTEGER PRIMARY KEY); CREATE TABLE protected(id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parent(id) ON DELETE CASCADE, value TEXT); CREATE TABLE unrelated(id INTEGER PRIMARY KEY); INSERT INTO parent VALUES(1); INSERT INTO protected VALUES(1,1,'before')`); err != nil {
		t.Fatal(err)
	}
	// Prepare before installation: an already-open old connection must not pass.
	old, err := ordinary.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	stmt, err := old.PrepareContext(context.Background(), `UPDATE protected SET value='old' WHERE id=1`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	if _, err = ordinary.Exec(`CREATE TRIGGER protected_insert BEFORE INSERT ON protected BEGIN SELECT pharos_teams_protected_writer_v1(); END; CREATE TRIGGER protected_update BEFORE UPDATE ON protected BEGIN SELECT pharos_teams_protected_writer_v1(); END; CREATE TRIGGER protected_delete BEFORE DELETE ON protected BEGIN SELECT pharos_teams_protected_writer_v1(); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = stmt.Exec(); err == nil {
		t.Fatal("old prepared writer passed fence")
	}
	for _, q := range []string{`INSERT INTO protected VALUES(2,1,'x')`, `UPDATE protected SET value='x'`, `DELETE FROM protected`, `INSERT OR REPLACE INTO protected VALUES(1,1,'replace')`, `DELETE FROM parent WHERE id=1`} {
		if _, err = ordinary.Exec(q); err == nil {
			t.Fatalf("ordinary writer passed fence: %s", q)
		}
	}
	if _, err = ordinary.Exec(`INSERT INTO unrelated VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	privileged, err := teamsOpenProtectedWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	defer privileged.db.Close()
	tx, err := teamsBeginProtectedWrite(context.Background(), privileged)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE protected SET value='unauthorized' WHERE id=1`); err == nil {
		t.Fatal("private lane granted authority outside protocol")
	}
	// Simulate the future protocol while the immediate writer is held.
	privileged.authorized.Store(true)
	defer privileged.authorized.Store(false)
	if _, err = tx.Exec(`UPDATE protected SET value='after' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var value string
	if err = ordinary.QueryRow(`SELECT value FROM protected WHERE id=1`).Scan(&value); err != nil || value != "after" {
		t.Fatalf("protected write: %q %v", value, err)
	}
}

func TestTeamsProtectedWriterRequiresDurabilityAndExclusion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.sqlite3")
	ordinary, err := sql.Open("sqlite", catalogDSNWithPragmas(path, durableCatalog))
	if err != nil {
		t.Fatal(err)
	}
	defer ordinary.Close()
	if _, err = ordinary.Exec(`CREATE TABLE example(id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	first, err := teamsOpenProtectedWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.db.Close()
	if tx, err := teamsBeginProtectedWrite(context.Background(), first); err == nil {
		tx.Rollback()
		t.Fatal("rollback journal accepted")
	}
	if _, err = ordinary.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	tx, err := teamsBeginProtectedWrite(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	second, err := teamsOpenProtectedWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.db.Close()
	if _, err = second.db.Exec(`PRAGMA busy_timeout=0`); err != nil {
		t.Fatal(err)
	}
	if other, err := teamsBeginProtectedWrite(context.Background(), second); err == nil {
		other.Rollback()
		t.Fatal("second writer acquired immediate lock")
	}
}
