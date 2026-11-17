package teams

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// These are public protocol DTOs. Config and payload remain raw bytes because
// their hashes and approvals bind exact serialized bytes, not reconstructed JSON.
type Work struct {
	ID                    string          `json:"id"`
	RequestID             string          `json:"request_id"`
	VersionID             string          `json:"version_id"`
	LibraryID             string          `json:"library_id"`
	Kind                  string          `json:"kind"`
	ConfigSHA256          string          `json:"config_sha256"`
	Config                json.RawMessage `json:"config"`
	Period                json.RawMessage `json:"period"`
	PopulationKey         string          `json:"population_key"`
	ExpiresAt             time.Time       `json:"expires_at"`
	EvaluationRevision    int64           `json:"evaluation_revision"`
	RequiredLocalRevision int64           `json:"required_local_revision"`
	BaselineRevision      int64           `json:"baseline_revision"`
	RunDecision           json.RawMessage `json:"run_decision,omitempty"`
}
type Cancellation struct {
	WorkID      string    `json:"work_id"`
	CancelledAt time.Time `json:"cancelled_at"`
}
type MetricsDefault struct {
	Value         bool  `json:"value"`
	PolicyVersion int64 `json:"policy_version"`
}
type PollResponse struct {
	ServerTime      time.Time      `json:"server_time"`
	Work            []Work         `json:"work"`
	Cancellations   []Cancellation `json:"cancellations"`
	MetricsDefault  MetricsDefault `json:"metrics_default"`
	NextPollSeconds int            `json:"next_poll_seconds"`
}
type PollRequest struct {
	AppVersion  string   `json:"app_version"`
	LibraryIDs  []string `json:"library_ids"`
	WaitSeconds int      `json:"wait_seconds"`
}
type Source struct {
	Token            string `json:"token"`
	EvidenceRevision string `json:"evidence_revision"`
	Protection       string `json:"protection"`
}
type Manifest struct {
	LocalRevision  int64    `json:"local_revision"`
	Complete       bool     `json:"complete"`
	SourceManifest []Source `json:"source_manifest"`
}
type SourcesResponse struct {
	ManifestSHA256         string   `json:"manifest_sha256"`
	OwnershipRevision      int64    `json:"ownership_revision"`
	OwnedSourceTokens      []string `json:"owned_source_tokens"`
	EvidenceConflictCount  int      `json:"evidence_conflict_count,omitempty"`
	UnavailableSourceCount int      `json:"unavailable_source_count,omitempty"`
}
type PrivacyUpdate struct {
	LocalRevision int64    `json:"local_revision"`
	Mark          []string `json:"mark"`
	Release       []string `json:"release"`
}
type PrivacySnapshot struct {
	PrivacyRevision     int64    `json:"privacy_revision"`
	PrivateSourceTokens []string `json:"private_source_tokens"`
}
type Approval struct {
	Mode                  string    `json:"mode"`
	DecisionID            string    `json:"decision_id,omitempty"`
	DecidedAt             time.Time `json:"decided_at"`
	PrivacyRevision       int64     `json:"privacy_revision"`
	SharedPrivacyRevision int64     `json:"shared_privacy_revision"`
	SettingsRevision      int64     `json:"settings_revision"`
	OrgPolicyVersion      int64     `json:"org_policy_version,omitempty"`
}
type Submission struct {
	IdempotencyKey     string          `json:"idempotency_key"`
	ConfigSHA256       string          `json:"config_sha256"`
	EvaluationRevision int64           `json:"evaluation_revision"`
	LocalRevision      int64           `json:"local_revision"`
	ManifestSHA256     string          `json:"manifest_sha256"`
	OwnershipRevision  int64           `json:"ownership_revision"`
	BaselineRevision   int64           `json:"baseline_revision"`
	PayloadSHA256      string          `json:"payload_sha256"`
	Approval           Approval        `json:"approval"`
	Payload            json.RawMessage `json:"payload"`
}
type Row struct {
	Key    []json.RawMessage          `json:"key"`
	Values map[string]json.RawMessage `json:"values"`
}
type Coverage struct {
	SourceState string   `json:"source_state"`
	Measurement string   `json:"measurement"`
	Caveats     []string `json:"caveats"`
}
type Payload struct {
	Outcome    string              `json:"outcome"`
	Form       string              `json:"form"`
	ObservedAt time.Time           `json:"observed_at"`
	ComputedAt time.Time           `json:"computed_at"`
	Coverage   Coverage            `json:"coverage"`
	Rows       []Row               `json:"rows"`
	DeleteKeys [][]json.RawMessage `json:"delete_keys"`
}
type Receipt struct {
	ID                string    `json:"id"`
	WorkID            string    `json:"work_id"`
	PayloadSHA256     string    `json:"payload_sha256"`
	SubmissionSHA256  string    `json:"submission_sha256"`
	IdempotencyKey    string    `json:"idempotency_key"`
	InstallationID    string    `json:"installation_id"`
	ResultRevision    int64     `json:"result_revision"`
	AcceptedAt        time.Time `json:"accepted_at"`
	Outcome           string    `json:"outcome"`
	ResponseID        *string   `json:"response_id,omitempty"`
	FeedCursor        *int64    `json:"feed_cursor,omitempty"`
	ProjectionPending bool      `json:"projection_pending,omitempty"`
}

func hashBytes(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func (w Work) VerifyConfig() error {
	if !validUniqueJSON(w.Config) || hashBytes(w.Config) != w.ConfigSHA256 {
		return errors.New("work config hash mismatch")
	}
	return nil
}

// Signed config must have one interpretation in every decoder.
func validUniqueJSON(raw []byte) bool {
	if len(raw) == 0 || len(raw) > 2<<20 {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return errors.New("JSON too deep")
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				keyToken, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return errors.New("duplicate JSON key")
				}
				seen[key] = true
				if err = value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid JSON")
		}
		_, err = d.Token()
		return err
	}
	if value(0) != nil {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

// ValidUniqueJSON checks syntax, nesting and duplicate object keys before a
// payload is shown for approval or used as an authoritative wire object.
func ValidUniqueJSON(raw []byte) bool { return validUniqueJSON(raw) }
func validOpaque(s string, empty bool) bool {
	if s == "" && empty {
		return true
	}
	b, e := base64.RawURLEncoding.Strict().DecodeString(s)
	return e == nil && len(b) == sha256.Size && base64.RawURLEncoding.EncodeToString(b) == s
}

// ValidateTokens rejects malformed, duplicate and unsorted opaque token sets.
func ValidateTokens(tokens []string, limit int) error {
	if tokens == nil || len(tokens) > limit {
		return errors.New("invalid source token set")
	}
	previous := ""
	for _, token := range tokens {
		if !validOpaque(token, false) || token <= previous {
			return errors.New("invalid source token set")
		}
		previous = token
	}
	return nil
}

// Bytes returns the exact canonical manifest representation used by the server.
// Inputs must already be sorted; truncation and duplicate tokens are errors.
func (m Manifest) Bytes() ([]byte, string, error) {
	if m.LocalRevision < 1 || m.SourceManifest == nil || len(m.SourceManifest) > 10000 {
		return nil, "", errors.New("invalid manifest bounds")
	}
	previous := ""
	for _, s := range m.SourceManifest {
		if !validOpaque(s.Token, false) || !validOpaque(s.EvidenceRevision, true) || s.Token <= previous {
			return nil, "", errors.New("invalid source token")
		}
		if s.Protection != "public" && s.Protection != "private" && s.Protection != "unknown" && s.Protection != "unavailable" {
			return nil, "", errors.New("invalid source protection")
		}
		previous = s.Token
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, "", err
	}
	return b, hashBytes(b), nil
}

func WorkPath(id, operation string) (string, error) {
	if !validDeviceID(id) {
		return "", errors.New("invalid work ID")
	}
	switch operation {
	case "status", "privacy", "inputs", "sources", "result", "receipt":
		return fmt.Sprintf("/v1/device/work/%s/%s", id, operation), nil
	default:
		return "", errors.New("invalid work operation")
	}
}
func LibraryPath(id, operation string) (string, error) {
	if id == "" || id == "." || id == ".." || len(id) > 128 {
		return "", errors.New("invalid library ID")
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == '~') {
			return "", errors.New("invalid library ID")
		}
	}
	switch operation {
	case "privacy", "dirty":
		return "/v1/device/libraries/" + id + "/" + operation, nil
	default:
		return "", errors.New("invalid library operation")
	}
}

// SubmissionBytes preserves the exact payload bytes reviewed locally. Go's
// ordinary json.Marshal compacts RawMessage, so build only the envelope around
// those already-validated bytes.
func SubmissionBytes(s Submission) ([]byte, error) {
	if !validUniqueJSON(s.Payload) || hashBytes(s.Payload) != s.PayloadSHA256 {
		return nil, errors.New("invalid submission payload")
	}
	head := struct {
		IdempotencyKey     string   `json:"idempotency_key"`
		ConfigSHA256       string   `json:"config_sha256"`
		EvaluationRevision int64    `json:"evaluation_revision"`
		LocalRevision      int64    `json:"local_revision"`
		ManifestSHA256     string   `json:"manifest_sha256"`
		OwnershipRevision  int64    `json:"ownership_revision"`
		BaselineRevision   int64    `json:"baseline_revision"`
		PayloadSHA256      string   `json:"payload_sha256"`
		Approval           Approval `json:"approval"`
	}{s.IdempotencyKey, s.ConfigSHA256, s.EvaluationRevision, s.LocalRevision, s.ManifestSHA256, s.OwnershipRevision, s.BaselineRevision, s.PayloadSHA256, s.Approval}
	prefix, err := json.Marshal(head)
	if err != nil {
		return nil, err
	}
	body := append(prefix[:len(prefix)-1], []byte(`,"payload":`)...)
	body = append(body, s.Payload...)
	body = append(body, '}')
	if !validUniqueJSON(body) {
		return nil, errors.New("invalid submission envelope")
	}
	return body, nil
}
