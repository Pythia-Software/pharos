//go:build darwin

package archive

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

const browserOpenSupported = true

// browserOpenNative hands an https link to Launch Services, which opens it in
// the default browser with its existing sign-ins. -g leaves the browser in the
// background, so a queued download does not take the person's focus.
func browserOpenNative(link string) error {
	// open's output can repeat the link, a download capability, so only its
	// exit status is reported.
	if err := exec.Command("/usr/bin/open", "-g", link).Run(); err != nil {
		return fmt.Errorf("open: %v", err)
	}
	return nil
}

// browserProfileOpenNative opens link in one Chromium profile. -n starts a
// short-lived launcher process that hands the request to the running browser,
// which is how a profile is chosen while the browser is already open.
func browserProfileOpenNative(app, dir, link string) error {
	if err := exec.Command("/usr/bin/open", "-g", "-n", "-a", app, "--args", "--profile-directory="+dir, link).Run(); err != nil {
		return fmt.Errorf("open: %v", err)
	}
	return nil
}

// browserProfilesNative lists installed Chromium-family browsers' profiles
// from their Local State files. It reads nothing else in a browser's data.
func browserProfilesNative() []browserProfile {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var profiles []browserProfile
	for _, browser := range chromiumBrowsers {
		app := ""
		for _, dir := range []string{"/Applications", filepath.Join(home, "Applications")} {
			if info, err := os.Stat(filepath.Join(dir, browser.App)); err == nil && info.IsDir() {
				app = filepath.Join(dir, browser.App)
				break
			}
		}
		if app == "" {
			continue
		}
		file, err := os.Open(filepath.Join(home, "Library", "Application Support", browser.Data, "Local State"))
		if err != nil {
			continue
		}
		content, err := io.ReadAll(io.LimitReader(file, 16<<20))
		file.Close()
		if err == nil {
			profiles = append(profiles, chromiumProfiles(browser, app, content)...)
		}
	}
	return profiles
}
