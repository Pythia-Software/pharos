package archive

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type claudeSupplementUnit struct {
	Key        string `json:"key"`
	Members    []int  `json:"members"`
	Occurrence string `json:"observation"`
	Observed   int64  `json:"observed_ns,string"`
}

func (units *claudeSourceUnits) groupContent(group claudeSupplementUnit) string {
	members := []claudeSourceMember{}
	for _, index := range group.Members {
		members = append(members, units.Members[index])
	}
	return hashBytes([]byte(jsonText(members)))
}

// Part receipts describe membership only. Continuity is assigned to the
// complete snapshot group after every transport part has been collected.
func (units *claudeSourceUnits) describeGroups(observed int64) {
	membership := map[string][]int{}
	for index, member := range units.Members {
		if key := claudeSupplementGroup(member.Name); key != "" {
			membership[key] = append(membership[key], index)
		}
	}
	keys := []string{}
	for key := range membership {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		group := claudeSupplementUnit{Key: key, Members: membership[key], Observed: observed}
		group.Occurrence = hashBytes([]byte(jsonText([]any{"supplement-part", key, units.groupContent(group), observed})))
		units.Groups = append(units.Groups, group)
	}
}

type claudeSupplementObservation struct {
	Key        string `json:"key"`
	Content    string `json:"content"`
	Occurrence string `json:"observation"`
	Observed   int64  `json:"observed_ns,string"`
}

func claudeSupplementContent(reads []claudeSupplementRead) (string, error) {
	members := map[string]claudeSourceMember{}
	for _, read := range reads {
		for _, index := range read.group.Members {
			member := read.input.units.Members[index]
			if previous, ok := members[member.Name]; ok && (previous.SHA256 != member.SHA256 || previous.Size != member.Size) {
				return "", errors.New("Claude export has conflicting supplementary copies")
			}
			members[member.Name] = member
		}
	}
	names := []string{}
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	values := []any{}
	for _, name := range names {
		member := members[name]
		values = append(values, []any{name, member.SHA256, member.Size})
	}
	return hashBytes([]byte(jsonText(values))), nil
}

func claudeSupplementHeadPath(root, key string) (string, error) {
	return claudeOwnedPath(root, ".unit-heads/"+hashBytes([]byte("supplement-group:"+key))+".json")
}

func prepareClaudeSupplementObservations(config Config, job *claudeImportJob) error {
	root := claudeImportRoot(config, job.Name)
	groups := map[string][]claudeSupplementRead{}
	complete := job.State == "collected"
	for _, part := range job.Parts {
		if part.State == claudePartExcluded || part.Manifest {
			continue
		}
		if part.State != "collected" || part.Units == "" || part.UnitsError != "" {
			complete = false
			continue
		}
		units, err := readClaudeSourceUnits(sourceFiles{}, filepath.Join(claudeImportJobDir(config, job.Name, job.ID), part.Units), part.SHA256, part.UnitsSHA256)
		if err != nil {
			return err
		}
		observed := int64(0)
		if at, ok := parseTime(job.ExportedAt); ok {
			observed = at.UnixNano()
		} else if at, ok := parseTime(part.SourceModified); ok {
			observed = at.UnixNano()
		}
		for _, group := range units.Groups {
			groups[group.Key] = append(groups[group.Key], claudeSupplementRead{input: claudeExportInput{units: units, version: observed}, group: group})
		}
	}
	previous := map[string]claudeSupplementObservation{}
	for _, group := range job.Groups {
		previous[group.Key] = group
	}
	job.Groups = nil
	keys := []string{}
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		reads := groups[key]
		content, err := claudeSupplementContent(reads)
		if err != nil {
			return err
		}
		observed := int64(0)
		for _, read := range reads {
			observed = max(observed, read.input.version, read.group.Observed)
		}
		group := claudeSupplementObservation{Key: key, Content: content, Observed: observed, Occurrence: hashBytes([]byte(jsonText([]any{"supplement-group", key, content, observed, job.ID})))}
		if prior, ok := previous[key]; ok && prior.Content == content {
			group.Occurrence = prior.Occurrence
		} else if complete {
			name, err := claudeSupplementHeadPath(root, key)
			if err != nil {
				return err
			}
			var head claudeUnitHead
			if data, err := os.ReadFile(name); err == nil {
				if json.Unmarshal(data, &head) != nil || !validRecordHash(head.Content) || !validRecordHash(head.Occurrence) {
					return errors.New("invalid Claude supplementary observation lookup")
				}
				if head.Content == content {
					group.Occurrence = head.Occurrence
				}
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		job.Groups = append(job.Groups, group)
	}
	return nil
}

func publishClaudeSupplementHeads(config Config, job *claudeImportJob) error {
	if job.State != "collected" {
		return nil
	}
	for _, part := range job.Parts {
		if part.State != claudePartExcluded && !part.Manifest && (part.State != "collected" || part.Units == "" || part.UnitsError != "") {
			return nil
		}
	}
	root := claudeImportRoot(config, job.Name)
	for _, group := range job.Groups {
		name, err := claudeSupplementHeadPath(root, group.Key)
		if err != nil {
			return err
		}
		var prior claudeUnitHead
		if data, err := os.ReadFile(name); err == nil {
			if json.Unmarshal(data, &prior) != nil {
				return errors.New("invalid Claude supplementary observation lookup")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if prior.Observed > group.Observed {
			continue
		}
		head := claudeUnitHead{Content: group.Content, Occurrence: group.Occurrence, Observed: group.Observed, FirstObserved: group.Observed}
		if prior.Occurrence == group.Occurrence {
			head.FirstObserved = prior.FirstObserved
		}
		if jsonText(head) == jsonText(prior) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return err
		}
		if err := ensureFree(root, int64(len(jsonText(head)))); err != nil {
			return err
		}
		if err := writeFileDurably(name, []byte(jsonText(head))); err != nil {
			return err
		}
	}
	return nil
}

func (units *claudeSourceUnits) validateGroups() error {
	seen := map[string]bool{}
	for _, group := range units.Groups {
		if group.Key == "" || seen[group.Key] || !validRecordHash(group.Occurrence) || len(group.Members) == 0 {
			return errors.New("invalid Claude supplementary source unit")
		}
		seen[group.Key] = true
		previous := -1
		for _, index := range group.Members {
			if index <= previous || index < 0 || index >= len(units.Members) || claudeSupplementGroup(units.Members[index].Name) != group.Key {
				return errors.New("invalid Claude supplementary unit membership")
			}
			previous = index
		}
	}
	return nil
}

type claudeSupplementRead struct {
	input claudeExportInput
	group claudeSupplementUnit
}

func (read claudeSupplementRead) selected() claudeExportInput {
	input := read.input
	units := *input.units
	units.Members = nil
	units.Conversations = nil
	for _, index := range read.group.Members {
		units.Members = append(units.Members, input.units.Members[index])
	}
	input.units = &units
	input.version = read.group.Observed
	return input
}

// Identical to the legacy discovery merge: complete supplementary groups can
// span several transport parts, but receive one catalog publication.
func mergeClaudeSupplementRecord(previous *WorkspaceRecord, record WorkspaceRecord) {
	known := map[string]bool{}
	for _, message := range previous.Conversations[0].Messages {
		known[message.NativeID] = true
	}
	for _, message := range record.Conversations[0].Messages {
		if !known[message.NativeID] {
			previous.Conversations[0].Messages = append(previous.Conversations[0].Messages, message)
			known[message.NativeID] = true
		}
	}
	if strings.HasPrefix(previous.SourceID, "claude-export:artifacts/") {
		kept := map[string]bool{}
		for _, asset := range previous.Conversations[0].Assets {
			kept[asset.Ref] = true
		}
		for _, asset := range record.Conversations[0].Assets {
			if !kept[asset.Ref] {
				previous.Conversations[0].Assets = append(previous.Conversations[0].Assets, asset)
			}
		}
		claudeArtifactRecord(previous)
	}
	previous.Observed = max(previous.Observed, record.Observed)
	previous.Conversations[0].Observed = previous.Observed
	previous.ActivityAt = max(previous.ActivityAt, record.ActivityAt)
	if !strings.HasPrefix(record.Title, "Claude.ai export · ") {
		previous.Title = record.Title
	}
}
