package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

const automaticAuditBytes = int64(16 << 20)
const automaticAuditWall = 5 * time.Second

type verificationSummary struct {
	Checked  int `json:"checked"`
	Passed   int `json:"passed"`
	Mismatch int `json:"mismatch"`
	Deferred int `json:"deferred"`
	Errors   int `json:"errors"`
}

type auditResult struct {
	Outcome     string   `json:"outcome"`
	Source      string   `json:"source,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	Detail      string   `json:"detail,omitempty"`
	Differences []string `json:"differences,omitempty"`
	Seconds     float64  `json:"seconds"`
	Discovered  int      `json:"discovered_units"`
	Deferred    int      `json:"deferred_units"`
}

type auditPool struct {
	catalog    *Catalog
	units      []syncUnit
	checks     map[string]string
	discovered int
	deferred   int
	failure    error
}

func newAuditPool(catalog *Catalog) *auditPool {
	pool := &auditPool{catalog: catalog, checks: map[string]string{}}
	rows, err := queryMaps(catalog.DB, "SELECT source_name,unit,checked_at FROM sync_verifications WHERE host_id=? ORDER BY checked_at DESC LIMIT 20000", currentHost().ID)
	pool.failure = err
	for _, row := range rows {
		pool.checks[firstString(row["source_name"])+"/"+firstString(row["unit"])] = firstString(row["checked_at"])
	}
	return pool
}

func (pool *auditPool) consider(unit syncUnit) {
	pool.discovered++
	if unit.Source.Kind != "conductor" && time.Since(time.Unix(0, unit.Version)) < 30*time.Second {
		pool.deferred++
		return
	}
	pool.units = append(pool.units, unit)
	sort.SliceStable(pool.units, func(left, right int) bool {
		first, second := pool.units[left], pool.units[right]
		return pool.checks[first.Source.Name+"/"+first.ID] < pool.checks[second.Source.Name+"/"+second.ID]
	})
	if len(pool.units) > 64 {
		pool.units = pool.units[:64]
	}
}

func (pool *auditPool) preflightSkipped(source SourceConfig) {
	rows, err := queryMaps(pool.catalog.DB, `SELECT p.item,p.size,p.version,COALESCE(p.signal,'') signal FROM source_item_states p
		LEFT JOIN sync_verifications v ON v.host_id=p.host_id AND v.source_name=p.source_name AND v.unit=p.item
		WHERE p.host_id=? AND p.source_name=? ORDER BY COALESCE(v.checked_at,''),p.item LIMIT 64`, currentHost().ID, source.Name)
	if err != nil {
		pool.failure = err
		return
	}
	for _, row := range rows {
		pool.consider(unitOf(source, []sourcePart{{item: firstString(row["item"]), size: integer(row["size"]), version: integer(row["version"]), signal: firstString(row["signal"])}}, false))
	}
}

func (c *Catalog) auditPool(ctx context.Context, pool *auditPool, runID string) auditResult {
	if pool.failure != nil {
		return auditResult{Outcome: "read_error", Detail: pool.failure.Error(), Discovered: pool.discovered, Deferred: pool.deferred}
	}
	if len(pool.units) == 0 {
		return auditResult{Outcome: "deferred", Detail: "no stable eligible unit; actively streaming inputs remain eligible later", Discovered: pool.discovered, Deferred: pool.deferred}
	}
	unit := pool.units[0]
	ctx, cancel := context.WithTimeout(ctx, automaticAuditWall)
	defer cancel()
	result := c.auditUnit(ctx, unit, runID, automaticAuditBytes)
	result.Discovered, result.Deferred = pool.discovered, pool.deferred
	if result.Outcome == "deferred" {
		result.Deferred++
	}
	err := c.recordVerification(unit, result)
	if err != nil {
		result.Outcome, result.Detail = "read_error", err.Error()
	}
	return result
}

func partsSignature(parts []sourcePart) string {
	values := []string{}
	for _, part := range parts {
		values = append(values, fmt.Sprintf("%s:%d:%d:%s", part.item, part.size, part.version, part.signal))
	}
	slices.Sort(values)
	return hashBytes([]byte(strings.Join(values, "\n")))
}

func (c *Catalog) auditUnit(ctx context.Context, unit syncUnit, runID string, budget int64) (result auditResult) {
	started := time.Now()
	result = auditResult{Outcome: "read_error", Source: unit.Source.Name, Unit: unit.ID}
	defer func() {
		result.Seconds = time.Since(started).Seconds()
		if result.Outcome == "read_error" && ctx.Err() != nil {
			result.Outcome = "deferred"
			result.Detail = "verification budget or cancellation"
		}
	}()
	if budget > 0 && (unit.Bytes > budget || (budget == automaticAuditBytes && (len(unit.Parts) > 64 || (unit.Source.Kind == "conductor" && unit.Bytes > 2000)))) {
		result.Outcome, result.Detail = "deferred", "large group queued for manual verification"
		return
	}
	dir, err := os.MkdirTemp(filepath.Dir(c.Path), ".pharos-verification-")
	if err != nil {
		result.Detail = err.Error()
		return
	}
	defer os.RemoveAll(dir)
	record, read, err := freezeAuditRecord(ctx, unit, filepath.Join(dir, "evidence"), budget)
	if err != nil {
		result.Detail = err.Error()
		if errors.Is(err, errAuditUnstable) || ctx.Err() != nil {
			result.Outcome = "deferred"
		}
		return
	}
	if record == nil {
		result.Outcome, result.Detail = "deferred", "the unit has no conversation to compare"
		return
	}
	if partsSignature(read) != partsSignature(unit.Parts) {
		result.Outcome, result.Detail = "deferred", "source version or group membership changed during acquisition"
		return
	}
	seed, err := snapshotAuditScope(ctx, c, *record, budget)
	if err != nil {
		result.Detail = err.Error()
		return
	}
	reference, err := OpenCatalog(filepath.Join(dir, "reference.sqlite3"))
	if err != nil {
		result.Detail = err.Error()
		return
	}
	defer reference.Close()
	reference.RepositoryAliases, reference.RepositorySeparate = c.RepositoryAliases, c.RepositorySeparate
	if err := seedAuditScope(ctx, reference, seed); err != nil {
		result.Detail = err.Error()
		return
	}
	tx, err := reference.beginWrite(ctx)
	if err != nil {
		result.Detail = err.Error()
		return
	}
	defer tx.Rollback()
	if _, _, err := ingestCopyMode(tx, *record, false, unit.hostID(), unit.Source.Name, unit.Reader != nil && captureViewOf(unit.Reader) != nil, true, repositoryOptions{c.RepositoryAliases, c.RepositorySeparate}); err != nil {
		result.Detail = err.Error()
		if errors.Is(err, errOlderCopy) {
			result.Outcome = "deferred"
		}
		return
	}
	if err := tx.Commit(); err != nil {
		result.Detail = err.Error()
		return
	}
	expected, err := snapshotAuditScope(ctx, reference, *record, budget)
	if err != nil {
		result.Detail = err.Error()
		return
	}
	if ctx.Err() != nil {
		result.Outcome, result.Detail = "deferred", "verification budget or cancellation"
		return
	}
	for _, category := range auditCategories {
		if normalizeAuditRows(category, seed[category]) != normalizeAuditRows(category, expected[category]) {
			result.Differences = append(result.Differences, category)
		}
	}
	if len(result.Differences) > 0 {
		result.Outcome = "mismatch"
		result.Detail = "full reference ingest disagrees with retained catalog rows"
		id := stableID("sync-integrity", unit.hostID(), unit.Source.Name, unit.ID)
		detail := map[string]any{"categories": result.Differences, "run_id": runID, "source_signature": partsSignature(read), "index_version": sourceIndexVersion(mustAuditAdapter(unit.Source)), "comparison": "isolated full reference ingest", "source_kind": unit.Source.Kind}
		_, err := c.DB.Exec(`INSERT INTO sync_integrity_issues(id,host_id,source_name,unit,first_at,last_at,state,detail_json)
			VALUES(?,?,?,?,?,?,'open',?) ON CONFLICT(id) DO UPDATE SET last_at=excluded.last_at,state='open',detail_json=excluded.detail_json,occurrences=occurrences+1,resolved_at=NULL`, id, unit.hostID(), unit.Source.Name, unit.ID, now(), now(), jsonText(detail))
		if err != nil {
			result.Outcome, result.Detail = "read_error", err.Error()
		}
	} else {
		result.Outcome = "passed"
		_, err := c.DB.Exec("UPDATE sync_integrity_issues SET state='resolved',resolved_at=? WHERE host_id=? AND source_name=? AND unit=? AND state='open'", now(), unit.hostID(), unit.Source.Name, unit.ID)
		if err != nil {
			result.Outcome, result.Detail = "read_error", err.Error()
		}
	}
	return
}

func mustAuditAdapter(source SourceConfig) Adapter { adapter, _ := MakeAdapter(source); return adapter }

var errAuditUnstable = errors.New("audit source changed during acquisition")

func readCapturedAuditRecord(ctx context.Context, unit syncUnit) (*WorkspaceRecord, []sourcePart, error) {
	view := captureViewOf(unit.Reader)
	adapter, err := MakeAdapter(unit.Source)
	if err != nil {
		return nil, nil, err
	}
	adapter.(interface{ bindCapture(*captureView) }).bindCapture(view)
	setAdapterContext(adapter, ctx)
	signature := func() (string, error) {
		paths := []string{}
		for _, part := range unit.Parts {
			if strings.HasPrefix(part.item, "group:") {
				continue
			}
			original := strings.TrimPrefix(part.item, "sqlite:")
			if separator := strings.LastIndex(original, ":sessions:"); separator >= 0 {
				original = original[:separator]
			}
			path, ok := view.captured(original)
			if !ok {
				return "", errAuditUnstable
			}
			paths = append(paths, path)
		}
		if antigravity, ok := adapter.(*antigravityAdapter); ok {
			paths = append(paths, filepath.Join(unit.Source.Path, antigravitySummaries))
			paths = append(paths, antigravity.runLogs()...)
		}
		values := []string{}
		for _, path := range paths {
			for _, dependency := range []string{path, path + "-wal"} {
				info, err := os.Stat(dependency)
				if errors.Is(err, os.ErrNotExist) {
					values = append(values, dependency+":missing")
					continue
				}
				if err != nil {
					return "", err
				}
				values = append(values, fmt.Sprintf("%s:%d:%d", dependency, info.Size(), info.ModTime().UnixNano()))
			}
		}
		return hashBytes([]byte(jsonText(values))), nil
	}
	before, err := signature()
	if err != nil {
		return nil, nil, err
	}
	var record *WorkspaceRecord
	var read []sourcePart
	err = adapter.(partialAdapter).discoverParts(func(parts []sourcePart) bool {
		return unitOf(unit.Source, parts, false).ID != unit.ID
	}, func(value *WorkspaceRecord, parts []sourcePart) error {
		if record != nil {
			return errors.New("audit selected more than one captured group")
		}
		record, read = value, parts
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	after, err := signature()
	if err != nil || before != after {
		return nil, nil, errAuditUnstable
	}
	return record, read, nil
}

func freezeAuditRecord(ctx context.Context, unit syncUnit, dir string, budget int64) (*WorkspaceRecord, []sourcePart, error) {
	if unit.Reader != nil && captureViewOf(unit.Reader) != nil {
		return readCapturedAuditRecord(ctx, unit)
	}
	if unit.Source.Kind == "conductor" {
		adapter, err := MakeAdapter(unit.Source)
		if err != nil {
			return nil, nil, err
		}
		setAdapterContext(adapter, ctx)
		part := unit.Parts[0]
		separator := strings.LastIndex(part.item, ":sessions:")
		if separator < 0 {
			return nil, nil, errors.New("invalid session identity")
		}
		path := strings.TrimPrefix(part.item[:separator], "sqlite:")
		var record *WorkspaceRecord
		var read []sourcePart
		err = adapter.(*conductorAdapter).database(path, map[string]bool{part.item[separator+len(":sessions:"):]: true}, func([]sourcePart) bool { return false }, func(value *WorkspaceRecord, parts []sourcePart) error { record, read = value, parts; return nil })
		return record, read, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	manifest := &captureManifest{Host: currentHost()}
	view := &captureView{host: currentHost(), dir: dir, manifest: manifest, mappings: []capturePathMapping{{Original: unit.Source.Path, Captured: dir}}}
	if info, err := os.Stat(unit.Source.Path); err == nil && !info.IsDir() {
		view.mappings = []capturePathMapping{{Original: unit.Source.Path, Captured: filepath.Join(dir, filepath.Base(unit.Source.Path))}}
	}
	files := []string{}
	databases := []string{}
	for _, part := range unit.Parts {
		if strings.HasPrefix(part.item, "group:") {
			continue
		}
		if strings.HasPrefix(part.item, "sqlite:") {
			databases = append(databases, strings.TrimPrefix(part.item, "sqlite:"))
		} else {
			files = append(files, part.item)
		}
	}
	if unit.Source.Kind == "antigravity" {
		adapter := &antigravityAdapter{baseAdapter: baseAdapter{config: unit.Source, ctx: ctx}}
		summary := filepath.Join(unit.Source.Path, antigravitySummaries)
		if _, err := os.Stat(summary); err == nil {
			databases = append(databases, summary)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
		ids := map[string]bool{}
		for _, part := range unit.Parts {
			if strings.HasPrefix(part.item, "sqlite:") || strings.HasPrefix(part.item, "group:") {
				continue
			}
			id := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(part.item))))
			ids[id] = true
			annotation := filepath.Join(unit.Source.Path, "annotations", id+".pbtxt")
			if _, err := os.Stat(annotation); err == nil {
				files = append(files, annotation)
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, nil, err
			}
		}
		logs, err := adapter.runLogsFor(ids)
		if err != nil {
			return nil, nil, err
		}
		files = append(files, logs...)
	}
	var bytes int64
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, nil, err
		}
		bytes += info.Size()
		if budget > 0 && bytes > budget {
			return nil, nil, fmt.Errorf("%w: evidence exceeds size budget", errAuditUnstable)
		}
		destination, ok := view.captured(path)
		if !ok {
			return nil, nil, errors.New("audit evidence outside configured source")
		}
		if err := copyStableAuditFile(ctx, path, destination, info); err != nil {
			return nil, nil, err
		}
	}
	for _, path := range databases {
		state, err := sqliteDependencyState(path)
		if err != nil {
			return nil, nil, err
		}
		bytes += state.Size + state.WALSize
		if budget > 0 && bytes > budget {
			return nil, nil, fmt.Errorf("%w: database evidence exceeds size budget", errAuditUnstable)
		}
		destination, ok := view.captured(path)
		if !ok {
			return nil, nil, errors.New("audit dependency outside source")
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return nil, nil, err
		}
		if err := snapshotSQLite(ctx, path, destination); err != nil {
			return nil, nil, err
		}
		after, err := sqliteDependencyState(path)
		if err != nil || state != after {
			return nil, nil, errAuditUnstable
		}
		rel, _ := filepath.Rel(dir, destination)
		manifest.Snapshots = append(manifest.Snapshots, &capturedSnapshot{Path: path, Captured: filepath.ToSlash(rel), Source: state})
	}
	config := unit.Source
	config.Path = dir
	if info, err := os.Stat(unit.Source.Path); err == nil && !info.IsDir() {
		config.Path = filepath.Join(dir, filepath.Base(unit.Source.Path))
		view.mappings = []capturePathMapping{{Original: unit.Source.Path, Captured: config.Path}}
	}
	adapter, err := MakeAdapter(config)
	if err != nil {
		return nil, nil, err
	}
	adapter.(interface{ bindCapture(*captureView) }).bindCapture(view)
	setAdapterContext(adapter, ctx)
	var record *WorkspaceRecord
	var read []sourcePart
	err = adapter.(partialAdapter).discoverParts(func(parts []sourcePart) bool { return unitOf(unit.Source, parts, false).ID != unit.ID }, func(value *WorkspaceRecord, parts []sourcePart) error {
		if record != nil {
			return errors.New("audit selected more than one group")
		}
		record, read = value, parts
		return nil
	})
	for _, path := range files {
		before, err := os.Stat(path)
		if err != nil {
			return nil, nil, err
		}
		copied, ok := view.captured(path)
		if !ok {
			return nil, nil, errAuditUnstable
		}
		after, err := os.Stat(copied)
		if err != nil || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
			return nil, nil, errAuditUnstable
		}
	}
	return record, read, err
}

func copyStableAuditFile(ctx context.Context, path, destination string, info os.FileInfo) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	source, err := os.Open(path)
	if err != nil {
		return err
	}
	defer source.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	buffer := make([]byte, 64<<10)
	remaining := info.Size()
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := io.ReadFull(source, buffer[:min(int64(len(buffer)), remaining)])
		if err != nil {
			return errAuditUnstable
		}
		if _, err := out.Write(buffer[:count]); err != nil {
			return err
		}
		remaining -= int64(count)
	}
	after, err := source.Stat()
	if err != nil || !os.SameFile(info, after) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return errAuditUnstable
	}
	return os.Chtimes(destination, info.ModTime(), info.ModTime())
}

var auditCategories = []string{"workspaces", "conversations", "conversation_group_membership", "messages", "search", "conversation_documents", "agent_sessions", "agent_session_usage", "agent_session_messages", "model_requests", "tool_calls", "tool_commands", "tool_urls", "metrics", "metric_ledger", "summaries"}
var auditSearchWord = regexp.MustCompile(`[\p{L}\p{N}]{3,24}`)

func snapshotAuditScope(ctx context.Context, catalog *Catalog, record WorkspaceRecord, budget int64) (map[string][]map[string]any, error) {
	tx, err := catalog.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id, err := storedWorkspaceID(tx, record)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return map[string][]map[string]any{}, nil
	}
	result := map[string][]map[string]any{}
	tables, err := queryMapsContext(ctx, tx, "SELECT name FROM sqlite_master WHERE type='table' AND sql NOT LIKE 'CREATE VIRTUAL TABLE%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	conditions := map[string]string{
		"workspace_id":     "=?",
		"conversation_id":  " IN (SELECT id FROM conversations WHERE workspace_id=?)",
		"agent_session_id": " IN (SELECT id FROM agent_sessions WHERE workspace_id=?)",
		"session_id":       " IN (SELECT id FROM agent_sessions WHERE workspace_id=?)",
		"message_id":       " IN (SELECT id FROM messages WHERE conversation_id IN (SELECT id FROM conversations WHERE workspace_id=?))",
		"tool_call_id":     " IN (SELECT id FROM tool_calls WHERE workspace_id=?)",
		"work_item_id":     " IN (SELECT id FROM work_items WHERE workspace_id=?)",
		"change_set_id":    " IN (SELECT id FROM change_sets WHERE workspace_id=?)",
	}
	var bytes int64
	for _, table := range tables {
		name := firstString(table["name"])
		if strings.HasPrefix(name, "sqlite_") || strings.HasPrefix(name, "messages_fts") || strings.HasPrefix(name, "messages_trigram") || strings.HasPrefix(name, "sync_") || name == "source_item_states" || name == "source_record_states" || name == "tool_call_cube" || name == "tool_usage_daily" {
			continue
		}
		columns, err := tableColumns(tx, name)
		if err != nil {
			return nil, err
		}
		where, args := "", []any{}
		switch name {
		case "workspaces":
			where = "id=?"
			args = append(args, id)
		case "repositories":
			where = "1=1"
		case "hosts":
			where = "id IN (SELECT host_id FROM workspace_sightings WHERE workspace_id=?)"
			args = append(args, id)
		case "conversation_identity_links":
			where = "left_id IN (SELECT id FROM conversations WHERE workspace_id=?) AND right_id IN (SELECT id FROM conversations WHERE workspace_id=?)"
			args = append(args, id, id)
		default:
			for _, key := range []string{"workspace_id", "conversation_id", "agent_session_id", "session_id", "message_id", "tool_call_id", "work_item_id", "change_set_id"} {
				if columns[key] {
					where = key + conditions[key]
					args = append(args, id)
					break
				}
			}
		}
		if where == "" {
			continue
		}
		rows, err := queryAuditRows(ctx, tx, "SELECT * FROM \""+name+"\" WHERE "+where, budget, &bytes, args...)
		if err != nil {
			return nil, err
		}
		result[name] = rows
	}
	search, err := queryAuditRows(ctx, tx, `SELECT m.id AS message_id,f.message_id AS indexed_message,f.text AS fts_text,r.fts_rowid,CASE WHEN tr.rowid IS NULL THEN 0 ELSE 1 END AS trigram_coverage
		FROM messages m LEFT JOIN message_fts_rows r ON r.message_id=m.id LEFT JOIN messages_fts f ON f.rowid=r.fts_rowid
		LEFT JOIN messages_trigram tr ON tr.rowid=r.fts_rowid WHERE m.conversation_id IN (SELECT id FROM conversations WHERE workspace_id=?) ORDER BY m.id`, budget, &bytes, id)
	if err != nil {
		return nil, err
	}
	samples := 0
	for _, row := range search {
		rowID := row["fts_rowid"]
		delete(row, "fts_rowid")
		word := auditSearchWord.FindString(firstString(row["fts_text"]))
		if rowID == nil || word == "" || samples >= 8 {
			continue
		}
		for _, table := range []string{"messages_fts", "messages_trigram"} {
			var hits int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE "+table+" MATCH ? AND rowid=?", `"`+word+`"`, rowID).Scan(&hits); err != nil {
				return nil, err
			}
			row[table+"_sample_hits"] = hits
		}
		samples++
	}
	result["search"] = search
	return result, nil
}

func queryAuditRows(ctx context.Context, query queryer, statement string, budget int64, used *int64, args ...any) ([]map[string]any, error) {
	rows, err := query.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	values, pointers := make([]any, len(columns)), make([]any, len(columns))
	for index := range values {
		pointers[index] = &values[index]
	}
	result := []map[string]any{}
	for rows.Next() {
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		rowBytes := int64(64 + len(columns)*64)
		for index, column := range columns {
			value := values[index]
			if data, ok := value.([]byte); ok {
				value = string(data)
			}
			row[column] = value
			if text, ok := value.(string); ok {
				rowBytes += int64(len(text))
			}
		}
		*used += rowBytes
		if budget > 0 && *used > budget*4 {
			return nil, fmt.Errorf("%w: catalog scope exceeds memory budget", errAuditUnstable)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func seedAuditScope(ctx context.Context, catalog *Catalog, seed map[string][]map[string]any) error {
	catalog.DB.SetMaxOpenConns(1)
	if _, err := catalog.DB.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	tx, err := catalog.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	tables, err := tableNames(tx)
	if err != nil {
		return err
	}
	for table, rows := range seed {
		if table == "search" || !tables[table] {
			continue
		}
		for _, row := range rows {
			columns := []string{}
			for column := range row {
				columns = append(columns, column)
			}
			slices.Sort(columns)
			args := []any{}
			for _, column := range columns {
				args = append(args, row[column])
			}
			if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO \""+table+"\" (\""+strings.Join(columns, "\",\"")+"\") VALUES("+placeholders(len(args))+")", args...); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = catalog.DB.ExecContext(ctx, "PRAGMA foreign_keys=ON")
	return err
}

func normalizeAuditRows(table string, rows []map[string]any) string {
	normalized := []string{}
	for _, row := range rows {
		fields := map[string]any{}
		for key, value := range row {
			if slices.Contains([]string{"indexed_at", "updated_at", "built_at", "computed_at", "refined_at"}, key) || (table == "summaries" && key == "created_at") {
				continue
			}
			if table == "metrics" && key == "observed_at" {
				continue
			}
			if key == "id" {
				if _, ok := value.(int64); ok {
					continue
				}
			}
			if table == "workspaces" && slices.Contains([]string{"repository_id", "head_ref", "base_ref", "branch", "location_history_json"}, key) {
				continue
			}
			fields[key] = value
		}
		normalized = append(normalized, hashBytes([]byte(jsonText(fields))))
	}
	slices.Sort(normalized)
	return hashBytes([]byte(strings.Join(normalized, "\n")))
}

func (s *Server) auditIntentional(ctx context.Context, runID string, names []string) auditResult {
	pool := newAuditPool(s.Catalog)
	for _, source := range s.Config().Sources {
		if !source.Enabled || !automaticProvider(source.Kind) || (len(names) > 0 && !slices.Contains(names, source.Name)) {
			continue
		}
		adapter, err := MakeAdapter(source)
		if err != nil {
			pool.failure = err
			continue
		}
		setAdapterContext(adapter, ctx)
		err = adapter.(partialAdapter).discoverParts(func(parts []sourcePart) bool { pool.consider(unitOf(source, parts, false)); return true }, func(*WorkspaceRecord, []sourcePart) error { return errors.New("incomplete audit discovery") })
		if err != nil {
			pool.failure = err
		}
	}
	return s.Catalog.auditPool(ctx, pool, runID)
}

func (s *Server) startVerification(w http.ResponseWriter, r *http.Request, body map[string]any) {
	names := []string{}
	if value, present := body["sources"]; present {
		values, valid := sliceValue(value)
		if !valid {
			writeError(w, errors.New("sources must be a list of names"), 400)
			return
		}
		names = stringSlice(values)
	}
	if !s.acquireManualIngest() {
		writeError(w, errors.New("manual sync operation already running"), 409)
		return
	}
	run := s.startRunNamed("verification", nil)
	ctx, end := s.beginIngest(run.ID)
	if !s.spawn(func(context.Context) {
		defer s.ingestMu.Unlock()
		defer end()
		result := auditResult{}
		if boolValue(body, "all", false) {
			summary := s.verifyAllInputs(ctx, run.ID, func(summary verificationSummary) {
				s.updateRun(run.ID, func(run *SyncRun) {
					run.Phase = "verifying retained inputs"
					run.DoneConversations = summary.Checked
					run.Verification = &summary
				})
			}, names)
			if summary.Errors > 0 {
				result.Outcome, result.Detail = "read_error", fmt.Sprintf("%d inputs could not be read; see verification coverage", summary.Errors)
			} else if summary.Deferred > 0 {
				result.Detail = fmt.Sprintf("%d inputs deferred; see verification coverage", summary.Deferred)
			}
		} else {
			result = s.auditIntentional(ctx, run.ID, names)
		}
		state := "complete"
		if ctx.Err() != nil {
			state = "interrupted"
		} else if result.Outcome == "read_error" {
			state = "failed"
		}
		s.updateRun(run.ID, func(run *SyncRun) {
			run.State, run.Phase = state, state
			run.CompletedAt = now()
			run.Error = nilIfEmpty(result.Detail)
		})
	}) {
		end()
		s.ingestMu.Unlock()
		writeError(w, errors.New("service stopping"), 503)
		return
	}
	writeJSON(w, map[string]any{"run_id": run.ID}, 202)
}

func (s *Server) verifyAllInputs(ctx context.Context, runID string, progress func(verificationSummary), scopes ...[]string) verificationSummary {
	summary := verificationSummary{}
	names := []string{}
	if len(scopes) > 0 {
		names = scopes[0]
	}
	seen := map[string]bool{}
	visit := func(adapter Adapter, host string) {
		setAdapterContext(adapter, ctx)
		partial, ok := adapter.(partialAdapter)
		if !ok {
			summary.Deferred++
			return
		}
		err := partial.discoverParts(func(parts []sourcePart) bool {
			if ctx.Err() != nil {
				return true
			}
			unit := unitOf(adapter.Config(), parts, false)
			unit.Host, unit.Reader = host, adapter
			key := host + "/" + unit.Source.Name + "/" + unit.ID + "/" + partsSignature(parts)
			if seen[key] {
				return true
			}
			seen[key] = true
			result := s.Catalog.auditUnit(ctx, unit, runID, 0)
			if err := s.Catalog.recordVerification(unit, result); err != nil {
				result.Outcome = "read_error"
			}
			summary.Checked++
			switch result.Outcome {
			case "passed":
				summary.Passed++
			case "mismatch":
				summary.Mismatch++
			case "deferred":
				summary.Deferred++
			default:
				summary.Errors++
			}
			if progress != nil {
				progress(summary)
			}
			return true
		}, func(*WorkspaceRecord, []sourcePart) error {
			return errors.New("verification discovery attempted to parse")
		})
		if err != nil && ctx.Err() == nil {
			summary.Errors++
		}
	}
	for _, source := range s.Config().Sources {
		if ctx.Err() != nil {
			break
		}
		if !source.Enabled || !automaticProvider(source.Kind) || (len(names) > 0 && !slices.Contains(names, source.Name)) {
			continue
		}
		adapter, err := syncAdapter(source)
		if err != nil {
			summary.Errors++
			continue
		}
		visit(adapter, currentHost().ID)
	}
	targets, err := captureTargets(s.Config().CaptureRoot, nil, true, names)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		summary.Errors++
	}
	for _, target := range targets {
		if ctx.Err() != nil {
			break
		}
		adapter, manifest, view, err := openCaptureAdapter(target)
		if err != nil {
			summary.Errors++
			continue
		}
		visit(adapter, target.Host.ID)
		conductor, ok := adapter.(*conductorAdapter)
		if !ok {
			continue
		}
		for _, snapshot := range manifest.Snapshots {
			handled, err := conductorSessionIDsContext(ctx, filepath.Join(view.dir, filepath.FromSlash(snapshot.Captured)))
			if err != nil {
				summary.Errors++
				continue
			}
			for _, generation := range snapshot.Generations {
				if ctx.Err() != nil {
					break
				}
				file := filepath.Join(view.dir, filepath.FromSlash(generation.Captured))
				ids, err := conductorSessionIDsContext(ctx, file)
				if err != nil {
					if errors.Is(err, os.ErrNotExist) {
						summary.Deferred++
					} else {
						summary.Errors++
					}
					continue
				}
				missing := map[string]bool{}
				for id := range ids {
					if !handled[id] {
						missing[id], handled[id] = true, true
					}
				}
				if len(missing) == 0 {
					continue
				}
				older := &captureView{host: view.host, dir: view.dir, manifest: manifest, mappings: []capturePathMapping{{Original: snapshot.Path, Captured: file}}}
				config := conductor.config
				config.Path = file
				reader := &conductorAdapter{baseAdapter: baseAdapter{config: config, capability: conductor.capability, view: older}, selected: missing}
				visit(reader, target.Host.ID)
			}
		}
	}
	if progress != nil {
		progress(summary)
	}
	return summary
}

func (c *Catalog) recordVerification(unit syncUnit, result auditResult) error {
	_, err := c.DB.Exec(`INSERT INTO sync_verifications(host_id,source_name,unit,checked_at,outcome,detail,signature)
		VALUES(?,?,?,?,?,?,?) ON CONFLICT(host_id,source_name,unit) DO UPDATE SET checked_at=excluded.checked_at,
		outcome=excluded.outcome,detail=excluded.detail,signature=excluded.signature`, unit.hostID(), unit.Source.Name, unit.ID, now(), result.Outcome, nilIfEmpty(result.Detail), partsSignature(unit.Parts))
	return err
}
