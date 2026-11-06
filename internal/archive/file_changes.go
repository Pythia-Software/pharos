package archive

import (
	"context"
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
)

// A file's changes as its sessions made them: each edit call's diff, read
// from the call's input in the transcript (session_file_edits keeps where
// each edit is). These need no Git and work for folders as well as
// repositories. A replacement shows the lines it replaced and their
// replacement, a patch its own hunks and a whole-file write its content;
// none knows the file's line numbers.

var (
	fileChangesLimit     = 30
	fileChangeLines      = 400
	fileChangeDiffBudget = 400 * 400
)

// diffLine is one line of a diff: ' ' kept, '+' added, '-' removed.
type diffLine struct {
	Kind byte
	Text string
}

func (line diffLine) MarshalJSON() ([]byte, error) {
	return json.Marshal([2]string{string(line.Kind), line.Text})
}

// editHunk is one part of an edit's diff, after a header naming it when
// the edit had several.
type editHunk struct {
	Header string     `json:"header,omitempty"`
	Lines  []diffLine `json:"lines"`
}

// editDiff is an edit call's change to one file: a replacement, a whole
// file written, a patch, or a file a patch listed without its lines.
type editDiff struct {
	Kind  string     `json:"kind"`
	Hunks []editHunk `json:"hunks"`
}

// readEditDiffs reads an edit call's change to path from its input, as
// readEditChanges reads its lines: replacements (and batches of them), a
// whole file, or a patch's section for path.
func readEditDiffs(text, path string) *editDiff {
	var message struct {
		Input any `json:"input"`
	}
	if json.Unmarshal([]byte(text), &message) != nil {
		return nil
	}
	path = filepath.Clean(path)
	replacement := func(fields map[string]any) (editHunk, bool) {
		for _, pair := range [][2]string{{"old_string", "new_string"}, {"Target", "Replacement"}, {"TargetContent", "ReplacementContent"}} {
			old, oldOK := fields[pair[0]].(string)
			replaced, newOK := fields[pair[1]].(string)
			if oldOK || newOK {
				return editHunk{Lines: lineDiff(splitLines(old), splitLines(replaced))}, true
			}
		}
		return editHunk{}, false
	}
	patch := func(text string) *editDiff {
		for _, section := range patchSections(text, path) {
			if section.Path != path {
				continue
			}
			diff := &editDiff{Kind: "patch", Hunks: []editHunk{}}
			for _, line := range section.Lines {
				if header, ok := strings.CutPrefix(line, "@@"); ok {
					diff.Hunks = append(diff.Hunks, editHunk{Header: strings.TrimSpace(strings.Trim(strings.TrimSpace(header), "@")), Lines: []diffLine{}})
					continue
				}
				if len(diff.Hunks) == 0 {
					diff.Hunks = append(diff.Hunks, editHunk{Lines: []diffLine{}})
				}
				hunk := &diff.Hunks[len(diff.Hunks)-1]
				hunk.Lines = append(hunk.Lines, diffLine{Kind: line[0], Text: line[1:]})
			}
			if section.Kind == "delete" {
				diff.Kind = "delete"
			}
			return diff
		}
		return nil
	}
	switch input := message.Input.(type) {
	case map[string]any:
		if edits, ok := firstNonNil(input["edits"], input["ReplacementChunks"]).([]any); ok {
			diff := &editDiff{Kind: "replace", Hunks: []editHunk{}}
			for _, edit := range edits {
				if hunk, ok := replacement(mapValue(edit)); ok {
					diff.Hunks = append(diff.Hunks, hunk)
				}
			}
			if len(diff.Hunks) == 0 {
				return nil
			}
			return diff
		}
		if changes, ok := input["changes"].([]any); ok {
			for _, item := range changes {
				if file := firstString(mapValue(item)["path"]); filepath.IsAbs(file) && filepath.Clean(file) == path {
					return &editDiff{Kind: "listed", Hunks: []editHunk{}}
				}
			}
			return nil
		}
		if content, ok := firstNonNil(input["content"], input["CodeContent"], input["file_text"]).(string); ok {
			lines := splitLines(content)
			hunk := editHunk{Lines: make([]diffLine, len(lines))}
			for index, line := range lines {
				hunk.Lines[index] = diffLine{Kind: '+', Text: line}
			}
			return &editDiff{Kind: "write", Hunks: []editHunk{hunk}}
		}
		if hunk, ok := replacement(input); ok {
			return &editDiff{Kind: "replace", Hunks: []editHunk{hunk}}
		}
		text, _ := firstNonNil(input["patch"], input["input"]).(string)
		return patch(text)
	case string:
		return patch(input)
	}
	return nil
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// lineDiff is the shortest edit from old lines to new ones: lines both keep
// in order, between those each removes and adds. Past the budget it is old
// removed and new added, around the lines they share at either end.
func lineDiff(old, replaced []string) []diffLine {
	start := 0
	for start < len(old) && start < len(replaced) && old[start] == replaced[start] {
		start++
	}
	end := 0
	for end < len(old)-start && end < len(replaced)-start && old[len(old)-1-end] == replaced[len(replaced)-1-end] {
		end++
	}
	lines := make([]diffLine, 0, len(old)+len(replaced))
	for _, line := range old[:start] {
		lines = append(lines, diffLine{Kind: ' ', Text: line})
	}
	a, b := old[start:len(old)-end], replaced[start:len(replaced)-end]
	if len(a)*len(b) > fileChangeDiffBudget {
		for _, line := range a {
			lines = append(lines, diffLine{Kind: '-', Text: line})
		}
		for _, line := range b {
			lines = append(lines, diffLine{Kind: '+', Text: line})
		}
	} else {
		// common[i][j] is the longest common run of a[i:] and b[j:].
		common := make([][]int, len(a)+1)
		for i := range common {
			common[i] = make([]int, len(b)+1)
		}
		for i := len(a) - 1; i >= 0; i-- {
			for j := len(b) - 1; j >= 0; j-- {
				if a[i] == b[j] {
					common[i][j] = common[i+1][j+1] + 1
				} else {
					common[i][j] = max(common[i+1][j], common[i][j+1])
				}
			}
		}
		i, j := 0, 0
		for i < len(a) || j < len(b) {
			switch {
			case i < len(a) && j < len(b) && a[i] == b[j]:
				lines = append(lines, diffLine{Kind: ' ', Text: a[i]})
				i, j = i+1, j+1
			case i < len(a) && (j == len(b) || common[i+1][j] >= common[i][j+1]):
				lines = append(lines, diffLine{Kind: '-', Text: a[i]})
				i++
			default:
				lines = append(lines, diffLine{Kind: '+', Text: b[j]})
				j++
			}
		}
	}
	for _, line := range old[len(old)-end:] {
		lines = append(lines, diffLine{Kind: ' ', Text: line})
	}
	return lines
}

type FileChangesOptions struct {
	Project, Path string
	Offset        int
}

// FileChanges are a file's edits, newest first, each with its session, its
// place in the transcript and the diff its call made, a page at a time.
func (c *Catalog) FileChanges(ctx context.Context, options FileChangesOptions) (map[string]any, error) {
	projects, err := c.fileProjects(ctx)
	if err != nil {
		return nil, err
	}
	project, choices, err := projects.choose(ctx, options.Project)
	if err != nil || choices != nil {
		return map[string]any{"choices": choices}, err
	}
	key, err := c.projectFileKey(ctx, project, options.Path)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"project": project.value(), "path": options.Path, "changes": []map[string]any{}, "offset": max(options.Offset, 0)}
	if key == "" {
		result["note"] = "No session edited " + options.Path + " with an edit tool."
		return result, nil
	}
	rows, err := queryMapsContext(ctx, c.reader(), `SELECT e.workspace_id,e.conversation_id,e.message_id,e.call_id,e.edited_at,e.lines_added,e.lines_removed,e.path,
		w.title,`+outcomeLabelSQL("w")+` outcome_label,
		(SELECT t.tool_name FROM tool_calls t WHERE t.workspace_id=e.workspace_id AND t.call_message_id=e.message_id AND t.call_id=e.call_id LIMIT 1) tool_name
		FROM session_file_edits e JOIN workspaces w ON w.id=e.workspace_id
		WHERE e.file_key=? AND e.workspace_id NOT IN (SELECT workspace_id FROM tool_mirror_workspaces)
		ORDER BY COALESCE(e.edited_at,'') DESC,e.workspace_id,e.seq DESC LIMIT ? OFFSET ?`, key, fileChangesLimit+1, max(options.Offset, 0))
	if err != nil {
		return nil, err
	}
	result["more"] = len(rows) > fileChangesLimit
	if len(rows) > fileChangesLimit {
		rows = rows[:fileChangesLimit]
	}
	changes := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		workspace := firstString(row["workspace_id"])
		place := transcriptPlace{Conversation: firstString(row["conversation_id"]), Message: firstString(row["message_id"]), Call: firstString(row["call_id"])}
		change := map[string]any{"workspace_id": workspace, "title": row["title"], "outcome_label": row["outcome_label"], "at": row["edited_at"],
			"tool_name": row["tool_name"], "lines_added": row["lines_added"], "lines_removed": row["lines_removed"],
			"url": placeURL(workspace, place), "session_url": "/work/" + url.PathEscape(workspace)}
		var text string
		if place.Message != "" {
			_ = c.reader().QueryRowContext(ctx, `SELECT COALESCE(text,'') FROM messages WHERE id=?`, place.Message).Scan(&text)
		}
		if diff := readEditDiffs(text, firstString(row["path"])); diff != nil {
			hunks, truncated := clipHunks(diff.Hunks, fileChangeLines)
			change["kind"], change["hunks"], change["truncated"] = diff.Kind, hunks, truncated
		} else {
			change["kind"], change["hunks"] = "unread", []editHunk{}
			change["note"] = "The transcript does not hold this edit's lines."
		}
		changes = append(changes, change)
	}
	result["changes"] = changes
	return result, nil
}

// clipHunks keeps a diff's first lines, and says if it left any out.
func clipHunks(hunks []editHunk, limit int) ([]editHunk, bool) {
	kept := []editHunk{}
	for _, hunk := range hunks {
		if limit <= 0 {
			return kept, true
		}
		if len(hunk.Lines) > limit {
			kept = append(kept, editHunk{Header: hunk.Header, Lines: hunk.Lines[:limit]})
			return kept, true
		}
		kept = append(kept, hunk)
		limit -= len(hunk.Lines)
	}
	return kept, false
}
