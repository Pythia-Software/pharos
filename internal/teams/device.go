// Package teams provides the public Pharos Teams device transport.
package teams

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DeviceClient binds one installation key to exactly one service origin.
// Callers own its key storage and must reuse logical receipt IDs across retries;
// each HTTP attempt gets an independent nonce.
type DeviceClient struct {
	origin              *url.URL
	installation        string
	key                 ed25519.PrivateKey
	http                *http.Client
	capabilityMu        sync.Mutex
	capabilityDiscovery int // 0 unknown, 1 v1, 2 v2
}

// Destroy clears the in-memory signing key after the client is no longer used.
func (c *DeviceClient) Destroy() {
	if c != nil {
		clear(c.key)
		c.key = nil
	}
}

func validDeviceID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !('0' <= c && c <= '9') && !('a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// NewDeviceClient accepts plain HTTP only for an explicit IP-loopback test
// server; deployed services use HTTPS. Redirects are never followed.
func CanonicalOrigin(origin string, allowLoopbackHTTP bool) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("invalid Teams service origin")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || !allowLoopbackHTTP || ip == nil || !ip.IsLoopback() {
			return "", errors.New("Teams service origin requires HTTPS")
		}
	}
	u.Path = ""
	u.RawPath = ""
	return u.String(), nil
}
func NewDeviceClient(origin, installation string, key ed25519.PrivateKey, allowLoopbackHTTP bool) (*DeviceClient, error) {
	canonical, err := CanonicalOrigin(origin, allowLoopbackHTTP)
	if err != nil {
		return nil, err
	}
	if !validDeviceID(installation) || len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid installation credential")
	}
	u, _ := url.Parse(canonical)
	return &DeviceClient{origin: u, installation: installation, key: bytes.Clone(key), http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// signedMessage uses the exact escaped request path and raw body bytes.
func signedMessage(id, method, target, timestamp, nonce string, body []byte) (string, error) {
	if !validDeviceID(id) || method != strings.ToUpper(method) || (method != "GET" && method != "POST" && method != "PUT") {
		return "", errors.New("invalid signing input")
	}
	u, err := url.ParseRequestURI(target)
	if err != nil || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || !strings.HasPrefix(target, "/v1/device/") || u.EscapedPath() != target {
		return "", errors.New("invalid signed target")
	}
	if strings.ContainsAny(timestamp+nonce, "\r\n") {
		return "", errors.New("invalid signing metadata")
	}
	sum := sha256.Sum256(body)
	return strings.Join([]string{"pharos-device-v1", id, method, target, timestamp, nonce, hex.EncodeToString(sum[:])}, "\n"), nil
}

// Request creates a signed attempt; body is replayed byte-for-byte. The
// returned response must be closed by the caller.
func (c *DeviceClient) Request(ctx context.Context, method, target string, body []byte) (*http.Response, error) {
	// Freeze bytes once so the signed payload and transmitted payload agree
	// even if a caller reuses its buffer while the request is in flight.
	body = bytes.Clone(body)
	var nonceBytes [16]byte
	if _, err := io.ReadFull(rand.Reader, nonceBytes[:]); err != nil {
		return nil, fmt.Errorf("generate request nonce: %w", err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes[:])
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	msg, err := signedMessage(c.installation, method, target, timestamp, nonce, body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.origin.String()+target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Pharos-Installation", c.installation)
	req.Header.Set("X-Pharos-Timestamp", timestamp)
	req.Header.Set("X-Pharos-Nonce", nonce)
	req.Header.Set("X-Pharos-Signature", base64.RawURLEncoding.EncodeToString(ed25519.Sign(c.key, []byte(msg))))
	response, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		response.Body.Close()
		return nil, errors.New("Teams service redirected a signed request")
	}
	return response, nil
}
