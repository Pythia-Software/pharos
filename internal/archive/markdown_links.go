package archive

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Link checks run on this Mac: browsers cannot stat a file or reliably check
// another localhost origin. Never follow a local redirect or send credentials.
var markdownLocalClient = &http.Client{
	Timeout:       2 * time.Second,
	Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

var markdownLineSuffix = regexp.MustCompile(`:\d+(?::\d+)?$`)

func markdownLocalURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return nil, false
	}
	host := strings.ToLower(u.Hostname())
	ip := net.ParseIP(host)
	if host != "localhost" && !strings.HasSuffix(host, ".localhost") && !(ip != nil && (ip.IsLoopback() || ip.IsUnspecified())) {
		return nil, false
	}
	// Resolve localhost ourselves, including subdomains, so DNS can never
	// redirect this local-only probe onto the network.
	if ip == nil || ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	if port := u.Port(); port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	u.Fragment = ""
	return u, true
}

func (s *Server) markdownFile(raw, workspace string) (*os.File, error) {
	if len(raw) > 8192 || strings.ContainsAny(raw, "\x00\r\n\\") {
		return nil, fmt.Errorf("invalid file path")
	}
	// A bare filename with an editor line suffix otherwise looks like a URL scheme.
	if !strings.ContainsAny(raw, "/") && strings.Contains(strings.Split(raw, ":")[0], ".") {
		raw = markdownLineSuffix.ReplaceAllString(raw, "")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" && u.Scheme != "file" || u.Host != "" && u.Host != "localhost" || u.RawQuery != "" || u.User != nil {
		return nil, fmt.Errorf("not a local file")
	}
	path := markdownLineSuffix.ReplaceAllString(u.Path, "")
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) {
		var root *string
		if workspace == "" || s.Catalog.reader().QueryRow(`SELECT location FROM workspaces WHERE id=?`, workspace).Scan(&root) != nil || root == nil || !filepath.IsAbs(*root) {
			return nil, fmt.Errorf("workspace location unavailable on this Mac")
		}
		path = filepath.Join(*root, path)
	}
	// Stat before opening: FIFOs and devices must never block a page load.
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("file unavailable on this Mac")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("not a regular file")
	}
	return file, nil
}

func (s *Server) markdownLink(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	raw, workspace := q.Get("href"), q.Get("workspace")
	w.Header().Set("Cache-Control", "no-store")
	if q.Get("open") == "1" {
		file, err := s.markdownFile(raw, workspace)
		if err != nil {
			writeError(w, err, http.StatusNotFound)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			writeError(w, err, http.StatusNotFound)
			return
		}
		// Local HTML/SVG/JS must not acquire Pharos's origin. Text opens in
		// the browser; binary files download through the normal browser flow.
		prefix := make([]byte, 512)
		n, _ := file.ReadAt(prefix, 0)
		if utf8.Valid(prefix[:n]) && !strings.ContainsRune(string(prefix[:n]), 0) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		} else {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": info.Name()}))
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		http.ServeContent(w, r, info.Name(), info.ModTime(), file)
		return
	}
	result := map[string]any{"status": "unavailable", "detail": "File unavailable on this Mac"}
	if strings.HasPrefix(strings.ToLower(raw), "http:") || strings.HasPrefix(strings.ToLower(raw), "https:") {
		u, ok := markdownLocalURL(raw)
		result["detail"] = "Local server unavailable on this Mac"
		if !ok {
			writeJSON(w, result, http.StatusOK)
			return
		}
		request, err := http.NewRequestWithContext(r.Context(), http.MethodHead, u.String(), nil)
		if err == nil {
			original, _ := url.Parse(raw)
			request.Host = original.Host
			response, err := markdownLocalClient.Do(request)
			if err == nil {
				response.Body.Close()
				result["detail"] = fmt.Sprintf("Local server returned HTTP %d", response.StatusCode)
				if response.StatusCode < 400 || response.StatusCode == 401 || response.StatusCode == 403 || response.StatusCode == 405 {
					result["status"] = "server"
				}
			}
		}
	} else if file, err := s.markdownFile(raw, workspace); err == nil {
		file.Close()
		result["status"], result["detail"] = "file", "File available on this Mac"
		result["href"] = "/api/markdown-link?" + url.Values{"href": {raw}, "workspace": {workspace}, "open": {"1"}}.Encode()
	}
	writeJSON(w, result, http.StatusOK)
}

// Fetch only the domain's public HTTPS favicon. DNS is checked at dial time,
// redirects are refused, and no page URL, cookies or credentials are forwarded.
var markdownFaviconClient = &http.Client{
	Timeout:       3 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport: &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, address := range addresses {
			if !markdownPublicIP(address.IP) {
				return nil, fmt.Errorf("not a public host")
			}
		}
		for _, address := range addresses {
			connection, err := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
			if err == nil {
				return connection, nil
			}
		}
		return nil, fmt.Errorf("host unavailable")
	}},
}

func markdownPublicIP(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
}

func (s *Server) markdownFavicon(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Query().Get("host")
	fallback := func() {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "private, max-age=300")
		io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="#999" stroke-width="1.7"><circle cx="12" cy="12" r="9"/><ellipse cx="12" cy="12" rx="4" ry="9"/><path d="M3 12h18"/></svg>`)
	}
	if len(host) > 253 || !strings.Contains(host, ".") || strings.ContainsAny(host, "/\\:@?#%\x00\r\n ") {
		fallback()
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "https://"+host+"/favicon.ico", nil)
	if err != nil {
		fallback()
		return
	}
	response, err := markdownFaviconClient.Do(request)
	if err != nil {
		fallback()
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		fallback()
		return
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (128<<10)+1))
	if err != nil || len(data) > 128<<10 {
		fallback()
		return
	}
	kind := http.DetectContentType(data)
	if kind != "image/png" && kind != "image/jpeg" && kind != "image/gif" && kind != "image/webp" && kind != "image/x-icon" && kind != "image/vnd.microsoft.icon" {
		fallback()
		return
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(data)
}
