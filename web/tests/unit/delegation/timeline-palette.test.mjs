import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {join} from 'node:path';
import {assets} from '../helpers/classic-script.mjs';

// Agent events take the primaries (ui.py --event-*), tool actions secondary and
// tertiary hues (delegation.css --activity-*), so the two families stay apart
// on one timeline row. Distances are OKLab ΔE ×100 against each theme's
// panel, with Machado (2009) protan/deutan simulation for colour-vision
// deficiency; events also differ from tools by shape, which is the fallback
// where a CVD distance is small.
const root=join(assets,'../../..');
const ui=readFileSync(join(assets,'ui.py'),'utf8'),css=readFileSync(join(root,'web/src/delegation.css'),'utf8');
const vars=(body,prefix)=>Object.fromEntries([...body.matchAll(new RegExp(`--${prefix}-([a-z-]+):(#[0-9a-fA-F]{6})`,'g'))].map(([,k,v])=>[k,v]));
const rule=(text,selector)=>{const at=text.indexOf(selector+'{');assert.ok(at>=0,`${selector} is defined`);return text.slice(at,text.indexOf('}',at));};
const tools=vars(rule(css,'.lt-session-tree,.delegation-parallel.is-recursive,.lt-ribbon-inspector'),'activity');
const eventLine=selector=>vars(ui.split('\n').find(line=>line.trim().startsWith(selector+'{--event-friction'))??'','event');
const panel=selector=>/--panel:(#[0-9a-fA-F]{6})/.exec(ui.split('\n').find(line=>line.trim().startsWith(selector+'{color-scheme'))??'')?.[1];
const themes={light:{events:eventLine(':root'),panel:panel(':root')},dark:{events:eventLine(':root[data-theme="dark"]'),panel:panel(':root[data-theme="dark"]')}};

const s2l=c=>c<=0.04045?c/12.92:((c+0.055)/1.055)**2.4;
const linear=hex=>[0,2,4].map(i=>s2l(parseInt(hex.slice(1+i,3+i),16)/255));
const oklab=([r,g,b])=>{const l=Math.cbrt(.4122214708*r+.5363325363*g+.0514459929*b),m=Math.cbrt(.2119034982*r+.6806995451*g+.1073969566*b),s=Math.cbrt(.0883024619*r+.2817188376*g+.6299787005*b);return [.2104542553*l+.793617785*m-.0040720468*s,1.9779984951*l-2.428592205*m+.4505937099*s,.0259040371*l+.7827717662*m-.808675766*s];};
const MACHADO={protan:[[.152286,1.052583,-.204868],[.114503,.786281,.099216],[-.003882,-.048116,1.051998]],deutan:[[.367322,.860646,-.227968],[.280085,.672501,.047413],[-.01182,.04294,.968881]]};
const simulate=(hex,kind)=>{const c=linear(hex);return kind?MACHADO[kind].map(row=>Math.max(0,Math.min(1,row[0]*c[0]+row[1]*c[1]+row[2]*c[2]))):c;};
const dE=(a,b,kind)=>{const p=oklab(simulate(a,kind)),q=oklab(simulate(b,kind));return 100*Math.hypot(p[0]-q[0],p[1]-q[1],p[2]-q[2]);};
const cvd=(a,b)=>Math.min(dE(a,b,'protan'),dE(a,b,'deutan'));
const muted=new Set(['wait','poll','other','model']);
const hued=Object.fromEntries(Object.entries(tools).filter(([k])=>!muted.has(k)));

test('every tool action and agent event group has a colour in both themes',()=>{
 for(const k of ['read','write','run','git','network','delegate','error'])assert.match(tools[k]??'',/^#/,k);
 for(const [mode,{events,panel}] of Object.entries(themes)){
  assert.match(panel??'',/^#/,`${mode} panel`);
  for(const k of ['friction','learning','second-look','choices'])assert.match(events[k]??'',/^#/,`${mode} ${k}`);
 }
});

test('agent events stay apart from one another and from tool actions in both themes',()=>{
 for(const [mode,{events}] of Object.entries(themes)){
  const groups=Object.entries(events).filter(([k])=>k!=='link'&&k!=='on');
  for(const [i,[a,x]] of groups.entries())for(const [b,y] of groups.slice(i+1))assert.ok(dE(x,y)>=15,`${mode} ${a}/${b} ΔE ${dE(x,y).toFixed(1)}`);
  for(const [t,x] of Object.entries(hued))for(const [e,y] of groups)assert.ok(dE(x,y)>=11,`${mode} tool ${t} vs event ${e} ΔE ${dE(x,y).toFixed(1)}`);
  // The collisions the primaries replaced: Error was red like friction, Read
  // was amber like learning, Delegate was blue like choices.
  assert.ok(dE(tools.error,events.friction)>=14&&cvd(tools.error,events.friction)>=12,`${mode} error vs friction ΔE ${dE(tools.error,events.friction).toFixed(1)}, CVD ${cvd(tools.error,events.friction).toFixed(1)}`);
  assert.ok(dE(tools.read,events.learning)>=14,`${mode} read vs learning`);
  assert.ok(dE(tools.delegate,events.choices)>=15,`${mode} delegate vs choices`);
  assert.ok(dE(tools.write,events.choices)>=15,`${mode} write vs choices`);
 }
});

test('tool action hues stay apart from one another',()=>{
 const list=Object.entries(hued);
 for(const [i,[a,x]] of list.entries())for(const [b,y] of list.slice(i+1))assert.ok(dE(x,y)>=12,`${a}/${b} ΔE ${dE(x,y).toFixed(1)}`);
});
