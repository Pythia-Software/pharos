package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"math"
	"strconv"
	"strings"
)

// This bounded metadata projection is not an activation predicate by itself:
// the writer fence, policy schema and all supported producer inputs must first
// be inventoried and verified. In particular it deliberately excludes message
// bodies and request facts_json.
var teamsProtectionProjectionQueries = []struct{ name, query string }{
	{"schema_version", `SELECT key,value FROM meta WHERE key='schema_version' ORDER BY key`},
	{"open_contract", `SELECT id,initializer_epoch,compiled_schema_sha256,actual_schema_sha256,catalog_version,library_version FROM teams_protection_open_contract ORDER BY id`},
	{"fence_revision", `SELECT id,revision FROM teams_protection_fence_revision ORDER BY id`},
	{"privacy", `SELECT conversation_id,provider,account,native_id,user_private,classifier_state,classifier_input,classifier_reviewed FROM teams_conversation_privacy ORDER BY conversation_id`},
	{"privacy_revision", `SELECT id,revision FROM teams_privacy_revision ORDER BY id`},
	{"baseline", `SELECT id,library_id,instance_id,state,generation,format_version,producer_version,policy_sha256,inventory_sha256,proposal_sha256,source_revision,privacy_revision,mapping_revision,acknowledged_policy FROM teams_protection_baseline ORDER BY id`},
	{"assertions", `SELECT assertion_id,conversation_id,provider,account,native_id,kind,evidence,policy_sha256,active,created_revision,retired_revision,retirement_evidence,retired_by_action_id FROM teams_protection_assertions ORDER BY assertion_id`},
	{"recovery_guards", `SELECT kind,scope,commitment FROM teams_protection_recovery_guards ORDER BY kind,scope,commitment`},
	{"actions", `SELECT action_id,conversation_id,provider,account,native_id,kind,observed_revision,retired_count FROM teams_protection_actions ORDER BY action_id`},
	{"observations", `SELECT binding_id,conversation_id,origin,baseline_generation,decision_revision,indexed_digest,evidence_sha256,capture_sha256,capture_revision,provenance_sha256,dependency_sha256,source_revision,privacy_revision,mapping_revision,policy_sha256,decision,decision_origin FROM teams_protection_observations ORDER BY binding_id,conversation_id,origin`},
	{"conversations", `SELECT id,workspace_id,provider,account,native_id,parent_id,agent_depth,origin,origin_host_id,coverage FROM conversations ORDER BY id`},
	{"links", `SELECT left_id,right_id,relationship,confidence FROM conversation_identity_links ORDER BY left_id,right_id,relationship`},
	{"source_bindings", `SELECT binding_id,provenance_sha256,host_id,source_name,source_kind,provider,account_label,source_root,superseded_at FROM teams_source_bindings ORDER BY binding_id`},
	{"native_proofs", `SELECT binding_id,conversation_id,origin,provider,native_id,status,trailing_partial,indexed_digest,total_tokens,total_digest FROM teams_native_source_proofs ORDER BY binding_id,conversation_id,origin`},
	{"profiles", `SELECT binding_id,conversation_id,origin,indexed_digest,evidence_sha256 FROM teams_codex_request_profiles ORDER BY binding_id,conversation_id,origin`},
	{"custody", `SELECT binding_id,conversation_id,origin,indexed_digest,evidence_sha256,capture_sha256,captured_at,capture_path,provenance_sha256,evidence_identity,capture_revision FROM teams_codex_capture_custody ORDER BY binding_id,conversation_id,origin`},
	{"transcript_evidence_v1", `SELECT binding_id,conversation_id,origin,indexed_digest,capture_sha256,capture_revision,transcript_sha256,review_input,content_matched,gates_version FROM teams_codex_transcript_evidence_v1 ORDER BY binding_id,conversation_id,origin`},
	{"source_states", `SELECT host_id,source_name,kind,capability,cursor,fingerprint,index_version,coverage,pending_count FROM source_states ORDER BY host_id,source_name`},
	{"source_record_states", `SELECT host_id,source_name,source_kind,source_account,source_id,digest FROM source_record_states ORDER BY host_id,source_name,source_kind,source_account,source_id`},
	{"source_item_states", `SELECT host_id,source_name,item,extractor,size,version,signal FROM source_item_states ORDER BY host_id,source_name,item`},
	{"source_revision", `SELECT id,revision,proof_requested FROM teams_source_revision ORDER BY id`},
	{"mapping_revisions", `SELECT revision,binding_id,library_id,instance_id,origin,org_id,person_id,installation_id,provider,mode,reviewed_at FROM teams_source_mapping_revisions ORDER BY revision`},
	{"mapping_current", `SELECT binding_id,revision FROM teams_source_mapping_current ORDER BY binding_id`},
	{"mapping_exposure", `SELECT binding_id,mapping_revision,exposed_at FROM teams_source_mapping_exposure ORDER BY binding_id`},
	{"enrollment", `SELECT id,generation FROM teams_protection_enrollment ORDER BY id`},
	{"policy_ack", `SELECT id,policy_id,policy_sha256,origin,org_id,person_id,installation_id,library_id,instance_id,enrollment_generation,settings_revision,acknowledged_at FROM teams_protection_policy_ack ORDER BY id`},
	{"v2_uploads", `SELECT work_id,version_id,library_id,instance_id,org_id,installation_id,config_sha256,snapshot_id,binding_id,inventory_revision,inventory_sha256,local_revision,evaluation_revision,settings_revision,source_revision,privacy_revision,generation,mode,policy_version,authorization_json,pages_json,seal_json,sealed_json,binding_json,assignments_json,state FROM teams_v2_uploads ORDER BY work_id`},
	{"origin_outbox", `SELECT observation_id,origin,org_id,person_id,installation_id,library_id,instance_id,work_id,snapshot_id,inventory_sha256,settings_revision,source_revision,privacy_revision,cache_generation,run_id,run_generation,expires_at,token,expected_revision,observation_json,observation_sha256,state,receipt_json FROM teams_origin_outbox ORDER BY observation_id`},
	{"profile_outbox", `SELECT upload_id,origin,org_id,person_id,installation_id,library_id,instance_id,work_id,settings_revision,source_revision,privacy_revision,snapshot_id,inventory_sha256,profile_revision,issued_at,expires_at,seal_json,seal_sha256,state,seal_receipt_json FROM teams_profile_outbox ORDER BY upload_id`},
	{"profile_pages", `SELECT upload_id,page_index,page_json,page_sha256,receipt_json FROM teams_profile_outbox_pages ORDER BY upload_id,page_index`},
	{"recovery_batches", `SELECT run_id,installation_id,upload_id,org_id,person_id,library_id,instance_id,generation,issued_at,expires_at,settings_revision,source_revision,privacy_revision,prior_profile_revision,prior_profile_sha256,state FROM teams_recovery_stage_batches ORDER BY run_id,installation_id,upload_id`},
	{"recovery_pages", `SELECT run_id,installation_id,upload_id,page_index,command_id,page_json,page_sha256,receipt_json FROM teams_recovery_stage_pages ORDER BY run_id,installation_id,upload_id,page_index`},
	{"settings", `SELECT id,library_id,instance_id,org_id,installation_id,key_fingerprint,enabled,metrics_opt_out,acknowledged_policy,settings_revision,local_revision,observed_generation,observed_privacy_revision FROM teams_local_settings ORDER BY id`},
}

var errTeamsProjectionBound = errors.New("protection metadata projection exceeds its bound")

var teamsProjectionMaxBytes = 16 << 20
var teamsProjectionMaxRows = 100000

type teamsProjectionWriter struct {
	h          hash.Hash
	size, rows int
	limit      int
}

func (w *teamsProjectionWriter) put(kind byte, b []byte) error {
	limit := w.limit
	if limit == 0 {
		limit = teamsProjectionMaxBytes
	}
	if len(b) > limit || w.size > limit-len(b)-9 {
		return errTeamsProjectionBound
	}
	w.size += len(b) + 9
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(b)))
	w.h.Write([]byte{kind})
	w.h.Write(n[:])
	w.h.Write(b)
	return nil
}
func teamsProtectionProjection(ctx context.Context, tx *sql.Tx) (string, error) {
	var version string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&version); err != nil {
		return "", err
	}
	baseDomain := "pharos-teams-protection-projection-v1"
	if version != strconv.Itoa(teamsProtectedFormat35) {
		return teamsProtectionProjectionPart(ctx, tx, teamsProtectionProjectionQueries, baseDomain, teamsProjectionMaxBytes)
	}
	// In format 35 the full pre-existing outbox moves into the separately
	// bounded journal digest, so a promised receipt never consumes capacity
	// reserved for unrelated ordinary protected metadata.
	base35, journal35 := teamsProtectionProjectionParts35()
	baseDomain = "pharos-teams-protected-base-35-v1"
	if _, err := teamsProtectionProjectionPreflight35(ctx, tx, base35, baseDomain, teamsProjectionMaxBytes); err != nil {
		return "", err
	}
	base, err := teamsProtectionProjectionPart(ctx, tx, base35, baseDomain, teamsProjectionMaxBytes)
	if err != nil {
		return "", err
	}
	journalDomain := "pharos-teams-origin-journal-projection-v1"
	if _, err = teamsProtectionProjectionPreflight35(ctx, tx, journal35, journalDomain, teamsOriginJournalMaxBytes); err != nil {
		if errors.Is(err, errTeamsProjectionBound) {
			return "", errTeamsJournalProjectionBound
		}
		return "", err
	}
	journal, err := teamsProtectionProjectionPart(ctx, tx, journal35, journalDomain, teamsOriginJournalMaxBytes)
	if err != nil {
		return "", err
	}
	root := teamsProjectionWriter{h: sha256.New(), limit: 256}
	for _, v := range []struct {
		kind  byte
		value string
	}{{'D', "pharos-teams-protected-format-35-root-v1"}, {'B', base}, {'J', journal}} {
		if err = root.put(v.kind, []byte(v.value)); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(root.h.Sum(nil)), nil
}

func teamsProtectionProjectionPart(ctx context.Context, tx *sql.Tx, specs []struct{ name, query string }, domain string, limit int) (string, error) {
	w := teamsProjectionWriter{h: sha256.New(), limit: limit}
	if err := w.put('D', []byte(domain)); err != nil {
		return "", err
	}
	for _, spec := range specs {
		if err := w.put('T', []byte(spec.name)); err != nil {
			return "", err
		}
		rows, err := tx.QueryContext(ctx, spec.query)
		if err != nil {
			return "", err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return "", err
		}
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		for rows.Next() {
			w.rows++
			if w.rows > teamsProjectionMaxRows {
				rows.Close()
				return "", errTeamsProjectionBound
			}
			if err = rows.Scan(ptrs...); err != nil {
				rows.Close()
				return "", err
			}
			if err = w.put('R', nil); err != nil {
				rows.Close()
				return "", err
			}
			for _, value := range values {
				var raw [8]byte
				switch v := value.(type) {
				case nil:
					err = w.put('N', nil)
				case int64:
					binary.BigEndian.PutUint64(raw[:], uint64(v))
					err = w.put('I', raw[:])
				case float64:
					binary.BigEndian.PutUint64(raw[:], math.Float64bits(v))
					err = w.put('F', raw[:])
				case string:
					err = w.put('S', []byte(v))
				case []byte:
					err = w.put('B', v)
				default:
					err = errTeamsProtectionContinuity
				}
				if err != nil {
					rows.Close()
					return "", err
				}
			}
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return "", err
		}
		if err = rows.Close(); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(w.h.Sum(nil)), nil
}

// The format-35 preflight reads only SQLite type and length metadata before
// the driver materializes any potentially large protected cell.
func teamsProtectionProjectionPreflight35(ctx context.Context, tx *sql.Tx, specs []struct{ name, query string }, domain string, limit int) (int64, error) {
	total := int64(9 + len(domain))
	rowsTotal := 0
	for _, spec := range specs {
		total += int64(9 + len(spec.name))
		if total > int64(limit) {
			return 0, errTeamsProjectionBound
		}
		shape, e := tx.QueryContext(ctx, "SELECT * FROM ("+spec.query+") LIMIT 0")
		if e != nil {
			return 0, e
		}
		cols, e := shape.Columns()
		shape.Close()
		if e != nil {
			return 0, e
		}
		terms := make([]string, 0, len(cols))
		for _, col := range cols {
			id := quotedIdentifier(col)
			terms = append(terms, "CASE typeof("+id+") WHEN 'null' THEN 0 WHEN 'integer' THEN 8 WHEN 'real' THEN 8 WHEN 'text' THEN length(CAST("+id+" AS BLOB)) WHEN 'blob' THEN length("+id+") ELSE -1 END")
		}
		rows, e := tx.QueryContext(ctx, "SELECT "+strings.Join(terms, ",")+" FROM ("+spec.query+")")
		if e != nil {
			return 0, e
		}
		vals := make([]int64, len(cols))
		ptr := make([]any, len(cols))
		for i := range vals {
			ptr[i] = &vals[i]
		}
		for rows.Next() {
			rowsTotal++
			if rowsTotal > teamsProjectionMaxRows {
				rows.Close()
				return 0, errTeamsProjectionBound
			}
			if e = rows.Scan(ptr...); e != nil {
				rows.Close()
				return 0, e
			}
			total += 9
			for _, n := range vals {
				cellLimit := limit
				if strings.HasPrefix(spec.name, "v35_") {
					cellLimit = teamsOriginJournalMaxCellBytes
				}
				if n < 0 || n > int64(cellLimit) {
					rows.Close()
					return 0, errTeamsProjectionBound
				}
				total += 9 + n
				if total > int64(limit) {
					rows.Close()
					return 0, errTeamsProjectionBound
				}

			}
		}
		if e = rows.Err(); e != nil {
			rows.Close()
			return 0, e
		}
		rows.Close()
	}
	return total, nil
}

// The v35 root still binds every v34 outbox field, but accounts those bytes
// with the new journal and its pending-receipt reservations.
func teamsProtectionProjectionParts35() (base, journal []struct{ name, query string }) {
	for _, spec := range teamsProtectionProjectionQueries {
		if spec.name == "origin_outbox" {
			journal = append(journal, struct{ name, query string }{"v35_origin_outbox", spec.query})
		} else {
			base = append(base, spec)
		}
	}
	journal = append(journal, teamsProtectionProjectionQueries35...)
	return base, journal
}
