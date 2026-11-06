import React,{useEffect,useId,useRef,useState} from 'react';
import {createPortal} from 'react-dom';
import './label-ribbon-inspector.css';

let pinnedInspector:{id:string;close:()=>void}|null=null;

/** Fit above or below the ribbon, keeping the inspected bar reachable. */
export function ribbonInspectorPlacement(anchor:{top:number;bottom:number},height:number,viewportHeight:number){
 const lower=Math.max(8,Math.min(anchor.bottom+8,viewportHeight-8)),upper=Math.max(8,Math.min(anchor.top-8,viewportHeight-8));
 const below=viewportHeight-8-lower,above=upper-8,after=height<=below||below>=above,maxHeight=after?below:above;
 return {top:after?lower:upper-Math.min(height,maxHeight),maxHeight};
}

/** Pointer/focus inspection, including a pinned preview, is transient. */
export function useRibbonInspector({interactive=false}:{interactive?:boolean}={}){
 const id=useId(),[card,setCard]=useState<{x:number;y:number;content:React.ReactNode;pinned:boolean;focus:boolean}|null>(null);
 const current=useRef(card);current.current=card;
 const anchor=useRef<Element|null>(null),popup=useRef<HTMLDivElement|null>(null),pending=useRef<ReturnType<typeof setTimeout>>();
 const keep=()=>{if(pending.current!==undefined){clearTimeout(pending.current);pending.current=undefined;}},close=()=>{keep();if(pinnedInspector?.id===id)pinnedInspector=null;current.current=null;setCard(null);};
 useEffect(()=>()=>{keep();if(pinnedInspector?.id===id)pinnedInspector=null;},[]);
 useEffect(()=>{
  if(!card)return;
  const key=(event:KeyboardEvent)=>{if(event.key==='Escape'){const restore=popup.current?.contains(document.activeElement);close();if(restore)(anchor.current as HTMLElement|null)?.focus?.({preventScroll:true});}};
  const scroll=(event:Event)=>{if(event.target instanceof Node&&popup.current?.contains(event.target))return;const target=anchor.current;if(target&&(target===document.activeElement||target.contains(document.activeElement))){const rect=target.getBoundingClientRect();setCard(value=>value?{...value,y:rect.bottom+8}:null);}else close();};
  const outside=(event:PointerEvent)=>{if(event.target instanceof Node&&!popup.current?.contains(event.target)&&!anchor.current?.contains(event.target))close();};
  const navigate=()=>close();
  window.addEventListener('keydown',key);window.addEventListener('scroll',scroll,true);window.addEventListener('resize',navigate);window.addEventListener('popstate',navigate);window.addEventListener('pharos:workspace-history',navigate);if(interactive||card.pinned)document.addEventListener('pointerdown',outside);
  return()=>{window.removeEventListener('keydown',key);window.removeEventListener('scroll',scroll,true);window.removeEventListener('resize',navigate);window.removeEventListener('popstate',navigate);window.removeEventListener('pharos:workspace-history',navigate);document.removeEventListener('pointerdown',outside);};
 },[!!card,card?.pinned,interactive]);
 const show=(target:Element,content:React.ReactNode,x?:number,pinned=false,focus=false)=>{if(pinnedInspector&&!pinned)return;if(pinned){if(pinnedInspector?.id!==id)pinnedInspector?.close();pinnedInspector={id,close};}keep();anchor.current=target;const rect=target.getBoundingClientRect(),next={x:Math.max(8,Math.min(x??rect.left,window.innerWidth-Math.min(interactive?300:340,window.innerWidth-16)-8)),y:rect.bottom+8,content,pinned,focus};current.current=next;setCard(next);};
 const pin=(target:Element,content:React.ReactNode,x?:number,focus=false)=>show(target,content,x,true,focus);
 const leave=()=>{if(current.current?.pinned)return;if(!interactive&&!popup.current?.classList.contains('is-scrollable')){const target=anchor.current;if(!target||!(target===document.activeElement||target.contains(document.activeElement)))close();return;}const target=anchor.current;if(!target||!(target===document.activeElement||target.contains(document.activeElement))){keep();pending.current=setTimeout(()=>{pending.current=undefined;setCard(null);},150);}};
 const portal=card?createPortal(<div id={id} role={card.pinned?'group':'tooltip'} onPointerDown={event=>event.stopPropagation()} onClick={event=>event.stopPropagation()} aria-label={card.pinned?'Ribbon segment details':undefined} className={`lt-ribbon-inspector${interactive?' is-interactive':''}${card.pinned?' is-pinned':''}`} onPointerEnter={keep} onPointerLeave={leave} ref={node=>{
  popup.current=node;if(!node)return;
  node.style.maxHeight=`${window.innerHeight-16}px`;
  const height=node.getBoundingClientRect().height,placement=ribbonInspectorPlacement(anchor.current!.getBoundingClientRect(),height,window.innerHeight);
  node.classList.toggle('is-scrollable',height>placement.maxHeight);
  node.style.maxHeight=`${placement.maxHeight}px`;node.style.top=`${placement.top}px`;
  if(card.focus&&!node.contains(document.activeElement))node.querySelector<HTMLElement>('a,button')?.focus({preventScroll:true});
 }} style={{left:card.x,top:card.y}}>{card.content}</div>,document.body):null;
 return {id,show,pin,close,leave,portal};
}

/** What the pointer or keyboard cursor is on in a ribbon: a message span, tool
 * dot or agent event. card renders its popover; pinned cards carry links. */
export type RibbonHit={card:(pinned:boolean)=>React.ReactNode;hover?:()=>void};
type Inspector=ReturnType<typeof useRibbonInspector>;
/** The one hover, pin and keyboard model every ribbon mark uses (session
 * timeline rows and the conversation fingerprint): hover shows the card under
 * the pointer, a click pins it with its links, and on a focused ribbon the
 * arrow keys browse candidates and Enter pins one with its link focused. */
export function useRibbonPointer({inspector,hitAt,candidates=[],pinnable=true,disabled=false,suspended=false,unhover}:{inspector:Inspector;hitAt:(event:React.MouseEvent<Element>)=>RibbonHit|null;candidates?:RibbonHit[];pinnable?:boolean;disabled?:boolean;suspended?:boolean;unhover?:()=>void}){
 const cursor=useRef(0);
 const show=(target:Element,hit:RibbonHit,x?:number)=>{hit.hover?.();inspector.show(target,hit.card(false),x);};
 const leave=()=>{unhover?.();inspector.leave();};
 return {
  onPointerMove:(event:React.PointerEvent<Element>)=>{if(disabled)return;if(suspended){inspector.close();return;}const hit=hitAt(event);if(hit)show(event.currentTarget,hit,event.clientX);else leave();},
  onPointerLeave:leave,
  onClick:(event:React.MouseEvent<Element>)=>{if(disabled||!pinnable)return;const hit=hitAt(event);if(!hit)return;event.preventDefault();event.stopPropagation();hit.hover?.();inspector.pin(event.currentTarget,hit.card(true),event.clientX);},
  onFocus:(event:React.FocusEvent<Element>)=>{if(disabled)return;const hit=candidates[cursor.current]??candidates[0];if(hit)show(event.currentTarget,hit);},
  onBlur:leave,
  onKeyDown:(event:React.KeyboardEvent<Element>)=>{
   if(disabled||!pinnable||!['ArrowLeft','ArrowRight','Enter',' '].includes(event.key)||!candidates.length)return;event.preventDefault();event.stopPropagation();
   if(event.key==='ArrowLeft'||event.key==='ArrowRight'){cursor.current=(cursor.current+(event.key==='ArrowLeft'?-1:1)+candidates.length)%candidates.length;inspector.close();show(event.currentTarget,candidates[cursor.current]);}
   else{const hit=candidates[cursor.current]??candidates[0];hit.hover?.();inspector.pin(event.currentTarget,hit.card(true),undefined,true);}
  },
 };
}
