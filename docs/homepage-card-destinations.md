# Card analysis destinations

All 119 active cards preserve their analysis on navigation. The 115 analysis
cards open an expanded chart with the same source query, preview executor and
visualization as Home; the three Findings cards and TL1 revisits retain their
native query routes. The 52 retired definitions remain resolvable.

## Navigation contract

**Card → matching expanded analysis → supporting query.** A related dashboard
is a separate, explicitly broader exploration; its projected filters and generic
chart defaults never replace the card's analysis.

- `card_query` and `q_*` identify the exact source query. Source WHERE, staged
  observation reductions, grouped HAVING, root lineage, distributions and
  paired axes remain intact. Durable snapshot tokens resolve before execution;
  unavailable queries show an error rather than a fresh template's population.
- `card_cover` retains the chosen metric, kind, series/sample limits, Other,
  legend, scale, smoothing and cumulative/percentage settings. Saved metric
  edits update this presentation. Existing collected covers migrate in place.
- `card_id` selects the original specialized study when applicable. Trailing
  totals, pooled ratios, complete-day endpoints, historical comparison spans,
  local-hour and weekday studies use the same loaders on both surfaces.
- Window edits change the exact source predicates while keeping the cover.
  The expanded parallelism charts use those source bounds, not the ribbon's
  separate calendar-day preset. Counting and gap preferences keep the same
  record execution path as Home.
- **View supporting query** opens the original Query Table with source and
  grouped filters intact. Its interval/source rows are the supporting evidence;
  the chart's smoothed or specialized values remain in the expanded analysis.
  Back to dashboard restores that analysis and cover.
- Generic Tools/MCP rolling rates and means pool numerator and denominator
  instead of averaging daily ratios. Writing series filter exact message
  timestamps before grouping into either days or hours.

## Every active card

The matching analysis uses the card's source dataset and saved calculation;
there is no categorical or date projection on its primary navigation path.

| Card ID | Primary analysis source | Entry point |
| --- | --- | --- |
| `tool-failures` | `tool_calls` | Expanded matching analysis |
| `error-signatures` | `tool_calls` | Expanded matching analysis |
| `edit-failures` | `tool_calls` | Expanded matching analysis |
| `test-failures` | `tool_calls` | Expanded matching analysis |
| `large-outputs` | `tool_calls` | Expanded matching analysis |
| `truncated-outputs` | `tool_calls` | Expanded matching analysis |
| `read-paths` | `tool_calls` | Expanded matching analysis |
| `primary-domains` | `tool_calls` | Expanded matching analysis |
| `mcp-methods` | `tool_calls` | Expanded matching analysis |
| `skill-evidence` | `skill_usages` | Expanded matching analysis |
| `human-words` | `writing_messages` | Expanded matching analysis |
| `explaining-repos` | `writing_messages` | Expanded matching analysis |
| `usage-models` | `usage` | Expanded matching analysis |
| `usage-repos` | `usage` | Expanded matching analysis |
| `cost-repos` | `usage` | Expanded matching analysis |
| `tool-failure-rates` | `tool_calls` | Expanded matching analysis |
| `duration-distribution` | `tool_calls` | Expanded matching analysis |
| `output-distribution` | `tool_calls` | Expanded matching analysis |
| `error-reach` | `tool_calls` | Expanded matching analysis |
| `cross-repo-errors` | `tool_calls` | Expanded matching analysis |
| `skill-reach` | `skill_usages` | Expanded matching analysis |
| `repeated-reads` | `tool_calls` | Expanded matching analysis |
| `human-word-mix` | `writing_messages` | Expanded matching analysis |
| `usage-work` | `usage` | Expanded matching analysis |
| `cache-efficiency` | `usage` | Expanded matching analysis |
| `effort-cost` | `usage` | Expanded matching analysis |
| `delegation-depth` | `usage` | Expanded matching analysis |
| `price-coverage` | `usage` | Expanded matching analysis |
| `mcp-response-size` | `mcp_calls` | Expanded matching analysis |
| `human-example-phrases` | `writing_messages` | Expanded matching analysis |
| `tl1-revisits` | `tl1_attempts` | Native query |
| `primary-urls` | `tool_calls` | Expanded matching analysis |
| `writing-hours` | `writing_messages` | Expanded matching analysis |
| `human-word-total` | `writing_messages` | Expanded matching analysis |
| `human-repository-words-trend` | `writing_messages` | Expanded matching analysis |
| `human-words-per-message` | `writing_messages` | Expanded matching analysis |
| `human-words-per-conversation` | `writing_messages` | Expanded matching analysis |
| `human-words-per-work` | `writing_messages` | Expanded matching analysis |
| `human-repo-conversation-depth` | `writing_messages` | Expanded matching analysis |
| `human-message-lengths` | `writing_messages` | Expanded matching analysis |
| `human-specific-feedback` | `writing_messages` | Expanded matching analysis |
| `human-messages-per-day` | `writing_messages` | Expanded matching analysis |
| `human-conversations-per-day` | `writing_messages` | Expanded matching analysis |
| `model-token-share-trend` | `usage` | Expanded matching analysis |
| `model-cost-share-trend` | `usage` | Expanded matching analysis |
| `model-session-costs` | `usage` | Expanded matching analysis |
| `largest-subagent-weight` | `usage` | Expanded matching analysis |
| `session-human-output` | `conversation_activity` | Expanded matching analysis |
| `session-delegation-count` | `usage` | Expanded matching analysis |
| `model-agent-roles` | `usage` | Expanded matching analysis |
| `effort-cost-share` | `usage` | Expanded matching analysis |
| `session-token-cost` | `usage` | Expanded matching analysis |
| `session-input-output` | `usage` | Expanded matching analysis |
| `model-token-share` | `usage` | Expanded matching analysis |
| `model-conversation-reach` | `usage` | Expanded matching analysis |
| `repository-model-use` | `usage` | Expanded matching analysis |
| `model-cost-share` | `usage` | Expanded matching analysis |
| `cache-repositories` | `usage` | Expanded matching analysis |
| `repository-failure-rates` | `tool_calls` | Expanded matching analysis |
| `conversation-error-counts` | `tool_calls` | Expanded matching analysis |
| `tool-call-share` | `tool_calls` | Expanded matching analysis |
| `tool-calls-per-conversation` | `tool_calls` | Expanded matching analysis |
| `pharos-mcp-sources` | `tool_calls` | Expanded matching analysis |
| `pharos-mcp-query-sources` | `mcp_calls` | Expanded matching analysis |
| `pharos-mcp-durations` | `mcp_calls` | Expanded matching analysis |
| `skill-body-sizes` | `skill_usages` | Expanded matching analysis |
| `skill-load-distribution` | `skill_usages` | Expanded matching analysis |
| `research-conversation-searches` | `tool_calls` | Expanded matching analysis |
| `archive-empty-search-rate` | `mcp_calls` | Expanded matching analysis |
| `provider-cost-share` | `usage` | Expanded matching analysis |
| `api-cost-total` | `usage` | Expanded matching analysis |
| `repository-cost-share` | `usage` | Expanded matching analysis |
| `window-work-cost` | `usage` | Expanded matching analysis |
| `work-cost-distribution` | `usage` | Expanded matching analysis |
| `work-volume-cost` | `usage` | Expanded matching analysis |
| `origin-tokens-channel` | `usage` | Expanded matching analysis |
| `origin-cost-channel` | `usage` | Expanded matching analysis |
| `origin-tokens-machine_id` | `usage` | Expanded matching analysis |
| `origin-cost-machine_id` | `usage` | Expanded matching analysis |
| `origin-tokens-execution_machine_id` | `usage` | Expanded matching analysis |
| `origin-cost-execution_machine_id` | `usage` | Expanded matching analysis |
| `run-effort-token-ranges` | `usage` | Expanded matching analysis |
| `repository-token-share` | `usage` | Expanded matching analysis |
| `task-token-ranges` | `usage` | Expanded matching analysis |
| `task-output-ranges` | `usage` | Expanded matching analysis |
| `repository-output-share` | `usage` | Expanded matching analysis |
| `task-effort-token-ranges` | `usage` | Expanded matching analysis |
| `task-model-token-ranges` | `usage` | Expanded matching analysis |
| `task-model-effort-token-ranges` | `usage` | Expanded matching analysis |
| `repository-delegated-share` | `usage` | Expanded matching analysis |
| `delegating-task-share` | `usage` | Expanded matching analysis |
| `delegated-run-token-ranges` | `usage` | Expanded matching analysis |
| `machine-time` | `tool_calls` | Expanded matching analysis |
| `machine-activities` | `tool_calls` | Expanded matching analysis |
| `machine-operations` | `tool_calls` | Expanded matching analysis |
| `machine-call-trend` | `tool_calls` | Expanded matching analysis |
| `machine-latency-trend` | `tool_calls` | Expanded matching analysis |
| `parallel-days` | `parallelism` | Expanded matching analysis |
| `parallel-trend` | `parallelism` | Expanded matching analysis |
| `parallel-hours` | `parallelism` | Expanded matching analysis |
| `parallel-repositories` | `parallelism` | Expanded matching analysis |
| `carbon-trend` | `usage` | Expanded matching analysis |
| `carbon-tiers` | `usage` | Expanded matching analysis |
| `carbon-repositories` | `usage` | Expanded matching analysis |
| `carbon-models` | `usage` | Expanded matching analysis |
| `carbon-providers` | `usage` | Expanded matching analysis |
| `carbon-effort` | `usage` | Expanded matching analysis |
| `findings-open` | `findings` | Native query |
| `findings-fixes` | `findings` | Native query |
| `findings-results` | `findings` | Native query |
| `models-needing-prices` | `usage` | Expanded matching analysis |
| `token-volume-trend` | `usage` | Expanded matching analysis |
| `token-volume-log` | `usage` | Expanded matching analysis |
| `cumulative-api-cost` | `usage` | Expanded matching analysis |
| `mcp-call-trend` | `mcp_calls` | Expanded matching analysis |
| `human-words-weekday` | `writing_messages` | Expanded matching analysis |
| `human-messages-weekday` | `writing_messages` | Expanded matching analysis |
| `parallel-active-weekday` | `parallelism` | Expanded matching analysis |
| `parallel-weekday` | `parallelism` | Expanded matching analysis |
