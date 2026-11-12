package archive

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// Supplementary export files are source documents, not prompts or agent
// replies. Keep their own identities rather than assigning account-wide
// memories, reflections or artifact versions to an unrelated conversation.
func claudeSupplementDirectory(name string) bool {
	return slices.Contains([]string{"projects", "memories", "reflections", "artifacts", "claude_code_cloud"}, name)
}

func claudeSupplementGroup(name string) string {
	parts := strings.Split(name, "/")
	if len(parts) < 2 {
		return ""
	}
	switch parts[0] {
	case "projects", "memories", "reflections", "claude_code_cloud":
		return name
	case "artifacts":
		return strings.Join(parts[:2], "/")
	}
	return ""
}

func (a *claudeAIAdapter) readSupplement(input claudeExportInput, emit func(WorkspaceRecord) error, check func(string, string) error) error {
	// A discovered member can itself be a ZIP attachment. Only an outer
	// export is a container whose entries should be enumerated.
	type member struct {
		name string
		open func() (io.ReadCloser, error)
	}
	var members []member
	version := input.version
	if input.units != nil {
		for _, entry := range input.units.Members {
			members = append(members, member{entry.Name, func() (io.ReadCloser, error) {
				data, err := input.units.member(a.files(), input.objectRoot, entry)
				if err != nil {
					return nil, err
				}
				return io.NopCloser(bytes.NewReader(data)), nil
			}})
		}
	} else {
		outerArchive := input.member == "" && strings.EqualFold(filepath.Ext(input.path), ".zip")
		if !outerArchive && input.member == "" {
			return nil
		}
		if outerArchive {
			if _, err := a.files().Database(input.path); err != nil {
				return err
			}
		}
		file, err := a.files().Open(input.path)
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if version == 0 {
			version = info.ModTime().UnixNano()
		}
		if outerArchive {
			archive, err := exportZip(file, info.Size())
			if err != nil {
				return err
			}
			if _, err := claudeZipListing(archive); err != nil {
				return err
			}
			for _, entry := range archive.File {
				if !entry.FileInfo().IsDir() {
					members = append(members, member{entry.Name, entry.Open})
				}
			}
		} else {
			members = append(members, member{input.member, func() (io.ReadCloser, error) { return a.files().Open(input.path) }})
		}
	}
	origin := a.original(input.path)
	records := map[string]*WorkspaceRecord{}
	// ZIP order is source order; do not sort cloud transcript fragments by
	// timestamps, which can be equal for several parts of the same message.
	for _, entry := range members {
		group := claudeSupplementGroup(entry.name)
		if group == "" {
			continue
		}
		if err := a.context().Err(); err != nil {
			return err
		}
		reader, err := entry.open()
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(claudeContextReader{a.context(), io.LimitReader(reader, claudeExportLimits.Member+1)})
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || int64(len(data)) > claudeExportLimits.Member {
			return fmt.Errorf("Claude export member %s: %w", entry.name, errors.Join(readErr, closeErr, errors.New("member could not be read completely")))
		}
		digest := hashBytes(data)
		if check != nil {
			if err := check(entry.name, digest); err != nil {
				return err
			}
		}
		text := utf8.Valid(data) && !bytes.ContainsRune(data, 0)
		var value any
		if path.Ext(entry.name) == ".json" {
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.UseNumber()
			if err := decoder.Decode(&value); err != nil {
				return fmt.Errorf("Claude export member %s: %w", entry.name, err)
			}
			if err := ensureEOF(decoder); err != nil {
				return fmt.Errorf("Claude export member %s: %w", entry.name, err)
			}
		}
		// The validation pass reads every member, including its ZIP CRC, but
		// need not accumulate large artifact source files in memory.
		if emit == nil {
			if entry.name == "claude_code_cloud/channel_session_transcripts.json" {
				if err := a.cloudExportSessions(value, origin+":"+entry.name, version, func(WorkspaceRecord) error { return nil }); err != nil {
					return err
				}
			}
			continue
		}
		if entry.name == "claude_code_cloud/channel_session_transcripts.json" {
			if err := a.cloudExportSessions(value, origin+":"+entry.name, version, emit); err != nil {
				return err
			}
			// Keep the original file below too: it records fragment boundaries
			// and fields that do not belong to the normalized conversation.
		}
		record := records[group]
		if record == nil {
			id := "claude-export:" + group
			record = &WorkspaceRecord{SourceID: id, SourceKind: "claude-ai", Account: a.config.Account, Observed: version,
				Title: "Claude.ai export · " + group, Metadata: map[string]any{"export_resource": group},
				Conversations: []ConversationRecord{{NativeID: id, Provider: "claude", Account: a.config.Account, Harness: "claude-export-document", Origin: "user-export", Coverage: "complete", Observed: version}}}
			records[group] = record
		}
		if meta := mapValue(value); meta != nil {
			if name := firstString(meta["name"]); name != "" {
				record.Title = name
			}
			if path.Base(entry.name) == "artifact.json" {
				for _, raw := range reasoningList(meta["versions"]) {
					v := mapValue(raw)
					if firstString(v["id"]) == firstString(meta["active_version"]) {
						record.Title = defaultString(v["title"], record.Title)
					}
				}
			}
			if at := firstString(meta["updated_at"], meta["created_at"]); at > record.ActivityAt {
				record.ActivityAt = at
			}
		}
		payload := map[string]any{"type": "export_file", "file_name": entry.name, "file_size": len(data), "sha256": digest, "file_type": mime.TypeByExtension(path.Ext(entry.name))}
		if text {
			content := string(data)
			if value != nil {
				pretty, _ := json.MarshalIndent(value, "", "  ")
				content = string(pretty)
			}
			payload["extracted_content"] = content
		} else {
			payload["text"] = "Binary file retained in the collected export; its contents are not a text transcript."
		}
		record.Conversations[0].Messages = append(record.Conversations[0].Messages, MessageRecord{
			NativeID: entry.name, Role: "system", Kind: "attachment", Selected: true,
			Text: jsonText(payload), EvidenceLocator: origin + ":" + entry.name})
	}
	keys := make([]string, 0, len(records))
	for key := range records {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if err := emit(*records[key]); err != nil {
			return err
		}
	}
	return nil
}

func (a *claudeAIAdapter) cloudExportSessions(value any, origin string, version int64, emit func(WorkspaceRecord) error) error {
	byID := map[string]*claudeAIConversation{}
	var order []string
	for index, raw := range reasoningList(mapValue(value)["claude_code_cloud"]) {
		row := mapValue(raw)
		id := firstString(row["session_id"])
		if id == "" {
			return errors.New("Claude cloud transcript has no session ID")
		}
		c := byID[id]
		if c == nil {
			c = &claudeAIConversation{UUID: id, Name: "Claude cloud session " + short(id), CreatedAt: firstString(row["created_at"])}
			byID[id] = c
			order = append(order, id)
		}
		row = maps.Clone(row)
		row["uuid"] = fmt.Sprintf("%s:fragment:%d", id, index)
		row["sender"] = firstString(row["role"])
		if row["sender"] == "user" {
			row["sender"] = "human"
		}
		c.UpdatedAt = firstString(row["created_at"])
		c.Messages = append(c.Messages, row)
	}
	for _, id := range order {
		record := a.record(*byID[id], origin, version)
		record.Conversations[0].Harness = "claude-code-cloud-export"
		if err := emit(record); err != nil {
			return err
		}
	}
	return nil
}
