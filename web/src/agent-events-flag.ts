import { preferences } from "./preferences";

// Agent Experience is a preview: its Analyze dashboard and query tables are
// listed only when Settings → Background Processing turns it on. Direct links work
// either way. The flag is a library preference, so it follows the library.
export const AGENT_EVENTS_KEY = "pharos-agent-events-v1";
export const agentEventTopics = new Set(["agent_experience"]);
export const agentEventDatasets = new Set(["agent_events", "agent_event_messages"]);
// A library whose configuration sets agent_events_enabled lists them too:
// its switch is locked on, and preferences.js says so in pharosConfiguredFlags.
export function agentEventsEnabled(): boolean {
  if (typeof window === "undefined") return false;
  const configured = (window as { pharosConfiguredFlags?: Record<string, boolean> }).pharosConfiguredFlags;
  return configured?.[AGENT_EVENTS_KEY] === true || preferences().get<boolean>(AGENT_EVENTS_KEY, false) === true;
}
