package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// cursorAdapter reads Cursor's chats (composers) from a Cursor user folder:
// ~/Library/Application Support/Cursor/User, or its global database. Cursor
// keeps every composer in globalStorage/state.vscdb, in its cursorDiskKV
// table: composerData:<id> holds a composer, bubbleId:<id>:<bubble> each of
// its messages, and the composer or, from Cursor 3, composerConvHeaders:<id>:
// <chunk> their order. A subagent is a composer whose subagentInfo names the
// composer that started it. A window's own state.vscdb lists its composers,
// which places the composers that do not name their folder themselves. The
// same databases hold Cursor's sign-in tokens, settings and each composer's
// encryption keys, which are neither read nor captured (cursorCaptureTables).
type cursorAdapter struct {
	baseAdapter
	// selected, when set, limits reading to the groups holding these
	// composers (snapshotSessions).
	selected map[string]bool
	// database, when set, is the global database to read in place of the
	// user folder's: an older snapshot of it, whose composers are still
	// placed by the folder's windows.
	database string
}

// cursorExtractor versions the parser; bump it when parsing changes.
const cursorExtractor = "cursor-v1"

func (a *cursorAdapter) partExtractor() string { return cursorExtractor }

// cursorCaptureTables are what a capture keeps: of ItemTable, a window's
// list of its composers; of cursorDiskKV, each composer, its messages and
// their order. Every other table and row, including any a later release adds,
// is left out, among them Cursor's sign-in tokens (cursorAuth/*) and
// settings, and each composer is kept without its encryption keys and the
// encrypted state they open. A composer that is not JSON cannot be cleared
// of them, so is left out too.
var cursorCaptureTables = map[string]captureTable{
	"ItemTable": {where: `key = 'composer.composerData'`},
	"cursorDiskKV": {
		columns: `key, CASE WHEN key GLOB 'composerData:*' AND value IS NOT NULL THEN json_remove(CAST(value AS TEXT), '$.blobEncryptionKey', '$.speculativeSummarizationEncryptionKey', '$.conversationState') ELSE value END`,
		where:   `key GLOB 'bubbleId:*' OR key GLOB 'composerConvHeaders:*' OR key GLOB 'composerData:*' AND (value IS NULL OR json_valid(CAST(value AS TEXT)))`,
	},
}

// globalDatabase is the database holding the composers: the source itself
// when it is a database, else globalStorage/state.vscdb.
func (a *cursorAdapter) globalDatabase() (string, error) {
	if a.database != "" {
		return a.database, nil
	}
	info, err := os.Stat(a.config.Path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return a.config.Path, nil
	}
	return filepath.Join(a.config.Path, "globalStorage", "state.vscdb"), nil
}

// cursorWindow is a window's storage: its database and the workspace.json
// naming its folder, each "" when absent.
type cursorWindow struct{ database, workspace string }

// windows lists the windows' storage of a user folder.
func (a *cursorAdapter) windows() ([]cursorWindow, error) {
	if info, err := os.Stat(a.config.Path); err != nil || !info.IsDir() {
		return nil, err
	}
	storage := filepath.Join(a.config.Path, "workspaceStorage")
	entries, err := os.ReadDir(storage)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	windows := []cursorWindow{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		window := cursorWindow{}
		for name, field := range map[string]*string{"state.vscdb": &window.database, "workspace.json": &window.workspace} {
			file := filepath.Join(storage, entry.Name(), name)
			if info, err := a.files().Stat(file); err == nil && info.Mode().IsRegular() {
				*field = file
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
		if window.database != "" {
			windows = append(windows, window)
		}
	}
	return windows, nil
}

// captureInputs are what a capture copies: the databases (only their
// conversations, see cursorCaptureTables) and the windows' workspace.json.
func (a *cursorAdapter) captureInputs() (databases, files []string, err error) {
	global, err := a.globalDatabase()
	if err != nil {
		return nil, nil, err
	}
	if _, err := os.Stat(global); err == nil {
		databases = append(databases, global)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	windows, err := a.windows()
	if err != nil {
		return nil, nil, err
	}
	for _, window := range windows {
		databases = append(databases, window.database)
		if window.workspace != "" {
			files = append(files, window.workspace)
		}
	}
	return databases, files, nil
}

func (a *cursorAdapter) Fingerprint() (string, error) {
	databases, files, err := a.captureInputs()
	if err != nil {
		return "", err
	}
	rows := []string{}
	for _, database := range databases {
		rows = append(rows, fmt.Sprintf("%s:%d", a.original(database), a.databaseVersion(database)))
	}
	for _, file := range files {
		if info, err := a.files().Stat(file); err == nil {
			rows = append(rows, fmt.Sprintf("%s:%d:%d", a.original(file), info.Size(), info.ModTime().UnixNano()))
		}
	}
	return cursorExtractor + ":" + hashBytes([]byte(strings.Join(rows, "\n"))), nil
}

func (a *cursorAdapter) Discover(emit func(WorkspaceRecord) error) error {
	return a.discoverParts(nil, func(record *WorkspaceRecord, _ []sourcePart) error {
		if record == nil {
			return nil
		}
		return emit(*record)
	})
}

func (a *cursorAdapter) discoverParts(unchanged func([]sourcePart) bool, emit func(*WorkspaceRecord, []sourcePart) error) error {
	defer a.shareSQLiteCopies()()
	plan, err := a.planParts()
	if err != nil {
		return err
	}
	return plan.discover(a.context(), unchanged, emit)
}

// cursorPlace is where a composer was run: a folder, and the remote host
// holding it for a remote window.
type cursorPlace struct{ location, remote string }

// cursorComposer is a composer's identity and how far it has changed.
type cursorComposer struct {
	id, parent string
	// version is the latest of its own timestamps, in milliseconds, and
	// signal what else changes when a message does.
	version int64
	bubbles int64
	signal  string
	place   cursorPlace
}

// cursorHead is what planning reads of a composer.
type cursorHead struct {
	CreatedAt     any `json:"createdAt"`
	LastUpdatedAt any `json:"lastUpdatedAt"`
	Checkpoint    any `json:"conversationCheckpointLastUpdatedAt"`
	SubagentInfo  struct {
		Parent any `json:"parentComposerId"`
	} `json:"subagentInfo"`
	Workspace struct {
		URI map[string]any `json:"uri"`
	} `json:"workspaceIdentifier"`
}

// open opens a database for reading in one transaction.
func (a *cursorAdapter) open(database string) (*sql.DB, *sql.Tx, error) {
	readable, err := a.files().Database(database)
	if err != nil {
		return nil, nil, err
	}
	db, err := openSourceSQLite(a.context(), readable)
	if err != nil {
		return nil, nil, err
	}
	tx, err := db.BeginTx(a.context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return db, tx, nil
}

// places reads which folder each window's composers were run in, from the
// window's list of them. A window that cannot be read is reported.
func (a *cursorAdapter) places() (map[string]cursorPlace, []error) {
	places := map[string]cursorPlace{}
	windows, err := a.windows()
	if err != nil {
		return places, []error{err}
	}
	var failures []error
	for _, window := range windows {
		place := cursorPlace{}
		if window.workspace != "" {
			manifest, _, err := a.files().ReadJSONVersion(window.workspace)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				failures = append(failures, err)
				continue
			}
			place.location, place.remote = vscodeWorkspaceLocation(mapValueDefault(manifest))
		}
		if place.location == "" {
			continue
		}
		ids, err := a.windowComposers(window.database)
		if err != nil {
			failures = append(failures, fmt.Errorf("Cursor window %s: %w", a.original(window.database), err))
			continue
		}
		for _, id := range ids {
			if _, seen := places[id]; !seen {
				places[id] = place
			}
		}
	}
	return places, failures
}

// windowComposers lists the composers a window's database names.
func (a *cursorAdapter) windowComposers(database string) ([]string, error) {
	db, tx, err := a.open(database)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	defer tx.Rollback()
	tables, err := tableNames(tx)
	if err != nil || !tables["ItemTable"] {
		return nil, err
	}
	var value []byte
	err = tx.QueryRowContext(a.context(), `SELECT value FROM ItemTable WHERE key='composer.composerData'`).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var list struct {
		AllComposers []struct {
			ComposerID string `json:"composerId"`
		} `json:"allComposers"`
	}
	if err := json.Unmarshal(value, &list); err != nil {
		return nil, err
	}
	ids := []string{}
	for _, composer := range list.AllComposers {
		if composer.ComposerID != "" {
			ids = append(ids, composer.ComposerID)
		}
	}
	return ids, nil
}

// cursorURIPlace is the place a composer's workspace URI names.
func cursorURIPlace(uri map[string]any) cursorPlace {
	if external, _ := uri["external"].(string); external != "" {
		location, remote := vscodeWorkspaceLocation(map[string]any{"folder": external})
		return cursorPlace{location, remote}
	}
	switch firstString(uri["scheme"]) {
	case "file":
		return cursorPlace{location: firstString(uri["fsPath"], uri["path"])}
	case "vscode-remote":
		return cursorPlace{location: firstString(uri["path"]), remote: firstString(uri["authority"])}
	}
	return cursorPlace{}
}

// composers lists the global database's composers.
func (a *cursorAdapter) composers(database string, places map[string]cursorPlace) ([]cursorComposer, error) {
	db, tx, err := a.open(database)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	defer tx.Rollback()
	tables, err := tableNames(tx)
	if err != nil || !tables["cursorDiskKV"] {
		return nil, err
	}
	// A digest of each composer's messages and their order. Cursor stamps no
	// time on a message, and a message can change without changing its size
	// (a token count, an exit code), so the digest is of their content, read
	// in key order.
	digests := map[string]hash.Hash{}
	counts := map[string]int64{}
	for _, prefix := range []string{"bubbleId:", "composerConvHeaders:"} {
		rows, err := tx.QueryContext(a.context(), `SELECT key, value FROM cursorDiskKV WHERE key GLOB ? ORDER BY key`, prefix+"*")
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key string
			var value sql.RawBytes
			if err := rows.Scan(&key, &value); err != nil {
				rows.Close()
				return nil, err
			}
			id, _, _ := strings.Cut(strings.TrimPrefix(key, prefix), ":")
			digest := digests[id]
			if digest == nil {
				digest = sha256.New()
				digests[id] = digest
			}
			fmt.Fprintf(digest, "%s\x00%d\x00", key, len(value))
			digest.Write(value)
			if prefix == "bubbleId:" {
				counts[id]++
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	rows, err := tx.QueryContext(a.context(), `SELECT substr(key, 14), value FROM cursorDiskKV WHERE key GLOB 'composerData:*' AND value IS NOT NULL ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	composers := []cursorComposer{}
	for rows.Next() {
		var id string
		var value []byte
		if err := rows.Scan(&id, &value); err != nil {
			return nil, err
		}
		digest := sha256.Sum256(value)
		composer := cursorComposer{id: id, bubbles: counts[id]}
		var head cursorHead
		// A composer that is not JSON is still planned, so reading it reports
		// it.
		if json.Unmarshal(value, &head) == nil {
			for _, at := range []any{head.CreatedAt, head.LastUpdatedAt, head.Checkpoint} {
				if ms, ok := number(at); ok {
					composer.version = max(composer.version, int64(ms))
				}
			}
			composer.parent = firstString(head.SubagentInfo.Parent)
			composer.place = cursorURIPlace(head.Workspace.URI)
		}
		if composer.place.location == "" {
			composer.place = places[id]
		}
		messages := ""
		if digest := digests[id]; digest != nil {
			messages = hex.EncodeToString(digest.Sum(nil)[:8])
		}
		composer.signal = hex.EncodeToString(digest[:8]) + ":" + messages + ":" + composer.place.location + "\x1f" + composer.place.remote
		composers = append(composers, composer)
	}
	return composers, rows.Err()
}

// planParts plans one group per composer that started its own work, holding
// it and the subagents it started; each composer is one part.
func (a *cursorAdapter) planParts() (*sourceWorkPlan, error) {
	database, err := a.globalDatabase()
	if err != nil {
		return nil, err
	}
	plan := &sourceWorkPlan{continueOnError: true}
	if _, err := a.files().Stat(database); errors.Is(err, os.ErrNotExist) {
		return plan, nil
	}
	places, failures := a.places()
	composers, err := a.composers(database, places)
	if err != nil {
		failures = append(failures, err)
	}
	byID := map[string]cursorComposer{}
	for _, composer := range composers {
		byID[composer.id] = composer
	}
	// A group is the work its top composer started, and keeps that
	// composer's workspace once Cursor deletes it: a subagent still names it.
	root := func(id string) string {
		seen := map[string]bool{}
		for !seen[id] {
			seen[id] = true
			composer, ok := byID[id]
			if !ok || composer.parent == "" {
				break
			}
			id = composer.parent
		}
		return id
	}
	groups := map[string][]cursorComposer{}
	roots := []string{}
	for _, composer := range composers {
		top := root(composer.id)
		if groups[top] == nil {
			roots = append(roots, top)
		}
		groups[top] = append(groups[top], composer)
	}
	origin := a.original(database)
	for _, top := range roots {
		members := groups[top]
		// A selection reads each group holding one of its composers whole:
		// a fork's copy and a subagent's are known only beside the composers
		// they copied, and the group's workspace and each composer's indexed
		// version keep an older snapshot from rolling back what is newer.
		if a.selected != nil && !slices.ContainsFunc(members, func(member cursorComposer) bool { return a.selected[member.id] }) {
			continue
		}
		// The root first, then its subagents a level at a time, each level by
		// ID, so a subagent follows the one that started it.
		levels := map[string]int{}
		for _, member := range members {
			id, seen := member.id, map[string]bool{}
			for id != top && !seen[id] {
				seen[id] = true
				id = byID[id].parent
				levels[member.id]++
			}
		}
		sort.SliceStable(members, func(i, j int) bool { return levels[members[i].id] < levels[members[j].id] })
		parts := make([]sourcePart, 0, len(members))
		for _, member := range members {
			parts = append(parts, sourcePart{item: origin + "#" + member.id, size: member.bubbles, version: member.version * 1e6, signal: member.signal})
		}
		plan.groups = append(plan.groups, plannedPartGroup{parts: parts, read: func(emit partEmitter) error {
			record, err := a.read(database, top, members)
			if err != nil {
				return err
			}
			return emit(record, parts)
		}})
	}
	plan.discoveryError = errors.Join(failures...)
	return plan, nil
}

// snapshotSessions lists the composers in a database snapshot. A superseded
// snapshot's that no newer one holds were deleted in Cursor, and are read
// from it alone.
func (a *cursorAdapter) snapshotSessions(ctx context.Context, file string) (map[string]bool, error) {
	db, err := openReadOnlySQLite(file)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tables, err := tableNames(db)
	if err != nil || !tables["cursorDiskKV"] {
		return map[string]bool{}, err
	}
	rows, err := queryMapsContext(ctx, db, `SELECT substr(key, 14) AS id FROM cursorDiskKV WHERE key GLOB 'composerData:*' AND value IS NOT NULL`)
	ids := map[string]bool{}
	for _, row := range rows {
		ids[firstString(row["id"])] = true
	}
	return ids, err
}

func (a *cursorAdapter) olderReader(file string, view *captureView, selected map[string]bool) Adapter {
	return &cursorAdapter{baseAdapter: baseAdapter{config: a.config, capability: a.capability, view: view}, selected: selected, database: file}
}

// readGroup reads, as indexing does, the group whose parts begin as parts
// do; nothing when that composer is gone.
func (a *cursorAdapter) readGroup(parts []sourcePart) (*WorkspaceRecord, []sourcePart, error) {
	defer a.shareSQLiteCopies()()
	plan, err := a.planParts()
	if err != nil || len(parts) == 0 {
		return nil, nil, err
	}
	for _, group := range plan.groups {
		if group.parts[0].item != parts[0].item {
			continue
		}
		var record *WorkspaceRecord
		var read []sourcePart
		err := group.read(func(value *WorkspaceRecord, parts []sourcePart) error { record, read = value, parts; return nil })
		return record, read, err
	}
	return nil, nil, plan.discoveryError
}

// cursorThread is a composer as read: its data and its messages in order.
type cursorThread struct {
	member  cursorComposer
	data    map[string]any
	bubbles []map[string]any
	// ids are the messages its headers name, in order, whether or not Cursor
	// still holds them; missing those it does not; and short that headers
	// themselves are missing.
	ids     []string
	missing map[string]bool
	short   bool
	// known is how many of ids are where Cursor wrote them: all of them,
	// unless a chunk of headers is lost, which moves those after it.
	known int
	// calls are the tool calls of its own messages, by call ID, and
	// copiedCalls those of the messages a fork copied from its parent.
	calls, copiedCalls map[string]bool
}

// complete reports whether every message the headers name was read.
func (t *cursorThread) complete() bool { return !t.short && len(t.missing) == 0 }

// read reads a composer and its subagents in one transaction, as the
// workspace of top, the composer that started their work.
func (a *cursorAdapter) read(database, top string, members []cursorComposer) (*WorkspaceRecord, error) {
	db, tx, err := a.open(database)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	defer tx.Rollback()
	if _, err := tableNames(tx); err != nil {
		return nil, err
	}
	// Live, the version is read once the transaction has begun reading, so
	// it is no older than what was read.
	version := a.databaseVersion(database)
	origin := a.original(database)
	threads := []*cursorThread{}
	byID := map[string]*cursorThread{}
	// copies are the parents Cursor deleted, as their forks' copies hold
	// them: the longest copy's headers, each message as the newest fork
	// holding it copied it, and when and where the newest fork copied them.
	copies, copied, copiedAt := map[string][]string{}, map[string]map[string]map[string]any{}, map[string]int64{}
	copiedFrom, bubbleAt := map[string]cursorPlace{}, map[string]int64{}
	for _, member := range members {
		thread, err := a.thread(tx, member)
		if err != nil {
			return nil, err
		}
		if thread == nil {
			continue
		}
		info := mapValueDefault(thread.data["subagentInfo"])
		// A fork starts with a copy of its parent's messages, as its parent's
		// headers name them, which are the parent's. A parent read elsewhere
		// holds them; once Cursor deletes the parent, the copy is all that is
		// left of it, and is read as the parent (copies).
		inCopy := map[string]bool{}
		forked := firstString(info["forkedFromComposerId"])
		inherited, _ := number(info["conversationLengthAtSpawn"])
		if n := int(inherited); forked != "" && n > 0 && n <= thread.known {
			parent, gone := byID[forked], false
			if parent == nil {
				var held int
				err := tx.QueryRowContext(a.context(), `SELECT COUNT(*) FROM cursorDiskKV WHERE key=? AND value IS NOT NULL`, "composerData:"+forked).Scan(&held)
				if err != nil {
					return nil, err
				}
				gone = held == 0
			}
			if parent == nil || n <= len(parent.ids) && slices.Equal(thread.ids[:n], parent.ids[:n]) {
				for _, id := range thread.ids[:n] {
					inCopy[id] = true
					delete(thread.missing, id)
				}
			}
			if gone {
				if len(copies[forked]) < n {
					copies[forked] = thread.ids[:n]
				}
				if copied[forked] == nil {
					copied[forked] = map[string]map[string]any{}
				}
				created, _ := number(thread.data["createdAt"])
				at := max(int64(created)*1e6, 1)
				for _, bubble := range thread.bubbles {
					if id := firstString(bubble["bubbleId"]); inCopy[id] && at >= bubbleAt[forked+"\x00"+id] {
						copied[forked][id], bubbleAt[forked+"\x00"+id] = bubble, at
					}
				}
				if at >= copiedAt[forked] {
					copiedAt[forked], copiedFrom[forked] = at, member.place
				}
			}
		}
		thread.calls, thread.copiedCalls = map[string]bool{}, map[string]bool{}
		thread.bubbles = slices.DeleteFunc(thread.bubbles, func(bubble map[string]any) bool {
			own := !inCopy[firstString(bubble["bubbleId"])]
			if call, _ := mapValueDefault(bubble["toolFormerData"])["toolCallId"].(string); call != "" {
				if own {
					thread.calls[call] = true
				} else {
					thread.copiedCalls[call] = true
				}
			}
			return !own
		})
		threads = append(threads, thread)
		byID[member.id] = thread
	}
	for _, id := range slices.Sorted(maps.Keys(copies)) {
		// Partial: Cursor held more of it than a fork copied.
		copy := &cursorThread{member: cursorComposer{id: id, place: copiedFrom[id]}, data: map[string]any{}, ids: copies[id], missing: map[string]bool{}, short: true,
			calls: map[string]bool{}, copiedCalls: map[string]bool{}}
		for _, header := range copy.ids {
			if bubble := copied[id][header]; bubble != nil {
				copy.bubbles = append(copy.bubbles, bubble)
				if call, _ := mapValueDefault(bubble["toolFormerData"])["toolCallId"].(string); call != "" {
					copy.calls[call] = true
				}
			}
		}
		threads = append([]*cursorThread{copy}, threads...)
		byID[id] = copy
	}
	var record *WorkspaceRecord
	depth := map[string]int{}
	echoes := map[string]bool{}
	for _, thread := range threads {
		member := thread.member
		info := mapValueDefault(thread.data["subagentInfo"])
		spawn, _ := info["toolCallId"].(string)
		if member.parent != "" && byID[member.parent] != nil {
			if echoes[member.parent] {
				echoes[member.id] = true
				continue
			}
			// A fork's copy of its parent's messages starts its own copies of
			// the subagents those messages started: a subagent started by a
			// copied call, or by a call that is not its parent's, is one of
			// these, left out while the subagent it copies is read whole.
			owned, original := false, false
			for _, other := range threads {
				owned = owned || other.member.id != member.parent && other.calls[spawn]
				original = original || other != thread && !echoes[other.member.id] && other.complete() &&
					firstString(mapValueDefault(other.data["subagentInfo"])["toolCallId"]) == spawn
			}
			if spawn != "" && original && (byID[member.parent].copiedCalls[spawn] || !byID[member.parent].calls[spawn] && owned) {
				echoes[member.id] = true
				continue
			}
		}
		conversation := a.conversation(origin, thread, spawn != "")
		if conversation == nil {
			continue
		}
		_, conversation.Copy = copies[member.id]
		// Linked to its composer even when that is not read with it: deleted
		// in Cursor, or not selected.
		if member.parent != "" {
			conversation.ParentNativeID = member.parent
			conversation.AgentNickname, conversation.AgentPath = firstString(thread.data["name"]), firstString(info["subagentTypeName"])
			if spawn != "" {
				depth[member.id] = depth[member.parent] + 1
				conversation.AgentDepth = max(depth[member.id], 1)
			}
		}
		if record == nil {
			purpose, outcome := agentPurposeOutcome(conversation.Messages)
			title := firstString(thread.data["name"])
			if !meaningfulWorkspaceTitle(title, member.id) {
				title = defaultString(purpose, "Cursor "+short(member.id))
			}
			// The composer that started the work names it; once Cursor
			// deletes it, its subagents leave the name and purpose to the
			// copy indexed before.
			if member.id != top {
				title, purpose, outcome = "Cursor "+short(top), "", ""
			}
			metadata := map[string]any{}
			if mode := firstString(thread.data["unifiedMode"]); mode != "" {
				metadata["mode"] = mode
			}
			repository := map[string]any(nil)
			if member.place.remote != "" {
				metadata["remote"] = member.place.remote
				repository = repositoryFromName(member.place.location, "")
			} else if member.place.location != "" {
				repository = a.repository(member.place.location, "")
			}
			record = &WorkspaceRecord{SourceID: top, SourceKind: "cursor", Title: title, Account: a.config.Account, Purpose: purpose, Outcome: outcome, ActivityAt: conversation.EndedAt,
				Location: member.place.location, Repository: repository, Metadata: metadata}
		}
		record.Conversations = append(record.Conversations, *conversation)
	}
	if record == nil {
		return nil, nil
	}
	observe(record, version)
	// A copy is as new as the newest fork that made it, so a read of the
	// parent itself replaces it once it is newer, holding all the copy holds.
	for index, conversation := range record.Conversations {
		if at := copiedAt[conversation.NativeID]; at > 0 {
			record.Conversations[index].Observed = min(at, version)
		}
	}
	return record, nil
}

// thread reads a composer and its messages, in the order its headers give:
// the composer's own list, or from Cursor 3 its chunks of headers, or, from
// releases before either, the messages it holds itself. A message a header
// names that is not there makes the composer partial.
func (a *cursorAdapter) thread(tx *sql.Tx, member cursorComposer) (*cursorThread, error) {
	var value []byte
	err := tx.QueryRowContext(a.context(), `SELECT value FROM cursorDiskKV WHERE key=?`, "composerData:"+member.id).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) || err == nil && value == nil {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	thread := &cursorThread{member: member, missing: map[string]bool{}}
	if err := json.Unmarshal(value, &thread.data); err != nil || thread.data == nil {
		return nil, fmt.Errorf("Cursor composer %s: %v", member.id, defaultString(errorText(err), "not an object"))
	}
	if inline, ok := thread.data["conversation"].([]any); ok && len(inline) > 0 {
		for _, raw := range inline {
			if bubble := mapValue(raw); bubble != nil {
				thread.bubbles = append(thread.bubbles, bubble)
				thread.ids = append(thread.ids, firstString(bubble["bubbleId"]))
			}
		}
		thread.known = len(thread.ids)
		return thread, nil
	}
	headers, _ := thread.data["fullConversationHeadersOnly"].([]any)
	gap := -1
	if log := mapValue(thread.data["headerLog"]); len(headers) == 0 && log != nil {
		chunks, _ := number(log["chunkCount"])
		for chunk := 0; chunk < int(chunks); chunk++ {
			var raw []byte
			err := tx.QueryRowContext(a.context(), `SELECT value FROM cursorDiskKV WHERE key=?`, fmt.Sprintf("composerConvHeaders:%s:%06d", member.id, chunk)).Scan(&raw)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
			// A chunk lost or damaged loses its headers, as a missing message
			// loses its own: the composer is partial, and the rest are read.
			var list []any
			if err != nil || json.Unmarshal(raw, &list) != nil || list == nil {
				thread.short = true
				if gap < 0 {
					gap = len(headers)
				}
				continue
			}
			headers = append(headers, list...)
		}
		if count, ok := number(log["count"]); ok && int(count) > len(headers) {
			thread.short = true
		}
	}
	thread.known = len(headers)
	if gap >= 0 {
		thread.known = gap
	}
	if len(headers) == 0 {
		return thread, nil
	}
	prefix := "bubbleId:" + member.id + ":"
	rows, err := tx.QueryContext(a.context(), `SELECT key, value FROM cursorDiskKV WHERE key >= ? AND key < ?`, prefix, "bubbleId:"+member.id+";")
	if err != nil {
		return nil, err
	}
	stored := map[string][]byte{}
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		stored[strings.TrimPrefix(key, prefix)] = raw
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Older composers hold their messages in conversationMap, by ID.
	legacy := mapValueDefault(thread.data["conversationMap"])
	for _, raw := range headers {
		id, _ := mapValueDefault(raw)["bubbleId"].(string)
		thread.ids = append(thread.ids, id)
		var bubble map[string]any
		if data := stored[id]; data == nil || json.Unmarshal(data, &bubble) != nil || bubble == nil {
			if bubble = mapValue(legacy[id]); bubble == nil {
				thread.missing[id] = true
				continue
			}
		}
		bubble["bubbleId"] = id
		thread.bubbles = append(thread.bubbles, bubble)
	}
	return thread, nil
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// conversation builds a composer's conversation from its messages; nil when
// it has none. spawned marks a subagent a tool call started, whose prompt is
// its parent's.
func (a *cursorAdapter) conversation(origin string, thread *cursorThread, spawned bool) *ConversationRecord {
	data, id := thread.data, thread.member.id
	model := firstString(mapValueDefault(data["modelConfig"])["modelName"])
	if model == "" {
		for _, bubble := range thread.bubbles {
			if model = firstString(mapValueDefault(bubble["modelInfo"])["modelName"]); model != "" {
				break
			}
		}
	}
	started := iso(data["createdAt"])
	conversation := &ConversationRecord{NativeID: id, Provider: "cursor", Account: a.config.Account, Origin: origin, Coverage: "complete", Harness: "cursor", Model: model, StartedAt: started}
	if !thread.complete() {
		conversation.Coverage = "partial"
		// Headers lost with a chunk name messages no one can list.
		if !thread.short {
			conversation.Unread = slices.Sorted(maps.Keys(thread.missing))
		}
	}
	context := map[string]any{}
	for key, field := range map[string]string{"context_tokens_used": "contextTokensUsed", "context_token_limit": "contextTokenLimit"} {
		if value, ok := number(data[field]); ok && value > 0 {
			context[key] = value
		}
	}
	if fork := firstString(mapValueDefault(data["subagentInfo"])["forkedFromComposerId"]); fork != "" {
		context["forked_from"] = fork
	}
	if len(context) > 0 {
		conversation.NativeContext = context
	}
	at := started
	for _, bubble := range thread.bubbles {
		bubbleID, _ := bubble["bubbleId"].(string)
		if bubbleID == "" {
			continue
		}
		// A message's own time; else, before Cursor recorded one, when its
		// request was sent; else the time of the message before it.
		if created := iso(firstNonNil(bubble["createdAt"], mapValueDefault(bubble["timingInfo"])["clientRpcSendTime"])); created != "" {
			at = created
		}
		base := MessageRecord{NativeID: bubbleID, Model: defaultString(mapValueDefault(bubble["modelInfo"])["modelName"], model), CreatedAt: at,
			EvidenceLocator: fmt.Sprintf("%s#%s:%s", origin, id, bubbleID), Selected: true,
			// The bubble's ID stands in for its raw text, so text quoting usage
			// JSON is never read as token usage.
			RawText: jsonText(map[string]any{"type": "cursor_bubble", "id": bubbleID})}
		add := func(item MessageRecord) { conversation.Messages = append(conversation.Messages, item) }
		text, _ := bubble["text"].(string)
		kind, _ := number(bubble["type"])
		switch kind {
		case 1:
			if strings.TrimSpace(text) == "" {
				continue
			}
			item := base
			item.Role, item.Kind, item.Text = "user", "message", text
			if spawned {
				item.Role, item.Sender = agentRole, "agent:"+firstString(mapValueDefault(data["subagentInfo"])["parentComposerId"])
			}
			add(item)
		case 2:
			if thought, _ := mapValueDefault(bubble["thinking"])["text"].(string); strings.TrimSpace(thought) != "" {
				item := base
				item.NativeID = bubbleID + ":thinking"
				add(reasoningRecord(item, thought, "text", ""))
			}
			if strings.TrimSpace(text) != "" {
				item := base
				item.Role, item.Kind, item.Text = "assistant", "message", text
				add(item)
			}
			if tool := mapValue(bubble["toolFormerData"]); tool != nil {
				cursorTool(base, bubble, tool, add)
			}
			if failure := firstString(mapValueDefault(bubble["errorDetails"])["message"]); failure != "" {
				item := base
				item.NativeID, item.Role, item.Kind, item.Text = bubbleID+":error", "system", "error", failure
				add(item)
			}
			// Before Cursor 3, a request's last reply holds its token count.
			tokens := mapValueDefault(bubble["tokenCount"])
			input, _ := number(tokens["inputTokens"])
			output, _ := number(tokens["outputTokens"])
			if input > 0 || output > 0 {
				item := base
				item.NativeID, item.Role, item.Kind, item.RawText = bubbleID+":usage", "system", "metadata", ""
				item.Text = jsonText(map[string]any{"type": "token_usage", "usage": map[string]any{"input_tokens": input, "output_tokens": output}, "usage_message_id": bubbleID})
				add(item)
			}
		}
		if completed := iso(bubble["completedAtMs"]); completed != "" {
			at = max(at, completed)
		}
	}
	if len(conversation.Messages) == 0 {
		return nil
	}
	conversation.EndedAt = at
	if updated := iso(data["lastUpdatedAt"]); updated > at {
		conversation.EndedAt = updated
	}
	return conversation
}

// cursorTool adds a tool bubble's call and, once it has run, its result.
func cursorTool(base MessageRecord, bubble, tool map[string]any, add func(MessageRecord)) {
	name, _ := tool["name"].(string)
	if name == "" {
		return
	}
	callID, _ := tool["toolCallId"].(string)
	callID = defaultString(callID, base.NativeID)
	// The model's arguments, else, in Cursor 3 where they are often left out,
	// what Cursor ran.
	input := cursorJSON(tool["rawArgs"])
	if object, ok := input.(map[string]any); input == nil || ok && len(object) == 0 {
		input = firstNonNil(cursorJSON(tool["params"]), input)
	}
	delegated := matchesFold(name, "task", "task_v2")
	call := base
	call.NativeID, call.CallID, call.Role, call.Kind = base.NativeID+":call", callID, "assistant", "tool_call"
	if delegated {
		call.Kind = "delegation"
	}
	call.Text = jsonText(map[string]any{"tool": name, "input": input})
	add(call)
	status := firstString(tool["status"])
	if status != "completed" && status != "error" && status != "cancelled" {
		return
	}
	result := base
	result.NativeID, result.CallID, result.Role, result.Kind = base.NativeID+":result", callID, "tool", "tool_result"
	if delegated {
		result.Kind = "delegation_result"
	}
	if completed := iso(bubble["completedAtMs"]); completed != "" {
		result.CreatedAt = completed
	}
	content := cursorJSON(tool["result"])
	details := map[string]any{}
	if output := mapValue(content); output != nil {
		if exit, ok := number(firstNonNil(output["exitCode"], output["exitCodeV2"])); ok {
			details["exit_code"] = exit
		}
		// A command's result is what it printed; its exit code, and whether
		// it was stopped or the person declined it, are details.
		if text, ok := output["output"].(string); ok {
			content = text
			if output["notInterrupted"] == false {
				details["interrupted"] = true
			}
			if output["rejected"] == true {
				details["status"] = "declined"
			}
		}
	}
	extra := mapValueDefault(cursorJSON(tool["additionalData"]))
	if started, ok := number(firstNonNil(bubble["startedAtMs"], extra["startedAtMs"])); ok {
		if completed, ok := number(bubble["completedAtMs"]); ok && completed >= started {
			details["duration_ms"] = completed - started
		}
	}
	if subagent := firstString(extra["subagentComposerId"]); subagent != "" {
		details["subagent"] = subagent
	}
	// A call cancelled while it ran was interrupted.
	if status == "cancelled" {
		details["cancelled"], details["interrupted"] = true, true
	}
	if status == "error" {
		failure := mapValueDefault(cursorJSON(tool["error"]))
		if text := firstString(failure["modelVisibleErrorMessage"], failure["clientVisibleErrorMessage"]); text != "" {
			details["error"] = text
			if content == nil {
				content = text
			}
		}
	}
	payload := map[string]any{"tool": name, "content": content, "is_error": status == "error"}
	if len(details) > 0 {
		payload["details"] = details
	}
	result.Text = jsonText(payload)
	add(result)
}

// cursorJSON is a field Cursor stores as JSON text, decoded; an object as
// it is; nil when empty.
func cursorJSON(value any) any {
	text, ok := value.(string)
	if !ok {
		return value
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var decoded any
	if json.Unmarshal([]byte(text), &decoded) != nil {
		return text
	}
	return decoded
}

// cursorProbeStats measures a global database for the probe: its composers
// and when they were first and last active.
func cursorProbeStats(database string) (probeStats, error) {
	stats := probeStats{}
	info, err := os.Stat(database)
	if err != nil {
		return stats, err
	}
	db, err := openSourceSQLite(context.Background(), database)
	if err != nil {
		return stats, err
	}
	defer db.Close()
	tables, err := tableNames(db)
	if err != nil || !tables["cursorDiskKV"] {
		return stats, err
	}
	var oldest, newest sql.NullInt64
	// Composers with a message: drafts and empty chats have none.
	if err := db.QueryRow(`SELECT COUNT(*), MIN(json_extract(v, '$.createdAt')), MAX(COALESCE(json_extract(v, '$.lastUpdatedAt'), json_extract(v, '$.createdAt')))
		FROM (SELECT CAST(value AS TEXT) AS v FROM cursorDiskKV WHERE key GLOB 'composerData:*' AND value IS NOT NULL)
		WHERE json_valid(v) AND COALESCE(json_array_length(v, '$.fullConversationHeadersOnly'), 0) + COALESCE(json_extract(v, '$.headerLog.count'), 0) + COALESCE(json_array_length(v, '$.conversation'), 0) > 0`).Scan(&stats.sessions, &oldest, &newest); err != nil {
		return stats, err
	}
	stats.files, stats.bytes = 1, info.Size()
	if oldest.Valid {
		stats.oldest, stats.newest = time.UnixMilli(oldest.Int64), time.UnixMilli(newest.Int64)
	}
	return stats, nil
}
