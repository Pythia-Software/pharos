// File history (docs/projects-and-outcomes.md): the sessions that edited a
// file and, where local Git checked its repository, the commits that changed
// it. The service (/api/file-history) reads stored facts; this module says
// how they read on the page.

export const fileHistoryRoute = "/files";

export type FileHistorySession = {
  workspace_id: string; title?: string; source?: string; first_edit_at?: string; last_edit_at?: string; edits?: number;
  lines_added?: number | null; lines_removed?: number | null; rewrote?: boolean; outcome_label?: string; outcome_why?: string; url: string;
};
export type FileHistoryLink = { workspace_id: string; method: string; confidence: number; counted: boolean };
export type FileHistoryCommit = {
  hash: string; author_at?: string; lines_added?: number; lines_removed?: number; on_mainline?: boolean | null; reverted_by?: string | null; sessions: FileHistoryLink[];
};
export type FileHistoryGit = { status: "checked" | "not_checked" | "not_applicable" | string; reason?: string | null; checked_at?: string; commits_since?: string | null; commits_truncated?: boolean };
export type FileHistoryFile = {
  file_key: string; path: string; repository_id?: string; project?: string | null; project_kind?: string;
  sessions: FileHistorySession[]; sessions_truncated?: boolean; commits: FileHistoryCommit[]; git: FileHistoryGit;
};
export type FileHistory = { path: string; files: FileHistoryFile[]; note: string };

// fileHistoryURL opens a file's history, narrowed to one project when given.
export function fileHistoryURL(path: string, project?: string | null): string {
  const params = new URLSearchParams({ file: path });
  if (project) params.set("file_project", project);
  return `${fileHistoryRoute}?${params}`;
}

// readFileHistoryRoute reads fileHistoryURL back: the file and its project.
export function readFileHistoryRoute(search: URLSearchParams): { file: string; file_project: string } {
  return { file: search.get("file") ?? "", file_project: search.get("file_project") ?? "" };
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

// linkText is how a session is linked to a commit: counted links are the
// session's own; below the counting threshold a link is a candidate.
export function linkText(link: FileHistoryLink): string {
  const method = link.method === "transcript" ? "printed by the session" : link.method === "inferred" ? "inferred from its edits" : link.method;
  return link.counted ? method : `candidate, ${method}`;
}
