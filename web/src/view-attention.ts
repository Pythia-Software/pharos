import { libraryHostParameters } from "./host-query-state";
import { readViewState, readClassicViewState, writeViewState, writeViewStates, installClassicDisclosures } from "./view-state";
import { attentionTargets, viewContract, viewFields, retainPageView, viewPageForURL } from "./view-contract";
import { adoptTextTargets, installPageFind } from "./page-find";
import { attentionMutationMatters, attentionReferenceAt, attentionTargetNoun, createDetailLanding, toggleAttentionTarget, type AttentionNode } from "./view-attention-model";
import "./view-attention.css";

type Resolver = (target: string, initial: boolean) => HTMLElement | null;
const resolvers = new Set<Resolver>();
export function registerAttentionResolver(resolve: Resolver): () => void { resolvers.add(resolve); schedule(); return () => { resolvers.delete(resolve); }; }
const detailLanding = createDetailLanding(() => schedule());
const cancelDetailLanding = detailLanding.cancel;
export const registerDetailAttentionResolver = detailLanding.register;
export function targetReference(kind: string, id: string): string {
  if (!attentionTargets.some(target => target.pattern.startsWith(`${kind}:<`)) || !id || id.length > 4000) throw new Error("Unknown attention target.");
  return `${kind}:${id}`;
}
declare global { interface Window { pharosView: { read: typeof readViewState; write: typeof writeViewState; writeAll: typeof writeViewStates; register: typeof registerAttentionResolver; contract: typeof viewContract; refresh: () => void; restoreDisclosures: ReturnType<typeof installClassicDisclosures>; globalParameters: () => string[]; retainPage: (params: URLSearchParams, path: string) => void; contextTarget: (element: Element) => ContextTarget | null; toggleAttention: (reference: string) => void }; } }
type ContextTarget = { reference: string; noun: string; highlighted: boolean };
let frame = 0, navigation = "", landed = false, stayPut = false;
const opened = new Set<string>();
let marked = new Set<HTMLElement>();
let status:HTMLElement|undefined,statusText:HTMLElement|undefined;
const available = (element: HTMLElement) => element.isConnected && !element.closest("[hidden], [inert]") && (!element.closest(".view") || element.closest(".view")!.classList.contains("active"));
const visible = (element: HTMLElement) => available(element) && element.getClientRects().length > 0;
function currentAttention() {
  const params = new URLSearchParams(location.search);
  return { highlights: [...new Set(params.getAll("highlight"))], reveal: params.get("reveal") ?? "" };
}
function resolve(reference: string, initial = !opened.has(reference)): HTMLElement | null {
  for (const resolver of resolvers) { const element = resolver(reference, initial); if (element && available(element)) return element; }
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
// The attention target under a right-click, for the Mac app's context menu.
function contextTarget(element: Element): ContextTarget | null {
  const reference = attentionReferenceAt(element as unknown as AttentionNode, (reference, node) => resolve(reference, false) === node as unknown as HTMLElement);
  if (!reference) return null;
  const { highlights, reveal } = currentAttention();
  return { reference, noun: attentionTargetNoun(reference), highlighted: reveal === reference || highlights.includes(reference) };
}
// The target is on screen already (it was just right-clicked), so marking it
// does not scroll.
function toggleAttention(reference: string) {
  const url = new URL(location.href); url.search = toggleAttentionTarget(url.search, reference).toString();
  stayPut = true;
  history.replaceState(history.state, "", url); window.dispatchEvent(new Event("pharos:uri-changed"));
}
function update() {
  frame=0;
  // text: targets are an alias for page Find, which owns its own landing.
  if(adoptTextTargets())return schedule();
  const { highlights,reveal }=currentAttention();
  const key=JSON.stringify([location.pathname,new URLSearchParams(location.search).get("conversation"),highlights,reveal]);
  if(key!==navigation) {navigation=key;landed=stayPut;opened.clear();}
  stayPut=false;
  const next=new Set<HTMLElement>();const missing:string[]=[];
  if (!reveal.startsWith("detail:")) cancelDetailLanding();
  for(const reference of [...highlights,...(reveal?[reveal]:[])]) {
    if(reference.startsWith("detail:")) {
      if (reference !== reveal || !detailLanding.resolve(reference)) missing.push(reference);
      continue;
    }
    const element=resolve(reference);if(!element){missing.push(reference);continue;}
    if(!opened.has(reference)){openAncestors(element);opened.add(reference);}
    element.classList.add(reference===reveal?"pharos-reveal":"pharos-highlight");next.add(element);
    if(reference===reveal && !landed && visible(element)) {landed=true;requestAnimationFrame(()=>{if(key===navigation&&element.isConnected)scroll(element)});}
  }
  for(const element of marked) {if(!next.has(element))element.classList.remove("pharos-highlight","pharos-reveal");else {if(resolve(reveal)!==element)element.classList.remove("pharos-reveal");}}
  marked=next;
  if(highlights.length || reveal) {
    if(!status){status=document.createElement("aside");status.className="pharos-attention-status";status.setAttribute("aria-label","Linked highlights");statusText=document.createElement("span");statusText.setAttribute("role","status");const clear=document.createElement("button");clear.type="button";clear.textContent="Clear highlights";clear.onclick=clearAttention;status.append(statusText,clear);document.body.append(status);}
    const message=missing.length ? `Targets not available in this view yet: ${missing.join(", ")}` : "Linked highlights";
    if(statusText!.textContent!==message)statusText!.textContent=message;
  }else{status?.remove();status=statusText=undefined;}
  // The pending list is observable without blocking the rest of the page.
  document.documentElement.dataset.pharosAttentionMissing=missing.join("\n");
  document.documentElement.dataset.pharosAttentionPending=detailLanding.pending ? "1" : "";
  document.documentElement.dataset.pharosNativeReveal=reveal.startsWith("detail:") ? "true" : "";
}
function schedule(){if(!frame) frame=requestAnimationFrame(update);}
export function installViewAttention() {
  const restoreDisclosures=installClassicDisclosures();
  window.pharosView={restoreDisclosures,read:readClassicViewState,write:writeViewState,writeAll:writeViewStates,register:registerAttentionResolver,contract:viewContract,refresh:schedule,globalParameters:()=>Object.values(viewFields).filter(field=>field.pages.includes("*")).map(field=>field.param),retainPage:(params,path)=>retainPageView(params,viewPageForURL(new URL(path,location.origin)),path.startsWith("/work/")?libraryHostParameters:[]),contextTarget,toggleAttention};
  for(const event of ["popstate","pharos:route","pharos:view"])window.addEventListener(event,()=>{navigation="";cancelDetailLanding();schedule();});
  window.addEventListener("pharos:uri-changed",schedule);
  const observer=new MutationObserver(records=>{
    const attention=currentAttention();if(!attention.reveal&&!attention.highlights.length)return;
    if(records.some(record => attentionMutationMatters(record, marked))) schedule();
  });
  observer.observe(document.body,{childList:true,subtree:true,attributes:true,attributeOldValue:true,attributeFilter:["class","hidden","open","data-pharos-target","data-view-field"]});
  installPageFind();
  window.dispatchEvent(new Event("pharos:view-ready"));schedule();
}
