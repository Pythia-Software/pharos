package uifeedback

import (
	"context"
	"errors"
	"time"
)

var (
	ErrTooLarge = errors.New("encoded report exceeds the storage limit; remove attachments or split the report")
	ErrNotFound = errors.New("submission not found")
	ErrConflict = errors.New("submission changed; refresh and try again")
)

// Store uses create-if-absent and revision-conditional mutations.
// List returns summaries only; detail payloads must not accumulate across rows.
// Implementations must remove document bytes when Delete succeeds, including attachments.
type Store interface {
	Create(context.Context, Document) (bool, error)
	List(context.Context) ([]Summary, error)
	Get(context.Context, string) (Entry, error)
	SetTags(context.Context, string, string, []string) error
	Delete(context.Context, string, string) error
}

type Summary struct {
	ID          string      `json:"id"`
	Application Application `json:"application"`
	Kind        string      `json:"kind"`
	Comment     string      `json:"comment"`
	Email       string      `json:"email,omitempty"`
	ReceivedAt  time.Time   `json:"received_at"`
	Annotations int         `json:"annotation_count"`
	Screenshots int         `json:"screenshot_count"`
	Diagnostics bool        `json:"has_diagnostics"`
	Tags        []string    `json:"tags"`
	Revision    string      `json:"revision"`
}

// summarize deliberately retains no screenshots, annotations, or log payloads.
func summarize(e Entry) Summary {
	screenshots := 0
	comment := e.Comment
	for _, a := range e.Annotations {
		if a.Screenshot != nil {
			screenshots++
		}
		if comment == "" {
			comment = a.Comment
		}
	}
	return Summary{e.ID, e.Application, e.Kind, comment, e.Email, e.ReceivedAt, len(e.Annotations), screenshots, e.Diagnostics != nil, e.Tags, e.Revision}
}
