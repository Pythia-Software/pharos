// Structural layout checks that only a browser can make. Each returns a list
// of readable problems, empty when the page is sound.
//
// Containers are found by convention, not per page: any element with a class
// that ends in "card", "panel", "tile" or "cover" (query-card, sync-panel,
// tl1-tile, query-cover, plain .card and .panel), plus the transcript's turns
// and tool events and every dialog. A new card that follows the convention is
// checked without touching this file.

// Runs in the page. Returns one line per offender; repeats of the same
// offender (rows of a table, say) are counted on its first line.
function inspect({ wide, extraContainers, slack }) {
  const found = new Map();
  const problem = line => {
    const key = line.replace(/-?\d+/g, "#"), seen = found.get(key);
    found.set(key, seen ? { ...seen, count: seen.count + 1 } : { line, count: 1 });
  };
  const label = el => {
    const classes = typeof el.className === "string" ? el.className.trim().split(/\s+/).filter(Boolean).slice(0, 3) : [];
    const text = (el.textContent || "").trim().replace(/\s+/g, " ").slice(0, 40);
    return `${el.tagName.toLowerCase()}${el.id ? `#${el.id}` : ""}${classes.map(name => `.${name}`).join("")}${text ? ` "${text}"` : ""}`;
  };
  const px = Math.round;
  const span = r => `${px(r.left)}–${px(r.right)}`;
  // Not rendered: display none, inside a closed <details>, transparent or
  // hidden. None of these can overflow anything the user sees.
  const rendered = el => el.checkVisibility ? el.checkVisibility({ contentVisibilityAuto: true, opacityProperty: true, visibilityProperty: true }) : el.getClientRects().length > 0;
  // Visually hidden text (1px boxes, clip) takes no room.
  const hidden = (s, r) => r.width <= 1 || r.height <= 1 || (s.clip && s.clip !== "auto") || s.clipPath === "inset(50%)";
  const clips = s => s.overflowX !== "visible" || s.overflowY !== "visible";
  const positioned = s => s.position === "absolute" || s.position === "fixed";
  const containerPattern = /(^|-)(card|panel|tile|cover)$/;
  const isContainer = el => [...el.classList].some(name => containerPattern.test(name)) || el.matches(extraContainers);
  const textRect = node => { const range = document.createRange(); range.selectNodeContents(node); return range.getBoundingClientRect(); };
  const words = node => `text "${node.textContent.trim().replace(/\s+/g, " ").slice(0, 30)}"`;

  // Visits the rendered elements and text under `root`. `visit(el, style,
  // rect)` returns false to skip an element's subtree; `text(node, rect)`
  // sees each non-blank text node.
  const walk = (root, visit, text) => {
    for (const child of root.childNodes) {
      if (child.nodeType === Node.TEXT_NODE) {
        if (text && child.textContent.trim()) { const r = textRect(child); if (r.width > 1) text(child, r); }
        continue;
      }
      if (child.nodeType !== Node.ELEMENT_NODE || !rendered(child)) continue;
      const s = getComputedStyle(child);
      if (visit(child, s, child.getBoundingClientRect()) !== false) walk(child, visit, text);
    }
  };

  // (b) The document itself never scrolls; main does.
  const doc = document.documentElement, body = document.body, main = document.querySelector("main"), width = innerWidth, height = innerHeight;
  const docWidth = Math.max(doc.scrollWidth, body.scrollWidth), docHeight = Math.max(doc.scrollHeight, body.scrollHeight);
  if (docWidth > width + slack || docHeight > height + slack) {
    // Even hidden, that overflow lets scrollIntoView move the whole app.
    // Name what no ancestor clips. A box that clips holds its in-flow
    // descendants, but an absolutely positioned one only from its containing
    // block (its nearest positioned or transformed ancestor) up: inside a
    // scrolling main, a box positioned against the page escapes it.
    const unclipped = el => {
      let skipping = getComputedStyle(el).position === "absolute";
      for (let up = el.parentElement; up && up !== body; up = up.parentElement) {
        const s = getComputedStyle(up);
        if (skipping && s.position === "static" && s.transform === "none") continue;
        if (clips(s)) return false;
        skipping = s.position === "absolute";
      }
      return true;
    };
    const size = `the document is ${docWidth}×${docHeight} in a ${width}×${height} viewport, so it can scroll instead of main`;
    let named = false;
    for (const el of body.querySelectorAll("*")) {
      const r = el.getBoundingClientRect();
      if (r.right <= width + slack && r.bottom <= height + slack || !rendered(el) || getComputedStyle(el).position === "fixed" || !unclipped(el)) continue;
      const host = el instanceof SVGElement ? el.closest("svg").parentElement : el;
      problem(`${size}, reaching past it: ${host.tagName.toLowerCase()}${[...host.classList].slice(0, 2).map(c => `.${c}`).join("")}${host === el ? "" : " svg"}`);
      named = true;
    }
    if (!named) problem(size);
  }

  // Outside main (the header, drawers, toolbars) nothing reaches past the
  // viewport's sides, where the body would cut it off.
  walk(body, (el, s, r) => {
    if (el === main || s.position === "fixed") return false;
    if (!hidden(s, r) && (r.left < -slack || r.right > width + slack)) { problem(`content outside main is cut off by the ${width}px viewport: ${label(el)} spans ${span(r)}`); return false; }
    return !clips(s);
  }, (node, r) => { if (r.left < -slack || r.right > width + slack) problem(`content outside main is cut off by the ${width}px viewport: ${words(node)} spans ${span(r)}`); });

  // Fixed surfaces (dialogs, drawers, popovers, toolbars, rails) sit inside
  // the viewport.
  walk(body, (el, s, r) => {
    if (s.position !== "fixed" || hidden(s, r)) return true;
    if (r.left < -slack || r.top < -slack || r.right > width + slack || r.bottom > height + slack) problem(`a fixed surface leaves the ${width}×${height} viewport: ${label(el)} at [${px(r.left)}, ${px(r.top)}, ${px(r.right)}, ${px(r.bottom)}]`);
    return false;
  });

  // (b) Nothing in main reaches past its sides, except wide data tables that
  // main scrolls on purpose. Content inside a scroll or clip container is
  // that container's business, so the walk stops there.
  const box = main.getBoundingClientRect();
  const left = box.left + main.clientLeft, right = left + main.clientWidth;
  const leaves = `content leaves main (${px(left)}–${px(right)})`;
  let tableRight = right, outside = false;
  walk(main, (el, s, r) => {
    if (s.position === "fixed") return false;
    if (el.matches(wide)) { tableRight = Math.max(tableRight, r.right); return false; }
    if (!hidden(s, r) && (r.right > right + slack || r.left < left - slack)) { problem(`${leaves}: ${label(el)} spans ${span(r)}`); outside = true; return false; }
    return !clips(s);
  }, (node, r) => { if (r.right > right + slack || r.left < left - slack) { problem(`${leaves}: ${words(node)} spans ${span(r)}`); outside = true; } });
  if (!outside && main.scrollWidth > main.clientWidth + slack && main.scrollWidth - main.scrollLeft > tableRight - left + slack) problem(`main scrolls sideways by ${main.scrollWidth - main.clientWidth}px`);

  // (d) Cards, covers, panels and dialogs hold their content: no in-flow
  // element escapes their box and no text runs out of their sides.
  // Absolutely positioned pieces (gutter icons, corner buttons, popovers) are
  // placed on purpose and exempt.
  const containers = new Set([...main.querySelectorAll("*"), ...document.querySelectorAll('[role="dialog"], dialog')].filter(el => isContainer(el) && rendered(el)));
  for (const container of containers) {
    const cr = container.getBoundingClientRect();
    if (hidden(getComputedStyle(container), cr)) continue;
    // A scrollable dialog can hold more content than its viewport. Its
    // vertical descendants remain reachable; escaping the screen is checked
    // above, and horizontal clipping/control visibility stay checked below.
    const scrollsVertically = ['auto', 'scroll'].includes(getComputedStyle(container).overflowY);
    const beyond = r => ["left", "right", "top", "bottom"].filter((side, i) => [cr.left - r.left, r.right - cr.right, cr.top - r.top, r.bottom - cr.bottom][i] > slack && !(scrollsVertically && (side === 'top' || side === 'bottom')));
    const escapes = `content escapes ${label(container)}`;
    walk(container, (el, s, r) => {
      if (positioned(s) || el.matches(wide)) return false;
      const sides = hidden(s, r) ? [] : beyond(r);
      if (sides.length) { problem(`${escapes}: ${label(el)} past the ${sides.join("/")}`); return false; }
      // A nested container is checked on its own; clipped content is clipped.
      return !(isContainer(el) || clips(s));
    }, (node, r) => {
      // Glyphs may rise above a tight line box; text only escapes sideways.
      const sides = beyond(r).filter(side => side === "left" || side === "right");
      if (sides.length) problem(`${escapes}: ${words(node)} past the ${sides.join("/")}`);
    });
    // Chart labels stay inside their SVG, which would clip them.
    for (const svg of container.querySelectorAll("svg")) {
      const sr = svg.getBoundingClientRect();
      if (sr.width < 48 || sr.height < 24 || !rendered(svg)) continue;
      for (const text of svg.querySelectorAll("text")) {
        const r = text.getBoundingClientRect();
        if (rendered(text) && r.width > 0 && (r.left < sr.left - slack || r.right > sr.right + slack || r.top < sr.top - slack || r.bottom > sr.bottom + slack)) problem(`chart label "${text.textContent.trim().slice(0, 20)}" cut off by ${label(svg)} in ${label(container)}`);
      }
    }
  }

  // No control is mostly hidden by a box that cannot scroll (overflow hidden
  // or clip, like the header); the walks above stop at such boxes.
  // Positioned controls (column resize handles) straddle edges on purpose.
  for (const control of document.querySelectorAll("a[href], button, input:not([type=hidden]), select, textarea, [tabindex]:not([tabindex='-1'])")) {
    if (!rendered(control) || control.closest("[inert], [aria-hidden='true']")) continue;
    const s = getComputedStyle(control), r = control.getBoundingClientRect();
    if (hidden(s, r) || positioned(s)) continue;
    let clip = control.parentElement;
    while (clip && clip !== body && getComputedStyle(clip).overflowX === "visible") clip = clip.parentElement;
    if (!clip || clip === body || !["hidden", "clip"].includes(getComputedStyle(clip).overflowX)) continue;
    const c = clip.getBoundingClientRect(), shown = Math.min(r.right, c.right) - Math.max(r.left, c.left);
    if (shown < r.width * 0.75) problem(`a control is cut off where nothing scrolls: ${label(control)} in ${label(clip)} (${px(Math.max(0, shown))} of ${px(r.width)}px shown)`);
  }
  return [...found.values()].map(({ line, count }) => count > 1 ? `${line} (and ${count - 1} like it)` : line);
}

// `wide`: Query Table tables may be wider than main, which then scrolls them
// sideways under a fixed header by design. `slack`: subpixel rounding.
const options = { wide: ".qt-table", extraContainers: ".turn, .tool-event, .subagent-run, .chat-bubble, .qt-metric, [role=dialog], dialog", slack: 1 };

export function layoutProblems(page) {
  return page.evaluate(inspect, options);
}

// (c) After main scrolls to its far corner, the header is still at the top
// of the viewport and main still starts below it; the document itself never
// moves. Leaves main scrolled to the bottom.
export function scrollProblems(page) {
  return page.evaluate(async () => {
    const main = document.querySelector("main"), header = document.querySelector("body > header");
    main.scrollTo(main.scrollWidth, main.scrollHeight);
    await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    const problems = [], h = header.getBoundingClientRect(), m = main.getBoundingClientRect();
    if (Math.abs(h.top) > 1 || h.bottom > innerHeight || h.left < -1 || h.right > innerWidth + 1) problems.push(`header moved to [${Math.round(h.left)}, ${Math.round(h.top)}, ${Math.round(h.right)}, ${Math.round(h.bottom)}] after scrolling main`);
    if (m.top < h.bottom - 1) problems.push(`main starts at ${Math.round(m.top)}px, under the header's bottom at ${Math.round(h.bottom)}px`);
    const scroller = document.scrollingElement;
    if (scroller.scrollTop || scroller.scrollLeft) problems.push(`the document scrolled to ${scroller.scrollLeft},${scroller.scrollTop} instead of main`);
    // Stay at the bottom, back at the left edge, for the next checks.
    scroller.scrollTo(0, 0);
    main.scrollTo(0, main.scrollHeight);
    return problems;
  });
}
