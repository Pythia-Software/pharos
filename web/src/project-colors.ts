import type { MetricTheme } from "@pythia-software/query-table-ui";
import { useSyncExternalStore } from "react";

export type ProjectColor = {
  id: string; key: string; name: string; kind: string;
  default_color: string; custom_color?: string; source: string;
  /** Conversations across the project's workspaces, and the latest one's end (or start). */
  conversations?: number; latest_conversation_at?: string;
};
declare global { interface Window { pharosProjectColors?: ProjectColor[] } }
export const projectColorFields = ["project_name", "root_project_name", "repository_name", "root_repository_name", "project_id", "root_project_id", "repository_id", "root_repository_id"];
export const noProjectColor = "#85909b";
const validColor = (value: unknown): value is string => typeof value === "string" && /^#[0-9a-f]{6}$/i.test(value);
export const effectiveProjectColor = (project: ProjectColor) => validColor(project.custom_color) ? project.custom_color : project.default_color;

// Provisional colors (e.g. a newly indexed project before palette refresh)
// meet the same light/dark mark-contrast target as saved defaults.
export function fitProjectColor(color: string): string {
  const channels = [1, 3, 5].map(at => Number.parseInt(color.slice(at, at + 2), 16) / 255).map(value => value <= .04045 ? value / 12.92 : ((value + .055) / 1.055) ** 2.4);
  const luminance = channels.reduce((sum, channel, index) => sum + channel * [.2126, .7152, .0722][index], 0);
  return "#" + channels.map(value => {
    const linear = luminance > .21 ? value * .21 / luminance : value + (1 - value) * (.21 - luminance) / (1 - luminance);
    const encoded = linear <= .0031308 ? linear * 12.92 : 1.055 * linear ** (1 / 2.4) - .055;
    return Math.round(encoded * 255).toString(16).padStart(2, "0");
  }).join("");
}

// Names are an adapter for name-grouped metric results. A name shared by
// several projects takes the color of the one with the most conversations
// (ties go to the lower ID), so the combined group keeps a saved color.
// ID-valued results always resolve exactly.
const conversationCount = (project: ProjectColor) => Number.isFinite(project.conversations) ? Math.max(0, project.conversations!) : 0;
const leads = (project: ProjectColor, other: ProjectColor) => conversationCount(project) > conversationCount(other) || conversationCount(project) === conversationCount(other) && project.id < other.id;

export function projectColorAliases(projects: ProjectColor[]): Map<string, ProjectColor> {
  const aliases = new Map<string, ProjectColor>();
  for (const project of projects) for (const alias of [project.name, ...(project.kind === "repository" ? [project.id.slice("repository:".length)] : [])]) {
    const holder = aliases.get(alias);
    if (!holder || leads(project, holder)) aliases.set(alias, project);
  }
  for (const project of projects) aliases.set(project.id, project);
  return aliases;
}

let records: ProjectColor[] = [];
let aliases = projectColorAliases(records);
let revision = 0;
const listeners = new Set<() => void>();
const subscribe = (listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener); }; };
export function useProjectColorRevision() { return useSyncExternalStore(subscribe, () => revision); }
const aliasProperty = (value: string) => `--project-value-${Array.from(value, character => character.codePointAt(0)!.toString(16).padStart(6, "0")).join("")}`;
export function projectColors(): ProjectColor[] { return records; }
export function installProjectColors(projects: ProjectColor[]) {
  const next = projects.filter(project => project && typeof project.id === "string" && /^[a-f0-9]{64}$/.test(project.key) && validColor(project.default_color));
  if (JSON.stringify(next) === JSON.stringify(records)) return;
  if (typeof document !== "undefined") for (const [name] of aliases) document.documentElement.style.removeProperty(aliasProperty(name));
  records = next;
  aliases = projectColorAliases(records);
  if (typeof document !== "undefined") for (const project of records) {
    document.documentElement.style.setProperty(`--project-color-${project.key}`, effectiveProjectColor(project));
  }
  if (typeof document !== "undefined") for (const [name] of aliases) document.documentElement.style.setProperty(aliasProperty(name), savedProjectColor(name)!);
  revision++;
  for (const listener of listeners) listener();
}
if (typeof window !== "undefined") installProjectColors(window.pharosProjectColors ?? []);

export function savedProjectColor(value: string): string | undefined {
  const project = aliases.get(value);
  if (project) return `var(--project-color-${project.key})`;
  return !value || ["No project", "Unspecified"].includes(value) ? noProjectColor : undefined;
}

export function projectSeriesColor(value: string, fallback: string): string {
  return savedProjectColor(value) ?? `var(${aliasProperty(value)}, ${fallback})`;
}

// Query Table's public theme API also covers user-authored metric panels.
// Multi-key defaults use a project only when no stronger semantic key is set
// by the caller (e.g. a model family or an agent-event kind).
export function projectMetricTheme(): MetricTheme {
  const overrides: Array<{ value: string | null; color: string }> = [...aliases].map(([value]) => ({ value, color: savedProjectColor(value)! }));
  for (const value of ["", "No project", "Unspecified", null]) if (!aliases.has(value ?? "")) overrides.push({ value, color: noProjectColor });
  return { colorBy: projectColorFields, tokens: { null: noProjectColor, other: noProjectColor }, dimensions: Object.fromEntries(projectColorFields.map(field => [field, { layer: "projects", overrides }])) };
}

let pending: Promise<ProjectColor[]> | undefined;
export function loadProjectColors(): Promise<ProjectColor[]> {
  return pending ??= (async () => {
    const started = revision;
    const response = await fetch("/api/project-colors");
    if (!response.ok) throw new Error(`Project colors could not be loaded (${response.status}).`);
    const projects = await response.json() as ProjectColor[];
    // An older in-flight read cannot undo a successful edit.
    if (started === revision) installProjectColors(projects);
    return records;
  })().finally(() => { pending = undefined; });
}

export async function saveProjectColor(id: string, color: string): Promise<ProjectColor[]> {
  const response = await fetch("/api/project-colors", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id, color }) });
  if (!response.ok) throw new Error("Project color could not be saved. Try again.");
  const projects = await response.json() as ProjectColor[];
  installProjectColors(projects);
  return records;
}
