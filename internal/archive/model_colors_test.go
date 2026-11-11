package archive

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestModelColorsPersistIndependentChoicesAndDiscoverUnpricedModels(t *testing.T) {
	c, config := testCatalog(t)
	server := testServer(t, config, c)
	execTx(t, c.DB, `INSERT INTO workspaces(id,source_kind,source_account,source_id,title,indexed_at) VALUES('colors','canonical','local','colors','Colors','2026-01-01');
 INSERT INTO conversations(id,workspace_id,provider,account,native_id,model) VALUES('colors','colors','codex','local','colors','gpt-local');
 INSERT INTO agent_sessions(id,workspace_id,conversation_id,native_id,kind,provider,model) VALUES('colors-helper','colors','colors','helper','subagent','claude','claude-opus-custom-20261110[1m]')`)
	for id, color := range map[string]string{"provider:openai": "#ABCDEF", "model:gpt-local": "#123456", "source:codex": "#654321"} {
		response := preferencesCall(t, server, http.MethodPost, "/api/model-colors", jsonText(map[string]string{"id": id, "color": color}))
		if response.Code != http.StatusOK {
			t.Fatalf("save %s: %d %s", id, response.Code, response.Body.String())
		}
	}
	for _, body := range []string{`{"id":"model:gpt-local","color":null}`, `{"id":"model:gpt-local"}`, `{"id":"model:gpt-local","color":"red"}`, `{"id":"provider:unknown","color":"#123456"}`, `{"id":"model:claude-opus-5","color":"#123456"}`, `{"id":"model:","color":"#123456"}`, `{"id":"something:openai","color":"#123456"}`} {
		if response := preferencesCall(t, server, http.MethodPost, "/api/model-colors", body); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid edit: %s: %d", body, response.Code)
		}
	}
	response := preferencesCall(t, server, http.MethodGet, "/api/model-colors", "")
	var choices modelColorPalette
	if err := json.Unmarshal(response.Body.Bytes(), &choices); err != nil || response.Code != http.StatusOK || len(choices.Models) != 0 || len(choices.Overrides) != 3 {
		t.Fatalf("choices-only refresh: %d %+v %v", response.Code, choices, err)
	}
	reopened := reopenCatalog(t, c.Path)
	palette, err := reopened.modelColors(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	if palette.Overrides["provider:openai"] != "#abcdef" || palette.Overrides["model:gpt-local"] != "#123456" || palette.Overrides["source:codex"] != "#654321" {
		t.Fatalf("lost overrides: %+v", palette)
	}
	if !strings.Contains(jsonText(palette.Models), `"opus-custom"`) || !strings.Contains(jsonText(palette.Models), `"gpt-local"`) {
		t.Fatalf("missing recorded models: %+v", palette)
	}
	response = preferencesCall(t, server, http.MethodGet, "/assets/preferences.js", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `window.pharosModelColors={"overrides":`) || !strings.Contains(response.Body.String(), `"provider:openai":"#abcdef"`) {
		t.Fatalf("first paint: %d %s", response.Code, response.Body.String())
	}
	response = preferencesCall(t, server, http.MethodPost, "/api/model-colors", `{"id":"provider:openai","color":""}`)
	var reset modelColorPalette
	if err := json.Unmarshal(response.Body.Bytes(), &reset); err != nil || response.Code != http.StatusOK {
		t.Fatalf("reset: %d %v", response.Code, err)
	}
	if _, ok := reset.Overrides["provider:openai"]; ok || reset.Overrides["model:gpt-local"] != "#123456" || reset.Overrides["source:codex"] != "#654321" {
		t.Fatalf("reset overwrote independent choices: %+v", reset)
	}
	if response := preferencesCall(t, server, http.MethodGet, "/settings/models", ""); response.Code != http.StatusOK {
		t.Fatalf("settings route: %d", response.Code)
	}
}
