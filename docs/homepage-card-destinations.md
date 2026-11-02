# Homepage card destinations

**Card → Full dashboard → Supporting query.** All 115 active analysis cards
open standard Analyze dashboards with their view, measure, breakdown, time
range and source predicates encoded in the URL. There is no intermediate
expanded-card page. The three Findings cards and TL1 revisits retain their
native workflows. All 52 retired definitions remain resolvable.

Home, the conveyor, the Card Library and agent-generated card links use the
same primary destinations. Dashboard controls are immediately available to
change the view, measures, grouping, range and filters. Dashboard drilldowns
open their contributing queries; returning restores the dashboard and
reconciles query edits.

Collected cards retain their exact source query and cover separately for
previews and editing. Their navigation URLs shed `card_*`, `q_*` and source
window companions, using `analyze_chart` and `analyze_filters` instead.
`analyze_window=today` preserves moving calendar intent and `analyze_timezone`
retains a pinned timezone. Elapsed datetime operands remain relative.
Existing collected cards migrate their dashboard mapping without replacing
saved queries or durable source snapshot references.

## Calculation differences

The dashboard is an exploratory view of the card's subject. Its chart can
present more groups or use a different reduction than the compact cover:

- Group eligibility remains in the saved source query. The dashboards expose
  ordinary sample thresholds; specialized HAVING rules such as at least one
  failure, two repositories per error/skill, repeated calls per path, or
  positive delegation per conversation can leave additional groups visible.
- Model conversation reach opens distinct conversation counts, rather than a
  share of group incidences. Largest subagent weight compares each model and
  effort's largest conversation, rather than showing one scalar maximum.
- Human phrase examples open the Human Words text-source composition. Concrete
  feedback opens correction-cue share; the compact study also requires a
  specific-detail cue. Specialized human averages and weekday studies can use
  different baselines and complete-period rules than the dashboard controls.
- Delegation depth opens token totals by depth; repeat-read comparisons open
  the maximum reads of a path in one conversation. These summarize the
  card's source instead of reproducing its exact observation distribution.
- Covers can fold series, hide legends, use custom metrics or show specialized
  historical norms. These remain cover preferences; dashboard controls and
  URL state own the destination presentation.

Tool Calls preserves per-call filters such as error signatures, file paths,
test failures, MCP evidence and exact start bounds. Conversation Input and
Output preserves conversation_activity records and token units. Neither needs
a lossy projection onto another dataset. Every active analysis destination
uses its source dataset.

## Every active card

| Card ID | Destination | View | Measure | Breakdown / observation |
| --- | --- | --- | --- | --- |
| `tool-failures` | `calls` | compare | `failures` | `tool_name` / records |
| `error-signatures` | `calls` | compare | `calls` | `error_signature` / records |
| `edit-failures` | `calls` | compare | `calls` | `repository_path` / records |
| `test-failures` | `calls` | compare | `calls` | `repository_command` / records |
| `large-outputs` | `time` | compare | `result_tokens` | `tool_source` / records |
| `truncated-outputs` | `calls` | compare | `truncation_rate` | `tool_name` / records |
| `read-paths` | `calls` | compare | `calls` | `repository_path` / records |
| `primary-domains` | `calls` | compare | `calls` | `host` / records |
| `mcp-methods` | `calls` | compare | `calls` | `mcp_server_method` / records |
| `skill-evidence` | `skills` | compare | `conversations` | `skill_name` / records |
| `human-words` | `human` | trend | `typed` | `none` / records |
| `explaining-repos` | `human` | compare | `typed` | `repository_name` / records |
| `usage-models` | `tokens` | trend | `tokens` | `model_family` / records |
| `usage-repos` | `tokens` | trend | `tokens` | `repository_name` / records |
| `cost-repos` | `tokens` | trend | `cost` | `repository_name` / records |
| `tool-failure-rates` | `calls` | compare | `failure_rate` | `tool_name` / records |
| `duration-distribution` | `time` | distribution | `time` | `command_source` / records |
| `output-distribution` | `time` | distribution | `result_tokens` | `tool_source` / records |
| `error-reach` | `calls` | compare | `repositories` | `error_signature` / records |
| `cross-repo-errors` | `calls` | compare | `calls` | `repository_error` / records |
| `skill-reach` | `skills` | compare | `repositories` | `skill_name` / records |
| `repeated-reads` | `calls` | compare | `calls` | `repository_path` / path_conversation |
| `human-word-mix` | `human` | compare | `typed_share` | `repository_name` / records |
| `usage-work` | `tokens` | distribution | `tokens` | `repository_name` / conversation |
| `cache-efficiency` | `tokens` | compare | `cache_read_share` | `provider_model` / records |
| `effort-cost` | `tokens` | compare | `cost` | `provider_model_effort` / conversation |
| `delegation-depth` | `tokens` | compare | `tokens` | `delegated_run_depth` / records |
| `price-coverage` | `tokens` | compare | `tokens` | `price_status` / records |
| `mcp-response-size` | `mcp` | distribution | `bytes` | `tool_name` / records |
| `human-example-phrases` | `human` | trend | `words` | `kind` / records |
| `tl1-revisits` | Native tl1_attempts workflow | Query | Saved query | Saved grouping |
| `primary-urls` | `calls` | compare | `conversations` | `url` / records |
| `writing-hours` | `human` | cycle | `typed` | `none` / records |
| `human-word-total` | `human` | trend | `typed` | `none` / records |
| `human-repository-words-trend` | `human` | trend | `typed` | `repository_name` / records |
| `human-words-per-message` | `human` | compare | `words_per_message` | `none` / records |
| `human-words-per-conversation` | `human` | compare | `typed` | `none` / conversation |
| `human-words-per-work` | `human` | compare | `typed` | `none` / work |
| `human-repo-conversation-depth` | `human` | compare | `typed` | `repository_name` / conversation |
| `human-message-lengths` | `human` | distribution | `typed` | `none` / conversation |
| `human-specific-feedback` | `human` | trend | `correction_share` | `none` / records |
| `human-messages-per-day` | `human` | trend | `typed_messages` | `none` / records |
| `human-conversations-per-day` | `human` | trend | `conversations` | `none` / records |
| `model-token-share-trend` | `tokens` | trend | `percent` | `model_family` / records |
| `model-cost-share-trend` | `tokens` | trend | `percent_cost` | `model_family` / records |
| `model-session-costs` | `tokens` | distribution | `cost` | `provider_model` / conversation |
| `largest-subagent-weight` | `tokens` | compare | `delegated_runs` | `provider_model_effort` / conversation |
| `session-human-output` | `activity` | scatter | `human_input` | `provider_model` / conversation |
| `session-delegation-count` | `tokens` | distribution | `delegated_runs` | `provider_model_effort` / conversation |
| `model-agent-roles` | `tokens` | compare | `agent_sessions` | `provider_model_role` / records |
| `effort-cost-share` | `tokens` | compare | `cost` | `effort` / records |
| `session-token-cost` | `tokens` | scatter | `tokens` | `provider_model` / conversation |
| `session-input-output` | `tokens` | scatter | `input` | `provider_model` / conversation |
| `model-token-share` | `tokens` | compare | `tokens` | `provider_model` / records |
| `model-conversation-reach` | `tokens` | compare | `conversations` | `provider_model` / records |
| `repository-model-use` | `tokens` | compare | `tokens` | `repository_provider_model` / records |
| `model-cost-share` | `tokens` | compare | `cost` | `provider_model` / records |
| `cache-repositories` | `tokens` | compare | `cache_read_share` | `repository_name` / records |
| `repository-failure-rates` | `calls` | compare | `failure_rate` | `repository_name` / records |
| `conversation-error-counts` | `time` | distribution | `calls` | `tool_name` / tool_conversation |
| `tool-call-share` | `calls` | compare | `calls` | `tool_name` / records |
| `tool-calls-per-conversation` | `time` | distribution | `calls` | `repository_name` / conversation |
| `pharos-mcp-sources` | `calls` | compare | `calls` | `app_provider` / records |
| `pharos-mcp-query-sources` | `mcp` | compare | `calls` | `query_source` / records |
| `pharos-mcp-durations` | `mcp` | distribution | `time` | `tool_name` / records |
| `skill-body-sizes` | `skills` | distribution | `body` | `skill_evidence` / records |
| `skill-load-distribution` | `skills` | distribution | `loads` | `skill_name` / skill_path |
| `research-conversation-searches` | `time` | distribution | `calls` | `repository_name` / conversation |
| `archive-empty-search-rate` | `mcp` | compare | `empty_rate` | `tool_name` / records |
| `provider-cost-share` | `tokens` | trend | `percent_cost` | `provider` / records |
| `api-cost-total` | `tokens` | compare | `cost` | `repository_name` / records |
| `repository-cost-share` | `tokens` | compare | `cost` | `repository_name` / records |
| `window-work-cost` | `tokens` | compare | `cost` | `repository_name` / conversation |
| `work-cost-distribution` | `tokens` | distribution | `cost` | `repository_name` / conversation |
| `work-volume-cost` | `tokens` | scatter | `root_conversations` | `root_repository_name` / repository |
| `origin-tokens-channel` | `tokens` | trend | `tokens` | `channel` / records |
| `origin-cost-channel` | `tokens` | trend | `cost` | `channel` / records |
| `origin-tokens-machine_id` | `tokens` | trend | `tokens` | `machine_id` / records |
| `origin-cost-machine_id` | `tokens` | trend | `cost` | `machine_id` / records |
| `origin-tokens-execution_machine_id` | `tokens` | trend | `tokens` | `execution_machine_id` / records |
| `origin-cost-execution_machine_id` | `tokens` | trend | `cost` | `execution_machine_id` / records |
| `run-effort-token-ranges` | `tokens` | distribution | `tokens` | `effort` / run_effort |
| `repository-token-share` | `tokens` | compare | `tokens` | `repository_name` / records |
| `task-token-ranges` | `tokens` | distribution | `tokens` | `repository_name` / conversation |
| `task-output-ranges` | `tokens` | distribution | `output` | `repository_name` / conversation |
| `repository-output-share` | `tokens` | compare | `output_share` | `repository_name` / records |
| `task-effort-token-ranges` | `tokens` | distribution | `tokens` | `effort` / conversation |
| `task-model-token-ranges` | `tokens` | distribution | `tokens` | `provider_model` / conversation |
| `task-model-effort-token-ranges` | `tokens` | distribution | `tokens` | `provider_model_effort` / conversation |
| `repository-delegated-share` | `tokens` | compare | `delegated_share` | `root_repository_name` / records |
| `delegating-task-share` | `tokens` | compare | `delegating_share` | `root_repository_name` / records |
| `delegated-run-token-ranges` | `tokens` | distribution | `tokens` | `repository_name` / delegated_run |
| `machine-time` | `time` | trend | `time` | `activity` / records |
| `machine-activities` | `time` | compare | `time` | `activity` / records |
| `machine-operations` | `time` | compare | `time` | `operation_name` / records |
| `machine-call-trend` | `calls` | trend | `calls` | `none` / records |
| `machine-latency-trend` | `calls` | trend | `mean_time` | `none` / records |
| `parallel-days` | `parallelism` | trend | `active_hours` | `level_band` / records |
| `parallel-trend` | `parallelism` | trend | `at_once` | `none` / records |
| `parallel-hours` | `parallelism` | cycle | `agent_hours` | `none` / records |
| `parallel-repositories` | `parallelism` | compare | `agent_hours` | `repository_name` / records |
| `carbon-trend` | `carbon` | trend | `co2` | `none` / records |
| `carbon-tiers` | `carbon` | compare | `co2` | `carbon_tier` / records |
| `carbon-repositories` | `carbon` | compare | `co2` | `repository_name` / records |
| `carbon-models` | `carbon` | compare | `co2` | `model_family` / records |
| `carbon-providers` | `carbon` | compare | `co2` | `provider` / records |
| `carbon-effort` | `carbon` | compare | `co2` | `effort` / records |
| `findings-open` | Native findings workflow | Query | Saved query | Saved grouping |
| `findings-fixes` | Native findings workflow | Query | Saved query | Saved grouping |
| `findings-results` | Native findings workflow | Query | Saved query | Saved grouping |
| `models-needing-prices` | `tokens` | compare | `tokens` | `model` / records |
| `token-volume-trend` | `tokens` | trend | `tokens` | `provider` / records |
| `token-volume-log` | `tokens` | trend | `tokens` | `repository_name` / records |
| `cumulative-api-cost` | `tokens` | trend | `cost` | `provider` / records |
| `mcp-call-trend` | `mcp` | trend | `calls` | `tool_name` / records |
| `human-words-weekday` | `human` | cycle | `typed` | `none` / records |
| `human-messages-weekday` | `human` | cycle | `typed_messages` | `none` / records |
| `parallel-active-weekday` | `parallelism` | cycle | `active_hours` | `none` / records |
| `parallel-weekday` | `parallelism` | cycle | `at_once` | `none` / records |
