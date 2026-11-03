import test from 'node:test';
import assert from 'node:assert/strict';
import {findPattern,findMatches,countMatches,findTest,findFromAttention,writeFindParameters,stepIndex,FIND_LIMIT} from '../../../src/page-find-model';
import {viewFields,parseViewValue,retainPageView} from '../../../src/view-contract';

const state=(text,options={})=>({text,caseSensitive:false,regex:false,...options});
const ready=(text,options)=>{const result=findPattern(state(text,options));assert.equal(result.status,'ready');return result.pattern;};

test('a typed short term waits for Enter; restored and entered terms search at any length',()=>{
 assert.equal(findPattern(state('')).status,'empty');
 assert.equal(findPattern(state('ab'),true).status,'short');
 assert.equal(findPattern(state('ab')).status,'ready');
 assert.equal(findPattern(state('abc'),true).status,'ready');
 // Code points, not UTF-16 units: one emoji plus a letter is still short.
 assert.equal(findPattern(state('😀a'),true).status,'short');
});

test('literal terms escape regex syntax; regex and case options are honoured',()=>{
 assert.deepEqual(findMatches('A.B(C) axb(c)',ready('a.b(c)')),[[0,6]]);
 assert.deepEqual(findMatches('axb a.b',ready('a.b',{regex:true})),[[0,3],[4,7]]);
 assert.deepEqual(findMatches('Quorum quorum',ready('quorum',{caseSensitive:true})),[[7,13]]);
 const invalid=findPattern(state('(',{regex:true}));
 assert.equal(invalid.status,'invalid');assert.ok(invalid.error);
});

test('matches skip empty regex hits, keep original offsets and stop at the limit',()=>{
 assert.deepEqual(findMatches('abc',ready('x*',{regex:true})),[]);
 // Lowercasing İ changes the string length; offsets must index the original.
 assert.deepEqual(findMatches('İstanbul İ test',ready('test')),[[11,15]]);
 assert.equal(findMatches('a'.repeat(FIND_LIMIT+50),ready('a')).length,FIND_LIMIT);
 assert.equal(findMatches('aaaa',ready('a'),2).length,2);
});

test('unrendered text is counted across texts within the remaining limit',()=>{
 const pattern=ready('turn');
 assert.equal(countMatches(['Turn one','no','turn turn'],pattern),3);
 assert.equal(countMatches(['turn turn turn'],pattern,2),2);
 assert.equal(countMatches([],pattern),0);
 const single=findTest(pattern);
 assert.equal(single('a TURN'),true);assert.equal(single('a TURN'),true);assert.equal(single('none'),false);
});

test('text: attention targets open Find: one literal as typed, several as an escaped OR regex',()=>{
 assert.equal(findFromAttention(['card:x'],'section:y'),null);
 assert.deepEqual(findFromAttention([],'text:a.b'),state('a.b'));
 assert.deepEqual(findFromAttention(['text:a.b','text:c'],'text:a.b'),state('a\\.b|c',{regex:true}));
 assert.equal(findFromAttention(['text:'],''),null);
});

test('Find parameters keep false flags implicit, close cleanly and parse with the contract',()=>{
 const params=new URLSearchParams('find=old&find_case=1&other=x');
 writeFindParameters(params,state('needle',{regex:true}));
 assert.equal(params.toString(),'other=x&find=needle&find_regex=1');
 assert.equal(parseViewValue(viewFields.find_regex,params.get('find_regex')),true);
 assert.throws(()=>parseViewValue(viewFields.find_case,'true'));
 writeFindParameters(params,null);
 assert.equal(params.toString(),'other=x');
 writeFindParameters(params,state(''));
 assert.equal(params.get('find'),'',"an open, empty Find stays in the URL");
});

test('Find belongs to its page: navigation drops it, staying keeps it',()=>{
 const moved=new URLSearchParams('find=x&find_case=1&find_regex=1&nav_collapsed=1');
 retainPageView(moved,'cards',[],'home');
 assert.equal(moved.toString(),'nav_collapsed=1');
 const stayed=new URLSearchParams('find=x&find_case=1');
 retainPageView(stayed,'cards',[],'cards');
 assert.equal(stayed.toString(),'find=x&find_case=1');
});

test('stepping wraps and starts from the first match on screen',()=>{
 assert.equal(stepIndex(-1,1,0),-1);
 assert.equal(stepIndex(-1,1,5,3),3);
 assert.equal(stepIndex(-1,-1,5,3),2);
 assert.equal(stepIndex(-1,-1,5,0),4);
 assert.equal(stepIndex(4,1,5),0);
 assert.equal(stepIndex(0,-1,5),4);
});
