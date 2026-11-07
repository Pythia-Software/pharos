// Source choice and consent stay separate from discovery and DOM rendering.
// providers are GET /api/providers' list. Setup offers those marked setup, in
// their order, as tools: a provider shown with another (Cursor CLI with
// Cursor) is one tool of two kinds. A niche tool (when_found) is listed only
// when it is found here, and can still be chosen under More tools.
function createPharosSourceWizard(providers=[]) {
  const supported=providers.filter(provider=>provider.setup).map(provider=>provider.kind);
  const catalog=providers.filter(provider=>provider.setup&&!provider.setup_with).map(provider=>({
    kind:provider.kind,name:provider.display_name,summary:provider.summary||'',hint:provider.folder_hint||'',whenFound:!!provider.when_found,
    locations:provider.locations||[],variables:provider.variables||[],
    kinds:[provider.kind,...providers.filter(other=>other.setup&&other.setup_with===provider.kind).map(other=>other.kind)],
  }));
  const toolOf=kind=>catalog.find(tool=>tool.kinds.includes(kind));
  const key=c=>c.kind+'\0'+c.path;
  return {
    mode:'scan', tools:[], detected:[], detectionReady:false, locations:[], report:null, decisions:new Map(), names:{},
    catalog, providers,
    tool:toolOf,
    capabilities() {return pharosCapabilityRows(providers,this.tools)},
    // What a kind never records, for the review.
    notRecorded(kind) {return (providers.find(provider=>provider.kind===kind)?.capabilities||[]).filter(capability=>capability.support==='unsupported').map(capability=>capability.label)},
    exports() {return providers.filter(provider=>provider.export).map(provider=>({kind:provider.kind,name:provider.display_name,how:provider.export}))},
    detect(tools) {
      this.detected=supported.filter(kind=>tools.some(tool=>tool.kind===kind));
      if(!this.detectionReady)this.tools=this.found().flatMap(tool=>tool.kinds);
      this.detectionReady=true;
    },
    found() {return catalog.filter(tool=>tool.kinds.some(kind=>this.detected.includes(kind)))},
    // Found tools; the others setup lists; and the niche ones it does not.
    toolGroups() {
      const found=this.found();
      return {found:found.map(tool=>tool.kind),offered:catalog.filter(tool=>!found.includes(tool)&&!tool.whenFound).map(tool=>tool.kind),more:catalog.filter(tool=>!found.includes(tool)&&tool.whenFound).map(tool=>tool.kind)};
    },
    // Look in the usual places of every found tool.
    scan() {this.mode='scan';this.tools=this.found().flatMap(tool=>tool.kinds);this.locations=[];this.report=null},
    // Check only the folders named for the chosen tools.
    manual() {this.mode='manual';this.locations=[];this.report=null},
    choose(kind,checked) {
      const tool=toolOf(kind);
      if(!tool)throw Error('Unsupported tool');
      this.tools=supported.filter(other=>tool.kinds.includes(other)?checked:this.tools.includes(other));
      this.locations=this.locations.filter(location=>this.tools.includes(location.kind));
      this.report=null;
    },
    chosen() {return catalog.filter(tool=>tool.kinds.some(kind=>this.tools.includes(kind)))},
    // A folder named for kind, replacing any named before; blank removes it.
    name(kind,path) {
      this.locations=this.locations.filter(location=>location.kind!==kind);
      if(path.trim())this.locations.push({kind,path:path.trim()});
      this.report=null;
    },
    discovered(report) {
      this.report={...report,candidates:report.candidates.filter(c=>this.tools.includes(c.kind))};
      for(const candidate of this.report.candidates)if(!this.decisions.has(key(candidate)))this.decisions.set(key(candidate),candidate.recommended===true);
    },
    set(candidate,checked) {this.decisions.set(key(candidate),checked)},
    checked(candidate) {return this.decisions.get(key(candidate))===true},
    selected() {return (this.report?.candidates||[]).filter(c=>c.acceptable&&this.checked(c)&&!(c.configured?.enabled||c.configured&&c.configured.scope!=='host'))},
    request() {
      if(this.mode!=='manual')return {kinds:[...this.tools],locations:this.locations.map(location=>({...location}))};
      const named=this.locations.filter(location=>this.tools.includes(location.kind));
      return {kinds:supported.filter(kind=>named.some(location=>location.kind===kind)),locations:named.map(location=>({...location})),only_locations:true};
    },
    selection() {
      const selected=this.selected(), accept=selected.map(c=>c.name);
      const decline=(this.report?.candidates||[]).filter(c=>!c.configured&&c.acceptable&&c.status==='found'&&!accept.includes(c.name)).map(c=>c.name);
      return {...this.request(),accept,decline,names:{...this.names}};
    },
  };
}

// What the chosen tools' histories hold: a row for each capability, with a
// cell for each tool (support and note), in the providers' order.
function pharosCapabilityRows(providers,kinds) {
  const chosen=kinds.map(kind=>providers.find(provider=>provider.kind===kind)).filter(Boolean);
  if(!chosen.length)return[];
  return chosen[0].capabilities.map(({key,label})=>({key,label,cells:chosen.map(provider=>{
    const capability=provider.capabilities.find(item=>item.key===key)||{support:'unsupported'};
    return {kind:provider.kind,name:provider.display_name,support:capability.support,note:capability.note||''};
  })}));
}

// Source setup UI.
// Detect installed tools and familiar data folders before offering choices. The same
// guide runs inside application setup and from Settings → Sources. Approval
// saves this Mac's choices, captures only the accepted sources, then offers
// indexing. The CLI retains its separate full-discovery probe.
(() => {
  'use strict';
  const DISMISSED = 'pharos-onboarding-dismissed:';
  // Display names, folder hints and providers by kind, from GET
  // /api/providers once setup has loaded it.
  let NAMES = {}, HINTS = {}, PROVIDERS = {};
  const toolName = kind => NAMES[kind] || kind;
  const learn = providers => {
    NAMES = Object.fromEntries(providers.map(provider => [provider.kind, provider.display_name]));
    HINTS = Object.fromEntries(providers.map(provider => [provider.kind, provider.folder_hint || '']));
    PROVIDERS = Object.fromEntries(providers.map(provider => [provider.kind, provider]));
  };
  const CSS = `
.source-setup-open .application-body{overflow:hidden}
.source-setup-open .application-body>:not(#pharosOnboarding){display:none!important}
.source-setup-open .application-body>#pharosOnboarding{display:flex;flex:1;min-height:0}
.pharos-onboard{position:fixed;inset:0;margin:auto;width:min(1040px,calc(100% - 48px));height:min(720px,calc(100dvh - 64px));max-width:none;max-height:calc(100dvh - 64px);padding:32px 40px;box-sizing:border-box;display:flex;flex-direction:column;background:var(--panel,#fff);color:var(--ink,#222);border:1px solid var(--line,#ccc);border-radius:12px;box-shadow:0 24px 70px #0006;overflow:hidden;font:14px/1.55 var(--sans,sans-serif)}
.pharos-onboard::backdrop{background:color-mix(in srgb,var(--bg) 52%,transparent);backdrop-filter:blur(14px);-webkit-backdrop-filter:blur(14px)}
.pharos-onboard.pharos-embedded{position:static;margin:0;width:100%;height:100%;max-height:none;padding:0;border:0;border-radius:0;box-shadow:none}
.pharos-onboard>h2,.application-setup .pharos-onboard>h2{margin:0 0 16px;padding:0;font:400 40px/1.12 var(--serif,serif);letter-spacing:-.02em}
.pharos-onboard-intro,.application-setup .pharos-onboard-intro{margin:0 0 18px;padding:0;color:var(--muted,#666);font-size:16px}
.pharos-onboard-body{overflow:auto;flex:1;min-height:0;padding:4px;scrollbar-gutter:stable}
.pharos-onboard-body h3{font:600 19px/1.3 var(--serif);margin:16px 0 0}
.pharos-onboard .pharos-muted{color:var(--muted,#666);font-weight:400}
.pharos-onboard [hidden]{display:none!important}
.pharos-onboard :focus-visible{outline:2px solid var(--focus-ring, var(--accent));outline-offset:3px}
.pharos-onboard>h2:focus{outline:none}
.pharos-tool-detected{color:var(--gold);font-size:10px;letter-spacing:.04em}
.pharos-source-progress{margin-right:auto;font-size:11px;color:var(--muted);white-space:nowrap}
.pharos-source-review{display:grid;gap:6px;padding:14px 0;border-bottom:1px solid var(--line)}
.pharos-source-review code{overflow-wrap:anywhere;white-space:pre-wrap;font-size:12px}
.pharos-custom-source{border:1px solid var(--line);border-radius:7px;padding:14px;margin:18px 0}
.pharos-custom-source input,.pharos-custom-source select{width:100%;box-sizing:border-box;min-width:0;padding:8px 10px;background:var(--bg);border:1px solid var(--line);border-radius:5px;color:var(--ink);font:inherit;margin:10px 0}
.pharos-custom-source button{margin:10px 10px 0 0}
.pharos-onboard .pharos-source-rename{grid-column:2;padding:0;border:0;margin:6px 0;background:transparent;font-size:12px}
.pharos-source-rename summary{cursor:pointer;color:var(--muted)}
.pharos-onboard .pharos-other-tools{padding:16px 0 0;border:0;border-top:1px solid var(--line);margin:18px 0 0;background:transparent}
.pharos-other-tools>summary{cursor:pointer;font-weight:600}
.pharos-onboard .pharos-tool-contents{margin:18px 0 0;padding:14px 0 0;border:0;border-top:1px solid var(--line);border-radius:0;background:transparent}
.pharos-tool-contents>summary{cursor:pointer;font-weight:600}
.pharos-tool-contents p{margin:6px 0 12px}
.pharos-visually-hidden{position:absolute;width:1px;height:1px;overflow:hidden;clip-path:inset(50%);white-space:nowrap}
.pharos-modes{display:grid;gap:12px;border:0;margin:0;padding:0;min-width:0}
.pharos-mode-wrap{border:1px solid var(--line);border-radius:9px;background:var(--bg)}
.pharos-mode-wrap:has(input:checked){border-color:var(--gold);box-shadow:inset 0 0 0 1px var(--gold);background:color-mix(in srgb,var(--gold) 7%,var(--bg))}
.pharos-onboard .pharos-mode{display:flex;gap:14px;align-items:flex-start;padding:18px 20px;margin:0;font-weight:400;cursor:pointer}
.pharos-modes>.pharos-mode{border:1px solid var(--line);border-radius:9px;background:var(--bg)}
.pharos-modes>.pharos-mode:has(input:checked){border-color:var(--gold);box-shadow:inset 0 0 0 1px var(--gold)}
.pharos-onboard .pharos-mode input{margin:5px 0 0;width:16px;height:16px;min-height:0;padding:0;flex:none;accent-color:var(--accent)}
.pharos-mode>span{display:grid;gap:4px;min-width:0}
.pharos-mode strong{display:flex;flex-wrap:wrap;align-items:center;gap:8px;font:600 18px/1.3 var(--serif)}
.pharos-recommended{font:600 10px/1.4 var(--sans);letter-spacing:.04em;text-transform:uppercase;color:var(--gold)}
.pharos-onboard .pharos-places{margin:0 20px 16px 50px;padding:0;border:0;background:transparent;font-size:13px}
.pharos-places>summary{cursor:pointer;color:var(--muted);font-weight:600}
.pharos-places ul{margin:10px 0 0;padding:0;list-style:none;display:grid;gap:6px}
.pharos-places code{font:12px ui-monospace,SFMono-Regular,monospace;overflow-wrap:anywhere}
.pharos-places p{margin:10px 0 0}
.pharos-exports-link{margin:18px 0 0;font-size:13px}
.pharos-onboard button.pharos-link{min-height:0;padding:0;border:0;background:transparent;color:var(--gold);font:600 13px/1.4 var(--sans);text-decoration:underline;text-underline-offset:2px}
.pharos-path-row{display:flex;gap:10px;align-items:center;margin:8px 0}
.pharos-path-row input{flex:1;min-width:0;box-sizing:border-box;padding:8px 10px;background:var(--bg);border:1px solid var(--line);border-radius:5px;color:var(--ink);font:12px ui-monospace,SFMono-Regular,monospace}
.pharos-path-row button{flex:none}
.pharos-folders{display:grid;gap:4px}
.pharos-folder>p{margin:0 0 6px;font-size:12px}
.pharos-folder-label{display:block;margin:8px 0 0;font-size:13px;font-weight:600}
.pharos-onboard-body h3.pharos-folders-heading{margin-top:26px;padding-top:16px;border-top:1px solid var(--line)}
.pharos-not-found{margin-top:4px;font-size:13px}
.pharos-not-recorded{font-size:12px}
.pharos-export-how{margin:12px 0 4px}
.pharos-modes.pharos-pair{grid-template-columns:repeat(2,minmax(0,1fr))}
.pharos-chips{display:flex;flex-wrap:wrap;gap:8px;border:0;margin:0;padding:0;min-width:0}
.pharos-onboard .pharos-chip-option{display:inline-flex;align-items:center;gap:8px;margin:0;padding:8px 14px;border:1px solid var(--line);border-radius:20px;background:var(--bg);font:600 13px/1.3 var(--sans);cursor:pointer}
.pharos-onboard .pharos-chip-option:has(input:checked){border-color:var(--gold);box-shadow:inset 0 0 0 1px var(--gold);background:color-mix(in srgb,var(--gold) 7%,var(--bg))}
.pharos-onboard .pharos-chip-option input{margin:0;width:14px;height:14px;min-height:0;padding:0;accent-color:var(--accent)}
.pharos-chip-option .pharos-tool-detected{font-weight:400}
.pharos-onboard .pharos-other-tools>.pharos-chips{margin-top:12px}
.pharos-onboard .pharos-candidate-head label.pharos-candidate-path{font:600 14px/1.4 ui-monospace,SFMono-Regular,monospace;overflow-wrap:anywhere}
.pharos-table-scroll{overflow-x:auto;max-width:100%}
.pharos-capabilities{border-collapse:collapse;font-size:14px}
.pharos-capabilities th,.pharos-capabilities td{padding:7px 10px;border-bottom:1px solid var(--line);text-align:left;vertical-align:top}
.pharos-capabilities thead th{font-weight:600;white-space:nowrap}
.pharos-capabilities tbody th{font-weight:400;white-space:nowrap}
.pharos-capabilities td small{display:block;max-width:24ch;margin-top:2px;font-size:12px}
.pharos-capabilities .pharos-support-unsupported>span,.pharos-capabilities .pharos-support-not-applicable>span{color:var(--muted,#666)}
.pharos-custom-source summary{cursor:pointer;font-weight:600}
.pharos-onboard-body>p{margin:18px 0}
.pharos-candidate{display:grid;grid-template-columns:22px 1fr;gap:3px 10px;padding:14px 0;border-bottom:1px solid var(--line,#ccc)}
.pharos-candidate:last-child{border-bottom:0}
.pharos-candidate input[type=checkbox]{margin:4px 0 0;width:17px;min-height:0;height:17px;padding:0;accent-color:var(--accent,#315845)}
.pharos-candidate-head{display:flex;flex-wrap:wrap;align-items:baseline;gap:4px 10px}
.pharos-onboard .pharos-candidate-head label{margin:0;font:600 16px/1.4 var(--sans);cursor:pointer}
.pharos-candidate-head code{font:12px ui-monospace,SFMono-Regular,monospace;color:var(--muted,#666);overflow-wrap:anywhere}
.pharos-candidate>div:not(.pharos-candidate-head){grid-column:2;font-size:13px}
.pharos-candidate .pharos-muted{color:var(--muted,#666)}
.pharos-candidate .pharos-warn{color:var(--warn,#98601d)}
.pharos-candidate-name{display:flex;align-items:center;gap:8px;color:var(--muted,#666)}
.pharos-candidate-name input{width:190px;padding:3px 8px;border:1px solid var(--line,#ccc);border-radius:7px;background:var(--bg,#fff);color:var(--ink,#222);font:12px ui-monospace,SFMono-Regular,monospace}
.pharos-chip{border:1px solid var(--line,#ccc);border-radius:20px;padding:1px 8px;font-size:12px;color:var(--muted,#666)}
.pharos-onboard-actions{display:flex;justify-content:flex-end;align-items:center;gap:10px;padding:20px 0 0;margin-top:20px;border-top:1px solid var(--line,#ccc);flex:none}
.pharos-error{color:var(--bad,#9c3d36)}.pharos-onboard-actions .pharos-error{margin-right:auto;font-size:13px}
.pharos-onboard button{cursor:pointer;min-height:40px;padding:7px 14px;border:1px solid var(--line,#ccc);border-radius:5px;font:600 12px/1.4 var(--sans);background:var(--panel,#fff);color:var(--ink,#222)}
.pharos-onboard-actions button.primary{background:var(--accent,#315845);border-color:var(--accent,#315845);color:var(--panel,#fff)}
.pharos-onboard-actions button:disabled{opacity:.55;cursor:default}
.pharos-steps{margin:14px 0 18px;border-top:1px solid var(--line,#ccc)}
.pharos-step{display:grid;grid-template-columns:minmax(110px,1fr) 3fr;gap:4px 14px;padding:10px 0;border-bottom:1px solid var(--line,#ccc);font-size:13px}
.pharos-step strong{font-size:14px}
.pharos-step .pharos-meter{grid-column:2;height:6px;overflow:hidden;border-radius:6px;background:var(--line,#ccc)}
.pharos-step .pharos-meter>span{display:block;height:100%;min-width:6px;background:var(--accent,#315845);transition:width .25s}
.pharos-onboard-body .pharos-lead{font-size:16px;margin:18px 0 6px}
.pharos-working{display:grid;gap:10px;margin:18px 0}.pharos-working>p{margin:0}
.pharos-working .pharos-meter{height:6px;overflow:hidden;border-radius:6px;background:var(--line,#ccc)}
.pharos-working .pharos-meter>span{display:block;width:35%;height:100%;background:var(--accent,#315845);animation:pharos-onboard-slide 1.6s ease-in-out infinite}
@keyframes pharos-onboard-slide{from{transform:translateX(-100%)}to{transform:translateX(286%)}}
@media(max-width:600px){.pharos-onboard{padding:24px;width:calc(100% - 24px);max-height:calc(100dvh - 24px)}.pharos-onboard>h2,.application-setup .pharos-onboard>h2{font-size:27px}.pharos-onboard-intro,.application-setup .pharos-onboard-intro{font-size:14px}.pharos-modes.pharos-pair{grid-template-columns:1fr}.pharos-onboard-actions{gap:8px}.pharos-onboard .pharos-onboard-actions button{min-width:0;padding:7px 10px}.pharos-candidate-name{flex-wrap:wrap}.pharos-candidate-name input{max-width:100%;box-sizing:border-box}.pharos-onboard .pharos-places{margin:0 14px 14px}.pharos-path-row{flex-wrap:wrap}.pharos-path-row input{flex-basis:100%}}
@media(prefers-reduced-motion:reduce){.pharos-working .pharos-meter>span{animation:none;width:100%;opacity:.5}}`;

  const node = (tag, cls, text) => {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined) n.textContent = text;
    return n;
  };
  let libraryScope = null;
  const call = async (path, options = {}) => {
    const headers = {'Content-Type': 'application/json'};
    if (libraryScope && !path.startsWith('/api/app/')) headers['X-Pharos-Library'] = libraryScope;
    const response = await fetch(path, {...options, headers});
    const body = await response.json().catch(() => ({}));
    if (!response.ok) throw Object.assign(new Error(body.error || response.statusText), {status: response.status});
    return body;
  };
  const toast = message => { if (typeof window.feedbackToast === 'function') window.feedbackToast(message); };
  const plural = (count, unit) => `${Number(count).toLocaleString()} ${unit}${count === 1 ? '' : 's'}`;
  const size = value => {
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let amount = Number(value) || 0, power = 0;
    while (amount >= 1000 && power < units.length - 1) { amount /= 1000; power++; }
    return `${amount.toFixed(amount < 10 && power ? 1 : 0)} ${units[power]}`;
  };
  // An indeterminate bar with a line of text, shown while a request is out.
  const working = (text, detail) => {
    const box = node('div', 'pharos-working'), bar = node('div', 'pharos-meter');
    box.setAttribute('role', 'status');
    bar.append(node('span'));
    box.append(node('p', '', text), bar);
    if (detail) box.append(node('p', 'pharos-muted', detail));
    return box;
  };
  const day = value => new Date(value).toLocaleDateString(undefined, {year: 'numeric', month: 'short', day: 'numeric'});
  const hostLabel = status => status.host?.label || 'this Mac';

  function facts(candidate) {
    const parts = [];
    if (candidate.unit && candidate.status !== 'protected') parts.push((candidate.truncated ? 'at least ' : '') + plural(candidate.count, candidate.unit));
    if (candidate.bytes) parts.push(size(candidate.bytes));
    if (candidate.oldest && candidate.newest) parts.push(`${day(candidate.oldest)} – ${day(candidate.newest)}`);
    else if (candidate.newest) parts.push(`updated ${day(candidate.newest)}`);
    return parts.join(' · ');
  }

  function candidateRow(candidate, index, choices) {
    const row = node('div', 'pharos-candidate'), head = node('div', 'pharos-candidate-head');
    const id = `pharosCandidate${index}`, configured = candidate.configured;
    // Rows sit under their tool's heading, so a row is named by its folder.
    const title = node('label', candidate.display_path ? 'pharos-candidate-path' : '', candidate.display_path || toolName(candidate.kind));
    title.htmlFor = id;
    head.append(title);
    if (configured) head.append(node('span', 'pharos-chip', `Added as ${configured.name}${configured.enabled ? '' : ' · paused'}`));
    else if (candidate.status !== 'found' && candidate.status !== 'manual') head.append(node('span', 'pharos-chip', candidate.status));
    if (candidate.acceptable) {
      const box = node('input');
      box.type = 'checkbox';
      box.id = id;
      box.setAttribute('aria-label', `${toolName(candidate.kind)}: ${candidate.display_path || candidate.path}`);
      box.checked = configured ? configured.enabled : candidate.recommended;
      // A paused source in this Mac's file can be switched on from here.
      box.disabled = Boolean(configured) && (configured.enabled || configured.scope !== 'host');
      row.append(box, head);
      const choice = {candidate, box};
      choices.push(choice);
      if (!configured) {
        const name = node('div', 'pharos-candidate-name'), input = node('input');
        input.value = candidate.name;
        input.setAttribute('aria-label', `Source name for ${candidate.display_path}`);
        input.spellcheck = false;
        choice.input = input;
        name.append(node('span', '', 'Source name'), input);
        choice.nameRow = name;
      }
    } else {
      row.append(node('span'), head);
    }
    const summary = facts(candidate);
    if (summary) row.append(node('div', 'pharos-muted', summary));
    if (candidate.retention) row.append(node('div', candidate.retention_days < 90 ? 'pharos-warn' : 'pharos-muted', candidate.retention));
    if (candidate.overlap?.summary) row.append(node('div', 'pharos-muted', candidate.overlap.summary));
    if (candidate.detail) row.append(node('div', 'pharos-muted', candidate.detail));
    if (candidate.found_by && candidate.found_by !== 'default location') row.append(node('div', 'pharos-muted', `Found by ${candidate.found_by}`));
    const choice = choices.find(item => item.candidate === candidate);
    if (choice?.nameRow) {
      const rename=node('details','pharos-source-rename');
      rename.append(node('summary','','Rename this source'),choice.nameRow);row.append(rename);
    }
    return row;
  }

  let active = null;
  function close() {
    const embedded=active?.embedded, opener=active?.opener;
    document.getElementById('pharosOnboarding')?.remove();
    document.getElementById('application-setup')?.classList.remove('source-setup-open');
    active=null;
    if(embedded)location.href='/library';else opener?.focus();
  }

  // The app's folder picker, where there is one: the native panel, or the
  // app manager's (a browser window of the app). purpose is source or export.
  const pickerAvailable = () => Boolean(document.getElementById('application-setup') || window.pharosManagedSession || window.webkit?.messageHandlers?.pharosLibrary);
  async function choosePath(purpose) {
    const native = window.webkit?.messageHandlers?.pharosLibrary;
    return native ? await native.postMessage({action: 'chooseFolder', purpose}) : (await call('/api/app/choose-folder', {method: 'POST', body: JSON.stringify({purpose})})).path;
  }

  // A tool as a chip: a checkbox and its name, marked when spotted here.
  function toolChip(tool, checked, spotted, onchange) {
    const label=node('label','pharos-chip-option'),box=node('input');box.type='checkbox';box.value=tool.kind;box.checked=checked;
    label.title=tool.summary;
    label.append(box,node('span','',tool.name));
    if(spotted)label.append(node('span','pharos-tool-detected','on this Mac'));
    box.onchange=()=>onchange(box.checked);
    return label;
  }

  // A path field with an optional Choose… button.
  function pathField(labelText, value, placeholder, purpose, onchange, fail) {
    const row=node('div','pharos-path-row'),input=node('input');
    input.value=value;input.placeholder=placeholder;input.spellcheck=false;input.setAttribute('aria-label',labelText);
    input.oninput=()=>onchange(input.value);
    row.append(input);
    if(pickerAvailable()){
      const pick=button(purpose==='export'?'Choose file…':'Choose folder…',false,async()=>{pick.disabled=true;try{const chosen=await choosePath(purpose);if(chosen){input.value=chosen;onchange(chosen)}}catch(failure){fail(failure)}finally{pick.disabled=false;pick.focus()}});
      pick.setAttribute('aria-label',`${pick.textContent} ${labelText}`);
      row.append(pick);
    }
    return {row,input};
  }

  // mode: start (choose how to find), scan, manual, or import; kind, a tool
  // to begin with when adding a folder.
  function openPanel(status, {mode='start', kind=''}={}) {
    if(active)return;
    const embedded=document.getElementById('application-setup');
    const dialog=node(embedded?'section':'dialog','pharos-onboard'+(embedded?' pharos-embedded':''));
    dialog.id='pharosOnboarding';dialog.setAttribute('aria-labelledby','pharosOnboardTitle');
    const title=node('h2');title.id='pharosOnboardTitle';title.tabIndex=-1;
    const intro=node('p','pharos-onboard-intro');
    const body=node('div','pharos-onboard-body'),actions=node('div','pharos-onboard-actions');
    let wizard=createPharosSourceWizard();
    let choice='scan';
    // Kinds whose usual folder has been filled in once, so clearing it sticks.
    const seeded=new Set();
    active={embedded:!!embedded,opener:document.activeElement,stage:0,back:()=>renderStart()};
    dialog.append(title,intro,body,actions);
    if(embedded){embedded.classList.add('source-setup-open');embedded.setAttribute('aria-labelledby',title.id);embedded.querySelector('.application-body').append(dialog)}
    else{document.body.append(dialog);dialog.showModal();dialog.addEventListener('cancel',event=>{event.preventDefault();active?.back()})}
    const heading=(text,description)=>{title.textContent=text;intro.textContent=description;intro.hidden=false;body.scrollTop=0;title.focus()};
    const step=n=>node('span','pharos-source-progress',`Step ${n} of 3`);
    const error=node('p','pharos-error');error.setAttribute('role','status');
    const fail=failure=>{error.textContent=failure.message;body.prepend(error)};
    const clear=()=>{body.replaceChildren();error.textContent=''};
    // Leaving setup before any source is chosen still writes this Mac's file,
    // so setup does not reopen on every page.
    const skip=async()=>{
      active.stage=-1;
      active.back=()=>{};
      const buttons=[...actions.querySelectorAll('button')];buttons.forEach(b=>b.disabled=true);
      try{await call('/api/probe/accept',{method:'POST',body:JSON.stringify({kinds:[],accept:[],decline:[]})});sessionStorage.setItem(DISMISSED+status.host?.id,'1');close()}
      catch(failure){active.stage=0;active.back=skip;fail(failure);buttons.forEach(b=>b.disabled=false)}
    };
    const leave=()=>status.host_file_exists?close():skip();
    // From Settings there is nothing to defer: leaving just closes.
    const leaveLabel=status.host_file_exists?'Cancel':'Set up later';
    const exportsLink=back=>{
      const line=node('p','pharos-exports-link'),open=node('button','pharos-link','Import an export');
      open.type='button';open.onclick=()=>renderImport(back);
      line.append(node('span','pharos-muted','Have a ChatGPT or Claude.ai export? '),open);
      return line;
    };

    // How to find: the usual places, or only the folders the person names.
    function renderStart(){
      active.stage=0;active.back=leave;
      heading('Find your AI conversations.','Pharos copies the history your AI tools keep on this Mac into your library, so you can search and study it. Choose how it finds that history.');
      clear();
      const modes=node('fieldset','pharos-modes');modes.append(node('legend','pharos-visually-hidden','How Pharos finds your conversations'));
      const option=(value,name,text,extra)=>{
        const label=node('label','pharos-mode'),radio=node('input'),copy=node('span');
        radio.type='radio';radio.name='pharos-find';radio.value=value;radio.checked=choice===value;radio.onchange=()=>{choice=value};
        const head=node('strong','',name);
        if(value==='scan')head.append(node('span','pharos-recommended','Recommended'));
        copy.append(head,node('span','pharos-muted',text));
        label.append(radio,copy);
        const wrap=node('div','pharos-mode-wrap');wrap.append(label);if(extra)wrap.append(extra);
        modes.append(wrap);
      };
      const places=node('details','pharos-places');places.append(node('summary','','Which places?'));
      const list=node('ul');
      for(const tool of wizard.catalog){
        const item=node('li');item.append(node('strong','',tool.name+' '),node('code','',tool.locations.join(', ')));
        if(tool.variables.length)item.append(node('span','pharos-muted',` or where ${tool.variables.join(' or ')} points`));
        list.append(item);
      }
      places.append(list,node('p','pharos-muted','These are folders the tools themselves create. Pharos looks only there, not across your disk.'));
      const found=wizard.found();
      const foundNote=found.length?`Already spotted here: ${found.map(tool=>tool.name).join(', ')}.`:'';
      option('scan','Look in the usual places',`Pharos checks where ${wizard.catalog.length} tools keep their history and counts what it finds. Nothing is copied until you approve it.${foundNote?' '+foundNote:''}`,places);
      option('manual','I’ll tell Pharos where to look','Choose your tools, then the folders that hold their history. Pharos checks only those folders.');
      body.append(modes,exportsLink(renderStart));
      actions.replaceChildren(step(1),button(leaveLabel,false,leave),button('Continue',true,()=>choice==='scan'?scan():renderManual()));
    }

    async function scan(){
      wizard.scan();
      if(!wizard.tools.length)return renderNothing();
      await discover('Looking in the usual places…','Checking where your tools keep their history. Nothing is added or copied yet.',renderFound,renderStart);
    }
    async function discover(text,description,next,back){
      heading(text,description);
      body.replaceChildren(working('Counting conversations…'));actions.replaceChildren();active.back=()=>{};
      try{wizard.discovered(await call('/api/probe',{method:'POST',body:JSON.stringify(wizard.request())}));next()}
      catch(failure){back();fail(failure)}
    }

    function renderNothing(){
      active.stage=1;active.back=renderStart;
      heading('Nothing in the usual places.','Pharos found none of the tools it knows in their usual folders. If you keep their history somewhere else, point Pharos at it.');
      clear();
      body.append(exportsLink(renderNothing));
      actions.replaceChildren(step(2),button('Back',false,renderStart),button('Choose tools and folders',true,()=>renderManual()));
    }

    // Candidates for each chosen tool, with what was not found.
    function candidateGroups(){
      const choices=[];
      for(const checked of wizard.report.checked||[])if(checked.found_by==='chosen folder'&&checked.status==='missing')body.append(node('p','pharos-error',`No history found at ${checked.path}. Check the folder or choose another location.`));
      for(const tool of wizard.chosen()){
        const candidates=wizard.report.candidates.filter(candidate=>tool.kinds.includes(candidate.kind));
        const group=node('section','pharos-source-group');group.append(node('h3','',tool.name));
        if(!candidates.length)group.append(node('p','pharos-muted',wizard.mode==='manual'?'No history found in the folders you named.':'Installed, but no saved history in its usual folders yet.'));
        for(const candidate of candidates){
          const row=candidateRow(candidate,choices.length,choices),choice=choices.find(c=>c.candidate===candidate);
          if(choice){choice.box.checked=choice.box.disabled?!!candidate.configured?.enabled:wizard.checked(candidate);choice.box.onchange=()=>wizard.set(candidate,choice.box.checked);
            if(choice.input){choice.input.value=wizard.names[candidate.name]||candidate.name;choice.input.oninput=()=>{wizard.names[candidate.name]=choice.input.value.trim()}}
          }
          group.append(row);
        }
        body.append(group);
      }
    }

    // A folder the scan did not find, for any tool.
    function otherFolder(){
      const custom=node('details','pharos-custom-source');custom.append(node('summary','','Add a folder Pharos didn’t find'));
      const kind=node('select');kind.setAttribute('aria-label','Tool for this folder');
      for(const tool of wizard.catalog)for(const each of tool.kinds){const option=node('option','',toolName(each));option.value=each;kind.append(option)}
      const hint=node('p','pharos-muted');
      let path='';
      const field=pathField('Folder',path,'/path/to/folder','source',value=>{path=value},fail);
      kind.onchange=()=>{hint.textContent=HINTS[kind.value]||''};kind.onchange();
      custom.append(kind,hint,field.row,button('Check folder',false,()=>{
        const value=path.trim();
        if(!value.startsWith('/')&&!value.startsWith('~/')){fail(Error('Enter an absolute folder path, or one starting with ~/.'));field.input.focus();return}
        if(!wizard.tools.includes(kind.value))wizard.choose(kind.value,true);
        if(!wizard.locations.some(l=>l.kind===kind.value&&l.path===value))wizard.locations.push({kind:kind.value,path:value});
        discover('Checking that folder…','Nothing is added or copied yet.',renderFound,renderFound);
      }));
      return custom;
    }

    function renderFound(){
      active.stage=1;active.back=wizard.mode==='manual'?renderManual:renderStart;
      const manual=wizard.mode==='manual';
      heading(manual?'Choose the folders to include.':'Found on this Mac.',manual?'These are the folders you named. Choose which to include.':'Choose what to include. Folders you leave out are kept as paused sources, so you can add them later in Settings → Sources.');
      clear();
      candidateGroups();
      if(!manual){
        body.append(otherFolder());
        const missing=wizard.toolGroups().offered.map(toolName);
        if(missing.length)body.append(node('p','pharos-muted pharos-not-found',`Not found here: ${missing.join(', ')}.`));
      }
      actions.replaceChildren(step(2),button('Back',false,active.back),button('Continue',true,review));
    }

    // Which tools, then a folder for each: only those folders are checked.
    function renderManual(){
      if(wizard.mode!=='manual'){wizard.manual();if(kind)wizard.tools=[];if(kind)wizard.choose(kind,true)}
      active.stage=1;active.back=renderStart;
      heading('Which tools do you use?','Pick your tools, then the folder that holds each one’s history. Pharos checks only the folders you name here.');
      clear();
      const groups=wizard.toolGroups(),found=new Set(groups.found);
      const chips=node('fieldset','pharos-chips');chips.setAttribute('aria-label','Your tools');
      const cards=kinds=>kinds.map(kind=>toolChip(wizard.tool(kind),wizard.tools.includes(kind),found.has(kind),checked=>{const top=body.scrollTop;wizard.choose(kind,checked);renderManual();body.scrollTop=top;body.querySelector(`.pharos-chips input[value="${kind}"]`)?.focus()}));
      chips.append(...cards([...groups.found,...groups.offered]));
      body.append(chips);
      if(groups.more.length){
        const more=node('details','pharos-other-tools');more.append(node('summary','','More tools'));
        more.open=groups.more.some(kind=>wizard.tools.includes(kind));
        const fieldset=node('fieldset','pharos-chips');fieldset.setAttribute('aria-label','More tools');fieldset.append(...cards(groups.more));
        more.append(fieldset);body.append(more);
      }
      const folders=node('section','pharos-folders');
      for(const tool of wizard.chosen()){
        const group=node('div','pharos-folder');group.append(node('h3','',tool.name));
        tool.kinds.forEach((each,index)=>{
          const named=wizard.locations.find(location=>location.kind===each);
          // The tool's own folder starts filled in; a second face (Cursor CLI)
          // starts empty, since many people never use it.
          if(!named&&index===0&&!seeded.has(each)&&tool.locations[0])wizard.name(each,tool.locations[0]);
          seeded.add(each);
          const usual=PROVIDERS[each]?.locations?.[0]||'';
          const field=pathField(`${toolName(each)} folder`,wizard.locations.find(location=>location.kind===each)?.path||'',usual?`Usually ${usual}`:'/path/to/folder','source',value=>wizard.name(each,value),fail);
          if(tool.kinds.length>1)group.append(node('label','pharos-folder-label',toolName(each)));
          group.append(field.row,node('p','pharos-muted',HINTS[each]||''));
        });
        folders.append(group);
      }
      if(wizard.chosen().length)body.append(node('h3','pharos-folders-heading','Folders to check'),folders);
      body.append(exportsLink(renderManual));
      const next=button('Check these folders',true,()=>{
        const named=wizard.request();
        const bad=named.locations.find(location=>!location.path.startsWith('/')&&!location.path.startsWith('~/'));
        if(bad){fail(Error(`Enter an absolute folder for ${toolName(bad.kind)}, or one starting with ~/.`));return}
        discover('Checking the folders you named…','Only these folders. Nothing is added or copied yet.',renderFound,renderManual);
      });
      next.disabled=!wizard.request().locations.length;
      actions.replaceChildren(step(2),button('Back',false,renderStart),next);
    }

    function review(){
      active.stage=2;active.back=renderFound;
      const selected=wizard.selected();
      heading(selected.length?'Pharos will read these folders.':'Nothing new to add.',selected.length?'Pharos will add them as sources for this Mac and copy their conversation files and database history into your library. Indexing comes next.':'No new sources are selected. You can finish setup now and add sources later in Settings → Sources.');
      clear();
      selected.forEach(candidate=>{
        const row=node('div','pharos-source-review');
        row.append(node('strong','',toolName(candidate.kind)),node('code','',candidate.display_path||candidate.path),node('span','pharos-muted',facts(candidate)||'Size unavailable'));
        const missing=wizard.notRecorded(candidate.kind);
        if(missing.length)row.append(node('span','pharos-muted pharos-not-recorded',`${toolName(candidate.kind)} doesn’t record: ${missing.join(', ').toLowerCase()}.`));
        body.append(row);
      });
      if(selected.length){
        const bytes=selected.reduce((total,c)=>total+(c.bytes||0),0);
        body.append(node('p','pharos-muted',`${plural(selected.length,'source')} selected${bytes?' · '+size(bytes)+' of source files':''}. Counts and sizes are estimates; database snapshots and search indexes need additional room.`));
        const contents=node('details','pharos-tool-contents');contents.append(node('summary','','What each tool records'));
        const table=node('div');renderContents(table,pharosCapabilityRows(wizard.providers,[...new Set(selected.map(c=>c.kind))]));
        contents.append(table);body.append(contents);
      }
      body.append(node('p','pharos-muted','These choices apply only on this Mac. Opening the library on another Mac will ask you to set up its own sources.'));
      const add=button(selected.length?'Add and capture':'Finish setup',true,async()=>{
        const buttons=[...actions.querySelectorAll('button')];buttons.forEach(b=>b.disabled=true);error.textContent='';active.back=()=>{};
        try{const result=await call('/api/probe/accept',{method:'POST',body:JSON.stringify(wizard.selection())});renderResult(result,body,actions,status);window.loadSources?.();refreshNote()}
        catch(failure){active.back=renderFound;fail(failure);buttons.forEach(b=>b.disabled=false)}
      });
      actions.replaceChildren(step(3),button('Back',false,renderFound),add);
    }

    // A cloud history arrives as a downloaded export: name it, and Pharos
    // checks it holds conversations before adding it.
    function renderImport(back){
      active.stage=1;active.back=back;
      heading('Import an export.','ChatGPT and Claude.ai keep your history in the cloud. Download an export, then choose it here; Pharos copies it into your library.');
      clear();
      const exports=wizard.exports();
      let chosen=exports[0]?.kind,path='';
      const kinds=node('fieldset','pharos-modes pharos-pair');kinds.append(node('legend','pharos-visually-hidden','Which export'));
      const how=node('p','pharos-muted pharos-export-how');
      const describe=()=>{how.textContent=exports.find(item=>item.kind===chosen)?.how||''};
      for(const item of exports){
        const label=node('label','pharos-mode'),radio=node('input'),copy=node('span');
        radio.type='radio';radio.name='pharos-export';radio.value=item.kind;radio.checked=item.kind===chosen;radio.onchange=()=>{chosen=item.kind;describe()};
        copy.append(node('strong','',item.name));label.append(radio,copy);kinds.append(label);
      }
      describe();
      const field=pathField('Export',path,'~/Downloads/export.zip','export',value=>{path=value},fail);
      body.append(kinds,how,node('h3','','The export'),field.row,
        node('p','pharos-muted','The ZIP you downloaded, the folder it unzips to, or its conversations.json. If it is in Downloads, macOS may ask to let Pharos read that folder.'));
      const add=button('Import',true,async()=>{
        const value=path.trim();
        if(!value.startsWith('/')&&!value.startsWith('~/')){fail(Error('Enter an absolute path, or one starting with ~/.'));field.input.focus();return}
        const buttons=[...actions.querySelectorAll('button')];buttons.forEach(b=>b.disabled=true);error.textContent='';
        try{const result=await call('/api/sources/import',{method:'POST',body:JSON.stringify({kind:chosen,path:value})});window.loadSources?.();renderResult(result,body,actions,status)}
        catch(failure){fail(failure);buttons.forEach(b=>b.disabled=false)}
      });
      actions.replaceChildren(button('Back',false,back),add);
    }

    heading('Finding your tools…','Checking where common AI tools keep their history on this Mac.');
    body.replaceChildren(working('Looking for installed tools…'));
    actions.replaceChildren(button(leaveLabel,false,leave));active.back=leave;
    // The tools setup offers, and what each records, come from the registry;
    // presence detection only says which are on this Mac.
    Promise.all([call('/api/providers'),call('/api/probe/tools').catch(()=>null)]).then(([catalog,result])=>{
      if(!dialog.isConnected||active?.stage!==0)return;
      learn(catalog.providers);
      wizard=createPharosSourceWizard(catalog.providers);
      wizard.detect(result?.tools||[]);
      ({scan,manual:renderManual,import:()=>renderImport(renderStart)}[mode]||renderStart)();
      if(!result)fail(Error('Pharos could not check for installed tools. You can still name their folders yourself.'));
    }).catch(failure=>{
      if(!dialog.isConnected||active?.stage!==0)return;
      body.replaceChildren();fail(failure);
    });
  }

  const SUPPORT={supported:'Yes',varies:'Sometimes',unsupported:'No','not-applicable':'Not applicable'};
  // A table of what the chosen tools record (pharosCapabilityRows), one
  // column a tool, so their limits are known before any is added.
  function renderContents(section,rows) {
    section.replaceChildren();section.hidden=!rows.length;
    if(!rows.length)return;
    section.append(node('h3','','What Pharos can read from these tools'),node('p','pharos-muted','Each tool keeps a different record of its conversations, and Pharos can only show what it keeps. When a tool reports no cost, Pharos prices its tokens.'));
    const scroll=node('div','pharos-table-scroll'),table=node('table','pharos-capabilities'),head=node('tr'),tbody=node('tbody'),thead=node('thead');
    scroll.tabIndex=0;scroll.setAttribute('role','region');scroll.setAttribute('aria-label','What each chosen tool records');
    const corner=node('th','','Records');corner.scope='col';head.append(corner);
    for(const cell of rows[0].cells){const th=node('th','',cell.name);th.scope='col';head.append(th)}
    for(const row of rows){
      const tr=node('tr'),label=node('th','',row.label);label.scope='row';tr.append(label);
      for(const cell of row.cells){const td=node('td','pharos-support-'+cell.support);td.append(node('span','',SUPPORT[cell.support]||cell.support));if(cell.note)td.append(node('small','pharos-muted',cell.note));tr.append(td)}
      tbody.append(tr);
    }
    thead.append(head);table.append(thead,tbody);scroll.append(table);section.append(scroll);
  }

  function renderResult(result, body, actions, status) {
    const added = [...result.added, ...result.enabled];
    body.parentElement.querySelector('.pharos-onboard-intro')?.remove();
    body.replaceChildren();
    body.append(node('p', '', added.length ? `Added ${added.join(', ')} for this Mac.` : 'Saved. No sources were added.'));
    if (result.declined.length) body.append(node('p', 'pharos-muted', `Kept as paused sources: ${result.declined.join(', ')}. Turn them on in Settings → Sources whenever you like.`));
    result.skipped.forEach(note => body.append(node('p', 'pharos-muted', note)));
    const done = node('button', 'primary', 'Done');
    done.type = 'button';
    done.onclick = close;
    actions.replaceChildren(done);
    if (!added.length) { active.back=close; return done.focus(); }
    active.back=()=>{};
    // Preservation completes before offering indexing. Large databases can take
    // longer; experimental row acquisition remains an explicit source opt-in.
    captureSources(added, status, body, actions);
  }

  const setTitle = (body, text) => { const title = body.parentElement?.querySelector('#pharosOnboardTitle'); if (title) title.textContent = text; };
  const button = (text, primary, onclick) => {
    const b = node('button', primary ? 'primary' : '', text);
    b.type = 'button';
    b.onclick = onclick;
    return b;
  };
  const meter = fraction => {
    const bar = node('div', 'pharos-meter'), fill = node('span');
    fill.style.width = `${Math.round(Math.min(Math.max(fraction, 0), 1) * 100)}%`;
    bar.append(fill);
    return bar;
  };
  async function runFromSetup(kind, options, update) {
    let run=(await call('/api/'+kind,{method:'POST',body:JSON.stringify(options)})).run;
    if(!run?.id)throw Error('Pharos did not return a run identifier.');
    update?.(run);
    while(run.state==='running'){
      await new Promise(resolve=>setTimeout(resolve,500));
      const status=await call('/api/'+kind);
      const next=(status.runs||[]).find(item=>item.id===run.id);
      if(!next)throw Error('This run is no longer available. Check Capture & index for progress.');
      run=next;update?.(run);
    }
    return run;
  }
  const libraryRuns = () => window.pharosLibrary?.runs || {
    capture:(sources,update)=>runFromSetup('capture',{sources},update),
    index:(options,update)=>runFromSetup('index',options,update),
  };
  function optionalBackup() {
    const details=node('details','pharos-custom-source');
    const link=node('a','','Explore backup options');link.href='/settings/backups';
    details.append(node('summary','','Protect your history with an optional backup'),node('p','pharos-muted','You can add a backup whenever you’re ready. It helps protect your library if a drive is lost or damaged.'),link);
    return details;
  }
  async function driveOf() {
    try { return (await call('/api/library/status')).drive; } catch { return null; }
  }

  function captureRows(run) {
    const list = node('div', 'pharos-steps');
    for (const result of run?.results || []) {
      const row = node('div', 'pharos-step'), bytes = (result.bytes_copied || 0) + (result.snapshot_bytes || 0);
      const state = {pending: 'Waiting', running: 'Copying', complete: 'Captured', failed: 'Failed'}[result.state] || result.state;
      const facts = [state];
      if (result.files_copied || result.files_unchanged) facts.push(`${plural(result.files_copied || 0, 'file')} copied${result.files_unchanged ? `, ${plural(result.files_unchanged, 'file')} unchanged` : ''}`);
      if (result.snapshots_taken) facts.push(plural(result.snapshots_taken, 'database snapshot'));
      if (bytes) facts.push(size(bytes));
      row.append(node('strong', '', result.name), node('span', result.state === 'failed' ? 'pharos-error' : 'pharos-muted', facts.join(' · ')));
      if (result.state === 'running' && result.bytes_planned) row.append(meter(result.bytes_copied / result.bytes_planned));
      (result.errors || []).slice(0, 3).forEach(error => row.append(node('span', 'pharos-error', error.path ? `${error.path}: ${error.error}` : error.error)));
      list.append(row);
    }
    return list;
  }

  async function captureSources(sources, status, body, actions) {
    const label = hostLabel(status), runs = libraryRuns();
    if(active)active.back=()=>{};
    const shown = () => document.body.contains(body);
    setTitle(body, `Capturing from ${label}`);
    const lead = node('p', 'pharos-lead', `Preserving conversation files and database history from ${sources.join(', ')} in the library…`);
    const note = node('p', 'pharos-muted', 'This preserves source data before indexing it. Large databases can take a while. You can close this; capture carries on and the header shows its progress.');
    const rows = node('div');
    rows.append(working('Starting the capture…'));
    body.replaceChildren(lead, rows, note);
    actions.replaceChildren(button('Run in background', false, close));
    let run;
    try {
      if (!runs) throw new Error('Pharos could not load its library controls; reload the page.');
      run = await runs.capture(sources, update => { if (update && shown()) rows.replaceChildren(captureRows(update)); });
    } catch (failure) {
      if (!shown()) return toast(failure.message);
      setTitle(body, `Sources added for ${label}`);
      body.replaceChildren(node('p', 'pharos-error', failure.status === 409 ? 'A capture is already running on this Mac. Capture these sources from Settings → Background Processing once it finishes.' : `The capture did not start: ${failure.message}`));
      actions.replaceChildren(button('Close', true, close));
      return;
    }
    const drive = await driveOf();
    const bytes = run?.bytes_copied || 0;
    const incremental = (run?.results || []).some(item => item.files_unchanged || item.snapshots_unchanged);
    const headline = run?.state === 'complete'
      ? (incremental ? `Captured ${size(bytes)} of new and changed files from ${label}; the rest was already in the library.` : `Captured ${size(bytes)} from ${label}.`)
      : `The capture from ${label} finished with errors.`;
    if (!shown()) {
      toast(run?.state === 'complete' ? `${headline} Index it in Settings → Background Processing.` : headline);
      return;
    }
    setTitle(body, run?.state === 'complete' ? `Captured from ${label}` : `Capture from ${label} incomplete`);
    const next = drive?.ejectable
      ? `You can eject ${drive.name} now; indexing can finish now or later on any Mac.`
      : 'Indexing makes these conversations searchable; it can run now or later.';
    body.replaceChildren(node('p', 'pharos-lead', headline), captureRows(run), node('p', '', run?.state === 'complete' ? next : `What was captured is kept; capture again from Settings → Background Processing. ${next}`));
    const index = button('Index now', true, () => indexSources(sources, status, body, actions));
    active.back=close;
    actions.replaceChildren(button('Later', false, close), index);
    index.focus();
  }

  function indexRows(run) {
    const list = node('div', 'pharos-steps');
    for (const source of run?.sources || []) {
      const row = node('div', 'pharos-step'), done = (run.results || []).find(item => `${item.host}/${item.source}` === source || item.source === source.split('/').pop());
      const name = source.split('/').pop();
      let text = 'Waiting', cls = 'pharos-muted';
      if (done) {
        text = done.error ? done.error : `${plural(done.conversations, 'conversation')} and ${plural(done.messages, 'message')} written${done.unchanged ? `, ${plural(done.unchanged, 'part')} unchanged` : ''}`;
        cls = done.error ? 'pharos-error' : 'pharos-muted';
      } else if (run.current_source === source) {
        text = `Indexing · ${run.phase} · ${plural(run.conversations, 'conversation')} so far`;
      }
      row.append(node('strong', '', name), node('span', cls, text));
      list.append(row);
    }
    return list;
  }

  async function indexSources(sources, status, body, actions) {
    const label = hostLabel(status), runs = libraryRuns();
    if(active)active.back=()=>{};
    const shown = () => document.body.contains(body);
    setTitle(body, `Indexing ${label}`);
    const rows = node('div');
    rows.append(working('Starting the index…'));
    body.replaceChildren(node('p', 'pharos-lead', `Indexing what was captured from ${label}…`), rows,
      node('p', 'pharos-muted', 'Indexing parses the captured conversations into the library, so it can take a while the first time. It runs in the background: close this whenever you like. Ejecting the drive stops it safely, and the next index picks up where it stopped.'));
    actions.replaceChildren(button('Run in background', false, close));
    let run;
    try {
      run = await runs.index({sources, only_needed: true}, update => { if (update && shown()) rows.replaceChildren(indexRows(update)); });
    } catch (failure) {
      if (!shown()) return toast(failure.message);
      body.replaceChildren(node('p', 'pharos-error', failure.status === 409 ? 'Source indexing is already running. Index these captures from Settings → Background Processing once it finishes.' : `Indexing did not start: ${failure.message}`));
      actions.replaceChildren(button('Close', true, close));
      return;
    }
    window.loadSources?.();
    const alreadyCurrent = (run?.results || []).filter(result => result.skipped_unchanged).length;
    const parsedParts = (run?.results || []).reduce((count, result) => count + (result.parsed || 0), 0);
    const skippedParts = (run?.results || []).reduce((count, result) => count + (result.unchanged || 0), 0);
    const summary = run?.state === 'complete'
      ? !run.conversations && !parsedParts && !skippedParts && (alreadyCurrent || !run.results?.length) ? `${label} is already up to date; no conversations rewritten.`
        : `Indexed ${plural(run.conversations, 'conversation')} (${plural(run.messages, 'message')}) from ${label}.${parsedParts ? ` ${plural(parsedParts, 'source part')} parsed.` : ''}${skippedParts ? ` ${skippedParts.toLocaleString()} unchanged files or sessions skipped.` : ''}`
      : run?.state === 'interrupted' ? `Indexing ${label} was interrupted; the next index resumes it.` : `Indexing ${label} finished with errors.`;
    if (!shown()) return toast(summary);
    setTitle(body, run?.state === 'complete' ? `${label} is in the library` : `Indexing ${label}`);
    body.replaceChildren(node('p', 'pharos-lead', summary), indexRows(run));
    if(run?.state==='complete')body.append(optionalBackup());
    if (run?.error && run.state !== 'complete') body.append(node('p', 'pharos-error', run.error));
    active.back=close;
    const done = button('Done', true, close);
    actions.replaceChildren(done);
    done.focus();
  }

  const NEW_DISMISSED = 'pharos-new-sources-dismissed:';
  // Settings → Sources: look again, name a folder, or import an export; and
  // say when a tool has history no source reads yet.
  async function refreshNote() {
    const slot = document.getElementById('sourceSetupAction'), notice = document.getElementById('sourceNewNotice');
    let status;
    try { status = await call('/api/probe/status?discovery=none'); } catch { return null; }
    if (!slot) return status;
    if (!status.library) { slot.replaceChildren(); notice?.replaceChildren(); return status; }
    const action = (id, text, title, options) => {
      const b = button(text, false, () => openPanel(status, options));
      b.id = id;
      b.title = title;
      return b;
    };
    slot.replaceChildren(
      action('pharosFindSources', 'Look for sources…', `Look in the usual places on ${hostLabel(status)}. Sources are saved in ${status.host_file_display}.`, {mode: 'scan'}),
      action('pharosAddSource', 'Add a folder…', 'Name a folder that holds a tool’s history. Pharos checks only that folder.', {mode: 'manual'}),
      action('pharosImportSource', 'Import an export…', 'Add a ChatGPT or Claude.ai export you downloaded.', {mode: 'import'}));
    if (notice) {
      const kinds = status.new_source_kinds || [], key = NEW_DISMISSED + status.host?.id + ':' + kinds.join(',');
      notice.replaceChildren();
      if (kinds.length && !sessionStorage.getItem(key)) {
        if (!Object.keys(NAMES).length) { try { learn((await call('/api/providers')).providers); } catch {} }
        const names = kinds.map(toolName), box = node('div', 'source-new-notice');
        box.setAttribute('role', 'status');
        box.append(node('span', '', `Pharos found conversations from ${names.length > 1 ? names.slice(0, -1).join(', ') + ' and ' + names.at(-1) : names[0]} that no source reads yet.`),
          button('Review', true, () => openPanel(status, {mode: 'scan'})),
          button('Not now', false, () => { sessionStorage.setItem(key, '1'); notice.replaceChildren(); }));
        notice.append(box);
      }
    }
    return status;
  }

  const sheet = node('style');
  sheet.textContent = CSS;
  document.head.append(sheet);
  window.pharosOnboarding={
    // mode: start (the default), scan, manual or import; kind, a tool to
    // begin with when naming a folder.
    open:async({library,mode,kind}={})=>{libraryScope=library||null;const status=await call('/api/probe/status?discovery=none');openPanel(status,{mode,kind})},
    back:()=>active?.back(),
  };
  if(document.body.hasAttribute('data-application-setup'))return;
  refreshNote().then(status => {
    if (!status || sessionStorage.getItem(DISMISSED + status.host?.id)) return;
    if (status.needs_onboarding) openPanel(status);

  });
})();
