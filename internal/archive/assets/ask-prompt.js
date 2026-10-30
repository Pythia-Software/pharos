// Prompt offsets use UTF-16 text units and one unit per atomic context chip.
// Keeping this model independent of the DOM lets additional chip kinds share
// editing, movement and history without becoming part of the plain-text prompt.
function askPromptNormalize(parts) {
  const result=[],ids=new Set();
  for(const part of parts){
    if(typeof part==='string'){
      if(!part)continue;
      if(typeof result.at(-1)==='string')result[result.length-1]+=part;else result.push(part);
    }else if(part?.id&&!ids.has(part.id)){
      ids.add(part.id);result.push({...part});
    }
  }
  return result;
}
function askPromptLength(parts){return parts.reduce((size,part)=>size+(typeof part==='string'?part.length:1),0);}
function askPromptText(parts){return parts.filter(part=>typeof part==='string').join('');}
function askPromptSplit(parts,offset){
  const before=[],after=[];let remaining=Math.max(0,offset);
  for(const part of parts){
    const size=typeof part==='string'?part.length:1;
    if(remaining>=size){before.push(part);remaining-=size;}
    else if(typeof part==='string'){before.push(part.slice(0,remaining));after.push(part.slice(remaining));remaining=0;}
    else after.push(part);
  }
  return [askPromptNormalize(before),askPromptNormalize(after)];
}
function askPromptSplice(parts,start,count,insert=[]){
  const [before,rest]=askPromptSplit(parts,start),[,after]=askPromptSplit(rest,count);
  return askPromptNormalize([...before,...insert,...after]);
}
function askPromptMoveChip(parts,id,target){
  let source=0;const chip=parts.find(part=>{if(typeof part!=='string'&&part.id===id)return true;source+=typeof part==='string'?part.length:1;return false;});
  if(!chip||target===source||target===source+1)return parts;
  const without=askPromptSplice(parts,source,1);
  return askPromptSplice(without,target>source?target-1:target,0,[chip]);
}
function askPromptStepChip(parts,id,direction){
  const index=parts.findIndex(part=>part?.id===id);if(index<0)return parts;
  const neighbor=parts[index+(direction<0?-1:1)];if(neighbor===undefined)return parts;
  const segments=typeof neighbor==='string'?[...new Intl.Segmenter(undefined,{granularity:'grapheme'}).segment(neighbor)]:null;
  const step=segments?(direction<0?segments.at(-1):segments[0]).segment.length:1;
  const source=askPromptLength(parts.slice(0,index));
  return askPromptMoveChip(parts,id,direction<0?source-step:source+1+step);
}
function askPromptReplaceText(parts,text){
  const result=[];let offset=0,previous=0;
  for(const part of parts){
    if(typeof part==='string'){offset+=part.length;continue;}
    const next=Math.min(offset,text.length);result.push(text.slice(previous,next),part);previous=next;
  }
  result.push(text.slice(previous));return askPromptNormalize(result);
}
function askPromptRestore(parts,text){
  if(!Array.isArray(parts)||parts.length>256||!parts.every(part=>typeof part==='string'||part&&['id','type','label','value'].every(key=>typeof part[key]==='string'&&part[key].length<=2000)))return null;
  const normalized=askPromptNormalize(parts);
  return askPromptText(normalized)===text?normalized:null;
}

function initAskPromptEditor(element){
  const chips=new Map();let disabled=false,dragged=null,touch=null,history=[],historyIndex=-1;
  const chipNode=node=>node?.nodeType===1&&node.hasAttribute('data-prompt-chip');
  function readNode(node){
    if(node.nodeType===3)return [node.data];
    if(chipNode(node))return chips.has(node.dataset.promptChip)?[{...chips.get(node.dataset.promptChip)}]:[];
    if(node.nodeType!==1&&node.nodeType!==11||node.nodeType===1&&node.hasAttribute('data-editor-end'))return [];
    if(node.tagName==='BR')return ['\n'];
    const parts=[];
    for(const child of node.childNodes){
      if(child.nodeType===1&&/^(DIV|P)$/.test(child.tagName)&&parts.length&&askPromptText(parts).at(-1)!=='\n')parts.push('\n');
      parts.push(...readNode(child));
    }
    return parts;
  }
  const read=()=>{
    const parts=askPromptNormalize(readNode(element));
    // Browsers leave one placeholder BR after deleting all editable content.
    if(parts.length===1&&parts[0]==='\n'&&!element.textContent&&element.querySelectorAll('br').length===1)return [];
    return parts;
  };
  function offsetAt(node,offset){
    const range=document.createRange();range.selectNodeContents(element);
    try{range.setEnd(node,offset);}catch{return askPromptLength(read());}
    return askPromptLength(askPromptNormalize(readNode(range.cloneContents())));
  }
  function selection(){
    const value=window.getSelection();
    if(!value?.rangeCount||!element.contains(value.anchorNode)||!element.contains(value.focusNode)){const end=askPromptLength(read());return [end,end];}
    const range=value.getRangeAt(0);return [offsetAt(range.startContainer,range.startOffset),offsetAt(range.endContainer,range.endOffset)];
  }
  function caret(offset){
    const range=document.createRange();let remaining=Math.max(0,offset),found=false;
    function visit(parent){for(const node of parent.childNodes){
      if(node.nodeType===3){if(remaining<=node.length){range.setStart(node,remaining);found=true;return;}remaining-=node.length;}
      else if(chipNode(node)||node.nodeName==='BR'){
        if(remaining===0){range.setStartBefore(node);found=true;return;}remaining--;
        if(remaining===0){range.setStartAfter(node);found=true;return;}
      }else{visit(node);if(found)return;}
    }}
    visit(element);if(!found){range.selectNodeContents(element);range.collapse(false);}else range.collapse(true);
    const value=window.getSelection();value.removeAllRanges();value.addRange(range);
  }
  function syncEmpty(){element.dataset.empty=String(!read().length);}
  function makeChip(chip){
    chips.set(chip.id,{...chip});
    const node=document.createElement('span');node.className='pharos-page-context';node.contentEditable='false';node.draggable=true;node.tabIndex=0;node.dataset.promptChip=chip.id;
    node.setAttribute('role','group');node.setAttribute('aria-label',chip.label+' context. Drag to move, or use Alt and arrow keys.');
    const label=document.createElement('span');label.className='pharos-context-label';label.textContent=chip.label;label.title=chip.value;
    const remove=document.createElement('button');remove.type='button';remove.className='pharos-context-remove cursor-interaction';remove.setAttribute('aria-label','Remove '+(chip.type==='page'?'page':chip.label)+' context');remove.title=remove.getAttribute('aria-label');remove.textContent='×';
    remove.addEventListener('click',()=>removeChip(chip.id));node.append(label,remove);
    node.addEventListener('keydown',event=>{
      if(event.target!==node)return;
      if(['Backspace','Delete'].includes(event.key)){event.preventDefault();removeChip(chip.id);}
      if(event.altKey&&['ArrowLeft','ArrowRight'].includes(event.key)){
        event.preventDefault();event.stopPropagation();const next=askPromptStepChip(read(),chip.id,event.key==='ArrowLeft'?-1:1);
        write(next,chipOffset(next,chip.id)+1);changed();element.querySelector('[data-prompt-chip="'+CSS.escape(chip.id)+'"]')?.focus();
      }
    });
    return node;
  }
  function chipOffset(parts,id){let size=0;for(const part of parts){if(part?.id===id)return size;size+=typeof part==='string'?part.length:1;}return size;}
  function write(parts,position){
    parts=askPromptNormalize(parts);
    const end=document.createElement('br');end.dataset.editorEnd='';
    const nodes=parts.map(part=>typeof part==='string'?document.createTextNode(part):makeChip(part));
    if(parts.length&&(typeof parts.at(-1)!=='string'||parts.at(-1).endsWith('\n')))nodes.push(end);
    element.replaceChildren(...nodes);
    syncEmpty();if(position!==undefined){element.focus({preventScroll:true});caret(position);}
  }
  function snapshot(){return {parts:read(),caret:selection()[0]};}
  function remember(){
    const next=snapshot();if(JSON.stringify(next.parts)===JSON.stringify(history[historyIndex]?.parts))return;
    history=history.slice(0,historyIndex+1);history.push(next);if(history.length>100)history.shift();historyIndex=history.length-1;
  }
  function changed(){element.dispatchEvent(new Event('input',{bubbles:true}));}
  function restoreHistory(delta){
    const next=historyIndex+delta;if(next<0||next>=history.length)return;
    historyIndex=next;const state=history[next];write(state.parts,state.caret);changed();
  }
  function removeChip(id){
    if(disabled)return;const parts=read(),index=chipOffset(parts,id);write(parts.filter(part=>part?.id!==id),index);changed();
  }
  function move(id,position){
    if(disabled)return;const parts=read(),next=askPromptMoveChip(parts,id,Math.max(0,Math.min(askPromptLength(parts),position)));
    if(next===parts)return;write(next,chipOffset(next,id)+1);changed();
  }
  function atPoint(x,y){
    const position=document.caretPositionFromPoint?.(x,y),range=position?null:document.caretRangeFromPoint?.(x,y);
    const node=position?.offsetNode||range?.startContainer,offset=position?.offset??range?.startOffset;
    if(!node||!element.contains(node))return null;
    const chip=(node.nodeType===1?node:node.parentElement).closest('[data-prompt-chip]');
    if(chip)return chipOffset(read(),chip.dataset.promptChip)+(x>chip.getBoundingClientRect().left+chip.offsetWidth/2?1:0);
    return offsetAt(node,offset);
  }
  Object.defineProperties(element,{
    value:{get:()=>askPromptText(read()),set:value=>{write(askPromptReplaceText(read(),String(value)));history=[snapshot()];historyIndex=0;}},
    disabled:{get:()=>disabled,set:value=>{disabled=!!value;element.contentEditable=String(!disabled);element.setAttribute('aria-disabled',String(disabled));}},
    placeholder:{set:value=>element.dataset.placeholder=value},
  });
  element.setSelectionRange=(start,end=start)=>{caret(start);if(end!==start){const value=window.getSelection(),anchor=value.getRangeAt(0).cloneRange();caret(end);const focus=value.getRangeAt(0);anchor.setEnd(focus.endContainer,focus.endOffset);value.removeAllRanges();value.addRange(anchor);}};
  element.addEventListener('input',event=>{syncEmpty();if(!event.isComposing)remember();});
  element.addEventListener('compositionend',remember);
  element.addEventListener('keydown',event=>{
    if((event.metaKey||event.ctrlKey)&&!event.altKey&&(event.key.toLowerCase()==='z'||event.key.toLowerCase()==='y')){
      event.preventDefault();restoreHistory(event.shiftKey||event.key.toLowerCase()==='y'?1:-1);
    }
  });
  element.addEventListener('beforeinput',event=>{
    if(event.isComposing)return;
    if(event.inputType==='historyUndo'||event.inputType==='historyRedo'){event.preventDefault();restoreHistory(event.inputType==='historyUndo'?-1:1);}
    if(event.inputType==='insertParagraph'||event.inputType==='insertLineBreak'){event.preventDefault();document.execCommand('insertText',false,'\n');}
  });
  element.addEventListener('paste',event=>{event.preventDefault();const text=event.clipboardData.getData('text/plain').replace(/\r\n?/g,'\n');if(!disabled&&text)document.execCommand('insertText',false,text);});
  for(const name of ['copy','cut'])element.addEventListener(name,event=>{
    const [start,end]=selection();if(start===end)return;
    const [,rest]=askPromptSplit(read(),start),[selected]=askPromptSplit(rest,end-start);
    event.preventDefault();event.clipboardData.setData('text/plain',selected.map(part=>typeof part==='string'?part:part.label).join(''));
    if(name==='cut'&&!disabled){write(askPromptSplice(read(),start,end-start),start);changed();}
  });
  element.addEventListener('dragstart',event=>{
    const chip=event.target.closest('[data-prompt-chip]');if(!chip||disabled)return;
    dragged=chip.dataset.promptChip;event.dataTransfer.effectAllowed='move';event.dataTransfer.setData('application/x-pharos-prompt-chip',dragged);event.dataTransfer.setData('text/plain',chips.get(dragged).label);chip.classList.add('pharos-chip-dragging');
  });
  element.addEventListener('dragover',event=>{if(!dragged)return;event.preventDefault();event.dataTransfer.dropEffect='move';const position=atPoint(event.clientX,event.clientY);if(position!==null)caret(position);});
  element.addEventListener('drop',event=>{
    event.preventDefault();if(disabled)return;
    const position=atPoint(event.clientX,event.clientY);
    if(dragged&&position!==null)move(dragged,position);
    else if(!dragged){const text=event.dataTransfer.getData('text/plain');if(text){if(position!==null)caret(position);document.execCommand('insertText',false,text);}}
    dragged=null;
  });
  element.addEventListener('dragend',()=>{dragged=null;element.querySelectorAll('.pharos-chip-dragging').forEach(node=>node.classList.remove('pharos-chip-dragging'));});
  element.addEventListener('pointerdown',event=>{
    const chip=event.target.closest('[data-prompt-chip]');
    if(event.pointerType==='touch'&&chip&&!event.target.closest('button')&&!disabled){touch={id:chip.dataset.promptChip,x:event.clientX,y:event.clientY,moved:false};chip.setPointerCapture(event.pointerId);}
  });
  element.addEventListener('pointermove',event=>{if(!touch)return;touch.moved||=Math.hypot(event.clientX-touch.x,event.clientY-touch.y)>5;if(touch.moved){event.preventDefault();const position=atPoint(event.clientX,event.clientY);if(position!==null)caret(position);}});
  element.addEventListener('pointerup',event=>{if(touch?.moved){const position=atPoint(event.clientX,event.clientY);if(position!==null)move(touch.id,position);}touch=null;});
  element.addEventListener('pointercancel',()=>{touch=null;});
  write([]);remember();
  return {
    chips:()=>read().filter(part=>typeof part!=='string'),
    document:read,
    restore:parts=>{const restored=askPromptRestore(parts,element.value);if(!restored)return false;write(restored);history=[snapshot()];historyIndex=0;return true;},
    setChip:chip=>{
      const parts=read(),exists=parts.some(part=>part?.id===chip.id),position=document.activeElement===element?selection()[0]:undefined;
      write(exists?parts.map(part=>part?.id===chip.id?chip:part):[chip,...parts],position);remember();
    },
  };
}
