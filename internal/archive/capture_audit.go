package archive

import (
	"context"
	"database/sql"
	"path/filepath"
)

// Bypass derivation fingerprints only for a freshly confirmed mismatch. Keep
// copy authority and evidence-version guards, including for historical copies;
// repair must never promote older evidence or discard another Mac's messages.
func (c *Catalog) reindexAuditRecord(ctx context.Context, unit syncUnit, record WorkspaceRecord) (string, error) {
	if err := c.queries.wait(ctx); err != nil {
		return "", err
	}
	var id string
	err := c.writeTransaction(ctx, "reindex verified mismatch", func(tx *sql.Tx) error {
		if _, _, err := ingestCopyModeContext(ctx, tx, record, false, unit.hostID(), unit.Source.Name, true, true, repositoryOptions{c.RepositoryAliases, c.RepositorySeparate}); err != nil {
			return err
		}
		var err error
		id, err = storedWorkspaceID(tx, record)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT OR IGNORE INTO workspace_library_dirty(workspace_id) VALUES(?)", id); err != nil {
			return err
		}
		return markSyncDeferred(tx)
	})
	return id, err
}

// A full reference comparison deliberately accepts corrections at the same
// source version. Its isolated authority must not hide that difference. The
// production catalog's copy evidence and versions are never changed here.
func (c *Catalog) compareRetainedAuditRecord(ctx context.Context, unit syncUnit, record WorkspaceRecord, dir string, budget int64) ([]string, error) {
	if err := c.queries.wait(ctx); err != nil {
		return nil, err
	}
	seed, err := snapshotAuditScope(ctx, c, record, budget)
	if err != nil {
		return nil, err
	}
	reference, err := newCatalog(filepath.Join(dir, "reference.sqlite3"))
	if err != nil {
		return nil, err
	}
	defer reference.Close()
	reference.RepositoryAliases, reference.RepositorySeparate = c.RepositoryAliases, c.RepositorySeparate
	if err = seedAuditScope(ctx, reference, seed); err != nil {
		return nil, err
	}
	tx, err := reference.beginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM copy_evidence WHERE host_id=?", unit.hostID()); err != nil {
		return nil, err
	}
	comparison := record
	comparison.EvidenceIdentity = ""
	comparison.EvidenceRevision = ""
	if _, _, err = ingestCopyModeContext(ctx, tx, comparison, false, unit.hostID(), unit.Source.Name, unit.Reader != nil && captureViewOf(unit.Reader) != nil, true, repositoryOptions{c.RepositoryAliases, c.RepositorySeparate}); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	expected, err := snapshotAuditScope(ctx, reference, record, budget)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	var differences []string
	for _, category := range auditCategories {
		if normalizeAuditRows(category, seed[category]) != normalizeAuditRows(category, expected[category]) {
			differences = append(differences, category)
		}
	}
	return differences, nil
}
