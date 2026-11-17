package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/gbdubs/pharos/internal/teams"
	"github.com/gbdubs/pharos/internal/teams/sourcewire"
)

// teamsV2Inventory is a local as-of observation, not an accepted server
// inventory or a work assignment. Values and paths never enter its wire items.
type teamsV2Inventory struct {
	Items                                       []sourcewire.Item
	Coverage                                    sourcewire.Coverage
	Complete                                    bool
	SourceRevision, PrivacyRevision, Generation int64
	ObservedAt                                  time.Time
	values                                      map[string]int64
	exposures                                   map[string]int64
}

// teamsObserveV2Codex creates a verified subset only. Unsupported and absent
// evidence is withheld, partial coverage is explicit, and no client authority
// is inferred from an account label. It cannot authorize content or a send.
func (c *Catalog) teamsObserveV2Codex(ctx context.Context, cfg Config, d LibraryDescriptor, credential *teams.EnrollmentCredential) (teamsV2Inventory, error) {
	var out teamsV2Inventory
	if credential == nil || credential.LibraryID != d.ID || canonical(cfg.CatalogPath) != canonical(c.Path) {
		return out, errTeamsNotEnrolled
	}
	key := credential.SourceKey()
	defer clear(key)
	if len(key) != 32 {
		return out, errTeamsNotEnrolled
	}
	account, err := teams.SourceWorkNamespace(key, "codex")
	if err != nil {
		return out, errTeamsSourceUnavailable
	}
	tx, err := c.reader().BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	settings, err := readTeamsSettings(ctx, tx)
	if err != nil {
		return out, err
	}
	if settings.LibraryID != d.ID || settings.InstanceID != d.Instance || settings.OrgID != credential.OrgID || settings.InstallationID != credential.InstallationID {
		return out, errTeamsNotEnrolled
	}
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM teams_source_revision WHERE id=1`).Scan(&out.SourceRevision); err != nil {
		return out, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&out.PrivacyRevision); err != nil {
		return out, err
	}
	g, err := readCacheGenerations(ctx, tx)
	if err != nil {
		return out, err
	}
	out.Generation = g.all
	rows, err := tx.QueryContext(ctx, `SELECT c.id,c.provider,c.account,c.native_id,COALESCE(p.status,''),COALESCE(p.total_digest,''),p.total_tokens,COALESCE(p.origin,''),
 COALESCE(b.source_root,''),COALESCE(m.mode,''),EXISTS(SELECT 1 FROM teams_conversation_privacy t WHERE (t.conversation_id=c.id OR (t.provider=c.provider AND t.account=c.account AND t.native_id=c.native_id)) AND t.user_private=1),
 COALESCE(b.host_id,''),COALESCE(b.source_name,''),COALESCE(b.provenance_sha256,''),COALESCE(b.binding_id,''),COALESCE(m.revision,0),
 EXISTS(SELECT 1 FROM source_record_states sr WHERE sr.host_id=b.host_id AND sr.source_name=b.source_name AND sr.source_kind='codex' AND sr.source_account=c.account AND sr.source_id=c.native_id AND sr.digest=p.indexed_digest),
 EXISTS(SELECT 1 FROM source_item_states si WHERE si.host_id=b.host_id AND si.source_name=b.source_name AND si.item=p.origin),
 EXISTS(SELECT 1 FROM source_states ss WHERE ss.host_id=b.host_id AND ss.source_name=b.source_name AND ss.coverage='complete'),
 EXISTS(SELECT 1 FROM conversation_identity_links l WHERE l.left_id=c.id OR l.right_id=c.id)
 FROM conversations c
 LEFT JOIN teams_native_source_proofs p ON p.conversation_id=c.id
 LEFT JOIN teams_source_bindings b ON b.binding_id=p.binding_id AND b.superseded_at=''
 LEFT JOIN teams_source_mapping_current mc ON mc.binding_id=b.binding_id
 LEFT JOIN teams_source_mapping_revisions m ON m.revision=mc.revision AND m.library_id=? AND m.instance_id=? AND m.origin=? AND m.org_id=? AND m.person_id=? AND m.installation_id=?
 ORDER BY c.id,p.origin LIMIT ?`, d.ID, d.Instance, credential.Origin, credential.OrgID, credential.PersonID, credential.InstallationID, sourcewire.MaxPages*sourcewire.MaxItemsPerPage+1)
	if err != nil {
		return out, err
	}
	type candidate struct {
		id, provider, account, native, status, digest, origin, root, mode, host, sourceName, provenance, bindingID string
		mappingRevision                                                                                            int64
		total                                                                                                      sql.NullInt64
		private, recordCurrent, itemCurrent, sourceCurrent, linked                                                 bool
	}
	var candidates []candidate
	for rows.Next() {
		var v candidate
		if err = rows.Scan(&v.id, &v.provider, &v.account, &v.native, &v.status, &v.digest, &v.total, &v.origin, &v.root, &v.mode, &v.private, &v.host, &v.sourceName, &v.provenance, &v.bindingID, &v.mappingRevision, &v.recordCurrent, &v.itemCurrent, &v.sourceCurrent, &v.linked); err != nil {
			rows.Close()
			return out, err
		}
		candidates = append(candidates, v)
		if len(candidates) > sourcewire.MaxPages*sourcewire.MaxItemsPerPage {
			rows.Close()
			return out, errTeamsSourceUnavailable
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	if err = rows.Close(); err != nil {
		return out, err
	}
	items := map[string]sourcewire.Item{}
	values := map[string]int64{}
	exposures := map[string]int64{}
	seenConversations := map[string]bool{}
	for _, v := range candidates {
		if ctx.Err() != nil {
			return teamsV2Inventory{}, ctx.Err()
		}
		if v.provider != "codex" || v.mode != "work" || v.status != "header_independent_unvalidated" || v.native == "" || len(v.native) > 512 {
			if !seenConversations[v.id] {
				out.Coverage.IdentityAmbiguous++
				seenConversations[v.id] = true
			}
			continue
		}
		token, e := teams.SourceToken(key, "codex", account, v.native)
		if e != nil {
			return teamsV2Inventory{}, errTeamsSourceUnavailable
		}
		if v.bindingID == "" || v.mappingRevision < 1 {
			return teamsV2Inventory{}, errTeamsSourceUnavailable
		}
		if old, ok := exposures[v.bindingID]; ok && old != v.mappingRevision {
			return teamsV2Inventory{}, errTeamsSourceUnavailable
		}
		exposures[v.bindingID] = v.mappingRevision
		witness, e := teams.SourceWitness(key, "codex", "codex_thread", v.native)
		if e != nil {
			return teamsV2Inventory{}, errTeamsSourceUnavailable
		}
		item := sourcewire.Item{Kind: "codex_thread", Token: token, Witness: witness, Protection: "unknown", Dependencies: []string{}}
		if v.private {
			item.Protection = "private"
		}
		digest, e := hex.DecodeString(v.digest)
		valid := e == nil && len(digest) == 32 && hex.EncodeToString(digest) == v.digest && v.total.Valid && v.recordCurrent && v.itemCurrent && v.sourceCurrent && !v.linked && teamsCurrentCodexBinding(cfg, v.host, v.sourceName, v.root, v.provenance)
		if valid {
			actual, e := teamsVerifyCodexFile(v.root, v.origin, v.native, v.digest)
			valid = e == nil && actual == v.total.Int64
		}
		if valid {
			item.Profile = &sourcewire.Profile{ID: "codex_thread_usage", Version: 1}
			item.EvidenceRevision, e = teams.SourceEvidenceRevision(key, token, item.Profile.ID, item.Profile.Version, digest)
			valid = e == nil
		}
		if !valid {
			item.Protection = "unavailable"
			item.Profile = nil
			item.EvidenceRevision = ""
			out.Coverage.OverlapUnresolved++
		}
		if prior, ok := items[token]; ok {
			// A disagreement between local occurrences never selects a winner.
			if prior.EvidenceRevision != item.EvidenceRevision || prior.Protection == "unavailable" || item.Protection == "unavailable" || (valid && values[token] != v.total.Int64) {
				prior.Protection = "unavailable"
				prior.Profile = nil
				prior.EvidenceRevision = ""
				delete(values, token)
				out.Coverage.OverlapUnresolved++
			} else if item.Protection == "private" {
				prior.Protection = "private"
			}
			items[token] = prior
		} else {
			items[token] = item
			if valid {
				values[token] = v.total.Int64
			}
		}
	}
	if out.Coverage.IdentityAmbiguous > 1000000 || out.Coverage.OverlapUnresolved > 1000000 {
		return teamsV2Inventory{}, errTeamsSourceUnavailable
	}
	out.Items = make([]sourcewire.Item, 0, len(items))
	for _, item := range items {
		out.Items = append(out.Items, item)
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Token < out.Items[j].Token })
	// The retained catalog cannot yet prove full native source enumeration or
	// cross-provider mirror closure. Never label this provisional subset complete.
	out.Complete = false
	out.values = values
	out.exposures = exposures
	out.ObservedAt = time.Now().UTC()
	if err = tx.Commit(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return teamsV2Inventory{}, err
	}
	return out, nil
}

// Only an exactly matching current local configuration is admitted in this
// first subset. Retained captures without current custody remain partial.
func teamsCurrentCodexBinding(cfg Config, host, name, root, fingerprint string) bool {
	if host != currentHost().ID || name == "" || root == "" || fingerprint == "" {
		return false
	}
	var found *SourceConfig
	for i := range cfg.Sources {
		source := &cfg.Sources[i]
		if source.Name == name {
			if found != nil {
				return false
			}
			found = source
		}
	}
	if found == nil || !found.Enabled || !isCodexSource(found.Kind) || found.Path != root {
		return false
	}
	options, err := teamsSourceOptionsFingerprint(found.Options)
	if err != nil {
		return false
	}
	data, err := json.Marshal(struct{ Domain, Host, Name, Kind, Account, Root, OptionsFingerprint string }{"pharos-teams-source-binding-v1", host, found.Name, found.Kind, found.Account, found.Path, options})
	if err != nil {
		return false
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) == fingerprint
}
