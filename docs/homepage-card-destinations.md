# Home card exploration audit

October 2026: all 104 analysis cards now enter through an Analyze dashboard. The remaining four cards open Findings or TL1, where their dedicated workflows live. The catalog has 108 active definitions; 52 retired definitions remain resolvable.

## Exploration flow

**Card → relevant dashboard → details, records or searches.** The card is a starting point for exploration. Its dashboard gives context and useful controls; it does not need to reproduce the card’s exact grouped query. A visible “Your starting point” section names the card and provides **View card details**.

- Every analysis recipe gets a dashboard destination, including distributions, thresholded rates, lineage comparisons and authorship studies. Editing a cover keeps that entry point.
- Encode useful initial measures, breakdowns, smoothing, scale and display. Topic defaults cover future cards; individual recipes choose more specific defaults where useful.
- Keep the original query, including HAVING, raw observations, lineage and source predicates, in `q_*`. `card_query` identifies its source dataset and `card_id` identifies the starting question. Details open that query; Back to dashboard returns to the overview with its filters and settings.
- Across different grains, project only dates and compatible categorical cohorts. Human message dates select overlapping conversations for Human Words. Per-call dates select Tools summary days. Message contents, repository paths and per-call numeric predicates remain in the exact details. Mixed OR filters are carried whole or omitted whole.
- The dashboard’s own grouped-query, record and search links follow the current chart selection. They do not get overwritten by the original card query.
- Durable collection retains the complete exploration URL and the immutable source query. Window edits update both the overview context and source query. Specialized saved covers still preview the original calculation.
- Charts resolve native relative dates for range controls and smoothing history. Smoothed, logarithmic and cumulative covers keep their presentation options. Parallel activity supports every collected-card window through 365 days. Cards inherit saved counting and turn-gap preferences so chart drilldowns match the hourly record dataset.

## New questions

- **Smoothed token volume:** provider workloads with a seven-day rolling mean.
- **Token volume across scales:** repository workloads on a logarithmic scale.
- **Accumulating API equivalent:** fully priced provider API cost accumulated from the range start.
- **Pharos request activity:** smoothed server requests by tool, with the retained-log coverage caveat.

## Every active card

| Card ID | Entry point | Exploration view | Details retain |
| --- | --- | --- | --- |
| `tool-failures` | Analyze → tools dashboard | failures by `tool_name`; group comparison. | Original grouping, measures, filters and supporting records. |
| `error-signatures` | Analyze → tools dashboard | failures by `tool_name`; group comparison. | Eligibility thresholds, denominators and supporting evidence. |
| `edit-failures` | Analyze → tools dashboard | failures by `repository_name`; group comparison. | Original grouping, measures, filters and supporting records. |
| `test-failures` | Analyze → tools dashboard | failures by `command_name`; group comparison. | Original grouping, measures, filters and supporting records. |
| `large-outputs` | Analyze → tools dashboard | context added by `tool_name`; group comparison. | Original grouping, measures, filters and supporting records. |
| `truncated-outputs` | Analyze → tools dashboard | calls by `tool_name`; group comparison. | Eligibility thresholds, denominators and supporting evidence. |
| `read-paths` | Analyze → tools dashboard | calls by `tool_name`; group comparison. | Original grouping, measures, filters and supporting records. |
| `primary-domains` | Analyze → tools dashboard | calls by `tool_name`; group comparison. | Original grouping, measures, filters and supporting records. |
| `mcp-methods` | Analyze → tools dashboard | calls by `mcp_server`; group comparison. | Original grouping, measures, filters and supporting records. |
| `skill-evidence` | Analyze → skills dashboard | evidence records by `skill_name`; daily overview. | Evidence types, conversation reach and repeated-load cohorts. |
| `human-words` | Analyze → human dashboard | typed by `kind`; daily overview. | Message sent time and authorship filters. |
| `explaining-repos` | Analyze → human dashboard | typed by `repository_name`; daily overview. | Message sent time and authorship filters. |
| `usage-models` | Analyze → tokens dashboard | tokens by `model_family`; daily overview. | Original grouping, measures, filters and supporting records. |
| `usage-repos` | Analyze → tokens dashboard | tokens by `repository_name`; daily overview. | Original grouping, measures, filters and supporting records. |
| `cost-repos` | Analyze → tokens dashboard | cost by `repository_name`; daily overview. | Original grouping, measures, filters and supporting records. |
| `tool-failure-rates` | Analyze → tools dashboard | failure rate by `tool_name`; group comparison. | Eligibility thresholds, denominators and supporting evidence. |
| `duration-distribution` | Analyze → tools dashboard | mean time by `command_name`; group comparison. | Observation distributions, sample thresholds and individual records. |
| `output-distribution` | Analyze → tools dashboard | context added by `tool_name`; group comparison. | Observation distributions, sample thresholds and individual records. |
| `error-reach` | Analyze → tools dashboard | failures by `tool_name`; group comparison. | Eligibility thresholds, denominators and supporting evidence. |
| `cross-repo-errors` | Analyze → tools dashboard | failures by `repository_name`; group comparison. | Original grouping, measures, filters and supporting records. |
| `skill-reach` | Analyze → skills dashboard | evidence records by `skill_name`; daily overview. | Eligibility thresholds, denominators and supporting evidence. |
| `repeated-reads` | Analyze → tools dashboard | calls by `tool_name`; group comparison. | Eligibility thresholds, denominators and supporting evidence. |
| `human-word-mix` | Analyze → human dashboard | typed by `kind`; daily overview. | Eligibility thresholds, denominators and supporting evidence. |
| `usage-work` | Analyze → tokens dashboard | tokens by `repository_name`; daily overview. | Per-task/run lineage, cohort identity and totals. |
| `cache-efficiency` | Analyze → tokens dashboard | tokens by `type`; daily overview. | Original grouping, measures, filters and supporting records. |
| `effort-cost` | Analyze → tokens dashboard | cost by `model_effort`; daily overview. | Per-task/run lineage, cohort identity and totals. |
| `delegation-depth` | Analyze → tokens dashboard | tokens by `session_kind`; daily overview. | Per-task/run lineage, cohort identity and totals. |
| `price-coverage` | Analyze → tokens dashboard | tokens by `provider`; daily overview. | Original grouping, measures, filters and supporting records. |
| `mcp-response-size` | Analyze → mcp dashboard | bytes by `tool_name`; group comparison. | Observation distributions, sample thresholds and individual records. |
| `human-example-phrases` | Analyze → human dashboard | all by `kind`; daily overview. | Message authorship, source text and the study calculation. |
| `tl1-revisits` | TL1 runs | Dedicated workflow. | Project and candidate identity. |
| `primary-urls` | Analyze → tools dashboard | calls by `tool_name`; group comparison. | Eligibility thresholds, denominators and supporting evidence. |
| `writing-hours` | Analyze → human dashboard | typed by `kind`; daily overview. | Message authorship, source text and the study calculation. |
| `human-word-total` | Analyze → human dashboard | typed by `kind`; daily overview. | Message authorship, source text and the study calculation. |
| `human-words-per-message` | Analyze → human dashboard | typed by `kind`; daily overview. | Message authorship, source text and the study calculation. |
| `human-words-per-conversation` | Analyze → human dashboard | typed by `kind`; daily overview. | Message authorship, source text and the study calculation. |
| `human-words-per-work` | Analyze → human dashboard | typed by `kind`; daily overview. | Message authorship, source text and the study calculation. |
| `human-repo-conversation-depth` | Analyze → human dashboard | typed by `repository_name`; daily overview. | Message sent time and authorship filters. |
| `human-message-lengths` | Analyze → human dashboard | typed by `kind`; daily overview. | Message authorship, source text and the study calculation. |
| `human-specific-feedback` | Analyze → human dashboard | typed by `kind`; daily overview. | Message authorship, source text and the study calculation. |
| `human-messages-per-day` | Analyze → human dashboard | typed by `kind`; daily overview. | Trailing-window calculation and its denominators. |
| `human-conversations-per-day` | Analyze → human dashboard | typed by `kind`; daily overview. | Trailing-window calculation and its denominators. |
| `model-token-share-trend` | Analyze → tokens dashboard | percent by `model_family`; 7-day smoothing. | Original grouping, measures, filters and supporting records. |
| `model-cost-share-trend` | Analyze → tokens dashboard | percent cost by `model_family`; 7-day smoothing. | Original grouping, measures, filters and supporting records. |
| `model-session-costs` | Analyze → tokens dashboard | cost by `model_family`; daily overview. | Observation distributions, sample thresholds and individual records. |
| `largest-subagent-weight` | Analyze → tokens dashboard | tokens by `model_family`; daily overview. | Per-task/run lineage, cohort identity and totals. |
| `session-human-output` | Analyze → human dashboard | all by `providers`; daily overview. | Paired measurements, cohort identity and logarithmic axes. |
| `session-delegation-count` | Analyze → tokens dashboard | tokens by `model_family`; daily overview. | Observation distributions, sample thresholds and individual records. |
| `model-agent-roles` | Analyze → tokens dashboard | tokens by `model_family`; daily overview. | Original grouping, measures, filters and supporting records. |
| `effort-cost-share` | Analyze → tokens dashboard | cost by `effort`; daily overview. | Original grouping, measures, filters and supporting records. |
| `session-token-cost` | Analyze → tokens dashboard | cost by `model_family`; daily overview. | Paired measurements, cohort identity and logarithmic axes. |
| `session-input-output` | Analyze → tokens dashboard | tokens by `model_family`; daily overview. | Paired measurements, cohort identity and logarithmic axes. |
| `model-token-share` | Analyze → tokens dashboard | percent by `model_family`; daily overview. | Original grouping, measures, filters and supporting records. |
| `model-conversation-reach` | Analyze → tokens dashboard | tokens by `model_family`; daily overview. | Original grouping, measures, filters and supporting records. |
| `repository-model-use` | Analyze → tokens dashboard | tokens by `model_family`; daily overview. | Original grouping, measures, filters and supporting records. |
| `model-cost-share` | Analyze → tokens dashboard | percent cost by `model_family`; daily overview. | Original grouping, measures, filters and supporting records. |
| `cache-repositories` | Analyze → tokens dashboard | tokens by `type`; daily overview. | Original grouping, measures, filters and supporting records. |
| `repository-failure-rates` | Analyze → tools dashboard | failure rate by `repository_name`; group comparison. | Eligibility thresholds, denominators and supporting evidence. |
| `conversation-error-counts` | Analyze → tools dashboard | failures by `tool_name`; group comparison. | Observation distributions, sample thresholds and individual records. |
| `tool-call-share` | Analyze → tools dashboard | calls by `tool_name`; group comparison. | Original grouping, measures, filters and supporting records. |
| `tool-calls-per-conversation` | Analyze → tools dashboard | calls by `tool_name`; group comparison. | Observation distributions, sample thresholds and individual records. |
| `pharos-mcp-sources` | Analyze → tools dashboard | calls by `source_kind`; group comparison. | Original grouping, measures, filters and supporting records. |
| `pharos-mcp-query-sources` | Analyze → mcp dashboard | calls by `query_source`; group comparison. | Original grouping, measures, filters and supporting records. |
| `pharos-mcp-durations` | Analyze → mcp dashboard | mean time by `tool_name`; group comparison. | Observation distributions, sample thresholds and individual records. |
| `skill-body-sizes` | Analyze → skills dashboard | retained body volume by `skill_name`; daily overview. | Observation distributions, sample thresholds and individual records. |
| `skill-load-distribution` | Analyze → skills dashboard | evidence records by `skill_name`; daily overview. | Observation distributions, sample thresholds and individual records. |
| `research-conversation-searches` | Analyze → tools dashboard | calls by `tool_name`; group comparison. | Observation distributions, sample thresholds and individual records. |
| `archive-empty-search-rate` | Analyze → mcp dashboard | calls by `status`; group comparison. | Eligibility thresholds, denominators and supporting evidence. |
| `provider-cost-share` | Analyze → tokens dashboard | percent cost by `provider`; 7-day smoothing. | Original grouping, measures, filters and supporting records. |
| `api-cost-total` | Analyze → tokens dashboard | cost by `repository_name`; daily overview. | Original grouping, measures, filters and supporting records. |
| `repository-cost-share` | Analyze → tokens dashboard | percent cost by `repository_name`; daily overview. | Original grouping, measures, filters and supporting records. |
| `window-work-cost` | Analyze → tokens dashboard | cost by `repository_name`; daily overview. | Per-task/run lineage, cohort identity and totals. |
| `work-cost-distribution` | Analyze → tokens dashboard | cost by `repository_name`; daily overview. | Observation distributions, sample thresholds and individual records. |
| `work-volume-cost` | Analyze → tokens dashboard | cost by `repository_name`; daily overview. | Paired measurements, cohort identity and logarithmic axes. |
| `run-effort-token-ranges` | Analyze → tokens dashboard | tokens by `effort`; daily overview. | Observation distributions, sample thresholds and individual records. |
| `repository-token-share` | Analyze → tokens dashboard | percent by `repository_name`; daily overview. | Original grouping, measures, filters and supporting records. |
| `task-token-ranges` | Analyze → tokens dashboard | tokens by `repository_name`; daily overview. | Observation distributions, sample thresholds and individual records. |
| `task-output-ranges` | Analyze → tokens dashboard | tokens by `repository_name`; daily overview. | Observation distributions, sample thresholds and individual records. |
| `repository-output-share` | Analyze → tokens dashboard | tokens by `type`; daily overview. | Original grouping, measures, filters and supporting records. |
| `task-effort-token-ranges` | Analyze → tokens dashboard | tokens by `effort`; daily overview. | Observation distributions, sample thresholds and individual records. |
| `task-model-token-ranges` | Analyze → tokens dashboard | tokens by `repository_name`; daily overview. | Observation distributions, sample thresholds and individual records. |
| `task-model-effort-token-ranges` | Analyze → tokens dashboard | tokens by `model_effort`; daily overview. | Observation distributions, sample thresholds and individual records. |
| `repository-delegated-share` | Analyze → tokens dashboard | tokens by `session_kind`; daily overview. | Per-task/run lineage, cohort identity and totals. |
| `delegating-task-share` | Analyze → tokens dashboard | tokens by `session_kind`; daily overview. | Per-task/run lineage, cohort identity and totals. |
| `delegated-run-token-ranges` | Analyze → tokens dashboard | tokens by `session_kind`; daily overview. | Observation distributions, sample thresholds and individual records. |
| `machine-time` | Analyze → time dashboard | recorded time by `activity`; daily overview. | Original grouping, measures, filters and supporting records. |
| `machine-activities` | Analyze → time dashboard | recorded time by `activity`; daily overview. | Original grouping, measures, filters and supporting records. |
| `machine-operations` | Analyze → time dashboard | recorded time by `activity`; daily overview. | Original grouping, measures, filters and supporting records. |
| `machine-call-trend` | Analyze → tools dashboard | calls by `tool_category`; 7-day smoothing. | Trailing-window calculation and its denominators. |
| `machine-latency-trend` | Analyze → tools dashboard | mean time by `tool_category`; 7-day smoothing. | Trailing-window calculation and its denominators. |
| `parallel-days` | Analyze → parallelism dashboard | Captured activity; saved counting and turn gap, explicit lookback. | Hourly occupancy, saved counting and gap assumptions. |
| `parallel-trend` | Analyze → parallelism dashboard | Captured activity; saved counting and turn gap, explicit lookback. | Hourly occupancy, saved counting and gap assumptions. |
| `parallel-hours` | Analyze → parallelism dashboard | Captured activity; saved counting and turn gap, explicit lookback. | Hourly occupancy, saved counting and gap assumptions. |
| `parallel-repositories` | Analyze → parallelism dashboard | Captured activity; saved counting and turn gap, explicit lookback. | Hourly occupancy, saved counting and gap assumptions. |
| `carbon-trend` | Analyze → carbon dashboard | co2 by `type`; daily overview. | Recorded ledger estimates; the dashboard explores current Carbon assumptions. |
| `carbon-tiers` | Analyze → carbon dashboard | co2 by `tier`; daily overview. | Recorded ledger estimates; the dashboard explores current Carbon assumptions. |
| `carbon-repositories` | Analyze → carbon dashboard | co2 by `repository_name`; daily overview. | Recorded ledger estimates; the dashboard explores current Carbon assumptions. |
| `carbon-models` | Analyze → carbon dashboard | co2 by `type`; daily overview. | Recorded ledger estimates; the dashboard explores current Carbon assumptions. |
| `carbon-providers` | Analyze → carbon dashboard | co2 by `provider`; daily overview. | Recorded ledger estimates; the dashboard explores current Carbon assumptions. |
| `carbon-effort` | Analyze → carbon dashboard | co2 by `type`; daily overview. | Recorded ledger estimates; the dashboard explores current Carbon assumptions. |
| `findings-open` | Findings | Dedicated workflow. | Actionable findings and measured savings. |
| `findings-fixes` | Findings | Dedicated workflow. | Actionable findings and measured savings. |
| `findings-results` | Findings | Dedicated workflow. | Actionable findings and measured savings. |
| `models-needing-prices` | Analyze → tokens dashboard | tokens by `model_family`; daily overview. | Original grouping, measures, filters and supporting records. |
| `token-volume-trend` | Analyze → tokens dashboard | tokens by `provider`; 7-day smoothing. | Original grouping, measures, filters and supporting records. |
| `token-volume-log` | Analyze → tokens dashboard | tokens by `repository_name`; daily overview; logarithmic scale. | Original grouping, measures, filters and supporting records. |
| `cumulative-api-cost` | Analyze → tokens dashboard | cost by `provider`; cumulative. | Original grouping, measures, filters and supporting records. |
| `mcp-call-trend` | Analyze → mcp dashboard | calls by `tool_name`; 7-day smoothing. | Original grouping, measures, filters and supporting records. |

[homepage-query-cards.json](homepage-query-cards.json) is the executable recipe snapshot. Tests compile every source query and cover, exercise dashboard context at every supported card window, and verify the details/back path preserves query tokens, thresholds and chart settings.
