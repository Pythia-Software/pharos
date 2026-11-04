import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { CartPanel, Dialog, ReviewDialog, type CartGroup, type Target } from "./findings";
import { useViewState } from "./view-state";
import { ViewDisclosure } from "./view-disclosure";
import { OptimizePlaceFilter, OptimizeRank } from "./optimize-controls";
import { safetyShortName, safetyDashboardPlaces, safetyDashboardChecks, safetyDangerLabels, safetyRankLabels, safetySelectionPlan, safetyPromptLimit, safetyCartChecks, type SafetyCartCheck, type SafetyCheckSummary, type SafetyDanger, type SafetyRank, type SafetySelectionGroup } from "./safety-dashboard-model";
import { type SafetyView, safetyURL, safetyCheckTableURL, safetyStateLabels, safetyTableFocus } from "./safety-route";
import type { QueryState } from "@pythia-software/query-table-core";
import { useSelection, type SelectionApi } from "@pythia-software/query-table-react";
import { SelectionToolbar } from "@pythia-software/query-table-ui";
import { onSafetyChange } from "./dataset-refresh";

const api = "/api/findings/safety";
type State = "open" | "handed_off" | "dismissed";
type Card = { id: string; rule: string; danger: SafetyDanger; subject: string; title: string; explanation: string; state: State; target: string; target_label: string; first_seen_at: string; last_seen_at: string; handed_at?: string; material_change: boolean; calls: number; cart?: { target: string; target_label: string; ticked: boolean } };
type Evidence = { id: string; call_id: string; message_id: string; at: string; command: string; cwd: string; cwd_basis: string; status: string; outcome: string; conditional: boolean; path: string };
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
  // goes into one prompt (one per location) or is dismissed at once.
  const listed = useMemo(() => checks.flatMap(check => check.ids), [overview, list, place]);
  const selection = useSelection(listed);
  useEffect(() => { selection.clear(); }, [list, place]);
  const checkNotices = (rule: string) => overview?.cases.filter(card => card.rule === rule).length ?? 0;
  const actions = (card: Card) => <div className="findings-actions">
    {card.state === "open" ? <>
      {card.cart ? <button className="findings-button" onClick={() => void act(card.id, { action: "cart_remove" }, "Removed from risky action prompt.")}>In prompt · Remove</button>
        : <button className="findings-button primary" onClick={() => void act(card.id, { action: "cart_add" }, `Added to risky action prompt for ${card.target_label}.`)}>Add to prompt</button>}
      <button className="findings-button quiet" onClick={() => void act(card.id, { action: "dismiss" }, "Dismissed. You can bring it back at any time.")}>Dismiss</button>
    </> : <button className="findings-button" onClick={() => void act(card.id, { action: "restore" }, "Brought back to Open.")}>Bring back</button>}
  </div>;
  return <div className="findings-page safety-page">
    <div className="view-heading findings-heading"><div><h1>{view === "settings" ? "Risky action settings" : view === "table" && !selected ? "Risky actions table" : "Risky actions"}</h1><p className="muted">{view === "settings" ? "Choose which checks raise risky action notices. Your choices follow the library between Macs." : view === "table" && !selected ? "Risky action notices, with their selected resources, locations, and handoff state. Tick rows to act on several at once; filter, group, and chart them like any table." : selected ? "Questions about consequential agent actions. Collect a prompt and let your agent investigate." : "A risky action is an agent operation that could expose credentials, rewrite shared history, delete data outside its work, or alter a system. Pharos raises a notice only where a command is likely to deserve your attention, and skips an agent's own scratch, build output and work in progress; a notice alone does not establish harm. Each card is one check; the most dangerous come first. Tick a card to copy a prompt asking your agent to investigate its notices and recommend fixes, or to dismiss expected activity, or open its table to look at each notice. Copying the prompt records a handoff."}</p></div></div>
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
            <pre>{entry.command}</pre><p>Working directory: <code>{entry.cwd || "Unresolved"}</code></p><p className="findings-muted">{entry.cwd_basis}{entry.conditional ? ". Compound or conditional call: individual execution needs context." : ""}</p>
            <a href={entry.path}>Open archived conversation ↗</a>
          </ViewDisclosure>)}
        </article> : !error ? <p>Loading case…</p> : null}
      </> : view === "table" ? null : <>
        <div className="findings-toolbar">
          <div className="findings-tabs" role="tablist" aria-label="Risky action notices by state">
            {(Object.keys(stateLabel) as State[]).map(state => <button role="tab" key={state} aria-selected={list === state} data-view-field="safety_list" onClick={() => setList(state)}>{stateLabel[state]} <span className="findings-count">{overview.summary[state]}</span></button>)}
          </div>
          <OptimizePlaceFilter places={places} value={place} onChange={setPlace} label="Show risky actions for" viewField="safety_place" />
          <OptimizeRank value={rank} onChange={setRank} options={safetyRankLabels} viewField="safety_rank" />
        </div>
        {list === "handed_off" ? <p className="findings-muted">You have handed these notices to an agent. Repeats accumulate here; a new target, broader operation, or stronger execution evidence can need fresh attention.</p> : null}
        <div className="findings-layout with-cart">
          {checks.length ? <section className="safety-check-cards" aria-label="Risky actions by check">
            <SafetySelectionBar selection={selection} copy={copy} />
            <div className="safety-check-grid">{checks.map(check => <SafetyCheckCard key={check.rule} check={check} place={place} selection={selection} href={safetyCheckTableURL(check.rule, { state: list, target: place })} />)}</div>
          </section> : <div className="findings-empty"><strong>{place ? "No notices for this location." : list === "open" ? "No open risky action notices." : list === "handed_off" ? "Nothing handed off yet." : "Nothing dismissed."}</strong><p>{place ? "Choose Everywhere to see every notice in this list." : list === "open" ? "Supported commands have not raised an open notice. Coverage and uncertainties are below." : "Your notices will appear here."}</p></div>}
          <SafetyCheckCart cart={overview.cart} onReview={next => setReview(next.target)} />
        </div>
      </>}
      {view !== "table" ? <ViewDisclosure viewKey="safety:coverage" className="safety-coverage" summary={<>What Pharos checked{overview.built_at ? ` · updated ${date(overview.built_at)}` : ""}</>}><p>{overview.built_at ? `${Number(overview.scanned_calls).toLocaleString()} retained shell calls examined. ` : "Risky action discovery runs with the next full Findings refresh. "}{overview.coverage}</p><p>Checks cover credential copies, rewritten published history, cloud deletion, disk and system changes, deletions outside the checkout, database changes, broadened permissions, deleted releases, discarded shared Git work, program-wide process signals, and remote API changes. They skip temporary, build and scratch locations. These are deterministic heuristics; no agent investigation runs inside Pharos.</p><a href={safetyURL("settings")}>Risky action settings and refresh</a></ViewDisclosure> : null}
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
export function SafetyTableContext({ query, total }: { query: QueryState; total: number | null | undefined }) {
  const focus = safetyTableFocus(query);
  const overview = useSafetyOverview();
  const [rules, setRules] = useState<Rule[]>([]);
  useEffect(() => { let current = true; void request<Settings>(`${api}/settings`).then(value => { if (current) setRules(value.rules); }).catch(() => {}); return () => { current = false; }; }, []);
  const rule = focus && rules.find(item => item.id === focus.rule);
  if (!focus || !rule) return null;
  // A location-filtered table omits its location columns, so name it from the
  // notices themselves.
  const place = focus.target ? overview?.cases.find(card => card.target === focus.target)?.target_label ?? "one location" : focus.place ?? "every location";
  const state = focus.state ? stateLabel[focus.state as State] ?? focus.state : "every state";
  return <section className="findings-panel safety-check-context" data-pharos-target={`safety-check:${focus.rule}`}>
    <p className="findings-muted">Risky actions · {state} · {place}</p>
    <h2>{rule.title}</h2>
    <p>{rule.explanation}</p>
    <p>{typeof total === "number" ? `${total.toLocaleString()} ${total === 1 ? "notice" : "notices"}. ` : ""}Each row is one path or resource the check selected, sorted by path, with the latest command that selected it. Tick rows to dismiss expected activity, or to copy a prompt asking your agent to investigate them.</p>
    {rule.questions?.length ? <><h3>Questions for each notice</h3><ul>{rule.questions.map(question => <li key={question}>{question}</li>)}</ul></> : null}
  </section>;
}

// Actions on the table's selected notices. Each applies to every selected
// notice or to none. A prompt asks an agent working in one location, so a
// selection across locations becomes one prompt per location. The bar
// outlives the selection, so its outcome and dialogs stay on screen.
export function SafetySelectionBar({ selection, copy }: { selection: SelectionApi; copy: (text: string) => Promise<void> }) {
  const overview = useSafetyOverview();
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<React.ReactNode>("");
  const [choosing, setChoosing] = useState(false);
  const [group, setGroup] = useState<CartGroup | null>(null);
  const ids = [...selection.selected].map(String);
  const plan = safetySelectionPlan(overview?.cases ?? [], ids);
  const { groups, blocked } = overview ? plan : { ...plan, blocked: "Loading these notices…" };
  const count = (n: number) => `${n.toLocaleString()} ${n === 1 ? "notice" : "notices"}`;
  const places = (list: SafetySelectionGroup[]) => list.map(item => `${item.label} (${item.ids.length.toLocaleString()})`).join(", ");
  async function bulk(action: string, chosen: string[]) {
    setBusy(true); setMessage("");
    try {
      await request(`${api}/bulk`, { action, ids: chosen });
      window.dispatchEvent(new Event("pharos:safety-changed"));
      return true;
    } catch (failure) { setMessage((failure as Error).message); return false; }
    finally { setBusy(false); }
  }
  async function review(chosen: SafetySelectionGroup) {
    setChoosing(false);
    if (!await bulk("cart_add", chosen.ids)) return;
    const cards = new Map(plan.selected.map(card => [card.id, card]));
    setGroup({ target: chosen.target, label: chosen.label, handoff: "review", handoff_reason: "Investigate and recommend; make no changes.", ticked: chosen.ids.length,
      items: chosen.ids.map(id => ({ id, title: cards.get(id)?.title ?? id, ticked: true, change_label: "Investigate and recommend", suggested_target: chosen.target })) });
  }
  function copyPrompts() {
    if (groups.length === 1 && groups[0]!.ids.length <= safetyPromptLimit) void review(groups[0]!); else setChoosing(true);
  }
  async function addToPrompts() {
    const added = groups;
    if (!await bulk("cart_add", ids)) return;
    selection.clear();
    setMessage(`Added ${count(ids.length)} to ${added.length === 1 ? `the prompt for ${added[0]!.label}` : `${added.length} prompts, one per location: ${places(added)}`}. Copy ${added.length === 1 ? "it" : "them"} from the prompts beside these notices.`);
  }
  async function setState(action: "dismiss" | "restore") {
    const n = ids.length;
    if (!await bulk(action, ids)) return;
    selection.clear();
    setMessage(action === "dismiss" ? `Dismissed ${count(n)}. Bring them back at any time.` : `Brought back ${count(n)} to Open.`);
  }
  const promptLabel = groups.length > 1 ? `Copy ${groups.length} prompts…` : "Copy prompt…";
  const addLabel = groups.length > 1 ? `Add to ${groups.length} location prompts` : groups.length === 1 ? `Add to ${groups[0]!.label} prompt` : "Add to prompt";
  return <div className="safety-selection">
    <SelectionToolbar selection={selection} className="safety-selection-toolbar" actions={() => <>
      <button type="button" className="findings-button primary" disabled={busy || Boolean(blocked)} onClick={copyPrompts}>{promptLabel}</button>
      <button type="button" className="findings-button" disabled={busy || Boolean(blocked)} onClick={() => void addToPrompts()}>{addLabel}</button>
      {!plan.known || plan.selected.some(card => card.state !== "dismissed") ? <button type="button" className="findings-button" disabled={busy} onClick={() => void setState("dismiss")}>Dismiss</button> : null}
      {plan.closed ? <button type="button" className="findings-button" disabled={busy} onClick={() => void setState("restore")}>Bring back</button> : null}
    </>} />
    {ids.length ? <p className="safety-selection-note">{blocked || (groups.length > 1
      ? `These notices come from ${groups.length} locations. A prompt asks an agent working in one location, so they become one prompt per location: ${places(groups)}.`
      : `A prompt for an agent working in ${groups[0]?.label ?? "this location"}. Copying hands these notices off; adding keeps them in that location's prompt to copy later.`)}</p> : null}
    {message ? <p className="safety-notice" role="status">{message}</p> : null}
    {choosing ? <Dialog title="Copy a prompt for one location" onClose={() => setChoosing(false)}>
      <p>A prompt asks an agent working in one location to investigate, so the {count(ids.length)} you selected become one prompt per location. Copy them one at a time; each copied prompt hands its notices off.</p>
      <ul className="safety-prompt-choices">{groups.map(item => <li key={item.target}>
        <span><strong>{item.label}</strong> · {count(item.ids.length)}</span>
        {item.ids.length > safetyPromptLimit ? <span className="findings-muted">A prompt holds up to {safetyPromptLimit} notices; narrow the selection for this location.</span>
          : <button type="button" className="findings-button primary" disabled={busy} onClick={() => void review(item)}>Review prompt</button>}
      </li>)}</ul>
    </Dialog> : null}
    {group ? <ReviewDialog key={group.target} safety group={group} copy={copy}
      onClose={() => { setGroup(null); setMessage(`Kept ${count(group.items.length)} in the prompt for ${group.label}; copy it from the prompts beside these notices.`); }}
      onCopied={() => {
        const copied = new Set(group.items.map(item => item.id)), remaining = ids.filter(id => !copied.has(id));
        setGroup(null); selection.replace(remaining);
        setMessage(remaining.length ? `Copied the prompt for ${group.label}; those notices moved to Handed off. ${count(remaining.length)} from other locations are still selected.` : `Copied the prompt for ${group.label}; those notices moved to Handed off.`);
        if (remaining.length) setChoosing(true);
        window.dispatchEvent(new Event("pharos:safety-changed"));
      }} /> : null}
  </div>;
}

const range = (first: string, last: string) => { const a = date(first), b = date(last); return a === b ? b : `${a} – ${b}`; };
const plural = (n: number, one: string, many = `${one}s`) => `${n.toLocaleString()} ${n === 1 ? one : many}`;

// One check's notices at a glance: what they share (one location, one
// directory, a recurring resource) before what varies, then when, how much,
// and what is new. The checkbox selects every notice on the card.
function SafetyCheckCard({ check, place, selection, href }: { check: SafetyCheckSummary; place: string; selection: SelectionApi; href: string }) {
  const box = useRef<HTMLInputElement>(null);
  const chosen = check.ids.filter(id => selection.isSelected(id)).length;
  const all = chosen === check.ids.length;
  useEffect(() => { if (box.current) box.current.indeterminate = chosen > 0 && !all; }, [chosen, all]);
  const toggle = () => {
    const next = new Set([...selection.selected].map(String));
    for (const id of check.ids) if (all) next.delete(id); else next.add(id);
    selection.replace(next);
  };
  const where = place ? null : check.locations === 1 ? `${check.count === 1 ? "In" : "All in"} ${check.places[0]?.label}`
    : `${check.places.map(item => `${item.label} ${item.count}`).join(" · ")}${check.morePlaces ? ` · ${plural(check.morePlaces, "more location")}` : ""}`;
  const single = check.count === 1;
  const facts = [range(check.first_seen_at, check.last_seen_at), plural(check.calls, "call"), check.fresh ? `${check.fresh.toLocaleString()} with new evidence` : "", check.inPrompt ? `${check.inPrompt.toLocaleString()} in a prompt` : ""].filter(Boolean);
  const chips = <>{check.danger ? <DangerChip danger={check.danger} /> : null}{check.recent ? <span className="findings-chip safety-recent">Recent</span> : null}</>;
  return <article className={`safety-check-card${chosen ? " selected" : ""}`} data-pharos-target={`safety-check:${check.rule}`}>
    <input ref={box} className="safety-check-select" type="checkbox" checked={all} onChange={toggle} aria-label={`Select ${single ? "the notice" : `all ${check.count.toLocaleString()} notices`}: ${check.title}`} />
    <div className="safety-check-body">
    <div className="safety-check-head"><strong className="safety-check-title">{check.title}</strong>{chips}</div>
    {where ? <span className="safety-check-where">{where}</span> : null}
    {check.resources.length ? <div className="safety-check-what">
      {check.under ? <span>All under <code title={check.under}>{safetyShortName(check.under)}</code></span> : null}
      <ul>{check.resources.map(item => <li key={item.name}><code title={item.name}>{safetyShortName(item.name)}</code>{item.count > 1 ? <span className="safety-check-times"> ×{item.count}</span> : null}</li>)}</ul>
      {check.moreResources ? <span className="findings-muted">and {plural(check.moreResources, "other")}</span> : null}
    </div> : null}
    <span className="safety-check-when">{facts.join(" · ")}</span>
    <a className="safety-check-open" href={href}>{single ? "Open in the table →" : `List all ${check.count.toLocaleString()} →`}</a>
    </div>
    <span className="safety-check-count" aria-label={plural(check.count, "notice")}>{check.count.toLocaleString()}</span>
  </article>;
}

function SafetyCheckCart({ cart, onReview }: { cart: CartGroup[]; onReview: (group: CartGroup) => void }) {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const bulk = async (body: Record<string, unknown>, ids: string[]) => {
    setBusy(true); setError("");
    try { await request(`${api}/bulk`, { ...body, ids }); window.dispatchEvent(new Event("pharos:safety-changed")); }
    catch (failure) { setError((failure as Error).message); }
    finally { setBusy(false); }
  };
  return <aside className="findings-cart safety-check-cart" id="findingsCart" aria-labelledby="findingsCartTitle">
    <h2 id="findingsCartTitle">Risky action prompts to copy</h2>
    {error ? <p className="findings-error" role="alert">{error}</p> : null}
    {!cart.length ? <p className="findings-cart-empty">Tick a check to copy a prompt for it, or add it to a prompt for its location. Each location gets its own prompt.</p> : null}
    {cart.map(group => {
      const checks = safetyCartChecks(group), ticked = checks.reduce((sum, check) => sum + check.ticked, 0);
      return <section key={group.target} className="findings-cart-group" aria-label={`Prompt for ${group.label}`}>
        <div className="findings-cart-head"><h3>{group.label}</h3><span>{plural(ticked, "notice")} ticked</span></div>
        <ul>{checks.map(check => <SafetyCartCheck key={check.rule} check={check} busy={busy}
          onTick={value => void bulk({ action: "cart_tick", ticked: value }, check.ids)}
          onRemove={() => void bulk({ action: "cart_remove" }, check.ids)} />)}</ul>
        <button type="button" className="findings-button primary" disabled={busy || !ticked} onClick={() => onReview(group)}>Review prompt ({plural(ticked, "notice")})</button>
      </section>;
    })}
    <p className="findings-cart-note">Risky actions and efficiency use separate prompts. After copying, these notices leave Open and remain in Handed off. To choose single notices, use the <a href={safetyURL("table")}>table</a>.</p>
  </aside>;
}

function SafetyCartCheck({ check, busy, onTick, onRemove }: { check: SafetyCartCheck; busy: boolean; onTick: (ticked: boolean) => void; onRemove: () => void }) {
  const box = useRef<HTMLInputElement>(null);
  const all = check.ticked === check.ids.length;
  useEffect(() => { if (box.current) box.current.indeterminate = check.ticked > 0 && !all; }, [check.ticked, all]);
  return <li className={check.ticked ? "" : "unticked"}>
    <label>
      <input ref={box} type="checkbox" checked={all} disabled={busy} onChange={event => onTick(event.target.checked)} />
      <span><span className="findings-cart-title">{check.title}</span><small>{check.ticked === check.ids.length ? plural(check.ids.length, "notice") : `${check.ticked.toLocaleString()} of ${plural(check.ids.length, "notice")} ticked`}{check.danger && safetyDangerLabels[check.danger] ? ` · ${safetyDangerLabels[check.danger]}` : ""}</small></span>
    </label>
    <button type="button" className="findings-button quiet safety-cart-remove" disabled={busy} aria-label={`Remove “${check.title}” from this prompt`} onClick={onRemove}>Remove</button>
  </li>;
}

function DangerChip({ danger }: { danger?: SafetyDanger }) {
  return danger && safetyDangerLabels[danger] ? <span className={`findings-chip safety-danger ${danger}`}>{safetyDangerLabels[danger]}</span> : null;
}
