import assert from 'node:assert/strict';
import {test} from 'node:test';
import {readFileSync} from 'node:fs';
import {join} from 'node:path';
import vm from 'node:vm';
import {assets, loadClassicScript} from '../helpers/classic-script.mjs';

const source=readFileSync(join(assets,'application.html'),'utf8').match(/<script id="setup-wizard-model">([\s\S]*?)<\/script>/)[1];
const model=vm.runInNewContext(source+';({createPharosSetupWizard,nextPharosLibraryName})',{TextEncoder,URLSearchParams});
const create=model.createPharosSetupWizard;
const plain=value=>structuredClone(value);

test('setup recommends storage from the number of Macs without creating anything',()=>{
  for(const [machines,storage]of [['one','mac'],['many','ssd']]){
    const wizard=create();wizard.setMachines(machines);
    assert.equal(wizard.step,'machines');assert.equal(wizard.storage,storage);
    assert.throws(()=>wizard.payload(),/Review/);
    wizard.next();assert.equal(wizard.step,'storage');
    assert.throws(()=>wizard.payload(),/Review/);
    wizard.next();assert.equal(wizard.step,'details');
  }
});

test('setup keeps deliberate overrides when going back, and clears dependent folders when the route changes',()=>{
  const wizard=create();wizard.setMachines('many');wizard.next();
  wizard.setStorage('mac');wizard.next();wizard.name='Research';wizard.parent='/Users/me/History';
  assert.ok(wizard.back());assert.equal(wizard.step,'storage');assert.equal(wizard.parent,'/Users/me/History');
  wizard.back();wizard.setMachines('many');wizard.next();
  assert.equal(wizard.storage,'mac');assert.equal(wizard.parent,'/Users/me/History');
  wizard.back();wizard.setMachines('one');wizard.next();
  assert.equal(wizard.storage,'mac');assert.equal(wizard.parent,'');assert.equal(wizard.name,'Research');
  wizard.setStorage('ssd');assert.equal(wizard.parent,'');
});

test('external setup requires a folder and review, and constructs one named library inside its parent',()=>{
  const wizard=create();wizard.setMachines('many');wizard.next();wizard.next();
  assert.equal(wizard.folder('/Users/me/Libraries'),'');
  assert.equal(wizard.next().field,'path');assert.equal(wizard.step,'details');
  wizard.parent='/Volumes/My SSD/';wizard.name=' Research ';
  assert.equal(wizard.folder(),'/Volumes/My SSD/Research');
  assert.throws(()=>wizard.payload(),/Review/);
  assert.equal(wizard.next(),null);
  assert.deepEqual(plain(wizard.payload()),{name:'Research',path:'/Volumes/My SSD/Research',location:'ssd'});
  wizard.back();wizard.name='New name';wizard.next();
  assert.equal(wizard.payload().path,'/Volumes/My SSD/New name');
});

test('local setup explicitly requests Mac storage, including when the app itself is portable',()=>{
  const wizard=create();wizard.next();wizard.next();wizard.next();
  assert.deepEqual(plain(wizard.payload()),{name:'My Library',path:'',location:'mac'});
  assert.equal(wizard.folder('/isolated/support/Libraries'),'/isolated/support/Libraries/My Library');
  wizard.parent='/';assert.equal(wizard.payload().path,'/My Library');
});

test('setup keeps invalid names and paths out of review',()=>{
  for(const name of ['', '.', '..','a/b','a\\b','a\0b','é'.repeat(81)]){
    const wizard=create();wizard.next();wizard.next();wizard.name=name;
    assert.equal(wizard.next().field,'name');assert.equal(wizard.step,'details');
  }
  const wizard=create();wizard.next();wizard.next();wizard.parent='relative/folder';
  assert.equal(wizard.next().field,'path');wizard.parent='~/History';wizard.next();
  assert.equal(wizard.payload().path,'~/History/My Library');
  assert.throws(()=>wizard.setMachines('unknown'));assert.throws(()=>wizard.setStorage('network'));
});

test('new libraries get an unused proposed name',()=>{
  assert.equal(model.nextPharosLibraryName([]),'My Library');
  assert.equal(model.nextPharosLibraryName([{name:'My Library'},{name:'My Library 2'},{name:'Research'}]),'My Library 3');
});

test('switching libraries saves the current session before connecting and only navigates on success',async()=>{
  const {switchPharosLibrary}=loadClassicScript('managed-library.js');
  for(const failing of ['', 'save', 'connect']){
    const events=[];
    const operation=switchPharosLibrary('archive',{
      flush:async()=>{events.push('save');if(failing==='save')throw Error('save failed')},
      connect:async id=>{events.push(id);if(failing==='connect')throw Error('connect failed')},
      navigate:path=>events.push(path),
    });
    if(failing)await assert.rejects(operation,new RegExp(failing+' failed'));else await operation;
    assert.deepEqual(events,failing==='save'?['save']:failing==='connect'?['save','archive']:['save','archive','/settings/libraries']);
  }
});


test('library settings resolves local and external destinations and rejects invalid proposals',()=>{
  const {pharosLibraryProposal,pharosLibraryName}=loadClassicScript('managed-library.js');
  assert.equal(pharosLibraryName([{name:'My Library'},{name:'My Library 2'}]),'My Library 3');
  assert.deepEqual(plain(pharosLibraryProposal(' Research ','','mac')),{name:'Research',path:'',location:'mac'});
  assert.deepEqual(plain(pharosLibraryProposal(' Research ',' /Volumes/SSD/ ','ssd')),{name:'Research',path:'/Volumes/SSD/Research',location:'ssd'});
  assert.equal(pharosLibraryProposal('Research','/','mac').path,'/Research');
  assert.equal(pharosLibraryProposal('Research','~/Libraries','mac').path,'~/Libraries/Research');
  for(const name of ['', '.', '..', 'a/b', 'a\\b', 'é'.repeat(81)])assert.throws(()=>pharosLibraryProposal(name,'','mac'),/name/);
  assert.throws(()=>pharosLibraryProposal('Research','','ssd'),/parent folder/);
  assert.throws(()=>pharosLibraryProposal('Research','relative/path','mac'),/absolute/);
  assert.throws(()=>pharosLibraryProposal('Research','','network'),/store/);
});
