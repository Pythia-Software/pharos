import React from "react";
import { useViewEntry } from "./view-state";

export type ViewDisclosureProps = Omit<React.ComponentPropsWithoutRef<"details">, "open" | "defaultOpen" | "onToggle" | "onToggleCapture" | "summary"> & {
  /** Stable section or record identity, never a display label or array index. */
  viewKey: string;
  summary: React.ReactNode;
  summaryTarget?: string;
  /** Used only when this identity is absent from the URL. */
  defaultOpen?: boolean;
  /** Capture defaults that depend on a saved preference or viewport. */
  captureInitial?: boolean;
  /** Deliberate toggles only; URL restoration never saves preferences. */
  onOpenChange?: (open: boolean) => void;
};

/** Standard durable accordion: owns its native summary, state and attention target. */
export function ViewDisclosure({ viewKey, summary, summaryTarget, defaultOpen = false, onOpenChange, captureInitial = false, children, ...props }: ViewDisclosureProps) {
  if (!viewKey.trim()) throw new Error("ViewDisclosure requires a stable, nonempty viewKey.");
  const [open,setOpen]=useViewEntry("disclosures",viewKey,defaultOpen,true,{captureInitial});
  return <details {...props} open={open} data-pharos-target={`section:${viewKey}`} data-view-field="disclosures" onToggle={event=>{
    if(event.currentTarget.open!==open){setOpen(event.currentTarget.open);onOpenChange?.(event.currentTarget.open);}
  }}>
    <summary data-pharos-target={summaryTarget}>{summary}</summary>
    {children}
  </details>;
}
