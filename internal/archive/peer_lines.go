package archive

import (
	"encoding/json"
	"hash/fnv"
	"strings"
	"unicode"
)

// Peer links find a conversation that read what another one wrote: a reviewer
// whose tool output (a diff, a file listing) contains lines another top-level
// conversation wrote in its edit calls. Lines are compared whole after
// normalization, so a line seen through `git diff` (+ prefix) or a numbered file
// read (`  42→` prefix) matches the text the writer sent. See
// docs/peer-conversations-design.md.

// peerLineMinChars is the shortest normalized line compared. Shorter lines
// (closing braces, imports, blank-ish prose) are shared by unrelated code.
const peerLineMinChars = 40

// peerSkippedInputKeys are tool-call fields that name or quote existing text
// rather than text the call writes.
var peerSkippedInputKeys = map[string]bool{"old_string": true, "file_path": true, "path": true, "description": true, "pattern": true, "glob": true}

// peerCommandPrefixes mark shell lines that are commands, not written content.
var peerCommandPrefixes = []string{"cd ", "git ", "npm ", "go test", "go build", "gh ", "rg ", "grep ", "sed ", "python3 ", "node "}

// normalizePeerLine strips the decorations a line picks up when it is shown
// again: diff markers, line-number gutters and surrounding whitespace.
func normalizePeerLine(line string) string {
	line = strings.TrimSpace(line)
	// Line-number gutters: "42→", "42\t", "42:" (cat -n, Read, grep -n).
	digits := 0
	for digits < len(line) && line[digits] >= '0' && line[digits] <= '9' {
		digits++
	}
	if digits > 0 && digits < len(line) {
		rest := line[digits:]
		switch {
		case strings.HasPrefix(rest, "→"):
			line = strings.TrimSpace(rest[len("→"):])
		case rest[0] == '\t' || rest[0] == ':' || rest[0] == '|':
			line = strings.TrimSpace(rest[1:])
		}
	}
	if line != "" && (line[0] == '+' || line[0] == '-') && !strings.HasPrefix(line, "+++") && !strings.HasPrefix(line, "---") {
		line = strings.TrimSpace(line[1:])
	}
	return strings.TrimRightFunc(line, unicode.IsSpace)
}

func peerLineHash(line string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(line))
	return h.Sum64()
}

func peerLineUseful(line string) bool {
	if len(line) < peerLineMinChars {
		return false
	}
	for _, prefix := range peerCommandPrefixes {
		if strings.HasPrefix(line, prefix) {
			return false
		}
	}
	return true
}

// peerStrings yields every string in a JSON document with the key holding it.
// Text that is not JSON is returned whole.
func peerStrings(text string, visit func(key, value string)) {
	var doc any
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		visit("", text)
		return
	}
	var walk func(key string, value any)
	walk = func(key string, value any) {
		switch v := value.(type) {
		case map[string]any:
			for k, x := range v {
				walk(k, x)
			}
		case []any:
			for _, x := range v {
				walk(key, x)
			}
		case string:
			visit(key, v)
		}
	}
	walk("", doc)
}

func isPatchText(text string) bool {
	return strings.Contains(text, "*** Begin Patch") || strings.HasPrefix(text, "diff --git")
}

// writtenPeerLines returns the hashes of lines a tool call writes: new file
// content, replacement text and the added lines of a patch. Text a call only
// quotes (old_string) or names (paths) is skipped.
func writtenPeerLines(toolCall string, into map[uint64]bool) {
	peerStrings(toolCall, func(key, value string) {
		if peerSkippedInputKeys[key] || !strings.Contains(value, "\n") && len(value) < peerLineMinChars {
			return
		}
		patch := isPatchText(value)
		for _, raw := range strings.Split(value, "\n") {
			if patch {
				if !strings.HasPrefix(raw, "+") || strings.HasPrefix(raw, "+++") {
					continue
				}
			}
			line := normalizePeerLine(raw)
			if peerLineUseful(line) {
				into[peerLineHash(line)] = true
			}
		}
	})
}

// readPeerLines returns the hashes of lines a tool result showed, and
// separately the added lines of any diff it showed. Diff lines are the
// stronger evidence: they are changes nobody had merged yet.
func readPeerLines(toolResult string, all, added map[uint64]bool) {
	peerStrings(toolResult, func(_, value string) {
		for _, raw := range strings.Split(value, "\n") {
			isAdded := strings.HasPrefix(raw, "+") && !strings.HasPrefix(raw, "+++")
			line := normalizePeerLine(raw)
			if !peerLineUseful(line) {
				continue
			}
			hash := peerLineHash(line)
			all[hash] = true
			if isAdded && added != nil {
				added[hash] = true
			}
		}
	})
}
