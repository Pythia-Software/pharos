package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

type queryObjectReference struct {
	ObjectType string            `json:"objectType"`
	Key        map[string]string `json:"key"`
}
type queryResolvedReference struct {
	Reference queryObjectReference `json:"reference"`
	Label     string               `json:"label,omitempty"`
	Href      string               `json:"href,omitempty"`
}

// Relationship types are a host allowlist, never SQL identifiers from a client.
func (s *Server) queryObjectReferences(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, errors.New("method not allowed"), 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1_000_000)
	var input struct {
		References []queryObjectReference `json:"references"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, err, 400)
		return
	}
	if err := requireQueryEOF(decoder); err != nil {
		writeError(w, err, 400)
		return
	}
	if len(input.References) > 100 {
		writeError(w, errors.New("at most 100 object references are allowed"), 400)
		return
	}
	// Validate the whole batch before any read.
	for _, ref := range input.References {
		if (ref.ObjectType != "pharos_library" && ref.ObjectType != "pharos_conversations") || len(ref.Key) != 1 || ref.Key["id"] == "" || len(ref.Key["id"]) > 10_000 {
			writeError(w, errors.New("unknown object type or invalid reference key"), 400)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTableDeadline)
	defer cancel()
	tx, err := s.Catalog.reader().BeginTx(ctx, nil)
	if err != nil {
		writeError(w, err, 500)
		return
	}
	defer tx.Rollback()
	results := []queryResolvedReference{}
	seen := map[string]bool{}
	for _, ref := range input.References {
		key := ref.ObjectType + "\x00" + ref.Key["id"]
		if seen[key] {
			continue
		}
		seen[key] = true
		result := queryResolvedReference{Reference: ref}
		var title, workspace string
		if ref.ObjectType == "pharos_library" {
			err = tx.QueryRowContext(ctx, `SELECT COALESCE(title,'') FROM workspaces WHERE id=?`, ref.Key["id"]).Scan(&title)
			workspace = ref.Key["id"]
		} else {
			err = tx.QueryRowContext(ctx, `SELECT COALESCE(w.title,''),c.workspace_id FROM conversations c JOIN workspaces w ON w.id=c.workspace_id WHERE c.id=?`, ref.Key["id"]).Scan(&title, &workspace)
		}
		if errors.Is(err, sql.ErrNoRows) {
			results = append(results, result)
			continue
		}
		if err != nil {
			writeError(w, err, 500)
			return
		}
		if title == "" {
			title = workspace
		}
		result.Label = title
		result.Href = "/work/" + url.PathEscape(workspace)
		if ref.ObjectType == "pharos_conversations" {
			result.Label = fmt.Sprintf("%s · conversation", title)
			result.Href += "/conversation/" + url.PathEscape(ref.Key["id"])
		}
		results = append(results, result)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, results, 200)
}
