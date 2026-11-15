// Settings are part of the executable query, not merely explanatory URL text.
// Only the dataset's reviewed request parameter may travel to its transport.
const parameters: Record<string, string> = { usage: "carbon_settings", rhythm: "habits_settings", parallelism: "parallelism_settings" };
export function dashboardQuerySettings(dataset: string, params: URLSearchParams): Record<string, string> {
  const encoded = params.get("analyze_query_settings"), name = parameters[dataset];
  if (!encoded || !name) return {};
  const value = JSON.parse(encoded);
  if (!value || typeof value !== "object" || Array.isArray(value) || Object.values(value).some(value => typeof value !== "string")) throw new Error("Invalid dashboard query settings.");
  return typeof value[name] === "string" ? { [name]: value[name] } : {};
}
export function dashboardQuerySettingsSnapshot(params?: Record<string, string>): string | undefined {
  const saved = Object.fromEntries(Object.values(parameters).flatMap(name => params?.[name] ? [[name, params[name]]] : []));
  return Object.keys(saved).length ? JSON.stringify(saved) : undefined;
}
