import React from "react";

type UsageEstimate = { label: string; detail: string };
declare global {
  interface Window {
    pharosOpenEstimateDialog?: (trigger: HTMLElement, context?: { usage?: UsageEstimate | null }) => void;
  }
}

/** Open the shared explanation, optionally including the currently mounted conversation. */
export function EstimateInfoButton({ readerHost }: { readerHost?: HTMLElement | null }) {
  return <button type="button" className="estimate-info-button" aria-label="About estimates" title="About estimates" aria-haspopup="dialog" onClick={event => {
    const panel = readerHost?.querySelector<HTMLElement & {usageEstimate?: UsageEstimate | null}>(".conversation-panel:not(.conversation-loading)");
    window.pharosOpenEstimateDialog?.(event.currentTarget, { usage: panel?.usageEstimate });
  }}>i</button>;
}
