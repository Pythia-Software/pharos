// Only reveal text that CSS actually hides, including inline text clipped by
// an ancestor and multiline line clamps. Scrollable containers remain usable.
export function textIsClipped(element: HTMLElement): boolean {
  if (!element.getClientRects().length) return false;
  const range = element.ownerDocument.createRange();
  range.selectNodeContents(element);
  const text = [...range.getClientRects()];
  for (let box: HTMLElement | null = element; box; box = box.parentElement) {
    const style = getComputedStyle(box), rect = box.getBoundingClientRect();
    const left = rect.left + box.clientLeft, top = rect.top + box.clientTop;
    const right = left + box.clientWidth, bottom = top + box.clientHeight;
    const clipsX = /^(hidden|clip)$/.test(style.overflowX), clipsY = /^(hidden|clip)$/.test(style.overflowY);
    // Scroll dimensions use local coordinates, so rotating or scaling a card
    // cannot turn its text's transformed bounds into a false overflow.
    if (box === element) {
      if (clipsX && box.scrollWidth > box.clientWidth || clipsY && box.scrollHeight > box.clientHeight) return true;
      continue;
    }
    if (text.some(part => clipsX && (part.left < left - 1 || part.right > right + 1)
      || clipsY && (part.top < top - 1 || part.bottom > bottom + 1))) return true;
  }
  return false;
}

export function overflowTooltip(label: string, detail: string | undefined, clipped: boolean): string | undefined {
  return [clipped ? label : "", ...(detail?.split("\n").filter(line => line !== label) ?? [])].filter(Boolean).join("\n") || undefined;
}

// Both React and classic labels opt in. Measure the active label only; resize
// observation keeps its title current when a panel or its container changes.
export function installOverflowTitles(doc: Document) {
  let active: HTMLElement | null = null;
  const refresh = () => {
    if (!active) return;
    const title = textIsClipped(active) ? active.textContent ?? "" : "";
    if (title) { if (active.title !== title) active.title = title; }
    else active.removeAttribute("title");
  };
  const resize = new ResizeObserver(refresh);
  const select = (event: Event) => {
    const next = event.target instanceof Element ? event.target.closest<HTMLElement>("[data-overflow-title]") : null;
    if (active !== next) {
      active?.removeAttribute("title");
      resize.disconnect();
      active = next;
      for (let box = active; box; box = box.parentElement) resize.observe(box);
    }
    refresh();
  };
  for (const name of ["pointerover", "pointermove", "focusin"]) doc.addEventListener(name, select, true);
  return () => {
    for (const name of ["pointerover", "pointermove", "focusin"]) doc.removeEventListener(name, select, true);
    active?.removeAttribute("title");
    resize.disconnect();
  };
}
