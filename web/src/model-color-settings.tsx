import React, { useEffect, useState } from "react";
import { useLocationRevision } from "./nested-pages";
import { customModelColor, effectiveModelColor, knownModels, loadModelColors, modelCatalogDate, modelClass, modelPaletteComparison, modelPaletteGroups, modelProvider, modelProviders, previewModelColor, providerBrands, providerColorWarnings, rankedModelClasses, saveModelColor, shadeChoices, useModelColorRevision, type ColorKind, type ModelPaletteEntry, type ModelProvider } from "./model-colors";
import "./project-colors.css";
import "./model-colors.css";

type Save = (kind: ColorKind, key: string, color: string) => Promise<boolean>;
function ColorEditor({ kind, name, label, detail, base, draft, setDraft, busy, save }: { kind: "provider" | "model"; name: string; label: string; detail: string; base: string; draft: string; setDraft: (color: string) => void; busy: boolean; save: Save }) {
  const color = effectiveModelColor(kind, name), custom = customModelColor(kind, name);
  return <div className={`model-color-editor model-color-${kind}`}>
    <div className="model-color-name"><strong>{label}</strong><small className="muted">{detail} · {custom ? "Your color" : kind === "provider" ? "Brand default" : "Recommended shade"}</small></div>
    <div className="model-color-controls">
      <label className="project-color-picker"><input type="color" aria-label={`Color for ${label}`} value={draft} disabled={busy} onInput={event => setDraft(event.currentTarget.value)} onChange={event => setDraft(event.target.value)} /><code>{draft}</code></label>
      {kind === "model" ? <div className="model-shade-choices" role="group" aria-label={`Suggested shades for ${label}`}>{shadeChoices(base).map(shade => <button key={shade} type="button" disabled={busy} aria-label={`Try ${shade} for ${label}`} aria-pressed={draft === shade} title={shade} style={{ background: shade }} onClick={() => setDraft(shade)} />)}</div> : null}
      <div className="project-color-previews" aria-label="Color on light and dark backgrounds"><span className="project-color-light"><i style={{ background: draft }} />Light</span><span className="project-color-dark"><i style={{ background: draft }} />Dark</span></div>
      <button type="button" disabled={busy || draft === color} onClick={() => void save(kind, name, draft)} aria-label={`Save color for ${label}`}>Save color</button>
      <button type="button" disabled={busy || !custom} onClick={() => void save(kind, name, "")} aria-label={`Restore ${kind === "provider" ? "brand color" : "recommended shade"} for ${label}`}>{kind === "provider" ? "Restore brand" : "Use recommended"}</button>
    </div>
  </div>;
}

function PaletteBanner({ provider, groups, entries, changed, selected, select }: { provider: ModelProvider; groups: ReturnType<typeof modelPaletteGroups>; entries: ModelPaletteEntry[]; changed: boolean; selected: string | null; select: (name: string) => void }) {
  const columns = {gridTemplateColumns: `repeat(${entries.length}, minmax(0, 1fr))`};
  const controlID = (name: string) => `model-color-${provider}-${encodeURIComponent(name)}`;
  return <div className="model-palette-chart" data-density={entries.length > 8 ? "dense" : entries.length > 4 ? "medium" : "sparse"} role="group" aria-label={`${providerBrands[provider].label} model palette`}>
    <div className="model-palette-row model-palette-headings"><span /><div className="model-palette-cells" style={columns}>{groups.map(group => <div className="model-palette-group" key={group.name} style={{gridColumn: `span ${group.models.length}`}}><span>{group.name}</span></div>)}</div></div>
    <div className="model-palette-row" role="group" aria-label={`${providerBrands[provider].label} saved model palette`}>
      <span className="model-palette-row-name">Saved</span><div className="model-palette-cells" style={columns}>{entries.map(entry => <button type="button" className="model-palette-swatch" id={controlID(entry.name)} key={entry.name} aria-label={`Edit color for ${entry.name}`} aria-pressed={selected === entry.name} onClick={() => select(entry.name)} style={{background: entry.saved}} title={`${entry.name} · ${modelCatalogDate(entry.name) ?? "Catalog date unavailable"} · ${entry.saved}`} />)}</div>
    </div>
    {changed ? <div className="model-palette-row" role="group" aria-label={`${providerBrands[provider].label} preview model palette`}>
      <span className="model-palette-row-name">Preview</span><div className="model-palette-cells" style={columns}>{entries.map(entry => <span className="model-palette-swatch" key={entry.name} style={{background: entry.preview}} title={`${entry.name} · ${entry.preview}`} />)}</div>
    </div> : null}
    <div className="model-palette-row model-palette-axis"><span /><div className="model-palette-cells" style={columns}>{entries.map(entry => <label className="model-palette-label" key={entry.name} htmlFor={controlID(entry.name)} title={`${entry.name} · ${modelCatalogDate(entry.name) ?? "Catalog date unavailable"}`}>{entry.name}</label>)}</div></div>
  </div>;
}

function ProviderColorCard({ provider, busy, save }: { provider: ModelProvider; busy: boolean; save: Save }) {
  const brand = providerBrands[provider], base = effectiveModelColor("provider", provider);
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [selected, setSelected] = useState<string | null>(null);
  const groups = modelPaletteGroups(knownModels().filter(model => modelProvider(model) === provider));
  const models = groups.flatMap(group => group.models);
  const draft = (kind: ColorKind, name: string) => drafts[`${kind}:${name}`] ?? effectiveModelColor(kind, name);
  const edit = (kind: ColorKind, name: string, color: string) => setDrafts(previous => {
    const next = {...previous};
    if (color === effectiveModelColor(kind, name)) delete next[`${kind}:${name}`]; else next[`${kind}:${name}`] = color;
    return next;
  });
  const saveDraft: Save = async (kind, name, color) => {
    const saved = await save(kind, name, color);
    if (saved) setDrafts(previous => { const next = {...previous}; delete next[`${kind}:${name}`]; return next; });
    return saved;
  };
  const proposedBase = draft("provider", provider), comparison = modelPaletteComparison(models, proposedBase, drafts);
  const warnings = providerColorWarnings(provider, proposedBase, comparison.entries);
  return <section className="model-provider-card" aria-label={`${brand.label} colors`}>
    <ColorEditor kind="provider" name={provider} label={brand.label} detail={`${models.length} models`} base={base} draft={proposedBase} setDraft={color => edit("provider", provider, color)} busy={busy} save={saveDraft} />
    {warnings.length ? <ul className="model-palette-warnings" role="status" aria-label={`${brand.label} color warnings`}>{warnings.map(warning => <li key={warning}>{warning}</li>)}</ul> : null}
    <p className="model-palette-hint">Select a model’s swatch or name to customize it. Proposed colors appear below the saved colors until you save.</p>
    <PaletteBanner provider={provider} groups={groups} entries={comparison.entries} changed={comparison.changed} selected={selected} select={setSelected} />
    {Object.keys(drafts).length ? <div className="model-palette-actions"><button type="button" disabled={busy} onClick={() => setDrafts({})}>Discard changes</button></div> : null}
    {selected ? <div className="model-inline-editor" role="group" aria-label={`Customize ${selected}`}>
      <ColorEditor kind="model" name={selected} label={selected} detail={[modelClass(selected), modelCatalogDate(selected) ? `Catalog date ${modelCatalogDate(selected)}` : "Catalog date unavailable"].filter(Boolean).join(" · ")} base={previewModelColor(selected, proposedBase, drafts[`model:${selected}`])} draft={draft("model", selected)} setDraft={color => edit("model", selected, color)} busy={busy} save={saveDraft} />
      <button className="model-inline-close" type="button" onClick={() => { document.getElementById(`model-color-${provider}-${encodeURIComponent(selected)}`)?.focus(); setSelected(null); }} aria-label={`Close editor for ${selected}`}>Close editor</button>
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
  async function save(kind: ColorKind, key: string, color: string) {
    setBusy(true); setError(""); setMessage("");
    try { await saveModelColor(kind, key, color); setMessage("Color saved to your library."); return true; }
    catch (error) { setError(error instanceof Error ? error.message : String(error)); return false; }
    finally { setBusy(false); }
  }
  return <div className="model-color-settings">
    <p className="muted">Each provider has a color family, with distinct shades for model classes and smaller variations for their versions. Your palette stays consistent across charts, cards, and dashboards and saves with your library.</p>
    <p className="model-color-note">For contemporary releases, core classes deepen in order: {hierarchy}. Older versions are lighter within each class, using the first date in Pharos’s pricing catalog. Defaults can rebalance as models arrive. Changing a provider color shifts its automatic palette; your individual choices stay yours.</p>
    <div role="status" aria-live="polite">{message}</div>
    {error ? <p role="alert">{error}</p> : null}
    <div className="model-provider-list">{modelProviders.map(provider => <ProviderColorCard key={provider} provider={provider} busy={busy} save={save} />)}</div>
  </div>;
}
