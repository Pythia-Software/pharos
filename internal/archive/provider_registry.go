package archive

import (
	"context"
	"io/fs"
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
			Kind: "claude", DisplayName: "Claude Code", Mutation: "retrieval-only", Automatic: true, Setup: 1, MirrorRank: 1,
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
				Chosen: func(path string) string {
					if filepath.Base(path) == "projects" {
						return filepath.Dir(path)
					}
					return path
				},
			},
		},
		{
			Kind: "codex", DisplayName: "Codex", Mutation: "retrieval-only", Automatic: true, Setup: 2, MirrorRank: 1,
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
			Kind: "copilot", DisplayName: "Copilot CLI", Mutation: "retrieval-only", Automatic: true, Setup: 6, MirrorRank: 1,
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
			Kind: "opencode", DisplayName: "OpenCode", Mutation: "retrieval-only", Automatic: true, Setup: 7, MirrorRank: 1,
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
			Kind: "cline", DisplayName: "Cline", Mutation: "retrieval-only", Automatic: true, Setup: 8, MirrorRank: 1,
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
			Kind: "vscode-copilot", DisplayName: "VS Code Copilot", Mutation: "retrieval-only", Automatic: true, Setup: 9, MirrorRank: 1,
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
			Kind: "cursor", DisplayName: "Cursor", Mutation: "retrieval-only", Automatic: true, Setup: 10, MirrorRank: 1,
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
			Kind: "cursor-cli", DisplayName: "Cursor CLI", Mutation: "retrieval-only", Automatic: true, Setup: 11, MirrorRank: 1,
			New:          func(base baseAdapter) Adapter { return &cursorCLIAdapter{baseAdapter: base} },
			Plan:         (*capturePlan).cursorCLI,
			Preservation: liveSQLite, Acquisition: acquireNever, CaptureTables: cursorCLICaptureTables,
			Probe: &providerProbe{
				Folders:  []string{".cursor/chats"}, // the editor's extensions are in .cursor too
				Defaults: func(r *probeRun, home string) { r.cursorCLI(filepath.Join(home, ".cursor"), "default location") },
				Folder:   (*probeRun).cursorCLI,
				NewSources: func(home string, config Config) int {
					dir := filepath.Join(home, ".cursor")
					chats, _ := (&cursorCLIAdapter{baseAdapter: baseAdapter{config: SourceConfig{Path: dir}}}).chats()
					return newProbeSource(home, config, "cursor-cli", dir, len(chats) > 0)
				},
			},
		},
		{
			Kind: "zed", DisplayName: "Zed", Mutation: "retrieval-only", Automatic: true, Setup: 12, MirrorRank: 1,
			New:          func(base baseAdapter) Adapter { return &zedAdapter{baseAdapter: base} },
			Plan:         (*capturePlan).zed,
			Preservation: sqliteGroupPreservation, Acquisition: acquireSQLiteGroup, CaptureTables: zedCaptureTables,
			Probe: &providerProbe{
				Folders: []string{zedUserDir + "/threads"},
				Defaults: func(r *probeRun, home string) {
					r.zed(filepath.Join(home, filepath.FromSlash(zedUserDir)), "default location")
				},
				Folder: (*probeRun).zed,
				NewSources: func(home string, config Config) int {
					dir := filepath.Join(home, filepath.FromSlash(zedUserDir))
					stats, err := zedProbeStats(filepath.Join(dir, filepath.FromSlash(zedDatabase)))
					return newProbeSource(home, config, "zed", dir, err == nil && stats.sessions > 0)
				},
			},
		},
		{
			Kind: "antigravity", DisplayName: "Antigravity", Mutation: "retrieval-only", Automatic: true, Setup: 5, MirrorRank: 1,
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
			Kind: "conductor", DisplayName: "Conductor", Mutation: "retrieval-only", Automatic: true, Setup: 3, MirrorRank: 9,
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
			Kind: "tl1", DisplayName: "TL1", Mutation: "retrieval-only", Setup: 4, MirrorRank: 0,
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
			New:  func(base baseAdapter) Adapter { return &canonicalAdapter{base} },
			Plan: (*capturePlan).canonical, Preservation: retainedInputs,
		},
		{
			Kind: "tl1-export", DisplayName: "TL1 export", Mutation: "tl1-release", MirrorRank: 0,
			New:  func(base baseAdapter) Adapter { return &canonicalAdapter{base} },
			Plan: (*capturePlan).canonical, Preservation: retainedInputs,
		},
		{
			Kind: "chatgpt", DisplayName: "ChatGPT", KindAliases: []string{"chatgpt-export"}, Mutation: "retrieval-only", MirrorRank: 1,
			New:  func(base baseAdapter) Adapter { return &chatGPTAdapter{base} },
			Plan: (*capturePlan).chatGPT, Preservation: retainedInputs,
		},
		{
			Kind: "claude-ai", DisplayName: "Claude.ai", Mutation: "retrieval-only", MirrorRank: 1,
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
func newProbeSource(home string, config Config, kind, dir string, has bool) int {
	if !has {
		return 0
	}
	if resolved, err := resolveUnprotected(home, dir); err == nil && configuredSource(config, home, kind, resolved) == nil {
		return 1
	}
	return 0
}
