import React, { useEffect, useState } from "react";
import { EMPTY_QUERY, type FieldSchema, type QueryState, type WhereTerm } from "@pythia-software/query-table-core";

// Empty columns/order use the source schema defaults, including host-added
// detail columns. Never reuse a named baseline or a grouped result schema.
export function tableRecoveryQuery(schema: FieldSchema, scopeWhere: WhereTerm[] = []): QueryState {
  return { ...EMPTY_QUERY, where: [...scopeWhere], limit: schema.defaultLimit ?? EMPTY_QUERY.limit };
}

// Explicit route options bypass all saved Analyze tabs and machine filters. The
// storage adapter's fresh flag also bypasses last-used and named default queries.
// Saved views stay available in Saved; a reset never deletes them.
export function freshPageURL(href: string): string {
  const url = new URL(href);
  url.search = "fresh=1";
  url.hash = "";
  if (url.pathname === "/analyze") {
    for (const [key, value] of Object.entries({ analyze_mode: "dashboards", analyze_topic: "tokens" })) url.searchParams.set(key, value);
  }
  return `${url.pathname}${url.search}`;
}

export function ResetPage() {
  return <a className="query-reset-page" href={freshPageURL(location.href)}>Reset page</a>;
}

// A renderer exception otherwise unmounts the entire React page, including its
// reset controls. Keep a recovery action outside the failed component tree.
export class PageErrorBoundary extends React.Component<{ children: React.ReactNode }, { error: Error | null }> {
  state: { error: Error | null } = { error: null };
  static getDerivedStateFromError(failure: unknown) {
    return { error: failure instanceof Error ? failure : new Error(String(failure)) };
  }
  render() {
    if (!this.state.error) return this.props.children;
    return <section className="query-recovery" role="alert">
      <span>This page could not be displayed. {this.state.error.message}</span>
      <button type="button" onClick={() => this.setState({ error: null })}>Try again</button>
      <ResetPage />
    </section>;
  }
}

// A slow or failed query offers Retry and Reset. Query Table keeps such a
// query out of the last-used slot (persistLast: "succeeded"), so a bare reload
// returns to the last query that loaded.
export function QueryRecovery({ busy, error, onRetry, onReset, identity }: { busy: boolean; error?: Error | null; onRetry: () => void; onReset: () => void; identity: string }) {
  const [slow, setSlow] = useState(false);
  useEffect(() => {
    setSlow(false);
    if (!busy) return;
    const timer = setTimeout(() => setSlow(true), 12_000);
    return () => clearTimeout(timer);
  }, [busy, identity]);
  if (!slow && !error) return null;
  return <div className="query-recovery" role="status">
    <span>{error ? error.message : "This query is taking longer than expected."}</span>
    {error ? <button type="button" onClick={onRetry}>Try again</button> : null}
    <button type="button" onClick={onReset}>Reset table</button>
    <ResetPage />
  </div>;
}
