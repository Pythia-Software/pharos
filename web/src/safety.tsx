import { DataReadinessIndicator } from "./data-readiness";
import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { CartPanel, ReviewDialog, type CartGroup, type Target } from "./findings";
import { useViewState } from "./view-state";
import { ViewDisclosure } from "./view-disclosure";
import { OptimizePlaceFilter, OptimizeRank } from "./optimize-controls";
import { safetyShortName, safetyDashboardPlaces, safetyDashboardChecks, safetyDangerLabels, safetyRankLabels, safetyRankExplanations, safetyPromptLimit, safetyCartChecks, type SafetyCheckSummary, type SafetyDanger, type SafetyRank } from "./safety-dashboard-model";
import { OptimizeCard, OptimizeCart, OptimizeSelectionBar } from "./optimize-shared";
import { type SafetyView, safetyURL, safetyCheckTableURL, safetyStateLabels, safetyTableFocus } from "./safety-route";
import { predicatesOf, type QueryState, type WhereClause } from "@pythia-software/query-table-core";
import { optimizeTableRemainder, type OptimizeTableSchemas } from "./optimize-table-title";
import { useSelection, type SelectionApi } from "@pythia-software/query-table-react";
import { onSafetyChange } from "./dataset-refresh";

const api = "/api/findings/safety";
type State = "open" | "handed_off" | "dismissed";
type Card = { id: string; rule: string; danger: SafetyDanger; subject: string; title: string; explanation: string; state: State; target: string; target_label: string; first_seen_at: string; last_seen_at: string; handed_at?: string; material_change: boolean; calls: number; cart?: { target: string; target_label: string; ticked: boolean } };
// quote evidence is an agent's own sentence (a safeguard check) rather than a command.
type Evidence = { id: string; call_id: string; message_id: string; at: string; command: string; cwd: string; cwd_basis: string; status: string; outcome: string; conditional: boolean; path: string; quote?: boolean };
type Detail = Card & { questions: string[]; evidence: Evidence[] };
type Overview = { cases: Card[]; summary: Record<State, number>; cart: CartGroup[]; targets: Target[]; built_at: string | null; scanned_calls: string; coverage: string };

async function request<T>(url: string, body?: unknown): Promise<T> {
  const response = await fetch(url, body === undefined ? undefined : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const value = await response.json();
  if (!response.ok) throw new Error(value.error ?? `Request failed (${response.status})`);
  return value as T;
}
const date = (value?: string) => value ? new Date(value).toLocaleDateString(undefined, { month: "short", day: "numeric" }) : "Date unavailable";
const stateLabel = safetyStateLabels;

export function SafetyPage({ copy, view = "", table, refreshDiscovery }: { copy: (text: string) => Promise<void>; view?: SafetyView; table?: React.ReactNode; refreshDiscovery: () => Promise<unknown> }) {
  const [list, setList] = useViewState<State>("safety_list", "open", view === "");
  const [place, setPlace] = useViewState<string>("safety_place", "", view === "");
  const [rank, setRank] = useViewState<SafetyRank>("safety_rank", "danger", view === "");
  const [selected, select] = useViewState<string>("safety_case", "", view !== "settings");
  const [overview, setOverview] = useState<Overview | null>(null);
  const [detail, setDetail] = useState<Detail | null>(null);
  const [error, setError] = useState("");
  const [review, setReview] = useState("");
  const [notice, setNotice] = useState("");
  const loadSequence = useRef(0);
  const reload = useCallback(async () => {
    const sequence = ++loadSequence.current;
    try { const value = await request<Overview>(api); if (sequence === loadSequence.current) { setOverview(value); setError(""); } }
    catch (failure) { if (sequence === loadSequence.current) setError((failure as Error).message); }
  }, []);
  useEffect(() => { void reload(); }, [reload, view]);
  useEffect(() => {
    const revisit = () => { if (location.pathname === "/findings" && new URLSearchParams(location.search).get("optimize_area") === "safety") void reload(); };
    const stop = onSafetyChange(() => void reload());
    window.addEventListener("pharos:route", revisit);
    return () => { stop(); window.removeEventListener("pharos:route", revisit); loadSequence.current++; };
  }, [reload]);
  useEffect(() => {
    let cancelled = false;
    setDetail(null);
    if (selected) void request<Detail>(`${api}/${encodeURIComponent(selected)}`).then(value => { if (!cancelled) { setDetail(value); setError(""); } }).catch(failure => { if (!cancelled) setError(failure.message); });
    return () => { cancelled = true; };
  }, [selected, overview]);
  const act = useCallback(async (id: string, body: Record<string, unknown>, done?: string) => {
    try {
      await request(`${api}/${encodeURIComponent(id)}/action`, body);
      window.dispatchEvent(new Event("pharos:safety-changed"));
      setNotice(done ?? "Saved.");
      return true;
    } catch (failure) { setError((failure as Error).message); return false; }
  }, []);
  const group = overview?.cart.find(item => item.target === review);
  const places = safetyDashboardPlaces(overview?.cases ?? []);
  const checks = safetyDashboardChecks(overview?.cases ?? [], list, place, rank);
  // Ticking a check card selects every notice it summarizes, so a whole check
  // goes into one prompt (one per location) or is closed at once.
  const listed = useMemo(() => checks.flatMap(check => check.ids), [overview, list, place]);
  const selection = useSelection(listed);
  useEffect(() => { selection.clear(); }, [list, place]);
  const checkNotices = (rule: string) => overview?.cases.filter(card => card.rule === rule).length ?? 0;
  const actions = (card: Card) => <div className="findings-actions">
    {card.state === "open" ? <>
      {card.cart ? <button className="findings-button" onClick={() => void act(card.id, { action: "cart_remove" }, "Removed from risky action prompt.")}>In prompt · Remove</button>
        : <button className="findings-button primary" onClick={() => void act(card.id, { action: "cart_add" }, `Added to risky action prompt for ${card.target_label}.`)}>Add to prompt</button>}
      <button className="findings-button quiet" onClick={() => void act(card.id, { action: "dismiss" }, "Acknowledged and closed. You can bring it back at any time.")}>Acknowledge and close</button>
    </> : <button className="findings-button" onClick={() => void act(card.id, { action: "restore" }, "Brought back to Open.")}>Bring back</button>}
  </div>;
  return <div className="findings-page safety-page">
    {view !== "table" || selected ? <div className="view-heading findings-heading"><div><h1>{view === "settings" ? "Risky action settings" : "Risky actions"}</h1><p className="muted">{view === "settings" ? "Choose which checks raise risky action notices. Your choices follow the library between Macs." : selected ? "Questions about consequential agent actions. Collect a prompt and let your agent investigate." : "A risky action is an agent operation that could expose credentials, rewrite shared history, delete data outside its work, or alter a system. Pharos raises a notice only where a command is likely to deserve your attention, and skips an agent's own scratch, build output and work in progress; a notice alone does not establish harm. Each card is one check; the most dangerous come first. Tick a card to copy a prompt asking your agent to investigate its notices and recommend fixes, or to dismiss expected activity, or open its table to look at each notice. Copying the prompt records a handoff."}</p></div>{view !== "settings" && view !== "table" ? <DataReadinessIndicator id="safety:overview" title="Risky actions" dataset="safety" query={{view,selected}} /> : null}</div> : null}
    {error ? <p className="findings-error" role="alert">{error}</p> : null}
    {notice ? <p className="safety-notice" role="status">{notice}</p> : null}
    {view === "settings" ? <SafetySettings refreshDiscovery={refreshDiscovery} /> : !overview ? <p>Loading risky action notices…</p> : <>
      {selected ? <>
        <button className="findings-button" data-view-field="safety_case" onClick={() => select("")}>{view === "table" ? "← Risky actions table" : "← Risky action notices"}</button>
        {detail ? <article className="findings-panel safety-detail" data-pharos-target={`safety:${detail.id}`}>
          <p className="findings-muted"><DangerChip danger={detail.danger} /> {detail.target_label} · {stateLabel[detail.state]}</p>
          <h2>{detail.title}</h2><p>{detail.explanation}</p>
          <p className="safety-subject">{detail.subject}</p>
          {checkNotices(detail.rule) > 1 ? <p><a href={safetyCheckTableURL(detail.rule)}>List all {checkNotices(detail.rule).toLocaleString()} resources this check selected</a></p> : null}
          {detail.handed_at ? <p className="findings-muted">Handed off {date(detail.handed_at)}. {detail.material_change ? "A later command has stronger execution evidence, so this notice is open again." : "No returned report is required. This records the handoff; the concern may still need investigation."}</p> : null}
          {actions(detail)}
          <h3>Questions for your agent</h3><ul>{detail.questions.map(question => <li key={question}>{question}</li>)}</ul>
          <h3>Archived evidence</h3>
          <p className="findings-muted">{detail.calls} distinct calls in the latest discovery window; up to 20 examples below. A notice identifies commands to look into, not confirmed harm or authorization.</p>
          {detail.evidence.map(entry => <ViewDisclosure key={entry.id} viewKey={`safety:${detail.id}:evidence:${entry.id}`} className="safety-evidence" summary={<>{date(entry.at)} · {entry.outcome}</>}>
            {entry.quote ? <blockquote className="safety-quote">{entry.command}</blockquote> : <><pre>{entry.command}</pre><p>Working directory: <code>{entry.cwd || "Unresolved"}</code></p><p className="findings-muted">{entry.cwd_basis}{entry.conditional ? ". Compound or conditional call: individual execution needs context." : ""}</p></>}
            <a href={entry.path}>Open archived conversation ↗</a>
          </ViewDisclosure>)}
        </article> : !error ? <p>Loading case…</p> : null}
      </> : view === "table" ? null : <>
        <div className="findings-toolbar">
          <div className="findings-tabs" role="tablist" aria-label="Risky action notices by state">
            {(Object.keys(stateLabel) as State[]).map(state => <button role="tab" key={state} aria-selected={list === state} data-view-field="safety_list" onClick={() => setList(state)}>{stateLabel[state]} <span className="findings-count">{overview.summary[state]}</span></button>)}
          </div>
          <OptimizePlaceFilter places={places} value={place} onChange={setPlace} label="Show risky actions for" viewField="safety_place" />
          <OptimizeRank value={rank} onChange={setRank} options={safetyRankLabels} explanations={safetyRankExplanations} viewField="safety_rank" />
        </div>
        {list === "handed_off" ? <p className="findings-muted">You have handed these notices to an agent. Repeats accumulate here; a new target, broader operation, or stronger execution evidence can need fresh attention.</p> : null}
        <div className="findings-layout with-cart">
          {checks.length ? <section className="safety-check-cards" aria-label="Risky actions by check">
            <SafetySelectionBar selection={selection} copy={copy} />
            <div className="safety-check-grid">{checks.map(check => <SafetyCheckCard key={check.rule} check={check} place={place} selection={selection} href={safetyCheckTableURL(check.rule, { state: list, target: place })} />)}</div>
          </section> : <div className="findings-empty"><strong>{place ? "No notices for this location." : list === "open" ? "No open risky action notices." : list === "handed_off" ? "Nothing handed off yet." : "Nothing closed."}</strong><p>{place ? "Choose Everywhere to see every notice in this list." : list === "open" ? "Supported commands have not raised an open notice. Coverage and uncertainties are below." : "Your notices will appear here."}</p></div>}
          <SafetyCheckCart cart={overview.cart} onReview={next => setReview(next.target)} />
        </div>
      </>}
      {view !== "table" ? <ViewDisclosure viewKey="safety:coverage" className="safety-coverage" summary={<>What Pharos checked{overview.built_at ? ` · updated ${date(overview.built_at)}` : ""}</>}><p>{overview.built_at ? `${Number(overview.scanned_calls).toLocaleString()} retained shell calls examined. ` : "Risky action discovery runs with the next full Findings refresh. "}{overview.coverage}</p><p>Checks cover credential copies, rewritten published history, cloud deletion, disk and system changes, deletions outside the checkout, database changes, broadened permissions, deleted releases, discarded shared Git work, program-wide process signals, and remote API changes. They skip temporary, build and scratch locations. Safeguard checks read agent events instead of commands: an agent saying it weakened or skipped a test, worked around a restriction, or bypassed a hook or gate raises one notice per instance; recurring problems agents could be spared go to Fixes instead. These are deterministic heuristics; no agent investigation runs inside Pharos.</p><a href={safetyURL("settings")}>Risky action settings and refresh</a></ViewDisclosure> : null}
    </>}
    {table ? <div className="safety-table" hidden={view !== "table" || Boolean(selected)}>
      <div className="safety-table-main">{table}</div>
      {view === "table" && overview ? <CartPanel safety cart={overview.cart} targets={overview.targets} act={act} onReview={next => setReview(next.target)} /> : null}
    </div> : null}
    {group ? <ReviewDialog key={group.target} safety group={group} copy={copy} onClose={() => setReview("")} onCopied={() => { setReview(""); window.dispatchEvent(new Event("pharos:safety-changed")); setNotice("Copied. Notices moved to Handed off."); }} /> : null}
  </div>;
}

type Settings = { rules: Array<{ id: string; title: string; explanation: string; questions?: string[]; danger?: SafetyDanger; enabled: boolean }>; built_at: string | null; scanned_calls: string; window_days: number; version: string };
function SafetySettings({ refreshDiscovery }: { refreshDiscovery: () => Promise<unknown> }) {
  const [settings, setSettings] = useState<Settings | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  useEffect(() => {
    let current = true;
    const load = () => void request<Settings>(`${api}/settings`).then(value => { if (current) setSettings(value); }).catch(failure => { if (current) setError(failure.message); });
    load(); const stop = onSafetyChange(load);
    return () => { current = false; stop(); };
  }, []);
  async function save(id: string, enabled: boolean) {
    setBusy(true); setError(""); setMessage("");
    try { setSettings(await request<Settings>(`${api}/settings`, { rule_enabled: { [id]: enabled } })); setMessage("Discovery setting saved. Existing notices and prompts remain available."); }
    catch (failure) { setError((failure as Error).message); }
    finally { setBusy(false); }
  }
  async function refresh() {
    setBusy(true); setError(""); setMessage("");
    try { await request("/api/findings/refresh", {}); await refreshDiscovery(); setMessage("Refresh requested for efficiency and risky actions. Discovery continues in the background."); }
    catch (failure) { setError((failure as Error).message); }
    finally { setBusy(false); }
  }
  return <div className="safety-settings">
    {error ? <p className="findings-error" role="alert">{error}</p> : null}
    {message ? <p role="status">{message}</p> : null}
    {!settings ? <p>Loading risky action settings…</p> : <>
      <section className="findings-panel"><h2>Discovery checks</h2><p className="findings-muted">{settings.rules.length} checks · {settings.rules.filter(rule => rule.enabled).length} enabled</p><p>Choose which operations raise new notices or update existing evidence. Checks are listed most dangerous first. Turning off a check keeps existing notices, prompt baskets, and handoff history.</p>
        <div className="safety-checks">{settings.rules.map(rule => <label key={rule.id} className="safety-check"><input type="checkbox" checked={rule.enabled} disabled={busy} onChange={event => void save(rule.id, event.target.checked)} /><span><strong>{rule.title}</strong>{rule.danger ? <> <DangerChip danger={rule.danger} /></> : null}<small>{rule.explanation}</small></span></label>)}</div>
      </section>
      <section className="findings-panel"><h2>Refresh and coverage</h2><p>A full refresh examines supported shell calls from the last {settings.window_days} days. A single event can qualify; efficiency thresholds do not apply.</p><p className="findings-muted">{settings.built_at ? `Last discovery: ${date(settings.built_at)} · ${Number(settings.scanned_calls).toLocaleString()} retained shell calls examined.` : "No safety discovery has run yet."}</p><button className="findings-button" disabled={busy} onClick={() => void refresh()}>Refresh efficiency and risky actions now</button><p>Discovery uses the shared Findings background schedule. Embedded scripts and arbitrary native or MCP mutations are not comprehensively interpreted.</p></section>
      <section className="findings-panel"><h2>Prompt handoff</h2><p>Risky action prompts ask an agent to investigate, explain, and recommend. Copying moves selected notices to Handed off; no returned report is required. You can bring a notice back at any time.</p><p>Risky actions and efficiency have separate baskets. Repeats accumulate quietly; a new target, broader operation, or stronger execution evidence can need fresh attention.</p></section>
    </>}
  </div>;
}

// The table's view of notices: their states, locations and baskets.
function useSafetyOverview() {
  const [overview, setOverview] = useState<Overview | null>(null);
  useEffect(() => {
    let current = true;
    const load = () => void request<Overview>(api).then(value => { if (current) setOverview(value); }).catch(() => {});
    load(); const stop = onSafetyChange(load);
    return () => { current = false; stop(); };
  }, []);
  return overview;
}

type Rule = Settings["rules"][number];
// When the table holds one check's notices, say which, where, and what to look
// for, so the rows read as that check's list of selected resources.
export function SafetyTableContext({ query, schemas, total, fallback }: { query: QueryState; schemas: OptimizeTableSchemas; total: number | null | undefined; fallback: React.ReactNode }) {
  const focus = safetyTableFocus(query);
  const overview = useSafetyOverview();
  const [rules, setRules] = useState<Rule[]>([]);
  useEffect(() => { let current = true; void request<Settings>(`${api}/settings`).then(value => { if (current) setRules(value.rules); }).catch(() => {}); return () => { current = false; }; }, []);
  const rule = focus && rules.find(item => item.id === focus.rule);
  if (!focus || !rule) return <>{fallback}</>;
  const source = query.grouping?.source ?? query;
  const hasFilter = (...names: string[]) => source.where.some(term => predicatesOf(term).some(clause => names.includes(clause.field)));
  const named: Pick<WhereClause, "field" | "op" | "value">[] = [{ field: "rule", op: "=", value: focus.rule }];
  if (focus.state) named.push({ field: "state", op: "=", value: focus.state });
  if (focus.target) named.push({ field: "target", op: "=", value: focus.target });
  else if (focus.place) named.push({ field: "target_label", op: "=", value: focus.place });
  // A location-filtered table omits its location column, so name it from the notices.
  const place = focus.target ? overview?.cases.find(card => card.target === focus.target)?.target_label ?? "one location" : focus.place ?? (!hasFilter("target", "target_label") ? "every location" : "");
  const state = focus.state ? stateLabel[focus.state as State] ?? focus.state : !hasFilter("state") ? "every state" : "";
  const count = typeof total === "number" ? `${total.toLocaleString()} ${query.grouping ? total === 1 ? "group" : "groups" : total === 1 ? "notice" : "notices"}` : "";
  const remainder = optimizeTableRemainder(schemas.source, query, named, schemas.result);
  return <div className="optimize-table-context" data-pharos-target={`safety-check:${focus.rule}`}>
    <h1 className="optimize-table-title">{rule.title}</h1>
    <p className="optimize-table-meta">{["Risky actions", state, place, count, remainder].filter(Boolean).join(" · ")}</p>
    <p className="optimize-table-explanation">{rule.explanation}</p>
    {rule.questions?.length ? <ViewDisclosure viewKey={`safety-check:${focus.rule}:questions`} className="optimize-table-questions" summary="Questions for each notice">
      <ul>{rule.questions.map(question => <li key={question}>{question}</li>)}</ul>
    </ViewDisclosure> : null}
  </div>;
}

// Actions on the table's selected notices (optimize-shared.tsx).
export function SafetySelectionBar({ selection, copy }: { selection: SelectionApi; copy: (text: string) => Promise<void> }) {
  const overview = useSafetyOverview();
  return <OptimizeSelectionBar selection={selection} copy={copy} items={overview?.cases ?? null} api={api} area="safety" changed="pharos:safety-changed"
    noun={{ one: "notice", many: "notices" }} place={{ one: "location", many: "locations" }} promptLimit={safetyPromptLimit} />;
}

const range = (first: string, last: string) => { const a = date(first), b = date(last); return a === b ? b : `${a} – ${b}`; };
const plural = (n: number, one: string, many = `${one}s`) => `${n.toLocaleString()} ${n === 1 ? one : many}`;

// One check's notices at a glance: what they share (one location, one
// directory, a recurring resource) before what varies, then when, how much,
// and what is new. The checkbox selects every notice on the card.
function SafetyCheckCard({ check, place, selection, href }: { check: SafetyCheckSummary; place: string; selection: SelectionApi; href: string }) {
  const where = place ? null : check.locations === 1 ? `${check.count === 1 ? "In" : "All in"} ${check.places[0]?.label}`
    : `${check.places.map(item => `${item.label} ${item.count}`).join(" · ")}${check.morePlaces ? ` · ${plural(check.morePlaces, "more location")}` : ""}`;
  const single = check.count === 1;
  const facts = [range(check.first_seen_at, check.last_seen_at), plural(check.calls, "call"), check.fresh ? `${check.fresh.toLocaleString()} with new evidence` : "", check.inPrompt ? `${check.inPrompt.toLocaleString()} in a prompt` : ""].filter(Boolean);
  const chips = <>{check.danger ? <DangerChip danger={check.danger} /> : null}{check.recent ? <span className="findings-chip safety-recent">Recent</span> : null}</>;
  return <OptimizeCard ids={check.ids} selection={selection} className="safety-check-card" target={`safety-check:${check.rule}`} count={check.count} countLabel={plural(check.count, "notice")}
    label={`Select ${single ? "the notice" : `all ${check.count.toLocaleString()} notices`}: ${check.title}`}>
    <div className="safety-check-head"><strong className="safety-check-title">{check.title}</strong>{chips}</div>
    {where ? <span className="safety-check-where">{where}</span> : null}
    {check.resources.length ? <div className="safety-check-what">
      {check.under ? <span>All under <code title={check.under}>{safetyShortName(check.under)}</code></span> : null}
      <ul>{check.resources.map(item => <li key={item.name}><code title={item.name}>{safetyShortName(item.name)}</code>{item.count > 1 ? <span className="safety-check-times"> ×{item.count}</span> : null}</li>)}</ul>
      {check.moreResources ? <span className="findings-muted">and {plural(check.moreResources, "other")}</span> : null}
    </div> : null}
    <span className="safety-check-when">{facts.join(" · ")}</span>
    <a className="safety-check-open" href={href}>{single ? "Open in the table →" : `List all ${check.count.toLocaleString()} →`}</a>
  </OptimizeCard>;
}

function SafetyCheckCart({ cart, onReview }: { cart: CartGroup[]; onReview: (group: CartGroup) => void }) {
  const groups = cart.map(group => ({ target: group.target, label: group.label, group,
    entries: safetyCartChecks(group).map(check => ({ key: check.rule, title: check.title, ids: check.ids, ticked: check.ticked, counted: true, detail: check.danger && safetyDangerLabels[check.danger] ? safetyDangerLabels[check.danger] : "" })) }));
  return <OptimizeCart className="safety-check-cart" title="Risky action prompts to copy" groups={groups} api={api} changed="pharos:safety-changed" noun={{ one: "notice", many: "notices" }} onReview={onReview}
    empty="Tick a check to copy a prompt for it, or add it to a prompt for its location. Each location gets its own prompt."
    note={<>Risky actions and efficiency use separate prompts. After copying, these notices leave Open and remain in Handed off. To choose single notices, use the <a href={safetyURL("table")}>table</a>.</>} />;
}

function DangerChip({ danger }: { danger?: SafetyDanger }) {
  return danger && safetyDangerLabels[danger] ? <span className={`findings-chip safety-danger ${danger}`}>{safetyDangerLabels[danger]}</span> : null;
}
