import { formatUSD } from "./currency";
import { fitQueryToScreen } from "./query-fit";
import { queryRow } from "./query-transport";
import {messageLabelLegacyURL} from "./message-label-legacy";
import {withFingerprintColumn} from "./fingerprint-columns";
import {MessageLabelDisplay,MessageLabelCards,messageLabelRenderers} from "./message-label-results";
import {messageLabelHostQuery,messageLabelConversationQuery,labelResultsMode} from "./message-label-results-model";
import {messageLabelsCatalog} from "./message-labels-catalog";
import { branchDisplay, gitFactDisplay, outcomeDisplay } from "./outcomes";
import { ViewDisclosure } from "./view-disclosure";
import { useViewEntry } from "./view-state";
import { installViewAttention, registerDetailAttentionResolver } from "./view-attention";
import { nativeDetailsReady } from "./view-attention-model";
import { revealQueryDetail } from "./query-attention";
import { createAttentionRegistry } from "@pythia-software/query-table-core";
import { portableLinkOptions, portableQueryLinks } from "./portable-query-link";
import { startDayClock } from "./day-window";
import "./visit-history";
import { originLabel, originRenderers, useOriginMetadata, isOriginDimension } from "./origin-dimensions";
import "./uifeedback/pharos-client";
import { DataTaxonomyModal, SourceQueryBuilder } from "./data-taxonomy";
import { installDelegationExplorer } from "./delegation";
import { installWorkspaceInbox } from "./workspace-inbox";
import { installWorkFiles } from "./work-files";
import { ConversationFacts, ResultConversationBrowser, ResultMessage, conversationActivitySummary, openableProps, type ResultTarget } from "./conversation-results";
import { TextSearchEditor } from "./text-search-editor";
import { RecordSearchEditor, recordSearchTargets } from "./record-search-editor";
import { usageDetailsSchema } from "./usage-row-model";
import { fittedDefaultQuery, usageActionQuery, usageQuerySchema, withGroupDetail } from "./usage-query-columns";
import { orderedUsageQuery } from "./usage-query-order";
import { usageRowRenderers } from "./usage-row-renderers";
import { SearchDialog } from "./search-dialog";
import { ExploreSearch } from "./explore-search";
import { recordKeywordSummary, RecordKeywordClause } from "./record-keyword-clause";
import { refreshTodayHost, changeLibrarySearch, libraryFind, libraryHostQuery, libraryHostState, readLibraryHost, readRecordKeyword, recordKeywordFind, recordKeywordHostQuery, sameHostState, withoutLibrarySearch, withRecordKeyword, type LibraryHost } from "./host-query-state";
import { SearchResultsDisplay } from "./search-results-view";
import { highlightParts, matchFindState, plainSnippetParts, summaryEntries, type WorkMatches, type WorkMatchItem } from "./find-match-model";
import { findPattern, writeFindParameters } from "./page-find-model";
import { queryForSearchResultsView, searchResultsView } from "./search-results-view-model";
import { newKeywordSearch, searchSentence } from "./text-search-model";
import { CarbonImpact } from "./carbon-impact";
import { carbonCatalogShape } from "./carbon-analysis-catalog";
import { usageSummaryURL } from "./usage-summary-links";
import { readConcurrencySettings } from "./concurrency-settings";
import { activityPresentation } from "./machine-activity-data";
import {  usageTime, type FilterChange } from "./usage-chart";
import type { TokenChartView } from "./usage-presets";
import { withComputedCapabilityRefresh } from "./computed-column-store";
import { postMetrics, type MetricResult } from "./metric-request";
import { tokenSummaryMetrics } from "./token-chart-request";
import messageLabelsDocument from "../../schemas/message_labels.schema.json";
import modelRequestsDocument from "../../schemas/model_requests.schema.json";
import { ThinkingDashboard } from "./thinking-dashboard";
import React, { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { createRoot } from "react-dom/client";
import { PageLink, useLocationRevision } from "./nested-pages";
import { LibraryContent } from "./library-content";
import { SyncTables } from "./sync-tables";
import { SettingsWorkspace } from "./settings-workspace";
import { LogsPage } from "./logs-page";
import { installAppNavigation } from "./app-navigation";
import { EMPTY_QUERY, groupKeyField, groupKeyFields, groupSourceQuery, predicatesOf, httpComputedColumnStore, inspectBucketQuery, encodeQuery, loadSchema, type ComputedColumnStore, type HostQueryState, type QueryCapabilitiesV3, type QueryResultV3, type ComputedExecution, type AggregationClause, type FieldSchema, type Transport, type WhereTerm, type QueryState, type RelationshipAdapter, jsonPropertyViewStorage } from "@pythia-software/query-table-core";
import { useQueryTable, type HostQueryOptions, type QueryTableApi } from "@pythia-software/query-table-react";
import { DataTable, FilterValueProvider, MetricsPanel, QueryFilterRow, defaultRenderers, type CellContext, type FilterValuePresentation, type RenderRegistry } from "@pythia-software/query-table-ui";
import "@pythia-software/query-table-ui/theme.css";
import "./pharos.css";
import { savedRollingWindow } from "./bookmarks";
import { acceptDefaultQuery, libraryStorageAdapter, rejectDefaultQuery, retryDefaultQuery } from "./storage";
import { queryRequest, queryFetch, shouldSkipQueryOnRestore } from "./query-request";
import { PageErrorBoundary, QueryRecovery, tableRecoveryQuery } from "./query-recovery";
import { createMCPMonitor, type MCPStatus } from "./mcp-monitor";
import { HomePage } from "./home";
import { AskPharosSearch, AskPharosSettings } from "./ask-pharos";
import { CardLibraryPage } from "./card-library";
import { linkTargets, navigateLink } from "./query-link-targets";
import { queryFromURL, readRollingWindow, resolveRollingQuery, rollingQueryMatches, restoreRollingDefinition } from "./query-links";
import { mainIntegration, pullRequestLabel, pullRequestURL, pullRequests } from "./pull-requests";
import { optionLabels } from "./option-labels";
import { preferences } from "./preferences";
import { UsageChartColorsProvider, useUsageTableRenderers } from "./usage-chart-colors";
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
import rhythmDocument from "../../schemas/rhythm.schema.json";
import safetyDocument from "../../schemas/safety.schema.json";
import findingsDocument from "../../schemas/findings.schema.json";
import agentEventsDocument from "../../schemas/agent_events.schema.json";
import agentEventMessagesDocument from "../../schemas/agent_event_messages.schema.json";
import outcomesDocument from "../../schemas/outcomes.schema.json";
import branchUsageDocument from "../../schemas/branch_usage.schema.json";
import { FindingsPage, startFindingsChrome } from "./findings";
import { FileHistoryPage } from "./file-history";
import { fileHistoryURL } from "./file-history-model";
import { safetyDetailURL, safetyStateLabels } from "./safety-route";
import { SafetySelectionBar, SafetyTableContext } from "./safety";
import { createQueryHistory } from "./query-history";
import { Icon } from "./icons";
import {  CategoryPill, MessageMix, authoredRenderers } from "./authored";
import { ConcurrencyModal, ConcurrencyPanel, concurrencyPrototypeEnabled, type ConcurrencySelection } from "./concurrency";
import { startConcurrencySummary } from "./concurrency-store";
import { HabitsPanel } from "./habits";
import { habitsAnalysisCatalog } from "./habits-analysis-catalog";
import { canonicalAnalyzeURL, isUsageWorkspace, readUsageWorkspace, usageDatasets, usageWorkspaceURL, type UsageDataset, type UsageTopic } from "./usage-workspace-model";
import { mcpAnalysisCatalog } from "./mcp-analysis-catalog";
import { agentEventsAnalysisCatalog } from "./agent-events-analysis-catalog";
import { branchAnalysisCatalog, branchCoverage, branchUsageDisplay } from "./branch-analysis-catalog";
import { outcomeCatalogFor, outcomeGroupLabel, outcomeRatioRows, outcomeTierCoverage, outcomeTiersReached, outcomesAnalysisCatalog } from "./outcomes-analysis-catalog";
import { AgentEventExamples } from "./agent-event-examples";
import { AgentEventRates } from "./agent-event-rates";
import { agentEventReadWhere, daySpan, eventRates, ratePeriod } from "./agent-event-rates-model";
import { toolsAnalysisCatalog } from "./tools-analysis-catalog";
import { toolTimeAnalysisCatalog, skillsAnalysisCatalog } from "./record-analysis-catalogs";
import { UsageWorkspace, UsageDashboardLinks, UsageWorkspaceLink, useDashboardHeadline } from "./usage-workspace";
import { CreateDashboardCard, DashboardCardSourceContext } from "./create-dashboard-card";
import { ribbonCardTitle } from "./parallelism-ribbon-card";
import { UsageQueryIdeas } from "./usage-query-ideas";
import { readDashboardLink, parallelismSelectionQuery } from "./usage-dashboard-model";
import { conversationAnalysisCatalog } from "./conversation-analysis-catalog";
import { DashboardAnalysis } from "./dashboard-analysis";
import { AnalysisTrend, type AmountFormat } from "./dashboard-trend";
import { isTrendView, analysisVersion, canonicalState, readAnalysisLink, switchView, validAnalysisState, type AnalysisCatalog, type AnalysisState, type AnalysisView } from "./dashboard-analysis-model";
import { dashboardDefaults } from "./dashboard-defaults";
import { tokenAnalysisCatalog } from "./token-analysis-catalog";
import { PricingNotice } from "./pricing-notice";
import { describeTerm, humanAnalysisCatalog, readHumanLink } from "./human-analysis-catalog";
import { parallelismCatalogShape, parallelismSettingsNote, type ParallelismSettings } from "./parallelism-analysis-catalog";
import { useCarbonCatalog, useParallelismCatalog } from "./analysis-catalog-hooks";
import { datasetsForSavedPreferences, onDatasetRefresh } from "./dataset-refresh";

type Row = Record<string, any>;
const relationships: RelationshipAdapter = {
  async resolve(references, signal) {
    return responseJSON(await fetch("/api/query-object-references", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ references }), signal }));
  },
};
type Dataset = "message_labels" | "model_requests" | "library" | "usage" | "conversation_activity" | "writing" | "writing_messages" | "mcp_calls" | "tools" | "tool_calls" | "skill_usages" | "tl1_attempts" | "parallelism" | "rhythm" | "findings" | "safety" | "agent_events" | "agent_event_messages" | "outcomes" | "branch_usage";
function mainIntegrationLink(row: Row): React.ReactNode {
  const merge = mainIntegration(row);
  if (!merge) return "";
  return merge.url ? <a className="qt-pr-link" href={merge.url} target="_blank" rel="noopener noreferrer" title={merge.commit} onClick={event => event.stopPropagation()}>{merge.label}</a>
    : <span title={merge.commit}>{merge.label}</span>;
}

function usdCost(value: unknown, row: Row): React.ReactNode {
  if (value === null || value === undefined || value === "") return <span className="muted" title="No confirmed API price for this model and date">—</span>;
  const amount = Number(value);
  const text = formatUSD(amount);
  if (row.price_status === "partial") return <span title="Some tokens had no confirmed rate, so this is a lower bound">{text}+</span>;
  if (row.price_status === "assumed") return <span title="Priced by assumption: no published API price, so a sibling model's price is used (see pricing/cost_changes.json)">≈{text}</span>;
  return <span>{text}</span>;
}

function datasetIsVisible(dataset: Dataset) {
  const params = new URLSearchParams(location.search), path = location.pathname;
  if (path === "/analyze") {
    const workspace = readUsageWorkspace(params);
    return workspace.mode !== "dashboards" && workspace.dataset === dataset;
  }
  if (dataset === "library") return path === "/library";
  return (dataset === "findings" || dataset === "safety") && path === "/findings" && params.get("view") === "table" && (params.get("optimize_area") === "safety") === (dataset === "safety");
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
    pharosQueryTables?: {
      createLink: () => Promise<{ url: string; query?: QueryState; dataset?: Dataset }>;
      refreshDashboards: (dataset: Dataset) => void;
      filterRepository: (repository: string) => void;
      filterModel: (model: string) => void;
      summaryURL: typeof usageSummaryURL;
    };
  }
}

const schemas: Record<Dataset, FieldSchema<Row>> = {
  message_labels:loadSchema<Row>(messageLabelsDocument),
  model_requests: loadSchema<Row>(modelRequestsDocument),
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
  rhythm: loadSchema<Row>(rhythmDocument),
  findings: loadSchema<Row>(findingsDocument),
  safety: loadSchema<Row>(safetyDocument),
  agent_events: loadSchema<Row>(agentEventsDocument),
  agent_event_messages: loadSchema<Row>(agentEventMessagesDocument),
  outcomes: loadSchema<Row>(outcomesDocument),
  branch_usage: loadSchema<Row>(branchUsageDocument),
};

const tableApis = new Map<Dataset, QueryTableApi<Row>>();
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
startDayClock();
// host compiles a table's host query state into the service's parameters. Each
// request carries the host state it runs with (Query Table adds it), so rows,
// metrics and filter values never pair one definition's table query with
// another's keyword search. Field statistics carry none and follow the
// current definition.
type HostFind = { find: (state: HostQueryState | undefined) => string; current: () => string };
function makeTransport(dataset: Dataset, queryCapabilities: QueryCapabilitiesV3, extra = "", onFind?: (find: FindSummary | null) => void, host?: HostFind): Transport<Row> {
  const endpoint = (suffix = "", params?: URLSearchParams, collection = true, request?: { hostState?: HostQueryState }) => {
    const query = new URLSearchParams(collection ? extra : "");
    if (collection && host) new URLSearchParams(request?.hostState ? host.find(request.hostState) : host.current()).forEach((value, name) => query.set(name, value));
    const carbon = new URLSearchParams(location.search).get("analyze_carbon");
    if (dataset === "usage" && carbon) query.set("carbon_settings", carbon);
    params?.forEach((value, name) => query.set(name, value));
    return `/api/query/${dataset}${suffix}${query.size ? `?${query}` : ""}`;
  };
  return {
    queryCapabilities,
    async fetchQueryV3(query, signal) {
      const result = await queryRequest<QueryResultV3 & { find?: FindSummary }>(endpoint("/query-v3", undefined, true, query), {
        method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(query), signal,
      });
      onFind?.(result.find ?? null);
      return result;
    },
  };
}

type Capabilities = { queryCapabilities: QueryCapabilitiesV3; computedExecution: ComputedExecution };
const fetchCapabilities = async (dataset: Dataset, signal?: AbortSignal) => queryRequest<Capabilities>(`/api/query/${dataset}/capabilities`, { signal });

const renderers: RenderRegistry<Row> = {
  ...defaultRenderers,
  ...messageLabelRenderers,
  safety_state: ({ value }) => safetyStateLabels[String(value) as keyof typeof safetyStateLabels] ?? String(value ?? ""),
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
    if (!id) return defaultRenderers.text(context);
    return <a className="qt-conversation-link" href={`/conversation/${encodeURIComponent(id)}`} title={`Open conversation ${id}`} onClick={event => {
      event.stopPropagation();
      if (event.button || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
      event.preventDefault();
      void openConversation(id);
    }}>{id}</a>;
  },
  main_integration({ row }: CellContext<Row>) { return mainIntegrationLink(row); },
  // A project is a repository or a folder; a session without a working
  // directory has none, which is a state of its own, not a blank.
  project({ value, row }: CellContext<Row>) {
    if (value == null || value === "") return row.project_kind === "none" ? <span className="qt-muted">No project</span> : "";
    return <span title={row.project_kind === "folder" ? `Folder ${value}` : `Repository ${value}`}>{String(value)}</span>;
  },
  // A session's outcome label, with the evidence that decided it.
  outcome_label({ value, row }: CellContext<Row>) {
    const outcome = outcomeDisplay(value);
    return <span className={`qt-outcome ${outcome.id}`} title={String(row.outcome_why ?? outcome.description)}>{outcome.label}</span>;
  },
  // A Git count is not applicable to a folder, and not checked is not zero.
  git_count({ value, row }: CellContext<Row>) {
    const fact = gitFactDisplay(value, row.project_kind, row.git_check, row.git_check_reason);
    return fact.muted ? <span className="qt-muted" title={fact.title}>{fact.text}</span> : fact.text;
  },
  // Spend by branch: a row without a branch says which coverage it is.
  branch_usage({ value, row }: CellContext<Row>) {
    const branch = branchUsageDisplay(value, row.coverage);
    return branch.muted ? <span className="qt-muted">{branch.text}</span> : branch.text;
  },
  git_branch({ value, row }: CellContext<Row>) {
    const branch = branchDisplay(value, row.project_kind);
    return branch.muted ? <span className="qt-muted" title={branch.title}>{branch.text}</span> : branch.text;
  },
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
  // An enum's schema label in place of its stored key (agent event kinds and sources).
  option_label({ value, field }: CellContext<Row>) {
    const options = (field.filter as { values?: { options?: Array<{ value: string; label: string }> } } | undefined)?.values?.options;
    return <span>{options?.find(option => option.value === value)?.label ?? String(value ?? "")}</span>;
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
  message_labels:"No messages match these labels and filters.",
  model_requests: "No model requests match this query.",
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
  rhythm: "No activity matches this query.",
  findings: "No findings match this query.",
  safety: "No risky action notices match this query.",
  agent_events: "No agent events match this query.",
  agent_event_messages: "No agent messages read for events match this query.",
  outcomes: "No sessions match this query.",
  branch_usage: "No spend in a Git repository matches this query.",
};

// Query Table 1.0.0 still formats numeric metrics with a field renderer only for
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

// host registers clauses Pharos runs itself (host-query-state.ts) as part of
// the table's definition; its find compiles them into each request. find
// carries other request parameters, such as a message search cursor.
// onFind receives Library message-search coverage when present.
type SurfaceHost = { query: HostQueryOptions; find: (state: HostQueryState | undefined) => string; onState?: (state: HostQueryState | undefined) => void };
function QuerySurface({ dataset, host, usageActions = false, find = "", onFind, trailing, selectionBar, queryHistory = false, controls, queryContent, querySummary, resultPresentation, presets }: { dataset: Dataset; host?: SurfaceHost; usageActions?: boolean; find?: string; onFind?: (find: FindSummary | null) => void; trailing?: (row: Row, api: QueryTableApi<Row>) => React.ReactNode; selectionBar?: (api: QueryTableApi<Row>) => React.ReactNode; queryHistory?: boolean; controls?: (api: QueryTableApi<Row>, builder: React.ReactNode) => React.ReactNode; queryContent?: (api: QueryTableApi<Row>) => React.ReactNode; querySummary?: (api: QueryTableApi<Row>) => React.ReactNode; resultPresentation?: (api: QueryTableApi<Row>) => React.ReactNode; presets?: (api: QueryTableApi<Row>) => React.ReactNode }) {
  const attention = useMemo(() => createAttentionRegistry(), [dataset]);
  const visible = datasetIsVisible(dataset);
  const schema = useMemo(() => {
    const base = withFingerprintColumn(schemas[dataset], dataset);
    return usageActions ? usageQuerySchema(base, dataset as UsageDataset) : base;
  }, [dataset, usageActions]);
  const locationRevision = useLocationRevision();
  const usageRenderers = useMemo(() => Object.hasOwn(usageDatasets, dataset) ? { ...renderers, ...usageRowRenderers(dataset as UsageDataset) } : renderers, [dataset]);
  const tableRenderers = useUsageTableRenderers(dataset==="message_labels"?{...usageRenderers,...messageLabelRenderers}:usageRenderers);
  const detailSchema = useMemo(() => usageDetailsSchema(schema, dataset), [schema, dataset]);
  const onFindRef = useRef(onFind);
  onFindRef.current = onFind;
  const [capabilities, setCapabilities] = useState<Capabilities>();
  const [capabilityError, setCapabilityError] = useState<Error>();
  const [capabilityRetry, setCapabilityRetry] = useState(0);
  const hostRef = useRef(host);
  hostRef.current = host;
  const hostFind = useRef("");
  // A server table runs only once it knows the server's capabilities: rows
  // and metrics both go through V3, and nothing reads the legacy routes
  // first. A failed handshake shows its error instead.
  useEffect(() => {
    if (!visible || capabilities) return;
    setCapabilityError(undefined);
    const controller = new AbortController();
    fetchCapabilities(dataset, controller.signal).then(setCapabilities, failure => {
      if (!controller.signal.aborted) setCapabilityError(failure instanceof Error ? failure : new Error(String(failure)));
    });
    return () => controller.abort();
  }, [dataset, Boolean(visible), Boolean(capabilities), capabilityRetry]);
  const computedColumnStore = useMemo(() => {
    if (!visible) return undefined;
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
  const transport = useMemo(() => {
    if (!capabilities) return undefined;
    return makeTransport(dataset, capabilities.queryCapabilities, find, value => onFindRef.current?.(value),
      host ? { find: state => hostRef.current?.find(state) ?? "", current: () => hostFind.current } : undefined);
  }, [dataset, find, capabilities, Boolean(host)]);
  const [bookmarkRevision, setBookmarkRevision] = useState(0);
  useEffect(() => { const changed = () => setBookmarkRevision(value => value + 1); window.addEventListener("pharos:bookmarks", changed); return () => window.removeEventListener("pharos:bookmarks", changed); }, []);
  const [recovered, setRecovered] = useState(false);
  const storage = useMemo(() => {
    const stored = libraryStorageAdapter({ fresh: new URLSearchParams(location.search).get("fresh") === "1", onRecovery: () => setRecovered(true) });
    // Fit a new Analyze table by default, while respecting every restored URL,
    // named/default save and last-used manual sizing choice.
    return usageActions ? { ...stored, loadLast: async (key: string) => await stored.loadLast(key) ?? (dataset==="message_labels"?messageLabelConversationQuery(fittedDefaultQuery(schema)):fittedDefaultQuery(schema)) } : stored;
  }, [bookmarkRevision, usageActions, schema]);
  const propertyStorage = useMemo(() => jsonPropertyViewStorage(
    key => preferences().get<string | null>(key, null),
    (key, value) => preferences().set(key, value),
  ), []);
  // Resolve an opened calendar search once. Repeated normalization and cursor
  // batches reuse the execution's bounds instead of following a moving clock.
  const openedSearches = useRef(new Map<string, HostQueryState>());
  const openedWindows = useRef(new Map<string,Set<string>>());
  const releasedWindow = useRef(false);
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
    const restored = rolling ? restoreRollingDefinition(query,queryFromURL(location.href)?.query,rolling,openedWindows.current) : {query,released:false};
    releasedWindow.current=restored.released;
    const resolved=restored.query;
    return usageActions ? usageActionQuery(orderedUsageQuery(dataset as UsageDataset, resolved), schema, dataset as UsageDataset) : withGroupDetail(resolved);
  }, [dataset, locationRevision, usageActions, schema]);
  const api = useQueryTable<Row>({ schema, transport,
    computedColumnStore, computedExecution: capabilities?.computedExecution,
    active: Boolean(visible) && Boolean(capabilities),
    // Keep URL configuration stable; active pauses execution/synchronization
    // without resetting restoration or undo history when a table is hidden.
    storage, links: portableLinkOptions, debounceMs: 100, syncUrl: true, queryParameter: `q_${dataset}`, canonicalizeQuery,
    // A bare reload restores the last query that loaded, never one that
    // failed, timed out or was cancelled; that one stays on screen to retry.
    persistLast: "succeeded", hostQuery: host?.query,
    itemDetails: { attention, storage: propertyStorage, ...(detailSchema ? { schema: detailSchema, defaultModes: ["modal", "expand"] as ("modal" | "expand")[] } : {}), relationships,
      loadRow: async (id, signal) => {
        const row = await queryRow<Row>(dataset, schema, id, signal);
        if (row && dataset === "tool_calls") {
          const detail = await queryRequest<Row>(`/api/tool-calls/${encodeURIComponent(String(id))}`, { signal });
          for (const field of ["input_text", "input_text_bytes", "input_text_truncated", "result_text", "result_text_bytes", "result_text_truncated", "result_details", "commands", "urls", "request", "next_request"]) row[field] = detail[field];
        }
        return row ? { row, loadedFields: Object.keys(row) } : null;
      } },
    onRefresh: () => { openedSearches.current.clear(); const current=apiRef.current;const selectedWindow=readRollingWindow(new URLSearchParams(location.search),dataset);if(selectedWindow?.mode==="today")current.setQuery(previous=>resolveRollingQuery(previous,selectedWindow));const renewed=refreshTodayHost(apiRef.current.query.hostState); if(renewed && !sameHostState(renewed,apiRef.current.query.hostState))apiRef.current.setQuery(previous=>({...previous,hostState:renewed,offset:0})); },
  });
  const promoteLink = useMemo(() => portableQueryLinks(schema.name, storage.snapshots), [schema.name, storage]);
  useEffect(() => {
    if (!visible || !api.restoration.ready) return;
    let cancelled = false;
    const promote = () => { if (!cancelled) void promoteLink(`q_${dataset}`, () => location.href, url => {
      if (cancelled) return;
      history.replaceState(history.state, "", url);
      window.dispatchEvent(new Event("pharos:uri-changed"));
    }).catch(() => { /* The self-contained URL remains usable on save failure. */ }); };
    // Native URL synchronization may be debounced. Listen for its completion.
    window.addEventListener("pharos:uri-changed", promote);
    queueMicrotask(promote);
    return () => { cancelled = true; window.removeEventListener("pharos:uri-changed", promote); };
  }, [visible, api.restoration.ready, api.query, promoteLink]);
  const hostState = api.query.hostState, hostKey = JSON.stringify(hostState ?? null);
  hostFind.current = host ? host.find(hostState) : "";
  const searching = Boolean(find || hostFind.current);
  useEffect(() => { hostRef.current?.onState?.(hostState); }, [hostKey]);
  // A default saved query that the service rejects or that runs out of time
  // is skipped on later visits (storage.ts); one that loads is restored again.
  const receipt = api.execution;
  useEffect(() => {
    if (!visible || !receipt || receipt.status === "running") return;
    const restoration = { defaults: api.defaults, canonicalize: canonicalizeQuery };
    if (receipt.status === "succeeded") acceptDefaultQuery(schema.name, receipt.query, restoration);
    // Only failed rows: a default whose rows load is not skipped for one metric.
    else if (receipt.rows === "failed" && shouldSkipQueryOnRestore(receipt.error)) rejectDefaultQuery(schema.name, receipt.query, restoration);
  }, [visible, receipt?.key, receipt?.status]);
  // Empty states describe the rows: a failed metric alone does not empty them.
  const failed = api.execution ? api.execution.rows === "failed" : Boolean(api.error);
  const resultsView = dataset==="message_labels"?labelResultsMode(api.query):searchResultsView(api.query, dataset === "library" ? readLibraryHost(hostState).view : "table");
  const attentionSchema = api.itemDetails?.schema.name;
  const detailsReady = nativeDetailsReady({ visible: Boolean(visible), restored: api.restoration.ready, loading: api.loading, rows: api.execution?.rows, schema: attentionSchema });
  const nativeDetailSurface = resultsView === "table";
  useEffect(() => {
    if (!detailsReady || !attentionSchema) return;
    // Wait for the result page: otherwise native details can reveal in an
    // off-page fallback which the arriving rows immediately replace.
    // Custom conversation/text presentations do not mount native properties.
    // Report unavailable rather than waiting for a provider that cannot exist.
    const unregister = registerDetailAttentionResolver((reference, signal) => nativeDetailSurface
      ? revealQueryDetail(attention, attentionSchema, reference, signal) : Promise.resolve("unavailable"));
    return () => { unregister(); attention.cancel(); };
  }, [detailsReady, attentionSchema, attention, nativeDetailSurface]);
  const apiRef = useRef(api);
  apiRef.current = api;
  // A new search starts from its first page.
  const searched = useRef(find);
  useEffect(() => {
    if (searched.current === find) return;
    searched.current = find;
    apiRef.current.setQuery(previous => previous.offset ? { ...previous, offset: 0 } : previous);
  }, [find]);
  const sourceFilters = api.query.grouping ? api.grouping!.source : api;
  tableApis.set(dataset, api);
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
    return () => { tableApis.delete(dataset); };
  }, [dataset]);
  useEffect(() => {
    if (!visible || !api.restoration.ready || api.error) return;
    const rolling = readRollingWindow(new URLSearchParams(location.search), dataset);
    if (rolling && (releasedWindow.current || !rollingQueryMatches(api.query.grouping?.source ?? api.query, rolling))) {
      for (const name of ["window_days", "window_field", "window_dataset", "window_mode", "window_until_field", "window_timezone"]) updateURI(name, "");
    } else if (!rolling) {
      const savedWindow = savedRollingWindow(dataset, api.query);
      if (savedWindow) {
        updateURI("window_days", String(savedWindow.days)); updateURI("window_field", savedWindow.field); updateURI("window_dataset", dataset);
        updateURI("window_mode", savedWindow.mode === "today" ? "today" : savedWindow.mode === "relative" ? "relative" : "");
        updateURI("window_timezone", savedWindow.mode === "today" ? savedWindow.timezone ?? "" : "");
        updateURI("window_until_field", savedWindow.untilField ?? "");
      }
    }
  }, [dataset, api.query, visible, api.restoration.ready, api.error]);

  // Where a page opts in, Back and Forward step through its query edits.
  const steps = useMemo(() => queryHistory ? createQueryHistory(dataset, window.history) : null, [queryHistory, dataset]);
  // Query Table starts restoring in the same event, so the render that counts
  // a navigation is not ready until the restored query is.
  const [navigations, setNavigations] = useState(0);
  useEffect(() => {
    if (!steps) return;
    const navigated = () => { steps.navigated(window.history.state); setNavigations(count => count + 1); };
    for (const name of ["popstate", "pharos:route"]) window.addEventListener(name, navigated);
    return () => { for (const name of ["popstate", "pharos:route"]) window.removeEventListener(name, navigated); };
  }, [steps]);
  useEffect(() => {
    if (!steps || !visible || !api.restoration.ready) return;
    if (steps.observe(api.query, query => apiRef.current.links.local(query, location.href, `q_${dataset}`).url)) window.dispatchEvent(new Event("pharos:uri-changed"));
  }, [steps, api.query, visible, api.restoration.ready, dataset, navigations]);
  // Drilldowns filter source records, including canonical computed fields.
  const sourceSchema = useMemo(() => ({ ...schema, fields: api.computed.catalogue }), [schema, api.computed.catalogue]);
  // Inactive controllers retain their query state, but their controls must not
  // mount: filter nullability/autocomplete and header charts issue metadata
  // requests independently of useQueryTable's active row-fetch flag.
  useOriginMetadata(Boolean(visible));
  if (!visible) return null;
  return <FilterValueProvider value={filterPresentations[dataset]}>
    <QueryRecovery busy={api.execution?.status === "running" || api.restoration.status === "resolving" || (!capabilities && !capabilityError)} error={capabilityError ?? api.error} recovered={recovered} identity={api.execution?.key ?? JSON.stringify(api.query)}
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
        const query = tableRecoveryQuery(schema);
        // Reset clears host clauses too, as the native Reset does.
        if (api.setDefinition && host) api.setDefinition({ query, hostState: host.query.defaults }); else api.setQuery(query);
        if (capabilityError) setCapabilityRetry(value => value + 1);
      }} />
    {Object.hasOwn(usageDatasets, dataset) ? <UsageQueryIdeas key={dataset} dataset={dataset as UsageDataset} api={api} /> : null}
    {presets?.(api)}
    {queryContent || resultPresentation ? <SourceQueryBuilder dataset={dataset} compact summary={querySummary?.(api)} api={api} fields={api.query.grouping ? api.schema!.fields : api.computed.catalogue} total={api.total} running={api.loading}
      whereRow={<>
        {resultPresentation?.(api)}
        <QueryFilterRow api={sourceFilters} fields={api.query.grouping ? api.grouping!.source.schema.fields : api.computed.catalogue} />
        {queryContent?.(api)}
      </>} /> : api.query.grouping ? <SourceQueryBuilder dataset={dataset} compact api={api} fields={api.schema!.fields} total={api.total} running={api.loading} /> : controls ? controls(api, <SourceQueryBuilder dataset={dataset} compact api={api} fields={api.computed.catalogue} total={api.total} running={api.loading} />) : recordSearchTargets[dataset] ? <RecordSearchEditor api={api} dataset={dataset} layout={<SourceQueryBuilder dataset={dataset} compact api={api} fields={api.computed.catalogue} total={api.total} running={api.loading} />} /> : <SourceQueryBuilder dataset={dataset} compact api={api} fields={api.computed.catalogue} total={api.total} running={api.loading} />}
    {queryContent ? controls?.(api, null) : null}
    <MetricsPanel api={api} aggregations={api.aggregations} fields={metricFields(api.query.grouping ? usageGroupFields(api.schema!.fields, api.query) : api.computed.catalogue)} renderers={metricRenderers}
      onInspect={(bucket, clause) => { const records = inspectBucketQuery(api.query, bucket, clause, sourceSchema); if (records) api.setQuery(Object.hasOwn(usageDatasets, dataset) ? orderedUsageQuery(dataset as UsageDataset, usageActions ? fitQueryToScreen({ ...records, orderBy: [] }) : { ...records, orderBy: [] }) : records); }} />
    {capabilityError && !capabilities ? <div className="query-table-error">Could not read this table's query capabilities: {capabilityError.message}</div> : null}
    {api.error ? <div className="query-table-error">{api.error?.message}</div> : null}
    {selectionBar && !api.query.grouping ? selectionBar(api) : null}
    {dataset==="message_labels"&&resultsView==="cards"?<MessageLabelCards api={api}/>:dataset === "library" && resultsView === "conversation" ? <ConversationResults api={api} searching={searching} failed={failed} renderers={tableRenderers} />

      : <DataTable
      maxHeight={100000}
      fields={api.query.grouping ? usageGroupFields(api.visibleFields, api.query) : api.visibleFields}
      rows={api.rows}
      query={api.query}
      onQueryChange={api.setQuery}
      renderers={tableRenderers}
      rowId={api.rowId}
      selection={selectionBar && !api.query.grouping ? api.selection : undefined}
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
        return <><span data-pharos-target={`row:${api.rowId(row)}`} aria-hidden="true" /><button type="button" className="mcp-detail-button" disabled={!records} title={records ? "Open the source records for this group, keeping source filters" : complete ? "These group keys cannot be matched exactly in the source records" : "Include all group key columns to inspect the source records"} onClick={() => { if (records) api.setQuery(Object.hasOwn(usageDatasets, dataset) ? orderedUsageQuery(dataset as UsageDataset, usageActions ? fitQueryToScreen({ ...records, orderBy: [] }) : { ...records, orderBy: [] }) : records); }}>Records</button></>;
      }, trailingLabel: usageActions ? "Drill in" : "Inspect" }
        : trailing ? { trailing: (row: Row) => <><span data-pharos-target={`row:${api.rowId(row)}`} aria-hidden="true" />{trailing(row, api)}</>, trailingLabel: dataset === "mcp_calls" || dataset === "tool_calls" ? "View" : dataset === "tools" || dataset === "writing" ? "Drill in" : dataset === "library" ? "Match" : "Actions" } : [...new URLSearchParams(location.search).getAll("highlight"),new URLSearchParams(location.search).get("reveal")??""].some(target=>target.startsWith("row:")) ? {trailing:(row:Row)=><span data-pharos-target={`row:${api.rowId(row)}`} aria-hidden="true" />} : {})}
    />}
  </FilterValueProvider>;
}

// The reader's requests.
function readerJSON(path: string, signal?: AbortSignal): Promise<Row> {
  return fetch(path, { signal }).then(responseJSON<Row>);
}

// A message search's matches, placed in the card summary. Each card loads
// its own, so the table page carries only one keyword hit per work.
function useWorkMatches(row: Row, find: string): WorkMatches | null {
  const hit = row.find_hit as FindHit | undefined;
  const wanted = Boolean(find && hit && !hit.url && !hit.path);
  const [loaded, setLoaded] = useState<{ key: string; matches: WorkMatches } | null>(null);
  const key = `${row.id}?${find}`;
  useEffect(() => {
    if (!wanted) return;
    const controller = new AbortController();
    void readerJSON(`/api/work/${encodeURIComponent(String(row.id))}/matches?${find}`, controller.signal)
      .then(matches => { if (!controller.signal.aborted) setLoaded({ key, matches: matches as WorkMatches }); })
      .catch(() => {}); // The row's own hit stays on show.
    return () => controller.abort();
  }, [key, wanted]);
  return wanted && loaded?.key === key && loaded.matches.items.some(item => item.kind === "match") ? loaded.matches : null;
}

function ConversationResultCard({ row, api, renderers, find }: { row: Row; api: QueryTableApi<Row>; renderers: RenderRegistry<Row>; find: string }) {
  const [browse, setBrowse] = useViewEntry("search_cards", `${row.id}/browse`, false);
  const [hasBrowsed, setHasBrowsed] = useState(false);
  const browserToggle = useRef<HTMLButtonElement>(null);
  const browserID = React.useId();
  const activitySummary = conversationActivitySummary(row);
  const firstInput = row.first_input ? { role: "user", text: row.first_input } : row.purpose ? { role: "user", text: row.purpose } : null;
  const latestResponse = row.last_response ? { role: "assistant", text: row.last_response } : row.outcome ? { role: "assistant", text: row.outcome } : null;
  const count = Number(row.find_match_count) || 1;
  const matches = useWorkMatches(row, find);
  const [target, setTarget] = useState<ResultTarget | null>(null);
  const openBrowser = () => { setHasBrowsed(true); setBrowse(true); browserToggle.current?.focus({ preventScroll: true }); };
  // A preview message opens Browse at its turn, marking a match's terms.
  const browseTo = (messageID: unknown, conversationID?: unknown, hit?: FindHit) => {
    const find = hit ? matchFindState(hit.snippet ?? "", hit.highlights) : null, pattern = find ? findPattern(find) : null;
    if (messageID) setTarget({ messageID: String(messageID), conversationID: conversationID ? String(conversationID) : undefined, pattern: pattern?.status === "ready" ? pattern.pattern : null });
    openBrowser();
  };
  const openHit = (hit: FindHit) => hit.message_id ? browseTo(hit.message_id, hit.conversation_id, hit) : openFindHit(hit);
  const match = row.find_hit ? <FindMatch hit={row.find_hit} label={`Match · ${count.toLocaleString()} ${findHitNoun(row.find_hit)}${count === 1 ? "" : "s"}`} onOpen={() => openHit(row.find_hit)} /> : null;
  const summaryMessage = (item: WorkMatchItem, label: string) => {
    if (item.kind === "match") {
      const hit = { ...(row.find_hit as FindHit), conversation_id: item.conversation_id, message_id: item.message_id, role: item.role, message_kind: item.message_kind, snippet: item.snippet, highlights: item.highlights };
      return <FindMatch key={item.message_id} label={label} hit={hit} onOpen={() => openHit(hit)} />;
    }
    const message = item.kind === "first" ? firstInput : latestResponse;
    return message ? <ResultMessage key={item.message_id} label={label} message={message} kind={item.kind === "first" ? "human" : "assistant"} compact onOpen={() => browseTo(item.message_id, item.conversation_id)} /> : null;
  };
  return <article className="conversation-result-card" data-pharos-target={`row:${row.id}`}>
    <div className="conversation-result-head">
      <div className="conversation-result-summary">
        <button className="conversation-result-title" type="button" onClick={() => window.pharosOpenDetail?.(String(row.id))}>{String(row.title ?? "Untitled work")}</button>
        <ConversationFacts row={row} fields={api.visibleFields} query={api.query} renderers={renderers} />
      </div>
      <div className="conversation-result-actions">
        <button type="button" onClick={() => window.pharosOpenDetail?.(String(row.id))}>Open <Icon name="arrow-right" /></button>
        <button type="button" ref={browserToggle} data-view-field="search_cards" className="conversation-browser-toggle" onClick={() => { setHasBrowsed(true); setBrowse(value => !value); }} aria-expanded={browse} aria-controls={browserID}><Icon name="tree" />{browse ? "Collapse turns" : "Browse"}<Icon name="chevron-down" /></button>
      </div>
    </div>
    {!browse && matches ? <div className="conversation-result-preview joined">
      {summaryEntries(matches, count).map((entry, index) => entry.type === "message" ? summaryMessage(entry.item, entry.label)
        : <button key={`gap:${index}`} type="button" data-view-field="search_cards" className="conversation-preview-browse" title={`Browse turns: ${activitySummary}`} aria-label={`${entry.label}. Browse turns: ${activitySummary}`} aria-expanded={browse} aria-controls={browserID} onClick={openBrowser}>
          <Icon name="tree" /><span>{entry.label}</span><Icon name="chevron-down" />
        </button>)}
    </div> : !browse && (firstInput || latestResponse || match) ? <div className={`conversation-result-preview${firstInput && (latestResponse || match) ? " joined" : ""}`}>
      {firstInput ? <ResultMessage label="First ask" message={firstInput} kind="human" compact onOpen={() => browseTo(row.first_input_id)} /> : null}
      <button type="button" data-view-field="search_cards" className="conversation-preview-browse" aria-label={`Browse turns: ${activitySummary}`} aria-expanded={browse} aria-controls={browserID} onClick={openBrowser}>
        <Icon name="tree" /><span>{activitySummary}</span><Icon name="chevron-down" />
      </button>
      {match}
      {latestResponse ? <ResultMessage label="Latest response" message={latestResponse} kind="assistant" compact onOpen={() => browseTo(row.last_response_id)} /> : null}
    </div> : browse && match ? <div className="conversation-result-preview">{match}</div> : !browse ? <p className="muted conversation-result-empty">No retained message preview.</p> : null}
    <div id={browserID} className={`conversation-browser-disclosure${browse ? " is-expanded" : ""}`} aria-hidden={!browse} ref={element => { if (element) element.inert = !browse; }}>
      <div className="conversation-browser-content">{hasBrowsed || browse ? <ResultConversationBrowser workspaceID={String(row.id)} readJSON={readerJSON} target={target} /> : null}</div>
    </div>
  </article>;
}

function ConversationResults({ api, searching, failed, renderers }: { api: QueryTableApi<Row>; searching: boolean; failed: boolean; renderers: RenderRegistry<Row> }) {
  const find = libraryFind(api.query.hostState);
  if (!api.loading && !api.rows.length) return <div className="conversation-results-empty no-results">{failed ? failedMessage : searching ? "No work matches this search and these filters." : "No work matches this query."}</div>;
  return <div className={`conversation-results ${api.loading ? "loading" : ""}`} aria-busy={api.loading}>
    {api.loading && !api.rows.length ? <div className="conversation-results-empty">Loading conversations…</div> : null}
    {api.rows.map((row) => <ConversationResultCard key={String(api.rowId(row) ?? row.id)} row={row} api={api} renderers={renderers} find={find} />)}
  </div>;
}

type FindHit = { workspace_id: string; conversation_id?: string; title: string; provider?: string; started_at?: string; message_id?: string; role?: string; message_kind?: string; snippet?: string; highlights?: [number, number][]; path?: string; old_path?: string; project?: string; url?: string; url_source?: string; tool_name?: string; tool_call_id?: string; attribution?: string; count: number };
// What the Library's keyword search found within the stored table filters.
type SearchProgress = { complete: boolean; stop_reason: string; stop_message?: string; next_cursor: string; count: number; count_relation: string; candidates: number; verified_bytes: number; skipped_oversize: number; elapsed_ms: number; strategy: string; coverage: string; expansions?: Record<string,string[]> };
type FindSummary = { workspaces: number; limited: boolean; search?: SearchProgress };

function openFindHit(hit: FindHit) {
  const params = new URLSearchParams();
  if (hit.conversation_id) params.set("conversation", hit.conversation_id);
  if (hit.message_id) params.set("message", hit.message_id);
  if (!hit.url && !hit.path) writeFindParameters(params, matchFindState(hit.snippet ?? "", hit.highlights));
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

const findHitNoun = (hit: FindHit) => hit.url ? "matching URL" : hit.path ? "matching file change" : "matching message";

// A keyword match, as a message in the conversation summary, with the
// query's matches marked.
function FindMatch({ hit, label, onOpen }: { hit: FindHit; label: string; onOpen: () => void }) {
  return <div className={`result-message compact library-find-match ${hit.role === "user" ? "human" : "assistant"}`} {...openableProps(onOpen)}>
    <div className="result-message-label">{label}</div>
    <div className="result-message-body library-find-snippet">{hit.url ? <a href={hit.url} target="_blank" rel="noopener noreferrer">{hit.url}</a>
      : (hit.path ? highlightParts : plainSnippetParts)(findHitText(hit), hit.highlights).map((part, index) => part.match ? <mark key={index}>{part.text}</mark> : part.text)}</div>
  </div>;
}

function FindMatchCell({ row }: { row: Row }) {
  const hit = row.find_hit as FindHit | undefined;
  if (!hit) return null;
  const count = Number(row.find_match_count) || 1;
  const open = <button type="button" className="library-find-cell" title={`${findHitText(hit)}\n\n${count.toLocaleString()} match${count === 1 ? "" : "es"} · ${findHitLabel(hit)}`} onClick={event => { event.stopPropagation(); openFindHit(hit); }}>Open match{count > 1 ? ` (${count.toLocaleString()})` : ""}</button>;
  // A file sessions edited opens its history: every session and commit.
  if (!hit.path || !hit.project) return open;
  return <>{open} <PageLink className="library-find-cell" href={fileHistoryURL(hit.path, hit.project)} title={`Every session that edited ${hit.path}`} onClick={event => event.stopPropagation()}>File history</PageLink></>;
}

// Search's content search (messages, files or URLs) and its result view are
// host query state (host-query-state.ts), saved and undone with the table query.
function LibraryPage() {
  const [hostState, setHostState] = useState<HostQueryState>();
  const host = useMemo((): SurfaceHost => ({ query: libraryHostQuery, find: libraryFind, onState: setHostState }), []);
  const find = libraryFind(hostState);
  const [found, setFound] = useState<FindSummary | null>(null);
  useEffect(() => { setFound(null); }, [find]);
  return <div className={`pharos-query-page${find ? " library-searching" : ""}`}>
    <div className="view-heading"><div><h1>Search</h1><p className="muted">Find past work. Follow a conversation, compare projects, or build a view of your own.</p></div></div>
    <QuerySurface dataset="library" host={host} resultPresentation={api => <SearchResultsDisplay view={searchResultsView(api.query, readLibraryHost(api.query.hostState).view)}
      onChange={next => api.setDefinition?.(previous => ({ query: queryForSearchResultsView(previous.query, next), hostState: libraryHostState({ ...readLibraryHost(previous.hostState), view: next }) }))} />}
      onFind={setFound} {...(find ? { trailing: (row: Row) => <FindMatchCell row={row} /> } : {})} queryContent={api => <LibrarySearchClause api={api} />} controls={() => <>
    {find && found ? <div className="library-find-status muted" role="status">
      <p>Limit controls workspace rows after filtering and sorting. Counts and metrics include the full matching population.</p>
      {found.workspaces === 0 ? <p>No messages matched these conditions. Try switching “and” to “or”, remove a condition, or choose a broader target.</p> : null}
      {found.search ? <ViewDisclosure viewKey="library:search-details" summary="Search details"><p>{found.search.count.toLocaleString()} matching messages; {found.search.elapsed_ms} ms. All eligible messages checked.</p><p>{found.search.coverage}</p>{found.search.expansions && Object.keys(found.search.expansions).length ? <p>Word variations: {Object.entries(found.search.expansions).map(([word, terms]) => `${word} → ${terms.join(", ")}`).join("; ")}</p> : null}</ViewDisclosure> : null}
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
  // Applying the search already in effect runs it again. A grouped query
  // keeps its table; the Conversations preference returns once ungrouped.
  const commit = (change: Partial<LibraryHost>, rerun = false) => {
    try {
      const hostState = changeLibrarySearch(state, change);
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

// Fetches V3 metrics for the given filters, refetching when they
// change or Usage is refreshed. Results are
// tagged so a stale response never renders under new chart settings.
function useMetrics(dataset: Dataset, where: WhereTerm[], clauses: AggregationClause[]) {
  const key = JSON.stringify({ where, clauses });
  const [state, setState] = useState<{ key: string; refresh: number; metrics: MetricResult[]; error: string; loading: boolean }>({ key: "", refresh: 0, metrics: [], error: "", loading: true });
  const [refresh, setRefresh] = useState(0);
  useEffect(() => onDatasetRefresh(dataset, () => setRefresh(value => value + 1)), [dataset]);
  useEffect(() => {
    const request = JSON.parse(key) as { where: WhereTerm[]; clauses: AggregationClause[] };
    if (!request.clauses.length) { setState({ key, refresh, metrics: [], error: "", loading: false }); return; }
    const controller = new AbortController();
    setState(previous => ({ ...previous, loading: true }));
    postMetrics(dataset, schemas[dataset], request.where, request.clauses, controller.signal)

      .then(metrics => setState({ key, refresh, metrics, error: "", loading: false }))
      .catch(failure => { if (!controller.signal.aborted) setState({ key, refresh, metrics: [], error: failure instanceof Error ? failure.message : String(failure), loading: false }); });
    return () => controller.abort();
  }, [dataset, key, refresh]);
  return { ...state, metrics: state.key === key ? state.metrics : [], loading: state.key !== key || state.refresh !== refresh || state.loading, error: state.key === key ? state.error : "" };
}

const costTick = formatUSD;
// Tokens and Cost amounts; shares and ratios are formatted by the trend.
const tokenAmount: AmountFormat = (value, measure) => measure.unit === "USD" ? formatUSD(value) : compactNumber(value);
const tokenTick: AmountFormat = (value, measure) => measure.unit === "USD" ? costTick(value) : compactNumber(value);

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
function ToolTimeSummary({ where, selected = false }: { where: WhereTerm[]; selected?: boolean }) {
  const result = useMetrics("tool_calls", where, [
    { id: "duration", op: "sum", field: "duration_ms", groupBy: [] },
    { id: "calls", op: "count", groupBy: [] },
    { id: "timed", op: "count", field: "duration_ms", groupBy: [] },
  ]);
  const value = (id: string) => Number(result.metrics.find(metric => metric.id === id)?.buckets[0]?.value) || 0;
  const duration = value("duration"), calls = value("calls"), timed = value("timed");
  const time = (ms: number) => ms >= 3600000 ? `${(ms / 3600000).toLocaleString(undefined, { maximumFractionDigits: 1 })} h` : ms >= 60000 ? `${(ms / 60000).toLocaleString(undefined, { maximumFractionDigits: 1 })} min` : `${(ms / 1000).toLocaleString(undefined, { maximumFractionDigits: 1 })} s`;
  const card = (label: string, amount: string, detail: string) => <div className="mcp-metric"><span>{label}</span><strong>{result.loading ? "Loading…" : result.error ? "Unavailable" : amount}</strong><small>{detail}</small></div>;
  return <div className="mcp-metrics dashboard-stats" aria-label="Tool Time summary" aria-busy={result.loading}>
    {card("Total recorded tool time", timed ? time(duration) : "—", `all activities in range${selected ? ", not only the legend selection" : ""} · concurrent calls can overlap`)}
    {card("Call volume", calls.toLocaleString(), "all matching calls, including those without timing")}
    {card("Time per timed run", timed ? time(duration / timed) : "—", `${timed.toLocaleString()} calls with a recorded duration`)}
    {card("Timing coverage", calls ? `${(100 * timed / calls).toLocaleString(undefined, { maximumFractionDigits: 1 })}%` : "—", `${(calls - timed).toLocaleString()} calls without timing`)}
  </div>;
}

type AuthorshipStatus = { built_at: string | null; running: boolean; stale: boolean; error: string | null; refined_at?: string | null; refinement_pending?: boolean; provisional_messages?: number; pending_messages?: number; deferred_messages?: number; refining?: boolean };

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
    if (preset === "today") { updateURI("analyze_window", "today"); updateURI("analyze_timezone", ""); }
    else if (preset || dates(where) !== dates(live.current.where)) updateURI("analyze_window", "");
    persist({ ...live.current, where }, "analyze_filters");
  };
  return { ...state, update, onFilter };
}

function GuidedUsageDashboard({ topic }: { topic: UsageTopic }) {
  if(topic==="labels")return <CatalogDashboard catalog={messageLabelsCatalog} dataset="message_labels" name="labels" defaults={dashboardDefaults.labels}/>;
  if(topic==="thinking-overview"||topic==="thinking-detail") return <ThinkingDashboard detail={topic==="thinking-detail"}/>;
  if (topic === "calls") return <CatalogDashboard catalog={toolTimeAnalysisCatalog} dataset="tool_calls" name="calls" defaults={dashboardDefaults.calls} />;
  if (topic === "activity") return <CatalogDashboard catalog={conversationAnalysisCatalog} dataset="conversation_activity" name="activity" defaults={dashboardDefaults.activity} summary={where => <ConversationsSummary where={where} />} />;
  if (topic === "tokens") return <TokenUsageDashboard />;
  if (topic === "human") return <HumanUsageDashboard />;
  if (topic === "parallelism") return <ParallelismUsageDashboard />;
  if (topic === "habits") return <HabitsDashboard />;
  if (topic === "tools") return <ToolsUsageDashboard />;
  if (topic === "mcp") return <MCPUsageDashboard />;
  if (topic === "carbon") return <CarbonDashboard />;
  if (topic === "time") return <ToolTimeDashboard />;
  if (topic === "agent_experience") return <AgentExperienceDashboard />;
  if (topic === "outcomes") return <OutcomesDashboard />;
  if (topic === "branches") return <BranchesDashboard />;
  return <SkillsDashboard />;
}

// CO₂e and electricity in the Carbon page's units; other amounts compactly.
const carbonAmount = (format: (value: number, metric: string) => string): AmountFormat => (value, measure) => measure.unit === "g CO₂e" ? format(value, "co2") : measure.unit === "Wh" ? format(value, "energy") : compactNumber(value);
const carbonLoading = <p className="muted" role="status">Loading your Carbon assumptions…</p>;

// Carbon's views, drawn with the catalog of the current assumptions inside
// the Carbon page's filters and estimate. Older links read through the shape.
function CarbonDashboard() {
  const carbon = useCarbonCatalog();
  return <CatalogDashboard catalog={carbon.catalog ?? carbonCatalogShape} dataset="usage" name="carbon" defaults={dashboardDefaults.carbon}
    frame={(views, where, onFilter) => <CarbonImpact where={where} onFilter={onFilter}>{carbon.catalog ? views(carbon.catalog, carbonAmount(carbon.format)) : carbonLoading}</CarbonImpact>} />;
}

// Habits: the panel titles the page; the catalog chart below keeps its own title.
function HabitsDashboard() {
  const headline = useDashboardHeadline();
  return <CatalogDashboard catalog={habitsAnalysisCatalog} dataset="rhythm" name="habits" defaults={dashboardDefaults.habits} chartHeadline={false}
    frame={views => <>
      {headline ? createPortal(<div className="usage-chart-title usage-chart-headline"><h2>Habits</h2></div>, headline) : null}
      <HabitsPanel analysis={views(habitsAnalysisCatalog)} />
    </>} />;
}

// Parallelism's views follow the saved counting and gap, with which the
// service builds its records; the note names the settings the returned
// records carry, and warns when another window saved others.
function ParallelismUsageDashboard() {
  const [selection, setSelection] = useState<ConcurrencySelection | null>(null);
  const { catalog, settings } = useParallelismCatalog();
  const ribbon = selection?.settings, differs = ribbon && (ribbon.counting !== settings.counting || ribbon.gap !== settings.gap);
  const headline = useDashboardHeadline();
  // The ribbon comes first, so it titles the page; the catalog chart below
  // keeps its own title, and each has its own card.
  return <CatalogDashboard catalog={catalog} linkCatalog={parallelismCatalogShape} dataset="parallelism" name="parallelism" defaults={dashboardDefaults.parallelism} chartHeadline={false}
    summary={where => <ParallelismSettingsNote where={where} believed={settings} />}
    before={<>
      {headline ? createPortal(<div className="usage-chart-title usage-chart-headline"><h2>{ribbonCardTitle(selection?.settings ?? readConcurrencySettings())}</h2><CreateDashboardCard target="ribbon" /></div>, headline) : null}
      <ConcurrencyPanel routeParameter="analyze_parallelism" onSelection={setSelection} />
      {selection ? <UsageDashboardLinks schema={schemas.parallelism} dataset="parallelism" query={parallelismSelectionQuery(selection)} /> : null}
      <p className="usage-dashboard-note">The query view contains the hourly occupancy records touching the selected range, with the same repository, counting, gap, and ribbon selection. Those records describe threaded activity; model-only timing remains in the chart.</p>
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
const validTokenDashboard = (view: TokenTrendView) => validAnalysisState(tokenAnalysisCatalog, view);

function TokenUsageDashboard() {
  const state = useUsageDashboardState<TokenTrendView>(dashboardDefaults.tokens as TokenTrendView, validTokenDashboard, usageTime, (view, where) => readAnalysisLink(tokenAnalysisCatalog, view, where));
  const choose = (next: AnalysisView) => state.update(switchView(tokenAnalysisCatalog, state.view, next));
  return <><PricingNotice copy={copyText} />{state.error ? <p className="query-table-error" role="alert">{state.error}</p>
    : !isTrendView(state.view.view) ? <DashboardAnalysis catalog={tokenAnalysisCatalog} schema={schemas.usage} dataset="usage" state={state.view} where={state.where}
    onChange={state.update} onView={choose} onFilter={state.onFilter} />
    : <AnalysisTrend catalog={tokenAnalysisCatalog} schema={schemas.usage} dataset="usage" state={state.view} where={state.where} onChange={state.update} onView={choose} onFilter={state.onFilter}
    format={tokenAmount} tick={tokenTick}>
    <TokenSummary where={state.where} />
  </AnalysisTrend>}</>;
}

// Human Words reads messages (writing_messages) through the engine. Text
// classification runs in the background; each finished build reruns the
// dashboard's queries, and the note says where it stands.
function HumanUsageDashboard() {
  const [reload, setReload] = useState(0);
  // Refresh, or a finished classification, reloads the message mix too.
  useEffect(() => onDatasetRefresh("writing_messages", () => setReload(value => value + 1)), []);
  const status = useAuthorshipStatus(() => window.dispatchEvent(new Event("pharos:usage-refresh")));
  return <CatalogDashboard catalog={humanAnalysisCatalog} dataset="writing_messages" name="human" defaults={dashboardDefaults.human} readWhere={readHumanWhere}
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
  return { where: read.where, notice: <>This older link’s {read.unsupported.some(selection) ? "legend selection and filters" : "filters"} on whole works cannot keep their meaning for messages, so they are left out: {read.unsupported.map(describeTerm).join("; ")}.</> };
};

// The dashboard's population, whatever the chart draws.
const humanSummaryMetrics: AggregationClause[] = [
  { id: "typed", op: "sum", field: "typed_words", groupBy: [] },
  { id: "words", op: "sum", field: "words", groupBy: [] },
  { id: "typed_messages", op: "sum", expression: "SUM(IF([typed_words]>0,1,0))", groupBy: [] },
  { id: "messages", op: "count", groupBy: [] },
];
function HumanWordsSummary({ where, status }: { where: WhereTerm[]; status: AuthorshipStatus | null }) {
  const result = useMetrics("writing_messages", where, humanSummaryMetrics);
  const value = (id: string) => Number(result.metrics.find(metric => metric.id === id)?.buckets[0]?.value) || 0;
  const typed = value("typed"), words = value("words"), typedMessages = value("typed_messages");
  const building = status !== null && !status.built_at;
  const card = (label: string, amount: string, detail: string) => <div className="mcp-metric"><span>{label}</span><strong>{building ? "Building…" : result.loading ? "Loading…" : result.error ? "Unavailable" : amount}</strong><small>{detail}</small></div>;
  return <>
    <div className="mcp-metrics dashboard-stats" aria-label="Human Words summary" aria-busy={result.loading}>
      {card("Words you wrote", compactNumber(typed), "typed or dictated in the selected period")}
      {card("Messages with your writing", typedMessages.toLocaleString(), `of ${value("messages").toLocaleString()} classified user messages`)}
      {card("Words per message", typedMessages ? Math.round(typed / typedMessages).toLocaleString() : "—", "average over messages containing your writing")}
      {card("Typed share", words ? `${Math.round(typed / words * 100)}%` : "—", `of ${compactNumber(words)} words in your messages`)}
    </div>
    {status ? <p className="usage-dashboard-note" role="status">{status.error ? `Writing update failed: ${status.error}` : status.refining ? "Refining writing history… Recent estimates may change." : status.running ? "Updating writing estimates…" : status.built_at ? `Writing updated ${new Date(status.built_at).toLocaleString()}.` : "Waiting for writing estimates."}{status.pending_messages ? ` ${status.pending_messages.toLocaleString()} messages await an estimate.` : ""}{status.provisional_messages ? ` ${status.provisional_messages.toLocaleString()} messages have provisional estimates.` : ""}{status.deferred_messages ? ` ${status.deferred_messages.toLocaleString()} large messages await overnight refinement.` : ""}{status.refined_at ? ` History refined ${new Date(status.refined_at).toLocaleString()}.` : ""}{status.refinement_pending ? " Historical refinement is scheduled overnight." : ""}</p> : null}
  </>;
}

// Conversations' population, whatever the chart draws.
const conversationSummaryMetrics: AggregationClause[] = [
  { id: "conversations", op: "count_distinct", field: "root_conversation_id", groupBy: [] },
  { id: "messages", op: "sum", field: "human_messages", groupBy: [] },
  { id: "output", op: "sum", field: "output_tokens", groupBy: [] },
  { id: "projects", op: "count_distinct", field: "project_name", groupBy: [] },
];
function ConversationsSummary({ where }: { where: WhereTerm[] }) {
  const result = useMetrics("conversation_activity", where, conversationSummaryMetrics);
  const value = (id: string) => Number(result.metrics.find(metric => metric.id === id)?.buckets[0]?.value) || 0;
  const conversations = value("conversations"), messages = value("messages"), output = value("output");
  const per = (amount: number) => { const mean = amount / conversations; return conversations ? `${mean < 10 ? mean.toLocaleString(undefined, { maximumFractionDigits: 1 }) : compactNumber(mean)} per conversation` : "none in this range"; };
  const card = (label: string, amount: string, detail: string) => <div className="mcp-metric"><span>{label}</span><strong>{result.loading ? "Loading…" : result.error ? "Unavailable" : amount}</strong><small>{detail}</small></div>;
  return <div className="mcp-metrics dashboard-stats" aria-label="Conversations summary" aria-busy={result.loading}>
    {card("Conversations", conversations.toLocaleString(), "top-level, with their delegated work")}
    {card("Your messages", messages.toLocaleString(), per(messages))}
    {card("Output tokens", compactNumber(output), per(output))}
    {card("Projects", value("projects").toLocaleString(), "with conversation activity")}
  </div>;
}

// An engine dashboard: Over time, or Compare, Distribution, Scatter and Cycle
// over one catalog, with the dashboard's own panels around it. Its link is
// read through readAnalysisLink, so an older link opens as it always did.
const durationText = (ms: number) => Math.abs(ms) >= 3_600_000 ? `${(ms / 3_600_000).toLocaleString(undefined, { maximumFractionDigits: 1 })} h` : formatDurationMS(ms);
// Durations in hours where the measure says so (Tool Time's recorded time),
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
// chartHeadline={false} leaves the page's heading to a chart in before.
function CatalogDashboard({ catalog, linkCatalog = catalog, dataset, name, defaults, before, after, summary, panels, readWhere, format = catalogAmount, frame, chartHeadline = true }: {
  catalog: AnalysisCatalog; linkCatalog?: AnalysisCatalog; dataset: UsageDataset; name: string; defaults: AnalysisState; chartHeadline?: boolean;
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
      {!isTrendView(state.view.view)
        ? <DashboardAnalysis catalog={drawn} schema={schema} dataset={dataset} state={state.view} where={state.where} onChange={state.update} onView={choose} onFilter={state.onFilter}>{summary?.(state.where, Boolean(state.view.selection?.values.length))}</DashboardAnalysis>
        : <AnalysisTrend catalog={drawn} schema={schema} dataset={dataset} state={state.view} where={state.where} onChange={state.update} onView={choose} onFilter={state.onFilter} format={amount} headline={chartHeadline}>{summary?.(state.where, Boolean(state.view.selection?.values.length))}</AnalysisTrend>}
      {panels?.(state.where, state.onFilter)}
    </>;
  };
  return <DashboardCardSourceContext.Provider value={{ catalog, state: state.view, where: state.where, schema }}><section className={`usage-catalog-dashboard usage-${name}-dashboard`}>
    {notice.current ? <p className="usage-dashboard-note" role="status">{notice.current}</p> : null}
    {before}
    {frame ? frame(views, state.where, state.onFilter) : views(catalog)}
    {after}
  </section></DashboardCardSourceContext.Provider>;
}

// Agent Experience: events found in agents' replies and thinking (agent_events),
// with rates over every agent message the pass read (agent_event_messages).
// Events come from the agent events pass on this Mac (the Settings switch);
// an empty library says what fills it.
const agentEventMessageFields = agentEventMessagesDocument.fields.map(field => field.name);
const agentEventSummaryMetrics: AggregationClause[] = [
  { id: "events", op: "count", groupBy: [] },
  { id: "messages", op: "count_distinct", field: "message_id", groupBy: [] },
  { id: "conversations", op: "count_distinct", field: "conversation_id", groupBy: [] },
  { id: "thinking", op: "sum", field: "in_thinking", groupBy: [] },
];
const agentEventReadMetrics: AggregationClause[] = [
  { id: "read", op: "count", groupBy: [] },
  { id: "conversations", op: "count_distinct", field: "conversation_id", groupBy: [] },
];
// Filters that also apply to the messages read (agentEventReadWhere).
const readWhere = (where: WhereTerm[]) => agentEventReadWhere(where, agentEventMessageFields);
function AgentExperienceSummary({ where }: { where: WhereTerm[] }) {
  const events = useMetrics("agent_events", where, agentEventSummaryMetrics);
  const read = useMetrics("agent_event_messages", readWhere(where), agentEventReadMetrics);
  const value = (result: typeof events, id: string) => Number(result.metrics.find(metric => metric.id === id)?.buckets[0]?.value) || 0;
  const loading = events.loading || read.loading, error = events.error || read.error;
  const messages = value(events, "messages"), scanned = value(read, "read"), total = value(events, "events");
  const card = (label: string, amount: string, detail: string) => <div className="mcp-metric"><span>{label}</span><strong>{loading ? "Loading…" : error ? "Unavailable" : amount}</strong><small>{detail}</small></div>;
  if (!loading && !error && !scanned) return <p className="usage-dashboard-note" role="status">No agent messages have been read for events yet. They are read in the background once agent events are turned on in Settings → Background Processing; the first run downloads a small model.</p>;
  return <div className="mcp-metrics dashboard-stats" aria-label="Agent Experience summary" aria-busy={loading}>
    {card("Events", total.toLocaleString(), "matching sentences in agents' replies and thinking")}
    {card("Messages with an event", scanned ? `${(100 * messages / scanned).toFixed(1)}%` : "—", `${messages.toLocaleString()} of ${scanned.toLocaleString()} agent messages read`)}
    {card("Conversations with an event", value(events, "conversations").toLocaleString(), `of ${value(read, "conversations").toLocaleString()} conversations read`)}
    {card("In thinking", total ? `${(100 * value(events, "thinking") / total).toFixed(0)}%` : "—", "of these events were in agents' private reasoning")}
  </div>;
}
// The rate panel: events by day and kind group over messages read by day,
// under the dashboard's filters (the event-only ones narrow the events).
const agentEventDailyEvents: AggregationClause[] = [{ id: "events", op: "count", groupBy: ["day", "family_group"] }];
const agentEventDailyRead: AggregationClause[] = [{ id: "read", op: "count", groupBy: ["day"] }];
function AgentExperienceRates({ where }: { where: WhereTerm[] }) {
  const events = useMetrics("agent_events", where, agentEventDailyEvents);
  const read = useMetrics("agent_event_messages", readWhere(where), agentEventDailyRead);
  const readBuckets = read.metrics.find(metric => metric.id === "read")?.buckets ?? [];
  const period = ratePeriod(daySpan(readBuckets));
  const periods = eventRates(events.metrics.find(metric => metric.id === "events")?.buckets ?? [], readBuckets, period);
  return <AgentEventRates periods={periods} period={period} loading={events.loading || read.loading} error={events.error || read.error} />;
}
function AgentExperienceDashboard() {
  return <CatalogDashboard catalog={agentEventsAnalysisCatalog} dataset="agent_events" name="agent-experience" defaults={dashboardDefaults.agent_experience}
    summary={where => <AgentExperienceSummary where={where} />}
    panels={where => <>
      <AgentExperienceRates where={where} />
      <p className="usage-dashboard-note">Events are sentences a small model judged to express one kind of event, found from keyword candidates in agents' visible replies and thinking. It is model output, not review: expect some misses and false alarms, and compare rates rather than reading single counts. Confidence threshold 0.5.</p>
      <AgentEventExamples where={where} queryURL={usageWorkspaceURL({ mode: "query", dataset: "agent_events", query: { ...EMPTY_QUERY, where } })} />
    </>} />;
}

// Outcomes: sessions by what became of their changes (outcomes), counted at
// their start. A breakdown needing local Git evidence is offered disabled with
// its reason when no session in the library reached that tier, so a library
// without Git draws every panel from its transcripts.
const outcomeSummaryMetrics: AggregationClause[] = [
  { id: "sessions", op: "count", groupBy: [] },
  { id: "cost", op: "sum", field: "cost_usd", groupBy: [] },
  { id: "kept", op: "sum", field: "kept_change", groupBy: [] },
  { id: "rework", op: "sum", field: "rework_removed_lines", groupBy: [] },
  { id: "kept_lines", op: "sum", field: "kept_lines", groupBy: [] },
];
const outcomeTierMetrics: AggregationClause[] = [{ id: "sessions", op: "count", groupBy: ["evidence_tier"] }];
const outcomeReworkMetrics: AggregationClause[] = [
  { id: "removed", op: "sum", field: "rework_removed_lines", groupBy: ["project_name"] },
  { id: "kept", op: "sum", field: "kept_lines", groupBy: ["project_name"] },
];
const outcomeKeptCostMetrics: AggregationClause[] = [
  { id: "cost", op: "sum", field: "cost_usd", groupBy: ["model_family"] },
  { id: "kept", op: "sum", field: "kept_change", groupBy: ["model_family"] },
];
const outcomePercent = (value: number | null) => value == null ? "—" : `${(100 * value).toFixed(1)}%`;
const metricBuckets = (result: { metrics: MetricResult[] }, id: string) => result.metrics.find(metric => metric.id === id)?.buckets ?? [];
function OutcomesSummary({ where }: { where: WhereTerm[] }) {
  const result = useMetrics("outcomes", where, outcomeSummaryMetrics);
  const value = (id: string) => Number(metricBuckets(result, id)[0]?.value) || 0;
  const sessions = value("sessions"), kept = value("kept"), keptLines = value("kept_lines");
  // A sum of no priced usage is unknown, not $0.
  const cost = metricBuckets(result, "cost")[0]?.value;
  const card = (label: string, amount: string, detail: string) => <div className="mcp-metric"><span>{label}</span><strong>{result.loading ? "Loading…" : result.error ? "Unavailable" : amount}</strong><small>{detail}</small></div>;
  return <div className="mcp-metrics dashboard-stats" aria-label="Outcomes summary" aria-busy={result.loading}>
    {card("Sessions", sessions.toLocaleString(), "counted at their start")}
    {card("API cost", cost == null && sessions ? "Unpriced" : formatUSD(Number(cost) || 0), "list-price equivalent; unpriced usage excluded")}
    {card("Kept changes", sessions ? outcomePercent(kept / sessions) : "—", `${kept.toLocaleString()} sessions landed, committed, or changed and kept`)}
    {card("Rework rate", keptLines ? outcomePercent(value("rework") / keptLines) : "—", keptLines ? `of ${compactNumber(keptLines)} kept lines, removed by later sessions` : "no kept lines recorded")}
  </div>;
}
function OutcomeRatioPanel({ title, note, where, clauses, breakdown, format, empty }: { title: string; note: string; where: WhereTerm[]; clauses: AggregationClause[]; breakdown: string; format: (row: ReturnType<typeof outcomeRatioRows>[number]) => [string, string]; empty: string }) {
  const result = useMetrics("outcomes", where, clauses);
  const rows = outcomeRatioRows(metricBuckets(result, clauses[0]!.id), metricBuckets(result, clauses[1]!.id), key => outcomeGroupLabel(breakdown, key));
  return <section className="outcome-panel" aria-label={title} aria-busy={result.loading}>
    <h3>{title}</h3>
    <p className="muted">{note}</p>
    {result.loading ? <p className="muted" role="status">Loading…</p> : result.error ? <p className="query-table-error" role="alert">{result.error}</p>
      : !rows.length ? <p className="muted">{empty}</p>
      : <table><tbody>{rows.map(row => { const [amount, detail] = format(row); return <tr key={row.key}><th scope="row">{row.label}</th><td>{amount}</td><td className="muted">{detail}</td></tr>; })}</tbody></table>}
  </section>;
}
function OutcomeCoveragePanel({ where }: { where: WhereTerm[] }) {
  const result = useMetrics("outcomes", where, outcomeTierMetrics);
  const tiers = outcomeTierCoverage(metricBuckets(result, "sessions"));
  return <section className="outcome-panel" aria-label="Evidence coverage" aria-busy={result.loading}>
    <h3>Evidence coverage</h3>
    <p className="muted">The strongest evidence each session's outcome rests on: local Git where it checked a repository on this Mac, otherwise the session's own tool calls.</p>
    {result.loading ? <p className="muted" role="status">Loading…</p> : result.error ? <p className="query-table-error" role="alert">{result.error}</p>
      : <table><tbody>{tiers.map(tier => <tr key={tier.tier}><th scope="row">{tier.label}</th><td>{tier.sessions.toLocaleString()}</td><td className="muted">{outcomePercent(tier.share)}</td></tr>)}</tbody></table>}
  </section>;
}
function OutcomesDashboard() {
  // Tiers are read over the whole library, so a filter never hides a breakdown.
  const reached = useMetrics("outcomes", [], outcomeTierMetrics);
  const catalog = outcomeCatalogFor(outcomeTiersReached(metricBuckets(reached, "sessions")));
  return <CatalogDashboard catalog={catalog} linkCatalog={outcomesAnalysisCatalog} dataset="outcomes" name="outcomes" defaults={dashboardDefaults.outcomes}
    summary={where => <OutcomesSummary where={where} />}
    panels={where => <div className="outcome-panels">
      <OutcomeRatioPanel title="Rework rate by project" where={where} clauses={outcomeReworkMetrics} breakdown="project_name" empty="No session recorded kept lines."
        note="Of the lines each project's sessions added and kept, the share later sessions removed, pooled over the project's lines."
        format={row => [outcomePercent(row.ratio), row.denominator ? `of ${compactNumber(row.denominator)} kept lines` : "no kept lines"]} />
      <OutcomeRatioPanel title="Cost per kept change by model" where={where} clauses={outcomeKeptCostMetrics} breakdown="model_family" empty="No session recorded usage."
        note="Each main model's sessions' whole cost, over those whose changes were kept."
        format={row => {
          const total = row.numerator == null ? "unpriced" : `${formatUSD(row.numerator)} total`;
          return [row.ratio != null ? formatUSD(row.ratio) : row.numerator == null && row.denominator ? "Unpriced" : "—", row.denominator ? `${row.denominator.toLocaleString()} kept · ${total}` : `no kept change · ${total}`];
        }} />
      <OutcomeCoveragePanel where={where} />
    </div>} />;
}

// Spend by Branch: priced usage of sessions in Git repositories, per branch,
// turn by turn (branch_usage). Its summary is the coverage split. A library
// with no session in a repository has nothing to split and says so.
const branchSummaryMetrics: AggregationClause[] = [
  { id: "cost", op: "sum", field: "cost_usd", groupBy: ["coverage"] },
  { id: "branches", op: "count_distinct", field: "branch", groupBy: [] },
];
const branchPresenceMetrics: AggregationClause[] = [{ id: "rows", op: "count", groupBy: [] }];
function BranchesSummary({ where }: { where: WhereTerm[] }) {
  const result = useMetrics("branch_usage", where, branchSummaryMetrics);
  const coverage = branchCoverage(metricBuckets(result, "cost"));
  const card = (label: string, amount: string, detail: string) => <div className="mcp-metric"><span>{label}</span><strong>{result.loading ? "Loading…" : result.error ? "Unavailable" : amount}</strong><small>{detail}</small></div>;
  const detail: Record<string, string> = { on_branch: "on a recorded branch", unknown: "before a session's first branch, or on a detached HEAD", no_branch_data: "in sessions that recorded no branch" };
  return <div className="mcp-metrics dashboard-stats" aria-label="Spend by branch coverage" aria-busy={result.loading}>
    {coverage.map(item => card(item.label, formatUSD(item.cost), `${outcomePercent(item.share)} of spend ${detail[item.coverage]}`))}
    {card("Branches", (Number(metricBuckets(result, "branches")[0]?.value) || 0).toLocaleString(), "with recorded spend")}
  </div>;
}
function BranchesDashboard() {
  // Read over the whole library, so a filter never hides the dashboard.
  const present = useMetrics("branch_usage", [], branchPresenceMetrics);
  if (!present.loading && !present.error && !(Number(metricBuckets(present, "rows")[0]?.value) > 0))
    return <p className="usage-dashboard-note" role="status">Spend by branch covers sessions in Git repositories, and none of this library's sessions with usage ran in one. Outcomes and Tokens and Cost cover every session.</p>;
  return <CatalogDashboard catalog={branchAnalysisCatalog} dataset="branch_usage" name="branches" defaults={dashboardDefaults.branches}
    summary={where => <BranchesSummary where={where} />} />;
}

function MCPUsageDashboard() {
  return <CatalogDashboard catalog={mcpAnalysisCatalog} dataset="mcp_calls" name="mcp" defaults={dashboardDefaults.mcp} after={<MCPUsageNote />} />;
}

// The Tools dashboard reads the daily rollup; its compare view opened by
// default, as before.
function ToolsUsageDashboard() {
  return <CatalogDashboard catalog={toolsAnalysisCatalog} dataset="tools" name="tools" defaults={dashboardDefaults.tools} before={<ToolLedgerBanner />} />;
}

function ToolTimeDashboard() {
  return <CatalogDashboard catalog={toolTimeAnalysisCatalog} dataset="tool_calls" name="time" defaults={dashboardDefaults.time}
    summary={(where, selected) => <ToolTimeSummary where={where} selected={selected} />} />;
}

function SkillsDashboard() {
  return <CatalogDashboard catalog={skillsAnalysisCatalog} dataset="skill_usages" name="skills" defaults={dashboardDefaults.skills} />;
}

// An older Analyze or Tools link opens what replaced its page
// (canonicalAnalyzeURL); the address says so before anything reads it.
function UsagePage() {
  useLocationRevision();
  if (location.pathname !== "/analyze") return null;
  if(new URLSearchParams(location.search).has("label_lab")){history.replaceState(history.state,"",messageLabelLegacyURL(new URLSearchParams(location.search)));}
  if (!isUsageWorkspace()) history.replaceState(history.state, "", canonicalAnalyzeURL(location.pathname, new URLSearchParams(location.search)) + location.hash);
  return <UsageChartColorsProvider>
    <UsageWorkspace renderDashboard={topic => <GuidedUsageDashboard topic={topic} />} renderTable={dataset => <AnalyzeQueryTable key={dataset} dataset={dataset} />} />
  </UsageChartColorsProvider>;
}

// A record keyword clause is the table's host query state (record_<dataset>):
// saved, shared, reset and undone with the table query. Each request compiles
// its own state into record_search, which the service validates and runs.
function AnalyzeQueryTable({ dataset }: { dataset: UsageDataset }) {
  const fields = schemas[dataset].fields;
  const host = useMemo((): SurfaceHost => ({ query: dataset==="message_labels"?messageLabelHostQuery(fields):recordKeywordHostQuery(dataset, fields), find: state => recordKeywordFind(state, dataset, fields) }), [dataset]);
  return <>
    {/* Keep record predicates and grouped HAVING in the native builder,
        including datasets whose older pages use RecordSearchEditor. */}
    <QuerySurface dataset={dataset} usageActions host={host}
      resultPresentation={dataset==="message_labels"?api=><MessageLabelDisplay api={api}/>:undefined}
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

function formatDurationMS(value: unknown): string {
  const ms = Number(value);
  if (value === null || value === undefined || !Number.isFinite(ms)) return "—";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
  return `${Math.floor(ms / 60_000)}m ${Math.round((ms % 60_000) / 1000)}s`;
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
    void load.current();
    const timer = window.setInterval(() => { if (document.querySelector("#tools.active, .usage-tools-dashboard")) void load.current(); }, 3000);
    return () => window.clearInterval(timer);
  }, []);
  const running = status?.backfill.running ?? false;
  const wasRunning = useRef(false);
  useEffect(() => {
    if (wasRunning.current && !running) window.dispatchEvent(new Event("pharos:usage-refresh"));
    wasRunning.current = running;
  }, [running]);
  async function build() {
    try { await fetch("/api/tools/backfill", { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" }); await load.current(); }
    catch (failure) { setError(failure instanceof Error ? failure.message : "Could not start"); }
  }
  if (error) return <div className="query-table-error">{error}</div>;
  if (!status || (!running && status.pending_conversations === 0 && !status.backfill.error)) return null;
  const percent = status.backfill.total ? Math.round(status.backfill.done / status.backfill.total * 100) : 0;
  return <div className="tool-ledger-banner" role="status">
    {running ? <span>Building the tool ledger from retained transcripts: {status.backfill.done.toLocaleString()} of {status.backfill.total.toLocaleString()} conversations ({percent}%).</span>
      : <span>{status.pending_conversations.toLocaleString()} of {status.conversations.toLocaleString()} conversations need a tool ledger rebuild. Building it re-reads their retained messages; no source files are needed.{status.backfill.error ? ` Last attempt failed: ${status.backfill.error}` : ""}</span>}
    {running ? null : <button type="button" onClick={() => void build()}>Build tool ledger</button>}
  </div>;
}

installAppNavigation();

const dataGuideHost = document.createElement("div");
dataGuideHost.id = "dataGuideHost";
document.body.append(dataGuideHost);
createRoot(dataGuideHost).render(<DataTaxonomyModal />);

// Ask remains available before Home's first visit and while another page is active.
{
  const host = document.createElement("div");host.hidden = true;document.body.append(host);
  createRoot(host).render(<AskPharosSearch schemas={schemas} />);
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
  ["mcpSettingsPage", <MCPSettings />],
  ["logsPage", <LogsPage copy={copyText} />],
  ["fileHistoryPage", <FileHistoryPage />],
  ["findingsPage", <FindingsPage copy={copyText} table={<QuerySurface dataset="findings" />} safetyTable={<QuerySurface dataset="safety" queryHistory presets={api => <SafetyTableContext query={api.query} total={api.total} />} selectionBar={api => <SafetySelectionBar selection={api.selection} copy={copyText} />} trailing={(row, api) => <a href={safetyDetailURL(String(api.rowId(row)), new URL(location.href))} onClick={event => { if(event.metaKey || event.ctrlKey || event.shiftKey) return; event.preventDefault(); navigateLink(safetyDetailURL(String(api.rowId(row)), new URL(location.href))); }}>Open notice</a>} />} />],
];

// Mount a page on its first visit. Hidden pages used to load capabilities,
// definitions and dashboard data together with Usage, competing for the same
// browser connections and catalog readers. Keep visited pages mounted so their
// table restoration and undo history survive navigation.
function LazyPage({ host, children }: { host: HTMLElement; children: React.ReactNode }) {
  useLocationRevision();
  const visited = useRef(false);
  visited.current ||= Boolean(host.id === "searchComposerDialog" || host.closest(".view.active"));
  if (!visited.current) return null;
  const libraryPage = ["queryTableLibrary", "queryTableUsage", "findingsPage"].includes(host.id);
  const settings = host.id === "findingsPage" && new URLSearchParams(location.search).get("view") === "settings";
  // Keep one element type across routes: Findings Settings bypasses the empty
  // state without remounting the dashboard and its retained table.
  return <LibraryContent enabled={libraryPage && !settings}>{children}</LibraryContent>;
}

for (const [id, component] of mounts) {
  const element = document.getElementById(id);
  if (element) createRoot(element).render(<PageErrorBoundary><LazyPage host={element}>{component}</LazyPage></PageErrorBoundary>);
}
startFindingsChrome(); installWorkspaceInbox(); installWorkFiles(); installDelegationExplorer();
if (concurrencyPrototypeEnabled) {
  const modalHost = document.createElement("div");
  modalHost.id = "parallelismModal";document.body.append(modalHost);
  createRoot(modalHost).render(<ConcurrencyModal />);
}
startConcurrencySummary();
// Carbon and Parallelism settings shape Usage and parallelism values on the
// server. Refresh their cards once the saved settings have landed; query
// tables rerun only through their own Run Now and auto-refresh.
window.addEventListener("pharos:preferences-saved", event => {
  for (const dataset of datasetsForSavedPreferences((event as CustomEvent<{ set?: Record<string, unknown>; delete?: string[] }>).detail)) window.pharosQueryTables?.refreshDashboards(dataset as Dataset);
});
window.pharosQueryTables = {
  summaryURL: (metric, window, generatedAt, _concurrency, from) => usageSummaryURL(metric, window, generatedAt, readConcurrencySettings(), from),
  async createLink() {
    const entry = [...tableApis.entries()].find(([dataset]) => datasetIsVisible(dataset));
    if (!entry) return { url: location.href };
    const [dataset, api] = entry, query = api.query;
    const link = await api.links.create(query);
    return { url: link.url, query, dataset };
  },
  refreshDashboards(dataset) {
    window.dispatchEvent(new CustomEvent("pharos:data-refresh", { detail: dataset }));
    if (dataset === "usage") window.dispatchEvent(new Event("pharos:usage-refresh"));
  },
  filterRepository(repository) {
    filterLibrary([{ field: "repository_name", op: "=", value: repository }]);
  },
  filterModel(model) {
    filterLibrary([{ field: "models", op: "contains", value: model }]);
  },
};

window.dispatchEvent(new Event("pharos:search-ready"));

installViewAttention();
