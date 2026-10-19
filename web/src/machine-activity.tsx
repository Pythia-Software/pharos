import React, { useEffect, useMemo, useRef, useState } from "react";
import type { WhereTerm } from "@pythia-software/query-table-core";
import { pushMachineLocation } from "./machine-location";
import { readMachineConcurrencySettings } from "./concurrency-settings";
import { preferences } from "./preferences";
import { ConcurrencyPanel } from "./concurrency";
import { MachineTime, type MachineCallsSurface } from "./machine-time";
import { exampleModels, exampleRepositories, machineDay, shiftMachineDay, machineWhere, machineWindow, type MachineScope } from "./machine-activity-data";
import "./machine-activity.css";

type MachineView = "tokens" | "parallelism" | "time";
const scopeKey = "pharos-machine-scope";
const views: Array<[MachineView, string, string]> = [
  ["tokens", "Machine Tokens", "What resources are agents consuming?"],
  ["parallelism", "Machine Parallelism", "How much work is happening concurrently?"],
  ["time", "Machine Time", "Where is agent time going?"],
];

function readLocation() {
  const params = new URLSearchParams(location.search);
  // A bookmarked token query carries its own population. Saved Machine Time
  // filters must not silently narrow a card’s destination. Explicit URL scopes
  // still win when the user deliberately included them.
  const tokenQuery = params.has("q_usage");
  const requested = params.get("machine_view") ?? (tokenQuery ? "tokens" : preferences().get("pharos-machine-view", "tokens"));
  const view: MachineView = requested === "time" || requested === "parallelism" ? requested : "tokens";
  const example = params.get("machine_demo") === "1";
  const saved = tokenQuery ? { days: 0, repository: "", provider: "", model: "" } : preferences().get<MachineScope>(scopeKey, { days: 0, repository: "", provider: "", model: "" });
  const days = Number(params.get("activity_days") ?? (example ? 14 : saved.days));
  return { view, example, scope: {
    days: example && !days ? 14 : [0, 7, 14, 30, 90].includes(days) ? days : 0,
    repository: params.get("activity_repo") ?? saved.repository ?? "",
    provider: params.get("activity_provider") ?? saved.provider ?? "",
    model: params.get("activity_model") ?? saved.model ?? "",
  } };
}

export function MachineActivity({ tokens, calls, parallelism }: { tokens: (where: WhereTerm[], applied: React.MutableRefObject<WhereTerm[]>, onTimeRangeChange: () => void) => React.ReactNode; calls: MachineCallsSurface; parallelism?: () => React.ReactNode }) {
  const [state, setState] = useState(readLocation);
  const { view, scope, example } = state;
  const appliedTokenScope = useRef<WhereTerm[]>([]);
  const [options, setOptions] = useState<Record<string, string[]>>({});
  const [optionsError, setOptionsError] = useState("");
  const window = useMemo(() => machineWindow(scope.days), [scope.days]);
  const tokenWhere = useMemo(() => machineWhere(scope, window, "last_usage_at"), [scope, window]);
  useEffect(() => {
    const url = new URL(location.href);
    const initial = readLocation();
    for (const [key, value] of Object.entries({ usage: "tokens", machine_view: initial.view, activity_days: String(initial.scope.days), activity_repo: initial.scope.repository, activity_provider: initial.scope.provider, activity_model: initial.scope.model })) url.searchParams.set(key, value);
    history.replaceState(history.state, "", url);
    globalThis.window.dispatchEvent(new Event("pharos:uri-changed"));
    const restore = () => setState(readLocation());
    globalThis.window.addEventListener("pharos:route", restore);
    return () => globalThis.window.removeEventListener("pharos:route", restore);
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    const fields = ["repository_name", "provider", "model"];
    Promise.all(fields.map(async field => {
      const response = await fetch(`/api/query/usage/distinct?field=${field}&limit=1000`, { signal: controller.signal });
      if (!response.ok) throw new Error("Filter choices could not be loaded. Saved selections still apply.");
      const result = await response.json() as { values: string[] };
      return [field, (result.values ?? []).map(item => String(item ?? "")).filter(Boolean)] as const;
    })).then(values => setOptions(Object.fromEntries(values))).catch(error => { if (!controller.signal.aborted) setOptionsError(error.message); });
    return () => controller.abort();
  }, []);
  function update(next: Partial<typeof state>) {
    const merged = { ...state, ...next };
    setState(merged);
    if (!merged.example) preferences().set(scopeKey, merged.scope);
    preferences().set("pharos-machine-view", merged.view);
    const url = new URL(location.href);
    const values = { machine_view: merged.view, machine_demo: merged.example ? "1" : "",
      activity_days: String(merged.scope.days), activity_repo: merged.scope.repository, activity_provider: merged.scope.provider, activity_model: merged.scope.model };
    for (const [key, value] of Object.entries(values)) url.searchParams.set(key, value);
    if (JSON.stringify(merged.scope) !== JSON.stringify(scope) || merged.example !== example) {
      const captured = readMachineConcurrencySettings();
      url.searchParams.set("q_machine_parallelism", JSON.stringify({ ...captured, zoom: null, workspace: "", agent: "" }));
      if (merged.example !== example) url.searchParams.delete("q_machine_calls");
    }
    pushMachineLocation(url);
  }
  const filter = (next: Partial<MachineScope>) => update({ scope: { ...scope, ...next } });
  const usingExample = example && view !== "tokens";
  const choices = (field: string, value: string) => [...new Set([value, ...(usingExample ? field === "repository_name" ? exampleRepositories : field === "provider" ? ["claude", "codex"] : exampleModels : options[field] ?? [])].filter(Boolean))].sort();
  return <section className="machine-activity" aria-label="Machine Activity">
    <div className="machine-view-tabs" role="group" aria-label="Machine Activity view">
      {views.map(([key, label, description]) => <button key={key} type="button" aria-pressed={view === key} className={view === key ? "active" : ""} onClick={() => update(key === "tokens" && example ? { view: key, example: false, scope: preferences().get<MachineScope>(scopeKey, { days: 0, repository: "", provider: "", model: "" }) } : { view: key })}><span>{label}</span><small>{description}</small></button>)}
    </div>
    <div className="machine-scope" role="group" aria-label="Machine Activity filters">
      <label>Period<select aria-label="Activity period" value={scope.days} onChange={event => filter({ days: Number(event.target.value) })}><option value="0" disabled={usingExample}>All recorded time</option>{[7, 14, 30, 90].map(days => <option key={days} value={days}>Last {days} complete days</option>)}</select></label>
      <label>Repository<select aria-label="Activity repository" value={scope.repository} onChange={event => filter({ repository: event.target.value })}><option value="">All repositories</option>{choices("repository_name", scope.repository).map(repository => <option key={repository}>{repository}</option>)}</select></label>
      <label>Provider<select aria-label="Activity provider" value={scope.provider} onChange={event => filter({ provider: event.target.value })}><option value="">All providers</option>{choices("provider", scope.provider).map(provider => <option key={provider}>{provider}</option>)}</select></label>
      <label>Model<select aria-label="Activity model" value={scope.model} onChange={event => filter({ model: event.target.value })}><option value="">All models</option>{choices("model", scope.model).map(model => <option key={model}>{model}</option>)}</select></label>
      {view !== "tokens" ? <label className="machine-data-source">Data<select aria-label="Activity data source" value={example ? "example" : "library"} onChange={event => update({ example: event.target.value === "example", scope: event.target.value === "example" ? { days: scope.days || 14, repository: "", provider: "", model: "" } : preferences().get<MachineScope>(scopeKey, { days: 0, repository: "", provider: "", model: "" }) })}><option value="library">Your library</option><option value="example">Example data</option></select></label> : null}
    </div>
    <div className="machine-scope-note"><span>{scope.days ? `${machineDay(window.start)} – ${shiftMachineDay(machineDay(window.end), -1)} · ${Intl.DateTimeFormat().resolvedOptions().timeZone}` : "All recorded time"} · Filters follow you between machine views.</span>{usingExample ? <span className="machine-example-label">Example data · synthetic activity</span> : null}</div>
    {optionsError && !usingExample ? <p className="muted">{optionsError}</p> : null}
    {view === "tokens" ? tokens(tokenWhere, appliedTokenScope, () => { if (scope.days) filter({ days: 0 }); }) : view === "time" ? <MachineTime scope={scope} window={window} example={example} onRepository={repository => filter({ repository })} calls={calls} /> : <div className="machine-parallelism"><ConcurrencyPanel scope={scope} activityWindow={window} example={example} />{!usingExample ? parallelism?.() : null}</div>}
  </section>;
}
