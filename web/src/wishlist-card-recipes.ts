import type { AggregationClause, SummaryColumn, WhereClause } from "@pythia-software/query-table-core";
import type { QueryRecipe } from "./curiosity-data";
import type { CardExploration } from "./card-recipes";

const eq = (field: string, value: string): WhereClause => ({ field, op: "=", value });
const gt = (field: string, value = 0): WhereClause => ({ field, op: ">", value: String(value) });
const present = (field: string): WhereClause => ({ field, op: "is_not_null", value: "" });
const min = (field: string, value = 2): WhereClause => ({ field, op: ">=", value: String(value) });
const count = (label = "Records"): SummaryColumn => ({ id: "card_count", op: "count", label });
const sum = (field: string, label: string): SummaryColumn => ({ id: `card_${field}`, op: "sum", field, label });
const distinct = (field: string, label: string): SummaryColumn => ({ id: `card_${field}`, op: "count_distinct", field, label });
const row = (field: string, label: string): SummaryColumn => ({ id: `card_${field}`, op: "row", field, label });
const metric = (id: string, op: AggregationClause["op"], field: string, groupBy: string[], label: string): AggregationClause => ({ id, op, ...(field ? { field } : {}), groupBy, label });
const view = (keys: string[], summaries: SummaryColumn[], subjects: string, action: string, unit: string, extra: Partial<CardExploration> = {}): CardExploration => ({ keys, summaries, valueField: summaries[0].id, subjects, action, unit, ...extra });
const conversations = distinct("conversation_id", "Conversations");
const repositories = distinct("repository_name", "Repositories");
const messages = { dataset: "writing_messages" as const, field: "sent_at" };
const calls = { dataset: "tool_calls" as const, field: "started_at" };
const skills = { dataset: "skill_usages" as const, field: "created_at" };
const attempts = { dataset: "tl1_attempts" as const, field: "started_at" };
const latestMessage = { orderBy: [{ field: "sent_at", dir: "desc" as const, nulls: "last" as const }] };
const humanWords = sum("typed_words", "Your words");

// Narrow observed versions of wishlist questions. Every qualification is part
// of the saved query (including minimum group support), not a cover-only filter.
export const wishlistCardRecipes: QueryRecipe[] = [
  { ...messages, id: "repeated-messages", idea: "conversation-03", category: "steering", title: "Which messages am I repeating?", description: "Exact message text found in at least three conversations, with repository reach.", unit: "conversations",
    where: [{ any: [gt("typed_words"), gt("resent_words")] }, ...["pasted_words", "quoted_words", "template_words", "attachment_words", "harness_words", "automated_words"].map(field => eq(field, "0")), present("text")],
    measures: [metric("message-reach", "count_distinct", "conversation_id", ["text"], "Conversations repeating each message")],
    exploration: view(["text"], [conversations, count("Messages"), repositories], "repeated messages", "Inspect repeated messages", "conversations", { having: [min(conversations.id, 3)], context: [{ field: "card_count", label: "messages" }] }),
    note: "Exact whole-message repeats, including classified re-sends. Excludes pasted, quoted, template and machine words; does not classify instructions or near duplicates." },
  { ...messages, id: "conversation-writing", idea: "conversation-04", category: "human", title: "How many messages am I writing per conversation?", description: "A distribution of messages containing your words in this window; open conversation totals.", unit: "messages", where: [gt("typed_words"), present("conversation_id")],
    measures: [metric("conversation-messages", "count", "", ["repository_name", "conversation_id"], "Human messages per conversation")],
    exploration: view(["repository_name", "conversation_id"], [count("Messages with your words"), humanWords, row("title", "Work")], "conversations", "Compare conversation writing", "messages", { representative: latestMessage, labelFields: ["card_title"], coverMetric: { id: "conversation-message-distribution", op: "sum", field: "card_count", groupBy: [], distribution: { kind: "histogram", input: "[card_count]", bins: 10 }, label: "Messages per conversation in window", display: { kind: "histogram", xLabel: "Messages", yLabel: "Conversations" } } }),
    cover: { mode: "result", metricId: "card_count", kind: "histogram" },
    note: "Counts messages with typed or dictated words, not agent exchanges or complete tasks. Conversations can be unfinished or cross the window boundary." },
  { ...messages, id: "positive-feedback", idea: "conversation-07", category: "steering", title: "Where am I giving positive feedback?", description: "Your messages matching explicit praise or thanks phrases, grouped by conversation.", unit: "messages",
    where: [gt("typed_words"), eq("other_words", "0"), present("conversation_id"), { any: ["great job", "good job", "well done", "thank you", "thanks for", "looks good", "that worked", "this works"].map(value => ({ field: "text", op: "contains" as const, value })) }],
    measures: [metric("feedback-messages", "count", "", ["conversation_id"], "Positive phrase matches")],
    exploration: view(["conversation_id"], [count("Matching messages"), row("title", "Work"), row("text", "Latest match"), row("repository_name", "Repository")], "conversations", "Read feedback matches", "messages", { representative: latestMessage, labelFields: ["card_title"] }),
    note: "Literal phrases in messages classified entirely as your words; may include negation or politeness. Does not establish final acceptance, task completion or correctness." },
  { ...messages, id: "human-action-phrases", idea: "human-06", category: "human", title: "Where do I report taking action?", description: "Your messages matching phrases such as ‘I fixed’ or ‘I ran’, grouped by conversation.", unit: "messages",
    where: [gt("typed_words"), eq("other_words", "0"), present("conversation_id"), { any: ["I fixed", "I've fixed", "I ran", "I've run", "I resolved", "I merged", "I deployed", "I did it myself"].map(value => ({ field: "text", op: "contains" as const, value })) }],
    measures: [metric("human-action-matches", "count", "", ["conversation_id"], "Reported-action phrase matches")],
    exploration: view(["conversation_id"], [count("Matching messages"), row("title", "Work"), row("text", "Latest match"), row("repository_name", "Repository")], "conversations", "Read reported-action matches", "messages", { representative: latestMessage, labelFields: ["card_title"] }),
    note: "Literal phrases in messages classified entirely as your words. Negations and quoted wording can still match; does not verify the action, connect a preceding obstacle or establish who finished the task." },
  { ...messages, id: "human-example-phrases", idea: "human-04", category: "human", title: "Where am I supplying examples?", description: "Your messages with explicit example-introduction phrases, with conversation and repository reach.", unit: "messages",
    where: [gt("typed_words"), eq("other_words", "0"), present("conversation_id"), { any: ["for example", "for instance", "here's an example", "here is an example"].map(value => ({ field: "text", op: "contains" as const, value })) }],
    measures: [metric("human-example-matches", "count", "", ["repository_name"], "Example phrase matches")],
    exploration: view(["repository_name"], [count("Matching messages"), conversations, row("text", "Latest match")], "repositories", "Read example phrase matches", "messages", { representative: latestMessage, context: [{ field: conversations.id, label: "conversations" }] }),
    note: "Literal phrases in messages classified entirely as your words. Opens candidates for inspection; does not extract examples, snippets, acceptance criteria or recurring example families." },
  { ...skills, id: "instruction-bytes", idea: "context-07", category: "skills", title: "Which skill bodies are loaded most heavily?", description: "Recorded body bytes by skill and load evidence, with conversation reach.", unit: "bytes", where: [eq("status", "ok"), gt("content_bytes")],
    measures: [metric("skill-bytes", "sum", "content_bytes", ["skill_name", "evidence_type"], "Retained skill body bytes")],
    exploration: view(["skill_name", "evidence_type"], [sum("content_bytes", "Retained body bytes"), count("Evidence records"), conversations], "skill/load groups", "Compare loaded skill bodies", "bytes", { context: [{ field: "card_count", label: "records" }] }),
    note: "Sums retained bytes on successful load evidence, separating file and harness loads. Unknown sizes are excluded. Bytes are not tokens, context carriage, or instruction compliance." },
  { ...skills, id: "skill-reloads", idea: "skills-05", category: "skills", title: "Which conversations reload a skill file?", description: "Two or more successful file-load calls for the same skill path in a conversation.", unit: "loads", where: [eq("evidence_type", "file_load"), eq("status", "ok"), present("skill_path"), present("conversation_id")],
    measures: [metric("skill-file-loads", "count_distinct", "tool_call_id", ["repository_name", "conversation_id", "skill_path"], "Distinct skill file loads")],
    exploration: view(["repository_name", "conversation_id", "skill_path"], [distinct("tool_call_id", "Distinct file-load calls"), sum("content_bytes", "Retained body bytes"), row("title", "Work")], "reloaded skills", "Inspect skill reloads", "loads", { having: [min("card_tool_call_id")], representative: { orderBy: [{ field: "created_at", dir: "desc", nulls: "last" }] }, labelFields: ["skill_path", "card_title"] }),
    note: "File-load calls only; excludes repeated harness snapshots and invocations. Conversation grain can span agents; matching paths do not establish unchanged bodies or compaction effects." },
  { dataset: "mcp_calls", field: "called_at", id: "empty-mcp-searches", idea: "context-04", category: "research", title: "Which Pharos searches return no items?", description: "Successful MCP searches with an explicitly recorded zero result count, grouped by method.", unit: "calls",
    where: [eq("status", "ok"), eq("result_count", "0"), { any: ["search_work", "search_conversations", "search_conversation_passages", "search_messages", "search_text"].map(name => eq("tool_name", name)) }],
    measures: [metric("empty-searches", "count", "", ["tool_name"], "Searches returning no items")],
    exploration: view(["tool_name"], [count("Zero-item calls"), sum("duration_ms", "Recorded elapsed time")], "search methods", "Inspect zero-item searches", "calls"),
    note: "Pharos MCP searches only. Missing result counts, disabled calls and errors are excluded. A zero-item response does not establish absent knowledge or a later search recovery." },
  { ...calls, id: "file-edit-activity", idea: "rework-01", category: "tools", title: "Which files get edited repeatedly?", description: "Resolved paths with two or more edit calls, with conversation reach and recorded line changes.", unit: "calls", where: [eq("tool_category", "edit"), present("repo_path")],
    measures: [metric("file-edits", "count", "", ["repository_name", "repo_path"], "Edit calls by path")],
    exploration: view(["repository_name", "repo_path"], [count("Edit calls"), conversations, sum("error_count", "Failed calls"), sum("lines_added", "Recorded lines added"), sum("lines_removed", "Recorded lines removed")], "edited paths", "Inspect repeated edits", "calls", { having: [min("card_count")], labelFields: ["repo_path", "repository_name"], context: [{ field: conversations.id, label: "conversations" }] }),
    note: "Counts retained edit calls, including failures. Line changes can count repeated edits; these are not net Git changes, reversals, verification cycles or human effort." },
  { ...calls, id: "environment-failures", idea: "rework-05", category: "errors", title: "Where do setup and access calls fail?", description: "Recorded package, build, container and permission-related failures by repository and error class.", unit: "failures",
    where: [gt("error_count"), { any: [eq("error_type", "permission_denied"), eq("error_type", "file_not_found"), ...["package", "build", "container"].map(category => eq("command_category", category))] }],
    measures: [metric("setup-failures", "count", "", ["repository_name", "error_type"], "Setup and access failure calls")],
    exploration: view(["repository_name", "error_type"], [count("Failure calls"), conversations, distinct("command_name", "Commands")], "repository/error groups", "Inspect setup and access failures", "failures", { context: [{ field: conversations.id, label: "conversations" }] }),
    note: "Uses recorded command/error categories. Build failures can be code errors; missing paths can be ordinary typos. No inferred environment diagnosis, directory blame or exposure-normalized rate." },
  { ...calls, id: "repeated-failed-commands", days: [7, 30], idea: "tools-04", category: "errors", title: "Which failing commands get repeated?", description: "Exact failed build, test and package commands appearing at least twice in a repository.", unit: "calls",
    where: [gt("error_count"), present("command"), { any: ["build", "test", "package"].map(category => eq("command_category", category)) }],
    measures: [metric("repeated-command-errors", "count", "", ["repository_name", "command"], "Failed calls with the same command")],
    exploration: view(["repository_name", "command"], [count("Failed calls"), conversations, sum("duration_ms", "Recorded elapsed time")], "repeated commands", "Inspect repeated failed commands", "calls", { having: [min("card_count")], labelFields: ["command", "repository_name"], context: [{ field: conversations.id, label: "conversations" }] }),
    note: "Exact command text in this window, across conversations. Does not establish adjacent retries, unchanged files or arguments, excess attempts or recovery episodes." },
  { ...attempts, id: "tl1-revisits", idea: "rework-07", category: "delegation", title: "Which TL1 tasks are being revisited?", description: "Attempts explicitly marked as revisits, grouped by project and task.", unit: "attempts", where: [eq("revisit", "true"), present("task_id")],
    measures: [metric("revisit-attempts", "sum", "attempt_count", ["project", "task_id"], "Marked revisit attempts")],
    exploration: view(["project", "task_id"], [sum("attempt_count", "Revisit attempts"), distinct("candidate_id", "Candidates"), sum("cost_usd", "Recorded API equivalent"), row("title", "Work")], "revisited tasks", "Inspect TL1 revisits", "attempts", { representative: { orderBy: [{ field: "started_at", dir: "desc", nulls: "last" }] }, labelFields: ["card_title", "project"] }),
    note: "Uses the explicit TL1 revisit flag. Revisits may be intentional iteration; they do not establish that earlier work failed." },
  { ...attempts, id: "tl1-mixed-dispositions", idea: "models-03", category: "models", title: "Which TL1 candidates have mixed dispositions?", description: "Shared candidates with multiple model/effort configurations and distinct recorded dispositions.", unit: "dispositions", where: [present("task_id"), present("candidate_id"), present("model_effort"), present("disposition")],
    measures: [metric("candidate-dispositions", "count_distinct", "disposition", ["project", "task_id", "candidate_id", "execution_class", "shape_version"], "Distinct attempt dispositions")],
    exploration: view(["project", "task_id", "candidate_id", "execution_class", "shape_version"], [distinct("disposition", "Dispositions"), distinct("model_effort", "Model/effort configurations"), sum("attempt_count", "Attempts"), sum("advanced_count", "Advanced attempts"), sum("error_count", "Error attempts"), sum("escalated_count", "Escalated attempts")], "mixed candidates", "Compare candidate dispositions", "dispositions", { having: [min("card_disposition"), min("card_model_effort")], context: [{ field: "card_attempt_count", label: "attempts" }] }),
    note: "Matches project, task, candidate, execution class and shape version. Attempts need not be simultaneous; mixed dispositions do not establish semantic disagreement or model quality." },
  { ...calls, id: "primary-urls", days: [7, 30], idea: "memory-01", category: "research", title: "Which recorded URLs span conversations?", description: "Primary recorded URLs ranked by distinct conversations, with calls and repository reach.", unit: "conversations", where: [present("url")],
    measures: [metric("url-conversations", "count_distinct", "conversation_id", ["url"], "Conversations per primary URL")],
    exploration: view(["url"], [conversations, count("Calls"), repositories], "URLs", "Explore recorded URLs", "conversations", { context: [{ field: "card_count", label: "calls" }] }),
    note: "Primary URL evidence on retained tool calls. May include supplied URL arguments; does not prove that a page was read, classify documentation, or enumerate every reached URL." },
  { ...messages, id: "work-writing", idea: "human-02", category: "human", title: "Which work needs the most of my writing?", description: "Your words and messages in this window, grouped by canonical work identity.", unit: "words", where: [gt("typed_words"), present("work_id")],
    measures: [metric("work-human-words", "sum", "typed_words", ["work_id"], "Your words by work")],
    exploration: view(["work_id"], [humanWords, count("Messages with your words"), conversations, row("title", "Work"), row("repository_name", "Repository")], "work items", "Compare writing by work", "words", { representative: latestMessage, labelFields: ["card_title"], context: [{ field: "card_count", label: "messages" }] }),
    note: "Period filters apply to the messages themselves. Work titles come from the latest matching message; words are classified typed/dictated words, not measured time or repeat corrections." },
  { ...messages, id: "writing-hours", idea: "rhythm-01", category: "human", title: "When am I writing to agents?", description: "Your words by local hourly interval, using this device’s timezone.", unit: "words", days: [1, 7], calendarTimezone: "local", where: [gt("typed_words")],
    measures: [{ ...metric("hourly-human-words", "sum", "typed_words", ["sent_at"], "Your words by local hour"), dateGrouping: { field: "sent_at", interval: "hour", timeZone: "UTC", range: "observed", includeEmpty: false }, sort: [{ key: "group0", dir: "desc" }], display: { kind: "bar-vertical", xLabel: "Local hour", yLabel: "Words", xFormat: { kind: "datetime", sourceUnit: "iso", style: "custom", pattern: "DD HH:mm" } } }],
    note: "Timezone is captured from this device when opening or collecting the card. Uses typed/dictated words; automated-only messages are excluded. Open the view for the full timeline; activity is not measured productivity." },
];
