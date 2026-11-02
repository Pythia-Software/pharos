import { queryRequest } from "./query-request";

type UsageHistory = { oldest_conversation_at: string | null };
let cached: UsageHistory | null = null, pending: Promise<UsageHistory> | null = null;

// Mounted charts share an in-flight read. Reload on mount or usage refresh
// so a newly imported older conversation can change the next preset choice.
export function loadUsageHistory(refresh = false): Promise<UsageHistory> {
  if (pending) return pending;
  if (cached && !refresh) return Promise.resolve(cached);
  pending = queryRequest<UsageHistory>("/api/usage/history").then(history => {
    cached = history;
    return history;
  }).finally(() => { pending = null; });
  return pending;
}
