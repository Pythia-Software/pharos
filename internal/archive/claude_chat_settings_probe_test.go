//go:build probe

package archive

// Read-only parser smoke test against an explicitly selected Claude cache:
// PHAROS_CLAUDE_SETTINGS_PROBE_ROOT="$HOME/Library/Application Support/Claude" \
//   go test -tags probe -run '^TestProbeClaudeChatSettings$' -v ./internal/archive
// It writes no captures or catalog rows and logs only aggregate field counts.
import (
	"encoding/json"
	"os"
	"testing"
)

func TestProbeClaudeChatSettings(t *testing.T) {
	root := os.Getenv("PHAROS_CLAUDE_SETTINGS_PROBE_ROOT")
	if root == "" {
		t.Skip("set PHAROS_CLAUDE_SETTINGS_PROBE_ROOT to explicitly select a local Claude folder")
	}
	projections, warnings, err := collectClaudeSettings(t.Context(), sourceFiles{ctx: t.Context()}, root)
	if err != nil {
		t.Fatal("read-only cache scan failed")
	}
	latest := map[string]struct {
		at    string
		value claudeConversationSetting
	}{}
	dates := 0
	for _, projection := range projections {
		var snapshot claudeSettingsSnapshot
		if err = json.Unmarshal(projection.data, &snapshot); err != nil {
			t.Fatal("invalid settings projection")
		}
		if snapshot.TimeBasis == "http_date" {
			dates++
		}
		for _, row := range snapshot.Conversations {
			key := snapshot.Organization + ":" + row.Conversation
			if snapshot.ObservedAt > latest[key].at {
				latest[key] = struct {
					at    string
					value claudeConversationSetting
				}{snapshot.ObservedAt, row}
			}
		}
	}
	models, efforts, thinking := 0, 0, 0
	for _, observation := range latest {
		if observation.value.Model != nil {
			models++
		}
		if observation.value.Effort != nil {
			efforts++
		}
		if observation.value.Thinking != nil {
			thinking++
		}
	}
	t.Logf("settings snapshots=%d, HTTP dates=%d, warnings=%d; distinct conversations=%d, latest selections with model=%d, effort=%d, thinking mode=%d", len(projections), dates, len(warnings), len(latest), models, efforts, thinking)
}
