import assert from 'node:assert/strict';
import {test} from 'node:test';
import {readFileSync} from 'node:fs';
import {join} from 'node:path';
import vm from 'node:vm';
import {assets} from '../helpers/classic-script.mjs';
const source=readFileSync(join(assets,'onboarding.js'),'utf8').split('// Source setup UI.')[0];
const create=vm.runInNewContext(source+';createPharosSourceWizard');
const plain=value=>structuredClone(value);
const candidate=(kind,path=kind)=>({name:kind,kind,path,acceptable:true,recommended:true,status:'found'});

test('source setup begins without tool assumptions or implicit agent dependencies',()=>{
  const wizard=create();assert.deepEqual(plain(wizard.request()),{kinds:[],locations:[]});
  wizard.choose('conductor',true);assert.deepEqual(plain(wizard.request().kinds),['conductor']);
  wizard.choose('tl1',true);assert.deepEqual(plain(wizard.request().kinds),['conductor','tl1']);
  assert.throws(()=>wizard.choose('unsupported',true));
});

test('presence detection offers only found tools first and preserves deliberate choices',()=>{
  const wizard=create();wizard.detect([{kind:'codex'},{kind:'conductor'},{kind:'unknown'},{kind:'codex'}]);
  assert.deepEqual(plain(wizard.toolGroups()),{found:['codex','conductor'],other:['claude','tl1','antigravity']});
  assert.deepEqual(plain(wizard.request().kinds),['codex','conductor']);
  wizard.choose('codex',false);wizard.choose('tl1',true);
  wizard.detect([{kind:'codex'},{kind:'conductor'}]);
  assert.deepEqual(plain(wizard.request().kinds),['conductor','tl1']);
  const empty=create();empty.detect([]);
  assert.deepEqual(plain(empty.request().kinds),[]);
  assert.equal(empty.toolGroups().other.length,5);
});

test('source consent includes only selected tools and preserves excluded folders across rediscovery',()=>{
  const wizard=create();wizard.choose('claude',true);
  const personal=candidate('claude','/personal'),work={...candidate('claude','/work'),name:'work'};
  wizard.discovered({candidates:[personal,work,candidate('codex')]});
  assert.equal(wizard.selected().length,2);wizard.set(work,false);
  wizard.discovered({candidates:[personal,work,candidate('codex')]});
  assert.deepEqual(plain(wizard.selection()),{kinds:['claude'],locations:[],accept:['claude'],decline:['work'],names:{}});
  wizard.choose('claude',false);wizard.discovered({candidates:[personal]});
  assert.deepEqual(plain(wizard.selection().accept),[]);
});

test('source changes retain folder provenance and cannot enable already configured shared sources',()=>{
  const wizard=create();wizard.choose('codex',true);wizard.locations.push({kind:'codex',path:'/custom'});
  const shared={...candidate('codex'),configured:{scope:'library',enabled:false}};
  wizard.discovered({candidates:[shared]});assert.equal(wizard.selected().length,0);
  wizard.choose('codex',false);assert.deepEqual(plain(wizard.request().locations),[]);
  wizard.choose('claude',true);
  const first=candidate('claude','/first'),other=candidate('claude','/different');
  wizard.discovered({candidates:[first]});wizard.set(first,false);
  wizard.discovered({candidates:[other]});assert.equal(wizard.selected().length,1);
});
