package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// devUI serves a checkout's UI against a running Pharos service, so a UI
// change can be tried on a real library without building the app or letting
// branch code open that library's catalog. Pages, assets, and carbon factors
// are read from disk on every request; API calls use the running service with
// its token.
type devUI struct {
	assets string
	token  string
	cookie string
	proxy  *httputil.ReverseProxy
	// Optional, read-only timing snapshot for visual prototypes. Kept outside
	// assets so personal library data is never bundled into a build.
	concurrencyData string
}

func newDevUI(assets string, service *url.URL, token string, port int) *devUI {
	d := &devUI{assets: assets, token: token, cookie: fmt.Sprintf("pharos_dev_token_%d", port)}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(service)
			// devUI has already checked the origin; the service sees a
			// bearer-token client, not the browser.
			for _, name := range []string{"Cookie", "Origin", "Referer", "Sec-Fetch-Site", "Sec-Fetch-Mode", "Sec-Fetch-Dest"} {
				r.Out.Header.Del(name)
			}
			r.Out.Header.Set("Authorization", "Bearer "+token)
			if r.Out.URL.Path == "/api/health/carbon" || r.Out.URL.Path == "/api/query/usage/carbon" || r.Out.URL.Path == "/api/usage/summary" {
				r.Out.Header.Set("Accept-Encoding", "identity")
			}
		},
		ModifyResponse: d.withLocalCarbonFactors,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			writeJSON(w, map[string]any{"error": fmt.Sprintf("Pharos service at %s is unreachable: %v", service, err)}, http.StatusBadGateway)
		},
	}
	d.proxy = proxy
	return d
}

// The installed service supplies live token counts, while the checkout supplies
// the factors and sources used by the UI being previewed.
func (d *devUI) withLocalCarbonFactors(response *http.Response) error {
	field := ""
	switch response.Request.URL.Path {
	case "/api/health/carbon", "/api/query/usage/carbon":
		field = "factors"
	case "/api/usage/summary":
		field = "carbon_factors"
	}
	if field == "" || response.StatusCode != http.StatusOK {
		return nil
	}
	// assets is the checkout's internal/archive/assets; the factors live at
	// the checkout root.
	factors, err := os.ReadFile(filepath.Join(d.assets, "..", "..", "carbon", "co2_factors.json"))
	if err != nil {
		return fmt.Errorf("read local carbon factors: %w", err)
	}
	if !json.Valid(factors) {
		return fmt.Errorf("local carbon factors are invalid JSON")
	}
	defer response.Body.Close()
	var body map[string]json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return fmt.Errorf("decode carbon API response: %w", err)
	}
	body[field] = factors
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode carbon API response: %w", err)
	}
	response.Body = io.NopCloser(bytes.NewReader(payload))
	response.ContentLength = int64(len(payload))
	response.Header.Set("Content-Length", strconv.Itoa(len(payload)))
	response.Header.Del("ETag")
	return nil
}

// ServeHTTP applies the service's own authorization and same-origin rules,
// since requests forwarded from here carry its token.
func (d *devUI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && isUIPage(r.URL.Path) && r.URL.Query().Get("token") != "" && secureEqual(r.URL.Query().Get("token"), d.token) {
		http.SetCookie(w, &http.Cookie{Name: d.cookie, Value: url.QueryEscape(d.token), Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		query := r.URL.Query()
		query.Del("token")
		target := &url.URL{Path: r.URL.Path, RawQuery: query.Encode()}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, target.String(), http.StatusSeeOther)
		return
	}
	cookie, err := r.Cookie(d.cookie)
	if err != nil || !secureEqual(cookie.Value, url.QueryEscape(d.token)) {
		writeJSON(w, map[string]any{"error": "authentication required"}, http.StatusUnauthorized)
		return
	}
	if (r.Method != http.MethodGet || strings.HasPrefix(r.URL.Path, "/api/")) && !sameOrigin(r) {
		writeJSON(w, map[string]any{"error": "cross-origin request refused"}, http.StatusForbidden)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/usage/concurrency-prototype" && d.concurrencyData != "":
		payload, err := os.ReadFile(d.concurrencyData)
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(payload)
	case r.Method == http.MethodGet && isUIPage(r.URL.Path):
		source, err := os.ReadFile(filepath.Join(d.assets, "ui.py"))
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		// Tell this tab apart from the installed app's.
		html := strings.Replace(uiHTML(source), "<title>Pharos</title>", "<title>Pharos (dev)</title>", 1)
		if d.concurrencyData != "" {
			html = strings.Replace(html, "<html", "<html data-concurrency-prototype", 1)
		}
		// Dev previews open with annotation controls, without changing the
		// library's optional-button preferences used by the installed app.
		html = strings.Replace(html, "</body>", "<script>if(typeof setFeedbackEnabled==='function'){document.querySelector('#feedbackToggle').hidden=false;setFeedbackEnabled(true)}</script></body>", 1)
		writeHTML(w, html)
	case r.Method == http.MethodGet && uiAssets[r.URL.Path].name != "":
		asset := uiAssets[r.URL.Path]
		payload, err := os.ReadFile(filepath.Join(d.assets, asset.name))
		writeAsset(w, asset.name, payload, err, asset.contentType)
	default:
		d.proxy.ServeHTTP(w, r)
	}
}

func runDevUICLI(config Config, args []string) error {
	flags := flag.NewFlagSet("dev-ui", flag.ContinueOnError)
	port := flags.Int("port", 8799, "")
	assets := flags.String("assets", filepath.Join("internal", "archive", "assets"), "")
	noOpen := flags.Bool("no-open", false, "")
	openPath := flags.String("open-path", "/", "Local UI page to open after signing in, including query filters")
	concurrencyData := flags.String("concurrency-data", "", "Read-only concurrency prototype JSON produced by tools/concurrency-prototype.py")
	if err := flags.Parse(args); err != nil {
		return err
	}
	page, err := url.Parse(*openPath)
	if err != nil || page.IsAbs() || page.Host != "" || page.User != nil || !isUIPage(page.Path) {
		return fmt.Errorf("--open-path must be a local UI page, such as /usage?machine_view=time")
	}
	if _, err := os.Stat(filepath.Join(*assets, "ui.py")); err != nil {
		return fmt.Errorf("no ui.py in %s; run from the repository root or pass --assets DIR", *assets)
	}
	if *port == config.Port {
		return fmt.Errorf("--port %d is the service's own port; choose another", *port)
	}
	service := &url.URL{Scheme: "http", Host: net.JoinHostPort(config.Host, strconv.Itoa(config.Port))}
	if err := pingService(service, config.APIToken); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v; API calls will fail until it runs.\n", err)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(*port)))
	if err != nil {
		return err
	}
	defer listener.Close()
	page.Scheme = "http"
	page.Host = net.JoinHostPort("127.0.0.1", strconv.Itoa(*port))
	query := page.Query()
	query.Set("token", config.APIToken)
	page.RawQuery = query.Encode()
	target := page.String()
	fmt.Printf("Serving the UI in %s against %s\n%s\n", *assets, service, target)
	handler := newDevUI(*assets, service, config.APIToken, *port)
	if *concurrencyData != "" {
		data, err := os.ReadFile(*concurrencyData)
		if err != nil {
			return fmt.Errorf("read concurrency prototype: %w", err)
		}
		if !json.Valid(data) {
			return fmt.Errorf("concurrency prototype must contain valid JSON")
		}
		handler.concurrencyData = *concurrencyData
	}
	if !*noOpen {
		_ = exec.Command("open", target).Run()
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	return server.Serve(listener)
}

func pingService(service *url.URL, token string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, service.String()+"/api/ready", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("Pharos service at %s is not running", service)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Pharos service at %s answered %s; is this its configuration?", service, response.Status)
	}
	return nil
}
