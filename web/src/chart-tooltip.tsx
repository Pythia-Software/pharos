import React, { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import "./chart-tooltip.css";
import { overflowTooltip, textIsClipped } from "./overflow-tooltip";

type Tip = { text: string; colors: (string | null)[]; x: number; y: number };

const literalColor = "(?:#[a-f0-9]{3,8}|rgba?\\([\\d.,% ]+\\))";
const tooltipColor = new RegExp(`^(?:${literalColor}|var\\(\\s*--[a-z0-9_-]+\\s*(?:,\\s*${literalColor}\\s*)?\\))$`, "i");

// Preserve custom-property references so portalled swatches track saved color
// edits too. Accept only color literals or one property with a literal fallback.
export function chartTooltipColors(serialized: string | null): (string | null)[] {
  try {
    const raw = JSON.parse(serialized ?? "[]");
    return Array.isArray(raw) ? raw.map(value => typeof value === "string" && tooltipColor.test(value) ? value : null) : [];
  } catch { return []; }
}

// One hover boundary for both our marks and the native metric renderer. Keep the
// bubble outside the cover's clipped link, without intercepting navigation.
export function ChartTooltip({ children }: { children: React.ReactNode }) {
  const host = useRef<HTMLSpanElement>(null), bubble = useRef<HTMLDivElement>(null);
  const [tip, setTip] = useState<Tip | null>(null);
  const [position, setPosition] = useState({ left: 0, top: 0 });
  useLayoutEffect(() => {
    if (!tip || !bubble.current) return;
    const { width, height } = bubble.current.getBoundingClientRect();
    setPosition({
      left: Math.max(8, Math.min(tip.x + 12, window.innerWidth - width - 8)),
      top: Math.max(8, tip.y + height + 24 > window.innerHeight ? tip.y - height - 12 : tip.y + 16),
    });
  }, [tip]);
  useEffect(() => {
    if (!tip) return;
    const dismiss = () => setTip(null);
    const escape = (event: KeyboardEvent) => { if (event.key === "Escape") dismiss(); };
    window.addEventListener("scroll", dismiss, true);
    window.addEventListener("resize", dismiss);
    window.addEventListener("blur", dismiss);
    window.addEventListener("keydown", escape);
    return () => {
      window.removeEventListener("scroll", dismiss, true);
      window.removeEventListener("resize", dismiss);
      window.removeEventListener("blur", dismiss);
      window.removeEventListener("keydown", escape);
    };
  }, [Boolean(tip)]);
  const move = (event: React.PointerEvent) => {
    if (event.pointerType === "touch") return;
    const target = (event.target as Element).closest("[data-chart-tooltip-label], [data-chart-tooltip], .qt-metric-mark[aria-label], .qt-metric-bar-row");
    if (!target || !host.current?.contains(target)) { setTip(null); return; }
    let text = target.getAttribute("data-chart-tooltip") ?? target.getAttribute("aria-label") ?? [
      target.querySelector(".qt-metric-bar-label")?.getAttribute("data-chart-title") ?? target.querySelector(".qt-metric-bar-label")?.textContent,
      target.querySelector(".qt-metric-bar-value")?.textContent,
    ].filter(Boolean).join("\n");
    const label = target.querySelector<HTMLElement>("[data-chart-label]");
    if (label) text = overflowTooltip(target.getAttribute("data-chart-tooltip-label") ?? "", text, textIsClipped(label)) ?? "";
    const colors = chartTooltipColors(target.getAttribute("data-chart-colors"));
    setTip(text ? { text, colors, x: event.clientX, y: event.clientY } : null);
  };
  return <span className="chart-tooltip-host" ref={host} onPointerMove={move} onPointerLeave={() => setTip(null)} onPointerDown={() => setTip(null)} onKeyDownCapture={() => setTip(null)}>
    {children}
    {tip && createPortal(<div ref={bubble} role="tooltip" className="chart-tooltip" style={position}>{tip.text.split("\n").map((line, index) => <span className="chart-tooltip-row" key={index}>{tip.colors[index] ? <i className="chart-tooltip-swatch" style={{ backgroundColor: tip.colors[index]! }} /> : null}{index === 0 ? <strong>{line}</strong> : line}</span>)}</div>, host.current?.closest("dialog") ?? document.body)}
  </span>;
}
