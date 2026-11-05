// Files (docs/projects-and-outcomes.md, "Files, sessions and commits,
// linked both ways"): a project's files as a tree, and for a file, a
// directory or a commit, the sessions and commits that touched it, each
// linked to the place in a transcript where it happened. The service
// (/api/file-history, /api/file-tree, /api/file-commit, /api/work/{id}/files)
// reads stored facts; this module says how they read on the page.

export const fileHistoryRoute = "/files";

// A place in a session's transcript: the call that made an edit.
export type FilePlace = {
  at?: string; path?: string; lines_added?: number | null; lines_removed?: number | null;
  conversation_id?: string | null; message_id?: string | null; call_id?: string | null; url: string;
};
// Rework judged between two sessions on one file: the other session, and
// how many lines one added that the later one removed.
export type FileRework = { workspace_id: string; title?: string | null; removed_lines: number; url: string };
export type FileHistorySession = {
  workspace_id: string; title?: string; source?: string; first_edit_at?: string; last_edit_at?: string; edits?: number;
  lines_added?: number | null; lines_removed?: number | null; rewrote?: boolean; outcome_label?: string; outcome_why?: string; url: string;
  places?: FilePlace[]; places_truncated?: boolean; reworked_by?: FileRework[]; reworked?: FileRework[];
};
export type FileHistoryLink = { workspace_id: string; title?: string | null; method: string; confidence: number; counted: boolean; url?: string };
export type FileHistoryCommit = {
  hash: string; author_at?: string; lines_added?: number; lines_removed?: number; on_mainline?: boolean | null; reverted_by?: string | null; sessions: FileHistoryLink[]; url?: string;
};
export type FileHistoryGit = {
  status: "checked" | "not_checked" | "not_applicable" | string; reason?: string | null; checked_at?: string; commits_since?: string | null; commits_truncated?: boolean;
  tree_ref?: string | null; tree_commit?: string | null;
};
export type FileHistoryFile = {
  file_key: string; path: string; repository_id?: string; project?: string | null; project_kind?: string; project_ref?: string; relative_path?: string; url?: string; in_tree?: boolean;
  sessions: FileHistorySession[]; sessions_truncated?: boolean; commits: FileHistoryCommit[]; git: FileHistoryGit;
};
export type FileHistory = { path: string; files: FileHistoryFile[]; note: string };

export type FileProject = { ref: string; name: string; kind: "repository" | "folder" | string; repository_id?: string; folder?: string; sessions?: number; files?: number; last_at?: string };
export type FileTreeEntry = { name: string; path: string; kind: "dir" | "file"; sessions: number; commits: number; last_at?: string | null; in_tree: boolean };
export type FileTreeSession = { workspace_id: string; title?: string; files: number; edits: number; last_edit_at?: string; outcome_label?: string; outcome_why?: string; url: string };
export type FileTreeCommit = { hash: string; author_at?: string; files: number; lines_added?: number; lines_removed?: number; on_mainline?: boolean | null; reverted_by?: string | null; sessions: FileHistoryLink[] };
export type FileTree = {
  project?: FileProject; choices?: FileProject[]; projects?: FileProject[]; git?: FileHistoryGit; dir?: string;
  entries?: FileTreeEntry[]; sessions?: FileTreeSession[]; commits?: FileTreeCommit[];
};
export type FileCommitFile = { path: string; lines_added: number; lines_removed: number; in_tree: boolean; sessions: number };
export type FileCommitDetail = {
  project?: FileProject; choices?: FileProject[]; git?: FileHistoryGit; note?: string;
  commit?: FileHistoryCommit & { committed_at?: string; parents?: number; mainline_evidence?: string | null; revert_evidence?: string | null; files: FileCommitFile[] };
};

// A Files page address: a project's file, directory or commit, or the
// project's root. The service's filePageURL (file_tree.go) builds the same,
// its parameters in the same (sorted) order.
export type FilePageTarget = { project?: string; file?: string; dir?: string; commit?: string };

export function filePageURL(target: FilePageTarget): string {
  const params = new URLSearchParams();
  if (target.project) params.set("file_project", target.project);
  if (target.file) params.set("file", target.file);
  else if (target.dir) params.set("file_dir", target.dir);
  else if (target.commit) params.set("file_commit", target.commit);
  params.sort();
  return `${fileHistoryRoute}?${params}`;
}

// readFilePageRoute reads a Files page address back. A commit shows before
// a file, and a file before a directory, should a hand-made URL set several.
export function readFilePageRoute(search: URLSearchParams): Required<FilePageTarget> {
  return { project: search.get("file_project") ?? "", file: search.get("file") ?? "", dir: search.get("file_dir") ?? "", commit: search.get("file_commit") ?? "" };
}

// keepFilesView carries the page's open directories (its disclosures) to
// another address on it, so moving through the tree keeps it as it was.
export function keepFilesView(href: string, search: string): string {
  const url = new URL(href, "http://pharos.invalid");
  const disclosures = new URLSearchParams(search).get("disclosures");
  if (url.pathname === fileHistoryRoute && disclosures && !url.searchParams.has("disclosures")) url.searchParams.set("disclosures", disclosures);
  return url.pathname + url.search;
}

// parentDir is the directory a path is in, "" at the root.
export function parentDir(path: string): string {
  const index = path.replace(/\/+$/, "").lastIndexOf("/");
  return index > 0 ? path.slice(0, index) : "";
}

// treeAncestors are the directories above a path, outermost first, which
// the tree opens to show it.
export function treeAncestors(path: string): string[] {
  const dirs: string[] = [];
  for (let dir = parentDir(path); dir; dir = parentDir(dir)) dirs.unshift(dir);
  return dirs;
}

// fileBreadcrumbs are a path's directories and its own name, each with the
// directory it opens (the name itself opens nothing).
export function fileBreadcrumbs(path: string): { name: string; dir: string | null }[] {
  const parts = path.split("/").filter(Boolean), leading = path.startsWith("/") ? "/" : "";
  return parts.map((name, index) => ({ name: index === 0 ? leading + name : name, dir: index === parts.length - 1 ? null : leading + parts.slice(0, index + 1).join("/") }));
}

// fileTreeKey is a tree directory's disclosure identity on the page.
export function fileTreeKey(project: string, dir: string): string {
  return `files:${project}:${dir}`;
}

// entryText is what touched a tree entry: sessions that edited it and
// commits that changed it; a folder's entries have no commits to count.
export function entryText(entry: Pick<FileTreeEntry, "sessions" | "commits">, git: boolean): string {
  const parts = [];
  if (entry.sessions) parts.push(`${entry.sessions.toLocaleString("en-US")} session${entry.sessions === 1 ? "" : "s"}`);
  if (git && entry.commits) parts.push(`${entry.commits.toLocaleString("en-US")} commit${entry.commits === 1 ? "" : "s"}`);
  return parts.join(" · ");
}

// treeText says where a project's tree comes from: local Git's tree at the
// mainline tip, or, without Git, the files its sessions edited.
export function treeText(git: FileHistoryGit | undefined): string {
  if (!git || git.status === "not_applicable") return "A folder, not a Git repository: these are the files its sessions edited.";
  if (git.status === "checked" && git.tree_commit) return `The tree at ${git.tree_ref || "HEAD"} (${git.tree_commit.slice(0, 7)}), read by local Git, with files sessions edited that it does not hold.`;
  if (git.status === "checked") return "Local Git did not read this repository's tree (too large or no commit), so these are the files sessions edited and commits changed.";
  return `${git.reason || "Local Git has not checked this repository."} These are the files its sessions edited.`;
}

// notInTreeText marks a repository file the tree does not hold: deleted,
// renamed or never committed by the mainline tip.
export function notInTreeText(git: FileHistoryGit | undefined): string {
  return `Not in the tree at ${git?.tree_ref || "the mainline tip"}: deleted, renamed or never committed.`;
}

// placeText is one edit of a file: which, when, and how many lines.
export function placeText(place: FilePlace, index: number): string {
  const when = place.at ? new Date(place.at).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" }) : "";
  const lines = place.lines_added == null && place.lines_removed == null ? "" : linesText(place.lines_added, place.lines_removed);
  return [`Edit ${index + 1}`, when, lines].filter(Boolean).join(" · ");
}

// reworkText is one side of rework between sessions on a file.
export function reworkText(rework: FileRework, direction: "by" | "of"): string {
  const lines = `${rework.removed_lines.toLocaleString("en-US")} line${rework.removed_lines === 1 ? "" : "s"}`;
  return direction === "by" ? `removed ${lines} it added` : `had ${lines} removed by this session`;
}

// sessionCommits are the commits of a file a session is linked to.
export function sessionCommits(file: Pick<FileHistoryFile, "commits">, workspace: string): { commit: FileHistoryCommit; link: FileHistoryLink }[] {
  return file.commits.flatMap(commit => commit.sessions.filter(link => link.workspace_id === workspace).map(link => ({ commit, link })));
}

// fileHistoryURL opens a file's history, narrowed to one project when given.
export function fileHistoryURL(path: string, project?: string | null): string {
  const params = new URLSearchParams({ file: path });
  if (project) params.set("file_project", project);
  return `${fileHistoryRoute}?${params}`;
}

// linesText is a change's size; a tool that recorded no counts says so
// rather than reading as no change.
export function linesText(added: number | null | undefined, removed: number | null | undefined): string {
  if (added == null && removed == null) return "lines not recorded";
  return `+${(added ?? 0).toLocaleString("en-US")} −${(removed ?? 0).toLocaleString("en-US")}`;
}

// gitText is what local Git contributes for a file: commits where it checked
// the repository, and otherwise why there are none. It is never an error:
// a folder's history comes from transcripts alone. Local Git is read from
// the repository's first session edit, so commits are counted since then.
export function gitText(git: FileHistoryGit, commits: number): string {
  if (git.status === "not_applicable") return "A folder, not a Git repository: this history comes from the sessions' own edits.";
  if (git.status === "checked") {
    const since = git.commits_since ? ` since ${git.commits_since.slice(0, 10)}` : "";
    const found = commits ? `${commits.toLocaleString("en-US")} commit${commits === 1 ? "" : "s"} changed this file${since}, from local Git.` : `Local Git found no commit of this file${since}.`;
    return since ? `${found} Earlier commits are not read.` : found;
  }
  return git.reason || "Local Git has not checked this repository.";
}

// commitState is where a commit stands: reverted, on mainline, or only on
// a branch. Unknown mainline state is left unsaid.
export function commitState(commit: FileHistoryCommit): string {
  if (commit.reverted_by) return `reverted by ${commit.reverted_by.slice(0, 7)}`;
  if (commit.on_mainline === true) return "on mainline";
  if (commit.on_mainline === false) return "not on mainline";
  return "";
}

const linkMethods: Record<string, string> = {
  transcript: "printed by the session",
  message: "its git command wrote the message",
  window: "its git command ran then",
  rewrite: "a rebase or amend of its commit",
  inferred: "inferred from its edits",
};

// linkText is how a session is linked to a commit: counted links are the
// session's own; below the counting threshold a link is a candidate.
export function linkText(link: FileHistoryLink): string {
  const method = linkMethods[link.method] ?? link.method;
  return link.counted ? method : `candidate, ${method}`;
}
