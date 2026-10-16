import React from "react";
import type { AggregationClause, AggregationResultEntry } from "@pythia-software/query-table-core";
import { metricSeriesColor, otherSeriesColor } from "./chart-colors";

type Role = "root" | "singleton" | "parent" | "subagent";
const roleLabels: Record<Role, string> = { root: "Root", singleton: "Singleton", parent: "Parent", subagent: "Subagent" };

// Distinct-session metrics are reduced before role shares. Preserve old saved
// two-role views while the revised definition splits roots into two categories.
export function stackedRoleRows(clause: AggregationClause, result: AggregationResultEntry, splitBy: string) {
  const roles: Role[] = splitBy === "session_kind" ? ["root", "subagent"] : ["singleton", "parent", "subagent"];
  const split = clause.groupBy.indexOf(splitBy), fields = clause.groupBy.filter((_, i) => i !== split);
  if (split < 0 || result.error || result.coverage === "partial" || result.other || (result.groupCount ?? result.buckets.length) > result.buckets.length) return;
  const rows = new Map<string, { key: string; keys: unknown[]; root: number; singleton: number; parent: number; subagent: number; total: number }>();
  for (const bucket of result.buckets) {
    const role = bucket.keys[split] as Role;
    if (bucket.error || typeof bucket.value !== "number" || !Number.isFinite(bucket.value) || bucket.value < 0 || !roles.includes(role)) return;
    const keys = bucket.keys.filter((_, i) => i !== split), key = JSON.stringify(keys);
    const row = rows.get(key) ?? { key, keys, root: 0, singleton: 0, parent: 0, subagent: 0, total: 0 };
    row[role] += bucket.value;
    row.total += bucket.value;
    rows.set(key, row);
  }
  return { fields, roles, rows: [...rows.values()].filter(row => row.total > 0).sort((a, b) => b.total - a.total || a.key.localeCompare(b.key)) };
}

export function CompactStackedBars({ clause, result, splitBy, budget }: { clause: AggregationClause; result: AggregationResultEntry; splitBy: string; budget: number }) {
  const chart = stackedRoleRows(clause, result, splitBy);
  if (!chart) return <span className="query-pie-empty">Role shares require complete session counts.</span>;
  const percent = (value: number) => value.toLocaleString(undefined, { style: "percent", maximumFractionDigits: 1 });
  return <div className="query-role-bars">
    {chart.rows.slice(0, budget).map(row => {
      const label = row.keys.map(key => String(key ?? "Unspecified")).join(" · "), color = metricSeriesColor(chart.fields, row.keys);
      const paint = (role: Role) => role === "root" || role === "singleton" ? otherSeriesColor : role === "parent" ? `color-mix(in srgb, ${color} 55%, white)` : color;
      return <div className="query-role-row" key={row.key} data-total={row.total}>
        <div className="query-role-label"><span>{label}</span><small>{row.total.toLocaleString()} sessions</small></div>
        <div className="query-role-track">{chart.roles.map(role => {
          const share = row[role] / row.total;
          return <i key={role} className="qt-metric-mark" data-role={role} data-share={share} style={{ width: `${share * 100}%`, background: paint(role) }} data-chart-colors={JSON.stringify([paint(role)])} data-chart-tooltip={`${label}\n${roleLabels[role]}: ${percent(share)}\n${row[role].toLocaleString()} of ${row.total.toLocaleString()} active sessions`} />;
        })}</div>
        <div className="query-role-values">{chart.roles.map(role => <span key={role}><i style={{ background: paint(role) }} />{roleLabels[role]} {percent(row[role] / row.total)}</span>)}</div>
      </div>;
    })}
    {chart.rows.length > budget ? <small className="query-box-key">{budget} of {chart.rows.length} models · ranked by active sessions</small> : null}
  </div>;
}
