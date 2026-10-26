import React, { useEffect, useState } from "react";
import { emptyLibraryState } from "./empty-library-model";
import { PageLink } from "./nested-pages";
import { Icon } from "./icons";
import "./library-content.css";

/** Search, Analyze and Optimize share the sync poll's library empty state. */
export function LibraryContent({ enabled = true, children }: { enabled?: boolean; children: React.ReactNode }) {
  const [status, setStatus] = useState(() => window.pharosSync?.status() ?? null);
  useEffect(() => {
    const update = () => setStatus(window.pharosSync?.status() ?? null);
    window.addEventListener("pharos:sync-status", update);
    update();
    return () => window.removeEventListener("pharos:sync-status", update);
  }, []);
  const empty = enabled ? emptyLibraryState(status) : null;
  return empty ? <div className="library-empty-card" role="status">
    {empty.message}
    {empty.action ? <> <PageLink className="library-empty-configure" href={empty.action.href}>{empty.action.label} <Icon name="arrow-right" /></PageLink></> : null}
  </div> : <>{children}</>;
}
