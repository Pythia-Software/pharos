import { isJsonPath, type AttentionRegistry, type AttentionTarget } from "@pythia-software/query-table-core";

// The page selects the dataset. JSON preserves numeric versus string record
// IDs and quoted JSONPaths without inventing an ambiguous delimiter language.
export type DetailTarget = Omit<AttentionTarget, "dataset"> & { rowId: string | number };
export function parseDetailTarget(reference: string): DetailTarget {
  if (!reference.startsWith("detail:") || reference.length >= 4096) throw new Error("Invalid detail target.");
  const value: unknown = JSON.parse(reference.slice(7));
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("Detail target needs an object.");
  const target = value as Record<string, unknown>;
  if (Object.keys(target).some(key => !["rowId", "field", "path"].includes(key)) ||
    !(typeof target.rowId === "string" && target.rowId.length > 0 || typeof target.rowId === "number" && Number.isFinite(target.rowId)) ||
    target.field !== undefined && (typeof target.field !== "string" || !target.field) ||
    target.path !== undefined && (target.field === undefined || !isJsonPath(target.path))) throw new Error("Invalid record, property or JSONPath identity.");
  return target as DetailTarget;
}
export function detailReference(target: DetailTarget): string {
  const reference = "detail:" + JSON.stringify(target);
  parseDetailTarget(reference);
  return reference;
}
export async function revealQueryDetail(registry: AttentionRegistry, dataset: string, reference: string, signal: AbortSignal) {
  const target = parseDetailTarget(reference);
  return (await registry.resolve({ ...target, dataset }, { signal })).status;
}
