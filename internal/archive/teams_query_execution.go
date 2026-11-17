package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/gbdubs/pharos/internal/teams"
)

// The resolver must prove a provider's account namespace and canonical source
// unit. In particular, a generic "local" account is not proof of identity.
// Production callers must not substitute a permissive fallback.
type teamsSourceResolver func(provider, account, native string) (string, string, string, error)

type teamsManifestEntry struct {
	Token            string `json:"token"`
	EvidenceRevision string `json:"evidence_revision"`
	Protection       string `json:"protection"`
}
type teamsSourceSnapshot struct {
	Generation          int64
	PrivacyRevision     int64
	Entries             []teamsManifestEntry
	conversationByToken map[string]string
	// Kept locally: the public wire records only protection, but explicit
	// user privacy must remain distinct from a temporary classifier hold.
	protectionReasonByToken map[string]string
}

var errTeamsSourceUnavailable = errors.New("source identity or privacy state needs reconciliation")
var errTeamsSnapshotChanged = errors.New("library changed during Teams evaluation")

// The service bounds a complete manifest to this many source units.
const teamsSourceLimit = 10000

// teamsSnapshotInTx builds the whole library manifest in a single SQLite
// snapshot. It reads identifiers and privacy metadata only, never messages.
func teamsSnapshotInTx(ctx context.Context, tx *sql.Tx, key []byte, resolve teamsSourceResolver) (teamsSourceSnapshot, error) {
	var snap teamsSourceSnapshot
	if len(key) != 32 {
		return snap, errTeamsSourceUnavailable
	}
	generations, err := readCacheGenerations(ctx, tx)
	if err != nil {
		return snap, err
	}
	snap.Generation = generations.all
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM teams_privacy_revision WHERE id=1`).Scan(&snap.PrivacyRevision); err != nil {
		return snap, err
	}
	var unresolved int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM teams_conversation_privacy p WHERE p.provider='' OR p.account='' OR p.native_id='') OR EXISTS(
 SELECT 1 FROM conversations c JOIN teams_conversation_privacy p ON p.conversation_id=c.id
 WHERE p.provider<>c.provider OR p.account<>c.account OR p.native_id<>c.native_id)`).Scan(&unresolved); err != nil {
		return snap, err
	}
	if unresolved != 0 {
		return snap, errTeamsSourceUnavailable
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.id,c.provider,c.account,c.native_id,COALESCE(p.user_private,0),COALESCE(p.classifier_state,'unclassified'),COALESCE(p.classifier_reviewed,0)
 FROM conversations c LEFT JOIN teams_conversation_privacy p ON (p.provider=c.provider AND p.account=c.account AND p.native_id=c.native_id) OR p.conversation_id=c.id ORDER BY c.id LIMIT ?`, teamsSourceLimit+1)
	if err != nil {
		return snap, err
	}
	defer rows.Close()
	snap.conversationByToken = map[string]string{}
	snap.protectionReasonByToken = map[string]string{}
	for rows.Next() {
		var id, provider, account, native, state string
		var userPrivate, reviewed bool
		if err = rows.Scan(&id, &provider, &account, &native, &userPrivate, &state, &reviewed); err != nil {
			return snap, err
		}
		if len(snap.Entries) >= teamsSourceLimit || resolve == nil {
			return snap, errTeamsSourceUnavailable
		}
		provider, account, native, err = resolve(provider, account, native)
		if err != nil {
			return snap, errTeamsSourceUnavailable
		}
		// A resolver may never bless the generic account placeholder.
		if strings.EqualFold(strings.TrimSpace(account), "local") {
			return snap, errTeamsSourceUnavailable
		}
		token, e := teams.SourceToken(key, provider, account, native)
		if e != nil {
			return snap, errTeamsSourceUnavailable
		}
		if _, exists := snap.conversationByToken[token]; exists {
			return snap, errTeamsSourceUnavailable
		}
		// Unknown is provisional and must not create a sticky explicit
		// privacy assertion. It is usable only after the service supports the
		// coordinated unknown-protection protocol and excludes it from content.
		protection := "unknown"
		switch {
		case userPrivate:
			protection = "private"
		case state == "pending" || state == "unclassified" || state == "failed" || state == "clear" || state == "flagged":
		default:
			return snap, errTeamsSourceUnavailable
		}
		snap.Entries = append(snap.Entries, teamsManifestEntry{Token: token, Protection: protection})
		snap.conversationByToken[token] = id
		if userPrivate {
			snap.protectionReasonByToken[token] = "explicit"
		} else {
			snap.protectionReasonByToken[token] = "provisional"
		}
	}
	if err = rows.Err(); err != nil {
		return snap, err
	}
	sort.Slice(snap.Entries, func(i, j int) bool { return snap.Entries[i].Token < snap.Entries[j].Token })
	return snap, nil
}

func (c *Catalog) teamsSourceSnapshot(ctx context.Context, key []byte, resolve teamsSourceResolver) (teamsSourceSnapshot, error) {
	tx, err := c.reader().BeginTx(ctx, nil)
	if err != nil {
		return teamsSourceSnapshot{}, err
	}
	defer tx.Rollback()
	return teamsSnapshotInTx(ctx, tx, key, resolve)
}

type teamsNumericResult struct {
	Values map[string]int64
	// Caveats are emitted by the trusted calculator, never supplied as free
	// text by a request or caller. No nonempty code is audited for this slice.
	Caveats           []string
	Estimated         bool
	Measured          bool
	Generation        int64
	PrivacyRevision   int64
	OwnedHash         string
	SharedPrivateHash string
	RegistrationHash  string
	PlanHash          string
	Registration      teamsRegistration
}

// teamsRegistration binds the service's accepted manifest, ownership and
// current shared privacy observation to one evaluation. No caller may supply
// a different epoch or owner set at staging time.
type teamsRegistration struct {
	ManifestHash          string
	OwnershipRevision     int64
	Owned                 []string
	SharedPrivacyRevision int64
	SharedPrivate         []string
	Manifest              teams.Manifest
	valid                 bool
}

func newTeamsRegistration(manifest teams.Manifest, sources teams.SourcesResponse, privacy teams.PrivacySnapshot) (teamsRegistration, error) {
	var r teamsRegistration
	_, hash, err := manifest.Bytes()
	if err != nil || hash != sources.ManifestSHA256 || sources.OwnershipRevision < 1 || sources.EvidenceConflictCount != 0 || sources.UnavailableSourceCount != 0 || privacy.PrivacyRevision < 0 || teams.ValidateTokens(sources.OwnedSourceTokens, 10000) != nil || teams.ValidateTokens(privacy.PrivateSourceTokens, 100000) != nil {
		return r, errTeamsSourceUnavailable
	}
	known := map[string]bool{}
	for _, source := range manifest.SourceManifest {
		known[source.Token] = true
	}
	for _, token := range sources.OwnedSourceTokens {
		if !known[token] {
			return r, errTeamsSourceUnavailable
		}
	}
	manifest.SourceManifest = append([]teams.Source{}, manifest.SourceManifest...)
	return teamsRegistration{ManifestHash: hash, OwnershipRevision: sources.OwnershipRevision, Owned: append([]string{}, sources.OwnedSourceTokens...), SharedPrivacyRevision: privacy.PrivacyRevision, SharedPrivate: append([]string{}, privacy.PrivateSourceTokens...), Manifest: manifest, valid: true}, nil
}
func teamsPlanHash(plan teamsQueryPlan) string {
	b, _ := json.Marshal(plan)
	return teamsPayloadHash(b)
}
func teamsRegistrationHash(r teamsRegistration) string {
	b, _ := json.Marshal(r)
	return teamsPayloadHash(b)
}

func teamsOwnedHash(owned []string) string {
	copyTokens := append([]string{}, owned...)
	sort.Strings(copyTokens)
	data, _ := json.Marshal(copyTokens)
	return teamsPayloadHash(data)
}

// teamsExecuteNumeric rechecks the manifest's local snapshot before any
// measurement read. It stages only server-assigned owned IDs inside the same
// transaction; a later send must recheck local and shared privacy revisions.
// The caller supplies ownership only after accepted manifest registration.
func (c *Catalog) teamsExecuteNumeric(ctx context.Context, plan teamsQueryPlan, key []byte, resolve teamsSourceResolver, expected teamsSourceSnapshot, registration teamsRegistration) (teamsNumericResult, error) {
	result := teamsNumericResult{Values: map[string]int64{}}
	if len(plan.Metrics) == 0 || !registration.valid {
		return result, errTeamsQueryUnsupported
	}
	tx, err := c.reader().BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	current, err := teamsSnapshotInTx(ctx, tx, key, resolve)
	if err != nil {
		return result, err
	}
	if current.Generation != expected.Generation || current.PrivacyRevision != expected.PrivacyRevision || len(current.Entries) != len(expected.Entries) {
		return result, errTeamsSnapshotChanged
	}
	if len(registration.Manifest.SourceManifest) != len(current.Entries) {
		return result, errTeamsSnapshotChanged
	}
	for i := range current.Entries {
		if current.Entries[i] != expected.Entries[i] {
			return result, errTeamsSnapshotChanged
		}
		if current.Entries[i].Protection == "unavailable" {
			return result, errTeamsSourceUnavailable
		}
		entry := current.Entries[i]
		source := registration.Manifest.SourceManifest[i]
		if entry.Token != source.Token || entry.EvidenceRevision != source.EvidenceRevision || entry.Protection != source.Protection {
			return result, errTeamsSnapshotChanged
		}
	}
	// Some legacy usage has no conversation owner, and mirror resolution can
	// suppress a whole workspace after linking it to another. Neither may be
	// guessed from a partial manifest or silently omitted.
	var missing int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_session_usage u JOIN agent_sessions a ON a.id=u.agent_session_id LEFT JOIN conversations c ON c.id=a.conversation_id WHERE u.total_tokens>0 AND c.id IS NULL)`).Scan(&missing); err != nil {
		return result, err
	}
	if missing != 0 {
		return result, errTeamsSourceUnavailable
	}
	workspaces, err := queryMapsContext(ctx, tx, `SELECT DISTINCT w.id,w.source_kind FROM agent_session_usage u JOIN agent_sessions a ON a.id=u.agent_session_id JOIN workspaces w ON w.id=a.workspace_id WHERE u.total_tokens>0`)
	if err != nil {
		return result, err
	}
	links, err := queryMapsContext(ctx, tx, identityLinkWorkspacesSQL)
	if err != nil {
		return result, err
	}
	if len(suppressMirrorRows(workspaces, links)) != len(workspaces) {
		return result, fmt.Errorf("%w: linked mirror ownership", errTeamsSourceUnavailable)
	}
	if _, err = tx.ExecContext(ctx, `CREATE TEMP TABLE teams_owned_conversations(id TEXT PRIMARY KEY)`); err != nil {
		return result, err
	}
	seen := map[string]bool{}
	protection := make(map[string]string, len(current.Entries))
	for _, entry := range current.Entries {
		protection[entry.Token] = entry.Protection
	}
	for _, token := range registration.Owned {
		id, ok := current.conversationByToken[token]
		if !ok || seen[token] || protection[token] == "unavailable" {
			return result, errTeamsSourceUnavailable
		}
		seen[token] = true
		if _, err = tx.ExecContext(ctx, `INSERT INTO teams_owned_conversations(id) VALUES(?)`, id); err != nil {
			return result, err
		}
	}
	fields := make([]string, len(plan.Metrics))
	dest := make([]any, 0, len(plan.Metrics)+3)
	values := make([]int64, len(plan.Metrics))
	metricIDs := map[string]bool{}
	for i, m := range plan.Metrics {
		if teamsNumericCapabilities[m.Field].MetricID == "" || m.ID == "" || metricIDs[m.ID] {
			return result, errTeamsQueryUnsupported
		}
		metricIDs[m.ID] = true
		fields[i] = "COALESCE(SUM(u." + m.Field + "),0)"
		dest = append(dest, &values[i])
	}
	var estimated, measured, count int
	dest = append(dest, &estimated, &measured, &count)
	q := `SELECT ` + strings.Join(fields, ",") + `,COALESCE(MAX(u.is_estimate),0),COALESCE(MIN(u.is_estimate),0),COUNT(*) FROM agent_session_usage u JOIN agent_sessions a ON a.id=u.agent_session_id JOIN teams_owned_conversations o ON o.id=a.conversation_id WHERE u.total_tokens>0`
	if err = tx.QueryRowContext(ctx, q).Scan(dest...); err != nil {
		return result, err
	}
	for i, m := range plan.Metrics {
		if values[i] > 9007199254740991 || values[i] < -9007199254740991 {
			return result, errTeamsQueryUnsupported
		}
		result.Values[m.ID] = values[i]
	}
	result.Estimated = estimated != 0
	result.Measured = count > 0 && measured == 0
	result.Generation = current.Generation
	result.PrivacyRevision = current.PrivacyRevision
	result.OwnedHash = teamsOwnedHash(registration.Owned)
	result.SharedPrivateHash = teamsOwnedHash(registration.SharedPrivate)
	result.RegistrationHash = teamsRegistrationHash(registration)
	result.PlanHash = teamsPlanHash(plan)
	result.Registration = registration
	return result, nil
}
