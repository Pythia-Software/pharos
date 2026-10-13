import React, { useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Icon } from "./icons";

// Help remains available to pointer, keyboard, and touch users. Portaling keeps
// it out of clipped panels; positioning also works at narrow viewport widths.
export function SearchHelp({ label, children }: { label: string; children: React.ReactNode }) {
  const id = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  const bubble = useRef<HTMLDivElement>(null);
  const timer = useRef<ReturnType<typeof setTimeout>>();
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState({ left: 8, top: 8 });
  const show = () => { clearTimeout(timer.current); setOpen(true); };
  const hide = () => { timer.current = setTimeout(() => { if (document.activeElement !== trigger.current) setOpen(false); }, 120); };
  useEffect(() => () => clearTimeout(timer.current), []);
  useEffect(() => {
    if (!open) return;
    const place = () => {
      const rect = trigger.current!.getBoundingClientRect();
      const width = bubble.current?.offsetWidth ?? 300, height = bubble.current?.offsetHeight ?? 80;
      setPosition({ left: Math.max(8, Math.min(rect.left, window.innerWidth - width - 8)), top: rect.bottom + height + 16 < window.innerHeight ? rect.bottom + 6 : Math.max(8, rect.top - height - 6) });
    };
    const outside = (event: PointerEvent) => { if (!trigger.current?.contains(event.target as Node) && !bubble.current?.contains(event.target as Node)) setOpen(false); };
    const escape = (event: KeyboardEvent) => { if (event.key === "Escape") { event.preventDefault(); setOpen(false); } };
    place();
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    document.addEventListener("pointerdown", outside);
    document.addEventListener("keydown", escape);
    return () => { window.removeEventListener("resize", place); window.removeEventListener("scroll", place, true); document.removeEventListener("pointerdown", outside); document.removeEventListener("keydown", escape); };
  }, [open, children]);
  return <>
    <button ref={trigger} type="button" className="search-help" aria-label={`${label} help`} aria-describedby={open ? id : undefined} onMouseEnter={show} onMouseLeave={hide} onFocus={show} onBlur={() => setOpen(false)} onClick={show}><Icon name="info" /></button>
    {open ? createPortal(<div ref={bubble} id={id} role="tooltip" className="search-help-bubble" style={position} onMouseEnter={show} onMouseLeave={hide}>{children}</div>, trigger.current?.closest("dialog") ?? document.body) : null}
  </>;
}
