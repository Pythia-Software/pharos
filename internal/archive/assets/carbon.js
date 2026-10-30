// Carbon Impact calculator and assumption controls. The React usage view
// supplies filtered tier/category counts; this module owns the single energy
// calculation used by both the summary and the shared usage chart.
(() => {
  'use strict';
  const CSS = `
.carbon-card{width:100%;box-sizing:border-box;background:var(--panel,#fff);border:1px solid var(--line,#ccc);border-radius:6px;padding:18px 20px}
.carbon-head{width:100%}
.carbon-hero{min-width:0}
.carbon-title-row{display:flex;flex-wrap:wrap;align-items:end;gap:8px 32px}
.carbon-title{min-width:0}
.carbon-hero .big{margin:2px 0}
.carbon-electricity .big{font-size:var(--fs-xl,22px)}
.carbon-comparisons{margin-top:8px;color:var(--muted,#666)}
.carbon-comparisons-title{font-size:var(--fs-xs,11px)}
.carbon-facts{display:flex;flex-wrap:wrap;align-items:center;gap:4px 14px;margin-top:12px;font-size:var(--fs-md,13px)}
.carbon-comparisons .carbon-facts{margin-top:3px}
.carbon-flight-route{display:inline-flex;align-items:center;gap:4px;white-space:nowrap}
.carbon-facts span{white-space:nowrap}
.carbon-facts b{font-weight:650;font-variant-numeric:tabular-nums}
.carbon-comparison{display:inline-flex;align-items:center;gap:4px;white-space:nowrap}
.carbon-comparison b{font-weight:650;font-variant-numeric:tabular-nums}
.carbon-comparison select{width:50px;min-width:0;padding:0 15px 0 3px;border:1px solid var(--line,#ccc);border-radius:5px;background-color:var(--panel,#fff);background-position:calc(100% - 9px) center,calc(100% - 5px) center;background-size:4px 4px;color:var(--ink,#222);font:inherit}
.carbon-comparison select[data-carbon-control="vehicle"]{width:110px}
.carbon-comparison .carbon-info-button{margin-left:2px}
.carbon-segmented{display:inline-flex;flex:none;border:1px solid var(--line,#ccc);border-radius:5px;overflow:hidden}
.carbon-segmented button{border:0;border-radius:0;background:none;color:var(--muted,#666);padding:4px 10px}
.carbon-segmented button+button{border-left:1px solid var(--line,#ccc)}
.carbon-segmented button[aria-pressed="true"]{background:var(--accent,#315845);color:var(--panel,#fff)}
.carbon-assumptions h3{margin:0;font-size:var(--fs-base,14px)}
.carbon-controls{display:flex;flex-wrap:wrap;align-items:end;gap:12px 18px;margin:12px 0 4px}
.carbon-controls label,.carbon-control{display:flex;flex-direction:column;gap:4px;color:var(--muted,#666);font-size:var(--fs-sm,12px)}
.carbon-controls select{min-width:260px;width:auto;padding:5px 28px 5px 8px;border:1px solid var(--line,#ccc);background-color:var(--panel,#fff);color:var(--ink,#222)}
.carbon-controls input{width:92px;padding:5px 8px;border:1px solid var(--line,#ccc);border-radius:5px;background:var(--panel,#fff);color:var(--ink,#222);font-variant-numeric:tabular-nums}
.carbon-control-title{display:inline-flex;align-items:center;gap:5px;min-height:23px;font-weight:600;color:var(--ink,#222)}
.carbon-info-button{display:inline-flex;align-items:center;justify-content:center;flex:none;width:23px;height:23px;padding:2px;border:1px solid transparent;border-radius:50%;background:transparent;color:var(--muted,#666);cursor:pointer}
.carbon-info-button:hover{border-color:var(--line,#ccc);background:color-mix(in srgb,var(--accent,#315845) 9%,var(--panel,#fff));color:var(--accent,#315845)}
.carbon-info-button:focus-visible{outline:2px solid var(--accent,#315845);outline-offset:2px}
.carbon-info-button svg{width:16px;height:16px;fill:none;stroke:currentColor;stroke-width:1.7;stroke-linecap:round;stroke-linejoin:round}
.carbon-info-dialog{width:min(640px,calc(100vw - 32px));max-height:min(82vh,760px);padding:0;border:1px solid var(--line,#ccc);border-radius:12px;background:var(--panel,#fff);color:var(--ink,#222);box-shadow:0 20px 60px #0005;overflow:auto}
.carbon-info-dialog::backdrop{background:rgba(14,25,22,.55)}
.carbon-info-head{display:flex;align-items:flex-start;justify-content:space-between;gap:16px;padding:24px 26px 16px;border-bottom:1px solid var(--line,#ccc)}
.carbon-info-eyebrow{margin:0 0 5px;color:var(--accent,#315845);font-size:11px;font-weight:750;letter-spacing:.09em;text-transform:uppercase}
.carbon-info-head h2{margin:0;font-size:21px;line-height:1.2}
.carbon-info-close{flex:none;min-width:32px;min-height:32px;border:1px solid var(--line,#ccc);border-radius:6px;background:var(--panel,#fff);color:var(--ink,#222);font:inherit;font-size:23px;line-height:1;cursor:pointer}
.carbon-info-close:hover{color:var(--accent,#315845)}
.carbon-info-close:focus-visible{outline:2px solid var(--accent,#315845);outline-offset:2px}
.carbon-info-body{padding:20px 26px 26px;font-size:var(--fs-md,13px);line-height:1.55}
.carbon-info-lead{margin:0 0 18px;font-size:15px;line-height:1.5}
.carbon-info-section{margin-top:18px}
.carbon-info-section h3{margin:0 0 5px;font-size:13px}
.carbon-info-section p{margin:0;color:var(--ink,#222)}
.carbon-info-example{margin-top:18px;padding:13px 15px;border-left:3px solid var(--accent,#315845);border-radius:0 6px 6px 0;background:color-mix(in srgb,var(--accent,#315845) 7%,var(--panel,#fff));font-variant-numeric:tabular-nums}
.carbon-info-sources{margin-top:20px;padding-top:14px;border-top:1px solid var(--line,#ccc);color:var(--muted,#666)}
.carbon-info-sources a{color:var(--accent,#315845)}
.carbon-table{width:100%;margin-top:14px;border-collapse:collapse;font-size:var(--fs-md,13px);font-variant-numeric:tabular-nums}
.carbon-table caption{text-align:left;font-weight:650;padding-bottom:4px}
.carbon-table th,.carbon-table td{padding:6px 0 6px 14px;text-align:right;white-space:nowrap;border-bottom:1px solid color-mix(in srgb,var(--line,#ccc) 60%,transparent)}
.carbon-table th:first-child,.carbon-table td:first-child{padding-left:0;text-align:left}
.carbon-table thead th{color:var(--muted,#666);font-size:var(--fs-sm,12px);font-weight:600;border-bottom-color:var(--line,#ccc)}
.carbon-table tfoot td{font-weight:650;border-bottom:0}
.carbon-table .carbon-share{width:30%;min-width:120px}
.carbon-share-content{display:flex;align-items:center;gap:8px}
.carbon-share-track{flex:1;min-width:0}
.carbon-share-percent{min-width:3.5em;text-align:right}
.carbon-bar{display:block;height:8px;border-radius:0 4px 4px 0;background:var(--accent,#315845);min-width:1px}
.carbon-sub{display:block;color:var(--muted,#666);font-size:var(--fs-xs,11px);font-weight:400}
.carbon-note{margin:10px 0 0;color:var(--muted,#666);font-size:var(--fs-sm,12px)}
.carbon-method{margin-top:14px;border-top:1px solid var(--line,#ccc);padding-top:10px;font-size:var(--fs-md,13px)}
.carbon-method summary{cursor:pointer;color:var(--accent,#315845)}
.carbon-method h4{margin:14px 0 4px;font-size:var(--fs-sm,12px);letter-spacing:.05em;text-transform:uppercase;color:var(--muted,#666)}
.carbon-method p,.carbon-method li{max-width:860px;line-height:1.45}
.carbon-method ul,.carbon-method ol{margin:4px 0;padding-left:20px}
.carbon-method code{font:12px ui-monospace,SFMono-Regular,monospace}
.carbon-method .carbon-table td{vertical-align:top}
.carbon-method .carbon-table td.carbon-basis{white-space:normal;text-align:left;min-width:280px}
.carbon-method .carbon-table td:first-child{white-space:normal}

.carbon-assumptions-card.carbon-card{margin-bottom:14px;padding:14px 16px}
.carbon-calculation>summary{cursor:pointer;font-weight:650;color:var(--ink,#222)}
.carbon-calculation-preview{display:block;margin:4px 0 0 16px;font-size:var(--fs-sm,12px);font-weight:400;color:var(--muted,#666)}
.carbon-fallback-marker{display:inline-block;margin:6px 0 0 16px;padding:2px 6px;border:1px solid var(--line,#ccc);border-radius:3px;font-size:var(--fs-xs,11px);font-weight:400}
.carbon-calculation-body{padding-top:10px;font-size:var(--fs-md,13px)}
.carbon-calculation-scope{margin:0 0 8px;color:var(--muted,#666);overflow-wrap:anywhere}
.carbon-calculation-legend{display:flex;flex-wrap:wrap;gap:5px 8px;margin-bottom:12px}
.carbon-provenance{display:inline-block;padding:2px 6px;border:1px solid var(--line,#ccc);border-radius:3px;font-size:var(--fs-xs,11px);font-weight:500;color:var(--ink,#222)}
.carbon-provenance.assumed{background:color-mix(in srgb,var(--gold,#ac893d) 14%,var(--panel,#fff))}
.carbon-provenance.estimated{background:color-mix(in srgb,var(--accent,#315845) 12%,var(--panel,#fff))}
.carbon-provenance.reported,.carbon-provenance.defined{color:var(--muted,#666)}
.carbon-calculation-step{margin-top:16px;padding-top:12px;border-top:1px solid var(--line,#ccc)}
.carbon-calculation-step h3{margin:0 0 6px;font-size:var(--fs-base,14px)}
.carbon-calculation-step>p{margin:4px 0 8px}
.carbon-equation{display:flex;flex-wrap:wrap;align-items:baseline;gap:4px 8px;margin:8px 0;font-variant-numeric:tabular-nums}
.carbon-equation>span{max-width:100%;white-space:normal;overflow-wrap:anywhere}
.carbon-calculation-overview{padding:10px 0;border-top:1px solid var(--line,#ccc)}
.carbon-calculation-overview h4{margin:0;font-size:var(--fs-sm,12px)}
.carbon-calculation-overview .carbon-equation{margin-bottom:0}
.carbon-ledger-scroll{max-width:100%;overflow-x:auto}
.carbon-ledger{width:100%;min-width:960px;table-layout:fixed;border-collapse:collapse;margin:8px 0;font-size:11px;font-variant-numeric:tabular-nums}
.carbon-ledger caption{text-align:left;color:var(--muted,#666);padding-bottom:6px;font-size:11px;line-height:1.6}
.carbon-ledger-key{margin:8px 0 0;color:var(--muted,#666);font-size:11px;line-height:1.6}
.carbon-ledger-key strong{font-weight:600;color:var(--ink,#222)}
.carbon-ledger th,.carbon-ledger td{padding:5px 4px;text-align:right;vertical-align:middle;border-bottom:1px solid var(--line,#ccc);white-space:nowrap}
.carbon-ledger th:first-child{text-align:left;padding-left:0}
.carbon-ledger thead th{font-size:10px;font-weight:600;color:var(--ink,#222);padding-top:7px;padding-bottom:7px}
.carbon-ledger .carbon-ledger-group th{text-align:left;padding:10px 0 3px;border-bottom:0;color:var(--ink,#222);font-size:11px}
.carbon-ledger col:first-child{width:8%}
.carbon-ledger col:nth-child(2){width:10%}
.carbon-ledger col:nth-child(3){width:11%}
.carbon-ledger col:nth-child(4){width:10%}
.carbon-ledger col:nth-child(5){width:11%}
.carbon-ledger col:nth-child(6){width:10%}
.carbon-ledger col:nth-child(7){width:10%}
.carbon-ledger col:nth-child(8){width:9%}
.carbon-ledger col:nth-child(9){width:11%}
.carbon-ledger col:nth-child(10){width:10%}
.carbon-math-cell{display:flex;align-items:center;justify-content:space-between;gap:4px;min-width:0}
.carbon-math-cell>.carbon-operator{flex:none;color:var(--ink,#222);opacity:.8;font-size:12px}
.carbon-quantity{display:inline-flex;flex-direction:column;align-items:inherit;line-height:1.4}
.carbon-quantity>strong{font-weight:600}
.carbon-quantity>small{color:var(--muted,#666);font-size:var(--fs-xs,11px);font-weight:400}
.carbon-ledger .carbon-quantity{flex-direction:row;align-items:baseline;gap:3px;white-space:nowrap}
.carbon-ledger .carbon-quantity>small{font-size:10px;color:var(--ink,#222);opacity:.8}
.carbon-ledger td:last-child .carbon-math-cell{justify-content:flex-start;gap:10px}
.carbon-ledger thead th:last-child{text-align:left;padding-left:23px}
.carbon-unit-fraction{display:inline-flex;flex-direction:column;align-items:stretch;max-width:100%;line-height:1.35;text-align:center;vertical-align:middle;font-variant-numeric:tabular-nums}
.carbon-fraction-top{padding:1px 2px 3px}
.carbon-fraction-bottom{padding:3px 2px 1px;border-top:1px solid currentColor}
.carbon-fraction-slash{position:absolute;width:1px;height:1px;overflow:hidden;clip-path:inset(50%);white-space:nowrap}
.carbon-factor-link:has(.carbon-unit-fraction){text-decoration:none;max-width:100%;padding:5px 7px}
.carbon-factor-link.carbon-assumption-box{border:1px dotted var(--gold,#ac893d);border-radius:4px;padding:5px 7px;text-decoration:none}
.carbon-assumption-box:hover{background:color-mix(in srgb,var(--gold,#ac893d) 8%,var(--panel,#fff))}
.carbon-factor-link.carbon-defined-box{border:1px solid var(--line,#ccc);border-radius:4px}
.carbon-ledger .carbon-factor-link{padding:3px 4px;font-size:10px;flex:none}
.carbon-ledger .carbon-unit-fraction{font-size:10px;white-space:nowrap;flex:none}
.carbon-ledger .carbon-fraction-top{padding:0 1px 2px}
.carbon-ledger .carbon-fraction-bottom{padding:2px 1px 0}
.carbon-ledger .carbon-term-row th,.carbon-ledger .carbon-term-row td{border-bottom:0}
.carbon-ledger .carbon-grand-total th{font-weight:650}
.carbon-grand-total{border-top:2px solid var(--line,#ccc)}
.carbon-row-trigger{display:flex;flex-direction:column;align-items:start;padding:0;background:none;border:0;font:inherit;font-weight:600;color:var(--ink,#222);cursor:pointer;text-align:left}
.carbon-row-trigger small{font-size:10px;font-weight:400;color:var(--accent,#315845);text-decoration:underline;text-underline-offset:2px}
.carbon-factor-link{min-height:28px;min-width:24px;margin:0;padding:1px 3px;border:0;background:transparent;color:var(--ink,#222);font:inherit;cursor:pointer;text-decoration:underline dashed var(--gold,#ac893d);text-underline-offset:3px}
.carbon-factor-link:hover{color:var(--accent,#315845)}
.carbon-factor-link:focus-visible,.carbon-row-trigger:focus-visible,.carbon-calculation summary:focus-visible{outline:2px solid var(--accent,#315845);outline-offset:3px}
.carbon-term-evidence[hidden]{display:none}
.carbon-term-evidence>td{white-space:normal;text-align:left;padding:12px;background:color-mix(in srgb,var(--bg,#f8f6f0) 45%,var(--panel,#fff))}
.carbon-row-evidence h4,.carbon-factor-evidence h4{margin:8px 0 5px;font-size:var(--fs-sm,12px)}
.carbon-row-factors{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:20px;border-top:1px solid var(--line,#ccc);margin-top:12px;padding-top:4px}
.carbon-factor-heading{display:flex;flex-wrap:wrap;align-items:baseline;gap:5px 8px;margin:8px 0}
.carbon-factor-heading h4{width:100%;margin:0}
.carbon-factor-evidence p{margin:6px 0;line-height:1.5}
.carbon-factor-sources{padding-left:18px;margin:6px 0;font-size:var(--fs-sm,12px)}
.carbon-factor-sources li{margin:8px 0}
.carbon-factor-sources p{color:var(--muted,#666)}
.carbon-factor-alternatives{display:grid;grid-template-columns:repeat(3,auto auto);justify-content:start;gap:3px 8px;margin:10px 0;font-variant-numeric:tabular-nums}
.carbon-factor-alternatives dt{color:var(--muted,#666)}
.carbon-factor-alternatives dd{margin:0 12px 0 0}
.carbon-evidence-disclosure{margin-top:10px;border-top:1px solid var(--line,#ccc);padding-top:8px}
.carbon-evidence-disclosure>summary{cursor:pointer;color:var(--accent,#315845)}
.carbon-evidence-disclosure>p,.carbon-evidence-disclosure li{line-height:1.5}
.carbon-sensitivity{display:flex;flex-wrap:wrap;gap:8px}
.carbon-sensitivity>span{padding:4px 8px;border:1px solid var(--line,#ccc);border-radius:4px}
.carbon-sensitivity>span.selected{background:color-mix(in srgb,var(--accent,#315845) 12%,var(--panel,#fff));border-color:var(--accent,#315845)}
@media(max-width:760px){
 .carbon-calculation-legend{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:6px}
 .carbon-ledger-scroll{margin-right:-4px;padding-bottom:6px}
 .carbon-row-factors{grid-template-columns:1fr;gap:12px}
 .carbon-equation{font-size:var(--fs-sm,12px);gap:3px 6px}
 .carbon-factor-alternatives{grid-template-columns:repeat(3,auto auto);gap:3px 5px}
 .carbon-factor-alternatives dd{margin-right:4px}
 .carbon-term-evidence>td{padding:10px}
 .carbon-row-evidence{width:calc(100vw - 100px);max-width:100%}
}

.carbon-cite{font-size:var(--fs-xs,11px);vertical-align:super;margin-left:1px}
@media(max-width:760px){.carbon-controls{display:grid;gap:8px}.carbon-controls>.carbon-control:not(:first-child){flex-direction:row;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:4px 12px}.carbon-segmented button{min-height:30px}.carbon-table th:first-child,.carbon-table td:first-child{white-space:normal}.carbon-method .carbon-table{display:block;overflow-x:auto}.carbon-comparison{max-width:100%;flex-wrap:wrap;white-space:normal}.carbon-table .carbon-share{display:none}.carbon-controls select{min-width:0;width:100%}}`;

  const CATEGORY_LABELS = {
    uncached_input: ['Uncached input', 'Prompt tokens processed in full'],
    cache_write: ['Cache writes', 'Input processed and stored for reuse'],
    cache_read: ['Cache reads', 'Input served from a prompt cache'],
    output: ['Output', 'Generated tokens, including reasoning'],
  };
  const SCENARIOS = [['low', 'Low'], ['central', 'Central'], ['high', 'High']];
  const INPUT_SENSITIVITIES = [['current', 'Labs'], ['third', 'Watershed']];
  const VEHICLES = [['car_miles', 'Gas car'], ['car_hybrid', 'Hybrid'], ['car_electric', 'Electric car'], ['car_pickup', 'Pickup']];
  // Representative airport locations, rounded to four decimals (FAA airport reference points).
  const CITIES = [
    {key: 'lax', label: 'LA · LAX', lat: 33.9425, lon: -118.4081},
    {key: 'sfo', label: 'SF · SFO', lat: 37.6188, lon: -122.3750},
    {key: 'den', label: 'Denver · DEN', lat: 39.8561, lon: -104.6737},
    {key: 'jfk', code: 'NYC', label: 'NYC · JFK', lat: 40.6413, lon: -73.7781},
    {key: 'dca', label: 'DC · DCA', lat: 38.8512, lon: -77.0402},
    {key: 'bos', label: 'Boston · BOS', lat: 42.3656, lon: -71.0096},
  ];
  const STORE = 'pharos-carbon-v1';

  const node = (tag, cls, text) => {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined && text !== null) n.textContent = text;
    return n;
  };
  const load = () => { const saved = window.pharosPrefs.get(STORE, {}); return saved && typeof saved === 'object' ? saved : {}; };
  const save = change => window.pharosPrefs.set(STORE, {...load(), ...change});
  const routeSettings = () => {
    if (typeof location === 'undefined') return null;
    const encoded = new URLSearchParams(location.search).get('analyze_carbon');
    if (!encoded) return null;
    try {
      const value = JSON.parse(encoded);
      return value && typeof value === 'object' && !Array.isArray(value) ? value : null;
    } catch { return null; }
  };
  const prefs = {...(routeSettings() || load())};
  let data = null, factorsData = null, error = '', usageLabel = 'all time', usageScope = '';
  const disclosureState = new Map();

  // Three significant figures, with the unit stepped so the number stays short.
  const sig = value => {
    const n = Number(value) || 0;
    if (n === 0) return '0';
    const abs = Math.abs(n);
    return Number(n.toPrecision(abs >= 1 ? 3 : 2)).toLocaleString(undefined, {maximumFractionDigits: 6});
  };
  const mass = grams => {
    const g = Number(grams) || 0;
    if (g >= 1e6) return `${sig(g / 1e6)} t`;
    if (g >= 1e3) return `${sig(g / 1e3)} kg`;
    return `${sig(g)} g`;
  };
  const energy = wh => {
    const w = Number(wh) || 0;
    if (w >= 1e6) return `${sig(w / 1e6)} MWh`;
    if (w >= 1e3) return `${sig(w / 1e3)} kWh`;
    return `${sig(w)} Wh`;
  };
  const tokens = value => {
    const n = Number(value) || 0;
    for (const [size, unit] of [[1e12, 'T'], [1e9, 'B'], [1e6, 'M'], [1e3, 'k']]) if (n >= size) return `${sig(n / size)}${unit}`;
    return n.toLocaleString();
  };
  const pick = (range, scenario) => Number(range?.[scenario]) || 0;
  // Tiers are listed in match order; show them smallest first.
  const bySize = tiers => [...tiers].sort((a, b) => pick(a.output_wh_per_mtok, 'central') - pick(b.output_wh_per_mtok, 'central'));
  const equivalent = (factors, key) => factors.equivalents?.find(item => item.key === key);
  function flightMiles(from, to) {
    const radians = degrees => degrees * Math.PI / 180;
    const a = radians(from.lat), b = radians(to.lat), dLat = b - a, dLon = radians(to.lon - from.lon);
    return 3958.8 * 2 * Math.asin(Math.sqrt(Math.sin(dLat / 2) ** 2 + Math.cos(a) * Math.cos(b) * Math.sin(dLon / 2) ** 2));
  }

  function comparisonSettings(factors) {
    const vehicleKey = VEHICLES.some(([key]) => key === prefs.vehicle && equivalent(factors, key)) ? prefs.vehicle : 'car_miles';
    const from = CITIES.find(city => city.key === prefs.flightFrom) || CITIES.find(city => city.key === 'jfk');
    const to = CITIES.find(city => city.key === prefs.flightTo && city.key !== from.key)
      || CITIES.find(city => city.key === 'den' && city.key !== from.key) || CITIES.find(city => city.key !== from.key);
    const miles = flightMiles(from, to);
    return {vehicleKey, vehicle: equivalent(factors, vehicleKey), phone: equivalent(factors, 'phone_charges'), from, to, miles,
      flight: equivalent(factors, miles < 300 ? 'flight_short' : miles < 2300 ? 'flight_medium' : 'flight_long')};
  }

  // The same arithmetic as carbon.go: tokens ÷ 1M × tier Wh per million
  // output tokens × the token type's ratio to output, then × PUE × grid.
  function calculate(factors, period, scenario, pue, grid, inputScale = 1, explain = false) {
    const tiers = Object.fromEntries(factors.tiers.map(tier => [tier.key, tier]));
    const ratio = category => category === 'output' ? 1 : pick(factors.ratios[category], scenario) * inputScale;
    const byCategory = {}, byTier = {}, terms = [];
    let total = 0, outputEquivalent = 0;
    for (const category of Object.keys(CATEGORY_LABELS)) byCategory[category] = {tokens: 0, wh: 0, outputEquivalent: 0};
    for (const [tierKey, counts] of Object.entries(period.tokens || {})) {
      const perMillion = pick(tiers[tierKey]?.output_wh_per_mtok, scenario);
      const row = byTier[tierKey] = {tokens: 0, wh: 0};
      for (const category of Object.keys(CATEGORY_LABELS)) {
        const count = Number(counts[category]) || 0, wh = count / 1e6 * perMillion * ratio(category);
        const modeledCount = count * ratio(category);
        if (explain) terms.push({tier: tierKey, category, count, perMillion, baseRatio: category === 'output' ? 1 : pick(factors.ratios[category], scenario), ratio: ratio(category), outputEquivalent: modeledCount, wh});
        byCategory[category].tokens += count;
        byCategory[category].wh += wh;
        byCategory[category].outputEquivalent += modeledCount;
        row.tokens += count;
        row.wh += wh;
        total += wh;
        outputEquivalent += modeledCount;
      }
    }
    const grams = wh => wh * pue * grid / 1000;
    return {byCategory, byTier, terms, outputEquivalent, pue, wh: total, facilityWh: total * pue, grams, co2: grams(total)};
  }

  function settings(factors) {
    const scenario = SCENARIOS.some(([key]) => key === prefs.scenario) ? prefs.scenario : 'central';
    const pueScenario = SCENARIOS.some(([key]) => key === prefs.pueScenario) ? prefs.pueScenario : 'central';
    const inputSensitivity = INPUT_SENSITIVITIES.some(([key]) => key === prefs.inputSensitivity) ? prefs.inputSensitivity : 'current';
    const grid = factors.grids.find(item => item.key === prefs.grid) || (prefs.grid === 'custom' ? null : factors.grids.find(item => item.key === factors.default_grid));
    const gridValue = grid ? grid.g_co2e_per_kwh : Math.max(0, Number(prefs.customGrid) || 0);
    return {scenario, pueScenario, inputSensitivity, inputScale: inputSensitivity === 'third' ? 5 / 3 : 1,
      grid, gridValue, pue: pick(factors.pue, pueScenario)};
  }

  function segmented(label, options, current, onChange) {
    const group = node('div', 'carbon-segmented');
    group.setAttribute('role', 'group');
    group.setAttribute('aria-label', label);
    for (const [key, text] of options) {
      const choice = node('button', '', text);
      choice.type = 'button';
      choice.dataset.carbonControl = label.toLowerCase();
      choice.dataset.carbonValue = key;
      choice.setAttribute('aria-pressed', String(key === current));
      choice.onclick = () => onChange(key);
      group.append(choice);
    }
    return group;
  }

  const INFO = {
    assumptions: {
      title: 'Calculation assumptions',
      lead: 'Dotted boxes contain assumptions. Click any box to see the selected factor, its rationale, and the research sources behind it.',
      sections: [['Reading the calculation', 'Reported token counts come from reconciled usage logs. Output-equivalence ratios, model-tier energy, facility overhead, and grid intensity are assumptions. Solid boxes identify the defined output normalization; Wh-to-kWh is an exact unit conversion. Calculated quantities are estimates.']],
    },
    grid: {
      title: 'Electricity grid', lead: 'The grid factor converts facility electricity into estimated operational CO₂e.',
      sections: [
        ['The calculation', 'Facility kWh × grid grams CO₂e per kWh gives grams CO₂e. Changing the grid changes emissions, not electricity use.'],
        ['Location and accounting', 'The default US average is a location-based proxy because the serving region is generally undisclosed. Regional factors describe the electricity mix in a place; the Google contracts option is a market-based example and should only be used for comparable procurement.'],
      ],
      example: 'At 350 g CO₂e/kWh, 1 kWh of facility electricity gives 350 g CO₂e.',
      sources: [['EPA eGRID summary data', 'https://www.epa.gov/egrid/summary-data'], ['Watershed on grid accounting', 'https://watershed.com/en-GB/blog/ai-emissions-by-region']],
    },
    customGrid: {
      title: 'Custom grid intensity', lead: 'Enter a carbon intensity in grams of CO₂e per kilowatt-hour of electricity.',
      sections: [
        ['Use a documented factor', 'A location-based figure should match the actual serving region where possible. A market-based figure should reflect the provider’s qualifying electricity contracts. This app cannot determine either from token logs.'],
      ],
      sources: [['EPA eGRID summary data', 'https://www.epa.gov/egrid/summary-data']],
    },
    pue: {
      title: 'Data-center overhead (PUE)', lead: 'Power usage effectiveness is facility electricity divided by IT equipment electricity.',
      sections: [
        ['Why it matters', 'A PUE of 1.15 adds 15% to the estimated server energy for cooling, power distribution, and other facility overhead. It is applied after token energy is calculated.'],
        ['The choices', 'Low, central, and high come from the cited data-center factors. This setting is independent of the estimate range so you can test overhead separately.'],
      ],
      example: '100 Wh of server electricity × 1.15 PUE = 115 Wh at the facility.',
      sources: [['Google data-center efficiency', 'https://datacenters.google/efficiency/']],
    },
    estimate: {
      title: 'Energy scenario', lead: 'Select the low, central, or high published-model assumptions used for token energy.',
      sections: [
        ['What changes', 'The switch changes model-tier energy and token-type ratios together.'],
        ['How to read the range', 'The low–high span combines the factor extremes. It is a sensitivity range, not a statistical confidence interval or a direct measurement of the provider’s servers.'],
      ],
    },
    inputSensitivity: {
      title: 'Input sensitivity',
      sections: [
        ['What it measures', 'Prefill processes fresh prompt tokens, often in parallel; decode generates output tokens sequentially. At the central estimate, Labs treats one output token as using 5× the energy of one fresh input token. Watershed uses 3×. The ratio can vary with model, context length, and serving conditions.'],
        ['Labs · 5:1', 'This estimate uses Anthropic’s published input-to-output API price ratio as an energy proxy. Prices can reflect different serving costs, but also include commercial choices; they are not direct electricity measurements.'],
        ['Watershed · 3:1', 'Watershed’s Activity Tier lists 0.32 Wh per 1,000 input tokens and 0.96 Wh per 1,000 output tokens. It derives its input factor from Google’s measured typical prompt energy, an assumed prompt length, and a 3:1 decode-to-prefill ratio. Google did not publish that prompt’s token split, so the per-token factors remain modeled estimates.'],
        ['Why both are useful', 'The Labs proxy follows a provider’s observed pricing; Watershed starts from published prompt electricity data and a phase-energy model. Neither directly measures per-token energy for the proprietary models in this card. Selecting either ratio keeps output energy and the relative cache discounts fixed; the low and high estimates retain their spread.'],
      ],
      example: 'In this card, a large model at Central counts fresh input at 180 Wh per million tokens with Labs or 300 Wh with Watershed. Cache reads are 18 or 30 Wh per million tokens, respectively; output remains 900 Wh.',
      sources: [['Watershed AI emissions framework, Table 2', 'https://cdn.sanity.io/files/3ogo9b9g/production/a5c1f64ca5864e61b47e6384ee4d0ed31bc861f4.pdf#page=24'], ['Anthropic pricing', 'https://platform.claude.com/docs/en/about-claude/pricing']],
    },
    vehicle: {
      title: 'Vehicle comparison', lead: 'This comparison divides the current CO₂e estimate by emissions per mile for the chosen vehicle.',
      sections: [
        ['What changes', 'Only the comparison in miles changes. It does not change the AI electricity or CO₂e estimate.'],
        ['Boundaries', 'Gasoline examples use stated miles-per-gallon assumptions. The electric-car example uses a fixed US-average grid rather than the grid selected for AI. Vehicle manufacture and upstream fuel emissions are excluded.'],
      ],
      sources: [['EPA greenhouse-gas equivalencies', 'https://www.epa.gov/energy/greenhouse-gas-equivalencies-calculator-calculations-and-references'], ['EPA electric-vehicle comparison', 'https://www.epa.gov/greenvehicles/comparison-your-car-vs-electric-vehicle']],
    },
    flight: {
      title: 'Flight comparison', lead: 'Choose airports to compare the current CO₂e estimate with one-way economy passenger flights.',
      sections: [
        ['How it works', 'The app measures approximate great-circle distance between airports and multiplies by an EPA passenger-mile combustion factor for short, medium, or long flights.'],
        ['Limits', 'The comparison does not change the AI estimate. It excludes actual routing, seat class, upstream fuel emissions, and non-CO₂ warming effects at altitude.'],
      ],
      sources: [['EPA 2025 emissions factors, Table 10', 'https://www.epa.gov/system/files/documents/2025-01/ghg-emission-factors-hub-2025.pdf']],
    },
  };

  function openInfo(topic, trigger, key = '') {
    const info = INFO[topic] || (topic === 'tier' ? {title: factorsData?.tiers.find(tier => tier.key === key)?.label} : topic === 'ratio' ? {title: `${CATEGORY_LABELS[key]?.[0]} relative energy`} : null);
    if (!info) return;
    const dialog = node('dialog', 'carbon-info-dialog');
    dialog.setAttribute('aria-labelledby', 'carbon-info-title');
    const head = node('div', 'carbon-info-head'), titleBlock = node('div');
    titleBlock.append(node('p', 'carbon-info-eyebrow', 'Carbon footprint · guide'), node('h2', '', info.title));
    titleBlock.querySelector('h2').id = 'carbon-info-title';
    const close = node('button', 'carbon-info-close', '×');
    close.type = 'button';
    close.setAttribute('aria-label', 'Close information');
    close.onclick = () => dialog.close();
    head.append(titleBlock, close);
    const body = node('div', 'carbon-info-body');
    if (factorsData && ['tier', 'ratio', 'pue', 'grid', 'customGrid', 'inputSensitivity', 'estimate'].includes(topic)) {
      body.append(factorEvidence(factorsData, settings(factorsData), topic, key, false));
    } else {
    body.append(node('p', 'carbon-info-lead', info.lead));
    for (const [heading, copy] of info.sections || []) {
      const section = node('section', 'carbon-info-section');
      section.append(node('h3', '', heading), node('p', '', copy));
      body.append(section);
    }
    if (info.example) body.append(node('div', 'carbon-info-example', info.example));
    if (info.sources?.length) {
      const sources = node('div', 'carbon-info-sources');
      sources.append(node('strong', '', 'Read the sources: '));
      info.sources.forEach(([label, url], index) => {
        if (index) sources.append(' · ');
        const link = node('a', '', label);
        link.href = url;
        link.target = '_blank';
        link.rel = 'noopener noreferrer';
        sources.append(link);
      });
      body.append(sources);
    }
    }
    dialog.append(head, body);
    dialog.addEventListener('click', event => { if (event.target === dialog) dialog.close(); });
    dialog.addEventListener('close', () => { dialog.remove(); if (trigger.isConnected) trigger.focus(); }, {once: true});
    document.body.append(dialog);
    dialog.showModal();
    close.focus();
  }

  function infoButton(topic, label) {
    const button = node('button', 'carbon-info-button');
    button.type = 'button';
    button.setAttribute('aria-label', `About ${label}`);
    button.title = `About ${label}`;
    const icon = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    icon.setAttribute('viewBox', '0 0 24 24');
    icon.setAttribute('aria-hidden', 'true');
    const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    use.setAttribute('href', '#ph-icon-info');
    icon.append(use);
    button.append(icon);
    button.onclick = () => openInfo(topic, button);
    return button;
  }

  function controlTitle(label, topic) {
    const title = node('div', 'carbon-control-title');
    title.append(node('span', '', label), infoButton(topic, label));
    return title;
  }

  function cite(factors, ids) {
    const span = node('span', 'carbon-cite');
    for (const id of ids || []) {
      const index = factors.sources.findIndex(source => source.id === id);
      if (index < 0) continue;
      const link = node('a', '', `[${index + 1}]`);
      link.href = factors.sources[index].url;
      link.target = '_blank';
      link.rel = 'noreferrer';
      link.title = factors.sources[index].title;
      span.append(link);
    }
    return span;
  }

  const precise = value => Number(Number(value || 0).toPrecision(10)).toLocaleString(undefined, {maximumFractionDigits: 10});
  const badge = (label, kind) => node('span', `carbon-provenance ${kind}`, label);

  function disclosure(key, title, cls = '') {
    const details = node('details', cls), summary = node('summary', '', title);
    details.dataset.viewKey='carbon:'+key;
    details.open = window.pharosView?.read('disclosures',{})[details.dataset.viewKey] ?? disclosureState.get(key) ?? false;
    summary.dataset.carbonControl = `disclosure:${key}`;
    details.ontoggle = () => { if (details.isConnected) disclosureState.set(key, details.open); };
    details.append(summary);
    return details;
  }

  function equation(parts) {
    const line = node('div', 'carbon-equation');
    for (const part of parts) line.append(part instanceof Node ? part : node('span', '', part));
    return line;
  }

  function quantity(value, unit) {
    const amount = node('span', 'carbon-quantity');
    amount.append(node('strong', '', value), ' ', node('small', '', unit));
    return amount;
  }

  function unitFraction(numerator, denominator) {
    const fraction = node('span', 'carbon-unit-fraction');
    fraction.setAttribute('role', 'math');
    fraction.setAttribute('aria-label', `${numerator} divided by ${denominator}`);
    fraction.append(node('span', 'carbon-fraction-top', numerator), node('span', 'carbon-fraction-slash', ' / '), node('span', 'carbon-fraction-bottom', denominator));
    return fraction;
  }

  const categoryUnit = category => ({uncached_input: 'uncached-input', cache_write: 'cache-write', cache_read: 'cache-read', output: 'output'}[category]);

  function sourceEvidence(factors, ids) {
    const list = node('ul', 'carbon-factor-sources');
    for (const id of [...new Set(ids || [])]) {
      const source = factors.sources.find(item => item.id === id);
      if (!source) continue;
      const item = node('li'), link = node('a', '', source.title);
      link.href = source.url; link.target = '_blank'; link.rel = 'noopener noreferrer';
      item.append(link, source.published ? ` · ${source.published}` : '', node('p', '', source.finding));
      list.append(item);
    }
    return list;
  }

  // Shared evidence for assumption help, ledger factors, and expanded rows.
  function factorEvidence(factors, setup, kind, key = '', heading = true) {
    const section = node('section', 'carbon-factor-evidence');
    let title, value, basis, range, sources = [], defined = false;
    if (kind === 'tier') {
      const tier = factors.tiers.find(item => item.key === key);
      title = `${tier.label} · server energy`;
      value = `${precise(pick(tier.output_wh_per_mtok, setup.scenario))} Wh per 1M output tokens`;
      basis = tier.basis; range = tier.output_wh_per_mtok; sources = tier.sources;
      section.append(node('p', '', `Model-name matches: ${tier.match.join(', ')}. This size tier is a proxy for the logged model, not a measurement of its serving hardware.${tier.key === factors.default_tier ? ' Models with unknown size use this tier by default.' : ''}`));
    } else if (kind === 'ratio') {
      title = `${CATEGORY_LABELS[key][0]} · relative energy`;
      defined = key === 'output';
      if (defined) {
        value = '1× output energy';
        basis = 'Output is the reference: its relative energy is 1 by definition. Every other category is expressed as a fraction of an output token’s energy.';
      } else {
        const ratio = factors.ratios[key], base = pick(ratio, setup.scenario);
        value = `${precise(base * setup.inputScale)}× output energy`;
        basis = ratio.basis; range = ratio; sources = ratio.sources;
        section.append(equation([`${precise(base)} base ratio`, '×', setup.inputSensitivity === 'third' ? '5/3 sensitivity' : '1 sensitivity', '≈', `${precise(base * setup.inputScale)} effective ratio`]),
          node('p', '', 'Input sensitivity scales uncached input, cache writes, and cache reads. Output stays at 1×. The alternative values below are the base ratios, before this multiplier.'));
      }
    } else if (kind === 'pue') {
      title = 'Facility overhead (PUE)'; value = `${precise(setup.pue)}× server electricity`;
      basis = factors.pue.basis; range = factors.pue; sources = factors.pue.sources;
      section.append(node('p', '', `Selected PUE setting: ${setup.pueScenario}. This is an assumed facility overhead for this usage.`));
    } else if (kind === 'grid' || kind === 'customGrid') {
      title = 'Electricity emissions factor'; value = `${precise(setup.gridValue)} g CO₂e per kWh`;
      basis = setup.grid?.basis || 'This factor was entered by you. No research source is attached to a custom value.';
      sources = setup.grid?.sources || [];
      section.append(node('p', '', setup.grid?.key === 'google_market'
        ? 'Market-based accounting: this factor credits electricity contracts. Applying it to this usage is an assumption about comparable procurement.'
        : setup.grid ? `${setup.grid.label} is the selected proxy for the electricity serving this usage; the actual inference location is generally undisclosed.` : 'User-supplied assumption.'));
    } else if (kind === 'inputSensitivity') {
      title = 'Input sensitivity'; value = setup.inputSensitivity === 'third' ? 'Watershed · 5/3× all non-output ratios' : 'Labs · 1× base ratios';
      basis = 'The Labs base ratios use pricing as an energy proxy. Watershed’s central input-to-output ratio is 1:3; scaling the Labs 1:5 ratio by 5/3 gives 1:3. The same multiplier applies to cache writes and reads in every energy scenario.';
      for (const [label, text] of INFO.inputSensitivity.sections) {
        const part = node('div'); part.append(node('h4', '', label), node('p', '', text)); section.append(part);
      }
      const links = node('p', 'carbon-factor-sources');
      for (const [label, url] of INFO.inputSensitivity.sources) {
        const link = node('a', '', label); link.href = url; link.target = '_blank'; link.rel = 'noopener noreferrer';
        links.append(link, ' ');
      }
      section.append(links);
    } else {
      title = 'Energy scenario'; value = SCENARIOS.find(([key]) => key === setup.scenario)[1];
      basis = 'Low, Central, and High select model-tier energy and non-output ratios together. PUE, electricity factor, and input sensitivity retain their separate settings. These are alternative assumptions, not statistical confidence bounds.';
    }
    const head = node('div', 'carbon-factor-heading');
    if (heading) head.append(node('h4', '', title));
    head.append(badge(defined ? 'Defined' : 'Assumed', defined ? 'defined' : 'assumed'), node('strong', '', value));
    section.prepend(head, node('p', '', basis));
    if (range) {
      const alternatives = node('dl', 'carbon-factor-alternatives');
      for (const [scenario, label] of SCENARIOS) alternatives.append(node('dt', '', label), node('dd', '', precise(pick(range, scenario))));
      section.append(alternatives);
    }
    if (sources.length) section.append(node('h4', '', 'Sources behind this assumption'), sourceEvidence(factors, sources));
    return section;
  }

  function factorLink(kind, key, text) {
    const button = node('button', 'carbon-factor-link');
    button.append(text);
    button.type = 'button'; button.dataset.carbonControl = `factor:${kind}:${key}`;
    button.setAttribute('aria-label', kind === 'assumptions' ? 'How assumption boxes work' : `Explain ${kind === 'tier' ? key + ' model-tier energy' : kind === 'ratio' ? CATEGORY_LABELS[key][0] + ' relative energy' : kind === 'pue' ? 'facility overhead' : 'electricity emissions factor'}`);
    if (kind === 'ratio' && key === 'output') button.classList.add('carbon-defined-box');
    else if (kind === 'assumptions' || text instanceof Node) button.classList.add('carbon-assumption-box');
    button.onclick = () => openInfo(kind, button, key);
    return button;
  }

  // Compact the ledger only; exact arithmetic remains in each tier's disclosure.
  function ledgerQuantity(value, unit, fullUnit = unit) {
    const amount = quantity(Math.abs(value) >= 1000 ? tokens(value) : sig(value), unit);
    amount.title = `${Number(value).toLocaleString(undefined, {maximumFractionDigits: 10})} ${fullUnit}`;
    amount.setAttribute('aria-label', amount.title);
    return amount;
  }

  function ledgerFraction(numerator, denominator, fullLabel) {
    const fraction = unitFraction(numerator, denominator);
    fraction.title = fullLabel;
    fraction.setAttribute('aria-label', fullLabel);
    return fraction;
  }

  function ledgerCell(row, label, content, operator = '') {
    const td = node('td'), math = node('div', 'carbon-math-cell'); td.dataset.label = label;
    if (operator) math.append(node('span', 'carbon-operator', operator));
    if (content) math.append(content);
    td.append(math); row.append(td);
  }

  function emissionsCells(row, result, setup, wh) {
    ledgerCell(row, 'Facility overhead (PUE)', factorLink('pue', '', ledgerFraction(`${precise(setup.pue)} fWh`, '1 sWh', `${precise(setup.pue)} facility Wh divided by 1 server Wh`)), '×');
    ledgerCell(row, 'Watt-hours to kilowatt-hours', ledgerFraction('1 kWh', '1k fWh', '1 kWh divided by 1,000 facility Wh'), '×');
    ledgerCell(row, 'Electricity emissions factor', factorLink('grid', '', ledgerFraction(`${sig(setup.gridValue)} g`, '1 kWh', `${precise(setup.gridValue)} g CO₂e divided by 1 kWh`)), '×');
    const grams = result.grams(wh), amount = quantity(mass(grams), '');
    amount.title = `${precise(grams)} g CO₂e`; amount.setAttribute('aria-label', amount.title);
    ledgerCell(row, 'Estimated CO₂e', amount, '=');
  }

  function rowEvidence(factors, setup, term) {
    const block = node('div', 'carbon-row-evidence');
    const ratioParts = term.category === 'output' ? '1 (defined output reference)' : setup.inputSensitivity === 'third' ? `(${precise(term.baseRatio)} × 5/3)` : precise(term.baseRatio);
    const unit = categoryUnit(term.category), outputEquivalent = term.outputEquivalent;
    block.append(node('h4', '', 'Substituted arithmetic'),
      equation([`${term.count.toLocaleString()} ${unit} tokens`, '×', unitFraction(`${ratioParts} OE tokens`, `1 ${unit} token`), '≈', `${precise(outputEquivalent)} output-equivalent tokens`]),
      node('p', 'carbon-note', 'Output-equivalent tokens express a modeled energy reference. The recorded output count stays unchanged.'),
      equation([`${precise(outputEquivalent)} output-equivalent tokens`, '×', unitFraction(`${precise(term.perMillion)} Wh`, '1M OE tokens'), '≈', `${precise(term.wh)} server Wh`]),
      equation([`${precise(term.wh)} server Wh`, '×', unitFraction(`${precise(setup.pue)} facility Wh`, '1 server Wh'), '≈', `${precise(term.wh * setup.pue)} facility Wh`]),
      equation([`${precise(term.wh * setup.pue)} facility Wh`, '×', unitFraction('1 kWh', '1,000 Wh'), '×', unitFraction(`${precise(setup.gridValue)} g CO₂e`, '1 kWh'), '≈', `${precise(term.wh * setup.pue * setup.gridValue / 1000)} g CO₂e`]),
      node('p', 'carbon-note', 'The divisions convert tokens to million-token units and Wh to kWh exactly. Estimates use full precision; displayed decimals may be rounded.'));
    if (term.category === 'uncached_input' && data.unclassified_tokens) block.append(node('p', 'carbon-note', `${Number(data.unclassified_tokens).toLocaleString()} unclassified tokens are already included in uncached input across this selection. Their category assignment is assumed; do not add them again.`));
    const evidence = node('div', 'carbon-row-factors');
    evidence.append(factorEvidence(factors, setup, 'tier', term.tier), factorEvidence(factors, setup, 'ratio', term.category));
    block.append(evidence);
    return block;
  }

  function calculationLedger(factors, result, setup, terms) {
    const scroll = node('div', 'carbon-ledger-scroll'), table = node('table', 'carbon-ledger'), head = node('tr');
    scroll.setAttribute('role', 'region'); scroll.setAttribute('aria-label', 'Token category calculation ledger'); scroll.tabIndex = 0;
    const columns = node('colgroup'); for (let index = 0; index < 10; index++) columns.append(node('col'));
    const caption = node('caption', '', 'Contributions by token category · Each tier shows the full equation. Click a tier for precise math; dotted factors for assumptions.');
    table.append(caption, columns);
    for (const [title, note] of [['Tier', 'Model size tier'], ['Tokens', 'Reported tokens from reconciled logs'], ['Equivalence', 'Output-equivalent tokens per reported token'], ['OE', 'Modeled output-equivalent tokens'], ['Tier energy', 'Server Wh per million output-equivalent tokens'], ['Server', 'Estimated server electricity in Wh'], ['PUE', 'Facility Wh per server Wh'], ['Wh → kWh', 'Exact watt-hour to kilowatt-hour conversion'], ['Grid', 'Grams of CO₂e per kilowatt-hour'], ['CO₂e', 'Estimated emissions']]) {
      const cell = node('th', '', title); cell.scope = 'col'; cell.title = note; cell.setAttribute('aria-label', note); head.append(cell);
    }
    table.createTHead().append(head);
    const tiers = bySize(factors.tiers), tierLabel = tier => tier.key === 'medium' ? 'Mid' : tier.label.replace(/ models$/i, '');
    const sumRow = (counts, wh, outputEquivalent) => {
      const row = node('tr', 'carbon-grand-total'), heading = node('th', '', 'Σ Total'); heading.scope = 'row'; heading.title = 'Sum of all categories'; row.append(heading);
      ledgerCell(row, 'Total reported tokens', ledgerQuantity(counts, 'tok', 'reported tokens'));
      ledgerCell(row, 'Output equivalence', null);
      ledgerCell(row, 'Total output-equivalent tokens', ledgerQuantity(outputEquivalent, 'OE', 'output-equivalent tokens'));
      ledgerCell(row, 'Tier energy', null);
      ledgerCell(row, 'Total server electricity', ledgerQuantity(wh, 'sWh', 'server Wh'));
      emissionsCells(row, result, setup, wh);
      return row;
    };
    for (const category of Object.keys(CATEGORY_LABELS)) {
      if (!terms.some(term => term.category === category && term.count > 0)) continue;
      const groupBody = node('tbody'); groupBody.dataset.category = category;
      const group = node('tr', 'carbon-ledger-group'), heading = node('th', '', CATEGORY_LABELS[category][0]);
      heading.colSpan = 10; heading.scope = 'rowgroup'; group.append(heading); groupBody.append(group);
      for (const tier of tiers) {
        const term = terms.find(term => term.tier === tier.key && term.category === category);
        if (!term?.count) continue;
        const key = `row:${tier.key}:${category}`, row = node('tr', 'carbon-term-row'), name = node('th'); name.scope = 'row';
        row.dataset.category = category; row.dataset.tier = tier.key;
        const detailRow = node('tr', 'carbon-term-evidence'), detailCell = node('td'); detailCell.colSpan = 10;
        detailRow.id = `carbon-row-${tier.key}-${term.category}`; detailRow.hidden = !disclosureState.get(key);
        const toggle = node('button', 'carbon-row-trigger', tierLabel(tier)); toggle.type = 'button';
        toggle.setAttribute('aria-label', `Explain ${CATEGORY_LABELS[category][0]} · ${tier.label}`);
        toggle.append(node('small', '', 'Details'));
        toggle.dataset.carbonControl = key; toggle.setAttribute('aria-expanded', String(!detailRow.hidden)); toggle.setAttribute('aria-controls', detailRow.id);
        toggle.onclick = () => {
          detailRow.hidden = !detailRow.hidden; disclosureState.set(key, !detailRow.hidden);
          if (!detailRow.hidden && !detailCell.firstChild) detailCell.append(rowEvidence(factors, setup, term));
          toggle.setAttribute('aria-expanded', String(!detailRow.hidden));
        };
        name.append(toggle); row.append(name);
        const ratio = Number(term.ratio.toPrecision(4));
        const ratioLink = factorLink('ratio', category, ledgerFraction(`${ratio === term.ratio ? '' : '≈ '}${precise(ratio)} OE`, '1 tok', `${precise(term.ratio)} OE tokens divided by 1 ${categoryUnit(category)} token`));
        const energyLink = factorLink('tier', tier.key, ledgerFraction(`${tokens(term.perMillion)} sWh`, '1M OE', `${precise(term.perMillion)} server Wh divided by 1M OE tokens`));
        ledgerCell(row, 'Reported tokens', ledgerQuantity(term.count, 'tok', `${categoryUnit(category)} tokens`));
        ledgerCell(row, 'Output equivalence', ratioLink, '×');
        ledgerCell(row, 'Modeled output-equivalent tokens', ledgerQuantity(term.outputEquivalent, 'OE', 'output-equivalent tokens'), '=');
        ledgerCell(row, 'Assumed tier energy', energyLink, '×');
        ledgerCell(row, 'Estimated server electricity', ledgerQuantity(term.wh, 'sWh', 'server Wh'), '=');
        emissionsCells(row, result, setup, term.wh);
        if (!detailRow.hidden) detailCell.append(rowEvidence(factors, setup, term));
        detailRow.append(detailCell); groupBody.append(row, detailRow);
      }
      table.append(groupBody);
    }
    const counts = Object.values(result.byCategory).reduce((sum, category) => sum + category.tokens, 0);
    table.createTFoot().append(sumRow(counts, result.wh, result.outputEquivalent));
    scroll.append(table);
    return scroll;
  }

  function calculationBreakdown(factors, result, setup, period) {
    const details = disclosure('calculation', 'Calculation breakdown', 'carbon-calculation');
    const summary = details.querySelector('summary'), counts = result.terms.reduce((sum, term) => sum + term.count, 0);
    summary.append(node('span', 'carbon-calculation-preview', `${tokens(counts)} tokens → ≈ ${tokens(result.outputEquivalent)} modeled output-equivalent tokens → ${energy(result.facilityWh)} → ${mass(result.co2)} CO₂e · Math and sources`));
    const fallbackCount = (Number(data.default_tier_unknown_tokens) || 0) + (data.default_tier_models || []).reduce((sum, model) => sum + Number(model.tokens || 0), 0);
    if (fallbackCount) summary.append(node('span', 'carbon-fallback-marker', `${tokens(fallbackCount)} tokens with unknown model size`));
    const content = node('div', 'carbon-calculation-body');
    content.append(node('p', 'carbon-calculation-scope', usageScope || usageLabel));
    const legend = node('div', 'carbon-calculation-legend');
    legend.append(badge('Reported · token logs', 'reported'), factorLink('assumptions', '', 'Assumption'), badge('Defined · constants', 'defined'), badge('Estimated · results', 'estimated'));
    content.append(legend);
    const overview = node('section', 'carbon-calculation-overview');
    overview.append(node('h4', '', 'Calculation at a glance'), equation([
      `${tokens(counts)} reported tokens`, '→ category ratios →', `≈ ${tokens(result.outputEquivalent)} modeled output-equivalent tokens`,
      '→ apply each tier’s energy factor, then sum →', `≈ ${precise(result.wh)} server Wh`
    ]), equation([
      `≈ ${precise(result.wh)} server Wh`, '×', `${precise(setup.pue)} PUE`, '→', `${precise(result.facilityWh)} facility Wh`,
      '÷', '1,000', '→', `${precise(result.facilityWh / 1000)} kWh`, '×', `${precise(setup.gridValue)} g CO₂e/kWh`, '→', `≈ ${mass(result.co2)} CO₂e`
    ]));
    content.append(overview);
    if (!counts) content.append(node('p', 'muted', 'No reported token usage matches these filters. There are no contributions to calculate.'));
    if (fallbackCount) {
      const fallback = disclosure('fallback', 'Why some models use the default tier', 'carbon-evidence-disclosure');
      const tier = factors.tiers.find(item => item.key === factors.default_tier);
      fallback.append(node('p', '', `Assigned to ${tier.label} by default; size unknown. This assignment is an assumption.`));
      const list = node('ul');
      for (const model of data.default_tier_models || []) list.append(node('li', '', `${model.model}: ${Number(model.tokens).toLocaleString()} tokens`));
      if (data.default_tier_unknown_tokens) list.append(node('li', '', `Model not reported: ${Number(data.default_tier_unknown_tokens).toLocaleString()} tokens`));
      fallback.append(list, factorEvidence(factors, setup, 'tier', factors.default_tier)); content.append(fallback);
    }
    if (data.unclassified_tokens) content.append(node('p', 'carbon-note', `${Number(data.unclassified_tokens).toLocaleString()} unclassified tokens are already included in uncached input. This category assignment is assumed; they are not added a second time.`));
    const one = node('section', 'carbon-calculation-step');
    one.append(node('h3', '', '1 · Calculate each contribution'),
      node('p', 'carbon-ledger-key', 'Key: tok = tokens · OE = output-equivalent tokens · sWh / fWh = server / facility watt-hours · kWh = kilowatt-hours · g / kg / t = grams / kilograms / tonnes CO₂e'),
      node('p', 'carbon-ledger-key', 'k = thousand · M = million · B = billion · T = trillion. CO₂e results scale to g / kg / t. Values are rounded; hover for precision or open tier details. Energy factors apply within each tier before summing.'));
    if (counts) one.append(calculationLedger(factors, result, setup, result.terms));
    one.append(equation(['Sum of all category contributions', '≈', `${precise(result.outputEquivalent)} modeled output-equivalent tokens`, 'and', `${precise(result.wh)} server Wh`]), badge('Estimated', 'estimated'));
    const two = node('section', 'carbon-calculation-step');
    two.append(node('h3', '', '2 · Include facility overhead'),
      equation([`${precise(result.wh)} server Wh`, '×', `${precise(setup.pue)} PUE`, '≈', `${precise(result.facilityWh)} facility Wh`]),
      factorLink('pue', '', `Why use ${precise(setup.pue)}× PUE?`),
      equation([`${precise(result.facilityWh)} Wh`, '÷', '1,000 Wh/kWh', '=', `${precise(result.facilityWh / 1000)} kWh`]), badge('Defined · exact unit conversion', 'defined'));
    const three = node('section', 'carbon-calculation-step');
    three.append(node('h3', '', '3 · Convert electricity to emissions'),
      equation([`${precise(result.facilityWh / 1000)} kWh`, '×', `${precise(setup.gridValue)} g CO₂e/kWh`, '≈', `${precise(result.co2)} g CO₂e`]),
      factorLink('grid', '', `Why use ${precise(setup.gridValue)} g CO₂e/kWh?`),
      node('p', 'carbon-note', `Headline: ${mass(result.co2)} CO₂e. Calculations use full precision; displayed contributions may not add exactly after rounding.`));
    content.append(one, two, three);
    const sensitivity = node('section', 'carbon-calculation-step'); sensitivity.append(node('h3', '', 'Sensitivity for this usage'));
    const scenarios = node('div', 'carbon-sensitivity');
    for (const [scenario, label] of SCENARIOS) {
      const alternative = calculate(factors, period, scenario, setup.pue, setup.gridValue, setup.inputScale);
      const item = node('span', scenario === setup.scenario ? 'selected' : '');
      item.append(`${label} `, node('strong', '', `${mass(alternative.co2)} CO₂e`)); scenarios.append(item);
    }
    sensitivity.append(scenarios, node('p', 'carbon-note', `Alternative energy assumptions, not confidence bounds. PUE ${precise(setup.pue)}, grid ${precise(setup.gridValue)} g CO₂e/kWh, and ${setup.inputSensitivity === 'third' ? 'Watershed' : 'Labs'} input sensitivity stay fixed.`));
    if (counts && !result.terms.some(term => term.category !== 'output' && term.count > 0)) sensitivity.append(node('p', 'carbon-note', 'Input sensitivity does not change this estimate: no non-output tokens are reported.'));
    content.append(sensitivity);
    const scope = disclosure('scope', 'Scope, bookkeeping, and limitations', 'carbon-evidence-disclosure');
    scope.append(node('p', '', 'This estimates inference electricity and operational emissions. Model training, embodied hardware emissions, water, networking, and your computer are outside this estimate. Only retained, reconciled token usage is included; linked mirror workspaces are removed.'),
      node('p', '', 'The calculation uses filtered totals. Chart interval and breakdown change the display only. Output includes reasoning; unclassified tokens are charged as uncached input.'));
    if (data.undated_tokens) scope.append(node('p', '', `${Number(data.undated_tokens).toLocaleString()} undated tokens are included in the total but cannot be placed on the chart.`));
    const caveats = node('ul'); for (const text of factors.caveats || []) caveats.append(node('li', '', text)); scope.append(caveats);
    content.append(scope, comparisonCalculation(factors, result), methodology(factors, result, setup));
    details.append(content);
    return details;
  }

  function comparisonCalculation(factors, result) {
    const details = disclosure('comparisons', 'Everyday comparison math and sources', 'carbon-evidence-disclosure');
    if (!result.wh) { details.append(node('p', '', 'No comparison quantities are calculated for empty usage.')); return details; }
    const {vehicle, phone, from, to, miles, flight} = comparisonSettings(factors);
    details.append(node('p', '', 'These comparisons give context to the estimate. Their factors and boundaries are separate from the assumptions about AI electricity.'));
    if (vehicle) details.append(node('h4', '', vehicle.label),
      equation([`${precise(result.co2)} g CO₂e`, '÷', `${precise(vehicle.g_co2e_per_unit)} g CO₂e/mile`, '≈', `${sig(result.co2 / vehicle.g_co2e_per_unit)} miles`]),
      badge('Assumed · comparison factor', 'assumed'), sourceEvidence(factors, vehicle.sources));
    if (phone) details.append(node('h4', '', 'Smartphone charges'),
      equation([`${precise(result.co2)} g CO₂e`, '÷', `${precise(phone.g_co2e_per_unit)} g CO₂e/charge`, '≈', `${sig(result.co2 / phone.g_co2e_per_unit)} charges`]),
      sourceEvidence(factors, phone.sources));
    if (flight) details.append(node('h4', '', `One-way flight: ${from.label} → ${to.label}`),
      equation([`≈ ${precise(miles)} passenger-miles`, '×', `${precise(flight.g_co2e_per_unit)} g CO₂e/passenger-mile`, '≈', `${precise(miles * flight.g_co2e_per_unit)} g CO₂e/flight`]),
      equation([`${precise(result.co2)} g CO₂e`, '÷', `${precise(miles * flight.g_co2e_per_unit)} g CO₂e/flight`, '≈', `${sig(result.co2 / (miles * flight.g_co2e_per_unit))} flights`]),
      badge('Assumed · passenger-mile emissions factor', 'assumed'), sourceEvidence(factors, [...flight.sources, 'faa-airports']));
    details.append(node('p', '', 'Flight distance is computed between reference airports, not an actual route. Flight comparisons exclude seat-class differences, upstream fuel emissions, and non-CO₂ effects at altitude. Vehicle comparisons exclude manufacture and fuel production; the electric-car factor uses a fixed US-average grid rather than the selected AI grid.'));
    return details;
  }

  function methodology(factors, result, setup) {
    const details = node('details', 'carbon-method');
    details.dataset.viewKey='carbon:reference';
    details.open = window.pharosView?.read('disclosures',{})[details.dataset.viewKey] ?? disclosureState.get('reference') ?? false;
    details.ontoggle = () => { if (details.isConnected) disclosureState.set('reference', details.open); };
    const summary = node('summary', '', 'All factor settings and sources');
    summary.dataset.carbonControl = 'disclosure:reference';
    details.append(summary);
    const formula = node('p');
    formula.append('For each model tier and token type: ', node('code', '', 'tokens ÷ 1M × Wh per 1M output tokens × token-type ratio × PUE ÷ 1,000 × grid g CO₂e/kWh'),
      `. Energy shown includes data-center overhead (PUE). This estimates inference electricity emissions, not a direct measurement; providers do not publish per-token energy. Model training, embodied hardware emissions, water, networking, and your own computer are outside this estimate. The estimate switch selects the model energy and token-type ratios; the PUE and grid can be selected separately. Input sensitivity: ${setup.inputSensitivity === 'third' ? 'Watershed (3:1 at Central)' : 'Labs (5:1 at Central)'}.`);
    details.append(node('h4', '', 'Formula'), formula);

    details.append(node('h4', '', 'Model tiers · server energy per 1M output tokens'));
    const tiers = node('table', 'carbon-table'), tierHead = node('tr'), tierBody = node('tbody');
    for (const text of ['Tier', 'Low', 'Central', 'High', 'Basis']) tierHead.append(node('th', '', text));
    tiers.createTHead().append(tierHead);
    for (const tier of bySize(factors.tiers)) {
      const tr = node('tr'), name = node('td', '', tier.label), basis = node('td', 'carbon-basis', tier.basis);
      name.append(node('span', 'carbon-sub', `Matches: ${tier.match.join(', ')}${tier.key === factors.default_tier ? ' · and unrecognized models' : ''}`));
      basis.append(cite(factors, tier.sources));
      tr.append(name, ...['low', 'central', 'high'].map(key => node('td', '', `${sig(tier.output_wh_per_mtok[key])} Wh`)), basis);
      tierBody.append(tr);
    }
    tiers.append(tierBody);
    details.append(tiers);

    details.append(node('h4', '', 'Base token-type ratios · before input sensitivity'));
    const ratios = node('table', 'carbon-table'), ratioHead = node('tr'), ratioBody = node('tbody');
    for (const text of ['Token type', 'Low', 'Central', 'High', 'Basis']) ratioHead.append(node('th', '', text));
    ratios.createTHead().append(ratioHead);
    const ratioRows = [...Object.keys(CATEGORY_LABELS).filter(key => factors.ratios[key]).map(key => [CATEGORY_LABELS[key][0], factors.ratios[key]]),
      ['PUE (data-center overhead)', factors.pue]];
    for (const [label, ratio] of ratioRows) {
      const tr = node('tr'), basis = node('td', 'carbon-basis', ratio.basis);
      basis.append(cite(factors, ratio.sources));
      tr.append(node('td', '', label), ...['low', 'central', 'high'].map(key => node('td', '', label.startsWith('PUE') ? sig(ratio[key]) : `${sig(ratio[key])}×`)), basis);
      ratioBody.append(tr);
    }
    ratios.append(ratioBody);
    details.append(ratios);

    details.append(node('h4', '', 'Grid carbon intensity'));
    const grids = node('ul');
    for (const grid of factors.grids) {
      const item = node('li');
      item.append(node('strong', '', `${grid.label}: ${sig(grid.g_co2e_per_kwh)} g CO₂e/kWh. `), grid.basis, cite(factors, grid.sources));
      grids.append(item);
    }
    details.append(grids);

    details.append(node('h4', '', `Sources · retrieved ${factors.retrieved_at}`));
    const sources = node('ol', 'carbon-source-index');
    for (const source of factors.sources) {
      const item = node('li'), link = node('a', '', source.title);
      link.href = source.url;
      link.target = '_blank';
      link.rel = 'noreferrer';
      item.append(link, source.published ? ` (${source.published})` : '', `: ${source.finding}`);
      sources.append(item);
    }
    details.append(sources);
    return details;
  }

  function updatePreferences(change) {
    Object.assign(prefs, change);
    save(change);
    syncSettingsURL(); render();
    // Usage rows carry estimates under the saved settings, so listeners that
    // refetch must wait for the save rather than the debounced write.
    Promise.resolve(window.pharosPrefs.flush?.()).then(() => window.dispatchEvent(new Event('pharos:carbon-settings')));
  }

  // A linked estimate must be independent of the next reader's preferences.
  // Visiting the link applies an override without changing saved settings.
  function settingsSnapshot(factors = factorsData) {
    if (!factors) return {...prefs};
    const setup = settings(factors), comparison = comparisonSettings(factors);
    return {scenario: setup.scenario, pueScenario: setup.pueScenario, inputSensitivity: setup.inputSensitivity,
      grid: setup.grid?.key ?? 'custom', customGrid: Math.max(0, Number(prefs.customGrid) || 0),
      vehicle: comparison.vehicleKey, flightFrom: comparison.from.key, flightTo: comparison.to.key};
  }

  function syncSettingsURL() {
    if (typeof location === 'undefined' || typeof history === 'undefined') return;
    const url = new URL(location.href);
    if (url.pathname !== '/analyze' || !(url.searchParams.get('analyze_topic') === 'carbon' || url.searchParams.get('analyze_view') === 'carbon')) return;
    url.searchParams.set('analyze_carbon', JSON.stringify(settingsSnapshot()));
    if (url.href !== location.href) {
      history.replaceState(history.state, '', url);
      window.dispatchEvent(new Event('pharos:uri-changed'));
    }
  }

  function restoreFocus(focused, ...roots) {
    if (focused?.control) roots.flatMap(root => [...root.querySelectorAll('[data-carbon-control]')])
      .find(element => element.dataset.carbonControl === focused.control && element.dataset.carbonValue === focused.value)?.focus({preventScroll: true});
  }

  function assumptionControls(factors) {
    const setup = settings(factors), update = updatePreferences;
    const assumptions = node('section', 'carbon-assumptions');
    assumptions.setAttribute('aria-label', 'Estimate assumptions');
    assumptions.append(node('h3', '', 'Estimate assumptions'));
    const controls = node('div', 'carbon-controls');
    const gridLabel = node('div', 'carbon-control'), gridSelect = node('select');
    gridSelect.dataset.carbonControl = 'grid';
    gridSelect.setAttribute('aria-label', 'Electricity grid');
    for (const grid of factors.grids) {
      const option = node('option', '', `${grid.label} · ${sig(grid.g_co2e_per_kwh)} g/kWh`);
      option.value = grid.key;
      gridSelect.append(option);
    }
    const custom = node('option', '', 'Custom…');
    custom.value = 'custom';
    gridSelect.append(custom);
    gridSelect.value = setup.grid ? setup.grid.key : 'custom';
    gridSelect.onchange = () => update({grid: gridSelect.value, customGrid: gridSelect.value === 'custom' ? (prefs.customGrid || setup.gridValue) : prefs.customGrid});
    gridLabel.append(controlTitle('Electricity grid', 'grid'), gridSelect);
    controls.append(gridLabel);
    if (!setup.grid) {
      const customLabel = node('div', 'carbon-control'), input = node('input');
      input.type = 'number';
      input.setAttribute('aria-label', 'Custom grid intensity in g CO₂e per kWh');
      input.min = '0';
      input.step = '1';
      input.value = String(setup.gridValue);
      input.dataset.carbonControl = 'customGrid';
      input.onchange = () => update({customGrid: Math.max(0, Number(input.value) || 0)});
      customLabel.append(controlTitle('g CO₂e per kWh', 'customGrid'), input);
      controls.append(customLabel);
    }
    const pueLabel = node('div', 'carbon-control');
    pueLabel.append(controlTitle(`PUE · ${sig(setup.pue)}`, 'pue'));
    pueLabel.append(segmented('PUE', SCENARIOS, setup.pueScenario, key => update({pueScenario: key})));
    const scenarioLabel = node('div', 'carbon-control');
    scenarioLabel.append(controlTitle('Energy scenario', 'estimate'));
    scenarioLabel.append(segmented('Estimate', SCENARIOS, setup.scenario, key => update({scenario: key})));
    const inputLabel = node('div', 'carbon-control');
    inputLabel.append(controlTitle('Input sensitivity', 'inputSensitivity'),
      segmented('Input sensitivity', INPUT_SENSITIVITIES, setup.inputSensitivity, key => update({inputSensitivity: key})));
    controls.append(pueLabel, scenarioLabel, inputLabel);
    assumptions.append(controls);
    return assumptions;
  }

  function render() {
    const root = document.getElementById('carbonCard'), summary = document.getElementById('carbonSummary'), assumptionsRoot = document.getElementById('carbonAssumptions');
    if (!root || !summary || !assumptionsRoot) return;
    const active = document.activeElement;
    const focused = active && [root, summary, assumptionsRoot].some(element => element.contains(active))
      ? {control: active.dataset.carbonControl, value: active.dataset.carbonValue} : null;
    if (factorsData) assumptionsRoot.replaceChildren(assumptionControls(factorsData));
    else assumptionsRoot.replaceChildren(node('h3', '', 'Estimate assumptions'), node('p', 'muted', error ? 'Assumption factors are unavailable.' : 'Loading assumption factors…'));
    if (!data) {
      const title = node('div', 'carbon-title');
      title.append(node('div', 'meta', 'Estimated CO₂e'), node('div', 'big', error ? 'Unavailable' : '…'));
      summary.replaceChildren(title);
      if (error) summary.append(node('div', 'query-table-error', error));
      summary.setAttribute('aria-busy', String(!error));
      root.replaceChildren();
      restoreFocus(focused, root, summary, assumptionsRoot);
      return;
    }
    summary.removeAttribute('aria-busy');
    const factors = data.factors, period = data.windows[0];
    const setup = settings(factors);
    const result = calculate(factors, period, setup.scenario, setup.pue, setup.gridValue, setup.inputScale, true);
    const update = updatePreferences;

    const head = node('div', 'carbon-head'), hero = node('div', 'carbon-hero');
    const titleRow = node('div', 'carbon-title-row'), title = node('div', 'carbon-title');
    title.append(node('div', 'meta', `Estimated CO₂e · ${usageLabel}`),
      node('div', 'big', `${mass(result.co2)} CO₂e`));
    const electricity = node('div', 'carbon-electricity');
    electricity.append(node('div', 'meta', 'Electricity'), node('div', 'big', energy(result.facilityWh)));
    const facts = node('div', 'carbon-facts');
    const fact = (value, text) => { const span = node('span'); span.append(node('b', '', value), ` ${text}`); facts.append(span); };
    const {vehicleKey, vehicle, phone, from, to, miles, flight} = comparisonSettings(factors);
    if (result.wh && vehicle) {
      const row = node('div', 'carbon-comparison'), vehicleSelect = node('select');
      vehicleSelect.dataset.carbonControl = 'vehicle';
      vehicleSelect.setAttribute('aria-label', 'Vehicle comparison');
      for (const [key, label] of VEHICLES) if (equivalent(factors, key)) {
        const option = node('option', '', label); option.value = key; option.title = equivalent(factors, key).label; vehicleSelect.append(option);
      }
      vehicleSelect.value = vehicleKey;
      vehicleSelect.title = vehicle.label;
      vehicleSelect.onchange = () => update({vehicle: vehicleSelect.value});
      row.append(node('b', '', `≈ ${sig(result.co2 / vehicle.g_co2e_per_unit)} miles`), 'in', vehicleSelect, infoButton('vehicle', 'vehicle comparison'));
      facts.append(row);
    }
    if (result.wh && phone) fact(`≈ ${sig(result.co2 / phone.g_co2e_per_unit)}`, phone.label);
    if (result.wh && flight) {
      const row = node('div', 'carbon-comparison');
      const citySelect = (label, current, other, key) => {
        const select = node('select');
        select.dataset.carbonControl = key;
        select.setAttribute('aria-label', label);
        select.title = current.label;
        for (const city of CITIES) if (city.key !== other.key) {
          const option = node('option', '', city.code || city.key.toUpperCase()); option.value = city.key; option.title = city.label; select.append(option);
        }
        select.value = current.key;
        select.onchange = () => update({[key]: select.value});
        return select;
      };
      const route = node('span', 'carbon-flight-route');
      route.append(citySelect('Flight from', from, to, 'flightFrom'), ' → ',
        citySelect('Flight to', to, from, 'flightTo'), infoButton('flight', 'flight comparison'));
      row.append(node('b', '', `≈ ${sig(result.co2 / (miles * flight.g_co2e_per_unit))} one-way flights`), route);
      facts.append(row);
    }
    titleRow.append(title, electricity);
    const comparisons = node('div', 'carbon-comparisons');
    comparisons.append(node('div', 'carbon-comparisons-title', 'Equivalent to'), facts);
    hero.append(titleRow);
    if (result.wh) hero.append(comparisons);
    head.append(hero);
    summary.replaceChildren(head);
    root.replaceChildren();

    root.append(calculationBreakdown(factors, result, setup, period));
    restoreFocus(focused, root, summary, assumptionsRoot);
  }

  function show(body, label, scope) {
    data = body;
    factorsData = body.factors;
    error = '';
    usageLabel = label || 'all time';
    usageScope = scope || usageLabel;
    syncSettingsURL();
    render();
  }

  function pending(message = '') {
    data = null;
    error = message;
    render();
  }

  function estimate(factors, period) {
    const setup = settings(factors);
    return calculate(factors, period, setup.scenario, setup.pue, setup.gridValue, setup.inputScale);
  }

  const sheet = node('style');
  sheet.textContent = CSS;
  document.head.append(sheet);
  window.pharosCarbon = {show, pending, estimate, mass, energy, settingsSnapshot};
  window.addEventListener('pharos:route', () => {
    for (const key of Object.keys(prefs)) delete prefs[key];
    Object.assign(prefs, routeSettings() || load());
    render();
    window.dispatchEvent(new Event('pharos:carbon-settings'));
  });
  window.dispatchEvent(new Event('pharos:carbon-ready'));
})();
