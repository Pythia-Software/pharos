export const safetyViews = ["", "table", "settings"] as const;
export type SafetyView = typeof safetyViews[number];
export const safetyStateLabels = { open: "Open", handed_off: "Handed off", dismissed: "Dismissed" };
export function safetyURL(view: SafetyView = ""): string {
  return `/findings?${new URLSearchParams({ optimize_area: "safety", ...(view ? { view } : {}) })}`;
}
export function safetyDetailURL(id: string, current?: URL): string {
  const url = current ? new URL(current.href) : new URL(safetyURL(), "http://localhost");
  url.searchParams.set("optimize_area", "safety");
  url.searchParams.set("safety_case", id);
  return url.pathname + url.search;
}
