import { rangeDays, type ChartRange } from "./chart-range";
import { startDayClock, localTimezone } from "./day-window";
import "./visit-history";
import { originLabel, originRenderers, useOriginMetadata, isOriginDimension } from "./origin-dimensions";
import "./uifeedback/pharos-client";
import { matchingSelectionIDs } from "./query-selection";
import { DataTaxonomyModal, SourceQueryBuilder } from "./data-taxonomy";
import { installDelegationExplorer } from "./delegation";
import { installWorkspaceInbox } from "./workspace-inbox";
import { MessageTableSearch, newMessageSearch } from "./message-table-search";
import "./conversation-search";
import { TextSearchEditor } from "./text-search-editor";
import { SearchHelp } from "./search-help";
import { RecordSearchEditor, recordSearchTargets } from "./record-search-editor";
import { usageDetailsSchema } from "./usage-row-model";
import { usageActionQuery, usageQuerySchema, withGroupDetail } from "./usage-query-columns";
import { orderedUsageQuery } from "./usage-query-order";
import { usageRowRenderers } from "./usage-row-renderers";
import { createQueryMetadataLoader } from "./query-metadata";
import { SearchDialog } from "./search-dialog";
import { ExploreSearch } from "./explore-search";
import { recordKeywordSummary, RecordKeywordClause } from "./record-keyword-clause";
import { refreshTodayHost, libraryFind, libraryHostQuery, libraryHostState, messageFind, messageHostQuery, readLibraryHost, readMessageSearchHost, readRecordKeyword, recordKeywordFind, recordKeywordHostQuery, sameHostState, withMessageSearch, withoutLibrarySearch, withRecordKeyword, type LibraryHost } from "./host-query-state";
import { SearchResultsDisplay } from "./search-results-view";
import { queryForSearchResultsView, searchResultsView, type SearchResultsView } from "./search-results-view-model";
import { newKeywordSearch, searchSentence, withheldBatch, type TextSearchDraft } from "./text-search-model";
import { CarbonImpact, useLegacyCarbonFilters } from "./carbon-impact";
import { carbonCatalogShape, legacyCarbonChart } from "./carbon-analysis-catalog";
import { usageSummaryURL } from "./usage-summary-links";
import { readConcurrencySettings } from "./concurrency-settings";
import { pushMachineLocation, useMachineQueryNavigation } from "./machine-location";
import { MachineActivity } from "./machine-activity";
import { activityPresentation } from "./machine-activity-data";
import { ChartSelect, DisplayControl, PeriodControl, RangeControl, ScaleControl, RollingControl, StackedColumns, chartPeriodCount, chartSpan, pickRange, presetPeriod, smoothingWhere, timeBounds, withTimeRange, usageTime, writingTime, type FilterChange } from "./usage-chart";
import { applyUsageAggregations } from "./usage-aggregations";
import { presetMatches, usageChartPresets, type TokenChartView, type UsagePreset } from "./usage-presets";
import { withComputedCapabilityRefresh } from "./computed-column-store";
import { MetricRequestError, postMetrics, postQueryV3Metrics, type MetricResult } from "./metric-request";
import { tokenSummaryMetrics } from "./token-chart-request";
import { chartPeriods, earliestKey, isHourly, localDay, parseDay, periodLabel, validPeriodCount, type ChartPeriod } from "./chart-period";
import { defaultChartScale, validChartScale } from "./chart-scale";
import React, { useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { useLocationRevision } from "./nested-pages";
import { LibraryContent } from "./library-content";
import { SyncTables } from "./sync-tables";
import { SettingsWorkspace } from "./settings-workspace";
import { installAppNavigation } from "./app-navigation";
import { EMPTY_QUERY, groupKeyField, groupKeyFields, groupSourceQuery, isWhereConjunction, isWhereGroup, predicatesOf, httpComputedColumnStore, inspectBucketQuery, applyAggregations, encodeQuery, loadSchema, memoryStorageAdapter, toAggregationQuery, type ComputedColumnStore, type HostQueryState, type QueryCapabilitiesV3, type QueryResultV3, type FetchRowsResultV2, type ComputedExecution, type MetricCapabilities, type AggregationClause, type FieldSchema, type Transport, type WhereBranch, type WhereClause, type WhereTerm, type QueryState, type RelationshipAdapter, jsonPropertyViewStorage } from "@pythia-software/query-table-core";
import { useQueryTable, type HostQueryOptions, type QueryTableApi, type QueryFilterApi } from "@pythia-software/query-table-react";
import { DataTable, FilterValueProvider, MetricsPanel, QueryFilterRow, SelectionToolbar, defaultRenderers, type CellContext, type FilterValuePresentation, type RenderRegistry } from "@pythia-software/query-table-ui";
import "@pythia-software/query-table-ui/theme.css";
import "./pharos.css";
import { savedRollingWindow } from "./bookmarks";
import { acceptDefaultQuery, libraryStorageAdapter, rejectDefaultQuery, retryDefaultQuery } from "./storage";
import { queryRequest, queryFetch, shouldSkipQueryOnRestore } from "./query-request";
import { PageErrorBoundary, QueryRecovery, ResetPage, tableRecoveryQuery } from "./query-recovery";
import { createMCPMonitor, type MCPStatus } from "./mcp-monitor";
import { HomePage } from "./home";
import { AskPharosSearch, AskPharosSettings } from "./ask-pharos";
import { CardLibraryPage } from "./card-library";
import { linkTargets, navigateLink } from "./query-link-targets";
import { QueryChip } from "./query-card";
import { queryFromURL, queryURL, rollingWindowField, updateQueryWindowURL, readRollingWindow, resolveRollingQuery, rollingQueryMatches } from "./query-links";
import { mainIntegration, pullRequestLabel, pullRequestURL, pullRequests } from "./pull-requests";
import { optionLabels } from "./option-labels";
import { preferences } from "./preferences";
import { chartBucketsWithHistory, foldSeries, plottedBucketKey, validChartDisplay, validRollingPeriods, type ChartDisplay } from "./chart-series";
import { isSliceTerm, readSlice, updateSlice, withSlice, validChartPresentation, type ChartPresentation, type LegendAction, type SliceSelection } from "./chart-selection";
import { UsageChartColorsProvider, useUsageChartPalette, useUsageTableRenderers } from "./usage-chart-colors";
import libraryDocument from "../../schemas/library.schema.json";
import usageDocument from "../../schemas/usage.schema.json";
import writingDocument from "../../schemas/writing.schema.json";
import conversationActivityDocument from "../../schemas/conversation_activity.schema.json";
import writingMessagesDocument from "../../schemas/writing_messages.schema.json";
import mcpCallsDocument from "../../schemas/mcp_calls.schema.json";
import toolsDocument from "../../schemas/tools.schema.json";
import toolCallsDocument from "../../schemas/tool_calls.schema.json";
import skillUsagesDocument from "../../schemas/skill_usages.schema.json";
import tl1AttemptsDocument from "../../schemas/tl1_attempts.schema.json";
import parallelismDocument from "../../schemas/parallelism.schema.json";
import findingsDocument from "../../schemas/findings.schema.json";
import { TL1Page } from "./tl1";
import { FindingsPage, startFindingsChrome } from "./findings";
import { Icon } from "./icons";
import { AuthoredMessages, CategoryPill, MessageMix, authoredRenderers } from "./authored";
import { ConcurrencyModal, ConcurrencyPanel, concurrencyPrototypeEnabled, type ConcurrencySelection } from "./concurrency";
import { openConcurrencyModal, startConcurrencySummary } from "./concurrency-store";
import { currentUsageAnalyses, isUsageWorkspace, readUsageWorkspace, usageDatasets, usageWorkspaceURL, type UsageDataset, type UsageTopic } from "./usage-workspace-model";
import { mcpAnalysisCatalog } from "./mcp-analysis-catalog";
import { toolsAnalysisCatalog } from "./tools-analysis-catalog";
import { agentTimeAnalysisCatalog, skillsAnalysisCatalog } from "./record-analysis-catalogs";
import { UsageWorkspace, UsageDashboardLinks, UsageWorkspaceLink } from "./usage-workspace";
import { UsageQueryIdeas } from "./usage-query-ideas";
import { toolSummaryCallFilters } from "./tools-dashboard-model";
import { readDashboardLink, readDashboardQuery, parallelismSelectionQuery } from "./usage-dashboard-model";
import { dashboardChartURL } from "./dashboard-chart-links";
import { DashboardAnalysis } from "./dashboard-analysis";
import { AnalysisTrend, type AmountFormat } from "./dashboard-trend";
import { analysisVersion, canonicalState, normalizeAnalysis, readAnalysisLink, switchView, validAnalysisState, type AnalysisCatalog, type AnalysisState, type AnalysisView } from "./dashboard-analysis-model";
import { dashboardDefaults, defaultTokenChart } from "./dashboard-defaults";
import { tokenAnalysisCatalog } from "./token-analysis-catalog";
import { describeTerm, humanAnalysisCatalog, humanLegacyURL, readHumanLink, writingGroups } from "./human-analysis-catalog";
import { parallelismCatalogShape, parallelismSettingsNote, type ParallelismSettings } from "./parallelism-analysis-catalog";
import { useCarbonCatalog, useParallelismCatalog } from "./analysis-catalog-hooks";
import { datasetsForSavedPreferences, onDatasetRefresh } from "./dataset-refresh";
import type { LocalRows } from "./analysis-request";

type Row = Record<string, any>;
const relationships: RelationshipAdapter = {
  async resolve(references, signal) {
    return responseJSON(await fetch("/api/query-object-references", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ references }), signal }));
  },
};
type Dataset = "library" | "usage" | "conversation_activity" | "writing" | "writing_messages" | "mcp_calls" | "tools" | "tool_calls" | "skill_usages" | "tl1_attempts" | "parallelism" | "findings";
type MessageView = "table" | "text";
type TranscriptMessage = { role: string; text: unknown; raw_text?: unknown };
type TurnSummary = { human: TranscriptMessage | null; response: TranscriptMessage | null };
type ConversationTurn = { provider: string; conversation: number; turn: number; input: TranscriptMessage | null; response: TranscriptMessage | null };
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

function mainIntegrationLink(row: Row): React.ReactNode {
  const merge = mainIntegration(row);
  if (!merge) return "";
  return merge.url ? <a className="qt-pr-link" href={merge.url} target="_blank" rel="noopener noreferrer" title={merge.commit} onClick={event => event.stopPropagation()}>{merge.label}</a>
    : <span title={merge.commit}>{merge.label}</span>;
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
function datasetIsVisible(dataset: Dataset | "machine_calls") {
  const params = new URLSearchParams(location.search), path = location.pathname;
  if (path === "/analyze" && isUsageWorkspace(params)) {
    const workspace = readUsageWorkspace(params);
    return workspace.mode !== "dashboards" && workspace.dataset === dataset;
  }
  if (dataset === "machine_calls") return path === "/analyze" && (params.get("analyze_view") ?? "tokens") === "tokens" && (params.get("machine_view") ?? preferences().get("pharos-machine-view", "tokens")) === "time";
  if (dataset === "library") return path === "/library";
  if (dataset === "findings") return path === "/findings" && params.get("view") === "table";
  if (dataset === "parallelism") return path === "/analyze" && (params.get("analyze_view") ?? "tokens") === "tokens" && (params.get("machine_view") ?? preferences().get("pharos-machine-view", "tokens")) === "parallelism";
  const usageView = params.get("analyze_view") ?? preferences().get("pharos-usage-view", "tokens");
  if (dataset === "conversation_activity") return path === "/analyze" && usageView === "conversations";
  if (dataset === "usage") return path === "/analyze" && usageView === "tokens";
  if (dataset === "writing" || dataset === "writing_messages") return path === "/analyze" && usageView === "writing" && (params.get("writing") === "messages") === (dataset === "writing_messages");
  if (["tools", "tool_calls", "skill_usages"].includes(dataset)) return path === "/tools" && (params.get("tools_view") ?? "calls") === (dataset === "tools" ? "summary" : dataset === "skill_usages" ? "skills" : "calls");
  return dataset === "mcp_calls" ? path === "/mcp" : path.startsWith("/tl1");
}


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
    pharosQueryTables?: {
      createLink: () => Promise<{ url: string; query?: QueryState; dataset?: Dataset }>;
      refresh: (dataset: Dataset) => void;
      filterRepository: (repository: string) => void;
      filterModel: (model: string) => void;
      summaryURL: typeof usageSummaryURL;
    };
  }
}

const schemas: Record<Dataset, FieldSchema<Row>> = {
  conversation_activity: loadSchema<Row>(conversationActivityDocument),
  library: loadSchema<Row>(libraryDocument),
  usage: loadSchema<Row>(usageDocument),
  writing: loadSchema<Row>(writingDocument),
  writing_messages: loadSchema<Row>(writingMessagesDocument),
  mcp_calls: loadSchema<Row>(mcpCallsDocument),
  tools: loadSchema<Row>(toolsDocument),
  tool_calls: loadSchema<Row>(toolCallsDocument),
  skill_usages: loadSchema<Row>(skillUsagesDocument),
  tl1_attempts: loadSchema<Row>(tl1AttemptsDocument),
  parallelism: loadSchema<Row>(parallelismDocument),
  findings: loadSchema<Row>(findingsDocument),
};

const tableApis = new Map<Dataset | "machine_calls", QueryTableApi<Row>>();
// Host routes can reveal a previously mounted, inactive table. Restore the
// destination directly: a synthetic popstate fires before React updates active
// flags, so it can miss that table and restore the outgoing one instead.
window.addEventListener("pharos:route", () => {
  const destination = [...tableApis.keys()].find(dataset => datasetIsVisible(dataset));
  if (destination) void tableApis.get(destination)?.restoreLocation(location.href).catch(() => {
    // Query Table's restoration state displays unavailable/invalid links.
  });
});

let pendingLibraryFilter: WhereTerm[] | undefined;

function filterLibrary(where: WhereTerm[]) {
  pendingLibraryFilter = where;
  window.dispatchEvent(new Event("pharos:library-filter"));
}

async function responseJSON<T>(response: Response): Promise<T> {
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(body.error || `Request failed with HTTP ${response.status}`);
  return body as T;
}

// extra carries the Library's keyword search to every request, so rows,
// metrics, and filter pickers all follow it. onFind hears how it went.
function recordBudget(dataset: Dataset, where: WhereTerm[]) {
  const fields = dataset === "tool_calls" ? ["command", "file_path"] : dataset === "writing_messages" ? ["text"] : dataset === "mcp_calls" ? ["arguments_json", "error_text"] : [];
  return where.some(term => predicatesOf(term).some(c => fields.includes(c.field))) ? new URLSearchParams({ interactive_search: "1" }) : undefined;
}
startDayClock();
const queryMetadata = createQueryMetadataLoader();
// host compiles a table's host query state into the service's parameters. Each
// request carries the host state it runs with (Query Table adds it), so rows,
// metrics and filter values never pair one definition's table query with
// another's keyword search. Field statistics carry none and follow the
// current definition.
type HostFind = { find: (state: HostQueryState | undefined) => string; current: () => string };
function makeTransport(dataset: Dataset, extra = "", onFind?: (find: FindSummary | null) => void, capabilities?: MetricCapabilities, queryCapabilities?: QueryCapabilitiesV3, host?: HostFind): Transport<Row> {
  const endpoint = (suffix = "", params?: URLSearchParams, collection = true, request?: { hostState?: HostQueryState }) => {
    const query = new URLSearchParams(collection ? extra : "");
    if (collection && host) new URLSearchParams(request?.hostState ? host.find(request.hostState) : host.current()).forEach((value, name) => query.set(name, value));
    const carbon = new URLSearchParams(location.search).get("analyze_carbon");
    if (dataset === "usage" && carbon) query.set("carbon_settings", carbon);
    params?.forEach((value, name) => query.set(name, value));
    return `/api/query/${dataset}${suffix}${query.size ? `?${query}` : ""}`;
  };
  return {
    metricCapabilities: capabilities,
    queryCapabilities,
    ...(queryCapabilities?.version === 3 ? { async fetchQueryV3(query: Parameters<NonNullable<Transport<Row>["fetchQueryV3"]>>[0], signal?: AbortSignal) {
      const result = await queryRequest<QueryResultV3 & { find?: FindSummary }>(endpoint("/query-v3", undefined, true, query), {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(query), signal,
      });
      onFind?.(result.find ?? null);
      return result;
    } } : {}),
    async fetchRow(id, signal) {
      const result = await queryRequest<{ row: Row | null }>(endpoint("/item", new URLSearchParams({ id: String(id) }), false), { signal });
      return result.row;
    },
    async fetchRowsV2(query, signal) {
      const result = await queryRequest<FetchRowsResultV2<Row> & { find?: FindSummary }>(endpoint("/rows-v2", recordBudget(dataset, query.where), true, query), {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(query), signal,
      });
      onFind?.(result.find ?? null);
      return result;
    },
    async fetchMetrics(query, signal) {
      return queryRequest(endpoint("/metrics", recordBudget(dataset, query.where), true, query), {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(query), signal,
      });
    },
    async fetchDistinctValues(query, signal) {
      const params = new URLSearchParams({ field: query.field, q: query.search });
      if (query.limit) params.set("limit", String(query.limit));
      const url = endpoint("/distinct", params, true, query);
      return queryMetadata(url, async () => queryRequest(url), signal);
    },
    async fetchFieldStats(fields, signal) {
      const url = endpoint("/field-stats", new URLSearchParams({ fields: fields.join(",") }));
      return queryMetadata(url, async () => queryRequest(url), signal);
    },
  };
}

type Capabilities = { metricCapabilities: MetricCapabilities; queryCapabilities?: QueryCapabilitiesV3; computedExecution: ComputedExecution };
const fetchCapabilities = async (dataset: Dataset, signal?: AbortSignal) => queryRequest<Capabilities>(`/api/query/${dataset}/capabilities`, { signal });

const renderers: RenderRegistry<Row> = {
  ...defaultRenderers,
  machine_activity: ({ value }) => activityPresentation(String(value ?? "")).label,
  bool_check(context: CellContext<Row>) {
    return context.value ? <Icon name="check" /> : defaultRenderers.bool_check(context);
  },
  number(context: CellContext<Row>) {
    if (context.value != null && ["card_rate", "card_escalation"].includes(context.field.name)) return `${(Number(context.value) * 100).toLocaleString(undefined, { maximumFractionDigits: 1 })}%`;
    // Links saved before the percent display carry a 0–100 cache rate.
    if (context.value != null && context.field.name === "usage_cache_hit_percent") return `${Number(context.value).toLocaleString(undefined, { maximumFractionDigits: 1 })}%`;
    if (context.value != null && context.field.name === "usage_price_delta") return formatUSD(Number(context.value));
    const value = Number(context.value);
    if (/(^token_count$|_tokens$)/.test(context.field.name) && Number.isFinite(value) && Math.abs(value) >= 1e6)
      return <span title={value.toLocaleString()}>{compactNumber(value)}</span>;
    return defaultRenderers.number(context);
  },
  work_title({ value, row, query }: CellContext<Row>) {
    // A group row (a cell or group Detail) names a work only when the work's
    // ID is one of its keys; otherwise its identity is the group's.
    const keys = query?.grouping ? groupKeyFields(query.grouping.keys) : null;
    const work = row.workspace_id ?? (!keys || keys.includes("id") ? row.id : undefined);
    if (work == null || work === "") return String(value ?? "Untitled work");
    return <button className="qt-work-link" title={String(value ?? "")} onClick={(event) => {
      event.stopPropagation();
      window.pharosOpenDetail?.(String(work));
    }}>{String(value ?? "Untitled work")}</button>;
  },
  pull_requests({ value, row }: CellContext<Row>) {
    const prs = pullRequests(row);
    if (!prs.length) return String(value ?? "");
    return <span className="qt-pr-list">{prs.map((pr, index) => {
      const label = pullRequestLabel(pr);
      const url = pullRequestURL(pr, row.canonical_remote);
      return url ? <a key={`${pr.host}:${pr.number}:${index}`} className="qt-pr-link" href={url} target="_blank" rel="noopener noreferrer" title={label} onClick={event => event.stopPropagation()}>{label}</a>
        : <span key={`${pr.host}:${pr.number}:${index}`} title={label}>{label}</span>;
    })}</span>;
  },
  conversation_link(context: CellContext<Row>) {
    const id = String(context.value ?? "");
    if (!id || shared) return defaultRenderers.text(context);
    return <a className="qt-conversation-link" href={`/conversation/${encodeURIComponent(id)}`} title={`Open conversation ${id}`} onClick={event => {
      event.stopPropagation();
      if (event.button || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
      event.preventDefault();
      void openConversation(id);
    }}>{id}</a>;
  },
  main_integration({ row }: CellContext<Row>) { return mainIntegrationLink(row); },
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
  ...originRenderers<Row>(),
};

// Filter chips, pickers, and cell quick filters show each value's schema label
// and, for status fields, the badge its cells use. Queries and URLs keep the
// raw value.
const statusBadges: Record<string, (value: string, label: string) => React.ReactNode> = {
  run_state: (value, label) => <span className={`qt-state-pill ${value}`}>{label}</span>,
  mcp_status: (value, label) => <span className={`mcp-call-state ${value}`}>{label}</span>,
  tool_status: (value, label) => <span className={`mcp-call-state ${value}`}>{label}</span>,
  authorship_category: (value, label) => <CategoryPill category={value} label={label} />,
  authorship_categories: (value, label) => <CategoryPill category={value} label={label} />,
};

const filterPresentations = Object.fromEntries(Object.entries(schemas).map(([dataset, schema]) => {
  const labels = optionLabels(schema.fields);
  const badges = new Map(schema.fields.flatMap(field => typeof field.render === "string" && statusBadges[field.render] ? [[field.name, statusBadges[field.render]] as const] : []));
  const label = (field: string, value: string) => isOriginDimension(field) ? originLabel(field, value) : labels.get(field)?.get(value) ?? value;
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
  conversation_activity: "No recorded conversation activity matches this query.",
  writing: "No conversations with user messages match this query.",
  writing_messages: "No classified user messages match this query.",
  mcp_calls: "No MCP calls match this query.",
  tools: "No tool use matches this query.",
  tool_calls: "No tool calls match this query.",
  skill_usages: "No skill usage evidence matches this query.",
  tl1_attempts: "No TL1 runs match this query.",
  parallelism: "No agent activity matches this query.",
  findings: "No findings match this query.",
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

function usageGroupFields(fields: FieldSchema<Row>["fields"], query: QueryState): FieldSchema<Row>["fields"] {
  const summaries = new Map(query.grouping?.summaries.map(column => [column.id, column]));
  return fields.map(field => {
    const column = summaries.get(field.name);
    // A summary's own display (Query Table 0.8) replaces these render fallbacks.
    return column ? { ...field, ...(column.label ? { label: column.label } : {}),
      ...(!field.render && !column.display && field.type === "number" ? { render: column.expression === "MEDIAN([duration_ms])" ? "duration_ms" : column.id === "card_price_delta" ? "usd" : "number" } : {}) } : field;
  });
}

// header renders between the filters and the metric panels, so a chart there
// can follow the table's filters.
// host registers clauses Pharos runs itself (host-query-state.ts) as part of
// the table's definition; its find compiles them into each request. find
// carries other request parameters, such as a message search cursor.
// onFind receives Library message-search coverage when present.
type SurfaceHost = { query: HostQueryOptions; find: (state: HostQueryState | undefined) => string; onState?: (state: HostQueryState | undefined) => void };
function QuerySurface({ dataset, host, usageActions = false, messageView = "table", find = "", onFind, trailing, header, controls, queryContent, querySummary, resultPresentation, presets, selectable = false, scopeWhere, appliedScope, namespace }: { dataset: Dataset; host?: SurfaceHost; usageActions?: boolean; selectable?: boolean; messageView?: MessageView; find?: string; onFind?: (find: FindSummary | null) => void; trailing?: (row: Row, api: QueryTableApi<Row>) => React.ReactNode; header?: (api: QueryTableApi<Row>) => React.ReactNode; controls?: (api: QueryTableApi<Row>, builder: React.ReactNode) => React.ReactNode; queryContent?: (api: QueryTableApi<Row>) => React.ReactNode; querySummary?: (api: QueryTableApi<Row>) => React.ReactNode; resultPresentation?: (api: QueryTableApi<Row>) => React.ReactNode; presets?: (api: QueryTableApi<Row>) => React.ReactNode; scopeWhere?: WhereTerm[]; appliedScope?: React.MutableRefObject<WhereTerm[]>; namespace?: "machine_calls" }) {
  const registryKey = namespace ?? dataset;
  const visible = shared || datasetIsVisible(registryKey);
  const schema = useMemo(() => namespace ? { ...schemas[dataset], name: "pharos_machine_calls", defaultSelect: [{ field: "started_at", width: 155 }, { field: "repository_name", width: 140 }, { field: "operation_name", width: 180 }, { field: "tool_name", width: 140 }, { field: "command", width: 350 }, { field: "duration_ms", width: 105 }, { field: "status", width: 85 }, { field: "duration_source", width: 120 }, { field: "title", width: 240 }] } : usageActions ? usageQuerySchema(schemas[dataset], dataset as UsageDataset) : schemas[dataset], [dataset, namespace, usageActions]);
  const locationRevision = useLocationRevision();
  const usageRenderers = useMemo(() => Object.hasOwn(usageDatasets, dataset) ? { ...renderers, ...usageRowRenderers(dataset as UsageDataset, Boolean(shared)) } : renderers, [dataset]);
  const tableRenderers = useUsageTableRenderers(usageRenderers);
  const detailSchema = useMemo(() => usageDetailsSchema(schema, dataset), [schema, dataset]);
  const onFindRef = useRef(onFind);
  onFindRef.current = onFind;
  const [capabilities, setCapabilities] = useState<Capabilities>();
  const [capabilityError, setCapabilityError] = useState<Error>();
  const [capabilityRetry, setCapabilityRetry] = useState(0);
  const remote = !shared;
  const hostRef = useRef(host);
  hostRef.current = host;
  const hostFind = useRef("");
  // A server table runs only once it knows the server's capabilities: rows
  // and metrics both go through V2/V3, and nothing reads the legacy routes
  // first. A failed handshake shows its error instead.
  useEffect(() => {
    if (!remote || !visible || capabilities) return;
    setCapabilityError(undefined);
    const controller = new AbortController();
    fetchCapabilities(dataset, controller.signal).then(setCapabilities, failure => {
      if (!controller.signal.aborted) setCapabilityError(failure instanceof Error ? failure : new Error(String(failure)));
    });
    return () => controller.abort();
  }, [dataset, remote, Boolean(visible), Boolean(capabilities), capabilityRetry]);
  const computedColumnStore = useMemo(() => {
    if (shared || !visible) return undefined;
    const backendStore = httpComputedColumnStore("/api/query-computed-columns", queryFetch);
    // Group projections have a derived schema name. Canonical definitions and
    // their execution handshake still belong to this surface's source dataset.
    const store: ComputedColumnStore = {
      ...backendStore,
      list: (_dataset: string, signal?: AbortSignal) => backendStore.list(schemas[dataset].name, signal),
      save: (_dataset, column, expectedRevision) => backendStore.save(schemas[dataset].name, column, expectedRevision),
    };
    return withComputedCapabilityRefresh(store, async (signal) => {
      setCapabilities(await fetchCapabilities(dataset, signal));
      setCapabilityError(undefined);
    });
  }, [dataset, Boolean(visible)]);
  const transport = useMemo(() => shared ? undefined : makeTransport(dataset, find, value => onFindRef.current?.(value), capabilities?.metricCapabilities, capabilities?.queryCapabilities,
    host ? { find: state => hostRef.current?.find(state) ?? "", current: () => hostFind.current } : undefined), [dataset, find, capabilities?.metricCapabilities, capabilities?.queryCapabilities, Boolean(host)]);
  const [bookmarkRevision, setBookmarkRevision] = useState(0);
  useEffect(() => { const changed = () => setBookmarkRevision(value => value + 1); window.addEventListener("pharos:bookmarks", changed); return () => window.removeEventListener("pharos:bookmarks", changed); }, []);
  // A shared file has no origin of its own to keep saved queries apart from other files'.
  const [recovered, setRecovered] = useState(false);
  const storage = useMemo(() => shared ? memoryStorageAdapter() : libraryStorageAdapter({ fresh: new URLSearchParams(location.search).get("fresh") === "1", onRecovery: () => setRecovered(true) }), [bookmarkRevision]);
  const propertyStorage = useMemo(() => jsonPropertyViewStorage(
    key => preferences().get<string | null>(key, null),
    (key, value) => preferences().set(key, value),
  ), []);
  // Resolve an opened calendar search once. Repeated normalization and cursor
  // batches reuse the execution's bounds instead of following a moving clock.
  const openedSearches = useRef(new Map<string, HostQueryState>());
  const scopeSignature = JSON.stringify(scopeWhere);
  const restoredScope = useRef<WhereTerm[]>(history.state?.pharosMachineScopes?.[`q_${registryKey}`] ?? []);
  const scopeRemovals = useMemo(() => new Set([...restoredScope.current, ...(appliedScope?.current ?? []), ...(history.state?.pharosMachineScopes?.[`q_${registryKey}`] ?? []), ...(scopeWhere ?? [])].map(termKey)), [scopeSignature, locationRevision]);
  useEffect(() => { if (appliedScope && scopeWhere) appliedScope.current = scopeWhere; }, [scopeSignature]);
  const canonicalizeQuery = useMemo(() => (query: QueryState) => {
    const hostKey = JSON.stringify(query.hostState);
    const opened = openedSearches.current.get(hostKey);
    const renewedHost = opened ?? refreshTodayHost(query.hostState);
    if (renewedHost && JSON.stringify(renewedHost) !== hostKey) {
      openedSearches.current.set(hostKey, renewedHost);
      openedSearches.current.set(JSON.stringify(renewedHost), renewedHost);
      query = { ...query, hostState: renewedHost, offset: 0 };
    }
    const rolling = readRollingWindow(new URLSearchParams(location.search), dataset);
    const resolved = rolling && rollingQueryMatches(query, rolling) ? resolveRollingQuery(query, rolling) : query;
    const presented = usageActions ? usageActionQuery(orderedUsageQuery(dataset as UsageDataset, resolved), schema, dataset as UsageDataset) : withGroupDetail(resolved);
    if (!scopeWhere) return presented;
    const source = presented.grouping?.source ?? presented;
    const where = [...source.where.filter(term => !scopeRemovals.has(termKey(term))), ...scopeWhere];
    if (JSON.stringify(where) === JSON.stringify(source.where)) return presented;
    return presented.grouping ? { ...presented, grouping: { ...presented.grouping, source: { ...source, where } } } : { ...presented, where };
  }, [dataset, locationRevision, scopeSignature, scopeRemovals, usageActions, schema]);
  const api = useQueryTable<Row>({ schema, transport,
    computedColumnStore, computedExecution: capabilities?.computedExecution,
    clientRows: shared?.datasets[dataset], active: Boolean(visible) && (!remote || Boolean(capabilities || capabilityError)),
    // Keep URL configuration stable; active pauses execution/synchronization
    // without resetting restoration or undo history when a table is hidden.
    storage, debounceMs: 100, syncUrl: true, queryParameter: `q_${registryKey}`, canonicalizeQuery,
    // A bare reload restores the last query that loaded, never one that
    // failed, timed out or was cancelled; that one stays on screen to retry.
    persistLast: "succeeded", hostQuery: host?.query,
    itemDetails: { storage: propertyStorage, ...(detailSchema ? { schema: detailSchema, defaultModes: ["modal", "expand"] as ("modal" | "expand")[] } : {}), relationships: shared ? undefined : relationships,
      loadRow: shared ? undefined : async (id, signal) => {
        const row = await transport!.fetchRow!(id, signal);
        return row ? { row, loadedFields: Object.keys(row) } : null;
      } },
    onRefresh: () => { if (namespace) window.dispatchEvent(new Event("pharos:machine-refresh")); },
  });
  // Apply the opening layout after URL/saved-query restoration, preserving the
  // restored analysis. Native URL sync persists the flag; subsequent manual
  // sizing choices remain available for the rest of this visit.
  const fittedOnOpen = useRef(false);
  useEffect(() => {
    if (!usageActions || !visible || !api.restoration.ready || api.error || fittedOnOpen.current) return;
    fittedOnOpen.current = true;
    if (!api.query.fitToScreen) api.setQuery(previous => ({ ...previous, fitToScreen: true }));
  }, [usageActions, visible, api.restoration.ready, api.error, api.query.fitToScreen]);
  const hostState = api.query.hostState, hostKey = JSON.stringify(hostState ?? null);
  hostFind.current = host ? host.find(hostState) : "";
  const searching = Boolean(find || hostFind.current);
  useEffect(() => { hostRef.current?.onState?.(hostState); }, [hostKey]);
  // A default saved query that the service rejects or that runs out of time
  // is skipped on later visits (storage.ts); one that loads is restored again.
  const receipt = api.execution;
  useEffect(() => {
    if (!visible || shared || !receipt || receipt.status === "running") return;
    const restoration = { defaults: api.defaults, canonicalize: canonicalizeQuery };
    if (receipt.status === "succeeded") acceptDefaultQuery(schema.name, receipt.query, restoration);
    // Only failed rows: a default whose rows load is not skipped for one metric.
    else if (receipt.rows === "failed" && shouldSkipQueryOnRestore(receipt.error)) rejectDefaultQuery(schema.name, receipt.query, restoration);
  }, [visible, receipt?.key, receipt?.status]);
  // Empty states describe the rows: a failed metric alone does not empty them.
  const failed = api.execution ? api.execution.rows === "failed" : Boolean(api.error);
  const resultsView = searchResultsView(api.query, dataset === "library" ? readLibraryHost(hostState).view : "table");
  const selectRows = selectable && !api.query.grouping && resultsView === "table";
  useMachineQueryNavigation(api, Boolean(visible && !shared && (namespace || dataset === "usage")), JSON.stringify([scopeWhere, new URLSearchParams(location.search).get("machine_view")]), `q_${registryKey}`, scopeWhere);
  const apiRef = useRef(api);
  apiRef.current = api;
  const priorScopeSignature = useRef(scopeSignature);
  useEffect(() => {
    if (priorScopeSignature.current !== scopeSignature) {
      priorScopeSignature.current = scopeSignature;
      // Persist the new scope too: a later local edit must not resurrect the
      // previous period or repository from the table's stored query.
      apiRef.current.setQuery(previous => ({ ...canonicalizeQuery(previous), offset: 0 }));
    }
  }, [scopeSignature]);
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
  // The source facade also changes as grouped results arrive. Keep filter
  // metadata effects stable while resolving every intent against its stage.
  const stableSourceFilters = useMemo(() => new Proxy({} as QueryFilterApi<Row>, {
    get(_target, property) {
      const current = apiRef.current;
      return (current.query.grouping ? current.grouping!.source : current)[property as keyof QueryFilterApi<Row>];
    },
  }), []);
  if (!namespace || visible) tableApis.set(registryKey, api);
  useEffect(() => {
    // Work-page links may run before Library's first mount or restoration.
    if (dataset !== "library" || !api.restoration.ready || !pendingLibraryFilter) return;
    const where = pendingLibraryFilter;
    pendingLibraryFilter = undefined;
    // One definition: the work's filters, without the current content search.
    if (api.setDefinition) api.setDefinition(previous => ({ query: { ...previous.query, where, offset: 0 }, hostState: withoutLibrarySearch(previous.hostState) }));
    else api.setQuery(previous => ({ ...previous, where, offset: 0 }));
  }, [dataset, api.restoration.ready, locationRevision]);
  useEffect(() => {
    return () => { tableApis.delete(registryKey); };
  }, [registryKey]);
  useEffect(() => {
    if (!visible || !api.restoration.ready || api.error) return;
    const rolling = readRollingWindow(new URLSearchParams(location.search), dataset);
    if (rolling && !rollingQueryMatches(api.query.grouping?.source ?? api.query, rolling)) {
      for (const name of ["window_days", "window_field", "window_dataset", "window_mode", "window_until_field", "window_timezone"]) updateURI(name, "");
    } else if (rolling?.mode === "today") {
      updateURI("window_timezone", localTimezone());
    } else if (!rolling) {
      const savedWindow = savedRollingWindow(dataset, api.query);
      if (savedWindow) {
        updateURI("window_days", String(savedWindow.days)); updateURI("window_field", savedWindow.field); updateURI("window_dataset", dataset);
        updateURI("window_mode", savedWindow.mode === "today" ? "today" : savedWindow.mode === "relative" ? "relative" : "");
        updateURI("window_timezone", savedWindow.mode === "today" ? localTimezone() : "");
        updateURI("window_until_field", savedWindow.untilField ?? "");
      }
    }
  }, [dataset, api.query, visible, api.restoration.ready, api.error]);

  useEffect(() => {
    const refresh = () => {
      const selected = readRollingWindow(new URLSearchParams(location.search), dataset);
      const current = apiRef.current;
      if (selected?.mode === "today" || refreshTodayHost(current.query.hostState) !== current.query.hostState) current.setQuery(previous => ({ ...resolveRollingQuery(previous, selected?.mode === "today" ? selected : undefined), hostState: refreshTodayHost(previous.hostState), offset: 0 }));
    };
    window.addEventListener("pharos:day-change", refresh);
    return () => window.removeEventListener("pharos:day-change", refresh);
  }, [dataset]);
  // Drilldowns filter source records, including canonical computed fields.
  const sourceSchema = useMemo(() => ({ ...schema, fields: api.computed.catalogue }), [schema, api.computed.catalogue]);
  // Inactive controllers retain their query state, but their controls must not
  // mount: filter nullability/autocomplete and header charts issue metadata
  // requests independently of useQueryTable's active row-fetch flag.
  useOriginMetadata(Boolean(visible));
  if (!visible) return null;
  return <FilterValueProvider value={filterPresentations[dataset]}>
    <QueryRecovery busy={api.execution?.status === "running" || api.restoration.status === "resolving" || (remote && !capabilities && !capabilityError)} error={capabilityError ?? api.error} recovered={recovered} identity={api.execution?.key ?? JSON.stringify(api.query)}
      onRetry={() => {
        const skipped = retryDefaultQuery(schema.name);
        if (recovered) {
          // Run the skipped default. Setting it as the default again (it
          // is) makes the Saved menu read it back.
          setRecovered(false);
          if (skipped) {
            try { api.saved.load(skipped); } catch { /* Query Table reports an unreadable save. */ }
            void api.saved.setDefault(skipped).catch(() => {});
            return;
          }
        }
        if (capabilityError) setCapabilityRetry(value => value + 1); else if (api.restoration.error) void api.restoreLocation(location.href).catch(() => {}); else api.refresh();
      }}
      onReset={() => {
        setRecovered(false);
        const query = tableRecoveryQuery(schema, scopeWhere);
        // Reset clears host clauses too, as the native Reset does.
        if (api.setDefinition && host) api.setDefinition({ query, hostState: host.query.defaults }); else api.setQuery(query);
        if (capabilityError) setCapabilityRetry(value => value + 1);
      }} />
    {!api.query.grouping || namespace ? header?.(api) : null}
    {!shared && !namespace && Object.hasOwn(usageDatasets, dataset) ? <UsageQueryIdeas key={dataset} dataset={dataset as UsageDataset} api={api} /> : null}
    {presets?.(api)}
    {!shared && !namespace && !["tl1_attempts", "findings"].includes(dataset) && rollingWindowField(dataset) ? <label className="usage-chart-field">Time window<select aria-label="Query time window" value={(() => { const choice = readRollingWindow(new URLSearchParams(location.search), dataset); return choice?.mode === "today" ? "today" : choice?.mode === "relative" && choice.days === 1 ? "24h" : "saved"; })()} onChange={event => {
      if (event.target.value === "saved") return;
      const selected = readRollingWindow(new URLSearchParams(location.search), dataset);
      const address = updateQueryWindowURL(queryURL(dataset, api.query, selected), event.target.value === "today" ? "today" : 1, api.query);
      const adjusted = queryFromURL(address)!;
      const params = new URL(address, location.origin).searchParams;
      for (const key of ["window_mode", "window_days", "window_field", "window_dataset", "window_timezone", "window_until_field"]) updateURI(key, params.get(key) || "");
      api.setQuery(adjusted.query);
    }}><option value="saved">Query filters</option><option value="today">Today</option><option value="24h">Last 24 hours</option></select></label> : null}
    {queryContent || resultPresentation ? <SourceQueryBuilder dataset={dataset} compact summary={querySummary?.(api)} api={stableApi} fields={api.query.grouping ? api.schema!.fields : api.computed.catalogue} total={api.total} running={api.loading}
      whereRow={<>
        {resultPresentation?.(api)}
        <QueryFilterRow api={stableSourceFilters} fields={api.query.grouping ? api.grouping!.source.schema.fields : api.computed.catalogue} />
        {queryContent?.(api)}
      </>} /> : api.query.grouping ? <SourceQueryBuilder dataset={dataset} compact api={stableApi} fields={api.schema!.fields} total={api.total} running={api.loading} /> : controls ? controls(api, <SourceQueryBuilder dataset={dataset} compact defaultCollapsed={Boolean(namespace)} api={stableApi} fields={api.computed.catalogue} total={api.total} running={api.loading} />) : recordSearchTargets[dataset] ? <RecordSearchEditor api={stableApi} dataset={dataset} layout={<SourceQueryBuilder dataset={dataset} compact api={stableApi} fields={api.computed.catalogue} total={api.total} running={api.loading} />} /> : <SourceQueryBuilder dataset={dataset} compact api={stableApi} fields={api.computed.catalogue} total={api.total} running={api.loading} />}
    {queryContent ? controls?.(api, null) : null}
    <MetricsPanel api={stableApi} aggregations={api.aggregations} fields={metricFields(api.query.grouping ? usageGroupFields(api.schema!.fields, api.query) : api.computed.catalogue)} renderers={metricRenderers}
      onInspect={(bucket, clause) => { const records = inspectBucketQuery(api.query, bucket, clause, sourceSchema); if (records) api.setQuery(Object.hasOwn(usageDatasets, dataset) ? orderedUsageQuery(dataset as UsageDataset, { ...records, orderBy: [], ...(usageActions ? { fitToScreen: true } : {}) }) : records); }} />
    {capabilityError && !capabilities ? <div className="query-table-error">Could not read this table's query capabilities: {capabilityError.message}</div> : null}
    {api.error ? <div className="query-table-error">{api.error?.message}</div> : null}
    {selectRows ? <SelectionToolbar selection={api.selection} actions={ids => <ShareSelection ids={ids} api={api} transport={transport} execution={capabilities?.computedExecution} />} /> : null}
    {dataset === "library" && resultsView === "conversation" ? <ConversationResults api={api} searching={searching} failed={failed} />
      : dataset === "writing_messages" && messageView === "text" && !api.query.grouping ? <AuthoredMessages api={api} emptyMessage={failed ? failedMessage : emptyMessages[dataset]} /> : <DataTable
      maxHeight={100000}
      {...(selectRows ? { selection: api.selection } : {})}
      fields={api.query.grouping ? usageGroupFields(api.visibleFields, api.query) : api.visibleFields}
      rows={api.rows}
      query={api.query}
      onQueryChange={api.setQuery}
      renderers={tableRenderers}
      rowId={api.rowId}
      columnDrag={api.columnDrag}
      itemDetails={api.itemDetails ?? undefined}
      linkTargets={linkTargets}
      onNavigate={navigateLink}
      total={api.total}
      loading={api.loading}
      emptyMessage={api.loading ? "Loading results…" : failed ? failedMessage : searching ? "No work matches this search and these filters." : emptyMessages[dataset]}
      classNames={{ empty: api.loading ? "pharos-table-loading" : undefined }}
      {...(api.query.grouping ? { trailing: (row: Row) => {
        const complete = api.query.grouping!.keys.every(key => Object.hasOwn(row, groupKeyField(key)));
        const records = complete ? groupSourceQuery(api.query, row, sourceSchema) : null;
        return <button type="button" className="mcp-detail-button" disabled={!records} title={records ? "Open the source records for this group, keeping source filters" : complete ? "These group keys cannot be matched exactly in the source records" : "Include all group key columns to inspect the source records"} onClick={() => { if (records) api.setQuery(Object.hasOwn(usageDatasets, dataset) ? orderedUsageQuery(dataset as UsageDataset, { ...records, orderBy: [], ...(usageActions ? { fitToScreen: true } : {}) }) : records); }}>Records</button>;
      }, trailingLabel: usageActions ? "Drill in" : "Inspect" }
        : trailing ? { trailing: (row: Row) => trailing(row, api), trailingLabel: dataset === "mcp_calls" || dataset === "tool_calls" ? "View" : dataset === "tools" || dataset === "writing" ? "Drill in" : dataset === "library" ? "Match" : "Actions" } : {})}
    />}
  </FilterValueProvider>;
}

// An export holds at most this many works (see shareWorkLimit in share.go).
const shareWorkLimit = 200;

// Share, for the works checked in the Library: one standalone HTML file with
// their conversations and tables. It can also check every work the current
// filters match, when they fit in one export.
function ShareSelection({ ids, api, transport, execution }: { ids: unknown[]; api: QueryTableApi<Row>; transport?: Transport<Row>; execution?: ComputedExecution }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const tooMany = ids.length > shareWorkLimit;
  const total = api.total ?? 0;
  const canFetchRows = Boolean((transport?.queryCapabilities?.version === 3 && transport.fetchQueryV3) || transport?.fetchRowsV2);
  const canSelectAll = Boolean(execution) && canFetchRows && total > ids.length && total <= shareWorkLimit;
  async function selectAll() {
    if (!transport || !execution) return;
    setBusy(true);
    setError("");
    try {
      api.selection.replace(await matchingSelectionIDs(api.query, api.schema ?? schemas.library, execution, transport, shareWorkLimit));
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : "Could not select the matching works");
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
        <div className="conversation-result-meta">{[row.repository_name, row.source_kind, models.join(", "), row.efforts ? `Effort: ${row.efforts}` : "", row.activity_at ? new Date(row.activity_at).toLocaleDateString() : ""].filter(Boolean).join(" · ")}</div>
        <div className="conversation-result-facts">
          <span className="conversation-result-count">{turnCount} turn{turnCount === 1 ? "" : "s"}</span>
          <span className="conversation-result-count">{toolCount} tool use{toolCount === 1 ? "" : "s"}</span>
          <span className="conversation-result-count">{fileCount} file{fileCount === 1 ? "" : "s"} edited</span>
          <span className="conversation-result-count conversation-result-cost" title="API list-price equivalent cost">{usdCost(row.cost_usd, row)} API cost</span>
          {prs.map((pr, index) => {
            const url = pullRequestURL(pr, row.canonical_remote);
            const label = pullRequestLabel(pr);
            return url ? <a key={`${pr.host}:${pr.number}:${index}`} className="conversation-result-count conversation-result-pr" href={url} target="_blank" rel="noopener noreferrer" title={pr.title || label}>{label}</a>
              : <span key={`${pr.host}:${pr.number}:${index}`} className="conversation-result-count" title={pr.title || label}>{label}</span>;
          })}
          {row.main_merge_commit ? <span className="conversation-result-count conversation-result-pr">{mainIntegrationLink(row)}</span> : null}
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

function ConversationResults({ api, searching, failed }: { api: QueryTableApi<Row>; searching: boolean; failed: boolean }) {
  if (!api.loading && !api.rows.length) return <div className="conversation-results-empty no-results">{failed ? failedMessage : searching ? "No work matches this search and these filters." : "No work matches this query."}</div>;
  return <div className={`conversation-results ${api.loading ? "loading" : ""}`} aria-busy={api.loading}>
    {api.loading && !api.rows.length ? <div className="conversation-results-empty">Loading conversations…</div> : null}
    {api.rows.map((row) => <ConversationResultCard key={String(api.rowId(row) ?? row.id)} row={row} />)}
  </div>;
}

type FindHit = { workspace_id: string; conversation_id?: string; title: string; provider?: string; started_at?: string; message_id?: string; role?: string; message_kind?: string; snippet?: string; path?: string; old_path?: string; url?: string; url_source?: string; tool_name?: string; tool_call_id?: string; attribution?: string; count: number };
// What the Library's keyword search found within the stored table filters.
type SearchProgress = { complete: boolean; stop_reason: string; stop_message?: string; next_cursor: string; count: number; count_relation: string; candidates: number; verified_bytes: number; skipped_oversize: number; elapsed_ms: number; strategy: string; coverage: string; expansions?: Record<string,string[]> };
type FindSummary = { workspaces: number; limited: boolean; search?: SearchProgress };

function openFindHit(hit: FindHit) {
  const params = new URLSearchParams();
  if (hit.conversation_id) params.set("conversation", hit.conversation_id);
  if (hit.message_id) params.set("message", hit.message_id);
  history.pushState(null, "", `/work/${encodeURIComponent(hit.workspace_id)}${params.size ? `?${params}` : ""}`);
  (window as unknown as { routeFromLocation?: () => void }).routeFromLocation?.();
}

// Conversation ID cells link to /conversation/{id}, which the service
// redirects to the conversation within its work; a plain click resolves the
// same route in place.
async function openConversation(id: string) {
  const fallback = `/conversation/${encodeURIComponent(id)}`;
  try {
    const [resolved] = await relationships.resolve([{ objectType: "pharos_conversations", key: { id } }]);
    if (!resolved?.href) { location.assign(fallback); return; }
    history.pushState(null, "", resolved.href);
    (window as unknown as { routeFromLocation?: () => void }).routeFromLocation?.();
  } catch { location.assign(fallback); }
}

function findHitLabel(hit: FindHit): string {
  if (hit.url) return `Open tool call${hit.tool_name ? ` · ${hit.tool_name}` : ""}`;
  if (hit.path) return hit.attribution === "workspace" ? "Open work · conversation unknown" : "Open conversation";
  if (hit.role === "user") return "Open message from a person";
  if (["thinking", "reasoning", "analysis"].includes(hit.message_kind ?? "")) return "Open saved agent reasoning";
  if (hit.role === "assistant" && hit.message_kind === "message") return "Open agent reply";
  return "Open matching message";
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

// The older Human messages table runs a message search; its definition is host
// query state (message_query). A cursor continues one search's batches, so it
// belongs to that search and stays out of the definition (message_cursor).
// Each request takes the cursor only when it continues the request's own
// search, so a request never pairs one search with another's cursor.
function urlMessageSearch(): string {
  const token = new URLSearchParams(location.search).get("message_query");
  try { return token ? messageFind(messageHostQuery.normalize!({ version: 1, params: { message_query: [token] } })) : ""; } catch { return ""; }
}
const urlMessageCursor = () => ({ search: urlMessageSearch(), value: new URLSearchParams(location.search).get("message_cursor") ?? "" });
type MessageCursor = { search: string; value: string };
function UserMessageSurface({ header, messageView }: { messageView: MessageView; header: (api: QueryTableApi<Row>, searching: boolean) => React.ReactNode }) {
  if (shared) return <QuerySurface dataset="writing_messages" messageView={messageView} header={api => header(api, false)} />;
  const [hostState, setHostState] = useState<HostQueryState>();
  const [cursor, setCursorState] = useState<MessageCursor>(urlMessageCursor);
  const cursorRef = useRef(cursor);
  const setCursor = (next: MessageCursor) => { cursorRef.current = next; setCursorState(next); };
  const host = useMemo((): SurfaceHost => ({ query: messageHostQuery, onState: setHostState, find: state => {
    const search = messageFind(state), current = cursorRef.current;
    return search && current.value && current.search === search ? `${search}&${new URLSearchParams({ find_cursor: current.value })}` : search;
  } }), []);
  const search = messageFind(hostState);
  const activeCursor = search && cursor.search === search ? cursor.value : "";
  // A cursor change runs the same definition again, from its first page.
  const rerun = (api: QueryTableApi<Row>, next: MessageCursor) => { setCursor(next); if (api.query.offset) api.setQuery(previous => ({ ...previous, offset: 0 })); else api.refresh(); };
  const [found, setFound] = useState<FindSummary | null>(null);
  useEffect(() => {
    const restore = () => { if (datasetIsVisible("writing_messages")) setCursor(urlMessageCursor()); };
    window.addEventListener("pharos:route", restore); return () => window.removeEventListener("pharos:route", restore);
  }, []);
  useEffect(() => { if (datasetIsVisible("writing_messages")) updateURI("message_cursor", activeCursor); }, [activeCursor]);
  useEffect(() => setFound(null), [search, activeCursor]);
  return <QuerySurface dataset="writing_messages" host={host} messageView={messageView} header={api => header(api, !!search)} onFind={setFound} controls={(api, builder) => <>
    <MessageSearchClause api={api} layout={builder} onSearch={() => setCursor({ search: "", value: "" })} />
    {search && found?.search ? <div className="library-find-status" role="status"><p>{found.search.complete ? "All eligible messages checked." : "Partial search batch; more messages may match."} Table filters and metrics apply to the matching messages in this batch. Ordering is within this batch.</p><p>{found.search.elapsed_ms} ms · {found.search.candidates.toLocaleString()} candidates checked{found.search.skipped_oversize ? ` · ${found.search.skipped_oversize} oversized messages skipped` : ""}.</p>{found.search.stop_message ? <p>{found.search.stop_message}</p> : null}{found.search.next_cursor ? <button type="button" onClick={() => rerun(api, { search, value: found.search!.next_cursor })}>Next message search batch</button> : null}{withheldBatch(found.search) ? <button type="button" onClick={() => api.refresh()}>Retry this batch</button> : null}{activeCursor ? <button type="button" onClick={() => rerun(api, { search: "", value: "" })}>Restart message search</button> : null}</div> : null}
  </>} />;
}

function MessageSearchClause({ api, layout, onSearch }: { api: QueryTableApi<Row>; layout: React.ReactNode; onSearch: () => void }) {
  const state = api.query.hostState, key = JSON.stringify(state ?? null);
  const applied = useMemo(() => readMessageSearchHost(state), [key]);
  const value = useMemo(() => applied ?? newMessageSearch(), [applied]);
  const [error, setError] = useState("");
  // One edit, the search with its record filters and order, is one definition.
  const commit = (next: TextSearchDraft | null, table: Partial<QueryState> = {}) => {
    try {
      const hostState = withMessageSearch(next);
      const same = sameHostState(messageHostQuery.normalize!(hostState), state);
      api.setDefinition?.(previous => ({ query: { ...previous.query, ...table, offset: 0 }, hostState }));
      if (same && next) api.refresh();
      onSearch(); setError("");
    } catch (failure) { setError(failure instanceof Error ? failure.message : String(failure)); }
  };
  return <>
    <MessageTableSearch api={api} value={value} active={!!applied} onApply={(next, table) => commit(next, table)} onClear={() => commit(null)} layout={layout} />
    {error ? <p className="query-table-error" role="alert">{error}</p> : null}
  </>;
}

// Search's content search (messages, files or URLs) and its result view are
// host query state (host-query-state.ts), saved and undone with the table query.
function LibraryPage() {
  const [hostState, setHostState] = useState<HostQueryState>();
  const host = useMemo((): SurfaceHost => ({ query: libraryHostQuery, find: shared ? () => "" : libraryFind, onState: setHostState }), []);
  const find = shared ? "" : libraryFind(hostState);
  const [found, setFound] = useState<FindSummary | null>(null);
  useEffect(() => { setFound(null); }, [find]);
  return <div className={`pharos-query-page${find ? " library-searching" : ""}`}>
    <div className="view-heading"><div><h1>{shared ? "Conversations" : "Search"}</h1>{!shared ? <p className="muted">Find past work. Follow a conversation, compare projects, or build a view of your own.</p> : null}</div></div>
    <QuerySurface dataset="library" host={host} resultPresentation={api => <SearchResultsDisplay view={searchResultsView(api.query, readLibraryHost(api.query.hostState).view)} grouped={!!api.query.grouping}
      onChange={next => api.setDefinition?.(previous => ({ query: queryForSearchResultsView(previous.query, next), hostState: libraryHostState({ ...readLibraryHost(previous.hostState), view: next }) }))} />}
      onFind={setFound} selectable={!shared} {...(find ? { trailing: (row: Row) => <FindMatchCell row={row} /> } : {})} queryContent={shared ? undefined : api => <LibrarySearchClause api={api} />} controls={shared ? undefined : () => <>
    {find && found ? <div className="library-find-status muted" role="status">
      <p>Limit controls workspace rows after filtering and sorting. Counts and metrics include the full matching population.</p>
      {found.workspaces === 0 ? <p>No messages matched these conditions. Try switching “and” to “or”, remove a condition, or choose a broader target.</p> : null}
      {found.search ? <details><summary>Search details</summary><p>{found.search.count.toLocaleString()} matching messages; {found.search.elapsed_ms} ms. All eligible messages checked.</p><p>{found.search.coverage}</p>{found.search.expansions && Object.keys(found.search.expansions).length ? <p>Word variations: {Object.entries(found.search.expansions).map(([word, terms]) => `${word} → ${terms.join(", ")}`).join("; ")}</p> : null}</details> : null}
    </div> : null}
    </>} />
  </div>;
}

function LibrarySearchClause({ api }: { api: QueryTableApi<Row> }) {
  const state = api.query.hostState, key = JSON.stringify(state ?? null);
  // Read once per definition, so the editor's rule IDs stay stable.
  const current = useMemo(() => readLibraryHost(state), [key]);
  const blank = useMemo(() => newKeywordSearch(), [key]);
  const [fileDraft, setFileDraft] = useState(current.file);
  const [fileCaseDraft, setFileCaseDraft] = useState(current.fileCase);
  useEffect(() => { setFileDraft(current.file); setFileCaseDraft(current.fileCase); }, [current]);
  const [error, setError] = useState("");
  // Applying the search already in effect runs it again.
  const commit = (change: Partial<LibraryHost>, rerun = false) => {
    try {
      const hostState = libraryHostState({ ...current, ...change });
      if (rerun && sameHostState(libraryHostQuery.normalize!(hostState), state)) api.refresh();
      else api.setDefinition?.(previous => ({ query: { ...previous.query, offset: 0 }, hostState }));
      setError("");
      return true;
    } catch (failure) { setError(failure instanceof Error ? failure.message : String(failure)); return false; }
  };
  return <ExploreSearch kind={current.kind} active={Boolean(libraryFind(state))}
    summary={current.kind === "text" && current.text ? searchSentence(current.text).parts.map(part => typeof part === "string" ? part : part.value).join("") : `${current.file}${current.fileCase ? " · case sensitive" : ""}`}
    onClear={() => commit({ kind: "text", text: null, file: "", fileCase: false })}>
    {(close, draftKind, setDraftKind) => <TextSearchEditor compact sentence={false} value={current.text ?? blank} active={!!current.text} kind={draftKind} onKind={setDraftKind}
      onApply={next => { if (commit({ kind: "text", text: next }, true)) close(); }} onClear={() => commit({ text: null })}
      file={{ text: fileDraft, caseSensitive: fileCaseDraft, active: !!current.file, dirty: fileDraft.trim() !== current.file || fileCaseDraft !== current.fileCase, onText: setFileDraft, onCase: setFileCaseDraft, onApply: () => { if (commit({ kind: draftKind, file: fileDraft.trim(), fileCase: fileCaseDraft }, true)) close(); }, onClear: () => { setFileDraft(""); commit({ file: "", fileCase: false }); } }}
      context={<><p className="explore-search-context">Search inside retained messages, file changes, or visited URLs. Use <b>where</b> in the query builder to filter workspaces.</p>{error ? <p className="text-editor-error" role="alert">{error}</p> : null}</>}
      workspaceScope={{ filters: [] }} orderControl={null} />}
  </ExploreSearch>;
}

const sum = (field: string, groupBy: string[], label: string): AggregationClause => ({ id: `${field}:${groupBy.join(",")}`, op: "sum", field, groupBy, label });

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

// Copying acknowledges new models, but doesn't resolve missing prices. Keep
// the refresh prompt available for retries and routine price-history updates.
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
  if (!status) return null;
  return <div className="refresh-prices-notice" role="status">
    <button type="button" className={`refresh-prices${fresh.length ? " attention" : ""}`} title={unpriced.length ? `Unpriced models: ${unpriced.map((model) => model.model).join(", ")}` : "Research current API prices and model aliases"} onClick={() => void copy()}>
      {copied === "copied" ? "Prompt copied" : copied === "failed" ? "Copy failed" : "Copy refresh prices prompt"}
      {!copied && fresh.length ? <span className="refresh-prices-count" aria-label={`${fresh.length} new unpriced models`}>{fresh.length}</span> : null}
    </button>
    {unpriced.length ? <p>{fresh.length ? (fresh.length === 1 ? "A new model has" : `${fresh.length} new models have`) : (unpriced.length === 1 ? "A model still has" : `${unpriced.length} models still have`)} no confirmed price definition. Cost totals may be incomplete until pricing is updated.</p> : null}
  </div>;
}

// Charts change their table's filters through this.
function filterChange(api: QueryTableApi<Row>): FilterChange {
  return (change, preset) => {
    const dataset = [...tableApis.entries()].find(([, candidate]) => candidate === api)?.[0] as Dataset | undefined;
    if (dataset && preset && rollingWindowField(dataset)) {
      updateURI("window_mode", preset === "today" ? "today" : "relative");
      updateURI("window_days", preset === "today" ? "1" : String(rangeDays(preset as ChartRange)));
      updateURI("window_field", dataset === "usage" ? "last_usage_at" : dataset === "writing" ? "last_message_at" : rollingWindowField(dataset)!);
      updateURI("window_until_field", dataset === "usage" ? "first_usage_at" : dataset === "writing" ? "first_message_at" : "");
      updateURI("window_dataset", dataset);
      updateURI("window_timezone", preset === "today" ? localTimezone() : "");
      if (preset === "all") for (const key of ["window_mode", "window_days", "window_field", "window_until_field", "window_dataset", "window_timezone"]) updateURI(key, "");
    }
    api.setQuery(previous => ({ ...previous, where: change(previous.where), offset: 0 }));
  };
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
  if (new URLSearchParams(location.search).get("fresh") === "1") return fallback;
  let saved = preferences().get<unknown>(key, {});
  const chart = new URLSearchParams(location.search).get("analyze_chart");
  if (chart && [tokenChartKey, writingViewKey].includes(key)) {
    try { saved = JSON.parse(chart); } catch { /* Keep the saved default for invalid chart state. */ }
  }
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

// Fetches metrics for the given filters from /metrics, or from a shared file's
// rows, refetching when either changes or Usage is refreshed. Results are
// tagged so a stale response never renders under new chart settings.
function useMetrics(dataset: Dataset, where: WhereTerm[], clauses: AggregationClause[], largerBudget = false) {
  const key = JSON.stringify({ where, clauses });
  const [state, setState] = useState<{ key: string; refresh: number; metrics: MetricResult[]; error: string; loading: boolean }>({ key: "", refresh: 0, metrics: [], error: "", loading: true });
  const [refresh, setRefresh] = useState(0);
  useEffect(() => onDatasetRefresh(dataset, () => setRefresh(value => value + 1)), [dataset]);
  useEffect(() => {
    const request = JSON.parse(key) as { where: WhereTerm[]; clauses: AggregationClause[] };
    if (!request.clauses.length) { setState({ key, refresh, metrics: [], error: "", loading: false }); return; }
    if (shared) {
      const rows = shared.datasets[dataset];
      const result = dataset === "usage"
        ? applyUsageAggregations(rows, shared.datasets.usage_hourly ?? [], { where: request.where, aggregations: request.clauses }, schemas.usage)
        : applyAggregations(rows, { ...EMPTY_QUERY, where: request.where, aggregations: request.clauses }, schemas[dataset]);
      setState({ key, refresh, metrics: result.metrics as MetricResult[], error: "", loading: false });
      return;
    }
    const controller = new AbortController();
    setState(previous => ({ ...previous, loading: true }));
    postMetrics(dataset, schemas[dataset], request.where, request.clauses, controller.signal)
      .catch(failure => {
        // Guided record graphs can exceed /metrics's generic group budget.
        // Use the same native V3 fallback as the existing Machine Time view.
        if (largerBudget && failure instanceof MetricRequestError && failure.resourceLimit) return postQueryV3Metrics(dataset, schemas[dataset], request.where, request.clauses, controller.signal);
        throw failure;
      })
      .then(metrics => setState({ key, refresh, metrics, error: "", loading: false }))
      .catch(failure => { if (!controller.signal.aborted) setState({ key, refresh, metrics: [], error: failure instanceof Error ? failure.message : String(failure), loading: false }); });
    return () => controller.abort();
  }, [dataset, key, refresh, largerBudget]);
  return { ...state, metrics: state.key === key ? state.metrics : [], loading: state.key !== key || state.refresh !== refresh || state.loading, error: state.key === key ? state.error : "" };
}

const tokenChartKey = "pharos-usage-chart";
const costTick = (value: number) => Math.abs(value) >= 1e4 ? `$${compactNumber(value)}` : `$${value.toLocaleString(undefined, { maximumFractionDigits: 2 })}`;
// Tokens and Cost amounts; shares and ratios are formatted by the trend.
const tokenAmount: AmountFormat = (value, measure) => measure.unit === "USD" ? formatUSD(value) : compactNumber(value);
const tokenTick: AmountFormat = (value, measure) => measure.unit === "USD" ? costTick(value) : compactNumber(value);
// The Usage page's chart shortcuts name a measure shown as values.
const tokenChartPresets: UsagePreset[] = usageChartPresets.map(preset => ({ ...preset, chart: { ...preset.chart, share: false } }));

function PresetRow({ label, presets, query, chart, onApply, clear }: { label: string; presets: UsagePreset[]; query: QueryTableApi<Row>["query"]; chart?: TokenChartView; onApply: (preset: UsagePreset | null) => void; clear?: boolean }) {
  return <div className="usage-presets" role="group" aria-label={`${label} presets`}>
    <span className="usage-preset-label">{label}</span>
    {presets.map(preset => <QueryChip key={preset.label} title={preset.label} description={preset.title} active={presetMatches(preset, query, chart)} onClick={() => onApply(preset)} />)}
    {clear ? <button type="button" className="usage-preset-clear" onClick={() => onApply(null)}>Clear metrics</button> : null}
  </div>;
}

function TokenUsage({ scopeWhere, appliedScope, onTimeRangeChange }: { scopeWhere?: WhereTerm[]; appliedScope?: React.MutableRefObject<WhereTerm[]>; onTimeRangeChange?: () => void } = {}) {
  const [chart, setChart] = useState<TokenTrendView>(() => readStored(tokenChartKey, defaultTokenChart as TokenTrendView, view => validAnalysisState(tokenAnalysisCatalog, { ...view, view: "trend" })));
  const update = (next: Partial<TokenTrendView>) => setChart(previous => { const merged = { ...previous, ...next }; store(tokenChartKey, merged); return merged; });
  return <QuerySurface dataset="usage" scopeWhere={scopeWhere} appliedScope={appliedScope} header={api => {
    const where = toAggregationQuery({ ...api.query, aggregations: [] }, schemas.usage).where;
    return <>
      <p className="muted machine-token-note">Reconciled tokens by session, model, and effort. Input includes cached tokens. Cost is the API list-price equivalent at standard rates; subscriptions and long-context premiums are excluded. ≈ marks costs priced by assumption.</p>
      <TokenSummary where={where} />
      <AnalysisTrend catalog={tokenAnalysisCatalog} schema={schemas.usage} dataset="usage" state={chart} where={where} onChange={update} onFilter={filterChange(api)} onTimeFilter={(change, preset) => {
        // A chart date selection replaces the page period. Release its scope
        // before updating the query so normalization cannot restore that limit.
        onTimeRangeChange?.();
        filterChange(api)(change, preset);
      }} local={tokenSharedRows} format={tokenAmount} tick={tokenTick} links={false} />
      <PresetRow label="Chart" presets={tokenChartPresets} query={api.query} chart={normalizeAnalysis(tokenAnalysisCatalog, { ...chart, view: "trend" }) as TokenChartView} onApply={preset => { if (preset?.chart) update(preset.chart as Partial<TokenTrendView>); }} />
    </>;
  }} />;
}


function TokenSummary({ where }: { where: WhereTerm[] }) {
  const result = useMetrics("usage", where, tokenSummaryMetrics);
  const values = new Map(result.metrics.map(metric => [metric.id, Number(metric.buckets[0]?.value) || 0]));
  const value = (field: string) => values.get(`${field}:`) ?? 0;
  const tokens = value("total_tokens"), cost = value("cost_usd"), uncached = value("uncached_input_tokens");
  const hasCost = result.metrics.find(metric => metric.id === "cost_usd:")?.buckets[0]?.value != null;
  const reads = value("cache_read_input_tokens"), output = value("output_tokens"), sessions = values.get("agent_sessions") ?? 0;
  const cacheHit = uncached + reads > 0 ? `${Math.round(reads / (uncached + reads) * 100)}%` : "—";
  const card = (label: string, amount: string, detail: string) => <div className="mcp-metric"><span>{label}</span><strong>{amount}</strong><small>{detail}</small></div>;
  return <div className="mcp-metrics usage-cards token-summary" aria-label="Machine token summary" aria-busy={result.loading}>
    {card("Total tokens", result.loading ? "Loading…" : result.error ? "Unavailable" : compactNumber(tokens), "all reported input and output")}
    {card("API price equivalent", result.loading ? "Loading…" : result.error ? "Unavailable" : tokens && !hasCost ? "Unpriced" : formatUSD(cost), "list prices · may include assumed/partial prices; unpriced usage excluded")}
    {card("Cache hit rate", result.loading ? "Loading…" : result.error ? "Unavailable" : cacheHit, `${compactNumber(reads)} cached input tokens read`)}
    {card("Output tokens", result.loading ? "Loading…" : result.error ? "Unavailable" : compactNumber(output), "including reported reasoning output")}
    {card("Agent sessions", result.loading ? "Loading…" : result.error ? "Unavailable" : sessions.toLocaleString(), "distinct sessions in the current filters")}
  </div>;
}

// The summary is of the dashboard's population: a legend selection only
// chooses what the chart draws, and the summary says so.
function AgentTimeSummary({ where, selected = false }: { where: WhereTerm[]; selected?: boolean }) {
  const result = useMetrics("tool_calls", where, [
    { id: "duration", op: "sum", field: "duration_ms", groupBy: [] },
    { id: "calls", op: "count", groupBy: [] },
    { id: "timed", op: "count", field: "duration_ms", groupBy: [] },
  ]);
  const value = (id: string) => Number(result.metrics.find(metric => metric.id === id)?.buckets[0]?.value) || 0;
  const duration = value("duration"), calls = value("calls"), timed = value("timed");
  const time = (ms: number) => ms >= 3600000 ? `${(ms / 3600000).toLocaleString(undefined, { maximumFractionDigits: 1 })} h` : ms >= 60000 ? `${(ms / 60000).toLocaleString(undefined, { maximumFractionDigits: 1 })} min` : `${(ms / 1000).toLocaleString(undefined, { maximumFractionDigits: 1 })} s`;
  const card = (label: string, amount: string, detail: string) => <div className="mcp-metric"><span>{label}</span><strong>{result.loading ? "Loading…" : result.error ? "Unavailable" : amount}</strong><small>{detail}</small></div>;
  return <div className="mcp-metrics dashboard-stats" aria-label="Agent Time summary" aria-busy={result.loading}>
    {card("Total recorded tool time", timed ? time(duration) : "—", `all activities in range${selected ? ", not only the legend selection" : ""} · concurrent calls can overlap`)}
    {card("Call volume", calls.toLocaleString(), "all matching calls, including those without timing")}
    {card("Time per timed run", timed ? time(duration / timed) : "—", `${timed.toLocaleString()} calls with a recorded duration`)}
    {card("Timing coverage", calls ? `${(100 * timed / calls).toLocaleString(undefined, { maximumFractionDigits: 1 })}%` : "—", `${(calls - timed).toLocaleString()} calls without timing`)}
  </div>;
}

type WritingCounts = Record<string, number | string | null>;
type WritingSeries = { works: number; daily: WritingCounts[]; hourly?: WritingCounts[]; totals: WritingCounts };
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

type WritingSplit = "channels" | "machines" | "execution_machines" | "kind" | "repository_name" | "source_kind" | "providers";
const writingSplits: Array<[WritingSplit, string]> = [["kind", "Kind"], ["channels", "Channels"], ["machines", "Machines"], ["execution_machines", "Execution machines"], ["repository_name", "Repository"], ["source_kind", "App"], ["providers", "Provider"]];
type WritingSplitDay = { day: string; value: string | null; typed_words: number; words: number };

function writingSplitLabel(split: WritingSplit, value: string): string {
  if (["channels", "machines", "execution_machines"].includes(split)) return value.split(", ").map(v => originLabel(split === "channels" ? "channel" : "machine_id", v === "Unknown" ? null : v)).join(" + ");
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
  return { ...data, series: data.key === key ? data.series : null, loading: data.key !== key || data.loading, error: data.key === key ? data.error : "" };
}

// The chart and totals cover only the days inside the time filter. Split by
// a row field, the key filters the table as on the tokens chart; the input
// kinds are not rows, so clicking one only narrows the chart.
const defaultWritingChart = { period: "day" as ChartPeriod, scope: "typed" as "typed" | "all", split: "kind" as WritingSplit, hours: 6, days: 3, rollingPeriods: 0, scale: defaultChartScale, display: "interval" as ChartDisplay };
type WritingChartView = typeof defaultWritingChart & ChartPresentation;
const validWritingChart = (value: WritingChartView) => Object.hasOwn(chartPeriods, value.period) && validPeriodCount(value.hours) && validPeriodCount(value.days) && ["typed", "all"].includes(value.scope) && writingSplits.some(([option]) => option === value.split) && validRollingPeriods(value.rollingPeriods) && validChartScale(value.scale) && validChartDisplay(value.display) && validChartPresentation(value);
// The legacy Human Words page's chart, over the series endpoint: its table
// lists works (the writing dataset), whose filters the chart follows. The
// Human Words dashboard reads messages through the analysis engine.
function WritingPanel({ where, onFilter, status, reload }: { where: WhereTerm[]; onFilter: FilterChange; status: AuthorshipStatus | null; reload: number }) {
  const originRevision = useOriginMetadata();
  const [view, setView] = useState<WritingChartView>(() => readStored(writingViewKey, { ...defaultWritingChart, period: "week" as ChartPeriod }, validWritingChart));
  const update = (next: Partial<typeof view>) => setView(previous => { const merged = { ...previous, ...next }; store(writingViewKey, merged); return merged; });
  const kinds = view.selection ?? { values: [], excluded: false };
  const seriesLimit = view.seriesLimit ?? 15;
  const periodCount = chartPeriodCount(view);
  const cumulative = view.display === "cumulative";
  const hourly = isHourly(view.period);
  // Resolved timestamps are part of the series request key. Keep one clock
  // through loading/data renders; renew it when filters change or refresh.
  const clock = useMemo(() => Date.now(), [JSON.stringify(where), reload]);
  const bounds = timeBounds(where, writingTime, clock);
  const window = { hourly, start: bounds.from?.toISOString(), end: bounds.to?.toISOString(), from: bounds.from ? localDay(bounds.from) : undefined, to: bounds.to ? localDay(new Date(bounds.to.getTime() - 1)) : undefined };
  const split = view.split === "kind" ? null : view.split;
  const selection = split ? readSlice(where, split) : kinds;
  const chosen = selection.values.map(value => value ?? "");
  const chartWhere = split ? where.filter(term => !isSliceTerm(term, split)) : where;
  const requestedWhere = smoothingWhere(chartWhere, writingTime, view.period, cumulative ? 0 : view.rollingPeriods, periodCount, clock);
  const requestedBounds = timeBounds(requestedWhere, writingTime, clock);
  const chartWindow = { hourly, start: requestedBounds.from?.toISOString(), end: requestedBounds.to?.toISOString(), from: requestedBounds.from ? localDay(requestedBounds.from) : undefined, to: requestedBounds.to ? localDay(new Date(requestedBounds.to.getTime() - 1)) : undefined };
  // Without a key filter, one request answers both the totals and the chart.
  const separate = chartWhere.length !== where.length || requestedWhere !== chartWhere;
  const data = useWritingSeries({ where, ...window, ...(split && !separate ? { split } : {}) }, reload);
  const chart = useWritingSeries(separate ? { where: requestedWhere, ...(split ? { split } : {}), ...chartWindow } : null, reload);
  const chartData = separate ? chart : data;
  const series = data.series, totals = series?.totals ?? {};
  const groups = view.scope === "typed" ? writingGroups.slice(0, 1) : writingGroups;
  const { chartSeries, buckets, cumulativeStart } = useMemo(() => {
    const timeline = hourly ? chartData.series?.hourly ?? [] : chartData.series?.daily ?? [];
    const days = split ? (chartData.series?.split ?? []).map(entry => entry.day) : timeline.map(day => day.day);
    const buckets = chartBucketsWithHistory(view.period, ...chartSpan(bounds, earliestKey(days)), periodCount, days.map(day => parseDay(String(day))),
      !cumulative && view.rollingPeriods > 1 ? requestedBounds.from ?? earliestKey(days) : undefined), byKey = new Map(buckets.map(bucket => [bucket.key, bucket])), messages = new Map<string, number>();
    const cumulativeStart = bounds.from ?? earliestKey(days);
    const bucketOf = (day: unknown) => plottedBucketKey(parseDay(String(day)), view.period, periodCount, buckets, view.display);
    for (const day of timeline) {
      const bucket = byKey.get(bucketOf(day.day));
      if (!bucket) continue;
      messages.set(bucket.key, (messages.get(bucket.key) ?? 0) + (Number(day.typed_messages) || 0));
      if (!split) for (const group of writingGroups) bucket.values[group.key] = (bucket.values[group.key] ?? 0) + groupTotal(day, group, "words");
    }
    for (const bucket of buckets) bucket.detail = `${(messages.get(bucket.key) ?? 0).toLocaleString()} messages with typed text`;
    if (!split) return { chartSeries: groups.map(group => ({ key: group.key, label: group.label, color: group.color, value: group.key })), buckets, cumulativeStart };
    const rows = (chartData.series?.split ?? []).map(entry => [bucketOf(entry.day), entry.value ?? "", Number(view.scope === "typed" ? entry.typed_words : entry.words) || 0] as [string, string, number]);
    return { chartSeries: foldSeries(rows, byKey, name => writingSplitLabel(split, name), undefined, chosen, seriesLimit, `writing:${split}`), buckets, cumulativeStart };
  }, [series, chartData.series, originRevision, split, view.scope, view.period, periodCount, hourly, bounds.from?.getTime(), bounds.to?.getTime(), chosen.join("\u0000"), seriesLimit, view.rollingPeriods, cumulative]);
  useUsageChartPalette(split, chartSeries);
  const selected = chartSeries.filter(item => item.value !== undefined && chosen.includes(item.value)).map(item => item.key);
  function select(key: string | null, action: LegendAction) {
    const value = key === null ? undefined : chartSeries.find(item => item.key === key)?.value;
    if (!split) update({ selection: updateSlice(kinds, value ?? "", action) });
    else onFilter(current => withSlice(current, split, updateSlice(readSlice(current, split), value || null, action)));
  }
  const typed = Number(totals.typed_words) || 0, pasted = Number(totals.pasted_words) || 0, typedMessages = Number(totals.typed_messages) || 0;
  const since = localDay(new Date(Date.now() - 29 * 86_400_000));
  const recent = (series?.daily ?? []).filter(day => String(day.day) >= since);
  const recentTyped = recent.reduce((sum, day) => sum + (Number(day.typed_words) || 0), 0), recentMessages = recent.reduce((sum, day) => sum + (Number(day.typed_messages) || 0), 0);
  const building = status !== null && !status.built_at;
  const card = (label: string, value: string, detail: string) => <div className="mcp-metric"><span>{label}</span><strong>{value}</strong><small>{detail}</small></div>;
  const unit = periodLabel(view.period, periodCount);
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
    <StackedColumns title={`${cumulative ? "Cumulative " : ""}${view.scope === "typed" ? "Typed words" : "Words of user input"}${cumulative ? "" : ` per ${unit}`}${split ? ` by ${writingSplits.find(([value]) => value === split)?.[1].toLowerCase()}` : ""}`}
      controls={<>
        <ChartSelect label="Metric" value={view.scope} options={[["typed", "Typed words"], ["all", "All user input"]]} onChange={scope => update({ scope })} />
        <ChartSelect label="Breakdown" value={view.split} options={writingSplits.map(([value, text]) => [value, value === "kind" ? "Text source" : text])}
          title="How each chart column is divided" onChange={next => update({ split: next, seriesLimit: 15, selection: { values: [], excluded: false } })} />
        <DisplayControl value={view.display} onChange={display => update({ display })} />
        <RangeControl bounds={bounds} onPresetChange={(range, oldest) => update(presetPeriod(range, oldest))} onChange={(from, to, preset) => onFilter(current => withTimeRange(current, writingTime, from, to, preset), preset)} />
        <ScaleControl value={view.scale} onChange={scale => update({ scale })} />
        <PeriodControl view={view} onChange={update} />
        <RollingControl value={view.rollingPeriods} display={view.display} period={view.period} count={periodCount} onChange={rollingPeriods => update({ rollingPeriods })} />
      </>}
      series={chartSeries} buckets={buckets} period={view.period} periodCount={periodCount} rollingPeriods={view.rollingPeriods} display={view.display} cumulativeStart={cumulativeStart} scale={view.scale} legend={chartSeries.length > 0}
      format={value => value.toLocaleString()} tickFormat={compactNumber} noun="words" loading={chartData.loading} error={chartData.error}
      selected={selected} excluded={selection.excluded} onSelect={select} onExpand={() => update({ seriesLimit: Math.min(1000, seriesLimit + 5) })} onRange={pickRange(onFilter, writingTime, view.period, periodCount, period => update({ period }))}
      seriesHref={(range, item) => dashboardChartURL({ dataset: "writing", where: range.rolling ? requestedWhere : where, time: writingTime, range, series: item, split,
        fields: split ? [view.scope === "typed" ? "typed_words" : "user_words"] : writingGroups.find(group => group.key === item.key)?.categories.map(category => `${category}_words`),
        current: new URLSearchParams(location.search) })} />
    <div className="writing-mix">
      <h3>Where user-turn text came from</h3>
      <p className="muted">All classified user-turn text in the selected dates and conversation filters. Shares use characters.</p>
      {!allChars ? <p className="muted">{data.loading ? "Loading…" : "No classified user messages match these filters."}</p> : <>
        <div className="writing-stack" role="img" aria-label={writingGroups.map(group => `${group.label} ${Math.round(100 * groupTotal(totals, group, "chars") / allChars)}%`).join(", ")}>
          {writingGroups.map(group => { const value = groupTotal(totals, group, "chars"); return value ? <i key={group.key} style={{ flex: value, background: group.color }} title={`${group.label}: ${Math.round(100 * value / allChars)}%`} /> : null; })}
        </div>
        <div className="writing-table-scroll" role="region" aria-label="User-turn text sources" tabIndex={0}><table className="writing-table">
          <thead><tr><th>Source</th><th className="num">Words</th><th className="num">Characters</th><th className="num">Share of text</th></tr></thead>
          <tbody>{writingGroups.map(group => { const chars = groupTotal(totals, group, "chars"); return <tr key={group.key}>
            <td><span className="usage-swatch" style={{ background: group.color }} /><strong>{group.label}</strong><div className="muted">{group.description}</div></td>
            <td className="num">{groupTotal(totals, group, "words").toLocaleString()}</td><td className="num">{chars.toLocaleString()}</td><td className="num">{(100 * chars / allChars).toFixed(1)}%</td>
          </tr>; })}</tbody>
        </table></div>
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
    updateURI(queryParameter("writing_messages"), encodeQuery(orderedUsageQuery("writing_messages", { ...EMPTY_QUERY, limit: schemas.writing_messages.defaultLimit ?? EMPTY_QUERY.limit, where })));
    chooseRows("messages");
  }
  const toggle = <div className="writing-rows-bar">
    <Segmented label="List" value={rows} options={[["conversations", "Conversations"], ["messages", "Messages"]]} onChange={chooseRows} />
    {rows === "messages" ? <div className="library-view-toggle" role="group" aria-label="Message result view">
      <button type="button" className={view === "table" ? "active" : ""} aria-pressed={view === "table"} onClick={() => chooseView("table")}>Table</button>
      <button type="button" className={view === "text" ? "active" : ""} aria-pressed={view === "text"} onClick={() => chooseView("text")}>Highlighted text</button>
    </div> : null}
  </div>;
  if (rows === "messages") return <UserMessageSurface key="messages" messageView={view} header={(api, searching) => <>
    {toggle}
    {!searching ? <MessageMix where={toAggregationQuery({ ...api.query, aggregations: [] }, schemas.writing_messages).where} reload={reload} onFilter={filterChange(api)} /> : null}

  </>} />;
  return <QuerySurface key="conversations" dataset="writing" trailing={row => <button type="button" className="mcp-detail-button" title="List this conversation's messages, highlighted by where their text came from" onClick={() => drill(row)}>Messages</button>} header={api => <>
    <WritingPanel where={toAggregationQuery({ ...api.query, aggregations: [] }, schemas.writing).where} onFilter={filterChange(api)} status={status} reload={reload} />
    {toggle}

  </>} />;
}

type UsageView = "tokens" | "writing" | "carbon" | "conversations";
const usageViewKey = "pharos-usage-view";

// ?analyze_view= picks the view; otherwise the last choice is kept.
function initialUsageView(): UsageView {
  // Human Words and Carbon Impact read the live library.
  if (shared) return "tokens";
  const requested = new URLSearchParams(location.search).get("analyze_view");
  if (requested === "tokens" || requested === "writing" || requested === "carbon" || requested === "conversations") { store(usageViewKey, requested); return requested; }
  const saved = preferences().get<unknown>(usageViewKey, "tokens");
  return saved === "writing" || saved === "carbon" || saved === "conversations" ? saved : "tokens";
}

// migrate reads an engine dashboard's link (readAnalysisLink): an older link
// may hold a legend selection as a filter. A migrated link is rewritten.
function useUsageDashboardState<T extends object>(defaults: T, valid: (value: T) => boolean, time: { first: string; last: string }, migrate?: (view: T, where: WhereTerm[]) => { state: T; where: WhereTerm[] }) {
  const read = () => readDashboardLink(new URLSearchParams(location.search), defaults, valid, time, migrate);
  // Write the state a link opened with: defaults it left out, and a
  // migrated link's state and population, so it is never read twice.
  const settle = (opened: ReturnType<typeof read>) => {
    if (opened.error) return;
    const url = new URL(location.href);
    if (!url.searchParams.has("analyze_chart") || opened.migrated) url.searchParams.set("analyze_chart", JSON.stringify(opened.view));
    if (!url.searchParams.has("analyze_filters") || opened.migrated) url.searchParams.set("analyze_filters", encodeQuery({ ...EMPTY_QUERY, where: opened.where }));
    history.replaceState(history.state, "", url);
  };
  const [state, setState] = useState(read);
  const live = useRef(state); live.current = state;
  useEffect(() => settle(state), []);
  useEffect(() => {
    // A route change can belong to another page, so a restored link is not
    // rewritten here; the next change writes it whole (persist).
    const restore = () => setState(read());
    const refresh = () => { if (new URLSearchParams(location.search).get("analyze_window") === "today") restore(); };
    window.addEventListener("pharos:route", restore); window.addEventListener("pharos:usage-refresh", refresh);
    return () => { window.removeEventListener("pharos:route", restore); window.removeEventListener("pharos:usage-refresh", refresh); };
  }, [time.first, time.last]);
  // A migrated link's state and population are written together, so its
  // filters are never read as a version 2 population while still holding
  // the selection.
  const persist = (next: typeof state, field: "analyze_chart" | "analyze_filters") => {
    live.current = { ...next, migrated: false }; setState(live.current);
    if (next.migrated || field === "analyze_chart") updateURI("analyze_chart", JSON.stringify(next.view));
    if (next.migrated || field === "analyze_filters") updateURI("analyze_filters", encodeQuery({ ...EMPTY_QUERY, where: next.where }));
  };
  const update = (change: Partial<T>) => persist({ ...live.current, view: { ...live.current.view, ...change } }, "analyze_chart");
  const onFilter: FilterChange = (change, preset) => {
    const where = change(live.current.where);
    const dates = (terms: WhereTerm[]) => JSON.stringify(terms.filter(term => predicatesOf(term).some(c => [time.first,time.last].includes(c.field))));
    if (preset === "today") updateURI("analyze_window", "today");
    else if (preset || dates(where) !== dates(live.current.where)) updateURI("analyze_window", "");
    persist({ ...live.current, where }, "analyze_filters");
  };
  return { ...state, update, onFilter };
}

function GuidedUsageDashboard({ topic }: { topic: UsageTopic }) {
  if (topic === "tokens") return <TokenUsageDashboard />;
  if (topic === "human") return <HumanUsageDashboard />;
  if (topic === "parallelism") return <ParallelismUsageDashboard />;
  if (topic === "tools") return <ToolsUsageDashboard />;
  if (topic === "mcp") return <MCPUsageDashboard />;
  if (topic === "carbon") return <CarbonDashboard />;
  if (topic === "time") return <AgentTimeDashboard />;
  return <SkillsDashboard />;
}

// CO₂e and electricity in the Carbon page's units; other amounts compactly.
const carbonAmount = (format: (value: number, metric: string) => string): AmountFormat => (value, measure) => measure.unit === "g CO₂e" ? format(value, "co2") : measure.unit === "Wh" ? format(value, "energy") : compactNumber(value);
const carbonLoading = <p className="muted" role="status">Loading your Carbon assumptions…</p>;

// Carbon's views, drawn with the catalog of the current assumptions inside
// the Carbon page's filters and estimate. Older links read through the shape.
function CarbonDashboard() {
  const carbon = useCarbonCatalog();
  return <CatalogDashboard catalog={carbon.catalog ?? carbonCatalogShape} dataset="usage" name="carbon" defaults={dashboardDefaults.carbon} currentURL="/analyze?analyze_view=carbon"
    frame={(views, where, onFilter) => <CarbonImpact where={where} onFilter={onFilter}>{carbon.catalog ? views(carbon.catalog, carbonAmount(carbon.format)) : carbonLoading}</CarbonImpact>} />;
}

// The legacy Carbon Impact page: the same chart over its own saved filters
// and chart, without the dashboard's views or links. A link's chart
// (analyze_chart, in the old page's vocabulary) wins over the saved one, on
// entry and on route changes.
const carbonChartKey = "pharos-carbon-chart";
function LegacyCarbon() {
  const [where, onFilter] = useLegacyCarbonFilters();
  const carbon = useCarbonCatalog();
  const read = () => legacyCarbonChart(new URLSearchParams(location.search).get("analyze_chart"), preferences().get<unknown>(carbonChartKey, null));
  const [chart, setChart] = useState<AnalysisState>(read);
  useEffect(() => {
    const restore = () => setChart(read());
    window.addEventListener("pharos:route", restore);
    return () => window.removeEventListener("pharos:route", restore);
  }, []);
  const update = (next: Partial<AnalysisState>) => setChart(previous => { const merged = { ...previous, ...next }; store(carbonChartKey, merged); return merged; });
  return <CarbonImpact where={where} onFilter={onFilter}>{carbon.catalog ? <AnalysisTrend catalog={carbon.catalog} schema={schemas.usage} dataset="usage" state={chart} where={where} onChange={update} onFilter={onFilter} format={carbonAmount(carbon.format)} links={false} /> : carbonLoading}</CarbonImpact>;
}

// Parallelism's views follow the saved counting and gap, with which the
// service builds its records; the note names the settings the returned
// records carry, and warns when another window saved others.
function ParallelismUsageDashboard() {
  const [selection, setSelection] = useState<ConcurrencySelection | null>(null);
  const { catalog, settings } = useParallelismCatalog();
  const ribbon = selection?.settings, differs = ribbon && (ribbon.counting !== settings.counting || ribbon.gap !== settings.gap);
  return <CatalogDashboard catalog={catalog} linkCatalog={parallelismCatalogShape} dataset="parallelism" name="parallelism" defaults={dashboardDefaults.parallelism} currentURL="/analyze?analyze_view=tokens&machine_view=parallelism"
    summary={where => <ParallelismSettingsNote where={where} believed={settings} />}
    before={<>
      <ConcurrencyPanel routeParameter="analyze_parallelism" onSelection={setSelection} />
      {selection ? <UsageDashboardLinks schema={schemas.parallelism} dataset="parallelism" query={parallelismSelectionQuery(selection)} currentURL="/analyze?analyze_view=tokens&machine_view=parallelism" /> : null}
      <p className="usage-dashboard-note">The query view contains the hourly occupancy records touching the selected range, with the same repository, counting, gap, and ribbon selection. Those records describe threaded activity; model-only timing remains in the chart.</p>
      <h3>Parallel activity over time</h3>
      {differs ? <p className="usage-dashboard-note" role="status">The ribbon uses this link’s counting and gap; the views below use your saved settings, with which the service builds its records. Change a setting in the ribbon to save it.</p> : null}
    </>} />;
}
function ParallelismSettingsNote({ where, believed }: { where: WhereTerm[]; believed: ParallelismSettings }) {
  const result = useMetrics("parallelism", where, parallelismSettingsMetric);
  const returned = (result.metrics[0]?.buckets ?? []).map(bucket => ({ counting: bucket.keys[0] === "agent" ? "agent" as const : "workspace" as const, gap: Number(bucket.keys[1]) }));
  if (result.loading || result.error) return null;
  const { note, warning } = parallelismSettingsNote(returned, believed);
  return <p className="usage-dashboard-note" role="status">{note}{warning ? <strong> {warning}</strong> : null}</p>;
}
const parallelismSettingsMetric: AggregationClause[] = [{ id: "settings", op: "count", groupBy: ["counting", "gap_minutes"] }];

// Over time and the analysis views share the dashboard's filters and range;
// their fields sit side by side in one analyze_chart object.
type TokenTrendView = Omit<TokenChartView, "metric" | "split"> & AnalysisState;
const tokenSharedRows: LocalRows | undefined = shared ? { rows: shared.datasets.usage ?? [], hourly: shared.datasets.usage_hourly ?? [] } : undefined;
const validTokenDashboard = (view: TokenTrendView) => validAnalysisState(tokenAnalysisCatalog, view);

// New records arrive while a dashboard is open (Pharos MCP's own requests,
// new usage); Refresh reruns its queries.
function DashboardRefresh() {
  return <p className="usage-dashboard-note usage-dashboard-refresh"><button type="button" onClick={() => window.dispatchEvent(new Event("pharos:usage-refresh"))}>Refresh</button></p>;
}

function TokenUsageDashboard() {
  const state = useUsageDashboardState<TokenTrendView>(dashboardDefaults.tokens as TokenTrendView, validTokenDashboard, usageTime, (view, where) => readAnalysisLink(tokenAnalysisCatalog, view, where));
  if (state.error) return <p className="query-table-error" role="alert">{state.error}</p>;
  const choose = (next: AnalysisView) => state.update(switchView(tokenAnalysisCatalog, state.view, next));
  if ((state.view.view ?? "trend") !== "trend") return <><DashboardAnalysis catalog={tokenAnalysisCatalog} schema={schemas.usage} dataset="usage" state={state.view} where={state.where}
    onChange={state.update} onView={choose} onFilter={state.onFilter} local={tokenSharedRows} />{tokenSharedRows ? null : <DashboardRefresh />}</>;
  return <AnalysisTrend catalog={tokenAnalysisCatalog} schema={schemas.usage} dataset="usage" state={state.view} where={state.where} onChange={state.update} onView={choose} onFilter={state.onFilter}
    local={tokenSharedRows} format={tokenAmount} tick={tokenTick} currentURL="/analyze?analyze_view=tokens&machine_view=tokens">
    <TokenSummary where={state.where} />
    {tokenSharedRows ? null : <DashboardRefresh />}
  </AnalysisTrend>;
}

// Human Words reads messages (writing_messages) through the engine. Text
// classification runs in the background; each finished build reruns the
// dashboard's queries, and the note says where it stands.
function HumanUsageDashboard() {
  const [reload, setReload] = useState(0);
  // Refresh, or a finished classification, reloads the message mix too.
  useEffect(() => onDatasetRefresh("writing_messages", () => setReload(value => value + 1)), []);
  const status = useAuthorshipStatus(() => window.dispatchEvent(new Event("pharos:usage-refresh")));
  return <CatalogDashboard catalog={humanAnalysisCatalog} dataset="writing_messages" name="human" defaults={dashboardDefaults.human} readWhere={readHumanWhere} currentURL="/analyze?analyze_view=writing"
    summary={where => <HumanWordsSummary where={where} status={status} />}
    panels={(where, onFilter) => <>
      <p className="usage-dashboard-note">Typed and dictated words are distinguished from pastes, copied agent output, templates, attachments, harness instructions and automated prompts. Select a column, bar or box to open its messages; each message is highlighted by where its text came from.</p>
      <MessageMix where={where} reload={reload} onFilter={onFilter} />
    </>} />;
}

// An older Human Words link: filters messages cannot keep (a work's totals,
// combined values, a legend selection of a combined group) are left out and
// listed, with the legacy Writing view where they still apply.
const readHumanWhere: ReadWhere = (where, state) => {
  const read = readHumanLink(where, state.v === analysisVersion);
  if (!read.unsupported.length) return read;
  const selection = (term: WhereTerm) => predicatesOf(term).some(clause => clause.field === state.split);
  return { where: read.where, notice: <>This older link’s {read.unsupported.some(selection) ? "legend selection and filters" : "filters"} on whole works cannot keep their meaning for messages, so they are left out: {read.unsupported.map(describeTerm).join("; ")}. <a href={humanLegacyURL(where)}>Open them in the legacy Human words view</a>, where they still apply.</> };
};

// The dashboard's population, whatever the chart draws.
const humanSummaryMetrics: AggregationClause[] = [
  { id: "typed", op: "sum", field: "typed_words", groupBy: [] },
  { id: "typed_messages", op: "sum", expression: "SUM(IF([typed_words]>0,1,0))", groupBy: [] },
  { id: "messages", op: "count", groupBy: [] },
  { id: "works", op: "count_distinct", field: "work_id", groupBy: [] },
];
function HumanWordsSummary({ where, status }: { where: WhereTerm[]; status: AuthorshipStatus | null }) {
  const result = useMetrics("writing_messages", where, humanSummaryMetrics);
  const value = (id: string) => Number(result.metrics.find(metric => metric.id === id)?.buckets[0]?.value) || 0;
  const typed = value("typed"), typedMessages = value("typed_messages");
  const building = status !== null && !status.built_at;
  const card = (label: string, amount: string, detail: string) => <div className="mcp-metric"><span>{label}</span><strong>{building ? "Building…" : result.loading ? "Loading…" : result.error ? "Unavailable" : amount}</strong><small>{detail}</small></div>;
  return <>
    <div className="mcp-metrics dashboard-stats" aria-label="Human Words summary" aria-busy={result.loading}>
      {card("Words you wrote", compactNumber(typed), "typed or dictated in the selected period")}
      {card("Messages with your writing", typedMessages.toLocaleString(), `of ${value("messages").toLocaleString()} classified user messages`)}
      {card("Words per message", typedMessages ? Math.round(typed / typedMessages).toLocaleString() : "—", "average over messages containing your writing")}
      {card("Works with input", value("works").toLocaleString(), "works with classified messages in this period")}
    </div>
    {status ? <p className="usage-dashboard-note" role="status">{status.error ? `Classification update failed: ${status.error}` : status.running ? "Updating text classifications…" : status.stale ? "Text classifications need an update." : status.built_at ? `Text classified ${new Date(status.built_at).toLocaleString()}` : "Waiting for text classification."}</p> : null}
  </>;
}

// An engine dashboard: Over time, or Compare, Distribution, Scatter and Cycle
// over one catalog, with the dashboard's own panels around it. Its link is
// read through readAnalysisLink, so an older link opens as it always did.
const durationText = (ms: number) => Math.abs(ms) >= 3_600_000 ? `${(ms / 3_600_000).toLocaleString(undefined, { maximumFractionDigits: 1 })} h` : formatDurationMS(ms);
// Durations in hours where the measure says so (Agent Time's recorded time),
// otherwise as a duration; counts compactly.
const catalogAmount: AmountFormat = (value, measure) => measure.format.kind === "duration" ? measure.unit === "hours" ? compactNumber(value / 3_600_000) : durationText(value) : measure.unit === "USD" ? formatUSD(value) : compactNumber(value);
// readWhere reads filters an older link wrote in another dataset's fields
// (Human Words' works); it returns where itself when nothing changes.
// panels follow the chart and share the dashboard's filters. frame places
// the views inside a dashboard's own page (Carbon's filters and estimate),
// drawing them with the catalog it builds from the user's settings; catalog
// then only reads links.
type CatalogViews = (catalog: AnalysisCatalog, format?: AmountFormat) => React.ReactNode;
// linkCatalog reads and validates links when the drawn catalog depends on
// the user's settings (Parallelism's counting), so a link stays valid under
// any setting. readWhere reads an older link's filters, and may leave out
// those it cannot keep, with a notice saying so.
type ReadWhere = (where: WhereTerm[], state: AnalysisState) => { where: WhereTerm[]; notice?: React.ReactNode };
function CatalogDashboard({ catalog, linkCatalog = catalog, dataset, name, defaults, currentURL, legacyInAnalysis = false, before, after, summary, panels, readWhere, format = catalogAmount, frame }: {
  catalog: AnalysisCatalog; linkCatalog?: AnalysisCatalog; dataset: UsageDataset; name: string; defaults: AnalysisState; currentURL?: string; legacyInAnalysis?: boolean;
  before?: React.ReactNode; after?: React.ReactNode; summary?: (where: WhereTerm[], selected: boolean) => React.ReactNode;
  panels?: (where: WhereTerm[], onFilter: FilterChange) => React.ReactNode; readWhere?: ReadWhere; format?: AmountFormat;
  frame?: (views: CatalogViews, where: WhereTerm[], onFilter: FilterChange) => React.ReactNode;
}) {
  // An older link is read in the catalog's terms once (canonicalState) and
  // rewritten, so a later change never merges with its old vocabulary.
  const notice = useRef<React.ReactNode>(null);
  const state = useUsageDashboardState<AnalysisState>(defaults, view => validAnalysisState(linkCatalog, view), linkCatalog.time, (view, where) => {
    const read = readWhere?.(where, view);
    notice.current = read?.notice ?? null;
    return readAnalysisLink(linkCatalog, canonicalState(linkCatalog, view), read ? read.where : where);
  });
  if (state.error) return <p className="query-table-error" role="alert">{state.error}</p>;
  const schema = schemas[dataset];
  const views: CatalogViews = (drawn, amount = format) => {
    const choose = (next: AnalysisView) => state.update(switchView(drawn, state.view, next));
    return <>
      {(state.view.view ?? "trend") !== "trend"
        ? <DashboardAnalysis catalog={drawn} schema={schema} dataset={dataset} state={state.view} where={state.where} onChange={state.update} onView={choose} onFilter={state.onFilter} legacyURL={legacyInAnalysis ? currentURL : undefined}>{summary?.(state.where, Boolean(state.view.selection?.values.length))}</DashboardAnalysis>
        : <AnalysisTrend catalog={drawn} schema={schema} dataset={dataset} state={state.view} where={state.where} onChange={state.update} onView={choose} onFilter={state.onFilter} format={amount} currentURL={currentURL}>{summary?.(state.where, Boolean(state.view.selection?.values.length))}</AnalysisTrend>}
      <DashboardRefresh />
      {panels?.(state.where, state.onFilter)}
    </>;
  };
  return <section className={`usage-catalog-dashboard usage-${name}-dashboard`}>
    {notice.current ? <p className="usage-dashboard-note" role="status">{notice.current}</p> : null}
    {before}
    {frame ? frame(views, state.where, state.onFilter) : views(catalog)}
    {after}
  </section>;
}

function MCPUsageDashboard() {
  return <CatalogDashboard catalog={mcpAnalysisCatalog} dataset="mcp_calls" name="mcp" defaults={dashboardDefaults.mcp} after={<MCPUsageNote />} />;
}

// The Tools dashboard reads the daily rollup; its compare view opened by
// default, as before.
function ToolsUsageDashboard() {
  return <CatalogDashboard catalog={toolsAnalysisCatalog} dataset="tools" name="tools" defaults={dashboardDefaults.tools} currentURL="/tools?tools_view=summary" legacyInAnalysis before={<ToolLedgerBanner />}
    after={<p className="usage-dashboard-note">Select a bar or point to inspect its daily summaries, then follow a summary’s Calls link to individual calls and source conversations. The legacy view includes failure kinds and the existing command and context presets.</p>} />;
}

function AgentTimeDashboard() {
  return <CatalogDashboard catalog={agentTimeAnalysisCatalog} dataset="tool_calls" name="time" defaults={dashboardDefaults.time} currentURL={currentUsageAnalyses.find(item => item.topic === "time")!.url}
    summary={(where, selected) => <AgentTimeSummary where={where} selected={selected} />} />;
}

function SkillsDashboard() {
  return <CatalogDashboard catalog={skillsAnalysisCatalog} dataset="skill_usages" name="skills" defaults={dashboardDefaults.skills} currentURL={currentUsageAnalyses.find(item => item.topic === "skills")!.url} />;
}

function UsagePage() {
  useLocationRevision();
  if (shared || location.pathname !== "/analyze" || !isUsageWorkspace()) return <LegacyUsagePage />;
  return <UsageChartColorsProvider>
    <UsageWorkspace schemas={schemas} renderDashboard={topic => <GuidedUsageDashboard topic={topic} />} renderTable={dataset => <AnalyzeQueryTable key={dataset} dataset={dataset} />} />
  </UsageChartColorsProvider>;
}

// A record keyword clause is the table's host query state (record_<dataset>):
// saved, shared, reset and undone with the table query. Each request compiles
// its own state into record_search, which the service validates and runs.
function AnalyzeQueryTable({ dataset }: { dataset: UsageDataset }) {
  const fields = schemas[dataset].fields;
  const host = useMemo((): SurfaceHost => ({ query: recordKeywordHostQuery(dataset, fields), find: state => recordKeywordFind(state, dataset, fields) }), [dataset]);
  return <>
    {/* Keep record predicates and grouped HAVING in the native builder,
        including datasets whose older pages use RecordSearchEditor. */}
    <QuerySurface dataset={dataset} usageActions host={host}
      querySummary={api => { const value = readRecordKeyword(api.query.hostState, dataset, fields); return value ? <span>{(api.query.grouping?.source ?? api.query).where.length} source filters{api.query.grouping ? ` · ${api.query.where.length} group filters` : ""} · {recordKeywordSummary(value, fields, usageDatasets[dataset])}</span> : undefined; }}
      queryContent={api => <AnalyzeRecordKeyword api={api} dataset={dataset} host={host.query} />}
      presets={dataset === "mcp_calls" ? () => <MCPUsageNote /> : undefined} />
  </>;
}

function AnalyzeRecordKeyword({ api, dataset, host }: { api: QueryTableApi<Row>; dataset: UsageDataset; host: HostQueryOptions }) {
  const fields = schemas[dataset].fields, state = api.query.hostState, key = JSON.stringify(state ?? null);
  const value = useMemo(() => readRecordKeyword(state, dataset, fields), [key, dataset]);
  const [error, setError] = useState("");
  return <>
    <RecordKeywordClause fields={fields} context={usageDatasets[dataset]} value={value}
      onChange={next => {
        try {
          const hostState = withRecordKeyword(dataset, next);
          // Applying the clause already in effect runs it again.
          if (next && sameHostState(host.normalize!(hostState), state)) api.refresh();
          else api.setDefinition?.(previous => ({ query: { ...previous.query, offset: 0 }, hostState }));
          setError("");
        } catch (failure) { setError(failure instanceof Error ? failure.message : String(failure)); }
      }} />
    {error ? <p className="query-table-error" role="alert">{error}</p> : null}
  </>;
}

function LegacyUsagePage() {
  useLocationRevision();
  const [view, setView] = useState<UsageView>(initialUsageView);
  const [status, setStatus] = useState<AuthorshipStatus | null>(null);
  const [selectedCall, setSelectedCall] = useState<{ id: string } | null>(null);
  useEffect(() => {
    const restore = () => { if (location.pathname === "/analyze") setView(initialUsageView()); };
    window.addEventListener("pharos:route", restore);
    return () => window.removeEventListener("pharos:route", restore);
  }, []);
  function choose(next: UsageView) {
    setView(next); store(usageViewKey, next);
    const url = new URL(location.href);
    url.searchParams.set("analyze_view", next);
    if (next !== "writing") { url.searchParams.delete("writing"); url.searchParams.delete("messages"); }
    pushMachineLocation(url);
  }
  const statusText = !status ? "" : status.error ? `Last update failed: ${status.error}` : status.running ? "Updating…" : status.built_at ? `Updated ${new Date(status.built_at).toLocaleString()}` : "Waiting to build";
  // Machine controls fetch scope choices and canonicalize their route on mount.
  // Keep them unmounted while another page (including Home) is active.
  if (!shared && location.pathname !== "/analyze") return null;
  return <UsageChartColorsProvider><div className="pharos-query-page usage-page">
    <div className="view-heading library-heading usage-heading">
      <div><h1>Analyze</h1><p className="muted">{view === "tokens"
        ? "Understand how your agents use tokens, work in parallel, and spend their time across repositories."
        : view === "writing"
          ? <>Text you typed or dictated into agent chats, per conversation or per message. Harness instructions, one-click prompts, attachments, pastes, and copied agent output are counted separately; filter the table and the chart and breakdown follow. {statusText ? <span className="meta">{statusText}</span> : null}</>
          : view === "conversations" ? "Compare human-supplied text estimates and recorded output across whole conversations, including delegated work. Human text is estimated once per classified message, excluding harness and automated spans; output retains the usage ledger’s time placement." : "Estimated inference electricity and CO₂e for the tokens in this library, using published research and explicit assumptions."}</p></div>
      <div className="usage-heading-actions">
        {!shared ? <ResetPage /> : null}
        {shared ? null : <div className="library-view-toggle" role="group" aria-label="Analyze view">
          <button type="button" className={view === "writing" ? "active" : ""} aria-pressed={view === "writing"} onClick={() => choose("writing")}>Human Words</button>
          <button type="button" className={view === "tokens" ? "active" : ""} aria-pressed={view === "tokens"} onClick={() => choose("tokens")}>Machine Activity</button>
          <button type="button" className={view === "carbon" ? "active" : ""} aria-pressed={view === "carbon"} onClick={() => choose("carbon")}>Carbon Impact</button>
        </div>}
        {view === "tokens" && !shared && (new URLSearchParams(location.search).get("machine_view") ?? preferences().get("pharos-machine-view", "tokens")) === "tokens" ? <div className="usage-heading-tools"><RefreshPricesButton /></div> : null}
      </div>
    </div>
    {view === "conversations" ? <QuerySurface dataset="conversation_activity" /> : view === "tokens" ? shared ? <TokenUsage /> : <MachineActivity tokens={(where, applied, onTimeRangeChange) => <TokenUsage scopeWhere={where} appliedScope={applied} onTimeRangeChange={onTimeRangeChange} />} calls={props => <div className="machine-call-surface"><QuerySurface dataset="tool_calls" namespace="machine_calls" {...props}
      controls={(api, builder) => <div id="machineRawCalls" className="machine-raw-calls"><div className="machine-panel-heading"><div><h3>Raw calls backing this selection</h3><p className="muted">{api.total?.toLocaleString() ?? "…"} matching calls · Full invocations, timings, statuses, and source conversations. Filters here also update the charts and analytics.</p></div></div>{builder}</div>}
      trailing={row => <span className="machine-inline-actions"><button type="button" className="mcp-detail-button" onClick={() => setSelectedCall({ id: String(row.id) })}>Details</button>{row.workspace_id ? <a href={`/work/${encodeURIComponent(row.workspace_id)}?conversation=${encodeURIComponent(row.conversation_id ?? "")}`}>Conversation ↗</a> : null}</span>} /></div>}
      parallelism={() => <div className="machine-call-surface"><QuerySurface dataset="parallelism" controls={(api, builder) => <div className="machine-raw-calls"><div className="machine-panel-heading"><div><h3>Parallel activity records</h3><p className="muted">{api.total?.toLocaleString() ?? "…"} hour-by-agent records · Estimated from transcript timestamps with your Parallelism counting and gap settings. Group, filter and chart them like any table.</p></div></div>{builder}</div>} /></div>} /> : view === "writing" ? <WritingUsage onStatus={setStatus} /> : <LegacyCarbon />}
    {selectedCall ? <ToolCallDialog id={selectedCall.id} onClose={() => setSelectedCall(null)} /> : null}
  </div></UsageChartColorsProvider>;
}


function MCPUsageNote() {
  return <p className="usage-dashboard-note">Calls to Pharos's own MCP server. The latest 5,000 calls are retained; the selected dates can include fewer. Output tokens are estimated from response size, and response text is not stored. <UsageWorkspaceLink href="/settings/mcp">MCP settings<Icon name="arrow-right" /></UsageWorkspaceLink></p>;
}

function MCPSettings() {
  useLocationRevision();
  return location.pathname === "/settings/mcp" ? <MCPSettingsPage /> : null;
}

function MCPSettingsPage() {
  const [status, setStatus] = useState<MCPStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [readErrors, setReadErrors] = useState({ status: "" });
  const [copied, setCopied] = useState("");
  const monitor = useMemo(() => createMCPMonitor({
    status: setStatus,
    error: (part, failure) => setReadErrors(previous => ({ ...previous, [part]: failure?.message ?? "" })),
  }), []);

  useEffect(() => {
    if (busy) return;
    void monitor.refresh();
    const timer = window.setInterval(() => void monitor.refresh(), 5000);
    return () => { window.clearInterval(timer); monitor.stop(); };
  }, [busy, monitor]);

  async function toggle() {
    if (!status) return;
    monitor.stop();
    setBusy(true);
    try {
      const next = await queryRequest<{ enabled: boolean }>("/api/mcp/enabled", {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled: !status.enabled }),
      });
      setStatus({ ...status, enabled: next.enabled }); setError("");
    } catch (failure) { setError(failure instanceof Error ? failure.message : "Could not update MCP"); }
    finally { setBusy(false); }
  }

  async function copy(label: string, value: string) {
    try { if (window.pharosCopyText) await window.pharosCopyText(value); else await navigator.clipboard.writeText(value); setCopied(label); window.setTimeout(() => setCopied(""), 2000); }
    catch { setError("Could not copy to clipboard. Select the text and copy it manually."); }
  }

  const connection = status ? JSON.stringify({ mcpServers: { pharos: { command: status.command, args: status.args } } }, null, 2) : "";
  const prompt = status ? `Add Pharos as a local stdio MCP server. Call list_repositories with the current working directory as path (or the repository name as query) to find this repository, then use search_conversations with that repository filter, a small limit, and a max_output_tokens budget. Open get_conversation_overview for promising results, search_conversation_passages for a specific topic, and get_conversation_messages only around cited message IDs. Treat previews as leads and inspect the cited evidence before relying on an outcome. Pharos is read-only. If its tools are unavailable, enable MCP in the Pharos app and reconnect this agent client. Use list_findings and get_finding for recurring problems Pharos found in past work; reading them never starts a measurement.\n\nConnection definition:\n${connection}` : "";
  return <div className="mcp-page pharos-query-page">
    <div className="mcp-settings-actions"><UsageWorkspaceLink href={usageWorkspaceURL({ topic: "mcp" })}>View MCP usage<Icon name="arrow-right" /></UsageWorkspaceLink><button type="button" className="mcp-refresh" disabled={busy} onClick={() => { setError(""); void monitor.refresh(true); }}>Refresh</button></div>
    {error ? <div className="query-table-error" role="alert">{error}</div> : null}
    {readErrors.status ? <div className="query-table-error" role="alert">Connection settings could not be loaded: {readErrors.status}</div> : null}
    <section className="mcp-card mcp-status-card" aria-label="MCP availability">
      <div><span className={`mcp-status ${status?.enabled ? "enabled" : "disabled"}`}>{status ? status.enabled ? "Available" : "Off" : readErrors.status ? "Unavailable" : "Loading"}</span><h2>Agent access</h2><p className="muted">{status?.note ?? (readErrors.status ? "Use Refresh to try loading connection settings again." : "Loading connection settings…")}</p>{status?.install_location ? <p className="muted">Pharos installation: <code>{status.install_location}</code></p> : null}</div>
      <button type="button" className={`toggle ${status?.enabled ? "on" : ""}`} role="switch" aria-checked={Boolean(status?.enabled)} aria-label="Enable MCP" disabled={!status || busy} onClick={() => void toggle()} />
    </section>
    <div className="mcp-setup-grid">
      <section className="mcp-card"><div className="mcp-card-heading"><div><h2>Connection</h2><p className="muted">Add this stdio server in an agent client’s MCP settings. The client launches Pharos locally.</p></div><button type="button" disabled={!status} onClick={() => void copy("connection", connection)}>{copied === "connection" ? "Copied" : "Copy JSON"}</button></div><pre className="mcp-code"><code>{connection || (readErrors.status ? "Connection settings unavailable. Use Refresh to try again." : "Loading…")}</code></pre><p className="mcp-fineprint">Client settings formats vary. Use the command and args shown here if your client does not accept this JSON shape.</p></section>
      <section className="mcp-card"><div className="mcp-card-heading"><div><h2>Prompt for an agent</h2><p className="muted">Paste this when asking an agent to add Pharos and use it efficiently.</p></div><button type="button" disabled={!status} onClick={() => void copy("prompt", prompt)}>{copied === "prompt" ? "Copied" : "Copy prompt"}</button></div><pre className="mcp-prompt">{prompt || (readErrors.status ? "Connection settings unavailable. Use Refresh to try again." : "Loading…")}</pre></section>
      {status?.skill?.text ? <section className="mcp-card mcp-skill-card"><div className="mcp-card-heading"><div><h2>Skill for agents</h2><p className="muted">A short skill that teaches an agent to search prior coding sessions with Pharos, and to ask you to turn on MCP when it isn’t available. Save it as:</p></div><button type="button" onClick={() => void copy("skill", status.skill?.text ?? "")}>{copied === "skill" ? "Copied" : "Copy skill"}</button></div>
        <ul className="mcp-skill-paths">{Object.entries(status.skill.paths ?? {}).map(([harness, where]) => <li key={harness}><span>{harness === "claude" ? "Claude Code" : harness === "codex" ? "Codex" : harness}</span><code>{where}</code></li>)}</ul>
        <pre className="mcp-prompt">{status.skill.text}</pre></section> : null}
    </div>

  </div>;
}

type ToolLedgerStatus = { conversations: number; current_conversations: number; pending_conversations: number; tool_calls: number; backfill: { running: boolean; done: number; total: number; error: string | null } };

// Filters that select the calls behind one summary row.
function drillFilters(row: Row): WhereTerm[] {
  return toolSummaryCallFilters(row);
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
        {call.mcp_server || call.mcp_method ? <div><dt>MCP</dt><dd>{[call.mcp_server, call.mcp_method].filter(Boolean).join(" · ")}</dd></div> : null}
        {call.skill_name ? <div><dt>Skills</dt><dd>{String(call.skill_name)}</dd></div> : null}
      </dl>
      {call.error_signature ? <p className="muted">Error signature: {String(call.error_signature)}</p> : null}
      {call.title ? <p className="muted">{String(call.title)}{call.repository_name ? ` · ${call.repository_name}` : ""} · {String(call.provider)} {call.model ? `· ${call.model}` : ""}{call.effort ? ` · Effort: ${call.effort}` : ""}</p> : null}
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
    const timer = window.setInterval(() => { if (document.querySelector("#tools.active, .usage-tools-dashboard")) void load.current(); }, 3000);
    return () => window.clearInterval(timer);
  }, []);
  const running = status?.backfill.running ?? false;
  const wasRunning = useRef(false);
  useEffect(() => {
    if (wasRunning.current && !running) { tableApis.get("tools")?.refresh(); tableApis.get("tool_calls")?.refresh(); tableApis.get("skill_usages")?.refresh(); window.dispatchEvent(new Event("pharos:usage-refresh")); }
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
  const clause = (item: WhereClause) => `${item.field}|${item.op}|${item.value}|${item.negated ? 1 : 0}`;
  const branch = (member: WhereBranch) => isWhereConjunction(member) ? `all:${member.all.map(clause).join("+")}` : clause(member);
  return isWhereGroup(term) ? `any:${term.any.map(branch).join("&")}` : clause(term);
}

function ToolsPage() {
  const readView = (): "summary" | "calls" | "skills" => {
    const selected = new URLSearchParams(location.search).get("tools_view");
    return selected === "summary" || selected === "skills" ? selected : "calls";
  };
  const [view, setView] = useState(readView);
  useEffect(() => {
    const restore = () => { if (location.pathname === "/tools") setView(readView()); };
    window.addEventListener("pharos:route", restore);
    return () => window.removeEventListener("pharos:route", restore);
  }, []);
  const [selected, setSelected] = useState<string | null>(null);
  function choose(next: "summary" | "calls" | "skills") { setView(next); updateURI("tools_view", next === "calls" ? "" : next); }
  function drill(row: Row) {
    tableApis.get("tool_calls")?.setQuery(previous => orderedUsageQuery("tool_calls", { ...previous, where: drillFilters(row), aggregations: [], orderBy: [], offset: 0 }));
    choose("calls");
  }
  return <div className="pharos-query-page tools-page">
    <div className="view-heading library-heading"><div><h1>Tool use</h1><p className="muted">Every tool call from retained transcripts, joined to its result. Shell commands are parsed into program, subcommand, and category. <b>Context added</b> is what a result grew the next request’s prompt by (measured from provider usage, or estimated from size at 4 bytes a token); <b>context carried</b> counts it again for every later request until compaction. Claude durations come from timestamps and include any wait for approval. Linked mirrors of the same work are counted once.</p></div>
      <div className="library-view-toggle" role="group" aria-label="Tool view">
        <button type="button" className={view === "calls" ? "active" : ""} aria-pressed={view === "calls"} onClick={() => choose("calls")}>Calls</button>
        <button type="button" className={view === "summary" ? "active" : ""} aria-pressed={view === "summary"} onClick={() => choose("summary")}>Summary</button>
        <button type="button" className={view === "skills" ? "active" : ""} aria-pressed={view === "skills"} onClick={() => choose("skills")}>Skills</button>
      </div></div>
    <ToolLedgerBanner />
    <div hidden={view !== "summary"}>
      <QuerySurface dataset="tools" trailing={row => <button type="button" className="mcp-detail-button" title="Show the calls behind this row" onClick={() => drill(row)}>Calls</button>} />
    </div>
    <div hidden={view !== "calls"}>
      <QuerySurface dataset="tool_calls" trailing={row => <button type="button" className="mcp-detail-button" onClick={() => setSelected(String(row.id))}>Details</button>} />
    </div>
    <div className="skill-usages" hidden={view !== "skills"}>
      <p className="muted">Explicit invocations, observed file-load attempts, and harness-reported loads. A listing is not usage, and a load does not prove the skill was followed. Status on file loads belongs to the parent tool call. Costs remain on that call and are not duplicated here.</p>
      <QuerySurface dataset="skill_usages" trailing={row => <span className="skill-usage-actions">
        {row.tool_call_id ? <button type="button" className="mcp-detail-button" onClick={() => setSelected(String(row.tool_call_id))}>Tool call</button> : null}
        <button type="button" className="mcp-detail-button" onClick={() => openFindHit({ workspace_id: String(row.workspace_id), conversation_id: String(row.conversation_id), message_id: String(row.body_message_id ?? row.evidence_message_id), title: String(row.title), count: 1 })}>Evidence</button>
      </span>} />
    </div>
    {selected ? <ToolCallDialog id={selected} onClose={() => setSelected(null)} /> : null}
  </div>;
}

if (!shared && concurrencyPrototypeEnabled && location.pathname === "/analyze" && new URLSearchParams(location.search).get("analyze_view") === "concurrency") {
  const url = new URL(location.href);url.searchParams.set("analyze_view", "tokens");url.searchParams.set("parallelism", "1");history.replaceState(null, "", url);
}

if (!shared) installAppNavigation();

const dataGuideHost = document.createElement("div");
dataGuideHost.id = "dataGuideHost";
document.body.append(dataGuideHost);
createRoot(dataGuideHost).render(<DataTaxonomyModal />);

// Ask remains available before Home's first visit and while another page is active.
if (!shared) {
  const host = document.createElement("div");host.hidden = true;document.body.append(host);
  createRoot(host).render(<AskPharosSearch schema={schemas.library} />);
}

const mounts: Array<[string, React.ReactNode]> = [
  ["searchComposerDialog", <SearchDialog schema={schemas.library} />],
  ["homePage", <HomePage schemas={schemas} />],
  ["cardLibraryPage", <CardLibraryPage schemas={schemas} />],
  ["syncHistoryTables", <SyncTables />],
  ["askSettingsPage", <AskPharosSettings />],
  ["settingsPage", <SettingsWorkspace content={document.getElementById("settingsContent")!} />],
  ["queryTableLibrary", <LibraryPage />],
  ["queryTableUsage", <UsagePage />],
  ["toolsPage", <ToolsPage />],
  ["mcpSettingsPage", <MCPSettings />],
  ["findingsPage", <FindingsPage copy={copyText} table={<QuerySurface dataset="findings" />} />],
  ["tl1Page", <TL1Page attempts={<QuerySurface dataset="tl1_attempts" />} filterAttempts={(where) => tableApis.get("tl1_attempts")?.setQuery(previous => ({ ...previous, where: where as WhereTerm[], offset: 0 }))} copy={copyText} />],
];

// Mount a page on its first visit. Hidden pages used to load capabilities,
// definitions and dashboard data together with Usage, competing for the same
// browser connections and catalog readers. Keep visited pages mounted so their
// table restoration and undo history survive navigation.
function LazyPage({ host, children }: { host: HTMLElement; children: React.ReactNode }) {
  useLocationRevision();
  const visited = useRef(false);
  visited.current ||= Boolean(shared || host.id === "searchComposerDialog" || host.closest(".view.active"));
  if (!visited.current) return null;
  const libraryPage = ["queryTableLibrary", "queryTableUsage", "toolsPage", "findingsPage"].includes(host.id);
  const settings = host.id === "findingsPage" && new URLSearchParams(location.search).get("view") === "settings";
  // Keep one element type across routes: Findings Settings bypasses the empty
  // state without remounting the dashboard and its retained table.
  return <LibraryContent enabled={!shared && libraryPage && !settings}>{children}</LibraryContent>;
}

for (const [id, component] of mounts) {
  if (shared && !["queryTableLibrary", "queryTableUsage", "toolsPage"].includes(id)) continue;
  const element = document.getElementById(id);
  if (element) createRoot(element).render(<PageErrorBoundary><LazyPage host={element}>{component}</LazyPage></PageErrorBoundary>);
}
if (!shared) { startFindingsChrome(); installWorkspaceInbox(); installDelegationExplorer(); }
if (!shared && concurrencyPrototypeEnabled) {
  const modalHost = document.createElement("div");
  modalHost.id = "parallelismModal";document.body.append(modalHost);
  createRoot(modalHost).render(<ConcurrencyModal />);
  startConcurrencySummary();
}
// Carbon and Parallelism settings shape Usage and parallelism values on the
// server. Refresh their tables and cards once the saved settings have landed.
window.addEventListener("pharos:preferences-saved", event => {
  for (const dataset of datasetsForSavedPreferences((event as CustomEvent<{ set?: Record<string, unknown>; delete?: string[] }>).detail)) window.pharosQueryTables?.refresh(dataset as Dataset);
});
window.pharosQueryTables = {
  summaryURL: (metric, window, generatedAt, _concurrency, from) => usageSummaryURL(metric, window, generatedAt, readConcurrencySettings(), from),
  async createLink() {
    const entry = [...tableApis.entries()].find(([dataset]) => datasetIsVisible(dataset));
    if (!entry) return { url: location.href };
    const [dataset, api] = entry, query = api.query;
    const link = await api.links.create(query);
    return { url: link.url, query, dataset: dataset === "machine_calls" ? "tool_calls" : dataset };
  },
  refresh(dataset) {
    window.dispatchEvent(new CustomEvent("pharos:data-refresh", { detail: dataset }));
    tableApis.get(dataset)?.refresh();
    if (dataset === "usage") {
      tableApis.get("writing")?.refresh();
      tableApis.get("writing_messages")?.refresh();
      window.dispatchEvent(new Event("pharos:usage-refresh"));
    }
  },
  filterRepository(repository) {
    filterLibrary([{ field: "repository_name", op: "=", value: repository }]);
  },
  filterModel(model) {
    filterLibrary([{ field: "models", op: "contains", value: model }]);
  },
};

window.dispatchEvent(new Event("pharos:search-ready"));
