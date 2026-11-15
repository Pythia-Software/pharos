import type { NestedPage } from "./nested-pages-model";

// Settings → Logs: diagnostics by source (internal/archive/diagnostic_logs.go).
// macos/MenuBar.swift lists the same sources in its Logs submenu.
export const logSources = [
  { id: "overview", label: "Overview", description: "What to read first when a page stays blank or Pharos stops, and a report to hand an agent." },
  { id: "page", label: "Page errors", description: "Errors the page hit: uncaught exceptions, failed and stalled requests, scripts that did not load, and web processes that stopped." },
  { id: "requests", label: "Slow & failed requests", description: "Requests the service answered slowly, with an error, or after an internal error, and those it is answering now." },
  { id: "service", label: "Service output", description: "This library's service output by run. A run that ended in a panic or exited with an error is a crash." },
  { id: "app", label: "App output", description: "Everything the Pharos app and its library services printed, by launch." },
  { id: "background", label: "Background work", description: "Background operations on this Mac that failed or were interrupted in the last week." },
  { id: "crashes", label: "Crash reports", description: "Reports macOS wrote in the last week when a Pharos process, or a web process the app started, crashed or used too many resources." },
] as const;

export type LogSourceId = typeof logSources[number]["id"];
export const logSourceIds: readonly LogSourceId[] = logSources.map(source => source.id);
export type Attention = "error" | "warning";
export const logsRoute = "/settings/logs";

export type SourceSummary = { id: Exclude<LogSourceId, "overview">; title: string; attention?: Attention; count: number; latest_at?: string; headline?: string; files?: string[] };
export type LogsSummary = { generated_at: string; folder: string; sources: SourceSummary[] };

/** The URL's source, or Overview for a missing or unknown one. */
export function logSourceFromParams(params: URLSearchParams): LogSourceId {
  const value = params.get("log_source") as LogSourceId | null;
  return value && logSourceIds.includes(value) ? value : "overview";
}

export function logSourceURL(id: LogSourceId): string {
  return id === "overview" ? logsRoute : `${logsRoute}?log_source=${id}`;
}

export function logSourceNavigationID(id: LogSourceId): string {
  return `logs:${id}`;
}

/** The Logs settings page becomes a group of its sources, each marked when it needs attention. */
export function withLogSources(settings: readonly NestedPage[], attention: Readonly<Partial<Record<LogSourceId, Attention>>> = {}): NestedPage[] {
  return settings.map(page => page.href !== logsRoute ? page : {
    id: logsRoute, label: page.label, description: page.description, attention: strongestAttention(Object.values(attention)),
    children: logSources.map(source => ({ id: logSourceNavigationID(source.id), label: source.label, href: logSourceURL(source.id), description: source.description, attention: attention[source.id] })),
  });
}

export function strongestAttention(levels: readonly (Attention | undefined)[]): Attention | undefined {
  return levels.includes("error") ? "error" : levels.includes("warning") ? "warning" : undefined;
}

export function attentionBySource(summary: LogsSummary | null): Partial<Record<LogSourceId, Attention>> {
  return Object.fromEntries((summary?.sources ?? []).filter(source => source.attention).map(source => [source.id, source.attention]));
}

/** Read first: errors before warnings, then the most recent. */
export function sourcesToRead(summary: LogsSummary): SourceSummary[] {
  const rank = (source: SourceSummary) => source.attention === "error" ? 2 : source.attention === "warning" ? 1 : 0;
  return summary.sources.filter(source => source.attention).sort((a, b) => rank(b) - rank(a) || (b.latest_at ?? "").localeCompare(a.latest_at ?? ""));
}

export function attentionLabel(attention: Attention | undefined): string {
  return attention === "error" ? "Has recent errors" : attention === "warning" ? "Has recent warnings" : "Nothing recent";
}

export function formatElapsed(milliseconds: number): string {
  if (milliseconds < 1000) return `${Math.round(milliseconds)} ms`;
  if (milliseconds < 60_000) return `${(milliseconds / 1000).toFixed(1)} s`;
  const minutes = Math.floor(milliseconds / 60_000), seconds = Math.round((milliseconds % 60_000) / 1000);
  return seconds ? `${minutes} min ${seconds} s` : `${minutes} min`;
}

export function formatLogTime(value: string | undefined, now = Date.now()): string {
  const at = Date.parse(value ?? "");
  if (!Number.isFinite(at)) return "";
  const minutes = Math.floor((now - at) / 60_000);
  const relative = minutes < 1 ? "just now" : minutes < 60 ? `${minutes} min ago` : minutes < 1440 ? `${Math.floor(minutes / 60)} hr ago` : `${Math.floor(minutes / 1440)} d ago`;
  return `${new Date(at).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit", second: "2-digit" })} · ${relative}`;
}
