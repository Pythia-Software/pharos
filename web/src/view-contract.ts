import { validateThinking,defaultThinking,thinkingViews,thinkingBreakdowns } from "./thinking-model";
import carbonFactors from "../../carbon/co2_factors.json";
import { defaultConcurrencySettings, concurrencyRangePresets } from "./concurrency-settings";
import { emptyTextSearch, newRule, ruleLabels, readTextSearch } from "./text-search-model";
import { safetyRanks } from "./safety-dashboard-model";
import { fixesKinds, fixesRanks } from "./fixes-model";
import { logSourceIds } from "./logs-model";
import { sessionSignals } from "./session-shape-model";
import {validTimelineVisibility} from "./timeline-visibility";
import { dataGuideSelectionKeys, dataSources } from "./data-taxonomy-model";
import { syncSchemas } from "./sync-table-model";
import { readerParams } from "./site-routes";
// One contract for host-owned view state. Query Table owns q_* and its
// composite controls; the site map exports this contract verbatim to agents.
export type ViewField = { param: string; pages: string[]; kind: "text" | "boolean" | "enum" | "json" | "strings"; description: string; values?: readonly string[]; default?: unknown; details?: Record<string,unknown>; captureEntries?: boolean };
const field = (param: string, pages: string[], kind: ViewField["kind"], description: string, values?: readonly string[], fallback?: unknown): ViewField => ({ param, pages, kind, description, ...(values ? { values } : {}), ...(fallback !== undefined ? { default: fallback } : {}) });
export const viewFields: Record<string, ViewField> = {
  readiness_open: field("readiness_open", ["*"], "boolean", "Show when the data was last checked and which updates are waiting.", undefined, false),
  readiness_scope: field("readiness_scope", ["*"], "text", "The selected result, or page for all results shown on this page.", undefined, "page"),
  readiness_preview: field("readiness_preview", ["*"], "enum", "An example update status for reviewing the prototype. Does not change your data or settings.", ["clear","minutes","recent","hours","orange","days","missing","running","paused","failed","unknown"], "clear"),

  label_results: field("label_results",["analyze/query/message_labels"],"enum","Native Query Table host presentation, saved with the query. Cards and rows share filters, grouping and pagination.",["cards","table"],"table"),
  timeline_visibility: field("timeline_visibility",["work"],"json","Timeline Key visibility by section: modes (message label IDs, including ? for uncategorized), events (friction, learning, second-look, choices), tools (action categories or error), origins (caller text source IDs). Optional sections selects whole layers by those same section IDs. Each optional selection is {values:string[],excluded:boolean}; allHidden:true with empty values and excluded:false hides the entire selection. absent or empty values shows everything in that section. Selections only hide visualization elements, preserving the clock and underlying records.",undefined,{}),
  timeline_columns: field("timeline_columns",["work"],"json","Timeline text column widths in CSS pixels: {execution,events,time:0}. Session and Events resize; the clock always fills the remaining space. Older fixed time widths are ignored. All rows share these widths.",undefined,{execution:175,events:165,time:0}),
  reader_view: field("reader_view",["work"],"enum","Single-agent conversation presentation. Conversation retains the standard transcript; Timeline shows recursive message-mode and tool-action ribbons.",["conversation","timeline"],"conversation"),
  reader_navigator: field("reader_navigator",["work"],"strings","Sticky session navigators: any combination of timeline, context and turns; [] hides all bars. The shared window follows the visible transcript and clicking reveals its source message.",undefined,["timeline","context","turns"]),
  thinking_filters: field("analyze_filters",["analyze/thinking-overview","analyze/thinking-detail"],"text","Additional native Model Requests population filters. The dashboard range and model controls intersect these filters; dates are replaced when Range changes."),
  thinking: field("thinking",["analyze/thinking-overview","analyze/thinking-detail","analyze/query/model_requests"],"json","Thinking dashboard applied filters and presentation. All controls share one range; dates are half-open ISO bounds."),
  disclosures: field("disclosures", ["*"], "json", "Expanded content and settings sections by stable section/record identity; values are booleans. Transient menus and editor drafts are excluded."),
  find: field("find", ["*"], "text", "Page Find term (Cmd/Ctrl+F). Searches the current page, including collapsed sections and unrendered conversation content; matches are highlighted and the first visible one is revealed. Present while Find is open."),
  find_case: field("find_case", ["*"], "boolean", "Page Find matches case. Default off."),
  find_regex: field("find_regex", ["*"], "boolean", "Page Find term is a JavaScript regular expression. Default off (literal)."),
  nav_collapsed: field("nav_collapsed", ["*"], "boolean", "Collapse the application sidebar."),
  nav_expanded: field("nav_expanded", ["*"], "json", "Expanded application and Data Guide menu branches by section and page ID."),
  guide_dataset: field("guide_dataset", ["*"], "enum", "Query source that opened the Data Guide; retained as link context.", Object.keys(dataSources)),
  history_collapsed: field("history_collapsed", ["*"], "strings", "Collapsed History groups by displayed calendar date."),
  history_search: field("history_search", ["*"], "text", "Applied History drawer text filter."),
  guide_open: field("guide_open", ["*"], "boolean", "Show the Data Guide."),
  guide_entity: field("guide_entity", ["*"], "enum", "Selected agent-history noun. Legacy glossary links open their related current topic.", dataGuideSelectionKeys),
  guide_example: field("guide_example", ["*"], "enum", "Selected example conversation.", ["implementation","review"]),
  overlay: field("overlay", ["*"], "enum", "Open read-only header content.", ["none","history","bookmarks","usage","drive"]),
  window_mode: field("window_mode", ["library","analyze/query/*","findings/table","findings/safety/table","findings/fixes/table"], "enum", "Native time intent companion for full queries.", ["relative","calendar","today"]),
  window_days: field("window_days", ["library","analyze/query/*","findings/table","findings/safety/table","findings/fixes/table"], "enum", "Native time-window size.", ["1","7","30","90","180","365"]),
  window_field: field("window_field", ["library","analyze/query/*","findings/table","findings/safety/table","findings/fixes/table"], "text", "Lower-bound timestamp field; preserve with the query."),
  window_until_field: field("window_until_field", ["library","analyze/query/*","findings/table","findings/safety/table","findings/fixes/table"], "text", "Upper-bound timestamp field for interval overlap."),
  window_dataset: field("window_dataset", ["library","analyze/query/*","findings/table","findings/safety/table","findings/fixes/table"], "text", "Dataset owning the time intent."),
  window_timezone: field("window_timezone", ["library","analyze/query/*","findings/table","findings/safety/table","findings/fixes/table"], "text", "Pinned IANA timezone for Today. Omit to follow the viewer."),
  query_dashboard_chart: field("analyze_chart", ["analyze/query/*"], "json", "Retained dashboard chart used when returning from its source query."),
  query_dashboard_filters: field("analyze_filters", ["analyze/query/*"], "text", "Retained dashboard population as a native query token."),
  query_dashboard_window: field("analyze_window", ["analyze/query/*"], "enum", "Retained moving dashboard time intent.", ["today"]),
  query_opened: field("analyze_opened", ["analyze/query/*"], "text", "Native query token recording the dashboard-to-query baseline; keeps return navigation from losing user filter/time edits."),
  dashboard_timezone: field("analyze_timezone", ["analyze/*"], "text", "Pinned IANA timezone for the dashboard Today range. Omit to follow the viewer."),
  query_days: field("analyze_days", ["analyze/query/*"], "enum", "Retained dashboard window, in elapsed days.", ["1","7","30","90","180","365"]),
  query_report: field("analyze_report", ["analyze/query/*"], "text", "Retained analysis report identity."),
  query_search: field("analyze_search", ["analyze/query/*"], "enum", "Retained search-entry route hint.", ["1"]),
  query_layout: field("query_layout", ["library","analyze/query/*","findings/table","findings/safety/table","findings/fixes/table","settings/sync"], "json", "Query builder collapsed state by dataset or background table schema ID."),
  reader_content: field("reader_content", ["work","library"], "json", "Expanded prose, turn summaries, file paths, action previews and deliberately loaded events by stable message/conversation identity."),
  search_cards: field("search_cards", ["library"], "json", "Conversation-result presentation keyed by workspace ID/browse, workspace ID/conversation, workspace ID/turn and workspace ID/message; browse is boolean, conversation is a stable conversation ID, turn is a stable message or outline entry ID, and message is the normalized message (native ID) a Search match opened within that turn."),
  chart_pins: field("chart_pins", ["analyze/*"], "json", "Pinned chart bucket keys by chart identity (dashboard title or Habits section and automation identity). Hover is transient."),
  conveyor_paused: field("conveyor_paused", ["home"], "boolean", "Pause the Home suggestion carousel."),
  delegation_page: field("delegation_page", ["work"], "text", "Zero-based session page number, 0–250 (site_link accepts a number or decimal string). Replays current cursors on open; does not store expiring server cursors."),
  card_window: field("card_window", ["cards"], "enum", "Preview lookback; moving windows remain relative. Numeric day values are also accepted by site_link.", ["default", "today", "1", "7", "30", "90"], "default"),
  card_filters: field("card_filters", ["cards"], "boolean", "Show the card filters.", undefined, false),
  findings_rank: field("findings_rank", ["findings", "findings/table"], "enum", "Linked rank override; restoring a URL does not save settings. Manual Rank by changes also save the library default.", ["", "usd", "tokens", "minutes", "failures"]),
  optimize_area: field("optimize_area", ["findings", "findings/*"], "enum", "Optimize section. Efficiency measures improvements; Risky actions raises notices for an agent to investigate; Fixes lists recurring agent problems a change to guidance, environment, tools or docs could prevent. Each has its own prompts.", ["efficiency", "safety", "fixes"], "efficiency"),
  fixes_list: field("fixes_list", ["findings/fixes"], "enum", "Fix lifecycle. Copying a prompt moves selected fixes to handed_off, and one that recurs afterwards opens again; dismissed is shown as Closed.", ["open", "handed_off", "dismissed"], "open"),
  fixes_place: field("fixes_place", ["findings/fixes"], "text", "Filter fixes by where their prompt goes: global (the shared setup) or a stable repository target.", undefined, ""),
  fixes_kind: field("fixes_kind", ["findings/fixes"], "enum", "Filter fixes by kind of fix; empty shows every kind.", ["", ...fixesKinds], ""),
  fixes_rank: field("fixes_rank", ["findings/fixes"], "enum", "Order fix cards by priority (work items weighted down once quiet past the settings' bound, plus agent time lost), work items, latest occurrence, time lost, or repositories spanned.", fixesRanks, "priority"),
  fixes_hidden: field("fixes_hidden", ["findings/fixes"], "boolean", "Also list open fixes the settings' bounds hide (quiet too long, or too few work items), marked as hidden. Default off.", undefined, false),
  fixes_item: field("fixes_item", ["findings/fixes", "findings/fixes/table"], "text", "Open one fix by stable ID: its quotes with reader links, where the fix belongs, guidance matches and status.", undefined, ""),
  safety_list: field("safety_list", ["findings/safety"], "enum", "Risky action notice lifecycle. Copying a prompt moves selected notices to handed_off; dismissed is shown as Closed.", ["open", "handed_off", "dismissed"], "open"),
  safety_place: field("safety_place", ["findings/safety"], "text", "Filter risky action notices by their stable repository, machine, or unresolved workspace target.", undefined, ""),
  safety_rank: field("safety_rank", ["findings/safety"], "enum", "Order the risky action check cards by danger, latest activity, call count, or notices with new execution evidence. Ties use danger, then latest activity.", safetyRanks, "danger"),
  safety_case: field("safety_case", ["findings/safety","findings/safety/table"], "text", "Open a risky action notice by stable ID; evidence and source links live inside Details.", undefined, ""),
  log_source: field("log_source", ["settings/logs"], "enum", "Settings → Logs source: overview (what to read first, and a report to copy for an agent), page (errors the page hit), requests (slow and failed requests, and those running now), service (this library's service output by run), app (the app's output by launch), background (failed or interrupted background work), crashes (macOS crash and resource reports).", logSourceIds, "overview"),
  delegation_columns: field("delegation_columns", ["work"], "strings", "Visible metric column IDs in display order."),
  delegation_scope: field("delegation_scope", ["work"], "enum", "Session metric scope in the hierarchy and ranking: self or self plus all children.", ["all", "self"], "all"),
  delegation_column_order: field("delegation_column_order", ["work"], "strings", "Metric column ordering, including hidden columns."),
  delegation_tint: field("delegation_tint", ["work"], "enum", "Tint session metric cells.", ["all", "sort", "off"]),
  delegation_expanded: field("delegation_expanded", ["work"], "strings", "Session IDs with detailed parallel lanes expanded."),
  delegation_relatives: field("delegation_relatives", ["work"], "strings", "Session rows whose relatives omitted at the explorer's family bound are loaded."),
  delegation_event: field("delegation_event", ["work"], "text", "Selected parallel-lane event ID."),
  timeline_idle: field("timeline_idle", ["work"], "boolean", "Skip idle gaps on the session timeline: stretches of 15 minutes or more with nothing running in any session (no message, model request or tool call in flight) that end with the caller speaking. Each skipped gap is a bar marked with the time the session resumed; the overview, zoom, axis and agent event arcs use the same compressed clock. Default on; a checkbox beside the timeline's zoom controls.", undefined, true),
  reader_filter_level: field("reader_filter_level", ["work"], "enum", "Content visibility portion of the conversation Key defaults, or a custom category selection. Choosing a whole-key default also sets reader_writing, reader_events, reader_labels and reader_highlight_hidden together.", ["all", "all-minus-usage", "thought", "primary", "custom"]),
  reader_filter: field("reader_filter", ["work"], "strings", "Visible transcript category IDs (reader-model TRANSCRIPT_CATEGORIES)."),
  reader_collapsed: field("reader_collapsed", ["work"], "strings", "Explicitly collapsed disclosure identities, overriding initially open content."),
  reader_expanded: field("reader_expanded", ["work"], "strings", "Expanded disclosure identities: message:<id>, call:<id>, or section:<key>."),
  reader_writing: field("reader_writing", ["work"], "boolean", "Highlight human-written text (typed or dictated, not pasted, quoted or templated) in the conversation reader. Default on; controlled by the reader's Highlight keys.", undefined, true),
  session_signal: field("session_signal", ["analyze/session_shape"], "enum", "Session Shape: the forensic signal the sessions table lists sessions by, most first. Omit to list sessions by all their forensic events.", ["all", ...sessionSignals], "all"),
  reader_events: field("reader_events", ["work"], "boolean", "Highlight agent events (Agent Experience, the sentence classifier) in the conversation reader: each event's sentence in its kind group's hue, what it was about and what the agent did underlined; hovering names the message's events. Default off; controlled by the reader's Highlight keys."),
  reader_labels: field("reader_labels", ["work"], "boolean", "Tint whole message cards by their local labels (Message labels, the message classifier), including reasoning, in the conversation reader; hovering a card names its labels. Default off; controlled by the reader's Highlight keys."),
  reader_highlight_hidden: field("reader_highlight_hidden", ["work"], "strings", "Hidden reader highlight keys: writing, events:<event group CSS class or event-target-legend>, labels:<label family ID>. Refines reader_writing, reader_events and reader_labels; category menus can hide every key. An empty list allows every hue in enabled kinds."),
  reader_event_link: field("reader_event_link", ["work"], "text", "Selected two-sided agent event in the conversation reader, by event ID (e<number>): the observation and the earlier text it points back at are highlighted, joined in the gutter, and the observation is revealed. Set from the fingerprint, the session timeline or a click on either end; shown with the Agent events highlight."),
  sidebar_visible: field("sidebar_visible", ["work"], "boolean", "Show the workspace navigation sidebar."),
  sidebar_width: field("sidebar_width", ["work"], "text", "Workspace navigation sidebar width, 300–5000 pixels (site_link accepts a number or decimal string)."),
  search_state: field("text_builder", ["library"], "json", "Complete applied TextSearchDraft, including nested groups, syntax/literal/regex, match options and scope. Browse library/view/search_state for the template. Missing search fields are filled from that template by site_link."),
  query_settings: field("analyze_query_settings", ["analyze/query/*"], "json", "Calculation settings captured by a dashboard query: carbon_settings, habits_settings or parallelism_settings, each a JSON string. The query source uses only its own settings."),
  carbon: field("analyze_carbon", ["analyze/carbon","analyze/query/*"], "json", "View-scoped Carbon assumptions; same shape as the Carbon controls."),
  parallelism: field("analyze_parallelism", ["analyze/parallelism","analyze/query/parallelism"], "json", "Ribbon mode, counting, gap, repository, range/custom bounds, selected workspace/agent, zoom and smoothingMinutes."),
  habits_view: field("habits_view", ["analyze/habits", "analyze/query/rhythm"], "enum", "The Habits chart to show. Its controls change with the view: activity calendar (calendar), weekday × hour punchcard (punchcard), or daily work schedule (days).", ["calendar", "punchcard", "days"], "days"),
  habits_streak: field("habits_streak", ["analyze/habits", "analyze/query/rhythm"], "enum", "Which days a Habits streak runs over: every day (all), or weekdays only (weekdays), where Saturdays and Sundays neither break nor extend it. Vacation days never do.", ["all", "weekdays"], "all"),
  habits_calendar: field("habits_calendar", ["analyze/habits", "analyze/query/rhythm"], "enum", "Whose days the Habits activity calendar shows: you (typed messages), agents you started (answering a message you sent), or automatic agent runs (started by a schedule, script, orchestrator or another agent).", ["you", "agents", "automated"], "you"),
  habits_weekends: field("habits_weekends", ["analyze/habits", "analyze/query/rhythm"], "enum", "Whether the Habits activity calendar shows Saturdays and Sundays (show) or weekdays only (hide).", ["show", "hide"], "show"),
  habits_weeks: field("habits_weeks", ["analyze/habits", "analyze/query/rhythm"], "enum", "Weeks the Habits activity calendar shows, ending with today's week.", ["13", "26", "53"], "53"),
  habits_punchcard: field("habits_punchcard", ["analyze/habits", "analyze/query/rhythm"], "enum", "Whose time the Habits weekday × hour punchcard shows: you, agents you started (with you or unattended), agents you started while you're away (unattended), automatic agent runs, or agents you started minus you.", ["you", "agents", "unattended", "automated", "difference"], "you"),
  habits_punchcard_days: field("habits_punchcard_days", ["analyze/habits", "analyze/query/rhythm"], "enum", "Days the Habits punchcard and automation table cover, through today, or all for every day captured.", ["7", "30", "90", "365", "all"], "90"),
  habits_shape_days: field("habits_shape_days", ["analyze/habits", "analyze/query/rhythm"], "enum", "Number of recent days shown in the Habits Your days chart.", ["7", "14", "28", "56", "90"], "28"),
  habits_day: field("habits_day", ["analyze/habits", "analyze/query/rhythm"], "text", "The calendar day selected on the Habits page (YYYY-MM-DD): its details, and the control that marks it as a vacation day."),
  file_history_path: field("file", ["files"], "text", "Files: the file whose workspaces and commits to show. A repository-relative path (src/main.go), a folder path (~/notes/plan.md) or an absolute path; when no path matches exactly, a path ending matches (without file_project, in every project)."),
  file_history_project: field("file_project", ["files"], "text", "Files: the project whose tree to show, and whose file file names: a list_projects name, or repository:<id> when two repositories share a name (get_file_history's files[].project_ref)."),
  file_tree_dir: field("file_dir", ["files"], "text", "Files: a directory of file_project (relative, no trailing slash) to show with its latest sessions and commits, when no file is set."),
  file_commit: field("file_commit", ["files"], "text", "Files: a commit of file_project's repository (a hash or its first 7+ characters) to show with its message, linked sessions and each file's diff; it shows instead of file or file_dir, and a file with it opens that file's diff."),
  file_view: field("file_view", ["files"], "enum", "Files: the tab a file opens on. code, blame and history are read by Git from the repository's chosen checkout; changes are its sessions' edits as diffs from their transcripts; sessions lists who edited it. A folder's file has only changes and sessions. Default: code for a repository file, changes otherwise.", ["code","blame","history","changes","sessions"]),
  file_dir_view: field("file_dir_view", ["files"], "enum", "Files: the tab a directory (or the project root) opens on: its files with their last change, its commits from the chosen checkout (repositories only), or the sessions that edited files in it. Default files.", ["files","commits","sessions"]),
  files_tree_collapsed: field("files_tree_collapsed", ["files"], "boolean", "Files: hide the project's file tree sidebar. Default off."),
  record_keyword: field("record_{dataset}", ["analyze/query/*"], "json", "Record keyword clause: {field:<text field ID>,draft:<TextSearchDraft>}.")
};
// These maps have viewer-dependent initial choices. Capture effective entries
// so an empty map cannot fall back to another browser's saved preference.
viewFields.query_layout.captureEntries=true;
for (const schema of Object.values(syncSchemas)) {
  viewFields[schema.name] = field(`q_${schema.name}`, ["settings/sync"], "text", `Native Query Table definition for ${schema.name}: filters, sorting, columns, grouping, metrics and pagination over the retained snapshot.`);
}

// Structured examples are exported with each field so an agent can construct
// the same object the UI owns, without guessing names from prose descriptions.
viewFields.thinking.details={template:defaultThinking,views:thinkingViews,breakdowns:thinkingBreakdowns,range:"all, today, 1d, 7d, 30d, 90d, 6m (180 days), 1y (365 days), or custom with from/to ISO timestamps. Dates and buckets always use the viewer’s local time zone. Legacy timezone and minimum fields are accepted for link compatibility and normalized to the viewer’s zone and zero. Range presets choose readable time buckets; manual buckets remain adjustable.",ordering:"Effort is fixed low-to-high with Unreported last; model+effort fixes model panels too."};
const searchTemplate = emptyTextSearch();
viewFields.disclosures.details={
  example:{quickStatsSettings:true},
  layout_sections:["menuBarPreferences","headerSettings","quickStatsSettings"],
  sections:{"sync:history":"settings/sync", "sync:sources":"settings/sync", "sync:repairs":"settings/sync", "sync:diagnostics":"settings/sync", "library:drive-health":"settings", "library:application":"settings/libraries", "machine:methodology":"/analyze?analyze_view=tokens&machine_view=time", "tokens.pricing-prompt":"analyze/tokens"},
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
viewFields.record_keyword.details={field:"Source text field ID from the dataset columns",draft:searchDetails,note:"Record keywords do not support fuzzy matching or message scope filters."};
viewFields.parallelism.details={
  template:defaultConcurrencySettings,
  values:{mode:["user","model"],counting:["workspace","agent"],gap:[0,15,30,60],range:["today","day",...concurrencyRangePresets.map(([key])=>key),"all","custom"]},
  dates:"day/startDay/endDay are YYYY-MM-DD; zoom is null or [startEpochMs,endEpochMs]. Relative range names resolve when opened.",
  identifiers:"repository is a repository name; workspace selects a workspace/checkout identity and agent selects a top-level conversation ID; empty means all.",
};
viewFields.carbon.details={
  template:{grid:"custom",customGrid:400,scenario:"central",pueScenario:"central",inputSensitivity:"current",vehicle:"car_miles",flightFrom:"jfk",flightTo:"lax"},
  values:{grid:[...carbonFactors.grids.map(grid=>grid.key),"custom"],scenario:["low","central","high"],pueScenario:["low","central","high"],inputSensitivity:["current","third"],vehicle:["car_miles","car_hybrid","car_electric","car_pickup"],flightFrom:["lax","sfo","den","jfk","dca","bos"],flightTo:["lax","sfo","den","jfk","dca","bos"]},
  grid:"Key from the shared carbon factors grids, or custom with nonnegative customGrid in g CO2e/kWh.",
};
export const attentionTargets = [
  { pattern: "fix:<id>", pages: ["findings/fixes", "findings/fixes/table"], description: "A fix's card on the dashboard, or its open detail, by stable ID. Reveal scrolls and highlights without focusing." },
  { pattern: "safety:<id>", pages: ["findings/safety"], description: "A risky action notice's open Details, by stable case ID. Reveal scrolls and highlights without focusing." },
  { pattern: "page", pages: ["*"], description: "Main page content." },
  { pattern: "control:<field>", pages: ["*"], description: "A registered view field or control wrapper." },
  { pattern: "control:chart:<label>", pages: ["analyze/*"], description: "Chart control identified by its lower-case accessible label, with spaces replaced by hyphens (metric, breakdown, scale)." },
  { pattern: "section:<id>", pages: ["*"], description: "A named section (its stable HTML ID)." },
  { pattern: "card:<id>", pages: ["home", "cards"], description: "Card definition ID from browse_site cards. Does not change the card filters." },
  { pattern: "message:<id>", pages: ["work"], description: "Message ID; materializes the message and opens its ancestors." },
  { pattern: "call:<id>", pages: ["work"], description: "Tool-call ID; reveals its owning message." },
  { pattern: "session:<id>", pages: ["work"], description: "Conversation/session ID in the delegation explorer." },
  { pattern: "row:<id>", pages: ["library", "analyze/query/*", "findings/table","findings/safety/table","findings/fixes/table"], description: "Stable record ID in the current query result. Does not broaden filters." },
  { pattern: "detail:<json>", pages: ["library", "analyze/query/*", "findings/table","findings/safety/table","findings/fixes/table"], revealOnly: true, description: 'Reveal an exact record/property/JSONPath via Query Table: detail:{"rowId":"id","field":"arguments_json","path":"$[\\"events\\"][21]"}. field/path are optional; preserve numeric versus string IDs. The page selects the dataset. Loads exact details without changing filters or pagination; disabled/absent properties are unavailable. This family supports reveal only.' },
  { pattern: "text:<literal>", pages: ["*"], description: "Literal text search; an alias for view.find. Opens page Find, highlights matches and reveals the first. Several text targets become one escaped OR regex. No browser focus movement." }
];
export const viewDependencyGaps = {
  query_table_json_cells: {status:"blocked",version:"1.0.0",owner:"@pythia-software/query-table-ui DataTable",reason:"Native property JSON is portable. JSON display cells created by DataTable still do not receive scoped controlled inspection state; requires a published upstream integration.",handoff:"docs/query-table-url-state-handoff.md"},
  query_table_attention: {status:"partial",version:"1.0.0",owner:"@pythia-software/query-table-core AttentionRegistry",reason:"Exact record/property/JSONPath reveal is integrated. The public API does not support highlight-only, multiple persistent semantic targets, or clearing/replacing prior property marks. detail: is reveal-only; existing host highlights remain supported.",handoff:"docs/query-table-url-state-handoff.md"},
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
    if(field.param==='reader_navigator'&&(!Array.isArray(value)||value.some(item=>!['timeline','context','turns'].includes(item))||new Set(value).size!==value.length))throw new Error('Unknown or repeated session navigator.');
    if (field.kind === "strings" ? !Array.isArray(value) || value.some(item => typeof item !== "string") : value === null || typeof value !== "object") throw new Error(`${field.param} has invalid state.`);
    if (["disclosures","reader_content","query_layout"].includes(field.param) && (Array.isArray(value)||Object.values(value).some(item=>typeof item!=="boolean")))throw new Error(`${field.param} needs booleans by stable identity.`);
    if (field.param==="chart_pins" && (Array.isArray(value)||Object.values(value).some(item=>typeof item!=="string")))throw new Error("Chart pins need bucket keys by chart identity.");
    if (field.param === "text_builder") {
      const restored=readTextSearch(new URLSearchParams({text_builder:raw}));if(restored.error||!restored.active)throw new Error(restored.error);
    }
    if (field.param.startsWith("record_")) {
      if(typeof value.field!=="string"||!value.field)throw new Error("Record keyword needs a field.");
      const restored=readTextSearch(new URLSearchParams({text_builder:JSON.stringify(value.draft)}));if(restored.error||!restored.active||restored.draft.fuzzy)throw new Error("Invalid record keyword search.");
      const scope=restored.draft.scope;
      if(Object.entries(emptyTextSearch().scope).some(([key,value])=>Array.isArray(value)?scope[key as keyof typeof scope].length!==0:scope[key as keyof typeof scope]!==value))throw new Error("Record keywords cannot carry message scope filters.");
    }
    if(field.param==="timeline_visibility"&&!validTimelineVisibility(value))throw new Error("Invalid timeline visibility state.");
    if(field.param==="thinking"&&!validateThinking(value))throw new Error("Invalid Thinking dashboard state.");
    return value;
  }
  if(field.param==="delegation_page" && (!/^\d+$/.test(raw)||Number(raw)>250))throw new Error("Choose a session page from 0 to 250.");
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
  label_results:{file:"web/src/message-label-results-model.ts",symbol:"messageLabelHostQuery"},
  dashboard_timezone:{file:"web/src/usage-dashboard-model.ts",symbol:"readDashboardLink"},
  query_settings:{file:"web/src/dashboard-query-settings.ts",symbol:"dashboardQuerySettings"},
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
for (const schema of Object.values(syncSchemas)) delegatedViewFields[schema.name] = {file:"web/src/sync-tables.tsx",symbol:"useQueryTable"};

for (const name of ["window_mode","window_days","window_field","window_until_field","window_dataset","window_timezone"]) delegatedViewFields[name]={file:"web/src/query-links.ts",symbol:"readRollingWindow"};
for (const name of ["query_dashboard_chart","query_dashboard_filters","query_dashboard_window","query_opened","query_days","query_report","query_search"]) delegatedViewFields[name]={file:"web/src/usage-workspace-model.ts",symbol:"usageWorkspaceURL"};

/** Attention and page Find describe one page's content; navigation drops them. */
export const pageAttentionParameters = ["highlight", "reveal", "find", "find_case", "find_regex"] as const;
/** Page navigation retains global view controls, but attention belongs to its source page. */
export function retainPageView(params: URLSearchParams, page: string, nativeParameters: readonly string[] = [], sourcePage?: string) {
  if(sourcePage!==page)for(const name of pageAttentionParameters)params.delete(name);
  const allowed=new Set([...Object.values(fieldsForPage(page)).map(field=>field.param),...nativeParameters]);
  for(const field of Object.values(viewFields))if(!field.param.includes("{dataset}")&&!allowed.has(field.param))params.delete(field.param);
}

// Native reader references are selections, not presentation preferences. A
// workspace opened from Search must not inherit another workspace's transcript,
// local event IDs, timeline window or page. Stable-identity maps can travel.
const workspaceSelections = [...readerParams.map(field => field.name), "delegation_session", "delegation_collapsed", "delegation_start", "delegation_end", "delegation_page", "delegation_expanded", "delegation_relatives", "delegation_event", "reader_event_link", "reader_expanded", "reader_collapsed"];
const workspacePresentation = ["explorer", "delegation_mode", "delegation_sort", "delegation_scope", "delegation_direction", "delegation_search", "delegation_matching"];
export function retainNavigationView(params: URLSearchParams, target: URL, source: URL, nativeParameters: readonly string[] = []) {
  const workspace = (url: URL) => { try { const id = url.pathname.match(/^\/work\/([^/]+)(?:\/|$)/)?.[1]; return id ? decodeURIComponent(id) : null; } catch { return null; } };
  const next = workspace(target);
  retainPageView(params, viewPageForURL(target), nativeParameters, viewPageForURL(source));
  if (!next || next !== workspace(source)) for (const name of [...workspaceSelections, ...pageAttentionParameters]) params.delete(name);
  if (!next) for (const name of workspacePresentation) params.delete(name);
}

/** Canonical page identity for route-scoped bindings retained in the shell. */
export function viewPageForURL(url: URL): string {
  if(url.pathname.startsWith("/work/"))return "work";
  if(url.pathname==="/analyze")return url.searchParams.get("analyze_mode")==="query"?`analyze/query/${url.searchParams.get("analyze_data")||"usage"}`:`analyze/${url.searchParams.get("analyze_topic")||"tokens"}`;
  if(url.pathname==="/findings"&&url.searchParams.get("optimize_area")==="fixes")return ["table","settings"].includes(url.searchParams.get("view")||"") ? `findings/fixes/${url.searchParams.get("view")}` : "findings/fixes";
  if(url.pathname==="/findings"&&url.searchParams.get("optimize_area")==="safety")return ["table","settings"].includes(url.searchParams.get("view")||"") ? `findings/safety/${url.searchParams.get("view")}` : "findings/safety";
  if(url.pathname==="/findings"&&["table","settings"].includes(url.searchParams.get("view")||""))return `findings/${url.searchParams.get("view")}`;
  return url.pathname.replace(/^\//,"")||"home";
}
export function viewFieldApplies(name: string, url: URL): boolean {
  const page=viewPageForURL(url);return viewFields[name]?.pages.some(pattern=>matchesViewPage(pattern,page))??false;
}
