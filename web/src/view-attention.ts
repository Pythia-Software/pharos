import { libraryHostParameters } from "./host-query-state";
import { readViewState, readClassicViewState, writeViewState, installClassicDisclosures } from "./view-state";
import { attentionTargets, viewContract, viewFields, retainPageView, viewPageForURL } from "./view-contract";
import "./view-attention.css";

type Resolver = (target: string, initial: boolean) => HTMLElement | null;
const resolvers = new Set<Resolver>();
export function registerAttentionResolver(resolve: Resolver): () => void { resolvers.add(resolve); schedule(); return () => { resolvers.delete(resolve); }; }
type DetailResolver = (reference: string, signal: AbortSignal) => Promise<"revealed" | "unavailable" | "cancelled">;
const detailResolvers = new Set<DetailResolver>();
let detailLanding: { reference: string; controller: AbortController; status: string } | undefined;
function cancelDetailLanding() { detailLanding?.controller.abort(); detailLanding = undefined; }
export function registerDetailAttentionResolver(resolve: DetailResolver): () => void {
  // A changed provider can make a previously unavailable/cancelled target
  // resolvable. Retry on registration, not on every observation pass.
  if (detailLanding && !["pending", "revealed"].includes(detailLanding.status)) cancelDetailLanding();
  detailResolvers.add(resolve); schedule();
  return () => {
    detailResolvers.delete(resolve);
    // A data refresh can temporarily withdraw its provider. Preserve a
    // successful landing so registration cannot replay scrolling afterwards.
    if (detailLanding && detailLanding.status !== "revealed") cancelDetailLanding();
    schedule();
  };
}
function resolveDetail(reference: string): boolean {
  if (detailLanding?.reference !== reference) cancelDetailLanding();
  if (!detailLanding && detailResolvers.size) {
    const resolver = [...detailResolvers].at(-1)!;
    const request = { reference, controller: new AbortController(), status: "pending" };
    detailLanding = request;
    void resolver(reference, request.controller.signal).catch(() => "unavailable").then(status => {
      if (detailLanding !== request || request.controller.signal.aborted) return;
      request.status = status; schedule();
    });
  }
  return detailLanding?.status === "revealed";
}
export function targetReference(kind: string, id: string): string {
  if (!attentionTargets.some(target => target.pattern.startsWith(`${kind}:<`)) || !id || id.length > 4000) throw new Error("Unknown attention target.");
  return `${kind}:${id}`;
}
declare global { interface Window { pharosView: { read: typeof readViewState; write: typeof writeViewState; register: typeof registerAttentionResolver; contract: typeof viewContract; refresh: () => void; restoreDisclosures: ReturnType<typeof installClassicDisclosures>; globalParameters: () => string[]; retainPage: (params: URLSearchParams, path: string) => void }; } }
let frame = 0, navigation = "", textContext = "", landed = false;
const opened = new Set<string>();
let marked = new Set<HTMLElement>();
let status:HTMLElement|undefined,statusText:HTMLElement|undefined;
let find: HTMLElement | undefined, findInput: HTMLInputElement | undefined, findStatus: HTMLElement | undefined;
let ranges: Range[] = [], matchIndex = 0, searchText = "", textLanding = "", findText = "";
const available = (element: HTMLElement) => element.isConnected && !element.closest("[hidden], [inert]") && (!element.closest(".view") || element.closest(".view")!.classList.contains("active"));
const visible = (element: HTMLElement) => available(element) && element.getClientRects().length > 0;
function currentAttention() {
  const params = new URLSearchParams(location.search);
  return { highlights: [...new Set(params.getAll("highlight"))], reveal: params.get("reveal") ?? "" };
}
function resolve(reference: string): HTMLElement | null {
  for (const resolver of resolvers) { const element = resolver(reference, !opened.has(reference)); if (element && available(element)) return element; }
  if (reference === "page") return document.querySelector<HTMLElement>("main");
  const colon = reference.indexOf(":"), kind = reference.slice(0, colon), id = reference.slice(colon + 1);
  if (colon < 0 || !id) return null;
  const literal = [...document.querySelectorAll<HTMLElement>("[data-pharos-target]")].find(element => element.dataset.pharosTarget === reference && available(element));
  if (literal) return kind === "row" ? literal.closest<HTMLElement>("tr") ?? literal : literal;
  // Only known identity families are allowed. A URL never supplies a selector.
  const attribute = ({ card: "data-card-definition", message: "data-message-id", call: "data-call-id", session: "data-session-id", control: "data-view-field" } as Record<string,string>)[kind];
  const element = kind === "section" ? document.getElementById(id) : attribute ? [...document.querySelectorAll<HTMLElement>(`[${attribute}="${CSS.escape(id)}"]`)].find(available) ?? null : null;
  return element && available(element) ? element : null;
}
function openAncestors(element: HTMLElement) {
  // A disclosure's own summary is already visible when closed. Reveal must
  // preserve its explicit URL state while opening ancestors that hide it.
  for (let ancestor = element.parentElement; ancestor; ancestor = ancestor.parentElement) if (ancestor instanceof HTMLDetailsElement && !ancestor.open) ancestor.open = true;
}
function scroll(element: HTMLElement) {
  openAncestors(element);
  // Center small targets, but keep the heading of a tall section on screen.
  const viewport = element.closest("main")?.clientHeight ?? innerHeight;
  element.scrollIntoView({ block: element.getBoundingClientRect().height > viewport ? "start" : "center", inline: "nearest", behavior: "instant" });
  // Reveal is intentionally independent of keyboard/browser focus.
}
function clearAttention() {
  const url = new URL(location.href); url.searchParams.delete("highlight"); url.searchParams.delete("reveal");
  history.replaceState(history.state,"",url); window.dispatchEvent(new Event("pharos:attention-cleared")); window.dispatchEvent(new Event("pharos:uri-changed"));
}
function findBox(text: string) {
  if (!find) {
    find = document.createElement("aside"); find.className = "pharos-attention-find"; find.setAttribute("aria-label","Find on this page");
    const label = document.createElement("label"); label.textContent = "Find "; findInput = document.createElement("input"); findInput.type = "search"; label.append(findInput);
    findStatus = document.createElement("span"); findStatus.setAttribute("role","status");
    const button = (label: string, action: () => void) => { const element = document.createElement("button"); element.type="button";element.textContent=label;element.onclick=action;return element; };
    const move = (delta: number) => { if (!ranges.length) return; matchIndex=(matchIndex+delta+ranges.length)%ranges.length; ranges[matchIndex].startContainer.parentElement?.scrollIntoView({block:"center"}); paintText(); };
    find.append(label, button("Previous",()=>move(-1)),button("Next",()=>move(1)),findStatus,button("Clear",clearAttention));
    findInput.onchange=()=>{if(!findInput!.value){clearAttention();return;}const url=new URL(location.href);url.searchParams.set("reveal",targetReference("text",findInput!.value));history.replaceState(history.state,"",url);window.dispatchEvent(new Event("pharos:uri-changed"));};
    document.body.append(find);
  }
  if (findText!==text) {findInput!.value=text;findText=text;}
}
function paintText() {
  // CSS Custom Highlight does not rewrite message markup, handlers or IDs.
  if (typeof Highlight !== "undefined" && CSS.highlights) {
    CSS.highlights.set("pharos-matches",new Highlight(...ranges));
    CSS.highlights.set("pharos-landing",new Highlight(...(ranges[matchIndex] ? [ranges[matchIndex]] : [])));
  }
  if(findStatus) { const text = ranges.length ? `${matchIndex+1} of ${ranges.length}` : "No visible matches"; if(findStatus.textContent!==text)findStatus.textContent=text; }
}
function matchText(texts: string[]) {
  const root = document.querySelector<HTMLElement>("dialog[open],.drawer.open") ?? document.querySelector<HTMLElement>(".view.active") ?? document.querySelector<HTMLElement>("main");
  ranges=[]; if(!root || !texts.length) {paintText();return;}
  const walker=document.createTreeWalker(root,NodeFilter.SHOW_TEXT);const nodes: {node: Text;start: number;end: number}[]=[];let content="",node:Node|null;
  while((node=walker.nextNode())) { const parent=node.parentElement; if(!parent || parent.closest("script,style,textarea,input,.pharos-attention-find") || !visible(parent))continue;const start=content.length;content+=node.textContent;nodes.push({node:node as Text,start,end:content.length}); }
  // RegExp supplies offsets into the original text. Lowercasing the entire
  // string can change its length (for example İ), corrupting Range offsets.
  const escaped=texts.map(text=>text.replace(/[.*+?^${}()|[\]\\]/g,"\\$&")).join("|");
  for(const match of content.matchAll(new RegExp(escaped,"giu"))) {
    if(ranges.length>=1000)break;
    const start=match.index!,end=start+match[0].length;
    const first=nodes.find(entry=>entry.end>start),last=nodes.find(entry=>entry.end>=end);
    if(first&&last){const range=new Range();range.setStart(first.node,start-first.start);range.setEnd(last.node,end-last.start);ranges.push(range)}
  }
  matchIndex=Math.min(matchIndex,Math.max(0,ranges.length-1));paintText();
}
function update() {
  frame=0;
  const { highlights,reveal }=currentAttention();
  const key=JSON.stringify([location.pathname,new URLSearchParams(location.search).get("conversation"),highlights,reveal]);
  if(key!==navigation) {navigation=key;landed=false;matchIndex=0;opened.clear();}
  const context=JSON.stringify([location.pathname,new URLSearchParams(location.search).get("conversation")]);
  if(context!==textContext){textContext=context;searchText="";textLanding="";}
  const next=new Set<HTMLElement>();const missing:string[]=[];
  if (!reveal.startsWith("detail:")) cancelDetailLanding();
  for(const reference of [...highlights,...(reveal?[reveal]:[])]) {
    if(reference.startsWith("text:")) continue;
    if(reference.startsWith("detail:")) {
      if (reference !== reveal || !resolveDetail(reference)) missing.push(reference);
      continue;
    }
    const element=resolve(reference);if(!element){missing.push(reference);continue;}
    if(!opened.has(reference)){openAncestors(element);opened.add(reference);}
    element.classList.add(reference===reveal?"pharos-reveal":"pharos-highlight");next.add(element);
    if(reference===reveal && !landed && visible(element)) {landed=true;requestAnimationFrame(()=>{if(key===navigation&&element.isConnected)scroll(element)});}
  }
  for(const element of marked) {if(!next.has(element))element.classList.remove("pharos-highlight","pharos-reveal");else {if(resolve(reveal)!==element)element.classList.remove("pharos-reveal");}}
  marked=next;
  const textTargets=[...new Set([reveal,...highlights].filter(target=>target.startsWith("text:") && target.length>5))];
  if(textTargets.length) {
    const texts=textTargets.map(target=>target.slice(5)), text=texts[0], signature=JSON.stringify([...texts].sort());
    // The reader searches retained, unmaterialized text too. Multiple literal
    // targets become an escaped OR search; only reveal requests a landing.
    const request={text,texts,landing:reveal.startsWith("text:"),handled:false};
    if(searchText!==signature || request.landing && textLanding!==reveal) {
      window.dispatchEvent(new CustomEvent("pharos:find-text",{detail:request}));
      if(request.handled){searchText=signature;if(request.landing)textLanding=reveal;landed ||= request.landing;}
    }
    if(!request.landing)textLanding="";
    if(searchText!==signature) findBox(text);
    matchText(texts);
    if(ranges.length && !landed && reveal.startsWith("text:")) {
      const needle=reveal.slice(5).toLocaleLowerCase();
      matchIndex=ranges.findIndex(range=>range.toString().toLocaleLowerCase()===needle);
      if(matchIndex>=0){landed=true;ranges[matchIndex].startContainer.parentElement?.scrollIntoView({block:"center"});paintText();}
      else matchIndex=0;
    }
    if(find) {const hidden=searchText===signature;if(find.hidden!==hidden)find.hidden=hidden;}
  } else {find?.remove();find=undefined;findInput=undefined;findStatus=undefined;findText=""; searchText=""; ranges=[];paintText();}
  if(highlights.length || reveal) {
    if(!status){status=document.createElement("aside");status.className="pharos-attention-status";status.setAttribute("aria-label","Linked highlights");statusText=document.createElement("span");statusText.setAttribute("role","status");const clear=document.createElement("button");clear.type="button";clear.textContent="Clear highlights";clear.onclick=clearAttention;status.append(statusText,clear);document.body.append(status);}
    const message=missing.length ? `Targets not available in this view yet: ${missing.join(", ")}` : "Linked highlights";
    if(statusText!.textContent!==message)statusText!.textContent=message;
  }else{status?.remove();status=statusText=undefined;}
  // The pending list is observable without blocking the rest of the page.
  document.documentElement.dataset.pharosAttentionMissing=missing.join("\n");
  document.documentElement.dataset.pharosAttentionPending=detailLanding?.status === "pending" ? "1" : "";
  document.documentElement.dataset.pharosNativeReveal=reveal.startsWith("detail:") ? "true" : "";
}
function schedule(){if(!frame) frame=requestAnimationFrame(update);}
export function installViewAttention() {
  const restoreDisclosures=installClassicDisclosures();
  window.pharosView={restoreDisclosures,read:readClassicViewState,write:writeViewState,register:registerAttentionResolver,contract:viewContract,refresh:schedule,globalParameters:()=>Object.values(viewFields).filter(field=>field.pages.includes("*")).map(field=>field.param),retainPage:(params,path)=>retainPageView(params,viewPageForURL(new URL(path,location.origin)),path.startsWith("/work/")?libraryHostParameters:[])};
  for(const event of ["popstate","pharos:route","pharos:view"])window.addEventListener(event,()=>{navigation="";textContext="";cancelDetailLanding();schedule();});
  for(const event of ["pharos:uri-changed","pharos:search-ready"])window.addEventListener(event,schedule);
  const observer=new MutationObserver(records=>{
    const attention=currentAttention();if(!attention.reveal&&!attention.highlights.length)return;
    const ordinaryClasses = (value: string) => value.split(/\s+/).filter(name => name && !["pharos-highlight", "pharos-reveal"].includes(name)).sort().join(" ");
    if(records.some(record => {
      // Our own Find/status UI must not schedule another attention pass.
      const owned=(node:Node)=>node instanceof Element && !!node.closest(".pharos-attention-find,.pharos-attention-status");
      if(owned(record.target) || record.target.parentElement && owned(record.target.parentElement))return false;
      if(record.type==="childList"&&[...record.addedNodes,...record.removedNodes].every(owned))return false;
      if(record.type !== "attributes" || record.attributeName !== "class") return true;
      const element = record.target as HTMLElement;
      return ordinaryClasses(record.oldValue ?? "") !== ordinaryClasses(element.getAttribute("class") ?? "") || marked.has(element) && !element.matches(".pharos-highlight,.pharos-reveal");
    })) schedule();
  });
  observer.observe(document.body,{childList:true,subtree:true,attributes:true,attributeOldValue:true,attributeFilter:["class","hidden","open","data-pharos-target","data-view-field"]});
  window.dispatchEvent(new Event("pharos:view-ready"));schedule();
}
