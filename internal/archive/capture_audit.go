package archive

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
)

// Only disposable reference copies use these settings. The library's pools
// continue to use catalogDSN and their full device-flush guarantees.
var auditReferencePragmas = catalogPragmas{synchronous: "OFF"}

var auditReferenceTemplate struct {
	sync.Mutex
	data []byte
}

func openAuditReference(path string) (*Catalog, error) {
	auditReferenceTemplate.Lock()
	if auditReferenceTemplate.data == nil {
		data, err := buildAuditReferenceTemplate()
		if err != nil {
			auditReferenceTemplate.Unlock()
			return nil, err
		}
		auditReferenceTemplate.data = data
	}
	data := auditReferenceTemplate.data
	auditReferenceTemplate.Unlock()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, err
	}
	return openCatalogWithDSN(path, catalogDSNWithPragmas(path, auditReferencePragmas), func(c *Catalog) error {
		return c.migrateHosts()
	})
}

// Initialize the reference schema once per process, then copy its checkpointed
// empty database for each comparison. No state from a comparison is reused.
func buildAuditReferenceTemplate() ([]byte, error) {
	dir, err := os.MkdirTemp("", "pharos-audit-template-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "reference.sqlite3")
	catalog, err := openCatalogWithDSN(path, catalogDSNWithPragmas(path, auditReferencePragmas), (*Catalog).Initialize)
	if err != nil {
		return nil, err
	}
	if err := catalog.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// Upgrade only the selected conversations' outdated derived ledgers. Retained
// source rows and copy evidence stay untouched; the independent comparison
// below must still pass before this input is verified.
func (c *Catalog) upgradeAuditLedgers(ctx context.Context, record WorkspaceRecord) error {
	if err := c.queries.wait(ctx); err != nil {
		return err
	}
	return c.writeTransaction(ctx, "upgrade verification ledgers", func(tx *sql.Tx) error {
		ids, err := pendingAuditLedgers(ctx, tx, record)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := rebuildStoredToolLedger(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func pendingAuditLedgers(ctx context.Context, q *sql.Tx, record WorkspaceRecord) ([]string, error) {
	workspace, err := storedWorkspaceID(q, record)
	if err != nil || workspace == "" {
		return nil, err
	}
	rows, err := queryMapsContext(ctx, q, `SELECT c.id,c.provider,c.account,c.native_id FROM conversations c
		LEFT JOIN tool_ledger_state s ON s.conversation_id=c.id
		WHERE c.workspace_id=? AND (s.conversation_id IS NULL OR s.version<>?)`, workspace, toolLedgerVersion)
	if err != nil {
		return nil, err
	}
	selected := map[usageConversationKey]bool{}
	for _, conversation := range record.Conversations {
		selected[usageConversationIdentity(conversation)] = true
	}
	var ids []string
	for _, row := range rows {
		if selected[usageConversationKey{firstString(row["provider"]), firstString(row["account"]), firstString(row["native_id"])}] {
			ids = append(ids, firstString(row["id"]))
		}
	}
	return ids, nil
}

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
	reference, err := openAuditReference(filepath.Join(dir, "reference.sqlite3"))
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
