import React from "react";
import { CategoryCardReview } from "./category-card-review";
import type { CardReviewStudy } from "./card-review-model";
import { contextCachingFeedback, contextCachingReviewKey, contextCachingScope, contextCachingTheses } from "./context-caching-review-model";

const study: CardReviewStudy = {
  label: "Context and caching card review", cardLabel: "Kept", eyebrow: "CARD STUDIES / 03 · CONTEXT & CACHING · REVIEW 2", title: "What fills your context",
  intro: "Seven views of cache reuse, tool context additions, and reading patterns. Compare models with repositories, typical result sizes with outliers, and popular paths with rereads.",
  scopeTitle: "Two views of context", scope: contextCachingScope.replace("Scope: ", ""),
  storageKey: contextCachingReviewKey, category: "context", theses: contextCachingTheses, feedback: contextCachingFeedback,
};
export function ContextCachingReview(props: Omit<React.ComponentProps<typeof CategoryCardReview>, "study">) {
  return <CategoryCardReview {...props} study={study} />;
}
