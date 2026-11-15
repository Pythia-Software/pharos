import { useEffect, useState } from "react";

export type SessionShapeCoverage = { state: string; pending_sessions?: number; stale_sessions?: number; missing_sessions?: number };

// Coverage describes the whole deduplicated dataset, before the view's filters.
export function sessionShapeFreshnessNote(coverage: SessionShapeCoverage | null | undefined): string {
  if (!coverage || coverage.state === "current") return "";
  const stale = Number(coverage.stale_sessions) || 0, missing = Number(coverage.missing_sessions) || 0;
  const parts = ["Session statistics are updating across the library."];
  if (stale) parts.push(`${stale.toLocaleString()} agent session${stale === 1 ? " uses" : "s use"} older statistics.`);
  if (missing) parts.push(`${missing.toLocaleString()} agent session${missing === 1 ? " is" : "s are"} not included yet.`);
  parts.push("Charts use available statistics; totals may change as indexing progresses.");
  return parts.join(" ");
}

let latest: SessionShapeCoverage | null = null;
const listeners = new Set<(coverage: SessionShapeCoverage) => void>();

export function reportSessionShapeCoverage(url: string, body: unknown) {
  if (!/\/api\/query\/session_shape\/query-v3(?:\?|$)/.test(url) || !body || typeof body !== "object") return;
  const coverage = (body as { coverage?: SessionShapeCoverage }).coverage;
  if (!coverage || typeof coverage.state !== "string" || JSON.stringify(coverage) === JSON.stringify(latest)) return;
  latest = coverage;
  listeners.forEach(listener => listener(coverage));
}

export function useSessionShapeCoverage(): SessionShapeCoverage | null {
  const [coverage, setCoverage] = useState(latest);
  useEffect(() => { listeners.add(setCoverage); setCoverage(latest); return () => { listeners.delete(setCoverage); }; }, []);
  return coverage;
}
