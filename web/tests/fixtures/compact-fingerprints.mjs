// Deterministic synthetic inputs for the two production fingerprint profiles.
// No private text or live library is needed to reproduce renderer parity.
export function fingerprintCases(seedCount = 32) {
 const epoch = 1700000000000, at = i => new Date(epoch + i * 10000).toISOString();
 const message = (i, extra = {}) => ({id: 'm' + i, role: i % 3 ? 'assistant' : 'user', kind: 'message', at: at(i), session_id: i % 2 ? 'child' : 'main', labels: [[i % 2 ? 'C.report.product' : 'P.request.work.implement', .9]], chunks: 1, ...extra});
 const conversation = n => ({id: 'conversation', work_id: 'work', main_session: 'main', messages: Array.from({length: n}, (_, i) => message(i))});
 const event = (i, m, extra = {}) => ({id: 'e' + i, message_id: 'm' + m, kind: 'obstacle', group: 'Friction', probability: .9, span: [i * 100, i * 100 + 60], target: {method: 'quote', strong: true, message_id: 'm' + Math.max(0, m - 12)}, ...extra});
 const tool = (i, m, extra = {}) => ({id: 't' + i, session_id: i % 2 ? 'child' : 'main', category: ['read','write','edit','run','git','network','delegate','wait','poll','unknown'][i % 10], status: i % 7 ? 'complete' : 'error', start: at(m), end: null, ...extra});
 const result = [], add = (name, c, events = [], taxonomy = []) => result.push({name, input: {conversation: c, events, taxonomy}});
 add('empty', conversation(0));
 add('single', conversation(1));
 add('small', conversation(12));
 add('untimed', {...conversation(12), messages: Array.from({length: 12}, (_, i) => message(i, {at: null, session_id: null, chunks: i % 3, labels: []}))});
 add('labels', {...conversation(48), messages: Array.from({length: 48}, (_, i) => message(i, {chunks: i % 7 ? 1 : 0, labels: [ ['P', .5], ['P.request', .8], ['P.request.work', .9], ['P.request.work.review', 1], ['C.report', .8], ['C.report.product', .9], ['C.feedback.challenge', .8], ['invalid', 1] ].slice(i % 8)}))}, [], [{id: 'C.report.product.child'}, {id: 'P.request.work.review.child'}]);
 add('label-boundaries', {...conversation(41), messages: Array.from({length: 41}, (_, i) => message(i, {labels: [[['P','C','P.frame.constraint','C.input.permission.deny','C.difficulty.blocker','C.unknown.child','C.report.completion'][i % 7],1]], chunks: i % 5 ? 1 : 0}))});
 add('palette', {...conversation(70), messages: Array.from({length: 70}, (_, i) => message(i, {labels: [[['P','P.request','P.request.work','P.request.work.investigate','P.request.work.implement','P.request.work.review','P.request.work.revise','P.request.work.verify','P.request.input','P.request.input.information','P.request.input.clarification','P.request.input.choice','P.request.input.permission','P.request.input.guidance','P.frame','P.frame.scope','P.frame.constraint','P.frame.criteria','P.control','P.control.continue','P.control.redirect','P.control.pause','P.control.cancel','C','C.input','C.input.answer','C.input.option','C.input.permission','C.input.permission.grant','C.input.permission.deny','C.input.permission.defer','C.feedback','C.feedback.accept','C.feedback.qualify','C.feedback.challenge','C.feedback.reject','C.deliberation','C.deliberation.plan','C.deliberation.hypothesis','C.deliberation.evidence','C.deliberation.reconsider','C.difficulty','C.difficulty.ambiguity','C.difficulty.inconsistency','C.difficulty.guidance','C.difficulty.blocker','C.report','C.report.intent','C.report.progress','C.report.product','C.report.finding','C.report.verification','C.report.completion','C.report.limitation'][i % 54],1]]}))});
 const sessions = [{id:'main',parent_id:null,depth:0},{id:'child',parent_id:'main',depth:1},{id:'other',parent_id:'main',depth:1}];
 const branch = conversation(100); branch.sessions = sessions; branch.messages.forEach((m,i)=>{m.session_id=['main','child','other'][i%3];});
 add('parallel', branch);
 add('parallel-compressed', {...branch,messages:Array.from({length:150},(_,i)=>message(i,{session_id:sessions[i%3].id}))});
 add('arcs', conversation(120), Array.from({length:18},(_,i)=>event(i,15+i*5,{kind:['a','b','c','d'][i%4],group:['Friction','Reactions and learning','Worth a second look','Choices you may not see'][i%4],target:{method:i%3?'quote':'overlap',strong:i%2===0,message_id:'m'+(i%12)}})));
 add('missing-targets', conversation(40), [event(0,5,{target:null}),event(1,20,{target:{method:'quote',message_id:'absent'}}),event(2,10,{target:{method:'quote',message_id:'m30'}}),event(3,15,{message_id:'absent'}),event(4,25,{target:{method:'file'}})]);
 add('merged-events', conversation(50), [event(0,30,{span:[0,100]}),event(1,30,{kind:'rare',group:'Reactions and learning',probability:.8,span:[10,90],target:null}),event(2,30,{kind:'alternate',span:[15,95],target:{method:'overlap',message_id:'m2'}}),event(3,40,{span:[0,50]}),event(4,40,{span:[50,100]}),event(5,40,{span:null})]);
 add('same-time', {...conversation(30), messages:Array.from({length:30},(_,i)=>message(i,{at:i<20?at(1):null}))},Array.from({length:20},(_,i)=>event(i,i+5)));
 add('tools-only', {...conversation(0),sessions,activity:Array.from({length:40},(_,i)=>tool(i,i,{duration_ms:i%3?100:80000}))});
 add('tools-untimed', {...conversation(0),activity:Array.from({length:20},(_,i)=>tool(i,i,{start:null,duration_ms:5000}))});
 const mixed={...branch,activity:Array.from({length:60},(_,i)=>tool(i,i*1.7,{duration_ms:i%12===0?180000:2000}))};
 add('mixed', mixed, Array.from({length:100},(_,i)=>event(i,15+i%85,{target:i%4?{method:'quote',strong:true,message_id:'m'+i%14}:null})));
 add('tool-anchors', {...conversation(3),activity:[tool(0,.5),tool(1,1,{message_id:'m1',start:null,duration_ms:10000}),tool(2,-10,{duration_ms:5000}),tool(3,10,{duration_ms:90000}),tool(4,1,{start:null}),tool(5,1,{end:at(0),duration_ms:8000})]});
 add('long-history', {...conversation(2501),activity:[tool(0,-10),tool(1,2550),tool(2,200,{start:null})]},[event(0,100),event(1,2500)]);
 add('dense', {...conversation(620),sessions,activity:Array.from({length:300},(_,i)=>tool(i,i*2,{duration_ms:i%9===0?180000:500}))},Array.from({length:1476},(_,i)=>event(i,10+i%610,{kind:'kind'+i%13,group:['Friction','Reactions and learning','Worth a second look','Choices you may not see'][i%4],target:i%5===0?null:{method:i%3?'quote':'overlap',strong:i%2===0,message_id:'m'+i%10}})));
 const unicode = conversation(40);
 unicode.messages.forEach((m,i)=>{m.id='m'+i+['-é','-😀','-\ue000','-a'][i%4];});
 add('unicode-identities',unicode,Array.from({length:12},(_,i)=>event(i,0,{id:['é','😀','\ue000','a'][i%4]+i,message_id:unicode.messages[10+i*2].id,kind:['a-b','a_b','ab'][i%3],target:{method:'quote',strong:true,message_id:unicode.messages[i%7].id}})));
 add('fractional-times', {...conversation(5),activity:[tool(0,0,{start:'2023-11-14T23:13:20.999999+01:00',end:'2023-11-14T23:13:40.000001+01:00'}),tool(1,1,{start:'2023-11-14T22:13:20.999001Z',duration_ms:.25})],messages:Array.from({length:5},(_,i)=>message(i,{at:'2023-11-14T22:13:'+String(20+i).padStart(2,'0')+'.999999999Z'}))});
 add('transitive-merging',conversation(30),[event(0,20,{span:[0,100],kind:'same',probability:.5}),event(1,20,{span:[10,110],kind:'same',probability:.9,target:null}),event(2,20,{span:[20,120],kind:'same',probability:.8,target:{method:'overlap',message_id:'m2'}}),event(3,22,{group:'unknown',target:null})]);
 let state=0x5eed;const random=()=>{state=(Math.imul(state,1664525)+1013904223)>>>0;return state/4294967296;};
 for(let k=0;k<seedCount;k++){
  const count=5+Math.floor(random()*180),c=conversation(count);c.sessions=k%3?sessions:[];
  c.messages.forEach((m,i)=>{m.at=i%11?at(Math.floor(random()*count)):null;m.chunks=i%7?1:0;m.labels=[[['P.frame','C.report.product','C.deliberation.plan','C.feedback.challenge'][Math.floor(random()*4)],1]];});
  c.activity=Array.from({length:Math.floor(random()*50)},(_,i)=>tool(i,random()*count,{message_id:i%4===0?'m'+Math.floor(random()*count):undefined,duration_ms:Math.floor(random()*120000)}));
  add('seeded-'+k,c,Array.from({length:Math.floor(random()*120)},(_,i)=>event(i,Math.floor(random()*count),{kind:'k'+Math.floor(random()*8),probability:random(),group:['Friction','Reactions and learning','Worth a second look','Choices you may not see','unknown'][Math.floor(random()*5)],target:random()<.3?null:{method:random()<.5?'quote':'overlap',strong:random()<.5,message_id:'m'+Math.floor(random()*count)}})));
 }
 return result;
}
