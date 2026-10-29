package archive

// CLI adapters deliberately keep provider credentials in the provider's CLI.
// Discovery never makes an inference request. No CLI runs before opt-in.
import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

type askSelection struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
}
type askModel struct {
	Variants map[string]string `json:"-"`
	ID       string            `json:"id"`
	Label    string            `json:"label"`
	Efforts  []string          `json:"efforts"`
	Default  bool              `json:"default"`
}
type askProvider struct {
	checkedAt        time.Time
	recordSession    func(context.Context, string) error
	progress         func(string)
	ID               string        `json:"id"`
	Models           []askModel    `json:"models"`
	DefaultSelection *askSelection `json:"defaultSelection,omitempty"`
	Error            string        `json:"error,omitempty"`
	path             string
	mcp              askMCPServer
}

type askMCPServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

var askProviderBinaries = map[string]string{"Codex": "codex", "Claude": "claude", "Antigravity": "agy"}

// Prefer Pharos's starting configuration only when the installed CLI offers it.
// Saved user selections and existing conversations take precedence in the UI.
func askDefaultSelection(p askProvider) askSelection {
	preferred, effort := "", "Default"
	switch p.ID {
	case "Codex":
		preferred, effort = "gpt61sol", "Low"
	case "Claude":
		preferred, effort = "sonnet55", "Medium"
	case "Antigravity":
		preferred = "gemini38flash"
	}
	normalize := func(value string) string {
		value = strings.TrimPrefix(strings.ToLower(value), "claude-")
		return strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, value)
	}
	var selected *askModel
	for i := range p.Models {
		m := &p.Models[i]
		if selected == nil || m.Default {
			selected = m
		}
		if preferred != "" && (normalize(m.ID) == preferred || normalize(m.Label) == preferred) {
			selected = m
			break
		}
	}
	result := askSelection{Provider: p.ID, Effort: "Default"}
	if selected != nil {
		result.Model = selected.ID
		if slices.Contains(selected.Efforts, effort) {
			result.Effort = effort
		}
	}
	return result
}

// GUI launches often have only /usr/bin:/bin. Check fixed installation paths,
// without running a login shell or reading the user's shell startup scripts.
var askFindCLI = func(name string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	home, _ := os.UserHomeDir()
	for _, dir := range []string{filepath.Join(home, ".local", "bin"), filepath.Join(home, ".cargo", "bin"), "/opt/homebrew/bin", "/usr/local/bin"} {
		path := filepath.Join(dir, name)
		if st, err := os.Stat(path); err == nil && !st.IsDir() && st.Mode()&0111 != 0 {
			return path
		}
	}
	return ""
}

func askInstalled() []askProvider {
	result := []askProvider{}
	for _, name := range []string{"Codex", "Claude", "Antigravity"} {
		if path := askFindCLI(askProviderBinaries[name]); path != "" {
			result = append(result, askProvider{ID: name, path: path, Models: []askModel{}})
		}
	}
	return result
}

// Bound process output even if a provider prints an unexpected transcript.
// Returning a write error also stops an output flood before it fills memory.
type askOutput struct{ bytes.Buffer }

func (b *askOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 2<<20 {
		return 0, fmt.Errorf("CLI output exceeded 2 MiB")
	}
	return b.Buffer.Write(p)
}
func askCommand(ctx context.Context, path, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	cmd.Env = cliEnvironment(path)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	return cmd
}
func askReadProtocol(ctx context.Context, path, dir string, args []string, messages []any, match func(map[string]json.RawMessage) bool) (map[string]json.RawMessage, error) {
	cmd := askCommand(ctx, path, dir, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var diagnostics askOutput
	cmd.Stderr = &diagnostics
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	defer func() { _ = cmd.Cancel(); _ = cmd.Wait() }()
	for _, m := range messages {
		if err = json.NewEncoder(stdin).Encode(m); err != nil {
			return nil, err
		}
	}
	scan := bufio.NewScanner(io.LimitReader(stdout, 2<<20))
	scan.Buffer(make([]byte, 4096), 1<<20)
	for scan.Scan() {
		var value map[string]json.RawMessage
		if json.Unmarshal(scan.Bytes(), &value) == nil && match(value) {
			return value, nil
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, fmt.Errorf("CLI did not return a model catalog; update and authenticate it, then retry")
}

var askDiscoverModels = func(ctx context.Context, p askProvider) ([]askModel, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "pharos-models-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	switch p.ID {
	case "Codex":
		reply, err := askReadProtocol(ctx, p.path, dir, []string{"app-server"}, []any{
			map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]string{"name": "pharos", "version": "1"}}},
			map[string]any{"method": "initialized"}, map[string]any{"id": 2, "method": "model/list", "params": map[string]any{"limit": 100, "includeHidden": false}},
		}, func(v map[string]json.RawMessage) bool { return string(v["id"]) == "2" })
		if err != nil {
			return nil, err
		}
		return askCodexModels(reply["result"])
	case "Claude":
		reply, err := askReadProtocol(ctx, p.path, dir, []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--tools", "", "--strict-mcp-config", "--disable-slash-commands", "--setting-sources", "", "--settings", `{"disableAllHooks":true}`}, []any{map[string]any{"type": "control_request", "request_id": "models", "request": map[string]string{"subtype": "initialize"}}}, func(v map[string]json.RawMessage) bool { return string(v["type"]) == `"control_response"` })
		if err != nil {
			return nil, err
		}
		var response struct {
			Response struct {
				Models []struct {
					Value, ResolvedModel, DisplayName string
					SupportedEffortLevels             []string
				}
			}
		}
		if err = json.Unmarshal(reply["response"], &response); err != nil {
			return nil, err
		}
		result := []askModel{}
		seen := map[string]bool{}
		for _, m := range response.Response.Models {
			if m.ResolvedModel == "" || seen[m.ResolvedModel] {
				continue
			}
			seen[m.ResolvedModel] = true
			label := m.DisplayName
			if m.Value == "default" {
				label = m.ResolvedModel
			}
			result = append(result, askModel{ID: m.ResolvedModel, Label: label, Efforts: askEfforts(m.SupportedEffortLevels), Default: m.Value == "default"})
		}
		return result, nil
	case "Antigravity":
		cmd := askCommand(ctx, p.path, dir, "models")
		var out askOutput
		cmd.Stdout = &out
		cmd.Stderr = io.Discard
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("could not list Antigravity models; authenticate agy and retry")
		}
		return askAGYModels(out.String()), nil
	}
	return nil, fmt.Errorf("unsupported CLI")
}

func askEfforts(raw []string) []string {
	result := []string{"Default"}
	for _, value := range raw {
		if value != "" {
			result = append(result, strings.ToUpper(value[:1])+value[1:])
		}
	}
	return result
}
func askCodexModels(raw json.RawMessage) ([]askModel, error) {
	var catalog struct {
		Data []struct {
			Model, DisplayName        string
			IsDefault                 bool
			SupportedReasoningEfforts []struct{ ReasoningEffort string }
		}
		NextCursor *string
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil, err
	}
	if catalog.NextCursor != nil {
		return nil, fmt.Errorf("CLI model catalog is paginated; update Pharos to support this catalog")
	}
	result := []askModel{}
	for _, m := range catalog.Data {
		efforts := []string{}
		for _, e := range m.SupportedReasoningEfforts {
			efforts = append(efforts, e.ReasoningEffort)
		}
		result = append(result, askModel{ID: m.Model, Label: m.DisplayName, Default: m.IsDefault, Efforts: askEfforts(efforts)})
	}
	return result, nil
}
func askAGYModels(raw string) []askModel {
	result := []askModel{}
	indices := map[string]int{}
	for line := range strings.SplitSeq(raw, "\n") {
		id, label, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || id == "" {
			continue
		}
		family, effort := id, "Default"
		for _, tier := range []string{"low", "medium", "high", "xhigh", "max"} {
			if strings.HasSuffix(id, "-"+tier) {
				family = strings.TrimSuffix(id, "-"+tier)
				effort = strings.ToUpper(tier[:1]) + tier[1:]
				label = strings.TrimSuffix(label, " ("+effort+")")
				break
			}
		}
		index, ok := indices[family]
		if !ok {
			index = len(result)
			indices[family] = index
			result = append(result, askModel{ID: family, Label: label, Efforts: []string{"Default"}, Default: index == 0, Variants: map[string]string{"Default": id}})
		}
		if effort != "Default" && !slices.Contains(result[index].Efforts, effort) {
			result[index].Efforts = append(result[index].Efforts, effort)
		}
		result[index].Variants[effort] = id
	}
	return result
}
func askValidSelection(p askProvider, s askSelection) bool {
	for _, m := range p.Models {
		if m.ID == s.Model && slices.Contains(m.Efforts, s.Effort) {
			return true
		}
	}
	return false
}

// Each call gets an explicit connection to this library's read-only MCP server
// in a disposable private directory. Private history stays in the lookup loop;
// the provider receives no Pharos API token or persistent configuration edits.
var askRunCLI = func(ctx context.Context, p askProvider, s askSelection, prompt string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "pharos-ask-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	cmd, err := askPrepareCLI(ctx, p, s, dir)
	if err != nil {
		return nil, err
	}
	if p.ID == "Antigravity" {
		return askRunAGY(ctx, cmd, p, prompt)
	}
	cmd.Stdin = strings.NewReader(prompt)
	var diagnostics askOutput
	cmd.Stderr = &diagnostics
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	events := askCLIEvents{provider: p}
	output := &io.LimitedReader{R: stdout, N: 2<<20 + 1}
	scan := bufio.NewScanner(output)
	scan.Buffer(make([]byte, 4096), 2<<20+1)
	var result []byte
	for scan.Scan() {
		events.read(scan.Bytes())
		var envelope struct{ Type string }
		if p.ID == "Claude" && json.Unmarshal(scan.Bytes(), &envelope) == nil && envelope.Type == "result" {
			result = bytes.Clone(scan.Bytes())
		}
	}
	readErr := scan.Err()
	if output.N == 0 {
		readErr = fmt.Errorf("CLI output exceeded 2 MiB")
	}
	if readErr != nil {
		_ = cmd.Cancel()
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if readErr != nil {
		return nil, readErr
	}
	if err != nil {
		return nil, fmt.Errorf("%s could not complete the request. Check its CLI version, Pharos MCP connection, login and account limits in Terminal, then retry", p.ID)
	}
	if p.ID == "Codex" {
		file, err := os.Open(filepath.Join(dir, "answer.json"))
		if err != nil {
			return nil, err
		}
		defer file.Close()
		return io.ReadAll(io.LimitReader(file, 128<<10+1))
	}
	var envelope struct {
		StructuredOutput json.RawMessage `json:"structured_output"`
		Result           json.RawMessage `json:"result"`
		Response         json.RawMessage `json:"response"`
		IsError          bool            `json:"is_error"`
		Status           string          `json:"status"`
	}
	if err = json.Unmarshal(result, &envelope); err != nil {
		return nil, fmt.Errorf("%s returned invalid JSON", p.ID)
	}
	if envelope.IsError || envelope.Status == "ERROR" {
		return nil, fmt.Errorf("%s reported a request error. Check its login and account limits, then retry", p.ID)
	}
	for _, value := range []json.RawMessage{envelope.StructuredOutput, envelope.Response, envelope.Result} {
		if len(value) == 0 || string(value) == "null" {
			continue
		}
		var text string
		if json.Unmarshal(value, &text) == nil {
			return []byte(text), nil
		}
		return value, nil
	}
	return nil, fmt.Errorf("%s returned no response", p.ID)
}

func askPrepareCLI(ctx context.Context, p askProvider, s askSelection, dir string) (*exec.Cmd, error) {
	if p.mcp.Command == "" {
		return nil, fmt.Errorf("Pharos MCP connection is unavailable; retry from Pharos")
	}
	schema := filepath.Join(dir, "response.schema.json")
	if err := os.WriteFile(schema, []byte(askResponseSchema), 0600); err != nil {
		return nil, err
	}
	var args []string
	switch p.ID {
	case "Codex":
		// Codex asks to approve each MCP tool call, which approval_policy
		// "never" refuses; preapprove the read-only Pharos server's tools only,
		// as Claude's --allowedTools does.
		args = []string{"exec", "--json", "--ignore-user-config", "--ephemeral", "--skip-git-repo-check", "--sandbox", "read-only", "-c", `approval_policy="never"`, "-c", "features.shell_tool=false", "-c", "features.apply_patch_freeform=false", "-c", `web_search="disabled"`, "--model", s.Model, "--output-schema", schema, "--output-last-message", filepath.Join(dir, "answer.json"), "--color", "never", "-"}
		args = append(args[:len(args)-1], "-c", "mcp_servers.pharos.command="+jsonText(p.mcp.Command), "-c", "mcp_servers.pharos.args="+jsonText(p.mcp.Args), "-c", "mcp_servers.pharos.required=true", "-c", `mcp_servers.pharos.default_tools_approval_mode="approve"`, "-")
	case "Claude":
		args = []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--json-schema", askResponseSchema, "--model", s.Model, "--tools", "", "--strict-mcp-config", "--disable-slash-commands", "--setting-sources", "", "--settings", `{"disableAllHooks":true}`, "--no-session-persistence", "--permission-mode", "dontAsk"}
		args = append(args, "--mcp-config", jsonText(map[string]any{"mcpServers": map[string]askMCPServer{"pharos": p.mcp}}), "--allowedTools", "mcp__pharos")
	case "Antigravity":
		// Use a dedicated workspace agent, plan mode and the terminal sandbox.
		// AGY has no session-scoped permission flag: fail before sending private
		// context if its global policy would soft-deny the connected MCP tools.
		// Use a distinct name: AGY keeps an already-running global server when
		// another definition has the same name, even for a different library.
		if err := askCheckAGYMCPPermissions(); err != nil {
			return nil, err
		}
		agentDir := filepath.Join(dir, ".agents", "agents")
		if err := os.MkdirAll(agentDir, 0700); err != nil {
			return nil, err
		}
		mcpConfig := jsonText(map[string]any{"mcpServers": map[string]askMCPServer{"pharos-ask": p.mcp}})
		if err := os.WriteFile(filepath.Join(dir, ".agents", "mcp_config.json"), []byte(mcpConfig), 0600); err != nil {
			return nil, err
		}
		agent := `---
name: pharos-navigation
description: Pharos private navigation planner
mainAgent: true
subagent: false
tools:
  - finish
  - call_mcp_tool
mcpServers: []
skills: []
plugins: []
commandExecutionPolicy: off
---
Use only the pharos-ask MCP server, for archived work and for site links (browse_site, then site_link); take section ids and private history from the supplied JSON context. Do not use other MCP servers or native tools.
`
		if err := os.WriteFile(filepath.Join(agentDir, "pharos-navigation.md"), []byte(agent), 0600); err != nil {
			return nil, err
		}
		model := s.Model
		for _, m := range p.Models {
			if m.ID == s.Model && m.Variants[s.Effort] != "" {
				model = m.Variants[s.Effort]
			}
		}
		args = []string{"--input-format", "stream-json", "--output-format", "stream-json", "--json-schema", schema, "--model", model, "--agent", "pharos-navigation", "--sandbox", "--mode", "plan", "--disable-slash-commands", "--print-timeout", "3m"}

	default:
		return nil, fmt.Errorf("unsupported CLI")
	}
	if s.Effort != "Default" && p.ID != "Antigravity" {
		if p.ID == "Codex" {
			args = append(args[:len(args)-1], "-c", `model_reasoning_effort="`+strings.ToLower(s.Effort)+`"`, "-")
		} else {
			args = append(args, "--effort", strings.ToLower(s.Effort))
		}
	}
	return askCommand(ctx, p.path, dir, args...), nil
}

func askCheckAGYMCPPermissions() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	sharedPath := filepath.Join(home, ".gemini", "config", "config.json")
	type permissionGrants struct {
		Allow []string `json:"allow"`
		Ask   []string `json:"ask"`
		Deny  []string `json:"deny"`
	}
	var settings struct {
		ToolPermission string           `json:"toolPermission"`
		Permissions    permissionGrants `json:"permissions"`
	}
	var shared struct {
		UserSettings struct {
			GlobalPermissionGrants permissionGrants `json:"globalPermissionGrants"`
		} `json:"userSettings"`
	}
	readSettings := func(path string, value any) error {
		raw, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err == nil && len(raw) > 0 {
			err = json.Unmarshal(raw, value)
		}
		if err != nil {
			return fmt.Errorf("read Antigravity MCP permissions in %s: %w", path, err)
		}
		return nil
	}
	if err := readSettings(path, &settings); err != nil {
		return err
	}
	if err := readSettings(sharedPath, &shared); err != nil {
		return err
	}
	// AGY combines shared and CLI grants. Matching deny and ask rules still
	// take precedence over an allow, regardless of which file supplies it.
	grants := shared.UserSettings.GlobalPermissionGrants
	grants.Allow = append(grants.Allow, settings.Permissions.Allow...)
	grants.Ask = append(grants.Ask, settings.Permissions.Ask...)
	grants.Deny = append(grants.Deny, settings.Permissions.Deny...)
	for _, tool := range mcpTools {
		matches := func(rules []string) bool {
			return slices.Contains(rules, "mcp(*)") || slices.Contains(rules, "mcp(pharos-ask/*)") || slices.Contains(rules, "mcp(pharos-ask/"+firstString(tool["name"])+")")
		}
		if settings.ToolPermission == "strict" || matches(grants.Deny) || matches(grants.Ask) || settings.ToolPermission != "always-proceed" && !matches(grants.Allow) {
			return fmt.Errorf("Antigravity needs permission to use Pharos MCP. Allow mcp(pharos-ask/*) using /permissions in Antigravity and resolve any conflicting ask, deny, or strict policy, then retry")
		}
	}
	return nil
}

// Register AGY's retained transcript before sending any private context. The
// input stays on stdin, never in the process command line. Stop the whole
// process group on cancellation, including children holding output pipes.
func askRunAGY(ctx context.Context, cmd *exec.Cmd, p askProvider, prompt string) ([]byte, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var diagnostics askOutput
	cmd.Stderr = &diagnostics
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	defer func() { _ = cmd.Cancel(); _ = cmd.Wait() }()
	scan := bufio.NewScanner(io.LimitReader(stdout, 2<<20))
	scan.Buffer(make([]byte, 4096), 1<<20)
	events := askCLIEvents{provider: p}
	sent := false
	for scan.Scan() {
		var event struct {
			Event          string `json:"event"`
			ConversationID string `json:"conversation_id"`
			Result         struct {
				Status           string          `json:"status"`
				Response         string          `json:"response"`
				StructuredOutput json.RawMessage `json:"structured_output"`
			} `json:"result"`
		}
		if json.Unmarshal(scan.Bytes(), &event) != nil {
			continue
		}
		if event.Event == "init" && !sent {
			if event.ConversationID == "" || p.recordSession == nil {
				return nil, fmt.Errorf("Antigravity did not provide a private session identity")
			}
			if err = p.recordSession(ctx, event.ConversationID); err != nil {
				return nil, err
			}
			if err = json.NewEncoder(stdin).Encode(map[string]any{"event": "user", "message": map[string]string{"content": prompt}}); err != nil {
				return nil, err
			}
			_ = stdin.Close()
			sent = true
			p.reportProgress("Connected to Antigravity; processing your question…")
		}
		if sent {
			events.read(scan.Bytes())
		}
		if event.Event == "result" {
			if event.Result.Status != "SUCCESS" {
				return nil, fmt.Errorf("Antigravity could not complete the request. Check its CLI version, login and account limits in Terminal, then retry")
			}
			if len(event.Result.StructuredOutput) > 0 && string(event.Result.StructuredOutput) != "null" {
				return event.Result.StructuredOutput, nil
			}
			return []byte(event.Result.Response), nil
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, fmt.Errorf("Antigravity ended without a response")
}
