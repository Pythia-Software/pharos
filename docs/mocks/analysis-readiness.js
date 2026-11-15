'use strict';
// UI rehearsal: all status, progress, graph nodes and result annotations read
// the same DOM-free store. No production service or scheduler is called.
(() => {
  const { Store, definitions, age } = ReadinessPrototype;
  const $ = id => document.getElementById(id);
  const esc = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  const paths = {
    clock: '<circle cx="12" cy="12" r="8.5"/><path d="M12 7v5l3 2"/>',
    check: '<path d="m5 12 4 4L19 6"/>', arrow: '<path d="M5 12h14m-5-5 5 5-5 5"/>',
    refresh: '<path d="M19 8a8 8 0 1 0 0 8M19 3v5h-5"/>', info: '<circle cx="12" cy="12" r="8.5"/><path d="M12 11v5m0-9v.1"/>',
    spark: '<path d="m12 3 2.5 6.5L21 12l-6.5 2.5L12 21l-2.5-6.5L3 12l6.5-2.5Z"/>',
    error: '<path d="m12 3 10 18H2L12 3Z"/><path d="M12 9v5m0 3v.1"/>',
    graph: '<rect x="9" y="3" width="6" height="5" rx="1"/><rect x="2" y="16" width="6" height="5" rx="1"/><rect x="16" y="16" width="6" height="5" rx="1"/><path d="M12 8v4M5 16v-4h14v4"/>',
  };
  const icon = name => `<svg class="icon" viewBox="0 0 24 24" aria-hidden="true">${paths[name] ?? paths.clock}</svg>`;
  const s = { surface:'banner', scenario:'minutes', breakdown:'none', toolsOnly:false, display:'chart', tab:'overview', target:null, node:'writing', graphAll:false, source:false, error:'', receipts:new Map(), settings:false, settingsTask:null };
  const scenarios = [...$('scenario').options].map(option=>({value:option.value,label:option.textContent}));
  const initialScenario = new URLSearchParams(location.search).get('state');
  if(scenarios.some(option=>option.value===initialScenario))s.scenario=initialScenario;
  let store = new Store(s.scenario), invoker = null;
  const dialog = $('processing-dialog');
  const announce = message => { $('announcer').textContent = message; };
  const focusAction = action => (dialog.open?dialog:$('screen')).querySelector(`[data-action="${action}"]`)?.focus({preventScroll:true});
  const statusIcon = status => status.condition==='failed'?'error':status.kind === 'clear' ? 'check' : status.kind === 'unknown' ? 'info' : status.kind === 'missing' ? 'spark' : status.busy ? 'refresh' : status.tone === 'days' ? 'error' : 'clock';
  const time = value => value === null ? 'No results yet' : new Date(value).toLocaleTimeString('en-US', {hour:'numeric',minute:'2-digit',timeZone:'America/Denver'});
  const number = value => value.toLocaleString('en-US');

  function receipt(id, dataset, extra = []) {
    const key = id + ':' + dataset + ':' + extra.join(',');
    if (!s.receipts.has(key)) s.receipts.set(key, store.receipt(dataset, extra, s.scenario === 'rendered-old'));
    return s.receipts.get(key);
  }
  function registerVisible() {
    const result = (id, title, dataset, extra = []) => ({id,title,dataset,filter:s.breakdown,consumed:receipt(id,dataset,extra)});
    const results = s.surface === 'card' ? [
      ...s.toolsOnly ? [] : [result('words','Human Words','writing_messages'),result('labels','Message labels','people_message_labels')],
      result('tools','Tool calls','tool_calls'),
    ] : [result('analysis',s.breakdown === 'labels' ? 'Human Words by message label' : 'Human Words','writing_messages',s.breakdown === 'labels' ? ['people_message_labels'] : [])];
    store.register(results);
  }
  function indicator(id = null, compact = false) {
    const status = store.status(id), label = status.label;
    return `<button type="button" class="freshness-control tone-${status.tone} ${id === null ? 'nav-freshness' : 'element-freshness'} ${compact ? 'compact-freshness' : ''}" data-action="open" ${id ? `data-result="${id}"` : ''} aria-haspopup="dialog" aria-controls="processing-dialog" aria-label="${esc(id ? store.results.get(id)?.title : 'Data on this page')}: ${esc(status.label)}. ${esc(status.sentence)}" data-hint="${esc(status.sentence)}">${icon(statusIcon(status))}${id === null ? `<span>${esc(label)}</span>` : ''}${id === null ? '<span class="freshness-caret" aria-hidden="true">⌄</span>' : ''}</button>`;
  }
  function dataControls() {
    if (s.surface === 'card') return `<div class="filters data-controls"><span class="filter-chip">Last 30 days</span><label class="result-toggle"><input type="checkbox" id="tools-only" ${s.toolsOnly ? 'checked' : ''}> Show only Tool calls</label><span class="filter-note">${store.results.size} visible results</span></div>`;
    return `<div class="filters data-controls"><span class="filter-chip">Last 30 days</span><label>Breakdown <select id="breakdown"><option value="none" ${s.breakdown === 'none' ? 'selected' : ''}>None</option><option value="labels" ${s.breakdown === 'labels' ? 'selected' : ''}>Message label</option></select></label><div class="view-switch" aria-label="Result presentation"><button type="button" data-display="chart" aria-pressed="${s.display === 'chart'}">Chart</button><button type="button" data-display="table" aria-pressed="${s.display === 'table'}">Table</button></div></div>`;
  }
  const bars = [29,46,35,19,49,72,58,28,38,53,74,44,23,35,54,82,60,43,29,68,48,62,87,53,38,64,78,57,72,45];
  function chart(compact = false, offset = 0, labelled = false) {
    return `<div class="chart-area" role="img" aria-label="Example daily counts for the last 30 days">${compact ? '' : '<div class="axis"><span>1.5k</span><span>1k</span><span>500</span><span>0</span></div>'}<div class="bars ${labelled?'labelled-bars':''}">${bars.slice(0,compact ? 15 : 30).map((_,i)=>`<i style="height:${bars[(i+offset)%bars.length]}%">${labelled?'<b class="planning"></b><b class="implementation"></b><b class="review"></b>':''}</i>`).join('')}</div></div>${compact ? '' : '<div class="chart-dates"><span>Oct 9</span><span>Oct 16</span><span>Oct 23</span><span>Oct 30</span><span>Nov 7</span></div>'}`;
  }
  function progress(def) {
    const st = store.stage(def), complete = st.state === 'complete';
    const total = st.total, done = st.done ?? 0;
    const label = total === null ? complete ? 'Done · total unavailable' : st.state === 'running' ? `${number(done)} ${def.unit} checked · total unavailable` : 'Total not available yet' : `${number(done)} of ${number(total)} ${def.unit}`;
    const percent = total === null ? null : Math.min(100, Math.round(done / Math.max(total,1) * 100));
    return `<div class="stage-count"><span>${label}</span><span>${percent === null ? '' : percent + '%'}</span></div><div class="stage-progress ${percent === null ? 'indeterminate' : ''}" role="progressbar" aria-label="${esc(def.label)}" ${percent === null ? `aria-valuetext="${esc(label)}"` : `aria-valuenow="${percent}" aria-valuemin="0" aria-valuemax="100"`}><span style="width:${percent === null ? (complete ? 100 : 26) : percent}%"></span></div>`;
  }
  function emptyState() {
    const r = store.status('analysis');
    return `<section class="empty-state revised-empty"><div class="empty-symbol">${icon(r.busy ? 'refresh' : 'spark')}</div><h2>${r.busy ? 'Your first results are being prepared' : 'No results yet'}</h2><p>${r.busy ? 'You can keep using Pharos while these steps run.' : 'Pharos needs to finish these steps before it can show results.'}</p><div class="empty-stage-list">${store.required('analysis').map(def=>`<div class="empty-stage"><div class="empty-stage-title"><strong>${esc(def.label)}</strong><span>${stageLabel(store.stage(def).state)}</span></div>${progress(def)}</div>`).join('')}</div><button type="button" class="primary" data-action="open" data-result="analysis">${r.busy ? 'View update details' : 'View update details'} ${icon('arrow')}</button><small>Open the update details to see what is waiting.</small></section>`;
  }
  function screen() {
    const isDashboard = s.surface === 'card', status = store.status();
    const heading = `<div class="breadcrumb">${isDashboard ? 'HOME <span>/</span> MY DASHBOARD' : 'ANALYZE <span>/</span> HUMAN WORDS'}</div><div class="page-heading"><div><h1>${isDashboard ? 'A view of your work' : 'Human Words'}</h1><p>${isDashboard ? 'Check when each result was last updated.' : 'Your own words, across your conversations with coding agents.'}</p></div><span class="page-meta">Last checked ${age(store.snapshot.now-store.snapshot.checkedAt)} ago</span></div>`;
    if (isDashboard) {
      const cards = [
        ...s.toolsOnly ? [] : [{id:'words',title:'Human Words',value:'24,680',unit:'Words written by you',offset:0},{id:'labels',title:'Message labels',value:'900',unit:'Labeled messages',offset:6}],
        {id:'tools',title:'Tool calls',value:'3,819',unit:'From conversations in your library',offset:10},
      ];
      return heading + dataControls() + `<div class="dashboard-grid">${cards.map(card=>`<article class="dashboard-card"><div class="dashboard-card-title"><h2>${card.title}</h2>${indicator(card.id,true)}</div><div class="metric-value">${store.status(card.id).kind === 'missing' ? '—' : card.value}</div><div class="metric-foot">${card.unit}</div>${store.status(card.id).kind === 'missing' ? '<p class="card-missing">Waiting for the first results.</p>' : chart(true,card.offset)}</article>`).join('')}</div><p class="dashboard-explanation">${icon('info')}The top bar summarizes these ${store.results.size} results. Hide some cards to see the summary change.</p>`;
    }
    if (status.kind === 'missing') return heading + dataControls() + emptyState();
    const value = store.snapshot.domains.writing.input === store.snapshot.domains.writing.published && status.kind !== 'old-result' ? '26,104' : '24,680';

    return heading + dataControls() + `<div class="metric-grid"><article class="metric"><div class="metric-label">Words written by you</div><div class="metric-value">${value}</div><div class="metric-foot">Last 30 days</div></article><article class="metric"><div class="metric-label">Your messages</div><div class="metric-value">${value==='26,104'?'1,332':'1,204'}</div><div class="metric-foot">Across 42 conversations</div></article><article class="metric"><div class="metric-label">Words per message</div><div class="metric-value">${value==='26,104'?'19.6':'20.5'}</div><div class="metric-foot">Excludes quoted text</div></article></div><section class="chart-card result-surface"><div class="chart-heading"><div><h2>${s.display === 'table' ? (s.breakdown === 'labels' ? 'Your writing by message label' : 'Your writing by repository') : s.breakdown === 'labels' ? 'Your words by message label' : 'Your words over time'}</h2><small>Daily totals · last 30 days</small></div>${indicator('analysis')}</div>${s.display === 'table' ? (s.breakdown==='labels'?'<table class="example-table"><thead><tr><th>Message label</th><th>Your words</th><th>Messages</th></tr></thead><tbody><tr><td>Planning</td><td>8,600</td><td>420</td></tr><tr><td>Implementation</td><td>12,800</td><td>624</td></tr><tr><td>Review</td><td>3,280</td><td>160</td></tr></tbody></table>':'<table class="example-table"><thead><tr><th>Repository</th><th>Your words</th><th>Messages</th></tr></thead><tbody><tr><td>Pharos</td><td>14,420</td><td>702</td></tr><tr><td>Query Table</td><td>6,980</td><td>340</td></tr><tr><td>Other work</td><td>3,280</td><td>162</td></tr></tbody></table>') : (s.breakdown==='labels'?'<div class="breakdown-legend"><span>Planning</span><span>Implementation</span><span>Review</span></div>':'')+chart(false,0,s.breakdown==='labels')}${status.kind==='old-result'?'<div class="result-refresh"><button type="button" class="quiet-button" data-action="refresh-results">Refresh these results</button></div>':''}</section>`;
  }

  const stageLabel = key => ({unknown:'Status unavailable',complete:'Done',running:'Updating',queued:'Waiting to start',waiting:'Waiting',blocked:'Waiting',failed:'Couldn’t finish',paused:'Paused',manual:'On request',pending:'Waiting'})[key];
  function scopeTitle() { return s.target ? store.results.get(s.target)?.title ?? 'This result' : 'Data on this page'; }
  function timing(def, st) {
    if (!store.snapshot.available) return 'Status unavailable';
    if (['failed','waiting','blocked'].includes(st.state)) return st.reason;
    if (st.state === 'running') return 'Running on this Mac';
    if (st.state === 'queued') return 'Waiting for its turn';
    if (st.state === 'paused') return 'Automatic updates paused';
    if (st.state === 'manual') return 'Runs when requested';
    if (st.state === 'complete') return 'Includes changes from the last check';
    if (def.id === 'labels') return 'Hourly · after writing and 10 quiet minutes';
    if (def.id === 'facts') return 'After message labels';
    return 'Next check ' + time(store.snapshot.nextCheckAt);
  }
  function pipeline(defs = store.required(s.target)) {
    return `<ul class="status-stages">${defs.map(def=>{const st=store.stage(def);return `<li class="is-${st.state}"><div class="step-top"><strong>${esc(def.label)}</strong><span class="step-state">${store.snapshot.available?stageLabel(st.state):'Status unavailable'}</span></div>${st.state==='complete'?'':progress(def)}<p class="step-description">${esc(timing(def,st))}</p></li>`;}).join('')}</ul>`;
  }
  function overview() {
    const status=store.status(s.target), snap=store.snapshot;
    const message = status.reason || (status.kind==='clear' ? 'Activity since the last check may not appear here yet.' : status.kind==='behind' ? 'Some changes are not included in these results yet.' : status.kind==='missing' ? 'Pharos needs to finish these steps before it can show results.' : status.kind==='old-result' ? 'Refresh this view to load the newer results.' : 'Pharos can’t tell whether these results are up to date.');
    return `<p class="dialog-description" id="processing-description">${message}</p><div class="check-timing"><span>Checked <strong>${age(snap.now-snap.checkedAt)} ago</strong></span><span>Next check <strong>${snap.available?'in '+age(snap.nextCheckAt-snap.now):'unknown'}</strong></span></div>${['clear','old-result','unknown'].includes(status.kind)?'':pipeline()}<a class="map-entry" href="#map">${icon('graph')}<span>View update details map</span>${icon('arrow')}</a>`;
  }

  function graphData() {
    const required = new Set(store.required(s.target).map(def=>def.id));
    const needLabels = required.has('labels');
    const snap=store.snapshot;
    const nodes = [
      {id:'frequent',kind:'trigger',title:'Every 5 minutes',sub:'Check for new conversations',x:20,y:20,detail:'Checks for new and changed conversations. Results are updated after the changes are read.',active:true},
      {id:'manual',kind:'trigger',title:'Update now in Settings',sub:'An update you request',x:270,y:20,detail:'Review the other updates running on this Mac, then choose Update now in settings.',active:true},
      {id:'hourly',kind:'trigger',title:'Hourly check',sub:'Schedule for message labels',x:520,y:20,detail:'Pharos checks once per hour. Labeling can start after the library has been quiet long enough.',active:needLabels},
      {id:'change',kind:'trigger',title:'New conversation files',sub:'Found during a check',x:770,y:20,detail:'Pharos finds changes when it checks your other apps. More recent activity may not appear here yet.',active:true},
      {id:'auto',kind:'gate',title:'Automatic updates on?',sub:snap.mode==='on'?'On':'Paused or on request',x:20,y:130,detail:'Controls scheduled updates. You can still request an update when automatic updates are off.',active:true,blocked:snap.mode!=='on'},
      {id:'library',kind:'gate',title:'Library connected?',sub:snap.available?'Connected · status available':'Status unavailable',x:270,y:130,detail:'The library must be connected before Pharos can update it.',active:true,blocked:!snap.available},
      {id:'quiet',kind:'gate',title:'Quiet for 10 minutes?',sub:snap.quiet?'Ready':'6 / 10 quiet minutes',x:520,y:130,detail:'Automatic labeling waits until there has been no new library activity for ten minutes.',active:needLabels,blocked:!snap.quiet},
      {id:'busy',kind:'gate',title:'Mac ready for updates?',sub:'Mac idle · plugged in',x:770,y:130,detail:'Large automatic updates may wait while your Mac is busy or on battery power. You can request them sooner in settings.',active:needLabels},
      {id:'cpu',kind:'pool',title:'Processor capacity',sub:`${snap.pools.cpu.used} of ${snap.pools.cpu.limit} updates running`,x:20,y:245,detail:`Example limit on processor work across libraries. ${snap.pools.cpu.holder} is using the available capacity. These numbers are simulated.`,active:true,blocked:snap.pools.cpu.used>=snap.pools.cpu.limit},
      ...definitions.filter(def=>['index','tools','writing'].includes(def.id)).map(def=>({id:def.id,kind:'stage',title:def.label,sub:stageLabel(store.stage(def).state),x:def.id==='tools'?520:270,y:def.id==='writing'?355:245,detail:def.explanation,active:required.has(def.id),definition:def})),
      {id:'enabled',kind:'gate',title:'Labels enabled?',sub:'On · uses graphics processor',x:520,y:355,detail:'Labeling can be turned on in settings. These steps run for views that use message labels.',active:needLabels},
      {id:'gpu',kind:'pool',title:'Graphics processor capacity',sub:`${snap.pools.gpu.used} of ${snap.pools.gpu.limit} updates running`,x:770,y:355,detail:`Example limit on graphics-processor work across libraries. ${snap.pools.gpu.holder} is using this capacity.`,active:needLabels,blocked:snap.pools.gpu.used>=snap.pools.gpu.limit},
      ...definitions.filter(def=>['labels','facts'].includes(def.id)).map(def=>({id:def.id,kind:'stage',title:def.label,sub:stageLabel(store.stage(def).state),x:520,y:def.id==='labels'?465:575,detail:def.explanation,active:required.has(def.id),definition:def})),
      {id:'words-result',kind:'result',title:'Human Words result',sub:'Your writing',x:270,y:575,detail:'This chart shows the results loaded when it opened. Refresh it to include newer results.',active:[...store.results.values()].some(result=>result.dataset==='writing_messages')},
      {id:'tools-result',kind:'result',title:'Tool calls result',sub:'Tool calls',x:20,y:575,detail:'Tool results update separately from writing estimates and message labels.',active:[...store.results.values()].some(result=>result.dataset==='tool_calls')},
      {id:'labels-result',kind:'result',title:'Label-based results',sub:'Needs message labels',x:770,y:575,detail:'A chart broken down by message label also needs the labeling steps to finish.',active:needLabels},
    ];
    const edges = [
      ['frequent','auto','trigger'],['auto','library','gate'],['manual','library','trigger'],['change','library','trigger'],['hourly','quiet','trigger'],['auto','quiet','gate'],['quiet','busy','gate'],['library','index','gate'],
      ['cpu','index','resource'],['cpu','writing','resource'],['cpu','tools','resource'],['tools','writing','order'],['writing','words-result','data'],['tools','tools-result','data'],
      ['manual','enabled','trigger'],['busy','enabled','gate'],['enabled','labels','gate'],['gpu','labels','resource'],['facts','labels-result','data'],
      ...definitions.flatMap(def=>def.needs.map(id=>[id,def.id,'data'])),
    ];
    if(!s.graphAll&&!needLabels&&!required.has('tools')) {
      const visible=nodes.filter(node=>node.active);
      Object.assign(visible.find(node=>node.id==='writing'),{x:520,y:245});
      Object.assign(visible.find(node=>node.id==='words-result'),{x:770,y:245});
      const ids=new Set(visible.map(node=>node.id));
      return {nodes:visible,edges:edges.filter(([from,to])=>ids.has(from)&&ids.has(to)),height:340};
    }
    return {nodes,edges,height:710};
  }
  function mermaidSource() {
    const {nodes,edges}=graphData();
    const quote = value => value.replace(/"/g,"'");
    return 'flowchart TB\n' + nodes.map(n=>`  ${n.id.replaceAll('-','_')}${n.kind==='gate'?'{"':n.kind==='pool'?'(["':'["'}${quote(n.title)}<br/>${quote(n.sub)}${n.kind==='gate'?'"}':n.kind==='pool'?' "])':'"]'}`).join('\n') + '\n' + edges.map(([a,b,kind])=>`  ${a.replaceAll('-','_')} ${kind==='resource'||kind==='order'?'-.->':'-->'}|${kind}| ${b.replaceAll('-','_')}`).join('\n');
  }
  function graph() {
    const {nodes,edges,height}=graphData(), byId=new Map(nodes.map(node=>[node.id,node]));
    const selected=byId.get(s.node)??byId.get('writing');
    const edgePath=(a,b)=>{
      const x1=a.x+100,y1=a.y+72,x2=b.x+100,y2=b.y;
      if(a.id==='cpu'&&a.y===b.y&&b.x>a.x+250) return `M${a.x+100},${a.y+72} L${a.x+100},${a.y+85} L${b.x+100},${a.y+85} L${b.x+100},${b.y+72}`;
      if(a.y===b.y) return `M${a.x+200},${a.y+36} L${b.x},${b.y+36}`;
      if(a.id==='tools'&&b.id==='writing') return `M${a.x+100},${a.y+72} C${a.x+100},${b.y+36} ${b.x+220},${b.y+36} ${b.x+200},${b.y+36}`;
      if(a.id==='writing'&&b.id==='labels') return `M${a.x+200},${a.y+36} C${b.x+45},${a.y+36} ${b.x-50},${b.y+36} ${b.x},${b.y+36}`;
      if(a.id==='cpu'&&b.id==='writing') return `M${a.x+100},${a.y+72} C${a.x+100},${b.y+36} ${b.x-40},${b.y+36} ${b.x},${b.y+36}`;
      if(a.id==='tools'&&b.id==='tools-result') return `M${a.x+200},${a.y+36} L1000,${a.y+36} L1000,690 L120,690 L120,647`;
      if(a.id==='change') return `M${a.x+100},${a.y+72} L${a.x+100},112 L${b.x+100},112 L${b.x+100},${b.y}`;
      if(a.id==='auto'&&b.id==='quiet') return `M${a.x+200},${a.y+36} L245,${a.y+36} L245,115 L${b.x+100},115 L${b.x+100},${b.y}`;
      if(a.id==='manual'&&b.id==='enabled') return `M${a.x+200},${a.y+36} L490,${a.y+36} L490,${b.y+36} L${b.x},${b.y+36}`;
      if(a.id==='words-result') return '';
      return `M${x1},${y1} C${x1},${(y1+y2)/2} ${x2},${(y1+y2)/2} ${x2},${y2}`;
    };
    return `<div class="graph-toolbar"><div class="graph-scope" aria-label="Map scope"><button type="button" data-action="graph-view" aria-pressed="${!s.graphAll}">This view</button><button type="button" data-action="graph-all" aria-pressed="${s.graphAll}">All processing</button></div><button type="button" class="quiet-button" data-action="source">${s.source?'Show graph':'Diagram text'}</button></div><div class="graph-legend"><span><i class="legend-trigger"></i>Starts an update</span><span><i class="legend-gate"></i>Required condition</span><span><i class="legend-pool"></i>Work capacity</span><span>→ Required step or condition</span><span>⇢ Work capacity or sequence</span></div>${s.source ? `<pre class="mermaid-source">${esc(mermaidSource())}</pre><button type="button" class="quiet-button" data-action="copy-graph">Copy diagram text</button>` : `<div class="graph-viewport" role="region" aria-label="Update steps, scroll to explore" tabindex="0"><div class="graph-canvas" style="height:${height}px"><svg class="graph-edges" width="1010" height="${height}" aria-hidden="true"><defs><marker id="edge-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M0 0 L10 5 L0 10Z"/></marker></defs>${edges.map(([from,to,kind])=>{const a=byId.get(from),b=byId.get(to);return `<path d="${edgePath(a,b)}" class="edge-${kind} ${!s.graphAll&&(!a.active||!b.active)?'edge-muted':''}" marker-end="url(#edge-arrow)"/>`;}).join('')}</svg>${nodes.map(node=>`<button type="button" class="graph-node node-${node.kind} ${node.blocked?'node-blocked':''} ${!s.graphAll&&!node.active?'node-muted':''} ${s.node===node.id?'node-selected':''}" style="left:${node.x}px;top:${node.y}px" data-node="${node.id}" aria-pressed="${s.node===node.id}" aria-label="${esc(node.title+'. '+node.sub)}"><span class="node-kind">${({trigger:'Starts an update',gate:'Required condition',pool:'Work capacity',stage:'Step',result:'Result'})[node.kind]}</span><strong>${esc(node.title)}</strong><small>${esc(node.sub)}</small></button>`).join('')}</div></div>`}<div class="graph-selection" aria-live="polite"><strong>${selected.title}</strong><p>${selected.detail}</p>${selected.definition?progress(selected.definition):''}</div><p class="graph-note">Select a step to see its details. Work limits and capacity numbers are examples.</p>`;
  }
  function settings() {
    const snap=store.snapshot, active=store.operations.filter(op=>op.state!=='done');
    const pending=definitions.filter(def=>snap.domains[def.domain].input!==snap.domains[def.domain].published);
    const remaining=pending.filter(def=>!active.some(op=>op.id===def.id));
    const running=active.find(op=>op.state==='running');
    return `<div class="breadcrumb">SETTINGS <span>/</span> BACKGROUND PROCESSING</div><div class="page-heading"><div><h1 id="settings-title" tabindex="-1">Background processing</h1><p>See what is running, then choose when updates run.</p></div><a class="text-link" href="#">Back to ${s.surface==='card'?'dashboard':'Human Words'}</a></div>
      <section class="processing-settings" aria-label="Update settings"><div class="workload"><div class="settings-section-heading"><h2>On this Mac</h2><a class="text-link" href="#map-all">Processing map ${icon('arrow')}</a></div><div class="resource-summary">${Object.entries(snap.pools).map(([id,pool])=>`<div><div class="resource-label"><strong>${id.toUpperCase()}</strong><span>${pool.used} / ${pool.limit} slots</span></div><meter min="0" max="${pool.limit}" value="${pool.used}" aria-label="${id.toUpperCase()} pool usage"></meter><small>${pool.used?esc(pool.holder):'Available'}</small></div>`).join('')}</div><p class="capacity-note">Example work limits across your libraries</p><div class="workload-row"><span>${running?'Running':'This library'}</span><strong>${running?definitions.find(def=>def.id===running.id).label:active.length?active.length+' steps waiting':'No work running'}</strong></div>${running?progress(definitions.find(def=>def.id===running.id)):''}${Object.entries(snap.pools).filter(([,pool])=>pool.external).map(([id,pool])=>`<div class="workload-row"><span>Also running · ${id.toUpperCase()}</span><strong>${esc(pool.holder)}</strong></div>`).join('')}<div class="workload-row"><span>Elsewhere in this library</span><strong>Backup · waiting for Archive SSD</strong></div></div>
      <div class="settings-schedule"><div class="settings-section-heading"><h2>Automatic updates</h2></div><div class="mode-switch" aria-label="Automatic update mode">${[['on','On'],['paused','Paused'],['manual','On request']].map(([id,label])=>`<button type="button" data-mode="${id}" aria-pressed="${snap.mode===id}">${label}</button>`).join('')}</div><p>${snap.mode==='on'?'Writing updates frequently. Labels wait for an hourly check and 10 quiet minutes.':snap.mode==='paused'?'Automatic updates are paused. You can still request a single update.':'Updates run only when you request them.'}</p></div>
      <div class="settings-task-list"><div class="settings-section-heading"><h2>This library</h2><span>${pending.length} steps still needed</span></div>${definitions.map(def=>{const st=store.stage(def);return `<div id="settings-${def.id}" class="settings-task ${s.settingsTask===def.id?'task-highlight':''}"><div class="settings-task-heading"><strong>${def.label}</strong><span>${snap.available?stageLabel(st.state):'Status unavailable'}</span></div><small>${esc(timing(def,st))}</small>${st.state==='complete'?'':progress(def)}</div>`;}).join('')}</div>
      <div class="settings-action"><p>${active.length?active.length+' steps already requested.':remaining.length?'Runs the updates this library still needs.':'No updates are waiting. Checks for new conversations.'}<small>Steps still run in order and wait for space on this Mac.</small></p><button type="button" class="primary" data-action="queue" ${!snap.available||(active.length&&!remaining.length)?'disabled':''}>${active.length&&!remaining.length?'Update requested':store.failed?'Retry update':'Update now'}</button></div>${s.error?`<div class="queue-error" role="alert">${esc(s.error)}</div>`:''}</section>
      <div class="simulation-controls settings-simulation"><span><strong>PROTOTYPE CONTROLS</strong></span><div>${running?'<button type="button" data-action="arrive">New messages found</button>':''}${active.length?`<button type="button" data-action="advance">${running?(running.done===0?'Report progress':'Finish step'):'Start next step'}</button>${Object.values(snap.pools).some(pool=>pool.external>0)?'<button type="button" data-action="release">Finish competing work</button>':''}`:'<button type="button" data-action="reset">Reset example</button>'}</div></div>`;
  }
  function renderDialog() {
    const status=store.status(s.target), isGraph=s.tab==='graph';
    dialog.classList.toggle('wide-dialog',isGraph);
    $('dialog-heading-content').innerHTML=`<div class="dialog-eyebrow">${esc(isGraph&&s.graphAll?'Background processing':scopeTitle())}</div><h2 id="processing-title" class="${isGraph?'':'tone-text-'+status.tone}">${isGraph?'Processing map':esc(status.label)}</h2>`;
    $('dialog-review-controls').innerHTML=`<label for="dialog-scenario">Preview example</label><select id="dialog-scenario" data-scenario-control>${scenarios.map(option=>`<option value="${option.value}" ${s.scenario===option.value?'selected':''}>${esc(option.label)}</option>`).join('')}</select>`;
    $('dialog-content').innerHTML=isGraph?`<p class="sr-only" id="processing-description">See how updates start, what they need and what they are waiting for. Select a step for details.</p>${graph()}`:overview();
    $('dialog-footer').innerHTML=`${isGraph&&!s.settings?'<button type="button" class="quiet-button" data-action="overview">'+icon('arrow')+' Back to status</button>':'<span></span>'}<a class="settings-link" href="#settings">${s.settings?'Back to settings':'Update settings'} ${icon('arrow')}</a>`;
  }

  function render() {
    registerVisible();
    $('screen').innerHTML=s.settings?settings():screen();
    $('page-freshness').hidden=s.settings;
    $('page-freshness').innerHTML=indicator();
    $('scenario').value=s.scenario;
    document.querySelector('[href="#settings"]').setAttribute('aria-current',s.settings?'page':'false');
    document.querySelectorAll('[data-surface]').forEach(button=>{if(!s.settings&&button.dataset.surface===s.surface)button.setAttribute('aria-current','page');else button.removeAttribute('aria-current');});
    if(dialog.open)renderDialog();
  }
  function reset(scenario=s.scenario, preserveView=false) {
    const viewport=dialog.querySelector('.graph-viewport');
    const position=viewport?{left:viewport.scrollLeft,top:viewport.scrollTop}:null;
    s.scenario=scenario;s.receipts.clear();s.error='';
    const url=new URL(location.href);url.searchParams.set('state',scenario);history.replaceState(null,'',url);
    if(!preserveView){s.target=null;s.tab='overview';s.node='writing';s.source=false;}
    store=new Store(scenario,s.surface==='empty'&&!['clear','rendered-old'].includes(scenario));
    render();
    if(preserveView&&position){const next=dialog.querySelector('.graph-viewport');if(next){next.scrollLeft=position.left;next.scrollTop=position.top;}}
  }

  function show(button) {invoker=button;s.target=button.dataset.result??null;s.tab='overview';s.error='';renderDialog();dialog.showModal();}
  dialog.addEventListener('close',()=>{if(location.hash.startsWith('#map'))history.replaceState(null,'',location.pathname+location.search+(s.settings?'#settings':''));if(s.settings){invoker=null;return;}(invoker?.isConnected?invoker:s.target?document.querySelector(`#screen [data-result="${s.target}"]`):document.querySelector('#page-freshness button'))?.focus({preventScroll:true});invoker=null;});
  dialog.addEventListener('keydown',event=>{if(event.key!=='Tab')return;const controls=[...dialog.querySelectorAll('button:not(:disabled),a[href],select,input:not(:disabled),[tabindex="0"]')].filter(el=>el.getClientRects().length);const first=controls[0],last=controls.at(-1);if(event.shiftKey&&document.activeElement===first){event.preventDefault();last.focus();}else if(!event.shiftKey&&document.activeElement===last){event.preventDefault();first.focus();}});
  dialog.addEventListener('click',event=>{if(event.target!==dialog)return;const r=dialog.getBoundingClientRect();if(event.clientX<r.left||event.clientX>r.right||event.clientY<r.top||event.clientY>r.bottom)dialog.close();});
  document.addEventListener('click',async event=>{
    const button=event.target.closest('button');if(!button)return;
    if(button.dataset.surface){s.surface=button.dataset.surface;s.settings=false;history.replaceState(null,'',location.pathname+location.search);reset();return;}
    if(button.dataset.display){s.display=button.dataset.display;render();document.querySelector(`[data-display="${s.display}"]`).focus();return;}
    if(button.dataset.node){const viewport=dialog.querySelector('.graph-viewport');const pos={left:viewport.scrollLeft,top:viewport.scrollTop};s.node=button.dataset.node;renderDialog();const next=dialog.querySelector('.graph-viewport');next.scrollLeft=pos.left;next.scrollTop=pos.top;dialog.querySelector(`[data-node="${s.node}"]`).focus({preventScroll:true});return;}
    if(button.dataset.mode){store.snapshot.mode=button.dataset.mode;render();document.querySelector(`[data-mode="${button.dataset.mode}"]`).focus();return;}
    switch(button.dataset.action){
      case 'open':show(button);break;
      case 'close':dialog.close();break;
      case 'queue':{const reply=definitions.every(def=>store.snapshot.domains[def.domain].input===store.snapshot.domains[def.domain].published)?store.checkSources():store.queue(['writing_messages','tool_calls','people_message_labels']);s.error=reply.error??'';render();document.querySelector('#screen [data-action="queue"]')?.focus();announce(s.error||`${reply.added} steps requested.`);break;}
      case 'advance':{const outcome=store.advance();s.error=outcome==='blocked'?'Waiting for another step or other work on this Mac to finish.':'';render();focusAction('advance');announce(outcome==='blocked'?s.error:'Update progress changed.');break;}
      case 'release':store.release();s.error='';render();focusAction('advance');break;
      case 'arrive':store.arrive();render();focusAction('advance');announce('New messages arrived. They will be included in a later update.');break;
      case 'refresh-results':s.receipts.clear();s.scenario='clear';render();announce('Results refreshed. Activity since the last check may not appear yet.');break;
      case 'overview':s.tab='overview';history.replaceState(null,'',location.pathname+location.search);renderDialog();dialog.querySelector('.close-dialog').focus();break;
      case 'graph-view':s.graphAll=false;renderDialog();focusAction('graph-view');break;
      case 'graph-all':s.graphAll=true;renderDialog();focusAction('graph-all');break;
      case 'source':s.source=!s.source;renderDialog();focusAction('source');break;
      case 'copy-graph':try{await navigator.clipboard.writeText(mermaidSource());button.textContent='Copied';}catch{announce('Clipboard unavailable. Select and copy the Diagram text.');}break;
      case 'reset':reset(s.scenario,true);focusAction('close');break;
    }
  });
  document.addEventListener('change',event=>{
    if(event.target.matches('[data-scenario-control]')){const controlId=event.target.id;reset(event.target.value,true);document.getElementById(controlId)?.focus({preventScroll:true});announce(`Example changed: ${scenarios.find(option=>option.value===s.scenario).label}.`);return;}
    if(event.target.id==='breakdown'){s.breakdown=event.target.value;render();$('breakdown').focus();return;}
    if(event.target.id==='tools-only'){s.toolsOnly=event.target.checked;render();$('tools-only').focus();}
  });
  $('reset').addEventListener('click',()=>reset('minutes',true));
  $('theme-toggle').addEventListener('click',()=>{const dark=document.documentElement.dataset.theme!=='dark';document.documentElement.dataset.theme=dark?'dark':'light';$('theme-toggle').textContent=dark?'Light theme':'Dark theme';$('theme-toggle').setAttribute('aria-label',`Switch to ${dark?'light':'dark'} theme`);});
  function route() {
    const hash=location.hash;
    if(hash==='#settings') {
      s.settingsTask=store.required(s.target).find(def=>store.stage(def).state!=='complete')?.id??null;
      s.settings=true;
      if(dialog.open)dialog.close();
      render();$('settings-title').focus({preventScroll:true});window.scrollTo(0,0);
    } else if(hash==='#map'||hash==='#map-all') {
      if(!dialog.open){invoker=document.activeElement;s.target=null;}
      s.tab='graph';s.graphAll=hash==='#map-all';s.source=false;renderDialog();if(!dialog.open)dialog.showModal();focusAction('graph-view');
    } else {s.settings=false;if(dialog.open)dialog.close();render();}
  }
  window.addEventListener('hashchange',route);
  render();route();
})();
