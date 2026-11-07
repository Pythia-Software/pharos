import {shortLabel,stateColor} from "./conversation-labels";
import catalog from "../../pricing/model_colors.json";
import { activities, activityPresentation } from "./machine-activity-data";
import { orderedValues } from "./session-shape-bands";
import agentEventKinds from "./agent-event-kinds.json";

// Agent event groups take the reader's event hues (ui.py's --event-* tokens):
// friction red, learning ochre, second look green, choices blue. A kind is a
// shade of its group's hue, darkest first.
const eventGroupHues: Record<string, string> = { Friction: "var(--event-friction)", "Reactions and learning": "var(--event-learning)", "Worth a second look": "var(--event-second-look)", "Choices you may not see": "var(--event-choices)" };
const eventKindStyles = new Map<string, { color: string; group: string; order: string }>(agentEventKinds.kinds.flatMap(kind => {
  const siblings = agentEventKinds.kinds.filter(other => other.group === kind.group), index = siblings.indexOf(kind);
  const style = { color: `color-mix(in srgb,${eventGroupHues[kind.group]} ${100 - Math.round(55 * index / Math.max(1, siblings.length - 1))}%,var(--bg))`, group: kind.group,
    order: `${agentEventKinds.groups.indexOf(kind.group)}:${String(index).padStart(2, "0")}` };
  return [[kind.key, style], [kind.label, style]] as [string, typeof style][];
}));

type Provider = keyof typeof catalog.providers;
type ModelStyle = { provider: Provider; class: string; color: string };
type SourceStyle = { provider: Provider; color: string };
export type SeriesStyle = { color: string; group?: string; groupPath?: string[]; order?: string };
const models = catalog.models as Record<string, ModelStyle>;
const sources = catalog.sources as Record<string, SourceStyle>;
const providerOrder: Provider[] = ["anthropic", "google", "openai", "other"];
export const otherSeriesColor = "#85909b";
const allocated = new Map<string, string>();
const reserved = new Set([otherSeriesColor, ...Object.values(orderedValues).flatMap(ordered => ordered.colors),...Object.values(models).map(model => model.color), ...Object.values(sources).map(source => source.color), ...Object.values(catalog.providers).map(provider => provider.color)]);

export function modelColorKey(name: string): string {
  return name.trim().toLowerCase().replace(/^claude-/, "").replace(/(-1m|\[1m\])$/, "").replace(/-\d{8}$/, "");
}

function modelProvider(name: string): Provider {
  if (/^(opus|sonnet|haiku|fable)(-|$)/.test(name)) return "anthropic";
  if (/^(gemini|gemma)(-|$)/.test(name)) return "google";
  if (/^(gpt|chatgpt|codex|o[134])(-|$)/.test(name)) return "openai";
  return "other";
}

function hashName(name: string): number {
  let hash = 2166136261;
  for (const character of name) hash = Math.imul(hash ^ character.charCodeAt(0), 16777619);
  return hash >>> 0;
}

function hexColor(hue: number, saturation: number, lightness: number): string {
  const amplitude = saturation * Math.min(lightness, 1 - lightness);
  const channel = (offset: number) => {
    const position = (offset + hue / 30) % 12;
    const value = lightness - amplitude * Math.max(-1, Math.min(position - 3, 9 - position, 1));
    return Math.round(255 * value).toString(16).padStart(2, "0");
  };
  return `#${channel(0)}${channel(8)}${channel(4)}`;
}

function generatedColor(key: string, provider?: Provider): string {
  const existing = allocated.get(key);
  if (existing) return existing;
  for (let attempt = 0; ; attempt++) {
    const hash = hashName(`${key}:${attempt}`);
    const hue = provider ? (catalog.providers[provider].hue + (hash % 17) - 8 + 360) % 360 : hash % 360;
    const color = hexColor(hue, (55 + ((hash >>> 8) % 30)) / 100, (35 + ((hash >>> 16) % 31)) / 100);
    if (reserved.has(color)) continue;
    reserved.add(color);
    allocated.set(key, color);
    return color;
  }
}

// The sky at each local hour: night blues from 22:00 to 04:00, a red, orange
// and yellow dawn, day blues deepest at noon, then a yellow, orange and red
// dusk. Night shades lighten in dark mode so they stay visible on its panels.
const night = (light: string, dark: string) => `light-dark(${light}, ${dark})`;
export const hourOfDayColors = [
  night("#1b2549", "#3d4a86"), night("#161e3d", "#36427a"), night("#11172f", "#303b6f"), night("#151c38", "#343f76"), night("#202b54", "#40508f"),
  "#c8433a", "#e57f35", "#f0c23a",
  "#a3cde9", "#89bfe7", "#6daee2", "#5299db", "#3d86d0", "#4891d6", "#579cdb", "#69a9df", "#7db5e2", "#93c2e5",
  "#e3cf86", "#efb52f", "#de6f33", "#b53a3c",
  night("#2a3768", "#4a5a9a"), night("#222d5a", "#43528f"),
];
export const hourOfDayColor = (hour: number) => Number.isInteger(hour) && hour >= 0 && hour < 24 ? hourOfDayColors[hour] : otherSeriesColor;

// Scopes whose series follow their palette's order rather than their size:
// legends, stacks and slices then read lowest to highest.
const sortedScopes = new Set(["level_band", "model_family", "provider", "effort", "model_effort", ...Object.keys(orderedValues)]);
export const orderedScope = (scope: string) => sortedScopes.has(scope.startsWith("root_") ? scope.slice(5) : scope);
// Scopes whose values are steps of one scale (a ramp, low to high), so
// slices and bars keep that order too, unlike models or providers.
export const steppedScope = (scope: string) => scope === "effort" || scope === "level_band" || Object.hasOwn(orderedValues, scope);
export function chartSeriesStyle(scope: string, name: string): SeriesStyle {
  if (scope === "function_role") return {color:stateColor(name === "Sender" ? "P" : name === "Receiver" ? "C" : "?"),order:name === "Sender" ? "0" : name === "Receiver" ? "1" : "2"};
  if(["label","family"].includes(scope)&&(name==="?"||/^[PC](\.|$)/.test(name))){
    const group=name.startsWith("P")?"Sender":name.startsWith("C")?"Receiver":"Coverage",parts=name.split('.');
    return {color:stateColor(name),order:(name.startsWith("P")?"0":name.startsWith("C")?"1":"2")+name,group,
      groupPath:[group,...scope==='label'?parts.slice(1,-1).map((_,index)=>shortLabel(parts.slice(0,index+2).join('.'))):[]]};
  }
  if (scope.startsWith("root_")) scope = scope.slice(5);
  if (scope === "family_group" && Object.hasOwn(eventGroupHues, name)) return { color: eventGroupHues[name], order: String(agentEventKinds.groups.indexOf(name)) };
  if (scope === "family" && eventKindStyles.has(name)) return eventKindStyles.get(name)!;
  // Daily covers key series by their displayed label; Machine Time's palette applies to either.
  if (scope === "hour_of_day") return { color: hourOfDayColor(Number.parseInt(name, 10)), order: name };
  if (scope === "activity") return { color: (activities.find(item => item.label === name) ?? activityPresentation(name)).color, order: name };
  if (scope === "model_effort") {
    const separator = name.lastIndexOf(" · ");
    const model = separator < 0 ? name : name.slice(0, separator);
    const effort = separator < 0 ? "" : name.slice(separator + 3);
    const modelStyle = chartSeriesStyle("model_family", model), effortStyle = chartSeriesStyle("effort", effort);
    return { color: generatedColor(`${scope}:${name}`, modelProvider(modelColorKey(model))), group: modelStyle.group, order: `${modelStyle.order}:${effortStyle.order}` };
  }
  if (scope === "effort") {
    const levels = ["none", "minimal", "low", "medium", "high", "xhigh", "ultra", "max"];
    const colors = ["#eee7fa", "#dac9f2", "#c3a7e7", "#a982d8", "#8d60c3", "#7443a9", "#5c2e8c", "#45206d"];
    const index = levels.indexOf(name.toLowerCase());
    return { color: index < 0 ? otherSeriesColor : colors[index], order: `${index < 0 ? 99 : index}:${name}` };
  }
  if (Object.hasOwn(orderedValues, scope)) {
    // Ordered values, by value or by their label, take their step of a ramp.
    const ordered = orderedValues[scope], index = ordered.order.findIndex(value => value === name || ordered.labels[value] === name);
    return index < 0 ? { color: otherSeriesColor, order: `99:${name}` } : { color: ordered.colors[index], order: String(index) };
  }
  if (scope === "level_band") {
    // Agents active at once, named by their displayed label ("2 at once"). The busiest band is the
    // bottom of each stack and the reddest, so colour and height both rise with concurrency.
    const level = Number.parseInt(name, 10), colors = ["#f7b77e", "#ee8272", "#e0668f", "#d6403a"];
    return Number.isInteger(level) && level >= 1 && level <= 4 ? { color: colors[level - 1], order: String(5 - level) } : { color: otherSeriesColor, order: "9" };
  }
  if (scope === "model_family") {
    const key = modelColorKey(name), model = Object.hasOwn(models, key) ? models[key] : undefined, provider = model?.provider ?? modelProvider(key);
    const modelClass = model?.class ?? (/codex/.test(key) ? "Codex" : /gemini.*flash/.test(key) ? "Flash" : /gemini.*pro/.test(key) ? "Pro" : key.split(/[-.]/)[0]);
    return {
      color: model?.color ?? generatedColor(`${scope}:${key}`, provider),
      group: catalog.providers[provider].label,
      order: `${providerOrder.indexOf(provider)}:${modelClass}:${key}`,
    };
  }
  if (scope === "provider") {
    const source = Object.hasOwn(sources, name) ? sources[name] : undefined;
    const provider = source?.provider ?? (Object.hasOwn(catalog.providers, name) ? name as Provider : "other");
    return {
      color: source?.color ?? generatedColor(`${scope}:${name}`, provider),
      group: catalog.providers[provider].label,
      order: `${providerOrder.indexOf(provider)}:${name}`,
    };
  }
  return { color: generatedColor(`${scope}:${name}`) };
}

// The fields whose palettes color a clause's groups. An hour-of-day date
// grouping keys its first group by local hour, whatever field it reads.
export function colorFields(clause: { groupBy: string[]; dateGrouping?: { interval: string } }): string[] {
  return clause.dateGrouping?.interval === "hourOfDay" ? ["hour_of_day", ...clause.groupBy.slice(1)] : clause.groupBy;
}

// Compound metric groups retain their model palette even when their first key
// is provider, repository, or a display label flattened for the native renderer.
export function metricSeriesColor(fields: string[], keys: unknown[]): string {
  const field = ["root_model_family", "model_family", "root_effort", "effort", "root_provider", "provider", "repository_name"].find(field => fields.includes(field)) ?? fields[0];
  return chartSeriesStyle(field ?? "generic", String(keys[fields.indexOf(field)] ?? "Unspecified")).color;
}
