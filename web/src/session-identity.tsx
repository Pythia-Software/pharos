import React from "react";
import type { TimelineSessionBranch } from "./timeline-label-model";

export const sessionBranchX = (depth: number) => 12 + Math.min(depth, 8) * 12;

export function SessionBranches({ depth, branch, root = true, folded = false }: {
  depth: number;
  branch?: TimelineSessionBranch;
  root?: boolean;
  folded?: boolean;
}) {
  if (!branch) return null;
  const x = sessionBranchX(depth);
  return <svg className="lt-session-branches" width={x + 10} height="100%" aria-hidden="true">
    {branch.rails.map(level => <line key={level} x1={sessionBranchX(level)} x2={sessionBranchX(level)} y1="0" y2="100%" />)}
    {root && branch.parentDepth !== undefined ? <path d={branch.rails.includes(branch.parentDepth) ? `M${sessionBranchX(branch.parentDepth)} 19H${folded ? x - 8 : x + 6}` : `M${sessionBranchX(branch.parentDepth)} 0V19H${folded ? x - 8 : x + 6}`} /> : null}
    {branch.children ? <line x1={x} x2={x} y1={root ? 27 : 0} y2="100%" /> : null}
  </svg>;
}

/** The same title and navigation treatment in each session view. */
export function SessionIdentity({ name, metadata, title, depth, branch, folded, current, openSession, className = "" }: {
  name: string;
  metadata: React.ReactNode;
  title?: string;
  depth: number;
  branch?: TimelineSessionBranch;
  folded?: boolean;
  current?: boolean;
  openSession?: () => void;
  className?: string;
}) {
  const content = <><SessionBranches depth={depth} branch={branch} folded={folded} /><strong data-overflow-title="">{name}</strong><small data-overflow-title="">{metadata}</small></>;
  const props = { className: `lt-session-identity ${className}${openSession && !current ? " is-interactive" : ""}`, style: { paddingLeft: sessionBranchX(depth) + 16 }, title, "aria-current": current ? "true" as const : undefined };
  return openSession && !current ? <button {...props} type="button" aria-label={`Open session ${name}`} onClick={event => { event.preventDefault(); event.stopPropagation(); openSession(); }}>{content}</button> : <span {...props}>{content}</span>;
}
