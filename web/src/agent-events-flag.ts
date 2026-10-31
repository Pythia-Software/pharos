import { preferences } from "./preferences";

// Agent Experience is a preview: its Analyze dashboard and query tables are
// listed only when Settings → App preferences turns it on. Direct links work
// either way. The flag is a library preference, so it follows the library.
export const AGENT_EVENTS_KEY = "pharos-agent-events-v1";
export const agentEventTopics = new Set(["agent_experience"]);
export const agentEventDatasets = new Set(["agent_events", "agent_event_messages"]);
export function agentEventsEnabled(): boolean {
  return typeof window !== "undefined" && preferences().get<boolean>(AGENT_EVENTS_KEY, false) === true;
}
