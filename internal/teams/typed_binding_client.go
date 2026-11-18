package teams

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

var errTypedBinding = errors.New("invalid Teams typed binding response")

// TypedBindingFloor is the service's issuance floor for the exact work and device.
type TypedBindingFloor struct {
	ServerTime   time.Time  `json:"server_time"`
	NextIssuedAt int64      `json:"next_issued_at"`
	RetryAt      *time.Time `json:"retry_at"`
}

// TypedBindingReceipt retains the service's issuance fence. Only an issued
// receipt can be used to read assignments; stale and superseded are outcomes.
// The service has no binding GET: recover an issuance by replaying the exact
// POST request. Typed routes may be absent on older or disabled deployments.
type TypedBindingReceipt struct {
	Status                string                           `json:"status"`
	Binding               *sourcewire.TypedBindingEnvelope `json:"binding,omitempty"`
	BindingSHA256         string                           `json:"binding_sha256,omitempty"`
	EligibilityGeneration *int64                           `json:"eligibility_generation,omitempty"`
	ValidUntil            *time.Time                       `json:"valid_until,omitempty"`
	Floor                 *TypedBindingFloor               `json:"floor,omitempty"`
}

// exactTypedJSON checks required nested fields as well as unknown and duplicate
// fields, including zero-valued fields that json.Unmarshal would otherwise hide.
func exactTypedJSON(raw []byte, out any) bool {
	if !validUniqueJSON(raw) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return false
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return false
	}
	var a, b any
	if json.Unmarshal(raw, &a) != nil || json.Unmarshal(encoded, &b) != nil {
		return false
	}
	var shape func(any, any) bool
	shape = func(x, y any) bool {
		switch v := y.(type) {
		case map[string]any:
			m, ok := x.(map[string]any)
			if !ok || len(m) != len(v) {
				return false
			}
			for k, child := range v {
				actual, ok := m[k]
				if !ok || !shape(actual, child) {
					return false
				}
			}
			return true
		case []any:
			a, ok := x.([]any)
			if !ok || len(a) != len(v) {
				return false
			}
			for i := range v {
				if !shape(a[i], v[i]) {
					return false
				}
			}
			return true
		default:
			return (x == nil) == (y == nil)
		}
	}
	return shape(a, b)
}
func validTypedFloor(f *TypedBindingFloor) bool {
	if f == nil || f.ServerTime.IsZero() || f.NextIssuedAt <= 0 || f.NextIssuedAt > 253402300799 || f.NextIssuedAt < f.ServerTime.Unix() {
		return false
	}
	earliest := time.Unix(f.NextIssuedAt, 0).Add(-sourcewire.RecoveryFutureSkew)
	if earliest.After(f.ServerTime) {
		return f.RetryAt != nil && f.RetryAt.Equal(earliest)
	}
	return f.RetryAt == nil
}

func (c *DeviceClient) TypedBindingFloor(ctx context.Context, workID string) (TypedBindingFloor, error) {
	var out TypedBindingFloor
	if !validDeviceID(workID) {
		return out, errTypedBinding
	}
	var raw json.RawMessage
	if err := c.exchange(ctx, http.MethodGet, "/v1/device/work/"+workID+"/typed-source-bindings/floor", nil, &raw); err != nil {
		return out, err
	}
	if len(raw) > 4096 || !exactTypedJSON(raw, &out) || !validTypedFloor(&out) {
		return TypedBindingFloor{}, errTypedBinding
	}
	return out, nil
}

// IssueTypedBinding sends the exact issued work identity and deterministic
// request ID. windowSHA256 is the independently expected work window digest.
func (c *DeviceClient) IssueTypedBinding(ctx context.Context, orgID, personID, libraryID, workID, windowSHA256 string, request sourcewire.TypedBindingRequest) (TypedBindingReceipt, error) {
	var out TypedBindingReceipt
	id, err := sourcewire.TypedBindingID(orgID, personID, c.installation, libraryID, workID, request)
	const maxSafeInteger = 1<<53 - 1
	if err != nil || id != request.BindingID || request.CatalogRevision != request.LocalRevision || !sourceDigest(windowSHA256) || request.InventoryRevision > maxSafeInteger || request.LocalRevision > maxSafeInteger || request.EvaluationRevision > maxSafeInteger || request.OwnershipRevision > maxSafeInteger || request.PrivacyRevision > maxSafeInteger {
		return out, errTypedBinding
	}
	body, err := json.Marshal(request)
	if err != nil {
		return out, err
	}
	var raw json.RawMessage
	if err = c.exchange(ctx, http.MethodPost, "/v1/device/work/"+workID+"/typed-source-bindings", body, &raw); err != nil {
		return out, err
	}
	if len(raw) > 32768 || !exactTypedJSON(raw, &out) {
		return TypedBindingReceipt{}, errTypedBinding
	}
	if out.Status == "superseded" {
		if out.Binding != nil || out.BindingSHA256 != "" || out.EligibilityGeneration != nil || out.ValidUntil != nil || !validTypedFloor(out.Floor) || out.Floor.NextIssuedAt <= request.IssuedAt {
			return TypedBindingReceipt{}, errTypedBinding
		}
		return out, nil
	}
	if (out.Status != "issued" && out.Status != "stale") || out.Floor != nil || out.Binding == nil || out.EligibilityGeneration == nil || *out.EligibilityGeneration < 0 || out.ValidUntil == nil || !out.ValidUntil.After(time.Unix(request.IssuedAt, 0).Add(-sourcewire.RecoveryFutureSkew)) {
		return TypedBindingReceipt{}, errTypedBinding
	}
	b := out.Binding
	_, digest, err := b.Bytes()
	if err != nil || digest != out.BindingSHA256 || b.BindingID != request.BindingID || b.WorkID != workID || b.InstallationID != c.installation || b.LibraryID != libraryID || b.SnapshotID != request.SnapshotID || b.InventorySHA256 != request.InventorySHA256 || b.ConfigSHA256 != request.ConfigSHA256 || b.WindowSHA256 != windowSHA256 || b.LocalRevision != request.LocalRevision || b.CatalogRevision != request.CatalogRevision || b.EvaluationRevision != request.EvaluationRevision || b.OwnershipRevision != request.OwnershipRevision || b.PrivacyRevision != request.PrivacyRevision {
		return TypedBindingReceipt{}, errTypedBinding
	}
	return out, nil
}

// TypedAssignments returns a complete verified profile chain, never a prefix.
func (c *DeviceClient) TypedAssignments(ctx context.Context, workID string, receipt TypedBindingReceipt, profileID string, profileVersion int) ([]sourcewire.TypedAssignmentPage, error) {
	totalBytes := 0
	return c.typedAssignments(ctx, workID, receipt, profileID, profileVersion, &totalBytes)
}

func (c *DeviceClient) typedAssignments(ctx context.Context, workID string, receipt TypedBindingReceipt, profileID string, profileVersion int, totalBytes *int) ([]sourcewire.TypedAssignmentPage, error) {
	b := receipt.Binding
	if !validDeviceID(workID) || receipt.Status != "issued" || b == nil || b.WorkID != workID || b.InstallationID != c.installation || receipt.EligibilityGeneration == nil || *receipt.EligibilityGeneration < 0 || receipt.ValidUntil == nil || receipt.ValidUntil.IsZero() || receipt.Floor != nil {
		return nil, errTypedBinding
	}
	_, digest, err := b.Bytes()
	if err != nil || digest != receipt.BindingSHA256 {
		return nil, errTypedBinding
	}
	var ref *sourcewire.ProfileReference
	for i := range b.Profiles {
		if b.Profiles[i].ProfileID == profileID && b.Profiles[i].ProfileVersion == profileVersion {
			ref = &b.Profiles[i]
			break
		}
	}
	if ref == nil {
		return nil, errTypedBinding
	}
	pages := make([]sourcewire.TypedAssignmentPage, 0, ref.AssignmentPageCount)
	for i := 0; i < ref.AssignmentPageCount; i++ {
		path := fmt.Sprintf("/v1/device/work/%s/typed-source-bindings/%s/profiles/%s/%d/assignments/%d", workID, b.BindingID, profileID, profileVersion, i)
		var raw json.RawMessage
		if err := c.exchange(ctx, http.MethodGet, path, nil, &raw); err != nil {
			return nil, err
		}
		*totalBytes += len(raw)
		if *totalBytes > 8<<20 {
			return nil, errTypedBinding
		}
		var page sourcewire.TypedAssignmentPage
		if len(raw) > sourcewire.MaxPageBytes || !exactTypedJSON(raw, &page) || page.Validate() != nil {
			return nil, errTypedBinding
		}
		pages = append(pages, page)
	}
	if sourcewire.VerifyTypedAssignmentPages(digest, *ref, pages) != nil {
		return nil, errTypedBinding
	}
	return pages, nil
}

// VerifiedTypedBinding is an issuance and all of its verified assignment chains.
// Its zero value is invalid. Its accessors return independent copies.
type VerifiedTypedBinding struct {
	request                                    []byte
	receipt                                    []byte
	pages                                      [][]sourcewire.TypedAssignmentPage
	origin, installation, org, person, library string
	publicKey                                  []byte
}

// Unexpired checks the locally retained receipt deadline. The server rechecks
// current privacy, ownership and eligibility before accepting a result.
func (v VerifiedTypedBinding) Unexpired(now time.Time) bool {
	if len(v.request) == 0 || len(v.receipt) == 0 || v.pages == nil {
		return false
	}
	var receipt TypedBindingReceipt
	return json.Unmarshal(v.receipt, &receipt) == nil && receipt.Status == "issued" && receipt.ValidUntil != nil && now.Before(*receipt.ValidUntil)
}

// MatchesCredential binds the verified response to the service and device that
// supplied it, as well as the scope used to derive the request identity.
func (v VerifiedTypedBinding) MatchesCredential(c *EnrollmentCredential) bool {
	return c != nil && credentialOriginMatches(v.origin, c.Origin) && v.installation == c.InstallationID && v.org == c.OrgID && v.person == c.PersonID && v.library == c.LibraryID && len(c.key) == ed25519.PrivateKeySize && bytes.Equal(v.publicKey, c.key.Public().(ed25519.PublicKey))
}

// RequestBytes returns the exact bytes signed for issuance.
func (v VerifiedTypedBinding) RequestBytes() []byte { return bytes.Clone(v.request) }

// Receipt returns a copy of the validated issuance, including its canonical binding hash.
func (v VerifiedTypedBinding) Receipt() TypedBindingReceipt {
	var receipt TypedBindingReceipt
	_ = json.Unmarshal(v.receipt, &receipt)
	return receipt
}

// Pages returns the verified chains in the order of the receipt's profiles.
func (v VerifiedTypedBinding) Pages() [][]sourcewire.TypedAssignmentPage {
	if v.pages == nil {
		return nil
	}
	raw, _ := json.Marshal(v.pages)
	var pages [][]sourcewire.TypedAssignmentPage
	_ = json.Unmarshal(raw, &pages)
	return pages
}

// IssueVerifiedTypedBinding obtains a fresh issuance and every committed profile
// chain from this device's signed endpoints. A stale, superseded, expired, or
// incomplete issuance never produces a verified result.
func (c *DeviceClient) IssueVerifiedTypedBinding(ctx context.Context, orgID, personID, libraryID, workID, windowSHA256 string, request sourcewire.TypedBindingRequest) (VerifiedTypedBinding, error) {
	var zero VerifiedTypedBinding
	if c == nil {
		return zero, errTypedBinding
	}
	// The floor is for allocating a new identity. Exact replays must reach
	// the service even after accepting that identity advances the floor.
	receipt, err := c.IssueTypedBinding(ctx, orgID, personID, libraryID, workID, windowSHA256, request)
	if err != nil {
		return zero, err
	}
	if receipt.Status != "issued" || receipt.ValidUntil == nil || !time.Now().Before(*receipt.ValidUntil) {
		return zero, errTypedBinding
	}
	pages := make([][]sourcewire.TypedAssignmentPage, 0, len(receipt.Binding.Profiles))
	totalBytes := 0
	for _, profile := range receipt.Binding.Profiles {
		chain, err := c.typedAssignments(ctx, workID, receipt, profile.ProfileID, profile.ProfileVersion, &totalBytes)
		if err != nil {
			return zero, err
		}
		pages = append(pages, chain)
	}
	if !time.Now().Before(*receipt.ValidUntil) {
		return zero, errTypedBinding
	}
	body, err := json.Marshal(request)
	if err != nil {
		return zero, err
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return zero, err
	}
	return VerifiedTypedBinding{request: body, receipt: raw, pages: pages, origin: c.origin.String(), installation: c.installation, org: orgID, person: personID, library: libraryID, publicKey: bytes.Clone(c.key.Public().(ed25519.PublicKey))}, nil
}
