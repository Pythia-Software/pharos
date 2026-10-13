package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"
)

// Names are resolved when their inputs are written, never on a Library read.
// Source titles remain separate from the displayed title, so a later sync
// cannot mistake a borrowed or authored label for the provider's own name.
const workspaceNamesVersion = "workspace-names-v1/" + authorshipVersion

func meaningfulWorkspaceTitle(title, sourceID string) bool {
	title = strings.TrimSpace(title)
	switch strings.ToLower(title) {
	case "", "untitled", "untitled work", "new chat", "new conversation", "new session", "new thread":
		return false
	}
	if title == sourceID {
		return false
	}
	for _, label := range []string{"Codex", "Claude", "Conductor", "ChatGPT", "Antigravity", "Antigravity CLI", "Antigravity IDE"} {
		if title == label+" "+short(sourceID) {
			return false
		}
	}
	return true
}

// authoredWorkspaceTitle uses only spans the shared classifier called typed.
// Keep a compact, deterministic excerpt rather than synthesizing new words.
func authoredWorkspaceTitle(text string, spans []authorSpan) string {
	parts := []string{}
	for _, span := range spans {
		if span.Category == spanTyped && span.Start >= 0 && span.End >= span.Start && span.End <= len(text) {
			parts = append(parts, text[span.Start:span.End])
		}
	}
	words := strings.Fields(strings.Join(parts, " "))
	if len(words) == 0 {
		return ""
	}
	limit := min(len(words), 12)
	for limit > 1 && utf8.RuneCountInString(strings.Join(words[:limit], " ")) > 80 {
		limit--
	}
	title := clipText(strings.Join(words[:limit], " "), 80)
	if limit < len(words) && !strings.HasSuffix(title, "…") {
		title += "…"
	}
	return title
}

// Keep a displayed fallback through upsert. Resolution runs after the record's
// conversations and identity links are in place, in the same transaction.
func workspaceTitleForIngest(tx *sql.Tx, record WorkspaceRecord, sourceTitle string) (string, error) {
	var title string
	err := tx.QueryRow("SELECT title FROM workspaces WHERE source_kind=? AND source_account=? AND source_id=?", record.SourceKind, record.Account, record.SourceID).Scan(&title)
	if err == sql.ErrNoRows {
		return sourceTitle, nil
	}
	return title, err
}

func recordWorkspaceSourceTitle(tx *sql.Tx, id string, record WorkspaceRecord) error {
	scope := firstString(record.Metadata["title_scope"])
	title := defaultString(record.Title, record.SourceID)
	// A newer rollout-only capture may lack the optional naming stores. Keep
	// a known name when that evidence is absent; an observed clear still wins.
	if record.SourceKind == "codex" && !meaningfulWorkspaceTitle(title, record.SourceID) && record.Metadata["title_cleared"] != true {
		var previous string
		err := tx.QueryRow("SELECT source_title FROM workspace_names WHERE workspace_id=?", id).Scan(&previous)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if meaningfulWorkspaceTitle(previous, record.SourceID) {
			title = previous
		}
	}
	_, err := tx.Exec(`INSERT INTO workspace_names(workspace_id,source_title,source_scope,display_title)
		SELECT id,?,?,title FROM workspaces WHERE id=?
		ON CONFLICT(workspace_id) DO UPDATE SET source_title=excluded.source_title,source_scope=excluded.source_scope`,
		title, nilIfEmpty(scope), id)
	return err
}

// A title-only source edit does not change the classifier's inputs. Existing
// per-conversation fingerprints include messages and classification metadata;
// membership and location also affect mirrors and headless-run attribution.
func invalidateWorkspaceAuthorship(tx *sql.Tx, id string, merged bool) error {
	rows, err := queryMaps(tx, `SELECT c.id,c.parent_id,c.harness,c.origin_host_id,d.fingerprint
		FROM conversations c LEFT JOIN conversation_derivation_inputs d ON d.conversation_id=c.id
		WHERE c.workspace_id=? AND `+currentGroupConversation("c")+` ORDER BY c.id`, id)
	if err != nil {
		return err
	}
	var location, prior string
	if err := tx.QueryRow(`SELECT COALESCE(w.location,''),COALESCE(n.authorship_input_hash,'')
		FROM workspaces w JOIN workspace_names n ON n.workspace_id=w.id WHERE w.id=?`, id).Scan(&location, &prior); err != nil {
		return err
	}
	fingerprint := hashBytes([]byte(jsonText([]any{location, rows})))
	if fingerprint == prior && !merged {
		return nil
	}
	if _, err := tx.Exec("UPDATE workspace_names SET authorship_input_hash=? WHERE workspace_id=?", fingerprint, id); err != nil {
		return err
	}
	return bumpAuthorshipInputs(tx)
}

type workspaceName struct {
	id, kind, scope, nativeID, current, source, display, override, authored, message, sentAt string
	origin, from                                                                             string
}

type workspaceNameEdge struct{ left, right string }

func workspaceNameEdges(tx *sql.Tx, ids []string) ([]workspaceNameEdge, error) {
	query := `SELECT DISTINCT a.workspace_id left_workspace,b.workspace_id right_workspace FROM conversation_identity_links l
		JOIN conversations a ON a.id=l.left_id JOIN conversations b ON b.id=l.right_id
		WHERE l.relationship='native-alias' AND a.workspace_id<>b.workspace_id`
	args := []any{}
	if ids != nil {
		if len(ids) == 0 {
			return nil, nil
		}
		query += " AND (a.workspace_id IN (" + placeholders(len(ids)) + ") OR b.workspace_id IN (" + placeholders(len(ids)) + "))"
		for range 2 {
			for _, id := range ids {
				args = append(args, id)
			}
		}
	}
	rows, err := queryMaps(tx, query, args...)
	if err != nil {
		return nil, err
	}
	edges := make([]workspaceNameEdge, 0, len(rows))
	for _, row := range rows {
		edges = append(edges, workspaceNameEdge{firstString(row["left_workspace"]), firstString(row["right_workspace"])})
	}
	return edges, nil
}

// refreshWorkspaceNames resolves a bounded work and its direct mirrors, or
// the whole catalog for a migration/classification pass. Only original source
// names are borrowed; derived names cannot bounce around an identity graph.
func refreshWorkspaceNames(tx *sql.Tx, ids []string) error {
	targets := map[string]bool{}
	for _, id := range ids {
		targets[id] = true
	}
	edges, err := workspaceNameEdges(tx, ids)
	if err != nil {
		return err
	}
	for _, edge := range edges {
		targets[edge.left], targets[edge.right] = true, true
	}
	if ids != nil {
		ids = sortedNameIDs(targets)
		edges, err = workspaceNameEdges(tx, ids)
		if err != nil {
			return err
		}
	}
	needed := map[string]bool{}
	for id := range targets {
		needed[id] = true
	}
	for _, edge := range edges {
		needed[edge.left], needed[edge.right] = true, true
	}
	seed := `INSERT OR IGNORE INTO workspace_names(workspace_id,source_title,display_title) SELECT id,title,title FROM workspaces`
	seedArgs := []any{}
	if ids != nil {
		all := sortedNameIDs(needed)
		if len(all) == 0 {
			return nil
		}
		seed += " WHERE id IN (" + placeholders(len(all)) + ")"
		for _, id := range all {
			seedArgs = append(seedArgs, id)
		}
	}
	if _, err := tx.Exec(seed, seedArgs...); err != nil {
		return err
	}
	query := `SELECT w.id,w.source_kind,w.source_id,w.title,n.source_title,n.source_scope,n.display_title,n.display_source,n.display_workspace_id,n.override_title,
		CASE WHEN m.content_hash=n.authored_hash AND n.authored_version=? THEN n.authored_title END authored_title,
		n.authored_message_id,n.authored_at FROM workspaces w JOIN workspace_names n ON n.workspace_id=w.id
		LEFT JOIN messages m ON m.id=n.authored_message_id`
	args := []any{workspaceNamesVersion}
	if ids != nil {
		all := sortedNameIDs(needed)
		if len(all) == 0 {
			return nil
		}
		query += " WHERE w.id IN (" + placeholders(len(all)) + ")"
		for _, id := range all {
			args = append(args, id)
		}
	}
	rows, err := queryMaps(tx, query, args...)
	if err != nil {
		return err
	}
	names := map[string]workspaceName{}
	for _, row := range rows {
		n := workspaceName{id: firstString(row["id"]), kind: firstString(row["source_kind"]), scope: firstString(row["source_scope"]), nativeID: firstString(row["source_id"]),
			current: firstString(row["title"]), source: firstString(row["source_title"]), display: firstString(row["display_title"]),
			origin: firstString(row["display_source"]), from: firstString(row["display_workspace_id"]),
			override: firstString(row["override_title"]), authored: firstString(row["authored_title"]), message: firstString(row["authored_message_id"]), sentAt: firstString(row["authored_at"])}
		// An explicit catalog rename wins over every automatic name, including
		// source renames. The previous applied title makes it distinguishable.
		if n.current != n.display && meaningfulWorkspaceTitle(n.current, n.nativeID) {
			n.override = n.current
		}
		names[n.id] = n
		if ids == nil {
			targets[n.id] = true
		}
	}
	neighbors := map[string][]string{}
	for _, edge := range edges {
		neighbors[edge.left] = append(neighbors[edge.left], edge.right)
		neighbors[edge.right] = append(neighbors[edge.right], edge.left)
	}
	for _, id := range sortedNameIDs(targets) {
		n, ok := names[id]
		if !ok {
			continue
		}
		title, origin, from := strings.TrimSpace(n.source), "source", id
		conflicts := 0
		broadFallback := n.kind == "conductor" && n.scope == "workspace"
		if !meaningfulWorkspaceTitle(title, n.nativeID) || broadFallback {
			title, origin, from = defaultString(strings.TrimSpace(n.source), n.nativeID), "fallback", ""
			best, candidates := 99, map[string]string{}
			if broadFallback && meaningfulWorkspaceTitle(title, n.nativeID) {
				// The adapter used a workspace name because the session's own
				// title was a placeholder. A specific linked session can improve it.
				best, candidates[title] = 2, id
				origin, from = "source", id
			}
			for _, otherID := range neighbors[id] {
				other := names[otherID]
				candidate := strings.TrimSpace(firstString(other.override, other.source))
				if !meaningfulWorkspaceTitle(candidate, other.nativeID) {
					continue
				}
				rank := 0
				if other.scope == "workspace" || other.scope == "task" || other.kind == "tl1" || other.kind == "tl1-export" {
					rank = 2
				} else if other.kind == "conductor" {
					rank = 1
				}
				if rank < best {
					best, candidates = rank, map[string]string{}
				}
				if rank == best {
					if prior := candidates[candidate]; prior == "" || otherID < prior {
						candidates[candidate] = otherID
					}
				}
			}
			if len(candidates) > 1 {
				conflicts = len(candidates)
			}
			previousTypedValid := n.origin != "typed" || names[n.from].authored == n.display
			if len(candidates) == 1 {
				for candidate, otherID := range candidates {
					title, origin, from = candidate, "linked", otherID
					if otherID == id {
						origin = "source"
					}
				}
			} else if len(candidates) > 1 && n.current == n.display && meaningfulWorkspaceTitle(n.display, n.nativeID) && previousTypedValid {
				// Conflicting equally specific names keep the previous choice.
				title, origin, from = n.display, n.origin, n.from
			} else {
				// Authorship is computed for a mirror group's representative.
				// Its cached first typed words can name the same native session.
				chosen := n
				for _, otherID := range neighbors[id] {
					other := names[otherID]
					if other.authored != "" && (chosen.authored == "" || other.sentAt < chosen.sentAt || other.sentAt == chosen.sentAt && other.message < chosen.message) {
						chosen = other
					}
				}
				if chosen.authored != "" {
					title, origin, from = chosen.authored, "typed", chosen.id
				}
			}
		}
		if n.override != "" {
			title, origin, from = n.override, "override", id
		}
		if _, err := tx.Exec(`UPDATE workspace_names SET display_title=?,display_source=?,display_workspace_id=?,override_title=?,display_conflicts=?
			WHERE workspace_id=? AND (display_title<>? OR display_source<>? OR COALESCE(display_workspace_id,'')<>? OR COALESCE(override_title,'')<>? OR display_conflicts<>?)`,
			title, origin, nilIfEmpty(from), nilIfEmpty(n.override), conflicts, id, title, origin, from, n.override, conflicts); err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE workspaces SET title=? WHERE id=? AND title<>?", title, id, title); err != nil {
			return err
		}
	}
	return nil
}

func sortedNameIDs(ids map[string]bool) []string {
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

// Cache the first classified human words at the end of the existing pass.
// No second classifier, per-request text scan, or raw first-prompt fallback.
func cacheAuthoredWorkspaceNames(tx *sql.Tx, results []authorResult) error {
	if _, err := tx.Exec(`INSERT OR IGNORE INTO workspace_names(workspace_id,source_title,display_title)
		SELECT id,title,title FROM workspaces`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE workspace_names SET authored_title=NULL,authored_message_id=NULL,authored_hash=NULL,authored_at=NULL,authored_version=?`, workspaceNamesVersion); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, result := range results {
		input := result.Input
		if seen[input.WorkspaceID] {
			continue
		}
		title := authoredWorkspaceTitle(input.Text, result.Spans)
		if title == "" {
			continue
		}
		seen[input.WorkspaceID] = true
		if _, err := tx.Exec(`UPDATE workspace_names SET authored_title=?,authored_message_id=?,authored_hash=?,authored_at=? WHERE workspace_id=?`,
			title, input.ID, hashBytes([]byte(input.Text)), formatTime(input.SentAt), input.WorkspaceID); err != nil {
			return err
		}
	}
	return refreshWorkspaceNames(tx, nil)
}

// A one-time upgrade uses the persisted classifier spans. It does not rerun
// the expensive authorship analysis just to fill the naming cache.
func (c *Catalog) ensureWorkspaceNames() error {
	version, err := c.metaValue(context.Background(), "workspace_names_version")
	if err != nil || version == workspaceNamesVersion {
		return err
	}
	want, built, err := c.authorshipGenerations()
	if err != nil {
		return err
	}
	return c.writeTransaction(context.Background(), "initialize workspace names", func(tx *sql.Tx) error {
		rows := []map[string]any{}
		if want == built {
			// Stale classifications cannot safely be applied to edited text.
			rows, err = queryMaps(tx, `SELECT a.workspace_id,a.message_id,a.sent_at,a.spans_json,m.text
				FROM message_authorship a JOIN messages m ON m.id=a.message_id WHERE a.typed_words>0 ORDER BY a.sent_at,a.message_id`)
			if err != nil {
				return err
			}
		}
		results := make([]authorResult, 0, len(rows))
		for _, row := range rows {
			at, ok := parseTime(firstString(row["sent_at"]))
			var spans []authorSpan
			if !ok || json.Unmarshal([]byte(firstString(row["spans_json"])), &spans) != nil {
				continue
			}
			results = append(results, authorResult{Input: authorInput{ID: firstString(row["message_id"]), WorkspaceID: firstString(row["workspace_id"]), Text: firstString(row["text"]), SentAt: at}, Spans: spans})
		}
		if err := cacheAuthoredWorkspaceNames(tx, results); err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO meta(key,value) VALUES('workspace_names_version',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, workspaceNamesVersion)
		return err
	})
}
