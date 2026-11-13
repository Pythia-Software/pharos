import React from "react";
import { createRoot } from "react-dom/client";
import { flushSync } from "react-dom";
import { ChartLegendChip, LegendMenu } from "./chart-legend-chip";
import { legendMenuItems, type LegendAction } from "./chart-selection";
import { readerKeyState, readerLineOptionLabel, readerLineScopes, updateReaderKeys } from "./reader-key-model";
import { Icon, type IconName } from "./icons";

export type ReaderKey = { id: string; label: string; values: string[]; title?: string; className?: string;
  family?: string; count?: number; children?: ReaderKey[]; available?: boolean; note?: string; icon?: IconName };
export type ReaderKeys = { items: ReaderKey[]; all: string[]; shown: string[]; groupToggle?: boolean; groupLabel?: string; groupField?: "reader_filter" | "reader_highlight_hidden"; groupMixed?: boolean; onSelect?: (id: string, action: LegendAction) => void; onChange: (shown: string[], allKinds?: boolean, action?: LegendAction) => void };
function ReaderKeyFilter({ items, all, shown, groupToggle = false, groupLabel = "Highlights", groupField = "reader_highlight_hidden", groupMixed = false, onSelect, onChange }: ReaderKeys) {
  const visible = all.filter(value => shown.includes(value)).length;
  const chip = (item: ReaderKey): React.ReactNode => <span key={item.id} className={`reader-key ${item.className ?? ""}`} data-label-family={item.family} title={item.title} role={item.children ? "group" : undefined} aria-label={item.children ? item.label : undefined}>
    {item.available === false ? <span className="reader-key-unavailable">{item.label}{item.note ? ` · ${item.note}` : ""}</span> : <ChartLegendChip
      item={{ key: item.id, label: item.label, color: "" }} {...readerKeyState(all, shown, item.values)}
      groupSize={item.children ? item.values.length : undefined}
      swatch={item.icon ? <Icon name={item.icon} className="reader-key-icon" /> : item.className === "event-target-legend" ? <span className="reader-event-link-example" aria-hidden="true"><span /><span /></span> : item.className || item.family !== undefined ? <i className="reader-key-swatch" aria-hidden="true" /> : null}
      suffix={item.count !== undefined ? <span className="transcript-filter-count">{item.count.toLocaleString()}</span> : null}
      onAction={action => onSelect ? onSelect(item.id, action) : onChange(updateReaderKeys(all, shown, item.values, action), false, action)} />}
    {item.children ? <span className="reader-key-children">{item.children.map(chip)}</span> : null}
  </span>;
  const children = <span className="reader-key-children">{items.map(chip)}</span>;
  return groupToggle ? <div className="reader-key conversation-key-group" role="group" aria-label={groupLabel}><button type="button" className="usage-legend-item usage-legend-category conversation-key-toggle" data-view-field={groupField} aria-pressed={visible === 0 ? false : visible === all.length && !groupMixed ? true : "mixed"} disabled={!all.length} title={`${visible ? "Hide" : "Show"} ${groupLabel.toLowerCase()}`} onClick={() => onChange(visible ? [] : all, true)}>{groupLabel}</button>{children}</div> : children;
}
type KeyPreset = { id: string; label: string; note: string };
type KeyDefaults = { presets: KeyPreset[]; selected: string | null; onChoose: (id: string) => void };
function ReaderKeyDefaults({ presets, selected, onChoose }: KeyDefaults) {
  const current = presets.find(preset => preset.id === selected)?.label ?? "Custom";
  return <LegendMenu name="Key" label="Key" menuLabel="Key defaults" title="Choose key defaults" category
    heading="Key defaults" suffix={<><span className="reader-key-separator" aria-hidden="true">·</span><span className="reader-key-current">{current}</span></>}>
    {choose => <>{presets.map(preset => <button key={preset.id} type="button" role="menuitemradio" tabIndex={-1}
      aria-checked={preset.id === selected} onClick={() => choose(() => onChoose(preset.id))}>
      <span>{preset.label}</span><small>{preset.note}</small>
    </button>)}</>}
  </LegendMenu>;
}
// The classic reader and React Browse preview share the app's legend menus.
function readerRoot<Props,>(host: HTMLElement, synchronous: boolean, renderContent: (props: Props) => React.ReactNode) {
  const root = createRoot(host);
  let disposed = false, initial = true;
  return { render: (props: Props) => {
      if (disposed) return;
      const render = () => root.render(renderContent(props));
      // Commit classic reader keys before its linked scroll measurements.
      const flush = initial && synchronous; initial = false;
      if (flush) flushSync(render); else render();
    },
    dispose: () => { disposed = true; queueMicrotask(() => root.unmount()); } };
}
window.pharosReaderKeys = (host, synchronous = false) => readerRoot(host, synchronous, props => <ReaderKeyFilter {...props} />);
window.pharosReaderDefaults = (host, synchronous = false) => readerRoot(host, synchronous, props => <ReaderKeyDefaults {...props} />);
// Line tags are mounted only when opened, keeping long transcripts cheap.
type LineTag = { label: string; values: string[]; category?: { label: string; values: string[] }; all: string[]; shown: string[]; onChange: (shown: string[]) => void };
function ReaderLineTag({ label, values, category, all, shown, onChange }: LineTag) {
  const state = readerKeyState(all, shown, values), scopes = readerLineScopes(values, category);
  return <LegendMenu name={label} label={label} menuLabel={`Filter ${label}`} title={`Filter ${label}`} heading={label}
    active={state.active} dimmed={state.dimmed} mixed={state.mixed}>
    {choose => <>{scopes.map((scope, index) => {
      const current = readerKeyState(all, shown, scope.values);
      const options = legendMenuItems(current.visibleCount, current.hiddenCount, current.selectionActive).filter(option => option.action !== "clear");
      return <React.Fragment key={scope.label}>
        {index ? <hr className="usage-legend-menu-separator" role="separator" /> : null}
        <div role={scopes.length > 1 ? "group" : undefined} aria-label={scopes.length > 1 ? scope.label : undefined}>
          {scopes.length > 1 ? <div className="usage-legend-menu-heading reader-line-scope-heading">{scope.label}</div> : null}
          {options.map(option => <button key={option.action} type="button" role="menuitem" tabIndex={-1}
            onClick={() => choose(() => onChange(updateReaderKeys(all, shown, scope.values, option.action)))}>{scopes.length > 1 ? readerLineOptionLabel(option.action, index ? category?.label : undefined, current.visibleCount, current.hiddenCount) : option.label}</button>)}
        </div>
      </React.Fragment>;
    })}
      {state.selectionActive ? <><hr className="usage-legend-menu-separator" role="separator" /><button type="button" role="menuitem" tabIndex={-1}
        onClick={() => choose(() => onChange(updateReaderKeys(all, shown, values, "clear")))}>Show All</button></> : null}
    </>}
  </LegendMenu>;
}
window.pharosReaderLineTag = (host, props) => {
  const root = createRoot(host);
  const render = (next: LineTag) => flushSync(() => root.render(<ReaderLineTag {...next} />));
  render(props);
  host.querySelector<HTMLButtonElement>('button')?.click();
  return {render, dispose: () => queueMicrotask(() => root.unmount())};
};
window.dispatchEvent(new Event("pharos:reader-keys-ready"));
declare global {
  interface Window {
    pharosReaderLineTag: (host: HTMLElement, props: LineTag) => { render: (props: LineTag) => void; dispose: () => void };
    pharosReaderKeys: (host: HTMLElement, synchronous?: boolean) => { render: (props: ReaderKeys) => void; dispose: () => void };
    pharosReaderDefaults: (host: HTMLElement, synchronous?: boolean) => { render: (props: KeyDefaults) => void; dispose: () => void };
  }
}
