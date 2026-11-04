import {test} from 'node:test';
import assert from 'node:assert/strict';
import {dominantFamily,familySet,labelFamilies,phaseFlow,transitions,motifs,turnPairs,jaccard,shape,scopedMessages,messageURL} from '../../../src/label-trajectories-model.ts';
import {timelinePreviewText} from '../../../src/timeline-preview.ts';
import {nativeTimelineLabels} from '../../../src/label-timeline-overlay.tsx';
import {timelineModeGroups,timelineLegendWindow} from '../../../src/timeline-legend.tsx';

test('native timeline lanes preserve session ownership, empty classifications and pending coverage',()=>{
 const taxonomy=[{id:'C.report.finding',name:'Finding',definition:'A finding'}];
 const native={preview:'Readable preview',preview_truncated:false,id:'message-1',conversation_id:'source-transcript',role:'assistant',kind:'reasoning',created_at:'2026-01-01T00:00:00Z',text:'Observed',model:'test-model',labels:{ids:['C','C.report','C.report.finding'],probs:{'C.report.finding':.9},segment:'AR'}};
 const source=nativeTimelineLabels({taxonomy,lanes:[{session:'main',messages:[native,{...native,id:'no-match',labels:{ids:[],probs:{},segment:'AR'}}]},{session:'child',labels_pending:true,messages:[{...native,id:'pending'}]},{session:'tools-only'}]});
 assert.deepEqual(source.taxonomy,taxonomy);
 assert.deepEqual(source.messages.get('main')[0].labels,[['C',0],['C.report',0],['C.report.finding',.9]]);
 assert.equal(source.messages.get('main')[0].session_id,'main');
 assert.equal(source.messages.get('main')[0].preview,'Readable preview');assert.equal(source.messages.get('main')[0].preview_truncated,false);
 assert.equal(source.messages.get('main')[0].conversation_id,'source-transcript');
 assert.equal(source.messages.get('main')[0].kind,'reasoning');
 assert.equal(source.messages.get('main')[1].chunks,1);
 assert.equal(source.messages.get('child')[0].chunks,0);
 assert.deepEqual(source.messages.get('child')[0].labels,[]);
 assert.deepEqual(source.messages.get('tools-only'),[]);
 assert.equal(nativeTimelineLabels(null).messages.size,0);
});

const message=(id,family,extra={})=>({id,role:'assistant',kind:'message',segment:'AR',labels:family?[[family,.9]]:[],chunks:family?1:0,text:'Test fixture',text_source:'Retained message excerpt',truncated:false,at:null,...extra});
const conversation=(id,messages)=>({id,work_id:'workspace-test',title:'Test fixture',repository:'fixture',provider:'codex',started_at:null,messages});
const index=family=>labelFamilies.findIndex(f=>f.id===family);

test('timeline legend keeps specific labels with their parent modes without duplicating ancestors',()=>{
 const messages=[message('a','C.report.finding',{labels:[['C',1],['C.report',.95],['C.report.finding',.9]]}),message('b','C.report.progress'),message('duplicate','C.report.finding'),message('request','P.request.work.implement'),message('parent','P.request'),message('generic','C'),message('future','P.custom.detail')];
 const groups=timelineModeGroups(messages,[{id:'P.custom',name:'Custom',definition:'A future mode'}]),byID=id=>groups.find(group=>group.id===id);
 assert.deepEqual(byID('C.report').labels,['C.report.finding','C.report.progress']);
 assert.deepEqual(byID('P.request').labels,['P.request.work.implement']);
 assert.deepEqual(byID('C').labels,[]);
 assert.deepEqual(byID('P.custom').labels,['P.custom.detail']);
 assert.equal(byID('P.custom').name,'Custom');
 assert.ok(groups.indexOf(byID('P.request'))<groups.indexOf(byID('C.report')));
 assert.equal(new Set(groups.flatMap(group=>group.labels)).size,4);
 assert.equal(groups.length,4,'absent families do not enter the legend');
 assert.equal(byID('P.request').direct,true);assert.equal(byID('C.report').direct,false);
});

test('dominant labels ignore ancestor probabilities and preserve all families for comparison',()=>{
 const m=message('one','C.report',{labels:[['C',1],['C.report',.99],['C.report.product',.8],['P',.99],['P.request',.95],['P.request.work.implement',.9]]});
 assert.equal(dominantFamily(m),'P.request');
 assert.deepEqual(familySet(m),['C.report','P.request']);
 assert.equal(dominantFamily(message('root','P')),'root');
 assert.equal(dominantFamily(message('empty',null,{chunks:1})),'none');
 assert.equal(dominantFamily(message('missing',null)),'unknown');
});

test('phase flow weights each occupied conversation equally rather than pooling messages',()=>{
 const bins=phaseFlow([[message('a','P.request'),message('b','C.report')],Array.from({length:10},(_,i)=>message(String(i),'C.report'))],1);
 assert.equal(bins[0].support,2);
 assert.equal(bins[0].shares[index('P.request')],.25);
 assert.equal(bins[0].shares[index('C.report')],.75);
 const sparse=phaseFlow([[message('a','P.request')]],4);
 assert.deepEqual(sparse.map(b=>b.support),[1,0,0,0]);
 assert.equal(sparse[1].shares.reduce((a,b)=>a+b),0);
 const ends=phaseFlow([[message('first','P.request'),message('last','C.report')]],4);
 assert.deepEqual(ends.map(b=>b.support),[1,0,0,1]);
 assert.equal(ends[3].shares[index('C.report')],1);
});

test('transitions stay within conversations and retain missing-label boundaries',()=>{
 const grid=transitions([[message('a','P.request'),message('b',null),message('c','C.report')],[message('d','P.frame')]]);
 assert.equal(grid[index('P.request')].counts[index('unknown')],1);
 assert.equal(grid[index('unknown')].counts[index('C.report')],1);
 assert.equal(grid[index('P.request')].counts[index('C.report')],0);
 assert.equal(grid[index('C.report')].total,0);
 assert.equal(grid[index('P.frame')].total,0);
});

test('motifs collapse adjacent repeats and count distinct supporting conversations',()=>{
 const seq=['P.request','P.request','C.report','P.control','C.report','P.control','P.request','C.report','P.control'].map((f,i)=>message(String(i),f));
 const result=motifs([conversation('first',seq),conversation('second',seq.slice(0,4))],'prose');
 const motif=result.find(m=>m.id==='P.request/C.report/P.control');
 assert.equal(motif.occurrences,3);
 assert.deepEqual(motif.conversations,['first','second']);
 assert.equal(motif.first.first,'0');
 const gaps=motifs([conversation('gap',[message('a','P.request'),message('b',null),message('c','C.report'),message('d','P.control')])],'prose');
 assert.equal(gaps.length,0);
});

test('turns use human cohorts rather than treating every user-role message as a human',()=>{
 const leading=message('leading','C.report'),human=message('human','P.request',{role:'user',segment:'H-typed'}),agent=message('agent','C.input',{role:'user',segment:'AIUR'}),next=message('next','P.control',{role:'user',segment:'H-template'});
 const turns=turnPairs([leading,human,agent,next]);
 assert.equal(turns.length,3);
 assert.equal(turns[0].human,null);
 assert.equal(turns[1].human.id,'human');
 assert.deepEqual(turns[1].agent.map(m=>m.id),['agent']);
 assert.equal(turns[2].agent.length,0);
});

test('recurrence compares sets without ancestor double-counting or invented missing values',()=>{
 const multi=message('multi','C.report',{labels:[['C',1],['C.report',.95],['C.report.product',.9],['P',1],['P.request',.9]]});
 assert.equal(jaccard(multi,message('report','C.report')),.5);
 assert.equal(jaccard(multi,multi),1);
 assert.equal(jaccard(multi,message('other','P.frame')),0);
 assert.equal(jaccard(multi,message('root','C')),null);
 assert.equal(jaccard(message('a',null),message('b',null)),null);
});

test('shape calculations ignore unknown gaps and do not create transitions across them',()=>{
 const stats=shape([message('a','P.request'),message('b',null),message('c','C.report')]);
 assert.equal(stats.pairs,0);
 assert.equal(stats.switchRate,0);
 assert.equal(stats.labeled,2);
 assert.ok(stats.diversity>0&&stats.diversity<1);
 assert.equal(shape([message('a','C.report'),message('b','C.report')]).diversity,0);
 assert.equal(shape([]).labeled,0);
});

test('thinking scope and reader destinations preserve stable source identities',()=>{
 const prose=message('prose','P.request'),thinking=message('thinking','C.deliberation',{kind:'reasoning'}),c=conversation('conversation-test',[prose,thinking]);
 assert.deepEqual(scopedMessages(c,'prose').map(m=>m.id),['prose']);
 assert.equal(scopedMessages(c,'thinking').length,2);
 const url=new URL(messageURL(c,prose),'http://localhost');
 assert.equal(url.pathname,'/work/workspace-test');
 assert.equal(url.searchParams.get('conversation'),c.id);
 assert.equal(url.searchParams.get('reveal'),'message:prose');
 assert.equal(url.searchParams.get('reader_labels'),'1');
});

import {functionStates,functionRole,hierarchy,workflowSessions,collectStreams,stateRuns,observedEdges,statePatterns,branchContinuations,exchanges} from '../../../src/label-trajectories-model.ts';
const stream=(id,messages,root=id)=>({id,conversation:conversation(root,messages),session:{id,name:id},messages});

test('Sender and Receiver are independent accepted functions, not actor identities or competing argmax labels',()=>{
 const mixed=message('mixed','C.report',{role:'assistant',labels:[['P',.95],['P.request',.8],['P.request.work.review',.7],['C',1],['C.report',.99],['C.report.intent',.98]]});
 assert.deepEqual(functionStates(mixed),['P.request','C.report']);
 assert.deepEqual(functionStates(mixed,'label'),['P.request.work.review','C.report.intent']);
 assert.equal(functionRole('P.request'),'Sender');assert.equal(functionRole('C.feedback'),'Receiver');
 assert.deepEqual(functionStates(message('human','C.feedback.accept',{role:'user',segment:'H-typed'})),['C.feedback']);
 assert.equal(hierarchy('P.request.work.review',[{id:'P',name:'Pitcher'},{id:'P.request',name:'Request'},{id:'P.request.work',name:'Work'},{id:'P.request.work.review',name:'Review'}]).join(' / '),'Sender / Request / Work / Review');
});

test('nested streams never interleave and child-reader links use the retained conversation identity',()=>{
 const c=conversation('parent',[message('p','P.request',{session_id:'main'}),message('child','C.report',{session_id:'helper',conversation_id:'actual-child',work_id:'child-work'}),message('p2','C.report',{session_id:'main'})]);
 c.main_session='main';c.sessions=[{id:'main',name:'Main',parent_id:null},{id:'helper',name:'Helper',parent_id:'main'}];
 const all=collectStreams([c],'prose',true);assert.deepEqual(all.map(s=>s.messages.map(m=>m.id)),[['p','p2'],['child']]);
 assert.deepEqual(collectStreams([c],'prose',false).map(s=>s.id),['main']);
 assert.deepEqual(collectStreams([c,c],'prose',true).map(s=>s.id),['main','helper']);
 const url=new URL(messageURL(c,c.messages[1]),'http://localhost');assert.equal(url.pathname,'/work/child-work');assert.equal(url.searchParams.get('conversation'),'actual-child');
});

test('recorded descendants stay beside their parent while independent and unresolved branches survive',()=>{
 const c=conversation('tree',[]);c.main_session='main';
 c.sessions=[{id:'main',parent_id:null},{id:'child',parent_id:'main'},{id:'sibling',parent_id:'main'},{id:'grandchild',parent_id:'child'},{id:'orphan',parent_id:'unavailable'},{id:'cycle',parent_id:'cycle'}];
 assert.deepEqual(workflowSessions(c).map(s=>s.id),['main','child','grandchild','sibling','orphan','cycle']);
});

test('state compression preserves mixed functions and missing boundaries; edges count accepted incidences',()=>{
 const mixed=message('mixed','C.report',{labels:[['P.request',.8],['C.report',.99]]});
 const messages=[message('r1','C.report'),message('r2','C.report'),mixed,message('gap',null),message('after','P.control')];
 assert.deepEqual(stateRuns(messages,'family').map(r=>[r.key,r.length]),[['C.report',2],['P.request|C.report',1],[null,1],['P.control',1]]);
 const edges=observedEdges([stream('one',messages),stream('two',[message('other','C.input')])],'family','changes');
 assert.equal(edges.find(e=>e.from==='C.report'&&e.to==='P.request').count,1);
 assert.equal(edges.find(e=>e.from==='C.report'&&e.to==='C.report').count,1);
 assert.equal(edges.some(e=>e.to==='P.control'),false);
 assert.equal(edges.some(e=>e.from==='P.control'),false);
});

test('motifs rank unique workflows while preserving independent branch support and whole state signatures',()=>{
 const sequence=[message('a','P.request'),message('b','C.report'),message('c','P.control')];
 const result=statePatterns([stream('first',sequence,'same-root'),stream('second',sequence,'same-root')],'family','changes');
 assert.equal(result[0].roots.size,1);assert.equal(result[0].streamIds.size,2);assert.equal(result[0].occurrences,2);
 assert.deepEqual(result[0].states,['P.request','C.report','P.control']);
 assert.equal(statePatterns([stream('gap',[sequence[0],message('gap',null),sequence[1],sequence[2]])],'family','changes').length,0);
});

test('prefix paths distinguish captured ends from missing labels instead of skipping across unknown work',()=>{
 const a=message('a','P.request'),b=message('b','C.report'),c=message('c','P.control');
 const sources=[stream('known',[a,b]),stream('gap',[a,message('unknown',null),c]),stream('empty',[])];
 const root=branchContinuations(sources,'family','changes',[]);assert.equal(root.total,3);assert.equal(root.missing,1);
 const first=branchContinuations(sources,'family','changes',['P.request']);assert.equal(first.total,2);assert.equal(first.missing,1);assert.equal(first.children.length,1);assert.equal(first.children[0].key,'C.report');
 const end=branchContinuations(sources,'family','changes',['P.request','C.report']);assert.equal(end.ended,1);assert.equal(end.missing,0);
});

test('closed paths require a real return without crossing missing evidence',()=>{
 const a=message('a','P.request'),b=message('b','C.report'),again=message('c','P.request');
 const loops=statePatterns([stream('loop',[a,b,again])],'family','changes',true);
 assert.deepEqual(loops[0].states,['P.request','C.report','P.request']);
 assert.equal(statePatterns([stream('gap',[a,b,message('unknown',null),again])],'family','changes',true).length,0);
 assert.equal(statePatterns([stream('immediate-gap',[a,message('unknown',null),again])],'family','changes',true).length,0);
 assert.equal(statePatterns([stream('repeat',[b,b,b])],'family','changes',true).length,0);
});

test('an agent can initiate an exchange and human feedback can be its Receiver response',()=>{
 const agent=message('agent','P.request.input.permission'),human=message('human','C.input.permission.grant',{role:'user',segment:'H-typed'}),mixed=message('mixed','C.feedback',{labels:[['P.control',.9],['C.feedback',.8]]});
 const result=exchanges([agent,human,mixed]);assert.equal(result[0].sender.id,'agent');assert.deepEqual(result[0].receivers.map(m=>m.id),['human']);assert.equal(result[1].sender.id,'mixed');assert.deepEqual(result[1].receivers.map(m=>m.id),['mixed']);
});

import {stateName,stateRows,compareStates,phasePrevalence} from '../../../src/label-trajectories-model.ts';
import {identitySeats,coverageKind,coveragePartitions,reliefCounts,prefixTree} from '../../../src/label-shape-model.ts';

test('display order is root then families then descendants regardless of taxonomy registration order',()=>{
 const messages=['C.report.product','P.control.redirect','P.request.work.review','C','P','C.input.answer','P.frame.scope','C.deliberation.plan'].map((id,i)=>message('m'+i,id));
 const states=stateRows([stream('s',messages)],'label',messages.map(m=>({id:m.labels[0][0],name:'label'})).reverse());
 assert.deepEqual(states,['P','P.request.work.review','P.frame.scope','P.control.redirect','C','C.input.answer','C.deliberation.plan','C.report.product']);
 assert.equal(stateName('P'),'Sender Generic');assert.equal(stateName('C'),'Receiver Generic');
 assert.equal(compareStates('P.request.work','P.request.work.review')<0,true);
});

test('identity seats preserve authored caller, agent and tool identities independently of function',()=>{
 const human=message('h','C.feedback.accept',{role:'user',segment:'H-typed'}),caller=message('p','P.request',{role:'user',segment:'AIUR'}),unknown=message('u',null,{role:'user',segment:''}),agent=message('a','P.request'),tool=message('t','C.report',{role:'tool'});
 const seats=identitySeats([agent,human,tool,caller,unknown],{name:'Worker'});
 assert.deepEqual(seats.map(s=>[s.id,s.events.map(e=>[e.message.id,e.index])]),[['human',[['h',1]]],['agent-input',[['p',3]]],['incoming',[['u',4]]],['agent',[['a',0]]],['tool',[['t',2]]]]);
});

test('coverage partitions every recorded role completely without treating structural roles as function predictions',()=>{
 const samples=[message('p','P.request',{role:'user'}),message('c','C.feedback',{role:'user'}),message('both','P.request',{labels:[['P.request',.9],['C.report',.9]]}),message('none',null,{chunks:1}),message('gap',null),message('generic','C',{role:'tool'})];
 assert.deepEqual(samples.map(coverageKind),['sender','receiver','both','none','unlabeled','receiver']);
 const rows=coveragePartitions([stream('s',samples)]);
 assert.equal(rows.reduce((n,r)=>n+r.total,0),samples.length);
 for(const row of rows)assert.equal(row.parts.reduce((n,p)=>n+p.count,0),row.total);
 assert.equal(rows.find(r=>r.role==='incoming').parts.find(p=>p.kind==='receiver').examples[0].message.id,'c');
});

test('relief counts distinct accepted detail and keeps Report separate from unknown work',()=>{
 const m=message('mixed','P.request',{labels:[['P.request',.99],['P.request.work.review',.9],['C.report.product',.9],['C.deliberation.plan',.8]]});
 assert.deepEqual(reliefCounts(m,'label'),{sender:1,receiver:1,report:true,missing:false,none:false});
 assert.equal(reliefCounts(message('gap',null),'label').missing,true);
 assert.equal(reliefCounts(message('none',null,{chunks:1}),'label').none,true);
});

test('visible prefix generations preserve conditional branch support, terminals and gaps',()=>{
 const a=message('a','P.request'),b=message('b','C.report'),c=message('c','P.control');
 const inputs=[stream('ab',[a,b]),stream('ac',[a,c]),stream('gap',[a,message('gap',null)]),stream('end',[a])];
 const tree=prefixTree(inputs,'family','changes',[]),first=tree.children[0];
 assert.equal(tree.count,4);assert.equal(first.count,4);assert.equal(first.ended,1);assert.equal(first.missing,1);
 assert.deepEqual(first.children.map(c=>c.count),[1,1]);
 const focused=prefixTree(inputs,'family','changes',['P.request','C.report']);assert.equal(focused.count,1);assert.equal(focused.ended,1);assert.equal(focused.missing,0);
});


test('phase prevalence includes Generic root matches and retains unlabeled messages in its denominator',()=>{
 const bins=phasePrevalence([stream('roots',[message('p','P'),message('c','C'),message('gap',null)])],3);
 assert.equal(bins[0].values.P,1);assert.equal(bins[1].values.C,1);assert.equal(bins[2].support,1);assert.equal(Object.values(bins[2].values).reduce((a,b)=>a+b,0),0);
});


test('prefix browsing can expose every continuation beyond the compact preview',()=>{
 const sources=[stream('a',[message('a','P.request')]),stream('b',[message('b','C.report')]),stream('c',[message('c','P.control')])];
 const compact=prefixTree(sources,'family','changes',[],false,1),all=prefixTree(sources,'family','changes',[],true,1);
 assert.equal(compact.children.length,1);assert.equal(compact.omitted,2);assert.equal(all.children.length,3);assert.equal(all.omitted,0);
});

import {workflowClock,clockRange,timeLanes,noteConnections,scoreNotes,branchOutlines} from '../../../src/label-timeline-model.ts';

test('shared clocks preserve overlapping agent timestamps and separate untimed evidence without inventing call positions',()=>{
 const at=n=>new Date(Date.UTC(2026,0,1,0,0,n)).toISOString();
 const c=conversation('clock',[message('call','P.request',{session_id:'main',at:at(10),kind:'delegation'}),message('late','C.report',{session_id:'main',at:at(40)}),message('child','C.deliberation',{session_id:'child',at:at(12)}),message('gap','C.report',{session_id:'child',at:'invalid'}),message('sibling','C.report',{session_id:'sibling',at:at(11)})]);
 c.main_session='main';c.sessions=[{id:'main',name:'Main',parent_id:null,depth:0},{id:'child',name:'Child',parent_id:'main',depth:1,call_message_id:'call'},{id:'sibling',name:'Sibling',parent_id:'main',depth:1,call_message_id:null}];
 const clock=workflowClock(c,'prose');
 assert.deepEqual(clock.range,[Date.parse(at(10)),Date.parse(at(40))]);
 assert.equal(clock.events.find(e=>e.message.id==='child').at,Date.parse(at(12)));
 assert.deepEqual(clock.untimed.map(e=>e.message.id),['gap']);
 assert.equal(clock.links.length,1);assert.equal(clock.links[0].at,Date.parse(at(10)));assert.equal(clock.links[0].first.message.id,'child');
 assert.equal(timeLanes(clock).flatMap(l=>l.events).length,4);
 assert.deepEqual([...branchOutlines(c).values()],['1','1.1','1.2']);
 assert.deepEqual(workflowClock(c,'prose',false).events.map(e=>e.message.id),['call','late']);
});

test('clock windows clamp to actual evidence and invalid or disjoint windows restore the full workflow',()=>{
 assert.deepEqual(clockRange([10,40],[0,20]),[10,20]);
 assert.deepEqual(clockRange([10,40],[20,100]),[20,40]);
 for(const range of [null,[20,20],[40,10],[80,100],[NaN,30]])assert.deepEqual(clockRange([10,40],range),[10,40]);
 const empty=workflowClock({...conversation('empty',[]),main_session:'main',sessions:[{id:'main',name:'Main'}]},'prose');
 assert.equal(empty.events.length,0);assert.ok(empty.range[1]>empty.range[0]);
});

test('score chords preserve all leaves and compression keeps repeated original events accessible',()=>{
 const a=message('a','P.request',{labels:[['P.request.work.review',.8],['P.frame.constraint',.7],['C.deliberation.plan',.9]]}),b={...a,id:'b'},gap=message('gap',null),after={...a,id:'after'};
 const notes=scoreNotes([stream('s',[a,b,gap,after])],'label',true);
 assert.deepEqual(notes.map(n=>n.length),[2,1,1]);assert.deepEqual(notes[0].members.map(m=>m.id),['a','b']);
 assert.deepEqual(notes[0].ids,['P.request.work.review','P.frame.constraint','C.deliberation.plan']);
 assert.equal(scoreNotes([stream('s',[a,b])],'label',false).length,2);
 assert.equal(scoreNotes([stream('s',[a,b])],'label',true,[0,10]).length,0);
});

test('score succession links remain in branch order and stop at missing-label boundaries',()=>{
 const sender=message('sender','P.request'),receiver=message('receiver','C.report'),gap=message('gap',null),mixed=message('mixed','C.report',{labels:[['P.frame',.8],['C.report',.9]]});
 assert.deepEqual(noteConnections([sender,receiver]).map(l=>[l.from.id,l.to.id]),[['sender','receiver']]);
 assert.equal(noteConnections([sender,gap,receiver]).length,0);
 assert.deepEqual(noteConnections([sender,mixed,receiver]).map(l=>[l.from.id,l.to.id]),[['sender','mixed'],['mixed','receiver']]);
});

test('expandable phase frequencies roll descendants up once per message while keeping the full branch denominator',()=>{
 const m=message('multi','P.request',{labels:[['P.request.work.review',.9],['P.request.work.verify',.8],['C.report.intent',.8]]}),unknown=message('unknown',null);
 const bins=phasePrevalence([stream('one',[m,unknown]),stream('two',[message('plain','C.report.product')])],1);
 assert.equal(bins[0].values['P.request'],.25);assert.equal(bins[0].values['P.request.work'],.25);
 assert.equal(bins[0].values['P.request.work.review'],.25);assert.equal(bins[0].values['P.request.work.verify'],.25);
 assert.equal(bins[0].values['C.report'],.75);assert.equal(bins[0].values.P,0);
});

import {messageAllocation,frequencyTable,frequencyMatches,tagPrefixPaths,tagContinuations,conversationRibbon,turnPosition,capturedSpan} from '../../../src/label-discovery-model.ts';
const discoveryTaxonomy=['P','P.request','P.request.work','P.request.work.review','P.request.work.verify','C','C.report','C.report.progress'].map(id=>({id,name:id,definition:id}));
test('additive message shares conserve mass through every taxonomy parent, including Other and Uncategorized',()=>{
 const mixed=message('mixed','P.request',{labels:[['P.request.work.review',.9],['P.request.work.verify',.8],['C.report',.95]]});
 const source=stream('s',[mixed,message('generic','P'),message('unlabeled',null),message('none',null,{chunks:1})]);
 const allocations=messageAllocation(mixed,discoveryTaxonomy);assert.equal(allocations.reduce((n,a)=>n+a.weight,0),1);assert.deepEqual(allocations.map(a=>a.weight),[.25,.25,.5]);
 const table=frequencyTable([source],discoveryTaxonomy,'phase');
 const walk=node=>{if(!node.children.length)return;for(let i=0;i<table.columns.length;i++){const parent=table.counts.get(node.id)?.[i]??0,sum=node.children.reduce((n,c)=>n+(table.counts.get(c.id)?.[i]??0),0);assert.ok(Math.abs(parent-sum)<1e-10,node.id+' children conserve mass');}node.children.forEach(walk);};walk(table.tree);
 assert.equal(table.counts.get('P~other').reduce((a,b)=>a+b),1);assert.equal(table.counts.get('C.report~other').reduce((a,b)=>a+b),.5);assert.equal(table.counts.get('uncategorized').reduce((a,b)=>a+b),2);
 assert.deepEqual(frequencyMatches([source],'phase','P.request.work.review','').map(e=>e.message.id),['mixed']);
 assert.deepEqual(frequencyMatches([source],'phase','C.report~other','').map(e=>e.message.id),['mixed']);
});
test('actor and model breakdowns retain unknown metadata and raw matching events',()=>{
 const incoming=message('u','P.request',{role:'user'}),work=message('a','C.report',{generation_model:'actual-generation-model'}),tool=message('t',null,{role:'tool'}),root=stream('root',[incoming,work,tool]);root.session.model='session-model';root.session.parent_id=null;
 const child=stream('child',[message('c','C.report')]);child.session.parent_id='root';child.session.model='child-model';
 const actor=frequencyTable([root,child],discoveryTaxonomy,'actor');assert.deepEqual(actor.columns,['User role','Main agent','Subagent','Tool']);assert.deepEqual(actor.totals,[1,1,1,1]);
 const model=frequencyTable([root,child],discoveryTaxonomy,'model');assert.ok(model.columns.includes('actual-generation-model'));assert.ok(model.columns.includes('child-model'));assert.equal(model.totals[model.columns.indexOf('Not recorded')],2);
 assert.equal(frequencyMatches([root,child],'actor','C','Subagent')[0].message.id,'c');
});
test('prefix tag filters include coexisting tags and descendants without crossing an unknown boundary',()=>{
 const mixed=message('ab','P.request',{labels:[['P.request.work.review',.8],['C.report.progress',.9]]}),single=message('a','P.request.work.review'),after=message('next','C.report.progress');
 const inputs=[stream('mixed',[mixed,after]),stream('single',[single,after]),stream('gap',[single,message('gap',null),after])];
 assert.equal(tagPrefixPaths(inputs,'label','messages',['P.request']).length,3);
 assert.equal(tagPrefixPaths(inputs,'label','messages',['P.request','C.report']).length,2);
 assert.equal(tagPrefixPaths(inputs,'label','messages',['P.request|C.report']).length,1);
 const next=tagContinuations(inputs,'label','messages',[]);assert.equal(next.tags.get('P'),3);assert.equal(next.tags.get('C'),1);
});
test('compact ribbons bound their size while preserving volume and captured nesting evidence',()=>{
 const c=conversation('ribbon',Array.from({length:201},(_,i)=>message(String(i),i%2?'P.request':'C.report',{session_id:i%3?'main':'child',at:new Date(i*1000).toISOString()})));c.main_session='main';c.sessions=[{id:'main',depth:0},{id:'child',depth:2}];
 const tokens=conversationRibbon(c,12);assert.ok(tokens.length<=12);assert.equal(tokens.reduce((n,t)=>n+t.count,0),201);assert.equal(Math.max(...tokens.map(t=>t.depth)),2);assert.equal(capturedSpan(c),200000);
 const s=stream('s',[message('first','P.request',{role:'user'}),message('a','C.report'),message('second','P.request',{role:'user'}),message('b','C.report')]);assert.deepEqual(turnPosition(s,s.messages[3]),{at:2,total:2,index:4,messages:4});
});

import {sessionRibbonTree,ribbonCounts,ribbonEventCount,ribbonHits,ribbonLayers,ribbonToolTracks,ribbonPointerHits,ribbonSourceURL} from '../../../src/label-session-ribbon-model.ts';
test('session ribbons partition thinking before function roles and keep every coexisting label down to a single observation',()=>{
 const at=n=>new Date(n).toISOString(),normal=message('plain','C.report.product',{at:at(10)}),thinking=message('thought','C.report',{kind:'reasoning',at:at(20),labels:[['C.report.progress',.9],['P.request.work.review',.8]]}),unknown=message('unknown',null,{at:at(30)}),untimed=message('untimed','C.report');
 const tree=sessionRibbonTree([normal,thinking,unknown,untimed],[],[],discoveryTaxonomy,[0,40]);
 assert.deepEqual(ribbonCounts(tree.items),{messages:3,thinking:1,activity:0,inferred:0});
 assert.equal(ribbonEventCount(tree.items),3);
 const sharedTool={...tree.items[0],id:'shared-tool',message:undefined,mark:{message_id:'plain',count:1}};
 const distinctTool={...sharedTool,id:'new-tool',mark:{message_id:'tool-message',count:1}};
 assert.equal(ribbonEventCount([...tree.items,sharedTool,distinctTool]),4,'labels, origin runs and tools sharing a message do not inflate the event total');
 const labels=tree.children.find(n=>n.id==='labels'),thoughts=labels.children.find(n=>n.id==='labels:thinking');
 assert.ok(thoughts);assert.equal(ribbonCounts(thoughts.items).messages,1);
 assert.deepEqual(thoughts.children.map(n=>n.title),['Sender','Receiver']);
 assert.ok(labels.children.filter(n=>n!==thoughts).every(n=>n.items.every(item=>item.message.id!=='thought')));
 const visit=node=>{if(!node.children.length){assert.equal(node.items.length,1);return;}assert.deepEqual(node.children.flatMap(n=>n.items).map(i=>i.id).sort(),node.items.map(i=>i.id).sort());node.children.forEach(visit);};visit(tree);
 const singleRole=sessionRibbonTree([{...thinking,labels:[['C.report.progress',.9]]}],[],[],discoveryTaxonomy,[0,40]).children[0].children[0];assert.equal(singleRole.title,'Thinking · Receiver');assert.ok(singleRole.children.every(n=>n.title!=='Receiver'));visit(singleRole);
 const generic=sessionRibbonTree([message('generic','P',{at:at(10)}),message('parent','P.request',{at:at(20)})],[],[],discoveryTaxonomy,[0,40]);visit(generic);
});
test('ribbon hit testing includes smaller covered symbols and activity observations without inventing durations',()=>{
 const marks=[{start:0,end:100,end_known:true,name:'Read files',category:'read',count:3,error:false},{start:50,end:90,end_known:false,name:'Recorded delegation',category:'delegate',count:1,error:false}];
 const tree=sessionRibbonTree([message('point','P.request',{at:new Date(50).toISOString()})],marks,[{start:0,end:110}],[],[0,100]);
 const hits=ribbonHits(tree.items,50,1);assert.equal(hits.length,4);assert.equal(hits.find(i=>i.mark?.category==='delegate').end,50);
 const layers=ribbonLayers(hits);assert.equal(layers[0].item.message.id,'point');assert.ok(layers.slice(1).every(layer=>!layer.item.message));assert.deepEqual(new Set(layers.map(layer=>layer.item.id)),new Set(hits.map(item=>item.id)));
 assert.equal(ribbonCounts(tree.items).activity,4);assert.equal(ribbonHits(tree.items,95,1).length,3);
 assert.equal(sessionRibbonTree([],marks,[],[],[101,200]).items.length,0);
});
test('tool tracks separate overlapping calls, reuse ended tracks and preserve point observations',()=>{
 const marks=[{start:0,end:10,end_known:true,name:'long',category:'run',count:1,error:false,tool_id:'long',preview:'npm test'},{start:1,end:4,end_known:true,name:'first',category:'read',count:1,error:false,tool_id:'first'},{start:2,end:3,end_known:true,name:'second',category:'read',count:1,error:false,tool_id:'second'},{start:4,end:7,end_known:true,name:'reuse',category:'read',count:1,error:false,tool_id:'reuse'},{start:11,end:12,end_known:true,name:'solo',category:'read',count:1,error:false,tool_id:'solo'}];
 const items=sessionRibbonTree([],marks,[],[],[0,20]).items,tracks=ribbonToolTracks(items),byName=name=>items.find(item=>item.title===name);
 for(const name of ['long','first','second'])assert.equal(tracks.get(byName(name).id).total,3);
 assert.equal(new Set(['long','first','second'].map(name=>tracks.get(byName(name).id).slot)).size,3);
 assert.equal(tracks.get(byName('first').id).slot,tracks.get(byName('reuse').id).slot);
 assert.equal(tracks.get(byName('solo').id).total,1);
 assert.deepEqual(ribbonToolTracks([...items].reverse()),tracks);
 const layers=ribbonLayers(items);
 for(const name of ['long','first','second']){
  const layer=layers.find(layer=>layer.item.title===name),hits=ribbonPointerHits(items,2.5,.01,layer.y+layer.height/2,1);
  assert.deepEqual(hits.map(item=>item.title),[name]);
 }
 const long=layers.find(layer=>layer.item.title==='long');assert.equal(ribbonPointerHits(items,2.5,.01,long.y+long.height/2,1)[0].mark.preview,'npm test');
 const points=sessionRibbonTree([],['a','b','c'].map(tool_id=>({...marks[0],tool_id,start:5,end:99,end_known:false})),[],[],[0,10]).items;
 assert.ok(points.every(item=>item.end===item.start));
 assert.equal(new Set([...ribbonToolTracks(points).values()].map(track=>track.slot)).size,3);
 assert.equal(ribbonHits(points,6,.01).length,0);
 assert.equal(sessionRibbonTree([],points.map(item=>item.mark),[],[],[6,10]).items.length,0);
});
test('compressed fingerprint segments preserve every original message for informative inspection',()=>{
 const events=Array.from({length:30},(_,i)=>message(String(i),i%2?'P.request.work.review':'C.report.progress',{at:i===29?null:new Date(i*1000).toISOString(),kind:i%3?'message':'reasoning'})),c=conversation('inspect',events);
 const tokens=conversationRibbon(c,4);
 assert.deepEqual(tokens.flatMap(t=>t.messages),events);assert.ok(tokens.every(t=>t.count===t.messages.length));
 assert.equal(tokens.flatMap(t=>t.messages).filter(m=>m.kind==='reasoning').length,10);
});

import {labelIntervals,recordedToolMarks,activityCategory} from '../../../src/label-session-ribbon-model.ts';
import {dashboardStreams,labelTrend,normalizeLabelChart,normalizeLabelDates,normalizeLabelSelection,defaultLabelChart} from '../../../src/label-dashboard-model.ts';
test('label intervals reach between observations, clip at tool boundaries and leave idle gaps blank',()=>{
 const at=n=>new Date(n).toISOString(),messages=[message('a','P.request',{at:at(0)}),message('thought','C.deliberation',{kind:'thinking',at:at(5000)}),message('last','C.report',{at:at(9000)}),message('idle','C.report',{at:at(3600000)})];
 const tools=recordedToolMarks([{id:'t',session_id:'s',message_id:'call',name:'exec_command',category:'shell',status:'ok',start:at(1000),end:at(2000)}]);
 const spans=labelIntervals(messages,tools);assert.deepEqual(spans.slice(0,3).map(s=>[s.start,s.end,s.attribution]),[[0,1000,'following'],[2000,5000,'preceding'],[9000,9000,'instant']]);
 const visible=sessionRibbonTree(messages,tools,[],[],[2500,4500]);assert.ok(visible.items.some(i=>i.message?.id==='thought'));
 assert.equal(tools[0].end_known,true);assert.equal(activityCategory({name:'wait_agent',category:'agent'}),'wait');assert.equal(activityCategory({name:'write_stdin',category:'shell'}),'poll');assert.equal(activityCategory({name:'exec_command',category:'shell',command_category:'git'}),'git');
});
test('fingerprint crests count overlapping captured branches separately from nesting depth',()=>{
 const at=n=>new Date(n).toISOString(),c=conversation('parallel',[message('main-a','C.report',{session_id:'main',at:at(0)}),message('main-b','C.report',{session_id:'main',at:at(90)}),...['a','b','c'].flatMap((sid,j)=>[message(sid+'-start','C.report',{session_id:sid,at:at(10+j*10)}),message(sid+'-end','C.report',{session_id:sid,at:at(60)})])]);
 c.main_session='main';c.sessions=[{id:'main',depth:0},...['a','b','c'].map(id=>({id,parent_id:'main',depth:1}))];
 const tokens=conversationRibbon(c,40);assert.deepEqual([...new Set(tokens.map(t=>t.parallelism))].sort(),[0,1,2,3]);assert.equal(Math.max(...tokens.map(t=>t.depth)),1);
});
test('label trends pool percentages across smoothing intervals without renormalizing coexisting labels',()=>{
 const start=+new Date(2026,0,2),hour=3600000,m=(id,n,labels)=>message(id,labels[0],{at:new Date(start+n*hour).toISOString(),labels:labels.map(id=>[id,.9])});
 const source=stream('frequency',[m('a',0,['P.request.work.review','P.request.work.verify','C.report']),m('b',1,['C.report']),message('unknown',null,{at:new Date(start+hour).toISOString()})]);
 const config={...defaultLabelChart,period:'hour',smoothing:2},dates={from:new Date(start+hour).toISOString(),to:new Date(start+2*hour).toISOString()},data=labelTrend([source],config,dates,discoveryTaxonomy);
 assert.equal(data.buckets.length,1);assert.equal(data.buckets[0].values['P.request'],1/3);assert.equal(data.buckets[0].values['C.report'],2/3);assert.equal(data.buckets[0].values['?'],1/3);
 const counts=labelTrend([source],{...config,measure:'count'},dates,discoveryTaxonomy);assert.equal(counts.buckets[0].values['C.report'],1);assert.equal(counts.buckets[0].values['P.request'],.5);
});
test('dashboard narrowing preserves original phases and turn positions, with explicit missing model metadata',()=>{
 const a=message('a','P.request',{role:'user',at:new Date(0).toISOString()}),b=message('b','C.report',{at:new Date(1000).toISOString(),generation_model:'model-a'}),c=message('c','P.request',{role:'user',at:new Date(2000).toISOString()}),source=stream('s',[a,b,c]);
 const filtered=dashboardStreams([source],'model-a');assert.equal(filtered[0].messages.length,1);assert.deepEqual(turnPosition(filtered[0],b),{at:1,total:2,index:2,messages:3});
 const table=frequencyTable(filtered,discoveryTaxonomy,'phase');assert.equal(table.totals[2],1);assert.equal(dashboardStreams([source],'Not recorded')[0].messages.length,2);
 assert.equal(dashboardStreams([source],'',{from:new Date(1000).toISOString(),to:new Date(2000).toISOString()})[0].messages[0].id,'b');
 assert.deepEqual(normalizeLabelChart({period:'invalid',smoothing:1,days:-1}),defaultLabelChart);assert.deepEqual(normalizeLabelSelection({values:[true,'C.report'],excluded:'yes'}),{values:['C.report'],excluded:false});assert.equal(normalizeLabelDates({from:'broken'}).from,undefined);
});

import {parallelThreads} from '../../../src/label-discovery-model.ts';
test('parallel strands preserve large exact counts and separate zero-overlap intervals',()=>{
 const segments=levels=>levels.map((parallelism,i)=>({x:i*10,width:10,token:{parallelism}}));
 assert.deepEqual(parallelThreads(segments([0,0,0])),{levels:[],peak:0,overflow:0});
 const joined=parallelThreads(segments([1,2,0,3,11]));assert.equal(joined.peak,11);assert.equal(joined.overflow,5);assert.equal(joined.levels.length,6);
 assert.deepEqual(joined.levels[0].groups,[{start:0,end:20},{start:30,end:50}]);
 assert.deepEqual(joined.levels[5].groups,[{start:40,end:50}]);
});

test('custom time boundaries exclude earlier messages in a partial first bucket while retaining preceding smoothing history',()=>{
 const start=+new Date(2026,0,2),hour=3600000,source=stream('partial',[message('history','C.report',{at:new Date(start-hour).toISOString()}),message('before','P.request',{at:new Date(start+1000).toISOString()}),message('inside','C.report',{at:new Date(start+40*60000).toISOString()}),message('end','P.request',{at:new Date(start+hour).toISOString()})]),dates={from:new Date(start+30*60000).toISOString(),to:new Date(start+hour).toISOString()},chart={...defaultLabelChart,period:'hour',measure:'count'};
 const plain=labelTrend([source],chart,dates,[]);assert.equal(plain.buckets.length,1);assert.equal(plain.buckets[0].values['C.report'],1);assert.ok(!plain.series.some(s=>s.key==='P.request'));
 const smoothed=labelTrend([source],{...chart,smoothing:2},dates,[]);assert.equal(smoothed.buckets[0].values['C.report'],1);assert.ok(!smoothed.series.some(s=>s.key==='P.request'));
});

import {audienceStreams,labelTrendMatches,labelDrillURL,normalizeLabelAudience} from '../../../src/label-dashboard-model.ts';
import {stateColor} from '../../../src/label-trajectories-model.ts';
test('authorship populations keep human words, agent output and retained thinking distinct from function roles',()=>{
 const messages=[message('human','C.feedback',{role:'user',segment:'H-feedback'}),message('typed','P.request',{role:'user',segment:'H-typed+ctx'}),message('template','P.frame',{role:'user',segment:'H-template'}),message('supplied','P.request',{role:'user',segment:'AIUR'}),message('unknown','P.request',{role:'user',segment:''}),message('output','P.request'),message('thought','C.report',{kind:'reasoning'}),message('tool','C.report',{role:'tool'})];
 const input=[stream('s',messages)],ids=audience=>audienceStreams(input,audience).flatMap(s=>s.messages.map(m=>m.id));
 assert.deepEqual(ids('human'),['human','typed']);assert.deepEqual(ids('machine'),['output']);assert.deepEqual(ids('thinking'),['thought']);assert.equal(ids('all').length,8);
 assert.equal(audienceStreams(input,'thinking')[0].sourceMessages.length,8);assert.equal(normalizeLabelAudience('invalid'),'all');
});
test('specific report labels retain meaning and unknown descendants inherit their closest chromatic parent',()=>{
 for(const id of ['C.report.finding','C.report.progress','C.report.product'])assert.notEqual(stateColor(id),stateColor('?'));
 assert.notEqual(stateColor('C.report.finding'),stateColor('C.report.progress'));assert.equal(stateColor('C.report.finding.future'),stateColor('C.report.finding'));
 const c=conversation('specific',[message('finding','C.report.finding'),message('product','C.report.product')]);assert.deepEqual(conversationRibbon(c).flatMap(t=>t.ids),['C.report.finding','C.report.product']);
});
test('rarity layering preserves every observation and hit while drawing common modes beneath rare modes and tools',()=>{
 const at=n=>new Date(n).toISOString(),messages=[message('a','C.report',{at:at(0)}),message('b','C.report',{at:at(10),labels:[['C.report',.9],['C.difficulty.blocker',.8]]})],marks=[{start:0,end:20,end_known:true,name:'Read',category:'read',count:1,error:false}];
 const tree=sessionRibbonTree(messages,marks,[],[],[0,20]),layers=ribbonLayers(tree.items),common=layers.filter(l=>l.item.label==='C.report'),rare=layers.find(l=>l.item.label==='C.difficulty.blocker');
 assert.ok(common.every(l=>l.height>rare.height));assert.ok(layers.indexOf(rare)>layers.indexOf(common.at(-1)));assert.ok(layers.at(-1).item.mark);assert.equal(layers.length,tree.items.length);assert.ok(ribbonHits(tree.items,10,1).some(i=>i.label==='C.difficulty.blocker'));
});
test('label drilldown retains authorship, filters and exact partial intervals without shrinking the denominator',()=>{
 const start=+new Date(2026,0,2),hour=3600000,m=(id,n,label)=>message(id,label,{at:new Date(start+n*hour).toISOString()}),source=stream('s',[m('review',0,'P.request.work.review'),m('report',0,'C.report.finding'),m('verify',1,'P.request.work.verify')]),chart={...defaultLabelChart,period:'hour',measure:'percent',resolution:'label',scale:'logarithmic'};
 const data=labelTrend([source],chart,{},[],'P.request');assert.ok(data.series.every(s=>s.key.startsWith('P.request.')));assert.equal(data.buckets[0].values['P.request.work.review'],.5);
 assert.deepEqual(labelTrendMatches([source],chart,data.series,{values:[],excluded:false},'P.request').map(e=>e.message.id),['review','verify']);
 const params=new URLSearchParams({label_audience:'thinking',label_repository:'example',label_model:'m',chart_pins:'{}'}),dates={from:new Date(start+hour/2).toISOString()},url=new URL(labelDrillURL(params,{from:new Date(start),to:new Date(start+2*hour),rolling:true},'P.request',{...chart,smoothing:3},dates),'http://localhost');
 assert.equal(url.searchParams.get('label_audience'),'thinking');assert.equal(url.searchParams.get('label_repository'),'example');assert.equal(url.searchParams.get('label_model'),'m');assert.equal(url.searchParams.get('label_focus'),'P.request');const drillDates=JSON.parse(url.searchParams.get('label_dates'));assert.equal(drillDates.from,new Date(start).toISOString());assert.deepEqual(drillDates.omit,[{from:new Date(start).toISOString(),to:dates.from}]);assert.deepEqual(dashboardStreams([source],'',drillDates)[0].messages.map(m=>m.id),['verify']);assert.equal(JSON.parse(url.searchParams.get('label_chart')).smoothing,0);assert.equal(JSON.parse(url.searchParams.get('label_chart')).scale,'logarithmic');assert.ok(!url.searchParams.has('chart_pins'));
});

test('smoothed inspection keeps preceding full buckets and omits the unused part of a custom first bucket',()=>{
 const start=+new Date(2026,0,2),hour=3600000,source=stream('history',[message('previous','C.report.finding',{at:new Date(start-hour).toISOString()}),message('clipped','C.report.finding',{at:new Date(start+10*60000).toISOString()}),message('included','C.report.finding',{at:new Date(start+40*60000).toISOString()})]),chart={...defaultLabelChart,period:'hour',resolution:'label',measure:'count',smoothing:2},dates={from:new Date(start+30*60000).toISOString(),to:new Date(start+hour).toISOString()};
 const original=labelTrend([source],chart,dates,[]);assert.equal(original.buckets[0].values['C.report.finding'],1);
 const href=labelDrillURL(new URLSearchParams(),{from:new Date(start-hour),to:new Date(start+hour),rolling:true},'C.report',chart,dates),next=JSON.parse(new URL(href,'http://localhost').searchParams.get('label_dates'));
 assert.deepEqual(dashboardStreams([source],'',next)[0].messages.map(m=>m.id),['previous','included']);
 const detailed=labelTrend([source],{...chart,smoothing:0},next,[],'C.report');assert.equal(detailed.buckets.reduce((n,b)=>n+(b.values['C.report.finding']??0),0),2);
});

import {ribbonSegment} from '../../../src/label-discovery-model.ts';
test('ribbon stripes retain every deepest accepted label including reports below the previous top-five cutoff',()=>{
 const labels=['P.request.work.implement','P.frame.scope','P.control.redirect','C.input.answer','C.feedback.accept','C.deliberation.plan','C.report.finding'],messages=labels.flatMap((id,i)=>Array.from({length:7-i},(_,j)=>message(`${i}:${j}`,id))),token=conversationRibbon(conversation('all-labels',messages),1)[0],segment=ribbonSegment(token.messages,[]);
 assert.deepEqual(new Set(segment.slices.map(s=>s.id)),new Set(labels));assert.ok(segment.slices.find(s=>s.id==='C.report.finding').share>0);assert.ok(Math.abs(segment.slices.reduce((n,s)=>n+s.share,0)-1)<1e-12);
 const deepest=ribbonSegment([message('ancestor','C.report',{labels:[['C.report',1],['C.report.finding',.3]]})],[]);assert.deepEqual(deepest.slices.map(s=>s.id),['C.report.finding']);
});
test('ribbon hover hierarchy and colored stripes use the same additive shares as Phase Flow',()=>{
 const messages=[message('mixed','P.request',{labels:[['P.request.work.review',.9],['P.request.work.verify',.8],['C.report.finding',.7]]}),message('parent','C.report'),message('unlabeled',null),message('no-match',null,{chunks:1})],taxonomy=[...discoveryTaxonomy,{id:'C.report.finding',name:'Finding',definition:''}],segment=ribbonSegment(messages,taxonomy),rows=new Map(segment.rows.map(r=>[r.id,r]));
 assert.equal(rows.get('all').share,1);assert.equal(rows.get('P').share,.125);assert.equal(rows.get('C').share,.375);assert.equal(rows.get('uncategorized').share,.5);assert.equal(rows.get('C.report~other').share,.25);assert.equal(rows.get('C.report~other').colorID,'C.report');
 assert.equal(rows.get('C.report.finding').parent,'C.report');assert.equal(rows.get('P.request.work.review').parent,'P.request.work');assert.ok(rows.get('P.request.work.review').depth>rows.get('P.request').depth);
 for(const row of segment.rows){const children=segment.rows.filter(child=>child.parent===row.id);if(children.length)assert.ok(Math.abs(children.reduce((n,c)=>n+c.weight,0)-row.weight)<1e-12);}
 const table=frequencyTable([stream('s',messages)],taxonomy,'phase');for(const row of segment.rows)assert.ok(Math.abs((table.counts.get(row.id)??[]).reduce((a,b)=>a+b,0)-row.weight)<1e-12);
 assert.deepEqual(ribbonSegment([],taxonomy),{rows:[],slices:[]});
});

import {parallelEnvelopes,ribbonMessageTitle} from '../../../src/label-discovery-model.ts';
test('overlap envelopes are symmetric, grow beyond three branches and break at zero overlap',()=>{
 const segments=levels=>levels.map((parallelism,i)=>({x:i*10,width:10,token:{parallelism}}));
 assert.deepEqual(parallelEnvelopes(segments([0,0])),[]);
 const shapes=parallelEnvelopes(segments([0,1,3,16,0,64]));assert.deepEqual(shapes.map(s=>s.peak),[16,64]);
 const heights=shapes[0].points.map(p=>p.lower-p.upper);assert.ok(heights[0]<heights[1]&&heights[1]<heights[2]);assert.ok(shapes[1].points[0].lower-shapes[1].points[0].upper>heights[2]);
 for(const shape of shapes){assert.ok(shape.path.endsWith('Z')&&!shape.path.includes('NaN'));for(const point of shape.points){assert.ok(point.upper>0&&point.lower<38);assert.ok(Math.abs((point.upper+point.lower)/2-19)<1e-12);}}
});
test('fingerprint captions distinguish a single message from an inclusive message range',()=>{
 assert.equal(ribbonMessageTitle(8,8,9),'Message 8 / 9');assert.equal(ribbonMessageTitle(1,1,1),'Message 1 / 1');assert.equal(ribbonMessageTitle(8,9,20),'Messages 8–9 / 20');
});

import {splitTimelineCallers,authorshipMix,authorshipSegments,authorshipExcerpt} from '../../../src/timeline-caller-model.ts';
import {normalizeTimelineColumns,timelineColumnWidth,timelineColumnLayout} from '../../../src/timeline-columns.tsx';
test('timeline Caller separates human and supplied input without reclassifying subagent assignments',()=>{
 const human=message('human','P.request',{role:'user',session_id:'main',authorship:[{category:'typed',bytes:40},{category:'pasted',bytes:60}]}),unknown=message('unknown','P.request',{role:'user',session_id:'main'}),automated=message('assignment','P.request',{role:'user',session_id:'child',segment:'H-typed',authorship:[{category:'automated',bytes:100}]}),reply=message('reply','C.report',{session_id:'main'}),childHuman=message('child-human','P.control',{role:'user',session_id:'child',authorship:[{category:'typed',bytes:10}]});
 const externalAutomation={...automated,id:'external-automation',session_id:'main'};
 const source=new Map([['main',[human,unknown,reply,externalAutomation]],['child',[automated,childHuman,human]]]);
 const split=splitTimelineCallers(source,[{id:'main',depth:0},{id:'child',depth:1}]);
 assert.deepEqual(new Set(split.caller.map(m=>m.id)),new Set(['human','unknown','child-human','external-automation']));
 assert.deepEqual(split.messages.get('main').map(m=>m.id),['reply']);
 assert.deepEqual(split.messages.get('child').map(m=>m.id),['assignment']);
 assert.equal(source.get('main').length,4);
 assert.equal(authorshipMix(split.caller.find(m=>m.id==='external-automation'))[0].category,'automated');
 assert.deepEqual(authorshipMix(unknown),[{category:'unknown',bytes:1}]);
 assert.equal(split.caller.find(m=>m.id==='human').session_id,'main');
});
test('Caller origin shares preserve mixed text and use recipient observations as clock boundaries',()=>{
 const prompt=message('prompt','P.request',{role:'user',at:new Date(1000).toISOString(),authorship:[{category:'typed',bytes:20},{category:'quoted',bytes:60},{category:'typed',bytes:20}]}),response=message('response','C.report',{at:new Date(5000).toISOString()});
 const tree=sessionRibbonTree([prompt],[],[],[],[0,6000],true,true,[prompt,response]);
 assert.equal(ribbonCounts(tree.items).messages,1);
 assert.equal(tree.items.find(item=>item.label).end,5000);
 const origins=tree.items.filter(item=>item.authorship);
 assert.deepEqual(origins.map(item=>[item.authorship,item.start,item.end]),[['typed',1000,1800],['quoted',1800,4200],['typed',4200,5000]]);
 assert.ok(origins.every(item=>item.color===tree.items.find(item=>item.label).color));
 assert.equal(new Set(origins.map(item=>item.id)).size,3);
 const origin=tree.children.find(node=>node.id==='authorship');
 assert.equal(origin.children.length,1);
 assert.equal(origin.children[0].children.length,3);
 assert.deepEqual(ribbonHits(origins,3000,0).map(item=>item.authorship),['quoted']);
 assert.deepEqual(ribbonHits(origins,1800,50).map(item=>item.authorship),['quoted']);
 assert.deepEqual(ribbonHits(origins,4199,50).map(item=>item.authorship),['quoted']);
 assert.deepEqual(ribbonHits(origins,4200,50).map(item=>item.authorship),['typed']);
 const zoomed=sessionRibbonTree([prompt],[],[],[],[2000,4000],true,true,[prompt,response]);
 assert.deepEqual(zoomed.items.filter(item=>item.authorship).map(item=>[item.start,item.end]),origins.map(item=>[item.start,item.end]));
 assert.deepEqual(authorshipMix(prompt),[{category:'typed',bytes:40},{category:'quoted',bytes:60}]);
});
test('Caller text positions retain repeated origins and safely normalize missing or invalid composition',()=>{
 const prompt=message('mixed','P.request',{authorship:[{category:'typed',bytes:2},{category:'quoted',bytes:6},{category:'typed',bytes:2},{category:'pasted',bytes:NaN},{category:'pasted',bytes:-1}]});
 assert.deepEqual(authorshipSegments(prompt).map(part=>[part.category,part.start,part.end]),[['typed',0,.2],['quoted',.2,.8],['typed',.8,1]]);
 assert.deepEqual(authorshipSegments(message('unknown','P.request')),[{category:'unknown',bytes:1,start:0,end:1}]);
 assert.equal(authorshipSegments({...prompt,authorship:[{category:'unrecognized',bytes:10}]})[0].category,'unknown');
 const simultaneous={...prompt,at:new Date(1000).toISOString(),labels:[['P.request',.9],['P.control',.8]]};
 const tree=sessionRibbonTree([simultaneous],[],[],[],[0,6000],true,true);
 const parts=tree.children.find(node=>node.id==='authorship').children[0].children;
 assert.equal(parts.length,3);
 assert.ok(parts.every(part=>part.items.length===2&&part.items.every(item=>item.start===1000&&item.end===1000)));
 assert.deepEqual(parts.map(part=>part.items[0].composition),[{start:0,end:.2},{start:.2,end:.8},{start:.8,end:1}]);
});
test('Caller span inspection uses the actual run excerpt even after the message preview ends',()=>{
 const prompt=message('text','P.request',{role:'user',at:new Date(1000).toISOString(),text:'A short preview',truncated:true,authorship:[{category:'harness',bytes:2000,text:'Retained harness context',truncated:true},{category:'typed',bytes:10,text:'Ship it!é',truncated:false},{category:'attachment',bytes:100,text:'[image]',truncated:false}]});
 const segments=authorshipSegments(prompt);
 assert.deepEqual(authorshipExcerpt(prompt,segments[1]),{text:'Ship it!é',truncated:false});
 const response=message('reply','C.report',{at:new Date(5000).toISOString()}),tree=sessionRibbonTree([prompt],[],[],[],[0,6000],true,true,[prompt,response]);
 const typed=tree.items.find(item=>item.authorship==='typed');
 assert.deepEqual(typed.excerpt,{text:'Ship it!é',truncated:false});
 const full=message('utf8','P.request',{text:'é😎done',authorship:[{category:'pasted',bytes:6},{category:'typed',bytes:4}]});
 assert.deepEqual(authorshipSegments(full).map(part=>authorshipExcerpt(full,part)),[{text:'é😎',truncated:false},{text:'done',truncated:false}]);
 const missing={...full,text:'é',truncated:true};
 assert.equal(authorshipExcerpt(missing,authorshipSegments(missing)[1]),null);
});
test('timeline column widths are bounded, portable and preserve automatic chart width',()=>{
 const columns=normalizeTimelineColumns({execution:260,events:200,time:880});
 const compact=timelineColumnLayout(columns,true);
 assert.ok(compact.events<columns.events);assert.equal(compact.execution,columns.execution);assert.equal(compact.time,columns.time);
 assert.deepEqual(timelineColumnLayout(columns,false),columns,'reopening event details restores the saved widths');
 assert.deepEqual(normalizeTimelineColumns(JSON.parse(JSON.stringify(columns))),columns);
 assert.equal(normalizeTimelineColumns({}).time,0);
 assert.equal(columns.time,0,'older fixed clock widths now fill the available space');
 const bounded=normalizeTimelineColumns({execution:-10,events:Infinity,time:99999});
 assert.ok(bounded.execution>0&&Number.isFinite(bounded.events)&&bounded.time<99999);
 assert.ok(timelineColumnWidth('events',120)<timelineColumnWidth('events',240));
});

test('ribbon source links retain aliases and timeline settings while revealing the original message or call',()=>{
 const source={workspace:'work /one',conversation:'canonical',session:'session'},item={id:'part',start:0,end:10,message:message('m:&=1','P.request',{work_id:'work /one',conversation_id:'original',session_id:'nested'})};
 const current='http://localhost/work/work%20%2Fone?explorer=timeline&reader_view=timeline&delegation_start=4&disclosures=%7B%7D&delegation_event=old&call=stale';
 const url=new URL(ribbonSourceURL(item,source,current),'http://localhost');
 assert.equal(url.searchParams.get('conversation'),'original');assert.equal(url.searchParams.get('message'),'m:&=1');assert.equal(url.searchParams.get('reveal'),'message:m:&=1');assert.equal(url.searchParams.get('delegation_session'),'nested');
 assert.equal(url.searchParams.get('explorer'),'timeline');assert.equal(url.searchParams.get('delegation_start'),'4');assert.equal(url.searchParams.get('disclosures'),'{}');
 assert.equal(url.searchParams.get('reader_view'),'conversation');for(const key of ['delegation_event','call'])assert.equal(url.searchParams.has(key),false);
 const tool=new URL(ribbonSourceURL({id:'tool',start:1000,end:2000,mark:{tool_id:'call /id'}},source,current),'http://localhost');
 assert.equal(tool.searchParams.get('call'),'call /id');assert.equal(tool.searchParams.get('at'),'1970-01-01T00:00:01.000Z');assert.equal(tool.searchParams.has('message'),false);assert.equal(tool.searchParams.get('reveal'),'call:call /id');
 const deep=new URL(ribbonSourceURL(item,source,current.replace('/work/work%20%2Fone?','/work/work%20%2Fone/conversation/original?')),'http://localhost');assert.equal(deep.searchParams.get('delegation_start'),'4');
 assert.equal(ribbonSourceURL({id:'inferred',start:0,end:10},source,current),null);
 const external=new URL(ribbonSourceURL({...item,message:{...item.message,work_id:'elsewhere'}},source,current),'http://localhost');assert.equal(external.pathname,'/work/elsewhere');assert.equal(external.searchParams.has('delegation_start'),false);
});

test('timeline legends follow drawn intervals, errors and the visible portion of caller composition',()=>{
 const at=n=>new Date(n).toISOString(),marks=[{start:200,end:800,end_known:true,category:'run',name:'Run',error:false},{start:90,end:600,end_known:true,category:'read',name:'Read',error:true},{start:900,end:950,end_known:true,category:'git',name:'Git',error:false}];
 const early=message('early','C.report.finding',{at:at(0)}),late=message('late','P.request',{at:at(2000)});
 const visible=timelineLegendWindow([{messages:[early,late],marks}],[100,250]);
 assert.deepEqual(visible.messages.map(m=>m.id),[],'the preceding report ends at the tool boundary before this window');
 assert.deepEqual(visible.tools,['run']);assert.equal(visible.error,true);
 const thought=message('thought','C.deliberation.plan',{at:at(800),kind:'reasoning'}),unmatched=message('unmatched',null,{at:at(1000)});
 const thinking=timelineLegendWindow([{messages:[thought,unmatched],marks:[],neighbors:[early,thought,unmatched]}],[300,500]);
 assert.deepEqual(thinking.messages.map(m=>m.id),['thought'],'thinking is visible before its recorded endpoint');
 const caller=message('human','P.request',{at:at(0),authorship:[{category:'typed',bytes:5},{category:'pasted',bytes:3},{category:'harness',bytes:2}]}),end={start:1000,end:1000,end_known:false,category:'other',name:'Boundary',error:false};
 const slice=range=>timelineLegendWindow([{caller:true,messages:[caller],marks:[end]}],range);
 assert.deepEqual(slice([50,450]).origins,['typed']);assert.deepEqual(slice([550,750]).origins,['pasted']);assert.deepEqual(slice([850,950]).origins,['harness']);assert.deepEqual(slice([1200,1400]).messages,[]);
 assert.equal(slice([50,450]).tools.length,0,'caller boundary tools are not themselves drawn in the caller ribbon');
});

test('timeline previews unwrap content and nested arguments without losing domain JSON or literal caller text',()=>{
 for(const [body,expected,options] of [
  [JSON.stringify({content:[{text:'Async agent launched successfully.\nWorking in the background.'}],details:{agentId:'opaque'},tool:'Task'}),'Async agent launched successfully.\nWorking in the background.'],
  [JSON.stringify({message:{content:[{type:'output_text',text:'First'},{type:'text',text:'Second'}]},usage:{tokens:9}}),'First\n\nSecond'],
  [JSON.stringify(JSON.stringify({content:[{type:'text',text:'Decoded twice'}]})),'Decoded twice'],
  [JSON.stringify({tool:'Task',input:{description:'Check',prompt:'Read the source\nThen check tests',subagent_type:'Reviewer'}}),'Read the source\nThen check tests',{input:true}],
  [JSON.stringify({tool:'exec_command',input:JSON.stringify({cmd:'npm test',timeout_ms:1000})}),'npm test',{input:true}],
  [JSON.stringify({input:{description:'Review the patch',subagent_type:'Reviewer'}}),'Review the patch',{input:true}],
  [JSON.stringify({cmd:'"quoted command"'}),'"quoted command"',{input:true}],
  ['"literal code"','"literal code"',{input:true}],
  [JSON.stringify({file_path:'/repo/README.md',offset:10,limit:20}),'/repo/README.md',{input:true}],
  [JSON.stringify({stdout:'Partial output',stderr:'Something failed',exit_code:2}),'Partial output\n\nstderr:\nSomething failed'],
  [JSON.stringify({content:[{type:'image',source:{data:'opaque'}},{text:'Caption'}]}),'[Image]\n\nCaption'],
  ['printf "hello\nworld"','printf "hello\nworld"'],
  ['{"content":[{"text":"unfinished','{"content":[{"text":"unfinished'],
  ['{"content":"literal user JSON"}','{"content":"literal user JSON"}',{unwrap:false}],
 ])assert.equal(timelinePreviewText(body,options),expected);
 const structured={text:'Keep the body',records:[{count:2}]};assert.deepEqual(JSON.parse(timelinePreviewText(JSON.stringify(structured))),structured);
 const edit={file_path:'/repo/a.go',old_string:'before',new_string:'after'};assert.deepEqual(JSON.parse(timelinePreviewText(JSON.stringify(edit),{input:true})),edit);
});

import {labelOccurrenceTree,labelDetailWindow,labelFocusPath,labelTrendsURL,labelEvidenceRows,matchesLabelNode} from '../../../src/label-inspection-model';
import {labelTrendKeys} from '../../../src/label-dashboard-model';
test('label inspection counts a multi-label message once per ancestor and preserves coverage and parent-only matches',()=>{
 const messages=[message('multi','P.request.work.review',{labels:[['P.request.work.review',.9],['P.request.work.verify',.8],['C.report.finding',.9]]}),message('parent','P.request'),message('generic','C'),message('missing',null),message('no-match',null,{chunks:1})];
 const tree=labelOccurrenceTree(messages),nodes=new Map();const visit=node=>{nodes.set(node.id,node);node.children.forEach(visit);};visit(tree);
 assert.equal(tree.count,5);assert.equal(nodes.get('P').count,2);assert.equal(nodes.get('P.request.work').count,1);assert.equal(nodes.get('P.request.work.review').count,1);assert.equal(nodes.get('C').count,2);
 assert.equal(nodes.get('uncategorized').count,2);assert.equal(nodes.get('unlabeled').count,1);assert.equal(nodes.get('no-match').count,1);
 assert.equal(nodes.get('P.request~other').count,1);assert.equal(nodes.get('C~other').count,1);assert.equal(nodes.has('C.report.finding~other'),false);
 assert.equal(matchesLabelNode(messages[0],'P.request~other'),false);assert.equal(matchesLabelNode(messages[1],'P.request~other'),true);
 const empty=labelOccurrenceTree([]);assert.equal(empty.count,0);assert.deepEqual(empty.children,[]);
});
test('label detail windows retain their message identities while selecting, paging and changing branches',()=>{
 const messages=Array.from({length:165},(_,i)=>message('m'+i,'P.request'));
 const first=labelDetailWindow(messages,'s','m55',undefined);assert.equal(first.start,0);assert.equal(first.next,'m80');assert.equal(first.previous,undefined);
 const saved={session:'s',start:'m80'},same=labelDetailWindow(messages,'s','m150',saved);assert.equal(same.start,80);assert.equal(same.previous,'m0');assert.equal(same.next,'m160');
 const last=labelDetailWindow(messages,'s','m164',{session:'s',start:'m160'});assert.deepEqual(last.messages.map(m=>m.id),['m160','m161','m162','m163','m164']);assert.equal(last.next,undefined);
 assert.equal(labelDetailWindow(messages,'other','m55',saved).start,0);assert.equal(labelDetailWindow(messages,'s','m2',saved).start,0);assert.deepEqual(labelDetailWindow([],'s',undefined,saved).messages,[]);
});
test('coverage and exact-parent trends preserve the full population denominator',()=>{
 const at=new Date(2026,0,2).toISOString(),source=stream('s',[message('known','P.request',{at}),message('missing',null,{at}),message('no-match',null,{chunks:1,at})]);
 for(const [focus,expected] of [['unlabeled','missing'],['no-match','no-match'],['P.request~other','known']]){
  const chart={...defaultLabelChart,resolution:'label'},trend=labelTrend([source],chart,{},[],focus);
  assert.equal(trend.buckets[0].values[focus],1/3);assert.deepEqual(labelTrendMatches([source],chart,trend.series,{values:[],excluded:false},focus).map(e=>e.message.id),[expected]);
 }
 assert.deepEqual(labelTrendKeys(message('deep','P.request.work.review'),'family','P.request.work'),['P.request.work.review']);
 assert.deepEqual(labelFocusPath('unlabeled'),['uncategorized','unlabeled']);assert.deepEqual(labelFocusPath('P.request~other'),['P','P.request','P.request~other']);
});
test('inspection links preserve population and date context and hierarchy scores are never invented',()=>{
 const params=new URLSearchParams({label_repository:'repo',label_audience:'thinking',label_model:'model',label_dates:'{"from":"2026-01-01"}',label_series:'{}',label_chart:'{"scale":"logarithmic","smoothing":3}',chart_pins:'{}'});
 const next=new URL(labelTrendsURL(params,'P.request.work'),'http://localhost');assert.equal(next.searchParams.get('label_repository'),'repo');assert.equal(next.searchParams.get('label_audience'),'thinking');assert.equal(next.searchParams.get('label_dates'),params.get('label_dates'));assert.equal(JSON.parse(next.searchParams.get('label_chart')).scale,'logarithmic');assert.equal(JSON.parse(next.searchParams.get('label_chart')).smoothing,3);assert.equal(next.searchParams.has('label_series'),false);assert.equal(next.searchParams.has('chart_pins'),false);
 const rows=labelEvidenceRows(message('deep','P.request.work.review'));assert.deepEqual(rows.map(row=>row.id),['P','P.request','P.request.work','P.request.work.review']);assert.equal(rows[0].score,undefined);assert.equal(rows.at(-1).score,.9);assert.deepEqual(labelEvidenceRows(message('stale','P.request',{chunks:0})),[]);
});
