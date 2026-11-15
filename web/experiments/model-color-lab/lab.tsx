import React, { useEffect, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import { colorDistance, colorForeground, colorLuminance } from "../../src/model-color-math";
import { modelDateLabel, modelPaletteGroups } from "../../src/model-colors";
import { algorithms, brands, classes, defaults, lch, models, palette, position, providerModels, providers, roots, specialistModels, workload, type Algorithm, type Model, type Options, type Provider, type Workload } from "./palette";
import "../../src/model-colors.css";
import "./style.css";

type State = { options: Options; bases: Record<Provider, string>; provider: Provider; theme: "light" | "dark"; workload: Workload; scope: "all" | "provider"; selected: string };
function initialState(): State {
  const params = new URLSearchParams(location.search), bases = roots();
  for (const provider of providers) if (/^#[0-9a-f]{6}$/i.test(params.get(provider) ?? "")) bases[provider] = params.get(provider)!;
  const number = (key: string, fallback: number, low: number, high: number) => params.has(key) && Number.isFinite(Number(params.get(key))) ? Math.max(low, Math.min(high, Number(params.get(key)))) : fallback;
  return {
    options: { algorithm: algorithms.some(item => item.id === params.get("algorithm")) ? params.get("algorithm") as Algorithm : defaults.algorithm, vividness: number("vividness", defaults.vividness, 0, 1.2), tierWeight: number("tierWeight", defaults.tierWeight, 0, 1), spread: number("spread", defaults.spread, 0, 100), lightness: number("lightness", defaults.lightness, .5, .78), specialistHues: params.get("specialists") !== "0", specialistMode: params.get("specialistMode") === "catalog" ? "catalog" : defaults.specialistMode, specialistSpread: number("specialistSpread", defaults.specialistSpread, 0, 300) },
    bases, provider: providers.includes(params.get("provider") as Provider) ? params.get("provider") as Provider : "anthropic",
    theme: params.get("theme") === "dark" ? "dark" : "light", workload: ["frontier", "balanced", "history", "specialists"].includes(params.get("workload") ?? "") ? params.get("workload") as Workload : "frontier",
    scope: params.get("scope") === "provider" ? "provider" : "all", selected: models.some(model => model.name === params.get("model")) ? params.get("model")! : "mythos-5-2",
  };
}
function writeState(state: State) {
  const query = new URLSearchParams({ algorithm: state.options.algorithm, provider: state.provider, theme: state.theme, workload: state.workload, scope: state.scope, model: state.selected, specialists: state.options.specialistHues ? "1" : "0", specialistMode: state.options.specialistMode });
  for (const key of ["vividness", "tierWeight", "spread", "lightness", "specialistSpread"] as const) query.set(key, String(state.options[key]));
  for (const provider of providers) if (state.bases[provider] !== brands[provider].color) query.set(provider, state.bases[provider]);
  history.replaceState(null, "", `?${query}`);
}
const pct = (value: number) => `${Math.round(value * 100)}%`;
const getColor = (colors: Map<string, string>, model: Model) => colors.get(model.name)!;
const contrast = (color: string, background: string) => {
  const values = [colorLuminance(color), colorLuminance(background)];
  return ((Math.max(...values) + .05) / (Math.min(...values) + .05)).toFixed(1);
};

function Slider({ label, value, min = 0, max = 1, step = .01, display = pct(value), disabled, onChange }: { label: string; value: number; min?: number; max?: number; step?: number; display?: string; disabled?: boolean; onChange: (value: number) => void }) {
  return <label className="slider"><span>{label}<output>{display}</output></span><input type="range" aria-label={label} min={min} max={max} step={step} value={value} disabled={disabled} onChange={event => onChange(Number(event.target.value))} /></label>;
}

function Chart({ title, colors, entries, subtitle, select }: { title: string; colors: Map<string, string>; entries: ReturnType<typeof workload>; subtitle: string; select: (model: string) => void }) {
  const total = entries.reduce((sum, entry) => sum + entry.weight, 0);
  const amounts = entries.map((entry, series) => Array.from({ length: 28 }, (_, day) => entry.weight * (1 + .22 * Math.sin(day * .37 + series) + .08 * Math.cos(day * .8 + series * .7))));
  const peak = Math.max(...Array.from({ length: 28 }, (_, day) => amounts.reduce((sum, series) => sum + series[day], 0))) || 1;
  const x = (day: number) => 42 + day / 27 * 498, y = (amount: number) => 176 - amount / peak * 144;
  const points = (series: number, upper: boolean) => Array.from({ length: 28 }, (_, day) => `${x(day)},${y(amounts.slice(0, series + (upper ? 1 : 0)).reduce((sum, values) => sum + values[day], 0))}`);
  return <article className="chart-card">
    <div className="chart-heading"><h3>{title}</h3><span>{subtitle}</span></div>
    <div className="chart-unit">Daily tokens · synthetic, normalized to peak</div>
    <svg viewBox="0 0 560 208" className="area-chart" role="img" aria-label={`${title}: synthetic daily token volume by model over 28 days`}>
      {[0, .5, 1].map(fraction => <g key={fraction}><line x1="42" x2="540" y1={y(peak * fraction)} y2={y(peak * fraction)} className="grid-line" /><text x="33" y={y(peak * fraction) + 4} textAnchor="end">{pct(fraction)}</text></g>)}
      {entries.map(({ model }, series) => <path key={model.name} d={`M${points(series, true).join(" L")} L${points(series, false).reverse().join(" L")} Z`} fill={getColor(colors, model)}><title>{model.name} · {getColor(colors, model)}</title></path>)}
      {[0, 9, 18, 27].map(day => <text key={day} x={x(day)} y="198" textAnchor={day === 0 ? "start" : day === 27 ? "end" : "middle"}>Day {day + 1}</text>)}
    </svg>
    <div className="chart-unit">Model share · same workload in both panels</div>
    <div className="share-bar" role="img" aria-label={`${title}: token share by model`}>{entries.map(({ model, weight }) => <div key={model.name} style={{ width: `${weight / total * 100}%`, background: getColor(colors, model) }} title={`${model.name} · ${pct(weight / total)}`} />)}</div>
    <div className="legend">{entries.map(({ model, weight }) => <button type="button" key={model.name} onClick={() => select(model.name)} title={`Inspect ${model.name}`}><i style={{ background: getColor(colors, model) }} /><span>{model.name}</span><b>{pct(weight / total)}</b></button>)}</div>
  </article>;
}

function PaletteBanner({ provider, current, candidate, selected, select }: { provider: Provider; current: Map<string, string>; candidate: Map<string, string>; selected: string; select: (name: string) => void }) {
  const groups = modelPaletteGroups(providerModels(provider).map(model => model.name)), names = groups.flatMap(group => group.models);
  if (!names.length) return <p className="empty">No catalog models for this provider yet. Its root is editable; no model identities or release dates are invented for this experiment.</p>;
  const columns = { gridTemplateColumns: `repeat(${names.length}, minmax(24px, 1fr))` };
  return <div className="palette-scroll"><div className="model-palette-chart" data-density="dense">
    <div className="model-palette-row model-palette-headings"><span /><div className="model-palette-cells" style={columns}>{groups.map(group => <div key={group.name} className="model-palette-group" style={{ gridColumn: `span ${group.models.length}` }}><span title={group.name}>{group.name}</span></div>)}</div></div>
    {([['Current', current], ['Preview', candidate]] as const).map(([label, colors]) => <div className="model-palette-row" key={label}><span className="model-palette-row-name">{label}</span><div className="model-palette-cells" style={columns}>{names.map(name => <button type="button" key={name} className="model-palette-swatch" aria-label={`${label} ${name}`} aria-pressed={selected === name} onClick={() => select(name)} title={`${name} · ${modelDateLabel(name)} · ${colors.get(name)}`} style={{ background: colors.get(name), color: colorForeground(colors.get(name)!) }}>{selected === name ? "↓" : ""}</button>)}</div></div>)}
    <div className="model-palette-row model-palette-axis"><span /><div className="model-palette-cells" style={columns}>{names.map(name => <button type="button" key={name} className="model-palette-label" onClick={() => select(name)}>{name}</button>)}</div></div>
  </div></div>;
}
function metric(colors: Map<string, string>, entries: ReturnType<typeof workload>) {
  const total = entries.reduce((sum, { weight }) => sum + weight, 0);
  return entries.reduce((sum, { model, weight }) => sum + lch(getColor(colors, model))[1] * weight / (total || 1), 0);
}
function nearest(colors: Map<string, string>, entries: ReturnType<typeof workload>) {
  let distance = Infinity;
  for (let i = 0; i < entries.length; i++) for (let j = i + 1; j < entries.length; j++) distance = Math.min(distance, colorDistance(getColor(colors, entries[i].model), getColor(colors, entries[j].model)));
  return Number.isFinite(distance) ? distance : undefined;
}
function App() {
  const [state, setState] = useState(initialState), [notice, setNotice] = useState("");
  const { options, bases, provider, theme, selected } = state;
  const setOption = <K extends keyof Options>(key: K, value: Options[K]) => setState(previous => ({ ...previous, options: { ...previous.options, [key]: value } }));
  useEffect(() => { writeState(state); document.documentElement.dataset.theme = theme; }, [state, theme]);
  useEffect(() => { const restore = () => setState(initialState()); window.addEventListener("popstate", restore); return () => window.removeEventListener("popstate", restore); }, []);
  const current = useMemo(() => palette(bases, { ...options, algorithm: "current" }), [bases]);
  const candidate = useMemo(() => palette(bases, options), [bases, options]);
  const chartReference = useMemo(() => state.workload === "specialists" ? palette(bases, { ...options, specialistHues: true, specialistMode: "catalog" }) : current, [state.workload, bases, options, current]);
  const entries = useMemo(() => workload(state.workload, state.scope === "provider" ? provider : undefined), [state.workload, state.scope, provider]);
  const select = (name: string) => setState(previous => ({ ...previous, selected: name, provider: models.find(model => model.name === name)!.provider }));
  const active = algorithms.find(item => item.id === options.algorithm)!;
  const chosen = models.find(model => model.name === selected)!;
  const referenceChroma = metric(chartReference, entries), nextChroma = metric(candidate, entries), distance = nearest(candidate, entries);
  const useProvider = (next: Provider) => setState(previous => ({ ...previous, provider: next, selected: providerModels(next).at(-1)?.name ?? previous.selected }));
  const previewSpecialists = () => setState(previous => {
    const target = specialistModels(previous.provider).length ? previous.provider : "openai";
    return { ...previous, provider: target, selected: specialistModels(target).at(-1)!.name, workload: "specialists", scope: "provider" };
  });
  const exportPalette = () => {
    const blob = new Blob([JSON.stringify({ experiment: "Pharos model color lab", options, roots: bases, models: Object.fromEntries(models.map(model => [model.name, { provider: model.provider, class: model.class, date: model.date ?? null, current: current.get(model.name), preview: candidate.get(model.name) }])) }, null, 2)], { type: "application/json" });
    const url = URL.createObjectURL(blob), anchor = document.createElement("a"); anchor.href = url; anchor.download = `pharos-model-colors-${options.algorithm}.json`; anchor.click(); setTimeout(() => URL.revokeObjectURL(url), 0);
    setNotice("Experiment exported. Your current URL also preserves all the controls and root colors.");
  };
  return <>
    <header className="app-header"><a href="/" className="wordmark">Pharos<span>Color lab</span></a><div><span className="local-badge">Local experiment</span><button type="button" onClick={() => setState(previous => ({ ...previous, theme: theme === "light" ? "dark" : "light" }))}>{theme === "light" ? "Dark" : "Light"} appearance</button><button type="button" onClick={exportPalette}>Export palette</button></div></header>
    <main>
      <div className="page-heading"><p className="eyebrow">Settings / Model &amp; provider colors</p><h1>Let the frontier be colorful.</h1><p>Explore how provider roots become model colors. Compare the current policy with three alternatives on the same model catalog and workload.</p></div>
      <div className="lab-layout">
        <aside className="controls">
          <h2>Color algorithm</h2>
          <div className="algorithm-list">{algorithms.map((item, index) => <button type="button" key={item.id} aria-pressed={options.algorithm === item.id} onClick={() => setOption("algorithm", item.id)}><span className="algorithm-number">0{index + 1}</span><span><strong>{item.name}</strong><small>{item.id === "current" ? "Production baseline" : item.id === "chroma" ? "Tier + release → chroma" : item.id === "fan" ? "Tier → hue · tier + release → chroma" : "Release → chroma"}</small></span></button>)}</div>
          <p className="algorithm-description">{active.description}</p>
          <div className="slider-group">
            <Slider label="Vividness" value={options.vividness} max={1.2} disabled={options.algorithm === "current"} onChange={value => setOption("vividness", value)} />
            <Slider label="Tier emphasis" value={options.tierWeight} disabled={options.algorithm === "current" || options.algorithm === "fresh"} onChange={value => setOption("tierWeight", value)} />
            <Slider label="Frontier lightness" value={options.lightness} min={.5} max={.78} disabled={options.algorithm === "current"} onChange={value => setOption("lightness", value)} />
            <Slider label="Tier hue spread" value={options.spread} max={100} step={1} display={`${options.spread}°`} disabled={options.algorithm === "current" || options.algorithm === "chroma"} onChange={value => setOption("spread", value)} />
            <div className="specialist-controls">
              <label className="specialist-treatment">Specialist treatment<select aria-label="Specialist treatment" disabled={options.algorithm === "current"} value={options.specialistHues ? options.specialistMode : "root"} onChange={event => {
                const value = event.target.value;
                setState(previous => ({ ...previous, options: { ...previous.options, specialistHues: value !== "root", specialistMode: value === "root" ? previous.options.specialistMode : value as Options['specialistMode'] } }));
              }}><option value="expanded">Wider hue fan</option><option value="catalog">Original specialist hues</option><option value="root">Use provider root</option></select></label>
              <Slider label="Specialist hue spread" value={options.specialistSpread} max={300} step={5} display={`${options.specialistSpread}°`} disabled={options.algorithm === "current" || !options.specialistHues || options.specialistMode !== "expanded"} onChange={value => setOption("specialistSpread", value)} />
              <p className="help">A separate, wider range for specialist classes. Core tiers keep their {options.spread}° fan. Specialist positions describe categories, not capability.</p>
              <button type="button" className="reset" onClick={previewSpecialists}>Preview specialist mix</button>
            </div>
          </div>
          <p className="help">Provider roots influence hue, chroma and lightness. Neutral roots stay neutral. Identical colors are used in both appearances.</p>
          <button type="button" className="reset" onClick={() => { setState({ ...initialState(), options: { ...defaults }, bases: roots(), provider: "anthropic", selected: "mythos-5-2", workload: "frontier", scope: "all" }); setNotice("Restored experiment defaults."); }}>Reset experiment</button>
          <p className="help">Controls are preserved in this page’s URL. Bookmark or share it while this local server is running.</p>
        </aside>
        <div className="workspace">
          <section aria-labelledby="usage-heading">
            <div className="section-heading"><div><p className="eyebrow">01 / In the graphs</p><h2 id="usage-heading">{state.workload === "frontier" ? "A frontier-heavy workload" : state.workload === "balanced" ? "A mix across the catalog" : state.workload === "specialists" ? "A mix of specialist classes" : "A historical workload"}</h2></div><div className="chart-controls"><label>Workload<select aria-label="Workload" value={state.workload} onChange={event => setState(previous => ({ ...previous, workload: event.target.value as Workload }))}><option value="frontier">Frontier heavy</option><option value="balanced">Across the catalog</option><option value="history">Historical mix</option><option value="specialists">Specialist mix</option></select></label><label>Models<select aria-label="Chart models" value={state.scope} onChange={event => setState(previous => ({ ...previous, scope: event.target.value as State['scope'] }))}><option value="all">{state.workload === "specialists" ? "All specialist classes" : "All core providers"}</option><option value="provider">Selected provider</option></select></label></div></div>
            <p className="input-note">Synthetic usage, not your library. Real catalog model identities and catalog/observed dates; identical data in both panels. Up to eight model series.</p>
            {state.workload === "specialists" ? <p className="input-note">The reference keeps your current algorithm and core settings, with the original specialist hues. Only the specialist treatment changes in the experiment.</p> : null}
            <div className="chart-grid"><Chart title={state.workload === "specialists" ? "Original specialist hues" : "Current policy"} subtitle="Reference" colors={chartReference} entries={entries} select={select} /><Chart title={active.name} subtitle="Experiment" colors={candidate} entries={entries} select={select} /></div>
            {!entries.length ? <p className="empty">{state.workload === "specialists" ? "This provider has no specialist classes alongside its core tiers. Select OpenAI or Google, or choose a different workload." : "This provider has no catalog models yet. Select another provider to preview a workload."}</p> : null}
            <div className="metrics"><span><strong>{referenceChroma > .0001 ? `${(nextChroma / referenceChroma).toFixed(2)}×` : "—"}</strong> usage-weighted chroma vs reference</span><span><strong>{distance === undefined ? "—" : distance.toFixed(3)}</strong> closest model pair · OKLab distance</span><span>{distance !== undefined && distance < .025 ? "Some series are close; inspect their labels." : "Inspect both appearances and neighboring versions."}</span></div>
          </section>
          <section aria-labelledby="settings-heading" className="settings-preview">
            <div className="section-heading"><div><p className="eyebrow">02 / In settings</p><h2 id="settings-heading">From one root to a whole family</h2></div><span className="catalog-count">{models.length} catalog models · {providers.length} roots</span></div>
            <p className="input-note">Choose a provider, then change its root color. Current and Preview both use that root, so the comparison isolates the algorithm.</p>
            <div className="provider-tabs" aria-label="Providers">{providers.map(key => <button type="button" key={key} aria-pressed={key === provider} onClick={() => useProvider(key)}><i style={{ background: bases[key] }} />{brands[key].label}</button>)}</div>
            <div className="model-provider-card">
              <div className="provider-header"><div><h3>{brands[provider].label}</h3><small>{providerModels(provider).length} models · {classes(provider).length} color classes</small></div><div className="root-controls"><label className="root-picker" style={{ background: bases[provider], color: colorForeground(bases[provider]) }}><input type="color" aria-label={`Root color for ${brands[provider].label}`} value={bases[provider]} onChange={event => setState(previous => ({ ...previous, bases: { ...previous.bases, [provider]: event.target.value } }))} /><span>Change root</span><code>{bases[provider]}</code></label><button type="button" onClick={() => setState(previous => ({ ...previous, bases: { ...previous.bases, [provider]: brands[provider].color } }))}>Restore brand</button></div></div>
              <PaletteBanner provider={provider} current={current} candidate={candidate} selected={selected} select={select} />
              {chosen.provider === provider ? <div className="inspector"><div><strong>{chosen.name}</strong><small>{chosen.class} · {modelDateLabel(chosen.name)}</small><small>{position(chosen).ranked ? `Tier position ${pct(position(chosen).tier)}` : "Specialist class · no capability rank"} · release position {pct(position(chosen).recency)}{!chosen.date ? " (undated midpoint)" : ""}</small></div><div className="swatch-comparison">{([['Current', current], ['Preview', candidate]] as const).map(([label, colors]) => {
                const color = getColor(colors, chosen), [L, C] = lch(color);
                return <div key={label}><span>{label}</span><div className="sample light" aria-label="On light background"><i style={{ background: color }} /></div><div className="sample dark" aria-label="On dark background"><i style={{ background: color }} /></div><code>{color}</code><small>L {L.toFixed(2)} · C {C.toFixed(3)}</small><small>Fill contrast · light {contrast(color, '#fffaf0')}:1</small><small>dark {contrast(color, '#183529')}:1</small></div>;
              })}</div></div> : null}
            </div>
          </section>
          <section className="algorithm-comparison" aria-labelledby="compare-heading"><div className="section-heading"><div><p className="eyebrow">03 / At a glance</p><h2 id="compare-heading">Four policies, same inputs</h2></div></div><div className="policy-grid">{algorithms.map(item => {
            const colors = palette(bases, { ...options, algorithm: item.id });
            return <button type="button" key={item.id} aria-pressed={options.algorithm === item.id} onClick={() => setOption("algorithm", item.id)}><strong>{item.name}</strong><div className="mini-palette">{entries.map(({ model, weight }) => <i key={model.name} title={model.name} style={{ flex: weight, background: getColor(colors, model) }} />)}</div><small>{item.description}</small></button>;
          })}</div></section>
          <section className="method"><h2>What the algorithms are doing</h2><p>{options.algorithm === "current" ? "Current uses recommendedModelColor directly, including its provider-root transform. It is the actual production policy." : options.algorithm === "fresh" ? "Score = release position. Each class’s newest dated release reaches full chroma; a hue fan separates ranked tiers." : `Score = ${pct(options.tierWeight)} tier position + ${pct(1 - options.tierWeight)} release position. ${options.algorithm === "fan" ? "Tier position also shifts the hue around the provider root." : "Ranked tiers keep the same provider hue."}`}</p><p>For the experimental policies, chroma grows from 20% to 100% of the root-derived budget as score increases. Lightness moves toward the frontier setting. Colors are computed in OKLCH and mapped into sRGB by reducing chroma. Vividness is constrained by the display gamut.</p><p>The wider specialist fan scales the catalog’s existing hue offsets to the selected span around the provider root. A lone specialist with no existing offset gets a separate hue at half that span. Providers with no ranked core classes keep their own root. Original specialist hues restores the earlier treatment; Use provider root removes the specialist offsets. Core tier colors are unaffected by either specialist control.</p><p>Tier order comes from the reviewed rules. Release position counts distinct catalog/observed dates within each class; same-date siblings share a position with a small symmetric hue offset, and undated labels use a midpoint. Unranked classes use release position only. Close shades remain possible. Production uses the default Hue fan preset; its generator and provider-root recommendations share this formula.</p><p>The input is the repository’s model catalog, shown in the provider swatches above; the workload is synthetic. Root colors and experiment controls stay in this local URL. Existing library overrides are not loaded.</p></section>
        </div>
      </div>
      <footer><span>Pharos · Model color lab</span><span role="status" aria-live="polite">{notice || "Local preview · no library writes"}</span></footer>
    </main>
  </>;
}
createRoot(document.getElementById("root")!).render(<App />);
