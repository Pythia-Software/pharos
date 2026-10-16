import React from "react";
import { CategoryCardReview } from "./category-card-review";
import type { CardReviewStudy } from "./card-review-model";
import { toolFlowFeedback, toolFlowReviewKey, toolFlowScope, toolFlowTheses } from "./tool-flow-review-model";

const study: CardReviewStudy = {
  label: "Tool flow card review", cardLabel: "Candidate", eyebrow: "CARD STUDIES / 05 · TOOL FLOW · REVIEW 2", title: "How tool work unfolds",
  intro: "Eight views of tool activity, call duration ranges, and Pharos MCP requests. General tool cards start at seven days; the separate Pharos MCP window starts at 30 days to include less frequent activity.",
  scopeTitle: "Read call time in context", scope: toolFlowScope.replace("Scope: ", ""),
  secondaryWindow: { param: "pharos_review_days", label: "Pharos MCP window", defaultDays: 30, cardIds: ["mcp-response-size", "pharos-mcp-sources", "pharos-mcp-query-sources", "pharos-mcp-durations"] }, storageKey: toolFlowReviewKey, category: "tools", theses: toolFlowTheses, feedback: toolFlowFeedback,
};
export function ToolFlowReview(props: Omit<React.ComponentProps<typeof CategoryCardReview>, "study">) {
  return <CategoryCardReview {...props} study={study} />;
}
