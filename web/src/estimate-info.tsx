import React from "react";
import { Icon } from "./icons";

type UsageEstimate = { label: string; detail: string };
declare global {
  interface Window {
    pharosOpenEstimateDialog?: (trigger: HTMLElement, context?: { usage?: UsageEstimate | null }) => void;
  }
}

/** Open the shared explanation, optionally including the currently mounted conversation. */
export function EstimateInfoButton({ readerHost, marker = false, label = "About estimates" }: { readerHost?: HTMLElement | null; marker?: boolean; label?: string }) {
  return <button type="button" className={marker ? "estimate-marker-button" : "estimate-info-button"} aria-label={label} title={label} aria-haspopup="dialog" onClick={event => {
    const panel = readerHost?.querySelector<HTMLElement & {usageEstimate?: UsageEstimate | null}>(".conversation-panel:not(.conversation-loading)");
    window.pharosOpenEstimateDialog?.(event.currentTarget, { usage: panel?.usageEstimate });
  }}>{marker ? "*" : <Icon name="info" />}</button>;
}
