import React, { useEffect, useRef, useState } from "react";
import { createQueryLinkService, httpQuerySnapshotStore, encodeQuery, type FieldSchema } from "@pythia-software/query-table-core";
import { SearchComposer, replaceSearchDraft, useSearchDraft } from "./search-composer";
import { freshSearch, restoreSearch, searchURL } from "./search-composer-state";
import { ModalClose } from "./modal";

export function SearchDialog({ schema }: { schema: FieldSchema }) {
  const libraryURL = useRef<string>();
  const state = useSearchDraft(schema);
  const [open, setOpen] = useState(false);
  useEffect(() => {
    const dialog = document.getElementById("searchDialog") as HTMLDialogElement;
    let resolution = new AbortController();
    const show = () => {
      if (dialog.open) return;
      if (location.pathname === "/library" && libraryURL.current !== location.search) {
        libraryURL.current = location.search;
        const params = new URLSearchParams(location.search);
        replaceSearchDraft(restoreSearch(params, schema));
        if (params.get("q_library")?.startsWith("@")) {
          resolution.abort(); resolution = new AbortController();
          const signal = resolution.signal;
          const service = createQueryLinkService(schema.name, httpQuerySnapshotStore("/api/query-snapshots"));
          void service.resolve(location.href, "q_library", signal).then(query => {
            if (!signal.aborted && query) { params.set("q_library", encodeQuery(query)); replaceSearchDraft(restoreSearch(params, schema)); }
          }).catch(error => { if (!signal.aborted) replaceSearchDraft({ ...freshSearch(schema), error: error instanceof Error ? error.message : String(error) }); });
        }
      }
      setOpen(true); window.pharosModal!.open(dialog, { onClose: () => { resolution.abort(); setOpen(false); } });
      requestAnimationFrame(() => dialog.querySelector<HTMLElement>('textarea, input:not([type="checkbox"])')?.focus());
    };
    window.addEventListener("pharos:open-search", show);
    return () => { resolution.abort(); window.removeEventListener("pharos:open-search", show); };
  }, [schema]);
  return <>
    <div className="search-dialog-heading"><h2 id="searchDialogTitle">Search past work</h2><ModalClose label="Close search" onClick={() => (document.getElementById("searchDialog") as HTMLDialogElement).close()} /></div>
    {open ? <SearchComposer key={state.text.root.id} schema={schema} state={state} onChange={replaceSearchDraft} onSubmit={next => { (document.getElementById("searchDialog") as HTMLDialogElement).close(); window.pharosNavigate?.(searchURL(next)); }} /> : null}
  </>;
}
