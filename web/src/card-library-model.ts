import { queryRecipes, type QueryRecipe } from "./curiosity-data";
import { agentEventDatasets, agentEventsEnabled } from "./agent-events-flag";

export const cardDatasets: Record<string, string> = { model_requests: "Model request timing", conversation_activity: "Conversation input/output", usage: "Token usage", library: "Workspaces", tool_calls: "Tool calls", skill_usages: "Skills", writing_messages: "User messages", tl1_attempts: "TL1 attempts", mcp_calls: "Pharos MCP requests", parallelism: "Parallel activity", rhythm: "Habits", findings: "Findings", safety: "Risky action notices" };
// The Card Library's address: its search, data source, topic tab
// (card_topic=all is the All tab), card kind and sort.
export const cardLibraryParams = ["card_search", "card_data", "card_topic", "card_kind", "card_sort"];
export const cardTopicAll = "all";
export const cardSorts = [["topic", "Topic"], ["data", "Dataset"], ["kind", "Card kind"], ["title", "Title"]] as const;
// The active card definitions; retired cards keep their IDs for old links.
// Cards over the preview's raw event tables (agent-events-flag.ts) are
// offered only while it is on; Agent Events cards always are, as its
// dashboard is listed.
export function cardTemplates(previews = agentEventsEnabled()): QueryRecipe[] {
  return queryRecipes.filter(recipe => !recipe.retired && (previews || !agentEventDatasets.has(recipe.dataset)));
}
