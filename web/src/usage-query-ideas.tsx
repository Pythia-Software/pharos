import { ViewDisclosure } from "./view-disclosure";
import React from "react";
import { loadSchema } from "@pythia-software/query-table-core";
import type { QueryTableApi } from "@pythia-software/query-table-react";
import { Icon } from "./icons";
import { preferences } from "./preferences";
import { emptyHostState } from "./host-query-state";
import { fittedDefaultQuery } from "./usage-query-columns";
import { schemaDocuments } from "./schema-documents";
import { applyQueryIdea, queryIdeaMatches, queryIdeasOpen, queryIdeasPreferenceKey, usageQueryIdeas, type QueryIdea } from "./usage-query-ideas-model";
import { usageDatasets, type UsageDataset } from "./usage-workspace-model";

export function UsageQueryIdeas({ dataset, api }: { dataset: UsageDataset; api: QueryTableApi<Record<string, any>> }) {
  const key = queryIdeasPreferenceKey(dataset);
  const ideas = usageQueryIdeas[dataset];
  const sections = [...new Set(ideas.map(idea => idea.section))];
  const choose = (query: typeof api.query) => {
    // A starting point owns the complete query, including host keyword clauses.
    if (api.setDefinition) api.setDefinition({ query, hostState: query.hostState ?? emptyHostState() });
    else api.setQuery(query);
  };
  const renderIdea = (idea: QueryIdea) => <button type="button" className="usage-idea" key={idea.id} aria-pressed={queryIdeaMatches(api.query, idea)} onClick={() => choose(applyQueryIdea(api.query, idea))}>
    <span className="usage-idea-heading"><strong>{idea.label}</strong><Icon name="arrow-right" /></span><span className="usage-idea-description">{idea.title}</span>
  </button>;
  return <ViewDisclosure viewKey={`query:${dataset}:ideas`} className="usage-query-search usage-query-ideas" defaultOpen={queryIdeasOpen(preferences().get<unknown>(key, undefined))} captureInitial onOpenChange={open => preferences().set(key, open)} summary={<><Icon name="compass" />Query ideas <span className="muted">{ideas.length} ways to explore</span></>}>
    <div className="usage-ideas-intro"><p>Explore {usageDatasets[dataset].label.toLowerCase()} from a different angle. Each idea sets its own filters, columns and grouping. Everything remains editable.</p>
      <button type="button" className="usage-ideas-records" onClick={() => choose(fittedDefaultQuery(loadSchema(schemaDocuments[dataset])))}><Icon name="read" />All records<Icon name="arrow-right" /></button>
    </div>
    {sections.map((section, index) => {
      const entries = ideas.filter(idea => idea.section === section), shown = index === 0 ? entries.slice(0, 4) : entries, more = entries.slice(shown.length);
      return <ViewDisclosure key={section} viewKey={`query:${dataset}:ideas:${section}`} className="usage-ideas-section" defaultOpen={index === 0} summary={<>{section}<span className="muted">{entries.length}</span></>}>
        <div className="usage-ideas-grid">{shown.map(renderIdea)}</div>
        {more.length ? <ViewDisclosure viewKey={`query:${dataset}:ideas:${section}:more`} className="usage-ideas-more" summary={`${more.length} more ${section.toLowerCase()} ideas`}><div className="usage-ideas-grid">{more.map(renderIdea)}</div></ViewDisclosure> : null}
      </ViewDisclosure>;
    })}
  </ViewDisclosure>;
}
