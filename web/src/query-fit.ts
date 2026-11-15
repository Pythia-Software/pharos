import type { QueryState } from "@pythia-software/query-table-core";

// Match the native Fit All to Screen action. Explicit widths are pinned in
// 1.0.0; fresh fitted views must release them, including legacy sizing state.
export function fitQueryToScreen(query: QueryState): QueryState {
  const { fitToScreen: _legacy, ...state } = query as QueryState & { fitToScreen?: boolean };
  return { ...state, select: query.select.map(({ width: _width, ...column }) => column), fit: "screen" };
}
