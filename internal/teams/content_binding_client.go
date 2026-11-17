package teams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"
)

type ContentBindRequest struct {
	BindingID          string            `json:"binding_id"`
	SnapshotID         string            `json:"snapshot_id"`
	InventoryRevision  int64             `json:"inventory_revision"`
	InventorySHA256    string            `json:"inventory_sha256"`
	LocalRevision      int64             `json:"local_revision"`
	CatalogRevision    int64             `json:"catalog_revision"`
	EvaluationRevision int64             `json:"evaluation_revision"`
	ConfigSHA256       string            `json:"config_sha256"`
	PrivacyRevision    int64             `json:"privacy_revision"`
	OwnershipRevision  int64             `json:"ownership_revision"`
	DecisionID         string            `json:"decision_id"`
	Processing         ContentProcessing `json:"processing"`
	Components         []string          `json:"components"`
}
type contentBindReceipt struct {
	BindingID      string `json:"binding_id"`
	BindingSHA256  string `json:"binding_sha256"`
	ComponentCount int    `json:"component_count"`
}
type contentBindingPage struct {
	BindingID     string            `json:"binding_id"`
	BindingSHA256 string            `json:"binding_sha256"`
	Index         int               `json:"index"`
	PageCount     int               `json:"page_count"`
	Items         []json.RawMessage `json:"items"`
}

// BindContent verifies each signed page and the complete server digest against
// the independently selected local closure. It only establishes a snapshot;
// disclosure requires a separate privacy ordering barrier and local consent.
func (c *DeviceClient) BindContent(ctx context.Context, workID, libraryID string, request ContentBindRequest, expected []ContentEvidence) (ContentBinding, error) {
	var empty ContentBinding
	invalid := errors.New("invalid Teams content binding")
	if !validDeviceID(workID) || !validDeviceID(request.BindingID) || !validDeviceID(request.SnapshotID) || !validDeviceID(request.DecisionID) || libraryID == "" || request.InventoryRevision < 1 || request.LocalRevision < 1 || request.CatalogRevision < request.LocalRevision || request.EvaluationRevision < 1 || request.PrivacyRevision < 0 || request.OwnershipRevision < 0 || !sourceDigest(request.InventorySHA256) || !sourceDigest(request.ConfigSHA256) || request.Processing.Execution != "local" || request.Processing.Provider == "" || request.Processing.Model == "" || len(request.Components) == 0 || len(request.Components) > 10000 || !sort.StringsAreSorted(request.Components) {
		return empty, invalid
	}
	for i, v := range request.Components {
		if v == "" || (i > 0 && request.Components[i-1] == v) {
			return empty, invalid
		}
	}
	raw, e := json.Marshal(request)
	if e != nil {
		return empty, e
	}
	var response json.RawMessage
	base := "/v1/device/work/" + workID + "/content-bindings"
	if e = c.exchange(ctx, http.MethodPost, base, raw, &response); e != nil {
		return empty, e
	}
	var receipt contentBindReceipt
	if sourceResponse(response, &receipt, "binding_id", "binding_sha256", "component_count") != nil || receipt.BindingID != request.BindingID || !sourceDigest(receipt.BindingSHA256) || receipt.ComponentCount < 1 || receipt.ComponentCount > 10000 || receipt.ComponentCount != len(expected) {
		return empty, invalid
	}
	pages := (receipt.ComponentCount + 199) / 200
	components := make([]ContentEvidence, 0, receipt.ComponentCount)
	for i := 0; i < pages; i++ {
		response = nil
		if e = c.exchange(ctx, http.MethodGet, fmt.Sprintf("%s/%s/pages/%d", base, request.BindingID, i), nil, &response); e != nil {
			return empty, e
		}
		var page contentBindingPage
		if sourceResponse(response, &page, "binding_id", "binding_sha256", "index", "page_count", "items") != nil || page.BindingID != request.BindingID || page.BindingSHA256 != receipt.BindingSHA256 || page.Index != i || page.PageCount != pages || page.Items == nil || len(page.Items) > 200 {
			return empty, invalid
		}
		for _, item := range page.Items {
			var v ContentEvidence
			if sourceResponse(item, &v, "token", "kind", "profile_id", "profile_version", "evidence_revision", "protection", "dependencies", "effective_protected", "selected") != nil {
				return empty, invalid
			}
			components = append(components, v)
		}
	}
	binding := ContentBinding{WorkID: workID, LibraryID: libraryID, InstallationID: c.installation, SnapshotID: request.SnapshotID, InventorySHA256: request.InventorySHA256, ConfigSHA256: request.ConfigSHA256, Processing: request.Processing, DecisionID: request.DecisionID, InventoryRevision: request.InventoryRevision, CatalogRevision: request.CatalogRevision, EvaluationRevision: request.EvaluationRevision, LocalRevision: request.LocalRevision, PrivacyRevision: request.PrivacyRevision, OwnershipRevision: request.OwnershipRevision, Components: components, SHA256: receipt.BindingSHA256}
	if len(components) != receipt.ComponentCount || VerifyContentBinding(binding, expected, receipt.BindingSHA256) != nil {
		return empty, invalid
	}
	return binding, nil
}

type ContentStartRequest struct {
	StartID       string `json:"start_id"`
	BindingID     string `json:"binding_id"`
	BindingSHA256 string `json:"binding_sha256"`
}
type ContentStartReceipt struct {
	StartID   string    `json:"start_id"`
	BindingID string    `json:"binding_id"`
	StartedAt time.Time `json:"started_at"`
}

// StartContentDisclosure is the server's one-use ordering point. Callers must
// durably consume it before handoff and never repeat a model invocation with
// this start after an ambiguous response or process restart.
func (c *DeviceClient) StartContentDisclosure(ctx context.Context, workID string, request ContentStartRequest) (ContentStartReceipt, error) {
	var out ContentStartReceipt
	if !validDeviceID(workID) || !validDeviceID(request.StartID) || !validDeviceID(request.BindingID) || !sourceDigest(request.BindingSHA256) {
		return out, errors.New("invalid Teams disclosure start")
	}
	raw, e := json.Marshal(request)
	if e != nil {
		return out, e
	}
	var response json.RawMessage
	if e = c.exchange(ctx, http.MethodPost, "/v1/device/work/"+workID+"/content-start", raw, &response); e != nil {
		return out, e
	}
	if sourceResponse(response, &out, "start_id", "binding_id", "started_at") != nil || out.StartID != request.StartID || out.BindingID != request.BindingID || out.StartedAt.IsZero() {
		return ContentStartReceipt{}, errors.New("invalid Teams disclosure receipt")
	}
	return out, nil
}

type ArtifactSubmission struct {
	IdempotencyKey       string          `json:"idempotency_key"`
	ContentBindingID     string          `json:"content_binding_id"`
	ContentStartID       string          `json:"content_start_id"`
	SourceDecisionID     string          `json:"source_decision_id"`
	ContentBindingSHA256 string          `json:"content_binding_sha256"`
	ConfigSHA256         string          `json:"config_sha256"`
	EvaluationRevision   int64           `json:"evaluation_revision"`
	OwnershipRevision    int64           `json:"ownership_revision"`
	Approval             Approval        `json:"approval"`
	PayloadSHA256        string          `json:"payload_sha256"`
	Payload              json.RawMessage `json:"payload"`
}

// SubmitArtifact transmits only the exact already-reviewed outbox bytes.
func (c *DeviceClient) SubmitArtifact(ctx context.Context, workID string, body []byte, payloadSHA string) (Receipt, error) {
	var out Receipt
	var submission ArtifactSubmission
	if !validDeviceID(workID) || !validUniqueJSON(body) || json.Unmarshal(body, &submission) != nil || submission.PayloadSHA256 != payloadSHA || hashBytes(submission.Payload) != payloadSHA || submission.Approval.Mode != "reviewed" || submission.Approval.DecisionID == "" || submission.SourceDecisionID == submission.Approval.DecisionID || !validDeviceID(submission.ContentBindingID) || !validDeviceID(submission.ContentStartID) {
		return out, errors.New("invalid staged Teams artifact")
	}
	if err := c.exchange(ctx, http.MethodPut, "/v1/device/work/"+workID+"/artifact-result", body, &out); err != nil {
		return out, err
	}
	if out.WorkID != workID || out.PayloadSHA256 != payloadSHA || out.SubmissionSHA256 != hashBytes(body) || out.IdempotencyKey != submission.IdempotencyKey || out.InstallationID != c.installation {
		return Receipt{}, ErrReceiptMismatch
	}
	return out, nil
}
