// One mark for events on near-identical text in the timeline and the
// fingerprint (event-merge.ts), by the reader's rule (assets/event-spans.js).
import assert from 'node:assert/strict';
import {test} from 'node:test';
import {eventSpanGroups} from '../../../../internal/archive/assets/event-spans.js';
import {byRarity,eventCardView,eventKindCounts,markIds,mergeAgentEvents} from '../../../src/event-merge.ts';
import {eventLayout,placeEvents} from '../../../src/event-link-overlay.tsx';
import {ribbonEventCount} from '../../../src/label-session-ribbon-model.ts';
import {reader,spans as classic} from '../helpers/transcript.mjs';
import {plain} from '../helpers/classic-script.mjs';

const event=(id,kind,group,span,extra={})=>({id,message_id:'m',kind,label:kind,group,probability:.8,source:'reply',sentence:'s'+id,target:null,span,position:span[0]/100,...extra});
const friction='Friction',learning='Reactions and learning',choices='Choices you may not see';

test('the timeline and the reader group sentences by the same rule',()=>{
 const cases=[[[0,10],[2,8]],[[0,10],[1,10]],[[0,10],[3,13]],[[0,10],[10,20]],[[0,10],[1,11],[2,12],[40,50]],[[5,9],[0,20],[30,40],[31,40]]];
 for(const spans of cases){
  assert.deepEqual(eventSpanGroups(spans),plain(classic.eventSpanGroups(spans)),JSON.stringify(spans));
  // The reader merges exactly these groups into one sentence each.
  const text='x'.repeat(60),view=reader.messageEventView({role:'assistant',kind:'message',text,agent_events:spans.map((sentence,i)=>({id:'e'+i,kind:'obstacle',label:'Obstacle',group:friction,probability:.9,sentence,subject:[],response:[]}))});
  const readerGroups=[...new Map(view.map(e=>[e.sentence.join(':'),[]])).keys()].length;
  const marks=mergeAgentEvents(spans.map((span,i)=>event('e'+i,'obstacle',friction,span)));
  assert.equal(marks.length,readerGroups,`${JSON.stringify(spans)}: one mark per reader sentence`);
 }
 // Only events of one message merge.
 assert.equal(mergeAgentEvents([event('a','obstacle',friction,[0,10]),event('b','obstacle',friction,[0,10],{message_id:'other'})]).length,2);
});

test('a merged mark shows its rarest kind; ties go to the reader\'s group order, then the kind',()=>{
 const members=[event('a','obstacle',friction,[0,10]),event('b','discovery',learning,[0,10]),event('c','assumption',choices,[1,10])];
 const counts=new Map([['obstacle',40],['discovery',3],['assumption',9]]);
 const [mark]=mergeAgentEvents(members,counts);
 assert.deepEqual([mark.kind,mark.group,mark.id],['discovery',learning,'b'],'the fewest events in the population');
 assert.deepEqual(markIds(mark),['b','c','a'],'members rarest first');
 assert.deepEqual(byRarity(members,new Map([['obstacle',3],['discovery',3],['assumption',3]])).map(e=>e.kind),['obstacle','discovery','assumption'],'a tie follows the group order');
 const two=[event('x','workaround',choices,[0,10]),event('y','assumption',choices,[0,10])];
 assert.equal(byRarity(two,new Map()).map(e=>e.kind)[0],'assumption','then the kind\'s key');
 // By default the population is the events given.
 assert.equal(mergeAgentEvents([...members,event('d','obstacle',friction,[50,60]),event('e','assumption',choices,[70,80])])[0].kind,'discovery');
});

test('a merged mark sits at its text\'s start and its arcs reach every target',()=>{
 const prompt={method:'quote',message_id:'p',text:'drawer only',strong:true,position:.5},earlier={method:'quote',message_id:'q',text:'side panel',strong:true,position:.2};
 const members=[event('a','obstacle',friction,[20,40],{target:prompt}),event('b','discovery',learning,[10,40],{target:earlier}),event('c','obstacle',friction,[22,40])];
 const [mark]=mergeAgentEvents(members,new Map([['obstacle',5],['discovery',1]]));
 assert.equal(mark.position,.1,'where the group\'s text starts');
 assert.equal(mark.target.message_id,'q','the rarest member\'s target');
 assert.deepEqual(mark.also.map(t=>t.message_id),['p'],'the other target');
 const at={m:.6,p:.1,q:.3},placed=placeEvents([mark],e=>at[e.message_id],e=>at[e.target.message_id]);
 const layout=eventLayout(placed,1000,40);
 assert.equal(layout.markers.length,1,'one marker');
 assert.deepEqual(layout.arcs.map(arc=>[arc.key,arc.x0,arc.events]).sort(),[['p',100,['b']],['q',300,['b']]],'both arcs end at it');
 assert.equal(layout.markers[0].arc,'q');
});

test('a merged card lists each kind once with the text and a shared target once; counts stay events',()=>{
 const target={method:'quote',message_id:'p',text:'drawer only',strong:true};
 const members=[event('a','obstacle',friction,[0,40],{sentence:'It failed twice.',target,probability:.7}),event('b','obstacle',friction,[0,40],{sentence:'It failed twice.',target,probability:.9}),event('c','assumption',choices,[0,30],{sentence:'It failed',target})];
 const [mark]=mergeAgentEvents(members);
 const view=eventCardView(mark);
 assert.deepEqual(view.labels.map(l=>[l.kind,l.probability]),[['assumption',.8],['obstacle',.9]]);
 assert.deepEqual(view.sentences,['It failed twice.'],'a sentence inside another is said once');
 assert.deepEqual(view.targets.map(t=>t.text),['drawer only']);
 assert.equal(eventKindCounts([mark]).get('obstacle'),2,'a mark counts its events');
 assert.equal(ribbonEventCount(members.map(e=>({id:e.id,start:0,end:0,title:'',color:'',thinking:false,event:e})),'events'),3,'row headers count events, not marks');
});
