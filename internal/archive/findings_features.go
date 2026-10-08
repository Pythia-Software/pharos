package archive

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Decoded inputs are immutable. The durable partition token is checked on
// every pass, including edits and publications by other processes. This avoids
// parsing unchanged JSON while retaining the durable cache across restarts.
type findingFeaturePartition struct {
	revision string
	rows     []map[string]any
}
type findingFeatureInput struct {
	version    string
	partitions map[string]findingFeaturePartition
}

const findingFeatureVersion = "features-v2"
const findingPartitionMarker = "/* finding partition */"

// featureRows caches SQL detector inputs, not candidates or user state. Every
// pass still runs discovery, gates, backtests, fixed plans and intervention
// decisions. Authoritative tool edits replace entire conversation partitions.
// The window and exact query are part of the version, so rollover and rule
// changes use the reference query. force/full findings discovery uses this too;
// runFindingsPassReference bypasses it for repair and parity audits.
func (env *findingEnv) featureRows(name, query, column string, args ...any) ([]map[string]any, error) {
	return env.featureRowsWithRevisions(name, query, column, "finding_tool_revisions", args...)
}

func (env *findingEnv) featureRowsWithRevisions(name, query, column, revisionsTable string, args ...any) ([]map[string]any, error) {
	reference := strings.Replace(query, findingPartitionMarker, "", 1)
	if !env.featureCache {
		rows, err := queryMapsContext(env.ctx, env.db, reference, args...)
		orderFindingFeatures(name, rows)
		return rows, err
	}
	if strings.Count(query, findingPartitionMarker) != 1 {
		return nil, fmt.Errorf("%s: missing feature partition selector", name)
	}
	version := hashBytes([]byte(findingFeatureVersion + ":" + findingsVersion + ":" + query + ":" + jsonText(args)))
	snapshot, shared := env.db.(*sql.Tx)
	var conn *sql.Conn
	var err error
	if !shared {
		conn, err = env.catalog.DB.Conn(env.ctx)
		if err != nil {
			return nil, err
		}
		defer conn.Close()
		snapshot, err = conn.BeginTx(env.ctx, nil)
		if err != nil {
			return nil, err
		}
		defer snapshot.Rollback()
	}
	var built string
	err = snapshot.QueryRowContext(env.ctx, "SELECT version FROM finding_feature_builds WHERE feature=?", name).Scan(&built)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	full := built != version
	revisions, err := queryMapsContext(env.ctx, snapshot, `SELECT r.conversation_id,r.revision FROM `+revisionsTable+` r
 LEFT JOIN finding_feature_partitions p ON p.feature=? AND p.conversation_id=r.conversation_id
 WHERE ? OR p.conversation_id IS NULL OR p.revision<>r.revision`, name, full)
	if err != nil {
		return nil, err
	}
	dirty := map[string]bool{}
	for _, row := range revisions {
		dirty[firstString(row["conversation_id"])] = true
	}
	partitions := map[string][]map[string]any{}
	nextInput := &findingFeatureInput{version: version, partitions: map[string]findingFeaturePartition{}}
	state := &env.catalog.findings
	state.featureMu.Lock()
	previous := state.featureInputs[name]
	state.featureMu.Unlock()
	if !full {
		// Read just tokens first. Large JSON overflow pages are visited only for
		// partitions missing from memory or changed since the previous pass.
		cached, err := queryMapsContext(env.ctx, snapshot, "SELECT conversation_id,revision FROM finding_feature_partitions WHERE feature=?", name)
		if err != nil {
			return nil, err
		}
		for _, row := range cached {
			id, revision := firstString(row["conversation_id"]), firstString(row["revision"])
			if env.roots[id] == "" {
				continue
			}
			if previous != nil && previous.version == version {
				if part, ok := previous.partitions[id]; ok && part.revision == revision {
					partitions[id] = part.rows
					nextInput.partitions[id] = part
					continue
				}
			}
			var text string
			if err := snapshot.QueryRowContext(env.ctx, "SELECT rows_json FROM finding_feature_partitions WHERE feature=? AND conversation_id=?", name, id).Scan(&text); err != nil {
				return nil, err
			}
			var rows []map[string]any
			decoder := json.NewDecoder(bytes.NewBufferString(text))
			decoder.UseNumber()
			if err := decoder.Decode(&rows); err != nil {
				// Cache bytes are disposable. Rebuild this conversation from the
				// authoritative snapshot instead of leaving manual refresh stuck.
				if !dirty[id] {
					revisions = append(revisions, map[string]any{"conversation_id": id, "revision": revision})
					dirty[id] = true
				}
				continue
			}
			partitions[id] = rows
			nextInput.partitions[id] = findingFeaturePartition{revision: revision, rows: rows}
		}
	}
	if len(revisions) > 0 || full {
		selector := ""
		if !full {
			if _, err := snapshot.ExecContext(env.ctx, "DROP TABLE IF EXISTS temp.finding_feature_dirty"); err != nil {
				return nil, err
			}
			if _, err := snapshot.ExecContext(env.ctx, "CREATE TEMP TABLE finding_feature_dirty(conversation_id TEXT PRIMARY KEY)"); err != nil {
				return nil, err
			}
			for _, row := range revisions {
				if _, err := snapshot.ExecContext(env.ctx, "INSERT INTO temp.finding_feature_dirty VALUES(?)", row["conversation_id"]); err != nil {
					return nil, err
				}
			}
			selector = " AND " + column + " IN (SELECT conversation_id FROM temp.finding_feature_dirty) "
		}
		rows, err := queryMapsContext(env.ctx, snapshot, strings.Replace(query, findingPartitionMarker, selector, 1), args...)
		if err != nil {
			return nil, err
		}
		if !full {
			if _, err := snapshot.ExecContext(env.ctx, "DROP TABLE temp.finding_feature_dirty"); err != nil {
				return nil, err
			}
		}
		for _, revision := range revisions {
			partitions[firstString(revision["conversation_id"])] = nil
		}
		for _, row := range prepareFindingFeatures(name, rows) {
			id := firstString(row["conversation_id"])
			partitions[id] = append(partitions[id], row)
		}
	}
	for _, row := range revisions {
		id := firstString(row["conversation_id"])
		if env.roots[id] != "" {
			nextInput.partitions[id] = findingFeaturePartition{revision: firstString(row["revision"]), rows: partitions[id]}
		}
	}
	if !shared {
		if err := snapshot.Commit(); err != nil {
			return nil, err
		}
	}
	if len(revisions) > 0 || full {
		// Publishing an old revision is harmless: the next read compares it with
		// the authoritative token. Never mark a newer token as consumed.
		if err := env.catalog.writeTransaction(env.ctx, "finding-features "+name, func(tx *sql.Tx) error {
			var current string
			err := tx.QueryRowContext(env.ctx, "SELECT version FROM finding_feature_builds WHERE feature=?", name).Scan(&current)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			if !full && current != built {
				return nil
			}
			if full {
				if _, err := tx.ExecContext(env.ctx, "DELETE FROM finding_feature_partitions WHERE feature=?", name); err != nil {
					return err
				}
			}
			statement, err := tx.PrepareContext(env.ctx, `INSERT INTO finding_feature_partitions(feature,conversation_id,revision,rows_json) VALUES(?,?,?,?)
   ON CONFLICT(feature,conversation_id) DO UPDATE SET revision=excluded.revision,rows_json=excluded.rows_json`)
			if err != nil {
				return err
			}
			defer statement.Close()
			for _, row := range revisions {
				id := firstString(row["conversation_id"])
				if _, err := statement.ExecContext(env.ctx, name, id, row["revision"], jsonText(partitions[id])); err != nil {
					return err
				}
			}
			_, err = tx.ExecContext(env.ctx, `INSERT INTO finding_feature_builds(feature,version) VALUES(?,?) ON CONFLICT(feature) DO UPDATE SET version=excluded.version`, name, version)
			return err
		}); err != nil {
			return nil, err
		}
	}
	state.featureMu.Lock()
	if state.featureInputs == nil {
		state.featureInputs = map[string]*findingFeatureInput{}
	}
	state.featureInputs[name] = nextInput
	state.featureMu.Unlock()
	ids := make([]string, 0, len(partitions))
	for id := range partitions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rows := []map[string]any{}
	for _, id := range ids {
		rows = append(rows, partitions[id]...)
	}
	orderFindingFeatures(name, rows)
	return rows, nil
}

// Match the reference scans' index order, including timestamp/token ties.
// Evidence limits and recoveries depend on traversal order, so conversation
// partitioning must not reorder the merged input.
func orderFindingFeatures(name string, rows []map[string]any) {
	switch name {
	case "failures", "drift":
		sort.SliceStable(rows, func(i, j int) bool {
			a, b := firstString(rows[i]["started_at"]), firstString(rows[j]["started_at"])
			if a != b {
				return a < b
			}
			return integer(rows[i]["feature_order"]) < integer(rows[j]["feature_order"])
		})
	case "heavy-output":
		sort.SliceStable(rows, func(i, j int) bool {
			a, b := integer(rows[i]["result_tokens"]), integer(rows[j]["result_tokens"])
			if a != b {
				return a < b
			}
			return integer(rows[i]["feature_order"]) < integer(rows[j]["feature_order"])
		})
	}
}

// These classifications use only authoritative call fields. Compute them when
// their conversation changes, then leave scope, pricing and statistics to the
// current pass. Bump findingFeatureVersion when these extraction rules change.
func prepareFindingFeatures(name string, rows []map[string]any) []map[string]any {
	kept := rows[:0]
	for _, row := range rows {
		switch name {
		case "heavy-output":
			shape := heavyShape(firstString(row["program"]), firstString(row["subcommand"]), firstString(row["command"]))
			if shape == "" {
				continue
			}
			row["feature_shape"] = shape
		case "drift":
			found := false
			providers := []string{""}
			for provider := range harnessFiles {
				providers = append(providers, provider)
			}
			for _, provider := range providers {
				kind, file := instructionHunt(provider, firstString(row["tool_category"]), firstString(row["tool_name"]), firstString(row["program"]), firstString(row["command"]), firstString(row["file_path"]))
				row["feature_hunt_kind:"+provider], row["feature_hunt_file:"+provider] = kind, file
				found = found || kind != ""
			}
			if !found {
				continue
			}
		case "failures":
			signature := firstString(row["error_signature"])
			errorType := firstString(row["error_type"])
			if errorType == "user_rejected" || errorType == "timeout" || errorType == "interrupted" || failureNotice.MatchString(signature) {
				continue
			}
			row["feature_family"] = failureFamily(signature)
		}
		kept = append(kept, row)
	}
	return kept
}
