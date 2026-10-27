package uifeedback

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"
)

const reportID = "4d8d0d0e-270b-4f85-8b75-b01234567890"
const annotationID = "9c0d0d0e-270b-4f85-8b75-b01234567890"

func submission() Submission {
	return Submission{Version: Version, ID: reportID, Application: Application{Name: "Pharos"}, Kind: "bug", Comment: "The filter does not update", Annotations: []Annotation{}}
}
func TestSubmissionValidation(t *testing.T) {
	s := submission()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.Comment = ""
	s.Annotations = []Annotation{{ID: annotationID, Comment: "Rename this", CapturedAt: time.Now(), URL: "http://localhost:8080/library?q=example"}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"javascript:alert(1)", "https://example.test/?token=secret", "https://user:secret@example.test/"} {
		s.Annotations[0].URL = raw
		if s.Validate() == nil {
			t.Fatalf("accepted URL %s", raw)
		}
	}
	s = submission()
	s.Diagnostics = &Diagnostics{Service: strings.Repeat("x", MaxLogBytes+1)}
	if s.Validate() == nil {
		t.Fatal("accepted oversized logs")
	}
	s = submission()
	s.Email = "Display name <user@example.test>"
	if s.Validate() == nil {
		t.Fatal("accepted non-address contact")
	}
	s = submission()
	s.ID = "../other"
	if s.Validate() == nil {
		t.Fatal("accepted traversal ID")
	}
}
func TestScreenshotValidation(t *testing.T) {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 12, 15))); err != nil {
		t.Fatal(err)
	}
	s := Screenshot{MIME: "image/png", Data: base64.StdEncoding.EncodeToString(b.Bytes())}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.MIME = "image/jpeg"
	if s.Validate() == nil {
		t.Fatal("accepted mislabeled raster")
	}
	s.MIME = "image/svg+xml"
	if s.Validate() == nil {
		t.Fatal("accepted active image format")
	}
}
func TestDecodeAndTags(t *testing.T) {
	for _, raw := range []string{`{"version":1,"extra":true}`, `{} {}`, `{} trailing`} {
		var s Submission
		if Decode([]byte(raw), &s) == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	for _, tags := range [][]string{{"fixed", "fixed"}, {""}, {"tag\nother"}, {" space "}} {
		if ValidateTags(tags) == nil {
			t.Fatalf("accepted %#v", tags)
		}
	}
	if err := ValidateTags([]string{"acknowledged", "ui"}); err != nil {
		t.Fatal(err)
	}
}
