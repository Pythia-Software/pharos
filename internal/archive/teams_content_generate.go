package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
)

type teamsArtifactPayload struct {
	Outcome    string    `json:"outcome"`
	Form       string    `json:"form"`
	ObservedAt time.Time `json:"observed_at"`
	ComputedAt time.Time `json:"computed_at"`
	Artifact   struct {
		Answer     json.RawMessage         `json:"answer"`
		Sources    []string                `json:"sources"`
		Processing teams.ContentProcessing `json:"processing"`
	} `json:"artifact"`
}

// Generate only after a local source grant, exact content binding and separate
// account/model processing permission. Each start is consumed durably before
// the one permitted prompt handoff and never reused after a crash or failure.
func (s *Server) generateTeamsContent(ctx context.Context, cfg Config, client *teams.DeviceClient, d LibraryDescriptor, credential *teams.EnrollmentCredential, reg teamsContentRegistration, manifest teams.CapabilityManifest) error {
	config, err := teamsLLMFromWork(reg.Work, manifest.AppVersion)
	if err != nil {
		return err
	}
	selection, account, err := s.Catalog.loadTeamsModelPermission(ctx, d, credential)
	if err != nil {
		return err
	}
	if reg.Binding.Processing.Execution != "local" || reg.Binding.Processing.Provider != selection.Provider || reg.Binding.Processing.Model != selection.Model || reg.Binding.Processing.Region != "" || reg.Binding.Processing.Route != "" {
		return errTeamsConsentStale
	}
	prompt, err := s.Catalog.teamsPrepareContentPrompt(ctx, cfg, client, d, credential, reg, manifest)
	if err != nil {
		return err
	}
	var provider askProvider
	for _, p := range askInstalled() {
		if p.ID == "Claude" {
			provider = p
			break
		}
	}
	if provider.path == "" {
		return errTeamsClaudeIsolation
	}
	provider.Models = []askModel{{ID: selection.Model, Efforts: []string{selection.Effort}}}
	stream, err := teamsOpenClaude(ctx, provider, selection, config.ResultSchema.JSONSchema, account)
	if err != nil {
		return fmt.Errorf("model preflight: %w", err)
	}
	defer stream.stop()
	startID := ""
	var startedAt time.Time
	disclosureStage := "local revalidation"
	begin := func(turnCtx context.Context) (func(), error) {
		bounded, cancel := context.WithTimeout(turnCtx, 15*time.Second)
		defer cancel()
		turnCtx = bounded
		lease, e := s.Catalog.teamsSharingLease()
		if e != nil {
			return nil, e
		}
		failed := true
		defer func() {
			if failed {
				lease.Close()
			}
		}()
		if e = s.Catalog.teamsRevalidateContent(turnCtx, cfg, d, credential, reg.Inventory, reg.Selected); e != nil {
			return nil, e
		}
		disclosureStage = "model permission"
		currentSelection, currentAccount, e := s.Catalog.loadTeamsModelPermission(turnCtx, d, credential)
		if e != nil || currentSelection != selection || currentAccount != account {
			return nil, errTeamsConsentStale
		}
		expected, e := teamsContentClosure(reg.Inventory, reg.Selected)
		if e != nil {
			return nil, e
		}
		var sourceAuth teams.SourceAuthorization
		if json.Unmarshal(reg.Upload.Authorization, &sourceAuth) != nil {
			return nil, errTeamsConsentStale
		}
		b := reg.Binding
		req := teams.ContentBindRequest{BindingID: reg.Upload.BindingID, SnapshotID: b.SnapshotID, InventoryRevision: b.InventoryRevision, InventorySHA256: b.InventorySHA256, LocalRevision: b.LocalRevision, CatalogRevision: b.CatalogRevision, EvaluationRevision: b.EvaluationRevision, ConfigSHA256: b.ConfigSHA256, PrivacyRevision: b.PrivacyRevision, OwnershipRevision: b.OwnershipRevision, DecisionID: sourceAuth.DecisionID, Processing: b.Processing, Components: reg.Selected}
		disclosureStage = "binding refresh"
		checked, e := client.BindContent(turnCtx, reg.Work.ID, d.ID, req, expected)
		if e != nil || checked.SHA256 != b.SHA256 {
			return nil, errTeamsSnapshotChanged
		}
		disclosureStage = "start reservation"
		startID = randomUUID()
		e = s.Catalog.writeTransaction(turnCtx, "teams-content-consume-start", func(tx *sql.Tx) error {
			settings, e := readTeamsSettings(turnCtx, tx)
			if e != nil {
				return e
			}
			var privacy, source int64
			if e = tx.QueryRowContext(turnCtx, `SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&privacy); e != nil {
				return e
			}
			if e = tx.QueryRowContext(turnCtx, `SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&source); e != nil {
				return e
			}
			generation, e := readCacheGenerations(turnCtx, tx)
			if e != nil || !settings.Enabled || settings.SettingsRevision != reg.Upload.SettingsRevision || privacy != reg.Inventory.PrivacyRevision || source != reg.Inventory.SourceRevision || generation.all != reg.Inventory.Generation {
				return errTeamsSnapshotChanged
			}
			res, e := tx.ExecContext(turnCtx, `UPDATE teams_content_runs SET state='started',start_id=? WHERE work_id=? AND state='bound' AND binding_json=?`, startID, reg.Work.ID, reg.Upload.BindingJSON)
			if e != nil {
				return e
			}
			n, e := res.RowsAffected()
			if e != nil || n != 1 {
				return errTeamsConsentStale
			}
			return nil
		})
		if e != nil {
			return nil, e
		}
		receipt, e := client.StartContentDisclosure(turnCtx, reg.Work.ID, teams.ContentStartRequest{StartID: startID, BindingID: reg.Upload.BindingID, BindingSHA256: b.SHA256})
		if e != nil {
			return nil, e
		}
		startedAt = receipt.StartedAt
		e = s.Catalog.writeTransaction(turnCtx, "teams-content-started", func(tx *sql.Tx) error {
			_, e := tx.ExecContext(turnCtx, `UPDATE teams_content_runs SET started_at=? WHERE work_id=? AND state='started' AND start_id=?`, startedAt.UTC().Format(time.RFC3339Nano), reg.Work.ID, startID)
			return e
		})
		if e != nil {
			return nil, e
		}
		disclosureStage = "prompt handoff"
		failed = false
		timer := time.AfterFunc(15*time.Second, stream.stop)
		return func() { timer.Stop(); lease.Close() }, nil
	}
	runCtx, stopRun := context.WithCancel(ctx)
	monitorDone := make(chan struct{})
	if !s.spawnRegistered(nil, func() {
		defer close(monitorDone)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		checks := 0
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				settings, e := readTeamsSettings(runCtx, s.Catalog.reader())
				var privacy, source int64
				if e == nil {
					e = s.Catalog.reader().QueryRowContext(runCtx, `SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&privacy)
				}
				if e == nil {
					e = s.Catalog.reader().QueryRowContext(runCtx, `SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&source)
				}
				if e != nil || !settings.Enabled || settings.SettingsRevision != reg.Upload.SettingsRevision || privacy != reg.Inventory.PrivacyRevision || source != reg.Inventory.SourceRevision {
					stopRun()
					return
				}
				checks++
				if checks%5 == 0 {
					expected, e := teamsContentClosure(reg.Inventory, reg.Selected)
					var a teams.SourceAuthorization
					if e != nil || json.Unmarshal(reg.Upload.Authorization, &a) != nil {
						stopRun()
						return
					}
					b := reg.Binding
					req := teams.ContentBindRequest{BindingID: reg.Upload.BindingID, SnapshotID: b.SnapshotID, InventoryRevision: b.InventoryRevision, InventorySHA256: b.InventorySHA256, LocalRevision: b.LocalRevision, CatalogRevision: b.CatalogRevision, EvaluationRevision: b.EvaluationRevision, ConfigSHA256: b.ConfigSHA256, PrivacyRevision: b.PrivacyRevision, OwnershipRevision: b.OwnershipRevision, DecisionID: a.DecisionID, Processing: b.Processing, Components: reg.Selected}
					checkCtx, cancel := context.WithTimeout(runCtx, 5*time.Second)
					current, e := client.BindContent(checkCtx, reg.Work.ID, d.ID, req, expected)
					cancel()
					if e != nil || current.SHA256 != b.SHA256 {
						stopRun()
						return
					}
				}
			}
		}
	}) {
		stopRun()
		return errServiceStopping
	}
	defer func() { stopRun(); <-monitorDone }()
	answer, err := stream.turn(runCtx, prompt, begin)
	if err != nil {
		return fmt.Errorf("model turn (%s): %w", disclosureStage, err)
	}
	if teamsAnswerContainsSourceIdentity(answer, reg.Inventory) {
		return errTeamsConsentStale
	}
	payload := teamsArtifactPayload{Outcome: "completed", Form: "artifact", ObservedAt: reg.Inventory.ObservedAt, ComputedAt: time.Now().UTC()}
	payload.Artifact.Answer = answer
	payload.Artifact.Sources = append([]string{}, reg.Selected...)
	payload.Artifact.Processing = reg.Binding.Processing
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	hash := teamsPayloadHash(raw)
	lease, err := s.Catalog.teamsSharingLease()
	if err != nil {
		return err
	}
	defer lease.Close()
	if err = s.Catalog.teamsRevalidateContent(ctx, cfg, d, credential, reg.Inventory, reg.Selected); err != nil {
		return err
	}
	return s.Catalog.writeTransaction(ctx, "teams-content-draft", func(tx *sql.Tx) error {
		res, e := tx.ExecContext(ctx, `UPDATE teams_content_runs SET payload=?,payload_sha256=?,state='awaiting_review' WHERE work_id=? AND state='started' AND start_id=? AND started_at=?`, raw, hash, reg.Work.ID, startID, startedAt.UTC().Format(time.RFC3339Nano))
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil || n != 1 {
			return errTeamsConsentStale
		}
		return nil
	})
}

// The response can quote only invocation-local labels. A model output that
// contains any stable source token or witness is withheld even if it used a
// JSON escape to spell it.
func teamsAnswerContainsSourceIdentity(raw []byte, inventory teamsContentInventory) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return true
	}
	var visit func(any) bool
	visit = func(v any) bool {
		switch x := v.(type) {
		case string:
			for _, item := range inventory.Items {
				if (item.Token != "" && strings.Contains(x, item.Token)) || (item.Witness != "" && strings.Contains(x, item.Witness)) {
					return true
				}
			}
		case []any:
			for _, item := range x {
				if visit(item) {
					return true
				}
			}
		case map[string]any:
			for k, item := range x {
				if visit(k) || visit(item) {
					return true
				}
			}
		}
		return false
	}
	return visit(value)
}
