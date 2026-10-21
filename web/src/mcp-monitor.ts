import { queryRequest } from "./query-request";

export type MCPStatus = { enabled: boolean; transport: string; command: string; args: string[]; install_location?: string; note: string; skill?: { name: string; text: string; paths?: Record<string, string> } };
export type MCPHistory = { stats: { total_calls: number; failed_calls: number; average_output_tokens: number; largest_output_tokens: number; truncated_calls: number } };
type Part = "status" | "history";

// Status and summary have independent reads: a stalled history read must not
// hide a usable connection definition. Polls share one in-flight refresh;
// explicit refresh and leaving the page invalidate all older publications.
export function createMCPMonitor(handlers: {
  status: (status: MCPStatus) => void;
  history?: (history: MCPHistory) => void;
  error: (part: Part, error: Error | null) => void;
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
    const read = async <T>(part: Part, url: string, publish: (data: T) => void) => {
      try {
        const data = await queryRequest<T>(url, { signal });
        if (!signal.aborted) { publish(data); handlers.error(part, null); }
      } catch (failure) {
        if (!signal.aborted) handlers.error(part, failure instanceof Error ? failure : new Error(String(failure)));
      }
    };
    const done = Promise.all([
      read("status", "/api/mcp", handlers.status),
      ...(handlers.history ? [read("history", "/api/mcp/calls?limit=1", handlers.history)] : []),
    ]).then(() => {
      if (current?.controller === controller) current = undefined;
    });
    current = { controller, done };
    return done;
  }

  return { refresh, stop };
}
