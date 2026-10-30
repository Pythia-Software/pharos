import carbonFactors from "../../carbon/co2_factors.json";
import { defaultConcurrencySettings, concurrencyRangePresets } from "./concurrency-settings";
import { emptyTextSearch, newRule, ruleLabels, readTextSearch } from "./text-search-model";
// One contract for host-owned view state. Query Table owns q_* and its
// composite controls; the site map exports this contract verbatim to agents.
export type ViewField = { param: string; pages: string[]; kind: "text" | "boolean" | "enum" | "json" | "strings"; description: string; values?: readonly string[]; default?: unknown; details?: Record<string,unknown>; captureEntries?: boolean };
const field = (param: string, pages: string[], kind: ViewField["kind"], description: string, values?: readonly string[], fallback?: unknown): ViewField => ({ param, pages, kind, description, ...(values ? { values } : {}), ...(fallback !== undefined ? { default: fallback } : {}) });
export const viewFields: Record<string, ViewField> = {
  disclosures: field("disclosures", ["*"], "json", "Expanded content and settings sections by stable section/record identity; values are booleans. Transient menus and editor drafts are excluded."),
  nav_collapsed: field("nav_collapsed", ["*"], "boolean", "Collapse the application sidebar."),
  nav_expanded: field("nav_expanded", ["*"], "json", "Expanded sidebar branches by section and page ID."),
  guide_dataset: field("guide_dataset", ["*"], "enum", "Data Guide source dataset.", ["library","usage","conversation_activity","writing","writing_messages","tool_calls","tools","skill_usages","mcp_calls","tl1_attempts","parallelism","findings"]),
  history_collapsed: field("history_collapsed", ["*"], "strings", "Collapsed History groups by displayed calendar date."),
  history_search: field("history_search", ["*"], "text", "Applied History drawer text filter."),
  guide_open: field("guide_open", ["*"], "boolean", "Show the Data Guide."),
  guide_entity: field("guide_entity", ["*"], "enum", "Selected Data Guide entity.", ["workspaces","conversations","agents","messages","tool_calls"]),
  guide_example: field("guide_example", ["*"], "enum", "Selected example conversation.", ["implementation","review"]),
  overlay: field("overlay", ["*"], "enum", "Open read-only header content.", ["none","history","bookmarks","usage","drive"]),
  window_mode: field("window_mode", ["library","analyze/query/*","findings/table"], "enum", "Native time intent companion for full queries.", ["relative","calendar","today"]),
  window_days: field("window_days", ["library","analyze/query/*","findings/table"], "enum", "Native time-window size.", ["1","7","30","90","180","365"]),
  window_field: field("window_field", ["library","analyze/query/*","findings/table"], "text", "Lower-bound timestamp field; preserve with the query."),
  window_until_field: field("window_until_field", ["library","analyze/query/*","findings/table"], "text", "Upper-bound timestamp field for interval overlap."),
  window_dataset: field("window_dataset", ["library","analyze/query/*","findings/table"], "text", "Dataset owning the time intent."),
  window_timezone: field("window_timezone", ["library","analyze/query/*","findings/table"], "text", "Pinned IANA timezone for Today. Omit to follow the viewer."),
  query_dashboard_chart: field("analyze_chart", ["analyze/query/*"], "json", "Retained dashboard chart used when returning from its source query."),
  query_dashboard_filters: field("analyze_filters", ["analyze/query/*"], "text", "Retained dashboard population as a native query token."),
  query_dashboard_window: field("analyze_window", ["analyze/query/*"], "enum", "Retained moving dashboard time intent.", ["today"]),
  query_opened: field("analyze_opened", ["analyze/query/*"], "text", "Native query token recording the dashboard-to-query baseline; keeps return navigation from losing user filter/time edits."),
  dashboard_timezone: field("analyze_timezone", ["analyze/*"], "text", "Pinned IANA timezone for the dashboard Today range. Omit to follow the viewer."),
  query_days: field("analyze_days", ["analyze/query/*"], "enum", "Retained dashboard window, in elapsed days.", ["1","7","30","90","180","365"]),
  query_report: field("analyze_report", ["analyze/query/*"], "text", "Retained analysis report identity."),
  query_search: field("analyze_search", ["analyze/query/*"], "enum", "Retained search-entry route hint.", ["1"]),
  query_layout: field("query_layout", ["library","analyze/query/*","findings/table"], "json", "Query builder collapsed state by dataset ID."),
  reader_content: field("reader_content", ["work","library"], "json", "Expanded prose, turn summaries, file paths, action previews and deliberately loaded events by stable message/conversation identity."),
  search_cards: field("search_cards", ["library"], "json", "Conversation-result presentation keyed by work ID/browse or work ID/turn; browse is boolean and turn is a stable input message ID."),
  chart_pins: field("chart_pins", ["analyze/*"], "json", "Pinned chart bucket keys by chart title. Hover is transient."),
  conveyor_paused: field("conveyor_paused", ["home"], "boolean", "Pause the Home suggestion carousel."),
  delegation_page: field("delegation_page", ["work"], "text", "Zero-based session page number, 0–250 (site_link accepts a number or decimal string). Replays current cursors on open; does not store expiring server cursors."),
  card_window: field("card_window", ["cards"], "enum", "Preview lookback; moving windows remain relative. Numeric day values are also accepted by site_link.", ["default", "today", "1", "7", "30", "90"], "default"),
  card_filters: field("card_filters", ["cards"], "boolean", "Show the card filters.", undefined, false),
  findings_rank: field("findings_rank", ["findings", "findings/table"], "enum", "Linked rank override; restoring a URL does not save settings. Manual Rank by changes also save the library default.", ["", "usd", "tokens", "minutes", "failures"]),
  delegation_columns: field("delegation_columns", ["work"], "strings", "Visible metric column IDs in display order."),
  delegation_column_order: field("delegation_column_order", ["work"], "strings", "Metric column ordering, including hidden columns."),
  delegation_tint: field("delegation_tint", ["work"], "enum", "Tint session metric cells.", ["all", "sort", "off"]),
  delegation_expanded: field("delegation_expanded", ["work"], "strings", "Session IDs with detailed parallel lanes expanded."),
  delegation_event: field("delegation_event", ["work"], "text", "Selected parallel-lane event ID."),
  reader_filter_level: field("reader_filter_level", ["work"], "enum", "Transcript Show preset or custom category selection.", ["all", "all-minus-usage", "thought", "primary", "custom"]),
  reader_filter: field("reader_filter", ["work"], "strings", "Visible transcript category IDs (reader-model TRANSCRIPT_CATEGORIES)."),
  reader_collapsed: field("reader_collapsed", ["work"], "strings", "Explicitly collapsed disclosure identities, overriding initially open content."),
  reader_expanded: field("reader_expanded", ["work"], "strings", "Expanded disclosure identities: message:<id>, call:<id>, or section:<key>."),
  reader_page: field("reader_page", ["work"], "text", "Zero-based conversation Find batch, 0–250 (site_link accepts a number or decimal string). Replays fresh cursors on restore; expired cursor blobs never travel in the URL."),
  reader_search: field("reader_search", ["work"], "json", "Applied conversation find definition: a TextSearchDraft; editor drafts are excluded."),
  reader_order: field("reader_order", ["work"], "enum", "Conversation search result order.", ["asc", "desc"]),
  sidebar_visible: field("sidebar_visible", ["work"], "boolean", "Show the workspace navigation sidebar."),
  sidebar_width: field("sidebar_width", ["work"], "text", "Workspace navigation sidebar width, 300–5000 pixels (site_link accepts a number or decimal string)."),
  search_state: field("text_builder", ["library"], "json", "Complete applied TextSearchDraft, including nested groups, syntax/literal/regex, match options and scope. Browse library/view/search_state for the template. Missing search fields are filled from that template by site_link."),
  carbon: field("analyze_carbon", ["analyze/carbon","analyze/query/*"], "json", "View-scoped Carbon assumptions; same shape as the Carbon controls."),
  parallelism: field("analyze_parallelism", ["analyze/parallelism","analyze/query/parallelism"], "json", "Ribbon mode, counting, gap, repository, range/custom bounds, selected workspace/agent, zoom and smoothingMinutes."),
  record_keyword: field("record_{dataset}", ["analyze/query/*"], "json", "Record keyword clause: {field:<text field ID>,draft:<TextSearchDraft>}.")
};
// These maps have viewer-dependent initial choices. Capture effective entries
// so an empty map cannot fall back to another browser's saved preference.
viewFields.query_layout.captureEntries=true;

// Structured examples are exported with each field so an agent can construct
// the same object the UI owns, without guessing names from prose descriptions.
const searchTemplate = emptyTextSearch();
viewFields.disclosures.details={
  example:{quickStatsSettings:true},
  layout_sections:["menuBarPreferences","headerSettings","quickStatsSettings"],
  sections:{"sync:separate":"settings/sync", "sync:repairs":"settings/sync", "sync:diagnostics":"settings/sync", "library:drive-health":"settings", "library:application":"settings/libraries", "machine:methodology":"/analyze?analyze_view=tokens&machine_view=time"},
  families:{"background:<item ID>":"settings/sync", "sync:forecast:<run ID>":"settings/sync", "cloud:schedule:<destination ID>":"settings/backups", "carbon:row:<tier>:<category>":"analyze/carbon", "status:cadence:<cadence ID>":"overlay=drive", "status:support":"overlay=drive", "library:activity:<activity ID>:<started_at>:details":"overlay=drive", "work:<work ID>:files":"work", "feedback:logs:<submission ID>":"feedback inbox"},
  defaults:"Fixed defaults remain implicit; data-dependent initial defaults are captured only in their active view. Explicit false closes a section even when its normal default is open.",
  note:"On settings/layout, true opens a section and false closes it. Combine with reveal=section:<identity> to scroll and highlight without moving focus. Settings values remain saved preferences.",
};
const searchDetails = {
  template: {...searchTemplate,root:{...searchTemplate.root,id:"group-1",children:[]}},
  rule_template: {...newRule(),id:"rule-1"}, rule_kinds:Object.keys(ruleLabels),
  editor:["visual","syntax"],match:["rules","literal","regex"],mode:["advanced","terms","phrase","literal","regex"],
  groups:"{id,kind:group,join:all|any,exclude:boolean,children:(rule|group)[]}; root is not excluded. At most 32 conditions, depth 4.",
  dates:"scope.from/to are ISO bounds or dates. For last N days retain dateWindow:{duration:7d,from:<same bound as scope.from>} and an empty scope.to. Today also retains to. Omit dateWindow for fixed custom dates.",
  literal_example:{...searchTemplate,root:{...searchTemplate.root,id:"group-1",children:[]},match:"literal",text:"failure"},
};
viewFields.search_state.details=searchDetails;
viewFields.reader_search.details={...searchDetails,note:"Scope is fixed to the current conversation/workspace; {} closes the applied Find."};
viewFields.record_keyword.details={field:"Source text field ID from the dataset columns",draft:searchDetails,note:"Record keywords do not support fuzzy matching or message scope filters."};
viewFields.parallelism.details={
  template:defaultConcurrencySettings,
  values:{mode:["user","model"],counting:["workspace","agent"],gap:[0,15,30,60],range:["today","day",...concurrencyRangePresets.map(([key])=>key),"all","custom"]},
  dates:"day/startDay/endDay are YYYY-MM-DD; zoom is null or [startEpochMs,endEpochMs]. Relative range names resolve when opened.",
  identifiers:"repository is a repository name; workspace and agent are stable session identities; empty means all.",
};
viewFields.carbon.details={
  template:{grid:"custom",customGrid:400,scenario:"central",pueScenario:"central",inputSensitivity:"current",vehicle:"car_miles",flightFrom:"jfk",flightTo:"lax"},
  values:{grid:[...carbonFactors.grids.map(grid=>grid.key),"custom"],scenario:["low","central","high"],pueScenario:["low","central","high"],inputSensitivity:["current","third"],vehicle:["car_miles","car_hybrid","car_electric","car_pickup"],flightFrom:["lax","sfo","den","jfk","dca","bos"],flightTo:["lax","sfo","den","jfk","dca","bos"]},
  grid:"Key from the shared carbon factors grids, or custom with nonnegative customGrid in g CO2e/kWh.",
};
export const attentionTargets = [
  { pattern: "page", pages: ["*"], description: "Main page content." },
  { pattern: "control:<field>", pages: ["*"], description: "A registered view field or control wrapper." },
  { pattern: "control:chart:<label>", pages: ["analyze/*"], description: "Chart control identified by its lower-case accessible label, with spaces replaced by hyphens (metric, breakdown, scale)." },
  { pattern: "section:<id>", pages: ["*"], description: "A named section (its stable HTML ID)." },
  { pattern: "card:<id>", pages: ["home", "cards"], description: "Card definition ID from browse_site cards. Does not change the card filters." },
  { pattern: "message:<id>", pages: ["work"], description: "Message ID; materializes the message and opens its ancestors." },
  { pattern: "call:<id>", pages: ["work"], description: "Tool-call ID; reveals its owning message." },
  { pattern: "session:<id>", pages: ["work"], description: "Conversation/session ID in the delegation explorer." },
  { pattern: "row:<id>", pages: ["library", "analyze/query/*", "findings/table"], description: "Stable record ID in the current query result. Does not broaden filters." },
  { pattern: "detail:<json>", pages: ["library", "analyze/query/*", "findings/table"], revealOnly: true, description: 'Reveal an exact record/property/JSONPath via Query Table: detail:{"rowId":"id","field":"arguments_json","path":"$[\\"events\\"][21]"}. field/path are optional; preserve numeric versus string IDs. The page selects the dataset. Loads exact details without changing filters or pagination; disabled/absent properties are unavailable. This family supports reveal only.' },
  { pattern: "text:<literal>", pages: ["*"], description: "Literal text search. Opens a prefilled Find box, highlights matches and reveals the first. No browser focus movement." }
];
export const viewDependencyGaps = {
  query_table_json_cells: {status:"blocked",version:"0.9.8",owner:"@pythia-software/query-table-ui DataTable",reason:"Native property JSON is portable. JSON display cells created by DataTable still do not receive scoped controlled inspection state; requires a published upstream integration.",handoff:"docs/query-table-url-state-handoff.md"},
  query_table_attention: {status:"partial",version:"0.9.8",owner:"@pythia-software/query-table-core AttentionRegistry",reason:"Exact record/property/JSONPath reveal is integrated. The public API does not support highlight-only, multiple persistent semantic targets, or clearing/replacing prior property marks. detail: is reveal-only; existing host highlights remain supported.",handoff:"docs/query-table-url-state-handoff.md"},
};
export const viewExclusions = {
  settings: "Settings values and their read-only diagnostics; section openness uses disclosures.",
  legacy: "Legacy Analyze/Tools controls are deferred; canonical pages cannot inherit this exclusion.",
  transient: "Hover, keyboard focus, menu openness, scroll offsets and automatic animation progress.",
  editing: "Unapplied drafts, editing and mutation/export/refresh actions. Applied searches are included."
};
export const viewContract = { version: 1, fields: viewFields, targets: attentionTargets, attention: { highlight: "Repeated target references", reveal: "One landing target; scroll and distinctive highlight, never browser focus" }, excluded: viewExclusions, dependencies:viewDependencyGaps };
export function matchesViewPage(pattern: string, page: string): boolean { return pattern === "*" || pattern === page || pattern.endsWith("/*") && page.startsWith(pattern.slice(0, -1)); }
export function fieldsForPage(page: string, dataset?: string): Record<string, ViewField> {
  return Object.fromEntries(Object.entries(viewFields).filter(([, value]) => value.pages.some(pattern => matchesViewPage(pattern, page))).map(([key, value]) => [key, { ...value, param: value.param.replace("{dataset}", dataset ?? "") }]));
}
export function parseViewValue(field: ViewField, raw: string): unknown {
  if (raw.length > 100000) throw new Error(`${field.param} is too large.`);
  if (field.kind === "boolean") { if (!["0", "1"].includes(raw)) throw new Error(`${field.param} needs 0 or 1.`); return raw === "1"; }
  if (field.kind === "enum") { if (!field.values?.includes(raw)) throw new Error(`${field.param} has an unknown value.`); return raw; }
  if (field.kind === "json" || field.kind === "strings") {
    const value = JSON.parse(raw);
    if (field.kind === "strings" ? !Array.isArray(value) || value.some(item => typeof item !== "string") : value === null || typeof value !== "object") throw new Error(`${field.param} has invalid state.`);
    if (["disclosures","reader_content","query_layout"].includes(field.param) && (Array.isArray(value)||Object.values(value).some(item=>typeof item!=="boolean")))throw new Error(`${field.param} needs booleans by stable identity.`);
    if (field.param==="chart_pins" && (Array.isArray(value)||Object.values(value).some(item=>typeof item!=="string")))throw new Error("Chart pins need bucket keys by chart identity.");
    if (["text_builder", "reader_search"].includes(field.param) && !(field.param === "reader_search" && !Object.keys(value).length)) {
      const restored=readTextSearch(new URLSearchParams({text_builder:raw}));if(restored.error||!restored.active)throw new Error(restored.error);
    }
    if (field.param.startsWith("record_")) {
      if(typeof value.field!=="string"||!value.field)throw new Error("Record keyword needs a field.");
      const restored=readTextSearch(new URLSearchParams({text_builder:JSON.stringify(value.draft)}));if(restored.error||!restored.active||restored.draft.fuzzy)throw new Error("Invalid record keyword search.");
      const scope=restored.draft.scope;
      if(Object.entries(emptyTextSearch().scope).some(([key,value])=>Array.isArray(value)?scope[key as keyof typeof scope].length!==0:scope[key as keyof typeof scope]!==value))throw new Error("Record keywords cannot carry message scope filters.");
    }
    return value;
  }
  if(["delegation_page","reader_page"].includes(field.param) && (!/^\d+$/.test(raw)||Number(raw)>250))throw new Error("Choose a session page from 0 to 250.");
  if(field.param==="window_timezone") {try {new Intl.DateTimeFormat("en",{timeZone:raw});}catch {throw new Error("Choose an IANA timezone.");}}
  if(field.param==="sidebar_width" && (!/^\d+$/.test(raw)||Number(raw)<300||Number(raw)>5000))throw new Error("Sidebar width must be 300 to 5000 pixels.");
  return raw;
}
export function encodeViewValue(field: ViewField, value: unknown): string {
  const raw = field.kind === "boolean" ? value === true ? "1" : value === false ? "0" : "invalid" : field.kind === "json" || field.kind === "strings" ? JSON.stringify(value) : String(value);
  parseViewValue(field, raw); return raw;
}

// Existing composite owners implement their published codecs directly. Every
// other host field must have a discovered useViewState/read/write binding.
export const delegatedViewFields: Record<string,{file:string; symbol:string}> = {
  dashboard_timezone:{file:"web/src/usage-dashboard-model.ts",symbol:"readDashboardLink"},
  carbon:{file:"internal/archive/assets/carbon.js",symbol:"syncSettingsURL"},
  parallelism:{file:"web/src/concurrency.tsx",symbol:"routeParameter"},
  record_keyword:{file:"web/src/host-query-state.ts",symbol:"recordKeywordHostQuery"},
  search_state:{file:"web/src/host-query-state.ts",symbol:"libraryHostQuery"},
  reader_filter:{file:"internal/archive/assets/ui.py",symbol:"restoreTranscriptView"},
  reader_filter_level:{file:"internal/archive/assets/ui.py",symbol:"restoreTranscriptView"},
  reader_expanded:{file:"internal/archive/assets/ui.py",symbol:"bindReaderView"},
  reader_collapsed:{file:"internal/archive/assets/ui.py",symbol:"bindReaderView"},
  reader_content:{file:"internal/archive/assets/ui.py",symbol:"viewContentKey"},
  history_collapsed:{file:"internal/archive/assets/ui.py",symbol:"renderHistory"},
  history_search:{file:"internal/archive/assets/ui.py",symbol:"restoreViewOverlay"},
  overlay:{file:"internal/archive/assets/ui.py",symbol:"restoreViewOverlay"},
};

for (const name of ["window_mode","window_days","window_field","window_until_field","window_dataset","window_timezone"]) delegatedViewFields[name]={file:"web/src/query-links.ts",symbol:"readRollingWindow"};
for (const name of ["query_dashboard_chart","query_dashboard_filters","query_dashboard_window","query_opened","query_days","query_report","query_search"]) delegatedViewFields[name]={file:"web/src/usage-workspace-model.ts",symbol:"usageWorkspaceURL"};

/** Page navigation retains global view controls, but attention belongs to its source page. */
export function retainPageView(params: URLSearchParams, page: string, nativeParameters: readonly string[] = [], sourcePage?: string) {
  if(sourcePage!==page){params.delete("highlight");params.delete("reveal");}
  const allowed=new Set([...Object.values(fieldsForPage(page)).map(field=>field.param),...nativeParameters]);
  for(const field of Object.values(viewFields))if(!field.param.includes("{dataset}")&&!allowed.has(field.param))params.delete(field.param);
}

/** Canonical page identity for route-scoped bindings retained in the shell. */
export function viewPageForURL(url: URL): string {
  if(url.pathname.startsWith("/work/"))return "work";
  if(url.pathname==="/analyze")return url.searchParams.get("analyze_mode")==="query"?`analyze/query/${url.searchParams.get("analyze_data")||"usage"}`:`analyze/${url.searchParams.get("analyze_topic")||"tokens"}`;
  if(url.pathname==="/findings"&&["table","settings"].includes(url.searchParams.get("view")||""))return `findings/${url.searchParams.get("view")}`;
  return url.pathname.replace(/^\//,"")||"home";
}
export function viewFieldApplies(name: string, url: URL): boolean {
  const page=viewPageForURL(url);return viewFields[name]?.pages.some(pattern=>matchesViewPage(pattern,page))??false;
}
