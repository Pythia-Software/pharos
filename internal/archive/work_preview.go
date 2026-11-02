package archive

import (
	"database/sql"
	"errors"
	"net/http"
	"sort"
)

// A link preview summarizes a workspace, or one conversation in it, from the
// cached delegation snapshot. The linking agent supplies the title and
// context, so the preview carries only what identifies the run: where and
// when it ran, which models, how many agents, and the explorer's accounting.
type workPreviewModel struct {
	Model    string  `json:"model"`
	Provider string  `json:"provider"`
	Sessions int     `json:"sessions"`
	Tokens   float64 `json:"tokens"`
}
type workPreviewScope struct {
	Start    *float64                    `json:"start"`
	End      *float64                    `json:"end"`
	Sessions int                         `json:"sessions"`
	Models   []workPreviewModel          `json:"models"`
	Totals   map[string]delegationMetric `json:"totals"`
}
type workPreview struct {
	Repository   string                 `json:"repository,omitempty"`
	Branch       string                 `json:"branch,omitempty"`
	Metrics      []delegationMetricSpec `json:"metrics"`
	Workspace    workPreviewScope       `json:"workspace"`
	Conversation *workPreviewScope      `json:"conversation,omitempty"`
}

// Models in a scope, most tokens first; sessions without a model are omitted.
func workPreviewModels(nodes []*delegationNode) []workPreviewModel {
	byModel := map[string]*workPreviewModel{}
	for _, n := range nodes {
		if n.Model == "" {
			continue
		}
		m := byModel[n.Provider+"\x00"+n.Model]
		if m == nil {
			m = &workPreviewModel{Model: n.Model, Provider: n.Provider}
			byModel[n.Provider+"\x00"+n.Model] = m
		}
		m.Sessions++
		if t := n.Self["tokens"].Value; t != nil {
			m.Tokens += *t
		}
	}
	models := make([]workPreviewModel, 0, len(byModel))
	for _, m := range byModel {
		models = append(models, *m)
	}
	sort.Slice(models, func(i, j int) bool {
		a, b := models[i], models[j]
		if a.Tokens != b.Tokens {
			return a.Tokens > b.Tokens
		}
		if a.Sessions != b.Sessions {
			return a.Sessions > b.Sessions
		}
		return a.Model < b.Model
	})
	return models
}

func (s *Server) getWorkPreview(w http.ResponseWriter, r *http.Request, workspace string) {
	snapshot, err := s.Catalog.delegationSnapshot(r.Context(), workspace)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	if snapshot == nil {
		writeJSON(w, map[string]any{"error": "workspace not found"}, 404)
		return
	}
	preview := workPreview{Metrics: delegationMetrics}
	var repository, branch sql.NullString
	err = s.Catalog.reader().QueryRowContext(r.Context(), `SELECT r.display_name,w.branch
 FROM workspaces w LEFT JOIN repositories r ON r.id=w.repository_id WHERE w.id=?`, workspace).Scan(&repository, &branch)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, map[string]any{"error": "workspace not found"}, 404)
		return
	}
	if err != nil {
		writeError(w, err, 500)
		return
	}
	preview.Repository, preview.Branch = repository.String, branch.String
	included := []*delegationNode{}
	for _, n := range snapshot.Nodes {
		if !n.ContextOnly {
			included = append(included, n)
		}
	}
	preview.Workspace = workPreviewScope{Start: snapshot.Start, End: snapshot.End, Sessions: len(included), Models: workPreviewModels(included), Totals: snapshot.Totals}
	if id := r.URL.Query().Get("conversation"); id != "" {
		// The conversation's own root session: the first of its sessions not
		// owned by another session in the same conversation. Its subtree
		// includes the agents it delegated to.
		for _, n := range included {
			if parent := snapshot.ByID[n.ParentID]; n.ConversationID != id || parent != nil && parent.ConversationID == id {
				continue
			}
			subtree := append([]*delegationNode{n}, delegationDescendants(n)...)
			preview.Conversation = &workPreviewScope{Start: n.SubtreeStart, End: n.SubtreeEnd, Sessions: len(subtree), Models: workPreviewModels(subtree), Totals: n.All}
			break
		}
		if preview.Conversation == nil {
			writeJSON(w, map[string]any{"error": "conversation not found in this workspace"}, 404)
			return
		}
	}
	writeJSON(w, preview, 200)
}
