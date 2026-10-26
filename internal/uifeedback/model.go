package uifeedback

import "github.com/gbdubs/pharos/internal/uifeedback/protocol"

// Re-export the protocol at the server boundary. Application adapters import
// protocol directly, keeping cloud/auth dependencies out of their executables.
const (
	Version      = protocol.Version
	MaxBodyBytes = protocol.MaxBodyBytes
	MaxLogBytes  = protocol.MaxLogBytes
	Retention    = protocol.Retention
)

type Application = protocol.Application
type Rect = protocol.Rect
type Viewport = protocol.Viewport
type Screenshot = protocol.Screenshot
type Annotation = protocol.Annotation
type Diagnostics = protocol.Diagnostics
type Submission = protocol.Submission
type Document = protocol.Document
type Entry = protocol.Entry

func validID(value string) bool            { return protocol.ValidID(value) }
func ValidateTags(tags []string) error     { return protocol.ValidateTags(tags) }
func Decode(data []byte, target any) error { return protocol.Decode(data, target) }
