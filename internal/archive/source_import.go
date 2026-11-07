package archive

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// sourceImport adds a downloaded export (ChatGPT, Claude.ai) as one of this
// Mac's sources. Its history is in the cloud, so there is nothing to find:
// the person names the ZIP, the folder it unzips to, or its conversations
// file, and capture copies it into the library like any source.
type sourceImport struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	Name string `json:"name"`
}

// importableProvider is the export provider kind names, if it is one.
func importableProvider(kind string) (*providerSpec, error) {
	spec, ok := registeredKind(kind)
	if !ok || spec.Export == "" {
		return nil, probeRequestError{fmt.Errorf("%q is not a kind of export Pharos imports", kind)}
	}
	return spec, nil
}

// checkExport says whether path holds spec's export, without parsing it: a
// ZIP's listing, or a folder's names. It returns the export's size.
func checkExport(spec *providerSpec, path string) (int64, error) {
	usage := probeRequestError{fmt.Errorf("choose the %s export: the ZIP you downloaded, the folder it unzips to, or its conversations.json", spec.DisplayName)}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, probeRequestError{fmt.Errorf("nothing is at %s", path)}
	} else if err != nil {
		return 0, probeRequestError{fmt.Errorf("Pharos cannot read %s: %w", path, err)}
	}
	conversations := func(names []string) bool {
		if spec.Kind == "chatgpt" {
			return len(chatGPTConversationFiles(names)) > 0
		}
		for _, name := range names {
			if name == "conversations.json" {
				return true
			}
		}
		return false
	}
	switch {
	case info.IsDir():
		entries, err := os.ReadDir(path)
		if err != nil {
			return 0, probeRequestError{fmt.Errorf("Pharos cannot read %s: %w", path, err)}
		}
		names, size := []string{}, int64(0)
		for _, entry := range entries {
			if entry.Type().IsRegular() {
				names = append(names, entry.Name())
				if info, err := entry.Info(); err == nil && strings.HasSuffix(entry.Name(), ".json") {
					size += info.Size()
				}
			}
		}
		if !conversations(names) {
			return 0, usage
		}
		return size, nil
	case strings.EqualFold(filepath.Ext(path), ".zip"):
		archive, err := zip.OpenReader(path)
		if err != nil {
			return 0, probeRequestError{fmt.Errorf("%s is not a readable ZIP: %w", filepath.Base(path), err)}
		}
		defer archive.Close()
		names := []string{}
		for _, entry := range archive.File {
			names = append(names, entry.Name)
		}
		if !conversations(names) {
			return 0, probeRequestError{fmt.Errorf("%s holds no conversations.json; is it a %s export?", filepath.Base(path), spec.DisplayName)}
		}
		return info.Size(), nil
	case strings.EqualFold(filepath.Ext(path), ".json") && conversations([]string{filepath.Base(path)}):
		return info.Size(), nil
	}
	return 0, usage
}

// ImportSource checks an export and writes it to this Mac's host file as an
// enabled source, named for its kind unless another name is given.
func ImportSource(config Config, request sourceImport) (ProbeAcceptResult, error) {
	path := hostConfigPath(config)
	result := ProbeAcceptResult{HostFile: path, Added: []string{}, Enabled: []string{}, Declined: []string{}, Skipped: []string{}}
	if currentHost().Fallback {
		return result, errHostUnknown
	}
	if path == "" {
		return result, errNotLibrary(config)
	}
	spec, err := importableProvider(request.Kind)
	if err != nil {
		return result, err
	}
	location := strings.TrimSpace(request.Path)
	if !strings.HasPrefix(location, "/") && !strings.HasPrefix(location, "~/") {
		return result, probeRequestError{fmt.Errorf("enter an absolute path, or one starting with ~/")}
	}
	location = expandPath(location)
	if _, err := checkExport(spec, location); err != nil {
		return result, err
	}
	hostFileMu.Lock()
	defer hostFileMu.Unlock()
	written, _ := readSourceBlocks(path, "")
	taken := map[string]bool{}
	for _, sources := range [][]SourceConfig{config.Sources, written} {
		for _, source := range sources {
			taken[source.Name] = true
			if source.Kind == spec.Kind && filepath.Clean(source.Path) == filepath.Clean(location) {
				result.Skipped = append(result.Skipped, fmt.Sprintf("%s is already imported as %s", tildePath(location), source.Name))
				return result, nil
			}
		}
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		name = spec.Kind
		for n := 2; taken[name]; n++ {
			name = fmt.Sprintf("%s-%d", spec.Kind, n)
		}
	} else if !sourceNameValid.MatchString(name) {
		return result, probeRequestError{fmt.Errorf("source name %q must start with a letter or digit and use only letters, digits, '.', '_' and '-'", name)}
	} else if taken[name] {
		return result, probeRequestError{fmt.Errorf("a source named %q already exists", name)}
	}
	if err := appendHostSources(path, currentHost(), []SourceConfig{{Name: name, Kind: spec.Kind, Path: location, Enabled: true}}); err != nil {
		return result, err
	}
	result.Added = append(result.Added, name)
	return result, nil
}

func (s *Server) importSource(w http.ResponseWriter, body map[string]any) {
	config := s.Config()
	if !config.Library {
		writeError(w, errNotLibrary(config), http.StatusBadRequest)
		return
	}
	var request sourceImport
	payload, _ := json.Marshal(body)
	if err := json.Unmarshal(payload, &request); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	result, err := ImportSource(config, request)
	if err == nil {
		err = s.reloadSources()
	}
	var invalid probeRequestError
	switch {
	case errors.As(err, &invalid):
		writeError(w, err, http.StatusBadRequest)
	case err != nil:
		writeError(w, err, http.StatusInternalServerError)
	default:
		writeJSON(w, result, http.StatusOK)
	}
}
