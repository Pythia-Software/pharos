import React, { useCallback, useEffect, useRef, useState } from "react";
import { ViewDisclosure } from "./view-disclosure";
import "./teams-settings.css";

type EnrollmentStatus = { state: string; available: boolean; library: string; fingerprint: string; problem: string; optedOut?: boolean };
type SourceAccount = { bindingId: string; provider: string; sourceName: string; accountLabel: string; host: string; root: string; identifiable: number; ambiguous: number; mode: string; exposed: boolean; unindexed?: boolean };
type SourceStatus = { sourceRevision: number; settingsRevision: number; proofRequested: boolean; sources: SourceAccount[] };
type ScheduledTerms = { field: string; coverage: string; defaultAndOptOut: string; cutoff: string; inference: string; withdrawal: string; anchorAt: string; untilAt: string };
type WorkItem = { scheduledResult?: { total: string; sourceState: string; exactJson: string }; scheduledTerms?: ScheduledTerms; scheduledAssignment?: { deadline_at: string; timezone: string }; workId: string; question: string; prompt: string; retention: unknown; requester: string; requesterId: string; requesterFallback?: string; readerLabels?: {entries:{person_id:string;status:string;display_name?:string}[]}; labelsReady: boolean; labelsToken?: string; labelsExpireAt?: string; generation: number; readers: string[] | null; state: string; payloadHash: string; configHash: string; payload: unknown; publicationV2?: {schema_version:number; descriptors:{id:string;readers:{kind:string;id:string}[];outputs:{fields:{id:string;unit:string}[];coverage:string[]};destination:{kind:string};disclosures:string[]}[]} | null; schedule?: {cadence:string; timezone:string; catch_up:string; response_window_seconds:number} | null; period?: {key:string; start_at?:string; end_at?:string} | null; exports?: {id:string;kind:string;destination:string;fields:string[]}[] | null; publications?: {id:string;kind:string;readers:{kind:string;id:string}[];fields:string[];include_completeness?:boolean}[] | null };
type WorkerStatus = { enabled: boolean; optOut: boolean; settingsRevision: number; acknowledgedPolicy: number; nextCheck: string; checking: boolean; problem: string; sourceProblem?: string; items: WorkItem[]; modelApproved: boolean };
type ModelInspection = { provider: string; model: string; effort: string; account: string; sessionDigest: string; authDigest: string; settingsRevision: number };
type SentItem = {workId:string; question:string; payload:unknown; sentAt:string};
type OrgPolicy = { value: boolean; policyVersion: number; settingsRevision: number };

async function workerRequest<T>(path = "", body?: object, signal?: AbortSignal): Promise<T> {
  const response = await fetch(`/api/teams/worker${path}`, body ? { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), signal } : { signal });
  const value = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(value.error || "Teams is unavailable.");
  return value as T;
}

type ProtectionPolicy = { policyId: string; policyDigest: string; disclosure: string; acknowledged: boolean; settingsRevision: number; continuity: string };

function LocalProtectionPolicy({ onChange }: { onChange: () => Promise<void> }) {
  const [policy, setPolicy] = useState<ProtectionPolicy | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const refresh = useCallback(async () => { try { setPolicy(await workerRequest<ProtectionPolicy>("/protection-policy")); setError(""); } catch { setError("Could not read the local protection policy."); } }, []);
  useEffect(() => { void refresh(); }, [refresh]);
  const acknowledge = async () => {
    if (!policy) return;
    setBusy(true); setError("");
    try { await workerRequest("/protection-policy", { acknowledge: true, settingsRevision: policy.settingsRevision }); await refresh(); await onChange(); }
    catch (e) { await refresh(); setError(e instanceof Error ? e.message : "Could not acknowledge the local protection policy."); }
    finally { setBusy(false); }
  };
  return <section aria-label="Local source protection"><h4>Local source protection</h4>
    {policy ? <><p>{policy.disclosure}</p><p>{policy.continuity === "held" ? "Source protection needs reconciliation. You can continue using this library." : policy.continuity === "reconciling" ? "Source protection is being reconciled." : "Source protection is not ready yet."}</p>{policy.acknowledged ? <p>Protection policy acknowledged for this library.</p> : <button type="button" disabled={busy} onClick={() => void acknowledge()}>Acknowledge protection policy</button>}</> : null}
    {error ? <p role="alert">{error}</p> : null}
  </section>;
}

function retentionText(raw: unknown): string {
  const policy = raw as {mode?: string; duration_days?: number | null} | null;
  if (policy?.mode === "organization_controlled") return typeof policy.duration_days === "number" ? `Your organization currently retains this for ${policy.duration_days} days and controls the retention policy.` : "Your organization controls retention; there is currently no scheduled expiry.";
  if (typeof policy?.duration_days === "number") return `Retention is ${policy.duration_days} days.`;
  return "No scheduled expiry is specified for this request.";
}

function ReaderAudiences({ item, refresh }: { item: WorkItem; refresh: () => Promise<void> }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const current = useRef<AbortController | null>(null);
  const load = useCallback(async (signal?: AbortSignal) => {
    current.current?.abort();
    const controller = new AbortController(); current.current = controller;
    const abort = () => controller.abort();
    if (signal?.aborted) controller.abort(); else signal?.addEventListener("abort", abort, {once:true});
    setBusy(true); setError("");
    try { await workerRequest("/reader-labels", {workId:item.workId, configHash:item.configHash}, controller.signal); if (!controller.signal.aborted) await refresh(); }
    catch { if (!controller.signal.aborted) { setError("Names could not be loaded. Refresh to try again."); await refresh(); } }
    finally { signal?.removeEventListener("abort", abort); if (current.current === controller) { setBusy(false); current.current = null; } }
  }, [item.workId, item.configHash, item.state, item.generation, refresh]);
  useEffect(() => () => current.current?.abort(), []);
  useEffect(() => {
    if (item.labelsReady) return;
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load, item.labelsReady]);
  useEffect(() => {
    if (!item.labelsExpireAt) return;
    const timer = window.setTimeout(() => { void load(); }, Math.max(0, Date.parse(item.labelsExpireAt!) - Date.now()));
    return () => window.clearTimeout(timer);
  }, [item.labelsExpireAt, load]);
  const entries = item.readerLabels?.entries || [];
  const row = (id: string, reportOnly = false) => {
    const label = entries.find(x => x.person_id === id);
    const fallback = id === item.requesterId && label?.status !== "inactive" ? item.requesterFallback : "";
    const name = label?.display_name || fallback;
    return <li key={id}><bdi>{name || "Name unavailable"}</bdi>{fallback && !label?.display_name ? " (request name)" : ""}{label?.status === "inactive" ? " — Inactive account; access may resume if reactivated" : ""}{reportOnly ? " — Report only" : ""} <code>{id}</code></li>;
  };
  return <section aria-label="Readers"><h5>Response collection</h5><ul>{item.readers?.map(id => row(id))}</ul><h5>Web report</h5><ul>{item.publicationV2?.descriptors.flatMap(x => x.readers.map(r => row(r.id, !item.readers?.includes(r.id))))}</ul>{!item.labelsReady ? <p role="status">Names are unavailable for one or more readers. Refresh names before approving, or decline this request.</p> : null}{error ? <p role="alert">{error}</p> : null}<button type="button" disabled={busy} onClick={() => void load()}>{busy ? "Loading names…" : "Refresh names"}</button></section>;
}

function TeamsWork() {
  const [status, setStatus] = useState<WorkerStatus | null>(null);
  const [policy, setPolicy] = useState<OrgPolicy | null>(null);
  const [allowMetrics, setAllowMetrics] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [model, setModel] = useState<ModelInspection | null>(null);
  const [sent, setSent] = useState<SentItem[] | null>(null);
  const refresh = useCallback(async () => { try { setStatus(await workerRequest<WorkerStatus>()); } catch { setError("Could not read Teams requests."); } }, []);
  useEffect(() => { void refresh(); const timer = window.setInterval(() => { void refresh(); }, 5000); return () => window.clearInterval(timer); }, [refresh]);
  const act = async (path: string, body: object) => {
    setBusy(true); setError("");
    try { await workerRequest(path, body); await refresh(); }
    catch (e) { await refresh(); setError(e instanceof Error ? e.message : "Could not update Teams."); }
    finally { setBusy(false); }
  };
  const reviewPolicy = async () => { setBusy(true); setError(""); try { setPolicy(await workerRequest<OrgPolicy>("/policy")); } catch (e) { setError(e instanceof Error ? e.message : "Could not read the organization default."); } finally { setBusy(false); } };
  const inspectModel = async () => { setBusy(true); setError(""); try { setModel(await workerRequest<ModelInspection>("/model/inspect", {})); } catch (e) { setError(e instanceof Error ? e.message : "Could not inspect the model account."); } finally { setBusy(false); } };
  const loadSent = async () => { setBusy(true); setError(""); try { setSent((await workerRequest<{items:SentItem[]}>("/log")).items); } catch (e) { setError(e instanceof Error ? e.message : "Could not load the send log."); } finally { setBusy(false); } };
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
    <LocalProtectionPolicy onChange={refresh} />
    {status?.enabled ? <section aria-label="Model processing permission"><h4>Claude processing</h4>
      <p>Content answers require a separate processing permission and a request-specific review. Claude processes eligible material remotely using your authenticated account.</p>
      <p>{status.modelApproved ? "This account and model are approved for processing." : "No current model processing permission."}</p>
      <button type="button" disabled={busy} onClick={() => void inspectModel()}>Review model account</button>{" "}
      {status.modelApproved ? <button type="button" disabled={busy} onClick={() => void act("/model/revoke", {})}>Revoke model permission</button> : null}
      {model ? <><p>Account: {model.account}. Provider: {model.provider}. Model: {model.model}. Effort: {model.effort}. Processing occurs with the provider, outside this Mac.</p>
        <button type="button" disabled={busy} onClick={() => { void act("/model/approve", model).then(() => setModel(null)); }}>Allow this account and model</button>{" "}<button type="button" disabled={busy} onClick={() => setModel(null)}>Cancel</button></> : null}
    </section> : null}
    {status?.problem ? <p role="status">{status.problem}</p> : null}
    {status?.sourceProblem ? <p role="status">{status.sourceProblem}</p> : null}
    {status?.items.map(item => <div className="teams-source" key={item.workId}>
      <h4>{item.question}</h4><p>Requested by {item.requester || "your organization"}.{!item.publicationV2 ? ` Readers: ${item.readers?.map(id => id === item.requesterId && item.requesterFallback ? `${item.requesterFallback} (request name; ${id})` : id).join(", ") || "none listed"}.` : ""}</p>
      {status.enabled && item.publicationV2 && ["awaiting_source_consent", "awaiting_review"].includes(item.state) ? <ReaderAudiences item={item} refresh={refresh} /> : null}
	  {item.schedule ? <><p>Repeats {item.schedule.cadence} in {item.schedule.timezone}; missed periods: {item.schedule.catch_up}. Response window: {item.schedule.response_window_seconds} seconds.</p><p>Current period: {item.period?.start_at} to {item.period?.end_at}. The token total covers all eligible history.</p></> : null}
      {item.scheduledTerms && item.scheduledAssignment ? <section aria-label="Scheduled report disclosure">
        <h5>Scheduled web report</h5>
        <p>{item.scheduledTerms.field}. {item.scheduledTerms.coverage}.</p>
        <p>{item.scheduledTerms.defaultAndOptOut}</p>
        <p>For this period, the report uses responses accepted by <time dateTime={item.scheduledAssignment.deadline_at}>{new Date(item.scheduledAssignment.deadline_at).toUTCString()}</time>. The schedule runs through <time dateTime={item.scheduledTerms.untilAt}>{new Date(item.scheduledTerms.untilAt).toUTCString()}</time>.</p>
        <p>{item.scheduledTerms.cutoff} {item.scheduledTerms.inference} {item.scheduledTerms.withdrawal}</p>
      </section> : item.publicationV2?.descriptors.map(x => <section key={x.id} aria-label="Report history disclosure"><p>Report with history, published on the web. Total captured tokens ({x.outputs.fields[0]?.unit}) across all eligible captured history. Each manually refreshed snapshot includes a complete or incomplete indicator.</p><p>Each refresh adds a retained snapshot; differences between snapshots can reveal individual contributions.</p><p>Withdrawing later does not remove snapshots already published; retention and explicit invalidation still apply.</p><p>Request digest: <code>{item.configHash}</code>. Every result requires your review before sending.</p></section>)}
	  {item.publications?.map(x => <p key={x.id}>Report readers: {x.readers.map(r => r.id).join(", ")}. Fields: {x.fields.join(", ")}. Completeness indicator: {x.include_completeness ? "included" : "not included"}. Each result requires your review before sending.</p>)}
	  {item.exports?.map(x => <p key={x.id}>Export to {x.destination}. Fields: {x.fields.join(", ")}. Each result requires your review before sending.</p>)}
	  {item.prompt ? <><p>Instructions: {item.prompt}</p><p>Retention: {JSON.stringify(item.retention)}.</p><p>Only locally reviewed, nonprivate content is eligible. Processing uses the approved Claude account and model.</p></> : null}
      {item.publicationV2 ? <p>{retentionText(item.retention)}</p> : null}
      <p>Status: {item.state === "source_safety_waiting" ? "Checking source safety" : item.state.replaceAll("_", " ")}.</p>
      {item.state === "source_safety_waiting" ? <p>Pharos will check again shortly while source safety processing finishes.</p> : null}
	  {item.prompt && item.state === "unavailable" ? <p>This request needs a completely indexed supported source and at least one reviewed, nonprivate conversation. Review a conversation in the library and check again after indexing.</p> : null}
	  {item.prompt && item.state === "started" ? <p>A previous attempt may have started. It will not automatically run the model again; you can decline this request.</p> : null}
	  {item.state === "delivery_unconfirmed" ? <p>The send is being reconciled. Pharos will check for a receipt before retrying.</p> : null}
      {item.state === "awaiting_source_consent" ? <><p>{item.prompt ? "Allow source membership checks and send eligible reviewed content to your approved Claude account to prepare a draft. The answer will still require your review before it is sent." : `Allow this request${item.schedule ? " and its future scheduled periods" : ""} to check opaque source membership. This does not approve sending an answer.`}</p><button type="button" disabled={busy || !status || (!!item.publicationV2 && !item.labelsReady)} onClick={() => void act("/allow-source", { workId: item.workId, settingsRevision: status.settingsRevision, ...(item.publicationV2 ? {configHash: item.configHash, labelsToken: item.labelsToken} : {}) })}>{item.prompt ? "Allow source checks and draft" : item.scheduledTerms ? "Allow source checks for this schedule" : "Allow source checks"}</button></> : null}
      {item.scheduledResult ? <section aria-label="Library result" className="teams-scheduled-result">
        <h5>This library’s result</h5>
        <p><strong>{item.scheduledResult.total}</strong> captured tokens</p>
        <p>{item.scheduledResult.sourceState === "partial" ? "Partial source coverage. This total may omit activity." : "Complete source coverage."}</p>
        <details><summary>Exact response data</summary><pre>{item.scheduledResult.exactJson}</pre></details>
      </section> : item.payload && !item.scheduledTerms ? <pre>{JSON.stringify(item.payload, null, 2)}</pre> : null}
      {item.state === "awaiting_review" ? <button type="button" disabled={busy || (!!item.publicationV2 && !item.labelsReady) || (!!item.scheduledTerms && !item.scheduledResult)} onClick={() => void act("/approve-result", { workId: item.workId, payloadHash: item.payloadHash, ...(item.publicationV2 ? {configHash: item.configHash, labelsToken: item.labelsToken} : {}) })}>Approve and send this result</button> : null}{" "}
      {["awaiting_source_consent", "awaiting_model_permission", "awaiting_review", "decline_pending", "started", "source_safety_waiting"].includes(item.state) ? <button type="button" disabled={busy} onClick={() => void act("/decline", { workId: item.workId })}>{item.state === "decline_pending" ? "Retry decline" : "Decline request"}</button> : null}
    </div>)}
    <section aria-label="Sent responses"><h4>Sent responses</h4><button type="button" disabled={busy} onClick={() => void loadSent()}>Show recent sends</button>
      {sent ? sent.length ? sent.map(item => <ViewDisclosure key={item.workId} viewKey={`teams:sent:${item.workId}`} summary={`${item.question || "Teams response"} · ${new Date(item.sentAt).toLocaleString()}`}><pre>{JSON.stringify(item.payload,null,2)}</pre></ViewDisclosure>) : <p>No sends recorded for this library.</p> : null}
    </section>
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
  const root = useRef<HTMLDivElement>(null);
  const [visible, setVisible] = useState(false);
  useEffect(() => {
    const update = () => setVisible(!document.hidden && !!root.current?.getClientRects().length);
    const observer = new MutationObserver(update);
    observer.observe(document.body, {attributes:true, subtree:true, attributeFilter:["hidden", "class", "style", "aria-hidden"]});
    document.addEventListener("visibilitychange", update); update();
    return () => { observer.disconnect(); document.removeEventListener("visibilitychange", update); };
  }, []);
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
  return <div ref={root} className="layout-preference-card teams-settings">
    <h3>Connect this library</h3>
    <p>Enrolling {status?.library ? <strong>{status.library}</strong> : "a library"} makes its retained history part of your organization’s data.</p>
    <p>Your organization’s visible defaults can share attributed statistics. You can opt out. Conversation content and answers derived from it require your permission; private conversations are excluded from Teams text analysis and AI answers while eligible statistics can still include them.</p>
    <p>Pharos will open your browser to sign in. Compare the code shown here with the one requested there before approving the connection.</p>
    {waiting && status?.fingerprint ? <p role="status">Connection code: <strong><code>{status.fingerprint}</code></strong></p> : null}
    {status?.state === "connected_paused" ? <p role="status">Connected.</p> : null}
    {status?.problem ? <p role="status">{status.problem}</p> : null}
    {error ? <p role="alert">{error}</p> : null}
    {waiting ? <button type="button" disabled={busy} onClick={() => void act(true)}>Cancel connection</button>
      : <button type="button" disabled={busy || !status?.available || status.state === "connected_paused" || status.state === "reconcile"} onClick={() => void act()}>Connect Teams</button>}
    {visible && status?.state === "connected_paused" ? <><TeamsWork /><SourceAccounts /></> : null}
  </div>;
}
