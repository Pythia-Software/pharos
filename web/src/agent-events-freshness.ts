// Agent event datasets (agent_events, agent_event_messages, agent_event_rates) leave out
// the rows of conversations whose events rest on superseded execution
// evidence, and their query responses carry the server's agentEventsCoverage
// as `coverage`. Absent rows are then pending, never "no events": every view
// that shows these counts reads the latest coverage here and says so. The
// state is the server's; nothing here decides freshness.
import { useEffect, useState } from "react";

export type AgentEventsCoverage = { state: "current" | "pending" | "unknown" | (string & {}); pending_conversations?: number; withheld_conversations?: number };
export const agentEventDatasets = ["agent_events", "agent_event_messages", "agent_event_rates"] as const;

/** The sentence a view shows beside agent-event counts, or "" when current. */
export function agentEventsFreshnessNote(coverage: AgentEventsCoverage | null | undefined): string {
  if (!coverage || coverage.state === "current") return "";
  const withheld = Number(coverage.withheld_conversations) || 0, pending = Number(coverage.pending_conversations) || 0;
  const parts = [
    withheld ? `Events for ${withheld.toLocaleString()} conversation${withheld === 1 ? "" : "s"} are left out until they are rebuilt for changed session evidence.` : "",
    pending ? `${pending.toLocaleString()} conversation${pending === 1 ? " is" : "s are"} waiting for events to be rebuilt.` : "",
  ].filter(Boolean);
  if (coverage.state === "unknown") parts.unshift("Conversations changed since the last events run.");
  return `${parts.join(" ") || "Some events are waiting to be rebuilt."} These counts may change.`;
}

/**
 * The sentence for views built from stored events that keep counting them
 * while a rebuild is pending (Fixes, Risky actions safeguard checks), unlike
 * the datasets, which leave them out. "" when current.
 */
export function agentEventsStillCountedNote(coverage: AgentEventsCoverage | null | undefined): string {
  if (!coverage || coverage.state === "current") return "";
  const waiting = Math.max(Number(coverage.pending_conversations) || 0, Number(coverage.withheld_conversations) || 0);
  const lead = waiting ? `Events for ${waiting.toLocaleString()} conversation${waiting === 1 ? " are" : "s are"} waiting to be rebuilt for changed transcripts or session evidence and still count here.`
    : coverage.state === "unknown" ? "Conversations changed since the last events run, and their events still count here." : "Some events are waiting to be rebuilt and still count here.";
  return `${lead} These counts may change.`;
}

/**
 * What a saved agent-event item says when the dataset no longer returns it:
 * the server's item answer (`/api/query/<dataset>/item`) says pending when the
 * item is stored but withheld until its conversation's events are rebuilt.
 * null means it is truly unavailable (rebuilt away or removed).
 */
export function agentEventItemPendingMessage(dataset: string, answer: { row?: unknown; pending?: boolean; coverage?: AgentEventsCoverage } | null | undefined): string | null {
  if (!(agentEventDatasets as readonly string[]).includes(dataset) || !answer || answer.row || answer.pending !== true) return null;
  const noun = dataset === "agent_events" ? "This event" : "This message's events";
  return `${noun} will be back once events are rebuilt for changed session evidence; until then it is left out rather than shown with an outdated attribution. Retry after the next events run.`;
}

/**
 * Whether no agent message has been read for events yet. Nothing visible is
 * not that when stored events are withheld for a rebuild (an upgrade or a
 * changed session claim can withhold every conversation): those views say
 * the rebuild is pending instead.
 */
export function agentEventsNeverRead(scanned: number, coverage: AgentEventsCoverage | null | undefined): boolean {
  return !scanned && !(Number(coverage?.withheld_conversations) > 0);
}

/** "≈" for a count that is not yet current. */
export const agentEventsFreshnessMark = (coverage: AgentEventsCoverage | null | undefined) => coverage && coverage.state !== "current" ? "≈" : "";

/** The dataset a query URL addresses, if it is an agent-event dataset. */
export function agentEventDatasetOf(url: string): string | null {
  const match = /\/api\/query\/([a-z_]+)\/query-v3/.exec(url);
  return match && (agentEventDatasets as readonly string[]).includes(match[1]) ? match[1] : null;
}

let latest: AgentEventsCoverage | null = null;
const listeners = new Set<(coverage: AgentEventsCoverage | null) => void>();

/** Records the coverage an agent-event query response carried. */
export function reportQueryCoverage(url: string, body: unknown) {
  if (!agentEventDatasetOf(url) || !body || typeof body !== "object") return;
  const coverage = (body as { coverage?: AgentEventsCoverage }).coverage;
  if (!coverage || typeof coverage.state !== "string") return;
  if (JSON.stringify(coverage) === JSON.stringify(latest)) return;
  latest = coverage;
  listeners.forEach(listener => listener(latest));
}

export function currentAgentEventsCoverage() { return latest; }

/** The latest coverage any agent-event query reported. */
export function useAgentEventsCoverage(): AgentEventsCoverage | null {
  const [coverage, setCoverage] = useState(latest);
  useEffect(() => { listeners.add(setCoverage); setCoverage(latest); return () => { listeners.delete(setCoverage); }; }, []);
  return coverage;
}
