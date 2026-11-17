package teams

import (
	"bytes"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
)

// credentialRecord is serialized only to the host-owned credential store. It
// is never an API response, catalog row, or browser-visible object.
type credentialRecord struct {
	Version         int            `json:"version"`
	Origin          string         `json:"origin"`
	OrgID           string         `json:"org_id"`
	PersonID        string         `json:"person_id"`
	InstallationID  string         `json:"installation_id"`
	LibraryID       string         `json:"library_id"`
	LibraryInstance string         `json:"library_instance"`
	ApplicationID   string         `json:"application_id"`
	PrivateKey      string         `json:"private_key"`
	SourceTokenKey  string         `json:"source_token_key"`
	MetricsDefault  MetricsDefault `json:"metrics_default"`
}

// CredentialBytes returns sensitive bytes for immediate secure storage only.
// The instance is bound so a copied or restored library cannot resume work.
func (c *EnrollmentCredential) CredentialBytes(instance, applicationID string) ([]byte, error) {
	if c == nil || instance == "" || applicationID == "" || len(c.key) != ed25519.PrivateKeySize || len(c.sourceKey) != 32 {
		return nil, errors.New("invalid Teams credential")
	}
	r := credentialRecord{Version: 1, Origin: c.Origin, OrgID: c.OrgID, PersonID: c.PersonID, InstallationID: c.InstallationID, LibraryID: c.LibraryID, LibraryInstance: instance, ApplicationID: applicationID, PrivateKey: base64.RawURLEncoding.EncodeToString(c.key), SourceTokenKey: base64.RawURLEncoding.EncodeToString(c.sourceKey), MetricsDefault: c.MetricsDefault}
	return json.Marshal(r)
}

func ParseCredential(raw []byte, libraryID, instance, applicationID string, allowLoopbackHTTP bool) (*EnrollmentCredential, error) {
	if !validUniqueJSON(raw) {
		return nil, errors.New("invalid Teams credential")
	}
	var r credentialRecord
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || r.Version != 1 || r.LibraryID != libraryID || r.LibraryInstance != instance || r.ApplicationID != applicationID || instance == "" || applicationID == "" || !validDeviceID(r.OrgID) || !validDeviceID(r.PersonID) || !validDeviceID(r.InstallationID) || r.MetricsDefault.PolicyVersion < 1 {
		return nil, errors.New("Teams credential needs reconciliation")
	}
	if _, err := LibraryPath(r.LibraryID, "privacy"); err != nil {
		return nil, errors.New("invalid Teams credential")
	}
	key, err := base64.RawURLEncoding.Strict().DecodeString(r.PrivateKey)
	if err != nil || len(key) != ed25519.PrivateKeySize || base64.RawURLEncoding.EncodeToString(key) != r.PrivateKey {
		return nil, errors.New("invalid Teams credential")
	}
	derived := ed25519.NewKeyFromSeed(key[:ed25519.SeedSize])
	if subtle.ConstantTimeCompare(derived, key) != 1 {
		clear(key)
		clear(derived)
		return nil, errors.New("invalid Teams credential")
	}
	clear(derived)
	source, err := base64.RawURLEncoding.Strict().DecodeString(r.SourceTokenKey)
	if err != nil || len(source) != 32 || base64.RawURLEncoding.EncodeToString(source) != r.SourceTokenKey {
		clear(key)
		return nil, errors.New("invalid Teams credential")
	}
	checker, err := NewDeviceClient(r.Origin, r.InstallationID, key, allowLoopbackHTTP)
	if err != nil {
		clear(key)
		clear(source)
		return nil, errors.New("invalid Teams credential")
	}
	checker.Destroy()
	return &EnrollmentCredential{OrgID: r.OrgID, PersonID: r.PersonID, InstallationID: r.InstallationID, LibraryID: r.LibraryID, Origin: r.Origin, MetricsDefault: r.MetricsDefault, key: key, sourceKey: source}, nil
}
