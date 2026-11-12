package archive

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type claudeSettingsSetup struct {
	Source     string `json:"source"`
	Path       string `json:"path"`
	Account    string `json:"account"`
	Configured bool   `json:"configured"`
	Enabled    bool   `json:"enabled"`
	Scope      string `json:"scope"`
}

func sourceNamed(sources []SourceConfig, name string) (SourceConfig, bool) {
	for _, source := range sources {
		if source.Name == name {
			return source, true
		}
	}
	return SourceConfig{}, false
}

// An import may use an account alias other than local. Its companion source
// uses that same alias, so the two inputs can join without crossing accounts.
func claudeSettingsForImport(config Config, importName string) (claudeSettingsSetup, error) {
	export, ok := sourceNamed(config.Sources, importName)
	if !ok || export.Kind != "claude-ai" {
		return claudeSettingsSetup{}, probeRequestError{errors.New("choose a configured Claude.ai export source")}
	}
	account := defaultString(export.Account, "local")
	return claudeSettingsForAccount(config, account)
}

func claudeSettingsForAccount(config Config, account string) (claudeSettingsSetup, error) {
	var matches []SourceConfig
	for _, source := range config.Sources {
		if source.Kind == claudeSettingsKind && defaultString(source.Account, "local") == account {
			matches = append(matches, source)
		}
	}
	// Prefer an enabled source, then keep configured order for paused sources.
	slices.SortStableFunc(matches, func(a, b SourceConfig) int {
		if a.Enabled == b.Enabled {
			return 0
		}
		if a.Enabled {
			return -1
		}
		return 1
	})
	if len(matches) > 0 {
		source := matches[0]
		scope := "host"
		if source.File != hostConfigPath(config) {
			scope = "library"
		}
		return claudeSettingsSetup{Source: source.Name, Path: source.Path, Account: account, Configured: true, Enabled: source.Enabled, Scope: scope}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return claudeSettingsSetup{}, err
	}
	spec, _ := providerFor(claudeSettingsKind)
	return claudeSettingsSetup{Path: filepath.Join(home, filepath.FromSlash(spec.Probe.Folders[0])), Account: account, Scope: "host"}, nil
}

func enableClaudeSettingsForImport(config Config, importName, path string) (claudeSettingsSetup, error) {
	export, ok := sourceNamed(config.Sources, importName)
	if !ok || export.Kind != "claude-ai" {
		return claudeSettingsSetup{}, probeRequestError{errors.New("choose a configured Claude.ai export source")}
	}
	return configureClaudeSettings(config, defaultString(export.Account, "local"), path, true)
}

// One-time capture reuses paused sources without enabling automatic acquisition.
func configureClaudeSettings(config Config, account, path string, enable bool) (claudeSettingsSetup, error) {
	if !config.Library || hostConfigPath(config) == "" {
		return claudeSettingsSetup{}, errNotLibrary(config)
	}
	if currentHost().Fallback {
		return claudeSettingsSetup{}, errHostUnknown
	}
	hostFileMu.Lock()
	defer hostFileMu.Unlock()
	file := hostConfigPath(config)
	written, err := readSourceBlocks(file, filepath.Dir(config.Path))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return claudeSettingsSetup{}, err
	}
	// Concurrent imports share the host-file lock. Reconcile its latest source
	// definitions before deciding whether another companion needs to be added.
	combined := slices.Clone(config.Sources)
	for _, source := range written {
		if i := slices.IndexFunc(combined, func(other SourceConfig) bool { return other.Name == source.Name }); i >= 0 {
			combined[i] = source
		} else {
			combined = append(combined, source)
		}
	}
	config.Sources = combined
	setup, err := claudeSettingsForAccount(config, account)
	if err != nil {
		return setup, err
	}
	if setup.Enabled || (setup.Configured && !enable) {
		return setup, nil
	}
	path = strings.TrimSpace(path)
	if !strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "~/") {
		return setup, probeRequestError{errors.New("choose an absolute Claude app-data or cache folder, or one starting with ~/")}
	}
	path = expandPath(path)
	home, err := os.UserHomeDir()
	if err != nil {
		return setup, err
	}
	resolved, err := resolveUnprotected(home, path)
	if err != nil {
		return setup, probeRequestError{fmt.Errorf("Claude settings folder is unavailable: %w", err)}
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return setup, probeRequestError{errors.New("choose an available Claude app-data or HTTP cache folder")}
	}
	if setup.Configured {
		original, _ := sourceNamed(config.Sources, setup.Source)
		if filepath.Clean(path) != filepath.Clean(original.Path) {
			return setup, probeRequestError{errors.New("the configured settings folder changed; check the source again")}
		}
		if err := SetSourceEnabled(defaultString(original.File, config.Path), original.Name, true); err != nil {
			return setup, err
		}
		setup.Enabled = true
		return setup, nil
	}
	source := SourceConfig{Kind: claudeSettingsKind, Path: path, Account: setup.Account, Enabled: enable, Name: claudeSettingsKind}
	for n := 2; ; n++ {
		if _, exists := sourceNamed(config.Sources, source.Name); !exists {
			break
		}
		source.Name = fmt.Sprintf("%s-%d", claudeSettingsKind, n)
	}
	if err := appendHostSources(file, currentHost(), []SourceConfig{source}); err != nil {
		return setup, err
	}
	return claudeSettingsSetup{Source: source.Name, Path: source.Path, Account: source.Account, Configured: true, Enabled: enable, Scope: "host"}, nil
}

func (s *Server) getClaudeSettingsSetup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	setup, err := claudeSettingsForImport(s.Config(), r.URL.Query().Get("name"))
	if err != nil {
		writeError(w, err, 400)
		return
	}
	writeJSON(w, setup, 200)
}
func (s *Server) postClaudeSettingsSetup(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Cache-Control", "no-store")
	var request struct {
		Name        string `json:"name"`
		Path        string `json:"path"`
		Account     string `json:"account"`
		CaptureOnly bool   `json:"capture_only"`
	}
	data, _ := json.Marshal(body)
	if err := json.Unmarshal(data, &request); err != nil {
		writeError(w, err, 400)
		return
	}
	var setup claudeSettingsSetup
	var err error
	if request.CaptureOnly {
		coverage, readErr := s.Catalog.claudeSettingsCoverage(s.life.ctx, request.Account, "")
		if readErr != nil {
			err = readErr
		} else if request.Account == "" || len(coverage) != 1 || coverage[0].Account != request.Account {
			err = probeRequestError{errors.New("choose an account with imported Claude Chat conversations")}
		} else {
			setup, err = configureClaudeSettings(s.Config(), request.Account, request.Path, false)
		}
	} else {
		setup, err = enableClaudeSettingsForImport(s.Config(), request.Name, request.Path)
	}
	if err == nil {
		err = s.reloadSources()
	}
	var invalid probeRequestError
	if errors.As(err, &invalid) {
		writeError(w, err, 400)
	} else if err != nil {
		writeError(w, err, 500)
	} else {
		writeJSON(w, setup, 200)
	}
}
