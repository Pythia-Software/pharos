// Files (docs/projects-and-outcomes.md, "Files, sessions and commits,
// linked both ways" and "The file viewer's checkout"): a project's files as
// a tree, and for a file, a directory or a commit, its code, blame, history
// and diffs, and the sessions and commits that touched it, each linked to
// the place in a transcript where it happened. Most of the service
// (/api/file-history, /api/file-tree, /api/file-commit, /api/file-changes,
// /api/work/{id}/files) reads stored facts; code, blame, history and commit
// diffs (/api/file-content, /api/file-blame, /api/file-log, /api/file-diff,
// /api/file-commit-git, /api/file-last-commits) are read by Git from the checkout the person chose.
// This module says how they read on the page.

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
export type FileHistory = { path: string; files: FileHistoryFile[]; files_truncated?: boolean; note: string };

// fileHistoryNotice is the service's note where the page needs it: no file
// matched (or only in another project), or more matched than are listed.
export function fileHistoryNotice(history: FileHistory): string {
  return !history.files.length || history.files_truncated ? history.note : "";
}

export type FileProject = { ref: string; name: string; kind: "repository" | "folder" | string; repository_id?: string; folder?: string; sessions?: number; files?: number; last_at?: string };
export type FileTreeEntry = {
  name: string; path: string; kind: "dir" | "file"; sessions: number; commits: number; last_at?: string | null; in_tree: boolean;
  last_session?: { workspace_id: string; title?: string | null; at?: string; url: string } | null; last_commit?: { hash: string; author_at?: string; url: string } | null;
};
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
// A commit with a file opens the commit at that file's diff. A view is the
// tab a file or directory opens on.
export type FilePageTarget = { project?: string; file?: string; dir?: string; commit?: string; view?: string };

export function filePageURL(target: FilePageTarget): string {
  const params = new URLSearchParams();
  if (target.project) params.set("file_project", target.project);
  if (target.commit) {
    params.set("file_commit", target.commit);
    if (target.file) params.set("file", target.file);
  } else if (target.file) {
    params.set("file", target.file);
    if (target.view) params.set("file_view", target.view);
  } else {
    if (target.dir) params.set("file_dir", target.dir);
    if (target.view) params.set("file_dir_view", target.view);
  }
  params.sort();
  return `${fileHistoryRoute}?${params}`;
}

// readFilePageRoute reads a Files page address back. A commit shows before
// a file, and a file before a directory, should a hand-made URL set several.
export function readFilePageRoute(search: URLSearchParams): Required<FilePageTarget> {
  return {
    project: search.get("file_project") ?? "", file: search.get("file") ?? "", dir: search.get("file_dir") ?? "", commit: search.get("file_commit") ?? "",
    view: search.get("file_commit") ? "" : search.get("file") ? search.get("file_view") ?? "" : search.get("file_dir_view") ?? "",
  };
}

// A file's tabs: its code and blame and its history from the checkout, the
// diffs its sessions' edits made, and the sessions themselves. A folder's
// file has no Git, so only the last two.
export const fileViews = ["code", "blame", "history", "changes", "sessions"] as const;
export type FileView = typeof fileViews[number];
export const fileViewLabels: Record<FileView, string> = { code: "Code", blame: "Blame", history: "History", changes: "Changes", sessions: "Sessions" };
export function fileTabs(repository: boolean): FileView[] {
  return repository ? [...fileViews] : ["changes", "sessions"];
}
// fileTab is the tab a file shows: the one asked for when it has it, else
// its code, or its changes outside Git or when the tree does not hold it.
export function fileTab(asked: string, repository: boolean, inTree = true): FileView {
  const tabs = fileTabs(repository);
  if ((tabs as string[]).includes(asked)) return asked as FileView;
  return repository && inTree ? "code" : "changes";
}
// A directory's tabs: its files, its commits from the checkout (a folder
// has none) and the sessions that edited files in it.
export const directoryViews = ["files", "commits", "sessions"] as const;
export type DirectoryView = typeof directoryViews[number];
export const directoryViewLabels: Record<DirectoryView, string> = { files: "Files", commits: "Commits", sessions: "Sessions" };
export function directoryTabs(repository: boolean): DirectoryView[] {
  return repository ? [...directoryViews] : ["files", "sessions"];
}
export function directoryTab(asked: string, repository: boolean): DirectoryView {
  const tabs = directoryTabs(repository);
  return (tabs as string[]).includes(asked) ? asked as DirectoryView : tabs[0];
}

// keepFilesView carries the page's open directories (its disclosures) and
// whether the tree is hidden to another address on it, so moving through
// the tree keeps it as it was.
export function keepFilesView(href: string, search: string): string {
  const url = new URL(href, "http://pharos.invalid"), from = new URLSearchParams(search);
  if (url.pathname !== fileHistoryRoute) return url.pathname + url.search;
  for (const param of ["disclosures", "files_tree_collapsed"]) {
    const value = from.get(param);
    if (value && !url.searchParams.has(param)) url.searchParams.set(param, value);
  }
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
// the repository's first recorded edit, so commits are counted since then.
// A list cut at its bound names only the commits shown.
export function gitText(git: FileHistoryGit, commits: number): string {
  if (git.status === "not_applicable") return "A folder, not a Git repository: this history comes from the agents' recorded edits.";
  if (git.status === "checked") {
    const since = git.commits_since ? ` since ${git.commits_since.slice(0, 10)}` : "";
    const count = `${commits.toLocaleString("en-US")} commit${commits === 1 ? "" : "s"}`;
    const found = !commits ? `Local Git found no commit of this file${since}.`
      : git.commits_truncated ? `The ${count} that most recently changed this file, from local Git; older ones are not shown.`
      : `${count} changed this file${since}, from local Git.`;
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
  transcript: "printed in its transcript",
  message: "its git command wrote the message",
  window: "its git command ran then",
  rewrite: "a rebase or amend of its commit",
  landed: "its branch commit landed in this squash",
  inferred: "inferred from its edits",
};

// linkText is how a workspace is linked to a commit: counted links are the
// workspace's own; below the counting threshold a link is a candidate.
export function linkText(link: FileHistoryLink): string {
  const method = linkMethods[link.method] ?? link.method;
  return link.counted ? method : `candidate, ${method}`;
}

// The checkout of a repository chosen on this Mac, which the Files page's
// Git views read (repository_checkout.go).
export type RepositoryCheckout = {
  repository_id: string; name?: string; path?: string | null; status: "unset" | "set" | "unavailable" | string; reason?: string | null; candidates?: string[];
};
// What a Git view answers with: the checkout's state, the commit it read,
// and a note when it read nothing (the commit or file is not there).
export type FileGitAnswer = {
  project?: FileProject; choices?: FileProject[]; checkout?: { path?: string | null; status: string; reason?: string | null };
  commit?: string; ref?: string; note?: string; path?: string;
};
export type FileContent = FileGitAnswer & { text?: string; truncated?: boolean; binary?: boolean; size?: number };
export type BlameCommit = { author: string; author_at: string; summary: string; sessions: FileHistoryLink[]; read: boolean };
export type FileBlame = FileGitAnswer & { lines?: string[]; groups?: { hash: string; start: number; count: number }[]; commits?: Record<string, BlameCommit> };
export type FileLogCommit = {
  hash: string; parents: string[]; author: string; author_at: string; committed_at?: string; subject: string; body?: string;
  read: boolean; on_mainline?: boolean | null; reverted_by?: string | null; sessions: FileHistoryLink[]; url: string;
};
export type FileLog = FileGitAnswer & { commits?: FileLogCommit[]; more?: boolean; offset?: number };
export type FileDiff = FileGitAnswer & { diff?: string; truncated?: boolean };
export type CommitGit = FileGitAnswer & {
  commit?: FileLogCommit & { files: { path: string; lines_added?: number; lines_removed?: number; binary: boolean }[]; files_truncated?: boolean };
};
// Each directory entry's last commit at the tree's commit, by its name.
export type FileLastCommits = FileGitAnswer & { entries?: Record<string, FileLogCommit>; truncated?: boolean };
// An edit's diff as its call made it, read from the transcript: kind is
// replace, write, patch, delete, listed (no lines) or unread.
export type FileChange = {
  workspace_id: string; title?: string | null; outcome_label?: string; at?: string | null; tool_name?: string | null;
  lines_added?: number | null; lines_removed?: number | null; url: string; session_url: string;
  kind: string; hunks: { header?: string; lines: [string, string][] }[]; truncated?: boolean; note?: string;
};
export type FileChanges = { project?: FileProject; choices?: FileProject[]; path: string; changes: FileChange[]; more?: boolean; offset?: number; note?: string };
export type FileFind = { project?: FileProject; choices?: FileProject[]; files: { path: string; in_tree: boolean; url: string }[]; more?: boolean };

// checkoutText says where Git views read from, or why they cannot yet.
export function checkoutText(checkout: Pick<RepositoryCheckout, "status" | "path" | "reason"> | undefined): string {
  if (!checkout) return "";
  if (checkout.status === "set") return `Reading code, blame and history from ${checkout.path}.`;
  return checkout.reason || "Choose this repository's checkout on this Mac to see its code, blame and history.";
}

// A unified diff, file by file and hunk by hunk, each line with its number
// on the old and new side.
export type DiffLine = { kind: " " | "+" | "-" | "\\"; text: string; old?: number; new?: number };
export type DiffHunk = { header: string; lines: DiffLine[] };
export type DiffFile = { path: string; oldPath?: string; binary: boolean; hunks: DiffHunk[] };

export function parseUnifiedDiff(text: string): DiffFile[] {
  const files: DiffFile[] = [];
  let file: DiffFile | null = null, hunk: DiffHunk | null = null, oldLine = 0, newLine = 0;
  const unquote = (name: string) => name.replace(/^"(.*)"$/, "$1").replace(/^[ab]\//, "");
  for (const line of text.split("\n")) {
    if (line.startsWith("diff --git ")) {
      const names = /^diff --git (?:"?a\/(.+?)"?) (?:"?b\/(.+?)"?)$/.exec(line);
      file = { path: names?.[2] ?? line.slice(11), oldPath: names?.[1], binary: false, hunks: [] };
      files.push(file);
      hunk = null;
      continue;
    }
    if (!file) continue;
    const header = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(.*)$/.exec(line);
    if (header) {
      oldLine = Number(header[1]); newLine = Number(header[2]);
      hunk = { header: line, lines: [] };
      file.hunks.push(hunk);
      continue;
    }
    if (!hunk) {
      if (line.startsWith("Binary files ")) file.binary = true;
      else if (line.startsWith("+++ ") && line !== "+++ /dev/null") file.path = unquote(line.slice(4));
      else if (line.startsWith("rename from ")) file.oldPath = line.slice(12);
      else if (line.startsWith("rename to ")) file.path = line.slice(10);
      continue;
    }
    const kind = line[0];
    if (kind === "+") hunk.lines.push({ kind, text: line.slice(1), new: newLine++ });
    else if (kind === "-") hunk.lines.push({ kind, text: line.slice(1), old: oldLine++ });
    else if (kind === " ") hunk.lines.push({ kind, text: line.slice(1), old: oldLine++, new: newLine++ });
    else if (kind === "\\") hunk.lines.push({ kind: "\\", text: line.slice(1).trim() });
  }
  for (const each of files) if (each.oldPath === each.path) delete each.oldPath;
  return files;
}

// commitsByDay groups commits newest first under the day each was authored,
// as GitHub's history does.
export function commitsByDay<T extends { author_at?: string }>(commits: T[], locale?: string): { day: string; commits: T[] }[] {
  const groups: { day: string; commits: T[] }[] = [];
  for (const commit of commits) {
    const day = commit.author_at ? new Date(commit.author_at).toLocaleDateString(locale, { year: "numeric", month: "short", day: "numeric" }) : "Undated";
    if (groups.at(-1)?.day !== day) groups.push({ day, commits: [] });
    groups.at(-1)!.commits.push(commit);
  }
  return groups;
}

// age is how long ago a time was, as a file list says it.
export function age(value: string | null | undefined, now = Date.now()): string {
  if (!value) return "";
  const seconds = Math.max(0, (now - new Date(value).getTime()) / 1000);
  const units: [number, string][] = [[365 * 86400, "year"], [30 * 86400, "month"], [7 * 86400, "week"], [86400, "day"], [3600, "hour"], [60, "minute"]];
  for (const [size, unit] of units) {
    const count = Math.floor(seconds / size);
    if (count >= 1) return `${count} ${unit}${count === 1 ? "" : "s"} ago`;
  }
  return "just now";
}

// blameRows lays a blame out as runs of lines, each run's commit named on
// its first line, with the file's own line numbers.
export function blameRows(blame: Pick<FileBlame, "lines" | "groups">): { hash: string; start: number; lines: string[] }[] {
  const lines = blame.lines ?? [];
  return (blame.groups ?? []).map(group => ({ hash: group.hash, start: group.start, lines: lines.slice(group.start - 1, group.start - 1 + group.count) }));
}

// changeText says what an edit did, for its header.
export function changeText(change: Pick<FileChange, "kind" | "tool_name" | "lines_added" | "lines_removed">): string {
  const kind: Record<string, string> = { replace: "Replaced lines", write: "Wrote the whole file", patch: "Patched", delete: "Deleted the file", listed: "Changed (lines not recorded)", unread: "Edited" };
  const parts = [kind[change.kind] ?? "Edited"];
  if (change.tool_name) parts.push(`with ${change.tool_name}`);
  if (change.lines_added != null || change.lines_removed != null) parts.push(linesText(change.lines_added, change.lines_removed));
  return parts.join(" · ");
}
