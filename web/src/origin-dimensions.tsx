import React, { useEffect, useSyncExternalStore } from "react";
import type { RenderRegistry } from "@pythia-software/query-table-ui";

export const originDimensions = [["channel", "Channel"], ["machine_id", "Machine"], ["execution_machine_id", "Execution machine"]] as const;
export type OriginDimension = typeof originDimensions[number][0];
export const channelLabels: Record<string, string> = { conductor: "Conductor", tl1: "TL1", cli: "CLI", desktop: "Desktop app", web: "Web", ide: "IDE", api: "API / integration" };
const machines = new Map<string, string>([["environment:web", "Web (device unknown)"]]);
let revision = 0, loading: Promise<void> | undefined;
const listeners = new Set<() => void>();
if (typeof window !== "undefined") {
  const shared = (window as unknown as { pharosShare?: { datasets?: Record<string, Array<Record<string,unknown>>> } }).pharosShare;
  for (const rows of Object.values(shared?.datasets ?? {})) for (const row of rows) for (const [key,label] of [["machine_id","machine"],["execution_machine_id","execution_machine"]]) if (row[key] && row[label]) machines.set(String(row[key]),String(row[label]));
  for (const event of ["pharos:data-refresh","pharos:usage-refresh"]) window.addEventListener(event, () => { void loadOriginMetadata(true); });
}
const subscribe = (listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener); }; };
export const isOriginDimension = (field: string): field is OriginDimension => originDimensions.some(([key]) => key === field);
export function originLabel(field: string, value: unknown, row?: Record<string, unknown>): string {
  const key = String(value ?? "");
  if (!key) return field === "channel" ? "Unknown channel" : "Unknown machine";
  if (field === "channel") return channelLabels[key] ?? key;
  const local = row?.[field === "execution_machine_id" ? "execution_machine" : "machine"];
  return typeof local === "string" && local ? local : machines.get(key) ?? key;
}
export async function loadOriginMetadata(force = false): Promise<void> {
  if (force) loading = undefined;
  if (loading || typeof window === "undefined" || (window as unknown as { pharosShare?: unknown }).pharosShare) return loading;
  const pending = fetch("/api/origins").then(async response => {
    if (!response.ok) throw new Error(`Origin metadata: ${response.status}`);
    const data = await response.json() as { machines?: Array<{ id: string; label: string }> };
    if (loading !== pending) return;
    for (const machine of data.machines ?? []) machines.set(machine.id, machine.label);
    revision++;
    for (const listener of listeners) listener();
  }).catch(() => { if (loading === pending) loading = undefined; });
  loading = pending;
  return pending;
}
export function useOriginMetadata(active = true): number {
  const value = useSyncExternalStore(subscribe, () => revision, () => 0);
  useEffect(() => { if (active) void loadOriginMetadata(); }, [active]);
  return value;
}
export function originRenderers<Row extends Record<string, unknown>>(): RenderRegistry<Row> {
  return Object.fromEntries(originDimensions.map(([field]) => [`origin_${field}`, ({ value, row }: { value: unknown; row: Row }) => <>{originLabel(field, value, row)}</>]));
}
