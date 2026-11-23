package archive

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// TL1 runs Antigravity agents through the agy CLI and records its stream-json
// output as <attempt>-agy.jsonl:
//
//	{"event":"init","conversation_id":<uuid>,"init":{"model":…,"cwd":…}}
//	{"event":"step_update","step_update":{"step_index":N,"state":"ACTIVE|DONE|ERROR","step_type":…}}
//	{"event":"result","result":{"status":…,"response":…,"error":…,"usage":{…}}}
//
// The conversation ID is Antigravity's own: the name of the session's brain
// folder and requests database, which the Antigravity source indexes. It
// becomes the TL1 conversation's alias, so the two link as one conversation
// and TL1's copy, the workspace that counts, carries its usage.
//
// The stream holds less than Antigravity's own records: read_file output is
// summarized and write/edit output can be empty. run_command retains command
// output and exit lines, except when replaced by a truncation notice. Events
// have no timestamp or reasoning text. Coverage is therefore partial. The prompt
// is the stdin_text of the sibling <attempt>-agy-inputs.ndjson,
// whose launch and exit times bound the run.

// tl1AGYInputsSuffix names the file beside an agy transcript that records
// what TL1 sent it.
const tl1AGYInputsSuffix = "-agy-inputs.ndjson"

var agyCommandExit = regexp.MustCompile(`(?m)^Command exited with code (-?[0-9]+)\.\r?$`)

// Only the CLI's header is exit-status evidence. A command may print that
// same sentence in its own output, which must not change its recorded status.
func agyExitCode(output string) (int, bool) {
	header, _, _ := strings.Cut(output, "Command output:")
	match := agyCommandExit.FindStringSubmatch(header)
	if len(match) != 2 {
		return 0, false
	}
	code, err := strconv.Atoi(match[1])
	return code, err == nil
}

// tl1AGYTranscript reports a TL1 file name of an agy transcript.
func tl1AGYTranscript(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), "-agy.jsonl")
}

// agyModel splits the effort suffix TL1 and the agy CLI add to a model
// selection (gemini-4-neon-medium) from the model Antigravity serves and
// prices (gemini-4-neon).
func agyModel(selection string) (model, effort string) {
	selection = strings.TrimSpace(selection)
	for _, level := range []string{"minimal", "low", "medium", "high", "xhigh", "max"} {
		if base, found := strings.CutSuffix(selection, "-"+level); found && base != "" {
			return base, level
		}
	}
	return selection, ""
}

// agyUsage is a step's usage in the shape the native Antigravity source
// records (see antigravityRequest.tokenUsage): input without cached input,
// and output that already includes thinking.
func agyUsage(usage map[string]any) map[string]any {
	count := func(name string) int64 { return integer(usage[name]) }
	return map[string]any{"input_tokens": count("input_tokens"), "cache_read_input_tokens": count("cache_read_tokens"),
		"output_tokens": count("output_tokens"), "reasoning_output_tokens": count("thinking_tokens")}
}

// agyUsageFields are the counts agyUsage reads, which a result's totals are
// compared on.
var agyUsageFields = []string{"input_tokens", "cache_read_tokens", "output_tokens", "thinking_tokens"}

type agyInvocation struct {
	prompts             []string
	launched, completed string
}

// agyInvocation reads the inputs TL1 recorded for an agy run: each launch's
// stdin, an agy user event whose message content is the prompt, and the
// launch and exit times. A missing file yields nothing.
func (s sourceFiles) agyInvocation(path string) agyInvocation {
	result := agyInvocation{}
	file, err := s.Open(path)
	if err != nil {
		return result
	}
	defer file.Close()
	scan := bufio.NewScanner(file)
	scan.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for scan.Scan() {
		var event map[string]any
		if json.Unmarshal(scan.Bytes(), &event) != nil {
			continue
		}
		switch firstString(event["event"]) {
		case "launch_requested":
			if result.launched == "" {
				result.launched = iso(event["observed_at"])
			}
			stdin := firstString(event["stdin_text"])
			if stdin == "" {
				continue
			}
			var sent map[string]any
			if json.Unmarshal([]byte(stdin), &sent) == nil {
				if content, ok := mapValue(sent["message"])["content"].(string); ok {
					stdin = content
				}
			}
			result.prompts = append(result.prompts, stdin)
		case "process_completed":
			result.completed = iso(event["observed_at"])
		}
	}
	return result
}

// parseAGYStream reads an agy stream-json transcript. origin is the path the
// file stands for (see baseAdapter.original), which evidence locators name;
// inputs is the sibling inputs file, read through the same view.
func (s sourceFiles) parseAGYStream(path, origin, inputs string) (ConversationRecord, bool, error) {
	file, err := s.Open(path)
	if err != nil {
		return ConversationRecord{}, false, nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ConversationRecord{}, false, nil
	}
	invocation := s.agyInvocation(inputs)
	// Exactly the bytes present when it was opened, as jsonlAdapter.parseFile.
	reader := bufio.NewReaderSize(io.LimitReader(file, info.Size()), 64*1024)
	messages := []MessageRecord{}
	conversationID, model, effort := "", "", ""
	line, prompt := 0, 0
	recognized := false
	locator := func(at int) string { return fmt.Sprintf("%s:%d", origin, at) }
	add := func(id, role, kind, text, callID string, at int) {
		messages = append(messages, MessageRecord{NativeID: id, Role: role, Kind: kind, Text: text, Effort: effort, CallID: callID, EvidenceLocator: locator(at), Selected: true})
	}
	// A response streams its text as deltas while ACTIVE; its DONE event
	// carries the last delta and the request's usage.
	type response struct {
		text  strings.Builder
		line  int
		ended bool
	}
	responses := map[int64]*response{}
	responseOrder := []int64{}
	// A tool step reports its parameters while ACTIVE and its output (or
	// error) when it ends; a call still running when the stream stops keeps
	// only its parameters.
	type call struct {
		name   string
		input  any
		line   int
		closed bool
	}
	calls := map[int64]*call{}
	callOrder := []int64{}
	usageSteps := map[int64]bool{}
	stepUsage := map[string]int64{}
	replied := false
	flush := func(index int64) {
		r := responses[index]
		if r == nil || r.ended {
			return
		}
		r.ended = true
		if text := strings.TrimSpace(r.text.String()); text != "" {
			add(fmt.Sprintf("step-%d", index), "assistant", "message", r.text.String(), "", r.line)
			replied = true
		}
	}
	openCall := func(index int64, step map[string]any) *call {
		c := calls[index]
		if c == nil {
			c = &call{line: line}
			calls[index] = c
			callOrder = append(callOrder, index)
		}
		details := mapValue(step["tool_info"])
		c.name = defaultString(firstNonNil(nilIfEmpty(firstString(step["tool_name"])), details["name"]), defaultString(c.name, "tool"))
		if parameters := details["parameters"]; parameters != nil {
			c.input = parameters
		}
		return c
	}
	emitCall := func(index int64, c *call) {
		kind := "tool_call"
		if c.name == "invoke_subagent" {
			kind = "delegation"
		}
		id := fmt.Sprintf("step-%d", index)
		add(id+":call", "assistant", kind, jsonText(map[string]any{"tool": c.name, "input": mapValueDefault(c.input)}), id, c.line)
	}
	var result map[string]any
	resultLine := 0
	for {
		encoded, readErr := reader.ReadBytes('\n')
		if len(encoded) == 0 && readErr == io.EOF {
			break
		}
		if readErr != nil && readErr != io.EOF {
			return ConversationRecord{}, false, fmt.Errorf("read %s line %d: %w", path, line+1, readErr)
		}
		line++
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		var event map[string]any
		if decoder.Decode(&event) != nil {
			if readErr == io.EOF {
				break
			}
			continue
		}
		switch firstString(event["event"]) {
		case "init":
			recognized = true
			conversationID = defaultString(event["conversation_id"], conversationID)
			details := mapValue(event["init"])
			if selection := firstString(details["model"]); selection != "" {
				var level string
				model, level = agyModel(selection)
				effort = defaultString(level, effort)
			}
		case "step_update":
			recognized = true
			step := mapValue(event["step_update"])
			conversationID = defaultString(step["conversation_id"], conversationID)
			index := integer(step["step_index"])
			state := firstString(step["state"])
			id := fmt.Sprintf("step-%d", index)
			switch firstString(step["step_type"]) {
			case "user_input":
				if state == "ACTIVE" {
					break
				}
				if prompt < len(invocation.prompts) {
					item := MessageRecord{NativeID: id, Role: "user", Kind: "message", Text: invocation.prompts[prompt], Effort: effort, CreatedAt: invocation.launched, EvidenceLocator: locator(line), Selected: true}
					if prompt > 0 {
						item.CreatedAt = ""
					}
					messages = append(messages, item)
				}
				prompt++
			case "agent_response":
				r := responses[index]
				if r == nil {
					r = &response{line: line}
					responses[index] = r
					responseOrder = append(responseOrder, index)
				}
				// Deltas split words anywhere, so their spaces are kept.
				delta, _ := step["text_delta"].(string)
				r.text.WriteString(delta)
				if state == "ACTIVE" {
					break
				}
				flush(index)
				if usage := mapValue(step["usage"]); usage != nil && !usageSteps[index] {
					usageSteps[index] = true
					for _, field := range agyUsageFields {
						stepUsage[field] += integer(usage[field])
					}
					add(id+":usage", "system", "metadata", jsonText(map[string]any{"type": "token_usage", "usage": agyUsage(usage), "usage_message_id": conversationID + ":" + id, "model_usage": usage}), "", line)
				}
			case "tool":
				c := openCall(index, step)
				if state == "ACTIVE" || c.closed {
					break
				}
				c.closed = true
				emitCall(index, c)
				details := mapValue(step["tool_info"])
				failure := mapValue(details["error"])
				failed := state == "ERROR" || toolResultHasError(details["error"]) || toolResultFailed(details)
				payload := antigravityToolResult(c.name, firstNonNil(details["output"], failure["message"]), failed, "")
				if toolResultHasError(details["error"]) {
					extra := mapValueDefault(payload["details"])
					extra["error"] = firstNonNil(failure["message"], details["error"])
					payload["details"] = extra
				}
				kind := "tool_result"
				if c.name == "invoke_subagent" {
					kind = "delegation_result"
				}
				add(id+":result", "tool", kind, jsonText(payload), id, line)
			default:
				// System notices and error markers carry no text of their
				// own; the result names a run's error.
				if state == "ACTIVE" {
					break
				}
				add(id, "system", "metadata", jsonText(step), "", line)
			}
		case "result":
			recognized = true
			result, resultLine = mapValue(event["result"]), line
			conversationID = defaultString(result["conversation_id"], conversationID)
		}
		if readErr == io.EOF {
			break
		}
	}
	for _, index := range responseOrder {
		flush(index)
	}
	for _, index := range callOrder {
		if c := calls[index]; !c.closed {
			emitCall(index, c)
		}
	}
	if result != nil {
		status := firstString(result["status"])
		if text, _ := result["response"].(string); strings.TrimSpace(text) != "" && !replied {
			add("result", "assistant", "message", text, "", resultLine)
		}
		if status != "" && status != "SUCCESS" {
			add("result:error", "system", "error", defaultString(result["error"], "Antigravity ended with status "+status), "", resultLine)
		}
		// The run's totals cover requests no step reported, such as a
		// step whose usage the stream left out; the difference is recorded
		// as one more request so the conversation adds up to them.
		if usage := mapValue(result["usage"]); usage != nil {
			remainder, unreported, consistent := map[string]any{}, false, true
			for _, field := range agyUsageFields {
				value := integer(usage[field]) - stepUsage[field]
				consistent = consistent && value >= 0
				unreported = unreported || value > 0
				remainder[field] = value
			}
			if consistent && unreported {
				add("result:usage", "system", "metadata", jsonText(map[string]any{"type": "token_usage", "usage": agyUsage(remainder), "usage_message_id": conversationID + ":result", "model_usage": usage}), "", resultLine)
			}
		}
	}
	if !recognized && info.Size() > 0 {
		return ConversationRecord{}, false, fmt.Errorf("parser mismatch: %s is a nonempty AGY transcript with no recognized stream events", origin)
	}
	if !recognized {
		return ConversationRecord{}, false, nil
	}
	nativeID := defaultString(conversationID, strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	return ConversationRecord{NativeID: nativeID, Provider: "antigravity", Model: model, Effort: effort, Origin: origin, Harness: "agy", Coverage: "partial",
		Messages: messages, StartedAt: invocation.launched, EndedAt: invocation.completed, Observed: info.ModTime().UnixNano()}, true, nil
}

// The CLI's header is exit evidence even after timestamp lines. Command
// output may print the same sentence and must not invent an exit status.
func antigravityExitCode(content string) *int64 {
	code, known := agyExitCode(content)
	if !known {
		return nil
	}
	value := int64(code)
	return &value
}

func antigravityToolResult(tool string, content any, failed bool, message string) map[string]any {
	payload := map[string]any{"tool": tool, "content": content, "is_error": failed || message != ""}
	details := map[string]any{}
	if tool == "run_command" {
		text, _ := toolResultText(content)
		if code := antigravityExitCode(text); code != nil {
			payload["exit_code"] = *code
			details["exit_code"] = *code
			payload["is_error"] = failed || message != "" || *code != 0
		}
	}
	if message != "" {
		details["error"] = message
	}
	if len(details) > 0 {
		payload["details"] = details
	}
	return payload
}
