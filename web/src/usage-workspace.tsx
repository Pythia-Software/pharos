import { analyzePages } from "./app-navigation-model";
import React, { useEffect, useState } from "react";
import type { FieldSchema, QueryState } from "@pythia-software/query-table-core";
import { dashboardQueryLinks } from "./dashboard-query-links";
import { Icon } from "./icons";
import { ResetPage } from "./query-recovery";
import { readUsageWorkspace, usageAnalysisLink, usageDashboardReturn, usageDatasets, usageTopics, type UsageDataset, type UsageTopic } from "./usage-workspace-model";
import { NestedPageLayout, PageLink, useLocationRevision } from "./nested-pages";
import { resolveCardDashboardURL } from "./saved-card-model";
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
  useLocationRevision();
  const address = location.href;
  const params = new URLSearchParams(location.search), { mode, dataset, topic } = readUsageWorkspace(params);
  const restoreCard = mode === "dashboards" && params.has("card_id");
  const [failure, setFailure] = useState<{ address: string; message: string }>();
  const [attempt, setAttempt] = useState(0);
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
  const title = mode === "dashboards" ? usageTopics[topic].label : `Query · ${usageDatasets[dataset].label}`;
  const pages = analyzePages(params);
  return <NestedPageLayout className="pharos-query-page usage-workspace" contentClassName="usage-workspace-content"
    label="Analyze" pages={pages} current={`${mode}:${mode === "dashboards" ? topic : dataset}`} title={title}
    description={mode === "dashboards" ? usageTopics[topic].description : "Build your own analysis. Filter record fields with where, or search record text for words and phrases. Group the matches and use having to filter grouped results."}
    actions={<><ResetPage />{mode !== "dashboards" ? <UsageWorkspaceLink href={back.href}>{back.label}<Icon name="arrow-right" /></UsageWorkspaceLink> : null}</>}>
    {mode === "dashboards" ? <div className="usage-dashboard" key={topic}>
      {renderDashboard(topic)}</div>
      : <div className="usage-workspace-table" key={dataset}>{renderTable(dataset)}</div>}
  </NestedPageLayout>;
}
