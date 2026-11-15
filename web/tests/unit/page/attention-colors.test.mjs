import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync,readdirSync} from 'node:fs';
import {join,relative} from 'node:path';
import {assets} from '../helpers/classic-script.mjs';

// Highlighter colours point to Find matches, linked targets or the region a
// Data Guide explanation describes. Ordinary selections, frames and focus
// rings stay distinct (docs/url-view-state.md#attention-colours).
const root=join(assets,'../../..');
const sources=[
 ...readdirSync(join(root,'web/src')).filter(name=>/\.(css|tsx?)$/.test(name)).map(name=>join(root,'web/src',name)),
 ...readdirSync(assets).filter(name=>/\.(js|css|html|py)$/.test(name)&&!/^(query-tables|data-guide)\./.test(name)).map(name=>join(assets,name)),
].map(file=>({file:relative(root,file),text:readFileSync(file,'utf8')}));
const owners=new Set(['web/src/page-find.css','web/src/view-attention.css']);
const rules=text=>[...text.matchAll(/([^{}]*)\{([^{}]*)\}/g)].map(([,selector,body])=>({selector:selector.trim(),body}));

test('attention colours are owned by Find and linked or instructional attention',()=>{
 for(const {file,text} of sources){
  if(owners.has(file))continue;
  for(const {selector,body} of rules(text)){
   if(!/var\(--attention-/.test(body))continue;
   // The shell defines the tokens; the reader's deep-link landing is attention.
   const allowed=file==='internal/archive/assets/ui.py'&&(selector.includes(':root')||selector.endsWith('.authorship-target .message-card'));
   assert.ok(allowed,`${file}: ${selector.slice(-80)} uses an attention colour`);
  }
  for(const hex of ['#fff176','#ffe94d','#ff9f43','#ffa24a','#e0a800'])
   if(file!=='internal/archive/assets/ui.py')assert.ok(!text.toLowerCase().includes(hex),`${file} uses reserved attention colour ${hex}`);
 }
});

test('the retired lamp and find-marker colours stay retired',()=>{
 for(const {file,text} of sources)assert.ok(!/--(lamp|lamp-strong|find-marker|find-current)\b/.test(text),`${file} uses a retired highlight colour`);
});

test('keyboard focus rings use the focus colour, never gold or the accent',()=>{
 for(const {file,text} of sources)for(const {selector,body} of rules(text)){
  if(!/:focus/.test(selector))continue;
  // A standalone page without the shell palette may fall back to the accent.
  const ring=body.replace(/var\(--focus-ring,[^;]*/g,'var(--focus-ring)');
  assert.ok(!/outline\s*:[^;]*var\(--(gold|accent)\b/.test(ring),`${file}: ${selector.slice(-80)} draws its focus ring in gold or accent`);
 }
});
