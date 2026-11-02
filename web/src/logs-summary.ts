import { useEffect, useSyncExternalStore } from "react";
import type { LogsSummary } from "./logs-model";

// One summary for the Settings sidebar's marks and the Logs page, fetched
// while either shows and at most every 15 s unless forced.
let current: LogsSummary | null = null, fetchedAt = 0;
let pending: Promise<void> | null = null;
const listeners = new Set<() => void>();

export function refreshLogsSummary(force = false): Promise<void> {
  if (pending) return pending;
  if (!force && Date.now() - fetchedAt < 15_000) return Promise.resolve();
  pending = fetch("/api/logs/summary").then(response => response.ok ? response.json() as Promise<LogsSummary> : null).then(value => {
    if (!value) return;
    current = value;
    fetchedAt = Date.now();
    listeners.forEach(listener => listener());
  }).catch(() => {}).finally(() => { pending = null; });
  return pending;
}

const subscribe = (listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener); }; };

export function useLogsSummary(active: boolean): LogsSummary | null {
  const summary = useSyncExternalStore(subscribe, () => current);
  useEffect(() => {
    if (!active) return;
    void refreshLogsSummary();
    const timer = setInterval(() => void refreshLogsSummary(), 30_000);
    return () => clearInterval(timer);
  }, [active]);
  return summary;
}
