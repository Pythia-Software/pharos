import React, { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { ChartPopover, ChartPopoverRow } from "./chart-popover";
import { useViewEntry } from "./view-state";
import { habitsChartURL, type HabitsChartSelection } from "./habits-chart-query";
import type { HabitsSummary, PunchcardActor } from "./habits-model";

export type HabitChartPoint = {
  key: string; heading: string; selection: HabitsChartSelection; detail?: string;
  rows: Array<{ label: string; value: string; color?: string; actors?: PunchcardActor[] }>;
};
type MarkProps = React.AriaAttributes & React.DOMAttributes<SVGElement> & { role?: string; tabIndex?: number; "data-habits-mark"?: string; "data-view-field"?: string };

export function HabitsChartInspection({ chart, data, points, fit, children, onSelect }: {
  chart: string; data: HabitsSummary; points: HabitChartPoint[]; fit?: boolean; onSelect?: (key: string) => void;
  children: (mark: (point: HabitChartPoint) => MarkProps) => React.ReactNode;
}) {
  const root = useRef<HTMLDivElement>(null), bubble = useRef<HTMLDivElement>(null);
  const [pin, setPin] = useViewEntry("chart_pins", chart, "", !fit);
  const [preview, setPreview] = useState("");
  const restoreFocus = useRef(false), focusLink = useRef(false);
  const point = !fit ? points.find(item => item.key === pin) ?? points.find(item => item.key === preview) : undefined;
  const pinned = Boolean(point && point.key === pin);
  const [position, setPosition] = useState({ left: 8, top: 8 });
  useLayoutEffect(() => {
    if (!point || !bubble.current) return;
    const place = () => {
      const mark = [...root.current?.querySelectorAll("[data-habits-mark]") ?? []].find(node => node.getAttribute("data-habits-mark") === point.key);
      if (!mark || !bubble.current) return;
      const at = mark.getBoundingClientRect(), box = bubble.current.getBoundingClientRect();
      setPosition({ left: Math.max(8, Math.min(at.right + 10, window.innerWidth - box.width - 8)),
        top: Math.max(8, Math.min(at.top, window.innerHeight - box.height - 8)) });
    };
    place();
    window.addEventListener("scroll", place, true);
    window.addEventListener("resize", place);
    if (focusLink.current) { focusLink.current = false; bubble.current.querySelector("a")?.focus(); }
    return () => { window.removeEventListener("scroll", place, true); window.removeEventListener("resize", place); };
  }, [point, pinned]);
  useEffect(() => {
    if (!point) return;
    const close = () => { setPreview(""); if (pin) setPin(""); };
    const outside = (event: PointerEvent) => {
      if (event.target instanceof Node && !root.current?.contains(event.target) && !bubble.current?.contains(event.target)) close();
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      close();
      if (pinned && bubble.current?.contains(document.activeElement)) {
        restoreFocus.current = true;
        const mark = [...root.current?.querySelectorAll<SVGElement>("[data-habits-mark]") ?? []].find(node => node.getAttribute("data-habits-mark") === point.key);
        mark?.focus();
        restoreFocus.current = false;
      }
    };
    document.addEventListener("pointerdown", outside);
    window.addEventListener("keydown", escape);
    return () => { document.removeEventListener("pointerdown", outside); window.removeEventListener("keydown", escape); };
  }, [point, pin, pinned, setPin]);
  const mark = (item: HabitChartPoint): MarkProps => {
    if (fit) return {};
    const show = () => { if (!pinned && !restoreFocus.current) setPreview(item.key); };
    const inspect = (keyboard = false) => { focusLink.current = keyboard; setPreview(""); setPin(item.key); onSelect?.(item.key); };
    return {
      "data-habits-mark": item.key, "data-view-field": "chart_pins", role: "button", tabIndex: 0,
      "aria-label": `${item.heading}: ${item.rows.map(row => `${row.label} ${row.value}`).join(", ")}`,
      "aria-pressed": pin === item.key,
      onMouseEnter: show, onMouseLeave: () => setPreview(""), onFocus: show, onBlur: () => setPreview(""),
      onClick: () => inspect(), onKeyDown: event => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); inspect(true); } },
    };
  };
  return <div ref={root} className={`habits-chart-inspection${fit ? " habits-chart-fit" : ""}`}>
    {children(mark)}
    {point && createPortal(<ChartPopover ref={bubble} heading={point.heading} pinned={pinned} className="habits-chart-popover" style={position}>
      <ul>{point.rows.map((row, index) => <li key={index}><ChartPopoverRow {...row}
        href={pinned && row.actors ? habitsChartURL(data, point.selection, row.actors, new URLSearchParams(location.search)) : undefined} /></li>)}</ul>
      {point.detail ? <div className="usage-chart-tip-more">{point.detail}</div> : null}
    </ChartPopover>, document.body)}
  </div>;
}
