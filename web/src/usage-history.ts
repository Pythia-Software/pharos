import { queryRequest } from "./query-request";

type UsageHistory = { oldest_conversation_at: string | null };
type SharedWorks = Array<{ conversations?: Array<{ started_at?: string | null }> }>;
let cached: UsageHistory | null = null, pending: Promise<UsageHistory> | null = null;

// Shared exports carry conversation dates and must stay usable offline.
export function sharedUsageHistory(works: SharedWorks): UsageHistory {
  let oldest: string | null = null;
  for (const work of works) for (const conversation of work.conversations ?? []) {
    const at = conversation.started_at;
    if (at && Number.isFinite(Date.parse(at)) && (!oldest || Date.parse(at) < Date.parse(oldest))) oldest = at;
  }
  return { oldest_conversation_at: oldest };
}

// Mounted charts share an in-flight read. Reload on mount or usage refresh
// so a newly imported older conversation can change the next preset choice.
export function loadUsageHistory(refresh = false): Promise<UsageHistory> {
  const shared = (window as unknown as { pharosShare?: { works: SharedWorks } }).pharosShare;
  if (shared) return Promise.resolve(sharedUsageHistory(shared.works));
  if (pending) return pending;
  if (cached && !refresh) return Promise.resolve(cached);
  pending = queryRequest<UsageHistory>("/api/usage/history").then(history => {
    cached = history;
    return history;
  }).finally(() => { pending = null; });
  return pending;
}
