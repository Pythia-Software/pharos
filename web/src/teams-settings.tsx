import React, { useCallback, useEffect, useState } from "react";
import "./teams-settings.css";

type EnrollmentStatus = { state: string; available: boolean; library: string; fingerprint: string; problem: string; optedOut?: boolean };

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
  </div>;
}
