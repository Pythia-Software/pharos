import React, { useEffect, useRef, useState } from "react";
import { createQueryLinkService, httpQuerySnapshotStore, encodeQuery, type FieldSchema } from "@pythia-software/query-table-core";
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
    let resolution = new AbortController();
    const show = (event: Event) => {
      if (dialog.open) return;
      invoker = document.activeElement as HTMLElement;
      const context = (event as CustomEvent<{workspaceID: string; title: string}>).detail;
      if (context?.workspaceID) { const scoped = freshSearch(schema); scoped.query.where = [{ field: "id", op: "=", value: context.workspaceID }]; replaceSearchDraft(scoped); }
      else if (location.pathname === "/library" && libraryURL.current !== location.search) {
        libraryURL.current = location.search;
        const params = new URLSearchParams(location.search);
        replaceSearchDraft(restoreSearch(params, schema));
        if (params.get("q_library")?.startsWith("@")) {
          resolution.abort(); resolution = new AbortController();
          const signal = resolution.signal;
          const service = createQueryLinkService(schema.name, httpQuerySnapshotStore("/api/query-snapshots"));
          void service.resolve(location.href, "q_library", signal).then(query => {
            if (!signal.aborted && query) { params.set("q_library", encodeQuery(query)); replaceSearchDraft(restoreSearch(params, schema)); }
          }).catch(error => { if (!signal.aborted) replaceSearchDraft({ ...freshSearch(schema), error: String(error) }); });
        }
      }
      setOpen(true); dialog.showModal();
      requestAnimationFrame(() => dialog.querySelector<HTMLElement>('textarea, input:not([type="checkbox"])')?.focus());
    };
    const close = () => { resolution.abort(); setOpen(false); invoker?.focus(); };
    const backdrop = (event: MouseEvent) => { if (event.target === dialog) { const r = dialog.getBoundingClientRect(); if (event.clientX < r.left || event.clientX > r.right || event.clientY < r.top || event.clientY > r.bottom) dialog.close(); } };
    window.addEventListener("pharos:open-search", show); dialog.addEventListener("close", close); dialog.addEventListener("click", backdrop);
    return () => { resolution.abort(); window.removeEventListener("pharos:open-search", show); dialog.removeEventListener("close", close); dialog.removeEventListener("click", backdrop); };
  }, [schema]);
  return <>
    <div className="search-dialog-heading"><h2 id="searchDialogTitle">Search past work</h2><button type="button" aria-label="Close search" onClick={() => (document.getElementById("searchDialog") as HTMLDialogElement).close()}><Icon name="close" /></button></div>
    {open ? <SearchComposer key={state.text.root.id} schema={schema} state={state} onChange={replaceSearchDraft} onSubmit={next => { (document.getElementById("searchDialog") as HTMLDialogElement).close(); window.pharosNavigate?.(searchURL(next)); }} /> : null}
  </>;
}
