import React from "react";
import { CategoryCardReview } from "./category-card-review";
import type { CardReviewStudy } from "./card-review-model";
import { tokenUsageFeedback, tokenUsageReviewKey, tokenUsageScope, tokenUsageTheses } from "./token-usage-review-model";

const study: CardReviewStudy = {
  label: "Token usage card review", cardLabel: "Approved", eyebrow: "CARD STUDIES / 09 · TOKEN USAGE · REVIEW 3 · APPROVED", title: "Where token volume goes",
  intro: "Compare whole-task token distributions across models and effort, then separate the footprints of standalone, parent, and delegated runs.",
  scopeTitle: "Read token volume in context", scope: tokenUsageScope.replace("Scope: ", ""), storageKey: tokenUsageReviewKey,
  category: "usage", theses: tokenUsageTheses, feedback: tokenUsageFeedback,
};
export function TokenUsageReview(props: Omit<React.ComponentProps<typeof CategoryCardReview>, "study">) {
  return <CategoryCardReview {...props} study={study} />;
}
