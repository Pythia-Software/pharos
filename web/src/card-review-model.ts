import type { QueryCategory } from "./query-categories";
export type ReviewThesis = { id: string; title: string; thesis: string; question: string; cards: { id: string; rationale: string; calculation: string }[] };
export type CardVerdict = "keep" | "change" | "drop";
export type ReviewFeedback = { overall: string; theses: Record<string, string>; cards: Record<string, { verdict?: CardVerdict; note: string }> };
const emptyReviewFeedback = (): ReviewFeedback => ({ overall: "", theses: {}, cards: {} });
export function createCardReviewModel(theses: ReviewThesis[], reportTitle: string, scope: string) {
  function readReviewFeedback(value: string | null): ReviewFeedback {
    try {
      const parsed = JSON.parse(value ?? "null"), next = emptyReviewFeedback();
      if (!parsed || typeof parsed !== "object") return next;
      if (typeof parsed.overall === "string") next.overall = parsed.overall;
      for (const thesis of theses) {
        if (typeof parsed.theses?.[thesis.id] === "string") next.theses[thesis.id] = parsed.theses[thesis.id];
        for (const card of thesis.cards) {
          const item = parsed.cards?.[card.id];
          if (item && typeof item === "object") next.cards[card.id] = { note: typeof item.note === "string" ? item.note : "", ...(["keep", "change", "drop"].includes(item.verdict) ? { verdict: item.verdict } : {}) };
        }
      }
      return next;
    } catch { return emptyReviewFeedback(); }
  }
  function reviewedCardCount(feedback: ReviewFeedback): number {
    return theses.flatMap(thesis => thesis.cards).filter(card => feedback.cards[card.id]?.verdict || feedback.cards[card.id]?.note.trim()).length;
  }
  function exportFeedback(feedback: ReviewFeedback, days: number, titles: Record<string, string> = {}, cardWindows: Record<string, number> = {}): string {
    const lines = [`# ${reportTitle}`, "", `Preview window at export: past ${days} days (rolling).`, scope, ""];
    const overrides = Object.entries(cardWindows).filter(([, window]) => window !== days);
    if (overrides.length) lines.push("Additional card windows:", ...overrides.map(([id, window]) => `- ${titles[id] ?? id} (${id}): past ${window} days (rolling).`), "");
    if (feedback.overall.trim()) lines.push("## Feedback on the set", "", feedback.overall.trim(), "");
    for (const thesis of theses) {
      const reviewed = thesis.cards.filter(card => feedback.cards[card.id]?.verdict || feedback.cards[card.id]?.note.trim());
      const note = feedback.theses[thesis.id]?.trim();
      if (!note && !reviewed.length) continue;
      lines.push(`## ${thesis.title}`, "", `Thesis: ${thesis.thesis}`, "");
      if (note) lines.push(note, "");
      for (const card of reviewed) {
        const item = feedback.cards[card.id];
        lines.push(`### ${titles[card.id] ?? card.id} (${card.id})`, "");
        if (cardWindows[card.id] !== undefined && cardWindows[card.id] !== days) lines.push(`Preview window: past ${cardWindows[card.id]} days (rolling).`, "");
        if (item.verdict) lines.push(`Verdict: ${item.verdict}`, "");
        if (item.note.trim()) lines.push(item.note.trim(), "");
      }
    }
    if (!feedback.overall.trim() && !Object.values(feedback.theses).some(note => note.trim()) && !reviewedCardCount(feedback)) lines.push("No feedback entered yet.", "");
    return lines.join("\n");
  }
  return { readReviewFeedback, reviewedCardCount, exportFeedback };
}
export type CardReviewStudy = {
  label: string; cardLabel?: string; eyebrow: string; title: string; intro: string; scopeTitle: string; scope: string;
  storageKey: string; category: QueryCategory; theses: ReviewThesis[];
  secondaryWindow?: { param: string; label: string; cardIds: string[]; defaultDays: 1 | 7 | 30 | 90 };
  feedback: ReturnType<typeof createCardReviewModel>;
};
