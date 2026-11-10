package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Execution dependencies of persisted consumers (docs/execution-resolver-design.md,
// "Ongoing invalidation"). A late acknowledgement in one conversation can
// change another's execution, parent or inherited history without touching
// that conversation's rows, so a derivative records the digest of all the
// execution evidence its conversation's resolution can depend on: its
// evidence component. The component joins conversations through every
// relationship resolution reads (recorded parents, child links, session
// parents, identity links, command launches) and through the lookup keys
// claims name, including keys no conversation answers yet: when the missing
// transcript arrives it joins the component and the digest changes. Equal
// digests mean equal evidence, so an unchanged digest needs no rebuild; a
// changed one rebuilds the whole component (conservative, never stale).
// Occurrence ownership is part of it (occurrenceDependencyLines): which of a
// transcript's observations are its own, copies or duplicates, and its
// partial coverage, which a parent's event IDs or session membership, an
// original arriving or leaving, or a copy source can change. Message content
// is not: a change to a conversation's own rows marks it dirty through the
// ledgers' triggers, and an append no copy shares leaves the digest equal.

// executionDependencyLimit bounds the closure a component walk reads. A
// larger component takes its digest from the catalog-wide digests instead
// (the same function, cached per source generation), so its publications
// and reads stay exact at the cost of one whole-catalog read per generation.
var executionDependencyLimit = 20000

// beforeDerivedPublication, when set (tests), runs before a ledger opens the
// write transaction that publishes what it built.
var beforeDerivedPublication func()

var errExecutionDependencyLimit = errors.New("execution evidence component exceeds the dependency limit")

type dependencyConversation struct {
	id, provider, account, native, parent, aliases string
	rows                                           []string
	keys                                           []string // lookup keys this conversation answers to
	objects                                        []string // lookup keys its claims name
	neighbors                                      []string // conversations it is directly related to
}

// dependencyRows reads the evidence rows of the given conversations (nil:
// all), one line per row, and their direct relationships.
func dependencyRows(ctx context.Context, q queryer, ids []string) (map[string]*dependencyConversation, error) {
	filter, filterSession, args := "", "", []any{}
	if ids != nil {
		filter = ` WHERE id IN (SELECT value FROM json_each(?))`
		filterSession = ` AND a.conversation_id IN (SELECT value FROM json_each(?))`
		args = append(args, jsonText(ids))
	}
	out := map[string]*dependencyConversation{}
	rows, err := queryMapsContext(ctx, q, `SELECT id,provider,account,native_id,COALESCE(parent_id,'') parent_id,agent_depth,COALESCE(agent_path,'') agent_path,
	 COALESCE(aliases_json,'[]') aliases,COALESCE(agent_nickname,'') nickname,COALESCE(started_at,'') started FROM conversations`+filter, args...)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		c := &dependencyConversation{id: firstString(row["id"]), provider: firstString(row["provider"]), account: firstString(row["account"]),
			native: firstString(row["native_id"]), parent: firstString(row["parent_id"]), aliases: firstString(row["aliases"])}
		c.rows = append(c.rows, strings.Join([]string{"c", c.id, c.provider, c.account, c.native, c.parent, firstString(row["agent_depth"]), firstString(row["agent_path"]), c.aliases,
			firstString(row["nickname"]), firstString(row["started"])}, "\x1f"))
		var aliases []string
		_ = json.Unmarshal([]byte(c.aliases), &aliases)
		c.keys = executionLookupKeys(c.provider, c.account, c.native, aliases)
		if c.parent != "" {
			c.neighbors = append(c.neighbors, c.parent)
		}
		out[c.id] = c
	}
	add := func(id, line string, neighbors ...string) {
		if c := out[id]; c != nil {
			c.rows = append(c.rows, line)
			for _, neighbor := range neighbors {
				if neighbor != "" && neighbor != id {
					c.neighbors = append(c.neighbors, neighbor)
				}
			}
		}
	}
	// A session's launch message is read for its time, position, assignment
	// and (copied launches) its call, role, kind and body: its hash stands
	// for the body. Resolution reads only whether usage was reported, so
	// accounting updates leave the digest alone (as execution_source_*
	// triggers do: the cached digests are keyed by that generation).
	sessions, err := queryMapsContext(ctx, q, `SELECT a.id,a.conversation_id,a.native_id,COALESCE(a.child_conversation_id,'') child,COALESCE(p.conversation_id,'') parent_conversation,
	 COALESCE(a.parent_id,'') parent,a.kind,a.total_tokens>0 tokens,COALESCE(a.delegation_message_id,'') delegation,COALESCE(a.started_at,'') started,
	 COALESCE(m.created_at,'') launch_at,COALESCE(m.source_order,'') launch_order,COALESCE(m.role,'') role,COALESCE(m.kind,'') launch_kind,
	 COALESCE(m.call_id,'') call_id,COALESCE(m.content_hash,'') hash,
	 EXISTS(SELECT 1 FROM agent_session_messages am WHERE am.agent_session_id=a.id) observed FROM agent_sessions a
	 LEFT JOIN agent_sessions p ON p.id=a.parent_id LEFT JOIN messages m ON m.id=a.delegation_message_id WHERE 1`+filterSession, args...)
	if err != nil {
		return nil, err
	}
	for _, s := range sessions {
		line := []string{"s", firstString(s["id"]), firstString(s["native_id"]), firstString(s["child"]), firstString(s["parent"]), firstString(s["kind"]),
			firstString(s["tokens"]), firstString(s["delegation"]), firstString(s["started"]), firstString(s["launch_at"]), firstString(s["launch_order"]),
			firstString(s["role"]), firstString(s["launch_kind"]), firstString(s["call_id"]), firstString(s["hash"]), firstString(s["observed"])}
		add(firstString(s["conversation_id"]), strings.Join(line, "\x1f"), firstString(s["child"]), firstString(s["parent_conversation"]))
	}
	if err := dependencyLaunchResults(ctx, q, out, ids); err != nil {
		return nil, err
	}
	if err := dependencyWorkflowLaunches(ctx, q, out, ids); err != nil {
		return nil, err
	}
	if err := dependencyOriginParents(ctx, q, out, ids); err != nil {
		return nil, err
	}
	links, err := queryMapsContext(ctx, q, `SELECT left_id,right_id,relationship FROM conversation_identity_links`+linkFilter(ids), duplicateArgs(args, 2)...)
	if err != nil {
		return nil, err
	}
	for _, link := range links {
		left, right := firstString(link["left_id"]), firstString(link["right_id"])
		line := strings.Join([]string{"l", left, right, firstString(link["relationship"])}, "\x1f")
		add(left, line, right)
		if right != left {
			add(right, line, left)
		}
	}
	launches, err := queryMapsContext(ctx, q, `SELECT child_id,parent_id FROM conversation_launches`+launchFilter(ids), duplicateArgs(args, 2)...)
	if err != nil {
		return nil, err
	}
	for _, launch := range launches {
		child, parent := firstString(launch["child_id"]), firstString(launch["parent_id"])
		line := strings.Join([]string{"n", child, parent}, "\x1f")
		add(child, line, parent)
		add(parent, line, child)
	}
	present, err := executionEvidenceTablesPresent(ctx, q)
	if err != nil {
		return nil, err
	}
	if present {
		claimFilter := ""
		if ids != nil {
			claimFilter = ` WHERE conversation_id IN (SELECT value FROM json_each(?))`
		}
		claims, err := loadStoredExecutionClaims(ctx, q, claimFilter, args...)
		if err != nil {
			return nil, err
		}
		for _, claim := range claims {
			add(claim.ConversationID, strings.Join([]string{"x", claim.Source, claim.Kind, claim.Subject, claim.Object, claim.Origin, claim.Authority, claim.Rule, claim.Namespace,
				claim.IDKind, claim.Locator, jsonText(claim.Detail)}, "\x1f"))
			if c := out[claim.ConversationID]; c != nil && claim.Object != "" && !strings.HasPrefix(claim.Object, "task-path:") {
				c.objects = append(c.objects, claim.Object)
			}
		}
		states, err := queryMapsContext(ctx, q, `SELECT conversation_id,version,input,id_policy FROM execution_evidence_state`+claimFilter, args...)
		if err != nil {
			return nil, err
		}
		for _, state := range states {
			add(firstString(state["conversation_id"]), strings.Join([]string{"e", firstString(state["version"]), firstString(state["input"]), firstString(state["id_policy"])}, "\x1f"))
		}
	}
	// What each transcript's observations are (own, copied, duplicate) and
	// its coverage, as the occurrence index decides them. A copy's facts
	// read its ancestors and copy sources, which are members of its
	// component, so a change there reaches it through the walk.
	occurrences, err := occurrenceDependencyLines(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	for id, lines := range occurrences {
		for _, line := range lines {
			add(id, "w\x1f"+line)
		}
	}
	return out, nil
}

// dependencyLaunchResults adds what the legacy launch-result reading
// (addLegacyLaunchResults) reads for transcripts without current typed
// evidence: each result of a launch (with or without a stored child link),
// by hash, and the handle it names as a key, so a correction or deletion of
// the result, or the named transcript arriving, changes the digest.
func dependencyLaunchResults(ctx context.Context, q queryer, out map[string]*dependencyConversation, ids []string) error {
	present, err := executionEvidenceTablesPresent(ctx, q)
	if err != nil {
		return err
	}
	// The transcripts in scope that lack current typed evidence, then their
	// launches through the conversation index (CROSS JOIN keeps that order).
	scope, args := `SELECT id FROM conversations`, []any{}
	if ids != nil {
		scope, args = `SELECT value id FROM json_each(?)`, append(args, jsonText(ids))
	}
	if present {
		scope = `SELECT id FROM (` + scope + `) j WHERE NOT EXISTS(SELECT 1 FROM execution_evidence_state s WHERE s.conversation_id=j.id AND s.version=?)`
		args = append(args, executionEvidenceVersion)
	}
	launches, err := queryMapsContext(ctx, q, `WITH scope(id) AS MATERIALIZED (`+scope+`)
SELECT a.id,a.conversation_id,d.call_id FROM scope CROSS JOIN agent_sessions a CROSS JOIN messages d
 WHERE a.conversation_id=scope.id AND d.id=a.delegation_message_id AND a.native_id<>'main' AND d.call_id<>''`, args...)
	if err != nil || len(launches) == 0 {
		return err
	}
	byCall := map[[2]string][]string{}
	calls := [][2]string{}
	for _, launch := range launches {
		k := [2]string{firstString(launch["conversation_id"]), firstString(launch["call_id"])}
		if len(byCall[k]) == 0 {
			calls = append(calls, k)
		}
		byCall[k] = append(byCall[k], firstString(launch["id"]))
	}
	// Only the results of those launch calls, through the call index: a
	// launcher's other tool results are never read.
	results, err := queryMapsContext(ctx, q, `SELECT m.id result_id,m.conversation_id,m.call_id,m.content_hash,`+legacyLaunchResultSQL("m")+` text FROM json_each(?) k
 CROSS JOIN messages m WHERE m.conversation_id=json_extract(k.value,'$[0]') AND m.call_id=json_extract(k.value,'$[1]') AND m.call_id<>''
 AND +m.kind IN ('delegation_result','tool_result') ORDER BY m.id`, jsonText(calls))
	if err != nil {
		return err
	}
	rows := []map[string]any{}
	for _, result := range results {
		for _, launch := range byCall[[2]string{firstString(result["conversation_id"]), firstString(result["call_id"])}] {
			row := maps.Clone(result)
			row["id"] = launch
			rows = append(rows, row)
		}
	}
	for _, row := range rows {
		c := out[firstString(row["conversation_id"])]
		if c == nil {
			continue
		}
		c.rows = append(c.rows, strings.Join([]string{"r", firstString(row["id"]), firstString(row["result_id"]), firstString(row["content_hash"])}, "\x1f"))
		h := readLegacyLaunchResult(firstString(row["text"]))
		if h.agent == "" && h.path == "" {
			// A usage footer's agent (readLegacyLaunchOutcome), read here
			// without the call's body: a key too many only widens the walk.
			h.agent, _, _ = agentUsageFooterResult(firstString(row["text"]))
		}
		if h.agent == "" {
			continue // task paths resolve among the launcher's children, already members
		}
		c.objects = append(c.objects, executionNativeKey(c.provider, c.account, h.agent))
		if root, _, _ := strings.Cut(c.native, ":subagent:"); c.provider == "claude" {
			c.objects = append(c.objects, claudeAgentKey(c.account, root, h.agent))
		}
	}
	return nil
}

// dependencyInverseCache holds, per catalog file and source generation, the
// inverse lookups a component walk needs and no index answers: which
// conversations answer to each alias key, and whose retained origin parent
// or legacy launch result names each key. Source generations advance with
// every evidence row (execution_source_generation) and not with derived
// publications, so an entry is exact for its generation.
var dependencyInverseCache struct {
	sync.Mutex
	entries map[string]*dependencyInverseEntry
}

type dependencyInverseEntry struct {
	generation                int64
	aliases, origins, handles map[string][]string
}

// dependencyInverseIndex reads the inverse lookups once per source
// generation. Legacy launch results are read only for transcripts without
// current typed evidence; none remain once it is current.
//
// A reader at a later generation than the cached entry patches a copy of it:
// every inverse lookup belongs to one owning conversation (its aliases, its
// origin parents, its launch results), and every change to an owner's rows
// is logged under that owner with its generation (execution_source_changes).
// The rows at this generation are therefore the entry's rows with the
// conversations changed since it re-read. A write transaction reads its own
// uncommitted rows this way without storing them (dependencyCacheReader).
func dependencyInverseIndex(ctx context.Context, q queryer) (*dependencyInverseEntry, error) {
	q, admit, done, err := dependencyCacheReader(ctx, q)
	if err != nil {
		return nil, err
	}
	defer done()
	file, generation, err := dependencyCacheKey(ctx, q)
	if err != nil {
		return nil, err
	}
	var base *dependencyInverseEntry
	if file != "" {
		dependencyInverseCache.Lock()
		base = dependencyInverseCache.entries[file]
		dependencyInverseCache.Unlock()
		if base != nil && base.generation == generation {
			return base, nil
		}
	}
	var entry *dependencyInverseEntry
	if base != nil && base.generation < generation {
		changed, err := executionChangedSince(ctx, q, base.generation)
		if err != nil {
			return nil, err
		}
		if len(changed) <= dependencyInversePatchLimit {
			entry = base.without(generation, changed)
			if err := fillDependencyInverse(ctx, q, entry, changed); err != nil {
				return nil, err
			}
		}
	}
	if entry == nil {
		entry = &dependencyInverseEntry{generation: generation, aliases: map[string][]string{}, origins: map[string][]string{}, handles: map[string][]string{}}
		if err := fillDependencyInverse(ctx, q, entry, nil); err != nil {
			return nil, err
		}
	}
	if file != "" && admit {
		dependencyInverseCache.Lock()
		if dependencyInverseCache.entries == nil || len(dependencyInverseCache.entries) >= dependencyCacheCatalogs {
			dependencyInverseCache.entries = map[string]*dependencyInverseEntry{}
		}
		if current := dependencyInverseCache.entries[file]; current == nil || current.generation < generation {
			dependencyInverseCache.entries[file] = entry
		}
		dependencyInverseCache.Unlock()
	}
	return entry, nil
}

// primeDependencyInverse brings the cached inverse index up to the committed
// source generation, outside any write transaction. A write transaction's
// own read is never cached (its rows may roll back), so a writer that finds
// no entry reads every conversation's lookups while it holds the catalog's
// write lock. A writer calls this before it begins, and then patches the
// entry only for its own rows. It is advisory: a failure leaves the writer
// to read what it needs itself.
func (c *Catalog) primeDependencyInverse(ctx context.Context) {
	reader := c.reader()
	// Only marking a ledger's derivatives reads the index. A catalog with
	// neither ledger (a first import) never asks for it, and bringing it
	// forward after every record would copy its growing maps each time.
	if ledgers, err := executionLedgersPresent(ctx, reader); err != nil || len(ledgers) == 0 {
		return
	}
	file, generation, err := dependencyCacheKey(ctx, reader)
	if err != nil || file == "" {
		return
	}
	dependencyInverseCache.Lock()
	entry := dependencyInverseCache.entries[file]
	dependencyInverseCache.Unlock()
	if entry != nil && entry.generation >= generation {
		return
	}
	_, _ = dependencyInverseIndex(ctx, reader)
}

// dependencyInversePatchLimit is how many changed conversations a cached
// inverse index is patched for; past it the index is read again.
var dependencyInversePatchLimit = 5000

// without copies the entry at generation, leaving out the lookups ids own.
// Cached entries are shared and never modified.
func (entry *dependencyInverseEntry) without(generation int64, ids []string) *dependencyInverseEntry {
	drop := map[string]bool{}
	for _, id := range ids {
		drop[id] = true
	}
	copyIndex := func(index map[string][]string) map[string][]string {
		out := make(map[string][]string, len(index))
		for key, owners := range index {
			kept := slices.DeleteFunc(slices.Clone(owners), func(id string) bool { return drop[id] })
			if len(kept) > 0 {
				out[key] = kept
			}
		}
		return out
	}
	return &dependencyInverseEntry{generation: generation, aliases: copyIndex(entry.aliases), origins: copyIndex(entry.origins), handles: copyIndex(entry.handles)}
}

// fillDependencyInverse adds the inverse lookups ids own (nil: every
// conversation) to entry.
func fillDependencyInverse(ctx context.Context, q queryer, entry *dependencyInverseEntry, ids []string) error {
	if ids != nil && len(ids) == 0 {
		return nil
	}
	scope, args := "", []any{}
	if ids != nil {
		scope, args = ` AND %s IN (SELECT value FROM json_each(?))`, []any{jsonText(ids)}
	}
	within := func(column string) string {
		if scope == "" {
			return ""
		}
		return fmt.Sprintf(scope, column)
	}
	add := func(index map[string][]string, key, id string) {
		if !slices.Contains(index[key], id) {
			index[key] = append(index[key], id)
		}
	}
	rows, err := queryMapsContext(ctx, q, `SELECT id,provider,account,native_id,aliases_json FROM conversations WHERE aliases_json NOT IN ('','[]')`+within("id"), args...)
	if err != nil {
		return err
	}
	for _, row := range rows {
		var list []string
		_ = json.Unmarshal([]byte(firstString(row["aliases_json"])), &list)
		for _, key := range executionLookupKeys(firstString(row["provider"]), firstString(row["account"]), firstString(row["native_id"]), list) {
			add(entry.aliases, key, firstString(row["id"]))
		}
	}
	rows, err = queryMapsContext(ctx, q, `SELECT DISTINCT conversation_id id FROM origin_observations WHERE parent_native_id IS NOT NULL AND parent_native_id<>''`+within("conversation_id"), args...)
	if err != nil && !strings.Contains(err.Error(), "no such table") {
		return err
	}
	withOrigins := []string{}
	for _, row := range rows {
		withOrigins = append(withOrigins, firstString(row["id"]))
	}
	rows, err = queryMapsContext(ctx, q, `SELECT DISTINCT c.id FROM conversations c JOIN agent_sessions a ON a.conversation_id=c.id
	 WHERE a.delegation_message_id IS NOT NULL`+within("c.id"), args...)
	if err != nil {
		return err
	}
	launchers := []string{}
	for _, row := range rows {
		launchers = append(launchers, firstString(row["id"]))
	}
	rows, err = queryMapsContext(ctx, q, `SELECT DISTINCT conversation_id id FROM messages WHERE `+claudeWorkflowCallSQL("")+within("conversation_id"), args...)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if id := firstString(row["id"]); !slices.Contains(launchers, id) {
			launchers = append(launchers, id)
		}
	}
	read := func(ids []string, fill func(map[string]*dependencyConversation) error, index map[string][]string) error {
		if len(ids) == 0 {
			return nil
		}
		conversations := map[string]*dependencyConversation{}
		rows, err := queryMapsContext(ctx, q, `SELECT id,provider,account,native_id FROM conversations WHERE id IN (SELECT value FROM json_each(?))`, jsonText(ids))
		if err != nil {
			return err
		}
		for _, row := range rows {
			conversations[firstString(row["id"])] = &dependencyConversation{id: firstString(row["id"]), provider: firstString(row["provider"]),
				account: firstString(row["account"]), native: firstString(row["native_id"])}
		}
		if err := fill(conversations); err != nil {
			return err
		}
		for id, c := range conversations {
			for _, object := range c.objects {
				add(index, object, id)
			}
		}
		return nil
	}
	var originScope []string
	if ids != nil {
		originScope = withOrigins
	}
	if err := read(withOrigins, func(cs map[string]*dependencyConversation) error {
		return dependencyOriginParents(ctx, q, cs, originScope)
	}, entry.origins); err != nil {
		return err
	}
	return read(launchers, func(cs map[string]*dependencyConversation) error {
		if err := dependencyLaunchResults(ctx, q, cs, launchers); err != nil {
			return err
		}
		return dependencyWorkflowLaunches(ctx, q, cs, launchers)
	}, entry.handles)
}

// dependencyOriginParents adds the parent identities origin observations
// retained (addRetainedNativeParents), in the parent's recorded namespace
// when the observation has one, else the child's, as the reading does.
func dependencyOriginParents(ctx context.Context, q queryer, out map[string]*dependencyConversation, ids []string) error {
	filter, args := "", []any{}
	if ids != nil {
		filter, args = ` AND conversation_id IN (SELECT value FROM json_each(?))`, []any{jsonText(ids)}
	}
	rows, err := queryMapsContext(ctx, q, `SELECT conversation_id,evidence_key,parent_native_id,COALESCE(parent_provider,'') parent_provider,
 COALESCE(parent_account,'') parent_account FROM origin_observations WHERE parent_native_id IS NOT NULL AND parent_native_id<>''`+filter, args...)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil
		}
		return err
	}
	for _, row := range rows {
		c := out[firstString(row["conversation_id"])]
		if c == nil {
			continue
		}
		provider, account := firstString(row["parent_provider"], c.provider), firstString(row["parent_account"], c.account)
		c.rows = append(c.rows, strings.Join([]string{"o", firstString(row["evidence_key"]), firstString(row["parent_native_id"]), provider, account}, "\x1f"))
		c.objects = append(c.objects, executionNativeKey(provider, account, firstString(row["parent_native_id"])))
	}
	return nil
}

// linkFilter and launchFilter select the rows touching ids in either
// column, through each column's index.
func linkFilter(ids []string) string {
	if ids == nil {
		return ""
	}
	return ` WHERE rowid IN (SELECT rowid FROM conversation_identity_links WHERE left_id IN (SELECT value FROM json_each(?))
	 UNION SELECT rowid FROM conversation_identity_links WHERE right_id IN (SELECT value FROM json_each(?)))`
}

func launchFilter(ids []string) string {
	if ids == nil {
		return ""
	}
	return ` WHERE rowid IN (SELECT rowid FROM conversation_launches WHERE child_id IN (SELECT value FROM json_each(?))
	 UNION SELECT rowid FROM conversation_launches WHERE parent_id IN (SELECT value FROM json_each(?)))`
}

func duplicateArgs(args []any, times int) []any {
	if len(args) == 0 {
		return nil
	}
	out := []any{}
	for range times {
		out = append(out, args...)
	}
	return out
}

// componentDigest hashes a component's evidence rows and the keys its claims
// name that no member answers to, with the versions that interpret them.
func componentDigest(members []*dependencyConversation) string {
	lines := []string{executionResolverVersion, executionEvidenceVersion, occurrenceDependencyVersion}
	answered := map[string]bool{}
	for _, c := range members {
		for _, key := range c.keys {
			answered[key] = true
		}
	}
	for _, c := range members {
		lines = append(lines, c.rows...)
		for _, object := range c.objects {
			if !answered[object] {
				lines = append(lines, "u\x1f"+object)
			}
		}
	}
	sort.Strings(lines)
	lines = slices.Compact(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:12])
}

// executionDependencyDigests returns every conversation's component digest,
// reading the whole catalog once (a background run's view).
//
// The result is shared per catalog file and source generation (callers must
// not modify it): a status read and a background run at the same evidence
// read it once. A reader at a later generation than the cached entry (a
// write transaction included) patches a copy of it, walking only the
// components that hold a conversation changed since (patchDependencyDigests).
func executionDependencyDigests(ctx context.Context, q queryer) (map[string]string, error) {
	q, admit, done, err := dependencyCacheReader(ctx, q)
	if err != nil {
		return nil, err
	}
	defer done()
	file, generation, err := dependencyCacheKey(ctx, q)
	if err != nil {
		return nil, err
	}
	var out map[string]string
	if file != "" {
		dependencyDigestCache.Lock()
		entry, ok := dependencyDigestCache.entries[file]
		dependencyDigestCache.Unlock()
		if ok && entry.generation == generation {
			return entry.digests, nil
		}
		if ok && entry.generation < generation {
			if out, err = patchDependencyDigests(ctx, q, entry); err != nil {
				return nil, err
			}
		}
	}
	if out == nil {
		all, err := dependencyRows(ctx, q, nil)
		if err != nil {
			return nil, err
		}
		out = make(map[string]string, len(all))
		for _, members := range dependencyComponents(all) {
			digest := componentDigest(members)
			for _, c := range members {
				out[c.id] = digest
			}
		}
	}
	if file != "" && admit {
		dependencyDigestCache.Lock()
		if dependencyDigestCache.entries == nil || len(dependencyDigestCache.entries) >= dependencyCacheCatalogs {
			dependencyDigestCache.entries = map[string]dependencyDigestEntry{}
		}
		dependencyDigestCache.entries[file] = dependencyDigestEntry{generation: generation, digests: out}
		dependencyDigestCache.Unlock()
	}
	return out, nil
}

// dependencyDigestPatchLimit is how many changed conversations cached
// catalog-wide digests are patched for; past it they are read again.
var dependencyDigestPatchLimit = 5000

// patchDependencyDigests is the catalog-wide digests at q's generation from
// base, an earlier generation's: a digest can change only in a component
// that holds a conversation changed since, now or at base (the argument
// executionStaleDependents rests on), so those components are walked again
// and every other digest is kept. Changes are logged per owning conversation
// with their generation (execution_source_changes). It returns nil, for a
// full read, past dependencyDigestPatchLimit changes or when the walk
// exceeds executionDependencyLimit.
func patchDependencyDigests(ctx context.Context, q queryer, base dependencyDigestEntry) (map[string]string, error) {
	changed, err := executionChangedSince(ctx, q, base.generation)
	if err != nil || len(changed) > dependencyDigestPatchLimit {
		return nil, err
	}
	return patchDependencyDigestsFor(ctx, q, base.digests, changed)
}

// patchDependencyDigestsFor is patchDependencyDigests for the given changed
// conversations.
func patchDependencyDigestsFor(ctx context.Context, q queryer, base map[string]string, changed []string) (map[string]string, error) {
	former := map[string]bool{}
	for _, id := range changed {
		if digest, ok := base[id]; ok {
			former[digest] = true
		}
	}
	starts := slices.Clone(changed)
	out := make(map[string]string, len(base))
	for id, digest := range base {
		if former[digest] {
			starts = append(starts, id) // in a changed component at base
		} else {
			out[id] = digest
		}
	}
	members, err := dependencyComponent(ctx, q, starts)
	if errors.Is(err, errExecutionDependencyLimit) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, component := range dependencyComponents(members) {
		digest := componentDigest(component)
		for _, c := range component {
			out[c.id] = digest
		}
	}
	return out, nil
}

// dependencyCacheCatalogs bounds the catalogs the dependency caches hold
// (a process normally opens one; tests open many).
const dependencyCacheCatalogs = 4

var dependencyDigestCache struct {
	sync.Mutex
	entries map[string]dependencyDigestEntry
}

type dependencyDigestEntry struct {
	generation int64
	digests    map[string]string
}

// committedReads is a read-only transaction: it sees only committed rows,
// so what it reads may fill the dependency caches.
type committedReads struct{ *sql.Tx }

// dependencyCacheReader decides whether what q reads may be cached. Lookups
// are safe from any reader, because a committed generation never repeats with
// other contents: the trigger counter only moves forward on commit, and a
// writer holding the lock sees committed rows at its starting generation or
// its own rows at a later one no committed entry has. Admission is not safe
// from a write transaction. Its rows may roll back, and the next commit
// can reach the same generation with different contents. Entries are
// therefore built and admitted only from a committed snapshot: a read-only
// transaction on a *sql.DB, so every query sees the generation the key names,
// or a committedReads the caller opened. Any other reader computes the
// lookups without caching them.
func dependencyCacheReader(ctx context.Context, q queryer) (queryer, bool, func(), error) {
	switch reader := q.(type) {
	case *sql.DB:
		tx, err := reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return nil, false, func() {}, err
		}
		return tx, true, func() { _ = tx.Rollback() }, nil
	case committedReads:
		return reader.Tx, true, func() {}, nil
	}
	return q, false, func() {}, nil
}

// dependencyCacheKey is the catalog file and source generation q reads at;
// file is "" (no caching) when the catalog does not track source
// generations or is not a file.
func dependencyCacheKey(ctx context.Context, q queryer) (string, int64, error) {
	generation, tracked, err := executionSourceGeneration(ctx, q)
	if err != nil || !tracked {
		return "", 0, err
	}
	rows, err := queryMapsContext(ctx, q, `SELECT file FROM pragma_database_list WHERE name='main'`)
	if err != nil || len(rows) == 0 {
		return "", 0, err
	}
	return firstString(rows[0]["file"]), generation, nil
}

// executionDependencyDigestsFor returns the component digests of ids, and of
// the rest of their components, by walking only those components, as a publication guard does inside its
// write transaction. It agrees with executionDependencyDigests on the same
// evidence. All starts are walked together and the members split into
// components afterwards; past executionDependencyLimit members it walks each
// start alone, and a component past the limit reads the catalog-wide digests.
func executionDependencyDigestsFor(ctx context.Context, q queryer, ids []string) (map[string]string, error) {
	members, err := dependencyComponent(ctx, q, ids)
	if errors.Is(err, errExecutionDependencyLimit) && len(ids) > 1 {
		out := map[string]string{}
		for _, id := range ids {
			if _, done := out[id]; done {
				continue
			}
			one, err := executionDependencyDigestsFor(ctx, q, []string{id})
			if err != nil {
				return nil, err
			}
			maps.Copy(out, one)
		}
		return out, nil
	}
	out := map[string]string{}
	if errors.Is(err, errExecutionDependencyLimit) {
		// Too large to walk: the catalog-wide digest is the same digest
		// (gone conversations get "").
		all, err := executionDependencyDigests(ctx, q)
		if err != nil {
			return nil, err
		}
		wanted := map[string]bool{}
		for _, id := range ids {
			out[id] = all[id]
			wanted[all[id]] = true
		}
		// With the rest of each component, as a walk returns it.
		for id, digest := range all {
			if wanted[digest] {
				out[id] = digest
			}
		}
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		// Gone from the catalog: nothing a derivative could depend on.
		out[id] = ""
	}
	for _, component := range dependencyComponents(members) {
		digest := componentDigest(component)
		for _, c := range component {
			out[c.id] = digest
		}
	}
	return out, nil
}

// dependencyComponents splits conversations into the components their
// relationships and keys join, as executionDependencyDigests does.
func dependencyComponents(all map[string]*dependencyConversation) [][]*dependencyConversation {
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		for parent[x] != "" && parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b string) {
		if _, ok := parent[a]; !ok {
			parent[a] = a
		}
		if _, ok := parent[b]; !ok {
			parent[b] = b
		}
		ra, rb := find(a), find(b)
		if ra != rb {
			if ra < rb {
				parent[rb] = ra
			} else {
				parent[ra] = rb
			}
		}
	}
	for id, c := range all {
		union("c:"+id, "c:"+id)
		for _, neighbor := range c.neighbors {
			if all[neighbor] != nil {
				union("c:"+id, "c:"+neighbor)
			}
		}
		for _, key := range c.keys {
			union("c:"+id, "k:"+key)
		}
		for _, object := range c.objects {
			union("c:"+id, "k:"+object)
		}
	}
	byRoot := map[string][]*dependencyConversation{}
	for id, c := range all {
		root := find("c:" + id)
		byRoot[root] = append(byRoot[root], c)
	}
	out := make([][]*dependencyConversation, 0, len(byRoot))
	for _, component := range byRoot {
		out = append(out, component)
	}
	return out
}

// dependencyComponent walks the components of starts breadth first: each round reads
// the frontier's rows, then the conversations that point at it (children by
// parent, child links, session parents, links and launches in either
// direction, claims naming its keys) and those its keys and claims name.
func dependencyComponent(ctx context.Context, q queryer, starts []string) (map[string]*dependencyConversation, error) {
	members := map[string]*dependencyConversation{}
	seenKeys := map[string]bool{}
	frontier := uniqueStrings(slices.Clone(starts))
	sort.Strings(frontier)
	for len(frontier) > 0 {
		if len(members)+len(frontier) > executionDependencyLimit {
			return nil, errExecutionDependencyLimit
		}
		read, err := dependencyRows(ctx, q, frontier)
		if err != nil {
			return nil, err
		}
		next := map[string]bool{}
		keys := []string{}
		for _, id := range frontier {
			c := read[id]
			if c == nil {
				continue
			}
			members[id] = c
			for _, neighbor := range c.neighbors {
				next[neighbor] = true
			}
			for _, key := range append(slices.Clone(c.keys), c.objects...) {
				if !seenKeys[key] {
					seenKeys[key] = true
					keys = append(keys, key)
				}
			}
		}
		inverse, err := dependencyInverse(ctx, q, frontier, keys)
		if err != nil {
			return nil, err
		}
		for _, id := range inverse {
			next[id] = true
		}
		frontier = frontier[:0]
		for id := range next {
			if members[id] == nil {
				frontier = append(frontier, id)
			}
		}
		sort.Strings(frontier)
	}
	return members, nil
}

// dependencyInverse finds conversations related to ids that ids' own rows do
// not name: those pointing at them, and those keys answer to or are named by.
func dependencyInverse(ctx context.Context, q queryer, ids, keys []string) ([]string, error) {
	list := jsonText(ids)
	found := []string{}
	collect := func(statement string, args ...any) error {
		rows, err := queryMapsContext(ctx, q, statement, args...)
		if err != nil {
			return err
		}
		for _, row := range rows {
			found = append(found, firstString(row["id"]))
		}
		return nil
	}
	for _, statement := range []string{
		`SELECT id FROM conversations WHERE parent_id IN (SELECT value FROM json_each(?))`,
		`SELECT conversation_id id FROM agent_sessions WHERE child_conversation_id IN (SELECT value FROM json_each(?))`,
		`SELECT c.conversation_id id FROM agent_sessions c JOIN agent_sessions p ON p.id=c.parent_id WHERE p.conversation_id IN (SELECT value FROM json_each(?))`,
	} {
		if err := collect(statement, list); err != nil {
			return nil, err
		}
	}
	if len(keys) == 0 {
		return found, nil
	}
	present, err := executionEvidenceTablesPresent(ctx, q)
	if err != nil {
		return nil, err
	}
	if present {
		if err := collect(`SELECT DISTINCT conversation_id id FROM execution_claims WHERE object IN (SELECT value FROM json_each(?)) AND object<>''`, jsonText(keys)); err != nil {
			return nil, err
		}
	}
	// Conversations that answer to the keys by alias, or whose retained
	// parent or legacy launch result names them: no index answers these, so
	// they come from an inverse index read once per source generation.
	inverse, err := dependencyInverseIndex(ctx, q)
	if err != nil {
		return nil, err
	}
	natives := []any{}
	// Claude agent keys by the root whose subagents answer them: each root's
	// subagents are read once, however many of its agents are named.
	agents := map[[2]string]map[string]bool{}
	for _, key := range keys {
		found = append(found, inverse.aliases[key]...)
		found = append(found, inverse.origins[key]...)
		found = append(found, inverse.handles[key]...)
		if rest, ok := strings.CutPrefix(key, "native:"); ok {
			if parts := strings.SplitN(rest, "|", 3); len(parts) == 3 {
				natives = append(natives, parts)
			}
		} else if rest, ok := strings.CutPrefix(key, "claude-agent:"); ok {
			if parts := strings.SplitN(rest, "|", 3); len(parts) == 3 {
				root := [2]string{parts[0], parts[1]}
				if agents[root] == nil {
					agents[root] = map[string]bool{}
				}
				agents[root][key] = true
			}
		} else if rest, ok := strings.CutPrefix(key, "claude-workflow:"); ok {
			if parts := strings.SplitN(rest, "|", 3); len(parts) == 3 {
				root := [2]string{parts[0], parts[1]}
				if agents[root] == nil {
					agents[root] = map[string]bool{}
				}
				agents[root][key] = true
			}
		}
	}
	for _, root := range slices.SortedFunc(maps.Keys(agents), func(a, b [2]string) int { return strings.Compare(a[0]+"|"+a[1], b[0]+"|"+b[1]) }) {
		rows, err := queryMapsContext(ctx, q, `SELECT id,account,native_id FROM conversations WHERE provider='claude' AND account=? AND native_id>=? AND native_id<?`,
			root[0], root[1]+":subagent:", root[1]+":subagent;")
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if slices.ContainsFunc(executionLookupKeys("claude", firstString(row["account"]), firstString(row["native_id"]), nil), func(key string) bool { return agents[root][key] }) {
				found = append(found, firstString(row["id"]))
			}
		}
	}
	// Native identities, in one indexed lookup.
	if len(natives) > 0 {
		if err := collect(`SELECT c.id FROM json_each(?) k JOIN conversations c ON c.provider=json_extract(k.value,'$[0]')
		 AND c.account=json_extract(k.value,'$[1]') AND c.native_id=json_extract(k.value,'$[2]')`, jsonText(natives)); err != nil {
			return nil, err
		}
	}
	return found, nil
}

// labelsExecutionCurrent reports whether a conversation's stored labels were
// built at its current execution dependency digest.
func labelsExecutionCurrent(ctx context.Context, q queryer, conversation string) (bool, error) {
	return storedExecutionCurrent(ctx, q, "message_labels_conversations", conversation)
}

// agentEventsExecutionCurrent is labelsExecutionCurrent for agent events.
func agentEventsExecutionCurrent(ctx context.Context, q queryer, conversation string) (bool, error) {
	return storedExecutionCurrent(ctx, q, "agent_events_conversations", conversation)
}

// storedExecutionCurrent reads a ledger's stored digest for a conversation
// and compares it with the current one, walking only its component. A row
// from before digests were recorded (an empty digest) is not current: what it was built
// on is unknown, and the next run rebuilds it. No row (never built, or no
// ledger) is current: there is nothing stale to report.
func storedExecutionCurrent(ctx context.Context, q queryer, ledger, conversation string) (bool, error) {
	rows, err := queryMapsContext(ctx, q, `SELECT execution_digest FROM `+ledger+` WHERE conversation_id=?`, conversation)
	if err != nil && strings.Contains(err.Error(), "no such column") {
		// Built by an executable that recorded no digests: like ''.
		rows, err = queryMapsContext(ctx, q, `SELECT '' execution_digest FROM `+ledger+` WHERE conversation_id=?`, conversation)
	}
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return true, nil
		}
		return false, err
	}
	if len(rows) == 0 {
		return true, nil
	}
	stored := firstString(rows[0]["execution_digest"])
	if stored == "" {
		return false, nil
	}
	if digests, err := cachedExecutionDependencyDigests(ctx, q); err != nil || digests != nil {
		return digests[conversation] == stored, err
	}
	digests, err := executionDependencyDigestsFor(ctx, q, []string{conversation})
	if err != nil {
		return false, err
	}
	return digests[conversation] == stored, nil
}

// cachedExecutionDependencyDigests is the cached catalog-wide digests at q's
// generation, or nil: a lookup, safe from any reader, that reads no rows.
func cachedExecutionDependencyDigests(ctx context.Context, q queryer) (map[string]string, error) {
	file, generation, err := dependencyCacheKey(ctx, q)
	if err != nil || file == "" {
		return nil, err
	}
	dependencyDigestCache.Lock()
	defer dependencyDigestCache.Unlock()
	if entry, ok := dependencyDigestCache.entries[file]; ok && entry.generation == generation {
		return entry.digests, nil
	}
	return nil, nil
}

// agentEventsExecutionStale counts conversations whose agent events were
// built against a superseded execution dependency digest, reading the whole
// catalog's digests once.
func agentEventsExecutionStale(ctx context.Context, q queryer) (int, error) {
	return storedExecutionStale(ctx, q, "agent_events_conversations")
}

func storedExecutionStale(ctx context.Context, q queryer, ledger string) (int, error) {
	rows, err := queryMapsContext(ctx, q, `SELECT conversation_id,execution_digest FROM `+ledger)
	if err != nil && strings.Contains(err.Error(), "no such column") {
		rows, err = queryMapsContext(ctx, q, `SELECT conversation_id,'' execution_digest FROM `+ledger) // no digests: all stale
	}
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return 0, nil
		}
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	digests, err := executionDependencyDigests(ctx, q)
	if err != nil {
		return 0, err
	}
	stale := 0
	for _, row := range rows {
		if digests[firstString(row["conversation_id"])] != firstString(row["execution_digest"]) {
			stale++
		}
	}
	return stale, nil
}

// executionDependencyCurrent reports, inside a publishing transaction, which
// conversations' captured digests still match their evidence. A derivative
// built from a builder that captured no digests (nil map entry "") is
// checked like any other: "" never equals a real digest.
func executionDependencyCurrent(ctx context.Context, q queryer, captured map[string]string) (map[string]bool, error) {
	ids := make([]string, 0, len(captured))
	for id := range captured {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	digests, err := executionDependencyDigestsFor(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = captured[id] != "" && digests[id] == captured[id]
	}
	return out, nil
}

// addColumnIfMissing adds a ledger column an older build created the table
// without.
func addColumnIfMissing(ctx context.Context, tx *sql.Tx, table, column, definition string) error {
	var present int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&present); err != nil || present > 0 {
		return err
	}
	_, err := tx.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+column+` `+definition)
	return err
}

// executionDependentLedgers are the derivatives built against execution
// dependency digests: each ledger's stored digest and the dirty table its
// run reads.
var executionDependentLedgers = [][2]string{{"message_labels_conversations", "message_labels_dirty"}, {"agent_events_conversations", "agent_events_dirty"}}

// Readers that decide pending by dirty marks (the label table and facts, the
// event datasets, the status counts, Ask) also read as pending what the next
// markExecutionDependents would mark. Writers that call it leave nothing
// unmarked, but another writer, or another process's older executable,
// leaves its changes past the cursor until the next mark; a catalog that has
// never marked, or marked under other resolver or evidence versions, has
// every derivative to compare. executionUnmarked computes that set exactly,
// with the mark's own function (executionUnmarkedDependents), without
// writing.

// executionUnmarkedFallbackSQL is a SQL subquery for readers that cannot
// compute executionUnmarked: every conversation built in ledger while
// anything is unmarked (executionPropagation.behind), and every derivative
// built before digests were recorded. It is broader than the exact set,
// never narrower.
func executionUnmarkedFallbackSQL(ledger string) string {
	return `SELECT conversation_id id FROM ` + ledger + ` WHERE execution_digest=''
 OR COALESCE((SELECT value FROM meta WHERE key='` + executionDependentsVersionsKey + `'),'')<>'` + executionDependentsVersions() + `'
 OR (SELECT value FROM execution_source_generation WHERE id=1)>COALESCE((SELECT CAST(value AS INTEGER) FROM meta WHERE key='` + executionDependentsKey + `'),0)`
}

// executionUnmarkedSQL is executionUnmarked as a SQL subquery of its
// conversation IDs (column id). If it cannot be computed it is the fallback,
// so a reader errs towards pending.
func executionUnmarkedSQL(ctx context.Context, q queryer, ledger string) string {
	ids, err := executionUnmarked(ctx, q, ledger)
	if err != nil {
		return executionUnmarkedFallbackSQL(ledger)
	}
	return sqlConversationSet(ids)
}

// sqlConversationSet is a subquery listing ids (column id); empty, it
// selects nothing.
func sqlConversationSet(ids []string) string {
	if len(ids) == 0 {
		return `SELECT NULL id WHERE 0`
	}
	return `SELECT value id FROM json_each('` + strings.ReplaceAll(jsonText(ids), "'", "''") + `')`
}

// executionUnmarked lists the conversations built in ledger that the next
// markExecutionDependents would mark. It is read only, and shared per
// catalog file, source generation and propagation state, built only from a
// committed snapshot (dependencyCacheReader).
func executionUnmarked(ctx context.Context, q queryer, ledger string) ([]string, error) {
	q, admit, done, err := dependencyCacheReader(ctx, q)
	if err != nil {
		return nil, err
	}
	defer done()
	if tx, ok := q.(*sql.Tx); ok && admit {
		q = committedReads{tx} // the walk's own caches may fill too
	}
	state, err := executionPropagationState(ctx, q)
	if err != nil || !state.tracked {
		return nil, err
	}
	columns, err := queryMapsContext(ctx, q, `SELECT name FROM pragma_table_info(?)`, ledger)
	if err != nil || len(columns) == 0 {
		return nil, err // no ledger: nothing built
	}
	if !slices.ContainsFunc(columns, func(row map[string]any) bool { return firstString(row["name"]) == "execution_digest" }) {
		// Built by an executable that recorded no digests, before the run's
		// migration adds them: on unknown evidence, all of it.
		rows, err := queryMapsContext(ctx, q, `SELECT conversation_id FROM `+ledger+` ORDER BY 1`)
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, firstString(row["conversation_id"]))
		}
		return ids, err
	}
	file, _, err := dependencyCacheKey(ctx, q)
	if err != nil {
		return nil, err
	}
	// The set also rests on the ledger's rows, which any executable may
	// write without moving the propagation state; the ledger's stamp moves
	// with every such write (executionDependentsStampTriggers).
	stamp, stamped := int64(0), false
	if rows, err := queryMapsContext(ctx, q, `SELECT value FROM execution_dependents_stamp WHERE id=?`, ledger); err == nil && len(rows) > 0 {
		stamp, stamped = integer(rows[0]["value"]), true
	} else if err != nil && !strings.Contains(err.Error(), "no such table") {
		return nil, err
	}
	if !stamped {
		file = "" // nothing tells a ledger write apart: do not cache
	}
	key := file + "\x00" + ledger
	if file != "" {
		executionUnmarkedCache.Lock()
		entry, ok := executionUnmarkedCache.entries[key]
		executionUnmarkedCache.Unlock()
		if ok && entry.state == state && entry.stamp == stamp {
			return entry.ids, nil
		}
	}
	stale, err := executionUnmarkedDependents(ctx, q, []string{ledger}, state)
	if err != nil {
		return nil, err
	}
	ids := slices.Sorted(maps.Keys(stale[ledger]))
	if file != "" && admit {
		executionUnmarkedCache.Lock()
		if executionUnmarkedCache.entries == nil || len(executionUnmarkedCache.entries) >= 2*dependencyCacheCatalogs {
			executionUnmarkedCache.entries = map[string]executionUnmarkedEntry{}
		}
		executionUnmarkedCache.entries[key] = executionUnmarkedEntry{state: state, stamp: stamp, ids: ids}
		executionUnmarkedCache.Unlock()
	}
	return ids, nil
}

// executionUnmarkedCached is executionUnmarked's cached answer for ledger at
// state, if a committed snapshot has built one that still holds. A lookup is
// safe from any reader, a write transaction included (dependencyCacheReader).
func executionUnmarkedCached(ctx context.Context, q queryer, ledger string, state executionPropagation) ([]string, bool, error) {
	file, _, err := dependencyCacheKey(ctx, q)
	if err != nil || file == "" {
		return nil, false, err
	}
	rows, err := queryMapsContext(ctx, q, `SELECT value FROM execution_dependents_stamp WHERE id=?`, ledger)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			err = nil
		}
		return nil, false, err
	}
	if len(rows) == 0 {
		return nil, false, nil
	}
	executionUnmarkedCache.Lock()
	entry, ok := executionUnmarkedCache.entries[file+"\x00"+ledger]
	executionUnmarkedCache.Unlock()
	if !ok || entry.state != state || entry.stamp != integer(rows[0]["value"]) {
		return nil, false, nil
	}
	return entry.ids, true, nil
}

var executionUnmarkedCache struct {
	sync.Mutex
	entries map[string]executionUnmarkedEntry
}

type executionUnmarkedEntry struct {
	state executionPropagation
	stamp int64
	ids   []string
}

// executionDependentsStampTriggers count every write to a ledger's rows,
// from any executable, in execution_dependents_stamp (keyed by the ledger's
// name): a cached unmarked set is valid only at the stamp it was read at.
// They move the cached-view generation too, since what readers hold back
// changes with it (ledgers are created after Initialize installs it).
func executionDependentsStampTriggers(ledger string) []string {
	bump := `BEGIN INSERT INTO execution_dependents_stamp(id,value) VALUES('` + ledger + `',1) ON CONFLICT(id) DO UPDATE SET value=value+1;
 UPDATE query_cache_generation SET value=value+1 WHERE id=1; END`
	return []string{
		`CREATE TABLE IF NOT EXISTS execution_dependents_stamp (id TEXT PRIMARY KEY, value INTEGER NOT NULL) WITHOUT ROWID`,
		`INSERT INTO execution_dependents_stamp(id,value) VALUES('` + ledger + `',1) ON CONFLICT(id) DO NOTHING`,
		`CREATE TRIGGER IF NOT EXISTS ` + ledger + `_stamp_insert AFTER INSERT ON ` + ledger + ` ` + bump,
		`CREATE TRIGGER IF NOT EXISTS ` + ledger + `_stamp_delete AFTER DELETE ON ` + ledger + ` ` + bump,
		`CREATE TRIGGER IF NOT EXISTS ` + ledger + `_stamp_update AFTER UPDATE ON ` + ledger + ` ` + bump,
	}
}

// executionDependencyMembersDDL records which conversations formed each
// component a derivative was built at, keyed by its digest. A change that
// takes a conversation out of a component (a deleted conversation, a removed
// or retargeted claim or parent, an identity correction) is logged under that
// conversation only, and the component it left can no longer be walked to
// from it; its recorded membership finds the derivatives built on it,
// whether or not that conversation has derivatives of its own.
const executionDependencyMembersDDL = `CREATE TABLE IF NOT EXISTS execution_dependency_members (
  digest TEXT NOT NULL, conversation_id TEXT NOT NULL, PRIMARY KEY(digest,conversation_id)) WITHOUT ROWID;
 CREATE INDEX IF NOT EXISTS execution_dependency_members_conversation_idx ON execution_dependency_members(conversation_id)`

// ensureExecutionDependencyMembers creates the membership table and indexes
// each ledger's digests, in a ledger's schema transaction. Derivatives built
// before digests were recorded (an empty digest) are marked. Derivatives built before
// membership was recorded get the members of the components whose digests
// still hold; those whose digests no longer hold are marked, as a mark
// would.
func ensureExecutionDependencyMembers(ctx context.Context, tx *sql.Tx) error {
	var existed int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='execution_dependency_members'`).Scan(&existed); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, executionDependencyMembersDDL); err != nil {
		return err
	}
	ledgers, err := executionLedgersPresent(ctx, tx)
	if err != nil {
		return err
	}
	stored := map[string]map[string]string{}
	for _, ledger := range ledgers {
		if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS `+ledger[0]+`_digest_idx ON `+ledger[0]+`(execution_digest)`); err != nil {
			return err
		}
		for _, statement := range executionDependentsStampTriggers(ledger[0]) {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		// Built before digests were recorded: on unknown evidence.
		rows, err := queryMapsContext(ctx, tx, `SELECT conversation_id FROM `+ledger[0]+` WHERE execution_digest=''`)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := markExecutionDependent(ctx, tx, ledger, firstString(row["conversation_id"])); err != nil {
				return err
			}
		}
		if existed > 0 {
			continue
		}
		rows, err = queryMapsContext(ctx, tx, `SELECT conversation_id,execution_digest FROM `+ledger[0]+` WHERE execution_digest<>''`)
		if err != nil {
			return err
		}
		stored[ledger[0]] = map[string]string{}
		for _, row := range rows {
			stored[ledger[0]][firstString(row["conversation_id"])] = firstString(row["execution_digest"])
		}
	}
	if existed > 0 || !slices.ContainsFunc(slices.Collect(maps.Values(stored)), func(m map[string]string) bool { return len(m) > 0 }) {
		return nil
	}
	current, err := executionDependencyDigests(ctx, tx)
	if err != nil {
		return err
	}
	held := map[string]bool{}
	for _, ledger := range ledgers {
		for id, digest := range stored[ledger[0]] {
			if current[id] == digest {
				held[digest] = true
			} else if err := markExecutionDependent(ctx, tx, ledger, id); err != nil {
				return err
			}
		}
	}
	for id, digest := range current {
		if held[digest] {
			if _, err := tx.ExecContext(ctx, `INSERT INTO execution_dependency_members(digest,conversation_id) VALUES(?,?) ON CONFLICT DO NOTHING`, digest, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// executionLedgersPresent is executionDependentLedgers that record digests.
func executionLedgersPresent(ctx context.Context, q queryer) ([][2]string, error) {
	ledgers := [][2]string{}
	for _, ledger := range executionDependentLedgers {
		rows, err := queryMapsContext(ctx, q, `SELECT COUNT(*) n FROM pragma_table_info(?) WHERE name='execution_digest'`, ledger[0])
		if err != nil {
			return nil, err
		}
		if len(rows) > 0 && integer(rows[0]["n"]) > 0 {
			ledgers = append(ledgers, ledger)
		}
	}
	return ledgers, nil
}

// executionPublication is a derivative publication's freshness check: which
// captured digests still hold, inside the publishing transaction, and the
// digests the published rows replace.
type executionPublication struct {
	current  map[string]bool
	replaced []string
}

// publishExecutionDependents checks captured (conversation -> digest built
// at) for ledger inside the publishing transaction, and records the members
// of every component that still holds (executionDependencyMembersDDL). The
// caller publishes the current ones, then calls finish.
func publishExecutionDependents(ctx context.Context, tx *sql.Tx, ledger string, captured map[string]string) (*executionPublication, error) {
	ids := slices.Sorted(maps.Keys(captured))
	digests, err := executionDependencyDigestsFor(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	out := &executionPublication{current: make(map[string]bool, len(ids))}
	built := map[string]bool{}
	for _, id := range ids {
		out.current[id] = captured[id] != "" && digests[id] == captured[id]
		if out.current[id] {
			built[captured[id]] = true
		}
	}
	for id, digest := range digests {
		if !built[digest] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO execution_dependency_members(digest,conversation_id) VALUES(?,?) ON CONFLICT DO NOTHING`, digest, id); err != nil {
			if strings.Contains(err.Error(), "no such table") {
				break // an older snapshot's ledger: nothing to record into
			}
			return nil, err
		}
	}
	rows, err := queryMapsContext(ctx, tx, `SELECT DISTINCT execution_digest FROM `+ledger+` WHERE execution_digest<>'' AND conversation_id IN (SELECT value FROM json_each(?))`, jsonText(ids))
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if digest := firstString(row["execution_digest"]); !built[digest] {
			out.replaced = append(out.replaced, digest)
		}
	}
	return out, nil
}

// finish forgets the members of replaced components no derivative rests on
// any more.
func (p *executionPublication) finish(ctx context.Context, tx *sql.Tx) error {
	if len(p.replaced) == 0 {
		return nil
	}
	ledgers, err := executionLedgersPresent(ctx, tx)
	if err != nil {
		return err
	}
	held := ""
	for _, ledger := range ledgers {
		held += ` AND NOT EXISTS(SELECT 1 FROM ` + ledger[0] + ` l WHERE l.execution_digest=execution_dependency_members.digest)`
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM execution_dependency_members WHERE digest IN (SELECT value FROM json_each(?))`+held, jsonText(p.replaced))
	if err != nil && strings.Contains(err.Error(), "no such table") {
		err = nil
	}
	return err
}

// executionDependentsKey (meta) is the execution source generation whose
// changes markExecutionDependents has propagated, and
// executionDependentsVersionsKey the resolver and evidence versions it
// compared digests under.
const (
	executionDependentsKey         = "execution_dependents_through"
	executionDependentsVersionsKey = "execution_dependents_versions"
)

func executionDependentsVersions() string {
	return executionResolverVersion + "/" + executionEvidenceVersion + "/" + occurrenceDependencyVersion
}

// executionPropagation is where marking stands in a snapshot.
type executionPropagation struct {
	tracked             bool
	generation, through int64
	// whole: no cursor (a catalog that has never marked, or an upgraded
	// one), or one recorded under other resolver or evidence versions, whose
	// every digest may differ with no source change. Every derivative is then
	// compared with the whole catalog's digests.
	whole bool
}

func executionPropagationState(ctx context.Context, q queryer) (executionPropagation, error) {
	generation, tracked, err := executionSourceGeneration(ctx, q)
	if err != nil || !tracked {
		return executionPropagation{}, err
	}
	state := executionPropagation{tracked: true, generation: generation, whole: true}
	rows, err := queryMapsContext(ctx, q, `SELECT key,value FROM meta WHERE key IN (?,?)`, executionDependentsKey, executionDependentsVersionsKey)
	if err != nil {
		return executionPropagation{}, err
	}
	meta := map[string]string{}
	for _, row := range rows {
		meta[firstString(row["key"])] = firstString(row["value"])
	}
	if through, ok := meta[executionDependentsKey]; ok && meta[executionDependentsVersionsKey] == executionDependentsVersions() {
		state.whole = false
		state.through, _ = strconv.ParseInt(through, 10, 64)
	}
	return state, nil
}

// behind is how far marking is behind: the source generations not yet
// propagated, or the whole generation when every derivative is compared.
func (p executionPropagation) behind() int64 {
	switch {
	case !p.tracked:
		return 0
	case p.whole:
		return max(1, p.generation)
	}
	return max(0, p.generation-p.through)
}

// markExecutionDependents propagates execution evidence changes to the
// derivatives built on them, in the writing transaction: every conversation
// whose stored labels or agent events were built against a component digest
// that no longer holds is marked dirty, as a change to its own rows marks it.
// Every reader that shows a dirty conversation as pending (the transcript,
// the label tables and dashboards, the runs' status) then does so before the
// next run, and the run rebuilds it; reads never write. It catches up on
// every source change since the last call, so a writer that does not call it
// is covered by the next that does (ingestion, the evidence backfill and
// recovery, and each labels or agent events run before it plans).
//
// What it marks is executionUnmarkedDependents, which readers compute too.
func markExecutionDependents(ctx context.Context, tx *sql.Tx) error {
	state, err := executionPropagationState(ctx, tx)
	if err != nil || !state.tracked {
		return err
	}
	ledgers, err := executionLedgersPresent(ctx, tx)
	if err != nil {
		return err
	}
	if state.behind() == 0 {
		// Nothing to propagate; derivatives published without a digest
		// (another executable's) still wait for a rebuild.
		return markUndigestedDependents(ctx, tx, ledgers)
	}
	if len(ledgers) > 0 {
		names := make([]string, 0, len(ledgers))
		for _, ledger := range ledgers {
			names = append(names, ledger[0])
		}
		// A writer that has changed nothing yet reads what a committed
		// snapshot already worked out (propagateExecutionChanges primes it),
		// rather than walking the changed components while it holds the lock.
		stale, primed := map[string]map[string]bool{}, true
		for _, name := range names {
			ids, ok, err := executionUnmarkedCached(ctx, tx, name, state)
			if err != nil {
				return err
			}
			if primed = primed && ok; !primed {
				break
			}
			stale[name] = map[string]bool{}
			for _, id := range ids {
				stale[name][id] = true
			}
		}
		if !primed {
			if stale, err = executionUnmarkedDependents(ctx, tx, names, state); err != nil {
				return err
			}
		}
		for _, ledger := range ledgers {
			for _, id := range slices.Sorted(maps.Keys(stale[ledger[0]])) {
				if err := markExecutionDependent(ctx, tx, ledger, id); err != nil {
					return err
				}
			}
		}
	}
	for key, value := range map[string]string{executionDependentsKey: strconv.FormatInt(state.generation, 10), executionDependentsVersionsKey: executionDependentsVersions()} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
			return err
		}
	}
	if len(ledgers) == 0 {
		return nil
	}
	// Moving the cursor changes what executionUnmarked reads, which no
	// cached-view trigger sees when nothing was marked: invalidate cached
	// views here, as label facts publication does. This touches only the
	// cached-view generation, never execution_source_generation, so the
	// resolver's caches are not invalidated by it.
	_, err = tx.ExecContext(ctx, `UPDATE query_cache_generation SET value=value+1 WHERE id=1`)
	if err != nil && strings.Contains(err.Error(), "no such table") {
		err = nil
	}
	return err
}

// markUndigestedDependents marks the derivatives in ledgers built without a
// digest (an empty one) that are not marked yet.
func markUndigestedDependents(ctx context.Context, tx *sql.Tx, ledgers [][2]string) error {
	for _, ledger := range ledgers {
		rows, err := queryMapsContext(ctx, tx, `SELECT conversation_id FROM `+ledger[0]+` l WHERE execution_digest=''
		 AND NOT EXISTS(SELECT 1 FROM `+ledger[1]+` d WHERE d.conversation_id=l.conversation_id)`)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := markExecutionDependent(ctx, tx, ledger, firstString(row["conversation_id"])); err != nil {
				return err
			}
		}
	}
	return nil
}

// markExecutionDependent marks one conversation's derivative in ledger as
// resting on superseded execution evidence.
func markExecutionDependent(ctx context.Context, tx *sql.Tx, ledger [2]string, id string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+ledger[1]+`(conversation_id,token) VALUES(?,hex(randomblob(8))) ON CONFLICT(conversation_id) DO NOTHING`, id); err != nil {
		return err
	}
	// Stored events of a superseded attribution leave the datasets until
	// they are published again.
	if ledger[0] == "agent_events_conversations" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_events_execution_stale(conversation_id) VALUES(?) ON CONFLICT DO NOTHING`, id); err != nil && !strings.Contains(err.Error(), "no such table") {
			return err
		}
	}
	return nil
}

// executionUnmarkedDependents lists, per ledger, the derivatives state has
// not marked whose stored digest does not hold: built before digests were
// recorded (an empty digest), or, when every derivative is compared (state.whole), every
// mismatch with the whole catalog's digests, else executionStaleDependents of
// the changes past the cursor.
func executionUnmarkedDependents(ctx context.Context, q queryer, ledgers []string, state executionPropagation) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	if !state.tracked {
		return out, nil
	}
	if state.whole {
		var digests map[string]string
		for _, ledger := range ledgers {
			rows, err := queryMapsContext(ctx, q, `SELECT conversation_id,execution_digest FROM `+ledger)
			if err != nil {
				return nil, err
			}
			out[ledger] = map[string]bool{}
			for _, row := range rows {
				if digests == nil {
					if digests, err = executionDependencyDigests(ctx, q); err != nil {
						return nil, err
					}
				}
				if id, stored := firstString(row["conversation_id"]), firstString(row["execution_digest"]); stored == "" || digests[id] != stored {
					out[ledger][id] = true
				}
			}
		}
		return out, nil
	}
	if state.behind() > 0 {
		changed, err := executionChangedSince(ctx, q, state.through)
		if err != nil {
			return nil, err
		}
		if out, err = executionStaleDependents(ctx, q, ledgers, changed); err != nil {
			return nil, err
		}
	}
	for _, ledger := range ledgers {
		rows, err := queryMapsContext(ctx, q, `SELECT conversation_id FROM `+ledger+` WHERE execution_digest=''`)
		if err != nil {
			return nil, err
		}
		if out[ledger] == nil {
			out[ledger] = map[string]bool{}
		}
		for _, row := range rows {
			out[ledger][firstString(row["conversation_id"])] = true
		}
	}
	return out, nil
}

// executionStaleDependents lists, per ledger, the conversations whose stored
// digest no longer holds after the changed conversations' source rows
// changed. A derivative's digest can only change if its component now
// contains a changed conversation, which a walk from the changed ones
// reaches, or contained one when it was built, which the recorded members of
// that digest name (or, for derivatives built before members were recorded,
// the changed conversation's own stored digest). Those components are walked
// and compared.
func executionStaleDependents(ctx context.Context, q queryer, ledgers []string, changed []string) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	if len(changed) == 0 || len(ledgers) == 0 {
		return out, nil
	}
	starts := map[string]bool{}
	for _, id := range changed {
		starts[id] = true
	}
	list := jsonText(changed)
	for _, ledger := range ledgers {
		siblings := func(members string, args ...any) ([]map[string]any, error) {
			return queryMapsContext(ctx, q, `SELECT conversation_id FROM `+ledger+` WHERE execution_digest IN
			 (SELECT execution_digest FROM `+ledger+` WHERE execution_digest<>'' AND conversation_id IN (SELECT value FROM json_each(?))`+members+`)`, args...)
		}
		rows, err := siblings(` UNION SELECT digest FROM execution_dependency_members WHERE conversation_id IN (SELECT value FROM json_each(?))`, list, list)
		if err != nil && strings.Contains(err.Error(), "no such table") {
			rows, err = siblings("", list) // a snapshot from before members were recorded
		}
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			starts[firstString(row["conversation_id"])] = true
		}
	}
	current, err := executionComponentDigests(ctx, q, slices.Sorted(maps.Keys(starts)))
	if err != nil {
		return nil, err
	}
	candidates := slices.Sorted(maps.Keys(current))
	for id := range starts {
		if _, ok := current[id]; !ok {
			candidates = append(candidates, id) // gone: nothing holds for it
		}
	}
	for _, ledger := range ledgers {
		rows, err := queryMapsContext(ctx, q, `SELECT conversation_id,execution_digest FROM `+ledger+`
		 WHERE execution_digest<>'' AND conversation_id IN (SELECT value FROM json_each(?))`, jsonText(candidates))
		if err != nil {
			return nil, err
		}
		out[ledger] = map[string]bool{}
		for _, row := range rows {
			if id := firstString(row["conversation_id"]); current[id] != firstString(row["execution_digest"]) {
				out[ledger][id] = true
			}
		}
	}
	return out, nil
}

// executionComponentDigests is the digest of every member of the components
// of starts (not only of starts), walking only those components; past
// executionDependencyLimit it is the catalog-wide digests.
func executionComponentDigests(ctx context.Context, q queryer, starts []string) (map[string]string, error) {
	members, err := dependencyComponent(ctx, q, starts)
	if errors.Is(err, errExecutionDependencyLimit) {
		return executionDependencyDigests(ctx, q)
	}
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, component := range dependencyComponents(members) {
		digest := componentDigest(component)
		for _, c := range component {
			out[c.id] = digest
		}
	}
	return out, nil
}

// executionDependentsBehind is how far marking is behind in q's snapshot
// (executionPropagation.behind): 0 when nothing is unmarked, or when the
// catalog tracks no generation.
func executionDependentsBehind(ctx context.Context, q queryer) (int64, error) {
	state, err := executionPropagationState(ctx, q)
	return state.behind(), err
}

// executionUndigestedUnmarked reports whether a derivative built without a
// digest has no dirty mark yet.
func executionUndigestedUnmarked(ctx context.Context, q queryer) (bool, error) {
	ledgers, err := executionLedgersPresent(ctx, q)
	if err != nil {
		return false, err
	}
	for _, ledger := range ledgers {
		rows, err := queryMapsContext(ctx, q, `SELECT 1 FROM `+ledger[0]+` l WHERE execution_digest=''
		 AND NOT EXISTS(SELECT 1 FROM `+ledger[1]+` d WHERE d.conversation_id=l.conversation_id) LIMIT 1`)
		if err != nil || len(rows) > 0 {
			return len(rows) > 0, err
		}
	}
	return false, nil
}

// propagateExecutionChanges runs markExecutionDependents when the source
// generation moved past it, or a derivative without a digest is unmarked,
// taking the writer only then.
func (c *Catalog) propagateExecutionChanges(ctx context.Context) error {
	state, err := executionPropagationState(ctx, c.reader())
	if err != nil {
		return err
	}
	if state.behind() == 0 {
		undigested, err := executionUndigestedUnmarked(ctx, c.reader())
		if err != nil || !undigested {
			return err
		}
	}
	if state.whole {
		// Every derivative is compared with the catalog-wide digests: read
		// them from a committed snapshot first, so the writer patches them for
		// what committed since instead of reading the whole catalog.
		if _, err := executionDependencyDigests(ctx, c.reader()); err != nil {
			return err
		}
	}
	c.primeDependencyInverse(ctx)
	// Work out what to mark from a committed snapshot; the writer walks the
	// changed components itself only if something commits in between.
	if ledgers, err := executionLedgersPresent(ctx, c.reader()); err == nil && !state.whole {
		for _, ledger := range ledgers {
			if _, err := executionUnmarked(ctx, c.reader(), ledger[0]); err != nil {
				return err
			}
		}
	}
	return c.writeTransaction(ctx, "propagate execution evidence changes", func(tx *sql.Tx) error { return markExecutionDependents(ctx, tx) })
}
