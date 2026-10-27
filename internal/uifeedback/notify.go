package uifeedback

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"sync"
	"time"
)

// Email uses the same Twilio Email API contract as Explo's emailnotify package.
// Only report IDs and private inbox links are emailed; attachments and contact
// details remain in the authenticated inbox.
type Email struct {
	KeySID    string
	KeySecret string
	From      string
	Origin    string
	Client    *http.Client
	Endpoint  string
}

func (e Email) Send(ctx context.Context, entry Entry) error {
	if e.KeySID == "" || e.KeySecret == "" || e.From == "" {
		return fmt.Errorf("email notification is not configured")
	}
	target := e.Endpoint
	if target == "" {
		target = "https://comms.twilio.com/v1/Emails"
	}
	client := e.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	link := e.Origin + "/?submission=" + entry.ID
	payload := map[string]any{"from": map[string]string{"address": e.From, "name": "Feedback inbox"}, "to": []map[string]string{{"address": AdminEmail}}, "content": map[string]string{"subject": "New UI feedback · " + entry.ID, "html": "<p>A new feedback submission is ready.</p><p><a href=\"" + html.EscapeString(link) + "\">Open the private inbox</a></p>", "text": "A new feedback submission is ready: " + link}}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, "POST", target, bytes.NewReader(b))
	req.SetBasicAuth(e.KeySID, e.KeySecret)
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("email notification request failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("email notification returned HTTP %d", res.StatusCode)
	}
	return nil
}

// Notification deliveries are at least once. The receipt suppresses completed
// retries; a crash between provider acceptance and receipt storage can duplicate
// an email. The mutex also coalesces simultaneous deliveries in one instance.
type Notifications struct {
	Store *GCS
	Email Email
	mu    sync.Mutex
}

func (n *Notifications) Handle(ctx context.Context, id, generation string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	o, err := n.Store.metadata(ctx, id)
	if err != nil {
		return err
	}
	if o.Generation != generation || o.Metadata["notified"] == generation {
		return nil
	}
	entry, err := n.Store.entry(ctx, id, o)
	if err != nil {
		return err
	}
	if err = n.Email.Send(ctx, entry); err != nil {
		return err
	}
	// A concurrent tag edit must not erase the notification receipt, nor should
	// it cause an already accepted email to be sent again during this request.
	for attempt := 0; attempt < 4; attempt++ {
		err = n.Store.patch(ctx, id, o.revision(), map[string]string{"notified": generation})
		if err != ErrConflict {
			return err
		}
		o, err = n.Store.metadata(ctx, id)
		if err != nil {
			return err
		}
		if o.Generation != generation {
			return nil
		}
	}
	return ErrConflict
}
