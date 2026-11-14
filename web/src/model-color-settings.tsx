import React, { useEffect, useRef, useState } from "react";
import { useLocationRevision } from "./nested-pages";
import { Icon } from "./icons";
import { colorForeground } from "./model-color-math";
import { customModelColor, editModelColorDrafts, effectiveModelColor, knownModels, loadModelColors, modelDateLabel, modelClass, modelPaletteComparison, modelPaletteGroups, modelProvider, modelProviders, previewModelColor, providerBrands, providerColorWarnings, rankedModelClasses, saveModelColors, useModelColorRevision, type ColorKind, type ModelPaletteEntry, type ModelProvider } from "./model-colors";
import "./project-colors.css";
import "./model-colors.css";

type Save = (changes: Record<string, string>) => Promise<boolean>;
// Only offer actions that would change something: Restore brand and Use
// recommended appear when the proposed color differs from the default, and
// the provider's Save and Discard changes when its palette has pending edits.
// An action that removes itself returns focus to the editor's picker.
function ColorEditor({ kind, name, label, detail, draft, setDraft, busy, canReset, reset, actions, picker }: { kind: "provider" | "model"; name: string; label: string; detail: string; draft: string; setDraft: (color: string) => void; busy: boolean; canReset: boolean; reset: () => void; actions?: React.ReactNode; picker?: React.RefObject<HTMLInputElement> }) {
  const custom = customModelColor(kind, name), own = useRef<HTMLInputElement>(null), input = picker ?? own;
  return <div className={`model-color-editor model-color-${kind}`}>
    <div className="model-color-name" title={`${label} · ${detail}`}><strong>{label}</strong><small className="muted">{detail} · {custom ? "Your color" : kind === "provider" ? "Brand default" : "Recommended shade"}</small></div>
    <div className="model-color-controls">
      <label className="model-color-picker" style={{background: draft, color: colorForeground(draft)}}><input ref={input} type="color" aria-label={`Color for ${label}`} value={draft} disabled={busy} onInput={event => setDraft(event.currentTarget.value)} onChange={event => setDraft(event.target.value)} /><Icon name="edit" /><span>Change color</span><code>{draft}</code></label>
      <div className="project-color-previews" aria-label="Color on light and dark backgrounds"><span className="project-color-light"><i style={{ background: draft }} />Light</span><span className="project-color-dark"><i style={{ background: draft }} />Dark</span></div>
      {actions}
      {canReset ? <button type="button" disabled={busy} onClick={() => { reset(); input.current?.focus({ preventScroll: true }); }} aria-label={`Restore ${kind === "provider" ? "brand color" : "recommended shade"} for ${label}`}>{kind === "provider" ? "Restore brand" : "Use recommended"}</button> : null}
    </div>
  </div>;
}

function PaletteBanner({ provider, groups, entries, changed, selected, select }: { provider: ModelProvider; groups: ReturnType<typeof modelPaletteGroups>; entries: ModelPaletteEntry[]; changed: boolean; selected: string | null; select: (name: string) => void }) {
  const columns = {gridTemplateColumns: `repeat(${entries.length}, minmax(0, 1fr))`};
  const controlID = (name: string) => `model-color-${provider}-${encodeURIComponent(name)}`;
  return <div className="model-palette-chart" data-density={entries.length > 8 ? "dense" : entries.length > 4 ? "medium" : "sparse"} role="group" aria-label={`${providerBrands[provider].label} model palette`}>
    <div className="model-palette-row model-palette-headings"><span /><div className="model-palette-cells" style={columns}>{groups.map(group => <div className="model-palette-group" key={group.name} style={{gridColumn: `span ${group.models.length}`}}><span title={group.name}>{group.name}</span></div>)}</div></div>
    {(["saved", ...(changed ? ["preview"] : [])] as const).map(row => <div className="model-palette-row" role="group" aria-label={`${providerBrands[provider].label} ${row} model palette`} key={row}>
      <span className="model-palette-row-name">{row === "saved" ? "Saved" : "Preview"}</span><div className="model-palette-cells" style={columns}>{entries.map(entry => {
        const color = row === "saved" ? entry.saved : entry.preview;
        return <button type="button" className="model-palette-swatch" id={row === "saved" ? controlID(entry.name) : undefined} key={entry.name} aria-label={`Edit ${row === "preview" ? "preview " : ""}color for ${entry.name}`} aria-pressed={selected === entry.name} onClick={() => select(entry.name)} style={{background: color, color: colorForeground(color)}} title={`${selected === entry.name ? "Click to close editor · " : ""}${entry.name} · ${modelDateLabel(entry.name)} · ${color}`}>{selected === entry.name ? <Icon name="edit" /> : null}</button>;
      })}</div>
    </div>)}
    <div className="model-palette-row model-palette-axis"><span /><div className="model-palette-cells" style={columns}>{entries.map(entry => <label className="model-palette-label" key={entry.name} htmlFor={controlID(entry.name)} title={`${entry.name} · ${modelDateLabel(entry.name)}`}>{entry.name}</label>)}</div></div>
  </div>;
}

function ProviderColorCard({ provider, busy, save }: { provider: ModelProvider; busy: boolean; save: Save }) {
  const brand = providerBrands[provider];
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [selected, setSelected] = useState<string | null>(null);
  const picker = useRef<HTMLInputElement>(null);
  const groups = modelPaletteGroups(knownModels().filter(model => modelProvider(model) === provider));
  const models = groups.flatMap(group => group.models);
  const providerDraft = drafts[`provider:${provider}`];
  const proposedBase = providerDraft === "" ? brand.color : providerDraft ?? effectiveModelColor("provider", provider);
  const edit = (kind: ColorKind, name: string, color: string) => setDrafts(previous => editModelColorDrafts(previous, kind, name, color, proposedBase));
  const differs = (color: string, fallback: string) => color.toLowerCase() !== fallback.toLowerCase();
  const saveDrafts = async () => {
    // The picker stays disabled until the busy state clears on the next render.
    if (await save(drafts)) { setDrafts({}); requestAnimationFrame(() => picker.current?.focus({ preventScroll: true })); }
  };
  const discard = () => { setDrafts({}); picker.current?.focus({ preventScroll: true }); };
  const dirty = Object.keys(drafts).length > 0, comparison = modelPaletteComparison(models, proposedBase, drafts);
  const warnings = providerColorWarnings(provider, proposedBase, comparison.entries);
  return <section className="model-provider-card" aria-label={`${brand.label} colors`}>
    <ColorEditor kind="provider" name={provider} label={brand.label} detail={`${models.length} models`} draft={proposedBase} setDraft={color => edit("provider", provider, color)} busy={busy} canReset={differs(proposedBase, brand.color)} reset={() => edit("provider", provider, "")} picker={picker} actions={dirty ? <>
      <button type="button" disabled={busy} onClick={() => void saveDrafts()} aria-label={`Save palette for ${brand.label}`}>Save</button>
      <button type="button" disabled={busy} onClick={discard}>Discard changes</button>
    </> : null} />
    {warnings.length ? <ul className="model-palette-warnings" role="status" aria-label={`${brand.label} color warnings`}>{warnings.map(warning => <li key={warning}>{warning}</li>)}</ul> : null}
    {models.length ? <PaletteBanner provider={provider} groups={groups} entries={comparison.entries} changed={comparison.changed} selected={selected} select={name => setSelected(previous => previous === name ? null : name)} /> : null}
    {selected ? <div className="model-inline-editor" role="group" aria-label={`Customize ${selected}`}>
      <ColorEditor kind="model" name={selected} label={selected} detail={[modelClass(selected), modelDateLabel(selected)].filter(Boolean).join(" · ")} draft={previewModelColor(selected, proposedBase, drafts[`model:${selected}`])} setDraft={color => edit("model", selected, color)} busy={busy} canReset={differs(previewModelColor(selected, proposedBase, drafts[`model:${selected}`]), previewModelColor(selected, proposedBase, ""))} reset={() => edit("model", selected, "")} />
    </div> : null}
  </section>;
}

export function ModelColorSettings() {
  useModelColorRevision();
  useLocationRevision();
  const visible = location.pathname === "/settings/models";
  const hierarchy = modelProviders.flatMap(provider => {
    const classes = rankedModelClasses(provider);
    return classes.length ? [`${providerBrands[provider].label} ${classes.join(" → ")}`] : [];
  }).join("; ");
  const [error, setError] = useState(""), [message, setMessage] = useState(""), [busy, setBusy] = useState(false);
  useEffect(() => { if (!visible) return; let active = true; void loadModelColors(true).catch(error => { if (active) setError(error.message); }); return () => { active = false; }; }, [visible]);
  async function save(changes: Record<string, string>) {
    setBusy(true); setError(""); setMessage("");
    try { await saveModelColors(changes); setMessage("Palette saved to your library."); return true; }
    catch (error) { setError(error instanceof Error ? error.message : String(error)); return false; }
    finally { setBusy(false); }
  }
  return <div className="model-color-settings">
    <p className="muted">Each provider has a color family, with distinct shades for model classes and smaller variations for their versions. Your palette stays consistent across charts, cards, and dashboards and saves with your library. Select a model’s swatch in either row or its name to open or close its editor. After you change a palette, Save applies its pending changes and Discard changes clears them.</p>
    <p className="model-color-note">For contemporary releases, core classes deepen in order: {hierarchy}. Older versions are lighter within each class. Dates use the pricing catalog or the first recorded use of an unnumbered label. Defaults can rebalance as models arrive. Changing a provider color shifts its automatic palette; your individual choices stay yours.</p>
    <div role="status" aria-live="polite">{message}</div>
    {error ? <p role="alert">{error}</p> : null}
    <div className="model-provider-list">{modelProviders.filter(provider => knownModels().some(model => modelProvider(model) === provider) || customModelColor("provider", provider)).map(provider => <ProviderColorCard key={provider} provider={provider} busy={busy} save={save} />)}</div>
  </div>;
}
