import React, { useRef, useState } from "react";
import type { WhereTerm } from "@pythia-software/query-table-core";
import type { AnalysisCatalog, AnalysisState } from "./dashboard-analysis-model";
import { collectDashboardCard, dashboardCardDescription, dashboardCardTitle, dashboardCardURL } from "./dashboard-card-model";
import { Icon } from "./icons";
import "./home.css";

export function CreateDashboardCard({ catalog, state, where }: { catalog: AnalysisCatalog; state: AnalysisState; where: WhereTerm[] }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [draft, setDraft] = useState({ url: "", title: "", description: "" });
  const [error, setError] = useState(""), [saved, setSaved] = useState(false);
  return <div className="dashboard-card-actions">
    <button type="button" onClick={() => {
      const url = dashboardCardURL(location.href, catalog, state, where);
      setDraft({ url, title: dashboardCardTitle(catalog, state), description: dashboardCardDescription(url) });
      setError(""); setSaved(false); dialog.current?.showModal();
    }}><Icon name="plus" />Create card</button>
    {saved ? <span role="status">Card added to <a href="/home" onClick={event => { event.preventDefault(); window.pharosNavigate?.("/home"); }}>your Home dashboard</a>.</span> : null}
    <dialog ref={dialog} className="home-editor" aria-labelledby="createDashboardCardTitle"><form onSubmit={event => {
      event.preventDefault();
      try { collectDashboardCard(draft.url, draft.title, draft.description); dialog.current?.close(); setSaved(true); }
      catch (failure) { setError(failure instanceof Error ? failure.message : String(failure)); }
    }}>
      <h2 id="createDashboardCardTitle">Create dashboard card</h2>
      <p className="home-editor-window-note">Add this chart to your Home dashboard with its current filters and time range. Relative ranges keep moving as new data arrives.</p>
      <label>Title<input autoFocus required maxLength={200} value={draft.title} onChange={event => setDraft({ ...draft, title: event.target.value })} /></label>
      <label>Description<textarea maxLength={500} value={draft.description} onChange={event => setDraft({ ...draft, description: event.target.value })} /></label>
      {error ? <p role="alert">{error}</p> : null}
      <div className="home-editor-actions"><button type="button" onClick={() => dialog.current?.close()}>Cancel</button><button type="submit">Create card</button></div>
    </form></dialog>
  </div>;
}
