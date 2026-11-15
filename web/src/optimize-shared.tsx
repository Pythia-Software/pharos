import React, { useEffect, useRef, useState } from "react";
import type { SelectionApi } from "@pythia-software/query-table-react";
import { SelectionToolbar } from "@pythia-software/query-table-ui";
import { Dialog, ReviewDialog, type CartGroup, type ReviewArea } from "./findings";
import { optimizeCount, optimizeSelectionPlan, type OptimizeItem, type OptimizeNoun, type OptimizeSelectionGroup } from "./optimize-model";

// The parts Risky actions and Fixes share: a card that selects what it
// summarizes, a prompt basket per place, and a selection bar that turns a
// selection into prompts or closes it. Each area passes its own API and words.

async function post<T>(url: string, body: unknown): Promise<T> {
  const response = await fetch(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const value = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(value.error ?? `Request failed (${response.status})`);
  return value as T;
}

// One card: a checkbox that selects every id it summarizes, its body, and a
// count on the right.
export function OptimizeCard({ ids, selection, label, className = "", target, count, countLabel, children }: {
  ids: readonly string[]; selection: SelectionApi; label: string; className?: string; target?: string; count: number; countLabel: string; children: React.ReactNode;
}) {
  const box = useRef<HTMLInputElement>(null);
  const chosen = ids.filter(id => selection.isSelected(id)).length;
  const all = chosen === ids.length && ids.length > 0;
  useEffect(() => { if (box.current) box.current.indeterminate = chosen > 0 && !all; }, [chosen, all]);
  const toggle = () => {
    const next = new Set([...selection.selected].map(String));
    for (const id of ids) if (all) next.delete(id); else next.add(id);
    selection.replace(next);
  };
  return <article className={`optimize-card ${className}${chosen ? " selected" : ""}`} data-pharos-target={target}>
    <input ref={box} className="optimize-card-select" type="checkbox" checked={all} onChange={toggle} aria-label={label} />
    <div className="optimize-card-body">{children}</div>
    <span className="optimize-card-count" aria-label={countLabel}>{count.toLocaleString()}</span>
  </article>;
}

// counted entries say how many items they hold (a check's notices).
export type OptimizeCartEntry = { key: string; title: string; detail: string; ids: string[]; ticked: number; counted?: boolean };
export type OptimizeCartGroup = { target: string; label: string; entries: OptimizeCartEntry[]; group: CartGroup };

// Prompt baskets, one per place. An entry may stand for several items (a
// check's notices); ticking or removing it applies to all of them.
export function OptimizeCart({ title, groups, api, changed, noun, empty, note, onReview, className = "" }: {
  title: string; groups: OptimizeCartGroup[]; api: string; changed: string; noun: OptimizeNoun; empty: string; note: React.ReactNode; onReview: (group: CartGroup) => void; className?: string;
}) {
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const bulk = async (body: Record<string, unknown>, ids: string[]) => {
    setBusy(true); setError("");
    try { await post(`${api}/bulk`, { ...body, ids }); window.dispatchEvent(new Event(changed)); }
    catch (failure) { setError((failure as Error).message); }
    finally { setBusy(false); }
  };
  return <aside className={`findings-cart ${className}`} id="findingsCart" aria-labelledby="findingsCartTitle">
    <h2 id="findingsCartTitle">{title}</h2>
    {error ? <p className="findings-error" role="alert">{error}</p> : null}
    {!groups.length ? <p className="findings-cart-empty">{empty}</p> : null}
    {groups.map(place => {
      const ticked = place.entries.reduce((sum, entry) => sum + entry.ticked, 0);
      return <section key={place.target} className="findings-cart-group" aria-label={`Prompt for ${place.label}`}>
        <div className="findings-cart-head"><h3>{place.label}</h3><span>{optimizeCount(ticked, noun)} ticked</span></div>
        <ul>{place.entries.map(entry => <OptimizeCartItem key={entry.key} entry={entry} noun={noun} busy={busy}
          onTick={value => void bulk({ action: "cart_tick", ticked: value }, entry.ids)} onRemove={() => void bulk({ action: "cart_remove" }, entry.ids)} />)}</ul>
        <button type="button" className="findings-button primary" disabled={busy || !ticked} onClick={() => onReview(place.group)}>Review prompt ({optimizeCount(ticked, noun)})</button>
      </section>;
    })}
    <p className="findings-cart-note">{note}</p>
  </aside>;
}

function OptimizeCartItem({ entry, noun, busy, onTick, onRemove }: { entry: OptimizeCartEntry; noun: OptimizeNoun; busy: boolean; onTick: (ticked: boolean) => void; onRemove: () => void }) {
  const box = useRef<HTMLInputElement>(null);
  const all = entry.ticked === entry.ids.length;
  useEffect(() => { if (box.current) box.current.indeterminate = entry.ticked > 0 && !all; }, [entry.ticked, all]);
  return <li className={entry.ticked ? "" : "unticked"}>
    <label>
      <input ref={box} type="checkbox" checked={all} disabled={busy} onChange={event => onTick(event.target.checked)} />
      <span><span className="findings-cart-title">{entry.title}</span><small>{entry.counted ? (all ? optimizeCount(entry.ids.length, noun) : `${entry.ticked.toLocaleString()} of ${optimizeCount(entry.ids.length, noun)} ticked`) + (entry.detail ? ` · ${entry.detail}` : "") : entry.detail}</small></span>
    </label>
    <button type="button" className="findings-button quiet safety-cart-remove" disabled={busy} aria-label={`Remove “${entry.title}” from this prompt`} onClick={onRemove}>Remove</button>
  </li>;
}

// Actions on selected items. Each applies to every selected item or to none.
// A prompt asks an agent working in one place, so a selection across places
// becomes one prompt per place. The bar outlives the selection, so its
// outcome and dialogs stay on screen.
export function OptimizeSelectionBar<T extends OptimizeItem & { title: string }>({ selection, copy, items, api, area, changed, noun, place, promptLimit, className = "" }: {
  selection: SelectionApi; copy: (text: string) => Promise<void>; items: readonly T[] | null; api: string; area: ReviewArea; changed: string;
  noun: OptimizeNoun; place: OptimizeNoun; promptLimit: number; className?: string;
}) {
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<React.ReactNode>("");
  const [choosing, setChoosing] = useState(false);
  const [group, setGroup] = useState<CartGroup | null>(null);
  const ids = [...selection.selected].map(String);
  const plan = optimizeSelectionPlan(items ?? [], ids, noun);
  const { groups, blocked } = items ? plan : { ...plan, blocked: `Loading these ${noun.many}…` };
  const count = (n: number) => optimizeCount(n, noun);
  const places = (list: OptimizeSelectionGroup[]) => list.map(item => `${item.label} (${item.ids.length.toLocaleString()})`).join(", ");
  async function bulk(action: string, chosen: string[]) {
    setBusy(true); setMessage("");
    try {
      await post(`${api}/bulk`, { action, ids: chosen });
      window.dispatchEvent(new Event(changed));
      return true;
    } catch (failure) { setMessage((failure as Error).message); return false; }
    finally { setBusy(false); }
  }
  async function review(chosen: OptimizeSelectionGroup) {
    setChoosing(false);
    if (!await bulk("cart_add", chosen.ids)) return;
    const byID = new Map(plan.selected.map(item => [item.id, item]));
    setGroup({ target: chosen.target, label: chosen.label, handoff: "review", handoff_reason: "Investigate and recommend; make no changes.", ticked: chosen.ids.length,
      items: chosen.ids.map(id => ({ id, title: byID.get(id)?.title ?? id, ticked: true, change_label: "Investigate and recommend", suggested_target: chosen.target })) });
  }
  function copyPrompts() {
    if (groups.length === 1 && groups[0]!.ids.length <= promptLimit) void review(groups[0]!); else setChoosing(true);
  }
  async function addToPrompts() {
    const added = groups;
    if (!await bulk("cart_add", ids)) return;
    selection.clear();
    setMessage(`Added ${count(ids.length)} to ${added.length === 1 ? `the prompt for ${added[0]!.label}` : `${added.length} prompts, one per ${place.one}: ${places(added)}`}. Copy ${added.length === 1 ? "it" : "them"} from the prompts beside these ${noun.many}.`);
  }
  async function setState(action: "dismiss" | "restore") {
    const n = ids.length;
    if (!await bulk(action, ids)) return;
    selection.clear();
    setMessage(action === "dismiss" ? `Acknowledged and closed ${count(n)}. Bring them back at any time.` : `Brought back ${count(n)} to Open.`);
  }
  const promptLabel = groups.length > 1 ? `Copy ${groups.length} prompts…` : "Copy prompt…";
  const addLabel = groups.length > 1 ? `Add to ${groups.length} ${place.one} prompts` : groups.length === 1 ? `Add to ${groups[0]!.label} prompt` : "Add to prompt";
  return <div className={`safety-selection ${className}`}>
    <SelectionToolbar selection={selection} className="safety-selection-toolbar" actions={() => <>
      <button type="button" className="findings-button primary" disabled={busy || Boolean(blocked)} onClick={copyPrompts}>{promptLabel}</button>
      <button type="button" className="findings-button" disabled={busy || Boolean(blocked)} onClick={() => void addToPrompts()}>{addLabel}</button>
      {!plan.known || plan.selected.some(item => item.state !== "dismissed") ? <button type="button" className="findings-button" disabled={busy} onClick={() => void setState("dismiss")}>Acknowledge and close</button> : null}
      {plan.closed ? <button type="button" className="findings-button" disabled={busy} onClick={() => void setState("restore")}>Bring back</button> : null}
    </>} />
    {ids.length ? <p className="safety-selection-note">{blocked || (groups.length > 1
      ? `These ${noun.many} come from ${groups.length} ${place.many}. A prompt asks an agent working in one ${place.one}, so they become one prompt per ${place.one}: ${places(groups)}.`
      : `A prompt for an agent working in ${groups[0]?.label ?? `this ${place.one}`}. Copying hands these ${noun.many} off; adding keeps them in that ${place.one}'s prompt to copy later.`)}</p> : null}
    {message ? <p className="safety-notice" role="status">{message}</p> : null}
    {choosing ? <Dialog title={`Copy a prompt for one ${place.one}`} onClose={() => setChoosing(false)}>
      <p>A prompt asks an agent working in one {place.one}, so the {count(ids.length)} you selected become one prompt per {place.one}. Copy them one at a time; each copied prompt hands its {noun.many} off.</p>
      <ul className="safety-prompt-choices">{groups.map(item => <li key={item.target}>
        <span><strong>{item.label}</strong> · {count(item.ids.length)}</span>
        {item.ids.length > promptLimit ? <span className="findings-muted">A prompt holds up to {promptLimit} {noun.many}; narrow the selection for this {place.one}.</span>
          : <button type="button" className="findings-button primary" disabled={busy} onClick={() => void review(item)}>Review prompt</button>}
      </li>)}</ul>
    </Dialog> : null}
    {group ? <ReviewDialog key={group.target} area={area} group={group} copy={copy}
      onClose={() => { setGroup(null); setMessage(`Kept ${count(group.items.length)} in the prompt for ${group.label}; copy it from the prompts beside these ${noun.many}.`); }}
      onCopied={() => {
        const copied = new Set(group.items.map(item => item.id)), remaining = ids.filter(id => !copied.has(id));
        setGroup(null); selection.replace(remaining);
        setMessage(remaining.length ? `Copied the prompt for ${group.label}; those ${noun.many} moved to Handed off. ${count(remaining.length)} from other ${place.many} are still selected.` : `Copied the prompt for ${group.label}; those ${noun.many} moved to Handed off.`);
        if (remaining.length) setChoosing(true);
        window.dispatchEvent(new Event(changed));
      }} /> : null}
  </div>;
}
