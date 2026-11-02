import React, { useCallback, useEffect, useRef, useState } from "react";
import { CartPanel, ReviewDialog, type CartGroup, type Target } from "./findings";
import { useViewState } from "./view-state";
import { ViewDisclosure } from "./view-disclosure";
import { OptimizePlaceFilter, OptimizeRank } from "./optimize-controls";
import { safetyDashboardReviews, safetyDashboardPlaces, safetyRankLabels, type SafetyRank } from "./safety-dashboard-model";
import { type SafetyView, safetyURL, safetyStateLabels } from "./safety-route";
import { onSafetyChange } from "./dataset-refresh";

const api = "/api/findings/safety";
type State = "open" | "handed_off" | "dismissed";
type Card = { id: string; title: string; explanation: string; state: State; target: string; target_label: string; first_seen_at: string; last_seen_at: string; handed_at?: string; material_change: boolean; calls: number; cart?: { target: string; target_label: string; ticked: boolean } };
type Evidence = { id: string; call_id: string; message_id: string; at: string; command: string; cwd: string; cwd_basis: string; status: string; outcome: string; conditional: boolean; path: string };
type Detail = Card & { subject: string; questions: string[]; evidence: Evidence[] };
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
  const [rank, setRank] = useViewState<SafetyRank>("safety_rank", "recent", view === "");
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
  const shown = safetyDashboardReviews(overview?.cases ?? [], list, place, rank);
  const places = safetyDashboardPlaces(overview?.cases ?? []);
  const actions = (card: Card) => <div className="findings-actions">
    {card.state === "open" ? <>
      {card.cart ? <button className="findings-button" onClick={() => void act(card.id, { action: "cart_remove" }, "Removed from risky action prompt.")}>In prompt · Remove</button>
        : <button className="findings-button primary" onClick={() => void act(card.id, { action: "cart_add" }, `Added to risky action prompt for ${card.target_label}.`)}>Add to prompt</button>}
      <button className="findings-button quiet" onClick={() => void act(card.id, { action: "dismiss" }, "Dismissed. You can bring it back at any time.")}>Dismiss</button>
    </> : <button className="findings-button" onClick={() => void act(card.id, { action: "restore" }, "Brought back to Open.")}>Bring back</button>}
  </div>;
  return <div className="findings-page safety-page">
    <div className="view-heading findings-heading"><div><h1>{view === "settings" ? "Risky action settings" : view === "table" && !selected ? "Risky actions table" : "Risky actions"}</h1><p className="muted">{view === "settings" ? "Choose which checks discover risky action reviews. Your choices follow the library between Macs." : view === "table" && !selected ? "Every risky action review, with its selected resource, location, calls, and handoff state. Filter, group, and chart them like any table." : selected ? "Questions about consequential agent actions. Collect a prompt and let your agent investigate." : "A risky action is an agent operation that could delete or expose data, change shared history, publish work, or alter a system. Pharos flags supported commands when their targets or potential consequences warrant review; a flag alone does not establish harm. Open Details to inspect the evidence, add a review to a prompt for your agent to investigate and recommend next steps, or dismiss expected activity. Copying the prompt records a handoff."}</p></div></div>
    {error ? <p className="findings-error" role="alert">{error}</p> : null}
    {notice ? <p className="safety-notice" role="status">{notice}</p> : null}
    {view === "settings" ? <SafetySettings refreshDiscovery={refreshDiscovery} /> : !overview ? <p>Loading risky action reviews…</p> : <>
      {selected ? <>
        <button className="findings-button" data-view-field="safety_case" onClick={() => select("")}>{view === "table" ? "← Risky actions table" : "← Risky action reviews"}</button>
        {detail ? <article className="findings-panel safety-detail" data-pharos-target={`safety:${detail.id}`}>
          <p className="findings-muted">{detail.target_label} · {stateLabel[detail.state]}</p>
          <h2>{detail.title}</h2><p>{detail.explanation}</p>
          <p className="safety-subject">{detail.subject}</p>
          {detail.handed_at ? <p className="findings-muted">Handed off {date(detail.handed_at)}. {detail.material_change ? "A later command has stronger execution evidence, so this review is open again." : "No returned report is required. This records the handoff; the concern may still need investigation."}</p> : null}
          {actions(detail)}
          <h3>Questions for your agent</h3><ul>{detail.questions.map(question => <li key={question}>{question}</li>)}</ul>
          <h3>Archived evidence</h3>
          <p className="findings-muted">{detail.calls} distinct calls in the latest discovery window; up to 20 examples below. Selection identifies commands to review, not confirmed harm or authorization.</p>
          {detail.evidence.map(entry => <ViewDisclosure key={entry.id} viewKey={`safety:${detail.id}:evidence:${entry.id}`} className="safety-evidence">
            <summary>{date(entry.at)} · {entry.outcome}</summary>
            <pre>{entry.command}</pre><p>Working directory: <code>{entry.cwd || "Unresolved"}</code></p><p className="findings-muted">{entry.cwd_basis}{entry.conditional ? ". Compound or conditional call: individual execution needs context." : ""}</p>
            <a href={entry.path}>Open archived conversation ↗</a>
          </ViewDisclosure>)}
        </article> : !error ? <p>Loading case…</p> : null}
      </> : view === "table" ? null : <>
        <div className="findings-toolbar">
          <div className="findings-tabs" role="tablist" aria-label="Risky action reviews by state">
            {(Object.keys(stateLabel) as State[]).map(state => <button role="tab" key={state} aria-selected={list === state} data-view-field="safety_list" onClick={() => setList(state)}>{stateLabel[state]} <span className="findings-count">{overview.summary[state]}</span></button>)}
          </div>
          <OptimizePlaceFilter places={places} value={place} onChange={setPlace} label="Show risky actions for" viewField="safety_place" />
          <OptimizeRank value={rank} onChange={setRank} options={safetyRankLabels} viewField="safety_rank" />
        </div>
        {list === "handed_off" ? <p className="findings-muted">You have handed these reviews to an agent. Repeats accumulate here; a new target, broader operation, or stronger execution evidence can need fresh attention.</p> : null}
        <div className="findings-layout with-cart">
          <div className="findings-list">
            {shown.map(card => <article key={card.id} className="findings-card safety-card" data-pharos-target={`safety:${card.id}`}>
              <div className="findings-card-meta"><span>{card.target_label}</span><span>{date(card.last_seen_at)}</span>{rank === "calls" ? <span>{card.calls.toLocaleString()} {card.calls === 1 ? "call" : "calls"}</span> : null}{card.material_change ? <span className="findings-chip new">New evidence</span> : null}</div>
              <h2><button className="safety-card-title" data-view-field="safety_case" onClick={() => select(card.id)}>{card.title}</button></h2>
              <p>{card.explanation}</p>
              <div className="safety-card-actions">{actions(card)}<button className="findings-button quiet" data-view-field="safety_case" onClick={() => select(card.id)}>Details</button></div>
            </article>)}
            {!shown.length ? <div className="findings-empty"><strong>{place ? "No reviews for this location." : list === "open" ? "No open risky action reviews." : list === "handed_off" ? "Nothing handed off yet." : "Nothing dismissed."}</strong><p>{place ? "Choose Everywhere to see every review in this list." : list === "open" ? "Supported commands have not produced an open review. Coverage and uncertainties are below." : "Your reviews will appear here."}</p></div> : null}
          </div>
          <CartPanel safety cart={overview.cart} targets={overview.targets} act={act} onReview={next => setReview(next.target)} />
        </div>
      </>}
      {view !== "table" ? <ViewDisclosure viewKey="safety:coverage" className="safety-coverage"><summary>What Pharos reviewed{overview.built_at ? ` · updated ${date(overview.built_at)}` : ""}</summary><p>{overview.built_at ? `${Number(overview.scanned_calls).toLocaleString()} retained shell calls examined. ` : "Risky action discovery runs with the next full Findings refresh. "}{overview.coverage}</p><p>Checks cover selected deletions, credential copies, SQL mutations, Git history, publication, cloud deletion, process selectors, permissions, HTTP mutations, and system or disk-image operations. These are deterministic heuristics; no agent investigation runs inside Pharos.</p><a href={safetyURL("settings")}>Risky action settings and refresh</a></ViewDisclosure> : null}
    </>}
    {table ? <div className="safety-table" hidden={view !== "table" || Boolean(selected)}>{table}</div> : null}
    {group ? <ReviewDialog key={group.target} safety group={group} copy={copy} onClose={() => setReview("")} onCopied={() => { setReview(""); window.dispatchEvent(new Event("pharos:safety-changed")); setNotice("Copied. Reviews moved to Handed off."); }} /> : null}
  </div>;
}

type Settings = { rules: Array<{ id: string; title: string; explanation: string; enabled: boolean }>; built_at: string | null; scanned_calls: string; window_days: number; version: string };
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
    try { setSettings(await request<Settings>(`${api}/settings`, { rule_enabled: { [id]: enabled } })); setMessage("Discovery setting saved. Existing reviews and prompts remain available."); }
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
      <section className="findings-panel"><h2>Discovery checks</h2><p className="findings-muted">{settings.rules.length} checks · {settings.rules.filter(rule => rule.enabled).length} enabled</p><p>Choose which operations create new reviews or update existing evidence. Turning off a check keeps existing reviews, prompt baskets, and handoff history.</p>
        <div className="safety-checks">{settings.rules.map(rule => <label key={rule.id} className="safety-check"><input type="checkbox" checked={rule.enabled} disabled={busy} onChange={event => void save(rule.id, event.target.checked)} /><span><strong>{rule.title}</strong><small>{rule.explanation}</small></span></label>)}</div>
      </section>
      <section className="findings-panel"><h2>Refresh and coverage</h2><p>A full refresh examines supported shell calls from the last {settings.window_days} days. A single event can qualify; efficiency thresholds do not apply.</p><p className="findings-muted">{settings.built_at ? `Last discovery: ${date(settings.built_at)} · ${Number(settings.scanned_calls).toLocaleString()} retained shell calls examined.` : "No safety discovery has run yet."}</p><button className="findings-button" disabled={busy} onClick={() => void refresh()}>Refresh efficiency and risky actions now</button><p>Discovery uses the shared Findings background schedule. Embedded scripts and arbitrary native or MCP mutations are not comprehensively interpreted.</p></section>
      <section className="findings-panel"><h2>Prompt handoff</h2><p>Risky action prompts ask an agent to investigate, explain, and recommend. Copying moves selected reviews to Handed off; no returned report is required. You can bring a review back at any time.</p><p>Risky actions and efficiency have separate baskets. Repeats accumulate quietly; a new target, broader operation, or stronger execution evidence can need fresh attention.</p></section>
    </>}
  </div>;
}
