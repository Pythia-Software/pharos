import React, { useEffect, useRef, useState } from "react";
import {
  EMPTY_QUERY,
  encodeQuery,
  decodeQueryStrict,
  loadSchema,
} from "@pythia-software/query-table-core";
import modelRequestDocument from "../../schemas/model_requests.schema.json";
import { dashboardQueryDetails } from "./dashboard-query-links";
import { withTimeRange } from "./chart-range";
import { ChartSelect, PeriodControl, RangeControl, presetPeriod, finerPeriod, chartPeriodCount } from "./usage-chart";
import { useViewState } from "./view-state";
import { ViewDisclosure } from "./view-disclosure";
import { PageLink } from "./nested-pages";
import { SearchHelp } from "./search-help";
import { usageWorkspaceURL } from "./usage-workspace-model";

import {
  thinkingBreakdowns,
  thinkingCount,
  thinkingDuration,
  thinkingFixedOrder,
  thinkingPercentiles,
  thinkingRequest,
  thinkingStyle,
  thinkingViews,
  thinkingWhere,
  thinkingWindow,
  initialThinking,
  defaultThinking,
  orderThinkingGroups,
  type ThinkingResponse,
  type ThinkingState,
  type ThinkingGroup,
} from "./thinking-model";
import {
  ThinkingBoxes,
  ThinkingCDF,
  ThinkingCompactions,
  ThinkingConversation,
  ThinkingDistribution,
  ThinkingHourly,
  ThinkingScatter,
} from "./thinking-charts";
import "./thinking.css";

async function loadThinking(
  url: string,
  signal: AbortSignal,
): Promise<ThinkingResponse> {
  const response = await fetch(url, { signal });
  const body = await response.json();
  if (!response.ok) throw Error(body.error ?? "Unable to load request timing.");
  return body;
}
const amount = (value: number | null) =>
  value === null ? "—" : value.toFixed(2) + "×";
export function thinkingDashboardURL(
  state: ThinkingState,
  detail: boolean,
  methodology = false,
) {
  const params = new URLSearchParams(location.search);
  params.set("thinking", JSON.stringify(state));
  const address = new URL(
    usageWorkspaceURL(
      {
        mode: "dashboards",
        topic: detail ? "thinking-detail" : "thinking-overview",
        dataset: "model_requests",
      },
      params,
    ),
    location.origin,
  );
  if (methodology) {
    address.searchParams.set(
      "disclosures",
      JSON.stringify({ "thinking:methodology": true }),
    );
    address.searchParams.set("reveal", "section:thinking:methodology");
  }
  return address.pathname + address.search;
}
function GroupLabel({
  group,
  state,
}: {
  group: ThinkingGroup;
  state: ThinkingState;
}) {
  return (
    <span className="thinking-group-label">
      <i style={{ background: thinkingStyle(group, state.groupBy).color }} />
      {group.label}
    </span>
  );
}
function GroupTable({ data, state }: { data: ThinkingResponse; state: ThinkingState }) {
  const ratio = state.view === "calendar" && state.calendarMetric === "ratio",
    reasoning = state.view === "reasoning",
    format = (v: number | null) => ratio ? amount(v) : reasoning
      ? v === null ? "—" : thinkingCount(v) + " tokens"
      : thinkingDuration(v);
  return <div className="thinking-table-wrap"><table>
    <thead><tr><th>Group</th><th>Retained requests</th>
      <th>{reasoning ? "Positive reports" : ratio ? "Gaps with estimates" : "Usable gaps"}</th>
      <th>Median</th><th>P95</th><th>Conversations</th></tr></thead>
    <tbody>{data.groups.map(g => <tr key={g.key}>
      <th><GroupLabel group={g} state={state} /></th>
      <td>{thinkingCount(g.Retained)}</td><td>{thinkingCount(g.stats.n)}</td>
      <td>{format(g.stats.median)}</td><td>{format(g.stats.p95)}</td>
      <td>{thinkingCount(reasoning ? g.RetainedConversations : g.Conversations)}</td>
    </tr>)}</tbody>
  </table></div>;
}
function ComparisonHeatmap({
  data,
  state,
  change,
}: {
  data: ThinkingResponse;
  state: ThinkingState;
  change: (value: Partial<ThinkingState>) => void;
}) {
  const maximum = Math.max(1, ...data.groups.flatMap((g) => g.counts)),
    fixed = thinkingFixedOrder(state);
  return (
    <div className="thinking-table-wrap thinking-heatmap">
      <table>
        <thead>
          <tr>
            <th>Group</th>
            {[
              ["volume", "Volume"],
              ["median", "Median"],
              ["p95", "P95"],
            ].map(([key, label]) => (
              <th
                key={key}
                aria-sort={
                  !fixed && state.sortBy === key
                    ? state.sortDirection === "asc"
                      ? "ascending"
                      : "descending"
                    : "none"
                }
              >
                <button
                  type="button"
                  disabled={fixed}
                  onClick={() =>
                    change({
                      sortBy: key as ThinkingState["sortBy"],
                      sortDirection:
                        state.sortBy === key && state.sortDirection === "desc"
                          ? "asc"
                          : "desc",
                    })
                  }
                >
                  {label}{" "}
                  {!fixed && state.sortBy === key
                    ? state.sortDirection === "desc"
                      ? "↓"
                      : "↑"
                    : ""}
                </button>
              </th>
            ))}
            {data.edges.slice(0, -1).map((v, i) => (
              <th className="thinking-bin-heading" key={v}>
                <span>
                  {thinkingDuration(v)}–{thinkingDuration(data.edges[i + 1])}
                </span>
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {data.groups.map((g) => (
            <tr key={g.key}>
              <th>
                <GroupLabel group={g} state={state} />
                {g.stats.n < 30 ? (
                  <small>
                    {g.stats.n ? "Sparse sample" : "No usable timing"}
                  </small>
                ) : null}
              </th>
              <td>{thinkingCount(g.stats.n)}</td>
              <td>{thinkingDuration(g.stats.median)}</td>
              <td>{thinkingDuration(g.stats.p95)}</td>
              {g.counts.map((n, i) => (
                <td
                  key={i}
                  className="thinking-bin-cell"
                  style={{
                    background: g.stats.n
                      ? `color-mix(in srgb,var(--sea) ${Math.round((state.normalization === "share" ? n / g.stats.n : n / maximum) * 95)}%,var(--panel))`
                      : undefined,
                  }}
                  title={`${g.label} · ${thinkingDuration(data.edges[i])}–${thinkingDuration(data.edges[i + 1])}: ${n} / ${g.stats.n} (${((100 * n) / Math.max(1, g.stats.n)).toFixed(1)}%)`}
                />
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      <p className="usage-dashboard-note">
        Color:{" "}
        {state.normalization === "share"
          ? "0–100% of each group"
          : "0–" + thinkingCount(maximum) + " requests"}
        .
      </p>
    </div>
  );
}
function Methodology() {
  return (
    <ViewDisclosure
      viewKey="thinking:methodology"
      className="usage-query-search thinking-disclosure"
    >
      <summary>Methodology, exclusions and production limits</summary>
      <p>
        Think time is a coarse proxy: the first retained usage-bearing response
        event minus the latest linked tool-result timestamp. The request
        ledger’s <code>requested_at</code> field names that response evidence,
        not a verified request-send clock. The wait includes reasoning, some
        generation, queueing, scheduling and delivery. It measures neither
        first-token latency nor pure inference nor complete user-turn duration.
      </p>
      <p>
        Only gaps from 100 ms through 600 seconds are usable. Missing or
        incomplete tool timestamps, cross-stream links, anchors at or before the
        previous response, and nonpositive gaps stay in the retained denominator
        with their exclusion reason. User-message-only anchors are excluded.
        Missing timing is never zero. Explicit native-alias links select one
        request-bearing transcript per alias family, preferring the native
        source and non-mirrored workspace. Inactive conversation-group members
        are excluded. Unlinked copies cannot safely be deduplicated by similar
        text.
      </p>
      <p>
        Distributions and quartiles use individual observations, including
        sparse groups; only overlays and boxes impose sample thresholds. Wait
        boxes need 30 usable gaps from three conversations. Reasoning boxes need
        30 retained requests from three retained conversations and a positive
        reasoning report. Positive reasoning counts use all retained requests,
        including those without timing. Numeric defaults cannot distinguish
        unreported reasoning from an explicit zero.
      </p>
      <p>
        Latency bins share fixed boundaries: 1, 2, 3, 5, 10 repeated across
        scales, from 100 ms through 600 seconds. Intervals include the lower
        edge, exclude the upper, and include the final maximum. Shares normalize
        within a group or group/time bucket. Every observed group is available;
        there is no hidden top-N. Custom periods are limited to 5,000 buckets
        per request; a larger result asks you to coarsen the interval.
      </p>
      <p>
        The server computes exact selected-population counts and quantiles.
        These reads do not change transcripts or usage. Large libraries and
        all-time fine-grained views can be expensive; requests are cancellable
        and budget errors remain visible. Model-request queries expose the same
        timing projection for evidence inspection.
      </p>
      <h3>Factor table</h3>
      <p>
        For each model, fit log gap against log context, log(1 + reported
        reasoning), log(1 + other output), cache-read share, log(1 + request
        position), effort and UTC calendar-day indicators. Stream fixed effects
        compare changes within an agent stream. Fits need 250 requests from 25
        conversations; streams need three requests. Collinear controls are
        omitted. Approximate 95% confidence intervals cluster by conversation.
        Reasoning and other-output doublings are evaluated at their median
        positive count. Associations cannot establish that context was
        unnecessary or that trimming it preserves quality.
      </p>
      <h3>Time-of-day workload adjustment</h3>
      <p>
        A separate per-model ridge regression estimates log gap from workload,
        cache and effort with five conversation-held-out folds. It uses only the
        selected cohort; every displayed request is evaluated by a fit that
        excludes its conversation. Each training fold needs 200 requests from 20
        conversations, a reported effort present in training and numeric
        features within training ranges. Unsupported observations remain
        raw-only. This production fit is a transparent linear baseline, unlike
        the study’s preset spline; it must be calibrated on future dates and
        independent libraries before recommending when to run work.
      </p>
      <p>
        Calendar cells take the median of individual gaps or individual
        observed/estimated ratios. Each measure needs 20 observations from three
        conversations per cell. Hour profiles first take daily median ratios for
        three-hour windows, then average their logs with equal day weights. Each
        day/window needs 20 modeled requests from three conversations; points
        need four qualifying days. Comparisons pair the same days, with 600
        deterministic whole-day bootstrap resamples for approximate intervals.
        Generated output is a retrospective input; these estimates cannot
        predict a request at its start.
      </p>
      <h3>After compaction</h3>
      <p>
        Each pair compares three requests before and after a compaction, within
        one conversation/agent stream, fixed model and effort, and one hour.
        Each side needs two usable gaps. Wait, context and output ratios are
        computed per event before taking medians. Output and task stage may
        change too. The gap crossing compaction is excluded; the next two
        requests must both have usable gaps. All
        events enter statistics even when a ratio exceeds the fixed 0–1×
        viewport.
      </p>
      <h3>Complete turn duration</h3>
      <p>
        A user turn can span many model requests, tools, approvals, parallel
        agents and pauses. Turn-level wall time needs explicit user-input and
        final-answer boundaries on the same task. Summing parallel request gaps
        or using conversation span would measure something else.
      </p>
    </ViewDisclosure>
  );
}
export function ThinkingDashboard({ detail }: { detail: boolean }) {
  const [nativeFilters, setNativeFilters] = useViewState<string>(
    "thinking_filters",
    "",
  );
  const nativeQuery = nativeFilters
    ? decodeQueryStrict(nativeFilters)
    : EMPTY_QUERY;
  if (!nativeQuery || nativeQuery.grouping)
    throw Error("Invalid Thinking population filters.");
  const [linked, setLinked] = useViewState<Partial<ThinkingState>>(
    "thinking",
    () => initialThinking(new URLSearchParams(location.search)),
  );
  const initial = initialThinking(new URLSearchParams(location.search));
  const state: ThinkingState = {
      ...initial,
      ...linked,
      minimum: 0,
      timezone: initial.timezone,
    },
    [data, setData] = useState<ThinkingResponse | null>(null),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true),
    [refresh, setRefresh] = useState(0),
    [rangeVersion, setRangeVersion] = useState(0),
    [evidenceOpen, setEvidenceOpen] = useState(false),
    [validation, setValidation] = useState<ThinkingResponse["validation"]>(),
    [validationError, setValidationError] = useState("");
  // Capture effective viewer defaults even for partial card links.
  useEffect(() => {
    if (Object.keys(defaultThinking).some((key) => !Object.hasOwn(linked, key)) ||
        linked.timezone !== state.timezone || linked.minimum !== 0)
      setLinked(state);
  }, [JSON.stringify(linked)]);
  const brushOrigin = useRef<Pick<
    ThinkingState,
    "range" | "from" | "to" | "period" | "hours" | "days"
  > | null>(null);
  const change = (next: Partial<ThinkingState>, brush = false) => {
    if (
      ["range", "from", "to"].some((k) => Object.hasOwn(next, k)) &&
      nativeFilters
    ) {
      const where = withTimeRange(nativeQuery.where, {
        first: "requested_at",
        last: "requested_at",
      });
      setNativeFilters(encodeQuery({ ...EMPTY_QUERY, where }));
    }
    if (brush) setRangeVersion((n) => n + 1);
    if (brush && !brushOrigin.current)
      brushOrigin.current = {
        range: state.range,
        from: state.from,
        to: state.to,
        period: state.period,
        hours: state.hours,
        days: state.days,
      };
    if (brush && next.from && next.to)
      next = { ...next, period: finerPeriod(state.period, new Date(next.from), new Date(next.to), chartPeriodCount(state)) };
    if (!brush && ["range", "from", "to"].some((k) => Object.hasOwn(next, k)))
      brushOrigin.current = null;
    setLinked((p) => ({ ...state, ...p, ...next }));
  };
  const request = thinkingRequest(state, !detail, false, nativeFilters);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError("");
    void loadThinking(request, controller.signal)
      .then((value) => {
        setData({ ...value, groups: orderThinkingGroups(value.groups, state) });
        setLoading(false);
      })
      .catch((e) => {
        if (!controller.signal.aborted) {
          setError(String(e.message ?? e));
          setLoading(false);
        }
      });
    return () => controller.abort();
  }, [request, refresh]);
  useEffect(() => {
    const update = () => setRefresh((n) => n + 1);
    window.addEventListener("pharos:usage-refresh", update);
    window.addEventListener("pharos:day-change", update);
    return () => {
      window.removeEventListener("pharos:usage-refresh", update);
      window.removeEventListener("pharos:day-change", update);
    };
  }, []);
  const evidenceRequest = thinkingRequest(
    { ...state, view: "histogram" },
    false,
    true,
    nativeFilters,
  );
  useEffect(() => {
    if (detail || !evidenceOpen) return;
    const controller = new AbortController();
    setValidation(undefined);
    setValidationError("");
    void loadThinking(evidenceRequest, controller.signal)
      .then((d) => setValidation(d.validation))
      .catch((e) => {
        if (!controller.signal.aborted) setValidationError(e.message);
      });
    return () => controller.abort();
  }, [detail, evidenceOpen, evidenceRequest, refresh]);
  const dataGroups = data?.groups ?? [],
    models = data?.Models ?? [],
    efforts = data?.Efforts ?? [],
    box = state.view === "reasoning" || state.view === "wait-box",
    fixed = thinkingFixedOrder(state);
  const trace =
    data?.traces?.find((t) => t.ID === state.session) ?? data?.traces?.[0];
  return (
    <section
      className="thinking-dashboard"
      data-view-field="thinking"
      aria-busy={loading}
    >
      <div className="thinking-links">
        <PageLink href={thinkingDashboardURL(state, !detail)}>
          {detail ? "Waiting and Compaction" : "Thinking"} →
        </PageLink>
        {detail ? (
          <PageLink href={thinkingDashboardURL(state, false, true)}>
            Methodology & validation →
          </PageLink>
        ) : null}
        <PageLink
          href={usageWorkspaceURL(
            {
              mode: "query",
              dataset: "model_requests",
              query: {
                ...EMPTY_QUERY,
                where: [...nativeQuery.where, ...thinkingWhere(state)],
              },
            },
            new URLSearchParams(location.search),
          )}
        >
          Inspect model-request evidence →
        </PageLink>
      </div>
      <div className="usage-tools-controls usage-analysis-controls thinking-controls">
        {detail ? (
          <ChartSelect
            label="View"
            value={state.view}
            options={thinkingViews.map(([a, b]) => [a, b])}
            onChange={(view) =>
              change({
                view: view as ThinkingState["view"],
                ...((view === "reasoning" || view === "wait-box") &&
                state.groupBy === "none"
                  ? { groupBy: "model_effort" }
                  : {}),
              })
            }
          />
        ) : null}
        <ChartSelect
          label="Model"
          value={state.model}
          options={[
            ["all", "All recorded models"],
            ...models.map((m) => [m, m] as [string, string]),
          ]}
          onChange={(model) => change({ model })}
        />
        <ChartSelect
          label="Effort"
          value={state.effort}
          options={[
            ["all", "All efforts"],
            ...efforts.map((e) => [e, e] as [string, string]),
          ]}
          onChange={(effort) => change({ effort })}
        />
        {detail ? (
          <>
            <ChartSelect
              label="Breakdown"
              value={state.groupBy}
              options={thinkingBreakdowns.map(([a, b]) => [a, b])}
              onChange={(groupBy) =>
                change({ groupBy: groupBy as ThinkingState["groupBy"] })
              }
            />
            <ChartSelect
              label="Order by"
              disabled={fixed}
              value={fixed ? "fixed" : state.sortBy + ":" + state.sortDirection}
              options={
                fixed
                  ? [["fixed", "Model / effort order"]]
                  : [
                      ["median:desc", "Median · decreasing"],
                      ["median:asc", "Median · increasing"],
                      ["p95:desc", "P95 · decreasing"],
                      ["p95:asc", "P95 · increasing"],
                      ["volume:desc", "Volume · decreasing"],
                      ["volume:asc", "Volume · increasing"],
                    ]
              }
              onChange={(v) => {
                const [sortBy, sortDirection] = v.split(":");
                change({
                  sortBy: sortBy as ThinkingState["sortBy"],
                  sortDirection:
                    sortDirection as ThinkingState["sortDirection"],
                });
              }}
            />
            {!box && state.view !== "calendar" ? (
              <ChartSelect
                label="Show as"
                value={state.normalization}
                options={[
                  ["share", "Share within group"],
                  ["count", "Request counts"],
                ]}
                onChange={(v) =>
                  change({ normalization: v as ThinkingState["normalization"] })
                }
              />
            ) : null}
          </>
        ) : null}
        <RangeControl
          key={rangeVersion}
          timezone={state.timezone || undefined}
          bounds={{
            from:
              state.range === "custom" && state.from
                ? new Date(state.from)
                : undefined,
            to:
              state.range === "custom" && state.to
                ? new Date(state.to)
                : undefined,
            ...(!["custom", "all"].includes(state.range)
              ? {
                  preset: state.range as Exclude<
                    ThinkingState["range"],
                    "custom" | "all"
                  >,
                }
              : {}),
          }}
          onPresetChange={(range, oldest) => change(presetPeriod(range, oldest))}
          onChange={(from, to, preset) =>
            change({
              range: preset ?? "custom",
              from: preset ? undefined : from?.toISOString(),
              to: preset ? undefined : to?.toISOString(),
            })
          }
        />
        {detail && state.view === "time" ? (
          <PeriodControl view={state} onChange={(next) => change(next)} />
        ) : null}
        {detail && state.view === "calendar" ? (
          <>
            <ChartSelect
              label="Rows"
              value={state.calendarRows}
              options={[
                ["day", "Days"],
                ["week", "Weeks"],
                ["month", "Months"],
              ]}
              onChange={(v) =>
                change({ calendarRows: v as ThinkingState["calendarRows"] })
              }
            />
            <ChartSelect
              label="Measure"
              value={state.calendarMetric}
              options={[
                ["raw", "Actual wait · seconds"],
                ["ratio", "Compared with workload estimate"],
              ]}
              onChange={(v) =>
                change({ calendarMetric: v as ThinkingState["calendarMetric"] })
              }
            />
          </>
        ) : null}
      </div>
      {nativeQuery.where.length ? (
        <p className="usage-dashboard-note">
          Applied query filters:{" "}
          {dashboardQueryDetails(loadSchema(modelRequestDocument), {
            ...EMPTY_QUERY,
            where: nativeQuery.where,
          })
            .flatMap((d) => d.values)
            .join(" and ")}
          .{" "}
          <button type="button" onClick={() => setNativeFilters("")}>
            Clear query filters
          </button>
        </p>
      ) : null}
      {error ? (
        <p role="alert" className="query-table-error">
          {error}{" "}
          <button type="button" onClick={() => setRefresh((n) => n + 1)}>
            Try again
          </button>
        </p>
      ) : loading ? (
        <p role="status" className="usage-dashboard-note">
          Computing request timing…
        </p>
      ) : null}
      {data && !error ? (
        <div className={loading ? "thinking-loading" : ""}>
          <p className="thinking-population" role="status">
            <strong>
              {thinkingCount(
                detail
                  ? state.view === "reasoning"
                    ? dataGroups.reduce((n, g) => n + g.stats.n, 0)
                    : state.view === "calendar" &&
                        state.calendarMetric === "ratio"
                      ? data.Adjusted
                      : data.Timed
                  : data.Timed,
              )}{" "}
              {detail && state.view === "reasoning"
                ? "positive reasoning reports"
                : detail &&
                    state.view === "calendar" &&
                    state.calendarMetric === "ratio"
                  ? "gaps with workload estimates"
                  : "usable timing gaps"}
            </strong>{" "}
            from {thinkingCount(data.Retained)} retained requests ·{" "}
            {thinkingCount(data.Conversations)} conversations
            {detail
              ? ` · ${dataGroups.length} groups shown`
              : ""}
            .
          </p>
          {detail ? (
            <>
              <h2>Compare whole distributions</h2>
              {fixed ? (
                <p className="usage-dashboard-note">
                  Effort stays low to high, with Unreported last. Model panels
                  follow the shared model order.
                </p>
              ) : null}
              {state.view === "calendar" ? (
                <p className="usage-dashboard-note">
                  Cells show{" "}
                  {state.calendarMetric === "raw"
                    ? "median actual wait in seconds"
                    : "median individual observed / estimated wait; 2× means twice the workload estimate"}
                  . {thinkingCount(data.Timed)} raw gaps ·{" "}
                  {thinkingCount(data.Adjusted)} supported estimates. Each cell
                  needs 20 observations from three conversations. Dates and
                  hours use {data.timezone}. Drag vertically to narrow Range.
                </p>
              ) : null}
              {!dataGroups.length ? (
                <p className="query-empty">
                  {data.Retained
                    ? "No observations available for this view. Choose a wider range or clear filters."
                    : "No retained requests in this range. Choose a wider range or clear filters."}
                </p>
              ) : (
                <div
                  className="thinking-chart-scroll"
                  onDoubleClick={() => {
                    if (brushOrigin.current) {
                      const previous = brushOrigin.current;
                      brushOrigin.current = null;
                      change(previous);
                    }
                  }}
                >
                  {state.view === "heatmap" ? (
                    <ComparisonHeatmap
                      data={data}
                      state={state}
                      change={change}
                    />
                  ) : box ? (
                    <ThinkingBoxes data={data} state={state} />
                  ) : (
                    <ThinkingDistribution
                      data={data}
                      state={state}
                      onChange={change}
                    />
                  )}
                </div>
              )}
              {state.view === "time" ? (
                <div
                  className="thinking-legend"
                  role="group"
                  aria-label="Percentile key"
                >
                  {thinkingPercentiles.map((r) => (
                    <button
                      type="button"
                      key={r.key}
                      aria-pressed={!state.hiddenPercentiles.includes(r.key)}
                      onClick={() =>
                        change({
                          hiddenPercentiles: state.hiddenPercentiles.includes(
                            r.key,
                          )
                            ? state.hiddenPercentiles.filter((k) => k !== r.key)
                            : [...state.hiddenPercentiles, r.key],
                        })
                      }
                    >
                      <svg width="25" height="10" aria-hidden="true">
                        <line
                          x1="0"
                          x2="25"
                          y1="5"
                          y2="5"
                          stroke={r.color}
                          strokeWidth="2"
                          strokeDasharray={
                            r.key === "median" ? undefined : "2 5"
                          }
                        />
                      </svg>
                      {r.label}
                    </button>
                  ))}
                  <SearchHelp label="Percentile lines">
                    <p>
                      Exact median, P75, P90 and P95 need 30, 50, 100 and 200
                      timed requests per group/period respectively, plus five
                      conversations. Unsupported periods break the line. These
                      are descriptive quantiles, not confidence intervals.
                    </p>
                  </SearchHelp>
                </div>
              ) : null}
              {state.view === "calendar" ? (
                <>
                  <div className="thinking-calendar-key">
                    <span>
                      {state.calendarMetric === "raw"
                        ? "Shorter wait"
                        : "≤0.5×"}
                    </span>
                    <i />
                    <span>
                      {state.calendarMetric === "raw"
                        ? "Longer wait (shared scale)"
                        : "≥1.5×"}
                    </span>
                  </div>
                  <h3>Is one time of day consistently faster?</h3>
                  <p className="usage-dashboard-note">
                    Day-balanced workload-adjusted gaps in three-hour windows.
                    Each day/window needs 20 modeled gaps from three
                    conversations; each point needs four qualifying days.
                    Whiskers bootstrap whole days.
                  </p>
                  <div className="thinking-chart-scroll">
                    <ThinkingHourly data={data} state={state} />
                  </div>
                  <div className="thinking-controls">
                    <ChartSelect
                      label="Compare"
                      value={String(state.hourA)}
                      options={Array.from({ length: 8 }, (_, i) => [
                        String(i * 3),
                        thinkingWindow(i * 3),
                      ])}
                      onChange={(v) => change({ hourA: Number(v) })}
                    />
                    <ChartSelect
                      label="With"
                      value={String(state.hourB)}
                      options={Array.from({ length: 8 }, (_, i) => [
                        String(i * 3),
                        thinkingWindow(i * 3),
                      ])}
                      onChange={(v) => change({ hourB: Number(v) })}
                    />
                  </div>
                  <p className="usage-dashboard-note">
                    Compare / With on the same days: below 1× means shorter
                    adjusted waits. An interval crossing 1× does not show a
                    clear difference.
                  </p>
                  <div className="thinking-table-wrap">
                    <table>
                      <thead>
                        <tr>
                          <th>Group</th>
                          <th>Adjusted gap ratio</th>
                          <th>Approx. 95% interval</th>
                          <th>Paired days</th>
                        </tr>
                      </thead>
                      <tbody>
                        {dataGroups.map((g) => (
                          <tr key={g.key}>
                            <th>
                              <GroupLabel group={g} state={state} />
                            </th>
                            <td>
                              {g.comparison.Ratio === null
                                ? "Not enough paired days"
                                : amount(g.comparison.Ratio)}
                            </td>
                            <td>
                              {g.comparison.Ratio === null
                                ? "—"
                                : `${amount(g.comparison.Low)}–${amount(g.comparison.High)}`}
                            </td>
                            <td>{g.comparison.days}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                </>
              ) : null}
              <p className="usage-dashboard-note">
                {box
                  ? "Boxes span p25–p75; the middle line is the median, whiskers p10–p90. Unsupported groups retain labels without boxes."
                  : state.view === "calendar"
                    ? "Unsupported estimates remain missing; raw observations stay available. This is retrospective normalization using generated output, not a start-of-request forecast."
                    : "Four rounded latency bins per order of magnitude, 100 ms–600 s. Hover for counts. Shares normalize within each timed group/time bucket. Drag across time to update Range for every group and view; double-click restores the pre-brush range."}
              </p>
            </>
          ) : (
            <>
              <div className="thinking-stat-cards thinking-overview-stats">
                <div>
                  <span>Typical wait</span>
                  <strong>{thinkingDuration(data.summary.median)}</strong>
                  <small>Median observed gap</small>
                </div>
                <div>
                  <span>Slowest 10% begin at</span>
                  <strong>{thinkingDuration(data.summary.p90)}</strong>
                  <small>90th percentile</small>
                </div>
                <div>
                  <span>Timing coverage</span>
                  <strong>
                    {data.Retained
                      ? ((100 * data.Timed) / data.Retained).toFixed(1)
                      : "0"}
                    %
                  </strong>
                  <small>Usable gaps / retained requests</small>
                </div>
                <div>
                  <span>Conversations represented</span>
                  <strong>{thinkingCount(data.Conversations)}</strong>
                  <small>Including requests without timing</small>
                </div>
              </div>
              <p className="usage-dashboard-note">
                A coarse proxy for how long an agent spends thinking before its
                next response or step. It includes queueing and delivery; it is
                not a pure inference or first-token clock.
              </p>
              <div className="thinking-overview-pair">
                <ThinkingCDF groups={data.groups} />
                <div>
                  <h2>What affects the wait?</h2>
                  <p className="usage-dashboard-note">
                    Associated wait multiplier when each input doubles.
                  </p>
                  <div className="thinking-table-wrap thinking-factors">
                    <table>
                      <thead>
                        <tr>
                          <th>Model</th>
                          <th>2× context</th>
                          <th>2× reasoning</th>
                          <th>2× other output</th>
                        </tr>
                      </thead>
                      <tbody>
                        {[
                          ...new Set((data.factors ?? []).map((f) => f.model)),
                        ].map((model) => (
                          <tr key={model}>
                            <th>
                              {model}
                              <small>
                                {thinkingCount(
                                  data.factors!.find((f) => f.model === model)!
                                    .N,
                                )}{" "}
                                requests
                              </small>
                            </th>
                            {["context", "reasoning", "other"].map((factor) => {
                              const f = data.factors?.find(
                                (f) => f.model === model && f.factor === factor,
                              );
                              return (
                                <td
                                  key={factor}
                                  style={
                                    f
                                      ? {
                                          background: `color-mix(in srgb,${f.Ratio >= 1 ? "var(--warn)" : "var(--sea)"} ${Math.min(42, Math.abs(Math.log2(f.Ratio)) * 45)}%,var(--panel))`,
                                        }
                                      : {}
                                  }
                                >
                                  {f ? (
                                    <>
                                      <strong>{amount(f.Ratio)}</strong>{" "}
                                      <small>
                                        95% CI: {f.Low.toFixed(2)}–
                                        {f.High.toFixed(2)}×
                                      </small>
                                    </>
                                  ) : (
                                    "Not enough variation"
                                  )}
                                </td>
                              );
                            })}
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                  {!data.factors?.length ? (
                    <p className="usage-dashboard-note">
                      No supported adjusted fits: each model needs 250 requests
                      from 25 conversations and independent within-stream
                      variation.
                    </p>
                  ) : null}
                  <p className="usage-dashboard-note">
                    1× means no change. Conversation-clustered intervals are
                    approximate. Output, caching and task stage can change too.{" "}
                    <PageLink href={thinkingDashboardURL(state, false, true)}>
                      How the fits work →
                    </PageLink>
                  </p>
                </div>
              </div>
              <ViewDisclosure
                viewKey="thinking:coverage"
                className="usage-query-search thinking-disclosure"
              >
                <summary>Which models and agents have usable timing?</summary>
                <h3>By recorded model</h3>
                <GroupTable
                  data={{ ...data, groups: data.coverage ?? [] }}
                  state={{ ...state, view: "histogram", groupBy: "model" }}
                />
                <h3>By agent / harness</h3>
                <GroupTable
                  data={{ ...data, groups: data.agentCoverage ?? [] }}
                  state={{ ...state, view: "histogram", groupBy: "harness" }}
                />
                <p>
                  Missing timing stays in the retained denominator. A source
                  with no projected usage requests cannot appear in this table.
                  Model names are retained as recorded; agent family is not
                  necessarily API vendor.
                </p>
              </ViewDisclosure>
              <h2 className="thinking-section-title">
                Compaction and thinking time
              </h2>
              <p className="usage-dashboard-note">
                Compare median waits before and after compaction within the
                same model, effort and conversation stream. We use up to three
                waits before and the next two usable waits after, skipping the
                wait that crosses compaction.
              </p>
              <p className="usage-dashboard-note">
                For example, waits of 10, 9 and 11 seconds before compaction
                have a median of 10 seconds. Waits of 3 and 1 seconds after have
                a median of 2 seconds. The compaction ratio is after ÷ before:
                2 ÷ 10 = <strong>0.2×</strong>, an 80% shorter wait.
              </p>
              {data.compactions?.length ? (
                <ThinkingCompactions events={data.compactions} />
              ) : (
                <p className="usage-dashboard-note">
                  No eligible paired compactions in this population.
                </p>
              )}
              <p className="usage-dashboard-note">
                The response after compaction often gets shorter too, so the
                reduction can reflect both shorter context and shorter output.
                Compaction’s own duration is excluded.
              </p>
              <ViewDisclosure
                viewKey="thinking:conversation"
                className="usage-query-search thinking-disclosure"
              >
                <summary>Follow a real conversation</summary>
                <p>
                  Context accumulates, compaction resets it, and waits settle or
                  spike. The twelve longest matching streams are available as
                  examples; aggregate charts always use all selected requests.
                </p>
                {data.traces?.length ? (
                  <>
                    <div className="thinking-controls">
                      <ChartSelect
                        label="Conversation stream"
                        value={trace?.ID ?? ""}
                        options={data.traces.map((t) => [
                          t.ID,
                          t.Model +
                            " · " +
                            t.Count +
                            " requests · " +
                            t.Conversation,
                        ])}
                        onChange={(session) => change({ session })}
                      />
                      <ChartSelect
                        label="Wait axis"
                        value={state.traceScale}
                        options={[
                          ["linear", "Linear"],
                          ["log", "Logarithmic"],
                        ]}
                        onChange={(v) =>
                          change({
                            traceScale: v as ThinkingState["traceScale"],
                          })
                        }
                      />
                    </div>
                    {trace ? (
                      <>
                        <ThinkingConversation
                          trace={trace}
                          scale={state.traceScale}
                        />
                        <PageLink
                          href={`/work/${encodeURIComponent(trace.Workspace)}?conversation=${encodeURIComponent(trace.Conversation)}`}
                        >
                          Open source conversation →
                        </PageLink>
                      </>
                    ) : null}
                  </>
                ) : (
                  <p>No matching stream has six retained requests.</p>
                )}
              </ViewDisclosure>
              <ViewDisclosure
                viewKey="thinking:validation"
                className="usage-query-search thinking-disclosure"
                onToggle={(e) => setEvidenceOpen(e.currentTarget.open)}
              >
                <summary>Can we trust the clock? Validation evidence</summary>
                {validationError ? (
                  <p role="alert">{validationError}</p>
                ) : validation ? (
                  <>
                    <p>
                      {thinkingCount(validation.pairs)} matched positive
                      response timers. Rank correlation:{" "}
                      {validation.correlation?.toFixed(2) ?? "unavailable"};
                      median reported thinking / gap:{" "}
                      {amount(validation.medianRatio)}.
                    </p>
                    {validation.pairs ? (
                      <ThinkingScatter
                        points={validation.points}
                        title="Recorded thinking (Y) vs observed gap (X)"
                      />
                    ) : (
                      <p>
                        No matched direct thinking timers in this population.
                        Missing evidence does not validate the proxy.
                      </p>
                    )}
                    <p>
                      Harness-reported thinkingDurationMs is matched by
                      conversation and response identity, taking the maximum
                      repeated report once. The chart shows up to 1,600 evenly
                      selected pairs; statistics use all matched pairs.
                      Source/version selection and start/stop semantics still
                      need validation.
                    </p>
                    {validation.counterCount ? (
                      <>
                        <ThinkingScatter
                          points={validation.counters}
                          title="Reported API + tool (Y) vs total duration (X)"
                        />
                        <p>
                          {validation.counterOverlap} of{" "}
                          {validation.counterCount} latest conversation cost
                          states report API + tool above total. The scatter
                          shows up to 1,600 conversation states; counts use
                          every matched state. Counters are cumulative and may
                          overlap or reset; they can span dates outside the
                          selected request window. This is a data-quality
                          diagnostic, not independent per-request latency.
                        </p>
                      </>
                    ) : (
                      <p>
                        No cumulative timing counters available for these
                        conversations.
                      </p>
                    )}
                  </>
                ) : (
                  <p role="status">Loading numerical timing evidence…</p>
                )}
              </ViewDisclosure>
              <Methodology />
            </>
          )}
        </div>
      ) : null}
    </section>
  );
}
