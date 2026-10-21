import React, { useRef, useState } from "react";
import { ModalSurface } from "@pythia-software/query-table-ui";
import type { LibrarySearchKind } from "./text-search-editor";
import { Icon } from "./icons";
import "./explore-search.css";

const targets: { kind: LibrarySearchKind; label: string }[] = [
  { kind: "text", label: "Messages" },
  { kind: "file", label: "Files" },
  { kind: "url", label: "URLs" },
];

// A host-owned content condition in the native builder. QueryFilterRow still
// owns WHERE, and QueryBuilder still owns grouping, sorting and presentation.
// Content search retains its own message scope and batched execution semantics.
export function ExploreSearch({ kind, active, summary, error, onClear, children }: {
  kind: LibrarySearchKind;
  active: boolean;
  summary: string;
  error?: string;
  onClear: () => void;
  children: (close: () => void, kind: LibrarySearchKind, onKind: (kind: LibrarySearchKind) => void) => React.ReactNode;
}) {
  const [open, setOpen] = useState(Boolean(error));
  const [draftKind, setDraftKind] = useState(kind);
  const invoker = useRef<HTMLElement | null>(null);
  const label = targets.find(target => target.kind === kind)?.label ?? "Messages";
  const edit = (event: React.MouseEvent<HTMLButtonElement>, next = kind) => {
    invoker.current = event.currentTarget;
    setDraftKind(next);
    setOpen(true);
  };
  return <>
    <div className="qt-qb-row explore-content-row">
      <div className="qt-qb-section-header"><span className="qt-qb-kw">search in</span></div>
      <div className="explore-content-controls">
        {active || error ? <>
          <button type="button" className="qt-btn explore-content-chip" onClick={event => edit(event)} aria-label={`Edit ${label.toLowerCase()} search`}>
            <Icon name="search" /><strong>{label}</strong><span>{error ? "Search needs attention" : summary}</span><Icon name="edit" />
          </button>
          <button type="button" className="qt-link-btn" aria-label="Remove content search" onClick={onClear}><Icon name="close" /></button>
        </> : <>
          {targets.map(target => <button key={target.kind} type="button" className="qt-btn" onClick={event => edit(event, target.kind)}><Icon name="plus" />{target.label}</button>)}
          <span className="explore-content-hint">Across the workspaces above</span>
        </>}
      </div>
    </div>
    {open ? <ModalSurface title="Search within work" onClose={() => setOpen(false)} returnFocus={invoker.current} className="qt-modal explore-search-modal" initialFocus="textarea, input:not([type=checkbox])">
      <div className="qt-overlay-body">{children(() => setOpen(false), draftKind, setDraftKind)}</div>
    </ModalSurface> : null}
  </>;
}
