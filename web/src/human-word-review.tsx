import React from "react";
import { CategoryCardReview } from "./category-card-review";
import type { CardReviewStudy } from "./card-review-model";
import { exportHumanWordFeedback, humanWordReviewKey, humanWordTheses, readReviewFeedback, reviewedCardCount } from "./human-word-review-model";

const study: CardReviewStudy = {
  label: "Human word card review", eyebrow: "CARD STUDIES / 01 · HUMAN WORDS · REVIEW 3", title: "Your contribution, in context",
  intro: "Twelve views of human writing, its distribution, and the feedback and context you supply. Review the metrics individually and as a set.",
  scopeTitle: "What counts as your words?",
  scope: "Text classified as typed or dictated. Pastes, copied agent output, and harness instructions are counted separately. Current values use messages sent in the selected window. Sparklines add 30 historical daily endpoints. Contribution studies also include supplied pastes and attachments, with their denominators shown. Writing is one signal of effort; these views do not measure time, success, or productivity.",
  storageKey: humanWordReviewKey, category: "human", theses: humanWordTheses,
  feedback: { readReviewFeedback, reviewedCardCount, exportFeedback: exportHumanWordFeedback },
};
export function HumanWordReview(props: Omit<React.ComponentProps<typeof CategoryCardReview>, "study">) {
  return <CategoryCardReview {...props} study={study} />;
}
