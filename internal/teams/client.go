package teams

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

const maxDeviceBody = 2 << 20

var ErrReceiptMismatch = errors.New("Teams receipt does not match submitted request")
var capabilityVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

type HTTPError struct {
	Status int
	Code   string
}

func (e *HTTPError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("Teams request failed (%d: %s)", e.Status, e.Code)
	}
	return fmt.Sprintf("Teams request failed (%d)", e.Status)
}

// exchange never logs payloads or upstream messages. It bounds all response
// reads, refuses redirects in Request, and leaves retry policy to the caller.
func (c *DeviceClient) exchange(ctx context.Context, method, path string, body []byte, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	if len(body) > maxDeviceBody {
		return errors.New("Teams request exceeds size limit")
	}
	r, err := c.Request(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, maxDeviceBody+1))
	if err != nil {
		return err
	}
	if len(data) > maxDeviceBody {
		return errors.New("Teams response exceeds size limit")
	}
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		var e struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if len(e.Error.Code) > 64 {
			e.Error.Code = ""
		}
		for _, ch := range e.Error.Code {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '_') {
				e.Error.Code = ""
				break
			}
		}
		return &HTTPError{Status: r.StatusCode, Code: e.Error.Code}
	}
	if out == nil {
		return nil
	}
	if !validUniqueJSON(data) {
		return errors.New("invalid Teams response")
	}
	if err = json.Unmarshal(data, out); err != nil {
		return errors.New("invalid Teams response")
	}
	return nil
}

type WorkStatus struct {
	State      string `json:"state"`
	ReasonCode string `json:"reason_code"`
	AppVersion string `json:"app_version"`
}

func (c *DeviceClient) Status(ctx context.Context, workID string, status WorkStatus) error {
	path, err := WorkPath(workID, "status")
	if err != nil {
		return err
	}
	switch status.State {
	case "accepted", "awaiting_review", "declined", "needs_update", "failed", "unavailable":
	default:
		return errors.New("invalid work status")
	}
	if status.AppVersion == "" {
		return errors.New("app version is required")
	}
	body, err := marshalRequest(status)
	if err != nil {
		return err
	}
	return c.exchange(ctx, http.MethodPost, path, body, nil)
}
func (c *DeviceClient) Dirty(ctx context.Context, libraryID, versionID string, revision int64) error {
	path, err := LibraryPath(libraryID, "dirty")
	if err != nil {
		return err
	}
	if !validDeviceID(versionID) || revision < 1 {
		return errors.New("invalid dirty notice")
	}
	body, err := marshalRequest(struct {
		VersionID     string `json:"version_id"`
		LocalRevision int64  `json:"local_revision"`
	}{versionID, revision})
	if err != nil {
		return err
	}
	return c.exchange(ctx, http.MethodPost, path, body, nil)
}
func marshalRequest(value any) ([]byte, error) { return json.Marshal(value) }

// manifestVersion discovers support only on this signed connection. Unknown or
// old servers retain the legacy manifest until a new client session.
func (c *DeviceClient) manifestVersion(ctx context.Context) (int, error) {
	c.capabilityMu.Lock()
	defer c.capabilityMu.Unlock()
	if c.capabilityDiscovery != 0 {
		return c.capabilityDiscovery, nil
	}
	var response struct {
		Versions []int `json:"manifest_schema_versions"`
	}
	if err := c.exchange(ctx, http.MethodGet, "/v1/device/capabilities", nil, &response); err != nil {
		var e *HTTPError
		if errors.As(err, &e) && (e.Status == 404 || e.Status == 405) {
			c.capabilityDiscovery = 1
			return 1, nil
		}
		return 0, err
	}
	if response.Versions == nil || len(response.Versions) > 16 {
		return 0, errors.New("invalid Teams capability discovery")
	}
	prev := 0
	for _, v := range response.Versions {
		if v <= prev {
			return 0, errors.New("invalid Teams capability discovery")
		}
		prev = v
		if v == 2 {
			c.capabilityDiscovery = 2
		}
	}
	if c.capabilityDiscovery == 0 {
		c.capabilityDiscovery = 1
	}
	return c.capabilityDiscovery, nil
}
func (c *DeviceClient) resetManifestVersion() {
	c.capabilityMu.Lock()
	c.capabilityDiscovery = 0
	c.capabilityMu.Unlock()
}

func (c *DeviceClient) Poll(ctx context.Context, in PollRequest) (PollResponse, error) {
	var out PollResponse
	if in.AppVersion == "" || in.LibraryIDs == nil || len(in.LibraryIDs) > 100 || in.WaitSeconds < 0 || in.WaitSeconds > 25 {
		return out, errors.New("invalid poll")
	}
	if in.CapabilityManifest != nil {
		m := in.CapabilityManifest
		if m.SchemaVersion != 1 || m.AppVersion != in.AppVersion || !capabilityVersionPattern.MatchString(m.AppVersion) || m.Capabilities == nil {
			return out, errors.New("invalid capability manifest")
		}
		previous := ""
		for _, id := range m.Capabilities {
			if (id != "deterministic_query_v1" && id != "export_deterministic_v1" && id != "recurring_deterministic_v1" && id != "report_deterministic_v1" && id != "scoped_artifact_v1" && id != "source_snapshot_v2") || id <= previous {
				return out, errors.New("invalid capability manifest")
			}
			previous = id
		}
	}
	seen := map[string]bool{}
	for _, id := range in.LibraryIDs {
		if _, err := LibraryPath(id, "privacy"); err != nil || seen[id] {
			return out, errors.New("invalid poll library")
		}
		seen[id] = true
	}
	var request any = in
	v2 := false
	if in.CapabilityManifest != nil {
		version, err := c.manifestVersion(ctx)
		if err != nil {
			return out, err
		}
		v2 = version == 2
	}
	if v2 {
		// Explicit fields retain empty arrays in the v2 closed wire shape.
		request = struct {
			AppVersion         string   `json:"app_version"`
			LibraryIDs         []string `json:"library_ids"`
			WaitSeconds        int      `json:"wait_seconds"`
			CapabilityManifest any      `json:"capability_manifest"`
		}{in.AppVersion, in.LibraryIDs, in.WaitSeconds, struct {
			SchemaVersion int                 `json:"schema_version"`
			AppVersion    string              `json:"app_version"`
			Features      []string            `json:"features"`
			Profiles      []CapabilityProfile `json:"profiles"`
		}{2, in.AppVersion, in.CapabilityManifest.Capabilities, []CapabilityProfile{}}}
	}
	b, err := marshalRequest(request)
	if err != nil {
		return out, err
	}
	err = c.exchange(ctx, http.MethodPost, "/v1/device/poll", b, &out)
	// Only a protocol rejection permits one legacy retry. Auth and transport
	// failures do not silently downgrade the installation's declaration.
	retryLegacy := v2 && err == nil && out.CapabilityStatus == "manifest_rejected" && len(out.Work) == 0
	if v2 && err != nil {
		var e *HTTPError
		retryLegacy = errors.As(err, &e) && (e.Code == "unsupported_manifest_schema" || (e.Status == 422 && e.Code == "invalid_input"))
	}
	if retryLegacy {
		c.capabilityMu.Lock()
		c.capabilityDiscovery = 1
		c.capabilityMu.Unlock()
		b, err = marshalRequest(in)
		if err != nil {
			return out, err
		}
		out = PollResponse{}
		err = c.exchange(ctx, http.MethodPost, "/v1/device/poll", b, &out)
	}

	if err != nil {
		return out, err
	}
	if (out.CapabilityStatus != "" && out.CapabilityStatus != "needs_update") || (out.CapabilityStatus == "needs_update" && len(out.Work) != 0) {
		return PollResponse{}, errors.New("invalid Teams capability status")
	}
	for _, w := range out.Work {
		if err = w.VerifyConfig(); err != nil {
			return PollResponse{}, err
		}
	}
	return out, nil
}
func (c *DeviceClient) RegisterSources(ctx context.Context, workID string, m Manifest) (SourcesResponse, error) {
	var out SourcesResponse
	path, err := WorkPath(workID, "sources")
	if err != nil {
		return out, err
	}
	body, digest, err := m.Bytes()
	if err != nil {
		return out, err
	}
	if err = c.exchange(ctx, http.MethodPut, path, body, &out); err != nil {
		return out, err
	}
	if out.ManifestSHA256 != digest {
		return SourcesResponse{}, errors.New("Teams manifest receipt does not match")
	}
	if out.EvidenceConflictCount != 0 || out.UnavailableSourceCount != 0 {
		return SourcesResponse{}, errors.New("Teams source evidence requires reconciliation")
	}
	if out.OwnershipRevision < 1 || ValidateTokens(out.OwnedSourceTokens, 10000) != nil {
		return SourcesResponse{}, errors.New("invalid Teams ownership response")
	}
	registered := map[string]bool{}
	for _, source := range m.SourceManifest {
		registered[source.Token] = true
	}
	for _, token := range out.OwnedSourceTokens {
		if !registered[token] {
			return SourcesResponse{}, errors.New("invalid Teams ownership response")
		}
	}
	return out, nil
}
func (c *DeviceClient) Privacy(ctx context.Context, workID string) (PrivacySnapshot, error) {
	var out PrivacySnapshot
	path, err := WorkPath(workID, "privacy")
	if err != nil {
		return out, err
	}
	err = c.exchange(ctx, http.MethodGet, path, nil, &out)
	if err == nil && (out.PrivacyRevision < 0 || ValidateTokens(out.PrivateSourceTokens, 100000) != nil) {
		return PrivacySnapshot{}, errors.New("invalid Teams privacy response")
	}
	return out, err
}
func (c *DeviceClient) UpdatePrivacy(ctx context.Context, libraryID string, u PrivacyUpdate) (int64, error) {
	path, err := LibraryPath(libraryID, "privacy")
	if err != nil {
		return 0, err
	}
	if u.LocalRevision < 1 || u.Mark == nil || u.Release == nil {
		return 0, errors.New("invalid privacy update")
	}
	body, err := marshalRequest(u)
	if err != nil {
		return 0, err
	}
	var result struct {
		PrivacyRevision int64 `json:"privacy_revision"`
	}
	if err = c.exchange(ctx, http.MethodPost, path, body, &result); err != nil {
		return 0, err
	}
	return result.PrivacyRevision, nil
}

// Submit sends the exact already-authorized outbox body. It must not construct
// or amend a reviewed payload. On uncertain success use Receipt for the same
// work, then compare the receipt hash before marking it delivered.
func (c *DeviceClient) Submit(ctx context.Context, workID string, body []byte, payloadSHA string) (Receipt, error) {
	var out Receipt
	body = bytes.Clone(body)
	var submission Submission
	if !validUniqueJSON(body) || json.Unmarshal(body, &submission) != nil || submission.PayloadSHA256 != payloadSHA || hashBytes(submission.Payload) != payloadSHA {
		return out, errors.New("invalid staged Teams payload")
	}
	path, err := WorkPath(workID, "result")
	if err != nil {
		return out, err
	}
	if err = c.exchange(ctx, http.MethodPut, path, body, &out); err != nil {
		return out, err
	}
	if out.WorkID != workID || out.PayloadSHA256 != payloadSHA || out.SubmissionSHA256 != hashBytes(body) || out.IdempotencyKey != submission.IdempotencyKey || out.InstallationID != c.installation {
		return Receipt{}, ErrReceiptMismatch
	}
	return out, nil
}
func (c *DeviceClient) Receipt(ctx context.Context, workID, payloadSHA, submissionSHA, idempotencyKey string) (Receipt, error) {
	var out Receipt
	path, err := WorkPath(workID, "receipt")
	if err != nil {
		return out, err
	}
	if err = c.exchange(ctx, http.MethodGet, path, nil, &out); err != nil {
		return out, err
	}
	if out.WorkID != workID || out.PayloadSHA256 != payloadSHA || out.SubmissionSHA256 != submissionSHA || out.IdempotencyKey != idempotencyKey || out.InstallationID != c.installation {
		return Receipt{}, ErrReceiptMismatch
	}
	return out, nil
}
