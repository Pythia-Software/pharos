import { predicatesOf, type WhereTerm } from "@pythia-software/query-table-core";

// The filters that apply to the messages read as well as to the events: those
// on fields agent_event_messages shares (repository, provider, model, source,
// dates…). A filter on an event's own fields (kind, sentence, probability)
// narrows the events, never the messages. id names an event in one dataset and
// a message in the other, so it never carries over.
export function agentEventReadWhere(where: WhereTerm[], messageFields: Iterable<string>): WhereTerm[] {
  const shared = new Set(messageFields);
  shared.delete("id");
  return where.filter(term => predicatesOf(term).every(clause => shared.has(clause.field)));
}
