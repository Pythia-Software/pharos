import React from "react";
import { createRoot } from "react-dom/client";
import { flushSync } from "react-dom";
import { ChartLegendChip, LegendMenu } from "./chart-legend-chip";
import { readerKeyState, updateReaderKeys } from "./reader-key-model";

export type ReaderKey = { id: string; label: string; values: string[]; title?: string; className?: string;
  family?: string; count?: number; children?: ReaderKey[]; available?: boolean; note?: string };
type ReaderKeys = { items: ReaderKey[]; all: string[]; shown: string[]; onChange: (shown: string[]) => void };
function ReaderKeyFilter({ items, all, shown, onChange }: ReaderKeys) {
  const chip = (item: ReaderKey): React.ReactNode => <span key={item.id} className={`reader-key ${item.className ?? ""}`} data-label-family={item.family} title={item.title} role={item.children ? "group" : undefined} aria-label={item.children ? item.label : undefined}>
    {item.available === false ? <span className="reader-key-unavailable">{item.label}{item.note ? ` · ${item.note}` : ""}</span> : <ChartLegendChip
      item={{ key: item.id, label: item.label, color: "" }} {...readerKeyState(all, shown, item.values)}
      groupSize={item.children ? item.values.length : undefined}
      swatch={item.className || item.family !== undefined ? <i className="reader-key-swatch" /> : null}
      suffix={item.count !== undefined ? <span className="transcript-filter-count">{item.count.toLocaleString()}</span> : null}
      onAction={action => onChange(updateReaderKeys(all, shown, item.values, action))} />}
    {item.children ? <span className="reader-key-children">{item.children.map(chip)}</span> : null}
  </span>;
  return <>{items.map(chip)}</>;
}
type KeyPreset = { id: string; label: string; note: string };
type KeyDefaults = { presets: KeyPreset[]; selected: string | null; onChoose: (id: string) => void };
function ReaderKeyDefaults({ presets, selected, onChoose }: KeyDefaults) {
  const current = presets.find(preset => preset.id === selected)?.label ?? "Custom";
  return <LegendMenu name="Key" label="Key" menuLabel="Key defaults" title="Choose key defaults" category
    heading="Key defaults" suffix={<span className="reader-key-current">{current}</span>}>
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
window.dispatchEvent(new Event("pharos:reader-keys-ready"));
declare global {
  interface Window {
    pharosReaderKeys: (host: HTMLElement, synchronous?: boolean) => { render: (props: ReaderKeys) => void; dispose: () => void };
    pharosReaderDefaults: (host: HTMLElement, synchronous?: boolean) => { render: (props: KeyDefaults) => void; dispose: () => void };
  }
}
