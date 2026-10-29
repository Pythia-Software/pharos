// Library health's storage breakdown: where the library's space goes, as one
// bar of its parts and a table by Mac, source or repository. The service
// measures and divides the space (storage_breakdown.go); this only draws it.
(() => {
  'use strict';
  const CSS = `
.storage-breakdown{--storage-originals:#ad823b;--storage-catalog:#4f5aac;--storage-other:var(--muted,#756c5f);margin-top:14px;padding:17px 20px}
:root[data-theme="dark"] .storage-breakdown{--storage-originals:#b08a3c;--storage-catalog:#7484e6}
@media(prefers-color-scheme:dark){:root:not([data-theme]) .storage-breakdown{--storage-originals:#b08a3c;--storage-catalog:#7484e6}}
.storage-head{display:flex;flex-wrap:wrap;align-items:baseline;justify-content:space-between;gap:4px 16px}
.storage-head h2{margin:0;font-size:var(--fs-xl,20px)}
.storage-bar{display:flex;gap:2px;height:14px;margin:14px 0 10px;border-radius:4px;overflow:hidden;background:var(--line,#ddd)}
.storage-bar span{min-width:2px;height:100%}
.storage-swatch{display:inline-block;flex:none;width:10px;height:10px;border-radius:3px;vertical-align:-1px}
.storage-legend{display:flex;flex-wrap:wrap;gap:6px 18px;margin:0;padding:0;list-style:none;font-size:var(--fs-md,13px)}
.storage-legend li{display:flex;align-items:center;gap:7px;white-space:nowrap}
.storage-legend b,.storage-table td.num{font-weight:600;font-variant-numeric:tabular-nums}
.storage-views{display:inline-flex;margin:18px 0 8px;border:1px solid var(--line,#ccc);border-radius:5px;overflow:hidden}
.storage-views button{border:0;border-radius:0;background:none;color:var(--muted,#666);padding:4px 10px}
.storage-views button+button{border-left:1px solid var(--line,#ccc)}
.storage-views button[aria-pressed="true"]{background:var(--accent,#315845);color:var(--panel,#fff)}
.storage-table{width:100%;border-collapse:collapse;table-layout:fixed;font-size:var(--fs-md,13px)}
.storage-table th{color:var(--muted,#666);font-weight:500;text-align:left}
.storage-table th,.storage-table td{padding:6px 8px 6px 0;border-top:1px solid var(--line,#ddd);vertical-align:middle}
.storage-table thead th{border-top:0}
.storage-table .num{width:13%;text-align:right}
.storage-table .storage-name{width:31%;overflow-wrap:anywhere}
.storage-row-bar{display:flex;gap:2px;height:8px}
.storage-row-bar span{height:100%;border-radius:2px}
.storage-note{margin-top:10px}
.storage-compact{display:flex;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:8px 16px;margin-top:14px;padding-top:12px;border-top:1px solid var(--line,#ddd);font-size:var(--fs-md,13px)}
.storage-compact p{flex:1 1 320px;margin:0}
.storage-compact-actions{display:flex;flex-wrap:wrap;gap:8px}
@media(max-width:500px){.storage-legend li{white-space:normal}.storage-table .storage-split{display:none}.storage-table .storage-name{width:45%}.storage-table .num{width:25%}.storage-breakdown{padding:14px}}
`;
  const SOURCES = {claude: 'Claude Code', codex: 'Codex', conductor: 'Conductor', tl1: 'TL1', antigravity: 'Antigravity', 'antigravity-cli': 'Antigravity CLI'};
  const VIEWS = [['machines', 'By Mac'], ['sources', 'By source'], ['repositories', 'By repository']];
  // Each part's color: its group's hue, lighter for the smaller parts.
  const FILLS = {
    originals_latest: 'var(--storage-originals)',
    originals_history: 'color-mix(in srgb,var(--storage-originals) 55%,var(--panel,#fff))',
    originals_other: 'color-mix(in srgb,var(--storage-originals) 30%,var(--panel,#fff))',
    catalog_database: 'var(--storage-catalog)',
    catalog_free: 'color-mix(in srgb,var(--storage-catalog) 18%,var(--panel,#fff))',
    catalog_log: 'color-mix(in srgb,var(--storage-catalog) 55%,var(--panel,#fff))',
    catalog_sidecar: 'color-mix(in srgb,var(--storage-catalog) 30%,var(--panel,#fff))',
    preserved: 'var(--storage-other)',
    other: 'color-mix(in srgb,var(--storage-other) 45%,var(--panel,#fff))',
  };
  // Compacting is offered once the catalog's free space is worth rewriting
  // it, which takes about 45 minutes for 100 GB: a gigabyte, and a tenth of
  // the catalog.
  const COMPACT_MIN_BYTES = 2 ** 30, COMPACT_MIN_SHARE = 0.1;
  let view = 'machines', confirming = false;

  function percent(part, total) {
    if (!total) return '0%';
    const value = part / total * 100;
    return value > 0 && value < 1 ? '<1%' : `${Math.round(value)}%`;
  }

  // What the panel shows for one breakdown and view, without the DOM.
  function model(data, selected = 'machines') {
    const total = Number(data?.total_bytes) || 0;
    const parts = (data?.parts || []).filter(part => Number(part.bytes) > 0)
      .map(part => ({...part, bytes: Number(part.bytes), fill: FILLS[part.key] || FILLS.other, share: percent(Number(part.bytes), total)}));
    const rows = (data?.[selected] || []).map(row => {
      const originals = Number(row.originals_bytes) || 0, catalog = Number(row.catalog_bytes) || 0;
      const label = selected === 'sources' ? SOURCES[row.id] || row.label : row.label;
      return {id: row.id, label, originals, catalog, total: originals + catalog};
    });
    const largest = Math.max(0, ...rows.map(row => row.total));
    for (const row of rows) {
      row.originalsWidth = largest ? row.originals / largest * 100 : 0;
      row.catalogWidth = largest ? row.catalog / largest * 100 : 0;
    }
    const catalogFree = Number(data?.catalog_free_bytes) || 0;
    const catalog = parts.filter(part => part.key === 'catalog_database' || part.key === 'catalog_free').reduce((sum, part) => sum + part.bytes, 0);
    const compactable = catalogFree >= COMPACT_MIN_BYTES && catalogFree >= catalog * COMPACT_MIN_SHARE;
    return {total, parts, rows, free: data?.volume_free_bytes, volume: data?.volume_name || 'the library drive',
      catalogFree, compactable, compactNeeds: Number(data?.compact_needs_bytes) || 0};
  }

  function node(tag, className, text) {
    const element = document.createElement(tag);
    if (className) element.className = className;
    if (text != null) element.textContent = text;
    return element;
  }

  function swatch(fill) {
    const element = node('span', 'storage-swatch');
    element.style.background = fill;
    return element;
  }

  function table(shown, bytes) {
    const element = node('table', 'storage-table'), head = node('tr');
    const name = view === 'machines' ? 'Mac' : view === 'sources' ? 'Source' : 'Repository';
    for (const [label, className] of [[name, 'storage-name'], ['', 'storage-split-bar'], ['Originals', 'num storage-split'], ['Catalog', 'num storage-split'], ['Total', 'num']]) {
      const cell = node('th', className, label);
      cell.scope = 'col';
      head.append(cell);
    }
    element.appendChild(node('thead')).append(head);
    const body = element.appendChild(node('tbody'));
    for (const row of shown.rows) {
      const line = node('tr'), bar = node('div', 'storage-row-bar');
      bar.setAttribute('aria-hidden', 'true');
      for (const [width, fill] of [[row.originalsWidth, 'var(--storage-originals)'], [row.catalogWidth, 'var(--storage-catalog)']]) {
        if (!width) continue;
        const segment = node('span');
        segment.style.width = `${width}%`;
        segment.style.background = fill;
        bar.append(segment);
      }
      const label = node('th', 'storage-name', row.label);
      label.scope = 'row';
      label.title = `${row.label}: ${bytes(row.originals)} originals, ${bytes(row.catalog)} catalog`;
      const barCell = node('td');
      barCell.append(bar);
      line.append(label, barCell, node('td', 'num storage-split', bytes(row.originals)), node('td', 'num storage-split', bytes(row.catalog)), node('td', 'num', bytes(row.total)));
      body.append(line);
    }
    return element;
  }

  // The offer to compact the catalog, and its confirmation. Compacting closes
  // the library, so it needs the app (window.pharosManagedLibrary) and goes to
  // the startup window, which shows its progress and reopens the library.
  function compact(root, data, shown, bytes) {
    const panel = node('div', 'storage-compact'), actions = node('div', 'storage-compact-actions');
    const library = window.pharosManagedLibrary;
    if (!confirming) {
      panel.append(node('p', '', `${bytes(shown.catalogFree)} of the catalog is free space left by deleted data. Pharos reuses it as the library grows; compacting gives it back to ${shown.volume} now.`));
      const start = node('button', '', 'Compact catalog…');
      start.type = 'button';
      start.onclick = () => { confirming = true; render(root, data); root.querySelector('.storage-compact button.primary')?.focus(); };
      actions.append(start);
    } else {
      panel.append(node('p', '', `Pharos closes the library while it compacts and reopens it when it’s done. A 100 GB catalog takes about 45 minutes, and it needs ${bytes(shown.compactNeeds)} free on ${shown.volume}. Meanwhile agents’ Pharos MCP calls fail, and a Terminal command using the library must finish first. Your next backup copies the whole catalog again.`));
      const go = node('button', 'primary', 'Compact now'), cancel = node('button', '', 'Cancel'), status = node('p', 'meta');
      go.type = cancel.type = 'button';
      status.setAttribute('role', 'status');
      cancel.onclick = () => { confirming = false; render(root, data); root.querySelector('.storage-compact button')?.focus(); };
      go.onclick = async () => {
        go.disabled = cancel.disabled = true;
        status.textContent = 'Starting…';
        const start = async () => {
          const response = await fetch('/api/app/compact', {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({id: library.id})});
          if (!response.ok) throw Error((await response.json().catch(() => ({}))).error || 'Could not start compacting. Try again.');
        };
        try {
          await changePharosLibrary(start, {flush: () => window.pharosSession?.flush(), navigate: path => location.assign(path), destination: '/app?after=compact'});
        } catch (error) {
          status.textContent = error.message;
          status.classList.add('error');
          go.disabled = cancel.disabled = false;
        }
      };
      actions.append(go, cancel);
      panel.append(status);
    }
    panel.insertBefore(actions, panel.children[1] || null);
    return library && typeof changePharosLibrary === 'function' ? panel : null;
  }

  function render(root, data) {
    const bytes = window.formatBytes, shown = model(data, view);
    root.replaceChildren();
    root.removeAttribute('aria-busy');
    const head = node('div', 'storage-head');
    head.append(node('h2', '', 'Storage'), node('div', 'meta',
      `${bytes(shown.total)} on ${shown.volume}${shown.free == null ? '' : ` · ${bytes(shown.free)} free`}`));
    root.append(head);
    const bar = node('div', 'storage-bar'), legend = node('ul', 'storage-legend');
    bar.setAttribute('role', 'img');
    bar.setAttribute('aria-label', shown.parts.map(part => `${part.label} ${bytes(part.bytes)}`).join(', '));
    for (const part of shown.parts) {
      const segment = node('span');
      segment.style.flex = `${part.bytes} 1 0`;
      segment.style.background = part.fill;
      segment.title = `${part.label}: ${bytes(part.bytes)} (${part.share})`;
      bar.append(segment);
      const item = node('li');
      item.append(swatch(part.fill), node('span', '', part.label), node('b', '', bytes(part.bytes)), node('span', 'meta', part.share));
      legend.append(item);
    }
    root.append(bar, legend);
    const offer = shown.compactable ? compact(root, data, shown, bytes) : null;
    if (offer) root.append(offer);
    const views = node('div', 'storage-views');
    views.setAttribute('role', 'group');
    views.setAttribute('aria-label', 'Break storage down');
    for (const [key, label] of VIEWS) {
      const button = node('button', '', label);
      button.type = 'button';
      button.setAttribute('aria-pressed', String(key === view));
      button.onclick = () => {
        view = key;
        render(root, data);
        root.querySelector(`.storage-views button[aria-pressed="true"]`)?.focus();
      };
      views.append(button);
    }
    root.append(views, table(shown, bytes));
    const measured = Date.parse(data.measured_at);
    root.append(node('div', 'meta storage-note', `${data.estimated || ''}${Number.isFinite(measured) ? ` Measured ${new Date(measured).toLocaleTimeString([], {hour: 'numeric', minute: '2-digit'})}.` : ''}`.trim()));
  }

  // load fills root from request() and, while the drive is first being
  // measured, asks again until current() says the page has moved on.
  async function load(root, request, current = () => true) {
    root.className = 'panel storage-breakdown';
    root.setAttribute('aria-busy', 'true');
    root.replaceChildren(node('div', 'meta', 'Storage'), node('div', 'big', '…'));
    for (;;) {
      let data;
      try {
        data = await request();
      } catch {
        if (current()) {
          root.removeAttribute('aria-busy');
          root.replaceChildren(node('div', 'meta', 'Storage'), node('div', 'big', 'Unavailable'));
        }
        return;
      }
      if (!current()) return;
      if (data.status !== 'measuring') {
        render(root, data);
        return;
      }
      root.replaceChildren(node('div', 'meta', 'Storage'), node('div', 'big', 'Measuring…'), node('div', 'meta health-detail', 'Sizing the catalog and each Mac’s captures'));
      await new Promise(resolve => setTimeout(resolve, 2000));
      if (!current()) return;
    }
  }

  const sheet = node('style');
  sheet.textContent = CSS;
  document.head.append(sheet);
  window.pharosStorageBreakdown = {load, model};
})();
