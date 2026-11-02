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

/** Classic content uses stable keys for both native and button disclosures. */
export function installClassicDisclosures() {
  type Disclosure = HTMLDetailsElement | HTMLButtonElement;
  const selector = "details[data-view-key],button[data-view-key][aria-controls]";
  const known = new WeakMap<Disclosure, boolean>(), defaults = new WeakMap<Disclosure, boolean>();
  const isOpen = (element: Disclosure) => element instanceof HTMLDetailsElement ? element.open : element.getAttribute("aria-expanded") === "true";
  const apply = (element: Disclosure, value: boolean) => {
    const open = value && !(element instanceof HTMLButtonElement && element.disabled);
    const changed = !known.has(element) || isOpen(element) !== open;
    known.set(element, open);
    if (element instanceof HTMLDetailsElement) element.open = open;
    else {
      element.setAttribute("aria-expanded", String(open));
      const panel = document.getElementById(element.getAttribute("aria-controls")!);
      if (panel) panel.hidden = !open;
      const action = element.querySelector("[data-view-action]");
      if (action) action.textContent = open ? "Collapse" : "Expand";
      if (element.dataset.viewLabel) element.setAttribute("aria-label", `${open ? "Collapse" : "Expand"} ${element.dataset.viewLabel}`);
    }
    if (changed) element.dispatchEvent(new Event("pharos:disclosure"));
  };
  const restore = (root: ParentNode = document, force = true) => {
    const values = readClassicViewState<Record<string,boolean>>("disclosures", {});
    const elements = [...(root instanceof Element && root.matches(selector) ? [root as Disclosure] : []), ...root.querySelectorAll<Disclosure>(selector)];
    for (const element of elements) {
      if (!element.isConnected) continue;
      const key = element.dataset.viewKey!;
      // A collapsed button still exposes its control. Never put its section
      // target on the panel that apply() hides.
      const target = element instanceof HTMLDetailsElement ? element : document.getElementById(key) ?? element;
      if (!target.dataset.pharosTarget) target.dataset.pharosTarget = `section:${key}`;
      // Reconciliation can remove runtime-owned attributes on retained nodes.
      // Repair identity even when their disclosure state is already known.
      if (!force && known.has(element)) continue;
      if (!defaults.has(element)) defaults.set(element, element.dataset.viewDefault === undefined ? isOpen(element) : element.dataset.viewDefault === "true");
      const fallback = defaults.get(element)!;
      apply(element, Object.hasOwn(values, key) ? values[key] : fallback);
      const view = element.closest(".view");
      const active = !element.closest("[hidden],[inert]") && (!view || view.classList.contains("active"));
      if (active && element.hasAttribute("data-view-capture-initial") && !Object.hasOwn(values, key)) captureViewEntry("disclosures", key, fallback);
    }
  };
  const write = (element: Disclosure, open: boolean) => {
    const values = readClassicViewState<Record<string,boolean>>("disclosures", {}), key = element.dataset.viewKey!;
    if (values[key] !== open) writeViewState("disclosures", {...values, [key]: open});
  };
  document.addEventListener("toggle", event => {
    const element = event.target;
    if (!(element instanceof HTMLDetailsElement) || !element.dataset.viewKey || !element.isConnected || known.get(element) === element.open) return;
    known.set(element, element.open); write(element, element.open);
  }, true);
  document.addEventListener("click", event => {
    const element = event.target instanceof Element ? event.target.closest<HTMLButtonElement>("button[data-view-key][aria-controls]") : null;
    if (!element || element.disabled || event.defaultPrevented) return;
    const open = !isOpen(element); apply(element, open); write(element, open);
  });
  const observer = new MutationObserver(records => {
    for (const record of records) {
      if (record.type === "attributes" && record.target instanceof Element) restore(record.target, false);
      for (const node of record.addedNodes) if (node instanceof Element) restore(node, false);
    }
  });
  observer.observe(document.body, {subtree:true, childList:true, attributes:true, attributeFilter:["data-pharos-target"]});
  for (const event of ["popstate", "pharos:route", "pharos:view", "pharos:uri-changed"]) window.addEventListener(event, () => restore());
  restore();
  return restore;
}
