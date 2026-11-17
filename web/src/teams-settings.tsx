import React, { useCallback, useEffect, useState } from "react";
import "./teams-settings.css";

type EnrollmentStatus = { state: string; available: boolean; library: string; fingerprint: string; problem: string; optedOut?: boolean };
type SourceAccount = { bindingId: string; provider: string; sourceName: string; accountLabel: string; host: string; root: string; identifiable: number; ambiguous: number; mode: string; exposed: boolean; unindexed?: boolean };
type SourceStatus = { sourceRevision: number; settingsRevision: number; proofRequested: boolean; sources: SourceAccount[] };
type WorkItem = { workId: string; question: string; requester: string; readers: string[] | null; state: string; payloadHash: string; payload: unknown; schedule?: {cadence:string; timezone:string; catch_up:string; response_window_seconds:number} | null; period?: {key:string; start_at?:string; end_at?:string} | null; exports?: {id:string;kind:string;destination:string;fields:string[]}[] | null; publications?: {id:string;kind:string;readers:{kind:string;id:string}[];fields:string[]}[] | null };
type WorkerStatus = { enabled: boolean; optOut: boolean; settingsRevision: number; acknowledgedPolicy: number; nextCheck: string; checking: boolean; problem: string; items: WorkItem[] };
type OrgPolicy = { value: boolean; policyVersion: number; settingsRevision: number };

async function workerRequest<T>(path = "", body?: object): Promise<T> {
  const response = await fetch(`/api/teams/worker${path}`, body ? { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) } : undefined);
  const value = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(value.error || "Teams is unavailable.");
  return value as T;
}

function TeamsWork() {
  const [status, setStatus] = useState<WorkerStatus | null>(null);
  const [policy, setPolicy] = useState<OrgPolicy | null>(null);
  const [allowMetrics, setAllowMetrics] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const refresh = useCallback(async () => { try { setStatus(await workerRequest<WorkerStatus>()); } catch { setError("Could not read Teams requests."); } }, []);
  useEffect(() => { void refresh(); const timer = window.setInterval(() => { void refresh(); }, 5000); return () => window.clearInterval(timer); }, [refresh]);
  const act = async (path: string, body: object) => {
    setBusy(true); setError("");
    try { await workerRequest(path, body); await refresh(); }
    catch (e) { await refresh(); setError(e instanceof Error ? e.message : "Could not update Teams."); }
    finally { setBusy(false); }
  };
  const reviewPolicy = async () => { setBusy(true); setError(""); try { setPolicy(await workerRequest<OrgPolicy>("/policy")); } catch (e) { setError(e instanceof Error ? e.message : "Could not read the organization default."); } finally { setBusy(false); } };
  return <section aria-label="Teams requests">
    <h3>Teams requests</h3>
    {!status?.enabled ? <>
      <p>Checking is paused. Review the organization default before enabling this library.</p>
      <button type="button" disabled={busy || !status} onClick={() => void reviewPolicy()}>Review organization default</button>
      {policy ? <><p>Automatic attributed statistics are {policy.value ? "enabled" : "disabled"} by the organization. Private conversations can contribute eligible numeric statistics; content and AI answers have separate permissions.</p>
        <label><input type="checkbox" checked={allowMetrics} onChange={e => setAllowMetrics(e.target.checked)} /> Allow automatic eligible statistics when the organization enables them.</label>
        <div><button type="button" disabled={busy} onClick={() => void act("/settings", { enabled: true, optOut: !allowMetrics, settingsRevision: policy.settingsRevision, policyVersion: policy.policyVersion })}>Start checking requests</button></div></> : null}
    </> : <>
      <p>Checking is on. New remote requests can wait 10–15 minutes while this Mac is idle. Use Check now to look sooner.</p>
      {status.nextCheck && !status.nextCheck.startsWith("0001") ? <p>Next check: {new Date(status.nextCheck).toLocaleTimeString()}.</p> : null}
      <button type="button" disabled={busy || status.checking} onClick={() => void act("/check", {})}>Check now</button>{" "}
      <button type="button" disabled={busy} onClick={() => void act("/settings", { enabled: false, optOut: status.optOut, settingsRevision: status.settingsRevision, policyVersion: status.acknowledgedPolicy })}>Pause checking</button>
    </>}
    {status?.problem ? <p role="status">{status.problem}</p> : null}
    {status?.items.map(item => <div className="teams-source" key={item.workId}>
      <h4>{item.question}</h4><p>Requested by {item.requester || "your organization"}. Readers: {item.readers?.join(", ") || "none listed"}.</p>
	  {item.schedule ? <><p>Repeats {item.schedule.cadence} in {item.schedule.timezone}; missed periods: {item.schedule.catch_up}. Response window: {item.schedule.response_window_seconds} seconds.</p><p>Current period: {item.period?.start_at} to {item.period?.end_at}. The token total covers all eligible history.</p></> : null}
	  {item.publications?.map(x => <p key={x.id}>Report readers: {x.readers.map(r => r.id).join(", ")}. Fields: {x.fields.join(", ")}. Each result requires your review before sending.</p>)}
	  {item.exports?.map(x => <p key={x.id}>Export to {x.destination}. Fields: {x.fields.join(", ")}. Each result requires your review before sending.</p>)}
      <p>Status: {item.state.replaceAll("_", " ")}.</p>
      {item.state === "awaiting_source_consent" ? <><p>Allow this request{item.schedule ? " and its future scheduled periods" : ""} to check opaque source membership. This does not approve sending an answer.</p><button type="button" disabled={busy || !status} onClick={() => void act("/allow-source", { workId: item.workId, settingsRevision: status.settingsRevision })}>Allow source checks</button></> : null}
      {item.payload ? <pre>{JSON.stringify(item.payload, null, 2)}</pre> : null}
      {item.state === "awaiting_review" ? <button type="button" disabled={busy} onClick={() => void act("/approve-result", { workId: item.workId, payloadHash: item.payloadHash })}>Approve and send this result</button> : null}{" "}
      {["awaiting_source_consent", "awaiting_review", "decline_pending"].includes(item.state) ? <button type="button" disabled={busy} onClick={() => void act("/decline", { workId: item.workId })}>{item.state === "decline_pending" ? "Retry decline" : "Decline request"}</button> : null}
    </div>)}
    {error ? <p role="alert">{error}</p> : null}
  </section>;
}

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
    {status?.state === "connected_paused" ? <><TeamsWork /><SourceAccounts /></> : null}
  </div>;
}
