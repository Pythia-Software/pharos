import React, { useEffect, useState } from "react";
import { ViewDisclosure } from "./view-disclosure";
import { useLocationRevision } from "./nested-pages";
import { compareModelColors, customModelColor, effectiveModelColor, knownModels, knownSources, loadModelColors, modelCatalogDate, modelClass, modelProvider, modelProviders, providerBrands, rankedModelClasses, saveModelColor, shadeChoices, sourceLabel, sourceProvider, useModelColorRevision, type ColorKind } from "./model-colors";
import "./project-colors.css";
import "./model-colors.css";

type Save = (kind: ColorKind, key: string, color: string) => Promise<void>;
function ColorEditor({ kind, name, label, detail, base, busy, save }: { kind: ColorKind; name: string; label: string; detail: string; base: string; busy: boolean; save: Save }) {
  const color = effectiveModelColor(kind, name), custom = customModelColor(kind, name);
  const [draft, setDraft] = useState(color);
  useEffect(() => { setDraft(color); }, [color]);
  return <div className={`model-color-editor model-color-${kind}`}>
    <div className="model-color-name"><strong>{label}</strong><small className="muted">{detail} · {custom ? "Your color" : kind === "provider" ? "Brand default" : kind === "source" ? "Follows provider" : "Recommended shade"}</small></div>
    <div className="model-color-controls">
      <label className="project-color-picker"><input type="color" aria-label={`Color for ${label}`} value={draft} disabled={busy} onChange={event => setDraft(event.target.value)} /><code>{draft}</code></label>
      {kind === "model" ? <div className="model-shade-choices" role="group" aria-label={`Suggested shades for ${label}`}>{shadeChoices(base).map(shade => <button key={shade} type="button" disabled={busy} aria-label={`Try ${shade} for ${label}`} aria-pressed={draft === shade} title={shade} style={{ background: shade }} onClick={() => setDraft(shade)} />)}</div> : null}
      <div className="project-color-previews" aria-label="Color on light and dark backgrounds"><span className="project-color-light"><i style={{ background: draft }} />Light</span><span className="project-color-dark"><i style={{ background: draft }} />Dark</span></div>
      <button type="button" disabled={busy || draft === color} onClick={() => void save(kind, name, draft)} aria-label={`Save color for ${label}`}>Save color</button>
      <button type="button" disabled={busy || !custom} onClick={() => void save(kind, name, "")} aria-label={`Restore ${kind === "provider" ? "brand color" : kind === "model" ? "recommended shade" : "provider color"} for ${label}`}>{kind === "provider" ? "Restore brand" : kind === "model" ? "Use recommended" : "Use provider"}</button>
    </div>
  </div>;
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
    try { await saveModelColor(kind, key, color); setMessage("Color saved to your library."); }
    catch (error) { setError(error instanceof Error ? error.message : String(error)); }
    finally { setBusy(false); }
  }
  return <div className="model-color-settings">
    <p className="muted">Each provider has a color family, with distinct shades for model classes and smaller variations for their versions. Your palette stays consistent across charts, cards, and dashboards and saves with your library.</p>
    <p className="model-color-note">For contemporary releases, core classes deepen in order: {hierarchy}. Older versions are lighter within each class, using the first date in Pharos’s pricing catalog. Defaults can rebalance as models arrive. Changing a provider color shifts its automatic palette; your individual choices stay yours.</p>
    <div role="status" aria-live="polite">{message}</div>
    {error ? <p role="alert">{error}</p> : null}
    <div className="model-provider-list">{modelProviders.map(provider => {
      const brand = providerBrands[provider], base = effectiveModelColor("provider", provider);
      const models = knownModels().filter(model => modelProvider(model) === provider).sort(compareModelColors);
      const sources = knownSources().filter(source => sourceProvider(source) === provider);
      return <section className="model-provider-card" key={provider} aria-label={`${brand.label} colors`}>
        <ColorEditor kind="provider" name={provider} label={brand.label} detail={sources.map(sourceLabel).join(" · ")} base={base} busy={busy} save={save} />
        <div className="model-palette-strip" aria-label={`${brand.label} model palette`}>{models.map(model => <span key={model} title={model} style={{ background: effectiveModelColor("model", model) }} />)}</div>
        <ViewDisclosure viewKey={`model-colors:${provider}`} summary={`${models.length} models · Customize shades`}>
          <p className="muted model-color-order">Classes follow the catalog’s order, with oldest catalog dates first within each class. Choose a suggested shade or any color of your own, then save.</p>
          {models.map(model => <ColorEditor key={model} kind="model" name={model} label={model} detail={[modelClass(model), modelCatalogDate(model) ? `Catalog date ${modelCatalogDate(model)}` : "Catalog date unavailable"].filter(Boolean).join(" · ")} base={effectiveModelColor("model", model)} busy={busy} save={save} />)}
        </ViewDisclosure>
        <ViewDisclosure viewKey={`source-colors:${provider}`} summary="Provider labels in charts">
          <p className="muted model-color-order">Recorded providers such as Codex and ChatGPT follow this palette. You can also choose a separate color for each label.</p>
          {sources.map(source => <ColorEditor key={source} kind="source" name={source} label={sourceLabel(source)} detail="Recorded provider" base={base} busy={busy} save={save} />)}
        </ViewDisclosure>
      </section>;
    })}</div>
  </div>;
}
