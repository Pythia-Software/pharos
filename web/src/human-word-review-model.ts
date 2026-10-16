import { createCardReviewModel, type ReviewThesis } from "./card-review-model";
export const humanWordTheses: ReviewThesis[] = [
  { id: "allocation", title: "Writing volume in context", thesis: "Compare current writing with your own recent norm, then see how it is distributed across days and repositories.", question: "Does the historical norm make the current total easier to interpret?", cards: [
    { id: "human-word-total", rationale: "Compare your current writing volume with your recent habits.", calculation: "Current trailing-window word total. The norm averages the same trailing-window total at each of the prior 30 daily endpoints, including zero-writing windows." },
    { id: "human-words", rationale: "See the day-to-day distribution of your human writing.", calculation: "Sum of your words by recorded day, for messages sent during the trailing window." },
    { id: "explaining-repos", rationale: "Locate the repositories receiving most of your written attention.", calculation: "Each slice is the repository’s human-written words divided by the total across all repositories. Seven repositories are named; Other combines the rest, including any exact query remainder. The destination contains all repositories." },
  ] },
  { id: "depth", title: "Explanation depth and its trend", thesis: "Show how much writing each message, conversation, or workspace receives, and whether that pattern is changing.", question: "Do the sparklines add useful context? Does the directory grouping match your notion of a workspace?", cards: [
    { id: "human-words-per-message", rationale: "Track how much explanation an average human-written message contains.", calculation: "At each daily endpoint: window words ÷ messages containing your writing. Ratios are computed from totals, not averaged from message averages." },
    { id: "human-words-per-conversation", rationale: "Track the human writing contributed to an average conversation.", calculation: "At each daily endpoint: window words ÷ distinct conversations you wrote in. Conversations may have started before the window." },
    { id: "human-words-per-work", rationale: "Compare your writing across workspace directories, combining conversations that share a directory.", calculation: "Join each message’s work ID to its Library location, group by exact repository + directory, then divide window words by directory count. Missing mappings are excluded and disclosed." },
    { id: "human-repo-conversation-depth", rationale: "Compare average conversation writing across repositories, with sample sizes.", calculation: "Per repository: your words ÷ distinct conversations you wrote in." },
  ] },
  { id: "shape", title: "Distribution, authorship, and timing", thesis: "Make the shape of writing visible: conversation sizes, the balance of authored and supplied words, and the hours when you contribute.", question: "Are these charts now readable, and are the units what you expected?", cards: [
    { id: "human-message-lengths", rationale: "Compare conversation sizes without a long outlier compressing the rest.", calculation: "Count conversations in fixed bands of window-incurred human words: 1–49, 50–199, 200–499, 500–999, 1,000–1,999, and 2,000+. Each bar is a share of conversations." },
    { id: "human-word-mix", rationale: "Compare your own writing with supplied and automated message text.", calculation: "Per repository: your words ÷ all retained user-message words, including automated text. The eight leading repositories are shown; the table contains all results." },
    { id: "writing-hours", rationale: "See which parts of the day account for your human writing.", calculation: "Sum of your words by hour of day in this device’s timezone, across all dates in the window. Empty hours are zero." },
  ] },
  { id: "contribution", title: "Evidence of substantive contribution", thesis: "Inspect two focused signals of what a person adds: feedback with concrete detail and supplied examples or context. Read the excerpts to judge the signals.", question: "Do these signals distinguish substantive input from nudging? Which examples are misclassified or missing?", cards: [
    { id: "human-specific-feedback", rationale: "Track feedback with concrete detail and inspect the messages behind the signal.", calculation: "At each daily endpoint, divide matching messages by assessed authored messages over the trailing period. Match a direction/correction/constraint cue plus concrete detail. Empty denominators remain gaps." },
    { id: "human-example-phrases", rationale: "Inspect the kinds of examples and context you supply.", calculation: "Share of assessed human contributions with each supply format. Context-only messages count; categories overlap. Percentages refer to contributions, not inferred code/file changes." },
  ] },
];

export type { CardVerdict, ReviewFeedback } from "./card-review-model";
export { emptyReviewFeedback } from "./card-review-model";
export const humanWordReviewKey = "pharos-human-word-review-v3";
export const { readReviewFeedback, reviewedCardCount, exportFeedback: exportHumanWordFeedback } = createCardReviewModel(humanWordTheses, "Human word cards — review 3", "Scope: classified human writing, supplied context, and inspectable contribution cues; these are not measured time or quality scores.");
