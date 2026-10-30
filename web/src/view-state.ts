import { useCallback, useEffect, useState, useSyncExternalStore, useMemo } from "react";
import { encodeViewValue, parseViewValue, viewFields, viewFieldApplies, viewPageForURL } from "./view-contract";

export function readViewState<T>(name: string, fallback: T, params = new URLSearchParams(location.search)): T {
  const field = viewFields[name];
  if (!field) throw new Error(`Unregistered view field: ${name}`);
  const values = params.getAll(field.param);
  if (!values.length) return fallback;
  if (values.length !== 1) throw new Error(`Repeated view field: ${field.param}`);
  return parseViewValue(field, values[0]) as T;
}
// Classic hosts must still mount when a copied URL is truncated or malformed.
// Query/search owners retain strict reads so an invalid query never broadens.
const reportedClassicValues = new Set<string>();
export function readClassicViewState<T>(name: string, fallback: T, params = new URLSearchParams(location.search)): T {
  if (!viewFields[name]) throw new Error(`Unregistered view field: ${name}`);
  try { return readViewState(name, fallback, params); }
  catch {
    const key=JSON.stringify([name,params.getAll(viewFields[name].param)]);
    if(!reportedClassicValues.has(key)) {
      if(reportedClassicValues.size>=100)reportedClassicValues.clear();
      reportedClassicValues.add(key);
      window.dispatchEvent(new CustomEvent("pharos:invalid-view",{detail:{name}}));
    }
    return fallback;
  }
}
export function writeViewState(name: string, value: unknown): void {
  const field = viewFields[name];
  if (!field) throw new Error(`Unregistered view field: ${name}`);
  const url = new URL(location.href);
  if(!viewFieldApplies(name,url))return;
  const raw = encodeViewValue(field, value);
  if (url.searchParams.get(field.param) === raw) return;
  url.searchParams.set(field.param, raw);
  history.replaceState(history.state, "", url);
  window.dispatchEvent(new Event("pharos:uri-changed"));
}
// All view state uses the same read/write and navigation lifecycle. A linked
// value wins over a saved preference. On an active page write effective values
// (including false/empty), so a copy overrides a different browser's defaults.
const subscribe = (changed: () => void) => {
  const events = ["popstate", "pharos:route", "pharos:view", "pharos:uri-changed"];
  for (const event of events) window.addEventListener(event, changed);
  return () => { for (const event of events) window.removeEventListener(event, changed); };
};
export function useViewState<T>(name: string, fallback: T | (() => T), active = true): [T, React.Dispatch<React.SetStateAction<T>>] {
  const [initial] = useState<T>(fallback);
  const field = viewFields[name];
  if (!field) throw new Error(`Unregistered view field: ${name}`);
  const raw = useSyncExternalStore(subscribe, () => JSON.stringify(new URLSearchParams(location.search).getAll(field.param)));
  const value = useMemo(() => {const values=JSON.parse(raw) as string[];if(values.length>1)throw new Error(`Repeated view field: ${field.param}`);return values.length ? parseViewValue(field,values[0]) as T : initial;}, [raw, initial, field]);
  useEffect(() => { if (active && raw === "[]") return captureViewDefault(name,initial); }, [name, active, raw, initial]);
  const update = useCallback<React.Dispatch<React.SetStateAction<T>>>(change => {
    const previous = readViewState(name, initial);
    writeViewState(name, typeof change === "function" ? (change as (p: T) => T)(previous) : change);
  }, [name, initial]);
  return [value, update];
}

// Capture viewer-dependent defaults together. Fixed defaults need no entry;
// user changes still write immediately. Unmounted/other-page captures expire.
type Capture={name:string;key?:string;value:unknown;page:string;path:string};
const captures = new Map<symbol,Capture>();
let captureFrame=0;
function captureViewDefault(name:string,value:unknown):()=>void {return captureViewValue(name,value);}
export function captureViewEntry(name:string,key:string,value:unknown):()=>void {return captureViewValue(name,value,key);}
function captureViewValue(name:string,value:unknown,key?:string):()=>void {
  const token=Symbol(),url=new URL(location.href);
  captures.set(token,{name,key,value,page:viewPageForURL(url),path:url.pathname});
  if(!captureFrame)captureFrame=requestAnimationFrame(()=>{
    captureFrame=0;const pending=[...captures.values()];captures.clear();
    const url=new URL(location.href);let changed=false;
    for(const entry of pending){
      if(!viewFieldApplies(entry.name,url))continue;
      if(entry.key===undefined){if(!url.searchParams.has(viewFields[entry.name].param)){url.searchParams.set(viewFields[entry.name].param,encodeViewValue(viewFields[entry.name],entry.value));changed=true;}continue;}
      if(entry.path!==url.pathname||entry.page!==viewPageForURL(url))continue;
      const values=readViewState<Record<string,unknown>>(entry.name,{},url.searchParams);
      if(Object.hasOwn(values,entry.key))continue;
      url.searchParams.set(viewFields[entry.name].param,encodeViewValue(viewFields[entry.name],{...values,[entry.key]:entry.value}));changed=true;
    }
    if(changed){history.replaceState(history.state,"",url);window.dispatchEvent(new Event("pharos:uri-changed"));}
  });
  return ()=>{captures.delete(token);};
}

// Repeated components keep state by stable content identity, never DOM order.
export function useViewEntry<T>(name: string, key: string, fallback: T, active = true, {captureInitial = false}: {captureInitial?: boolean} = {}): [T, React.Dispatch<React.SetStateAction<T>>] {
  const [entries, setEntries] = useViewState<Record<string,T>>(name, {}, active);
  const value = Object.hasOwn(entries,key) ? entries[key] : fallback;
  const capture=captureInitial||viewFields[name].captureEntries;
  useEffect(()=>{
    if(active && capture && !Object.hasOwn(entries,key))return captureViewEntry(name,key,fallback);
  },[active,capture,key,entries,fallback,setEntries]);
  const update = useCallback<React.Dispatch<React.SetStateAction<T>>>(change => setEntries(previous => {
    const old = Object.hasOwn(previous,key) ? previous[key] : fallback;
    return {...previous,[key]:typeof change === "function" ? (change as (p:T)=>T)(old) : change};
  }), [key, fallback, setEntries]);
  return [value, update];
}

/** Classic-script content opts into the same disclosure map with a stable key. */
export function installClassicDisclosures() {
  const known=new WeakMap<HTMLDetailsElement,boolean>();
  const defaults=new WeakMap<HTMLDetailsElement,boolean>();
  const restore=(root: ParentNode=document, force=false)=>{
    const values=readViewState<Record<string,boolean>>("disclosures",{});
    const elements=[...(root instanceof HTMLDetailsElement && root.dataset.viewKey ? [root] : []),...root.querySelectorAll<HTMLDetailsElement>("details[data-view-key]")];
    for(const element of elements){if(!force&&known.has(element))continue;if(!defaults.has(element))defaults.set(element,element.open);const key=element.dataset.viewKey!;element.open=Object.hasOwn(values,key)?values[key]:defaults.get(element)!;known.set(element,element.open);}
  };
  document.addEventListener("toggle",event=>{
    const element=event.target;if(!(element instanceof HTMLDetailsElement)||!element.dataset.viewKey)return;
    if(known.get(element)===element.open)return;known.set(element,element.open);
    const values=readViewState<Record<string,boolean>>("disclosures",{}),key=element.dataset.viewKey;
    if(values[key]!==element.open)writeViewState("disclosures",{...values,[key]:element.open});
  },true);
  const observer=new MutationObserver(records=>{for(const record of records)for(const node of record.addedNodes)if(node instanceof Element)restore(node);});
  observer.observe(document.body,{subtree:true,childList:true});
  for(const event of ["popstate","pharos:route","pharos:view"])window.addEventListener(event,()=>restore(document,true));
  restore();
}
