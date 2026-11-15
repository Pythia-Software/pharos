package archive

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"
)

func (a *claudeAIAdapter) partExtractor() string { return claudeAIExtractor }
func (a *claudeAIAdapter) immutablePart(part sourcePart) bool {
	return strings.HasPrefix(part.item, "claude-json:") || strings.HasPrefix(part.item, "claude-supplement:") || strings.HasPrefix(part.item, "group:claude-observation:")
}
func (a *claudeAIAdapter) discoverParts(unchanged func([]sourcePart) bool, emit func(*WorkspaceRecord, []sourcePart) error) error {
	plan, err := a.planParts()
	if err != nil {
		return err
	}
	return plan.discover(a.context(), unchanged, emit)
}

type claudeConversationRead struct {
	input    claudeExportInput
	unit     claudeConversationUnit
	observed int64
}
type claudeSupplementPlan struct {
	reads               []claudeSupplementRead
	content, occurrence string
	observed            int64
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
	type checkedGroup struct {
		plannedPartGroup
		check    func() error
		observed int64
	}
	groups := []checkedGroup{}
	observations := map[string]string{}
	checkContent := func(key, content, kind string) error {
		if old, ok := observations[key]; ok && old != content {
			return errors.New("Claude export has conflicting " + kind + " copies")
		}
		observations[key] = content
		return nil
	}
	checkSupplement := func(snapshot, name, digest string) error {
		return checkContent(snapshot+"\x00supplement\x00"+name, digest, "supplementary")
	}
	add := func(parts []sourcePart, observed int64, check func() error, read func(partEmitter) error) {
		groups = append(groups, checkedGroup{plannedPartGroup{parts: parts, read: read}, check, observed})
	}
	// Parsing history alone does not prove that this payload is still selected.
	// Reuse it only while every previously emitted evidence claim is current;
	// the marker transaction checks that again before advancing source time.
	addObservation := func(part sourcePart, observed int64, check func() error, read func(partEmitter) error) {
		marker := sourcePart{item: "group:claude-observation:" + part.item, version: observed, signal: part.signal, metadata: true}
		parts := []sourcePart{part, marker}
		validated := false
		validate := func() error {
			if validated {
				return nil
			}
			if err := check(); err != nil {
				return err
			}
			validated = true
			return nil
		}
		payloadCurrent := func() (bool, error) {
			if plan.tracker == nil || plan.tracker.evidenceCurrent == nil || !plan.tracker.unchanged([]sourcePart{part}) {
				return false, nil
			}
			return plan.tracker.evidenceCurrent(marker)
		}
		add(parts, observed, func() error {
			current, err := payloadCurrent()
			if err != nil || current {
				return err
			}
			return validate()
		}, func(emit partEmitter) error {
			current, err := payloadCurrent()
			if err != nil {
				return err
			}
			if current {
				repeat := marker
				repeat.evidenceObservation = true
				err := emit(nil, []sourcePart{repeat})
				if !errors.Is(err, errObservationNeedsPayload) {
					return err
				}
				// Another publication displaced a claim after the check above.
			}
			// An immutable payload whose authority changed still needs validation
			// before it can be republished, even if preflight reused its history.
			if err := validate(); err != nil {
				return err
			}
			claims := int64(0)
			if err := read(func(record *WorkspaceRecord, _ []sourcePart) error {
				if record == nil {
					return nil
				}
				claims += 1 + int64(len(record.Conversations))
				return emit(record, nil)
			}); err != nil {
				return err
			}
			// This derived count belongs to the complete payload, including cloud
			// documents which can emit several records. Missing or partial proof
			// requires replay; it cannot certify just the surviving claims.
			proof := sourcePart{item: observationProofItem(marker), size: claims, signal: part.signal, metadata: true}
			return emit(nil, append(parts, proof))
		})
	}
	identity := func(kind, occurrence, content string) string {
		return hashBytes([]byte(jsonText([]any{a.config.Name, a.config.Account, kind, occurrence, content})))
	}
	conversations := []claudeConversationRead{}
	conversationIndex := map[string]int{}
	native := map[string][]claudeExportInput{}
	nativeOrder := []string{}
	supplements := map[string][]claudeSupplementRead{}
	supplementOrder := []string{}
	for _, input := range inputs {
		if input.units != nil {
			for _, unit := range input.units.Conversations {
				content := hashBytes([]byte(jsonText([]any{unit.Envelope, unit.Messages})))
				if err := checkContent(input.snapshot+"\x00"+unit.ID, content, "conversation"); err != nil {
					return nil, err
				}
				observed := max(unit.Observed, input.version)
				if index, ok := conversationIndex[unit.Occurrence]; ok {
					conversations[index].observed = max(conversations[index].observed, observed)
				} else {
					conversationIndex[unit.Occurrence] = len(conversations)
					conversations = append(conversations, claudeConversationRead{input, unit, observed})
				}
			}
			// Unchanged retained membership participates in conflict validation too.
			for _, member := range input.units.Members {
				if claudeSupplementGroup(member.Name) != "" {
					if err := checkSupplement(input.snapshot, member.Name, member.SHA256); err != nil {
						return nil, err
					}
				}
			}
			if len(input.units.Groups) > 0 {
				for _, group := range input.units.Groups {
					key := input.snapshot + "\x00" + group.Key
					if _, ok := supplements[key]; !ok {
						supplementOrder = append(supplementOrder, key)
					}
					supplements[key] = append(supplements[key], claudeSupplementRead{input, group})
				}
				continue
			}
			if !input.units.Supplements {
				continue
			}
		} else if !input.conversations && input.member == "" && !strings.EqualFold(filepath.Ext(input.path), ".zip") {
			continue
		}
		if _, ok := native[input.snapshot]; !ok {
			nativeOrder = append(nativeOrder, input.snapshot)
		}
		native[input.snapshot] = append(native[input.snapshot], input)
	}
	// A snapshot containing native fallbacks must assemble those members with
	// retained supplementary members too, including already retired native paths.
	for _, input := range inputs {
		if input.units == nil || len(input.units.Members) == 0 {
			continue
		}
		reads, ok := native[input.snapshot]
		if !ok {
			continue
		}
		found := false
		for _, read := range reads {
			found = found || read.path == input.path
		}
		if !found {
			native[input.snapshot] = append(reads, input)
		}
	}
	for _, candidate := range conversations {
		input, unit, observed := candidate.input, candidate.unit, candidate.observed
		content := hashBytes([]byte(jsonText([]any{unit.Envelope, unit.Messages})))
		size := input.units.Objects[unit.Envelope].Size
		for _, hash := range unit.Messages {
			size += input.units.Objects[hash].Size
		}
		part := sourcePart{item: "claude-json:" + unit.Occurrence, size: size, signal: identity("conversation", unit.Occurrence, content)}
		load := func() (WorkspaceRecord, error) {
			conversation, err := input.units.conversation(a.files(), input.objectRoot, unit)
			if err != nil {
				return WorkspaceRecord{}, err
			}
			record := a.record(conversation, a.original(input.path), observed)
			record.EvidenceIdentity = part.signal
			if a.view != nil {
				record.EvidenceRevision = a.view.evidenceRevision
			}
			return record, nil
		}
		addObservation(part, observed, func() error { _, err := load(); return err }, func(emit partEmitter) error {
			record, err := load()
			if err != nil {
				return err
			}
			return emit(&record, nil)
		})
	}
	// Native fallbacks have no verified per-conversation/member metadata. Plan
	// each entire snapshot together: validate unchanged siblings when any input
	// changes, and merge all supplementary members before publishing a record.
	for _, snapshot := range nativeOrder {
		reads := native[snapshot]
		parts := []sourcePart{}
		observed := int64(0)
		for _, input := range reads {
			observed = max(observed, input.version)
			size := int64(0)
			signal := input.sha256
			if input.units != nil {
				for _, member := range input.units.Members {
					size += member.Size
				}
			} else {
				info, err := a.files().Stat(input.path)
				if err != nil {
					return nil, err
				}
				size = info.Size()
				if signal == "" {
					signal = hashBytes([]byte(jsonText([]any{a.original(input.path), size, info.ModTime().UnixNano()})))
				}
			}
			item := hashBytes([]byte(jsonText([]any{a.original(input.path), signal, input.version, input.member})))
			parts = append(parts, sourcePart{item: "claude-input:" + item, size: size, signal: signal})
		}
		check := func() error {
			for _, input := range reads {
				if err := verifyClaudeExportInput(a.files(), input); err != nil {
					return err
				}
				if input.conversations && input.units == nil {
					if err := a.readExport(input, nil, func(c claudeAIConversation) error {
						messages := []string{}
						for _, message := range c.Messages {
							messages = append(messages, claudeObjectHash([]byte(jsonText(message))))
						}
						content := hashBytes([]byte(jsonText([]any{claudeObjectHash([]byte(jsonText(c.Fields))), messages})))
						return checkContent(input.snapshot+"\x00"+c.UUID, content, "conversation")
					}); err != nil {
						return err
					}
				}
				if err := a.readSupplement(input, nil, func(name, digest string) error { return checkSupplement(input.snapshot, name, digest) }); err != nil {
					return err
				}
			}
			return nil
		}
		nativeIdentity := identity("native", partsSignature(parts), "")
		add(parts, observed, check, func(emit partEmitter) error {
			publish := func(record *WorkspaceRecord) error {
				record.EvidenceIdentity = nativeIdentity
				if a.view != nil {
					record.EvidenceRevision = a.view.evidenceRevision
				}
				return emit(record, nil)
			}
			records := map[string]*WorkspaceRecord{}
			order := []string{}
			for _, input := range reads {
				if input.conversations && input.units == nil {
					if err := a.readExport(input, func(record WorkspaceRecord) error { return publish(&record) }, nil); err != nil {
						return err
					}
				}
				if err := a.readSupplement(input, func(record WorkspaceRecord) error {
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
				if err := publish(records[id]); err != nil {
					return err
				}
			}
			return emit(nil, parts)
		})
	}
	completeGroups := []claudeSupplementPlan{}
	completeIndex := map[string]int{}
	for _, key := range supplementOrder {
		reads := supplements[key]
		if _, fallback := native[reads[0].input.snapshot]; fallback {
			continue
		}
		content, err := claudeSupplementContent(reads)
		if err != nil {
			return nil, err
		}
		observed := int64(0)
		for _, read := range reads {
			observed = max(observed, read.input.version, read.group.Observed)
		}
		occurrence := hashBytes([]byte(jsonText([]any{"legacy-supplement-group", key, content, observed})))
		declared := map[string]bool{}
		for _, group := range reads[0].input.groupObservations {
			if declared[group.Key] || !validRecordHash(group.Content) || !validRecordHash(group.Occurrence) {
				return nil, errors.New("invalid Claude complete supplementary observation")
			}
			declared[group.Key] = true
			if group.Key == reads[0].group.Key {
				if group.Content != content {
					return nil, errors.New("Claude supplementary observation disagrees with its complete membership")
				}
				occurrence = group.Occurrence
				observed = max(observed, group.Observed)
			}
		}
		if index, ok := completeIndex[occurrence]; ok {
			completeGroups[index].observed = max(completeGroups[index].observed, observed)
		} else {
			completeIndex[occurrence] = len(completeGroups)
			completeGroups = append(completeGroups, claudeSupplementPlan{reads, content, occurrence, observed})
		}
	}
	for _, group := range completeGroups {
		reads, observed := group.reads, group.observed
		size := int64(0)
		counted := map[string]bool{}
		for _, read := range reads {
			for _, index := range read.group.Members {
				member := read.input.units.Members[index]
				if !counted[member.Name] {
					size += member.Size
					counted[member.Name] = true
				}
			}
		}
		signal := identity("supplement", group.occurrence, group.content)
		part := sourcePart{item: "claude-supplement:" + group.occurrence, size: size, signal: signal}
		selected := func(read claudeSupplementRead) claudeExportInput {
			input := read.selected()
			input.version = observed
			return input
		}
		check := func() error {
			for _, read := range reads {
				input := selected(read)
				if err := a.readSupplement(input, nil, func(name, digest string) error { return checkSupplement(input.snapshot, name, digest) }); err != nil {
					return err
				}
			}
			return nil
		}
		addObservation(part, observed, check, func(emit partEmitter) error {
			records := map[string]*WorkspaceRecord{}
			order := []string{}
			for _, read := range reads {
				if err := a.readSupplement(selected(read), func(record WorkspaceRecord) error {
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
			return nil
		})
	}
	// Native fallbacks, retained conversations and supplementary groups share
	// one source-time order. Keep validation attached while sorting readers.
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].observed < groups[j].observed })
	for _, group := range groups {
		plan.groups = append(plan.groups, group.plannedPartGroup)
	}
	plan.preflight = func(unchanged func([]sourcePart) bool) error {
		for _, group := range groups {
			if err := a.context().Err(); err != nil {
				return err
			}
			if unchanged != nil && unchanged(group.parts) {
				continue
			}
			if err := group.check(); err != nil {
				return err
			}
		}
		return nil
	}
	return plan, nil
}
