import React from "react";
import { CategoryCardReview } from "./category-card-review";
import type { CardReviewStudy } from "./card-review-model";
import { errorPatternFeedback, errorPatternReviewKey, errorPatternScope, errorPatternTheses } from "./error-pattern-review-model";

const study: CardReviewStudy = {
  label: "Error patterns card review", cardLabel: "Kept", eyebrow: "CARD STUDIES / 04 · ERROR PATTERNS · REVIEW 2", title: "Where work hits friction",
  intro: "Nine views of recorded errors, recurring diagnostics, and conversation outliers. Compare volume with rates, broad patterns with local hotspots, and edit failures with test evidence.",
  scopeTitle: "Start with the recorded evidence", scope: errorPatternScope.replace("Scope: ", ""),
  storageKey: errorPatternReviewKey, category: "errors", theses: errorPatternTheses, feedback: errorPatternFeedback,
};
export function ErrorPatternReview(props: Omit<React.ComponentProps<typeof CategoryCardReview>, "study">) {
  return <CategoryCardReview {...props} study={study} />;
}
