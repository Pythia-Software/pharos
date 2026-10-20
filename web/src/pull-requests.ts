// Links from Library rows to the pull requests and origin/main merges that
// carried their work. Only http(s) URLs are ever linked.
type Row = Record<string, any>;
export type PullRequest = { number: number; title?: string | null; url?: string | null; host?: string | null };

export function githubPullURL(remote: unknown, number: number): string {
  const match = String(remote ?? "").match(/github\.com(?::|\/)([^/\s]+\/[^/\s]+?)(?:\.git)?$/i);
  return match ? `https://github.com/${match[1]}/pull/${number}` : "";
}

export function pullRequests(row: Row): PullRequest[] {
  try {
    const details = typeof row.pr_details === "string" ? JSON.parse(row.pr_details) : row.pr_details;
    return Array.isArray(details) ? details.filter(pr => Number.isInteger(pr?.number) && pr.number > 0) : [];
  } catch { return []; }
}

export function pullRequestURL(pr: PullRequest, remote: unknown): string {
  const url = pr.url || (pr.host === "github.com" ? githubPullURL(remote, pr.number) : "");
  return /^https?:\/\//i.test(url ?? "") ? String(url) : "";
}

export function pullRequestLabel(pr: PullRequest): string {
  return pr.title?.trim() ? `${pr.title.trim()} · #${pr.number}` : `PR #${pr.number}`;
}

// The merge commit that brought the work onto origin/main, linked only to GitHub.
export function mainIntegration(row: Row): { label: string; url: string; commit: string } | undefined {
  if (!row.main_merge_commit) return undefined;
  const prefix = row.main_merge_method === "merge" ? "Merged" : "On main";
  const label = `${prefix}: ${row.main_merge_title || String(row.main_merge_commit).slice(0, 12)}`;
  const url = /^https:\/\/github\.com\//i.test(String(row.main_merge_url ?? "")) ? String(row.main_merge_url) : "";
  return { label, url, commit: String(row.main_merge_commit) };
}
