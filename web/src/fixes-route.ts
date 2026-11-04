// Optimize → Fixes: recurring agent problems a change to guidance, the
// environment, a tool, setup or docs could prevent.
export const fixesViews = ["", "settings"] as const;
export type FixesView = typeof fixesViews[number];
export const fixesStateLabels = { open: "Open", handed_off: "Handed off", dismissed: "Closed" };
export function fixesURL(view: FixesView = ""): string {
  return `/findings?${new URLSearchParams({ optimize_area: "fixes", ...(view ? { view } : {}) })}`;
}
export function fixDetailURL(id: string): string {
  return `/findings?${new URLSearchParams({ optimize_area: "fixes", fixes_item: id })}`;
}
