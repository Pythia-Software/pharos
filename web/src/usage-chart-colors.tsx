import React, { createContext, useCallback, useContext, useLayoutEffect, useMemo, useState } from "react";
import type { RenderRegistry } from "@pythia-software/query-table-ui";
import { modelColorKey } from "./chart-colors";
import type { ChartSeries } from "./chart-series";

export type ChartFacetPalette = { facet: string | null; colors: Map<string, string> };
const emptyPalette: ChartFacetPalette = { facet: null, colors: new Map() };
const PaletteContext = createContext(emptyPalette);
const PublishContext = createContext<(palette: ChartFacetPalette) => void>(() => {});

export function chartFacetPalette(facet: string | null, series: ChartSeries[]): ChartFacetPalette {
  if (!facet || !["repository_name", "provider", "providers", "model_family"].includes(facet)) return emptyPalette;
  const colors = new Map<string, string>();
  for (const item of series) {
    const values = item.value !== undefined ? [item.value] : item.foldedValues ?? [];
    for (const value of values) colors.set(facet === "model_family" ? modelColorKey(value) : value, item.color);
  }
  return { facet, colors };
}

export function chartCellColor(palette: ChartFacetPalette, field: string, value: unknown, modelFamily?: unknown): string | undefined {
  const model = palette.facet === "model_family" && (field === "model_family" || field === "model");
  if (!model && field !== palette.facet) return undefined;
  const raw = field === "model" && modelFamily !== undefined && modelFamily !== null ? modelFamily : value;
  const key = String(raw ?? "");
  return palette.colors.get(model ? modelColorKey(key) : key);
}

export function usageTableRenderers<Row extends Record<string, unknown>>(registry: RenderRegistry<Row>, palette: ChartFacetPalette): RenderRegistry<Row> {
  if (!palette.facet) return registry;
  return Object.fromEntries(Object.entries(registry).map(([name, render]) => [name, context => {
    const content = render(context);
    const color = chartCellColor(palette, context.field.name, context.value, context.row.model_family);
    return color ? <span className="usage-facet-value"><span className="usage-facet-badge" data-chart-facet={palette.facet} style={{ backgroundColor: color }} aria-hidden="true" /><span className="usage-facet-label">{content}</span></span> : content;
  }]));
}

export function UsageChartColorsProvider({ children }: { children: React.ReactNode }) {
  const [palette, setPalette] = useState(emptyPalette);
  const publish = useCallback((next: ChartFacetPalette) => setPalette(previous => previous.facet === next.facet && previous.colors.size === next.colors.size && [...previous.colors].every(([key, color]) => next.colors.get(key) === color) ? previous : next), []);
  return <PublishContext.Provider value={publish}><PaletteContext.Provider value={palette}>{children}</PaletteContext.Provider></PublishContext.Provider>;
}

export function useUsageTableRenderers<Row extends Record<string, unknown>>(registry: RenderRegistry<Row>): RenderRegistry<Row> {
  const palette = useContext(PaletteContext);
  return useMemo(() => usageTableRenderers(registry, palette), [registry, palette]);
}

export function useUsageChartPalette(facet: string | null, series: ChartSeries[]) {
  const publish = useContext(PublishContext);
  useLayoutEffect(() => {
    publish(chartFacetPalette(facet, series));
  }, [publish, facet, series]);
  useLayoutEffect(() => () => publish(emptyPalette), [publish]);
}
