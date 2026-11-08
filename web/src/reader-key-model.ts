import type { LegendAction } from "./chart-selection";

/** Reader keys are explicit visibility, so removing the last key hides all. */
export function updateReaderKeys(all: string[], shown: string[], values: string[], action: LegendAction): string[] {
  const selected = new Set(shown), group = new Set(values);
  if (action === "clear") return [...all];
  if (action === "only") return all.filter(value => group.has(value));
  if (action === "exclude") return all.filter(value => !group.has(value));
  const remove = action === "hide" || action !== "show" && values.every(value => selected.has(value));
  return all.filter(value => group.has(value) ? !remove : selected.has(value));
}
export function readerKeyState(all: string[], shown: string[], values: string[]) {
  const selected = new Set(shown), count = values.filter(value => selected.has(value)).length;
  const selectionActive = all.some(value => !selected.has(value));
  return { active: selectionActive && count === values.length, mixed: count > 0 && count < values.length,
    dimmed: count === 0, selectionActive, soleSelection: false, visibleCount: count, hiddenCount: values.length - count };
}
