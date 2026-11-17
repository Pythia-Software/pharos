import React, { useCallback, useEffect, useState } from "react";
import "./teams-settings.css";

type EnrollmentStatus = { state: string; available: boolean; library: string; fingerprint: string; problem: string; optedOut?: boolean };
type SourceAccount = { bindingId: string; provider: string; sourceName: string; accountLabel: string; host: string; root: string; identifiable: number; ambiguous: number; mode: string; exposed: boolean; unindexed?: boolean };
type SourceStatus = { sourceRevision: number; settingsRevision: number; proofRequested: boolean; sources: SourceAccount[] };

async function sourceRequest(path = "", body?: object): Promise<SourceStatus> {
  const response = await fetch(`/api/teams/source-accounts${path}`, body ? { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) } : undefined);
  const value = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(value.error || "Source accounts are unavailable.");
  return value as SourceStatus;
}

function SourceAccounts() {
  const [status, setStatus] = useState<SourceStatus | null>(null);
  const [checked, setChecked] = useState<Record<string, boolean>>({});
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const refresh = useCallback(async () => { try { setStatus(await sourceRequest()); setError(""); } catch { setError("Could not read source accounts."); } }, []);
  useEffect(() => { void refresh(); }, [refresh]);
  const act = async (path: string, body: object) => {
    setBusy(true); setError("");
    try { await sourceRequest(path, body); await refresh(); }
    catch (e) { await refresh(); setError(e instanceof Error ? e.message : "Could not update source accounts."); }
    finally { setBusy(false); }
  };
  return <section aria-label="Source accounts">
    <h3>Source accounts</h3>
    <p>Review each source before it can contribute. Only choose your work account when the source contains that account alone; copied history, imports, or account switches can mix accounts. Pharos does not verify the account with the provider.</p>
    <p>Source paths and labels stay on this Mac. If the account changes or the history may be mixed, mark the source unavailable and reconcile it before using it again.</p>
    {!status?.proofRequested ? <button type="button" disabled={busy || !status} onClick={() => status && void act("/index", { settingsRevision: status.settingsRevision })}>Find sources on the next sync</button> : <p>Source discovery is requested. New observations appear after the next library sync.</p>}
    {status?.proofRequested && !status.sources.length ? <p>No identifiable sources have been indexed yet.</p> : null}
    {status?.sources.map(source => <div className="teams-source" key={source.bindingId || `${source.host}:${source.provider}:${source.sourceName}`}>
      <h4>{source.provider} · {source.sourceName}</h4>
      <p>{source.host} · {source.accountLabel} · <span>{source.root}</span></p>
      {source.unindexed ? <p>Unavailable: source identity has not been established. Its coverage is not included.</p> : <>
        <p>{source.identifiable} identifiable, {source.ambiguous} needing review. Status: {source.mode === "work" ? "Work account" : source.mode === "unavailable" ? "Unavailable" : "Not assigned"}.</p>
        {source.exposed ? <p>Changing this account requires reconciliation because it may already have been registered.</p> : <label><input type="checkbox" checked={!!checked[source.bindingId]} onChange={e => setChecked({ ...checked, [source.bindingId]: e.target.checked })} /> This source contains only my work {source.provider} account.</label>}
        <div><button type="button" disabled={busy || source.exposed || !checked[source.bindingId] || source.mode === "work"} onClick={() => void act("/review", { sourceBindingId: source.bindingId, expectedRevision: status.sourceRevision, mode: "work", attested: true })}>Use my work account</button>{" "}
        <button type="button" disabled={busy || source.mode === "unavailable"} onClick={() => void act("/review", { sourceBindingId: source.bindingId, expectedRevision: status.sourceRevision, mode: "unavailable", attested: false })}>Mark unavailable</button></div>
      </>}
    </div>)}
    {error ? <p role="alert">{error}</p> : null}
    <button type="button" disabled={busy} onClick={() => void refresh()}>Refresh source accounts</button>
  </section>;
}

async function enrollmentRequest(method = "GET", cancel = false): Promise<EnrollmentStatus> {
  const response = await fetch(`/api/teams/enrollment${cancel ? "/cancel" : ""}`, { method, headers: { "Content-Type": "application/json" } });
  const value = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(value.error || "Teams is unavailable.");
  return value as EnrollmentStatus;
}

export function TeamsSettings() {
  const [status, setStatus] = useState<EnrollmentStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const refresh = useCallback(async () => { try { setStatus(await enrollmentRequest()); setError(""); } catch { setError("Could not read the Teams connection."); } }, []);
  useEffect(() => { void refresh(); }, [refresh]);
  useEffect(() => {
    if (status?.state !== "waiting" && status?.state !== "starting") return;
    const timer = window.setInterval(() => { void refresh(); }, 2000);
    return () => window.clearInterval(timer);
  }, [status?.state, refresh]);
  const act = async (cancel = false) => {
    setBusy(true); setError("");
    try { if (cancel) { await enrollmentRequest("POST", true); await refresh(); } else setStatus(await enrollmentRequest("POST")); }
    catch (e) { setError(e instanceof Error ? e.message : "Teams is unavailable."); }
    finally { setBusy(false); }
  };
  const waiting = status?.state === "waiting" || status?.state === "starting";
  return <div className="layout-preference-card teams-settings">
    <h3>Connect this library</h3>
    <p>Enrolling {status?.library ? <strong>{status.library}</strong> : "a library"} makes its retained history part of your organization’s data.</p>
    <p>Your organization’s visible defaults can share attributed statistics. You can opt out. Conversation content and answers derived from it require your permission; private conversations are excluded from Teams text analysis and AI answers while eligible statistics can still include them.</p>
    <p>Pharos will open your browser to sign in. Compare the code shown here with the one requested there before approving the connection.</p>
    {waiting && status?.fingerprint ? <p role="status">Connection code: <strong><code>{status.fingerprint}</code></strong></p> : null}
    {status?.state === "connected_paused" ? <p role="status">Connected. Requests are paused until setup and privacy checks are ready.</p> : null}
    {status?.problem ? <p role="status">{status.problem}</p> : null}
    {error ? <p role="alert">{error}</p> : null}
    {waiting ? <button type="button" disabled={busy} onClick={() => void act(true)}>Cancel connection</button>
      : <button type="button" disabled={busy || !status?.available || status.state === "connected_paused" || status.state === "reconcile"} onClick={() => void act()}>Connect Teams</button>}
    {status?.state === "connected_paused" ? <SourceAccounts /> : null}
  </div>;
}
