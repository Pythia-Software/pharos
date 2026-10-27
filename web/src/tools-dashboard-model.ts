import type { WhereTerm } from "@pythia-software/query-table-core";
import type { MetricResult } from "./metric-request";
import { isOriginDimension, originLabel } from "./origin-dimensions";

// MetricCard displays bucket keys directly. Translate only its presentation
// copy and retain an exact lookup for record drilldowns, including duplicate
// device names and the original null group.
export function toolMetricPresentation(metric: MetricResult | undefined, groupBy: string[], labels: Record<string, (value: string) => string> = {}) {
  const source = new Map<string, MetricResult["buckets"][number]>();
  if (!metric) return { metric, sourceBucket: (bucket: MetricResult["buckets"][number]) => bucket };
  // A label shared by two values (device names, or Agent Time's command:foo
  // and tool:foo) names its value too, so every group stays distinct and
  // opens its own records.
  const names = groupBy.map(field => {
    const names = new Map<unknown, string>();
    const custom = labels[field] as ((value: string) => string) | undefined, origin = isOriginDimension(field);
    if (!custom && !origin) return names;
    const name = (value: unknown) => custom ? value === null || value === undefined || value === "" ? undefined : custom(String(value)) : originLabel(field, value);
    const shown = (value: unknown) => name(value) ?? value;
    const values = [...new Set(metric.buckets.map(bucket => bucket.keys[groupBy.indexOf(field)]))];
    for (const value of values) {
      const label = name(value);
      if (label === undefined) continue;
      const repeated = values.some(other => other !== value && shown(other) === label);
      names.set(value, repeated ? `${label} (${custom !== undefined ? String(value) : String(value ?? "unknown").slice(-8)})` : label);
    }
    return names;
  });
  const buckets = metric.buckets.map(bucket => {
    const presented = { ...bucket, keys: bucket.keys.map((key, index) => names[index]?.get(key) ?? key) };
    source.set(JSON.stringify(presented.keys), bucket);
    return presented;
  });
  return { metric: { ...metric, buckets }, sourceBucket: (bucket: MetricResult["buckets"][number]) => source.get(JSON.stringify(bucket.keys)) ?? bucket };
}

export const toolSummaryDimensions = ["day", "tool_name", "tool_category", "mcp_server", "program", "subcommand", "command_category", "channel", "machine_id", "execution_machine_id", "provider", "model", "effort", "repository_name", "session_kind", "source_kind"];
export function toolSummaryCallFilters(row: Record<string, unknown>): WhereTerm[] {
  return toolSummaryDimensions.map(field => {
    const value = row[field];
    return value === null || value === undefined || value === "" ? { field, op: "is_null", value: "" } : { field, op: "=", value: String(value) };
  });
}
