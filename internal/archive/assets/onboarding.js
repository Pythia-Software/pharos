// Source choice and consent stay separate from discovery and DOM rendering.
function createPharosSourceWizard() {
  const supported=['claude','codex','conductor','tl1','antigravity'];
  const key=c=>c.kind+'\0'+c.path;
  return {
    tools:[], detected:[], detectionReady:false, locations:[], report:null, decisions:new Map(), names:{},
    detect(tools) {
      this.detected=supported.filter(kind=>tools.some(tool=>tool.kind===kind));
      if(!this.detectionReady)this.tools=[...this.detected];
      this.detectionReady=true;
    },
    toolGroups() {return {found:[...this.detected],other:supported.filter(kind=>!this.detected.includes(kind))}},
    choose(kind,checked) {
      if(!supported.includes(kind))throw Error('Unsupported tool');
      this.tools=supported.filter(tool=>tool===kind?checked:this.tools.includes(tool));
      this.locations=this.locations.filter(location=>this.tools.includes(location.kind));
      this.report=null;
    },
    discovered(report) {
      this.report={...report,candidates:report.candidates.filter(c=>this.tools.includes(c.kind))};
      for(const candidate of this.report.candidates)if(!this.decisions.has(key(candidate)))this.decisions.set(key(candidate),candidate.recommended===true);
    },
    set(candidate,checked) {this.decisions.set(key(candidate),checked)},
    checked(candidate) {return this.decisions.get(key(candidate))===true},
    selected() {return (this.report?.candidates||[]).filter(c=>c.acceptable&&this.checked(c)&&!(c.configured?.enabled||c.configured&&c.configured.scope!=='host'))},
    request() {return {kinds:[...this.tools],locations:this.locations.map(location=>({...location}))}},
    selection() {
      const selected=this.selected(), accept=selected.map(c=>c.name);
      const decline=(this.report?.candidates||[]).filter(c=>!c.configured&&c.acceptable&&c.status==='found'&&!accept.includes(c.name)).map(c=>c.name);
      return {...this.request(),accept,decline,names:{...this.names}};
    },
  };
}

// Source setup UI.
// Detect installed tools and familiar data folders before offering choices. The same
// guide runs inside application setup and from Settings → Sources. Approval
// saves this Mac's choices, captures only the accepted sources, then offers
// indexing. The CLI retains its separate full-discovery probe.
(() => {
  'use strict';
  const DISMISSED = 'pharos-onboarding-dismissed:';
  const KINDS = {claude: 'Claude Code', codex: 'Codex', conductor: 'Conductor', tl1: 'TL1', chatgpt: 'ChatGPT', antigravity: 'Antigravity'};
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
.pharos-onboard :focus-visible{outline:2px solid var(--accent);outline-offset:3px}
.pharos-onboard>h2:focus{outline:none}
.pharos-tool-options{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px;border:0;margin:0;padding:0;min-width:0}
.pharos-onboard .pharos-tool{display:flex;align-items:center;gap:16px;position:relative;padding:20px;margin:0;border:1px solid var(--line);border-radius:9px;background:var(--bg);font-weight:400;cursor:pointer}
.pharos-onboard .pharos-tool:has(input:checked){border-color:var(--gold);background:color-mix(in srgb,var(--gold) 7%,var(--bg));box-shadow:inset 0 0 0 1px var(--gold)}
.pharos-tool>svg{width:64px;flex:none;fill:var(--bg);stroke:var(--gold);stroke-width:1.5;stroke-linejoin:round;stroke-linecap:round}
.pharos-tool>span{display:grid;gap:6px;padding-right:10px;min-width:0}
.pharos-tool strong{font:600 19px/1.3 var(--serif)}
.pharos-tool .pharos-muted{font-size:12px;line-height:1.5}
.pharos-tool-detected{color:var(--gold);font-size:10px;letter-spacing:.04em}
.pharos-onboard .pharos-tool input[type=checkbox]{position:absolute;top:12px;right:12px;width:16px;min-height:0;height:16px;padding:0;margin:0;accent-color:var(--accent)}
.pharos-source-progress{margin-right:auto;font-size:11px;color:var(--muted);white-space:nowrap}
.pharos-source-review{display:grid;gap:6px;padding:14px 0;border-bottom:1px solid var(--line)}
.pharos-source-review code{overflow-wrap:anywhere;white-space:pre-wrap;font-size:12px}
.pharos-custom-source{border:1px solid var(--line);border-radius:7px;padding:14px;margin:18px 0}
.pharos-custom-source input,.pharos-custom-source select{width:100%;box-sizing:border-box;min-width:0;padding:8px 10px;background:var(--bg);border:1px solid var(--line);border-radius:5px;color:var(--ink);font:inherit;margin:10px 0}
.pharos-custom-source button{margin:10px 10px 0 0}
.pharos-onboard .pharos-source-rename{grid-column:2;padding:0;border:0;margin:6px 0;background:transparent;font-size:12px}
.pharos-source-rename summary{cursor:pointer;color:var(--muted)}
.pharos-onboard .pharos-other-sources{padding:0;border:0;margin:14px 0;background:transparent;font-size:12px;color:var(--muted)}
.pharos-onboard .pharos-other-tools{padding:16px 0 0;border:0;border-top:1px solid var(--line);margin:18px 0 0;background:transparent}
.pharos-other-tools>summary{cursor:pointer;font-weight:600}
.pharos-other-tools>.pharos-tool-options{margin-top:16px}
.pharos-other-sources summary,.pharos-custom-source summary{cursor:pointer;font-weight:600}
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
@keyframes pharos-onboard-slide{from{margin-left:-35%}to{margin-left:100%}}
@media(max-width:600px){.pharos-onboard{padding:24px;width:calc(100% - 24px);max-height:calc(100dvh - 24px)}.pharos-onboard>h2,.application-setup .pharos-onboard>h2{font-size:27px}.pharos-onboard-intro,.application-setup .pharos-onboard-intro{font-size:14px}.pharos-tool-options{grid-template-columns:1fr;gap:10px}.pharos-onboard .pharos-tool{padding:14px 12px;gap:12px}.pharos-tool>svg{width:48px}.pharos-tool strong{font-size:17px}.pharos-onboard-actions{gap:8px}.pharos-onboard .pharos-onboard-actions button{min-width:0;padding:7px 10px}.pharos-candidate-name{flex-wrap:wrap}.pharos-candidate-name input{max-width:100%;box-sizing:border-box}}
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
    const title = node('label', '', KINDS[candidate.kind] || candidate.kind);
    title.htmlFor = id;
    head.append(title);
    if (candidate.display_path) head.append(node('code', '', candidate.display_path));
    if (configured) head.append(node('span', 'pharos-chip', `Added as ${configured.name}${configured.enabled ? '' : ' · paused'}`));
    else if (candidate.status !== 'found' && candidate.status !== 'manual') head.append(node('span', 'pharos-chip', candidate.status));
    if (candidate.acceptable) {
      const box = node('input');
      box.type = 'checkbox';
      box.id = id;
      box.setAttribute('aria-label', `${KINDS[candidate.kind] || candidate.kind}: ${candidate.display_path || candidate.path}`);
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

  function openPanel(status) {
    if(active)return;
    const embedded=document.getElementById('application-setup');
    const dialog=node(embedded?'section':'dialog','pharos-onboard'+(embedded?' pharos-embedded':''));
    dialog.id='pharosOnboarding';dialog.setAttribute('aria-labelledby','pharosOnboardTitle');
    const title=node('h2');title.id='pharosOnboardTitle';title.tabIndex=-1;
    const intro=node('p','pharos-onboard-intro');
    const body=node('div','pharos-onboard-body'),actions=node('div','pharos-onboard-actions');
    const wizard=createPharosSourceWizard();
    active={embedded:!!embedded,opener:document.activeElement,stage:0,back:()=>renderTools()};
    dialog.append(title,intro,body,actions);
    if(embedded){embedded.classList.add('source-setup-open');embedded.setAttribute('aria-labelledby',title.id);embedded.querySelector('.application-body').append(dialog)}
    else{document.body.append(dialog);dialog.showModal();dialog.addEventListener('cancel',event=>{event.preventDefault();active?.back()})}
    const heading=(text,description)=>{title.textContent=text;intro.textContent=description;body.scrollTop=0;title.focus()};
    const step=n=>node('span','pharos-source-progress',`Step ${n} of 3`);
    const error=node('p','pharos-error');error.setAttribute('role','status');
    const fail=failure=>{error.textContent=failure.message;body.prepend(error)};
    const skip=async()=>{
      active.stage=-1;
      active.back=()=>{};
      const buttons=[...actions.querySelectorAll('button')];buttons.forEach(b=>b.disabled=true);
      try{await call('/api/probe/accept',{method:'POST',body:JSON.stringify({kinds:[],accept:[],decline:[]})});sessionStorage.setItem(DISMISSED+status.host?.id,'1');close()}
      catch(failure){active.stage=0;active.back=skip;fail(failure);buttons.forEach(b=>b.disabled=false)}
    };
    function renderTools() {
      active.stage=0;active.back=skip;
      const groups=wizard.toolGroups();
      heading(groups.found.length?'Found on this Mac.':'Connect your tools.',groups.found.length?'We found these tools in common locations. Review what to include, then we’ll find their conversation folders.':'No tools were found in the common locations. Open Other tools to choose one and locate its history.');
      body.replaceChildren();error.textContent='';
      const descriptions={claude:'Conversations from the Claude Code app and CLI.',codex:'Conversations from the Codex app and CLI.',conductor:'Agent conversations and workspace context.',tl1:'Agent runs, tasks and project context.',antigravity:'Conversations from Antigravity / AGY.'};
      const note=node('p','pharos-muted');
      const update=()=>{next.disabled=!wizard.tools.length;note.textContent='Reviewing locations comes next. Nothing is captured until you approve the sources.'};
      function options(kinds,labelText,detected){
        const fieldset=node('fieldset','pharos-tool-options');fieldset.setAttribute('aria-label',labelText);
        for(const kind of kinds){
          const label=node('label','pharos-tool'),box=node('input');box.type='checkbox';box.value=kind;box.checked=wizard.tools.includes(kind);
          const art=document.createElementNS('http://www.w3.org/2000/svg','svg');art.setAttribute('viewBox','0 0 80 54');art.setAttribute('aria-hidden','true');
          art.innerHTML=kind==='conductor'||kind==='tl1'?'<rect x="5" y="6" width="48" height="32" rx="3"/><rect x="26" y="19" width="48" height="30" rx="3"/><path d="m34 29 6 5-6 5m14 0h13"/>':'<rect x="7" y="5" width="66" height="44" rx="4"/><path d="M7 16h66m-54 9 8 6-8 6m19 0h17"/>';
          const copy=node('span'),name=node('strong','',KINDS[kind]);copy.append(name,node('span','pharos-muted',descriptions[kind]));
          if(detected)copy.append(node('span','pharos-tool-detected','Detected on this Mac'));
          label.append(box,art,copy);fieldset.append(label);
          box.onchange=()=>{wizard.choose(kind,box.checked);update()};
        }
        return fieldset;
      }
      const next=button('Continue',true,discover);
      if(groups.found.length)body.append(options(groups.found,'Detected tools',true));
      body.append(note);
      const other=node('details','pharos-other-tools');other.append(node('summary','','Other tools'));
      other.open=groups.other.some(kind=>wizard.tools.includes(kind));
      if(groups.other.length)other.append(options(groups.other,'Other tools',false));
      other.append(node('p','pharos-muted','Tools in a custom location may not be detected. Select one here to find or choose its conversation folder.'));
      const exports=node('details','pharos-other-sources');exports.append(node('summary','','ChatGPT exports and other formats'),node('p','pharos-muted','ChatGPT history comes from a downloaded export, rather than a local agent folder. You can configure exports and other supported source formats later in Settings → Sources.'));
      other.append(exports);body.append(other);
      actions.replaceChildren(step(1),button('Set up later',false,skip),next);update();
    }
    async function discover(){
      heading('Looking for your conversations…','Checking only the tools you selected. This check won’t add sources or start a capture.');
      body.replaceChildren(working('Checking selected source locations…'));actions.replaceChildren();active.back=()=>{};
      try{wizard.discovered(await call('/api/probe',{method:'POST',body:JSON.stringify(wizard.request())}));renderLocations()}
      catch(failure){renderTools();fail(failure)}
    }
    function renderLocations(){
      active.stage=1;active.back=renderTools;
      heading('Choose the folders to include.','These locations belong to your selected tools. Review them before adding sources for this Mac.');
      body.replaceChildren();error.textContent='';
      const choices=[];
      for(const checked of wizard.report.checked||[])if(checked.found_by==='chosen folder'&&checked.status==='missing')body.append(node('p','pharos-error',`No history found at ${checked.path}. Check the folder or choose another location.`));
      for(const kind of wizard.tools){
        const candidates=wizard.report.candidates.filter(candidate=>candidate.kind===kind);
        const group=node('section','pharos-source-group');group.append(node('h3','',KINDS[kind]));
        if(!candidates.length)group.append(node('p','pharos-muted','No saved history found in the usual locations. Choose a different folder below, or go Back to change tools.'));
        for(const candidate of candidates){
          const row=candidateRow(candidate,choices.length,choices),choice=choices.find(c=>c.candidate===candidate);
          if(choice){choice.box.checked=choice.box.disabled?!!candidate.configured?.enabled:wizard.checked(candidate);choice.box.onchange=()=>wizard.set(candidate,choice.box.checked);
            if(choice.input){choice.input.value=wizard.names[candidate.name]||candidate.name;choice.input.oninput=()=>{wizard.names[candidate.name]=choice.input.value.trim()}}
          }
          group.append(row);
        }
        body.append(group);
      }
      const custom=node('details','pharos-custom-source');custom.append(node('summary','','Use a different folder'));
      const kind=node('select');kind.setAttribute('aria-label','Tool for this folder');
      wizard.tools.forEach(tool=>{const option=node('option','',KINDS[tool]);option.value=tool;kind.append(option)});
      const path=node('input');path.setAttribute('aria-label','Source folder');path.placeholder='/path/to/source';path.spellcheck=false;
      const hint=node('p','pharos-muted'),hints={claude:'Choose the Claude configuration folder containing projects, or the projects folder itself.',codex:'Choose the Codex home folder containing sessions.',conductor:'Choose the Conductor app data folder containing its databases.',tl1:'Choose the TL1 folder containing registry.json or workspaces.json.',antigravity:'Choose the Antigravity profile folder.'};
      kind.onchange=()=>hint.textContent=hints[kind.value];kind.onchange();
      custom.append(kind,hint,path);
      if(embedded||window.pharosManagedSession||window.webkit?.messageHandlers?.pharosLibrary){
        const pick=button('Choose folder…',false,async()=>{pick.disabled=true;try{const native=window.webkit?.messageHandlers?.pharosLibrary;const chosen=native?await native.postMessage({action:'chooseFolder'}):(await call('/api/app/choose-folder',{method:'POST',body:'{}'})).path;if(chosen)path.value=chosen}catch(failure){fail(failure)}finally{pick.disabled=false;pick.focus()}});custom.append(pick);
      }
      custom.append(button('Check folder',false,()=>{const value=path.value.trim();if(!value.startsWith('/')&&!value.startsWith('~/')){fail(Error('Enter an absolute folder path, or one starting with ~/.'));path.focus();return}if(!wizard.locations.some(l=>l.kind===kind.value&&l.path===value))wizard.locations.push({kind:kind.value,path:value});discover()}));
      body.append(custom);actions.replaceChildren(step(2),button('Back',false,renderTools),button('Continue',true,review));
    }
    function review(){
      active.stage=2;active.back=renderLocations;
      const selected=wizard.selected();
      heading('Review source access.',selected.length?'Pharos will add these sources for this Mac and copy their conversation files into your library. Indexing comes next.':'No new sources are selected. You can finish setup now and add sources later.');
      body.replaceChildren();error.textContent='';
      selected.forEach(candidate=>{const row=node('div','pharos-source-review');row.append(node('strong','',KINDS[candidate.kind]),node('code','',candidate.display_path||candidate.path),node('span','pharos-muted',facts(candidate)||'Size unavailable'));body.append(row)});
      if(selected.length){const bytes=selected.reduce((total,c)=>total+(c.bytes||0),0);body.append(node('p','pharos-muted',`${plural(selected.length,'source')} selected${bytes?' · '+size(bytes)+' of source files':''}. Counts and sizes are estimates; database snapshots and search indexes need additional room.`))}
      body.append(node('p','pharos-muted','These choices apply only on this Mac. Opening the library on another Mac will ask you to configure its own sources.'));
      const add=button(selected.length?'Add and capture':'Finish setup',true,async()=>{
        const buttons=[...actions.querySelectorAll('button')];buttons.forEach(b=>b.disabled=true);error.textContent='';active.back=()=>{};
        try{const result=await call('/api/probe/accept',{method:'POST',body:JSON.stringify(wizard.selection())});renderResult(result,body,actions,status);window.loadSources?.();refreshNote()}
        catch(failure){active.back=renderLocations;fail(failure);buttons.forEach(b=>b.disabled=false)}
      });
      actions.replaceChildren(step(3),button('Back',false,renderLocations),add);
    }
    heading('Finding your tools…','Checking common application and data-folder locations on this Mac.');
    body.replaceChildren(working('Looking for installed tools…'));
    actions.replaceChildren(step(1),button('Set up later',false,skip));active.back=skip;
    call('/api/probe/tools').then(result=>{
      if(!dialog.isConnected||active?.stage!==0)return;
      wizard.detect(result.tools||[]);renderTools();
    }).catch(failure=>{
      if(!dialog.isConnected||active?.stage!==0)return;
      wizard.detect([]);renderTools();fail(Error('Tool detection was unavailable. You can still select tools under Other tools.'));
    });
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
    // Copying the raw files is quick; once it is done the drive can go, and
    // indexing can follow now or later on any Mac.
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
    const lead = node('p', 'pharos-lead', `Copying the conversation files of ${sources.join(', ')} into the library…`);
    const note = node('p', 'pharos-muted', 'This copies raw files and takes seconds to minutes. Nothing is parsed yet. You can close this; the capture carries on and the header shows its progress.');
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
      body.replaceChildren(node('p', 'pharos-error', failure.status === 409 ? 'A capture is already running on this Mac. Capture these sources from Settings → Capture & index once it finishes.' : `The capture did not start: ${failure.message}`));
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
      toast(run?.state === 'complete' ? `${headline} Index it in Settings → Capture & index.` : headline);
      return;
    }
    setTitle(body, run?.state === 'complete' ? `Captured from ${label}` : `Capture from ${label} incomplete`);
    const next = drive?.ejectable
      ? `You can eject ${drive.name} now; indexing can finish now or later on any Mac.`
      : 'Indexing makes these conversations searchable; it can run now or later.';
    body.replaceChildren(node('p', 'pharos-lead', headline), captureRows(run), node('p', '', run?.state === 'complete' ? next : `What was captured is kept; capture again from Settings → Capture & index. ${next}`));
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
      body.replaceChildren(node('p', 'pharos-error', failure.status === 409 ? 'Source indexing is already running. Index these captures from Settings → Capture & index once it finishes.' : `Indexing did not start: ${failure.message}`));
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

  async function refreshNote() {
    const slot = document.getElementById('sourceSetupAction');
    let status;
    try { status = await call('/api/probe/status?discovery=none'); } catch { return null; }
    if (!slot) return status;
    if (!status.library) { slot.replaceChildren(); return status; }
    const find = node('button', '', 'Configure sources on this Mac…');
    find.type = 'button';
    find.id = 'pharosFindSources';
    find.title = `Look for conversation sources on ${hostLabel(status)}. Configuration is saved in ${status.host_file_display}.`;
    find.onclick = () => openPanel(status);
    slot.replaceChildren(find);
    return status;
  }

  const sheet = node('style');
  sheet.textContent = CSS;
  document.head.append(sheet);
  window.pharosOnboarding={
    open:async({library}={})=>{libraryScope=library||null;const status=await call('/api/probe/status?discovery=none');openPanel(status)},
    back:()=>active?.back(),
  };
  if(document.body.hasAttribute('data-application-setup'))return;
  refreshNote().then(status => {
    if (!status || sessionStorage.getItem(DISMISSED + status.host?.id)) return;
    if (status.needs_onboarding) openPanel(status);

  });
})();
