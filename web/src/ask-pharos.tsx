import React, { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import type { FieldSchema } from "@pythia-software/query-table-core";
import { HomeSearch } from "./search-composer";

declare global { interface Window { initPharosAsk?: (root: HTMLElement) => () => void; } }
type Selection = { provider: string; model: string; effort: string };
type Model = { id: string; label: string; efforts: string[]; default: boolean };
type Provider = { id: string; models: Model[]; defaultSelection?: Selection };
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

export function AskPharosSearch({ schema }: { schema: FieldSchema }) {
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
    return window.initPharosAsk(root);
  }, [markup]);
  return <>
    {!markup && homeHost && createPortal(<HomeSearch schema={schema} />, homeHost)}
    <div ref={mount} dangerouslySetInnerHTML={{ __html: markup }} />
    {searchHost && createPortal(<HomeSearch schema={schema} />, searchHost)}
  </>;
}

export function AskPharosSettings() {
  const [status, setStatus] = useState<Status | null>(null), [error, setError] = useState("");
  const [models, setModels] = useState<Model[]>([]), [selection, setSelection] = useState<Selection>({ provider: "", model: "", effort: "Default" });
  const [busy, setBusy] = useState(false), [modelGeneration, setModelGeneration] = useState(0);
  const consent = useRef<HTMLDialogElement>(null);
  const refresh = async () => {
    try { const result = await askAPI<Status>("status"); setStatus(result); if (result.settings.selection?.provider) setSelection(result.settings.selection); else setSelection(current => ({ ...current, provider: result.providers[0]?.id || "" })); }
    catch (error) { setError(String(error)); }
  };
  useEffect(() => { void refresh(); const update = () => { void refresh(); }; window.addEventListener("pharos:ask-config", update); return () => window.removeEventListener("pharos:ask-config", update); }, []);
  useEffect(() => {
    if (!status?.settings.enabled || !selection.provider) { setModels([]); return; }
    let canceled = false; setModels([]);
    askAPI<Provider>(`models?provider=${encodeURIComponent(selection.provider)}${modelGeneration ? "&refresh=1" : ""}`).then(result => {
      if (canceled) return; setModels(result.models);
      setSelection(current => {
        const existing = result.models.find(model => model.id === current.model);
        if (!existing && result.defaultSelection) return result.defaultSelection;
        const model = existing || result.models.find(model => model.default) || result.models[0];
        return { ...current, model: model?.id || "", effort: model?.efforts.includes(current.effort) ? current.effort : "Default" };
      });
    }).catch(error => { if (!canceled) setError(String(error)); });
    return () => { canceled = true; };
  }, [selection.provider, status?.settings.enabled, modelGeneration]);
  async function save(change: Partial<Access> & { consent?: boolean }) {
    setBusy(true); setError("");
    try { await askAPI("settings", change); consent.current?.close(); window.dispatchEvent(new Event("pharos:ask-config")); await refresh(); }
    catch (error) { setError(String(error)); }
    finally { setBusy(false); }
  }
  const model = models.find(model => model.id === selection.model);
  return <div className="ask-settings">
    <p>Ask questions in plain language and find useful Pharos pages using an installed, authenticated Codex, Claude, or Antigravity CLI.</p>
    {error && <p role="alert">{error}</p>}
    {!status ? <p role="status">Checking available CLIs…</p> : <>
      <label><input type="checkbox" checked={status.settings.enabled} disabled={busy || (!status.providers.length && !status.settings.enabled)} onChange={event => event.target.checked ? consent.current?.showModal() : void save({ enabled: false })} />Enable Ask Pharos on this Mac</label>
      {!status.providers.length && <p>Install and authenticate a supported CLI, then reopen this page.</p>}
      <label><input type="checkbox" checked={status.settings.showInvitation} disabled={busy} onChange={event => void save({ showInvitation: event.target.checked })} />Show the Ask Pharos invitation</label>
      {status.settings.enabled && <>
        <label>CLI <select aria-label="Default AI CLI" value={selection.provider} onChange={event => setSelection({ provider: event.target.value, model: "", effort: "Default" })}>{status.providers.map(provider => <option key={provider.id}>{provider.id}</option>)}</select></label>
        <label>Model <select aria-label="Default AI model" disabled={!models.length} value={selection.model} onChange={event => setSelection(current => ({ ...current, model: event.target.value, effort: "Default" }))}>{!models.length && <option>Loading models…</option>}{models.map(model => <option key={model.id} value={model.id}>{model.label}</option>)}</select></label>
        <label>Effort <select aria-label="Default AI effort" value={selection.effort} onChange={event => setSelection(current => ({ ...current, effort: event.target.value }))}>{(model?.efforts || ["Default"]).map(value => <option key={value}>{value}</option>)}</select></label>
        <button type="button" disabled={busy || !model} onClick={() => void save({ selection })}>Save as default</button>
      </>}
      <p>Conversations stay in this library and are indexed alongside past work. Page visits are kept separately for this Mac, are limited to the latest 1,000 entries, and are available only to Ask Pharos’s private navigation interface.</p>
      <p>Disabling Ask Pharos stops active questions and leaves ordinary search available.</p>
      <button type="button" onClick={() => { void refresh(); setModelGeneration(value => value + 1); }}>Refresh available CLIs</button>
    </>}
    <dialog ref={consent} className="ask-consent" aria-labelledby="ask-settings-consent-title">
      <h2 id="ask-settings-consent-title">Ask in your own words</h2>
      <p>Ask Pharos navigates the app using your installed, authenticated CLI. Each question uses your agent account’s quota. These conversations will themselves be indexed by Pharos.</p>
      <p>The agent can consult your Pharos page visits and previous Ask Pharos chats through a private interface. Page visits are never offered through the general MCP service.</p>
      <p>This is optional. No other use of Pharos involves AI. When disabled, Pharos never accesses your agentic CLIs.</p>
      <button type="button" disabled={busy} onClick={() => void save({ enabled: true, consent: true })}>Enable Ask Pharos</button>
      <button type="button" onClick={() => consent.current?.close()}>Not right now</button>
      <button type="button" disabled={busy} onClick={() => void save({ enabled: false, showInvitation: false })}>No, and don’t show this again</button>
    </dialog>
  </div>;
}
