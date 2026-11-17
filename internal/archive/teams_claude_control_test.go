package archive

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestTeamsClaudeControlSyntheticTranscript(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/teams/claude-control-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var events []json.RawMessage
	if json.Unmarshal(raw, &events) != nil || len(events) != 3 {
		t.Fatal("invalid fixture")
	}
	account, ok := teamsClaudeInitialize(events[0], "init")
	if !ok || account == ([32]byte{}) {
		t.Fatal("synthetic initialization rejected")
	}
	var envelope map[string]any
	if json.Unmarshal(events[0], &envelope) != nil {
		t.Fatal("fixture")
	}
	payload := envelope["response"].(map[string]any)["response"].(map[string]any)
	payload["commands"] = []any{}
	payload["models"] = []any{map[string]any{"value": "synthetic"}}
	payload["user_output_styles_dir"] = "/synthetic/styles"
	extended, _ := json.Marshal(envelope)
	if _, ok = teamsClaudeInitialize(extended, "init"); !ok {
		t.Fatal("bounded model metadata rejected")
	}
	payload["commands"] = []any{map[string]any{"name": "synthetic-command"}}
	unsafe, _ := json.Marshal(envelope)
	if _, ok = teamsClaudeInitialize(unsafe, "init"); ok {
		t.Fatal("ambient command accepted")
	}
	if !teamsClaudeSettingsValid(events[1], "settings", "claude-opus-5-5", "low") || !teamsClaudeHooksValid(events[2], "hooks") {
		t.Fatal("synthetic effective policy rejected")
	}
}

const syntheticSafetySettings = `{"disableClaudeAiConnectors":true,"disableBundledSkills":true,"disableAllHooks":true,"disableWorkflows":true,"disableSkillShellExecution":true,"autoMemoryEnabled":false}`

func TestTeamsClaudeControlRejectsChangedSettingsAndHooks(t *testing.T) {
	settings := `{"type":"control_response","response":{"subtype":"success","request_id":"settings","response":{"effective":` + syntheticSafetySettings + `,"sources":[{"source":"flagSettings","settings":` + syntheticSafetySettings + `}],"applied":{"model":"synthetic-model","effort":"low","advisor":null,"ultracode":false,"ultracodeRequested":false,"ultracodeAvailable":false,"ultrathinkAvailable":false,"outsideFileToolReadsBlocked":false,"outsideFileToolWritesBlocked":false,"outsideBlocksCountLaunchFoldersOnly":false,"launchEnvLeftOwnFilesInPlace":true}}}}`
	if !teamsClaudeSettingsValid([]byte(settings), "settings", "synthetic-model", "low") {
		t.Fatal("reviewed settings rejected")
	}
	for _, bad := range []string{
		strings.Replace(settings, `"autoMemoryEnabled":false`, `"autoMemoryEnabled":true`, 1),
		strings.Replace(settings, `"effective":{`, `"effective":{"env":{"ANTHROPIC_BASE_URL":"https://synthetic.invalid"},`, 1),
		strings.Replace(settings, `"source":"flagSettings"`, `"source":"policySettings"`, 1),
		strings.Replace(settings, `"advisor":null`, `"advisor":"other"`, 1),
		strings.Replace(settings, `"request_id":"settings"`, `"request_id":"other"`, 1),
		strings.Replace(settings, `"disableAllHooks":true`, `"disableAllHooks":true,"disableAllHooks":true`, 1),
	} {
		if teamsClaudeSettingsValid([]byte(bad), "settings", "synthetic-model", "low") {
			t.Fatalf("unsafe settings accepted: %s", bad)
		}
	}
	hooks := `{"type":"control_response","response":{"subtype":"success","request_id":"hooks","response":{"events":[],"hooks":[],"eventCatalog":[],"policy":{"disabledByPolicy":false,"managedOnly":false,"pluginOnly":false,"allDisabled":true,"policyHookCount":0},"safeMode":{"managedHooksStillApply":false,"exitHint":"restart"}}}}`
	if !teamsClaudeHooksValid([]byte(hooks), "hooks") {
		t.Fatal("reviewed hooks rejected")
	}
	for _, bad := range []string{
		strings.Replace(hooks, `"hooks":[]`, `"hooks":[{}]`, 1),
		strings.Replace(hooks, `"policyHookCount":0`, `"policyHookCount":1`, 1),
		strings.Replace(hooks, `"managedHooksStillApply":false`, `"managedHooksStillApply":true`, 1),
		strings.Replace(hooks, `"allDisabled":true`, `"allDisabled":true,"policyUnreadable":true`, 1),
	} {
		if teamsClaudeHooksValid([]byte(bad), "hooks") {
			t.Fatalf("unsafe hooks accepted: %s", bad)
		}
	}
}
