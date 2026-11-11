// The DOM-free parts of linked attention (view-attention.ts): native detail
// landings and which page mutations warrant another attention pass.
export type DetailStatus = "revealed" | "unavailable" | "cancelled";
export type DetailResolver = (reference: string, signal: AbortSignal) => Promise<DetailStatus>;

// A native detail target lands through the most recently registered provider.
// Without one it waits; `changed` asks the page for another pass.
export function createDetailLanding(changed: () => void) {
  const resolvers = new Set<DetailResolver>();
  let landing: { reference: string; controller: AbortController; status: string } | undefined;
  const cancel = () => { landing?.controller.abort(); landing = undefined; };
  return {
    cancel,
    get pending() { return landing?.status === "pending"; },
    register(resolve: DetailResolver): () => void {
      // A changed provider can make a previously unavailable/cancelled target
      // resolvable. Retry on registration, not on every observation pass.
      if (landing && !["pending", "revealed"].includes(landing.status)) cancel();
      resolvers.add(resolve); changed();
      return () => {
        resolvers.delete(resolve);
        // A data refresh can temporarily withdraw its provider. Preserve a
        // successful landing so registration cannot replay scrolling afterwards.
        if (landing && landing.status !== "revealed") cancel();
        changed();
      };
    },
    // True once the reference has been revealed.
    resolve(reference: string): boolean {
      if (landing?.reference !== reference) cancel();
      if (!landing && resolvers.size) {
        const resolver = [...resolvers].at(-1)!;
        const request = { reference, controller: new AbortController(), status: "pending" };
        landing = request;
        void resolver(reference, request.controller.signal).catch(() => "unavailable").then(status => {
          if (landing !== request || request.controller.signal.aborted) return;
          request.status = status; changed();
        });
      }
      return landing?.status === "revealed";
    },
  };
}

// A table offers native details only once its first result page has loaded:
// otherwise a detail can land in an off-page fallback the rows then replace.
export function nativeDetailsReady(table: { visible: boolean; restored: boolean; loading: boolean; rows?: string; schema?: string }): boolean {
  return table.visible && table.restored && !table.loading && Boolean(table.rows && table.rows !== "running") && Boolean(table.schema);
}

const attentionClasses = ["pharos-highlight", "pharos-reveal"];
const ordinaryClasses = (value: string) => value.split(/\s+/).filter(name => name && !attentionClasses.includes(name)).sort().join(" ");
// Find and the status UI must not schedule another attention pass.
const owned = (node: Node | null) => typeof (node as Element | null)?.closest === "function" && !!(node as Element).closest(".pharos-find,.pharos-attention-status,.pharos-attention-menu,.pharos-attention-dismiss");
export function attentionMutationMatters(record: MutationRecord, marked: ReadonlySet<Element>): boolean {
  if (owned(record.target) || owned(record.target.parentElement)) return false;
  if (record.type === "childList" && [...record.addedNodes, ...record.removedNodes].every(owned)) return false;
  if (record.type !== "attributes" || record.attributeName !== "class") return true;
  const element = record.target as Element;
  // Attention's own classes are not a change, unless a marked element lost them.
  return ordinaryClasses(record.oldValue ?? "") !== ordinaryClasses(element.getAttribute("class") ?? "") || marked.has(element) && !element.matches(".pharos-highlight,.pharos-reveal");
}

// The Mac app's context menu highlights what was right-clicked: the innermost
// element with a semantic identity whose reference resolves back to it (so a
// shared `data-view-field` or a repeated ID never names a different element).
// A table row is named by the marker in its trailing cell.
export interface AttentionNode { tagName: string; parentElement: AttentionNode | null; getAttribute(name: string): string | null; querySelector(selector: string): AttentionNode | null }
const identityAttributes: [string, string][] = [["data-card-definition", "card"], ["data-message-id", "message"], ["data-call-id", "call"], ["data-session-id", "session"], ["data-view-field", "control"]];
function referencesOf(node: AttentionNode): string[] {
  const references: string[] = [];
  const literal = node.getAttribute("data-pharos-target");
  if (literal) references.push(literal);
  for (const [attribute, kind] of identityAttributes) { const id = node.getAttribute(attribute); if (id) references.push(`${kind}:${id}`); }
  if (node.tagName === "TR") { const row = node.querySelector('[data-pharos-target^="row:"]')?.getAttribute("data-pharos-target"); if (row) references.push(row); }
  return references;
}
export function attentionReferenceAt<T extends AttentionNode>(start: T | null, resolvesTo: (reference: string, node: T) => boolean): string | null {
  for (let node = start; node; node = node.parentElement as T | null) {
    const reference = referencesOf(node).find(reference => resolvesTo(reference, node!));
    if (reference) return reference;
  }
  return null;
}

const targetNouns: Record<string, string> = { card: "Card", message: "Message", call: "Tool Call", session: "Conversation", row: "Row", section: "Section", control: "Control", safety: "Review" };
export function attentionTargetNoun(reference: string): string { return targetNouns[reference.slice(0, reference.indexOf(":"))] ?? "Element"; }

// Highlighting adds the target as the link's landing (`reveal`) when it has
// none, and as another `highlight` otherwise. Highlighting it again removes it;
// removing the landing promotes the first remaining highlight.
export function toggleAttentionTarget(search: string, reference: string): URLSearchParams {
  const params = new URLSearchParams(search);
  let highlights = [...new Set(params.getAll("highlight"))], reveal = params.get("reveal") ?? "";
  if (reveal === reference || highlights.includes(reference)) {
    highlights = highlights.filter(item => item !== reference);
    if (reveal === reference) reveal = highlights.shift() ?? "";
  } else if (reveal) highlights.push(reference);
  else reveal = reference;
  params.delete("highlight"); params.delete("reveal");
  for (const item of highlights) params.append("highlight", item);
  if (reveal) params.set("reveal", reveal);
  return params;
}
