import type { ProjectColor } from "./project-colors";

export const projectColorSorts = ["conversations", "latest", "name"] as const;
export type ProjectColorSort = typeof projectColorSorts[number];
/** Most-used and most recent first: the projects people are likely to recolor. Ties fall back to name, then ID. */
export function sortProjectColors(projects: readonly ProjectColor[], sort: ProjectColorSort): ProjectColor[] {
  const byName = (a: ProjectColor, b: ProjectColor) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id);
  const compare = sort === "conversations" ? (a: ProjectColor, b: ProjectColor) => (b.conversations ?? 0) - (a.conversations ?? 0)
    : sort === "latest" ? (a: ProjectColor, b: ProjectColor) => (Date.parse(b.latest_conversation_at ?? "") || 0) - (Date.parse(a.latest_conversation_at ?? "") || 0)
    : () => 0;
  return [...projects].sort((a, b) => compare(a, b) || byName(a, b));
}
