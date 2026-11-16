package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The published list of importable kinds is the registry's. Run with
// PHAROS_UPDATE_GOLDEN=1 after changing a kind's Import, aliases, capture
// tables or content capabilities.
func TestPluginKindsContractMatchesTheRegistry(t *testing.T) {
	want, err := json.MarshalIndent(pluginKinds(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	path := filepath.Join("..", "..", "contracts", "plugins", "kinds.json")
	if os.Getenv("PHAROS_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from the provider registry; run with PHAROS_UPDATE_GOLDEN=1 and review the change", path)
	}
}

// importGlob compiles a providerImport glob: "**/" spans any number of path
// components and "*" stays inside one.
func importGlob(t *testing.T, glob string) *regexp.Regexp {
	t.Helper()
	pattern := regexp.QuoteMeta(glob)
	pattern = strings.ReplaceAll(pattern, `\*\*/`, `(?:.*/)?`)
	pattern = strings.ReplaceAll(pattern, `\*`, `[^/]*`)
	return regexp.MustCompile("^" + pattern + "$")
}

// What a kind tells importers to fetch is what its Plan captures: no more,
// so nothing fetched is ignored, and no less, so nothing read is left behind.
func TestImportableKindsDeclareWhatTheirPlansCapture(t *testing.T) {
	trees := map[string]map[string]bool{ // relative path -> whether the Plan captures it
		"claude": {
			"-work-repo/s1.jsonl":                   true,
			"-work-repo/s1/subagents/agent-1.jsonl": true,
			"-work-repo/sessions-index.json":        true,
			"-work-repo/notes.txt":                  false,
			"-work-repo/s1.jsonl.bak":               false,
		},
		"canonical": {
			"export.json":         true,
			"nested/more.json":    true,
			"nested/readme.txt":   false,
			"nested/export.jsonl": false,
		},
		"codex": {
			"sessions/2026/10/27/rollout-1.jsonl":   true,
			"archived_sessions/rollout-0.jsonl.zst": true,
			"rollout-2.jsonl":                       true,
			"session_index.jsonl":                   true,
			"state_5.sqlite":                        true,
			"log/codex-tui.jsonl":                   false,
			"history.jsonl":                         false,
			"sessions/notes.txt":                    false,
			"logs_1.sqlite":                         false,
		},
	}
	for _, spec := range providerRegistry {
		if spec.Import == nil || !slices.Contains(spec.Import.Forms, importFiles) {
			continue
		}
		t.Run(spec.Kind, func(t *testing.T) {
			tree, ok := trees[spec.Kind]
			if !ok {
				t.Fatalf("add a tree for %s: a kind importable as files needs its globs checked against its Plan", spec.Kind)
			}
			root := t.TempDir()
			for relative := range tree {
				path := filepath.Join(root, filepath.FromSlash(relative))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if strings.HasSuffix(relative, ".sqlite") {
					db := openWritableSQLite(t, path)
					if _, err := db.Exec(`CREATE TABLE threads(id TEXT, name TEXT)`); err != nil {
						t.Fatal(err)
					}
					db.Close()
				} else if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			plan, err := planCapture(context.Background(), SourceConfig{Name: spec.Kind, Kind: spec.Kind, Path: root})
			if err != nil {
				t.Fatal(err)
			}
			var planned, declared, expected []string
			for _, item := range append(append([]captureItem{}, plan.files...), plan.databases...) {
				planned = append(planned, item.rel)
			}
			for relative, captured := range tree {
				if captured {
					expected = append(expected, relative)
				}
				for _, glob := range append(append([]string{}, spec.Import.Files...), spec.Import.Databases...) {
					if importGlob(t, glob).MatchString(relative) {
						declared = append(declared, relative)
						break
					}
				}
			}
			sort.Strings(planned)
			sort.Strings(declared)
			sort.Strings(expected)
			if !slices.Equal(planned, expected) {
				t.Fatalf("the Plan captures %v, the tree expects %v", planned, expected)
			}
			if !slices.Equal(declared, planned) {
				t.Fatalf("the globs select %v, the Plan captures %v", declared, planned)
			}
		})
	}
}
