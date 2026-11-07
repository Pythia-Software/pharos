import React,{useEffect,useLayoutEffect,useMemo,useRef,useSyncExternalStore} from 'react';
import {createPortal} from 'react-dom';
import {Icon,type IconName} from './icons';
import {readinessDial} from './data-readiness-icon';
import {PageLink,useLocationRevision} from './nested-pages';
import {useViewState} from './view-state';
import {readinessAge,readinessModel,readinessPreview,readinessScenarios,readinessSettingsURL,readinessStageDescription,readinessStageState,readinessProgressText,timeValue,type ReadinessScope,type ReadinessModel,type ReadinessStage,type ReadinessScenario} from './data-readiness-model';
import {displayedReadinessScopes,processingState,readinessRevision,registerReadiness,subscribeReadiness} from './data-readiness-store';
import './data-readiness.css';
const useProcessing=()=>useSyncExternalStore(subscribeReadiness,readinessRevision);
export function openDataReadiness(id='page',invoker?:HTMLElement){window.dispatchEvent(new CustomEvent('pharos:open-readiness',{detail:{id,invoker}}));}
function scenario():ReadinessScenario | null {const value=new URLSearchParams(location.search).get('readiness_preview');return readinessScenarios.some(([id])=>id===value)?value as ReadinessScenario:null;}
function modelFor(scopes:ReadinessScope[]){const state=processingState();return readinessPreview(readinessModel(state.snapshot,scopes,Date.now(),state.unavailable),scenario());}
// Shared 24px sprite artwork surrounds a quantitative, filled 12-hour dial.
// Only current data gets a check; unmeasured lag never invents a slice.
export function FreshnessIcon({model}:{model:ReadinessModel}) {
  const dial=readinessDial(model);
  const badge:IconName=dial.kind==='lag'?'freshness-clock':`freshness-${dial.kind}`;
  return <svg className="app-icon data-freshness-icon" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
    <use href="#ph-icon-freshness-database"/>
    {dial.kind==='lag'&&(dial.fraction===1?<circle cx="16" cy="16" r="5" fill="currentColor" stroke="none"/>:dial.path?<path d={dial.path} fill="currentColor" stroke="none"/>:null)}
    <use href={`#ph-icon-${badge}`}/>
  </svg>;
}

// These indicators register only mounted, visible result boundaries. Hidden
// React pages remain mounted in Pharos, so DOM visibility is checked at read
// time as well as disposing registrations on unmount.
export function DataReadinessIndicator({id,title,dataset,query,updatedAt,refreshing,refreshError,empty,className=''}:ReadinessScope&{className?:string}){
  const ref=useRef<HTMLButtonElement>(null),key=JSON.stringify([id,title,dataset,query,updatedAt,refreshing,refreshError,empty]);
  useProcessing();useLocationRevision();
  const scope=useMemo(()=>({id,title,dataset,query,updatedAt,refreshing,refreshError,empty}),[key]);
  useLayoutEffect(()=>registerReadiness(scope,ref.current!),[scope]);
  const model=modelFor([scope]);
  return <button ref={ref} type="button" data-view-field="readiness_scope" data-readiness-result={id} className={`data-readiness-indicator ${className}`} data-tone={model.tone} aria-label={`${title}: ${model.label}. ${model.sentence}`} title={`${model.label}. ${model.sentence}`} aria-haspopup="dialog" aria-controls="dataReadinessDialog" onClick={event=>{event.preventDefault();event.stopPropagation();openDataReadiness(id,event.currentTarget);}}><FreshnessIcon model={model}/></button>;
}

export function HeaderDataReadiness(){
  useProcessing();useLocationRevision();
  const host=document.getElementById('dataReadinessToggle');
  const model=modelFor(displayedReadinessScopes());
  useEffect(()=>{
    if(!host)return;
    const click=()=>openDataReadiness('page',host);
    host.addEventListener('click',click);return()=>host.removeEventListener('click',click);
  },[host]);
  useLayoutEffect(()=>{if(!host)return;host.dataset.tone=model.tone;host.title=`${model.label}. ${model.sentence}`;host.setAttribute('aria-label',`Data updates: ${model.label}. ${model.sentence}`);},[host,model.label,model.sentence,model.tone]);
  return host?createPortal(<FreshnessIcon model={model}/>,host):null;
}
const clock=(at:number|null)=>at===null?'Time unavailable':new Date(at).toLocaleString(undefined,{...(new Date(at).toDateString()===new Date().toDateString()?{}:{month:'short',day:'numeric'}),hour:'numeric',minute:'2-digit'});
function StageProgress({stage}:{stage:ReadinessStage}){
  const step=stage.operation?.step,done=step?.done??stage.done,total=step?.total??stage.total;
  const hasTotal=typeof total==='number'&&Number.isFinite(total)&&total>=0;
  const hasDone=typeof done==='number'&&Number.isFinite(done)&&done>=0;
  const amount=readinessProgressText(stage);
  const value=hasTotal&&hasDone?(total?Math.min(100,Math.round(done/total*100)):0):null;
  return <div className="data-readiness-progress"><div><span>{amount}</span>{value!==null?<span>{value}%</span>:null}</div><div className={`pharos-status-bar${value===null?' indeterminate waiting':''}`} role="progressbar" aria-label={stage.label} {...value===null?{'aria-valuetext':amount}:{'aria-valuenow':value,'aria-valuemin':0,'aria-valuemax':100}}><i style={{width:value===null?'30%':value+'%'}}/></div></div>;
}
function ProgressList({model}:{model:ReadinessModel}){return <ul className="data-readiness-stages">{model.stages.map(stage=>{const description=readinessStageDescription(stage);return <li key={stage.id}><div><strong>{stage.label}</strong><span>{readinessStageState(stage)}</span></div>{stage.state!=='current'?<StageProgress stage={stage}/>:null}{stage.state!=='current'&&(description||stage.nextAt)?<p>{description}{stage.nextAt?`${description?' · ':''}Next planned check: ${clock(timeValue(stage.nextAt))}`:''}</p>:null}</li>;})}</ul>;}
// A first analysis is not a zero-row query. Use the existing library empty
// surface and the same stage list; never enqueue work from this surface.
export function DataReadinessEmpty({id,title,dataset,query,empty,children}:ReadinessScope&{children:React.ReactNode}){
  useProcessing();useLocationRevision();
  const ref=useRef<HTMLElement>(null),key=JSON.stringify([id,title,dataset,query,empty]);
  const scope=useMemo(()=>({id,title,dataset,query,empty}),[key]);
  const model=modelFor([scope]),missing=model.kind==='missing'&&(model.preview||empty===true);
  useLayoutEffect(()=>{if(missing&&ref.current)return registerReadiness(scope,ref.current);},[scope,missing]);
  return missing?<section ref={ref} className="library-empty-card data-readiness-empty" aria-label="No results yet"><h3>No results yet</h3><p>Pharos needs to finish these steps before it can show results.</p><ProgressList model={model}/><button type="button" className="usage-preset-clear" data-view-field="readiness_scope" data-readiness-result={id} onClick={event=>openDataReadiness(id,event.currentTarget)}>View update details <Icon name="arrow-right"/></button></section>:<>{children}</>;
}
export function DataReadinessModal(){
  useProcessing();useLocationRevision();
  const dialog=useRef<HTMLDialogElement>(null),invoker=useRef<HTMLElement|null>(null);
  const [shown,setShown]=useViewState('readiness_open',false),[scope,setScope]=useViewState('readiness_scope','page');
  // Never capture this fallback: ordinary URLs must not opt into synthetic data.
  const [preview,setPreview]=useViewState<ReadinessScenario>('readiness_preview','clear',false);
  const scopes=displayedReadinessScopes(),selected=scope==='page'?scopes:scopes.filter(item=>item.id===scope);
  const model=modelFor(selected),scopeName=scope==='page'?'This page':selected[0]?.title??'This result';
  useEffect(()=>{const show=(event:Event)=>{const context=(event as CustomEvent<{id:string;invoker?:HTMLElement}>).detail;invoker.current=context.invoker??document.activeElement as HTMLElement;setScope(context.id);setShown(true);};window.addEventListener('pharos:open-readiness',show);return()=>window.removeEventListener('pharos:open-readiness',show);},[]);
  useEffect(()=>{const element=dialog.current!;if(shown&&!element.open)element.showModal();else if(!shown&&element.open)element.close();},[shown]);
  useEffect(()=>{const element=dialog.current!;const close=()=>{setShown(false);const target=invoker.current;invoker.current=null;if(!target)return;const replacement=target.dataset.readinessResult?document.querySelector<HTMLElement>(`[data-readiness-result="${CSS.escape(target.dataset.readinessResult)}"]`):null;for(const control of [target,replacement,document.getElementById('dataReadinessToggle'),document.getElementById('headerMore')])if(control?.isConnected&&control.getClientRects().length){control.focus({preventScroll:true});break;}};element.addEventListener('close',close);return()=>element.removeEventListener('close',close);},[]);
  const previewEnabled=scenario()!==null;
  const settings=readinessSettingsURL(model);
  return <dialog ref={dialog} id="dataReadinessDialog" data-view-field="readiness_open" className="data-guide data-readiness-dialog" aria-labelledby="dataReadinessTitle" aria-describedby="dataReadinessDescription" onClick={event=>{if(event.target!==event.currentTarget)return;const r=event.currentTarget.getBoundingClientRect();if(event.clientX<r.left||event.clientX>r.right||event.clientY<r.top||event.clientY>r.bottom)event.currentTarget.close();}}>
    <header className="data-guide-header"><div><small>{model.preview?'Preview · ':''}{scopeName}</small><h2 id="dataReadinessTitle">{model.label}</h2></div><button type="button" className="data-guide-close" data-view-field="readiness_open" aria-label="Close update details" onClick={()=>dialog.current?.close()}><Icon name="close"/></button></header>
    {previewEnabled?<div className="data-readiness-preview"><label htmlFor="readinessPreviewState">Preview example</label><select id="readinessPreviewState" data-view-field="readiness_preview" value={preview} onChange={event=>setPreview(event.target.value as ReadinessScenario)}>{readinessScenarios.map(([id,label])=><option key={id} value={id}>{label}</option>)}</select></div>:null}
    <div className="data-guide-body"><p id="dataReadinessDescription" className="muted">{model.sentence}</p><dl className="data-readiness-times"><div><dt>Last checked</dt><dd>{model.checkedAt===null?'Time unavailable':readinessAge(Math.max(0,Date.now()-model.checkedAt))+' ago'}</dd></div><div><dt>Next planned check</dt><dd>{clock(model.nextAt)}</dd></div></dl>{!['current','saved'].includes(model.kind)?<ProgressList model={model}/>:null}<p className="data-readiness-evidence">{model.preview?'Example updates. Your data and settings are unchanged.':'Saved results may need a refresh to show the latest updates.'}{model.kind==='behind'&&model.lagMs!==null?' A full clock means 12 hours behind or more.':''}</p></div>
    <footer className="data-readiness-footer"><PageLink data-view-field="readiness_open" href={settings} onClick={()=>{invoker.current=null;dialog.current?.close();}}>Update settings<Icon name="arrow-right"/></PageLink></footer>
  </dialog>;
}


// The Background Processing page already owns every mutation. Reuse the same
// activity model and load row as the drive panel above its existing controls.
type ActivityModel={lines:Array<{id:string;label:string;currentLabel?:string;primary?:string[];secondary?:string[];fraction?:number|null}>;load?:{metrics:Array<{id:string;label:string;value:string;warning?:string}>}|null;available?:boolean};
declare global {interface Window {pharosStatusPanel?:{panelModel(view:unknown):ActivityModel};}}
export function BackgroundProcessingWorkload(){
  useProcessing();useLocationRevision();
  if(location.pathname!=='/settings/sync')return null;
  const view=window.pharosStatusStore?.current()?.view;
  const model=window.pharosStatusPanel?.panelModel(view);
  const others=(view as {others?:Array<{id:string;name:string;operations?:Array<{id:string;label:string;state:string}>}>}|undefined)?.others??[];
  return <section className="bg-card data-readiness-workload" aria-label="Updates running now"><h3>Running now</h3>{model?.lines.length?<ul>{model.lines.map(line=><li key={line.id}><strong>{line.currentLabel||line.label}</strong><span>{[...line.primary??[],...line.secondary??[]].join(' · ')}</span>{line.fraction!==null&&line.fraction!==undefined?<div className="pharos-status-bar"><i style={{width:Math.min(100,line.fraction*100)+'%'}}/></div>:null}</li>)}</ul>:<p className="muted">{model?.available?'No updates are running for this library.':'Update progress is unavailable.'}</p>}
    {others.flatMap(library=>(library.operations??[]).filter(operation=>['running','queued','blocked'].includes(operation.state)).map(operation=><p className="muted" key={library.id+':'+operation.id}>{library.name}: {operation.label}</p>))}
    {model?.load?<div className="pharos-status-load">{model.load.metrics.map(metric=><span key={metric.id} title={metric.warning}><span>{metric.label}</span><b>{metric.value}</b></span>)}</div>:null}
  </section>;
}
