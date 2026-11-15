import {readerGeometry,readerVisibleWindow,type ReaderNavigatorData,type ReaderSpan} from './reader-navigator-model';
import type {TimeRange} from './timeline-label-model';
import type {TimeScale} from './timeline-idle-model';

/** One reader owns all geometry. No transcript queries or marker writes on
 * scroll; the scroll path reads the offset and moves one shared indicator.
 * Resize/toggle/filter/materialization invalidate the cached layout. */
export function trackReaderViewport(panel:HTMLElement,host:HTMLElement,data:ReaderNavigatorData,byID:Map<string,TimeRange>,scale:TimeScale){
  const list=panel.querySelector<HTMLElement>('.turn-list')!,scroller=panel.closest<HTMLElement>('.workspace-reader-content')!;
  const band=host.querySelector<HTMLElement>('.reader-navigator-window')!,edges=[...host.querySelectorAll<HTMLElement>('.reader-navigator-boundary')];
  let frame=0,stale=true,disposed=false,geometry=readerGeometry([]),origin=0,viewTop=0,viewBottom=0,trackWidth=1,last='';
  const measure=(scroll:number)=>{
    const root=scroller.getBoundingClientRect(),sticky=host.getBoundingClientRect();
    origin=root.top;
    viewTop=Math.max(0,sticky.height);viewBottom=scroller.clientHeight;trackWidth=Math.max(1,host.querySelector<HTMLElement>('.reader-navigator-window-track')!.clientWidth);
    const spans:ReaderSpan[]=[],effective=new WeakMap<HTMLElement,HTMLElement|null>();
    const target=(element:HTMLElement):HTMLElement|null=>{
      if(element===list)return list;
      if(effective.has(element))return effective.get(element)!;
      const parent=element.parentElement,parentTarget=parent?target(parent):null;
      const value=!parentTarget||element.hidden||element.matches('[data-show]')&&getComputedStyle(element).display==='none'?null:parentTarget!==parent?parentTarget:element instanceof HTMLDetailsElement&&!element.open?element.querySelector<HTMLElement>(':scope > summary'):element;
      effective.set(element,value);return value;
    };
    const bounds=new Map<HTMLElement,TimeRange>();
    const add=(element:HTMLElement,id:string)=>{
      const time=byID.get(id),shown=target(element);if(!time||!shown)return;
      const previous=bounds.get(shown);bounds.set(shown,previous?[Math.min(previous[0],time[0]),Math.max(previous[1],time[1])]:[...time]);
    };
    for(const turn of data.turns){
      const rendered=new Map<string,HTMLElement>();
      for(const element of turn.target.querySelectorAll<HTMLElement>('[data-message-id],.tool-event,[data-pharos-find-pending]')){
        const row=element.hasAttribute('data-pharos-find-pending')?element:element.closest<HTMLElement>('.timeline-entry')??element;
        if(element.dataset.messageId&&!rendered.has(element.dataset.messageId))rendered.set(element.dataset.messageId,row);
        for(const message of (element as HTMLElement&{sourceMessages?:{id:string}[]}).sourceMessages??[])if(!rendered.has(message.id))rendered.set(message.id,row);
        // The same lazy source closure used by Find assigns each skipped
        // page its own records, including separate earlier/later gaps after
        // a deep jump. Reading it never materializes transcript content.
        if(element.hasAttribute('data-pharos-find-pending'))for(const message of (element as HTMLElement&{pharosSourceMessages?:()=>{id:string}[]}).pharosSourceMessages?.()??[])if(!rendered.has(message.id))rendered.set(message.id,row);
      }
      // Hidden/unmaterialized records describe their collapsed summary or
      // remaining page control, preserving navigation without rendering them.
      const fallback=turn.target.open?turn.target.querySelector<HTMLElement>('.sequence-more')??turn.target.querySelector<HTMLElement>(':scope > summary')!:turn.target;
      for(const id of turn.messages)add(rendered.get(id)??fallback,id);
    }
    for(const [element,[start,end]] of bounds){const rect=element.getBoundingClientRect();spans.push({top:rect.top-origin+scroll,bottom:rect.bottom-origin+scroll,start,end});}
    geometry=readerGeometry(spans);
  };
  const update=()=>{
    frame=0;if(disposed||!host.isConnected)return;
    const scroll=scroller.scrollTop,relaid=stale;
    if(stale){stale=false;measure(scroll);}
    const range=readerVisibleWindow(geometry,scroll+viewTop,scroll+viewBottom);
    const key=range?.join(':')??'';if(key===last&&!relaid)return;last=key;
    // All geometry reads finish before these writes. The fill and two edges
    // occupy their own compositor layers, so movement never resizes the SVG.
    if(relaid)scroller.style.setProperty('--reader-navigator-height',`${viewTop}px`);
    band.hidden=!range;for(const edge of edges)edge.hidden=!range;
    if(range){
      const left=Math.max(0,Math.min(1,scale.fraction(range[0]))),right=Math.max(left,Math.min(1,scale.fraction(range[1])));
      band.dataset.position=String((left+right)/2);
      const width=Math.min(1,Math.max(3/trackWidth,right-left)),start=Math.min(left,1-width);
      band.style.transform=`translateX(${start*100}%) scaleX(${width})`;
      edges[0].style.transform=`translateX(${start*100}%)`;
      edges[1].style.transform=`translateX(calc(${(start+width)*100}% - 2px))`;
    }
  };
  const schedule=()=>{if(!frame)frame=requestAnimationFrame(update);};
  const invalidate=()=>{stale=true;schedule();};
  const observer=new ResizeObserver(invalidate);
  // A sibling overview may grow without resizing the transcript; individual
  // turns may exchange height without changing the list's total height.
  for(let element:HTMLElement|null=panel;element&&element!==scroller;element=element.parentElement)observer.observe(element);
  observer.observe(scroller);observer.observe(host);observer.observe(list);
  for(const child of scroller.children)observer.observe(child);
  for(const turn of data.turns)observer.observe(turn.target);
  const mutations=new MutationObserver(invalidate);mutations.observe(list,{childList:true,subtree:true,attributes:true,attributeFilter:['hidden']});
  const events=['toggle','input','transcript-filter','transcript-materialized'];
  for(const name of events)panel.addEventListener(name,invalidate,true);
  scroller.addEventListener('scroll',schedule,{passive:true});window.addEventListener('resize',invalidate);schedule();
  return()=>{disposed=true;cancelAnimationFrame(frame);observer.disconnect();mutations.disconnect();for(const name of events)panel.removeEventListener(name,invalidate,true);scroller.removeEventListener('scroll',schedule);window.removeEventListener('resize',invalidate);scroller.style.removeProperty('--reader-navigator-height');};
}
