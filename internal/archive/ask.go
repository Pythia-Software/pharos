package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

type askSettings struct {
	Enabled        bool         `json:"enabled"`
	ShowInvitation bool         `json:"showInvitation"`
	Selection      askSelection `json:"selection"`
}
type askCard struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Kind        string `json:"kind"`
	Href        string `json:"href"`
}
type askTurn struct {
	ID            string            `json:"id"`
	Prompt        string            `json:"prompt"`
	CLI           string            `json:"cli"`
	Model         string            `json:"model"`
	ModelLabel    string            `json:"modelLabel"`
	Effort        string            `json:"effort"`
	Status        string            `json:"status"`
	Message       string            `json:"message"`
	Reply         string            `json:"reply"`
	ElapsedMs     int64             `json:"elapsedMs"`
	StartedAt     int64             `json:"startedAt"`
	Cards         []askCard         `json:"cards"`
	Clarification *askClarification `json:"clarification,omitempty"`
}
type askSession struct {
	HostID             string       `json:"hostID"`
	QuestionCount      int          `json:"questionCount,omitempty"`
	ID                 string       `json:"id"`
	CreatedAt          int64        `json:"createdAt"`
	UpdatedAt          int64        `json:"updatedAt"`
	Selection          askSelection `json:"selection"`
	Draft              string       `json:"draft"`
	ClarificationDraft string       `json:"clarificationDraft,omitempty"`
	Conversation       []askTurn    `json:"conversation"`
}
type askVisit struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	At    int64  `json:"at"`
}
type askActive struct {
	cancel context.CancelFunc
	done   chan struct{}
}
type askState struct {
	discovering map[string]context.CancelFunc
	mu          sync.Mutex
	active      map[string]askActive
	models      map[string]askProvider
}

var askID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)
var askRequestTimeout = 3 * time.Minute
var askVisitLimit = 1000

func askDecode(body map[string]any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
func (s *Server) askSettings(ctx context.Context) (askSettings, error) {
	config := askSettings{ShowInvitation: true}
	var raw string
	err := s.Catalog.reader().QueryRowContext(ctx, "SELECT value FROM ask_settings WHERE host_id=?", currentHost().ID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return config, nil
	}
	if err != nil {
		return config, err
	}
	err = json.Unmarshal([]byte(raw), &config)
	return config, err
}
func (s *Server) saveAskSettings(ctx context.Context, config askSettings) error {
	return s.Catalog.writeTransaction(ctx, "ask-settings", func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO ask_settings(host_id,value) VALUES(?,?) ON CONFLICT(host_id) DO UPDATE SET value=excluded.value", currentHost().ID, jsonText(config))
		return err
	})
}
func (s *Server) askSession(ctx context.Context, id string) (askSession, error) {
	var session askSession
	var raw string
	err := s.Catalog.reader().QueryRowContext(ctx, "SELECT value FROM ask_sessions WHERE id=?", id).Scan(&raw)
	if err != nil {
		return session, err
	}
	err = json.Unmarshal([]byte(raw), &session)
	return session, err
}
func (s *Server) saveAskSession(ctx context.Context, session askSession, index bool) error {
	if session.HostID == "" {
		session.HostID = currentHost().ID
	}
	return s.Catalog.writeTransaction(ctx, "ask-session", func(tx *sql.Tx) error {
		result, err := tx.Exec("INSERT INTO ask_sessions(id,host_id,value,updated_at) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at ", session.ID, session.HostID, jsonText(session), session.UpdatedAt)
		if err != nil {
			return err
		}
		// Index only the actual questions and replies. Private visit/lookup context
		// is never copied into the general archive or its MCP search responses.
		if count, _ := result.RowsAffected(); count == 0 {
			return fmt.Errorf("chat could not be saved")
		}
		if !index || len(session.Conversation) == 0 {
			return nil
		}
		workspaceID, err := upsertWorkspace(tx, WorkspaceRecord{SourceID: session.ID, SourceKind: "pharos", Account: session.HostID, Title: session.Conversation[0].Prompt, ActivityAt: time.UnixMilli(session.UpdatedAt).UTC().Format(timeLayout)}, "", map[string]any{}, false)
		if err != nil {
			return err
		}
		convID, err := upsertConversation(tx, workspaceID, "", ConversationRecord{NativeID: "ask-" + session.ID, Provider: strings.ToLower(session.Selection.Provider), Model: session.Selection.Model, Effort: strings.ToLower(session.Selection.Effort), Account: "pharos:" + session.HostID, Origin: "Ask Pharos", Harness: "pharos", Coverage: "complete", StartedAt: time.UnixMilli(session.CreatedAt).UTC().Format(timeLayout), EndedAt: time.UnixMilli(session.UpdatedAt).UTC().Format(timeLayout)}, session.HostID)
		if err != nil {
			return err
		}
		for index, turn := range session.Conversation {
			text := turn.Reply
			if turn.Clarification != nil {
				text += "\n" + turn.Clarification.Question
				for i, option := range turn.Clarification.Options {
					text += fmt.Sprintf("\n%c. %s", 'A'+i, option)
				}
			}
			for _, card := range turn.Cards {
				text += "\n" + card.Title + ": " + card.Description + "\n" + card.Href
			}
			for role, message := range map[string]string{"user": turn.Prompt, "assistant": text} {
				if message == "" {
					continue
				}
				order := index * 2
				if role == "assistant" {
					order++
				}
				messageID, changed, err := upsertMessage(tx, convID, MessageRecord{NativeID: turn.ID + "-" + role, Role: role, Kind: "message", Text: message, Model: turn.Model, Effort: strings.ToLower(turn.Effort), SourceOrder: order, CreatedAt: time.UnixMilli(turn.StartedAt).UTC().Format(timeLayout), Selected: true})
				if err != nil {
					return err
				}
				if changed {
					if err = replaceMessageFTS(tx, messageID); err != nil {
						return err
					}
				}
			}
		}
		return upsertConversationDocument(tx, convID)
	})
}

// Return summaries rather than materializing every message in every chat.
func (s *Server) askSessions(ctx context.Context, query string, offset int) ([]askSession, error) {
	rows, err := s.Catalog.reader().QueryContext(ctx, `SELECT id,updated_at,
 json_extract(value,'$.createdAt'), json_extract(value,'$.selection'),
 COALESCE(json_extract(value,'$.draft'),''),
 COALESCE(json_extract(value,'$.conversation[0].prompt'),''),
 json_array_length(json_extract(value,'$.conversation'))
 FROM ask_sessions WHERE (?='' OR instr(lower(value),lower(?))>0)
 ORDER BY updated_at DESC,id LIMIT 100 OFFSET ?`, query, query, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []askSession{}
	for rows.Next() {
		var session askSession
		var selection, title string
		if err = rows.Scan(&session.ID, &session.UpdatedAt, &session.CreatedAt, &selection, &session.Draft, &title, &session.QuestionCount); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(selection), &session.Selection); err != nil {
			return nil, err
		}
		session.Conversation = []askTurn{}
		if title != "" {
			session.Conversation = append(session.Conversation, askTurn{Prompt: title, CLI: session.Selection.Provider})
		}
		result = append(result, session)
	}
	return result, rows.Err()
}
func (s *Server) askProvider(ctx context.Context, name string) (askProvider, error) {
	config, err := s.askSettings(ctx)
	if err != nil {
		return askProvider{}, err
	}
	if !config.Enabled {
		return askProvider{}, fmt.Errorf("enable Ask Pharos before using a CLI")
	}
	for _, p := range askInstalled() {
		if p.ID != name {
			continue
		}
		if cached, ok := s.ask.models[name]; ok && cached.path == p.path && time.Since(cached.discoveredAt) < 5*time.Minute {
			return cached, nil
		}
		discoveryCtx, cancel := context.WithCancel(ctx)
		key := randomToken()
		if s.ask.discovering == nil {
			s.ask.discovering = map[string]context.CancelFunc{}
		}
		s.ask.discovering[key] = cancel
		s.ask.mu.Unlock()
		models, discoveryErr := askDiscoverModels(discoveryCtx, p)
		cancel()
		s.ask.mu.Lock()
		delete(s.ask.discovering, key)
		p.Models, err = models, discoveryErr
		config, settingsErr := s.askSettings(ctx)
		if settingsErr != nil {
			return p, settingsErr
		}
		if !config.Enabled {
			return p, fmt.Errorf("Ask Pharos was disabled")
		}

		if err != nil {
			return p, err
		}
		if len(p.Models) == 0 {
			return p, fmt.Errorf("%s returned no available models; update and authenticate it", name)
		}
		if s.ask.models == nil {
			s.ask.models = map[string]askProvider{}
		}
		p.discoveredAt = time.Now()
		selection := askDefaultSelection(p)
		p.DefaultSelection = &selection
		s.ask.models[name] = p
		return p, nil
	}
	return askProvider{}, fmt.Errorf("%s is no longer available on this Mac", name)
}
func (s *Server) askHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, fmt.Errorf("GET or POST required"), 405)
		return
	}
	ctx := r.Context()
	var body map[string]any
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			writeError(w, err, 400)
			return
		}
	}
	s.ask.mu.Lock()
	defer s.ask.mu.Unlock()
	fail := func(err error) { writeError(w, err, 400) }
	switch r.URL.Path {
	case "/api/ask/status":
		if r.Method != http.MethodGet {
			fail(fmt.Errorf("GET required"))
			return
		}
		settings, err := s.askSettings(ctx)
		if err != nil {
			fail(err)
			return
		}
		writeJSON(w, map[string]any{"settings": settings, "providers": askInstalled()}, 200)
	case "/api/ask/settings":
		if r.Method != http.MethodPost {
			fail(fmt.Errorf("POST required"))
			return
		}
		settings, err := s.askSettings(ctx)
		if err != nil {
			fail(err)
			return
		}
		if v, ok := body["enabled"].(bool); ok {
			if v && len(askInstalled()) == 0 {
				fail(fmt.Errorf("install and authenticate a supported CLI first"))
				return
			}
			settings.Enabled = v
		}
		if v, ok := body["showInvitation"].(bool); ok {
			settings.ShowInvitation = v
		}
		// Consent is a dedicated write, not a general UI preference.
		if settings.Enabled {
			old, _ := s.askSettings(ctx)
			if !old.Enabled && body["consent"] != true {
				fail(fmt.Errorf("Ask Pharos requires explicit consent"))
				return
			}
		}
		if raw, ok := body["selection"].(map[string]any); ok {
			var selection askSelection
			if err := askDecode(raw, &selection); err != nil {
				fail(err)
				return
			}
			p, err := s.askProvider(ctx, selection.Provider)
			if err != nil {
				fail(err)
				return
			}
			if !askValidSelection(p, selection) {
				fail(fmt.Errorf("model or effort is unavailable"))
				return
			}
			settings.Selection = selection
		}
		if err = s.saveAskSettings(ctx, settings); err != nil {
			fail(err)
			return
		}
		if !settings.Enabled {
			for _, active := range s.ask.active {
				active.cancel()
			}
			s.ask.models = nil
			for _, cancel := range s.ask.discovering {
				cancel()
			}
		}
		writeJSON(w, settings, 200)
	case "/api/ask/models":
		if r.Method != http.MethodGet {
			fail(fmt.Errorf("GET required"))
			return
		}
		if r.URL.Query().Get("refresh") == "1" {
			delete(s.ask.models, r.URL.Query().Get("provider"))
		}
		p, err := s.askProvider(ctx, r.URL.Query().Get("provider"))
		if err != nil {
			fail(err)
			return
		}
		writeJSON(w, p, 200)
	case "/api/ask/sessions":
		if r.Method != http.MethodGet {
			fail(fmt.Errorf("GET required"))
			return
		}
		sessions, err := s.askSessions(ctx, r.URL.Query().Get("q"), clamp(queryInt(r, "offset", 0), 0, 100000))
		if err != nil {
			fail(err)
			return
		}
		writeJSON(w, map[string]any{"sessions": sessions}, 200)
	case "/api/ask/session":
		id := r.URL.Query().Get("id")
		if r.Method == http.MethodPost {
			id = firstString(body["id"])
		}
		if !askID.MatchString(id) {
			fail(fmt.Errorf("invalid chat ID"))
			return
		}
		session, err := s.askSession(ctx, id)
		if r.Method == http.MethodGet {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, err, 404)
				return
			}
			if err != nil {
				fail(err)
				return
			}
			if _, running := s.ask.active[id]; !running {
				for i := range session.Conversation {
					if session.Conversation[i].Status == "loading" {
						session.Conversation[i].Status = "canceled"
						session.Conversation[i].Reply = "Request stopped. You can retry it below."
					}
				}
			}
			writeJSON(w, session, 200)
			return
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			fail(err)
			return
		}
		draft := firstString(body["draft"])
		if len(draft) > 12000 {
			fail(fmt.Errorf("draft is too long"))
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			session = askSession{ID: id, HostID: currentHost().ID, CreatedAt: time.Now().UnixMilli(), Conversation: []askTurn{}}
			if raw, ok := body["selection"].(map[string]any); ok {
				_ = askDecode(raw, &session.Selection)
			}
		}
		if len(session.Conversation) == 0 {
			if raw, ok := body["selection"].(map[string]any); ok {
				_ = askDecode(raw, &session.Selection)
			}
		}
		session.Draft = draft
		if raw, ok := body["clarificationDraft"]; ok {
			text, valid := raw.(string)
			if !valid || len(text) > 12000 {
				fail(fmt.Errorf("clarification draft is too long or invalid"))
				return
			}
			session.ClarificationDraft = text
		}
		session.UpdatedAt = time.Now().UnixMilli()
		if err = s.saveAskSession(ctx, session, false); err != nil {
			fail(err)
			return
		}
		writeJSON(w, session, 200)
	case "/api/ask/cancel":
		if r.Method != http.MethodPost {
			fail(fmt.Errorf("POST required"))
			return
		}
		if active, ok := s.ask.active[firstString(body["id"])]; ok {
			active.cancel()
			s.ask.mu.Unlock()
			select {
			case <-active.done:
			case <-ctx.Done():
			}
			s.ask.mu.Lock()
		}
		writeJSON(w, map[string]bool{"ok": true}, 200)
	case "/api/ask/visits":
		if r.Method != http.MethodPost {
			fail(fmt.Errorf("POST required"))
			return
		}
		settings, err := s.askSettings(ctx)
		if err != nil {
			fail(err)
			return
		}
		if !settings.Enabled {
			writeJSON(w, map[string]bool{"ok": true}, 200)
			return
		}
		var input struct {
			Visits    []askVisit
			Replaces  *askVisit
			Reconcile bool
		}
		if err = askDecode(body, &input); err != nil {
			fail(err)
			return
		}
		if len(input.Visits) > 1000 {
			fail(fmt.Errorf("too many visits"))
			return
		}
		err = s.Catalog.writeTransaction(ctx, "ask-visits", func(tx *sql.Tx) error {
			// The browser history is authoritative. Reconcile its initial snapshot
			// to remove superseded entries retained by earlier versions, and replace
			// individual identities atomically as titles or query state change.
			if input.Reconcile {
				if _, err := tx.Exec("DELETE FROM ask_visits WHERE host_id=?", currentHost().ID); err != nil {
					return err
				}
			} else if old := input.Replaces; old != nil {
				if _, err := tx.Exec("DELETE FROM ask_visits WHERE host_id=? AND url=? AND at=?", currentHost().ID, old.URL, old.At); err != nil {
					return err
				}
			}
			for _, v := range input.Visits {
				if !askSafeURL(v.URL) || len(v.Title) > 500 || v.At < 1 || v.At > time.Now().Add(time.Minute).UnixMilli() {
					continue
				}
				if _, err := tx.Exec("INSERT INTO ask_visits(host_id,url,at,title) VALUES(?,?,?,?) ON CONFLICT(host_id,url,at) DO UPDATE SET title=excluded.title", currentHost().ID, v.URL, v.At, v.Title); err != nil {
					return err
				}
			}
			_, err := tx.Exec("DELETE FROM ask_visits WHERE host_id=? AND rowid NOT IN (SELECT rowid FROM ask_visits WHERE host_id=? ORDER BY at DESC LIMIT ?)", currentHost().ID, currentHost().ID, askVisitLimit)
			return err
		})
		if err != nil {
			fail(err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true}, 200)
	case "/api/ask/remove-visit":
		if r.Method != http.MethodPost {
			fail(fmt.Errorf("POST required"))
			return
		}
		var visit askVisit
		if err := askDecode(body, &visit); err != nil {
			fail(err)
			return
		}
		err := s.Catalog.writeTransaction(ctx, "ask-remove-visit", func(tx *sql.Tx) error {
			_, err := tx.Exec("DELETE FROM ask_visits WHERE host_id=? AND url=? AND at=?", currentHost().ID, visit.URL, visit.At)
			return err
		})
		if err != nil {
			fail(err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true}, 200)
	case "/api/ask/clear-visits":
		if r.Method != http.MethodPost {
			fail(fmt.Errorf("POST required"))
			return
		}
		err := s.Catalog.writeTransaction(ctx, "ask-clear-visits", func(tx *sql.Tx) error {
			_, err := tx.Exec("DELETE FROM ask_visits WHERE host_id=?", currentHost().ID)
			return err
		})
		if err != nil {
			fail(err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true}, 200)
	case "/api/ask/send":
		if r.Method != http.MethodPost {
			fail(fmt.Errorf("POST required"))
			return
		}
		// Long-running inference releases the state lock; cancellation and settings
		// remain usable. startAsk reserves this session before the lock is dropped.
		session, p, runCtx, cancel, err := s.startAsk(ctx, body)
		if err != nil {
			fail(err)
			return
		}
		s.ask.mu.Unlock()
		result, runErr := s.answerAsk(runCtx, p, session, firstString(body["timezone"]))
		cancel()
		s.ask.mu.Lock()
		fresh, readErr := s.askSession(context.WithoutCancel(ctx), session.ID)
		if readErr == nil {
			session.Draft = fresh.Draft
			session.ClarificationDraft = fresh.ClarificationDraft
		}
		turn := &session.Conversation[len(session.Conversation)-1]
		turn.ElapsedMs = time.Since(time.UnixMilli(turn.StartedAt)).Milliseconds()
		turn.Message = ""
		if runErr != nil {
			turn.Status = "error"
			turn.Reply = runErr.Error()
			if errors.Is(runErr, context.DeadlineExceeded) {
				turn.Reply = "The CLI took too long to respond. Your question is saved; you can retry it below."
			}
			if errors.Is(runErr, context.Canceled) {
				turn.Status = "canceled"
				turn.Reply = "Request canceled. Your question is still in this conversation."
			}
		} else {
			turn.Cards = result.Cards
			turn.Reply = result.Message
			turn.Clarification = result.Clarification
			turn.Status = "multiple"
			if turn.Clarification != nil {
				turn.Status = "clarification"
			} else if len(turn.Cards) == 1 {
				turn.Status = "single"
			}
		}
		session.UpdatedAt = time.Now().UnixMilli()
		saveCtx, saveCancel := context.WithTimeout(s.life.ctx, 5*time.Second)
		defer saveCancel()
		err = s.saveAskSession(saveCtx, session, true)
		close(s.ask.active[session.ID].done)
		delete(s.ask.active, session.ID)
		if err != nil {
			writeError(w, err, 500)
			return
		}
		writeJSON(w, session, 200)
	default:
		writeError(w, fmt.Errorf("not found"), 404)
	}
}
func (s *Server) startAsk(ctx context.Context, body map[string]any) (askSession, askProvider, context.Context, context.CancelFunc, error) {
	var input struct {
		ID        string
		TurnID    string
		Prompt    string
		Draft     string
		Selection askSelection
		Retry     bool
	}
	fail := func(err error) (askSession, askProvider, context.Context, context.CancelFunc, error) {
		return askSession{}, askProvider{}, nil, nil, err
	}
	if err := askDecode(body, &input); err != nil {
		return fail(err)
	}
	if !askID.MatchString(input.ID) || !askID.MatchString(input.TurnID) {
		return fail(fmt.Errorf("invalid chat or message ID"))
	}
	input.Prompt = strings.TrimSpace(input.Prompt)
	if input.Prompt == "" || len(input.Prompt) > 12000 {
		return fail(fmt.Errorf("enter a question of at most 12,000 bytes"))
	}
	if len(input.Draft) > 12000 {
		return fail(fmt.Errorf("draft is too long"))
	}
	p, err := s.askProvider(ctx, input.Selection.Provider)
	if err != nil {
		return fail(err)
	}
	if !askValidSelection(p, input.Selection) {
		return fail(fmt.Errorf("selected model or effort is no longer available"))
	}
	if len(s.ask.active) >= 2 {
		return fail(fmt.Errorf("two Ask Pharos requests are already running"))
	}
	if _, running := s.ask.active[input.ID]; running {
		return fail(fmt.Errorf("this chat is already processing a question"))
	}
	session, err := s.askSession(ctx, input.ID)
	if errors.Is(err, sql.ErrNoRows) {
		session = askSession{ID: input.ID, HostID: currentHost().ID, CreatedAt: time.Now().UnixMilli(), Selection: input.Selection, Conversation: []askTurn{}}
	} else if err != nil {
		return fail(err)
	}
	if len(session.Conversation) > 0 && session.Selection != input.Selection {
		return fail(fmt.Errorf("model selection is fixed for this chat; clear to start another"))
	}
	if len(session.Conversation) == 0 {
		session.Selection = input.Selection
	}
	for i, turn := range session.Conversation {
		if turn.ID == input.TurnID {
			if !input.Retry || i != len(session.Conversation)-1 || !slices.Contains([]string{"canceled", "error", "loading"}, turn.Status) {
				return fail(fmt.Errorf("message was already submitted; reload the chat to see its result"))
			}
			input.Prompt = turn.Prompt
			session.Conversation = session.Conversation[:i]
			break
		}
	}
	if len(session.Conversation) >= 200 {
		return fail(fmt.Errorf("start a fresh chat to continue; this chat has 200 questions"))
	}
	label := input.Selection.Model
	for _, model := range p.Models {
		if model.ID == input.Selection.Model {
			label = model.Label
		}
	}
	session.Conversation = append(session.Conversation, askTurn{ID: input.TurnID, Prompt: input.Prompt, CLI: p.ID, Model: input.Selection.Model, ModelLabel: label, Effort: input.Selection.Effort, Status: "loading", StartedAt: time.Now().UnixMilli(), Cards: []askCard{}})
	session.Draft = input.Draft
	session.ClarificationDraft = ""
	session.UpdatedAt = time.Now().UnixMilli()
	if err = s.saveAskSession(ctx, session, true); err != nil {
		return fail(err)
	}
	runCtx, cancel := context.WithTimeout(ctx, askRequestTimeout)
	stopOnShutdown := context.AfterFunc(s.life.ctx, cancel)
	finalCancel := func() { stopOnShutdown(); cancel() }
	if s.ask.active == nil {
		s.ask.active = map[string]askActive{}
	}
	s.ask.active[session.ID] = askActive{cancel: finalCancel, done: make(chan struct{})}
	return session, p, runCtx, finalCancel, nil
}

// Only local UI routes can enter visit history. Agent responses use known IDs,
// never model-provided URLs. Reject encoded traversal, credentials and controls.
func askSafeURL(raw string) bool {
	if len(raw) > 16000 || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\\\r\n\x00") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || !isUIPage(u.Path) || strings.Contains(u.Path, "..") || strings.ContainsAny(u.Path, "\\\r\n\x00") {
		return false
	}
	for key := range u.Query() {
		if strings.Contains(strings.ToLower(key), "token") {
			return false
		}
	}
	return true
}
