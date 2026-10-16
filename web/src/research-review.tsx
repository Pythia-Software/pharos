import React from "react";
import { CategoryCardReview } from "./category-card-review";
import type { CardReviewStudy } from "./card-review-model";
import { researchFeedback, researchReviewKey, researchScope, researchTheses } from "./research-review-model";

const study: CardReviewStudy = {
  label: "Research card review", cardLabel: "Kept", eyebrow: "CARD STUDIES / 07 · RESEARCH · REVIEW 2", title: "Where agents look for answers",
  intro: "Four views of conversation search counts, frequent sites, shared URLs, and Pharos empty-result rates. Web activity starts at seven days; archive searches start at 30 days.",
  scopeTitle: "Read research activity in context", scope: researchScope.replace("Scope: ", ""),
  storageKey: researchReviewKey, category: "research", theses: researchTheses, feedback: researchFeedback,
  secondaryWindow: { param: "archive_review_days", label: "Archive search window", defaultDays: 30, cardIds: ["archive-empty-search-rate"] },
};
export function ResearchReview(props: Omit<React.ComponentProps<typeof CategoryCardReview>, "study">) {
  return <CategoryCardReview {...props} study={study} />;
}
