package archive

import (
	"context"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// providerSpec declares one kind of source: how its adapter is made, what
// capture copies of it and how its inputs are kept, where probe looks for it
// on this Mac, and how updates and Library treat it. Every decision that
// depends on a source's kind reads its spec, so a provider is one entry in
// providerRegistry. Parsing stays in the adapter New makes.
type providerSpec struct {
	// Kind is the kind a [[sources]] block names; KindAliases are other
	// spellings it accepts. Both match case-insensitively, and a kind is
	// never renamed: library.toml and host files name it.
	Kind        string
	KindAliases []string
	// DisplayName names the provider to people ("Claude Code").
	DisplayName string
	// Instructions is where a person keeps the provider's rules for every
	// project: a home-relative file ("~/.claude/CLAUDE.md"), a folder of rule
	// files ending in "/", or, when the app keeps them in its settings, the
	// place to find them there. Fixes and Findings point at it.
	Instructions string
	// Content declares what its records hold, so a missing value it never
	// records is shown as not recorded rather than zero.
	Content contentCapabilities
	// Mutation is what the adapter's Capability reports.
	Mutation string
	// Resolve, when set, rewrites a configured source before it is read or
	// planned (Codex's rollout folder).
	Resolve func(SourceConfig) SourceConfig
	// New makes the adapter for a configured source.
	New func(baseAdapter) Adapter
	// Plan lists what capture copies: exactly what the adapter reads.
	Plan func(p *capturePlan, ctx context.Context, source SourceConfig, info fs.FileInfo) error
	// Preservation names how the inputs of a source with retained
	// acquisition enabled are kept (preservationMode).
	Preservation func(SourceConfig) string
	// Acquisition is what acquireRetainedInput copies before indexing.
	Acquisition acquisitionMode
	// CaptureTables are the tables a capture of its databases keeps, or nil
	// to keep them all. A provider whose database also holds credentials
	// keeps only the tables, or rows, its adapter reads.
	CaptureTables map[string]captureTable
	// Automatic sources are read by background updates.
	Automatic bool
	// Setup is the provider's place in the setup wizard's list of tools,
	// from 1, or 0 when the wizard does not offer it.
	Setup int
	// SetupWith names the tool setup shows this provider under, when it is
	// another face of one (Cursor CLI under Cursor).
	SetupWith string
	// OfferWhenFound keeps a niche tool out of setup's list unless it is
	// found on this Mac; it can still be searched for.
	OfferWhenFound bool
	// Summary says in a line what setup reads from the tool, and FolderHint
	// which folder to choose when it is somewhere unusual.
	Summary, FolderHint string
	// Export, for a provider whose history is a downloaded export rather
	// than a folder on this Mac, says how to download it.
	Export string
	// MirrorRank orders the sources holding one conversation: Library shows
	// the copy of the lowest, then the most recently active.
	MirrorRank int
	// Probe says where probe looks for the provider, or nil.
	Probe *providerProbe
}

// acquisitionMode is what acquireRetainedInput does for a source whose
// retained acquisition is enabled.
type acquisitionMode uint8

const (
	// acquireFiles copies the plan's inputs into a generation first.
	acquireFiles acquisitionMode = iota
	// acquireNever indexes the live source: Conductor's session exporter,
	// and Cursor's snapshots, which leave out what Cursor keeps beside its
	// chats (captureTables).
	acquireNever
	// acquireSQLiteGroup copies a shared database's sessions as a logical
	// group, once sqlite_record_capture is on (a staged rollout, shown in
	// status); before then it indexes the live source.
	acquireSQLiteGroup
)

// acquires says whether acquireRetainedInput copies source before indexing.
func (mode acquisitionMode) acquires(source SourceConfig) bool {
	switch mode {
	case acquireNever:
		return false
	case acquireSQLiteGroup:
		return source.Options["sqlite_record_capture"] == true
	}
	return true
}

// providerProbe is where probe looks for a provider on this Mac. Presence
// (Folders, Profiles, Applications, Env) only checks metadata; Defaults and
// Folder read what a location holds.
type providerProbe struct {
	// Folders are its data folders below the home, slash-separated.
	Folders []string
	// Profiles are alternate profile folders: entries of Dir (below the
	// home) whose names start with Prefix, from one bounded listing.
	Profiles *probeProfiles
	// Applications are app bundles whose presence says it is installed.
	Applications []string
	// Env are variables naming its folder, in order.
	Env []probeEnv
	// Defaults looks in its usual locations.
	Defaults func(r *probeRun, home string)
	// Folder looks in one folder a variable or the user named.
	Folder func(r *probeRun, path, foundBy string)
	// Chosen, when set, rewrites a folder the user chose before Folder
	// reads it.
	Chosen func(path string) string
	// NewSources counts its usual locations holding history that no
	// configured source reads.
	NewSources func(home string, config Config) int
}

type probeProfiles struct{ Dir, Prefix string }

// probeEnv is a variable naming a provider's folder: Sub below its value.
type probeEnv struct{ Name, Sub string }

// groupReader is an adapter whose sessions share a database, or are each
// one, so the update audit reads a session's group in place rather than
// freezing its files.
type groupReader interface {
	readGroup(parts []sourcePart) (*WorkspaceRecord, []sourcePart, error)
}

func retainedInputs(SourceConfig) string { return "retained-inputs" }

func liveSQLite(SourceConfig) string { return "live-sqlite-rollout-gap" }

func sqliteGroupPreservation(source SourceConfig) string {
	if source.Options["sqlite_record_capture"] == true {
		return "retained-sqlite-group"
	}
	return "live-sqlite-rollout-gap"
}

// providerRegistry lists every provider, in the order probe looks for them.
// It is filled by init, since the probes it names read it in turn.
var providerRegistry []providerSpec

func init() {
	providerRegistry = []providerSpec{
		{
			Kind: "claude", DisplayName: "Claude Code", Summary: "Conversations from the Claude Code app and CLI.", FolderHint: "The Claude configuration folder holding projects (usually ~/.claude), or its projects folder.", Instructions: "~/.claude/CLAUDE.md", Mutation: "retrieval-only", Automatic: true, Setup: 1, MirrorRank: 1,
			Content: contentCapabilities{Supported: "cwd git_branch model message_tokens tool_calls tool_results reasoning timestamps subagents harness_version", Varies: "effort speed title",
				Notes: map[string]string{"effort": "When the transcript records it.", "speed": "When the transcript records the request's speed or service tier.", "title": "When Claude Code named the session."}},
			New:          func(base baseAdapter) Adapter { return &jsonlAdapter{baseAdapter: base, provider: "claude"} },
			Plan:         (*capturePlan).claude,
			Preservation: retainedInputs,
			Probe: &providerProbe{
				Folders: []string{".claude", ".config/claude"}, Profiles: &probeProfiles{"", ".claude"},
				Applications: []string{"Claude Code.app"}, Env: []probeEnv{{Name: "CLAUDE_CONFIG_DIR"}},
				Defaults: func(r *probeRun, home string) {
					r.claude(filepath.Join(home, ".claude"), "default location", false)
					r.claude(filepath.Join(home, ".config", "claude"), "default location", false)
					// Alternate profiles such as ~/.claude-work are usually selected with
					// CLAUDE_CONFIG_DIR from a shell alias, which no environment reveals.
					if entries, err := os.ReadDir(home); err == nil {
						for _, entry := range entries {
							if strings.HasPrefix(entry.Name(), ".claude") && entry.Name() != ".claude" && (entry.IsDir() || entry.Type()&fs.ModeSymlink != 0) {
								r.claude(filepath.Join(home, entry.Name()), "~/.claude* directory", true)
							}
						}
					}
				},
				Folder: func(r *probeRun, path, foundBy string) { r.claude(path, foundBy, false) },
				// A chosen projects folder is read as its configuration folder's.
				Chosen: chosenParentOf("projects"),
			},
		},
		{
			Kind: "codex", DisplayName: "Codex", Summary: "Sessions from the Codex app and CLI.", FolderHint: "The Codex home folder holding sessions (usually ~/.codex).", Instructions: "~/.codex/AGENTS.md", Mutation: "retrieval-only", Automatic: true, Setup: 2, MirrorRank: 1,
			Content: contentCapabilities{Supported: "cwd git_branch model message_tokens tool_calls tool_results timestamps harness_version", Varies: "effort speed reasoning subagents title",
				Notes: map[string]string{"effort": "From releases that record it.", "speed": "From releases that record the turn's service tier.", "reasoning": "A summary, or only that it reasoned when the reasoning is encrypted.", "subagents": "From releases that record which session started another.", "title": "When Codex named the session."}},
			Resolve: resolveRolloutSource,
			New:     func(base baseAdapter) Adapter { return &jsonlAdapter{baseAdapter: base, provider: "codex"} },
			Plan:    (*capturePlan).codex,
			Preservation: func(source SourceConfig) string {
				if source.Options["sqlite_record_capture"] != true {
					return "filesystem-retained-sqlite-gated"
				}
				return "retained-inputs"
			},
			Probe: &providerProbe{
				Folders: []string{".codex"}, Applications: []string{"Codex.app"}, Env: []probeEnv{{Name: "CODEX_HOME"}},
				Defaults: func(r *probeRun, home string) { r.codex(filepath.Join(home, ".codex"), "default location") },
				Folder:   (*probeRun).codex,
			},
		},
		{
			Kind: "copilot", DisplayName: "Copilot CLI", Summary: "Sessions from GitHub Copilot in the terminal.", FolderHint: "The Copilot CLI folder holding session-state (usually ~/.copilot).", Instructions: "~/.copilot/copilot-instructions.md", Mutation: "retrieval-only", Automatic: true, Setup: 6, MirrorRank: 1,
			Content: contentCapabilities{Supported: "cwd model tool_calls tool_results timestamps subagents", Varies: "git_branch effort message_tokens reasoning title harness_version",
				Notes: map[string]string{"speed": "Fast mode is a model of its own in Copilot's picker, so its requests count as standard.", "message_tokens": "Older releases record only the session's total.", "reasoning": "A summary for OpenAI models, or only that it reasoned."}, VariesNote: "When the session records it."},
			New:          func(base baseAdapter) Adapter { return &copilotAdapter{base} },
			Plan:         (*capturePlan).copilot,
			Preservation: retainedInputs,
			Probe: &providerProbe{
				Folders: []string{".copilot"}, Env: []probeEnv{{Name: "COPILOT_HOME"}},
				Defaults: func(r *probeRun, home string) { r.copilot(filepath.Join(home, ".copilot"), "default location") },
				Folder:   (*probeRun).copilot,
				NewSources: func(home string, config Config) int {
					found := 0
					for _, dir := range probeFolderDirs(home, ".copilot", []probeEnv{{Name: "COPILOT_HOME"}}) {
						info, err := os.Stat(filepath.Join(dir, "session-state"))
						found += newProbeSource(home, config, "copilot", dir, err == nil && info.IsDir())
					}
					return found
				},
			},
		},
		{
			Kind: "opencode", DisplayName: "OpenCode", Summary: "Sessions from OpenCode.", FolderHint: "The OpenCode data folder holding opencode.db (usually ~/.local/share/opencode).", Instructions: "~/.config/opencode/AGENTS.md", Mutation: "retrieval-only", Automatic: true, Setup: 8, MirrorRank: 1,
			Content: contentCapabilities{Supported: "cwd model message_tokens tool_calls reasoning timestamps subagents harness_version", Varies: "speed tool_results title cost",
				Notes: map[string]string{"speed": "The tier an OpenAI reply reports. A fast mode chosen as a model of its own is not told apart.", "tool_results": "For calls that finished.", "title": "When OpenCode named the session.", "cost": "The session's total."}},
			New:          func(base baseAdapter) Adapter { return &opencodeAdapter{baseAdapter: base} },
			Plan:         (*capturePlan).opencode,
			Preservation: sqliteGroupPreservation, Acquisition: acquireSQLiteGroup, CaptureTables: opencodeKept(),
			Probe: &providerProbe{
				Folders: []string{".local/share/opencode"}, Env: []probeEnv{{Name: "XDG_DATA_HOME", Sub: "opencode"}},
				Defaults: func(r *probeRun, home string) {
					r.opencode(filepath.Join(home, ".local", "share", "opencode"), "default location")
				},
				Folder: (*probeRun).opencode,
				NewSources: func(home string, config Config) int {
					found := 0
					for _, dir := range probeFolderDirs(home, ".local/share/opencode", []probeEnv{{Name: "XDG_DATA_HOME", Sub: "opencode"}}) {
						databases, err := (&opencodeAdapter{baseAdapter: baseAdapter{config: SourceConfig{Path: dir}}}).databases()
						found += newProbeSource(home, config, "opencode", dir, err == nil && len(databases) > 0)
					}
					return found
				},
			},
		},
		{
			Kind: "cline", DisplayName: "Cline", Summary: "Sessions from Cline.", FolderHint: "The Cline data folder holding sessions (usually ~/.cline/data).", Instructions: "~/Documents/Cline/Rules/", Mutation: "retrieval-only", Automatic: true, Setup: 9, MirrorRank: 1,
			Content: contentCapabilities{Supported: "cwd model message_tokens tool_calls tool_results timestamps", Varies: "git_branch reasoning title harness_version cost",
				Notes: map[string]string{"speed": "Cline has no fast mode.", "reasoning": "Redacted thinking records only that it reasoned.", "cost": "The session's total.", "subagents": "A teammate's transcripts aren't read."}, VariesNote: "When the session records it."},
			New:          func(base baseAdapter) Adapter { return &clineAdapter{base} },
			Plan:         (*capturePlan).cline,
			Preservation: retainedInputs,
			Probe: &providerProbe{
				Folders: []string{".cline/data"}, Env: []probeEnv{{Name: "CLINE_DATA_DIR"}, {Name: "CLINE_DIR", Sub: "data"}},
				Defaults: func(r *probeRun, home string) { r.cline(filepath.Join(home, ".cline", "data"), "default location") },
				Folder:   (*probeRun).cline,
				NewSources: func(home string, config Config) int {
					found := 0
					for _, dir := range probeFolderDirs(home, ".cline/data", []probeEnv{{Name: "CLINE_DATA_DIR"}, {Name: "CLINE_DIR", Sub: "data"}}) {
						info, err := os.Stat(filepath.Join(dir, "sessions"))
						found += newProbeSource(home, config, "cline", dir, err == nil && info.IsDir())
					}
					return found
				},
			},
		},
		{
			Kind: "vscode-copilot", DisplayName: "VS Code Copilot", Summary: "Chats from GitHub Copilot in VS Code.", FolderHint: "The VS Code User folder (in ~/Library/Application Support/Code).", Instructions: "~/.copilot/copilot-instructions.md", Mutation: "retrieval-only", Automatic: true, Setup: 5, MirrorRank: 1,
			Content: contentCapabilities{Supported: "tool_calls tool_results reasoning subagents", Varies: "cwd model message_tokens timestamps title harness_version",
				Notes: map[string]string{"speed": "Fast mode is a model of its own in Copilot's picker, so its requests count as standard.", "cwd": "The window's folder.", "model": "Auto is known once it replied.", "message_tokens": "Some releases record only the last round's input.", "timestamps": "Per request, not per message.", "title": "When the chat was renamed.", "harness_version": "The Copilot Chat extension's version."}},
			New:          func(base baseAdapter) Adapter { return &vscodeCopilotAdapter{base} },
			Plan:         (*capturePlan).vscodeCopilot,
			Preservation: retainedInputs,
			Probe: &providerProbe{
				Folders: vscodeUserDirs,
				Defaults: func(r *probeRun, home string) {
					for _, user := range vscodeUserDirs {
						r.vscodeCopilot(filepath.Join(home, filepath.FromSlash(user)), "default location")
					}
				},
				Folder: (*probeRun).vscodeCopilot,
				NewSources: func(home string, config Config) int {
					count := 0
					for _, user := range vscodeUserDirs {
						dir := filepath.Join(home, filepath.FromSlash(user))
						sessions, _, err := (&vscodeCopilotAdapter{baseAdapter{config: SourceConfig{Path: dir}}}).sessions()
						count += newProbeSource(home, config, "vscode-copilot", dir, err == nil && len(sessions) > 0)
					}
					return count
				},
			},
		},
		{
			Kind: "cursor", DisplayName: "Cursor", Summary: "Chats from the Cursor editor and Cursor CLI.", FolderHint: "The Cursor User folder (in ~/Library/Application Support/Cursor).", Instructions: "Cursor's User Rules (Cursor Settings → Rules)", Mutation: "retrieval-only", Automatic: true, Setup: 3, MirrorRank: 1,
			Content: contentCapabilities{Supported: "tool_calls subagents", Varies: "cwd model speed message_tokens tool_results reasoning timestamps title",
				Notes: map[string]string{"speed": "The chat's fast mode setting now, which stands for all of its requests.", "message_tokens": "Before Cursor 3. Cursor 3 records only the size of the context.", "tool_results": "For calls that finished.", "timestamps": "Some messages are given the time of the one before."}, VariesNote: "When the chat records it."},
			New:          func(base baseAdapter) Adapter { return &cursorAdapter{baseAdapter: base} },
			Plan:         (*capturePlan).cursor,
			Preservation: liveSQLite, Acquisition: acquireNever, CaptureTables: cursorCaptureTables,
			Probe: &providerProbe{
				Folders: []string{cursorUserDir},
				Defaults: func(r *probeRun, home string) {
					r.cursor(filepath.Join(home, filepath.FromSlash(cursorUserDir)), "default location")
				},
				Folder: (*probeRun).cursor,
				NewSources: func(home string, config Config) int {
					dir := filepath.Join(home, filepath.FromSlash(cursorUserDir))
					return newProbeSource(home, config, "cursor", dir, cursorHasChats(filepath.Join(dir, "globalStorage", "state.vscdb")))
				},
			},
		},
		{
			Kind: "cursor-cli", DisplayName: "Cursor CLI", Summary: "Chats from Cursor CLI.", FolderHint: "The ~/.cursor folder holding chats.", SetupWith: "cursor", Mutation: "retrieval-only", Automatic: true, Setup: 4, MirrorRank: 1,
			Content: contentCapabilities{Supported: "tool_calls tool_results", Varies: "cwd model reasoning timestamps title",
				Notes: map[string]string{"speed": "Fast mode is kept in the CLI's settings, which Pharos does not read.", "message_tokens": "Only the size of the context.", "reasoning": "Encrypted: only that it reasoned.", "timestamps": "Prompts only; replies are placed after them."}, VariesNote: "When the chat records it."},
			New:          func(base baseAdapter) Adapter { return &cursorCLIAdapter{baseAdapter: base} },
			Plan:         (*capturePlan).cursorCLI,
			Preservation: liveSQLite, Acquisition: acquireNever, CaptureTables: cursorCLICaptureTables,
			Probe: &providerProbe{
				Folders:  []string{".cursor/chats"}, // the editor's extensions are in .cursor too
				Defaults: func(r *probeRun, home string) { r.cursorCLI(filepath.Join(home, ".cursor"), "default location") },
				Folder:   (*probeRun).cursorCLI,
				Chosen:   chosenParentOf("chats"),
				NewSources: func(home string, config Config) int {
					dir := filepath.Join(home, ".cursor")
					chats, _ := (&cursorCLIAdapter{baseAdapter: baseAdapter{config: SourceConfig{Path: dir}}}).chats()
					return newProbeSource(home, config, "cursor-cli", dir, len(chats) > 0)
				},
			},
		},
		{
			Kind: "zed", DisplayName: "Zed", Summary: "Threads from Zed’s agent panel.", FolderHint: "Zed’s folder holding threads (in ~/Library/Application Support).", Instructions: "~/.config/zed/AGENTS.md", Mutation: "retrieval-only", Automatic: true, Setup: 10, MirrorRank: 1,
			Content: contentCapabilities{Supported: "tool_calls tool_results", Varies: "cwd model effort speed message_tokens reasoning subagents title",
				Notes: map[string]string{"speed": "The thread's fast mode setting now, which stands for all of its requests.", "message_tokens": "Each turn's last request.", "reasoning": "Or only that it was redacted.", "timestamps": "Every message is given its thread's start."}, VariesNote: "When the thread records it."},
			New:          func(base baseAdapter) Adapter { return &zedAdapter{baseAdapter: base} },
			Plan:         (*capturePlan).zed,
			Preservation: sqliteGroupPreservation, Acquisition: acquireSQLiteGroup, CaptureTables: zedCaptureTables,
			Probe: &providerProbe{
				Folders: []string{zedUserDir + "/threads"},
				Defaults: func(r *probeRun, home string) {
					r.zed(filepath.Join(home, filepath.FromSlash(zedUserDir)), "default location")
				},
				Folder: (*probeRun).zed,
				Chosen: chosenParentOf("threads"),
				NewSources: func(home string, config Config) int {
					dir := filepath.Join(home, filepath.FromSlash(zedUserDir))
					stats, err := zedProbeStats(filepath.Join(dir, filepath.FromSlash(zedDatabase)))
					return newProbeSource(home, config, "zed", dir, err == nil && stats.sessions > 0)
				},
			},
		},
		{
			Kind: "antigravity", DisplayName: "Antigravity", Summary: "Conversations from Antigravity.", FolderHint: "The Antigravity profile folder (usually ~/.gemini/antigravity).", Instructions: "~/.gemini/GEMINI.md", Mutation: "retrieval-only", Automatic: true, Setup: 7, MirrorRank: 1,
			Content: contentCapabilities{Supported: "tool_calls tool_results timestamps subagents", Varies: "cwd model effort message_tokens title harness_version",
				Notes: map[string]string{"reasoning": "Only how many tokens it spent thinking.", "message_tokens": "When the conversation's database is present.", "harness_version": "The installed app's version, seen on this Mac."}, VariesNote: "When the conversation records it."},
			New:          func(base baseAdapter) Adapter { return &antigravityAdapter{baseAdapter: base} },
			Plan:         (*capturePlan).antigravity,
			Preservation: sqliteGroupPreservation, Acquisition: acquireSQLiteGroup,
			Probe: &providerProbe{
				Folders:  []string{".gemini/antigravity", ".gemini/antigravity-cli", ".gemini/antigravity-ide"},
				Profiles: &probeProfiles{".gemini", "antigravity"}, Applications: []string{"Antigravity.app"},
				Defaults: func(r *probeRun, home string) {
					for _, dir := range antigravityDirs(home) {
						foundBy := "~/.gemini/antigravity* directory"
						if antigravityProducts[filepath.Base(dir)] != "" {
							foundBy = "default location"
						}
						r.antigravity(dir, foundBy)
					}
				},
				Folder: (*probeRun).antigravity,
				NewSources: func(home string, config Config) int {
					count := 0
					for _, dir := range antigravityDirs(home) {
						count += newProbeSource(home, config, "antigravity", dir, true)
					}
					return count
				},
			},
		},
		{
			Kind: "conductor", DisplayName: "Conductor", Summary: "Workspaces and chats run in Conductor.", FolderHint: "Conductor’s app data folder holding its databases.", OfferWhenFound: true, Mutation: "retrieval-only", Automatic: true, Setup: 11, MirrorRank: 9,
			Content: contentCapabilities{Supported: "cwd model tool_calls tool_results reasoning", Varies: "git_branch effort speed message_tokens timestamps title harness_version",
				Notes: map[string]string{"harness_version": "When the Claude Code or Codex session it ran is also a source.", "subagents": "Recorded by the Claude Code or Codex session it ran."}, VariesNote: "When Conductor stores it."},
			New:  func(base baseAdapter) Adapter { return &conductorAdapter{baseAdapter: base} },
			Plan: (*capturePlan).conductor,
			Preservation: func(source SourceConfig) string {
				if source.Options["sqlite_record_capture"] == true {
					return "retained-sessions"
				}
				return "live-sqlite-rollout-gap"
			},
			Acquisition: acquireNever,
			Probe: &providerProbe{
				Folders: []string{"Library/Application Support/com.conductor.app"}, Applications: []string{"Conductor.app"},
				Defaults: func(r *probeRun, home string) {
					r.conductor(filepath.Join(home, "Library", "Application Support", "com.conductor.app"), "default location")
				},
				Folder: (*probeRun).conductor,
			},
		},
		{
			Kind: "tl1", DisplayName: "TL1", Summary: "Agent runs, tasks and project context from TL1.", FolderHint: "The TL1 folder holding registry.json or workspaces.json.", OfferWhenFound: true, Mutation: "retrieval-only", Setup: 12, MirrorRank: 0,
			Content: contentCapabilities{Supported: "cwd model effort tool_calls title", Varies: "git_branch speed message_tokens tool_results reasoning timestamps subagents harness_version cost",
				Notes: map[string]string{"cwd": "The repository's folder.", "message_tokens": "Per request for Claude Code runs, per turn for Codex.", "timestamps": "Not in Codex exec runs.", "subagents": "Codex runs that record which session started another.", "harness_version": "Claude Code runs only.", "cost": "From TL1's run summary."}, VariesNote: "When the run records it."},
			New: func(base baseAdapter) Adapter { return &tl1Adapter{baseAdapter: base} },
			Plan: func(p *capturePlan, ctx context.Context, source SourceConfig, info fs.FileInfo) error {
				return p.tl1(source)
			},
			Preservation: sqliteGroupPreservation, Acquisition: acquireSQLiteGroup,
			Probe: &providerProbe{
				Folders: []string{".tl1"}, Applications: []string{"TL1.app"},
				Defaults: func(r *probeRun, home string) {
					if !r.tl1(filepath.Join(home, ".tl1", tl1RegistryFile), "default location") {
						r.tl1(filepath.Join(home, ".tl1", tl1LegacyRegistryFile), "default location (TL1 before registry.json)")
					}
				},
				Folder: func(r *probeRun, path, foundBy string) {
					if !r.tl1(filepath.Join(path, tl1RegistryFile), foundBy) {
						r.tl1(filepath.Join(path, tl1LegacyRegistryFile), foundBy)
					}
				},
			},
		},
		{
			Kind: "canonical", DisplayName: "Canonical export", Mutation: "retrieval-only", MirrorRank: 5,
			Content: contentCapabilities{Varies: "cwd git_branch model effort speed message_tokens tool_calls tool_results reasoning timestamps subagents title harness_version cost", VariesNote: "When the export holds it."},
			New:     func(base baseAdapter) Adapter { return &canonicalAdapter{base} },
			Plan:    (*capturePlan).canonical, Preservation: retainedInputs,
		},
		{
			Kind: "tl1-export", DisplayName: "TL1 export", Mutation: "tl1-release", MirrorRank: 0,
			Content: contentCapabilities{Varies: "cwd git_branch model effort speed message_tokens tool_calls tool_results reasoning timestamps subagents title harness_version cost", VariesNote: "When the export holds it."},
			New:     func(base baseAdapter) Adapter { return &canonicalAdapter{base} },
			Plan:    (*capturePlan).canonical, Preservation: retainedInputs,
		},
		{
			Kind: "chatgpt", DisplayName: "ChatGPT", Export: "In ChatGPT, open Settings → Data controls → Export data. The email it sends links to a ZIP.", Instructions: "ChatGPT's custom instructions (Settings → Personalization)", KindAliases: []string{"chatgpt-export"}, Mutation: "retrieval-only", MirrorRank: 1,
			Content: contentCapabilities{Supported: "model tool_calls tool_results timestamps", Varies: "effort speed reasoning title", NotApplicable: "cwd git_branch subagents harness_version",
				Notes: map[string]string{"speed": "When the export records the tier.", "reasoning": "Or only how long it thought.", "message_tokens": "Exports have no token counts."}, VariesNote: "When the export holds it."},
			New:  func(base baseAdapter) Adapter { return &chatGPTAdapter{base} },
			Plan: (*capturePlan).chatGPT, Preservation: retainedInputs,
		},
		{
			Kind: "claude-ai", DisplayName: "Claude.ai", Export: "On claude.ai, open Settings → Privacy → Export data. The email it sends links to a ZIP.", Mutation: "retrieval-only", MirrorRank: 1,
			Content: contentCapabilities{Supported: "timestamps", Varies: "tool_calls tool_results reasoning title", NotApplicable: "cwd git_branch subagents harness_version",
				Notes: map[string]string{"tool_calls": "Not in older exports, which flatten each message to text.", "tool_results": "Not in older exports, which flatten each message to text.", "model": "The export names no model.", "message_tokens": "Exports have no token counts.", "reasoning": "A summary, when the export holds one."}, VariesNote: "When the export holds it."},
			New:  func(base baseAdapter) Adapter { return &claudeAIAdapter{base} },
			Plan: (*capturePlan).claudeAI, Preservation: retainedInputs,
		},
	}
}

// opencodeKept keeps every table opencodeCaptureTables names, whole.
func opencodeKept() map[string]captureTable {
	keep := map[string]captureTable{}
	for table := range opencodeCaptureTables {
		keep[table] = captureTable{}
	}
	return keep
}

// hostEnv is everything resolving a provider's locations depends on, so a
// test can resolve them for any home without touching this Mac.
type hostEnv struct {
	Home string
	// Getenv reads a variable that names a provider's folder.
	Getenv func(string) string
	// Applications are the folders that hold app bundles.
	Applications []string
	// List names a folder's entries, in one bounded listing.
	List func(dir string) []fs.DirEntry
}

// probeRoot is one place a provider may keep its data, and how it is known.
type probeRoot struct{ Path, FoundBy string }

// path is the folder a variable's value names: Sub below it, with ~ the
// home given.
func (env probeEnv) path(home, value string) string {
	if value == "~" || strings.HasPrefix(value, "~/") {
		value = filepath.Join(home, strings.TrimPrefix(value[1:], "/"))
	}
	return filepath.Join(resolvePath(home, value), env.Sub)
}

// resolveRoots yields where spec's data may be on host, in the order probe
// prefers them: data folders, profile folders, applications, then the
// variables that name a folder. It checks nothing exists; probe does. A
// profile folder's listing is read only when the roots before it are not
// enough.
func resolveRoots(spec *providerSpec, host hostEnv) iter.Seq[probeRoot] {
	return func(yield func(probeRoot) bool) {
		probe := spec.Probe
		if probe == nil {
			return
		}
		for _, folder := range probe.Folders {
			if !yield(probeRoot{filepath.Join(host.Home, filepath.FromSlash(folder)), "data folder"}) {
				return
			}
		}
		if probe.Profiles != nil && host.List != nil {
			dir := filepath.Join(host.Home, probe.Profiles.Dir)
			for _, entry := range host.List(dir) {
				if strings.HasPrefix(entry.Name(), probe.Profiles.Prefix) && !antigravityBackup(entry.Name()) && (entry.IsDir() || entry.Type()&fs.ModeSymlink != 0) {
					if !yield(probeRoot{filepath.Join(dir, entry.Name()), "profile folder"}) {
						return
					}
				}
			}
		}
		for _, root := range host.Applications {
			for _, bundle := range probe.Applications {
				if !yield(probeRoot{filepath.Join(root, bundle), "application"}) {
					return
				}
			}
		}
		for _, env := range probe.Env {
			if value := host.Getenv(env.Name); value != "" {
				if !yield(probeRoot{env.path(host.Home, value), env.Name}) {
					return
				}
			}
		}
	}
}

// probeEnvNames are the variables naming providers' folders
// (providerProbe.Env), in registry order.
func probeEnvNames() []string {
	names := []string{}
	for _, spec := range providerRegistry {
		if spec.Probe != nil {
			for _, env := range spec.Probe.Env {
				names = append(names, env.Name)
			}
		}
	}
	return names
}

// providerFor is the provider a source of kind reads, matching its kind or
// an alias case-insensitively.
func providerFor(kind string) (*providerSpec, bool) {
	kind = strings.ToLower(kind)
	for index := range providerRegistry {
		spec := &providerRegistry[index]
		if spec.Kind == kind || slices.Contains(spec.KindAliases, kind) {
			return spec, true
		}
	}
	return nil, false
}

// globalInstructionPaths are the files and folders, home-relative, where
// providers keep their rules for every project: each once, in registry order.
func globalInstructionPaths() []string {
	paths := []string{}
	for _, spec := range providerRegistry {
		if instructionPath(spec.Instructions) && !slices.Contains(paths, spec.Instructions) {
			paths = append(paths, spec.Instructions)
		}
	}
	return paths
}

// registeredKind is the provider whose kind is exactly kind, as setup,
// updates and Library compare the kinds they store.
func registeredKind(kind string) (*providerSpec, bool) {
	for index := range providerRegistry {
		if providerRegistry[index].Kind == kind {
			return &providerRegistry[index], true
		}
	}
	return nil, false
}

// otherProviders are those setup does not offer, in registry order.
func otherProviders() []*providerSpec {
	specs := []*providerSpec{}
	for index := range providerRegistry {
		if providerRegistry[index].Setup == 0 {
			specs = append(specs, &providerRegistry[index])
		}
	}
	return specs
}

// setupProviders are the providers the setup wizard offers, in its order.
func setupProviders() []*providerSpec {
	specs := []*providerSpec{}
	for index := range providerRegistry {
		if providerRegistry[index].Setup > 0 {
			specs = append(specs, &providerRegistry[index])
		}
	}
	slices.SortFunc(specs, func(a, b *providerSpec) int { return a.Setup - b.Setup })
	return specs
}

// mirrorRank is a source kind's MirrorRank, or 5 for a kind no provider
// reads.
func mirrorRank(kind string) int {
	if spec, ok := registeredKind(kind); ok {
		return spec.MirrorRank
	}
	return 5
}

// newProbeSource is 1 when has, for a usual location dir holding history,
// and no configured source reads it.
// chosenParentOf reads a chosen folder named name, which the person may pick
// because it holds the history, as the folder the source reads: its parent.
func chosenParentOf(name string) func(string) string {
	return func(path string) string {
		if filepath.Base(path) == name {
			return filepath.Dir(path)
		}
		return path
	}
}

func newProbeSource(home string, config Config, kind, dir string, has bool) int {
	if !has {
		return 0
	}
	if resolved, err := resolveUnprotected(home, dir); err == nil && configuredSource(config, home, kind, resolved) == nil {
		return 1
	}
	return 0
}
