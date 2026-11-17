package archive

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

type teamsRequestBucket struct{ Hour, Model, Effort string }
type teamsRequestCounts struct{ Input, Cached, Output, Reasoning, Total int64 }

// teamsRequestIndexParity is called inside the indexing write transaction. It
// checks every captured request's indexed raw accounting message, time and
// model, then checks the aggregate request ledger independently. A matching
// hourly sum alone is never sufficient to certify the per-request profile.
func teamsRequestIndexParity(tx *sql.Tx, conversation string, proof *teamsCodexRequestProof) (bool, error) {
	if proof == nil || len(proof.Facts) == 0 || len(proof.Occurrences) == 0 {
		return false, nil
	}
	// No unresolved child stream or unindexed session remainder may share this
	// thread's claimed request sequence.
	var sessions int
	var sessionTotal sql.NullInt64
	var estimated sql.NullInt64
	if err := tx.QueryRow(`SELECT count(*),MAX(CASE WHEN native_id='main' THEN total_tokens END),MAX(CASE WHEN native_id='main' THEN is_estimate END) FROM agent_sessions WHERE conversation_id=?`, conversation).Scan(&sessions, &sessionTotal, &estimated); err != nil {
		return false, err
	}
	var expectedTotal int64
	for _, fact := range proof.Facts {
		if expectedTotal > teamsMaxExactInteger-fact.Total {
			return false, nil
		}
		expectedTotal += fact.Total
	}
	if sessions != 1 || !sessionTotal.Valid || sessionTotal.Int64 != expectedTotal || !estimated.Valid || estimated.Int64 != 0 {
		return false, nil
	}
	var previousOrder int64 = -1
	for _, v := range proof.Occurrences {
		var raw, stamp, model, effort, role, kind string
		var selected int
		var order sql.NullInt64
		err := tx.QueryRow(`SELECT COALESCE(NULLIF(raw_text,''),text),COALESCE(created_at,''),COALESCE(model,''),COALESCE(effort,''),role,kind,selected,source_order FROM messages WHERE conversation_id=? AND native_id=?`, conversation, v.NativeID).Scan(&raw, &stamp, &model, &effort, &role, &kind, &selected, &order)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		t, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return false, nil
		}
		hash := sha256.Sum256([]byte(raw))
		if hex.EncodeToString(hash[:]) != v.RawSHA256 || t.UTC().Format(time.RFC3339Nano) != v.Timestamp || model != v.Model || effort != v.Effort || role != "system" || kind != "metadata" || selected != 1 || !order.Valid || order.Int64 <= previousOrder {
			return false, nil
		}
		previousOrder = order.Int64
	}
	expected := map[teamsRequestBucket]teamsRequestCounts{}
	for _, f := range proof.Facts {
		key := teamsRequestBucket{usageHour(f.Timestamp), f.Model, f.Effort}
		if key.Hour == "" {
			return false, nil
		}
		x := expected[key]
		for _, v := range []struct {
			dst *int64
			n   int64
		}{{&x.Input, f.Input}, {&x.Cached, f.Cached.Value}, {&x.Output, f.Output}, {&x.Reasoning, f.Reasoning.Value}, {&x.Total, f.Total}} {
			if *v.dst > teamsMaxExactInteger-v.n {
				return false, nil
			}
			*v.dst += v.n
		}
		expected[key] = x
	}
	rows, err := tx.Query(`SELECT a.native_id,u.usage_hour,u.model,u.effort,u.attribution,u.input_tokens,u.uncached_input_tokens,u.cache_read_input_tokens,u.cache_creation_input_tokens,u.cache_creation_5m_input_tokens,u.cache_creation_1h_input_tokens,u.output_tokens,u.reasoning_output_tokens,u.unclassified_tokens,u.total_tokens,u.is_estimate,u.input_tokens_available FROM agent_session_usage u JOIN agent_sessions a ON a.id=u.agent_session_id WHERE a.conversation_id=?`, conversation)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := map[teamsRequestBucket]bool{}
	for rows.Next() {
		var session, hour, model, effort, attribution string
		var input, uncached, cached, write, write5, write60, output, reasoning, unclassified, total int64
		var estimate, available int
		if err = rows.Scan(&session, &hour, &model, &effort, &attribution, &input, &uncached, &cached, &write, &write5, &write60, &output, &reasoning, &unclassified, &total, &estimate, &available); err != nil {
			return false, err
		}
		key := teamsRequestBucket{hour, model, effort}
		want, ok := expected[key]
		if !ok || found[key] || session != "main" || attribution != "request" || estimate != 0 || available != 1 || write != 0 || write5 != 0 || write60 != 0 || unclassified != 0 || input != want.Input || cached != want.Cached || uncached != want.Input-want.Cached || output != want.Output || reasoning != want.Reasoning || total != want.Total {
			return false, nil
		}
		found[key] = true
	}
	if err = rows.Err(); err != nil {
		return false, err
	}
	return len(found) == len(expected), nil
}

// teamsPersistCodexRequestProfile updates or removes the local derived proof
// in the same transaction as the indexed messages and attribution ledger.
func teamsPersistCodexRequestProfile(tx *sql.Tx, binding, conversation, origin, indexedDigest string, p *teamsCodexRequestProof) (bool, error) {
	valid, err := teamsRequestIndexParity(tx, conversation, p)
	if err != nil {
		return false, err
	}
	if !valid {
		tag, e := tx.Exec(`DELETE FROM teams_codex_request_profiles WHERE binding_id=? AND conversation_id=? AND origin=?`, binding, conversation, origin)
		if e != nil {
			return false, e
		}
		n, e := tag.RowsAffected()
		return n > 0, e
	}
	facts, err := json.Marshal(p.Facts)
	if err != nil {
		return false, err
	}
	tag, err := tx.Exec(`INSERT INTO teams_codex_request_profiles(binding_id,conversation_id,origin,indexed_digest,evidence_sha256,facts_json) VALUES(?,?,?,?,?,?) ON CONFLICT(binding_id,conversation_id,origin) DO UPDATE SET indexed_digest=excluded.indexed_digest,evidence_sha256=excluded.evidence_sha256,facts_json=excluded.facts_json WHERE teams_codex_request_profiles.indexed_digest<>excluded.indexed_digest OR teams_codex_request_profiles.evidence_sha256<>excluded.evidence_sha256 OR teams_codex_request_profiles.facts_json<>excluded.facts_json`, binding, conversation, origin, indexedDigest, p.Digest, string(facts))
	if err != nil {
		return false, err
	}
	n, err := tag.RowsAffected()
	return n > 0, err
}
