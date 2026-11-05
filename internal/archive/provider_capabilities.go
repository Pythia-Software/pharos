package archive

import (
	"slices"
	"strings"
)

// contentCapability is one kind of content a provider's records can hold.
type contentCapability struct {
	// Key names it in declarations and the API; Label names it to people,
	// and Detail says what it is.
	Key    string `json:"key"`
	Label  string `json:"label"`
	Detail string `json:"detail"`
}

// contentCapabilityList is every capability, in the order they are shown.
// The comment on each names the record field it fills.
var contentCapabilityList = []contentCapability{
	{"cwd", "Folder", "The folder the agent worked in."},                                         // WorkspaceRecord.Location
	{"git_branch", "Git branch", "The branch it worked on."},                                     // WorkspaceRecord.Metadata["branch"]
	{"model", "Model", "The model that replied."},                                                // Model on conversations and messages
	{"effort", "Effort", "The reasoning effort chosen."},                                         // Effort on conversations and messages
	{"message_tokens", "Token usage", "The tokens each request used."},                           // usage records
	{"tool_calls", "Tool calls", "The tools the agent called, with their input."},                // tool_call messages
	{"tool_results", "Tool results", "What each tool returned."},                                 // tool_result messages
	{"reasoning", "Thinking", "The model's reasoning."},                                          // reasoning messages
	{"timestamps", "Message times", "When each message was sent."},                               // MessageRecord.CreatedAt
	{"subagents", "Subagents", "Which conversations an agent started."},                          // ConversationRecord.ParentNativeID
	{"title", "Title", "A name the tool gave the conversation."},                                 // WorkspaceRecord.Title
	{"harness_version", "Tool version", "The version of the tool."},                              // HarnessVersionFirst and Last
	{"cost", "Reported cost", "A cost the tool reports. Without one, Pharos prices the tokens."}, // a reported_cost metric
}

// contentSupport is how much of one capability a provider records.
type contentSupport string

const (
	// supported: every format version records it, where it applies.
	supported contentSupport = "supported"
	// varies: some format versions, releases or records do.
	varies contentSupport = "varies"
	// unsupported: Pharos never reads it from this provider, so a missing
	// value means not recorded, never zero.
	unsupported contentSupport = "unsupported"
	// notApplicable: it means nothing for this provider (a folder for a
	// web chat).
	notApplicable contentSupport = "not-applicable"
)

// contentCapabilities declares what content a provider's records hold. Each
// list is space-separated capability keys; a key in none is unsupported.
type contentCapabilities struct {
	Supported, Varies, NotApplicable string
	// Notes say, for people, how a capability varies or why it is missing.
	Notes map[string]string
	// VariesNote is the note of a varying capability without one of its own.
	VariesNote string
}

// support is how much of capability key c declares.
func (c contentCapabilities) support(key string) contentSupport {
	switch {
	case slices.Contains(strings.Fields(c.Supported), key):
		return supported
	case slices.Contains(strings.Fields(c.Varies), key):
		return varies
	case slices.Contains(strings.Fields(c.NotApplicable), key):
		return notApplicable
	}
	return unsupported
}

// note is what people are told about capability key, or "".
func (c contentCapabilities) note(key string) string {
	if note := c.Notes[key]; note != "" {
		return note
	}
	if c.support(key) == varies {
		return c.VariesNote
	}
	return ""
}

// providerCapability is one capability of one provider, as the API gives it.
type providerCapability struct {
	Key     string         `json:"key"`
	Label   string         `json:"label"`
	Support contentSupport `json:"support"`
	Note    string         `json:"note,omitempty"`
}

// providerInfo is what the API tells people about a provider.
type providerInfo struct {
	Kind         string               `json:"kind"`
	DisplayName  string               `json:"display_name"`
	Setup        bool                 `json:"setup"`
	Automatic    bool                 `json:"automatic"`
	Capabilities []providerCapability `json:"capabilities"`
}

func (spec *providerSpec) info() providerInfo {
	info := providerInfo{Kind: spec.Kind, DisplayName: spec.DisplayName, Setup: spec.Setup > 0, Automatic: spec.Automatic, Capabilities: []providerCapability{}}
	for _, capability := range contentCapabilityList {
		info.Capabilities = append(info.Capabilities, providerCapability{capability.Key, capability.Label, spec.Content.support(capability.Key), spec.Content.note(capability.Key)})
	}
	return info
}

// contentLimits are the labels of the capabilities spec never records, and
// of those it records only sometimes, for a source's Settings card.
func (spec *providerSpec) contentLimits() (missing, partial []string) {
	missing, partial = []string{}, []string{}
	for _, capability := range contentCapabilityList {
		switch spec.Content.support(capability.Key) {
		case unsupported:
			missing = append(missing, capability.Label)
		case varies:
			partial = append(partial, capability.Label)
		}
	}
	return missing, partial
}

// providerInfos are the providers setup offers, in its order, then the rest
// in registry order.
func providerInfos() []providerInfo {
	infos := []providerInfo{}
	for _, spec := range setupProviders() {
		infos = append(infos, spec.info())
	}
	for index := range providerRegistry {
		if spec := &providerRegistry[index]; spec.Setup == 0 {
			infos = append(infos, spec.info())
		}
	}
	return infos
}
