package archive

import (
	"bufio"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type installedVersionObservation struct {
	Version string `json:"version"`
	Since   string `json:"since"`
}

func installedVersionForActivity(observation installedVersionObservation, started, ended string) (string, string) {
	since, ok := parseTime(observation.Since)
	if !ok || observation.Version == "" {
		return "", ""
	}
	first, last := "", ""
	if end, ok := parseTime(ended); ok && !end.Before(since) {
		last = observation.Version
	}
	if start, ok := parseTime(started); ok && !start.Before(since) {
		first = observation.Version
		if last == "" {
			last = observation.Version
		}
	}
	return first, last
}

func antigravityHarness(app string) string {
	if app == "antigravity-cli" {
		return "antigravity-cli"
	}
	return "antigravity"
}

// Observe the installed version before reading source records. The observation
// time is the earliest point at which Pharos can safely attribute that version.
func (a *antigravityAdapter) prepareInstalledVersions(c *Catalog) error {
	a.installedVersions = map[string]installedVersionObservation{}
	if a.view != nil && a.view.offHost() {
		return nil
	}
	readVersion := a.installedVersion
	if readVersion == nil {
		readVersion = installedAntigravityVersion
	}
	for _, app := range []string{"antigravity", "antigravity-cli", "antigravity-ide"} {
		version := readVersion(app)
		if version == "" {
			continue
		}
		key := "installed_harness_version:" + app
		var saved string
		err := c.DB.QueryRow("SELECT value FROM meta WHERE key=?", key).Scan(&saved)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		var observation installedVersionObservation
		if saved != "" {
			_ = decodeJSONText(saved, &observation)
		}
		if observation.Version != version || observation.Since == "" {
			observation = installedVersionObservation{Version: version, Since: formatTime(c.clock())}
			if _, err := c.DB.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, jsonText(observation)); err != nil {
				return err
			}
		}
		a.installedVersions[app] = observation
	}
	return nil
}

// Captures from this Mac can use the local observation; other hosts cannot.
func (a *antigravityAdapter) installedHarnessVersion(app string) installedVersionObservation {
	if a.view != nil && a.view.offHost() {
		return installedVersionObservation{}
	}
	return a.installedVersions[app]
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
