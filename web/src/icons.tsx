import React from "react";

// The symbol artwork lives in the page shell so both the shell and React views
// use the same 24 px, 1.6 px stroke icon set. See docs/iconography.md.
export type IconName =
  | "arrow-left" | "arrow-right" | "arrow-down" | "refresh" | "severity-high"
  | "severity-medium" | "severity-low" | "check" | "info" | "settings" | "chevron-down" | "copy"
  | "search" | "grip" | "edit" | "close" | "plus" | "compass" | "arrow-up" | "read" | "web" | "bookmark" | "chevron-left" | "chevron-right";

export function Icon({ name, className = "" }: { name: IconName; className?: string }) {
  return <svg className={`app-icon ${className}`.trim()} viewBox="0 0 24 24" aria-hidden="true" focusable="false">
    <use href={`#ph-icon-${name}`} />
  </svg>;
}
