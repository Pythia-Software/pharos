import { safeURL, type Annotation, type Diagnostics, type Screenshot } from "./model";

export interface FeedbackReview {
  annotations: Annotation[];
  sourceIDs: string[];
  diagnostics: Diagnostics | null;
  includeDiagnostics: boolean;
}
// Preserve the exact reviewed annotations (including an empty selection) and
// logs. New annotations can be added later without restoring excluded ones.
export function restoreReview(saved: FeedbackReview | undefined, current: Annotation[]): FeedbackReview {
  const currentIDs = new Set(current.map(a => a.id)), known = new Set(saved?.sourceIDs);
  return {
    annotations: saved ? [...saved.annotations.filter(a => currentIDs.has(a.id)), ...current.filter(a => !known.has(a.id))] : current,
    sourceIDs: [...currentIDs], diagnostics: saved?.diagnostics ?? null,
    includeDiagnostics: saved?.includeDiagnostics ?? false,
  };
}

// The complete mapping boundary between Pharos's local annotation records and
// the reusable upload protocol. Copy-only details never enter an upload.
export type PharosAnnotation = {
  id: string; note: string; label: string; selector: string; text: string; url?: string; include_url?: boolean; include_text?: boolean;
  has_screenshot?: boolean; created_at: string; bounds: Annotation["bounds"]; viewport: Annotation["viewport"];
};
export function annotationForUpload(a: PharosAnnotation, screenshot?: Screenshot): Annotation {
  return { id: a.id, comment: a.note, label: a.label, selector: a.selector,
    text: a.include_text ? a.text : undefined, url: a.include_url && a.url ? safeURL(a.url) : undefined,
    captured_at: a.created_at, bounds: a.bounds, viewport: a.viewport, screenshot };
}
