package archive

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// backfillHarnessVersions advances a durable cursor in small transactions. A
// completed pass resets the cursor so files captured later can fill missing
// versions, while conversations already filled by indexing are skipped.
func (c *Catalog) backfillHarnessVersions() error {
	return c.backfillHarnessVersionsContext(context.Background())
}

func (c *Catalog) backfillHarnessVersionsContext(ctx context.Context) error {
	var cursor int64
	capturedPaths := newHarnessInputs()
	defer capturedPaths.Close()
	if err := c.DB.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM meta WHERE key='harness_version_backfill'`).Scan(&cursor); err != nil && err != sql.ErrNoRows {
		return err
	}
	for {
		rows, err := queryMapsContext(ctx, c.DB, `SELECT c.rowid rowid,c.id,c.origin,c.origin_host_id,w.source_kind
   FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
   WHERE c.rowid>? AND (c.harness IS NULL OR (w.source_kind IN ('claude','codex') AND (c.harness_version_first IS NULL OR c.harness_version_last IS NULL)))
   ORDER BY c.rowid LIMIT 250`, cursor)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return c.writeTransaction(ctx, "harness-backfill", func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('harness_version_backfill','0') ON CONFLICT(key) DO UPDATE SET value='0'`)
				return err
			})
		}
		// Source files and retained message JSON are read before taking a write lock.
		prepared := make([]harnessBackfillRow, 0, len(rows))
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			value := harnessBackfillRow{id: firstString(row["id"]), kind: firstString(row["source_kind"]), rowid: integer(row["rowid"])}
			value.harness = value.kind
			switch value.kind {
			case "claude":
				value.harness = "claude-code"
				events, e := queryMapsContext(ctx, c.DB, `SELECT json_extract(raw_text,'$.version') version,json_extract(raw_text,'$.entrypoint') entrypoint FROM messages
     WHERE conversation_id=? AND raw_text IS NOT NULL AND json_valid(raw_text) AND (json_extract(raw_text,'$.version') IS NOT NULL OR json_extract(raw_text,'$.entrypoint') IS NOT NULL)
     ORDER BY COALESCE(source_order,0),created_at,id`, value.id)
				if e != nil {
					return e
				}
				for _, event := range events {
					if v := firstString(event["version"]); v != "" {
						if value.first == "" {
							value.first = v
						}
						value.last = v
					}
					if entry := firstString(event["entrypoint"]); entry != "" {
						value.harness = "claude-code/" + entry
					}
				}
			case "codex":
				path, err := c.harnessSourcePath(ctx, firstString(row["origin"]), firstString(row["origin_host_id"]), "codex", capturedPaths)
				if err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					continue
				}
				value.harness, value.first, value.last = capturedPaths.files(path).codexHarness(ctx, path)
			case "conductor":
				value.harness = "conductor"
			case "chatgpt":
				value.harness = "chatgpt-export"
			case "tl1", "tl1-export":
				value.harness = "tl1"
			case "canonical":
				value.harness = "canonical"
			case "antigravity":
				value.harness = "antigravity"
			}
			prepared = append(prepared, value)
		}
		capturedPaths.Close()
		last := integer(rows[len(rows)-1]["rowid"])
		if err := c.writeTransaction(ctx, "harness-backfill", func(tx *sql.Tx) error {
			for _, value := range prepared {
				if _, err := tx.ExecContext(ctx, `UPDATE conversations SET harness=CASE WHEN harness IS NULL OR harness IN ('claude-code','codex') THEN COALESCE(?,harness) ELSE harness END,harness_version_first=COALESCE(harness_version_first,?),
    harness_version_last=COALESCE(harness_version_last,?),harness_version_source=CASE WHEN harness_version_last IS NULL AND ?<>'' THEN 'transcript' ELSE harness_version_source END WHERE id=? AND (harness IS NULL OR (harness_version_first IS NULL AND ?<>'') OR (harness_version_last IS NULL AND ?<>''))`,
					nilIfEmpty(value.harness), nilIfEmpty(value.first), nilIfEmpty(value.last), value.last, value.id, value.first, value.last); err != nil {
					return err
				}
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('harness_version_backfill',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, last)
			return err
		}); err != nil {
			return err
		}
		cursor = last
	}
}

type harnessBackfillRow struct {
	id, kind, harness, first, last string
	rowid                          int64
}

// One lookup pass caches each host's manifest inventory and owns its temporary
// readers until the caller finishes parsing. A bad source never fails indexing.
type harnessInputs struct {
	paths       map[string]map[string]string
	inventories map[string]map[string]harnessCapture
	failures    map[string]error
	leases      []func()
	// views are the leases' capture views by leased path: their readers open
	// through them, streaming an encoded file rather than decoding it to disk.
	views map[string]*captureView
}

// files is how a path harnessSourcePath returned is read.
func (inputs *harnessInputs) files(path string) sourceFiles {
	return sourceFiles{view: inputs.views[path]}
}

type harnessCapture struct {
	dir    string
	host   Host
	source captureSourceInfo
	file   *capturedFile
}

func newHarnessInputs() *harnessInputs {
	return &harnessInputs{paths: map[string]map[string]string{}, inventories: map[string]map[string]harnessCapture{}, failures: map[string]error{}, views: map[string]*captureView{}}
}
func (inputs *harnessInputs) Close() {
	for _, close := range inputs.leases {
		close()
	}
	inputs.leases = nil
	inputs.paths = map[string]map[string]string{}
}

func (c *Catalog) harnessSourcePath(ctx context.Context, origin, host, provider string, cache *harnessInputs) (string, error) {
	if c.evidenceOnly || host == "" || origin == "" || strings.HasPrefix(origin, "sqlite:") {
		return "", nil
	}
	if path, ok := cache.paths[host][origin]; ok {
		return path, nil
	}
	if cache.paths[host] == nil {
		cache.paths[host] = map[string]string{}
	}
	if cache.inventories[host] == nil {
		cache.inventories[host] = map[string]harnessCapture{}
		root := filepath.Join(c.captureRootPath(), host)
		sources, err := os.ReadDir(root)
		if err != nil && !os.IsNotExist(err) {
			cache.failures[host] = err
		}
		for _, source := range sources {
			if !source.IsDir() || !validCaptureName(source.Name()) {
				continue
			}
			dir := filepath.Join(root, source.Name())
			lock, err := lockCaptureTarget(ctx, dir)
			if err != nil {
				cache.failures[host] = err
				continue
			}
			manifest, err := loadCaptureManifest(dir)
			releaseLockFile(lock)
			if err != nil {
				cache.failures[host] = err
				continue
			}
			for _, file := range manifest.Files {
				if file != nil {
					cache.inventories[host][file.Path] = harnessCapture{dir, manifest.Host, manifest.Source, file}
				}
			}
		}
	}
	if retained, ok := cache.inventories[host][origin]; ok {
		lock, err := lockCaptureTarget(ctx, retained.dir)
		if err != nil {
			return "", err
		}
		only := &captureManifest{Version: captureManifestVersion, Host: retained.host, Source: captureSourceInfo{Name: retained.source.Name, Kind: provider, Path: origin, Account: retained.source.Account}, Files: []*capturedFile{retained.file}, PathMap: []capturePathMapping{{Original: origin, Captured: retained.file.Captured}}}
		input, err := freezeManifestInput(ctx, captureTarget{Host: retained.host, Source: retained.source.Name, Dir: retained.dir}, SourceConfig{}, false, only)
		releaseLockFile(lock)
		if err == nil {
			cache.leases = append(cache.leases, input.Close)
			path := input.Adapter.Config().Path
			cache.views[path] = captureViewOf(input.Adapter)
			cache.paths[host][origin] = path
			return path, nil
		}
		cache.failures[host] = err
	}
	if host == currentHost().ID {
		if _, err := os.Stat(origin); err == nil {
			// Best-effort metadata enrichment may read local inputs, but must
			// not silently create persistent captures during sync or upgrade.
			cache.paths[host][origin] = origin
			return origin, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	if err := cache.failures[host]; err != nil {
		return "", err
	}
	cache.paths[host][origin] = ""
	return "", nil
}

func codexHarnessFromFile(path string) (string, string, string) {
	return codexHarnessFromFileContext(context.Background(), path)
}

func codexHarnessFromFileContext(ctx context.Context, path string) (string, string, string) {
	return sourceFiles{}.codexHarness(ctx, path)
}

// codexHarness reads a rollout forward through the capture view.
func (s sourceFiles) codexHarness(ctx context.Context, path string) (string, string, string) {
	harness, first, last := "codex", "", ""
	file, err := s.Open(path)
	if err != nil {
		return harness, first, last
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for {
		if ctx.Err() != nil {
			break
		}
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			var event struct {
				Type    string `json:"type"`
				Payload struct {
					Originator string `json:"originator"`
					CLIVersion string `json:"cli_version"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &event) == nil && event.Type == "session_meta" {
				if event.Payload.Originator != "" {
					harness = "codex/" + event.Payload.Originator
				}
				if v := event.Payload.CLIVersion; v != "" {
					if first == "" {
						first = v
					}
					last = v
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			break
		}
	}
	return harness, first, last
}

func (c *Catalog) inheritHarnessAliases() error {
	return c.inheritHarnessAliasesContext(context.Background())
}

func (c *Catalog) inheritHarnessAliasesContext(ctx context.Context) error {
	return c.writeTransaction(ctx, "harness-aliases", func(tx *sql.Tx) error { return inheritHarnessAliasesTxContext(ctx, tx) })
}

func inheritHarnessAliasesTx(tx *sql.Tx) error {
	return inheritHarnessAliasesTxContext(context.Background(), tx)
}

func inheritHarnessAliasesTxContext(ctx context.Context, tx *sql.Tx) error {
	// Keep any already-compatible native peer. Filtering candidates before
	// choosing a peer would otherwise alternate between disagreeing aliases.
	_, err := tx.ExecContext(ctx, `WITH peers AS (
   SELECT l.left_id wrapper_id,native.harness_version_first,native.harness_version_last
   FROM conversation_identity_links l JOIN conversations native ON native.id=l.right_id
   JOIN workspaces w ON w.id=native.workspace_id WHERE l.relationship='native-alias' AND w.source_kind IN ('claude','codex') AND native.harness_version_last IS NOT NULL
   UNION ALL
   SELECT l.right_id,native.harness_version_first,native.harness_version_last
   FROM conversation_identity_links l JOIN conversations native ON native.id=l.left_id
   JOIN workspaces w ON w.id=native.workspace_id WHERE l.relationship='native-alias' AND w.source_kind IN ('claude','codex') AND native.harness_version_last IS NOT NULL)
 UPDATE conversations AS wrapper SET
  harness_version_first=COALESCE(wrapper.harness_version_first,peer.harness_version_first),
  harness_version_last=peer.harness_version_last,harness_version_source='alias'
  FROM peers peer
  WHERE wrapper.id=peer.wrapper_id AND wrapper.harness='conductor'
  AND (wrapper.harness_version_first IS NOT COALESCE(wrapper.harness_version_first,peer.harness_version_first)
   OR wrapper.harness_version_last IS NOT peer.harness_version_last
   OR wrapper.harness_version_source IS NOT 'alias')
  AND wrapper.id NOT IN (SELECT current.id FROM peers stable JOIN conversations current ON current.id=stable.wrapper_id
   WHERE current.harness_version_first IS COALESCE(current.harness_version_first,stable.harness_version_first)
   AND current.harness_version_last IS stable.harness_version_last AND current.harness_version_source='alias')`)

	return err
}
