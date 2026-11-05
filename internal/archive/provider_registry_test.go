package archive

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestProviderRegistryIsConsistent(t *testing.T) {
	names := map[string]bool{}
	setup := map[int]string{}
	for _, spec := range providerRegistry {
		for _, name := range append([]string{spec.Kind}, spec.KindAliases...) {
			if name == "" || name != strings.ToLower(name) || names[name] {
				t.Fatalf("kind %q is empty, not lowercase or named twice", name)
			}
			names[name] = true
		}
		if spec.DisplayName == "" || spec.Mutation == "" || spec.New == nil || spec.Plan == nil || spec.Preservation == nil {
			t.Fatalf("%s lacks a required field: %+v", spec.Kind, spec)
		}
		if spec.Setup > 0 {
			if other := setup[spec.Setup]; other != "" {
				t.Fatalf("%s and %s share setup position %d", spec.Kind, other, spec.Setup)
			}
			setup[spec.Setup] = spec.Kind
		}
		if probe := spec.Probe; probe != nil && (probe.Defaults == nil || probe.Folder == nil) {
			t.Fatalf("%s probes without reading what it finds", spec.Kind)
		}
		if spec.Setup > 0 && spec.Probe == nil {
			t.Fatalf("setup offers %s, which probe cannot find", spec.Kind)
		}
		adapter, err := MakeAdapter(SourceConfig{Name: "source", Kind: strings.ToUpper(spec.Kind), Path: t.TempDir()})
		if err != nil {
			t.Fatal(spec.Kind, err)
		}
		if adapter.Capability() != spec.Mutation || !productionAdapter(adapter) {
			t.Fatalf("%s made %T reporting %q", spec.Kind, adapter, adapter.Capability())
		}
	}
	if _, err := MakeAdapter(SourceConfig{Kind: "unknown"}); err == nil || err.Error() != "unsupported source kind: unknown" {
		t.Fatal("an unknown kind was read", err)
	}
	if _, err := planCapture(t.Context(), SourceConfig{Kind: "unknown", Path: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "unsupported source kind") {
		t.Fatal("an unknown kind was planned", err)
	}
	if mode := preservationMode(SourceConfig{Kind: "unknown", Options: map[string]any{"retained_input_capture": true}}); mode != "unsupported:unknown" {
		t.Fatal(mode)
	}
	if productionAdapter(&canonicalAdapter{baseAdapter{config: SourceConfig{Kind: "claude"}}}) {
		t.Fatal("an adapter another kind makes counted as production")
	}
}

// Each provider declares every capability at most once, with a note for
// people wherever its support varies.
func TestProvidersDeclareTheirContent(t *testing.T) {
	keys := map[string]bool{}
	for _, capability := range contentCapabilityList {
		keys[capability.Key] = true
	}
	for _, spec := range providerRegistry {
		named := map[string]bool{}
		for _, list := range []string{spec.Content.Supported, spec.Content.Varies, spec.Content.NotApplicable} {
			for _, key := range strings.Fields(list) {
				if !keys[key] || named[key] {
					t.Fatalf("%s declares %q twice or unknown", spec.Kind, key)
				}
				named[key] = true
			}
		}
		for key := range spec.Content.Notes {
			if !keys[key] {
				t.Fatalf("%s notes unknown %q", spec.Kind, key)
			}
		}
		for _, capability := range spec.info().Capabilities {
			if capability.Support == varies && capability.Note == "" {
				t.Fatalf("%s varies in %s without saying how", spec.Kind, capability.Key)
			}
		}
	}
	cursor, _ := providerFor("cursor-cli")
	if missing, partial := cursor.contentLimits(); !slices.Contains(missing, "Token usage") || !slices.Contains(partial, "Thinking") || slices.Contains(missing, "Tool calls") {
		t.Fatal(missing, partial)
	}
}

// The setup wizard and its tests read the providers as GET /api/providers
// gives them, from testdata/providers.json.
func TestProvidersFixtureMatchesTheRegistry(t *testing.T) {
	catalog, config := testCatalog(t)
	config.Sources = []SourceConfig{{Name: "chats", Kind: "cursor-cli", Path: t.TempDir(), Enabled: true}}
	server := testServer(t, config, catalog)
	// A source's Settings card says what its kind never records.
	if code, sources := probeCall(t, server, http.MethodGet, "/api/sources", ""); code != http.StatusOK ||
		!strings.Contains(jsonText(sources["items"]), `"not_recorded":["Git branch","Effort","Token usage"`) {
		t.Fatal(code, sources)
	}
	code, served := probeCall(t, server, http.MethodGet, "/api/providers", "")
	if code != http.StatusOK {
		t.Fatal(code, served)
	}
	data, _ := json.MarshalIndent(served, "", "  ")
	data = append(data, '\n')
	path := filepath.Join("..", "..", "testdata", "providers.json")
	if os.Getenv("PHAROS_UPDATE_PROVIDERS") == "1" {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if stored, err := os.ReadFile(path); err != nil || string(stored) != string(data) {
		t.Fatalf("%s is not the registry; run with PHAROS_UPDATE_PROVIDERS=1 (%v)", path, err)
	}
	for index, spec := range setupProviders() {
		if info := providerInfos()[index]; info.Kind != spec.Kind || !info.Setup {
			t.Fatalf("setup's tools come first, in its order: %+v", info)
		}
	}
}

// A provider is one registry entry: with only its spec, a source of its kind
// is read, captured, acquired before indexing, found by probe, offered by
// setup, updated in the background and ranked among mirrors.
func TestAProviderNeedsOnlyItsSpec(t *testing.T) {
	look := func(r *probeRun, dir, foundBy string) {
		if candidate, _ := r.locate("test-provider", dir, foundBy); candidate != nil {
			candidate.Unit = "export"
			r.keep(candidate)
		}
	}
	override(t, &providerRegistry, append(slices.Clone(providerRegistry), providerSpec{
		Kind: "test-provider", KindAliases: []string{"test-alias"}, DisplayName: "Test provider", Mutation: "retrieval-only",
		Automatic: true, Setup: 99, MirrorRank: 3,
		New:  func(base baseAdapter) Adapter { return &canonicalAdapter{base} },
		Plan: (*capturePlan).canonical, Preservation: retainedInputs,
		Probe: &providerProbe{
			Folders: []string{".test-provider"}, Env: []probeEnv{{Name: "TEST_PROVIDER_HOME", Sub: "data"}, {Name: "XDG_DATA_HOME", Sub: "test-provider"}},
			Defaults: func(r *probeRun, home string) { look(r, filepath.Join(home, ".test-provider"), "default location") },
			Folder:   look,
		},
	}))
	home := probeHome(t)
	useHost(t, "host-a")
	named := filepath.Join(home, "named")
	chosen := filepath.Join(home, "chosen")
	for _, dir := range []string{filepath.Join(named, "data"), filepath.Join(named, "test-provider"), filepath.Join(named, "opencode"), chosen} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A variable is expanded before the provider's folder below it is added.
	t.Setenv("TEST_PROVIDER_HOME", "~/../"+filepath.Base(home)+"/named")
	if tools := probeTools(home, Config{}); !slices.Contains(tools, ProbeTool{"test-provider", "~/named/data", "TEST_PROVIDER_HOME"}) {
		t.Fatalf("tools = %+v", tools)
	}
	t.Setenv("TEST_PROVIDER_HOME", "")
	export := filepath.Join(home, ".test-provider")
	writeSourceFile(t, filepath.Join(export, "one.json"), `{"workspaces":[{"id":"work","title":"Synthetic","conversations":[{"id":"thread","provider":"codex","messages":[{"id":"one","role":"user","text":"hello"}]}]}]}`)
	source := SourceConfig{Name: "test", Kind: "Test-Alias", Path: export, Account: "local", Enabled: true, Options: map[string]any{"retained_input_capture": true}}

	adapter, err := MakeAdapter(source)
	if err != nil || adapter.Capability() != "retrieval-only" || !productionAdapter(adapter) {
		t.Fatalf("adapter %T: %v", adapter, err)
	}
	if mode := preservationMode(source); mode != "retained-inputs" {
		t.Fatal(mode)
	}
	config := captureTestConfig(t, source)
	if summary := runCapture(t, config); !summary.OK {
		t.Fatalf("capture: %#v", summary)
	}
	if manifest := readCaptureManifest(t, config, "test"); len(manifest.Files) != 1 || manifest.Files[0].Captured != "files/one.json" {
		t.Fatalf("captured %#v", manifest.Files)
	}
	catalog, _ := testCatalog(t)
	if result := ingestSource(t, catalog, source); result.Workspaces != 1 || result.Revision == "" {
		t.Fatalf("indexed without acquiring first: %+v", result)
	}
	var title string
	if err := catalog.DB.QueryRow("SELECT title FROM workspaces WHERE source_kind='Test-Alias'").Scan(&title); err != nil || title != "Synthetic" {
		t.Fatal(title, err)
	}
	if !automaticProvider("test-provider") || mirrorRank("test-provider") != 3 {
		t.Fatal("updates or Library ignore the provider")
	}

	if tools := probeTools(home, Config{}); !slices.ContainsFunc(tools, func(tool ProbeTool) bool { return tool.Kind == "test-provider" && tool.FoundBy == "data folder" }) {
		t.Fatalf("tools = %+v", tools)
	}
	stubShellEnv(t, map[string]string{"TEST_PROVIDER_HOME": named, "XDG_DATA_HOME": named})
	report, err := selectedProbe(Config{}, nil, ProbeSelection{Kinds: &[]string{"test-provider", "opencode"}, Locations: []ProbeLocation{{Kind: "test-provider", Path: chosen}}})
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, candidate := range report.Candidates {
		if candidate.Kind == "test-provider" {
			found[candidate.Path] = candidate.FoundBy
		}
	}
	if len(found) != 4 || found[export] != "default location" || !strings.Contains(found[filepath.Join(named, "data")], "TEST_PROVIDER_HOME") ||
		!strings.Contains(found[filepath.Join(named, "test-provider")], "XDG_DATA_HOME") || found[chosen] == "" {
		t.Fatalf("candidates = %v", found)
	}
	// OpenCode still looks below the variable it shares.
	if !slices.ContainsFunc(report.Checked, func(location ProbeLocation) bool {
		return location.Kind == "opencode" && location.Path == "~/named/opencode" && strings.Contains(location.FoundBy, "XDG_DATA_HOME")
	}) {
		t.Fatalf("checked = %+v", report.Checked)
	}
	if _, err := selectedProbe(Config{}, nil, ProbeSelection{Kinds: &[]string{"canonical"}}); err == nil {
		t.Fatal("setup offered a provider it does not list")
	}
}
