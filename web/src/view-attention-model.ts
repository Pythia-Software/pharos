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
export function nativeDetailsReady(table: { visible: boolean; shared: boolean; restored: boolean; loading: boolean; rows?: string; schema?: string }): boolean {
  return table.visible && !table.shared && table.restored && !table.loading && Boolean(table.rows && table.rows !== "running") && Boolean(table.schema);
}

const attentionClasses = ["pharos-highlight", "pharos-reveal"];
const ordinaryClasses = (value: string) => value.split(/\s+/).filter(name => name && !attentionClasses.includes(name)).sort().join(" ");
// Find and the status UI must not schedule another attention pass.
const owned = (node: Node | null) => typeof (node as Element | null)?.closest === "function" && !!(node as Element).closest(".pharos-find,.pharos-attention-status");
export function attentionMutationMatters(record: MutationRecord, marked: ReadonlySet<Element>): boolean {
  if (owned(record.target) || owned(record.target.parentElement)) return false;
  if (record.type === "childList" && [...record.addedNodes, ...record.removedNodes].every(owned)) return false;
  if (record.type !== "attributes" || record.attributeName !== "class") return true;
  const element = record.target as Element;
  // Attention's own classes are not a change, unless a marked element lost them.
  return ordinaryClasses(record.oldValue ?? "") !== ordinaryClasses(element.getAttribute("class") ?? "") || marked.has(element) && !element.matches(".pharos-highlight,.pharos-reveal");
}
