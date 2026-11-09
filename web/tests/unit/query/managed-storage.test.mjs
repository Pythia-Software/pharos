import assert from 'node:assert/strict';
import {test} from 'node:test';
import {loadClassicScript} from '../helpers/classic-script.mjs';
import historyBudget from '../../../../testdata/page-history-budget.json' with {type:'json'};

const keys=['pharos-ui-feedback-v1','pharos-ui-feedback-draft-v1','pharos-ui-feedback-layout-v1',
  'pharos-feedback-enabled','pharos-history-v1','pharos-nav-open','query-table:last:pharos_library','pharos-query-rejected:pharos_library'];
function session(database={},options={}) {
  const scope={preferences:'pharos-session:mac-app:',storage:'library:instance:',legacy:'library:',...options.scope};
  const calls=[],paths=[];
  const env=loadClassicScript('managed-storage.js',{
    pharosManagedSession:scope,pharosPreferenceValues:{...database},
    console:{warn(){}},
    fetch:async(path,request)=>{
      const body=JSON.parse(request.body);calls.push(body);paths.push(path);
      if(options.send)return options.send(body,path);
      Object.assign(database,body.set);
      for(const key of body.delete)delete database[key];
      return{ok:true};
    },
    ...options.globals,
  });
  return{env,calls,paths,scope,storage:env.localStorage,flush:()=>env.pharosSession.flush()};
}

test('page history keeps the newest complete visits within the encoded preference limit',()=>{
  const {serializePageHistory}=loadClassicScript('managed-storage.js');
  // preferences_test.go checks these same byte budgets with encoding/json.
  const entries=JSON.parse(historyBudget.prefixes.at(-1).value);
  const {value:two,encoded_bytes:budget}=historyBudget.prefixes[2];
  assert.equal(serializePageHistory(entries,{maxBytes:budget}),two);
  assert.equal(serializePageHistory(entries,{maxBytes:budget-1}),JSON.stringify(entries.slice(0,1)));
  assert.equal(serializePageHistory(entries,{limit:1}),JSON.stringify(entries.slice(0,1)));
  const oversized={url:'/library?search='+'x'.repeat(budget),title:'Huge view',at:5};
  assert.equal(serializePageHistory([oversized,...entries],{maxBytes:budget}),two,'an unstoreable visit does not erase usable history');
  assert.equal(serializePageHistory([]),'[]');
});

test('session save failures include the server reason and retain the draft for retry',async()=>{
  let failing=true;
  const current=session({}, {send:()=>failing?{ok:false,status:400,json:async()=>({error:'preference is larger than 512 KB'})}:{ok:true}});
  current.storage.setItem('pharos-ui-feedback-draft-v1','draft');
  await assert.rejects(current.flush(),/HTTP 400.*larger than 512 KB/);
  assert.equal(current.storage.getItem('pharos-ui-feedback-draft-v1'),'draft');
  failing=false;await current.flush();
  assert.equal(current.calls.length,2);
});

test('managed sessions restore working state from the library across empty web views',async()=>{
  const database={},first=session(database);
  for(const [index,key]of keys.entries())first.storage.setItem(key,`value-${index}`);
  first.storage.setItem('cover-cache','temporary preview');
  await first.flush();
  assert.deepEqual([...first.paths].sort(),['/api/preferences','/api/session-preferences']);
  const restored=session(database);
  for(const [index,key]of keys.entries())assert.equal(restored.storage.getItem(key),`value-${index}`);
  assert.equal(restored.storage.getItem('cover-cache'),null);
  assert.equal(session(database,{scope:{preferences:'pharos-session:other-mac:'}}).storage.getItem(keys[0]),null);
  assert.equal(session({}).storage.getItem(keys[0]),null,'another library has independent preferences');
  restored.storage.removeItem(keys[0]);await restored.flush();
  assert.equal(session(database).storage.getItem(keys[0]),null,'deletions survive relaunch');
});

test('transient state does not delay or report failures during durable saves',async()=>{
  let rejectBackground,finishCritical;
  const timers=[];
  const current=session({}, {globals:{setTimeout:fn=>{timers.push(fn);return timers.length},clearTimeout(){}},send:(_body,path)=>
    new Promise((resolve,reject)=>{if(path==='/api/session-preferences')rejectBackground=reject;else finishCritical=resolve})});
  current.storage.setItem('pharos-history-v1','old');
  current.storage.setItem('pharos-ui-feedback-draft-v1','draft');
  const saving=current.flush();
  current.storage.setItem('pharos-history-v1','new');
  rejectBackground(new Error('busy'));
  finishCritical({ok:true});
  await saving;
  await Promise.resolve();
  assert.equal(current.storage.getItem('pharos-history-v1'),'new');
  assert.equal(current.env.events.filter(event=>event.type==='pharos:session-save-error').length,0);
  assert.ok(timers.length,'background failures schedule a retry');
  const retry=timers.at(-1);retry();
  await Promise.resolve();
  assert.equal(current.calls.at(-1).set[current.scope.preferences+'pharos-history-v1'],'new');
});

test('a failed feedback draft save still warns the user',async()=>{
  let run;
  const current=session({}, {globals:{setTimeout:fn=>{run=fn;return 1},clearTimeout(){}},send:()=>Promise.reject(new Error('offline'))});
  current.storage.setItem('pharos-ui-feedback-draft-v1','draft');
  run();
  await new Promise(resolve=>setImmediate(resolve));
  assert.equal(current.env.events.filter(event=>event.type==='pharos:session-save-error').length,1);
  assert.equal(current.storage.getItem('pharos-ui-feedback-draft-v1'),'draft');
});

test('explicit history removal follows older saves and preserves later visits',async()=>{
  const finish=[];
  const current=session({}, {send:()=>new Promise(resolve=>finish.push(resolve))});
  current.storage.setItem('pharos-history-v1','old');
  await current.flush();
  current.storage.setItem('pharos-history-v1','cleared');
  const removal=current.env.pharosSession.preserve('pharos-history-v1');
  current.storage.setItem('pharos-history-v1','later visit');
  assert.deepEqual(current.paths,['/api/session-preferences']);
  finish[0]({ok:true});
  await new Promise(resolve=>setImmediate(resolve));
  assert.equal(current.paths.at(-1),'/api/preferences');
  assert.equal(current.calls.at(-1).set[current.scope.preferences+'pharos-history-v1'],'cleared');
  finish.at(-1)({ok:true});await removal;
  finish[1]({ok:true});
  await new Promise(resolve=>setImmediate(resolve));
  await current.flush();
  assert.equal(current.paths.at(-1),'/api/session-preferences');
  assert.equal(current.calls.at(-1).set[current.scope.preferences+'pharos-history-v1'],'later visit');
  finish.at(-1)({ok:true});
});

test('a failed explicit removal does not prevent later navigation',async()=>{
  const current=session({}, {send:(_body,path)=>path==='/api/preferences'?Promise.reject(new Error('offline')):{ok:true}});
  current.storage.setItem('pharos-history-v1','cleared');
  await assert.rejects(current.env.pharosSession.preserve('pharos-history-v1'),/offline/);
  await current.flush();
  assert.equal(current.storage.getItem('pharos-history-v1'),'cleared');
});

test('managed session writes serialize and retry without replacing newer edits',async()=>{
  let finish,reject;const requests=[];
  const current=session({}, {send:body=>{requests.push(body);return new Promise((resolve,rejectWrite)=>{finish=resolve;reject=rejectWrite})}});
  current.storage.setItem(keys[0],'original');const first=current.flush();
  current.storage.setItem(keys[0],'newer');const waiting=current.flush();
  reject(new Error('drive unavailable'));
  await assert.rejects(first,/drive unavailable/);await assert.rejects(waiting,/drive unavailable/);
  const retry=current.flush();finish({ok:true});await retry;
  assert.equal(requests.length,2);
  assert.equal(requests[1].set[current.scope.preferences+keys[0]],'newer');
  assert.equal(current.storage.getItem(keys[0]),'newer');
});

test('managed sessions import only the same library’s known older session keys',async()=>{
  const raw=loadClassicScript('managed-storage.js').localStorage;
  raw.setItem('library:'+keys[0],'annotation');
  raw.setItem('other-library:'+keys[1],'private other draft');
  raw.setItem('library:cover-cache','old preview');
  const database={},current=session(database,{globals:{localStorage:raw}});
  assert.equal(current.storage.getItem(keys[0]),'annotation');
  assert.equal(current.storage.getItem(keys[1]),null);
  await current.flush();
  assert.equal(raw.getItem('library:'+keys[0]),null);
  assert.equal(raw.getItem('other-library:'+keys[1]),'private other draft');
  assert.equal(Object.keys(database).length,1);
});
