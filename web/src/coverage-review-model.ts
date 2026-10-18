import { createCardReviewModel, type ReviewThesis } from "./card-review-model";
import { carbonDrafts, findingDrafts, machineTimeDrafts, parallelismDrafts, pricingDrafts, type DraftCard } from "./draft-cards";
export { draftContext, type DraftCard } from "./draft-cards";

export const coverageDrafts: DraftCard[] = [...machineTimeDrafts, ...parallelismDrafts, ...carbonDrafts, ...findingDrafts, ...pricingDrafts];

// Reviews 2 and 3 kept or changed every card; review 1 discarded the parallelism summary, carbon in
// everyday terms, carbon by token type, three library-health cards and both
// TL1 cards. Drafts are not collectible, so their loaders were removed.
export const coverageStatus: Record<string, "Kept" | "Revised" | "New"> = {
  "draft-machine-time": "Kept",
  "draft-machine-activities": "Kept",
  "draft-machine-operations": "Kept",
  "draft-machine-call-trend": "New",
  "draft-machine-latency-trend": "New",
  "draft-parallel-days": "Kept",
  "draft-parallel-trend": "New",
  "draft-parallel-hours": "Revised",
  "draft-parallel-repositories": "Kept",
  "draft-carbon-trend": "Kept",
  "draft-carbon-tiers": "Revised",
  "draft-carbon-repositories": "Kept",
  "draft-carbon-models": "Kept",
  "draft-carbon-providers": "Kept",
  "draft-carbon-effort": "Kept",
  "draft-findings-open": "Kept",
  "draft-findings-fixes": "Kept",
  "draft-findings-results": "Kept",
  "draft-library-pricing": "Kept",
};

export const coverageTheses: ReviewThesis[] = [
  { id: "machine-time", title: "Machine time", thesis: "Show how much recorded tool and command time your agents spend, which kinds of work and operations take it, and how call volume and speed change.", question: "Do the three machine-time views work together, or does one say enough?", cards: [
    { id: "draft-machine-time", rationale: "See the overall size and daily shape of the work agents hand to tools, and whether it is growing. ", calculation: "Sum duration_ms of tool calls started in the past N complete local days, by Machine Time activity and day. Show six leading activities plus exact Other. The headline compares with the N days before. Overlapping calls add together, so this is machine work, not elapsed time. Calls without a recorded duration count as calls but add no time; the timed share is shown." },
    { id: "draft-machine-activities", rationale: "Find out whether tests, builds, reading or delegation dominate recorded machine time.", calculation: "Sum recorded duration by activity (the same rules as Machine Time: test, build, read & search, edit, git, dependencies, delegation, and named commands or tools). Show five activities plus exact Other, with each one's share of total time, call count and mean per timed call on hover." },
    { id: "draft-machine-call-trend", rationale: "New: follow how much agents lean on tools day to day, smoothed so single busy days stand out against the norm.", calculation: "Count every recorded tool call by local start day, including errors and calls without a duration. Each of 31 daily endpoints sums the trailing seven complete days and divides by seven, including idle days; today is excluded until complete. The dashed norm is the mean of the 30 prior endpoints." },
    { id: "draft-machine-latency-trend", rationale: "New: notice when tool calls get slower, for example after a dependency or harness change.", calculation: "For each trailing seven complete days, divide total recorded duration by the number of calls with a recorded duration (pooled, not a mean of daily means). Long waits and watches pull the mean up; Longest-running operations names them. Windows without timed calls are gaps. The dashed norm is the mean of the 30 prior endpoints." },
    { id: "draft-machine-operations", rationale: "Name the specific commands that consume the most machine time, as candidates to speed up or cache.", calculation: "Group by operation (program plus subcommand, or tool and MCP server) and activity, then rank by summed recorded duration. Show the six largest, with call counts, mean per timed call and share of all time on hover. This ranks totals; duration ranges per command remain in the Tool Flow cards." },
  ] },
  { id: "parallelism", title: "Parallel agents", thesis: "Show how often several agents run at once, when that happens, and where. An agent is a workspace or a root session, following your Parallelism setting.", question: "Do the daily shape, time of day and repositories cover what you want to know about parallelism?", cards: [
    { id: "draft-parallel-days", rationale: "See which days you ran several agents in parallel, and how much of each day was single-threaded..", calculation: "For each captured local day, add up the time with exactly one, exactly two, or three or more active units. Stacked bars therefore show the elapsed time with any activity, with three or more on the baseline. The headline divides the time with two or more by all active time. Bars use the snapshot's timezone and day boundaries." },
    { id: "draft-parallel-trend", rationale: "New: track whether you run more agents together over time, as a seven-day average beside its norm.", calculation: "For each captured day with seven captured days behind it, sum agent-hours (units × time) over the trailing seven days and divide by the hours with any unit active. The 14-day snapshot therefore gives eight endpoints; the dashed norm is the mean of the earlier ones. Units follow your Parallelism setting." },
    { id: "draft-parallel-hours", rationale: "Find the hours of the day when agents overlap most, to plan when to start long tasks. Revised: no headline; the busiest hour is noted below the card.", calculation: "For each local hour of the day, sum the area of concurrent units (units × time) over the captured days and divide by the number of days × one hour. A value of 2× means two units on average during that hour, including idle days. Daylight-saving days are stepped in real hours." },
    { id: "draft-parallel-repositories", rationale: "Compare where agent time goes, counting parallel agents separately.", calculation: "For each repository, sum each unit's threaded time (so two workspaces for one hour count as two hours). Hover shows the elapsed time with any of that repository's units active and the resulting mean concurrency. Five repositories plus exact Other." },
  ] },
  { id: "carbon", title: "Carbon impact", thesis: "Bring the Carbon page's estimate to Home: its recent trend, and how it divides by model size, repository, model, provider and effort.", question: "Which breakdowns would you keep beside the trend? Model, model size and provider can overlap.", cards: [
    { id: "draft-carbon-trend", rationale: "Track estimated emissions the same way as human words and API cost: a current period against its recent norm.", calculation: "Fetch daily token counts by model tier and token type from the Carbon endpoint, then apply your Carbon Impact settings at 31 daily endpoints. Each endpoint sums its trailing N local calendar days; the latest includes today so far, matching the other carbon cards. The dashed norm is the mean of the 30 prior endpoints. The sample line converts the latest value to gasoline-car miles." },
    { id: "draft-carbon-tiers", rationale: "See how much of the estimate comes from large models, the biggest lever on energy per token. Revised: a shorter pie.", calculation: "Estimate CO₂e separately for the small, mid-size and large model tiers over the period. Models are assigned to tiers by name; unmatched models use the default tier. Slices are additive parts of the full estimate." },
    { id: "draft-carbon-repositories", rationale: "Attribute the estimate to the repositories that generated the tokens.", calculation: "Request the Carbon series split by repository, sum each repository's daily token counts over the period, and estimate each total separately. Five repositories plus exact Other. Tokens without a usage date cannot be placed in the period and are omitted." },
    { id: "draft-carbon-models", rationale: "Name the individual models behind the estimate, beyond their size tier.", calculation: "Sum each model family's uncached input (including unclassified), cache-write, cache-read and output tokens from Usage aggregations over the same local-calendar period. Place the family in a tier exactly as the Carbon endpoint does, then estimate it with your Carbon Impact settings. Five models plus exact Other; hover shows the tier and token volume." },
    { id: "draft-carbon-providers", rationale: "Compare the estimate across Claude, Codex, Antigravity and other providers.", calculation: "The same per-model estimate, grouped by provider: each provider's tokens are split by model family so each part uses its own tier before summing. Slices are additive parts of the full estimate; up to five providers plus exact Other." },
    { id: "draft-carbon-effort", rationale: "See whether higher reasoning effort accounts for a disproportionate share of the estimate. The dev preview library was indexed before effort tracking, so the card is empty there.", calculation: "The same per-model estimate, grouped by the effort recorded on each usage fact, ordered from lowest to highest effort. Unspecified effort stays its own gray slice. The card is empty when no usage in the period records effort. Effort is read during indexing; libraries indexed before effort tracking fill it in when sources are reparsed, and sources that never recorded it stay unspecified." },
  ] },
  { id: "findings", title: "Optimization findings", thesis: "Surface the Findings tab's biggest opportunities, the kinds of fix they call for, and what verified results have saved.", question: "Do these three give you a reason to open Findings?", cards: [
    { id: "draft-findings-open", rationale: "Put the largest likely savings in front of you without opening Findings.", calculation: "Read open findings from the latest Findings analysis, rank them by likely monthly saving in your Findings unit (dollars by default), and show the top three. Likely saving = cost at stake × removable share × how often a change takes × how often the pattern persists. The headline sums likely savings across all open findings." },
    { id: "draft-findings-fixes", rationale: "See where the changes with the most likely savings would be made.", calculation: "Group open findings by the lever of the change their prompt makes: repository instructions, global instructions, repository tools and skills, harness settings, or automation prompts. Sum each group's likely monthly saving in your Findings unit; each finding counts once. Hover shows the number of findings. The Findings API now includes each card's lever; against an older service the card falls back to the step wording without its article." },
    { id: "draft-findings-results", rationale: "Show whether copied prompts are paying off, as one net amount.", calculation: "From the Findings summary: dollars saved by verified results minus the added context cost of the changes. Hover shows both parts; the footnote gives the number of weeks measured." },
  ] },
  { id: "pricing", title: "Pricing", thesis: "Name the models whose prices are missing or assumed, so cost cards can be trusted.", question: "Should this appear only when some models need prices?", cards: [
    { id: "draft-library-pricing", rationale: "Name the models whose prices are missing or assumed, so cost cards can be trusted.", calculation: "List unpriced and assumed models by all-time recorded tokens. The headline is the share of all tokens that are unpriced, assumed, or only partially priced. Assumed models borrow a sibling model's price; partial facts are lower bounds. This names models, unlike the token-weighted Pricing coverage card." },
  ] },
];

export const coverageReviewKey = "pharos-coverage-review-v4";
export const coverageScope = "These areas have no Query Table fields yet, so each draft card reads the same API as the page it opens (Carbon Impact, Machine Time, Parallelism, Findings, Settings) and is not collectible until a typed card adapter exists. Carbon cards apply your Carbon Impact settings to one local-calendar period; values are modeled estimates from reported tokens. Model, provider and effort breakdowns use Usage aggregations with the Carbon page's tier rules. Machine time sums recorded tool durations, so overlapping calls add up. Parallelism reads the dev timing snapshot (the production service has no timing endpoint yet) and uses the latest captured days. Findings and pricing cards show current state, not a period. Earlier reviews' drafts remain saved separately; feedback typing triggers no requests.";
export const coverageFeedback = createCardReviewModel(coverageTheses, "Uncovered areas — draft cards, review 4", coverageScope);
