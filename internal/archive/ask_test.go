package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func askTestServer(t *testing.T) *Server {
	t.Helper()
	useHost(t, "ask-host")
	catalog, config := testCatalog(t)
	s := testServer(t, config, catalog)
	override(t, &askFindCLI, func(name string) string {
		if name == "codex" {
			return "/test/codex"
		}
		return ""
	})
	override(t, &askDiscoverModels, func(ctx context.Context, p askProvider) ([]askModel, error) {
		return []askModel{{ID: "test-model", Label: "Test Model", Efforts: []string{"Default", "Low"}, Default: true}}, nil
	})
	return s
}
func askCall(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	text := ""
	if body != nil {
		text = jsonText(body)
	}
	return preferencesCall(t, s, method, "/api/ask/"+path, text)
}
func askOK(t *testing.T, s *Server, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	r := askCall(t, s, http.MethodPost, path, body)
	if r.Code != 200 {
		t.Fatalf("%s: %d %s", path, r.Code, r.Body.String())
	}
	return r
}
func askEnable(t *testing.T, s *Server) {
	t.Helper()
	askOK(t, s, "settings", map[string]any{"enabled": true, "consent": true})
}
func askInput() map[string]any {
	return map[string]any{"id": "chat-1", "turnID": "turn-1", "prompt": "Where am I wasting time?", "selection": askSelection{Provider: "Codex", Model: "test-model", Effort: "Low"}, "timezone": "America/Denver"}
}
func askReply(ids ...string) []byte {
	choices := []map[string]string{}
	for _, id := range ids {
		choices = append(choices, map[string]string{"id": id, "description": "Explore the recorded evidence."})
	}
	return []byte(jsonText(map[string]any{"message": "Here are useful places to look.", "destinations": choices, "lookup": askLookup{Kind: "none"}, "clarification": nil}))
}

func TestAskClarificationConversation(t *testing.T) {
	s := askTestServer(t)
	askEnable(t, s)
	clarification := &askClarification{Question: "By use, do you mean model tokens, API cost equivalent, or something else?", Options: []string{"Model tokens", "API cost equivalent"}}
	calls := 0
	override(t, &askRunCLI, func(ctx context.Context, provider askProvider, selection askSelection, prompt string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(jsonText(askAgentResponse{Clarification: clarification, Lookup: askLookup{Kind: "none"}})), nil
		}
		_, raw, _ := strings.Cut(prompt, "Context JSON:\n")
		var context struct {
			Conversation []struct {
				Question      string            `json:"question"`
				Clarification *askClarification `json:"clarification"`
			}
		}
		if err := json.Unmarshal([]byte(raw), &context); err != nil || len(context.Conversation) != 2 || context.Conversation[0].Clarification == nil || context.Conversation[0].Clarification.Options[1] != "API cost equivalent" || context.Conversation[1].Question != "API cost equivalent" {
			t.Errorf("clarification and answer missing from agent context: %+v %v", context, err)
		}
		return askReply("analyze"), nil
	})
	askOK(t, s, "send", askInput())
	session, err := s.askSession(t.Context(), "chat-1")
	if err != nil || len(session.Conversation) != 1 || session.Conversation[0].Status != "clarification" || session.Conversation[0].Clarification == nil || len(session.Conversation[0].Cards) != 0 || calls != 1 || len(s.ask.active) != 0 {
		t.Fatalf("clarification did not finish and wait for user: %+v %v, calls %d", session, err, calls)
	}
	askOK(t, s, "session", map[string]any{"id": "chat-1", "draft": "An unrelated draft", "clarificationDraft": "Workspace count"})
	restarted := testServer(t, s.Config(), s.Catalog)
	restored, err := restarted.askSession(t.Context(), "chat-1")
	if err != nil || restored.ClarificationDraft != "Workspace count" || restored.Conversation[0].Clarification.Question != clarification.Question {
		t.Fatalf("clarification did not survive reopening: %+v %v", restored, err)
	}
	answer := askInput()
	answer["turnID"], answer["prompt"], answer["draft"] = "turn-2", "API cost equivalent", "An unrelated draft"
	askOK(t, s, "send", answer)
	session, err = s.askSession(t.Context(), "chat-1")
	if err != nil || len(session.Conversation) != 2 || session.Conversation[1].Status != "single" || session.Conversation[1].Prompt != "API cost equivalent" || session.ClarificationDraft != "" || session.Draft != "An unrelated draft" || calls != 2 {
		t.Fatalf("clarification answer: %+v %v, calls %d", session, err, calls)
	}
	var count int
	if err = s.Catalog.reader().QueryRow("SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH ?", `"equivalent"`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("visible choices and answer were not indexed: %d %v", count, err)
	}
	if response := askCall(t, s, http.MethodPost, "send", answer); response.Code == 200 || calls != 2 {
		t.Fatal("duplicate answer issued another inference request")
	}
}

func TestAskClarificationValidation(t *testing.T) {
	s := askTestServer(t)
	for _, tc := range []struct {
		name         string
		question     string
		options      []string
		lookup       string
		destinations bool
	}{
		{name: "empty question", question: " ", options: []string{"Tokens"}},
		{name: "empty options", question: "Which usage?"},
		{name: "duplicate options", question: "Which usage?", options: []string{"Tokens", " tokens "}},
		{name: "empty option", question: "Which usage?", options: []string{" "}},
		{name: "Other is supplied by UI", question: "Which usage?", options: []string{"Tokens", "Other"}},
		{name: "too many options", question: "Which usage?", options: []string{"a", "b", "c", "d"}},
		{name: "oversize question", question: strings.Repeat("x", 601), options: []string{"Tokens"}},
		{name: "oversize option", question: "Which usage?", options: []string{strings.Repeat("x", 161)}},
		{name: "mixed lookup", question: "Which usage?", options: []string{"Tokens"}, lookup: "visits"},
		{name: "mixed destination", question: "Which usage?", options: []string{"Tokens"}, destinations: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			override(t, &askRunCLI, func(context.Context, askProvider, askSelection, string) ([]byte, error) {
				var response askAgentResponse
				_ = json.Unmarshal(askReply(), &response)
				response.Clarification = &askClarification{Question: tc.question, Options: tc.options}
				if tc.lookup != "" {
					response.Lookup.Kind = tc.lookup
				}
				if tc.destinations {
					_ = json.Unmarshal(askReply("analyze"), &response)
					response.Clarification = &askClarification{Question: tc.question, Options: tc.options}
				}
				return []byte(jsonText(response)), nil
			})
			if _, err := s.answerAsk(t.Context(), askProvider{ID: "Codex"}, askSession{}, "UTC"); err == nil {
				t.Fatal("invalid clarification accepted")
			}
		})
	}
}

func TestAskConsentAndCLIAvailability(t *testing.T) {
	s := askTestServer(t)
	discovered := 0
	override(t, &askDiscoverModels, func(ctx context.Context, p askProvider) ([]askModel, error) {
		discovered++
		return nil, fmt.Errorf("must not run")
	})
	r := askCall(t, s, http.MethodGet, "status", nil)
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"enabled":false`) {
		t.Fatal(r.Body.String())
	}
	for _, path := range []string{"models?provider=Codex", "send"} {
		method := http.MethodGet
		var body any
		if path == "send" {
			method = http.MethodPost
			body = askInput()
		}
		if r := askCall(t, s, method, path, body); r.Code == 200 {
			t.Fatalf("disabled %s succeeded", path)
		}
	}
	if discovered != 0 {
		t.Fatal("disabled feature ran CLI discovery")
	}
	if r := askCall(t, s, http.MethodPost, "settings", map[string]any{"enabled": true}); r.Code == 200 {
		t.Fatal("enabled without consent")
	}
	override(t, &askFindCLI, func(string) string { return "" })
	if r := askCall(t, s, http.MethodPost, "settings", map[string]any{"enabled": true, "consent": true}); r.Code == 200 {
		t.Fatal("enabled without CLI")
	}
	askOK(t, s, "settings", map[string]any{"enabled": false, "showInvitation": false})
	settings, err := s.askSettings(t.Context())
	if err != nil || settings.Enabled || settings.ShowInvitation {
		t.Fatalf("settings: %+v %v", settings, err)
	}
	useHost(t, "another-host")
	settings, err = s.askSettings(t.Context())
	if err != nil || settings.Enabled || !settings.ShowInvitation {
		t.Fatalf("opt-in traveled between Macs: %+v %v", settings, err)
	}
}
func TestAskConversationPersistenceAndSelection(t *testing.T) {
	s := askTestServer(t)
	askEnable(t, s)
	calls := 0
	override(t, &askRunCLI, func(ctx context.Context, p askProvider, selection askSelection, prompt string) ([]byte, error) {
		calls++
		if strings.Contains(prompt, s.Config().APIToken) {
			t.Error("passed service token to CLI")
		}
		var response askAgentResponse
		_ = json.Unmarshal(askReply("slow-commands", "findings"), &response)
		response.Message = fmt.Sprintf("Navigation reply %d", calls)
		return []byte(jsonText(response)), nil
	})
	askOK(t, s, "send", askInput())
	session, err := s.askSession(t.Context(), "chat-1")
	if err != nil || len(session.Conversation) != 1 || len(session.Conversation[0].Cards) != 2 {
		t.Fatalf("session: %+v %v", session, err)
	}
	if session.Conversation[0].Cards[0].Href == "" || session.Conversation[0].ModelLabel != "Test Model" {
		t.Fatal("missing route or model identity")
	}
	var conversationID string
	if err = s.Catalog.reader().QueryRow("SELECT id FROM conversations WHERE native_id=?", "ask-chat-1").Scan(&conversationID); err != nil {
		t.Fatal(err)
	}
	checkPreviews := func(reply string) {
		t.Helper()
		overview := auditCall(t, s.Catalog, "get_conversation_overview", map[string]any{"conversation_id": conversationID})
		request, response := overview["request"].(map[string]any), overview["last_response"].(map[string]any)
		if request["text"] != session.Conversation[0].Prompt || !strings.Contains(firstString(response["text"]), reply) || firstString(request["message_id"]) == "" || firstString(response["message_id"]) == "" {
			t.Fatalf("chat discovery previews missing or stale: %s", jsonText(overview))
		}
		matches, err := s.Catalog.vectorScores(t.Context(), conversationVectors, reply, 0.01)
		if err != nil || len(matches) != 1 || matches[0].id != conversationID {
			t.Fatalf("chat missing from conversation-context search: %+v %v", matches, err)
		}
	}
	checkPreviews("Navigation reply 1")
	// Retrying a completed submission cannot issue another billable request.
	if r := askCall(t, s, http.MethodPost, "send", askInput()); r.Code == 200 {
		t.Fatal("duplicate submission accepted")
	}
	if calls != 1 {
		t.Fatalf("duplicate inference calls: %d", calls)
	}
	changed := askInput()
	changed["selection"] = askSelection{Provider: "Codex", Model: "test-model", Effort: "Default"}
	changed["turnID"] = "turn-2"
	if r := askCall(t, s, http.MethodPost, "send", changed); r.Code == 200 {
		t.Fatal("changed selection mid-chat")
	}
	follow := askInput()
	follow["turnID"] = "turn-2"
	follow["prompt"] = "What about retries?"
	askOK(t, s, "send", follow)
	checkPreviews("Navigation reply 2")
	askOK(t, s, "session", map[string]any{"id": "chat-1", "draft": "Keep this draft"})
	restarted := testServer(t, s.Config(), s.Catalog)
	got, err := restarted.askSession(t.Context(), "chat-1")
	if err != nil || len(got.Conversation) != 2 || got.Draft != "Keep this draft" {
		t.Fatalf("restart lost chat: %+v %v", got, err)
	}
	summaries, err := s.askSessions(t.Context(), "wasting", 0)
	if err != nil || len(summaries) != 1 || summaries[0].QuestionCount != 2 || len(summaries[0].Conversation) != 1 {
		t.Fatalf("history summaries: %+v %v", summaries, err)
	}
	for _, query := range []string{"retries", "Navigation reply 2", "Explore the recorded evidence"} {
		matches, err := s.askSessions(t.Context(), query, 0)
		if err != nil || len(matches) != 1 || matches[0].ID != session.ID {
			t.Fatalf("full-text history match %q missing: %+v %v", query, matches, err)
		}
	}
	found, searchErr := s.Catalog.Search(SearchOptions{Query: "wasting", Limit: 10, ctx: t.Context()})
	if searchErr != nil || len(found["items"].([]map[string]any)) != 1 {
		t.Fatalf("chat missing from general search: %v %v", found, searchErr)
	}
	var count int
	if err = s.Catalog.reader().QueryRow("SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH ?", `"wasting"`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("chat was not indexed: %d %v", count, err)
	}
}
func TestAskRejectsUnknownDestinations(t *testing.T) {
	s := askTestServer(t)
	askEnable(t, s)
	override(t, &askRunCLI, func(context.Context, askProvider, askSelection, string) ([]byte, error) {
		return askReply("https://example.com"), nil
	})
	response := askOK(t, s, "send", askInput())
	var session askSession
	_ = json.Unmarshal(response.Body.Bytes(), &session)
	if session.Conversation[0].Status != "error" || len(session.Conversation[0].Cards) != 0 {
		t.Fatalf("unsafe response: %+v", session)
	}
	for _, raw := range []string{"https://example.com", "//example.com", "/api/ask/status", "/work/../api/release", "/work/%2e%2e/api/release", "/work/%5cexample", "/library?token=secret", "/library?api_token=secret", "/library\n", "/work/x%00"} {
		if askSafeURL(raw) {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, raw := range []string{"/analyze?analyze_topic=tools", "/library?search=cache", "/?ask_chat=chat-1"} {
		if !askSafeURL(raw) {
			t.Errorf("refused %q", raw)
		}
	}
}
func TestAskPrivateVisitsAndChats(t *testing.T) {
	s := askTestServer(t)
	askEnable(t, s)
	override(t, &askVisitLimit, 2)
	now := time.Now().UnixMilli()
	older := askVisit{URL: "/analyze", Title: "Earlier visit", At: now - 4000}
	original := askVisit{URL: "/analyze", Title: "Original title", At: now - 3000}
	replacement := askVisit{URL: "/analyze?analyze_topic=tools", Title: "Updated title and query", At: now - 2000}
	askOK(t, s, "visits", map[string]any{"visits": []askVisit{older, original}})
	askOK(t, s, "visits", map[string]any{"visits": []askVisit{replacement}, "replaces": original})
	_, retained, err := s.askLookup(t.Context(), askLookup{Kind: "visits"}, "")
	if err != nil || len(retained) != 2 || retained[0].Href != replacement.URL || retained[1].Description != time.UnixMilli(older.At).UTC().Format(time.RFC3339) {
		t.Fatalf("replacing a visit retained its old identity or removed an unrelated visit: %+v %v", retained, err)
	}
	askOK(t, s, "remove-visit", replacement)
	_, retained, err = s.askLookup(t.Context(), askLookup{Kind: "visits", Query: "Updated title"}, "")
	if err != nil || len(retained) != 0 {
		t.Fatalf("removed replacement still available: %+v %v", retained, err)
	}
	// Initial synchronization also removes old invisible rows from prior builds.
	askOK(t, s, "visits", map[string]any{"visits": []askVisit{replacement}, "reconcile": true})
	_, retained, err = s.askLookup(t.Context(), askLookup{Kind: "visits"}, "")
	if err != nil || len(retained) != 1 || retained[0].Href != replacement.URL {
		t.Fatalf("browser snapshot did not reconcile superseded visits: %+v %v", retained, err)
	}
	askOK(t, s, "visits", map[string]any{"visits": []askVisit{}, "reconcile": true})
	_, retained, err = s.askLookup(t.Context(), askLookup{Kind: "visits"}, "")
	if err != nil || len(retained) != 0 {
		t.Fatalf("empty browser history left private visits: %+v %v", retained, err)
	}
	secret := "private visit marker not in public search"
	askOK(t, s, "visits", map[string]any{"visits": []askVisit{{URL: "/library?search=private", Title: secret, At: now - 2000}, {URL: "/analyze?analyze_topic=tools", Title: "Tools", At: now - 1000}, {URL: "/findings", Title: "Findings", At: now}, {URL: "//external", Title: "Bad", At: now}}})
	result, cards, err := s.askLookup(t.Context(), askLookup{Kind: "visits", Query: ""}, "chat-1")
	if err != nil || len(cards) != 2 {
		t.Fatalf("visits: %+v %+v %v", result, cards, err)
	}
	if strings.Contains(jsonText(result), secret) {
		t.Fatal("retention did not remove old visit")
	}
	// No visit metadata enters indexed messages or generic UI preferences.
	var count int
	if err = s.Catalog.reader().QueryRow("SELECT COUNT(*) FROM messages_fts").Scan(&count); err != nil || count != 0 {
		t.Fatalf("visits in archive: %d %v", count, err)
	}
	prefs, err := s.Catalog.UIPreferences(t.Context())
	if err != nil || len(prefs) != 0 {
		t.Fatalf("private state in UI preferences: %v %v", prefs, err)
	}
	// The agent asks for visits through its private response protocol, then gets
	// the returned known IDs, not a public MCP tool or general API credential.
	calls := 0
	// Use the specific filtered visit's identity.
	_, filtered, _ := s.askLookup(t.Context(), askLookup{Kind: "visits", Query: "Tools"}, "chat-1")
	override(t, &askRunCLI, func(ctx context.Context, p askProvider, sel askSelection, prompt string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(jsonText(askAgentResponse{Lookup: askLookup{Kind: "visits", Query: "Tools"}})), nil
		}
		if !strings.Contains(prompt, "bounded") || !strings.Contains(prompt, "America/Denver") {
			t.Error("missing context")
		}
		return askReply(filtered[0].ID), nil
	})
	askOK(t, s, "send", askInput())
	if calls != 2 {
		t.Fatalf("lookups: %d", calls)
	}
	_, chats, err := s.askLookup(t.Context(), askLookup{Kind: "chats", Query: "wasting"}, "other-chat")
	if err != nil || len(chats) != 1 || chats[0].Href != "/?ask_chat=chat-1" {
		t.Fatalf("chat lookup: %+v %v", chats, err)
	}
	useHost(t, "other-host")
	_, cards, err = s.askLookup(t.Context(), askLookup{Kind: "visits"}, "")
	if err != nil || len(cards) != 0 {
		t.Fatalf("visits crossed hosts: %+v %v", cards, err)
	}
}
func TestAskCancelAndDisable(t *testing.T) {
	for _, action := range []string{"cancel", "disable"} {
		t.Run(action, func(t *testing.T) {
			s := askTestServer(t)
			askEnable(t, s)
			started := make(chan struct{})
			done := make(chan *httptest.ResponseRecorder, 1)
			override(t, &askRunCLI, func(ctx context.Context, p askProvider, selection askSelection, prompt string) ([]byte, error) {
				close(started)
				<-ctx.Done()
				return nil, ctx.Err()
			})
			go func() { done <- askCall(t, s, http.MethodPost, "send", askInput()) }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("CLI did not start")
			}
			if action == "cancel" {
				askOK(t, s, "cancel", map[string]any{"id": "chat-1"})
			} else {
				askOK(t, s, "settings", map[string]any{"enabled": false})
			}
			select {
			case result := <-done:
				if result.Code != 200 {
					t.Fatalf("completion: %d %s", result.Code, result.Body.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("request ignored cancellation")
			}
			session, err := s.askSession(t.Context(), "chat-1")
			if err != nil || session.Conversation[0].Status != "canceled" {
				t.Fatalf("canceled state: %+v %v", session, err)
			}
		})
	}
}

// A finished answer waits to be saved while the catalog writer is busy. That
// must not lose it, make a reload report the request stopped, or hold up the
// rest of Ask; a stop meanwhile replaces it.
func TestAskSavesAnswerPastABusyCatalog(t *testing.T) {
	busy := func(t *testing.T, s *Server) (release func()) {
		held, done, written := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		calls := 0
		override(t, &askRunCLI, func(context.Context, askProvider, askSelection, string) ([]byte, error) {
			if calls++; calls > 1 {
				return askReply("home"), nil
			}
			go func() {
				written <- s.Catalog.writeTransaction(context.Background(), "test-busy", func(*sql.Tx) error {
					close(held)
					<-done
					return nil
				})
			}()
			<-held
			return askReply("home"), nil
		})
		return func() {
			close(done)
			if err := <-written; err != nil {
				t.Fatal(err)
			}
		}
	}
	read := func(t *testing.T, body []byte) askSession {
		t.Helper()
		var session askSession
		if err := json.Unmarshal(body, &session); err != nil || len(session.Conversation) != 1 {
			t.Fatalf("session: %s %v", body, err)
		}
		return session
	}
	reload := func(t *testing.T, s *Server) askSession {
		t.Helper()
		response := askCall(t, s, http.MethodGet, "session?id=chat-1", nil)
		if response.Code != 200 {
			t.Fatalf("reload: %d %s", response.Code, response.Body.String())
		}
		return read(t, response.Body.Bytes())
	}
	saved := func(t *testing.T, s *Server, status string) askSession {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			session, err := s.askSession(t.Context(), "chat-1")
			if err == nil && session.Conversation[0].Status == status {
				s.ask.mu.Lock()
				defer s.ask.mu.Unlock()
				if len(s.ask.active) != 0 || len(s.ask.saving) != 0 {
					t.Fatalf("a saved chat is still held: %v %v", s.ask.active, s.ask.saving)
				}
				return session
			}
			if time.Now().After(deadline) {
				t.Fatalf("the chat was never saved as %s: %+v %v", status, session, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	t.Run("save in progress", func(t *testing.T) {
		s := askTestServer(t)
		askEnable(t, s)
		// The first save waits for the writer, so anything that waited for it
		// in turn would stall this test.
		override(t, &askSaveTimeout, time.Minute)
		override(t, &askSaveRetry, 10*time.Millisecond)
		release := busy(t, s)
		sent := make(chan *httptest.ResponseRecorder, 1)
		go func() { sent <- askCall(t, s, http.MethodPost, "send", askInput()) }()
		deadline := time.Now().Add(10 * time.Second)
		for {
			// Until send saves the question, the chat does not exist.
			response := askCall(t, s, http.MethodGet, "session?id=chat-1", nil)
			if response.Code == 200 && read(t, response.Body.Bytes()).Conversation[0].Status == "single" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("a reload did not show the finished answer")
			}
			time.Sleep(10 * time.Millisecond)
		}
		askCall(t, s, http.MethodGet, "status", nil)
		askOK(t, s, "session", map[string]any{"id": "chat-1", "draft": "Typed while saving"})
		askOK(t, s, "cancel", map[string]any{"id": "chat-1"})
		if session := reload(t, s); session.Conversation[0].Status != "canceled" || session.Draft != "Typed while saving" {
			t.Fatalf("reload after a stop: %+v", session)
		}
		release()
		if response := <-sent; response.Code != 200 {
			t.Fatalf("send: %d %s", response.Code, response.Body.String())
		}
		if session := saved(t, s, "canceled"); session.Draft != "Typed while saving" || len(session.Conversation[0].Cards) != 0 {
			t.Fatalf("saved after a stop: %+v", session)
		}
	})

	t.Run("save retried", func(t *testing.T) {
		s := askTestServer(t)
		askEnable(t, s)
		override(t, &askSaveTimeout, 50*time.Millisecond)
		override(t, &askSaveRetry, 10*time.Millisecond)
		release := busy(t, s)
		if session := read(t, askOK(t, s, "send", askInput()).Body.Bytes()); session.Conversation[0].Status != "single" {
			t.Fatalf("the response lost the answer: %+v", session)
		}
		if session := reload(t, s); session.Conversation[0].Status != "single" {
			t.Fatalf("reload while saving: %+v", session)
		}
		next := askInput()
		next["turnID"] = "turn-2"
		if response := askCall(t, s, http.MethodPost, "send", next); response.Code == 200 {
			t.Fatal("a question was added to a chat whose answer is not saved")
		}
		release()
		saved(t, s, "single")
		askOK(t, s, "send", next)
	})
}

func TestAskModelCatalogs(t *testing.T) {
	raw := json.RawMessage(`{"data":[{"model":"available-model","displayName":"Available Model","isDefault":true,"supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"high"}]}],"nextCursor":null}`)
	models, err := askCodexModels(raw)
	if err != nil || len(models) != 1 || !askValidSelection(askProvider{Models: models}, askSelection{Model: "available-model", Effort: "High"}) {
		t.Fatalf("models: %+v %v", models, err)
	}
	if askValidSelection(askProvider{Models: models}, askSelection{Model: "invented", Effort: "High"}) {
		t.Fatal("invented model accepted")
	}
	agy := askAGYModels("Fetching models...\nmodel-low\tModel (Low)\nmodel-high\tModel (High)\n")
	if len(agy) != 1 || len(agy[0].Efforts) != 3 {
		t.Fatalf("agy: %+v", agy)
	}
	cards, err := askDestinations()
	if err != nil || len(cards) < 20 {
		t.Fatalf("catalog: %d %v", len(cards), err)
	}
	for _, card := range cards {
		if !askSafeURL(card.Href) {
			t.Errorf("invalid destination %s", card.ID)
		}
	}
}

func TestAskDefaultSelection(t *testing.T) {
	for _, tc := range []struct {
		name, provider, preferred, label, effort string
	}{
		{"Codex", "Codex", "gpt-6.1-sol", "GPT-6.1-Sol", "Low"},
		{"Claude", "Claude", "claude-sonnet-5-5", "Sonnet 5.5", "Medium"},
		{"Antigravity", "Antigravity", "gemini-3.8-flash", "Gemini 3.8 Flash", "Default"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fallback := askModel{ID: "cli-default", Default: true, Efforts: []string{"Default", "Low", "Medium"}}
			preferred := askModel{ID: tc.preferred, Label: tc.label, Efforts: []string{"Default", "Low", "Medium"}}
			provider := askProvider{ID: tc.provider, Models: []askModel{fallback, preferred}}
			want := askSelection{Provider: tc.provider, Model: tc.preferred, Effort: tc.effort}
			if got := askDefaultSelection(provider); got != want || !askValidSelection(provider, got) {
				t.Fatalf("preferred selection: %+v, want %+v", got, want)
			}
			// Discovery labels may identify the model even when the CLI uses an opaque ID.
			provider.Models[1].ID = "discovered-id"
			want.Model = "discovered-id"
			if got := askDefaultSelection(provider); got != want {
				t.Fatalf("label selection: %+v, want %+v", got, want)
			}
			provider.Models[1].Efforts = []string{"Default"}
			want.Effort = "Default"
			if got := askDefaultSelection(provider); got != want || !askValidSelection(provider, got) {
				t.Fatalf("unsupported effort fallback: %+v, want %+v", got, want)
			}
			provider.Models = []askModel{{ID: "first", Efforts: []string{"Default"}}, fallback}
			want.Model, want.Effort = fallback.ID, tc.effort
			if got := askDefaultSelection(provider); got != want || !askValidSelection(provider, got) {
				t.Fatalf("unavailable model fallback: %+v, want %+v", got, want)
			}
			provider.Models[1].Default = false
			want.Model, want.Effort = "first", "Default"
			if got := askDefaultSelection(provider); got != want {
				t.Fatalf("unmarked catalog fallback: %+v, want %+v", got, want)
			}
			provider.Models = nil
			if got := askDefaultSelection(provider); got.Model != "" || got.Provider != tc.provider {
				t.Fatalf("empty catalog invented a model: %+v", got)
			}
		})
	}
}

// Manual adapter smoke test: opt in to one installed CLI explicitly. The
// ordinary suites never invoke it or spend account quota.
func TestAskInstalledCLI(t *testing.T) {
	name := os.Getenv("PHAROS_TEST_ASK_CLI")
	if name == "" {
		t.Skip("manual: PHAROS_TEST_ASK_CLI=Codex, Claude, or Antigravity")
	}
	var provider askProvider
	for _, p := range askInstalled() {
		if p.ID == name {
			provider = p
		}
	}
	if provider.path == "" {
		t.Fatalf("%s is not installed", name)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	models, err := askDiscoverModels(ctx, provider)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) == 0 {
		t.Fatal("no available models")
	}
	provider.Models = models
	model := models[0]
	for _, m := range models {
		if name == "Claude" && strings.Contains(strings.ToLower(m.Label), "sonnet") {
			model = m
			break
		}
		if name == "Antigravity" && strings.Contains(m.ID, "flash") {
			model = m
			break
		}
	}
	effort := "Default"
	if slices.Contains(model.Efforts, "Low") {
		effort = "Low"
	}
	catalog, config := testCatalog(t)
	executable := filepath.Join(t.TempDir(), "pharos")
	build := exec.CommandContext(ctx, "go", "build", "-o", executable, "../../cmd/pharos")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build MCP server: %v\n%s", err, output)
	}
	override(t, &askMCPExecutable, func() string { return executable })
	config.Port = 1 // Nothing listens, so MCP uses this test catalog directly.
	if err := os.WriteFile(config.Path, []byte(fmt.Sprintf("catalog_path = %q\narchive_root = %q\nstaging_root = %q\nport = 1\n", config.CatalogPath, config.ArchiveRoot, config.StagingRoot)), 0600); err != nil {
		t.Fatal(err)
	}
	server := testServer(t, config, catalog)
	session := askSession{Selection: askSelection{Provider: name, Model: model.ID, Effort: effort}, Conversation: []askTurn{{Prompt: "First call list_repositories through the Pharos MCP server (pharos-ask for Antigravity, pharos otherwise), then show me Pharos settings. Use the catalog for the destination; do not request a private lookup."}}}
	answer, err := server.answerAsk(ctx, provider, session, "America/Denver")
	if err != nil {
		t.Fatal(err)
	}
	if len(answer.Cards) == 0 {
		t.Fatalf("no destination returned: %s", answer.Message)
	}
	var calls int
	if err := catalog.DB.QueryRow("SELECT COUNT(*) FROM mcp_calls WHERE tool_name='list_repositories' AND status='ok'").Scan(&calls); err != nil || calls == 0 {
		t.Fatalf("agent did not successfully use Pharos MCP: calls=%d, err=%v", calls, err)
	}
	t.Logf("%s · %s · %s -> %s", name, model.Label, effort, answer.Cards[0].Title)
}

func TestAskNativeContextNeverEntersGeneralArchive(t *testing.T) {
	s := askTestServer(t)
	err := s.Catalog.writeTransaction(t.Context(), "test-native-ask", func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO ask_native_sessions(provider,native_id,host_id) VALUES('antigravity','private-session','ask-host')"); err != nil {
			return err
		}
		private := WorkspaceRecord{SourceID: "private-session", SourceKind: "antigravity", Account: "local", Title: "Private protocol", Conversations: []ConversationRecord{{NativeID: "private-session", Provider: "antigravity", Account: "local", Messages: []MessageRecord{{NativeID: "raw-context", Role: "user", Kind: "message", Text: "private visits and lookup payload", Selected: true}}}}}
		conversations, messages, err := ingestWorkspace(tx, private, false)
		if err != nil {
			return err
		}
		if conversations != 0 || messages != 0 {
			return fmt.Errorf("private transcript was ingested: %d conversations, %d messages", conversations, messages)
		}
		// A normal conversation from the same source still ingests.
		private.SourceID = "public-session"
		private.Conversations[0].NativeID = "public-session"
		private.Conversations[0].Messages[0].Text = "An ordinary coding conversation"
		_, _, err = ingestWorkspace(tx, private, false)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.Catalog.reader().QueryRow("SELECT COUNT(*) FROM messages WHERE text LIKE '%private visits%'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("private data was indexed: %d %v", count, err)
	}
	if err = s.Catalog.reader().QueryRow("SELECT COUNT(*) FROM messages WHERE text='An ordinary coding conversation'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("ordinary ingestion changed: %d %v", count, err)
	}
}
// The picker answers from the models found in the background: enabling Ask
// and starting the service discover them, and a stale list is refreshed
// behind an answer from the old one, which a failed refresh keeps.
func TestAskModelsRefreshInBackground(t *testing.T) {
	s := askTestServer(t)
	var calls atomic.Int32
	entered, gate := make(chan struct{}), make(chan error)
	override(t, &askDiscoverModels, func(ctx context.Context, p askProvider) ([]askModel, error) {
		n := calls.Add(1)
		if n > 1 {
			entered <- struct{}{}
			if err := <-gate; err != nil {
				return nil, err
			}
		}
		return []askModel{{ID: fmt.Sprintf("model-%d", n), Label: "Model", Efforts: []string{"Default"}, Default: true}}, nil
	})
	refreshing := func() {
		t.Helper()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("no refresh started")
		}
	}
	settle := func(s *Server) {
		t.Helper()
		s.ask.mu.Lock()
		d := s.ask.discovering["Codex"]
		s.ask.mu.Unlock()
		if d == nil {
			return
		}
		select {
		case <-d.done:
		case <-time.After(5 * time.Second):
			t.Fatal("discovery did not finish")
		}
	}
	models := func() string {
		t.Helper()
		r := askCall(t, s, http.MethodGet, "models?provider=Codex", nil)
		if r.Code != 200 {
			t.Fatalf("models: %d %s", r.Code, r.Body.String())
		}
		var p askProvider
		if err := json.Unmarshal(r.Body.Bytes(), &p); err != nil || len(p.Models) != 1 {
			t.Fatalf("models: %s %v", r.Body.String(), err)
		}
		return p.Models[0].ID
	}
	age := func(by time.Duration) {
		s.ask.mu.Lock()
		defer s.ask.mu.Unlock()
		cached := s.ask.models["Codex"]
		cached.checkedAt = cached.checkedAt.Add(-by)
		s.ask.models["Codex"] = cached
	}
	askEnable(t, s)
	settle(s)
	if got := models(); got != "model-1" || calls.Load() != 1 {
		t.Fatalf("enabling did not discover models ahead of the picker: %s after %d discoveries", got, calls.Load())
	}
	age(askModelsTTL)
	if got := models(); got != "model-1" {
		t.Fatalf("a stale list was not answered at once: %s", got)
	}
	refreshing()
	gate <- nil
	settle(s)
	if got := models(); got != "model-2" {
		t.Fatalf("refresh did not replace the list: %s", got)
	}
	age(askModelsTTL)
	models()
	refreshing()
	gate <- fmt.Errorf("not authenticated")
	settle(s)
	if got := models(); got != "model-2" || calls.Load() != 3 {
		t.Fatalf("a failed refresh lost the list or retried at once: %s after %d discoveries", got, calls.Load())
	}
	restarted := testServer(t, s.Config(), s.Catalog)
	restarted.warmAskModelsOnStart()
	refreshing()
	gate <- nil
	settle(restarted)
	restarted.ask.mu.Lock()
	defer restarted.ask.mu.Unlock()
	if got := restarted.ask.models["Codex"].Models; len(got) != 1 || got[0].ID != "model-4" {
		t.Fatalf("starting with Ask enabled did not discover models: %+v", got)
	}
}
func TestAskDisableInterruptsModelDiscovery(t *testing.T) {
	s := askTestServer(t)
	started := make(chan struct{})
	done := make(chan *httptest.ResponseRecorder, 1)
	override(t, &askDiscoverModels, func(ctx context.Context, p askProvider) ([]askModel, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	askEnable(t, s)
	go func() { done <- askCall(t, s, http.MethodGet, "models?provider=Codex", nil) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery did not start")
	}
	askOK(t, s, "settings", map[string]any{"enabled": false})
	select {
	case response := <-done:
		if response.Code == 200 {
			t.Fatal("discovery succeeded after disable")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("discovery ignored cancellation")
	}
}
func TestAskCLIProcessCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cmd := askCommand(ctx, "/bin/sh", t.TempDir(), "-c", "printf ready; sleep 30 & wait")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make([]byte, 5)
	if _, err = io.ReadFull(stdout, ready); err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled process succeeded")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("descendant kept canceled request alive")
	}
}

// The CLI's MCP server answers from the service Ask runs in, never from the
// library the installed app assigns MCP to for outside agents.
func TestAskUsesLibraryMCP(t *testing.T) {
	s := askTestServer(t)
	askEnable(t, s)
	override(t, &askMCPExecutable, func() string { return "/test/pharos" })
	config := s.Config()
	configPath, err := filepath.Abs(config.Path)
	if err != nil {
		t.Fatal(err)
	}
	calls, service := 0, ""
	override(t, &askRunCLI, func(ctx context.Context, p askProvider, selection askSelection, prompt string) ([]byte, error) {
		calls++
		if p.mcp.Command != "/test/pharos" || len(p.mcp.Args) != 5 || !reflect.DeepEqual(p.mcp.Args[:4], []string{"--config", configPath, "mcp", "--service"}) {
			t.Fatalf("MCP connection = %+v", p.mcp)
		}
		service = p.mcp.Args[4]
		var endpoint applicationEndpoint
		data, err := os.ReadFile(service)
		if err == nil {
			err = json.Unmarshal(data, &endpoint)
		}
		if err != nil || endpoint.URL != "http://"+net.JoinHostPort(config.Host, strconv.Itoa(config.Port)) || endpoint.Token != config.APIToken || endpoint.Catalog != config.CatalogPath {
			t.Fatalf("service endpoint = %+v, %v", endpoint, err)
		}
		if !strings.Contains(prompt, "pharos MCP server connected") || !strings.Contains(prompt, "Page visits and private chat history remain available only through lookup") {
			t.Fatal("missing MCP or private-history instructions")
		}
		return askReply("home"), nil
	})
	askOK(t, s, "send", askInput())
	if calls != 1 {
		t.Fatalf("CLI calls = %d", calls)
	}
	if _, err := os.Stat(service); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the service endpoint outlived the request: %v", err)
	}
	if err := s.Catalog.SetMCPEnabled(false); err != nil {
		t.Fatal(err)
	}
	input := askInput()
	input["turnID"] = "turn-2"
	response := askOK(t, s, "send", input)
	if calls != 1 || !strings.Contains(response.Body.String(), "Pharos MCP is turned off") {
		t.Fatalf("disabled MCP launched CLI or lost the error: %d %s", calls, response.Body.String())
	}
	if enabled, err := s.Catalog.MCPEnabled(); err != nil || enabled {
		t.Fatalf("Ask changed MCP consent: %v %v", enabled, err)
	}
}

func TestAskCLIConnections(t *testing.T) {
	// No installed CLI or inference is involved: inspect the configuration each
	// adapter hands to its process, including paths needing serialization.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PHAROS_API_TOKEN", "must-not-reach-agent")
	settings := filepath.Join(os.Getenv("HOME"), ".gemini", "antigravity-cli", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"permissions":{"allow":["mcp(pharos-ask/*)"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	want := askMCPServer{Command: `/drive with spaces/Pharos "special".app/pharos`, Args: []string{"--config", "/drive with spaces/library.toml", "mcp"}}
	for _, name := range []string{"Codex", "Claude", "Antigravity"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			provider := askProvider{ID: name, path: "/test/cli", mcp: want}
			cmd, err := askPrepareCLI(t.Context(), provider, askSelection{Provider: name, Model: "test-model", Effort: "Low"}, dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range cmd.Env {
				if strings.HasPrefix(entry, "PHAROS_API_TOKEN=") {
					t.Fatal("API token reached provider")
				}
			}
			flag := func(name string) string {
				t.Helper()
				index := slices.Index(cmd.Args, name)
				if index < 0 || index+1 >= len(cmd.Args) {
					t.Fatalf("missing %s in %v", name, cmd.Args)
				}
				return cmd.Args[index+1]
			}
			var got askMCPServer
			switch name {
			case "Codex":
				config := map[string]string{}
				for i, arg := range cmd.Args {
					if arg == "-c" {
						key, value, _ := strings.Cut(cmd.Args[i+1], "=")
						config[key] = value
					}
				}
				if err := json.Unmarshal([]byte(config["mcp_servers.pharos.command"]), &got.Command); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(config["mcp_servers.pharos.args"]), &got.Args); err != nil {
					t.Fatal(err)
				}
				if config["mcp_servers.pharos.required"] != "true" || config["mcp_servers.pharos.default_tools_approval_mode"] != `"approve"` || config["features.shell_tool"] != "false" || flag("--sandbox") != "read-only" || cmd.Args[len(cmd.Args)-1] != "-" {
					t.Fatalf("missing required MCP startup or sandbox: %v", cmd.Args)
				}
			case "Claude":
				var config struct {
					Servers map[string]askMCPServer `json:"mcpServers"`
				}
				if err := json.Unmarshal([]byte(flag("--mcp-config")), &config); err != nil {
					t.Fatal(err)
				}
				got = config.Servers["pharos"]
				if len(config.Servers) != 1 || flag("--allowedTools") != "mcp__pharos" || flag("--tools") != "" || flag("--permission-mode") != "dontAsk" || !slices.Contains(cmd.Args, "--strict-mcp-config") {
					t.Fatalf("missing scoped MCP permissions: %v", cmd.Args)
				}
			case "Antigravity":
				agent, err := os.ReadFile(filepath.Join(dir, ".agents", "agents", "pharos-navigation.md"))
				if err != nil {
					t.Fatal(err)
				}
				servers, err := os.ReadFile(filepath.Join(dir, ".agents", "mcp_config.json"))
				if err != nil {
					t.Fatal(err)
				}
				var config struct {
					Servers map[string]askMCPServer `json:"mcpServers"`
				}
				if err := json.Unmarshal(servers, &config); err != nil {
					t.Fatal(err)
				}
				if len(config.Servers) != 1 || !strings.Contains(string(agent), "  - call_mcp_tool\n") || flag("--agent") != "pharos-navigation" {
					t.Fatalf("MCP not available to navigation agent: %s", agent)
				}
				got = config.Servers["pharos-ask"]
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("MCP connection = %+v, want %+v", got, want)
			}
		})
	}
}

func TestAskAntigravityMCPPermissions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(os.Getenv("HOME"), ".gemini", "antigravity-cli", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		settings string
		allowed  bool
	}{
		{`{}`, false},
		{`{"permissions":{"allow":["mcp(pharos-ask/*)"]}}`, true},
		{`{"permissions":{"allow":["mcp(other/*)"]}}`, false},
		{`{"permissions":{"allow":["mcp(pharos/*)"]}}`, false},
		{`{"permissions":{"allow":["mcp(pharos-ask/list_repositories)"]}}`, false},
		{`{"toolPermission":"always-proceed"}`, true},
		{`{"toolPermission":"strict","permissions":{"allow":["mcp(pharos-ask/*)"]}}`, false},
		{`{"permissions":{"allow":["mcp(*)"],"deny":["mcp(pharos-ask/search_work)"]}}`, false},
		{`{"permissions":{"allow":["mcp(*)"],"ask":["mcp(pharos-ask/*)"]}}`, false},
	} {
		if err := os.WriteFile(path, []byte(tc.settings), 0600); err != nil {
			t.Fatal(err)
		}
		if err := askCheckAGYMCPPermissions(); (err == nil) != tc.allowed {
			t.Errorf("permissions %s: %v", tc.settings, err)
		}
	}
	shared := filepath.Join(os.Getenv("HOME"), ".gemini", "config", "config.json")
	if err := os.MkdirAll(filepath.Dir(shared), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, shared, cli string
		allowed           bool
	}{
		{"shared grant without CLI file", `{"allow":["mcp(pharos-ask/*)"]}`, "", true},
		{"shared grant with unrelated CLI settings", `{"allow":["mcp(pharos-ask/*)"]}`, `{"model":"test-model"}`, true},
		{"shared grant with null CLI permissions", `{"allow":["mcp(pharos-ask/*)"]}`, `{"permissions":null}`, true},
		{"shared grant with empty CLI permissions", `{"allow":["mcp(pharos-ask/*)"]}`, `{"permissions":{}}`, true},
		{"shared grant with unrelated CLI permissions", `{"allow":["mcp(pharos-ask/*)"]}`, `{"permissions":{"allow":["mcp(other/*)"]}}`, true},
		{"CLI deny overrides shared allow", `{"allow":["mcp(pharos-ask/*)"]}`, `{"permissions":{"deny":["mcp(pharos-ask/*)"]}}`, false},
		{"CLI ask overrides shared allow", `{"allow":["mcp(pharos-ask/*)"]}`, `{"permissions":{"ask":["mcp(pharos-ask/*)"]}}`, false},
		{"strict mode blocks shared grant", `{"allow":["mcp(pharos-ask/*)"]}`, `{"toolPermission":"strict"}`, false},
		{"shared deny beats shared allow", `{"allow":["mcp(*)"],"deny":["mcp(pharos-ask/*)"]}`, "", false},
		{"shared deny beats CLI allow", `{"deny":["mcp(pharos-ask/*)"]}`, `{"permissions":{"allow":["mcp(pharos-ask/*)"]}}`, false},
		{"shared ask beats CLI allow", `{"ask":["mcp(pharos-ask/*)"]}`, `{"permissions":{"allow":["mcp(pharos-ask/*)"]}}`, false},
		{"shared ask beats shared allow", `{"allow":["mcp(*)"],"ask":["mcp(pharos-ask/*)"]}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if tc.cli != "" {
				if err := os.WriteFile(path, []byte(tc.cli), 0600); err != nil {
					t.Fatal(err)
				}
			}
			raw := `{"userSettings":{"globalPermissionGrants":` + tc.shared + `}}`
			if err := os.WriteFile(shared, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if err := askCheckAGYMCPPermissions(); (err == nil) != tc.allowed {
				t.Errorf("shared=%s CLI=%s: %v", tc.shared, tc.cli, err)
			}
		})
	}
}

func TestAskViewIdentityRetainsQueryState(t *testing.T) {
	if askViewKey("/analyze?repo=pharos&view=tools") != askViewKey("/analyze?view=tools&repo=pharos") {
		t.Fatal("parameter order changed view identity")
	}
	if askViewKey("/analyze?repo=pharos") == askViewKey("/analyze?repo=other") {
		t.Fatal("different filters became the same visited view")
	}
}
