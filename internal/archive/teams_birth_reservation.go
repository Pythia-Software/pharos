package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/gbdubs/pharos/internal/platform/credentialstore"
)

// The path-indexed reservation survives every interrupted create. Its binding is
// deliberately separate from catalog bytes, so an existing leaf is never proof
// that a new library was created here.
type teamsBirthReservation struct {
	Version         int                   `json:"version"`
	Phase           string                `json:"phase"`
	Scope           string                `json:"scope"`
	Application     string                `json:"application"`
	Path            string                `json:"path"`
	Host            string                `json:"host"`
	UID             int                   `json:"uid"`
	LibraryID       string                `json:"library_id"`
	InstanceID      string                `json:"instance_id"`
	ImageSHA256     string                `json:"image_sha256,omitempty"`
	Tuple           *teamsCheckpointTuple `json:"tuple,omitempty"`
	DirectoryDevice uint64                `json:"directory_device,omitempty"`
	DirectoryInode  uint64                `json:"directory_inode,omitempty"`
	CatalogDevice   uint64                `json:"catalog_device,omitempty"`
	CatalogInode    uint64                `json:"catalog_inode,omitempty"`
}

func teamsBirthReservationName(applicationID, path string) string {
	sum := sha256.Sum256([]byte("pharos-managed-birth-location-v1\x00" + applicationID + "\x00" + path))
	return hex.EncodeToString(sum[:])
}
func (r teamsBirthReservation) valid() bool {
	if r.Version != 1 || r.Scope != teamsCheckpointScope(r.Application, r.LibraryID) || r.Application == "" || r.Host == "" || r.UID < 0 || r.LibraryID == "" || r.InstanceID == "" || !filepath.IsAbs(r.Path) || filepath.Clean(r.Path) != r.Path {
		return false
	}
	switch r.Phase {
	case "reserved":
		return r.ImageSHA256 == "" && r.Tuple == nil && r.DirectoryInode == 0 && r.CatalogInode == 0
	case "image_bound":
		return teamsCheckpointHex(r.ImageSHA256, 64) && teamsCheckpointTupleValid(r.Tuple) && r.Tuple.Instance == teamsCheckpointInstance(r.LibraryID, r.InstanceID) && r.DirectoryInode != 0 && r.CatalogInode != 0
	case "committed":
		return teamsCheckpointHex(r.ImageSHA256, 64) && teamsCheckpointTupleValid(r.Tuple) && r.Tuple.Instance == teamsCheckpointInstance(r.LibraryID, r.InstanceID) && r.DirectoryInode != 0 && r.CatalogInode != 0
	default:
		return false
	}
}
func teamsEncodeBirthReservation(r teamsBirthReservation) ([]byte, error) {
	if !r.valid() {
		return nil, errTeamsProtectionContinuity
	}
	return json.Marshal(r)
}
func teamsReadBirthReservation(store credentialstore.Store, name string) ([]byte, teamsBirthReservation, error) {
	raw, err := store.Load(name)
	if err != nil {
		return nil, teamsBirthReservation{}, err
	}
	var r teamsBirthReservation
	if err = json.Unmarshal(raw, &r); err != nil || !r.valid() {
		return nil, teamsBirthReservation{}, errTeamsProtectionContinuity
	}
	canonical, err := teamsEncodeBirthReservation(r)
	if err != nil || string(canonical) != string(raw) {
		return nil, teamsBirthReservation{}, errTeamsProtectionContinuity
	}
	return raw, r, nil
}
func teamsAdvanceBirthReservation(store credentialstore.Store, name string, oldRaw []byte, old, next teamsBirthReservation) ([]byte, error) {
	if old.Phase == "reserved" && next.Phase != "image_bound" || old.Phase == "image_bound" && next.Phase != "committed" || old.Phase == "committed" {
		return nil, errTeamsProtectionContinuity
	}
	if old.Version != next.Version || old.Scope != next.Scope || old.Application != next.Application || old.Path != next.Path || old.Host != next.Host || old.UID != next.UID || old.LibraryID != next.LibraryID || old.InstanceID != next.InstanceID {
		return nil, errTeamsProtectionContinuity
	}
	if old.Phase == "image_bound" && (old.ImageSHA256 != next.ImageSHA256 || old.Tuple == nil || next.Tuple == nil || *old.Tuple != *next.Tuple) {
		return nil, errTeamsProtectionContinuity
	}
	encoded, err := teamsEncodeBirthReservation(next)
	if err != nil {
		return nil, err
	}
	if len(oldRaw) == 0 {
		return nil, errors.New("missing birth reservation predecessor")
	}
	if err = store.Replace(name, oldRaw, encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}
