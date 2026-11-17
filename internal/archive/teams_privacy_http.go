package archive

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// teamsPrivacyHTTP only changes the local library. It never sends an answer or
// grants access; request consent and private input eligibility are separate.
func (s *Server) teamsPrivacyHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/teams/privacy" {
		http.NotFound(w, r)
		return
	}
	id := r.URL.Query().Get("conversation")
	if id == "" {
		writeError(w, errors.New("conversation is required"), http.StatusBadRequest)
		return
	}
	var p teamsPrivacy
	var err error
	switch r.Method {
	case http.MethodGet:
		p, err = s.Catalog.teamsPrivacy(r.Context(), id)
	case http.MethodPost:
		var body struct {
			Action          string `json:"action"`
			Private         *bool  `json:"private"`
			ClassifierInput string `json:"classifierInput"`
			Revision        *int64 `json:"revision"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		dec.DisallowUnknownFields()
		if err = dec.Decode(&body); err != nil {
			writeError(w, errors.New("invalid privacy action"), 400)
			return
		}
		if err = dec.Decode(&struct{}{}); err != io.EOF {
			writeError(w, errors.New("invalid privacy action"), 400)
			return
		}
		if body.Private == nil || body.Revision == nil {
			writeError(w, errors.New("private and revision are required"), 400)
			return
		}
		switch body.Action {
		case "mark":
			p, err = s.Catalog.setTeamsPrivate(r.Context(), id, *body.Private, body.Revision)
		case "review":
			p, err = s.Catalog.reviewTeamsClassification(r.Context(), id, body.ClassifierInput, *body.Private, body.Revision)
		default:
			writeError(w, errors.New("unknown privacy action"), 400)
			return
		}
	default:
		w.Header().Set("Allow", "GET, POST")
		writeError(w, errors.New("method not allowed"), http.StatusMethodNotAllowed)
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, errors.New("conversation not found"), 404)
		return
	}
	if err != nil {
		if errors.Is(err, errTeamsPrivacyStale) {
			writeError(w, err, http.StatusConflict)
		} else {
			writeError(w, errors.New("could not update privacy"), 500)
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, p, 200)
}
