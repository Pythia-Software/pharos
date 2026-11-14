import React, { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { FieldSchema } from "@pythia-software/query-table-core";
import { HomeSearch } from "./search-composer";
import { ViewDisclosure } from "./view-disclosure";
import { createAskChartPreviews } from "./ask-chart-preview";
import { createAskWorkPreviews } from "./ask-work-preview";
import { combineAskPreviews, type AskLinkPreviews } from "./ask-link-previews";
import type { QueryDataset } from "./query-links";

declare global { interface Window { initPharosAsk?: (root: HTMLElement, options?: { previews?: AskLinkPreviews }) => () => void; } }
type Selection = { provider: string; model: string; effort: string };
type Provider = { id: string };
type Access = { enabled: boolean; showInvitation: boolean; selection?: Selection };
type Status = { settings: Access; providers: Provider[] };
async function askAPI<T>(path: string, body?: unknown): Promise<T> {
  const response = await fetch(`/api/ask/${path}`, body === undefined ? undefined : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const result = await response.json(); if (!response.ok) throw new Error(result.error || "Ask Pharos request failed"); return result;
}

// Keep the existing search editor mounted in a portal. The conversation view
// owns only its own DOM, including the approved SVG contours and loading UI.
export function AskPharosHome() {
  const host = useRef<HTMLDivElement>(null);
  useEffect(() => { window.dispatchEvent(new CustomEvent("pharos:ask-home", { detail: host.current })); }, []);
  return <div id="homeAskSearch" ref={host} />;
}

export function AskPharosSearch({ schemas }: { schemas: Record<QueryDataset, FieldSchema> }) {
  const schema = schemas.library;
  const mount = useRef<HTMLDivElement>(null);
  const [markup, setMarkup] = useState("");
  const [homeHost, setHomeHost] = useState<HTMLElement | null>(() => document.getElementById("homeAskSearch"));
  useEffect(() => {
    const attach = (event: Event) => setHomeHost((event as CustomEvent<HTMLElement>).detail);
    setHomeHost(document.getElementById("homeAskSearch"));
    window.addEventListener("pharos:ask-home", attach);
    return () => window.removeEventListener("pharos:ask-home", attach);
  }, []);
  const [searchHost, setSearchHost] = useState<HTMLElement | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    fetch(new URL("assets/ask-pharos.html", location.origin).pathname, { signal: controller.signal }).then(response => { if (!response.ok) throw new Error("Composer unavailable"); return response.text(); }).then(setMarkup).catch(() => {});
    return () => controller.abort();
  }, []);
  useEffect(() => {
    if (!markup || !mount.current || !window.initPharosAsk) return;
    const root = mount.current.querySelector<HTMLElement>("#pharos-ask")!;
    setSearchHost(root.querySelector<HTMLElement>(".pharos-search-host"));
    return window.initPharosAsk(root, { previews: combineAskPreviews(createAskChartPreviews(schemas), createAskWorkPreviews()) });
  }, [markup, schemas]);
  return <>
    {!markup && homeHost && createPortal(<HomeSearch schema={schema} />, homeHost)}
    <div ref={mount} dangerouslySetInnerHTML={{ __html: markup }} />
    {searchHost && createPortal(<HomeSearch schema={schema} />, searchHost)}
  </>;
}

// App Preferences: one switch turns Ask Pharos on; its card expands only
// while it is on. The composer owns the CLI, model and effort choice.
export function AskPharosSettings() {
  const [status, setStatus] = useState<Status | null>(null), [error, setError] = useState("");
  const [busy, setBusy] = useState(false), [refreshing, setRefreshing] = useState(false), [refreshed, setRefreshed] = useState("");
  const consent = useRef<HTMLDialogElement>(null);
  const refresh = async () => {
    try { const result = await askAPI<Status>("status"); setStatus(result); return result; }
    catch (error) { setError(String(error)); return null; }
  };
  useEffect(() => { void refresh(); const update = () => { void refresh(); }; window.addEventListener("pharos:ask-config", update); return () => window.removeEventListener("pharos:ask-config", update); }, []);
  async function save(change: Partial<Access> & { consent?: boolean }) {
    setBusy(true); setError("");
    try { await askAPI("settings", change); consent.current?.close(); window.dispatchEvent(new Event("pharos:ask-config")); await refresh(); }
    catch (error) { setError(String(error)); }
    finally { setBusy(false); }
  }
  // Rediscover installed CLIs and drop each one's cached model list, so the
  // composer offers what is installed now.
  async function refreshCLIs() {
    setRefreshing(true); setError(""); setRefreshed("");
    try {
      const result = await refresh();
      if (!result) return;
      await Promise.all(result.providers.map(provider => askAPI<Provider>(`models?provider=${encodeURIComponent(provider.id)}&refresh=1`).catch(() => null)));
      window.dispatchEvent(new Event("pharos:ask-config"));
      setRefreshed(result.providers.length ? `Found ${askList(result.providers.map(provider => provider.id))}.` : "No supported CLI found.");
    } finally { setRefreshing(false); }
  }
  const enabled = Boolean(status?.settings.enabled), providers = status?.providers || [];
  const heading = <div>
    <h3 id="askPharosEnabledTitle">Ask Pharos</h3>
    <p id="askPharosEnabledDescription" className="muted">Ask questions in plain language and find useful pages with your installed, authenticated Codex, Claude, or Antigravity CLI.{status && !providers.length && !enabled ? " Install and authenticate a supported CLI, then reopen this page." : ""}</p>
  </div>;
  return <>
    <div className="layout-preference-card ask-preferences">
      {enabled ? <ViewDisclosure viewKey="askPharosSettings" className="ask-preferences-disclosure" summary={<>
        {heading}
        <span className="menu-bar-expand" aria-hidden="true"><span className="ask-expand-closed">Expand</span><span className="ask-expand-open">Collapse</span><svg className="app-icon" viewBox="0 0 24 24"><use href="#ph-icon-chevron-down" /></svg></span>
      </>}>
        <div className="menu-bar-preferences-body ask-preferences-body">
          <p className="muted">{providers.length ? `Available CLIs: ${askList(providers.map(provider => provider.id))}.` : "No supported CLI found."} Choose the CLI, model and effort for each question in the Ask Pharos composer.</p>
          <p className="muted">Turning Ask Pharos off stops active questions; ordinary search stays available. Conversations stay in this library and are indexed alongside past work. Page visits are kept separately for this Mac, are limited to the latest 1,000 entries, and are available only to Ask Pharos’s private navigation interface.</p>
          <p className="ask-preferences-actions"><button type="button" disabled={refreshing} onClick={() => void refreshCLIs()}>{refreshing ? "Refreshing…" : "Refresh available CLIs"}</button><span className="muted" role="status">{refreshed}</span></p>
        </div>
      </ViewDisclosure> : <div className="optional-behavior">{heading}</div>}
      <button type="button" className={`toggle ask-preferences-toggle${enabled ? " on" : ""}`} role="switch" aria-checked={enabled} aria-labelledby="askPharosEnabledTitle" aria-describedby="askPharosEnabledDescription" disabled={!status || busy || (!providers.length && !enabled)} onClick={() => enabled ? void save({ enabled: false }) : consent.current?.showModal()} />
    </div>
    <div className="optional-behavior">
      <div><h3 id="askPharosInvitationTitle">Show the Ask Pharos invitation</h3><p id="askPharosInvitationDescription" className="muted">While Ask Pharos is off, offer to turn it on beside search. On by default.</p></div>
      <button type="button" className={`toggle${status?.settings.showInvitation ? " on" : ""}`} role="switch" aria-checked={Boolean(status?.settings.showInvitation)} aria-labelledby="askPharosInvitationTitle" aria-describedby="askPharosInvitationDescription" disabled={!status || busy} onClick={() => void save({ showInvitation: !status?.settings.showInvitation })} />
    </div>
    {error && <p className="ask-preferences-error" role="alert">{error}</p>}
    <dialog ref={consent} className="ask-consent" aria-labelledby="ask-settings-consent-title">
      <h2 id="ask-settings-consent-title">Ask in your own words</h2>
      <p>Ask Pharos navigates the app using your installed, authenticated CLI. Each question uses your agent account’s quota. These conversations will themselves be indexed by Pharos.</p>
      <p>The agent can consult your Pharos page visits and previous Ask Pharos chats through a private interface. Page visits are never offered through the general MCP service.</p>
      <p>This is optional. No other use of Pharos involves AI. When disabled, Pharos never accesses your agentic CLIs.</p>
      <button type="button" disabled={busy} onClick={() => void save({ enabled: true, consent: true })}>Enable Ask Pharos</button>
      <button type="button" onClick={() => consent.current?.close()}>Not right now</button>
      <button type="button" disabled={busy} onClick={() => void save({ enabled: false, showInvitation: false })}>No, and don’t show this again</button>
    </dialog>
  </>;
}

function askList(items: string[]) {
  return items.length < 3 ? items.join(" and ") : `${items.slice(0, -1).join(", ")}, and ${items.at(-1)}`;
}
