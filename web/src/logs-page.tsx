import React, { useCallback, useEffect, useRef, useState } from "react";
import { Icon } from "./icons";
import { PageLink, useLocationRevision } from "./nested-pages";
import { ViewDisclosure } from "./view-disclosure";
import { useViewEntry, useViewState } from "./view-state";
import { attentionLabel, formatElapsed, formatLogTime, logSourceURL, logSources, logsRoute, sourcesToRead, type Attention, type LogSourceId, type LogsSummary, type SourceSummary } from "./logs-model";
import { refreshLogsSummary, useLogsSummary } from "./logs-summary";
import "./logs.css";

type LogEvent = { id: string; at: string; level: "error" | "warning" | "info"; kind: string; message: string; detail?: string; page?: string; method?: string; path?: string; status?: number; duration_ms?: number; count?: number };
type OutputRun = { started_at?: string; ended_at?: string; exit?: string; running: boolean; crashed: boolean; problems: number; crash?: string; lines: string[]; omitted?: number };
type Operation = { id: string; label: string; state: string; message: string; step?: string; started_at: string; ended_at?: string };
type CrashReport = { name: string; path: string; at: string; process: string; kind: "crash" | "resource"; summary: string; frames?: string[] };
type SourceDocument = { summary: SourceSummary; events?: LogEvent[]; running?: { method: string; path: string; started_at: string; duration_ms: number }[]; runs?: OutputRun[]; operations?: Operation[]; reports?: CrashReport[] };

function LevelIcon({ level }: { level: Attention | "info" | undefined }) {
  if (!level || level === "info") return <span className="logs-level info"><Icon name="info" /></span>;
  return <span className={`logs-level ${level}`} title={level === "error" ? "Error" : "Warning"}><Icon name={level === "error" ? "error" : "severity-high"} /></span>;
}

export function LogsPage({ copy }: { copy: (text: string) => Promise<void> }) {
  useLocationRevision();
  const active = location.pathname === logsRoute;
  const [source] = useViewState<LogSourceId>("log_source", "overview", active);
  const summary = useLogsSummary(active);
  const [loaded, setLoaded] = useState<{ source: LogSourceId; value?: SourceDocument; error?: string } | null>(null);
  // Only the latest request may update the page: an earlier source's
  // answer that arrives late must not replace the one shown.
  const latest = useRef(0);
  const load = useCallback(async (id: LogSourceId) => {
    if (id === "overview") return;
    const request = ++latest.current;
    let next: { source: LogSourceId; value?: SourceDocument; error?: string };
    try {
      const response = await fetch(`/api/logs/source?id=${id}`);
      const value = await response.json();
      if (!response.ok) throw new Error(value.error ?? `HTTP ${response.status}`);
      next = { source: id, value };
    } catch (error) {
      next = { source: id, error: error instanceof Error ? error.message : String(error) };
    }
    if (request === latest.current) setLoaded(next);
  }, []);
  useEffect(() => {
    if (!active) return;
    void load(source);
    // Requests answering now change by the second; the rest by the minute.
    const timer = setInterval(() => void load(source), source === "requests" ? 5_000 : 30_000);
    return () => clearInterval(timer);
  }, [active, source, load]);
  const refresh = () => { void refreshLogsSummary(true); void load(source); };
  const toolbar = <div className="logs-toolbar">
    {summary ? <span className="muted">Updated {formatLogTime(summary.generated_at)}</span> : null}
    <button type="button" className="button-with-icon" onClick={refresh}><Icon name="refresh" />Refresh</button>
  </div>;
  if (source === "overview") return <div className="logs-page">{toolbar}<Overview copy={copy} summary={summary} /></div>;
  const current = loaded?.source === source ? loaded : null;
  return <div className="logs-page">
    {toolbar}
    {current?.error ? <p className="logs-error" role="alert">Could not read this log: {current.error}</p> : null}
    {current?.value ? <SourceView source={source} doc={current.value} /> : current?.error ? null : <p className="muted">Loading…</p>}
  </div>;
}

function Overview({ copy, summary }: { copy: (text: string) => Promise<void>; summary: LogsSummary | null }) {
  const [copied, setCopied] = useState<string>("");
  if (!summary) return <p className="muted">Loading…</p>;
  const toRead = sourcesToRead(summary);
  const copyReport = async () => {
    setCopied("Copying…");
    try {
      const response = await fetch("/api/logs/report");
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      await copy(await response.text());
      setCopied("Copied. Paste it into your agent.");
    } catch (error) {
      setCopied(`Could not copy: ${error instanceof Error ? error.message : String(error)}`);
    }
  };
  return <>
    <section className="logs-section" aria-labelledby="logsReadFirst">
      <h2 id="logsReadFirst">Read first</h2>
      {toRead.length ? <ul className="logs-cards">{toRead.map(source => <li key={source.id} className={`logs-card ${source.attention}`}>
        <LevelIcon level={source.attention} />
        <div>
          <PageLink href={logSourceURL(source.id)} className="logs-card-title">{source.title}</PageLink>
          {source.headline ? <p className="logs-headline">{source.headline}</p> : null}
          <p className="muted">{attentionLabel(source.attention)}{source.latest_at ? ` · latest ${formatLogTime(source.latest_at)}` : ""}</p>
        </div>
      </li>)}</ul> : <p className="muted">Nothing in the last day needs a look: no page errors, slow reads, crashes or failed background work.</p>}
    </section>
    <section className="logs-section" aria-labelledby="logsAgent">
      <h2 id="logsAgent">Hand it to an agent</h2>
      <p className="muted">The report lists what each marked source recorded recently, with the paths of the full logs on this Mac so an agent can read further.</p>
      <div className="logs-actions"><button type="button" className="button-with-icon" onClick={() => void copyReport()}><Icon name="copy" />Copy report for an agent</button><span className="muted" role="status">{copied}</span></div>
      <p className="muted">Logs folder: <code>{summary.folder}</code></p>
    </section>
    <section className="logs-section" aria-labelledby="logsAll">
      <h2 id="logsAll">All sources</h2>
      <ul className="logs-sources">{logSources.filter(source => source.id !== "overview").map(source => {
        const entry = summary.sources.find(item => item.id === source.id);
        return <li key={source.id}>
          <LevelIcon level={entry?.attention} />
          <div><PageLink href={logSourceURL(source.id)}>{source.label}</PageLink><p className="muted">{entry?.headline ?? source.description}</p></div>
          <span className="muted logs-count">{entry?.count ? `${entry.count.toLocaleString()} recorded` : "Empty"}</span>
        </li>;
      })}</ul>
    </section>
  </>;
}

function Files({ files }: { files?: string[] }) {
  if (!files?.length) return null;
  return <p className="muted logs-files">{files.length === 1 ? "Full log" : "Full logs"}: {files.map(file => <code key={file}>{file}</code>)}</p>;
}

function SourceView({ source, doc }: { source: LogSourceId; doc: SourceDocument }) {
  const summary = doc.summary;
  return <>
    {summary.attention && summary.headline ? <p className={`logs-banner ${summary.attention}`}><LevelIcon level={summary.attention} />{summary.headline}</p> : null}
    <Files files={summary.files} />
    {source === "requests" ? <section className="logs-section" aria-labelledby="logsRunning">
      <h2 id="logsRunning">Answering now</h2>
      {doc.running?.length ? <ul className="logs-events">{doc.running.map(request => <li key={`${request.started_at} ${request.path}`} className="logs-event">
        <LevelIcon level={request.duration_ms >= 30_000 ? "error" : request.duration_ms >= 3_000 ? "warning" : "info"} />
        <div><p className="logs-message"><code>{request.method} {request.path}</code></p><p className="muted">Running for {formatElapsed(request.duration_ms)}</p></div>
      </li>)}</ul> : <p className="muted">No request has been running for more than a second.</p>}
    </section> : null}
    {/* The API omits an empty collection; its source still shows its empty state. */}
    {source === "page" || source === "requests" ? <Events source={source} events={doc.events ?? []} /> : null}
    {source === "service" || source === "app" ? <Runs source={source} runs={doc.runs ?? []} /> : null}
    {source === "background" ? <Operations operations={doc.operations ?? []} /> : null}
    {source === "crashes" ? <Crashes reports={doc.reports ?? []} /> : null}
  </>;
}

function Events({ source, events }: { source: LogSourceId; events: LogEvent[] }) {
  return <section className="logs-section" aria-labelledby="logsEvents">
    <h2 id="logsEvents">Recorded</h2>
    {events.length ? <ul className="logs-events">{events.map(event => {
      const key = `logs:${source}:${event.id}`;
      const facts = [formatLogTime(event.at), event.count && event.count > 1 ? `${event.count} times` : "", event.page ? `on ${event.page}` : "", event.duration_ms ? formatElapsed(event.duration_ms) : ""].filter(Boolean).join(" · ");
      return <li key={key} className="logs-event">
        <LevelIcon level={event.level} />
        <div>
          <p className="logs-message">{event.message}</p>
          <p className="muted">{facts}</p>
          {event.detail ? <ViewDisclosure viewKey={key} summary="Details" className="logs-detail"><pre>{event.detail}</pre></ViewDisclosure> : null}
        </div>
      </li>;
    })}</ul> : <p className="muted">Nothing recorded yet.</p>}
  </section>;
}

function Runs({ source, runs }: { source: LogSourceId; runs: OutputRun[] }) {
  return <section className="logs-section" aria-labelledby="logsRuns">
    <h2 id="logsRuns">Runs</h2>
    {runs.length ? runs.map((run, index) => {
      const state = run.running ? "Running" : run.crashed ? `Crashed · ${run.crash}` : run.exit ? `Ended · ${run.exit}` : "Ended";
      const level: Attention | "info" = run.crashed && !run.running ? "error" : run.problems ? "warning" : "info";
      return <ViewDisclosure key={run.started_at ?? `before-${index}`} viewKey={`logs:${source}:run:${run.started_at ?? "earlier"}`} defaultOpen={index === 0 || run.crashed} className={`logs-run ${level}`}
        summary={<><LevelIcon level={level} /><span>{run.started_at ? `Started ${formatLogTime(run.started_at)}` : "Earlier output"}</span><span className="muted">{state}{run.problems ? ` · ${run.problems} lines mention an error` : ""}</span></>}>
        {run.omitted ? <p className="muted">{run.omitted.toLocaleString()} earlier lines are in the full log.</p> : null}
        <pre className="logs-output">{run.lines.length ? run.lines.join("\n") : "No output."}</pre>
      </ViewDisclosure>;
    }) : <p className="muted">No output recorded yet.</p>}
  </section>;
}

function Operations({ operations }: { operations: Operation[] }) {
  return <section className="logs-section" aria-labelledby="logsOperations">
    <h2 id="logsOperations">Failed and interrupted</h2>
    {operations.length ? <ul className="logs-events">{operations.map(operation => <li key={operation.id} className="logs-event">
      <LevelIcon level={operation.state === "failed" ? "error" : "warning"} />
      <div>
        <p className="logs-message"><strong>{operation.label}</strong> · {operation.message}</p>
        <p className="muted">{[formatLogTime(operation.started_at), operation.state === "failed" ? "Failed" : "Interrupted", operation.step ? `during “${operation.step}”` : ""].filter(Boolean).join(" · ")}</p>
      </div>
    </li>)}</ul> : <p className="muted">No background work failed or was interrupted in the last week.</p>}
    <p className="muted"><PageLink href="/settings/sync">Open Background Processing →</PageLink></p>
  </section>;
}

function Crashes({ reports }: { reports: CrashReport[] }) {
  return <section className="logs-section" aria-labelledby="logsCrashes">
    <h2 id="logsCrashes">Reports</h2>
    {reports.length ? <ul className="logs-events">{reports.map(report => <li key={report.name} className="logs-event">
      <LevelIcon level={report.kind === "crash" ? "error" : "warning"} />
      <div>
        <p className="logs-message"><strong>{report.process}</strong> · {report.kind === "crash" ? "Crash" : "Resource use"}{report.summary ? ` · ${report.summary}` : ""}</p>
        <p className="muted">{formatLogTime(report.at)} · <code>{report.path}</code></p>
        {report.frames?.length ? <pre className="logs-frames">{report.frames.join("\n")}</pre> : null}
        <CrashText name={report.name} />
      </div>
    </li>)}</ul> : <p className="muted">macOS wrote no crash or resource report for Pharos in the last week.</p>}
  </section>;
}

// The full report loads when its disclosure opens, including from a link.
function CrashText({ name }: { name: string }) {
  const key = `logs:crash:${name}`;
  const [open] = useViewEntry("disclosures", key, false);
  const [text, setText] = useState("");
  useEffect(() => {
    if (!open || text) return;
    fetch(`/api/logs/crash?name=${encodeURIComponent(name)}`).then(response => response.text()).then(setText).catch(error => setText(String(error)));
  }, [open, name, text]);
  return <ViewDisclosure viewKey={key} summary="Full report" className="logs-detail"><pre>{text || "Loading…"}</pre></ViewDisclosure>;
}
