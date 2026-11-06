// A card cover that is a grid of shaded cells, as the dashboards' activity
// grids and heatmaps draw them: rows × columns, each cell an intensity. It is
// plain data, so a finished cover can be kept in the preview cache.
export type GridCover = {
  rows: string[];
  columns: string[];
  // Row-major. Sequential cells run 0–1; diverging cells −1–1, below and
  // above a reference. null is a cell without data.
  cells: (number | null)[][];
  // Each cell's value as the page words it, for its tooltip.
  values: (string | null)[][];
  tone: "sequential" | "diverging";
  legend: [string, string];
  // Which rows and columns are labelled.
  rowTicks: number[];
  columnTicks: number[];
  caption?: string;
};

// Evenly spaced labels, first and last included, at most count of them.
export function gridTicks(length: number, count: number): number[] {
  if (length <= count) return Array.from({ length }, (_, i) => i);
  const step = (length - 1) / (count - 1);
  return [...new Set(Array.from({ length: count }, (_, i) => Math.round(i * step)))];
}

// Cells touch once they are too many to separate legibly.
export const gridGaps = (cover: Pick<GridCover, "rows" | "columns">) => ({ row: cover.rows.length > 30 ? 0 : 1, column: cover.columns.length > 30 ? 0 : 1 });

// The cover's height: rows get room to read, up to what a card holds, plus
// the column labels and the key. The card's own head and padding add 41 px.
export const gridCoverHeight = (cover: Pick<GridCover, "rows">) => Math.round(Math.min(190, Math.max(110, cover.rows.length * 11))) + 14 + 16 + 41;
