import React from "react";
import { CategoryCardReview } from "./category-card-review";
import type { CardReviewStudy } from "./card-review-model";
import { delegationFeedback, delegationReviewKey, delegationScope, delegationTheses } from "./delegation-review-model";

const study: CardReviewStudy = {
  label: "Delegation card review", cardLabel: "Approved", eyebrow: "CARD STUDIES / 10 · DELEGATION · REVIEW 2 · APPROVED", title: "How agents divide the work",
  intro: "Compare delegated token share with task participation, then inspect the depth and own-token workloads of active delegates.",
  scopeTitle: "Read delegation activity in context", scope: delegationScope.replace("Scope: ", ""), storageKey: delegationReviewKey,
  category: "delegation", theses: delegationTheses, feedback: delegationFeedback,
};
export function DelegationReview(props: Omit<React.ComponentProps<typeof CategoryCardReview>, "study">) {
  return <CategoryCardReview {...props} study={study} />;
}
