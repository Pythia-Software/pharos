import { originDimensions, originLabel, isOriginDimension, useOriginMetadata } from "./origin-dimensions";
import React, { useEffect, useLayoutEffect, useState } from "react";
import { EMPTY_QUERY, decodeQuery, encodeQuery, type WhereTerm } from "@pythia-software/query-table-core";
import { ChartSelect, rangeLabel, timeBounds, usageTime, withTimeRange, type FilterChange } from "./usage-chart";
import { readSlice, withSlice } from "./chart-selection";
import { preferences } from "./preferences";
import type { CarbonFactors } from "./carbon-analysis-catalog";

type CarbonTokens = { tokens: Record<string, Record<string, number>> };
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
      settingsSnapshot: (factors?: CarbonFactors) => Record<string, unknown>;
    };
  }
}

const filtersKey = "pharos-carbon-filters";
const providers: Record<string, string> = { claude: "Claude", codex: "Codex", chatgpt: "ChatGPT", antigravity: "Antigravity", canonical: "Canonical", tl1: "TL1" };

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

function CarbonFilter({ field, choices, where, onFilter }: { field: "repository_name" | "provider" | "channel" | "machine_id" | "execution_machine_id"; choices: string[]; where: WhereTerm[]; onFilter: FilterChange }) {
  const selected = readSlice(where, field), values = [...new Set([...choices, ...selected.values.map(value => value ?? "")])].sort();
  const multiple = selected.excluded || selected.values.length > 1;
  const label = originDimensions.find(([key]) => key===field)?.[1] ?? (field === "repository_name" ? "Repository" : "Provider");
  const value = multiple ? "selection" : selected.values.length ? `v:${selected.values[0] ?? ""}` : "all";
  return <ChartSelect label={label} value={value}
    options={[["all", `All ${field === "repository_name" ? "repositories" : field === "provider" ? "providers" : label.toLowerCase()+" values"}`], ...values.map((name): [string, string] => [`v:${name}`, isOriginDimension(field) ? originLabel(field,name) : name ? field === "provider" ? providers[name] ?? name : name : field === "provider" ? "Unknown provider" : "No repository"]), ...(multiple ? [["selection", "Multiple values / exclusions", "Filters restored from this link"] as [string, string, string]] : [])]}
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

// The Carbon page's filters and legacy-page filter storage. The dashboard
// keeps its filters in analyze_filters (useUsageDashboardState); the legacy
// page in q_carbon and its saved preference.
export function useLegacyCarbonFilters(): [WhereTerm[], FilterChange] {
  const [where, setWhere] = useState(() => initialWhere());
  useEffect(() => {
    const restore = () => setWhere(initialWhere());
    window.addEventListener("pharos:route", restore);
    return () => window.removeEventListener("pharos:route", restore);
  }, []);
  const onFilter: FilterChange = change => setWhere(previous => {
    const next = change(previous), encoded = encodeQuery({ ...EMPTY_QUERY, where: next });
    preferences().set(filtersKey, encoded);
    const url = new URL(location.href);
    if (next.length) url.searchParams.set("q_carbon", encoded); else url.searchParams.delete("q_carbon");
    history.replaceState(null, "", url.pathname + url.search + url.hash);
    return next;
  });
  return [where, onFilter];
}

// Carbon Impact: the usage filters and the page's own estimate and
// assumptions (carbon.js), around a chart its caller draws with the engine
// (useCarbonCatalog builds the catalog from the current assumptions).
export function CarbonImpact({ where, onFilter, children }: { where: WhereTerm[]; onFilter: FilterChange; children: React.ReactNode }) {
  useOriginMetadata();
  const [calculator, setCalculator] = useState(() => window.pharosCarbon);
  const [refresh, setRefresh] = useState(0);
  useEffect(() => {
    const ready = () => setCalculator(window.pharosCarbon);
    window.addEventListener("pharos:carbon-ready", ready);
    ready();
    return () => window.removeEventListener("pharos:carbon-ready", ready);
  }, []);
  useEffect(() => {
    const reload = () => setRefresh(value => value + 1);
    window.addEventListener("pharos:usage-refresh", reload);
    return () => window.removeEventListener("pharos:usage-refresh", reload);
  }, []);
  const requestKey = JSON.stringify({ where, hourly: false, split: "" });
  const bounds = timeBounds(where, usageTime);
  const state = useCarbonData(requestKey, refresh);
  const data = state.data, error = state.error;
  const scopeValue = (field: "repository_name" | "provider" | "channel" | "machine_id" | "execution_machine_id") => {
    const selection = readSlice(where, field);
    if (!selection.values.length) return field === "repository_name" ? "All repositories" : field === "provider" ? "All providers" : `All ${originDimensions.find(([key]) => key===field)?.[1].toLowerCase()} values`;
    return `${selection.excluded ? "Except " : ""}${selection.values.map(value => isOriginDimension(field) ? originLabel(field,value) : value ? field === "provider" ? providers[value] ?? value : value : field === "provider" ? "Unknown provider" : "No repository").join(", ")}`;
  };
  const originScope = originDimensions.filter(([field]) => readSlice(where,field).values.length).map(([field]) => scopeValue(field)).join(" · ");
  const scope = `${originScope ? originScope+" · " : ""}${scopeValue("repository_name")} · ${scopeValue("provider")} · ${bounds.from || bounds.to ? `${bounds.from?.toLocaleString() ?? "Start of history"} → ${bounds.to?.toLocaleString() ?? "Present"}${bounds.to ? " (end exclusive)" : ""}` : "All time"}`;
  const label = `${originScope ? originScope+" · " : ""}${scopeValue("repository_name")} · ${scopeValue("provider")} · ${rangeLabel(bounds) || "All time"}`;
  useLayoutEffect(() => {
    if (data) calculator?.show(data, label, scope);
    else calculator?.pending(error);
  }, [calculator, data, label, scope, error]);
  const choices = state.data?.choices ?? {};
  return <section className="carbon-impact" aria-label="Carbon Impact" data-feedback-label="Carbon Impact">
    <section className="carbon-analysis" aria-label="Carbon usage analysis">
    <section className="carbon-filters" aria-label="Usage filters">
      <div className="carbon-filter-heading"><h3>Usage filters</h3>{where.length ? <button type="button" onClick={() => onFilter(() => [])}>Clear filters</button> : null}</div>
      <div className="usage-chart-controls">
        <CarbonFilter field="repository_name" choices={choices.repository_name ?? []} where={where} onFilter={onFilter} />
        <CarbonFilter field="provider" choices={choices.provider ?? []} where={where} onFilter={onFilter} />
        {originDimensions.map(([field]) => <CarbonFilter key={field} field={field} choices={choices[field] ?? []} where={where} onFilter={onFilter} />)}
      </div>
    </section>
    {data && !data.windows.some(window => Object.values(window.tokens).some(counts => Object.values(counts).some(value => value > 0))) ? <p className="muted" role="status">No reported token usage matches these filters.</p> : null}
    {error ? <button type="button" onClick={() => setRefresh(value => value + 1)}>Retry carbon usage</button> : null}
    {children}
    <div id="carbonSummary" className="carbon-card carbon-summary" aria-label="Filtered carbon estimate" />
    {data?.undated_tokens ? <p className="muted">{data.undated_tokens.toLocaleString()} tokens have no usage date. They are included in the total but cannot be placed on the chart.</p> : null}
    </section>
    <div id="carbonAssumptions" className="carbon-card carbon-assumptions-card" />
    <div id="carbonCard" className={data ? "carbon-card" : ""} />
  </section>;
}
