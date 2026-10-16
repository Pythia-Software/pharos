import { useEffect, useRef } from "react";
import type { QueryTableApi } from "@pythia-software/query-table-react";
import type { WhereTerm } from "@pythia-software/query-table-core";
import { machineFilterKey } from "./machine-navigation";

export function pushMachineLocation(url: URL) {
  if (url.href === location.href) return;
  const entry = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random()}`;
  history.pushState({ ...history.state, pharosMachineEntry: entry }, "", url);
  window.dispatchEvent(new Event("pharos:uri-changed"));
}

// The table library replaces the URL after every query update. Preserve the
// previous entry before adding a locally applied filter as a navigation step.
// URL restoration and changes to the page scope already own their history entry.
export function useMachineQueryNavigation<Row>(api: QueryTableApi<Row>, enabled: boolean, context: string, parameter: string, scopeWhere?: WhereTerm[]) {
  const where = (api.query.grouping?.source ?? api.query).where;
  const signature = JSON.stringify(where.map(machineFilterKey).sort());
  const querySignature = JSON.stringify(api.query);
  const previous = useRef({ signature, context, url: location.href, state: history.state });
  useEffect(() => {
    const last = previous.current;
    const entry = history.state?.pharosMachineEntry;
    if (enabled && api.canUndo && last.context === context && last.state?.pharosMachineEntry === entry && last.signature !== signature) {
      const next = new URL(api.links.local(api.query, location.href, parameter).url);
      history.replaceState(last.state, "", last.url);
      pushMachineLocation(next);
    }
    if (enabled && scopeWhere) {
      history.replaceState({ ...history.state, pharosMachineScopes: { ...history.state?.pharosMachineScopes, [parameter]: scopeWhere } }, "", location.href);
    }
    previous.current = { signature, context, url: location.href, state: history.state };
  }, [signature, querySignature, context, enabled, api.canUndo]);
}
