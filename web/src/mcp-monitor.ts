import { queryRequest } from "./query-request";

export type MCPStatus = { enabled: boolean; transport: string; command: string; args: string[]; install_location?: string; note: string; skill?: { name: string; text: string; paths?: Record<string, string> } };

// Reads the connection settings. Polls share one in-flight refresh; explicit
// refresh and leaving the page invalidate all older publications. Call
// history is a dashboard (mcp_calls), read through query-v3.
export function createMCPMonitor(handlers: {
  status: (status: MCPStatus) => void;
  error: (error: Error | null) => void;
}) {
  let current: { controller: AbortController; done: Promise<void> } | undefined;

  function stop() {
    current?.controller.abort();
    current = undefined;
  }

  function refresh(replace = false): Promise<void> {
    if (current && !replace) return current.done;
    stop();
    const controller = new AbortController(), { signal } = controller;
    const done = queryRequest<MCPStatus>("/api/mcp", { signal }).then(data => {
      if (!signal.aborted) { handlers.status(data); handlers.error(null); }
    }, failure => {
      if (!signal.aborted) handlers.error(failure instanceof Error ? failure : new Error(String(failure)));
    }).then(() => {
      if (current?.controller === controller) current = undefined;
    });
    current = { controller, done };
    return done;
  }

  return { refresh, stop };
}
