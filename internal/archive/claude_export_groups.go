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

func (units *claudeSourceUnits) observeGroups(root string, observed int64) (map[string]claudeUnitHead, error) {
	headWrites := map[string]claudeUnitHead{}
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
		content := units.groupContent(group)
		name, err := claudeOwnedPath(root, ".unit-heads/"+hashBytes([]byte("supplement:"+key))+".json")
		if err != nil {
			return nil, err
		}
		var previous claudeUnitHead
		if data, err := os.ReadFile(name); err == nil {
			if json.Unmarshal(data, &previous) != nil {
				return nil, errors.New("invalid Claude supplementary observation lookup")
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		group.Occurrence = hashBytes([]byte(jsonText([]any{"supplement", key, content, observed})))
		if previous.Content == content {
			group.Occurrence, group.Observed = previous.Occurrence, previous.FirstObserved
		}
		units.Groups = append(units.Groups, group)
		if observed >= previous.Observed {
			headWrites[name] = claudeUnitHead{Content: content, Occurrence: group.Occurrence, Observed: observed, FirstObserved: group.Observed}
		}
	}
	return headWrites, nil
}

func (units *claudeSourceUnits) publishGroupHeads(root string, observed int64) error {
	for _, group := range units.Groups {
		name, err := claudeOwnedPath(root, ".unit-heads/"+hashBytes([]byte("supplement:"+group.Key))+".json")
		if err != nil {
			return err
		}
		var previous claudeUnitHead
		if data, err := os.ReadFile(name); err == nil {
			if json.Unmarshal(data, &previous) != nil {
				return errors.New("invalid Claude supplementary observation lookup")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if previous.Observed > observed {
			continue
		}
		head := claudeUnitHead{Content: units.groupContent(group), Occurrence: group.Occurrence, Observed: observed, FirstObserved: group.Observed}
		if jsonText(head) == jsonText(previous) {
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
	previous.Observed = max(previous.Observed, record.Observed)
	previous.Conversations[0].Observed = previous.Observed
	previous.ActivityAt = max(previous.ActivityAt, record.ActivityAt)
	if !strings.HasPrefix(record.Title, "Claude.ai export · ") {
		previous.Title = record.Title
	}
}
