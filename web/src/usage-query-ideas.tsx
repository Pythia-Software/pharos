import { ViewDisclosure } from "./view-disclosure";
import React from "react";
import type { QueryTableApi } from "@pythia-software/query-table-react";
import { Icon } from "./icons";
import { preferences } from "./preferences";
import { QueryChip } from "./query-card";
import { applyQueryIdea, queryIdeaMatches, queryIdeasOpen, queryIdeasPreferenceKey, usageQueryIdeas } from "./usage-query-ideas-model";
import type { UsageDataset } from "./usage-workspace-model";

export function UsageQueryIdeas({ dataset, api }: { dataset: UsageDataset; api: QueryTableApi<Record<string, any>> }) {
  const key = queryIdeasPreferenceKey(dataset);
  const ideas = usageQueryIdeas[dataset];
  const sections = [...new Set(ideas.map(idea => idea.section))];
  if (!ideas.length) return null;
  return <ViewDisclosure viewKey={`query:${dataset}:ideas`} className="usage-query-search usage-query-ideas" defaultOpen={queryIdeasOpen(preferences().get<unknown>(key, undefined))} captureInitial onOpenChange={open => preferences().set(key, open)} summary={<><Icon name="compass" />Query ideas <span className="muted">{ideas.length} starting points</span></>}>
    {sections.map(section => <div className="usage-presets" role="group" aria-label={`${section} query ideas`} key={section}>
      <span className="usage-preset-label">{section}</span>
      {ideas.filter(idea => idea.section === section).map(idea => <QueryChip key={idea.id} title={idea.label} description={idea.title} active={queryIdeaMatches(api.query, idea)} onClick={() => api.setQuery(previous => applyQueryIdea(previous, idea))} />)}
    </div>)}
  </ViewDisclosure>;
}
