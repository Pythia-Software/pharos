import { analyzePages } from "./app-navigation-model";
import React from "react";
import type { QueryState } from "@pythia-software/query-table-core";
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

export function UsageDashboardLinks({ dataset, query, currentURL, groupedLabel = "Open grouped query", analysisLabel = "Full current analysis" }: { dataset: UsageDataset; query: QueryState; currentURL?: string; groupedLabel?: string; analysisLabel?: string }) {
  const params = new URLSearchParams(location.search);
  const analysis = currentURL ? usageAnalysisLink(dataset, query, currentURL, params) : null;
  return <div className="usage-dashboard-links">
    {query.aggregations?.length || query.grouping ? <UsageWorkspaceLink href={usageWorkspaceURL({ mode: "query", search: false, dataset, query: groupedDashboardQuery(query) }, params)}>{groupedLabel}<Icon name="arrow-right" /></UsageWorkspaceLink> : null}
    <UsageWorkspaceLink href={usageWorkspaceURL({ mode: "query", search: false, dataset, query: usageSearchQuery(query) }, params)}>Open records<Icon name="arrow-right" /></UsageWorkspaceLink>
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
    description={mode === "dashboards" ? usageTopics[topic].description : "Build your own analysis. Filter record fields with where, or search record text for words and phrases. Group the matches and use having to filter grouped results."}
    actions={<><ResetPage />{mode !== "dashboards" ? <UsageWorkspaceLink href={back.href}>{back.label}<Icon name="arrow-right" /></UsageWorkspaceLink> : null}</>}>
    {mode === "dashboards" ? <div className="usage-dashboard" key={topic}>
      {card && details ? <section className="usage-card-context" aria-label="Card exploration"><div><span className="muted">Your starting point</span><h2>{card.title}</h2><p>{card.description}</p></div><UsageWorkspaceLink href={details}>View card details<Icon name="arrow-right" /></UsageWorkspaceLink></section> : null}
      {renderDashboard(topic)}</div>
      : <div className="usage-workspace-table" key={dataset}>{renderTable(dataset)}</div>}
  </NestedPageLayout>;
}
