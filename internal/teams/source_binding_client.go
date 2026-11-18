package teams

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

type SourceSealReceipt struct {
	SnapshotID        string `json:"snapshot_id"`
	InventoryRevision int64  `json:"inventory_revision"`
	InventorySHA256   string `json:"inventory_sha256"`
	LocalRevision     int64  `json:"local_revision"`
	OwnershipRevision int64  `json:"ownership_revision"`
	PrivacyRevision   int64  `json:"privacy_revision"`
}
type SourceBindRequest struct {
	BindingID          string `json:"binding_id"`
	SnapshotID         string `json:"snapshot_id"`
	InventoryRevision  int64  `json:"inventory_revision"`
	InventorySHA256    string `json:"inventory_sha256"`
	LocalRevision      int64  `json:"local_revision"`
	CatalogRevision    int64  `json:"catalog_revision"`
	EvaluationRevision int64  `json:"evaluation_revision"`
	ConfigSHA256       string `json:"config_sha256"`
	OwnershipRevision  int64  `json:"ownership_revision"`
	PrivacyRevision    int64  `json:"privacy_revision"`
}
type SourceBinding struct {
	SourceBindRequest
	AssignmentSHA256    string `json:"assignment_sha256"`
	AssignmentCount     int    `json:"assignment_count"`
	AssignmentPageCount int    `json:"assignment_page_count"`
	BindingState        string `json:"binding_state"`
	ProjectionState     string `json:"projection_state"`
}
type sourceAssignmentPage struct {
	BindingID         string                  `json:"binding_id"`
	SnapshotID        string                  `json:"snapshot_id"`
	InventorySHA256   string                  `json:"inventory_sha256"`
	OwnershipRevision int64                   `json:"ownership_revision"`
	PrivacyRevision   int64                   `json:"privacy_revision"`
	AssignmentSHA256  string                  `json:"assignment_sha256"`
	Index             int                     `json:"index"`
	PageCount         int                     `json:"page_count"`
	Items             []sourcewire.Assignment `json:"items"`
}

var errSourceBinding = errors.New("invalid Teams source binding")

type SourceWatermark struct {
	LocalRevision             int64  `json:"local_revision"`
	ReservedInventoryRevision int64  `json:"reserved_inventory_revision"`
	ReservedInventorySHA256   string `json:"reserved_inventory_sha256"`
	AcceptedInventoryRevision int64  `json:"accepted_inventory_revision"`
	AcceptedInventorySHA256   string `json:"accepted_inventory_sha256"`
}

type SourceAuthorization struct {
	ScheduledAssignment *ScheduledAssignment `json:"scheduled_assignment,omitempty"`
	SnapshotID          string               `json:"snapshot_id"`
	InventoryRevision   int64                `json:"inventory_revision"`
	InventorySHA256     string               `json:"inventory_sha256"`
	LocalRevision       int64                `json:"local_revision"`
	EvaluationRevision  int64                `json:"evaluation_revision"`
	ConfigSHA256        string               `json:"config_sha256"`
	Mode                string               `json:"mode"`
	DecisionID          string               `json:"decision_id"`
	DecidedAt           time.Time            `json:"decided_at"`
	SettingsRevision    int64                `json:"settings_revision"`
	OrgPolicyVersion    int64                `json:"org_policy_version"`
}
type SourceAuthorizationReceipt struct {
	SnapshotID          string    `json:"snapshot_id"`
	AuthorizationSHA256 string    `json:"authorization_sha256"`
	AcceptedAt          time.Time `json:"accepted_at"`
	ExpiresAt           time.Time `json:"expires_at"`
}

// DecodeSourceAuthorization validates the same exact persisted bytes on initial
// disclosure and when an interrupted upload is resumed.
func DecodeSourceAuthorization(workID string, raw []byte) (SourceAuthorization, error) {
	var auth SourceAuthorization
	if !validDeviceID(workID) || !validUniqueJSON(raw) {
		return SourceAuthorization{}, errSourceBinding
	}
	keys := []string{"snapshot_id", "inventory_revision", "inventory_sha256", "local_revision", "evaluation_revision", "config_sha256", "mode", "decision_id", "decided_at", "settings_revision", "org_policy_version"}
	fields, err := pollObject(raw)
	if err != nil {
		return SourceAuthorization{}, errSourceBinding
	}
	if assignment, present := fields["scheduled_assignment"]; present {
		parsed, err := decodeScheduledAssignment(assignment)
		if err != nil || parsed.AssignmentID != workID {
			return SourceAuthorization{}, errSourceBinding
		}
		keys = append(keys, "scheduled_assignment")
	}
	if sourceResponse(raw, &auth, keys...) != nil || !validDeviceID(auth.SnapshotID) || auth.InventoryRevision < 1 || !sourceDigest(auth.InventorySHA256) || auth.LocalRevision < 1 || auth.EvaluationRevision < 1 || !sourceDigest(auth.ConfigSHA256) || auth.DecidedAt.IsZero() || auth.SettingsRevision < 1 {
		return SourceAuthorization{}, errSourceBinding
	}
	if auth.ScheduledAssignment != nil && auth.Mode == "standing" {
		return SourceAuthorization{}, errSourceBinding
	}
	switch auth.Mode {
	case "reviewed", "standing":
		if !validDeviceID(auth.DecisionID) || auth.OrgPolicyVersion != 0 {
			return SourceAuthorization{}, errSourceBinding
		}
	case "automatic_metrics":
		if auth.DecisionID != "" || auth.OrgPolicyVersion < 1 {
			return SourceAuthorization{}, errSourceBinding
		}
	default:
		return SourceAuthorization{}, errSourceBinding
	}
	return auth, nil
}

// AuthorizeSourceDisclosure grants only operational source metadata for this
// exact work and snapshot. It never grants content access or result delivery.
func (c *DeviceClient) AuthorizeSourceDisclosure(ctx context.Context, workID string, raw []byte) (SourceAuthorizationReceipt, error) {
	var out SourceAuthorizationReceipt
	auth, err := DecodeSourceAuthorization(workID, raw)
	if err != nil {
		return out, err
	}
	raw = bytes.Clone(raw)
	var response json.RawMessage
	if err := c.exchange(ctx, http.MethodPost, "/v1/device/work/"+workID+"/source-authorizations", raw, &response); err != nil {
		return out, err
	}
	if sourceResponse(response, &out, "snapshot_id", "authorization_sha256", "accepted_at", "expires_at") != nil || out.SnapshotID != auth.SnapshotID || out.AuthorizationSHA256 != hashBytes(raw) || out.AcceptedAt.IsZero() || !out.ExpiresAt.After(out.AcceptedAt) {
		return SourceAuthorizationReceipt{}, errSourceBinding
	}
	return out, nil
}

func (c *DeviceClient) SourceWatermark(ctx context.Context, libraryID string) (SourceWatermark, error) {
	var out SourceWatermark
	path, err := LibraryPath(libraryID, "source-watermark")
	if err != nil {
		return out, err
	}
	var raw json.RawMessage
	if err = c.exchange(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return out, err
	}
	if sourceResponse(raw, &out, "local_revision", "reserved_inventory_revision", "reserved_inventory_sha256", "accepted_inventory_revision", "accepted_inventory_sha256") != nil || out.LocalRevision < 0 || out.ReservedInventoryRevision < 0 || out.AcceptedInventoryRevision < 0 || out.AcceptedInventoryRevision > out.ReservedInventoryRevision {
		return SourceWatermark{}, errSourceBinding
	}
	validPair := func(rev int64, digest string) bool {
		return rev == 0 && digest == "" || rev > 0 && sourceDigest(digest)
	}
	if !validPair(out.ReservedInventoryRevision, out.ReservedInventorySHA256) || !validPair(out.AcceptedInventoryRevision, out.AcceptedInventorySHA256) || (out.AcceptedInventoryRevision > 0 && out.AcceptedInventoryRevision == out.ReservedInventoryRevision && out.AcceptedInventorySHA256 != out.ReservedInventorySHA256) {
		return SourceWatermark{}, errSourceBinding
	}
	return out, nil
}

func sourceDigest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func sourceResponse(raw []byte, out any, keys ...string) error {
	if !validUniqueJSON(raw) {
		return errSourceBinding
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != len(keys) {
		return errSourceBinding
	}
	for _, key := range keys {
		value, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errSourceBinding
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return errSourceBinding
	}
	return nil
}
func sourceAssignmentItemsExact(raw []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	var items []json.RawMessage
	if json.Unmarshal(fields["items"], &items) != nil || items == nil {
		return false
	}
	for _, item := range items {
		var value sourcewire.Assignment
		if sourceResponse(item, &value, "token", "decision") != nil {
			return false
		}
	}
	return true
}

// SealSourceSnapshot is a published inventory receipt, not a work assignment.
// The caller holds the sharing gate and has persisted the exposure reservation.
func (c *DeviceClient) SealSourceSnapshot(ctx context.Context, workID string, raw []byte) (SourceSealReceipt, error) {
	var out SourceSealReceipt
	if !validDeviceID(workID) {
		return out, errSourceBinding
	}
	raw = bytes.Clone(raw)
	seal, err := sourcewire.DecodeSeal(raw)
	if err != nil {
		return out, err
	}
	var response json.RawMessage
	path := fmt.Sprintf("/v1/device/work/%s/source-snapshots/%s/seal", workID, seal.SnapshotID)
	if err = c.exchange(ctx, http.MethodPost, path, raw, &response); err != nil {
		return out, err
	}
	if sourceResponse(response, &out, "snapshot_id", "inventory_revision", "inventory_sha256", "local_revision", "ownership_revision", "privacy_revision") != nil || out.SnapshotID != seal.SnapshotID || out.InventoryRevision != seal.InventoryRevision || out.InventorySHA256 != seal.InventorySHA256 || out.LocalRevision != seal.LocalRevision || out.OwnershipRevision < 0 || out.PrivacyRevision < 0 {
		return SourceSealReceipt{}, errSourceBinding
	}
	return out, nil
}

func (b SourceBindRequest) valid() bool {
	return validDeviceID(b.BindingID) && validDeviceID(b.SnapshotID) && b.InventoryRevision > 0 && sourceDigest(b.InventorySHA256) && b.LocalRevision > 0 && b.CatalogRevision >= b.LocalRevision && b.EvaluationRevision > 0 && sourceDigest(b.ConfigSHA256) && b.OwnershipRevision >= 0 && b.PrivacyRevision >= 0
}

func (c *DeviceClient) BindSourceInventory(ctx context.Context, workID string, request SourceBindRequest) (SourceBinding, error) {
	var out SourceBinding
	if !validDeviceID(workID) || !request.valid() {
		return out, errSourceBinding
	}
	body, err := json.Marshal(request)
	if err != nil {
		return out, errSourceBinding
	}
	var response json.RawMessage
	if err = c.exchange(ctx, http.MethodPost, "/v1/device/work/"+workID+"/source-bindings", body, &response); err != nil {
		return out, err
	}
	keys := []string{"binding_id", "snapshot_id", "inventory_revision", "inventory_sha256", "local_revision", "catalog_revision", "evaluation_revision", "config_sha256", "ownership_revision", "privacy_revision", "assignment_sha256", "assignment_count", "assignment_page_count", "binding_state", "projection_state"}
	if sourceResponse(response, &out, keys...) != nil || out.SourceBindRequest != request || !sourceDigest(out.AssignmentSHA256) || out.AssignmentCount < 0 || out.AssignmentCount > sourcewire.MaxPages*sourcewire.MaxItemsPerPage || out.AssignmentPageCount != max(1, (out.AssignmentCount+sourcewire.MaxAssignmentsPerPage-1)/sourcewire.MaxAssignmentsPerPage) || (out.BindingState != "ready" && out.BindingState != "awaiting_barrier") || (out.ProjectionState != "current" && out.ProjectionState != "staged") {
		return SourceBinding{}, errSourceBinding
	}
	return out, nil
}

// SourceAssignments validates every page and the page-independent hash before
// returning any decision. Retained prior tokens absent from this inventory may
// appear only as unavailable and never become locally eligible.
func (c *DeviceClient) SourceAssignments(ctx context.Context, workID string, b SourceBinding, expectedTokens []string) ([]sourcewire.Assignment, error) {
	if !validDeviceID(workID) || !b.SourceBindRequest.valid() || b.BindingState != "ready" || (b.ProjectionState != "current" && b.ProjectionState != "staged") || !sourceDigest(b.AssignmentSHA256) || b.AssignmentCount < 0 || b.AssignmentCount > sourcewire.MaxPages*sourcewire.MaxItemsPerPage || b.AssignmentPageCount != max(1, (b.AssignmentCount+sourcewire.MaxAssignmentsPerPage-1)/sourcewire.MaxAssignmentsPerPage) || expectedTokens == nil {
		return nil, errSourceBinding
	}
	expected := map[string]bool{}
	for _, token := range expectedTokens {
		if expected[token] {
			return nil, errSourceBinding
		}
		expected[token] = true
	}
	h, err := sourcewire.NewAssignmentHasher(sourcewire.AssignmentHeader{BindingID: b.BindingID, SnapshotID: b.SnapshotID, InventorySHA256: b.InventorySHA256, OwnershipRevision: b.OwnershipRevision, PrivacyRevision: b.PrivacyRevision})
	if err != nil {
		return nil, err
	}
	all := make([]sourcewire.Assignment, 0, b.AssignmentCount)
	for i := 0; i < b.AssignmentPageCount; i++ {
		path := fmt.Sprintf("/v1/device/work/%s/source-snapshots/%s/bindings/%s/assignments/%d", workID, b.SnapshotID, b.BindingID, i)
		var raw json.RawMessage
		if err = c.exchange(ctx, http.MethodGet, path, nil, &raw); err != nil {
			return nil, err
		}
		var page sourceAssignmentPage
		if sourceResponse(raw, &page, "binding_id", "snapshot_id", "inventory_sha256", "ownership_revision", "privacy_revision", "assignment_sha256", "index", "page_count", "items") != nil || !sourceAssignmentItemsExact(raw) || page.BindingID != b.BindingID || page.SnapshotID != b.SnapshotID || page.InventorySHA256 != b.InventorySHA256 || page.OwnershipRevision != b.OwnershipRevision || page.PrivacyRevision != b.PrivacyRevision || page.AssignmentSHA256 != b.AssignmentSHA256 || page.Index != i || page.PageCount != b.AssignmentPageCount || page.Items == nil || len(page.Items) > sourcewire.MaxAssignmentsPerPage {
			return nil, errSourceBinding
		}
		for _, item := range page.Items {
			if err = h.Add(item); err != nil {
				return nil, errSourceBinding
			}
			if _, ok := expected[item.Token]; !ok && item.Decision != "unavailable" {
				return nil, errSourceBinding
			}
			delete(expected, item.Token)
			all = append(all, item)
		}
	}
	digest, count, err := h.Finish()
	if err != nil || digest != b.AssignmentSHA256 || count != b.AssignmentCount || len(expected) != 0 {
		return nil, errSourceBinding
	}
	return all, nil
}
