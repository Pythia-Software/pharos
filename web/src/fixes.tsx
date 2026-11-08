import { DataReadinessIndicator } from "./data-readiness";
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
import { type FixesView, fixesURL, fixDetailURL, fixesHiddenTableURL, fixesStateLabels } from "./fixes-route";
import {
  fixesBoundsPreview, fixesDashboard, fixesFacts, fixesHiddenNote, fixesHiddenReason, fixesKindsPresent, fixesLeadQuote, fixesPlaces, fixesRange,
  fixesRankExplanations, fixesRankLabels, type FixCard, type FixCount, type FixesBounds, type FixesRank, type FixesState, type FixGuidanceHit, type FixStatus,
} from "./fixes-model";
import { useSelection, type SelectionApi } from "@pythia-software/query-table-react";
import type { QueryState } from "@pythia-software/query-table-core";
import { fixesTableFocus } from "./fixes-route";
import "./fixes.css";

// Optimize → Fixes. The grouping runs in the background (fixes.go); this page
// reads its items, their triage and the prompt baskets, and writes only the
// person's choices.
const api = "/api/findings/fixes";
const changed = "pharos:fixes-changed";
const noun = { one: "fix", many: "fixes" };
const place = { one: "place", many: "places" };

type Overview = {
  items: FixCard[]; summary: Record<FixesState, number>; hidden: { stale: number; rare: number; intended?: number; total: number };
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
// A plain day (2026-11-01) is that day where you are, not UTC midnight.
const date = (value?: string | null) => value ? new Date(/^\d{4}-\d{2}-\d{2}$/.test(value) ? `${value}T12:00:00` : value).toLocaleDateString(undefined, { month: "short", day: "numeric" }) : "";
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

export function FixesPage({ copy, view = "", table, refreshDiscovery }: { copy: (text: string) => Promise<void>; view?: FixesView; table?: React.ReactNode; refreshDiscovery: () => Promise<unknown> }) {
  const dashboard = view === "";
  const [list, setList] = useViewState<FixesState>("fixes_list", "open", dashboard);
  const [where, setWhere] = useViewState<string>("fixes_place", "", dashboard);
  const [kind, setKind] = useViewState<string>("fixes_kind", "", dashboard);
  const [rank, setRank] = useViewState<FixesRank>("fixes_rank", "priority", dashboard);
  const [showHidden, setShowHidden] = useViewState<boolean>("fixes_hidden", false, dashboard);
  const [selected, select] = useViewState<string>("fixes_item", "", view !== "settings");
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
    : view === "table" && !selected ? "Every fix in every state, including those your settings hide from Open, with its kind, place, recurrence and priority. Tick rows to act on several at once; filter, group and chart them like any table."
    : selected ? "One recurring problem, the agents' own words about it, and where the fix probably belongs."
      : "Problems agents keep running into across separate work items, which one change to guidance, the environment, a tool or docs could prevent. Tick cards to copy a prompt for your agent; a problem that recurs after its handoff opens again.";
  const hiddenNote = overview ? fixesHiddenNote(shown.hidden, overview.bounds) : "";
  const stateTips: Record<FixesState, string> = {
    open: "Problems to act on. Your settings hide the quiet and the rare ones.",
    handed_off: "You handed these to an agent. A problem that recurs after its handoff moves back to Open.",
    dismissed: "Problems you closed. Bring one back at any time.",
  };
  return <div className="findings-page fixes-page">
    <div className="view-heading findings-heading"><div><h1>{view === "settings" ? "Fixes settings" : view === "table" && !selected ? "Fixes table" : "Fixes"}</h1><p className="muted">{lede}</p></div>{view !== "settings" && view !== "table" ? <DataReadinessIndicator id="fixes:overview" title="Fixes" dataset="fixes" query={{view,selected}} /> : null}</div>
    {error ? <p className="findings-error" role="alert">{error}</p> : null}
    {notice ? <p className="safety-notice" role="status">{notice}</p> : null}
    {view === "settings" ? <FixesSettings overview={overview} refreshDiscovery={refreshDiscovery} />
      : !overview ? <p className="findings-muted">Loading fixes…</p>
        : selected ? <div className="findings-layout with-cart findings-detail-layout">
            <FixDetailView id={selected} overview={overview} back={() => select("")} backLabel={view === "table" ? "← Fixes table" : "← Fixes"} act={act} onReview={setReview} />
            <FixesCart detail cart={overview.cart} items={items} onReview={next => setReview(next.target)} />
          </div>
          : view === "table" ? null : <>
            {!overview.built_at ? <div className="findings-empty"><strong>No fixes yet.</strong><p>Fixes are grouped from agent events in the background, after the agent events pass has read your conversations. Turn it on in Settings → Background Processing; grouping follows on its own.</p></div> : null}
            <div className="findings-toolbar">
              <div className="findings-tabs" role="tablist" aria-label="Fixes by state">
                {(Object.keys(fixesStateLabels) as FixesState[]).map(state => <button role="tab" key={state} aria-selected={list === state} title={stateTips[state]} data-view-field="fixes_list" onClick={() => setList(state)}>{fixesStateLabels[state]} <span className="findings-count">{shown.counts[state]}</span></button>)}
              </div>
              <OptimizePlaceFilter places={fixesPlaces(items)} value={where} onChange={setWhere} label="Show fixes for" viewField="fixes_place" />
              <FixesKindFilter items={items} value={kind} onChange={setKind} />
              {shown.hidden.total ? <button type="button" className="findings-button fixes-hidden-toggle" data-view-field="fixes_hidden" data-pharos-target="control:fixes_hidden" aria-pressed={showHidden} title={`${hiddenNote} ${showHidden ? "They are shown, marked; click to hide them again." : "Click to show them, marked."}`} onClick={() => setShowHidden(!showHidden)}>
                {showHidden ? "Showing" : "Show"} {shown.hidden.total.toLocaleString()} hidden</button> : null}
              <OptimizeRank value={rank} onChange={setRank} options={fixesRankLabels} explanations={fixesRankExplanations} viewField="fixes_rank" />
            </div>
            <div className="findings-layout with-cart">
              <section className="fixes-cards" aria-label="Recurring problems">
                <OptimizeSelectionBar selection={selection} copy={copy} items={items} api={api} area="fixes" changed={changed} noun={noun} place={place} promptLimit={overview.prompt_limit} />
                {shown.main.length ? <div className="optimize-card-grid">{shown.main.map(item => <FixCardView key={item.id} item={item} selection={selection} bounds={overview.bounds} />)}</div>
                  : <div className="findings-empty"><strong>{where || kind ? "No fixes match these filters." : list === "open" ? "No open fixes." : list === "handed_off" ? "Nothing handed off yet." : "Nothing closed."}</strong>
                    <p>{where || kind ? "Choose Everywhere and All kinds to see every fix in this list." : list === "open" && shown.hidden.total ? "Your settings hide every open problem; show them above, or loosen the bounds." : list === "open" ? "No problem recurred across enough separate work items." : "Fixes you act on appear here."}</p></div>}
                {hiddenNote ? <p className="findings-muted fixes-hidden-note">{hiddenNote} <a href={fixesHiddenTableURL()}>List them in the table</a> · <a href={fixesURL("settings")}>Change these bounds</a></p> : null}
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
    {table ? <div className="safety-table" hidden={view !== "table" || Boolean(selected)}>
      <div className="safety-table-main">{table}</div>
      {view === "table" && !selected && overview ? <FixesCart table cart={overview.cart} items={items} onReview={next => setReview(next.target)} /> : null}
    </div> : null}
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

// A card reads like a Risky actions check: the title, the agents' words, one
// line of chips, where the fix belongs, and one facts line ending in the link.
// Longer explanations are tooltips; the detail page has them in full.
function FixCardView({ item, selection, bounds }: { item: FixCard; selection: SelectionApi; bounds: FixesBounds }) {
  const hidden = fixesHiddenReason(item, bounds);
  const lead = fixesLeadQuote(item);
  const where = `${item.destination_kind}: ${item.destination}`;
  const facts = [...fixesFacts(item), item.safeguards ? plural(item.safeguards, "safeguard workaround") : ""].filter(Boolean);
  return <OptimizeCard ids={[item.id]} selection={selection} className={`fixes-card${hidden ? " hidden-by-bounds" : ""}${item.deprioritized ? " quiet" : ""}`} target={`fix:${item.id}`}
    count={item.work_items} countLabel={plural(item.work_items, "work item")} label={`Select: ${item.title}`}>
    <a className="fixes-card-title" href={fixDetailURL(item.id)}>{item.title}</a>
    {lead ? <blockquote className="fixes-lead" title={lead}>{lead}</blockquote> : null}
    <div className="fixes-chips">
      <span className="findings-chip" title={item.groups > 1 ? `${item.groups} recurring groups with the same key term, kind of fix and destination, shown as one card` : undefined}>{item.category_label}</span>
      <StatusChip item={item} />
      {item.cart ? <span className="findings-chip watching">In a prompt</span> : null}
      {item.intended ? <span className="findings-chip good" title={item.intended_reason ?? ""}>Intended</span>
        : hidden ? <span className="findings-chip" title={hidden}>Hidden by your settings</span> : null}
    </div>
    <div className="fixes-where" title={`${where}${item.scope_reason ? `\n${item.scope_reason}` : ""}`}><strong>{item.destination_kind}</strong> {item.destination}</div>
    {item.guidance_hit ? <div className="fixes-hit" title={item.guidance_hit.text}>Already in {item.guidance_hit.file}:{item.guidance_hit.line}: “{item.guidance_hit.text}”</div> : null}
    <div className="fixes-foot">
      <span className="safety-check-when">{item.terms.slice(0, 2).map(term => <code key={term}>{term}</code>)}{facts.join(" · ")}</span>
      <a className="safety-check-open" href={fixDetailURL(item.id)} aria-label={`Evidence from ${plural(item.events, "event")}`}>Evidence →</a>
    </div>
  </OptimizeCard>;
}

function FixesCart({ cart, items, onReview, table = false, detail = false }: { cart: CartGroup[]; items: readonly FixCard[]; onReview: (group: CartGroup) => void; table?: boolean; detail?: boolean }) {
  const byID = new Map(items.map(item => [item.id, item]));
  const groups = cart.map(group => ({ target: group.target, label: group.label, group,
    entries: group.items.map(entry => ({ key: entry.id, title: entry.title, ids: [entry.id], ticked: entry.ticked ? 1 : 0,
      detail: [byID.get(entry.id)?.category_label, byID.get(entry.id) ? plural(byID.get(entry.id)!.work_items, "work item") : ""].filter(Boolean).join(" · ") })) }));
  return <OptimizeCart className="fixes-cart" title="Fix prompts to copy" groups={groups} api={api} changed={changed} noun={noun} onReview={onReview}
    empty={detail ? "Add fixes to a prompt for their place. The shared setup and each repository get their own prompt." : `Tick ${table ? "rows" : "a card"} to copy a prompt, or add ${table ? "them" : "it"} to a prompt for ${table ? "their place" : "its place"}. The shared setup and each repository get their own prompt.`}
    note="Fixes, risky actions and efficiency use separate prompts. After copying, these fixes leave Open and remain in Handed off; one that recurs opens again." />;
}

function FixDetailView({ id, overview, back, backLabel, act, onReview }: { id: string; overview: Overview; back: () => void; backLabel: string; act: (id: string, body: Record<string, unknown>, done: string) => Promise<void>; onReview: (target: string) => void }) {
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
  const hours = (n: number) => `${n.toLocaleString(undefined, { maximumFractionDigits: 1 })} h`;
  const facts: Array<[string, React.ReactNode]> = detail ? [
    ["Work items", detail.work_items.toLocaleString()],
    ["Events", <>{detail.events.toLocaleString()} <span className="findings-muted">· {plural(detail.conversations, "conversation")}{detail.thinking ? ` · ${detail.thinking.toLocaleString()} in thinking` : ""}</span></>],
    ["Seen", <>{fixesRange(detail.status)}{status && status.state !== "unknown" ? <span className="findings-muted"> · quiet {plural(status.days_quiet, "day")}</span> : null}</>],
    ["Agent time", detail.cost.episodes ? <>{hours(detail.cost.hours)} <span className="findings-muted">· {plural(detail.cost.episodes, "friction episode")}, {detail.cost.unresolved.toLocaleString()} unresolved</span></> : <span className="findings-muted" title="Lessons and facts don't start friction episodes, so no agent time is measured.">None measured</span>],
    ...(detail.groups > 1 ? [["Merged", plural(detail.groups, "recurring group")] as [string, React.ReactNode]] : []),
    ["Repositories", detail.repositories.map(repository => `${repository.key} ${repository.count}`).join(", ")],
    ["Kinds", detail.kinds.map(kind => `${kind.label ?? kind.key} ${kind.count}`).join(", ")],
    ["Models", detail.models.map(model => `${model.key} ${model.count}`).join(", ")],
  ] : [];
  return <div className="fixes-detail-page">
    <button type="button" className="findings-button" data-view-field="fixes_item" onClick={back}>{backLabel}</button>
    {error ? <p className="findings-error" role="alert">{error === "Request failed (404)" || /not found/i.test(error) ? "This problem is no longer in the latest grouping." : error}</p> : null}
    {!detail ? (!error ? <p className="findings-muted">Loading…</p> : null) : <article className="fixes-detail" data-pharos-target={`fix:${detail.id}`}>
      <p className="fixes-detail-meta"><span className="findings-chip">{detail.category_label}</span>{card ? <StatusChip item={card} /> : null}<span className="findings-muted" title={detail.scope_reason}>{scopeLabel(detail)} · {fixesStateLabels[card?.state ?? "open"]}</span></p>
      <h2>{detail.title}</h2>
      {fixesLeadQuote(detail) ? <blockquote className="fixes-lead">{fixesLeadQuote(detail)}</blockquote> : null}
      {detail.intended ? <p className="fixes-intended"><strong>Guidance already covers this (intended).</strong> {detail.intended_reason}</p> : null}
      {hidden && !detail.intended ? <p className="findings-muted">{hidden} It stays here and in its prompt until you change that.</p> : null}
      {card?.handed_at ? <p className="findings-muted">Handed off {date(card.handed_at)}.{card.recurred ? " It happened again since, so it is open again." : ""}</p> : null}
      {card ? <div className="findings-actions">{card.state === "open" ? <>
        {card.cart ? <><button type="button" className="findings-button primary" onClick={() => onReview(card.cart!.target)}>Review prompt for {card.cart.target_label}</button><button type="button" className="findings-button" onClick={() => void act(card.id, { action: "cart_remove" }, "Removed from the prompt.")}>In prompt · Remove</button></>
          : <button type="button" className="findings-button primary" onClick={() => void act(card.id, { action: "cart_add" }, `Added to the prompt for ${card.target_label}.`)}>Add to prompt</button>}
        <button type="button" className="findings-button quiet" onClick={() => void act(card.id, { action: "dismiss" }, "Acknowledged and closed. You can bring it back at any time.")}>Acknowledge and close</button>
      </> : <button type="button" className="findings-button" onClick={() => void act(card.id, { action: "restore" }, "Brought back to Open.")}>Bring back</button>}</div> : null}

      <div className="findings-detail-grid fixes-detail-grid">
        <div className="fixes-detail-main">
          <section className="findings-panel" aria-labelledby="fixWhereTitle">
            <h3 id="fixWhereTitle">Where the fix probably belongs</h3>
            <p><strong>{detail.destination_kind}</strong>: {detail.destination}</p>
            <p>{detail.suggestion}</p>
            <p className="findings-muted">{detail.scope_reason} {detail.category_label}: {detail.category_explanation}</p>
          </section>
          <section className="findings-panel" aria-labelledby="fixSaidTitle">
            <h3 id="fixSaidTitle">What agents said</h3>
            <ul className="fixes-quotes">{detail.quotes.map(quote => <FixQuote key={quote.event_id} quote={quote} />)}</ul>
            {detail.member_count > detail.quotes.length ? <ViewDisclosure viewKey={`fix:${detail.id}:members`} className="fixes-members" summary={<>All {plural(detail.member_count, "event")}{detail.member_count > detail.members.length ? ` · the ${detail.members.length} closest shown` : ""}</>}>
              <ul className="fixes-quotes">{detail.members.map(quote => <FixQuote key={quote.event_id} quote={quote} />)}</ul>
            </ViewDisclosure> : null}
          </section>
        </div>
        <div className="findings-side">
          <section className="findings-panel" aria-labelledby="fixRecursTitle">
            <h3 id="fixRecursTitle">What recurs</h3>
            <dl className="findings-facts">{facts.filter(([, value]) => value !== "").map(([term, value]) => <React.Fragment key={term}><dt>{term}</dt><dd>{value}</dd></React.Fragment>)}</dl>
          </section>
          <section className="findings-panel" aria-labelledby="fixStoppedTitle">
            <h3 id="fixStoppedTitle">Has it stopped?</h3>
            <p>{status?.detail}</p>
            {status?.commits_since?.length ? <p className="findings-muted">Guidance commits since it was last seen: {status.commits_since.map(note => `${note.repository} ${note.sha} (${note.day}) “${note.subject}”`).join("; ")}</p> : null}
            {status?.persisted ? <p className="findings-muted">It recurred {plural(status.persisted.events_after ?? 0, "time")} after {status.persisted.repository} {status.persisted.sha} (“{status.persisted.subject}”, {status.persisted.day}); that guidance change didn't stop it.</p> : null}
          </section>
          <section className="findings-panel" aria-labelledby="fixGuidanceTitle">
            <h3 id="fixGuidanceTitle">Already in the guidance?</h3>
            {detail.guidance_hits.length ? <><ul className="fixes-list">{detail.guidance_hits.map(hit => <li key={`${hit.repository}:${hit.file}:${hit.line}`}><code>{hit.repository && hit.repository !== "(global)" ? `${hit.repository}/` : ""}{hit.file}:{hit.line}</code> names <code>{hit.term}</code>: “{hit.text}”</li>)}</ul>
              <p className="findings-muted">The guidance already mentions it and agents still run into it, so the fix may be in the environment or in enforcing it, not more words.</p></>
              : <p className="findings-muted">No current guidance file names its key terms{detail.terms.length ? <> ({detail.terms.map(term => <code key={term}>{term}</code>)})</> : null}.</p>}
          </section>
          {detail.tool_errors.length || detail.files.length || detail.safeguards ? <section className="findings-panel" aria-labelledby="fixNamedTitle">
            <h3 id="fixNamedTitle">Around it</h3>
            {detail.tool_errors.length ? <><p className="findings-muted">Failed calls just before</p><ul className="fixes-list">{detail.tool_errors.map(entry => <li key={entry.key}><code>{entry.key}</code> ×{entry.count}</li>)}</ul></> : null}
            {detail.files.length ? <p className="fixes-named"><span className="findings-muted">Files and commands named</span> {detail.files.map(file => <code key={file}>{file}</code>)}</p> : null}
            {detail.safeguards ? <p className="fixes-safeguards">{plural(detail.safeguards, "event")} in these conversations got around a safeguard; they are notices in <a href={safetyURL()}>Risky actions</a>, where each instance is reviewed on its own.</p> : null}
          </section> : null}
        </div>
      </div>
    </article>}
  </div>;
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
type Settings = { bounds: FixesBounds; default_bounds: FixesBounds; open: number; hidden_stale: number; hidden_rare: number; hidden_intended?: number; built_at: string | null; end: string | null; window_days: number };
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
        <p className="findings-muted">{settings.open ? `${plural(settings.open, "open problem")}: ${(settings.open - settings.hidden_stale - settings.hidden_rare - (settings.hidden_intended ?? 0)).toLocaleString()} shown, ${settings.hidden_stale.toLocaleString()} hidden as quiet, ${settings.hidden_rare.toLocaleString()} hidden as rare${settings.hidden_intended ? `, ${settings.hidden_intended.toLocaleString()} left out as intended` : ""}.` : "No open problems yet."}{settings.end ? ` "Quiet" counts days before ${date(settings.end)}, the latest day the agent events pass read.` : ""}</p>
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
      <section className="findings-panel" data-pharos-target="section:fixes-ranks">
        <h2>How Rank by orders the dashboard</h2>
        <dl className="findings-facts">{(Object.keys(fixesRankLabels) as FixesRank[]).map(rank => <React.Fragment key={rank}><dt>{fixesRankLabels[rank]}</dt><dd>{fixesRankExplanations[rank]}</dd></React.Fragment>)}</dl>
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

// Actions on the table's selected fixes (optimize-shared.tsx).
export function FixesSelectionBar({ selection, copy }: { selection: SelectionApi; copy: (text: string) => Promise<void> }) {
  const { overview } = useFixesOverview("table");
  return <OptimizeSelectionBar selection={selection} copy={copy} items={overview?.items ?? null} api={api} area="fixes" changed={changed} noun={noun} place={place} promptLimit={overview?.prompt_limit ?? 12} />;
}

// What a table limited to hidden fixes, or one state, holds.
export function FixesTableContext({ query, total }: { query: QueryState; total: number | null | undefined }) {
  const focus = fixesTableFocus(query);
  if (focus.shown !== "hidden") return null;
  return <section className="findings-panel safety-check-context" data-pharos-target="section:fixes-hidden">
    <p className="findings-muted">Fixes · Open · hidden by your settings</p>
    <h2>Fixes your settings hide from Open</h2>
    <p>{typeof total === "number" ? `${plural(total, "fix", "fixes")}. ` : ""}Each is quiet longer than your bound, seen in fewer work items than your minimum, or guidance working as intended. The dashboard leaves them out until they recur or you change the bounds; you can still hand them off or close them here.</p>
    <a href={fixesURL("settings")}>Fixes settings</a>
  </section>;
}
