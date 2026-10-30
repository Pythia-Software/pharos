package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// This list is the history interchange boundary. Everything else is either
// private user/application state or a rebuildable projection. The retained
// transfer catalog also preserves conflicting revisions as evidence.
var transferHistoryTables = []string{
	"repositories", "hosts", "machines", "host_machines", "pull_requests", "workspaces", "workspace_delivery_inputs", "work_items", "conversations", "origin_observations", "messages",
	"agent_sessions", "agent_session_usage", "agent_session_messages", "message_authorship", "message_authorship_spans", "reasoning_extraction_state", "conversation_launches", "conversation_identity_links", "model_requests", "tool_calls", "tool_commands", "tool_urls",
	"skill_usages", "conversation_instructions", "task_attempts", "handoffs", "change_sets", "change_files",
	"work_pr_links", "metrics", "metric_ledger", "workspace_sightings", "conversation_sightings", "copy_versions", "copy_evidence", "source_evidence",
	"declared_native_aliases", "conversation_group_membership", "conversation_parent_refs", "ask_native_sessions", "protections", "activity_events", "receipts", "mcp_calls",
}

type transferFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type libraryTransferReceipt struct {
	Version       int            `json:"version"`
	ID            string         `json:"id"`
	Source        string         `json:"source"`
	Target        string         `json:"target"`
	SourceDigest  string         `json:"source_digest"`
	CatalogDigest string         `json:"catalog_digest"`
	Files         []transferFile `json:"files"`
	State         string         `json:"state"`
	Move          bool           `json:"move"`
}

var libraryTransferFault func(string) error

func transferCheckpoint(stage string) error {
	if libraryTransferFault != nil {
		return libraryTransferFault(stage)
	}
	return nil
}

func quotedIdentifier(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

type sqlReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func historyTables(ctx context.Context, db sqlReader) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tables := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		if slices.Contains(transferHistoryTables, name) || strings.HasPrefix(name, "tl1_") {
			tables = append(tables, name)
		}
	}
	return tables, rows.Err()
}

func historyDigest(ctx context.Context, db sqlReader) (string, error) {
	tables, err := historyTables(ctx, db)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	for _, table := range tables {
		// Opening on another Mac records a new observer. Host discovery is kept
		// on both sides and must not invalidate an otherwise frozen history.
		if table == "hosts" {
			continue
		}
		if err = encoder.Encode(table); err != nil {
			return "", err
		}
		fields, err := transferColumns(ctx, db, table)
		if err != nil {
			return "", err
		}
		sort.SliceStable(fields, func(i, j int) bool {
			if fields[i].pk == 0 {
				return false
			}
			if fields[j].pk == 0 {
				return true
			}
			return fields[i].pk < fields[j].pk
		})
		order := []string{}
		for _, field := range fields {
			if field.pk > 0 {
				order = append(order, quotedIdentifier(field.name))
			}
		}
		if len(order) == 0 {
			for _, field := range fields {
				order = append(order, quotedIdentifier(field.name))
			}
		}
		rows, err := db.QueryContext(ctx, "SELECT * FROM "+quotedIdentifier(table)+" ORDER BY "+strings.Join(order, ","))
		if err != nil {
			return "", err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return "", err
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				break
			}
			err = encoder.Encode(values)
			if err != nil {
				break
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// SQLite's deterministic row ordering here is independent of physical pages.
// History tables without rowids use their declared compound primary key.
func historyOrder(table string) string {
	switch table {
	case "agent_session_usage":
		return "agent_session_id,usage_hour,model,effort,attribution"
	case "work_pr_links":
		return "workspace_id,pr_id,relationship"
	case "workspace_sightings":
		return "workspace_id,host_id,source_name"
	case "conversation_sightings":
		return "conversation_id,host_id,origin"
	case "copy_versions":
		return "host_id,workspace_id,conversation_id,origin"
	case "declared_native_aliases":
		return "conversation_id,account,native_id"
	case "ask_native_sessions":
		return "provider,native_id"
	default:
		return "1,2" // first columns include each remaining table's primary identity
	}
}

func fileDigest(path string) (string, int64, error) {
	return fileDigestContext(context.Background(), path)
}

type transferContextReader struct {
	ctx context.Context
	io.Reader
}

func (r transferContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func fileDigestContext(ctx context.Context, path string) (string, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, transferContextReader{ctx: ctx, Reader: f})
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// Leave room for SQLite's snapshot/VACUUM or merge journal as well as the
// ordinary free-space margin. Payload copies perform their own checks; this
// check is repeated after copying so evidence cannot consume the SQL workspace.
func ensureCatalogWorkingSpace(destination string, paths ...string) error {
	var bytes int64
	for _, path := range paths {
		for _, file := range []string{path, path + "-wal"} {
			info, err := os.Stat(file)
			if os.IsNotExist(err) && file != path {
				continue
			}
			if err != nil {
				return err
			}
			bytes += info.Size()
		}
	}
	return ensureFree(nearestExisting(destination), 2*bytes)
}

func copyVerified(ctx context.Context, source, target string) (transferFile, error) {
	var result transferFile
	if err := ctx.Err(); err != nil {
		return result, err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() {
		return result, fmt.Errorf("refusing non-regular retained file %s", source)
	}
	if err = ensureFree(filepath.Dir(target), info.Size()); err != nil {
		return result, err
	}
	if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return result, err
	}
	in, err := os.Open(source)
	if err != nil {
		return result, err
	}
	defer in.Close()
	out, err := os.OpenFile(target+".partial", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return result, err
	}
	hash := sha256.New()
	buffer := make([]byte, 1<<20)
	for {
		if err = ctx.Err(); err != nil {
			break
		}
		var n int
		n, err = in.Read(buffer)
		if n > 0 {
			if _, e := out.Write(buffer[:n]); e != nil {
				err = e
				break
			}
			_, _ = hash.Write(buffer[:n])
		}
		if errors.Is(err, io.EOF) {
			err = nil
			break
		}
		if err != nil {
			break
		}
	}
	if err == nil {
		err = out.Sync()
	}
	if e := out.Close(); err == nil {
		err = e
	}
	if err != nil {
		return result, err
	}
	if err = os.Rename(target+".partial", target); err != nil {
		return result, err
	}
	if err = os.Chtimes(target, info.ModTime(), info.ModTime()); err != nil {
		return result, err
	}
	digest, size, err := fileDigest(target)
	if err != nil {
		return result, err
	}
	if digest != hex.EncodeToString(hash.Sum(nil)) || size != info.Size() {
		return result, fmt.Errorf("verification failed for %s", source)
	}
	if err = syncFile(target); err != nil {
		return result, err
	}
	if err = syncDirectory(filepath.Dir(target)); err != nil {
		return result, err
	}
	return transferFile{SHA256: digest, Size: size}, nil
}

func retainedTree(ctx context.Context, source, target, label string, skipImports bool) ([]transferFile, error) {
	files := []transferFile{}
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return files, nil
	}
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("retained evidence contains a symbolic link at %s; resolve it before migration", path)
		}
		if entry.IsDir() {
			if filepath.Base(filepath.Dir(path)) == ".imports" && strings.HasPrefix(entry.Name(), ".stage-") {
				return filepath.SkipDir
			}
			if skipImports && entry.Name() == ".imports" {
				return filepath.SkipDir
			}
			// Leases (§6.5) describe running processes, not library data, and
			// temporaries (reader leases, a compression in progress) are not
			// evidence: local backup leaves both out too.
			if entry.Name() == leaseDirName || path != source && strings.HasSuffix(entry.Name(), captureTempSuffix) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() == ".delivered.json" || entry.Name() == "indexed.json" || strings.HasSuffix(entry.Name(), ".lock") || strings.HasSuffix(entry.Name(), ".partial") || strings.HasSuffix(entry.Name(), captureTempSuffix) {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, rel)
		if err = os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return err
		}
		file, err := copyVerified(ctx, path, dest)
		if err != nil {
			return err
		}
		file.Path = filepath.ToSlash(filepath.Join(label, rel))
		files = append(files, file)
		return nil
	})
	return files, err
}

func sanitizeHistory(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", catalogDSN(path))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = removePrivateAskHistory(ctx, db); err != nil {
		return err
	}
	tables, err := historyTables(ctx, db)
	if err != nil {
		return err
	}
	keep := map[string]bool{"meta": true}
	for _, name := range tables {
		keep[name] = true
	}
	rows, err := queryMaps(db, `SELECT type,name,sql FROM sqlite_master WHERE type IN ('table','view','trigger') ORDER BY CASE type WHEN 'trigger' THEN 0 WHEN 'view' THEN 1 ELSE 2 END`)
	if err != nil {
		return err
	}
	if _, err = db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	for _, row := range rows {
		kind, name := firstString(row["type"]), firstString(row["name"])
		if kind == "table" {
			continue
		}
		if _, err = db.Exec("DROP " + strings.ToUpper(kind) + " IF EXISTS " + quotedIdentifier(name)); err != nil {
			return err
		}
	}
	// Drop virtual tables before their automatically managed shadow tables.
	for _, row := range rows {
		if strings.HasPrefix(strings.ToUpper(firstString(row["sql"])), "CREATE VIRTUAL TABLE") {
			if _, err = db.Exec("DROP TABLE IF EXISTS " + quotedIdentifier(firstString(row["name"]))); err != nil {
				return err
			}
		}
	}
	remaining, err := queryMaps(db, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return err
	}
	for _, row := range remaining {
		name := firstString(row["name"])
		if !keep[name] {
			if _, err = db.Exec("DROP TABLE " + quotedIdentifier(name)); err != nil {
				return err
			}
		}
	}
	if _, err = db.Exec(`DELETE FROM meta WHERE key<>'schema_version'; VACUUM`); err != nil {
		return err
	}
	return syncFile(path)
}

// Ask's user-visible questions and replies also have searchable mirrors.
// Remove those roots and their provenance before dropping private tables and
// vacuuming the snapshot; no deleted text may survive in the transfer file.
func removePrivateAskHistory(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON;
 CREATE TEMP TABLE private_ask_workspaces AS SELECT w.id FROM workspaces w WHERE w.source_kind='pharos' AND (
 EXISTS(SELECT 1 FROM ask_sessions a WHERE a.id=w.source_id AND a.host_id=w.source_account)
 OR EXISTS(SELECT 1 FROM conversations c WHERE c.workspace_id=w.id AND c.origin='Ask Pharos' AND c.account='pharos:'||w.source_account));
 CREATE TEMP TABLE private_ask_conversations AS SELECT id,provider,account,native_id FROM conversations WHERE workspace_id IN (SELECT id FROM private_ask_workspaces);
 CREATE TEMP TABLE private_ask_messages AS SELECT id FROM messages WHERE conversation_id IN (SELECT id FROM private_ask_conversations);
 DELETE FROM message_authorship_spans WHERE message_id IN (SELECT id FROM private_ask_messages) OR source_message_id IN (SELECT id FROM private_ask_messages) OR source_conversation_id IN (SELECT id FROM private_ask_conversations);
 DELETE FROM message_authorship WHERE workspace_id IN (SELECT id FROM private_ask_workspaces) OR conversation_id IN (SELECT id FROM private_ask_conversations);
 DELETE FROM conversation_launches WHERE child_id IN (SELECT id FROM private_ask_conversations) OR parent_id IN (SELECT id FROM private_ask_conversations);
 DELETE FROM conversation_parent_refs WHERE (provider,account,native_id) IN (SELECT provider,account,native_id FROM private_ask_conversations);
 DELETE FROM protections WHERE (scope_type='workspace' AND scope_id IN (SELECT id FROM private_ask_workspaces)) OR (scope_type='conversation' AND scope_id IN (SELECT id FROM private_ask_conversations)) OR (scope_type='message' AND scope_id IN (SELECT id FROM private_ask_messages));
 DELETE FROM receipts WHERE workspace_id IN (SELECT id FROM private_ask_workspaces);
 DELETE FROM reclamation WHERE workspace_id IN (SELECT id FROM private_ask_workspaces);
 UPDATE conversations SET parent_id=NULL WHERE parent_id IN (SELECT id FROM private_ask_conversations);
 DELETE FROM workspaces WHERE id IN (SELECT id FROM private_ask_workspaces);`)
	return err
}

type transferColumn struct {
	name    string
	pk      int
	integer bool
}

func transferColumns(ctx context.Context, db sqlReader, table string) ([]transferColumn, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+quotedIdentifier(table)+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := []transferColumn{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var def any
		if err = rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
			return nil, err
		}
		columns = append(columns, transferColumn{name: name, pk: pk, integer: strings.EqualFold(kind, "INTEGER")})
	}
	return columns, rows.Err()
}

// Merge immutable history without transferring private settings. Conflicting
// source values remain in the verified, retained transfer catalog as evidence.
// Foreign keys in legacy catalogs can use different local IDs for the same
// native entity. Map those IDs before any insert, rather than treating an
// ignored unique conflict as a successful import.
var transferNaturalKeys = map[string][]string{
	"workspaces":     {"source_kind", "source_account", "source_id"},
	"work_items":     {"source_kind", "source_account", "source_id"},
	"conversations":  {"provider", "account", "native_id"},
	"messages":       {"conversation_id", "native_id"},
	"agent_sessions": {"conversation_id", "native_id"},
	"pull_requests":  {"host", "repository_id", "number"},
	"task_attempts":  {"work_item_id", "source_id"},
}
var transferReferences = map[string]string{
	"workspace_id": "workspaces", "conversation_id": "conversations", "message_id": "messages",
	"repository_id": "repositories", "work_item_id": "work_items", "agent_session_id": "agent_sessions",
	"pr_id": "pull_requests", "attempt_id": "task_attempts", "change_set_id": "change_sets",
	"tool_call_id": "tool_calls", "delegation_message_id": "messages", "call_message_id": "messages",
	"result_message_id": "messages", "child_conversation_id": "conversations",
	"left_id": "conversations", "right_id": "conversations",
	"source_conversation_id": "conversations", "source_message_id": "messages", "work_id": "workspaces", "child_id": "conversations",
}

func mergeHistory(ctx context.Context, target *Catalog, incoming string) error {
	conn, err := target.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "ATTACH DATABASE ? AS delivered", incoming); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "DETACH DATABASE delivered")
	tables, err := historyTables(ctx, conn)
	if err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "PRAGMA defer_foreign_keys=ON"); err != nil {
		return err
	}
	ordered := append([]string(nil), tables...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return slices.Index(transferHistoryTables, ordered[i]) < slices.Index(transferHistoryTables, ordered[j])
	})
	mapped := map[string]bool{}
	ref := func(table, col, expression string) string {
		entity := transferReferences[col]
		if col == "parent_id" && (table == "conversations" || table == "agent_sessions") {
			entity = table
		}
		if table == "conversation_launches" && col == "parent_id" {
			entity = "conversations"
		}
		if mapped[entity] {
			return "COALESCE((SELECT destination FROM map_" + entity + " WHERE source=" + expression + ")," + expression + ")"
		}
		return expression
	}
	// Maps are built in dependency order. Self parent references are mapped once
	// the table's complete map exists.
	for _, table := range ordered {
		keys := transferNaturalKeys[table]
		if len(keys) == 0 {
			continue
		}
		matches := []string{}
		for _, col := range keys {
			matches = append(matches, "d."+quotedIdentifier(col)+" IS "+ref(table, col, "s."+quotedIdentifier(col)))
		}
		query := "CREATE TEMP TABLE map_" + table + " AS SELECT s.id source,COALESCE((SELECT d.id FROM main." + quotedIdentifier(table) + " d WHERE " + strings.Join(matches, " AND ") + " LIMIT 1),s.id) destination FROM delivered." + quotedIdentifier(table) + " s"
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return err
		}
		mapped[table] = true
	}
	for _, table := range ordered {
		columns, err := transferColumns(ctx, tx, table)
		if err != nil {
			return err
		}
		expressions := []string{}
		for _, col := range columns {
			e := "s." + quotedIdentifier(col.name)
			if col.name == "id" && mapped[table] {
				e = "(SELECT destination FROM map_" + table + " WHERE source=s.id)"
			} else {
				e = ref(table, col.name, e)
			}
			expressions = append(expressions, e+" AS "+quotedIdentifier(col.name))
		}
		if _, err = tx.ExecContext(ctx, "CREATE TEMP VIEW incoming_"+table+" AS SELECT "+strings.Join(expressions, ",")+" FROM delivered."+quotedIdentifier(table)+" s"); err != nil {
			return err
		}
	}
	// Freeze version eligibility before any upsert advances an installation or
	// copy version. Workspace metadata can advance at its last owned revision
	// without a transcript payload; replacing metrics still requires the complete
	// conversation set. A later non-writer sighting cannot advance owned metadata.
	_, err = tx.ExecContext(ctx, `CREATE TEMP TABLE delivered_older(id TEXT PRIMARY KEY);
 INSERT INTO delivered_older SELECT s.id FROM incoming_conversations s JOIN conversations d ON d.id=s.id
 WHERE COALESCE(s.origin_host_id,'')=COALESCE(d.origin_host_id,'') AND COALESCE(s.origin,'')=COALESCE(d.origin,'') AND
 COALESCE((SELECT MAX(version) FROM incoming_copy_versions v WHERE v.conversation_id=s.id AND v.host_id=s.origin_host_id),0)<
 COALESCE((SELECT MAX(version) FROM copy_versions v WHERE v.conversation_id=d.id AND v.host_id=d.origin_host_id),0);
 INSERT OR IGNORE INTO delivered_older
 SELECT v.conversation_id FROM incoming_copy_versions v JOIN copy_evidence e
 ON e.host_id=v.host_id AND e.workspace_id=v.workspace_id AND e.conversation_id=v.conversation_id AND e.origin=v.origin
 WHERE v.conversation_id<>'' AND v.version=e.version AND NOT EXISTS(
 SELECT 1 FROM incoming_copy_evidence s WHERE s.host_id=v.host_id AND s.workspace_id=v.workspace_id
 AND s.conversation_id=v.conversation_id AND s.origin=v.origin AND s.version=v.version AND s.identity=e.identity);
 CREATE TEMP TABLE delivered_conversations(id TEXT PRIMARY KEY);
 INSERT INTO delivered_conversations SELECT s.id FROM incoming_conversations s LEFT JOIN conversations d ON d.id=s.id
 WHERE s.id NOT IN (SELECT id FROM delivered_older) AND (d.id IS NULL OR (
   (COALESCE(s.origin_host_id,'')=COALESCE(d.origin_host_id,'') AND COALESCE(s.origin,'')=COALESCE(d.origin,'') AND
    COALESCE((SELECT MAX(version) FROM incoming_copy_versions v WHERE v.conversation_id=s.id AND v.host_id=s.origin_host_id),0)>
    COALESCE((SELECT MAX(version) FROM copy_versions v WHERE v.conversation_id=d.id AND v.host_id=d.origin_host_id),0))
   OR NOT EXISTS(SELECT 1 FROM messages dm WHERE dm.conversation_id=d.id AND NOT EXISTS(
     SELECT 1 FROM incoming_messages sm WHERE sm.conversation_id=s.id AND sm.native_id=dm.native_id AND sm.content_hash=dm.content_hash))));
 CREATE TEMP TABLE delivered_workspaces(id TEXT PRIMARY KEY);
 INSERT INTO delivered_workspaces SELECT s.id FROM incoming_workspaces s WHERE NOT EXISTS(SELECT 1 FROM workspaces d WHERE d.id=s.id)
 OR (NOT EXISTS(SELECT 1 FROM conversations d WHERE d.workspace_id=s.id AND d.id NOT IN (SELECT id FROM delivered_conversations))
 AND EXISTS(SELECT 1 FROM incoming_copy_versions v JOIN copy_versions d ON d.host_id=v.host_id AND d.workspace_id=v.workspace_id AND d.conversation_id='' AND v.conversation_id='' WHERE v.workspace_id=s.id AND v.version>d.version));
 CREATE TEMP TABLE delivered_workspace_metadata(id TEXT PRIMARY KEY);
 INSERT INTO delivered_workspace_metadata SELECT s.id FROM incoming_workspaces s WHERE s.id IN (SELECT id FROM delivered_workspaces)
 OR (EXISTS(SELECT 1 FROM incoming_workspace_delivery_inputs i JOIN copy_versions d ON d.host_id=i.writer_host_id AND d.workspace_id=i.workspace_id AND d.conversation_id='' WHERE i.workspace_id=s.id AND i.version>d.version)
 AND NOT EXISTS(SELECT 1 FROM incoming_conversations c WHERE c.workspace_id=s.id AND c.id IN (SELECT id FROM delivered_older)));
 CREATE TEMP TABLE delivered_tl1_installations(installation_id TEXT PRIMARY KEY);
 INSERT INTO delivered_tl1_installations SELECT s.installation_id FROM incoming_tl1_installations s LEFT JOIN tl1_installations d USING(installation_id)
 WHERE d.installation_id IS NULL OR COALESCE(s.source_version,0)>COALESCE(d.source_version,0);`)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM messages WHERE conversation_id IN (SELECT id FROM delivered_conversations)
 AND NOT EXISTS(SELECT 1 FROM incoming_messages s WHERE s.conversation_id=messages.conversation_id AND s.native_id=messages.native_id)`); err != nil {
		return err
	}
	// Accepted session state is authoritative, including removals and hourly
	// usage buckets. Replace its membership/usage and prune obsolete derived
	// entities before importing; divergent unions keep their existing facts.
	if _, err = tx.ExecContext(ctx, `DELETE FROM agent_session_messages WHERE agent_session_id IN (SELECT id FROM agent_sessions WHERE conversation_id IN (SELECT id FROM delivered_conversations));
 DELETE FROM agent_session_usage WHERE agent_session_id IN (SELECT id FROM agent_sessions WHERE conversation_id IN (SELECT id FROM delivered_conversations));`); err != nil {
		return err
	}
	for _, table := range []string{"agent_sessions", "skill_usages", "tool_calls", "model_requests"} {
		if _, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE conversation_id IN (SELECT id FROM delivered_conversations) AND id NOT IN (SELECT id FROM incoming_"+table+")"); err != nil {
			return err
		}
	}
	// Integer row IDs are destination-local. Replace per-workspace metrics only
	// when the source owns that revision; union-derived totals are rebuilt below.
	if _, err = tx.ExecContext(ctx, `DELETE FROM metrics WHERE workspace_id IN (SELECT id FROM delivered_workspaces);
 DELETE FROM metric_ledger WHERE workspace_id IN (SELECT id FROM delivered_workspaces);`); err != nil {
		return err
	}
	for _, table := range ordered {
		columns, err := transferColumns(ctx, tx, table)
		if err != nil {
			return err
		}
		names, updates := []string{}, []string{}
		scope := ""
		integerID := false
		primaryColumns := 0
		for _, column := range columns {
			if column.pk > 0 {
				primaryColumns++
			}
		}
		for _, col := range columns {
			if primaryColumns == 1 && col.name == "id" && col.pk == 1 && col.integer {
				integerID = true
				continue
			}
			q := quotedIdentifier(col.name)
			names = append(names, q)
			if col.pk == 0 {
				updates = append(updates, q+"=excluded."+q)
			}
			if col.name == "conversation_id" {
				scope = "excluded.conversation_id IN (SELECT id FROM delivered_conversations)"
			}
		}
		switch table {
		case "conversations":
			scope = "excluded.id IN (SELECT id FROM delivered_conversations)"
		case "workspaces":
			scope = "excluded.id IN (SELECT id FROM delivered_workspace_metadata)"
		case "workspace_delivery_inputs":
			scope = "excluded.workspace_id IN (SELECT id FROM delivered_workspace_metadata)"
		case "copy_versions":
			scope = "excluded.version>copy_versions.version"
		case "copy_evidence":
			scope = "excluded.version>copy_evidence.version"
		case "source_evidence":
			scope = "excluded.version>source_evidence.version"
		case "origin_observations":
			scope = "excluded.conversation_id IN (SELECT id FROM delivered_conversations)"
		}
		if strings.HasPrefix(table, "tl1_") {
			scope = "excluded.installation_id IN (SELECT installation_id FROM delivered_tl1_installations)"
		}
		list := strings.Join(names, ",")
		predicate := "true"
		for _, column := range columns {
			if column.name == "conversation_id" && table != "copy_versions" {
				predicate = "conversation_id NOT IN (SELECT id FROM delivered_older)"
			}
			parent := ""
			switch column.name {
			case "agent_session_id":
				parent = "agent_sessions"
			case "tool_call_id":
				parent = "tool_calls"
			case "message_id":
				parent = "messages"
			}
			if parent != "" {
				predicate += " AND (incoming." + quotedIdentifier(column.name) + " IS NULL OR incoming." + quotedIdentifier(column.name) + " IN (SELECT id FROM incoming_" + parent + " WHERE conversation_id NOT IN (SELECT id FROM delivered_older)))"
			}
		}
		if table == "conversations" {
			predicate = "id NOT IN (SELECT id FROM delivered_older)"
		}
		if table == "workspace_delivery_inputs" {
			predicate = "workspace_id IN (SELECT id FROM delivered_workspace_metadata)"
		}
		if table == "copy_evidence" {
			predicate += " AND EXISTS(SELECT 1 FROM copy_versions v WHERE v.host_id=incoming.host_id AND v.workspace_id=incoming.workspace_id AND v.conversation_id=incoming.conversation_id AND v.origin=incoming.origin AND v.version=incoming.version)"
		}
		if table == "source_evidence" {
			predicate = "source_kind='tl1' AND entity IN (SELECT installation_id FROM delivered_tl1_installations)"
		}
		if table == "conversation_parent_refs" {
			predicate = "EXISTS(SELECT 1 FROM incoming_conversations c WHERE c.provider=incoming.provider AND c.account=incoming.account AND c.native_id=incoming.native_id AND c.id NOT IN (SELECT id FROM delivered_older))"
			scope = "EXISTS(SELECT 1 FROM incoming_conversations c WHERE c.provider=excluded.provider AND c.account=excluded.account AND c.native_id=excluded.native_id AND c.id IN (SELECT id FROM delivered_conversations))"
		}
		if table == "metrics" || table == "metric_ledger" {
			predicate = "workspace_id IN (SELECT id FROM delivered_workspaces)"
		}
		if integerID {
			matches := []string{}
			for _, name := range names {
				matches = append(matches, "existing."+name+" IS incoming."+name)
			}
			predicate += " AND NOT EXISTS(SELECT 1 FROM " + quotedIdentifier(table) + " existing WHERE " + strings.Join(matches, " AND ") + ")"
		}
		query := "INSERT INTO " + quotedIdentifier(table) + "(" + list + ") SELECT " + list + " FROM incoming_" + table + " incoming WHERE " + predicate + " ON CONFLICT DO "
		if scope != "" && len(updates) > 0 {
			query += "UPDATE SET " + strings.Join(updates, ",") + " WHERE " + scope
		} else {
			query += "NOTHING"
		}
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("merge history table %s: %w", table, err)
		}
	}
	// Advancing an unassociated legacy copy must clear the prior association,
	// just as ordinary ingestion does; a stale raw identity cannot guard it.
	if _, err = tx.ExecContext(ctx, `DELETE FROM copy_evidence WHERE NOT EXISTS(
 SELECT 1 FROM copy_versions v WHERE v.host_id=copy_evidence.host_id AND v.workspace_id=copy_evidence.workspace_id
 AND v.conversation_id=copy_evidence.conversation_id AND v.origin=copy_evidence.origin AND v.version=copy_evidence.version);
 DELETE FROM source_evidence WHERE source_kind='tl1' AND entity IN (SELECT installation_id FROM delivered_tl1_installations)
 AND NOT EXISTS(SELECT 1 FROM incoming_source_evidence s WHERE s.host_id=source_evidence.host_id
 AND s.source_kind=source_evidence.source_kind AND s.source_account=source_evidence.source_account AND s.entity=source_evidence.entity);`); err != nil {
		return err
	}
	// Divergent copies can add missing messages, but cannot prune or rewrite the
	// other copy. The existing merged writer marker prevents later source pruning.
	if _, err = tx.ExecContext(ctx, `UPDATE conversations SET origin_host_id='merged' WHERE id IN (SELECT id FROM incoming_conversations) AND id NOT IN (SELECT id FROM delivered_conversations) AND id NOT IN (SELECT id FROM delivered_older);
 UPDATE conversations SET parent_id=(SELECT p.id FROM conversation_parent_refs r JOIN conversations p ON p.provider=r.provider AND p.account=r.account AND p.native_id=r.parent_native_id WHERE r.provider=conversations.provider AND r.account=conversations.account AND r.native_id=conversations.native_id)
 WHERE parent_id IS NULL AND EXISTS(SELECT 1 FROM conversation_parent_refs r JOIN conversations p ON p.provider=r.provider AND p.account=r.account AND p.native_id=r.parent_native_id WHERE r.provider=conversations.provider AND r.account=conversations.account AND r.native_id=conversations.native_id AND p.id<>conversations.id);
 INSERT INTO workspace_library_dirty(workspace_id) SELECT id FROM workspaces WHERE true ON CONFLICT DO NOTHING;`); err != nil {
		return err
	}
	rows, err := queryMaps(tx, "SELECT id FROM incoming_workspaces")
	if err != nil {
		return err
	}
	// Accepted copies already carry reconciled sessions and ledgers. Only a
	// divergent union needs those facts rebuilt from its combined messages.
	conversationRows, err := queryMaps(tx, "SELECT id FROM incoming_conversations WHERE id NOT IN (SELECT id FROM delivered_older) AND id NOT IN (SELECT id FROM delivered_conversations)")
	if err != nil {
		return err
	}
	merged := map[string]bool{}
	for _, row := range conversationRows {
		merged[firstString(row["id"])] = true
	}
	for _, row := range rows {
		if err = rederiveWorkspace(tx, firstString(row["id"]), merged); err != nil {
			return fmt.Errorf("rebuild imported workspace: %w", err)
		}
	}
	messages, err := queryMaps(tx, "SELECT id FROM incoming_messages")
	if err != nil {
		return err
	}
	for _, row := range messages {
		if err = replaceMessageFTS(tx, firstString(row["id"])); err != nil {
			return err
		}
	}
	// Check referential coverage before accepting a single missing entity.
	violations, err := queryMaps(tx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	if len(violations) > 0 {
		return fmt.Errorf("history import has unresolved references; source retained: %v", violations[0])
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return target.refreshAllLibrary(ctx)
}

func pendingDeliveryPath(config Config) string {
	return filepath.Join(filepath.Dir(descriptorPath(config)), "deliveries", "pending.json")
}

func readTransferReceipt(path string) (libraryTransferReceipt, error) {
	var r libraryTransferReceipt
	b, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(b, &r)
	}
	if err == nil && (r.Version != 1 || r.ID == "" || !validCaptureName(r.ID)) {
		err = fmt.Errorf("unsupported delivery receipt")
	}
	return r, err
}

func verifyTransfer(target Config, r libraryTransferReceipt) error {
	d, err := readLibraryDescriptor(target)
	if err != nil {
		return err
	}
	if d.Instance != r.Target {
		return fmt.Errorf("destination was restored or replaced; source cleanup is paused")
	}
	dir := filepath.Join(target.CaptureRoot, ".imports", r.ID)
	proof, err := readTransferReceipt(filepath.Join(dir, "transfer.json"))
	if err != nil {
		return err
	}
	if (proof.State != "verified" && proof.State != "complete") || proof.SourceDigest != r.SourceDigest || proof.Target != r.Target || proof.CatalogDigest != r.CatalogDigest {
		return fmt.Errorf("destination has no matching verified delivery")
	}
	return verifyTransferPayload(dir, r)
}

func verifyTransferPayload(dir string, r libraryTransferReceipt) error {
	return verifyTransferPayloadContext(context.Background(), dir, r)
}

func verifyTransferPayloadContext(ctx context.Context, dir string, r libraryTransferReceipt) error {
	digest, _, err := fileDigestContext(ctx, filepath.Join(dir, "history.sqlite3"))
	if err != nil || digest != r.CatalogDigest {
		return fmt.Errorf("destination history snapshot failed verification")
	}
	for _, f := range r.Files {
		if !filepath.IsLocal(f.Path) {
			return fmt.Errorf("unsafe delivery inventory")
		}
		digest, size, e := fileDigestContext(ctx, filepath.Join(dir, filepath.FromSlash(f.Path)))
		if e != nil || digest != f.SHA256 || size != f.Size {
			return fmt.Errorf("destination evidence failed verification: %s", f.Path)
		}
	}
	return nil
}

// Transfers walk both retained trees and write a catalog, configuration and
// delivery journal. Check cross-library containment before creating any output,
// including symlink aliases of existing ancestors and hard-linked catalogs.
func libraryStorageOverlaps(source, target Config) bool {
	if sameFile(source.CatalogPath, target.CatalogPath) {
		return true
	}
	paths := func(c Config) []string {
		return []string{c.CatalogPath, c.CaptureRoot, c.ArchiveRoot, c.Path, descriptorPath(c)}
	}
	for _, a := range paths(source) {
		if a == "" {
			continue
		}
		for _, b := range paths(target) {
			if b == "" {
				continue
			}
			a, b := canonical(a), canonical(b)
			if within(a, b) || within(b, a) {
				return true
			}
		}
	}
	return false
}

func TransferLibrary(ctx context.Context, source, target Config, move bool, progress func(string)) (libraryTransferReceipt, error) {
	var receipt libraryTransferReceipt
	if libraryStorageOverlaps(source, target) {
		return receipt, fmt.Errorf("source and destination libraries must have separate storage")
	}
	if err := source.checkLibrary(libraryVolumeIdentity); err != nil {
		return receipt, err
	}
	if err := target.checkLibrary(libraryVolumeIdentity); err != nil {
		return receipt, err
	}
	if err := checkCatalogCompatibility(source.CatalogPath); err != nil {
		return receipt, err
	}
	if err := checkCatalogCompatibility(target.CatalogPath); err != nil {
		return receipt, err
	}
	paths := []string{libraryUseLock(source), libraryUseLock(target)}
	sort.Strings(paths)
	locks := []*os.File{}
	defer func() {
		for _, lock := range locks {
			lock.Close()
		}
	}()
	for _, path := range paths {
		lock, err := fileLease(path, true)
		if err != nil {
			return receipt, err
		}
		locks = append(locks, lock)
	}
	a, err := ensureLibraryDescriptor(source, "")
	if err != nil {
		return receipt, err
	}
	b, err := ensureLibraryDescriptor(target, "")
	if err != nil {
		return receipt, err
	}
	if a.Instance == b.Instance {
		return receipt, fmt.Errorf("source and destination have the same writable identity; recover the copy first")
	}
	report := func(s string) {
		if progress != nil {
			progress(s)
		}
	}
	// Resume before taking a new snapshot: the source database may already have
	// been emptied in an earlier run whose evidence cleanup was interrupted.
	if pending, e := readTransferReceipt(pendingDeliveryPath(source)); e == nil {
		if pending.Source != a.Instance {
			return receipt, fmt.Errorf("source recovery invalidated its delivery journal")
		}
		db, e := openReadOnlySQLite(source.CatalogPath)
		if e != nil {
			return receipt, e
		}
		var applied string
		_ = db.QueryRow("SELECT value FROM meta WHERE key='delivery_cleanup'").Scan(&applied)
		current, e := historyDigest(ctx, db)
		db.Close()
		if e != nil {
			return receipt, e
		}
		if applied == pending.ID || (pending.Target == b.Instance && move && current == pending.SourceDigest && verifySourceEvidence(source, pending) == nil) {
			if !move || pending.Target != b.Instance {
				return receipt, fmt.Errorf("source cleanup already started; resume to the original destination")
			}
			if err = verifyTransfer(target, pending); err != nil {
				return pending, err
			}
			report("Resuming verified source cleanup")
			if err = cleanupDeliveredHistory(ctx, source, pending); err != nil {
				return pending, err
			}
			pending.State = "complete"
			return pending, durableJSON(filepath.Join(target.CaptureRoot, ".imports", pending.ID, "transfer.json"), pending)
		}
		// No source deletion has begun. Keep the older acceptance receipt and
		// take a fresh inventory covering the newly arrived revisions. This also
		// permits an explicitly selected new recovery destination when the entire
		// source is still present; automatic delivery never changes its target.
		pending.State = "superseded"
		if err = durableJSON(filepath.Join(filepath.Dir(pendingDeliveryPath(source)), pending.ID+".json"), pending); err != nil {
			return receipt, err
		}
		if err = os.Remove(pendingDeliveryPath(source)); err != nil {
			return receipt, err
		}
		if err = syncDirectory(filepath.Dir(pendingDeliveryPath(source))); err != nil {
			return receipt, err
		}
		report("Source changed; taking a fresh delivery inventory")
	} else if !errors.Is(e, os.ErrNotExist) {
		return receipt, e
	}
	report("Snapshotting archived history")
	// Reuse the catalog's guarded, resumable migrations before inventorying its
	// columns. A compatible older library need not be opened in a window first.
	prepared, err := OpenCatalog(source.CatalogPath)
	if err != nil {
		return receipt, fmt.Errorf("prepare source library: %w", err)
	}
	prepared.setCaptureRoot(source.CaptureRoot)
	if _, e := os.Stat(source.CaptureRoot); e == nil {
		targets, e := captureTargets(source.CaptureRoot, nil, true, nil)
		if e != nil {
			prepared.Close()
			return receipt, e
		}
		for _, target := range targets {
			if result := prepared.IndexCapture(ctx, target, nil); result.Error != nil {
				prepared.Close()
				return receipt, fmt.Errorf("index retained source history before transfer: %v", result.Error)
			}
		}
	} else if !os.IsNotExist(e) {
		prepared.Close()
		return receipt, e
	}
	if err = prepared.Close(); err != nil {
		return receipt, err
	}
	if err = ensureCatalogWorkingSpace(target.CaptureRoot, source.CatalogPath); err != nil {
		return receipt, err
	}
	if err = os.MkdirAll(filepath.Join(target.CaptureRoot, ".imports"), 0o700); err != nil {
		return receipt, err
	}
	dir, err := os.MkdirTemp(filepath.Join(target.CaptureRoot, ".imports"), ".stage-")
	if err != nil {
		return receipt, err
	}
	// Failed staging is safe to discard: it has never authorized source cleanup.
	defer func() {
		if dir != "" {
			_ = os.RemoveAll(dir)
		}
	}()
	path := filepath.Join(dir, "history.sqlite3")
	if err = snapshotSQLite(ctx, source.CatalogPath, path); err != nil {
		return receipt, err
	}
	snapshot, err := sql.Open("sqlite", catalogDSN(path))
	if err != nil {
		return receipt, err
	}
	digest, err := historyDigest(ctx, snapshot)
	snapshot.Close()
	if err != nil {
		return receipt, err
	}
	receipt = libraryTransferReceipt{Version: 1, Source: a.Instance, Target: b.Instance, SourceDigest: digest, Move: move, State: "copying", Files: []transferFile{}}
	if err = sanitizeHistory(ctx, path); err != nil {
		return receipt, fmt.Errorf("prepare transfer history: %w", err)
	}
	receipt.CatalogDigest, _, err = fileDigest(path)
	if err != nil {
		return receipt, err
	}
	report("Copying retained evidence")
	for _, tree := range []struct{ source, label string }{{source.CaptureRoot, "captures"}, {source.ArchiveRoot, "preserved"}} {
		if tree.source == "" {
			continue
		}
		files, e := retainedTree(ctx, tree.source, filepath.Join(dir, tree.label), tree.label, false)
		if e != nil {
			return receipt, e
		}
		receipt.Files = append(receipt.Files, files...)
	}
	if err = validateStagedCaptureEvidence(ctx, filepath.Join(dir, "captures")); err != nil {
		return receipt, fmt.Errorf("verify retained source evidence: %w", err)
	}
	// Content-addressed transfer IDs make retries and repeated copies reuse the
	// same evidence. Prior imports retain their own proof inside this inventory.
	receipt.ID = hashBytes([]byte(a.Instance + "\x00" + b.Instance + "\x00" + digest + "\x00" + jsonText(receipt.Files)))
	final := filepath.Join(target.CaptureRoot, ".imports", receipt.ID)
	if _, e := os.Stat(final); e == nil {
		old, e := readTransferReceipt(filepath.Join(final, "transfer.json"))
		if e != nil {
			return receipt, e
		}
		if old.SourceDigest != receipt.SourceDigest || old.Target != receipt.Target || old.Source != receipt.Source {
			return receipt, fmt.Errorf("transfer identity does not match its inventory")
		}
		if e = verifyTransferPayload(final, old); e != nil {
			return receipt, e
		}
		receipt.CatalogDigest = old.CatalogDigest
		if err = os.RemoveAll(dir); err != nil {
			return receipt, err
		}
		dir = ""
	} else if !errors.Is(e, os.ErrNotExist) {
		return receipt, e
	} else {
		// Publish the inventory first; incomplete imports are never evidence roots.
		if err = durableJSON(filepath.Join(dir, "transfer.json"), receipt); err != nil {
			return receipt, err
		}
		if err = flushTree(dir); err != nil {
			return receipt, err
		}
		if err = os.Rename(dir, final); err != nil {
			return receipt, err
		}
		dir = ""
		if err = syncDirectory(filepath.Dir(final)); err != nil {
			return receipt, err
		}
	}
	path = filepath.Join(final, "history.sqlite3")
	report("Reconciling archived history")
	if err = ensureCatalogWorkingSpace(filepath.Dir(target.CatalogPath), path, target.CatalogPath); err != nil {
		return receipt, err
	}
	catalog, err := OpenCatalog(target.CatalogPath)
	if err != nil {
		return receipt, fmt.Errorf("open destination library: %w", err)
	}
	catalog.setCaptureRoot(target.CaptureRoot)
	if err = mergeHistory(ctx, catalog, path); err != nil {
		catalog.Close()
		return receipt, fmt.Errorf("merge destination history: %w", err)
	}
	if err = catalog.Close(); err != nil {
		return receipt, err
	}
	report("Verifying destination acceptance")
	receipt.State = "verified"
	if err = durableJSON(filepath.Join(final, "transfer.json"), receipt); err != nil {
		return receipt, err
	}
	if err = verifyTransfer(target, receipt); err != nil {
		return receipt, err
	}
	if move {
		report("Removing verified source history")
		if err = durableJSON(pendingDeliveryPath(source), receipt); err != nil {
			return receipt, err
		}
		if err = cleanupDeliveredHistory(ctx, source, receipt); err != nil {
			return receipt, err
		}
	}
	receipt.State = "complete"
	return receipt, durableJSON(filepath.Join(final, "transfer.json"), receipt)
}

// Only user state and small capture checkpoints survive a move. Building a
// fresh catalog avoids leaving transcripts in FTS, rollups, derived findings,
// query caches, free SQLite pages, or a retained WAL.
var moveRetainedTables = []string{"ui_preferences", "query_table_query_snapshots", "query_table_computed_columns", "sync_settings", "background_settings", "automatic_source_states",
	"cloud_destinations", "cloud_backup_runs", "cloud_backup_schedules", "library_size_history",
	"ask_settings", "ask_sessions", "ask_visits", "ask_native_sessions", "finding_actions", "finding_cart", "finding_cart_clones", "finding_interventions", "finding_settings",
	"source_states", "source_record_states", "source_item_states", "source_terminal_states", "sync_generations", "hosts", "protections", "repository_retirements", "library_delivery_checkpoints", "library_workspace_delivery_checkpoints"}

func cleanupDeliveredHistory(ctx context.Context, source Config, receipt libraryTransferReceipt) error {
	catalog, err := OpenCatalog(source.CatalogPath)
	if err != nil {
		return err
	}
	var applied string
	_ = catalog.DB.QueryRow("SELECT value FROM meta WHERE key='delivery_cleanup'").Scan(&applied)
	if applied != receipt.ID {
		digest, e := historyDigest(ctx, catalog.DB)
		if e != nil {
			catalog.Close()
			return e
		}
		if digest != receipt.SourceDigest {
			catalog.Close()
			return fmt.Errorf("source changed during delivery; no source history was removed")
		}
		if err = verifySourceEvidence(source, receipt); err != nil {
			catalog.Close()
			return err
		}
		cleanPath := source.CatalogPath + ".delivery-clean"
		if err = ensureCatalogWorkingSpace(filepath.Dir(cleanPath), source.CatalogPath); err != nil {
			catalog.Close()
			return err
		}
		_ = os.Remove(cleanPath)
		_ = os.Remove(cleanPath + "-wal")
		_ = os.Remove(cleanPath + "-shm")
		clean, e := newCatalog(cleanPath)
		if e != nil {
			catalog.Close()
			return e
		}
		e = clean.writeTransaction(ctx, "retain-library-preferences", func(tx *sql.Tx) error {
			if _, e := tx.ExecContext(ctx, "ATTACH DATABASE ? AS prior", source.CatalogPath); e != nil {
				return e
			}
			for _, table := range moveRetainedTables {
				var exists int
				if e := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM prior.sqlite_master WHERE type='table' AND name=?", table).Scan(&exists); e != nil {
					return e
				}
				if exists == 0 {
					continue
				}
				columns, e := transferColumns(ctx, tx, table)
				if e != nil {
					return e
				}
				names := make([]string, len(columns))
				for i, column := range columns {
					names[i] = quotedIdentifier(column.name)
				}
				list := strings.Join(names, ",")
				if _, e := tx.ExecContext(ctx, "INSERT OR REPLACE INTO "+quotedIdentifier(table)+"("+list+") SELECT "+list+" FROM prior."+quotedIdentifier(table)); e != nil {
					return e
				}
			}
			if _, e := tx.ExecContext(ctx, `INSERT OR REPLACE INTO meta(key,value) SELECT key,value FROM prior.meta WHERE key IN ('mcp_enabled','repository_renames',?)`, legacyHostKey); e != nil {
				return e
			}
			if e := retainDeliveryCheckpoints(ctx, tx, receipt); e != nil {
				return e
			}
			_, e := tx.ExecContext(ctx, "INSERT OR REPLACE INTO meta(key,value) VALUES('delivery_cleanup',?)", receipt.ID)
			return e
		})
		if closeErr := clean.Close(); e == nil {
			e = closeErr
		}
		if checkpointErr := catalog.Checkpoint(); e == nil {
			e = checkpointErr
		}
		if closeErr := catalog.Close(); e == nil {
			e = closeErr
		}
		if e != nil {
			return e
		}
		if err = syncFile(cleanPath); err != nil {
			return err
		}
		if err = os.Rename(cleanPath, source.CatalogPath); err != nil {
			return err
		}
		if err = syncDirectory(filepath.Dir(source.CatalogPath)); err != nil {
			return err
		}
		if err = transferCheckpoint("source-catalog-emptied"); err != nil {
			return err
		}
	} else {
		catalog.Close()
	}
	if err = cleanupDeliveredEvidence(source, receipt); err != nil {
		return err
	}
	receipt.State = "complete"
	if err = durableJSON(filepath.Join(filepath.Dir(pendingDeliveryPath(source)), receipt.ID+".json"), receipt); err != nil {
		return err
	}
	if err = os.Remove(pendingDeliveryPath(source)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(filepath.Dir(pendingDeliveryPath(source)))
}

func sourceEvidencePath(source Config, relative string) (string, error) {
	if !filepath.IsLocal(relative) {
		return "", fmt.Errorf("unsafe source inventory")
	}
	parts := strings.SplitN(filepath.ToSlash(relative), "/", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid source inventory")
	}
	root := ""
	switch parts[0] {
	case "captures":
		root = source.CaptureRoot
	case "preserved":
		root = source.ArchiveRoot
	default:
		return "", fmt.Errorf("invalid evidence root")
	}
	path := filepath.Join(root, filepath.FromSlash(parts[1]))
	if !within(canonical(path), canonical(root)) {
		return "", fmt.Errorf("source evidence is a symbolic link")
	}
	return path, nil
}
func verifySourceEvidence(source Config, r libraryTransferReceipt) error {
	for _, file := range r.Files {
		path, err := sourceEvidencePath(source, file.Path)
		if err != nil {
			return err
		}
		digest, size, err := fileDigest(path)
		if err != nil || digest != file.SHA256 || size != file.Size {
			return fmt.Errorf("source evidence changed; cleanup paused: %s", file.Path)
		}
	}
	return nil
}
func cleanupDeliveredEvidence(source Config, r libraryTransferReceipt) error {
	// Preserve only the small no-recapture fingerprints outside payload trees.
	for _, file := range r.Files {
		path, err := sourceEvidencePath(source, file.Path)
		if err != nil {
			return err
		}
		if filepath.Base(path) == captureManifestName && !strings.Contains(file.Path, "/.imports/") {
			manifest, err := loadCaptureManifest(filepath.Dir(path))
			if err == nil && manifest.DeliveredFingerprint != "" {
				if err = durableJSON(filepath.Join(filepath.Dir(path), ".delivered.json"), map[string]string{"fingerprint": manifest.DeliveredFingerprint, "destination_instance": r.Target}); err != nil {
					return err
				}
			}
		}
	}
	// Delete payloads first, manifests last. A crash leaves the durable pending
	// receipt in place and prevents capture/index from using a partial inventory.
	files := append([]transferFile(nil), r.Files...)
	touched := map[string]bool{}
	sort.SliceStable(files, func(i, j int) bool {
		return filepath.Base(files[i].Path) != captureManifestName && filepath.Base(files[j].Path) == captureManifestName
	})
	for _, file := range files {
		path, err := sourceEvidencePath(source, file.Path)
		if err != nil {
			return err
		}
		digest, size, err := fileDigest(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if digest != file.SHA256 || size != file.Size {
			return fmt.Errorf("source payload changed during cleanup; retained %s", path)
		}
		if err = os.Remove(path); err != nil {
			return err
		}
		touched[filepath.Dir(path)] = true
	}
	// Index markers are derived history too. Empty directories and delivery
	// fingerprints can remain; neither contains conversation content.
	err := filepath.WalkDir(source.CaptureRoot, func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() == indexMarkerName {
			touched[filepath.Dir(path)] = true
			return os.Remove(path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for dir := range touched {
		if err = syncDirectory(dir); err != nil {
			return err
		}
	}
	return nil
}

func runLibraryTransferCLI(args []string) error {
	f := flag.NewFlagSet("library-transfer", flag.ContinueOnError)
	from := f.String("from", "", "source configuration")
	to := f.String("to", "", "destination configuration")
	move := f.Bool("move", false, "remove source history only after durable verification")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *from == "" || *to == "" {
		return fmt.Errorf("usage: pharos library-transfer --from CONFIG --to CONFIG [--move]")
	}
	a, err := LoadConfig(*from)
	if err != nil {
		return err
	}
	b, err := LoadConfig(*to)
	if err != nil {
		return err
	}
	variant := "copy"
	if *move {
		variant = "move"
	}
	// Leased beside both catalogs and in the app's lease directory, so each
	// library's service and the broker see it (§6.5).
	dirs := []string{leaseDirs(a)[0], leaseDirs(b)[0], appLeaseDir()}
	return runCLIOperationIn(dirs, []string{a.Path, b.Path}, "library-transfer", OperationSpec{Kind: "library.transfer", Variant: variant}, func(ctx context.Context, op *Op) error {
		receipt, err := TransferLibrary(ctx, a, b, *move, func(s string) { fmt.Fprintln(os.Stderr, s); op.Step(s, "", -1) })
		if err != nil {
			return err
		}
		return printJSON(receipt)
	})
}
