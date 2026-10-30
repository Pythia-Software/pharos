
window.initPharosAsk = function(root, {chartPreviews} = {}) {
  const product = root.querySelector('.pharos-product');
  const cleanup=[];
  const listen=(target,name,fn,options)=>{target.addEventListener(name,fn,options);cleanup.push(()=>target.removeEventListener(name,fn,options));};
  const find = selector => root.querySelector(selector);

  const ai = find('.pharos-ai');
  const regular = find('.pharos-regular');
  const invite = find('.pharos-invitation');
  const consent = find('.pharos-consent');

  let showInvitation = true, invitationDismissed = false;
  let loadingModels=false,providers=[], accessEnabled=false, disposed=false, draftTimer=null, writeQueue=Promise.resolve();
  const providerModels={},providerCatalog={};
  const report=error=>{searchNotice.hidden=false;searchNotice.textContent=error.message||String(error);};
  const api=async(path,body,signal,onProgress)=>{
    const response=await fetch("/api/ask/"+path,{method:body?"POST":"GET",headers:{"Content-Type":"application/json",...(onProgress?{Accept:'application/x-ndjson'}:{})},...(body?{body:JSON.stringify(body)}:{}),signal});
    return askReadResponse(response,onProgress);
  };
  const modalReturns = new WeakMap();
  const searchNotice = find('.pharos-search-notice');
  const thread = find('.pharos-thread');
  const threadWrap=find('.pharos-thread-wrap'),threadViewport=find('.pharos-thread-viewport'),latestButton=find('.pharos-latest');
  let threadFollowing=true,threadLayoutFrame=null;
  const turnViews = new Map();
  let turns = [];
  let clarificationDraft='';
  const connectProvider = find('#pharos-connect-provider');
  const question = find('#pharos-question');
  const promptEditor=initAskPromptEditor(question);
  const picker = find('#pharos-model-menu');
  const pickerTrigger = find('.pharos-model-trigger');
  const chatHistory = find('#pharos-chat-history'),historyTrigger=find('.pharos-history-trigger'),historyList=find('.pharos-history-list');
  const choices = {provider:'',model:'',effort:'Default'};
  const modelLabel = () => providerModels[choices.provider]?.[choices.model] || choices.model || 'Choose a model';
  const validSelection = value => value && providers.some(p=>p.id===value.provider) && typeof value.model==='string';
  const copySelection = value => ({provider:value.provider,model:value.model,effort:value.effort});
  let defaultSelection = null;
  async function loadModels(provider) {
    if(providerCatalog[provider]&&Date.now()-providerCatalog[provider].loadedAt<300000)return providerCatalog[provider];
    const result=await api('models?provider='+encodeURIComponent(provider));
    result.loadedAt=Date.now();providerCatalog[provider]=result;providerModels[provider]=Object.fromEntries(result.models.map(m=>[m.id,m.label]));return result;
  }
  const defaultFor=provider=>{const catalog=providerCatalog[provider],models=catalog?.models||[],model=models.find(m=>m.default)||models[0];return catalog?.defaultSelection||{provider,model:model?.id||'',effort:'Default'};};
  function effortChoices(){return providerCatalog[choices.provider]?.models.find(m=>m.id===choices.model)?.efforts||['Default'];}
  async function refreshStatus() {
    const status=await api('status');if(disposed)return;
    providers=status.providers;accessEnabled=status.settings.enabled;showInvitation=status.settings.showInvitation;
    mode=!providers.length?'missing':accessEnabled?'enabled':showInvitation?'available':'declined';
    connectProvider.replaceChildren(...providers.map(p=>{const option=document.createElement('option');option.value=option.textContent=p.id;return option;}));
    if(!accessEnabled||!providers.length){surfaceRestorePending=false;accessEnabled=false;stop();render();return;}
    if(status.settings.selection?.provider&&!providers.some(p=>p.id===status.settings.selection.provider)){regularOverride=true;Object.assign(choices,{provider:status.settings.selection.provider,model:'',effort:'Default'});stop();render();report(new Error(status.settings.selection.provider+' is unavailable. Choose another CLI in Settings → Ask Pharos.'));return;}
    loadingModels=true;render();
    const provider=providers.some(p=>p.id===status.settings.selection?.provider)?status.settings.selection.provider:providers[0].id;
    try {await loadModels(provider);defaultSelection=validSelection(status.settings.selection)?status.settings.selection:null;if(!turns.length&&!activeSessionId)Object.assign(choices,defaultSelection||defaultFor(provider));connectProvider.value=provider;}
    catch(error){report(error);regularOverride=true;}
    loadingModels=false;const restoring=surfaceRestorePending,restored=restoreSurface();await loadChatHistory();
    if(restoring&&askRestorePresentation(location.href,savedSurface)&&(!savedSurface.id||savedSurface.id===activeSessionId)&&promptEditor.restore(savedSurface.prompt))contextURI=promptEditor.chips().find(chip=>chip.id==='page')?.value||'';
    render();
    if(restored&&presentation==='modal')question.focus({preventScroll:true});
  }
  let ideaRepositories=[],ideaVariation=0,ideas=askPromptSuggestions();
  const suggestions = find('.pharos-suggestions');
  const ideaViewport=find('.pharos-idea-viewport'),ideaRow=find('.pharos-suggestion-list');
  const ideaToggle=find('.pharos-idea-toggle'),ideaActions=find('.pharos-idea-actions');
  const ideaHiddenKey='pharos-ask-suggestions-hidden';
  let ideasHidden=window.pharosPrefs?.get(ideaHiddenKey,false)===true;
  let ideaContextRequested=false;
  const ideaContextController=new AbortController();
  const defaultIdeaCorners=[[0],[2],[1,3],[3],[0,2],[1],[0,2],[1,3],[2]];
  let ideaCorners=ideas.map((_,index)=>[...defaultIdeaCorners[index%defaultIdeaCorners.length]]);
  // Choose each ribbon once. Resize, carousel movement and theme changes keep it stable.
  let ideaRibbons=ideaCorners.map(corners=>{
    const phase=Math.floor(Math.random()*360);
    return corners.map((_,index)=>({phase:(phase+index*(110+Math.floor(Math.random()*140)))%360,roundness:Math.random(),reach:Math.random(),depth:Math.random()}));
  });
  let ideaOrder=ideas.map((_,index)=>index);
  let ideaPage = 0, ideaCenter=1,ideaPaused=matchMedia('(prefers-reduced-motion: reduce)').matches,ideaElapsed = 0, ideaLastTick = Date.now();
  let ideaCount=3,ideaStride=0,ideaSize='',ideaMoving=false,ideaScrollEnd=null,ideaMotion=null;
  const ideaCycleMs = 30000;
  let appearance = matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  let pickerPage = null;

  let mode = 'missing';
  let regularOverride = false;
  let pending = null;
  let ticker = null,pollTimer=null;
  let serial = 0;
  let frozenAt = null;
  let sessions=[],activeSessionId=null,activeCreatedAt=0,activeUpdatedAt=0,sessionLoad=0,openingSession=null;
  let historyQuery="",historyOffset=0,historyMore=false,historyLoad=0,historyTimer=null;
  let draftPending=null,cancelQueue=Promise.resolve();
  // One composer owns the chat whether it is on Home, in a modal, or tucked away.
  const mountHost=root.parentElement;
  let homeHost=document.getElementById('homeAskSearch')||mountHost;
  homeHost.append(root);
  listen(window,'pharos:ask-home',event=>{homeHost=event.detail||mountHost;restoreHome();});
  const overlay=document.createElement('dialog');
  overlay.id='askPharosDialog';overlay.className='pharos-ask-dialog';overlay.setAttribute('aria-label','Ask Pharos');
  let overlayLayoutFrame=null,overlayViewportHeight=0;
  function overlayViewport(){return {height:window.visualViewport?.height||window.innerHeight,top:window.visualViewport?.offsetTop||0};}
  function overlayPadding(){const style=getComputedStyle(overlay);return parseFloat(style.paddingTop)+parseFloat(style.paddingBottom);}
  function composerHeight(){const frame=find('.pharos-ai-frame'),style=getComputedStyle(frame);return Math.ceil(find('.pharos-ai-inner').getBoundingClientRect().height+parseFloat(style.paddingTop)+parseFloat(style.paddingBottom));}
  function sizeOverlay(){
    if(!overlay.open||disposed)return;
    const viewport=overlayViewport(),frame=find('.pharos-ai-frame'),frameHeight=ai.hidden?0:composerHeight();
    const padding=overlayPadding(),contentHeight=Math.ceil(root.getBoundingClientRect().height-frame.getBoundingClientRect().height+frameHeight+padding);
    const transcriptHeight=threadWrap.hidden?0:threadWrap.getBoundingClientRect().height+parseFloat(getComputedStyle(threadWrap).marginBottom);
    const draftGrowth=Math.max(0,question.getBoundingClientRect().height-parseFloat(getComputedStyle(question).minHeight));
    const emptyHeight=ai.hidden?contentHeight:frameHeight+padding-transcriptHeight-draftGrowth;
    const layout=askDialogLayout({viewportHeight:viewport.height,contentHeight,emptyHeight});
    // A window resize or on-screen keyboard must keep controls reachable at once.
    if(overlayViewportHeight!==viewport.height)delete overlay.dataset.positioned;
    overlayViewportHeight=viewport.height;
    frame.style.setProperty('--pharos-dialog-frame-height',frameHeight+'px');
    overlay.style.setProperty('--pharos-dialog-top',viewport.top+layout.top+'px');
    overlay.style.maxHeight=viewport.height+'px';overlay.style.height=layout.height+'px';
    if(!overlay.dataset.positioned){overlay.getBoundingClientRect();overlay.dataset.positioned='true';}
  }
  function scheduleOverlayLayout(){
    if(disposed||!overlay.open||overlayLayoutFrame!==null)return;
    overlayLayoutFrame=requestAnimationFrame(()=>{overlayLayoutFrame=null;sizeOverlay();});
  }
  const overlayObserver=new ResizeObserver(scheduleOverlayLayout);overlayObserver.observe(root);
  listen(window,'resize',()=>{scheduleThreadLayout();scheduleOverlayLayout();});
  if(window.visualViewport){listen(window.visualViewport,'resize',()=>{scheduleThreadLayout();scheduleOverlayLayout();});listen(window.visualViewport,'scroll',scheduleOverlayLayout);}
  overlay.addEventListener('transitionend',event=>{if(event.target===overlay){sizePicker();sizeHistory();}});
  const floating=document.createElement('aside');
  floating.className='pharos-floating';floating.hidden=true;floating.setAttribute('aria-label','Ask Pharos progress');
  floating.innerHTML='<button type="button" class="pharos-floating-drag" aria-label="Move Ask Pharos progress" title="Drag to move; use arrow keys to reposition">⠿</button><button type="button" class="pharos-floating-open" title="Reopen Ask Pharos"><div class="pharos-floating-copy"><strong>Ask Pharos</strong><span></span></div><svg class="pharos-floating-expand" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M8 3H3v5m13-5h5v5M3 16v5h5m13-5v5h-5M3 3l6 6m12-6-6 6M3 21l6-6m12 6-6-6"/></svg></button><button type="button" class="pharos-floating-dismiss" aria-label="Dismiss Ask Pharos progress">×</button>';
  const announcement=document.createElement('span');announcement.className='pharos-floating-announcement';announcement.setAttribute('role','status');
  document.body.append(overlay,floating,announcement);
  const headerButton=document.getElementById('askPharosToggle');
  if(headerButton)headerButton.disabled=false;
  const composerGlow=createPharosGlow(find('.pharos-ai-frame'));
  const glows=[composerGlow,createPharosGlow(floating,{radius:13})];
  if(headerButton)glows.push(createPharosGlow(headerButton.querySelector('.ask-pharos-icon'),{outline:document.querySelector('#ph-icon-chat path').getAttribute('d')}));
  const browseButton=find('.pharos-browse'),windowClose=find('.pharos-window-close'),composerHeader=find('.pharos-composer-header');
  let presentation='home',returnFocus=null,contextURI='',floatPosition=null,drag=null,suppressReopen=false;
  function updateSurface() {
    const turn=turns.at(-1),state=askFloatingState(turn);
    glows.forEach(glow=>glow.setWorking(!!pending));
    floating.hidden=presentation!=='floating';
    floating.dataset.ready=String(state.ready);
    floating.querySelector('.pharos-floating-open span').textContent=state.message;
    browseButton.hidden=ai.hidden;
    const browseHint=find('.pharos-browse-hint'),showBrowseHint=askShowBrowseHint({pending:!!pending&&!ai.hidden,browsing:!!pending?.browsing,presentation});
    browseHint.hidden=!showBrowseHint;
    if(showBrowseHint)browseButton.setAttribute('aria-describedby',browseHint.id);else browseButton.removeAttribute('aria-describedby');
    browseButton.title=showBrowseHint?'':'Picture in picture · keep browsing';
    windowClose.hidden=presentation!=='modal';
    // Keep the same close control available when switching to regular search.
    const headerHost=ai.hidden?product:find('.pharos-ai-inner');
    if(composerHeader.parentElement!==headerHost)headerHost.prepend(composerHeader);
    composerHeader.hidden=windowClose.hidden&&find('.pharos-clear-chat').hidden;
    if(headerButton){
      const ready=String(presentation!=='home'&&presentation!=='modal'&&state.ready),title=state.ready&&presentation!=='home'?'Ask Pharos · '+state.message:'Ask Pharos about this page';
      if(headerButton.dataset.ready!==ready)headerButton.dataset.ready=ready;
      if(headerButton.title!==title)headerButton.title=title;
    }
  }
  function restoreHome() {
    if(presentation==='modal'||homeHost===mountHost||!['/','/home'].includes(location.pathname))return;
    const returning=presentation!=='home'||root.parentElement!==homeHost;
    if(!returning)return;
    presentation='home';if(root.parentElement!==homeHost)homeHost.append(root);
    chartPreviews?.revalidate();
    if(!new URL(location.href).searchParams.has('ask_chat'))syncChatURL(activeSessionId);
    render();
  }
  function restoreFocus() {
    const target=returnFocus!==document.body&&returnFocus?.isConnected&&returnFocus.getClientRects().length?returnFocus:headerButton?.getClientRects().length?headerButton:document.getElementById('headerMore');
    target?.focus({preventScroll:true});
  }
  function openOverlay() {
    surfaceRestorePending=false;
    if(presentation==='modal')return;
    returnFocus=document.activeElement;
    contextURI=askPageURI(location.href);
    promptEditor.setChip({id:'page',type:'page',value:contextURI,label:'About '+(window.historyTitle?.(contextURI)||new URL(contextURI,location.origin).pathname)});
    regularOverride=false;
    delete overlay.dataset.positioned;overlay.style.removeProperty('height');overlay.style.removeProperty('--pharos-dialog-top');
    presentation='modal';overlay.append(root);overlay.showModal();
    chartPreviews?.revalidate();
    render();sizeThread();sizeOverlay();question.focus({preventScroll:true});
    if(mode==='available'||mode==='declined')openModal(consent);
  }
  function browse(focus=true) {
    surfaceRestorePending=false;
    if(pending)pending.browsing=true;
    closePicker();closeHistory();
    presentation='floating';
    if(overlay.open)overlay.close();
    // A closed dialog retains the composer while the separate progress panel is visible.
    overlay.append(root);updateSurface();
    // The window may have changed size while this panel was hidden.
    const rect=floating.getBoundingClientRect();positionFloating(rect.left,rect.top);
    if(focus)restoreFocus();
  }
  function closeOverlay() {
    surfaceRestorePending=false;
    if(pending){browse();return;}
    presentation=['/','/home'].includes(location.pathname)?'home':'dismissed';
    overlay.close();
    if(presentation==='home')homeHost.append(root);
    updateSurface();restoreFocus();
  }
  function notifyCompletion(turn) {
    if(!['floating','dismissed'].includes(presentation))return;
    announcement.textContent='Ask Pharos: '+askFloatingState(turn).message;
    window.feedbackToast?.(announcement.textContent);
    updateSurface();
  }
  function positionFloating(x,y) {
    const rect=floating.getBoundingClientRect();
    floatPosition=askFloatingPosition({x,y,width:rect.width,height:rect.height,viewportWidth:innerWidth,viewportHeight:innerHeight});
    floating.style.left=floatPosition.x+'px';floating.style.top=floatPosition.y+'px';floating.style.right='auto';floating.style.bottom='auto';
  }
  listen(window,'pharos:ask-open',openOverlay);
  windowClose.addEventListener('click',closeOverlay);
  browseButton.addEventListener('click',()=>browse());
  overlay.addEventListener('cancel',event=>{if(event.defaultPrevented)return;event.preventDefault();closeOverlay();});
  // Nested model/history menus consume Escape before closing the whole search.
  root.addEventListener('keydown',event=>{if(event.key==='Escape'&&event.defaultPrevented)event.stopPropagation();});
  floating.querySelector('.pharos-floating-open').addEventListener('click',openOverlay);
  floating.querySelector('.pharos-floating-dismiss').addEventListener('click',()=>{surfaceRestorePending=false;presentation='dismissed';updateSurface();});
  const handle=floating.querySelector('.pharos-floating-drag');
  // Like picture in picture: drag the panel itself, or tap to reopen it.
  floating.addEventListener('pointerdown',event=>{
    suppressReopen=false;drag=null;
    if(event.button!==0||event.target.closest('.pharos-floating-dismiss'))return;
    const rect=floating.getBoundingClientRect();
    drag={id:event.pointerId,x:event.clientX-rect.left,y:event.clientY-rect.top,startX:event.clientX,startY:event.clientY,moved:false};
  },true);
  floating.addEventListener('pointermove',event=>{
    if(drag?.id!==event.pointerId)return;
    if(!drag.moved&&Math.hypot(event.clientX-drag.startX,event.clientY-drag.startY)<4)return;
    drag.moved=true;floating.setPointerCapture(event.pointerId);
    positionFloating(event.clientX-drag.x,event.clientY-drag.y);
  });
  floating.addEventListener('pointerup',()=>{suppressReopen=!!drag?.moved;drag=null;},true);
  for(const name of ['pointercancel','lostpointercapture'])floating.addEventListener(name,()=>{drag=null;});
  floating.addEventListener('click',event=>{
    if(suppressReopen&&event.detail>0){suppressReopen=false;event.preventDefault();event.stopPropagation();}
  },true);
  handle.addEventListener('keydown',event=>{
    const movement={ArrowLeft:[-20,0],ArrowRight:[20,0],ArrowUp:[0,-20],ArrowDown:[0,20]}[event.key];if(!movement)return;
    event.preventDefault();const rect=floating.getBoundingClientRect();positionFloating(rect.left+movement[0],rect.top+movement[1]);
  });
  listen(window,'resize',()=>{if(floatPosition&&!floating.hidden)positionFloating(floatPosition.x,floatPosition.y);});
  const activeKey='pharos-ask-active-'+(window.pharosPreviewScope||'local');
  const surfaceKey=activeKey+'-surface';
  let savedSurface=null,surfaceRestorePending=true;
  try{savedSurface=JSON.parse(sessionStorage.getItem(surfaceKey));}catch{}
  function restoreSurface(){
    if(!surfaceRestorePending)return false;
    surfaceRestorePending=false;
    const restored=askRestorePresentation(location.href,savedSurface);
    if(!restored)return false;
    if(restored==='modal')openOverlay();
    else if(restored==='floating')browse(false);
    else if(restored!=='home'){presentation='dismissed';overlay.append(root);}
    contextURI=typeof savedSurface.contextURI==='string'?savedSurface.contextURI:'';
    const point=savedSurface.floatPosition;
    if(point&&Number.isFinite(point.x)&&Number.isFinite(point.y)&&restored==='floating')positionFloating(point.x,point.y);
    updateSurface();return restored!=='home';
  }
  function currentSession(){return {id:activeSessionId,createdAt:activeCreatedAt,updatedAt:activeUpdatedAt,conversation:turns,draft:question.value,clarificationDraft,selection:copySelection(choices)};}
  function sessionRecords(){return activeSessionId?[currentSession(),...sessions.filter(s=>s.id!==activeSessionId)]:sessions;}
  function writeHistory(){try{if(activeSessionId)sessionStorage.setItem(activeKey,activeSessionId);else sessionStorage.removeItem(activeKey);}catch{}}
  function syncChatURL(id, push=false){
    if(disposed||presentation!=='home')return;
    const href=askChatURL(location.href,id);
    if(href===location.href)return;
    history[push?'pushState':'replaceState'](history.state,'',href);
    window.dispatchEvent(new Event('pharos:uri-changed'));
  }
  function flushDraft(){clearTimeout(draftTimer);draftTimer=null;const payload=draftPending;draftPending=null;if(payload)writeQueue=writeQueue.catch(()=>{}).then(()=>api('session',payload)).catch(report);return writeQueue;}
  function saveChat(touch=true){
    if(disposed)return;
    surfaceRestorePending=false;
    if(turns.length||question.value.length){if(!activeSessionId){sessionLoad++;activeSessionId=crypto.randomUUID();activeCreatedAt=Date.now();syncChatURL(activeSessionId);}if(touch)activeUpdatedAt=Date.now();sessions=sessionRecords();}
    writeHistory();syncComposerActions();
    clearTimeout(draftTimer);
    if(!activeSessionId||!accessEnabled)return;
    draftPending={id:activeSessionId,draft:question.value,clarificationDraft,selection:copySelection(choices)};
    draftTimer=setTimeout(flushDraft,300);
  }
  function historyDate(timestamp) {
    if(!timestamp)return 'Earlier';
    const date=new Date(timestamp),today=new Date(),yesterday=new Date(today);yesterday.setDate(today.getDate()-1);
    return date.toDateString()===today.toDateString()?'Today':date.toDateString()===yesterday.toDateString()?'Yesterday':date.toLocaleDateString(undefined,{month:'short',day:'numeric',...(date.getFullYear()!==today.getFullYear()?{year:'numeric'}:{})});
  }
  function renderHistory() {
    const entries=askHistoryEntries({sessions,current:activeSessionId?currentSession():null,query:historyQuery});
    historyList.replaceChildren();find('.pharos-history-empty').hidden=entries.length>0;find('.pharos-history-more').hidden=!historyMore;
    for(const session of entries){
      const first=session.conversation[0],last=session.conversation.at(-1),title=first?.prompt||session.draft||'Untitled chat';
      const button=document.createElement('a');button.href=askHistoryURL(location.href,session.id);button.className='pharos-history-entry cursor-interaction';
      button.setAttribute('aria-label','Open chat: '+title);button.setAttribute('aria-current',String(session.id===activeSessionId));button.dataset.sessionId=session.id;
      const heading=document.createElement('span');heading.className='pharos-history-title';heading.textContent=title;heading.dataset.tooltip=title;
      const meta=document.createElement('span');meta.className='pharos-history-meta';
      const count=session.questionCount??session.conversation.length;
      meta.textContent=[historyDate(session.updatedAt),last?.cli??session.selection.provider,count?count+' question'+(count===1?'':'s'):'Draft',session.id===activeSessionId?'Current':null].filter(Boolean).join(' \u00b7 ');
      button.append(heading,meta);button.addEventListener('click',event=>{if(event.button||event.metaKey||event.ctrlKey||event.shiftKey||event.altKey)return;event.preventDefault();void openSession(session.id,true);});historyList.append(button);
    }
  }
  function closeHistory(focus=false) {
    chatHistory.hidden=true;ai.classList.remove('pharos-history-open');historyTrigger.setAttribute('aria-expanded','false');if(focus)historyTrigger.focus();
  }
  async function refreshHistory(more=false){
    const request=++historyLoad;
    try{const result=await api('sessions?q='+encodeURIComponent(historyQuery)+'&offset='+(more?historyOffset:0));if(request!==historyLoad)return;sessions=more?[...sessions,...result.sessions]:result.sessions;historyOffset=(more?historyOffset:0)+result.sessions.length;historyMore=result.sessions.length===100;if(!chatHistory.hidden){renderHistory();sizeHistory();}}catch(error){report(error);}
  }
  find('.pharos-history-filter').addEventListener('input',event=>{historyQuery=event.target.value;clearTimeout(historyTimer);historyTimer=setTimeout(()=>refreshHistory(),200);});
  find('.pharos-history-more').addEventListener('click',()=>refreshHistory(true));
  function openHistory() {
    if(historyTrigger.hidden||ai.hidden)return;
    closePicker();saveChat(false);renderHistory();chatHistory.hidden=false;historyTrigger.setAttribute('aria-expanded','true');sizeHistory();find('.pharos-history-close').focus();void refreshHistory();
  }
  function sizeHistory() {
    if(chatHistory.hidden)return;
    const anchor=find('.pharos-composer-actions').getBoundingClientRect(),bounds=product.getBoundingClientRect();
    product.style.setProperty('--pharos-history-width',Math.max(0,Math.min(340,anchor.right-bounds.left-16))+'px');
    // The floating dialog can grow upward to make room below the composer.
    // An above-menu would sit outside its bounds and be clipped by the dialog.
    const height=chatHistory.getBoundingClientRect().height,above=!overlay.open&&anchor.top>=height+12;
    chatHistory.dataset.placement=above?'above':'below';
    ai.classList.add('pharos-history-open');ai.style.setProperty('--pharos-history-space',above?'0px':height+8+'px');
  }
  function activateSession(session) {
    activeSessionId=session.id;activeCreatedAt=session.createdAt;activeUpdatedAt=session.updatedAt;
    turns=session.conversation.map(turn=>({...turn}));question.value=session.draft||'';clarificationDraft=session.clarificationDraft||'';Object.assign(choices,session.selection);writeHistory();
    threadFollowing=true;
    const latest=turns.at(-1);if(latest?.status==='loading')queueMicrotask(()=>{if(activeSessionId===session.id&&!pending)watchRequest(latest);});
  }
  async function loadSession(id){
    const session=await api('session?id='+encodeURIComponent(id));
    if(providers.some(p=>p.id===session.selection.provider))try{await loadModels(session.selection.provider);}catch(error){report(error);}
    return session;
  }
  async function openSession(id,navigate=false){
    const request=++sessionLoad;
    stop();saveChat(false);flushDraft();closeHistory();closePicker();
    const focused=document.activeElement;
    openingSession=request;syncComposer();
    try {
      await writeQueue;
      const session=await loadSession(id);
      if(disposed||request!==sessionLoad)return;
      activateSession(session);regularOverride=false;syncChatURL(id,navigate);renderThread();render();
    }catch(error){if(!disposed&&request===sessionLoad)report(error);}
    finally{if(openingSession===request){openingSession=null;syncComposer();if(navigate&&!disposed&&request===sessionLoad&&(document.activeElement===focused||document.activeElement===document.body))question.focus({preventScroll:true});}}
  }
  function clearChat(navigate=true){
    sessionLoad++;openingSession=null;
    stop();saveChat(false);flushDraft();closeHistory();closePicker();
    turns=[];activeSessionId=null;activeCreatedAt=0;activeUpdatedAt=0;question.value='';clarificationDraft='';threadFollowing=true;writeHistory();syncChatURL(null,!!navigate);renderThread();render();if(navigate)question.focus({preventScroll:true});
  }
  async function loadChatHistory(){
    const request=++sessionLoad;
    const data=await api('sessions');if(disposed||request!==sessionLoad)return;sessions=data.sessions;
    if(turns.length||question.value.length)return;
    const id=new URL(location.href).searchParams.get('ask_chat')||sessionStorage.getItem(activeKey);
    if(id){
      openingSession=request;syncComposer();
      try{const session=await loadSession(id);if(disposed||request!==sessionLoad||turns.length||question.value.length)return;activateSession(session);syncChatURL(id);renderThread();}
      catch(error){if(!disposed&&request===sessionLoad)report(error);}
      finally{if(openingSession===request){openingSession=null;syncComposer();}}
    }
  }
  function render() {
    const enabled = mode === 'enabled' && !regularOverride;
    ai.hidden = !enabled;
    suggestions.hidden = !enabled || turns.length>0;
    regular.hidden = enabled;
    invite.hidden = mode !== 'available' || !showInvitation || invitationDismissed || consent.open;
    find('.pharos-ai-mode').hidden = mode !== 'enabled';
    find('.pharos-current-settings').textContent = loadingModels?'Loading available models…':choices.provider + ' \u00b7 ' + modelLabel() + ' \u00b7 ' + choices.effort;
    find('.pharos-selected-provider').textContent = choices.provider;
    find('.pharos-selected-model').textContent = modelLabel();
    find('.pharos-selected-effort').textContent = choices.effort;
    const isDefault = defaultSelection && ['provider','model','effort'].every(key=>defaultSelection[key]===choices[key]);
    const defaultButton = find('.pharos-save-default');
    defaultButton.setAttribute('aria-pressed',String(!!isDefault));
    defaultButton.setAttribute('aria-label',isDefault?'This selection is the default':'Make this selection the default');
    defaultButton.dataset.tooltip = isDefault?'Default CLI, model and effort for new questions':'Save this CLI, model and effort as your default';
    defaultButton.querySelector('span').textContent = isDefault?'\u2605':'\u2606';
    syncComposer();
    if (enabled) autoFit();
    drawEdge();
    renderIdeas();
    sizePicker();
    sizeHistory();
    sizeModal();
  }
  function persist(){writeHistory();}
  async function saveAccessPreference(consent=false){
    try{await api('settings',{enabled:mode==='enabled',showInvitation,consent});accessEnabled=mode==='enabled';window.dispatchEvent(new CustomEvent('pharos:ask-config',{detail:root}));}catch(error){report(error);await refreshStatus();}
  }
  function sizeModal() {
    const dialog=consent.open?consent:null;
    product.classList.toggle('pharos-modal-open',!!dialog);

  }
  function closeModals() {
    for(const dialog of [consent])if(dialog.open)dialog.close();
    sizeModal();
  }
  function openModal(dialog,returnFocus=document.activeElement) {
    closePicker();closeHistory();closeModals();modalReturns.set(dialog,returnFocus);

    dialog.showModal();render();
  }
  function closeModal(dialog,focus=true) {
    if(dialog.open)dialog.close();render();
    const trigger=modalReturns.get(dialog);if(focus&&trigger?.isConnected&&!trigger.hidden)trigger.focus();
  }
  function deferConsent() {
    invitationDismissed=true;closeModal(consent,false);render();persist();find('.home-search-trigger')?.focus();
  }
  function autoFit() {
    question.style.height = 'auto';
    const limit=Math.max(68,Math.round(window.innerHeight*.28));
    question.style.height = Math.min(limit,Math.max(68,question.scrollHeight)) + 'px';
    question.style.overflowY=question.scrollHeight>limit?'auto':'hidden';
    scheduleThreadLayout();
  }
  function syncThreadScroll(){
    const away=threadViewport.scrollHeight-threadViewport.clientHeight-threadViewport.scrollTop>24;
    latestButton.hidden=!away;
    threadWrap.classList.toggle('pharos-earlier',threadViewport.scrollTop>2);
  }
  function scheduleThreadLayout(){
    if(disposed||threadLayoutFrame!==null)return;
    threadLayoutFrame=requestAnimationFrame(()=>{threadLayoutFrame=null;sizeThread();scheduleOverlayLayout();});
  }
  function sizeThread(){
    if(threadWrap.hidden||ai.hidden||!threadViewport.clientWidth||!turns.length)return;
    const lastView=turnViews.get(turns.at(-1).id);lastView?.fitOther?.();
    const last=lastView?.element,previous=turnViews.get(turns.at(-2)?.id)?.get('.pharos-agent-message');
    if(!last)return;
    const content=thread.getBoundingClientRect(),end=last.getBoundingClientRect();
    const padding=thread.scrollHeight-(end.bottom-content.top);
    const gap=previous?end.top-previous.getBoundingClientRect().bottom:0;
    const frame=find('.pharos-ai-frame'),main=root.closest('main');
    const chrome=(overlay.open?composerHeight():frame.getBoundingClientRect().height)-threadViewport.getBoundingClientRect().height;
    // Measure from the composer's position in the unscrolled page so scrolling
    // the page itself never changes the transcript's size.
    const top=frame.getBoundingClientRect().top+(main?.scrollTop||0);
    const outside=overlay.open?overlayPadding()+Math.max(0,root.getBoundingClientRect().height-frame.getBoundingClientRect().height):0;
    const browseHint=find('.pharos-browse-hint');
    const bottomSpace=browseHint.hidden?20:Math.max(20,browseHint.getBoundingClientRect().bottom-frame.getBoundingClientRect().bottom+12);
    const available=overlay.open?Math.max(0,overlayViewport().height-chrome-outside):Math.max(0,window.innerHeight-Math.max(80,top)-chrome-bottomSpace);
    // A first exchange includes the transcript's top padding, too; otherwise
    // following the bottom clips the first message and draws the earlier-content fade.
    const height=askThreadHeight({total:thread.scrollHeight,latest:previous?end.height+padding:thread.scrollHeight,previous:previous?.getBoundingClientRect().height||0,gap,available});
    const scrollTop=threadViewport.scrollTop;
    threadViewport.style.maxHeight=available+'px';
    threadViewport.style.height=height+'px';
    threadViewport.scrollTop=threadFollowing?threadViewport.scrollHeight:scrollTop;
    syncThreadScroll();drawEdge();
  }
  threadViewport.addEventListener('scroll',()=>{
    threadFollowing=threadViewport.scrollHeight-threadViewport.clientHeight-threadViewport.scrollTop<=24;
    syncThreadScroll();
  });
  latestButton.addEventListener('click',()=>{threadFollowing=true;scheduleThreadLayout();});
  const threadObserver=new ResizeObserver(scheduleThreadLayout);
  threadObserver.observe(thread);threadObserver.observe(question);threadObserver.observe(find('.pharos-composer-footer'));
  function syncComposerActions() {
    const actions=askComposerActions({draft:question.value,turns,pending:!!pending,opening:openingSession!==null,enabled:accessEnabled,model:choices.model});
    const send=find('.pharos-send'),label=actions.stop?'Stop':'Send question';
    send.disabled=!(actions.send||actions.stop);send.dataset.action=actions.stop?'stop':'send';
    send.setAttribute('aria-label',label);send.dataset.tooltip=label;
    historyTrigger.hidden=!actions.history;
    find('.pharos-history-anchor').hidden=!actions.history;
    const clearButton=find('.pharos-clear-chat'),showClear=actions.clear&&!ai.hidden,clearChanged=clearButton.hidden===showClear;
    clearButton.hidden=!showClear;
    if(clearChanged){updateSurface();if(!ai.hidden)autoFit();}
    if(!actions.history&&!chatHistory.hidden)closeHistory();
    turns.forEach(syncClarification);
  }
  function syncComposer() {
    const hasThread=turns.length>0;
    thread.hidden=!hasThread;
    threadWrap.hidden=!hasThread;
    ai.classList.toggle('pharos-has-thread',hasThread);
    suggestions.hidden=ai.hidden||hasThread;
    question.disabled=openingSession!==null;
    question.placeholder=openingSession!==null?'Loading conversation…':hasThread?'Ask a follow-up or refine your question...':'What would you like to find in your work?';
    syncComposerActions();
    pickerTrigger.disabled=hasThread||loadingModels||openingSession!==null;
    pickerTrigger.dataset.tooltip=hasThread?'Model selection is fixed for this chat. Clear to start with another model.':'Choose CLI, model, and effort';
    if(hasThread&&!picker.hidden)closePicker();
    // Only the pending reply is busy, so new user messages remain readable.
    ai.removeAttribute('aria-busy');
    scheduleThreadLayout();updateSurface();
  }
  function appendTurn(turn) {
    const element=find('#pharos-turn-template').content.firstElementChild.cloneNode(true);
    element.dataset.turnId=turn.id;
    const get=selector=>element.querySelector(selector);
    get('.pharos-user-question').textContent=turn.prompt;
    const avatar=get('.pharos-agent-avatar');avatar.dataset.provider=turn.cli;
    avatar.append(find('#pharos-lighthouse-template').content.firstElementChild.cloneNode(true));
    const badge=document.createElement('span');badge.className='pharos-agent-badge';
    badge.append(find('#pharos-logo-'+turn.cli.toLowerCase()).content.firstElementChild.cloneNode(true));avatar.append(badge);
    avatar.setAttribute('aria-label','Pharos with '+turn.cli);avatar.removeAttribute('aria-hidden');avatar.setAttribute('role','img');
    const view={element,get,previews:new Map(),hasChartPreview:false};turnViews.set(turn.id,view);thread.append(element);
    get('.pharos-retry').addEventListener('click',()=>startRequest(turn));
    get('.pharos-retry-model').addEventListener('click',retryWithDifferentModel);
    get('.pharos-recovery-search').addEventListener('click',useRegular);
    get('.pharos-recovery-clear').addEventListener('click',()=>clearChat());
    element.querySelectorAll('.pharos-destination').forEach(link=>link.addEventListener('click',event=>{
      if(event.metaKey||event.ctrlKey||event.shiftKey||event.altKey||event.button)return;
      event.preventDefault();stop();if(presentation==='modal')closeOverlay();window.pharosNavigate?.(link.getAttribute('href'));
    }));
    updateTurn(turn);
  }
  function updateTurn(turn) {
    const view=turnViews.get(turn.id);if(!view)return;
    const {element,get}=view,loading=turn.status==='loading';
    element.dataset.status=turn.status;
    get('.pharos-agent-message').setAttribute('aria-busy',String(loading));
    get('.pharos-waiting').hidden=!loading;
    const wait=askWaitStatus(turn);
    get('.pharos-wait-message').textContent=wait.message;
    get('.pharos-elapsed').textContent=wait.elapsed;
    const activity=turn.activity||[],activityKey=JSON.stringify(activity);
    get('.pharos-activity').hidden=!askShowActivity(turn);
    if(view.activityKey!==activityKey){
      view.activityKey=activityKey;
      get('.pharos-activity ol').replaceChildren(...activity.map(event=>{
        const item=document.createElement('li');item.textContent=Math.floor(event.elapsedMs/1000)+'s · '+event.message;return item;
      }));
    }
    const response=answerFor(turn),hasResults=response.cards.length>0&&['multiple','single'].includes(turn.status);
    get('.pharos-destinations').hidden=!hasResults;
    get('.pharos-results-label').hidden=!hasResults||!turn.reply?.trim();get('.pharos-results-label').textContent=turn.reply;
    const omitted=askOmittedNote(hasResults?turn.omitted:0);get('.pharos-omitted').hidden=!omitted;get('.pharos-omitted').textContent=omitted;
    view.hasChartPreview=false;
    element.querySelectorAll('.pharos-destination').forEach((link,index)=>{
      const card=response.cards[index],result=link.parentElement,slot=result.querySelector('.pharos-chart-preview-slot');
      result.hidden=link.hidden=!hasResults||!card;
      const key=hasResults&&card&&chartPreviews?.canRender(card.href)?JSON.stringify([card.href,card.title]):'';
      const previous=view.previews.get(link);
      if(previous?.key!==key){
        previous?.dispose();view.previews.delete(link);
        if(key)view.previews.set(link,{key,dispose:chartPreviews.mount(slot,card)});
      }
      slot.hidden=!key;result.classList.toggle('has-preview',!!key);
      view.hasChartPreview ||= !!key;
      if(!card)return;
      link.dataset.destination=card.title;link.href=card.href;
      link.querySelector('strong').textContent=card.title;
      link.querySelector('span:last-of-type').textContent=card.description;
    });
    get('.pharos-answer').hidden=!turn.reply||hasResults;get('.pharos-answer').textContent=turn.reply;
    get('.pharos-recovery').hidden=!(turn===turns.at(-1)&&['error','canceled'].includes(turn.status));
    scheduleThreadLayout();drawEdge();
    syncClarification(turn);updateSurface();
  }
  function syncClarification(turn){
    const view=turnViews.get(turn.id);if(!view)return;
    const section=view.get('.pharos-clarification'),clarification=turn.clarification;
    section.hidden=!clarification||turn.status!=='clarification';
    if(section.hidden)return;
    const key=JSON.stringify(clarification);
    if(view.clarificationKey!==key){
      view.clarificationKey=key;
      view.get('.pharos-clarification-question').textContent=clarification.question;
      const options=view.get('.pharos-clarification-options');options.replaceChildren();
      view.clarificationButtons=clarification.options.map((text,index)=>{
        const button=document.createElement('button');button.type='button';button.className='pharos-clarification-choice cursor-interaction';
        const letter=document.createElement('span');letter.className='pharos-option-letter';letter.textContent=String.fromCharCode(65+index);
        const label=document.createElement('span');label.textContent=text;button.append(letter,label);
        button.setAttribute('aria-label',letter.textContent+'. '+text);
        button.addEventListener('click',()=>answerClarification(turn,text));options.append(button);return button;
      });
      const other=view.get('.pharos-clarification-other'),input=other.querySelector('textarea'),send=other.querySelector('button'),label=other.querySelector('label');
      label.querySelector('.pharos-option-letter').textContent=String.fromCharCode(65+clarification.options.length);
      input.id='pharos-other-'+turn.id;label.htmlFor=input.id;
      input.value=turn===turns.at(-1)?clarificationDraft:'';
      send.replaceChildren(find('.pharos-send svg').cloneNode(true));
      view.otherInput=input;view.otherSend=send;
      const fit=()=>{if(other.hidden)return;const before=input.style.height;input.style.height='auto';input.style.height=Math.min(110,Math.max(22,input.scrollHeight))+'px';if(before!==input.style.height)scheduleThreadLayout();};
      view.fitOther=fit;
      input.addEventListener('input',()=>{clarificationDraft=input.value;fit();saveChat();});
      input.addEventListener('keydown',event=>{if(event.key==='Enter'&&!event.shiftKey&&!event.isComposing){event.preventDefault();answerClarification(turn,input.value);}});
      send.addEventListener('click',()=>answerClarification(turn,input.value));
      fit();
    }
    const latest=turn===turns.at(-1);
    const actions=askClarificationActions({latest,status:turn.status,pending:!!pending,enabled:accessEnabled,opening:openingSession!==null,draft:view.otherInput.value});
    const following=turns[turns.indexOf(turn)+1];
    view.clarificationButtons.forEach((button,index)=>{
      button.disabled=!actions.active;button.setAttribute('aria-pressed',String(following?.prompt===clarification.options[index]));
    });
    view.get('.pharos-clarification-other').hidden=!latest;
    view.otherInput.disabled=!actions.active;view.otherSend.disabled=!actions.sendOther;
  }
  function answerClarification(turn,text){
    const actions=askClarificationActions({latest:turn===turns.at(-1),status:turn.status,pending:!!pending,enabled:accessEnabled,opening:openingSession!==null,draft:text});
    if(actions.sendOther){submitQuestion(text,true);question.focus({preventScroll:true});}
  }
  function disposePreviews(){turnViews.forEach(view=>view.previews.forEach(preview=>preview.dispose()));}
  function renderThread(){disposePreviews();thread.replaceChildren();turnViews.clear();turns.forEach(appendTurn);syncComposer();}
  function answerFor(turn){return {intro:'',cards:turn.cards||[]};}
  function drawEdge(){composerGlow.draw();}
  function drawIdeas() {
    suggestions.querySelectorAll('.pharos-suggestion').forEach((button,index) => {
      if(button.inert)return;
      const {width:w,height:h}=button.getBoundingClientRect();
      if (!w || !h) return;
      const art = button.querySelector('svg');
      art.setAttribute('viewBox',[-6,-6,w+12,h+12].join(' '));
      const id=Number(button.dataset.idea),corners=ideaCorners[id],styles=ideaRibbons[id];
      const orient=(kind,x,y)=>[kind===1||kind===2?w-x:x,kind===2||kind===3?h-y:y];
      // The shared fill, border and strand mask use the same contour at every corner.
      // Every join has a horizontal or vertical tangent, including both ends of the arc.
      const corner=(kind,offset=0)=>{
        const p=(x,y)=>orient(kind,x,y).join(' ');
        if(!corners.includes(kind))return kind===1||kind===3?{start:p(1,12),path:`Q ${p(1,1)} ${p(12,1)}`}:{start:p(12,1),path:`Q ${p(1,1)} ${p(1,12)}`};
        const style=styles[corners.indexOf(kind)],arc=.5522847498;
        const cornerX=Math.min(20+style.roundness*10,w*.22),cornerY=Math.min(18+(1-style.roundness)*10,h*.28);
        const reachX=Math.min(w*.46,cornerX+24+style.reach*20),reachY=Math.min(h*.46,cornerY+16+style.reach*12);
        const topY=-1.1-style.depth*1.2-offset,edgeX=-.8-style.depth*.9-offset;
        const rx=cornerX-edgeX,ry=cornerY-topY;
        const points=[
          [reachX,1], [reachX-(reachX-cornerX)*.4,1], [cornerX+(reachX-cornerX)*.4,topY], [cornerX,topY],
          [cornerX-arc*rx,topY], [edgeX,cornerY-arc*ry], [edgeX,cornerY],
          [edgeX,cornerY+(reachY-cornerY)*.4], [1,reachY-(reachY-cornerY)*.4], [1,reachY]
        ];
        if(kind===1||kind===3)points.reverse();
        return {start:p(...points[0]),path:[1,4,7].map(i=>'C '+points.slice(i,i+3).map(point=>p(...point)).join(' ')).join(' '),reachX,reachY};
      };
      const surface=[0,3,2,1].map((kind,index)=>{const part=corner(kind);return `${index?'L':'M'} ${part.start} ${part.path}`;}).join(' ')+' Z';
      const key='pharos-idea-'+index,regions=askWrapRegions(art,key,w,h,12,surface);
      const fill=art.querySelector('.pharos-idea-surface');fill.setAttribute('d',surface);fill.setAttribute('clip-path',regions.boxClip);
      const border=art.querySelector('.pharos-idea-border');border.setAttribute('d',regions.box);border.setAttribute('clip-path',regions.contourClip);
      const ribbons=art.querySelector(':scope > g');ribbons.replaceChildren();ribbons.setAttribute('mask',regions.outsideMask);
      const defs=art.querySelector('defs');defs.querySelectorAll('[data-ribbon-paint]').forEach(element=>element.remove());
      const add=(tag,attrs,parent)=>{const element=document.createElementNS('http://www.w3.org/2000/svg',tag);Object.entries(attrs).forEach(([name,value])=>element.setAttribute(name,value));parent.append(element);return element;};
      corners.forEach((kind,ribbon)=>{
        const {reachX,reachY}=corner(kind),style=styles[ribbon],angle=style.phase*Math.PI/180;
        const cx=reachX/2,cy=reachY/2,radius=Math.hypot(reachX,reachY)/2;
        const gradient=(name,start,end)=>add('linearGradient',{id:key+'-'+name+'-'+kind,'data-ribbon-paint':'',gradientUnits:'userSpaceOnUse',x1:start[0],y1:start[1],x2:end[0],y2:end[1]},defs);
        // Color direction varies independently of the taper, so every ribbon fades at its tips.
        const paint=gradient('color',orient(kind,cx-radius*Math.cos(angle),cy-radius*Math.sin(angle)),orient(kind,cx+radius*Math.cos(angle),cy+radius*Math.sin(angle)));
        [['0','var(--pharos-aqua)'],['.48','var(--pharos-violet)'],['1','var(--pharos-gold)']].forEach(([offset,color])=>add('stop',{offset,'stop-color':color},paint));
        const fade=gradient('fade',orient(kind,reachX,1),orient(kind,1,reachY));
        [[0,0],[.2,1],[.8,1],[1,0]].forEach(([offset,opacity])=>add('stop',{offset,'stop-color':'white','stop-opacity':opacity},fade));
        const taperId=key+'-taper-'+kind;
        const taper=add('mask',{id:taperId,'data-ribbon-paint':'',maskUnits:'userSpaceOnUse',x:-6,y:-6,width:w+12,height:h+12},defs);
        add('rect',{x:-6,y:-6,width:w+12,height:h+12,fill:'url(#'+fade.id+')'},taper);
        const group=add('g',{class:'pharos-idea-ribbon','data-corner':kind,mask:'url(#'+taperId+')'},ribbons);
        for(let strand=0;strand<3;strand++){
          const part=corner(kind,strand*1.05);
          add('path',{d:`M ${part.start} ${part.path}`,stroke:'url(#'+paint.id+')'},group);
        }
      });
    });
  }
  const wrapIdea=index=>((index%ideas.length)+ideas.length)%ideas.length;
  const readingIdeas=()=>{const center=ideaStride?wrapIdea(Math.round(ideaViewport.scrollLeft/ideaStride)):ideaCenter;return Array.from({length:ideaCount},(_,index)=>ideaOrder[wrapIdea(center-Math.floor(ideaCount/2)+index)]);};
  function updateIdeaText(){
    ideas=askPromptSuggestions({repositories:ideaRepositories,variation:ideaVariation});
    for(const button of ideaRow.children)button.querySelector('.pharos-idea-text').textContent=ideas[Number(button.dataset.idea)];
    markReading();drawIdeas();
  }
  async function loadIdeaContext(){
    if(ideaContextRequested||!accessEnabled||ideasHidden)return;
    ideaContextRequested=true;
    try{
      const response=await fetch('/api/query/library/distinct?field=repository_name&limit=12',{signal:ideaContextController.signal});
      if(!response.ok)return;
      const result=await response.json();if(disposed)return;
      ideaRepositories=Array.isArray(result.values)?result.values:[];updateIdeaText();
    }catch{/* Suggestions remain useful without optional repository metadata. */}
  }
  function syncIdeaVisibility(){
    ideaToggle.textContent=ideasHidden?'Show suggestions':'Hide suggestions';
    ideaToggle.setAttribute('aria-expanded',String(!ideasHidden));
    ideaViewport.hidden=ideaActions.hidden=ideasHidden;
    if(ideasHidden&&ideaMotion){ideaMotion.cancel();ideaMotion=null;settleIdeas();}
  }
  ideaToggle.addEventListener('click',()=>{
    ideasHidden=!ideasHidden;window.pharosPrefs?.set(ideaHiddenKey,ideasHidden);
    ideaElapsed=0;ideaLastTick=Date.now();ideaSize='';syncIdeaVisibility();renderIdeas();sizeModal();
  });
  syncIdeaVisibility();
  function buildIdeas(){
    if(ideaRow.children.length)return;
    for(let slot=0;slot<ideas.length*3;slot++){
      const id=ideaOrder[slot%ideas.length],button=find('#pharos-suggestion-template').content.firstElementChild.cloneNode(true);
      button.dataset.idea=id;button.dataset.slot=slot;button.dataset.corners=ideaCorners[id].join(',');
      button.querySelector('.pharos-idea-text').textContent=ideas[id];
      button.addEventListener('click',()=>{
        if(button.hasAttribute('data-xray')){moveIdeas(Number(button.dataset.slot)-(ideas.length+ideaCenter),true);return;}
        stop();closePicker();question.value=ideas[id];autoFit();drawEdge();saveChat();question.focus();const end=question.value.length+promptEditor.chips().length;question.setSelectionRange(end,end);
      });
      ideaRow.append(button);
    }
  }
  function ideaHeld(){return frozenAt!==null||ideaPaused||suggestions.hidden||ideasHidden||document.hidden||pending||question.value.trim()||!picker.hidden||!chatHistory.hidden||consent.open||ideaMoving||ideaViewport.matches(':hover,:focus-within')||matchMedia('(prefers-reduced-motion: reduce)').matches;}
  function updateIdeaClock(){
    const held=ideaHeld();find('.pharos-idea-countdown-label').textContent=held?(ideaPaused||frozenAt!==null||matchMedia('(prefers-reduced-motion: reduce)').matches?'Paused':'Waiting'):'More in '+Math.ceil((ideaCycleMs-ideaElapsed)/1000)+'s';
    find('.pharos-idea-countdown-track i').style.transform=`scaleX(${Math.max(0,1-ideaElapsed/ideaCycleMs)})`;
    const play=find('.pharos-idea-play');play.textContent=ideaPaused?'\u25b6 Play':'\u2161 Pause';play.setAttribute('aria-label',ideaPaused?'Play suggestions':'Pause suggestions');play.setAttribute('aria-pressed',String(ideaPaused));
  }
  function markReading(){
    if(ideasHidden||suggestions.hidden||!ideaViewport.clientWidth)return;
    const at=ideaStride?Math.round(ideaViewport.scrollLeft/ideaStride):ideas.length+ideaCenter;
    const first=at-Math.floor(ideaCount/2),last=at+Math.floor(ideaCount/2);
    let height=54,repaint=false;
    [...ideaRow.children].forEach((button,slot)=>{
      const reading=slot>=first&&slot<=last;
      // Offscreen questions must not set the height of the entire repertoire.
      if(reading)height=Math.max(height,Math.ceil(button.querySelector('.pharos-idea-text').getBoundingClientRect().height)+16);
      button.toggleAttribute('data-reading',reading);button.toggleAttribute('data-xray',!reading);
      // Only this reading area and the two visible peeks participate in keyboard navigation.
      const inert=slot<first-1||slot>last+1;
      if(button.inert&&!inert)repaint=true;
      button.inert=inert;
      button.setAttribute('aria-label',reading?ideas[Number(button.dataset.idea)]:'Bring '+ideas[Number(button.dataset.idea)]+' into view');
    });
    const size=height+'px';
    if(suggestions.style.getPropertyValue('--pharos-idea-height')!==size){suggestions.style.setProperty('--pharos-idea-height',size);repaint=true;}
    if(repaint)drawIdeas();
  }
  function settleIdeas(){
    if(!ideaStride)return;
    clearTimeout(ideaScrollEnd);ideaScrollEnd=null;
    const at=Math.round(ideaViewport.scrollLeft/ideaStride);ideaCenter=wrapIdea(at);ideaPage=Math.floor(ideaCenter/3);
    ideaViewport.scrollLeft=(ideas.length+ideaCenter)*ideaStride;
    ideaMoving=false;ideaMotion=null;ideaViewport.removeAttribute('data-moving');ideaViewport.style.scrollSnapType='';markReading();updateIdeaClock();
  }
  function renderIdeas() {
    if(suggestions.hidden||ideasHidden)return;
    void loadIdeaContext();
    buildIdeas();const width=ideaViewport.clientWidth;if(!width)return;
    ideaCount=width>=600?3:1;
    const cardWidth=(width-2*(ideaCount===3?44:28)-(ideaCount-1)*14)/ideaCount;
    const size=width+':'+cardWidth;
    if(size!==ideaSize){
      if(ideaMotion){ideaMotion.cancel();ideaMotion=null;}
      ideaSize=size;ideaMoving=false;ideaViewport.removeAttribute('data-moving');ideaViewport.style.scrollSnapType='';ideaStride=cardWidth+14;
      suggestions.style.setProperty('--pharos-idea-width',cardWidth+'px');suggestions.style.setProperty('--pharos-idea-side',(width-cardWidth)/2+'px');
      ideaViewport.scrollLeft=(ideas.length+ideaCenter)*ideaStride;
    }
    markReading();drawIdeas();updateIdeaClock();
  }
  function runIdeaMotion(){
    const motion=ideaMotion;if(!motion||frozenAt!==null)return;
    motion.started=performance.now();
    const step=()=>{
      if(ideaMotion!==motion||frozenAt!==null)return;
      const elapsed=motion.elapsedMs+performance.now()-motion.started,t=Math.min(1,elapsed/motion.duration),ease=1-Math.pow(1-t,3);
      ideaViewport.scrollLeft=(motion.from+(motion.to-motion.from)*ease)*ideaStride;markReading();
      if(t<1)motion.frame=requestAnimationFrame(step);else settleIdeas();
    };
    motion.frame=requestAnimationFrame(step);
  }
  function pauseIdeaMotion(){
    if(!ideaMotion)return;
    if(frozenAt===null)ideaMotion.elapsedMs=Math.min(ideaMotion.duration,ideaMotion.elapsedMs+performance.now()-ideaMotion.started);
    cancelAnimationFrame(ideaMotion.frame);
  }
  function moveIdeas(offset,user=false){
    if(!ideaStride||!offset)return;
    if(ideaMotion){ideaMotion.cancel();ideaMotion=null;}
    clearTimeout(ideaScrollEnd);ideaScrollEnd=null;
    const from=ideaViewport.scrollLeft,to=from+offset*ideaStride;
    ideaElapsed=0;ideaLastTick=Date.now();
    if(frozenAt!==null||matchMedia('(prefers-reduced-motion: reduce)').matches){ideaViewport.scrollLeft=to;settleIdeas();return;}
    // Match the catalog's slower timed glide and quicker user-requested movement.
    const duration=user?1020:2040;ideaMoving=true;ideaViewport.setAttribute('data-moving','');
    ideaMotion={from:from/ideaStride,to:to/ideaStride,duration,elapsedMs:0,frame:null,started:performance.now(),cancel(){cancelAnimationFrame(this.frame);ideaMoving=false;}};
    ideaViewport.style.scrollSnapType='none';runIdeaMotion();updateIdeaClock();
    if(user)find('.pharos-idea-announcement').textContent='Showing more prompt suggestions.';
  }
  function rotateIdeas() {moveIdeas(ideaCount,false);}
  function advanceIdeas(now=Date.now()) {
    const delta = Math.max(0,now-ideaLastTick);ideaLastTick=now;
    if(!ideaHeld()){ideaElapsed+=delta;if(ideaElapsed>=ideaCycleMs)rotateIdeas();}
    updateIdeaClock();
  }
  const ideaTimer=setInterval(advanceIdeas,250);
  find('.pharos-more-ideas').addEventListener('click',()=>moveIdeas(Math.ceil(ideaCount/2),true));
  find('.pharos-idea-previous').addEventListener('click',()=>moveIdeas(-Math.ceil(ideaCount/2),true));
  find('.pharos-idea-play').addEventListener('click',()=>{advanceIdeas();ideaPaused=!ideaPaused;ideaLastTick=Date.now();updateIdeaClock();});
  find('.pharos-idea-refresh').addEventListener('click',()=>{
    if(ideaMotion){ideaMotion.cancel();ideaMotion=null;}clearTimeout(ideaScrollEnd);ideaScrollEnd=null;ideaMoving=false;ideaViewport.removeAttribute('data-moving');
    ideaVariation++;ideas=askPromptSuggestions({repositories:ideaRepositories,variation:ideaVariation});
    // Move by one page without clumping related subjects together.
    ideaOrder=[...ideaOrder.slice(3),...ideaOrder.slice(0,3)];ideaCenter=1;ideaPage=0;ideaElapsed=0;ideaLastTick=Date.now();ideaSize='';ideaRow.replaceChildren();renderIdeas();
    find('.pharos-idea-announcement').textContent='Prompt suggestions refreshed.';
  });
  ideaViewport.addEventListener('keydown',event=>{if(['ArrowLeft','ArrowRight'].includes(event.key)&&!event.altKey&&!event.metaKey&&!event.ctrlKey&&!event.shiftKey){event.preventDefault();moveIdeas((event.key==='ArrowLeft'?-1:1)*Math.ceil(ideaCount/2),true);}});
  ideaViewport.addEventListener('scroll',()=>{
    markReading();if(ideaMotion||frozenAt!==null)return;
    ideaMoving=true;clearTimeout(ideaScrollEnd);ideaScrollEnd=setTimeout(settleIdeas,150);
  },{passive:true});
  function interruptIdeaSlide(){if(ideaMotion){ideaMotion.cancel();ideaMotion=null;ideaViewport.removeAttribute('data-moving');ideaViewport.style.scrollSnapType='';settleIdeas();}ideaElapsed=0;ideaLastTick=Date.now();}
  ideaViewport.addEventListener('pointerdown',interruptIdeaSlide);
  ideaViewport.addEventListener('wheel',interruptIdeaSlide,{passive:true});
  const ideaObserver=new ResizeObserver(renderIdeas);ideaObserver.observe(ideaViewport);
  function sizePicker() {
    if (picker.hidden) return;
    const anchor=pickerTrigger.getBoundingClientRect(),gap=8;
    const above=Math.max(0,Math.min(innerHeight,anchor.top)-gap*2),below=Math.max(0,innerHeight-Math.max(0,anchor.bottom)-gap*2);
    ai.style.setProperty('--pharos-picker-width',Math.max(0,Math.min(find('.pharos-ai-inner').clientWidth-32,innerWidth-gap*2))+'px');
    picker.style.maxHeight=Math.max(above,below)+'px';
    const bounds=picker.getBoundingClientRect();
    const top=bounds.height<=below||below>=above?anchor.bottom+gap:anchor.top-gap-bounds.height;
    picker.style.left=Math.max(gap,Math.min(anchor.left,innerWidth-gap-bounds.width))+'px';
    picker.style.top=Math.max(gap,Math.min(top,innerHeight-gap-bounds.height))+'px';
  }
  function closePicker(focus = false) {
    picker.hidePopover();
    picker.hidden = true;
    pickerTrigger.setAttribute('aria-expanded','false');
    if (focus) pickerTrigger.focus();
  }
  function showPickerPage(page,focus = true) {
    if(turns.length)return;
    pickerPage = page;
    find('.pharos-picker-root').hidden = !!page;
    find('.pharos-picker-submenu').hidden = !page;
    if (page) {
      const labels = {provider:'CLI',model:'Model',effort:'Effort'};
      find('.pharos-picker-back span').textContent = labels[page];
      const values = page === 'provider' ? providers.map(p=>p.id) : page === 'model' ? Object.keys(providerModels[choices.provider]||{}) : effortChoices();
      const options = find('.pharos-picker-options');
      options.replaceChildren();
      values.forEach(value => {
        const button = document.createElement('button');
        button.type = 'button';button.className = 'pharos-picker-row pharos-picker-choice cursor-interaction';
        button.setAttribute('aria-pressed',String(choices[page] === value));
        const label = document.createElement('span');label.textContent = page === 'model' ? providerModels[choices.provider][value] : value;
        const check = find('#pharos-check-icon').content.firstElementChild.cloneNode(true);
        button.append(label,check);
        button.addEventListener('click',async() => {
          if(turns.length)return;
          try{if(page==='provider'){button.disabled=true;label.textContent='Loading '+value+' models…';await loadModels(value);}}catch(error){button.disabled=false;label.textContent=value;report(error);return;}
          if(disposed||turns.length||openingSession!==null)return;
          stop(); choices[page] = value;
          if (page === 'provider') Object.assign(choices,defaultFor(value));
          if(page==='model'&&!effortChoices().includes(choices.effort))choices.effort='Default';
          render();saveChat();showPickerPage(null,false);
          find('[data-picker="' + page + '"]').focus();
        });
        options.append(button);
      });
    }
    sizePicker();
    if (focus) (page ? find('.pharos-picker-back') : find('[data-picker]')).focus();
  }
  function stop(cancel = true) {
    const turn=pending&&turns.find(turn=>turn.id===pending.turnId);
    if(turn)turn.elapsedMs=Math.max(0,(frozenAt??Date.now())-pending.started);
    if(pending&&cancel){pending.controller?.abort();cancelQueue=api('cancel',{id:activeSessionId}).catch(report);}
    serial++; pending = null;
    clearInterval(ticker);clearTimeout(pollTimer);pollTimer=null;ticker = null;
    if(turn&&cancel){turn.status='canceled';turn.reply='Request canceled. Your question is still in this conversation.';updateTurn(turn);}
    syncComposer();
    if(turn)saveChat(false);
  }
  function changeMode(next) {
    stop();closePicker();closeHistory();closeModals(); mode = next; regularOverride = false;
    render(); persist();
  }
  find('.pharos-try').addEventListener('click', () => openModal(consent));
  find('.pharos-dismiss').addEventListener('click', () => {invitationDismissed=true;render();persist();find('.home-search-trigger')?.focus();});
  find('.pharos-cancel').addEventListener('click', deferConsent);
  consent.addEventListener('cancel',event=>{event.preventDefault();deferConsent();});
  find('.pharos-never').addEventListener('click',()=>{showInvitation=false;invitationDismissed=false;changeMode('declined');saveAccessPreference();find('.home-search-trigger')?.focus();});
  find('.pharos-enable').addEventListener('click',async()=>{
    const button=find('.pharos-enable');button.disabled=true;
    try{
      await api('settings',{enabled:true,showInvitation,consent:true});accessEnabled=true;
      const provider=connectProvider.value;await loadModels(provider);
      if(!turns.length)Object.assign(choices,defaultSelection?.provider===provider?defaultSelection:defaultFor(provider));
      invitationDismissed=false;changeMode('enabled');visitChanged();window.dispatchEvent(new CustomEvent('pharos:ask-config',{detail:root}));await loadChatHistory();question.focus();
    }catch(error){report(error);}finally{button.disabled=false;}
  });

  function useRegular() {stop();closePicker();closeHistory(); regularOverride = true;render(); find('.home-search-trigger')?.focus();}
  function retryWithDifferentModel() {
    const draft=askRetryDraft({turns,draft:question.value});
    clearChat();question.value=draft;autoFit();saveChat();render();openPicker();
  }
  find('.pharos-search-mode').addEventListener('click', useRegular);
  find('.pharos-ai-mode').addEventListener('click', () => {regularOverride = false; stop(); render(); find('#pharos-question').focus();});
  function openPicker() {
    if(pickerTrigger.disabled)return;
    closeHistory();
    picker.hidden = false;picker.showPopover();pickerTrigger.setAttribute('aria-expanded','true');showPickerPage(null);
  }
  pickerTrigger.addEventListener('click',() => {
    if (!picker.hidden) {closePicker();return;}
    openPicker();
  });
  find('.pharos-save-default').addEventListener('click',async()=>{try{await api('settings',{selection:copySelection(choices)});defaultSelection=copySelection(choices);render();window.dispatchEvent(new CustomEvent('pharos:ask-config',{detail:root}));}catch(error){report(error);}});
  product.querySelectorAll('[data-picker]').forEach(button => button.addEventListener('click',() => showPickerPage(button.dataset.picker)));
  find('.pharos-picker-back').addEventListener('click',() => {const previous = pickerPage;showPickerPage(null,false);find('[data-picker="' + previous + '"]').focus();});
  listen(document,'pointerdown',event => {if(!find('.pharos-picker-anchor').contains(event.target))closePicker();});
  // WebKit can focus the containing dialog when a picker button is clicked.
  // That ancestor focus is still an interaction within this picker.
  find('.pharos-picker-anchor').addEventListener('focusout',event => {
    const anchor=find('.pharos-picker-anchor'),target=event.relatedTarget;
    if(target&&!anchor.contains(target)&&!target.contains(anchor))closePicker();
  });
  product.addEventListener('keydown',event => {if(event.key === 'Escape' && !picker.hidden){event.preventDefault();closePicker(true);}});
  historyTrigger.addEventListener('click',()=>{if(chatHistory.hidden)openHistory();else closeHistory();});
  find('.pharos-history-close').addEventListener('click',()=>closeHistory(true));
  find('.pharos-clear-chat').addEventListener('click',()=>clearChat());
  listen(document,'pointerdown',event=>{if(!find('.pharos-history-anchor').contains(event.target))closeHistory();});
  find('.pharos-history-anchor').addEventListener('focusout',event=>{if(event.relatedTarget&&!find('.pharos-history-anchor').contains(event.relatedTarget))closeHistory();});
  product.addEventListener('keydown',event=>{if(event.key==='Escape'&&!chatHistory.hidden){event.preventDefault();closeHistory(true);}});
  listen(window,'resize',() => {if(!ai.hidden)autoFit();drawEdge();renderIdeas();sizePicker();sizeHistory();sizeModal();});
  listen(document,'scroll',sizePicker,true);
  let edgeLayoutFrame=null;
  const edgeObserver=new ResizeObserver(()=>{
    if(disposed||edgeLayoutFrame!==null)return;
    edgeLayoutFrame=requestAnimationFrame(()=>{edgeLayoutFrame=null;if(!disposed&&product.getBoundingClientRect().width&&!ai.hidden){autoFit();drawEdge();sizePicker();}});
  });edgeObserver.observe(find('.pharos-ai-frame'));
  async function startRequest(turn,retry=true){
    stop();flushDraft();const request=++serial;
    announcement.textContent='';
    turn.status='loading';turn.reply='';turn.cards=[];delete turn.omitted;delete turn.clarification;turn.elapsedMs=0;turn.activity=[];
    turn.message='Starting '+turn.cli+'…';
    const controller=new AbortController();pending={request,turnId:turn.id,started:Date.now(),controller,edited:false,browsing:presentation==='floating'||presentation==='dismissed'};
    turns.forEach(updateTurn);syncComposer();writeHistory();
    ticker=setInterval(()=>tickRequest(request),1000);
    try {
      await writeQueue;await cancelQueue;
      const session=await api('send',{id:activeSessionId,turnID:turn.id,prompt:turn.prompt,draft:question.value,selection:copySelection(choices),retry,currentURI:turn.currentURI,timezone:Intl.DateTimeFormat().resolvedOptions().timeZone},controller.signal,activity=>{
        if(pending?.request!==request)return;
        if(!turn.activity.length){pending.started=Date.now()-activity.elapsedMs;turn.startedAt=pending.started;}
        turn.activity=[...(turn.activity||[]),activity].slice(-40);turn.elapsedMs=Date.now()-pending.started;updateTurn(turn);
      });
      if(pending?.request!==request)return;
      activeCreatedAt=session.createdAt;activeUpdatedAt=session.updatedAt;const edited=pending.edited,browsing=pending.browsing;const final=session.conversation.find(t=>t.id===turn.id);Object.assign(turn,final);stop(false);updateTurn(turn);syncComposer();sessions=sessionRecords();
      window.dispatchEvent(new Event('pharos:data-refresh'));
      if(askShouldNavigate({hasPreview:turnViews.get(turn.id)?.hasChartPreview,cards:turn.cards,omitted:turn.omitted,draft:question.value,enabled:mode==='enabled',regular:regularOverride,edited,browsing})){if(presentation==='modal')closeOverlay();window.pharosNavigate?.(turn.cards[0].href);}else notifyCompletion(turn);
    }catch(error){
      if(pending?.request!==request)return;
      // A lost response is not permission to repeat a billable request.
      try{const saved=await api('session?id='+encodeURIComponent(activeSessionId));if(pending?.request!==request)return;const actual=saved.conversation.find(t=>t.id===turn.id);if(actual){Object.assign(turn,actual);stop(false);updateTurn(turn);syncComposer();if(turn.status==='loading')watchRequest(turn);else notifyCompletion(turn);return;}}catch{}
      if(pending?.request!==request)return;stop(false);turn.status='error';turn.reply=error.message||String(error);updateTurn(turn);syncComposer();notifyCompletion(turn);
    }
  }
  function watchRequest(turn){
    const request=++serial,controller=new AbortController();
    pending={request,turnId:turn.id,started:turn.startedAt||Date.now(),controller,edited:true};syncComposer();ticker=setInterval(()=>tickRequest(request),1000);
    const poll=async()=>{try{const saved=await api('session?id='+encodeURIComponent(activeSessionId),undefined,controller.signal);if(pending?.request!==request)return;const actual=saved.conversation.find(t=>t.id===turn.id);if(actual){Object.assign(turn,actual);turn.elapsedMs=Date.now()-pending.started;updateTurn(turn);if(actual.status!=='loading'){activeUpdatedAt=saved.updatedAt;stop(false);syncComposer();sessions=sessionRecords();notifyCompletion(turn);return;}}}catch(error){if(pending?.request!==request)return;stop(false);turn.status='error';turn.reply='Could not reconnect to this question. Reopen the chat to check its result.';updateTurn(turn);syncComposer();notifyCompletion(turn);return;}if(pending?.request===request)pollTimer=setTimeout(poll,800);};
    void poll();
  }
  function tickRequest(request){if(pending?.request!==request)return;const turn=turns.find(t=>t.id===pending.turnId);if(!turn)return;turn.elapsedMs=Date.now()-pending.started;updateTurn(turn);}
  function submitQuestion(value,preserveDraft=false){
    if(pending||openingSession!==null||!accessEnabled||!value.trim()||!choices.model)return;
    const prompt=value.trim();closePicker();closeHistory();
    if(!activeSessionId){sessionLoad++;activeSessionId=crypto.randomUUID();activeCreatedAt=Date.now();}
    syncChatURL(activeSessionId);threadFollowing=true;
    const turn={id:crypto.randomUUID(),prompt,currentURI:presentation==='modal'?contextURI:askPageURI(location.href),cli:choices.provider,model:choices.model,modelLabel:modelLabel(),effort:choices.effort,status:'loading',message:'',reply:'',cards:[],elapsedMs:0};
    turns.push(turn);appendTurn(turn);if(!preserveDraft)question.value='';clarificationDraft='';autoFit();startRequest(turn,false);
  }
  function ask(){submitQuestion(question.value);}
  find('.pharos-send').addEventListener('click',()=>{if(pending)stop();else ask();});
  ai.addEventListener('submit', event => {event.preventDefault(); ask();});
  find('#pharos-question').addEventListener('keydown', event => {if(event.target===question&&event.key === 'Enter' && !event.shiftKey && !event.isComposing){event.preventDefault();if(!pending)ask();}});
  question.addEventListener('input', () => {contextURI=promptEditor.chips().find(chip=>chip.id==='page')?.value||'';if(pending)pending.edited=true;autoFit();saveChat();});
  function syncTheme(){appearance=document.documentElement.dataset.theme==='dark'?'dark':'light';drawEdge();drawIdeas();}
  const themeObserver=new MutationObserver(syncTheme);themeObserver.observe(document.documentElement,{attributes:true,attributeFilter:['data-theme','style']});
  const configChanged=event=>{if(event.detail!==root)void refreshStatus().then(visitChanged).catch(report);};listen(window,'pharos:ask-config',configChanged);
  const visitChanged=event=>{
    if(!accessEnabled)return;
    const entry=event?.detail?.entry;
    let visits;
    try{
      visits=entry?[entry]:JSON.parse(localStorage.getItem('pharos-history-v1')||'[]').map(entry=>({...entry,title:window.historyTitle?.(entry.url,entry.page)||entry.title}));
    }catch{return;}
    visits=visits.map(entry=>({...entry,title:String(entry.title||'Visited page').slice(0,500)}));
    const input={visits,replaces:event?.detail?.replaced||null,reconcile:!entry};
    void askWriteVisit(()=>api('visits',input)).catch(report);
  };
  listen(window,'pharos:visit',visitChanged);
  const routeChanged=()=>{surfaceRestorePending=false;sessionLoad++;openingSession=null;flushDraft();if(presentation==='modal')closeOverlay();restoreHome();if(presentation!=='home')return;if(!['/','/home'].includes(location.pathname)){if(pending)browse();}else if(accessEnabled){const id=new URL(location.href).searchParams.get('ask_chat');if(id&&id!==activeSessionId)void openSession(id);else if(!id&&activeSessionId)clearChat(false);else{if(id)regularOverride=false;render();}}};
  listen(window,'pharos:route',routeChanged);
  listen(window,'pharos:view',()=>{restoreHome();if(presentation==='home'&&pending&&!['/','/home'].includes(location.pathname))browse();});
  listen(window,'pagehide',()=>{
    // A second reload during startup must not replace the state still loading.
    if(!surfaceRestorePending)try{sessionStorage.setItem(surfaceKey,JSON.stringify({page:askPageURI(location.href),id:activeSessionId||sessionStorage.getItem(activeKey),presentation,contextURI,floatPosition,prompt:promptEditor.document()}));}catch{}
    if(draftPending){const payload=draftPending;draftPending=null;void fetch('/api/ask/session',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload),keepalive:true});}
  });
  render();syncTheme();void refreshStatus().then(visitChanged).catch(report);
  return ()=>{disposePreviews();if(overlay.open)overlay.close();mountHost.append(root);overlay.remove();floating.remove();announcement.remove();if(headerButton)headerButton.disabled=true;disposed=true;ideaContextController.abort();sessionLoad++;stop();glows.forEach(glow=>glow.destroy());clearTimeout(draftTimer);cancelAnimationFrame(threadLayoutFrame);cancelAnimationFrame(overlayLayoutFrame);cancelAnimationFrame(edgeLayoutFrame);overlayObserver.disconnect();threadObserver.disconnect();clearInterval(ideaTimer);clearTimeout(ideaScrollEnd);ideaMotion?.cancel();edgeObserver.disconnect();ideaObserver.disconnect();clearTimeout(historyTimer);themeObserver.disconnect();cleanup.forEach(fn=>fn());window.removeEventListener('pharos:ask-config',configChanged);window.removeEventListener('pharos:visit',visitChanged);window.removeEventListener('pharos:route',routeChanged);};
};
