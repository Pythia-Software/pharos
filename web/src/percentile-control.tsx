import React, { useEffect, useId, useRef, useState } from "react";
import { Icon } from "./icons";
import { percentileLabel, percentileLevels, type PercentileLevel } from "./dashboard-analysis-model";

// The trend's %ile control: which reference levels to draw, any number of
// them. A heatmap traces each across its periods; a line draws each over
// the earlier points as a level. Each level is a checkbox in a small menu;
// one a chart cannot draw is disabled with its reason. Only the menu's
// open state is local: the levels are the chart's (percentiles).
export function PercentileControl({ value, onChange, limit, title }: {
  value: PercentileLevel[]; onChange: (next: PercentileLevel[]) => void; limit: (level: PercentileLevel) => string | undefined; title?: string;
}) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null), menuId = useId();
  useEffect(() => {
    if (!open) return;
    const outside = (event: Event) => { if (!root.current?.contains(event.target as Node)) setOpen(false); };
    const escape = (event: KeyboardEvent) => { if (event.key === "Escape") { setOpen(false); root.current?.querySelector("button")?.focus(); } };
    document.addEventListener("pointerdown", outside);
    document.addEventListener("focusin", outside);
    document.addEventListener("keydown", escape);
    return () => { document.removeEventListener("pointerdown", outside); document.removeEventListener("focusin", outside); document.removeEventListener("keydown", escape); };
  }, [open]);
  const shown = value.length ? value.map(level => level === "mean" ? "Mean" : level).join(", ") : "Off";
  const toggle = (level: PercentileLevel) => onChange(percentileLevels.filter(item => item === level ? !value.includes(level) : value.includes(item)));
  return <div className="usage-chart-field usage-chart-picker percentile-control" ref={root}>
    <span className="usage-preset-label">%ile</span>
    <button type="button" className="percentile-control-trigger" data-pharos-target="control:chart:percentiles" aria-label="%ile" aria-haspopup="true" aria-expanded={open} aria-controls={open ? menuId : undefined} title={title}
      onClick={() => setOpen(current => !current)}>
      <span>{shown}</span><Icon name="chevron-down" className="dashboard-select-caret" />
    </button>
    {open ? <div className="percentile-control-menu" id={menuId} role="group" aria-label="Reference levels">
      {percentileLevels.map(level => {
        const reason = limit(level);
        return <label key={level} className="percentile-control-option" title={reason}>
          <input type="checkbox" checked={value.includes(level)} disabled={Boolean(reason)} onChange={() => toggle(level)} />
          <span>{percentileLabel(level)}</span>
        </label>;
      })}
      {value.length ? <button type="button" className="percentile-control-clear" onClick={() => onChange([])}>None</button> : null}
    </div> : null}
  </div>;
}
