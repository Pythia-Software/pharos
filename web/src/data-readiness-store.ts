import type { ReadinessScope, ReadinessSnapshot } from './data-readiness-model';

declare global {
  interface Window {
    pharosStatusStore?: {current():{view?:{library:ReadinessSnapshot|null}|null;error?:unknown;mode?:string;receivedAt?:number}|null;subscribe(listener:()=>void):()=>void;start():void};
  }
}
const listeners=new Set<()=>void>();
const scopes=new Map<symbol,{scope:ReadinessScope;element:HTMLElement}>();
let revision=0,started=false;
const changed=()=>{revision++;for(const listener of listeners)listener();};
export const readinessRevision=()=>revision;
export function subscribeReadiness(listener:()=>void){listeners.add(listener);return()=>{listeners.delete(listener);};}
export function registerReadiness(scope:ReadinessScope,element:HTMLElement){
  const key=Symbol(scope.id);scopes.set(key,{scope,element});changed();
  return()=>{scopes.delete(key);changed();};
}
export function displayedReadinessScopes():ReadinessScope[]{
  const seen=new Map<string,ReadinessScope>();
  for(const {scope,element} of scopes.values()){
    if(!element.isConnected||element.closest('[hidden], [aria-hidden="true"]')||!element.getClientRects().length)continue;
    const page=element.closest('.view');if(page&&!page.classList.contains('active'))continue;
    seen.set(scope.id,scope);
  }
  return [...seen.values()];
}
export function processingState(){const state=window.pharosStatusStore?.current();return {snapshot:state?.view?.library??null,unavailable:Boolean(state?.error)||state?.mode==='stopped',receivedAt:state?.receivedAt??0};}
export function startReadiness(){
  if(started)return;started=true;
  window.setInterval(changed,60_000);
  const connect=()=>{window.pharosStatusStore?.subscribe(changed);window.pharosStatusStore?.start();changed();};
  if(window.pharosStatusStore)connect();else document.addEventListener('DOMContentLoaded',connect,{once:true});
  for(const event of ['pharos:route','pharos:view','pharos:uri-changed'])window.addEventListener(event,()=>requestAnimationFrame(changed));
}
