import React, { FormEvent, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { EMPTY_QUERY, applyAggregations, decodeQuery, encodeQuery, isOrGroup, loadSchema, predicatesOf, memoryStorageAdapter, toAggregationQuery, toServerQuery, type AggregationClause, type FieldSchema, type OrderByClause, type ServerQuery, type Transport, type WhereClause, type WhereTerm } from "@pythia-software/query-table-core";
import { useQueryTable, type QueryTableApi } from "@pythia-software/query-table-react";
import { DataTable, FilterValueProvider, MetricsPanel, QueryBuilder, SelectionToolbar, defaultRenderers, type CellContext, type FilterValuePresentation, type RenderRegistry } from "@pythia-software/query-table-ui";
import "@pythia-software/query-table-ui/theme.css";
import "./pharos.css";
import { libraryStorageAdapter } from "./storage";
import { preferences } from "./preferences";
import libraryDocument from "../../schemas/library.schema.json";
import usageDocument from "../../schemas/usage.schema.json";
import writingDocument from "../../schemas/writing.schema.json";
import writingMessagesDocument from "../../schemas/writing_messages.schema.json";
import mcpCallsDocument from "../../schemas/mcp_calls.schema.json";
import toolsDocument from "../../schemas/tools.schema.json";
import toolCallsDocument from "../../schemas/tool_calls.schema.json";
import tl1AttemptsDocument from "../../schemas/tl1_attempts.schema.json";
import { TL1Page } from "./tl1";
import { FindingsPage, startFindingsChrome } from "./findings";
import { Icon } from "./icons";
import { AuthoredMessages, CategoryPill, MessageMix, authoredRenderers } from "./authored";

type Row = Record<string, any>;
type Dataset = "library" | "usage" | "writing" | "writing_messages" | "mcp_calls" | "tools" | "tool_calls" | "tl1_attempts";
type LibraryView = "table" | "conversation";
type MessageView = "table" | "text";
type TranscriptMessage = { role: string; text: unknown; raw_text?: unknown };
type TurnSummary = { human: TranscriptMessage | null; response: TranscriptMessage | null };
type ConversationTurn = { provider: string; conversation: number; turn: number; input: TranscriptMessage | null; response: TranscriptMessage | null };
type PullRequest = { number: number; title?: string | null; url?: string | null; host?: string | null };
// Messages render with the conversation view's own renderer (ui.py), so XML
// chips, Markdown, and Show more behave the same in both places.
function ResultMessage({ label, message, kind }: { label: string; message: TranscriptMessage; kind: "human" | "assistant" }) {
  const body = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const card = window.pharosMessageCard?.(message);
    if (card) body.current?.replaceChildren(card);
    else if (body.current) body.current.textContent = String(message.text ?? "");
  }, [message.role, message.text, message.raw_text]);
  return <div className={`result-message ${kind}`}>
    <div className="result-message-label">{label}</div>
    <div className="result-message-body" ref={body} />
  </div>;
}

function githubPullURL(remote: unknown, number: number): string {
  const match = String(remote ?? "").match(/github\.com(?::|\/)([^/\s]+\/[^/\s]+?)(?:\.git)?$/i);
  return match ? `https://github.com/${match[1]}/pull/${number}` : "";
}

function pullRequests(row: Row): PullRequest[] {
  try {
    const details = typeof row.pr_details === "string" ? JSON.parse(row.pr_details) : row.pr_details;
    return Array.isArray(details) ? details.filter(pr => Number.isInteger(pr?.number) && pr.number > 0) : [];
  } catch { return []; }
}

function pullRequestURL(pr: PullRequest, remote: unknown): string {
  const url = pr.url || (pr.host === "github.com" ? githubPullURL(remote, pr.number) : "");
  return /^https?:\/\//i.test(url ?? "") ? String(url) : "";
}

function mainIntegration(row: Row): React.ReactNode {
  if (!row.main_merge_commit) return "";
  const prefix = row.main_merge_method === "merge" ? "Merged" : "On main";
  const label = `${prefix}: ${row.main_merge_title || String(row.main_merge_commit).slice(0, 12)}`;
  const url = /^https:\/\/github\.com\//i.test(String(row.main_merge_url ?? "")) ? String(row.main_merge_url) : "";
  return url ? <a className="qt-pr-link" href={url} target="_blank" rel="noopener noreferrer" title={String(row.main_merge_commit)} onClick={event => event.stopPropagation()}>{label}</a>
    : <span title={String(row.main_merge_commit)}>{label}</span>;
}

function formatUSD(amount: number): string {
  if (!Number.isFinite(amount)) return "";
  if (Math.abs(amount) >= 1e6) return `$${compactNumber(amount)}`;
  const digits = Math.abs(amount) >= 100 ? 0 : Math.abs(amount) >= 1 ? 2 : 4;
  return amount.toLocaleString(undefined, { style: "currency", currency: "USD", minimumFractionDigits: Math.min(digits, 2), maximumFractionDigits: digits });
}

function usdCost(value: unknown, row: Row): React.ReactNode {
  if (value === null || value === undefined || value === "") return <span className="muted" title="No confirmed API price for this model and date">—</span>;
  const amount = Number(value);
  const text = formatUSD(amount);
  if (row.price_status === "partial") return <span title="Some tokens had no confirmed rate, so this is a lower bound">{text}+</span>;
  if (row.price_status === "assumed") return <span title="Priced by assumption: no published API price, so a sibling model's price is used (see pricing/cost_changes.json)">≈{text}</span>;
  return <span>{text}</span>;
}

// A shared export (see share.go) carries its own rows in place of the service:
// every table runs over them in the browser, and the pages that need the
// service (search, MCP, TL1, Human Words, Carbon) are left out.
type SharedExport = { works: Row[]; datasets: Record<string, Row[]>; tool_calls: Record<string, Row> };
const shared: SharedExport | null = (window as unknown as { pharosShare?: SharedExport | null }).pharosShare ?? null;

const queryParameter = (dataset: Dataset) => `q_${dataset}`;

function updateURI(name: string, value: string) {
  const url = new URL(window.location.href);
  if (value) url.searchParams.set(name, value);
  else url.searchParams.delete(name);
  if (url.href !== window.location.href) {
    history.replaceState(null, "", url);
    window.dispatchEvent(new Event("pharos:uri-changed"));
  }
}

declare global {
  interface Window {
    pharosOpenDetail?: (id: string) => void;
    pharosCopyText?: (text: string) => Promise<void>;
    pharosShareDownload?: (ids: string[]) => Promise<void>;
    pharosApi?: (path: string) => Promise<Row>;
    pharosMessageCard?: (message: TranscriptMessage) => HTMLElement;
    pharosTurnSummaries?: (conversation: Row) => TurnSummary[];
    pharosCarbon?: { refresh: () => Promise<void> };
    pharosQueryTables?: {
      refresh: (dataset: Dataset) => void;
      filterRepository: (repository: string) => void;
      filterModel: (model: string) => void;
    };
  }
}

const schemas: Record<Dataset, FieldSchema<Row>> = {
  library: loadSchema<Row>(libraryDocument),
  usage: loadSchema<Row>(usageDocument),
  writing: loadSchema<Row>(writingDocument),
  writing_messages: loadSchema<Row>(writingMessagesDocument),
  mcp_calls: loadSchema<Row>(mcpCallsDocument),
  tools: loadSchema<Row>(toolsDocument),
  tool_calls: loadSchema<Row>(toolCallsDocument),
  tl1_attempts: loadSchema<Row>(tl1AttemptsDocument),
};

const tableApis = new Map<Dataset, QueryTableApi<Row>>();
let clearLibrarySearch: (() => void) | undefined;

async function responseJSON<T>(response: Response): Promise<T> {
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(body.error || `Request failed with HTTP ${response.status}`);
  return body as T;
}

// extra carries the Library's keyword search to every request, so rows,
// metrics, and filter pickers all follow it. onFind hears how it went.
function makeTransport(dataset: Dataset, extra = "", onFind?: (find: FindSummary | null) => void): Transport<Row> {
  const endpoint = (suffix = "", params?: URLSearchParams) => {
    const query = new URLSearchParams(extra);
    params?.forEach((value, name) => query.set(name, value));
    return `/api/query/${dataset}${suffix}${query.size ? `?${query}` : ""}`;
  };
  return {
    async fetchRows(query: ServerQuery, signal?: AbortSignal) {
      const result = await responseJSON<{ rows: Row[]; total: number; find?: FindSummary }>(await fetch(endpoint(), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(query),
        signal,
      }));
      onFind?.(result.find ?? null);
      return result;
    },
    async fetchDistinctValues(query, signal) {
      const params = new URLSearchParams({ field: query.field, q: query.search });
      if (query.limit) params.set("limit", String(query.limit));
      return responseJSON(await fetch(endpoint("/distinct", params), { signal }));
    },
    async fetchFieldStats(fields, signal) {
      return responseJSON(await fetch(endpoint("/field-stats", new URLSearchParams({ fields: fields.join(",") })), { signal }));
    },
    async fetchAggregations(query, signal) {
      return responseJSON(await fetch(endpoint("/aggregations"), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(query),
        signal,
      }));
    },
  };
}

const renderers: RenderRegistry<Row> = {
  ...defaultRenderers,
  bool_check(context: CellContext<Row>) {
    return context.value ? <Icon name="check" /> : defaultRenderers.bool_check(context);
  },
  number(context: CellContext<Row>) {
    const value = Number(context.value);
    if (/(^token_count$|_tokens$)/.test(context.field.name) && Number.isFinite(value) && Math.abs(value) >= 1e6)
      return <span title={value.toLocaleString()}>{compactNumber(value)}</span>;
    return defaultRenderers.number(context);
  },
  work_title({ value, row }: CellContext<Row>) {
    return <button className="qt-work-link" title={String(value ?? "")} onClick={(event) => {
      event.stopPropagation();
      window.pharosOpenDetail?.(String(row.workspace_id ?? row.id));
    }}>{String(value ?? "Untitled work")}</button>;
  },
  pull_requests({ value, row }: CellContext<Row>) {
    const prs = pullRequests(row);
    if (!prs.length) return String(value ?? "");
    return <span className="qt-pr-list">{prs.map((pr, index) => {
      const label = pr.title?.trim() ? `${pr.title.trim()} · #${pr.number}` : `PR #${pr.number}`;
      const url = pullRequestURL(pr, row.canonical_remote);
      return url ? <a key={`${pr.host}:${pr.number}:${index}`} className="qt-pr-link" href={url} target="_blank" rel="noopener noreferrer" title={label} onClick={event => event.stopPropagation()}>{label}</a>
        : <span key={`${pr.host}:${pr.number}:${index}`} title={label}>{label}</span>;
    })}</span>;
  },
  main_integration({ row }: CellContext<Row>) { return mainIntegration(row); },
  run_state({ value }: CellContext<Row>) {
    const state = String(value ?? "unknown");
    return <span className={`qt-state-pill ${state}`}>{state}</span>;
  },
  usd({ value, row }: CellContext<Row>) { return usdCost(value, row); },
  sync_progress({ value }: CellContext<Row>) {
    const progress = Math.max(0, Math.min(100, Number(value) || 0));
    return <span className="qt-progress"><span className="qt-progress-track"><span className="qt-progress-fill" style={{ width: `${progress}%` }} /></span><span>{Math.round(progress)}%</span></span>;
  },
  mcp_status({ value }: CellContext<Row>) {
    const state = String(value ?? "unknown");
    return <span className={`mcp-call-state ${state}`}>{state}</span>;
  },
  tool_status({ value }: CellContext<Row>) {
    const state = String(value ?? "unknown");
    return <span className={`mcp-call-state ${state}`}>{state.replace("_", " ")}</span>;
  },
  ...authoredRenderers,
};

// Filter chips, pickers, and cell quick filters show each value's schema label
// and, for status fields, the badge its cells use. Queries and URLs keep the
// raw value.
function optionLabels(fields: FieldSchema<Row>["fields"]): Map<string, Map<string, string>> {
  const labels = new Map<string, Map<string, string>>();
  for (const field of fields) {
    const values = field.filter?.values;
    if (values?.source !== "static") continue;
    const options = new Map<string, string>();
    for (const option of values.options) if (typeof option !== "string") options.set(option.value, option.label);
    if (options.size) labels.set(field.name, options);
  }
  return labels;
}

const statusBadges: Record<string, (value: string, label: string) => React.ReactNode> = {
  run_state: (value, label) => <span className={`qt-state-pill ${value}`}>{label}</span>,
  mcp_status: (value, label) => <span className={`mcp-call-state ${value}`}>{label}</span>,
  tool_status: (value, label) => <span className={`mcp-call-state ${value}`}>{label}</span>,
  authorship_category: (value, label) => <CategoryPill category={value} label={label} />,
};

const filterPresentations = Object.fromEntries(Object.entries(schemas).map(([dataset, schema]) => {
  const labels = optionLabels(schema.fields);
  const badges = new Map(schema.fields.flatMap(field => typeof field.render === "string" && statusBadges[field.render] ? [[field.name, statusBadges[field.render]] as const] : []));
  const label = (field: string, value: string) => labels.get(field)?.get(value) ?? value;
  const presentation: FilterValuePresentation = {
    label,
    render: (field, value) => value ? badges.get(field)?.(value, label(field, value)) : undefined,
  };
  return [dataset, presentation];
})) as Record<Dataset, FilterValuePresentation>;

// A failed query clears its rows; say so rather than claiming nothing matched.
const failedMessage = "No results: the query failed (see the error above).";

const emptyMessages: Record<Dataset, string> = {
  library: "No work matches this query.",
  usage: "No token usage matches this query.",
  writing: "No conversations with user messages match this query.",
  writing_messages: "No classified user messages match this query.",
  mcp_calls: "No MCP calls match this query.",
  tools: "No tool use matches this query.",
  tool_calls: "No tool calls match this query.",
  tl1_attempts: "No TL1 runs match this query.",
};

// query-table-ui 0.3.0 formats metric values with a field's renderer only for
// its built-in unit keys (duration_ms, byte_size). Route dollar measures
// through the byte_size key and back to the usd renderer, so metric panels show
// "$1,234" instead of "1234.5678". Real byte_size fields are unaffected.
const usdMetricFields = new WeakSet<object>();
const tokenMetricFields = new WeakSet<object>();
const metricFieldCache = new WeakMap<object, FieldSchema<Row>["fields"]>();
function metricFields(fields: FieldSchema<Row>["fields"]): FieldSchema<Row>["fields"] {
  let mapped = metricFieldCache.get(fields);
  if (!mapped) {
    mapped = fields.map((field) => {
      if (field.render !== "usd" && !(field.render === "number" && /(^token_count$|_tokens$)/.test(field.name))) return field;
      const alias = { ...field, render: "byte_size" };
      if (field.render === "usd") usdMetricFields.add(alias);
      else tokenMetricFields.add(alias);
      return alias;
    });
    metricFieldCache.set(fields, mapped);
  }
  return mapped;
}
const metricRenderers: RenderRegistry<Row> = {
  ...renderers,
  byte_size(context: CellContext<Row>) {
    if (usdMetricFields.has(context.field as object)) return formatUSD(Number(context.value));
    if (tokenMetricFields.has(context.field as object)) return compactNumber(Number(context.value));
    return defaultRenderers.byte_size(context);
  },
};

// header renders between the filters and the metric panels, so a chart there
// can follow the table's filters.
// find, for the Library, is a keyword search that narrows the rows before the
// filters apply; onFind hears how it went.
function QuerySurface({ dataset, libraryView = "table", messageView = "table", find = "", onFind, trailing, header, selectable = false }: { dataset: Dataset; selectable?: boolean; libraryView?: LibraryView; messageView?: MessageView; find?: string; onFind?: (find: FindSummary | null) => void; trailing?: (row: Row, api: QueryTableApi<Row>) => React.ReactNode; header?: (api: QueryTableApi<Row>) => React.ReactNode }) {
  const schema = schemas[dataset];
  const onFindRef = useRef(onFind);
  onFindRef.current = onFind;
  const transport = useMemo(() => shared ? undefined : makeTransport(dataset, find, value => onFindRef.current?.(value)), [dataset, find]);
  // A shared file has no origin of its own to keep saved queries apart from other files'.
  const storage = useMemo(() => shared ? memoryStorageAdapter() : libraryStorageAdapter(), []);
  // A query in the URL (a link, or a drill-down from another table) starts
  // the table, rather than the last one used, which loads after it otherwise.
  const [urlQuery] = useState(() => {
    const token = new URLSearchParams(location.search).get(queryParameter(dataset));
    return token === null ? undefined : decodeQuery(token);
  });
  const api = useQueryTable<Row>({ schema, transport, clientRows: shared?.datasets[dataset], storage, debounceMs: 100, ...(urlQuery ? { initialQuery: urlQuery } : {}) });
  const apiRef = useRef(api);
  apiRef.current = api;
  // A new search starts from its first page.
  const searched = useRef(find);
  useEffect(() => {
    if (searched.current === find) return;
    searched.current = find;
    apiRef.current.setQuery(previous => previous.offset ? { ...previous, offset: 0 } : previous);
  }, [find]);
  // query-table 0.3.0's nullability effect depends on the controller object,
  // while useQueryTable returns a fresh facade as data arrives. A stable proxy
  // prevents an effect/request loop and still resolves every property live.
  const stableApi = useMemo(() => new Proxy({} as QueryTableApi<Row>, {
    get(_target, property) { return apiRef.current[property as keyof QueryTableApi<Row>]; },
  }), []);
  tableApis.set(dataset, api);
  useEffect(() => {
    return () => { tableApis.delete(dataset); };
  }, [dataset]);
  const pendingRouteQuery = useRef<string | null>(null);
  const firstRouteQuery = useRef(true);
  useEffect(() => {
    const restore = () => {
      const token = new URLSearchParams(window.location.search).get(queryParameter(dataset));
      if (token !== null) {
        const restored = decodeQuery(token);
        const canonicalToken = encodeQuery(restored);
        pendingRouteQuery.current = encodeQuery(apiRef.current.query) === canonicalToken ? null : canonicalToken;
        apiRef.current.setQuery(restored);
      } else if (!firstRouteQuery.current) {
        const defaultsToken = encodeQuery(apiRef.current.defaults);
        pendingRouteQuery.current = encodeQuery(apiRef.current.query) === defaultsToken ? null : defaultsToken;
        apiRef.current.setQuery(apiRef.current.defaults);
      }
      firstRouteQuery.current = false;
    };
    restore();
    window.addEventListener("pharos:route", restore);
    return () => window.removeEventListener("pharos:route", restore);
  }, [dataset]);
  const initialQuery = useRef(true);
  useEffect(() => {
    if (initialQuery.current) { initialQuery.current = false; if (pendingRouteQuery.current !== null) return; }
    const token = encodeQuery(api.query);
    if (pendingRouteQuery.current !== null && token !== pendingRouteQuery.current) return;
    pendingRouteQuery.current = null;
    updateURI(queryParameter(dataset), token);
  }, [dataset, api.query]);

  return <FilterValueProvider value={filterPresentations[dataset]}>
    {header?.(api)}
    <QueryBuilder api={stableApi} fields={schema.fields} total={api.total} running={api.loading} />
    <MetricsPanel aggregations={api.aggregations} fields={metricFields(schema.fields)} renderers={metricRenderers} />
    {api.error ? <div className="query-table-error">{api.error.message}</div> : null}
    {selectable ? <SelectionToolbar selection={api.selection} actions={ids => <ShareSelection ids={ids} api={api} transport={transport} />} /> : null}
    {dataset === "library" && libraryView === "conversation" ? <ConversationResults api={api} searching={Boolean(find)} />
      : dataset === "writing_messages" && messageView === "text" ? <AuthoredMessages api={api} emptyMessage={api.error ? failedMessage : emptyMessages[dataset]} /> : <DataTable
      maxHeight={100000}
      {...(selectable ? { selection: api.selection } : {})}
      fields={api.visibleFields}
      rows={api.rows}
      query={api.query}
      onQueryChange={api.setQuery}
      renderers={renderers}
      rowId={api.rowId}
      columnDrag={api.columnDrag}
      total={api.total}
      loading={api.loading}
      emptyMessage={api.error ? failedMessage : find ? "No work matches this search and these filters." : emptyMessages[dataset]}
      {...(trailing ? { trailing: (row: Row) => trailing(row, api), trailingLabel: dataset === "mcp_calls" || dataset === "tool_calls" ? "View" : dataset === "tools" || dataset === "writing" ? "Drill in" : dataset === "library" ? "Match" : "Actions" } : {})}
    />}
  </FilterValueProvider>;
}

// An export holds at most this many works (see shareWorkLimit in share.go).
const shareWorkLimit = 200;

// Share, for the works checked in the Library: one standalone HTML file with
// their conversations and tables. It can also check every work the current
// filters match, when they fit in one export.
function ShareSelection({ ids, api, transport }: { ids: unknown[]; api: QueryTableApi<Row>; transport?: Transport<Row> }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const tooMany = ids.length > shareWorkLimit;
  const total = api.total ?? 0;
  const canSelectAll = Boolean(transport) && total > ids.length && total <= shareWorkLimit;
  async function selectAll() {
    if (!transport) return;
    setBusy(true);
    try {
      const result = await transport.fetchRows({ ...toServerQuery(api.query, schemas.library), limit: shareWorkLimit, offset: 0 });
      api.selection.replace(result.rows.map(row => api.rowId(row)).filter((id): id is NonNullable<typeof id> => id !== null));
    } finally { setBusy(false); }
  }
  return <>
    {canSelectAll ? <button type="button" className="qt-btn" disabled={busy} onClick={() => void selectAll()}>Select all {total.toLocaleString()} results</button> : null}
    <button type="button" className="qt-btn share-selection" disabled={tooMany}
      title={tooMany ? `One export holds at most ${shareWorkLimit} works. Narrow the selection.` : "Download the selected conversations, with tables of their tool use and token usage, as one standalone HTML file to send or publish. It includes full transcripts, tool output, and file paths."}
      onClick={() => { setError(""); void window.pharosShareDownload?.(ids.map(String)).catch((failure: unknown) => setError(failure instanceof Error ? failure.message : "The export failed")); }}><Icon name="arrow-down" /> Share {ids.length.toLocaleString()} conversation{ids.length === 1 ? "" : "s"}</button>
    {error ? <span className="share-error" role="alert">Could not share: {error}</span> : null}
  </>;
}

// The reader's requests, answered from the file itself in a shared export.
function readerJSON(path: string, signal?: AbortSignal): Promise<Row> {
  return shared ? window.pharosApi!(path) : fetch(path, { signal }).then(responseJSON<Row>);
}

function conversationTurns(work: Row): ConversationTurn[] {
  return (work.conversations ?? []).flatMap((conversation: Row, index: number) => {
    const provider = String(conversation.provider ?? "Agent");
    return (window.pharosTurnSummaries?.(conversation) ?? [])
      .filter(summary => summary.human)
      .map((summary, turn) => ({ provider, conversation: index + 1, turn: turn + 1, input: summary.human, response: summary.response }));
  });
}

function ResultConversationBrowser({ workspaceID }: { workspaceID: string }) {
  const [state, setState] = useState<{ loading: boolean; error: string; turns: ConversationTurn[] }>({ loading: true, error: "", turns: [] });
  const [active, setActive] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    void (async () => {
      try {
        const base = `/api/work/${encodeURIComponent(workspaceID)}`;
        const work = await readerJSON(base, controller.signal);
        // The overview omits messages. Sub-agents' assignments are not user
        // turns, so only top-level conversations are fetched.
        const conversations = await Promise.all((work.conversations ?? []).filter((conversation: Row) => !conversation.parent_id)
          .map((conversation: Row) => readerJSON(`${base}/conversations/${encodeURIComponent(String(conversation.id))}`, controller.signal)));
        if (!controller.signal.aborted) setState({ loading: false, error: "", turns: conversationTurns({ conversations }) });
      } catch (error) {
        if (!controller.signal.aborted) setState({ loading: false, error: error instanceof Error ? error.message : "Conversation unavailable", turns: [] });
      }
    })();
    return () => controller.abort();
  }, [workspaceID]);
  if (state.loading) return <div className="conversation-result-status">Loading turns…</div>;
  if (state.error) return <div className="conversation-result-status badtext">{state.error}</div>;
  if (!state.turns.length) return <div className="conversation-result-status">No retained turns are available.</div>;
  const index = Math.min(active, state.turns.length - 1);
  const turn = state.turns[index];
  return <div className="result-turn-browser">
    <div className="result-turn-nav">
      <button type="button" disabled={index === 0} onClick={() => setActive(index - 1)} aria-label="Previous turn"><Icon name="arrow-left" /></button>
      <span>{turn.provider}{state.turns.some((item) => item.conversation !== turn.conversation) ? ` conversation ${turn.conversation}` : ""} · turn {turn.turn} of {state.turns.filter((item) => item.conversation === turn.conversation).length}</span>
      <button type="button" disabled={index === state.turns.length - 1} onClick={() => setActive(index + 1)} aria-label="Next turn"><Icon name="arrow-right" /></button>
    </div>
    <div className="result-exchange">
      <ResultMessage label="You" message={turn.input ?? { role: "user", text: "Input unavailable" }} kind="human" />
      <ResultMessage label={turn.provider} message={turn.response ?? { role: "assistant", text: "No retained response for this turn." }} kind="assistant" />
    </div>
  </div>;
}

function ConversationResultCard({ row }: { row: Row }) {
  const [browse, setBrowse] = useState(false);
  const turnCount = Math.max(0, Number(row.turn_count) || 0);
  const firstInput = row.first_input ? { role: "user", text: row.first_input } : row.purpose ? { role: "user", text: row.purpose } : null;
  const latestResponse = row.last_response ? { role: "assistant", text: row.last_response } : row.outcome ? { role: "assistant", text: row.outcome } : null;
  const toolCount = Math.max(0, Number(row.tool_use_count) || 0);
  const fileCount = Math.max(0, Number(row.changed_file_count) || 0);
  const models = String(row.models ?? "").split(",").map(model => model.trim()).filter(Boolean);
  const prs = pullRequests(row);
  return <article className="conversation-result-card">
    <div className="conversation-result-head">
      <div className="conversation-result-summary">
        <button className="conversation-result-title" type="button" onClick={() => window.pharosOpenDetail?.(String(row.id))}>{String(row.title ?? "Untitled work")}</button>
        <div className="conversation-result-meta">{[row.repository_name, row.source_kind, models.join(", "), row.activity_at ? new Date(row.activity_at).toLocaleDateString() : ""].filter(Boolean).join(" · ")}</div>
        <div className="conversation-result-facts">
          <span className="conversation-result-count">{turnCount} turn{turnCount === 1 ? "" : "s"}</span>
          <span className="conversation-result-count">{toolCount} tool use{toolCount === 1 ? "" : "s"}</span>
          <span className="conversation-result-count">{fileCount} file{fileCount === 1 ? "" : "s"} edited</span>
          <span className="conversation-result-count conversation-result-cost" title="API list-price equivalent cost">{usdCost(row.cost_usd, row)} API cost</span>
          {prs.map((pr, index) => {
            const url = pullRequestURL(pr, row.canonical_remote);
            const label = pr.title?.trim() ? `${pr.title.trim()} · #${pr.number}` : `PR #${pr.number}`;
            return url ? <a key={`${pr.host}:${pr.number}:${index}`} className="conversation-result-count conversation-result-pr" href={url} target="_blank" rel="noopener noreferrer" title={pr.title || label}>{label}</a>
              : <span key={`${pr.host}:${pr.number}:${index}`} className="conversation-result-count" title={pr.title || label}>{label}</span>;
          })}
          {row.main_merge_commit ? <span className="conversation-result-count conversation-result-pr">{mainIntegration(row)}</span> : null}
        </div>
      </div>
      <div className="conversation-result-actions">
        <button type="button" onClick={() => window.pharosOpenDetail?.(String(row.id))}>Open full conversation</button>
        <button type="button" onClick={() => setBrowse((value) => !value)} disabled={!turnCount} aria-expanded={browse}>{browse ? "Close turn browser" : "Browse turns"}</button>
      </div>
    </div>
    {row.find_hit ? <FindMatch hit={row.find_hit} count={Number(row.find_match_count) || 1} /> : null}
    {firstInput || latestResponse ? <div className="conversation-result-preview">
      {firstInput ? <ResultMessage label="First ask" message={firstInput} kind="human" /> : null}
      {latestResponse ? <ResultMessage label="Latest response" message={latestResponse} kind="assistant" /> : null}
    </div> : <p className="muted conversation-result-empty">No retained message preview.</p>}
    {browse ? <ResultConversationBrowser workspaceID={String(row.id)} /> : null}
  </article>;
}

function ConversationResults({ api, searching }: { api: QueryTableApi<Row>; searching: boolean }) {
  if (!api.loading && !api.rows.length) return <div className="conversation-results-empty no-results">{api.error ? failedMessage : searching ? "No work matches this search and these filters." : "No work matches this query."}</div>;
  return <div className={`conversation-results ${api.loading ? "loading" : ""}`} aria-busy={api.loading}>
    {api.loading && !api.rows.length ? <div className="conversation-results-empty">Loading conversations…</div> : null}
    {api.rows.map((row) => <ConversationResultCard key={String(api.rowId(row) ?? row.id)} row={row} />)}
  </div>;
}

type FindKind = "text" | "file" | "url";
type FindHit = { workspace_id: string; conversation_id?: string; title: string; provider?: string; started_at?: string; message_id?: string; role?: string; message_kind?: string; snippet?: string; path?: string; old_path?: string; url?: string; url_source?: string; tool_name?: string; tool_call_id?: string; attribution?: string; count: number };
// What the Library's keyword search found before the table's filters applied.
type FindSummary = { workspaces: number; limited: boolean };

function openFindHit(hit: FindHit) {
  const params = new URLSearchParams();
  if (hit.conversation_id) params.set("conversation", hit.conversation_id);
  if (hit.message_id) params.set("message", hit.message_id);
  history.pushState(null, "", `/work/${encodeURIComponent(hit.workspace_id)}${params.size ? `?${params}` : ""}`);
  (window as unknown as { routeFromLocation?: () => void }).routeFromLocation?.();
}

function findHitLabel(hit: FindHit): string {
  if (hit.url) return `Open tool call${hit.tool_name ? ` · ${hit.tool_name}` : ""}`;
  if (hit.path) return hit.attribution === "workspace" ? "Open work · conversation unknown" : "Open conversation";
  return `Open ${hit.role === "user" ? "user message" : hit.message_kind === "message" ? "agent response" : "agent thought"}`;
}

function findHitText(hit: FindHit): string {
  if (hit.url) return hit.url;
  if (hit.path) return `${hit.old_path ? `${hit.old_path} → ` : ""}${hit.path}`;
  return hit.snippet ?? "";
}

// A result's keyword match: its most relevant hit and how many there were.
function FindMatch({ hit, count }: { hit: FindHit; count: number }) {
  const noun = hit.url ? "matching URL" : hit.path ? "matching file change" : "matching message";
  return <div className="library-find-match">
    <div className="result-message-label">Keyword match · {count.toLocaleString()} {noun}{count === 1 ? "" : "s"}</div>
    <p className="library-find-snippet">{hit.url ? <a href={hit.url} target="_blank" rel="noopener noreferrer">{hit.url}</a> : findHitText(hit)}</p>
    <button type="button" className="library-find-open" onClick={() => openFindHit(hit)}>{findHitLabel(hit)}</button>
  </div>;
}

function FindMatchCell({ row }: { row: Row }) {
  const hit = row.find_hit as FindHit | undefined;
  if (!hit) return null;
  const count = Number(row.find_match_count) || 1;
  return <button type="button" className="library-find-cell" title={`${findHitText(hit)}\n\n${count.toLocaleString()} match${count === 1 ? "" : "es"} · ${findHitLabel(hit)}`} onClick={event => { event.stopPropagation(); openFindHit(hit); }}>Open match{count > 1 ? ` (${count.toLocaleString()})` : ""}</button>;
}

function findParameters(query: string, kind: FindKind, fuzzy: boolean, caseSensitive: boolean, separators: boolean): string {
  if (!query) return "";
  const params = new URLSearchParams({ find: query });
  if (kind !== "text") params.set("find_kind", kind);
  if (kind === "text" && !fuzzy) params.set("find_fuzzy", "0");
  if (kind === "text" && caseSensitive) params.set("find_case", "1");
  if (kind === "text" && separators) params.set("find_separators", "1");
  return params.toString();
}

function LibraryPage() {
  const read = () => new URLSearchParams(location.search);
  const [draft, setDraft] = useState(() => read().get("search") ?? "");
  const [search, setSearch] = useState(() => read().get("search") ?? "");
  const [kind, setKind] = useState<FindKind>(() => (read().get("kind") as FindKind) || "text");
  const [fuzzy, setFuzzy] = useState(() => read().get("fuzzy") !== "0");
  const [caseSensitive, setCaseSensitive] = useState(() => read().get("case") === "1");
  const [separators, setSeparators] = useState(() => read().get("separators") === "1");
  const [view, setView] = useState<LibraryView>(() => read().get("view") === "conversation" ? "conversation" : "table");
  useEffect(() => { clearLibrarySearch = () => { setDraft(""); setSearch(""); updateURI("search", ""); }; return () => { clearLibrarySearch = undefined; }; }, []);
  useEffect(() => { const restore = () => { const params = read(); const q = params.get("search") ?? ""; setDraft(q); setSearch(q); setKind((params.get("kind") as FindKind) || "text"); setFuzzy(params.get("fuzzy") !== "0"); setCaseSensitive(params.get("case") === "1"); setSeparators(params.get("separators") === "1"); setView(params.get("view") === "conversation" ? "conversation" : "table"); }; window.addEventListener("pharos:route", restore); return () => window.removeEventListener("pharos:route", restore); }, []);
  function submit(event: FormEvent) { event.preventDefault(); const next = draft.trim(); setSearch(next); updateURI("search", next); }
  function chooseView(next: LibraryView) { setView(next); updateURI("view", next === "conversation" ? next : ""); }
  function clear() { setDraft(""); setSearch(""); updateURI("search", ""); }
  const find = shared ? "" : findParameters(search, kind, fuzzy, caseSensitive, separators);
  const [found, setFound] = useState<FindSummary | null>(null);
  useEffect(() => { setFound(null); }, [find]);
  return <div className={`pharos-query-page${find ? " library-searching" : ""}`}>
    <div className="view-heading library-heading"><div><h1>{shared ? "Conversations" : "Find past work"}</h1></div><div className="library-view-toggle" role="group" aria-label="Library result view"><button type="button" className={view === "table" ? "active" : ""} aria-pressed={view === "table"} onClick={() => chooseView("table")}>Table</button><button type="button" className={view === "conversation" ? "active" : ""} aria-pressed={view === "conversation"} onClick={() => chooseView("conversation")}>Conversations</button></div></div>
    {shared ? null : <form className="semantic-search" onSubmit={submit}><select aria-label="Search type" value={kind} onChange={event => { const next = event.target.value as FindKind; setKind(next); updateURI("kind", next === "text" ? "" : next); }}><option value="text">Conversation text</option><option value="file">Modified file</option><option value="url">Tool URL</option></select><input type="search" value={draft} onChange={event => setDraft(event.target.value)} placeholder={kind === "text" ? "Words or an exact phrase in quotes…" : kind === "file" ? "File path or name…" : "URL or host…"} aria-label="Search library" /><button type="submit">Search</button>{search ? <button type="button" className="semantic-search-clear" onClick={clear}>Clear</button> : null}</form>}
    {shared ? null : kind === "text" ? <div className="search-options" role="group" aria-label="Text search options"><label><input type="checkbox" checked={fuzzy} onChange={event => { setFuzzy(event.target.checked); updateURI("fuzzy", event.target.checked ? "" : "0"); }} /> Fuzzy words</label><label><input type="checkbox" checked={caseSensitive} onChange={event => { setCaseSensitive(event.target.checked); updateURI("case", event.target.checked ? "1" : ""); }} /> Case sensitive</label><label title="For quoted phrases, require punctuation and spaces exactly as typed"><input type="checkbox" checked={separators} onChange={event => { setSeparators(event.target.checked); updateURI("separators", event.target.checked ? "1" : ""); }} /> Match separators</label></div> : null}
    {search && found ? <p className="library-find-status muted">{found.limited ? "At least " : ""}{found.workspaces.toLocaleString()} result{found.workspaces === 1 ? "" : "s"} match “{search}”; the filters below narrow them further.{found.limited ? " Search reached its 5,000-record limit; narrow the query for complete results." : ""}</p> : null}
    <QuerySurface dataset="library" libraryView={view} find={find} onFind={setFound} selectable={!shared && view === "table"} {...(find ? { trailing: (row: Row) => <FindMatchCell row={row} /> } : {})} />
  </div>;
}

type ChartPeriod = "day" | "week" | "month";
type ChartRange = "30d" | "90d" | "6m" | "1y";
type TokenSplit = "type" | "provider" | "model_family" | "repository_name" | "session_kind";
type TokenChartView = { metric: "tokens" | "cost" | "percent"; split: TokenSplit; period: ChartPeriod };
// A preset either fills the metric panels or sets the chart above them.
type UsagePreset = { label: string; title: string; aggregations?: AggregationClause[]; chart?: Partial<TokenChartView>; orderBy?: OrderByClause[] };
const sum = (field: string, groupBy: string[], label: string): AggregationClause => ({ id: `${field}:${groupBy.join(",")}`, op: "sum", field, groupBy, label });
const costPresets: UsagePreset[] = [
  { label: "Total cost", title: "Total API-equivalent cost, per month, and by provider", aggregations: [
    sum("cost_usd", [], "Total cost"),
    sum("cost_usd", ["month"], "Cost per month"),
    sum("cost_usd", ["provider"], "Cost by provider"),
  ] },
  { label: "Daily cost", title: "Chart API-equivalent cost per day, split by provider", chart: { metric: "cost", split: "provider", period: "day" } },
  { label: "Weekly cost", title: "API-equivalent cost per week, by model and by repository", aggregations: [
    sum("cost_usd", ["week", "model_family"], "Cost per week by model"),
    sum("cost_usd", ["repository_name"], "Cost by repository"),
  ] },
  { label: "Cost by model", title: "Cost by model, with the cost of the same tokens at today's prices", aggregations: [
    sum("cost_usd", ["model_family"], "Cost by model"),
    sum("cost_today_usd", ["model_family"], "Same tokens at today's prices"),
  ] },
  { label: "Most expensive work", title: "Workspaces ranked by API-equivalent cost", aggregations: [sum("cost_usd", ["title"], "Cost by work")] },
  { label: "Price changes", title: "Cost as billed on the day versus the same usage at today's prices, per month", aggregations: [
    sum("cost_usd", ["month"], "Cost at the day's prices"),
    sum("cost_today_usd", ["month"], "Cost at today's prices"),
  ] },
  { label: "Subagent cost", title: "Chart root agent versus subagent cost per week", chart: { metric: "cost", split: "session_kind", period: "week" } },
  { label: "Price coverage", title: "Cost and tokens by price status: priced, assumed, partial, or unpriced", aggregations: [
    sum("cost_usd", ["price_status"], "Cost by price status"),
    sum("total_tokens", ["price_status"], "Tokens by price status"),
  ] },
];

const usagePresets: UsagePreset[] = [
  { label: "Daily by provider", title: "Chart total tokens per day, split by provider", chart: { metric: "tokens", split: "provider", period: "day" } },
  { label: "Weekly cache mix", title: "Chart uncached input, cache reads, cache writes, and output per week", chart: { metric: "tokens", split: "type", period: "week" } },
  { label: "Models", title: "Total tokens and cache reads by model and provider", aggregations: [
    sum("total_tokens", ["model_family", "provider"], "Tokens by model"),
    sum("cache_read_input_tokens", ["model_family"], "Cached input by model"),
    sum("uncached_input_tokens", ["model_family"], "Uncached input by model"),
  ] },
  { label: "Repositories", title: "Total tokens by repository, overall and per week", aggregations: [
    sum("total_tokens", ["repository_name"], "Tokens by repository"),
    sum("total_tokens", ["week", "repository_name"], "Tokens per week by repository"),
  ] },
  { label: "Subagents", title: "Chart root agent versus subagent tokens per week", chart: { metric: "tokens", split: "session_kind", period: "week" } },
];

const byDesc = (field: string): OrderByClause[] => [{ field, dir: "desc" }];
const writingPresets: UsagePreset[] = [
  { label: "Deepest conversations", title: "Conversations with the most words you typed", orderBy: byDesc("typed_words") },
  { label: "Most hands-on", title: "Conversations with the most messages you typed into", orderBy: byDesc("typed_turns") },
  { label: "Longest messages", title: "Conversations holding your longest single typed message", orderBy: byDesc("longest_typed_words") },
  { label: "Most pasted-in", title: "Conversations with the most text that looks pasted", orderBy: byDesc("pasted_words") },
  { label: "Most copied from agents", title: "Conversations with the most agent output or earlier messages copied in", orderBy: byDesc("copied_words") },
];
const writingMetricPresets: UsagePreset[] = [
  { label: "By repository", title: "Typed and pasted words by repository", aggregations: [sum("typed_words", ["repository_name"], "Typed words by repository"), sum("pasted_words", ["repository_name"], "Likely pasted words by repository")] },
  { label: "By provider", title: "Typed words and tokens by provider", aggregations: [sum("typed_words", ["providers"], "Typed words by provider"), sum("total_tokens", ["providers"], "Tokens by provider")] },
  { label: "By source", title: "Typed words by source app", aggregations: [sum("typed_words", ["source_kind"], "Typed words by source")] },
];

const byAsc = (field: string): OrderByClause[] => [{ field, dir: "asc" }];
const messagePresets: UsagePreset[] = [
  { label: "Newest", title: "Most recent messages first", orderBy: byDesc("sent_at") },
  { label: "Longest typed", title: "Messages with the most words you typed", orderBy: byDesc("typed_words") },
  { label: "Most pasted", title: "Messages with the most text that looks pasted", orderBy: byDesc("pasted_words") },
  { label: "Most copied", title: "Messages with the most agent output or earlier messages copied in", orderBy: byDesc("copied_words") },
  { label: "Least typed", title: "Messages whose text is least your own", orderBy: [...byAsc("typed_share"), ...byDesc("words")] },
];
const messageMetricPresets: UsagePreset[] = [
  { label: "By category", title: "Messages by the category holding most of their words", aggregations: [{ id: "messages:main_category", op: "count", groupBy: ["main_category"], label: "Messages by main category" }, sum("other_words", ["main_category"], "Words from elsewhere by main category")] },
  { label: "Per week", title: "Typed and pasted words per week", aggregations: [sum("typed_words", ["week"], "Typed words per week"), sum("pasted_words", ["week"], "Likely pasted words per week")] },
  { label: "By repository", title: "Typed words and messages by repository", aggregations: [sum("typed_words", ["repository_name"], "Typed words by repository"), sum("message_count", ["repository_name"], "Messages by repository")] },
];

type PricingModel = { model: string; provider: string; tokens: number; first_day: string; last_day: string };
type PricingStatus = { prompt: string; unpriced_models: PricingModel[]; unpriced_tokens: number; confirmed_changes: number; proposed_changes: number };
const acknowledgedPricingKey = "pharos-pricing-acknowledged-models";

function acknowledgedModels(): Set<string> {
  const saved = preferences().get<unknown>(acknowledgedPricingKey, []);
  return new Set(Array.isArray(saved) ? saved.filter((model): model is string => typeof model === "string") : []);
}

async function copyText(text: string) {
  // The app shell's copy uses the native clipboard in the Mac app, where the web clipboard may be denied.
  if (window.pharosCopyText) return window.pharosCopyText(text);
  try { await navigator.clipboard.writeText(text); return; } catch { /* WKWebView may deny the async clipboard; fall back below. */ }
  const area = document.createElement("textarea");
  area.value = text;
  area.style.position = "fixed";
  area.style.opacity = "0";
  document.body.append(area);
  area.select();
  const copied = document.execCommand("copy");
  area.remove();
  if (!copied) throw new Error("Clipboard unavailable");
}

// Highlights when usage includes a model with no confirmed price that the user
// hasn't handed to an agent yet. Copying the prompt acknowledges those models.
function RefreshPricesButton() {
  const [status, setStatus] = useState<PricingStatus | null>(null);
  const [acknowledged, setAcknowledged] = useState(acknowledgedModels);
  const [copied, setCopied] = useState<"" | "copied" | "failed">("");
  useEffect(() => {
    const controller = new AbortController();
    if (shared) return;
    const load = () => void fetch("/api/pricing", { signal: controller.signal }).then(responseJSON<PricingStatus>).then(setStatus).catch(() => {});
    load();
    window.addEventListener("pharos:usage-refresh", load);
    return () => { controller.abort(); window.removeEventListener("pharos:usage-refresh", load); };
  }, []);
  const unpriced = status?.unpriced_models ?? [];
  const fresh = unpriced.filter((model) => !acknowledged.has(model.model));
  async function copy() {
    if (!status) return;
    try {
      await copyText(status.prompt);
      const next = new Set([...acknowledged, ...unpriced.map((model) => model.model)]);
      preferences().set(acknowledgedPricingKey, [...next]);
      setAcknowledged(next);
      setCopied("copied");
    } catch { setCopied("failed"); }
    window.setTimeout(() => setCopied(""), 2500);
  }
  if (!fresh.length) return null;
  return <div className="refresh-prices-notice" role="status">
    <button type="button" className="refresh-prices attention" title={`New unpriced models: ${fresh.map((model) => model.model).join(", ")}`} onClick={() => void copy()}>
      {copied === "copied" ? "Prompt copied" : copied === "failed" ? "Copy failed" : "Copy refresh prices prompt"}
      {!copied ? <span className="refresh-prices-count" aria-label={`${fresh.length} new unpriced models`}>{fresh.length}</span> : null}
    </button>
    <p>{fresh.length === 1 ? "A new model has" : `${fresh.length} new models have`} no price definition. Cost totals may be incomplete until pricing is updated.</p>
  </div>;
}

// Stacked column charts by day, week (Monday), or month over the table's time
// filter, or all time. Keys match the usage dataset's day, week, and month
// fields.
const chartPeriods: Record<ChartPeriod, { unit: string }> = { day: { unit: "day" }, week: { unit: "week" }, month: { unit: "month" } };
const chartRanges: Array<[ChartRange, string]> = [["30d", "30 days"], ["90d", "90 days"], ["6m", "6 months"], ["1y", "1 year"]];
const periodOptions: Array<[ChartPeriod, string]> = [["day", "Day"], ["week", "Week"], ["month", "Month"]];
// value, on a series the key can filter to, is what it filters to.
type ChartSeries = { key: string; label: string; color: string; value?: string; hiddenCount?: number };
// parts lists what a folded series (Other) holds in this bucket.
type ChartBucket = { key: string; date: Date; values: Record<string, number>; parts?: Record<string, Array<[string, number]>>; detail?: string };
const pad2 = (value: number) => String(value).padStart(2, "0");
const localDay = (date: Date) => `${date.getFullYear()}-${pad2(date.getMonth() + 1)}-${pad2(date.getDate())}`;

function periodStart(date: Date, period: ChartPeriod): Date {
  const start = new Date(date);
  start.setHours(12, 0, 0, 0);
  if (period === "week") start.setDate(start.getDate() - ((start.getDay() + 6) % 7));
  if (period === "month") start.setDate(1);
  return start;
}

function periodKey(date: Date, period: ChartPeriod): string {
  const day = localDay(periodStart(date, period));
  return period === "month" ? day.slice(0, 7) : day;
}

// parseDay reads a day key, or a month key as its first day.
function parseDay(day: string): Date {
  const [year, month, date] = day.split("-").map(Number);
  return new Date(year, (month || 1) - 1, date || 1, 12);
}

// earliestKey returns the first day, week, or month key among values, which
// "All time" starts from.
function earliestKey(values: unknown[]): Date | undefined {
  const keys = values.map(String).filter(value => /^\d{4}-\d{2}/.test(value)).sort();
  return keys.length ? parseDay(keys[0]) : undefined;
}

// rangeStart is the local midnight a range preset starts from.
function rangeStart(range: ChartRange): Date {
  const start = new Date();
  start.setHours(0, 0, 0, 0);
  if (range === "30d") start.setDate(start.getDate() - 29);
  else if (range === "90d") start.setDate(start.getDate() - 89);
  else if (range === "6m") { start.setMonth(start.getMonth() - 6); start.setDate(start.getDate() + 1); }
  else { start.setFullYear(start.getFullYear() - 1); start.setDate(start.getDate() + 1); }
  return start;
}

function nextPeriod(date: Date, period: ChartPeriod) {
  if (period === "day") date.setDate(date.getDate() + 1);
  else if (period === "week") date.setDate(date.getDate() + 7);
  else date.setMonth(date.getMonth() + 1);
}

function periodBuckets(period: ChartPeriod, start: Date, end: Date): ChartBucket[] {
  const last = periodStart(end, period), buckets: ChartBucket[] = [];
  for (let date = periodStart(start, period); date <= last && buckets.length < 5000; nextPeriod(date, period))
    buckets.push({ key: periodKey(date, period), date: new Date(date), values: {} });
  return buckets;
}

// The charts and the table share one time filter: a row matches when its
// activity, from its first to its last timestamp, overlaps the range. Dragging
// across columns, a range preset, and a filter typed into the table all set it.
type TimeFields = { first: string; last: string };
const usageTime: TimeFields = { first: "first_usage_at", last: "last_usage_at" };
const writingTime: TimeFields = { first: "first_message_at", last: "last_message_at" };
// to is exclusive.
type TimeBounds = { from?: Date; to?: Date };

// parseStamp reads a datetime filter value as the service does: RFC 3339, or a
// zone-less date or time as UTC.
function parseStamp(raw: string): Date | undefined {
  let text = raw.trim().replace(" ", "T");
  if (/^\d{4}-\d{2}-\d{2}$/.test(text)) text += "T00:00:00";
  if (/T\d{2}:\d{2}(:\d{2}(\.\d+)?)?$/.test(text)) text += "Z";
  const date = new Date(text);
  return Number.isNaN(date.getTime()) ? undefined : date;
}

// localStamp writes a time as RFC 3339 with the local offset, so a day
// boundary picked in the chart is local midnight to the service too.
function localStamp(date: Date): string {
  const offset = -date.getTimezoneOffset(), hours = Math.floor(Math.abs(offset) / 60);
  return `${localDay(date)}T${pad2(date.getHours())}:${pad2(date.getMinutes())}:00${offset < 0 ? "-" : "+"}${pad2(hours)}:${pad2(Math.abs(offset) % 60)}`;
}

function isTimeTerm(term: WhereTerm, fields: TimeFields): term is WhereClause {
  return !isOrGroup(term) && (term.field === fields.first || term.field === fields.last) && !term.negated && [">", ">=", "<", "<="].includes(term.op);
}

function timeBounds(where: WhereTerm[], fields: TimeFields): TimeBounds {
  const bounds: TimeBounds = {};
  for (const term of where) {
    if (!isTimeTerm(term, fields)) continue;
    const at = parseStamp(term.value);
    if (!at) continue;
    if (term.op === ">" || term.op === ">=") { if (!bounds.from || at > bounds.from) bounds.from = at; }
    else {
      const end = term.op === "<=" ? new Date(at.getTime() + 1) : at;
      if (!bounds.to || end < bounds.to) bounds.to = end;
    }
  }
  return bounds;
}

function withTimeRange(where: WhereTerm[], fields: TimeFields, from?: Date, to?: Date): WhereTerm[] {
  const kept = where.filter(term => !isTimeTerm(term, fields));
  if (from) kept.push({ field: fields.last, op: ">=", value: localStamp(from) });
  if (to) kept.push({ field: fields.first, op: "<", value: localStamp(to) });
  return kept;
}

// chartSpan is the first and last day a chart shows: the time filter's range,
// else from the earliest data to today.
function chartSpan(bounds: TimeBounds, earliest?: Date): [Date, Date] {
  const end = bounds.to ? new Date(bounds.to.getTime() - 1) : new Date();
  return [bounds.from ?? (earliest && earliest < end ? earliest : end), end];
}

// bucketRange is the time from the first bucket's start to the last one's end.
// A range reaching into the future is left open, so it keeps today.
function bucketRange(first: ChartBucket, last: ChartBucket, period: ChartPeriod): [Date, Date | undefined, Date] {
  const from = new Date(first.date), to = new Date(last.date);
  from.setHours(0, 0, 0, 0);
  to.setHours(0, 0, 0, 0);
  nextPeriod(to, period);
  return [from, to > new Date() ? undefined : to, to];
}

// A range picked in a chart shows enough columns to read: a short one moves to
// a finer period.
function finerPeriod(period: ChartPeriod, from: Date, to: Date): ChartPeriod {
  const days = (to.getTime() - from.getTime()) / 86_400_000;
  if (period === "month" && days < 240) period = "week";
  if (period === "week" && days < 56) period = "day";
  return period;
}

function rangeLabel(bounds: TimeBounds): string {
  const year = new Date().getFullYear();
  const day = (date: Date) => date.toLocaleDateString(undefined, { month: "short", day: "numeric", ...(date.getFullYear() !== year ? { year: "numeric" } : {}) });
  const last = bounds.to ? new Date(bounds.to.getTime() - 1) : undefined;
  if (bounds.from && last) return localDay(bounds.from) === localDay(last) ? day(last) : `${day(bounds.from)} – ${day(last)}`;
  return bounds.from ? `Since ${day(bounds.from)}` : last ? `Through ${day(last)}` : "";
}

// The range presets set the time filter; one the presets don't name shows as
// its own button, which clears it.
function RangeControl({ bounds, onChange }: { bounds: TimeBounds; onChange: (from?: Date) => void }) {
  const active = !bounds.from && !bounds.to ? "all" : bounds.from && !bounds.to ? chartRanges.find(([range]) => rangeStart(range).getTime() === bounds.from!.getTime())?.[0] : undefined;
  return <div className="usage-presets chart-toggle" role="group" aria-label="Range">
    <span className="usage-preset-label">Range</span>
    {chartRanges.map(([range, text]) => <button key={range} type="button" aria-pressed={active === range} onClick={() => onChange(rangeStart(range))}>{text}</button>)}
    <button type="button" aria-pressed={active === "all"} onClick={() => onChange()}>All time</button>
    {active ? null : <button type="button" aria-pressed="true" className="chart-range-custom" title="Clear this time filter" onClick={() => onChange()}>{rangeLabel(bounds)}<span className="usage-legend-x" aria-label="Clear">×</span></button>}
  </div>;
}

// A key filters the table to rows with its value; null stands for rows with
// none. Several values are one OR group.
type SliceValue = string | null;
const sliceClause = (clause: WhereClause, field: string) => clause.field === field && !clause.negated && (clause.op === "=" || clause.op === "is_null");
function isSliceTerm(term: WhereTerm, field: string): boolean {
  return isOrGroup(term) ? term.any.length > 0 && term.any.every(clause => sliceClause(clause, field)) : sliceClause(term, field);
}

function sliceValues(where: WhereTerm[], field: string): SliceValue[] {
  const term = where.find(item => isSliceTerm(item, field));
  return term ? predicatesOf(term).map(clause => clause.op === "is_null" ? null : clause.value) : [];
}

function withSlice(where: WhereTerm[], field: string, values: SliceValue[]): WhereTerm[] {
  const kept = where.filter(term => !isSliceTerm(term, field));
  const clauses: WhereClause[] = values.map(value => value === null ? { field, op: "is_null", value: "" } : { field, op: "=", value });
  if (clauses.length === 1) kept.push(clauses[0]);
  else if (clauses.length) kept.push({ any: clauses });
  return kept;
}

// Clicking a key shows only it, or everything again when it was alone;
// ⌘-, Ctrl-, or Shift-clicking adds it or takes it away.
function nextSlice<T>(current: T[], value: T, additive: boolean): T[] {
  const has = current.includes(value);
  if (additive) return has ? current.filter(item => item !== value) : [...current, value];
  return has && current.length === 1 ? [] : [value];
}

// Charts change their table's filters through this.
type FilterChange = (change: (where: WhereTerm[]) => WhereTerm[]) => void;
function filterChange(api: QueryTableApi<Row>): FilterChange {
  return change => api.setQuery(previous => ({ ...previous, where: change(previous.where), offset: 0 }));
}

// foldSeries adds (bucket, value, amount) rows to their buckets as series.
// Fixed values keep one color each whatever the filters; otherwise the five
// largest in view get their own series, as does any selected value, and the
// rest fold into Other. Expanding Other raises the limit five at a time.
function foldSeries(rows: Array<[string, string, number]>, byKey: Map<string, ChartBucket>, label: (name: string) => string, fixed: string[] | undefined, selected: string[], limit = 5): ChartSeries[] {
  const inView = rows.filter(([key]) => byKey.has(key)), totals = new Map<string, number>();
  for (const [, name, value] of inView) totals.set(name, (totals.get(name) ?? 0) + value);
  const shown = fixed ? [...fixed.filter(name => totals.get(name)), ...[...totals.keys()].filter(name => !fixed.includes(name) && totals.get(name))]
    : [...totals.entries()].filter(([, value]) => value > 0).sort((left, right) => right[1] - left[1]).map(([name]) => name);
  const top = shown.slice(0, limit);
  // A selected value keeps its key, so it can be cleared, even with nothing in view.
  for (const name of selected) if (!top.includes(name)) top.push(name);
  const rest = shown.filter(name => !top.includes(name));
  const color = (name: string, index: number) => splitColors[(fixed ? fixed.indexOf(name) : index)] ?? splitColors[index % splitColors.length];
  const series: ChartSeries[] = top.map((name, index) => ({ key: `s:${name}`, label: label(name), color: color(name, index), value: name }));
  if (rest.length) series.push({ key: "other", label: `Other (${rest.length})`, color: "var(--muted)", hiddenCount: rest.length });
  for (const [bucketKey, name, value] of inView) {
    const target = byKey.get(bucketKey)!, key = top.includes(name) ? `s:${name}` : "other";
    target.values[key] = (target.values[key] ?? 0) + value;
    if (key !== "other" || value <= 0) continue;
    const parts = ((target.parts ??= {}).other ??= []), part = parts.find(([item]) => item === label(name));
    if (part) part[1] += value;
    else parts.push([label(name), value]);
  }
  return series;
}

// niceTicks places two or three gridlines at round values up to peak.
function niceTicks(peak: number): number[] {
  if (!(peak > 0)) return [];
  const raw = peak / 2.5, magnitude = 10 ** Math.floor(Math.log10(raw)), normal = raw / magnitude;
  const step = (normal <= 1 ? 1 : normal <= 2 ? 2 : normal <= 5 ? 5 : 10) * magnitude, ticks: number[] = [];
  for (let index = 1; index * step <= peak * 1.0001 && index < 10; index++) ticks.push(index * step);
  return ticks;
}

// niceCeil rounds up to 1, 2, or 5 times a power of ten.
function niceCeil(value: number): number {
  const magnitude = 10 ** Math.floor(Math.log10(value)), normal = value / magnitude;
  return (normal <= 1 ? 1 : normal <= 2 ? 2 : normal <= 5 ? 5 : 10) * magnitude;
}

// A column scale maps a value to a height, in percent of the plot.
type ColumnScale = { y: (value: number) => number; ticks: number[]; split?: number };

// columnScale is linear, unless a few columns dwarf the rest: when the tallest
// is over twice the 80th percentile of non-empty columns (rounded up to a
// round value), the lower half of the plot is linear up to that value and the
// upper half logarithmic above it, so outliers stay comparable with each other
// without flattening every other column.
function columnScale(totals: number[]): ColumnScale {
  const peak = Math.max(0, ...totals), filled = totals.filter(value => value > 0).sort((left, right) => left - right);
  const split = filled.length >= 5 ? niceCeil(filled[Math.ceil(filled.length * 0.8) - 1]) : 0;
  if (!(split > 0 && peak > 2 * split)) {
    const scale = peak || 1;
    return { y: value => 100 * value / scale, ticks: niceTicks(peak) };
  }
  const span = Math.log(peak / split);
  const y = (value: number) => value <= split ? 50 * value / split : 50 + 50 * Math.log(value / split) / span;
  // Powers of ten above the break, or 1-2-5 steps when fewer than two fit;
  // labels closer than a tenth of the plot to the break or each other are dropped.
  const candidates: number[] = [], powers: number[] = [];
  for (let power = Math.floor(Math.log10(split)); power <= Math.ceil(Math.log10(peak)); power++)
    for (const step of [1, 2, 5]) {
      const value = step * 10 ** power;
      if (value <= split || value > peak) continue;
      candidates.push(value);
      if (step === 1) powers.push(value);
    }
  const upper: number[] = [];
  for (const value of powers.length >= 2 ? powers : candidates)
    if (y(value) - Math.max(50, ...upper.map(y)) >= 10) upper.push(value);
  return { y, ticks: [...niceTicks(split).filter(value => y(value) <= 40), ...upper], split };
}

function compactNumber(value: number): string {
  const n = Number(value) || 0, abs = Math.abs(n);
  if (abs >= 1e6) {
    const units = [[1e6, "M"], [1e9, "B"], [1e12, "T"]] as const;
    let index = abs >= 1e12 ? 2 : abs >= 1e9 ? 1 : 0;
    let scaled = n / units[index][0], digits = Math.abs(scaled) < 10 ? 1 : 0;
    if (index < 2 && Math.abs(Number(scaled.toFixed(digits))) >= 1000) {
      index++;
      scaled = n / units[index][0];
      digits = 1;
    }
    return `${scaled.toFixed(digits)}${units[index][1]}`;
  }
  if (abs >= 1e4) return `${Math.round(n / 1e3)}k`;
  return Math.round(n).toLocaleString();
}

function readStored<T extends object>(key: string, fallback: T, valid: (value: T) => boolean): T {
  const saved = preferences().get<unknown>(key, {});
  if (!saved || typeof saved !== "object") return fallback;
  const stored = { ...fallback, ...saved } as T;
  return valid(stored) ? stored : fallback;
}

function store(key: string, value: unknown) {
  preferences().set(key, value);
}

function Segmented<T extends string>({ label, value, options, onChange }: { label: string; value: T; options: Array<[T, string, string?]>; onChange: (next: T) => void }) {
  return <div className="usage-presets chart-toggle" role="group" aria-label={label}>
    <span className="usage-preset-label">{label}</span>
    {options.map(([option, text, disabledReason]) => <button key={option} type="button" aria-pressed={value === option} disabled={Boolean(disabledReason)} title={disabledReason} onClick={() => onChange(option)}>{text}</button>)}
  </div>;
}

// A single series needs no legend unless the title does not name it (a split
// that happens to hold one value). Hovering a column shows a key of the series
// it holds, largest first, with what a folded series contains.
// With onSelect, clicking a key selects its series: only selected series are
// drawn and the rest stay in the key, dimmed. With onRange, dragging across
// columns (or clicking or pressing Enter on one) picks a time range.
function StackedColumns({ title, controls, series, buckets: sourceBuckets, period, format, tickFormat = format, noun, loading, error, legend = false, selected = [], percent = false, onSelect, onExpand, onRange }: {
  title: string; controls: React.ReactNode; series: ChartSeries[]; buckets: ChartBucket[]; period: ChartPeriod;
  format: (value: number) => string; tickFormat?: (value: number) => string; noun: string; loading: boolean; error: string; legend?: boolean;
  selected?: string[]; percent?: boolean; onSelect?: (key: string | null, additive: boolean) => void; onExpand?: () => void; onRange?: (first: ChartBucket, last: ChartBucket) => void;
}) {
  const root = useRef<HTMLDivElement>(null), tipRef = useRef<HTMLDivElement>(null), bars = useRef<HTMLDivElement>(null);
  const [tip, setTip] = useState<{ bucket: ChartBucket; x: number; width: number; top: number } | null>(null);
  const [drag, setDrag] = useState<{ anchor: number; current: number } | null>(null);
  // Pointer events can outrun rendering, so the handlers read the drag from a ref.
  const dragRef = useRef(drag);
  const moveDrag = (next: typeof drag) => { dragRef.current = next; setDrag(next); };
  const drawn = selected.length ? series.filter(item => selected.includes(item.key)) : series;
  const buckets = percent ? sourceBuckets.map(bucket => {
    const total = drawn.reduce((sum, item) => sum + (bucket.values[item.key] ?? 0), 0);
    const values = Object.fromEntries(drawn.map(item => [item.key, total ? 100 * (bucket.values[item.key] ?? 0) / total : 0]));
    const parts = Object.fromEntries(Object.entries(bucket.parts ?? {}).map(([key, entries]) => [key, entries.map(([name, value]) => [name, total ? 100 * value / total : 0] as [string, number])]));
    return { ...bucket, values, parts };
  }) : sourceBuckets;
  const total = (bucket: ChartBucket) => drawn.reduce((sum, item) => sum + (bucket.values[item.key] ?? 0), 0);
  const { y, ticks, split } = percent ? { y: (value: number) => value, ticks: [25, 50, 75, 100], split: undefined } : columnScale(buckets.map(total));
  const unit = chartPeriods[period].unit, stacked = drawn.length > 1;
  const label = (date: Date) => period === "month" ? date.toLocaleDateString(undefined, { month: "short", year: "numeric" }) : date.toLocaleDateString(undefined, { month: "short", day: "numeric", ...(period === "day" && buckets.length > 120 ? { year: "2-digit" } : {}) });
  const heading = (bucket: ChartBucket) => `${period === "week" ? "Week of " : ""}${label(bucket.date)}`;
  const suffix = noun ? ` ${noun}` : "";
  const present = (bucket: ChartBucket) => drawn.filter(item => (bucket.values[item.key] ?? 0) > 0).sort((left, right) => bucket.values[right.key] - bucket.values[left.key]);
  const describe = (bucket: ChartBucket) => `${heading(bucket)}: `
    + (present(bucket).map(item => `${item.label} ${format(bucket.values[item.key])}`).join(", ") || "none")
    + (stacked ? `; ${format(total(bucket))}${suffix} in all` : suffix) + (bucket.detail ? `; ${bucket.detail}` : "");
  useLayoutEffect(() => {
    const element = tipRef.current, box = root.current;
    if (!tip || !element || !box) return;
    const width = element.offsetWidth, right = tip.x + tip.width / 2 + 10;
    element.style.left = `${right + width <= box.offsetWidth - 4 ? right : Math.max(0, tip.x - tip.width / 2 - 10 - width)}px`;
    element.style.top = `${Math.max(0, Math.min(tip.top, box.offsetHeight - element.offsetHeight - 4))}px`;
  }, [tip]);
  function show(event: React.SyntheticEvent<HTMLElement>, bucket: ChartBucket) {
    const box = root.current?.getBoundingClientRect(), at = event.currentTarget.getBoundingClientRect(), plot = event.currentTarget.parentElement?.getBoundingClientRect();
    if (box && plot) setTip({ bucket, x: at.left - box.left + at.width / 2, width: at.width, top: plot.top - box.top });
  }
  useEffect(() => {
    if (!drag) return;
    const cancel = (event: KeyboardEvent) => { if (event.key === "Escape") moveDrag(null); };
    window.addEventListener("keydown", cancel);
    return () => window.removeEventListener("keydown", cancel);
  }, [Boolean(drag)]);
  const indexAt = (x: number) => {
    const box = bars.current?.getBoundingClientRect();
    return box?.width ? Math.max(0, Math.min(buckets.length - 1, Math.floor((x - box.left) / box.width * buckets.length))) : 0;
  };
  const span = (value: { anchor: number; current: number } | null) => value ? [Math.min(value.anchor, value.current), Math.max(value.anchor, value.current)] : null;
  const picked = span(drag);
  const picking = onRange && buckets.length ? {
    onPointerDown(event: React.PointerEvent<HTMLDivElement>) {
      if (event.button !== 0) return;
      event.currentTarget.setPointerCapture(event.pointerId);
      const index = indexAt(event.clientX);
      moveDrag({ anchor: index, current: index });
    },
    onPointerMove(event: React.PointerEvent<HTMLDivElement>) {
      const current = dragRef.current, index = indexAt(event.clientX);
      if (current && index !== current.current) moveDrag({ ...current, current: index });
    },
    onPointerUp() {
      const range = span(dragRef.current);
      if (!range) return;
      moveDrag(null);
      setTip(null);
      onRange(buckets[range[0]], buckets[range[1]]);
    },
    onPointerCancel() { moveDrag(null); },
  } : {};
  const density = buckets.length > 300 ? "denser" : buckets.length > 120 ? "dense" : "";
  const middle = buckets[Math.floor(buckets.length / 2)], last = buckets[buckets.length - 1];
  const selectable = (item: ChartSeries) => Boolean(onSelect) && item.value !== undefined;
  return <div className="usage-chart" ref={root}>
    <div className="usage-chart-head"><h3>{title}</h3><div className="usage-chart-controls">{controls}</div></div>
    {series.length > 1 || legend || onRange ? <div className="usage-chart-legend">
      {series.length > 1 || legend ? series.map(item => {
        const on = selected.includes(item.key), dimmed = selected.length > 0 && !on;
        const content = <><span className="usage-swatch" style={{ background: item.color }} />{item.label}{on && selected.length === 1 ? <span className="usage-legend-x" aria-hidden="true">×</span> : null}</>;
        return item.hiddenCount && onExpand ? <button key={item.key} type="button" className="usage-legend-item" title={`Show next ${Math.min(5, item.hiddenCount)} values`} onClick={onExpand}>{content}</button>
          : selectable(item) ? <button key={item.key} type="button" className={`usage-legend-item${dimmed ? " dimmed" : ""}`} aria-pressed={on}
          title={on && selected.length === 1 ? "Clear this filter" : `Show only ${item.label}; ⌘-click to ${on ? "remove it" : "add it"}`}
          onClick={event => onSelect!(item.key, event.metaKey || event.ctrlKey || event.shiftKey)}>{content}</button>
          : <span key={item.key} className={`usage-legend-item${dimmed ? " dimmed" : ""}`}>{content}</span>;
      }) : null}
      {selected.length > 1 ? <button type="button" className="usage-legend-item usage-legend-clear" onClick={() => onSelect?.(null, false)}>Clear</button> : null}
      {onRange ? <span className="usage-chart-hint">{series.some(selectable) ? "Click a key to filter · drag" : "Drag"} across columns to filter dates</span> : null}
    </div> : null}
    {error ? <p className="query-table-error">{error}</p> : null}
    <div className={`usage-chart-plot${split ? " broken" : ""}`}>
      {split ? <div className="usage-chart-log" aria-hidden="true"><span>log scale</span></div> : null}
      {ticks.map(tick => <div key={tick} className="usage-chart-grid" style={{ bottom: `${y(tick)}%` }} aria-hidden="true"><span>{tickFormat(tick)}</span></div>)}
      <div ref={bars} className={`usage-chart-bars ${density} ${loading ? "loading" : ""} ${onRange ? "pickable" : ""}`} role="list" aria-busy={loading} aria-label={split ? `${title}; linear up to ${tickFormat(split)}, logarithmic above` : title} {...picking}>
        {buckets.map((bucket, index) => {
          // Each segment spans the heights of its stack's running totals, so a
          // stack crossing the break is split at the same value as the axis.
          let below = 0;
          const parts = drawn.flatMap(item => {
            const value = bucket.values[item.key] ?? 0, bottom = y(below);
            below += value;
            const height = y(below) - bottom;
            return value > 0 && (!stacked || percent || height >= 0.5) ? [{ item, height }] : [];
          });
          const className = [tip?.bucket.key === bucket.key && !drag ? "active" : "", picked && index >= picked[0] && index <= picked[1] ? "picked" : ""].filter(Boolean).join(" ");
          return <span key={bucket.key} role="listitem" tabIndex={0} aria-label={describe(bucket)} className={className} onMouseEnter={event => show(event, bucket)} onFocus={event => show(event, bucket)} onMouseLeave={() => setTip(null)} onBlur={() => setTip(null)}
            onKeyDown={onRange ? event => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); onRange(bucket, bucket); } } : undefined}>
            {parts.map(({ item, height }, index) => <i key={item.key} className={index === parts.length - 1 ? "top" : ""} style={{ height: `${stacked ? height : Math.max(1.5, height)}%`, background: item.color }} />)}
          </span>;
        })}
      </div>
      {split ? <div className="usage-chart-break" aria-hidden="true"><span>{tickFormat(split)}</span></div> : null}
      {picked ? <div className="usage-chart-pick" style={{ left: `${100 * picked[0] / buckets.length}%`, width: `${100 * (picked[1] - picked[0] + 1) / buckets.length}%` }} aria-hidden="true">
        <span>{picked[0] === picked[1] ? heading(buckets[picked[0]]) : `${label(buckets[picked[0]].date)} – ${label(buckets[picked[1]].date)}`}</span>
      </div> : null}
    </div>
    <div className="usage-chart-axis"><span>{buckets.length ? label(buckets[0].date) : ""}</span><span>{middle && buckets.length > 4 ? label(middle.date) : ""}</span><span>{!last ? "" : last.key !== periodKey(new Date(), period) ? label(last.date) : period === "day" ? "today" : `this ${unit}`}</span></div>
    {tip && !drag ? <div className="usage-chart-tip" ref={tipRef} role="status">
      <div className="usage-chart-tip-head">{heading(tip.bucket)}</div>
      {present(tip.bucket).length ? <ul>{present(tip.bucket).map(item => {
        const parts = [...(tip.bucket.parts?.[item.key] ?? [])].sort((left, right) => right[1] - left[1]);
        return <li key={item.key}>
          <span className="usage-chart-tip-row"><span className="usage-swatch" style={{ background: item.color }} /><span className="usage-chart-tip-label">{item.label}</span><span className="usage-chart-tip-value">{format(tip.bucket.values[item.key])}</span></span>
          {parts.length ? <ul className="usage-chart-tip-parts">
            {parts.slice(0, 4).map(([name, value]) => <li key={name} className="usage-chart-tip-row"><span className="usage-chart-tip-label">{name}</span><span className="usage-chart-tip-value">{format(value)}</span></li>)}
            {parts.length > 4 ? <li className="usage-chart-tip-more">+{parts.length - 4} more</li> : null}
          </ul> : null}
        </li>;
      })}</ul> : <div className="usage-chart-tip-more">Nothing in this {unit}</div>}
      {stacked && present(tip.bucket).length > 1 ? <div className="usage-chart-tip-row usage-chart-tip-total"><span className="usage-chart-tip-label">Total</span><span className="usage-chart-tip-value">{format(total(tip.bucket))}{suffix}</span></div> : null}
      {tip.bucket.detail ? <div className="usage-chart-tip-more">{tip.bucket.detail}</div> : null}
    </div> : null}
  </div>;
}

type AggregationMetric = { id: string; buckets: Array<{ keys: unknown[]; value: unknown }> };

// Fetches aggregations for the given filters, refetching when either changes
// or Usage is refreshed. Results are tagged so a stale response never renders
// under new chart settings.
function useAggregations(dataset: Dataset, where: WhereTerm[], aggregations: AggregationClause[]) {
  const key = JSON.stringify({ where, aggregations });
  const [state, setState] = useState<{ key: string; metrics: AggregationMetric[]; error: string; loading: boolean }>({ key: "", metrics: [], error: "", loading: true });
  const [refresh, setRefresh] = useState(0);
  useEffect(() => {
    const bump = () => setRefresh(value => value + 1);
    window.addEventListener("pharos:usage-refresh", bump);
    return () => window.removeEventListener("pharos:usage-refresh", bump);
  }, []);
  useEffect(() => {
    if (shared) {
      const request = JSON.parse(key) as { where: WhereTerm[]; aggregations: AggregationClause[] };
      const result = applyAggregations(shared.datasets[dataset], { ...EMPTY_QUERY, where: request.where, aggregations: request.aggregations }, schemas[dataset]);
      // The service returns time buckets newest first; so does a shared file.
      const timePosition = (aggregation: AggregationClause) => aggregation.groupBy.findIndex(field => field === "day" || field === "week" || field === "month");
      request.aggregations.forEach((aggregation, index) => {
        const position = timePosition(aggregation);
        if (dataset === "usage" && position >= 0) result.metrics[index]?.buckets.sort((left, right) => String(right.keys[position] ?? "").localeCompare(String(left.keys[position] ?? "")));
      });
      setState({ key, metrics: result.metrics as AggregationMetric[], error: "", loading: false });
      return;
    }
    const controller = new AbortController();
    setState(previous => ({ ...previous, loading: true }));
    fetch(`/api/query/${dataset}/aggregations`, { method: "POST", headers: { "Content-Type": "application/json" }, body: key, signal: controller.signal })
      .then(responseJSON<{ metrics: AggregationMetric[] }>)
      .then(result => setState({ key, metrics: result.metrics ?? [], error: "", loading: false }))
      .catch(failure => { if (!controller.signal.aborted) setState({ key, metrics: [], error: failure instanceof Error ? failure.message : String(failure), loading: false }); });
    return () => controller.abort();
  }, [dataset, key, refresh]);
  return { ...state, metrics: state.key === key ? state.metrics : [] };
}

// Token types stack from the bottom in this order; adjacent colors pass the
// palette validator's normal-vision and color-blindness checks in both themes.
const tokenTypes: ChartSeries[] = [
  { key: "uncached_input_tokens", label: "Uncached input", color: "var(--heat-3)" },
  { key: "cache_read_input_tokens", label: "Cache reads", color: "var(--heat-1)" },
  { key: "cache_creation_input_tokens", label: "Cache writes", color: "var(--gold)" },
  { key: "output_tokens", label: "Output", color: "var(--heat-2)" },
  { key: "unclassified_tokens", label: "Unclassified", color: "var(--muted)" },
];
const splitColors = ["var(--heat-2)", "var(--gold)", "var(--heat-0)", "var(--heat-3)", "var(--heat-1)"];
const tokenSplits: Array<[TokenSplit, string]> = [["type", "Token type"], ["provider", "Provider"], ["model_family", "Model"], ["repository_name", "Repository"], ["session_kind", "Agent"]];
// Providers and agent kinds keep one color each whatever the filters; models
// and repositories show the five largest in view and fold the rest into Other.
const fixedSplitOrder: Partial<Record<TokenSplit, string[]>> = { provider: ["claude", "codex", "tl1", "chatgpt", "canonical"], session_kind: ["root", "subagent"] };
const sessionKindLabels: Record<string, string> = { root: "Root agent", subagent: "Sub-agent" };
const tokenChartKey = "pharos-usage-chart";
const defaultTokenChart: TokenChartView = { metric: "tokens", split: "type", period: "week" };
const costTick = (value: number) => Math.abs(value) >= 1e4 ? `$${compactNumber(value)}` : `$${value.toLocaleString(undefined, { maximumFractionDigits: 2 })}`;

function splitLabel(split: TokenSplit, value: unknown): string {
  if (value === null || value === undefined || value === "") return split === "repository_name" ? "No repository" : "Unknown";
  const text = String(value);
  if (split === "provider") return optionLabels(schemas.usage.fields).get("provider")?.get(text) ?? text;
  if (split === "session_kind") return sessionKindLabels[text] ?? text;
  return text;
}

// Picking a range in a chart filters the table to it, moving to a finer
// period when the range is short.
function pickRange(onFilter: FilterChange, fields: TimeFields, period: ChartPeriod, onPeriod: (next: ChartPeriod) => void) {
  return (first: ChartBucket, last: ChartBucket) => {
    const [from, to, end] = bucketRange(first, last, period);
    onFilter(where => withTimeRange(where, fields, from, to));
    const finer = finerPeriod(period, from, end);
    if (finer !== period) onPeriod(finer);
  };
}

// Split by a row field, the key filters the table to the values clicked; the
// chart leaves that filter out of its own query so the other values stay in
// the key to pick from. Token types are not rows, so clicking one only
// narrows the chart.
function TokenChart({ where, onFilter, view, onChange }: { where: WhereTerm[]; onFilter: FilterChange; view: TokenChartView; onChange: (next: Partial<TokenChartView>) => void }) {
  const split: TokenSplit = view.metric === "cost" && view.split === "type" ? "provider" : view.split;
  const measure = view.metric === "cost" ? "cost_usd" : "total_tokens";
  const [types, setTypes] = useState<string[]>([]);
  const [seriesLimit, setSeriesLimit] = useState(5);
  useEffect(() => setSeriesLimit(5), [split]);
  const sliced = split !== "type";
  const chosen = sliced ? sliceValues(where, split).map(value => value ?? "") : types;
  const chartWhere = sliced ? where.filter(term => !isSliceTerm(term, split)) : where;
  const aggregations = useMemo(() => split === "type"
    ? tokenTypes.map(type => sum(type.key, [view.period], type.label))
    : [sum(measure, [view.period, split], "chart")], [split, measure, view.period]);
  const result = useAggregations("usage", chartWhere, aggregations);
  const bounds = timeBounds(where, usageTime);
  const { series, buckets } = useMemo(() => {
    const earliest = earliestKey(result.metrics.flatMap(metric => metric.buckets.map(bucket => bucket.keys[0])));
    const buckets = periodBuckets(view.period, ...chartSpan(bounds, earliest)), byKey = new Map(buckets.map(bucket => [bucket.key, bucket]));
    if (split === "type") {
      result.metrics.forEach((metric, index) => {
        for (const bucket of metric.buckets) {
          const target = byKey.get(String(bucket.keys[0]));
          if (target) target.values[tokenTypes[index].key] = Number(bucket.value) || 0;
        }
      });
      return { series: tokenTypes.filter(type => type.key !== "unclassified_tokens" || buckets.some(bucket => bucket.values[type.key])).map(type => ({ ...type, value: type.key })), buckets };
    }
    const rows = (result.metrics[0]?.buckets ?? []).map(bucket => [String(bucket.keys[0]), String(bucket.keys[1] ?? ""), Number(bucket.value) || 0] as [string, string, number]);
    return { series: foldSeries(rows, byKey, name => splitLabel(split, name), fixedSplitOrder[split], chosen, seriesLimit), buckets };
  }, [result.metrics, split, view.period, bounds.from?.getTime(), bounds.to?.getTime(), chosen.join("\u0000"), seriesLimit]);
  const selected = series.filter(item => item.value !== undefined && chosen.includes(item.value)).map(item => item.key);
  function select(key: string | null, additive: boolean) {
    const value = key === null ? undefined : series.find(item => item.key === key)?.value;
    if (!sliced) setTypes(current => value === undefined ? [] : nextSlice(current, value, additive));
    else onFilter(current => withSlice(current, split, value === undefined ? [] : nextSlice(sliceValues(current, split), value || null, additive)));
  }
  const unit = chartPeriods[view.period].unit, splitName = tokenSplits.find(([value]) => value === split)?.[1].toLowerCase() ?? split;
  const title = `${view.metric === "cost" ? "API cost" : view.metric === "percent" ? "Token share" : "Tokens"} per ${unit} by ${splitName}`;
  const controls = <>
    <Segmented label="Show" value={view.metric} options={[["tokens", "Tokens"], ["cost", "Cost"], ["percent", "%"]]} onChange={metric => onChange({ metric })} />
    <Segmented label="Split" value={split} options={tokenSplits.map(([value, text]) => [value, text, value === "type" && view.metric === "cost" ? "Cost is priced per request, not per token type" : undefined])} onChange={next => onChange({ split: next })} />
    <Segmented label="By" value={view.period} options={periodOptions} onChange={period => onChange({ period })} />
    <RangeControl bounds={bounds} onChange={from => onFilter(current => withTimeRange(current, usageTime, from))} />
  </>;
  return <StackedColumns title={title} controls={controls} series={series.length ? series : [{ key: "none", label: title, color: "var(--heat-1)" }]} buckets={buckets} period={view.period}
    format={view.metric === "cost" ? formatUSD : view.metric === "percent" ? value => `${value.toLocaleString(undefined, { maximumFractionDigits: 1 })}%` : compactNumber}
    tickFormat={view.metric === "cost" ? costTick : view.metric === "percent" ? value => `${value}%` : compactNumber}
    noun={view.metric === "tokens" ? "tokens" : ""} loading={result.loading} error={result.error} legend={series.length > 0} percent={view.metric === "percent"}
    selected={selected} onSelect={select} onExpand={() => setSeriesLimit(limit => limit + 5)} onRange={pickRange(onFilter, usageTime, view.period, period => onChange({ period }))} />;
}

function PresetRow({ label, presets, onApply, clear }: { label: string; presets: UsagePreset[]; onApply: (preset: UsagePreset | null) => void; clear?: boolean }) {
  return <div className="usage-presets" role="group" aria-label={`${label} presets`}>
    <span className="usage-preset-label">{label}</span>
    {presets.map(preset => <button key={preset.label} type="button" title={preset.title} onClick={() => onApply(preset)}>{preset.label}</button>)}
    {clear ? <button type="button" className="usage-preset-clear" onClick={() => onApply(null)}>Clear metrics</button> : null}
  </div>;
}

function TokenUsage() {
  const [chart, setChart] = useState<TokenChartView>(() => readStored(tokenChartKey, defaultTokenChart, view =>
    ["tokens", "cost", "percent"].includes(view.metric) && tokenSplits.some(([value]) => value === view.split) && view.period in chartPeriods));
  const update = (next: Partial<TokenChartView>) => setChart(previous => { const merged = { ...previous, ...next }; store(tokenChartKey, merged); return merged; });
  function apply(preset: UsagePreset | null) {
    if (preset?.chart) {
      update(preset.chart);
      document.querySelector("#usage .usage-chart")?.scrollIntoView({ block: "nearest", behavior: "smooth" });
      return;
    }
    tableApis.get("usage")?.setQuery(previous => ({ ...previous, aggregations: preset?.aggregations ?? [], offset: 0 }));
  }
  return <QuerySurface dataset="usage" header={api => {
    const where = toAggregationQuery(api.query, schemas.usage).where;
    return <>
      <TokenSummary where={where} />
      <TokenChart where={where} onFilter={filterChange(api)} view={chart} onChange={update} />
      <div className="usage-preset-rows">
        <PresetRow label="Cost" presets={costPresets} onApply={apply} />
        <PresetRow label="Tokens" presets={usagePresets} onApply={apply} clear />
      </div>
    </>;
  }} />;
}

const tokenSummaryAggregations: AggregationClause[] = [
  sum("total_tokens", [], "Total tokens"),
  sum("cost_usd", [], "API cost"),
  sum("uncached_input_tokens", [], "Uncached input"),
  sum("cache_read_input_tokens", [], "Cache reads"),
  sum("output_tokens", [], "Output tokens"),
  { id: "agent_sessions", op: "count_distinct", field: "agent_session_id", groupBy: [], label: "Agent sessions" },
];

function TokenSummary({ where }: { where: WhereTerm[] }) {
  const result = useAggregations("usage", where, tokenSummaryAggregations);
  const values = new Map(result.metrics.map(metric => [metric.id, Number(metric.buckets[0]?.value) || 0]));
  const value = (field: string) => values.get(`${field}:`) ?? 0;
  const tokens = value("total_tokens"), cost = value("cost_usd"), uncached = value("uncached_input_tokens");
  const reads = value("cache_read_input_tokens"), output = value("output_tokens"), sessions = values.get("agent_sessions") ?? 0;
  const cacheHit = uncached + reads > 0 ? `${Math.round(reads / (uncached + reads) * 100)}%` : "—";
  const card = (label: string, amount: string, detail: string) => <div className="mcp-metric"><span>{label}</span><strong>{amount}</strong><small>{detail}</small></div>;
  return <div className="mcp-metrics usage-cards token-summary" aria-label="Machine token summary" aria-busy={result.loading}>
    {card("Total tokens", result.loading ? "Loading…" : result.error ? "Unavailable" : compactNumber(tokens), "all reported input and output")}
    {card("API price equivalent", result.loading ? "Loading…" : result.error ? "Unavailable" : formatUSD(cost), "list-price estimate at the day’s rates")}
    {card("Cache hit rate", result.loading ? "Loading…" : result.error ? "Unavailable" : cacheHit, `${compactNumber(reads)} cached input tokens read`)}
    {card("Output tokens", result.loading ? "Loading…" : result.error ? "Unavailable" : compactNumber(output), "including reported reasoning output")}
    {card("Agent sessions", result.loading ? "Loading…" : result.error ? "Unavailable" : sessions.toLocaleString(), "distinct sessions in the current filters")}
  </div>;
}

// Writing categories as the page reports them. Each group lists the
// message_authorship categories it combines (see docs/human-authorship.md).
const writingGroups = [
  { key: "typed", label: "Typed", description: "Written or dictated by you", categories: ["typed"], color: "var(--sea)" },
  { key: "pasted", label: "Likely pasted", description: "Code, logs, tables, agent-style formatting, or sent faster than typing", categories: ["pasted"], color: "var(--heat-1)" },
  { key: "copied", label: "Copied or re-sent", description: "Matches agent output from the previous 48 hours, or anything you sent before", categories: ["quoted", "resent"], color: "var(--gold)" },
  { key: "prompts", label: "Templates and attachments", description: "Repeated one-click prompts, slash commands, attachment references", categories: ["template", "attachment"], color: "var(--heat-2)" },
  { key: "machine", label: "Harness and automation", description: "Harness instructions and prompts sent by scripts or other agents", categories: ["harness", "automated"], color: "var(--muted)" },
];
type WritingCounts = Record<string, number | string | null>;
type WritingSeries = { works: number; daily: WritingCounts[]; totals: WritingCounts };
type AuthorshipStatus = { built_at: string | null; running: boolean; stale: boolean; error: string | null };
const writingViewKey = "pharos-writing-view";
const groupTotal = (counts: WritingCounts, group: typeof writingGroups[number], unit: "words" | "chars") => group.categories.reduce((sum, category) => sum + (Number(counts[`${category}_${unit}`]) || 0), 0);

// Tracks the background classification, polling while it runs, and reports
// each finished build so the page can reload.
function useAuthorshipStatus(onBuilt: () => void): AuthorshipStatus | null {
  const [status, setStatus] = useState<AuthorshipStatus | null>(null);
  const built = useRef<string | null | undefined>(undefined);
  useEffect(() => {
    let timer = 0, live = true;
    const load = async () => {
      try {
        const next = await responseJSON<AuthorshipStatus>(await fetch("/api/authorship"));
        if (!live) return;
        setStatus(next);
        if (built.current !== undefined && next.built_at !== built.current) onBuilt();
        built.current = next.built_at;
        if (next.running) timer = window.setTimeout(load, 5000);
      } catch { /* the status line stays as it was */ }
    };
    void load();
    window.addEventListener("pharos:usage-refresh", load);
    return () => { live = false; window.clearTimeout(timer); window.removeEventListener("pharos:usage-refresh", load); };
  }, []);
  return status;
}

type WritingSplit = "kind" | "repository_name" | "source_kind" | "providers";
const writingSplits: Array<[WritingSplit, string]> = [["kind", "Kind"], ["repository_name", "Repository"], ["source_kind", "App"], ["providers", "Provider"]];
type WritingSplitDay = { day: string; value: string | null; typed_words: number; words: number };

function writingSplitLabel(split: WritingSplit, value: string): string {
  if (!value) return split === "repository_name" ? "No repository" : "Unknown";
  const labels = (field: string) => optionLabels(schemas.writing.fields).get(field);
  if (split === "source_kind") return labels("source_kind")?.get(value) ?? value;
  if (split === "providers") return value.split(",").map(provider => labels("provider")?.get(provider) ?? optionLabels(schemas.usage.fields).get("provider")?.get(provider) ?? provider).join(" + ");
  return value;
}

// Fetches the writing series for body, or nothing without one, again when
// either changes or authorship is rebuilt.
function useWritingSeries(body: object | null, reload: number) {
  const key = body ? JSON.stringify(body) : "";
  const [data, setData] = useState<{ key: string; series: (WritingSeries & { split?: WritingSplitDay[] }) | null; error: string; loading: boolean }>({ key: "", series: null, error: "", loading: true });
  useEffect(() => {
    if (!key) return;
    const controller = new AbortController();
    setData(previous => ({ ...previous, loading: true }));
    fetch("/api/query/writing/series", { method: "POST", headers: { "Content-Type": "application/json" }, body: key, signal: controller.signal })
      .then(responseJSON<WritingSeries & { split?: WritingSplitDay[] }>)
      .then(series => setData({ key, series, error: "", loading: false }))
      .catch(failure => { if (!controller.signal.aborted) setData({ key, series: null, error: failure instanceof Error ? failure.message : String(failure), loading: false }); });
    return () => controller.abort();
  }, [key, reload]);
  return data;
}

// The chart and totals cover only the days inside the time filter. Split by
// a row field, the key filters the table as on the tokens chart; the input
// kinds are not rows, so clicking one only narrows the chart.
function WritingPanel({ where, onFilter, status, reload }: { where: WhereTerm[]; onFilter: FilterChange; status: AuthorshipStatus | null; reload: number }) {
  const [view, setView] = useState(() => readStored(writingViewKey, { period: "week" as ChartPeriod, scope: "typed" as "typed" | "all", split: "kind" as WritingSplit }, value => value.period in chartPeriods && ["typed", "all"].includes(value.scope) && writingSplits.some(([option]) => option === value.split)));
  const update = (next: Partial<typeof view>) => setView(previous => { const merged = { ...previous, ...next }; store(writingViewKey, merged); return merged; });
  const [kinds, setKinds] = useState<string[]>([]);
  const [seriesLimit, setSeriesLimit] = useState(5);
  const bounds = timeBounds(where, writingTime);
  const window = { from: bounds.from ? localDay(bounds.from) : undefined, to: bounds.to ? localDay(new Date(bounds.to.getTime() - 1)) : undefined };
  const split = view.split === "kind" ? null : view.split;
  useEffect(() => setSeriesLimit(5), [split]);
  const chosen = split ? sliceValues(where, split).map(value => value ?? "") : kinds;
  const chartWhere = split ? where.filter(term => !isSliceTerm(term, split)) : where;
  // Without a key filter, one request answers both the totals and the chart.
  const separate = chartWhere.length !== where.length;
  const data = useWritingSeries({ where, ...window, ...(split && !separate ? { split } : {}) }, reload);
  const chart = useWritingSeries(separate ? { where: chartWhere, split, ...window } : null, reload);
  const chartData = separate ? chart : data;
  const series = data.series, totals = series?.totals ?? {};
  const groups = view.scope === "typed" ? writingGroups.slice(0, 1) : writingGroups;
  const { chartSeries, buckets } = useMemo(() => {
    const days = split ? (chartData.series?.split ?? []).map(entry => entry.day) : (series?.daily ?? []).map(day => day.day);
    const buckets = periodBuckets(view.period, ...chartSpan(bounds, earliestKey(days))), byKey = new Map(buckets.map(bucket => [bucket.key, bucket])), messages = new Map<string, number>();
    const bucketOf = (day: unknown) => periodKey(parseDay(String(day)), view.period);
    for (const day of series?.daily ?? []) {
      const bucket = byKey.get(bucketOf(day.day));
      if (!bucket) continue;
      messages.set(bucket.key, (messages.get(bucket.key) ?? 0) + (Number(day.typed_messages) || 0));
      if (!split) for (const group of writingGroups) bucket.values[group.key] = (bucket.values[group.key] ?? 0) + groupTotal(day, group, "words");
    }
    for (const bucket of buckets) bucket.detail = `${(messages.get(bucket.key) ?? 0).toLocaleString()} messages with typed text`;
    if (!split) return { chartSeries: groups.map(group => ({ key: group.key, label: group.label, color: group.color, value: group.key })), buckets };
    const rows = (chartData.series?.split ?? []).map(entry => [bucketOf(entry.day), entry.value ?? "", Number(view.scope === "typed" ? entry.typed_words : entry.words) || 0] as [string, string, number]);
    return { chartSeries: foldSeries(rows, byKey, name => writingSplitLabel(split, name), undefined, chosen, seriesLimit), buckets };
  }, [series, chartData.series, split, view.scope, view.period, bounds.from?.getTime(), bounds.to?.getTime(), chosen.join("\u0000"), seriesLimit]);
  const selected = chartSeries.filter(item => item.value !== undefined && chosen.includes(item.value)).map(item => item.key);
  function select(key: string | null, additive: boolean) {
    const value = key === null ? undefined : chartSeries.find(item => item.key === key)?.value;
    if (!split) setKinds(current => value === undefined ? [] : nextSlice(current, value, additive));
    else onFilter(current => withSlice(current, split, value === undefined ? [] : nextSlice(sliceValues(current, split), value || null, additive)));
  }
  const typed = Number(totals.typed_words) || 0, pasted = Number(totals.pasted_words) || 0, typedMessages = Number(totals.typed_messages) || 0;
  const since = localDay(new Date(Date.now() - 29 * 86_400_000));
  const recent = (series?.daily ?? []).filter(day => String(day.day) >= since);
  const recentTyped = recent.reduce((sum, day) => sum + (Number(day.typed_words) || 0), 0), recentMessages = recent.reduce((sum, day) => sum + (Number(day.typed_messages) || 0), 0);
  const building = status !== null && !status.built_at;
  const card = (label: string, value: string, detail: string) => <div className="mcp-metric"><span>{label}</span><strong>{value}</strong><small>{detail}</small></div>;
  const unit = chartPeriods[view.period].unit;
  const allChars = writingGroups.reduce((sum, group) => sum + groupTotal(totals, group, "chars"), 0);
  return <section className="writing-panel" aria-label="Human Words">
    {data.error ? <div className="query-table-error">{data.error}</div> : null}
    <div className="mcp-metrics usage-cards">
      {building ? card("Words you wrote", "Building…", "Classifying every retained user message") : <>
        {card("Words you wrote", compactNumber(typed), `Up to ${compactNumber(typed + pasted)} including text that only looks pasted${totals.first_day ? ` · since ${totals.first_day}` : ""}`)}
        {card("Last 30 days", compactNumber(recentTyped), `${recentMessages.toLocaleString()} messages with typed text`)}
        {card("Messages with typed text", typedMessages.toLocaleString(), `of ${(Number(totals.messages) || 0).toLocaleString()} user turns in ${(series?.works ?? 0).toLocaleString()} conversations`)}
        {card("Words per message", typedMessages ? Math.round(typed / typedMessages).toLocaleString() : "—", "average typed words, where any were typed")}
        {card("Novels", (typed / 90000).toFixed(1), "at 90,000 words each")}
      </>}
    </div>
    <StackedColumns title={`${view.scope === "typed" ? "Typed words" : "Words of user input"} per ${unit}${split ? ` by ${writingSplits.find(([value]) => value === split)?.[1].toLowerCase()}` : ""}`}
      controls={<>
        <Segmented label="Show" value={view.scope} options={[["typed", "Typed"], ["all", "All input"]]} onChange={scope => update({ scope })} />
        <Segmented label="Split" value={view.split} options={writingSplits} onChange={next => update({ split: next })} />
        <Segmented label="By" value={view.period} options={periodOptions} onChange={period => update({ period })} />
        <RangeControl bounds={bounds} onChange={from => onFilter(current => withTimeRange(current, writingTime, from))} />
      </>}
      series={chartSeries} buckets={buckets} period={view.period} legend={Boolean(split) && chartSeries.length > 0}
      format={value => value.toLocaleString()} tickFormat={compactNumber} noun="words" loading={chartData.loading} error={chartData.error}
      selected={selected} onSelect={select} onExpand={() => setSeriesLimit(limit => limit + 5)} onRange={pickRange(onFilter, writingTime, view.period, period => update({ period }))} />
    <div className="writing-mix">
      <h3>Where user-turn text came from</h3>
      {!allChars ? <p className="muted">{data.loading ? "Loading…" : "No classified user messages match these filters."}</p> : <>
        <div className="writing-stack" role="img" aria-label={writingGroups.map(group => `${group.label} ${Math.round(100 * groupTotal(totals, group, "chars") / allChars)}%`).join(", ")}>
          {writingGroups.map(group => { const value = groupTotal(totals, group, "chars"); return value ? <i key={group.key} style={{ flex: value, background: group.color }} title={`${group.label}: ${Math.round(100 * value / allChars)}%`} /> : null; })}
        </div>
        <table className="writing-table">
          <thead><tr><th>Source</th><th className="num">Words</th><th className="num">Characters</th><th className="num">Share of text</th></tr></thead>
          <tbody>{writingGroups.map(group => { const chars = groupTotal(totals, group, "chars"); return <tr key={group.key}>
            <td><span className="usage-swatch" style={{ background: group.color }} /><strong>{group.label}</strong><div className="muted">{group.description}</div></td>
            <td className="num">{groupTotal(totals, group, "words").toLocaleString()}</td><td className="num">{chars.toLocaleString()}</td><td className="num">{(100 * chars / allChars).toFixed(1)}%</td>
          </tr>; })}</tbody>
        </table>
      </>}
    </div>
  </section>;
}

// Human Words lists conversations or single messages (?writing=messages), and
// messages as a table or as highlighted text (?messages=text), like the
// Library's Table and Conversations views.
type WritingRows = "conversations" | "messages";
const writingRows = (): WritingRows => new URLSearchParams(location.search).get("writing") === "messages" ? "messages" : "conversations";
const messageView = (): MessageView => new URLSearchParams(location.search).get("messages") === "text" ? "text" : "table";

function WritingUsage({ onStatus }: { onStatus: (status: AuthorshipStatus | null) => void }) {
  const [reload, setReload] = useState(0);
  const status = useAuthorshipStatus(() => { setReload(value => value + 1); tableApis.get("writing")?.refresh(); tableApis.get("writing_messages")?.refresh(); });
  useEffect(() => onStatus(status), [status]);
  const [rows, setRows] = useState<WritingRows>(writingRows);
  const [view, setView] = useState<MessageView>(messageView);
  useEffect(() => {
    const restore = () => { setRows(writingRows()); setView(messageView()); };
    window.addEventListener("pharos:route", restore);
    return () => window.removeEventListener("pharos:route", restore);
  }, []);
  function chooseRows(next: WritingRows) { setRows(next); updateURI("writing", next === "messages" ? next : ""); }
  function chooseView(next: MessageView) { setView(next); updateURI("messages", next === "text" ? next : ""); }
  // A conversation's Messages button lists the messages counted toward it;
  // the message table starts from the query in the URL.
  function drill(row: Row) {
    const where: WhereTerm[] = [{ field: "work_id", op: "=", value: String(row.id) }];
    updateURI(queryParameter("writing_messages"), encodeQuery({ ...EMPTY_QUERY, limit: schemas.writing_messages.defaultLimit ?? EMPTY_QUERY.limit, where }));
    chooseRows("messages");
  }
  function apply(dataset: Dataset, preset: UsagePreset | null) {
    tableApis.get(dataset)?.setQuery(previous => ({ ...previous, ...(preset?.orderBy ? { orderBy: preset.orderBy } : { aggregations: preset?.aggregations ?? [] }), offset: 0 }));
  }
  const toggle = <div className="writing-rows-bar">
    <Segmented label="List" value={rows} options={[["conversations", "Conversations"], ["messages", "Messages"]]} onChange={chooseRows} />
    {rows === "messages" ? <div className="library-view-toggle" role="group" aria-label="Message result view">
      <button type="button" className={view === "table" ? "active" : ""} aria-pressed={view === "table"} onClick={() => chooseView("table")}>Table</button>
      <button type="button" className={view === "text" ? "active" : ""} aria-pressed={view === "text"} onClick={() => chooseView("text")}>Highlighted text</button>
    </div> : null}
  </div>;
  if (rows === "messages") return <QuerySurface key="messages" dataset="writing_messages" messageView={view} header={api => <>
    {toggle}
    <MessageMix where={toAggregationQuery(api.query, schemas.writing_messages).where} reload={reload} onFilter={filterChange(api)} />
    <div className="usage-preset-rows">
      <PresetRow label="Sort" presets={messagePresets} onApply={preset => apply("writing_messages", preset)} />
      <PresetRow label="Metrics" presets={messageMetricPresets} onApply={preset => apply("writing_messages", preset)} clear />
    </div>
  </>} />;
  return <QuerySurface key="conversations" dataset="writing" trailing={row => <button type="button" className="mcp-detail-button" title="List this conversation's messages, highlighted by where their text came from" onClick={() => drill(row)}>Messages</button>} header={api => <>
    <WritingPanel where={toAggregationQuery(api.query, schemas.writing).where} onFilter={filterChange(api)} status={status} reload={reload} />
    {toggle}
    <div className="usage-preset-rows">
      <PresetRow label="Sort" presets={writingPresets} onApply={preset => apply("writing", preset)} />
      <PresetRow label="Metrics" presets={writingMetricPresets} onApply={preset => apply("writing", preset)} clear />
    </div>
  </>} />;
}

type UsageView = "tokens" | "writing" | "carbon";
const usageViewKey = "pharos-usage-view";

// ?usage= picks the view; otherwise the last choice is kept.
function initialUsageView(): UsageView {
  // Human Words and Carbon Impact read the live library.
  if (shared) return "tokens";
  const requested = new URLSearchParams(location.search).get("usage");
  if (requested === "tokens" || requested === "writing" || requested === "carbon") { store(usageViewKey, requested); return requested; }
  const saved = preferences().get<unknown>(usageViewKey, "tokens");
  return saved === "writing" || saved === "carbon" ? saved : "tokens";
}

function CarbonImpact() {
  useEffect(() => {
    const refresh = () => { void window.pharosCarbon?.refresh(); };
    refresh();
    window.addEventListener("pharos:usage-refresh", refresh);
    return () => window.removeEventListener("pharos:usage-refresh", refresh);
  }, []);
  return <section aria-label="Carbon Impact" data-feedback-label="Carbon Impact">
    <div id="carbonCard" className="carbon-card" aria-busy="true" />
  </section>;
}

function UsagePage() {
  const [view, setView] = useState<UsageView>(initialUsageView);
  const [status, setStatus] = useState<AuthorshipStatus | null>(null);
  useEffect(() => {
    const restore = () => { if (location.pathname === "/usage") setView(initialUsageView()); };
    window.addEventListener("pharos:route", restore);
    return () => window.removeEventListener("pharos:route", restore);
  }, []);
  function choose(next: UsageView) {
    setView(next); store(usageViewKey, next); updateURI("usage", next === "writing" ? next : "");
    if (next !== "writing") { updateURI("writing", ""); updateURI("messages", ""); }
  }
  const statusText = !status ? "" : status.error ? `Last update failed: ${status.error}` : status.running ? "Updating…" : status.built_at ? `Updated ${new Date(status.built_at).toLocaleString()}` : "Waiting to build";
  return <div className="pharos-query-page usage-page">
    <div className="view-heading library-heading usage-heading">
      <div><h1>Usage</h1><p className="muted">{view === "tokens"
        ? "Reconciled tokens per agent session, day, and model. Input includes cached input; filter to any provider, model, or repository and the chart follows. Cost is the API list-price equivalent on the day of use, at standard rates. It ignores long-context premiums and subscriptions, so treat it as a lower bound. ≈ marks costs priced by assumption."
        : view === "writing"
          ? <>Text you typed or dictated into agent chats, per conversation or per message. Harness instructions, one-click prompts, attachments, pastes, and copied agent output are counted separately; filter the table and the chart and breakdown follow. {statusText ? <span className="meta">{statusText}</span> : null}</>
          : "Estimated inference electricity and CO₂e for the tokens in this library, using published research and explicit assumptions."}</p></div>
      <div className="usage-heading-actions">
        {shared ? null : <div className="library-view-toggle" role="group" aria-label="Usage view">
          <button type="button" className={view === "writing" ? "active" : ""} aria-pressed={view === "writing"} onClick={() => choose("writing")}>Human Words</button>
          <button type="button" className={view === "tokens" ? "active" : ""} aria-pressed={view === "tokens"} onClick={() => choose("tokens")}>Machine Tokens</button>
          <button type="button" className={view === "carbon" ? "active" : ""} aria-pressed={view === "carbon"} onClick={() => choose("carbon")}>Carbon Impact</button>
        </div>}
        {view === "tokens" && !shared ? <RefreshPricesButton /> : null}
      </div>
    </div>
    {view === "tokens" ? <TokenUsage /> : view === "writing" ? <WritingUsage onStatus={setStatus} /> : <CarbonImpact />}
  </div>;
}

type MCPStatus = { enabled: boolean; transport: string; command: string; args: string[]; install_location?: string; note: string; skill?: { name: string; text: string; paths?: Record<string, string> } };
type MCPCall = { id: number; called_at: string; tool_name: string; arguments_json: string; status: string; error_text: string | null; duration_ms: number; response_bytes: number; estimated_output_tokens: number; result_count: number | null; truncated: boolean };
type MCPHistory = { stats: { total_calls: number; failed_calls: number; average_output_tokens: number; largest_output_tokens: number; truncated_calls: number } };

const mcpMetrics = [
  { label: "By tool", aggregations: [sum("call_count", ["tool_name"], "Calls by tool"), sum("error_count", ["tool_name"], "Errors by tool"), sum("estimated_output_tokens", ["tool_name"], "Output tokens by tool")] },
  { label: "By day", aggregations: [sum("call_count", ["day"], "Calls per day"), sum("error_count", ["day"], "Errors per day"), sum("estimated_output_tokens", ["day"], "Output tokens per day")] },
  { label: "Failures", aggregations: [sum("error_count", ["tool_name", "status"], "Failures by tool and status")] },
  { label: "Token cost", aggregations: [sum("estimated_output_tokens", ["tool_name"], "Output tokens by tool"), sum("truncated_count", ["tool_name"], "Truncated calls by tool")] },
];

function MCPPage() {
  const [status, setStatus] = useState<MCPStatus | null>(null);
  const [history, setHistory] = useState<MCPHistory | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState("");
  const [selectedCall, setSelectedCall] = useState<MCPCall | null>(null);
  async function refresh() {
    try {
      const [nextStatus, nextHistory] = await Promise.all([
        fetch("/api/mcp").then(responseJSON<MCPStatus>),
        fetch("/api/mcp/calls?limit=1").then(responseJSON<MCPHistory>),
      ]);
      setStatus(nextStatus); setHistory(nextHistory); setError("");
    } catch (failure) { setError(failure instanceof Error ? failure.message : "MCP status unavailable"); }
  }

  useEffect(() => {
    void refresh();
    const onRoute = () => { if (location.pathname === "/mcp") void refresh(); };
    const timer = window.setInterval(() => { if (document.querySelector("#mcp.active")) void refresh(); }, 5000);
    window.addEventListener("pharos:route", onRoute);
    return () => { window.clearInterval(timer); window.removeEventListener("pharos:route", onRoute); };
  }, []);

  async function toggle() {
    if (!status) return;
    setBusy(true);
    try {
      const next = await responseJSON<{ enabled: boolean }>(await fetch("/api/mcp/enabled", {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled: !status.enabled }),
      }));
      setStatus({ ...status, enabled: next.enabled }); setError("");
    } catch (failure) { setError(failure instanceof Error ? failure.message : "Could not update MCP"); }
    finally { setBusy(false); }
  }

  async function copy(label: string, value: string) {
    try { if (window.pharosCopyText) await window.pharosCopyText(value); else await navigator.clipboard.writeText(value); setCopied(label); window.setTimeout(() => setCopied(""), 2000); }
    catch { setError("Could not copy to clipboard. Select the text and copy it manually."); }
  }

  function applyMetricPreset(aggregations: AggregationClause[] | null) {
    tableApis.get("mcp_calls")?.setQuery(previous => ({ ...previous, aggregations: aggregations ?? [], offset: 0 }));
  }

  const connection = status ? JSON.stringify({ mcpServers: { pharos: { command: status.command, args: status.args } } }, null, 2) : "";
  const prompt = status ? `Add Pharos as a local stdio MCP server. Use search_conversations first, with a small limit and max_output_tokens budget. Open get_conversation_overview for promising results, search_conversation_passages for a specific topic, and get_conversation_messages only around cited message IDs. Treat previews as leads and inspect the cited evidence before relying on an outcome. Pharos is read-only. If its tools are unavailable, enable MCP in the Pharos app and reconnect this agent client. Use list_findings and get_finding for recurring problems Pharos found in past work; reading them never starts a measurement.\n\nConnection definition:\n${connection}` : "";
  const stats = history?.stats;
  return <div className="mcp-page pharos-query-page">
    <div className="view-heading"><div><h1>MCP</h1><p className="muted">Let local agents search past conversations in small steps. Review calls to spot oversized responses and failed queries.</p></div><button type="button" className="mcp-refresh" onClick={() => { void refresh(); tableApis.get("mcp_calls")?.refresh(); }}>Refresh</button></div>
    {error ? <div className="query-table-error" role="alert">{error}</div> : null}
    <section className="mcp-card mcp-status-card" aria-label="MCP availability">
      <div><span className={`mcp-status ${status?.enabled ? "enabled" : "disabled"}`}>{status ? status.enabled ? "Available" : "Off" : "Loading"}</span><h2>Agent access</h2><p className="muted">{status?.note ?? "Loading connection settings…"}</p>{status?.install_location ? <p className="muted">Pharos installation: <code>{status.install_location}</code></p> : null}</div>
      <button type="button" className={`toggle ${status?.enabled ? "on" : ""}`} role="switch" aria-checked={Boolean(status?.enabled)} aria-label="Enable MCP" disabled={!status || busy} onClick={() => void toggle()} />
    </section>
    <section className="mcp-history" aria-label="MCP call history">
      <div className="mcp-card-heading"><div><h2>Call history</h2><p className="muted">Newest first by default. Token counts are estimates from response size; no response text is stored. The latest 5,000 calls are retained. Summary cards cover all retained calls; table filters and metrics apply below.</p></div></div>
      <div className="mcp-metrics">
        <div className="mcp-metric"><span>Calls</span><strong>{stats?.total_calls?.toLocaleString() ?? "—"}</strong></div>
        <div className="mcp-metric"><span>Errors</span><strong>{stats?.failed_calls?.toLocaleString() ?? "—"}</strong></div>
        <div className="mcp-metric"><span>Average output</span><strong>{stats ? `${compactNumber(stats.average_output_tokens)} est. tokens` : "—"}</strong></div>
        <div className="mcp-metric"><span>Largest output</span><strong>{stats ? `${compactNumber(stats.largest_output_tokens)} est. tokens` : "—"}</strong></div>
        <div className="mcp-metric"><span>Truncated</span><strong>{stats?.truncated_calls?.toLocaleString() ?? "—"}</strong></div>
      </div>
      <div className="usage-presets" role="group" aria-label="MCP metric presets">
        {mcpMetrics.map(preset => <button key={preset.label} type="button" onClick={() => applyMetricPreset(preset.aggregations)}>{preset.label}</button>)}
        <button type="button" className="usage-preset-clear" onClick={() => applyMetricPreset(null)}>Clear metrics</button>
      </div>
      <QuerySurface dataset="mcp_calls" trailing={row => <button type="button" className="mcp-detail-button" onClick={() => setSelectedCall(row as MCPCall)}>Details</button>} />
    </section>
    <div className="mcp-setup-grid">
      <section className="mcp-card"><div className="mcp-card-heading"><div><h2>Connection</h2><p className="muted">Add this stdio server in an agent client’s MCP settings. The client launches Pharos locally.</p></div><button type="button" disabled={!status} onClick={() => void copy("connection", connection)}>{copied === "connection" ? "Copied" : "Copy JSON"}</button></div><pre className="mcp-code"><code>{connection || "Loading…"}</code></pre><p className="mcp-fineprint">Client settings formats vary. Use the command and args shown here if your client does not accept this JSON shape.</p></section>
      <section className="mcp-card"><div className="mcp-card-heading"><div><h2>Prompt for an agent</h2><p className="muted">Paste this when asking an agent to add Pharos and use it efficiently.</p></div><button type="button" disabled={!status} onClick={() => void copy("prompt", prompt)}>{copied === "prompt" ? "Copied" : "Copy prompt"}</button></div><pre className="mcp-prompt">{prompt || "Loading…"}</pre></section>
      {status?.skill?.text ? <section className="mcp-card mcp-skill-card"><div className="mcp-card-heading"><div><h2>Skill for agents</h2><p className="muted">A short skill that has an agent read this repository’s findings, confirm them, and propose the smallest fixes. Save it as:</p></div><button type="button" onClick={() => void copy("skill", status.skill?.text ?? "")}>{copied === "skill" ? "Copied" : "Copy skill"}</button></div>
        <ul className="mcp-skill-paths">{Object.entries(status.skill.paths ?? {}).map(([harness, where]) => <li key={harness}><span>{harness === "claude" ? "Claude Code" : harness === "codex" ? "Codex" : harness}</span><code>{where}</code></li>)}</ul>
        <pre className="mcp-prompt">{status.skill.text}</pre></section> : null}
    </div>
    {selectedCall ? <div className="mcp-dialog-backdrop" onClick={() => setSelectedCall(null)}><div className="mcp-dialog" role="dialog" aria-modal="true" aria-label="MCP call details" onClick={event => event.stopPropagation()}>
      <div className="mcp-card-heading"><div><h2>{selectedCall.tool_name}</h2><p className="muted">{new Date(selectedCall.called_at).toLocaleString()} · {selectedCall.status}</p></div><button type="button" onClick={() => setSelectedCall(null)}>Close</button></div>
      <p className="muted">{compactNumber(selectedCall.estimated_output_tokens)} estimated output tokens · {selectedCall.response_bytes.toLocaleString()} bytes · {selectedCall.duration_ms.toLocaleString()} ms{selectedCall.result_count === null ? "" : ` · ${selectedCall.result_count} results`}{selectedCall.truncated ? " · truncated" : ""}</p>
      <h3>Sanitized arguments</h3><pre className="mcp-code">{selectedCall.arguments_json}</pre>
      {selectedCall.error_text ? <><h3>Error</h3><pre className="mcp-code badtext">{selectedCall.error_text}</pre></> : null}
    </div></div> : null}
  </div>;
}

type ToolPreset = { label: string; title: string; aggregations?: AggregationClause[]; where?: WhereTerm[]; orderBy?: OrderByClause[] };
type ToolLedgerStatus = { conversations: number; current_conversations: number; pending_conversations: number; tool_calls: number; backfill: { running: boolean; done: number; total: number; error: string | null } };
const count = (groupBy: string[], label: string): AggregationClause => ({ id: `count:${groupBy.join(",")}`, op: "count", groupBy, label });
const shellCalls: WhereTerm[] = [{ field: "tool_category", op: "=", value: "command" }];

// Summary presets group the daily rollup; call presets explore individual calls.
const toolSummaryPresets: Array<{ group: string; presets: ToolPreset[] }> = [
  { group: "Errors", presets: [
    { label: "Error rate by tool", title: "Errors and calls per tool; divide for the rate", aggregations: [sum("error_count", ["tool_name"], "Errors by tool"), sum("call_count", ["tool_name"], "Calls by tool")] },
    { label: "Failure kinds", title: "Why calls failed: rejected, interrupted, timed out, non-zero exit, blocked by hook, or no result", aggregations: [
      sum("rejected_count", [], "Rejected by user"), sum("interrupted_count", [], "Interrupted"), sum("timeout_count", [], "Timed out"),
      sum("nonzero_exit_count", [], "Non-zero exit"), sum("hook_blocked_count", [], "Blocked by hook"), sum("no_result_count", [], "No result"),
    ] },
    { label: "Failing commands", title: "Non-zero exits by shell program and subcommand", where: shellCalls, aggregations: [sum("nonzero_exit_count", ["command_name"], "Non-zero exits by command"), sum("call_count", ["command_name"], "Runs by command")] },
    { label: "Errors by model", title: "Errors and calls by model", aggregations: [sum("error_count", ["model_family"], "Errors by model"), sum("call_count", ["model_family"], "Calls by model")] },
  ] },
  { group: "Commands", presets: [
    { label: "Top programs", title: "Shell calls by program (git, cat, go, rg, …)", where: shellCalls, aggregations: [sum("call_count", ["program"], "Shell calls by program")] },
    { label: "Subcommands", title: "Shell calls by program and subcommand (git status, go test, npm run build, …)", where: shellCalls, aggregations: [sum("call_count", ["command_name"], "Shell calls by command"), sum("total_duration_ms", ["command_name"], "Time by command")] },
    { label: "Command categories", title: "Shell calls and time by kind of command", where: shellCalls, aggregations: [sum("call_count", ["command_category"], "Calls by command category"), sum("total_duration_ms", ["command_category"], "Time by command category")] },
    { label: "Shell instead of tools", title: "Shell calls that read, search, or list files, which dedicated tools could do", where: [...shellCalls, { any: ["read", "search", "list"].map(value => ({ field: "command_category", op: "=" as const, value })) }], aggregations: [sum("call_count", ["program"], "Read/search/list via shell")] },
  ] },
  { group: "Time", presets: [
    { label: "Slowest tools", title: "Total and longest duration per tool", aggregations: [sum("total_duration_ms", ["tool_name"], "Total time by tool"), { id: "max:max_duration_ms:tool_name", op: "max", field: "max_duration_ms", groupBy: ["tool_name"], label: "Longest call by tool" }] },
    { label: "Tests & builds", title: "Time spent in test and build commands per week", where: [{ any: ["test", "build"].map(value => ({ field: "command_category", op: "=" as const, value })) }], aggregations: [sum("total_duration_ms", ["week", "command_category"], "Test/build time per week"), sum("call_count", ["week", "command_category"], "Test/build runs per week")] },
    { label: "Calls per day", title: "Tool calls per day by category", aggregations: [sum("call_count", ["day", "tool_category"], "Calls per day by category")] },
  ] },
  { group: "Context", presets: [
    { label: "Context added", title: "Tokens tool results added to the context, by tool and by shell program", aggregations: [sum("result_tokens", ["tool_name"], "Context added by tool"), sum("result_tokens", ["program"], "Context added by program")] },
    { label: "Context carried", title: "Result tokens re-read by later requests until compaction", aggregations: [sum("carried_tokens", ["tool_name"], "Context carried by tool"), sum("carried_tokens", ["program"], "Context carried by program")] },
    { label: "Cost by tool", title: "API-equivalent cost of tool results and the output that requested them", aggregations: [sum("tool_cost_usd", ["tool_name"], "Cost by tool"), sum("context_cost_usd", ["tool_category"], "Context cost by category")] },
    { label: "MCP servers", title: "MCP calls, errors, and context by server", where: [{ field: "mcp_server", op: "is_not_null", value: "" }], aggregations: [sum("call_count", ["mcp_server"], "Calls by MCP server"), sum("error_count", ["mcp_server"], "Errors by MCP server"), sum("result_tokens", ["mcp_server"], "Context added by MCP server")] },
    { label: "Subagents", title: "Tool use by root agents versus subagents", aggregations: [sum("call_count", ["session_kind", "tool_category"], "Calls by agent kind")] },
  ] },
];

const toolCallPresets: ToolPreset[] = [
  { label: "Calls by day", title: "Tool call volume and errors over time", aggregations: [count(["day"], "Calls per day"), sum("error_count", ["day"], "Errors per day")] },
  { label: "Calls by week", title: "Weekly tool call volume by category", aggregations: [count(["week", "tool_category"], "Calls per week by category")] },
  { label: "Calls by tool", title: "Tool call volume by tool", aggregations: [count(["tool_name"], "Calls by tool")] },
  { label: "Slowest calls", title: "Single calls by duration", orderBy: [{ field: "duration_ms", dir: "desc" }] },
  { label: "Largest results", title: "Single calls by tokens their result added to the context", orderBy: [{ field: "result_tokens", dir: "desc" }] },
  { label: "Most carried", title: "Results re-read the most before compaction", orderBy: [{ field: "carried_tokens", dir: "desc" }] },
  { label: "Failures", title: "Failed calls, newest first, with a breakdown by error type", where: [{ field: "status", op: "=", value: "error" }], orderBy: [{ field: "started_at", dir: "desc" }], aggregations: [count(["error_type"], "Failures by type")] },
  { label: "Failures by signature", title: "Recurring failures grouped by normalized signature", where: [{ field: "error_signature", op: "is_not_null", value: "" }], aggregations: [count(["error_signature"], "Failures by signature")] },
  { label: "Test failures", title: "Test commands whose results contain failures", where: [{ field: "test_failure", op: "=", value: "true" }], orderBy: [{ field: "started_at", dir: "desc" }] },
  { label: "Exit codes", title: "Non-zero exit codes by command", where: [{ field: "exit_code", op: "!=", value: "0" }, { field: "exit_code", op: "is_not_null", value: "" }], aggregations: [count(["command_name", "exit_code"], "Exit codes by command")] },
  { label: "Rejected", title: "Calls a person declined", where: [{ field: "error_type", op: "=", value: "user_rejected" }], orderBy: [{ field: "started_at", dir: "desc" }], aggregations: [count(["tool_name"], "Rejections by tool")] },
  { label: "Sites", title: "Calls that reached a URL (web fetches, browser navigation, curl and other network commands), by site and tool", where: [{ field: "host", op: "is_not_null", value: "" }], orderBy: [{ field: "started_at", dir: "desc" }], aggregations: [count(["host"], "Calls by site"), count(["host", "tool_name"], "Calls by site and tool")] },
  { label: "Web searches", title: "Web searches, newest first, by tool", where: [{ field: "search_query", op: "is_not_null", value: "" }], orderBy: [{ field: "started_at", dir: "desc" }], aggregations: [count(["tool_name"], "Searches by tool")] },
];

// Filters that select the calls behind one summary row.
function drillFilters(row: Row): WhereTerm[] {
  const where: WhereTerm[] = [];
  for (const field of ["day", "tool_name", "program", "subcommand", "provider", "model", "repository_name", "session_kind", "source_kind"]) {
    const value = row[field];
    where.push(value === null || value === undefined || value === "" ? { field, op: "is_null", value: "" } : { field, op: "=", value: String(value) });
  }
  return where;
}

function formatDurationMS(value: unknown): string {
  const ms = Number(value);
  if (value === null || value === undefined || !Number.isFinite(ms)) return "—";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
  return `${Math.floor(ms / 60_000)}m ${Math.round((ms % 60_000) / 1000)}s`;
}

const urlSourceLabels: Record<string, string> = { input: "Tool argument", command: "Shell command", result: "Opened", search_result: "Search result" };

// Reached URLs first; the links a search returned follow, since the agent may not have opened them.
function ToolCallURLs({ urls }: { urls: Row[] }) {
  const reached = urls.filter(entry => entry.source !== "search_result"), results = urls.filter(entry => entry.source === "search_result");
  const table = (rows: Row[]) => <div className="mcp-tool-table-wrap"><table className="tool-commands tool-urls"><thead><tr><th>Site</th><th>Source</th><th>URL</th></tr></thead><tbody>
    {rows.map(entry => <tr key={entry.position}><td>{String(entry.host)}</td><td>{urlSourceLabels[String(entry.source)] ?? String(entry.source)}</td><td className="tool-command-text"><a href={String(entry.url)} target="_blank" rel="noopener noreferrer">{String(entry.url)}</a></td></tr>)}
  </tbody></table></div>;
  return <>
    {reached.length ? <><h3>Sites reached</h3>{table(reached)}</> : null}
    {results.length ? <><h3>Search results ({results.length.toLocaleString()})</h3>{table(results)}</> : null}
  </>;
}

function ToolCallDialog({ id, onClose }: { id: string; onClose: () => void }) {
  const [call, setCall] = useState<Row | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    if (shared) {
      const row = shared.datasets.tool_calls.find(item => item.id === id);
      if (row) setCall({ ...row, ...shared.tool_calls[id] });
      else setError("Call unavailable");
      return;
    }
    const controller = new AbortController();
    fetch(`/api/tool-calls/${encodeURIComponent(id)}`, { signal: controller.signal }).then(responseJSON<Row>).then(setCall)
      .catch(failure => { if (!controller.signal.aborted) setError(failure instanceof Error ? failure.message : "Call unavailable"); });
    return () => controller.abort();
  }, [id]);
  const number = (value: unknown) => value === null || value === undefined ? "—" : Number(value).toLocaleString();
  return <div className="mcp-dialog-backdrop" onClick={onClose}><div className="mcp-dialog tool-dialog" role="dialog" aria-modal="true" aria-label="Tool call details" onClick={event => event.stopPropagation()}>
    <div className="mcp-card-heading"><div><h2>{call ? String(call.tool_name) : "Tool call"}</h2><p className="muted">{call ? `${new Date(String(call.started_at)).toLocaleString()} · ${call.status}${call.error_type ? ` · ${String(call.error_type).replace(/_/g, " ")}` : ""}` : error || "Loading…"}</p></div>
      <div className="tool-dialog-actions">{call?.workspace_id ? <button type="button" onClick={() => { onClose(); window.pharosOpenDetail?.(String(call.workspace_id)); }}>Open work</button> : null}<button type="button" onClick={onClose}>Close</button></div></div>
    {call ? <>
      <dl className="tool-facts">
        <div><dt>Duration</dt><dd>{formatDurationMS(call.duration_ms)}{call.duration_source === "timestamps" ? " (timestamps)" : ""}</dd></div>
        <div><dt>Context added</dt><dd>{call.result_tokens == null ? "—" : compactNumber(Number(call.result_tokens))} tokens{call.result_tokens_source ? ` · ${call.result_tokens_source}` : ""}</dd></div>
        <div><dt>Carried</dt><dd>{call.carried_tokens == null ? "—" : compactNumber(Number(call.carried_tokens))} tokens over {number(call.carried_requests)} requests</dd></div>
        <div><dt>Cost</dt><dd>{call.tool_cost_usd === null || call.tool_cost_usd === undefined ? "—" : formatUSD(Number(call.tool_cost_usd))}</dd></div>
        <div><dt>Exit code</dt><dd>{call.exit_code ?? "—"}</dd></div>
        <div><dt>Calls in request</dt><dd>{number(call.parallel_count)}</dd></div>
      </dl>
      {call.error_signature ? <p className="muted">Error signature: {String(call.error_signature)}</p> : null}
      {call.title ? <p className="muted">{String(call.title)}{call.repository_name ? ` · ${call.repository_name}` : ""} · {String(call.provider)} {call.model ? `· ${call.model}` : ""}</p> : null}
      {Array.isArray(call.commands) && call.commands.length ? <><h3>Commands</h3><div className="mcp-tool-table-wrap"><table className="tool-commands"><thead><tr><th>#</th><th>Program</th><th>Category</th><th>Exit</th><th>Duration</th><th>Command</th></tr></thead><tbody>
        {call.commands.map((command: Row) => <tr key={command.position}><td>{command.operator && command.operator !== "script" ? command.operator : Number(command.position) + 1}</td><td>{[command.program, command.subcommand].filter(Boolean).join(" ")}</td><td>{command.category ?? ""}</td><td>{command.exit_code ?? ""}</td><td>{command.duration_ms === null || command.duration_ms === undefined ? "" : formatDurationMS(command.duration_ms)}</td><td className="tool-command-text">{String(command.command)}</td></tr>)}
      </tbody></table></div></> : null}
      {call.search_query ? <><h3>Search query</h3><p className="tool-search-query">{String(call.search_query)}</p></> : null}
      {Array.isArray(call.urls) && call.urls.length ? <ToolCallURLs urls={call.urls} /> : null}
      <h3>Input</h3><pre className="mcp-code">{String(call.input_text ?? "")}</pre>
      <h3>Result{Number(call.result_text_bytes) > String(call.result_text ?? "").length ? ` (first ${String(call.result_text ?? "").length.toLocaleString()} of ${Number(call.result_text_bytes).toLocaleString()} characters)` : ""}</h3><pre className="mcp-code">{String(call.result_text ?? "") || "No result was recorded."}</pre>
      {call.result_details ? <><h3>Result metadata</h3><pre className="mcp-code">{String(call.result_details)}</pre></> : null}
    </> : null}
  </div></div>;
}

function ToolLedgerBanner() {
  const [status, setStatus] = useState<ToolLedgerStatus | null>(null);
  const [error, setError] = useState("");
  const load = useRef(async () => {});
  load.current = async () => {
    try { setStatus(await responseJSON<ToolLedgerStatus>(await fetch("/api/tools/status"))); setError(""); }
    catch (failure) { setError(failure instanceof Error ? failure.message : "Status unavailable"); }
  };
  useEffect(() => {
    if (shared) return;
    void load.current();
    const timer = window.setInterval(() => { if (document.querySelector("#tools.active")) void load.current(); }, 3000);
    return () => window.clearInterval(timer);
  }, []);
  const running = status?.backfill.running ?? false;
  const wasRunning = useRef(false);
  useEffect(() => {
    if (wasRunning.current && !running) { tableApis.get("tools")?.refresh(); tableApis.get("tool_calls")?.refresh(); }
    wasRunning.current = running;
  }, [running]);
  async function build() {
    try { await fetch("/api/tools/backfill", { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" }); await load.current(); }
    catch (failure) { setError(failure instanceof Error ? failure.message : "Could not start"); }
  }
  if (shared) return null;
  if (error) return <div className="query-table-error">{error}</div>;
  if (!status || (!running && status.pending_conversations === 0 && !status.backfill.error)) return null;
  const percent = status.backfill.total ? Math.round(status.backfill.done / status.backfill.total * 100) : 0;
  return <div className="tool-ledger-banner" role="status">
    {running ? <span>Building the tool ledger from retained transcripts: {status.backfill.done.toLocaleString()} of {status.backfill.total.toLocaleString()} conversations ({percent}%). Tables refresh as it goes.</span>
      : <span>{status.pending_conversations.toLocaleString()} of {status.conversations.toLocaleString()} conversations need a tool ledger rebuild. Building it re-reads their retained messages; no source files are needed.{status.backfill.error ? ` Last attempt failed: ${status.backfill.error}` : ""}</span>}
    {running ? null : <button type="button" onClick={() => void build()}>Build tool ledger</button>}
  </div>;
}

function termKey(term: WhereTerm): string {
  const clause = (item: { field: string; op: string; value: string; negated?: boolean }) => `${item.field}|${item.op}|${item.value}|${item.negated ? 1 : 0}`;
  return "any" in term ? `any:${term.any.map(clause).join("&")}` : clause(term);
}

function ToolsPage() {
  const [view, setView] = useState<"summary" | "calls">(() => new URLSearchParams(location.search).get("tools_view") === "summary" ? "summary" : "calls");
  const [selected, setSelected] = useState<string | null>(null);
  // Filters the last preset added, so the next preset replaces them while
  // keeping filters the user added by hand.
  const presetWhere = useRef<Partial<Record<Dataset, string[]>>>({});
  function choose(next: "summary" | "calls") { setView(next); updateURI("tools_view", next === "summary" ? next : ""); }
  function apply(dataset: Dataset, preset: ToolPreset | null) {
    const added = presetWhere.current[dataset] ?? [];
    presetWhere.current[dataset] = (preset?.where ?? []).map(termKey);
    tableApis.get(dataset)?.setQuery(previous => ({
      ...previous,
      aggregations: preset?.aggregations ?? [],
      where: [...previous.where.filter(term => !added.includes(termKey(term))), ...(preset?.where ?? [])],
      orderBy: preset?.orderBy ?? previous.orderBy,
      offset: 0,
    }));
  }
  function drill(row: Row) {
    tableApis.get("tool_calls")?.setQuery(previous => ({ ...previous, where: drillFilters(row), aggregations: [], offset: 0 }));
    choose("calls");
  }
  return <div className="pharos-query-page tools-page">
    <div className="view-heading library-heading"><div><h1>Tool use</h1><p className="muted">Every tool call from retained transcripts, joined to its result. Shell commands are parsed into program, subcommand, and category. <b>Context added</b> is what a result grew the next request’s prompt by (measured from provider usage, or estimated from size at 4 bytes a token); <b>context carried</b> counts it again for every later request until compaction. Claude durations come from timestamps and include any wait for approval. Linked mirrors of the same work are counted once.</p></div>
      <div className="library-view-toggle" role="group" aria-label="Tool view">
        <button type="button" className={view === "calls" ? "active" : ""} aria-pressed={view === "calls"} onClick={() => choose("calls")}>Calls</button>
        <button type="button" className={view === "summary" ? "active" : ""} aria-pressed={view === "summary"} onClick={() => choose("summary")}>Summary</button>
      </div></div>
    <ToolLedgerBanner />
    <div hidden={view !== "summary"}>
      {toolSummaryPresets.map(({ group, presets }) => <div key={group} className="usage-presets" role="group" aria-label={`${group} presets`}>
        <span className="usage-preset-label">{group}</span>
        {presets.map(preset => <button key={preset.label} type="button" title={preset.title} onClick={() => apply("tools", preset)}>{preset.label}</button>)}
        {group === "Context" ? <button type="button" className="usage-preset-clear" onClick={() => apply("tools", null)}>Clear metrics</button> : null}
      </div>)}
      <QuerySurface dataset="tools" trailing={row => <button type="button" className="mcp-detail-button" title="Show the calls behind this row" onClick={() => drill(row)}>Calls</button>} />
    </div>
    <div hidden={view !== "calls"}>
      <div className="usage-presets" role="group" aria-label="Call presets">
        <span className="usage-preset-label">Calls</span>
        {toolCallPresets.map(preset => <button key={preset.label} type="button" title={preset.title} onClick={() => apply("tool_calls", preset)}>{preset.label}</button>)}
        <button type="button" className="usage-preset-clear" onClick={() => apply("tool_calls", null)}>Clear metrics</button>
      </div>
      <QuerySurface dataset="tool_calls" trailing={row => <button type="button" className="mcp-detail-button" onClick={() => setSelected(String(row.id))}>Details</button>} />
    </div>
    {selected ? <ToolCallDialog id={selected} onClose={() => setSelected(null)} /> : null}
  </div>;
}

const mounts: Array<[string, React.ReactNode]> = [
  ["queryTableLibrary", <LibraryPage />],
  ["queryTableUsage", <UsagePage />],
  ["toolsPage", <ToolsPage />],
  ["mcpPage", <MCPPage />],
  ["findingsPage", <FindingsPage copy={copyText} />],
  ["tl1Page", <TL1Page attempts={<QuerySurface dataset="tl1_attempts" />} filterAttempts={(where) => tableApis.get("tl1_attempts")?.setQuery(previous => ({ ...previous, where: where as WhereTerm[], offset: 0 }))} copy={copyText} />],
];
for (const [id, component] of mounts) {
  if (shared && !["queryTableLibrary", "queryTableUsage", "toolsPage"].includes(id)) continue;
  const element = document.getElementById(id);
  if (element) createRoot(element).render(component);
}
if (!shared) startFindingsChrome();
window.pharosQueryTables = {
  refresh(dataset) {
    tableApis.get(dataset)?.refresh();
    if (dataset === "usage") {
      tableApis.get("writing")?.refresh();
      tableApis.get("writing_messages")?.refresh();
      window.dispatchEvent(new Event("pharos:usage-refresh"));
    }
  },
  filterRepository(repository) {
    clearLibrarySearch?.();
    tableApis.get("library")?.setQuery((previous) => ({
      ...previous,
      where: [{ field: "repository_name", op: "=", value: repository }],
      offset: 0,
    }));
  },
  filterModel(model) {
    clearLibrarySearch?.();
    tableApis.get("library")?.setQuery((previous) => ({
      ...previous,
      where: [{ field: "models", op: "contains", value: model }],
      offset: 0,
    }));
  },
};
