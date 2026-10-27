import React, { useEffect, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import { loadSchema, memoryStorageAdapter } from "@pythia-software/query-table-core";
import { useQueryTable } from "@pythia-software/query-table-react";
import { DataTable, QueryBuilder, MetricsPanel, SelectionToolbar, defaultRenderers } from "@pythia-software/query-table-ui";
import "@pythia-software/query-table-ui/theme.css";
import "./inbox.css";
import { changeTags, normalizeEntry, type Entry, type Summary } from "./model";

type Row = Summary & { application_name: string; application_version: string };
const schema = loadSchema<Row>({ name: "uifeedback_submissions_v1", version: 1, idField: "id", defaultLimit: 25,
  defaultSort: [{ field: "received_at", dir: "desc" }],
  defaultSelect: [{ field: "id", width: 90 }, { field: "received_at", width: 180 }, { field: "application_name", width: 130 }, { field: "kind", width: 110 }, { field: "comment", width: 350 }, { field: "tags", width: 150 }, { field: "annotation_count", width: 110 }],
  fields: [
    ...[["id", "Report"], ["application_name", "Application"], ["application_version", "Version"], ["kind", "Kind"], ["comment", "Commentary"], ["email", "Contact email"]].map(([name, label]) => ({ name, label, type: "text", render: name === "id" ? "feedback_open" : undefined, aggregate: { groupable: true } })),
    { name: "received_at", label: "Received", type: "date", render: "feedback_time", aggregate: { groupable: true } },
    { name: "tags", label: "Tags", type: "text[]", aggregate: { groupable: true } },
    ...[["annotation_count", "Annotations"], ["screenshot_count", "Screenshots"]].map(([name, label]) => ({ name, label, type: "number", aggregate: { groupable: true } })),
    { name: "has_diagnostics", label: "Diagnostics", type: "boolean", aggregate: { groupable: true } },
  ],
});
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, init); if (res.status === 401) throw new Error("Your session expired. Sign in again to continue.");
  if (!res.ok) throw new Error((await res.text()).slice(0, 500)); return res.status === 204 ? undefined as T : await res.json();
}
function Inbox() {
  const [rows, setRows] = useState<Row[]>([]), [loading, setLoading] = useState(true), [error, setError] = useState(""), [busy, setBusy] = useState(false);
  const [detail, setDetail] = useState<Entry | null>(null), [tag, setTag] = useState(""), [notice, setNotice] = useState("");
  const storage = useMemo(() => memoryStorageAdapter(), []);
  const api = useQueryTable<Row>({ schema, clientRows: rows, storage, syncUrl: false, onRefresh: () => { void refresh(); } });
  const renderers = useMemo(() => ({ ...defaultRenderers,
    feedback_time: ({ value }: { value: unknown }) => typeof value === "string" ? <time dateTime={value}>{new Date(value).toLocaleString()}</time> : "",
    feedback_open: ({ value }: { value: unknown }) => typeof value === "string" && /^[0-9a-f-]{36}$/.test(value) ? <button onClick={() => void open(value)}>Open</button> : "",
  }), []);
  async function refresh() { setLoading(true); try { const data = await request<{ submissions: Summary[] }>("/api/submissions"); setRows(data.submissions.map(r => ({ ...r, application_name: r.application.name, application_version: r.application.version ?? "" }))); setError(""); } catch (e) { setError(String(e)); } finally { setLoading(false); } }
  async function open(id: string) { try { setDetail(normalizeEntry(await request<Entry>(`/api/submissions/${id}`))); const u = new URL(location.href); u.searchParams.set("submission", id); history.replaceState(null, "", u); } catch (e) { setError(String(e)); } }
  useEffect(() => { void refresh(); const id = new URLSearchParams(location.search).get("submission"); if (id) void open(id); }, []);
  async function mutate(ids: string[], action: "add" | "remove" | "delete") {
    if (action !== "delete" && !tag.trim()) return;
    if (action === "delete" && !confirm(`Permanently delete ${ids.length} report${ids.length === 1 ? "" : "s"}, including screenshots and logs?`)) return;
    setBusy(true); setError(""); let done = 0;
    try {
      for (const id of ids) {
        const row = rows.find(r => r.id === id); if (!row) continue;
        if (action === "delete") await request(`/api/submissions/${id}`, { method: "DELETE", headers: { "If-Match": row.revision } });
        else await request(`/api/submissions/${id}`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ tags: changeTags(row.tags, tag, action === "remove"), revision: row.revision }) });
        done++; if (detail?.id === id && action === "delete") setDetail(null);
      }
      api.selection.clear(); setNotice(`${done} report${done === 1 ? "" : "s"} ${action === "delete" ? "deleted" : "updated"}.`);
      await refresh(); if (detail && action !== "delete") await open(detail.id);
    } catch (e) { await refresh(); setError(`${done} completed. ${String(e)}`); } finally { setBusy(false); }
  }
  const actions = (ids: string[]) => <><input aria-label="Tag" placeholder="Tag" maxLength={80} value={tag} onChange={e => setTag(e.target.value)} /><button disabled={busy || !tag.trim()} onClick={() => void mutate(ids, "add")}>Add tag</button><button disabled={busy || !tag.trim()} onClick={() => void mutate(ids, "remove")}>Remove tag</button><button className="danger" disabled={busy} onClick={() => void mutate(ids, "delete")}>Delete</button></>;
  return <main className="feedback-inbox"><header><div><p className="eyebrow">Pythia Software</p><h1>Feedback inbox</h1><p>Reports and prompts from your applications · retained for 90 days</p></div><nav><button disabled={loading || busy} onClick={() => void refresh()}>Refresh</button><a href="/auth/login">Sign in</a><form action="/auth/logout" method="post"><button>Sign out</button></form></nav></header>
    {error && <p role="alert" className="error">{error}</p>}{notice && <p role="status">{notice}</p>}
    <section className="inbox-table" aria-label="Feedback submissions"><QueryBuilder api={api} fields={api.computed.catalogue} total={api.total} running={loading || api.loading} compact />
      <MetricsPanel api={api} aggregations={api.aggregations} fields={api.computed.catalogue} renderers={renderers} />
      <SelectionToolbar selection={api.selection} actions={ids => actions(ids.map(String))} />
      <DataTable fields={api.visibleFields} rows={api.rows} query={api.query} onQueryChange={api.setQuery} rowId={api.rowId} renderers={renderers} columnDrag={api.columnDrag} total={api.total} loading={loading || api.loading} selection={api.query.grouping ? undefined : api.selection} emptyMessage={loading ? "Loading reports…" : "No reports match this view."} />
      {api.error && <p role="alert">{api.error.message}</p>}
    </section>
    {detail && <section className="report-detail" aria-label="Report details"><div className="detail-heading"><h2>{detail.application.name} · {detail.kind}</h2><button onClick={() => { setDetail(null); history.replaceState(null, "", "/"); }}>Close report</button></div>
      <p><time>{new Date(detail.received_at).toLocaleString()}</time> · {detail.id}</p><p>Tags: {detail.tags.join(", ") || "None"}</p><div className="report-actions">{actions([detail.id])}</div>
      {detail.email && <p>Contact: <a href={`mailto:${encodeURIComponent(detail.email)}`}>{detail.email}</a></p>}
      <p className="commentary">{detail.comment}</p>
      {detail.annotations.map((a, i) => <article key={a.id}><h3>{i + 1}. {a.label || "Annotation"}</h3><p className="commentary">{a.comment}</p><p><code>{a.selector}</code></p><p><time>{new Date(a.captured_at).toLocaleString()}</time> · {a.viewport.width} × {a.viewport.height}</p>{a.url && <p><a href={a.url} target="_blank" rel="noreferrer">{a.url}</a></p>}{a.text && <blockquote>{a.text}</blockquote>}{a.screenshot && <img alt={`Captured element: ${a.label || i + 1}`} src={`data:${a.screenshot.mime};base64,${a.screenshot.data}`} />}</article>)}
      {detail.diagnostics && <details><summary>Diagnostic logs</summary>{Object.entries(detail.diagnostics).map(([kind, text]) => <section key={kind}><h3>{kind}</h3><pre>{text}</pre></section>)}</details>}
      <div className="report-actions"><button onClick={() => { const blob = new Blob([JSON.stringify(detail, null, 2)], { type: "application/json" }); const url = URL.createObjectURL(blob); const a = document.createElement("a"); a.href = url; a.download = `${detail.id}.json`; a.click(); URL.revokeObjectURL(url); }}>Download report</button><button onClick={() => void navigator.clipboard.writeText(JSON.stringify(detail, null, 2)).then(() => setNotice("Report copied."), () => setError("Could not copy. Download the report instead."))}>Copy report for an agent</button></div>
    </section>}
  </main>;
}
createRoot(document.getElementById("inbox")!).render(<Inbox />);
