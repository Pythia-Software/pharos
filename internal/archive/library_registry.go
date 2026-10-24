package archive

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const libraryDescriptorName = "pharos-library.json"
const libraryFormatVersion = 1

var libraryVolumeIdentity = volumeIdentity

func libraryUseLock(config Config) string {
	return filepath.Join(filepath.Dir(canonical(config.CatalogPath)), ".pharos-use.lock")
}

// LibraryDescriptor belongs to the data, never to an installed application.
// Instance changes on restore; receipts must not identify a target by its name.
type LibraryDescriptor struct {
	Version       int    `json:"version"`
	ID            string `json:"id"`
	Instance      string `json:"instance"`
	Name          string `json:"name"`
	Inode         uint64 `json:"directory_inode"`
	RecoveredFrom string `json:"recovered_from_library_id,omitempty"`
	RecoveryPoint string `json:"recovery_point,omitempty"`
	// Sticky protection: pausing a delivery pair must not make undelivered
	// evidence eligible for ordinary snapshot pruning, including CLI capture.
	RetainPendingEvidence bool `json:"retain_pending_evidence,omitempty"`
}

type registeredLibrary struct {
	LibraryDescriptor
	Config   string `json:"config"`
	Volume   string `json:"volume,omitempty"`
	Relative string `json:"relative,omitempty"`
}

type installationRegistry struct {
	Version        int                 `json:"version"`
	Application    string              `json:"application"`
	Libraries      []registeredLibrary `json:"libraries"`
	MCP            string              `json:"mcp_library"`
	Selected       string              `json:"selected_library"`
	DeliverySource string              `json:"delivery_source,omitempty"`
	DeliveryTarget string              `json:"delivery_target,omitempty"`
}

// fileLease is deliberately separate from SQLite's short transaction locks:
// offline migrations must exclude services, captures and other CLI writers.
func fileLease(path string, exclusive bool) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	if err = syscall.Flock(int(f.Fd()), mode|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("Pharos is using this library; close its active operation and retry: %w", err)
	}
	return f, nil
}

func durableJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err = writeFileAtomic(path, append(data, '\n'), 0o600); err != nil {
		return err
	}
	f, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func descriptorPath(config Config) string {
	if config.Library {
		return filepath.Join(filepath.Dir(config.Path), libraryDescriptorName)
	}
	return filepath.Join(filepath.Dir(config.CatalogPath), libraryDescriptorName)
}

func readLibraryDescriptor(config Config) (LibraryDescriptor, error) {
	var d LibraryDescriptor
	b, err := os.ReadFile(descriptorPath(config))
	if err != nil {
		return d, err
	}
	if err = json.Unmarshal(b, &d); err != nil {
		return d, err
	}
	if d.Version != libraryFormatVersion || d.ID == "" || d.Instance == "" {
		return d, fmt.Errorf("this library needs a newer Pharos or a valid library descriptor")
	}
	info, err := os.Stat(filepath.Dir(descriptorPath(config)))
	if err != nil {
		return d, err
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && d.Inode != uint64(stat.Ino) {
		return d, fmt.Errorf("this is a copied library; use Recover library to create its independent writable identity")
	}
	return d, nil
}

func ensureLibraryDescriptor(config Config, name string) (LibraryDescriptor, error) {
	d, err := readLibraryDescriptor(config)
	if err == nil {
		return d, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return d, err
	}
	// Never create a descriptor (or a missing mount point) for absent data.
	if _, err = os.Stat(config.CatalogPath); err != nil {
		return d, err
	}
	lock, err := fileLease(descriptorPath(config)+".lock", true)
	if err != nil {
		return d, err
	}
	defer lock.Close()
	if d, err = readLibraryDescriptor(config); err == nil {
		return d, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return d, err
	}
	if name = strings.TrimSpace(name); name == "" {
		name = filepath.Base(filepath.Dir(config.Path))
	}
	info, err := os.Stat(filepath.Dir(descriptorPath(config)))
	if err != nil {
		return d, err
	}
	d = LibraryDescriptor{Version: libraryFormatVersion, ID: randomToken(), Instance: randomToken(), Name: name}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		d.Inode = uint64(stat.Ino)
	}
	return d, durableJSON(descriptorPath(config), d)
}

func protectPendingEvidence(config Config) error {
	d, err := ensureLibraryDescriptor(config, "")
	if err != nil || d.RetainPendingEvidence {
		return err
	}
	d.RetainPendingEvidence = true
	return durableJSON(descriptorPath(config), d)
}

func installationID(application string) string {
	// Per-Mac support storage and host identity keep an SSD installation's
	// assignment independent on each Mac. Canonicalization unifies symlinks.
	location := canonical(application)
	if mount, _, err := volumeMount(location); err == nil {
		if volume := libraryVolumeIdentity(location); strings.HasPrefix(volume, "uuid:") {
			rel, _ := filepath.Rel(mount, location)
			location = volume + "\x00" + rel
		}
	}
	key := currentHost().ID + "\x00" + location
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:16])
}

func installationPath(id string) string {
	return filepath.Join(pharosSupportDir(), "applications", id, "registry.json")
}

func loadInstallation(application string) (installationRegistry, error) {
	id := installationID(application)
	r := installationRegistry{Version: 1, Application: canonical(application), Libraries: []registeredLibrary{}}
	b, err := os.ReadFile(installationPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(b, &r); err != nil {
		return r, err
	}
	if r.Version != 1 {
		return r, fmt.Errorf("this application registry needs a newer Pharos")
	}
	r.Application = canonical(application)
	return r, nil
}

func (r *installationRegistry) library(id string) *registeredLibrary {
	for i := range r.Libraries {
		if r.Libraries[i].ID == id {
			return &r.Libraries[i]
		}
	}
	return nil
}

func (r *installationRegistry) register(config Config, name string) (string, error) {
	if err := config.checkLibrary(libraryVolumeIdentity); err != nil {
		return "", err
	}
	if err := checkCatalogCompatibility(config.CatalogPath); err != nil {
		return "", err
	}
	d, err := ensureLibraryDescriptor(config, name)
	if err != nil {
		return "", err
	}
	for _, registered := range r.Libraries {
		if registered.ID != d.ID {
			if other, e := LoadConfig(registered.Config); e == nil && sameFile(config.CatalogPath, other.CatalogPath) {
				return "", fmt.Errorf("this catalog already belongs to the registered library %s", registered.Name)
			}
		}
	}
	if old := r.library(d.ID); old != nil {
		if old.Instance != d.Instance {
			return "", fmt.Errorf("a different writable copy of %s is already registered; recover it separately", d.Name)
		}
		old.Config = config.Path
		return d.ID, nil
	}
	entry := registeredLibrary{LibraryDescriptor: d, Config: config.Path, Volume: config.VolumeID}
	if mount, _, err := volumeMount(config.Path); err == nil {
		entry.Relative, _ = filepath.Rel(mount, config.Path)
	}
	r.Libraries = append(r.Libraries, entry)
	if r.MCP == "" {
		r.MCP = d.ID
	}
	return d.ID, nil
}

func (r *installationRegistry) save() error {
	return durableJSON(installationPath(installationID(r.Application)), r)
}

// Compatibility is checked read-only, before adoption, snapshots or any PRAGMA
// that could change a catalog written by a newer release.
func checkCatalogCompatibility(path string) error {
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	return (&Catalog{Path: path, DB: db}).checkSchemaVersion()
}

func (l *registeredLibrary) resolve() string {
	if _, err := os.Stat(l.Config); err == nil {
		return l.Config
	}
	if !strings.HasPrefix(l.Volume, "uuid:") || !filepath.IsLocal(l.Relative) {
		return l.Config
	}
	entries, _ := os.ReadDir("/Volumes")
	for _, entry := range entries {
		mount := filepath.Join("/Volumes", entry.Name())
		if libraryVolumeIdentity(mount) != l.Volume {
			continue
		}
		path := filepath.Join(mount, l.Relative)
		config, err := LoadConfig(path)
		if err != nil {
			continue
		}
		d, err := readLibraryDescriptor(config)
		if err == nil && d.ID == l.ID && d.Instance == l.Instance {
			l.Config = path
			return path
		}
	}
	return l.Config
}

func (l *registeredLibrary) load() (Config, error) {
	config, err := LoadConfig(l.resolve())
	if err != nil {
		return config, err
	}
	if err = config.checkLibrary(libraryVolumeIdentity); err != nil {
		return config, err
	}
	d, err := readLibraryDescriptor(config)
	if err != nil {
		return config, err
	}
	if d.ID != l.ID || d.Instance != l.Instance {
		return config, fmt.Errorf("%s was restored or replaced; register its recovery candidate explicitly before using it", l.Name)
	}
	return config, checkCatalogCompatibility(config.CatalogPath)
}
