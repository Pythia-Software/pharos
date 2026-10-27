package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// InitiationRecord is evidence supplied by an adapter or a canonical producer.
// A descendant's initiation is always resolved through its causal parent.
type InitiationRecord struct {
	Channel          string         `json:"channel,omitempty"`
	Machine          *MachineRecord `json:"machine,omitempty"`
	ExecutionMachine *MachineRecord `json:"execution_machine,omitempty"`
	Evidence         string         `json:"evidence,omitempty"`
}
type MachineRecord struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

var originDimensions = []string{"channel", "machine_id", "machine", "machine_kind", "execution_machine_id", "execution_machine", "execution_machine_kind"}

var originFields = []string{"channel", "machine_id", "machine", "machine_kind", "execution_machine_id", "execution_machine", "execution_machine_kind", "channel_status", "machine_status", "execution_machine_status", "origin_conversation_id", "origin_evidence"}

const originDDL = `
CREATE TABLE IF NOT EXISTS machines(id TEXT PRIMARY KEY,label TEXT NOT NULL,kind TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS host_machines(host_id TEXT PRIMARY KEY,machine_id TEXT NOT NULL REFERENCES machines(id));
CREATE TABLE IF NOT EXISTS origin_observations(
 conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
 evidence_key TEXT NOT NULL,channel TEXT,machine_id TEXT,execution_machine_id TEXT,
 priority INTEGER NOT NULL,evidence TEXT NOT NULL,parent_native_id TEXT,
 channel_priority INTEGER,machine_priority INTEGER,execution_priority INTEGER,
 parent_provider TEXT,parent_account TEXT,
 PRIMARY KEY(conversation_id,evidence_key));
CREATE TABLE IF NOT EXISTS conversation_origins(
 conversation_id TEXT PRIMARY KEY REFERENCES conversations(id) ON DELETE CASCADE,
 channel TEXT,machine_id TEXT,machine TEXT,machine_kind TEXT,
 execution_machine_id TEXT,execution_machine TEXT,execution_machine_kind TEXT,
 channel_status TEXT,machine_status TEXT,execution_machine_status TEXT,
 origin_conversation_id TEXT,origin_evidence TEXT);
CREATE INDEX IF NOT EXISTS conversation_origins_channel_idx ON conversation_origins(channel,conversation_id);
CREATE INDEX IF NOT EXISTS conversation_origins_machine_idx ON conversation_origins(machine_id,conversation_id);
CREATE TABLE IF NOT EXISTS origin_state(id INTEGER PRIMARY KEY CHECK(id=1),revision INTEGER NOT NULL,built INTEGER NOT NULL);
INSERT OR IGNORE INTO origin_state VALUES(1,1,0);
`

func (c *Catalog) ensureOriginSchema() error {
	if _, err := c.DB.Exec(originDDL); err != nil {
		return err
	}
	var version string
	if err := c.DB.QueryRow("SELECT value FROM meta WHERE key='origin_resolver_version'").Scan(&version); err != nil && err != sql.ErrNoRows {
		return err
	}
	if version != "1" {
		if _, err := c.DB.Exec("UPDATE origin_state SET revision=revision+1 WHERE id=1"); err != nil {
			return err
		}
		if _, err := c.DB.Exec("INSERT INTO meta(key,value) VALUES('origin_resolver_version','1') ON CONFLICT(key) DO UPDATE SET value=excluded.value"); err != nil {
			return err
		}
	}
	for _, table := range []string{"conversations", "agent_sessions", "conversation_identity_links", "conversation_launches", "origin_observations", "machines", "host_machines", "workspaces", "conversation_sightings"} {
		for _, event := range []string{"INSERT", "UPDATE", "DELETE"} {
			// Metadata and copy-authority updates need not traverse the graph again.
			when := ""
			if event == "UPDATE" && table == "workspaces" {
				when = " WHEN OLD.source_kind IS NOT NEW.source_kind"
			}
			if event == "UPDATE" && table == "agent_sessions" {
				when = " WHEN OLD.native_id IS NOT NEW.native_id OR OLD.kind IS NOT NEW.kind OR OLD.conversation_id IS NOT NEW.conversation_id OR OLD.parent_id IS NOT NEW.parent_id OR OLD.child_conversation_id IS NOT NEW.child_conversation_id"
			}
			if event == "UPDATE" && table == "conversation_sightings" {
				when = " WHEN OLD.host_id IS NOT NEW.host_id OR OLD.conversation_id IS NOT NEW.conversation_id"
			}
			if event == "UPDATE" && table == "conversations" {
				when = " WHEN OLD.parent_id IS NOT NEW.parent_id OR OLD.agent_depth IS NOT NEW.agent_depth OR OLD.harness IS NOT NEW.harness OR OLD.workspace_id IS NOT NEW.workspace_id OR OLD.provider IS NOT NEW.provider OR OLD.account IS NOT NEW.account OR OLD.native_id IS NOT NEW.native_id OR OLD.aliases_json IS NOT NEW.aliases_json"
			}
			name := fmt.Sprintf("origin_dirty_%s_%s", table, event)
			if _, err := c.DB.Exec("DROP TRIGGER IF EXISTS " + name); err != nil {
				return err
			}
			if _, err := c.DB.Exec(fmt.Sprintf(`CREATE TRIGGER %s AFTER %s ON %s%s BEGIN UPDATE origin_state SET revision=revision+1 WHERE id=1; END`, name, event, table, when)); err != nil {
				return err
			}
		}
	}
	for _, field := range []string{"parent_provider", "parent_account"} {
		if has, err := c.hasColumn("origin_observations", field); err != nil {
			return err
		} else if !has {
			if _, err := c.DB.Exec("ALTER TABLE origin_observations ADD COLUMN " + field + " TEXT"); err != nil {
				return err
			}
		}
	}
	for _, field := range []string{"channel_priority", "machine_priority", "execution_priority"} {
		if has, err := c.hasColumn("origin_observations", field); err != nil {
			return err
		} else if !has {
			if _, err := c.DB.Exec("ALTER TABLE origin_observations ADD COLUMN " + field + " INTEGER"); err != nil {
				return err
			}
		}
	}
	for _, field := range originDimensions {
		if has, err := c.hasColumn("tool_usage_daily", field); err != nil {
			return err
		} else if !has {
			if _, err := c.DB.Exec("ALTER TABLE tool_usage_daily ADD COLUMN " + field + " TEXT"); err != nil {
				return err
			}
		}
	}

	return nil
}

func putMachine(tx *sql.Tx, m *MachineRecord) (any, error) {
	if m == nil || strings.TrimSpace(m.ID) == "" {
		return nil, nil
	}
	if m.Kind == "" {
		m = &MachineRecord{m.ID, m.Label, "device"}
	}
	if !slices.Contains([]string{"device", "web", "cloud"}, m.Kind) {
		return nil, fmt.Errorf("unknown machine kind %q", m.Kind)
	}
	_, err := tx.Exec(`INSERT INTO machines VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET label=excluded.label,kind=excluded.kind WHERE machines.label<>excluded.label OR machines.kind<>excluded.kind`, m.ID, defaultString(m.Label, m.ID), m.Kind)
	return m.ID, err
}
func normalizedChannel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if slices.Contains([]string{"conductor", "tl1", "cli", "desktop", "web", "ide", "api"}, value) {
		return value
	}
	return ""
}

// Only positively identified entrypoints establish a channel. An SDK, a bare
// provider, and an export do not identify the initiating app.
func harnessChannel(harness, source string) (string, int) {
	switch source {
	case "conductor":
		return "conductor", 20
	case "tl1":
		return "tl1", 20
	}
	switch strings.ToLower(harness) {
	case "conductor":
		return "conductor", 20
	case "tl1":
		return "tl1", 20
	case "codex/codex_cli_rs", "codex/codex_exec", "codex/cli", "claude-code/cli", "antigravity-cli":
		return "cli", 10
	case "codex/codex_desktop", "codex/codex-desktop", "codex/codex desktop", "codex/desktop":
		return "desktop", 10
	case "codex/vscode", "codex/codex_vscode", "claude-code/vscode", "antigravity":
		return "ide", 10
	}
	return "", 0
}

// Keep only attribution metadata: session headers can also contain very large
// instruction/prompt payloads that do not belong in origin evidence.
func nativeOriginContext(meta map[string]any) map[string]any {
	result := map[string]any{}
	for _, key := range []string{"originator", "source", "cli_version", "parent_thread_id", "agent_path", "platform", "machine_id"} {
		if v, ok := meta[key]; ok {
			result[key] = v
		}
	}
	return result
}

func recordOriginObservation(tx *sql.Tx, id, host, source string, cv ConversationRecord) error {
	ch, priority := harnessChannel(cv.Harness, source)
	if source == "codex" && firstString(cv.NativeContext["originator"]) != "" {
		ch, priority = harnessChannel("codex/"+firstString(cv.NativeContext["originator"]), "")
	}
	if source == "claude" && firstString(cv.NativeContext["entrypoint"]) != "" {
		ch, priority = harnessChannel("claude-code/"+firstString(cv.NativeContext["entrypoint"]), "")
	}
	if ch == "" && firstString(cv.NativeContext["originator"], cv.NativeContext["entrypoint"]) == "" {
		if marker, ok := cv.NativeContext["source"].(string); ok {
			switch marker {
			case "cli", "exec":
				ch, priority = "cli", 10
			case "vscode":
				ch, priority = "ide", 10
			case "desktop":
				ch, priority = "desktop", 10
			case "web":
				ch, priority = "web", 10
			}
		}
	}
	machinePriority, executionPriority := 5, 5
	var machine, execution any
	evidence := cv.Harness
	if len(cv.NativeContext) > 0 {
		data, err := json.Marshal(cv.NativeContext)
		if err != nil {
			return err
		}
		evidence = string(data)
	}
	if cv.Initiation != nil {
		v := cv.Initiation
		if v.Channel != "" {
			ch = normalizedChannel(v.Channel)
			if ch == "" {
				return fmt.Errorf("unknown initiation channel %q", v.Channel)
			}
			priority = 30
		}
		var err error
		machine, err = putMachine(tx, v.Machine)
		if err != nil {
			return err
		}
		execution, err = putMachine(tx, v.ExecutionMachine)
		if err != nil {
			return err
		}
		if machine != nil {
			machinePriority = 30
		}
		if execution != nil {
			executionPriority = 30
		}
		evidence = firstString(v.Evidence, evidence, "canonical initiation")
	}
	// The capture's source host is supplied here, including when indexing off-host.
	// Imported exports deliberately never acquire the importing device.
	if execution == nil && slices.Contains([]string{"codex", "claude", "conductor", "tl1", "antigravity"}, source) {
		var local string
		if err := tx.QueryRow("SELECT machine_id FROM host_machines WHERE host_id=?", host).Scan(&local); err != nil && err != sql.ErrNoRows {
			return err
		} else if err == nil {
			execution = local
		}
	}
	if machine == nil && execution != nil {
		machine = execution
	}
	key := host + "|" + source + "|" + cv.Origin
	_, err := tx.Exec(`INSERT INTO origin_observations(conversation_id,evidence_key,channel,machine_id,execution_machine_id,priority,evidence,parent_native_id,channel_priority,machine_priority,execution_priority,parent_provider,parent_account) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(conversation_id,evidence_key) DO UPDATE SET channel=excluded.channel,machine_id=COALESCE(excluded.machine_id,origin_observations.machine_id),execution_machine_id=COALESCE(excluded.execution_machine_id,origin_observations.execution_machine_id),priority=excluded.priority,evidence=excluded.evidence,parent_native_id=excluded.parent_native_id,channel_priority=excluded.channel_priority,machine_priority=excluded.machine_priority,execution_priority=excluded.execution_priority,parent_provider=excluded.parent_provider,parent_account=excluded.parent_account
 WHERE origin_observations.channel IS NOT excluded.channel OR (excluded.machine_id IS NOT NULL AND origin_observations.machine_id IS NOT excluded.machine_id) OR (excluded.execution_machine_id IS NOT NULL AND origin_observations.execution_machine_id IS NOT excluded.execution_machine_id) OR origin_observations.priority<>excluded.priority OR origin_observations.evidence<>excluded.evidence OR origin_observations.parent_native_id IS NOT excluded.parent_native_id OR origin_observations.channel_priority IS NOT excluded.channel_priority OR origin_observations.machine_priority IS NOT excluded.machine_priority OR origin_observations.execution_priority IS NOT excluded.execution_priority OR origin_observations.parent_provider IS NOT excluded.parent_provider OR origin_observations.parent_account IS NOT excluded.parent_account`, id, key, nilIfEmpty(ch), machine, execution, priority, evidence, nilIfEmpty(cv.ParentNativeID), priority, machinePriority, executionPriority, nilIfEmpty(cv.ParentProvider), nilIfEmpty(cv.ParentAccount))
	return err
}

// Origins are materialized once per graph revision, shared by all datasets.
// Source observations are never overwritten by another copy's authority.
func (c *Catalog) ensureOrigins(ctx context.Context) error {
	if c.queryOnly {
		return ctx.Err()
	}
	c.originsMu.Lock()
	defer c.originsMu.Unlock()
	var dirty bool
	if err := c.reader().QueryRowContext(ctx, "SELECT revision<>built FROM origin_state WHERE id=1").Scan(&dirty); err != nil || !dirty {
		return err
	}
	return c.writeTransaction(ctx, "conversation-origins", func(tx *sql.Tx) error {
		var revision, built int64
		if err := tx.QueryRowContext(ctx, "SELECT revision,built FROM origin_state WHERE id=1").Scan(&revision, &built); err != nil {
			return err
		}
		if revision == built {
			return nil
		}
		rows, err := computeOrigins(ctx, tx)
		if err != nil {
			return err
		}
		// Linked child executions also supply attribution for tool calls stored
		// in their parent's workspace, through the session's child handle.
		linked, err := queryMapsContext(ctx, tx, `SELECT DISTINCT child_conversation_id,workspace_id FROM agent_sessions WHERE child_conversation_id IS NOT NULL`)
		if err != nil {
			return err
		}
		dependentWorkspaces := map[string][]string{}
		for _, session := range linked {
			id := firstString(session["child_conversation_id"])
			dependentWorkspaces[id] = append(dependentWorkspaces[id], firstString(session["workspace_id"]))
		}
		cols := append([]string{"conversation_id"}, originFields...)
		updates, changes := []string{}, []string{}
		for _, f := range originFields {
			updates = append(updates, f+"=excluded."+f)
			changes = append(changes, "conversation_origins."+f+" IS NOT excluded."+f)
		}
		statement, err := tx.PrepareContext(ctx, `INSERT INTO conversation_origins(`+strings.Join(cols, ",")+`) VALUES(`+placeholders(len(cols))+`) ON CONFLICT(conversation_id) DO UPDATE SET `+strings.Join(updates, ",")+" WHERE "+strings.Join(changes, " OR "))
		if err != nil {
			return err
		}
		defer statement.Close()
		dirtyWorkspaces := map[string]bool{}
		for _, row := range rows {
			values := make([]any, len(cols))
			for i, f := range cols {
				values[i] = row[f]
			}
			result, err := statement.ExecContext(ctx, values...)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n > 0 {
				dirtyWorkspaces[firstString(row["workspace_id"])] = true
				for _, workspace := range dependentWorkspaces[firstString(row["conversation_id"])] {
					dirtyWorkspaces[workspace] = true
				}
			}
		}
		for workspace := range dirtyWorkspaces {
			if _, err := tx.ExecContext(ctx, `INSERT INTO tool_rollup_dirty VALUES(?,hex(randomblob(16))) ON CONFLICT(workspace_id) DO UPDATE SET revision=excluded.revision`, workspace); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE origin_state SET built=? WHERE id=1", revision)
		return err
	})
}

// computeOrigins reads one snapshot and never writes, including on the MCP light path.
func computeOrigins(ctx context.Context, q queryer) ([]map[string]any, error) {
	cvs, err := queryMapsContext(ctx, q, `SELECT c.id,c.workspace_id,c.native_id,c.account,c.provider,c.parent_id,c.agent_depth,c.harness,c.aliases_json,w.source_kind FROM conversations c JOIN workspaces w ON w.id=c.workspace_id ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	sessions, err := queryMapsContext(ctx, q, "SELECT id,native_id,kind,conversation_id,parent_id,child_conversation_id FROM agent_sessions ORDER BY id")
	if err != nil {
		return nil, err
	}
	links, err := queryMapsContext(ctx, q, "SELECT left_id,right_id FROM conversation_identity_links ORDER BY left_id,right_id")
	if err != nil {
		return nil, err
	}
	launches, err := queryMapsContext(ctx, q, "SELECT child_id,parent_id FROM conversation_launches ORDER BY child_id,parent_id")
	if err != nil {
		return nil, err
	}
	observations, err := queryMapsContext(ctx, q, "SELECT * FROM origin_observations ORDER BY conversation_id,evidence_key")
	if err != nil {
		return nil, err
	}
	// Historical native sightings are usable only if every seen host has a
	// device mapping and they all agree. Exports never inherit their download host.
	historical, err := queryMapsContext(ctx, q, `SELECT s.conversation_id,MIN(hm.machine_id) machine_id FROM conversation_sightings s
          JOIN conversations c ON c.id=s.conversation_id JOIN workspaces w ON w.id=c.workspace_id
          LEFT JOIN host_machines hm ON hm.host_id=s.host_id
          WHERE w.source_kind IN ('codex','claude','conductor','tl1','antigravity')
          GROUP BY s.conversation_id HAVING COUNT(DISTINCT hm.machine_id)=1
          AND COUNT(DISTINCT s.host_id)=COUNT(DISTINCT CASE WHEN hm.machine_id IS NOT NULL THEN s.host_id END)`)
	if err != nil {
		return nil, err
	}
	for _, h := range historical {
		observations = append(observations, map[string]any{"conversation_id": h["conversation_id"], "machine_id": h["machine_id"], "execution_machine_id": h["machine_id"], "priority": int64(1), "evidence": "native source host (inferred)"})
	}
	machineRows, err := queryMapsContext(ctx, q, "SELECT * FROM machines")
	if err != nil {
		return nil, err
	}
	machines := map[string]map[string]any{}
	for _, m := range machineRows {
		machines[firstString(m["id"])] = m
	}
	byID := map[string]map[string]any{}
	native := map[string]string{}
	for _, cv := range cvs {
		id := firstString(cv["id"])
		byID[id] = cv
		native[firstString(cv["provider"])+"|"+firstString(cv["account"])+"|"+firstString(cv["native_id"])] = id
	}
	// A wrapper can declare a parent's native ID before its native transcript
	// arrives. Do not arbitrarily resolve an alias claimed by unrelated wrappers.
	aliasClaims := map[string]string{}
	ambiguous := map[string]bool{}
	for _, cv := range cvs {
		var aliases []string
		_ = json.Unmarshal([]byte(firstString(cv["aliases_json"])), &aliases)
		for _, alias := range aliases {
			key := firstString(cv["provider"]) + "|" + firstString(cv["account"]) + "|" + alias
			if prior := aliasClaims[key]; prior != "" && prior != cv["id"] {
				ambiguous[key] = true
			}
			aliasClaims[key] = firstString(cv["id"])
		}
	}
	for key, id := range aliasClaims {
		if native[key] == "" && !ambiguous[key] {
			native[key] = id
		}
	}
	// Retained native parent IDs let children arriving before parents resolve later.
	for _, o := range observations {
		cv := byID[firstString(o["conversation_id"])]
		if cv == nil {
			continue
		}
		if parent := firstString(o["parent_native_id"]); parent != "" {
			key := firstString(o["parent_provider"], cv["provider"]) + "|" + firstString(o["parent_account"], cv["account"]) + "|" + parent
			parents, _ := cv["recorded_parent_ids"].([]string)
			cv["recorded_parent_ids"] = append(parents, defaultString(native[key], "missing:"+key))
		}
	}
	works, seenWorks, workspaceLinks := []map[string]any{}, map[string]bool{}, []map[string]any{}
	for _, cv := range cvs {
		w := firstString(cv["workspace_id"])
		if !seenWorks[w] {
			works = append(works, map[string]any{"id": w, "source_kind": cv["source_kind"]})
			seenWorks[w] = true
		}
	}
	for _, link := range links {
		a, b := byID[firstString(link["left_id"])], byID[firstString(link["right_id"])]
		if a != nil && b != nil {
			workspaceLinks = append(workspaceLinks, map[string]any{"left_workspace": a["workspace_id"], "right_workspace": b["workspace_id"]})
		}
	}
	representatives := []map[string]any{}
	for _, work := range suppressMirrorRows(works, workspaceLinks) {
		representatives = append(representatives, map[string]any{"workspace_id": work["id"]})
	}
	lineage := resolveUsageConversationRoots(cvs, sessions, links, launches, representatives)
	// Evidence belongs to the mirror identity, including suppressed wrappers.
	observed := map[string][]map[string]any{}
	hasOriginObservation := map[string]bool{}
	for _, o := range observations {
		id := lineage.canonical[firstString(o["conversation_id"])]
		if o["evidence_key"] != nil {
			hasOriginObservation[firstString(o["conversation_id"])] = true
		}
		observed[id] = append(observed[id], o)
	}
	for _, cv := range cvs {
		id := firstString(cv["id"])
		canonical := lineage.canonical[id]
		ch, p := harnessChannel(firstString(cv["harness"]), firstString(cv["source_kind"]))
		if ch != "" && !hasOriginObservation[id] {
			observed[canonical] = append(observed[canonical], map[string]any{"channel": ch, "priority": int64(p), "evidence": cv["harness"]})
		}
	}
	rows := make([]map[string]any, 0, len(cvs))
	for _, cv := range cvs {
		id := firstString(cv["id"])
		canonical := lineage.canonical[id]
		root := lineage.roots[id]
		row := map[string]any{"conversation_id": id, "channel_status": "unknown", "machine_status": "unknown", "execution_machine_status": "unknown"}
		exec, status, evidence := originClaim(observed[canonical], "execution_machine_id")
		row["execution_machine_id"], row["execution_machine_status"] = nilIfEmpty(exec), status
		if m := machines[exec]; m != nil {
			row["execution_machine"], row["execution_machine_kind"] = m["label"], m["kind"]
		}
		if root == nil {
			reason := "missing_parent"
			if lineage.unresolved[id] != "" {
				reason = lineage.unresolved[id]
			}
			row["channel_status"], row["machine_status"] = reason, reason
		} else {
			rid := firstString(root["id"])
			row["origin_conversation_id"] = rid
			for _, field := range []string{"channel", "machine_id"} {
				v, s, e := originClaim(observed[rid], field)
				if s == "direct" && rid != canonical {
					s = "inherited"
				}
				row[field] = nilIfEmpty(v)
				target := "channel_status"
				if field == "machine_id" {
					target = "machine_status"
				}
				row[target] = s
				if e != "" {
					evidence = e
				}
			}
			if m := machines[firstString(row["machine_id"])]; m != nil {
				row["machine"], row["machine_kind"] = m["label"], m["kind"]
			}
			if row["machine_id"] == nil && row["channel"] == "web" && row["machine_status"] == "unknown" {
				row["machine_id"], row["machine"], row["machine_kind"], row["machine_status"] = "environment:web", "Web (device unknown)", "web", "inferred"
			}
		}
		row["origin_evidence"] = nilIfEmpty(evidence)
		row["workspace_id"] = cv["workspace_id"]
		rows = append(rows, row)
	}
	return rows, nil
}

func originClaim(rows []map[string]any, field string) (string, string, string) {
	priority := -1
	value, evidence, status := "", "", "unknown"
	for _, row := range rows {
		v := firstString(row[field])
		if v == "" {
			continue
		}
		p := int(integer(row["priority"]))
		priorityField := map[string]string{"channel": "channel_priority", "machine_id": "machine_priority", "execution_machine_id": "execution_priority"}[field]
		if row[priorityField] != nil {
			p = int(integer(row[priorityField]))
		}
		if p > priority {
			priority, value, status, evidence = p, v, "direct", firstString(row["evidence"])
		} else if p == priority && v != value {
			status = "conflict"
		}
	}
	if status == "conflict" {
		return "", status, evidence
	}
	if value != "" && field != "channel" && priority < 30 {
		status = "inferred"
	}
	return value, status, evidence
}
func (c *Catalog) conversationOrigins(ctx context.Context) (map[string]map[string]any, error) {
	if c.queryOnly {
		return cachedValue(ctx, c, "conversation-origins", func(ctx context.Context) (map[string]map[string]any, error) {
			tx, err := c.reader().BeginTx(ctx, nil)
			if err != nil {
				return nil, err
			}
			defer tx.Rollback()
			rows, err := computeOrigins(ctx, tx)
			if err != nil {
				return nil, err
			}
			result := map[string]map[string]any{}
			for _, row := range rows {
				result[firstString(row["conversation_id"])] = row
			}
			return result, nil
		})
	}

	if err := c.ensureOrigins(ctx); err != nil {
		return nil, err
	}
	return cachedValue(ctx, c, "conversation-origins", func(ctx context.Context) (map[string]map[string]any, error) {
		rows, err := queryMapsContext(ctx, c.reader(), "SELECT * FROM conversation_origins")
		if err != nil {
			return nil, err
		}
		result := map[string]map[string]any{}
		for _, row := range rows {
			result[firstString(row["conversation_id"])] = row
		}
		return result, nil
	})
}
func copyOrigin(row, origin map[string]any) {
	for _, field := range originFields {
		row[field] = origin[field]
	}
	for field, set := range map[string]string{"channel": "channels", "machine_id": "machines", "execution_machine_id": "execution_machines"} {
		row[set] = defaultString(firstString(origin[field]), "Unknown")
	}
}
func (c *Catalog) attachOrigins(ctx context.Context, rows []map[string]any) error {
	origins, err := c.conversationOrigins(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if origin := origins[firstString(row["conversation_id"])]; origin != nil {
			copyOrigin(row, origin)
		}
	}
	return nil
}

// Aggregated units already resolve their dimensions across all contributors.
// Look up names by those IDs without replacing mixed or unknown placement
// with the origin of one representative conversation.
func (c *Catalog) attachOriginMachineLabels(ctx context.Context, rows []map[string]any) error {
	if len(rows) == 0 {
		return nil
	}
	machines, err := queryMapsContext(ctx, c.reader(), "SELECT id,label,kind FROM machines")
	if err != nil {
		return err
	}
	byID := make(map[string]map[string]any, len(machines))
	for _, machine := range machines {
		byID[firstString(machine["id"])] = machine
	}
	for _, row := range rows {
		for _, field := range []string{"machine", "execution_machine"} {
			machine := byID[firstString(row[field+"_id"])]
			row[field], row[field+"_kind"] = machine["label"], machine["kind"]
		}
	}
	return nil
}

// foldedMembers names additional workspaces whose facts contribute to a row.
// Library rows pass nil; writing rows include their folded child workspaces.
func (c *Catalog) attachWorkspaceOrigins(ctx context.Context, rows []map[string]any, foldedMembers map[string][]string) error {
	origins, err := c.conversationOrigins(ctx)
	if err != nil {
		return err
	}
	cvs, err := queryMapsContext(ctx, c.reader(), "SELECT id,workspace_id FROM conversations")
	if err != nil {
		return err
	}
	members := map[string][]map[string]any{}
	for _, cv := range cvs {
		w := firstString(cv["workspace_id"])
		members[w] = append(members[w], origins[firstString(cv["id"])])
	}
	for _, row := range rows {
		workspace := firstString(row["workspace_id"], row["id"])
		ids := append([]string{workspace}, foldedMembers[workspace]...)
		if aliases, ok := row["mirrored_workspace_ids"].([]string); ok {
			ids = append(ids, aliases...)
		}
		all := []map[string]any{}
		for _, id := range ids {
			all = append(all, members[id]...)
		}
		for _, f := range []struct{ scalar, set string }{{"channel", "channels"}, {"machine_id", "machines"}, {"execution_machine_id", "execution_machines"}} {
			values := map[string]bool{}
			for _, o := range all {
				values[firstString(o[f.scalar])] = true
			}
			keys := slices.Sorted(maps.Keys(values))
			parts := make([]string, len(keys))
			for i, v := range keys {
				parts[i] = defaultString(v, "Unknown")
			}
			row[f.set] = strings.Join(parts, ", ")
			if len(keys) == 1 {
				row[f.scalar] = nilIfEmpty(keys[0])
			} else {
				row[f.scalar] = nil
			}
		}
		for _, field := range []string{"machine", "machine_kind", "execution_machine", "execution_machine_kind"} {
			row[field] = nil
			if len(all) > 0 {
				prefix := "machine_id"
				if strings.HasPrefix(field, "execution") {
					prefix = "execution_machine_id"
				}
				if row[prefix] != nil {
					row[field] = all[0][field]
				}
			}
		}
	}
	return nil
}

// originReadSource shadows the persistent projection only for a read-only
// connection, so a held writer cannot block attribution-aware MCP queries.
func (c *Catalog) originReadSource(ctx context.Context, q queryer) (string, []any, error) {
	if !c.queryOnly {
		return "", nil, nil
	}
	rows, err := computeOrigins(ctx, q)
	if err != nil {
		return "", nil, err
	}
	columns := []string{"json_extract(value,'$.conversation_id') conversation_id"}
	for _, f := range originFields {
		columns = append(columns, "json_extract(value,'$."+f+"') "+f)
	}
	return "WITH conversation_origins AS (SELECT " + strings.Join(columns, ",") + " FROM json_each(?)) ", []any{jsonText(rows)}, nil
}
