package archive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Findings are evidence-backed optimization prompts: recurring patterns in
// the catalog that a change to a repository, its instructions, or a harness
// setting can fix (docs/findings.md, design in docs/auto-optimization-design.md).
//
// The daily findings pass derives findings, finding_aliases, and
// finding_daily. What the user did lives in finding_actions (dismiss,
// snooze, restore), finding_cart, and finding_interventions (each copied
// prompt and its measured result). Rebuilds never touch those three, and all
// of them sit in the catalog, so they travel with the library and its backups.
const findingsSchemaSQL = `
CREATE TABLE IF NOT EXISTS findings (
  id TEXT PRIMARY KEY,
  detector TEXT NOT NULL,
  scope TEXT NOT NULL,
  lever TEXT NOT NULL,
  repository_id TEXT,
  pattern TEXT NOT NULL,
  spec_json TEXT NOT NULL,
  card_json TEXT NOT NULL,
  metric_json TEXT NOT NULL,
  baseline_json TEXT NOT NULL,
  impact_json TEXT NOT NULL,
  gate_json TEXT NOT NULL,
  affected INTEGER NOT NULL DEFAULT 0,
  providers_json TEXT NOT NULL DEFAULT '[]',
  facts_json TEXT NOT NULL DEFAULT '{}',
  evidence_json TEXT NOT NULL DEFAULT '[]',
  target TEXT NOT NULL,
  active INTEGER NOT NULL DEFAULT 1,
  first_seen_at TEXT NOT NULL,
  gate_passed_at TEXT,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS findings_scope_idx ON findings(scope);
CREATE TABLE IF NOT EXISTS finding_aliases (
  alias TEXT PRIMARY KEY,
  finding_id TEXT NOT NULL
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS finding_daily (
  finding_id TEXT NOT NULL,
  day TEXT NOT NULL,
  events REAL NOT NULL DEFAULT 0,
  exposure REAL NOT NULL DEFAULT 0,
  cost_usd REAL NOT NULL DEFAULT 0,
  PRIMARY KEY(finding_id, day)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS finding_actions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  finding_id TEXT NOT NULL,
  action TEXT NOT NULL,
  reason TEXT,
  until_at TEXT,
  rate REAL,
  host_id TEXT,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS finding_actions_finding_idx ON finding_actions(finding_id, id);
CREATE TABLE IF NOT EXISTS finding_cart (
  finding_id TEXT PRIMARY KEY,
  target TEXT NOT NULL,
  ticked INTEGER NOT NULL DEFAULT 1,
  added_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS finding_interventions (
  id TEXT PRIMARY KEY,
  finding_id TEXT NOT NULL,
  attempt INTEGER NOT NULL,
  lever TEXT NOT NULL,
  change TEXT NOT NULL,
  target TEXT NOT NULL,
  copy_id TEXT NOT NULL,
  copied_at TEXT NOT NULL,
  host_id TEXT,
  handoff TEXT,
  plan_json TEXT NOT NULL,
  status TEXT NOT NULL,
  result_json TEXT,
  decided_at TEXT,
  savings_json TEXT,
  regressed_at TEXT,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS finding_interventions_finding_idx ON finding_interventions(finding_id, copied_at);
CREATE TABLE IF NOT EXISTS finding_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
-- Repository rows a merge retired, so findings scoped to them follow the
-- surviving repository (see mergeRepositoryGroupTx).
CREATE TABLE IF NOT EXISTS repository_retirements (
  old_id TEXT PRIMARY KEY,
  new_id TEXT NOT NULL,
  retired_at TEXT NOT NULL
);
`

func (c *Catalog) ensureFindingsSchema() error {
	_, err := c.DB.Exec(findingsSchemaSQL)
	return err
}

// findingCheckpoints are the thresholds the daily pass previews in settings;
// the threshold slider snaps to them (product decision 17).
var findingCheckpoints = []int{3, 5, 10, 15, 20}

// findingStates are the states the Findings view and MCP tools filter by.
const (
	findingOpen      = "open"
	findingWatching  = "watching"
	findingWon       = "won"
	findingDismissed = "dismissed"
	findingSnoozed   = "snoozed"
	// findingHidden is a candidate below the observability gate.
	findingHidden = "hidden"
)

// Intervention statuses. A copied prompt is watching until its analysis
// window closes; "withdrawn" is "I didn't apply this".
const (
	interventionWatching     = "watching"
	interventionImproved     = "improved"
	interventionUnchanged    = "unchanged"
	interventionWorse        = "worse"
	interventionInconclusive = "inconclusive"
	interventionWithdrawn    = "withdrawn"
)

// findingSettings are the options on the Findings settings page. They live
// in the catalog, so they follow the library between Macs.
type findingSettings struct {
	// Threshold is the affected conversations in 28 days a pattern needs;
	// 0 follows the recommendation.
	Threshold int `json:"threshold"`
	// Rank orders findings: usd, tokens, minutes, or failures.
	Rank string `json:"rank"`
	// Handoff is what prompts ask the agent to do: auto follows each
	// repository's habit; pr or diff always asks for that.
	Handoff string `json:"handoff"`
	// RepositoryHandoff overrides Handoff per repository ID.
	RepositoryHandoff map[string]string `json:"repository_handoff"`
	// SeenAt is when the Findings view was last opened, for the tab badge.
	SeenAt string `json:"seen_at"`
}

var findingRanks = map[string]bool{"usd": true, "tokens": true, "minutes": true, "failures": true}

func (c *Catalog) findingSettings(ctx context.Context) (findingSettings, error) {
	settings := findingSettings{Rank: "usd", Handoff: "auto", RepositoryHandoff: map[string]string{}}
	rows, err := c.DB.QueryContext(ctx, "SELECT key,value FROM finding_settings")
	if err != nil {
		return settings, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return settings, err
		}
		switch {
		case key == "threshold":
			settings.Threshold, _ = strconv.Atoi(value)
		case key == "rank" && findingRanks[value]:
			settings.Rank = value
		case key == "handoff" && (value == "auto" || value == "pr" || value == "diff"):
			settings.Handoff = value
		case key == "seen_at":
			settings.SeenAt = value
		case strings.HasPrefix(key, "handoff:") && (value == "pr" || value == "diff"):
			settings.RepositoryHandoff[strings.TrimPrefix(key, "handoff:")] = value
		}
	}
	return settings, rows.Err()
}

// SetFindingSettings stores the changed options. A threshold of 0 follows
// the recommendation; a repository handoff of "auto" removes its override.
func (c *Catalog) SetFindingSettings(ctx context.Context, body map[string]any) error {
	updates := map[string]string{}
	deletes := []string{}
	if value, ok := body["threshold"]; ok {
		threshold := int(integer(value))
		if threshold < 0 || threshold > 100 {
			return fmt.Errorf("threshold must be between 0 and 100")
		}
		updates["threshold"] = strconv.Itoa(threshold)
	}
	if value, ok := body["rank"]; ok {
		if !findingRanks[firstString(value)] {
			return fmt.Errorf("rank must be usd, tokens, minutes, or failures")
		}
		updates["rank"] = firstString(value)
	}
	if value, ok := body["handoff"]; ok {
		switch firstString(value) {
		case "auto", "pr", "diff":
			updates["handoff"] = firstString(value)
		default:
			return fmt.Errorf("handoff must be auto, pr, or diff")
		}
	}
	for repository, value := range mapValueDefault(body["repository_handoff"]) {
		switch firstString(value) {
		case "pr", "diff":
			updates["handoff:"+repository] = firstString(value)
		case "auto", "":
			deletes = append(deletes, "handoff:"+repository)
		default:
			return fmt.Errorf("a repository's handoff must be auto, pr, or diff")
		}
	}
	if len(updates) == 0 && len(deletes) == 0 {
		return nil
	}
	return c.writeTransaction(ctx, "finding-settings", func(tx *sql.Tx) error {
		for key, value := range updates {
			if _, err := tx.Exec("INSERT INTO finding_settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value); err != nil {
				return err
			}
		}
		for _, key := range deletes {
			if _, err := tx.Exec("DELETE FROM finding_settings WHERE key=?", key); err != nil {
				return err
			}
		}
		return nil
	})
}

// MarkFindingsSeen records a visit to the Findings view: the tab badge
// counts findings that became visible after it.
func (c *Catalog) MarkFindingsSeen(ctx context.Context) error {
	return c.writeTransaction(ctx, "findings-seen", func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO finding_settings(key,value) VALUES('seen_at',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", formatTime(c.clock()))
		return err
	})
}

// findingUserState is everything the user did to one finding, gathered
// through its aliases.
type findingUserState struct {
	Dismissed     bool
	DismissReason string
	DismissedAt   string
	SnoozeUntil   string
	SnoozeWorse   bool
	SnoozeRate    float64
	SnoozedAt     string
	Cart          *findingCartEntry
	Interventions []*findingIntervention
}

type findingCartEntry struct {
	FindingID string `json:"finding_id"`
	Target    string `json:"target"`
	Ticked    bool   `json:"ticked"`
	AddedAt   string `json:"added_at"`
}

type findingIntervention struct {
	ID          string         `json:"id"`
	FindingID   string         `json:"finding_id"`
	Attempt     int            `json:"attempt"`
	Lever       string         `json:"lever"`
	Change      string         `json:"change"`
	Target      string         `json:"target"`
	CopyID      string         `json:"copy_id"`
	CopiedAt    string         `json:"copied_at"`
	HostID      string         `json:"host_id,omitempty"`
	Handoff     string         `json:"handoff,omitempty"`
	Plan        findingPlan    `json:"plan"`
	Status      string         `json:"status"`
	Result      map[string]any `json:"result,omitempty"`
	DecidedAt   string         `json:"decided_at,omitempty"`
	Savings     findingSavings `json:"savings"`
	RegressedAt string         `json:"regressed_at,omitempty"`
}

// latest is the newest intervention that was not withdrawn.
func (state *findingUserState) latest() *findingIntervention {
	for index := len(state.Interventions) - 1; index >= 0; index-- {
		if state.Interventions[index].Status != interventionWithdrawn {
			return state.Interventions[index]
		}
	}
	return nil
}

// attempts counts the interventions that reached a result or are measuring.
func (state *findingUserState) attempts() int {
	count := 0
	for _, item := range state.Interventions {
		if item.Status != interventionWithdrawn {
			count++
		}
	}
	return count
}

// findingAliasMap maps every alias, and every current ID, to the current ID.
func findingAliasMap(ctx context.Context, q queryer) (map[string]string, error) {
	rows, err := queryMapsContext(ctx, q, "SELECT alias,finding_id FROM finding_aliases")
	if err != nil {
		return nil, err
	}
	aliases := map[string]string{}
	for _, row := range rows {
		aliases[firstString(row["alias"])] = firstString(row["finding_id"])
	}
	return aliases, nil
}

func resolveFindingID(aliases map[string]string, id string) string {
	if current, ok := aliases[id]; ok {
		return current
	}
	return id
}

// findingUserStates reads the user tables once, keyed by current finding ID.
func (c *Catalog) findingUserStates(ctx context.Context, q queryer) (map[string]*findingUserState, error) {
	aliases, err := findingAliasMap(ctx, q)
	if err != nil {
		return nil, err
	}
	states := map[string]*findingUserState{}
	state := func(id string) *findingUserState {
		id = resolveFindingID(aliases, id)
		if states[id] == nil {
			states[id] = &findingUserState{}
		}
		return states[id]
	}
	actions, err := queryMapsContext(ctx, q, "SELECT finding_id,action,reason,until_at,rate,created_at FROM finding_actions ORDER BY id")
	if err != nil {
		return nil, err
	}
	for _, row := range actions {
		item := state(firstString(row["finding_id"]))
		switch firstString(row["action"]) {
		case "dismiss":
			item.Dismissed, item.DismissReason, item.DismissedAt = true, firstString(row["reason"]), firstString(row["created_at"])
			item.SnoozeUntil, item.SnoozeWorse = "", false
		case "snooze":
			item.Dismissed = false
			item.SnoozeUntil, item.SnoozedAt = firstString(row["until_at"]), firstString(row["created_at"])
			item.SnoozeWorse = firstString(row["reason"]) == "until_worse"
			item.SnoozeRate, _ = number(row["rate"])
		case "restore":
			item.Dismissed, item.DismissReason, item.SnoozeUntil, item.SnoozeWorse = false, "", "", false
		}
	}
	cart, err := queryMapsContext(ctx, q, "SELECT finding_id,target,ticked,added_at FROM finding_cart ORDER BY added_at")
	if err != nil {
		return nil, err
	}
	for _, row := range cart {
		entry := &findingCartEntry{FindingID: resolveFindingID(aliases, firstString(row["finding_id"])), Target: firstString(row["target"]), Ticked: integer(row["ticked"]) != 0, AddedAt: firstString(row["added_at"])}
		state(entry.FindingID).Cart = entry
	}
	interventions, err := loadInterventions(ctx, q, "")
	if err != nil {
		return nil, err
	}
	for _, item := range interventions {
		current := resolveFindingID(aliases, item.FindingID)
		state(current).Interventions = append(state(current).Interventions, item)
	}
	return states, nil
}

func loadInterventions(ctx context.Context, q queryer, where string, args ...any) ([]*findingIntervention, error) {
	rows, err := queryMapsContext(ctx, q, `SELECT id,finding_id,attempt,lever,change,target,copy_id,copied_at,host_id,handoff,plan_json,status,result_json,decided_at,savings_json,regressed_at
		FROM finding_interventions `+where+` ORDER BY copied_at,id`, args...)
	if err != nil {
		return nil, err
	}
	items := make([]*findingIntervention, 0, len(rows))
	for _, row := range rows {
		item := &findingIntervention{ID: firstString(row["id"]), FindingID: firstString(row["finding_id"]), Attempt: int(integer(row["attempt"])),
			Lever: firstString(row["lever"]), Change: firstString(row["change"]), Target: firstString(row["target"]), CopyID: firstString(row["copy_id"]),
			CopiedAt: firstString(row["copied_at"]), HostID: firstString(row["host_id"]), Handoff: firstString(row["handoff"]), Status: firstString(row["status"]),
			DecidedAt: firstString(row["decided_at"]), RegressedAt: firstString(row["regressed_at"])}
		_ = json.Unmarshal([]byte(firstString(row["plan_json"])), &item.Plan)
		if text := firstString(row["result_json"]); text != "" {
			_ = json.Unmarshal([]byte(text), &item.Result)
		}
		if text := firstString(row["savings_json"]); text != "" {
			_ = json.Unmarshal([]byte(text), &item.Savings)
		}
		items = append(items, item)
	}
	return items, nil
}

// FindingAction applies a user action to a finding: dismiss (reason not
// real, not worth it, or won't fix), snooze (7d, 30d, or until_worse),
// restore, cart add/remove/move/tick, and not_applied ("I didn't apply
// this"), which withdraws the latest measurement.
func (c *Catalog) FindingAction(ctx context.Context, id string, body map[string]any) error {
	action := firstString(body["action"])
	aliases, err := findingAliasMap(ctx, c.DB)
	if err != nil {
		return err
	}
	id = resolveFindingID(aliases, id)
	var exists int
	if err := c.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM findings WHERE id=?", id).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("no finding with id %q", id)
	}
	host := c.currentHostID()
	stamp := formatTime(c.clock())
	switch action {
	case "dismiss":
		reason := firstString(body["reason"])
		if reason != "" && reason != "not_real" && reason != "not_worth_it" && reason != "wont_fix" {
			return fmt.Errorf("reason must be not_real, not_worth_it, or wont_fix")
		}
		return c.writeTransaction(ctx, "finding-dismiss", func(tx *sql.Tx) error {
			if _, err := tx.Exec("DELETE FROM finding_cart WHERE finding_id=?", id); err != nil {
				return err
			}
			_, err := tx.Exec("INSERT INTO finding_actions(finding_id,action,reason,host_id,created_at) VALUES(?,?,?,?,?)", id, "dismiss", nilIfEmpty(reason), nilIfEmpty(host), stamp)
			return err
		})
	case "snooze":
		mode := firstString(body["until"])
		var until any
		var rate any
		switch mode {
		case "7d":
			until = formatTime(c.clock().AddDate(0, 0, 7))
		case "30d":
			until = formatTime(c.clock().AddDate(0, 0, 30))
		case "until_worse":
			value, err := c.findingRecentRate(ctx, id, 7)
			if err != nil {
				return err
			}
			rate = value
		default:
			return fmt.Errorf("until must be 7d, 30d, or until_worse")
		}
		reason := any(nil)
		if mode == "until_worse" {
			reason = "until_worse"
		}
		return c.writeTransaction(ctx, "finding-snooze", func(tx *sql.Tx) error {
			if _, err := tx.Exec("DELETE FROM finding_cart WHERE finding_id=?", id); err != nil {
				return err
			}
			_, err := tx.Exec("INSERT INTO finding_actions(finding_id,action,reason,until_at,rate,host_id,created_at) VALUES(?,?,?,?,?,?,?)", id, "snooze", reason, until, rate, nilIfEmpty(host), stamp)
			return err
		})
	case "restore":
		return c.writeTransaction(ctx, "finding-restore", func(tx *sql.Tx) error {
			_, err := tx.Exec("INSERT INTO finding_actions(finding_id,action,host_id,created_at) VALUES(?,?,?,?)", id, "restore", nilIfEmpty(host), stamp)
			return err
		})
	case "cart_add", "cart_move":
		target := firstString(body["target"])
		if target == "" {
			if err := c.DB.QueryRowContext(ctx, "SELECT target FROM findings WHERE id=?", id).Scan(&target); err != nil {
				return err
			}
		}
		if !validFindingTarget(target) {
			return fmt.Errorf("unknown prompt target %q", target)
		}
		return c.writeTransaction(ctx, "finding-cart", func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO finding_cart(finding_id,target,ticked,added_at) VALUES(?,?,1,?)
				ON CONFLICT(finding_id) DO UPDATE SET target=excluded.target`, id, target, stamp)
			return err
		})
	case "cart_remove":
		return c.writeTransaction(ctx, "finding-cart", func(tx *sql.Tx) error {
			_, err := tx.Exec("DELETE FROM finding_cart WHERE finding_id=?", id)
			return err
		})
	case "cart_tick":
		ticked := 0
		if value, ok := body["ticked"].(bool); ok && value {
			ticked = 1
		}
		return c.writeTransaction(ctx, "finding-cart", func(tx *sql.Tx) error {
			_, err := tx.Exec("UPDATE finding_cart SET ticked=? WHERE finding_id=?", ticked, id)
			return err
		})
	case "not_applied":
		return c.writeTransaction(ctx, "finding-not-applied", func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE finding_interventions SET status=?,updated_at=? WHERE id=(SELECT id FROM finding_interventions
				WHERE finding_id IN (SELECT alias FROM finding_aliases WHERE finding_id=? UNION SELECT ?) AND status=? ORDER BY copied_at DESC LIMIT 1)`,
				interventionWithdrawn, stamp, id, id, interventionWatching)
			return err
		})
	}
	return fmt.Errorf("unknown finding action %q", action)
}

// validFindingTarget accepts the prompt targets a cart can hold: a
// repository, one provider's global instructions on one Mac, or an
// automation.
func validFindingTarget(target string) bool {
	kind, rest, ok := strings.Cut(target, ":")
	if !ok || rest == "" {
		return false
	}
	switch kind {
	case "repo", "automation":
		return true
	case "global":
		host, provider, ok := strings.Cut(rest, ":")
		return ok && host != "" && provider != ""
	}
	return false
}

// findingRecentRate is a finding's rate over its last days of daily counts,
// for a snooze that waits until the pattern gets worse.
func (c *Catalog) findingRecentRate(ctx context.Context, id string, days int) (float64, error) {
	since := c.clock().AddDate(0, 0, -days).Format("2006-01-02")
	var events, exposure float64
	err := c.DB.QueryRowContext(ctx, "SELECT COALESCE(SUM(events),0),COALESCE(SUM(exposure),0) FROM finding_daily WHERE finding_id=? AND day>?", id, since).Scan(&events, &exposure)
	if err != nil || exposure == 0 {
		return 0, err
	}
	return events / exposure, nil
}

// currentHostID names this Mac in user actions, so a global finding's result
// is read on the Mac whose instruction files changed.
func (c *Catalog) currentHostID() string {
	if host := currentHost(); !host.Fallback {
		return host.ID
	}
	return ""
}

// findingTargetLabel names a prompt target the way the user sees it.
func findingTargetLabel(target string, repositories map[string]string, hosts map[string]string) string {
	kind, rest, _ := strings.Cut(target, ":")
	switch kind {
	case "repo":
		if name := repositories[rest]; name != "" {
			return name
		}
		return "a repository"
	case "global":
		host, provider, _ := strings.Cut(rest, ":")
		label := "All repositories · " + providerLabel(provider)
		if name := hosts[host]; name != "" && len(hosts) > 1 {
			label += " on " + name
		}
		return label
	case "automation":
		parts := strings.Split(rest, ":")
		if len(parts) >= 2 && parts[0] == "tl1" {
			return "TL1 · " + parts[len(parts)-1]
		}
		return "Automation · " + parts[len(parts)-1]
	}
	return target
}

func providerLabel(provider string) string {
	switch provider {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "antigravity":
		return "Antigravity"
	case "gemini":
		return "Gemini"
	}
	if provider == "" {
		return "agents"
	}
	return strings.ToUpper(provider[:1]) + provider[1:]
}

// sortedKeys returns a map's keys in order.
func sortedFindingKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// localDay is the local calendar day of a stored timestamp.
func localDay(value string) string {
	parsed, ok := parseTime(value)
	if !ok {
		return ""
	}
	return parsed.Local().Format("2006-01-02")
}

// jsonUnmarshalString decodes stored JSON; empty text leaves target alone.
func jsonUnmarshalString(text string, target any) error {
	if text == "" {
		return nil
	}
	return json.Unmarshal([]byte(text), target)
}

func dayTime(day string) time.Time {
	parsed, _ := time.ParseInLocation("2006-01-02", day, time.Local)
	return parsed
}
