import { test } from 'node:test';
import assert from 'node:assert/strict';
import { loadClassicScript, plain } from './helpers/classic-script.mjs';
const model = loadClassicScript('ask-model.js',{TextEncoder,TextDecoder});

test('Ask composer keeps search available while draft and conversation controls follow content', () => {
  const actions = state => plain(model.askComposerActions({enabled:true, model:'available-model', ...state}));
  assert.deepEqual(actions({}), {send:false, history:true, clear:false, modelLocked:false});
  assert.deepEqual(actions({draft:'  '}), {send:false, history:false, clear:true, modelLocked:false});
  assert.deepEqual(actions({draft:'Find my work'}), {send:true, history:false, clear:true, modelLocked:false});
  assert.deepEqual(actions({turns:[{prompt:'Find my work'}]}), {send:false, history:false, clear:true, modelLocked:true});
  assert.equal(actions({draft:'Follow up', pending:true}).send, false);
  assert.equal(actions({draft:'Find', enabled:false}).send, false);
  assert.equal(actions({draft:'Find', model:''}).send, false);
});
test('a single destination opens for any question, while editing or search suppresses navigation', () => {
  const navigate = state => model.askShouldNavigate({enabled:true, regular:false, edited:false, prompt:'Open my token usage', cards:[{id:'tokens'}], ...state});
  assert.equal(navigate({}), true);
  for (const prompt of ['Where am I wasting time?', 'Please show me yesterday’s page', 'Take me to my earlier chat', 'Go to Settings', 'Find my work']) assert.equal(navigate({prompt}),true);
  for (const state of [{cards:[]}, {cards:[{},{}]}, {draft:' '}, {draft:'Actually…'}, {edited:true}, {regular:true}, {enabled:false}]) assert.equal(navigate(state),false);
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
