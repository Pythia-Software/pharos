package archive

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/gbdubs/pharos/internal/platform/credentialstore"
	"github.com/gbdubs/pharos/internal/teams"
)

// The default host adapter uses protected per-installation files outside all
// libraries, transfers and backups. Its narrow Store interface permits a
// future Keychain implementation without changing enrollment or policy.
var teamsCredentialStore = func(cfg Config) (credentialstore.Store, error) {
	root := filepath.Join(pharosSupportDir(), "teams-credentials")
	resolvedRoot := canonical(root)
	for _, path := range []string{filepath.Dir(cfg.CatalogPath), cfg.ArchiveRoot, cfg.CaptureRoot, cfg.StagingRoot} {
		if path != "" && within(resolvedRoot, canonical(path)) {
			return nil, credentialstore.ErrUnsafe
		}
	}
	if cfg.Library && cfg.Path != "" && within(resolvedRoot, canonical(filepath.Dir(cfg.Path))) {
		return nil, credentialstore.ErrUnsafe
	}
	return credentialstore.New(root), nil
}

func teamsCredentialName(applicationID, libraryID string) string {
	sum := sha256.Sum256([]byte("credential\x00" + applicationID + "\x00" + libraryID))
	return hex.EncodeToString(sum[:])
}
func teamsSourceKeyCheck(credential *teams.EnrollmentCredential) []byte {
	key := credential.SourceKey()
	defer clear(key)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("pharos-source-key-check-v1"))
	return mac.Sum(nil)
}
func teamsSourceKeyName(org, person string) string {
	sum := sha256.Sum256([]byte("source-key-check\x00" + org + "\x00" + person))
	return hex.EncodeToString(sum[:])
}

// saveTeamsCredential persists the credential before recording an enrollment.
// The membership remains disabled and opted out; policy acknowledgement and
// recovery reconciliation are separate explicit steps.
func (c *Catalog) saveTeamsCredential(ctx context.Context, cfg Config, d LibraryDescriptor, credential *teams.EnrollmentCredential) error {
	if credential == nil || cfg.Application == "" || d.ID == "" || d.Instance == "" || credential.LibraryID != d.ID {
		return errTeamsNotEnrolled
	}
	settings, err := readTeamsSettings(ctx, c.reader())
	if err != nil {
		return err
	}
	if settings.LibraryID != "" && (settings.LibraryID != d.ID || settings.InstanceID != d.Instance || settings.OrgID != credential.OrgID) {
		return errTeamsNotEnrolled
	}
	installationKey, err := teamsInstallationKey(cfg, credential.Origin, false)
	if err != nil {
		return err
	}
	defer clear(installationKey)
	returnedKey := credential.SigningKey()
	defer clear(returnedKey)
	if subtle.ConstantTimeCompare(installationKey, returnedKey) != 1 {
		return errTeamsNotEnrolled
	}
	appID := installationID(cfg.Application)
	raw, err := credential.CredentialBytes(d.Instance, appID)
	if err != nil {
		return err
	}
	defer clear(raw)
	verified, err := teams.ParseCredential(raw, d.ID, d.Instance, appID, false)
	if err != nil {
		return err
	}
	verified.Destroy()
	store, err := teamsCredentialStore(cfg)
	if err != nil {
		return err
	}
	check := teamsSourceKeyCheck(credential)
	defer clear(check)
	checkName := teamsSourceKeyName(credential.OrgID, credential.PersonID)
	if err = store.Save(checkName, check); errors.Is(err, credentialstore.ErrExists) {
		prior, e := store.Load(checkName)
		if e != nil {
			return e
		}
		defer clear(prior)
		if subtle.ConstantTimeCompare(check, prior) != 1 {
			return errTeamsNotEnrolled
		}
	} else if err != nil {
		return err
	}
	if err = store.Save(teamsCredentialName(appID, d.ID), raw); err != nil {
		return err
	}
	public := returnedKey.Public().(ed25519.PublicKey)
	keyFingerprint := teamsPayloadHash(public)
	if _, err = c.setTeamsLocalSettings(ctx, d.ID, d.Instance, credential.OrgID, credential.InstallationID, keyFingerprint, false, true, 0); err != nil {
		return errors.New("Teams credential saved but enrollment needs reconciliation")
	}
	return nil
}
func (c *Catalog) loadTeamsCredential(cfg Config, d LibraryDescriptor) (*teams.EnrollmentCredential, error) {
	if cfg.Application == "" || d.ID == "" || d.Instance == "" {
		return nil, errTeamsNotEnrolled
	}
	appID := installationID(cfg.Application)
	store, err := teamsCredentialStore(cfg)
	if err != nil {
		return nil, err
	}
	raw, err := store.Load(teamsCredentialName(appID, d.ID))
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	credential, err := teams.ParseCredential(raw, d.ID, d.Instance, appID, false)
	if err != nil {
		return nil, errTeamsNotEnrolled
	}
	settings, e := readTeamsSettings(context.Background(), c.reader())
	if e != nil {
		credential.Destroy()
		return nil, e
	}
	key := credential.SigningKey()
	fingerprint := teamsPayloadHash(key.Public().(ed25519.PublicKey))
	clear(key)
	if settings.LibraryID != d.ID || settings.InstanceID != d.Instance || settings.OrgID != credential.OrgID || settings.InstallationID != credential.InstallationID || settings.KeyFingerprint != fingerprint {
		credential.Destroy()
		return nil, errTeamsNotEnrolled
	}
	return credential, nil
}

type teamsInstallationRecord struct {
	Version       int    `json:"version"`
	ApplicationID string `json:"application_id"`
	Origin        string `json:"origin"`
	PrivateKey    string `json:"private_key"`
}

func teamsInstallationName(appID, origin string) string {
	sum := sha256.Sum256([]byte("installation-key\x00" + appID + "\x00" + origin))
	return hex.EncodeToString(sum[:])
}

// The app/service pair owns one signing key. Every separately authorized
// library enrollment reuses it; the server may bind several libraries to the
// same installation. Concurrent creation loads the atomically published key.
func teamsInstallationKey(cfg Config, origin string, create bool) (ed25519.PrivateKey, error) {
	if cfg.Application == "" {
		return nil, errTeamsNotEnrolled
	}
	canonicalOrigin, err := teams.CanonicalOrigin(origin, false)
	if err != nil {
		return nil, err
	}
	store, err := teamsCredentialStore(cfg)
	if err != nil {
		return nil, err
	}
	appID := installationID(cfg.Application)
	name := teamsInstallationName(appID, canonicalOrigin)
	raw, err := store.Load(name)
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return nil, errTeamsNotEnrolled
		}
		_, created, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return nil, e
		}
		record := teamsInstallationRecord{Version: 1, ApplicationID: appID, Origin: canonicalOrigin, PrivateKey: base64.RawURLEncoding.EncodeToString(created)}
		encoded, e := json.Marshal(record)
		if e == nil {
			e = store.Save(name, encoded)
		}
		clear(encoded)
		clear(created)
		if e != nil && !errors.Is(e, credentialstore.ErrExists) {
			return nil, e
		}
		raw, err = store.Load(name)
	}
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	if !teams.ValidUniqueJSON(raw) {
		return nil, errTeamsNotEnrolled
	}
	var record teamsInstallationRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record) != nil || record.Version != 1 || record.ApplicationID != appID || record.Origin != canonicalOrigin {
		return nil, errTeamsNotEnrolled
	}
	key, err := base64.RawURLEncoding.Strict().DecodeString(record.PrivateKey)
	if err != nil || len(key) != ed25519.PrivateKeySize || base64.RawURLEncoding.EncodeToString(key) != record.PrivateKey {
		return nil, errTeamsNotEnrolled
	}
	derived := ed25519.NewKeyFromSeed(key[:ed25519.SeedSize])
	valid := subtle.ConstantTimeCompare(derived, key) == 1
	clear(derived)
	if !valid {
		clear(key)
		return nil, errTeamsNotEnrolled
	}
	return key, nil
}
