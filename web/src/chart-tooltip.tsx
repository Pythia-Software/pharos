import React, { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import "./chart-tooltip.css";

type Tip = { text: string; x: number; y: number };

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
    const target = (event.target as Element).closest("[data-chart-tooltip], .qt-metric-mark[aria-label], .qt-metric-bar-row");
    if (!target || !host.current?.contains(target)) { setTip(null); return; }
    const text = target.getAttribute("data-chart-tooltip") ?? target.getAttribute("aria-label") ?? [
      target.querySelector(".qt-metric-bar-label")?.getAttribute("data-chart-title") ?? target.querySelector(".qt-metric-bar-label")?.textContent,
      target.querySelector(".qt-metric-bar-value")?.textContent,
    ].filter(Boolean).join("\n");
    setTip(text ? { text, x: event.clientX, y: event.clientY } : null);
  };
  return <span className="chart-tooltip-host" ref={host} onPointerMove={move} onPointerLeave={() => setTip(null)} onPointerDown={() => setTip(null)}>
    {children}
    {tip && createPortal(<div ref={bubble} role="tooltip" className="chart-tooltip" style={position}>{tip.text.split("\n").map((line, index) => index === 0 ? <strong key={index}>{line}</strong> : <span key={index}>{line}</span>)}</div>, host.current?.closest("dialog") ?? document.body)}
  </span>;
}
