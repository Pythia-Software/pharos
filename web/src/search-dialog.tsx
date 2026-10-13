import React, { useEffect, useRef, useState } from "react";
import type { FieldSchema } from "@pythia-software/query-table-core";
import { SearchComposer, replaceSearchDraft, useSearchDraft } from "./search-composer";
import { freshSearch, restoreSearch, searchURL } from "./search-composer-state";
import { Icon } from "./icons";

export function SearchDialog({ schema }: { schema: FieldSchema }) {
  const libraryURL = useRef<string>();
  const state = useSearchDraft(schema);
  const [open, setOpen] = useState(false);
  useEffect(() => {
    const dialog = document.getElementById("searchDialog") as HTMLDialogElement;
    let invoker: HTMLElement | null = null;
    const show = (event: Event) => {
      if (dialog.open) return;
      invoker = document.activeElement as HTMLElement;
      const context = (event as CustomEvent<{workspaceID: string; title: string}>).detail;
      if (context?.workspaceID) { const scoped = freshSearch(schema); scoped.query.where = [{ field: "id", op: "=", value: context.workspaceID }]; replaceSearchDraft(scoped); }
      else if (location.pathname === "/library" && libraryURL.current !== location.search) { libraryURL.current = location.search; replaceSearchDraft(restoreSearch(new URLSearchParams(location.search), schema)); }
      setOpen(true); dialog.showModal();
      requestAnimationFrame(() => dialog.querySelector<HTMLInputElement>('input:not([type="checkbox"])')?.focus());
    };
    const close = () => { setOpen(false); invoker?.focus(); };
    const backdrop = (event: MouseEvent) => { if (event.target === dialog) { const r = dialog.getBoundingClientRect(); if (event.clientX < r.left || event.clientX > r.right || event.clientY < r.top || event.clientY > r.bottom) dialog.close(); } };
    window.addEventListener("pharos:open-search", show); dialog.addEventListener("close", close); dialog.addEventListener("click", backdrop);
    return () => { window.removeEventListener("pharos:open-search", show); dialog.removeEventListener("close", close); dialog.removeEventListener("click", backdrop); };
  }, [schema]);
  return <>
    <div className="search-dialog-heading"><h2 id="searchDialogTitle">Search library</h2><button type="button" aria-label="Close library search" onClick={() => (document.getElementById("searchDialog") as HTMLDialogElement).close()}><Icon name="close" /></button></div>
    {open ? <SearchComposer key={state.text.root.id} schema={schema} state={state} onChange={replaceSearchDraft} onSubmit={next => { (document.getElementById("searchDialog") as HTMLDialogElement).close(); window.pharosNavigate?.(searchURL(next)); }} /> : null}
  </>;
}
