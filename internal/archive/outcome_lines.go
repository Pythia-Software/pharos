package archive

import (
	"crypto/sha256"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
)

// Rework is judged by line survival: which lines a session added does a
// later session remove. Each line is kept only as a short fingerprint, never
// its text, and only lines long enough to say something: a brace or a blank
// line removed later is not rework.

// lineFingerprintSize is the bytes of a line's fingerprint: 48 bits, so a
// session's lines are compared by set without a meaningful collision.
const lineFingerprintSize = 6

// minFingerprintLine is the shortest trimmed line that counts.
const minFingerprintLine = 8

type lineSet map[string]struct{}

func fingerprintLines(text string, into lineSet) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if len(line) < minFingerprintLine {
			continue
		}
		sum := sha256.Sum256([]byte(line))
		into[string(sum[:lineFingerprintSize])] = struct{}{}
	}
}

// packLines stores a set as its sorted fingerprints, end to end.
func packLines(set lineSet) []byte {
	if len(set) == 0 {
		return nil
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return []byte(strings.Join(keys, ""))
}

func unpackLines(packed []byte) lineSet {
	set := lineSet{}
	for start := 0; start+lineFingerprintSize <= len(packed); start += lineFingerprintSize {
		set[string(packed[start:start+lineFingerprintSize])] = struct{}{}
	}
	return set
}

// editChange is the lines one edit call adds and removes from its file; a
// rewrite gives the file's whole new content.
type editChange struct {
	Added, Removed lineSet
	Rewrite        bool
}

// readEditChange reads an edit call's input, as its call message stores it:
// a replacement (Claude and Codex Edit, MultiEdit, Antigravity), a whole file
// (Write) or a patch (apply_patch, including one inside a Codex code-mode
// script). An input it cannot read gives no lines.
func readEditChange(text, path string) (editChange, bool) {
	var message struct {
		Input any `json:"input"`
	}
	if json.Unmarshal([]byte(text), &message) != nil {
		return editChange{}, false
	}
	change := editChange{Added: lineSet{}, Removed: lineSet{}}
	replace := func(fields map[string]any) bool {
		for _, pair := range [][2]string{{"old_string", "new_string"}, {"Target", "Replacement"}, {"TargetContent", "ReplacementContent"}} {
			old, oldOK := fields[pair[0]].(string)
			replacement, newOK := fields[pair[1]].(string)
			if oldOK || newOK {
				fingerprintLines(old, change.Removed)
				fingerprintLines(replacement, change.Added)
				return true
			}
		}
		return false
	}
	switch input := message.Input.(type) {
	case map[string]any:
		if edits, ok := input["edits"].([]any); ok {
			found := false
			for _, edit := range edits {
				found = replace(mapValue(edit)) || found
			}
			if !found {
				return editChange{}, false
			}
		} else if content, ok := firstNonNil(input["content"], input["CodeContent"], input["file_text"]).(string); ok {
			change.Rewrite = true
			fingerprintLines(content, change.Added)
		} else if !replace(input) {
			patch, _ := firstNonNil(input["patch"], input["input"]).(string)
			if !readPatch(patch, path, change) {
				return editChange{}, false
			}
		}
	case string:
		if !readPatch(input, path, change) {
			return editChange{}, false
		}
	default:
		return editChange{}, false
	}
	// A line both removed and added is moved or reindented, not changed.
	for line := range change.Removed {
		if _, ok := change.Added[line]; ok {
			delete(change.Removed, line)
			delete(change.Added, line)
		}
	}
	return change, true
}

// patchEscapes decodes a patch quoted in a script's string literal.
var patchEscapes = strings.NewReplacer(`\n`, "\n", `\t`, "\t", `\"`, `"`, `\'`, "'", `\\`, `\`, "\\`", "`")

// readPatch reads the sections of an apply_patch patch that change path.
func readPatch(patch, path string, change editChange) bool {
	start := strings.Index(patch, "*** Begin Patch")
	if start < 0 {
		return false
	}
	patch = patch[start:]
	if end := strings.Index(patch, "*** End Patch"); end >= 0 {
		patch = patch[:end]
	}
	if !strings.Contains(patch, "\n") || strings.Count(patch, `\n`) > strings.Count(patch, "\n") {
		patch = patchEscapes.Replace(patch)
	}
	found, current := false, false
	for _, line := range strings.Split(patch, "\n") {
		if name, ok := strings.CutPrefix(line, "*** "); ok {
			current = false
			for _, kind := range []string{"Update File: ", "Add File: ", "Delete File: "} {
				if file, ok := strings.CutPrefix(name, kind); ok {
					current = patchNamesPath(strings.TrimSpace(file), path)
					found = found || current
				}
			}
			continue
		}
		if !current || strings.HasPrefix(line, "@@") {
			continue
		}
		if text, ok := strings.CutPrefix(line, "+"); ok {
			fingerprintLines(text, change.Added)
		} else if text, ok := strings.CutPrefix(line, "-"); ok {
			fingerprintLines(text, change.Removed)
		}
	}
	return found
}

// patchNamesPath reports whether a patch's file name, absolute or relative to
// the working directory, is the call's path.
func patchNamesPath(name, path string) bool {
	if name == "" || path == "" {
		return false
	}
	if filepath.IsAbs(name) {
		return filepath.Clean(name) == filepath.Clean(path)
	}
	return strings.HasSuffix(filepath.Clean(path), string(filepath.Separator)+filepath.Clean(name))
}

// fileLines is what a session did to one file's lines: the lines it added
// and kept, the lines it removed that it had not added, and whether it wrote
// the file whole, so that its kept lines are the file.
type fileLines struct {
	Kept, Removed lineSet
	Rewrote       bool
	Read          bool
}

func (lines *fileLines) apply(change editChange) {
	if lines.Kept == nil {
		lines.Kept, lines.Removed = lineSet{}, lineSet{}
	}
	lines.Read = true
	if change.Rewrite {
		// Whatever this session kept and did not write again, it removed.
		lines.Kept, lines.Rewrote = lineSet{}, true
	}
	for line := range change.Removed {
		if _, own := lines.Kept[line]; own {
			delete(lines.Kept, line)
		} else {
			lines.Removed[line] = struct{}{}
		}
	}
	for line := range change.Added {
		lines.Kept[line] = struct{}{}
		delete(lines.Removed, line)
	}
}

// removeLater adds to gone the kept lines a later session's change to the
// file removed: the lines it removed, or, when it wrote the file whole, the
// lines its file no longer has.
func removeLater(gone, kept lineSet, laterKept, laterRemoved []byte, rewrote bool) {
	if rewrote {
		final := unpackLines(laterKept)
		for line := range kept {
			if _, ok := final[line]; !ok {
				gone[line] = struct{}{}
			}
		}
		return
	}
	for start := 0; start+lineFingerprintSize <= len(laterRemoved); start += lineFingerprintSize {
		if line := string(laterRemoved[start : start+lineFingerprintSize]); hasLine(kept, line) {
			gone[line] = struct{}{}
		}
	}
}

func hasLine(set lineSet, line string) bool {
	_, ok := set[line]
	return ok
}
