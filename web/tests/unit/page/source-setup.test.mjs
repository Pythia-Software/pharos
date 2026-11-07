import assert from 'node:assert/strict';
import {test} from 'node:test';
import {readFileSync} from 'node:fs';
import {join} from 'node:path';
import vm from 'node:vm';
import {assets} from '../helpers/classic-script.mjs';
import registry from '../../../../testdata/providers.json';
const source=readFileSync(join(assets,'onboarding.js'),'utf8').split('// Source setup UI.')[0];
const [makeWizard,capabilityRows]=vm.runInNewContext(source+';[createPharosSourceWizard,pharosCapabilityRows]');
const create=()=>makeWizard(registry.providers);
const plain=value=>structuredClone(value);
const candidate=(kind,path=kind)=>({name:kind,kind,path,acceptable:true,recommended:true,status:'found'});

test('source setup begins without tool assumptions or implicit agent dependencies',()=>{
  const wizard=create();assert.deepEqual(plain(wizard.request()),{kinds:[],locations:[]});
  wizard.choose('conductor',true);assert.deepEqual(plain(wizard.request().kinds),['conductor']);
  wizard.choose('tl1',true);assert.deepEqual(plain(wizard.request().kinds),['conductor','tl1']);
  assert.throws(()=>wizard.choose('unsupported',true));
});

test('setup lists found tools, then the common ones, and keeps niche tools out of the way',()=>{
  const wizard=create();wizard.detect([{kind:'codex'},{kind:'tl1'},{kind:'unknown'},{kind:'codex'}]);
  const groups=plain(wizard.toolGroups());
  assert.deepEqual(groups.found,['codex','tl1'],'a niche tool found here is listed with the others found');
  assert.deepEqual(groups.more,['conductor'],'a niche tool not found here is only under More tools');
  assert.ok(!groups.offered.includes('tl1')&&!groups.offered.includes('conductor'));
  assert.equal(groups.offered[0],'claude','the most used tools come first');
  assert.deepEqual(plain(wizard.request().kinds),['codex','tl1']);
  const empty=create();empty.detect([]);
  assert.deepEqual(plain(empty.request().kinds),[]);
  assert.deepEqual(plain(empty.toolGroups().more),['conductor','tl1']);
  assert.ok(!empty.toolGroups().offered.includes('chatgpt'),'exports are imported, not found');
  assert.deepEqual(plain(empty.exports().map(item=>item.kind)),['chatgpt','claude-ai']);
  assert.ok(empty.exports().every(item=>item.how),'each export says how to download it');
});

test('a tool with two faces is one choice that reads both',()=>{
  const wizard=create();
  assert.ok(!wizard.catalog.some(tool=>tool.kind==='cursor-cli'),'Cursor CLI is shown under Cursor');
  assert.deepEqual(plain(wizard.tool('cursor-cli').kinds),['cursor','cursor-cli']);
  wizard.detect([{kind:'cursor-cli'}]);
  assert.deepEqual(plain(wizard.toolGroups().found),['cursor'],'either face finds the tool');
  wizard.scan();assert.deepEqual(plain(wizard.request().kinds),['cursor','cursor-cli']);
  wizard.choose('cursor',false);assert.deepEqual(plain(wizard.request().kinds),[]);
});

test('looking in the usual places reads every found tool, and only those',()=>{
  const wizard=create();wizard.detect([{kind:'claude'},{kind:'zed'}]);
  wizard.choose('claude',false);wizard.locations.push({kind:'zed',path:'/old'});
  wizard.scan();
  assert.deepEqual(plain(wizard.request()),{kinds:['claude','zed'],locations:[]});
});

test('naming folders checks only those folders, for the tools chosen',()=>{
  const wizard=create();wizard.detect([{kind:'claude'}]);wizard.manual();
  assert.deepEqual(plain(wizard.request()),{kinds:['claude'],locations:[{kind:'claude',path:'~/.claude'}],only_locations:true},'a chosen tool starts with its usual folder');
  wizard.choose('cursor',true);
  assert.deepEqual(plain(wizard.request().locations.map(location=>location.kind)),['claude','cursor'],'a second face (Cursor CLI) starts empty');
  wizard.name('cursor','');wizard.name('cursor-cli','  ~/.cursor ');
  assert.deepEqual(plain(wizard.request()),{kinds:['claude','cursor-cli'],locations:[{kind:'claude',path:'~/.claude'},{kind:'cursor-cli',path:'~/.cursor'}],only_locations:true});
  wizard.name('claude','');wizard.name('cursor-cli','/elsewhere');wizard.choose('cursor',true);
  assert.deepEqual(plain(wizard.request().locations),[{kind:'cursor-cli',path:'/elsewhere'}],'a cleared folder stays clear; a changed one is replaced');
  wizard.choose('cursor',false);
  assert.deepEqual(plain(wizard.request()),{kinds:[],locations:[],only_locations:true},'a tool unchosen takes its folders with it');
  wizard.choose('claude',false);wizard.choose('claude',true);
  assert.deepEqual(plain(wizard.request().locations),[{kind:'claude',path:'~/.claude'}],'chosen again, it starts again from its usual folder');
  wizard.scan();wizard.choose('codex',true);assert.deepEqual(plain(wizard.request().locations),[],'looking in the usual places names no folders');
  const accepted=create();accepted.manual();accepted.choose('codex',true);accepted.name('codex','/codex');
  assert.deepEqual(plain(accepted.request().locations),[{kind:'codex',path:'/codex'}]);
  accepted.discovered({candidates:[candidate('codex','/codex')]});
  assert.equal(accepted.selection().only_locations,true,'approval repeats the same narrow check');
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

test('setup describes each tool from the registry and says what each records',()=>{
  assert.deepEqual(plain(makeWizard([]).toolGroups()),{found:[],offered:[],more:[]});
  const wizard=create();
  for(const tool of wizard.catalog)assert.ok(tool.summary&&tool.locations.length,`${tool.kind} says what it reads and where`);
  assert.ok(wizard.catalog.find(tool=>tool.kind==='claude').locations.includes('~/.claude*'),'alternate profiles are listed');
  assert.deepEqual(plain(wizard.capabilities()),[]);
  assert.ok(wizard.notRecorded('cursor-cli').includes('Token usage'));
  assert.deepEqual(plain(wizard.notRecorded('unknown')),[]);
  wizard.choose('cursor',true);wizard.choose('claude',true);
  const rows=plain(wizard.capabilities());
  assert.deepEqual(rows.map(row=>row.key),registry.capabilities.map(capability=>capability.key));
  const tokens=rows.find(row=>row.key==='message_tokens');
  assert.deepEqual(tokens.cells.map(cell=>[cell.kind,cell.support]),[['claude','supported'],['cursor','varies'],['cursor-cli','unsupported']]);
  assert.ok(tokens.cells[2].note,'a missing capability says why when it can');
  assert.equal(tokens.cells[0].name,'Claude Code');
  assert.deepEqual(plain(capabilityRows(registry.providers,['unknown'])),[]);
});
