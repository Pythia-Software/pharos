import { useViewEntry } from "./view-state";
import React, { useState } from "react";
import type { QueryTableApi } from "@pythia-software/query-table-react";
import { Icon } from "./icons";
import { preferences } from "./preferences";
import { QueryChip } from "./query-card";
import { applyQueryIdea, queryIdeaMatches, queryIdeasOpen, queryIdeasPreferenceKey, usageQueryIdeas } from "./usage-query-ideas-model";
import type { UsageDataset } from "./usage-workspace-model";

export function UsageQueryIdeas({ dataset, api }: { dataset: UsageDataset; api: QueryTableApi<Record<string, any>> }) {
  const key = queryIdeasPreferenceKey(dataset);
  const [open, setOpen] = useViewEntry("disclosures",`query:${dataset}:ideas`,queryIdeasOpen(preferences().get<unknown>(key, undefined)),true,{captureInitial:true});
  const ideas = usageQueryIdeas[dataset];
  const sections = [...new Set(ideas.map(idea => idea.section))];
  if (!ideas.length) return null;
  return <details className="usage-query-search usage-query-ideas" open={open} onToggle={event => {
    const next = event.currentTarget.open;
    // The initial native toggle event is not a user preference change.
    if (next === open) return;
    setOpen(next); preferences().set(key, next);
  }}>
    <summary><Icon name="compass" />Query ideas <span className="muted">{ideas.length} starting points</span></summary>
    {sections.map(section => <div className="usage-presets" role="group" aria-label={`${section} query ideas`} key={section}>
      <span className="usage-preset-label">{section}</span>
      {ideas.filter(idea => idea.section === section).map(idea => <QueryChip key={idea.id} title={idea.label} description={idea.title} active={queryIdeaMatches(api.query, idea)} onClick={() => api.setQuery(previous => applyQueryIdea(previous, idea))} />)}
    </div>)}
  </details>;
}
