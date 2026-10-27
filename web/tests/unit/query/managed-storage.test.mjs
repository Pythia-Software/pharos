import assert from 'node:assert/strict';
import {test} from 'node:test';
import {loadClassicScript} from '../helpers/classic-script.mjs';

const keys=['pharos-ui-feedback-v1','pharos-ui-feedback-draft-v1','pharos-ui-feedback-layout-v1',
  'pharos-feedback-enabled','pharos-history-v1','pharos-nav-open','query-table:last:pharos_library','pharos-query-rejected:pharos_library'];
function session(database={},options={}) {
  const scope={preferences:'pharos-session:mac-app:',storage:'library:instance:',legacy:'library:',...options.scope};
  const calls=[];
  const env=loadClassicScript('managed-storage.js',{
    pharosManagedSession:scope,pharosPreferenceValues:{...database},
    console:{warn(){}},
    fetch:async(path,request)=>{
      const body=JSON.parse(request.body);calls.push(body);
      if(options.send)return options.send(body);
      Object.assign(database,body.set);
      for(const key of body.delete)delete database[key];
      return{ok:true};
    },
    ...options.globals,
  });
  return{env,calls,scope,storage:env.localStorage,flush:()=>env.pharosSession.flush()};
}

test('managed sessions restore working state from the library across empty web views',async()=>{
  const database={},first=session(database);
  for(const [index,key]of keys.entries())first.storage.setItem(key,`value-${index}`);
  first.storage.setItem('cover-cache','temporary preview');
  await first.flush();
  const restored=session(database);
  for(const [index,key]of keys.entries())assert.equal(restored.storage.getItem(key),`value-${index}`);
  assert.equal(restored.storage.getItem('cover-cache'),null);
  assert.equal(session(database,{scope:{preferences:'pharos-session:other-mac:'}}).storage.getItem(keys[0]),null);
  assert.equal(session({}).storage.getItem(keys[0]),null,'another library has independent preferences');
  restored.storage.removeItem(keys[0]);await restored.flush();
  assert.equal(session(database).storage.getItem(keys[0]),null,'deletions survive relaunch');
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
