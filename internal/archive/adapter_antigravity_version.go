package archive

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func antigravityHarness(app string) string {
	if app == "antigravity-cli" {
		return "antigravity-cli"
	}
	return "antigravity"
}

// An installed version describes only live transcripts from this Mac.
func (a *antigravityAdapter) installedHarnessVersion(app string) string {
	if a.view != nil {
		return ""
	}
	if version, ok := a.installedVersions[app]; ok {
		return version
	}
	if a.installedVersions == nil {
		a.installedVersions = map[string]string{}
	}
	version := installedAntigravityVersion(app)
	a.installedVersions[app] = version
	return version
}

func installedAntigravityVersion(app string) string {
	if app == "antigravity-cli" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		file, err := os.Open(filepath.Join(home, ".gemini", "antigravity-cli", "cli.log"))
		if err != nil {
			return ""
		}
		defer file.Close()
		version := ""
		scan := bufio.NewScanner(file)
		for scan.Scan() {
			if _, tail, ok := strings.Cut(scan.Text(), "Language server version:"); ok {
				if parts := strings.Fields(tail); len(parts) > 0 {
					version = parts[0]
				}
			}
		}
		return version
	}
	for _, path := range []string{"/Applications/Antigravity.app/Contents/Info.plist", filepath.Join(os.Getenv("HOME"), "Applications", "Antigravity.app", "Contents", "Info.plist")} {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		out, err := exec.Command("plutil", "-extract", "CFBundleShortVersionString", "raw", "-o", "-", path).Output()
		if err == nil {
			return strings.TrimSpace(string(out))
		}
	}
	return ""
}
