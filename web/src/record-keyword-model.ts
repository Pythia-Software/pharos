import type { FieldDef } from "@pythia-software/query-table-core";
import { emptyTextSearch, readTextSearch, searchRequest, type TextSearchDraft } from "./text-search-model";

export type RecordKeywordSearch = { field: string; draft: TextSearchDraft };
export const recordKeywordFields = (fields: FieldDef[]) => fields.filter(field => ["text", "enum"].includes(field.type) && field.filter?.enabled !== false && field.source?.kind !== "derived");
export const recordKeywordParameter = (dataset: string) => `record_${dataset}`;
export function readRecordKeywordSearch(token: string | null, fields: FieldDef[]): { value: RecordKeywordSearch | null; error: string } {
  if (!token) return { value: null, error: "" };
  try {
    if (token.length > 28000) throw new Error();
    const stored = JSON.parse(token);
    if (!recordKeywordFields(fields).some(field => field.name === stored.field)) throw new Error();
    const restored = readTextSearch(new URLSearchParams({ text_builder: JSON.stringify(stored.draft) }));
    if (restored.error || !restored.active || restored.draft.fuzzy || JSON.stringify(restored.draft.scope) !== JSON.stringify(emptyTextSearch().scope)) throw new Error();
    return { value: { field: stored.field, draft: restored.draft }, error: "" };
  } catch { return { value: null, error: "This record keyword filter could not be read. Edit it or remove it before running the query." }; }
}
export function recordKeywordRequest(search: RecordKeywordSearch): string {
  if (JSON.stringify(search).length > 28000) throw new Error("This keyword filter is too large. Remove some conditions.");
  const { request } = searchRequest(search.draft);
  if (!request || request.fuzzy) throw new Error("Invalid record keyword conditions");
  const { where, query, mode, case_sensitive, separators } = request;
  return JSON.stringify({ field: search.field, ...(where ? { where } : { query, mode }), case_sensitive, separators });
}
export const freshRecordKeywordSearch = (field: string): RecordKeywordSearch => ({ field, draft: emptyTextSearch() });
