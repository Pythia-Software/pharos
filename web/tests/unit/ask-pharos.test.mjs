import { test } from 'node:test';
import assert from 'node:assert/strict';
import { loadClassicScript, plain } from './helpers/classic-script.mjs';
const model = loadClassicScript('ask-model.js');

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
