import React, { useEffect, useRef, useState } from "react";
import { PageLink, useLocationRevision } from "./nested-pages";
import { localPageNavigation } from "./nested-pages-model";
import { outcomeDisplay } from "./outcomes";
import { useViewEntry, useViewState } from "./view-state";
import { ViewDisclosure } from "./view-disclosure";
import {
  commitState, entryText, fileBreadcrumbs, fileHistoryRoute, filePageURL, fileTreeKey, gitText, keepFilesView, linesText, linkText, notInTreeText, placeText, reworkText,
  sessionCommits, treeAncestors, treeText,
  type FileCommitDetail, type FileHistory, type FileHistoryCommit, type FileHistoryFile, type FileHistoryGit, type FileHistoryLink, type FileHistorySession,
  type FilePageTarget, type FileProject, type FileTree,
} from "./file-history-model";
import "./file-history.css";

const day = (value?: string | null) => value ? new Date(value).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" }) : "";

// Each address's answer is kept a minute, so moving through the tree does
// not ask again for directories already open.
const answers = new Map<string, { at: number; value: Promise<unknown> }>();
function fetchFiles<T>(url: string): Promise<T> {
  const kept = answers.get(url);
  if (kept && Date.now() - kept.at < 60_000) return kept.value as Promise<T>;
  const value = fetch(url).then(async response => {
    const body = await response.json();
    if (!response.ok) throw new Error(body.error ?? `HTTP ${response.status}`);
    return body as T;
  });
  answers.set(url, { at: Date.now(), value });
  value.catch(() => answers.delete(url));
  return value;
}

// useFiles loads one answer; null while it loads.
function useFiles<T>(url: string | null): { value?: T; error?: string } | null {
  const [loaded, setLoaded] = useState<{ url: string; value?: T; error?: string } | null>(null);
  const latest = useRef("");
  useEffect(() => {
    if (!url) return;
    latest.current = url;
    fetchFiles<T>(url).then(value => ({ url, value }), error => ({ url, error: error instanceof Error ? error.message : String(error) }))
      .then(next => { if (latest.current === next.url) setLoaded(next); });
  }, [url]);
  return loaded?.url === url ? loaded : null;
}

// FilesLink moves to another Files page address, keeping the tree as it is.
function FilesLink({ to, children, ...props }: { to: FilePageTarget } & Omit<React.ComponentPropsWithoutRef<"a">, "href">) {
  const href = filePageURL(to);
  return <a {...props} href={href} onClick={event => {
    props.onClick?.(event);
    const local = localPageNavigation(href, location.href, { ...event, target: event.currentTarget.target });
    if (!local || !window.pharosNavigate) return;
    event.preventDefault();
    window.pharosNavigate(keepFilesView(local, location.search));
  }}>{children}</a>;
}

// FileHistoryPage is the Files page: a project's tree beside the project,
// a directory, a file or a commit, each linked to the sessions and commits
// that touched it and the places in their transcripts where they did.
export function FileHistoryPage() {
  useLocationRevision();
  const active = location.pathname === fileHistoryRoute;
  const [path, setPath] = useViewState<string>("file_history_path", "", active);
  const [project] = useViewState<string>("file_history_project", "", active);
  const [dir] = useViewState<string>("file_tree_dir", "", active);
  const [commit] = useViewState<string>("file_commit", "", active);
  const [draft, setDraft] = useState(path);
  useEffect(() => setDraft(path), [path]);
  // A file opened by its path alone names its project once it loads.
  const [found, setFound] = useState<{ path: string; project: string } | null>(null);
  const treeProject = project || (found?.path === path ? found.project : "");
  return <div className="file-history-page">
    <div className="view-heading"><div><h1>Files</h1><p className="muted">Every file sessions edited and, in a Git repository, every file in its tree: who changed it, where in their transcripts, and what became of it.</p></div></div>
    <form className="file-history-form" onSubmit={event => { event.preventDefault(); setPath(draft.trim()); }}>
      <label htmlFor="fileHistoryPath">File</label>
      <input id="fileHistoryPath" type="text" value={draft} spellCheck={false} placeholder="src/main.go, ~/notes/plan.md or an absolute path" onChange={event => setDraft(event.target.value)} />
      <button type="submit" disabled={!draft.trim()}>Show history</button>
    </form>
    {!active ? null : treeProject ? <div className="files-layout">
      <FileTreePanel project={treeProject} selected={commit ? "" : path || dir} />
      <div className="files-pane">
        {commit ? <CommitPane project={treeProject} commit={commit} />
          : path ? <FilePane path={path} project={project} onProject={ref => setFound({ path, project: ref })} />
          : <DirectoryPane project={treeProject} dir={dir} />}
      </div>
    </div>
      : path ? <FilePane path={path} project="" onProject={ref => setFound({ path, project: ref })} />
      : <ProjectsPane />}
  </div>;
}

function Loading() { return <p className="muted" role="status">Loading…</p>; }
function Failure({ error }: { error: string }) { return <p className="file-history-error" role="alert">{error}</p>; }

// Choices answers a project name several projects share.
function Choices({ choices, target }: { choices: FileProject[]; target: Omit<FilePageTarget, "project"> }) {
  return <div className="files-choices"><p>Several projects have this name. Choose one:</p>
    <ul>{choices.map(choice => <li key={choice.ref}><FilesLink to={{ ...target, project: choice.ref }}>{choice.name}</FilesLink> <span className="muted">{choice.repository_id ?? choice.folder}</span></li>)}</ul>
  </div>;
}

function ProjectsPane() {
  const loaded = useFiles<FileTree>("/api/file-tree");
  if (!loaded) return <Loading />;
  if (loaded.error) return <Failure error={loaded.error} />;
  const projects = loaded.value!.projects ?? [];
  return <section className="files-projects" aria-label="Projects">
    <h2>Projects</h2>
    {projects.length ? <ul className="file-history-list">
      {projects.map(project => <li key={project.ref}>
        <FilesLink to={{ project: project.ref }}>{project.name}</FilesLink>
        <span className="muted">{project.kind === "folder" ? "folder" : "repository"} · {project.files?.toLocaleString("en-US")} file{project.files === 1 ? "" : "s"} edited by {project.sessions?.toLocaleString("en-US")} session{project.sessions === 1 ? "" : "s"}</span>
        <span className="file-history-when">{day(project.last_at)}</span>
      </li>)}
    </ul> : <p className="muted">No session has edited a file yet.</p>}
  </section>;
}

// FileTreePanel is the project's tree: directories open to show the
// selected file or directory, and load their entries when opened.
function FileTreePanel({ project, selected }: { project: string; selected: string }) {
  const root = useFiles<FileTree>(`/api/file-tree?${new URLSearchParams({ project })}`);
  const open = new Set(treeAncestors(selected).concat(selected));
  return <nav className="file-tree" aria-label="Project files">
    {!root ? <Loading /> : root.error ? <Failure error={root.error} />
      : root.value!.choices ? <Choices choices={root.value!.choices} target={{}} />
      : <>
        <p className="file-tree-project"><FilesLink to={{ project }} aria-current={selected ? undefined : "page"}>{root.value!.project?.name}</FilesLink></p>
        <p className="muted file-tree-source">{treeText(root.value!.git)}</p>
        <TreeEntries project={project} tree={root.value!} open={open} selected={selected} />
      </>}
  </nav>;
}

function TreeEntries({ project, tree, open, selected }: { project: string; tree: FileTree; open: Set<string>; selected: string }) {
  const git = tree.git?.status === "checked", hasTree = Boolean(tree.git?.tree_commit);
  const entries = tree.entries ?? [];
  if (!entries.length) return <p className="muted">Nothing here.</p>;
  return <ul className="file-tree-list">
    {entries.map(entry => {
      const counts = entryText(entry, git);
      const gone = hasTree && !entry.in_tree;
      const label = <span className={`file-tree-entry${gone ? " file-tree-gone" : ""}`} title={gone ? notInTreeText(tree.git) : undefined}>
        <FilesLink to={entry.kind === "dir" ? { project, dir: entry.path } : { project, file: entry.path }} aria-current={entry.path === selected ? "page" : undefined}>{entry.name}</FilesLink>
        {counts ? <span className="file-tree-counts">{counts}</span> : null}
      </span>;
      return <li key={entry.path}>{entry.kind === "dir"
        ? <TreeDirectory project={project} path={entry.path} label={label} open={open} selected={selected} />
        : label}</li>;
    })}
  </ul>;
}

// TreeDirectory loads its entries only once open.
function TreeDirectory({ project, path, label, open, selected }: { project: string; path: string; label: React.ReactNode; open: Set<string>; selected: string }) {
  const viewKey = fileTreeKey(project, path);
  const [isOpen] = useViewEntry("disclosures", viewKey, open.has(path));
  return <ViewDisclosure className="file-tree-dir" viewKey={viewKey} summary={label} defaultOpen={open.has(path)}>
    {isOpen ? <TreeLevel project={project} dir={path} open={open} selected={selected} /> : null}
  </ViewDisclosure>;
}

function TreeLevel({ project, dir, open, selected }: { project: string; dir: string; open: Set<string>; selected: string }) {
  const loaded = useFiles<FileTree>(`/api/file-tree?${new URLSearchParams({ project, dir })}`);
  if (!loaded) return <Loading />;
  if (loaded.error) return <Failure error={loaded.error} />;
  return <TreeEntries project={project} tree={loaded.value!} open={open} selected={selected} />;
}

// Breadcrumbs lead from the project's root to a file or directory.
function Breadcrumbs({ project, name, path }: { project: string; name: string; path: string }) {
  return <nav className="files-breadcrumbs" aria-label="Path">
    <FilesLink to={{ project }}>{name}</FilesLink>
    {fileBreadcrumbs(path).map(crumb => <React.Fragment key={crumb.name + (crumb.dir ?? "")}>
      <span aria-hidden="true"> / </span>
      {crumb.dir === null ? <span aria-current="page">{crumb.name}</span> : <FilesLink to={{ project, dir: crumb.dir }}>{crumb.name}</FilesLink>}
    </React.Fragment>)}
  </nav>;
}

function Outcome({ label, why }: { label?: string; why?: string }) {
  const outcome = outcomeDisplay(label);
  return <span className="file-history-outcome" title={outcome.description}>{outcome.label}{why ? <span className="muted"> · {why}</span> : null}</span>;
}

// SessionLinks are the sessions linked to a commit, each opening where it
// printed or made the commit, else the session.
function SessionLinks({ links }: { links: FileHistoryLink[] }) {
  if (!links.length) return <span className="muted">No linked session</span>;
  return <span>{links.map((link, index) => <React.Fragment key={link.workspace_id}>{index ? ", " : ""}
    <PageLink href={link.url ?? `/work/${encodeURIComponent(link.workspace_id)}`} title={linkText(link)}>{link.title || (link.counted ? "Session" : "Candidate session")}</PageLink>
    {link.counted ? null : <span className="muted"> (candidate)</span>}
  </React.Fragment>)}</span>;
}

function CommitRow({ project, commit }: { project: string; commit: FileHistoryCommit & { files?: number } }) {
  const state = commitState(commit);
  return <li>
    <span className="file-history-when">{day(commit.author_at)}</span>
    <FilesLink to={{ project, commit: commit.hash }} title={commit.hash}><code>{commit.hash.slice(0, 7)}</code></FilesLink>
    <span className="muted">{commit.files ? `${commit.files.toLocaleString("en-US")} file${commit.files === 1 ? "" : "s"} · ` : ""}{linesText(commit.lines_added, commit.lines_removed)}{state ? ` · ${state}` : ""}</span>
    <SessionLinks links={commit.sessions} />
  </li>;
}

function FilePane({ path, project, onProject }: { path: string; project: string; onProject: (ref: string) => void }) {
  const loaded = useFiles<FileHistory>(`/api/file-history?${new URLSearchParams({ path, ...project ? { project } : {} })}`);
  const files = loaded?.value?.files;
  const only = files?.length === 1 ? files[0].project_ref : "";
  useEffect(() => { if (only && !project) onProject(only); }, [only, project]);
  if (!loaded) return <Loading />;
  if (loaded.error) return <Failure error={loaded.error} />;
  return <>
    {files!.length ? null : <p className="muted">{loaded.value!.note}</p>}
    {files!.map(file => <FileTimeline key={file.file_key} file={file} several={files!.length > 1} />)}
  </>;
}

function FileTimeline({ file, several }: { file: FileHistoryFile; several: boolean }) {
  const ref = file.project_ref ?? "", relative = file.relative_path ?? file.path;
  const commits = file.commits.length;
  return <section className="file-history-file" aria-label={file.path}>
    {ref ? <Breadcrumbs project={ref} name={file.project ?? ref} path={relative} /> : null}
    <h2><code>{file.path}</code>{several && file.project ? <span className="muted"> · <FilesLink to={{ project: ref, file: relative }}>{file.project}</FilesLink></span> : null}</h2>
    <p className="muted">
      {file.sessions.length ? `Edited by ${file.sessions.length.toLocaleString("en-US")}${file.sessions_truncated ? "+" : ""} session${file.sessions.length === 1 ? "" : "s"}` : "No session edited this file through its tool calls"}
      {file.git.status === "checked" ? ` · ${commits.toLocaleString("en-US")}${file.git.commits_truncated ? "+" : ""} commit${commits === 1 ? "" : "s"}` : ""}
      {file.in_tree === false && file.git.tree_commit ? ` · ${notInTreeText(file.git)}` : ""}
    </p>
    <h3>Sessions</h3>
    {file.sessions.length ? <ol className="file-history-list file-sessions">
      {file.sessions.map(session => <SessionEntry key={session.workspace_id} file={file} session={session} />)}
    </ol> : <p className="muted">Sessions that changed it another way (a script, a generator) are linked to its commits below, where local Git found them.</p>}
    {file.sessions_truncated ? <p className="muted">Older sessions are not shown.</p> : null}
    <h3>Commits</h3>
    <p className="muted">{gitText(file.git, commits)}</p>
    {commits ? <ol className="file-history-list">{file.commits.map(commit => <CommitRow key={commit.hash} project={ref} commit={commit} />)}</ol> : null}
    {file.git.commits_truncated ? <p className="muted">Older commits are not shown.</p> : null}
  </section>;
}

function SessionEntry({ file, session }: { file: FileHistoryFile; session: FileHistorySession }) {
  const committed = sessionCommits(file, session.workspace_id), places = session.places ?? [];
  return <li className="file-session" id={`session-${session.workspace_id}`}>
    <div className="file-session-head">
      <span className="file-history-when">{day(session.last_edit_at)}</span>
      <PageLink href={session.url} title="Open the session at its first edit of this file">{session.title || session.workspace_id}</PageLink>
      <span className="muted">{session.edits?.toLocaleString("en-US")} edit{session.edits === 1 ? "" : "s"} · {linesText(session.lines_added, session.lines_removed)}{session.rewrote ? " · rewrote the file" : ""}</span>
    </div>
    <Outcome label={session.outcome_label} why={session.outcome_why} />
    {places.length ? <div className="file-places" aria-label="Edits in the transcript">
      {places.map((place, index) => <PageLink key={index} className="file-place" href={place.url} title={`Open this edit in the transcript${place.path ? `: ${place.path}` : ""}`}>{placeText(place, index)}</PageLink>)}
      {session.places_truncated ? <PageLink className="file-place" href={`/work/${encodeURIComponent(session.workspace_id)}`}>More edits in the session…</PageLink> : null}
    </div> : null}
    {session.reworked_by?.length ? <p className="file-rework">Later sessions removed its lines: {session.reworked_by.map((other, index) => <React.Fragment key={other.workspace_id}>{index ? "; " : ""}
      <PageLink href={other.url}>{other.title || other.workspace_id}</PageLink> {reworkText(other, "by")}</React.Fragment>)}</p> : null}
    {session.reworked?.length ? <p className="file-rework">It removed lines earlier sessions added: {session.reworked.map((other, index) => <React.Fragment key={other.workspace_id}>{index ? "; " : ""}
      <PageLink href={other.url}>{other.title || other.workspace_id}</PageLink> {reworkText(other, "of")}</React.Fragment>)}</p> : null}
    {committed.length ? <p className="muted">Committed in {committed.map(({ commit, link }, index) => <React.Fragment key={commit.hash}>{index ? ", " : ""}
      <FilesLink to={{ project: file.project_ref, commit: commit.hash }} title={linkText(link)}><code>{commit.hash.slice(0, 7)}</code></FilesLink>{link.counted ? "" : " (candidate)"}</React.Fragment>)}</p> : null}
  </li>;
}

function DirectoryPane({ project, dir }: { project: string; dir: string }) {
  const loaded = useFiles<FileTree>(`/api/file-tree?${new URLSearchParams({ project, dir, activity: "1" })}`);
  if (!loaded) return <Loading />;
  if (loaded.error) return <Failure error={loaded.error} />;
  const tree = loaded.value!;
  if (tree.choices) return <Choices choices={tree.choices} target={{ dir }} />;
  const name = tree.project?.name ?? project, git = tree.git?.status === "checked";
  return <section className="files-directory" aria-label={dir || name}>
    {dir ? <Breadcrumbs project={project} name={name} path={dir} /> : null}
    <h2>{dir ? <code>{dir}/</code> : name}</h2>
    <p className="muted">{dir ? `${(tree.entries ?? []).length.toLocaleString("en-US")} entries.` : treeText(tree.git)}</p>
    <DirectoryGit git={tree.git} />
    <h3>Latest sessions here</h3>
    {tree.sessions?.length ? <ol className="file-history-list">{tree.sessions.map(session => <li key={session.workspace_id}>
      <span className="file-history-when">{day(session.last_edit_at)}</span>
      <PageLink href={session.url}>{session.title || session.workspace_id}</PageLink>
      <span className="muted">{session.files.toLocaleString("en-US")} file{session.files === 1 ? "" : "s"} · {session.edits.toLocaleString("en-US")} edit{session.edits === 1 ? "" : "s"}</span>
      <Outcome label={session.outcome_label} />
    </li>)}</ol> : <p className="muted">No session edited a file here.</p>}
    {git ? <>
      <h3>Latest commits here</h3>
      {tree.commits?.length ? <ol className="file-history-list">{tree.commits.map(commit => <CommitRow key={commit.hash} project={project} commit={commit} />)}</ol>
        : <p className="muted">Local Git found no commit here{tree.git?.commits_since ? ` since ${tree.git.commits_since.slice(0, 10)}` : ""}.</p>}
    </> : null}
  </section>;
}

function DirectoryGit({ git }: { git?: FileHistoryGit }) {
  if (!git || git.status === "not_applicable" || git.status === "checked") return null;
  return <p className="muted">{git.reason || "Local Git has not checked this repository."}</p>;
}

function CommitPane({ project, commit }: { project: string; commit: string }) {
  const loaded = useFiles<FileCommitDetail>(`/api/file-commit?${new URLSearchParams({ project, commit })}`);
  if (!loaded) return <Loading />;
  if (loaded.error) return <Failure error={loaded.error} />;
  const value = loaded.value!;
  if (value.choices) return <Choices choices={value.choices} target={{ commit }} />;
  const detail = value.commit, name = value.project?.name ?? project;
  return <section className="files-commit" aria-label={`Commit ${commit}`}>
    <nav className="files-breadcrumbs" aria-label="Path"><FilesLink to={{ project }}>{name}</FilesLink></nav>
    <h2>Commit <code>{(detail?.hash ?? commit).slice(0, 12)}</code></h2>
    {!detail ? <p className="muted">{value.note}</p> : <>
      <p className="muted">Authored {day(detail.author_at)}{detail.committed_at && day(detail.committed_at) !== day(detail.author_at) ? `, committed ${day(detail.committed_at)}` : ""}
        {" · "}{linesText(detail.lines_added, detail.lines_removed)}{(detail.parents ?? 1) > 1 ? ` · a merge of ${detail.parents} parents` : ""}</p>
      <p>{detail.reverted_by ? <>Reverted by <FilesLink to={{ project, commit: detail.reverted_by }}><code>{detail.reverted_by.slice(0, 7)}</code></FilesLink>{detail.revert_evidence ? <span className="muted"> ({detail.revert_evidence})</span> : null}</>
        : commitState(detail) || "Mainline unknown"}{detail.mainline_evidence ? <span className="muted"> · {detail.mainline_evidence}</span> : null}</p>
      <h3>Sessions</h3>
      {detail.sessions.length ? <ol className="file-history-list">{detail.sessions.map(link => <li key={link.workspace_id}>
        <PageLink href={link.url ?? `/work/${encodeURIComponent(link.workspace_id)}`}>{link.title || link.workspace_id}</PageLink>
        <span className="muted">{linkText(link)} · confidence {link.confidence.toFixed(2)}</span>
      </li>)}</ol> : <p className="muted">No session is linked to this commit: it was made outside the sessions this library holds, or they left no trace of it.</p>}
      <h3>Files</h3>
      <ol className="file-history-list">{detail.files.map(file => <li key={file.path}>
        <FilesLink to={{ project, file: file.path }}><code>{file.path}</code></FilesLink>
        <span className="muted">{linesText(file.lines_added, file.lines_removed)}{file.sessions ? ` · edited by ${file.sessions} session${file.sessions === 1 ? "" : "s"}` : ""}{file.in_tree ? "" : ` · ${notInTreeText(value.git)}`}</span>
      </li>)}</ol>
    </>}
  </section>;
}
