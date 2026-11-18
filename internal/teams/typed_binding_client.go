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

var (
	ErrTypedBinding            = errors.New("invalid Teams typed binding response")
	ErrTypedBindingStale       = errors.New("Teams typed binding is stale")
	ErrTypedBindingExpired     = errors.New("Teams typed binding expired")
	ErrTypedBindingUnsupported = errors.New("unsupported Teams typed binding profile")
)

// TypedBindingSuperseded carries the server's floor for the next issuance.
type TypedBindingSuperseded struct{ Floor TypedBindingFloor }

func (*TypedBindingSuperseded) Error() string { return "Teams typed binding superseded" }

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
		return out, ErrTypedBinding
	}
	var raw json.RawMessage
	if err := c.exchange(ctx, http.MethodGet, "/v1/device/work/"+workID+"/typed-source-bindings/floor", nil, &raw); err != nil {
		return out, err
	}
	if len(raw) > 4096 || !exactTypedJSON(raw, &out) || !validTypedFloor(&out) {
		return TypedBindingFloor{}, ErrTypedBinding
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
		return out, ErrTypedBinding
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
		return TypedBindingReceipt{}, ErrTypedBinding
	}
	if out.Status == "superseded" {
		if out.Binding != nil || out.BindingSHA256 != "" || out.EligibilityGeneration != nil || out.ValidUntil != nil || !validTypedFloor(out.Floor) || out.Floor.NextIssuedAt <= request.IssuedAt {
			return TypedBindingReceipt{}, ErrTypedBinding
		}
		return out, nil
	}
	if (out.Status != "issued" && out.Status != "stale") || out.Floor != nil || out.Binding == nil || out.EligibilityGeneration == nil || *out.EligibilityGeneration < 0 || out.ValidUntil == nil || !out.ValidUntil.After(time.Unix(request.IssuedAt, 0).Add(-sourcewire.RecoveryFutureSkew)) {
		return TypedBindingReceipt{}, ErrTypedBinding
	}
	b := out.Binding
	_, digest, err := b.Bytes()
	if err != nil || digest != out.BindingSHA256 || b.BindingID != request.BindingID || b.WorkID != workID || b.InstallationID != c.installation || b.LibraryID != libraryID || b.SnapshotID != request.SnapshotID || b.InventorySHA256 != request.InventorySHA256 || b.ConfigSHA256 != request.ConfigSHA256 || b.WindowSHA256 != windowSHA256 || b.LocalRevision != request.LocalRevision || b.CatalogRevision != request.CatalogRevision || b.EvaluationRevision != request.EvaluationRevision || b.OwnershipRevision != request.OwnershipRevision || b.PrivacyRevision != request.PrivacyRevision {
		return TypedBindingReceipt{}, ErrTypedBinding
	}
	return out, nil
}

// TypedAssignments returns a complete verified profile chain, never a prefix.
func (c *DeviceClient) TypedAssignments(ctx context.Context, workID string, receipt TypedBindingReceipt, profileID string, profileVersion int) ([]sourcewire.TypedAssignmentPage, error) {
	return c.typedAssignments(ctx, workID, receipt, profileID, profileVersion, 8<<20)
}

func (c *DeviceClient) typedAssignments(ctx context.Context, workID string, receipt TypedBindingReceipt, profileID string, profileVersion int, maxBytes int) ([]sourcewire.TypedAssignmentPage, error) {
	b := receipt.Binding
	if !validDeviceID(workID) || receipt.Status != "issued" || b == nil || b.WorkID != workID || b.InstallationID != c.installation || receipt.EligibilityGeneration == nil || *receipt.EligibilityGeneration < 0 || receipt.ValidUntil == nil || receipt.ValidUntil.IsZero() || receipt.Floor != nil {
		return nil, ErrTypedBinding
	}
	_, digest, err := b.Bytes()
	if err != nil || digest != receipt.BindingSHA256 {
		return nil, ErrTypedBinding
	}
	var ref *sourcewire.ProfileReference
	for i := range b.Profiles {
		if b.Profiles[i].ProfileID == profileID && b.Profiles[i].ProfileVersion == profileVersion {
			ref = &b.Profiles[i]
			break
		}
	}
	if ref == nil {
		return nil, ErrTypedBinding
	}
	pages := make([]sourcewire.TypedAssignmentPage, 0, ref.AssignmentPageCount)
	totalBytes := 0
	for i := 0; i < ref.AssignmentPageCount; i++ {
		path := fmt.Sprintf("/v1/device/work/%s/typed-source-bindings/%s/profiles/%s/%d/assignments/%d", workID, b.BindingID, profileID, profileVersion, i)
		var raw json.RawMessage
		if err := c.exchange(ctx, http.MethodGet, path, nil, &raw); err != nil {
			return nil, err
		}
		totalBytes += len(raw)
		if totalBytes > maxBytes {
			return nil, ErrTypedBinding
		}
		var page sourcewire.TypedAssignmentPage
		if len(raw) > sourcewire.MaxPageBytes || !exactTypedJSON(raw, &page) || page.Validate() != nil {
			return nil, ErrTypedBinding
		}
		pages = append(pages, page)
	}
	if sourcewire.VerifyTypedAssignmentPages(digest, *ref, pages) != nil {
		return nil, ErrTypedBinding
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

// Preserve the server's status and code for diagnostics while exposing the
// same dispositions for a replay response and a later assignment read.
func typedBindingReadError(err error) error {
	var remote *HTTPError
	if errors.As(err, &remote) && remote.Status == http.StatusConflict {
		switch remote.Code {
		case "stale_source_binding":
			return fmt.Errorf("%w: %w", ErrTypedBindingStale, err)
		case "source_issuance_expired":
			return fmt.Errorf("%w: %w", ErrTypedBindingExpired, err)
		}
	}
	return err
}

// IssueVerifiedTypedBinding verifies the current service's codex_request_usage
// v1 assignment chain. Persist the request before calling. Reuse its exact bytes
// after transport failure. A superseded issuance carries a floor for a new ID;
// a stale or expired issuance requires fresh work evaluation.
func (c *DeviceClient) IssueVerifiedTypedBinding(ctx context.Context, orgID, personID, libraryID, workID, windowSHA256 string, request sourcewire.TypedBindingRequest) (VerifiedTypedBinding, error) {
	var zero VerifiedTypedBinding
	if c == nil {
		return zero, ErrTypedBinding
	}
	// The floor is for allocating a new identity. Exact replays must reach
	// the service even after accepting that identity advances the floor.
	receipt, err := c.IssueTypedBinding(ctx, orgID, personID, libraryID, workID, windowSHA256, request)
	if err != nil {
		return zero, typedBindingReadError(err)
	}
	switch receipt.Status {
	case "superseded":
		return zero, &TypedBindingSuperseded{Floor: *receipt.Floor}
	case "stale":
		return zero, ErrTypedBindingStale
	}
	if !time.Now().Before(*receipt.ValidUntil) {
		return zero, ErrTypedBindingExpired
	}
	// The service's issuer supports exactly this profile. The wire validation
	// bounds its chain to MaxPages and commits each page's canonical bytes.
	profiles := receipt.Binding.Profiles
	if len(profiles) != 1 || profiles[0].ProfileID != "codex_request_usage" || profiles[0].ProfileVersion != 1 {
		return zero, ErrTypedBindingUnsupported
	}
	fetchCtx, cancel := context.WithDeadline(ctx, *receipt.ValidUntil)
	defer cancel()
	profile := profiles[0]
	chain, err := c.typedAssignments(fetchCtx, workID, receipt, profile.ProfileID, profile.ProfileVersion, profile.AssignmentPageCount*sourcewire.MaxPageBytes)
	if !time.Now().Before(*receipt.ValidUntil) {
		return zero, ErrTypedBindingExpired
	}
	if err != nil {
		return zero, typedBindingReadError(err)
	}
	pages := [][]sourcewire.TypedAssignmentPage{chain}
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
