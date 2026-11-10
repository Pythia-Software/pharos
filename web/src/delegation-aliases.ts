// Saved explorer links name sessions by IDs that an execution identity upgrade
// can replace. The server resolves the IDs a URL mentions through its source
// bindings: to the canonical row ID (itself for a current row), or to null
// when the old ID no longer identifies one execution (split) or cannot be
// resolved. Normalizing moves
// resolved state onto its execution and leaves unresolved and unrelated state
// exactly as it was; it never applies an unresolved ID to a chosen execution.
import { readDelegationRoute } from "./delegation-route";
import { encodeViewValue, parseViewValue, viewFields } from "./view-contract";

export type DelegationAliases = Record<string, string | null>;
const disclosurePrefix = "delegation-labels:";
const requestLimit = 200;

function viewValue<T>(params: URLSearchParams, name: string, fallback: T): T {
  const raw = params.get(viewFields[name].param);
  if (raw == null) return fallback;
  try { return parseViewValue(viewFields[name], raw) as T; } catch { return fallback; }
}
// A disclosure key is delegation-labels:<session ID>:<ribbon node>. Session
// IDs can contain colons, so every prefix is a candidate; the server answers
// for each, and the longest prefix it resolves names the session.
function disclosureSessions(key: string): string[] {
  if (!key.startsWith(disclosurePrefix)) return [];
  const parts = key.slice(disclosurePrefix.length).split(":");
  return parts.slice(0, -1).map((_, i) => parts.slice(0, i + 1).join(":"));
}

/** Session IDs the URL's explorer state names, for the server to resolve. */
export function delegationAliasRequests(params: URLSearchParams): string[] {
  const route = readDelegationRoute(params);
  const ids = new Set([route.selected, ...route.collapsed, ...viewValue<string[]>(params, "delegation_expanded", []), ...viewValue<string[]>(params, "delegation_relatives", [])]);
  for (const key of Object.keys(viewValue<Record<string, boolean>>(params, "disclosures", {}))) for (const id of disclosureSessions(key)) ids.add(id);
  ids.delete("");
  return [...ids].sort().slice(0, requestLimit);
}

/** The execution an ID resolves to; unresolved and unknown IDs stay as they are. */
export const resolveSessionAlias = (id: string, aliases: DelegationAliases) => aliases[id] ?? id;

function normalizeIDs(ids: string[], aliases: DelegationAliases) {
  return [...new Set(ids.map(id => resolveSessionAlias(id, aliases)))].sort();
}

/**
 * Session-derived disclosure keys move to their execution. An explicit value
 * for the canonical key wins over aliased values; aliases that disagree
 * without one close the disclosure.
 */
export function normalizeDisclosureAliases(disclosures: Record<string, boolean>, aliases: DelegationAliases): Record<string, boolean> {
  const next: Record<string, boolean> = {}, moved = new Map<string, boolean[]>();
  for (const [key, open] of Object.entries(disclosures)) {
    // A current session whose ID extends a moved one keeps its own key.
    const from = disclosureSessions(key).reverse().find(id => typeof aliases[id] === "string");
    if (from === undefined || aliases[from] === from) { next[key] = open; continue; }
    const target = `${disclosurePrefix}${aliases[from]}${key.slice(disclosurePrefix.length + from.length)}`;
    moved.set(target, [...moved.get(target) ?? [], open]);
  }
  for (const [key, values] of moved) if (!Object.hasOwn(disclosures, key)) next[key] = values.every(Boolean);
  return next;
}

/**
 * Raw URL values for the explorer parameters an alias upgrade changes, or
 * null when none change. Selection, folded sessions, expanded lanes, loaded
 * relatives and session disclosures all use the same resolution.
 */
export function normalizeDelegationView(params: URLSearchParams, aliases: DelegationAliases): Record<string, string> | null {
  const changes: Record<string, string> = {};
  const route = readDelegationRoute(params);
  const selected = resolveSessionAlias(route.selected, aliases);
  if (selected !== route.selected) changes.delegation_session = selected;
  const collapsed = normalizeIDs(route.collapsed, aliases).join(",");
  if (route.collapsed.length && collapsed !== [...route.collapsed].sort().join(",")) changes.delegation_collapsed = collapsed;
  for (const name of ["delegation_expanded", "delegation_relatives"]) {
    const field = viewFields[name], ids = viewValue<string[] | null>(params, name, null);
    if (!ids) continue;
    const raw = encodeViewValue(field, normalizeIDs(ids, aliases));
    if (raw !== encodeViewValue(field, [...ids].sort())) changes[field.param] = raw;
  }
  const disclosuresField = viewFields.disclosures;
  const disclosures = viewValue<Record<string, boolean> | null>(params, "disclosures", null);
  if (disclosures) {
    const next = normalizeDisclosureAliases(disclosures, aliases);
    if (JSON.stringify(next) !== JSON.stringify(disclosures)) changes[disclosuresField.param] = encodeViewValue(disclosuresField, next);
  }
  return Object.keys(changes).length ? changes : null;
}

/** Saved IDs the server reported as naming no current session. */
export const unresolvedAliases = (aliases: DelegationAliases) => Object.keys(aliases).filter(id => aliases[id] === null);

/**
 * The session the reader shows for a transcript. A saved selection the server
 * could not resolve stays visibly unresolved: it is never silently replaced
 * by another of the transcript's sessions, though a transcript with exactly
 * one session still shows that one.
 */
export function readerSelection<T extends { id: string }>(current: readonly T[], selected: string, unresolved: ReadonlySet<string>): { row?: T; unresolved: boolean } {
  const hit = current.find(row => row.id === selected);
  if (hit) return { row: hit, unresolved: false };
  if (selected && unresolved.has(selected)) return { row: current.length === 1 ? current[0] : undefined, unresolved: true };
  return { row: current[0], unresolved: false };
}
