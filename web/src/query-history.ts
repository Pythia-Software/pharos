import type { QueryState } from "@pythia-software/query-table-core";

// Back and Forward step through a table's query edits, on pages that opt in.
// Query Table mirrors each edit into the address with replaceState, so the
// previous query would be lost; this keeps it as its own history entry.

// What a step is: filters, sorts, grouping, metrics, columns and paging.
// Column widths and fitting are presentation, not a place to go back to.
export function queryHistoryKey(query: QueryState): string {
  const { where, orderBy, grouping, aggregations, select, limit, offset } = query;
  return JSON.stringify({ where, orderBy, grouping: grouping ?? null, aggregations: aggregations ?? [], select: select.map(column => column.field), limit, offset });
}

type HistoryLike = Pick<History, "state" | "pushState" | "replaceState">;
const stateField = "pharosQuery";

export function createQueryHistory(dataset: string, history: HistoryLike, now = () => Date.now(), { coalesceMs = 1000 } = {}) {
  let settled: { key: string; query: QueryState } | null = null;
  let lastPush = -Infinity, restoring = false;
  const tagged = (key: string) => ({ ...history.state && typeof history.state === "object" ? history.state : {}, [stateField]: { dataset, key } });
  return {
    // The query the table shows once restoration is ready, and how to address
    // any query. Call it after every navigation too, even when the query did
    // not change, so the navigation settles. Returns true when the edit became
    // a new entry.
    observe(query: QueryState, url: (query: QueryState) => string): boolean {
      const key = queryHistoryKey(query);
      if (settled?.key === key) { settled.query = query; restoring = false; return false; }
      const previous = settled;
      settled = { key, query };
      // The first query, or one a navigation restored (however long that
      // took), belongs to its entry.
      if (!previous || restoring) {
        restoring = false;
        history.replaceState(tagged(key), "");
        return false;
      }
      // Rapid edits (typing, a run of clicks) are one step.
      if (now() - lastPush < coalesceMs) {
        history.replaceState(tagged(key), "", url(query));
        lastPush = now();
        return false;
      }
      history.replaceState(tagged(previous.key), "", url(previous.query));
      history.pushState(tagged(key), "", url(query));
      lastPush = now();
      return true;
    },
    // Back, Forward or an in-app link moved to another entry. Its query
    // arrives through restoration and is recorded, not pushed: the next query
    // observed ends the navigation. An entry this table tagged names its
    // query, so an unchanged restoration is no edit.
    navigated(state: unknown) {
      const entry = state && typeof state === "object" ? (state as Record<string, unknown>)[stateField] as { dataset?: string; key?: string } | undefined : undefined;
      lastPush = -Infinity;
      if (entry?.dataset === dataset && entry.key && entry.key === settled?.key) return;
      restoring = true;
    },
  };
}
