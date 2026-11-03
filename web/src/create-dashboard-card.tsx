import React, { createContext, useContext, useEffect, useRef, useState } from "react";
import type { FieldSchema, WhereTerm } from "@pythia-software/query-table-core";
import type { AnalysisCatalog, AnalysisState } from "./dashboard-analysis-model";
import { collectDashboardCard, dashboardCardDescription, dashboardCardPresentation, dashboardCardTargets, dashboardCardURL, dashboardCardWindow, type DashboardCardTarget } from "./dashboard-card-model";
import { loadDashboardCardPreview } from "./dashboard-card-preview";
import type { QueryPreview } from "./query-previews";
import { QueryCard } from "./query-card";
import { Icon } from "./icons";
import "./home.css";

// The dashboard state a card is made from. A dashboard provides it to the
// charts drawn around its catalog chart (Parallelism's ribbon).
export type DashboardCardSource = { catalog: AnalysisCatalog; state: AnalysisState; where: WhereTerm[]; schema: FieldSchema };
export const DashboardCardSourceContext = createContext<DashboardCardSource | null>(null);

type Draft = { url: string; targets: ReturnType<typeof dashboardCardTargets>; target: DashboardCardTarget; title: string; description: string; renamed: boolean; error: string };

// The dialog is the card: its cover draws the chart the card will show and
// its title and description are edited in place. When the dashboard's URL
// holds several charts, the dialog chooses among them.
export function CreateDashboardCard({ source, target = "chart" }: { source?: DashboardCardSource; target?: DashboardCardTarget }) {
  const provided = useContext(DashboardCardSourceContext), from = source ?? provided;
  const dialog = useRef<HTMLDialogElement>(null);
  const [draft, setDraft] = useState<Draft | null>(null), [saved, setSaved] = useState(false);
  const [preview, setPreview] = useState<{ key: string; preview?: QueryPreview; error?: string }>({ key: "" });
  const key = draft ? JSON.stringify([draft.url, draft.target]) : "";
  useEffect(() => {
    if (!draft || !from) return;
    const controller = new AbortController();
    loadDashboardCardPreview(draft.url, from.schema, controller.signal, undefined, Date.now(), draft.target)
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
    const url = dashboardCardURL(location.href, from.catalog, from.state, from.where), targets = dashboardCardTargets(url);
    const chosen = targets.find(item => item.id === target) ?? targets[0];
    setDraft({ url, targets, target: chosen.id, title: chosen.title, description: dashboardCardDescription(url, chosen.id), renamed: false, error: "" });
    setSaved(false);
  };
  const update = (change: Partial<Draft>) => setDraft(current => current && { ...current, ...change });
  // An unedited title or description follows the chosen chart.
  const choose = (id: DashboardCardTarget) => setDraft(current => current && { ...current, target: id, title: current.renamed ? current.title : current.targets.find(item => item.id === id)!.title,
    description: current.description === dashboardCardDescription(current.url, current.target) ? dashboardCardDescription(current.url, id) : current.description });
  const current: { preview?: QueryPreview; error?: string } = preview.key === key ? preview : {};
  let windowLabel = "Saved", presentation: ReturnType<typeof dashboardCardPresentation> | undefined;
  if (draft) try { windowLabel = dashboardCardWindow(draft.url, draft.target); presentation = dashboardCardPresentation(draft.url, draft.target); } catch { /* The preview reports the invalid definition. */ }
  return <span className="dashboard-card-actions">
    <button type="button" className="dashboard-card-create" aria-label="Create card" title="Create card" onClick={start}><Icon name="plus" /></button>
    {saved ? <span role="status">Added to <a href="/home" onClick={event => { event.preventDefault(); window.pharosNavigate?.("/home"); }}>Home</a></span> : null}
    <dialog ref={dialog} className="home-editor dashboard-card-editor" aria-labelledby="createDashboardCardTitle" onClose={() => setDraft(null)}>{draft ? <form onSubmit={event => {
      event.preventDefault();
      try { collectDashboardCard(draft.url, draft.title, draft.description, draft.target); dialog.current?.close(); setSaved(true); }
      catch (failure) { update({ error: failure instanceof Error ? failure.message : String(failure) }); }
    }}>
      <h2 id="createDashboardCardTitle">Create dashboard card</h2>
      {draft.targets.length > 1 ? <fieldset className="dashboard-card-targets"><legend>Chart</legend>
        {draft.targets.map(item => <label key={item.id}><input type="radio" name="dashboardCardTarget" value={item.id} checked={draft.target === item.id} onChange={() => choose(item.id)} />{item.label}</label>)}
      </fieldset> : null}
      <div className="dashboard-card-stage">
        <QueryCard id="dashboard-card-draft" title={draft.title} description={draft.description} url={draft.url} windowLabel={windowLabel} category={presentation?.category ?? "usage"}
          loadingSpec={presentation?.loading} preview={current.preview} error={current.error} onOpen={() => {}}
          editing={{ onTitle: title => update({ title, renamed: true }), onDescription: description => update({ description }) }} />
      </div>
      <p className="home-editor-window-note">Name the card and describe it in place. It keeps this chart’s current filters and time range; relative ranges keep moving as new data arrives.</p>
      {draft.error ? <p role="alert">{draft.error}</p> : null}
      <div className="home-editor-actions"><button type="button" onClick={() => dialog.current?.close()}>Cancel</button><button type="submit">Create card</button></div>
    </form> : null}</dialog>
  </span>;
}
