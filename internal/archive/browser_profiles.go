package archive

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// browserProfile is one place a download link can open: a Chromium-family
// browser profile, keyed "<browser>:<profile directory>". The empty key is
// the default browser. People with several signed-in profiles choose the one
// signed in to Claude, so a link is not spent on the wrong account.
type browserProfile struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// app is the application bundle; dir is the profile directory name
	// passed to --profile-directory. Neither leaves this Mac's service.
	app, dir string
}

// chromiumBrowser is a Chromium-family browser that keeps its profiles in a
// Local State file and accepts --profile-directory.
type chromiumBrowser struct{ ID, Name, App, Data string }

var chromiumBrowsers = []chromiumBrowser{
	{"chrome", "Google Chrome", "Google Chrome.app", "Google/Chrome"},
	{"brave", "Brave", "Brave Browser.app", "BraveSoftware/Brave-Browser"},
	{"edge", "Microsoft Edge", "Microsoft Edge.app", "Microsoft Edge"},
	{"vivaldi", "Vivaldi", "Vivaldi.app", "Vivaldi"},
	{"chromium", "Chromium", "Chromium.app", "Chromium"},
}

// chromiumProfiles reads a Local State file's profile list. A profile
// directory must be a plain name, never a path or something read as a flag.
func chromiumProfiles(browser chromiumBrowser, app string, localState []byte) []browserProfile {
	var state struct {
		Profile struct {
			InfoCache map[string]struct {
				Name     string `json:"name"`
				UserName string `json:"user_name"`
			} `json:"info_cache"`
		} `json:"profile"`
	}
	if json.Unmarshal(localState, &state) != nil {
		return nil
	}
	var profiles []browserProfile
	for dir, info := range state.Profile.InfoCache {
		if !browserProfileDir(dir) {
			continue
		}
		label := browser.Name + " — " + strings.TrimSpace(info.Name)
		if strings.TrimSpace(info.Name) == "" {
			label = browser.Name + " — " + dir
		}
		if info.UserName != "" {
			label += " (" + info.UserName + ")"
		}
		profiles = append(profiles, browserProfile{Key: browser.ID + ":" + dir, Label: label, app: app, dir: dir})
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Key < profiles[j].Key })
	return profiles
}

func browserProfileDir(dir string) bool {
	return dir != "" && len(dir) <= 64 && !strings.HasPrefix(dir, "-") && !strings.HasPrefix(dir, ".") && !strings.ContainsAny(dir, "/\\:\x00\n\r")
}

// listBrowserProfiles and openBrowserProfile are replaced in tests.
var listBrowserProfiles = browserProfilesNative

var openBrowserProfile = func(profile browserProfile, link string) error {
	if safeClaudeDownloadURL(link) == "" || strings.HasPrefix(link, "-") || !browserProfileDir(profile.dir) || profile.app == "" {
		return errors.New("Pharos opens only Claude https download links in a listed browser profile")
	}
	return browserProfileOpenNative(profile.app, profile.dir, link)
}

// openClaudeLink opens link where key says: a listed profile, or the default
// browser for the empty key. A profile that is no longer listed is an error,
// never a silent fallback to a browser that may be signed in elsewhere.
func openClaudeLink(key, link string) error {
	if key == "" {
		return openBrowserURL(link)
	}
	for _, profile := range listBrowserProfiles() {
		if profile.Key == key {
			return openBrowserProfile(profile, link)
		}
	}
	return errors.New("the chosen browser profile is no longer available; choose another")
}

func browserProfileLabel(key string, profiles []browserProfile) string {
	if key == "" {
		return "your default browser"
	}
	for _, profile := range profiles {
		if profile.Key == key {
			return profile.Label
		}
	}
	return key
}
