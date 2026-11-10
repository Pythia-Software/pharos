import React, { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { PageLink, useLocationRevision } from "./nested-pages";
import { localPageNavigation } from "./nested-pages-model";
import { outcomeDisplay } from "./outcomes";
import { useViewEntry, useViewState } from "./view-state";
import { ViewDisclosure } from "./view-disclosure";
import {
  age, blameRows, changeText, checkoutText, commitState, commitsByDay, directoryTab, directoryTabs, directoryViewLabels, fileBreadcrumbs, fileHistoryNotice, fileHistoryRoute,
  filePageURL, fileTab, fileTabs, fileTreeKey, fileViewLabels, keepFilesView, linesText, linkText, notInTreeText, parentDir, parseUnifiedDiff, placeText,
  reworkText, sessionCommits, treeAncestors, treeText,
  type CommitGit, type DiffLine, type FileBlame, type FileChanges, type FileCommitDetail, type FileContent, type FileDiff, type FileFind,
  type FileHistory, type FileHistoryCommit, type FileLastCommits, type FileHistoryFile, type FileHistoryGit, type FileHistoryLink, type FileHistorySession, type FileLog,
  type FileLogCommit, type FilePageTarget, type FileProject, type FileTree, type FileTreeEntry, type RepositoryCheckout,
} from "./file-history-model";
import "./file-history.css";

const day = (value?: string | null) => value ? new Date(value).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" }) : "";
const plural = (count: number, word: string) => `${count.toLocaleString("en-US")} ${word}${count === 1 ? "" : "s"}`;

// Each address's answer is kept a minute, so moving through the tree does
// not ask again for directories already open. Choosing a checkout forgets
// them all, since Git's answers change with it.
const answers = new Map<string, { at: number; value: Promise<unknown> }>();
let revision = 0;
const revisionListeners = new Set<() => void>();
function refreshFiles() {
  answers.clear();
  revision++;
  revisionListeners.forEach(listener => listener());
}
function useFilesRevision(): number {
  return useSyncExternalStore(listener => { revisionListeners.add(listener); return () => revisionListeners.delete(listener); }, () => revision);
}
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
  const current = useFilesRevision();
  const key = url ? `${current} ${url}` : "";
  const [loaded, setLoaded] = useState<{ key: string; value?: T; error?: string } | null>(null);
  const latest = useRef("");
  useEffect(() => {
    if (!url) return;
    latest.current = key;
    fetchFiles<T>(url).then(value => ({ key, value }), error => ({ key, error: error instanceof Error ? error.message : String(error) }))
      .then(next => { if (latest.current === next.key) setLoaded(next); });
  }, [key]);
  return loaded?.key === key ? loaded : null;
}

const query = (params: Record<string, string | number | undefined>) =>
  new URLSearchParams(Object.entries(params).filter(([, value]) => value !== undefined && value !== "").map(([name, value]) => [name, String(value)]));

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

// FileHistoryPage is the Files page: a project's tree in a sidebar beside a
// directory, a file or a commit. A file has tabs for its code, blame and
// history, which Git reads from the repository's chosen checkout, the diffs
// its sessions' edits made and the sessions themselves.
export function FileHistoryPage() {
  useLocationRevision();
  const active = location.pathname === fileHistoryRoute;
  const [path, setPath] = useViewState<string>("file_history_path", "", active);
  const [project] = useViewState<string>("file_history_project", "", active);
  const [dir] = useViewState<string>("file_tree_dir", "", active);
  const [commit] = useViewState<string>("file_commit", "", active);
  // Which tab opens depends on the file, so no default is written.
  const [fileView] = useViewState<string>("file_view", "", false);
  const [dirView] = useViewState<string>("file_dir_view", "", false);
  const [collapsed, setCollapsed] = useViewState<boolean>("files_tree_collapsed", false, active);
  // A file opened by its path alone names its project once it loads.
  const [found, setFound] = useState<{ path: string; project: string } | null>(null);
  const treeProject = project || (found?.path === path ? found.project : "");
  if (!active) return null;
  if (!treeProject) return <div className="file-history-page">
    <div className="view-heading"><div><h1>Files</h1><p className="muted">Every file sessions edited and, in a Git repository, every file in its tree: its code, blame and history, who changed it, and where in their transcripts.</p></div></div>
    <PathForm path={path} onPath={setPath} />
    {path ? <FilePane path={path} project="" view={fileView} onProject={ref => setFound({ path, project: ref })} /> : <ProjectsPane />}
  </div>;
  return <div className={`file-history-page files-with-tree${collapsed ? " files-tree-collapsed" : ""}`}>
    {collapsed ? null : <FilesSidebar project={treeProject} selected={commit ? path : path || dir} onCollapse={() => setCollapsed(true)} />}
    <div className="files-main">
      {collapsed ? <button type="button" className="files-tree-show" data-view-field="files_tree_collapsed" onClick={() => setCollapsed(false)} aria-label="Show the file tree"><SidebarIcon /> Files</button> : null}
      {commit ? <CommitPane project={treeProject} commit={commit} focus={path} />
        : path ? <FilePane path={path} project={project} view={fileView} onProject={ref => setFound({ path, project: ref })} />
        : <DirectoryPane project={treeProject} dir={dir} view={dirView} />}
    </div>
  </div>;
}

function PathForm({ path, onPath }: { path: string; onPath: (path: string) => void }) {
  const [draft, setDraft] = useState(path);
  useEffect(() => setDraft(path), [path]);
  return <form className="file-history-form" onSubmit={event => { event.preventDefault(); onPath(draft.trim()); }}>
    <label htmlFor="fileHistoryPath">File</label>
    <input id="fileHistoryPath" type="text" value={draft} spellCheck={false} placeholder="src/main.go, ~/notes/plan.md or an absolute path" onChange={event => setDraft(event.target.value)} />
    <button type="submit" disabled={!draft.trim()}>Show file</button>
  </form>;
}

function Loading() { return <p className="muted" role="status">Loading…</p>; }
function Failure({ error }: { error: string }) { return <p className="file-history-error" role="alert">{error}</p>; }

function FolderIcon() { return <svg className="files-icon files-icon-dir" viewBox="0 0 16 16" aria-hidden="true"><path d="M1.5 3.5h4.2l1.4 1.6h7.4v8.4h-13z" /></svg>; }
function FileIcon() { return <svg className="files-icon" viewBox="0 0 16 16" aria-hidden="true"><path d="M3.5 1.5h6l3 3v10h-9z M9.5 1.5v3h3" /></svg>; }
function SidebarIcon() { return <svg className="files-icon" viewBox="0 0 16 16" aria-hidden="true"><path d="M1.5 2.5h13v11h-13z M6 2.5v11" /></svg>; }

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
        {project.kind === "folder" ? <FolderIcon /> : <SidebarIcon />}
        <FilesLink to={{ project: project.ref }}>{project.name}</FilesLink>
        <span className="muted">{project.kind === "folder" ? "folder" : "repository"} · {plural(project.files ?? 0, "file")} edited by {plural(project.sessions ?? 0, "session")}</span>
        <span className="file-history-when">{day(project.last_at)}</span>
      </li>)}
    </ul> : <p className="muted">No session has edited a file yet.</p>}
  </section>;
}

// useProjectTree is a project's root: its name, its Git state and, for a
// repository, the checkout its Git views read.
function useProjectTree(project: string) {
  const root = useFiles<FileTree>(`/api/file-tree?${query({ project })}`);
  const repository = root?.value?.project?.repository_id;
  const checkout = useFiles<RepositoryCheckout>(repository ? `/api/repositories/${encodeURIComponent(repository)}/checkout` : null);
  return { root, repository, checkout: checkout?.value, git: root?.value?.git };
}

// FilesSidebar is the project's tree, with Go to file: directories open to
// show the selected file or directory, and load their entries when opened.
function FilesSidebar({ project, selected, onCollapse }: { project: string; selected: string; onCollapse: () => void }) {
  const { root, git } = useProjectTree(project);
  const open = new Set(treeAncestors(selected).concat(selected));
  const [find, setFind] = useState("");
  const tree = root?.value;
  return <nav className="files-sidebar" aria-label="Project files">
    <div className="files-sidebar-head">
      {tree?.project ? <FilesLink className="files-sidebar-project" to={{ project }} aria-current={selected ? undefined : "page"}>{tree.project.name}</FilesLink> : <span />}
      <button type="button" className="files-icon-button" data-view-field="files_tree_collapsed" onClick={onCollapse} aria-label="Hide the file tree" title="Hide the file tree"><SidebarIcon /></button>
    </div>
    {git?.tree_commit ? <p className="files-sidebar-ref" title={treeText(git)}><RefChip git={git} /></p> : <p className="muted files-sidebar-ref">{tree ? treeText(git) : ""}</p>}
    <input className="files-find" type="search" value={find} placeholder="Go to file" aria-label="Go to file" spellCheck={false}
      onChange={event => setFind(event.target.value)} onKeyDown={event => { if (event.key === "Escape") setFind(""); }} />
    {find.trim() ? <FindResults project={project} text={find} onChosen={() => setFind("")} />
      : !root ? <Loading /> : root.error ? <Failure error={root.error} />
      : tree!.choices ? <Choices choices={tree!.choices} target={{}} />
      : <TreeEntries project={project} tree={tree!} open={open} selected={selected} />}
  </nav>;
}

function FindResults({ project, text, onChosen }: { project: string; text: string; onChosen: () => void }) {
  const [asked, setAsked] = useState(text);
  useEffect(() => { const timer = setTimeout(() => setAsked(text), 150); return () => clearTimeout(timer); }, [text]);
  const loaded = useFiles<FileFind>(`/api/file-find?${query({ project, q: asked.trim() })}`);
  if (!loaded) return <Loading />;
  if (loaded.error) return <Failure error={loaded.error} />;
  const files = loaded.value!.files;
  if (!files.length) return <p className="muted">No file matches.</p>;
  return <ul className="files-find-results">
    {files.map(file => <li key={file.path}><FilesLink to={{ project, file: file.path }} onClick={onChosen} className={file.in_tree ? undefined : "file-tree-gone"}>
      <FileIcon /><span>{file.path}</span>
    </FilesLink></li>)}
    {loaded.value!.more ? <li className="muted">More files match; type more of the path.</li> : null}
  </ul>;
}

function TreeEntries({ project, tree, open, selected }: { project: string; tree: FileTree; open: Set<string>; selected: string }) {
  const hasTree = Boolean(tree.git?.tree_commit);
  const entries = tree.entries ?? [];
  if (!entries.length) return <p className="muted">Nothing here.</p>;
  return <ul className="file-tree-list">
    {entries.map(entry => {
      const gone = hasTree && !entry.in_tree;
      const label = <FilesLink className={`file-tree-entry${gone ? " file-tree-gone" : ""}`} title={gone ? notInTreeText(tree.git) : entry.path}
        to={entry.kind === "dir" ? { project, dir: entry.path } : { project, file: entry.path }} aria-current={entry.path === selected ? "page" : undefined}>
        {entry.kind === "dir" ? <FolderIcon /> : <FileIcon />}<span>{entry.name}</span>
      </FilesLink>;
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
  const loaded = useFiles<FileTree>(`/api/file-tree?${query({ project, dir })}`);
  if (!loaded) return <Loading />;
  if (loaded.error) return <Failure error={loaded.error} />;
  return <TreeEntries project={project} tree={loaded.value!} open={open} selected={selected} />;
}

function RefChip({ git, commit }: { git?: FileHistoryGit; commit?: string }) {
  const hash = commit || git?.tree_commit;
  if (!hash) return null;
  return <span className="files-ref" title={`${git?.tree_ref ? `${git.tree_ref} at ` : ""}${hash}`}>
    <svg className="files-icon" viewBox="0 0 16 16" aria-hidden="true"><path d="M4.5 2v12 M4.5 9c0-3 7-2 7-5 M11.5 2.5v2" /><circle cx="4.5" cy="13" r="1.4" /><circle cx="11.5" cy="3" r="1.4" /></svg>
    {git?.tree_ref && !commit ? <span>{git.tree_ref}</span> : null}<code>{hash.slice(0, 7)}</code>
  </span>;
}

// FilesHeader names what the pane shows, path by path from the project's
// root, with the tree's commit and a button copying the path.
function FilesHeader({ project, name, path, git, kind, children }: { project: string; name: string; path: string; git?: FileHistoryGit; kind: "file" | "dir"; children?: React.ReactNode }) {
  const [copied, setCopied] = useState(false);
  return <div className="files-header">
    <h1 className="files-path">
      <FilesLink to={{ project }}>{name}</FilesLink>
      {fileBreadcrumbs(path).map(crumb => <React.Fragment key={crumb.name + (crumb.dir ?? "")}>
        <span className="files-path-sep" aria-hidden="true">/</span>
        {crumb.dir === null ? <span aria-current="page">{crumb.name}</span> : <FilesLink to={{ project, dir: crumb.dir }}>{crumb.name}</FilesLink>}
      </React.Fragment>)}
    </h1>
    <div className="files-header-actions">
      <RefChip git={git} />
      {path ? <button type="button" className="files-button" onClick={() => {
        // ui.py installs the shell clipboard wrapper, the shared host clipboard owner.
        void window.pharosCopyText?.(path).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1500); }, () => {});
      }} title={`Copy ${kind === "dir" ? "the directory's" : "the file's"} path`}>{copied ? "Copied" : "Copy path"}</button> : null}
      {children}
    </div>
  </div>;
}

// CheckoutSetup chooses the checkout of a repository on this Mac, from the
// checkouts its sessions ran in or a folder typed in.
function CheckoutSetup({ checkout, onDone }: { checkout: RepositoryCheckout; onDone?: () => void }) {
  const [draft, setDraft] = useState("");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const post = async (body: Record<string, unknown>) => {
    setSaving(true); setError("");
    try {
      const response = await fetch(`/api/repositories/${encodeURIComponent(checkout.repository_id)}/checkout`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      const answer = await response.json();
      if (!response.ok) throw new Error(answer.error ?? `HTTP ${response.status}`);
      onDone?.();
      refreshFiles();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : String(failure));
    } finally { setSaving(false); }
  };
  const candidates = (checkout.candidates ?? []).filter(path => path !== checkout.path);
  return <section className="files-checkout" aria-label="Repository checkout">
    <h2>{checkout.status === "set" ? "Change this repository's checkout" : `See ${checkout.name ?? "this repository"}'s code, blame and history`}</h2>
    <p>{checkout.status === "unavailable" ? `${checkout.reason} ` : ""}Choose where {checkout.name ?? "this repository"} is checked out on this Mac. When you open its code, blame, history or a commit's diff, Pharos runs read-only Git there, and stores nothing it reads. Sessions and their edits show without one.</p>
    {candidates.length ? <ul className="files-checkout-candidates">{candidates.map(path => <li key={path}>
      <button type="button" className="files-button" disabled={saving} onClick={() => void post({ path })}>Use <code>{path}</code></button>
    </li>)}</ul> : null}
    <form className="files-checkout-form" onSubmit={event => { event.preventDefault(); void post({ path: draft.trim() }); }}>
      <label htmlFor={`checkout-${checkout.repository_id}`}>{candidates.length ? "Or a folder" : "Folder"}</label>
      <input id={`checkout-${checkout.repository_id}`} type="text" value={draft} spellCheck={false} placeholder="~/code/project" onChange={event => setDraft(event.target.value)} />
      <button type="submit" className="files-button" disabled={saving || !draft.trim()}>Use this folder</button>
      {checkout.status === "set" ? <button type="button" className="files-button" disabled={saving} onClick={() => void post({ clear: true })}>Forget the checkout</button> : null}
    </form>
    {error ? <Failure error={error} /> : null}
  </section>;
}

// CheckoutLine says where Git reads from, with a control to change it.
function CheckoutLine({ checkout }: { checkout?: RepositoryCheckout }) {
  const [changing, setChanging] = useState(false);
  if (!checkout || checkout.status !== "set") return null;
  return <>
    <p className="files-checkout-line muted">Read by Git from <code>{checkout.path}</code> · <button type="button" className="files-link-button" aria-expanded={changing} onClick={() => setChanging(value => !value)}>{changing ? "Keep it" : "Change"}</button></p>
    {changing ? <CheckoutSetup checkout={checkout} onDone={() => setChanging(false)} /> : null}
  </>;
}

// NeedsCheckout stands in for a Git view until the checkout is chosen.
function NeedsCheckout({ checkout, children }: { checkout?: RepositoryCheckout; children: React.ReactNode }) {
  if (!checkout) return <Loading />;
  if (checkout.status !== "set") return <CheckoutSetup checkout={checkout} />;
  return <>{children}</>;
}

function Outcome({ label, why }: { label?: string; why?: string }) {
  const outcome = outcomeDisplay(label);
  return <span className="file-history-outcome" title={outcome.description}>{outcome.label}{why ? <span className="muted"> · {why}</span> : null}</span>;
}

// SessionLinks are the sessions linked to a commit, each opening where it
// printed or made the commit, else the session.
function SessionLinks({ links, empty = true }: { links: FileHistoryLink[]; empty?: boolean }) {
  if (!links.length) return empty ? <span className="muted">No linked session</span> : null;
  return <span className="files-session-links">{links.map((link, index) => <React.Fragment key={link.workspace_id}>{index ? ", " : ""}
    <PageLink href={link.url ?? `/work/${encodeURIComponent(link.workspace_id)}`} title={linkText(link)}>{link.title || (link.counted ? "Session" : "Candidate session")}</PageLink>
    {link.counted ? null : <span className="muted"> (candidate)</span>}
  </React.Fragment>)}</span>;
}

// LatestCommit is the newest commit under a file or directory, as a bar
// above it: from Git's history at the tree's commit when the checkout is
// chosen (the page History shows), else the newest commit the scan stored.
function LatestCommit({ project, path, checkout, stored, count, history }: { project: string; path: string; checkout?: RepositoryCheckout; stored?: FileHistoryCommit; count: number; history: FilePageTarget }) {
  const log = useFiles<FileLog>(checkout?.status === "set" ? `/api/file-log?${query({ project, path })}` : null);
  const read = checkout?.status === "set";
  if (read && !log) return <div className="files-latest muted">Loading the latest commit…</div>;
  const fromGit = log?.value?.commits?.[0];
  const commit: FileHistoryCommit | undefined = fromGit ?? (read && !log?.error && !log?.value?.note ? undefined : stored);
  if (!commit) return null;
  const state = commitState(commit);
  return <div className="files-latest">
    <span className="files-latest-main">
      {fromGit ? <span className="files-latest-author">{fromGit.author}</span> : null}
      <FilesLink to={{ project, commit: commit.hash, file: history.file }} className="files-latest-subject">{fromGit?.subject ?? <code>{commit.hash.slice(0, 7)}</code>}</FilesLink>
      <SessionLinks links={commit.sessions} empty={false} />
    </span>
    <span className="files-latest-meta muted">
      {state ? <span>{state}</span> : null}
      <code title={commit.hash}>{commit.hash.slice(0, 7)}</code>
      <span title={commit.author_at}>{age(commit.author_at)}</span>
      <FilesLink to={history}>{fromGit ? "History" : plural(count, "commit")}</FilesLink>
    </span>
  </div>;
}

function Tabs<T extends string>({ tabs, labels, current, target }: { tabs: readonly T[]; labels: Record<T, string>; current: T; target: (view: T) => FilePageTarget }) {
  return <nav className="files-tabs" aria-label="Views">
    {tabs.map(tab => <FilesLink key={tab} to={target(tab)} aria-current={tab === current ? "page" : undefined}>{labels[tab]}</FilesLink>)}
  </nav>;
}

function FilePane({ path, project, view, onProject }: { path: string; project: string; view: string; onProject: (ref: string) => void }) {
  const loaded = useFiles<FileHistory>(`/api/file-history?${query({ path, project })}`);
  const files = loaded?.value?.files;
  const only = files?.length === 1 ? files[0].project_ref : "";
  useEffect(() => { if (only && !project) onProject(only); }, [only, project]);
  if (!loaded) return <Loading />;
  if (loaded.error) return <Failure error={loaded.error} />;
  if (!files!.length) return <p className="muted">{loaded.value!.note}</p>;
  if (files!.length > 1) return <section className="files-matches" aria-label="Matching files">
    <h2>{plural(files!.length, "file")} match <code>{path}</code></h2>
    {fileHistoryNotice(loaded.value!) ? <p className="muted">{fileHistoryNotice(loaded.value!)}</p> : null}
    <ul className="file-history-list">{files!.map(file => <li key={file.file_key}>
      <FileIcon />
      {file.project_ref ? <FilesLink to={{ project: file.project_ref, file: file.relative_path ?? file.path }}><code>{file.relative_path ?? file.path}</code></FilesLink> : <code>{file.path}</code>}
      <span className="muted">{file.project ?? ""} · {plural(file.sessions.length, "session")}</span>
    </li>)}</ul>
  </section>;
  return <FileView file={files![0]} view={view} />;
}

function FileView({ file, view }: { file: FileHistoryFile; view: string }) {
  const project = file.project_ref ?? "", relative = file.relative_path ?? file.path;
  const repository = file.project_kind === "repository";
  const { checkout, git } = useProjectTree(project);
  const tab = fileTab(view, repository, file.in_tree !== false);
  return <section className="files-file" aria-label={file.path}>
    <FilesHeader project={project} name={file.project ?? project} path={relative} git={repository ? git ?? file.git : undefined} kind="file" />
    {repository ? <LatestCommit project={project} path={relative} checkout={checkout} stored={file.commits[0]} count={file.commits.length}
      history={{ project, file: relative, view: "history" }} /> : null}
    <p className="muted files-summary">
      {file.sessions.length ? `Edited by ${plural(file.sessions.length, "session")}${file.sessions_truncated ? "+" : ""}` : "No session edited this file through its tool calls"}
      {file.in_tree === false && file.git.tree_commit ? ` · ${notInTreeText(file.git)}` : ""}
      {!repository ? " · a folder, not a Git repository: its history is its sessions' edits" : ""}
    </p>
    {repository ? <CheckoutLine checkout={checkout} /> : null}
    <Tabs tabs={fileTabs(repository)} labels={fileViewLabels} current={tab} target={view => ({ project, file: relative, view })} />
    {tab === "code" ? <NeedsCheckout checkout={checkout}><CodeView project={project} path={relative} /></NeedsCheckout>
      : tab === "blame" ? <NeedsCheckout checkout={checkout}><BlameView project={project} path={relative} /></NeedsCheckout>
      : tab === "history" ? <HistoryView project={project} path={relative} checkout={checkout} stored={file} />
      : tab === "changes" ? <ChangesView project={project} path={relative} />
      : <SessionsView file={file} />}
  </section>;
}

// CodeView is the file at the tree the scan read, with line numbers.
function CodeView({ project, path }: { project: string; path: string }) {
  const loaded = useFiles<FileContent>(`/api/file-content?${query({ project, path })}`);
  if (!loaded) return <Loading />;
  if (loaded.error) return <Failure error={loaded.error} />;
  const content = loaded.value!;
  if (content.note || content.text == null) return <p className="muted">{content.note ?? (content.binary ? "A binary file." : "Git did not read this file.")}</p>;
  const lines = content.text.replace(/\n$/, "").split("\n");
  return <div className="files-blob">
    <div className="files-blob-head muted">{plural(lines.length, "line")} · {content.size != null ? sizeText(content.size) : ""}{content.ref === "HEAD" ? " · the checkout's HEAD" : ""}{content.truncated ? " · cut short" : ""}</div>
    <div className="files-code"><pre className="files-gutter" aria-hidden="true">{lines.map((_, index) => index + 1).join("\n")}</pre><pre className="files-lines"><code>{lines.join("\n")}</code></pre></div>
  </div>;
}

function sizeText(bytes: number): string {
  if (bytes >= 1 << 20) return `${(bytes / (1 << 20)).toFixed(1)} MB`;
  if (bytes >= 1 << 10) return `${Math.round(bytes / (1 << 10))} KB`;
  return plural(bytes, "byte");
}

// BlameView is the commit that last changed each run of lines, and the
// sessions linked to it; a session's lines are marked.
function BlameView({ project, path }: { project: string; path: string }) {
  const loaded = useFiles<FileBlame>(`/api/file-blame?${query({ project, path })}`);
  if (!loaded) return <Loading />;
  if (loaded.error) return <Failure error={loaded.error} />;
  const blame = loaded.value!;
  if (blame.note || !blame.lines) return <p className="muted">{blame.note ?? "Git did not blame this file."}</p>;
  const rows = blameRows(blame), commits = blame.commits ?? {};
  const bySessions = Object.values(commits).filter(commit => commit.sessions.some(link => link.counted)).length;
  return <div className="files-blob">
    <div className="files-blob-head muted">{plural(blame.lines.length, "line")} from {plural(Object.keys(commits).length, "commit")}{bySessions ? `, ${bySessions} linked to sessions (marked)` : ""}</div>
    <div className="files-blame">
      {rows.map(row => {
        const commit = commits[row.hash], linked = commit?.sessions.some(link => link.counted);
        return <div key={row.start} className={`files-blame-row${linked ? " files-blame-session" : ""}`}>
          <div className="files-blame-meta">
            <span className="files-blame-when" title={commit?.author_at}>{age(commit?.author_at)}</span>
            <FilesLink to={{ project, commit: row.hash, file: path }} className="files-blame-subject" title={`${commit?.summary ?? ""} (${row.hash.slice(0, 7)}, ${commit?.author ?? ""})`}>{commit?.summary || row.hash.slice(0, 7)}</FilesLink>
            {commit?.sessions.length ? <SessionLinks links={commit.sessions} /> : null}
          </div>
          <pre className="files-gutter" aria-hidden="true">{row.lines.map((_, index) => row.start + index).join("\n")}</pre>
          <pre className="files-lines"><code>{row.lines.join("\n")}</code></pre>
        </div>;
      })}
    </div>
  </div>;
}

// HistoryView is the commits of a file or directory, read by Git from the
// checkout, by day, a page at a time; without a checkout, the commits the
// scan stored.
function HistoryView({ project, path, checkout, stored }: { project: string; path: string; checkout?: RepositoryCheckout; stored?: { commits: FileHistoryCommit[] } }) {
  if (!checkout) return <Loading />;
  if (checkout.status !== "set") return <>
    <CheckoutSetup checkout={checkout} />
    {stored?.commits.length ? <>
      <h2 className="files-subheading">Commits Pharos recorded</h2>
      <p className="muted">The commits local Git found since the repository's first session edit; the full history needs the checkout.</p>
      <ol className="files-commits">{stored.commits.map(commit => <li key={commit.hash}>
        <div className="files-commit-main">
          <FilesLink to={{ project, commit: commit.hash, file: path || undefined }}><code>{commit.hash.slice(0, 7)}</code></FilesLink>
          <span className="muted">{day(commit.author_at)} · {linesText(commit.lines_added, commit.lines_removed)}{commitState(commit) ? ` · ${commitState(commit)}` : ""}</span>
        </div>
        <SessionLinks links={commit.sessions} />
      </li>)}</ol>
    </> : null}
  </>;
  return <LogPages project={project} path={path} />;
}

function LogPages({ project, path }: { project: string; path: string }) {
  const [pages, setPages] = useState(1);
  useEffect(() => setPages(1), [project, path]);
  return <>{Array.from({ length: pages }, (_, page) => <LogPage key={page} project={project} path={path} offset={page * 50} last={page === pages - 1} onMore={() => setPages(pages + 1)} />)}</>;
}

function LogPage({ project, path, offset, last, onMore }: { project: string; path: string; offset: number; last: boolean; onMore: () => void }) {
  const loaded = useFiles<FileLog>(`/api/file-log?${query({ project, path, offset: offset || undefined })}`);
  if (!loaded) return <Loading />;
  if (loaded.error) return <Failure error={loaded.error} />;
  const log = loaded.value!;
  if (log.note) return <p className="muted">{log.note}</p>;
  const commits = log.commits ?? [];
  if (!commits.length && !offset) return <p className="muted">Git found no commit of {path || "this repository"}.</p>;
  return <>
    {commitsByDay(commits).map(group => <section key={`${offset}-${group.day}`} className="files-day" aria-label={`Commits on ${group.day}`}>
      <h2 className="files-subheading">Commits on {group.day}</h2>
      <ol className="files-commits">{group.commits.map(commit => <LogCommit key={commit.hash} project={project} path={path} commit={commit} />)}</ol>
    </section>)}
    {last && log.more ? <button type="button" className="files-button files-more" onClick={onMore}>Older commits</button> : null}
  </>;
}

function LogCommit({ project, path, commit }: { project: string; path: string; commit: FileLogCommit }) {
  const state = commitState(commit);
  return <li className={commit.sessions.some(link => link.counted) ? "files-commit-session" : undefined}>
    <div className="files-commit-main">
      <FilesLink className="files-commit-subject" to={{ project, commit: commit.hash, file: path && !path.endsWith("/") ? path : undefined }}>{commit.subject}</FilesLink>
      <span className="muted">{commit.author} · <span title={commit.author_at}>{age(commit.author_at)}</span>{state ? ` · ${state}` : ""}{commit.parents.length > 1 ? " · merge" : ""}</span>
      {commit.sessions.length ? <span className="files-commit-sessions">Sessions: <SessionLinks links={commit.sessions} /></span> : null}
    </div>
    <FilesLink to={{ project, commit: commit.hash }} className="files-hash" title={commit.hash}><code>{commit.hash.slice(0, 7)}</code></FilesLink>
  </li>;
}

// ChangesView is the file's edits as their calls made them, newest first,
// each opening at its place in the transcript.
function ChangesView({ project, path }: { project: string; path: string }) {
  const [pages, setPages] = useState(1);
  useEffect(() => setPages(1), [project, path]);
  return <>{Array.from({ length: pages }, (_, page) => <ChangesPage key={page} project={project} path={path} offset={page * 30} last={page === pages - 1} onMore={() => setPages(pages + 1)} />)}</>;
}

function ChangesPage({ project, path, offset, last, onMore }: { project: string; path: string; offset: number; last: boolean; onMore: () => void }) {
  const loaded = useFiles<FileChanges>(`/api/file-changes?${query({ project, path, offset: offset || undefined })}`);
  if (!loaded) return <Loading />;
  if (loaded.error) return <Failure error={loaded.error} />;
  const value = loaded.value!;
  if (!value.changes.length && !offset) return <p className="muted">{value.note ?? "No session edited this file with an edit tool. Sessions that changed it another way (a script, a generator) show in its history, through the commits."}</p>;
  return <>
    {offset ? null : <p className="muted files-summary">Each edit as the session's call made it, read from its transcript. Replacements do not know the file's line numbers.</p>}
    {value.changes.map((change, index) => <article key={`${offset}-${index}`} className="files-change">
      <div className="files-change-head">
        <PageLink href={change.session_url} className="files-change-session">{change.title || change.workspace_id}</PageLink>
        <span className="muted">{changeText(change)}</span>
        <span className="muted" title={change.at ?? undefined}>{change.at ? new Date(change.at).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" }) : ""}</span>
        <Outcome label={change.outcome_label} />
        <PageLink href={change.url} className="files-change-open">Open in transcript</PageLink>
      </div>
      {change.hunks.length ? <DiffTable hunks={change.hunks.map(hunk => ({ header: hunk.header ?? "", lines: hunk.lines.map(([kind, text]) => ({ kind: kind as DiffLine["kind"], text })) }))} numbered={false} />
        : <p className="muted files-change-note">{change.note ?? (change.kind === "listed" ? "The call named this file without its lines." : "No lines to show.")}</p>}
      {change.truncated ? <p className="muted files-change-note">The rest of this edit is not shown.</p> : null}
    </article>)}
    {last && value.more ? <button type="button" className="files-button files-more" onClick={onMore}>Older edits</button> : null}
  </>;
}

const diffLineLimit = 3000;

// DiffTable shows hunks of a diff, each line marked kept, added or removed,
// with the old and new line numbers when Git gave them.
function DiffTable({ hunks, numbered }: { hunks: { header: string; lines: DiffLine[] }[]; numbered: boolean }) {
  let shown = 0;
  return <div className={`files-diff${numbered ? "" : " files-diff-plain"}`} role="table" aria-label="Diff">
    {hunks.map((hunk, index) => {
      if (shown >= diffLineLimit) return null;
      const lines = hunk.lines.slice(0, diffLineLimit - shown);
      shown += lines.length;
      return <div key={index} role="rowgroup">
        {hunk.header ? <div className="files-diff-hunk" role="row"><span role="cell">{hunk.header}</span></div> : index ? <div className="files-diff-gap" role="row" /> : null}
        {lines.map((line, at) => <div key={at} role="row" className={`files-diff-line${line.kind === "+" ? " add" : line.kind === "-" ? " del" : line.kind === "\\" ? " note" : ""}`}>
          {numbered ? <><span className="files-diff-num" role="cell">{line.old ?? ""}</span><span className="files-diff-num" role="cell">{line.new ?? ""}</span></> : null}
          <span className="files-diff-sign" role="cell" aria-label={line.kind === "+" ? "added" : line.kind === "-" ? "removed" : undefined}>{line.kind === "\\" ? "" : line.kind}</span>
          <span className="files-diff-text" role="cell">{line.text}</span>
        </div>)}
      </div>;
    })}
    {shown >= diffLineLimit ? <p className="muted files-change-note">Only the first {diffLineLimit.toLocaleString("en-US")} lines are shown.</p> : null}
  </div>;
}

function SessionsView({ file }: { file: FileHistoryFile }) {
  if (!file.sessions.length) return <p className="muted">No session edited this file through its tool calls. Sessions that changed it another way (a script, a generator) are linked to its commits, where local Git found them.</p>;
  return <>
    <ol className="file-history-list file-sessions">
      {file.sessions.map(session => <SessionEntry key={session.workspace_id} file={file} session={session} />)}
    </ol>
    {file.sessions_truncated ? <p className="muted">Older sessions are not shown.</p> : null}
  </>;
}

function SessionEntry({ file, session }: { file: FileHistoryFile; session: FileHistorySession }) {
  const committed = sessionCommits(file, session.workspace_id), places = session.places ?? [];
  return <li className="file-session" id={`session-${session.workspace_id}`}>
    <div className="file-session-head">
      <span className="file-history-when">{day(session.last_edit_at)}</span>
      <PageLink href={session.url} title="Open the session at its first edit of this file">{session.title || session.workspace_id}</PageLink>
      <span className="muted">{plural(session.edits ?? 0, "edit")} · {linesText(session.lines_added, session.lines_removed)}{session.rewrote ? " · rewrote the file" : ""}</span>
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
      <FilesLink to={{ project: file.project_ref, commit: commit.hash, file: file.relative_path }} title={linkText(link)}><code>{commit.hash.slice(0, 7)}</code></FilesLink>{link.counted ? "" : " (candidate)"}</React.Fragment>)}</p> : null}
  </li>;
}

function DirectoryPane({ project, dir, view }: { project: string; dir: string; view: string }) {
  const loaded = useFiles<FileTree>(`/api/file-tree?${query({ project, dir, activity: "1" })}`);
  const { checkout, git: rootGit } = useProjectTree(project);
  const tree = loaded?.value;
  const lastCommits = useFiles<FileLastCommits>(checkout?.status === "set" && loaded?.value?.project?.kind === "repository" ? `/api/file-last-commits?${query({ project, path: dir })}` : null);
  if (!loaded) return <Loading />;
  if (loaded.error) return <Failure error={loaded.error} />;
  if (tree!.choices) return <Choices choices={tree!.choices} target={{ dir }} />;
  const repository = tree!.project?.kind === "repository";
  const tab = directoryTab(view, repository);
  const name = tree!.project?.name ?? project;
  return <section className="files-directory" aria-label={dir || name}>
    <FilesHeader project={project} name={name} path={dir} git={repository ? rootGit ?? tree!.git : undefined} kind="dir" />
    {repository ? <LatestCommit project={project} path={dir} checkout={checkout} stored={tree!.commits?.[0]} count={tree!.commits?.length ?? 0}
      history={{ project, dir, view: "commits" }} /> : <p className="muted files-summary">{treeText(tree!.git)}</p>}
    {repository && tree!.git?.status !== "checked" ? <p className="muted files-summary">{tree!.git?.reason || "Local Git has not checked this repository."}</p> : null}
    {repository ? <CheckoutLine checkout={checkout} /> : null}
    <Tabs tabs={directoryTabs(repository)} labels={directoryViewLabels} current={tab} target={view => ({ project, dir, view })} />
    {tab === "files" ? <DirectoryTable project={project} dir={dir} tree={tree!} fromGit={checkout?.status === "set" ? lastCommits?.value?.entries ?? null : undefined} />
      : tab === "commits" ? <HistoryView project={project} path={dir} checkout={checkout} stored={{ commits: tree!.commits ?? [] }} />
      : <DirectorySessions tree={tree!} />}
  </section>;
}

// DirectoryTable lists a directory's entries with their last change: the
// last commit Git finds at the tree's commit when the checkout is chosen
// (null while it reads), else the newest stored commit or the session that
// last edited it.
function DirectoryTable({ project, dir, tree, fromGit }: { project: string; dir: string; tree: FileTree; fromGit?: Record<string, FileLogCommit> | null }) {
  const entries = tree.entries ?? [], hasTree = Boolean(tree.git?.tree_commit);
  if (!entries.length) return <p className="muted">Nothing here.</p>;
  const change = (entry: FileTreeEntry) => {
    const read = fromGit?.[entry.name], file = entry.kind === "file" ? entry.path : undefined;
    if (read) return { node: <FilesLink to={{ project, commit: read.hash, file }} title={`${read.subject} (${read.hash.slice(0, 7)}, ${read.author})`}>{read.subject}</FilesLink>, at: read.author_at };
    const commit = entry.last_commit, session = entry.last_session;
    if (fromGit === undefined && commit && (!session?.at || (commit.author_at ?? "") >= session.at)) return { node: <FilesLink to={{ project, commit: commit.hash, file }}><code>{commit.hash.slice(0, 7)}</code></FilesLink>, at: commit.author_at };
    if (session) return { node: <PageLink href={session.url} className="files-table-session" title="The session that last edited it">{session.title || session.workspace_id}</PageLink>, at: session.at };
    return { node: null, at: fromGit === null ? undefined : entry.last_at ?? undefined };
  };
  return <table className="files-table">
    <thead><tr><th scope="col">Name</th><th scope="col">Last change</th><th scope="col" className="files-table-when">When</th></tr></thead>
    <tbody>
      {dir ? <tr><td colSpan={3} className="files-table-name"><FilesLink to={{ project, dir: parentDir(dir) }} aria-label="Parent directory"><FolderIcon /><span>..</span></FilesLink></td></tr> : null}
      {entries.map(entry => {
        const last = change(entry), gone = hasTree && !entry.in_tree;
        return <tr key={entry.path} className={gone ? "file-tree-gone" : undefined}>
          <td className="files-table-name"><FilesLink to={entry.kind === "dir" ? { project, dir: entry.path } : { project, file: entry.path }} title={gone ? notInTreeText(tree.git) : undefined}>
            {entry.kind === "dir" ? <FolderIcon /> : <FileIcon />}<span>{entry.name}</span></FilesLink></td>
          <td className="files-table-change">{last.node}{entry.sessions ? <span className="muted files-table-count">{last.node ? " · " : ""}{plural(entry.sessions, "session")}</span> : null}</td>
          <td className="files-table-when muted" title={last.at}>{age(last.at)}</td>
        </tr>;
      })}
    </tbody>
  </table>;
}

function DirectorySessions({ tree }: { tree: FileTree }) {
  if (!tree.sessions?.length) return <p className="muted">No session edited a file here.</p>;
  return <ol className="file-history-list">{tree.sessions.map(session => <li key={session.workspace_id}>
    <span className="file-history-when">{day(session.last_edit_at)}</span>
    <PageLink href={session.url}>{session.title || session.workspace_id}</PageLink>
    <span className="muted">{plural(session.files, "file")} · {plural(session.edits, "edit")}</span>
    <Outcome label={session.outcome_label} />
  </li>)}</ol>;
}

// CommitPane is one commit: its message from the checkout, where it stands,
// the sessions linked to it and each file's diff, the focused file's open.
function CommitPane({ project, commit, focus }: { project: string; commit: string; focus: string }) {
  const stored = useFiles<FileCommitDetail>(`/api/file-commit?${query({ project, commit })}`);
  const { checkout, repository } = useProjectTree(project);
  const fromGit = useFiles<CommitGit>(checkout?.status === "set" ? `/api/file-commit-git?${query({ project, commit })}` : null);
  if (!stored) return <Loading />;
  if (stored.error) return <Failure error={stored.error} />;
  const value = stored.value!;
  if (value.choices) return <Choices choices={value.choices} target={{ commit }} />;
  const detail = value.commit, git = fromGit?.value?.commit, name = value.project?.name ?? project;
  const hash = git?.hash ?? detail?.hash ?? commit;
  const sessions = detail?.sessions ?? git?.sessions ?? [];
  const storedFiles = new Map((detail?.files ?? []).map(file => [file.path, file]));
  const files = git?.files ?? detail?.files.map(file => ({ path: file.path, lines_added: file.lines_added, lines_removed: file.lines_removed, binary: false })) ?? [];
  const added = files.reduce((sum, file) => sum + (file.lines_added ?? 0), 0), removed = files.reduce((sum, file) => sum + (file.lines_removed ?? 0), 0);
  const parents = git?.parents ?? [];
  return <section className="files-commit" aria-label={`Commit ${hash.slice(0, 7)}`}>
    <div className="files-header">
      <nav className="files-path" aria-label="Project"><FilesLink to={{ project }}>{name}</FilesLink><span className="files-path-sep" aria-hidden="true">/</span><span>commit</span></nav>
    </div>
    <div className="files-commit-box">
      <h1 className="files-commit-title">{git?.subject ?? <>Commit <code>{hash.slice(0, 12)}</code></>}</h1>
      {git?.body ? <pre className="files-commit-body">{git.body}</pre> : null}
      <p className="muted files-commit-meta">
        {git ? <>{git.author} authored <span title={git.author_at}>{day(git.author_at)}</span>{git.committed_at && day(git.committed_at) !== day(git.author_at) ? <>, committed {day(git.committed_at)}</> : null}</>
          : detail ? <>Authored {day(detail.author_at)}{detail.committed_at && day(detail.committed_at) !== day(detail.author_at) ? `, committed ${day(detail.committed_at)}` : ""}</> : null}
        {" · "}<code title={hash}>{hash.slice(0, 12)}</code>
        {parents.length ? <> · {parents.length > 1 ? "parents" : "parent"} {parents.map((parent, index) => <React.Fragment key={parent}>{index ? ", " : ""}<FilesLink to={{ project, commit: parent }}><code>{parent.slice(0, 7)}</code></FilesLink></React.Fragment>)}</> : null}
      </p>
      {detail ? (detail.reverted_by || commitState(detail) ? <p className="files-commit-state">{detail.reverted_by ? <>Reverted by <FilesLink to={{ project, commit: detail.reverted_by }}><code>{detail.reverted_by.slice(0, 7)}</code></FilesLink>{detail.revert_evidence ? <span className="muted"> ({detail.revert_evidence})</span> : null}</>
        : commitState(detail)}{detail.mainline_evidence ? <span className="muted"> · {detail.mainline_evidence}</span> : null}</p> : null)
        : git ? null : <p className="muted">{value.note}</p>}
      <div className="files-commit-sessions-box">
        <h2 className="files-subheading">Sessions</h2>
        {sessions.length ? <ul className="files-commit-session-list">{sessions.map(link => <li key={link.workspace_id}>
          <PageLink href={link.url ?? `/work/${encodeURIComponent(link.workspace_id)}`}>{link.title || link.workspace_id}</PageLink>
          <span className="muted"> {linkText(link)} · confidence {link.confidence.toFixed(2)}</span>
        </li>)}</ul> : <p className="muted">No session is linked to this commit: it was made outside the sessions this library holds, or they left no trace of it.</p>}
      </div>
    </div>
    {repository ? <CheckoutLine checkout={checkout} /> : null}
    {fromGit?.value?.note ? <p className="muted">{fromGit.value.note}</p> : null}
    {fromGit?.error ? <Failure error={fromGit.error} /> : null}
    <h2 className="files-subheading">{plural(files.length, "file")} changed <span className="muted">{linesText(added, removed)}</span></h2>
    {checkout && checkout.status !== "set" ? <CheckoutSetup checkout={checkout} /> : null}
    {git?.files_truncated ? <p className="muted">Too many files to list them all.</p> : null}
    <div className="files-commit-files">
      {files.map(file => {
        const known = storedFiles.get(file.path);
        const summary = <span className="files-commit-file">
          <FilesLink to={{ project, file: file.path }}><code>{file.path}</code></FilesLink>
          <span className="muted">{file.binary ? "binary" : linesText(file.lines_added, file.lines_removed)}{known?.sessions ? ` · edited by ${plural(known.sessions, "session")}` : ""}{known && !known.in_tree ? ` · ${notInTreeText(value.git)}` : ""}</span>
        </span>;
        return checkout?.status === "set" && fromGit?.value?.commit
          ? <CommitFile key={file.path} project={project} commit={hash} path={file.path} summary={summary} open={file.path === focus || (!focus && files.length <= 5)} />
          : <div key={file.path} className="files-commit-file-row">{summary}</div>;
      })}
    </div>
  </section>;
}

// CommitFile is one file's diff in a commit, read once opened.
function CommitFile({ project, commit, path, summary, open }: { project: string; commit: string; path: string; summary: React.ReactNode; open: boolean }) {
  const viewKey = `files-diff:${project}:${commit.slice(0, 12)}:${path}`;
  const [isOpen] = useViewEntry("disclosures", viewKey, open);
  return <ViewDisclosure className="files-commit-diff" viewKey={viewKey} summary={summary} defaultOpen={open}>
    {isOpen ? <CommitFileDiff project={project} commit={commit} path={path} /> : null}
  </ViewDisclosure>;
}

function CommitFileDiff({ project, commit, path }: { project: string; commit: string; path: string }) {
  const loaded = useFiles<FileDiff>(`/api/file-diff?${query({ project, commit, path })}`);
  if (!loaded) return <Loading />;
  if (loaded.error) return <Failure error={loaded.error} />;
  if (loaded.value!.note) return <p className="muted">{loaded.value!.note}</p>;
  const files = parseUnifiedDiff(loaded.value!.diff ?? "");
  if (!files.length) return <p className="muted">No change to show.</p>;
  return <>{files.map(file => file.binary ? <p key={file.path} className="muted">A binary file.</p>
    : <DiffTable key={file.path} hunks={file.hunks} numbered />)}{loaded.value!.truncated ? <p className="muted">This diff is too long to show in full.</p> : null}</>;
}
