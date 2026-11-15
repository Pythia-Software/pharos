package archive

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	uifeedback "github.com/gbdubs/pharos/internal/uifeedback/protocol"
)

// The hosted service is the only Pharos-specific transport choice. All wire
// types and the cloud inbox live in internal/uifeedback, without archive imports.
var FeedbackServiceURL = "https://pythia-feedback-iqj5l54y3q-uc.a.run.app"
var feedbackHTTPClient = &http.Client{Timeout: 50 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

type feedbackLogBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *feedbackLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > 2*uifeedback.MaxLogBytes {
		b.data = append([]byte(nil), b.data[len(b.data)-uifeedback.MaxLogBytes:]...)
	}
	return len(p), nil
}
func (b *feedbackLogBuffer) text() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	data := b.data
	if len(data) > uifeedback.MaxLogBytes {
		data = data[len(data)-uifeedback.MaxLogBytes:]
	}
	return redactFeedbackLog(string(data))
}

var feedbackServiceLog feedbackLogBuffer

// CaptureFeedbackServiceLogs mirrors stderr, preserving the native app's log
// stream while retaining only a bounded in-memory tail for an explicit preview.
// It is installed once by the executable, never by catalog/test constructors.
func CaptureFeedbackServiceLogs() func() {
	reader, writer, err := os.Pipe()
	if err != nil {
		return func() {}
	}
	original := os.Stderr
	logger := log.Writer()
	os.Stderr = writer
	log.SetOutput(writer)
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.MultiWriter(original, &feedbackServiceLog), reader)
		_ = reader.Close()
		close(done)
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			log.SetOutput(logger)
			os.Stderr = original
			_ = writer.Close()
			// A shell descendant can inherit stderr and outlive its parent. It must
			// not keep the service or a completed CLI command waiting for EOF.
			select {
			case <-done:
			case <-time.After(500 * time.Millisecond):
				_ = reader.Close()
				<-done
			}
		})
	}
}

var feedbackSecrets = regexp.MustCompile(`(?i)((?:access_token|refresh_token|token|authorization|api[_-]?key|secret|password)["']?\s*[:=]\s*["']?)[^\s&"']+`)
var feedbackBearer = regexp.MustCompile(`(?i)Bearer\s+[^\s"']+`)
var feedbackUserPath = regexp.MustCompile(`/Users/[^/\s]+`)

func redactFeedbackLog(raw string) string {
	value := feedbackUserPath.ReplaceAllString(feedbackSecrets.ReplaceAllString(feedbackBearer.ReplaceAllString(raw, "Bearer [redacted]"), "${1}[redacted]"), "/Users/[redacted]")
	if len(value) > uifeedback.MaxLogBytes {
		value = value[len(value)-uifeedback.MaxLogBytes:]
	}
	return strings.ToValidUTF8(value, "")
}
func (s *Server) feedbackHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == "POST" && r.URL.Path == "/api/feedback/screenshot":
		exportFeedbackScreenshot(w, r)
	case r.Method == "GET" && r.URL.Path == "/api/feedback/diagnostics":
		writeJSON(w, map[string]any{"service": feedbackServiceLog.text()}, 200)
	case r.Method == "POST" && r.URL.Path == "/api/feedback/submit":
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, uifeedback.MaxBodyBytes))
		if err != nil {
			writeJSON(w, map[string]any{"error": "Feedback exceeds the 8 MiB limit."}, 413)
			return
		}
		var submission uifeedback.Submission
		if err = uifeedback.Decode(data, &submission); err == nil {
			err = submission.Validate()
		}
		if err != nil {
			writeJSON(w, map[string]any{"error": fmt.Sprint(err)}, 400)
			return
		}
		endpoint := FeedbackServiceURL
		if configured := os.Getenv("PHAROS_FEEDBACK_URL"); configured != "" {
			endpoint = configured
		}
		u, err := url.Parse(endpoint)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) {
			writeJSON(w, map[string]any{"error": "Feedback service is not configured."}, 503)
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), "POST", strings.TrimRight(endpoint, "/")+"/v1/submissions", bytes.NewReader(data))
		if err != nil {
			writeJSON(w, map[string]any{"error": "Could not prepare feedback."}, 500)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := feedbackHTTPClient.Do(req)
		if err != nil {
			writeJSON(w, map[string]any{"error": "Could not reach the feedback service. Your draft is saved; try again."}, 502)
			return
		}
		defer res.Body.Close()
		if res.StatusCode != 200 && res.StatusCode != 201 {
			writeJSON(w, map[string]any{"error": fmt.Sprintf("Feedback service returned HTTP %d. Your draft is saved; try again.", res.StatusCode)}, res.StatusCode)
			return
		}
		var receipt struct {
			ID       string `json:"id"`
			Received bool   `json:"received"`
		}
		if jsonErr := json.NewDecoder(io.LimitReader(res.Body, 8192)).Decode(&receipt); jsonErr != nil || receipt.ID != submission.ID || !receipt.Received {
			writeJSON(w, map[string]any{"error": "Could not confirm receipt. Your draft is saved; retry to confirm."}, 502)
			return
		}
		writeJSON(w, receipt, res.StatusCode)
	default:
		writeJSON(w, map[string]any{"error": "not found"}, 404)
	}
}

// Exported images outlive the annotation draft (including Clear on Copy), so
// agents can read the copied file link until the OS clears temporary storage.
func exportFeedbackScreenshot(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, (2<<20)+1024))
	if err != nil {
		writeJSON(w, map[string]any{"error": "Screenshot exceeds the export limit."}, 413)
		return
	}
	var screenshot uifeedback.Screenshot
	if err = uifeedback.Decode(data, &screenshot); err == nil {
		err = screenshot.Validate()
	}
	if err != nil {
		writeJSON(w, map[string]any{"error": fmt.Sprint(err)}, 400)
		return
	}
	image, _ := base64.StdEncoding.DecodeString(screenshot.Data) // Validated above.
	dir, err := os.MkdirTemp("", "pharos-annotation-")
	if err != nil {
		writeJSON(w, map[string]any{"error": "Could not save the screenshot to temporary storage. Try again."}, 500)
		return
	}
	extension := ".png"
	if screenshot.MIME == "image/jpeg" {
		extension = ".jpg"
	}
	path := filepath.Join(dir, "screenshot"+extension)
	if err := os.WriteFile(path, image, 0600); err != nil {
		_ = os.RemoveAll(dir)
		writeJSON(w, map[string]any{"error": "Could not save the screenshot to temporary storage. Try again."}, 500)
		return
	}
	link := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	writeJSON(w, map[string]any{"url": link}, 200)
}
