package archive

import (
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Each bundled CLI holds a shared lease for its entire lifetime. The native
// app creates a pending marker before stopping the service/MCP, then takes the
// exclusive lease before allowing Sparkle to replace the bundle. The marker
// survives the app's exit, closing the gap before Sparkle installs/relaunches.
// Keep the path convention in sync with macos/UpdateLifecycle.swift.
func acquireAppUpdateLease(executable, support string) (*os.File, error) {
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	const suffix = "/Contents/MacOS/pharos"
	if !strings.HasSuffix(executable, suffix) {
		return nil, nil // Source builds and standalone CLI installations.
	}
	app := strings.TrimSuffix(executable, suffix)
	if !strings.HasSuffix(app, ".app") {
		return nil, nil
	}
	directory := filepath.Join(support, "updates")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	base := filepath.Join(directory, fmt.Sprintf("%x", sha256.Sum256([]byte(app))))
	lease, err := os.Open(base + ".lock")
	if errors.Is(err, os.ErrNotExist) {
		lease, err = os.OpenFile(base+".lock", os.O_CREATE|os.O_EXCL|os.O_RDONLY, 0o600)
		if errors.Is(err, os.ErrExist) {
			lease, err = os.Open(base + ".lock")
		}
	}
	if err != nil {
		return nil, fmt.Errorf("open Pharos update lock: %w", err)
	}
	if err = syscall.Flock(int(lease.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		lease.Close()
		return nil, appUpdateLockError(err)
	}
	if pending, err := os.Open(base + ".pending"); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			lease.Close()
			return nil, fmt.Errorf("check Pharos update state: %w", err)
		}
		defer pending.Close()
		data, err := io.ReadAll(pending)
		if err != nil {
			lease.Close()
			return nil, fmt.Errorf("read Pharos update state: %w", err)
		}
		var state struct {
			Version string `json:"version"`
		}
		installed, versionErr := appBundleVersion(app)
		if json.Unmarshal(data, &state) != nil || state.Version == "" || versionErr != nil || installed == state.Version {
			lease.Close()
			return nil, fmt.Errorf("Pharos is updating; reopen Pharos.app before reconnecting")
		}
		if err := removeCompletedAppUpdate(pending, base+".pending"); err != nil {
			lease.Close()
			return nil, err
		}
	}
	return lease, nil
}

// Serialize stale-marker removers on the opened marker inode. The bundle's
// shared lease excludes the installer; this second, short lock ensures a
// reader of an unlinked marker cannot delete a fresh GUI handoff at that path.
// GUI creation is exclusive and never overwrites an existing marker.
func removeCompletedAppUpdate(pending *os.File, path string) error {
	if err := syscall.Flock(int(pending.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil // Another CLI is already cleaning up this completed handoff.
		}
		return fmt.Errorf("lock completed Pharos update state: %w", err)
	}
	opened, err := pending.Stat()
	if err != nil {
		return fmt.Errorf("inspect completed Pharos update state: %w", err)
	}
	current, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect Pharos update state: %w", err)
	}
	if !os.SameFile(opened, current) {
		return fmt.Errorf("Pharos update state changed; reconnect after the app reopens")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("clear completed Pharos update state: %w", err)
	}
	return nil
}

func appUpdateLockError(err error) error {
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return fmt.Errorf("Pharos is updating; reconnect after the app reopens: %w", err)
	}
	return fmt.Errorf("acquire Pharos update lock: %w", err)
}

// build-app.sh writes an XML Info.plist. Read only the bundle version, with no
// shell commands or macOS dependencies in CLI startup.
func appBundleVersion(app string) (string, error) {
	f, err := os.Open(filepath.Join(app, "Contents", "Info.plist"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	decoder := xml.NewDecoder(f)
	for {
		token, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				return "", fmt.Errorf("missing CFBundleVersion")
			}
			return "", err
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == "key" {
			var key string
			if err := decoder.DecodeElement(&key, &start); err != nil {
				return "", err
			}
			if key != "CFBundleVersion" {
				continue
			}
			for {
				token, err := decoder.Token()
				if err != nil {
					return "", err
				}
				if _, ok := token.(xml.CharData); ok {
					continue
				}
				value, ok := token.(xml.StartElement)
				if !ok || value.Name.Local != "string" {
					return "", fmt.Errorf("invalid CFBundleVersion")
				}
				var version string
				if err := decoder.DecodeElement(&version, &value); err != nil {
					return "", err
				}
				if version == "" {
					return "", fmt.Errorf("empty CFBundleVersion")
				}
				return version, nil
			}
		}
	}
}
