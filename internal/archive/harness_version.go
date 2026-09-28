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
	var cursor int64
	capturedPaths := map[string]map[string]string{}
	if err := c.DB.QueryRow(`SELECT CAST(value AS INTEGER) FROM meta WHERE key='harness_version_backfill'`).Scan(&cursor); err != nil && err != sql.ErrNoRows {
		return err
	}
	for {
		rows, err := queryMaps(c.DB, `SELECT c.rowid rowid,c.id,c.origin,c.origin_host_id,w.source_kind
   FROM conversations c JOIN workspaces w ON w.id=c.workspace_id
   WHERE c.rowid>? AND (c.harness IS NULL OR (w.source_kind IN ('claude','codex') AND (c.harness_version_first IS NULL OR c.harness_version_last IS NULL)))
   ORDER BY c.rowid LIMIT 250`, cursor)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			_, err = c.DB.Exec(`INSERT INTO meta(key,value) VALUES('harness_version_backfill','0') ON CONFLICT(key) DO UPDATE SET value='0'`)
			return err
		}
		// Source files and retained message JSON are read before taking a write lock.
		prepared := make([]harnessBackfillRow, 0, len(rows))
		for _, row := range rows {
			value := harnessBackfillRow{id: firstString(row["id"]), kind: firstString(row["source_kind"]), rowid: integer(row["rowid"])}
			value.harness = value.kind
			switch value.kind {
			case "claude":
				value.harness = "claude-code"
				events, e := queryMaps(c.DB, `SELECT json_extract(raw_text,'$.version') version,json_extract(raw_text,'$.entrypoint') entrypoint FROM messages
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
				value.harness, value.first, value.last = codexHarnessFromFile(c.harnessSourcePath(firstString(row["origin"]), firstString(row["origin_host_id"]), capturedPaths))
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
		tx, err := c.beginWrite(context.Background())
		if err != nil {
			return err
		}
		for _, value := range prepared {
			_, err = tx.Exec(`UPDATE conversations SET harness=CASE WHEN harness IS NULL OR harness IN ('claude-code','codex') THEN COALESCE(?,harness) ELSE harness END,harness_version_first=COALESCE(harness_version_first,?),
    harness_version_last=COALESCE(harness_version_last,?),harness_version_source=CASE WHEN harness_version_last IS NULL AND ?<>'' THEN 'transcript' ELSE harness_version_source END WHERE id=? AND (harness IS NULL OR (harness_version_first IS NULL AND ?<>'') OR (harness_version_last IS NULL AND ?<>''))`,
				nilIfEmpty(value.harness), nilIfEmpty(value.first), nilIfEmpty(value.last), value.last, value.id, value.first, value.last)
			if err != nil {
				tx.Rollback()
				return err
			}
			cursor = value.rowid
		}
		if _, err = tx.Exec(`INSERT INTO meta(key,value) VALUES('harness_version_backfill',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, cursor); err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
}

type harnessBackfillRow struct {
	id, kind, harness, first, last string
	rowid                          int64
}

func (c *Catalog) harnessSourcePath(origin, host string, cache map[string]map[string]string) string {
	if host == currentHost().ID {
		if _, err := os.Stat(origin); err == nil {
			return origin
		}
	}
	if host == "" || origin == "" || strings.HasPrefix(origin, "sqlite:") {
		return ""
	}
	paths, ok := cache[host]
	if !ok {
		paths = map[string]string{}
		root := filepath.Join(c.captureRootPath(), host)
		sources, err := os.ReadDir(root)
		if err == nil {
			for _, source := range sources {
				if !source.IsDir() {
					continue
				}
				dir := filepath.Join(root, source.Name())
				manifest, err := loadCaptureManifest(dir)
				if err != nil {
					continue
				}
				for _, file := range manifest.Files {
					if file != nil {
						paths[file.Path] = filepath.Join(dir, filepath.FromSlash(file.Captured))
					}
				}
			}
		}
		cache[host] = paths
	}
	if path := paths[origin]; path != "" {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func codexHarnessFromFile(path string) (string, string, string) {
	harness, first, last := "codex", "", ""
	file, err := os.Open(path)
	if err != nil {
		return harness, first, last
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for {
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
	_, err := c.DB.Exec(`UPDATE conversations AS wrapper SET
  harness_version_first=COALESCE(wrapper.harness_version_first,peer.harness_version_first),
  harness_version_last=peer.harness_version_last,harness_version_source='alias'
  FROM (SELECT l.left_id wrapper_id,native.harness_version_first,native.harness_version_last
   FROM conversation_identity_links l JOIN conversations native ON native.id=l.right_id
   JOIN workspaces w ON w.id=native.workspace_id WHERE l.relationship='native-alias' AND w.source_kind IN ('claude','codex') AND native.harness_version_last IS NOT NULL
   UNION ALL
   SELECT l.right_id,native.harness_version_first,native.harness_version_last
   FROM conversation_identity_links l JOIN conversations native ON native.id=l.left_id
   JOIN workspaces w ON w.id=native.workspace_id WHERE l.relationship='native-alias' AND w.source_kind IN ('claude','codex') AND native.harness_version_last IS NOT NULL) peer
  WHERE wrapper.id=peer.wrapper_id AND wrapper.harness='conductor'`)
	return err
}
