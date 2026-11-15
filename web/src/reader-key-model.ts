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

// A category with the same members as the action has no distinct choices.
export function readerLineScopes(values: string[], category?: { label: string; values: string[] }) {
  const action = new Set(values), group = new Set(category?.values ?? []);
  const scopes = [{ label: "Specific activity", values }];
  if (category && group.size && (group.size !== action.size || [...group].some(value => !action.has(value)))) {
    scopes.push({ label: `Category: ${category.label}`, values: category.values });
  }
  return scopes;
}

export function readerLineOptionLabel(action: LegendAction, category: string | undefined, visible: number, hidden: number) {
  const target = category ? `the ${category} category` : "this activity";
  if (action === "only") return `Show only ${target}`;
  if (action === "show") return visible && hidden ? `Show ${hidden} hidden ${hidden === 1 ? "label" : "labels"} in ${target}` : `Show ${target}`;
  if (action === "hide") return visible && hidden ? `Hide ${visible} visible ${visible === 1 ? "label" : "labels"} in ${target}` : `Hide ${target}`;
  return "Show All";
}
