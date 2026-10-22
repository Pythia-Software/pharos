import { analyzePages } from "./app-navigation-model";
import React from "react";
import type { FieldSchema, QueryState } from "@pythia-software/query-table-core";
import { dashboardQueryLinks } from "./dashboard-query-links";
import { Icon } from "./icons";
import { ResetPage } from "./query-recovery";
import { readUsageWorkspace, usageAnalysisLink, usageDashboardReturn, usageDatasets, usageTopics, type UsageDataset, type UsageTopic } from "./usage-workspace-model";
import { NestedPageLayout, PageLink } from "./nested-pages";
import { cardDetailsURL, cardSourceDataset } from "./card-exploration";
import { instantiateRecipe, queryRecipes } from "./curiosity-data";
import { readRollingWindow } from "./query-links";
import "./usage-workspace.css";

export const UsageWorkspaceLink = PageLink;

export function UsageDashboardLinks({ dataset, query, schema, currentURL }: { dataset: UsageDataset; query: QueryState; schema: FieldSchema; currentURL?: string }) {
  const params = new URLSearchParams(location.search);
  const analysis = currentURL ? usageAnalysisLink(dataset, query, currentURL, params) : null;
  return <div className="usage-dashboard-links">
    <div className="usage-query-destinations">{dashboardQueryLinks(dataset, query, schema, params).map(link => <UsageWorkspaceLink className="usage-query-destination" key={link.title} href={link.href}>
      <span className="usage-query-destination-heading"><strong>{link.title}</strong><Icon name="arrow-right" /></span>
      {link.sentence.length ? <span className="usage-query-sentence">{link.sentence.map(part => <React.Fragment key={part.keyword}>{part.prefix}<strong>{part.keyword}</strong>{part.text ? ` ${part.text}` : ""}</React.Fragment>)}.</span> : null}
    </UsageWorkspaceLink>)}</div>
    {analysis ? <UsageWorkspaceLink className="usage-legacy-link" href={analysis.href}>Legacy view{analysis.resetsFilters ? " (resets filters)" : ""}<Icon name="arrow-right" /></UsageWorkspaceLink> : null}
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
