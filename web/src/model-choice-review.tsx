import React from "react";
import { CategoryCardReview } from "./category-card-review";
import type { CardReviewStudy } from "./card-review-model";
import { modelChoiceFeedback, modelChoiceReviewKey, modelChoiceTheses } from "./model-choice-review-model";

const study: CardReviewStudy = {
  label: "Model choice card review", cardLabel: "Revised", eyebrow: "CARD STUDIES / 02 · MODEL CHOICE · REVIEW 6", title: "Model choice, in context",
  intro: "Fourteen views of model use, reasoning effort, conversation costs, and delegation. Compare supported cohort ranges, compare singleton, parent, and subagent roles, find the largest delegation tree, and explore human input versus output.",
  scopeTitle: "How to read these comparisons",
  scope: "Tokens include recorded cached input and reflect usage facts placed at their first recorded usage time. The conversation pie counts model/conversation pairings: conversations using multiple models count under each. API equivalent is not subscription spend. Cost comparisons use fully priced facts and cover the priced portion inside this window. Sessions here are whole top-level conversations including recorded delegated agents, linked command launches, and continuations. Each conversation belongs once to its top-level recorded model/effort cohort, even if children use other configurations. Missing or cyclic parent chains are excluded from session comparisons. Model and effort shares use the configurations that actually consumed the usage. Delegation boxes exclude zero-delegation conversations and show classes with three or more delegating conversations. Role bars split active agent sessions into singleton roots, parent roots, and subagents, using recorded delegation across all time; cost boxes require five. Human input tokens are estimates of human-supplied text, counted once per classified message. Different models receive different work; usage and cost do not establish quality.",
  storageKey: modelChoiceReviewKey, category: "models", theses: modelChoiceTheses, feedback: modelChoiceFeedback,
};
export function ModelChoiceReview(props: Omit<React.ComponentProps<typeof CategoryCardReview>, "study">) {
  return <CategoryCardReview {...props} study={study} />;
}
