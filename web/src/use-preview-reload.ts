import { useCallback, useEffect, useRef, type DependencyList, type Dispatch, type SetStateAction } from "react";
import { clearPreviewCache, loadQueryPreviews, type PreviewJob, type QueryPreview } from "./query-previews";

// Retry one card without disturbing healthy previews. A change of query scope
// cancels pending retries so old results cannot replace the new view's cards.
export function usePreviewReload(
  setPreviews: Dispatch<SetStateAction<Record<string, QueryPreview>>>,
  setErrors: Dispatch<SetStateAction<Record<string, string>>>,
  scope: DependencyList,
) {
  const pending = useRef(new Map<string, AbortController>());
  useEffect(() => () => {
    for (const controller of pending.current.values()) controller.abort();
    pending.current.clear();
  }, scope);

  return useCallback((job: PreviewJob) => {
    if (pending.current.has(job.id)) return;
    const controller = new AbortController(); pending.current.set(job.id, controller);
    clearPreviewCache();
    setPreviews(current => { const next = { ...current }; delete next[job.id]; return next; });
    setErrors(current => { const next = { ...current }; delete next[job.id]; return next; });
    void loadQueryPreviews([job], (id, preview, error) => {
      if (controller.signal.aborted) return;
      if (preview) setPreviews(current => ({ ...current, [id]: preview }));
      if (error) setErrors(current => ({ ...current, [id]: String(error) }));
    }, controller.signal).finally(() => {
      if (pending.current.get(job.id) === controller) pending.current.delete(job.id);
    });
  }, [setPreviews, setErrors]);
}
