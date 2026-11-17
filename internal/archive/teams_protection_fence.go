package archive

import (
	"context"
	"database/sql"
	"fmt"
)

// These triggers are a prototype for the currently inventoried inputs, not a
// production activation mechanism. New proof inputs require an updated fence
// and projection; all existing writers must migrate before installation.
var teamsProtectionFenceInputs = []string{
	"teams_conversation_privacy", "teams_privacy_revision", "teams_protection_baseline", "teams_protection_assertions", "teams_protection_actions", "teams_protection_observations", "teams_protection_recovery_guards",
	"conversations", "conversation_identity_links", "teams_source_bindings", "teams_native_source_proofs", "teams_codex_request_profiles", "teams_codex_capture_custody", "teams_codex_transcript_evidence_v1", "teams_source_revision", "source_states", "source_record_states", "source_item_states", "teams_source_mapping_revisions", "teams_source_mapping_current", "teams_source_mapping_exposure", "teams_local_settings", "teams_protection_enrollment", "teams_protection_policy_ack",
	"teams_v2_uploads", "teams_origin_outbox", "teams_profile_outbox", "teams_profile_outbox_pages", "teams_recovery_stage_batches", "teams_recovery_stage_pages",
}
var teamsProtectionFenceMechanical = []string{"teams_protection_open_contract", "teams_protection_fence_revision", "teams_protection_checkpoint", "teams_protection_flush"}

type teamsFenceStatement struct{ name, sql string }

func teamsProtectionFenceStatements() []teamsFenceStatement {
	var statements []teamsFenceStatement
	for _, group := range []struct {
		tables []string
		count  bool
	}{{teamsProtectionFenceInputs, true}, {teamsProtectionFenceMechanical, false}} {
		for _, table := range group.tables {
			for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
				name := fmt.Sprintf("teams_fence_%s_%s", table, op)
				body := `SELECT pharos_teams_protected_writer_v1();`
				if group.count {
					body += ` UPDATE teams_protection_fence_revision SET revision=revision+1 WHERE id=1 AND revision<9223372036854775807; SELECT CASE WHEN (SELECT revision FROM teams_protection_fence_revision WHERE id=1)>=9223372036854775807 THEN RAISE(ABORT,'protection revision exhausted') END;`
				}
				statements = append(statements, teamsFenceStatement{name, fmt.Sprintf(`CREATE TRIGGER %s BEFORE %s ON %s BEGIN %s END`, name, op, table, body)})
			}
		}
	}

	return statements
}
func teamsInstallProtectionFence(ctx context.Context, tx *sql.Tx) error {
	for _, s := range teamsProtectionFenceStatements() {
		if _, err := tx.ExecContext(ctx, s.sql); err != nil {
			return err
		}
	}
	return teamsVerifyProtectionFence(ctx, tx)
}
func teamsVerifyProtectionFence(ctx context.Context, tx *sql.Tx) error {
	for _, s := range teamsProtectionFenceStatements() {
		var actual string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, s.name).Scan(&actual); err != nil || actual != s.sql {
			return errTeamsProtectionContinuity
		}
	}
	return nil
}
