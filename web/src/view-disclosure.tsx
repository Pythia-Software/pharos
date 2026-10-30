import React from "react";
import { useViewEntry } from "./view-state";

// Stable semantic keys are required, including for disclosures in repeated
// records. Opening/closing content is view state; menu details use their own
// explicit transient classification instead. Capture an initial value only
// when it depends on a saved preference or viewport, not for fixed defaults.
export function ViewDisclosure({ viewKey, open: fallback = false, onToggle, captureInitial = false, ...props }: React.ComponentPropsWithoutRef<"details"> & { viewKey: string; captureInitial?: boolean }) {
  const [open,setOpen]=useViewEntry("disclosures",viewKey,Boolean(fallback),true,{captureInitial});
  return <details {...props} open={open} data-pharos-target={`section:${viewKey}`} data-view-field="disclosures" onToggle={event=>{
    if(event.currentTarget.open!==open)setOpen(event.currentTarget.open);
    onToggle?.(event);
  }} />;
}
