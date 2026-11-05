import { postQueryRows } from "./query-transport";
import { useEffect, useState } from "react";
import type { WhereTerm } from "@pythia-software/query-table-core";
import { agentEventKindLabel } from "./agent-events-analysis-catalog";
import { onDatasetRefresh } from "./dataset-refresh";
import { conversationRowURL } from "./usage-row-model";
import "./agent-event-examples.css";
import { encodeViewValue, viewFields } from "./view-contract";

// The reader at the event's message, with Events highlighted.
export function eventReaderURL(row: Record<string, unknown>): string | undefined {
  const href = conversationRowURL(row, row.message_id);
  return href && `${href}${href.includes("?") ? "&" : "?"}reader_events=${encodeViewValue(viewFields.reader_events, true)}`;
}

// The events behind the dashboard's numbers: for the current filters, the
// ones the model is most confident about, each with what it was about, what
// the agent did, and a link to the message in the reader. Unless the filters
// pick one kind, the list takes kinds in turn, so one frequent kind cannot
// fill it.

type EventRow = {
  id: string; family: string; sentence: string; subject: string; response: string; has_subject: boolean | number;
  source: string; occurred_at: string; title?: string; repository_name?: string; provider?: string; probability: number;
  conversation_id: string; workspace_id?: string; message_id: string;
};
const fields = ["id", "family", "sentence", "subject", "response", "has_subject", "source", "occurred_at", "title", "repository_name", "provider", "probability", "conversation_id", "workspace_id", "message_id"];

function useEventRows(where: WhereTerm[], limit: number) {
  const key = JSON.stringify(where);
  const [refresh, setRefresh] = useState(0);
  const [state, setState] = useState<{ key: string; rows: EventRow[]; total: number; error: string; loading: boolean }>({ key: "", rows: [], total: 0, error: "", loading: true });
  useEffect(() => onDatasetRefresh("agent_events", () => setRefresh(value => value + 1)), []);
  useEffect(() => {
    const controller = new AbortController();
    setState(previous => ({ ...previous, loading: true }));
    const request = { select: fields, where: JSON.parse(key), orderBy: [{ field: "probability", dir: "desc" as const }, { field: "occurred_at", dir: "desc" as const }, { field: "id", dir: "asc" as const }], limit, offset: 0 };
    postQueryRows<EventRow>("agent_events",request,controller.signal)
      .then(body => {
        setState({ key, rows: body.rows ?? [], total: Number(body.total) || 0, error: "", loading: false });
      })
      .catch(failure => { if (!controller.signal.aborted) setState({ key, rows: [], total: 0, error: failure instanceof Error ? failure.message : String(failure), loading: false }); });
    return () => controller.abort();
  }, [key, limit, refresh]);
  return state;
}

const when = (value: string) => {
  const at = new Date(value);
  return Number.isFinite(at.getTime()) ? at.toLocaleString(undefined, { weekday: "short", month: "short", day: "numeric", hour: "numeric", minute: "2-digit" }) : "";
};

// Takes the most confident remaining event of each kind in turn.
export function interleaveByKind<Row extends { family: string }>(rows: Row[], count: number): Row[] {
  const queues = new Map<string, Row[]>();
  for (const row of rows) {
    const queue = queues.get(row.family);
    if (queue) queue.push(row);
    else queues.set(row.family, [row]);
  }
  const picked: Row[] = [], lists = [...queues.values()];
  for (let round = 0; picked.length < count && lists.some(list => round < list.length); round++) {
    for (const list of lists) {
      if (round < list.length && picked.length < count) picked.push(list[round]);
    }
  }
  return picked;
}

export function AgentEventExamples({ where, queryURL }: { where: WhereTerm[]; queryURL: string }) {
  const [shown, setShown] = useState(12);
  const fetched = Math.max(240, shown * 4);
  const { rows: candidates, total, error, loading } = useEventRows(where, fetched);
  const rows = interleaveByKind(candidates, shown);
  return <section className="agent-event-examples" aria-busy={loading}>
    <header className="agent-event-examples-header">
      <h3>Events to read</h3>
      <p className="muted">The events the model is most confident about, for the current filters. Select a column, bar or key above to narrow them.</p>
    </header>
    {error ? <p className="query-table-error" role="alert">{error}</p> : null}
    {!error && !loading && !rows.length ? <p className="muted">No events match these filters.</p> : null}
    <ol className="agent-event-list">
      {rows.map(row => {
        const href = eventReaderURL(row);
        const subject = Number(row.has_subject) ? row.subject : "";
        return <li key={row.id} className="agent-event-item">
          <div className="agent-event-kind">{agentEventKindLabel(row.family)}</div>
          <p className="agent-event-sentence">{row.sentence}</p>
          {subject ? <p className="agent-event-context"><span className="agent-event-role">About</span>{subject}</p> : null}
          {row.response && row.response !== row.sentence ? <p className="agent-event-context"><span className="agent-event-role">Response</span>{row.response}</p> : null}
          <p className="agent-event-meta muted">
            {[when(row.occurred_at), row.source === "thinking" ? "Thinking" : "Visible reply", row.repository_name, row.title].filter(Boolean).join(" · ")}
            {href ? <> · <a href={href}>Open in the reader</a></> : null}
          </p>
        </li>;
      })}
    </ol>
    <footer className="agent-event-examples-footer">
      {rows.length < total ? <button type="button" className="secondary" onClick={() => setShown(value => value + 24)} disabled={loading}>Show more</button> : null}
      <a href={queryURL}>All {total.toLocaleString()} events in the query table</a>
    </footer>
  </section>;
}
