import React, { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import { PageLink } from "./nested-pages";
import { outcomeDisplay } from "./outcomes";
import { commitState, linesText, linkText, placeText, reworkText, filePageURL, type FileHistoryLink, type FilePlace, type FileRework } from "./file-history-model";
import "./file-history.css";

// A session's files and commits (/api/work/{id}/files), on its Work page:
// from the session down to each file it edited, each edit's call in its
// transcript, the file's page with its whole history, the rework between it
// and other sessions, and its commits with where it made them.

export type WorkFile = {
  file_key: string; path: string; project?: string; project_ref?: string; relative_path?: string; url: string;
  first_edit_at?: string; last_edit_at?: string; edits: number; lines_added?: number | null; lines_removed?: number | null; rewrote?: boolean; other_sessions: number;
  places: FilePlace[]; places_truncated?: boolean; reworked_by: FileRework[]; reworked: FileRework[];
};
export type WorkCommit = FileHistoryLink & {
  hash: string; repository_id: string; project: string; project_ref: string; author_at?: string | null; files?: number | null;
  lines_added?: number | null; lines_removed?: number | null; on_mainline?: boolean | null; reverted_by?: string | null; url: string; place_url?: string;
};
export type WorkFiles = {
  workspace_id: string; outcome_label?: string; outcome_why?: string; git: { status?: string; reason?: string | null };
  files: WorkFile[]; files_truncated?: boolean; commits: WorkCommit[];
};

// workFilesSummary is the section's one-line account of what it holds.
export function workFilesSummary(value: Pick<WorkFiles, "files" | "commits" | "files_truncated">): string {
  const files = value.files.length, commits = value.commits.filter(commit => commit.counted).length;
  const parts = [`${files.toLocaleString("en-US")}${value.files_truncated ? "+" : ""} file${files === 1 ? "" : "s"} edited`];
  if (value.commits.length) parts.push(`${commits.toLocaleString("en-US")} linked commit${commits === 1 ? "" : "s"}${value.commits.length > commits ? ` and ${value.commits.length - commits} candidate${value.commits.length - commits === 1 ? "" : "s"}` : ""}`);
  return parts.join(" · ");
}

export function WorkFilesSection({ workspace }: { workspace: string }) {
  const [loaded, setLoaded] = useState<{ value?: WorkFiles; error?: string } | null>(null);
  useEffect(() => {
    let live = true;
    fetch(`/api/work/${encodeURIComponent(workspace)}/files`).then(async response => {
      const body = await response.json();
      if (!response.ok) throw new Error(body.error ?? `HTTP ${response.status}`);
      return { value: body as WorkFiles };
    }).catch(error => ({ error: error instanceof Error ? error.message : String(error) })).then(next => { if (live) setLoaded(next); });
    return () => { live = false; };
  }, [workspace]);
  if (!loaded) return <p className="muted" role="status">Loading…</p>;
  if (loaded.error) return <p className="file-history-error" role="alert">{loaded.error}</p>;
  const value = loaded.value!, outcome = outcomeDisplay(value.outcome_label), project = value.files.find(file => file.project_ref)?.project_ref ?? value.commits[0]?.project_ref;
  return <div className="work-files">
    <p><strong title={outcome.description}>{outcome.label}</strong>{value.outcome_why ? <span className="muted"> · {value.outcome_why}</span> : null}</p>
    <p className="muted">{workFilesSummary(value)}. Each edit opens its call in the transcript; each file opens its page, with every session that edited it and its commits.</p>
    {value.files.length ? <ol className="file-history-list">{value.files.map(file => <li key={file.file_key} className="file-session">
      <div className="file-session-head">
        <PageLink href={file.url} title="Every session that edited this file, and its commits"><code>{file.relative_path ?? file.path}</code></PageLink>
        <span className="muted">{file.project ? `${file.project} · ` : ""}{file.edits.toLocaleString("en-US")} edit{file.edits === 1 ? "" : "s"} · {linesText(file.lines_added, file.lines_removed)}{file.rewrote ? " · rewrote the file" : ""}</span>
        {file.other_sessions ? <PageLink href={file.url} className="muted">{file.other_sessions.toLocaleString("en-US")} other session{file.other_sessions === 1 ? "" : "s"}</PageLink> : null}
      </div>
      <div className="file-places" aria-label={`Edits of ${file.path}`}>
        {file.places.map((place, index) => <PageLink key={index} className="file-place" href={place.url} title={`Open this edit in the transcript${place.path ? `: ${place.path}` : ""}`}>{placeText(place, index)}</PageLink>)}
        {file.places_truncated ? <PageLink className="file-place" href={file.url}>More on the file's page…</PageLink> : null}
      </div>
      {file.reworked_by.length ? <p className="file-rework">Later sessions removed its lines: {file.reworked_by.map((other, index) => <React.Fragment key={other.workspace_id}>{index ? "; " : ""}
        <PageLink href={other.url}>{other.title || other.workspace_id}</PageLink> {reworkText(other, "by")}</React.Fragment>)}</p> : null}
      {file.reworked.length ? <p className="file-rework">It removed lines earlier sessions added: {file.reworked.map((other, index) => <React.Fragment key={other.workspace_id}>{index ? "; " : ""}
        <PageLink href={other.url}>{other.title || other.workspace_id}</PageLink> {reworkText(other, "of")}</React.Fragment>)}</p> : null}
    </li>)}</ol> : <p className="muted">It edited no file through its tool calls.</p>}
    {value.files_truncated ? <p className="muted">More files are not shown.</p> : null}
    <h3>Commits</h3>
    {value.commits.length ? <ol className="file-history-list">{value.commits.map(commit => {
      const state = commitState({ hash: commit.hash, on_mainline: commit.on_mainline, reverted_by: commit.reverted_by, sessions: [] });
      return <li key={commit.repository_id + commit.hash}>
        <PageLink href={commit.url} title={commit.hash}><code>{commit.hash.slice(0, 7)}</code></PageLink>
        <span className="muted">{commit.project}{commit.author_at ? ` · ${new Date(commit.author_at).toLocaleDateString()}` : ""}{commit.files ? ` · ${commit.files} file${commit.files === 1 ? "" : "s"}` : ""}{commit.lines_added != null ? ` · ${linesText(commit.lines_added, commit.lines_removed)}` : ""}{state ? ` · ${state}` : ""}</span>
        <span>{linkText(commit)}</span>
        {commit.place_url ? <PageLink href={commit.place_url}>Where in the transcript</PageLink> : null}
      </li>;
    })}</ol> : <p className="muted">{value.git.status === "checked" ? "Local Git linked no commit to this session." : value.git.reason || (value.git.status === "not_applicable" ? "Not in a Git repository: no commits to link." : "Local Git has not checked this session.")}</p>}
    {project ? <p className="muted"><PageLink href={filePageURL({ project })}>Browse the project's files</PageLink></p> : null}
  </div>;
}

declare global {
  interface Window {
    pharosWorkFiles?: { mount: (host: HTMLElement, props: { workspace: string }) => { destroy: () => void } };
  }
}

// installWorkFiles lets the Work page (ui.py) mount the section.
export function installWorkFiles() {
  window.pharosWorkFiles = {
    mount(host, { workspace }) {
      const root = createRoot(host);
      root.render(<WorkFilesSection workspace={workspace} />);
      return { destroy: () => root.unmount() };
    },
  };
}
