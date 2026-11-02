import { useEffect, useMemo, useState } from "react";
import factors from "../../carbon/co2_factors.json";
import { analysisCatalogFor } from "./analysis-catalogs";
import type { AnalysisCatalog } from "./dashboard-analysis-model";
import type { CarbonAssumptions, CarbonFactors } from "./carbon-analysis-catalog";
import { readConcurrencySettings } from "./concurrency-settings";
import type { ParallelismSettings } from "./parallelism-analysis-catalog";

// The user's settings that Carbon's and Parallelism's catalogs are built
// with, for any surface: a dashboard, a card's analysis, an all-cards check.

// Carbon: the factors the service embeds (carbon/co2_factors.json), and the
// calculator's current settings (carbon.js, a classic script, which also
// honours a link's analyze_carbon without saving it).
export function currentCarbonAssumptions(): CarbonAssumptions | undefined {
  const calculator = window.pharosCarbon;
  return calculator ? { settings: calculator.settingsSnapshot(factors as CarbonFactors), factors: factors as CarbonFactors, estimate: calculator.estimate } : undefined;
}
// Rebuilt when the calculator loads and whenever its settings change; the
// amounts are in the Carbon page's units.
export function useCarbonCatalog(): { catalog: AnalysisCatalog | null; format: (value: number, metric: string) => string } {
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    const bump = () => setRevision(value => value + 1);
    window.addEventListener("pharos:carbon-ready", bump);
    window.addEventListener("pharos:carbon-settings", bump);
    // The calculator may have loaded between the first render and now.
    if (window.pharosCarbon) bump();
    return () => { window.removeEventListener("pharos:carbon-ready", bump); window.removeEventListener("pharos:carbon-settings", bump); };
  }, []);
  const catalog = useMemo(() => { const carbon = currentCarbonAssumptions(); return carbon ? analysisCatalogFor("carbon", { carbon }) ?? null : null; }, [revision]);
  const format = (value: number, metric: string) => metric === "co2" ? window.pharosCarbon?.mass(value) ?? String(value) : metric === "energy" ? window.pharosCarbon?.energy(value) ?? String(value) : value.toLocaleString();
  return { catalog, format };
}

// Parallelism: the saved counting and gap, re-read when a save lands.
export const savedParallelism = (): ParallelismSettings => { const { counting, gap } = readConcurrencySettings(); return { counting, gap }; };
export function useParallelismCatalog(): { catalog: AnalysisCatalog; settings: ParallelismSettings } {
  const [settings, setSettings] = useState(savedParallelism);
  useEffect(() => {
    const saved = (event: Event) => { if (Object.hasOwn((event as CustomEvent<{ set?: object }>).detail?.set ?? {}, "pharos-concurrency-v1")) setSettings(savedParallelism()); };
    window.addEventListener("pharos:preferences-saved", saved);
    return () => window.removeEventListener("pharos:preferences-saved", saved);
  }, []);
  const catalog = useMemo(() => analysisCatalogFor("parallelism", { parallelism: settings.counting })!, [settings.counting]);
  return { catalog, settings };
}

// The complete catalog for any topic, null while Carbon's calculator loads.
export function useAnalysisCatalog(topic: string): AnalysisCatalog | null {
  const carbon = useCarbonCatalog(), parallelism = useParallelismCatalog();
  return topic === "carbon" ? carbon.catalog : topic === "parallelism" ? parallelism.catalog : analysisCatalogFor(topic) ?? null;
}
