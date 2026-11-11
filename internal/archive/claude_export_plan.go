package archive

import (
	"errors"
	"path/filepath"
	"strings"
)

func (a *claudeAIAdapter) partExtractor() string { return claudeAIExtractor }

func (a *claudeAIAdapter) immutablePart(part sourcePart) bool {
	return strings.HasPrefix(part.item, "claude-json:") || strings.HasPrefix(part.item, "claude-supplement:")
}

func (a *claudeAIAdapter) discoverParts(unchanged func([]sourcePart) bool, emit func(*WorkspaceRecord, []sourcePart) error) error {
	plan, err := a.planParts()
	if err != nil {
		return err
	}
	return plan.discover(a.context(), unchanged, emit)
}

func (a *claudeAIAdapter) planParts() (*sourceWorkPlan, error) {
	inputs, err := claudeExportInputs(a.files(), a.config.Path)
	if err != nil {
		return nil, err
	}
	if len(inputs) == 0 {
		return nil, errors.New("no Claude conversation inputs found")
	}
	plan := &sourceWorkPlan{}
	checks := []func() error{}
	seen := map[string]bool{}
	observations := map[string]string{}
	supplements := map[string][]claudeSupplementRead{}
	supplementOrder := []string{}
	checkSupplement := func(snapshot, name, digest string) error {
		key := snapshot + "\x00supplement\x00" + name
		if old, ok := observations[key]; ok && old != digest {
			return errors.New("Claude export has conflicting supplementary copies")
		}
		observations[key] = digest
		return nil
	}
	add := func(part sourcePart, check func() error, read func(partEmitter) error) {
		if seen[part.item] {
			return
		}
		seen[part.item] = true
		plan.groups = append(plan.groups, plannedPartGroup{parts: []sourcePart{part}, read: read})
		checks = append(checks, check)
	}
	for _, input := range inputs {
		if input.units != nil {
			for _, unit := range input.units.Conversations {
				key := input.snapshot + "\x00" + unit.ID
				content := hashBytes([]byte(jsonText([]any{unit.Envelope, unit.Messages})))
				if old, ok := observations[key]; ok && old != content {
					return nil, errors.New("Claude export has conflicting conversation copies")
				}
				observations[key] = content
				size := input.units.Objects[unit.Envelope].Size
				for _, hash := range unit.Messages {
					size += input.units.Objects[hash].Size
				}
				part := sourcePart{item: "claude-json:" + unit.Occurrence, size: size, signal: hashBytes([]byte(jsonText([]any{unit.Envelope, unit.Messages, unit.Observed})))}
				load := func() (WorkspaceRecord, error) {
					conversation, err := input.units.conversation(a.files(), input.objectRoot, unit)
					if err != nil {
						return WorkspaceRecord{}, err
					}
					record := a.record(conversation, a.original(input.path), unit.Observed)
					record.EvidenceIdentity = part.signal
					if a.view != nil {
						record.EvidenceRevision = a.view.evidenceRevision
					}
					return record, nil
				}
				add(part, func() error { _, err := load(); return err }, func(emit partEmitter) error {
					record, err := load()
					if err != nil {
						return err
					}
					return emit(&record, []sourcePart{part})
				})
			}
			if len(input.units.Groups) > 0 {
				for _, group := range input.units.Groups {
					key := input.snapshot + "\x00" + group.Key
					if _, ok := supplements[key]; !ok {
						supplementOrder = append(supplementOrder, key)
					}
					supplements[key] = append(supplements[key], claudeSupplementRead{input: input, group: group})
				}
				continue
			}
			if !input.units.Supplements {
				continue
			}
		} else if !input.conversations && input.member == "" && filepath.Ext(input.path) != ".zip" {
			continue
		}
		info, err := a.files().Stat(input.path)
		if err != nil {
			return nil, err
		}
		signal := input.sha256
		if signal == "" {
			signal = hashBytes([]byte(jsonText([]any{a.original(input.path), info.Size(), info.ModTime().UnixNano()})))
		}
		item := hashBytes([]byte(jsonText([]any{signal, input.version, input.member})))
		part := sourcePart{item: "claude-input:" + item, size: info.Size(), signal: signal}
		check := func() error {
			if err := verifyClaudeExportInput(a.files(), input); err != nil {
				return err
			}
			if input.conversations && input.units == nil {
				if err := a.readExport(input, nil, func(c claudeAIConversation) error {
					key := input.snapshot + "\x00" + c.UUID
					messages := []string{}
					for _, message := range c.Messages {
						messages = append(messages, claudeObjectHash([]byte(jsonText(message))))
					}
					content := hashBytes([]byte(jsonText([]any{claudeObjectHash([]byte(jsonText(c.Fields))), messages})))
					if old, ok := observations[key]; ok && old != content {
						return errors.New("Claude export has conflicting conversation copies")
					}
					observations[key] = content
					return nil
				}); err != nil {
					return err
				}
			}
			return a.readSupplement(input, nil, func(name, digest string) error { return checkSupplement(input.snapshot, name, digest) })
		}
		add(part, check, func(emit partEmitter) error {
			// A native part can emit many records. Its checkpoint is committed
			// only after every record succeeds, so cancellation cannot hide the rest.
			record := func(r WorkspaceRecord) error { return emit(&r, nil) }
			if input.conversations && input.units == nil {
				if err := a.readExport(input, record, nil); err != nil {
					return err
				}
			}
			if err := a.readSupplement(input, record, nil); err != nil {
				return err
			}
			return emit(nil, []sourcePart{part})
		})
	}
	for _, key := range supplementOrder {
		reads := supplements[key]
		signals := []string{}
		size := int64(0)
		for _, read := range reads {
			signals = append(signals, read.group.Occurrence)
			for _, index := range read.group.Members {
				size += read.input.units.Members[index].Size
			}
		}
		signal := hashBytes([]byte(jsonText(signals)))
		part := sourcePart{item: "claude-supplement:" + signal, size: size, signal: signal}
		check := func() error {
			for _, read := range reads {
				input := read.selected()
				if err := a.readSupplement(input, nil, func(name, digest string) error { return checkSupplement(input.snapshot, name, digest) }); err != nil {
					return err
				}
			}
			return nil
		}
		add(part, check, func(emit partEmitter) error {
			records := map[string]*WorkspaceRecord{}
			order := []string{}
			for _, read := range reads {
				if err := a.readSupplement(read.selected(), func(record WorkspaceRecord) error {
					if previous := records[record.SourceID]; previous != nil {
						mergeClaudeSupplementRecord(previous, record)
					} else {
						records[record.SourceID] = &record
						order = append(order, record.SourceID)
					}
					return nil
				}, nil); err != nil {
					return err
				}
			}
			for _, id := range order {
				record := records[id]
				record.EvidenceIdentity = part.signal
				if a.view != nil {
					record.EvidenceRevision = a.view.evidenceRevision
				}
				if err := emit(record, nil); err != nil {
					return err
				}
			}
			return emit(nil, []sourcePart{part})
		})
	}
	plan.preflight = func(unchanged func([]sourcePart) bool) error {
		for i, group := range plan.groups {
			if err := a.context().Err(); err != nil {
				return err
			}
			if unchanged != nil && unchanged(group.parts) {
				continue
			}
			if err := checks[i](); err != nil {
				return err
			}
		}
		return nil
	}
	return plan, nil
}
