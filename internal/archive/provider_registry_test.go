package archive

import (
	"os"
	"path/filepath"
	"regexp"
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

// The setup wizard offers the tools setupProviders lists, in its order, under
// their display names.
func TestSetupWizardListsTheRegistrysTools(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("assets", "onboarding.js"))
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`const supported=\[([^\]]*)\]`).FindSubmatch(script)
	if match == nil {
		t.Fatal("onboarding.js lists no supported tools")
	}
	want := []string{}
	for _, spec := range setupProviders() {
		want = append(want, "'"+spec.Kind+"'")
	}
	if got := string(match[1]); got != strings.Join(want, ",") {
		t.Fatalf("onboarding.js offers %s, the registry %s", got, strings.Join(want, ","))
	}
	kinds := regexp.MustCompile(`const KINDS = \{([^}]*)\}`).FindSubmatch(script)
	if kinds == nil {
		t.Fatal("onboarding.js names no tools")
	}
	for _, spec := range providerRegistry {
		if spec.Setup == 0 && spec.Kind != "chatgpt" && spec.Kind != "claude-ai" {
			continue
		}
		key := spec.Kind
		if strings.Contains(key, "-") {
			key = "'" + key + "'"
		}
		if !strings.Contains(string(kinds[1]), key+": '"+spec.DisplayName+"'") {
			t.Fatalf("onboarding.js does not name %s %q", spec.Kind, spec.DisplayName)
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
			Folders: []string{".test-provider"}, Env: []probeEnv{{Name: "TEST_PROVIDER_HOME", Sub: "data"}},
			Defaults: func(r *probeRun, home string) { look(r, filepath.Join(home, ".test-provider"), "default location") },
			Folder:   look,
		},
	}))
	home := probeHome(t)
	useHost(t, "host-a")
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
	named := filepath.Join(home, "named")
	chosen := filepath.Join(home, "chosen")
	for _, dir := range []string{filepath.Join(named, "data"), chosen} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stubShellEnv(t, map[string]string{"TEST_PROVIDER_HOME": named})
	report, err := selectedProbe(Config{}, nil, ProbeSelection{Kinds: &[]string{"test-provider"}, Locations: []ProbeLocation{{Kind: "test-provider", Path: chosen}}})
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, candidate := range report.Candidates {
		found[candidate.Path] = candidate.FoundBy
	}
	if len(found) != 3 || found[export] != "default location" || !strings.Contains(found[filepath.Join(named, "data")], "TEST_PROVIDER_HOME") || found[chosen] == "" {
		t.Fatalf("candidates = %v", found)
	}
	if _, err := selectedProbe(Config{}, nil, ProbeSelection{Kinds: &[]string{"canonical"}}); err == nil {
		t.Fatal("setup offered a provider it does not list")
	}
}
