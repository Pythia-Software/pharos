package archive

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const statisticsRowCap = 100000
const statisticsByteCap = 64 << 20
const statisticsTTL = 5 * time.Minute

type statisticsRow struct {
	ID        string                     `json:"id"`
	Workspace string                     `json:"workspace"`
	Meta      map[string]any             `json:"meta"`
	Metrics   map[string]statMetricValue `json:"metrics"`
	Aliases   []string                   `json:"aliases,omitempty"`
	Members   []string                   `json:"members,omitempty"`
	Score     float64                    `json:"score,omitempty"`
	MessageID string                     `json:"message_id,omitempty"`
}
type statisticsSnapshot struct {
	Rows       []statisticsRow           `json:"rows"`
	Membership []statisticsRow           `json:"membership,omitempty"`
	Coverage   map[string]map[string]int `json:"coverage"`
	Complete   bool                      `json:"complete"`
	Freshness  map[string]any            `json:"freshness"`
	ID         string                    `json:"-"`
	Secret     string                    `json:"-"`
	Created    int64                     `json:"-"`
	Expires    int64                     `json:"-"`
}

func (c *Catalog) queryStatistics(tool string, args map[string]any) (any, error) {
	if tool == "query_statistics" && args["release_snapshot"] == true {
		for key := range args {
			if key != "snapshot_id" && key != "release_snapshot" {
				return nil, fmt.Errorf("release_snapshot only accepts snapshot_id")
			}
		}
		id, ok := args["snapshot_id"].(string)
		if !ok || id == "" {
			return nil, fmt.Errorf("release_snapshot requires snapshot_id")
		}
		result, err := c.DB.Exec("DELETE FROM mcp_stat_snapshots WHERE id=?", id)
		if err != nil {
			return nil, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		return map[string]any{"snapshot_id": id, "released": count > 0, "returned_count": 0}, nil
	}
	r, err := parseMetricRequest(tool, args)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var snapshot statisticsSnapshot
	newSnapshot := false
	position := 0
	if r.Cursor != "" || r.SnapshotID != "" {
		if r.Cursor != "" {
			data, e := base64.RawURLEncoding.DecodeString(r.Cursor)
			if e != nil {
				return nil, fmt.Errorf("invalid cursor")
			}
			parts := strings.Split(string(data), ".")
			if len(parts) != 4 {
				return nil, fmt.Errorf("invalid cursor")
			}
			if r.SnapshotID != "" && r.SnapshotID != parts[0] {
				return nil, fmt.Errorf("cursor and snapshot_id refer to different snapshots")
			}
			r.SnapshotID = parts[0]
		}
		var original statisticsRequest
		snapshot, original, err = c.loadStatisticsSnapshot(ctx, r.SnapshotID)
		if err != nil {
			return nil, err
		}
		if r.Cursor != "" {
			position, err = statisticsCursorPosition(snapshot, r)
			if err != nil {
				return nil, err
			}
		}
		if err = compatibleStatisticsRequest(original, r, snapshot.Membership != nil); err != nil {
			return nil, err
		}
		if original.Entity == "workspace" && r.Entity == "conversation" {
			snapshot.Rows = snapshot.Membership
			snapshot.Complete = true
			snapshot.Coverage = map[string]map[string]int{}
			if len(r.WorkspaceIDs) > 0 {
				ids := map[string]bool{}
				for _, id := range r.WorkspaceIDs {
					ids[id] = true
				}
				kept := []statisticsRow{}
				for _, row := range snapshot.Rows {
					if ids[row.Workspace] {
						kept = append(kept, row)
					}
				}
				snapshot.Rows = kept
			}
		}
	} else {
		// Do a bounded amount of numeric-summary work. Remaining dirty groups
		// are explicitly pending, and the service's maintenance loop backfills them.
		if len(r.Metrics) > 0 {
			where, args := statisticsContext(r)
			from := `workspaces w LEFT JOIN repositories repo ON repo.id=w.repository_id`
			if r.Entity == "conversation" {
				from = `conversations c JOIN workspaces w ON w.id=c.workspace_id LEFT JOIN repositories repo ON repo.id=w.repository_id`
			}
			dirty, e := queryMapsContext(ctx, c.DB, `SELECT DISTINCT w.id FROM `+from+` JOIN mcp_metric_dirty d ON d.workspace_id=w.id WHERE `+strings.Join(where, " AND ")+` LIMIT 100`, args...)
			if e != nil {
				return nil, e
			}
			ids := []string{}
			for _, row := range dirty {
				ids = append(ids, firstString(row["id"]))
			}
			if _, err = c.refreshMetricProjections(ctx, 100, ids...); err != nil {
				return nil, err
			}
		}
		snapshot, err = c.buildStatisticsSnapshot(ctx, r)
		if err != nil {
			return nil, err
		}
		if r.Mode == "metadata" {
			newSnapshot = true
		}
	}
	if r.Mode == "cards" {
		return c.statisticsCards(snapshot, r)
	}
	render := func() (any, error) {
		if r.Type == "aggregate" {
			return statisticsAggregatePage(snapshot, r, position)
		}
		return statisticsMetadataPage(snapshot, r, position)
	}
	result, err := render()
	if err != nil {
		return nil, err
	}
	// Complete one-shot results need no persisted state. Keep it only for
	// continuation, captured membership access, or explicitly requested reuse.
	if newSnapshot && (r.RetainSnapshot || statisticsNeedsSnapshot(result)) {
		prepareStatisticsSnapshot(c, &snapshot)
		// Snapshot metadata and signed cursors count toward the output budget.
		// Validate the final response before consuming a shared snapshot slot.
		result, err = render()
		if err != nil {
			return nil, err
		}
		if err = c.saveStatisticsSnapshot(ctx, &snapshot, r); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func statisticsNeedsSnapshot(result any) bool {
	page := result.(map[string]any)
	if page["next_cursor"] != nil {
		return true
	}
	items, _ := page["items"].([]map[string]any)
	for _, item := range items {
		if item["conversation_ids_access"] != nil {
			return true
		}
	}
	return false
}

func (c *Catalog) buildStatisticsSnapshot(ctx context.Context, r statisticsRequest) (statisticsSnapshot, error) {
	snapshot := statisticsSnapshot{Coverage: map[string]map[string]int{}, Complete: r.Query == ""}
	tx, err := c.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return snapshot, err
	}
	defer tx.Rollback()
	clauses, values := statisticsContext(r)
	entityID, from, projectionID, entity := "w.id", `workspaces w LEFT JOIN repositories repo ON repo.id=w.repository_id`, "w.id", "workspace"
	fields := `w.id workspace_id,w.source_kind source,w.activity_at,repo.display_name repository,w.indexed_at,
	CASE WHEN EXISTS(SELECT 1 FROM conversations ac WHERE ac.workspace_id=w.id AND ac.coverage<>'complete') THEN 'partial' ELSE 'complete' END archive_coverage`
	if r.Entity == "conversation" {
		entityID = "c.id"
		from = `conversations c JOIN workspaces w ON w.id=c.workspace_id LEFT JOIN repositories repo ON repo.id=w.repository_id`
		fields += `,c.provider,c.model,c.effort,c.coverage,c.started_at,c.ended_at`
		if r.Scope == "conversation" {
			projectionID = "c.id"
			entity = "conversation"
		}
	}
	if r.Query != "" || r.Mode == "cards" {
		fields += `,substr(w.title,1,201) title`
	}
	jsonFields := []string{}
	for _, name := range r.Metrics {
		jsonFields = append(jsonFields, "'"+name+"',json_extract(p.metrics_json,'$."+name+"')")
	}
	metrics := "'{}'"
	if len(jsonFields) > 0 {
		metrics = "CASE WHEN p.entity_id IS NULL THEN NULL ELSE json_object(" + strings.Join(jsonFields, ",") + ") END"
	}
	rows, err := queryMapsContext(ctx, tx, `SELECT `+entityID+` entity_id,`+fields+`,`+metrics+` metrics,EXISTS(SELECT 1 FROM mcp_metric_dirty d WHERE d.workspace_id=w.id) metric_pending FROM `+from+` LEFT JOIN mcp_metric_projection p ON p.entity='`+entity+`' AND p.entity_id=`+projectionID+` WHERE `+strings.Join(clauses, " AND ")+` LIMIT ?`, append(values, statisticsRowCap+1)...)
	if err != nil {
		return snapshot, err
	}
	if len(rows) > statisticsRowCap {
		return snapshot, fmt.Errorf("statistics population exceeds %d rows; narrow filters", statisticsRowCap)
	}
	all := []statisticsRow{}
	for _, row := range rows {
		m, e := decodeMetrics(row["metrics"])
		if e != nil {
			return snapshot, e
		}
		selectedMetrics := map[string]statMetricValue{}
		for _, name := range r.Metrics {
			selectedMetrics[name] = m[name]
			if integer(row["metric_pending"]) != 0 {
				v := selectedMetrics[name]
				v.Status = "pending"
				selectedMetrics[name] = v
			}
		}
		all = append(all, statisticsRow{ID: firstString(row["entity_id"]), Workspace: firstString(row["workspace_id"]), Meta: row, Metrics: selectedMetrics, Aliases: []string{firstString(row["entity_id"])}})
		delete(row, "metrics")
		delete(row, "entity_id")
		delete(row, "metric_pending")
	}
	links, err := queryMapsContext(ctx, tx, `SELECT l.left_id,l.right_id,a.workspace_id left_workspace,b.workspace_id right_workspace FROM conversation_identity_links l JOIN conversations a ON a.id=l.left_id JOIN conversations b ON b.id=l.right_id`)
	if err != nil {
		return snapshot, err
	}
	all = canonicalStatisticsRows(all, links, r.Entity)
	for _, name := range r.Metrics {
		snapshot.Coverage[name] = map[string]int{}
	}
	eligible := []statisticsRow{}
	for _, row := range all {
		matches := true
		for _, name := range r.Metrics {
			snapshot.Coverage[name][row.Metrics[name].Status]++
		}
		for _, f := range r.Filters {
			if !metricMatches(row.Metrics[f.Name], f, r.Quality) {
				matches = false
				break
			}
		}
		if matches {
			eligible = append(eligible, row)
		}
	}
	if r.Query != "" {
		eligible, err = c.statisticsTextMatches(ctx, tx, eligible, r)
		if err != nil {
			return snapshot, err
		}
	}
	sortStatisticsRows(eligible, r)
	snapshot.Rows = eligible
	if statContainsString(r.Select, "conversation_ids") || statContainsString(r.Select, "conversation_count") {
		// IDs are loaded once for the population; no per-workspace detail reads.
		wanted := map[string]int{}
		for i, row := range snapshot.Rows {
			for _, id := range row.Aliases {
				wanted[id] = i
			}
		}
		workspaceIDs := []string{}
		for id := range wanted {
			workspaceIDs = append(workspaceIDs, id)
		}
		membership, err := queryMapsContext(ctx, tx, `SELECT c.id,c.workspace_id,c.coverage FROM conversations c WHERE c.workspace_id IN (SELECT value FROM json_each(?)) AND `+currentGroupConversation("c")+` LIMIT ?`, jsonText(workspaceIDs), statisticsRowCap+1)
		if err != nil {
			return snapshot, err
		}
		members := []statisticsRow{}
		for _, m := range membership {
			if i, ok := wanted[firstString(m["workspace_id"])]; ok {
				members = append(members, statisticsRow{ID: firstString(m["id"]), Workspace: snapshot.Rows[i].Workspace, Meta: map[string]any{"coverage": m["coverage"]}, Metrics: map[string]statMetricValue{}, Aliases: []string{firstString(m["id"])}})
			}
		}
		if len(membership) > statisticsRowCap {
			return snapshot, fmt.Errorf("membership population exceeds %d; omit conversation_ids/count or narrow the library", statisticsRowCap)
		}
		members = canonicalStatisticsRows(members, links, "conversation")
		sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })
		byWorkspace := map[string]int{}
		for i, row := range snapshot.Rows {
			byWorkspace[row.Workspace] = i
		}
		for _, m := range members {
			if i, ok := byWorkspace[m.Workspace]; ok {
				snapshot.Rows[i].Members = append(snapshot.Rows[i].Members, m.ID)
			}
		}
		snapshot.Membership = members
	}
	if err = tx.Commit(); err != nil {
		return snapshot, err
	}
	freshness, err := c.archiveSourceStatus()
	if err != nil {
		return snapshot, err
	}
	snapshot.Freshness = map[string]any{"status": freshness["status"], "stale_sources": freshness["stale_sources"]}
	return snapshot, nil
}

func statisticsContext(r statisticsRequest) ([]string, []any) {
	where := []string{"1=1"}
	args := []any{}
	if r.Entity == "conversation" {
		where = append(where, currentGroupConversation("c"))
	}
	if r.Repository != "" {
		where = append(where, "(repo.display_name LIKE ? OR repo.canonical_remote LIKE ?)")
		args = append(args, "%"+r.Repository+"%", "%"+r.Repository+"%")
	}
	if r.Source != "" {
		where = append(where, "w.source_kind=?")
		args = append(args, r.Source)
	}
	for _, field := range []struct{ value, column string }{{r.Owner, "w.owner"}, {r.Flavor, "w.flavor"}, {r.Version, "w.version"}, {r.Outcome, "w.outcome"}} {
		if field.value != "" {
			where = append(where, field.column+" LIKE ?")
			args = append(args, "%"+field.value+"%")
		}
	}
	if r.Error != "" {
		where = append(where, "EXISTS(SELECT 1 FROM metric_ledger ml WHERE ml.workspace_id=w.id AND ml.error_type LIKE ?)")
		args = append(args, "%"+r.Error+"%")
	}
	if len(r.WorkspaceIDs) > 0 {
		where = append(where, "w.id IN (SELECT value FROM json_each(?))")
		args = append(args, jsonText(r.WorkspaceIDs))
	}
	if r.Provider != "" {
		if r.Entity == "conversation" {
			where = append(where, "c.provider=?")
		} else {
			where = append(where, "EXISTS(SELECT 1 FROM conversations cp WHERE cp.workspace_id=w.id AND cp.provider=?)")
		}
		args = append(args, r.Provider)
	}
	if r.Model != "" || r.Effort != "" {
		alias := "c"
		if r.Entity == "workspace" {
			alias = "ce"
		}
		conv, cv := configurationPredicate(alias+".model", alias+".effort", r.Model, normalizeEffort(r.Effort))
		msg, mv := configurationPredicate("COALESCE(me.model,"+alias+".model)", "me.effort", r.Model, normalizeEffort(r.Effort))
		clause := "((" + conv + ") OR EXISTS(SELECT 1 FROM messages me WHERE me.conversation_id=" + alias + ".id AND (me.model IS NOT NULL OR me.effort IS NOT NULL) AND " + msg + "))"
		if r.Entity == "workspace" {
			clause = "EXISTS(SELECT 1 FROM conversations ce WHERE ce.workspace_id=w.id AND " + clause + ")"
		}
		where = append(where, clause)
		args = append(args, cv...)
		args = append(args, mv...)
	}
	if r.File != "" {
		where = append(where, "EXISTS(SELECT 1 FROM change_sets cs JOIN change_files cf ON cf.change_set_id=cs.id WHERE cs.workspace_id=w.id AND cf.path LIKE ?)")
		args = append(args, "%"+r.File+"%")
	}
	if r.PR != nil {
		where = append(where, "EXISTS(SELECT 1 FROM work_pr_links l JOIN pull_requests pr ON pr.id=l.pr_id WHERE l.workspace_id=w.id AND pr.number=?)")
		args = append(args, *r.PR)
	}
	date := "w.activity_at"
	if r.Entity == "conversation" {
		date = "COALESCE(c.started_at,w.activity_at)"
	}
	if r.From != "" {
		where = append(where, date+">=?")
		args = append(args, timeBound(r.From, false))
	}
	if r.To != "" {
		where = append(where, date+"<=?")
		args = append(args, timeBound(r.To, true))
	}
	return where, args
}

func canonicalStatisticsRows(rows []statisticsRow, links []map[string]any, entity string) []statisticsRow {
	parent := map[string]string{}
	for _, r := range rows {
		parent[r.ID] = r.ID
	}
	// Intermediate aliases need not satisfy the request's context filters.
	// Resolve their identity edges too, or A-B-C would count A and C twice
	// whenever B is outside the selected source/repository/ID cohort.
	for _, l := range links {
		left, right := firstString(l["left_id"]), firstString(l["right_id"])
		if entity == "workspace" {
			left, right = firstString(l["left_workspace"]), firstString(l["right_workspace"])
		}
		for _, id := range []string{left, right} {
			if id != "" && parent[id] == "" {
				parent[id] = id
			}
		}
	}
	var root func(string) string
	root = func(id string) string {
		if parent[id] != id {
			parent[id] = root(parent[id])
		}
		return parent[id]
	}
	for _, l := range links {
		left, right := firstString(l["left_id"]), firstString(l["right_id"])
		if entity == "workspace" {
			left, right = firstString(l["left_workspace"]), firstString(l["right_workspace"])
		}
		if parent[left] != "" && parent[right] != "" {
			parent[root(right)] = root(left)
		}
	}
	groups := map[string][]statisticsRow{}
	for _, r := range rows {
		groups[root(r.ID)] = append(groups[root(r.ID)], r)
	}
	out := []statisticsRow{}
	priority := map[string]int{"tl1": 0, "tl1-export": 0, "codex": 1, "claude": 1, "antigravity": 1, "conductor": 9}
	for _, group := range groups {
		sort.Slice(group, func(i, j int) bool {
			a, ok := priority[firstString(group[i].Meta["source"])]
			if !ok {
				a = 5
			}
			b, ok := priority[firstString(group[j].Meta["source"])]
			if !ok {
				b = 5
			}
			if a != b {
				return a < b
			}
			return group[i].ID < group[j].ID
		})
		chosen := group[0]
		chosen.Aliases = []string{}
		for _, r := range group {
			chosen.Aliases = append(chosen.Aliases, r.ID)
			for name, v := range r.Metrics {
				old := chosen.Metrics[name]
				if metricQualityRank(v) < metricQualityRank(old) {
					chosen.Metrics[name] = v
				}
			}
		}
		sort.Strings(chosen.Aliases)
		out = append(out, chosen)
	}
	return out
}
func metricQualityRank(v statMetricValue) int {
	if v.Value == nil {
		return 4
	}
	switch v.Status {
	case "complete":
		return 0
	case "assumed":
		return 1
	case "partial":
		return 2
	default:
		return 3
	}
}

// statisticsTextLimits bound a statistics text search: it ranks at most
// ranked of the recent most recently indexed eligible matches. Tests lower
// them.
var statisticsTextLimits = struct{ recent, ranked int }{25000, 500}

func (c *Catalog) statisticsTextMatches(ctx context.Context, tx *sql.Tx, rows []statisticsRow, r statisticsRequest) ([]statisticsRow, error) {
	if _, err := tx.ExecContext(ctx, "CREATE TEMP TABLE mcp_statistics_eligible(id TEXT PRIMARY KEY)"); err != nil {
		return nil, err
	}
	defer tx.ExecContext(context.Background(), "DROP TABLE IF EXISTS temp.mcp_statistics_eligible")
	aliases := map[string]int{}
	for i, row := range rows {
		for _, id := range row.Aliases {
			aliases[id] = i
		}
	}
	if r.Entity == "conversation" {
		for id := range aliases {
			if _, err := tx.ExecContext(ctx, "INSERT INTO temp.mcp_statistics_eligible VALUES(?)", id); err != nil {
				return nil, err
			}
		}
	} else {
		for id := range aliases {
			if _, err := tx.ExecContext(ctx, "INSERT INTO temp.mcp_statistics_eligible SELECT id FROM conversations WHERE workspace_id=?", id); err != nil {
				return nil, err
			}
		}
	}
	if parsed := ftsQuery(r.Query); parsed != "" {
		matches, err := queryMapsContext(ctx, tx, `WITH recent AS MATERIALIZED (
		SELECT message_id,rank FROM messages_fts WHERE messages_fts MATCH ? AND rowid IN
		(SELECT f.fts_rowid FROM message_fts_rows f JOIN messages m ON m.id=f.message_id JOIN temp.mcp_statistics_eligible e ON e.id=m.conversation_id)
		ORDER BY rowid DESC LIMIT `+fmt.Sprint(statisticsTextLimits.recent)+`)
		SELECT m.conversation_id,c.workspace_id,recent.message_id FROM recent JOIN messages m ON m.id=recent.message_id JOIN conversations c ON c.id=m.conversation_id ORDER BY recent.rank LIMIT `+fmt.Sprint(statisticsTextLimits.ranked), parsed)
		if err != nil {
			return nil, err
		}
		seen := map[int]bool{}
		for _, m := range matches {
			id := firstString(m["conversation_id"])
			if r.Entity == "workspace" {
				id = firstString(m["workspace_id"])
			}
			if i, ok := aliases[id]; ok && !seen[i] {
				seen[i] = true
				rows[i].Score = .55 / float64(len(seen))
				rows[i].MessageID = firstString(m["message_id"])
			}
		}
	}
	store := conversationVectors
	if r.Entity == "workspace" {
		store = workspaceVectors
	}
	scores, err := statisticsVectorScores(ctx, tx, store, r.Query, aliases)
	if err != nil {
		return nil, err
	}
	for _, s := range scores {
		if i, ok := aliases[s.id]; ok {
			rows[i].Score += s.score * .35
		}
	}
	out := []statisticsRow{}
	for _, row := range rows {
		if strings.Contains(strings.ToLower(firstString(row.Meta["title"])), strings.ToLower(r.Query)) {
			row.Score += .3
		}
		if row.Score > 0 {
			out = append(out, row)
		}
	}
	return out, nil
}

// Score eligible vectors using the leased read connection. Acquiring another
// pooled connection here can starve four concurrent queries, and would read
// vectors from a different snapshot than their metrics and identity links.
func statisticsVectorScores(ctx context.Context, tx *sql.Tx, store vectorStore, query string, eligible map[string]int) ([]semanticMatch, error) {
	ids := []string{}
	for id := range eligible {
		ids = append(ids, id)
	}
	encoded := jsonText(ids)
	queryVector := semanticEmbed(query)
	rows, err := tx.QueryContext(ctx, "SELECT "+store.key+",vector,NULL FROM "+store.vectors+" WHERE "+store.key+" IN (SELECT value FROM json_each(?)) UNION ALL SELECT "+store.key+",NULL,vector_json FROM "+store.documents+" WHERE "+store.key+" IN ("+store.missing()+") AND "+store.key+" IN (SELECT value FROM json_each(?))", encoded, encoded)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	matches := []semanticMatch{}
	buffer := make([]float64, 0, semanticDimensions)
	for rows.Next() {
		var id string
		var blob sql.RawBytes
		var text sql.NullString
		if err = rows.Scan(&id, &blob, &text); err != nil {
			return nil, err
		}
		var vector []float64
		if text.Valid {
			vector, err = decodeVector(text.String)
			if err != nil {
				continue
			}
		} else {
			buffer = appendVectorBlob(buffer[:0], blob)
			vector = buffer
		}
		if score := semanticCosine(queryVector, vector); score > .1 {
			matches = append(matches, semanticMatch{id, score})
		}
	}
	return matches, rows.Err()
}

func sortStatisticsRows(rows []statisticsRow, r statisticsRequest) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if r.Order != nil {
			x, y := a.Metrics[r.Order.Metric].effective(r.Quality), b.Metrics[r.Order.Metric].effective(r.Quality)
			if x == nil && y != nil {
				return false
			}
			if x != nil && y == nil {
				return true
			}
			if x != nil && y != nil && x.(float64) != y.(float64) {
				if r.Order.Direction == "asc" {
					return x.(float64) < y.(float64)
				}
				return x.(float64) > y.(float64)
			}
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		at, bt := firstString(a.Meta["started_at"], a.Meta["activity_at"]), firstString(b.Meta["started_at"], b.Meta["activity_at"])
		if at != bt {
			return at > bt
		}
		return a.ID < b.ID
	})
}

func statContainsString(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}

func (c *Catalog) statisticsCards(s statisticsSnapshot, r statisticsRequest) (any, error) {
	start := min(r.Offset, len(s.Rows))
	end := min(start+r.Limit, len(s.Rows))
	cards := []map[string]any{}
	for _, row := range s.Rows[start:end] {
		card := map[string]any{"workspace_id": row.Workspace, "score": row.Score}
		if r.Entity == "conversation" {
			card["conversation_id"] = row.ID
			card["started_at"] = row.Meta["started_at"]
			card["activity_at"] = row.Meta["activity_at"]
			if row.MessageID != "" {
				card["message_id"] = row.MessageID
			}
		} else {
			card["id"] = row.ID
		}
		cards = append(cards, card)
	}
	if r.Entity == "conversation" {
		if err := c.conversationCards(cards, ftsQuery(r.Query)); err != nil {
			return nil, err
		}
	} else {
		var err error
		cards, err = c.libraryPage(context.Background(), cards)
		if err != nil {
			return nil, err
		}
	}
	for i, card := range cards {
		card["metrics"] = statisticsMetricEvidence(s.Rows[start+i], r)
		selected := statisticsMetadataRow(s.Rows[start+i], r, s)
		for _, field := range r.Select {
			if _, err := metricName(field); err != nil {
				card[field] = selected[field]
			}
		}
		for _, field := range []string{"conversation_count", "conversation_ids_complete", "conversation_ids_access"} {
			if v, ok := selected[field]; ok {
				card[field] = v
			}
		}
		if r.Entity == "workspace" {
			for _, key := range []string{"purpose", "outcome", "first_input", "last_response"} {
				if value, ok := card[key].(string); ok {
					card[key] = clipText(value, 160)
				}
			}
		}
	}
	result := statisticsEnvelope(s, r)
	result["items"] = cards
	result["total"] = len(s.Rows)
	result["next_offset"] = nil
	for {
		result["returned_count"] = len(cards)
		if start+len(cards) < len(s.Rows) {
			result["next_offset"] = start + len(cards)
		}
		if len(jsonText(result)) <= r.Budget*3 {
			break
		}
		if len(cards) <= 1 {
			return nil, fmt.Errorf("max_output_tokens too small for one metric card; increase budget or use metadata mode")
		}
		cards = cards[:len(cards)-1]
		result["items"] = cards
		result["truncated"] = true
	}
	return result, nil
}

func statisticsMetricEvidence(row statisticsRow, r statisticsRequest) map[string]any {
	m := map[string]any{}
	for _, name := range r.Metrics {
		v := row.Metrics[name]
		owner := row.ID
		if r.Scope == "workspace" {
			owner = row.Workspace
		}
		m[name] = map[string]any{"value": v.Value, "effective_value": v.effective(r.Quality), "status": v.Status, "scope": r.Scope, "owner_id": owner}
		if name == "cost_usd" {
			m[name].(map[string]any)["price_status"] = nilIfEmpty(v.PriceStatus)
		}
	}
	return m
}

func statisticsEnvelope(s statisticsSnapshot, r statisticsRequest) map[string]any {
	schema := map[string]any{}
	for _, d := range metricDefinitions {
		if statContainsString(r.Metrics, d.Name) {
			schema[d.Name] = map[string]any{"unit": d.Unit, "definition": d.Definition, "scope": r.Scope, "default_status": "complete", "derivation_version": metricProjectionVersion}
			if d.Name == "cost_usd" {
				schema[d.Name].(map[string]any)["default_price_status"] = "priced"
			}
		}
	}
	effective := map[string]any{}
	_ = json.Unmarshal([]byte(jsonText(r)), &effective)
	delete(effective, "cursor")
	delete(effective, "snapshot_id")
	result := map[string]any{"metric_schema": schema, "metric_coverage": map[string]any{"cohort": "structured_context_before_metric_filters", "counts": s.Coverage}, "effective_request": effective, "freshness": s.Freshness, "population_complete": s.Complete, "total_matching": len(s.Rows)}
	if !s.Complete {
		result["retrieval_limits"] = map[string]any{"recent_eligible_matches": statisticsTextLimits.recent, "ranked_messages": statisticsTextLimits.ranked, "semantic_minimum": .1}
	}
	if s.ID != "" {
		result["snapshot_id"] = s.ID
		result["snapshot_created_at"] = time.UnixMilli(s.Created).UTC().Format(time.RFC3339Nano)
		result["snapshot_expires_at"] = time.UnixMilli(s.Expires).UTC().Format(time.RFC3339Nano)
	}
	return result
}

func statisticsMetadataRow(row statisticsRow, r statisticsRequest, s statisticsSnapshot) map[string]any {
	item := map[string]any{"workspace_id": row.Workspace}
	if r.Entity == "conversation" {
		item["conversation_id"] = row.ID
	}
	details := map[string]any{}
	for _, name := range r.Select {
		if _, err := metricName(name); err == nil {
			v := row.Metrics[name]
			item[name] = v.effective(r.Quality)
			if v.Status != "complete" {
				details[name] = map[string]any{"value": v.Value, "status": v.Status}
				if name == "cost_usd" {
					details[name].(map[string]any)["price_status"] = nilIfEmpty(v.PriceStatus)
				}
			}
			continue
		}
		switch name {
		case "workspace_id", "conversation_id":
		case "linked_conversation_ids", "mirrored_workspace_ids":
			aliases := []string{}
			for _, id := range row.Aliases {
				if id != row.ID {
					aliases = append(aliases, id)
				}
			}
			item[name] = aliases
		case "conversation_ids":
			item[name] = row.Members[:min(20, len(row.Members))]
			if row.Members == nil {
				item[name] = []string{}
			}
			item["conversation_count"] = len(row.Members)
			item["conversation_ids_complete"] = len(row.Members) <= 20
			if len(row.Members) > 20 {
				item["conversation_ids_access"] = map[string]any{"tool": "query_statistics", "arguments": map[string]any{"entity": "conversation", "metric_scope": "conversation", "workspace_ids": []string{row.Workspace}, "select": []string{}, "snapshot_id": s.ID}}
			}
		case "conversation_count":
			item[name] = len(row.Members)
		default:
			item[name] = row.Meta[name]
		}
	}
	if len(details) > 0 {
		item["metric_details"] = details
	}
	if coverage := firstString(row.Meta["coverage"]); r.Entity == "conversation" && coverage != "complete" {
		item["coverage"] = coverage
	}
	if r.Entity == "workspace" && firstString(row.Meta["archive_coverage"]) != "complete" {
		item["archive_coverage"] = row.Meta["archive_coverage"]
	}
	return item
}

func statisticsMetadataPage(s statisticsSnapshot, r statisticsRequest, position int) (any, error) {
	if position < 0 || position > len(s.Rows) {
		return nil, fmt.Errorf("invalid cursor position")
	}
	items := []map[string]any{}
	end := min(position+r.Limit, len(s.Rows))
	for _, row := range s.Rows[position:end] {
		items = append(items, statisticsMetadataRow(row, r, s))
	}
	result := statisticsEnvelope(s, r)
	result["items"] = items
	for {
		result["returned_count"] = len(items)
		result["next_cursor"] = nil
		if position+len(items) < len(s.Rows) {
			result["next_cursor"] = statisticsCursor(s, r, position+len(items))
		}
		if len(jsonText(result)) <= r.Budget*3 {
			break
		}
		if len(items) <= 1 {
			return nil, fmt.Errorf("max_output_tokens too small for metadata and one row; increase budget or reduce selected fields")
		}
		items = items[:len(items)-1]
		result["items"] = items
	}
	return result, nil
}

func statisticsAggregatePage(s statisticsSnapshot, r statisticsRequest, position int) (any, error) {
	groups := map[string][]statisticsRow{}
	keys := map[string]map[string]any{}
	for _, row := range s.Rows {
		k := map[string]any{}
		for _, field := range r.GroupBy {
			k[field] = row.Meta[field]
		}
		encoded := jsonText(k)
		groups[encoded] = append(groups[encoded], row)
		keys[encoded] = k
	}
	if len(r.GroupBy) == 0 && len(groups) == 0 {
		groups["{}"] = []statisticsRow{}
		keys["{}"] = map[string]any{}
	}
	order := []string{}
	for k := range groups {
		order = append(order, k)
	}
	sort.Strings(order)
	if len(order) > 2000 {
		return nil, fmt.Errorf("more than 2000 aggregate groups; narrow filters")
	}
	items := []map[string]any{}
	for _, key := range order {
		values := map[string]any{}
		quality := map[string]any{}
		for _, a := range r.Aggregations {
			if a.Metric == "" {
				values[a.ID] = len(groups[key])
				continue
			}
			count := 0
			total, low, high := 0.0, 0.0, 0.0
			statuses := map[string]int{}
			for _, row := range groups[key] {
				v := row.Metrics[a.Metric]
				statuses[v.Status]++
				n, ok := number(v.effective(r.Quality))
				if !ok {
					continue
				}
				if count == 0 || n < low {
					low = n
				}
				if count == 0 || n > high {
					high = n
				}
				total += n
				count++
			}
			var value any
			if a.Op == "count" {
				value = count
			} else if count > 0 {
				switch a.Op {
				case "sum":
					value = total
				case "avg":
					value = total / float64(count)
				case "min":
					value = low
				case "max":
					value = high
				}
			}
			values[a.ID] = value
			quality[a.ID] = map[string]any{"usable_values": count, "population": len(groups[key]), "statuses": statuses}
		}
		items = append(items, map[string]any{"keys": keys[key], "values": values, "quality": quality})
	}
	if position < 0 || position > len(items) {
		return nil, fmt.Errorf("invalid aggregate cursor position")
	}
	totalGroups := len(items)
	items = items[position:min(position+r.Limit, len(items))]
	result := statisticsEnvelope(s, r)
	result["groups"] = items
	result["total_groups"] = totalGroups
	for {
		result["returned_count"] = len(items)
		result["next_cursor"] = nil
		if position+len(items) < totalGroups {
			result["next_cursor"] = statisticsCursor(s, r, position+len(items))
		}
		if len(jsonText(result)) <= r.Budget*3 {
			break
		}
		if len(items) <= 1 {
			return nil, fmt.Errorf("max_output_tokens too small for one aggregate group")
		}
		items = items[:len(items)-1]
		result["groups"] = items
	}
	return result, nil
}

func randomStatisticsID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func prepareStatisticsSnapshot(c *Catalog, s *statisticsSnapshot) {
	s.ID, s.Secret = randomStatisticsID(), randomStatisticsID()
	s.Created = c.clock().UnixMilli()
	s.Expires = s.Created + statisticsTTL.Milliseconds()
}
func (c *Catalog) saveStatisticsSnapshot(ctx context.Context, s *statisticsSnapshot, r statisticsRequest) error {
	payload, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if len(payload) > statisticsByteCap {
		return fmt.Errorf("statistics snapshot exceeds 64 MiB; narrow filters or select fewer fields")
	}
	if s.ID == "" {
		prepareStatisticsSnapshot(c, s)
	}
	tx, err := c.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM mcp_stat_snapshots WHERE expires_at<=?", s.Created); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM mcp_stat_snapshots").Scan(&count); err != nil {
		return err
	}
	if count >= 8 {
		return fmt.Errorf("eight statistics snapshots are active; reuse a snapshot or retry after expiry")
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO mcp_stat_snapshots VALUES(?,?,?,?,?,?)", s.ID, s.Created, s.Expires, jsonText(r), string(payload), s.Secret)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (c *Catalog) loadStatisticsSnapshot(ctx context.Context, id string) (statisticsSnapshot, statisticsRequest, error) {
	s, r := statisticsSnapshot{}, statisticsRequest{}
	var request, payload string
	err := c.DB.QueryRowContext(ctx, "SELECT created_at,expires_at,request_json,payload,secret FROM mcp_stat_snapshots WHERE id=?", id).Scan(&s.Created, &s.Expires, &request, &payload, &s.Secret)
	if err == sql.ErrNoRows {
		return s, r, fmt.Errorf("snapshot unavailable; restart the query")
	}
	if err != nil {
		return s, r, err
	}
	if c.clock().UnixMilli() >= s.Expires {
		return s, r, fmt.Errorf("snapshot expired; restart the query")
	}
	s.ID = id
	requestArgs := map[string]any{}
	if err = json.Unmarshal([]byte(request), &requestArgs); err != nil {
		return s, r, err
	}
	r, err = parseMetricRequest("query_statistics", requestArgs)
	if err != nil {
		return s, r, err
	}
	if err = json.Unmarshal([]byte(payload), &s); err != nil {
		return s, r, err
	}
	return s, r, nil
}

func statisticsShape(r statisticsRequest) string {
	r.Cursor, r.SnapshotID = "", ""
	r.RetainSnapshot = false
	r.Limit, r.Budget, r.Offset = 0, 0, 0
	data := sha256.Sum256([]byte(jsonText(r)))
	return hex.EncodeToString(data[:8])
}
func statisticsCursor(s statisticsSnapshot, r statisticsRequest, position int) string {
	body := fmt.Sprintf("%s.%d.%s", s.ID, position, statisticsShape(r))
	mac := hmac.New(sha256.New, []byte(s.Secret))
	mac.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString([]byte(body + "." + hex.EncodeToString(mac.Sum(nil))))
}
func statisticsCursorPosition(s statisticsSnapshot, r statisticsRequest) (int, error) {
	data, err := base64.RawURLEncoding.DecodeString(r.Cursor)
	if err != nil {
		return 0, fmt.Errorf("invalid cursor")
	}
	parts := strings.Split(string(data), ".")
	if len(parts) != 4 || parts[0] != s.ID || parts[2] != statisticsShape(r) {
		return 0, fmt.Errorf("cursor query arguments changed")
	}
	body := strings.Join(parts[:3], ".")
	mac := hmac.New(sha256.New, []byte(s.Secret))
	mac.Write([]byte(body))
	signature, err := hex.DecodeString(parts[3])
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return 0, fmt.Errorf("invalid cursor signature")
	}
	var position int
	if _, err = fmt.Sscanf(parts[1], "%d", &position); err != nil {
		return 0, fmt.Errorf("invalid cursor position")
	}
	return position, nil
}

func compatibleStatisticsRequest(original, r statisticsRequest, membership bool) error {
	if original.Entity == "workspace" && r.Entity == "conversation" && membership && len(r.Metrics) == 0 && len(r.Filters) == 0 && r.Query == "" && r.Scope == "conversation" {
		if r.Type != "rows" || r.Repository != "" || r.Source != "" || r.Model != "" || r.Provider != "" || r.Effort != "" || r.File != "" || r.PR != nil || r.From != "" || r.To != "" || r.Owner != "" || r.Flavor != "" || r.Version != "" || r.Outcome != "" || r.Error != "" {
			return fmt.Errorf("membership snapshot only supports IDs/coverage and workspace_ids selection")
		}
		return nil
	}
	a, b := original, r
	a.Cursor, a.SnapshotID, b.Cursor, b.SnapshotID = "", "", "", ""
	a.RetainSnapshot, b.RetainSnapshot = false, false
	a.Select, b.Select = nil, nil
	a.Aggregations, b.Aggregations = nil, nil
	a.Type, b.Type = "rows", "rows"
	a.GroupBy, b.GroupBy = nil, nil
	a.Limit, a.Budget, b.Limit, b.Budget = 0, 0, 0, 0
	if jsonText(a) != jsonText(b) {
		return fmt.Errorf("snapshot population/scope/quality/order changed; restart query")
	}
	for _, name := range r.Metrics {
		if !statContainsString(original.Metrics, name) {
			return fmt.Errorf("metric %s was not captured; restart with the required selection", name)
		}
	}
	return nil
}
