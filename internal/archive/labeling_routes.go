package archive

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Routes run a labeling request through a model CLI as an annotation-only
// worker: no tools, files, browser, MCP or delegation, untrusted corpus
// text on stdin, an ephemeral session, an empty private working directory.
// Each flag was checked against claude 2.1.288 and codex-cli 0.155.0 --help.
const (
	labelingRouteClaude = "claude"
	labelingRouteCodex  = "codex"
	// labelingRouteAgy is the Antigravity CLI (agy 1.4.8). Its model id is
	// the model and effort joined ("gemini-3.8-flash-low"); the prompt is
	// an argument (-p), as in the historical runner; it has no flag to
	// keep a session from being saved.
	labelingRouteAgy = "agy"
)

// labelingCLIModel is the model id a route's CLI is given.
func labelingCLIModel(route, model, effort string) string {
	if route == labelingRouteAgy {
		return model + "-" + effort
	}
	return model
}

// labelingCodexDisabled are codex features that give the model tools or
// actions: the historical runner's list (evidence/label-models/20261027/
// run_models.py) plus the rest that `codex features list` of codex-cli
// 0.155.0 shows enabled by default (image_generation, unified_exec,
// view_image, tool_suggest, skill_search, sleep_tool, goals,
// remote_plugin). Every name was confirmed in that listing. Web search is
// a config key, not a feature: --config web_search="disabled".
var labelingCodexDisabled = []string{"apps", "plugins", "shell_tool", "multi_agent", "browser_use", "browser_use_external",
	"computer_use", "in_app_browser", "code_mode_host", "code_mode", "image_generation", "unified_exec", "view_image", "tool_suggest",
	"skill_search", "sleep_tool", "goals", "remote_plugin"}

// Process timing, overridable by tests: a lingering CLI gets SIGTERM this
// long after its terminal event, and SIGKILL this long after SIGTERM.
var (
	labelingLingerGrace = 10 * time.Second
	labelingKillGrace   = 5 * time.Second
)

// labelingCommand is the argv for one attempt (without the CLI path), and
// the files the route reads or writes.
func labelingCommand(route, model, effort, schemaText, schemaPath, answerPath, sessionID string) ([]string, error) {
	switch route {
	case labelingRouteClaude:
		return []string{"-p", "--model", model, "--effort", effort, "--output-format", "stream-json", "--verbose",
			"--json-schema", schemaText, "--tools", "", "--safe-mode", "--disable-slash-commands", "--no-chrome", "--strict-mcp-config",
			"--permission-prompts", "none", "--permission-mode", "dontAsk", "--no-session-persistence", "--session-id", sessionID,
			// A named session skips Claude Code's own title-generation call
			// (an auxiliary claude-haiku-4-5 request of 5-7k input tokens,
			// costlier than the labeling call in the 2026-10-27 pilot).
			"--name", "Pharos labeling", "--prompt-suggestions", "false"}, nil
	case labelingRouteCodex:
		args := []string{"exec", "--ephemeral", "--ignore-user-config", "--ignore-rules", "--skip-git-repo-check", "--sandbox", "read-only",
			"--model", model, "--config", `model_reasoning_effort="` + effort + `"`, "--config", "project_doc_max_bytes=0", "--config", `web_search="disabled"`, "--color", "never",
			"--json", "--output-schema", schemaPath, "--output-last-message", answerPath}
		for _, feature := range labelingCodexDisabled {
			args = append(args, "--disable", feature)
		}
		return append(args, "-"), nil
	case labelingRouteAgy:
		// --print-timeout and the prompt (-p PROMPT, last) are added by the
		// runner.
		return []string{"--model", labelingCLIModel(route, model, effort), "--sandbox", "--disable-slash-commands", "--output-format", "stream-json",
			"--json-schema", schemaPath}, nil
	}
	return nil, errors.New("unknown route " + route + ": use claude, codex or agy")
}

// labelingStream accumulates what an attempt's event stream says. It reads
// sanitized events, so replaying a saved events.jsonl rebuilds the same
// state the live run saw.
type labelingStream struct {
	route     string
	SessionID string           `json:"session_id,omitempty"`
	Models    []string         `json:"reported_models,omitempty"`
	Terminal  string           `json:"terminal_event,omitempty"`
	Failed    bool             `json:"failed,omitempty"`
	Error     string           `json:"error,omitempty"`
	Refusal   string           `json:"refusal,omitempty"`
	Tools     []map[string]any `json:"tool_events,omitempty"`
	Warnings  []string         `json:"warnings,omitempty"`
	Unparsed  int              `json:"unparsed_lines,omitempty"`
	// Started is set by the route's first session event (Claude
	// system/init, Codex thread.started): before it nothing was sent.
	Started                bool `json:"started,omitempty"`
	structured, structTool json.RawMessage
	// errorCode is the CLI's or provider's own error description (Claude's
	// subtype and errors, Codex's failure message), never echoed model or
	// corpus text: refusal errors are recognized only here.
	errorCode  string
	text       string
	usage      map[string]any
	modelUsage map[string]map[string]any
	cliCost    *float64
	toolIDs    map[string]bool
}

func newLabelingStream(route string) *labelingStream {
	return &labelingStream{route: route, toolIDs: map[string]bool{}}
}

func (s *labelingStream) model(name string) {
	if name != "" && !containsString(s.Models, name) {
		s.Models = append(s.Models, name)
	}
}

// sanitize keeps what the ledger needs from one raw event and drops private
// reasoning text: Claude thinking blocks and Codex reasoning items keep
// only their type and id. Usage is kept.
func (s *labelingStream) sanitize(event map[string]any) map[string]any {
	kind := firstString(event["type"])
	if s.route == labelingRouteClaude {
		switch kind {
		case "system":
			if event["subtype"] == "init" {
				return pick(event, "type", "subtype", "session_id", "model", "tools", "mcp_servers", "claude_code_version", "permissionMode")
			}
			return pick(event, "type", "subtype", "session_id")
		case "assistant", "user":
			message := mapValueDefault(event["message"])
			content := []any{}
			if items, ok := message["content"].([]any); ok {
				for _, raw := range items {
					block := mapValueDefault(raw)
					switch firstString(block["type"]) {
					case "thinking", "redacted_thinking":
						content = append(content, map[string]any{"type": block["type"]})
					case "tool_result":
						kept := pick(block, "type", "tool_use_id", "is_error")
						if text, ok := block["content"].(string); ok && len(text) <= 500 {
							kept["content"] = text
						}
						content = append(content, kept)
					default:
						content = append(content, block)
					}
				}
			}
			kept := pick(message, "id", "model", "role", "stop_reason", "usage")
			kept["content"] = content
			out := pick(event, "type", "session_id", "parent_tool_use_id")
			out["message"] = kept
			return out
		case "result":
			return event
		}
		return pick(event, "type", "subtype")
	}
	if s.route == labelingRouteAgy {
		switch firstString(event["event"]) {
		case "init":
			return pick(event, "event", "init", "conversation_id", "model")
		case "step_update":
			step := mapValueDefault(event["step_update"])
			return map[string]any{"event": "step_update", "step_update": pick(step, "step_type", "state", "tool_name", "usage", "model", "duration_seconds")}
		case "result":
			return event
		}
		return pick(event, "event")
	}
	if item := mapValue(event["item"]); item != nil {
		out := pick(event, "type")
		switch firstString(item["type"]) {
		case "reasoning":
			out["item"] = pick(item, "id", "type")
		case "agent_message":
			out["item"] = pick(item, "id", "type", "text")
		case "error":
			out["item"] = pick(item, "id", "type", "message")
		default:
			out["item"] = pick(item, "id", "type", "status", "server", "tool", "name")
		}
		return out
	}
	return event
}

// observe updates the stream state from one sanitized event.
func (s *labelingStream) observe(event map[string]any) {
	kind := firstString(event["type"])
	if s.route == labelingRouteClaude {
		switch kind {
		case "system":
			if event["subtype"] == "init" {
				s.Started = true
				s.SessionID = defaultString(firstString(event["session_id"]), s.SessionID)
				s.model(firstString(event["model"]))
			}
		case "assistant":
			message := mapValueDefault(event["message"])
			s.model(firstString(message["model"]))
			if message["stop_reason"] == "refusal" {
				s.Refusal = "stop_reason refusal"
			}
			items, _ := message["content"].([]any)
			for _, raw := range items {
				block := mapValueDefault(raw)
				if block["type"] != "tool_use" {
					continue
				}
				if block["name"] == "StructuredOutput" {
					s.structTool, _ = json.Marshal(block["input"])
					continue
				}
				id := firstString(block["id"])
				if id == "" || !s.toolIDs[id] {
					s.toolIDs[id] = true
					s.Tools = append(s.Tools, map[string]any{"name": block["name"], "id": id})
				}
			}
		case "result":
			s.Terminal = "result"
			s.SessionID = defaultString(firstString(event["session_id"]), s.SessionID)
			s.Failed = event["is_error"] == true || firstString(event["subtype"]) != "success"
			if s.Failed {
				s.Error = defaultString(firstString(event["subtype"]), "error")
				if errs, ok := event["errors"].([]any); ok && len(errs) > 0 {
					s.Error += ": " + compactJSON(errs)
				}
				s.errorCode = s.Error
				// Claude Code writes its own errors into the result text with
				// an "API Error:" prefix (a policy block reads "API Error: ...
				// violate our Usage Policy"): that text is the CLI's, not the
				// model's, and counts as an error description.
				if text := strings.TrimSpace(firstString(event["result"])); strings.HasPrefix(text, "API Error:") {
					s.errorCode += " " + firstRunes(text, 500)
				}
				if text := firstString(event["result"]); text != "" {
					s.Error += ": " + firstRunes(text, 500)
				}
			}
			if event["stop_reason"] == "refusal" {
				s.Refusal = "stop_reason refusal"
			}
			if value, ok := event["structured_output"]; ok && value != nil {
				s.structured, _ = json.Marshal(value)
			}
			s.text = firstString(event["result"])
			s.usage = mapValue(event["usage"])
			s.modelUsage = map[string]map[string]any{}
			for model, usage := range mapValueDefault(event["modelUsage"]) {
				if m := mapValue(usage); m != nil {
					s.modelUsage[model] = m
				}
			}
			if cost, ok := number(event["total_cost_usd"]); ok {
				s.cliCost = &cost
			}
		}
		return
	}
	if s.route == labelingRouteAgy {
		s.observeAgy(event)
		return
	}
	switch kind {
	case "thread.started":
		s.Started = true
		s.SessionID = firstString(event["thread_id"])
	case "turn.completed":
		s.Terminal = kind
		s.usage = mapValue(event["usage"])
	case "turn.failed":
		s.Terminal, s.Failed = kind, true
		s.usage = mapValue(event["usage"])
		failure := mapValueDefault(event["error"])
		s.Error = defaultString(firstString(failure["message"]), "turn failed")
		s.errorCode = strings.TrimSpace(firstString(failure["code"]) + " " + firstString(failure["type"]) + " " + s.Error)
	case "error":
		s.Warnings = append(s.Warnings, firstString(event["message"]))
	}
	item := mapValue(event["item"])
	if item == nil {
		return
	}
	switch itemType := firstString(item["type"]); itemType {
	case "agent_message":
		if kind == "item.completed" {
			s.text = firstString(item["text"])
		}
	case "reasoning":
	case "error":
		s.Warnings = append(s.Warnings, firstString(item["message"]))
	default:
		id := firstString(item["id"])
		if id == "" || !s.toolIDs[id] {
			s.toolIDs[id] = true
			s.Tools = append(s.Tools, map[string]any{"item_type": itemType, "id": id})
		}
	}
}

// observeAgy reads Antigravity events: init (the conversation), step
// updates (any tool other than the structured finish is tool use), and the
// result (status, structured_output, usage).
func (s *labelingStream) observeAgy(event map[string]any) {
	switch firstString(event["event"]) {
	case "init":
		s.Started = true
		init := mapValueDefault(event["init"])
		s.SessionID = defaultString(firstString(event["conversation_id"]), firstString(init["conversation_id"]))
		s.model(defaultString(firstString(event["model"]), firstString(init["model"])))
	case "step_update":
		step := mapValueDefault(event["step_update"])
		s.model(firstString(step["model"]))
		// Steps seen in agy 1.4.8 for an annotation: user_input,
		// agent_response, finish. Anything else, or any named tool other
		// than finish, is tool use (the init event lists browser, shell and
		// file tools as available).
		stepType := strings.ToLower(firstString(step["step_type"]))
		tool := firstString(step["tool_name"])
		if tool == "" && stepType != "" && stepType != "user_input" && stepType != "agent_response" && stepType != "planner_response" && stepType != "finish" {
			tool = stepType
		}
		if tool != "" && tool != "finish" {
			key := tool + "/" + stepType
			if !s.toolIDs[key] {
				s.toolIDs[key] = true
				s.Tools = append(s.Tools, map[string]any{"name": tool})
			}
		}
	case "result":
		result := mapValueDefault(event["result"])
		s.Terminal = "result"
		s.Started = true
		s.model(firstString(result["model"]))
		status := firstString(result["status"])
		s.Failed = status != "SUCCESS"
		if s.Failed {
			s.Error = defaultString(status, "no status")
			if text := firstString(result["error"]); text != "" {
				s.Error += ": " + firstRunes(text, 500)
			} else if errorValue := result["error"]; errorValue != nil {
				s.Error += ": " + firstRunes(compactJSON(errorValue), 500)
			}
			s.errorCode = s.Error
		}
		if value, ok := result["structured_output"]; ok && value != nil {
			s.structured, _ = json.Marshal(value)
		}
		s.text = firstString(result["response"])
		s.usage = labelingAgyUsage(mapValue(result["usage"]))
		if cost, ok := number(result["cost_usd"]); ok {
			s.cliCost = &cost
		}
	}
}

// labelingAgyUsage maps Antigravity's usage counters to the standard
// names: cached tokens are part of the input (as Gemini reports them), and
// thinking tokens are added to output, which can only overstate a cost. A
// usage whose input and output are both zero is no usage at all: the
// attempt is then priced by estimate, never at $0.
func labelingAgyUsage(usage map[string]any) map[string]any {
	if usage == nil {
		return nil
	}
	input, _ := number(usage["input_tokens"])
	output, _ := number(usage["output_tokens"])
	thinking, _ := number(usage["thinking_tokens"])
	cached, _ := number(usage["cache_read_tokens"])
	if input <= 0 && output <= 0 {
		return nil
	}
	return map[string]any{"input_tokens": input, "output_tokens": output + thinking, "cached_input_tokens": cached,
		"total_tokens": input + output + thinking}
}

// line handles one raw stdout line: it returns the sanitized event to
// save, or nil for a line that is not a JSON object.
func (s *labelingStream) line(raw []byte) []byte {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	var event map[string]any
	if err := json.Unmarshal(raw, &event); err != nil || event == nil {
		s.Unparsed++
		data, _ := json.Marshal(map[string]any{"type": "pharos.unparsed_line", "bytes": len(raw)})
		return data
	}
	event = s.sanitize(event)
	s.observe(event)
	data, _ := json.Marshal(event)
	return data
}

// replayLabelingStream rebuilds the state from a saved events.jsonl.
func replayLabelingStream(route, path string) (*labelingStream, error) {
	stream := newLabelingStream(route)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return stream, nil
		}
		return nil, err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for {
		raw, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(raw)) > 0 {
			var event map[string]any
			if json.Unmarshal(raw, &event) == nil {
				if event["type"] == "pharos.unparsed_line" {
					stream.Unparsed++
				} else {
					stream.observe(event)
				}
			}
		}
		if err == io.EOF {
			return stream, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// output returns the structured annotation: Claude's result
// structured_output (or its StructuredOutput call), Codex's last message
// file or last agent message.
func (s *labelingStream) output(answerPath string) json.RawMessage {
	if s.route == labelingRouteClaude || s.route == labelingRouteAgy {
		if len(s.structured) > 0 && string(s.structured) != "null" {
			return s.structured
		}
		if len(s.structTool) > 0 && string(s.structTool) != "null" {
			return s.structTool
		}
		return nil
	}
	text := s.text
	if answerPath != "" {
		if data, err := os.ReadFile(answerPath); err == nil && len(bytes.TrimSpace(data)) > 0 {
			text = string(data)
		}
	}
	text = strings.TrimSpace(text)
	if json.Valid([]byte(text)) && strings.HasPrefix(text, "{") {
		return json.RawMessage(text)
	}
	return nil
}

// labelingAccountError recognizes an authentication, rate-limit or quota
// error in a CLI's terminal error: every further request would fail the
// same way, so the run halts at once.
var labelingAccountError = regexp.MustCompile(`(?i)(rate[ _-]?limit|too many requests|\b429\b|quota|usage limit|credit balance|billing|\b40[13]\b|unauthori[sz]ed|forbidden|authenticat|invalid (api|x-api)[ _-]?key|not logged in|log ?in again|/login|expired (token|session|credentials))`)

// labelingNetworkError recognizes a connection failure: no request can
// get through, so the run halts at once (and it is never a refusal).
var labelingNetworkError = regexp.MustCompile(`(?i)(connection refused|\bECONNREFUSED\b|\bECONNRESET\b|\bENOTFOUND\b|\bETIMEDOUT\b|\bEAI_AGAIN\b|network is unreachable|could not resolve host|tcp connect error|dns error)`)

// refusalText finds refusal words anywhere; alone they only make a reply
// suspected (an evidence quote may say "I can't get the build to pass").
var refusalText = regexp.MustCompile(`(?i)\b(I can(no|')t|I cannot|I won'?t|I'm (not able|unable)|I am (not able|unable)|unable to (help|comply|assist)|I must decline)\b`)

// labelingRefusalOpening is a reply that opens by declining.
var labelingRefusalOpening = regexp.MustCompile(`(?i)^(sorry,? (but )?)?(I can(no|')t|I cannot|I won'?t|I'm (not able|unable)|I am (not able|unable)|I must decline|I'm sorry,? but)\b`)

// labelingOpensWithRefusal reports a reply that is a refusal: not
// JSON-like, and opening with a refusal phrase.
func labelingOpensWithRefusal(text string) bool {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") || strings.Contains(text, `"annotations"`) {
		return false
	}
	return labelingRefusalOpening.MatchString(text)
}

// labelingRefusalError recognizes a provider or CLI error code or message
// that is a policy refusal: explicit, word-bounded policy or refusal
// wording only (Anthropic's "violate our Usage Policy", OpenAI's
// content-policy and invalid_prompt codes, an explicit "refusal" or
// "refused"; labelingNetworkError is checked first, so "Connection
// refused" is a failure). It is matched against labelingStream.errorCode,
// never against echoed model or corpus text, and never matches connection
// errors.
var labelingRefusalError = regexp.MustCompile(`(?i)(\busage polic(y|ies)\b|\bcontent[_ ]policy\b|\bcontent[_ ]filter\b|\binvalid_prompt\b|\bpolicy[_ ]violation\b|\bsafety[_ ]system\b|\brefusal\b|\brefused\b)`)

// labelingProcess is how one CLI process ended.
type labelingProcess struct {
	StartError string           `json:"start_error,omitempty"`
	ExitCode   *int             `json:"exit_code,omitempty"`
	TimedOut   bool             `json:"timed_out,omitempty"`
	Stopped    bool             `json:"stopped,omitempty"`
	Cleanup    []map[string]any `json:"cleanup,omitempty"`
	PID        int              `json:"pid,omitempty"`
	// ReceiptError is the first failure to write or sync the saved stream:
	// the attempt's receipt is then not durable.
	ReceiptError string        `json:"receipt_error,omitempty"`
	Duration     time.Duration `json:"-"`
}

// labelingReceipt is where an attempt's sanitized stream is saved.
type labelingReceipt interface {
	io.Writer
	Sync() error
}

// cliEnvironment is the user's environment, so the CLI finds its login,
// without variables that would make it act as a nested Claude Code or
// reach Pharos's API, and with the binary's directory on PATH for native
// launchers which locate node. Labeling records none of it.
func cliEnvironment(path string) []string {
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key != "CLAUDECODE" && key != "CLAUDE_CODE_ENTRYPOINT" && key != "PHAROS_API_TOKEN" {
			env = append(env, entry)
		}
	}
	return append(env, "PATH="+filepath.Dir(path)+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// runLabelingProcess runs one CLI attempt: the prompt goes to stdin, every
// stdout line is sanitized into events, and the process group is cleaned up
// after a terminal event (labelingLingerGrace, SIGTERM, labelingKillGrace,
// SIGKILL), on timeout or on a hard stop. started is called with the pid
// once the process runs.
func runLabelingProcess(path string, args []string, dir, prompt string, events labelingReceipt, stderr *os.File, stream *labelingStream, mu *sync.Mutex,
	timeout time.Duration, hardStop <-chan struct{}, started func(pid int)) (result labelingProcess) {
	begin := time.Now()
	defer func() { result.Duration = time.Since(begin) }()
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		result.StartError = err.Error()
		return result
	}
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		stdoutR.Close()
		stdoutW.Close()
		result.StartError = err.Error()
		return result
	}
	cmd := exec.Command(path, args...)
	cmd.Dir, cmd.Env = dir, cliEnvironment(path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinR, stdoutW, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err = cmd.Start()
	stdoutW.Close()
	stdinR.Close()
	if err != nil {
		stdoutR.Close()
		stdinW.Close()
		result.StartError = err.Error()
		return result
	}
	pid := cmd.Process.Pid
	result.PID = pid
	if started != nil {
		started(pid)
	}
	go func() {
		io.WriteString(stdinW, prompt)
		stdinW.Close()
	}()
	terminal := make(chan struct{})
	readerDone := make(chan struct{})
	var receiptError error
	go func() {
		defer close(readerDone)
		reader := bufio.NewReaderSize(stdoutR, 1<<16)
		signaled := false
		for {
			raw, err := reader.ReadBytes('\n')
			if len(raw) > 0 {
				mu.Lock()
				saved := stream.line(raw)
				isTerminal := stream.Terminal != ""
				mu.Unlock()
				// The CLI is still read to the end after a write fails, so it
				// never blocks on a full pipe; the attempt stays uncertain.
				if saved != nil && receiptError == nil {
					if _, err := events.Write(append(saved, '\n')); err != nil {
						receiptError = fmt.Errorf("write: %w", err)
					}
				}
				if isTerminal && !signaled {
					if receiptError == nil {
						if err := events.Sync(); err != nil {
							receiptError = fmt.Errorf("sync: %w", err)
						}
					}
					signaled = true
					close(terminal)
				}
			}
			if err != nil {
				return
			}
		}
	}()
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()
	signalGroup := func(signal syscall.Signal, reason string) {
		if err := syscall.Kill(-pid, signal); err == nil || !errors.Is(err, syscall.ESRCH) {
			result.Cleanup = append(result.Cleanup, map[string]any{"signal": signal.String(), "reason": reason,
				"after_seconds": time.Since(begin).Seconds()})
		}
	}
	timeoutTimer := time.NewTimer(timeout)
	defer timeoutTimer.Stop()
	var linger, kill <-chan time.Time
	terminalWait := (<-chan struct{})(terminal)
	terminalSeen, terminated := false, false
	terminate := func(reason string) {
		if terminated {
			return
		}
		terminated = true
		signalGroup(syscall.SIGTERM, reason)
		kill = time.After(labelingKillGrace)
	}
wait:
	for {
		select {
		case <-exited:
			break wait
		case <-terminalWait:
			terminalWait = nil
			terminalSeen = true
			linger = time.After(labelingLingerGrace)
		case <-linger:
			terminate("lingering after its terminal event")
		case <-timeoutTimer.C:
			if !terminalSeen {
				result.TimedOut = true
				terminate("timeout without a terminal event")
			}
		case <-hardStop:
			hardStop = nil
			if !terminalSeen {
				result.Stopped = true
				terminate("stopped")
			}
		case <-kill:
			signalGroup(syscall.SIGKILL, "still running after SIGTERM")
			kill = nil
		}
	}
	// Children that outlive the CLI are part of the attempt too.
	if syscall.Kill(-pid, 0) == nil {
		signalGroup(syscall.SIGTERM, "process group outlived the CLI")
		deadline := time.Now().Add(labelingKillGrace)
		for syscall.Kill(-pid, 0) == nil && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if syscall.Kill(-pid, 0) == nil {
			signalGroup(syscall.SIGKILL, "process group still running after SIGTERM")
		}
	}
	select {
	case <-readerDone:
	case <-time.After(2 * time.Second):
		stdoutR.SetReadDeadline(time.Now())
		<-readerDone
	}
	stdoutR.Close()
	if receiptError != nil {
		result.ReceiptError = receiptError.Error()
	} else if err := events.Sync(); err != nil {
		result.ReceiptError = "sync: " + err.Error()
	}
	if cmd.ProcessState != nil {
		code := cmd.ProcessState.ExitCode()
		result.ExitCode = &code
	}
	return result
}

// labelingCLIVersion asks a CLI for its version once per job.
func labelingCLIVersion(path string) string {
	cmd := exec.Command(path, "--version")
	cmd.Env = cliEnvironment(path)
	cmd.Dir = os.TempDir()
	done := make(chan struct{})
	var output []byte
	go func() {
		output, _ = cmd.Output()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		<-done
	}
	return strings.TrimSpace(string(output))
}
