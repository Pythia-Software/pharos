import React, { useEffect, useState } from "react";
import { Modal } from "./modal";
import { queryRow } from "./query-transport";
import { loadSchema } from "@pythia-software/query-table-core";
import mcpSchema from "../../schemas/mcp_calls.schema.json";
import { formatTokens } from "./token-count";
import { CodeRuns, codeRuns } from "./code-highlight";

export type MCPCall = { id: number; called_at: string; tool_name: string; arguments_json: string; status: string; error_text: string | null; duration_ms: number; response_bytes: number; estimated_output_tokens: number; result_count: number | null; truncated: boolean };

// Table rows contain only the selected columns. Detail fields must be loaded
// by identity, independently of the current column selection or saved view.
export async function loadMCPCall(id: string, signal?: AbortSignal): Promise<MCPCall> {
  const row = await queryRow<MCPCall>("mcp_calls", loadSchema<MCPCall>(mcpSchema), id, signal);
  if (!row) throw new Error("This MCP call is no longer available. Only the latest 5,000 calls are retained.");
  return row;
}

export function mcpDetailNumber(value: unknown, format: (value: number) => string = value => value.toLocaleString()): string {
  return typeof value === "number" && Number.isFinite(value) ? format(value) : "—";
}

export function MCPCallDetails({ id, onClose }: { id: string; onClose: () => void }) {
  const [call, setCall] = useState<MCPCall | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    loadMCPCall(id, controller.signal).then(value => { if (!controller.signal.aborted) setCall(value); })
      .catch(failure => { if (!controller.signal.aborted) setError(failure instanceof Error ? failure.message : "Could not load this MCP call."); });
    return () => controller.abort();
  }, [id]);
  return <Modal className="mcp-dialog" aria-label="MCP call details" onClose={onClose}>
    <div className="mcp-card-heading"><div><h2>{call?.tool_name ?? "MCP call"}</h2>{call ? <p className="muted">{new Date(call.called_at).toLocaleString()} · {call.status}</p> : null}</div><button type="button" onClick={onClose}>Close</button></div>
    {error ? <p className="query-table-error" role="alert">{error}</p> : !call ? <p role="status">Loading call details…</p> : <>
      <p className="muted">{mcpDetailNumber(call.estimated_output_tokens, formatTokens)} estimated output tokens · {mcpDetailNumber(call.response_bytes)} bytes · {mcpDetailNumber(call.duration_ms)} ms{call.result_count == null ? "" : ` · ${mcpDetailNumber(call.result_count)} ${call.result_count === 1 ? "result" : "results"}`}{call.truncated ? " · truncated" : ""}</p>
      <h3>Sanitized arguments</h3><pre className="mcp-code"><CodeRuns runs={codeRuns(call.arguments_json, "json")} /></pre>
      {call.error_text ? <><h3>Error</h3><pre className="mcp-code badtext">{call.error_text}</pre></> : null}
    </>}
  </Modal>;
}
