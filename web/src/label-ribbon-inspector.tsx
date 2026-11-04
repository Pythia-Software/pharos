import React,{useEffect,useId,useRef,useState} from 'react';
import {createPortal} from 'react-dom';
import './label-ribbon-inspector.css';

let pinnedInspector:{id:string;close:()=>void}|null=null;

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
  const scroll=(event:Event)=>{if((interactive||current.current?.pinned)&&event.target instanceof Node&&popup.current?.contains(event.target))return;const target=anchor.current;if(target&&(target===document.activeElement||target.contains(document.activeElement))){const rect=target.getBoundingClientRect();setCard(value=>value?{...value,y:rect.bottom+8}:null);}else close();};
  const outside=(event:PointerEvent)=>{if(event.target instanceof Node&&!popup.current?.contains(event.target)&&!anchor.current?.contains(event.target))close();};
  const navigate=()=>close();
  window.addEventListener('keydown',key);window.addEventListener('scroll',scroll,true);window.addEventListener('resize',navigate);window.addEventListener('popstate',navigate);window.addEventListener('pharos:workspace-history',navigate);if(interactive||card.pinned)document.addEventListener('pointerdown',outside);
  return()=>{window.removeEventListener('keydown',key);window.removeEventListener('scroll',scroll,true);window.removeEventListener('resize',navigate);window.removeEventListener('popstate',navigate);window.removeEventListener('pharos:workspace-history',navigate);document.removeEventListener('pointerdown',outside);};
 },[!!card,card?.pinned,interactive]);
 const show=(target:Element,content:React.ReactNode,x?:number,pinned=false,focus=false)=>{if(pinnedInspector&&!pinned)return;if(pinned){if(pinnedInspector?.id!==id)pinnedInspector?.close();pinnedInspector={id,close};}keep();anchor.current=target;const rect=target.getBoundingClientRect(),next={x:Math.max(8,Math.min(x??rect.left,window.innerWidth-Math.min(interactive?300:340,window.innerWidth-16)-8)),y:rect.bottom+8,content,pinned,focus};current.current=next;setCard(next);};
 const pin=(target:Element,content:React.ReactNode,x?:number,focus=false)=>show(target,content,x,true,focus);
 const leave=()=>{if(current.current?.pinned)return;if(!interactive){const target=anchor.current;if(!target||!(target===document.activeElement||target.contains(document.activeElement)))close();return;}const target=anchor.current;if(!target||!(target===document.activeElement||target.contains(document.activeElement))){keep();pending.current=setTimeout(()=>{pending.current=undefined;setCard(null);},150);}};
 const portal=card?createPortal(<div id={id} role={card.pinned?'group':'tooltip'} onPointerDown={event=>event.stopPropagation()} onClick={event=>event.stopPropagation()} aria-label={card.pinned?'Ribbon segment details':undefined} className={`lt-ribbon-inspector${interactive?' is-interactive':''}${card.pinned?' is-pinned':''}`} onPointerEnter={keep} onPointerLeave={leave} ref={node=>{popup.current=node;if(node){const height=node.getBoundingClientRect().height;node.style.top=`${Math.max(8,Math.min(card.y,window.innerHeight-height-8))}px`;if(card.focus&&!node.contains(document.activeElement))node.querySelector<HTMLElement>('a,button')?.focus({preventScroll:true});}}} style={{left:card.x,top:card.y}}>{card.content}</div>,document.body):null;
 return {id,show,pin,close,leave,portal};
}
