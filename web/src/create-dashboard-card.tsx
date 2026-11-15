import { DataReadinessIndicator } from "./data-readiness";
import { cardReadinessSource } from "./data-readiness-card";
import React, { createContext, useContext, useEffect, useRef, useState } from "react";
import type { FieldSchema, WhereTerm } from "@pythia-software/query-table-core";
import type { AnalysisCatalog, AnalysisState } from "./dashboard-analysis-model";
import { DASHBOARD_CARD_ACTION, collectDashboardCard, dashboardCardDescription, dashboardCardGroups, dashboardCardPresentation, dashboardCardTargets, dashboardCardURL, dashboardCardWindow, pageCardURL, type DashboardCardTarget } from "./dashboard-card-model";
import { validCardGroups } from "./card-cover";
import { CardGroupsField } from "./card-groups-field";
import { loadDashboardCardPreview } from "./dashboard-card-preview";
import type { QueryPreview } from "./query-previews";
import { QueryCard } from "./query-card";
import { Icon } from "./icons";
import type { QueryCategory } from "./query-categories";
import "./home.css";

// The dashboard state a card is made from. A dashboard provides it to the
// charts drawn around its catalog chart (Parallelism's ribbon). A page
// without a catalog chart (Thinking) is its own address.
export type DashboardCardSource = { catalog: AnalysisCatalog; state: AnalysisState; where: WhereTerm[]; schema: FieldSchema } | { page: true; schema: FieldSchema };
export const DashboardCardSourceContext = createContext<DashboardCardSource | null>(null);

// One action group for bespoke dashboard results, using the same source and
// title declaration as their saved-card preview. Card creation remains separate.
export function DashboardResultActions({target}:{target:DashboardCardTarget}) {
  const from=useContext(DashboardCardSourceContext);
  if(!from)return null;
  const address="page" in from?pageCardURL(location.href):dashboardCardURL(location.href,from.catalog,from.state,from.where);
  const title=dashboardCardTargets(address,from.schema).find(item=>item.id===target)?.title??"This chart";
  const {dataset}=cardReadinessSource(address);
  return <span className="dashboard-card-actions"><DataReadinessIndicator id={`chart:${target}`} title={title} dataset={dataset} query={"page" in from?Object.fromEntries(new URLSearchParams(location.search)):{state:from.state,where:from.where}} /><CreateDashboardCard target={target}/></span>;
}

type Draft = { url: string; targets: ReturnType<typeof dashboardCardTargets>; target: DashboardCardTarget; title: string; description: string; action: string; category?: QueryCategory; renamed: boolean; groups: string; error: string };

// The dialog is the card: its cover draws the chart the card will show and
// its title, description, category and call to action are edited in place.
// The category follows the chosen chart until the author picks one. When the dashboard's URL
// holds several charts, the dialog chooses among them.
export function CreateDashboardCard({ source, target = "chart" }: { source?: DashboardCardSource; target?: DashboardCardTarget }) {
  const provided = useContext(DashboardCardSourceContext), from = source ?? provided;
  const dialog = useRef<HTMLDialogElement>(null);
  const [draft, setDraft] = useState<Draft | null>(null), [saved, setSaved] = useState(false);
  const [preview, setPreview] = useState<{ key: string; preview?: QueryPreview; error?: string }>({ key: "" });
  // The preview follows a whole count; an unfinished one keeps the last.
  const typed = draft?.groups.trim() ? Number(draft.groups) : undefined, groups = validCardGroups(typed) ? typed : undefined;
  const key = draft ? JSON.stringify([draft.url, draft.target, groups]) : "";
  useEffect(() => {
    if (!draft || !from) return;
    const controller = new AbortController();
    loadDashboardCardPreview(draft.url, from.schema, controller.signal, undefined, Date.now(), draft.target, undefined, undefined, undefined, groups)
      .then(result => { if (!controller.signal.aborted) setPreview({ key, preview: result }); })
      .catch(failure => { if (!controller.signal.aborted) setPreview({ key, error: failure instanceof Error ? failure.message : String(failure) }); });
    return () => controller.abort();
  }, [key]);
  const opened = Boolean(draft);
  useEffect(() => {
    if (!opened || dialog.current?.open) return;
    dialog.current?.showModal();
    dialog.current?.querySelector<HTMLInputElement>(".query-title")?.focus();
  }, [opened]);
  if (!from) return null;
  const start = () => {
    const url = "page" in from ? pageCardURL(location.href) : dashboardCardURL(location.href, from.catalog, from.state, from.where), targets = dashboardCardTargets(url, from.schema);
    const chosen = targets.find(item => item.id === target) ?? targets[0];
    setDraft({ url, targets, target: chosen.id, title: chosen.title, description: dashboardCardDescription(url, chosen.id, from.schema), action: DASHBOARD_CARD_ACTION, renamed: false, groups: "", error: "" });
    setSaved(false);
  };
  const update = (change: Partial<Draft>) => setDraft(current => current && { ...current, ...change });
  // An unedited title or description follows the chosen chart.
  const choose = (id: DashboardCardTarget) => setDraft(current => current && { ...current, target: id, title: current.renamed ? current.title : current.targets.find(item => item.id === id)!.title,
    description: current.description === dashboardCardDescription(current.url, current.target, from.schema, groups) ? dashboardCardDescription(current.url, id, from.schema, groups) : current.description });
  const current: { preview?: QueryPreview; error?: string } = preview.key === key ? preview : {};
  let windowLabel = "Saved", presentation: ReturnType<typeof dashboardCardPresentation> | undefined, countable: ReturnType<typeof dashboardCardGroups>;
  if (draft) try { windowLabel = dashboardCardWindow(draft.url, draft.target); presentation = dashboardCardPresentation(draft.url, draft.target, groups); countable = dashboardCardGroups(draft.url, draft.target); } catch { /* The preview reports the invalid definition. */ }
  return <span className="dashboard-card-actions">

    <button type="button" className="dashboard-card-create" aria-label="Create card" title="Create card" onClick={start}><Icon name="plus" /></button>
    {saved ? <span role="status">Added to <a href="/home" onClick={event => { event.preventDefault(); window.pharosNavigate?.("/home"); }}>Home</a></span> : null}
    <dialog ref={dialog} className="home-editor card-editor" aria-labelledby="createDashboardCardTitle" onClose={() => setDraft(null)}>{draft ? <form onSubmit={event => {
      event.preventDefault();
      try { collectDashboardCard(draft.url, draft.title, draft.description, draft.target, { action: draft.action, category: draft.category }, undefined, countable && draft.groups.trim() ? Number(draft.groups) : undefined); dialog.current?.close(); setSaved(true); }
      catch (failure) { update({ error: failure instanceof Error ? failure.message : String(failure) }); }
    }}>
      <h2 id="createDashboardCardTitle">Create dashboard card</h2>
      {draft.targets.length > 1 ? <fieldset className="dashboard-card-targets"><legend>Chart</legend>
        {draft.targets.map(item => <label key={item.id}><input type="radio" name="dashboardCardTarget" value={item.id} checked={draft.target === item.id} onChange={() => choose(item.id)} />{item.label}</label>)}
      </fieldset> : null}
      <div className="card-editor-stage">
        <QueryCard id="dashboard-card-draft" title={draft.title} description={draft.description} url={draft.url} windowLabel={windowLabel} category={draft.category ?? presentation?.category ?? "usage"} actionLabel={draft.action}
          loadingSpec={presentation?.loading} preview={current.preview} error={current.error} onOpen={() => {}}
          editing={{ onTitle: title => update({ title, renamed: true }), onDescription: description => update({ description }), onAction: action => update({ action }), onCategory: category => update({ category }) }} />
      </div>
      {countable ? <CardGroupsField groups={countable} value={draft.groups} onChange={value => {
        const count = value.trim() ? Number(value) : undefined;
        update({ groups: value, description: draft.description === dashboardCardDescription(draft.url, draft.target, from.schema, groups)
          ? dashboardCardDescription(draft.url, draft.target, from.schema, validCardGroups(count) ? count : undefined) : draft.description });
      }} /> : null}
      <p className="home-editor-window-note">Name the card, describe it and choose its category and call to action in place. It keeps this chart’s current filters and time range; relative ranges keep moving as new data arrives.</p>
      {draft.error ? <p role="alert">{draft.error}</p> : null}
      <div className="home-editor-actions"><button type="button" onClick={() => dialog.current?.close()}>Cancel</button><button type="submit">Create card</button></div>
    </form> : null}</dialog>
  </span>;
}
