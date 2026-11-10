import { DataReadinessIndicator } from "./data-readiness";
import { originDimensions, originLabel, isOriginDimension, useOriginMetadata } from "./origin-dimensions";
import React, { useEffect, useLayoutEffect, useMemo, useState } from "react";
import factors from "../../carbon/co2_factors.json";
import {  type WhereTerm } from "@pythia-software/query-table-core";
import { ChartSelect, rangeLabel, timeBounds, usageTime, type FilterChange } from "./usage-chart";
import { readSlice, withSlice } from "./chart-selection";
import type { CarbonFactors } from "./carbon-analysis-catalog";

import type { CarbonTokens, CarbonUsage } from "./carbon-usage";
import type { ServedReadiness } from "./result-cache";

type CarbonResult = { pue: number; co2: number; facilityWh: number; byCategory: Record<string, { wh: number }>; grams: (wh: number) => number };
type CarbonData = CarbonUsage & { factors: CarbonFactors };
declare global {
  interface Window {
    pharosCarbon?: {
      show: (data: CarbonData, label: string, scope: string) => void;
      pending: (error?: string) => void;
      estimate: (factors: CarbonFactors, period: CarbonTokens, settings?: Record<string, unknown>) => CarbonResult;
      mass: (grams: number) => string;
      energy: (wh: number) => string;
      settingsSnapshot: (factors?: CarbonFactors) => Record<string, unknown>;
    };
  }
}

const providers: Record<string, string> = { claude: "Claude", codex: "Codex", chatgpt: "ChatGPT", antigravity: "Antigravity", canonical: "Canonical export", tl1: "TL1" };

function CarbonFilter({ field, choices, where, onFilter }: { field: "repository_name" | "provider" | "channel" | "machine_id" | "execution_machine_id"; choices: string[]; where: WhereTerm[]; onFilter: FilterChange }) {
  const selected = readSlice(where, field), values = [...new Set([...choices, ...selected.values.map(value => value ?? "")])].sort();
  const multiple = selected.excluded || selected.values.length > 1;
  const label = originDimensions.find(([key]) => key===field)?.[1] ?? (field === "repository_name" ? "Repository" : "Provider");
  const value = multiple ? "selection" : selected.values.length ? `v:${selected.values[0] ?? ""}` : "all";
  return <ChartSelect label={label} value={value}
    options={[["all", `All ${field === "repository_name" ? "repositories" : field === "provider" ? "providers" : label.toLowerCase()+" values"}`], ...values.map((name): [string, string] => [`v:${name}`, isOriginDimension(field) ? originLabel(field,name) : name ? field === "provider" ? providers[name] ?? name : name : field === "provider" ? "Unknown provider" : "No repository"]), ...(multiple ? [["selection", "Multiple values / exclusions", "Filters restored from this link"] as [string, string, string]] : [])]}
    onChange={next => onFilter(current => withSlice(current, field, { values: next === "all" ? [] : [next.slice(2) || null], excluded: false }))} />;
}

// Carbon Impact: the usage filters and the page's own estimate and
// assumptions (carbon.js), around a chart its caller draws with the engine
// (useCarbonCatalog builds the catalog from the current assumptions). The
// caller reads the usage and the filter choices through query-v3
// (carbon-usage.ts); usage is null until it has loaded.
export type CarbonImpactUsage = { usage: CarbonUsage | null; choices: Record<string, string[]>; error: string; readiness?: ServedReadiness; retry: () => void };
export function CarbonImpact({ where, onFilter, usage: state, children }: { where: WhereTerm[]; onFilter: FilterChange; usage: CarbonImpactUsage; children: React.ReactNode }) {
  useOriginMetadata();
  const [calculator, setCalculator] = useState(() => window.pharosCarbon);
  useEffect(() => {
    const ready = () => setCalculator(window.pharosCarbon);
    window.addEventListener("pharos:carbon-ready", ready);
    ready();
    return () => window.removeEventListener("pharos:carbon-ready", ready);
  }, []);
  const bounds = timeBounds(where, usageTime);
  const data = useMemo(() => state.usage ? { ...state.usage, factors: factors as CarbonFactors } : null, [state.usage]), error = state.error;
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
  const choices = state.choices;
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
    {error ? <button type="button" onClick={state.retry}>Retry carbon usage</button> : null}
    {children}
    <div className="data-readiness-region"><DataReadinessIndicator id="carbon:estimate" title="Filtered carbon estimate" dataset="usage" query={{where}} {...state.readiness} /><div id="carbonSummary" className="carbon-card carbon-summary" aria-label="Filtered carbon estimate" /></div>
    {data?.undated_tokens ? <p className="muted">{data.undated_tokens.toLocaleString()} tokens have no usage date. They are included in the total but cannot be placed on the chart.</p> : null}
    </section>
    <div id="carbonAssumptions" className="carbon-card carbon-assumptions-card" />
    <div id="carbonCard" className={data ? "carbon-card" : ""} />
  </section>;
}
