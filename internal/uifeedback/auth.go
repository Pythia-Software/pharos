package uifeedback

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pythia-software/pythia-auth/libraries/auth-go/auth"
)

const AdminEmail = "grady@pythia.software"

type AuthConfig struct {
	Issuer    string
	Project   string
	Origin    string
	CookieKey []byte // Stable 32-byte secret shared by Cloud Run revisions.
	Client    *http.Client
}
type AdminAuth struct {
	cfg    AuthConfig
	auth   *auth.Auth
	cipher cipher.AEAD
	secure bool
}

func NewAdminAuth(cfg AuthConfig) (*AdminAuth, error) {
	if len(cfg.CookieKey) != 32 || cfg.Project == "" {
		return nil, fmt.Errorf("auth project and 32-byte cookie key are required")
	}
	for _, raw := range []string{cfg.Origin, cfg.Issuer} {
		u, e := url.Parse(raw)
		if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) {
			return nil, fmt.Errorf("auth URLs require HTTPS (or local development HTTP)")
		}
	}
	cfg.Origin = strings.TrimRight(cfg.Origin, "/")
	cfg.Issuer = strings.TrimRight(cfg.Issuer, "/")
	for _, raw := range []string{cfg.Origin, cfg.Issuer} {
		u, _ := url.Parse(raw)
		if u.Path != "" {
			return nil, fmt.Errorf("auth URLs must be bare origins")
		}
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 15 * time.Second}
	}
	block, e := aes.NewCipher(cfg.CookieKey)
	if e != nil {
		return nil, e
	}
	aead, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	return &AdminAuth{cfg: cfg, auth: auth.New(auth.Config{Project: cfg.Project, Issuer: cfg.Issuer, HTTPClient: cfg.Client}), cipher: aead, secure: strings.HasPrefix(cfg.Origin, "https:")}, nil
}
func (a *AdminAuth) seal(name string, value any) string {
	b, _ := json.Marshal(value)
	nonce := make([]byte, a.cipher.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(a.cipher.Seal(nonce, nonce, b, []byte(name)))
}
func (a *AdminAuth) open(r *http.Request, name string, value any) error {
	c, e := r.Cookie(name)
	if e != nil {
		return e
	}
	b, e := base64.RawURLEncoding.DecodeString(c.Value)
	if e != nil || len(b) < a.cipher.NonceSize() {
		return fmt.Errorf("invalid cookie")
	}
	plain, e := a.cipher.Open(nil, b[:a.cipher.NonceSize()], b[a.cipher.NonceSize():], []byte(name))
	if e != nil {
		return e
	}
	return json.Unmarshal(plain, value)
}
func (a *AdminAuth) cookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Secure: a.secure, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: age})
}

type loginState struct {
	State      string    `json:"state"`
	Verifier   string    `json:"verifier"`
	Expires    time.Time `json:"expires"`
	Submission string    `json:"submission,omitempty"`
}

func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func (a *AdminAuth) Login(w http.ResponseWriter, r *http.Request) {
	s := loginState{State: randomToken(), Verifier: randomToken(), Expires: time.Now().Add(10 * time.Minute)}
	if id := r.URL.Query().Get("submission"); validID(id) {
		s.Submission = id
	}
	a.cookie(w, "feedback_login", a.seal("feedback_login", s), 600)
	hash := sha256.Sum256([]byte(s.Verifier))
	q := url.Values{"project": {a.cfg.Project}, "redirect_uri": {a.cfg.Origin + "/auth/callback"}, "state": {s.State}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "code_challenge_method": {"S256"}, "response_type": {"code"}}
	http.Redirect(w, r, a.cfg.Issuer+"/login?"+q.Encode(), http.StatusSeeOther)
}
func (a *AdminAuth) Callback(w http.ResponseWriter, r *http.Request) {
	var state loginState
	if a.open(r, "feedback_login", &state) != nil || time.Now().After(state.Expires) || state.State == "" || subtle.ConstantTimeCompare([]byte(state.State), []byte(r.URL.Query().Get("state"))) != 1 || r.URL.Query().Get("code") == "" {
		http.Error(w, "Sign-in expired. Start again at /auth/login.", 400)
		return
	}
	a.cookie(w, "feedback_login", "", -1)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	form := url.Values{"grant_type": {"authorization_code"}, "code": {r.URL.Query().Get("code")}, "code_verifier": {state.Verifier}, "redirect_uri": {a.cfg.Origin + "/auth/callback"}, "client_id": {a.cfg.Project}}
	req, _ := http.NewRequestWithContext(ctx, "POST", a.cfg.Issuer+"/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, e := a.cfg.Client.Do(req)
	if e != nil {
		http.Error(w, "Sign-in unavailable. Try again.", 502)
		return
	}
	defer res.Body.Close()
	var token struct {
		Access string `json:"access_token"`
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 32<<10)).Decode(&token) != nil || token.Access == "" {
		http.Error(w, "Sign-in failed. Start again at /auth/login.", 401)
		return
	}
	check := r.Clone(ctx)
	check.Header = make(http.Header)
	check.Header.Set("Authorization", "Bearer "+token.Access)
	a.verify(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value := a.seal("feedback_session", token.Access)
		if len(value) > 3800 {
			http.Error(w, "Sign-in token too large", 502)
			return
		}
		a.cookie(w, "feedback_session", value, 900)
		target := "/"
		if validID(state.Submission) {
			target += "?submission=" + state.Submission
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	})).ServeHTTP(w, check)
}
func (a *AdminAuth) verify(next http.Handler) http.Handler {
	return a.auth.Require(auth.AllowPAT(false))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ac, ok := auth.FromContext(r.Context())
		if !ok || ac.SubType != "user" || !strings.EqualFold(ac.Email, AdminEmail) {
			http.Error(w, "This inbox is restricted to its administrator.", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}
func (a *AdminAuth) Protect(next http.Handler) http.Handler {
	verified := a.verify(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != a.cfg.Origin || r.Header.Get("Sec-Fetch-Site") == "cross-site") {
			http.Error(w, "cross-origin request refused", 403)
			return
		}
		if r.Header.Get("Authorization") == "" {
			var token string
			if a.open(r, "feedback_session", &token) != nil {
				if r.URL.Path == "/" {
					target := "/auth/login"
					if id := r.URL.Query().Get("submission"); validID(id) {
						target += "?submission=" + id
					}
					http.Redirect(w, r, target, 303)
				} else {
					http.Error(w, "Sign in at /auth/login", 401)
				}
				return
			}
			r = r.Clone(r.Context())
			r.Header = r.Header.Clone()
			r.Header.Set("Authorization", "Bearer "+token)
		}
		verified.ServeHTTP(w, r)
	})
}
func (a *AdminAuth) Logout(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" && r.Header.Get("Origin") != a.cfg.Origin {
		http.Error(w, "cross-origin request refused", 403)
		return
	}
	a.cookie(w, "feedback_session", "", -1)
	http.Redirect(w, r, "/auth/login", 303)
}

// StorageEvents accepts only Google's signed identity for the dedicated Pub/Sub
// push service account and this exact service audience. User tokens never pass.
func (a *AdminAuth) StorageEvents(email string, next http.Handler) http.Handler {
	return a.auth.Require(auth.AllowPAT(false), auth.AllowGcpIdToken(), auth.Audience(a.cfg.Origin))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ac, ok := auth.FromContext(r.Context())
		if !ok || email == "" || ac.Email != email || ac.SubType != "service" || (ac.Issuer != "https://accounts.google.com" && ac.Issuer != "accounts.google.com") {
			http.Error(w, "forbidden", 403)
			return
		}
		next.ServeHTTP(w, r)
	}))
}
