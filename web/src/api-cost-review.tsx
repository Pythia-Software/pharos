import React from "react";
import { CategoryCardReview } from "./category-card-review";
import type { CardReviewStudy } from "./card-review-model";
import { apiCostFeedback, apiCostReviewKey, apiCostScope, apiCostTheses } from "./api-cost-review-model";

const study: CardReviewStudy = {
  label: "API cost card review", cardLabel: "Candidate", eyebrow: "CARD STUDIES / 08 · API COST · REVIEW 2", title: "Where the API equivalent goes",
  intro: "Compare period cost trends and repository shares with root-task costs, delegated workloads, and pricing coverage.",
  scopeTitle: "Read cost estimates in context", scope: apiCostScope.replace("Scope: ", ""),
  storageKey: apiCostReviewKey, category: "cost", theses: apiCostTheses, feedback: apiCostFeedback,
};
export function ApiCostReview(props: Omit<React.ComponentProps<typeof CategoryCardReview>, "study">) {
  return <CategoryCardReview {...props} study={study} />;
}
