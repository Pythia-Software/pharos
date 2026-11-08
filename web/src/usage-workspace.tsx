import { dataSources } from "./data-taxonomy-model";
import { analyzePages } from "./app-navigation-model";
import React, { createContext, useContext, useEffect, useState } from "react";
import {EMPTY_QUERY,encodeQuery,type FieldSchema,type QueryState } from "@pythia-software/query-table-core";
import { dashboardQueryLinks } from "./dashboard-query-links";
import { Icon } from "./icons";
import { ResetPage } from "./query-recovery";
import { readUsageWorkspace, usageDashboardReturn, usageDatasets, usageTopics, type UsageDataset, type UsageTopic } from "./usage-workspace-model";
import { NestedPageLayout, PageLink, useLocationRevision } from "./nested-pages";
import { resolveCardDashboardURL } from "./saved-card-model";
import "./usage-workspace.css";

export const UsageWorkspaceLink = PageLink;

// A dashboard's main chart titles the page: its title takes the heading's
// place, while the dashboard's name stays the page's h1.
const DashboardHeadline = createContext<HTMLElement | null>(null);
export const useDashboardHeadline = () => useContext(DashboardHeadline);

export function UsageDashboardLinks({ dataset, query, schema }: { dataset: UsageDataset; query: QueryState; schema: FieldSchema }) {
  const params = new URLSearchParams(location.search);
  return <div className="usage-dashboard-links">
    <div className="usage-query-destinations">{dashboardQueryLinks(dataset, query, schema, params).map(link => <UsageWorkspaceLink className="usage-query-destination" key={link.title} href={link.href}>
      <span className="usage-query-destination-heading"><strong>{link.title}</strong><Icon name="arrow-right" /></span>
      {link.sentence.length ? <span className="usage-query-sentence">{link.sentence.map(part => <React.Fragment key={part.keyword}>{part.prefix}<strong>{part.keyword}</strong>{part.text ? ` ${part.text}` : ""}</React.Fragment>)}.</span> : null}
    </UsageWorkspaceLink>)}</div>
  </div>;
}

// New records arrive while a dashboard is open (Pharos MCP's own requests,
// new usage); Refresh reruns its queries.
export function UsageWorkspace({ renderDashboard, renderTable }: { renderDashboard: (topic: UsageTopic) => React.ReactNode; renderTable: (dataset: UsageDataset) => React.ReactNode }) {
  useLocationRevision();
  const address = location.href;
  const params = new URLSearchParams(location.search), { mode, dataset, topic } = readUsageWorkspace(params);
  const restoreCard = mode === "dashboards" && params.has("card_id");
  const [failure, setFailure] = useState<{ address: string; message: string }>();
  const [attempt, setAttempt] = useState(0);
  const [headline, setHeadline] = useState<HTMLDivElement | null>(null);
  useEffect(() => {
    if (!restoreCard) return;
    const controller = new AbortController();
    setFailure(undefined);
    void resolveCardDashboardURL(address, controller.signal).then(url => {
      if (controller.signal.aborted || location.href !== address) return;
      history.replaceState(history.state, "", url);
      window.dispatchEvent(new Event("pharos:uri-changed"));
    }).catch(error => {
      if (!controller.signal.aborted && location.href === address) setFailure({ address, message: error instanceof Error ? error.message : String(error) });
    });
    return () => controller.abort();
  }, [address, restoreCard, attempt]);
  // Do not mount a dashboard (or issue its queries) using an obsolete card
  // mapping while its retained source is still being restored.
  if (restoreCard) return <section className="query-recovery" role={failure?.address === address ? "alert" : "status"}>
    {failure?.address === address ? <><span>{failure.message}</span><button type="button" onClick={() => setAttempt(value => value + 1)}>Try again</button><ResetPage /></> : <span>Opening dashboard…</span>}
  </section>;
  const back = usageDashboardReturn(params);
  const title = mode === "dashboards" ? usageTopics[topic].label : usageDatasets[dataset].label;
  const pages = analyzePages(params);
  return <NestedPageLayout className="pharos-query-page usage-workspace" contentClassName="usage-workspace-content"
    label="Analyze" pages={pages} current={`${mode}:${mode === "dashboards" ? topic : dataset}`} title={title} titleSlot={setHeadline}
    description={mode === "dashboards" ? "" : `${dataSources[dataset].grain}. Choose a query idea, or explore the records with your own filters and grouping.`}
    actions={<><ResetPage />{mode==='dashboards'&&['human','thinking-detail'].includes(topic)?<UsageWorkspaceLink href={'/analyze?analyze_mode=dashboards&analyze_topic=labels&analyze_data=message_labels&analyze_filters='+encodeURIComponent(encodeQuery({...EMPTY_QUERY,where:[{field:'source',op:'=',value:topic==='human'?'human':'thinking'}]}))}>Explore {topic==='human'?'human words':'thinking'} labels<Icon name="arrow-right"/></UsageWorkspaceLink>:null}{mode === "dashboards" ? <button type="button" className="usage-dashboard-refresh" onClick={() => window.dispatchEvent(new Event("pharos:usage-refresh"))}>Refresh</button> : null}{mode !== "dashboards" && (params.has("analyze_opened") || params.has("analyze_chart")) ? <UsageWorkspaceLink href={back.href}>{back.label}<Icon name="arrow-right" /></UsageWorkspaceLink> : null}</>}>
    {mode === "dashboards" ? <div className="usage-dashboard" key={topic}>
      <DashboardHeadline.Provider value={headline}>{renderDashboard(topic)}</DashboardHeadline.Provider></div>
      : <div className="usage-workspace-table" key={dataset}>{renderTable(dataset)}</div>}
  </NestedPageLayout>;
}
