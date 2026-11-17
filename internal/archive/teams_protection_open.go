package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// Bump when Initialize or a migration changes semantics without changing the
// embedded schema. This is not a substitute for reviewing future migrations.
const teamsProtectionInitializerEpoch = 1

type teamsSchemaQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func teamsCompiledSchemaDigest() string {
	sum := sha256.Sum256([]byte(schemaSQL()))
	return hex.EncodeToString(sum[:])
}
func teamsActualSchemaDigest(ctx context.Context, q teamsSchemaQuery) (string, error) {
	rows, err := q.QueryContext(ctx, `SELECT type,name,tbl_name,sql FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY type,name`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	h := sha256.New()
	var length [8]byte
	count, total := 0, 0
	for rows.Next() {
		count++
		if count > 10000 {
			return "", errTeamsProjectionBound
		}
		var fields [4]string
		if err = rows.Scan(&fields[0], &fields[1], &fields[2], &fields[3]); err != nil {
			return "", err
		}
		for _, v := range fields {
			if len(v) > 16<<20 || total > (16<<20)-len(v)-8 {
				return "", errTeamsProjectionBound
			}
			total += len(v) + 8
			binary.BigEndian.PutUint64(length[:], uint64(len(v)))
			h.Write(length[:])
			h.Write([]byte(v))
		}
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func teamsCaptureOpenContract(ctx context.Context, tx *sql.Tx) error {
	if err := teamsVerifyProtectionFence(ctx, tx); err != nil {
		return err
	}
	actual, err := teamsActualSchemaDigest(ctx, tx)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO teams_protection_open_contract(id,initializer_epoch,compiled_schema_sha256,actual_schema_sha256,catalog_version,library_version) VALUES(1,?,?,?,?,?)`, teamsProtectionInitializerEpoch, teamsCompiledSchemaDigest(), actual, protectedCatalogSchemaVersion, libraryVersion())
	return err
}
func teamsOpenContractMatches(ctx context.Context, q teamsSchemaQuery) (bool, error) {
	var epoch, version int
	var compiled, actual, library string
	if err := q.QueryRowContext(ctx, `SELECT initializer_epoch,compiled_schema_sha256,actual_schema_sha256,catalog_version,library_version FROM teams_protection_open_contract WHERE id=1`).Scan(&epoch, &compiled, &actual, &version, &library); err != nil {
		return false, err
	}
	if epoch != teamsProtectionInitializerEpoch || compiled != teamsCompiledSchemaDigest() || version != protectedCatalogSchemaVersion || library != libraryVersion() {
		return false, nil
	}
	var stored string
	if err := q.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&stored); err != nil || stored != strconv.Itoa(protectedCatalogSchemaVersion) {
		return false, err
	}
	current, err := teamsActualSchemaDigest(ctx, q)
	return err == nil && current == actual, err
}

// Recovery operates on an unpublished disposable copy and only removes
// authority. It accepts a different build contract only when the source is a
// supported v34 schema, its stored structural digest still matches, every
// expected fence body is intact, and all protected projection inputs can be
// prepared by this build. It does not make the copy authoritative.
func teamsRecoveryStructureSupported(ctx context.Context, tx *sql.Tx) error {
	var epoch, version int
	var compiled, actual, library string
	if err := tx.QueryRowContext(ctx, `SELECT initializer_epoch,compiled_schema_sha256,actual_schema_sha256,catalog_version,library_version FROM teams_protection_open_contract WHERE id=1`).Scan(&epoch, &compiled, &actual, &version, &library); err != nil {
		return errTeamsProtectionContinuity
	}
	if epoch < 1 || version != protectedCatalogSchemaVersion || !validRecordHash(compiled) || !validRecordHash(actual) || library == "" || len(library) > 512 {
		return errTeamsProtectionContinuity
	}
	current, err := teamsActualSchemaDigest(ctx, tx)
	if err != nil || current != actual {
		return errTeamsProtectionContinuity
	}
	// #794's inactive v34 format predates these acquisition tables and their
	// fences. A disposable copy can be stripped when that exact old shape is
	// present; a partially installed newer shape is not supported.
	newTables := []string{"teams_origin_outbox", "teams_profile_outbox", "teams_profile_outbox_pages", "teams_recovery_stage_batches", "teams_recovery_stage_pages"}
	present := 0
	for _, name := range newTables {
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`, name).Scan(&exists); err != nil {
			return errTeamsProtectionContinuity
		}
		if exists {
			present++
		}
	}
	if present != 0 && present != len(newTables) {
		return errTeamsProtectionContinuity
	}
	legacy := present == 0
	var hasTranscriptEvidence bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='teams_codex_transcript_evidence_v1')`).Scan(&hasTranscriptEvidence); err != nil {
		return errTeamsProtectionContinuity
	}
	if !hasTranscriptEvidence {
		var remaining int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name GLOB 'teams_transcript_*'`).Scan(&remaining); err != nil || remaining != 0 {
			return errTeamsProtectionContinuity
		}
	}
	newFenceTables := append([]string{"teams_v2_uploads"}, newTables...)
	for _, fence := range teamsProtectionFenceStatements() {
		if !hasTranscriptEvidence && teamsFenceBelongsTo(fence.name, []string{"teams_codex_transcript_evidence_v1"}) {
			continue
		}
		if legacy && teamsFenceBelongsTo(fence.name, newFenceTables) {
			continue
		}
		var definition string
		if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, fence.name).Scan(&definition); err != nil || definition != fence.sql {
			return errTeamsProtectionContinuity
		}
	}
	// Compare the tables this recovery transition reads or changes against
	// this executable's supported schema, independently of the copied record.
	// Different builds remain compatible when these structures are identical.
	reference, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return err
	}
	defer reference.Close()
	if _, err = reference.ExecContext(ctx, schemaSQL()); err != nil {
		return err
	}
	for _, name := range []string{"meta", "teams_privacy_revision", "teams_protection_baseline", "teams_protection_assertions", "teams_protection_checkpoint", "teams_protection_open_contract", "teams_protection_observations", "teams_protection_enrollment", "teams_protection_policy_ack"} {
		var expected, got string
		if err = reference.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&expected); err != nil {
			return errTeamsProtectionContinuity
		}
		if err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&got); err != nil || got != expected {
			return errTeamsProtectionContinuity
		}
		// Recovery writes these tables after removing the fences. An extra
		// trigger could otherwise run arbitrary mutations during the transition.
		if err = teamsRecoveryTriggersSupported(ctx, tx, reference, name); err != nil {
			return err
		}
	}
	for _, spec := range teamsProtectionProjectionQueries {
		if !hasTranscriptEvidence && spec.name == "transcript_evidence_v1" {
			continue
		}
		if legacy && (spec.name == "v2_uploads" || spec.name == "origin_outbox" || spec.name == "profile_outbox" || spec.name == "profile_pages" || spec.name == "recovery_batches" || spec.name == "recovery_pages") {
			continue
		}
		rows, e := tx.QueryContext(ctx, spec.query+` LIMIT 0`)
		if e != nil {
			return errTeamsProtectionContinuity
		}
		if e = rows.Close(); e != nil {
			return e
		}
	}
	return nil
}

func teamsFenceBelongsTo(name string, tables []string) bool {
	for _, table := range tables {
		for _, operation := range []string{"INSERT", "UPDATE", "DELETE"} {
			if name == fmt.Sprintf("teams_fence_%s_%s", table, operation) {
				return true
			}
		}
	}
	return false
}

func teamsRecoveryTriggersSupported(ctx context.Context, tx *sql.Tx, reference *sql.DB, table string) error {
	expected := map[string]string{}
	rows, err := reference.QueryContext(ctx, `SELECT name,sql FROM sqlite_master WHERE type='trigger' AND tbl_name=?`, table)
	if err != nil {
		return errTeamsProtectionContinuity
	}
	for rows.Next() {
		var name, definition string
		if rows.Scan(&name, &definition) != nil {
			rows.Close()
			return errTeamsProtectionContinuity
		}
		expected[name] = definition
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return errTeamsProtectionContinuity
	}
	rows.Close()
	for _, fence := range teamsProtectionFenceStatements() {
		if strings.HasPrefix(fence.name, "teams_fence_"+table+"_") {
			expected[fence.name] = fence.sql
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT name,sql FROM sqlite_master WHERE type='trigger' AND tbl_name=?`, table)
	if err != nil {
		return errTeamsProtectionContinuity
	}
	defer rows.Close()
	for rows.Next() {
		var name, definition string
		if rows.Scan(&name, &definition) != nil || expected[name] != definition {
			return errTeamsProtectionContinuity
		}
		delete(expected, name)
	}
	if rows.Err() != nil || len(expected) != 0 {
		return errTeamsProtectionContinuity
	}
	return nil
}
