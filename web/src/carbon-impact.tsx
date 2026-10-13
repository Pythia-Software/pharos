import React, { useEffect, useLayoutEffect, useMemo, useState } from "react";
import { EMPTY_QUERY, decodeQuery, encodeQuery, type WhereTerm } from "@pythia-software/query-table-core";
import { ChartSelect, PeriodControl, RangeControl, StackedColumns, chartPeriodCount, chartSpan, pickRange, rangeLabel, timeBounds, usageTime, withTimeRange, type FilterChange, type PeriodView } from "./usage-chart";
import { chartPeriods, earliestKey, isHourly, parseDay, periodBuckets, periodKey, periodLabel, validPeriodCount } from "./chart-period";
import { foldSeries, type ChartSeries } from "./chart-series";
import { readSlice, withSlice } from "./chart-selection";
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
    };
  }
}

type CarbonView = PeriodView & { metric: "co2" | "energy"; split: "type" | "provider" | "repository_name" | "tier" };
const viewKey = "pharos-carbon-chart", filtersKey = "pharos-carbon-filters";
const defaultView: CarbonView = { metric: "co2", split: "type", period: "week", hours: 6, days: 3 };
const splits: Array<[CarbonView["split"], string]> = [["type", "Token type"], ["provider", "Provider"], ["repository_name", "Repository"], ["tier", "Model tier"]];
const categories: ChartSeries[] = [
  { key: "uncached_input", label: "Uncached input", color: "var(--heat-3)" },
  { key: "cache_read", label: "Cache reads", color: "var(--heat-1)" },
  { key: "cache_write", label: "Cache writes", color: "var(--gold)" },
  { key: "output", label: "Output", color: "var(--heat-2)" },
];
const providers: Record<string, string> = { claude: "Claude", codex: "Codex", chatgpt: "ChatGPT", antigravity: "Antigravity", canonical: "Canonical", tl1: "TL1" };

function initialView(): CarbonView {
  const saved = preferences().get<Partial<CarbonView>>(viewKey, {});
  // Explicitly select supported preferences, dropping old scale/smoothing settings.
  const view = Object.fromEntries(Object.entries(defaultView).map(([key, value]) => [key, saved[key as keyof CarbonView] ?? value])) as CarbonView;
  return ["co2", "energy"].includes(view.metric) && splits.some(([key]) => key === view.split) && Object.hasOwn(chartPeriods, view.period)
    && validPeriodCount(view.hours) && validPeriodCount(view.days) ? view : defaultView;
}

function initialWhere(): WhereTerm[] {
  const encoded = new URLSearchParams(location.search).get("q_carbon") ?? preferences().get<string>(filtersKey, "");
  if (encoded) {
    try { return decodeQuery(encoded).where; } catch { return []; }
  }
  // Preserve the old calculator's 30-day choice until new filters are saved.
  if (preferences().get<{ window?: string }>("pharos-carbon-v1", {}).window === "30d") {
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

export function CarbonImpact() {
  const [view, setView] = useState(initialView), [where, setWhere] = useState(initialWhere);
  const [calculator, setCalculator] = useState(() => window.pharosCarbon);
  const [refresh, setRefresh] = useState(0), [assumptions, setAssumptions] = useState(0), [limit, setLimit] = useState(15);
  const update = (change: Partial<CarbonView>) => setView(previous => { const next = { ...previous, ...change }; preferences().set(viewKey, next); return next; });
  const onFilter: FilterChange = change => setWhere(previous => {
    const next = change(previous), encoded = encodeQuery({ ...EMPTY_QUERY, where: next });
    preferences().set(filtersKey, encoded);
    const url = new URL(location.href);
    if (next.length) url.searchParams.set("q_carbon", encoded); else url.searchParams.delete("q_carbon");
    history.replaceState(null, "", url.pathname + url.search + url.hash);
    return next;
  });
  useEffect(() => {
    const ready = () => setCalculator(window.pharosCarbon);
    window.addEventListener("pharos:carbon-ready", ready);
    ready();
    return () => window.removeEventListener("pharos:carbon-ready", ready);
  }, []);
  useEffect(() => {
    const reload = () => setRefresh(value => value + 1), recalculate = () => setAssumptions(value => value + 1), restore = () => setWhere(initialWhere());
    window.addEventListener("pharos:usage-refresh", reload);
    window.addEventListener("pharos:carbon-settings", recalculate);
    window.addEventListener("pharos:route", restore);
    return () => {
      window.removeEventListener("pharos:usage-refresh", reload);
      window.removeEventListener("pharos:carbon-settings", recalculate);
      window.removeEventListener("pharos:route", restore);
    };
  }, []);
  useEffect(() => setLimit(15), [view.split]);
  const requestKey = JSON.stringify({ where, hourly: isHourly(view.period), split: view.split === "type" ? "" : view.split });
  const [state, setState] = useState<{ key: string; data: CarbonData | null; loading: boolean; error: string }>({ key: "", data: null, loading: true, error: "" });
  useEffect(() => {
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
  const data = state.key === requestKey && !state.loading ? state.data : null;
  const loading = state.key !== requestKey || state.loading, error = state.key === requestKey ? state.error : "";
  const bounds = timeBounds(where, usageTime), count = chartPeriodCount(view);
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
  const { series, buckets } = useMemo(() => {
    const buckets = periodBuckets(view.period, ...chartSpan(bounds, earliestKey(data?.series.map(point => point.day) ?? [])), count);
    const byKey = new Map(buckets.map(bucket => [bucket.key, bucket]));
    if (!data || !calculator) return { series: [], buckets };
    const rows: Array<[string, string, number]> = [];
    for (const point of data.series) {
      const key = periodKey(parseDay(point.day), view.period, count), bucket = byKey.get(key);
      if (!bucket) continue;
      const result = calculator.estimate(data.factors, point);
      if (view.split === "type") {
        for (const category of categories) {
          const wh = result.byCategory[category.key].wh;
          bucket.values[category.key] = (bucket.values[category.key] ?? 0) + (view.metric === "co2" ? result.grams(wh) : wh * result.pue);
        }
      } else rows.push([key, point.value, view.metric === "co2" ? result.co2 : result.facilityWh]);
    }
    const name = (value: string) => view.split === "tier" ? data.factors.tiers.find(tier => tier.key === value)?.label ?? value : view.split === "provider" ? (providers[value] ?? value) || "Unknown provider" : value || "No repository";
    return { series: view.split === "type" ? categories : foldSeries(rows, byKey, name, undefined, [], limit, view.split === "tier" ? "carbon:tier" : view.split), buckets };
  }, [calculator, data, assumptions, view.metric, view.split, view.period, count, bounds.from?.getTime(), bounds.to?.getTime(), limit]);
  const format = view.metric === "co2" ? calculator?.mass : calculator?.energy;
  const title = `Estimated ${view.metric === "co2" ? "CO₂e" : "electricity"} per ${periodLabel(view.period, count)} by ${splits.find(([key]) => key === view.split)?.[1].toLowerCase()}`;
  const choices = state.data?.choices ?? {};
  return <section className="carbon-impact" aria-label="Carbon Impact" data-feedback-label="Carbon Impact">
    <div id="carbonAssumptions" className="carbon-card carbon-assumptions-card" />
    <section className="carbon-analysis" aria-label="Carbon usage analysis">
    <section className="carbon-filters" aria-label="Usage filters">
      <div className="carbon-filter-heading"><h3>Usage filters</h3>{where.length ? <button type="button" onClick={() => onFilter(() => [])}>Clear filters</button> : null}</div>
      <div className="usage-chart-controls">
        <CarbonFilter field="repository_name" choices={choices.repository_name ?? []} where={where} onFilter={onFilter} />
        <CarbonFilter field="provider" choices={choices.provider ?? []} where={where} onFilter={onFilter} />
        <RangeControl bounds={bounds} editable includeTime={isHourly(view.period)} onChange={onDateRange} />
      </div>
    </section>
    <div id="carbonSummary" className="carbon-card carbon-summary" aria-label="Filtered carbon estimate" />
    {data && !data.windows.some(window => Object.values(window.tokens).some(counts => Object.values(counts).some(value => value > 0))) ? <p className="muted" role="status">No reported token usage matches these filters.</p> : null}
    {error ? <button type="button" onClick={() => setRefresh(value => value + 1)}>Retry carbon usage</button> : null}
    <StackedColumns title={title} controls={<>
      <ChartSelect label="Metric" value={view.metric} options={[["co2", "CO₂e"], ["energy", "Electricity"]]} onChange={metric => update({ metric })} />
      <ChartSelect label="Breakdown" value={view.split} options={splits} onChange={split => update({ split })} />
      <PeriodControl view={view} onChange={update} />
    </>} series={series} buckets={buckets} period={view.period} periodCount={count} scale="linear"
      format={format ?? (value => String(value))} noun="" loading={loading} error={error} legend={series.length > 0} onExpand={() => setLimit(value => value + 5)}
      onRange={pickRange(onFilter, usageTime, view.period, count, period => update({ period }))} />
    {data?.undated_tokens ? <p className="muted">{data.undated_tokens.toLocaleString()} tokens have no usage date. They are included in the total but cannot be placed on the chart.</p> : null}
    </section>
    <div id="carbonCard" className={data ? "carbon-card" : ""} />
  </section>;
}
