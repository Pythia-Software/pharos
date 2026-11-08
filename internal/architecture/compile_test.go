//go:build portability_compile

package architecture

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCrossPlatformCompilation is deliberately absent from normal go test,
// -short, setup and frontend checks. Invoke only when explicitly wanted:
// go test -tags portability_compile -run '^TestCrossPlatformCompilation$' -timeout 15m ./internal/architecture
// It compiles production code, never executes foreign binaries or packages an
// app. Native lifecycle correctness remains a separate target-specific proof.
func TestCrossPlatformCompilation(t *testing.T) {
	if testing.Short() {
		t.Skip("explicit compilation matrix: omit -short")
	}
	root := repoRoot(t)
	for _, name := range []string{
		"internal/archive/assets/query-tables.js", "internal/archive/assets/query-tables.css",
		"internal/archive/assets/data-guide.js", "internal/archive/assets/data-guide.css",
		"internal/uifeedback/assets/inbox.js", "internal/uifeedback/assets/inbox.css",
	} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("embedded frontend prerequisite %s: %v; run tools/setup.sh first", name, err)
		}
	}
	for _, target := range []struct{ os, arch string }{{"darwin", "arm64"}, {"linux", "amd64"}, {"windows", "amd64"}} {
		t.Run(target.os+"/"+target.arch, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "go", "build", "./...")
			cmd.Dir = root
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if key != "GOOS" && key != "GOARCH" && key != "CGO_ENABLED" && key != "GOWORK" && key != "GOFLAGS" {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "GOOS="+target.os, "GOARCH="+target.arch, "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("production compilation: %v\n%s", err, output)
			}
		})
	}
}
