package archive

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Temporary audit evidence is not a preservation inventory. Only the selected
// rollouts and the raw naming rows their normal parser can consult are copied.
// In particular, a large state database/WAL is not itself the evidence size.
type codexAuditDatabase struct {
	Name    string
	Schema  []map[string]any
	Rows    []map[string]any
	Version int64 `json:"-"`
	HasPath bool
}

type codexAuditNames struct {
	IndexPresent bool
	Index        []string
	Databases    []codexAuditDatabase
	IndexVersion int64 `json:"-"`
}

type auditContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r auditContextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

func scopedCodexAudit(unit syncUnit) bool {
	if unit.Source.Kind != "codex" || unit.Record != nil {
		return false
	}
	if unit.Reader == nil {
		return true
	}
	_, ok := unit.Reader.(*jsonlAdapter)
	return ok
}

func auditEvidenceBudget(used, budget int64) error {
	if budget > 0 && used > budget {
		return &auditDeferral{Reason: "evidence_size", Detail: "selected evidence exceeds verification budget"}
	}
	return nil
}

// readCodexAuditNames holds a read transaction on each supported store while
// selecting the dependency closure. Path collisions matter, including a row
// shadowed by a newer schema, so their IDs are included in every store. No
// naming value is taken from the catalog under verification.
func readCodexAuditNames(ctx context.Context, adapter *jsonlAdapter, identities, paths map[string]bool, budget int64) (codexAuditNames, error) {
	result := codexAuditNames{}
	info, err := os.Stat(adapter.config.Path)
	if err != nil || !info.IsDir() {
		return result, err
	}
	type reader struct {
		db *sql.DB
		tx *sql.Tx
	}
	readers := []reader{}
	defer func() {
		for _, reader := range readers {
			reader.tx.Rollback()
			reader.db.Close()
		}
	}()
	databases, err := codexNamingDatabases(adapter.config.Path)
	if err != nil {
		return result, err
	}
	ids := map[string]bool{}
	for id := range identities {
		ids[id] = true
	}
	for _, path := range databases {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		readable, err := adapter.files().Database(path)
		if err != nil {
			return result, err
		}
		db, err := openReadOnlySQLite(readable)
		if err != nil {
			return result, err
		}
		tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			db.Close()
			return result, err
		}
		readers = append(readers, reader{db, tx})
		schema, err := queryMapsContext(ctx, tx, "PRAGMA table_info(threads)")
		if err != nil {
			return result, err
		}
		columns, err := tableColumns(tx, "threads")
		if err != nil || !columns["id"] || !columns["name"] {
			return result, errors.Join(err, errAuditUnstable)
		}
		result.Databases = append(result.Databases, codexAuditDatabase{Name: filepath.Base(path), Schema: schema, Version: adapter.databaseVersion(path), HasPath: columns["rollout_path"]})
		if !columns["rollout_path"] {
			continue
		}
		// The resolver cleans rollout paths in Go. Stream just IDs and paths
		// rather than missing aliases such as /sessions/../sessions/rollout.
		rows, err := tx.QueryContext(ctx, "SELECT id,rollout_path FROM threads")
		if err != nil {
			return result, err
		}
		for rows.Next() {
			var id, rollout any
			if err = rows.Scan(&id, &rollout); err != nil {
				break
			}
			if path := firstString(rollout); path != "" && paths[filepath.Clean(path)] && firstString(id) != "" {
				ids[firstString(id)] = true
				if limitErr := auditEvidenceBudget(int64(len(ids))*128, budget); limitErr != nil {
					rows.Close()
					return result, limitErr
				}
			}
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return result, err
		}
	}
	var used int64
	for index, reader := range readers {
		store := &result.Databases[index]
		columns := "id,name"
		if store.HasPath {
			columns += ",rollout_path"
		}
		// Stream the parser's projection in its original occurrence order.
		// Filtering in Go preserves its Unicode/whitespace ID normalization
		// and duplicate-ID semantics; unrelated rows never enter the snapshot.
		rows, err := reader.tx.QueryContext(ctx, "SELECT "+columns+" FROM threads")
		if err != nil {
			return result, err
		}
		for rows.Next() {
			values := make([]any, 2)
			if store.HasPath {
				values = append(values, nil)
			}
			destinations := make([]any, len(values))
			for i := range values {
				destinations[i] = &values[i]
			}
			if err = rows.Scan(destinations...); err != nil {
				break
			}
			if !ids[firstString(values[0])] {
				continue
			}
			row := map[string]any{"id": values[0], "name": values[1]}
			if store.HasPath {
				row["rollout_path"] = values[2]
			}
			used += int64(len(jsonText(row)))
			if err = auditEvidenceBudget(used, budget); err != nil {
				break
			}
			store.Rows = append(store.Rows, row)
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return result, err
		}
		used += int64(len(jsonText(store.Schema)))
		if err := auditEvidenceBudget(used, budget); err != nil {
			return result, err
		}
	}
	index := filepath.Join(adapter.config.Path, "session_index.jsonl")
	if info, err := adapter.files().Stat(index); err == nil {
		result.IndexPresent, result.IndexVersion = true, info.ModTime().UnixNano()
		file, err := adapter.files().Open(index)
		if err != nil {
			return result, err
		}
		defer file.Close()
		reader := bufio.NewReaderSize(auditContextReader{ctx, file}, 64<<10)
		for {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			line := []byte{}
			var err error
			for {
				var fragment []byte
				fragment, err = reader.ReadSlice('\n')
				line = append(line, fragment...)
				if limitErr := auditEvidenceBudget(int64(len(line)), budget); limitErr != nil {
					return result, limitErr
				}
				if err != bufio.ErrBufferFull {
					break
				}
			}
			if err != nil && err != io.EOF {
				return result, err
			}
			decoder := json.NewDecoder(bytes.NewReader(line))
			decoder.UseNumber()
			var row map[string]any
			if decoder.Decode(&row) == nil && ids[firstString(row["id"])] {
				used += int64(len(line))
				if err := auditEvidenceBudget(used, budget); err != nil {
					return result, err
				}
				result.Index = append(result.Index, string(line))
			}
			if err == io.EOF {
				break
			}
		}
	} else if !os.IsNotExist(err) {
		return result, err
	}
	return result, auditEvidenceBudget(used, budget)
}

func freezeCodexAuditRecord(ctx context.Context, unit syncUnit, dir string, budget int64) (*WorkspaceRecord, []sourcePart, int64, error) {
	var adapter *jsonlAdapter
	if original, ok := unit.Reader.(*jsonlAdapter); ok {
		copy := *original
		adapter = &copy
	} else {
		reader, err := MakeAdapter(unit.Source)
		if err != nil {
			return nil, nil, 0, err
		}
		adapter = reader.(*jsonlAdapter)
	}
	adapter.ctx = ctx
	root := adapter.config.Path
	originalRoot := adapter.original(root)
	rootInfo, err := os.Stat(root)
	if err != nil {
		return nil, nil, 0, err
	}
	if !rootInfo.IsDir() {
		root = filepath.Dir(root)
		originalRoot = filepath.Dir(originalRoot)
	}
	manifest := &captureManifest{Host: Host{ID: unit.hostID()}, PathMap: []capturePathMapping{{Original: originalRoot, Captured: "source"}}}
	view := newCaptureView(manifest.Host, dir, manifest)
	paths, ids := map[string]bool{}, map[string]bool{}
	type transcript struct {
		path, copied string
		info         os.FileInfo
		native       os.FileInfo
		digest       string
	}
	files := []transcript{}
	var used int64
	for _, part := range unit.Parts {
		path := part.item
		if adapter.view != nil {
			var ok bool
			path, ok = adapter.view.captured(part.item)
			if !ok {
				return nil, nil, used, errAuditUnstable
			}
		}
		info, err := adapter.files().Stat(path)
		if err != nil {
			return nil, nil, used, err
		}
		used += info.Size()
		if err := auditEvidenceBudget(used, budget); err != nil {
			return nil, nil, used, err
		}
		copied, ok := view.captured(part.item)
		if !ok {
			return nil, nil, used, errors.New("Codex audit evidence outside source")
		}
		if err := os.MkdirAll(filepath.Dir(copied), 0700); err != nil {
			return nil, nil, used, err
		}
		input, err := adapter.files().Open(path)
		if err != nil {
			return nil, nil, used, err
		}
		output, err := os.OpenFile(copied, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			input.Close()
			return nil, nil, used, err
		}
		native, err := os.Stat(path)
		if err != nil {
			input.Close()
			output.Close()
			return nil, nil, used, err
		}
		digest := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(output, digest), auditContextReader{ctx, io.LimitReader(input, info.Size())})
		if copyErr == nil && written != info.Size() {
			copyErr = errAuditUnstable
		}
		err = errors.Join(copyErr, input.Close(), output.Close())
		if err == nil {
			err = os.Chtimes(copied, info.ModTime(), info.ModTime())
		}
		if err != nil {
			return nil, nil, used, err
		}
		files = append(files, transcript{path: path, copied: copied, info: info, native: native, digest: fmt.Sprintf("%x", digest.Sum(nil))})
		paths[filepath.Clean(part.item)] = true
		id, err := (sourceFiles{ctx: ctx}).readCodexRolloutIdentity(copied)
		if err != nil {
			return nil, nil, used, err
		}
		ids[id] = true
		if match := codexSessionFilename.FindStringSubmatch(filepath.Base(part.item)); len(match) == 2 {
			ids[match[1]] = true
		}
	}
	remaining := int64(0)
	if budget > 0 {
		remaining = max(1, budget-used)
	}
	names, err := readCodexAuditNames(ctx, adapter, ids, paths, remaining)
	if err != nil {
		return nil, nil, used, err
	}
	used += int64(len(jsonText(names)))
	if err := auditEvidenceBudget(used, budget); err != nil {
		return nil, nil, used, err
	}
	if names.IndexPresent {
		path := filepath.Join(dir, "source", "session_index.jsonl")
		if err := os.WriteFile(path, []byte(strings.Join(names.Index, "")), 0600); err != nil {
			return nil, nil, used, err
		}
		stamp := time.Unix(0, names.IndexVersion)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			return nil, nil, used, err
		}
	}
	for _, store := range names.Databases {
		path := filepath.Join(dir, "source", store.Name)
		db, err := sql.Open("sqlite", path)
		if err != nil {
			return nil, nil, used, err
		}
		columns := "id,name"
		if store.HasPath {
			columns += ",rollout_path"
		}
		func() {
			defer db.Close()
			_, err = db.ExecContext(ctx, "CREATE TABLE threads("+columns+")")
			if err != nil {
				return
			}
			for _, row := range store.Rows {
				values := []any{row["id"], row["name"]}
				if store.HasPath {
					values = append(values, row["rollout_path"])
				}
				if _, err = db.ExecContext(ctx, "INSERT INTO threads VALUES("+placeholders(len(values))+")", values...); err != nil {
					return
				}
			}
		}()
		if err != nil {
			return nil, nil, used, err
		}
		rel, _ := filepath.Rel(dir, path)
		manifest.Snapshots = append(manifest.Snapshots, &capturedSnapshot{Path: filepath.Join(originalRoot, store.Name), Captured: filepath.ToSlash(rel), Source: sqliteSourceState{MTimeNS: store.Version}})
	}
	config := adapter.Config()
	config.Path = filepath.Join(dir, "source")
	if !rootInfo.IsDir() {
		config.Path = filepath.Join(config.Path, filepath.Base(adapter.config.Path))
	}
	reference, err := MakeAdapter(config)
	if err != nil {
		return nil, nil, used, err
	}
	reference.(interface{ bindCapture(*captureView) }).bindCapture(view)
	setAdapterContext(reference, ctx)
	var record *WorkspaceRecord
	var read []sourcePart
	err = reference.(partialAdapter).discoverParts(func(parts []sourcePart) bool { return unitOf(unit.Source, parts, false).ID != unit.ID }, func(value *WorkspaceRecord, parts []sourcePart) error {
		if record != nil {
			return errors.New("Codex audit selected more than one group")
		}
		if adapter.view != nil {
			retainedRecordIdentity(adapter.view, value, parts)
		}
		record, read = value, parts
		return nil
	})
	if err != nil {
		return nil, nil, used, err
	}
	// Re-read only the selected dependency closure, not the database/WAL
	// timestamps: another conversation may legitimately be writing meanwhile.
	after, err := readCodexAuditNames(ctx, adapter, ids, paths, remaining)
	if err != nil {
		return nil, nil, used, err
	}
	if jsonText(names) != jsonText(after) {
		return nil, nil, used, errAuditUnstable
	}
	for _, file := range files {
		after, err := adapter.files().Stat(file.path)
		if err != nil || after.Size() != file.info.Size() || !after.ModTime().Equal(file.info.ModTime()) {
			return nil, nil, used, errAuditUnstable
		}
		native, err := os.Stat(file.path)
		if err != nil || !os.SameFile(file.native, native) {
			return nil, nil, used, errAuditUnstable
		}
		input, err := adapter.files().Open(file.path)
		if err != nil {
			return nil, nil, used, err
		}
		digest := sha256.New()
		written, err := io.Copy(digest, auditContextReader{ctx, io.LimitReader(input, file.info.Size()+1)})
		err = errors.Join(err, input.Close())
		if err != nil {
			return nil, nil, used, err
		}
		if written != file.info.Size() || fmt.Sprintf("%x", digest.Sum(nil)) != file.digest {
			return nil, nil, used, errAuditUnstable
		}
	}
	return record, read, used, nil
}
