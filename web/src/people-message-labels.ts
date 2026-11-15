import type { AnalysisCatalog, AnalysisState, AnalysisView } from './dashboard-analysis-model';
import { shortLabel } from './conversation-labels';

const views: AnalysisView[] = ['trend', 'trend_line', 'trend_bar', 'compare'];
const sources: Record<string, {scope: string; measures: string[]}> = {
  writing_messages: {scope: 'human', measures: ['messages', 'typed_messages']},
  conversation_activity: {scope: 'activity', measures: ['messages']},
  rhythm: {scope: 'habits', measures: ['messages']},
};
export const peopleLabelBreakdowns = (dataset: string) => ({
  message_family: { label: 'Message label · general', fields: ['family'], measures: sources[dataset].measures, views },
  message_label: { label: 'Message label · specific', fields: ['label'], measures: sources[dataset].measures, views },
});

// Pages, saved cards and query returns must agree on the source a chart uses.
export function peopleMessageLabelScope(dataset: string, state: AnalysisState): string | undefined {
  const source = sources[dataset];
  if (!source) return;
  const trend = state.view === undefined || ['trend', 'trend_line', 'trend_bar'].includes(state.view);
  if (!['message_family', 'message_label'].includes(state.split) || !source.measures.includes(state.metric)
    || !trend && (state.view !== 'compare' || state.unit && state.unit !== 'records')) return;
  return source.scope;
}

// Message-count breakdowns read message/label observations. Other People
// metrics retain their original source and grain, including Habits' hours.
export function peopleMessageLabelCatalog(catalog: AnalysisCatalog, state: AnalysisState): AnalysisCatalog {
  const scope = peopleMessageLabelScope(catalog.dataset, state), breakdown = catalog.breakdowns[state.split];
  if (!scope || !breakdown?.measures) return catalog;
  const measures = { ...catalog.measures };
  for (const metric of breakdown.measures) measures[metric] = {
    ...measures[metric], op: 'count', field: 'message_id', value: undefined, expression: undefined,
    additive: true,
    where: [{ field: 'people_scope', op: '=', value: scope }, ...measures[metric].where ?? []],
    note: measures[metric].note + ' Each message counts once under its highest-confidence specific label, or that label’s family at general resolution. Groups add up to the message total. Missing labels remain visible as Uncategorized.',
  };
  return {
    ...catalog, dataset: 'people_message_labels', measures, trend: { ...catalog.trend, allSeries: true },
    records: { label: 'Labeled message', plural: 'Labeled messages', note: 'One message with its highest-confidence accepted specific label or missing-label coverage in this dashboard’s population.' },
    labels: { ...catalog.labels, label: id => id === '?' ? 'Uncategorized' : shortLabel(id), family: id => id === '?' ? 'Uncategorized' : shortLabel(id) },
  };
}
