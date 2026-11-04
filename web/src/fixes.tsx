import React, { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { ReviewDialog, type CartGroup, type Target } from "./findings";
import { useViewState } from "./view-state";
import { ViewDisclosure } from "./view-disclosure";
import { OptimizePlaceFilter, OptimizeRank } from "./optimize-controls";
import { OptimizeCard, OptimizeCart, OptimizeSelectionBar } from "./optimize-shared";
import { Icon } from "./icons";
import { eventReaderURL } from "./agent-event-examples";
import { onFixesChange } from "./dataset-refresh";
import { safetyURL } from "./safety-route";
import { type FixesView, fixesURL, fixDetailURL, fixesStateLabels } from "./fixes-route";
import {
  fixesBoundsPreview, fixesDashboard, fixesFacts, fixesHiddenNote, fixesHiddenReason, fixesKindsPresent, fixesPlaces, fixesRange,
  fixesRankExplanations, fixesRankLabels, type FixCard, type FixCount, type FixesBounds, type FixesRank, type FixesState, type FixGuidanceHit, type FixStatus,
} from "./fixes-model";
import { useSelection, type SelectionApi } from "@pythia-software/query-table-react";
import "./fixes.css";

// Optimize → Fixes. The grouping runs in the background (fixes.go); this page
// reads its items, their triage and the prompt baskets, and writes only the
// person's choices.
const api = "/api/findings/fixes";
const changed = "pharos:fixes-changed";
const noun = { one: "fix", many: "fixes" };
const place = { one: "place", many: "places" };

type Overview = {
  items: FixCard[]; summary: Record<FixesState, number>; hidden: { stale: number; rare: number; total: number };
  bounds: FixesBounds; default_bounds: FixesBounds; cart: CartGroup[]; targets: Target[];
  built_at: string | null; end: string | null; in_scope_events: string; excluded_events: number; safeguard_events: number; prompt_limit: number; coverage: string;
};
type Quote = { event_id: string; sentence: string; about?: string; kind: string; kind_label: string; repository: string; model: string; day: string; source: string; workspace_id?: string; conversation_id: string; message_id: string };
type CommitNote = { repository: string; sha: string; day: string; subject: string; term: string; events_after?: number };
type Detail = Omit<FixCard, "status"> & {
  suggestion: string; category_explanation: string; quotes: Quote[]; members: Quote[]; member_count: number; guidance_hits: FixGuidanceHit[];
  tool_errors: FixCount[]; kinds: FixCount[]; models: FixCount[]; thinking: number; cost: { episodes: number; hours: number; calls: number; unresolved: number };
  status: FixStatus & { commits_since?: CommitNote[]; persisted?: CommitNote };
};

async function request<T>(url: string, body?: unknown): Promise<T> {
  const response = await fetch(url, body === undefined ? undefined : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const value = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(value.error ?? `Request failed (${response.status})`);
  return value as T;
}
const plural = (n: number, one: string, many = `${one}s`) => `${n.toLocaleString()} ${n === 1 ? one : many}`;
const date = (value?: string | null) => value ? new Date(value).toLocaleDateString(undefined, { month: "short", day: "numeric" }) : "";
const notify = () => window.dispatchEvent(new Event(changed));

function useFixesOverview(active: unknown) {
  const [overview, setOverview] = useState<Overview | null>(null);
  const [error, setError] = useState("");
  const sequence = useRef(0);
  const reload = useCallback(async () => {
    const mine = ++sequence.current;
    try { const value = await request<Overview>(api); if (mine === sequence.current) { setOverview(value); setError(""); } }
    catch (failure) { if (mine === sequence.current) setError((failure as Error).message); }
  }, []);
  useEffect(() => { void reload(); }, [reload, active]);
  useEffect(() => {
    const stop = onFixesChange(() => void reload());
    return () => { stop(); sequence.current++; };
  }, [reload]);
  return { overview, error, setError };
}

export function FixesPage({ copy, view = "", refreshDiscovery }: { copy: (text: string) => Promise<void>; view?: FixesView; refreshDiscovery: () => Promise<unknown> }) {
  const dashboard = view === "";
  const [list, setList] = useViewState<FixesState>("fixes_list", "open", dashboard);
  const [where, setWhere] = useViewState<string>("fixes_place", "", dashboard);
  const [kind, setKind] = useViewState<string>("fixes_kind", "", dashboard);
  const [rank, setRank] = useViewState<FixesRank>("fixes_rank", "priority", dashboard);
  const [showHidden, setShowHidden] = useViewState<boolean>("fixes_hidden", false, dashboard);
  const [selected, select] = useViewState<string>("fixes_item", "", dashboard);
  const { overview, error, setError } = useFixesOverview(view);
  const [review, setReview] = useState("");
  const [notice, setNotice] = useState("");
  const items = overview?.items ?? [];
  const shown = fixesDashboard(items, { list, place: where, kind, rank, showHidden });
  const listed = useMemo(() => [...shown.main, ...shown.maybe].map(item => item.id), [overview, list, where, kind, rank, showHidden]);
  const selection = useSelection(listed);
  useEffect(() => { selection.clear(); }, [list, where, kind, showHidden]);
  const group = overview?.cart.find(item => item.target === review);
  const act = useCallback(async (id: string, body: Record<string, unknown>, done: string) => {
    try { await request(`${api}/${encodeURIComponent(id)}/action`, body); notify(); setNotice(done); }
    catch (failure) { setError((failure as Error).message); }
  }, [setError]);

  const lede = view === "settings" ? "Choose which recurring problems stay on the menu. Your choices follow the library between Macs."
    : selected ? "One recurring problem, the agents' own words about it, and where the fix probably belongs."
      : "Problems agents keep running into that a change to guidance, the environment, a tool, setup or docs could prevent. Each card is one problem seen across separate work items, with where the fix probably belongs. Tick cards to copy a prompt asking your agent to investigate or make the change, or close what you don't want to fix. Copying the prompt records a handoff; a problem that recurs afterwards opens again.";
  return <div className="findings-page fixes-page">
    <div className="view-heading findings-heading"><div><h1>{view === "settings" ? "Fixes settings" : "Fixes"}</h1><p className="muted">{lede}</p></div></div>
    {error ? <p className="findings-error" role="alert">{error}</p> : null}
    {notice ? <p className="safety-notice" role="status">{notice}</p> : null}
    {view === "settings" ? <FixesSettings overview={overview} refreshDiscovery={refreshDiscovery} />
      : !overview ? <p className="findings-muted">Loading fixes…</p>
        : selected ? <FixDetailView id={selected} overview={overview} back={() => select("")} act={act} onReview={setReview} />
          : <>
            {!overview.built_at ? <div className="findings-empty"><strong>No fixes yet.</strong><p>Fixes are grouped from agent events in the background, after the agent events pass has read your conversations. Turn it on in Settings → Background Processing; grouping follows on its own.</p></div> : null}
            <div className="findings-toolbar">
              <div className="findings-tabs" role="tablist" aria-label="Fixes by state">
                {(Object.keys(fixesStateLabels) as FixesState[]).map(state => <button role="tab" key={state} aria-selected={list === state} data-view-field="fixes_list" onClick={() => setList(state)}>{fixesStateLabels[state]} <span className="findings-count">{shown.counts[state]}</span></button>)}
              </div>
              <OptimizePlaceFilter places={fixesPlaces(items)} value={where} onChange={setWhere} label="Show fixes for" viewField="fixes_place" />
              <FixesKindFilter items={items} value={kind} onChange={setKind} />
              <OptimizeRank value={rank} onChange={setRank} options={fixesRankLabels} viewField="fixes_rank" />
            </div>
            <p className="findings-muted fixes-rank-note">{fixesRankExplanations[rank]}</p>
            {shown.hidden.total ? <p className="fixes-hidden-note" data-pharos-target="control:fixes_hidden">
              {fixesHiddenNote(shown.hidden, overview.bounds)}{" "}
              <button type="button" className="fixes-link-button" data-view-field="fixes_hidden" aria-pressed={showHidden} onClick={() => setShowHidden(!showHidden)}>{showHidden ? "Hide them" : `Show ${shown.hidden.total === 1 ? "it" : `all ${shown.hidden.total.toLocaleString()}`}`}</button>
              {" · "}<a href={fixesURL("settings")}>Change these bounds</a>
            </p> : null}
            {list === "handed_off" ? <p className="findings-muted">You handed these to an agent. A problem that recurs after its handoff moves back to Open.</p> : null}
            <div className="findings-layout with-cart">
              <section className="fixes-cards" aria-label="Recurring problems">
                <OptimizeSelectionBar selection={selection} copy={copy} items={items} api={api} area="fixes" changed={changed} noun={noun} place={place} promptLimit={overview.prompt_limit} />
                {shown.main.length ? <div className="optimize-card-grid">{shown.main.map(item => <FixCardView key={item.id} item={item} selection={selection} bounds={overview.bounds} />)}</div>
                  : <div className="findings-empty"><strong>{where || kind ? "No fixes match these filters." : list === "open" ? "No open fixes." : list === "handed_off" ? "Nothing handed off yet." : "Nothing closed."}</strong>
                    <p>{where || kind ? "Choose Everywhere and All kinds to see every fix in this list." : list === "open" && shown.hidden.total ? "Your settings hide every open problem; show them above, or loosen the bounds." : list === "open" ? "No problem recurred across enough separate work items." : "Fixes you act on appear here."}</p></div>}
                {shown.maybe.length ? <ViewDisclosure viewKey="fixes:maybe" className="fixes-maybe" summary={<>Maybe worth writing down · {plural(shown.maybe.length, "codebase fact")} fewer than five work items relearned</>}>
                  <p className="findings-muted">Often a task's own findings rather than something guidance should carry. Check before acting.</p>
                  <div className="optimize-card-grid">{shown.maybe.map(item => <FixCardView key={item.id} item={item} selection={selection} bounds={overview.bounds} />)}</div>
                </ViewDisclosure> : null}
              </section>
              <FixesCart cart={overview.cart} items={items} onReview={next => setReview(next.target)} />
            </div>
            <ViewDisclosure viewKey="fixes:coverage" className="safety-coverage" summary={<>What Pharos grouped{overview.built_at ? ` · updated ${date(overview.built_at)}` : ""}</>}>
              <p>{overview.built_at ? `${Number(overview.in_scope_events || 0).toLocaleString()} agent events in scope${overview.end ? `, through ${date(overview.end)}` : ""}. ` : ""}{overview.coverage}</p>
              <p>{overview.excluded_events ? `${plural(overview.excluded_events, "event")} about your own requests were left out: they are about how a task was asked, not something to fix in the setup. ` : ""}Which events count as the same problem, the kind of fix and where it belongs are rules of thumb over the agents' words; counts are exact. No language model runs to make these cards or their prompts.</p>
              <p>{overview.safeguard_events ? <>{plural(overview.safeguard_events, "event")} where an agent got around a safeguard (skipped a test, worked around a restriction) are notices in <a href={safetyURL()}>Risky actions</a>, one per instance, rather than fixes. A card says when its conversations hold any.</> : <>Agents getting around a safeguard raise notices in <a href={safetyURL()}>Risky actions</a> rather than fixes.</>}</p>
              <a href={fixesURL("settings")}>Fixes settings and refresh</a>
            </ViewDisclosure>
          </>}
    {group ? <ReviewDialog key={group.target} area="fixes" group={group} copy={copy} onClose={() => setReview("")} onCopied={() => { setReview(""); notify(); setNotice("Copied. These fixes moved to Handed off."); }} /> : null}
  </div>;
}

function FixesKindFilter({ items, value, onChange }: { items: readonly FixCard[]; value: string; onChange: (value: string) => void }) {
  const kinds = fixesKindsPresent(items);
  if (kinds.length < 2) return null;
  return <label className="findings-filter" data-pharos-target="control:fixes_kind">
    <span className="visually-hidden">Kind of fix</span>
    <select data-view-field="fixes_kind" value={value} onChange={event => onChange(event.target.value)} aria-label="Kind of fix">
      <option value="">All kinds</option>
      {kinds.map(kind => <option key={kind.key} value={kind.key}>{kind.label} ({kind.count})</option>)}
    </select><Icon name="chevron-down" />
  </label>;
}

const statusClass: Record<string, string> = { recurring: "warn", quiet: "good", "quiet-fixed": "good" };
function StatusChip({ item }: { item: Pick<FixCard, "status" | "recurred"> }) {
  return <>
    <span className={`findings-chip ${statusClass[item.status.state] ?? ""}`} title={item.status.detail}>{item.status.label}</span>
    {item.recurred ? <span className="findings-chip bad" title="It happened again after you handed it off">New since handoff</span> : null}
  </>;
}
const scopeLabel = (item: Pick<FixCard, "scope" | "target_label">) => item.scope === "global" ? "Across repositories" : item.target_label;

function FixCardView({ item, selection, bounds }: { item: FixCard; selection: SelectionApi; bounds: FixesBounds }) {
  const named = item.files.filter(file => !item.terms.includes(file));
  const hidden = fixesHiddenReason(item, bounds);
  return <OptimizeCard ids={[item.id]} selection={selection} className={`fixes-card${hidden ? " hidden-by-bounds" : ""}${item.deprioritized ? " quiet" : ""}`} target={`fix:${item.id}`}
    count={item.work_items} countLabel={plural(item.work_items, "work item")} label={`Select: ${item.title}`}>
    <a className="fixes-card-title" href={fixDetailURL(item.id)}>{item.title}</a>
    <div className="fixes-chips">
      <span className="findings-chip">{item.category_label}</span>
      <span className="findings-chip" title={item.scope_reason}>{scopeLabel(item)}</span>
      <StatusChip item={item} />
      {item.groups > 1 ? <span className="findings-chip" title="Recurring groups with the same key term, kind of fix and destination, shown as one card">{item.groups} groups</span> : null}
      {item.cart ? <span className="findings-chip watching">In a prompt</span> : null}
      {hidden ? <span className="findings-chip" title={hidden}>Hidden by your settings</span> : null}
    </div>
    <div className="fixes-where"><strong>{item.destination_kind}</strong> {item.destination}</div>
    {item.terms.length ? <div className="fixes-what">{item.terms.map(term => <code key={term}>{term}</code>)}{named.length ? <span className="findings-muted"> named: {named.map(file => <code key={file}>{file}</code>)}</span> : null}</div> : null}
    {item.guidance_hit ? <div className="fixes-hit">Already in {item.guidance_hit.file}:{item.guidance_hit.line}: “{item.guidance_hit.text.slice(0, 110)}”</div> : null}
    <span className="safety-check-when">{fixesFacts(item).join(" · ")}</span>
    {item.safeguards ? <span className="fixes-safeguards">{plural(item.safeguards, "safeguard workaround")} in these conversations · <a href={safetyURL()}>Risky actions</a></span> : null}
    <a className="safety-check-open" href={fixDetailURL(item.id)}>Evidence from {plural(item.events, "event")} →</a>
  </OptimizeCard>;
}

function FixesCart({ cart, items, onReview }: { cart: CartGroup[]; items: readonly FixCard[]; onReview: (group: CartGroup) => void }) {
  const byID = new Map(items.map(item => [item.id, item]));
  const groups = cart.map(group => ({ target: group.target, label: group.label, group,
    entries: group.items.map(entry => ({ key: entry.id, title: entry.title, ids: [entry.id], ticked: entry.ticked ? 1 : 0,
      detail: [byID.get(entry.id)?.category_label, byID.get(entry.id) ? plural(byID.get(entry.id)!.work_items, "work item") : ""].filter(Boolean).join(" · ") })) }));
  return <OptimizeCart className="fixes-cart" title="Fix prompts to copy" groups={groups} api={api} changed={changed} noun={noun} onReview={onReview}
    empty="Tick a card to copy a prompt for it, or add it to a prompt for its place. The shared setup and each repository get their own prompt."
    note="Fixes, risky actions and efficiency use separate prompts. After copying, these fixes leave Open and remain in Handed off; one that recurs opens again." />;
}

function FixDetailView({ id, overview, back, act, onReview }: { id: string; overview: Overview; back: () => void; act: (id: string, body: Record<string, unknown>, done: string) => Promise<void>; onReview: (target: string) => void }) {
  const [detail, setDetail] = useState<Detail | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let current = true;
    setError("");
    void request<Detail>(`${api}/${encodeURIComponent(id)}`).then(value => { if (current) setDetail(value); }).catch(failure => { if (current) { setDetail(null); setError(failure.message); } });
    return () => { current = false; };
  }, [id, overview]);
  const card = overview.items.find(item => item.id === id);
  const status = detail?.status;
  const hidden = card ? fixesHiddenReason(card, overview.bounds) : "";
  return <>
    <button type="button" className="findings-button" data-view-field="fixes_item" onClick={back}>← Fixes</button>
    {error ? <p className="findings-error" role="alert">{error === "Request failed (404)" || /not found/i.test(error) ? "This problem is no longer in the latest grouping." : error}</p> : null}
    {!detail ? (!error ? <p className="findings-muted">Loading…</p> : null) : <article className="findings-panel fixes-detail" data-pharos-target={`fix:${detail.id}`}>
      <div className="fixes-chips"><span className="findings-chip">{detail.category_label}</span><span className="findings-chip" title={detail.scope_reason}>{scopeLabel(detail)}</span>{card ? <StatusChip item={card} /> : null}<span className="findings-chip">{fixesStateLabels[card?.state ?? "open"]}</span></div>
      <h2>{detail.title}</h2>
      {hidden ? <p className="findings-muted">{hidden} It stays here and in its prompt until you change that.</p> : null}
      {card?.handed_at ? <p className="findings-muted">Handed off {date(card.handed_at)}.{card.recurred ? " It happened again since, so it is open again." : ""}</p> : null}
      {card ? <div className="findings-actions">{card.state === "open" ? <>
        {card.cart ? <><button type="button" className="findings-button primary" onClick={() => onReview(card.cart!.target)}>Review prompt for {card.cart.target_label}</button><button type="button" className="findings-button" onClick={() => void act(card.id, { action: "cart_remove" }, "Removed from the prompt.")}>Remove from prompt</button></>
          : <button type="button" className="findings-button primary" onClick={() => void act(card.id, { action: "cart_add" }, `Added to the prompt for ${card.target_label}.`)}>Add to prompt</button>}
        <button type="button" className="findings-button quiet" onClick={() => void act(card.id, { action: "dismiss" }, "Closed. You can bring it back at any time.")}>Close</button>
      </> : <button type="button" className="findings-button" onClick={() => void act(card.id, { action: "restore" }, "Brought back to Open.")}>Bring back</button>}</div> : null}

      <h3>What recurs</h3>
      <dl className="fixes-facts">
        <div><dt>Work items</dt><dd>{detail.work_items.toLocaleString()}</dd></div>
        <div><dt>Events</dt><dd>{detail.events.toLocaleString()}<small>{plural(detail.conversations, "conversation")} · {detail.thinking.toLocaleString()} in thinking</small></dd></div>
        <div><dt>Seen</dt><dd>{fixesRange(detail.status)}<small>{status && status.state !== "unknown" ? `${plural(status.days_quiet, "day")} before the data ends` : ""}</small></dd></div>
        <div><dt>Agent time</dt><dd>{detail.cost.episodes ? `${detail.cost.hours.toLocaleString(undefined, { maximumFractionDigits: 1 })} h` : "None measured"}<small>{detail.cost.episodes ? `${plural(detail.cost.episodes, "friction episode")} · ${detail.cost.unresolved.toLocaleString()} unresolved` : "Lessons don't start friction episodes"}</small></dd></div>
      </dl>
      <p className="fixes-counts">{[...detail.kinds.map(kind => `${kind.label ?? kind.key} ${kind.count}`), ...detail.repositories.map(repository => `${repository.key} ${repository.count}`), ...detail.models.map(model => `${model.key} ${model.count}`)].map(text => <span key={text} className="findings-chip">{text}</span>)}</p>
      {detail.tool_errors.length ? <><h4>Failed calls just before</h4><ul className="fixes-list">{detail.tool_errors.map(entry => <li key={entry.key}><code>{entry.key}</code> ×{entry.count}</li>)}</ul></> : null}
      {detail.files.length ? <p>Files and commands named: {detail.files.map(file => <code key={file}>{file} </code>)}</p> : null}
      {detail.safeguards ? <p className="fixes-safeguards">{plural(detail.safeguards, "event")} in these conversations got around a safeguard; they are notices in <a href={safetyURL()}>Risky actions</a>, where each instance is reviewed on its own.</p> : null}

      <h3>Where the fix probably belongs</h3>
      <p><strong>{detail.destination_kind}</strong>: {detail.destination}</p>
      <p className="findings-muted">{detail.scope_reason} {detail.category_label}: {detail.category_explanation}</p>
      <h3>What the change might be</h3>
      <p>{detail.suggestion}</p>

      <h3>Already in the guidance?</h3>
      {detail.guidance_hits.length ? <><ul className="fixes-list">{detail.guidance_hits.map(hit => <li key={`${hit.repository}:${hit.file}:${hit.line}`}><code>{hit.repository && hit.repository !== "(global)" ? `${hit.repository}/` : ""}{hit.file}:{hit.line}</code> names <code>{hit.term}</code>: “{hit.text}”</li>)}</ul>
        <p className="findings-muted">The guidance already mentions it and agents still run into it, so the fix may be in the environment or in enforcing it, not more words.</p></>
        : <p className="findings-muted">No current guidance file names its key terms{detail.terms.length ? <> ({detail.terms.map(term => <code key={term}>{term} </code>)})</> : null}.</p>}

      <h3>Has it stopped?</h3>
      <p>{card ? <StatusChip item={card} /> : null} {status?.detail}</p>
      {status?.commits_since?.length ? <p className="findings-muted">Guidance commits since it was last seen: {status.commits_since.map(note => `${note.repository} ${note.sha} (${note.day}) “${note.subject}”`).join("; ")}</p> : null}
      {status?.persisted ? <p className="findings-muted">It recurred {plural(status.persisted.events_after ?? 0, "time")} after {status.persisted.repository} {status.persisted.sha} (“{status.persisted.subject}”, {status.persisted.day}); that guidance change didn't stop it.</p> : null}

      <h3>What agents said</h3>
      <ul className="fixes-quotes">{detail.quotes.map(quote => <FixQuote key={quote.event_id} quote={quote} />)}</ul>
      {detail.member_count > detail.quotes.length ? <ViewDisclosure viewKey={`fix:${detail.id}:members`} className="fixes-members" summary={<>All {plural(detail.member_count, "event")}{detail.member_count > detail.members.length ? ` · the ${detail.members.length} closest shown` : ""}</>}>
        <ul className="fixes-quotes">{detail.members.map(quote => <FixQuote key={quote.event_id} quote={quote} />)}</ul>
      </ViewDisclosure> : null}
    </article>}
  </>;
}

function FixQuote({ quote }: { quote: Quote }) {
  const href = eventReaderURL({ workspace_id: quote.workspace_id, conversation_id: quote.conversation_id, message_id: quote.message_id });
  return <li>
    <blockquote>{quote.sentence}</blockquote>
    {quote.about && quote.about !== quote.sentence ? <p className="fixes-about"><span>About</span> {quote.about}</p> : null}
    <p className="findings-muted">{[quote.kind_label, quote.repository, quote.model, quote.day ? date(`${quote.day}T12:00:00`) : "No date", quote.source === "thinking" ? "In thinking" : "In a reply"].join(" · ")}{href ? <> · <a href={href}>Open in the reader</a></> : null}</p>
  </li>;
}

const quietOptions = [3, 7, 14, 30];
const hideOptions = [14, 30, 60, 90, 0];
const minimumOptions = [2, 3, 4, 5, 10];
type Settings = { bounds: FixesBounds; default_bounds: FixesBounds; open: number; hidden_stale: number; hidden_rare: number; built_at: string | null; end: string | null; window_days: number };
function FixesSettings({ overview, refreshDiscovery }: { overview: Overview | null; refreshDiscovery: () => Promise<unknown> }) {
  const [settings, setSettings] = useState<Settings | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const ids = { quiet: useId(), hide: useId(), minimum: useId() };
  useEffect(() => {
    let current = true;
    const load = () => void request<Settings>(`${api}/settings`).then(value => { if (current) setSettings(value); }).catch(failure => { if (current) setError(failure.message); });
    load(); const stop = onFixesChange(load);
    return () => { current = false; stop(); };
  }, []);
  async function save(body: Record<string, unknown>, done: string) {
    setBusy(true); setError(""); setMessage("");
    try { setSettings(await request<Settings>(`${api}/settings`, body)); notify(); setMessage(done); }
    catch (failure) { setError((failure as Error).message); }
    finally { setBusy(false); }
  }
  async function refresh() {
    setBusy(true); setError(""); setMessage("");
    try { await request("/api/findings/refresh", {}); await refreshDiscovery(); setMessage("Refresh requested. Grouping continues in the background."); }
    catch (failure) { setError((failure as Error).message); }
    finally { setBusy(false); }
  }
  const items = overview?.items ?? [];
  const preview = (bounds: FixesBounds) => fixesBoundsPreview(items, bounds);
  const label = (text: string, hidden: number) => overview ? `${text} · hides ${hidden.toLocaleString()}` : text;
  const bounds = settings?.bounds;
  const isDefault = bounds && settings && JSON.stringify(bounds) === JSON.stringify(settings.default_bounds);
  return <div className="safety-settings fixes-settings">
    {error ? <p className="findings-error" role="alert">{error}</p> : null}
    {message ? <p role="status">{message}</p> : null}
    {!settings || !bounds ? <p className="findings-muted">Loading fixes settings…</p> : <>
      <section className="findings-panel" data-pharos-target="section:fixes-bounds">
        <h2>What stays on the menu</h2>
        <p>A problem agents stopped running into, or one only a few work items hit, is rarely worth a change. These bounds rank such problems lower or take them off Open. Hidden problems are counted on the dashboard, and you can show them there at any time; what you handed off or closed stays listed.</p>
        <p className="findings-muted">{settings.open ? `${plural(settings.open, "open problem")}: ${(settings.open - settings.hidden_stale - settings.hidden_rare).toLocaleString()} shown, ${settings.hidden_stale.toLocaleString()} hidden as quiet, ${settings.hidden_rare.toLocaleString()} hidden as rare.` : "No open problems yet."}{settings.end ? ` "Quiet" counts days before ${date(settings.end)}, the latest day the agent events pass read.` : ""}</p>
        <div className="fixes-bounds">
          <label htmlFor={ids.quiet}><strong>Rank lower once quiet for</strong><small>Its priority drops to 60% past this, and to 30% past three times it.</small></label>
          <select id={ids.quiet} disabled={busy} value={bounds.quiet_days} onChange={event => void save({ quiet_days: Number(event.target.value) }, "Saved. Priorities use the new bound.")}>
            {[...new Set([...quietOptions, bounds.quiet_days])].sort((a, b) => a - b).map(days => <option key={days} value={days}>{plural(days, "day")}</option>)}
          </select>
          <label htmlFor={ids.hide}><strong>Hide once quiet for</strong><small>Off Open until it recurs.</small></label>
          <select id={ids.hide} disabled={busy} value={bounds.hide_days} onChange={event => void save({ hide_days: Number(event.target.value) }, "Saved. Open uses the new bound.")}>
            {[...new Set([...hideOptions, bounds.hide_days])].sort((a, b) => (a || 1e9) - (b || 1e9)).map(days => <option key={days} value={days}>{days ? label(plural(days, "day"), preview({ ...bounds, hide_days: days }).stale) : "Never"}</option>)}
          </select>
          <label htmlFor={ids.minimum}><strong>Hide when seen in fewer than</strong><small>Every problem recurs in at least two work items; raise this to keep only the common ones.</small></label>
          <select id={ids.minimum} disabled={busy} value={bounds.min_work_items} onChange={event => void save({ min_work_items: Number(event.target.value) }, "Saved. Open uses the new bound.")}>
            {[...new Set([...minimumOptions, bounds.min_work_items])].sort((a, b) => a - b).map(n => <option key={n} value={n}>{label(plural(n, "work item"), preview({ ...bounds, min_work_items: n }).rare)}</option>)}
          </select>
        </div>
        <p className="fixes-defaults"><button type="button" className="findings-button" disabled={busy || Boolean(isDefault)} onClick={() => void save({ reset: true }, "Restored the defaults.")}>Restore defaults</button>
          <span className="findings-muted">Rank lower after {plural(settings.default_bounds.quiet_days, "day")}, hide after {plural(settings.default_bounds.hide_days, "day")}, hide below {plural(settings.default_bounds.min_work_items, "work item")}.</span></p>
      </section>
      <section className="findings-panel">
        <h2>Fixes and Risky actions</h2>
        <p>Fixes are problems agents keep running into that you can prevent once: a missing tool, a shell surprise, a failing integration or check, a stale doc, a fact agents relearn. They are grouped across work items and ranked by how often they recur, so these bounds apply.</p>
        <p>An agent getting around a safeguard (skipping or weakening a test, working around a restriction, bypassing a hook) is different: one instance can matter, so each raises its own notice in <a href={safetyURL()}>Risky actions</a>, under its safeguard checks, and is never hidden for being rare. Fixes leaves those events out and only says when a problem's conversations hold some. Both share triage, prompts per place and handoffs, but in separate baskets.</p>
        <p>Events about how a task was asked (unclear instructions, assumptions about the request, disagreeing) are left out of both.</p>
      </section>
      <section className="findings-panel">
        <h2>Refresh</h2>
        <p>Grouping runs in the background after the agent events pass and with each full Findings refresh, over the last {settings.window_days} days of agent events. Opening this page never starts it.</p>
        <p className="findings-muted">{settings.built_at ? `Last grouped ${date(settings.built_at)}.` : "Not grouped yet."}</p>
        <button type="button" className="findings-button" disabled={busy} onClick={() => void refresh()}>Refresh Findings now</button>
      </section>
    </>}
  </div>;
}
