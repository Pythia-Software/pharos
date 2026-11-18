package teams

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

var ErrTypedResult = errors.New("invalid Teams typed result transport")

// TypedResultCommand identifies sealed, canonical bytes retained by the caller.
// Reconstruct it from the same bytes after a restart; never regenerate a reviewed result.
type TypedResultCommand struct {
	WorkID      string
	Reservation []byte
	Pages       [][]byte
	Commit      []byte
}

func (cmd TypedResultCommand) clone() TypedResultCommand {
	cmd.Reservation = bytes.Clone(cmd.Reservation)
	cmd.Commit = bytes.Clone(cmd.Commit)
	pages := make([][]byte, len(cmd.Pages))
	for i := range cmd.Pages {
		pages[i] = bytes.Clone(cmd.Pages[i])
	}
	cmd.Pages = pages
	return cmd
}

type typedResultHeader struct {
	UploadID           string `json:"upload_id"`
	ReservationSHA256  string `json:"reservation_sha256"`
	TerminalPageSHA256 string `json:"terminal_page_sha256"`
	PreviousPageSHA256 string `json:"previous_page_sha256"`
	PayloadSHA256      string `json:"payload_sha256"`
	ExpiresAt          string `json:"expires_at"`
	SourceBindingID    string `json:"source_binding_id"`
	AssignmentSHA256   string `json:"assignment_sha256"`
	InventoryRevision  int64  `json:"inventory_revision"`
	CatalogRevision    int64  `json:"catalog_revision"`
	Index              *int   `json:"index"`
	PageCount          int    `json:"page_count"`
	RowCount           *int   `json:"row_count"`
	DeleteCount        *int   `json:"delete_count"`
	ByteCount          int    `json:"byte_count"`
}
type TypedResultIdentity struct {
	WorkID, InstallationID, UploadID, ReservationSHA256, PayloadSHA256, CommitBodySHA256, ReservationExpiresAt string
	PageSHA256                                                                                                 []string
	SourceBindingID, AssignmentSHA256                                                                          string
	InventoryRevision, CatalogRevision                                                                         int64
}

func typedDomainHash(domain string, b []byte) string {
	h := sha256.New()
	h.Write([]byte(domain + "\n"))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}
func typedHeader(raw []byte, max int) (typedResultHeader, error) {
	var h typedResultHeader
	if len(raw) == 0 || len(raw) > max || !validUniqueJSON(raw) || json.Unmarshal(raw, &h) != nil {
		return h, ErrTypedResult
	}
	return h, nil
}

func typedFields(raw []byte, names string) bool {
	var fields map[string]json.RawMessage
	if !validUniqueJSON(raw) || json.Unmarshal(raw, &fields) != nil {
		return false
	}
	expected := strings.Fields(names)
	if len(fields) != len(expected) {
		return false
	}
	for _, name := range expected {
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	return true
}

// identity validates the transport graph and calculates the server's digests.
// Page and reservation bytes must already use the canonical encoding specified
// by the typed-query contract; the service independently validates their content.
func (cmd TypedResultCommand) identity(installation string) (TypedResultIdentity, error) {
	var id TypedResultIdentity
	if len(cmd.Reservation) == 0 || len(cmd.Reservation) > 32768 || len(cmd.Commit) == 0 || len(cmd.Commit) > 4096 || len(cmd.Pages) < 1 || len(cmd.Pages) > 128 {
		return id, ErrTypedResult
	}
	totalSize := 0
	for _, p := range cmd.Pages {
		if len(p) == 0 || len(p) > 512<<10 || totalSize > (16<<20)-len(p) {
			return id, ErrTypedResult
		}
		totalSize += len(p)
	}
	if !typedFields(cmd.Reservation, "upload_id config_sha256 evaluation_revision local_revision manifest_sha256 source_binding_id assignment_sha256 inventory_revision catalog_revision ownership_revision baseline_revision schema_sha256 window outcome form observed_at computed_at coverage page_count row_count delete_count byte_count payload_sha256 approval expires_at") || !typedFields(cmd.Commit, "upload_id reservation_sha256 terminal_page_sha256 page_count row_count delete_count byte_count payload_sha256") {
		return id, ErrTypedResult
	}
	r, e := typedHeader(cmd.Reservation, 32768)
	if e != nil || !validDeviceID(cmd.WorkID) || !validDeviceID(installation) || !validDeviceID(r.UploadID) || !sourceDigest(r.PayloadSHA256) || r.PageCount < 1 || r.PageCount > 128 || r.RowCount == nil || r.DeleteCount == nil || *r.RowCount < 0 || *r.RowCount > 10000 || *r.DeleteCount < 0 || *r.DeleteCount > 10000 || r.ByteCount < 1 || r.ByteCount > 16<<20 || len(cmd.Pages) != r.PageCount || !validDeviceID(r.SourceBindingID) || !sourceDigest(r.AssignmentSHA256) || r.InventoryRevision < 1 || r.InventoryRevision > 1<<53-1 || r.CatalogRevision < 1 || r.CatalogRevision > 1<<53-1 {
		return id, ErrTypedResult
	}
	expiry, expiryErr := time.Parse(time.RFC3339Nano, r.ExpiresAt)
	if expiryErr != nil || expiry.UTC().Format(time.RFC3339Nano) != r.ExpiresAt {
		return id, ErrTypedResult
	}
	// The canonical scope uses the reservation's canonical JSON verbatim.
	scoped := append([]byte(fmt.Sprintf(`{"work_id":%q,"installation_id":%q,"reservation":`, cmd.WorkID, installation)), cmd.Reservation...)
	scoped = append(scoped, '}')
	id = TypedResultIdentity{WorkID: cmd.WorkID, InstallationID: installation, UploadID: r.UploadID, ReservationSHA256: typedDomainHash("pharos-teams-typed-result-reservation-v2", scoped), PayloadSHA256: r.PayloadSHA256, ReservationExpiresAt: r.ExpiresAt, CommitBodySHA256: hashBytes(cmd.Commit), SourceBindingID: r.SourceBindingID, AssignmentSHA256: r.AssignmentSHA256, InventoryRevision: r.InventoryRevision, CatalogRevision: r.CatalogRevision}
	prev := id.ReservationSHA256
	total := 0
	rows, deletes := 0, 0
	for i, raw := range cmd.Pages {
		p, err := typedHeader(raw, 512<<10)
		var page struct {
			UploadID string            `json:"upload_id"`
			Index    *int              `json:"index"`
			Previous string            `json:"previous_page_sha256"`
			Rows     []json.RawMessage `json:"rows"`
			Deletes  []json.RawMessage `json:"delete_keys"`
		}
		if !exactTypedJSON(raw, &page) || page.Rows == nil || page.Deletes == nil {
			return TypedResultIdentity{}, ErrTypedResult
		}
		rows += len(page.Rows)
		deletes += len(page.Deletes)
		total += len(raw)
		if err != nil || p.UploadID != id.UploadID || p.Index == nil || *p.Index != i || p.PreviousPageSHA256 != prev || total > 16<<20 {
			return TypedResultIdentity{}, ErrTypedResult
		}
		prev = typedDomainHash("pharos-teams-typed-result-page-v2", raw)
		id.PageSHA256 = append(id.PageSHA256, prev)
	}
	c, err := typedHeader(cmd.Commit, 4096)
	if err != nil || c.UploadID != id.UploadID || c.ReservationSHA256 != id.ReservationSHA256 || c.TerminalPageSHA256 != prev || c.PayloadSHA256 != id.PayloadSHA256 || c.PageCount != r.PageCount || c.RowCount == nil || c.DeleteCount == nil || *c.RowCount != *r.RowCount || *c.DeleteCount != *r.DeleteCount || c.ByteCount != r.ByteCount || total != r.ByteCount || rows != *r.RowCount || deletes != *r.DeleteCount {
		return TypedResultIdentity{}, ErrTypedResult
	}
	return id, nil
}

// ErrTypedResultUncertain means a dispatched request may have taken effect. It
// never authorizes a retry. Persist and serialize state across client instances;
// recheck current authorization before every explicitly chosen content send.
var ErrTypedResultUncertain = errors.New("Teams typed result outcome unresolved")

// PreparedTypedResult owns a validated immutable graph. Construction requires
// exclusive access to the input buffers; subsequent caller mutation is harmless.
type PreparedTypedResult struct {
	reservation, commit []byte
	pages               [][]byte
	id                  TypedResultIdentity
}

func PrepareTypedResult(cmd TypedResultCommand, installation string) (*PreparedTypedResult, error) {
	id, err := cmd.identity(installation)
	if err != nil {
		return nil, err
	}
	copy := cmd.clone()
	return &PreparedTypedResult{copy.Reservation, copy.Commit, copy.Pages, id}, nil
}

// Identity is small durable reconciliation metadata; persist it before dispatch.
func (p *PreparedTypedResult) Identity() TypedResultIdentity {
	id := p.id
	id.PageSHA256 = append([]string(nil), id.PageSHA256...)
	return id
}
func (id TypedResultIdentity) valid(installation string) bool {
	t, e := time.Parse(time.RFC3339Nano, id.ReservationExpiresAt)
	return validDeviceID(id.WorkID) && validDeviceID(id.InstallationID) && id.InstallationID == installation && validDeviceID(id.UploadID) && validDeviceID(id.SourceBindingID) && sourceDigest(id.ReservationSHA256) && sourceDigest(id.CommitBodySHA256) && sourceDigest(id.PayloadSHA256) && sourceDigest(id.AssignmentSHA256) && id.InventoryRevision > 0 && id.InventoryRevision <= 1<<53-1 && id.CatalogRevision > 0 && id.CatalogRevision <= 1<<53-1 && e == nil && t.UTC().Format(time.RFC3339Nano) == id.ReservationExpiresAt
}

// LookupTypedResultReceipt is read-only. Missing or mismatched receipts leave
// the command unresolved and do not prove that a reservation was closed.
func (c *DeviceClient) LookupTypedResultReceipt(ctx context.Context, id TypedResultIdentity, prior *Receipt) (Receipt, error) {
	var out Receipt
	if !id.valid(c.installation) {
		return out, ErrTypedResult
	}
	if err := c.typedResultExchange(ctx, http.MethodGet, "/v1/device/work/"+id.WorkID+"/receipt", nil, &out); err != nil {
		return Receipt{}, fmt.Errorf("%w: %w", ErrTypedResultUncertain, err)
	}
	if !id.validReceipt(&out, prior) {
		return Receipt{}, ErrTypedResultUncertain
	}
	return out, nil
}

type TypedResultResponse struct {
	Status            string   `json:"status"`
	UploadID          string   `json:"upload_id"`
	ReservationSHA256 string   `json:"reservation_sha256,omitempty"`
	PageSHA256        string   `json:"page_sha256,omitempty"`
	Receipt           *Receipt `json:"receipt,omitempty"`
}
type TypedResultResolution struct {
	Status               string   `json:"status"`
	Receipt              *Receipt `json:"receipt,omitempty"`
	UploadID             string   `json:"upload_id,omitempty"`
	ReservationSHA256    string   `json:"reservation_sha256,omitempty"`
	CommitBodySHA256     string   `json:"commit_body_sha256,omitempty"`
	ReservationExpiresAt string   `json:"reservation_expires_at,omitempty"`
	RetainUntil          string   `json:"retain_until,omitempty"`
}

func (id TypedResultIdentity) validReceipt(r *Receipt, prior *Receipt) bool {
	if r == nil || !validDeviceID(r.ID) || r.WorkID != id.WorkID || r.InstallationID != id.InstallationID || r.IdempotencyKey != id.UploadID || r.PayloadSHA256 != id.PayloadSHA256 || r.SubmissionSHA256 != id.CommitBodySHA256 || r.ResultRevision < 1 || r.ResultRevision > 1<<53-1 || r.AcceptedAt.IsZero() || r.Outcome != "completed" || r.SourceBindingID != id.SourceBindingID || r.AssignmentSHA256 != id.AssignmentSHA256 || r.InventoryRevision != id.InventoryRevision || r.CatalogRevision != id.CatalogRevision || (r.ResponseID != nil && !validDeviceID(*r.ResponseID)) || (r.FeedCursor != nil && (*r.FeedCursor < 1 || *r.FeedCursor > 1<<53-1)) {
		return false
	}
	switch r.PublicationState {
	case "published", "withheld", "superseded":
	case "staged":
		if !r.ProjectionPending {
			return false
		}
	default:
		return false
	}
	if prior != nil {
		// The first reply uses the handler clock; a replay reads PostgreSQL
		// timestamptz, which stores microseconds.
		left, right := *r, *prior
		// Publication visibility can change after the immutable acceptance.
		left.PublicationState, right.PublicationState = "", ""
		left.AcceptedAt = left.AcceptedAt.UTC().Truncate(time.Microsecond)
		right.AcceptedAt = right.AcceptedAt.UTC().Truncate(time.Microsecond)
		a, _ := json.Marshal(left)
		b, _ := json.Marshal(right)
		return bytes.Equal(a, b)
	}
	return true
}
func (c *DeviceClient) typedResultExchange(ctx context.Context, method, path string, body []byte, out any) error {
	var raw json.RawMessage
	if err := c.exchangeLimit(ctx, method, path, body, &raw, 32768); err != nil {
		return err
	}
	if len(raw) > 32768 || !exactTypedJSON(raw, out) {
		return ErrTypedResult
	}
	return nil
}
func (c *DeviceClient) typedResultAction(ctx context.Context, cmd *PreparedTypedResult, index int, prior *Receipt, authorize TypedResultAuthorization) (TypedResultResponse, error) {
	var out TypedResultResponse
	if cmd == nil || cmd.id.InstallationID != c.installation {
		return out, ErrTypedResult
	}
	id := cmd.id
	var err error
	base := "/v1/device/work/" + id.WorkID + "/typed-result-uploads"
	method, path, body := http.MethodPost, base, cmd.reservation
	if index >= 0 {
		if index >= len(cmd.pages) {
			return out, ErrTypedResult
		}
		method, path, body = http.MethodPut, fmt.Sprintf("%s/%s/pages/%d", base, id.UploadID, index), cmd.pages[index]
	}
	if index == -2 {
		method, path, body = http.MethodPut, base+"/"+id.UploadID+"/commit", cmd.commit
	}
	if index < -2 {
		return out, ErrTypedResult
	}
	if authorize == nil {
		return out, ErrTypedResult
	}
	if err = authorize(ctx, cmd.Identity(), method, path); err != nil {
		return out, fmt.Errorf("%w: %w", ErrTypedResult, err)
	}
	if err = c.typedResultExchange(ctx, method, path, body, &out); err != nil {
		return TypedResultResponse{}, fmt.Errorf("%w: %w", ErrTypedResultUncertain, err)
	}
	if out.UploadID != id.UploadID {
		return TypedResultResponse{}, ErrTypedResultUncertain
	}
	if prior != nil && out.Status != "accepted" {
		return TypedResultResponse{}, ErrTypedResultUncertain
	}
	switch out.Status {
	case "accepted":
		if out.PageSHA256 != "" || (index != -1 && out.ReservationSHA256 != "") || (index == -1 && out.ReservationSHA256 != id.ReservationSHA256) || !id.validReceipt(out.Receipt, prior) {
			return TypedResultResponse{}, ErrTypedResultUncertain
		}
	case "open":
		if index != -1 || out.ReservationSHA256 != id.ReservationSHA256 || out.PageSHA256 != "" || out.Receipt != nil {
			return TypedResultResponse{}, ErrTypedResultUncertain
		}
	case "staged":
		if index < 0 || out.PageSHA256 != id.PageSHA256[index] || out.ReservationSHA256 != "" || out.Receipt != nil {
			return TypedResultResponse{}, ErrTypedResultUncertain
		}
	default:
		return TypedResultResponse{}, ErrTypedResultUncertain
	}
	return out, nil
}

// TypedResultAuthorization must durably record the in-flight identity. The caller must already hold
// exclusive sender ownership through the request and outcome handling. It must
// recheck current authorization for this exact content action, including retries.
// A nil callback is rejected; this transport never retries automatically.
type TypedResultAuthorization func(context.Context, TypedResultIdentity, string, string) error

// ReserveTypedResult and StageTypedResultPage send only the exact sealed bytes.
func (c *DeviceClient) ReserveTypedResult(ctx context.Context, cmd *PreparedTypedResult, prior *Receipt, authorize TypedResultAuthorization) (TypedResultResponse, error) {
	return c.typedResultAction(ctx, cmd, -1, prior, authorize)
}
func (c *DeviceClient) StageTypedResultPage(ctx context.Context, cmd *PreparedTypedResult, index int, prior *Receipt, authorize TypedResultAuthorization) (TypedResultResponse, error) {
	if index < 0 {
		return TypedResultResponse{}, ErrTypedResult
	}
	return c.typedResultAction(ctx, cmd, index, prior, authorize)
}
func (c *DeviceClient) CommitTypedResult(ctx context.Context, cmd *PreparedTypedResult, prior *Receipt, authorize TypedResultAuthorization) (TypedResultResponse, error) {
	return c.typedResultAction(ctx, cmd, -2, prior, authorize)
}

// CloseTypedResult explicitly requests an irreversible server fence. The durable sender
// must serialize this decision with all sends and persist the identity first.
func (c *DeviceClient) CloseTypedResult(ctx context.Context, id TypedResultIdentity, prior *Receipt) (TypedResultResolution, error) {
	var out TypedResultResolution
	if !id.valid(c.installation) {
		return out, ErrTypedResult
	}
	var err error
	body, _ := json.Marshal(struct {
		UploadID          string `json:"upload_id"`
		ReservationSHA256 string `json:"reservation_sha256"`
		CommitBodySHA256  string `json:"commit_body_sha256"`
	}{id.UploadID, id.ReservationSHA256, id.CommitBodySHA256})
	if err = c.typedResultExchange(ctx, http.MethodPost, "/v1/device/work/"+id.WorkID+"/typed-result-uploads/reconcile", body, &out); err != nil {
		return TypedResultResolution{}, fmt.Errorf("%w: %w", ErrTypedResultUncertain, err)
	}
	if prior != nil && out.Status != "accepted" {
		return TypedResultResolution{}, ErrTypedResultUncertain
	}
	switch out.Status {
	case "accepted":
		if !id.validReceipt(out.Receipt, prior) || out.UploadID != "" || out.ReservationSHA256 != "" || out.CommitBodySHA256 != "" || out.ReservationExpiresAt != "" || out.RetainUntil != "" {
			return TypedResultResolution{}, ErrTypedResultUncertain
		}
	case "indeterminate":
		if out.Receipt != nil || out.UploadID != "" || out.ReservationSHA256 != "" || out.CommitBodySHA256 != "" || out.ReservationExpiresAt != "" || out.RetainUntil != "" {
			return TypedResultResolution{}, ErrTypedResultUncertain
		}
	case "closed":
		expiry, e := time.Parse(time.RFC3339Nano, out.ReservationExpiresAt)
		retain, e2 := time.Parse(time.RFC3339Nano, out.RetainUntil)
		expectedExpiry, _ := time.Parse(time.RFC3339Nano, id.ReservationExpiresAt)
		if out.Receipt != nil || out.UploadID != id.UploadID || out.ReservationSHA256 != id.ReservationSHA256 || out.CommitBodySHA256 != id.CommitBodySHA256 || !expiry.Equal(expectedExpiry.Truncate(time.Microsecond)) || e != nil || e2 != nil || expiry.UTC().Format(time.RFC3339Nano) != out.ReservationExpiresAt || retain.UTC().Format(time.RFC3339Nano) != out.RetainUntil || retain.Before(expiry.Add(7*24*time.Hour)) {
			return TypedResultResolution{}, ErrTypedResultUncertain
		}
	default:
		return TypedResultResolution{}, ErrTypedResultUncertain
	}
	return out, nil
}
