import React from "react";
import { CategoryCardReview } from "./category-card-review";
import type { CardReviewStudy } from "./card-review-model";
import { skillsInstructionsFeedback, skillsInstructionsReviewKey, skillsInstructionsScope, skillsInstructionsTheses } from "./skills-instructions-review-model";

const study: CardReviewStudy = {
  label: "Skills and instructions card review", cardLabel: "Kept", eyebrow: "CARD STUDIES / 06 · SKILLS AND INSTRUCTIONS · REVIEW 2", title: "How skills enter your work",
  intro: "Four views of skill reach, retained body sizes, and per-conversation file loading. Activity starts at seven days; body sizes start at 30 days to include sparse size evidence.",
  scopeTitle: "Read skill evidence in context", scope: skillsInstructionsScope.replace("Scope: ", ""),
  storageKey: skillsInstructionsReviewKey, category: "skills", theses: skillsInstructionsTheses, feedback: skillsInstructionsFeedback,
  secondaryWindow: { param: "skill_body_days", label: "Skill body window", defaultDays: 30, cardIds: ["skill-body-sizes"] },
};
export function SkillsInstructionsReview(props: Omit<React.ComponentProps<typeof CategoryCardReview>, "study">) {
  return <CategoryCardReview {...props} study={study} />;
}
