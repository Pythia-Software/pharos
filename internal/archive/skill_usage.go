package archive

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

type skillUsage struct {
	Key, CallKey, Stream, EvidenceNativeID, BodyNativeID string
	Name, Path, Evidence, Status, CreatedAt              string
	ContentBytes                                         *int64
	bodyHash                                             string
}

func mcpResourceTool(name string) bool {
	switch strings.ToLower(name) {
	case "read_mcp_resource", "list_mcp_resources", "list_mcp_resource_templates":
		return true
	}
	return false
}

func mcpResultError(value any) bool {
	if text, ok := value.(string); ok {
		var decoded any
		if json.Unmarshal([]byte(text), &decoded) != nil {
			return false
		}
		value = decoded
	}
	fields := mapValue(value)
	return fields["isError"] == true || fields["is_error"] == true
}

func skillName(path string) string {
	if strings.HasPrefix(path, "bundled:") {
		return strings.TrimPrefix(path, "bundled:")
	}
	if filepath.Base(path) == "SKILL.md" {
		return filepath.Base(filepath.Dir(path))
	}
	return filepath.Base(path)
}

// splitSkillBody separates the header Claude puts before an injected skill's
// instructions, in the conversation and in invoked_skills attachments alike.
func splitSkillBody(text string) (dir, body string, ok bool) {
	const header = "Base directory for this skill: "
	if !strings.HasPrefix(text, header) {
		return "", text, false
	}
	dir, body, _ = strings.Cut(strings.TrimPrefix(text, header), "\n")
	return strings.TrimSpace(dir), body, true
}

func skillReadPaths(call toolCall) []string {
	paths := []string{}
	seen := map[string]bool{}
	add := func(path, cwd string) {
		if filepath.Base(path) != "SKILL.md" || strings.ContainsAny(path, "$*?`\n") {
			return
		}
		if absolute := absoluteToolPath(path, cwd); absolute != "" {
			path = absolute
		}
		if !seen[path] {
			paths = append(paths, path)
			seen[path] = true
		}
	}
	readName := strings.ToLower(call.ToolName)
	if separator := strings.LastIndex(readName, "__"); separator >= 0 {
		readName = readName[separator+2:]
	}
	switch readName {
	case "read", "view", "read_file", "open_file", "view_file":
		add(call.FilePath, call.CWD)
	}
	var scan func(string, string, int)
	scan = func(command, cwd string, depth int) {
		if depth > 3 {
			return
		}
		for _, segment := range parseShellCommand(command).Segments {
			words := shellWords(segment.Text)
			if len(words) == 0 {
				continue
			}
			program := filepath.Base(words[0])
			if program == "cd" && len(words) == 2 {
				cwd = absoluteToolPath(words[1], cwd)
				continue
			}
			if program == "sh" || program == "bash" || program == "zsh" {
				for index, word := range words {
					if (word == "-c" || word == "-lc") && index+1 < len(words) {
						scan(words[index+1], cwd, depth+1)
					}
				}
				continue
			}
			switch program {
			case "cat", "head", "tail", "more", "less", "bat":
			case "sed":
				if segment.Category != "read" {
					continue
				}
			default:
				continue
			}
			if strings.Contains(segment.Text, "<<") {
				continue
			}
			for _, word := range words[1:] {
				add(word, cwd)
			}
		}
	}
	for _, command := range call.commandTexts {
		scan(command, call.CWD, 0)
	}
	return paths
}

func buildSkillUsages(messages []MessageRecord, calls []toolCall) []skillUsage {
	usages := []skillUsage{}
	callsByMessage := map[string]toolCall{}
	streams := make([]string, len(messages))
	for index := range streams {
		streams[index] = "main"
	}
	for _, summary := range agentSessionSummaries(messages) {
		for _, index := range summary.messageIndexes {
			if index >= 0 && index < len(streams) {
				streams[index] = summary.nativeID
			}
		}
	}
	for _, call := range calls {
		callsByMessage[call.CallNativeID] = call
	}
	attach := func(name, path, body string, message MessageRecord, stream string, includeHarness bool) bool {
		for index := len(usages) - 1; index >= 0; index-- {
			usage := &usages[index]
			if usage.Stream != stream || usage.Status != "ok" {
				continue
			}
			if usage.Evidence != "explicit_invocation" && (!includeHarness || usage.Evidence != "harness_load") {
				continue
			}
			if message.CallID != "" && usage.CallKey != message.CallID {
				continue
			}
			if usage.Name != name && !strings.HasSuffix(usage.Name, ":"+name) && (path == "" || usage.Path != path) {
				continue
			}
			if usage.BodyNativeID != "" && usage.ContentBytes != nil {
				return body == "" || usage.bodyHash == hashBytes([]byte(strings.TrimSpace(body)))
			}
			if usage.Path == "" {
				usage.Path = path
			}
			usage.BodyNativeID = message.NativeID
			if body != "" {
				size := int64(len(body))
				usage.ContentBytes = &size
				usage.bodyHash = hashBytes([]byte(strings.TrimSpace(body)))
			}
			return true
		}
		return false
	}
	harnessSeen := map[string]bool{}
	for index, message := range messages {
		stream := streams[index]
		var envelope map[string]any
		_ = json.Unmarshal([]byte(message.Text), &envelope)
		if call, ok := callsByMessage[message.NativeID]; ok {
			input := mapValue(envelope["input"])
			name := strings.ToLower(call.ToolName)
			if separator := strings.LastIndex(name, "__"); separator >= 0 {
				name = name[separator+2:]
			}
			if name == "skill" || name == "get_skill" || (name == "slashcommand" && firstString(input["skill"], input["skill_id"]) != "") {
				skill := strings.TrimPrefix(strings.TrimSpace(firstString(input["skill"], input["skill_id"], input["name"], input["command"])), "/")
				if skill != "" {
					usages = append(usages, skillUsage{Key: call.Key + ":invocation", CallKey: call.Key, Stream: call.Stream, EvidenceNativeID: message.NativeID, Name: skill, Path: firstString(input["skill_path"], input["path"]), Evidence: "explicit_invocation", Status: call.Status, CreatedAt: call.StartedAt})
				}
			}
			for _, path := range skillReadPaths(call) {
				usages = append(usages, skillUsage{Key: call.Key + ":file:" + path, CallKey: call.Key, Stream: call.Stream, EvidenceNativeID: message.NativeID, BodyNativeID: call.ResultNativeID, Name: skillName(path), Path: path, Evidence: "file_load", Status: call.Status, CreatedAt: call.StartedAt})
			}
			continue
		}
		text := message.Text
		if message.Kind == contextKind && firstString(envelope["type"]) == "injected_context" {
			text, _ = envelope["text"].(string)
		}
		if dir, body, ok := splitSkillBody(text); ok && (message.Kind == contextKind || message.Role == "user" || message.Role == agentRole) {
			path := filepath.Join(dir, "SKILL.md")
			name := skillName(path)
			if !attach(name, path, body, message, stream, false) {
				size := int64(len(body))
				usages = append(usages, skillUsage{Key: message.NativeID + ":body", Stream: stream, EvidenceNativeID: message.NativeID, BodyNativeID: message.NativeID, Name: name, Path: path, Evidence: "harness_load", Status: "ok", CreatedAt: message.CreatedAt, ContentBytes: &size, bodyHash: hashBytes([]byte(strings.TrimSpace(body)))})
			}
		}
		if firstString(envelope["type"]) != "attachment" {
			continue
		}
		attachment := mapValue(envelope["attachment"])
		if firstString(attachment["type"]) != "invoked_skills" {
			continue
		}
		for _, skill := range mapSlice(attachment["skills"]) {
			path := firstString(skill["path"])
			name := firstString(skill["name"])
			if name == "" {
				name = skillName(path)
			}
			if name == "" || name == "." {
				continue
			}
			body, hasBody := skill["content"].(string)
			_, body, _ = splitSkillBody(body)
			if attach(name, path, body, message, stream, true) {
				continue
			}
			key := stream + "\x00" + name + "\x00" + path + "\x00" + strings.TrimSpace(body)
			if harnessSeen[key] {
				continue
			}
			harnessSeen[key] = true
			usage := skillUsage{Key: message.NativeID + ":attachment:" + name + ":" + path, Stream: stream, EvidenceNativeID: message.NativeID, BodyNativeID: message.NativeID, Name: name, Path: path, Evidence: "harness_load", Status: "ok", CreatedAt: message.CreatedAt}
			if hasBody {
				size := int64(len(body))
				usage.ContentBytes = &size
				usage.bodyHash = hashBytes([]byte(strings.TrimSpace(body)))
			}
			usages = append(usages, usage)
		}
	}
	return usages
}
