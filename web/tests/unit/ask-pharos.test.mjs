import { EMPTY_QUERY, encodeQuery } from '@pythia-software/query-table-core';
import { askChartPreviewJob, onAskChartRefresh } from '../../src/ask-chart-model';
import { askWorkPreviewAddress, workPreviewEndpoint, workPreviewView } from '../../src/ask-work-model';
import { combineAskPreviews } from '../../src/ask-link-previews';
import { dashboardDefaults } from '../../src/dashboard-defaults';
import { usageTopicDatasets } from '../../src/usage-workspace-model';
import { schemas } from './helpers/recipes.mjs';
import { fixture } from './helpers/parity.mjs';
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { loadClassicScript, plain } from './helpers/classic-script.mjs';
const model = loadClassicScript('ask-model.js',{TextEncoder,TextDecoder});
const prompt = loadClassicScript('ask-prompt.js');

test('Ask dialog starts a quarter down the window and grows around its original center',()=>{
  const layout=options=>plain(model.askDialogLayout({emptyHeight:194,...options}));
  const empty=layout({viewportHeight:1070,contentHeight:194});
  assert.deepEqual(empty,{top:1070/4,height:194});
  const medium=layout({viewportHeight:1070,contentHeight:600});
  assert.equal(medium.top+medium.height/2,empty.top+empty.height/2,'growth shares space above and below the original composer');
  assert.ok(medium.top<empty.top&&medium.top+medium.height>empty.top+empty.height);
  assert.deepEqual(layout({viewportHeight:1070,contentHeight:800}),{top:0,height:800},'after reaching the top, growth continues down the page');
  assert.deepEqual(layout({viewportHeight:1070,contentHeight:1500}),{top:0,height:1070});
  assert.deepEqual(layout({viewportHeight:300,contentHeight:194}),{top:75,height:194});
  assert.deepEqual(layout({viewportHeight:250,contentHeight:194}),{top:56,height:194},'short windows keep the empty composer on screen');
  for(const viewportHeight of [250,400,650,1070])for(const contentHeight of [0,194,350,700,2000]){
    const result=layout({viewportHeight,contentHeight});
    assert.ok(result.top>=0&&result.top+result.height<=viewportHeight,'growing content must fit the available viewport');
  }
});

test('inline prompt chips move among text and other chips without changing the question or context',()=>{
  const home={id:'page',type:'page',label:'About Home',value:'/'},repo={id:'repo',type:'repository',label:'Pharos',value:'pharos'};
  const original=[home,'Find usage in ',repo,' this week'];
  const moved=plain(prompt.askPromptMoveChip(original,'page',6));
  assert.deepEqual(moved,['Find ',home,'usage in ',repo,' this week']);
  assert.equal(prompt.askPromptText(moved),'Find usage in  this week');
  assert.equal(prompt.askPromptLength(moved),prompt.askPromptText(moved).length+2);
  const end=plain(prompt.askPromptMoveChip(moved,'page',prompt.askPromptLength(moved)));
  assert.deepEqual(end,['Find usage in ',repo,' this week',home]);
  assert.deepEqual(plain(prompt.askPromptMoveChip(end,'page',0)),original);
  assert.deepEqual(original,[home,'Find usage in ',repo,' this week'],'moving must not modify prior undo states');
  assert.deepEqual(plain(prompt.askPromptMoveChip(moved,'missing',0)),moved);
  assert.deepEqual(plain(prompt.askPromptMoveChip(moved,'page',5)),moved);
  assert.deepEqual(plain(prompt.askPromptMoveChip(moved,'page',6)),moved);
});

test('prompt replacement, selection and deletion preserve atomic chips and multiline Unicode text',()=>{
  const chip={id:'page',type:'page',label:'About Home',value:'/'};
  const original=['Find ',chip,'é漢🧭\nthis week'];
  assert.deepEqual(plain(prompt.askPromptSplice(original,5,1)),['Find é漢🧭\nthis week']);
  assert.deepEqual(plain(prompt.askPromptSplice(original,4,4,[' more '])),['Find more 🧭\nthis week']);
  assert.deepEqual(plain(prompt.askPromptReplaceText(original,'New draft')),['New d',chip,'raft']);
  assert.deepEqual(plain(prompt.askPromptReplaceText(original,'')),[chip]);
  assert.deepEqual(plain(prompt.askPromptNormalize(['','Hello',' ',chip,chip,'world','!'])),['Hello ',chip,'world!']);
  const [before,after]=plain(prompt.askPromptSplit(original,10));
  assert.deepEqual(plain(prompt.askPromptNormalize([...before,...after])),original);
  assert.equal(prompt.askPromptText([chip]),'','context labels are metadata, not question text');
  assert.deepEqual(plain(prompt.askPromptStepChip(['A👩‍💻',chip,'éB'],'page',-1)),['A',chip,'👩‍💻éB']);
  assert.deepEqual(plain(prompt.askPromptStepChip(['A👩‍💻',chip,'éB'],'page',1)),['A👩‍💻é',chip,'B']);
  assert.deepEqual(plain(prompt.askPromptRestore(original,'Find é漢🧭\nthis week')),original);
  assert.equal(prompt.askPromptRestore(original,'A newer draft'),null,'stale chip positions must not overwrite a newer draft');
  assert.equal(prompt.askPromptRestore([{id:'page',label:'Incomplete chip'}],''),null);
  assert.equal(prompt.askPromptRestore(null,''),null,'older tabs need no document migration');
});

test('shared Ask glow changes working speed without restarting its hue and cleans up motion', () => {
  const rates=[[],[]],events=[[],[]];
  const hues=events.map((log,index)=>({currentTime:42000,play:()=>log.push('play'),pause:()=>log.push('pause'),cancel:()=>log.push('cancel'),updatePlaybackRate:rate=>rates[index].push(rate)}));
  let mediaChanged,unsubscribed=false;
  const media={matches:false,addEventListener:(name,listener)=>{mediaChanged=listener;},removeEventListener:(name,listener)=>{assert.equal(listener,mediaChanged);unsubscribed=true;}};
  const glow=loadClassicScript('ask-glow.js',{matchMedia:()=>media});
  const writes=[];
  const host={dataset:new Proxy({},{set:(target,key,value)=>{writes.push([key,value]);target[key]=value;return true;}})};
  // Only the decorative layers can animate; the host contains unfiltered text
  // and panel fills and deliberately has no animate() method.
  const motion=glow.askGlowMotion(host,hues.map(hue=>({animate:()=>hue})));
  motion.setWorking(false);
  motion.setWorking(true);
  motion.setWorking(true); // Streamed progress must not restart the effect.
  assert.equal(host.dataset.glowWorking,'true');
  motion.setWorking(false);
  assert.equal(host.dataset.glowWorking,'false');
  assert.deepEqual(writes,[['glowWorking','false'],['glowWorking','true'],['glowWorking','false']],'streamed progress must not trigger redundant DOM mutations');
  for(let index=0;index<hues.length;index++){
    assert.equal(rates[index].length,2);
    assert.ok(rates[index][0]>rates[index][1],'working cycles colors faster than idle');
    assert.equal(hues[index].currentTime,42000,'speed changes preserve each layer’s hue phase');
  }
  media.matches=true;mediaChanged();
  media.matches=false;mediaChanged();
  for(const log of events)assert.deepEqual(log,['play','pause','play']);
  motion.destroy();
  assert.ok(unsubscribed);
  for(const log of events)assert.equal(log.at(-1),'cancel');
  assert.equal(host.dataset.glowWorking,undefined);
});

test('glow traces cross their origin within one open path instead of splitting a dash at closed endpoints', () => {
  const glow=loadClassicScript('ask-glow.js');
  for(const contour of ['M3 13a4 4 0 0 0 4 4h9l5 4V7a4 4 0 0 0-4-4H7a4 4 0 0 0-4 4Z','M 20.5 1.2 C 10 1 1 10 1 20 L 20.5 1.2 Z']){
    const trace=glow.askGlowTracePath(contour);
    assert.equal((trace.match(/M/gi)||[]).length,1,'one continuous subpath keeps the dash intact');
    assert.ok(!/z/i.test(trace),'the stroke endpoints stay outside the animated range');
    assert.equal((trace.match(/a4|C /g)||[]).length,(contour.match(/a4|C /g)||[]).length*2,'both laps preserve the original curves');
  }
});

test('shared Ask glow pauses its hue while the decoration is not shown, and resumes in phase', () => {
  const events=[];
  const hue={play:()=>events.push('play'),pause:()=>events.push('pause'),cancel(){},updatePlaybackRate(){}};
  let report,observed,disconnected=false;
  class IntersectionObserver{constructor(callback){report=callback;}observe(target){observed=target;}disconnect(){disconnected=true;}}
  const glow=loadClassicScript('ask-glow.js',{IntersectionObserver,matchMedia:()=>({matches:false,addEventListener(){},removeEventListener(){}})});
  const layers=[{animate:()=>hue}];
  const motion=glow.askGlowMotion({dataset:{}},layers);
  assert.equal(observed,layers[0]);
  events.length=0;
  report([{isIntersecting:false}]);
  assert.deepEqual(events,['pause'],'a hidden panel or full screen composer stops ticking');
  report([{isIntersecting:true}]);
  assert.deepEqual(events,['pause','play']);
  motion.destroy();
  assert.ok(disconnected);
});

test('the edge trace turns each corner as the previous side leaves, around one lap', () => {
  const glow=loadClassicScript('ask-glow.js');
  const width=1000,height=500,length=200,perimeter=3000;
  const lanes=plain(glow.askEdgeTraceLanes({width,height,length}));
  assert.deepEqual(lanes.map(lane=>lane.side),['top','right','bottom','left']);
  assert.deepEqual(lanes.map(lane=>lane.start*perimeter),[0,1000,1500,2500],'each side starts as the signal reaches its corner');
  for(const [index,lane] of lanes.entries()){
    const next=lanes[(index+1)%lanes.length],ends=(lane.start+lane.travel)*perimeter,turns=next.start*perimeter+(next.start?0:perimeter);
    assert.equal(ends-turns,length,`${lane.side} leaves while the next side enters, so the corner never goes dark`);
    assert.ok(lane.travel<1,'a lane fits within its lap');
    // Streaks start and finish entirely off their side.
    const size=lane.axis==='X'?width:height;
    assert.deepEqual([Math.min(lane.from,lane.to),Math.max(lane.from,lane.to)],[-length,size]);
  }
  assert.ok(lanes[0].from<lanes[0].to&&lanes[2].from>lanes[2].to,'the signal runs clockwise');
});

test('shared Ask glow respects reduced motion from startup, including while working', () => {
  const events=[];
  const hue={play:()=>events.push('play'),pause:()=>events.push('pause'),cancel(){},updatePlaybackRate(){}};
  const glow=loadClassicScript('ask-glow.js',{matchMedia:()=>({matches:true,addEventListener(){},removeEventListener(){}})});
  const host={dataset:{}};
  const motion=glow.askGlowMotion(host,[{animate:()=>hue},{animate:()=>hue}]);
  motion.setWorking(true);
  assert.deepEqual(events,['pause','pause']);
  assert.equal(host.dataset.glowWorking,'true','loading remains visible with motion disabled');
  motion.destroy();
});

test('prompt suggestions mix broad questions with real repository scopes only in multi-repository libraries', () => {
  const suggestions=options=>plain(model.askPromptSuggestions(options));
  const broad=suggestions();
  for(const repositories of [[],['pharos'],['pharos','pharos',null,'','  ']]){
    assert.deepEqual(suggestions({repositories}),broad);
  }
  const repositories=['pharos','query-table'];
  const mixed=suggestions({repositories});
  assert.equal(mixed.length,broad.length);
  assert.equal(new Set(mixed).size,mixed.length);
  assert.ok(mixed.some((text,index)=>text===broad[index]),'keep library-wide questions');
  for(const name of repositories)assert.ok(mixed.some(text=>text.includes('“'+name+'” repository')));
  assert.ok(mixed.every(text=>!text.includes('{scope}')),'never offer a template placeholder');
  const varied=suggestions({repositories,variation:1});
  assert.notDeepEqual(varied,mixed,'refresh can introduce another repository for the same question');
  assert.equal(varied.find(text=>text==='Which repositories use the most tokens?'),'Which repositories use the most tokens?');
});

test('suggestion scopes preserve repository names as data, including punctuation and Unicode', () => {
  const repositories=['研究/<test> & tools','owner/repo.with-dots'];
  const suggestions=plain(model.askPromptSuggestions({repositories}));
  for(const repository of repositories)assert.ok(suggestions.some(text=>text.includes('“'+repository+'” repository')));
});

test('Ask composer keeps search available while draft and conversation controls follow content', () => {
  const actions = state => plain(model.askComposerActions({enabled:true, model:'available-model', ...state}));
  assert.deepEqual(actions({}), {send:false, stop:false, history:true, clear:false, modelLocked:false});
  assert.deepEqual(actions({draft:'  '}), {send:false, stop:false, history:false, clear:true, modelLocked:false});
  assert.deepEqual(actions({draft:'Find my work'}), {send:true, stop:false, history:false, clear:true, modelLocked:false});
  assert.deepEqual(actions({turns:[{prompt:'Find my work'}]}), {send:false, stop:false, history:false, clear:true, modelLocked:true});
  assert.equal(actions({draft:'Follow up', pending:true}).send, false);
  assert.equal(actions({draft:'Find', enabled:false}).send, false);
  assert.equal(actions({draft:'Find', model:''}).send, false);
  for(const draft of ['', '  ', 'An unsent follow-up']) {
    const running=actions({draft,pending:true});
    assert.equal(running.stop,true);
    assert.equal(running.send,false);
    assert.equal(actions({draft,pending:false}).stop,false);
  }
  assert.equal(actions({pending:true,enabled:false,model:''}).stop,true,'an active request can still be stopped when model access changes');
  assert.equal(actions({draft:'Find',opening:true}).send,false);
  assert.equal(actions({opening:true}).stop,false,'opening a conversation is not an active request');
});
test('a single destination opens for any question, while editing or search suppresses navigation', () => {
  const navigate = state => model.askShouldNavigate({enabled:true, regular:false, edited:false, prompt:'Open my token usage', cards:[{id:'tokens'}], ...state});
  assert.equal(navigate({}), true);
  for (const prompt of ['Where am I wasting time?', 'Please show me yesterday’s page', 'Take me to my earlier chat', 'Go to Settings', 'Find my work']) assert.equal(navigate({prompt}),true);
  for (const state of [{cards:[]}, {cards:[{},{}]}, {hasPreview:true}, {omitted:1}, {draft:' '}, {draft:'Actually…'}, {edited:true}, {regular:true}, {enabled:false}]) assert.equal(navigate(state),false);
  // A lone card left after Pharos dropped others is a choice, and says so.
  assert.equal(model.askOmittedNote(0), '');
  assert.ok(model.askOmittedNote(1));
  assert.match(model.askOmittedNote(2), /2/);
});
test('retrying with another model restores the full conversation and unsent draft in order', () => {
  const turns=[
    {prompt:'Find slow commands\nin this project',status:'clarification',clarification:{question:'Which period?',options:['This week','Last week']}},
    {prompt:'This week',status:'multiple',reply:'Here are some views.',cards:[{title:'Usage',description:'Explore commands',href:'/analyze'}]},
    {prompt:'This week',status:'error',reply:'No response'},
  ];
  const expected='You:\nFind slow commands\nin this project\n\nPharos:\nWhich period?\n\n- This week\n\n- Last week\n\nYou:\nThis week\n\nPharos:\nHere are some views.\n\nUsage\nExplore commands\n/analyze\n\nYou:\nThis week\n\nPharos (error):\nNo response';
  assert.equal(model.askRetryDraft({turns}),expected);
  assert.equal(model.askRetryDraft({turns,draft:'  Focus on build commands\nand tests  '}),expected+'\n\nYou (unsent draft):\n  Focus on build commands\nand tests  ');
  assert.equal(model.askRetryDraft({turns,draft:'  '}),expected);
  assert.equal(model.askRetryDraft({}), '');
  assert.equal(model.askRetryDraft({turns:[{prompt:'Try this',status:'canceled',reply:'Request canceled'}]}),'You:\nTry this\n\nPharos (canceled):\nRequest canceled');
  assert.equal(model.askRetryDraft({turns:[{prompt:'Hello',reply:''}]}),'You:\nHello');
});
test('retry transcripts fit the server byte limit while prioritizing recent turns and the unsent draft', () => {
  const turns=[
    {prompt:'Older question',reply:'A'.repeat(8000)},
    {prompt:'Recent question',reply:'B'.repeat(8000)},
    {prompt:'Latest question',status:'error',reply:'No response'},
  ];
  const draft='Keep this unsent question.';
  const text=model.askRetryDraft({turns,draft});
  assert.ok(Buffer.byteLength(text,'utf8')<=12000);
  assert.ok(text.startsWith('[Earlier conversation omitted or shortened to fit.]'));
  assert.ok(!text.includes('Older question'));
  assert.ok(text.includes('Recent question\n\nPharos:\n'+'B'.repeat(8000)));
  assert.ok(text.includes('Latest question\n\nPharos (error):\nNo response'));
  assert.ok(text.endsWith('You (unsent draft):\n'+draft));
  assert.equal(turns[0].reply.length,8000,'formatting must not modify the saved conversation');
});
test('retry transcript limits count UTF-8 bytes, including labels, and safely shorten oversized messages', () => {
  const exact={turns:[{prompt:'x'.repeat(11995)}]};
  assert.equal(Buffer.byteLength(model.askRetryDraft(exact),'utf8'),12000);
  assert.equal(model.askRetryDraft(exact),'You:\n'+'x'.repeat(11995),'an exactly fitting transcript needs no omissions');
  const cases=[
    {turns:[{prompt:'x'.repeat(11996)}]},
    {turns:[{prompt:'p'.repeat(11000),reply:'r'.repeat(8000)}]},
    {turns:[{prompt:'é漢🧭'.repeat(2000),reply:'Reply'}],draft:'Keep my draft'},
    {turns:[{prompt:'Earlier question'}],draft:'🧭'.repeat(3000)},
  ];
  for(const input of cases){
    const text=model.askRetryDraft(input);
    assert.ok(Buffer.byteLength(text,'utf8')<=12000,'labels and notices must fit the same byte budget');
    assert.ok(text.includes('[Message shortened.]'));
    assert.equal(text,text.toWellFormed(),'truncation must preserve complete Unicode code points');
    assert.ok(!text.includes('\ufffd'),'truncation must not replace part of a code point');
    if(input.draft==='Keep my draft')assert.ok(text.endsWith('You (unsent draft):\nKeep my draft'));
  }
});
test('chat URLs preserve unrelated parameters and support replacing and clearing the active chat', () => {
  const original='http://pharos.test/?token=local&range=week#composer';
  const first=new URL(model.askChatURL(original,'chat one'));
  assert.equal(first.searchParams.get('ask_chat'),'chat one');
  assert.equal(first.searchParams.get('token'),'local');
  assert.equal(first.searchParams.get('range'),'week');
  assert.equal(first.hash,'#composer');
  const second=new URL(model.askChatURL(first.href,'another-chat'));
  assert.deepEqual(second.searchParams.getAll('ask_chat'),['another-chat']);
  assert.equal(model.askChatURL(second.href,null),original);
  assert.equal(new URL(model.askChatURL('http://pharos.test/home','saved')).searchParams.get('ask_chat'),'saved');
  const destination='http://pharos.test/analyze?analyze_topic=tools';
  assert.equal(model.askChatURL(destination,'saved'),destination);
  assert.equal(model.askHistoryURL(original,'saved'),model.askChatURL(original,'saved'));
  assert.equal(model.askHistoryURL('http://pharos.test/home?range=week','saved'),'http://pharos.test/home?range=week&ask_chat=saved');
  for(const path of ['/library?text=cache','/analyze?analyze_topic=tools','/settings#models']){
    assert.equal(model.askHistoryURL('http://pharos.test'+path,'chat one'),'http://pharos.test/?ask_chat=chat+one');
  }
  assert.equal(model.askHistoryURL('http://pharos.test/ask?ask_chat=old','saved'),'http://pharos.test/ask?ask_chat=saved','full screen keeps history on its page');
});
test('full screen opens the chat on the Ask page and returns to the page it came from', () => {
  assert.equal(model.askFullscreenURL('http://pharos.test/analyze?analyze_topic=tools','chat one'),'http://pharos.test/ask?ask_chat=chat+one');
  assert.equal(model.askFullscreenURL('http://pharos.test/?ask_chat=old',null),'http://pharos.test/ask');
  const exit=(from,id='saved')=>plain(model.askFullscreenExit('http://pharos.test/ask?ask_chat=saved',from,id));
  assert.deepEqual(exit('/analyze?analyze_topic=tools'),{href:'http://pharos.test/analyze?analyze_topic=tools',presentation:'modal'});
  assert.deepEqual(exit('/?range=week'),{href:'http://pharos.test/?range=week&ask_chat=saved',presentation:'home'});
  assert.deepEqual(exit('/home',null),{href:'http://pharos.test/home',presentation:'home'});
  for(const from of [null,undefined,'','/ask?ask_chat=other','https://elsewhere.test/analyze','http://[bad'])assert.deepEqual(exit(from),{href:'http://pharos.test/?ask_chat=saved',presentation:'home'},String(from));
});
test('reload restores the tab surface only for the same page and chat', () => {
  const href='http://pharos.test/?ask_chat=saved-chat&range=week';
  const saved={page:'/?range=week',id:'saved-chat'};
  for(const presentation of ['home','modal','floating','dismissed']){
    assert.equal(model.askRestorePresentation(href,{...saved,presentation}),presentation);
  }
  const modal={...saved,presentation:'modal'};
  assert.equal(model.askRestorePresentation('http://pharos.test/?range=week',modal),'modal','an overlay draft need not change the page URL');
  assert.equal(model.askRestorePresentation(href+'&token=local',modal),'modal');
  assert.equal(model.askRestorePresentation('http://pharos.test/?ask_chat=other-chat&range=week',modal),null);
  assert.equal(model.askRestorePresentation('http://pharos.test/?range=month',modal),null);
  assert.equal(model.askRestorePresentation('http://pharos.test/analyze',modal),null);
  for(const invalid of [null,{},'broken',{...saved,presentation:'unknown'}])assert.equal(model.askRestorePresentation(href,invalid),null);
  for(const presentation of ['modal','floating','dismissed']){
    assert.equal(model.askRestorePresentation('http://pharos.test/analyze?analyze_topic=tokens',{page:'/analyze?analyze_topic=tokens',presentation}),presentation);
  }
  assert.equal(model.askRestorePresentation('http://pharos.test/analyze',{page:'/analyze',presentation:'home'}),null);
  assert.equal(model.askRestorePresentation('http://pharos.test/ask?ask_chat=saved-chat',{page:'/ask',id:'saved-chat',presentation:'page'}),'page');
  assert.equal(model.askRestorePresentation('http://pharos.test/analyze',{page:'/analyze',presentation:'page'}),null);
});
test('history keeps server matches from later messages and only adds the current chat to unfiltered lists', () => {
  const first={id:'initial',updatedAt:1,conversation:[{prompt:'Initial topic'}]},second={id:'newer',updatedAt:2,conversation:[{prompt:'Another topic'}]};
  const entries=options=>plain(model.askHistoryEntries({sessions:[first,second],...options}));
  assert.deepEqual(entries({query:'later topic'}).map(session=>session.id),['newer','initial']);
  const current={...first,updatedAt:3};
  assert.deepEqual(entries({current}).map(session=>session.id),['initial','newer']);
  assert.deepEqual(entries({query:'later topic',current,sessions:[]}),[],'an unmatched current chat must not leak into search results');
});
test('private visit writes preserve deletion order and recover after a failed update', async () => {
  const events=[];
  let finish;
  const pending=new Promise(resolve=>{finish=resolve;});
  const update=model.askWriteVisit(async()=>{events.push('replace');await pending;events.push('saved');});
  const removal=model.askWriteVisit(()=>events.push('remove'));
  await Promise.resolve();
  assert.deepEqual(events,['replace']);
  finish();await Promise.all([update,removal]);
  assert.deepEqual(events,['replace','saved','remove']);
  const failed=model.askWriteVisit(()=>{throw new Error('offline');});
  const clear=model.askWriteVisit(()=>events.push('clear'));
  await assert.rejects(failed,/offline/);await clear;
  assert.equal(events.at(-1),'clear');
});
test('a single visit replaces one entry, while a full history write reconciles the private store', () => {
  const visit={url:'/analyze',at:2,title:'Analyze'};
  const one=plain(model.askVisitInput({entry:visit,replaced:{url:'/',at:1}}));
  assert.equal(one.reconcile,false);
  assert.deepEqual(one.visits.map(entry=>entry.url),['/analyze']);
  assert.deepEqual(one.replaces,{url:'/',at:1},'the private store receives the identity removed from browser history');
  assert.equal(plain(model.askVisitInput({entry:visit})).replaces,null);
  const stored=[{url:'/',at:1,title:'Stored title'},{url:'/library',page:'library',at:2},{url:'/settings',at:3,title:'x'.repeat(900)}];
  const full=plain(model.askVisitInput({stored,titleFor:url=>url==='/'?'Home':''}));
  assert.equal(full.reconcile,true);
  assert.equal(full.replaces,null);
  assert.deepEqual(full.visits.map(entry=>entry.url),['/','/library','/settings'],'every stored visit is sent');
  assert.equal(full.visits[0].title,'Home','current page titles replace stored ones');
  assert.equal(full.visits[1].title,'Visited page','untitled visits get a readable fallback');
  assert.equal(full.visits[2].title.length,500,'titles are clipped to the server limit');
  assert.equal(stored[2].title.length,900,'browser history is not modified');
});
test('a provider change starts from its default and a model change keeps only an effort it offers', () => {
  const catalogs={
    Codex:{models:[{id:'fast',efforts:['Default']},{id:'deep',efforts:['Default','Low','High'],default:true}]},
    Claude:{models:[{id:'opus',efforts:['Default','High']},{id:'sonnet',efforts:['Default']}]},
    Gemini:{models:[{id:'pro'}],defaultSelection:{provider:'Gemini',model:'pro',effort:'High'}},
  };
  assert.deepEqual(plain(model.askDefaultSelection('Codex',catalogs.Codex)),{provider:'Codex',model:'deep',effort:'Default'},'the marked default model wins');
  assert.deepEqual(plain(model.askDefaultSelection('Claude',catalogs.Claude)).model,'opus','without a marked default the first model is used');
  assert.deepEqual(plain(model.askDefaultSelection('Gemini',catalogs.Gemini)),catalogs.Gemini.defaultSelection,"a provider's own default wins");
  assert.equal(model.askDefaultSelection('Empty',{models:[]}).model,'','a provider without models has no model to send with');
  const start={provider:'Codex',model:'deep',effort:'High'};
  const pick=(page,value)=>plain(model.askPickSelection(start,page,value,catalogs));
  assert.deepEqual(pick('provider','Claude'),{provider:'Claude',model:'opus',effort:'Default'});
  assert.deepEqual(pick('model','fast'),{provider:'Codex',model:'fast',effort:'Default'},'an effort the new model lacks resets');
  assert.deepEqual(pick('effort','Low'),{...start,effort:'Low'});
  assert.deepEqual(plain(model.askPickSelection({provider:'Claude',model:'sonnet',effort:'Default'},'model','opus',catalogs)).effort,'Default');
  assert.deepEqual(plain(model.askPickSelection({provider:'Claude',model:'sonnet',effort:'High'},'model','opus',catalogs)).effort,'High','an offered effort is kept');
  assert.deepEqual(start,{provider:'Codex',model:'deep',effort:'High'},'picking returns a new selection');
  assert.deepEqual(plain(model.askEffortChoices(catalogs.Codex,'missing')),['Default'],'an unknown model offers only the default effort');
});
test('Fast is off by default, offered per model and dropped when the model or provider lacks it', () => {
  const catalogs={
    Codex:{models:[{id:'luna',efforts:['Default'],fast:true},{id:'old',efforts:['Default']}]},
    Claude:{models:[{id:'opus',efforts:['Default'],fast:true,default:true}]},
  };
  assert.equal(model.askDefaultSelection('Claude',catalogs.Claude).fast,undefined,'a default selection is not fast');
  assert.equal(model.askFastAvailable(catalogs.Codex,'luna'),true);
  assert.equal(model.askFastAvailable(catalogs.Codex,'old'),false);
  const fast=plain(model.askPickSelection({provider:'Codex',model:'luna',effort:'Default'},'fast',true,catalogs));
  assert.deepEqual(fast,{provider:'Codex',model:'luna',effort:'Default',fast:true});
  assert.deepEqual(plain(model.askPickSelection(fast,'fast',false,catalogs)),{provider:'Codex',model:'luna',effort:'Default'},'turning Fast off leaves no flag');
  assert.equal(plain(model.askPickSelection(fast,'model','old',catalogs)).fast,undefined,'a model without Fast drops it');
  assert.equal(plain(model.askPickSelection(fast,'effort','Default',catalogs)).fast,true,'an effort change keeps Fast');
  assert.equal(plain(model.askPickSelection(fast,'provider','Claude',catalogs)).fast,undefined,'a new provider starts with Fast off');
  assert.equal(model.askIsDefaultSelection(fast,{provider:'Codex',model:'luna',effort:'Default'}),false,'Fast differs from a standard-speed default');
  assert.equal(model.askIsDefaultSelection(fast,fast),true);
});
test('a saved selection is used while its provider exists, otherwise the first provider loads', () => {
  const providers=[{id:'Codex'},{id:'Claude'}];
  const saved={provider:'Claude',model:'retired-model',effort:'High'};
  assert.equal(model.askSelectionProvider(saved,providers),'Claude');
  assert.equal(model.askSelectionProvider({...saved,provider:'Gone'},providers),'Codex');
  assert.equal(model.askSelectionProvider(null,providers),'Codex');
  assert.equal(model.askValidSelection(saved,providers),true,'a model missing from the catalog stays selected; the server validates it');
  assert.equal(model.askValidSelection({...saved,provider:'Gone'},providers),false);
  assert.equal(model.askValidSelection({provider:'Codex'},providers),false);
  assert.equal(model.askValidSelection(null,providers),false);
  assert.equal(model.askIsDefaultSelection(saved,{...saved}),true);
  assert.equal(model.askIsDefaultSelection(saved,{...saved,effort:'Default'}),false);
  assert.equal(model.askIsDefaultSelection(saved,null),false);
});
test('a later or invalidated chat load makes an earlier slow load stale', async () => {
  const loads=model.askLoadSequence(),applied=[];
  const open=async(id,wait)=>{const token=loads.next();await wait;if(loads.current(token))applied.push(id);};
  let finishSlow;
  const slow=open('slow',new Promise(resolve=>{finishSlow=resolve;}));
  await open('fast',Promise.resolve());
  finishSlow();await slow;
  assert.deepEqual(applied,['fast'],'a late response must not replace the chat opened after it');
  const token=loads.next();
  assert.equal(loads.current(token),true);
  loads.invalidate();
  assert.equal(loads.current(token),false,'a new chat or route change cancels a pending load');
});
test('an opened chat focuses its composer only if focus stayed put while it loaded', () => {
  const body={},link={},elsewhere={};
  const focus=state=>model.askFocusAfterLoad({navigate:true,current:true,before:link,active:link,body,...state});
  assert.equal(focus({}),true);
  assert.equal(focus({active:body}),true,'focus dropped to the page while loading');
  assert.equal(focus({active:elsewhere}),false,'the user moved focus while it loaded');
  assert.equal(focus({navigate:false}),false,'restoring from the URL leaves focus alone');
  assert.equal(focus({current:false}),false,'a superseded load does not take focus');
});
test('the transcript fits the latest exchange with a little prior context, within its window budget', () => {
  const height=state=>model.askThreadHeight({total:2000,latest:300,previous:250,gap:26,available:600,...state});
  assert.equal(height({}),390);
  assert.equal(height({total:4000}),390,'earlier history must not grow the composer');
  assert.equal(height({previous:30}),356,'short replies need no extra empty space');
  assert.equal(height({previous:0,total:300}),300,'a first exchange fits naturally');
  assert.equal(height({latest:1200}),600,'a long reply stays scrollable');
  assert.equal(height({available:180}),180,'small windows keep the editor reachable');
});
test('only the latest unanswered clarification accepts a choice or a nonempty custom answer', () => {
  const actions=state=>plain(model.askClarificationActions({latest:true,status:'clarification',enabled:true,...state}));
  assert.deepEqual(actions({}),{active:true,sendOther:false});
  assert.deepEqual(actions({draft:'  '}),{active:true,sendOther:false});
  assert.deepEqual(actions({draft:'Workspace count'}),{active:true,sendOther:true});
  for(const state of [{latest:false},{status:'loading'},{status:'single'},{pending:true},{opening:true},{enabled:false}]){
    assert.deepEqual(actions({draft:'Tokens',...state}),{active:false,sendOther:false});
  }
});

test('Ask receives activity before the final answer across split UTF-8 and JSON chunks', {timeout:2000}, async () => {
  let controller,observed;
  const seen=new Promise(resolve=>{observed=resolve;});
  const body=new ReadableStream({start(value){controller=value;}});
  const response=new Response(body,{headers:{'Content-Type':'application/x-ndjson'}});
  const activity={message:'Browsing available views…',elapsedMs:1200};
  const bytes=new TextEncoder().encode(JSON.stringify({type:'progress',activity})+'\n');
  const split=bytes.indexOf(0xe2)+1;
  const messages=[];
  const result=model.askReadResponse(response,value=>{messages.push(plain(value));observed();});
  controller.enqueue(bytes.slice(0,split));controller.enqueue(bytes.slice(split));
  await seen;
  assert.deepEqual(messages,[activity]);
  const session={id:'chat-1',conversation:[{status:'single',cards:[{href:'/'}]}]};
  controller.enqueue(new TextEncoder().encode(JSON.stringify({type:'result',session})+'\n'));
  controller.close();
  assert.deepEqual(plain(await result),session);
});

test('Ask stream failures reconcile instead of accepting an unfinished answer', async () => {
  const stream=text=>new Response(text,{headers:{'Content-Type':'application/x-ndjson'}});
  await assert.rejects(model.askReadResponse(stream('{"type":"progress","activity":{}}\n')),/before the answer/);
  await assert.rejects(model.askReadResponse(stream('{"type":"result","session":')),/before the answer/);
  await assert.rejects(model.askReadResponse(stream('not JSON\n')),/JSON|Unexpected/);
  await assert.rejects(model.askReadResponse(new Response('{"error":"Model unavailable"}',{status:400})),/Model unavailable/);
  const session={id:'older-server'};
  assert.deepEqual(plain(await model.askReadResponse(new Response(JSON.stringify(session)))),session);
});

test('Ask preserves progress and elapsed time, exposing activity details only for errors', () => {
  const activity=[{message:'Searching archived work…',elapsedMs:4000}];
  const active=plain(model.askWaitStatus({activity,elapsedMs:7000,cli:'Codex'}));
  assert.equal(active.message,activity[0].message);
  assert.match(active.elapsed,/7s elapsed/);
  const quiet=plain(model.askWaitStatus({activity,elapsedMs:24000,cli:'Codex'}));
  assert.equal(quiet.message,activity[0].message);
  assert.match(quiet.elapsed,/20s since last update/);
  assert.equal(model.askWaitStatus({cli:'Claude',elapsedMs:12000}).elapsed,'');
  for(const status of ['loading','single','multiple','answer','none','clarification','canceled']) {
    assert.equal(model.askShowActivity({status,activity}),false,status);
  }
  assert.equal(model.askShowActivity({status:'error',activity}),true);
  assert.equal(model.askShowActivity({status:'error',activity:[]}),false);
  assert.equal(model.askShowActivity({status:'error',activity:null}),false);
  assert.equal(model.askShowActivity({status:'error'}),false);
});

test('page context preserves the active view while removing chat routing and credentials', () => {
  assert.equal(model.askPageURI('http://pharos.test/analyze?analyze_topic=tokens&q_usage=filters#chart'), '/analyze?analyze_topic=tokens&q_usage=filters#chart');
  assert.equal(model.askPageURI('http://pharos.test/?ask_chat=chat&token=secret&accessToken=secret&range=week'), '/?range=week');
});
test('browsing suppresses automatic navigation even after reopening a single response', () => {
  const state={cards:[{href:'/analyze'}],enabled:true,regular:false,edited:false};
  assert.equal(model.askShouldNavigate(state),true);
  assert.equal(model.askShouldNavigate({...state,browsing:true}),false);
  assert.equal(model.askShowBrowseHint({}),false);
  for(const presentation of ['home','modal']) {
    assert.equal(model.askShowBrowseHint({pending:true,presentation}),true);
    assert.equal(model.askShowBrowseHint({pending:true,presentation,browsing:true}),false);
    assert.equal(model.askShowBrowseHint({pending:false,presentation}),false);
  }
  for(const presentation of ['floating','dismissed'])assert.equal(model.askShowBrowseHint({pending:true,presentation}),false);
  assert.equal(model.askShowBrowseHint({pending:true,presentation:'modal',hint:false}),false);
});
test('a dismissed or used browse hint stays away for five questions, then returns', () => {
  assert.deepEqual(plain(model.askStartBrowseHint(undefined)),{hint:true,snoozed:0});
  let snoozed=model.askBrowseHintSnooze(),shown=[];
  for(let i=0;i<7;i++){const start=model.askStartBrowseHint(snoozed);shown.push(start.hint);snoozed=start.snoozed;}
  assert.deepEqual(shown,[false,false,false,false,false,true,true]);
  assert.deepEqual(plain(model.askStartBrowseHint('junk')),{hint:true,snoozed:0});
});
test('floating progress reflects the latest state and distinguishes completion from waiting or cancellation', () => {
  const running=model.askFloatingState({status:'loading',message:'Comparing model usage…'});
  assert.equal(running.message,'Comparing model usage…');assert.equal(running.ready,false);
  const streaming=model.askFloatingState({status:'loading',message:'Starting Codex…',elapsedMs:7000,activity:[{message:'Browsing available views…',elapsedMs:2000},{message:'Searching archived work…',elapsedMs:4000}]});
  assert.equal(streaming.message,'Searching archived work…');assert.equal(streaming.ready,false);
  for (const status of ['single','multiple','clarification','error']) assert.equal(model.askFloatingState({status}).ready,true);
  assert.equal(model.askFloatingState({status:'canceled'}).ready,false);
  const opened=model.askFloatingState({status:'single'},{navigated:true});
  assert.match(opened.message,/follow-up/);assert.equal(opened.ready,false);
  assert.equal(model.askFloatingState({status:'clarification'},{navigated:true}).ready,true);
});
test('dragged progress stays reachable after moving or shrinking the window', () => {
  const bounds={width:300,height:80,viewportWidth:390,viewportHeight:550};
  assert.deepEqual(plain(model.askFloatingPosition({...bounds,x:-100,y:999})),{x:8,y:462});
  assert.deepEqual(plain(model.askFloatingPosition({...bounds,x:60,y:100})),{x:60,y:100});
});

test('Ask previews resolve supported dashboard links independently of the current page and collection', t => {
  const {values} = fixture(t);
  global.location = new URL('http://parity.test/library?record_usage=unrelated');
  const original = location.href;
  for (const [topic, state] of Object.entries(dashboardDefaults)) {
    const params = new URLSearchParams({analyze_mode:'dashboards', analyze_topic:topic, analyze_chart:JSON.stringify(state), analyze_filters:encodeQuery({...EMPTY_QUERY,where:[]})});
    const address = '/analyze?' + params;
    const job = askChartPreviewJob(address,schemas);
    assert.equal(job.dataset,usageTopicDatasets[topic]);
    assert.equal(job.options.dashboard,address);
  }
  assert.equal(location.href,original);
  assert.equal(values.has('pharos-bookmarks-v1'),false);
});

test('Ask leaves unsupported, invalid and unavailable chart destinations as ordinary links', t => {
  fixture(t);
  const valid='/analyze?analyze_mode=dashboards&analyze_topic=tokens';
  for (const address of ['/settings','/library','/analyze','/analyze?analyze_mode=query&analyze_topic=tokens',
    'https://elsewhere.test'+valid, valid+'&analyze_chart=%7Bbad',valid+'&analyze_filters=bad',valid.replace('tokens','missing')]) {
    assert.equal(askChartPreviewJob(address,schemas),undefined,address);
  }
  assert.equal(askChartPreviewJob(valid,{}),undefined);
  assert.equal(askChartPreviewJob(valid,{usage:schemas.tools}),undefined);
});

test('Ask preview jobs keep each chart URL and its moving window intact', t => {
  fixture(t);
  const urls = ['claude','codex'].map(provider=>'/analyze?'+new URLSearchParams({analyze_mode:'dashboards',analyze_topic:'tokens',analyze_window:'today',analyze_timezone:'America/Denver',analyze_filters:encodeQuery({...EMPTY_QUERY,where:[{field:'provider',op:'=',value:provider}]})}));
  const jobs = urls.map(address=>askChartPreviewJob(address,schemas));
  assert.notEqual(jobs[0].id,jobs[1].id);
  for (const [index,job] of jobs.entries()) assert.equal(job.options.dashboard,urls[index]);
});

test('Ask previews work pages and their conversations in either link spelling, and nothing else', () => {
  assert.deepEqual(askWorkPreviewAddress('/work/w%201'),{workspace:'w 1'});
  assert.deepEqual(askWorkPreviewAddress('/work/w1?conversation=c1&reveal=m4'),{workspace:'w1',conversation:'c1'});
  assert.deepEqual(askWorkPreviewAddress('/work/w1/conversation/c1?highlight=m1&highlight=m2'),{workspace:'w1',conversation:'c1'});
  for (const address of ['/work','/work/','/work/w1/files','/work/w1/conversation/','/work/w1/conversation/c1?conversation=c2','/work/w1?find=retry','https://elsewhere.test/work/w1','/conversation/c1','/?ask_chat=w1','/analyze?analyze_mode=dashboards']) {
    assert.equal(askWorkPreviewAddress(address),undefined,address);
  }
  assert.equal(workPreviewEndpoint({workspace:'w/1',conversation:'c&1'}),'/api/work/w%2F1/preview?conversation=c%261');
  assert.equal(workPreviewEndpoint({workspace:'w1'}),'/api/work/w1/preview');
});

test('Ask combines preview kinds so each link renders with the first kind that claims it', () => {
  const mounted = [], kind = (name, prefix) => ({canRender:address=>address.startsWith(prefix),revalidate:()=>mounted.push(name+' revalidated'),mount:(_host,card)=>{mounted.push(name+' '+card.href);return ()=>{};}});
  const previews = combineAskPreviews(kind('chart','/analyze'),kind('work','/work/'));
  assert.equal(previews.canRender('/settings'),false);
  previews.mount(null,{href:'/work/w1',title:''});previews.mount(null,{href:'/analyze?x',title:''});previews.revalidate();
  assert.deepEqual(mounted,['work /work/w1','chart /analyze?x','chart revalidated','work revalidated']);
});

test('work previews identify the run in two lines: where and when, then who and how much', () => {
  const metric = (value, quality='observed') => ({value,quality,known:1,total:1});
  const specs = [['cost','API cost','usd'],['clock','Clock span','ms'],['children','Children','count'],['turns','User turns','count'],['files','Files Edited','count']].map(([id,label,unit])=>({id,label,unit,definition:label+' definition'}));
  const models = [{model:'claude-opus-4-5-20251101',provider:'claude',sessions:3,tokens:9},{model:'gpt-5',provider:'codex',sessions:1,tokens:5},{model:'gemini',provider:'antigravity',sessions:1,tokens:2}];
  const now = Date.parse('2026-11-01T18:00:00Z'), start = Date.parse('2026-10-30T20:14:00Z');
  const workspace = {start,end:null,sessions:7,models,totals:{cost:metric(41.2,'partial'),clock:metric(3_600_000*3+12*60_000),children:metric(6),turns:metric(1),files:metric(null)}};
  const preview = {repository:'pharos',branch:'gbdubs/fix',metrics:specs,workspace};
  const text = parts => parts.map(part=>part.text);
  const work = workPreviewView(preview,now);
  assert.match(work.where[0].text,/Oct 30/);assert.doesNotMatch(work.where[0].text,/2026/,'this year is implied');
  assert.match(work.where[0].title,/2026/);
  assert.equal(work.where[1].text,'3h 12m');
  assert.equal(work.where[2].text,'pharos · gbdubs/fix','the place truncates first');
  assert.deepEqual(text(work.what),['6 subagents','claude-opus-4-5, gpt-5 +1','1 turn','$41.20+'],'unknown measures are omitted; partial ones are marked');
  assert.match(work.what[1].title,/claude-opus-4-5-20251101 \(claude\): 3 sessions/);
  assert.match(workPreviewView(preview,Date.parse('2027-02-01T00:00:00Z')).where[0].text,/2026/,'other years are named');
  // Independent top-level conversations are sessions, not subagents; partial
  // counts keep their marker even when only one item was seen.
  const conversation = {start:null,end:null,sessions:2,models:models.slice(1,2),totals:{children:metric(0),turns:metric(1,'partial'),files:metric(4,'partial')}};
  const scoped = workPreviewView({metrics:specs,workspace,conversation},now);
  assert.deepEqual(text(scoped.where),[],'nothing known about where or when is left out');
  assert.deepEqual(text(scoped.what),['0 subagents','gpt-5','1+ turns','4+ files']);
});

test('retained Ask charts revalidate on reopening and their own data or calendar refresh until disposed', () => {
  const openings = new EventTarget(), data = new EventTarget(), refreshes = [];
  const stop = onAskChartRefresh('usage',openings,stale=>refreshes.push(stale),data);
  openings.dispatchEvent(new Event('open'));
  data.dispatchEvent(new CustomEvent('pharos:data-refresh',{detail:'tools'}));
  assert.deepEqual(refreshes,[false],'reopening keeps a fresh preview and unrelated datasets leave the chart alone');
  data.dispatchEvent(new CustomEvent('pharos:data-refresh',{detail:'usage'}));
  data.dispatchEvent(new Event('pharos:usage-refresh'));
  assert.deepEqual(refreshes,[false,true,true],'both data and calendar changes invalidate the retained view');
  stop();
  openings.dispatchEvent(new Event('open'));
  data.dispatchEvent(new CustomEvent('pharos:data-refresh',{detail:'usage'}));
  data.dispatchEvent(new Event('pharos:usage-refresh'));
  assert.equal(refreshes.length,3,'switching or clearing chats removes every refresh subscription');
});
