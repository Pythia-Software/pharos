import { safeURL, type Annotation, type Diagnostics, type Screenshot, type Submission } from "./model";

// Older local receipts stored only id/email. They must not borrow report text
// from the original proposal when the actual submitted text is unavailable.
export type FeedbackReceipt = Pick<Submission, "id" | "email"> & Partial<Pick<Submission, "kind" | "comment">>;
export type AskFeedbackOutcome = { status: "sent"; receipt: FeedbackReceipt } | { status: "discarded" };
export type PreparedAskFeedback = AskFeedbackOutcome | { status: "pending"; submission: Submission };
export function feedbackReceipt(submission: Submission): FeedbackReceipt {
  return { id: submission.id, email: submission.email, kind: submission.kind, comment: submission.comment };
}

// A send belongs to the proposal, not to a mounted editor. Remounts observe
// the same operation; after completion, prepare reconciles persisted outcomes.
export function createAskFeedbackSender(transport: {
  prepare: (id: string, payload: Omit<Submission, "id">) => Promise<PreparedAskFeedback>;
  submit: (submission: Submission) => Promise<void>;
  complete: (id: string, submission: Submission) => Promise<void>;
}) {
  const active = new Map<string, Promise<AskFeedbackOutcome>>();
  return {
    pending: (id: string) => active.get(id),
    send(id: string, payload: Omit<Submission, "id">): Promise<AskFeedbackOutcome> {
      const current = active.get(id);
      if (current) return current;
      const operation = (async (): Promise<AskFeedbackOutcome> => {
        const prepared = await transport.prepare(id, payload);
        if (prepared.status !== "pending") return prepared;
        await transport.submit(prepared.submission);
        await transport.complete(id, prepared.submission);
        return { status: "sent", receipt: feedbackReceipt(prepared.submission) };
      })();
      active.set(id, operation);
      const clear = () => { if (active.get(id) === operation) active.delete(id); };
      void operation.then(clear, clear);
      return operation;
    },
  };
}

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
