package teams

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// EnrollmentAttempt keeps its key and callback state in memory until a human
// confirms the matching fingerprint in the authenticated browser. The caller
// must not put ApprovalURL in logs: its fragment contains a one-time secret.
type EnrollmentAttempt struct {
	ApprovalURL    string
	Fingerprint    string
	ExpiresAt      time.Time
	origin         string
	libraryID      string
	id             string
	key            ed25519.PrivateKey
	state          string
	allowLoopback  bool
	listener       net.Listener
	server         *http.Server
	code           chan string
	once           sync.Once
	closed         chan struct{}
	closeOnce      sync.Once
	redeemMu       sync.Mutex
	redemptionCode string
	completed      bool
}

type enrollmentStart struct {
	PublicKey         string `json:"public_key"`
	LibraryID         string `json:"library_id"`
	LibraryLabel      string `json:"library_label"`
	DisclosureVersion string `json:"disclosure_version"`
	CallbackPort      int    `json:"callback_port"`
	CallbackState     string `json:"callback_state"`
}
type enrollmentStarted struct {
	ID          string    `json:"enrollment_id"`
	ApprovalURL string    `json:"approval_url"`
	ExpiresAt   time.Time `json:"expires_at"`
}
type enrollmentRedeemed struct {
	OrgID          string         `json:"org_id"`
	PersonID       string         `json:"person_id"`
	InstallationID string         `json:"installation_id"`
	LibraryID      string         `json:"library_id"`
	SourceTokenKey string         `json:"source_token_key"`
	MetricsDefault MetricsDefault `json:"metrics_default"`
}

// EnrollmentCredential is intentionally not serializable. A host-owned secure
// store must persist its key and origin, bound to the library descriptor and
// installation, before a worker can use it.
type EnrollmentCredential struct {
	OrgID, PersonID, InstallationID, LibraryID, Origin string
	MetricsDefault                                     MetricsDefault
	key                                                ed25519.PrivateKey
	sourceKey                                          []byte
}

func (c *EnrollmentCredential) SigningKey() ed25519.PrivateKey { return bytes.Clone(c.key) }
func (c *EnrollmentCredential) SourceKey() []byte              { return bytes.Clone(c.sourceKey) }
func (c *EnrollmentCredential) Destroy() {
	if c != nil {
		clear(c.key)
		clear(c.sourceKey)
		c.key = nil
		c.sourceKey = nil
	}
}

func enrollmentFingerprint(pub ed25519.PublicKey, id string) (string, error) {
	if len(pub) != ed25519.PublicKeySize || !validDeviceID(id) {
		return "", errors.New("invalid enrollment identity")
	}
	h := sha256.New()
	h.Write(pub)
	h.Write([]byte(id))
	return hex.EncodeToString(h.Sum(nil))[:10], nil
}
func randomOpaque32() (string, error) {
	var b [32]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
func validOpaque32(s string) bool {
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return err == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == s
}

// BeginEnrollment creates an ephemeral loopback listener and sends the public
// key only. The browser must show the user this attempt's fingerprint.
func BeginEnrollment(ctx context.Context, origin, libraryID, label, disclosure string, allowLoopbackHTTP bool) (*EnrollmentAttempt, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	return BeginEnrollmentWithKey(ctx, origin, libraryID, label, disclosure, key, allowLoopbackHTTP)
}

// BeginEnrollmentWithKey reuses this application's protected installation key
// when binding another library to the same service.
func BeginEnrollmentWithKey(ctx context.Context, origin, libraryID, label, disclosure string, installationKey ed25519.PrivateKey, allowLoopbackHTTP bool) (*EnrollmentAttempt, error) {
	if _, err := LibraryPath(libraryID, "privacy"); err != nil || label == "" || len(label) > 200 || disclosure == "" || len(disclosure) > 100 {
		return nil, errors.New("invalid enrollment details")
	}
	if len(installationKey) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid installation credential")
	}
	derived := ed25519.NewKeyFromSeed(installationKey[:ed25519.SeedSize])
	valid := bytes.Equal(derived, installationKey)
	clear(derived)
	if !valid {
		return nil, errors.New("invalid installation credential")
	}
	key := ed25519.PrivateKey(bytes.Clone(installationKey))
	pub := key.Public().(ed25519.PublicKey)
	// Reuse the device client's exact origin/TLS/redirect restrictions.
	transport, err := NewDeviceClient(origin, "00000000-0000-4000-8000-000000000000", key, allowLoopbackHTTP)
	if err != nil {
		clear(key)
		return nil, err
	}
	defer transport.Destroy()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		clear(key)
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			listener.Close()
			clear(key)
		}
	}()
	state, err := randomOpaque32()
	if err != nil {
		return nil, err
	}
	in := enrollmentStart{PublicKey: base64.RawURLEncoding.EncodeToString(pub), LibraryID: libraryID, LibraryLabel: label, DisclosureVersion: disclosure, CallbackPort: listener.Addr().(*net.TCPAddr).Port, CallbackState: state}
	body, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	startContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(startContext, http.MethodPost, transport.origin.String()+"/v1/device/enrollments", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := transport.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 64<<10+1))
	if err != nil || len(data) > 64<<10 || response.StatusCode != http.StatusCreated || !validUniqueJSON(data) {
		return nil, errors.New("Teams enrollment could not start")
	}
	var started enrollmentStarted
	if json.Unmarshal(data, &started) != nil || !validDeviceID(started.ID) {
		return nil, errors.New("invalid Teams enrollment response")
	}
	now := time.Now()
	if !started.ExpiresAt.After(now) || started.ExpiresAt.After(now.Add(15*time.Minute+30*time.Second)) {
		return nil, errors.New("invalid Teams enrollment expiry")
	}
	approval, err := url.Parse(started.ApprovalURL)
	if err != nil || approval.Scheme != transport.origin.Scheme || approval.Host != transport.origin.Host || approval.User != nil || approval.Path != "/enroll/"+started.ID || approval.RawQuery != "" || approval.ForceQuery {
		return nil, errors.New("invalid Teams approval URL")
	}
	fragment, err := url.ParseQuery(approval.Fragment)
	if err != nil || len(fragment) != 1 || len(fragment["confirmation"]) != 1 || !validOpaque32(fragment.Get("confirmation")) {
		return nil, errors.New("invalid Teams approval URL")
	}
	fingerprint, err := enrollmentFingerprint(pub, started.ID)
	if err != nil {
		return nil, err
	}
	attempt := &EnrollmentAttempt{ApprovalURL: started.ApprovalURL, Fingerprint: fingerprint, ExpiresAt: started.ExpiresAt, origin: transport.origin.String(), libraryID: libraryID, id: started.ID, key: key, state: state, allowLoopback: allowLoopbackHTTP, listener: listener, code: make(chan string, 1), closed: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/teams/enrollment", attempt.callback)
	attempt.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 8192}
	go func() { _ = attempt.server.Serve(listener) }()
	time.AfterFunc(time.Until(attempt.ExpiresAt), func() { _ = attempt.Close() })
	ok = true
	return attempt, nil
}
func (a *EnrollmentAttempt) callback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	q, parseErr := url.ParseQuery(r.URL.RawQuery)
	if parseErr != nil || r.Method != http.MethodGet || r.URL.Path != "/teams/enrollment" || len(q) != 2 || len(q["state"]) != 1 || len(q["code"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(a.state)) != 1 || !validOpaque32(q.Get("code")) || !time.Now().Before(a.ExpiresAt) {
		http.Error(w, "Enrollment could not be completed.", http.StatusBadRequest)
		return
	}
	accepted := false
	a.once.Do(func() { accepted = true })
	if !accepted {
		http.Error(w, "Enrollment has already been received.", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "You can return to Pharos.")
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	a.code <- q.Get("code")
}
func (a *EnrollmentAttempt) Close() error {
	a.closeOnce.Do(func() { close(a.closed) })
	if a.server != nil {
		return a.server.Close()
	}
	return a.listener.Close()
}

// Redeem waits for this attempt's single-use callback and signs redemption
// with its original key. A timeout or cancellation stops the loopback server.
func (a *EnrollmentAttempt) Redeem(ctx context.Context) (*EnrollmentCredential, error) {
	a.redeemMu.Lock()
	defer a.redeemMu.Unlock()
	if a.completed || !time.Now().Before(a.ExpiresAt) {
		return nil, errors.New("Teams enrollment expired or completed")
	}
	timer := time.NewTimer(time.Until(a.ExpiresAt))
	defer timer.Stop()
	if a.redemptionCode == "" {
		select {
		case a.redemptionCode = <-a.code:
			_ = a.Close()
		case <-ctx.Done():
			_ = a.Close()
			return nil, ctx.Err()
		case <-a.closed:
			return nil, errors.New("Teams enrollment closed")
		case <-timer.C:
			_ = a.Close()
			return nil, errors.New("Teams enrollment expired")
		}
	}
	client, err := NewDeviceClient(a.origin, a.id, a.key, a.allowLoopback)
	if err != nil {
		return nil, err
	}
	defer client.Destroy()
	body, err := json.Marshal(struct {
		Code string `json:"enrollment_code"`
	}{a.redemptionCode})
	if err != nil {
		return nil, err
	}
	var out enrollmentRedeemed
	path := "/v1/device/enrollments/" + a.id + "/redeem"
	if err = client.exchange(ctx, http.MethodPost, path, body, &out); err != nil {
		return nil, err
	}
	sourceKey, err := base64.RawURLEncoding.Strict().DecodeString(out.SourceTokenKey)
	if err != nil || len(sourceKey) != 32 || base64.RawURLEncoding.EncodeToString(sourceKey) != out.SourceTokenKey || !validDeviceID(out.OrgID) || !validDeviceID(out.PersonID) || !validDeviceID(out.InstallationID) || out.LibraryID != a.libraryID || out.MetricsDefault.PolicyVersion < 1 {
		return nil, fmt.Errorf("invalid Teams enrollment credential")
	}
	credential := &EnrollmentCredential{OrgID: out.OrgID, PersonID: out.PersonID, InstallationID: out.InstallationID, LibraryID: out.LibraryID, Origin: a.origin, MetricsDefault: out.MetricsDefault, key: bytes.Clone(a.key), sourceKey: sourceKey}
	a.completed = true
	a.redemptionCode = ""
	clear(a.key)
	return credential, nil
}

// CallbackAddress is for the local application to show diagnostic state only;
// it never contains the callback's state or redemption code.
func (a *EnrollmentAttempt) CallbackAddress() string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(a.listener.Addr().(*net.TCPAddr).Port))
}
