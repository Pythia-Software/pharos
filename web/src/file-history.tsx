import React, { useEffect, useRef, useState } from "react";
import { PageLink, useLocationRevision } from "./nested-pages";
import { outcomeDisplay } from "./outcomes";
import { useViewState } from "./view-state";
import { commitState, fileHistoryRoute, fileHistoryURL, gitText, linesText, linkText, type FileHistory, type FileHistoryFile } from "./file-history-model";
import "./file-history.css";

const day = (value?: string) => value ? new Date(value).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" }) : "";

// FileHistoryPage answers "why is this file like this?": each session that
// edited a file, newest first, and the commits local Git found for it.
export function FileHistoryPage() {
  useLocationRevision();
  const active = location.pathname === fileHistoryRoute;
  const [path, setPath] = useViewState<string>("file_history_path", "", active);
  const [project] = useViewState<string>("file_history_project", "", active);
  const [draft, setDraft] = useState(path);
  useEffect(() => setDraft(path), [path]);
  const [loaded, setLoaded] = useState<{ key: string; value?: FileHistory; error?: string } | null>(null);
  const latest = useRef("");
  const key = JSON.stringify([path, project]);
  useEffect(() => {
    if (!active || !path) return;
    latest.current = key;
    const params = new URLSearchParams({ path, ...project ? { project } : {} });
    fetch(`/api/file-history?${params}`).then(async response => {
      const value = await response.json();
      if (!response.ok) throw new Error(value.error ?? `HTTP ${response.status}`);
      return { key, value: value as FileHistory };
    }).catch(error => ({ key, error: error instanceof Error ? error.message : String(error) }))
      .then(next => { if (latest.current === next.key) setLoaded(next); });
  }, [active, key]);
  const current = loaded?.key === key ? loaded : null;
  return <div className="file-history-page">
    <div className="view-heading"><div><h1>File history</h1><p className="muted">Every session that edited a file, what it did and what became of it. In a Git repository, the commits that changed it too.</p></div></div>
    <form className="file-history-form" onSubmit={event => { event.preventDefault(); setPath(draft.trim()); }}>
      <label htmlFor="fileHistoryPath">File</label>
      <input id="fileHistoryPath" type="text" value={draft} spellCheck={false} placeholder="src/main.go, ~/notes/plan.md or an absolute path" onChange={event => setDraft(event.target.value)} />
      <button type="submit" disabled={!draft.trim()}>Show history</button>
    </form>
    {project ? <p className="muted">In {project} · <PageLink href={fileHistoryURL(path)}>Every project</PageLink></p> : null}
    {!path ? <p className="muted">Enter a file's path, or open a file from Search's file search.</p>
      : !current ? <p className="muted" role="status">Loading…</p>
      : current.error ? <p className="file-history-error" role="alert">{current.error}</p>
      : <>
        {current.value!.files.length ? null : <p className="muted">{current.value!.note}</p>}
        {current.value!.files.map(file => <FileTimeline key={file.file_key} file={file} />)}
      </>}
  </div>;
}

function FileTimeline({ file }: { file: FileHistoryFile }) {
  return <section className="file-history-file" aria-label={file.path}>
    <h2><code>{file.path}</code>{file.project ? <span className="muted"> · {file.project}</span> : null}</h2>
    <h3>Sessions</h3>
    <ol className="file-history-list">
      {file.sessions.map(session => {
        const outcome = outcomeDisplay(session.outcome_label);
        return <li key={session.workspace_id}>
          <span className="file-history-when">{day(session.last_edit_at)}</span>
          <PageLink href={session.url}>{session.title || session.workspace_id}</PageLink>
          <span className="muted">{session.edits?.toLocaleString("en-US")} edit{session.edits === 1 ? "" : "s"} · {linesText(session.lines_added, session.lines_removed)}{session.rewrote ? " · rewrote the file" : ""}</span>
          <span className="file-history-outcome" title={session.outcome_why || outcome.description}>{outcome.label}</span>
        </li>;
      })}
    </ol>
    {file.sessions_truncated ? <p className="muted">Older sessions are not shown.</p> : null}
    <h3>Commits</h3>
    <p className="muted">{gitText(file.git, file.commits.length)}</p>
    {file.commits.length ? <ol className="file-history-list">
      {file.commits.map(commit => <li key={commit.hash}>
        <span className="file-history-when">{day(commit.author_at)}</span>
        <code title={commit.hash}>{commit.hash.slice(0, 7)}</code>
        <span className="muted">{linesText(commit.lines_added, commit.lines_removed)}{commitState(commit) ? ` · ${commitState(commit)}` : ""}</span>
        <span>{commit.sessions.length ? commit.sessions.map((link, index) => <React.Fragment key={link.workspace_id}>{index ? ", " : ""}<PageLink href={`/work/${encodeURIComponent(link.workspace_id)}`} title={linkText(link)}>{link.counted ? "Session" : "Candidate session"}</PageLink></React.Fragment>)
          : <span className="muted">No linked session</span>}</span>
      </li>)}
    </ol> : null}
    {file.git.commits_truncated ? <p className="muted">Older commits are not shown.</p> : null}
  </section>;
}
