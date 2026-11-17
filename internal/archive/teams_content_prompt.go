package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/gbdubs/pharos/internal/teams"
)

type teamsPromptMessage struct {
	Role      string `json:"role"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
}
type teamsPromptEvidence struct {
	Source   string               `json:"source"`
	Messages []teamsPromptMessage `json:"messages"`
}

// teamsPrepareContentPrompt reads only selected, reviewed components, inside a
// single SQLite snapshot after local and shared protection checks. It never
// traverses the interactive Ask history, other libraries, paths or files.
func (c *Catalog) teamsPrepareContentPrompt(ctx context.Context, cfg Config, client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential, reg teamsContentRegistration, manifest teams.CapabilityManifest) (string, error) {
	config, err := teamsLLMFromWork(reg.Work, manifest.AppVersion)
	if err != nil {
		return "", err
	}
	if !teamsContentMatchesUpload(reg.Inventory, reg.Upload) {
		return "", errTeamsSnapshotChanged
	}
	lease, err := c.teamsSharingLease()
	if err != nil {
		return "", err
	}
	defer lease.Close()
	if err = c.teamsRevalidateContent(ctx, cfg, d, credential, reg.Inventory, reg.Selected); err != nil {
		return "", err
	}
	expected, err := teamsContentClosure(reg.Inventory, reg.Selected)
	if err != nil {
		return "", err
	}
	var auth teams.SourceAuthorization
	if json.Unmarshal(reg.Upload.Authorization, &auth) != nil {
		return "", errTeamsConsentStale
	}
	b := reg.Binding
	request := teams.ContentBindRequest{BindingID: b.SnapshotID, SnapshotID: b.SnapshotID, InventoryRevision: b.InventoryRevision, InventorySHA256: b.InventorySHA256, LocalRevision: b.LocalRevision, CatalogRevision: b.CatalogRevision, EvaluationRevision: b.EvaluationRevision, ConfigSHA256: b.ConfigSHA256, PrivacyRevision: b.PrivacyRevision, OwnershipRevision: b.OwnershipRevision, DecisionID: auth.DecisionID, Processing: b.Processing, Components: reg.Selected}
	request.BindingID = reg.Upload.BindingID
	checked, err := client.BindContent(ctx, reg.Work.ID, d.ID, request, expected)
	if err != nil || checked.SHA256 != b.SHA256 {
		return "", errTeamsSnapshotChanged
	}
	tx, err := c.reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var sourceRev, privacyRev int64
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&sourceRev); err != nil {
		return "", err
	}
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&privacyRev); err != nil {
		return "", err
	}
	g, err := readCacheGenerations(ctx, tx)
	if err != nil || sourceRev != reg.Inventory.SourceRevision || privacyRev != reg.Inventory.PrivacyRevision || g.all != reg.Inventory.Generation {
		return "", errTeamsSnapshotChanged
	}
	selected := map[string]bool{}
	for _, v := range reg.Selected {
		selected[v] = true
	}
	evidence := make([]teamsPromptEvidence, 0, len(selected))
	total, count := 0, 0
	for _, candidate := range reg.Inventory.Candidates {
		if !selected[candidate.ComponentToken] {
			continue
		}
		p, e := readTeamsPrivacy(ctx, tx, candidate.ConversationID)
		if e != nil || p.UserPrivate || p.ClassifierState != "clear" || !p.ClassifierReviewed {
			return "", errTeamsConsentStale
		}
		fingerprint, e := teamsManualReviewInput(ctx, tx, candidate.ConversationID)
		if e != nil || fingerprint != candidate.Input || fingerprint != p.ClassifierInput {
			return "", errTeamsSnapshotChanged
		}
		rows, e := tx.QueryContext(ctx, `SELECT role,text,COALESCE(created_at,'') FROM messages WHERE conversation_id=? AND kind='message' AND role IN ('user','assistant') AND selected=1 ORDER BY source_order,native_id LIMIT 1001`, candidate.ConversationID)
		if e != nil {
			return "", e
		}
		// Labels are local to this invocation. Stable opaque source tokens are
		// never sent to the model or made available for it to echo in prose.
		item := teamsPromptEvidence{Source: fmt.Sprintf("Source %d", len(evidence)+1), Messages: []teamsPromptMessage{}}
		for rows.Next() {
			var m teamsPromptMessage
			if e = rows.Scan(&m.Role, &m.Text, &m.CreatedAt); e != nil {
				rows.Close()
				return "", e
			}
			count++
			total += len(m.Text)
			if count > 1000 || total > 96<<10 {
				rows.Close()
				return "", errTeamsSourceUnavailable
			}
			item.Messages = append(item.Messages, m)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return "", e
		}
		evidence = append(evidence, item)
	}
	if len(evidence) != len(selected) {
		return "", errTeamsConsentStale
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	body, err := json.Marshal(struct {
		Question     string                `json:"question"`
		Instructions string                `json:"instructions"`
		Evidence     []teamsPromptEvidence `json:"evidence"`
	}{config.Question, config.Prompt, evidence})
	if err != nil || len(body) > 128<<10 {
		return "", errTeamsSourceUnavailable
	}
	return string(body), nil
}
