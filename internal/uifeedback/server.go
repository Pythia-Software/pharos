package uifeedback

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type Server struct {
	Store  Store
	Auth   *AdminAuth
	Assets fs.FS
	// Event is called for authenticated GCS finalize notifications.
	Event        func(context.Context, string, string) error
	EventAccount string
	Bucket       string
	mu           sync.Mutex
	window       time.Time
	count        int
}

func response(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func readJSON(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	content, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if content != "application/json" {
		http.Error(w, "application/json required", 415)
		return false
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		http.Error(w, "request exceeds size limit", 413)
		return false
	}
	if Decode(data, target) != nil {
		http.Error(w, "invalid JSON document", 400)
		return false
	}
	return true
}
func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		http.Error(w, "submission not found", 404)
	case errors.Is(err, ErrTooLarge):
		http.Error(w, ErrTooLarge.Error(), http.StatusRequestEntityTooLarge)
	case errors.Is(err, ErrConflict):
		http.Error(w, ErrConflict.Error(), 409)
	default:
		slog.Error("feedback storage request failed", "error", err)
		http.Error(w, "storage unavailable; try again", 503)
	}
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	mux.HandleFunc("POST /v1/submissions", s.submit)
	mux.HandleFunc("GET /auth/login", s.Auth.Login)
	mux.HandleFunc("GET /auth/callback", s.Auth.Callback)
	mux.Handle("POST /auth/logout", s.Auth.Protect(http.HandlerFunc(s.Auth.Logout)))
	mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServerFS(s.Assets)))
	admin := http.NewServeMux()
	admin.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(s.Assets, "index.html")
		if err != nil {
			http.Error(w, "inbox build missing", 503)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})
	admin.HandleFunc("GET /api/submissions", s.list)
	admin.HandleFunc("GET /api/submissions/{id}", s.get)
	admin.HandleFunc("PATCH /api/submissions/{id}", s.tags)
	admin.HandleFunc("DELETE /api/submissions/{id}", s.delete)
	mux.Handle("/", s.Auth.Protect(admin))
	if s.Event != nil {
		mux.Handle("POST /events/storage", s.Auth.StorageEvents(s.EventAccount, http.HandlerFunc(s.event)))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; worker-src 'self' blob:; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}
func (s *Server) submit(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if time.Since(s.window) > time.Minute {
		s.window = time.Now()
		s.count = 0
	}
	s.count++
	limited := s.count > 30
	s.mu.Unlock()
	if limited {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many submissions; try again in a minute", 429)
		return
	}
	var submission Submission
	if !readJSON(w, r, &submission, MaxBodyBytes) {
		return
	}
	if err := submission.Validate(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	created, err := s.Store.Create(r.Context(), Document{Submission: submission, ReceivedAt: time.Now().UTC()})
	if err != nil {
		fail(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		slog.Info("feedback accepted", "event", "uifeedback.received", "submission_id", submission.ID, "application", submission.Application.Name)
	}
	response(w, status, map[string]any{"id": submission.ID, "received": true})
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.List(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ReceivedAt.After(rows[j].ReceivedAt) })
	response(w, 200, map[string]any{"submissions": rows})
}
func idOK(w http.ResponseWriter, r *http.Request) bool {
	if !validID(r.PathValue("id")) {
		http.Error(w, "invalid submission ID", 400)
		return false
	}
	return true
}
func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	if !idOK(w, r) {
		return
	}
	e, err := s.Store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if e.Annotations == nil {
		e.Annotations = []Annotation{}
	}
	response(w, 200, e)
}
func (s *Server) tags(w http.ResponseWriter, r *http.Request) {
	if !idOK(w, r) {
		return
	}
	var input struct {
		Tags     []string `json:"tags"`
		Revision string   `json:"revision"`
	}
	if !readJSON(w, r, &input, 8192) {
		return
	}
	if input.Tags == nil {
		input.Tags = []string{}
	}
	if err := ValidateTags(input.Tags); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if input.Revision == "" {
		http.Error(w, "revision required", 400)
		return
	}
	if err := s.Store.SetTags(r.Context(), r.PathValue("id"), input.Revision, input.Tags); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	if !idOK(w, r) {
		return
	}
	revision := r.Header.Get("If-Match")
	if revision == "" {
		http.Error(w, "If-Match revision required", 428)
		return
	}
	if err := s.Store.Delete(r.Context(), r.PathValue("id"), revision); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) event(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Message struct {
			Attributes        map[string]string `json:"attributes"`
			Data              string            `json:"data"`
			ID                string            `json:"messageId"`
			LegacyID          string            `json:"message_id,omitempty"`
			PublishTime       string            `json:"publishTime"`
			LegacyPublishTime string            `json:"publish_time,omitempty"`
			OrderingKey       string            `json:"orderingKey,omitempty"`
		} `json:"message"`
		Subscription    string `json:"subscription"`
		DeliveryAttempt int    `json:"deliveryAttempt,omitempty"`
	}
	if !readJSON(w, r, &input, 64<<10) {
		return
	}
	a := input.Message.Attributes
	if a["eventType"] != "OBJECT_FINALIZE" || a["bucketId"] != s.Bucket || !strings.HasPrefix(a["objectId"], objectPrefix) {
		w.WriteHeader(204)
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(a["objectId"], objectPrefix), ".json")
	if !validID(id) || a["objectGeneration"] == "" {
		http.Error(w, "invalid storage event", 400)
		return
	}
	if err := s.Event(r.Context(), id, a["objectGeneration"]); err != nil && !errors.Is(err, ErrNotFound) {
		fail(w, err)
		return
	}
	w.WriteHeader(204)
}
