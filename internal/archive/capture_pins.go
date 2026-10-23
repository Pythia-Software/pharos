package archive

// Pins and all record-store publication use the per-host capture flock. Lock
// order is index lock -> capture lock -> catalog transaction. A parser never
// holds the capture lock. Pins have no time based expiry.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

const preservationPinsName = "preservation-pins.json"

type preservationPin struct {
	Dependencies []string `json:"dependencies,omitempty"`
	ClosureBytes int64    `json:"closure_bytes"`
	ID           string   `json:"id"`
	Owner        string   `json:"owner"`
	Purpose      string   `json:"purpose"`
	Path         string   `json:"path"`
	SHA256       string   `json:"sha256"`
	Size         int64    `json:"size"`
	CreatedAt    string   `json:"created_at"`
	ReleasedAt   string   `json:"released_at,omitempty"`
}
type preservationPins struct {
	Version int               `json:"version"`
	Pins    []preservationPin `json:"pins"`
	// An optional job-owned capacity gate stops all new source acquisitions.
	// Backfill can instead reserve a finite remaining byte allowance. Spending
	// is persisted before acquisition, so a crash can waste allowance, never
	// manufacture capacity. Only the owning job may remove its gate.
	Gates map[string]int64 `json:"acquisition_allowances,omitempty"`
}

func loadPreservationPins(dir string) (preservationPins, error) {
	p := preservationPins{Version: 1}
	b, err := os.ReadFile(filepath.Join(dir, preservationPinsName))
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(b, &p); err != nil {
		return p, err
	}
	if p.Version != 1 {
		return p, fmt.Errorf("unsupported preservation pins version %d; upgrade Pharos", p.Version)
	}
	for _, pin := range p.Pins {
		for _, dependency := range pin.Dependencies {
			if !safeRetainedPath(dependency) {
				return p, errors.New("unsafe pin dependency")
			}
		}
		if !safeRetainedPath(pin.Path) || pin.Size < 0 || !validRecordHash(pin.SHA256) || pin.Owner == "" {
			return p, errors.New("invalid preservation pin; capture stopped")
		}
	}
	return p, nil
}
func safeRetainedPath(p string) bool {
	return filepath.ToSlash(filepath.Clean(p)) == p && !strings.ContainsAny(p, "\\\x00") && (strings.HasPrefix(p, "history/") || strings.HasPrefix(p, "records/v1/")) && !strings.Contains(p, "/../")
}
func savePreservationPins(dir string, p preservationPins) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := ensureFree(dir, int64(len(b))+4096); err != nil {
		return err
	}
	return writeFileDurably(filepath.Join(dir, preservationPinsName), b)
}
func pinnedPath(dir, rel string) (bool, error) {
	p, err := loadPreservationPins(dir)
	if err != nil {
		return true, err
	}
	for _, pin := range p.Pins {
		if pin.ReleasedAt == "" {
			if pin.Path == rel {
				return true, nil
			}
			for _, dependency := range pin.Dependencies {
				if dependency == rel {
					return true, nil
				}
			}
		}
	}
	return false, nil
}
func fileSHA256(file string) (string, int64, error) {
	return fileSHA256Context(context.Background(), file)
}
func fileSHA256Context(ctx context.Context, file string) (string, int64, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := newRecordHash()
	n, err := io.Copy(h, &recordCountReader{r: f, ctx: ctx})
	return fmt.Sprintf("%x", h.Sum(nil)), n, err
}

// pinVersion must be called under the host lock. A latest path is first made
// immutable, even with capture_snapshot_generations=0. Overlapping owners
// get independent IDs and releases are idempotent.
func (c *sourceCapture) pinVersion(owner, purpose string, v capturedVersion, dependencies ...string) (preservationPin, error) {
	if owner == "" || purpose == "" {
		return preservationPin{}, errors.New("pin owner and purpose are required")
	}
	p, err := loadPreservationPins(c.dir)
	if err != nil {
		return preservationPin{}, err
	}
	if strings.HasPrefix(v.Captured, "files/") {
		digest, size, err := fileSHA256Context(c.ctx, filepath.Join(c.dir, filepath.FromSlash(v.Captured)))
		if err != nil {
			return preservationPin{}, err
		}
		if size != v.Size || v.SHA256 != "" && digest != v.SHA256 {
			return preservationPin{}, errors.New("latest pin input changed; reconcile inventory")
		}
		v.SHA256 = digest
		v, err = c.preserve(v, true)
		if err != nil {
			return preservationPin{}, err
		}
	}
	if !safeRetainedPath(v.Captured) {
		return preservationPin{}, errors.New("pin input is not immutable")
	}
	digest, size, err := fileSHA256Context(c.ctx, filepath.Join(c.dir, filepath.FromSlash(v.Captured)))
	if err != nil {
		return preservationPin{}, err
	}
	if size != v.Size || v.SHA256 != "" && digest != v.SHA256 {
		return preservationPin{}, errors.New("pin input changed; reconcile inventory")
	}
	pin := preservationPin{ID: hashBytes([]byte(owner + "\x00" + v.Captured + "\x00" + digest)), Owner: owner, Purpose: purpose, Path: v.Captured, SHA256: digest, Size: size, CreatedAt: now()}
	pin.ClosureBytes = size
	seen := map[string]bool{v.Captured: true}
	for _, dependency := range dependencies {
		if seen[dependency] {
			continue
		}
		seen[dependency] = true
		if !safeRetainedPath(dependency) {
			return preservationPin{}, errors.New("unsafe pin dependency")
		}
		info, e := os.Stat(filepath.Join(c.dir, filepath.FromSlash(dependency)))
		if e != nil || !info.Mode().IsRegular() {
			return preservationPin{}, errors.New("pin dependency missing; reconcile inventory")
		}
		pin.Dependencies = append(pin.Dependencies, dependency)
		pin.ClosureBytes += info.Size()
	}
	sort.Strings(pin.Dependencies)
	for _, old := range p.Pins {
		if old.ID == pin.ID && old.ReleasedAt == "" {
			return old, nil
		}
	}
	// Version 2 makes pre-pin writers refuse the source before touching files.
	if err = c.upgradePreservationWriter(); err != nil {
		return preservationPin{}, err
	}
	p.Pins = append(p.Pins, pin)
	err = savePreservationPins(c.dir, p)
	return pin, err
}
func releasePreservationOwner(dir, owner string) error {
	p, err := loadPreservationPins(dir)
	if err != nil {
		return err
	}
	for i := range p.Pins {
		if p.Pins[i].Owner == owner && p.Pins[i].ReleasedAt == "" {
			p.Pins[i].ReleasedAt = now()
		}
	}
	return savePreservationPins(dir, p)
}
func reserveCaptureAcquisition(dir string, bytes int64) error {
	p, err := loadPreservationPins(dir)
	if err != nil {
		return err
	}
	for _, left := range p.Gates {
		if left < bytes {
			return fmt.Errorf("capture pending: preservation job capacity exhausted (needs %d bytes)", bytes)
		}
	}
	if len(p.Gates) == 0 {
		return nil
	}
	for owner, left := range p.Gates {
		p.Gates[owner] = left - bytes
	}
	return savePreservationPins(dir, p)
}

// lockCaptureTarget also works off-host. Never call it from a captureSession:
// that session already owns this flock.
func lockCaptureTarget(ctx context.Context, dir string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(filepath.Dir(dir), captureLockName), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errCaptureBusy
		}
		return nil, err
	}
	return f, nil
}

// Reader/job bookkeeping must not claim a new capture observation or overwrite
// its last-run diagnostics. Only the writer compatibility version changes.
func (c *sourceCapture) upgradePreservationWriter() error {
	if c.manifest.Version >= captureManifestVersion {
		return nil
	}
	c.manifest.Version = captureManifestVersion
	data, e := json.Marshal(c.manifest)
	if e != nil {
		return e
	}
	return writeRecordMetadata(filepath.Join(c.dir, captureManifestName), data)
}
