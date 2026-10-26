// Package protocol defines the application-independent UI feedback wire types.
// It has no dependency on the inbox server, authentication, storage, or Pharos.
package protocol

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	Version      = 1
	MaxBodyBytes = 8 << 20
	MaxLogBytes  = 64 << 10
	Retention    = 90 * 24 * time.Hour
)

var idPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)

func ValidID(value string) bool { return idPattern.MatchString(value) }

type Application struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type Rect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}
type Viewport struct {
	Width            float64 `json:"width"`
	Height           float64 `json:"height"`
	DevicePixelRatio float64 `json:"device_pixel_ratio"`
}
type Screenshot struct {
	MIME string `json:"mime"`
	Data string `json:"data"` // Base64 raster bytes; never an external URL.
}
type Annotation struct {
	ID         string      `json:"id"`
	Comment    string      `json:"comment"`
	Label      string      `json:"label,omitempty"`
	Selector   string      `json:"selector,omitempty"`
	Text       string      `json:"text,omitempty"`
	URL        string      `json:"url,omitempty"`
	CapturedAt time.Time   `json:"captured_at"`
	Bounds     Rect        `json:"bounds"`
	Viewport   Viewport    `json:"viewport"`
	Screenshot *Screenshot `json:"screenshot,omitempty"`
}
type Diagnostics struct {
	Browser  string `json:"browser,omitempty"`
	Requests string `json:"requests,omitempty"`
	Service  string `json:"service,omitempty"`
}
type Submission struct {
	Version     int          `json:"version"`
	ID          string       `json:"id"`
	Application Application  `json:"application"`
	Kind        string       `json:"kind"`
	Comment     string       `json:"comment"`
	Email       string       `json:"email,omitempty"`
	Annotations []Annotation `json:"annotations"`
	Diagnostics *Diagnostics `json:"diagnostics,omitempty"`
}
type Document struct {
	Submission
	ReceivedAt time.Time `json:"received_at"`
}

// Tags live in GCS custom metadata, separate from the immutable document.
type Entry struct {
	Document
	Tags     []string `json:"tags"`
	Revision string   `json:"revision"`
}

func (s Submission) Validate() error {
	if s.Version != Version || !ValidID(s.ID) {
		return fmt.Errorf("unsupported version or invalid submission ID")
	}
	if strings.TrimSpace(s.Application.Name) == "" || len(s.Application.Name) > 80 || len(s.Application.Version) > 100 {
		return fmt.Errorf("application name is required (maximum 80 bytes)")
	}
	switch s.Kind {
	case "bug", "feature", "prompt", "feedback":
	default:
		return fmt.Errorf("unknown feedback kind")
	}
	if len(s.Comment) > 16000 || len(s.Annotations) > 20 {
		return fmt.Errorf("maximum 20 annotations and 16000 bytes of commentary")
	}
	hasComment := strings.TrimSpace(s.Comment) != ""
	if s.Email != "" {
		a, err := mail.ParseAddress(s.Email)
		if err != nil || a.Address != s.Email || len(s.Email) > 254 {
			return fmt.Errorf("invalid email address")
		}
	}
	seen := map[string]bool{}
	for _, a := range s.Annotations {
		if !ValidID(a.ID) || seen[a.ID] || a.CapturedAt.IsZero() {
			return fmt.Errorf("annotations require unique IDs and capture times")
		}
		seen[a.ID] = true
		if strings.TrimSpace(a.Comment) == "" || len(a.Comment) > 16000 || len(a.Label) > 500 || len(a.Selector) > 2000 || len(a.Text) > 1000 || len(a.URL) > 16000 {
			return fmt.Errorf("annotation commentary is required and fields must fit their limits")
		}
		hasComment = true
		if a.URL != "" {
			u, err := url.Parse(a.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Query().Has("token") {
				return fmt.Errorf("invalid annotation URL (credentials and authentication tokens are excluded)")
			}
		}
		if a.Screenshot != nil {
			if err := a.Screenshot.Validate(); err != nil {
				return err
			}
		}
	}
	if !hasComment {
		return fmt.Errorf("add commentary or an annotation")
	}
	if s.Diagnostics != nil {
		for _, value := range []string{s.Diagnostics.Browser, s.Diagnostics.Requests, s.Diagnostics.Service} {
			if len(value) > MaxLogBytes {
				return fmt.Errorf("each diagnostic log is limited to 64 KiB")
			}
		}
	}
	return nil
}
func (s Screenshot) Validate() error {
	if s.MIME != "image/png" && s.MIME != "image/jpeg" {
		return fmt.Errorf("screenshots must be PNG or JPEG")
	}
	if len(s.Data) > 2<<20 {
		return fmt.Errorf("each screenshot is limited to 1.5 MiB")
	}
	b, err := base64.StdEncoding.DecodeString(s.Data)
	if err != nil {
		return fmt.Errorf("invalid screenshot encoding")
	}
	c, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || "image/"+format != s.MIME || c.Width < 1 || c.Height < 1 || c.Width > 4096 || c.Height > 4096 {
		return fmt.Errorf("invalid screenshot or dimensions exceed 4096 pixels")
	}
	return nil
}
func ValidateTags(tags []string) error {
	if len(tags) > 30 {
		return fmt.Errorf("maximum 30 tags")
	}
	seen := map[string]bool{}
	for _, tag := range tags {
		if strings.TrimSpace(tag) != tag || tag == "" || len(tag) > 80 || strings.ContainsAny(tag, "\r\n\x00") || seen[tag] {
			return fmt.Errorf("tags must be unique, nonempty, and at most 80 bytes")
		}
		seen[tag] = true
	}
	return nil
}
func Decode(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if d.More() {
		return fmt.Errorf("unexpected trailing JSON")
	}
	var extra any
	if err := d.Decode(&extra); err == nil {
		return fmt.Errorf("unexpected trailing JSON")
	} else if err != io.EOF {
		return err
	}
	return nil
}
