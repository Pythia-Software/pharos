import React, { useEffect, useRef, useState } from "react";
import "./machine-navigation.css";
export { machineFilterKey } from "./machine-activity-data";

// Track the population being explored, not paging, sorting, or presentation.
// Restoring a snapshot must not itself create another undo entry.
export function useDrilldownHistory<T>(value: T, context: string, identify: (snapshot: T) => string = JSON.stringify, record = true) {
  const signature = identify(value);
  const previous = useRef({ value, context, signature });
  const [history, setHistory] = useState<T[]>([]);
  useEffect(() => {
    const last = previous.current;
    previous.current = { value, context, signature };
    if (!record || last.context !== context) setHistory([]);
    else if (last.signature !== signature) setHistory(entries => [...entries, last.value].slice(-100));
  }, [context, signature, record]);
  return {
    canBack: record && previous.current.context === context && history.length > 0,
    back(restore: (snapshot: T) => void) {
      const snapshot = history.at(-1);
      if (snapshot === undefined) return;
      previous.current = { value: snapshot, context, signature: identify(snapshot) };
      setHistory(entries => entries.slice(0, -1));
      restore(snapshot);
    },
  };
}

export function ZoomNavigation({ canBack, zoomed, onBack, onReset, children }: { canBack: boolean; zoomed: boolean; onBack: () => void; onReset: () => void; children?: React.ReactNode }) {
  return <nav className="machine-zoom-navigation" aria-label="Exploration navigation">
    <div className="machine-zoom-actions">
      <button type="button" aria-label="Back one step" title="Undo the last drill-down or filter" disabled={!canBack} onClick={onBack}>← Back</button>
      <button type="button" disabled={!zoomed} onClick={onReset}>Reset zoom</button>
    </div>
    {children}
  </nav>;
}
