// Session outcomes (docs/projects-and-outcomes.md): each session's label, in
// order from most to least integrated, and how missing Git facts read. Absence
// is never failure: a fact Git alone could give is not applicable to a folder,
// which is not the same as zero.

export type OutcomeLabel = "landed" | "committed" | "changed_kept" | "changed_recent" | "reworked" | "no_changes" | "unknown";

export const outcomeLabels: { id: OutcomeLabel; label: string; description: string }[] = [
  { id: "landed", label: "Landed", description: "Its linked commits reached mainline (local Git)." },
  { id: "committed", label: "Committed", description: "Committed, not on mainline yet (local Git)." },
  { id: "changed_kept", label: "Changed and kept", description: "Edits whose lines later sessions mostly kept through the rework window." },
  { id: "changed_recent", label: "Changed, too recent", description: "Edits younger than the rework window, so rework cannot be judged yet." },
  { id: "reworked", label: "Reworked", description: "Later sessions removed at least half of the lines it added, or its commits were reverted, within the rework window." },
  { id: "no_changes", label: "No changes", description: "The session changed no files: research, review and questions are normal." },
  { id: "unknown", label: "Unknown", description: "Not enough evidence, such as a source that records no tool calls." },
];

const byId = new Map(outcomeLabels.map(label => [label.id, label]));

// outcomeDisplay is a stored label as the page shows it; an unrecognized or
// missing value is Unknown, never a blank.
export function outcomeDisplay(value: unknown): { id: OutcomeLabel; label: string; description: string } {
  return byId.get(value as OutcomeLabel) ?? byId.get("unknown")!;
}

export type ProjectKind = "repository" | "folder" | "none";

// gitFactDisplay is a count Git gives (branches, pull requests, linked
// commits) for a session's project. The service leaves it empty when the
// project is not a repository and the session recorded no such Git data: a
// folder reads "Not a repository" and no project "No git data", never zero.
// A repository that local Git could not inspect reads "Not checked", with
// the reason: failing to look is not zero either, and a session that changed
// nothing in its repository has no commits to look for. A count that was
// recorded shows.
// A session's branch: a folder has none to record, and a repository session
// that recorded none says so; neither is a blank.
export function branchDisplay(value: unknown, kind: unknown): { text: string; muted: boolean; title?: string } {
  if (value != null && value !== "") return { text: String(value), muted: false };
  if (kind === "repository") return { text: "No branch", muted: true, title: "The session recorded no branch." };
  return gitFactDisplay(null, kind);
}

export function gitFactDisplay(value: unknown, kind: unknown, check?: unknown, reason?: unknown): { text: string; muted: boolean; title?: string } {
  if (value != null && value !== "") return { text: Number(value).toLocaleString("en-US"), muted: false };
  if (kind === "folder") return { text: "Not a repository", muted: true, title: "This project is a folder; Git facts do not apply." };
  if (kind === "none") return { text: "No git data", muted: true, title: "This session has no working folder." };
  if (check === "not_checked") return { text: "Not checked", muted: true, title: String(reason || "Local Git has not been checked.") };
  if (check === "not_applicable") return { text: "Not applicable", muted: true, title: "The session changed no file in a repository." };
  return { text: "", muted: false };
}
