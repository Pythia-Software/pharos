import { analyzePages } from "./app-navigation-model";
import React from "react";
import type { QueryState } from "@pythia-software/query-table-core";
import type { QueryTableApi } from "@pythia-software/query-table-react";
import { RecordSearchEditor } from "./record-search-editor";
import { groupedDashboardQuery } from "./usage-dashboard-model";
import { Icon } from "./icons";
import { ResetPage } from "./query-recovery";
import { readUsageWorkspace, usageAnalysisLink, usageDashboardReturn, usageDatasets, usageTopics, usageSearchQuery, usageWorkspaceURL, type UsageDataset, type UsageTopic } from "./usage-workspace-model";
import { NestedPageLayout, PageLink } from "./nested-pages";
import { cardDetailsURL, cardSourceDataset } from "./card-exploration";
import { instantiateRecipe, queryRecipes } from "./curiosity-data";
import { readRollingWindow } from "./query-links";
import "./usage-workspace.css";

export const UsageWorkspaceLink = PageLink;

// Query keeps the complete analysis. Search opens source records explicitly.
export function UsageQueryLinks({ dataset, api }: { dataset: UsageDataset; api: QueryTableApi<Record<string, any>> }) {
  const params = new URLSearchParams(location.search);
  const open = readUsageWorkspace(params).search;
  return <details className="usage-query-search" open={open} onToggle={event => {
    if (event.currentTarget.open === open) return;
    const url = new URL(location.href);
    if (event.currentTarget.open) url.searchParams.set("analyze_search", "1"); else url.searchParams.delete("analyze_search");
    history.replaceState(history.state, "", url); window.dispatchEvent(new Event("pharos:uri-changed"));
  }}><summary><Icon name="search" />Search and filters <span className="muted">Optional</span></summary>
    {open ? <>
      <RecordSearchEditor api={api} dataset={dataset} target={api.query.grouping ? { label: "Groups", field: api.query.grouping.keys[0] ?? api.schema!.fields[0].name } : usageDatasets[dataset]} />
      <p className="usage-workspace-search-note">These conditions filter {api.query.grouping ? "the grouped results" : "the records"} in this query. <UsageWorkspaceLink href="/library?view=conversation">Search all conversation text<Icon name="arrow-right" /></UsageWorkspaceLink> opens Library without these query filters, for phrases, proximity, and transcript context.</p>
    </> : null}
  </details>;
}

export function UsageDashboardLinks({ dataset, query, currentURL, groupedLabel = "Open grouped query", analysisLabel = "Full current analysis" }: { dataset: UsageDataset; query: QueryState; currentURL?: string; groupedLabel?: string; analysisLabel?: string }) {
  const params = new URLSearchParams(location.search);
  const analysis = currentURL ? usageAnalysisLink(dataset, query, currentURL, params) : null;
  return <div className="usage-dashboard-links">
    {query.aggregations?.length || query.grouping ? <UsageWorkspaceLink href={usageWorkspaceURL({ mode: "query", search: false, dataset, query: groupedDashboardQuery(query) }, params)}>{groupedLabel}<Icon name="arrow-right" /></UsageWorkspaceLink> : null}
    <UsageWorkspaceLink href={usageWorkspaceURL({ mode: "query", search: false, dataset, query: usageSearchQuery(query) }, params)}>Open records<Icon name="arrow-right" /></UsageWorkspaceLink>
    <UsageWorkspaceLink href={usageWorkspaceURL({ mode: "query", search: true, dataset, query: usageSearchQuery(query) }, params)}>Search / filter records<Icon name="arrow-right" /></UsageWorkspaceLink>
    {analysis ? <UsageWorkspaceLink href={analysis.href}>{analysisLabel}{analysis.resetsFilters ? " (resets filters)" : ""}<Icon name="arrow-right" /></UsageWorkspaceLink> : null}
  </div>;
}

export function UsageWorkspace({ renderDashboard, renderTable }: { renderDashboard: (topic: UsageTopic) => React.ReactNode; renderTable: (dataset: UsageDataset) => React.ReactNode }) {
  const params = new URLSearchParams(location.search), { mode, dataset, topic } = readUsageWorkspace(params);
  const source = cardSourceDataset(params), details = cardDetailsURL(params);
  const recipe = queryRecipes.find(recipe => recipe.id === params.get("card_id") && recipe.dataset === source);
  const card = recipe && instantiateRecipe(recipe, source && readRollingWindow(params, source)?.days || readUsageWorkspace(params).days);
  const back = usageDashboardReturn(params);
  const title = mode === "dashboards" ? usageTopics[topic].label : `Query · ${usageDatasets[dataset].label}`;
  const pages = analyzePages(params);
  return <NestedPageLayout className="pharos-query-page usage-workspace" contentClassName="usage-workspace-content"
    label="Analyze" pages={pages} current={`${mode}:${mode === "dashboards" ? topic : dataset}`} title={title}
    description={mode === "dashboards" ? usageTopics[topic].description : "Build your own analysis. Change filters, columns, groups, formulas, and metrics. Add search conditions when you need them."}
    actions={<><ResetPage />{mode !== "dashboards" ? <UsageWorkspaceLink href={back.href}>{back.label}<Icon name="arrow-right" /></UsageWorkspaceLink> : null}</>}>
    {mode === "dashboards" ? <div className="usage-dashboard" key={topic}>
      {card && details ? <section className="usage-card-context" aria-label="Card exploration"><div><span className="muted">Your starting point</span><h2>{card.title}</h2><p>{card.description}</p></div><UsageWorkspaceLink href={details}>View card details<Icon name="arrow-right" /></UsageWorkspaceLink></section> : null}
      {renderDashboard(topic)}</div>
      : <div className="usage-workspace-table" key={dataset}>{renderTable(dataset)}</div>}
  </NestedPageLayout>;
}
