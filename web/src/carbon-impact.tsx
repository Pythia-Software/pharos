import React, { useEffect, useLayoutEffect, useMemo, useState } from "react";
import { EMPTY_QUERY, decodeQuery, encodeQuery, type WhereTerm } from "@pythia-software/query-table-core";
import { ChartSelect, DisplayControl, PeriodControl, RangeControl, RollingControl, StackedColumns, chartPeriodCount, chartSpan, pickRange, rangeLabel, smoothingWhere, timeBounds, usageTime, withTimeRange, type FilterChange, type PeriodView } from "./usage-chart";
import { chartPeriods, earliestKey, isHourly, parseDay, periodLabel, validPeriodCount } from "./chart-period";
import { chartBucketsWithHistory, foldSeries, plottedBucketKey, validChartDisplay, validRollingPeriods, type ChartDisplay, type ChartSeries } from "./chart-series";
import { readSlice, withSlice } from "./chart-selection";
import { validChartPresentation, type ChartPresentation } from "./chart-selection";
import { preferences } from "./preferences";

type CarbonTokens = { tokens: Record<string, Record<string, number>> };
type CarbonFactors = { tiers: Array<{ key: string; label: string }> };
type CarbonResult = { pue: number; co2: number; facilityWh: number; byCategory: Record<string, { wh: number }>; grams: (wh: number) => number };
type CarbonData = {
  factors: CarbonFactors;
  windows: CarbonTokens[];
  series: Array<CarbonTokens & { day: string; value: string }>;
  choices: Record<string, string[]>;
  undated_tokens: number;
};
declare global {
  interface Window {
    pharosCarbon?: {
      show: (data: CarbonData, label: string, scope: string) => void;
      pending: (error?: string) => void;
      estimate: (factors: CarbonFactors, period: CarbonTokens) => CarbonResult;
      mass: (grams: number) => string;
      energy: (wh: number) => string;
      settingsSnapshot: () => Record<string, unknown>;
    };
  }
}

type CarbonView = PeriodView & ChartPresentation & { metric: "co2" | "energy"; split: "type" | "provider" | "repository_name" | "tier"; rollingPeriods: number; display?: ChartDisplay };
const viewKey = "pharos-carbon-chart", filtersKey = "pharos-carbon-filters";
const defaultView: CarbonView = { metric: "co2", split: "type", period: "week", hours: 6, days: 3, seriesLimit: 15, rollingPeriods: 0, display: "interval" };
const splits: Array<[CarbonView["split"], string]> = [["type", "Token type"], ["provider", "Provider"], ["repository_name", "Repository"], ["tier", "Model tier"]];
const categories: ChartSeries[] = [
  { key: "uncached_input", label: "Uncached input", color: "var(--heat-3)" },
  { key: "cache_read", label: "Cache reads", color: "var(--heat-1)" },
  { key: "cache_write", label: "Cache writes", color: "var(--gold)" },
  { key: "output", label: "Output", color: "var(--heat-2)" },
];
const providers: Record<string, string> = { claude: "Claude", codex: "Codex", chatgpt: "ChatGPT", antigravity: "Antigravity", canonical: "Canonical", tl1: "TL1" };

function initialView(dashboard = false): CarbonView {
  let saved = dashboard ? {} : preferences().get<Partial<CarbonView>>(viewKey, {});
  if (new URLSearchParams(location.search).has("analyze_chart")) {
    try { saved = JSON.parse(new URLSearchParams(location.search).get("analyze_chart") ?? "null") ?? saved; } catch { /* Keep existing view. */ }
  }
  // Keep only supported preferences, filling new controls for older links.
  const view = Object.fromEntries(Object.entries(defaultView).map(([key, value]) => [key, saved[key as keyof CarbonView] ?? value])) as CarbonView;
  return ["co2", "energy"].includes(view.metric) && splits.some(([key]) => key === view.split) && Object.hasOwn(chartPeriods, view.period)
    && validPeriodCount(view.hours) && validPeriodCount(view.days) && validRollingPeriods(view.rollingPeriods) && validChartDisplay(view.display) && validChartPresentation(view) ? view : defaultView;
}

function initialWhere(dashboard = false): WhereTerm[] {
  const params = new URLSearchParams(location.search);
  const encoded = (params.get("analyze_topic") === "carbon" ? params.get("analyze_filters") : null) ?? params.get("q_carbon") ?? (dashboard ? "" : preferences().get<string>(filtersKey, ""));
  if (encoded) {
    try { return decodeQuery(encoded).where; } catch { return []; }
  }
  // Preserve the old calculator's 30-day choice until new filters are saved.
  if (dashboard || preferences().get<{ window?: string }>("pharos-carbon-v1", {}).window === "30d") {
    const from = new Date(); from.setHours(0, 0, 0, 0); from.setDate(from.getDate() - 29);
    return withTimeRange([], usageTime, from);
  }
  return [];
}

function CarbonFilter({ field, choices, where, onFilter }: { field: "repository_name" | "provider"; choices: string[]; where: WhereTerm[]; onFilter: FilterChange }) {
  const selected = readSlice(where, field), values = [...new Set([...choices, ...selected.values.map(value => value ?? "")])].sort();
  const multiple = selected.excluded || selected.values.length > 1;
  const label = field === "repository_name" ? "Repository" : "Provider";
  const value = multiple ? "selection" : selected.values.length ? `v:${selected.values[0] ?? ""}` : "all";
  return <ChartSelect label={label} value={value}
    options={[["all", `All ${field === "repository_name" ? "repositories" : "providers"}`], ...values.map((name): [string, string] => [`v:${name}`, name ? field === "provider" ? providers[name] ?? name : name : field === "provider" ? "Unknown provider" : "No repository"]), ...(multiple ? [["selection", "Multiple values / exclusions", "Filters restored from this link"] as [string, string, string]] : [])]}
    onChange={next => onFilter(current => withSlice(current, field, { values: next === "all" ? [] : [next.slice(2) || null], excluded: false }))} />;
}

function useCarbonData(requestKey: string, refresh: number) {
  const [state, setState] = useState<{ key: string; data: CarbonData | null; loading: boolean; error: string }>({ key: "", data: null, loading: true, error: "" });
  useEffect(() => {
    if (!requestKey) return;
    const controller = new AbortController();
    setState(previous => ({ ...previous, loading: true, error: "" }));
    fetch("/api/query/usage/carbon", { method: "POST", headers: { "Content-Type": "application/json" }, body: requestKey, signal: controller.signal })
      .then(async response => {
        const body = await response.json();
        if (!response.ok) throw new Error(body.error || response.statusText);
        if (!body.factors || !body.windows?.length || !Array.isArray(body.series)) throw new Error("Carbon usage data is unavailable.");
        if (!controller.signal.aborted) setState({ key: requestKey, data: body, loading: false, error: "" });
      }).catch(error => { if (!controller.signal.aborted) setState({ key: requestKey, data: null, loading: false, error: error instanceof Error ? error.message : String(error) }); });
    return () => controller.abort();
  }, [requestKey, refresh]);
  return { ...state, data: state.key === requestKey && !state.loading ? state.data : null, loading: state.key !== requestKey || state.loading, error: state.key === requestKey ? state.error : "" };
}

export function CarbonImpact({ queryLinks }: { queryLinks?: (state: { where: WhereTerm[]; view: CarbonView }) => React.ReactNode } = {}) {
  const [view, setView] = useState(() => initialView(Boolean(queryLinks))), [where, setWhere] = useState(() => initialWhere(Boolean(queryLinks)));
  const [calculator, setCalculator] = useState(() => window.pharosCarbon);
  const [refresh, setRefresh] = useState(0), [assumptions, setAssumptions] = useState(0);
  const limit = view.seriesLimit ?? 15;
  const update = (change: Partial<CarbonView>) => setView(previous => { const next = { ...previous, ...change }; preferences().set(viewKey, next);
    if (queryLinks) { const url = new URL(location.href); url.searchParams.set("analyze_chart", JSON.stringify(next)); history.replaceState(history.state, "", url); }
    return next; });
  const onFilter: FilterChange = change => setWhere(previous => {
    const next = change(previous), encoded = encodeQuery({ ...EMPTY_QUERY, where: next });
    preferences().set(filtersKey, encoded);
    const url = new URL(location.href);
    const parameter = queryLinks ? "analyze_filters" : "q_carbon";
    if (next.length || queryLinks) url.searchParams.set(parameter, encoded); else url.searchParams.delete(parameter);
    history.replaceState(null, "", url.pathname + url.search + url.hash);
    return next;
  });
  useEffect(() => {
    if (!queryLinks) return;
    const url = new URL(location.href);
    if (!url.searchParams.has("analyze_chart")) url.searchParams.set("analyze_chart", JSON.stringify(view));
    if (!url.searchParams.has("analyze_filters")) url.searchParams.set("analyze_filters", encodeQuery({ ...EMPTY_QUERY, where }));
    history.replaceState(history.state, "", url);
  }, []);
  useEffect(() => {
    const ready = () => setCalculator(window.pharosCarbon);
    window.addEventListener("pharos:carbon-ready", ready);
    ready();
    return () => window.removeEventListener("pharos:carbon-ready", ready);
  }, []);
  useEffect(() => {
    const reload = () => setRefresh(value => value + 1), recalculate = () => setAssumptions(value => value + 1), restore = () => { setWhere(initialWhere(Boolean(queryLinks))); setView(initialView(Boolean(queryLinks))); };
    window.addEventListener("pharos:usage-refresh", reload);
    window.addEventListener("pharos:carbon-settings", recalculate);
    window.addEventListener("pharos:route", restore);
    return () => {
      window.removeEventListener("pharos:usage-refresh", reload);
      window.removeEventListener("pharos:carbon-settings", recalculate);
      window.removeEventListener("pharos:route", restore);
    };
  }, []);
  const requestKey = JSON.stringify({ where, hourly: isHourly(view.period), split: view.split === "type" ? "" : view.split });
  const bounds = timeBounds(where, usageTime), count = chartPeriodCount(view);
  const cumulative = view.display === "cumulative";
  const requestedWhere = smoothingWhere(where, usageTime, view.period, cumulative ? 0 : view.rollingPeriods, count);
  const separate = requestedWhere !== where;
  const state = useCarbonData(requestKey, refresh);
  const historyData = useCarbonData(separate ? JSON.stringify({ where: requestedWhere, hourly: isHourly(view.period), split: view.split === "type" ? "" : view.split }) : "", refresh);
  const data = state.data, chartData = separate ? historyData.data : data;
  const loading = state.loading || separate && historyData.loading, error = state.error || (separate ? historyData.error : "");
  const scopeValue = (field: "repository_name" | "provider") => {
    const selection = readSlice(where, field);
    if (!selection.values.length) return field === "repository_name" ? "All repositories" : "All providers";
    return `${selection.excluded ? "Except " : ""}${selection.values.map(value => value ? field === "provider" ? providers[value] ?? value : value : field === "provider" ? "Unknown provider" : "No repository").join(", ")}`;
  };
  const scope = `${scopeValue("repository_name")} · ${scopeValue("provider")} · ${bounds.from || bounds.to ? `${bounds.from?.toLocaleString() ?? "Start of history"} → ${bounds.to?.toLocaleString() ?? "Present"}${bounds.to ? " (end exclusive)" : ""}` : "All time"}`;
  const label = `${scopeValue("repository_name")} · ${scopeValue("provider")} · ${rangeLabel(bounds) || "All time"}`;
  const onDateRange = (from?: Date, to?: Date) => {
    onFilter(current => withTimeRange(current, usageTime, from, to));
  };
  useLayoutEffect(() => {
    if (data) calculator?.show(data, label, scope);
    else calculator?.pending(error);
  }, [calculator, data, label, scope, error]);
  const { series, buckets, cumulativeStart } = useMemo(() => {
    const dates = chartData?.series.map(point => parseDay(point.day)) ?? [], earliest = earliestKey(chartData?.series.map(point => point.day) ?? []);
    const buckets = chartBucketsWithHistory(view.period, ...chartSpan(bounds, earliest), count, dates,
      !cumulative && view.rollingPeriods > 1 ? timeBounds(requestedWhere, usageTime).from ?? earliest : undefined);
    const byKey = new Map(buckets.map(bucket => [bucket.key, bucket]));
    const cumulativeStart = bounds.from ?? earliest;
    if (!chartData || !calculator) return { series: [], buckets, cumulativeStart };
    const rows: Array<[string, string, number]> = [];
    for (const point of chartData.series) {
      const key = plottedBucketKey(parseDay(point.day), view.period, count, buckets, view.display), bucket = byKey.get(key);
      if (!bucket) continue;
      const result = calculator.estimate(chartData.factors, point);
      if (view.split === "type") {
        for (const category of categories) {
          const wh = result.byCategory[category.key].wh;
          bucket.values[category.key] = (bucket.values[category.key] ?? 0) + (view.metric === "co2" ? result.grams(wh) : wh * result.pue);
        }
      } else rows.push([key, point.value, view.metric === "co2" ? result.co2 : result.facilityWh]);
    }
    const name = (value: string) => view.split === "tier" ? chartData.factors.tiers.find(tier => tier.key === value)?.label ?? value : view.split === "provider" ? (providers[value] ?? value) || "Unknown provider" : value || "No repository";
    return { series: view.split === "type" ? categories : foldSeries(rows, byKey, name, undefined, [], limit, view.split === "tier" ? "carbon:tier" : view.split), buckets, cumulativeStart };
  }, [calculator, chartData, assumptions, view.metric, view.split, view.period, count, bounds.from?.getTime(), bounds.to?.getTime(), limit, view.rollingPeriods, cumulative]);
  const format = view.metric === "co2" ? calculator?.mass : calculator?.energy;
  const title = `${cumulative ? "Cumulative estimated" : "Estimated"} ${view.metric === "co2" ? "CO₂e" : "electricity"}${cumulative ? "" : ` per ${periodLabel(view.period, count)}`} by ${splits.find(([key]) => key === view.split)?.[1].toLowerCase()}`;
  const choices = state.data?.choices ?? {};
  return <section className="carbon-impact" aria-label="Carbon Impact" data-feedback-label="Carbon Impact">
    <div id="carbonAssumptions" className="carbon-card carbon-assumptions-card" />
    <section className="carbon-analysis" aria-label="Carbon usage analysis">
    <section className="carbon-filters" aria-label="Usage filters">
      <div className="carbon-filter-heading"><h3>Usage filters</h3>{where.length ? <button type="button" onClick={() => onFilter(() => [])}>Clear filters</button> : null}</div>
      <div className="usage-chart-controls">
        <CarbonFilter field="repository_name" choices={choices.repository_name ?? []} where={where} onFilter={onFilter} />
        <CarbonFilter field="provider" choices={choices.provider ?? []} where={where} onFilter={onFilter} />
      </div>
    </section>
    <div id="carbonSummary" className="carbon-card carbon-summary" aria-label="Filtered carbon estimate" />
    {data && !data.windows.some(window => Object.values(window.tokens).some(counts => Object.values(counts).some(value => value > 0))) ? <p className="muted" role="status">No reported token usage matches these filters.</p> : null}
    {error ? <button type="button" onClick={() => setRefresh(value => value + 1)}>Retry carbon usage</button> : null}
    <StackedColumns title={title} controls={<>
      <ChartSelect label="Metric" value={view.metric} options={[["co2", "CO₂e"], ["energy", "Electricity"]]} onChange={metric => update({ metric })} />
      <ChartSelect label="Breakdown" value={view.split} options={splits} onChange={split => update({ split, seriesLimit: 15 })} />
      <DisplayControl value={view.display} onChange={display => update({ display })} />
      <RangeControl bounds={bounds} editable includeTime={isHourly(view.period)} onChange={onDateRange} />
      <PeriodControl view={view} onChange={update} />
      <RollingControl value={view.rollingPeriods} display={view.display} period={view.period} count={count} onChange={rollingPeriods => update({ rollingPeriods })} />
    </>} series={series} buckets={buckets} period={view.period} periodCount={count} rollingPeriods={view.rollingPeriods} display={view.display} cumulativeStart={cumulativeStart} scale="linear"
      format={format ?? (value => String(value))} noun="" loading={loading} error={error} legend={series.length > 0} onExpand={() => update({ seriesLimit: Math.min(1000, limit + 5) })}
      onRange={pickRange(onFilter, usageTime, view.period, count, period => update({ period }))} />
    {data?.undated_tokens ? <p className="muted">{data.undated_tokens.toLocaleString()} tokens have no usage date. They are included in the total but cannot be placed on the chart.</p> : null}
    {queryLinks?.({ where, view })}
    </section>
    <div id="carbonCard" className={data ? "carbon-card" : ""} />
  </section>;
}
