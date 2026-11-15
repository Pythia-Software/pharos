// What Optimize's review areas (Risky actions, Fixes) share: items with a
// triage state and a place their prompt goes to.
export type OptimizeItem = { id: string; state: string; target: string; target_label: string };
export type OptimizeSelectionGroup = { target: string; label: string; ids: string[] };
export type OptimizeNoun = { one: string; many: string };

// How a selection maps onto prompts: one group per place, largest first, and
// why prompts are unavailable when they are.
export function optimizeSelectionPlan<T extends OptimizeItem>(items: readonly T[], ids: readonly string[], noun: OptimizeNoun = { one: "notice", many: "notices" }) {
  const byID = new Map(items.map(item => [item.id, item]));
  const selected = ids.flatMap(id => byID.get(id) ?? []);
  const known = selected.length === ids.length;
  const closed = selected.filter(item => item.state !== "open").length;
  const groups = new Map<string, OptimizeSelectionGroup>();
  for (const item of selected) {
    const group = groups.get(item.target) ?? { target: item.target, label: item.target_label, ids: [] };
    group.ids.push(item.id);
    groups.set(item.target, group);
  }
  const blocked = !known ? `Loading these ${noun.many}…`
    : closed ? `${closed.toLocaleString()} of the selected ${noun.many} ${closed === 1 ? "is" : "are"} not open. Bring ${closed === 1 ? "it" : "them"} back, or untick ${closed === 1 ? "it" : "them"}, to add the selection to a prompt.`
      : "";
  return { selected, known, closed, blocked, groups: [...groups.values()].sort((a, b) => b.ids.length - a.ids.length || a.label.localeCompare(b.label)) };
}

export const optimizeCount = (n: number, noun: OptimizeNoun) => `${n.toLocaleString()} ${n === 1 ? noun.one : noun.many}`;
