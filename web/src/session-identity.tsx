import React from "react";
import { Icon } from "./icons";
import { useRibbonInspector } from "./label-ribbon-inspector";
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

/** What a session view leaves out or cannot show yet: an error icon whose
 * popover lists the notes. Hover previews them; a click pins them, so any
 * session links they carry can be followed. */
export function SessionNotice({ notes, name }: { notes: React.ReactNode[]; name: string }) {
  const inspector = useRibbonInspector({ interactive: true });
  const sentence = (note: React.ReactNode) => typeof note === "string" ? note.charAt(0).toUpperCase() + note.slice(1) : note;
  const card = <div className="lt-session-notice-card"><strong>Not fully shown in this lane</strong><ul>{notes.map((note, i) => <li key={i}>{sentence(note)}</li>)}</ul></div>;
  return <>
    <button type="button" className="lt-session-notice" aria-haspopup="dialog" aria-label={`${notes.length === 1 ? "Note" : `${notes.length} notes`} about what ${name} does not show`}
      onPointerEnter={event => inspector.show(event.currentTarget, card)} onPointerLeave={inspector.leave}
      onClick={event => { event.preventDefault(); event.stopPropagation(); inspector.pin(event.currentTarget, card, undefined, event.detail === 0); }}>
      <Icon name="error" />
    </button>
    {inspector.portal}
  </>;
}

/** The same title and navigation treatment in each session view. notes put a
 * SessionNotice after the metadata. */
export function SessionIdentity({ name, metadata, notes, title, depth, branch, folded, current, openSession, className = "" }: {
  name: string;
  metadata: React.ReactNode;
  notes?: React.ReactNode[];
  title?: string;
  depth: number;
  branch?: TimelineSessionBranch;
  folded?: boolean;
  current?: boolean;
  openSession?: () => void;
  className?: string;
}) {
  const interactive = !!openSession && !current;
  const open = (event: React.MouseEvent) => { event.preventDefault(); event.stopPropagation(); openSession!(); };
  const props = { className: `lt-session-identity ${className}${interactive ? " is-interactive" : ""}${notes?.length ? " has-notice" : ""}`, style: { paddingLeft: sessionBranchX(depth) + 16 }, title, "aria-current": current ? "true" as const : undefined };
  if (notes?.length) {
    // The notice is a button of its own, so the session's button sits beside
    // it and stretches over the cell rather than containing it.
    return <span {...props}>
      <SessionBranches depth={depth} branch={branch} folded={folded} />
      {interactive ? <button type="button" className="lt-session-open" aria-label={`Open session ${name}`} onClick={open}><strong data-overflow-title="">{name}</strong></button> : <strong data-overflow-title="">{name}</strong>}
      <small className="lt-session-metadata"><span data-overflow-title="">{metadata}</span><SessionNotice notes={notes} name={name} /></small>
    </span>;
  }
  const content = <><SessionBranches depth={depth} branch={branch} folded={folded} /><strong data-overflow-title="">{name}</strong><small data-overflow-title="">{metadata}</small></>;
  return interactive ? <button {...props} type="button" aria-label={`Open session ${name}`} onClick={open}>{content}</button> : <span {...props}>{content}</span>;
}
