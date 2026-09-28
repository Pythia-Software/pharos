package archive

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// backfillHarnessVersions advances a durable cursor in small transactions. It
// reads retained Claude events and only Codex session metadata from source files.
func (c *Catalog) backfillHarnessVersions() error {
	var cursor int64
	capturedPaths := map[string]map[string]string{}
	_ = c.DB.QueryRow(`SELECT CAST(value AS INTEGER) FROM meta WHERE key='harness_version_backfill'`).Scan(&cursor)
	for {
		rows, err := queryMaps(c.DB, `SELECT c.rowid rowid,c.id,c.provider,c.origin,c.origin_host_id,w.source_kind
   FROM conversations c JOIN workspaces w ON w.id=c.workspace_id WHERE c.rowid>? ORDER BY c.rowid LIMIT 250`, cursor)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		tx, err := c.beginWrite(context.Background())
		if err != nil {
			return err
		}
		for _, row := range rows {
			id := firstString(row["id"])
			kind := firstString(row["source_kind"])
			harness := kind
			first, last := "", ""
			switch kind {
			case "claude":
				harness = "claude-code"
				events, e := queryMaps(tx, `SELECT json_extract(raw_text,'$.version') version,json_extract(raw_text,'$.entrypoint') entrypoint FROM messages
     WHERE conversation_id=? AND raw_text IS NOT NULL AND json_valid(raw_text) AND (json_extract(raw_text,'$.version') IS NOT NULL OR json_extract(raw_text,'$.entrypoint') IS NOT NULL)
     ORDER BY COALESCE(source_order,0),created_at,id`, id)
				if e != nil {
					tx.Rollback()
					return e
				}
				for _, event := range events {
					if v := firstString(event["version"]); v != "" {
						if first == "" {
							first = v
						}
						last = v
					}
					if entry := firstString(event["entrypoint"]); entry != "" {
						harness = "claude-code/" + entry
					}
				}
			case "codex":
				harness, first, last = codexHarnessFromFile(c.harnessSourcePath(firstString(row["origin"]), firstString(row["origin_host_id"]), capturedPaths))
			case "conductor":
				harness = "conductor"
			case "chatgpt":
				harness = "chatgpt-export"
			case "tl1", "tl1-export":
				harness = "tl1"
			case "canonical":
				harness = "canonical"
			case "antigravity":
				harness = "antigravity"
			}
			_, err = tx.Exec(`UPDATE conversations SET harness=CASE WHEN harness IS NULL OR harness IN ('claude-code','codex') THEN COALESCE(?,harness) ELSE harness END,harness_version_first=COALESCE(harness_version_first,?),
    harness_version_last=COALESCE(harness_version_last,?),harness_version_source=CASE WHEN harness_version_last IS NULL AND ?<>'' THEN 'transcript' ELSE harness_version_source END WHERE id=? AND (harness IS NULL OR harness IN ('claude-code','codex') OR (harness_version_first IS NULL AND ?<>'') OR (harness_version_last IS NULL AND ?<>''))`,
				nilIfEmpty(harness), nilIfEmpty(first), nilIfEmpty(last), last, id, first, last)
			if err != nil {
				tx.Rollback()
				return err
			}
			cursor = integer(row["rowid"])
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
		root := filepath.Join(filepath.Dir(filepath.Dir(c.Path)), "captures", host)
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
